// Package dnsx resolves hostnames without relying on the local (possibly
// poisoned) system resolver.
//
// Two modes are supported:
//
//	system — the OS resolver (correct on a clean network)
//	doh    — one or more DNS-over-HTTPS servers, configured either as an
//	         sdns:// stamp (dnscrypt-proxy DNS stamp, protocol 0x02) or as a
//	         plain https:// URL. The server's IP is pinned by the stamp so the
//	         DoH endpoint itself never needs a working local resolver.
package dnsx

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
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

// Mode selects the resolution strategy.
type Mode string

const (
	ModeSystem Mode = "system"
	ModeDoH    Mode = "doh"
)

// Server is one DNS-over-HTTPS endpoint.
type Server struct {
	// Raw is the value the user entered (sdns://… or https://…).
	Raw string `json:"raw"`
	// Host is the TLS server name (SNI + Host header).
	Host string `json:"host"`
	// Path is the DoH request path (usually /dns-query).
	Path string `json:"path"`
	// Addr is a pinned "ip:port" for the endpoint, may be empty.
	Addr string `json:"addr,omitempty"`
	// URL is the effective endpoint used for requests.
	URL string `json:"url"`
	// Name is a human readable label.
	Name string `json:"name,omitempty"`
}

// Endpoint returns the https URL to POST DNS queries to.
func (s Server) Endpoint() string {
	host := s.Host
	if host == "" {
		if u, err := url.Parse(s.URL); err == nil {
			host = u.Hostname()
		}
	}
	path := s.Path
	if path == "" {
		path = "/dns-query"
	}
	return "https://" + host + path
}

// ParseServer accepts an sdns:// stamp or an https:// URL.
func ParseServer(raw string) (Server, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Server{}, errors.New("empty DNS server")
	}
	if strings.HasPrefix(raw, "sdns://") {
		return parseStamp(raw)
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return Server{}, fmt.Errorf("不支持的 DNS 地址：%s", raw)
	}
	path := u.Path
	if path == "" {
		path = "/dns-query"
	}
	return Server{Raw: raw, Host: u.Hostname(), Path: path, URL: u.String(), Name: u.Hostname()}, nil
}

// parseStamp decodes a dnscrypt-proxy DNS stamp (see dnsstamps spec).
//
//	byte      protocol (0x02 = DoH)
//	uint64le  properties
//	lp        server address ("ip:port" or just "ip")
//	vlen+data TLS certificate hashes (ignored)
//	lp        hostname (SNI / Host)
//	lp        path
func parseStamp(raw string) (Server, error) {
	body := strings.TrimPrefix(raw, "sdns://")
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(body, "="))
	if err != nil {
		return Server{}, fmt.Errorf("sdns 解码失败: %w", err)
	}
	if len(data) < 9 {
		return Server{}, errors.New("sdns 内容过短")
	}
	switch data[0] {
	case 0x02: // DoH
	case 0x00, 0x01, 0x03, 0x04, 0x05:
		return Server{}, fmt.Errorf("该 sdns 不是 DoH（协议 %d），目前只支持 DoH", data[0])
	default:
		return Server{}, fmt.Errorf("未知 sdns 协议 %d", data[0])
	}
	props := binary.LittleEndian.Uint64(data[1:9])
	_ = props
	pos := 9
	addr, pos, err := readLP(data, pos)
	if err != nil {
		return Server{}, err
	}
	vlen := 0
	if pos < len(data) {
		vlen = int(data[pos])
		pos++
		if pos+vlen > len(data) {
			return Server{}, errors.New("sdns 证书哈希长度越界")
		}
		pos += vlen
	}
	host, pos, err := readLP(data, pos)
	if err != nil {
		return Server{}, err
	}
	path, _, err := readLP(data, pos)
	if err != nil {
		return Server{}, err
	}
	if host == "" {
		return Server{}, errors.New("sdns 缺少主机名")
	}
	if path == "" {
		path = "/dns-query"
	}
	srv := Server{Raw: raw, Host: host, Path: path, Addr: normalizeAddr(addr), Name: strings.TrimPrefix(strings.TrimPrefix(raw, "sdns://"), "")}
	srv.Name = srv.Host
	srv.URL = "sdns://" + body
	return srv, nil
}

