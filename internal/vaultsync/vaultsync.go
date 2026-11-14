// Package vaultsync moves the user's sensitive client state (SSH hosts, SSH
// private keys, FTP profiles and pinned host keys) between devices.
//
// Everything is encrypted on the client with AES-256-GCM under a key derived
// from the *login password* with Argon2id. The server only ever stores the
// ciphertext; the password and the derived key never leave the machine. The
// KDF salt is deterministic (derived from the username), so any device that
// knows the account password derives the same key.
package vaultsync

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"

	"wailstools/internal/ftpx"
	"wailstools/internal/sshmgr"
)

// KindBundle is the single blob kind used for the whole secret bundle.
const KindBundle = "bundle"

const (
	blobPrefix = "wtv1:"
	saltPrefix = "wails-tools/vault/v1/"
	// CurrentBundleVersion is bumped when the plaintext layout changes.
	CurrentBundleVersion = 1
)

// KDFParams are fixed so every client derives the same key.
var KDFParams = struct {
	Memory      uint32
	Iterations  uint32
	Parallelism uint8
	KeyLength   uint32
}{Memory: 32 * 1024, Iterations: 3, Parallelism: 2, KeyLength: 32}

// ErrWrongPassword means the bundle could not be authenticated with this key.
var ErrWrongPassword = errors.New("密钥包无法解密：密码不匹配或密码已更改")

// DeriveKey turns the account password into the bundle key.
func DeriveKey(username, password string) []byte {
	sum := sha256.Sum256([]byte(saltPrefix + strings.ToLower(strings.TrimSpace(username))))
	return argon2.IDKey([]byte(password), sum[:], KDFParams.Iterations, KDFParams.Memory, KDFParams.Parallelism, KDFParams.KeyLength)
}

// Bundle is the plaintext secret document.
type Bundle struct {
	Version    int               `json:"version"`
	Username   string            `json:"username,omitempty"`
	UpdatedAt  string            `json:"updatedAt"`
	Hosts      []sshmgr.Host     `json:"hosts,omitempty"`
	Keys       []sshmgr.Key      `json:"sshKeys,omitempty"`
	FTP        []ftpx.Profile    `json:"ftpProfiles,omitempty"`
	KnownHosts map[string]string `json:"knownHosts,omitempty"`
}

// Empty reports whether the bundle carries nothing worth syncing.
func (b Bundle) Empty() bool {
	return len(b.Hosts) == 0 && len(b.Keys) == 0 && len(b.FTP) == 0 && len(b.KnownHosts) == 0
}

// Counts summarises the bundle for the UI.
func (b Bundle) Counts() map[string]int {
	return map[string]int{"hosts": len(b.Hosts), "keys": len(b.Keys), "ftp": len(b.FTP), "knownHosts": len(b.KnownHosts)}
}

// Seal encrypts the bundle and returns a transportable blob.
func Seal(key []byte, b Bundle) (string, error) {
	if len(key) != 32 {
		return "", errors.New("vaultsync: key must be 32 bytes")
	}
	b.Version = CurrentBundleVersion
	if b.UpdatedAt == "" {
		b.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	plain, err := json.Marshal(b)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ct := gcm.Seal(nil, nonce, plain, []byte(blobPrefix))
	return blobPrefix + base64.RawStdEncoding.EncodeToString(append(nonce, ct...)), nil
}

// Open decrypts a blob produced by Seal.
func Open(key []byte, blob string) (Bundle, error) {
	var out Bundle
	blob = strings.TrimSpace(blob)
	if blob == "" {
		return out, nil
	}
	if !strings.HasPrefix(blob, blobPrefix) {
		return out, errors.New("vaultsync: unknown blob format")
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(blob, blobPrefix))
	if err != nil {
		return out, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return out, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return out, err
	}
	if len(raw) <= gcm.NonceSize() {
		return out, errors.New("vaultsync: blob too short")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], []byte(blobPrefix))
	if err != nil {
		return out, ErrWrongPassword
	}
	if err := json.Unmarshal(plain, &out); err != nil {
		return Bundle{}, err
	}
	return out, nil
}

// Merge combines a local and a remote bundle item by item. The newer record
// (by UpdatedAt, falling back to CreatedAt) wins; items that only exist on one
// side are kept, so nothing is ever lost by syncing.
func Merge(local, remote Bundle) Bundle {
	out := Bundle{Version: CurrentBundleVersion, Username: firstNonEmpty(local.Username, remote.Username)}
	out.UpdatedAt = later(local.UpdatedAt, remote.UpdatedAt)
	if out.UpdatedAt == "" {
		out.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}

	hosts := map[string]sshmgr.Host{}
	for _, h := range local.Hosts {
		hosts[h.ID] = h
	}
	for _, h := range remote.Hosts {
		if cur, ok := hosts[h.ID]; !ok || newer(h.UpdatedAt, h.CreatedAt, cur.UpdatedAt, cur.CreatedAt) {
			hosts[h.ID] = h
		}
	}
	out.Hosts = sortedHosts(hosts)

	keys := map[string]sshmgr.Key{}
	for _, k := range local.Keys {
		keys[k.ID] = k
	}
	for _, k := range remote.Keys {
		cur, ok := keys[k.ID]
		if !ok || (cur.PrivateKey == "" && k.PrivateKey != "") {
			keys[k.ID] = k
		}
	}
	out.Keys = sortedKeys(keys)

	ftp := map[string]ftpx.Profile{}
	for _, p := range local.FTP {
		ftp[p.ID] = p
	}
	for _, p := range remote.FTP {
		if cur, ok := ftp[p.ID]; !ok || newer(p.UpdatedAt, p.CreatedAt, cur.UpdatedAt, cur.CreatedAt) {
			ftp[p.ID] = p
		}
	}
	out.FTP = sortedProfiles(ftp)

	if len(local.KnownHosts) > 0 || len(remote.KnownHosts) > 0 {
		out.KnownHosts = map[string]string{}
		for k, v := range local.KnownHosts {
			out.KnownHosts[k] = v
		}
		for k, v := range remote.KnownHosts {
			if v != "" {
				out.KnownHosts[k] = v
			}
		}
	}
	return out
}

// Changed reports whether applying remote would alter local.
func Changed(local, remote Bundle) bool {
	if len(local.Hosts) != len(remote.Hosts) || len(local.Keys) != len(remote.Keys) || len(local.FTP) != len(remote.FTP) {
		return true
	}
	lj, _ := json.Marshal(local)
	rj, _ := json.Marshal(remote)
	return string(lj) != string(rj)
}

func newer(updated, created, curUpdated, curCreated string) bool {
	a := firstNonEmpty(updated, created)
	b := firstNonEmpty(curUpdated, curCreated)
	if a == "" {
		return false
	}
	if b == "" {
		return true
	}
	return a > b
}

func later(a, b string) string {
	if a > b {
		return a
	}
	return b
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func sortedHosts(m map[string]sshmgr.Host) []sshmgr.Host {
	if len(m) == 0 {
		return nil
	}
	out := make([]sshmgr.Host, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func sortedKeys(m map[string]sshmgr.Key) []sshmgr.Key {
	if len(m) == 0 {
		return nil
	}
	out := make([]sshmgr.Key, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func sortedProfiles(m map[string]ftpx.Profile) []ftpx.Profile {
	if len(m) == 0 {
		return nil
	}
	out := make([]ftpx.Profile, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
