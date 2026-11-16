package app

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"wailstools/internal/p2p"
)

// relaySecret derives the shared secret for relayed sessions: both devices of an
// account must compute the same value (a device id would break authentication).
func (a *App) relaySecret() string {
	who := strings.ToLower(strings.TrimSpace(a.sess.user))
	return p2p.BridgeSecret(who)
}

// startRelayServer parks a bridge stream on the sync server so the account's
// other devices can reach this one through the server. Started after login.
func (a *App) startRelayServer() {
	link := a.link
	if link == nil {
		return
	}
	a.relayOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		a.relayCancel = cancel
		open := func(ctx context.Context, method string, body any) (net.Conn, func(), error) {
			return link.OpenStream(ctx, method, body)
		}
		go p2p.RelayServe(ctx, open, a.relaySecret(), a.localDeviceID(), func(ch *p2p.Channel) {
			a.p2pLogf("p2p: relay paired (peer %s)", ch.Peer())
			go ch.Serve(context.Background(), &p2pWireHandler{app: a})
		})
	})
}

// P2PRelayConnect reaches a device through the sync server instead of punching.
func (a *App) P2PRelayConnect(deviceID string) (map[string]any, error) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return nil, errors.New("请选择设备")
	}
	link, err := a.requireLink()
	if err != nil {
		return nil, err
	}
	if ch, ok := a.p2p.get(deviceID); ok {
		probeCtx, probeCancel := context.WithTimeout(context.Background(), 6*time.Second)
		probeErr := ch.Ping(probeCtx)
		probeCancel()
		if probeErr == nil && ch.Alive() {
			return map[string]any{"device": deviceID, "already": true, "relayed": ch.Relayed()}, nil
		}
		a.p2p.drop(deviceID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	open := func(ctx context.Context, method string, body any) (net.Conn, func(), error) {
		return link.OpenStream(ctx, method, body)
	}
	channel, err := p2p.RelayDial(ctx, open, a.relaySecret(), deviceID)
	if err != nil {
		a.p2p.fail(deviceID, err)
		a.emit("p2p:error", map[string]any{"device": deviceID, "error": err.Error()})
		return nil, err
	}
	a.p2p.put(deviceID, channel)
	a.p2p.markOutgoing(deviceID)
	go channel.Serve(context.Background(), &p2pWireHandler{app: a})
	a.heartbeat(deviceID, channel)
	a.p2p.watch(deviceID, channel, func(dev string) {
		a.p2pLogf("p2p: relay link to %s lost", dev)
		a.emit("p2p:disconnected", map[string]any{"device": dev, "relayed": true})
	})
	a.emit("p2p:connected", map[string]any{"device": deviceID, "relayed": true})
	return map[string]any{"device": deviceID, "relayed": true}, nil
}
