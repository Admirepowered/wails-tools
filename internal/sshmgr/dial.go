package sshmgr

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"wailstools/internal/proxyx"
	"wailstools/internal/secretstore"
	"wailstools/internal/settings"
)

// Relayer can open a raw TCP stream to target through the WailsTools server.
type Relayer interface {
	DialTCP(ctx context.Context, target string) (net.Conn, error)
	Available() bool
	Describe() string
}

// ProxyResolver returns the effective proxy rule for a scope.
type ProxyResolver func(scope string) settings.ProxyRule

// DialOptions tunes a connection attempt.
type DialOptions struct {
	// Passphrase overrides the stored key passphrase.
	Passphrase string
	// Password overrides the stored password.
	Password string
	// InsecureHostKey accepts unknown or changed host keys (still recorded).
	InsecureHostKey bool
	// Timeout bounds the whole handshake chain.
	Timeout time.Duration
}

// Client is an established SSH connection.
type Client struct {
	ID        string      `json:"id"`
	HostID    string      `json:"hostId"`
	Name      string      `json:"name"`
	Address   string      `json:"address"`
	User      string      `json:"user"`
	Transport string      `json:"transport"`
	Chain     []string    `json:"chain,omitempty"`
	OpenedAt  time.Time   `json:"openedAt"`
	SSH       *ssh.Client `json:"-"`
}

// Manager owns saved hosts plus live SSH connections.
type Manager struct {
	Store    *Store
	ProxyFor ProxyResolver
	Relay    Relayer

	mu      sync.Mutex
	clients map[string]*Client
	seq     int
	dir     string
}

// NewManager wires a manager over a store.
func NewManager(store *Store, proxyFor ProxyResolver, relay Relayer, dir string) *Manager {
	if proxyFor == nil {
		proxyFor = func(string) settings.ProxyRule { return settings.ProxyRule{Mode: "off"} }
	}
	return &Manager{Store: store, ProxyFor: proxyFor, Relay: relay, clients: map[string]*Client{}, dir: dir}
}

// Connect establishes (and pools) a connection to hostID.
func (m *Manager) Connect(ctx context.Context, hostID string, opts DialOptions) (*Client, error) {
	host, ok := m.Store.GetHost(hostID)
	if !ok {
		return nil, fmt.Errorf("ssh host %q not found", hostID)
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	type result struct {
		client *Client
		err    error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := m.connect(ctx, host, opts)
		ch <- result{c, err}
	}()
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("connect %s: %w", host.Addr(), ctx.Err())
	case r := <-ch:
		return r.client, r.err
	}
}

func (m *Manager) connect(ctx context.Context, host Host, opts DialOptions) (*Client, error) {
	jumps := make([]Host, 0, len(host.JumpIDs))
	for _, id := range host.JumpIDs {
		jh, ok := m.Store.GetHost(id)
		if !ok {
			return nil, fmt.Errorf("jump host %q not found", id)
		}
		jumps = append(jumps, jh)
	}
	chain := append(jumps, host)

	var (
		sshClient *ssh.Client
		transport string
		descs     []string
	)
	for i, h := range chain {
		var raw net.Conn
		var err error
		var desc string
		if i == 0 {
			raw, desc, err = m.dialEndpoint(ctx, h, opts)
			if err != nil {
				return nil, fmt.Errorf("dial %s: %w", h.Addr(), err)
			}
			transport = desc
		} else {
			raw, err = sshClient.Dial("tcp", h.Addr())
			if err != nil {
				_ = sshClient.Close()
				return nil, fmt.Errorf("dial %s through jump host: %w", h.Addr(), err)
			}
			transport = "jump"
		}
		descs = append(descs, fmt.Sprintf("%s(%s)", h.Addr(), desc))

		cfg, err := m.clientConfig(h, opts)
		if err != nil {
			_ = raw.Close()
			if sshClient != nil {
				_ = sshClient.Close()
			}
			return nil, err
		}
		_ = raw.SetDeadline(time.Now().Add(opts.Timeout))
		if tc, ok := raw.(*net.TCPConn); ok {
			_ = tc.SetKeepAlive(true)
		}
		conn, chans, reqs, err := ssh.NewClientConn(raw, h.Addr(), cfg)
		if err != nil {
			_ = raw.Close()
			if sshClient != nil {
				_ = sshClient.Close()
			}
			return nil, fmt.Errorf("ssh handshake with %s failed: %w", h.Addr(), err)
		}
		_ = raw.SetDeadline(time.Time{})
		sshClient = ssh.NewClient(conn, chans, reqs)
	}

	m.mu.Lock()
	m.seq++
	id := fmt.Sprintf("sess-%d", m.seq)
	c := &Client{
		ID:        id,
		HostID:    host.ID,
		Name:      host.Name,
		Address:   host.Addr(),
		User:      host.User,
		Transport: transport,
		Chain:     descs,
		OpenedAt:  time.Now(),
		SSH:       sshClient,
	}
	m.clients[id] = c
	m.mu.Unlock()

	m.Store.MarkHostUsed(host.ID, transport)
	return c, nil
}

