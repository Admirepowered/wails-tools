package settings

import (
	"path/filepath"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return s
}

func TestDefaults(t *testing.T) {
	s := newStore(t)
	cfg := s.Get()
	if cfg.Proxy.Default.Mode != "off" {
		t.Fatalf("default proxy mode %q", cfg.Proxy.Default.Mode)
	}
	if cfg.Downloads.MaxConcurrency <= 0 || cfg.Downloads.ConnectionsPer <= 0 || cfg.Downloads.ChunkSize <= 0 {
		t.Fatalf("download defaults not normalised: %+v", cfg.Downloads)
	}
	if cfg.Server.Port == 0 || cfg.Server.Host == "" {
		t.Fatalf("server defaults missing: %+v", cfg.Server)
	}
}

func TestScopeResolution(t *testing.T) {
	s := newStore(t)
	if _, err := s.Update(func(c *Settings) error {
		c.Proxy.Default = ProxyRule{Mode: "http", URL: "127.0.0.1:7890"}
		return nil
	}); err != nil {
		t.Fatalf("set default: %v", err)
	}
	if rule := s.ProxyFor(ScopeSSH); rule.Mode != "http" || rule.URL != "127.0.0.1:7890" {
		t.Fatalf("scope did not inherit default: %+v", rule)
	}
	// Per-scope override wins.
	if _, err := s.SetScopeProxy(ScopeSSH, ProxyRule{Mode: "socks5", URL: "10.0.0.2:1080"}); err != nil {
		t.Fatalf("set scope: %v", err)
	}
	if rule := s.ProxyFor(ScopeSSH); rule.Mode != "socks5" {
		t.Fatalf("override not applied: %+v", rule)
	}
	if rule := s.ProxyFor(ScopeFTP); rule.Mode != "http" {
		t.Fatalf("other scope changed unexpectedly: %+v", rule)
	}
	// Explicitly disabling one scope must survive the global default.
	if _, err := s.SetScopeProxy(ScopeFTP, ProxyRule{Mode: "off"}); err != nil {
		t.Fatalf("disable scope: %v", err)
	}
	if rule := s.ProxyFor(ScopeFTP); rule.Enabled() {
		t.Fatalf("ftp scope should be direct: %+v", rule)
	}
	// Clearing restores inheritance.
	if _, err := s.ClearScopeProxy(ScopeSSH); err != nil {
		t.Fatalf("clear scope: %v", err)
	}
	if rule := s.ProxyFor(ScopeSSH); rule.Mode != "http" {
		t.Fatalf("clear did not restore default: %+v", rule)
	}
}

func TestPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := s.Update(func(c *Settings) error {
		c.Server.Host = "sync.example.com"
		c.Server.Port = 9999
		c.Downloads.Directory = "D:/downloads"
		c.UI.Theme = "light"
		c.Proxy.Scopes = map[string]ProxyRule{
			ScopeNetdisk: {Mode: "socks5", URL: "127.0.0.1:1080", NoProxy: []string{".internal"}},
		}
		return nil
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	reloaded, err := NewStore(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	cfg := reloaded.Get()
	if cfg.Server.Host != "sync.example.com" || cfg.Server.Port != 9999 {
		t.Fatalf("server not persisted: %+v", cfg.Server)
	}
	if cfg.Downloads.Directory != "D:/downloads" || cfg.UI.Theme != "light" {
		t.Fatalf("downloads/ui not persisted: %+v %+v", cfg.Downloads, cfg.UI)
	}
	rule := cfg.Proxy.Resolve(ScopeNetdisk)
	if rule.Mode != "socks5" || len(rule.NoProxy) != 1 {
		t.Fatalf("proxy scope not persisted: %+v", rule)
	}
}

func TestSaveReplacesDocument(t *testing.T) {
	s := newStore(t)
	next := Default()
	next.UI.Theme = "light"
	next.Server.Host = "h"
	saved, err := s.Save(next)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved.UI.Theme != "light" || s.Get().Server.Host != "h" {
		t.Fatalf("save did not apply: %+v", saved)
	}
}

func TestUpdateErrorKeepsPrevious(t *testing.T) {
	s := newStore(t)
	before := s.Get()
	if _, err := s.Update(func(c *Settings) error {
		c.Server.Host = "should-not-apply"
		return errAbort
	}); err == nil {
		t.Fatal("expected error")
	}
	if s.Get().Server.Host == "should-not-apply" || s.Get().Server.Host != before.Server.Host {
		t.Fatal("failed update leaked into the store")
	}
}

func TestProxyEnabled(t *testing.T) {
	cases := []struct {
		rule ProxyRule
		want bool
	}{
		{ProxyRule{Mode: "off", URL: "1.2.3.4:80"}, false},
		{ProxyRule{Mode: "http", URL: ""}, false},
		{ProxyRule{Mode: "http", URL: "1.2.3.4:80"}, true},
		{ProxyRule{Mode: "socks5", URL: "1.2.3.4:1080"}, true},
	}
	for _, tc := range cases {
		if got := tc.rule.Enabled(); got != tc.want {
			t.Fatalf("%+v enabled=%v, want %v", tc.rule, got, tc.want)
		}
	}
}

var errAbort = &abortError{}

type abortError struct{}

func (*abortError) Error() string { return "aborted by test" }
