package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"wailstools/internal/netdisk"
)

// NetdiskPushOffline resolves a netdisk file link and hands it to the sync
// server as an offline download (URL + headers, so no cookie ever leaves the
// client in plain form beyond the signed request).
func (a *App) NetdiskPushOffline(req netdisk.ResolveDownloadRequest) (map[string]any, error) {
	link, err := a.NetdiskResolveLink(req)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(link.URL) == "" {
		return nil, errors.New("网盘没有返回可用的下载地址")
	}
	headers := map[string]string{}
	for k, v := range link.Headers {
		headers[k] = v
	}
	if link.Cookie != "" {
		headers["Cookie"] = link.Cookie
	}
	if link.UserAgent != "" {
		headers["User-Agent"] = link.UserAgent
	}
	if link.Referer != "" {
		headers["Referer"] = link.Referer
	}
	name := link.Filename
	if name == "" {
		name = req.Name
	}
	client, err := a.requireLink()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return client.OfflineAddURL(ctx, link.URL, name, headers)
}
