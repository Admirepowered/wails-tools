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
func (a *App) ServerStatus() serverlink.Status { return a.link.Status() }

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
	return a.link.Register(ctx, strings.TrimSpace(username), password, strings.TrimSpace(email), strings.TrimSpace(displayName))
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
	return a.link.Login(ctx, strings.TrimSpace(username), password)
}

// LogoutServer clears the session.
func (a *App) LogoutServer() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return a.link.Logout(ctx)
}

// ChangeServerPassword rotates the account password.
func (a *App) ChangeServerPassword(oldPassword, newPassword string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var out map[string]any
	if err := a.link.Call(ctx, "auth.changePassword", map[string]any{
		"oldPassword": oldPassword, "newPassword": newPassword,
	}, &out); err != nil {
		return nil, err
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
				key := "upload:" + p
				n, err := a.link.Upload(ctx, p, remote, func(done, total int64) {
					a.emit("transfer:progress", map[string]any{"key": key, "kind": "cloud-upload", "done": done, "total": total})
				})
				if err == nil {
					a.emit("transfer:done", map[string]any{"key": key, "kind": "cloud-upload", "path": remote, "bytes": n})
				}
				return err
			})
			results = append(results, map[string]any{"local": lp, "error": errStr(err)})
			continue
		}
		remote := serverlink.JoinRemotePath(remoteDir, filepath.ToSlash(filepath.Base(lp)))
		key := "upload:" + lp
		n, err := a.link.Upload(ctx, lp, remote, func(done, total int64) {
			a.emit("transfer:progress", map[string]any{"key": key, "kind": "cloud-upload", "done": done, "total": total})
		})
		if err != nil {
			results = append(results, map[string]any{"local": lp, "error": err.Error()})
			continue
		}
		a.emit("transfer:done", map[string]any{"key": key, "kind": "cloud-upload", "path": remote, "bytes": n})
		results = append(results, map[string]any{"local": lp, "remote": remote, "bytes": n})
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
	ctx := context.Background()
	key := "cloud:" + remotePath
	n, err := a.link.Download(ctx, remotePath, target, func(done, total int64) {
		a.emit("transfer:progress", map[string]any{"key": key, "kind": "cloud-download", "done": done, "total": total})
	})
	if err != nil {
		return nil, err
	}
	a.emit("transfer:done", map[string]any{"key": key, "kind": "cloud-download", "path": target, "bytes": n})
	return map[string]any{"bytes": n, "path": target}, nil
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
