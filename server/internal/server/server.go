// Package server wires the wire protocol, the API and the dual-stack
// (QUIC/UDP first, TLS/TCP fallback) listener together.
package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"wailstools/internal/netx"
	"wailstools/internal/wire"
	"wailstools/server/internal/api"
	"wailstools/server/internal/store"
)

// Config configures the server.
type Config struct {
	DataDir           string
	TCPAddr           string
	UDPAddr           string
	EnableTCP         bool
	EnableUDP         bool
	AllowRegistration bool
	RelayEnabled      bool
	RelayBlockedCIDRs []string
	DefaultQuotaBytes int64
	TokenTTL          time.Duration
	Logger            *log.Logger
}

// Server is a running instance.
type Server struct {
	cfg     Config
	store   *store.DB
	api     *api.API
	lis     *netx.Listener
	logger  *log.Logger
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	started time.Time
}

// New validates the configuration and opens storage.
func New(cfg Config) (*Server, error) {
	if cfg.DataDir == "" {
		cfg.DataDir = "data"
	}
	if !cfg.EnableTCP && !cfg.EnableUDP {
		cfg.EnableUDP = true
		cfg.EnableTCP = true
	}
	if cfg.TCPAddr == "" {
		cfg.TCPAddr = ":8642"
	}
	if cfg.UDPAddr == "" {
		cfg.UDPAddr = cfg.TCPAddr
	}
	if cfg.Logger == nil {
		cfg.Logger = log.New(os.Stdout, "[wt-server] ", log.LstdFlags)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, err
	}
	db, err := store.Open(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	secret, err := loadSecret(filepath.Join(cfg.DataDir, "server.secret"))
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	a := api.New(db, secret, api.Options{
		AllowRegistration: cfg.AllowRegistration,
		DefaultQuota:      cfg.DefaultQuotaBytes,
		TokenTTL:          cfg.TokenTTL,
		RelayEnabled:      cfg.RelayEnabled,
		RelayBlockedCIDRs: cfg.RelayBlockedCIDRs,
		Logf:              cfg.Logger.Printf,
	})
	return &Server{cfg: cfg, store: db, api: a, logger: cfg.Logger}, nil
}

func loadSecret(path string) ([]byte, error) {
	if data, err := os.ReadFile(path); err == nil && len(data) >= 32 {
		return data, nil
	}
	buf := make([]byte, 48)
	if _, err := readRand(buf); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		return nil, err
	}
	return buf, nil
}

// Start begins listening.
func (s *Server) Start(ctx context.Context) error {
	cert, err := netx.LoadOrCreateCert(filepath.Join(s.cfg.DataDir, "tls"), "server", []string{"127.0.0.1"})
	if err != nil {
		return fmt.Errorf("load tls cert: %w", err)
	}
	tlsConf := netx.ServerTLS(cert)
	lis, err := netx.Listen(ctx, netx.ListenConfig{
		TCPAddr:   s.cfg.TCPAddr,
		UDPAddr:   s.cfg.UDPAddr,
		TLS:       tlsConf,
		EnableUDP: s.cfg.EnableUDP,
		EnableTCP: s.cfg.EnableTCP,
	})
	if err != nil {
		return err
	}
	s.lis = lis
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.started = time.Now()

	if udp := lis.UDPAddr(); udp != "" {
		s.logger.Printf("listening QUIC/UDP on %s (preferred)", udp)
	}
	if tcp := lis.TCPAddr(); tcp != "" {
		s.logger.Printf("listening TLS/TCP on %s (fallback)", tcp)
	}
	s.logger.Printf("data dir: %s", s.cfg.DataDir)
	if fp := netx.Fingerprint(cert.Leaf); fp != "" {
		s.logger.Printf("certificate fingerprint: %s", fp)
	}

	s.wg.Add(1)
	go s.acceptLoop()
	return nil
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		sess, err := s.lis.Accept(s.ctx)
		if err != nil {
			if errors.Is(err, netx.ErrListenerClosed) || s.ctx.Err() != nil {
				return
			}
			s.logger.Printf("accept: %v", err)
			continue
		}
		s.logger.Printf("session established transport=%s remote=%s", sess.Transport(), sess.RemoteAddr())
		s.wg.Add(1)
		go func(sess netx.Session) {
			defer s.wg.Done()
			s.serveSession(sess)
		}(sess)
	}
}

func (s *Server) serveSession(sess netx.Session) {
	defer sess.Close()
	var wg sync.WaitGroup
	for {
		stream, err := sess.Accept(context.Background())
		if err != nil {
			wg.Wait()
			return
		}
		wg.Add(1)
		go func(conn net.Conn) {
			defer wg.Done()
			defer conn.Close()
			err := wire.Serve(conn, s.api.Authenticate, s.api)
			if err != nil && !errors.Is(err, os.ErrClosed) && s.verbose(err) {
				s.logger.Printf("stream closed transport=%s: %v", sess.Transport(), err)
			}
		}(stream)
	}
}

func (s *Server) verbose(err error) bool {
	msg := err.Error()
	switch {
	case msg == "EOF", msg == "":
		return false
	}
	return !errors.Is(err, net.ErrClosed)
}

// Shutdown stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.cancel != nil {
		s.cancel()
	}
	if s.lis != nil {
		_ = s.lis.Close()
	}
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.store.Close()
}

// Addrs reports the bound addresses.
func (s *Server) Addrs() (udp, tcp string) {
	if s.lis == nil {
		return "", ""
	}
	return s.lis.UDPAddr(), s.lis.TCPAddr()
}

// Uptime returns how long the server has been running.
func (s *Server) Uptime() time.Duration {
	if s.started.IsZero() {
		return 0
	}
	return time.Since(s.started)
}
