package netdisk

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestLiveBaiduList(t *testing.T) {
	cookie := os.Getenv("WT_BAIDU_COOKIE")
	if cookie == "" {
		t.Skip("no cookie")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c := NewBaiduClient(nil)
	account := Account{ID: "live", Provider: ProviderBaidu}
	res, err := c.ListFiles(ctx, account, cookie, ListFilesRequest{Path: "/", PageSize: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, f := range res.Files {
		t.Logf("%s  id=%s  size=%d", f.Name, f.ID, f.Size)
	}
}

