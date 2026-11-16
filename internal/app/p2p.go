package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"wailstools/internal/machineid"
	"wailstools/internal/p2p"
	"wailstools/internal/settings"
	"wailstools/internal/wire"
)

// p2pHub owns the direct (hole punched) connections to other devices.
type p2pHub struct {
	mu       sync.Mutex
	channels map[string]*p2p.Channel // deviceID -> live channel
	errors   map[string]string       // deviceID -> last failure reason
	busy     map[string]bool
	seen     map[string]bool // answered rendezvous sessions
	incoming map[string]bool // device -> we answered its punch (serving)
	outgoing map[string]bool // device -> we punched to it
	maps     map[string]map[int]*portMap
	watchMu  sync.Mutex
	watching bool
}

// portMap is one local TCP listener forwarded to the peer's port.
type portMap struct {
	Local  int
	Remote int
	Device string
	ln     net.Listener
}

func newP2PHub() *p2pHub {
	return &p2pHub{
		channels: map[string]*p2p.Channel{},
		errors:   map[string]string{},
		busy:     map[string]bool{},
		seen:     map[string]bool{},
		incoming: map[string]bool{},
		outgoing: map[string]bool{},
		maps:     map[string]map[int]*portMap{},
	}
}

func (h *p2pHub) get(deviceID string) (*p2p.Channel, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.channels[deviceID]
	return c, ok
}

func (h *p2pHub) put(deviceID string, c *p2p.Channel) {
	h.mu.Lock()
	if old := h.channels[deviceID]; old != nil && old != c {
		_ = old.Close()
	}
	h.channels[deviceID] = c
	delete(h.errors, deviceID)
	h.mu.Unlock()
}

// watch closes the hub entry when the QUIC connection dies, so mappings bound
// to a dead path are released instead of silently black-holing traffic.
func (h *p2pHub) watch(deviceID string, c *p2p.Channel, onClose func(string)) {
	go func() {
		<-c.Done()
		h.mu.Lock()
		cur, ok := h.channels[deviceID]
		if !ok || cur != c {
			h.mu.Unlock()
			return
		}
		h.mu.Unlock()
		h.drop(deviceID)
		if onClose != nil {
			onClose(deviceID)
		}
	}()
}

// markIncoming records that this device answers punches from deviceID.
func (h *p2pHub) markIncoming(deviceID string) {
	h.mu.Lock()
	h.incoming[deviceID] = true
	delete(h.outgoing, deviceID)
	h.mu.Unlock()
}

// markOutgoing records that this device punched towards deviceID.
func (h *p2pHub) markOutgoing(deviceID string) {
	h.mu.Lock()
	h.outgoing[deviceID] = true
	delete(h.incoming, deviceID)
	h.mu.Unlock()
}

func (h *p2pHub) fail(deviceID string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err != nil {
		h.errors[deviceID] = err.Error()
	}
}

func (h *p2pHub) drop(deviceID string) {
	h.mu.Lock()
	c := h.channels[deviceID]
	delete(h.channels, deviceID)
	delete(h.incoming, deviceID)
	delete(h.outgoing, deviceID)
	maps := h.maps[deviceID]
	delete(h.maps, deviceID)
	h.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
	// Disconnecting the punch releases every port mapping bound to it.
	for _, pm := range maps {
		if pm.ln != nil {
			_ = pm.ln.Close()
		}
	}
}

// rendezvousAddr resolves the server's UDP rendezvous endpoint.
func (a *App) rendezvousAddr() (string, error) {
	cfg := a.settings.Get()
	host := strings.TrimSpace(cfg.Server.Host)
	if host == "" {
		return "", errors.New("未配置同步服务器地址")
	}
	port := 0
	if info, err := a.RemoteServerInfo(); err == nil {
		if v, ok := info["p2pPort"].(float64); ok {
			port = int(v)
		}
	}
	if port <= 0 && cfg.Server.Port > 0 {
		port = cfg.Server.Port + 1
	}
	if port <= 0 {
		return "", errors.New("服务器未开启打洞服务")
	}
	return net.JoinHostPort(host, fmt.Sprintf("%d", port)), nil
}

