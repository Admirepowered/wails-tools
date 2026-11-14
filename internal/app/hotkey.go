package app

import (
	"errors"
	"strings"

	"wailstools/internal/hotkey"
	"wailstools/internal/settings"
)

// registerScreenshotHotkey installs the configured global shortcut.
func (a *App) registerScreenshotHotkey() {
	combo := strings.TrimSpace(a.settings.Get().Screenshots.Hotkey)
	if combo == "" || !hotkey.Supported() {
		return
	}
	if _, err := hotkey.Parse(combo); err != nil {
		a.emit("hotkey:error", err.Error())
		return
	}
	if a.hotkey != nil {
		a.hotkey.Unregister()
		a.hotkey = nil
	}
	h, err := hotkey.Register(hotkey.Options{Combo: combo, OnTrigger: func() {
		a.CaptureScreenshot(hotkeyScreenIndex())
	}})
	if err != nil {
		a.emit("hotkey:error", err.Error())
		return
	}
	a.hotkey = h
	a.emit("hotkey:registered", combo)
}

// SaveScreenshotHotkey stores a new shortcut and re-registers it.
func (a *App) SaveScreenshotHotkey(combo string) (string, error) {
	combo = strings.TrimSpace(combo)
	if combo != "" {
		if _, err := hotkey.Parse(combo); err != nil {
			return "", err
		}
	}
	if _, err := a.settings.Update(func(s *settings.Settings) error {
		s.Screenshots.Hotkey = combo
		return nil
	}); err != nil {
		return "", err
	}
	a.registerScreenshotHotkey()
	if combo == "" {
		return "", nil
	}
	if a.hotkey == nil {
		return "", errors.New("快捷键注册失败（可能已被占用）")
	}
	return a.hotkey.Combo(), nil
}

// HotkeySupported reports platform support for global shortcuts.
func (a *App) HotkeySupported() bool { return hotkey.Supported() }

func hotkeyScreenIndex() int { return 0 }
