// Package updater checks for, downloads and applies client updates from the
// WailsTools server or from GitHub (with SNI/DNS bypass).
package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"wailstools/internal/githubx"
	"wailstools/internal/serverlink"
	"wailstools/internal/settings"
	"wailstools/internal/version"
	"wailstools/internal/wire"
)

// Release describes one available build.
type Release struct {
	ID        string `json:"id"`
	Platform  string `json:"platform"`
	Arch      string `json:"arch"`
	Channel   string `json:"channel"`
	Version   string `json:"version"`
	Filename  string `json:"filename"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256,omitempty"`
	Notes     string `json:"notes,omitempty"`
	Source    string `json:"source"`
	URL       string `json:"url,omitempty"`
	Downloads int64  `json:"downloads,omitempty"`
	Mandatory bool   `json:"mandatory,omitempty"`
}

// CheckResult reports the outcome of an update check.
type CheckResult struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Available bool   `json:"available"`
	Source    string `json:"source"`
	// CurrentHash is the SHA-256 of the running executable.
	CurrentHash string `json:"currentHash,omitempty"`
	// ReleaseHash is the SHA-256 the release expects.
	ReleaseHash string `json:"releaseHash,omitempty"`
	// SameBuild reports that the release is byte-identical to the running one.
	SameBuild bool            `json:"sameBuild"`
	Release   *Release        `json:"release,omitempty"`
	Message   string          `json:"message"`
	Error     string          `json:"error,omitempty"`
	GitHub    *githubx.Result `json:"github,omitempty"`
	CheckedAt string          `json:"checkedAt"`
}

// Manager performs update operations.
type Manager struct {
	store *settings.Store
	link  *serverlink.Client
	logf  func(string, ...any)
}

// New builds an update manager.
func New(store *settings.Store, link *serverlink.Client, logf func(string, ...any)) *Manager {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Manager{store: store, link: link, logf: logf}
}

// Current returns the running version.
func (m *Manager) Current() string { return version.Version }

// CompareVersions compares dotted versions; returns 1 when a > b.
func CompareVersions(a, b string) int {
	pa := strings.Split(strings.TrimPrefix(strings.TrimSpace(a), "v"), ".")
	pb := strings.Split(strings.TrimPrefix(strings.TrimSpace(b), "v"), ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var va, vb int
		if i < len(pa) {
			va, _ = strconv.Atoi(strings.TrimSpace(pa[i]))
		}
		if i < len(pb) {
			vb, _ = strconv.Atoi(strings.TrimSpace(pb[i]))
		}
		if va != vb {
			if va > vb {
				return 1
			}
			return -1
		}
	}
	return 0
}

// SelfHash returns the SHA-256 of the running executable.
func SelfHash() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return fileSHA256(exe)
}

// Check queries the configured sources for a newer build. A release counts as
// an update when its hash differs from the running executable (default), or
// when the version is newer.
func CheckHashComparison(currentHash string, rel Release) (available bool, sameBuild bool) {
	if rel.SHA256 != "" && currentHash != "" {
		same := strings.EqualFold(rel.SHA256, currentHash)
		return !same, same
	}
	return CompareVersions(rel.Version, version.Version) > 0, false
}

// Check queries the configured sources for a newer build.
func (m *Manager) Check(ctx context.Context) (CheckResult, error) {
	cfg := m.store.Get().Update
	cur := version.Version
	selfHash, _ := SelfHash()
	res := CheckResult{Current: cur, CheckedAt: time.Now().Format(time.RFC3339), Source: cfg.Source, CurrentHash: selfHash}

	// Updates are always pulled from the sync server; the GitHub path was
	// removed and the update check never uses a proxy.
	res.Source = "server"
	var serverErr error
	{
		rel, err := m.checkServer(ctx, cfg.Channel)
		if err == nil && rel != nil {
			res.Release = rel
			res.Latest = rel.Version
			res.Source = "server"
			res.Available, res.SameBuild = CheckHashComparison(selfHash, *rel)
			res.CurrentHash = selfHash
			res.ReleaseHash = rel.SHA256
			if !res.Available {
				res.Message = "已是最新构建（哈希一致）"
			} else {
				res.Message = "发现新构建（哈希不同）"
			}
			m.recordCheck()
			return res, nil
		}
		if err != nil {
			serverErr = err
			m.logf("update check via server failed: %v", err)
		}
	}

	res.Latest = cur
	res.Message = "无法从同步服务器获取更新"
	if serverErr != nil {
		res.Error = serverErr.Error()
	}
	m.recordCheck()
	if res.Error == "" {
		return res, nil
	}
	return res, errors.New(res.Error)
}

