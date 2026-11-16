package dlengine

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"wailstools/internal/settings"
)

// TestHeadersReachTheOrigin proves the per-task User-Agent/Cookie/Referer are
// actually sent by the probe and the chunk requests.
func TestHeadersReachTheOrigin(t *testing.T) {
	var mu sync.Mutex
	seen := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, fmt.Sprintf("%s ua=%q cookie=%q ref=%q range=%q",
			r.Method, r.Header.Get("User-Agent"), r.Header.Get("Cookie"),
			r.Header.Get("Referer"), r.Header.Get("Range")))
		mu.Unlock()
		body := strings.Repeat("x", 8192)
		w.Header().Set("Accept-Ranges", "bytes")
		if rng := r.Header.Get("Range"); rng != "" {
			w.Header().Set("Content-Range", "bytes 0-8191/8192")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte(body))
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	dir := t.TempDir()
	store, err := settings.NewStore(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	store.Update(func(s *settings.Settings) error {
		s.Downloads.Directory = filepath.Join(dir, "dl")
		return nil
	})
	engine, err := NewEngine(store, filepath.Join(dir, "dl"), nil)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	task, err := engine.Add(Request{
		URL:         srv.URL + "/f.bin",
		Filename:    "f.bin",
		Headers:     map[string]string{"User-Agent": "netdisk;7.44.7.1;PC;PC-Windows;10.0.19045;WindowsBaiduYunGuanJia", "Cookie": "BDUSS=abc"},
		Referer:     "https://pan.baidu.com/",
		Connections: 2,
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := engine.Start(task.ID); err != nil {
		t.Fatalf("start: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(seen)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got, ok := engine.Get(task.ID); ok && got.Status == StatusFailed {
		t.Fatalf("task failed: %s", got.Error)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("origin never received a request")
	}
	for _, line := range seen {
		t.Log(line)
		if !strings.Contains(line, "netdisk;7.44.7.1") {
			t.Fatalf("User-Agent missing: %s", line)
		}
		if !strings.Contains(line, `cookie="BDUSS=abc"`) {
			t.Fatalf("Cookie missing: %s", line)
		}
	}
}
