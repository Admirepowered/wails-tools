package netdisk

import (
	"context"
	"net/http"
	"strings"
	"sync"
)

type quarkResponseCookieCaptureKey struct{}

type quarkResponseCookieCapture func([]*http.Cookie)

type quarkResponseCookieState struct {
	mu      sync.RWMutex
	fields  map[string]string
	capture quarkResponseCookieCapture
}

func withQuarkResponseCookieCapture(ctx context.Context, capture quarkResponseCookieCapture) context.Context {
	if capture == nil {
		return ctx
	}
	return context.WithValue(ctx, quarkResponseCookieCaptureKey{}, &quarkResponseCookieState{
		fields:  make(map[string]string),
		capture: capture,
	})
}

func captureQuarkResponseCookies(ctx context.Context, resp *http.Response) {
	if resp == nil {
		return
	}
	state, ok := ctx.Value(quarkResponseCookieCaptureKey{}).(*quarkResponseCookieState)
	if !ok || state == nil {
		return
	}
	cookies := resp.Cookies()
	if len(cookies) == 0 {
		return
	}
	for _, cookie := range cookies {
		if cookie != nil && cookie.Name == "__puus" && strings.TrimSpace(cookie.Value) != "" {
			state.mu.Lock()
			state.fields[cookie.Name] = strings.TrimSpace(cookie.Value)
			state.mu.Unlock()
		}
	}
	if state.capture != nil {
		state.capture(cookies)
	}
}

func quarkResponseCookieValue(cookies []*http.Cookie, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	for _, cookie := range cookies {
		if cookie != nil && cookie.Name == name && strings.TrimSpace(cookie.Value) != "" {
			return strings.TrimSpace(cookie.Value)
		}
	}
	return ""
}

func quarkCookieWithCapturedResponseFields(ctx context.Context, cookie string) string {
	state, ok := ctx.Value(quarkResponseCookieCaptureKey{}).(*quarkResponseCookieState)
	if !ok || state == nil {
		return cookie
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	for name, value := range state.fields {
		if name == "__puus" && strings.TrimSpace(value) != "" {
			cookie, _ = mergeCookieField(cookie, name, value)
		}
	}
	return cookie
}
