package sniproxy

import (
	"context"
	"crypto/tls"
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
var dohEndpoints = []struct {
	Host string
	IP   string
}{
	{"cloudflare-dns.com", "104.16.249.249"},
	{"cloudflare-dns.com", "104.16.248.249"},
	{"cloudflare-dns.com", "1.1.1.1"},
	{"cloudflare-dns.com", "1.0.0.1"},
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
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				ip := pinnedDohIP(host)
				if ip == "" {
					return (&net.Dialer{Timeout: 6 * time.Second}).DialContext(ctx, network, addr)
				}
				return (&net.Dialer{Timeout: 6 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip, port))
			},
		},
	}
	return r
}

func pinnedDohIP(host string) string {
	for _, e := range dohEndpoints {
		if strings.EqualFold(e.Host, host) {
			return e.IP
		}
	}
	return ""
}

// Lookup returns the IP for host. Pinned rules win, then the cache, then DoH,
// then the system resolver.
func (r *Resolver) Lookup(ctx context.Context, host string, rules *RuleSet) (string, string, error) {
	host = normalizeHost(host)
	if rule, ok := rules.Match(host); ok && len(rule.IPs) > 0 {
		return rule.IPs[0], "pinned", nil
	}
	if ip := r.cached(host); ip != "" {
		return ip, "cache", nil
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
	for _, endpoint := range dohEndpoints {
		name := normalizeHost(host)
		url := fmt.Sprintf("https://%s/dns-query?name=%s&type=A", endpoint.Host, name)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Accept", "application/dns-json")
		req.Header.Set("User-Agent", "WailsTools/1.0")
		resp, err := r.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		var parsed dohResponse
		decodeErr := json.NewDecoder(resp.Body).Decode(&parsed)
		_ = resp.Body.Close()
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
	if lastErr == nil {
		lastErr = errors.New("no DoH endpoint")
	}
	return "", lastErr
}
