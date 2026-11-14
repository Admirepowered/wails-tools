package sshmgr

// ExportHosts returns every host with its password decrypted, ready to be
// packed into an end-to-end encrypted sync bundle.
func (s *Store) ExportHosts() []Host {
	hosts := s.ListHosts()
	out := make([]Host, 0, len(hosts))
	for _, h := range hosts {
		if h.Password != "" {
			if plain, err := s.secrets.Reveal(h.Password); err == nil {
				h.Password = plain
			} else {
				h.Password = ""
			}
		}
		out = append(out, h)
	}
	return out
}

// ExportKeys returns every key with private material and passphrase decrypted.
func (s *Store) ExportKeys() []Key {
	keys := s.ListKeys()
	out := make([]Key, 0, len(keys))
	for _, k := range keys {
		priv, pass, err := s.keyMaterial(k.ID)
		if err != nil {
			continue
		}
		k.PrivateKey = priv
		k.Passphrase = pass
		out = append(out, k)
	}
	return out
}

// ApplyHosts imports hosts sealed with the local machine key. Existing records
// only get replaced when the incoming one is newer.
func (s *Store) ApplyHosts(items []Host) (int, error) {
	changed := 0
	for _, item := range items {
		if cur, ok := s.GetHost(item.ID); ok {
			if !newerTimestamp(item.UpdatedAt, cur.UpdatedAt) {
				continue
			}
		}
		if _, err := s.SaveHost(item); err != nil {
			continue
		}
		changed++
	}
	return changed, nil
}

// ApplyKeys imports key material, keeping locally existing keys untouched.
func (s *Store) ApplyKeys(items []Key) (int, error) {
	changed := 0
	for _, item := range items {
		if _, ok := s.GetKey(item.ID); ok {
			// Never overwrite a local private key: it is the only copy the user
			// may have until the next push.
			continue
		}
		if item.PrivateKey == "" {
			continue
		}
		if _, err := s.SaveKey(item); err != nil {
			continue
		}
		changed++
	}
	return changed, nil
}

// newerTimestamp reports whether a is strictly newer than b (RFC3339 strings
// compare correctly when both carry the same layout).
func newerTimestamp(a, b string) bool {
	if a == "" {
		return false
	}
	if b == "" {
		return true
	}
	return a > b
}
