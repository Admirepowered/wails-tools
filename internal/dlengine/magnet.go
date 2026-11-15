package dlengine

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"wailstools/internal/torrentdl"
)

// KindHTTP and KindMagnet classify download tasks.
const (
	KindHTTP   = "http"
	KindMagnet = "magnet"
)

// IsMagnet reports whether raw is a BitTorrent magnet link.
func IsMagnet(raw string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw)), "magnet:")
}

// magnetName extracts the suggested name (dn) from a magnet link.
func magnetName(raw string) string {
	if i := strings.Index(raw, "?"); i >= 0 {
		if q, err := url.ParseQuery(raw[i+1:]); err == nil {
			if dn := strings.TrimSpace(q.Get("dn")); dn != "" {
				return sanitizeFilename(dn)
			}
		}
	}
	if i := strings.Index(raw, "btih:"); i >= 0 {
		hash := raw[i+5:]
		if j := strings.IndexAny(hash, "&?"); j >= 0 {
			hash = hash[:j]
		}
		if len(hash) > 12 {
			hash = hash[:12]
		}
		return "magnet-" + strings.ToLower(hash)
	}
	return "magnet-download"
}

// runMagnet drives one magnet task through the torrent engine.
func (e *Engine) runMagnet(ctx context.Context, tr *taskRunner) {
	tr.mu.Lock()
	dir := tr.task.Directory
	if dir == "" {
		dir = e.baseDir
	}
	magnet := tr.task.URL
	tr.task.Status = StatusDownloading
	tr.task.ProxyUsed = "bt"
	tr.task.RangeOK = false
	tr.task.SpeedBps = 0
	tr.task.Error = ""
	tr.task.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	tr.mu.Unlock()
	e.emitTask(tr)

	// A magnet download holds one concurrency slot like an HTTP download.
	select {
	case e.sem <- struct{}{}:
		defer func() { <-e.sem }()
	case <-ctx.Done():
		e.markMagnetStatus(tr, StatusPaused, "")
		return
	}

	progress := func(done, total int64) {
		tr.mu.Lock()
		tr.task.Downloaded = done
		if total > 0 {
			tr.task.Total = total
		}
		tr.task.SpeedBps = tr.speed.sample(done)
		tr.task.Percent = percentOf(done, tr.task.Total)
		tr.task.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		tr.mu.Unlock()
		e.emitTask(tr)
	}

	res, err := torrentdl.Download(ctx, magnet, torrentdl.Options{
		Dir:      dir,
		Progress: progress,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			e.markMagnetStatus(tr, StatusPaused, "")
			return
		}
		e.markMagnetStatus(tr, StatusFailed, err.Error())
		return
	}

	tr.mu.Lock()
	tr.task.Filename = res.Name
	tr.task.Total = res.Bytes
	tr.task.Downloaded = res.Bytes
	tr.task.Percent = 100
	tr.task.SpeedBps = 0
	tr.task.Status = StatusCompleted
	tr.task.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	tr.task.UpdatedAt = tr.task.CompletedAt
	tr.mu.Unlock()
	e.persist()
	e.emitTask(tr)
	closeDone(tr)
}

func (e *Engine) markMagnetStatus(tr *taskRunner, status, msg string) {
	tr.mu.Lock()
	tr.task.Status = status
	tr.task.SpeedBps = 0
	if msg != "" {
		tr.task.Error = msg
	}
	tr.task.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	tr.mu.Unlock()
	e.persist()
	e.emitTask(tr)
	closeDone(tr)
}

// percentOf is a small helper shared with the HTTP path.
func percentOf(done, total int64) float64 {
	if total <= 0 {
		return 0
	}
	p := float64(done) / float64(total) * 100
	if p > 100 {
		p = 100
	}
	return p
}
