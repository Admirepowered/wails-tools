package sniproxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Options configures the local SNI proxy.
type Options struct {
	Addr     string
	Rules    *RuleSet
	Resolver *Resolver
	CA       *CA
	// Mitm enables certificate interception for allowlisted domains.
	Mitm bool
	// Upstream dials through the app's existing proxy; nil means direct.
	Upstream func(ctx context.Context, network, addr string) (net.Conn, error)
	Logf     func(format string, args ...any)
	OnEvent  func(kind string, data map[string]any)
}

// Stats is a snapshot of the proxy activity.
type Stats struct {
	Active      int64  `json:"active"`
	Total       int64  `json:"total"`
	Mitm        int64  `json:"mitm"`
	Passthrough int64  `json:"passthrough"`
	Upstream    int64  `json:"upstream"`
	Failed      int64  `json:"failed"`
	LastError   string `json:"lastError"`
}

// Server is a running SNI proxy listener.
type Server struct {
	opts Options
	ln   net.Listener

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool

	active, total, mitm, passthrough, upstream, failed int64
	lastErr                                            string
}

// Start binds the proxy and serves until Close.
func Start(opts Options) (*Server, error) {
	if opts.Addr == "" {
		opts.Addr = "127.0.0.1:17891"
	}
	if opts.Logf == nil {
		opts.Logf = func(format string, args ...any) {}
	}
	ln, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		return nil, err
	}
	s := &Server{opts: opts, ln: ln, conns: map[net.Conn]struct{}{}}
	go s.acceptLoop()
	return s, nil
}

// Addr is the bound address.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Stats snapshots the counters.
func (s *Server) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Stats{
		Active: atomic.LoadInt64(&s.active), Total: atomic.LoadInt64(&s.total),
		Mitm: atomic.LoadInt64(&s.mitm), Passthrough: atomic.LoadInt64(&s.passthrough),
		Upstream: atomic.LoadInt64(&s.upstream), Failed: atomic.LoadInt64(&s.failed),
		LastError: s.lastErr,
	}
}

// Close stops the listener and drops live connections.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	conns := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	err := s.ln.Close()
	for _, c := range conns {
		_ = c.Close()
	}
	return err
}

func (s *Server) acceptLoop() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = conn.Close()
			return
		}
		s.conns[conn] = struct{}{}
		s.mu.Unlock()
		atomic.AddInt64(&s.total, 1)
		atomic.AddInt64(&s.active, 1)
		go func(c net.Conn) {
			defer func() {
				s.mu.Lock()
				delete(s.conns, c)
				s.mu.Unlock()
				atomic.AddInt64(&s.active, -1)
				_ = c.Close()
			}()
			s.handleConn(c)
		}(conn)
	}
}

func (s *Server) fail(format string, args ...any) {
	s.mu.Lock()
	s.lastErr = fmt.Sprintf(format, args...)
	s.mu.Unlock()
	atomic.AddInt64(&s.failed, 1)
	s.opts.Logf(format, args...)
}

func (s *Server) handleConn(conn net.Conn) {
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	br := bufio.NewReader(conn)
	first, err := br.Peek(1)
	if err != nil {
		return
	}
	if first[0] == 0x05 {
		s.handleSOCKS5(conn, br)
		return
	}
	s.handleHTTP(conn, br)
}

// ---- SOCKS5 ----------------------------------------------------------------

func (s *Server) handleSOCKS5(conn net.Conn, br *bufio.Reader) {
	head := make([]byte, 2)
	if _, err := io.ReadFull(br, head); err != nil {
		return
	}
	if head[0] != 5 {
		return
	}
	if _, err := io.ReadFull(br, make([]byte, int(head[1]))); err != nil {
		return
	}
	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
		return
	}
	req := make([]byte, 4)
	if _, err := io.ReadFull(br, req); err != nil {
		return
	}
	if req[1] != 1 { // CONNECT only
		_, _ = conn.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	var host string
	switch req[3] {
	case 1:
		buf := make([]byte, 4)
		if _, err := io.ReadFull(br, buf); err != nil {
			return
		}
		host = net.IP(buf).String()
	case 3:
		l, err := br.ReadByte()
		if err != nil {
			return
		}
		buf := make([]byte, int(l))
		if _, err := io.ReadFull(br, buf); err != nil {
			return
		}
		host = string(buf)
	case 4:
		buf := make([]byte, 16)
		if _, err := io.ReadFull(br, buf); err != nil {
			return
		}
		host = net.IP(buf).String()
	default:
		_, _ = conn.Write([]byte{0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(br, portBuf); err != nil {
		return
	}
	port := int(portBuf[0])<<8 | int(portBuf[1])
	// Success reply first: for MITM the TLS handshake follows immediately.
	if _, err := conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	s.tunnel(&bufferedConn{Conn: conn, r: br}, host, fmt.Sprintf("%d", port))
}

// ---- HTTP CONNECT / plain HTTP --------------------------------------------

func (s *Server) handleHTTP(conn net.Conn, br *bufio.Reader) {
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	if strings.EqualFold(req.Method, http.MethodConnect) {
		host, port, err := net.SplitHostPort(req.Host)
		if err != nil {
			host = req.Host
			port = "443"
		}
		if _, err := conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
			return
		}
		s.tunnel(&bufferedConn{Conn: conn, r: br}, host, port)
		return
	}
	s.forwardHTTP(req, conn)
}

// forwardHTTP proxies a plain HTTP request through the existing upstream.
func (s *Server) forwardHTTP(req *http.Request, conn net.Conn) {
	dial := s.opts.Upstream
	if dial == nil {
		dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, network, addr)
		}
	}
	transport := &http.Transport{
		DisableKeepAlives: true,
		DialContext:       dial,
	}
	req.RequestURI = ""
	if req.URL != nil && req.URL.Host == "" {
		req.URL.Host = req.Host
	}
	if req.URL != nil {
		req.URL.Scheme = "http"
	}
	atomic.AddInt64(&s.upstream, 1)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		s.fail("sniproxy: http %s: %v", req.Host, err)
		_, _ = io.WriteString(conn, "HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\n\r\n")
		return
	}
	defer resp.Body.Close()
	_ = resp.Write(conn)
}

