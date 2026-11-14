// Package api implements the server-side wire protocol: registration, login,
// note synchronisation, per-user file storage and TCP relay.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"wailstools/internal/wire"
	"wailstools/server/internal/auth"
	"wailstools/server/internal/store"
)

// Version is the server build identifier.
const Version = "1.0.0"

// Options configures the API.
type Options struct {
	AllowRegistration bool
	DefaultQuota      int64
	TokenTTL          time.Duration
	RelayEnabled      bool
	// RelayBlockedCIDRs reserves future restriction of relay targets.
	RelayBlockedCIDRs []string
	// LocalAdmin lets loopback connections act as the first admin without a
	// token (reserved local maintenance access).
	LocalAdmin bool
	Logf       func(format string, args ...any)
}

// API is the wire.Handler implementation.
type API struct {
	DB      *store.DB
	Secret  []byte
	Opts    Options
	started time.Time

	mu     sync.RWMutex
	online map[string]int
}

// New creates an API.
func New(db *store.DB, secret []byte, opts Options) *API {
	if opts.DefaultQuota <= 0 {
		opts.DefaultQuota = 10 << 30 // 10 GiB
	}
	if opts.TokenTTL <= 0 {
		opts.TokenTTL = 30 * 24 * time.Hour
	}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	return &API{DB: db, Secret: secret, Opts: opts, started: time.Now(), online: map[string]int{}}
}

// Authenticate validates the hello frame. A stream without a token is treated
// as anonymous: only public methods (register/login/server.info) are reachable
// and every authenticated method re-checks the user before doing any work.
func (a *API) Authenticate(h wire.Hello) (string, error) {
	if strings.TrimSpace(h.Token) == "" {
		return "", nil
	}
	hash := auth.HashToken(h.Token)
	t, err := a.DB.GetToken(hash)
	if err != nil {
		return "", wire.Errorf("unauthorized", "invalid token")
	}
	if t.ExpiresAt != "" {
		if exp, err := time.Parse(time.RFC3339, t.ExpiresAt); err == nil && time.Now().After(exp) {
			_ = a.DB.DeleteToken(hash)
			return "", wire.Errorf("unauthorized", "token expired")
		}
	}
	u, err := a.DB.GetUser(t.UserID)
	if err != nil {
		return "", wire.Errorf("unauthorized", "user no longer exists")
	}
	if u.Disabled {
		return "", wire.Errorf("forbidden", "account disabled")
	}
	return u.ID, nil
}

// AuthenticateRemote authenticates a session, granting unrestricted local
// access for loopback connections when LocalAdmin is enabled.
func (a *API) AuthenticateRemote(h wire.Hello, remote string) (string, error) {
	if a.Opts.LocalAdmin && strings.TrimSpace(h.Token) == "" && isLoopback(remote) {
		if id := a.firstAdminID(); id != "" {
			return id, nil
		}
	}
	return a.Authenticate(h)
}

func (a *API) firstAdminID() string {
	users, err := a.DB.ListUsers()
	if err != nil {
		return ""
	}
	for _, u := range users {
		if u.Role == "admin" {
			return u.ID
		}
	}
	return ""
}

