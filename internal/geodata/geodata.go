// Package geodata downloads and queries the MetaCubeX geoip.metadb database so
// the proxy engine can send traffic for selected countries direct.
package geodata

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/oschwald/maxminddb-golang"

	"wailstools/internal/githubx"
)

// DownloadURL is the upstream database location.
const DownloadURL = "https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geoip.metadb"

// Status describes the local database.
type Status struct {
	Ready       bool   `json:"ready"`
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	UpdatedAt   string `json:"updatedAt,omitempty"`
	Downloading bool   `json:"downloading"`
	Error       string `json:"error,omitempty"`
}

// DB wraps the maxmind database.
type DB struct {
	mu     sync.RWMutex
	path   string
	reader *maxminddb.Reader
	status Status
}

// Open creates a manager rooted at dir (the database may be missing). Common
// alternative file names are accepted so a manually placed database is found.
func Open(dir string) *DB {
	candidates := []string{"geoip.metadb", "geoip.mmdb", "Country.mmdb", "country.mmdb"}
	path := filepath.Join(dir, candidates[0])
	for _, name := range candidates {
		cand := filepath.Join(dir, name)
		if info, err := os.Stat(cand); err == nil && info.Size() > 0 {
			path = cand
			break
		}
	}
	db := &DB{path: path, status: Status{Path: path}}
	db.load()
	return db
}

func (d *DB) load() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.reader != nil {
		_ = d.reader.Close()
		d.reader = nil
	}
	info, err := os.Stat(d.path)
	if err != nil {
		d.status.Ready = false
		return
	}
	reader, err := maxminddb.Open(d.path)
	if err != nil {
		d.status.Ready = false
		d.status.Error = err.Error()
		return
	}
	d.reader = reader
	d.status.Ready = true
	d.status.Size = info.Size()
	d.status.UpdatedAt = info.ModTime().Format(time.RFC3339)
	d.status.Error = ""
}

// Status reports the database state, re-checking the file on disk first so a
// manually placed or replaced database is picked up immediately.
func (d *DB) Status() Status {
	d.refresh()
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.status
}

// refresh reopens the database when the file appeared, changed or vanished.
func (d *DB) refresh() {
	info, err := os.Stat(d.path)
	d.mu.RLock()
	ready := d.status.Ready
	size := d.status.Size
	updated := d.status.UpdatedAt
	d.mu.RUnlock()

	if err != nil {
		if ready {
			d.load()
		}
		return
	}
	now := info.ModTime().Format(time.RFC3339)
	if !ready || info.Size() != size || now != updated {
		d.load()
	}
}

// Path returns the database file path.
func (d *DB) Path() string { return d.path }

// Download fetches (or refreshes) the database, falling back to the GitHub
// SNI-bypass client when direct access fails.
func (d *DB) Download(ctx context.Context, onProgress func(done, total int64)) error {
	d.mu.Lock()
	d.status.Downloading = true
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.status.Downloading = false
		d.mu.Unlock()
	}()

	if err := os.MkdirAll(filepath.Dir(d.path), 0o755); err != nil {
		return err
	}
	tmp := d.path + ".download"
	_ = os.Remove(tmp)

	err := d.downloadDirect(ctx, tmp, onProgress)
	if err != nil {
		if derr := d.downloadBypass(ctx, tmp, onProgress); derr != nil {
			d.mu.Lock()
			d.status.Error = derr.Error()
			d.mu.Unlock()
			return fmt.Errorf("下载 geoip.metadb 失败: %v（直连: %v）", derr, err)
		}
	}
	if err := os.Rename(tmp, d.path); err != nil {
		return err
	}
	d.load()
	if st := d.Status(); !st.Ready {
		if st.Error != "" {
			return fmt.Errorf("geoip.metadb 无法解析: %s", st.Error)
		}
		return errors.New("geoip.metadb 无法解析")
	}
	return nil
}

func (d *DB) downloadDirect(ctx context.Context, target string, onProgress func(int64, int64)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, DownloadURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "WailsTools/1.0")
	client := &http.Client{Timeout: 3 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return writeBody(resp.Body, resp.ContentLength, target, onProgress)
}

func (d *DB) downloadBypass(ctx context.Context, target string, onProgress func(int64, int64)) error {
	client := githubx.New(githubx.Config{Fragment: true, Timeout: 30 * time.Second})
	resp, _, err := client.Do(ctx, http.MethodGet, DownloadURL, nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return writeBody(resp.Body, resp.ContentLength, target, onProgress)
}

func writeBody(body io.Reader, total int64, target string, onProgress func(int64, int64)) error {
	f, err := os.Create(target)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, 256*1024)
	var done int64
	last := time.Now()
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
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
			return rerr
		}
	}
	return f.Sync()
}

// record matches the MetaCubeX geoip.metadb schema.
type record struct {
	Country struct {
		ISO string `maxminddb:"iso_code"`
	} `maxminddb:"country"`
	Code string `maxminddb:"code"`
}

// Lookup returns the ISO country code for an IP, or "" when unknown.
func (d *DB) Lookup(ip net.IP) string {
	if ip == nil {
		return ""
	}
	d.mu.RLock()
	reader := d.reader
	d.mu.RUnlock()
	if reader == nil {
		return ""
	}
	var rec record
	if err := reader.Lookup(ip, &rec); err != nil {
		return ""
	}
	if rec.Country.ISO != "" {
		return strings.ToUpper(rec.Country.ISO)
	}
	if rec.Code != "" {
		return strings.ToUpper(rec.Code)
	}
	return ""
}

// Countries lists the ISO codes present in the database (best effort).
func (d *DB) Countries() []string {
	d.refresh()
	d.mu.RLock()
	reader := d.reader
	d.mu.RUnlock()
	if reader == nil {
		return nil
	}
	seen := map[string]bool{}
	networks := reader.Networks()
	for networks.Next() {
		var rec record
		if _, err := networks.Network(&rec); err != nil {
			continue
		}
		code := rec.Country.ISO
		if code == "" {
			code = rec.Code
		}
		if code != "" {
			seen[strings.ToUpper(code)] = true
		}
	}
	out := make([]string, 0, len(seen))
	for code := range seen {
		out = append(out, code)
	}
	return out
}
