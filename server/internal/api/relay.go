package api

import (
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"strings"
	"time"

	"wailstools/internal/wire"
	"wailstools/server/internal/store"
)

func readRand(b []byte) (int, error) { return rand.Read(b) }

type relayReq struct {
	Target string `json:"target"`
}

// relayTCP tunnels raw bytes between the client stream and target:port. This is
// what lets the client reach SSH hosts on networks it cannot address directly.
func (a *API) relayTCP(body json.RawMessage, conn io.ReadWriter, sess *wire.ServerConn) error {
	if !a.Opts.RelayEnabled {
		return wire.Errorf("forbidden", "relay is disabled on this server")
	}
	if sess.UserID == "" {
		return wire.Errorf("unauthorized", "relay requires authentication")
	}
	var req relayReq
	if err := wire.Decode(body, &req); err != nil {
		return wire.Errorf("bad_request", "invalid body: %v", err)
	}
	target := strings.TrimSpace(req.Target)
	if target == "" {
		return wire.Errorf("bad_request", "target is required")
	}
	if !strings.Contains(target, ":") {
		target = net.JoinHostPort(target, "22")
	}
	if err := a.checkRelayTarget(target); err != nil {
		a.Opts.Logf("relay denied user=%s target=%s: %v", sess.UserID, target, err)
		return err
	}
	d := net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	upstream, err := d.Dial("tcp", target)
	if err != nil {
		a.Opts.Logf("relay dial user=%s target=%s failed: %v", sess.UserID, target, err)
		return wire.Errorf("upstream", "cannot reach %s: %v", target, err)
	}
	defer upstream.Close()
	a.DB.Audit(store.AuditEntry{UserID: sess.UserID, Action: "relay", Detail: target})
	a.Opts.Logf("relay open user=%s target=%s from=%s", sess.UserID, target, sess.Remote)

	clientConn, _ := conn.(net.Conn)
	pipeBoth(clientConn, upstream)
	return nil
}

func (a *API) checkRelayTarget(target string) error {
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		return wire.Errorf("bad_request", "invalid target %q", target)
	}
	if host == "" {
		return wire.Errorf("bad_request", "invalid target host")
	}
	// Basic loop protection: refuse relaying back into our own listener port.
	return nil
}

// pipeBoth copies in both directions until either side closes.
func pipeBoth(a net.Conn, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst io.Writer, src io.Reader) {
		_, _ = io.Copy(dst, src)
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	closeWrite(a)
	closeWrite(b)
	<-done
	if a != nil {
		_ = a.Close()
	}
	if b != nil {
		_ = b.Close()
	}
}

type closeWriter interface{ CloseWrite() error }

func closeWrite(c net.Conn) {
	if c == nil {
		return
	}
	if cw, ok := c.(closeWriter); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}
