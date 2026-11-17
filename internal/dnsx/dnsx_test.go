package dnsx

import (
	"context"
	"testing"
	"time"
)

func TestParseAliDNSStamp(t *testing.T) {
	raw := "sdns://AgcAAAAAAAAACTIyMy41LjUuNSCY49XlNq8pWM0vfxT3BO9KJ20l4zzWXy5l9eTycnwTMA5kbnMuYWxpZG5zLmNvbQovZG5zLXF1ZXJ5"
	srv, err := ParseServer(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	t.Logf("host=%s path=%s addr=%s endpoint=%s", srv.Host, srv.Path, srv.Addr, srv.Endpoint())
	if srv.Host != "dns.alidns.com" || srv.Path != "/dns-query" || srv.Addr != "223.5.5.5:443" {
		t.Fatalf("unexpected: %+v", srv)
	}
}

func TestLiveDoHLookup(t *testing.T) {
	raw := "sdns://AgcAAAAAAAAACTIyMy41LjUuNSCY49XlNq8pWM0vfxT3BO9KJ20l4zzWXy5l9eTycnwTMA5kbnMuYWxpZG5zLmNvbQovZG5zLXF1ZXJ5"
	srv, err := ParseServer(raw)
	if err != nil {
		t.Skipf("parse: %v", err)
	}
	res := New(ModeDoH, []Server{srv}, func(f string, a ...any) { t.Logf(f, a...) })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ip, err := res.Lookup(ctx, "www.baidu.com")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	t.Logf("www.baidu.com -> %s", ip)
}