// localDeviceID is the device id this installation reports on login.
func (a *App) localDeviceID() string { return a.settings.DeviceID() }

// P2PProbe checks that the server's UDP rendezvous is reachable from here.
// A failure usually means the port is blocked by a firewall or security group.
func (a *App) P2PProbe() map[string]any {
	out := map[string]any{"ok": false}
	addr, err := a.rendezvousAddr()
	if err != nil {
		out["error"] = err.Error()
		return out
	}
	out["rendezvous"] = addr
	raddr, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		out["error"] = err.Error()
		return out
	}
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		out["error"] = err.Error()
		return out
	}
	defer conn.Close()
	payload := []byte(`{"s":"probe","r":"a"}`)
	if _, err := conn.WriteToUDP(payload, raddr); err != nil {
		out["error"] = err.Error()
		return out
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 512)
	n, from, err := conn.ReadFromUDP(buf)
	if err != nil {
		out["error"] = "打洞服务无响应：服务器需要放行 UDP " + addr + "（通常是同步端口 +1）"
		return out
	}
	if !from.IP.Equal(raddr.IP) || from.Port != raddr.Port {
		out["error"] = "收到非打洞服务的响应"
		return out
	}
	out["ok"] = true
	out["reply"] = strings.TrimSpace(string(buf[:n]))
	out["publicEndpoint"] = conn.LocalAddr().String()
	return out
}

// P2PStatus reports the hole-punch state of every device and connection.
func (a *App) P2PStatus() map[string]any {
	h := a.p2p
	h.mu.Lock()
	connected := make([]string, 0, len(h.channels))
	for id := range h.channels {
		connected = append(connected, id)
	}
	errs := make(map[string]string, len(h.errors))
	for id, msg := range h.errors {
		errs[id] = msg
	}
	h.mu.Unlock()
	return map[string]any{
		"localDevice": a.localDeviceID(),
		"connected":   connected,
		"errors":      errs,
		"loggedIn":    a.sess.loggedIn,
	}
}

// P2PConnect punches a direct path to another device of this account.
func (a *App) P2PConnect(deviceID string) (map[string]any, error) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return nil, errors.New("请选择设备")
	}
	if deviceID == a.localDeviceID() {
		return nil, errors.New("不能连接本机")
	}
	if !a.sess.loggedIn || a.link == nil || !a.link.Available() {
		return nil, errors.New("请先连接并登录同步服务器")
	}
	if _, ok := a.p2p.get(deviceID); ok {
		return a.P2PStatus(), nil
	}
	if _, err := a.p2pConnectTarget(deviceID, "", deviceID); err != nil {
		a.p2p.fail(deviceID, err)
		return nil, err
	}
	return a.P2PStatus(), nil
}

// P2PDisconnect closes a direct path.
func (a *App) P2PDisconnect(deviceID string) (map[string]any, error) {
	a.p2p.drop(strings.TrimSpace(deviceID))
	a.emit("p2p:disconnected", map[string]any{"device": deviceID})
	return a.P2PStatus(), nil
}

