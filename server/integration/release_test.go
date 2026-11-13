package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wailstools/internal/wire"
)

// TestReleaseDistribution exercises the "server stores client builds, clients
// auto-update" flow end to end.
func TestReleaseDistribution(t *testing.T) {
	srv := startServer(t, true, true)
	_, tcpAddr := srv.Addrs()
	_, portStr, _ := net.SplitHostPort(tcpAddr)
	admin, _ := newClient(t, atoi(portStr))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := admin.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := admin.Register(ctx, "release-admin", "super-secret-9", "", ""); err != nil {
		t.Fatalf("register admin: %v", err)
	}

	// Build a fake client binary and upload it.
	payload := bytes.Repeat([]byte("wails-tools-binary"), 4096)
	sum := sha256.Sum256(payload)
	binPath := filepath.Join(t.TempDir(), "WailsTools.exe")
	if err := os.WriteFile(binPath, payload, 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	uploaded, err := admin.UploadRelease(ctx, binPath, map[string]any{
		"platform": "windows",
		"arch":     "amd64",
		"channel":  "stable",
		"version":  "9.9.9",
		"filename": "WailsTools.exe",
		"notes":    "integration test build",
	}, nil)
	if err != nil {
		t.Fatalf("upload release: %v", err)
	}
	relID, _ := uploaded["id"].(string)
	if relID == "" {
		t.Fatalf("missing release id: %+v", uploaded)
	}

	// Latest must be reported for the platform.
	var latest struct {
		Found   bool `json:"found"`
		Release struct {
			ID       string `json:"id"`
			Version  string `json:"version"`
			Size     int64  `json:"size"`
			SHA256   string `json:"sha256"`
			Filename string `json:"filename"`
		} `json:"release"`
	}
	if err := admin.Call(ctx, "update.latest", map[string]any{
		"platform": "windows", "arch": "amd64", "channel": "stable",
	}, &latest); err != nil {
		t.Fatalf("update.latest: %v", err)
	}
	if !latest.Found || latest.Release.Version != "9.9.9" || latest.Release.Size != int64(len(payload)) {
		t.Fatalf("unexpected latest: %+v", latest)
	}
	if latest.Release.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("server did not record the checksum: %s", latest.Release.SHA256)
	}

	// Download the release and compare bytes.
	conn, closeFn, err := admin.OpenStream(ctx, "update.get", map[string]any{"id": relID})
	if err != nil {
		t.Fatalf("open release stream: %v", err)
	}
	var hdr struct {
		OK     bool   `json:"ok"`
		Size   int64  `json:"size"`
		SHA256 string `json:"sha256"`
		Error  string `json:"error"`
	}
	if err := wire.ReadFrame(conn, &hdr); err != nil {
		closeFn()
		t.Fatalf("release header: %v", err)
	}
	if !hdr.OK || hdr.Size != int64(len(payload)) {
		closeFn()
		t.Fatalf("unexpected header: %+v", hdr)
	}
	got, err := io.ReadAll(io.LimitReader(conn, hdr.Size))
	closeFn()
	if err != nil {
		t.Fatalf("read release: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("release bytes mismatch: %d vs %d", len(got), len(payload))
	}

	// A non-admin account must not be able to publish builds.
	plain, _ := newClient(t, atoi(portStr))
	if _, err := plain.Connect(ctx); err != nil {
		t.Fatalf("plain connect: %v", err)
	}
	if _, err := plain.Register(ctx, "release-plain", "super-secret-10", "", ""); err != nil {
		t.Fatalf("register plain: %v", err)
	}
	if _, err := plain.UploadRelease(ctx, binPath, map[string]any{
		"platform": "windows", "arch": "amd64", "channel": "stable",
		"version": "9.9.9", "filename": "WailsTools.exe",
	}, nil); err == nil {
		t.Fatal("non-admin release upload must be rejected")
	}

	// Deleting removes it from the latest query.
	if err := admin.Call(ctx, "update.delete", map[string]any{"id": relID}, nil); err != nil {
		t.Fatalf("delete release: %v", err)
	}
	var after struct {
		Found bool `json:"found"`
	}
	if err := admin.Call(ctx, "update.latest", map[string]any{
		"platform": "windows", "arch": "amd64", "channel": "stable",
	}, &after); err != nil {
		t.Fatalf("latest after delete: %v", err)
	}
	if after.Found {
		t.Fatal("release still reported after deletion")
	}
}

// TestReleaseListFilters verifies platform/channel filtering.
func TestReleaseListFilters(t *testing.T) {
	srv := startServer(t, true, true)
	_, tcpAddr := srv.Addrs()
	_, portStr, _ := net.SplitHostPort(tcpAddr)
	admin, _ := newClient(t, atoi(portStr))
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if _, err := admin.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := admin.Register(ctx, "list-admin", "super-secret-11", "", ""); err != nil {
		t.Fatalf("register: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "build.bin")
	if err := os.WriteFile(bin, []byte(strings.Repeat("x", 512)), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, spec := range []map[string]any{
		{"platform": "windows", "arch": "amd64", "channel": "stable", "version": "1.0.1", "filename": "a.exe"},
		{"platform": "windows", "arch": "amd64", "channel": "beta", "version": "1.1.0-beta", "filename": "b.exe"},
		{"platform": "linux", "arch": "amd64", "channel": "stable", "version": "1.0.1", "filename": "c.bin"},
	} {
		if _, err := admin.UploadRelease(ctx, bin, spec, nil); err != nil {
			t.Fatalf("upload %v: %v", spec["version"], err)
		}
	}
	var list struct {
		Releases []struct {
			Platform string `json:"platform"`
			Channel  string `json:"channel"`
			Version  string `json:"version"`
		} `json:"releases"`
	}
	if err := admin.Call(ctx, "update.list", map[string]any{"platform": "windows", "channel": "stable"}, &list); err != nil {
		t.Fatalf("update.list: %v", err)
	}
	if len(list.Releases) != 1 || list.Releases[0].Version != "1.0.1" {
		t.Fatalf("filter returned %+v", list.Releases)
	}
	var all struct {
		Releases []struct {
			Version string `json:"version"`
		} `json:"releases"`
	}
	if err := admin.Call(ctx, "update.list", map[string]any{}, &all); err != nil {
		t.Fatalf("update.list all: %v", err)
	}
	if len(all.Releases) != 3 {
		t.Fatalf("expected 3 releases, got %d", len(all.Releases))
	}
}
