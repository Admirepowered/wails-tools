package netdisk

import (
	"context"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestLiveBaiduFetch downloads the first bytes of the resolved link with the
// netdisk UA to prove the whole chain works.
func TestLiveBaiduFetch(t *testing.T) {
	cookie := os.Getenv("WT_BAIDU_COOKIE")
	fileID := os.Getenv("WT_BAIDU_FILE_ID")
	if cookie == "" || fileID == "" {
		t.Skip("env not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	c := NewBaiduClient(nil)
	link, err := c.ResolveDownload(ctx, Account{ID: "live"}, cookie, ResolveDownloadRequest{FileID: fileID, Path: "/x.zip"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, link.URL, nil)
	req.Header.Set("User-Agent", link.UserAgent)
	req.Header.Set("Cookie", link.Cookie)
	req.Header.Set("Referer", link.Referer)
	req.Header.Set("Range", "bytes=0-65535")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	n, _ := io.Copy(io.Discard, resp.Body)
	t.Logf("status=%d bytes=%d ua=%q", resp.StatusCode, n, link.UserAgent)
	if resp.StatusCode != 200 && resp.StatusCode != 206 {
		t.Fatalf("unexpected status %d", resp.StatusCode)
	}
}
