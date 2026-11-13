package sshmgr

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"wailstools/internal/settings"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "ssh")
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return s
}

func TestGenerateKeys(t *testing.T) {
	store := newStore(t)
	cases := []struct {
		typ  string
		bits int
		want string
	}{
		{"rsa", 2048, "rsa"},
		{"rsa", 3072, "rsa"},
		{"ed25519", 0, "ed25519"},
		{"ecdsa", 256, "ecdsa"},
	}
	for _, tc := range cases {
		key, err := store.GenerateKey(GenerateOptions{Name: tc.typ, Type: tc.typ, Bits: tc.bits, Comment: "test"})
		if err != nil {
			t.Fatalf("generate %s/%d: %v", tc.typ, tc.bits, err)
		}
		if key.Type != tc.want {
			t.Fatalf("type %q, want %q", key.Type, tc.want)
		}
		wantPrefix := map[string]string{"rsa": "ssh-rsa ", "ed25519": "ssh-ed25519 ", "ecdsa": "ecdsa-sha2-"}[tc.want]
		if !strings.HasPrefix(key.PublicKey, wantPrefix) {
			t.Fatalf("unexpected public key prefix for %s: %q", tc.want, key.PublicKey)
		}
		if !strings.HasPrefix(key.Fingerprint, "SHA256:") {
			t.Fatalf("unexpected fingerprint: %q", key.Fingerprint)
		}
		if key.PrivateKey != "" {
			t.Fatal("private key must not be returned to the UI layer")
		}
		// The stored material must actually sign.
		priv, _, err := store.keyMaterial(key.ID)
		if err != nil {
			t.Fatalf("key material: %v", err)
		}
		signer, err := ssh.ParsePrivateKey([]byte(priv))
		if err != nil {
			t.Fatalf("parse private key: %v", err)
		}
		if signer.PublicKey().Type() == "" {
			t.Fatal("signer has no type")
		}
	}
	if len(store.ListKeys()) != len(cases) {
		t.Fatalf("expected %d keys, got %d", len(cases), len(store.ListKeys()))
	}
}

func TestGenerateRSARejectsSmallKeys(t *testing.T) {
	store := newStore(t)
	if _, err := store.GenerateKey(GenerateOptions{Type: "rsa", Bits: 1024}); err == nil {
		t.Fatal("expected rejection of 1024-bit RSA keys")
	}
}

