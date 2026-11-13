package proxysvc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"wailstools/internal/miproxy"
)

// StartOptions configures the local proxy listener.
type StartOptions struct {
	// External binds 0.0.0.0 so other machines can use the proxy.
	External bool
	// Username/Password protect the inbound when External is set.
	Username string
	Password string
	// Port is the preferred listen port (0 = random).
	Port int
}

// Status describes the running local proxy.
type Status struct {
	Running  bool          `json:"running"`
	Node     *miproxy.Node `json:"node,omitempty"`
	Addr     string        `json:"addr,omitempty"`
	External bool          `json:"external"`
	Auth     bool          `json:"auth"`
	Stats    miproxy.Stats `json:"stats"`
	Since    string        `json:"since,omitempty"`
	Error    string        `json:"error,omitempty"`
}

// Service owns the single active local proxy instance.
type Service struct {
	mu    sync.Mutex
	srv   *miproxy.Server
	node  miproxy.Node
	opts  StartOptions
	since time.Time
	logf  func(string, ...any)
}

// New creates the service.
func New(logf func(string, ...any)) *Service {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Service{logf: logf}
}

// Start launches the local proxy for a node.
func (s *Service) Start(node miproxy.Node, opts StartOptions) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv != nil {
		_ = s.srv.Close()
		s.srv = nil
	}
	listen := "127.0.0.1:0"
	if opts.External {
		port := opts.Port
		if port <= 0 {
			port = 7890
		}
		listen = net.JoinHostPort("0.0.0.0", strconv.Itoa(port))
	} else if opts.Port > 0 {
		listen = net.JoinHostPort("127.0.0.1", strconv.Itoa(opts.Port))
	}
	srv, err := miproxy.StartServer(miproxy.ServerConfig{
		Node:       node,
		ListenAddr: listen,
		Username:   opts.Username,
		Password:   opts.Password,
		Logf:       s.logf,
	})
	if err != nil {
		return Status{Error: err.Error()}, err
	}
	s.srv = srv
	s.node = node
	s.opts = opts
	s.since = time.Now()
	s.logf("proxy started on %s for %s", srv.Addr(), node.Label())
	return s.statusLocked(), nil
}

// Stop tears the local proxy down.
func (s *Service) Stop() error {
	s.mu.Lock()
	srv := s.srv
	s.srv = nil
	s.mu.Unlock()
	if srv != nil {
		return srv.Close()
	}
	return nil
}

// Status snapshots the runtime state.
func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked()
}

func (s *Service) statusLocked() Status {
	st := Status{External: s.opts.External, Auth: s.opts.Username != ""}
	if s.srv == nil {
		return st
	}
	node := s.node
	st.Running = true
	st.Node = &node
	st.Addr = s.srv.Addr()
	st.Stats = s.srv.Stats()
	st.Since = s.since.Format(time.RFC3339)
	return st
}

// Running reports whether a proxy is active.
func (s *Service) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.srv != nil
}

// Probe starts a temporary proxy for the node and measures reachability.
func Probe(ctx context.Context, node miproxy.Node, probeURL string) (int64, error) {
	if probeURL == "" {
		probeURL = "www.gstatic.com:80"
	}
	d := miproxy.NewDialer(node, 15*time.Second)
	start := time.Now()
	conn, err := d.Dial(ctx, probeURL)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	host, _, _ := net.SplitHostPort(probeURL)
	req := fmt.Sprintf("HEAD /generate_204 HTTP/1.1\r\nHost: %s\r\nUser-Agent: WailsTools/1.0\r\nConnection: close\r\n\r\n", host)
	_ = conn.SetDeadline(time.Now().Add(12 * time.Second))
	if _, err := conn.Write([]byte(req)); err != nil {
		return 0, err
	}
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		return 0, err
	}
	if n < 12 || string(buf[:5]) != "HTTP/" {
		return 0, errors.New("probe: unexpected response")
	}
	return time.Since(start).Milliseconds(), nil
}
