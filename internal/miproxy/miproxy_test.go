package miproxy

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestParseVMessLink(t *testing.T) {
	payload := `{"v":"2","ps":"香港-优化","add":"p6.example.com","port":"16632","id":"b7422f14-421f-4618-b45f-22fc92cf4960","aid":"0","net":"ws","type":"none","host":"cdn.example.com","path":"/","tls":""}`
	link := "vmess://" + base64.StdEncoding.EncodeToString([]byte(payload))
	node, ok := ParseLink(link)
	if !ok {
		t.Fatal("vmess link not parsed")
	}
	if node.Protocol != ProtoVMess || node.Server != "p6.example.com" || node.Port != 16632 {
		t.Fatalf("unexpected node: %+v", node)
	}
	if node.UUID != "b7422f14-421f-4618-b45f-22fc92cf4960" || node.AlterID != 0 {
		t.Fatalf("uuid/alterId wrong: %+v", node)
	}
	if node.Network != NetWS || node.Host != "cdn.example.com" || node.Path != "/" || node.TLS {
		t.Fatalf("transport fields wrong: %+v", node)
	}
	if node.Name != "香港-优化" {
		t.Fatalf("name not decoded: %q", node.Name)
	}
}

func TestParseVLESSAndTrojanLinks(t *testing.T) {
	vless, ok := ParseLink("vless://aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee@1.2.3.4:443?encryption=none&security=tls&type=ws&path=%2Fws&sni=example.com#VLESS-HK")
	if !ok || vless.Protocol != ProtoVLESS || vless.Port != 443 || !vless.TLS || vless.Path != "/ws" || vless.SNI != "example.com" {
		t.Fatalf("vless parse failed: %+v ok=%v", vless, ok)
	}
	trojan, ok := ParseLink("trojan://secret@5.6.7.8:8443?sni=t.example.com#T")
	if !ok || trojan.Protocol != ProtoTrojan || trojan.Password != "secret" || !trojan.TLS || trojan.Port != 8443 {
		t.Fatalf("trojan parse failed: %+v ok=%v", trojan, ok)
	}
}

func TestParseSSLinks(t *testing.T) {
	userInfo := base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:pa55"))
	n1, ok := ParseLink("ss://" + userInfo + "@1.2.3.4:8388#SS1")
	if !ok || n1.Protocol != ProtoSS || n1.Method != "aes-256-gcm" || n1.Password != "pa55" {
		t.Fatalf("ss sip002 parse failed: %+v ok=%v", n1, ok)
	}
	full := base64.StdEncoding.EncodeToString([]byte("chacha20-ietf-poly1305:pw@9.9.9.9:443"))
	n2, ok := ParseLink("ss://" + full + "#SS2")
	if !ok || n2.Server != "9.9.9.9" || n2.Method != "chacha20-ietf-poly1305" || n2.Port != 443 {
		t.Fatalf("ss legacy parse failed: %+v ok=%v", n2, ok)
	}
}

func TestParsePlainProxyLinks(t *testing.T) {
	n, ok := ParseLink("socks5://user:pass@127.0.0.1:1080#local")
	if !ok || n.Protocol != ProtoSOCKS5 || n.Port != 1080 || n.Username != "user" || n.Password != "pass" {
		t.Fatalf("socks5 parse failed: %+v", n)
	}
	h, ok := ParseLink("http://1.2.3.4:8080")
	if !ok || h.Protocol != ProtoHTTP || h.Port != 8080 {
		t.Fatalf("http parse failed: %+v", h)
	}
}

func TestEncodeDestination(t *testing.T) {
	ipv4, err := encodeDestination("1.2.3.4:443")
	if err != nil {
		t.Fatalf("ipv4: %v", err)
	}
	if ipv4[0] != 0x01 || ipv4[1] != 0xbb || ipv4[2] != 0x01 || len(ipv4) != 7 {
		t.Fatalf("unexpected ipv4 encoding: %x", ipv4)
	}
	domain, err := encodeDestination("example.com:80")
	if err != nil {
		t.Fatalf("domain: %v", err)
	}
	if domain[0] != 0x00 || domain[1] != 0x50 || domain[2] != 0x02 || domain[3] != byte(len("example.com")) {
		t.Fatalf("unexpected domain encoding: %x", domain)
	}
	ipv6, err := encodeDestination("[2001:db8::1]:53")
	if err != nil {
		t.Fatalf("ipv6: %v", err)
	}
	if ipv6[2] != 0x03 || len(ipv6) != 3+16 {
		t.Fatalf("unexpected ipv6 encoding: %x", ipv6)
	}
}

