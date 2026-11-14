package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FullSyncResult aggregates one "sync everything" pass.
type FullSyncResult struct {
	Notes       map[string]any `json:"notes"`
	Subs        map[string]any `json:"subs"`
	Secrets     map[string]any `json:"secrets"`
	Screenshots map[string]any `json:"screenshots"`
	Errors      []string       `json:"errors,omitempty"`
	NeedsUnlock bool           `json:"needsUnlock,omitempty"`
	Message     string         `json:"message"`
}

// SyncAllNow is the UI entry point for a full sync.
func (a *App) SyncAllNow() (FullSyncResult, error) {
	return a.FullSync()
}

// FullSync restores everything a fresh device needs: notes, subscriptions and
// custom proxy nodes, the encrypted secret bundle (SSH hosts/keys, FTP and
// netdisk accounts) and cloud screenshots.
func (a *App) FullSync() (FullSyncResult, error) {
	res := FullSyncResult{}
	if a.link == nil {
		return res, errNotConnected
	}
	if !a.link.Available() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := a.link.Connect(ctx); err != nil {
			return res, fmt.Errorf("未连接服务器: %w", err)
		}
	}
	if !a.sess.loggedIn && a.link.Token() == "" {
		return res, errors.New("请先登录同步服务器")
	}

	if notes, err := a.SyncNotes(); err == nil {
		res.Notes = map[string]any{"pushed": notes.Pushed, "pulled": notes.Pulled, "conflicts": notes.Conflicts}
	} else {
		res.Errors = append(res.Errors, "笔记: "+err.Error())
	}

	if subs, err := a.SyncSubscriptions(); err == nil {
		res.Subs = map[string]any{
			"sources": subs.Merged, "nodes": subs.NodesMerged,
			"remoteSources": subs.Remote, "remoteNodes": subs.NodesRemote,
		}
	} else {
		res.Errors = append(res.Errors, "订阅: "+err.Error())
	}

	if key, _ := a.vaultState.get(); key != nil {
		if secrets, err := a.SyncSecrets(); err == nil {
			res.Secrets = secrets
		} else {
			res.Errors = append(res.Errors, "密钥/网盘: "+err.Error())
		}
	} else {
		res.NeedsUnlock = true
	}

	if shots, err := a.pullCloudScreenshots(); err == nil {
		res.Screenshots = shots
	} else {
		res.Errors = append(res.Errors, "截图: "+err.Error())
	}

	if len(res.Errors) == 0 {
		res.Message = "全部同步完成"
		if res.NeedsUnlock {
			res.Message = "笔记/订阅/截图已同步；输入账号密码后可同步 SSH/FTP/网盘等加密数据"
		}
	} else {
		res.Message = "部分同步失败：" + strings.Join(res.Errors, "；")
	}
	a.emit("sync:done", res)
	return res, nil
}

// pullCloudScreenshots downloads screenshots that exist on the server but not
// on this device, so a fresh install sees the whole gallery.
func (a *App) pullCloudScreenshots() (map[string]any, error) {
	out := map[string]any{"pulled": 0, "skipped": 0}
	dir := a.ScreenshotDefaultDir()
	if dir == "" {
		return out, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	entries, err := a.link.ListRemote(ctx, "/screenshots")
	if err != nil {
		// No folder on the server yet is not an error.
		if strings.Contains(err.Error(), "not_found") {
			return out, nil
		}
		return out, err
	}
	local := map[string]bool{}
	if names, err := os.ReadDir(dir); err == nil {
		for _, name := range names {
			if !name.IsDir() {
				local[name.Name()] = true
			}
		}
	}
	const maxFiles = 200
	const maxBytes = 32 << 20
	pulled := 0
	skipped := 0
	for _, entry := range entries {
		if entry.IsDir || entry.Size > maxBytes {
			skipped++
			continue
		}
		if local[entry.Name] || pulled >= maxFiles {
			continue
		}
		target := filepath.Join(dir, entry.Name)
		if _, err := a.link.Download(ctx, joinRemotePath("/screenshots", entry.Name), target, nil); err != nil {
			skipped++
			continue
		}
		pulled++
	}
	out["pulled"] = pulled
	out["skipped"] = skipped
	if pulled > 0 {
		a.emit("screenshots:pulled", out)
	}
	return out, nil
}

func joinRemotePath(base, name string) string {
	b := strings.TrimSuffix(base, "/")
	if b == "" {
		b = ""
	}
	return b + "/" + name
}

// scheduleFullSync debounces a full sync right after login.
func (a *App) scheduleFullSync() {
	a.vaultState.mu.Lock()
	if a.vaultState.timer != nil {
		a.vaultState.timer.Stop()
	}
	a.vaultState.timer = time.AfterFunc(2*time.Second, func() {
		if _, err := a.FullSync(); err != nil && a.log != nil {
			a.log.Printf("full sync: %v", err)
		}
	})
	a.vaultState.mu.Unlock()
}
