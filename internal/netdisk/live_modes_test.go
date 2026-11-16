package netdisk

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"
)

func fetchWith(client *http.Client, link DownloadLink, label string) {
	req, _ := http.NewRequest(http.MethodGet, link.URL, nil)
	req.Header.Set("User-Agent", link.UserAgent)
	req.Header.Set("Cookie", link.Cookie)
	if link.Referer != "" {
		req.Header.Set("Referer", link.Referer)
	}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("%-12s GET: %v\n", label, err)
		return
	}
	n, _ := io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	fmt.Printf("%-12s status=%d bytes=%d\n", label, resp.StatusCode, n)

	for _, rng := range []string{"bytes=0-0", "bytes=0-1", "bytes=0-1023", "bytes=0-65535"} {
		r, _ := http.NewRequest(http.MethodGet, link.URL, nil)
		r.Header.Set("User-Agent", link.UserAgent)
		r.Header.Set("Cookie", link.Cookie)
		r.Header.Set("Range", rng)
		rr, err := client.Do(r)
		if err != nil {
			fmt.Printf("%-12s %s err=%v\n", label, rng, err)
			continue
		}
		n, _ := io.Copy(io.Discard, io.LimitReader(rr.Body, 1<<20))
		rr.Body.Close()
		fmt.Printf("%-12s %-14s status=%d bytes=%d\n", label, rng, rr.StatusCode, n)
	}
	req2, _ := http.NewRequest(http.MethodGet, link.URL, nil)
	req2.Header.Set("User-Agent", link.UserAgent)
	req2.Header.Set("Cookie", link.Cookie)
	req2.Header.Set("Range", "bytes=0-65535")
	resp2, err := client.Do(req2)
	if err != nil {
		fmt.Printf("%-12s RANGE: %v\n", label, err)
		return
	}
	n2, _ := io.Copy(io.Discard, io.LimitReader(resp2.Body, 1<<20))
	resp2.Body.Close()
	fmt.Printf("%-12s range status=%d bytes=%d\n", label, resp2.StatusCode, n2)
}

func TestLiveBaiduFetchModes(t *testing.T) {
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
	t.Logf("ua=%q", link.UserAgent)
	fetchWith(&http.Client{Timeout: 60 * time.Second}, link, "direct")

	if pu, err := url.Parse("http://127.0.0.1:7890"); err == nil {
		proxied := &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
		fetchWith(proxied, link, "via-proxy")
	}
}

