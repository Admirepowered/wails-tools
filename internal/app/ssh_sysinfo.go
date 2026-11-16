package app

import (
	"errors"
	"strings"
	"time"

	"wailstools/internal/sshmgr"
)

// SSHSystemInfo reports CPU / memory / GPU facts of the remote host.
func (a *App) SSHSystemInfo(sessionID string) (sshmgr.SystemInfo, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return sshmgr.SystemInfo{}, errors.New("session is required")
	}
	info, err := a.ssh.GatherSystemInfo(sessionID)
	if err == nil && (info.CPUModel != "" || info.MemTotalMB > 0) {
		return info, nil
	}
	// One retry: ssh exec sessions occasionally time out under load, and the UI
	// prefers a slightly stale sample over an error.
	time.Sleep(300 * time.Millisecond)
	return a.ssh.GatherSystemInfo(sessionID)
}

// SSHCwd returns the shell's current directory (used to sync the file browser).
func (a *App) SSHCwd(sessionID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", errors.New("session is required")
	}
	out, err := a.ssh.Exec(sessionID, `readlink /proc/$(pgrep -n -P $(pgrep -n -x bash || pgrep -n -x zsh || pgrep -n -x sh) 2>/dev/null)/cwd 2>/dev/null || echo "$HOME"`)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

