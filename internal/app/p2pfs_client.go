package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"wailstools/internal/p2p"
	"wailstools/internal/wire"
)

// Client-side file management over a punched channel.

// P2PFilesEntry is one entry of the peer's shared directory.
type P2PFilesEntry struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Dir      bool   `json:"dir"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
}

// P2PFilesListing is the peer's shared directory listing.
type P2PFilesListing struct {
	Path    string          `json:"path"`
	Root    string          `json:"root"`
	Entries []P2PFilesEntry `json:"entries"`
}

func (a *App) p2pChannel(deviceID string) (*p2p.Channel, error) {
	ch, ok := a.p2p.get(strings.TrimSpace(deviceID))
	if !ok {
		return nil, errors.New("请先与该设备打洞连接")
	}
	return ch, nil
}

// p2pPeerCall issues one RPC on a fresh stream of the punched channel.
func p2pPeerCall(ctx context.Context, ch *p2p.Channel, method string, body any, out any) error {
	return ch.Call(ctx, method, body, out)
}

// P2PFilesList lists the peer's shared directory.
func (a *App) P2PFilesList(deviceID, path string) (P2PFilesListing, error) {
	var out P2PFilesListing
	ch, err := a.p2pChannel(deviceID)
	if err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := p2pPeerCall(ctx, ch, "p2p.fs.list", map[string]any{"path": path}, &out); err != nil {
		return out, err
	}
	if out.Entries == nil {
		out.Entries = []P2PFilesEntry{}
	}
	return out, nil
}

// P2PFilesMkdir creates a directory on the peer.
func (a *App) P2PFilesMkdir(deviceID, path string) error {
	ch, err := a.p2pChannel(deviceID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var out map[string]any
	return p2pPeerCall(ctx, ch, "p2p.fs.mkdir", map[string]any{"path": path}, &out)
}

// P2PFilesRemove deletes a file or directory on the peer.
func (a *App) P2PFilesRemove(deviceID, path string) error {
	ch, err := a.p2pChannel(deviceID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var out map[string]any
	return p2pPeerCall(ctx, ch, "p2p.fs.remove", map[string]any{"path": path}, &out)
}

// P2PFilesDownload copies a file from the peer to a local path.
func (a *App) P2PFilesDownload(deviceID, remotePath, localPath string) (map[string]any, error) {
	ch, err := a.p2pChannel(deviceID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(localPath) == "" {
		return nil, errors.New("请选择保存路径")
	}
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	stream, err := ch.OpenStream(ctx, "p2p.fs.read", map[string]any{"path": remotePath})
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	f, err := os.Create(localPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	n, err := io.Copy(f, stream)
	if err != nil {
		return nil, err
	}
	return map[string]any{"bytes": n, "path": localPath}, nil
}

// P2PFilesUpload copies a local file to the peer's shared directory.
func (a *App) P2PFilesUpload(deviceID, localPath, remotePath string) (map[string]any, error) {
	ch, err := a.p2pChannel(deviceID)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(localPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if strings.TrimSpace(remotePath) == "" {
		remotePath = filepath.Base(localPath)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	stream, err := ch.OpenStream(ctx, "p2p.fs.write", map[string]any{"path": remotePath})
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	n, err := io.Copy(stream, f)
	if err != nil {
		return nil, fmt.Errorf("上传中断: %w", err)
	}
	return map[string]any{"bytes": n, "path": remotePath}, nil
}

var _ = net.IPv4len
var _ = wire.Version
