package netdisk

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestLiveBaiduHeaderMatrix(t *testing.T) {
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
	client := &http.Client{Timeout: 60 * time.Second}
	try := func(label string, hdr map[string]string) {
		req, _ := http.NewRequest(http.MethodGet, link.URL, nil)
		req.Header.Set("User-Agent", link.UserAgent)
		req.Header.Set("Cookie", link.Cookie)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			fmt.Printf("%-28s err=%v\n", label, err)
			return
		}
		n, _ := io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		fmt.Printf("%-28s status=%d bytes=%d\n", label, resp.StatusCode, n)
	}
	try("range+referer", map[string]string{"Range": "bytes=0-65535", "Referer": "https://pan.baidu.com/"})
	try("range+accept", map[string]string{"Range": "bytes=0-65535", "Accept": "*/*"})
	try("range+ifrange(etag)", map[string]string{"Range": "bytes=0-65535", "If-Range": "W/whatever"})
	try("range+ifrange(wrong)", map[string]string{"Range": "bytes=0-65535", "If-Range": "\"abc\""})
	try("range-only", map[string]string{"Range": "bytes=0-65535"})
	try("no-headers", nil)
}