// dialEndpoint opens the raw socket for the first hop.
func (m *Manager) dialEndpoint(ctx context.Context, h Host, opts DialOptions) (net.Conn, string, error) {
	useRelay := h.Route == RouteRelay
	target := h.Addr()
	if useRelay {
		if strings.TrimSpace(h.RelayTarget) != "" {
			target = h.RelayTarget
		}
		if m.Relay == nil || !m.Relay.Available() {
			return nil, "", errors.New("relay route requires a connected sync server (connect it in Settings → Server)")
		}
		conn, err := m.Relay.DialTCP(ctx, target)
		if err != nil {
			return nil, "", fmt.Errorf("relay via server: %w", err)
		}
		return conn, "relay:" + target, nil
	}

	rule := m.ProxyFor(settings.ScopeSSH)
	dialer, err := proxyx.New(rule)
	if err != nil && !errors.Is(err, proxyx.ErrNoProxy) {
		return nil, "", err
	}
	if dialer == nil {
		var nd net.Dialer
		conn, err := nd.DialContext(ctx, "tcp", target)
		if err != nil {
			return nil, "", err
		}
		return conn, "direct", nil
	}
	if dialer.Bypassed(target) {
		var nd net.Dialer
		conn, err := nd.DialContext(ctx, "tcp", target)
		if err != nil {
			return nil, "", err
		}
		return conn, "direct(bypass)", nil
	}
	conn, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		return nil, "", err
	}
	return conn, "proxy:" + rule.Mode, nil
}

func (m *Manager) clientConfig(h Host, opts DialOptions) (*ssh.ClientConfig, error) {
	auths := make([]ssh.AuthMethod, 0, 3)
	switch h.AuthMode {
	case AuthPassword:
		pass := opts.Password
		if pass == "" {
			p, err := m.Store.HostPassword(h.ID)
			if err != nil {
				return nil, err
			}
			pass = p
		}
		if pass == "" {
			return nil, errors.New("password auth selected but no password stored")
		}
		auths = append(auths, ssh.Password(pass))
	case AuthKeyboard:
		pass := opts.Password
		if pass == "" {
			p, err := m.Store.HostPassword(h.ID)
			if err != nil {
				return nil, err
			}
			pass = p
		}
		auths = append(auths, ssh.KeyboardInteractive(func(name, instruction string, questions []string, echos []bool) ([]string, error) {
			answers := make([]string, len(questions))
			for i := range answers {
				answers[i] = pass
			}
			return answers, nil
		}))
	case AuthAgent:
		if signer := agentSigner(); signer != nil {
			auths = append(auths, ssh.PublicKeys(signer))
		}
		if len(auths) == 0 {
			return nil, errors.New("no ssh agent available")
		}
	case AuthNone:
	default: // AuthKey
		if h.KeyID == "" {
			return nil, errors.New("key auth selected but no key assigned")
		}
		signer, err := m.Store.signer(h.KeyID, opts.Passphrase)
		if err != nil {
			return nil, err
		}
		auths = append(auths, ssh.PublicKeys(signer))
		// A password may still be useful as a fallback for servers that ask
		// for interactive confirmation.
		if p, err := m.Store.HostPassword(h.ID); err == nil && p != "" {
			auths = append(auths, ssh.Password(p))
		}
	}
	if len(auths) == 0 {
		auths = append(auths, ssh.Password(""))
	}
	user := h.User
	if user == "" {
		return nil, errors.New("ssh user is required")
	}
	return &ssh.ClientConfig{
		User:            user,
		Auth:            auths,
		HostKeyCallback: m.hostKeyCallback(h, opts.InsecureHostKey),
		Timeout:         opts.Timeout,
	}, nil
}

