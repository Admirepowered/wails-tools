package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"wailstools/internal/torrentdl"
	"wailstools/internal/wire"
	"wailstools/server/internal/store"
)

// OfflineManager owns server-side magnet downloads ("离线下载").
type OfflineManager struct {
	api *API
	mu  sync.Mutex
	run map[string]context.CancelFunc
}

// Offline returns the process-wide manager.
func (a *API) Offline() *OfflineManager {
	a.offlineOnce.Do(func() {
		a.offlineMgr = &OfflineManager{api: a, run: map[string]context.CancelFunc{}}
	})
	return a.offlineMgr
}

func (m *OfflineManager) start(t store.OfflineTask) {
	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.run[t.ID] = cancel
	m.mu.Unlock()

	go func() {
		defer func() {
			m.mu.Lock()
			delete(m.run, t.ID)
			m.mu.Unlock()
			cancel()
		}()
		m.download(ctx, t)
	}()
}

func (m *OfflineManager) download(ctx context.Context, t store.OfflineTask) {
	uid := t.UserID
	user, err := m.api.DB.GetUser(uid)
	if err != nil {
		m.fail(t, "user not found")
		return
	}
	root, err := m.api.DB.UserDataDir(uid)
	if err != nil {
		m.fail(t, err.Error())
		return
	}
	used, _ := m.api.DB.RecalcUsage(uid)
	quota := user.QuotaBytes
	if quota <= 0 {
		quota = m.api.Opts.DefaultQuota
	}

	dest := filepath.Join(root, "离线下载")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		m.fail(t, err.Error())
		return
	}

	t.Status = "downloading"
	m.save(t)

	lastSave := time.Now()
	progress := func(done, total int64) {
		t.Done = done
		if total > 0 {
			t.Total = total
		}
		if time.Since(lastSave) > 2*time.Second {
			lastSave = time.Now()
			m.save(t)
		}
	}

	res, err := torrentdl.Download(ctx, t.Magnet, torrentdl.Options{
		Dir:      dest,
		Progress: progress,
		OnMetadata: func(name string, total int64) error {
			if name != "" {
				t.Name = name
			}
			if quota > 0 && used+total > quota {
				return fmt.Errorf("quota_exceeded: 需要 %d 字节，剩余 %d 字节（首次校验：%d/%d）", total, max64(0, quota-used), used, quota)
			}
			t.Total = total
			m.save(t)
			return nil
		},
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			t.Status = "canceled"
			m.save(t)
			return
		}
		m.fail(t, err.Error())
		return
	}

	// Register every downloaded file so it shows up in the file browser and is
	// counted by RecalcUsage (the same 10 GiB pool as screenshots).
	relDir := filepath.Join("离线下载", res.Name)
	if err := m.register(uid, root, relDir); err != nil {
		m.fail(t, err.Error())
		return
	}
	if _, err := m.api.DB.RecalcUsage(uid); err != nil {
		m.fail(t, err.Error())
		return
	}

	t.Status = "completed"
	t.Dir = relDir
	t.Total = res.Bytes
	t.Done = res.Bytes
	m.save(t)
}

// register records the files under relDir in the file metadata store.
func (m *OfflineManager) register(uid, root, relDir string) error {
	abs := filepath.Join(root, relDir)
	return filepath.Walk(abs, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		base := filepath.Base(path)
		if strings.HasPrefix(base, ".") { // torrent bookkeeping never gets registered
			return nil
		}
		now := time.Now().UTC().Format(time.RFC3339)
		return m.api.DB.PutFileMeta(store.FileMeta{
			Path: rel, UserID: uid, Size: info.Size(), IsDir: info.IsDir(),
			UpdatedAt: now, CreatedAt: now,
		})
	})
}

func (m *OfflineManager) save(t store.OfflineTask) {
	_ = m.api.DB.PutOfflineTask(t)
}

func (m *OfflineManager) fail(t store.OfflineTask, msg string) {
	t.Status = "failed"
	t.Error = msg
	m.save(t)
}

func (m *OfflineManager) cancel(id string) {
	m.mu.Lock()
	cancel, ok := m.run[id]
	m.mu.Unlock()
	if ok {
		cancel()
	}
}

