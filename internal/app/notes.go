package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"wailstools/internal/notes"
	"wailstools/internal/serverlink"
)

// ListNotes returns local notes.
func (a *App) ListNotes(includeDeleted bool) []notes.Note {
	return a.notes.List(includeDeleted)
}

// GetNote returns a single note.
func (a *App) GetNote(id string) (notes.Note, error) {
	n, ok := a.notes.Get(id)
	if !ok {
		return notes.Note{}, errors.New("note not found")
	}
	return n, nil
}

// SaveNote creates or updates a note.
func (a *App) SaveNote(n notes.Note) (notes.Note, error) {
	return a.notes.Save(n)
}

// DeleteNote removes a note locally (and remotely on the next sync).
func (a *App) DeleteNote(id string) error { return a.notes.Delete(id) }

// NoteFolders lists the folders currently in use.
func (a *App) NoteFolders() []string {
	set := map[string]bool{}
	for _, n := range a.notes.List(false) {
		if f := strings.TrimSpace(n.Folder); f != "" {
			set[f] = true
		}
	}
	out := make([]string, 0, len(set))
	for f := range set {
		out = append(out, f)
	}
	return out
}

// SyncResult summarises one synchronisation pass.
type SyncResult struct {
	Pushed     int    `json:"pushed"`
	Pulled     int    `json:"pulled"`
	Conflicts  int    `json:"conflicts"`
	ServerTime int64  `json:"serverTime"`
	Transport  string `json:"transport"`
	Message    string `json:"message"`
}

// SyncNotes performs a two-way sync with the server.
func (a *App) SyncNotes() (SyncResult, error) {
	if !a.link.Available() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := a.link.Connect(ctx); err != nil {
			return SyncResult{}, fmt.Errorf("未连接服务器: %w", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	dirty := a.notes.Dirty()
	changes := make([]serverlink.Note, 0, len(dirty))
	for _, n := range dirty {
		changes = append(changes, notes.ToRemote(n))
	}
	remoteRaw, conflictsRaw, serverTime, err := a.link.NotesSync(ctx, a.notes.LastSync(), changes)
	if err != nil {
		return SyncResult{}, err
	}
	converted := make([]notes.Note, 0, len(remoteRaw))
	for _, r := range remoteRaw {
		converted = append(converted, notes.FromRemote(r))
	}
	pulled, err := a.notes.Merge(converted)
	if err != nil {
		return SyncResult{}, err
	}
	ids := make([]string, 0, len(dirty))
	for _, n := range dirty {
		ids = append(ids, n.ID)
	}
	if err := a.notes.MarkSynced(ids, serverTime); err != nil {
		return SyncResult{}, err
	}
	res := SyncResult{
		Pushed:     len(dirty),
		Pulled:     pulled,
		Conflicts:  len(conflictsRaw),
		ServerTime: serverTime,
		Transport:  a.link.Status().Transport,
		Message:    "同步完成",
	}
	if len(conflictsRaw) > 0 {
		res.Message = "同步完成（存在冲突，已保留较新版本）"
	}
	a.emit("notes:synced", res)
	return res, nil
}

// ExportNoteMarkdown writes a note to disk as markdown.
func (a *App) ExportNoteMarkdown(id, target string) (string, error) {
	n, ok := a.notes.Get(id)
	if !ok {
		return "", errors.New("note not found")
	}
	if target == "" {
		dir := a.settings.Get().Downloads.Directory
		name := safeFileName(n.Title)
		if name == "" {
			name = "note"
		}
		target = filepath.Join(dir, name+".md")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	return target, os.WriteFile(target, []byte(n.ExportMarkdown()), 0o644)
}

func safeFileName(s string) string {
	bad := `<>:"/\|?*`
	out := strings.Map(func(r rune) rune {
		if strings.ContainsRune(bad, r) {
			return '_'
		}
		return r
	}, strings.TrimSpace(s))
	if len(out) > 80 {
		out = out[:80]
	}
	return out
}
