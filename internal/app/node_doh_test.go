package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"wailstools/internal/dnsx"
	"wailstools/internal/miproxy"
	"wailstools/internal/serverlink"
	"wailstools/internal/settings"
)

// TestNodeThroughDoHAndRelay reproduces the production path for a node whose IP
// the local network blocks:
//
//	DoH resolves the node name -> direct TCP fails -> fall back to the sync server
//
// It needs WT_USER/WT_PASS (sync server credentials) and is skipped otherwise.
func TestNodeThroughDoHAndRelay(t *testing.T) {
	user, pass := os.Getenv("WT_USER"), os.Getenv("WT_PASS")
	if user == "" || pass == "" {
		t.Skip("WT_USER/WT_PASS not set")
	}
	const stamp = "sdns://AgcAAAAAAAAACTIyMy41LjUuNSCY49XlNq8pWM0vfxT3BO9KJ20l4zzWXy5l9eTycnwTMA5kbnMuYWxpZG5zLmNvbQovZG5zLXF1ZXJ5"
	srv, err := dnsx.ParseServer(stamp)
	if err != nil {
		t.Fatalf("stamp: %v", err)
	}
	res := dnsx.New(dnsx.ModeDoH, []dnsx.Server{srv}, func(f string, a ...any) { t.Logf(f, a...) })
	miproxy.HostResolver = res.Lookup
	defer func() { miproxy.HostResolver = nil }()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ip, err := res.Lookup(ctx, "cf.yfjc.sbs")
	if err != nil {
		t.Fatalf("doh: %v", err)
	}
	t.Logf("cf.yfjc.sbs -> %s (DoH)", ip)

	dir := t.TempDir()
	store, err := settings.NewStore(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	store.Update(func(s *settings.Settings) error {
		s.Server.Host = "s2.admirecn.de"
		s.Server.Port = 8642
		s.Server.DeviceID = "wt-doh-test"
		return nil
	})
	link := serverlink.New(store, func(f string, a ...any) { t.Logf(f, a...) })
	if _, err := link.Connect(ctx); err != nil {
		t.Fatalf("sync connect: %v", err)
	}
	if _, err := link.Login(ctx, user, pass); err != nil {
		t.Fatalf("login: %v", err)
	}

	node := miproxy.Node{
		ID: "test-hk", Name: "香港HKBN", Protocol: "vless",
		Server: "cf.yfjc.sbs", Port: 443,
		UUID: "79c66798-1237-40e8-932f-5dce99a439df", Fingerprint: "safari",
		Network: "ws", Path: "/yfjc/hk", Host: "yfjc-hk1.777036.xyz",
		TLS: true, SNI: "yfjc-hk1.777036.xyz",
	}
	d := miproxy.NewDialer(node, 20*time.Second)
	d.FallbackRelay = link.DialTCP
	d.Logf = func(f string, a ...any) { t.Logf(f, a...) }

	conn, err := d.Dial(ctx, "www.google.com:443")
	if err != nil {
		t.Fatalf("dial via node: %v", err)
	}
	defer conn.Close()
	t.Log("connected through the node (DoH + sync-server relay fallback)")
}