func isLoopback(remote string) bool {
	host := remote
	if h, _, err := net.SplitHostPort(remote); err == nil {
		host = h
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// TrackConnection records a live stream for the online counter.
func (a *API) TrackConnection(userID string, delta int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.online[userID] += delta
	if a.online[userID] <= 0 {
		delete(a.online, userID)
	}
}

// Handle dispatches a request.
func (a *API) Handle(method string, body json.RawMessage, sess *wire.ServerConn) (any, error) {
	ctx := &callCtx{api: a, sess: sess}
	switch method {
	// ---- public -----------------------------------------------------------
	case "server.info":
		return a.serverInfo()
	case "auth.register":
		return ctx.register(body)
	case "auth.login":
		return ctx.login(body)
	// ---- authenticated ----------------------------------------------------
	case "auth.me":
		return ctx.me()
	case "auth.logout":
		return ctx.logout()
	case "auth.changePassword":
		return ctx.changePassword(body)
	case "notes.list":
		return ctx.notesList(body)
	case "notes.put":
		return ctx.notesPut(body)
	case "notes.delete":
		return ctx.notesDelete(body)
	case "notes.sync":
		return ctx.notesSync(body)
	case "files.list":
		return ctx.filesList(body)
	case "files.stat":
		return ctx.filesStat(body)
	case "files.mkdir":
		return ctx.filesMkdir(body)
	case "files.remove":
		return ctx.filesRemove(body)
	case "files.rename":
		return ctx.filesRename(body)
	case "files.usage":
		return ctx.filesUsage()
	case "subs.get":
		return ctx.subsGet()
	case "subs.put":
		return ctx.subsPut(body)
	case "files.extract":
		return ctx.filesExtract(body)
	case "relay.check":
		return map[string]any{"enabled": a.Opts.RelayEnabled}, nil
	// ---- client update distribution (public reads, admin writes) ----------
	case "update.list":
		return ctx.updateList(body)
	case "update.latest":
		return ctx.updateLatest(body)
	case "update.delete":
		return ctx.updateDelete(body)
	default:
		return nil, wire.Errorf("unknown_method", "unknown method %q", method)
	}
}

// Pipe handles streaming methods.
func (a *API) Pipe(method string, body json.RawMessage, conn net.Conn, sess *wire.ServerConn) error {
	ctx := &callCtx{api: a, sess: sess}
	switch method {
	case "files.get":
		return ctx.filesGet(body, conn)
	case "files.put":
		return ctx.filesPut(body, conn)
	case "relay.tcp":
		return a.relayTCP(body, conn, sess)
	case "update.put":
		return ctx.updatePut(body, conn)
	case "update.get":
		return ctx.updateGet(body, conn)
	default:
		return wire.Errorf("unknown_method", "unknown streaming method %q", method)
	}
}

func (a *API) serverInfo() (any, error) {
	n, _ := a.DB.CountUsers()
	a.mu.RLock()
	online := len(a.online)
	a.mu.RUnlock()
	return map[string]any{
		"version":         Version,
		"uptimeSeconds":   int64(time.Since(a.started).Seconds()),
		"users":           n,
		"online":          online,
		"registration":    a.Opts.AllowRegistration,
		"relay":           a.Opts.RelayEnabled,
		"serverTime":      time.Now().UnixMilli(),
		"protocolVersion": wire.Version,
	}, nil
}

// callCtx carries per-call state.
type callCtx struct {
	api  *API
	sess *wire.ServerConn
}

func (c *callCtx) userID() (string, error) {
	if c.sess.UserID == "" {
		return "", wire.Errorf("unauthorized", "authentication required")
	}
	return c.sess.UserID, nil
}

type registerReq struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
}

func (c *callCtx) register(body json.RawMessage) (any, error) {
	var req registerReq
	if err := wire.Decode(body, &req); err != nil {
		return nil, wire.Errorf("bad_request", "invalid body: %v", err)
	}
	if !c.api.Opts.AllowRegistration {
		// The first account is always allowed so a fresh server is usable.
		if n, _ := c.api.DB.CountUsers(); n > 0 {
			return nil, wire.Errorf("forbidden", "registration is disabled on this server")
		}
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
		return nil, wire.Errorf("internal", "hash password: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	role := "user"
	if n, _ := c.api.DB.CountUsers(); n == 0 {
		role = "admin"
	}
	u := store.User{
		ID:           newUserID(),
		Username:     req.Username,
		PasswordHash: hash,
		DisplayName:  firstNonEmpty(req.DisplayName, req.Username),
		Email:        strings.TrimSpace(req.Email),
		Role:         role,
		QuotaBytes:   c.api.Opts.DefaultQuota,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	created, err := c.api.DB.CreateUser(u)
	if err != nil {
		if errors.Is(err, store.ErrExists) {
			return nil, wire.Errorf("conflict", "username already exists")
		}
		return nil, wire.Errorf("internal", "%v", err)
	}
	c.api.DB.Audit(store.AuditEntry{UserID: created.ID, Action: "register", Detail: created.Username})
	token, hashTok, err := auth.NewToken()
	if err != nil {
		return nil, wire.Errorf("internal", "token: %v", err)
	}
	exp := auth.TokenExpiry(c.api.Opts.TokenTTL)
	if err := c.api.DB.SaveToken(store.Token{Hash: hashTok, UserID: created.ID, Label: "register", CreatedAt: now, ExpiresAt: exp}); err != nil {
		return nil, wire.Errorf("internal", "%v", err)
	}
	return map[string]any{"user": created.Public(), "token": token, "expiresAt": exp}, nil
}

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Label    string `json:"label"`
}

func (c *callCtx) login(body json.RawMessage) (any, error) {
	var req loginReq
	if err := wire.Decode(body, &req); err != nil {
		return nil, wire.Errorf("bad_request", "invalid body: %v", err)
	}
	u, err := c.api.DB.GetUserByName(strings.TrimSpace(req.Username))
	if err != nil {
		return nil, wire.Errorf("unauthorized", "invalid username or password")
	}
	ok, err := auth.VerifyPassword(req.Password, u.PasswordHash)
	if err != nil || !ok {
		c.api.DB.Audit(store.AuditEntry{UserID: u.ID, Action: "login-failed"})
		return nil, wire.Errorf("unauthorized", "invalid username or password")
	}
	if u.Disabled {
		return nil, wire.Errorf("forbidden", "account disabled")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	u.LastLoginAt = now
	_ = c.api.DB.UpdateUser(u)
	token, hashTok, err := auth.NewToken()
	if err != nil {
		return nil, wire.Errorf("internal", "token: %v", err)
	}
	exp := auth.TokenExpiry(c.api.Opts.TokenTTL)
	label := req.Label
	if label == "" {
		label = "desktop"
	}
	if err := c.api.DB.SaveToken(store.Token{Hash: hashTok, UserID: u.ID, Label: label, CreatedAt: now, ExpiresAt: exp}); err != nil {
		return nil, wire.Errorf("internal", "%v", err)
	}
	c.api.DB.Audit(store.AuditEntry{UserID: u.ID, Action: "login"})
	return map[string]any{"user": u.Public(), "token": token, "expiresAt": exp}, nil
}

func (c *callCtx) me() (any, error) {
	uid, err := c.userID()
	if err != nil {
		return nil, err
	}
	u, err := c.api.DB.GetUser(uid)
	if err != nil {
		return nil, wire.Errorf("not_found", "user not found")
	}
	usage, _ := c.api.DB.RecalcUsage(uid)
	return map[string]any{"user": u.Public(), "usedBytes": usage}, nil
}

func (c *callCtx) logout() (any, error) {
	if c.sess.Hello.Token != "" {
		_ = c.api.DB.DeleteToken(auth.HashToken(c.sess.Hello.Token))
	}
	return map[string]any{"ok": true}, nil
}

type changePasswordReq struct {
	OldPassword string `json:"oldPassword"`
	NewPassword string `json:"newPassword"`
}

func (c *callCtx) changePassword(body json.RawMessage) (any, error) {
	uid, err := c.userID()
	if err != nil {
		return nil, err
	}
	var req changePasswordReq
	if err := wire.Decode(body, &req); err != nil {
		return nil, wire.Errorf("bad_request", "invalid body: %v", err)
	}
	u, err := c.api.DB.GetUser(uid)
	if err != nil {
		return nil, wire.Errorf("not_found", "user not found")
	}
	ok, err := auth.VerifyPassword(req.OldPassword, u.PasswordHash)
	if err != nil || !ok {
		return nil, wire.Errorf("unauthorized", "current password is incorrect")
	}
	if err := auth.ValidatePassword(req.NewPassword); err != nil {
		return nil, wire.Errorf("bad_request", "%v", err)
	}
	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		return nil, wire.Errorf("internal", "%v", err)
	}
	u.PasswordHash = hash
	u.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := c.api.DB.UpdateUser(u); err != nil {
		return nil, wire.Errorf("internal", "%v", err)
	}
	c.api.DB.Audit(store.AuditEntry{UserID: uid, Action: "change-password"})
	return map[string]any{"ok": true}, nil
}

// ---- notes ---------------------------------------------------------------

type notesListReq struct {
	Since int64 `json:"since"`
}

func (c *callCtx) notesList(body json.RawMessage) (any, error) {
	uid, err := c.userID()
	if err != nil {
		return nil, err
	}
	var req notesListReq
	_ = wire.Decode(body, &req)
	notes, err := c.api.DB.ListNotes(uid, req.Since)
	if err != nil {
		return nil, wire.Errorf("internal", "%v", err)
	}
	return map[string]any{"notes": notes, "serverTime": time.Now().UnixMilli()}, nil
}

type noteBody struct {
	Note store.Note `json:"note"`
}

func (c *callCtx) notesPut(body json.RawMessage) (any, error) {
	uid, err := c.userID()
	if err != nil {
		return nil, err
	}
	var req noteBody
	if err := wire.Decode(body, &req); err != nil {
		return nil, wire.Errorf("bad_request", "invalid body: %v", err)
	}
	n := req.Note
	if strings.TrimSpace(n.ID) == "" {
		return nil, wire.Errorf("bad_request", "note id is required")
	}
	now := time.Now().UnixMilli()
	if n.UpdatedAt <= 0 {
		n.UpdatedAt = now
	}
	if n.CreatedAt <= 0 {
		n.CreatedAt = n.UpdatedAt
	}
	n.UserID = uid
	saved, err := c.api.DB.PutNote(n)
	if err != nil {
		return nil, wire.Errorf("internal", "%v", err)
	}
	return map[string]any{"note": saved, "serverTime": now}, nil
}

type noteDeleteReq struct {
	ID        string `json:"id"`
	UpdatedAt int64  `json:"updatedAt"`
}

func (c *callCtx) notesDelete(body json.RawMessage) (any, error) {
	uid, err := c.userID()
	if err != nil {
		return nil, err
	}
	var req noteDeleteReq
	if err := wire.Decode(body, &req); err != nil {
		return nil, wire.Errorf("bad_request", "invalid body: %v", err)
	}
	if req.ID == "" {
		return nil, wire.Errorf("bad_request", "note id is required")
	}
	n, err := c.api.DB.GetNote(uid, req.ID)
	if err != nil {
		return map[string]any{"ok": true}, nil
	}
	n.Deleted = true
	n.Content = ""
	n.UpdatedAt = req.UpdatedAt
	if n.UpdatedAt <= 0 {
		n.UpdatedAt = time.Now().UnixMilli()
	}
	if _, err := c.api.DB.PutNote(n); err != nil {
		return nil, wire.Errorf("internal", "%v", err)
	}
	return map[string]any{"ok": true, "serverTime": time.Now().UnixMilli()}, nil
}

type notesSyncReq struct {
	Since   int64        `json:"since"`
	Changes []store.Note `json:"changes"`
}

func (c *callCtx) notesSync(body json.RawMessage) (any, error) {
	uid, err := c.userID()
	if err != nil {
		return nil, err
	}
	var req notesSyncReq
	if err := wire.Decode(body, &req); err != nil {
		return nil, wire.Errorf("bad_request", "invalid body: %v", err)
	}
	applied := make([]string, 0, len(req.Changes))
	conflicts := make([]store.Note, 0)
	for _, incoming := range req.Changes {
		if strings.TrimSpace(incoming.ID) == "" {
			continue
		}
		incoming.UserID = uid
		existing, err := c.api.DB.GetNote(uid, incoming.ID)
		if err == nil {
			if existing.UpdatedAt > incoming.UpdatedAt {
				conflicts = append(conflicts, existing)
				continue
			}
			if existing.Deleted && !incoming.Deleted {
				// Tombstones win unless the client explicitly edited later.
				if incoming.UpdatedAt <= existing.UpdatedAt {
					conflicts = append(conflicts, existing)
					continue
				}
			}
		}
		if incoming.CreatedAt <= 0 {
			incoming.CreatedAt = incoming.UpdatedAt
		}
		if _, err := c.api.DB.PutNote(incoming); err != nil {
			return nil, wire.Errorf("internal", "%v", err)
		}
		applied = append(applied, incoming.ID)
	}
	notes, err := c.api.DB.ListNotes(uid, req.Since)
	if err != nil {
		return nil, wire.Errorf("internal", "%v", err)
	}
	now := time.Now().UnixMilli()
	return map[string]any{
		"notes":      notes,
		"applied":    applied,
		"conflicts":  conflicts,
		"serverTime": now,
	}, nil
}

// ---- files ---------------------------------------------------------------

// safeRel normalises a client path to a clean POSIX relative path. Traversal
// segments are rejected outright rather than silently rewritten.
func safeRel(p string) (string, error) {
	p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", wire.Errorf("bad_request", "invalid path")
		}
	}
	clean := path.Clean("/" + p)
	clean = strings.TrimPrefix(clean, "/")
	if clean == "." {
		clean = ""
	}
	return clean, nil
}

func (c *callCtx) filesDir(uid string) (string, error) {
	root, err := c.api.DB.UserDataDir(uid)
	if err != nil {
		return "", wire.Errorf("internal", "%v", err)
	}
	return root, nil
}

func (c *callCtx) absPath(uid, rel string) (string, error) {
	root, err := c.filesDir(uid)
	if err != nil {
		return "", err
	}
	clean, err := safeRel(rel)
	if err != nil {
		return "", err
	}
	full := filepath.Join(root, filepath.FromSlash(clean))
	if !strings.HasPrefix(filepath.Clean(full), filepath.Clean(root)) {
		return "", wire.Errorf("bad_request", "path escapes user storage")
	}
	return full, nil
}

type fileListReq struct {
	Path string `json:"path"`
}

func (c *callCtx) filesList(body json.RawMessage) (any, error) {
	uid, err := c.userID()
	if err != nil {
		return nil, err
	}
	var req fileListReq
	_ = wire.Decode(body, &req)
	full, err := c.absPath(uid, req.Path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, wire.Errorf("not_found", "path not found")
		}
		return nil, wire.Errorf("internal", "%v", err)
	}
	clean, _ := safeRel(req.Path)
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		rel := path.Join("/", clean, e.Name())
		out = append(out, map[string]any{
			"name":    e.Name(),
			"path":    rel,
			"isDir":   e.IsDir(),
			"size":    info.Size(),
			"modTime": info.ModTime().UTC().Format(time.RFC3339),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		di, _ := out[i]["isDir"].(bool)
		dj, _ := out[j]["isDir"].(bool)
		if di != dj {
			return di
		}
		return strings.ToLower(fmt.Sprint(out[i]["name"])) < strings.ToLower(fmt.Sprint(out[j]["name"]))
	})
	return map[string]any{"path": path.Join("/", clean), "entries": out}, nil
}

