package app

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"os"
	"path/filepath"

	"wailstools/internal/autostart"
	"wailstools/internal/settings"
	"wailstools/internal/shot"
)

// ---- screenshots ---------------------------------------------------------

// CaptureScreenshot grabs a display (-1 = all) and saves it.
func (a *App) CaptureScreenshot(display int) (shot.Item, error) {
	return a.shots.Capture(display, "screen")
}

// ScreenshotDisplays reports how many monitors are attached.
func (a *App) ScreenshotDisplays() int { return shot.Displays() }

// ListScreenshots returns the newest saved screenshots.
func (a *App) ListScreenshots(limit int) ([]shot.Item, error) {
	items, err := a.shots.List(limit)
	if items == nil {
		items = []shot.Item{}
	}
	return items, err
}

// DeleteScreenshot removes a saved file.
func (a *App) DeleteScreenshot(path string) error { return a.shots.Delete(path) }

// GetScreenshotSettings returns the capture configuration.
func (a *App) GetScreenshotSettings() settings.ScreenshotSettings {
	return a.settings.Get().Screenshots
}

// SaveScreenshotSettings stores the capture configuration and applies the
// clipboard watcher state.
func (a *App) SaveScreenshotSettings(next settings.ScreenshotSettings) (settings.ScreenshotSettings, error) {
	cfg, err := a.settings.Update(func(s *settings.Settings) error {
		s.Screenshots = next
		return nil
	})
	if err != nil {
		return settings.ScreenshotSettings{}, err
	}
	if next.WatchClipboard {
		if err := a.shots.StartClipboardWatch(); err != nil {
			a.emit("screenshot:watch", map[string]any{"watching": false, "error": err.Error()})
		} else {
			a.emit("screenshot:watch", map[string]any{"watching": true})
		}
	} else {
		a.shots.StopClipboardWatch()
		a.emit("screenshot:watch", map[string]any{"watching": false})
	}
	return cfg.Screenshots, nil
}

// SetClipboardWatch enables or disables automatic saving of copied images.
func (a *App) SetClipboardWatch(enabled bool) (bool, error) {
	cfg := a.settings.Get().Screenshots
	cfg.WatchClipboard = enabled
	if _, err := a.SaveScreenshotSettings(cfg); err != nil {
		return false, err
	}
	return a.shots.Watching(), nil
}

// ClipboardWatching reports whether the watcher is active.
func (a *App) ClipboardWatching() bool { return a.shots.Watching() }

// OpenScreenshotDirectory reveals the screenshot folder.
func (a *App) OpenScreenshotDirectory() error {
	dir := a.settings.Get().Screenshots.Directory
	if dir == "" {
		return os.ErrNotExist
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return reveal(dir)
}

// PickScreenshotDirectory lets the user choose the save folder.
func (a *App) PickScreenshotDirectory() (string, error) {
	dir, err := a.PickDirectory("选择截图保存目录")
	if err != nil || dir == "" {
		return "", err
	}
	cfg := a.settings.Get().Screenshots
	cfg.Directory = dir
	if _, err := a.SaveScreenshotSettings(cfg); err != nil {
		return "", err
	}
	return dir, nil
}

// ScreenshotDefaultDir returns the configured directory.
func (a *App) ScreenshotDefaultDir() string {
	dir := a.settings.Get().Screenshots.Directory
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "wails-tools-screenshots")
	}
	return dir
}

// ScreenshotThumbnail renders a small PNG data URL for the gallery.
func (a *App) ScreenshotThumbnail(path string, maxSize int) (string, error) {
	if maxSize <= 0 {
		maxSize = 320
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return "", err
	}
	scaled := scaleNearest(img, maxSize)
	var buf bytes.Buffer
	if err := png.Encode(&buf, scaled); err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// scaleNearest downscales an image so its longest side fits maxSize.
func scaleNearest(img image.Image, maxSize int) *image.RGBA {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return image.NewRGBA(image.Rect(0, 0, 1, 1))
	}
	scale := float64(maxSize) / float64(max(b.Dx(), b.Dy()))
	if scale >= 1 {
		scale = 1
	}
	nw := int(float64(w) * scale)
	nh := int(float64(h) * scale)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	out := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		sy := b.Min.Y + y*h/nh
		for x := 0; x < nw; x++ {
			sx := b.Min.X + x*w/nw
			out.Set(x, y, img.At(sx, sy))
		}
	}
	return out
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ---- autostart -----------------------------------------------------------

// GetAutostartStatus reports the login-start registration.
func (a *App) GetAutostartStatus() autostart.Status {
	st := autostart.Info()
	// Reflect the settings flag as the user intent.
	if cfg := a.settings.Get().Startup; cfg.Enabled != st.Enabled {
		st.Enabled = cfg.Enabled
	}
	return st
}

// SetAutostart enables or disables launching at login.
func (a *App) SetAutostart(enabled bool) (autostart.Status, error) {
	var (
		st  autostart.Status
		err error
	)
	if enabled {
		st, err = autostart.Enable(autostart.Options{})
	} else {
		st, err = autostart.Disable()
	}
	if err != nil {
		return st, err
	}
	if _, serr := a.settings.Update(func(s *settings.Settings) error {
		s.Startup.Enabled = enabled
		return nil
	}); serr != nil {
		return st, serr
	}
	st.Enabled = enabled
	return st, nil
}
