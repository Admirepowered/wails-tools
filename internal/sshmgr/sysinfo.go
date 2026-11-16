package sshmgr

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Exec runs a one-shot command over a new SSH session and returns stdout.
func (m *Manager) Exec(sessionID, command string) (string, error) {
	client, ok := m.Client(sessionID)
	if !ok || client == nil || client.SSH == nil {
		return "", errors.New("ssh session not found")
	}
	sess, err := client.SSH.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	var out bytes.Buffer
	sess.Stdout = &out
	sess.Stderr = &out
	if err := sess.Start(command); err != nil {
		return "", err
	}
	done := make(chan error, 1)
	go func() { done <- sess.Wait() }()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		_ = sess.Signal(ssh.SIGTERM)
	}
	return out.String(), nil
}

// SystemInfo is the remote host snapshot shown above the SSH terminal.
type SystemInfo struct {
	CPUModel   string    `json:"cpuModel"`
	CPUCores   int       `json:"cpuCores"`
	CPUPct     float64   `json:"cpuPct"`
	MemTotalMB int64     `json:"memTotalMb"`
	MemUsedMB  int64     `json:"memUsedMb"`
	MemPct     float64   `json:"memPct"`
	Load1      float64   `json:"load1"`
	Uptime     string    `json:"uptime"`
	Hostname   string    `json:"hostname"`
	GPUs       []GPUInfo `json:"gpus"`
	GPUSource  string    `json:"gpuSource,omitempty"`
}

// GPUInfo is one NVIDIA device.
type GPUInfo struct {
	Name     string  `json:"name"`
	UtilPct  float64 `json:"utilPct"`
	MemUsed  int64   `json:"memUsedMb"`
	MemTotal int64   `json:"memTotalMb"`
	TempC    float64 `json:"tempC"`
}

// GatherSystemInfo collects CPU/memory/GPU facts from the remote host.
func (m *Manager) GatherSystemInfo(sessionID string) (SystemInfo, error) {
	out, err := m.Exec(sessionID, `sh -c '
echo "HOST::$(hostname 2>/dev/null)"
echo "CPU::$(grep -m1 "model name" /proc/cpuinfo 2>/dev/null | cut -d: -f2-)"
echo "CORES::$(grep -c ^processor /proc/cpuinfo 2>/dev/null)"
echo "LOAD::$(cut -d" " -f1-3 /proc/loadavg 2>/dev/null)"
echo "MEM::$(grep -E "^(MemTotal|MemAvailable):" /proc/meminfo 2>/dev/null | tr "\n" " ")"
echo "UP::$(cut -d. -f1 /proc/uptime 2>/dev/null)"
echo "STAT1::$(grep -m1 "^cpu " /proc/stat 2>/dev/null)"
sleep 1
echo "STAT2::$(grep -m1 "^cpu " /proc/stat 2>/dev/null)"
echo "GPU::$(command -v nvidia-smi >/dev/null 2>&1 && nvidia-smi --query-gpu=name,utilization.gpu,memory.used,memory.total,temperature.gpu --format=csv,noheader,nounits 2>/dev/null | tr "\n" ";")"
'`)
	if err != nil {
		return SystemInfo{}, err
	}
	return parseSystemInfo(out), nil
}

func parseSystemInfo(raw string) SystemInfo {
	info := SystemInfo{}
	var stat1, stat2 string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		idx := strings.Index(line, "::")
		if idx < 0 {
			continue
		}
		key, value := line[:idx], strings.TrimSpace(line[idx+2:])
		switch key {
		case "HOST":
			info.Hostname = value
		case "CPU":
			info.CPUModel = strings.Join(strings.Fields(value), " ")
		case "CORES":
			fmt.Sscanf(value, "%d", &info.CPUCores)
		case "LOAD":
			fields := strings.Fields(value)
			if len(fields) > 0 {
				fmt.Sscanf(fields[0], "%f", &info.Load1)
			}
		case "MEM":
			info.MemTotalMB, info.MemUsedMB, info.MemPct = parseMeminfo(value)
		case "UP":
			var secs int64
			fmt.Sscanf(value, "%d", &secs)
			info.Uptime = formatUptime(secs)
		case "STAT1":
			stat1 = value
		case "STAT2":
			stat2 = value
		case "GPU":
			info.GPUs = parseNvidiaSMI(value)
			if len(info.GPUs) > 0 {
				info.GPUSource = "nvidia-smi"
			}
		}
	}
	info.CPUPct = cpuUsage(stat1, stat2)
	return info
}

func parseMeminfo(value string) (totalMB, usedMB int64, pct float64) {
	var totalKB, availKB int64
	for _, part := range strings.Fields(value) {
		switch {
		case strings.HasPrefix(part, "MemTotal:"):
		case strings.HasPrefix(part, "MemAvailable:"):
		}
	}
	// Fields look like: MemTotal: 16384000 kB MemAvailable: 8000000 kB
	tokens := strings.Fields(value)
	for i := 0; i+1 < len(tokens); i++ {
		switch tokens[i] {
		case "MemTotal:":
			fmt.Sscanf(tokens[i+1], "%d", &totalKB)
		case "MemAvailable:":
			fmt.Sscanf(tokens[i+1], "%d", &availKB)
		}
	}
	if totalKB <= 0 {
		return 0, 0, 0
	}
	totalMB = totalKB / 1024
	usedMB = (totalKB - availKB) / 1024
	pct = float64(totalKB-availKB) / float64(totalKB) * 100
	return totalMB, usedMB, pct
}

func cpuUsage(stat1, stat2 string) float64 {
	t1, i1 := parseCPUStat(stat1)
	t2, i2 := parseCPUStat(stat2)
	dt, di := t2-t1, i2-i1
	if dt <= 0 {
		return 0
	}
	return float64(dt-di) / float64(dt) * 100
}

func parseCPUStat(line string) (total, idle int64) {
	fields := strings.Fields(line)
	if len(fields) < 5 {
		return 0, 0
	}
	var values []int64
	for _, f := range fields[1:] {
		var v int64
		fmt.Sscanf(f, "%d", &v)
		values = append(values, v)
	}
	for i, v := range values {
		total += v
		if i == 3 || i == 4 { // idle + iowait
			idle += v
		}
	}
	return total, idle
}

func parseNvidiaSMI(value string) []GPUInfo {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	var out []GPUInfo
	for _, entry := range strings.Split(value, ";") {
		parts := strings.Split(entry, ",")
		if len(parts) < 4 {
			continue
		}
		g := GPUInfo{Name: strings.TrimSpace(parts[0])}
		fmt.Sscanf(strings.TrimSpace(parts[1]), "%f", &g.UtilPct)
		fmt.Sscanf(strings.TrimSpace(parts[2]), "%d", &g.MemUsed)
		fmt.Sscanf(strings.TrimSpace(parts[3]), "%d", &g.MemTotal)
		if len(parts) > 4 {
			fmt.Sscanf(strings.TrimSpace(parts[4]), "%f", &g.TempC)
		}
		out = append(out, g)
	}
	return out
}

func formatUptime(secs int64) string {
	if secs <= 0 {
		return ""
	}
	d := secs / 86400
	h := (secs % 86400) / 3600
	m := (secs % 3600) / 60
	switch {
	case d > 0:
		return fmt.Sprintf("%d天%d小时", d, h)
	case h > 0:
		return fmt.Sprintf("%d小时%d分", h, m)
	default:
		return fmt.Sprintf("%d分", m)
	}
}
