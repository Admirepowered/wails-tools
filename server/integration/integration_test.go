// Package integration exercises the full client <-> server stack: transport
// negotiation (UDP preferred, TCP fallback), registration, per-user note sync,
// cloud file transfer and the SSH relay tunnel.
package integration

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wailstools/internal/serverlink"
	"wailstools/internal/settings"
	"wailstools/server/internal/server"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

type harness struct {
	t      *testing.T
	srv    *server.Server
	port   int
	client *serverlink.Client
	store  *settings.Store
}

func startServer(t *testing.T, enableUDP, enableTCP bool) *server.Server {
	return startServerWithQuota(t, enableUDP, enableTCP, 64<<20)
}

func startServerWithQuota(t *testing.T, enableUDP, enableTCP bool, quota int64) *server.Server {
	t.Helper()
	dir := t.TempDir()
	port := freePort(t)
	srv, err := server.New(server.Config{
		DataDir:           filepath.Join(dir, "data"),
		TCPAddr:           net.JoinHostPort("127.0.0.1", itoa(port)),
		UDPAddr:           net.JoinHostPort("127.0.0.1", itoa(port)),
		EnableUDP:         enableUDP,
		EnableTCP:         enableTCP,
		AllowRegistration: true,
		RelayEnabled:      true,
		DefaultQuotaBytes: quota,
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		shutdownCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = srv.Shutdown(shutdownCtx)
	})
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("server.Start: %v", err)
	}
	return srv
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}

func newClient(t *testing.T, port int) (*serverlink.Client, *settings.Store) {
	t.Helper()
	dir := t.TempDir()
	store, err := settings.NewStore(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatalf("settings store: %v", err)
	}
	if _, err := store.Update(func(s *settings.Settings) error {
		s.Server.Host = "127.0.0.1"
		s.Server.Port = port
		s.Server.PreferUDP = true
		return nil
	}); err != nil {
		t.Fatalf("configure endpoint: %v", err)
	}
	c := serverlink.New(store, t.Logf)
	c.RestoreToken()
	t.Cleanup(func() { _ = c.Close() })
	return c, store
}

func TestUDPPreferredRegistrationAndNotes(t *testing.T) {
	srv := startServer(t, true, true)
	_, tcpAddr := srv.Addrs()
	_, portStr, err := net.SplitHostPort(tcpAddr)
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	port := atoi(portStr)

	client, _ := newClient(t, port)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	status, err := client.Connect(ctx)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if status.Transport != "udp" {
		t.Fatalf("expected udp transport, got %q", status.Transport)
	}

	if _, err := client.Register(ctx, "alice", "super-secret-1", "alice@example.com", "Alice"); err != nil {
		t.Fatalf("register: %v", err)
	}
	if client.Token() == "" {
		t.Fatal("expected a session token")
	}
	me, err := client.Me(ctx)
	if err != nil {
		t.Fatalf("me: %v", err)
	}
	if me.Username != "alice" {
		t.Fatalf("unexpected user: %+v", me)
	}

	now := time.Now().UnixMilli()
	changes := []serverlink.Note{{
		ID: "note-1", Title: "第一条笔记", Content: "# 你好\n内容", Folder: "工作",
		UpdatedAt: now, CreatedAt: now,
	}}
	notes, conflicts, serverTime, err := client.NotesSync(ctx, 0, changes)
	if err != nil {
		t.Fatalf("notes sync: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", conflicts)
	}
	if len(notes) != 1 || notes[0].Title != "第一条笔记" {
		t.Fatalf("unexpected notes: %+v", notes)
	}
	if serverTime <= 0 {
		t.Fatalf("expected server time, got %d", serverTime)
	}

	// A second sync from scratch must return the stored note.
	again, _, _, err := client.NotesSync(ctx, 0, nil)
	if err != nil {
		t.Fatalf("notes resync: %v", err)
	}
	if len(again) != 1 || again[0].Content != "# 你好\n内容" {
		t.Fatalf("note not persisted: %+v", again)
	}
}

func TestTCPFallbackWhenUDPDisabled(t *testing.T) {
	srv := startServer(t, false, true)
	_, tcpAddr := srv.Addrs()
	_, portStr, _ := net.SplitHostPort(tcpAddr)
	client, _ := newClient(t, atoi(portStr))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	status, err := client.Connect(ctx)
	if err != nil {
		t.Fatalf("connect over tcp only: %v", err)
	}
	if status.Transport != "tcp" {
		t.Fatalf("expected tcp transport, got %q", status.Transport)
	}
	if _, err := client.Register(ctx, "bob", "super-secret-2", "", "Bob"); err != nil {
		t.Fatalf("register over tcp: %v", err)
	}
	if _, err := client.ServerInfo(ctx); err != nil {
		t.Fatalf("server info over tcp: %v", err)
	}
}

