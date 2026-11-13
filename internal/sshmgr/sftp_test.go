package sshmgr

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wailstools/internal/settings"
)

func TestSFTPBrowseUploadDownload(t *testing.T) {
	addr, _ := startSSHServer(t, "sftp-pass")
	host, port := splitAddr(t, addr)
	store := newStore(t)
	saved, err := store.SaveHost(Host{
		Name: "sftp", Address: host, Port: port, User: "tester",
		AuthMode: AuthPassword, Password: "sftp-pass", Route: RouteDirect,
	})
	if err != nil {
		t.Fatalf("save host: %v", err)
	}
	mgr := NewManager(store, func(string) settings.ProxyRule { return settings.ProxyRule{Mode: "off"} }, nil, store.dir)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := mgr.Connect(ctx, saved.ID, DialOptions{})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = mgr.Disconnect(client.ID) }()

	work := t.TempDir()
	remoteDir := filepath.ToSlash(work)
	// The SFTP server serves the real filesystem, so use a temp directory.
	if err := mgr.SFTPMkdir(client.ID, remoteDir); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	local := filepath.Join(t.TempDir(), "upload.txt")
	payload := strings.Repeat("sftp-round-trip\n", 200)
	if err := os.WriteFile(local, []byte(payload), 0o644); err != nil {
		t.Fatalf("write local: %v", err)
	}
	remoteFile := filepath.ToSlash(filepath.Join(work, "upload.txt"))
	if _, err := mgr.SFTPUpload(context.Background(), client.ID, local, remoteFile, nil); err != nil {
		t.Fatalf("upload: %v", err)
	}

	entries, err := mgr.SFTPList(client.ID, remoteDir)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Name == "upload.txt" && !e.IsDir && e.Size == int64(len(payload)) {
			found = true
		}
	}
	if !found {
		t.Fatalf("uploaded file not listed: %+v", entries)
	}

	preview, err := mgr.SFTPReadFile(client.ID, remoteFile, 64)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if !strings.HasPrefix(preview, "sftp-round-trip") {
		t.Fatalf("unexpected preview %q", preview)
	}

	back := filepath.Join(t.TempDir(), "download.txt")
	if _, err := mgr.SFTPDownload(context.Background(), client.ID, remoteFile, back, nil); err != nil {
		t.Fatalf("download: %v", err)
	}
	got, err := os.ReadFile(back)
	if err != nil {
		t.Fatalf("read downloaded: %v", err)
	}
	if string(got) != payload {
		t.Fatalf("download mismatch: %d vs %d bytes", len(got), len(payload))
	}

	renamed := remoteFile + ".bak"
	if err := mgr.SFTPRename(client.ID, remoteFile, renamed); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if err := mgr.SFTPRemove(client.ID, renamed, false); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := mgr.SFTPStat(client.ID, renamed); err == nil {
		t.Fatal("expected stat to fail after removal")
	}
}

func TestInteractiveTerminal(t *testing.T) {
	addr, _ := startSSHServer(t, "term-pass")
	host, port := splitAddr(t, addr)
	store := newStore(t)
	saved, err := store.SaveHost(Host{
		Name: "term", Address: host, Port: port, User: "tester",
		AuthMode: AuthPassword, Password: "term-pass", Route: RouteDirect,
	})
	if err != nil {
		t.Fatalf("save host: %v", err)
	}
	mgr := NewManager(store, func(string) settings.ProxyRule { return settings.ProxyRule{Mode: "off"} }, nil, store.dir)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client, err := mgr.Connect(ctx, saved.ID, DialOptions{})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = mgr.Disconnect(client.ID) }()

	dataCh := make(chan string, 16)
	closed := make(chan struct{}, 1)
	term, err := mgr.OpenTerminal(client.ID, TerminalOptions{
		Cols: 100, Rows: 30,
		OnData:  func(b []byte) { dataCh <- string(b) },
		OnClose: func(error) { closed <- struct{}{} },
	})
	if err != nil {
		t.Fatalf("open terminal: %v", err)
	}
	defer func() { _ = term.Close() }()

	if err := term.Write([]byte("hello-terminal\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	deadline := time.After(8 * time.Second)
	var buf strings.Builder
	for !strings.Contains(buf.String(), "got:hello-terminal") {
		select {
		case chunk := <-dataCh:
			buf.WriteString(chunk)
		case <-deadline:
			t.Fatalf("terminal did not echo input, got %q", buf.String())
		}
	}
	if err := term.Resize(120, 40); err != nil {
		t.Fatalf("resize: %v", err)
	}
}
