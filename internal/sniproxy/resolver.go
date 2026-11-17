package sniproxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// dnsCacheTTL mirrors the reference resolver: three days.
const dnsCacheTTL = 3 * 24 * time.Hour

// dohEndpoints are pinned Cloudflare DNS endpoints. Resolving the DoH hostname
// itself is exactly what is broken on censored networks, so the IPs are fixed.
type dohEndpoint struct {
	Host string
	IP   string
	Path string
}

var dohEndpoints = []dohEndpoint{
	{"cloudflare-dns.com", "104.16.249.249", "/dns-query"},
	{"cloudflare-dns.com", "104.16.248.249", "/dns-query"},
	{"cloudflare-dns.com", "1.1.1.1", "/dns-query"},
	{"cloudflare-dns.com", "1.0.0.1", "/dns-query"},
	{"dns.google", "8.8.8.8", "/resolve"},
	{"dns.google", "8.8.4.4", "/resolve"},
}

type cacheEntry struct {
	IP string `json:"ip"`
	TS int64  `json:"ts"`
}

// Resolver resolves domains to IPs with a 3-day cache, pinned rules first and
// DNS-over-HTTPS (via pinned Cloudflare IPs) as the self-iterating fallback.
type Resolver struct {
	path string

	mu    sync.Mutex
	cache map[string]cacheEntry

	client *http.Client
	proxyC *http.Client
	logf   func(string, ...any)
}

// NewResolver creates a resolver storing its cache under root/sniproxy.
func NewResolver(dir string, logf func(string, ...any)) *Resolver {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	r := &Resolver{path: filepath.Join(dir, "dns-cache.json"), cache: map[string]cacheEntry{}, logf: logf}
	_ = os.MkdirAll(dir, 0o755)
	if data, err := os.ReadFile(r.path); err == nil {
		var loaded map[string]cacheEntry
		if json.Unmarshal(data, &loaded) == nil && loaded != nil {
			r.cache = loaded
		}
	}
	r.client = &http.Client{
		Timeout: 8 * time.Second,
		Transport: &http.Transport{
			// DoH is reached by pinned IP *without SNI*: the local network
			// resets handshakes that carry the DoH hostname, while Cloudflare's
			// default certificate for these IPs is exactly cloudflare-dns.com.
			// Verification still checks the certificate against that name.
			DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				ip := pinnedDohIP(host)
				if ip == "" {
					ip = host
				}
				raw, err := (&net.Dialer{Timeout: 6 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip, port))
				if err != nil {
					return nil, err
				}
				tc := tls.Client(raw, noSNIConfig(host))
				if err := tc.HandshakeContext(ctx); err != nil {
					_ = raw.Close()
					return nil, err
				}
				return tc, nil
			},
		},
	}
	r.proxyC = &http.Client{Timeout: 10 * time.Second}
	return r
}

// SetUpstream routes DoH through the app proxy when the direct path is blocked.
func (r *Resolver) SetUpstream(dial func(ctx context.Context, network, addr string) (net.Conn, error)) {
	if dial == nil {
		return
	}
	r.proxyC = &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				// The proxy may resolve names through a poisoned resolver, so
				// hand it the pinned DoH IP directly while TLS still verifies
				// the cloudflare-dns.com certificate.
				host, port, err := net.SplitHostPort(addr)
				if err == nil {
					if ip := pinnedDohIP(host); ip != "" {
						addr = net.JoinHostPort(ip, port)
					}
				}
				return dial(ctx, network, addr)
			},
		},
	}
}

func pinnedDohIP(host string) string {
	for _, e := range dohEndpoints {
		if strings.EqualFold(e.Host, host) {
			return e.IP
		}
	}
	return ""
}

// External is an optional resolution hook (the app's DNS settings: system or
// DoH). When set it replaces the built-in DoH+system chain, so the user's DNS
// choice also applies to connections that do not go through the proxy.
var externalLookup func(ctx context.Context, host string) (string, error)

// SetExternal installs the app-level resolver.
func (r *Resolver) SetExternal(fn func(ctx context.Context, host string) (string, error)) {
	externalLookup = fn
}

