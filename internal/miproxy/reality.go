package miproxy

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	utls "github.com/metacubex/utls"
	"golang.org/x/crypto/hkdf"
)

// realityMaxShortIDLen mirrors Xray's shortId limit.
const realityMaxShortIDLen = 8

// realityOptions carries the parsed REALITY parameters of a node.
type realityOptions struct {
	ServerName  string
	PublicKey   *ecdh.PublicKey
	ShortID     [realityMaxShortIDLen]byte
	Fingerprint utls.ClientHelloID
}

// newRealityOptions validates a node's REALITY fields.
func newRealityOptions(node Node) (realityOptions, error) {
	out := realityOptions{
		ServerName:  firstNonEmpty(node.SNI, node.Host, node.Server),
		Fingerprint: realityFingerprint(node.Fingerprint),
	}
	if strings.TrimSpace(node.RealityPublicKey) == "" {
		return out, errors.New("REALITY 节点缺少 publicKey (pbk)")
	}
	pub, err := decodeRealityPublicKey(node.RealityPublicKey)
	if err != nil {
		return out, err
	}
	out.PublicKey = pub
	sid, err := decodeRealityShortID(node.RealityShortID)
	if err != nil {
		return out, err
	}
	out.ShortID = sid
	return out, nil
}

func decodeRealityPublicKey(value string) (*ecdh.PublicKey, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		raw, err = base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	}
	if err != nil {
		return nil, fmt.Errorf("REALITY publicKey 解码失败: %w", err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("REALITY publicKey 长度错误: %d", len(raw))
	}
	return ecdh.X25519().NewPublicKey(raw)
}

func decodeRealityShortID(value string) ([realityMaxShortIDLen]byte, error) {
	var out [realityMaxShortIDLen]byte
	value = strings.TrimSpace(value)
	if value == "" {
		return out, nil
	}
	if len(value) > realityMaxShortIDLen*2 {
		return out, fmt.Errorf("REALITY shortId 过长: %s", value)
	}
	raw, err := hex.DecodeString(value)
	if err != nil {
		return out, fmt.Errorf("REALITY shortId 不是十六进制: %w", err)
	}
	copy(out[:], raw)
	return out, nil
}

// realityFingerprint maps the link's fp= value to a uTLS ClientHello.
func realityFingerprint(name string) utls.ClientHelloID {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "ios":
		return utls.HelloIOS_Auto
	case "android":
		return utls.HelloAndroid_11_OkHttp
	case "firefox":
		return utls.HelloFirefox_Auto
	case "safari", "safari16":
		return utls.HelloSafari_Auto
	case "edge":
		return utls.HelloEdge_Auto
	case "360", "360browser":
		return utls.Hello360_Auto
	case "qq", "qqbrowser":
		return utls.HelloQQ_Auto
	case "random", "randomized":
		return utls.HelloRandomized
	case "", "chrome":
		return utls.HelloChrome_Auto
	default:
		return utls.HelloChrome_Auto
	}
}

// realityHandshake performs the REALITY client handshake (port of XTLS/REALITY
// as implemented by mihomo's component/tls/reality.go).
func realityHandshake(ctx context.Context, conn net.Conn, timeout time.Duration, opts realityOptions) (net.Conn, error) {
	for retry := 0; ; retry++ {
		verifier := &realityVerifier{serverName: opts.ServerName}
		uConfig := &utls.Config{
			ServerName:             opts.ServerName,
			InsecureSkipVerify:     true,
			SessionTicketsDisabled: true,
			VerifyConnection:       verifier.VerifyConnection,
		}
		uConn := utls.UClient(conn, uConfig, opts.Fingerprint)
		verifier.UConn = uConn
		if err := uConn.BuildHandshakeState(); err != nil {
			return nil, err
		}
		// Old REALITY servers do not understand the X25519MLKEM768 group.
		if err := removeX25519MLKEM768(uConn); err != nil {
			return nil, err
		}

		hello := uConn.HandshakeState.Hello
		// golang/go#5373: the raw ClientHello must be patched together with the
		// session id, otherwise the server sees a mismatch.
		for i := range hello.Raw[39 : 39+32] {
			hello.Raw[39+i] = 0
		}
		binary.BigEndian.PutUint64(hello.SessionId, uint64(time.Now().Unix()))
		copy(hello.SessionId[8:], opts.ShortID[:])
		hello.SessionId[0] = 1
		hello.SessionId[1] = 8
		hello.SessionId[2] = 2

		keys := uConn.HandshakeState.State13.KeyShareKeys
		if keys == nil || keys.Ecdhe == nil {
			if retry > 2 {
				return nil, errors.New("REALITY 握手失败：无法生成 ECDHE 密钥")
			}
			continue
		}
		authKey, err := keys.Ecdhe.ECDH(opts.PublicKey)
		if err != nil {
			return nil, err
		}
		verifier.authKey = authKey
		if _, err := hkdf.New(sha256.New, authKey, hello.Random[:20], []byte("REALITY")).Read(authKey); err != nil {
			return nil, err
		}
		block, err := aes.NewCipher(authKey)
		if err != nil {
			return nil, err
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		aead.Seal(hello.SessionId[:0], hello.Random[20:], hello.SessionId[:16], hello.Raw)
		copy(hello.Raw[39:], hello.SessionId)

		hsCtx := ctx
		if timeout > 0 {
			var cancel context.CancelFunc
			hsCtx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
		if err := uConn.HandshakeContext(hsCtx); err != nil {
			return nil, fmt.Errorf("REALITY 握手失败: %w", err)
		}
		if !verifier.verified {
			return nil, errors.New("REALITY 认证失败（server 未返回合法签名，检查 pbk/sid/SNI）")
		}
		return uConn, nil
	}
}

// removeX25519MLKEM768 strips the hybrid group from the ClientHello.
func removeX25519MLKEM768(conn *utls.UConn) error {
	for _, extension := range conn.Extensions {
		if ce, ok := extension.(*utls.SupportedCurvesExtension); ok {
			ce.Curves = slices.DeleteFunc(ce.Curves, func(id utls.CurveID) bool {
				return id == utls.X25519MLKEM768
			})
		}
		if ks, ok := extension.(*utls.KeyShareExtension); ok {
			ks.KeyShares = slices.DeleteFunc(ks.KeyShares, func(share utls.KeyShare) bool {
				return share.Group == utls.X25519MLKEM768
			})
		}
	}
	return conn.BuildHandshakeState()
}

// realityVerifier validates the REALITY server certificate.
type realityVerifier struct {
	*utls.UConn
	serverName string
	authKey    []byte
	verified   bool
}

func (c *realityVerifier) VerifyConnection(state utls.ConnectionState) error {
	certs := state.PeerCertificates
	if len(certs) == 0 {
		return errors.New("REALITY: 服务端没有证书")
	}
	if pub, ok := certs[0].PublicKey.(ed25519.PublicKey); ok {
		h := hmac.New(sha512.New, c.authKey)
		h.Write(pub)
		if bytes.Equal(h.Sum(nil), certs[0].Signature) {
			c.verified = true
			return nil
		}
	}
	// Fall back to normal verification against the masqueraded hostname.
	opts := x509.VerifyOptions{DNSName: c.serverName, Intermediates: x509.NewCertPool()}
	for _, cert := range certs[1:] {
		opts.Intermediates.AddCert(cert)
	}
	if _, err := certs[0].Verify(opts); err != nil {
		return err
	}
	return nil
}