func TestImportKeyWithPassphrase(t *testing.T) {
	store := newStore(t)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "imported", []byte("secret-pass"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	pemBytes := string(pem.EncodeToMemory(block))

	// Wrong passphrase must fail.
	if _, err := store.ImportKey(ImportOptions{Name: "bad", PrivateKey: pemBytes, Passphrase: "nope"}); err == nil {
		t.Fatal("expected wrong passphrase to fail")
	}
	key, err := store.ImportKey(ImportOptions{Name: "good", PrivateKey: pemBytes, Passphrase: "secret-pass"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if key.Type != "ed25519" {
		t.Fatalf("unexpected type %q", key.Type)
	}
	if !strings.HasPrefix(key.PublicKey, "ssh-ed25519 ") {
		t.Fatalf("unexpected public key %q", key.PublicKey)
	}
	signer, err := store.signer(key.ID, "")
	if err != nil {
		t.Fatalf("signer with stored passphrase: %v", err)
	}
	if signer == nil {
		t.Fatal("nil signer")
	}
}

func TestHostSecretsAreSealed(t *testing.T) {
	store := newStore(t)
	host, err := store.SaveHost(Host{
		Name: "web", Address: "10.0.0.5", Port: 22, User: "root",
		AuthMode: AuthPassword, Password: "super-secret",
	})
	if err != nil {
		t.Fatalf("save host: %v", err)
	}
	if host.Password != "" {
		t.Fatal("password must be redacted in the returned host")
	}
	raw, err := os.ReadFile(filepath.Join(store.dir, "hosts.json"))
	if err != nil {
		t.Fatalf("read hosts file: %v", err)
	}
	if strings.Contains(string(raw), "super-secret") {
		t.Fatal("plaintext password leaked to disk")
	}
	pass, err := store.HostPassword(host.ID)
	if err != nil {
		t.Fatalf("decrypt password: %v", err)
	}
	if pass != "super-secret" {
		t.Fatalf("password round trip mismatch: %q", pass)
	}

	// Reload from disk to prove persistence.
	reloaded, err := Open(store.dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, ok := reloaded.GetHost(host.ID)
	if !ok || got.Name != "web" || got.Addr() != "10.0.0.5:22" {
		t.Fatalf("unexpected reloaded host: %+v", got)
	}
	if p, err := reloaded.HostPassword(got.ID); err != nil || p != "super-secret" {
		t.Fatalf("reloaded password: %q %v", p, err)
	}
}

func TestJumpRouteRequiresHosts(t *testing.T) {
	store := newStore(t)
	if _, err := store.SaveHost(Host{Name: "x", Address: "1.1.1.1", User: "u", Route: RouteJump}); err == nil {
		t.Fatal("expected jump route without jump hosts to be rejected")
	}
}

// ---- live SSH dial tests -------------------------------------------------

type testRelayer struct {
	addr string
	used bool
}

func (r *testRelayer) DialTCP(ctx context.Context, target string) (net.Conn, error) {
	r.used = true
	var d net.Dialer
	return d.DialContext(ctx, "tcp", r.addr)
}

func (r *testRelayer) Available() bool  { return true }
func (r *testRelayer) Describe() string { return "test-relay" }

func startSSHServer(t *testing.T, password string) (addr string, hostFingerprint string) {
	t.Helper()
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatalf("host signer: %v", err)
	}
	hostFingerprint = ssh.FingerprintSHA256(signer.PublicKey())

	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if c.User() == "tester" && string(pass) == password {
				return nil, nil
			}
			return nil, os.ErrPermission
		},
	}
	cfg.AddHostKey(signer)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = lis.Close() })

	go func() {
		for {
			conn, err := lis.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				sconn, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					_ = c.Close()
					return
				}
				defer sconn.Close()
				go ssh.DiscardRequests(reqs)
				for newCh := range chans {
					if newCh.ChannelType() != "session" {
						_ = newCh.Reject(ssh.UnknownChannelType, "only sessions")
						continue
					}
					ch, chReqs, err := newCh.Accept()
					if err != nil {
						continue
					}
					go func(ch ssh.Channel, reqs <-chan *ssh.Request) {
						defer ch.Close()
						for req := range reqs {
							switch req.Type {
							case "exec":
								_, _ = ch.Write([]byte("test-ssh-ok\n"))
								_ = req.Reply(true, nil)
								_, _ = ch.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
								return
							case "subsystem":
								// Serve the real SFTP protocol on this channel.
								_ = req.Reply(true, nil)
								srv, err := sftp.NewServer(ch)
								if err != nil {
									return
								}
								_ = srv.Serve()
								return
							case "pty-req":
								_ = req.Reply(true, nil)
							case "window-change":
								_ = req.Reply(true, nil)
							case "shell":
								_ = req.Reply(true, nil)
								_, _ = ch.Write([]byte("welcome\n"))
								// Echo everything back so terminal input is observable.
								buf := make([]byte, 1024)
								for {
									n, err := ch.Read(buf)
									if n > 0 {
										_, _ = ch.Write([]byte("got:"))
										_, _ = ch.Write(buf[:n])
									}
									if err != nil {
										return
									}
								}
							default:
								_ = req.Reply(false, nil)
							}
						}
					}(ch, chReqs)
				}
			}(conn)
		}
	}()
	return lis.Addr().String(), hostFingerprint
}

