package netdisk

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"wailstools/internal/localvault"

	"github.com/zalando/go-keyring"
)

const legacyKeyringService = "WailsTools Netdisk"

type Store struct {
	accountsPath string
	cookiesPath  string
	cookiesMu    sync.Mutex
}

func NewStore(accountsPath, cookiesPath string) *Store {
	return &Store{
		accountsPath: accountsPath,
		cookiesPath:  cookiesPath,
	}
}

func (s *Store) LoadAccounts() ([]Account, error) {
	if s == nil || s.accountsPath == "" {
		return nil, nil
	}
	data, err := os.ReadFile(s.accountsPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, nil
	}
	var accounts []Account
	if err := json.Unmarshal(data, &accounts); err != nil {
		return nil, err
	}
	return accounts, nil
}

func (s *Store) SaveAccounts(accounts []Account) error {
	if s == nil || s.accountsPath == "" {
		return errors.New("netdisk account store is not configured")
	}
	data, err := json.MarshalIndent(accounts, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.accountsPath), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(s.accountsPath, data, 0o600)
}

func (s *Store) LoadCookie(accountID string) (string, error) {
	if s == nil {
		return "", nil
	}
	s.cookiesMu.Lock()
	defer s.cookiesMu.Unlock()
	return s.loadCookieLocked(accountID)
}

func (s *Store) loadCookieLocked(accountID string) (string, error) {
	cookies, err := s.loadCookiesLocked()
	if err != nil {
		return "", err
	}
	if cookies != nil && cookies[accountID] != "" {
		return cookies[accountID], nil
	}
	return s.loadLegacyCookieLocked(accountID)
}

func (s *Store) SaveCookie(accountID, cookie string) error {
	if s == nil || s.cookiesPath == "" {
		return errors.New("netdisk cookie store is not configured")
	}
	s.cookiesMu.Lock()
	defer s.cookiesMu.Unlock()
	return s.saveCookieLocked(accountID, cookie)
}

func (s *Store) saveCookieLocked(accountID, cookie string) error {
	cookies, err := s.loadCookiesLocked()
	if err != nil {
		return err
	}
	if cookies == nil {
		cookies = make(map[string]string)
	}
	cookies[accountID] = cookie
	return s.saveCookiesLocked(cookies)
}

func (s *Store) DeleteCookie(accountID string) error {
	if s == nil {
		return nil
	}
	s.cookiesMu.Lock()
	defer s.cookiesMu.Unlock()
	cookies, err := s.loadCookiesLocked()
	if err != nil {
		return err
	}
	if cookies == nil {
		return nil
	}
	delete(cookies, accountID)
	if err := s.saveCookiesLocked(cookies); err != nil {
		return err
	}
	legacyErr := keyring.Delete(legacyKeyringService, accountID)
	if errors.Is(legacyErr, keyring.ErrNotFound) {
		return nil
	}
	return legacyErr
}

// loadCookiesLocked and saveCookiesLocked require cookiesMu to be held. Keeping
// the legacy migration on these helpers avoids recursively locking LoadCookie.
func (s *Store) loadCookiesLocked() (map[string]string, error) {
	if s == nil || s.cookiesPath == "" {
		return nil, nil
	}
	data, err := os.ReadFile(s.cookiesPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, nil
	}
	plain, encrypted, err := localvault.Open(data)
	if err != nil {
		return nil, err
	}
	var cookies map[string]string
	if err := json.Unmarshal(plain, &cookies); err != nil {
		return nil, err
	}
	if !encrypted {
		if err := s.saveCookiesLocked(cookies); err != nil {
			return nil, err
		}
	}
	return cookies, nil
}

func (s *Store) saveCookiesLocked(cookies map[string]string) error {
	if s == nil || s.cookiesPath == "" {
		return errors.New("netdisk cookie store is not configured")
	}
	data, err := json.MarshalIndent(cookies, "", "  ")
	if err != nil {
		return err
	}
	data, err = localvault.Seal(data)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.cookiesPath), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(s.cookiesPath, data, 0o600)
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tempPath)
		}
	}()
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Chmod(perm); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func (s *Store) loadLegacyCookieLocked(accountID string) (string, error) {
	value, err := keyring.Get(legacyKeyringService, accountID)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if err := s.saveCookieLocked(accountID, value); err != nil {
		return "", err
	}
	_ = keyring.Delete(legacyKeyringService, accountID)
	return value, nil
}
