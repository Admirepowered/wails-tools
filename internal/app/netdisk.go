package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"wailstools/internal/dlengine"
	"wailstools/internal/netdisk"
	"wailstools/internal/proxyx"
	"wailstools/internal/settings"
)

// ndManager returns the netdisk manager with a proxy-aware HTTP client that
// follows the current "netdisk" proxy scope.
func (a *App) ndManager() (*netdisk.Manager, error) {
	if a.netdisk == nil {
		return nil, errors.New("网盘模块未初始化")
	}
	rule := a.settings.ProxyFor(settings.ScopeNetdisk)
	sig := strings.Join([]string{rule.Mode, rule.URL, rule.Username, rule.Password}, "|")
	a.ndMu.Lock()
	defer a.ndMu.Unlock()
	if sig != a.ndSig {
		client, err := proxyx.HTTPClient(rule, 0)
		if err != nil {
			return nil, fmt.Errorf("网盘代理配置无效: %w", err)
		}
		a.netdisk.SetHTTPClient(client)
		a.ndSig = sig
	}
	return a.netdisk, nil
}

// NetdiskProviderInfo describes a supported cloud drive.
type NetdiskProviderInfo struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Capabilities []string `json:"capabilities"`
}

// NetdiskProviders lists the supported cloud-drive providers.
func (a *App) NetdiskProviders() []NetdiskProviderInfo {
	return []NetdiskProviderInfo{
		{ID: string(netdisk.ProviderQuark), Name: "夸克网盘", Capabilities: []string{"list", "search", "download", "upload", "share", "recycle", "video_preview"}},
		{ID: string(netdisk.ProviderUC), Name: "UC 网盘", Capabilities: []string{"list", "search", "download", "upload", "share", "recycle", "video_preview"}},
		{ID: string(netdisk.ProviderBaidu), Name: "百度网盘", Capabilities: []string{"list", "search", "download", "upload", "share", "recycle"}},
	}
}

// ListNetdiskAccounts returns saved cloud-drive accounts.
func (a *App) ListNetdiskAccounts() ([]netdisk.Account, error) {
	m, err := a.ndManager()
	if err != nil {
		return nil, err
	}
	return m.ListAccounts(), nil
}

