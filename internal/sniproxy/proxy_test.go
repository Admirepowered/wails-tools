package sniproxy

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestParseAndMatchRules(t *testing.T) {
	rules := ParseRules(`
github.com=140.82.116.3,140.82.114.4
.api.github.com
wikipedia.org=208.80.153.224
.wikisource.org=[wikipedia.org]
!raw.example.com=1.2.3.4
plain.example.com
`)
	cases := []struct {
		host     string
		listed   bool
		passthru bool
		ip       string
	}{
		{"github.com", true, false, "140.82.116.3"},
		{"GitHub.COM", true, false, "140.82.116.3"},
		{"en.wikipedia.org", true, false, "208.80.153.224"},
		{"wikisource.org", true, false, "208.80.153.224"},
		{"en.wikisource.org", true, false, "208.80.153.224"},
		{"raw.example.com", true, true, "1.2.3.4"},
		{"plain.example.com", true, false, ""},
		{"example.org", false, false, ""},
	}
	for _, c := range cases {
		rule, ok := rules.Match(c.host)
		if ok != c.listed {
			t.Fatalf("%s: listed=%v want %v", c.host, ok, c.listed)
		}
		if !ok {
			continue
		}
		if rule.Passthrough != c.passthru {
			t.Fatalf("%s: passthrough=%v want %v", c.host, rule.Passthrough, c.passthru)
		}
		got := ""
		if len(rule.IPs) > 0 {
			got = rule.IPs[0]
		}
		if got != c.ip {
			t.Fatalf("%s: ip=%q want %q", c.host, got, c.ip)
		}
	}
}

func TestDefaultRulesCoverGithub(t *testing.T) {
	rules := ParseRules(DefaultRules)
	for _, host := range []string{"github.com", "api.github.com", "raw.githubusercontent.com", "en.wikipedia.org", "pbs.twimg.com"} {
		if _, ok := rules.Match(host); !ok {
			t.Fatalf("default rules should cover %s", host)
		}
	}
}

func TestMitmTunnelThroughProxy(t *testing.T) {
	// Real TLS echo server (the "internet" side).
	cert := selfSignedForTest(t, "origin.test")
	sniSeen := make(chan string, 1)
	originCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			select {
			case sniSeen <- hello.ServerName:
			default:
			}
			return nil, nil
		},
	}
	// MITM only applies to HTTPS ports, so the origin must listen on 8443.
	origin, err := tls.Listen("tcp", "127.0.0.1:8443", originCfg)
	if err != nil {
		t.Skipf("port 8443 unavailable: %v", err)
	}
	defer origin.Close()
	go func() {
		for {
			conn, err := origin.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 64)
				n, err := c.Read(buf)
				if err != nil {
					return
				}
				_, _ = c.Write(buf[:n])
			}(conn)
		}
	}()
	originPort := origin.Addr().(*net.TCPAddr).Port

	dir := t.TempDir()
	ca, err := LoadOrCreateCA(dir)
	if err != nil {
		t.Fatalf("ca: %v", err)
	}
	proxy, err := Start(Options{
		Addr:     "127.0.0.1:0",
		Rules:    ParseRules("origin.test=127.0.0.1"),
		Resolver: NewResolver(dir, t.Logf),
		CA:       ca,
		Mitm:     true,
		Logf:     t.Logf,
	})
	if err != nil {
		t.Fatalf("proxy start: %v", err)
	}
	defer proxy.Close()

	conn, err := net.Dial("tcp", proxy.Addr())
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()
	// Port 8443 is treated as an HTTPS port, exactly like a real CONNECT.
	target := net.JoinHostPort("origin.test", itoa(originPort))
	if _, err := io.WriteString(conn, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n"); err != nil {
		t.Fatalf("connect write: %v", err)
	}
	header := make([]byte, 39)
	if _, err := io.ReadFull(conn, header); err != nil {
		t.Fatalf("connect reply: %v", err)
	}
	if !strings.Contains(string(header), "200") {
		t.Fatalf("unexpected CONNECT reply: %q", header)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca.CertPEM()) {
		t.Fatal("failed to add root CA")
	}
	client := tls.Client(conn, &tls.Config{RootCAs: pool, ServerName: "origin.test"})
	if err := client.Handshake(); err != nil {
		t.Fatalf("client handshake through MITM: %v", err)
	}
	if _, err := client.Write([]byte("ping-mitm")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 9)
	if _, err := io.ReadFull(client, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != "ping-mitm" {
		t.Fatalf("echo mismatch: %q", buf)
	}
	if stats := proxy.Stats(); stats.Mitm != 1 {
		t.Fatalf("mitm counter: %+v", stats)
	}
	// The outbound ClientHello must not carry the destination name.
	select {
	case seen := <-sniSeen:
		if seen != "" {
			t.Fatalf("outbound handshake leaked SNI %q", seen)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("origin never received the handshake")
	}
	_ = originPort
}

func selfSignedForTest(t *testing.T, host string) tls.Certificate {
	t.Helper()
	dir := t.TempDir()
	ca, err := LoadOrCreateCA(dir)
	if err != nil {
		t.Fatalf("ca: %v", err)
	}
	leaf, err := ca.Leaf(host)
	if err != nil {
		t.Fatalf("leaf: %v", err)
	}
	return leaf
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	buf := [8]byte{}
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
