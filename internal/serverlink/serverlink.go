// Package serverlink manages the client's connection to a WailsTools server:
// QUIC-over-UDP with automatic TLS-over-TCP fallback, RPC calls, note/file
// synchronisation and the TCP relay used for SSH jumps.
package serverlink

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"wailstools/internal/netx"
	"wailstools/internal/settings"
	"wailstools/internal/wire"
)

// User mirrors the server's public user record.
type User struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName,omitempty"`
	Email       string `json:"email,omitempty"`
	Role        string `json:"role"`
	QuotaBytes  int64  `json:"quotaBytes"`
	UsedBytes   int64  `json:"usedBytes"`
	CreatedAt   string `json:"createdAt"`
}

// Note is a synced note.
type Note struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Content   string   `json:"content"`
	Folder    string   `json:"folder,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Pinned    bool     `json:"pinned"`
	Deleted   bool     `json:"deleted"`
	UpdatedAt int64    `json:"updatedAt"`
	CreatedAt int64    `json:"createdAt"`
	Revision  int64    `json:"revision"`
}

// RemoteEntry is one entry in the server-side file store.
type RemoteEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	ModTime string `json:"modTime"`
}

// Status is the connection state exposed to the UI.
type Status struct {
	Connected   bool   `json:"connected"`
	Transport   string `json:"transport"`
	Address     string `json:"address"`
	User        *User  `json:"user,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	UDPError    string `json:"udpError,omitempty"`
	TCPError    string `json:"tcpError,omitempty"`
	UDPMs       int64  `json:"udpMs"`
	TCPMs       int64  `json:"tcpMs"`
	LastError   string `json:"lastError,omitempty"`
	Since       string `json:"since,omitempty"`
}

// Client owns the session to one server.
type Client struct {
	store *settings.Store
	logf  func(string, ...any)

	mu      sync.RWMutex
	sess    netx.Session
	status  Status
	token   string
	user    *User
	pin     string
	onState func(Status)
}

// New creates a client.
func New(store *settings.Store, logf func(string, ...any)) *Client {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Client{store: store, logf: logf}
}

// SetStateHandler registers a state callback (used to push Wails events).
func (c *Client) SetStateHandler(fn func(Status)) {
	c.mu.Lock()
	c.onState = fn
	c.mu.Unlock()
}

func (c *Client) setStatus(fn func(*Status)) Status {
	c.mu.Lock()
	fn(&c.status)
	st := c.status
	cb := c.onState
	c.mu.Unlock()
	if cb != nil {
		cb(st)
	}
	return st
}

// Status returns the current connection state.
func (c *Client) Status() Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	st := c.status
	if c.user != nil {
		u := *c.user
		st.User = &u
	}
	return st
}

// Token returns the active session token (persisted in settings).
func (c *Client) Token() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.token
}

// Available reports whether relay-capable connectivity exists.
func (c *Client) Available() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sess != nil
}

// Describe returns a short relay description for the SSH UI.
func (c *Client) Describe() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.sess == nil {
		return "no server"
	}
	return fmt.Sprintf("%s %s", strings.ToUpper(c.status.Transport), c.status.Address)
}

