package localvault

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/zalando/go-keyring"
)

const (
	marker         = "wailstools-local-vault:v1:"
	keyringService = "WailsTools Local Vault"
	keyringUser    = "file-encryption-key-v1"
)

var (
	keyMu     sync.Mutex
	cachedKey []byte
)

func IsSealed(data []byte) bool {
	data = trim(data)
	return bytes.HasPrefix(data, []byte(marker))
}

func Seal(data []byte) ([]byte, error) {
	key, err := masterKey()
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	sealed := gcm.Seal(nil, nonce, data, []byte(marker))
	payload := append(nonce, sealed...)
	out := marker + base64.RawStdEncoding.EncodeToString(payload)
	return []byte(out), nil
}

func Open(data []byte) ([]byte, bool, error) {
	data = trim(data)
	if len(data) == 0 {
		return nil, false, nil
	}
	if !bytes.HasPrefix(data, []byte(marker)) {
		return data, false, nil
	}
	encoded := strings.TrimSpace(string(bytes.TrimPrefix(data, []byte(marker))))
	payload, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, true, err
	}
	key, err := masterKey()
	if err != nil {
		return nil, true, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, true, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, true, err
	}
	if len(payload) <= gcm.NonceSize() {
		return nil, true, errors.New("local vault payload is too short")
	}
	nonce := payload[:gcm.NonceSize()]
	ciphertext := payload[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, []byte(marker))
	return plain, true, err
}

func masterKey() ([]byte, error) {
	keyMu.Lock()
	defer keyMu.Unlock()
	if len(cachedKey) == 32 {
		return append([]byte(nil), cachedKey...), nil
	}
	if isTestBinary() {
		sum := sha256.Sum256([]byte("wailstools-local-vault-test-key"))
		cachedKey = sum[:]
		return append([]byte(nil), cachedKey...), nil
	}
	raw, err := keyring.Get(keyringService, keyringUser)
	if errors.Is(err, keyring.ErrNotFound) {
		key := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, key); err != nil {
			return nil, err
		}
		raw = base64.RawStdEncoding.EncodeToString(key)
		if err := keyring.Set(keyringService, keyringUser, raw); err != nil {
			return nil, err
		}
		cachedKey = key
		return append([]byte(nil), cachedKey...), nil
	}
	if err != nil {
		return nil, err
	}
	key, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, errors.New("local vault key has invalid length")
	}
	cachedKey = key
	return append([]byte(nil), cachedKey...), nil
}

func trim(data []byte) []byte {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	return bytes.TrimSpace(data)
}

func isTestBinary() bool {
	name := strings.ToLower(os.Args[0])
	return strings.HasSuffix(name, ".test") || strings.HasSuffix(name, ".test.exe")
}
