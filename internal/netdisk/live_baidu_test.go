package netdisk

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLiveBaiduDownload resolves a real link with a real cookie; skipped unless
// WT_BAIDU_COOKIE is set.
func TestLiveBaiduDownload(t *testing.T) {
	cookie := os.Getenv("WT_BAIDU_COOKIE")
	fileID := os.Getenv("WT_BAIDU_FILE_ID")
	if cookie == "" || fileID == "" {
		t.Skip("WT_BAIDU_COOKIE / WT_BAIDU_FILE_ID not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c := NewBaiduClient(nil)
	tpl, err := c.fetchAccountTpl(ctx, cookie)
	if err != nil {
		t.Fatalf("gettemplatevariable: %v", err)
	}
	t.Logf("tpl: sign1=%q sign3=%q timestamp=%q", tpl.Sign1, tpl.Sign3, tpl.Timestamp)
	sign, err := baiduAccountSign(tpl)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	t.Logf("sign=%q", sign)
	dlink, err := c.fetchAccountDLink(ctx, cookie, fileID, tpl, sign)
	if err != nil {
		t.Fatalf("dlink: %v", err)
	}
	t.Logf("dlink=%s", dlink)
	final, err := baiduFollowRedirect(ctx, dlink, cookie, baiduNetdiskUA)
	if err != nil {
		t.Fatalf("redirect: %v", err)
	}
	t.Logf("final=%s", final)
}
