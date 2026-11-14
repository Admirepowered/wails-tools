package netdisk

import "errors"

// ExportAll returns every account plus its decrypted cookie, for the encrypted
// secret bundle.
func (s *Store) ExportAll() ([]Account, map[string]string, error) {
	accounts, err := s.LoadAccounts()
	if err != nil {
		return nil, nil, err
	}
	cookies := map[string]string{}
	for _, account := range accounts {
		if account.ID == "" {
			continue
		}
		cookie, err := s.LoadCookie(account.ID)
		if err == nil && cookie != "" {
			cookies[account.ID] = cookie
		}
	}
	return accounts, cookies, nil
}

// ImportAll merges accounts (newer UpdatedAt wins) and cookies (incoming wins
// when it differs) into the local store.
func (s *Store) ImportAll(accounts []Account, cookies map[string]string) (int, error) {
	changed := 0
	existing, err := s.LoadAccounts()
	if err != nil {
		return 0, err
	}
	byID := make(map[string]Account, len(existing)+len(accounts))
	for _, account := range existing {
		if account.ID != "" {
			byID[account.ID] = account
		}
	}
	accountsChanged := false
	for _, account := range accounts {
		if account.ID == "" {
			continue
		}
		if cur, ok := byID[account.ID]; ok && !account.UpdatedAt.After(cur.UpdatedAt) {
			continue
		}
		byID[account.ID] = account
		accountsChanged = true
	}
	if accountsChanged {
		out := make([]Account, 0, len(byID))
		for _, account := range byID {
			out = append(out, account)
		}
		if err := s.SaveAccounts(out); err != nil {
			return changed, err
		}
		changed += len(out)
	}
	for id, cookie := range cookies {
		if id == "" || cookie == "" {
			continue
		}
		if cur, err := s.LoadCookie(id); err == nil && cur == cookie {
			continue
		}
		if err := s.SaveCookie(id, cookie); err != nil {
			return changed, err
		}
		changed++
	}
	return changed, nil
}

var errManagerStore = errors.New("netdisk manager has no persistent store")

// ExportAll returns accounts and decrypted cookies for syncing.
func (m *Manager) ExportAll() ([]Account, map[string]string, error) {
	if m == nil {
		return nil, nil, errManagerStore
	}
	store, ok := m.store.(*Store)
	if !ok || store == nil {
		return nil, nil, errManagerStore
	}
	return store.ExportAll()
}

// ImportAll merges remote accounts/cookies and reloads the in-memory map so the
// UI sees them immediately.
func (m *Manager) ImportAll(accounts []Account, cookies map[string]string) (int, error) {
	if m == nil {
		return 0, errManagerStore
	}
	store, ok := m.store.(*Store)
	if !ok || store == nil {
		return 0, errManagerStore
	}
	n, err := store.ImportAll(accounts, cookies)
	if err != nil {
		return n, err
	}
	if err := m.ReloadFromStore(); err != nil {
		return n, err
	}
	return n, nil
}
