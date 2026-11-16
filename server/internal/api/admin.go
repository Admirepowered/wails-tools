package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"wailstools/internal/wire"
	"wailstools/server/internal/auth"
	"wailstools/server/internal/store"
)

// maxDevicesPerUser caps how many distinct devices may share one account.
const maxDevicesPerUser = 5

// deviceTTL drops devices that have not logged in for this long, so an old
// machine can never permanently occupy a slot.
const deviceTTL = 30 * 24 * time.Hour

func (c *callCtx) requireAdmin() (store.User, error) {
	uid, err := c.userID()
	if err != nil {
		return store.User{}, err
	}
	u, err := c.api.DB.GetUser(uid)
	if err != nil {
		return store.User{}, wire.Errorf("unauthorized", "user no longer exists")
	}
	if u.Role != "admin" {
		return store.User{}, wire.Errorf("forbidden", "administrator only")
	}
	return u, nil
}

// ---- registration switch -------------------------------------------------

func (c *callCtx) adminSettingsGet() (any, error) {
	if _, err := c.requireAdmin(); err != nil {
		return nil, err
	}
	return map[string]any{
		"allowRegister": c.api.DB.AllowRegister(),
		"maxDevices":    maxDevicesPerUser,
	}, nil
}

func (c *callCtx) adminSettingsSet(body json.RawMessage) (any, error) {
	if _, err := c.requireAdmin(); err != nil {
		return nil, err
	}
	var req struct {
		AllowRegister *bool `json:"allowRegister"`
	}
	if err := wire.Decode(body, &req); err != nil {
		return nil, wire.Errorf("bad_request", "invalid body: %v", err)
	}
	if req.AllowRegister != nil {
		if err := c.api.DB.SetAllowRegister(*req.AllowRegister); err != nil {
			return nil, wire.Errorf("internal", "%v", err)
		}
	}
	return map[string]any{"ok": true, "allowRegister": c.api.DB.AllowRegister()}, nil
}

// ---- users ---------------------------------------------------------------

type adminUserView struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	Role        string `json:"role"`
	Disabled    bool   `json:"disabled"`
	Devices     int    `json:"devices"`
	QuotaBytes  int64  `json:"quotaBytes"`
	CreatedAt   string `json:"createdAt"`
	LastLoginAt string `json:"lastLoginAt,omitempty"`
}

func (c *callCtx) adminUsersList() (any, error) {
	if _, err := c.requireAdmin(); err != nil {
		return nil, err
	}
	users, err := c.api.DB.ListUsers()
	if err != nil {
		return nil, wire.Errorf("internal", "%v", err)
	}
	out := make([]adminUserView, 0, len(users))
	for _, u := range users {
		out = append(out, adminUserView{
			ID: u.ID, Username: u.Username, Role: u.Role, Disabled: u.Disabled,
			Devices: len(c.api.DB.ListDevices(u.ID)), QuotaBytes: u.QuotaBytes,
			CreatedAt: u.CreatedAt, LastLoginAt: u.LastLoginAt,
		})
	}
	return map[string]any{"users": out}, nil
}

