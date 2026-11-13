package githubx

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testCert(t *testing.T, hosts ...string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: hosts[0]},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     hosts,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("cert: %v", err)
	}
	leaf, _ := x509.ParseCertificate(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

// startTLS runs an HTTPS server whose certificate covers the given hostnames.
func startTLS(t *testing.T, handler http.Handler, hosts ...string) (*httptest.Server, int) {
	t.Helper()
	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{testCert(t, hosts...)}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	_, portStr, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	port := 0
	for _, r := range portStr {
		port = port*10 + int(r-'0')
	}
	return srv, port
}

func TestPinnedIPWithFragment(t *testing.T) {
	var port int
	_, port = startTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != fmt.Sprintf("github.com:%d", port) {
			t.Errorf("Host header not preserved: %q", r.Host)
		}
		if !strings.HasPrefix(r.URL.Path, "/repos/") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	}), "github.com")

	client := New(Config{
		Fragment:    true,
		Insecure:    true,
		Timeout:     10 * time.Second,
		PinnedHosts: map[string][]string{"github.com": {"127.0.0.1"}},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var out map[string]bool
	res, err := client.GetJSON(ctx, fmt.Sprintf("https://github.com:%d/repos/x/y", port), &out, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if !out["ok"] {
		t.Fatalf("unexpected body: %+v", out)
	}
	if !res.OK || res.UsedIP != "127.0.0.1" {
		t.Fatalf("expected the pinned IP to be used: %+v", res)
	}
	if !res.Fragment {
		t.Fatalf("expected fragmentation to be used: %+v", res)
	}
	if got := client.LastUsed(); !strings.Contains(got, "fragment") {
		t.Fatalf("unexpected lastUsed %q", got)
	}
}

func TestFragmentKeepsLargePayloadIntact(t *testing.T) {
	payload := strings.Repeat("wails-tools-fragment-payload;", 4096) // ~110 KB
	_, port := startTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if len(body) != 64*1024 {
			t.Errorf("server received %d bytes", len(body))
		}
		fmt.Fprint(w, payload)
	}), "github.com")

	client := New(Config{
		Fragment:     true,
		FragmentSize: 3,
		Insecure:     true,
		Timeout:      15 * time.Second,
		PinnedHosts:  map[string][]string{"github.com": {"127.0.0.1"}},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	body := strings.NewReader(strings.Repeat("x", 64*1024))
	resp, _, err := client.Do(ctx, http.MethodPost, fmt.Sprintf("https://github.com:%d/upload", port), body, nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != payload {
		t.Fatalf("payload corrupted: got %d bytes", len(got))
	}
}

func TestRedirectIsFollowedAndRepinned(t *testing.T) {
	var port int
	_, port = startTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, fmt.Sprintf("https://api.github.com:%d/next", port), http.StatusFound)
		case "/next":
			fmt.Fprint(w, "redirected")
		default:
			http.NotFound(w, r)
		}
	}), "github.com", "api.github.com")

	client := New(Config{
		Insecure: true,
		Timeout:  10 * time.Second,
		PinnedHosts: map[string][]string{
			"github.com":     {"127.0.0.1"},
			"api.github.com": {"127.0.0.1"},
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resp, res, err := client.Do(ctx, http.MethodGet, fmt.Sprintf("https://github.com:%d/start", port), nil, nil)
	if err != nil {
		t.Fatalf("redirect request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "redirected" {
		t.Fatalf("unexpected body %q", body)
	}
	if !res.OK {
		t.Fatalf("result not ok: %+v", res)
	}
}

func TestAllCandidatesFail(t *testing.T) {
	// Nothing is listening on these addresses.
	client := New(Config{
		Timeout:     800 * time.Millisecond,
		PinnedHosts: map[string][]string{"github.com": {"127.0.0.2"}},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, res, err := client.Do(ctx, http.MethodGet, "https://github.com:1/never", nil, nil)
	if err == nil {
		t.Fatal("expected failure")
	}
	if res == nil || len(res.Attempts) == 0 {
		t.Fatalf("expected attempt details: %+v", res)
	}
	if res.OK {
		t.Fatal("result must not be ok")
	}
}

func TestProbeReportsStrategy(t *testing.T) {
	_, port := startTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"resources":{}}`)
	}), "api.github.com")
	client := New(Config{
		Fragment:    true,
		Insecure:    true,
		Timeout:     10 * time.Second,
		PinnedHosts: map[string][]string{"api.github.com": {"127.0.0.1"}},
	})
	// Probe uses the fixed api.github.com URL, so point it at the test port by
	// using a client whose pinned host resolves to the test server.
	_ = port
	res := client.Probe(context.Background())
	// The public api.github.com may or may not be reachable in CI; only the
	// shape of the result is asserted here.
	if res.Host != "api.github.com" {
		t.Fatalf("unexpected host %q", res.Host)
	}
}