func TestConnectDirectPasswordAndRunCommand(t *testing.T) {
	addr, _ := startSSHServer(t, "pw-123")
	host, port := splitAddr(t, addr)
	store := newStore(t)
	saved, err := store.SaveHost(Host{
		Name: "test", Address: host, Port: port, User: "tester",
		AuthMode: AuthPassword, Password: "pw-123", Route: RouteDirect,
	})
	if err != nil {
		t.Fatalf("save host: %v", err)
	}
	mgr := NewManager(store, func(string) settings.ProxyRule { return settings.ProxyRule{Mode: "off"} }, nil, store.dir)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := mgr.Connect(ctx, saved.ID, DialOptions{})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if client.Transport != "direct" {
		t.Fatalf("unexpected transport %q", client.Transport)
	}
	out, err := mgr.RunCommand(context.Background(), client.ID, "echo hi", 10*time.Second)
	if err != nil {
		t.Fatalf("run command: %v", err)
	}
	if stdout, _ := out["stdout"].(string); !strings.Contains(stdout, "test-ssh-ok") {
		t.Fatalf("unexpected stdout: %+v", out)
	}
	// Host key must have been pinned on first use.
	if pinned := store.KnownHostFingerprint(saved.Addr()); pinned == "" {
		t.Fatal("expected the host key fingerprint to be pinned")
	}
	if sessions := mgr.Sessions(); len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if err := mgr.Disconnect(client.ID); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if sessions := mgr.Sessions(); len(sessions) != 0 {
		t.Fatalf("expected no sessions after disconnect, got %d", len(sessions))
	}
}

func TestConnectThroughRelay(t *testing.T) {
	addr, _ := startSSHServer(t, "pw-456")
	host, port := splitAddr(t, addr)
	store := newStore(t)
	saved, err := store.SaveHost(Host{
		Name: "relayed", Address: "203.0.113.9", Port: port, User: "tester",
		AuthMode: AuthPassword, Password: "pw-456", Route: RouteRelay, RelayTarget: addr,
	})
	if err != nil {
		t.Fatalf("save host: %v", err)
	}
	relay := &testRelayer{addr: addr}
	mgr := NewManager(store, func(string) settings.ProxyRule { return settings.ProxyRule{Mode: "off"} }, relay, store.dir)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := mgr.Connect(ctx, saved.ID, DialOptions{})
	if err != nil {
		t.Fatalf("connect through relay: %v", err)
	}
	if !relay.used {
		t.Fatal("expected the relay to be used")
	}
	if client.Transport != "relay:"+addr {
		t.Fatalf("unexpected transport %q", client.Transport)
	}
	_ = mgr.Disconnect(client.ID)
	_ = host
}

func TestConnectRequiresServerForRelayRoute(t *testing.T) {
	_, port := splitAddr(t, "127.0.0.1:22")
	store := newStore(t)
	saved, err := store.SaveHost(Host{
		Name: "relayed", Address: "203.0.113.10", Port: port, User: "tester",
		AuthMode: AuthPassword, Password: "x", Route: RouteRelay,
	})
	if err != nil {
		t.Fatalf("save host: %v", err)
	}
	mgr := NewManager(store, nil, nil, store.dir)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = mgr.Connect(ctx, saved.ID, DialOptions{})
	if err == nil || !strings.Contains(err.Error(), "relay") {
		t.Fatalf("expected relay availability error, got %v", err)
	}
}

func TestConnectWrongPasswordFails(t *testing.T) {
	addr, _ := startSSHServer(t, "right-password")
	host, port := splitAddr(t, addr)
	store := newStore(t)
	saved, err := store.SaveHost(Host{
		Name: "test", Address: host, Port: port, User: "tester",
		AuthMode: AuthPassword, Password: "wrong-password", Route: RouteDirect,
	})
	if err != nil {
		t.Fatalf("save host: %v", err)
	}
	mgr := NewManager(store, func(string) settings.ProxyRule { return settings.ProxyRule{Mode: "off"} }, nil, store.dir)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := mgr.Connect(ctx, saved.ID, DialOptions{}); err == nil {
		t.Fatal("expected authentication failure")
	}
}

func splitAddr(t *testing.T, addr string) (string, int) {
	t.Helper()
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split %q: %v", addr, err)
	}
	port := 0
	for _, r := range p {
		port = port*10 + int(r-'0')
	}
	return h, port
}
