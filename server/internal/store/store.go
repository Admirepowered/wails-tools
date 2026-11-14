// Package store persists server state (users, tokens, notes and file metadata)
// in an embedded bbolt database.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Buckets.
var (
	bucketUsers     = []byte("users")
	bucketUsernames = []byte("usernames")
	bucketTokens    = []byte("tokens")
	bucketNotes     = []byte("notes")
	bucketFileMeta  = []byte("filemeta")
	bucketAudit     = []byte("audit")
)

// User is a registered account.
type User struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"passwordHash"`
	DisplayName  string `json:"displayName,omitempty"`
	Email        string `json:"email,omitempty"`
	Role         string `json:"role"`
	QuotaBytes   int64  `json:"quotaBytes"`
	UsedBytes    int64  `json:"usedBytes"`
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`
	LastLoginAt  string `json:"lastLoginAt,omitempty"`
	Disabled     bool   `json:"disabled"`
}

// Public returns the user without secrets.
func (u User) Public() User {
	out := u
	out.PasswordHash = ""
	return out
}

// Token is a persisted session token.
type Token struct {
	Hash      string `json:"hash"`
	UserID    string `json:"userId"`
	Label     string `json:"label,omitempty"`
	CreatedAt string `json:"createdAt"`
	ExpiresAt string `json:"expiresAt"`
}

// Note is a synced markdown note.
type Note struct {
	ID      string   `json:"id"`
	UserID  string   `json:"userId"`
	Title   string   `json:"title"`
	Content string   `json:"content"`
	Folder  string   `json:"folder,omitempty"`
	Tags    []string `json:"tags,omitempty"`
	Pinned  bool     `json:"pinned"`
	Deleted bool     `json:"deleted"`
	// UpdatedAt is the client-supplied last-modified time in unix milliseconds.
	UpdatedAt int64  `json:"updatedAt"`
	CreatedAt int64  `json:"createdAt"`
	Revision  int64  `json:"revision"`
	Checksum  string `json:"checksum,omitempty"`
}

// FileMeta describes a stored file.
type FileMeta struct {
	Path      string `json:"path"`
	UserID    string `json:"userId"`
	Size      int64  `json:"size"`
	IsDir     bool   `json:"isDir"`
	UpdatedAt string `json:"updatedAt"`
	CreatedAt string `json:"createdAt"`
	Checksum  string `json:"checksum,omitempty"`
}

// AuditEntry records an administrative event.
type AuditEntry struct {
	At     string `json:"at"`
	UserID string `json:"userId"`
	Action string `json:"action"`
	Detail string `json:"detail,omitempty"`
}

// DB wraps the bolt database.
type DB struct {
	db   *bolt.DB
	data string
}

// Open opens (and initialises) the database under dir.
func Open(dir string) (*DB, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "wails-tools.db")
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, err
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{bucketUsers, bucketUsernames, bucketTokens, bucketNotes, bucketFileMeta, bucketAudit, bucketReleases, bucketSubs} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	dataDir := filepath.Join(dir, "files")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &DB{db: db, data: dataDir}, nil
}

// Close closes the database.
func (d *DB) Close() error { return d.db.Close() }

// DataDir is the per-server blob root.
func (d *DB) DataDir() string { return d.data }

