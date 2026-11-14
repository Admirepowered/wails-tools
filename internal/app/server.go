package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"wailstools/internal/serverlink"
	"wailstools/internal/settings"
)

// ServerStatus reports the client <-> server link state.
func (a *App) ServerStatus() serverlink.Status {
	st := a.link.Status()
	if a.sess.loggedIn && a.sess.user != "" {
		if st.User != nil {
			// Keep the server-supplied fields (quota etc.) and refresh the role
			// from the session, which is what gates the admin UI.
			u := *st.User
			u.Role = a.sess.role
			if u.Username == "" {
				u.Username = a.sess.user
			}
			st.User = &u
		} else {
			st.User = &serverlink.User{Username: a.sess.user, Role: a.sess.role}
		}
	}
	return st
}

// ConnectServer dials the configured server (UDP first, TCP fallback).
func (a *App) ConnectServer() (serverlink.Status, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return a.link.Connect(ctx)
}

// SaveServerEndpoint updates host/port and transport preferences.
func (a *App) SaveServerEndpoint(host string, port int, preferUDP, disableUDP bool) (settings.Settings, error) {
	return a.settings.Update(func(s *settings.Settings) error {
		s.Server.Host = strings.TrimSpace(host)
		s.Server.Port = port
		s.Server.PreferUDP = preferUDP
		s.Server.DisableUDP = disableUDP
		return nil
	})
}

// RegisterServer creates an account on the server.
func (a *App) RegisterServer(username, password, email, displayName string) (serverlink.Status, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if !a.link.Available() {
		if _, err := a.link.Connect(ctx); err != nil {
			return serverlink.Status{}, err
		}
	}
	st, err := a.link.Register(ctx, strings.TrimSpace(username), password, strings.TrimSpace(email), strings.TrimSpace(displayName))
	if err == nil {
		a.sess.loggedIn = true
		a.sess.user = strings.TrimSpace(username)
		a.sess.role = ""
		if st.User != nil {
			a.setSessionUser(*st.User)
		}
		a.refreshDeviceCount()
		a.rememberPassword(username, password)
		a.scheduleFullSync()
		if a.onLogin != nil {
			a.onLogin()
		}
	}
	return st, err
}

// LoginServer authenticates against the server.
func (a *App) LoginServer(username, password string) (serverlink.Status, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if !a.link.Available() {
		if _, err := a.link.Connect(ctx); err != nil {
			return serverlink.Status{}, err
		}
	}
	st, err := a.link.Login(ctx, strings.TrimSpace(username), password)
	if err == nil {
		a.sess.loggedIn = true
		a.sess.user = strings.TrimSpace(username)
		a.sess.role = ""
		if st.User != nil {
			a.setSessionUser(*st.User)
		}
		a.refreshDeviceCount()
		// The secret bundle key is derived from the raw password; remember it
		// locally (sealed) and sync the encrypted bundle.
		a.rememberPassword(username, password)
		a.scheduleFullSync()
		if a.onLogin != nil {
			a.onLogin()
		}
	}
	return st, err
}

