// Package netx provides the multiplexed session transport used between the
// WailsTools client and server. Sessions are carried either over QUIC (UDP,
// preferred) or over TLS+TCP with yamux multiplexing (fallback).
package netx

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/quic-go/quic-go"
)

// ALPN identifies the protocol during the TLS handshake.
const ALPN = "wailstools/1"

// Transport names reported to callers (and shown in the UI).
const (
	TransportUDP = "udp"
	TransportTCP = "tcp"
)

// Session is a multiplexed connection that can open and accept streams.
type Session interface {
	Open(ctx context.Context) (net.Conn, error)
	Accept(ctx context.Context) (net.Conn, error)
	Transport() string
	RemoteAddr() net.Addr
	Close() error
}

// DialConfig describes how to reach the server.
type DialConfig struct {
	Host             string
	Port             int
	TLS              *tls.Config
	HandshakeTimeout time.Duration
	PreferUDP        bool
	// DisableUDP forces the TCP fallback (useful for debugging and for
	// networks that drop UDP outright).
	DisableUDP bool
}

func (c DialConfig) addr() string {
	host := c.Host
	if host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, strconv.Itoa(c.Port))
}

// DialReport explains which transports were attempted.
type DialReport struct {
	Transport   string        `json:"transport"`
	Addr        string        `json:"addr"`
	UDPMs       int64         `json:"udpMs"`
	TCPMs       int64         `json:"tcpMs"`
	UDPError    string        `json:"udpError,omitempty"`
	TCPError    string        `json:"tcpError,omitempty"`
	Fingerprint string        `json:"fingerprint,omitempty"`
	Elapsed     time.Duration `json:"-"`
}

// Dial connects to the server, preferring QUIC over UDP and falling back to
// TLS over TCP. The returned report always describes what happened.
func Dial(ctx context.Context, cfg DialConfig) (Session, *DialReport, error) {
	if cfg.HandshakeTimeout <= 0 {
		cfg.HandshakeTimeout = 4 * time.Second
	}
	start := time.Now()
	report := &DialReport{Addr: cfg.addr()}
	var udpErr, tcpErr error

	if !cfg.DisableUDP {
		udpStart := time.Now()
		sess, err := dialQUIC(ctx, cfg)
		report.UDPMs = time.Since(udpStart).Milliseconds()
		if err == nil {
			report.Transport = TransportUDP
			report.Elapsed = time.Since(start)
			return sess, report, nil
		}
		udpErr = err
		report.UDPError = err.Error()
	}

	tcpStart := time.Now()
	sess, err := dialTCP(ctx, cfg)
	report.TCPMs = time.Since(tcpStart).Milliseconds()
	if err == nil {
		report.Transport = TransportTCP
		report.Elapsed = time.Since(start)
		return sess, report, nil
	}
	tcpErr = err
	report.TCPError = err.Error()
	report.Elapsed = time.Since(start)

	switch {
	case udpErr != nil && tcpErr != nil:
		return nil, report, fmt.Errorf("connect %s failed: udp: %v; tcp: %v", cfg.addr(), udpErr, tcpErr)
	case tcpErr != nil:
		return nil, report, fmt.Errorf("connect %s failed: %w", cfg.addr(), tcpErr)
	default:
		return nil, report, fmt.Errorf("connect %s failed: %w", cfg.addr(), udpErr)
	}
}

func dialQUIC(ctx context.Context, cfg DialConfig) (Session, error) {
	tlsConf := cfg.TLS
	if tlsConf == nil {
		tlsConf = &tls.Config{InsecureSkipVerify: true}
	} else {
		tlsConf = tlsConf.Clone()
	}
	if len(tlsConf.NextProtos) == 0 {
		tlsConf.NextProtos = []string{ALPN}
	}
	dctx, cancel := context.WithTimeout(ctx, cfg.HandshakeTimeout)
	defer cancel()
	conn, err := quic.DialAddr(dctx, cfg.addr(), tlsConf, quicConfig(false))
	if err != nil {
		return nil, err
	}
	return &quicSession{conn: conn}, nil
}