// UserDataDir returns (and creates) the blob directory for a user.
func (d *DB) UserDataDir(userID string) (string, error) {
	if userID == "" || strings.ContainsAny(userID, `/\`) {
		return "", errors.New("invalid user id")
	}
	p := filepath.Join(d.data, userID)
	if err := os.MkdirAll(p, 0o755); err != nil {
		return "", err
	}
	return p, nil
}

func putJSON(b *bolt.Bucket, key string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return b.Put([]byte(key), data)
}

func getJSON(b *bolt.Bucket, key string, v any) error {
	raw := b.Get([]byte(key))
	if raw == nil {
		return os.ErrNotExist
	}
	return json.Unmarshal(raw, v)
}

// ErrExists is returned when a uniqueness constraint is violated.
var ErrExists = errors.New("already exists")

// ErrMissingUser reports an unknown user id.
var ErrMissingUser = errors.New("user not found")

// CreateUser inserts a new user with a unique username.
func (d *DB) CreateUser(u User) (User, error) {
	err := d.db.Update(func(tx *bolt.Tx) error {
		un := tx.Bucket(bucketUsernames)
		nameKey := strings.ToLower(u.Username)
		if un.Get([]byte(nameKey)) != nil {
			return ErrExists
		}
		if err := putJSON(tx.Bucket(bucketUsers), u.ID, u); err != nil {
			return err
		}
		return un.Put([]byte(nameKey), []byte(u.ID))
	})
	if err != nil {
		return User{}, err
	}
	return u, nil
}

// GetUser loads a user by id.
func (d *DB) GetUser(id string) (User, error) {
	var u User
	err := d.db.View(func(tx *bolt.Tx) error {
		return getJSON(tx.Bucket(bucketUsers), id, &u)
	})
	return u, err
}

// GetUserByName loads a user by username.
func (d *DB) GetUserByName(name string) (User, error) {
	var u User
	err := d.db.View(func(tx *bolt.Tx) error {
		id := tx.Bucket(bucketUsernames).Get([]byte(strings.ToLower(name)))
		if id == nil {
			return os.ErrNotExist
		}
		return getJSON(tx.Bucket(bucketUsers), string(id), &u)
	})
	return u, err
}

// UpdateUser rewrites a user record.
func (d *DB) UpdateUser(u User) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		return putJSON(tx.Bucket(bucketUsers), u.ID, u)
	})
}

// DeleteUser removes a user record.
func (d *DB) DeleteUser(id string) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketUsers)
		if b == nil {
			return bolt.ErrBucketNotFound
		}
		if b.Get([]byte(id)) == nil {
			return ErrMissingUser
		}
		return b.Delete([]byte(id))
	})
}

// ListUsers returns all users.
func (d *DB) ListUsers() ([]User, error) {
	var out []User
	err := d.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketUsers).ForEach(func(_, v []byte) error {
			var u User
			if err := json.Unmarshal(v, &u); err != nil {
				return err
			}
			out = append(out, u)
			return nil
		})
	})
	return out, err
}

// CountUsers returns the number of registered accounts.
func (d *DB) CountUsers() (int, error) {
	n := 0
	err := d.db.View(func(tx *bolt.Tx) error {
		n = tx.Bucket(bucketUsers).Stats().KeyN
		return nil
	})
	return n, err
}

// SaveToken persists a session token.
func (d *DB) SaveToken(t Token) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		return putJSON(tx.Bucket(bucketTokens), t.Hash, t)
	})
}

// GetToken returns a token record.
func (d *DB) GetToken(hash string) (Token, error) {
	var t Token
	err := d.db.View(func(tx *bolt.Tx) error {
		return getJSON(tx.Bucket(bucketTokens), hash, &t)
	})
	return t, err
}

// DeleteToken removes a token.
func (d *DB) DeleteToken(hash string) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketTokens).Delete([]byte(hash))
	})
}

// DeleteUserTokens removes every token for a user.
func (d *DB) DeleteUserTokens(userID string) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketTokens)
		var doomed [][]byte
		_ = b.ForEach(func(k, v []byte) error {
			var t Token
			if json.Unmarshal(v, &t) == nil && t.UserID == userID {
				doomed = append(doomed, append([]byte(nil), k...))
			}
			return nil
		})
		for _, k := range doomed {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}

func noteKey(userID, noteID string) string { return userID + "\x00" + noteID }

// PutNote stores a note, bumping its revision.
func (d *DB) PutNote(n Note) (Note, error) {
	err := d.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketNotes)
		var old Note
		if raw := b.Get([]byte(noteKey(n.UserID, n.ID))); raw != nil {
			_ = json.Unmarshal(raw, &old)
		}
		n.Revision = old.Revision + 1
		return putJSON(b, noteKey(n.UserID, n.ID), n)
	})
	return n, err
}

// GetNote loads one note.
func (d *DB) GetNote(userID, noteID string) (Note, error) {
	var n Note
	err := d.db.View(func(tx *bolt.Tx) error {
		return getJSON(tx.Bucket(bucketNotes), noteKey(userID, noteID), &n)
	})
	return n, err
}

// ListNotes returns all notes for a user (including tombstones) modified after
// sinceMS (0 = all).
func (d *DB) ListNotes(userID string, sinceMS int64) ([]Note, error) {
	var out []Note
	err := d.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketNotes).ForEach(func(k, v []byte) error {
			var n Note
			if err := json.Unmarshal(v, &n); err != nil {
				return nil
			}
			if n.UserID != userID {
				return nil
			}
			if sinceMS > 0 && n.UpdatedAt <= sinceMS {
				return nil
			}
			out = append(out, n)
			return nil
		})
	})
	return out, err
}

// FileMetaKey builds the metadata key.
func FileMetaKey(userID, p string) string { return userID + "\x00" + p }

// PutFileMeta stores file metadata.
func (d *DB) PutFileMeta(m FileMeta) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		return putJSON(tx.Bucket(bucketFileMeta), FileMetaKey(m.UserID, m.Path), m)
	})
}

// DeleteFileMeta removes file metadata.
func (d *DB) DeleteFileMeta(userID, p string) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketFileMeta).Delete([]byte(FileMetaKey(userID, p)))
	})
}

// ListFileMeta lists metadata under prefix (metadata paths are POSIX style).
func (d *DB) ListFileMeta(userID, prefix string) ([]FileMeta, error) {
	var out []FileMeta
	err := d.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketFileMeta).ForEach(func(k, v []byte) error {
			var m FileMeta
			if err := json.Unmarshal(v, &m); err != nil {
				return nil
			}
			if m.UserID != userID {
				return nil
			}
			if prefix != "" && prefix != "/" {
				if m.Path != prefix && !strings.HasPrefix(m.Path, strings.TrimSuffix(prefix, "/")+"/") {
					return nil
				}
			}
			out = append(out, m)
			return nil
		})
	})
	return out, err
}

// Audit appends an audit record.
func (d *DB) Audit(e AuditEntry) {
	if e.At == "" {
		e.At = time.Now().UTC().Format(time.RFC3339)
	}
	_ = d.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketAudit)
		key := fmt.Sprintf("%s|%s", e.At, e.UserID)
		return putJSON(b, key, e)
	})
}

// ListAudit returns the most recent audit entries.
func (d *DB) ListAudit(limit int) ([]AuditEntry, error) {
	var out []AuditEntry
	err := d.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketAudit).ForEach(func(_, v []byte) error {
			var e AuditEntry
			if err := json.Unmarshal(v, &e); err != nil {
				return nil
			}
			out = append(out, e)
			return nil
		})
	})
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, err
}

// RecalcUsage recomputes a user's stored byte count.
func (d *DB) RecalcUsage(userID string) (int64, error) {
	metas, err := d.ListFileMeta(userID, "")
	if err != nil {
		return 0, err
	}
	var total int64
	for _, m := range metas {
		if !m.IsDir {
			total += m.Size
		}
	}
	u, err := d.GetUser(userID)
	if err != nil {
		return 0, err
	}
	u.UsedBytes = total
	u.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return total, d.UpdateUser(u)
}
