package app

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// fileLogger writes a fresh log file on every start so the user can always
// find the current session's diagnostics.
type fileLogger struct {
	mu   sync.Mutex
	file *os.File
	path string
}

// newFileLogger truncates <root>/logs/app.log and returns a logger.
func newFileLogger(root string) *fileLogger {
	dir := filepath.Join(root, "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return &fileLogger{}
	}
	path := filepath.Join(dir, "app.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644) // 每次启动清空
	if err != nil {
		return &fileLogger{}
	}
	l := &fileLogger{file: f, path: path}
	l.Printf("=== WailsTools 启动 %s ===", time.Now().Format("2006-01-02 15:04:05"))
	return l
}

// Printf appends a timestamped line.
func (l *fileLogger) Printf(format string, args ...any) {
	if l == nil || l.file == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	line := fmt.Sprintf(format, args...)
	fmt.Fprintf(l.file, "%s %s\n", time.Now().Format("15:04:05.000"), line)
	// 立即落盘，保证运行中用别的程序也能看到最新日志。
	_ = l.file.Sync()
}

// Close flushes and closes the log.
func (l *fileLogger) Close() {
	if l == nil || l.file == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = l.file.Sync()
	_ = l.file.Close()
	l.file = nil
}

// Path returns the log file location.
func (l *fileLogger) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}
