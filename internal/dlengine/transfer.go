package dlengine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"wailstools/internal/proxyx"
	"wailstools/internal/settings"
)

// ErrTaskFailed marks a definitive task failure.
var ErrTaskFailed = errors.New("download failed")

type clientInfo struct {
	client *http.Client
	desc   string
}

func (e *Engine) clientFor(t Task) (clientInfo, error) {
	cfg := e.store.Get()
	rule := cfg.Proxy.Resolve(settings.ScopeHTTP)
	switch t.ProxyMode {
	case "off":
		rule = settings.ProxyRule{Mode: "off"}
	case "on":
		if !rule.Enabled() {
			return clientInfo{}, errors.New("proxy mode is \"on\" but no proxy is configured for the http scope")
		}
	}
	client, err := proxyx.HTTPClient(rule, 0)
	if err != nil {
		return clientInfo{}, err
	}
	desc := "direct"
	if rule.Enabled() {
		desc = rule.Mode + "://" + rule.URL
	}
	return clientInfo{client: client, desc: desc}, nil
}

func buildRequest(ctx context.Context, method, rawURL string, t Task) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return nil, err
	}
	ua := "WailsTools/1.0"
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "*/*")
	for k, v := range t.Headers {
		if strings.EqualFold(k, "Host") {
			continue
		}
		req.Header.Set(k, v)
	}
	if t.Referer != "" {
		req.Header.Set("Referer", t.Referer)
	}
	return req, nil
}

type probeResult struct {
	FinalURL  string
	Total     int64
	RangeOK   bool
	Filename  string
	ETag      string
	ProxyDesc string
}

func (e *Engine) probe(ctx context.Context, t Task) (*probeResult, error) {
	ci, err := e.clientFor(t)
	if err != nil {
		return nil, err
	}
	pr := &probeResult{ProxyDesc: ci.desc}

	// A ranged GET of one byte is the most reliable capability probe and also
	// reveals the real filename/length for redirecting links.
	req, err := buildRequest(ctx, http.MethodGet, t.URL, t)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", "bytes=0-0")
	resp, err := ci.client.Do(req)
	if err != nil {
		// Retry without the range header: some servers reject Range requests.
		req2, err2 := buildRequest(ctx, http.MethodGet, t.URL, t)
		if err2 != nil {
			return nil, err2
		}
		req2.Header.Set("Range", "")
		resp, err = ci.client.Do(req2)
		if err != nil {
			return nil, err
		}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8<<10))

	pr.FinalURL = resp.Request.URL.String()
	pr.ETag = resp.Header.Get("ETag")
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if name := filenameFromDisposition(cd); name != "" {
			pr.Filename = name
		}
	}
	switch resp.StatusCode {
	case http.StatusPartialContent:
		pr.RangeOK = true
		if cr := resp.Header.Get("Content-Range"); cr != "" {
			if idx := strings.LastIndex(cr, "/"); idx >= 0 {
				if total, err := strconv.ParseInt(strings.TrimSpace(cr[idx+1:]), 10, 64); err == nil {
					pr.Total = total
				}
			}
		}
	case http.StatusOK:
		pr.RangeOK = resp.Header.Get("Accept-Ranges") == "bytes"
		pr.Total = resp.ContentLength
		if pr.Total < 0 {
			pr.Total = 0
		}
		if resp.Header.Get("Accept-Ranges") == "none" {
			pr.RangeOK = false
		}
	default:
		return nil, &statusError{code: resp.StatusCode, status: resp.Status}
	}
	if pr.Total == 0 && pr.RangeOK {
		// HEAD fallback for length.
		if hreq, err := buildRequest(ctx, http.MethodHead, t.URL, t); err == nil {
			if hresp, err := ci.client.Do(hreq); err == nil {
				pr.Total = hresp.ContentLength
				_ = hresp.Body.Close()
			}
		}
	}
	return pr, nil
}

func filenameFromDisposition(cd string) string {
	parts := strings.Split(cd, ";")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(strings.ToLower(p), "filename*=") {
			v := p[len("filename*="):]
			if i := strings.LastIndex(v, "''"); i >= 0 {
				v = v[i+2:]
			}
			if dec, err := url.PathUnescape(strings.Trim(v, `"`)); err == nil {
				return dec
			}
		}
		if strings.HasPrefix(strings.ToLower(p), "filename=") {
			v := strings.Trim(p[len("filename="):], `"`)
			if dec, err := url.PathUnescape(v); err == nil {
				return dec
			}
			return v
		}
	}
	return ""
}

