// Package paths resolves the on-disk locations used by the client.
package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

var (
	once     sync.Once
	rootDir  string
	rootErr  error
	override string
)

// SetRoot overrides the application data root. It must be called before Root.
func SetRoot(dir string) { override = dir }

// Root returns the per-user data directory, creating it on demand.
func Root() (string, error) {
	once.Do(func() {
		if override != "" {
			rootDir = override
		} else {
			rootDir = defaultRoot()
		}
		if err := os.MkdirAll(rootDir, 0o755); err != nil {
			rootErr = err
			return
		}
	})
	return rootDir, rootErr
}

func defaultRoot() string {
	if v := os.Getenv("WAILSTOOLS_HOME"); v != "" {
		return v
	}
	switch runtime.GOOS {
	case "windows":
		if v := os.Getenv("LOCALAPPDATA"); v != "" {
			return filepath.Join(v, "WailsTools")
		}
		if v := os.Getenv("APPDATA"); v != "" {
			return filepath.Join(v, "WailsTools")
		}
	case "darwin":
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "Library", "Application Support", "WailsTools")
		}
	}
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return filepath.Join(v, "wails-tools")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "share", "wails-tools")
	}
	return filepath.Join(os.TempDir(), "wails-tools")
}

// Join resolves a path relative to the data root, creating parent directories.
func Join(parts ...string) (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	p := filepath.Join(append([]string{root}, parts...)...)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	return p, nil
}

// Dir resolves (and creates) a directory under the data root.
func Dir(parts ...string) (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	p := filepath.Join(append([]string{root}, parts...)...)
	if err := os.MkdirAll(p, 0o755); err != nil {
		return "", err
	}
	return p, nil
}

// Downloads returns the default download directory.
func Downloads() string {
	root, err := Root()
	if err != nil {
		return os.TempDir()
	}
	p := filepath.Join(root, "downloads")
	_ = os.MkdirAll(p, 0o755)
	return p
}
