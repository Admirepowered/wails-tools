//go:build windows

package updater

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Apply replaces the running executable with the downloaded build and starts
// the new version. The caller should quit the app right after a nil return.
func (m *Manager) Apply(staged string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if _, err := os.Stat(staged); err != nil {
		return "", fmt.Errorf("更新文件不存在: %w", err)
	}
	backup := exe + ".old"
	_ = os.Remove(backup)

	// Windows allows renaming a running executable, so the new build can take
	// its place immediately.
	renamed := false
	if err := os.Rename(exe, backup); err == nil {
		renamed = true
	}
	if renamed {
		if err := copyFile(staged, exe); err != nil {
			_ = os.Rename(backup, exe)
			return "", fmt.Errorf("替换可执行文件失败: %w", err)
		}
		_ = os.Remove(staged)
		if err := startDetached(exe); err != nil {
			return "", fmt.Errorf("启动新版本失败: %w", err)
		}
		return exe, nil
	}

	// Fallback: a small helper script swaps the file after this process exits.
	newPath := exe + ".new"
	if err := copyFile(staged, newPath); err != nil {
		return "", fmt.Errorf("准备更新文件失败: %w", err)
	}
	script := filepath.Join(os.TempDir(), fmt.Sprintf("wails-tools-update-%d.cmd", time.Now().UnixNano()))
	body := fmt.Sprintf(`@echo off
timeout /t 2 /nobreak >nul
move /y "%s" "%s" >nul
start "" "%s"
del "%%~f0"
`, newPath, exe, exe)
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		return "", err
	}
	cmd := exec.Command("cmd", "/c", script)
	cmd.SysProcAttr = detachedAttr()
	if err := cmd.Start(); err != nil {
		return "", errors.New("无法启动更新脚本: " + err.Error())
	}
	return exe, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := out.ReadFrom(in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func startDetached(exe string) error {
	cmd := exec.Command(exe)
	cmd.Dir = filepath.Dir(exe)
	cmd.SysProcAttr = detachedAttr()
	return cmd.Start()
}
