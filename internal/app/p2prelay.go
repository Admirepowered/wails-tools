package app

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"wailstools/internal/p2p"
)

// relaySecret derives a per-account secret for relayed sessions (the relayed
// path never uses the punched session secret).
func (a *App) relaySecret() string {
	return "relay:" + a.localDeviceID()
}

// startRelayServer parks a bridge stream on the sync server so other devices of
// this account can reach us through it. Started after login.
func (a *App) startRelayServer() {
	link := a.link
	if link == nil {
		return
	}
	a.relayOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		a.relayCancel = cancel
		go p2p.RelayServe(ctx, func(ctx context.Context, method string, body any) (net.Conn, func(), error) {
			return link.OpenStream(ctx, method, body)
		}, a.relaySecret(), a.localDeviceID(), func(ch *p2p.Channel) {
			a.p2pLogf("p2p: relay bridge established (peer %s)", ch.Peer())
			a.p2p.put("relay-peer", ch)
			go ch.Serve(context.Background(), &p2pWireHandler{app: a, trust: true})
			a.emit("p2p:connected", map[string]any{"device": "relay-peer", "relayed": true})
		})
	})
}

// P2PRelayConnect connects to a device through the sync server instead of
// punching. Higher latency, but works behind symmetric NAT.
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
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	channel, err := p2p.RelayDial(ctx, func(ctx context.Context, method string, body any) (net.Conn, func(), error) {
		return link.OpenStream(ctx, method, body)
	}, a.relaySecret(), deviceID)
	if err != nil {
		a.p2p.fail(deviceID, err)
		return nil, err
	}
	a.p2p.put(deviceID, channel)
	a.p2p.markOutgoing(deviceID)
	go channel.Serve(context.Background(), &p2pWireHandler{app: a, trust: true})
	a.heartbeat(deviceID, channel)
	a.p2p.watch(deviceID, channel, func(dev string) {
		a.p2pLogf("p2p: relay link to %s lost", dev)
		a.emit("p2p:disconnected", map[string]any{"device": dev, "relayed": true})
	})
	a.emit("p2p:connected", map[string]any{"device": deviceID, "relayed": true})
	return map[string]any{"device": deviceID, "relayed": true}, nil
}
