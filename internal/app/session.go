package app

// sessionState remembers the logged-in account for session queries.
type sessionState struct {
	loggedIn    bool
	user        string
	role        string
	deviceCount int
}

// IsAdmin reports whether the current session has the admin role.
func (s *sessionState) IsAdmin() bool { return s.loggedIn && s.role == "admin" }

// ServerSession reports the current login state (survives missed events).
func (a *App) ServerSession() map[string]any {
	return map[string]any{
		"loggedIn":    a.sess.loggedIn,
		"username":    a.sess.user,
		"role":        a.sess.role,
		"isAdmin":     a.sess.IsAdmin(),
		"deviceCount": a.sess.deviceCount,
		"connected":   a.link != nil && a.link.Available(),
	}
}
