// Package app contains the Wails-bound application services.
package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"wailstools/internal/dlengine"
	"wailstools/internal/geodata"
	"wailstools/internal/hotkey"
	"wailstools/internal/netdisk"
	"wailstools/internal/notes"
	"wailstools/internal/paths"
	"wailstools/internal/proxysvc"
	"wailstools/internal/proxyx"
	"wailstools/internal/secretstore"
	"wailstools/internal/serverlink"
	"wailstools/internal/settings"
	"wailstools/internal/shot"
	"wailstools/internal/sshmgr"
	"wailstools/internal/subscription"
	"wailstools/internal/transfermgr"
	"wailstools/internal/tray"
	"wailstools/internal/updater"
	"wailstools/internal/version"
)

// App is the root service bound to the frontend.
type App struct {
	ctx context.Context

	settings    *settings.Store
	ssh         *sshmgr.Manager
	ftp         *ftpHub
	engine      *dlengine.Engine
	notes       *notes.Store
	link        *serverlink.Client
	netdisk     *netdisk.Manager
	vault       *secretstore.Vault
	updater     *updater.Manager
	subs        *subscription.Manager
	shots       *shot.Manager
	tray        *tray.Tray
	quitting    bool
	proxySvc    *proxysvc.Service
	customNodes *proxysvc.NodeStore
	transfers   *transfermgr.Manager
	geo         *geodata.DB
	media       *mediaRegistry
	hotkey      *hotkey.Hotkey
	log         *fileLogger

	mu      sync.Mutex
	terms   map[string]*sshmgr.Terminal
	ndMu    sync.Mutex
	ndSig   string
	rootDir string
}

// New constructs the application graph.
func New() (*App, error) {
	root, err := paths.Root()
	if err != nil {
		return nil, err
	}
	vault, err := secretstore.Shared(root)
	if err != nil {
		return nil, err
	}
	store, err := settings.NewStore("")
	if err != nil {
		return nil, err
	}
	sshDir, err := paths.Dir("ssh")
	if err != nil {
		return nil, err
	}
	sshStore, err := sshmgr.Open(sshDir)
	if err != nil {
		return nil, err
	}
	link := serverlink.New(store, nil)
	link.RestoreToken()
	a := &App{
		settings: store,
		link:     link,
		vault:    vault,
		rootDir:  root,
		terms:    map[string]*sshmgr.Terminal{},
	}
	a.ssh = sshmgr.NewManager(sshStore, func(scope string) settings.ProxyRule {
		return store.ProxyFor(scope)
	}, link, sshDir)

	a.ftp = newFTPHub(store, vault)
	eng, err := dlengine.NewEngine(store, "", func(ev dlengine.Event) {
		a.emit("download:event", ev)
	})
	if err != nil {
		return nil, err
	}
	a.engine = eng

	notesDir, err := paths.Dir("notes")
	if err != nil {
		return nil, err
	}
	ns, err := notes.Open(notesDir)
	if err != nil {
		return nil, err
	}
	a.notes = ns

	// Netdisk accounts reuse the local vault for cookie storage.
	ndDir, err := paths.Dir("netdisk")
	if err != nil {
		return nil, err
	}
	if mgr, err := netdisk.NewManager(netdisk.NewStore(
		filepath.Join(ndDir, "accounts.json"),
		filepath.Join(ndDir, "cookies.json"),
	)); err == nil {
		a.netdisk = mgr
	} else {
		fmt.Fprintf(os.Stderr, "netdisk manager unavailable: %v\n", err)
	}
	link.SetStateHandler(func(st serverlink.Status) {
		a.emit("server:status", st)
		a.refreshTrayTooltip()
	})
	a.updater = updater.New(store, link, nil)
	updater.CleanupOld()

	subsDir, err := paths.Dir("subscriptions")
	if err != nil {
		return nil, err
	}
	subStore, err := subscription.Open(subsDir)
	if err != nil {
		return nil, err
	}
	a.subs = subscription.NewManager(subStore, store, nil)
	a.subs.SetUpdateHandler(func(res subscription.UpdateResult) {
		a.emit("subscription:updated", res)
	})
	a.proxySvc = proxysvc.New(func(format string, args ...any) {
		if a.log != nil {
			a.log.Printf(format, args...)
		}
	})
	nodesDir, nerr := paths.Dir("nodes")
	if nerr == nil {
		if ns, err := proxysvc.OpenNodes(nodesDir); err == nil {
			a.customNodes = ns
		}
	}
	if root != "" {
		a.log = newFileLogger(root)
		a.geo = geodata.Open(root)
	}
	a.media = newMediaRegistry()
	a.initTransfers()
	a.shots = shot.NewManager(store, nil)
	a.shots.SetSavedHandler(func(item shot.Item) {
		if !a.settings.Get().Screenshots.AutoUpload || a.link == nil || !a.link.Available() {
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			target := "/screenshots/" + filepath.Base(item.Path)
			_ = a.link.MkdirRemote(ctx, "/screenshots")
			if _, err := a.link.Upload(ctx, item.Path, target, nil); err != nil {
				a.emit("screenshot:upload-failed", err.Error())
				return
			}
			a.emit("screenshot:uploaded", map[string]any{"path": target, "name": filepath.Base(item.Path)})
		}()
	})
	a.shots.SetSavedHandler(func(item shot.Item) {
		a.emit("screenshot:saved", item)
	})
	return a, nil
}