// Connect dials the configured server using the saved token.
func (c *Client) Connect(ctx context.Context) (Status, error) {
	cfg := c.store.Get().Server
	if strings.TrimSpace(cfg.Host) == "" {
		return c.Status(), errors.New("server host is not configured")
	}
	port := cfg.Port
	if port <= 0 {
		port = 8642
	}
	c.mu.RLock()
	pin := cfg.Pin
	c.mu.RUnlock()

	tlsConf := netx.ClientTLS(pin, func(fp string) {
		c.logf("trusting server certificate %s on first use", fp)
		c.mu.Lock()
		c.pin = fp
		c.mu.Unlock()
		_, _ = c.store.Update(func(s *settings.Settings) error {
			s.Server.Pin = fp
			return nil
		})
	})
	sess, report, err := netx.Dial(ctx, netx.DialConfig{
		Host:             cfg.Host,
		Port:             port,
		TLS:              tlsConf,
		HandshakeTimeout: 3 * time.Second,
		DisableUDP:       cfg.DisableUDP,
	})
	if err != nil {
		st := c.setStatus(func(s *Status) {
			s.Connected = false
			s.LastError = err.Error()
			if report != nil {
				s.UDPError = report.UDPError
				s.TCPError = report.TCPError
				s.UDPMs = report.UDPMs
				s.TCPMs = report.TCPMs
			}
		})
		return st, err
	}

	c.mu.Lock()
	if c.sess != nil {
		_ = c.sess.Close()
	}
	c.sess = sess
	c.mu.Unlock()

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(port))
	st := c.setStatus(func(s *Status) {
		s.Connected = true
		s.Transport = report.Transport
		s.Address = addr
		s.UDPMs = report.UDPMs
		s.TCPMs = report.TCPMs
		s.UDPError = report.UDPError
		s.TCPError = report.TCPError
		s.LastError = ""
		s.Since = time.Now().Format(time.RFC3339)
		s.Fingerprint = c.pin
	})
	_, _ = c.store.Update(func(s *settings.Settings) error {
		s.Server.LastUsed = report.Transport
		return nil
	})
	c.logf("connected to %s via %s (udp=%dms tcp=%dms)", addr, report.Transport, report.UDPMs, report.TCPMs)

	if cfg.Token != "" || c.token != "" {
		if _, err := c.Me(ctx); err != nil {
			c.logf("token check failed: %v", err)
		}
	}
	return st, nil
}

// ensure returns a live session, reconnecting when needed.
func (c *Client) ensure(ctx context.Context) (netx.Session, error) {
	c.mu.RLock()
	sess := c.sess
	c.mu.RUnlock()
	if sess != nil {
		return sess, nil
	}
	if _, err := c.Connect(ctx); err != nil {
		return nil, err
	}
	c.mu.RLock()
	sess = c.sess
	c.mu.RUnlock()
	if sess == nil {
		return nil, errors.New("not connected")
	}
	return sess, nil
}

func (c *Client) openStream(ctx context.Context, purpose string) (*wire.Client, func(), error) {
	sess, err := c.ensure(ctx)
	if err != nil {
		return nil, nil, err
	}
	conn, err := sess.Open(ctx)
	if err != nil {
		c.mu.Lock()
		c.sess = nil
		c.mu.Unlock()
		return nil, nil, err
	}
	c.mu.RLock()
	token := c.token
	c.mu.RUnlock()
	wc, err := wire.NewClient(conn, wire.Hello{Token: token, App: "wails-tools-client", Purpose: purpose})
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	return wc, func() { _ = wc.Close() }, nil
}

// Call performs one RPC.
func (c *Client) Call(ctx context.Context, method string, body any, out any) error {
	wc, closeFn, err := c.openStream(ctx, "rpc")
	if err != nil {
		return err
	}
	defer closeFn()
	return wc.Call(method, body, out)
}

// Register creates an account and stores the token.
func (c *Client) Register(ctx context.Context, username, password, email, displayName string) (Status, error) {
	var out struct {
		User  User   `json:"user"`
		Token string `json:"token"`
	}
	if err := c.Call(ctx, "auth.register", map[string]any{
		"username": username, "password": password, "email": email, "displayName": displayName,
	}, &out); err != nil {
		return c.Status(), err
	}
	return c.adoptToken(out.Token, out.User)
}

// Login authenticates and stores the token.
func (c *Client) Login(ctx context.Context, username, password string) (Status, error) {
	var out struct {
		User  User   `json:"user"`
		Token string `json:"token"`
	}
	if err := c.Call(ctx, "auth.login", map[string]any{"username": username, "password": password}, &out); err != nil {
		return c.Status(), err
	}
	return c.adoptToken(out.Token, out.User)
}

func (c *Client) adoptToken(token string, user User) (Status, error) {
	c.mu.Lock()
	c.token = token
	u := user
	c.user = &u
	c.mu.Unlock()
	_, err := c.store.Update(func(s *settings.Settings) error {
		s.Server.Token = token
		s.Server.Username = user.Username
		return nil
	})
	if err != nil {
		return c.Status(), err
	}
	return c.setStatus(func(s *Status) {
		uu := user
		s.User = &uu
	}), nil
}