func (c *callCtx) adminUsersCreate(body json.RawMessage) (any, error) {
	if _, err := c.requireAdmin(); err != nil {
		return nil, err
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := wire.Decode(body, &req); err != nil {
		return nil, wire.Errorf("bad_request", "invalid body: %v", err)
	}
	req.Username = strings.TrimSpace(req.Username)
	if err := auth.ValidateUsername(req.Username); err != nil {
		return nil, wire.Errorf("bad_request", "%v", err)
	}
	if err := auth.ValidatePassword(req.Password); err != nil {
		return nil, wire.Errorf("bad_request", "%v", err)
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		return nil, wire.Errorf("internal", "%v", err)
	}
	role := "user"
	if strings.EqualFold(strings.TrimSpace(req.Role), "admin") {
		role = "admin"
	}
	now := time.Now().UTC().Format(time.RFC3339)
	u, err := c.api.DB.CreateUser(store.User{
		ID:       newUserID(),
		Username: req.Username, PasswordHash: hash, DisplayName: req.Username,
		Role: role, QuotaBytes: c.api.Opts.DefaultQuota, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return nil, wire.Errorf("conflict", "%v", err)
	}
	return map[string]any{"ok": true, "id": u.ID}, nil
}

func (c *callCtx) adminUsersDelete(body json.RawMessage) (any, error) {
	me, err := c.requireAdmin()
	if err != nil {
		return nil, err
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := wire.Decode(body, &req); err != nil {
		return nil, wire.Errorf("bad_request", "invalid body: %v", err)
	}
	if req.ID == me.ID {
		return nil, wire.Errorf("bad_request", "不能删除自己")
	}
	_ = c.api.DB.DeleteUserTokens(req.ID)
	if err := c.api.DB.DeleteUser(req.ID); err != nil {
		return nil, wire.Errorf("not_found", "user not found")
	}
	c.api.DB.DeleteUserDevices(req.ID)
	return map[string]any{"ok": true}, nil
}

func (c *callCtx) adminUsersSetRole(body json.RawMessage) (any, error) {
	if _, err := c.requireAdmin(); err != nil {
		return nil, err
	}
	var req struct {
		ID   string `json:"id"`
		Role string `json:"role"`
	}
	if err := wire.Decode(body, &req); err != nil {
		return nil, wire.Errorf("bad_request", "invalid body: %v", err)
	}
	u, err := c.api.DB.GetUser(req.ID)
	if err != nil {
		return nil, wire.Errorf("not_found", "user not found")
	}
	switch strings.ToLower(strings.TrimSpace(req.Role)) {
	case "admin", "user":
		u.Role = strings.ToLower(strings.TrimSpace(req.Role))
	default:
		return nil, wire.Errorf("bad_request", "role must be admin or user")
	}
	u.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := c.api.DB.UpdateUser(u); err != nil {
		return nil, wire.Errorf("internal", "%v", err)
	}
	return map[string]any{"ok": true}, nil
}

// ---- devices -------------------------------------------------------------

func (c *callCtx) adminDevicesList(body json.RawMessage) (any, error) {
	if _, err := c.requireAdmin(); err != nil {
		return nil, err
	}
	var req struct {
		UserID string `json:"userId"`
	}
	_ = wire.Decode(body, &req)
	return map[string]any{"devices": c.api.DB.ListDevices(req.UserID)}, nil
}

func (c *callCtx) adminDevicesRemove(body json.RawMessage) (any, error) {
	if _, err := c.requireAdmin(); err != nil {
		return nil, err
	}
	var req struct {
		UserID   string `json:"userId"`
		DeviceID string `json:"deviceId"`
	}
	if err := wire.Decode(body, &req); err != nil {
		return nil, wire.Errorf("bad_request", "invalid body: %v", err)
	}
	if err := c.api.DB.DeleteDevice(req.UserID, req.DeviceID); err != nil {
		return nil, wire.Errorf("not_found", "device not found")
	}
	return map[string]any{"ok": true}, nil
}

// ---- own devices (any authenticated user) --------------------------------

func (c *callCtx) adminDevicesSelf(body json.RawMessage) (any, error) {
	uid, err := c.userID()
	if err != nil {
		return nil, err
	}
	// Optional body: the caller identifies itself so a device row that was
	// replaced (e.g. by a CLI login from the same machine) is re-registered.
	var req struct {
		DeviceID   string `json:"deviceId"`
		DeviceName string `json:"deviceName"`
		MachineID  string `json:"machineId"`
	}
	if len(body) > 0 {
		_ = wire.Decode(body, &req)
	}
	if id := strings.TrimSpace(req.DeviceID); id != "" {
		known := false
		for _, d := range c.api.DB.ListDevices(uid) {
			if d.ID == id {
				known = true
				break
			}
		}
		if !known {
			_ = c.registerDevice(uid, id, req.DeviceName, req.MachineID)
		}
	}
	return map[string]any{
		"devices":    c.api.DB.ListDevices(uid),
		"maxDevices": maxDevicesPerUser,
	}, nil
}

func (c *callCtx) adminDevicesSelfRemove(body json.RawMessage) (any, error) {
	uid, err := c.userID()
	if err != nil {
		return nil, err
	}
	var req struct {
		DeviceID string `json:"deviceId"`
	}
	if err := wire.Decode(body, &req); err != nil {
		return nil, wire.Errorf("bad_request", "invalid body: %v", err)
	}
	if err := c.api.DB.DeleteDevice(uid, req.DeviceID); err != nil {
		return nil, wire.Errorf("not_found", "device not found")
	}
	return map[string]any{"ok": true}, nil
}

// ---- device registration on login ---------------------------------------

// registerDevice enforces the per-user device cap and updates last-seen. It
// also collapses duplicates that belong to the same physical machine, so
// reinstalling the client or clearing its settings never eats a second slot.
func (c *callCtx) registerDevice(userID, deviceID, deviceName, machineID string) error {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return nil
	}
	deviceName = strings.TrimSpace(deviceName)
	machineID = strings.TrimSpace(machineID)

	// CLI helper tools (wt-admin, wt-publish, probes) never own a device slot.
	if strings.HasPrefix(deviceID, "wt-") {
		for _, d := range c.api.DB.ListDevices(userID) {
			if strings.HasPrefix(d.ID, "wt-") {
				_ = c.api.DB.DeleteDevice(userID, d.ID)
			}
		}
		return nil
	}
	// Clean leftovers from builds that did register CLI devices.
	for _, d := range c.api.DB.ListDevices(userID) {
		if strings.HasPrefix(d.ID, "wt-") {
			_ = c.api.DB.DeleteDevice(userID, d.ID)
		}
	}

	// Drop devices that have not logged in for a long time.
	cutoff := time.Now().UTC().Add(-deviceTTL)
	for _, d := range c.api.DB.ListDevices(userID) {
		if seen, err := time.Parse(time.RFC3339, d.LastSeen); err == nil && seen.Before(cutoff) {
			_ = c.api.DB.DeleteDevice(userID, d.ID)
		}
	}

	for _, d := range c.api.DB.ListDevices(userID) {
		if d.ID == deviceID {
			err := c.api.DB.TouchDevice(userID, deviceID, deviceName, machineID)
			// Every login refreshes the 9-digit numbers of the whole account so
			// legacy rows (and the row used by this login) always have one.
			_, _ = c.api.DB.EnsureDeviceNumbers(userID)
			return err
		}
	}

	// A login with the same machine fingerprint replaces the previous row; keep
	// its 9-digit number stable so a shared number does not change.
	reuseNumber := c.api.DB.FindDeviceNumberByMachine(userID, machineID)
	if machineID != "" {
		// Same machine, different id: keep only the current registration.
		if removed, _ := c.api.DB.DeleteDevicesByMachine(userID, machineID); len(removed) > 0 {
			c.api.DB.Audit(store.AuditEntry{UserID: userID, Action: "device-dedupe",
				Detail: fmt.Sprintf("machine %s replaced %v", machineID, removed)})
		}
		// Legacy rows created before the fingerprint existed: match the hostname.
		if deviceName != "" {
			for _, d := range c.api.DB.ListDevices(userID) {
				if d.Machine == "" && d.Name == deviceName {
					_ = c.api.DB.DeleteDevice(userID, d.ID)
				}
			}
		}
	}

	if len(c.api.DB.ListDevices(userID)) >= maxDevicesPerUser {
		return wire.Errorf("device_limit", "该账号已达 %d 台设备上限，请在管理页移除旧设备", maxDevicesPerUser)
	}
	if err := c.api.DB.AddDevice(userID, deviceID, deviceName, machineID); err != nil {
		return err
	}
	if reuseNumber != "" {
		_ = c.api.DB.SetDeviceNumber(userID, deviceID, reuseNumber)
	}
	// Make sure every device of this account has a 9-digit number; legacy rows
	// were created before the feature existed.
	_, _ = c.api.DB.EnsureDeviceNumbers(userID)
	return nil
}

var errNoUser = errors.New("no user")
