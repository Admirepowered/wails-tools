//go:build !windows

package singleinst

func acquire(name string) (func(), error) { return func() {}, nil }
