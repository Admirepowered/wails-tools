package integration

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	"wailstools/internal/p2p"
	"wailstools/internal/serverlink"
	"wailstools/internal/settings"
	"wailstools/internal/wire"
	"wailstools/server/internal/server"
)

// startP2PServer enables the UDP rendezvous (server TCP port + 1).
func startP2PServer(t *testing.T) *server.Server {
	t.Helper()
	dir := t.TempDir()
	port := freePort(t)
	srv, err := server.New(server.Config{
		DataDir:           filepath.Join(dir, "data"),
		TCPAddr:           net.JoinHostPort("127.0.0.1", itoa(port)),
		UDPAddr:           net.JoinHostPort("127.0.0.1", itoa(port)),
		EnableUDP:         true,
		EnableTCP:         true,
		AllowRegistration: true,
		RelayEnabled:      true,
		DefaultQuotaBytes: 64 << 20,
		P2PAddr:           net.JoinHostPort("127.0.0.1", itoa(port+1)),
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		shutdownCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = srv.Shutdown(shutdownCtx)
	})
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("server.Start: %v", err)
	}
	return srv
}

// Two clients exchange a hole-punch session through the server and then talk
// TCP over the punched QUIC channel.
func TestP2PHolePunchAndRelay(t *testing.T) {
	srv := startP2PServer(t)
	_, tcpAddr := srv.Addrs()
	_, portStr, err := net.SplitHostPort(tcpAddr)
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	port := atoi(portStr)
	rendezvous := net.JoinHostPort("127.0.0.1", itoa(port+1))

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// A local echo server that the peer will reach through the punched channel.
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	defer echo.Close()
	go func() {
		for {
			conn, err := echo.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()

	// Two "devices" of the same account.
	mkClient := func(deviceID, machine string) *serverlink.Client {
		dir := t.TempDir()
		store, err := settings.NewStore(filepath.Join(dir, "settings.json"))
		if err != nil {
			t.Fatalf("settings: %v", err)
		}
		if _, err := store.Update(func(s *settings.Settings) error {
			s.Server.Host = "127.0.0.1"
			s.Server.Port = port
			s.Server.DeviceID = deviceID
			return nil
		}); err != nil {
			t.Fatalf("configure: %v", err)
		}
		c := serverlink.New(store, t.Logf)
		c.SetMachineID(machine)
		t.Cleanup(func() { _ = c.Close() })
		return c
	}

	deviceA := mkClient("dev-a", "machine-a")
	deviceB := mkClient("dev-b", "machine-b")
	if _, err := deviceA.Connect(ctx); err != nil {
		t.Fatalf("connect A: %v", err)
	}
	if _, err := deviceA.Register(ctx, "alice", "super-secret-1", "", "Alice"); err != nil {
		t.Fatalf("register: %v", err)
	}
	// Registration alone does not create a device row; log in once to register
	// dev-a like a real client would.
	if _, err := deviceA.Login(ctx, "alice", "super-secret-1"); err != nil {
		t.Fatalf("login A: %v", err)
	}
	if _, err := deviceB.Connect(ctx); err != nil {
		t.Fatalf("connect B: %v", err)
	}
	if _, err := deviceB.Login(ctx, "alice", "super-secret-1"); err != nil {
		t.Fatalf("login B: %v", err)
	}

	// A offers a punch session to B.
	var offer struct {
		Session string `json:"session"`
		Secret  string `json:"secret"`
	}
	if err := deviceA.Call(ctx, "p2p.offer", map[string]any{
		"fromDevice": "dev-a", "toDevice": "dev-b",
	}, &offer); err != nil {
		t.Fatalf("p2p.offer: %v", err)
	}
	if offer.Session == "" || offer.Secret == "" {
		t.Fatalf("empty offer: %+v", offer)
	}

	var pending struct {
		Sessions []struct {
			ID     string `json:"id"`
			From   string `json:"from"`
			Secret string `json:"secret"`
		} `json:"sessions"`
	}
	if err := deviceB.Call(ctx, "p2p.pending", map[string]any{"device": "dev-b"}, &pending); err != nil {
		t.Fatalf("p2p.pending: %v", err)
	}
	if len(pending.Sessions) != 1 || pending.Sessions[0].ID != offer.Session {
		t.Fatalf("B did not see the offer: %+v", pending.Sessions)
	}

	// Punch from both sides at once.
	type result struct {
		ch  *p2p.Channel
		err error
	}
	doneA := make(chan result, 1)
	doneB := make(chan result, 1)
	go func() {
		ch, err := p2p.Punch(ctx, p2p.Options{Rendezvous: rendezvous, Session: offer.Session, Secret: offer.Secret, Role: "a", Timeout: 40 * time.Second})
		doneA <- result{ch, err}
	}()
	go func() {
		ch, err := p2p.Punch(ctx, p2p.Options{Rendezvous: rendezvous, Session: offer.Session, Secret: offer.Secret, Role: "b", Timeout: 40 * time.Second})
		doneB <- result{ch, err}
	}()
	var chA, chB *p2p.Channel
	for i := 0; i < 2; i++ {
		select {
		case r := <-doneA:
			if r.err != nil {
				t.Fatalf("punch A: %v", r.err)
			}
			chA = r.ch
		case r := <-doneB:
			if r.err != nil {
				t.Fatalf("punch B: %v", r.err)
			}
			chB = r.ch
		case <-ctx.Done():
			t.Fatal("punch timed out")
		}
	}
	defer chA.Close()
	defer chB.Close()

	// B serves peer requests; A relays through it to the echo server.
	handler := &relayHandler{}
	go chB.Serve(context.Background(), handler)

	conn, err := chA.DialTCP(ctx, echo.Addr().String())
	if err != nil {
		t.Fatalf("DialTCP: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write([]byte("hello-p2p")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len("hello-p2p"))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != "hello-p2p" {
		t.Fatalf("echo mismatch: %q", buf)
	}

}

// relayHandler implements the minimal wire handler the peer needs: ping plus a
// TCP relay for p2p.tcp.
type relayHandler struct{}

func (h *relayHandler) Handle(method string, body json.RawMessage, sess *wire.ServerConn) (any, error) {
	if method == "p2p.ping" {
		return map[string]any{"ok": true}, nil
	}
	return nil, wire.Errorf("unknown_method", "unsupported")
}

func (h *relayHandler) Pipe(method string, body json.RawMessage, conn net.Conn, sess *wire.ServerConn) error {
	if method != p2p.MethodDialTCP {
		return wire.Errorf("unknown_method", "unsupported stream")
	}
	var req struct {
		Target string `json:"target"`
	}
	if err := wire.Decode(body, &req); err != nil {
		return wire.Errorf("bad_request", "invalid body")
	}
	upstream, err := net.Dial("tcp", req.Target)
	if err != nil {
		return err
	}
	defer upstream.Close()
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, conn)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(conn, upstream)
		done <- struct{}{}
	}()
	<-done
	return nil
}

// TestP2PInviteAcceptAndMap covers the explicit-accept flow: the initiator asks
// by 9-digit device number, the target accepts, both punch and the initiator
// can forward a local TCP port to the peer (port mapping uses the same path).
func TestP2PInviteAcceptAndMap(t *testing.T) {
	srv := startP2PServer(t)
	_, tcpAddr := srv.Addrs()
	_, portStr, err := net.SplitHostPort(tcpAddr)
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	port := atoi(portStr)
	rendezvous := net.JoinHostPort("127.0.0.1", itoa(port+1))

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	defer echo.Close()
	go func() {
		for {
			conn, err := echo.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()

	mkClient := func(deviceID, machine string) *serverlink.Client {
		dir := t.TempDir()
		store, err := settings.NewStore(filepath.Join(dir, "settings.json"))
		if err != nil {
			t.Fatalf("settings: %v", err)
		}
		if _, err := store.Update(func(s *settings.Settings) error {
			s.Server.Host = "127.0.0.1"
			s.Server.Port = port
			s.Server.DeviceID = deviceID
			return nil
		}); err != nil {
			t.Fatalf("configure: %v", err)
		}
		c := serverlink.New(store, t.Logf)
		c.SetMachineID(machine)
		t.Cleanup(func() { _ = c.Close() })
		return c
	}

	a := mkClient("dev-a", "machine-a")
	b := mkClient("dev-b", "machine-b")
	if _, err := a.Connect(ctx); err != nil {
		t.Fatalf("connect A: %v", err)
	}
	if _, err := a.Register(ctx, "bob", "super-secret-2", "", "Bob"); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := a.Login(ctx, "bob", "super-secret-2"); err != nil {
		t.Fatalf("login A: %v", err)
	}
	if _, err := b.Connect(ctx); err != nil {
		t.Fatalf("connect B: %v", err)
	}
	if _, err := b.Login(ctx, "bob", "super-secret-2"); err != nil {
		t.Fatalf("login B: %v", err)
	}

	// Device numbers are allocated per device.
	type selfDevices struct {
		Devices []struct {
			ID     string `json:"id"`
			Number string `json:"number"`
		} `json:"devices"`
	}
	numberOf := func(c *serverlink.Client, device string) string {
		var out selfDevices
		if err := c.Call(ctx, "admin.devices.self", nil, &out); err != nil {
			t.Fatalf("devices.self: %v", err)
		}
		for _, d := range out.Devices {
			if d.ID == device {
				return d.Number
			}
		}
		return ""
	}
	numB := numberOf(b, "dev-b")
	if len(numB) != 9 {
		t.Fatalf("device B number = %q", numB)
	}

	// A invites B by number.
	var invite struct {
		Request struct {
			ID string `json:"id"`
			To string `json:"toDevice"`
		} `json:"request"`
	}
	if err := a.Call(ctx, "p2p.request", map[string]any{"number": numB, "device": "dev-a"}, &invite); err != nil {
		t.Fatalf("p2p.request: %v", err)
	}
	if invite.Request.ID == "" {
		t.Fatal("empty request id")
	}

	// B sees it and accepts.
	var pending struct {
		Requests []struct {
			ID     string `json:"id"`
			Number string `json:"fromNumber"`
		} `json:"requests"`
	}
	if err := b.Call(ctx, "p2p.requests", map[string]any{"device": "dev-b"}, &pending); err != nil {
		t.Fatalf("p2p.requests: %v", err)
	}
	if len(pending.Requests) != 1 || pending.Requests[0].ID != invite.Request.ID {
		t.Fatalf("B did not see the invitation: %+v", pending.Requests)
	}
	var accepted struct {
		Request struct {
			State   string `json:"state"`
			Session string `json:"session"`
			Secret  string `json:"secret"`
		} `json:"request"`
	}
	if err := b.Call(ctx, "p2p.respond", map[string]any{"device": "dev-b", "request": invite.Request.ID, "accept": true}, &accepted); err != nil {
		t.Fatalf("p2p.respond: %v", err)
	}
	if accepted.Request.State != "accepted" || accepted.Request.Session == "" {
		t.Fatalf("accept failed: %+v", accepted.Request)
	}

	// While serving, B may not initiate (role lock).
	if err := b.Call(ctx, "p2p.request", map[string]any{"number": numB, "device": "dev-b"}, new(map[string]any)); err == nil {
		t.Fatal("serving device must not initiate a punch")
	}

	// A polls and gets the same session.
	var state struct {
		Found   bool `json:"found"`
		Request struct {
			State   string `json:"state"`
			Session string `json:"session"`
			Secret  string `json:"secret"`
		} `json:"request"`
	}
	if err := a.Call(ctx, "p2p.requestState", map[string]any{"device": "dev-a", "request": invite.Request.ID}, &state); err != nil {
		t.Fatalf("p2p.requestState: %v", err)
	}
	if !state.Found || state.Request.State != "accepted" || state.Request.Session != accepted.Request.Session {
		t.Fatalf("initiator did not see acceptance: %+v", state)
	}

	// Both sides punch with the accepted session.
	type result struct {
		ch  *p2p.Channel
		err error
	}
	doneA := make(chan result, 1)
	doneB := make(chan result, 1)
	go func() {
		ch, err := p2p.Punch(ctx, p2p.Options{Rendezvous: rendezvous, Session: state.Request.Session, Secret: state.Request.Secret, Role: "a", Timeout: 40 * time.Second})
		doneA <- result{ch, err}
	}()
	go func() {
		ch, err := p2p.Punch(ctx, p2p.Options{Rendezvous: rendezvous, Session: state.Request.Session, Secret: state.Request.Secret, Role: "b", Timeout: 40 * time.Second})
		doneB <- result{ch, err}
	}()
	var chA, chB *p2p.Channel
	for i := 0; i < 2; i++ {
		select {
		case r := <-doneA:
			if r.err != nil {
				t.Fatalf("punch A: %v", r.err)
			}
			chA = r.ch
		case r := <-doneB:
			if r.err != nil {
				t.Fatalf("punch B: %v", r.err)
			}
			chB = r.ch
		case <-ctx.Done():
			t.Fatal("punch timed out")
		}
	}
	defer chA.Close()
	defer chB.Close()
	go chB.Serve(context.Background(), &relayHandler{})

	// Port mapping: forward through the channel to the echo server.
	conn, err := chA.DialTCP(ctx, echo.Addr().String())
	if err != nil {
		t.Fatalf("DialTCP: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write([]byte("mapped")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len("mapped"))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != "mapped" {
		t.Fatalf("echo mismatch: %q", buf)
	}
}
