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
	sess.Stderr = nil
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
	return sanitizeText(out.String()), nil
}

// sanitizeText drops bytes that are not valid UTF-8: Wails marshals strings as
// JSON, and invalid bytes would show up as boxes in the UI.
func sanitizeText(s string) string {
	if isValidUTF8(s) {
		return s
	}
	var b strings.Builder
	for _, r := range strings.ToValidUTF8(s, "") {
		b.WriteRune(r)
	}
	return b.String()
}

func isValidUTF8(s string) bool {
	return strings.ToValidUTF8(s, "\uFFFD") == s
}

// SystemInfo is the remote host snapshot shown under the SSH workspace.
type SystemInfo struct {
	CPUModel    string    `json:"cpuModel"`
	CPUCores    int       `json:"cpuCores"`
	CPUPct      float64   `json:"cpuPct"`
	MemTotalMB  int64     `json:"memTotalMb"`
	MemUsedMB   int64     `json:"memUsedMb"`
	MemPct      float64   `json:"memPct"`
	Load1       float64   `json:"load1"`
	Uptime      string    `json:"uptime"`
	Hostname    string    `json:"hostname"`
	DiskTotalMB int64     `json:"diskTotalMb"`
	DiskUsedMB  int64     `json:"diskUsedMb"`
	DiskPct     float64   `json:"diskPct"`
	GPUs        []GPUInfo `json:"gpus"`
	GPUSource   string    `json:"gpuSource,omitempty"`
}

// GPUInfo is one NVIDIA device.
type GPUInfo struct {
	Name     string  `json:"name"`
	UtilPct  float64 `json:"utilPct"`
	MemUsed  int64   `json:"memUsedMb"`
	MemTotal int64   `json:"memTotalMb"`
	TempC    float64 `json:"tempC"`
}

// staticScript runs once per session: identity, uptime and the root filesystem
// size (all of which rarely change).
const staticScript = `echo "HOST::$(hostname 2>/dev/null || uname -n 2>/dev/null)"
echo "CPU::$(grep -m1 'model name' /proc/cpuinfo 2>/dev/null | cut -d: -f2-)"
echo "CORES::$(nproc 2>/dev/null)"
echo "UP::$(cut -d. -f1 /proc/uptime 2>/dev/null)"
echo "DISK::$(df -P -k / 2>/dev/null | tail -n1)"`

// dynamicScript returns the changing numbers: memory, load, CPU ticks and GPU.
const dynamicScript = `echo "MEM::$(grep -E '^MemTotal:|^MemAvailable:' /proc/meminfo 2>/dev/null | tr '\n' ' ')"
echo "LOAD::$(cut -d' ' -f1 /proc/loadavg 2>/dev/null)"
echo "STAT1::$(grep -m1 '^cpu ' /proc/stat 2>/dev/null)"
sleep 0.4
echo "STAT2::$(grep -m1 '^cpu ' /proc/stat 2>/dev/null)"
echo "DISK::$(df -P -k / 2>/dev/null | tail -n1)"
echo "GPU::$(command -v nvidia-smi >/dev/null 2>&1 && nvidia-smi --query-gpu=name,utilization.gpu,memory.used,memory.total,temperature.gpu --format=csv,noheader,nounits 2>/dev/null | tr '\n' ';')"`

