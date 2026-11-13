// Package dlengine implements the HTTP(S) download engine: concurrent chunked
// transfers, resumable state, retries, speed accounting and a task queue.
package dlengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"wailstools/internal/settings"
)

// Task status values.
const (
	StatusQueued      = "queued"
	StatusPreparing   = "preparing"
	StatusDownloading = "downloading"
	StatusPaused      = "paused"
	StatusCompleted   = "completed"
	StatusFailed      = "failed"
	StatusCanceled    = "canceled"
)

// Request describes a new download.
type Request struct {
	URL         string            `json:"url"`
	Filename    string            `json:"filename"`
	Directory   string            `json:"directory"`
	Headers     map[string]string `json:"headers"`
	Referer     string            `json:"referer"`
	Connections int               `json:"connections"`
	ProxyMode   string            `json:"proxyMode"` // inherit|off|on
}

// Task is the persisted, UI-facing download record.
type Task struct {
	ID          string            `json:"id"`
	URL         string            `json:"url"`
	FinalURL    string            `json:"finalUrl,omitempty"`
	Filename    string            `json:"filename"`
	Directory   string            `json:"directory"`
	Total       int64             `json:"total"`
	Downloaded  int64             `json:"downloaded"`
	Status      string            `json:"status"`
	Error       string            `json:"error,omitempty"`
	Connections int               `json:"connections"`
	SpeedBps    float64           `json:"speedBps"`
	Percent     float64           `json:"percent"`
	ETag        string            `json:"etag,omitempty"`
	RangeOK     bool              `json:"rangeOk"`
	Headers     map[string]string `json:"headers,omitempty"`
	Referer     string            `json:"referer,omitempty"`
	ProxyMode   string            `json:"proxyMode"`
	ProxyUsed   string            `json:"proxyUsed,omitempty"`
	CreatedAt   string            `json:"createdAt"`
	UpdatedAt   string            `json:"updatedAt"`
	CompletedAt string            `json:"completedAt,omitempty"`
	Retries     int               `json:"retries"`
}

// Event is pushed to the UI.
type Event struct {
	Kind string   `json:"kind"` // task | log | done
	Task *Task    `json:"task,omitempty"`
	Log  string   `json:"log,omitempty"`
	IDs  []string `json:"ids,omitempty"`
}

// chunk is one byte range of a multi-connection download.
type chunk struct {
	Index int   `json:"index"`
	Start int64 `json:"start"`
	End   int64 `json:"end"`  // inclusive
	Done  int64 `json:"done"` // bytes written so far
}

// state is the resumable side-car.
type state struct {
	URL       string            `json:"url"`
	FinalURL  string            `json:"finalUrl"`
	Filename  string            `json:"filename"`
	Total     int64             `json:"total"`
	ETag      string            `json:"etag"`
	RangeOK   bool              `json:"rangeOk"`
	Chunks    []chunk           `json:"chunks"`
	Headers   map[string]string `json:"headers,omitempty"`
	Referer   string            `json:"referer,omitempty"`
	UpdatedAt string            `json:"updatedAt"`
}

// taskRunner holds live state for an in-flight task.
type taskRunner struct {
	mu        sync.Mutex
	task      Task
	state     state
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	chunkWG   sync.WaitGroup
	doneBytes int64
	speed     speedMeter
}

type speedMeter struct {
	mu       sync.Mutex
	lastAt   time.Time
	lastDone int64
	speed    float64
}

func (s *speedMeter) sample(done int64) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if s.lastAt.IsZero() {
		s.lastAt, s.lastDone = now, done
		return 0
	}
	el := now.Sub(s.lastAt).Seconds()
	if el < 0.25 {
		return s.speed
	}
	delta := float64(done - s.lastDone)
	s.speed = delta / el
	s.lastAt, s.lastDone = now, done
	return s.speed
}

// Engine runs downloads under a concurrency limit.
type Engine struct {
	store   *settings.Store
	baseDir string
	onEvent func(Event)

	mu    sync.RWMutex
	tasks map[string]*taskRunner
	order []string

	sem chan struct{}
}

// NewEngine loads pending tasks from dir.
func NewEngine(store *settings.Store, dir string, onEvent func(Event)) (*Engine, error) {
	if dir == "" {
		dir = store.Get().Downloads.Directory
	}
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	e := &Engine{store: store, baseDir: dir, tasks: map[string]*taskRunner{}, onEvent: onEvent}
	max := store.Get().Downloads.MaxConcurrency
	if max <= 0 {
		max = 3
	}
	e.sem = make(chan struct{}, max)
	if err := e.load(); err != nil {
		return nil, err
	}
	return e, nil
}

func (e *Engine) tasksPath() string { return filepath.Join(e.baseDir, "tasks.json") }

