package netx

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Fingerprint returns the SHA-256 fingerprint of a certificate in the
// "AA:BB:..." colon form used for pinning.
func Fingerprint(cert *x509.Certificate) string {
	if cert == nil {
		return ""
	}
	sum := sha256.Sum256(cert.Raw)
	return colonHex(sum[:])
}

func colonHex(b []byte) string {
	h := strings.ToUpper(hex.EncodeToString(b))
	parts := make([]string, 0, len(h)/2)
	for i := 0; i < len(h); i += 2 {
		parts = append(parts, h[i:i+2])
	}
	return strings.Join(parts, ":")
}

// NormalizeFingerprint strips separators and lowercases for comparison.
func NormalizeFingerprint(fp string) string {
	r := strings.NewReplacer(":", "", " ", "", "-", "")
	return strings.ToLower(r.Replace(strings.TrimSpace(fp)))
}

// LoadOrCreateCert loads a PEM certificate/key pair, generating a self-signed
// certificate on first use.
func LoadOrCreateCert(dir, name string, extraSANs []string) (tls.Certificate, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return tls.Certificate{}, err
	}
	certPath := filepath.Join(dir, name+".crt")
	keyPath := filepath.Join(dir, name+".key")
	if cert, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		return cert, nil
	}
	cert, err := generateSelfSigned(extraSANs)
	if err != nil {
		return tls.Certificate{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
	keyDER, err := x509.MarshalECPrivateKey(cert.PrivateKey.(*ecdsa.PrivateKey))
	if err != nil {
		return tls.Certificate{}, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, err
	}
	return cert, nil
}

func generateSelfSigned(extraSANs []string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	hostname, _ := os.Hostname()
	dnsNames := []string{"localhost", "wailstools.local"}
	if hostname != "" {
		dnsNames = append(dnsNames, hostname)
	}
	for _, san := range extraSANs {
		san = strings.TrimSpace(san)
		if san == "" {
			continue
		}
		if ip := net.ParseIP(san); ip != nil {
			continue
		}
		dnsNames = append(dnsNames, san)
	}
	ips := []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}
	for _, san := range extraSANs {
		if ip := net.ParseIP(strings.TrimSpace(san)); ip != nil {
			ips = append(ips, ip)
		}
	}
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "WailsTools Self-Signed", Organization: []string{"WailsTools"}},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              dnsNames,
		IPAddresses:           ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}

// ServerTLS wraps a certificate for the QUIC/TCP listeners.
func ServerTLS(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{ALPN},
		MinVersion:   tls.VersionTLS12,
	}
}

// ClientTLS builds a client TLS config that pins the server certificate when a
// fingerprint is known. onPin is invoked with the observed fingerprint so the
// caller can persist it (trust-on-first-use).
func ClientTLS(pin string, onPin func(string)) *tls.Config {
	want := NormalizeFingerprint(pin)
	cfg := &tls.Config{
		InsecureSkipVerify: true, // verification is done manually below
		NextProtos:         []string{ALPN},
		MinVersion:         tls.VersionTLS12,
		ServerName:         "wailstools.local",
	}
	cfg.VerifyPeerCertificate = func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return errors.New("tls: server presented no certificate")
		}
		leaf, err := x509.ParseCertificate(rawCerts[0])
		if err != nil {
			return fmt.Errorf("tls: parse server certificate: %w", err)
		}
		got := NormalizeFingerprint(Fingerprint(leaf))
		if want == "" {
			if onPin != nil {
				onPin(Fingerprint(leaf))
			}
			return nil
		}
		if got != want {
			return fmt.Errorf("tls: server certificate fingerprint changed (expected %s, got %s)", pin, Fingerprint(leaf))
		}
		return nil
	}
	return cfg
}
