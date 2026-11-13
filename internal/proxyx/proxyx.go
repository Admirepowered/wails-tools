// Package proxyx turns a proxy rule into concrete dialers and HTTP clients.
// It supports HTTP CONNECT proxies and SOCKS5 proxies, plus host bypass lists.
package proxyx

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	xproxy "golang.org/x/net/proxy"

	"wailstools/internal/settings"
)

// ErrNoProxy indicates the rule disables proxying.
var ErrNoProxy = errors.New("proxyx: no proxy configured")

// ErrBypassed indicates the destination matched the bypass list.
var ErrBypassed = errors.New("proxyx: destination bypassed proxy")

// Dialer connects to a destination, honouring the proxy rule.
type Dialer struct {
	rule   settings.ProxyRule
	scheme string
	addr   string
	auth   *authPair
}

type authPair struct{ user, pass string }

// New validates the rule and prepares a dialer.
func New(rule settings.ProxyRule) (*Dialer, error) {
	mode := strings.ToLower(strings.TrimSpace(rule.Mode))
	raw := strings.TrimSpace(rule.URL)
	if mode == "" || mode == "off" || raw == "" {
		return nil, ErrNoProxy
	}
	if !strings.Contains(raw, "://") {
		raw = mode + "://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("proxy URL: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "http", "https", "socks5", "socks5h", "socks4", "socks4a":
	case "socks":
		scheme = "socks5"
	default:
		return nil, fmt.Errorf("proxy scheme %q unsupported", scheme)
	}
	addr := u.Host
	if addr == "" {
		addr = raw
	}
	if !strings.Contains(addr, ":") {
		port := "1080"
		if scheme == "http" || scheme == "https" {
			port = "8080"
		}
		addr = net.JoinHostPort(addr, port)
	}
	d := &Dialer{rule: rule, scheme: scheme, addr: addr}
	user := u.User.Username()
	pass, _ := u.User.Password()
	if user == "" && rule.Username != "" {
		user, pass = rule.Username, rule.Password
	}
	if user != "" {
		d.auth = &authPair{user: user, pass: pass}
	}
	return d, nil
}

// Rule exposes the underlying configuration.
func (d *Dialer) Rule() settings.ProxyRule { return d.rule }

// Bypassed reports whether host matches the rule's bypass list.
func (d *Dialer) Bypassed(hostport string) bool {
	return matchesBypass(hostport, d.rule.NoProxy)
}

func matchesBypass(hostport string, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(strings.ToLower(host), "[]")
	ip := net.ParseIP(host)
	for _, raw := range patterns {
		p := strings.TrimSpace(strings.ToLower(raw))
		if p == "" {
			continue
		}
		if p == "*" {
			return true
		}
		if strings.Contains(p, "/") {
			if _, cidr, err := net.ParseCIDR(p); err == nil && ip != nil && cidr.Contains(ip) {
				return true
			}
			continue
		}
		p = strings.TrimPrefix(p, ".")
		if host == p || strings.HasSuffix(host, "."+p) {
			return true
		}
		if ip != nil && ip.Equal(net.ParseIP(p)) {
			return true
		}
	}
	return false
}

// DialContext connects to addr through the proxy (or directly when bypassed).
func (d *Dialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if d == nil {
		var nd net.Dialer
		return nd.DialContext(ctx, network, addr)
	}
	if d.Bypassed(addr) {
		var nd net.Dialer
		return nd.DialContext(ctx, network, addr)
	}
	switch d.scheme {
	case "http", "https":
		return d.dialConnect(ctx, addr)
	default:
		return d.dialSOCKS(ctx, network, addr)
	}
}

