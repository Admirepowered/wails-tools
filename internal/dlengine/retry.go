package dlengine

import (
	"context"
	"time"
)

// statusError marks a non-success HTTP response from the origin server.
type statusError struct {
	code   int
	status string
}

func (e *statusError) Error() string { return "server returned " + e.status }

// retryable reports whether the response is worth another attempt.
//
// 403 is retried as well: CDN links (Baidu pan, signed URLs) reject a plain GET
// without a Range header, and hot-linking protection kicks in from time to time;
// both recover when the request is repeated with a range/a fresh connection.
func (e *statusError) retryable() bool {
	switch {
	case e.code >= 500:
		return true
	case e.code == 403, e.code == 408, e.code == 425, e.code == 429:
		return true
	default:
		return false
	}
}

// probeWithRetry probes the URL, retrying transient origin failures.
func (e *Engine) probeWithRetry(ctx context.Context, tr *taskRunner) (*probeResult, error) {
	t := tr.task
	t.URL = tr.task.URL
	retries := e.store.Get().Downloads.RetryCount
	if retries <= 0 {
		retries = 3
	}
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, err
		}
		pr, err := e.probe(ctx, t)
		if err == nil {
			return pr, nil
		}
		lastErr = err
		tr.mu.Lock()
		tr.task.Retries++
		tr.task.Error = err.Error()
		tr.mu.Unlock()
		if se, ok := err.(*statusError); ok && !se.retryable() {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, lastErr
		case <-time.After(time.Duration(attempt+1) * 700 * time.Millisecond):
		}
	}
	return nil, lastErr
}
