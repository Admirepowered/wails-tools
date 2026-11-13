// Package notes implements the local markdown note store and its two-way
// synchronisation with the WailsTools server.
package notes

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"wailstools/internal/serverlink"
)

// Note is a markdown note.
type Note struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Content   string   `json:"content"`
	Folder    string   `json:"folder,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Pinned    bool     `json:"pinned"`
	Deleted   bool     `json:"deleted"`
	UpdatedAt int64    `json:"updatedAt"`
	CreatedAt int64    `json:"createdAt"`
	Revision  int64    `json:"revision"`
	// Dirty marks a local edit that still has to be pushed.
	Dirty bool `json:"dirty"`
}

// Meta stores sync bookkeeping.
type Meta struct {
	LastSyncMS int64  `json:"lastSyncMs"`
	DeviceID   string `json:"deviceId"`
}

type document struct {
	Notes []Note `json:"notes"`
	Meta  Meta   `json:"meta"`
}

// Store is the local note database (a single JSON document).
type Store struct {
	mu   sync.RWMutex
	path string
	doc  document
}

// Open loads the local note store from dir.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dir, "notes.json")}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(data, &s.doc); err != nil {
		return nil, err
	}
	return s, nil
}

// Path exposes the backing file.
func (s *Store) Path() string { return s.path }

func (s *Store) persistLocked() error {
	data, err := json.MarshalIndent(s.doc, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// List returns notes sorted by folder, pin state then recency.
func (s *Store) List(includeDeleted bool) []Note {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Note, 0, len(s.doc.Notes))
	for _, n := range s.doc.Notes {
		if n.Deleted && !includeDeleted {
			continue
		}
		out = append(out, n)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Pinned != out[j].Pinned {
			return out[i].Pinned
		}
		if out[i].UpdatedAt != out[j].UpdatedAt {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title)
	})
	return out
}

// Get returns one note.
func (s *Store) Get(id string) (Note, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, n := range s.doc.Notes {
		if n.ID == id {
			return n, true
		}
	}
	return Note{}, false
}

// Save creates or updates a note and marks it dirty.
func (s *Store) Save(n Note) (Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UnixMilli()
	if strings.TrimSpace(n.ID) == "" {
		n.ID = newNoteID()
	}
	if n.CreatedAt <= 0 {
		n.CreatedAt = now
	}
	n.UpdatedAt = now
	n.Dirty = true
	found := false
	for i := range s.doc.Notes {
		if s.doc.Notes[i].ID == n.ID {
			s.doc.Notes[i] = n
			found = true
			break
		}
	}
	if !found {
		s.doc.Notes = append(s.doc.Notes, n)
	}
	return n, s.persistLocked()
}

// Delete tombstones a note locally.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.doc.Notes {
		if s.doc.Notes[i].ID == id {
			s.doc.Notes[i].Deleted = true
			s.doc.Notes[i].Content = ""
			s.doc.Notes[i].UpdatedAt = time.Now().UnixMilli()
			s.doc.Notes[i].Dirty = true
			return s.persistLocked()
		}
	}
	return errors.New("note not found")
}

// Dirty returns notes pending upload.
func (s *Store) Dirty() []Note {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Note, 0, len(s.doc.Notes))
	for _, n := range s.doc.Notes {
		if n.Dirty {
			out = append(out, n)
		}
	}
	return out
}

// LastSync returns the last successful sync timestamp.
func (s *Store) LastSync() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.doc.Meta.LastSyncMS
}

// MarkSynced clears the dirty flag and records the sync time.
func (s *Store) MarkSynced(ids []string, serverTime int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	for i := range s.doc.Notes {
		if set[s.doc.Notes[i].ID] {
			s.doc.Notes[i].Dirty = false
		}
	}
	if serverTime > s.doc.Meta.LastSyncMS {
		s.doc.Meta.LastSyncMS = serverTime
	}
	return s.persistLocked()
}

// Merge absorbs remote notes using last-write-wins with tombstones.
func (s *Store) Merge(remote []Note) (updated int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := map[string]int{}
	for i, n := range s.doc.Notes {
		index[n.ID] = i
	}
	for _, r := range remote {
		if r.ID == "" {
			continue
		}
		i, ok := index[r.ID]
		if !ok {
			local := Note{
				ID: r.ID, Title: r.Title, Content: r.Content, Folder: r.Folder,
				Tags: r.Tags, Pinned: r.Pinned, Deleted: r.Deleted,
				UpdatedAt: r.UpdatedAt, CreatedAt: r.CreatedAt, Revision: r.Revision,
			}
			s.doc.Notes = append(s.doc.Notes, local)
			index[r.ID] = len(s.doc.Notes) - 1
			updated++
			continue
		}
		cur := &s.doc.Notes[i]
		if cur.Dirty && cur.UpdatedAt > r.UpdatedAt {
			continue // local edit is newer; it will be pushed next round
		}
		if r.UpdatedAt >= cur.UpdatedAt {
			cur.Title = r.Title
			cur.Content = r.Content
			cur.Folder = r.Folder
			cur.Tags = r.Tags
			cur.Pinned = r.Pinned
			cur.Deleted = r.Deleted
			cur.UpdatedAt = r.UpdatedAt
			cur.Revision = r.Revision
			if r.Deleted {
				cur.Content = ""
			}
			cur.Dirty = false
			updated++
		}
	}
	if updated > 0 {
		err = s.persistLocked()
	}
	return updated, err
}

// PurgeTombstones removes deleted notes older than the given age.
func (s *Store) PurgeTombstones(olderThan time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-olderThan).UnixMilli()
	out := s.doc.Notes[:0]
	for _, n := range s.doc.Notes {
		if n.Deleted && n.UpdatedAt < cutoff {
			continue
		}
		out = append(out, n)
	}
	s.doc.Notes = out
	return s.persistLocked()
}

// ToRemote converts a local note for the wire.
func ToRemote(n Note) serverlink.Note {
	return serverlink.Note{
		ID: n.ID, Title: n.Title, Content: n.Content, Folder: n.Folder,
		Tags: n.Tags, Pinned: n.Pinned, Deleted: n.Deleted,
		UpdatedAt: n.UpdatedAt, CreatedAt: n.CreatedAt, Revision: n.Revision,
	}
}

// FromRemote converts a wire note to the local shape.
func FromRemote(n serverlink.Note) Note {
	return Note{
		ID: n.ID, Title: n.Title, Content: n.Content, Folder: n.Folder,
		Tags: n.Tags, Pinned: n.Pinned, Deleted: n.Deleted,
		UpdatedAt: n.UpdatedAt, CreatedAt: n.CreatedAt, Revision: n.Revision,
	}
}

// ExportMarkdown renders a note as a markdown document with front matter.
func (n Note) ExportMarkdown() string {
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(n.Title)
	b.WriteString("\n\n")
	if len(n.Tags) > 0 {
		b.WriteString("Tags: ")
		b.WriteString(strings.Join(n.Tags, ", "))
		b.WriteString("\n\n")
	}
	b.WriteString(n.Content)
	return b.String()
}

// Checksum returns a stable content hash used for conflict detection.
func (n Note) Checksum() string {
	sum := sha256.Sum256([]byte(n.Title + "\x00" + n.Content))
	return hex.EncodeToString(sum[:8])
}

func newNoteID() string {
	return "note-" + strings.ReplaceAll(timeNowRFC(), ":", "")
}

var nowFn = time.Now

func timeNowRFC() string {
	return nowFn().Format("20060102T150405.000")
}
