package app

import (
	"context"
	"time"
)

// startKeepalive pings the server, reconnects with backoff after a drop and
// retries automatic login whenever a valid token exists.
func (a *App) startKeepalive() {
	go func() {
		if !a.settings.Get().Server.AutoConnect {
			return
		}
		backoff := 5 * time.Second
		loggedIn := false
		a.onLogin = func() { loggedIn = true }
		a.onLogout = func() { loggedIn = false }
		for {
			time.Sleep(20 * time.Second)
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			if !a.settings.Get().Server.AutoConnect {
				cancel()
				continue
			}
			if !a.link.Available() {
				if _, err := a.link.Connect(ctx); err != nil {
					cancel()
					time.Sleep(backoff)
					if backoff < 60*time.Second {
						backoff *= 2
					}
					continue
				}
				backoff = 5 * time.Second
				loggedIn = false
				a.emit("server:status", a.link.Status())
			}
			if err := a.link.Ping(ctx); err != nil {
				// 连接已失效：关闭并按退避重连。
				_ = a.link.Close()
				loggedIn = false
				a.emit("server:error", "连接已断开，稍后重试")
				cancel()
				time.Sleep(backoff)
				if backoff < 60*time.Second {
					backoff *= 2
				}
				continue
			}
			if !loggedIn && a.link.Token() != "" {
				if u, err := a.link.Me(ctx); err == nil {
					loggedIn = true
					a.sess.loggedIn = true
					a.setSessionUser(u)
					a.refreshDeviceCount()
					a.emit("server:login", map[string]any{"username": u.Username, "role": u.Role, "auto": true})
				}
			} else if loggedIn && a.sess.role == "" && a.link.Token() != "" {
				// 角色未知（升级前登录的会话）：补拉一次用户记录。
				if u, err := a.link.Me(ctx); err == nil {
					a.setSessionUser(u)
				}
			}
			cancel()
		}
	}()
}
