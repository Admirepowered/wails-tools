// Package shot captures screenshots and auto-saves them (including images
// copied to the clipboard) into a configurable directory.
package shot

import (
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"wailstools/internal/settings"
)

// Item describes a saved screenshot.
type Item struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
	CreatedAt string `json:"createdAt"`
	Source    string `json:"source"` // screen | clipboard
}

// Manager captures and stores screenshots.
type Manager struct {
	store *settings.Store
	logf  func(string, ...any)

	mu      sync.Mutex
	watcher *clipboardWatcher
	onSaved func(Item)
}

// NewManager builds a screenshot manager.
func NewManager(store *settings.Store, logf func(string, ...any)) *Manager {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Manager{store: store, logf: logf}
}

// SetSavedHandler registers a callback fired after each auto-saved capture.
func (m *Manager) SetSavedHandler(fn func(Item)) {
	m.mu.Lock()
	m.onSaved = fn
	m.mu.Unlock()
}

func (m *Manager) emit(item Item) {
	m.mu.Lock()
	cb := m.onSaved
	m.mu.Unlock()
	if cb != nil {
		cb(item)
	}
}

// Displays reports the number of monitors.
func Displays() int {
	if n := displayCount(); n > 0 {
		return n
	}
	return 1
}

// Capture grabs a display (-1 = all displays combined) and saves it.
func (m *Manager) Capture(display int, source string) (Item, error) {
	cfg := m.store.Get().Screenshots
	img, err := captureDisplay(display)
	if err != nil {
		return Item{}, err
	}
	return m.save(img, cfg, source)
}

// SaveImage writes an arbitrary image using the configured format/pattern.
func (m *Manager) SaveImage(img image.Image, source string) (Item, error) {
	cfg := m.store.Get().Screenshots
	rgba, ok := img.(*image.RGBA)
	if !ok {
		rgba = toRGBA(img)
	}
	return m.save(rgba, cfg, source)
}

func toRGBA(img image.Image) *image.RGBA {
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			out.Set(x, y, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return out
}

func (m *Manager) save(img *image.RGBA, cfg settings.ScreenshotSettings, source string) (Item, error) {
	dir := cfg.Directory
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "wails-tools-screenshots")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Item{}, err
	}
	name := buildName(cfg.Pattern, source)
	ext := ".png"
	if strings.EqualFold(cfg.Format, "jpg") || strings.EqualFold(cfg.Format, "jpeg") {
		ext = ".jpg"
	}
	path := filepath.Join(dir, name+ext)
	path = uniquePath(path)

	f, err := os.Create(path)
	if err != nil {
		return Item{}, err
	}
	if ext == ".jpg" {
		err = jpeg.Encode(f, img, &jpeg.Options{Quality: 92})
	} else {
		err = png.Encode(f, img)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(path)
		return Item{}, err
	}
	info, _ := os.Stat(path)
	item := Item{
		Path:      path,
		Name:      filepath.Base(path),
		Size:      sizeOf(info),
		Width:     img.Bounds().Dx(),
		Height:    img.Bounds().Dy(),
		CreatedAt: time.Now().Format(time.RFC3339),
		Source:    source,
	}
	m.logf("screenshot saved: %s (%dx%d)", path, item.Width, item.Height)
	if cfg.AutoSave {
		m.emit(item)
	}
	return item, nil
}

func sizeOf(info os.FileInfo) int64 {
	if info == nil {
		return 0
	}
	return info.Size()
}

func buildName(pattern, source string) string {
	if strings.TrimSpace(pattern) == "" {
		pattern = "shot-{date}-{time}"
	}
	now := time.Now()
	replacer := strings.NewReplacer(
		"{date}", now.Format("2006-01-02"),
		"{time}", now.Format("15-04-05"),
		"{datetime}", now.Format("20060102-150405"),
		"{unix}", strconv.FormatInt(now.Unix(), 10),
		"{source}", source,
	)
	name := replacer.Replace(pattern)
	name = sanitize(name)
	if name == "" {
		name = "shot-" + now.Format("20060102-150405")
	}
	return name
}

func sanitize(name string) string {
	bad := `<>:"/\|?*`
	out := strings.Map(func(r rune) rune {
		if strings.ContainsRune(bad, r) || r < 32 {
			return '_'
		}
		return r
	}, strings.TrimSpace(name))
	if len(out) > 120 {
		out = out[:120]
	}
	return strings.Trim(out, ". ")
}

func uniquePath(path string) string {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return path
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	for i := 1; i < 1000; i++ {
		cand := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if _, err := os.Stat(cand); errors.Is(err, os.ErrNotExist) {
			return cand
		}
	}
	return fmt.Sprintf("%s-%d%s", base, time.Now().UnixNano(), ext)
}

// List returns saved screenshots, newest first.
func (m *Manager) List(limit int) ([]Item, error) {
	cfg := m.store.Get().Screenshots
	dir := cfg.Directory
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	items := make([]Item, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".png" && ext != ".jpg" && ext != ".jpeg" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, Item{
			Path:      filepath.Join(dir, e.Name()),
			Name:      e.Name(),
			Size:      info.Size(),
			CreatedAt: info.ModTime().Format(time.RFC3339),
			Source:    "file",
		})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].CreatedAt > items[j].CreatedAt })
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

// Delete removes a saved screenshot.
func (m *Manager) Delete(path string) error {
	cfg := m.store.Get().Screenshots
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	dir, err := filepath.Abs(cfg.Directory)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(abs, dir) {
		return errors.New("refusing to delete outside the screenshot directory")
	}
	return os.Remove(abs)
}

// StartClipboardWatch begins auto-saving images copied to the clipboard.
func (m *Manager) StartClipboardWatch() error {
	if !clipboardSupported() {
		return errors.New("当前平台不支持剪贴板监听")
	}
	m.mu.Lock()
	if m.watcher != nil {
		m.mu.Unlock()
		return nil
	}
	w := newClipboardWatcher(func(img *image.RGBA) {
		cfg := m.store.Get().Screenshots
		if !cfg.WatchClipboard {
			return
		}
		if _, err := m.save(img, cfg, "clipboard"); err != nil {
			m.logf("clipboard save failed: %v", err)
		}
	})
	m.watcher = w
	m.mu.Unlock()
	return w.start()
}

// StopClipboardWatch stops the clipboard watcher.
func (m *Manager) StopClipboardWatch() {
	m.mu.Lock()
	w := m.watcher
	m.watcher = nil
	m.mu.Unlock()
	if w != nil {
		w.stop()
	}
}

// Watching reports whether the clipboard watcher is active.
func (m *Manager) Watching() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.watcher != nil
}
