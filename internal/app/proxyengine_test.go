package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"wailstools/internal/miproxy"
	"wailstools/internal/proxysvc"
	"wailstools/internal/settings"
)

// TestActivateProxyNodeEngine proves the activation path: the engine starts,
// the proxy scopes are rewritten, and traffic actually flows through it.
func TestActivateProxyNodeEngine(t *testing.T) {
	dir := t.TempDir()
	store, err := settings.NewStore(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	a := &App{settings: store, proxySvc: proxysvc.New(nil)}

	// A "direct" node makes the engine forward without an upstream proxy, so
	// the test only depends on loopback.
	node := miproxy.Node{Name: "local-direct", Protocol: miproxy.ProtoDirect}
	res, err := a.ActivateProxyNode(node, nil, true, false, "", "")
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if res["mode"] != "engine" {
		t.Fatalf("expected engine mode, got %v", res["mode"])
	}
	st := a.ProxyRuntimeStatus()
	if !st.Running || st.Addr == "" {
		t.Fatalf("engine not running: %+v", st)
	}

	cfg := store.Get()
	for _, scope := range []string{settings.ScopeHTTP, settings.ScopeNetdisk, settings.ScopeSSH, settings.ScopeFTP} {
		rule := cfg.Proxy.Resolve(scope)
		if rule.Mode != "socks5" || rule.URL != st.Addr {
			t.Fatalf("scope %s not pointed at the engine: %+v", scope, rule)
		}
	}
	// The sync link must stay direct.
	if rule := cfg.Proxy.Resolve(settings.ScopeSync); rule.Mode != "off" {
		t.Fatalf("sync scope must stay direct: %+v", rule)
	}

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "via-engine")
	}))
	defer origin.Close()
	proxyURL, _ := url.Parse("http://" + st.Addr)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}, Timeout: 10 * time.Second}
	resp, err := client.Get(origin.URL)
	if err != nil {
		t.Fatalf("request through engine: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "via-engine" {
		t.Fatalf("unexpected body %q", body)
	}

	// Deactivation stops the engine and restores direct connections.
	cfg2, err := a.DeactivateProxy()
	if err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if a.ProxyRuntimeStatus().Running {
		t.Fatal("engine still running after deactivate")
	}
	for _, scope := range settings.Scopes {
		if rule := cfg2.Proxy.Resolve(scope); rule.Mode != "off" {
			t.Fatalf("scope %s not reset: %+v", scope, rule)
		}
	}
}

func TestActivatePlainUpstreamNode(t *testing.T) {
	dir := t.TempDir()
	store, _ := settings.NewStore(filepath.Join(dir, "settings.json"))
	a := &App{settings: store, proxySvc: proxysvc.New(nil)}

	node := miproxy.Node{Name: "socks-up", Protocol: miproxy.ProtoSOCKS5, Server: "10.0.0.5", Port: 1080, Username: "u", Password: "p"}
	res, err := a.ActivateProxyNode(node, []string{settings.ScopeHTTP}, false, false, "", "")
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if res["mode"] != "upstream" {
		t.Fatalf("expected upstream mode, got %v", res["mode"])
	}
	rule := store.Get().Proxy.Resolve(settings.ScopeHTTP)
	if rule.Mode != "socks5" || rule.URL != "10.0.0.5:1080" || rule.Username != "u" {
		t.Fatalf("unexpected rule: %+v", rule)
	}
	// Other scopes must be untouched.
	if other := store.Get().Proxy.Resolve(settings.ScopeSSH); other.Mode == "socks5" && other.URL == rule.URL {
		t.Fatalf("unexpected scope leak: %+v", other)
	}
}

func TestCustomNodeStoreViaApp(t *testing.T) {
	dir := t.TempDir()
	store, _ := settings.NewStore(filepath.Join(dir, "settings.json"))
	nodes, err := proxysvc.OpenNodes(dir)
	if err != nil {
		t.Fatalf("open nodes: %v", err)
	}
	a := &App{settings: store, proxySvc: proxysvc.New(nil), customNodes: nodes}

	saved, err := a.SaveCustomNode(miproxy.Node{
		Name: "HK", Protocol: miproxy.ProtoVMess, Server: "hk.example.com", Port: 16632,
		UUID: "b7422f14-421f-4618-b45f-22fc92cf4960", Network: "ws", Path: "/", Host: "cdn.example.com",
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved.ID == "" {
		t.Fatal("id not assigned")
	}
	if list := a.ListCustomNodes(); len(list) != 1 || list[0].Name != "HK" {
		t.Fatalf("unexpected list: %+v", list)
	}
	// Invalid nodes are rejected.
	if _, err := a.SaveCustomNode(miproxy.Node{Protocol: miproxy.ProtoVMess, Server: "x", Port: 1}); err == nil {
		t.Fatal("expected missing uuid error")
	}
	if err := a.DeleteCustomNode(saved.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(a.ListCustomNodes()) != 0 {
		t.Fatal("node not deleted")
	}
}
