package app

import "path/filepath"

// LogPath returns the current session log file path (empty when unavailable).
func (a *App) LogPath() string {
	if a.log == nil {
		return ""
	}
	return a.log.Path()
}

// OpenLogDir opens the log directory in the file manager.
func (a *App) OpenLogDir() error {
	if a.log == nil || a.log.Path() == "" {
		return nil
	}
	return reveal(filepath.Dir(a.log.Path()))
}
