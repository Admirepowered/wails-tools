package miproxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

// TestInboundConcurrency proves the local listener serves many requests in
// parallel instead of serialising them.
func TestInboundConcurrency(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		_, _ = io.WriteString(w, "ok")
	}))
	defer origin.Close()

	srv := startDirectServer(t, "", "")
	proxyURL, _ := url.Parse("http://" + srv.Addr())
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL), MaxIdleConnsPerHost: 32},
		Timeout:   10 * time.Second,
	}

	const n = 12
	start := time.Now()
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := client.Get(origin.URL)
			if err != nil {
				errs <- err
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent request failed: %v", err)
	}
	elapsed := time.Since(start)
	// Serialised handling would take n*150ms (1.8s); parallel should be far less.
	if elapsed > time.Duration(n)*150*time.Millisecond/2 {
		t.Fatalf("requests appear serialised: %v for %d requests", elapsed, n)
	}
	if srv.Stats().Accepted < n {
		t.Fatalf("expected at least %d accepted connections, got %d", n, srv.Stats().Accepted)
	}
}
