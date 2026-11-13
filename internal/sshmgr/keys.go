package sshmgr

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// GenerateOptions drives key generation.
type GenerateOptions struct {
	Name       string `json:"name"`
	Type       string `json:"type"` // rsa | ed25519 | ecdsa
	Bits       int    `json:"bits"` // rsa: 2048/3072/4096, ecdsa: 256/384/521
	Comment    string `json:"comment"`
	Passphrase string `json:"passphrase"`
}

// GenerateKey creates a new key pair and stores it.
func (s *Store) GenerateKey(opts GenerateOptions) (Key, error) {
	keyType := strings.ToLower(strings.TrimSpace(opts.Type))
	if keyType == "" {
		keyType = "rsa"
	}
	if opts.Name == "" {
		opts.Name = keyType + "-" + time.Now().Format("20060102-150405")
	}
	var priv crypto.PrivateKey
	var bits int
	switch keyType {
	case "rsa":
		bits = opts.Bits
		if bits == 0 {
			bits = 3072
		}
		if bits < 2048 {
			return Key{}, fmt.Errorf("rsa key must be at least 2048 bits, got %d", bits)
		}
		if bits > 16384 {
			return Key{}, errors.New("rsa key size too large")
		}
		k, err := rsa.GenerateKey(rand.Reader, bits)
		if err != nil {
			return Key{}, err
		}
		priv = k
	case "ed25519":
		_, k, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return Key{}, err
		}
		priv = k
		bits = 256
	case "ecdsa":
		bits = opts.Bits
		var curve elliptic.Curve
		switch bits {
		case 0, 256:
			curve, bits = elliptic.P256(), 256
		case 384:
			curve = elliptic.P384()
		case 521:
			curve = elliptic.P521()
		default:
			return Key{}, fmt.Errorf("unsupported ecdsa size %d", bits)
		}
		k, err := ecdsa.GenerateKey(curve, rand.Reader)
		if err != nil {
			return Key{}, err
		}
		priv = k
	default:
		return Key{}, fmt.Errorf("unsupported key type %q", opts.Type)
	}

	comment := opts.Comment
	if comment == "" {
		comment = opts.Name + "@wailstools"
	}
	var block *pem.Block
	var err error
	if opts.Passphrase != "" {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, comment, []byte(opts.Passphrase))
	} else {
		block, err = ssh.MarshalPrivateKey(priv, comment)
	}
	if err != nil {
		return Key{}, err
	}
	key := Key{
		Name:       opts.Name,
		Type:       keyType,
		Bits:       bits,
		Comment:    comment,
		PrivateKey: string(pem.EncodeToMemory(block)),
		Passphrase: opts.Passphrase,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	pub, fp, err := publicFromPrivate(key.PrivateKey, opts.Passphrase, comment)
	if err != nil {
		return Key{}, err
	}
	key.PublicKey = pub
	key.Fingerprint = fp
	return s.SaveKey(key)
}

// ImportOptions describes an existing private key upload.
type ImportOptions struct {
	Name       string `json:"name"`
	PrivateKey string `json:"privateKey"`
	Passphrase string `json:"passphrase"`
	Comment    string `json:"comment"`
}

// ImportKey stores an existing private key.
func (s *Store) ImportKey(opts ImportOptions) (Key, error) {
	raw := strings.TrimSpace(opts.PrivateKey)
	if raw == "" {
		return Key{}, errors.New("private key is empty")
	}
	pub, fp, err := publicFromPrivate(raw, opts.Passphrase, opts.Comment)
	if err != nil {
		return Key{}, err
	}
	keyType, bits := classifySigner(raw, opts.Passphrase)
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		name = keyType + "-imported-" + time.Now().Format("20060102-150405")
	}
	key := Key{
		Name:        name,
		Type:        keyType,
		Bits:        bits,
		Comment:     opts.Comment,
		PublicKey:   pub,
		PrivateKey:  raw,
		Passphrase:  opts.Passphrase,
		Fingerprint: fp,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	return s.SaveKey(key)
}

func classifySigner(raw, passphrase string) (string, int) {
	var signer ssh.Signer
	var err error
	if passphrase != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(raw), []byte(passphrase))
	} else {
		signer, err = ssh.ParsePrivateKey([]byte(raw))
	}
	if err != nil || signer == nil {
		return "unknown", 0
	}
	switch k := signer.PublicKey().(type) {
	case ssh.CryptoPublicKey:
		switch pk := k.CryptoPublicKey().(type) {
		case *rsa.PublicKey:
			return "rsa", pk.N.BitLen()
		case ed25519.PublicKey:
			return "ed25519", 256
		case *ecdsa.PublicKey:
			return "ecdsa", pk.Curve.Params().BitSize
		}
	}
	return "unknown", 0
}

func publicFromPrivate(raw, passphrase, comment string) (string, string, error) {
	var signer ssh.Signer
	var err error
	if passphrase != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(raw), []byte(passphrase))
	} else {
		signer, err = ssh.ParsePrivateKey([]byte(raw))
	}
	if err != nil {
		return "", "", fmt.Errorf("parse private key: %w", err)
	}
	pub := signer.PublicKey()
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	if comment != "" {
		line = line + " " + comment
	}
	return line, ssh.FingerprintSHA256(pub), nil
}

// PublicKeyFor returns the authorized_keys line for a stored key.
func (s *Store) PublicKeyFor(id string) (string, error) {
	k, ok := s.GetKey(id)
	if !ok {
		return "", errors.New("ssh key not found")
	}
	return k.PublicKey, nil
}

// ExportPrivateKey returns the decrypted private key PEM (for download).
func (s *Store) ExportPrivateKey(id string) (string, error) {
	priv, _, err := s.keyMaterial(id)
	return priv, err
}

// signer builds an ssh.Signer for a stored key, prompting for a passphrase
// override when the stored one is missing.
func (s *Store) signer(id, passphraseOverride string) (ssh.Signer, error) {
	priv, pass, err := s.keyMaterial(id)
	if err != nil {
		return nil, err
	}
	if passphraseOverride != "" {
		pass = passphraseOverride
	}
	if pass != "" {
		signer, err := ssh.ParsePrivateKeyWithPassphrase([]byte(priv), []byte(pass))
		if err != nil {
			return nil, fmt.Errorf("decrypt key passphrase failed: %w", err)
		}
		return signer, nil
	}
	signer, err := ssh.ParsePrivateKey([]byte(priv))
	if err != nil {
		var missing *ssh.PassphraseMissingError
		if errors.As(err, &missing) {
			return nil, errors.New("this key is passphrase-protected; provide the passphrase")
		}
		return nil, err
	}
	return signer, nil
}
