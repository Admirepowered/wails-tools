//go:build windows

package sysproxy

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

const (
	internetSettingsKey           = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`
	internetOptionRefresh         = 37
	internetOptionSettingsChanged = 39
)

var (
	wininet               = syscall.NewLazyDLL("wininet.dll")
	procInternetSetOption = wininet.NewProc("InternetSetOptionW")
)

func supported() bool { return true }

func current() State {
	st := State{Supported: true}
	key, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsKey, registry.QUERY_VALUE)
	if err != nil {
		return st
	}
	defer key.Close()
	if enable, _, err := key.GetIntegerValue("ProxyEnable"); err == nil {
		st.Enabled = enable != 0
	}
	if server, _, err := key.GetStringValue("ProxyServer"); err == nil {
		st.Server = server
	}
	return st
}

func enable(addr string) error {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return errors.New("empty proxy address")
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsKey, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return fmt.Errorf("open internet settings: %w", err)
	}
	defer key.Close()
	// Set both http and https so browsers and CLI tools pick it up.
	server := fmt.Sprintf("http=%s;https=%s", addr, addr)
	if err := key.SetStringValue("ProxyServer", server); err != nil {
		return err
	}
	if err := key.SetDWordValue("ProxyEnable", 1); err != nil {
		return err
	}
	// Bypass loopback so the proxy itself and local services keep working.
	_ = key.SetStringValue("ProxyOverride", "localhost;127.*;10.*;172.16.*;172.17.*;172.18.*;172.19.*;172.20.*;172.21.*;172.22.*;172.23.*;172.24.*;172.25.*;172.26.*;172.27.*;172.28.*;172.29.*;172.30.*;172.31.*;192.168.*;<local>")
	notifyChange()
	return nil
}

func disable() error {
	key, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsKey, registry.SET_VALUE)
	if err != nil {
		return nil
	}
	defer key.Close()
	if err := key.SetDWordValue("ProxyEnable", 0); err != nil {
		return err
	}
	notifyChange()
	return nil
}

// notifyChange tells WinINet to re-read the proxy configuration.
func notifyChange() {
	_, _, _ = procInternetSetOption.Call(0, internetOptionSettingsChanged, 0, 0)
	_, _, _ = procInternetSetOption.Call(0, internetOptionRefresh, 0, 0)
	_ = unsafe.Pointer(nil)
}
