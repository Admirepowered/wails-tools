package api

import (
	"encoding/json"
	"strings"

	"wailstools/internal/wire"
	"wailstools/server/internal/store"
)

// ---- NAT hole punching signalling ---------------------------------------
//
// The server only introduces two devices of the same account and stores the
// shared secret; the UDP path is negotiated directly between the clients.

// p2pRequest invites another device (looked up by its 9-digit number) to a
// punch. The target must explicitly accept before endpoints are exchanged.
func (c *callCtx) p2pRequest(body json.RawMessage) (any, error) {
	uid, err := c.userID()
	if err != nil {
		return nil, err
	}
	if c.api.P2P == nil {
		return nil, wire.Errorf("unsupported", "this server has no p2p rendezvous enabled")
	}
	var req struct {
		Number       string `json:"number"`
		Device       string `json:"device"`
		TargetDevice string `json:"targetDevice"`
	}
	if err := wire.Decode(body, &req); err != nil {
		return nil, wire.Errorf("bad_request", "invalid body: %v", err)
	}
	me := strings.TrimSpace(req.Device)
	number := strings.TrimSpace(req.Number)
	if me == "" {
		return nil, wire.Errorf("bad_request", "缺少当前设备")
	}
	if number == "" && strings.TrimSpace(req.TargetDevice) == "" {
		return nil, wire.Errorf("bad_request", "设备号不能为空")
	}
	myDev, meFound := findDevice(c.api.DB.ListDevices(uid), me)
	if !meFound {
		return nil, wire.Errorf("not_found", "当前设备未登记，请重新登录后再试")
	}
	if myDev.Number == "" {
		_, _ = c.api.DB.EnsureDeviceNumbers(uid)
		if devs, err := c.api.DB.EnsureDeviceNumbers(uid); err == nil {
			if d, ok := findDevice(devs, me); ok {
				myDev = d
			}
		}
	}
	var targetUser string
	var targetDev store.Device
	var ok bool
	if number != "" {
		targetUser, targetDev, ok = c.api.DB.FindDeviceByNumber(number)
		if !ok {
			return nil, wire.Errorf("not_found", "没有找到设备号 %s 对应的设备", number)
		}
	} else {
		// Direct device id: used by "connect to this device" from the list.
		targetID := strings.TrimSpace(req.TargetDevice)
		targetUser, targetDev, ok = c.api.DB.FindDeviceByID(targetID)
		if !ok {
			return nil, wire.Errorf("not_found", "设备不存在，请刷新设备列表")
		}
		if targetDev.Number == "" {
			_, _ = c.api.DB.EnsureDeviceNumbers(targetUser)
			if _, updated, ok2 := c.api.DB.FindDeviceByID(targetDev.ID); ok2 {
				targetDev = updated
			}
		}
	}
	if targetDev.ID == me {
		return nil, wire.Errorf("bad_request", "不能穿透自己")
	}
	info, err := c.api.P2P.CreateRequest(uid, me, myDev.Number, myDev.Name, targetUser, targetDev.ID)
	if err != nil {
		return nil, wire.Errorf("p2p_role", "%v", err)
	}
	return map[string]any{"request": info}, nil
}

// p2pRequests lists pending invitations for a device.
func (c *callCtx) p2pRequests(body json.RawMessage) (any, error) {
	uid, err := c.userID()
	if err != nil {
		return nil, err
	}
	var req struct {
		Device string `json:"device"`
	}
	_ = wire.Decode(body, &req)
	device := strings.TrimSpace(req.Device)
	if c.api.P2P == nil || device == "" {
		return map[string]any{"requests": []any{}}, nil
	}
	return map[string]any{"requests": c.api.P2P.Requests(uid, device)}, nil
}

// p2pRespond accepts or declines an invitation.
func (c *callCtx) p2pRespond(body json.RawMessage) (any, error) {
	uid, err := c.userID()
	if err != nil {
		return nil, err
	}
	if c.api.P2P == nil {
		return nil, wire.Errorf("unsupported", "this server has no p2p rendezvous enabled")
	}
	var req struct {
		Device  string `json:"device"`
		Request string `json:"request"`
		Accept  bool   `json:"accept"`
	}
	if err := wire.Decode(body, &req); err != nil {
		return nil, wire.Errorf("bad_request", "invalid body: %v", err)
	}
	info, err := c.api.P2P.Respond(uid, strings.TrimSpace(req.Device), strings.TrimSpace(req.Request), req.Accept)
	if err != nil {
		return nil, wire.Errorf("p2p", "%v", err)
	}
	return map[string]any{"request": info, "p2pPort": c.api.Opts.P2PPort}, nil
}

// p2pRequestState lets the initiator poll until the target answers.
func (c *callCtx) p2pRequestState(body json.RawMessage) (any, error) {
	uid, err := c.userID()
	if err != nil {
		return nil, err
	}
	var req struct {
		Device  string `json:"device"`
		Request string `json:"request"`
	}
	_ = wire.Decode(body, &req)
	if c.api.P2P == nil {
		return map[string]any{"found": false}, nil
	}
	info, ok := c.api.P2P.RequestState(uid, strings.TrimSpace(req.Device), strings.TrimSpace(req.Request))
	if !ok {
		return map[string]any{"found": false}, nil
	}
	return map[string]any{"found": true, "request": info, "p2pPort": c.api.Opts.P2PPort}, nil
}

// p2pCancel withdraws an invitation.
func (c *callCtx) p2pCancel(body json.RawMessage) (any, error) {
	uid, err := c.userID()
	if err != nil {
		return nil, err
	}
	var req struct {
		Device  string `json:"device"`
		Request string `json:"request"`
	}
	_ = wire.Decode(body, &req)
	if c.api.P2P != nil {
		c.api.P2P.CancelRequest(uid, strings.TrimSpace(req.Device), strings.TrimSpace(req.Request))
	}
	return map[string]any{"ok": true}, nil
}

// findDevice locates one of the user's devices by id.
func findDevice(list []store.Device, id string) (store.Device, bool) {
	for _, d := range list {
		if d.ID == id {
			return d, true
		}
	}
	return store.Device{}, false
}

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