// ---- tunnels ---------------------------------------------------------------

// tunnel decides between MITM, pinned-IP passthrough and the upstream proxy.
func (s *Server) tunnel(conn net.Conn, host, port string) {
	host = normalizeHost(host)
	if host == "" {
		s.fail("sniproxy: empty host")
		return
	}
	rule, listed := s.opts.Rules.Match(host)
	targetPort := port
	if targetPort == "" {
		targetPort = "443"
	}

	if !listed {
		s.rawThroughUpstream(conn, host, targetPort)
		return
	}

	// MITM needs the root CA and the user switch; passthrough rules skip it.
	if s.opts.Mitm && !rule.Passthrough && s.opts.CA != nil && isTLSPort(targetPort) {
		if s.mitmTunnel(conn, host, targetPort) {
			return
		}
		return
	}

	ip, via, err := s.resolve(context.Background(), host)
	if err != nil || ip == "" {
		s.rawThroughUpstream(conn, host, targetPort)
		return
	}
	atomic.AddInt64(&s.passthrough, 1)
	s.opts.Logf("sniproxy: pinned passthrough %s -> %s (%s)", host, ip, via)
	upstream, err := s.dialDirect(context.Background(), net.JoinHostPort(ip, targetPort))
	if err != nil {
		s.fail("sniproxy: dial %s: %v", ip, err)
		return
	}
	defer upstream.Close()
	pipeBoth(conn, upstream)
}

func (s *Server) resolve(ctx context.Context, host string) (string, string, error) {
	if s.opts.Resolver == nil {
		return "", "", errors.New("no resolver")
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return s.opts.Resolver.Lookup(lookupCtx, host, s.opts.Rules)
}

// dialDirect dials the real target, optionally through the existing proxy.
func (s *Server) dialDirect(ctx context.Context, addr string) (net.Conn, error) {
	if s.opts.Upstream != nil {
		return s.opts.Upstream(ctx, "tcp", addr)
	}
	return (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", addr)
}

// mitm terminates TLS with a locally signed certificate and re-originates it to
// the pinned IP while preserving the real SNI.
func (s *Server) mitmTunnel(conn net.Conn, host, port string) bool {
	ip, via, err := s.resolve(context.Background(), host)
	if err != nil || ip == "" {
		s.fail("sniproxy: resolve %s: %v", host, err)
		s.rawThroughUpstream(conn, host, port)
		return false
	}
	atomic.AddInt64(&s.mitm, 1)
	s.opts.Logf("sniproxy: mitm %s -> %s (%s)", host, ip, via)

	serverConf := &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			name := hello.ServerName
			if name == "" {
				name = host
			}
			leaf, err := s.opts.CA.Leaf(name)
			if err != nil {
				return nil, err
			}
			return &leaf, nil
		},
	}
	clientConn := tls.Server(conn, serverConf)
	if err := clientConn.Handshake(); err != nil {
		s.fail("sniproxy: client tls %s: %v", host, err)
		return false
	}
	raw, err := s.dialDirect(context.Background(), net.JoinHostPort(ip, port))
	if err != nil {
		s.fail("sniproxy: upstream %s: %v", ip, err)
		return false
	}
	// Deliberately no SNI: the whole point of this mode is that the outbound
	// ClientHello carries no destination name. Cloudflare and most vhosts still
	// route by the Host header, so the decrypted request keeps working; if the
	// server refuses a nameless handshake the connection just fails.
	upstream := tls.Client(raw, &tls.Config{
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS12,
	})
	if err := upstream.Handshake(); err != nil {
		_ = raw.Close()
		s.fail("sniproxy: upstream tls %s: %v", host, err)
		return false
	}
	pipeBoth(clientConn, upstream)
	return true
}

// rawThroughUpstream forwards the untouched byte stream through the app's
// existing proxy configuration (the default for domains outside the rules).
func (s *Server) rawThroughUpstream(conn net.Conn, host, port string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	atomic.AddInt64(&s.upstream, 1)
	upstream, err := s.dialDirect(ctx, net.JoinHostPort(host, port))
	if err != nil {
		s.fail("sniproxy: dial %s:%s: %v", host, port, err)
		return
	}
	defer upstream.Close()
	pipeBoth(conn, upstream)
}

func pipeBoth(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(b, a)
		if cw, ok := b.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(a, b)
		if cw, ok := a.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		done <- struct{}{}
	}()
	<-done
}

// bufferedConn keeps the bytes already buffered by bufio available to readers.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// isTLSPort reports whether MITM applies to this port.
func isTLSPort(port string) bool {
	return port == "443" || port == "8443"
}
