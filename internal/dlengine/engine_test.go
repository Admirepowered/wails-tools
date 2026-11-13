package dlengine

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"wailstools/internal/settings"
)

// rangeServer serves a deterministic payload with Range support so the chunked
// download path can be exercised end to end.
func rangeServer(t *testing.T, payload []byte, failFirstN int32) (*httptest.Server, *int32) {
	t.Helper()
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&requests, 1)
		if failFirstN > 0 && n <= failFirstN {
			http.Error(w, "transient failure", http.StatusInternalServerError)
			return
		}
		total := int64(len(payload))
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("ETag", `"test-etag"`)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="payload.bin"`)
		rangeHeader := r.Header.Get("Range")
		if rangeHeader == "" {
			w.Header().Set("Content-Length", strconv.FormatInt(total, 10))
			_, _ = w.Write(payload)
			return
		}
		var start, end int64
		if _, err := fmt.Sscanf(rangeHeader, "bytes=%d-%d", &start, &end); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if end >= total {
			end = total - 1
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, total))
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[start : end+1])
	}))
	t.Cleanup(srv.Close)
	return srv, &requests
}

func newTestEngine(t *testing.T, dir string) *Engine {
	t.Helper()
	store, err := settings.NewStore(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	if _, err := store.Update(func(s *settings.Settings) error {
		s.Downloads.Directory = dir
		s.Downloads.ConnectionsPer = 4
		s.Downloads.ChunkSize = 64 << 10
		s.Downloads.RetryCount = 3
		return nil
	}); err != nil {
		t.Fatalf("configure downloads: %v", err)
	}
	eng, err := NewEngine(store, dir, nil)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	return eng
}

func waitFor(t *testing.T, eng *Engine, id string, timeout time.Duration) Task {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		task, ok := eng.Get(id)
		if ok {
			switch task.Status {
			case StatusCompleted, StatusFailed, StatusCanceled:
				return task
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	task, _ := eng.Get(id)
	t.Fatalf("task did not finish, status=%q error=%q", task.Status, task.Error)
	return task
}

func TestChunkedDownloadWithRanges(t *testing.T) {
	payload := bytes.Repeat([]byte("0123456789abcdef"), 64*1024) // 1 MiB
	srv, requests := rangeServer(t, payload, 0)
	dir := t.TempDir()
	eng := newTestEngine(t, dir)

	task, err := eng.Add(Request{URL: srv.URL + "/payload.bin", Connections: 4})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := eng.Start(task.ID); err != nil {
		t.Fatalf("start: %v", err)
	}
	done := waitFor(t, eng, task.ID, 30*time.Second)
	if done.Status != StatusCompleted {
		t.Fatalf("status=%s error=%s", done.Status, done.Error)
	}
	if done.Total != int64(len(payload)) || done.Downloaded != int64(len(payload)) {
		t.Fatalf("size mismatch: total=%d downloaded=%d want=%d", done.Total, done.Downloaded, len(payload))
	}
	out, err := os.ReadFile(filepath.Join(dir, done.Filename))
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	wantSum := sha256.Sum256(payload)
	gotSum := sha256.Sum256(out)
	if hex.EncodeToString(wantSum[:]) != hex.EncodeToString(gotSum[:]) {
		t.Fatalf("checksum mismatch: wrote %d bytes", len(out))
	}
	if atomic.LoadInt32(requests) < 2 {
		t.Fatalf("expected multiple ranged requests, got %d", atomic.LoadInt32(requests))
	}
	// Chunk part files must be cleaned up.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".part.") || strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("leftover partial file: %s", e.Name())
		}
	}
}

func TestDownloadRetriesTransientFailures(t *testing.T) {
	payload := bytes.Repeat([]byte("retry-me"), 4096)
	srv, _ := rangeServer(t, payload, 2)
	dir := t.TempDir()
	eng := newTestEngine(t, dir)
	task, err := eng.Add(Request{URL: srv.URL + "/retry.bin", Connections: 1})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := eng.Start(task.ID); err != nil {
		t.Fatalf("start: %v", err)
	}
	done := waitFor(t, eng, task.ID, 40*time.Second)
	if done.Status != StatusCompleted {
		t.Fatalf("expected completion after retries, got %s (%s)", done.Status, done.Error)
	}
	out, _ := os.ReadFile(filepath.Join(dir, done.Filename))
	if !bytes.Equal(out, payload) {
		t.Fatalf("payload mismatch after retries")
	}
	if done.Retries == 0 {
		t.Fatal("expected the retry counter to be incremented")
	}
}

func TestResumeAfterPause(t *testing.T) {
	payload := bytes.Repeat([]byte("resumable-data-"), 128*1024) // ~2 MiB
	srv, _ := rangeServer(t, payload, 0)
	dir := t.TempDir()
	eng := newTestEngine(t, dir)

	task, err := eng.Add(Request{URL: srv.URL + "/big.bin", Connections: 2, Filename: "big.bin"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := eng.Start(task.ID); err != nil {
		t.Fatalf("start: %v", err)
	}
	// Pause as soon as some progress has been reported.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		task, _ := eng.Get(task.ID)
		if task.Downloaded > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := eng.Pause(task.ID); err != nil {
		t.Fatalf("pause: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	paused, ok := eng.Get(task.ID)
	if !ok {
		t.Fatal("task disappeared")
	}
	if paused.Status == StatusCompleted {
		t.Skip("download finished before the pause took effect")
	}
	if paused.Status != StatusPaused {
		t.Fatalf("expected paused status, got %s", paused.Status)
	}
	partial := paused.Downloaded

	if err := eng.Start(task.ID); err != nil {
		t.Fatalf("resume: %v", err)
	}
	done := waitFor(t, eng, task.ID, 60*time.Second)
	if done.Status != StatusCompleted {
		t.Fatalf("resume failed: %s (%s)", done.Status, done.Error)
	}
	out, err := os.ReadFile(filepath.Join(dir, "big.bin"))
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !bytes.Equal(out, payload) {
		t.Fatalf("resumed file corrupt: got %d bytes, partial was %d", len(out), partial)
	}
}

func TestFilenameFromDisposition(t *testing.T) {
	payload := []byte("hello")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename*=UTF-8''%E6%B5%8B%E8%AF%95.bin`)
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write(payload)
	}))
	defer srv.Close()
	dir := t.TempDir()
	eng := newTestEngine(t, dir)
	task, err := eng.Add(Request{URL: srv.URL + "/x"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	eng.Start(task.ID)
	done := waitFor(t, eng, task.ID, 20*time.Second)
	if done.Status != StatusCompleted {
		t.Fatalf("status %s: %s", done.Status, done.Error)
	}
	if done.Filename != "测试.bin" {
		t.Fatalf("unexpected filename %q", done.Filename)
	}
}

func TestProbeReportsMetadata(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 4096)
	srv, _ := rangeServer(t, payload, 0)
	eng := newTestEngine(t, t.TempDir())
	res := eng.Probe(t.Context(), srv.URL+"/probe.bin")
	if ok, _ := res["ok"].(bool); !ok {
		t.Fatalf("probe failed: %+v", res)
	}
	if total, _ := res["total"].(int64); total != int64(len(payload)) {
		t.Fatalf("probe total %v, want %d", res["total"], len(payload))
	}
	if name, _ := res["filename"].(string); name != "payload.bin" {
		t.Fatalf("probe filename %q", name)
	}
}
