package sniproxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// CA is the locally generated root that signs per-host MITM certificates.
type CA struct {
	cert     *x509.Certificate
	key      *ecdsa.PrivateKey
	certPath string
	keyPath  string

	mu     sync.Mutex
	leaves map[string]tls.Certificate
}

// LoadOrCreateCA loads (or generates) the root certificate in dir.
func LoadOrCreateCA(dir string) (*CA, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	ca := &CA{
		certPath: filepath.Join(dir, "root-ca.pem"),
		keyPath:  filepath.Join(dir, "root-ca.key"),
		leaves:   map[string]tls.Certificate{},
	}
	certPEM, errCert := os.ReadFile(ca.certPath)
	keyPEM, errKey := os.ReadFile(ca.keyPath)
	if errCert == nil && errKey == nil {
		certBlock, _ := pem.Decode(certPEM)
		keyBlock, _ := pem.Decode(keyPEM)
		if certBlock != nil && keyBlock != nil {
			cert, err := x509.ParseCertificate(certBlock.Bytes)
			if err == nil {
				key, kerr := x509.ParseECPrivateKey(keyBlock.Bytes)
				if kerr == nil {
					ca.cert = cert
					ca.key = key
					return ca, nil
				}
			}
		}
	}
	if err := ca.generate(); err != nil {
		return nil, err
	}
	return ca, nil
}

func (c *CA) generate() error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "WailsTools SNI Proxy Root CA", Organization: []string{"WailsTools"}},
		NotBefore:             now.Add(-24 * time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		MaxPathLen:            1,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(c.certPath, certPEM, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(c.keyPath, keyPEM, 0o600); err != nil {
		return err
	}
	c.cert = cert
	c.key = key
	return nil
}

// CertPath is the root certificate file the user must trust.
func (c *CA) CertPath() string { return c.certPath }

// CertPEM returns the root certificate bytes.
func (c *CA) CertPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.cert.Raw})
}

// Leaf returns a certificate for host signed by the root CA.
func (c *CA) Leaf(host string) (tls.Certificate, error) {
	host = normalizeHost(host)
	if host == "" {
		return tls.Certificate{}, errors.New("empty host")
	}
	c.mu.Lock()
	if cert, ok := c.leaves[host]; ok {
		c.mu.Unlock()
		return cert, nil
	}
	c.mu.Unlock()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host, Organization: []string{"WailsTools SNI Proxy"}},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(0, 1, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{host},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return tls.Certificate{}, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, err
	}
	out := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
	c.mu.Lock()
	c.leaves[host] = out
	c.mu.Unlock()
	return out, nil
}

// InstallRoot trusts the root certificate. On Windows it uses certutil for the
// current user; elsewhere it returns the path so the user can import manually.
func (c *CA) InstallRoot() (string, error) {
	if runtime.GOOS != "windows" {
		return c.certPath, fmt.Errorf("请手动导入根证书：%s", c.certPath)
	}
	if _, err := execCertutil(c.certPath); err != nil {
		return c.certPath, fmt.Errorf("导入根证书失败（可手动导入）：%w", err)
	}
	return c.certPath, nil
}
