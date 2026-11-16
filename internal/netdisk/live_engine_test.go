package netdisk

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"wailstools/internal/dlengine"
	"wailstools/internal/settings"
)

// TestLiveBaiduEnginePath runs the real download engine against the signed CDN
// URL with the exact headers the app uses, and reports the outcome.
func TestLiveBaiduEnginePath(t *testing.T) {
	cookie := os.Getenv("WT_BAIDU_COOKIE")
	fileID := os.Getenv("WT_BAIDU_FILE_ID")
	if cookie == "" || fileID == "" {
		t.Skip("env not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c := NewBaiduClient(nil)
	link, err := c.ResolveDownload(ctx, Account{ID: "live"}, cookie, ResolveDownloadRequest{FileID: fileID, Path: "/x.7z"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	dir := t.TempDir()
	store, _ := settings.NewStore(filepath.Join(dir, "settings.json"))
	store.Update(func(s *settings.Settings) error {
		s.Downloads.Directory = filepath.Join(dir, "dl")
		s.Downloads.MaxConcurrency = 2
		return nil
	})
	engine, err := dlengine.NewEngine(store, filepath.Join(dir, "dl"), nil)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	headers := map[string]string{
		"User-Agent": link.UserAgent,
		"Cookie":     link.Cookie,
		"Referer":    link.Referer,
	}
	task, err := engine.Add(dlengine.Request{
		URL: link.URL, Filename: "x.7z", Headers: headers, Referer: link.Referer,
		Connections: 8, ChunkSize: 4 << 20, ProxyMode: "off",
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := engine.Start(task.ID); err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(8 * time.Second)
	got, _ := engine.Get(task.ID)
	fmt.Printf("status=%s downloaded=%d total=%d error=%q retries=%d\n",
		got.Status, got.Downloaded, got.Total, got.Error, got.Retries)
	if err := engine.Cancel(task.ID); err != nil {
		t.Logf("cancel: %v", err)
	}
	if got.Status == dlengine.StatusFailed {
		t.Fatalf("engine download failed: %s", got.Error)
	}
}

