package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"wailstools/internal/p2p"
)

// p2pInvite is one pending punch invitation shown to the user.
type p2pInvite struct {
	ID       string `json:"id"`
	Number   string `json:"number"`
	Name     string `json:"name"`
	From     string `json:"from"`
	State    string `json:"state"`
	Created  string `json:"created"`
	accepted bool
}

// invites keeps the in-memory invitations seen by the watcher.
type inviteBox struct {
	mu    sync.Mutex
	items map[string]p2pInvite
	order []string
}

func newInviteBox() *inviteBox {
	return &inviteBox{items: map[string]p2pInvite{}}
}

func (b *inviteBox) put(inv p2pInvite) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.items[inv.ID]; !ok {
		b.order = append(b.order, inv.ID)
	}
	b.items[inv.ID] = inv
}

func (b *inviteBox) remove(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.items, id)
}

func (b *inviteBox) list() []p2pInvite {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]p2pInvite, 0, len(b.items))
	for _, id := range b.order {
		if inv, ok := b.items[id]; ok {
			out = append(out, inv)
		}
	}
	return out
}

// P2PInvites returns the invitations waiting for the user's answer.
func (a *App) P2PInvites() []p2pInvite {
	return a.invites.list()
}

// P2PConnectByNumber invites a device by its 9-digit number and, once the peer
// accepts, punches a direct path to it.
func (a *App) P2PConnectByNumber(number string) (map[string]any, error) {
	number = strings.TrimSpace(number)
	if len(number) != 9 {
		return nil, errors.New("请输入 9 位设备号")
	}
	return a.p2pConnectTarget("", number, "")
}

// p2pConnectTarget invites a device either by 9-digit number or by device id,
// then waits for acceptance and punches.
func (a *App) p2pConnectTarget(targetDevice, number, label string) (map[string]any, error) {
	link, err := a.requireLink()
	if err != nil {
		return nil, err
	}
	// One attempt at a time; ignore double clicks while waiting for an answer.
	if label != "" {
		a.p2p.mu.Lock()
		if _, ok := a.p2p.channels[label]; ok {
			a.p2p.mu.Unlock()
			return map[string]any{"device": label, "already": true}, nil
		}
		if a.p2p.busy[label] {
			a.p2p.mu.Unlock()
			return nil, errors.New("该设备正在打洞中，请稍候")
		}
		a.p2p.busy[label] = true
		a.p2p.mu.Unlock()
		defer func() {
			a.p2p.mu.Lock()
			delete(a.p2p.busy, label)
			a.p2p.mu.Unlock()
		}()
	}
	me := a.localDeviceID()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var res map[string]any
	if number != "" {
		res, err = link.P2PRequest(ctx, number, me)
	} else {
		res, err = link.P2PRequestDevice(ctx, targetDevice, me)
	}
	if err != nil {
		return nil, err
	}
	req, _ := res["request"].(map[string]any)
	id, _ := req["id"].(string)
	toDevice, _ := req["toDevice"].(string)
	if id == "" {
		return nil, errors.New("服务器没有返回邀请编号")
	}
	a.p2pLogf("p2p: invite %s sent to %s (device %s, number %q)", id, label, toDevice, number)
	a.emit("p2p:invite-sent", map[string]any{"request": id, "number": number, "device": toDevice, "label": label})

	// Wait for the target to answer (max 90s, matching the server TTL).
	deadline := time.Now().Add(95 * time.Second)
	for time.Now().Before(deadline) {
		pollCtx, pollCancel := context.WithTimeout(context.Background(), 10*time.Second)
		st, err := link.P2PRequestState(pollCtx, me, id)
		pollCancel()
		if err == nil {
			if info, ok := st["request"].(map[string]any); ok {
				switch info["state"] {
				case "accepted":
					a.p2pLogf("p2p: invite %s accepted, punching to %s", id, toDevice)
					_ = link.P2PCancel(context.Background(), me, id)
					return a.p2pConnectDevice(toDevice, info)
				case "declined":
					a.p2pLogf("p2p: invite %s declined by %s", id, toDevice)
					return nil, errors.New("对方拒绝了穿透请求")
				case "expired":
					a.p2pLogf("p2p: invite %s expired (no answer)", id)
					return nil, errors.New("对方没有在规定时间内响应")
				}
			}
		}
		time.Sleep(1 * time.Second)
	}
	a.p2pLogf("p2p: invite %s timed out after 95s", id)
	_ = link.P2PCancel(context.Background(), me, id)
	return nil, errors.New("等待对方接受超时")
}