func (c *callCtx) filesStat(body json.RawMessage) (any, error) {
	uid, err := c.userID()
	if err != nil {
		return nil, err
	}
	var req fileListReq
	_ = wire.Decode(body, &req)
	full, err := c.absPath(uid, req.Path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return nil, wire.Errorf("not_found", "path not found")
	}
	return map[string]any{
		"name":    info.Name(),
		"size":    info.Size(),
		"isDir":   info.IsDir(),
		"modTime": info.ModTime().UTC().Format(time.RFC3339),
	}, nil
}

func (c *callCtx) filesMkdir(body json.RawMessage) (any, error) {
	uid, err := c.userID()
	if err != nil {
		return nil, err
	}
	var req fileListReq
	if err := wire.Decode(body, &req); err != nil {
		return nil, wire.Errorf("bad_request", "invalid body: %v", err)
	}
	full, err := c.absPath(uid, req.Path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(full, 0o755); err != nil {
		return nil, wire.Errorf("internal", "%v", err)
	}
	clean, _ := safeRel(req.Path)
	_ = c.api.DB.PutFileMeta(store.FileMeta{
		Path: "/" + clean, UserID: uid, IsDir: true,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	})
	return map[string]any{"ok": true}, nil
}

type fileRemoveReq struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive"`
}

