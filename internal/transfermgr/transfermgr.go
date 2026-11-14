// Package transfermgr tracks long-running file transfers and lets the user
// pause, resume and cancel them. Runners must be restartable from an offset.
package transfermgr

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Status values.
const (
	StatusRunning  = "running"
	StatusPaused   = "paused"
	StatusDone     = "done"
	StatusFailed   = "failed"
	StatusCanceled = "canceled"
)

// Item is the UI-facing transfer record.
type Item struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Label     string `json:"label"`
	Path      string `json:"path"`
	Total     int64  `json:"total"`
	Done      int64  `json:"done"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	StartedAt string `json:"startedAt"`
	UpdatedAt string `json:"updatedAt"`
}

// Runner copies data starting at offset and reports absolute progress.
// It must return ctx.Err() promptly when the context is cancelled.
type Runner func(ctx context.Context, offset int64, progress func(done int64)) (int64, error)

type job struct {
	mu     sync.Mutex
	item   Item
	run    Runner
	cancel context.CancelFunc
	offset int64
	done   chan struct{}
}

// Manager owns all transfers.
type Manager struct {
	mu      sync.Mutex
	items   map[string]*job
	order   []string
	seq     int
	onEvent func(Item)
}

// New creates a manager; onEvent is called on every state change.
func New(onEvent func(Item)) *Manager {
	if onEvent == nil {
		onEvent = func(Item) {}
	}
	return &Manager{items: map[string]*job{}, onEvent: onEvent}
}

// Start registers and launches a transfer.
func (m *Manager) Start(kind, label, path string, total int64, run Runner) string {
	m.mu.Lock()
	m.seq++
	id := fmt.Sprintf("tr-%d", time.Now().UnixNano())
	now := time.Now().Format(time.RFC3339)
	j := &job{
		item: Item{ID: id, Kind: kind, Label: label, Path: path, Total: total, Status: StatusRunning, StartedAt: now, UpdatedAt: now},
		run:  run,
		done: make(chan struct{}),
	}
	m.items[id] = j
	m.order = append(m.order, id)
	m.mu.Unlock()
	go m.execute(j, 0)
	return id
}

func (m *Manager) execute(j *job, offset int64) {
	ctx, cancel := context.WithCancel(context.Background())
	j.mu.Lock()
	j.cancel = cancel
	j.offset = offset
	j.item.Status = StatusRunning
	j.item.Error = ""
	j.item.UpdatedAt = time.Now().Format(time.RFC3339)
	j.mu.Unlock()
	m.emit(j)

	done, err := j.run(ctx, offset, func(d int64) {
		j.mu.Lock()
		j.offset = d
		j.item.Done = d
		if j.item.Total > 0 && d > j.item.Total {
			j.item.Done = j.item.Total
		}
		j.item.UpdatedAt = time.Now().Format(time.RFC3339)
		item := j.item
		j.mu.Unlock()
		m.onEvent(item)
	})

	j.mu.Lock()
	j.cancel = nil
	switch {
	case err == nil:
		j.item.Status = StatusDone
		j.item.Done = max64(done, j.item.Done)
		if j.item.Total <= 0 {
			j.item.Total = j.item.Done
		}
	case errors.Is(err, context.Canceled):
		if j.item.Status != StatusCanceled {
			j.item.Status = StatusPaused
		}
	default:
		j.item.Status = StatusFailed
		j.item.Error = err.Error()
	}
	j.item.UpdatedAt = time.Now().Format(time.RFC3339)
	j.mu.Unlock()
	m.emit(j)
}

// Pause stops a running transfer, keeping its offset for resume.
func (m *Manager) Pause(id string) error {
	j, err := m.job(id)
	if err != nil {
		return err
	}
	j.mu.Lock()
	if j.item.Status != StatusRunning || j.cancel == nil {
		j.mu.Unlock()
		return errors.New("该传输当前不可暂停")
	}
	j.item.Status = StatusPaused
	cancel := j.cancel
	j.mu.Unlock()
	cancel()
	m.emit(j)
	return nil
}

// Resume continues a paused transfer from its offset.
func (m *Manager) Resume(id string) error {
	j, err := m.job(id)
	if err != nil {
		return err
	}
	j.mu.Lock()
	if j.item.Status != StatusPaused {
		j.mu.Unlock()
		return errors.New("该传输当前不可继续")
	}
	offset := j.offset
	j.mu.Unlock()
	go m.execute(j, offset)
	return nil
}

// Cancel aborts a transfer.
func (m *Manager) Cancel(id string) error {
	j, err := m.job(id)
	if err != nil {
		return err
	}
	j.mu.Lock()
	j.item.Status = StatusCanceled
	cancel := j.cancel
	j.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.emit(j)
	return nil
}

// Remove forgets a finished transfer.
func (m *Manager) Remove(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.items[id]; !ok {
		return errors.New("传输记录不存在")
	}
	delete(m.items, id)
	out := m.order[:0]
	for _, x := range m.order {
		if x != id {
			out = append(out, x)
		}
	}
	m.order = out
	return nil
}

// List returns transfers newest first.
func (m *Manager) List() []Item {
	m.mu.Lock()
	order := append([]string(nil), m.order...)
	m.mu.Unlock()
	out := make([]Item, 0, len(order))
	for i := len(order) - 1; i >= 0; i-- {
		m.mu.Lock()
		j, ok := m.items[order[i]]
		m.mu.Unlock()
		if !ok {
			continue
		}
		j.mu.Lock()
		out = append(out, j.item)
		j.mu.Unlock()
	}
	return out
}

// Get returns one transfer.
func (m *Manager) Get(id string) (Item, bool) {
	j, err := m.job(id)
	if err != nil {
		return Item{}, false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.item, true
}

func (m *Manager) job(id string) (*job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.items[id]
	if !ok {
		return nil, errors.New("传输记录不存在")
	}
	return j, nil
}

func (m *Manager) emit(j *job) {
	j.mu.Lock()
	item := j.item
	j.mu.Unlock()
	m.onEvent(item)
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
