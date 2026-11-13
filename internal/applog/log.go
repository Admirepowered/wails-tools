package applog

import (
	"encoding/json"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	mu     sync.Mutex
	writer io.Writer = io.Discard
	file   *os.File
)

type entry struct {
	Time       string         `json:"time"`
	Level      string         `json:"level"`
	Component  string         `json:"component"`
	Action     string         `json:"action"`
	Message    string         `json:"message,omitempty"`
	DurationMS int64          `json:"duration_ms,omitempty"`
	Fields     map[string]any `json:"fields,omitempty"`
	Error      string         `json:"error,omitempty"`
}

func Init(logDir string) (*os.File, error) {
	if strings.TrimSpace(logDir) == "" {
		return nil, nil
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, err
	}
	mu.Lock()
	if file != nil {
		_ = file.Close()
		file = nil
		writer = io.Discard
	}
	mu.Unlock()

	if err := resetLogFiles(logDir); err != nil {
		return nil, err
	}
	path := filepath.Join(logDir, "wailstools-"+time.Now().Format("2006-01-02")+".log")
	next, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}

	mu.Lock()
	file = next
	// Windows GUI builds can have an invalid stderr handle. Write the file first
	// so logging still works even if the optional stderr write fails.
	writer = io.MultiWriter(next, os.Stderr)
	mu.Unlock()

	log.SetOutput(writer)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	Info("app", "logger_ready", map[string]any{"path": path})
	return next, nil
}

func resetLogFiles(logDir string) error {
	entries, err := os.ReadDir(logDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, "wailstools-") && strings.HasSuffix(name, ".log") {
			if err := os.Remove(filepath.Join(logDir, name)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

func Info(component, action string, fields map[string]any) {
	write("info", component, action, "", 0, fields, nil)
}

func Warn(component, action, message string, fields map[string]any) {
	write("warn", component, action, message, 0, fields, nil)
}

func Error(component, action string, err error, fields map[string]any) {
	write("error", component, action, "", 0, fields, err)
}

func Begin(component, action string, fields map[string]any) func(error, map[string]any) {
	start := time.Now()
	Info(component, action+"_start", fields)
	return func(err error, finishFields map[string]any) {
		merged := merge(fields, finishFields)
		write(levelFor(err), component, action+"_finish", "", time.Since(start).Milliseconds(), merged, err)
	}
}

func write(level, component, action, message string, durationMS int64, fields map[string]any, err error) {
	item := entry{
		Time:       time.Now().Format(time.RFC3339Nano),
		Level:      level,
		Component:  component,
		Action:     action,
		Message:    message,
		DurationMS: durationMS,
		Fields:     sanitizeFields(fields),
	}
	if err != nil {
		item.Error = truncate(err.Error(), 1000)
	}
	data, marshalErr := json.Marshal(item)
	if marshalErr != nil {
		data = []byte(`{"level":"error","component":"applog","action":"marshal_failed"}`)
	}
	data = append(data, '\n')

	mu.Lock()
	defer mu.Unlock()
	_, _ = writer.Write(data)
}

func levelFor(err error) string {
	if err != nil {
		return "error"
	}
	return "info"
}

func merge(left, right map[string]any) map[string]any {
	if len(left) == 0 && len(right) == 0 {
		return nil
	}
	out := make(map[string]any, len(left)+len(right))
	for key, value := range left {
		out[key] = value
	}
	for key, value := range right {
		out[key] = value
	}
	return out
}

func sanitizeFields(fields map[string]any) map[string]any {
	if len(fields) == 0 {
		return nil
	}
	out := make(map[string]any, len(fields))
	for key, value := range fields {
		out[key] = sanitizeValue(key, value)
	}
	return out
}

func sanitizeValue(key string, value any) any {
	if isSensitiveKey(key) {
		return "[redacted]"
	}
	switch typed := value.(type) {
	case string:
		if strings.EqualFold(key, "url") || strings.HasSuffix(strings.ToLower(key), "_url") {
			return safeURL(typed)
		}
		return truncate(typed, 2000)
	case map[string]string:
		next := make(map[string]any, len(typed))
		for k, v := range typed {
			next[k] = sanitizeValue(k, v)
		}
		return next
	case map[string]any:
		return sanitizeFields(typed)
	default:
		return value
	}
}

func isSensitiveKey(key string) bool {
	key = strings.ToLower(key)
	sensitive := []string{"cookie", "token", "authorization", "password", "secret", "seckey", "randsk", "sign", "vcode", "bduss", "stoken", "ptoken", "__pus", "__puus"}
	for _, item := range sensitive {
		if strings.Contains(key, item) {
			return true
		}
	}
	return false
}

func safeURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return truncate(raw, 2000)
	}
	query := parsed.Query()
	for key, values := range query {
		if isSensitiveQueryKey(key) {
			for i := range values {
				values[i] = "[redacted]"
			}
			query[key] = values
		}
	}
	parsed.RawQuery = query.Encode()
	parsed.Fragment = ""
	return truncate(parsed.String(), 2000)
}

func isSensitiveQueryKey(key string) bool {
	key = strings.TrimSpace(strings.ToLower(key))
	if key == "" {
		return false
	}
	if isSensitiveKey(key) {
		return true
	}
	sensitive := []string{"auth", "pwd", "passcode", "access_key", "accesskey", "access_token", "refresh_token", "session", "ticket", "signature", "sig"}
	for _, item := range sensitive {
		if strings.Contains(key, item) {
			return true
		}
	}
	return false
}

func truncate(value string, max int) string {
	if max <= 0 || len(value) <= max {
		return value
	}
	return value[:max] + "..."
}
