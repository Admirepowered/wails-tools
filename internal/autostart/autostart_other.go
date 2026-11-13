//go:build !windows

package autostart

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

func desktopEntryPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "autostart", "wails-tools.desktop")
}

func info() Status {
	path := desktopEntryPath()
	st := Status{Supported: runtime.GOOS == "linux", Location: path}
	if path == "" {
		st.Error = "home directory unavailable"
		return st
	}
	if _, err := os.Stat(path); err == nil {
		st.Enabled = true
		st.Command = path
	}
	if runtime.GOOS != "linux" {
		st.Supported = false
		st.Error = UnsupportedError.Error()
	}
	return st
}

func enable(exe string, args []string) error {
	if runtime.GOOS != "linux" {
		return UnsupportedError
	}
	path := desktopEntryPath()
	if path == "" {
		return errors.New("home directory unavailable")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	exec := commandLine(exe, args)
	content := "[Desktop Entry]\nType=Application\nName=WailsTools\nExec=" + exec + "\nX-GNOME-Autostart-enabled=true\nTerminal=false\n"
	return os.WriteFile(path, []byte(content), 0o644)
}

func disable() error {
	path := desktopEntryPath()
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