func (m *Manager) hostKeyCallback(h Host, insecure bool) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		fp := ssh.FingerprintSHA256(key)
		pinned := strings.TrimSpace(h.HostKeyFingerprint)
		if pinned == "" {
			pinned = m.Store.KnownHostFingerprint(h.Addr())
		}
		switch {
		case pinned == "":
			_ = m.Store.SetKnownHostFingerprint(h.Addr(), fp)
			return nil
		case normalizeFP(pinned) == normalizeFP(fp):
			return nil
		case insecure:
			_ = m.Store.SetKnownHostFingerprint(h.Addr(), fp)
			return nil
		default:
			return fmt.Errorf("host key mismatch for %s: pinned %s, presented %s", hostname, pinned, fp)
		}
	}
}

func normalizeFP(fp string) string {
	return strings.ToLower(strings.TrimSpace(fp))
}

// Sessions lists live connections.
func (m *Manager) Sessions() []Client {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Client, 0, len(m.clients))
	for _, c := range m.clients {
		cc := *c
		cc.SSH = nil
		out = append(out, cc)
	}
	return out
}

// Client returns a live session by id.
func (m *Manager) Client(id string) (*Client, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.clients[id]
	return c, ok
}

// Disconnect closes a session.
func (m *Manager) Disconnect(id string) error {
	m.mu.Lock()
	c, ok := m.clients[id]
	if ok {
		delete(m.clients, id)
	}
	m.mu.Unlock()
	if !ok {
		return errors.New("session not found")
	}
	if c.SSH != nil {
		return c.SSH.Close()
	}
	return nil
}

// DisconnectAll closes every session.
func (m *Manager) DisconnectAll() {
	m.mu.Lock()
	all := m.clients
	m.clients = map[string]*Client{}
	m.mu.Unlock()
	for _, c := range all {
		if c.SSH != nil {
			_ = c.SSH.Close()
		}
	}
}

// TestHost verifies credentials and reachability without pooling a session.
func (m *Manager) TestHost(ctx context.Context, hostID string, opts DialOptions) (map[string]any, error) {
	start := time.Now()
	c, err := m.Connect(ctx, hostID, opts)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error(), "elapsedMs": time.Since(start).Milliseconds()}, err
	}
	var uname string
	if sess, err := c.SSH.NewSession(); err == nil {
		if out, err := sess.CombinedOutput("uname -a || ver"); err == nil {
			uname = strings.TrimSpace(string(out))
		}
		_ = sess.Close()
	}
	_ = m.Disconnect(c.ID)
	return map[string]any{
		"ok":        true,
		"transport": c.Transport,
		"chain":     c.Chain,
		"elapsedMs": time.Since(start).Milliseconds(),
		"uname":     uname,
	}, nil
}

// RunCommand executes one command on a session and returns its output.
func (m *Manager) RunCommand(ctx context.Context, sessionID, command string, timeout time.Duration) (map[string]any, error) {
	c, ok := m.Client(sessionID)
	if !ok || c.SSH == nil {
		return nil, errors.New("ssh session not found")
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	type result struct {
		stdout, stderr []byte
		err            error
	}
	ch := make(chan result, 1)
	go func() {
		sess, err := c.SSH.NewSession()
		if err != nil {
			ch <- result{err: err}
			return
		}
		defer sess.Close()
		var stdout, stderr strings.Builder
		sess.Stdout = &stdout
		sess.Stderr = &stderr
		err = sess.Run(command)
		ch <- result{stdout: []byte(stdout.String()), stderr: []byte(stderr.String()), err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(timeout):
		return nil, errors.New("command timed out")
	case r := <-ch:
		exitCode := 0
		if r.err != nil {
			var ee *ssh.ExitError
			if errors.As(r.err, &ee) {
				exitCode = ee.ExitStatus()
			} else {
				return nil, r.err
			}
		}
		return map[string]any{
			"stdout":   string(r.stdout),
			"stderr":   string(r.stderr),
			"exitCode": exitCode,
		}, nil
	}
}

var _ = secretstore.Seal