// Startup is called by Wails when the window is ready.
func (a *App) Startup(ctx context.Context) {
	a.ctx = ctx
	if cfg := a.settings.Get(); cfg.Screenshots.WatchClipboard {
		if err := a.shots.StartClipboardWatch(); err != nil {
			a.emit("screenshot:watch", map[string]any{"watching": false, "error": err.Error()})
		}
	}
	if a.subs != nil {
		a.subs.StartAutoRefresh(context.Background())
	}
	if a.settings.Get().Startup.TrayEnabled {
		a.startTray()
	}
	a.registerScreenshotHotkey()
	if cfg := a.settings.Get(); cfg.Update.AutoCheck {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			res, err := a.updater.Check(ctx)
			if err != nil {
				a.emit("update:check", map[string]any{"error": err.Error()})
				return
			}
			if !res.Available || res.Release == nil {
				return
			}
			a.emit("update:available", map[string]any{
				"latest": res.Latest, "source": res.Source, "transport": updater.DescribeTransport(res.GitHub),
			})
			if a.settings.Get().Update.AutoInstall {
				path, derr := a.updater.Download(ctx, *res.Release, nil)
				if derr != nil {
					a.emit("update:check", map[string]any{"error": derr.Error()})
					return
				}
				if _, aerr := a.ApplyUpdate(path); aerr != nil {
					a.emit("update:check", map[string]any{"error": aerr.Error()})
				}
			}
		}()
	}
	go a.restoreActiveNode()
	if a.settings.Get().Server.AutoConnect {
		go func() {
			c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if _, err := a.link.Connect(c); err != nil {
				a.emit("server:error", err.Error())
				return
			}
			// Restore the saved session so the account is logged in automatically.
			// 更新重启后令牌可能还没落盘，先等几次再判定是否已登录。
			for i := 0; i < 3 && a.link.Token() == ""; i++ {
				time.Sleep(2 * time.Second)
			}
			if a.link.Token() != "" {
				// 更新后重启时配置/令牌可能尚未落盘完成，重试几次再判定失效。
				var lastErr error
				logged := false
				for attempt := 0; attempt < 3; attempt++ {
					if attempt > 0 {
						time.Sleep(2 * time.Second)
					}
					if u, err := a.link.Me(c); err == nil {
						a.emit("server:login", map[string]any{"username": u.Username, "auto": true})
						logged = true
						break
					} else {
						lastErr = err
					}
				}
				if !logged {
					msg := ""
					if lastErr != nil {
						msg = lastErr.Error()
					}
					if strings.Contains(msg, "unauthorized") || strings.Contains(msg, "expired") || strings.Contains(msg, "exists") {
						_ = a.link.Logout(c)
						a.emit("server:login-expired", map[string]any{"error": msg})
					} else {
						a.emit("server:error", "自动登录失败: "+msg)
					}
				}
			}
			if a.subs != nil {
				sctx, scancel := context.WithTimeout(context.Background(), 40*time.Second)
				defer scancel()
				if _, err := a.SyncSubscriptions(); err != nil {
					a.emit("subscription:error", err.Error())
				} else {
					a.emit("subscription:synced", map[string]any{"auto": true})
				}
				_ = sctx
			}
		}()
	}
}

