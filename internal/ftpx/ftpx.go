// Package ftpx provides FTP/FTPS client operations (browse, download, upload,
// rename, delete) with proxy-aware control and data connections.
package ftpx

import (
	"context"
	"crypto/tls"
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

	"github.com/jlaffaye/ftp"

	"wailstools/internal/proxyx"
	"wailstools/internal/settings"
)

// Config describes how to reach an FTP server.
type Config struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	User         string `json:"user"`
	Password     string `json:"password"`
	TLS          bool   `json:"tls"`          // implicit FTPS
	ExplicitFTPS bool   `json:"explicitFtps"` // AUTH TLS on port 21
	Passive      bool   `json:"passive"`
	TimeoutSec   int    `json:"timeoutSec"`
	Path         string `json:"path"` // remembered remote path
}

// Entry is a remote directory entry.
type Entry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	ModTime string `json:"modTime"`
}

// Progress mirrors sshmgr.Progress without importing it.
type Progress struct {
	Done     int64   `json:"done"`
	Total    int64   `json:"total"`
	Percent  float64 `json:"percent"`
	SpeedBps float64 `json:"speedBps"`
	Path     string  `json:"path"`
}

// Client is a live FTP connection.
type Client struct {
	mu     sync.Mutex
	cfg    Config
	conn   *ftp.ServerConn
	id     string
	closed bool
}

// Dialer opens FTP connections honouring a proxy rule.
type Dialer struct {
	rule settings.ProxyRule
}

// NewDialer builds a dialer for the ftp proxy scope.
func NewDialer(rule settings.ProxyRule) *Dialer { return &Dialer{rule: rule} }

func (d *Dialer) proxy() (*proxyx.Dialer, error) {
	if d == nil || !d.rule.Enabled() {
		return nil, nil
	}
	p, err := proxyx.New(d.rule)
	if err != nil {
		if errors.Is(err, proxyx.ErrNoProxy) {
			return nil, nil
		}
		return nil, err
	}
	return p, nil
}

// Connect establishes an FTP session.
func (d *Dialer) Connect(ctx context.Context, cfg Config, id string) (*Client, error) {
	port := cfg.Port
	if port <= 0 {
		if cfg.TLS {
			port = 990
		} else {
			port = 21
		}
	}
	timeout := time.Duration(cfg.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	addr := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", port))

	pd, err := d.proxy()
	if err != nil {
		return nil, err
	}

	opts := []ftp.DialOption{
		ftp.DialWithTimeout(timeout),
		ftp.DialWithShutTimeout(10 * time.Second),
	}
	if pd != nil {
		opts = append(opts, ftp.DialWithDialFunc(func(network, address string) (net.Conn, error) {
			return pd.DialContext(ctx, network, address)
		}))
	}
	tlsConf := &tls.Config{InsecureSkipVerify: true, ServerName: cfg.Host}
	if cfg.TLS {
		opts = append(opts, ftp.DialWithExplicitTLS(tlsConf))
	} else if cfg.ExplicitFTPS {
		opts = append(opts, ftp.DialWithExplicitTLS(tlsConf))
	}
	if !cfg.Passive {
		opts = append(opts, ftp.DialWithDisabledEPSV(true))
	}
	conn, err := ftp.Dial(addr, opts...)
	if err != nil {
		return nil, fmt.Errorf("ftp connect %s: %w", addr, err)
	}
	if err := conn.Login(cfg.User, cfg.Password); err != nil {
		_ = conn.Quit()
		return nil, fmt.Errorf("ftp login: %w", err)
	}
	return &Client{cfg: cfg, conn: conn, id: id, closed: false}, nil
}

// Config exposes the connection configuration.
func (c *Client) Config() Config { return c.cfg }

// Close ends the session.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return c.conn.Quit()
}