// p2pConnectDevice performs the initiator side of the punch once accepted.
func (a *App) p2pConnectDevice(deviceID string, accepted map[string]any) (map[string]any, error) {
	session, _ := accepted["session"].(string)
	secret, _ := accepted["secret"].(string)
	if session == "" || secret == "" {
		return nil, errors.New("服务器返回的打洞会话无效")
	}
	if ch, ok := a.p2p.get(deviceID); ok {
		return map[string]any{"device": deviceID, "peer": ch.Peer(), "already": true}, nil
	}
	rendezvous, err := a.rendezvousAddr()
	if err != nil {
		return nil, err
	}
	a.p2p.mu.Lock()
	a.p2p.busy[deviceID] = true
	a.p2p.mu.Unlock()
	defer func() {
		a.p2p.mu.Lock()
		delete(a.p2p.busy, deviceID)
		a.p2p.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	channel, err := p2p.Punch(ctx, p2p.Options{
		Rendezvous: rendezvous, Session: session, Secret: secret,
		Role: "a", Timeout: 45 * time.Second, LocalPort: a.settings.Get().ProxyRuntime.P2PPort,
		Logf: a.p2pLogf,
	})
	if err != nil {
		a.p2p.fail(deviceID, err)
		a.emit("p2p:error", map[string]any{"device": deviceID, "error": err.Error()})
		return nil, err
	}
	a.p2p.put(deviceID, channel)
	a.p2p.markOutgoing(deviceID)
	if !a.verifyChannel(deviceID, channel) {
		return nil, errors.New("打洞成功但链路无响应（NAT 映射可能已关闭）")
	}
	a.heartbeat(deviceID, channel)
	a.p2p.watch(deviceID, channel, func(dev string) {
		a.p2pLogf("p2p: link to %s lost, releasing mappings", dev)
		a.emit("p2p:disconnected", map[string]any{"device": dev})
	})
	go channel.Serve(context.Background(), &p2pWireHandler{app: a})
	a.emit("p2p:connected", map[string]any{"device": deviceID, "peer": channel.Peer()})
	return map[string]any{"device": deviceID, "peer": channel.Peer()}, nil
}

// P2PAcceptInvite accepts an invitation and answers the punch as the target.
func (a *App) P2PAcceptInvite(requestID string) (map[string]any, error) {
	link, err := a.requireLink()
	if err != nil {
		return nil, err
	}
	me := a.localDeviceID()
	ctx, cancel := withTimeout(15 * time.Second)
	defer cancel()
	res, err := link.P2PRespond(ctx, me, requestID, true)
	if err != nil {
		return nil, err
	}
	a.invites.remove(requestID)
	info, _ := res["request"].(map[string]any)
	session, _ := info["session"].(string)
	secret, _ := info["secret"].(string)
	fromDevice, _ := info["fromDevice"].(string)
	if session == "" || secret == "" {
		return nil, errors.New("服务器返回的打洞会话无效")
	}
	// Refuse when this device is already punching outwards (role lock).
	if a.p2p.serving() && a.p2p.initiating() {
		return nil, errors.New("当前设备已作为穿透端，不能同时被穿透")
	}
	a.p2pLogf("p2p: accepted invite %s from %s, punching back", requestID, fromDevice)
	a.answerPunch(session, fromDevice, secret)
	return map[string]any{"device": fromDevice, "accepted": true}, nil
}

// P2PDeclineInvite rejects an invitation.
func (a *App) P2PDeclineInvite(requestID string) error {
	link, err := a.requireLink()
	if err != nil {
		return err
	}
	me := a.localDeviceID()
	ctx, cancel := withTimeout(15 * time.Second)
	defer cancel()
	if _, err := link.P2PRespond(ctx, me, requestID, false); err != nil {
		return err
	}
	a.invites.remove(requestID)
	return nil
}

func (a *App) p2pLogf(format string, args ...any) {
	if a.log != nil {
		a.log.Printf(format, args...)
	}
}

func withTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// serving reports whether this device currently answers punches as a target.
func (h *p2pHub) serving() bool { return len(h.incomingDevices()) > 0 }

// initiating reports whether this device currently has outgoing links.
func (h *p2pHub) initiating() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for dev := range h.channels {
		if h.outgoing[dev] {
			return true
		}
	}
	return false
}

