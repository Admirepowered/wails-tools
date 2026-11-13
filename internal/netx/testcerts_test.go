package netx

import (
	"crypto/tls"
)

// generateTestCert builds an in-memory self-signed certificate for tests.
func generateTestCert() (tls.Certificate, error) {
	return generateSelfSigned([]string{"127.0.0.1"})
}
