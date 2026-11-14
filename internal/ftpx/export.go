package ftpx

// Export returns every profile with its password decrypted.
func (s *ProfileStore) Export() []Profile {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Profile, 0, len(s.items))
	for _, p := range s.items {
		if p.Password != "" && s.secrets != nil {
			if plain, err := s.secrets.Reveal(p.Password); err == nil {
				p.Password = plain
			} else {
				p.Password = ""
			}
		}
		out = append(out, p)
	}
	return out
}

// Apply imports profiles, replacing existing records only when the incoming
// one is newer.
func (s *ProfileStore) Apply(items []Profile) (int, error) {
	changed := 0
	for _, item := range items {
		if item.ID == "" {
			continue
		}
		if cur, ok := s.get(item.ID); ok {
			if !newerTimestamp(item.UpdatedAt, cur.UpdatedAt) {
				continue
			}
		}
		if _, err := s.Save(item); err != nil {
			continue
		}
		changed++
	}
	return changed, nil
}

func (s *ProfileStore) get(id string) (Profile, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.items {
		if p.ID == id {
			return p, true
		}
	}
	return Profile{}, false
}

func newerTimestamp(a, b string) bool {
	if a == "" {
		return false
	}
	if b == "" {
		return true
	}
	return a > b
}

// WipeSecrets forgets every stored profile password.
func (s *ProfileStore) WipeSecrets() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := 0
	for i := range s.items {
		if s.items[i].Password != "" {
			s.items[i].Password = ""
			changed++
		}
	}
	if changed > 0 {
		if err := s.persist(); err != nil {
			return changed, err
		}
	}
	return changed, nil
}