func (d *Dialer) dialSOCKS(ctx context.Context, network, addr string) (net.Conn, error) {
	var auth *xproxy.Auth
	if d.auth != nil {
		auth = &xproxy.Auth{User: d.auth.user, Password: d.auth.pass}
	}
	base := &net.Dialer{Timeout: 15 * time.Second}
	upstream, err := xproxy.SOCKS5("tcp", d.addr, auth, base)
	if err != nil {
		return nil, err
	}
	if cd, ok := upstream.(xproxy.ContextDialer); ok {
		return cd.DialContext(ctx, network, addr)
	}
	type res struct {
		c   net.Conn
		err error
	}
	ch := make(chan res, 1)
	go func() {
		c, err := upstream.Dial(network, addr)
		ch <- res{c, err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		return r.c, r.err
	}
}

func (d *Dialer) dialConnect(ctx context.Context, target string) (net.Conn, error) {
	nd := &net.Dialer{Timeout: 15 * time.Second}
	conn, err := nd.DialContext(ctx, "tcp", d.addr)
	if err != nil {
		return nil, fmt.Errorf("proxy connect %s: %w", d.addr, err)
	}
	if d.scheme == "https" {
		host, _, _ := net.SplitHostPort(d.addr)
		tconn := tls.Client(conn, &tls.Config{ServerName: host})
		if err := tconn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("proxy tls: %w", err)
		}
		conn = tconn
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", target, target)
	if d.auth != nil {
		token := base64.StdEncoding.EncodeToString([]byte(d.auth.user + ":" + d.auth.pass))
		fmt.Fprintf(&b, "Proxy-Authorization: Basic %s\r\n", token)
	}
	b.WriteString("Proxy-Connection: Keep-Alive\r\n\r\n")
	if _, err := conn.Write([]byte(b.String())); err != nil {
		_ = conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("proxy handshake: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_ = conn.Close()
		return nil, fmt.Errorf("proxy CONNECT rejected: %s", resp.Status)
	}
	if br.Buffered() > 0 {
		_ = conn.Close()
		return nil, errors.New("proxyx: unexpected buffered data after CONNECT")
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

// ChainDialers returns a dialer that tries each candidate in order until one
// produces a connection.
func ChainDialers(dialers ...*Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		var lastErr error
		for _, d := range dialers {
			if d == nil {
				continue
			}
			conn, err := d.DialContext(ctx, network, addr)
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		if lastErr == nil {
			lastErr = ErrNoProxy
		}
		return nil, lastErr
	}
}

// HTTPClient builds an http.Client that routes through the rule.
func HTTPClient(rule settings.ProxyRule, timeout time.Duration) (*http.Client, error) {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	tr := &http.Transport{
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 3 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	if !rule.Enabled() {
		tr.Proxy = nil
		return &http.Client{Transport: tr, Timeout: 0}, nil
	}
	d, err := New(rule)
	if err != nil {
		if errors.Is(err, ErrNoProxy) {
			return &http.Client{Transport: tr, Timeout: 0}, nil
		}
		return nil, err
	}
	tr.DialContext = d.DialContext
	tr.Proxy = func(*http.Request) (*url.URL, error) { return nil, nil }
	return &http.Client{Transport: tr, Timeout: 0}, nil
}

// ProxyURLString renders a rule back into a URL string for display.
func ProxyURLString(rule settings.ProxyRule) string {
	if !rule.Enabled() {
		return ""
	}
	raw := rule.URL
	if !strings.Contains(raw, "://") {
		raw = rule.Mode + "://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if rule.Username != "" && u.User == nil {
		u.User = url.UserPassword(rule.Username, rule.Password)
	}
	return u.String()
}

// ParseProxyURL fills a rule from a proxy URL string.
func ParseProxyURL(raw string) settings.ProxyRule {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return settings.ProxyRule{Mode: "off"}
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return settings.ProxyRule{Mode: "http", URL: raw}
	}
	mode := "http"
	if strings.HasPrefix(strings.ToLower(u.Scheme), "socks") {
		mode = "socks5"
	}
	rule := settings.ProxyRule{Mode: mode, URL: u.Host}
	if rule.URL == "" {
		rule.URL = raw
	}
	if u.User != nil {
		rule.Username = u.User.Username()
		rule.Password, _ = u.User.Password()
	}
	return rule
}

// Test pings a URL through the rule to verify connectivity.
type TestResult struct {
	OK         bool   `json:"ok"`
	Detail     string `json:"detail"`
	LatencyMS  int64  `json:"latencyMs"`
	StatusCode int    `json:"statusCode"`
	ViaProxy   bool   `json:"viaProxy"`
	ProxyAddr  string `json:"proxyAddr,omitempty"`
}

// TestProxy verifies a proxy rule by issuing a HEAD/GET to probeURL.
func TestProxy(ctx context.Context, rule settings.ProxyRule, probeURL string) TestResult {
	if probeURL == "" {
		probeURL = "http://www.gstatic.com/generate_204"
	}
	start := time.Now()
	res := TestResult{}
	client, err := HTTPClient(rule, 20*time.Second)
	if err != nil {
		res.Detail = err.Error()
		return res
	}
	if rule.Enabled() {
		res.ViaProxy = true
		if d, err := New(rule); err == nil {
			res.ProxyAddr = d.addr
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	if err != nil {
		res.Detail = err.Error()
		return res
	}
	req.Header.Set("User-Agent", "WailsTools/1.0")
	resp, err := client.Do(req)
	res.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		res.Detail = err.Error()
		return res
	}
	defer resp.Body.Close()
	res.StatusCode = resp.StatusCode
	res.OK = resp.StatusCode < 400 || resp.StatusCode == http.StatusNoContent
	res.Detail = strconv.Itoa(resp.StatusCode) + " " + http.StatusText(resp.StatusCode)
	return res
}

// TestTCP dials host:port through the rule to verify reachability.
func TestTCP(ctx context.Context, rule settings.ProxyRule, hostport string) TestResult {
	start := time.Now()
	res := TestResult{ViaProxy: rule.Enabled()}
	var d *Dialer
	if rule.Enabled() {
		var err error
		d, err = New(rule)
		if err != nil {
			res.Detail = err.Error()
			return res
		}
		res.ProxyAddr = d.addr
	}
	var conn net.Conn
	var err error
	if d != nil {
		conn, err = d.DialContext(ctx, "tcp", hostport)
	} else {
		var nd net.Dialer
		conn, err = nd.DialContext(ctx, "tcp", hostport)
	}
	res.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		res.Detail = err.Error()
		return res
	}
	_ = conn.Close()
	res.OK = true
	res.Detail = "connected to " + hostport
	return res
}