// RestoreToken loads a previously saved token.
func (c *Client) RestoreToken() {
	cfg := c.store.Get().Server
	c.mu.Lock()
	c.token = cfg.Token
	c.pin = cfg.Pin
	c.mu.Unlock()
}

// Logout clears the token locally and on the server.
func (c *Client) Logout(ctx context.Context) error {
	_ = c.Call(ctx, "auth.logout", nil, nil)
	c.mu.Lock()
	c.token = ""
	c.user = nil
	c.mu.Unlock()
	_, err := c.store.Update(func(s *settings.Settings) error {
		s.Server.Token = ""
		return nil
	})
	return err
}

// Me refreshes the current user record.
func (c *Client) Me(ctx context.Context) (User, error) {
	var out struct {
		User      User  `json:"user"`
		UsedBytes int64 `json:"usedBytes"`
	}
	if err := c.Call(ctx, "auth.me", nil, &out); err != nil {
		return User{}, err
	}
	c.mu.Lock()
	u := out.User
	u.UsedBytes = out.UsedBytes
	c.user = &u
	c.mu.Unlock()
	return out.User, nil
}

// ServerInfo returns public server metadata.
func (c *Client) ServerInfo(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	err := c.Call(ctx, "server.info", nil, &out)
	return out, err
}

// ---- notes ---------------------------------------------------------------

// NotesSince fetches notes modified after sinceMs.
func (c *Client) NotesSince(ctx context.Context, sinceMs int64) ([]Note, int64, error) {
	var out struct {
		Notes      []Note `json:"notes"`
		ServerTime int64  `json:"serverTime"`
	}
	if err := c.Call(ctx, "notes.list", map[string]any{"since": sinceMs}, &out); err != nil {
		return nil, 0, err
	}
	return out.Notes, out.ServerTime, nil
}

// NotesReplace replaces one note.
func (c *Client) NotesReplace(ctx context.Context, n Note) (Note, error) {
	var out struct {
		Note Note `json:"note"`
	}
	if err := c.Call(ctx, "notes.put", map[string]any{"note": n}, &out); err != nil {
		return Note{}, err
	}
	return out.Note, nil
}

// NotesDelete tombstones a note.
func (c *Client) NotesDelete(ctx context.Context, id string, updatedAt int64) error {
	return c.Call(ctx, "notes.delete", map[string]any{"id": id, "updatedAt": updatedAt}, nil)
}

// NotesSync performs a two-way sync.
func (c *Client) NotesSync(ctx context.Context, since int64, changes []Note) ([]Note, []Note, int64, error) {
	var out struct {
		Notes      []Note `json:"notes"`
		Conflicts  []Note `json:"conflicts"`
		ServerTime int64  `json:"serverTime"`
	}
	if err := c.Call(ctx, "notes.sync", map[string]any{"since": since, "changes": changes}, &out); err != nil {
		return nil, nil, 0, err
	}
	return out.Notes, out.Conflicts, out.ServerTime, nil
}

// ---- remote files --------------------------------------------------------

// ListRemote lists a directory in the server-side store.
func (c *Client) ListRemote(ctx context.Context, p string) ([]RemoteEntry, error) {
	var out struct {
		Path    string        `json:"path"`
		Entries []RemoteEntry `json:"entries"`
	}
	if err := c.Call(ctx, "files.list", map[string]any{"path": p}, &out); err != nil {
		return nil, err
	}
	return out.Entries, nil
}

// MkdirRemote creates a directory.
func (c *Client) MkdirRemote(ctx context.Context, p string) error {
	return c.Call(ctx, "files.mkdir", map[string]any{"path": p}, nil)
}

// RemoveRemote deletes a path.
func (c *Client) RemoveRemote(ctx context.Context, p string, recursive bool) error {
	return c.Call(ctx, "files.remove", map[string]any{"path": p, "recursive": recursive}, nil)
}

// RenameRemote renames a path.
func (c *Client) RenameRemote(ctx context.Context, from, to string) error {
	return c.Call(ctx, "files.rename", map[string]any{"from": from, "to": to}, nil)
}

// Usage returns storage consumption.
func (c *Client) Usage(ctx context.Context) (used, quota int64, err error) {
	var out struct {
		UsedBytes  int64 `json:"usedBytes"`
		QuotaBytes int64 `json:"quotaBytes"`
	}
	err = c.Call(ctx, "files.usage", nil, &out)
	return out.UsedBytes, out.QuotaBytes, err
}

