package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"wailstools/internal/settings"
	"wailstools/internal/vaultsync"
)

// Secret sync: SSH hosts, SSH private keys and FTP profiles are packed into one
// bundle, encrypted on this machine with a key derived from the account
// password (Argon2id) and stored on the server as ciphertext only.

var errNoVaultKey = errors.New("密钥尚未就绪：需要先输入账号密码（密钥由原始密码派生）")

// vaultState holds the derived key in memory for the session.
type vaultState struct {
	mu    sync.Mutex
	key   []byte
	user  string
	timer *time.Timer
}

func (v *vaultState) set(key []byte, user string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.key = key
	v.user = user
}

func (v *vaultState) get() ([]byte, string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.key) != 32 {
		return nil, v.user
	}
	return append([]byte(nil), v.key...), v.user
}

func (v *vaultState) clear() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.key = nil
	v.user = ""
}

// ---- password handling (local only) --------------------------------------

// rememberPassword derives the bundle key from the raw password and keeps a
// sealed copy on this machine so auto-login can still open the bundle.
func (a *App) rememberPassword(username, password string) {
	username = strings.TrimSpace(username)
	if password == "" || username == "" {
		return
	}
	key := vaultsync.DeriveKey(username, password)
	a.vaultState.set(key, username)
	sealed, err := a.vault.Seal(password)
	if err != nil {
		return
	}
	_, _ = a.settings.Update(func(s *settings.Settings) error {
		s.Server.Username = username
		s.Server.PasswordSealed = sealed
		return nil
	})
}

// restoreVaultKey re-derives the bundle key from the locally sealed password.
func (a *App) restoreVaultKey() bool {
	cfg := a.settings.Get()
	user := strings.TrimSpace(cfg.Server.Username)
	sealed := cfg.Server.PasswordSealed
	if user == "" || sealed == "" {
		return false
	}
	password, err := a.vault.Reveal(sealed)
	if err != nil || password == "" {
		return false
	}
	a.vaultState.set(vaultsync.DeriveKey(user, password), user)
	return true
}

// forgetLocalPassword drops the sealed password and the derived key.
func (a *App) forgetLocalPassword() error {
	a.vaultState.clear()
	_, err := a.settings.Update(func(s *settings.Settings) error {
		s.Server.PasswordSealed = ""
		return nil
	})
	return err
}

// ---- bundle packing ------------------------------------------------------

