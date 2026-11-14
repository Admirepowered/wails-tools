package proxysvc

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
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

// ProbeURL is the default reachability target.
const ProbeURL = "http://www.gstatic.com/generate_204"

// Probe measures whether a node can fetch ProbeURL, returning the latency.
func Probe(ctx context.Context, node miproxy.Node, rawURL string) (int64, error) {
	if rawURL == "" {
		rawURL = ProbeURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return 0, err
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		host += ":80"
	}
	path := u.RequestURI()
	if path == "" {
		path = "/"
	}
	d := miproxy.NewDialer(node, 15*time.Second)
	start := time.Now()
	conn, err := d.Dial(ctx, host)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUser-Agent: WailsTools/1.0\r\nAccept: */*\r\nConnection: close\r\n\r\n", path, u.Host)
	_ = conn.SetDeadline(time.Now().Add(12 * time.Second))
	if _, err := conn.Write([]byte(req)); err != nil {
		return 0, err
	}
	reader := bufio.NewReaderSize(conn, 4096)
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(statusLine)
	if len(fields) < 2 || !strings.HasPrefix(fields[0], "HTTP/") {
		return 0, errors.New("probe: unexpected response")
	}
	code := 0
	fmt.Sscanf(fields[1], "%d", &code)
	if code < 200 || code >= 400 {
		return 0, fmt.Errorf("probe: HTTP %d", code)
	}
	return time.Since(start).Milliseconds(), nil
}
