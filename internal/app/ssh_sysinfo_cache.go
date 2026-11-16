package app

import (
	"sync"
)

// systemInfoCache keeps the last successful sample per SSH session. Hostname and
// CPU model never change, and a single failed `sh` invocation should not blank
// the whole status bar — so every field is merged with the cached value.
var sysInfoMu sync.Mutex
var sysInfoCache = map[string]SystemInfoSnapshot{}

// SystemInfoSnapshot mirrors sshmgr.SystemInfo for the cache.
type SystemInfoSnapshot struct {
	CPUModel   string
	CPUCores   int
	CPUPct     float64
	MemTotalMB int64
	MemUsedMB  int64
	MemPct     float64
	Load1      float64
	Uptime     string
	Hostname   string
	DiskTotal  int64
	DiskUsed   int64
	DiskPct    float64
}

func (a *App) cachedSystemInfo(sessionID string) (SystemInfoSnapshot, bool) {
	sysInfoMu.Lock()
	defer sysInfoMu.Unlock()
	v, ok := sysInfoCache[sessionID]
	return v, ok
}

func (a *App) storeSystemInfo(sessionID string, v SystemInfoSnapshot) {
	sysInfoMu.Lock()
	sysInfoCache[sessionID] = v
	sysInfoMu.Unlock()
}