// normalizeAddr turns the stamp address into "ip:port".
func normalizeAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	addr = strings.Trim(addr, "[]")
	if addr == "" {
		return ""
	}
	if _, _, err := net.SplitHostPort(addr); err == nil {
		return addr
	}
	return net.JoinHostPort(addr, "443")
}

func readLP(data []byte, pos int) (string, int, error) {
	if pos >= len(data) {
		return "", pos, errors.New("sdns 字段缺失")
	}
	n := int(data[pos])
	pos++
	if pos+n > len(data) {
		return "", pos, errors.New("sdns 字段越界")
	}
	return string(data[pos : pos+n]), pos + n, nil
}

// Resolver performs the lookups and caches the answers.
type Resolver struct {
	mu       sync.Mutex
	mode     Mode
	servers  []Server
	cache    map[string]cacheEntry
	client   *http.Client
	logf     func(string, ...any)
	cacheTTL time.Duration
}

type cacheEntry struct {
	IP string
	TS time.Time
}

// New builds a resolver. An empty server list with mode=doh falls back to the
// built-in Cloudflare/Google endpoints so the proxy still works out of the box.
func New(mode Mode, servers []Server, logf func(string, ...any)) *Resolver {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if mode != ModeDoH {
		mode = ModeSystem
	}
	if mode == ModeDoH && len(servers) == 0 {
		for _, raw := range builtinServers {
			if srv, err := ParseServer(raw); err == nil {
				servers = append(servers, srv)
			}
		}
	}
	return &Resolver{
		mode:     mode,
		servers:  servers,
		cache:    map[string]cacheEntry{},
		client:   &http.Client{Timeout: 6 * time.Second},
		logf:     logf,
		cacheTTL: 10 * time.Minute,
	}
}

// builtinServers are used when DoH is selected without an explicit list.
var builtinServers = []string{
	"https://cloudflare-dns.com/dns-query",
	"https://dns.google/dns-query",
}

// Mode reports the active strategy.
func (r *Resolver) Mode() Mode {
	if r == nil {
		return ModeSystem
	}
	return r.mode
}

// SetServers replaces the endpoint list at runtime.
func (r *Resolver) SetServers(mode Mode, servers []Server) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if mode != ModeDoH {
		mode = ModeSystem
	}
	r.mode = mode
	r.servers = servers
	r.cache = map[string]cacheEntry{}
}

// Lookup returns one IPv4 (preferred) or IPv6 address for host.
func (r *Resolver) Lookup(ctx context.Context, host string) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", errors.New("empty host")
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return ip.String(), nil
	}
	if r == nil {
		return systemLookup(ctx, host)
	}
	if ip := r.cached(host); ip != "" {
		return ip, nil
	}
	if r.Mode() == ModeDoH {
		// Never fall back to the local resolver here: the whole point of DoH
		// mode is that the local resolver may be poisoned. Report the failure.
		ip, err := r.lookupDoH(ctx, host)
		if err != nil {
			return "", fmt.Errorf("DoH 解析 %s 失败（未回退本地 DNS）: %w", host, err)
		}
		if ip == "" {
			return "", fmt.Errorf("DoH 没有返回 %s 的地址", host)
		}
		r.store(host, ip)
		return ip, nil
	}
	ip, err := systemLookup(ctx, host)
	if err != nil {
		return "", err
	}
	r.store(host, ip)
	return ip, nil
}

// LookupAll returns every address the active resolver knows.
func (r *Resolver) LookupAll(ctx context.Context, host string) ([]string, error) {
	host = strings.TrimSpace(host)
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return []string{ip.String()}, nil
	}
	if r != nil && r.Mode() == ModeDoH {
		ips, err := r.lookupDoHAll(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("DoH 解析 %s 失败（未回退本地 DNS）: %w", host, err)
		}
		return ips, nil
	}
	return systemLookupAll(ctx, host)
}

// DialContext is a drop-in net.Dialer replacement that resolves through us.
func (r *Resolver) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ip, err := r.Lookup(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", host, err)
	}
	d := &net.Dialer{Timeout: 15 * time.Second}
	return d.DialContext(ctx, network, net.JoinHostPort(ip, port))
}

func (r *Resolver) cached(host string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.cache[host]
	if !ok || entry.IP == "" {
		return ""
	}
	if time.Since(entry.TS) > r.cacheTTL {
		delete(r.cache, host)
		return ""
	}
	return entry.IP
}

