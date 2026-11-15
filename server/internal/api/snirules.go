package api

import (
	"encoding/json"

	"wailstools/internal/wire"
	"wailstools/server/internal/store"
)

// ---- server-published default SNI rules ---------------------------------

// sniRulesGet returns the default rule list published by the admin. Clients try
// this first and fall back to their compiled-in defaults.
func (c *callCtx) sniRulesGet() (any, error) {
	if _, err := c.userID(); err != nil {
		return nil, err
	}
	rules, at, by, found := c.api.DB.GetSNIRules()
	return map[string]any{
		"found": found, "rules": rules, "updatedAt": at, "updatedBy": by,
	}, nil
}

// sniRulesPut publishes the default rule list (admin only).
func (c *callCtx) sniRulesPut(body json.RawMessage) (any, error) {
	admin, err := c.requireAdmin()
	if err != nil {
		return nil, err
	}
	var req struct {
		Rules string `json:"rules"`
	}
	if err := wire.Decode(body, &req); err != nil {
		return nil, wire.Errorf("bad_request", "invalid body: %v", err)
	}
	at, err := c.api.DB.PutSNIRules(req.Rules, admin.Username)
	if err != nil {
		return nil, wire.Errorf("internal", "%v", err)
	}
	c.api.DB.Audit(store.AuditEntry{UserID: admin.ID, Action: "sni-rules", Detail: at})
	return map[string]any{"ok": true, "updatedAt": at}, nil
}