func (m *Manager) recordCheck() {
	_, _ = m.store.Update(func(s *settings.Settings) error {
		s.Update.LastCheckAt = time.Now().Format(time.RFC3339)
		return nil
	})
}

func (m *Manager) checkServer(ctx context.Context, channel string) (*Release, error) {
	if m.link == nil || !m.link.Available() {
		return nil, errors.New("同步服务器未连接")
	}
	var out struct {
		Found   bool    `json:"found"`
		Release Release `json:"release"`
	}
	if err := m.link.Call(ctx, "update.latest", map[string]any{
		"platform": runtime.GOOS,
		"arch":     runtime.GOARCH,
		"channel":  channel,
	}, &out); err != nil {
		return nil, err
	}
	if !out.Found || out.Release.ID == "" {
		return nil, nil
	}
	rel := out.Release
	rel.Source = "server"
	return &rel, nil
}

func (m *Manager) githubClient(cfg settings.UpdateSettings) *githubx.Client {
	return githubx.New(githubx.Config{
		Fragment: cfg.GitHubFragment,
		Timeout:  20 * time.Second,
	})
}

type githubReleasePayload struct {
	TagName string `json:"tag_name"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
	Assets  []struct {
		Name               string `json:"name"`
		Size               int64  `json:"size"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func (m *Manager) checkGitHub(ctx context.Context, cfg settings.UpdateSettings) (*Release, *githubx.Result, error) {
	repo := strings.TrimSpace(cfg.GitHubRepo)
	if repo == "" {
		repo = "Admirepowered/wails-tools"
	}
	client := m.githubClient(cfg)
	var payload githubReleasePayload
	res, err := client.GetJSON(ctx, "https://api.github.com/repos/"+repo+"/releases/latest", &payload, nil)
	if err != nil {
		return nil, res, err
	}
	asset, shaAsset := pickGitHubAsset(payload.Assets, runtime.GOOS, runtime.GOARCH)
	if asset == nil {
		return nil, res, fmt.Errorf("GitHub 最新版本 %s 没有匹配 %s/%s 的安装包", payload.TagName, runtime.GOOS, runtime.GOARCH)
	}
	rel := &Release{
		ID:       payload.TagName,
		Platform: runtime.GOOS,
		Arch:     runtime.GOARCH,
		Channel:  cfg.Channel,
		Version:  strings.TrimPrefix(payload.TagName, "v"),
		Filename: asset.Name,
		Size:     asset.Size,
		Notes:    payload.Body,
		Source:   "github",
		URL:      asset.BrowserDownloadURL,
	}
	if shaAsset != nil {
		if sum, err := m.fetchGitHubSHA(ctx, client, shaAsset.BrowserDownloadURL); err == nil {
			rel.SHA256 = sum
		}
	}
	return rel, res, nil
}

func (m *Manager) fetchGitHubSHA(ctx context.Context, client *githubx.Client, url string) (string, error) {
	resp, _, err := client.Do(ctx, http.MethodGet, url, nil, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("sha256 asset returned %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return "", errors.New("empty sha256 file")
	}
	return strings.ToLower(fields[0]), nil
}

type githubAsset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// pickGitHubAsset selects the installer matching the platform, preferring names
// that mention the arch, and finds an optional ".sha256" sidecar.
func pickGitHubAsset(assets []struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
}, platform, arch string) (*githubAsset, *githubAsset) {
	platform = strings.ToLower(platform)
	arch = strings.ToLower(arch)
	var best *githubAsset
	var bestScore int
	var sha *githubAsset
	for i := range assets {
		name := strings.ToLower(assets[i].Name)
		a := &githubAsset{Name: assets[i].Name, Size: assets[i].Size, BrowserDownloadURL: assets[i].BrowserDownloadURL}
		if strings.HasSuffix(name, ".sha256") {
			base := strings.TrimSuffix(name, ".sha256")
			if strings.Contains(base, platform) {
				sha = a
			}
			continue
		}
		if !strings.Contains(name, platform) {
			continue
		}
		score := 1
		if strings.Contains(name, arch) {
			score += 2
		}
		switch {
		case strings.HasSuffix(name, ".exe") && platform == "windows":
			score += 3
		case strings.HasSuffix(name, ".tar.gz"), strings.HasSuffix(name, ".zip"):
			score++
		}
		if best == nil || score > bestScore {
			best, bestScore = a, score
		}
	}
	return best, sha
}

// Download fetches a release into the update staging directory.
func (m *Manager) Download(ctx context.Context, rel Release, onProgress func(done, total int64)) (string, error) {
	dir, err := stagingDir()
	if err != nil {
		return "", err
	}
	name := rel.Filename
	if name == "" {
		name = "WailsTools-update"
	}
	target := filepath.Join(dir, filepath.Base(name))
	_ = os.Remove(target)

	switch rel.Source {
	case "github":
		if err := m.downloadGitHub(ctx, rel, target, onProgress); err != nil {
			return "", err
		}
	default:
		if err := m.downloadServer(ctx, rel, target, onProgress); err != nil {
			return "", err
		}
	}
	if rel.SHA256 != "" {
		sum, err := fileSHA256(target)
		if err != nil {
			return "", err
		}
		if !strings.EqualFold(sum, rel.SHA256) {
			_ = os.Remove(target)
			return "", fmt.Errorf("校验失败：期望 %s，实际 %s", rel.SHA256, sum)
		}
		m.logf("update checksum verified: %s", sum)
	}
	return target, nil
}

func (m *Manager) downloadServer(ctx context.Context, rel Release, target string, onProgress func(int64, int64)) error {
	if m.link == nil || !m.link.Available() {
		return errors.New("同步服务器未连接")
	}
	conn, closeFn, err := m.link.OpenStream(ctx, "update.get", map[string]any{"id": rel.ID})
	if err != nil {
		return err
	}
	defer closeFn()
	var hdr struct {
		OK       bool   `json:"ok"`
		Size     int64  `json:"size"`
		Filename string `json:"filename"`
		SHA256   string `json:"sha256"`
		Error    string `json:"error"`
	}
	if err := wire.ReadFrame(conn, &hdr); err != nil {
		return fmt.Errorf("读取更新信息失败: %w", err)
	}
	if !hdr.OK {
		return fmt.Errorf("服务器拒绝下载: %s", hdr.Error)
	}
	f, err := os.Create(target)
	if err != nil {
		return err
	}
	defer f.Close()
	var done int64
	buf := make([]byte, 256*1024)
	last := time.Now()
	for done < hdr.Size {
		want := int64(len(buf))
		if rem := hdr.Size - done; rem < want {
			want = rem
		}
		n, rerr := conn.Read(buf[:want])
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
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
			return rerr
		}
	}
	if onProgress != nil {
		onProgress(done, hdr.Size)
	}
	if done != hdr.Size {
		return fmt.Errorf("下载不完整：%d/%d", done, hdr.Size)
	}
	return f.Sync()
}

