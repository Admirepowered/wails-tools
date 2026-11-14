package api

import (
	"encoding/json"

	"wailstools/internal/wire"
)

// maxVaultBlob caps one encrypted secret bundle (SSH keys + host/FTP lists).
const maxVaultBlob = 4 << 20

// ---- end-to-end encrypted secret bundles ---------------------------------
//
// The server never derives or stores the encryption key: clients encrypt with
// an Argon2id key derived from the login password and upload ciphertext only.

func (c *callCtx) vaultGet(body json.RawMessage) (any, error) {
	uid, err := c.userID()
	if err != nil {
		return nil, err
	}
	var req struct {
		Kind string `json:"kind"`
	}
	if err := wire.Decode(body, &req); err != nil {
		return nil, wire.Errorf("bad_request", "invalid body: %v", err)
	}
	b, ok := c.api.DB.GetVault(uid, req.Kind)
	if !ok {
		return map[string]any{"found": false, "kind": req.Kind}, nil
	}
	return map[string]any{
		"found": true, "kind": b.Kind, "data": b.Data,
		"updatedAt": b.UpdatedAt, "size": b.Size,
	}, nil
}

func (c *callCtx) vaultPut(body json.RawMessage) (any, error) {
	uid, err := c.userID()
	if err != nil {
		return nil, err
	}
	var req struct {
		Kind string `json:"kind"`
		Data string `json:"data"`
	}
	if err := wire.Decode(body, &req); err != nil {
		return nil, wire.Errorf("bad_request", "invalid body: %v", err)
	}
	if req.Kind == "" {
		return nil, wire.Errorf("bad_request", "kind is required")
	}
	if len(req.Data) > maxVaultBlob {
		return nil, wire.Errorf("too_large", "secret bundle exceeds %d bytes", maxVaultBlob)
	}
	b, err := c.api.DB.PutVault(uid, req.Kind, req.Data)
	if err != nil {
		return nil, wire.Errorf("internal", "%v", err)
	}
	return map[string]any{"ok": true, "kind": b.Kind, "updatedAt": b.UpdatedAt, "size": b.Size}, nil
}

func (c *callCtx) vaultList() (any, error) {
	uid, err := c.userID()
	if err != nil {
		return nil, err
	}
	return map[string]any{"blobs": c.api.DB.ListVault(uid)}, nil
}

func (c *callCtx) vaultDelete(body json.RawMessage) (any, error) {
	uid, err := c.userID()
	if err != nil {
		return nil, err
	}
	var req struct {
		Kind string `json:"kind"`
	}
	if err := wire.Decode(body, &req); err != nil {
		return nil, wire.Errorf("bad_request", "invalid body: %v", err)
	}
	if err := c.api.DB.DeleteVault(uid, req.Kind); err != nil {
		return nil, wire.Errorf("internal", "%v", err)
	}
	return map[string]any{"ok": true}, nil
}

func (c *callCtx) vaultWipe() (any, error) {
	uid, err := c.userID()
	if err != nil {
		return nil, err
	}
	n, err := c.api.DB.WipeVault(uid)
	if err != nil {
		return nil, wire.Errorf("internal", "%v", err)
	}
	return map[string]any{"ok": true, "removed": n}, nil
}
