//go:build windows

package autostart

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

func info() Status {
	st := Status{Supported: true, Location: `HKCU\` + runKey}
	key, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return st
		}
		st.Error = err.Error()
		return st
	}
	defer key.Close()
	value, _, err := key.GetStringValue(AppName)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return st
		}
		st.Error = err.Error()
		return st
	}
	st.Enabled = value != ""
	st.Command = value
	return st
}

func enable(exe string, args []string) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return fmt.Errorf("open run key: %w", err)
	}
	defer key.Close()
	return key.SetStringValue(AppName, commandLine(exe, args))
}

func disable() error {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}
	defer key.Close()
	if err := key.DeleteValue(AppName); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}