func (c *callCtx) filesRemove(body json.RawMessage) (any, error) {
	uid, err := c.userID()
	if err != nil {
		return nil, err
	}
	var req fileRemoveReq
	if err := wire.Decode(body, &req); err != nil {
		return nil, wire.Errorf("bad_request", "invalid body: %v", err)
	}
	if strings.TrimSpace(req.Path) == "" || strings.TrimSpace(req.Path) == "/" {
		return nil, wire.Errorf("bad_request", "refusing to remove the storage root")
	}
	full, err := c.absPath(uid, req.Path)
	if err != nil {
		return nil, err
	}
	if req.Recursive {
		err = os.RemoveAll(full)
	} else {
		err = os.Remove(full)
	}
	if err != nil {
		return nil, wire.Errorf("internal", "%v", err)
	}
	clean, _ := safeRel(req.Path)
	metas, _ := c.api.DB.ListFileMeta(uid, "/"+clean)
	for _, m := range metas {
		_ = c.api.DB.DeleteFileMeta(uid, m.Path)
	}
	_ = c.api.DB.DeleteFileMeta(uid, "/"+clean)
	_, _ = c.api.DB.RecalcUsage(uid)
	return map[string]any{"ok": true}, nil
}

type fileRenameReq struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func (c *callCtx) filesRename(body json.RawMessage) (any, error) {
	uid, err := c.userID()
	if err != nil {
		return nil, err
	}
	var req fileRenameReq
	if err := wire.Decode(body, &req); err != nil {
		return nil, wire.Errorf("bad_request", "invalid body: %v", err)
	}
	fromFull, err := c.absPath(uid, req.From)
	if err != nil {
		return nil, err
	}
	toFull, err := c.absPath(uid, req.To)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(toFull), 0o755); err != nil {
		return nil, wire.Errorf("internal", "%v", err)
	}
	if err := os.Rename(fromFull, toFull); err != nil {
		return nil, wire.Errorf("internal", "%v", err)
	}
	return map[string]any{"ok": true}, nil
}

