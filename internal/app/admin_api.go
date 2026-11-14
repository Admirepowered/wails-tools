package app

import (
	"context"
	"errors"
	"time"

	"wailstools/internal/serverlink"
)

var errNotConnected = errors.New("同步服务器未连接")

// AdminUser is one row of the admin user table.
type AdminUser struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	Role        string `json:"role"`
	Disabled    bool   `json:"disabled"`
	Devices     int    `json:"devices"`
	QuotaBytes  int64  `json:"quotaBytes"`
	CreatedAt   string `json:"createdAt"`
	LastLoginAt string `json:"lastLoginAt,omitempty"`
}

// AdminDevice is one registered client device.
type AdminDevice struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	LastSeen string `json:"lastSeen"`
}

// DeviceSummary is the caller's own device list.
type DeviceSummary struct {
	Devices    []AdminDevice `json:"devices"`
	MaxDevices int           `json:"maxDevices"`
}

// setSessionUser records the authenticated identity for status queries.
func (a *App) setSessionUser(u serverlink.User) {
	if u.Username != "" {
		a.sess.user = u.Username
	}
	a.sess.role = u.Role
}

// refreshDeviceCount caches how many devices the account currently uses.
func (a *App) refreshDeviceCount() {
	if a.link == nil || !a.link.Available() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out struct {
		Devices []AdminDevice `json:"devices"`
	}
	if err := a.link.Call(ctx, "admin.devices.self", nil, &out); err == nil {
		a.sess.deviceCount = len(out.Devices)
	}
}

func (a *App) adminCall(method string, body any, out any) error {
	if a.link == nil || !a.link.Available() {
		return errNotConnected
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return a.link.Call(ctx, method, body, out)
}

// ---- own devices ---------------------------------------------------------

// MyDevices lists the devices currently bound to the logged-in account.
func (a *App) MyDevices() (DeviceSummary, error) {
	var out DeviceSummary
	if err := a.adminCall("admin.devices.self", nil, &out); err != nil {
		return DeviceSummary{}, err
	}
	a.sess.deviceCount = len(out.Devices)
	return out, nil
}

// RemoveMyDevice unbinds one of the caller's own devices.
func (a *App) RemoveMyDevice(deviceID string) error {
	var out map[string]any
	if err := a.adminCall("admin.devices.selfRemove", map[string]any{"deviceId": deviceID}, &out); err != nil {
		return err
	}
	a.sess.deviceCount = a.sess.deviceCount - 1
	if a.sess.deviceCount < 0 {
		a.sess.deviceCount = 0
	}
	return nil
}

// ---- admin: registration switch -----------------------------------------

// AdminSettingsInfo returns server admin settings.
func (a *App) AdminSettingsInfo() (map[string]any, error) {
	out := map[string]any{}
	if err := a.adminCall("admin.settings.get", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// AdminSetRegistration toggles public registration.
func (a *App) AdminSetRegistration(allow bool) (map[string]any, error) {
	out := map[string]any{}
	if err := a.adminCall("admin.settings.set", map[string]any{"allowRegister": allow}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ---- admin: users --------------------------------------------------------

// AdminUsers lists every account on the server.
func (a *App) AdminUsers() ([]AdminUser, error) {
	var out struct {
		Users []AdminUser `json:"users"`
	}
	if err := a.adminCall("admin.users.list", nil, &out); err != nil {
		return nil, err
	}
	return out.Users, nil
}

// AdminCreateUser creates an account (bypasses the registration switch).
func (a *App) AdminCreateUser(username, password, role string) (map[string]any, error) {
	out := map[string]any{}
	if err := a.adminCall("admin.users.create", map[string]any{
		"username": username, "password": password, "role": role,
	}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// AdminDeleteUser removes an account.
func (a *App) AdminDeleteUser(id string) error {
	var out map[string]any
	return a.adminCall("admin.users.delete", map[string]any{"id": id}, &out)
}

// AdminSetUserRole promotes or demotes an account.
func (a *App) AdminSetUserRole(id, role string) error {
	var out map[string]any
	return a.adminCall("admin.users.setRole", map[string]any{"id": id, "role": role}, &out)
}

// AdminDevices lists the devices bound to one account.
func (a *App) AdminDevices(userID string) ([]AdminDevice, error) {
	var out struct {
		Devices []AdminDevice `json:"devices"`
	}
	if err := a.adminCall("admin.devices.list", map[string]any{"userId": userID}, &out); err != nil {
		return nil, err
	}
	return out.Devices, nil
}

// AdminRemoveDevice unbinds a device from any account.
func (a *App) AdminRemoveDevice(userID, deviceID string) error {
	var out map[string]any
	return a.adminCall("admin.devices.remove", map[string]any{"userId": userID, "deviceId": deviceID}, &out)
}