func (a *App) localSecretBundle() vaultsync.Bundle {
	cfg := a.settings.Get()
	b := vaultsync.Bundle{
		Username:  strings.TrimSpace(cfg.Server.Username),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if a.ssh != nil && a.ssh.Store != nil {
		b.Hosts = a.ssh.Store.ExportHosts()
		b.Keys = a.ssh.Store.ExportKeys()
		b.KnownHosts = a.ssh.Store.KnownHosts()
	}
	if a.ftp != nil && a.ftp.store != nil {
		b.FTP = a.ftp.store.Export()
	}
	if a.netdisk != nil {
		if accounts, cookies, err := a.netdisk.ExportAll(); err == nil {
			b.Netdisk = accounts
			b.NetdiskCookies = cookies
		}
	}
	return b
}

func (a *App) applySecretBundle(b vaultsync.Bundle) map[string]int {
	out := map[string]int{"hosts": 0, "keys": 0, "ftp": 0, "knownHosts": 0}
	if a.ssh != nil && a.ssh.Store != nil {
		if n, err := a.ssh.Store.ApplyHosts(b.Hosts); err == nil {
			out["hosts"] = n
		}
		if n, err := a.ssh.Store.ApplyKeys(b.Keys); err == nil {
			out["keys"] = n
		}
		known := a.ssh.Store.KnownHosts()
		for host, fp := range b.KnownHosts {
			if fp == "" || known[host] == fp {
				continue
			}
			if err := a.ssh.Store.SetKnownHostFingerprint(host, fp); err == nil {
				out["knownHosts"]++
			}
		}
	}
	if a.ftp != nil && a.ftp.store != nil {
		if n, err := a.ftp.store.Apply(b.FTP); err == nil {
			out["ftp"] = n
		}
	}
	if a.netdisk != nil && (len(b.Netdisk) > 0 || len(b.NetdiskCookies) > 0) {
		if n, err := a.netdisk.ImportAll(b.Netdisk, b.NetdiskCookies); err == nil {
			out["netdisk"] = n
		}
	}
	return out
}

// ---- sync ----------------------------------------------------------------

// SecretSyncStatus reports the state of the encrypted secret sync.
func (a *App) SecretSyncStatus() map[string]any {
	cfg := a.settings.Get()
	key, user := a.vaultState.get()
	hosts, keys, ftp, netdiskCount := 0, 0, 0, 0
	if a.ssh != nil && a.ssh.Store != nil {
		hosts = len(a.ssh.Store.ListHosts())
		keys = len(a.ssh.Store.ListKeys())
	}
	if a.ftp != nil && a.ftp.store != nil {
		ftp = len(a.ftp.store.List())
	}
	if a.netdisk != nil {
		netdiskCount = len(a.netdisk.ListAccounts())
	}
	return map[string]any{
		"enabled":     cfg.SecretSync.Enabled,
		"keyReady":    key != nil,
		"hasPassword": cfg.Server.PasswordSealed != "",
		"username":    user,
		"lastSync":    cfg.SecretSync.LastSync,
		"lastError":   cfg.SecretSync.LastError,
		"connected":   a.link != nil && a.link.Available(),
		"loggedIn":    a.sess.loggedIn,
		"hosts":       hosts,
		"keys":        keys,
		"ftp":         ftp,
		"netdisk":     netdiskCount,
	}
}

// SetSecretSyncEnabled toggles automatic secret sync.
func (a *App) SetSecretSyncEnabled(enabled bool) (map[string]any, error) {
	if _, err := a.settings.Update(func(s *settings.Settings) error {
		s.SecretSync.Enabled = enabled
		return nil
	}); err != nil {
		return nil, err
	}
	if enabled {
		a.scheduleSecretSync()
	}
	return a.SecretSyncStatus(), nil
}

// ForgetSavedPassword drops the locally stored password and the bundle key.
func (a *App) ForgetSavedPassword() (map[string]any, error) {
	if err := a.forgetLocalPassword(); err != nil {
		return nil, err
	}
	return a.SecretSyncStatus(), nil
}

// SyncSecretsNow performs a bidirectional merge of the encrypted bundle.
func (a *App) SyncSecretsNow() (map[string]any, error) {
	return a.SyncSecrets()
}

// UnlockSecretSync re-authenticates with the raw account password, stores it
// locally (sealed) and immediately derives the bundle key + syncs. Needed on
// installs that signed in with a token before the password was ever typed.
func (a *App) UnlockSecretSync(password string) (map[string]any, error) {
	if a.link == nil || !a.link.Available() {
		return nil, errNotConnected
	}
	user := strings.TrimSpace(a.sess.user)
	if user == "" {
		user = strings.TrimSpace(a.settings.Get().Server.Username)
	}
	if user == "" {
		return nil, errors.New("尚未登录服务器账号")
	}
	if strings.TrimSpace(password) == "" {
		return nil, errors.New("请输入账号密码")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if _, err := a.link.Login(ctx, user, password); err != nil {
		return nil, errors.New("密码不正确：" + err.Error())
	}
	a.rememberPassword(user, password)
	res, err := a.SyncSecrets()
	if err != nil {
		status := a.SecretSyncStatus()
		status["syncError"] = err.Error()
		return status, err
	}
	return res, nil
}

// SyncSecrets pulls the encrypted bundle, merges it into the local stores and
// pushes the merged result back.
func (a *App) SyncSecrets() (map[string]any, error) {
	key, user := a.vaultState.get()
	if key == nil {
		return nil, errNoVaultKey
	}
	if a.link == nil || !a.link.Available() {
		return nil, errNotConnected
	}
	cfg := a.settings.Get()
	if !cfg.SecretSync.Enabled {
		return nil, errors.New("密钥同步已在设置中关闭")
	}

	local := a.localSecretBundle()
	local.Username = user

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var got struct {
		Found     bool   `json:"found"`
		Data      string `json:"data"`
		UpdatedAt string `json:"updatedAt"`
	}
	if err := a.link.Call(ctx, "vault.get", map[string]any{"kind": vaultsync.KindBundle}, &got); err != nil {
		a.recordSecretSyncError(err)
		return nil, err
	}

	remote := vaultsync.Bundle{}
	if got.Found && strings.TrimSpace(got.Data) != "" {
		b, err := vaultsync.Open(key, got.Data)
		if err != nil {
			a.recordSecretSyncError(err)
			a.emit("secrets:error", err.Error())
			return nil, err
		}
		remote = b
	}

	merged := vaultsync.Merge(local, remote)
	applied := a.applySecretBundle(merged)

	pushed := got.Found && !vaultsync.Changed(merged, remote)
	if !got.Found || vaultsync.Changed(merged, remote) {
		blob, err := vaultsync.Seal(key, merged)
		if err != nil {
			a.recordSecretSyncError(err)
			return nil, err
		}
		var put map[string]any
		if err := a.link.Call(ctx, "vault.put", map[string]any{
			"kind": vaultsync.KindBundle, "data": blob,
		}, &put); err != nil {
			a.recordSecretSyncError(err)
			return nil, err
		}
		pushed = true
	}

	now := time.Now().UTC().Format(time.RFC3339)
	_, _ = a.settings.Update(func(s *settings.Settings) error {
		s.SecretSync.LastSync = now
		s.SecretSync.LastError = ""
		return nil
	})
	res := map[string]any{
		"ok": true, "pushed": pushed, "updatedAt": now,
		"applied": applied, "counts": merged.Counts(),
	}
	a.emit("secrets:synced", res)
	return res, nil
}

func (a *App) recordSecretSyncError(err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	_, _ = a.settings.Update(func(s *settings.Settings) error {
		s.SecretSync.LastError = msg
		return nil
	})
}

// scheduleSecretSync debounces automatic pushes after local edits.
func (a *App) scheduleSecretSync() {
	cfg := a.settings.Get()
	if !cfg.SecretSync.Enabled {
		return
	}
	if key, _ := a.vaultState.get(); key == nil {
		return
	}
	if a.link == nil || !a.link.Available() {
		return
	}
	a.vaultState.mu.Lock()
	if a.vaultState.timer != nil {
		a.vaultState.timer.Stop()
	}
	a.vaultState.timer = time.AfterFunc(3*time.Second, func() {
		if _, err := a.SyncSecrets(); err != nil {
			if a.log != nil {
				a.log.Printf("secret sync: %v", err)
			}
		}
	})
	a.vaultState.mu.Unlock()
}

// wipeSecretsAfterPasswordChange drops everything that was encrypted with the
// old password: saved host/FTP passwords and SSH private keys. The lists
// themselves (names, addresses, public keys) survive.
func (a *App) wipeSecretsAfterPasswordChange() map[string]any {
	out := map[string]any{}
	if a.ssh != nil && a.ssh.Store != nil {
		if n, err := a.ssh.Store.WipeSecrets(); err == nil {
			out["sshCleared"] = n
		}
	}
	if a.ftp != nil && a.ftp.store != nil {
		if n, err := a.ftp.store.WipeSecrets(); err == nil {
			out["ftpCleared"] = n
		}
	}
	a.vaultState.clear()
	_, _ = a.settings.Update(func(s *settings.Settings) error {
		s.Server.PasswordSealed = ""
		s.SecretSync.LastSync = ""
		s.SecretSync.LastError = ""
		return nil
	})
	out["passwordForgotten"] = true
	a.emit("secrets:cleared", out)
	return out
}
