// Package singleinst prevents multiple app instances from running at once.
package singleinst

import "errors"

// ErrAlreadyRunning reports another instance owns the lock.
var ErrAlreadyRunning = errors.New("another instance is already running")

// Acquire takes the single-instance lock. It returns a release function.
func Acquire(name string) (func(), error) {
	return acquire(name)
}
