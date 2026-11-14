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
	"sort"
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
	mu        sync.RWMutex
	path      string
	active    string
	reader    *maxminddb.Reader
	status    Status
	fallbacks []*http.Client
}

// SetFallbackClients configures clients tried after the SNI-bypass attempt.
// The usual order is proxy first, then direct.
func (d *DB) SetFallbackClients(clients ...*http.Client) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.fallbacks = clients
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
	db := &DB{path: path, active: path + ".active", status: Status{Path: path}}
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
	// 打开副本而不是源文件：这样用户随时可以替换/删除 geoip.metadb，
	// Windows 也不会因为本进程持有句柄而报「文件正在使用」。
	if d.active != "" && d.active != d.path {
		if err := copyFile(d.path, d.active); err != nil {
			d.status.Ready = false
			d.status.Error = err.Error()
			return
		}
	}
	openPath := d.path
	if d.active != "" && d.active != d.path {
		openPath = d.active
	}
	reader, err := maxminddb.Open(openPath)
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

	var errs []string
	ok := false
	// 访问 GitHub 一律先走 SNI 绕过，失败后依次回退代理、直连。
	if err := d.downloadBypass(ctx, tmp, onProgress); err == nil {
		ok = true
	} else {
		errs = append(errs, "sni: "+err.Error())
		d.mu.RLock()
		fallbacks := append([]*http.Client(nil), d.fallbacks...)
		d.mu.RUnlock()
		for _, client := range fallbacks {
			if client == nil {
				continue
			}
			if err := d.downloadWith(ctx, client, tmp, onProgress); err == nil {
				ok = true
				break
			} else {
				errs = append(errs, "fallback: "+err.Error())
			}
		}
		if !ok {
			if err := d.downloadDirect(ctx, tmp, onProgress); err == nil {
				ok = true
			} else {
				errs = append(errs, "direct: "+err.Error())
			}
		}
	}
	if !ok {
		d.mu.Lock()
		d.status.Error = strings.Join(errs, " | ")
		d.mu.Unlock()
		return fmt.Errorf("下载 geoip.metadb 失败: %s", strings.Join(errs, " | "))
	}
	// 本进程只打开副本，替换源文件前仍显式释放句柄，避免任何残留占用。
	d.mu.Lock()
	if d.reader != nil {
		_ = d.reader.Close()
		d.reader = nil
	}
	d.status.Ready = false
	d.mu.Unlock()
	if d.active != "" {
		_ = os.Remove(d.active)
	}
	if err := os.Rename(tmp, d.path); err != nil {
		_ = os.Remove(d.path)
		if err2 := os.Rename(tmp, d.path); err2 != nil {
			d.mu.Lock()
			d.status.Error = "替换文件失败（被占用）: " + err2.Error()
			d.mu.Unlock()
			return fmt.Errorf("替换 geoip.metadb 失败: %w", err2)
		}
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

func (d *DB) downloadWith(ctx context.Context, client *http.Client, target string, onProgress func(int64, int64)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, DownloadURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "WailsTools/1.0")
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

// LookupCodes returns every country code assigned to an IP. Meta-geoip records
// are either a bare string ("cn") or an array of strings (["us","ca"]).
func (d *DB) LookupCodes(ip net.IP) []string {
	if ip == nil {
		return nil
	}
	d.mu.RLock()
	reader := d.reader
	d.mu.RUnlock()
	if reader == nil {
		return nil
	}
	var single string
	if err := reader.Lookup(ip, &single); err == nil {
		single = strings.TrimSpace(single)
		if single != "" {
			return []string{strings.ToUpper(single)}
		}
	}
	var many []string
	if err := reader.Lookup(ip, &many); err == nil {
		out := make([]string, 0, len(many))
		for _, code := range many {
			code = strings.ToUpper(strings.TrimSpace(code))
			if code != "" {
				out = append(out, code)
			}
		}
		return out
	}
	return nil
}

// Lookup returns the primary country code for an IP, or "" when unknown.
func (d *DB) Lookup(ip net.IP) string {
	codes := d.LookupCodes(ip)
	if len(codes) == 0 {
		return ""
	}
	return codes[0]
}

// Countries lists the country codes present in the database.
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
		var single string
		if _, err := networks.Network(&single); err == nil {
			if code := strings.ToUpper(strings.TrimSpace(single)); code != "" {
				seen[code] = true
			}
			continue
		}
		var many []string
		if _, err := networks.Network(&many); err == nil {
			for _, code := range many {
				if code = strings.ToUpper(strings.TrimSpace(code)); code != "" {
					seen[code] = true
				}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for code := range seen {
		out = append(out, code)
	}
	sort.Strings(out)
	return out
}

// copyFile copies src to dst, replacing dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
