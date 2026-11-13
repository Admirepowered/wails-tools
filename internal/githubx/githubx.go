// Package githubx talks to GitHub (API, release assets, raw content) in
// networks where the default TLS path is filtered.
//
// Two techniques are combined, mirroring the approach used by Pulse's
// github_client.go and extending it:
//
//  1. IP pinning: the TCP connection goes to a known-good GitHub IP while the
//     TLS ServerName and the HTTP Host header keep the real hostname, so DNS
//     poisoning and IP-based blocks are bypassed.
//  2. ClientHello fragmentation: the first TLS records are split into tiny TCP
//     segments, which defeats DPI that keys on the SNI extension appearing in a
//     single packet.
//
// Every candidate is tried in order and the first working path is reported so
// the UI can show what actually happened.
package githubx

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultPinnedHosts maps well-known GitHub hostnames to IPs that are usually
// reachable when the default resolution is blocked.
var DefaultPinnedHosts = map[string][]string{
	"github.com": {
		"20.27.177.113",
		"140.82.121.4",
		"140.82.116.3",
		"140.82.114.4",
	},
	"api.github.com": {
		"140.82.112.5",
		"140.82.113.5",
		"140.82.114.5",
		"140.82.116.6",
	},
	"objects.githubusercontent.com": {
		"185.199.108.133",
		"185.199.109.133",
		"185.199.110.133",
		"185.199.111.133",
	},
	"release-assets.githubusercontent.com": {
		"185.199.108.133",
		"185.199.109.133",
		"185.199.110.133",
		"185.199.111.133",
	},
	"raw.githubusercontent.com": {
		"185.199.108.133",
		"185.199.109.133",
		"185.199.110.133",
		"185.199.111.133",
	},
	"codeload.github.com": {
		"20.27.177.113",
		"140.82.116.3",
	},
	"github-releases.githubusercontent.com": {
		"185.199.108.133",
		"185.199.109.133",
	},
}

// Config tunes the client.
type Config struct {
	// Fragment splits the TLS ClientHello into small TCP segments.
	Fragment bool
	// FragmentSize is the segment size used while fragmenting (default 4).
	FragmentSize int
	// Insecure skips certificate verification (only for broken middleboxes).
	Insecure bool
	// Timeout bounds each individual attempt.
	Timeout time.Duration
	// PinnedHosts overrides the built-in table (tests, self-hosted mirrors).
	PinnedHosts map[string][]string
	// NoPinning disables the IP-pinning candidates (direct DNS only).
	NoPinning bool
	// UserAgent is sent when the caller does not provide one.
	UserAgent string
}

// Attempt describes one connection strategy.
type Attempt struct {
	Host       string `json:"host"`
	IP         string `json:"ip,omitempty"`
	Fragment   bool   `json:"fragment"`
	OK         bool   `json:"ok"`
	LatencyMS  int64  `json:"latencyMs"`
	StatusCode int    `json:"statusCode,omitempty"`
	Error      string `json:"error,omitempty"`
}

// Result reports the outcome of a request.
type Result struct {
	OK        bool      `json:"ok"`
	Host      string    `json:"host"`
	UsedIP    string    `json:"usedIp,omitempty"`
	Fragment  bool      `json:"fragment"`
	LatencyMS int64     `json:"latencyMs"`
	Attempts  []Attempt `json:"attempts"`
	Error     string    `json:"error,omitempty"`
}

// Client performs GitHub requests with fallbacks.
type Client struct {
	cfg Config

	mu       sync.Mutex
	lastUsed string
}

// New builds a client with sane defaults.
func New(cfg Config) *Client {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 20 * time.Second
	}
	if cfg.FragmentSize <= 0 {
		cfg.FragmentSize = 4
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "WailsTools/1.0"
	}
	if cfg.PinnedHosts == nil {
		cfg.PinnedHosts = DefaultPinnedHosts
	}
	return &Client{cfg: cfg}
}

// LastUsed reports the strategy of the most recent successful request.
func (c *Client) LastUsed() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastUsed
}

