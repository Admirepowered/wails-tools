package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
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

// fileNameFromURL derives a file name from a download URL.
func fileNameFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "download.bin"
	}
	base := path.Base(u.Path)
	if base == "." || base == "/" || base == "" {
		return "download.bin"
	}
	return base
}

func (m *OfflineManager) download(ctx context.Context, t store.OfflineTask) {
	if taskIsHTTP(t) {
		m.downloadHTTP(ctx, t)
		return
	}
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
	Magnet  string            `json:"magnet"`
	URL     string            `json:"url"`
	Name    string            `json:"name"`
	Headers map[string]string `json:"headers"`
}

// taskIsHTTP reports whether the task downloads a plain HTTP(S) URL.
func taskIsHTTP(t store.OfflineTask) bool { return t.Kind == "url" }

func (c *callCtx) offlineAdd(body json.RawMessage) (any, error) {
	uid, err := c.userID()
	if err != nil {
		return nil, err
	}
	var req offlineAddReq
	if err := wire.Decode(body, &req); err != nil {
		return nil, wire.Errorf("bad_request", "invalid body: %v", err)
	}
	magnet := strings.TrimSpace(req.Magnet)
	rawURL := strings.TrimSpace(req.URL)
	kind := "magnet"
	switch {
	case magnet != "" && strings.HasPrefix(strings.ToLower(magnet), "magnet:"):
	case rawURL != "" && (strings.HasPrefix(strings.ToLower(rawURL), "http://") || strings.HasPrefix(strings.ToLower(rawURL), "https://")):
		kind = "url"
	default:
		return nil, wire.Errorf("bad_request", "需要 magnet: 磁力链或 http(s) 下载地址")
	}
	t := store.OfflineTask{
		ID:      "of-" + randomHex(8),
		UserID:  uid,
		Kind:    kind,
		Magnet:  magnet,
		URL:     rawURL,
		Headers: req.Headers,
		Name:    strings.TrimSpace(req.Name),
		Status:  "queued",
	}
	if t.Name == "" {
		if kind == "magnet" {
			t.Name = magnetLabel(t.Magnet)
		} else {
			t.Name = fileNameFromURL(t.URL)
		}
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
	if tasks == nil {
		tasks = []store.OfflineTask{} // JSON arrays, never null
	}
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

// downloadHTTP pulls a URL (with its headers) into the offline folder. Used by
// netdisk pushes: the client resolves the signed link and hands over URL +
// headers so the server can fetch it directly.
func (m *OfflineManager) downloadHTTP(ctx context.Context, t store.OfflineTask) {
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
	name := sanitizeName(t.Name)
	if name == "" {
		name = fileNameFromURL(t.URL)
	}
	target := uniquePath(filepath.Join(dest, name))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.URL, nil)
	if err != nil {
		m.fail(t, err.Error())
		return
	}
	for k, v := range t.Headers {
		if strings.EqualFold(k, "host") {
			continue
		}
		req.Header.Set(k, v)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")
	}
	client := &http.Client{Timeout: 0}
	resp, err := client.Do(req)
	if err != nil {
		m.fail(t, err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		m.fail(t, fmt.Sprintf("下载地址返回 %d", resp.StatusCode))
		return
	}
	if total := resp.ContentLength; total > 0 {
		if quota > 0 && used+total > quota {
			m.fail(t, fmt.Sprintf("quota_exceeded: 需要 %d 字节，剩余 %d 字节", total, max64(0, quota-used)))
			return
		}
		t.Total = total
	}
	t.Status = "downloading"
	m.save(t)

	f, err := os.Create(target)
	if err != nil {
		m.fail(t, err.Error())
		return
	}
	defer f.Close()
	buf := make([]byte, 128<<10)
	var done int64
	lastSave := time.Now()
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				m.fail(t, "写入失败: "+werr.Error())
				return
			}
			done += int64(n)
			t.Done = done
			if quota > 0 && used+done > quota {
				f.Close()
				_ = os.Remove(target)
				m.fail(t, "quota_exceeded: 存储配额不足")
				return
			}
			if time.Since(lastSave) > 2*time.Second {
				lastSave = time.Now()
				m.save(t)
			}
		}
		if rerr != nil {
			if rerr != io.EOF {
				m.fail(t, rerr.Error())
				return
			}
			break
		}
	}
	rel := filepath.Join("离线下载", filepath.Base(target))
	if err := m.register(uid, root, rel); err != nil {
		m.fail(t, err.Error())
		return
	}
	_, _ = m.api.DB.RecalcUsage(uid)
	t.Status = "completed"
	t.Dir = rel
	t.Name = filepath.Base(target)
	t.Total = done
	t.Done = done
	m.save(t)
}

// sanitizeName strips path separators from a suggested file name.
func sanitizeName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.Trim(name, ".")
	return name
}

// uniquePath avoids overwriting an existing file.
func uniquePath(p string) string {
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return p
	}
	ext := filepath.Ext(p)
	base := strings.TrimSuffix(p, ext)
	for i := 1; i < 1000; i++ {
		cand := fmt.Sprintf("%s-%d%s", base, i, ext)
		if _, err := os.Stat(cand); os.IsNotExist(err) {
			return cand
		}
	}
	return p
}