func (r *Resolver) store(host, ip string) {
	r.mu.Lock()
	r.cache[host] = cacheEntry{IP: ip, TS: time.Now()}
	r.mu.Unlock()
}

// ClearCache drops every cached answer.
func (r *Resolver) ClearCache() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.cache = map[string]cacheEntry{}
	r.mu.Unlock()
}

func systemLookup(ctx context.Context, host string) (string, error) {
	ips, err := systemLookupAll(ctx, host)
	if err != nil {
		return "", err
	}
	for _, ip := range ips {
		if strings.Contains(ip, ".") {
			return ip, nil
		}
	}
	return ips[0], nil
}

func systemLookupAll(ctx context.Context, host string) ([]string, error) {
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(addrs) == 0 {
		if err == nil {
			err = errors.New("no address")
		}
		return nil, err
	}
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.IP.String())
	}
	return out, nil
}

// ---- DoH (RFC 8484 wire format) -----------------------------------------

// buildQuery encodes a DNS query message for name/qtype.
func buildQuery(name string, qtype uint16) ([]byte, uint16, error) {
	var id uint16
	if err := binary.Read(randReader{}, binary.BigEndian, &id); err != nil {
		id = uint16(time.Now().UnixNano())
	}
	buf := make([]byte, 0, 64)
	buf = binary.BigEndian.AppendUint16(buf, id)
	buf = binary.BigEndian.AppendUint16(buf, 0x0100) // RD=1
	buf = binary.BigEndian.AppendUint16(buf, 1)      // QDCOUNT
	buf = binary.BigEndian.AppendUint16(buf, 0)      // ANCOUNT
	buf = binary.BigEndian.AppendUint16(buf, 0)      // NSCOUNT
	buf = binary.BigEndian.AppendUint16(buf, 0)      // ARCOUNT
	for _, label := range strings.Split(strings.Trim(name, "."), ".") {
		if label == "" || len(label) > 63 {
			return nil, 0, fmt.Errorf("非法域名: %s", name)
		}
		buf = append(buf, byte(len(label)))
		buf = append(buf, label...)
	}
	buf = append(buf, 0)
	buf = binary.BigEndian.AppendUint16(buf, qtype)
	buf = binary.BigEndian.AppendUint16(buf, 1) // class IN
	return buf, id, nil
}

// parseAnswers walks the answer section and returns A/AAAA addresses.
func parseAnswers(msg []byte, wantType uint16) ([]string, error) {
	if len(msg) < 12 {
		return nil, errors.New("DNS 响应过短")
	}
	rcode := msg[3] & 0x0F
	if rcode != 0 {
		return nil, fmt.Errorf("DNS RCODE=%d", rcode)
	}
	qd := int(binary.BigEndian.Uint16(msg[4:6]))
	an := int(binary.BigEndian.Uint16(msg[6:8]))
	pos := 12
	for i := 0; i < qd; i++ {
		pos = skipName(msg, pos)
		if pos < 0 || pos+4 > len(msg) {
			return nil, errors.New("DNS 问题段越界")
		}
		pos += 4
	}
	var out []string
	for i := 0; i < an && pos >= 0; i++ {
		pos = skipName(msg, pos)
		if pos < 0 || pos+10 > len(msg) {
			return nil, errors.New("DNS 记录越界")
		}
		rtype := binary.BigEndian.Uint16(msg[pos : pos+2])
		rdlen := int(binary.BigEndian.Uint16(msg[pos+8 : pos+10]))
		pos += 10
		if pos+rdlen > len(msg) {
			return nil, errors.New("DNS 记录数据越界")
		}
		if rtype == wantType {
			switch rtype {
			case 1:
				if rdlen == 4 {
					out = append(out, net.IPv4(msg[pos], msg[pos+1], msg[pos+2], msg[pos+3]).String())
				}
			case 28:
				if rdlen == 16 {
					ip := make(net.IP, 16)
					copy(ip, msg[pos:pos+16])
					out = append(out, ip.String())
				}
			}
		}
		pos += rdlen
	}
	return out, nil
}

// skipName advances past a (possibly compressed) DNS name.
func skipName(msg []byte, pos int) int {
	for pos < len(msg) {
		l := int(msg[pos])
		switch {
		case l == 0:
			return pos + 1
		case l&0xC0 == 0xC0:
			return pos + 2
		default:
			pos += 1 + l
		}
	}
	return -1
}

