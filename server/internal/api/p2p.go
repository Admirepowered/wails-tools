package api

import (
	"encoding/json"
	"strings"

	"wailstools/internal/wire"
)

// ---- NAT hole punching signalling ---------------------------------------
//
// The server only introduces two devices of the same account and stores the
// shared secret; the UDP path is negotiated directly between the clients.

func (c *callCtx) p2pOffer(body json.RawMessage) (any, error) {
	uid, err := c.userID()
	if err != nil {
		return nil, err
	}
	if c.api.P2P == nil {
		return nil, wire.Errorf("unsupported", "this server has no p2p rendezvous enabled")
	}
	var req struct {
		FromDevice string `json:"fromDevice"`
		ToDevice   string `json:"toDevice"`
	}
	if err := wire.Decode(body, &req); err != nil {
		return nil, wire.Errorf("bad_request", "invalid body: %v", err)
	}
	from := strings.TrimSpace(req.FromDevice)
	to := strings.TrimSpace(req.ToDevice)
	if from == "" || to == "" {
		return nil, wire.Errorf("bad_request", "fromDevice and toDevice are required")
	}
	devices := c.api.DB.ListDevices(uid)
	known := map[string]bool{}
	for _, d := range devices {
		known[d.ID] = true
	}
	if !known[from] {
		return nil, wire.Errorf("not_found", "当前设备未登记，请重新登录后再试")
	}
	if !known[to] {
		return nil, wire.Errorf("not_found", "目标设备不在该账号下")
	}
	info, err := c.api.P2P.CreateSession(uid, from, to)
	if err != nil {
		return nil, wire.Errorf("bad_request", "%v", err)
	}
	return map[string]any{
		"session": info.ID, "secret": info.Secret, "from": info.From, "to": info.To,
		"p2pPort": c.api.Opts.P2PPort,
	}, nil
}

func (c *callCtx) p2pPending(body json.RawMessage) (any, error) {
	uid, err := c.userID()
	if err != nil {
		return nil, err
	}
	if c.api.P2P == nil {
		return map[string]any{"sessions": []any{}}, nil
	}
	var req struct {
		Device string `json:"device"`
	}
	_ = wire.Decode(body, &req)
	device := strings.TrimSpace(req.Device)
	if device == "" {
		return map[string]any{"sessions": []any{}}, nil
	}
	return map[string]any{
		"sessions": c.api.P2P.Pending(uid, device),
		"p2pPort":  c.api.Opts.P2PPort,
	}, nil
}

func (c *callCtx) p2pDrop(body json.RawMessage) (any, error) {
	uid, err := c.userID()
	if err != nil {
		return nil, err
	}
	if c.api.P2P == nil {
		return map[string]any{"ok": true}, nil
	}
	var req struct {
		Session string `json:"session"`
	}
	if err := wire.Decode(body, &req); err != nil {
		return nil, wire.Errorf("bad_request", "invalid body: %v", err)
	}
	c.api.P2P.Drop(uid, req.Session)
	return map[string]any{"ok": true}, nil
}