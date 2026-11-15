//go:build windows

package sniproxy

import (
	"os/exec"
	"syscall"
)

// execCertutil imports the root certificate into the current user's trusted
// root store (no administrator prompt needed).
func execCertutil(certPath string) ([]string, error) {
	cmd := exec.Command("certutil", "-user", "-addstore", "-f", "Root", certPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	return []string{string(out)}, err
}