// Shutdown releases resources.
func (a *App) Shutdown(ctx context.Context) {
	for _, t := range a.terms {
		_ = t.Close()
	}
	a.ssh.DisconnectAll()
	a.ftp.closeAll()
	_ = a.link.Close()
	a.engine.PauseAll()
	if a.subs != nil {
		a.subs.StopAutoRefresh()
	}
	if a.shots != nil {
		a.shots.StopClipboardWatch()
	}
	a.stopTray()
	if a.log != nil {
		a.log.Printf("=== 退出 ===")
		a.log.Close()
	}
	if a.hotkey != nil {
		a.hotkey.Unregister()
		a.hotkey = nil
	}
	if a.proxySvc != nil {
		_ = a.proxySvc.Stop()
	}
}

// quit asks Wails to close the application.
func (a *App) quit() {
	a.mu.Lock()
	a.quitting = true
	a.mu.Unlock()
	a.stopTray()
	if a.ctx != nil {
		wruntime.Quit(a.ctx)
	}
}

func (a *App) emit(event string, data ...any) {
	if a.log != nil {
		if len(data) > 0 {
			a.logEvent(event, data[0])
		} else {
			a.log.Printf("event %s", event)
		}
	}
	if a.ctx == nil {
		return
	}
	wruntime.EventsEmit(a.ctx, event, data...)
}

// ---- app info & settings -------------------------------------------------

// AppInfo describes the runtime environment for the UI.
type AppInfo struct {
	Version   string `json:"version"`
	DataDir   string `json:"dataDir"`
	Platform  string `json:"platform"`
	GoVersion string `json:"goVersion"`
	NotesPath string `json:"notesPath"`
}

// AppInfo returns environment details.
func (a *App) AppInfo() AppInfo {
	return AppInfo{
		Version:   version.Version,
		DataDir:   a.rootDir,
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
		GoVersion: runtime.Version(),
		NotesPath: a.notes.Path(),
	}
}

// GetSettings returns the current configuration.
func (a *App) GetSettings() settings.Settings { return a.settings.Get() }

// SaveSettings replaces the configuration.
func (a *App) SaveSettings(next settings.Settings) (settings.Settings, error) {
	return a.settings.Save(next)
}

// ProxyScopes lists the scopes shown on the proxy page.
func (a *App) ProxyScopes() []string { return settings.Scopes }

// GetProxy returns the effective rule for a scope plus whether it is an override.
func (a *App) GetProxy(scope string) map[string]any {
	cfg := a.settings.Get()
	rule := cfg.Proxy.Resolve(scope)
	_, override := cfg.Proxy.Scopes[scope]
	return map[string]any{
		"rule":     rule,
		"override": override,
		"url":      proxyx.ProxyURLString(rule),
	}
}

// SetProxy configures one scope (mode "off" disables the proxy there).
func (a *App) SetProxy(scope string, rule settings.ProxyRule) (settings.Settings, error) {
	if rule.Mode == "inherit" {
		return a.settings.ClearScopeProxy(scope)
	}
	return a.settings.SetScopeProxy(scope, rule)
}

// SetProxyFromURL accepts a proxy URL string ("socks5://user:pass@host:port").
func (a *App) SetProxyFromURL(scope, rawURL string) (settings.Settings, error) {
	return a.settings.SetScopeProxy(scope, proxyx.ParseProxyURL(rawURL))
}

// SetAllProxies applies one rule to every scope.
func (a *App) SetAllProxies(rule settings.ProxyRule) (settings.Settings, error) {
	return a.settings.Update(func(s *settings.Settings) error {
		s.Proxy.Default = rule
		s.Proxy.Scopes = map[string]settings.ProxyRule{}
		for _, sc := range settings.Scopes {
			s.Proxy.Scopes[sc] = rule.Clone()
		}
		return nil
	})
}