func dialTCP(ctx context.Context, cfg DialConfig) (Session, error) {
	dctx, cancel := context.WithTimeout(ctx, cfg.HandshakeTimeout)
	defer cancel()
	dialer := &net.Dialer{Timeout: cfg.HandshakeTimeout}
	raw, err := dialer.DialContext(dctx, "tcp", cfg.addr())
	if err != nil {
		return nil, err
	}
	tlsConf := cfg.TLS
	if tlsConf == nil {
		tlsConf = &tls.Config{InsecureSkipVerify: true}
	} else {
		tlsConf = tlsConf.Clone()
	}
	if len(tlsConf.NextProtos) == 0 {
		tlsConf.NextProtos = []string{ALPN}
	}
	tlsConn := tls.Client(raw, tlsConf)
	if err := tlsConn.HandshakeContext(dctx); err != nil {
		_ = raw.Close()
		return nil, err
	}
	mux, err := yamux.Client(tlsConn, yamuxConfig())
	if err != nil {
		_ = tlsConn.Close()
		return nil, err
	}
	return &yamuxSession{sess: mux}, nil
}

func quicConfig(server bool) *quic.Config {
	cfg := &quic.Config{
		MaxIdleTimeout:        90 * time.Second,
		KeepAlivePeriod:       20 * time.Second,
		HandshakeIdleTimeout:  8 * time.Second,
		MaxIncomingStreams:    512,
		MaxIncomingUniStreams: 16,
	}
	if server {
		cfg.Allow0RTT = true
	}
	return cfg
}

func yamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 20 * time.Second
	cfg.ConnectionWriteTimeout = 30 * time.Second
	cfg.MaxStreamWindowSize = 1 << 20
	// yamux requires exactly one of Logger/LogOutput; discard by default.
	cfg.LogOutput = io.Discard
	return cfg
}

// quicSession adapts a quic.Conn to Session.
type quicSession struct {
	conn *quic.Conn
}

func (s *quicSession) Open(ctx context.Context) (net.Conn, error) {
	st, err := s.conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	return &quicStreamConn{Stream: st, conn: s.conn}, nil
}

func (s *quicSession) Accept(ctx context.Context) (net.Conn, error) {
	st, err := s.conn.AcceptStream(ctx)
	if err != nil {
		return nil, err
	}
	return &quicStreamConn{Stream: st, conn: s.conn}, nil
}

func (s *quicSession) Transport() string    { return TransportUDP }
func (s *quicSession) RemoteAddr() net.Addr { return s.conn.RemoteAddr() }

func (s *quicSession) Close() error {
	return s.conn.CloseWithError(0, "bye")
}

// quicStreamConn gives a quic.Stream the net.Conn surface used across the app.
type quicStreamConn struct {
	*quic.Stream
	conn *quic.Conn
}

func (c *quicStreamConn) LocalAddr() net.Addr  { return c.conn.LocalAddr() }
func (c *quicStreamConn) RemoteAddr() net.Addr { return c.conn.RemoteAddr() }

func (c *quicStreamConn) Close() error {
	c.Stream.CancelRead(0)
	return c.Stream.Close()
}

// yamuxSession adapts a yamux.Session to Session.
type yamuxSession struct {
	sess *yamux.Session
}

