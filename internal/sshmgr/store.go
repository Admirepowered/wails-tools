// Package sshmgr manages SSH hosts, RSA/Ed25519 keys, connection chains
// (jump hosts), server-relayed connections and SFTP file operations.
package sshmgr

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"wailstools/internal/paths"
	"wailstools/internal/secretstore"
)

// Route modes for reaching a host.
const (
	RouteDirect = "direct" // dial the host directly
	RouteJump   = "jump"   // chain through one or more SSH hosts
	RouteRelay  = "relay"  // tunnel through the WailsTools sync server
)

// Auth modes.
const (
	AuthPassword = "password"
	AuthKey      = "key"
	AuthKeyboard = "keyboard-interactive"
	AuthAgent    = "agent"
	AuthNone     = "none"
)

// Host is a saved SSH target.
type Host struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Address  string   `json:"address"` // host or IP
	Port     int      `json:"port"`
	User     string   `json:"user"`
	AuthMode string   `json:"authMode"`
	KeyID    string   `json:"keyId,omitempty"`
	Password string   `json:"password,omitempty"` // sealed
	Route    string   `json:"route"`
	JumpIDs  []string `json:"jumpIds,omitempty"`
	// RelayTarget optionally overrides the address dialled on the server side.
	RelayTarget string   `json:"relayTarget,omitempty"`
	Group       string   `json:"group,omitempty"`
	Color       string   `json:"color,omitempty"`
	Notes       string   `json:"notes,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	// HostKeyFingerprint pins the server key (SHA256:... form).
	HostKeyFingerprint string `json:"hostKeyFingerprint,omitempty"`
	LastUsed           string `json:"lastUsed,omitempty"`
	LastTransport      string `json:"lastTransport,omitempty"`
	CreatedAt          string `json:"createdAt"`
	UpdatedAt          string `json:"updatedAt"`
}

// Addr returns host:port.
func (h Host) Addr() string {
	port := h.Port
	if port <= 0 {
		port = 22
	}
	if strings.Contains(h.Address, ":") && !strings.HasPrefix(h.Address, "[") {
		// already host:port
		return h.Address
	}
	return joinHostPort(h.Address, port)
}

func joinHostPort(host string, port int) string {
	if port <= 0 {
		port = 22
	}
	if strings.HasPrefix(host, "[") {
		return host + ":" + itoa(port)
	}
	if strings.Contains(host, ":") {
		return "[" + host + "]:" + itoa(port)
	}
	return host + ":" + itoa(port)
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [12]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// Key is a managed SSH key pair.
type Key struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"` // rsa | ed25519 | ecdsa
	Bits        int    `json:"bits,omitempty"`
	Comment     string `json:"comment,omitempty"`
	PublicKey   string `json:"publicKey"`            // authorized_keys line
	PrivateKey  string `json:"privateKey"`           // sealed PEM/OpenSSH
	Fingerprint string `json:"fingerprint"`          // SHA256:...
	Passphrase  string `json:"passphrase,omitempty"` // sealed, optional
	CreatedAt   string `json:"createdAt"`
}

// Store persists hosts and keys as JSON documents.
type Store struct {
	mu       sync.Mutex
	dir      string
	hosts    []Host
	keys     []Key
	knownMap map[string]string // "host:port" -> fingerprint
	secrets  *secretstore.Vault
}

