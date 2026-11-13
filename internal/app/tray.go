package app

import (
	"context"
	"os"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"wailstools/internal/paths"
	"wailstools/internal/serverlink"
	"wailstools/internal/settings"
	"wailstools/internal/tray"
	"wailstools/internal/version"
)

// Tray menu command identifiers.
const (
	trayCmdShow = 1 + iota
	trayCmdHide
	trayCmdCapturePrimary
	trayCmdCaptureAll
	trayCmdClipboard
	trayCmdSync
	trayCmdServer
	trayCmdAutostart
	trayCmdDataDir
	trayCmdQuit
)

// startTray installs the tray icon when the platform supports it.
func (a *App) startTray() {
	if a.tray != nil || !tray.Supported() {
		return
	}
	iconPath := ""
	if p, err := paths.Join("tray.ico"); err == nil {
		if err := os.WriteFile(p, tray.BuildICO(32), 0o644); err == nil {
			iconPath = p
		}
	}
	t, err := tray.New(tray.Options{
		Tooltip:       a.trayTooltip(),
		IconPath:      iconPath,
		Menu:          a.trayMenu,
		OnMenu:        a.trayAction,
		OnDoubleClick: a.ShowMainWindow,
	})
	if err != nil {
		a.emit("tray:error", err.Error())
		return
	}
	a.mu.Lock()
	a.tray = t
	a.mu.Unlock()
	a.emit("tray:state", map[string]any{"available": true, "enabled": true})
}

// stopTray removes the tray icon.
func (a *App) stopTray() {
	a.mu.Lock()
	t := a.tray
	a.tray = nil
	a.mu.Unlock()
	if t != nil {
		_ = t.Remove()
	}
}

// linkStatus returns the server status, tolerating an uninitialised link.
func (a *App) linkStatus() serverlink.Status {
	if a.link == nil {
		return serverlink.Status{}
	}
	return a.link.Status()
}

func (a *App) trayTooltip() string {
	parts := []string{"WailsTools " + version.Version}
	st := a.linkStatus()
	switch {
	case !st.Connected:
		parts = append(parts, "服务器未连接")
	default:
		transport := "TCP"
		if st.Transport == "udp" {
			transport = "UDP/QUIC"
		}
		parts = append(parts, transport+" 已连接")
		if st.User != nil && st.User.Username != "" {
			parts = append(parts, "@"+st.User.Username)
		}
	}
	return tray.Tooltip(parts...)
}

func (a *App) trayMenu() []tray.MenuItem {
	cfg := a.settings.Get()
	connected := a.linkStatus().Connected
	serverLabel := "连接同步服务器"
	if connected {
		serverLabel = "断开同步服务器"
	}
	items := []tray.MenuItem{
		{ID: trayCmdShow, Label: "显示主窗口"},
		{ID: trayCmdHide, Label: "隐藏窗口"},
		{Separator: true},
		{ID: trayCmdCapturePrimary, Label: "截图 · 主屏"},
		{ID: trayCmdCaptureAll, Label: "截图 · 全部屏幕"},
		{ID: trayCmdClipboard, Label: "剪贴板图片自动保存", Checked: cfg.Screenshots.WatchClipboard},
		{Separator: true},
		{ID: trayCmdSync, Label: "立即同步笔记"},
		{ID: trayCmdServer, Label: serverLabel, Checked: connected},
		{Separator: true},
		{ID: trayCmdAutostart, Label: "开机自启动", Checked: cfg.Startup.Enabled},
		{ID: trayCmdDataDir, Label: "打开数据目录"},
		{Separator: true},
		{ID: trayCmdQuit, Label: "退出 WailsTools"},
	}
	return items
}

