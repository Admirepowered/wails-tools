// Package sysproxy toggles the operating system's HTTP proxy so other
// applications can share the local WailsTools proxy.
package sysproxy

import (
	"errors"
	"runtime"
)

// ErrUnsupported marks platforms without an implementation.
var ErrUnsupported = errors.New("system proxy is not supported on " + runtime.GOOS)

// State describes the OS proxy configuration.
type State struct {
	Enabled bool   `json:"enabled"`
	Server  string `json:"server"`
	// Supported reports whether this platform can toggle the proxy.
	Supported bool `json:"supported"`
}

// Current reads the OS proxy state.
func Current() State {
	return current()
}

// Enable points the OS proxy at addr ("host:port").
func Enable(addr string) error { return enable(addr) }

// Disable turns the OS proxy off.
func Disable() error { return disable() }

// Supported reports whether the platform implements the toggle.
func Supported() bool { return supported() }