// DisableAllProxies forces direct connections everywhere.
func (a *App) DisableAllProxies() (settings.Settings, error) {
	off := settings.ProxyRule{Mode: "off"}
	return a.settings.Update(func(s *settings.Settings) error {
		s.Proxy.Default = off
		s.Proxy.Scopes = map[string]settings.ProxyRule{}
		for _, sc := range settings.Scopes {
			s.Proxy.Scopes[sc] = off.Clone()
		}
		return nil
	})
}

// TestProxy verifies a scope's proxy by fetching probeURL.
func (a *App) TestProxy(scope, probeURL string) proxyx.TestResult {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	rule := a.settings.ProxyFor(scope)
	if probeURL == "" {
		probeURL = "http://www.gstatic.com/generate_204"
	}
	return proxyx.TestProxy(ctx, rule, probeURL)
}

// TestProxyRule verifies an unsaved rule.
func (a *App) TestProxyRule(rule settings.ProxyRule, probeURL string) proxyx.TestResult {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	return proxyx.TestProxy(ctx, rule, probeURL)
}

// OpenDataDir reveals the application data directory.
func (a *App) OpenDataDir() error { return reveal(a.rootDir) }

// ---- small helpers -------------------------------------------------------

func reveal(path string) error {
	if _, err := os.Stat(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	switch runtime.GOOS {
	case "windows":
		return exec.Command("explorer", path).Start()
	case "darwin":
		return exec.Command("open", path).Start()
	default:
		return exec.Command("xdg-open", path).Start()
	}
}

// RevealInExplorer opens the containing folder and selects the file.
func (a *App) RevealInExplorer(path string) error {
	if path == "" {
		return errors.New("path is required")
	}
	if runtime.GOOS == "windows" {
		fi, err := os.Stat(path)
		if err == nil && fi.IsDir() {
			return exec.Command("explorer", path).Start()
		}
		return exec.Command("explorer", "/select,"+path).Start()
	}
	return reveal(filepath.Dir(path))
}

// OpenPath opens a file with the OS default handler.
func (a *App) OpenPath(path string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("cmd", "/c", "start", "", path).Start()
	case "darwin":
		return exec.Command("open", path).Start()
	default:
		return exec.Command("xdg-open", path).Start()
	}
}

// PickDirectory opens a native folder chooser.
func (a *App) PickDirectory(title string) (string, error) {
	if a.ctx == nil {
		return "", errors.New("app not ready")
	}
	if title == "" {
		title = "选择目录"
	}
	return wruntime.OpenDirectoryDialog(a.ctx, wruntime.OpenDialogOptions{Title: title})
}

// PickFile opens a native file chooser.
func (a *App) PickFile(title string) (string, error) {
	if a.ctx == nil {
		return "", errors.New("app not ready")
	}
	if title == "" {
		title = "选择文件"
	}
	return wruntime.OpenFileDialog(a.ctx, wruntime.OpenDialogOptions{Title: title})
}

// ReadTextFile returns file contents (bounded) for previews.
func (a *App) ReadTextFile(path string, maxBytes int64) (string, error) {
	if maxBytes <= 0 {
		maxBytes = 512 << 10
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	buf := make([]byte, maxBytes)
	n, _ := f.Read(buf)
	return string(buf[:n]), nil
}

// FileEntry is a local filesystem entry.
type FileEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	ModTime string `json:"modTime"`
}

// ListLocal lists a local directory (empty path = home).
func (a *App) ListLocal(dir string) (map[string]any, error) {
	if strings.TrimSpace(dir) == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		dir = home
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]FileEntry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, FileEntry{
			Name:    e.Name(),
			Path:    filepath.Join(dir, e.Name()),
			IsDir:   e.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime().Format(time.RFC3339),
		})
	}
	parent := filepath.Dir(dir)
	if parent == dir {
		parent = ""
	}
	return map[string]any{
		"path":      dir,
		"parent":    parent,
		"entries":   out,
		"home":      mustHome(),
		"separator": string(os.PathSeparator),
	}, nil
}

func mustHome() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}
