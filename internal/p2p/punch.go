// Package p2p implements client-side NAT hole punching: two devices of the same
// account exchange their public UDP endpoints through the sync server, punch a
// direct path and then run a QUIC connection over it. Everything after the
// punch — RPC, SSH jump, proxy dialing, future file management — is multiplexed
// as streams over that QUIC connection.
package p2p

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"time"

	"github.com/quic-go/quic-go"
)

const (
	probeMagic    = "WTP2P1"
	annInterval   = 250 * time.Millisecond
	probeInterval = 200 * time.Millisecond
	alpn          = "wailstools-p2p"
)

// Options describes one hole-punch attempt.
type Options struct {
	// Rendezvous is the sync server's UDP rendezvous address (host:port).
	Rendezvous string
	Session    string
	Secret     string
	// Role is "a" for the initiator (dials QUIC) or "b" for the responder
	// (listens for QUIC).
	Role string
	// Timeout bounds the whole attempt; defaults to 45s.
	Timeout time.Duration
	Logf    func(string, ...any)
}

// announce is the datagram sent to the rendezvous server.
type announce struct {
	Session string `json:"s"`
	Role    string `json:"r"`
}

type announceReply struct {
	OK    bool   `json:"ok"`
	Error string `json:"err"`
	You   string `json:"you"`
	Peer  string `json:"peer"`
}

var (
	// ErrPeerOffline means the other device never announced, so it is not
	// running or not logged in.
	ErrPeerOffline = errors.New("目标设备未响应：对方可能没有运行客户端或未登录")
	// ErrPunchFailed means both sides announced but no direct path opened.
	ErrPunchFailed = errors.New("打洞失败：双方网络不允许直连（对称型 NAT 或防火墙拦截）")
)

// Punch negotiates a direct QUIC connection with the peer device.
func Punch(ctx context.Context, o Options) (*Channel, error) {
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	if o.Timeout <= 0 {
		o.Timeout = 45 * time.Second
	}
	role := "a"
	if o.Role == "b" || o.Role == "B" {
		role = "b"
	}
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			_ = conn.Close()
		}
	}()

	deadline := time.Now().Add(o.Timeout)
	peerStr, err := announceEndpoint(ctx, conn, o, deadline)
	if err != nil {
		return nil, err
	}
	o.Logf("p2p: peer endpoint %s", peerStr)
	peerAddr, err := net.ResolveUDPAddr("udp4", peerStr)
	if err != nil {
		return nil, fmt.Errorf("解析对端地址失败: %w", err)
	}

	if err := punchProbe(ctx, conn, peerAddr, o.Secret, deadline); err != nil {
		return nil, err
	}
	o.Logf("p2p: direct path open, starting QUIC handshake")

	quicConn, err := handshake(ctx, conn, peerAddr, role, o.Secret)
	if err != nil {
		return nil, fmt.Errorf("直连建立失败: %w", err)
	}
	success = true
	return NewChannel(quicConn, o.Secret, peerStr), nil
}

// announceEndpoint keeps announcing until the server reports the peer's public
// endpoint.
func announceEndpoint(ctx context.Context, conn *net.UDPConn, o Options, deadline time.Time) (string, error) {
	raddr, err := net.ResolveUDPAddr("udp4", o.Rendezvous)
	if err != nil {
		return "", fmt.Errorf("rendezvous 地址无效: %w", err)
	}
	payload, _ := json.Marshal(announce{Session: o.Session, Role: o.Role})
	buf := make([]byte, 2048)
	lastErr := ""
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if _, err := conn.WriteToUDP(payload, raddr); err != nil {
			return "", err
		}
		_ = conn.SetReadDeadline(time.Now().Add(annInterval))
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		if !from.IP.Equal(raddr.IP) || from.Port != raddr.Port {
			continue
		}
		var rep announceReply
		if json.Unmarshal(buf[:n], &rep) != nil {
			continue
		}
		if rep.Error != "" {
			lastErr = rep.Error
		}
		if rep.OK && rep.Peer != "" {
			return rep.Peer, nil
		}
	}
	if lastErr != "" {
		return "", fmt.Errorf("打洞会话无效: %s", lastErr)
	}
	return "", ErrPeerOffline
}