func (e *Engine) uniqueTarget(dir, name string) string {
	target := filepath.Join(dir, name)
	if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
		return target
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 1; i < 1000; i++ {
		cand := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, i, ext))
		if _, err := os.Stat(cand); errors.Is(err, os.ErrNotExist) {
			return cand
		}
	}
	return filepath.Join(dir, fmt.Sprintf("%s-%d%s", base, time.Now().UnixNano(), ext))
}

func (e *Engine) run(ctx context.Context, tr *taskRunner) {
	tr.mu.Lock()
	t := tr.task
	tr.mu.Unlock()

	fail := func(err error) {
		tr.mu.Lock()
		tr.task.Status = StatusFailed
		tr.task.Error = err.Error()
		tr.task.SpeedBps = 0
		tr.task.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		tr.mu.Unlock()
		e.persist()
		e.emitTask(tr)
		closeDone(tr)
	}
	if err := ctx.Err(); err != nil {
		tr.mu.Lock()
		tr.task.Status = StatusPaused
		tr.mu.Unlock()
		closeDone(tr)
		return
	}

	// Wait for a download slot.
	select {
	case e.sem <- struct{}{}:
		defer func() { <-e.sem }()
	case <-ctx.Done():
		tr.mu.Lock()
		tr.task.Status = StatusPaused
		tr.mu.Unlock()
		closeDone(tr)
		return
	}

	pr, err := e.probeWithRetry(ctx, tr)
	if err != nil {
		fail(err)
		return
	}

	tr.mu.Lock()
	tr.task.ProxyUsed = pr.ProxyDesc
	tr.task.FinalURL = pr.FinalURL
	if pr.Filename != "" && t.URL != "" {
		// Only adopt the server-provided name when the user did not pin one.
		cur := tr.task.Filename
		if cur == "" || cur == "download.bin" || cur == guessFilename(t.URL) {
			tr.task.Filename = sanitizeFilename(pr.Filename)
		}
	}
	tr.task.ETag = pr.ETag
	tr.task.RangeOK = pr.RangeOK
	if pr.Total > 0 {
		tr.task.Total = pr.Total
	}
	dir := tr.task.Directory
	name := tr.task.Filename
	st := tr.state
	resume := st.URL == t.URL && st.Total == pr.Total && len(st.Chunks) > 0 && (st.ETag == "" || pr.ETag == "" || st.ETag == pr.ETag)
	if !resume {
		st = state{
			URL:      t.URL,
			FinalURL: pr.FinalURL,
			Filename: name,
			Total:    pr.Total,
			ETag:     pr.ETag,
			RangeOK:  pr.RangeOK,
			Headers:  t.Headers,
			Referer:  t.Referer,
		}
	}
	cfg := e.store.Get()
	conns := tr.task.Connections
	if !pr.RangeOK || pr.Total <= 0 {
		conns = 1
	}
	if conns < 1 {
		conns = 1
	}
	if !resume {
		st.Chunks = planChunks(pr.Total, conns, cfg.Downloads.ChunkSize)
	}
	st.Filename = name
	st.Total = pr.Total
	tr.state = st
	tr.task.Status = StatusDownloading
	tr.task.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	tr.doneBytes = 0
	for _, c := range st.Chunks {
		tr.doneBytes += c.Done
	}
	tr.task.Downloaded = tr.doneBytes
	tr.mu.Unlock()

	e.writeState(tr.task.ID, st)
	e.emitTask(tr)

	// Progress ticker.
	stopTicker := make(chan struct{})
	var tickerWG sync.WaitGroup
	tickerWG.Add(1)
	go func() {
		defer tickerWG.Done()
		tk := time.NewTicker(400 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-stopTicker:
				return
			case <-tk.C:
				done := atomic.LoadInt64(&tr.doneBytes)
				speed := tr.speed.sample(done)
				tr.mu.Lock()
				tr.task.Downloaded = done
				tr.task.SpeedBps = speed
				tr.task.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
				if tr.task.Total > 0 {
					tr.task.Percent = float64(done) / float64(tr.task.Total) * 100
				}
				tr.mu.Unlock()
				e.emitTask(tr)
			}
		}
	}()

	// Chunk workers.
	var wg sync.WaitGroup
	errCh := make(chan error, len(st.Chunks)+1)
	active := st.Chunks
	for i := range active {
		c := active[i]
		if c.Done >= c.End-c.Start+1 {
			continue
		}
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if err := e.fetchChunk(ctx, tr, idx); err != nil {
				errCh <- err
			}
		}(i)
	}
	wg.Wait()
	close(stopTicker)
	tickerWG.Wait()

	if err := ctx.Err(); err != nil {
		tr.mu.Lock()
		tr.task.Status = StatusPaused
		tr.task.SpeedBps = 0
		tr.mu.Unlock()
		e.writeState(tr.task.ID, tr.state)
		e.persist()
		e.emitTask(tr)
		closeDone(tr)
		return
	}
	select {
	case err := <-errCh:
		if err != nil {
			fail(err)
			return
		}
	default:
	}

	// Assemble.
	tr.mu.Lock()
	curState := tr.state
	tr.mu.Unlock()
	if err := e.assemble(tr.task.ID, dir, name, curState, pr.RangeOK && pr.Total > 0); err != nil {
		fail(err)
		return
	}
	_ = os.Remove(e.partPath(tr.task.ID))

	total := curState.Total
	if total <= 0 {
		total = atomic.LoadInt64(&tr.doneBytes)
	}
	tr.mu.Lock()
	tr.task.Status = StatusCompleted
	tr.task.Downloaded = total
	tr.task.Total = total
	tr.task.Percent = 100
	tr.task.SpeedBps = 0
	tr.task.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	tr.task.UpdatedAt = tr.task.CompletedAt
	doneTask := tr.task
	tr.mu.Unlock()
	e.persist()
	e.emitTask(tr)
	e.emit(Event{Kind: "done", Task: &doneTask})
	closeDone(tr)
}