// LogoutServer clears the session.
func (a *App) LogoutServer() error {
	a.sess.loggedIn = false
	a.sess.user = ""
	a.sess.role = ""
	a.sess.deviceCount = 0
	a.vaultState.clear()
	if a.onLogout != nil {
		a.onLogout()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return a.link.Logout(ctx)
}

// ChangeServerPassword rotates the account password.
//
// The server drops the encrypted secret bundles it stored, because they are
// keyed by the old password; the caller is warned by the UI before this runs.
func (a *App) ChangeServerPassword(oldPassword, newPassword string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var out map[string]any
	if err := a.link.Call(ctx, "auth.changePassword", map[string]any{
		"oldPassword": oldPassword, "newPassword": newPassword,
	}, &out); err != nil {
		return nil, err
	}
	// Everything encrypted with the old password is gone now.
	wiped := a.wipeSecretsAfterPasswordChange()
	for k, v := range wiped {
		out[k] = v
	}
	if user := strings.TrimSpace(a.sess.user); user != "" {
		a.rememberPassword(user, newPassword)
		if a.settings.Get().SecretSync.Enabled && a.link.Available() {
			// Push a fresh (metadata-only) bundle under the new key.
			go func() { _, _ = a.SyncSecrets() }()
		}
	}
	return out, nil
}

// RemoteServerInfo fetches public server metadata.
func (a *App) RemoteServerInfo() (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return a.link.ServerInfo(ctx)
}

// RefreshServerUser reloads the account record.
func (a *App) RefreshServerUser() (serverlink.User, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return a.link.Me(ctx)
}

// ---- server-side file storage --------------------------------------------

// ListServerFiles lists the user's cloud file store.
func (a *App) ListServerFiles(p string) ([]serverlink.RemoteEntry, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return a.link.ListRemote(ctx, p)
}

// MkdirServerFile creates a directory in the cloud store.
func (a *App) MkdirServerFile(p string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return a.link.MkdirRemote(ctx, p)
}

// RemoveServerFile deletes a cloud path.
func (a *App) RemoveServerFile(p string, recursive bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return a.link.RemoveRemote(ctx, p, recursive)
}

// RenameServerFile renames a cloud path.
func (a *App) RenameServerFile(from, to string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return a.link.RenameRemote(ctx, from, to)
}

// ServerUsage reports quota consumption.
func (a *App) ServerUsage() (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	used, quota, err := a.link.Usage(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"usedBytes": used, "quotaBytes": quota}, nil
}

// UploadToServer uploads local files into the cloud store.
func (a *App) UploadToServer(localPaths []string, remoteDir string) (map[string]any, error) {
	if len(localPaths) == 0 {
		return nil, errors.New("no files selected")
	}
	ctx := context.Background()
	results := make([]map[string]any, 0, len(localPaths))
	for _, lp := range localPaths {
		info, err := os.Stat(lp)
		if err != nil {
			results = append(results, map[string]any{"local": lp, "error": err.Error()})
			continue
		}
		if info.IsDir() {
			err := filepath.Walk(lp, func(p string, fi os.FileInfo, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				rel, _ := filepath.Rel(filepath.Dir(lp), p)
				remote := serverlink.JoinRemotePath(remoteDir, filepath.ToSlash(rel))
				if fi.IsDir() {
					return a.link.MkdirRemote(ctx, remote)
				}
				_, err := a.startCloudUpload(p, remote)
				return err
			})
			results = append(results, map[string]any{"local": lp, "error": errStr(err)})
			continue
		}
		remote := serverlink.JoinRemotePath(remoteDir, filepath.ToSlash(filepath.Base(lp)))
		id, err := a.startCloudUpload(lp, remote)
		if err != nil {
			results = append(results, map[string]any{"local": lp, "error": err.Error()})
			continue
		}
		results = append(results, map[string]any{"local": lp, "remote": remote, "transferId": id})
	}
	return map[string]any{"results": results}, nil
}

// DownloadFromServer downloads a cloud file to a local directory.
func (a *App) DownloadFromServer(remotePath, localDir string) (map[string]any, error) {
	if localDir == "" {
		localDir = a.settings.Get().Downloads.Directory
	}
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		return nil, err
	}
	target := filepath.Join(localDir, filepath.Base(remotePath))
	if _, err := os.Stat(target); err == nil {
		ext := filepath.Ext(target)
		base := strings.TrimSuffix(filepath.Base(target), ext)
		target = filepath.Join(localDir, fmt.Sprintf("%s-%s%s", base, strconv.FormatInt(time.Now().Unix(), 10), ext))
	}
	id, err := a.startCloudDownload(remotePath, target)
	if err != nil {
		return nil, err
	}
	return map[string]any{"transferId": id, "path": target}, nil
}

// RelayStatus reports whether SSH-via-server is available.
func (a *App) RelayStatus() map[string]any {
	return map[string]any{
		"available": a.link.Available(),
		"detail":    a.link.Describe(),
	}
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
