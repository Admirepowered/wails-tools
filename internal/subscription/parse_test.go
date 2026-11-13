package subscription

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"wailstools/internal/settings"
)

func TestParsePlainList(t *testing.T) {
	body := `# comment
http://user:pass@1.2.3.4:8080#香港 01
socks5://5.6.7.8:1080#US-02
socks5h://9.9.9.9:1080
vmess://not-supported
http://1.2.3.4:8080
`
	src := Parse(body)
	if len(src.Nodes) != 4 {
		t.Fatalf("expected 4 nodes, got %d: %+v", len(src.Nodes), src.Nodes)
	}
	first := src.Nodes[0]
	if first.Scheme != "http" || first.Host != "1.2.3.4" || first.Port != 8080 {
		t.Fatalf("unexpected first node: %+v", first)
	}
	if first.Username != "user" || first.Password != "pass" {
		t.Fatalf("credentials not parsed: %+v", first)
	}
	if first.Name != "香港 01" {
		t.Fatalf("name not decoded: %q", first.Name)
	}
	if src.Nodes[1].Scheme != "socks5" {
		t.Fatalf("socks5h should map to socks5: %+v", src.Nodes[1])
	}
	// Duplicate endpoint deduplicated.
	if len(src.Nodes) != 4 {
		t.Fatalf("duplicates were not removed: %d", len(src.Nodes))
	}
}

func TestParseBase64List(t *testing.T) {
	plain := "socks5://a:b@10.0.0.1:1080#节点A\nhttp://10.0.0.2:3128#节点B\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(plain))
	src := Parse(encoded)
	if len(src.Nodes) != 2 {
		t.Fatalf("expected 2 nodes from base64, got %d", len(src.Nodes))
	}
	if src.Nodes[0].Host != "10.0.0.1" || src.Nodes[1].Port != 3128 {
		t.Fatalf("unexpected nodes: %+v", src.Nodes)
	}
}

func TestParseClashYAML(t *testing.T) {
	body := `mixed-port: 7890
proxies:
  - name: "日本 01"
    type: socks5
    server: 1.1.1.1
    port: 1080
    username: u1
    password: p1
  - {name: "HK", type: http, server: 2.2.2.2, port: 8080}
  - name: "SS"
    type: ss
    server: 3.3.3.3
    port: 8388
proxy-groups:
  - name: auto
`
	src := Parse(body)
	if len(src.Nodes) != 2 {
		t.Fatalf("expected 2 usable nodes, got %d: %+v", len(src.Nodes), src.Nodes)
	}
	if src.Nodes[0].Name != "日本 01" || src.Nodes[0].Username != "u1" {
		t.Fatalf("block style parse failed: %+v", src.Nodes[0])
	}
	if src.Nodes[1].Scheme != "http" || src.Nodes[1].Port != 8080 {
		t.Fatalf("inline map parse failed: %+v", src.Nodes[1])
	}
}

func TestParseJSONDocuments(t *testing.T) {
	clashJSON := `{"proxies":[{"name":"A","type":"socks5","server":"1.2.3.4","port":1080},{"name":"B","type":"ss","server":"5.6.7.8","port":1}]}`
	src := Parse(clashJSON)
	if len(src.Nodes) != 1 || src.Nodes[0].Scheme != "socks5" {
		t.Fatalf("clash json parse failed: %+v", src.Nodes)
	}
	sip008 := `{"servers":[{"server":"9.9.9.9","server_port":8388,"method":"aes-256-gcm"}]}`
	if got := Parse(sip008); len(got.Nodes) != 0 {
		t.Fatalf("shadowsocks-only subscription must not yield nodes: %+v", got.Nodes)
	}
	bare := `[{"name":"C","type":"http","server":"7.7.7.7","port":3128}]`
	if got := Parse(bare); len(got.Nodes) != 1 {
		t.Fatalf("bare array parse failed: %+v", got.Nodes)
	}
}

func TestParseUserInfo(t *testing.T) {
	info := ParseUserInfo("upload=1024; download=2048; total=10737418240; expire=1893456000")
	if info.Upload != 1024 || info.Download != 2048 || info.Total != 10737418240 || info.Expire != 1893456000 {
		t.Fatalf("unexpected info: %+v", info)
	}
	if got := ParseUserInfo("garbage"); got != (Info{}) {
		t.Fatalf("garbage should parse to zero info: %+v", got)
	}
	merged := MergeInfo(Info{Total: 1}, Info{Download: 5})
	if merged.Total != 1 || merged.Download != 5 {
		t.Fatalf("merge failed: %+v", merged)
	}
}

func TestNodeURL(t *testing.T) {
	n := Node{Scheme: "socks5", Host: "1.2.3.4", Port: 1080, Username: "u", Password: "p"}
	if got := n.URL(); got != "socks5://u:p@1.2.3.4:1080" {
		t.Fatalf("unexpected URL %q", got)
	}
	plain := Node{Scheme: "http", Host: "example.com", Port: 8080}
	if got := plain.URL(); got != "http://example.com:8080" {
		t.Fatalf("unexpected URL %q", got)
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

	// Reload from disk.
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, ok := reopened.Get(saved.ID)
	if !ok || len(got.Nodes) != 1 || got.LastFetch == "" {
		t.Fatalf("persistence failed: %+v", got)
	}
	if err := reopened.Delete(saved.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := reopened.Get(saved.ID); ok {
		t.Fatal("subscription not deleted")
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
	if _, err := os.Stat(filepath.Join(dir, "subscriptions.json")); err != nil {
		t.Fatalf("store file missing: %v", err)
	}
}