// startP2PWatcher polls incoming punch invitations for this device. Nothing is
// answered automatically: the user must accept in the UI.
func (a *App) startP2PWatcher() {
	h := a.p2p
	h.watchMu.Lock()
	if h.watching {
		h.watchMu.Unlock()
		return
	}
	h.watching = true
	h.watchMu.Unlock()

	go func() {
		for {
			time.Sleep(3 * time.Second)
			if !a.sess.loggedIn || a.link == nil || !a.link.Available() {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			list, err := a.link.P2PRequests(ctx, a.localDeviceID())
			cancel()
			if err != nil {
				continue
			}
			known := map[string]bool{}
			for _, inv := range a.invites.list() {
				known[inv.ID] = true
			}
			for _, item := range list {
				id, _ := item["id"].(string)
				if id == "" || known[id] {
					continue
				}
				number, _ := item["fromNumber"].(string)
				name, _ := item["fromName"].(string)
				from, _ := item["fromDevice"].(string)
				created, _ := item["created"].(string)
				a.invites.put(p2pInvite{ID: id, Number: number, Name: name, From: from, State: "pending", Created: created})
				a.emit("p2p:invite", map[string]any{"id": id, "number": number, "name": name})
				if a.settings.Get().ProxyRuntime.P2PAutoAccept {
					a.p2pLogf("p2p: auto-accepting invite %s from %s(#%s)", id, from, number)
					go func(reqID string) {
						if _, err := a.P2PAcceptInvite(reqID); err != nil {
							a.p2pLogf("p2p: auto-accept %s failed: %v", reqID, err)
						}
					}(id)
				}
			}
		}
	}()
}

// answerPunch performs the responder side of a hole punch and starts serving.
func (a *App) answerPunch(sessionID, fromDevice, secret string) {
	if _, ok := a.p2p.get(fromDevice); ok {
		return
	}
	rendezvous, err := a.rendezvousAddr()
	if err != nil {
		a.p2p.fail(fromDevice, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	channel, err := p2p.Punch(ctx, p2p.Options{
		Rendezvous: rendezvous, Session: sessionID, Secret: secret,
		Role: "b", Timeout: 40 * time.Second, LocalPort: a.settings.Get().ProxyRuntime.P2PPort,
		Logf: func(format string, args ...any) {
			if a.log != nil {
				a.log.Printf(format, args...)
			}
		},
	})
	if err != nil {
		a.p2p.fail(fromDevice, err)
		a.emit("p2p:error", map[string]any{"device": fromDevice, "error": err.Error()})
		return
	}
	a.p2p.put(fromDevice, channel)
	a.p2p.markIncoming(fromDevice)
	a.p2p.watch(fromDevice, channel, func(dev string) {
		a.p2pLogf("p2p: link to %s lost, releasing mappings", dev)
		a.emit("p2p:disconnected", map[string]any{"device": dev})
	})
	go channel.Serve(context.Background(), &p2pWireHandler{app: a})
	a.emit("p2p:connected", map[string]any{"device": fromDevice, "peer": channel.Peer(), "incoming": true})
}

// ---- peer requests -------------------------------------------------------

// p2pWireHandler serves requests that arrive over a punched channel.
type p2pWireHandler struct{ app *App }

func (h *p2pWireHandler) Handle(method string, body json.RawMessage, sess *wire.ServerConn) (any, error) {
	switch method {
	case "p2p.ping":
		host, _ := os.Hostname()
		return map[string]any{"ok": true, "host": host}, nil
	case "p2p.capabilities":
		// Reserved: file management will advertise its methods here.
		return map[string]any{"tcp": true, "files": false}, nil
	default:
		return nil, wire.Errorf("unknown_method", "p2p method %q is not implemented", method)
	}
}

func (h *p2pWireHandler) Pipe(method string, body json.RawMessage, conn net.Conn, sess *wire.ServerConn) error {
	switch method {
	case "p2p.tcp":
		var req struct {
			Target string `json:"target"`
		}
		if err := wire.Decode(body, &req); err != nil {
			return wire.Errorf("bad_request", "invalid body: %v", err)
		}
		return h.pipeTCP(req.Target, conn)
	default:
		return wire.Errorf("unknown_method", "p2p stream method %q is not implemented", method)
	}
}

// pipeTCP connects to target on behalf of the peer and copies bytes both ways.
func (h *p2pWireHandler) pipeTCP(target string, conn net.Conn) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return wire.Errorf("bad_request", "target is required")
	}
	if err := p2pTargetAllowed(target); err != nil {
		return wire.Errorf("forbidden", "%v", err)
	}
	dialer := net.Dialer{Timeout: 15 * time.Second}
	upstream, err := dialer.DialContext(context.Background(), "tcp", target)
	if err != nil {
		return wire.Errorf("unreachable", "连接 %s 失败: %v", target, err)
	}
	defer upstream.Close()

	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, conn)
		if cw, ok := upstream.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(conn, upstream)
		if cw, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		done <- struct{}{}
	}()
	<-done
	return nil
}

