package app

import (
	"context"
	"path"
	"strings"
	"time"

	"wailstools/internal/netdisk"
)

// NetdiskEnqueueFolder downloads every file under a netdisk folder.
func (a *App) NetdiskEnqueueFolder(accountID, folder string) (map[string]any, error) {
	mgr, err := a.ndManager()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	queued := 0
	var walk func(dir string, depth int) error
	walk = func(dir string, depth int) error {
		if depth > 8 {
			return nil
		}
		listing, err := mgr.ListFiles(ctx, netdisk.ListFilesRequest{AccountID: accountID, Path: dir})
		if err != nil {
			return err
		}
		for _, f := range listing.Files {
			if f.IsDir {
				sub := f.Path
				if sub == "" {
					sub = path.Join(dir, f.Name)
				}
				if err := walk(sub, depth+1); err != nil {
					a.emit("netdisk:error", err.Error())
				}
				continue
			}
			name := f.Name
			if name == "" {
				name = path.Base(f.Path)
			}
			if _, err := a.NetdiskEnqueueDownload(netdisk.ResolveDownloadRequest{
				AccountID: accountID,
				FileID:    f.ID,
				Path:      f.Path,
				Name:      name,
				Size:      f.Size,
			}); err != nil {
				a.emit("netdisk:error", "跳过 "+name+": "+err.Error())
				continue
			}
			queued++
		}
		return nil
	}
	if err := walk(strings.TrimSpace(folder), 0); err != nil {
		return nil, err
	}
	return map[string]any{"queued": queued, "folder": folder}, nil
}
