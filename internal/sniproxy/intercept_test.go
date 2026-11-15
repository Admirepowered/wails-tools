package sniproxy_test

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	"wailstools/internal/miproxy"
	"wailstools/internal/sniproxy"
)

// originServer runs a TLS vhost that echoes one line and records the SNI it saw.
func originServer(t *testing.T) (addr string, sniSeen chan string) {
	t.Helper()
	dir := t.TempDir()
	ca, err := sniproxy.LoadOrCreateCA(dir)
	if err != nil {
		t.Fatalf("ca: %v", err)
	}
	leaf, err := ca.Leaf("origin.test")
	if err != nil {
		t.Fatalf("leaf: %v", err)
	}
	sniSeen = make(chan string, 4)
	cfg := &tls.Config{
		Certificates: []tls.Certificate{leaf},
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			select {
			case sniSeen <- hello.ServerName:
			default:
			}
			return nil, nil
		},
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:8443", cfg)
	if err != nil {
		t.Skipf("port 8443 unavailable: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 6\r\nConnection: close\r\n\r\nSNI-OK")
			}(conn)
		}
	}()
	return ln.Addr().String(), sniSeen
}

// The local proxy must hand matching TLS tunnels to the SNI engine and keep
// everything else on the proxy chain.
func TestLocalProxyInterceptsRulesOnly(t *testing.T) {
	_, sniSeen := originServer(t)

	dir := t.TempDir()
	ca, err := sniproxy.LoadOrCreateCA(dir)
	if err != nil {
		t.Fatalf("ca: %v", err)
	}
	handler := sniproxy.NewHandler(sniproxy.Options{
		Rules:    sniproxy.ParseRules("origin.test=127.0.0.1"),
		Resolver: sniproxy.NewResolver(dir, t.Logf),
		CA:       ca,
		Mitm:     true,
		Logf:     t.Logf,
	})

	var intercepted, chained int64
	proxy, err := miproxy.StartServer(miproxy.ServerConfig{
		Node: miproxy.Node{Protocol: miproxy.ProtoDirect, Name: "direct", Server: "direct", Port: 443},
		Intercept: func(info miproxy.InterceptInfo) bool {
			host := info.Host
			port := info.Port
			if port != "443" && port != "8443" {
				return false
			}
			if _, ok := handler.Match(host); !ok {
				chained++
				return false
			}
			intercepted++
			if info.Kind == "http-connect" {
				_, _ = info.Client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
			}
			conn := info.Client
			if info.Reader != nil {
				conn = &readerConn{Conn: info.Client, R: info.Reader}
			}
			handler.HandleTunnel(conn, host, port)
			return true
		},
		ListenAddr: "127.0.0.1:0",
		Logf:       t.Logf,
	})
	if err != nil {
		t.Fatalf("proxy start: %v", err)
	}
	defer proxy.Close()

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.CertPEM())
	proxyURL, _ := url.Parse("http://" + proxy.Addr())
	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{RootCAs: pool},
		},
	}

	// 1) Covered domain: SNI engine handles it.
	resp, err := client.Get("https://origin.test:8443/")
	if err != nil {
		t.Fatalf("intercepted request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "SNI-OK" {
		t.Fatalf("unexpected body %q", body)
	}
	if intercepted == 0 {
		t.Fatal("rule-covered tunnel was not intercepted")
	}
	select {
	case seen := <-sniSeen:
		if seen != "" {
			t.Fatalf("outbound handshake leaked SNI %q", seen)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("origin never received the tunnel")
	}

	// 2) Uncovered domain: chain handles it (direct node reaches the origin).
	if chained == 0 {
		resp2, err := client.Get("https://127.0.0.1:8443/")
		if err == nil {
			resp2.Body.Close()
		}
		if chained == 0 {
			t.Fatal("non-matching tunnel did not fall through to the chain")
		}
	}
}

type readerConn struct {
	net.Conn
	R io.Reader
}

func (c *readerConn) Read(p []byte) (int, error) { return c.R.Read(p) }