func (m *Manager) downloadGitHub(ctx context.Context, rel Release, target string, onProgress func(int64, int64)) error {
	cfg := m.store.Get().Update
	client := m.githubClient(cfg)
	resp, _, err := client.Do(ctx, http.MethodGet, rel.URL, nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub 返回 %d", resp.StatusCode)
	}
	f, err := os.Create(target)
	if err != nil {
		return err
	}
	defer f.Close()
	total := resp.ContentLength
	var done int64
	buf := make([]byte, 256*1024)
	last := time.Now()
	for {
		n, rerr := resp.Body.Read(buf)
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
	if onProgress != nil {
		onProgress(done, total)
	}
	return f.Sync()
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// stagingDir returns the directory where downloads are staged.
func stagingDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "WailsTools", "updates")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// CleanupOld removes the backup left behind by a previous self-update.
func CleanupOld() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	_ = os.Remove(exe + ".old")
}

// DescribeTransport summarises how a GitHub request travelled.
func DescribeTransport(res *githubx.Result) string {
	if res == nil {
		return ""
	}
	if res.Fragment && res.UsedIP != "" {
		return "IP " + res.UsedIP + " + 分片"
	}
	if res.UsedIP != "" {
		return "IP " + res.UsedIP
	}
	if res.Fragment {
		return "直连 + 分片"
	}
	return "直连"
}

// parseSHAFromNotes extracts a "sha256: ..." line if the release notes carry it.
func parseSHAFromNotes(notes string) string {
	for _, line := range strings.Split(notes, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToLower(line), "sha256:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "sha256:"))
		}
	}
	return ""
}

var _ = json.Marshal
var _ = net.JoinHostPort
