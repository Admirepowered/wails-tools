package dnsx

import (
	"context"
	"testing"
	"time"
)

func TestLiveResolveCfYfjc(t *testing.T) {
	raw := "sdns://AgcAAAAAAAAACTIyMy41LjUuNSCY49XlNq8pWM0vfxT3BO9KJ20l4zzWXy5l9eTycnwTMA5kbnMuYWxpZG5zLmNvbQovZG5zLXF1ZXJ5"
	srv, err := ParseServer(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	res := New(ModeDoH, []Server{srv}, func(f string, a ...any) { t.Logf(f, a...) })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ips, err := res.LookupAll(ctx, "cf.yfjc.sbs")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	t.Logf("cf.yfjc.sbs -> %v", ips)
}
