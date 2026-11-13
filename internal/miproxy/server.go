package miproxy

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ServerConfig configures the local mixed inbound.
type ServerConfig struct {
	Node Node
	// ListenAddr is the bind address, e.g. "127.0.0.1:0" (internal only) or
	// "0.0.0.0:1080" (shareable with other machines).
	ListenAddr string
	// Username/Password enable SOCKS5 user/pass auth and HTTP Basic proxy auth.
	Username string
	Password string
	Logf     func(format string, args ...any)
}

// Stats counts inbound activity.
type Stats struct {
	Accepted  int64  `json:"accepted"`
	Active    int64  `json:"active"`
	Failed    int64  `json:"failed"`
	BytesIn   int64  `json:"bytesIn"`
	BytesOut  int64  `json:"bytesOut"`
	StartedAt string `json:"startedAt"`
}

// Server is a running local mixed proxy.
type Server struct {
	cfg    ServerConfig
	ln     net.Listener
	dialer *Dialer
	logf   func(string, ...any)

	accepted int64
	active   int64
	failed   int64
	bytesIn  int64
	bytesOut int64
	started  time.Time

	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
	conns  map[net.Conn]struct{}
}

// StartServer binds the listener and serves until Close.
func StartServer(cfg ServerConfig) (*Server, error) {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "127.0.0.1:0"
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", cfg.ListenAddr, err)
	}
	s := &Server{
		cfg:     cfg,
		ln:      ln,
		dialer:  NewDialer(cfg.Node, 15*time.Second),
		logf:    cfg.Logf,
		started: time.Now(),
		conns:   map[net.Conn]struct{}{},
	}
	s.wg.Add(1)
	go s.acceptLoop()
	return s, nil
}

// Addr returns the bound address.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Node returns the active node.
func (s *Server) Node() Node { return s.cfg.Node }

// Stats snapshots counters.
func (s *Server) Stats() Stats {
	return Stats{
		Accepted:  atomic.LoadInt64(&s.accepted),
		Active:    atomic.LoadInt64(&s.active),
		Failed:    atomic.LoadInt64(&s.failed),
		BytesIn:   atomic.LoadInt64(&s.bytesIn),
		BytesOut:  atomic.LoadInt64(&s.bytesOut),
		StartedAt: s.started.Format(time.RFC3339),
	}
}

// Close stops accepting and waits for connections to drain.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	_ = s.ln.Close()

	// Close active client connections so handlers blocked in io.Copy return.
	s.mu.Lock()
	active := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		active = append(active, c)
	}
	s.mu.Unlock()
	for _, c := range active {
		_ = c.Close()
	}

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		// A wedged handler must not block shutdown forever.
	}
	return nil
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		atomic.AddInt64(&s.accepted, 1)
		atomic.AddInt64(&s.active, 1)
		s.mu.Lock()
		if s.conns == nil {
			s.conns = map[net.Conn]struct{}{}
		}
		s.conns[conn] = struct{}{}
		s.mu.Unlock()
		s.wg.Add(1)
		go func(c net.Conn) {
			defer s.wg.Done()
			defer atomic.AddInt64(&s.active, -1)
			defer func() {
				s.mu.Lock()
				delete(s.conns, c)
				s.mu.Unlock()
			}()
			defer c.Close()
			if err := s.handle(c); err != nil {
				atomic.AddInt64(&s.failed, 1)
				s.logf("inbound error from %s: %v", c.RemoteAddr(), err)
			}
		}(conn)
	}
}

// handle sniffs the first byte to pick SOCKS5 or HTTP.
func (s *Server) handle(conn net.Conn) error {
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	br := bufio.NewReaderSize(conn, 16*1024)
	first, err := br.Peek(1)
	if err != nil {
		return err
	}
	if first[0] == 0x05 {
		return s.handleSOCKS5(conn, br)
	}
	return s.handleHTTP(conn, br)
}

func (s *Server) handleSOCKS5(conn net.Conn, br *bufio.Reader) error {
	head := make([]byte, 2)
	if _, err := io.ReadFull(br, head); err != nil {
		return err
	}
	if head[0] != 0x05 {
		return errors.New("socks5: bad version")
	}
	methods := make([]byte, int(head[1]))
	if _, err := io.ReadFull(br, methods); err != nil {
		return err
	}
	wantAuth := s.cfg.Username != ""
	if wantAuth {
		if !containsByte(methods, 0x02) {
			_, _ = conn.Write([]byte{0x05, 0xFF})
			return errors.New("socks5: client does not support auth")
		}
		if _, err := conn.Write([]byte{0x05, 0x02}); err != nil {
			return err
		}
		if err := s.socks5Auth(conn, br); err != nil {
			return err
		}
	} else {
		if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
			return err
		}
	}

	reqHead := make([]byte, 4)
	if _, err := io.ReadFull(br, reqHead); err != nil {
		return err
	}
	if reqHead[0] != 0x05 {
		return errors.New("socks5: bad request version")
	}
	if reqHead[1] != 0x01 {
		_, _ = conn.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return errors.New("socks5: only CONNECT is supported")
	}
	target, err := readSOCKSAddr(br, reqHead[3])
	if err != nil {
		return err
	}
	upstream, err := s.dialer.Dial(context.Background(), target)
	if err != nil {
		_, _ = conn.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return err
	}
	defer upstream.Close()
	if _, err := conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Time{})
	s.pipe(conn, br, upstream)
	return nil
}