func (a *App) trayAction(id int) {
	switch id {
	case trayCmdShow:
		a.ShowMainWindow()
	case trayCmdHide:
		a.HideMainWindow()
	case trayCmdCapturePrimary, trayCmdCaptureAll:
		display := 0
		if id == trayCmdCaptureAll {
			display = -1
		}
		go func() {
			item, err := a.shots.Capture(display, "screen")
			if err != nil {
				a.emit("tray:error", err.Error())
				return
			}
			a.emit("screenshot:saved", item)
			a.refreshTrayTooltip()
		}()
	case trayCmdClipboard:
		cfg := a.settings.Get().Screenshots
		cfg.WatchClipboard = !cfg.WatchClipboard
		go func() {
			if _, err := a.SaveScreenshotSettings(cfg); err != nil {
				a.emit("tray:error", err.Error())
			}
		}()
	case trayCmdSync:
		go func() {
			res, err := a.SyncNotes()
			if err != nil {
				a.emit("tray:error", err.Error())
				return
			}
			a.emit("notes:synced", res)
		}()
	case trayCmdServer:
		go func() {
			if a.linkStatus().Connected {
				if a.link != nil {
					_ = a.link.Close()
				}
				return
			}
			if a.link == nil {
				return
			}
			if _, err := a.ConnectServer(); err != nil {
				a.emit("tray:error", err.Error())
			}
		}()
	case trayCmdAutostart:
		cfg := a.settings.Get().Startup
		go func() {
			if _, err := a.SetAutostart(!cfg.Enabled); err != nil {
				a.emit("tray:error", err.Error())
			}
		}()
	case trayCmdDataDir:
		_ = a.OpenDataDir()
	case trayCmdQuit:
		a.quit()
	}
}

// refreshTrayTooltip updates the hover text after state changes.
func (a *App) refreshTrayTooltip() {
	a.mu.Lock()
	t := a.tray
	a.mu.Unlock()
	if t == nil {
		return
	}
	_ = t.UpdateTooltip(a.trayTooltip())
}

// ShowMainWindow restores and focuses the main window.
func (a *App) ShowMainWindow() {
	if a.ctx == nil {
		return
	}
	wruntime.WindowShow(a.ctx)
	wruntime.WindowUnminimise(a.ctx)
}

// HideMainWindow sends the window to the tray.
func (a *App) HideMainWindow() {
	if a.ctx == nil {
		return
	}
	wruntime.WindowHide(a.ctx)
}

// TrayAvailable reports whether the platform has a tray implementation.
func (a *App) TrayAvailable() bool { return tray.Supported() }

// TrayEnabled reports whether the tray icon is currently installed.
func (a *App) TrayEnabled() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tray != nil
}

// SetTrayEnabled turns the tray icon on or off.
func (a *App) SetTrayEnabled(enabled bool) (settings.StartupSettings, error) {
	cfg, err := a.settings.Update(func(s *settings.Settings) error {
		s.Startup.TrayEnabled = enabled
		return nil
	})
	if err != nil {
		return settings.StartupSettings{}, err
	}
	if enabled {
		a.startTray()
	} else {
		a.stopTray()
	}
	a.emit("tray:state", map[string]any{"available": tray.Supported(), "enabled": enabled})
	return cfg.Startup, nil
}

// SetCloseToTray controls whether closing the window hides it instead of quitting.
func (a *App) SetCloseToTray(enabled bool) (settings.StartupSettings, error) {
	cfg, err := a.settings.Update(func(s *settings.Settings) error {
		s.Startup.CloseToTray = enabled
		return nil
	})
	return cfg.Startup, err
}

// OnBeforeClose implements the close-to-tray behaviour. It returns true to
// prevent the window from closing.
func (a *App) OnBeforeClose(ctx context.Context) bool {
	if a.isQuitting() {
		return false
	}
	cfg := a.settings.Get().Startup
	if !cfg.CloseToTray || !cfg.TrayEnabled || !tray.Supported() {
		return false
	}
	a.HideMainWindow()
	return true
}

// StartHidden reports whether the app should start without showing the window.
func (a *App) StartHidden() bool {
	cfg := a.settings.Get().Startup
	return cfg.StartMinimized && cfg.TrayEnabled && tray.Supported()
}

func (a *App) isQuitting() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.quitting
}
