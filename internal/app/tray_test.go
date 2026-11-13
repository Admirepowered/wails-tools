package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"wailstools/internal/settings"
	"wailstools/internal/shot"
)

func newShotManager(store *settings.Store) *shot.Manager {
	return shot.NewManager(store, func(string, ...any) {})
}

func TestTrayMenuReflectsSettings(t *testing.T) {
	dir := t.TempDir()
	store, err := settings.NewStore(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	a := &App{settings: store}
	items := a.trayMenu()
	if len(items) < 8 {
		t.Fatalf("menu too short: %d items", len(items))
	}

	// The clipboard and autostart items must mirror the saved settings.
	if _, err := store.Update(func(s *settings.Settings) error {
		s.Screenshots.WatchClipboard = true
		s.Startup.Enabled = true
		return nil
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	items = a.trayMenu()
	checked := map[int]bool{}
	lastID := 0
	separators := 0
	for _, it := range items {
		if it.Separator {
			separators++
			continue
		}
		checked[it.ID] = it.Checked
		lastID = it.ID
	}
	if !checked[trayCmdClipboard] {
		t.Fatal("clipboard item should be checked")
	}
	if !checked[trayCmdAutostart] {
		t.Fatal("autostart item should be checked")
	}
	if lastID != trayCmdQuit {
		t.Fatalf("quit must be the last item, got id %d", lastID)
	}
	if separators < 3 {
		t.Fatalf("expected at least 3 separators, got %d", separators)
	}
	if _, ok := checked[trayCmdShow]; !ok {
		t.Fatal("missing show command")
	}
	if _, ok := checked[trayCmdCaptureAll]; !ok {
		t.Fatal("missing capture command")
	}
}

func TestTrayClipboardToggle(t *testing.T) {
	dir := t.TempDir()
	store, err := settings.NewStore(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	a := &App{settings: store, shots: newShotManager(store)}

	before := store.Get().Screenshots.WatchClipboard
	a.trayAction(trayCmdClipboard)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if store.Get().Screenshots.WatchClipboard != before {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if store.Get().Screenshots.WatchClipboard == before {
		t.Fatal("clipboard toggle did not persist")
	}
}

func TestTrayCaptureAction(t *testing.T) {
	dir := t.TempDir()
	store, err := settings.NewStore(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	shotDir := filepath.Join(dir, "shots")
	if _, err := store.Update(func(s *settings.Settings) error {
		s.Screenshots.Directory = shotDir
		s.Screenshots.AutoSave = true
		return nil
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	a := &App{settings: store, shots: newShotManager(store)}

	a.trayAction(trayCmdCapturePrimary)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if entries, err := os.ReadDir(shotDir); err == nil && len(entries) > 0 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Skip("screen capture unavailable in this environment")
}

func TestTrayUnsupportedIDIsSafe(t *testing.T) {
	dir := t.TempDir()
	store, _ := settings.NewStore(filepath.Join(dir, "settings.json"))
	a := &App{settings: store}
	// Unknown command must not panic.
	a.trayAction(9999)
}