// Open loads (creating if needed) the store in dir.
func Open(dir string) (*Store, error) {
	if dir == "" {
		d, err := paths.Dir("ssh")
		if err != nil {
			return nil, err
		}
		dir = d
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	vault, err := secretstore.Shared(filepath.Dir(dir))
	if err != nil {
		return nil, err
	}
	s := &Store{dir: dir, secrets: vault, knownMap: map[string]string{}}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) hostsPath() string      { return filepath.Join(s.dir, "hosts.json") }
func (s *Store) keysPath() string       { return filepath.Join(s.dir, "keys.json") }
func (s *Store) knownHostsPath() string { return filepath.Join(s.dir, "known_hosts.json") }

func (s *Store) load() error {
	if data, err := os.ReadFile(s.hostsPath()); err == nil {
		if err := json.Unmarshal(data, &s.hosts); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if data, err := os.ReadFile(s.keysPath()); err == nil {
		if err := json.Unmarshal(data, &s.keys); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if data, err := os.ReadFile(s.knownHostsPath()); err == nil {
		_ = json.Unmarshal(data, &s.knownMap)
	}
	if s.knownMap == nil {
		s.knownMap = map[string]string{}
	}
	return nil
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Store) persistHosts() error { return writeJSON(s.hostsPath(), s.hosts) }
func (s *Store) persistKeys() error  { return writeJSON(s.keysPath(), s.keys) }
func (s *Store) persistKnown() error { return writeJSON(s.knownHostsPath(), s.knownMap) }

// ListHosts returns hosts sorted by name.
func (s *Store) ListHosts() []Host {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]Host(nil), s.hosts...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// GetHost returns a host by id.
func (s *Store) GetHost(id string) (Host, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, h := range s.hosts {
		if h.ID == id {
			return h, true
		}
	}
	return Host{}, false
}

// SaveHost inserts or updates a host, sealing its password.
func (s *Store) SaveHost(h Host) (Host, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339)
	if h.ID == "" {
		h.ID = newID("host")
		h.CreatedAt = now
	}
	h.UpdatedAt = now
	if h.CreatedAt == "" {
		h.CreatedAt = now
	}
	if h.Port <= 0 {
		h.Port = 22
	}
	if h.AuthMode == "" {
		h.AuthMode = AuthKey
	}
	if h.Route == "" {
		h.Route = RouteDirect
	}
	if h.Route == RouteJump && len(h.JumpIDs) == 0 {
		return Host{}, errors.New("jump route requires at least one jump host")
	}
	// An empty password from the UI means "keep the stored secret", so editing
	// other fields never silently wipes credentials.
	existingSealed := ""
	for _, existing := range s.hosts {
		if existing.ID == h.ID {
			existingSealed = existing.Password
			break
		}
	}
	if h.Password == "" && existingSealed != "" {
		h.Password = existingSealed
	} else {
		sealed, err := s.secrets.Seal(h.Password)
		if err != nil {
			return Host{}, err
		}
		h.Password = sealed
	}
	replaced := false
	for i := range s.hosts {
		if s.hosts[i].ID == h.ID {
			s.hosts[i] = h
			replaced = true
			break
		}
	}
	if !replaced {
		s.hosts = append(s.hosts, h)
	}
	if err := s.persistHosts(); err != nil {
		return Host{}, err
	}
	return s.redact(h), nil
}

// RedactHost strips the sealed password.
func (s *Store) RedactHost(h Host) Host { return s.redact(h) }

func (s *Store) redact(h Host) Host {
	h.Password = ""
	return h
}

// DeleteHost removes a host.
func (s *Store) DeleteHost(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.hosts[:0]
	found := false
	for _, h := range s.hosts {
		if h.ID == id {
			found = true
			continue
		}
		out = append(out, h)
	}
	if !found {
		return errors.New("host not found")
	}
	s.hosts = out
	return s.persistHosts()
}

// HostPassword returns the decrypted password for a stored host id.
func (s *Store) HostPassword(id string) (string, error) {
	s.mu.Lock()
	var sealed string
	found := false
	for _, h := range s.hosts {
		if h.ID == id {
			sealed = h.Password
			found = true
			break
		}
	}
	s.mu.Unlock()
	if !found {
		return "", errors.New("ssh host not found")
	}
	return s.secrets.Reveal(sealed)
}

// MarkHostUsed records a successful connection.
func (s *Store) MarkHostUsed(id, transport string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.hosts {
		if s.hosts[i].ID == id {
			s.hosts[i].LastUsed = time.Now().UTC().Format(time.RFC3339)
			s.hosts[i].LastTransport = transport
			_ = s.persistHosts()
			return
		}
	}
}

// ListKeys returns public key metadata (private material excluded).
func (s *Store) ListKeys() []Key {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Key, 0, len(s.keys))
	for _, k := range s.keys {
		k.PrivateKey = ""
		k.Passphrase = ""
		out = append(out, k)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// GetKey returns key metadata by id.
func (s *Store) GetKey(id string) (Key, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.keys {
		if k.ID == id {
			return k, true
		}
	}
	return Key{}, false
}

// SaveKey inserts or updates a key (sealing private material).
func (s *Store) SaveKey(k Key) (Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339)
	if k.ID == "" {
		k.ID = newID("key")
	}
	if k.CreatedAt == "" {
		k.CreatedAt = now
	}
	priv, err := s.secrets.Seal(k.PrivateKey)
	if err != nil {
		return Key{}, err
	}
	k.PrivateKey = priv
	if k.Passphrase != "" {
		pp, err := s.secrets.Seal(k.Passphrase)
		if err != nil {
			return Key{}, err
		}
		k.Passphrase = pp
	}
	replaced := false
	for i := range s.keys {
		if s.keys[i].ID == k.ID {
			s.keys[i] = k
			replaced = true
			break
		}
	}
	if !replaced {
		s.keys = append(s.keys, k)
	}
	if err := s.persistKeys(); err != nil {
		return Key{}, err
	}
	out := k
	out.PrivateKey = ""
	out.Passphrase = ""
	return out, nil
}

// DeleteKey removes a key.
func (s *Store) DeleteKey(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.keys[:0]
	found := false
	for _, k := range s.keys {
		if k.ID == id {
			found = true
			continue
		}
		out = append(out, k)
	}
	if !found {
		return errors.New("key not found")
	}
	s.keys = out
	return s.persistKeys()
}

// keyMaterial returns the decrypted private key and passphrase.
func (s *Store) keyMaterial(id string) (string, string, error) {
	s.mu.Lock()
	var raw, pass string
	found := false
	for _, k := range s.keys {
		if k.ID == id {
			raw, pass = k.PrivateKey, k.Passphrase
			found = true
			break
		}
	}
	s.mu.Unlock()
	if !found {
		return "", "", errors.New("ssh key not found")
	}
	priv, err := s.secrets.Reveal(raw)
	if err != nil {
		return "", "", err
	}
	pp, err := s.secrets.Reveal(pass)
	if err != nil {
		return "", "", err
	}
	return priv, pp, nil
}

// KnownHostFingerprint returns the pinned fingerprint for host:port.
func (s *Store) KnownHostFingerprint(hostport string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.knownMap[hostport]
}

// SetKnownHostFingerprint pins a fingerprint.
func (s *Store) SetKnownHostFingerprint(hostport, fp string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.knownMap[hostport] = fp
	return s.persistKnown()
}

// KnownHosts returns all pinned fingerprints.
func (s *Store) KnownHosts() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.knownMap))
	for k, v := range s.knownMap {
		out[k] = v
	}
	return out
}