func (r *Resolver) queryType(ctx context.Context, srv Server, host string, qtype uint16) ([]string, error) {
	msg, _, err := buildQuery(host, qtype)
	if err != nil {
		return nil, err
	}
	endpoint := srv.Endpoint()
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	client := r.clientFor(srv)

	// POST (RFC 8484 §4.1) is the primary path.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(msg))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 65535))
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return parseAnswers(body, qtype)
	}
	if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusMethodNotAllowed && resp.StatusCode != http.StatusUnsupportedMediaType {
		return nil, fmt.Errorf("DoH %s 返回 %s", endpoint, resp.Status)
	}

	// GET ?dns=<base64url> (RFC 8484 §4.1) for servers that only support that.
	q := u.Query()
	q.Set("dns", base64.RawURLEncoding.EncodeToString(msg))
	u.RawQuery = q.Encode()
	greq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	greq.Header.Set("Accept", "application/dns-message")
	gresp, err := client.Do(greq)
	if err != nil {
		return nil, err
	}
	defer gresp.Body.Close()
	gbody, _ := io.ReadAll(io.LimitReader(gresp.Body, 65535))
	if gresp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DoH %s 返回 %s", endpoint, gresp.Status)
	}
	return parseAnswers(gbody, qtype)
}

// clientFor returns an HTTP client that connects to the endpoint's pinned IP
// (when the stamp provides one) while still verifying the TLS certificate for
// the hostname. That is what makes DoH work before DNS works.
func (r *Resolver) clientFor(srv Server) *http.Client {
	if srv.Addr == "" {
		return r.client
	}
	pinHost, pinPort, splitErr := net.SplitHostPort(srv.Addr)
	if splitErr != nil {
		pinHost, pinPort = strings.Trim(srv.Addr, "[]"), "443"
	}
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		port := pinPort
		if _, p, err := net.SplitHostPort(addr); err == nil && p != "" {
			port = p
		}
		d := &net.Dialer{Timeout: 8 * time.Second}
		return d.DialContext(ctx, network, net.JoinHostPort(pinHost, port))
	}
	return &http.Client{
		Timeout: 6 * time.Second,
		Transport: &http.Transport{
			DialContext: dial,
			// Keep SNI = stamp hostname; only the TCP target is pinned.
			ForceAttemptHTTP2: true,
		},
	}
}

// Test queries a server for a well-known name and returns the IP plus latency.
func Test(ctx context.Context, raw string) (map[string]any, error) {
	srv, err := ParseServer(raw)
	if err != nil {
		return nil, err
	}
	res := New(ModeDoH, []Server{srv}, nil)
	start := time.Now()
	ip, err := res.lookupDoH(ctx, "dns.alidns.com")
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"host":     srv.Host,
		"path":     srv.Path,
		"addr":     srv.Addr,
		"endpoint": srv.Endpoint(),
		"sample":   ip,
		"ms":       time.Since(start).Milliseconds(),
	}, nil
}

// randReader is a tiny helper so buildQuery can grab a random message id.
type randReader struct{}

func (randReader) Read(p []byte) (int, error) { return rand.Read(p) }

func (r *Resolver) lookupDoH(ctx context.Context, host string) (string, error) {
	ips, err := r.lookupDoHAll(ctx, host)
	if err != nil {
		return "", err
	}
	for _, ip := range ips {
		if strings.Contains(ip, ".") {
			return ip, nil
		}
	}
	if len(ips) == 0 {
		return "", nil
	}
	return ips[0], nil
}

func (r *Resolver) lookupDoHAll(ctx context.Context, host string) ([]string, error) {
	r.mu.Lock()
	servers := append([]Server(nil), r.servers...)
	r.mu.Unlock()
	if len(servers) == 0 {
		return nil, errors.New("没有配置 DoH 服务器")
	}
	var lastErr error
	for _, srv := range servers {
		for _, qtype := range []uint16{1, 28} {
			ips, err := r.queryType(ctx, srv, host, qtype)
			if err != nil {
				lastErr = err
				r.logf("dns: %s via %s: %v", host, srv.Endpoint(), err)
				break
			}
			if len(ips) > 0 {
				return ips, nil
			}
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("所有 DoH 服务器都没有返回 %s 的地址", host)
	}
	return nil, lastErr
}