func (s *Server) socks5Auth(conn net.Conn, br *bufio.Reader) error {
	head := make([]byte, 2)
	if _, err := io.ReadFull(br, head); err != nil {
		return err
	}
	if head[0] != 0x01 {
		return errors.New("socks5: bad auth version")
	}
	user := make([]byte, int(head[1]))
	if _, err := io.ReadFull(br, user); err != nil {
		return err
	}
	plen, err := br.ReadByte()
	if err != nil {
		return err
	}
	pass := make([]byte, int(plen))
	if _, err := io.ReadFull(br, pass); err != nil {
		return err
	}
	if string(user) != s.cfg.Username || string(pass) != s.cfg.Password {
		_, _ = conn.Write([]byte{0x01, 0x01})
		return errors.New("socks5: auth failed")
	}
	_, err = conn.Write([]byte{0x01, 0x00})
	return err
}

func containsByte(list []byte, b byte) bool {
	for _, v := range list {
		if v == b {
			return true
		}
	}
	return false
}

func readSOCKSAddr(br *bufio.Reader, atyp byte) (string, error) {
	var host string
	switch atyp {
	case 0x01:
		buf := make([]byte, 4)
		if _, err := io.ReadFull(br, buf); err != nil {
			return "", err
		}
		host = net.IP(buf).String()
	case 0x04:
		buf := make([]byte, 16)
		if _, err := io.ReadFull(br, buf); err != nil {
			return "", err
		}
		host = net.IP(buf).String()
	case 0x03:
		l, err := br.ReadByte()
		if err != nil {
			return "", err
		}
		buf := make([]byte, int(l))
		if _, err := io.ReadFull(br, buf); err != nil {
			return "", err
		}
		host = string(buf)
	default:
		return "", fmt.Errorf("socks5: bad address type %d", atyp)
	}
	var p [2]byte
	if _, err := io.ReadFull(br, p[:]); err != nil {
		return "", err
	}
	port := int(p[0])<<8 | int(p[1])
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

func (s *Server) handleHTTP(conn net.Conn, br *bufio.Reader) error {
	req, err := readHTTPRequestHead(br)
	if err != nil {
		return err
	}
	if s.cfg.Username != "" && !s.checkProxyAuth(req.headers) {
		_, _ = conn.Write([]byte("HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic realm=\"WailsTools\"\r\nContent-Length: 0\r\n\r\n"))
		return errors.New("http: proxy auth required")
	}
	if strings.EqualFold(req.method, "CONNECT") {
		upstream, err := s.dialer.Dial(context.Background(), req.target)
		if err != nil {
			_, _ = conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n"))
			return err
		}
		defer upstream.Close()
		if _, err := conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
			return err
		}
		_ = conn.SetDeadline(time.Time{})
		s.pipe(conn, br, upstream)
		return nil
	}

	// Plain HTTP: forward the request to the origin through the node.
	host := req.headers["host"]
	if host == "" {
		return errors.New("http: missing Host header")
	}
	target := host
	if !strings.Contains(target, ":") {
		target += ":80"
	}
	upstream, err := s.dialer.Dial(context.Background(), target)
	if err != nil {
		_, _ = conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n"))
		return err
	}
	defer upstream.Close()

	var b strings.Builder
	fmt.Fprintf(&b, "%s %s HTTP/1.1\r\n", req.method, req.path)
	for k, v := range req.headers {
		if k == "proxy-authorization" || k == "proxy-connection" {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\r\n", canonicalHeader(k), v)
	}
	b.WriteString("\r\n")
	if _, err := upstream.Write([]byte(b.String())); err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Time{})
	s.pipe(conn, br, upstream)
	return nil
}

func (s *Server) checkProxyAuth(headers map[string]string) bool {
	raw := headers["proxy-authorization"]
	if raw == "" {
		return false
	}
	const prefix = "basic "
	if !strings.HasPrefix(strings.ToLower(raw), prefix) {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw[len(prefix):]))
	if err != nil {
		return false
	}
	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return false
	}
	return parts[0] == s.cfg.Username && parts[1] == s.cfg.Password
}

type httpHead struct {
	method  string
	target  string
	path    string
	headers map[string]string
}

func readHTTPRequestHead(br *bufio.Reader) (httpHead, error) {
	var head httpHead
	line, err := br.ReadString('\n')
	if err != nil {
		return head, err
	}
	parts := strings.Fields(strings.TrimSpace(line))
	if len(parts) < 3 {
		return head, errors.New("http: malformed request line")
	}
	head.method = parts[0]
	head.target = parts[1]
	head.path = parts[1]
	head.headers = map[string]string{}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return head, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		idx := strings.Index(line, ":")
		if idx <= 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:idx]))
		head.headers[key] = strings.TrimSpace(line[idx+1:])
	}
	// Absolute-form targets (proxy requests) become origin-form paths.
	if u := head.path; strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		if idx := strings.Index(u[8:], "/"); idx >= 0 {
			head.path = u[8+idx:]
		} else {
			head.path = "/"
		}
	}
	return head, nil
}

func canonicalHeader(k string) string {
	if k == "" {
		return k
	}
	return strings.ToUpper(k[:1]) + k[1:]
}

// pipe copies bytes both ways, honouring half-closes.
func (s *Server) pipe(client net.Conn, clientReader io.Reader, upstream net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		n, _ := io.Copy(upstream, clientReader)
		atomic.AddInt64(&s.bytesIn, n)
		closeWrite(upstream)
	}()
	go func() {
		defer wg.Done()
		n, _ := io.Copy(client, upstream)
		atomic.AddInt64(&s.bytesOut, n)
		closeWrite(client)
	}()
	wg.Wait()
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}