// Lookup returns the IP for host. Pinned rules win, then the cache, then the
// configured resolver (external hook), then DoH, then the system resolver.
func (r *Resolver) Lookup(ctx context.Context, host string, rules *RuleSet) (string, string, error) {
	host = normalizeHost(host)
	if rule, ok := rules.Match(host); ok && len(rule.IPs) > 0 {
		return rule.IPs[0], "pinned", nil
	}
	if ip := r.cached(host); ip != "" {
		return ip, "cache", nil
	}
	if externalLookup != nil {
		if ip, err := externalLookup(ctx, host); err == nil && ip != "" {
			r.store(host, ip)
			return ip, "configured", nil
		} else if err != nil {
			// Do not fall through to the local resolver when the user picked a
			// custom DNS: report the error instead of a possibly poisoned answer.
			r.logf("sniproxy: configured dns %s: %v", host, err)
			return "", "", err
		}
	}
	if ip, err := r.lookupDoH(ctx, host); err == nil && ip != "" {
		r.store(host, ip)
		return ip, "doh", nil
	} else if err != nil {
		r.logf("sniproxy: doh lookup %s: %v", host, err)
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		if err == nil {
			err = errors.New("no address")
		}
		return "", "", err
	}
	for _, ip := range ips {
		if v4 := ip.To4(); v4 != nil {
			r.store(host, v4.String())
			return v4.String(), "system", nil
		}
	}
	r.store(host, ips[0].String())
	return ips[0].String(), "system", nil
}

func (r *Resolver) cached(host string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.cache[host]
	if !ok || entry.IP == "" {
		return ""
	}
	if time.Since(time.Unix(entry.TS, 0)) > dnsCacheTTL {
		delete(r.cache, host)
		return ""
	}
	return entry.IP
}

func (r *Resolver) store(host, ip string) {
	r.mu.Lock()
	r.cache[host] = cacheEntry{IP: ip, TS: time.Now().Unix()}
	data, err := json.MarshalIndent(r.cache, "", "  ")
	r.mu.Unlock()
	if err != nil {
		return
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err == nil {
		_ = os.Rename(tmp, r.path)
	}
}

// CacheSize reports how many domains are cached.
func (r *Resolver) CacheSize() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.cache)
}

type dohResponse struct {
	Status int `json:"Status"`
	Answer []struct {
		Name string `json:"name"`
		Type int    `json:"type"`
		Data string `json:"data"`
	} `json:"Answer"`
}

func (r *Resolver) lookupDoH(ctx context.Context, host string) (string, error) {
	var lastErr error
	type attempt struct {
		client    *http.Client
		endpoints []dohEndpoint
	}
	// When an upstream proxy is configured it is tried first: plain DoH is
	// blocked in exactly the networks where this feature is needed.
	attempts := []attempt{{client: r.client, endpoints: dohEndpoints[:minInt(2, len(dohEndpoints))]}}
	if r.proxyC != nil {
		attempts = []attempt{
			{client: r.proxyC, endpoints: dohEndpoints},
			{client: r.client, endpoints: dohEndpoints[:minInt(2, len(dohEndpoints))]},
		}
	}
	for _, att := range attempts {
		if ctx.Err() != nil {
			break
		}
		for _, endpoint := range att.endpoints {
			if ctx.Err() != nil {
				break
			}
			name := normalizeHost(host)
			url := fmt.Sprintf("https://%s%s?name=%s&type=A", endpoint.Host, endpoint.Path, name)
			reqCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
			req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
			if err != nil {
				cancel()
				return "", err
			}
			req.Header.Set("Accept", "application/dns-json")
			req.Header.Set("User-Agent", "WailsTools/1.0")
			resp, err := att.client.Do(req)
			if err != nil {
				cancel()
				lastErr = err
				continue
			}
			var parsed dohResponse
			decodeErr := json.NewDecoder(resp.Body).Decode(&parsed)
			_ = resp.Body.Close()
			cancel()
			if decodeErr != nil {
				lastErr = decodeErr
				continue
			}
			if parsed.Status != 0 {
				lastErr = fmt.Errorf("dns status %d", parsed.Status)
				continue
			}
			for _, ans := range parsed.Answer {
				if ans.Type != 1 {
					continue
				}
				if ip := net.ParseIP(ans.Data); ip != nil {
					if v4 := ip.To4(); v4 != nil {
						return v4.String(), nil
					}
				}
			}
			lastErr = errors.New("no A record")
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no DoH endpoint")
	}
	return "", lastErr
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// noSNIConfig performs a TLS handshake without the SNI extension while still
// verifying that the certificate is valid for host.
func noSNIConfig(host string) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // manual verification below (no SNI is sent)
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("doh: peer sent no certificate")
			}
			intermediates := x509.NewCertPool()
			for _, cert := range cs.PeerCertificates[1:] {
				intermediates.AddCert(cert)
			}
			_, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{DNSName: host, Intermediates: intermediates})
			return err
		},
	}
}