// randomHex is a small local id generator (avoids pulling a uuid package).
func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// ---- handlers ------------------------------------------------------------

type offlineAddReq struct {
	Magnet string `json:"magnet"`
	Name   string `json:"name"`
}

func (c *callCtx) offlineAdd(body json.RawMessage) (any, error) {
	uid, err := c.userID()
	if err != nil {
		return nil, err
	}
	var req offlineAddReq
	if err := wire.Decode(body, &req); err != nil {
		return nil, wire.Errorf("bad_request", "invalid body: %v", err)
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(req.Magnet)), "magnet:") {
		return nil, wire.Errorf("bad_request", "需要 magnet: 开头的磁力链")
	}
	t := store.OfflineTask{
		ID:     "of-" + randomHex(8),
		UserID: uid,
		Magnet: strings.TrimSpace(req.Magnet),
		Name:   strings.TrimSpace(req.Name),
		Status: "queued",
	}
	if t.Name == "" {
		t.Name = magnetLabel(t.Magnet)
	}
	if err := c.api.DB.PutOfflineTask(t); err != nil {
		return nil, wire.Errorf("internal", "%v", err)
	}
	c.api.Offline().start(t)
	c.api.DB.Audit(store.AuditEntry{UserID: uid, Action: "offline.add", Detail: t.Name})
	return c.offlineStatus(uid), nil
}

func (c *callCtx) offlineList() (any, error) {
	uid, err := c.userID()
	if err != nil {
		return nil, err
	}
	return c.offlineStatus(uid), nil
}

// offlineStatus returns the task list plus the shared quota numbers.
func (c *callCtx) offlineStatus(uid string) map[string]any {
	u, _ := c.api.DB.GetUser(uid)
	used, _ := c.api.DB.RecalcUsage(uid)
	quota := c.api.Opts.DefaultQuota
	if u.QuotaBytes > 0 {
		quota = u.QuotaBytes
	}
	tasks := c.api.DB.ListOfflineTasks(uid)
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].CreatedAt < tasks[j].CreatedAt })
	return map[string]any{
		"tasks": tasks, "usedBytes": used, "quotaBytes": quota,
	}
}

func (c *callCtx) offlineDelete(body json.RawMessage) (any, error) {
	uid, err := c.userID()
	if err != nil {
		return nil, err
	}
	var req struct {
		ID    string `json:"id"`
		Purge bool   `json:"purge"`
	}
	if err := wire.Decode(body, &req); err != nil {
		return nil, wire.Errorf("bad_request", "invalid body: %v", err)
	}
	t, ok := c.api.DB.GetOfflineTask(req.ID)
	if !ok || t.UserID != uid {
		return nil, wire.Errorf("not_found", "task not found")
	}
	c.api.Offline().cancel(t.ID)
	if req.Purge && t.Dir != "" {
		root, err := c.api.DB.UserDataDir(uid)
		if err == nil {
			_ = os.RemoveAll(filepath.Join(root, filepath.FromSlash(t.Dir)))
			if metas, err := c.api.DB.ListFileMeta(uid, t.Dir); err == nil {
				for _, m := range metas {
					_ = c.api.DB.DeleteFileMeta(uid, m.Path)
				}
			}
			_, _ = c.api.DB.RecalcUsage(uid)
		}
	}
	if err := c.api.DB.DeleteOfflineTask(t.ID); err != nil {
		return nil, wire.Errorf("internal", "%v", err)
	}
	return c.offlineStatus(uid), nil
}

// magnetLabel derives a display name from a magnet link.
func magnetLabel(magnet string) string {
	if i := strings.Index(magnet, "?"); i >= 0 {
		if q, err := url.ParseQuery(magnet[i+1:]); err == nil {
			if dn := strings.TrimSpace(q.Get("dn")); dn != "" {
				return dn
			}
		}
	}
	if i := strings.Index(magnet, "btih:"); i >= 0 {
		h := magnet[i+5:]
		if j := strings.IndexAny(h, "&?"); j >= 0 {
			h = h[:j]
		}
		if len(h) > 10 {
			h = h[:10]
		}
		return "magnet-" + strings.ToLower(h)
	}
	return "magnet"
}
