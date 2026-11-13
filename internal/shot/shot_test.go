package shot

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wailstools/internal/settings"
)

func newManager(t *testing.T) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := settings.NewStore(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	shotDir := filepath.Join(dir, "shots")
	if _, err := store.Update(func(s *settings.Settings) error {
		s.Screenshots.Directory = shotDir
		s.Screenshots.Pattern = "shot-{date}-{time}-{source}"
		s.Screenshots.Format = "png"
		s.Screenshots.AutoSave = true
		return nil
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	return NewManager(store, t.Logf), shotDir
}

func sampleImage() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 8, 6))
	for y := 0; y < 6; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 20), G: uint8(y * 30), B: 90, A: 255})
		}
	}
	return img
}

func TestSaveListDelete(t *testing.T) {
	m, dir := newManager(t)
	item, err := m.SaveImage(sampleImage(), "screen")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if !strings.HasPrefix(filepath.Base(item.Path), "shot-") {
		t.Fatalf("pattern not applied: %q", item.Path)
	}
	if !strings.HasSuffix(item.Path, ".png") {
		t.Fatalf("unexpected extension: %q", item.Path)
	}
	if item.Width != 8 || item.Height != 6 {
		t.Fatalf("unexpected dimensions: %+v", item)
	}
	if info, err := os.Stat(item.Path); err != nil || info.Size() == 0 {
		t.Fatalf("file missing or empty: %v", err)
	}

	// A second capture must not overwrite the first.
	second, err := m.SaveImage(sampleImage(), "screen")
	if err != nil {
		t.Fatalf("second save: %v", err)
	}
	if second.Path == item.Path {
		t.Fatal("second screenshot overwrote the first")
	}

	items, err := m.List(10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 screenshots, got %d", len(items))
	}

	if err := m.Delete(item.Path); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(item.Path); !os.IsNotExist(err) {
		t.Fatal("file still present after delete")
	}
	// Deleting outside the screenshot directory must be refused.
	outside := filepath.Join(filepath.Dir(dir), "outside.png")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	if err := m.Delete(outside); err == nil {
		t.Fatal("expected deletion outside the directory to be refused")
	}
}

func TestJPEGFormatAndUniqueNames(t *testing.T) {
	m, _ := newManager(t)
	store := m.store
	if _, err := store.Update(func(s *settings.Settings) error {
		s.Screenshots.Format = "jpg"
		s.Screenshots.Pattern = "fixed"
		return nil
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	first, err := m.SaveImage(sampleImage(), "clipboard")
	if err != nil {
		t.Fatalf("save jpg: %v", err)
	}
	if !strings.HasSuffix(first.Path, ".jpg") {
		t.Fatalf("expected jpg, got %q", first.Path)
	}
	second, err := m.SaveImage(sampleImage(), "clipboard")
	if err != nil {
		t.Fatalf("second jpg: %v", err)
	}
	if first.Path == second.Path {
		t.Fatal("unique naming failed")
	}
}

func TestBuildNameSanitises(t *testing.T) {
	name := buildName(`bad<>:"/\|?*name`, "screen")
	for _, ch := range []string{"<", ">", ":", `"`, "/", `\`, "|", "?", "*"} {
		if strings.Contains(name, ch) {
			t.Fatalf("illegal character %q kept in %q", ch, name)
		}
	}
	if buildName("", "screen") == "" {
		t.Fatal("default pattern must produce a name")
	}
}

func TestClipboardSupportMatchesPlatform(t *testing.T) {
	if got := clipboardSupported(); got != (os.PathSeparator == '\\') {
		t.Fatalf("clipboardSupported=%v on this platform", got)
	}
}