// List returns the entries of dir.
func (c *Client) List(dir string) ([]Entry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if dir == "" {
		wd, err := c.conn.CurrentDir()
		if err == nil {
			dir = wd
		} else {
			dir = "/"
		}
	}
	entries, err := c.conn.List(dir)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if e.Name == "." || e.Name == ".." {
			continue
		}
		isDir := e.Type == ftp.EntryTypeFolder
		if !isDir && e.Type == ftp.EntryTypeLink {
			// resolve symlink/dir heuristically
			if strings.HasPrefix(e.Target, "/") || e.Target != "" {
				isDir = strings.TrimSpace(e.Target) != ""
			}
		}
		out = append(out, Entry{
			Name:    e.Name,
			Path:    path.Join(dir, e.Name),
			IsDir:   isDir,
			Size:    int64(e.Size),
			Mode:    e.Name,
			ModTime: e.Time.UTC().Format(time.RFC3339),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// Pwd returns the current remote directory.
func (c *Client) Pwd() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.CurrentDir()
}

// Mkdir creates a remote directory.
func (c *Client) Mkdir(dir string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.MakeDir(dir)
}

// Remove deletes a file.
func (c *Client) Remove(p string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.Delete(p)
}

// RemoveDir deletes a directory (recursively).
func (c *Client) RemoveDir(p string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries, err := c.conn.List(p)
	if err == nil {
		for _, e := range entries {
			if e.Name == "." || e.Name == ".." {
				continue
			}
			child := path.Join(p, e.Name)
			if e.Type == ftp.EntryTypeFolder {
				_ = c.removeDirLocked(child)
			} else {
				_ = c.conn.Delete(child)
			}
		}
	}
	return c.conn.RemoveDir(p)
}

func (c *Client) removeDirLocked(p string) error {
	entries, err := c.conn.List(p)
	if err == nil {
		for _, e := range entries {
			if e.Name == "." || e.Name == ".." {
				continue
			}
			child := path.Join(p, e.Name)
			if e.Type == ftp.EntryTypeFolder {
				_ = c.removeDirLocked(child)
			} else {
				_ = c.conn.Delete(child)
			}
		}
	}
	return c.conn.RemoveDir(p)
}

// Rename renames a remote path.
func (c *Client) Rename(from, to string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.Rename(from, to)
}

// Download copies a remote file to localPath with progress reporting.
func (c *Client) Download(ctx context.Context, remotePath, localPath string, onProgress func(Progress)) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var total int64
	if size, err := c.conn.FileSize(remotePath); err == nil {
		total = size
	} else if entries, lerr := c.conn.List(path.Dir(remotePath)); lerr == nil {
		base := path.Base(remotePath)
		for _, e := range entries {
			if e.Name == base {
				total = int64(e.Size)
			}
		}
	}
	resp, err := c.conn.Retr(remotePath)
	if err != nil {
		return 0, err
	}
	defer resp.Close()

	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return 0, err
	}
	lf, err := os.Create(localPath)
	if err != nil {
		return 0, err
	}
	defer lf.Close()

	var done int64
	start := time.Now()
	last := start
	buf := make([]byte, 512*1024)
	for {
		select {
		case <-ctx.Done():
			return done, ctx.Err()
		default:
		}
		n, rerr := resp.Read(buf)
		if n > 0 {
			if _, werr := lf.Write(buf[:n]); werr != nil {
				return done, werr
			}
			done += int64(n)
			now := time.Now()
			if onProgress != nil && now.Sub(last) > 150*time.Millisecond {
				last = now
				onProgress(mkProgress(remotePath, done, total, start))
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
		onProgress(mkProgress(remotePath, done, total, start))
	}
	return done, lf.Sync()
}

func mkProgress(p string, done, total int64, start time.Time) Progress {
	pr := Progress{Done: done, Total: total, Path: p}
	if total > 0 {
		pr.Percent = float64(done) / float64(total) * 100
	}
	if el := time.Since(start).Seconds(); el > 0 {
		pr.SpeedBps = float64(done) / el
	}
	return pr
}

// Upload copies a local file to the remote host.
func (c *Client) Upload(ctx context.Context, localPath, remotePath string, onProgress func(Progress)) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	lf, err := os.Open(localPath)
	if err != nil {
		return 0, err
	}
	defer lf.Close()
	st, err := lf.Stat()
	if err != nil {
		return 0, err
	}
	if st.IsDir() {
		return 0, errors.New("local path is a directory")
	}
	if dir := path.Dir(remotePath); dir != "" && dir != "." && dir != "/" {
		_ = c.conn.MakeDir(dir)
	}
	rp := &readerProgress{r: lf, total: st.Size(), start: time.Now(), last: time.Now(), path: remotePath, progress: onProgress}
	if err := c.conn.Stor(remotePath, rp); err != nil {
		return rp.done, err
	}
	if onProgress != nil {
		onProgress(mkProgress(remotePath, rp.done, rp.total, rp.start))
	}
	return rp.done, nil
}

// readerProgress wraps an io.Reader to report upload progress.
type readerProgress struct {
	r        io.Reader
	done     int64
	total    int64
	start    time.Time
	last     time.Time
	path     string
	progress func(Progress)
}

func (r *readerProgress) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.done += int64(n)
		now := time.Now()
		if r.progress != nil && now.Sub(r.last) > 150*time.Millisecond {
			r.last = now
			r.progress(mkProgress(r.path, r.done, r.total, r.start))
		}
	}
	return n, err
}

