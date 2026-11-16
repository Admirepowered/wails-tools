package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"wailstools/internal/netdisk"
)

// NetdiskPlayURL resolves a netdisk video and registers a local stream URL the
// WebView can play (the browser cannot send the pan Cookie/UA itself).
func (a *App) NetdiskPlayURL(req netdisk.VideoPreviewRequest) (map[string]any, error) {
	m, err := a.ndManager()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	res, err := m.ResolveVideoPreview(ctx, req)
	if err != nil {
		return nil, err
	}
	if len(res.Qualities) == 0 {
		return nil, errors.New("网盘没有返回可播放地址")
	}
	quality := res.Qualities[0]
	for _, q := range res.Qualities {
		if q.Key != "" && q.Key == res.DefaultQuality {
			quality = q
			break
		}
	}
	headers := map[string]string{}
	for k, v := range quality.Headers {
		headers[k] = v
	}
	if quality.Cookie != "" {
		headers["Cookie"] = quality.Cookie
	}
	if quality.UserAgent != "" {
		headers["User-Agent"] = quality.UserAgent
	}
	if quality.Referer != "" {
		headers["Referer"] = quality.Referer
	}
	name := quality.Filename
	if name == "" {
		name = res.Filename
	}
	if name == "" {
		name = req.Name
	}
	local, err := a.RegisterMediaStream(quality.URL, name, quality.ContentType, headers)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"url":         local,
		"filename":    name,
		"contentType": quality.ContentType,
		"size":        quality.Size,
		"format":      quality.Format,
		"qualities":   res.Qualities,
	}, nil
}

// NetdiskDeleteFiles removes netdisk entries; Baidu needs the paths as well as
// the ids, which the UI does not always carry.
func (a *App) NetdiskDeleteFiles(req netdisk.DeleteFilesRequest) error {
	if len(req.Paths) == 0 && len(req.FileIDs) == 0 {
		return errors.New("paths are required")
	}
	m, err := a.ndManager()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := m.DeleteFiles(ctx, req); err != nil && strings.Contains(strings.ToLower(err.Error()), "paths are required") {
		return errors.New("百度网盘删除需要文件路径：请在列表中右键该文件重试")
	} else {
		return err
	}
}