// punchProbe sends probes until the peer answers with the same authenticated
// datagram, which proves the NAT mapping works in both directions.
func punchProbe(ctx context.Context, conn *net.UDPConn, peer *net.UDPAddr, secret string, deadline time.Time) error {
	probe := []byte(probeMagic + " " + token(secret, "probe"))
	buf := make([]byte, 2048)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, err := conn.WriteToUDP(probe, peer); err != nil {
			return err
		}
		_ = conn.SetReadDeadline(time.Now().Add(probeInterval))
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		if from.String() != peer.String() {
			continue
		}
		if string(buf[:n]) == string(probe) {
			return nil
		}
	}
	return ErrPunchFailed
}

// handshake upgrades the punched socket to QUIC and authenticates it with the
// session secret.
func handshake(ctx context.Context, conn *net.UDPConn, peer *net.UDPAddr, role, secret string) (*quic.Conn, error) {
	_ = conn.SetReadDeadline(time.Time{})
	cert, err := selfSignedCert()
	if err != nil {
		return nil, err
	}
	quicCfg := &quic.Config{
		HandshakeIdleTimeout: 15 * time.Second,
		MaxIdleTimeout:       3 * time.Minute,
		KeepAlivePeriod:      10 * time.Second,
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	var qconn *quic.Conn
	if role == "a" {
		tlsConf := &tls.Config{InsecureSkipVerify: true, NextProtos: []string{alpn}}
		qconn, err = quic.Dial(ctx, conn, peer, tlsConf, quicCfg)
	} else {
		tlsConf := &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{alpn}}
		var listener *quic.Listener
		listener, err = quic.Listen(conn, tlsConf, quicCfg)
		if err != nil {
			return nil, err
		}
		defer listener.Close()
		qconn, err = listener.Accept(ctx)
	}
	if err != nil {
		return nil, err
	}
	if err := authenticateStream(ctx, qconn, role, secret); err != nil {
		_ = qconn.CloseWithError(0, "auth failed")
		return nil, err
	}
	return qconn, nil
}

// authenticateStream proves both ends share the session secret.
func authenticateStream(ctx context.Context, conn *quic.Conn, role, secret string) error {
	want := []byte(token(secret, "hello"))
	ack := []byte(token(secret, "hello-ok"))
	if role == "a" {
		stream, err := conn.OpenStreamSync(ctx)
		if err != nil {
			return err
		}
		if _, err := stream.Write(want); err != nil {
			return err
		}
		got := make([]byte, len(ack))
		_ = stream.SetReadDeadline(time.Now().Add(10 * time.Second))
		if _, err := io.ReadFull(stream, got); err != nil {
			return errors.New("对端未完成认证（会话密钥不匹配）")
		}
		if !hmac.Equal(got, ack) {
			return errors.New("对端认证失败")
		}
		_ = stream.Close()
		return nil
	}
	stream, err := conn.AcceptStream(ctx)
	if err != nil {
		return err
	}
	got := make([]byte, len(want))
	_ = stream.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(stream, got); err != nil {
		return errors.New("对端未完成认证")
	}
	if !hmac.Equal(got, want) {
		return errors.New("会话密钥不匹配")
	}
	if _, err := stream.Write(ack); err != nil {
		return err
	}
	_ = stream.Close()
	return nil
}

// token derives a hex HMAC used for probes and the channel handshake.
func token(secret, purpose string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("wailstools-p2p/" + purpose))
	return fmt.Sprintf("%x", mac.Sum(nil))
}

// selfSignedCert builds an ephemeral certificate for the QUIC handshake. The
// peer does not verify it: authentication happens through the session secret.
func selfSignedCert() (tls.Certificate, error) {
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
		Subject:      pkix.Name{CommonName: "wailstools-p2p"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}
