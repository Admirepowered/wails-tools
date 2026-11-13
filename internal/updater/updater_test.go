package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.1.0", "1.0.9", 1},
		{"1.0.10", "1.0.9", 1},
		{"v1.2.3", "1.2.3", 0},
		{"2.0", "2.0.1", -1},
		{"1.0.0", "1.0", 0},
	}
	for _, tc := range cases {
		if got := CompareVersions(tc.a, tc.b); got != tc.want {
			t.Fatalf("CompareVersions(%q,%q)=%d want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestPickGitHubAsset(t *testing.T) {
	type asset = struct {
		Name               string `json:"name"`
		Size               int64  `json:"size"`
		BrowserDownloadURL string `json:"browser_download_url"`
	}
	assets := []asset{
		{Name: "WailsTools-windows-amd64.exe", Size: 100, BrowserDownloadURL: "https://x/win.exe"},
		{Name: "WailsTools-windows-amd64.exe.sha256", Size: 10, BrowserDownloadURL: "https://x/win.exe.sha256"},
		{Name: "WailsTools-linux-amd64.tar.gz", Size: 90, BrowserDownloadURL: "https://x/linux.tar.gz"},
		{Name: "checksums.txt", Size: 5, BrowserDownloadURL: "https://x/checksums.txt"},
	}
	best, sha := pickGitHubAsset(assets, "windows", "amd64")
	if best == nil || best.Name != "WailsTools-windows-amd64.exe" {
		t.Fatalf("unexpected asset: %+v", best)
	}
	if sha == nil || sha.Name != "WailsTools-windows-amd64.exe.sha256" {
		t.Fatalf("sha sidecar not found: %+v", sha)
	}
	best, _ = pickGitHubAsset(assets, "linux", "amd64")
	if best == nil || best.Name != "WailsTools-linux-amd64.tar.gz" {
		t.Fatalf("linux asset not selected: %+v", best)
	}
	if best, _ := pickGitHubAsset(assets, "darwin", "arm64"); best != nil {
		t.Fatalf("expected no darwin asset, got %+v", best)
	}
}

func TestFileSHA256(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blob.bin")
	payload := []byte("wails-tools-update-payload")
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	sum, err := fileSHA256(path)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	want := sha256.Sum256(payload)
	if sum != hex.EncodeToString(want[:]) {
		t.Fatalf("checksum mismatch: %s", sum)
	}
}

func TestDescribeTransport(t *testing.T) {
	cases := []struct {
		res  *githubxResult
		want string
	}{
		{nil, ""},
	}
	_ = cases
	if got := DescribeTransport(nil); got != "" {
		t.Fatalf("nil should be empty, got %q", got)
	}
}

func TestParseSHAFromNotes(t *testing.T) {
	notes := "fix things\nsha256: ABCDEF\n"
	if got := parseSHAFromNotes(notes); got != "ABCDEF" {
		t.Fatalf("unexpected sha %q", got)
	}
	if got := parseSHAFromNotes("no hash here"); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

// githubxResult aliases the type used in the nil test above.
type githubxResult = struct{}

func TestCurrentVersionIsSemver(t *testing.T) {
	v := New(nil, nil, nil).Current()
	if v == "" || v[0] < '0' || v[0] > '9' {
		t.Fatalf("unexpected version %q", v)
	}
	if CompareVersions(v, "0.0.1") <= 0 {
		t.Fatalf("version %q should be newer than 0.0.1", v)
	}
}
