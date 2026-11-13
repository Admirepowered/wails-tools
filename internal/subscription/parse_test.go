package subscription

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"wailstools/internal/miproxy"
	"wailstools/internal/settings"
)

func TestParsePlainList(t *testing.T) {
	body := `# comment
http://user:pass@1.2.3.4:8080#香港 01
socks5://5.6.7.8:1080#US-02
vmess://` + base64.StdEncoding.EncodeToString([]byte(`{"v":"2","ps":"HK-VMess","add":"h.example.com","port":"443","id":"11111111-2222-3333-4444-555555555555","aid":"0","net":"ws","path":"/ws","host":"cdn.example.com","tls":"tls"}`)) + `
`
	src := Parse(body)
	if len(src.Nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d: %+v", len(src.Nodes), src.Nodes)
	}
	byName := map[string]miproxy.Node{}
	for _, n := range src.Nodes {
		byName[n.Name] = n
	}
	httpNode, ok := byName["香港 01"]
	if !ok || httpNode.Protocol != miproxy.ProtoHTTP || httpNode.Server != "1.2.3.4" || httpNode.Port != 8080 {
		t.Fatalf("http node parse failed: %+v", httpNode)
	}
	if httpNode.Username != "user" || httpNode.Password != "pass" {
		t.Fatalf("credentials lost: %+v", httpNode)
	}
	vm, ok := byName["HK-VMess"]
	if !ok {
		t.Fatalf("vmess node missing: %+v", src.Nodes)
	}
	if vm.Protocol != miproxy.ProtoVMess || vm.UUID == "" || vm.Network != miproxy.NetWS || !vm.TLS || vm.Path != "/ws" {
		t.Fatalf("vmess fields wrong: %+v", vm)
	}
}

func TestParseBase64Subscription(t *testing.T) {
	plain := "vmess://" + base64.StdEncoding.EncodeToString([]byte(`{"ps":"A","add":"a.com","port":"16632","id":"22222222-3333-4444-5555-666666666666","aid":"0","net":"ws","path":"/","host":"h.com"}`)) + "\n" +
		"trojan://pw@b.com:443?sni=b.com#B\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(plain))
	src := Parse(encoded)
	if len(src.Nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(src.Nodes))
	}
	protocols := map[miproxy.Protocol]bool{}
	for _, n := range src.Nodes {
		protocols[n.Protocol] = true
	}
	if !protocols[miproxy.ProtoVMess] || !protocols[miproxy.ProtoTrojan] {
		t.Fatalf("unexpected protocols: %+v", protocols)
	}
}

func TestParseClashYAML(t *testing.T) {
	body := `mixed-port: 7890
proxies:
  - name: "HK"
    type: vmess
    server: hk.example.com
    port: 443
    uuid: 33333333-4444-5555-6666-777777777777
    alterId: 0
    cipher: auto
    tls: true
    network: ws
    ws-path: /ray
    ws-host: cdn.example.com
  - {name: "SS", type: ss, server: 1.2.3.4, port: 8388, cipher: aes-256-gcm, password: secret}
  - {name: "Junk", type: tuic, server: 9.9.9.9, port: 443}
proxy-groups:
  - name: auto
`
	src := Parse(body)
	if len(src.Nodes) != 2 {
		t.Fatalf("expected 2 usable nodes, got %d: %+v", len(src.Nodes), src.Nodes)
	}
	var vm, ss *miproxy.Node
	for i := range src.Nodes {
		switch src.Nodes[i].Protocol {
		case miproxy.ProtoVMess:
			vm = &src.Nodes[i]
		case miproxy.ProtoSS:
			ss = &src.Nodes[i]
		}
	}
	if vm == nil || vm.Path != "/ray" || vm.Host != "cdn.example.com" || !vm.TLS {
		t.Fatalf("clash vmess parse failed: %+v", vm)
	}
	if ss == nil || ss.Method != "aes-256-gcm" || ss.Password != "secret" {
		t.Fatalf("clash ss parse failed: %+v", ss)
	}
}

func TestParseUserInfoHeader(t *testing.T) {
	info := ParseUserInfo("upload=1024; download=2048; total=10737418240; expire=1893456000")
	if info.Upload != 1024 || info.Download != 2048 || info.Total != 10737418240 || info.Expire != 1893456000 {
		t.Fatalf("unexpected info: %+v", info)
	}
	merged := MergeInfo(Info{Total: 1}, Info{Download: 5})
	if merged.Total != 1 || merged.Download != 5 {
		t.Fatalf("merge failed: %+v", merged)
	}
}

func TestProxyURLForPlainNodes(t *testing.T) {
	n := miproxy.Node{Protocol: miproxy.ProtoSOCKS5, Server: "1.2.3.4", Port: 1080, Username: "u", Password: "p"}
	if got := ProxyURL(n); got != "socks5://u:p@1.2.3.4:1080" {
		t.Fatalf("unexpected URL %q", got)
	}
	vm := miproxy.Node{Protocol: miproxy.ProtoVMess, Server: "x", Port: 1}
	if got := ProxyURL(vm); got != "" {
		t.Fatalf("proxy protocols must not render a plain URL, got %q", got)
	}
}

func TestStorePersistenceAndRefresh(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("subscription-userinfo", "upload=1; download=2; total=3; expire=4")
		_, _ = w.Write([]byte("socks5://u:p@10.0.0.9:1080#节点\n"))
	}))
	defer srv.Close()

	store, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	saved, err := store.Save(Source{Name: "测试订阅", URL: srv.URL, Enabled: true, IntervalHours: 6})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	settingsStore, err := settings.NewStore(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	mgr := NewManager(store, settingsStore, t.Logf)
	res, err := mgr.Refresh(context.Background(), saved.ID)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if res.NodeCount != 1 || len(res.Source.Nodes) != 1 {
		t.Fatalf("unexpected refresh result: %+v", res)
	}
	if res.Source.Info.Total != 3 || res.Source.Info.Expire != 4 {
		t.Fatalf("userinfo not captured: %+v", res.Source.Info)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, found := reopened.Get(saved.ID)
	if !found || len(got.Nodes) != 1 || got.LastFetch == "" {
		t.Fatalf("persistence failed: %+v", got)
	}
	if err := reopened.Delete(saved.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "subscriptions.json")); err != nil {
		t.Fatalf("store file missing: %v", err)
	}
}

func TestRefreshFailureKeepsError(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()
	store, _ := Open(dir)
	saved, _ := store.Save(Source{Name: "bad", URL: srv.URL, Enabled: true})
	settingsStore, _ := settings.NewStore(filepath.Join(dir, "settings.json"))
	mgr := NewManager(store, settingsStore, t.Logf)
	if _, err := mgr.Refresh(context.Background(), saved.ID); err == nil {
		t.Fatal("expected refresh failure")
	}
	got, _ := store.Get(saved.ID)
	if got.LastError == "" {
		t.Fatal("error was not recorded")
	}
}
