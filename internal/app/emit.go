package app

import (
	"encoding/json"
	"fmt"
)

// logEvent records emitted events for diagnostics.
func (a *App) logEvent(name string, payload any) {
	if a.log == nil {
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		a.log.Printf("event %s", name)
		return
	}
	text := string(data)
	if len(text) > 600 {
		text = text[:600] + "…"
	}
	a.log.Printf("event %s %s", name, text)
}

var _ = fmt.Sprintf