// ListSimple is used by the UI for quick path validation.
func (c *Client) Exists(p string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.conn.FileSize(p)
	if err == nil {
		return true
	}
	_, err = c.conn.CurrentDir()
	if err != nil {
		return false
	}
	entries, err := c.conn.List(path.Dir(p))
	if err != nil {
		return false
	}
	base := path.Base(p)
	for _, e := range entries {
		if e.Name == base {
			return true
		}
	}
	return false
}

// DownloadFrom resumes a remote file download at offset.
func (c *Client) DownloadFrom(ctx context.Context, remotePath, localPath string, offset int64, onProgress func(Progress)) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var total int64
	if size, err := c.conn.FileSize(remotePath); err == nil {
		total = size
	}
	resp, err := c.conn.RetrFrom(remotePath, uint64(offset))
	if err != nil {
		return offset, err
	}
	defer resp.Close()

	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return offset, err
	}
	flags := os.O_CREATE | os.O_WRONLY
	if offset > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	lf, err := os.OpenFile(localPath, flags, 0o644)
	if err != nil {
		return offset, err
	}
	defer lf.Close()

	done := offset
	start := time.Now()
	last := start
	buf := make([]byte, 512*1024)
	for {
		select {
		case <-ctx.Done():
			return done, ctx.Err()
		default:
		}
		n, rerr := resp.Read(buf)
		if n > 0 {
			if _, werr := lf.Write(buf[:n]); werr != nil {
				return done, werr
			}
			done += int64(n)
			now := time.Now()
			if onProgress != nil && now.Sub(last) > 150*time.Millisecond {
				last = now
				onProgress(mkProgress(remotePath, done, total, start))
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
		onProgress(mkProgress(remotePath, done, total, start))
	}
	return done, lf.Sync()
}

// UploadFrom resumes a remote file upload at offset.
func (c *Client) UploadFrom(ctx context.Context, localPath, remotePath string, offset int64, onProgress func(Progress)) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	lf, err := os.Open(localPath)
	if err != nil {
		return offset, err
	}
	defer lf.Close()
	st, err := lf.Stat()
	if err != nil {
		return offset, err
	}
	if offset > 0 {
		if _, err := lf.Seek(offset, io.SeekStart); err != nil {
			return offset, err
		}
	}
	if dir := path.Dir(remotePath); dir != "" && dir != "." && dir != "/" {
		_ = c.conn.MakeDir(dir)
	}
	rp := &readerProgress{r: lf, done: offset, total: st.Size(), start: time.Now(), last: time.Now(), path: remotePath, progress: onProgress}
	if err := c.conn.StorFrom(remotePath, rp, uint64(offset)); err != nil {
		return rp.done, err
	}
	if onProgress != nil {
		onProgress(mkProgress(remotePath, rp.done, rp.total, rp.start))
	}
	return rp.done, nil
}