type candidate struct {
	ip       string
	fragment bool
}

func (c *Client) candidates(host string, attempted map[string]bool) []candidate {
	host = strings.ToLower(host)
	out := make([]candidate, 0, 8)
	if !c.cfg.NoPinning {
		for _, ip := range c.cfg.PinnedHosts[host] {
			ip = strings.TrimSpace(ip)
			if ip == "" {
				continue
			}
			if c.cfg.Fragment {
				out = append(out, candidate{ip: ip, fragment: true})
			}
			out = append(out, candidate{ip: ip, fragment: false})
		}
	}
	// DNS candidates (direct hostname) as the last resort.
	out = append(out, candidate{ip: "", fragment: false})
	if c.cfg.Fragment {
		out = append(out, candidate{ip: "", fragment: true})
	}
	filtered := out[:0]
	for _, cand := range out {
		key := fmt.Sprintf("%s|%v", cand.ip, cand.fragment)
		if attempted[key] {
			continue
		}
		attempted[key] = true
		filtered = append(filtered, cand)
	}
	return filtered
}

// Do performs a request, trying every candidate until one succeeds.
func (c *Client) Do(ctx context.Context, method, target string, body io.Reader, headers map[string]string) (*http.Response, *Result, error) {
	parsed, err := url.Parse(target)
	if err != nil {
		return nil, nil, err
	}
	host := parsed.Hostname()
	if host == "" {
		return nil, nil, fmt.Errorf("githubx: missing host in %q", target)
	}
	res := &Result{Host: host}
	start := time.Now()
	attempted := map[string]bool{}
	var fallback *http.Response

	for _, cand := range c.candidates(host, attempted) {
		attemptStart := time.Now()
		attempt := Attempt{Host: host, IP: cand.ip, Fragment: cand.fragment}
		resp, err := c.doOnce(ctx, method, parsed, body, headers, cand)
		attempt.LatencyMS = time.Since(attemptStart).Milliseconds()
		if err != nil {
			attempt.Error = err.Error()
			res.Attempts = append(res.Attempts, attempt)
			continue
		}
		attempt.OK = true
		attempt.StatusCode = resp.StatusCode
		res.Attempts = append(res.Attempts, attempt)

		// A 4xx/5xx from one vhost may simply mean the pinned IP serves a
		// different hostname; keep the response and try the next candidate.
		if resp.StatusCode >= 400 {
			if fallback != nil {
				_ = fallback.Body.Close()
			}
			fallback = resp
			continue
		}

		// Follow redirects manually so every hop is re-pinned.
		if loc := resp.Header.Get("Location"); loc != "" && resp.StatusCode >= 300 && resp.StatusCode < 400 {
			next, err := parsed.Parse(loc)
			if err != nil {
				_ = resp.Body.Close()
				return nil, res, err
			}
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			hopResp, hopRes, err := c.Do(ctx, method, next.String(), body, headers)
			if hopRes != nil {
				res.Attempts = append(res.Attempts, hopRes.Attempts...)
			}
			if err != nil {
				res.Error = err.Error()
				return nil, res, err
			}
			res.OK = true
			res.UsedIP = cand.ip
			res.Fragment = cand.fragment
			res.LatencyMS = time.Since(start).Milliseconds()
			c.recordUsed(cand)
			return hopResp, res, nil
		}

		res.OK = true
		res.UsedIP = cand.ip
		res.Fragment = cand.fragment
		res.LatencyMS = time.Since(start).Milliseconds()
		c.recordUsed(cand)
		return resp, res, nil
	}

	if fallback != nil {
		res.OK = true
		res.UsedIP = ""
		res.LatencyMS = time.Since(start).Milliseconds()
		return fallback, res, nil
	}

	res.LatencyMS = time.Since(start).Milliseconds()
	res.Error = "all github candidates failed"
	if len(res.Attempts) > 0 {
		res.Error = res.Attempts[len(res.Attempts)-1].Error
	}
	return nil, res, errors.New("githubx: " + res.Error)
}

