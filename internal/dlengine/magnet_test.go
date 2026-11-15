package dlengine

import (
	"path/filepath"
	"testing"

	"wailstools/internal/settings"
)

func TestAddMagnetTask(t *testing.T) {
	store, err := settings.NewStore(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	dir := t.TempDir()
	if _, err := store.Update(func(s *settings.Settings) error {
		s.Downloads.Directory = dir
		return nil
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	engine, err := NewEngine(store, dir, nil)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}

	magnet := "magnet:?xt=urn:btih:08ada5a7a6183aae1e09d831df6748d566095a10&dn=Sintel"
	task, err := engine.Add(Request{URL: magnet})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if task.Kind != KindMagnet {
		t.Fatalf("kind = %q", task.Kind)
	}
	if task.Filename != "Sintel" {
		t.Fatalf("filename = %q", task.Filename)
	}

	// HTTP links still work and are classified as HTTP.
	httpTask, err := engine.Add(Request{URL: "https://example.com/file.zip"})
	if err != nil {
		t.Fatalf("add http: %v", err)
	}
	if httpTask.Kind != KindHTTP {
		t.Fatalf("http kind = %q", httpTask.Kind)
	}

	// Unsupported schemes are rejected.
	if _, err := engine.Add(Request{URL: "ftp://example.com/x"}); err == nil {
		t.Fatal("ftp should be rejected")
	}
}