func (s *yamuxSession) Open(ctx context.Context) (net.Conn, error) {
	type result struct {
		conn net.Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := s.sess.OpenStream()
		ch <- result{c, err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		return r.conn, r.err
	}
}

func (s *yamuxSession) Accept(ctx context.Context) (net.Conn, error) {
	type result struct {
		conn net.Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := s.sess.AcceptStream()
		ch <- result{c, err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		return r.conn, r.err
	}
}

func (s *yamuxSession) Transport() string    { return TransportTCP }
func (s *yamuxSession) RemoteAddr() net.Addr { return s.sess.RemoteAddr() }
func (s *yamuxSession) Close() error         { return s.sess.Close() }

// ListenConfig configures a dual-stack server listener.
type ListenConfig struct {
	TCPAddr   string
	UDPAddr   string
	TLS       *tls.Config
	EnableUDP bool
	EnableTCP bool
}

// Listener accepts sessions from every enabled transport.
type Listener struct {
	cfg      ListenConfig
	quicLis  *quic.Listener
	tcpLis   net.Listener
	incoming chan acceptResult
	closed   chan struct{}
	closeOne sync.Once
	wg       sync.WaitGroup
}

type acceptResult struct {
	sess Session
	err  error
}

// ErrListenerClosed is returned once the listener has been closed.
var ErrListenerClosed = errors.New("netx: listener closed")

// Listen starts the configured transports.
func Listen(ctx context.Context, cfg ListenConfig) (*Listener, error) {
	l := &Listener{cfg: cfg, incoming: make(chan acceptResult, 32), closed: make(chan struct{})}
	if cfg.EnableUDP {
		udpAddr := cfg.UDPAddr
		if udpAddr == "" {
			udpAddr = cfg.TCPAddr
		}
		ql, err := quic.ListenAddr(udpAddr, cfg.TLS, quicConfig(true))
		if err != nil {
			return nil, fmt.Errorf("listen udp %s: %w", udpAddr, err)
		}
		l.quicLis = ql
		l.wg.Add(1)
		go l.acceptQUIC(ctx)
	}
	if cfg.EnableTCP {
		tlsConf := cfg.TLS.Clone()
		if len(tlsConf.NextProtos) == 0 {
			tlsConf.NextProtos = []string{ALPN}
		}
		tl, err := tls.Listen("tcp", cfg.TCPAddr, tlsConf)
		if err != nil {
			if l.quicLis != nil {
				_ = l.quicLis.Close()
			}
			return nil, fmt.Errorf("listen tcp %s: %w", cfg.TCPAddr, err)
		}
		l.tcpLis = tl
		l.wg.Add(1)
		go l.acceptTCP(ctx)
	}
	if l.quicLis == nil && l.tcpLis == nil {
		return nil, errors.New("netx: no transport enabled")
	}
	return l, nil
}

func (l *Listener) acceptQUIC(ctx context.Context) {
	defer l.wg.Done()
	for {
		conn, err := l.quicLis.Accept(ctx)
		if err != nil {
			select {
			case <-l.closed:
				return
			default:
			}
			if ctx.Err() != nil {
				return
			}
			l.push(acceptResult{err: err})
			return
		}
		l.push(acceptResult{sess: &quicSession{conn: conn}})
	}
}

func (l *Listener) acceptTCP(ctx context.Context) {
	defer l.wg.Done()
	for {
		conn, err := l.tcpLis.Accept()
		if err != nil {
			select {
			case <-l.closed:
				return
			default:
			}
			l.push(acceptResult{err: err})
			return
		}
		go func(c net.Conn) {
			mux, err := yamux.Server(c, yamuxConfig())
			if err != nil {
				_ = c.Close()
				return
			}
			l.push(acceptResult{sess: &yamuxSession{sess: mux}})
		}(conn)
	}
}

func (l *Listener) push(r acceptResult) {
	select {
	case l.incoming <- r:
	case <-l.closed:
		if r.sess != nil {
			_ = r.sess.Close()
		}
	}
}

// Accept waits for the next session.
func (l *Listener) Accept(ctx context.Context) (Session, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-l.closed:
		return nil, ErrListenerClosed
	case r := <-l.incoming:
		if r.err != nil {
			return nil, r.err
		}
		return r.sess, nil
	}
}

// UDPAddr reports the bound QUIC address.
func (l *Listener) UDPAddr() string {
	if l.quicLis == nil {
		return ""
	}
	return l.quicLis.Addr().String()
}

// TCPAddr reports the bound TCP address.
func (l *Listener) TCPAddr() string {
	if l.tcpLis == nil {
		return ""
	}
	return l.tcpLis.Addr().String()
}

// Close shuts down every transport.
func (l *Listener) Close() error {
	l.closeOne.Do(func() {
		close(l.closed)
		if l.quicLis != nil {
			_ = l.quicLis.Close()
		}
		if l.tcpLis != nil {
			_ = l.tcpLis.Close()
		}
	})
	l.wg.Wait()
	return nil
}