func (e *Engine) load() error {
	data, err := os.ReadFile(e.tasksPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var list []Task
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	for _, t := range list {
		switch t.Status {
		case StatusDownloading, StatusPreparing:
			t.Status = StatusPaused
			t.SpeedBps = 0
		}
		st := readState(e.partPath(t.ID))
		if st != nil {
			t.Total = st.Total
			t.RangeOK = st.RangeOK
			var done int64
			for _, c := range st.Chunks {
				done += c.Done
			}
			t.Downloaded = done
			if t.Total > 0 {
				t.Percent = float64(done) / float64(t.Total) * 100
			}
		}
		tr := &taskRunner{task: t, done: make(chan struct{})}
		if st != nil {
			tr.state = *st
		}
		e.tasks[t.ID] = tr
		e.order = append(e.order, t.ID)
	}
	return nil
}

func (e *Engine) persist() {
	e.mu.RLock()
	list := make([]Task, 0, len(e.tasks))
	for _, tr := range e.tasks {
		tr.mu.Lock()
		list = append(list, tr.task)
		tr.mu.Unlock()
	}
	e.mu.RUnlock()
	sort.SliceStable(list, func(i, j int) bool { return list[i].CreatedAt < list[j].CreatedAt })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return
	}
	tmp := e.tasksPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, e.tasksPath())
}

func (e *Engine) partPath(id string) string {
	return filepath.Join(cacheDirQuality(e.baseDir), id+".state.json")
}

func cacheDirQuality(base string) string {
	d := filepath.Join(base, ".wt-state")
	_ = os.MkdirAll(d, 0o755)
	return d
}

func readState(path string) *state {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var st state
	if err := json.Unmarshal(data, &st); err != nil {
		return nil
	}
	return &st
}

func (e *Engine) writeState(id string, st state) {
	st.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(e.partPath(id), data, 0o600)
}

func (e *Engine) emit(ev Event) {
	if e.onEvent != nil {
		e.onEvent(ev)
	}
}

func (e *Engine) emitTask(tr *taskRunner) {
	tr.mu.Lock()
	t := tr.task
	tr.mu.Unlock()
	e.emit(Event{Kind: "task", Task: &t})
}

// List returns all tasks ordered by creation time.
func (e *Engine) List() []Task {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]Task, 0, len(e.tasks))
	for _, id := range e.order {
		tr, ok := e.tasks[id]
		if !ok {
			continue
		}
		tr.mu.Lock()
		t := tr.task
		if t.Status == StatusDownloading {
			t.SpeedBps = tr.speed.speed
			if t.Total > 0 {
				t.Percent = float64(t.Downloaded) / float64(t.Total) * 100
			}
		}
		tr.mu.Unlock()
		out = append(out, t)
	}
	return out
}

// Get returns a single task.
func (e *Engine) Get(id string) (Task, bool) {
	e.mu.RLock()
	tr, ok := e.tasks[id]
	e.mu.RUnlock()
	if !ok {
		return Task{}, false
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	t := tr.task
	if t.Total > 0 {
		t.Percent = float64(t.Downloaded) / float64(t.Total) * 100
	}
	return t, true
}

func newTaskID() string {
	return fmt.Sprintf("dl-%d", time.Now().UnixNano())
}

func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.Map(func(r rune) rune {
		switch r {
		case ':', '*', '?', '"', '<', '>', '|':
			return '_'
		}
		return r
	}, name)
	name = strings.Trim(name, ". ")
	if name == "" {
		name = "download.bin"
	}
	if len(name) > 180 {
		name = name[:180]
	}
	return name
}