func (c *callCtx) filesUsage() (any, error) {
	uid, err := c.userID()
	if err != nil {
		return nil, err
	}
	used, err := c.api.DB.RecalcUsage(uid)
	if err != nil {
		return nil, wire.Errorf("internal", "%v", err)
	}
	u, _ := c.api.DB.GetUser(uid)
	return map[string]any{"usedBytes": used, "quotaBytes": u.QuotaBytes}, nil
}

type filePathReq struct {
	Path   string `json:"path"`
	Offset int64  `json:"offset"`
}

// fileGetHeader precedes the raw bytes of a download.
type fileGetHeader struct {
	OK    bool   `json:"ok"`
	Size  int64  `json:"size"`
	Error string `json:"error,omitempty"`
}

func (c *callCtx) filesGet(body json.RawMessage, conn net.Conn) error {
	uid, err := c.userID()
	if err != nil {
		_ = wire.WriteFrame(conn, fileGetHeader{Error: "unauthorized"})
		return err
	}
	var req filePathReq
	if err := wire.Decode(body, &req); err != nil {
		_ = wire.WriteFrame(conn, fileGetHeader{Error: "bad_request"})
		return err
	}
	full, err := c.absPath(uid, req.Path)
	if err != nil {
		_ = wire.WriteFrame(conn, fileGetHeader{Error: "bad_request"})
		return err
	}
	f, err := os.Open(full)
	if err != nil {
		_ = wire.WriteFrame(conn, fileGetHeader{Error: "not_found"})
		return wire.Errorf("not_found", "file not found")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		_ = wire.WriteFrame(conn, fileGetHeader{Error: "not_found"})
		return wire.Errorf("not_found", "not a file")
	}
	offset := req.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > info.Size() {
		offset = info.Size()
	}
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			_ = wire.WriteFrame(conn, fileGetHeader{Error: "internal"})
			return err
		}
	}
	remaining := info.Size() - offset
	if err := wire.WriteFrame(conn, fileGetHeader{OK: true, Size: remaining}); err != nil {
		return err
	}
	_, err = io.CopyN(conn, f, remaining)
	return err
}