func closeDone(tr *taskRunner) {
	tr.mu.Lock()
	ch := tr.done
	tr.mu.Unlock()
	if ch != nil {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
}

func planChunks(total int64, conns int, chunkSize int64) []chunk {
	if total <= 0 {
		return []chunk{{Index: 0, Start: 0, End: -1, Done: 0}}
	}
	if chunkSize <= 0 {
		chunkSize = 8 << 20
	}
	maxChunks := int((total + chunkSize - 1) / chunkSize)
	if maxChunks < 1 {
		maxChunks = 1
	}
	n := conns
	if n > maxChunks {
		n = maxChunks
	}
	if n < 1 {
		n = 1
	}
	size := total / int64(n)
	if size > chunkSize*4 {
		size = chunkSize * 4
	}
	out := make([]chunk, 0, n+1)
	var start int64
	for i := 0; i < n; i++ {
		end := start + size - 1
		if i == n-1 {
			end = total - 1
		}
		if end >= total {
			end = total - 1
		}
		out = append(out, chunk{Index: i, Start: start, End: end})
		start = end + 1
		if start >= total {
			break
		}
	}
	return out
}

func chunkFilePath(dir, name string, idx int) string {
	return filepath.Join(dir, fmt.Sprintf("%s.part.%d", name, idx))
}

func (e *Engine) fetchChunk(ctx context.Context, tr *taskRunner, idx int) error {
	cfg := e.store.Get()
	retries := cfg.Downloads.RetryCount
	if retries <= 0 {
		retries = 3
	}
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil
		}
		if err := e.fetchChunkOnce(ctx, tr, idx); err != nil {
			lastErr = err
			tr.mu.Lock()
			tr.task.Retries++
			tr.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Duration(attempt+1) * 900 * time.Millisecond):
			}
			continue
		}
		return nil
	}
	return fmt.Errorf("chunk %d failed after retries: %w", idx, lastErr)
}

func (e *Engine) fetchChunkOnce(ctx context.Context, tr *taskRunner, idx int) error {
	tr.mu.Lock()
	st := tr.state
	task := tr.task
	tr.mu.Unlock()
	if idx >= len(st.Chunks) {
		return nil
	}
	c := st.Chunks[idx]
	total := c.End - c.Start + 1
	if total <= 0 {
		return nil
	}
	if c.Done >= total {
		return nil
	}
	path := st.FinalURL
	if path == "" {
		path = task.URL
	}
	ci, err := e.clientFor(task)
	if err != nil {
		return err
	}
	req, err := buildRequest(ctx, http.MethodGet, path, task)
	if err != nil {
		return err
	}
	from := c.Start + c.Done
	if st.RangeOK && st.Total > 0 {
		to := c.End
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", from, to))
		req.Header.Set("If-Range", firstNonEmpty(st.ETag, ""))
		if st.ETag == "" {
			req.Header.Del("If-Range")
		}
	}
	resp, err := ci.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if st.RangeOK && st.Total > 0 && resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %s", resp.Status)
	}

	partPath := chunkFilePath(task.Directory, st.Filename, idx)
	f, err := os.OpenFile(partPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() != c.Done {
		// Partial file out of sync with state: rewrite from scratch.
		if err := f.Truncate(c.Done); err == nil {
			if _, err := f.Seek(c.Done, io.SeekStart); err != nil {
				return err
			}
		}
	}

	buf := make([]byte, 256*1024)
	written := c.Done
	dlCfg := e.store.Get().Downloads
	limiter := newRateLimiter(dlCfg.SpeedLimitKBps)
	for written < total {
		select {
		case <-ctx.Done():
			e.saveChunkProgress(tr, idx, written)
			return nil
		default:
		}
		remain := total - written
		chunk := int64(len(buf))
		if chunk > remain {
			chunk = remain
		}
		n, rerr := resp.Body.Read(buf[:chunk])
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				e.saveChunkProgress(tr, idx, written)
				return werr
			}
			written += int64(n)
			atomic.AddInt64(&tr.doneBytes, int64(n))
			limiter.wait(int64(n))
			e.saveChunkProgress(tr, idx, written)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	if written < total {
		return fmt.Errorf("short read: got %d of %d bytes", written, total)
	}
	return nil
}

