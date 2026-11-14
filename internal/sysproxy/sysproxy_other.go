//go:build !windows

package sysproxy

func supported() bool { return false }

func current() State { return State{Supported: false} }

func enable(addr string) error { return ErrUnsupported }

func disable() error { return ErrUnsupported }
