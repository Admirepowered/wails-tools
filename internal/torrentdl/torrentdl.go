// Package torrentdl downloads BitTorrent magnet links into a directory.
package torrentdl

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/storage"
)

// Options configures one magnet download.
type Options struct {
	// Dir receives the torrent files.
	Dir string
	// ListenPort is the inbound TCP/UDP port (0 = random).
	ListenPort int
	// Progress is called while downloading.
	Progress func(completed, total int64)
	// OnMetadata is called once the torrent name and size are known; returning
	// an error aborts the download (e.g. quota exceeded).
	OnMetadata func(name string, total int64) error
	// Logf receives lifecycle messages.
	Logf func(format string, args ...any)
}

// Result describes a finished download.
type Result struct {
	Name  string // torrent name (file or directory inside Dir)
	Path  string // absolute path inside Dir
	Bytes int64
}

func (o *Options) logf(format string, args ...any) {
	if o.Logf != nil {
		o.Logf(format, args...)
	}
}

// Download fetches every file of a magnet link and waits for completion.
// It returns when the torrent is complete or ctx is cancelled.
func Download(ctx context.Context, magnet string, opts Options) (Result, error) {
	if opts.Dir == "" {
		return Result{}, errors.New("未指定保存目录")
	}
	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = opts.Dir
	cfg.Seed = false
	cfg.NoUpload = true
	cfg.ListenPort = opts.ListenPort
	cfg.DisableIPv6 = false
	// Keep torrent bookkeeping (.torrent.bolt.db, piece completion) out of the
	// download directory: for offline downloads that directory is the user's
	// file tree and must only contain payload files.
	cfg.DefaultStorage = storage.NewMMapWithCompletion(opts.Dir, storage.NewMapPieceCompletion())
	client, err := torrent.NewClient(cfg)
	if err != nil {
		return Result{}, fmt.Errorf("启动 BT 客户端失败: %w", err)
	}
	defer client.Close()

	t, err := client.AddMagnet(magnet)
	if err != nil {
		return Result{}, fmt.Errorf("解析磁力链失败: %w", err)
	}
	opts.logf("torrent: waiting for metadata %s", magnet)

	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	case <-t.GotInfo():
	}

	if opts.OnMetadata != nil {
		if err := opts.OnMetadata(t.Name(), t.Length()); err != nil {
			return Result{}, err
		}
	}
	t.DownloadAll()
	opts.logf("torrent: %q files=%d total=%d", t.Name(), len(t.Files()), t.Length())

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if t.Complete().Bool() {
			res := Result{Name: t.Name(), Path: opts.Dir + "/" + t.Name(), Bytes: t.Length()}
			opts.logf("torrent: complete %s (%d bytes)", res.Name, res.Bytes)
			return res, nil
		}
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-ticker.C:
			if opts.Progress != nil {
				opts.Progress(t.BytesCompleted(), t.Length())
			}
		}
	}
}