func (h *p2pHub) incomingDevices() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.incoming))
	for dev := range h.incoming {
		out = append(out, dev)
	}
	return out
}

// ---- port mapping --------------------------------------------------------

// P2PMapPort binds a local TCP port to a port on the punched device. Data
// travels over the QUIC (UDP) channel; each accepted connection opens one
// stream that is released as soon as the connection closes.
func (a *App) P2PMapPort(deviceID string, localPort, remotePort int) ([]map[string]any, error) {
	ch, ok := a.p2p.get(deviceID)
	if !ok {
		return nil, errors.New("请先连接到该设备（打洞成功后才能映射端口）")
	}
	if localPort <= 0 || localPort > 65535 || remotePort <= 0 || remotePort > 65535 {
		return nil, errors.New("端口必须在 1-65535 之间")
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", localPort))
	if err != nil {
		return nil, fmt.Errorf("本地端口 %d 绑定失败: %w", localPort, err)
	}
	pm := &portMap{Local: localPort, Remote: remotePort, Device: deviceID, ln: ln}
	a.p2p.mu.Lock()
	if a.p2p.maps[deviceID] == nil {
		a.p2p.maps[deviceID] = map[int]*portMap{}
	}
	if old, ok := a.p2p.maps[deviceID][localPort]; ok && old.ln != nil {
		_ = old.ln.Close()
	}
	a.p2p.maps[deviceID][localPort] = pm
	a.p2p.mu.Unlock()
	go a.servePortMap(pm, ch)
	a.emit("p2p:maps", a.P2PMaps())
	return a.P2PMaps(), nil
}

// servePortMap accepts local connections and forwards each through one stream.
func (a *App) servePortMap(pm *portMap, ch *p2p.Channel) {
	remote := fmt.Sprintf("127.0.0.1:%d", pm.Remote)
	for {
		conn, err := pm.ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			stream, err := ch.DialTCP(ctx, remote)
			cancel()
			if err != nil {
				return
			}
			defer stream.Close() // one connection = one stream, released on close
			done := make(chan struct{})
			go func() {
				_, _ = io.Copy(stream, c)
				closeWrite(stream)
				close(done)
			}()
			_, _ = io.Copy(c, stream)
			<-done
		}(conn)
	}
}

// P2PUnmap removes a mapping and stops its listener.
func (a *App) P2PUnmap(deviceID string, localPort int) ([]map[string]any, error) {
	a.p2p.mu.Lock()
	if m, ok := a.p2p.maps[deviceID]; ok {
		if pm, ok := m[localPort]; ok {
			if pm.ln != nil {
				_ = pm.ln.Close()
			}
			delete(m, localPort)
		}
	}
	a.p2p.mu.Unlock()
	a.emit("p2p:maps", a.P2PMaps())
	return a.P2PMaps(), nil
}

// P2PMaps lists the active port mappings.
func (a *App) P2PMaps() []map[string]any {
	a.p2p.mu.Lock()
	defer a.p2p.mu.Unlock()
	out := []map[string]any{}
	for device, m := range a.p2p.maps {
		for _, pm := range m {
			out = append(out, map[string]any{
				"device": device, "local": pm.Local, "remote": pm.Remote,
			})
		}
	}
	return out
}

// closeWrite half-closes a stream when possible.
func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}
