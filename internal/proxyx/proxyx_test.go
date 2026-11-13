package proxyx

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"wailstools/internal/settings"
)

func TestParseProxyURL(t *testing.T) {
	cases := []struct {
		in   string
		mode string
		url  string
		user string
		pass string
	}{
		{"", "off", "", "", ""},
		{"127.0.0.1:7890", "http", "127.0.0.1:7890", "", ""},
		{"http://user:secret@10.0.0.2:8080", "http", "10.0.0.2:8080", "user", "secret"},
		{"socks5://10.0.0.3:1080", "socks5", "10.0.0.3:1080", "", ""},
		{"socks5h://u:p@proxy.local:1080", "socks5", "proxy.local:1080", "u", "p"},
	}
	for _, tc := range cases {
		rule := ParseProxyURL(tc.in)
		if rule.Mode != tc.mode {
			t.Fatalf("%q: mode %q, want %q", tc.in, rule.Mode, tc.mode)
		}
		if rule.URL != tc.url {
			t.Fatalf("%q: url %q, want %q", tc.in, rule.URL, tc.url)
		}
		if rule.Username != tc.user || rule.Password != tc.pass {
			t.Fatalf("%q: credentials %q/%q, want %q/%q", tc.in, rule.Username, rule.Password, tc.user, tc.pass)
		}
	}
}

func TestBypassMatching(t *testing.T) {
	d, err := New(settings.ProxyRule{
		Mode:    "http",
		URL:     "127.0.0.1:1",
		NoProxy: []string{".internal", "192.168.0.0/16", "localhost", "10.1.2.3"},
	})
	if err != nil {
		t.Fatalf("new dialer: %v", err)
	}
	bypassed := []string{
		"api.internal:443",
		"deep.api.internal:22",
		"192.168.5.9:80",
		"localhost:8642",
		"10.1.2.3:22",
	}
	for _, target := range bypassed {
		if !d.Bypassed(target) {
			t.Fatalf("expected %s to bypass the proxy", target)
		}
	}
	notBypassed := []string{"example.com:443", "10.9.9.9:22", "notinternal.com:80"}
	for _, target := range notBypassed {
		if d.Bypassed(target) {
			t.Fatalf("expected %s to use the proxy", target)
		}
	}
}

func TestNewRejectsUnsupportedScheme(t *testing.T) {
	if _, err := New(settings.ProxyRule{Mode: "ftp", URL: "ftp://1.2.3.4:21"}); err == nil {
		t.Fatal("expected unsupported scheme error")
	}
}

func TestNoProxyReturnsSentinel(t *testing.T) {
	if _, err := New(settings.ProxyRule{Mode: "off"}); err != ErrNoProxy {
		t.Fatalf("expected ErrNoProxy, got %v", err)
	}
}

// startConnectProxy runs a minimal HTTP CONNECT proxy and records the targets.
func startConnectProxy(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Proxy-Authorization") == "" {
			http.Error(w, "auth required", http.StatusProxyAuthRequired)
			return
		}
		seen = append(seen, r.Host)
		upstream, err := net.DialTimeout("tcp", r.Host, 5*time.Second)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijack", http.StatusInternalServerError)
			return
		}
		client, _, err := hj.Hijack()
		if err != nil {
			_ = upstream.Close()
			return
		}
		_, _ = client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		go func() { _, _ = io.Copy(upstream, client); _ = upstream.Close() }()
		_, _ = io.Copy(client, upstream)
		_ = client.Close()
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestHTTPClientThroughConnectProxy(t *testing.T) {
	proxySrv, seen := startConnectProxy(t)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "origin-ok")
	}))
	defer origin.Close()

	rule := settings.ProxyRule{Mode: "http", URL: strings.TrimPrefix(proxySrv.URL, "http://"), Username: "user", Password: "pass"}
	client, err := HTTPClient(rule, 10*time.Second)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	resp, err := client.Get(origin.URL)
	if err != nil {
		t.Fatalf("request through proxy: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "origin-ok" {
		t.Fatalf("unexpected body %q", body)
	}
	if len(*seen) == 0 {
		t.Fatal("proxy was not used")
	}
}

func TestDirectClientBypassesProxy(t *testing.T) {
	proxySrv, seen := startConnectProxy(t)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "direct-ok")
	}))
	defer origin.Close()

	client, err := HTTPClient(settings.ProxyRule{Mode: "off"}, 10*time.Second)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	resp, err := client.Get(origin.URL)
	if err != nil {
		t.Fatalf("direct request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "direct-ok" {
		t.Fatalf("unexpected body %q", body)
	}
	if len(*seen) != 0 {
		t.Fatalf("proxy should not have been used, saw %v", *seen)
	}
	_ = proxySrv
}

func TestDialThroughConnectProxyEcho(t *testing.T) {
	proxySrv, seen := startConnectProxy(t)
	// Target TCP service.
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("target listen: %v", err)
	}
	defer target.Close()
	go func() {
		c, err := target.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		br := bufio.NewReader(c)
		line, _ := br.ReadString('\n')
		_, _ = c.Write([]byte("echo:" + line))
	}()

	d, err := New(settings.ProxyRule{
		Mode: "http", URL: strings.TrimPrefix(proxySrv.URL, "http://"),
		Username: "u", Password: "p",
	})
	if err != nil {
		t.Fatalf("dialer: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := d.DialContext(ctx, "tcp", target.Addr().String())
	if err != nil {
		t.Fatalf("dial through proxy: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("hello\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 64)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf[:n]) != "echo:hello\n" {
		t.Fatalf("unexpected echo %q", buf[:n])
	}
	if len(*seen) != 1 {
		t.Fatalf("expected one proxied connection, got %v", *seen)
	}
}

func TestTestTCPDirect(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer lis.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res := TestTCP(ctx, settings.ProxyRule{Mode: "off"}, lis.Addr().String())
	if !res.OK {
		t.Fatalf("expected direct TCP test to pass: %+v", res)
	}
	if res.ViaProxy {
		t.Fatal("direct test must not report a proxy")
	}
}