type filePutReq struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Truncate bool   `json:"truncate"`
	Offset   int64  `json:"offset"`
}

// filePutAck is written after the raw upload body has been consumed.
type filePutAck struct {
	OK     bool   `json:"ok"`
	Size   int64  `json:"size"`
	Error  string `json:"error,omitempty"`
	Detail string `json:"detail,omitempty"`
}

func (c *callCtx) filesPut(body json.RawMessage, conn net.Conn) error {
	ack := func(a filePutAck) {
		_ = wire.WriteFrame(conn, a)
	}
	uid, err := c.userID()
	if err != nil {
		ack(filePutAck{Error: "unauthorized"})
		return err
	}
	var req filePutReq
	if err := wire.Decode(body, &req); err != nil {
		ack(filePutAck{Error: "bad_request", Detail: err.Error()})
		return wire.Errorf("bad_request", "invalid body: %v", err)
	}
	if req.Size < 0 {
		ack(filePutAck{Error: "bad_request"})
		return wire.Errorf("bad_request", "invalid size")
	}
	u, err := c.api.DB.GetUser(uid)
	if err != nil {
		ack(filePutAck{Error: "not_found"})
		return wire.Errorf("not_found", "user not found")
	}
	used, _ := c.api.DB.RecalcUsage(uid)
	if u.QuotaBytes > 0 && used+req.Size > u.QuotaBytes {
		ack(filePutAck{Error: "quota_exceeded", Detail: "storage quota exceeded"})
		return wire.Errorf("quota_exceeded", "storage quota exceeded (%d/%d bytes)", used, u.QuotaBytes)
	}
	full, err := c.absPath(uid, req.Path)
	if err != nil {
		ack(filePutAck{Error: "bad_request"})
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		ack(filePutAck{Error: "internal", Detail: err.Error()})
		return wire.Errorf("internal", "%v", err)
	}
	flags := os.O_CREATE | os.O_WRONLY
	if req.Truncate {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(full, flags, 0o644)
	if err != nil {
		ack(filePutAck{Error: "internal", Detail: err.Error()})
		return wire.Errorf("internal", "%v", err)
	}
	defer f.Close()
	if req.Offset > 0 {
		if _, err := f.Seek(req.Offset, io.SeekStart); err != nil {
			ack(filePutAck{Error: "internal", Detail: err.Error()})
			return wire.Errorf("internal", "%v", err)
		}
	}
	written, err := io.CopyN(f, conn, req.Size)
	if err != nil && err != io.EOF {
		ack(filePutAck{Error: "internal", Detail: "write failed: " + err.Error(), Size: written})
		return wire.Errorf("internal", "write failed: %v", err)
	}
	if written != req.Size {
		ack(filePutAck{Error: "incomplete", Detail: "short upload", Size: written})
		return wire.Errorf("incomplete", "received %d of %d bytes", written, req.Size)
	}
	if err := f.Sync(); err != nil {
		ack(filePutAck{Error: "internal", Detail: err.Error()})
		return wire.Errorf("internal", "%v", err)
	}
	clean, _ := safeRel(req.Path)
	now := time.Now().UTC().Format(time.RFC3339)
	_ = c.api.DB.PutFileMeta(store.FileMeta{
		Path: "/" + clean, UserID: uid, Size: written, UpdatedAt: now, CreatedAt: now,
	})
	_, _ = c.api.DB.RecalcUsage(uid)
	ack(filePutAck{OK: true, Size: written})
	return nil
}

func newUserID() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 16)
	if _, err := readRand(b); err != nil {
		return fmt.Sprintf("u%d", time.Now().UnixNano())
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return "u-" + string(b)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