// p2pTargetAllowed blocks cloud metadata endpoints; everything else (including
// the peer's own localhost) is intentionally reachable.
func p2pTargetAllowed(target string) error {
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		return errors.New("目标地址格式无效")
	}
	if host == "169.254.169.254" || strings.HasPrefix(host, "169.254.") {
		return errors.New("禁止访问云元数据地址")
	}
	return nil
}

// ---- dialers -------------------------------------------------------------

// p2pDialTCP opens a TCP path through a punched device (proxy hop / SSH jump).
func (a *App) p2pDialTCP(ctx context.Context, deviceID, target string) (net.Conn, error) {
	return (&p2pDevice{app: a}).DialTCP(ctx, deviceID, target)
}

// p2pDialer opens TCP streams through a punched device.
type p2pDevice struct{ app *App }

func (d *p2pDevice) Ready(deviceID string) bool {
	_, ok := d.app.p2p.get(deviceID)
	return ok
}

func (d *p2pDevice) DialTCP(ctx context.Context, deviceID, target string) (net.Conn, error) {
	channel, ok := d.app.p2p.get(deviceID)
	if !ok {
		return nil, errors.New("该设备尚未打通直连：请在「同步服务 → 我的设备」右键选择「连接到此设备」")
	}
	return channel.DialTCP(ctx, target)
}

var _ settings.SecretSyncSettings

// P2PLocalNumber returns this installation's 9-digit device number as stored on
// the sync server (empty when the account has no device row yet).
func (a *App) P2PLocalNumber() map[string]any {
	out := map[string]any{"deviceId": a.localDeviceID(), "number": "", "name": ""}
	if a.link == nil || !a.link.Available() {
		out["error"] = "同步服务器未连接"
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	var res struct {
		Devices []struct {
			ID       string `json:"id"`
			Number   string `json:"number"`
			Name     string `json:"name"`
			LastSeen string `json:"lastSeen"`
		} `json:"devices"`
	}
	host, _ := os.Hostname()
	body := map[string]any{
		"deviceId":   a.localDeviceID(),
		"deviceName": host,
		"machineId":  machineid.ID(),
	}
	if err := a.link.Call(ctx, "admin.devices.self", body, &res); err != nil {
		out["error"] = err.Error()
		return out
	}
	want := a.localDeviceID()
	ids := make([]string, 0, len(res.Devices))
	for _, d := range res.Devices {
		ids = append(ids, d.ID+"#"+d.Number)
	}
	a.p2pLogf("p2p: local device id %s, host %s, account devices %v", want, host, ids)
	pick := func(pred func(id, name string) bool) (string, string, bool) {
		for _, d := range res.Devices {
			if pred(d.ID, d.Name) && d.Number != "" {
				return d.Number, d.Name, true
			}
		}
		return "", "", false
	}
	if num, name, ok := pick(func(id, _ string) bool { return id == want }); ok {
		out["number"], out["name"] = num, name
		return out
	}
	// Legacy rows may have registered under another id for this machine.
	if num, name, ok := pick(func(_, n string) bool { return n == host }); ok {
		out["number"], out["name"], out["fallback"] = num, name, true
		return out
	}
	if len(res.Devices) == 1 && res.Devices[0].Number != "" {
		out["number"], out["name"], out["fallback"] = res.Devices[0].Number, res.Devices[0].Name, true
		return out
	}
	out["error"] = "本机设备尚未登记，请重新登录"
	return out
}
