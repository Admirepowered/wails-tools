package app

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Remote media streaming: the WebView cannot send Cookie/Referer/UA to a
// netdisk CDN, so a local endpoint proxies the request with the right headers and
// forwards Range requests. HLS playlists are rewritten so their segments also go
// through the proxy.

type mediaStream struct {
	url         string
	filename    string
	contentType string
	headers     map[string]string
	expiresAt   time.Time
}

func (a *App) streamAdd(s mediaStream) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b[:])
	s.expiresAt = time.Now().Add(2 * time.Hour)
	a.media.mu.Lock()
	if a.media.streams == nil {
		a.media.streams = map[string]mediaStream{}
	}
	a.media.streams[token] = s
	a.media.mu.Unlock()
	name := s.filename
	if name == "" {
		name = "media"
	}
	return "/stream/" + token + "/" + url.PathEscape(name), nil
}

func (a *App) streamGet(token string) (mediaStream, bool) {
	a.media.mu.Lock()
	defer a.media.mu.Unlock()
	s, ok := a.media.streams[token]
	if !ok || time.Now().After(s.expiresAt) {
		return mediaStream{}, false
	}
	return s, true
}

// RegisterMediaStream exposes a remote URL (with headers) to the WebView.
func (a *App) RegisterMediaStream(rawURL, filename, contentType string, headers map[string]string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", errors.New("url is required")
	}
	return a.streamAdd(mediaStream{url: rawURL, filename: filename, contentType: contentType, headers: headers})
}

// handleStream proxies one media request (and HLS segments) to the origin.
func (a *App) handleStream(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/stream/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	token := parts[0]
	target, ok := a.streamGet(token)
	if !ok {
		http.NotFound(w, r)
		return
	}
	rawURL := target.url
	if len(parts) == 2 && strings.HasPrefix(parts[1], "hls") {
		if u := r.URL.Query().Get("u"); u != "" {
			rawURL = u
		}
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD, OPTIONS")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, rawURL, nil)
	if err != nil {
		http.Error(w, "invalid target", http.StatusBadGateway)
		return
	}
	for k, v := range target.headers {
		if strings.TrimSpace(v) != "" {
			req.Header.Set(k, v)
		}
	}
	for _, h := range []string{"Range", "If-Range", "If-None-Match", "If-Modified-Since"} {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	client := &http.Client{Timeout: 0}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "upstream failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// HLS: rewrite playlist lines so segments hit the proxy again.
	if isM3U8(rawURL, resp.Header.Get("Content-Type")) && r.Method == http.MethodGet {
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if err != nil {
			http.Error(w, "playlist read failed", http.StatusBadGateway)
			return
		}
		base, _ := url.Parse(rawURL)
		var out strings.Builder
		for _, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") && !strings.Contains(trimmed, "URI=") {
				out.WriteString(line)
				out.WriteString("\n")
				continue
			}
			if strings.HasPrefix(trimmed, "#") {
				out.WriteString(rewriteM3U8URIAttrs(line, base, a.hlsProxyURL(token)))
			} else if abs := resolveRef(base, trimmed); abs != "" {
				out.WriteString(a.hlsProxyURL(token) + "?u=" + url.QueryEscape(abs))
			} else {
				out.WriteString(line)
			}
			out.WriteString("\n")
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Del("Content-Length")
		w.WriteHeader(resp.StatusCode)
		_, _ = io.WriteString(w, out.String())
		return
	}

	for _, h := range []string{"Content-Type", "Content-Length", "Accept-Ranges", "Content-Range", "ETag", "Last-Modified"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	if w.Header().Get("Content-Type") == "" && target.contentType != "" {
		w.Header().Set("Content-Type", target.contentType)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, resp.Body)
}

func (a *App) hlsProxyURL(token string) string {
	return "/stream/" + token + "/hls"
}

func isM3U8(rawURL, contentType string) bool {
	if strings.Contains(strings.ToLower(contentType), "mpegurl") {
		return true
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return strings.HasSuffix(strings.ToLower(u.Path), ".m3u8")
}

func resolveRef(base *url.URL, ref string) string {
	if base == nil {
		return ""
	}
	u, err := base.Parse(ref)
	if err != nil {
		return ""
	}
	return u.String()
}

// rewriteM3U8URIAttrs rewrites URI="..." attributes inside playlist tags.
func rewriteM3U8URIAttrs(line string, base *url.URL, proxyPath string) string {
	const marker = `URI="`
	idx := strings.Index(line, marker)
	if idx < 0 {
		return line
	}
	start := idx + len(marker)
	end := strings.Index(line[start:], `"`)
	if end < 0 {
		return line
	}
	ref := line[start : start+end]
	abs := resolveRef(base, ref)
	if abs == "" {
		return line
	}
	return line[:start] + proxyPath + "?u=" + url.QueryEscape(abs) + line[start+end:]
}