func (c *Client) recordUsed(cand candidate) {
	c.mu.Lock()
	switch {
	case cand.ip == "" && !cand.fragment:
		c.lastUsed = "direct"
	case cand.ip == "" && cand.fragment:
		c.lastUsed = "direct+fragment"
	case cand.fragment:
		c.lastUsed = "pinned " + cand.ip + " +fragment"
	default:
		c.lastUsed = "pinned " + cand.ip
	}
	c.mu.Unlock()
}

func (c *Client) doOnce(ctx context.Context, method string, parsed *url.URL, body io.Reader, headers map[string]string, cand candidate) (*http.Response, error) {
	port := parsed.Port()
	if port == "" {
		port = "443"
	}
	host := parsed.Hostname()
	address := net.JoinHostPort(host, port)
	if cand.ip != "" {
		address = net.JoinHostPort(cand.ip, port)
	}

	dialer := &net.Dialer{Timeout: c.cfg.Timeout, KeepAlive: 30 * time.Second}
	dialCtx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	rawConn, err := dialer.DialContext(dialCtx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", address, err)
	}

	conn := rawConn
	if cand.fragment {
		conn = newFragmentConn(rawConn, c.cfg.FragmentSize)
	}

	tlsConf := &tls.Config{
		ServerName:         parsed.Hostname(),
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: c.cfg.Insecure,
		NextProtos:         []string{"http/1.1"},
	}
	tlsConn := tls.Client(conn, tlsConf)
	if err := tlsConn.HandshakeContext(dialCtx); err != nil {
		_ = rawConn.Close()
		return nil, fmt.Errorf("tls handshake with %s: %w", address, err)
	}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return tlsConn, nil
		},
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return tlsConn, nil
		},
		ForceAttemptHTTP2:     false,
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: c.cfg.Timeout,
	}
	// Only one request per connection: the TLS conn is already established.
	req, err := http.NewRequestWithContext(ctx, method, parsed.String(), body)
	if err != nil {
		_ = tlsConn.Close()
		return nil, err
	}
	req.Host = parsed.Host
	req.URL.Host = parsed.Host
	req.Header.Set("Host", parsed.Host)
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", c.cfg.UserAgent)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		_ = tlsConn.Close()
		return nil, err
	}
	resp.Body = &closingBody{ReadCloser: resp.Body, conn: tlsConn}
	return resp, nil
}

type closingBody struct {
	io.ReadCloser
	conn net.Conn
}

func (b *closingBody) Close() error {
	err := b.ReadCloser.Close()
	_ = b.conn.Close()
	return err
}

// GetJSON performs a GET and decodes a JSON body.
func (c *Client) GetJSON(ctx context.Context, target string, out any, headers map[string]string) (*Result, error) {
	if headers == nil {
		headers = map[string]string{}
	}
	if _, ok := headers["Accept"]; !ok {
		headers["Accept"] = "application/vnd.github+json"
	}
	resp, res, err := c.Do(ctx, http.MethodGet, target, nil, headers)
	if err != nil {
		return res, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return res, fmt.Errorf("githubx: %s returned %d: %s", target, resp.StatusCode, strings.TrimSpace(string(detail)))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return res, fmt.Errorf("githubx: decode %s: %w", target, err)
	}
	return res, nil
}

// Probe checks whether GitHub is reachable and reports the working strategy.
func (c *Client) Probe(ctx context.Context) Result {
	start := time.Now()
	resp, res, err := c.Do(ctx, http.MethodGet, "https://api.github.com/rate_limit", nil, map[string]string{
		"Accept": "application/vnd.github+json",
	})
	if err != nil {
		if res == nil {
			res = &Result{Host: "api.github.com"}
		}
		res.OK = false
		res.Error = err.Error()
		res.LatencyMS = time.Since(start).Milliseconds()
		return *res
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	res.OK = resp.StatusCode < 400
	if !res.OK {
		res.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	res.LatencyMS = time.Since(start).Milliseconds()
	return *res
}
