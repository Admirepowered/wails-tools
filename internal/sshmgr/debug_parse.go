package sshmgr

import "strings"

// ParseSampleForDebug parses raw "KEY::value" lines (diagnostics only).
func ParseSampleForDebug(raw string) SystemInfo {
	info := SystemInfo{}
	var stat1, stat2 string
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "STAT1::"):
			stat1 = strings.TrimSpace(strings.TrimPrefix(trimmed, "STAT1::"))
		case strings.HasPrefix(trimmed, "STAT2::"):
			stat2 = strings.TrimSpace(strings.TrimPrefix(trimmed, "STAT2::"))
		default:
			applyKV(&info, trimmed)
		}
	}
	info.CPUPct = cpuUsage(stat1, stat2)
	return info
}