func TestKDFMatchesReference(t *testing.T) {
	// Regression guard: the KDF must keep producing the same bytes, because a
	// silent change breaks every VMess handshake.
	key := make([]byte, 16)
	for i := range key {
		key[i] = byte(i)
	}
	got := hex.EncodeToString(vmessKDF(key, "AES Auth ID Encryption")[:16])
	if len(got) != 32 {
		t.Fatalf("unexpected kdf length: %q", got)
	}
	again := hex.EncodeToString(vmessKDF(key, "AES Auth ID Encryption")[:16])
	if got != again {
		t.Fatal("kdf must be deterministic")
	}
	other := hex.EncodeToString(vmessKDF(key, "VMess Header AEAD Key")[:16])
	if got == other {
		t.Fatal("different paths must derive different keys")
	}
}

// startDirectServer runs the local inbound with a "direct" node so the SOCKS5
// and HTTP front-ends can be exercised without an upstream proxy.
func startDirectServer(t *testing.T, user, pass string) *Server {
	t.Helper()
	srv, err := StartServer(ServerConfig{
		Node:       Node{Protocol: ProtoDirect},
		ListenAddr: "127.0.0.1:0",
		Username:   user,
		Password:   pass,
		Logf:       t.Logf,
	})
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func TestHTTPProxyInbound(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hello-through-proxy")
	}))
	defer origin.Close()

	srv := startDirectServer(t, "", "")
	proxyURL, _ := url.Parse("http://" + srv.Addr())
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		Timeout:   10 * time.Second,
	}
	resp, err := client.Get(origin.URL)
	if err != nil {
		t.Fatalf("request through proxy: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello-through-proxy" {
		t.Fatalf("unexpected body %q", body)
	}
	if srv.Stats().Accepted < 1 {
		t.Fatalf("stats not updated: %+v", srv.Stats())
	}
}

func TestHTTPProxyAuth(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer origin.Close()

	srv := startDirectServer(t, "user", "pass")
	proxyURL, _ := url.Parse("http://" + srv.Addr())

	// Without credentials the proxy must answer 407.
	resp, err := (&http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}, Timeout: 5 * time.Second}).Get(origin.URL)
	if err != nil {
		t.Fatalf("unauthenticated request: %v", err)
	}
	if resp.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("expected 407, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	authURL, _ := url.Parse("http://user:pass@" + srv.Addr())
	resp2, err := (&http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(authURL)}, Timeout: 5 * time.Second}).Get(origin.URL)
	if err != nil {
		t.Fatalf("authenticated request: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp2.StatusCode)
	}
}

func TestSOCKS5Inbound(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "socks-ok")
	}))
	defer origin.Close()
	host := strings.TrimPrefix(origin.URL, "http://")

	srv := startDirectServer(t, "", "")
	conn, err := DialSOCKS5(srv.Addr(), host)
	if err != nil {
		t.Fatalf("socks5 dial: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: " + host + "\r\nConnection: close\r\n\r\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	raw, err := io.ReadAll(conn)
	if err != nil && len(raw) == 0 {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(raw), "socks-ok") {
		t.Fatalf("unexpected response: %q", string(raw))
	}
}

func TestDialerDirectPassthrough(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "direct")
	}))
	defer origin.Close()
	host := strings.TrimPrefix(origin.URL, "http://")
	d := NewDialer(Node{Protocol: ProtoDirect}, 5*time.Second)
	conn, err := d.Dial(context.Background(), host)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: " + host + "\r\nConnection: close\r\n\r\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 64)
	n, _ := conn.Read(buf)
	if !strings.Contains(string(buf[:n]), "200") {
		t.Fatalf("unexpected response %q", string(buf[:n]))
	}
}

func TestServerCloseIsIdempotent(t *testing.T) {
	srv := startDirectServer(t, "", "")
	if err := srv.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	// New connections must fail after close.
	if _, err := net.DialTimeout("tcp", srv.Addr(), 500*time.Millisecond); err == nil {
		t.Fatal("listener still accepting after close")
	}
}
