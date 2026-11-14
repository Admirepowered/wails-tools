package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"wailstools/internal/settings"
	"wailstools/internal/subscription"
)

// SubSyncResult summarises a subscription synchronisation pass.
type SubSyncResult struct {
	Local   int    `json:"local"`
	Remote  int    `json:"remote"`
	Merged  int    `json:"merged"`
	Pushed  bool   `json:"pushed"`
	Message string `json:"message"`
}

// SyncSubscriptions merges the local subscriptions with the server copy
// (per-source last-write-wins) and pushes the merged list back.
func (a *App) SyncSubscriptions() (SubSyncResult, error) {
	if !a.link.Available() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := a.link.Connect(ctx); err != nil {
			return SubSyncResult{}, fmt.Errorf("未连接服务器: %w", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	local := a.subs.Snapshot()
	raw, _, found, err := a.link.GetSubscriptions(ctx)
	if err != nil {
		return SubSyncResult{}, err
	}
	var remote []subscription.Source
	if found && len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &remote); err != nil {
			return SubSyncResult{}, fmt.Errorf("服务器订阅数据无效: %w", err)
		}
	}

	merged := mergeSubscriptions(local, remote)
	if err := a.subs.Replace(merged); err != nil {
		return SubSyncResult{}, err
	}
	payload, err := json.Marshal(merged)
	if err != nil {
		return SubSyncResult{}, err
	}
	if _, err := a.link.PutSubscriptions(ctx, payload); err != nil {
		return SubSyncResult{}, err
	}
	res := SubSyncResult{
		Local:   len(local),
		Remote:  len(remote),
		Merged:  len(merged),
		Pushed:  true,
		Message: fmt.Sprintf("订阅已同步（本地 %d / 远端 %d → %d）", len(local), len(remote), len(merged)),
	}
	a.emit("subscription:synced", res)
	return res, nil
}

// mergeSubscriptions combines two lists by ID, keeping the newer updatedAt.
func mergeSubscriptions(local, remote []subscription.Source) []subscription.Source {
	byID := map[string]subscription.Source{}
	order := make([]string, 0, len(local)+len(remote))
	add := func(src subscription.Source, incoming bool) {
		if src.ID == "" {
			if incoming {
				return
			}
			src.ID = "sub-" + fmt.Sprintf("%d", time.Now().UnixNano())
		}
		cur, ok := byID[src.ID]
		if !ok {
			byID[src.ID] = src
			order = append(order, src.ID)
			return
		}
		if newer(src, cur) {
			byID[src.ID] = src
		}
	}
	for _, s := range local {
		add(s, false)
	}
	for _, s := range remote {
		add(s, true)
	}
	out := make([]subscription.Source, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func newer(a, b subscription.Source) bool {
	ta, errA := time.Parse(time.RFC3339, a.UpdatedAt)
	tb, errB := time.Parse(time.RFC3339, b.UpdatedAt)
	if errA != nil && errB != nil {
		return false
	}
	if errA != nil {
		return false
	}
	if errB != nil {
		return true
	}
	return ta.After(tb)
}

// ExtractCloudFile unpacks an archive stored in the cloud space.
func (a *App) ExtractCloudFile(remotePath string) (map[string]any, error) {
	if remotePath == "" {
		return nil, errors.New("请选择要解压的文件")
	}
	if !a.link.Available() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := a.link.Connect(ctx); err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := a.link.ExtractCloudArchive(ctx, remotePath)
	if err != nil {
		return nil, err
	}
	a.emit("cloud:extracted", res)
	return res, nil
}

// SaveLastLocalDir remembers the last local folder for the file manager.
func (a *App) SaveLastLocalDir(dir string) error {
	_, err := a.settings.Update(func(s *settings.Settings) error {
		s.UI.LastLocalDir = dir
		return nil
	})
	return err
}

// LastLocalDir returns the remembered local folder.
func (a *App) LastLocalDir() string { return a.settings.Get().UI.LastLocalDir }