// AddNetdiskAccount stores an account from its browser cookie.
func (a *App) AddNetdiskAccount(provider string, cookie string, displayName string) (netdisk.Account, error) {
	m, err := a.ndManager()
	if err != nil {
		return netdisk.Account{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return m.AddAccount(ctx, netdisk.AddAccountRequest{
		Provider:    netdisk.Provider(provider),
		Cookie:      cookie,
		DisplayName: displayName,
	})
}

// UpdateNetdiskCookie replaces an account cookie.
func (a *App) UpdateNetdiskCookie(accountID, cookie string) (netdisk.Account, error) {
	m, err := a.ndManager()
	if err != nil {
		return netdisk.Account{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return m.UpdateAccountCookie(ctx, accountID, netdisk.AddAccountRequest{Cookie: cookie})
}

// UpdateNetdiskRemark renames the account label.
func (a *App) UpdateNetdiskRemark(accountID, remark string) (netdisk.Account, error) {
	m, err := a.ndManager()
	if err != nil {
		return netdisk.Account{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return m.UpdateAccountRemark(ctx, accountID, remark)
}

// RefreshNetdiskAccount re-probes an account.
func (a *App) RefreshNetdiskAccount(accountID string) (netdisk.Account, error) {
	m, err := a.ndManager()
	if err != nil {
		return netdisk.Account{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return m.RefreshAccount(ctx, accountID)
}

// RemoveNetdiskAccount deletes an account.
func (a *App) RemoveNetdiskAccount(accountID string) error {
	m, err := a.ndManager()
	if err != nil {
		return err
	}
	return m.RemoveAccount(accountID)
}

// NetdiskListFiles lists a remote cloud directory.
func (a *App) NetdiskListFiles(req netdisk.ListFilesRequest) (netdisk.FileList, error) {
	m, err := a.ndManager()
	if err != nil {
		return netdisk.FileList{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	return m.ListFiles(ctx, req)
}

// NetdiskSearch searches files by name.
func (a *App) NetdiskSearch(req netdisk.SearchFilesRequest) (netdisk.FileList, error) {
	m, err := a.ndManager()
	if err != nil {
		return netdisk.FileList{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	return m.SearchFiles(ctx, req)
}

// NetdiskMkdir creates a folder.
func (a *App) NetdiskMkdir(req netdisk.CreateFolderRequest) error {
	m, err := a.ndManager()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return m.CreateFolder(ctx, req)
}

// NetdiskRename renames a file.
func (a *App) NetdiskRename(req netdisk.RenameFileRequest) error {
	m, err := a.ndManager()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return m.RenameFile(ctx, req)
}

// NetdiskDelete deletes files.
func (a *App) NetdiskDelete(req netdisk.DeleteFilesRequest) error {
	m, err := a.ndManager()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	return m.DeleteFiles(ctx, req)
}

// NetdiskCopy copies files between folders.
func (a *App) NetdiskCopy(req netdisk.FileTransferRequest) error {
	m, err := a.ndManager()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	return m.CopyFiles(ctx, req)
}

// NetdiskMove moves files between folders.
func (a *App) NetdiskMove(req netdisk.FileTransferRequest) error {
	m, err := a.ndManager()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	return m.MoveFiles(ctx, req)
}

// NetdiskUpload uploads a local file to the drive, emitting progress.
func (a *App) NetdiskUpload(req netdisk.UploadFileRequest) (netdisk.UploadResult, error) {
	m, err := a.ndManager()
	if err != nil {
		return netdisk.UploadResult{}, err
	}
	ctx := context.Background()
	key := "netdisk-upload:" + req.LocalPath
	res, err := m.UploadFile(ctx, req, func(uploaded int64) {
		a.emit("transfer:progress", map[string]any{"key": key, "kind": "netdisk-upload", "done": uploaded})
	})
	if err == nil {
		a.emit("transfer:done", map[string]any{"key": key, "kind": "netdisk-upload", "path": res.Path, "bytes": res.Size})
	}
	return res, err
}

// NetdiskResolveLink resolves a direct download URL for a cloud file.
func (a *App) NetdiskResolveLink(req netdisk.ResolveDownloadRequest) (netdisk.DownloadLink, error) {
	m, err := a.ndManager()
	if err != nil {
		return netdisk.DownloadLink{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	return m.ResolveDownload(ctx, req)
}

// NetdiskEnqueueDownload resolves a link and hands it to the download engine.
func (a *App) NetdiskEnqueueDownload(req netdisk.ResolveDownloadRequest) (dlengine.Task, error) {
	link, err := a.NetdiskResolveLink(req)
	if err != nil {
		return dlengine.Task{}, err
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
	name := link.Filename
	if name == "" {
		name = req.Name
	}
	task, err := a.engine.Add(dlengine.Request{
		URL:       link.URL,
		Filename:  name,
		Headers:   headers,
		Referer:   link.Referer,
		ProxyMode: "inherit",
	})
	if err != nil {
		return dlengine.Task{}, err
	}
	if err := a.engine.Start(task.ID); err != nil {
		return task, err
	}
	return task, nil
}

// NetdiskCreateShare creates a share link.
func (a *App) NetdiskCreateShare(req netdisk.CreateShareRequest) (netdisk.ShareResult, error) {
	m, err := a.ndManager()
	if err != nil {
		return netdisk.ShareResult{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	return m.CreateShare(ctx, req)
}

// NetdiskShares lists existing shares.
func (a *App) NetdiskShares(accountID string, page, pageSize int) (netdisk.AccountShareList, error) {
	m, err := a.ndManager()
	if err != nil {
		return netdisk.AccountShareList{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	return m.ListAccountShares(ctx, netdisk.ListAccountSharesRequest{AccountID: accountID, Page: page, PageSize: pageSize})
}

// NetdiskCancelShares cancels shares.
func (a *App) NetdiskCancelShares(accountID string, shareIDs []string) error {
	m, err := a.ndManager()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	return m.CancelShares(ctx, netdisk.CancelSharesRequest{AccountID: accountID, ShareIDs: shareIDs})
}

// NetdiskRecycle lists the recycle bin.
func (a *App) NetdiskRecycle(accountID string, page, pageSize int) (netdisk.RecycleList, error) {
	m, err := a.ndManager()
	if err != nil {
		return netdisk.RecycleList{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	return m.ListRecycle(ctx, netdisk.ListRecycleRequest{AccountID: accountID, Page: page, PageSize: pageSize})
}

// NetdiskRestoreRecycle restores recycle-bin entries.
func (a *App) NetdiskRestoreRecycle(accountID string, itemIDs []string) error {
	m, err := a.ndManager()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	return m.RestoreRecycle(ctx, netdisk.RecycleMutationRequest{AccountID: accountID, ItemIDs: itemIDs})
}

// NetdiskDeleteRecycle purges recycle-bin entries.
func (a *App) NetdiskDeleteRecycle(accountID string, itemIDs []string) error {
	m, err := a.ndManager()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	return m.DeleteRecycle(ctx, netdisk.RecycleMutationRequest{AccountID: accountID, ItemIDs: itemIDs})
}

// NetdiskVideoPreview resolves playable video qualities.
func (a *App) NetdiskVideoPreview(req netdisk.VideoPreviewRequest) (netdisk.VideoPreviewResult, error) {
	m, err := a.ndManager()
	if err != nil {
		return netdisk.VideoPreviewResult{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	return m.ResolveVideoPreview(ctx, req)
}
