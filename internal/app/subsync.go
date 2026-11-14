package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"wailstools/internal/miproxy"
	"wailstools/internal/settings"
	"wailstools/internal/subscription"
)

// SubSyncResult summarises a subscription synchronisation pass.
type SubSyncResult struct {
	Local       int    `json:"local"`
	Remote      int    `json:"remote"`
	Merged      int    `json:"merged"`
	NodesLocal  int    `json:"nodesLocal"`
	NodesRemote int    `json:"nodesRemote"`
	NodesMerged int    `json:"nodesMerged"`
	Pushed      bool   `json:"pushed"`
	Message     string `json:"message"`
}

// syncDocument is the server-side document. It keeps subscriptions and custom
// nodes together so one round trip restores the whole proxy setup.
type syncDocument struct {
	Version int                   `json:"version"`
	Sources []subscription.Source `json:"sources"`
	Nodes   []miproxy.Node        `json:"nodes,omitempty"`
}

// SyncSubscriptions merges the local subscriptions + custom nodes with the
// server copy (per-item last-write-wins) and pushes the merged document back.
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
	var localNodes []miproxy.Node
	if a.customNodes != nil {
		localNodes = a.customNodes.List()
	}

	raw, _, found, err := a.link.GetSubscriptions(ctx)
	if err != nil {
		return SubSyncResult{}, err
	}
	var remote []subscription.Source
	var remoteNodes []miproxy.Node
	if found && len(raw) > 0 && string(raw) != "null" {
		remote, remoteNodes, err = parseSyncPayload(raw)
		if err != nil {
			return SubSyncResult{}, err
		}
	}

	merged := mergeSubscriptions(local, remote)
	if err := a.subs.Replace(merged); err != nil {
		return SubSyncResult{}, err
	}
	if a.customNodes != nil && len(remoteNodes) > 0 {
		if _, err := a.customNodes.MergeRemote(remoteNodes); err != nil {
			return SubSyncResult{}, err
		}
	}
	mergedNodes := localNodes
	if a.customNodes != nil {
		mergedNodes = a.customNodes.List()
	}

	payload, err := json.Marshal(syncDocument{Version: 2, Sources: merged, Nodes: mergedNodes})
	if err != nil {
		return SubSyncResult{}, err
	}
	if _, err := a.link.PutSubscriptions(ctx, payload); err != nil {
		return SubSyncResult{}, err
	}
	res := SubSyncResult{
		Local:       len(local),
		Remote:      len(remote),
		Merged:      len(merged),
		NodesLocal:  len(localNodes),
		NodesRemote: len(remoteNodes),
		NodesMerged: len(mergedNodes),
		Pushed:      true,
		Message:     fmt.Sprintf("订阅已同步（%d 个订阅 / %d 个自定义节点）", len(merged), len(mergedNodes)),
	}
	a.emit("subscription:synced", res)
	return res, nil
}

// parseSyncPayload accepts both the legacy array payload and the current
// {version, sources, nodes} document.
func parseSyncPayload(raw []byte) ([]subscription.Source, []miproxy.Node, error) {
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "[") {
		var legacy []subscription.Source
		if err := json.Unmarshal(raw, &legacy); err != nil {
			return nil, nil, fmt.Errorf("服务器订阅数据无效: %w", err)
		}
		return legacy, nil, nil
	}
	var doc syncDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, nil, fmt.Errorf("服务器同步数据无效: %w", err)
	}
	return doc.Sources, doc.Nodes, nil
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
