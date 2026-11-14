package app

// sessionUser remembers the logged-in username for session queries.
type sessionState struct {
	loggedIn bool
	user     string
}

// ServerSession reports the current login state (survives missed events).
func (a *App) ServerSession() map[string]any {
	return map[string]any{
		"loggedIn":  a.sess.loggedIn,
		"username":  a.sess.user,
		"connected": a.link != nil && a.link.Available(),
	}
}