// Add registers a task. It does not start it.
func (e *Engine) Add(req Request) (Task, error) {
	url := strings.TrimSpace(req.URL)
	if url == "" {
		return Task{}, errors.New("url is required")
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return Task{}, errors.New("only http(s) urls are supported")
	}
	cfg := e.store.Get()
	dir := req.Directory
	if dir == "" {
		dir = cfg.Downloads.Directory
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Task{}, err
	}
	name := sanitizeFilename(req.Filename)
	if req.Filename == "" {
		name = sanitizeFilename(guessFilename(url))
	}
	conns := req.Connections
	if conns <= 0 {
		conns = cfg.Downloads.ConnectionsPer
	}
	if conns <= 0 {
		conns = 8
	}
	if conns > 32 {
		conns = 32
	}
	now := time.Now().UTC().Format(time.RFC3339)
	t := Task{
		ID:          newTaskID(),
		URL:         url,
		Filename:    name,
		Directory:   dir,
		Status:      StatusQueued,
		Connections: conns,
		Headers:     req.Headers,
		Referer:     req.Referer,
		ProxyMode:   strings.ToLower(strings.TrimSpace(req.ProxyMode)),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	tr := &taskRunner{task: t, done: make(chan struct{})}
	e.mu.Lock()
	e.tasks[t.ID] = tr
	e.order = append(e.order, t.ID)
	e.mu.Unlock()
	e.persist()
	e.emitTask(tr)
	return t, nil
}

func guessFilename(raw string) string {
	u := raw
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	u = strings.TrimSuffix(u, "/")
	seg := u
	if i := strings.LastIndex(u, "/"); i >= 0 {
		seg = u[i+1:]
	}
	if seg == "" || !strings.Contains(seg, ".") {
		return "download.bin"
	}
	return seg
}

// Start begins (or resumes) a task.
func (e *Engine) Start(id string) error {
	e.mu.RLock()
	tr, ok := e.tasks[id]
	e.mu.RUnlock()
	if !ok {
		return errors.New("task not found")
	}
	tr.mu.Lock()
	status := tr.task.Status
	if status == StatusDownloading || status == StatusPreparing {
		if tr.ctx != nil && tr.ctx.Err() == nil {
			tr.mu.Unlock()
			return nil
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	tr.ctx = ctx
	tr.cancel = cancel
	tr.done = make(chan struct{})
	tr.task.Status = StatusPreparing
	tr.task.Error = ""
	tr.mu.Unlock()

	e.emitTask(tr)
	go e.run(ctx, tr)
	return nil
}

// Pause stops a running task, keeping its state for resume.
func (e *Engine) Pause(id string) error {
	e.mu.RLock()
	tr, ok := e.tasks[id]
	e.mu.RUnlock()
	if !ok {
		return errors.New("task not found")
	}
	tr.mu.Lock()
	if tr.cancel != nil {
		tr.cancel()
	}
	if tr.task.Status == StatusDownloading || tr.task.Status == StatusPreparing || tr.task.Status == StatusQueued {
		tr.task.Status = StatusPaused
	}
	tr.task.SpeedBps = 0
	tr.mu.Unlock()
	e.writeState(id, tr.state)
	e.persist()
	e.emitTask(tr)
	return nil
}

// Cancel aborts a task and removes its partial data.
func (e *Engine) Cancel(id string) error {
	e.mu.RLock()
	tr, ok := e.tasks[id]
	e.mu.RUnlock()
	if !ok {
		return errors.New("task not found")
	}
	tr.mu.Lock()
	if tr.cancel != nil {
		tr.cancel()
	}
	tr.task.Status = StatusCanceled
	tr.task.SpeedBps = 0
	dir := tr.task.Directory
	name := tr.task.Filename
	tr.mu.Unlock()
	e.cleanupPartial(id, dir, name, len(tr.state.Chunks))
	_ = os.Remove(e.partPath(id))
	e.persist()
	e.emitTask(tr)
	return nil
}

// Remove deletes a task (and optionally its file).
func (e *Engine) Remove(id string, deleteFile bool) error {
	e.mu.Lock()
	tr, ok := e.tasks[id]
	if ok {
		delete(e.tasks, id)
		out := e.order[:0]
		for _, x := range e.order {
			if x != id {
				out = append(out, x)
			}
		}
		e.order = out
	}
	e.mu.Unlock()
	if !ok {
		return errors.New("task not found")
	}
	tr.mu.Lock()
	if tr.cancel != nil {
		tr.cancel()
	}
	dir, name := tr.task.Directory, tr.task.Filename
	nChunks := len(tr.state.Chunks)
	tr.mu.Unlock()
	e.cleanupPartial(id, dir, name, nChunks)
	_ = os.Remove(e.partPath(id))
	if deleteFile {
		_ = os.Remove(filepath.Join(dir, name))
	}
	e.persist()
	return nil
}

func (e *Engine) cleanupPartial(id, dir, name string, nChunks int) {
	_ = os.Remove(filepath.Join(dir, name+".part"))
	for i := 0; i < nChunks+1; i++ {
		_ = os.Remove(filepath.Join(dir, fmt.Sprintf("%s.part.%d", name, i)))
	}
	_ = os.Remove(filepath.Join(dir, name+".tmp"))
}

// PauseAll pauses every active task.
func (e *Engine) PauseAll() {
	for _, t := range e.List() {
		if t.Status == StatusDownloading || t.Status == StatusQueued || t.Status == StatusPreparing {
			_ = e.Pause(t.ID)
		}
	}
}

// ClearFinished removes finished/failed/canceled records.
func (e *Engine) ClearFinished() {
	for _, t := range e.List() {
		switch t.Status {
		case StatusCompleted, StatusFailed, StatusCanceled:
			_ = e.Remove(t.ID, false)
		}
	}
}

var _ = atomic.AddInt64