func (e *Engine) saveChunkProgress(tr *taskRunner, idx int, written int64) {
	tr.mu.Lock()
	if idx < len(tr.state.Chunks) {
		tr.state.Chunks[idx].Done = written
	}
	st := tr.state
	tr.mu.Unlock()
	e.writeState(tr.task.ID, st)
}

func (e *Engine) assemble(id, dir, name string, st state, rangeOK bool) error {
	if !rangeOK || st.Total <= 0 || len(st.Chunks) <= 1 {
		// Single stream: rename the part file into place.
		part := chunkFilePath(dir, name, 0)
		if _, err := os.Stat(part); err != nil {
			// Fall back to the plain .part name.
			alt := filepath.Join(dir, name+".part")
			if _, err2 := os.Stat(alt); err2 == nil {
				part = alt
			}
		}
		target := e.uniqueTarget(dir, name)
		if filepath.Base(target) != name {
			// keep the requested name when the file does not exist yet
			if _, err := os.Stat(filepath.Join(dir, name)); errors.Is(err, os.ErrNotExist) {
				target = filepath.Join(dir, name)
			}
		}
		return os.Rename(part, target)
	}
	target := filepath.Join(dir, name)
	tmp := target + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	for i := range st.Chunks {
		part := chunkFilePath(dir, name, i)
		in, err := os.Open(part)
		if err != nil {
			_ = out.Close()
			return fmt.Errorf("missing chunk %d: %w", i, err)
		}
		if _, err := io.Copy(out, in); err != nil {
			_ = in.Close()
			_ = out.Close()
			return err
		}
		_ = in.Close()
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	_ = out.Close()
	final := e.uniqueTarget(dir, name)
	if err := os.Rename(tmp, final); err != nil {
		return err
	}
	for i := range st.Chunks {
		_ = os.Remove(chunkFilePath(dir, name, i))
	}
	if filepath.Base(final) != name {
		// Reflect the chosen unique name back into the task so the UI shows it.
		e.mu.RLock()
		tr, ok := e.tasks[id]
		e.mu.RUnlock()
		if ok {
			tr.mu.Lock()
			tr.task.Filename = filepath.Base(final)
			tr.mu.Unlock()
		}
	}
	return nil
}

// Probe inspects a URL and reports downloadable metadata for the UI.
func (e *Engine) Probe(ctx context.Context, rawURL string) map[string]any {
	t := Task{URL: strings.TrimSpace(rawURL)}
	if t.URL == "" {
		return map[string]any{"ok": false, "error": "url is required"}
	}
	if !strings.HasPrefix(t.URL, "http://") && !strings.HasPrefix(t.URL, "https://") {
		return map[string]any{"ok": false, "error": "only http(s) urls are supported"}
	}
	pr, err := e.probe(ctx, t)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	name := pr.Filename
	if name == "" {
		name = guessFilename(pr.FinalURL)
	}
	return map[string]any{
		"ok":        true,
		"filename":  name,
		"total":     pr.Total,
		"rangeOk":   pr.RangeOK,
		"finalUrl":  pr.FinalURL,
		"etag":      pr.ETag,
		"proxyUsed": pr.ProxyDesc,
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// rateLimiter spreads writes to respect a kB/s ceiling (0 = unlimited).
type rateLimiter struct {
	perSec int64
	start  time.Time
	total  int64
}

func newRateLimiter(kbps int) *rateLimiter {
	if kbps <= 0 {
		return nil
	}
	return &rateLimiter{perSec: int64(kbps) * 1024, start: time.Now()}
}

func (r *rateLimiter) wait(n int64) {
	if r == nil || r.perSec <= 0 {
		return
	}
	r.total += n
	expected := time.Duration(float64(r.total) / float64(r.perSec) * float64(time.Second))
	elapsed := time.Since(r.start)
	if expected > elapsed {
		time.Sleep(expected - elapsed)
	}
}
