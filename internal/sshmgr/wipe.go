package sshmgr

// ClearHostSecret forgets the stored password of one host while keeping the
// rest of the record.
func (s *Store) ClearHostSecret(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.hosts {
		if s.hosts[i].ID == id {
			if s.hosts[i].Password == "" {
				return nil
			}
			s.hosts[i].Password = ""
			return s.persistHosts()
		}
	}
	return nil
}

// WipeSecrets forgets every stored host password and deletes all private key
// material. Used when the account password changes, because the old ciphertext
// can never be opened again.
func (s *Store) WipeSecrets() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := 0
	for i := range s.hosts {
		if s.hosts[i].Password != "" {
			s.hosts[i].Password = ""
			changed++
		}
	}
	if changed > 0 {
		if err := s.persistHosts(); err != nil {
			return changed, err
		}
	}
	if len(s.keys) > 0 {
		changed += len(s.keys)
		s.keys = nil
		if err := s.persistKeys(); err != nil {
			return changed, err
		}
	}
	return changed, nil
}