// Upload streams a local file into the server store.
func (c *Client) Upload(ctx context.Context, localPath, remotePath string, onProgress func(done, total int64)) (int64, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	total := st.Size()
	wc, closeFn, err := c.openStream(ctx, "file")
	if err != nil {
		return 0, err
	}
	defer closeFn()
	if err := wc.OpenStream("files.put", map[string]any{
		"path": remotePath, "size": total, "truncate": true,
	}); err != nil {
		return 0, err
	}
	conn := wc.Conn()
	buf := make([]byte, 256*1024)
	var done int64
	last := time.Now()
	for {
		select {
		case <-ctx.Done():
			return done, ctx.Err()
		default:
		}
		n, rerr := f.Read(buf)
		if n > 0 {
			if _, werr := conn.Write(buf[:n]); werr != nil {
				return done, werr
			}
			done += int64(n)
			if onProgress != nil && time.Since(last) > 150*time.Millisecond {
				last = time.Now()
				onProgress(done, total)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return done, rerr
		}
	}
	if onProgress != nil {
		onProgress(done, total)
	}
	// The server acknowledges the completed body so upload failures (quota,
	// disk errors) are reported instead of silently succeeding.
	var ack struct {
		OK     bool   `json:"ok"`
		Size   int64  `json:"size"`
		Error  string `json:"error"`
		Detail string `json:"detail"`
	}
	if err := wire.ReadFrame(conn, &ack); err != nil {
		return done, fmt.Errorf("upload acknowledgement failed: %w", err)
	}
	if !ack.OK {
		detail := ack.Detail
		if detail == "" {
			detail = ack.Error
		}
		return done, fmt.Errorf("upload rejected: %s", detail)
	}
	return done, nil
}

// Download streams a file from the server store.
func (c *Client) Download(ctx context.Context, remotePath, localPath string, onProgress func(done, total int64)) (int64, error) {
	wc, closeFn, err := c.openStream(ctx, "file")
	if err != nil {
		return 0, err
	}
	defer closeFn()
	if err := wc.OpenStream("files.get", map[string]any{"path": remotePath}); err != nil {
		return 0, err
	}
	conn := wc.Conn()
	var hdr struct {
		OK    bool   `json:"ok"`
		Size  int64  `json:"size"`
		Error string `json:"error"`
	}
	if err := wire.ReadFrame(conn, &hdr); err != nil {
		return 0, fmt.Errorf("download header: %w", err)
	}
	if !hdr.OK {
		msg := hdr.Error
		if msg == "" {
			msg = "not found"
		}
		return 0, fmt.Errorf("download failed: %s", msg)
	}
	if err := os.MkdirAll(filepathDir(localPath), 0o755); err != nil {
		return 0, err
	}
	f, err := os.Create(localPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	buf := make([]byte, 256*1024)
	var done int64
	last := time.Now()
	for done < hdr.Size {
		want := int64(len(buf))
		if rem := hdr.Size - done; rem < want {
			want = rem
		}
		n, rerr := conn.Read(buf[:want])
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return done, werr
			}
			done += int64(n)
			if onProgress != nil && time.Since(last) > 150*time.Millisecond {
				last = time.Now()
				onProgress(done, hdr.Size)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return done, rerr
		}
	}
	if onProgress != nil {
		onProgress(done, hdr.Size)
	}
	if done != hdr.Size {
		return done, fmt.Errorf("incomplete download: %d of %d bytes", done, hdr.Size)
	}
	return done, f.Sync()
}

func filepathDir(p string) string {
	i := strings.LastIndexAny(p, `/\`)
	if i <= 0 {
		return "."
	}
	return p[:i]
}

// UploadRelease streams a client build to the server release store.
func (c *Client) UploadRelease(ctx context.Context, localPath string, meta map[string]any, onProgress func(done, total int64)) (map[string]any, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	body := map[string]any{}
	for k, v := range meta {
		body[k] = v
	}
	body["size"] = st.Size()
	body["sha256"] = hex.EncodeToString(hasher.Sum(nil))

	wc, closeFn, err := c.openStream(ctx, "release")
	if err != nil {
		return nil, err
	}
	defer closeFn()
	if err := wc.OpenStream("update.put", body); err != nil {
		return nil, err
	}
	conn := wc.Conn()
	buf := make([]byte, 256*1024)
	var done int64
	last := time.Now()
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			if _, werr := conn.Write(buf[:n]); werr != nil {
				return nil, werr
			}
			done += int64(n)
			if onProgress != nil && time.Since(last) > 150*time.Millisecond {
				last = time.Now()
				onProgress(done, st.Size())
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, rerr
		}
	}
	if onProgress != nil {
		onProgress(done, st.Size())
	}
	var ack struct {
		OK      bool           `json:"ok"`
		ID      string         `json:"id"`
		Error   string         `json:"error"`
		Detail  string         `json:"detail"`
		Release map[string]any `json:"release"`
	}
	if err := wire.ReadFrame(conn, &ack); err != nil {
		return nil, fmt.Errorf("release upload acknowledgement failed: %w", err)
	}
	if !ack.OK {
		detail := ack.Detail
		if detail == "" {
			detail = ack.Error
		}
		return nil, fmt.Errorf("release upload rejected: %s", detail)
	}
	return map[string]any{"id": ack.ID, "release": ack.Release}, nil
}

// OpenStream starts a streaming call and returns the raw connection plus a
// close function. The caller owns the byte protocol on the stream.
func (c *Client) OpenStream(ctx context.Context, method string, body any) (net.Conn, func(), error) {
	wc, closeFn, err := c.openStream(ctx, "stream")
	if err != nil {
		return nil, nil, err
	}
	if err := wc.OpenStream(method, body); err != nil {
		closeFn()
		return nil, nil, err
	}
	return wc.Conn(), closeFn, nil
}

// StatRemote returns the size of a cloud file.
func (c *Client) StatRemote(ctx context.Context, remotePath string) (int64, error) {
	var out struct {
		Size  int64 `json:"size"`
		IsDir bool  `json:"isDir"`
	}
	if err := c.Call(ctx, "files.stat", map[string]any{"path": remotePath}, &out); err != nil {
		return 0, err
	}
	return out.Size, nil
}

// GetSubscriptions fetches the per-user subscription document.
func (c *Client) GetSubscriptions(ctx context.Context) (json.RawMessage, int64, bool, error) {
	var out struct {
		Found         bool            `json:"found"`
		Subscriptions json.RawMessage `json:"subscriptions"`
		UpdatedAt     int64           `json:"updatedAt"`
	}
	if err := c.Call(ctx, "subs.get", nil, &out); err != nil {
		return nil, 0, false, err
	}
	return out.Subscriptions, out.UpdatedAt, out.Found, nil
}

// PutSubscriptions stores the subscription document on the server.
func (c *Client) PutSubscriptions(ctx context.Context, payload []byte) (int64, error) {
	var out struct {
		UpdatedAt int64 `json:"updatedAt"`
	}
	if err := c.Call(ctx, "subs.put", map[string]any{"subscriptions": json.RawMessage(payload)}, &out); err != nil {
		return 0, err
	}
	return out.UpdatedAt, nil
}

// ExtractCloudArchive unpacks a zip/tar archive stored in the cloud space.
func (c *Client) ExtractCloudArchive(ctx context.Context, p string) (map[string]any, error) {
	var out map[string]any
	if err := c.Call(ctx, "files.extract", map[string]any{"path": p}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// DialTCP opens a raw TCP stream to target through the server relay.
func (c *Client) DialTCP(ctx context.Context, target string) (net.Conn, error) {
	wc, _, err := c.openStream(ctx, "relay")
	if err != nil {
		return nil, err
	}
	if err := wc.OpenStream("relay.tcp", map[string]any{"target": target}); err != nil {
		_ = wc.Close()
		return nil, err
	}
	return &relayConn{Conn: wc.Conn(), wc: wc}, nil
}

// relayConn keeps the wire client alive for the lifetime of the tunnel.
type relayConn struct {
	net.Conn
	wc *wire.Client
}

func (r *relayConn) Close() error {
	err := r.Conn.Close()
	_ = r.wc.Close()
	return err
}

// Close tears the session down.
func (c *Client) Close() error {
	c.mu.Lock()
	sess := c.sess
	c.sess = nil
	c.mu.Unlock()
	c.setStatus(func(s *Status) {
		s.Connected = false
		s.Transport = ""
	})
	if sess != nil {
		return sess.Close()
	}
	return nil
}

// JoinRemotePath joins remote POSIX segments.
func JoinRemotePath(parts ...string) string {
	return path.Join(parts...)
}

var _ = json.Marshal

// DownloadFrom resumes a cloud download at offset.
func (c *Client) DownloadFrom(ctx context.Context, remotePath, localPath string, offset int64, onProgress func(done, total int64)) (int64, error) {
	wc, closeFn, err := c.openStream(ctx, "file")
	if err != nil {
		return offset, err
	}
	defer closeFn()
	if err := wc.OpenStream("files.get", map[string]any{"path": remotePath, "offset": offset}); err != nil {
		return offset, err
	}
	conn := wc.Conn()
	var hdr struct {
		OK    bool   `json:"ok"`
		Size  int64  `json:"size"`
		Error string `json:"error"`
	}
	if err := wire.ReadFrame(conn, &hdr); err != nil {
		return offset, fmt.Errorf("download header: %w", err)
	}
	if !hdr.OK {
		msg := hdr.Error
		if msg == "" {
			msg = "not found"
		}
		return offset, fmt.Errorf("download failed: %s", msg)
	}
	if err := os.MkdirAll(filepathDir(localPath), 0o755); err != nil {
		return offset, err
	}
	flags := os.O_CREATE | os.O_WRONLY
	if offset > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(localPath, flags, 0o644)
	if err != nil {
		return offset, err
	}
	defer f.Close()
	total := offset + hdr.Size
	done := offset
	buf := make([]byte, 256*1024)
	last := time.Now()
	for done-offset < hdr.Size {
		want := int64(len(buf))
		if rem := hdr.Size - (done - offset); rem < want {
			want = rem
		}
		n, rerr := conn.Read(buf[:want])
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return done, werr
			}
			done += int64(n)
			if onProgress != nil && time.Since(last) > 150*time.Millisecond {
				last = time.Now()
				onProgress(done, total)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return done, rerr
		}
	}
	if onProgress != nil {
		onProgress(done, total)
	}
	if done-offset != hdr.Size {
		return done, fmt.Errorf("incomplete download: %d of %d bytes", done-offset, hdr.Size)
	}
	return done, f.Sync()
}

// UploadFrom resumes a cloud upload at offset.
func (c *Client) UploadFrom(ctx context.Context, localPath, remotePath string, offset int64, onProgress func(done, total int64)) (int64, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return offset, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return offset, err
	}
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return offset, err
		}
	}
	total := st.Size()
	remaining := total - offset
	if remaining < 0 {
		remaining = 0
	}
	wc, closeFn, err := c.openStream(ctx, "file")
	if err != nil {
		return offset, err
	}
	defer closeFn()
	if err := wc.OpenStream("files.put", map[string]any{
		"path": remotePath, "size": remaining, "truncate": offset == 0, "offset": offset,
	}); err != nil {
		return offset, err
	}
	conn := wc.Conn()
	buf := make([]byte, 256*1024)
	done := offset
	last := time.Now()
	for done-offset < remaining {
		select {
		case <-ctx.Done():
			return done, ctx.Err()
		default:
		}
		n, rerr := f.Read(buf)
		if n > 0 {
			if _, werr := conn.Write(buf[:n]); werr != nil {
				return done, werr
			}
			done += int64(n)
			if onProgress != nil && time.Since(last) > 150*time.Millisecond {
				last = time.Now()
				onProgress(done, total)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return done, rerr
		}
	}
	if onProgress != nil {
		onProgress(done, total)
	}
	var ack struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		Detail string `json:"detail"`
	}
	if err := wire.ReadFrame(conn, &ack); err != nil {
		return done, fmt.Errorf("upload acknowledgement failed: %w", err)
	}
	if !ack.OK {
		detail := ack.Detail
		if detail == "" {
			detail = ack.Error
		}
		return done, fmt.Errorf("upload rejected: %s", detail)
	}
	return done, nil
}