// GatherSystemInfo collects the static facts once and the dynamic ones per call.
func (m *Manager) GatherSystemInfo(sessionID string) (SystemInfo, error) {
	info := SystemInfo{}
	rawStatic, staticErr := m.Exec(sessionID, staticScript)
	if staticErr == nil {
		for _, line := range strings.Split(rawStatic, "\n") {
			line = strings.TrimSpace(line)
			idx := strings.Index(line, "::")
			if idx < 0 {
				continue
			}
			key, value := line[:idx], strings.TrimSpace(line[idx+2:])
			switch key {
			case "HOST":
				info.Hostname = asciiOnly(value)
			case "CPU":
				info.CPUModel = asciiOnly(strings.Join(strings.Fields(value), " "))
			case "CORES":
				fmt.Sscanf(value, "%d", &info.CPUCores)
			case "UP":
				var secs int64
				fmt.Sscanf(value, "%d", &secs)
				info.Uptime = formatUptime(secs)
			case "DISK":
				if total, used, pct, ok := parseDF(value); ok {
					info.DiskTotalMB, info.DiskUsedMB, info.DiskPct = total, used, pct
				}
			}
		}
	}
	raw, err := m.Exec(sessionID, dynamicScript)
	if err != nil {
		return info, err
	}
	var stat1, stat2 string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		idx := strings.Index(line, "::")
		if idx < 0 {
			continue
		}
		key, value := line[:idx], strings.TrimSpace(line[idx+2:])
		switch key {
		case "STAT1":
			stat1 = value
		case "STAT2":
			stat2 = value
		default:
			applyKV(&info, line)
		}
	}
	info.CPUPct = cpuUsage(stat1, stat2)
	return info, nil
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if v := strings.TrimSpace(line); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func parseMeminfo(value string) (totalMB, usedMB int64, pct float64) {
	tokens := strings.Fields(value)
	var totalKB, availKB int64
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
	for i, f := range fields[1:] {
		var v int64
		fmt.Sscanf(f, "%d", &v)
		total += v
		if i == 3 || i == 4 {
			idle += v
		}
	}
	return total, idle
}

// parseDF parses one `df -P -k` line: "dev 1K-blocks used avail capacity mount".
func parseDF(line string) (totalMB, usedMB int64, pct float64, ok bool) {
	fields := strings.Fields(line)
	if len(fields) < 4 {
		return 0, 0, 0, false
	}
	var totalKB, usedKB int64
	if _, err := fmt.Sscanf(fields[1], "%d", &totalKB); err != nil || totalKB <= 0 {
		return 0, 0, 0, false
	}
	if _, err := fmt.Sscanf(fields[2], "%d", &usedKB); err != nil {
		return 0, 0, 0, false
	}
	return totalKB / 1024, usedKB / 1024, float64(usedKB) / float64(totalKB) * 100, true
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

// asciiOnly keeps printable ASCII and collapses whitespace. Hostnames and CPU
// model strings are ASCII; anything else is a decoding artefact that would show
// up as placeholder boxes in the UI.
func asciiOnly(s string) string {
	var b strings.Builder
	lastSpace := false
	for _, r := range s {
		switch {
		case r == '\t' || r == '\n' || r == '\r' || r == ' ':
			if !lastSpace && b.Len() > 0 {
				b.WriteByte(' ')
				lastSpace = true
			}
		case r >= 0x20 && r < 0x7f:
			b.WriteRune(r)
			lastSpace = false
		}
	}
	return strings.TrimSpace(b.String())
}

// applyKV parses one "KEY::value" line shared by both scripts.
func applyKV(info *SystemInfo, line string) {
	idx := strings.Index(line, "::")
	if idx < 0 {
		return
	}
	key, value := line[:idx], strings.TrimSpace(line[idx+2:])
	switch key {
	case "HOST":
		info.Hostname = asciiOnly(value)
	case "CPU":
		info.CPUModel = asciiOnly(strings.Join(strings.Fields(value), " "))
	case "CORES":
		fmt.Sscanf(value, "%d", &info.CPUCores)
	case "UP":
		var secs int64
		fmt.Sscanf(value, "%d", &secs)
		info.Uptime = formatUptime(secs)
	case "DISK":
		if total, used, pct, ok := parseDF(value); ok {
			info.DiskTotalMB, info.DiskUsedMB, info.DiskPct = total, used, pct
		}
	case "MEM":
		info.MemTotalMB, info.MemUsedMB, info.MemPct = parseMeminfo(value)
	case "LOAD":
		fields := strings.Fields(value)
		if len(fields) > 0 {
			fmt.Sscanf(fields[0], "%f", &info.Load1)
		}
	case "GPU":
		info.GPUs = parseNvidiaSMI(value)
		if len(info.GPUs) > 0 {
			info.GPUSource = "nvidia-smi"
		}
	}
}
