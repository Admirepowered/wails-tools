// Package autostart toggles launching WailsTools when the user signs in.
package autostart

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// AppName is the registry / desktop entry name.
const AppName = "WailsTools"

// Options describes how the entry should be created.
type Options struct {
	// ExePath overrides the current executable.
	ExePath string
	// Args are appended to the launch command.
	Args []string
}

func resolveExe(opts Options) (string, error) {
	if strings.TrimSpace(opts.ExePath) != "" {
		return opts.ExePath, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}

func commandLine(exe string, args []string) string {
	quoted := `"` + exe + `"`
	if len(args) == 0 {
		return quoted
	}
	return quoted + " " + strings.Join(args, " ")
}

// UnsupportedError marks platforms without an implementation.
var UnsupportedError = errors.New("autostart is not supported on " + runtime.GOOS)

// Status reports the current state.
type Status struct {
	Enabled   bool   `json:"enabled"`
	Command   string `json:"command,omitempty"`
	Location  string `json:"location,omitempty"`
	Supported bool   `json:"supported"`
	Error     string `json:"error,omitempty"`
}

// Info returns the current registration.
func Info() Status {
	return info()
}

// Enable registers the app for login start.
func Enable(opts Options) (Status, error) {
	exe, err := resolveExe(opts)
	if err != nil {
		return Info(), err
	}
	if _, err := os.Stat(exe); err != nil {
		return Info(), fmt.Errorf("executable not found: %w", err)
	}
	if err := enable(exe, opts.Args); err != nil {
		return Info(), err
	}
	return Info(), nil
}

// Disable removes the login-start registration.
func Disable() (Status, error) {
	if err := disable(); err != nil {
		return Info(), err
	}
	return Info(), nil
}