func TestFileTransferAndIsolation(t *testing.T) {
	srv := startServer(t, true, true)
	_, tcpAddr := srv.Addrs()
	_, portStr, _ := net.SplitHostPort(tcpAddr)
	port := atoi(portStr)

	alice, _ := newClient(t, port)
	bob, _ := newClient(t, port)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if _, err := alice.Connect(ctx); err != nil {
		t.Fatalf("alice connect: %v", err)
	}
	if _, err := bob.Connect(ctx); err != nil {
		t.Fatalf("bob connect: %v", err)
	}
	if _, err := alice.Register(ctx, "carol", "super-secret-3", "", "Carol"); err != nil {
		t.Fatalf("alice register: %v", err)
	}
	if _, err := bob.Register(ctx, "dave", "super-secret-4", "", "Dave"); err != nil {
		t.Fatalf("bob register: %v", err)
	}

	// Upload a file as alice.
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "hello.txt")
	payload := strings.Repeat("wails-tools-cloud-file\n", 500)
	if err := os.WriteFile(src, []byte(payload), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	written, err := alice.Upload(ctx, src, "/docs/hello.txt", nil)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if written != int64(len(payload)) {
		t.Fatalf("upload size %d, want %d", written, len(payload))
	}

	entries, err := alice.ListRemote(ctx, "/docs")
	if err != nil {
		t.Fatalf("list remote: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Name == "hello.txt" && !e.IsDir && e.Size == int64(len(payload)) {
			found = true
		}
	}
	if !found {
		t.Fatalf("uploaded file missing from listing: %+v", entries)
	}

	// Download it back and compare bytes.
	dst := filepath.Join(t.TempDir(), "hello-copy.txt")
	got, err := alice.Download(ctx, "/docs/hello.txt", dst, nil)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read copy: %v", err)
	}
	if got != int64(len(payload)) || string(data) != payload {
		t.Fatalf("round trip mismatch: got %d bytes", len(data))
	}

	// Bob must not see alice's file.
	bobEntries, err := bob.ListRemote(ctx, "/docs")
	if err != nil {
		// A fresh user has no /docs directory at all; either empty or not found is fine.
		if !strings.Contains(err.Error(), "not_found") {
			t.Fatalf("bob list: %v", err)
		}
	} else if len(bobEntries) != 0 {
		t.Fatalf("per-user isolation broken, bob sees %+v", bobEntries)
	}

	used, quota, err := alice.Usage(ctx)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if used < int64(len(payload)) {
		t.Fatalf("usage %d smaller than payload %d", used, len(payload))
	}
	if quota <= 0 {
		t.Fatalf("expected a positive quota, got %d", quota)
	}

	// Path traversal must be rejected.
	if _, err := alice.Download(ctx, "/../../etc/passwd", filepath.Join(t.TempDir(), "x"), nil); err == nil {
		t.Fatal("expected traversal to be rejected")
	}
}

func TestRelayTunnelThroughServer(t *testing.T) {
	srv := startServer(t, true, true)
	_, tcpAddr := srv.Addrs()
	_, portStr, _ := net.SplitHostPort(tcpAddr)
	client, _ := newClient(t, atoi(portStr))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := client.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := client.Register(ctx, "relay-user", "super-secret-5", "", ""); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Local echo server acting as the "SSH host" behind the relay.
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()

	tunnel, err := client.DialTCP(ctx, echo.Addr().String())
	if err != nil {
		t.Fatalf("relay dial: %v", err)
	}
	defer tunnel.Close()
	msg := []byte("ssh-banner-through-relay\n")
	if _, err := tunnel.Write(msg); err != nil {
		t.Fatalf("relay write: %v", err)
	}
	buf := make([]byte, len(msg))
	_ = tunnel.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(tunnel, buf); err != nil {
		t.Fatalf("relay read: %v", err)
	}
	if string(buf) != string(msg) {
		t.Fatalf("relay echo mismatch: %q", buf)
	}
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return n
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func TestQuotaEnforced(t *testing.T) {
	srv := startServerWithQuota(t, true, true, 4096)
	_, tcpAddr := srv.Addrs()
	_, portStr, _ := net.SplitHostPort(tcpAddr)
	client, _ := newClient(t, atoi(portStr))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := client.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := client.Register(ctx, "quota-user", "super-secret-6", "", ""); err != nil {
		t.Fatalf("register: %v", err)
	}

	big := filepath.Join(t.TempDir(), "big.bin")
	if err := os.WriteFile(big, make([]byte, 8192), 0o644); err != nil {
		t.Fatalf("write big: %v", err)
	}
	if _, err := client.Upload(ctx, big, "/big.bin", nil); err == nil {
		t.Fatal("expected the upload to be rejected by the quota")
	}

	small := filepath.Join(t.TempDir(), "small.bin")
	if err := os.WriteFile(small, make([]byte, 2048), 0o644); err != nil {
		t.Fatalf("write small: %v", err)
	}
	if _, err := client.Upload(ctx, small, "/small.bin", nil); err != nil {
		t.Fatalf("small upload should fit the quota: %v", err)
	}
	entries, err := client.ListRemote(ctx, "/")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Name == "small.bin" {
			found = true
		}
	}
	if !found {
		t.Fatalf("small.bin missing after upload: %+v", entries)
	}
}

func TestDuplicateRegistrationRejected(t *testing.T) {
	srv := startServer(t, true, true)
	_, tcpAddr := srv.Addrs()
	_, portStr, _ := net.SplitHostPort(tcpAddr)
	client, _ := newClient(t, atoi(portStr))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := client.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := client.Register(ctx, "dup-user", "super-secret-7", "", ""); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if _, err := client.Register(ctx, "dup-user", "super-secret-8", "", ""); err == nil {
		t.Fatal("expected duplicate username to be rejected")
	}
	if _, err := client.Register(ctx, "x", "short", "", ""); err == nil {
		t.Fatal("expected weak password to be rejected")
	}
	// Wrong password must not authenticate.
	if _, err := client.Login(ctx, "dup-user", "wrong-password"); err == nil {
		t.Fatal("expected wrong password login to fail")
	}
	if _, err := client.Login(ctx, "dup-user", "super-secret-7"); err != nil {
		t.Fatalf("valid login failed: %v", err)
	}
}
