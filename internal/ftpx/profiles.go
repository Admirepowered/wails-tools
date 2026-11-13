package ftpx

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"wailstools/internal/secretstore"
)

// Profile is a saved FTP connection.
type Profile struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Config    Config `json:"config"`
	Password  string `json:"password,omitempty"` // sealed
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
	LastUsed  string `json:"lastUsed,omitempty"`
}

// ProfileStore persists FTP profiles.
type ProfileStore struct {
	mu      sync.Mutex
	path    string
	items   []Profile
	secrets *secretstore.Vault
}

// OpenProfiles loads the profile file (creating the directory if needed).
func OpenProfiles(path string, vault *secretstore.Vault) (*ProfileStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	s := &ProfileStore{path: path, secrets: vault}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &s.items); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *ProfileStore) persist() error {
	data, err := json.MarshalIndent(s.items, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// List returns profiles without passwords.
func (s *ProfileStore) List() []Profile {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Profile, 0, len(s.items))
	for _, p := range s.items {
		p.Password = ""
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// Save inserts or updates a profile, sealing the password.
func (s *ProfileStore) Save(p Profile) (Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.ID == "" {
		p.ID = "ftp-" + randomSuffix()
	}
	if p.Config.Port <= 0 {
		if p.Config.TLS {
			p.Config.Port = 990
		} else {
			p.Config.Port = 21
		}
	}
	// An empty password means "keep the stored secret" when the profile already
	// exists, so editing a host never wipes its credentials.
	existingSealed := ""
	for _, item := range s.items {
		if item.ID == p.ID {
			existingSealed = item.Password
			break
		}
	}
	if p.Password == "" && existingSealed != "" {
		p.Password = existingSealed
	} else if s.secrets != nil {
		sealed, err := s.secrets.Seal(p.Password)
		if err != nil {
			return Profile{}, err
		}
		p.Password = sealed
	}
	found := false
	for i := range s.items {
		if s.items[i].ID == p.ID {
			if p.CreatedAt == "" {
				p.CreatedAt = s.items[i].CreatedAt
			}
			s.items[i] = p
			found = true
			break
		}
	}
	if !found {
		s.items = append(s.items, p)
	}
	if err := s.persist(); err != nil {
		return Profile{}, err
	}
	out := p
	out.Password = ""
	return out, nil
}

// Delete removes a profile.
func (s *ProfileStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.items[:0]
	found := false
	for _, p := range s.items {
		if p.ID == id {
			found = true
			continue
		}
		out = append(out, p)
	}
	if !found {
		return errors.New("ftp profile not found")
	}
	s.items = out
	return s.persist()
}

// PasswordFor returns the decrypted password for a profile.
func (s *ProfileStore) PasswordFor(id string) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.items {
		if p.ID == id {
			if s.secrets == nil {
				return p.Password, p.Config.User, nil
			}
			plain, err := s.secrets.Reveal(p.Password)
			return plain, p.Config.User, err
		}
	}
	return "", "", errors.New("ftp profile not found")
}

func randomSuffix() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 8)
	f, err := os.Open(os.DevNull)
	if err == nil {
		_, _ = f.Read(b)
		_ = f.Close()
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}
