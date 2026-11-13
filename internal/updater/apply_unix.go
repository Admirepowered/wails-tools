//go:build !windows

package updater

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// Apply swaps the executable atomically and restarts the app.
func (m *Manager) Apply(staged string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if err := os.Chmod(staged, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(staged, exe); err != nil {
		return "", fmt.Errorf("replace executable: %w", err)
	}
	cmd := exec.Command(exe)
	cmd.Dir = filepath.Dir(exe)
	cmd.SysProcAttr = detachedAttr()
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("restart: %w", err)
	}
	return exe, nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o755)
}

func detachedAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
