package miproxy

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

// ssMethod describes a Shadowsocks AEAD cipher.
type ssMethod struct {
	keyLen int
	aead   func(key []byte) (cipher.AEAD, error)
}

func lookupSSMethod(name string) (ssMethod, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "aes-128-gcm":
		return ssMethod{keyLen: 16, aead: aesGCM}, nil
	case "aes-192-gcm":
		return ssMethod{keyLen: 24, aead: aesGCM}, nil
	case "aes-256-gcm":
		return ssMethod{keyLen: 32, aead: aesGCM}, nil
	case "chacha20-ietf-poly1305", "chacha20-poly1305":
		return ssMethod{keyLen: 32, aead: func(key []byte) (cipher.AEAD, error) {
			return chacha20poly1305.New(key)
		}}, nil
	}
	return ssMethod{}, fmt.Errorf("unsupported shadowsocks method %q", name)
}

func aesGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// evpBytesToKey derives the Shadowsocks master key from the password.
func evpBytesToKey(password string, keyLen int) []byte {
	var out, prev []byte
	for len(out) < keyLen {
		h := md5.New()
		h.Write(prev)
		h.Write([]byte(password))
		prev = h.Sum(nil)
		out = append(out, prev...)
	}
	return out[:keyLen]
}

func ssSubkey(masterKey, salt []byte) ([]byte, error) {
	r := hkdf.New(sha1.New, masterKey, salt, []byte("ss-subkey"))
	out := make([]byte, len(masterKey))
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, err
	}
	return out, nil
}

// ssConn implements Shadowsocks AEAD TCP framing.
type ssConn struct {
	conn  net.Conn
	mgr   ssMethod
	mkey  []byte
	write cipher.AEAD
	read  cipher.AEAD

	wNonce [12]byte
	rNonce [12]byte

	writeMu sync.Mutex
	readMu  sync.Mutex
	buf     []byte
	rready  bool
}

func newSSConn(node Node, conn net.Conn, target string) (net.Conn, error) {
	mgr, err := lookupSSMethod(node.Method)
	if err != nil {
		return nil, err
	}
	master := evpBytesToKey(node.Password, mgr.keyLen)
	salt := make([]byte, mgr.keyLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	subkey, err := ssSubkey(master, salt)
	if err != nil {
		return nil, err
	}
	aead, err := mgr.aead(subkey)
	if err != nil {
		return nil, err
	}
	c := &ssConn{conn: conn, mgr: mgr, write: aead, mkey: master}
	if _, err := conn.Write(salt); err != nil {
		return nil, err
	}

	// First payload is the SOCKS5-style address header.
	header := make([]byte, 0, 32)
	if ip := net.ParseIP(hostOf(target)); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			header = append(header, 0x01)
			header = append(header, v4...)
		} else {
			header = append(header, 0x04)
			header = append(header, ip.To16()...)
		}
	} else {
		host := hostOf(target)
		if len(host) > 255 {
			return nil, errors.New("ss: domain too long")
		}
		header = append(header, 0x03, byte(len(host)))
		header = append(header, []byte(host)...)
	}
	var p [2]byte
	binary.BigEndian.PutUint16(p[:], uint16(portOf(target)))
	header = append(header, p[:]...)
	if _, err := c.Write(header); err != nil {
		return nil, err
	}
	return c, nil
}

func hostOf(target string) string {
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		return target
	}
	return host
}

func portOf(target string) int {
	_, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return 0
	}
	var port int
	fmt.Sscanf(portStr, "%d", &port)
	return port
}

func incrementNonce(nonce *[12]byte) {
	for i := 0; i < 8; i++ {
		nonce[i]++
		if nonce[i] != 0 {
			return
		}
	}
}

func (c *ssConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	total := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > 0x3FFF {
			chunk = chunk[:0x3FFF]
		}
		var lenBuf [2]byte
		binary.BigEndian.PutUint16(lenBuf[:], uint16(len(chunk)))
		encLen := c.write.Seal(nil, c.wNonce[:], lenBuf[:], nil)
		incrementNonce(&c.wNonce)
		encPayload := c.write.Seal(nil, c.wNonce[:], chunk, nil)
		incrementNonce(&c.wNonce)
		if _, err := c.conn.Write(encLen); err != nil {
			return total, err
		}
		if _, err := c.conn.Write(encPayload); err != nil {
			return total, err
		}
		total += len(chunk)
		p = p[len(chunk):]
	}
	return total, nil
}

func (c *ssConn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	for len(c.buf) == 0 {
		if !c.rready {
			salt := make([]byte, c.mgr.keyLen)
			if _, err := io.ReadFull(c.conn, salt); err != nil {
				return 0, err
			}
			// The master key was derived at construction; reuse it.
			subkey, err := ssSubkey(c.mkey, salt)
			if err != nil {
				return 0, err
			}
			aead, err := c.mgr.aead(subkey)
			if err != nil {
				return 0, err
			}
			c.read = aead
			c.rready = true
			continue
		}
		var encLen [2 + 16]byte
		if _, err := io.ReadFull(c.conn, encLen[:]); err != nil {
			return 0, err
		}
		lenBuf, err := c.read.Open(nil, c.rNonce[:], encLen[:], nil)
		if err != nil {
			return 0, err
		}
		incrementNonce(&c.rNonce)
		length := int(binary.BigEndian.Uint16(lenBuf))
		if length <= 0 || length > 0x3FFF {
			return 0, errors.New("ss: invalid chunk length")
		}
		enc := make([]byte, length+16)
		if _, err := io.ReadFull(c.conn, enc); err != nil {
			return 0, err
		}
		plain, err := c.read.Open(nil, c.rNonce[:], enc, nil)
		if err != nil {
			return 0, err
		}
		incrementNonce(&c.rNonce)
		c.buf = plain
	}
	n := copy(p, c.buf)
	c.buf = c.buf[n:]
	return n, nil
}

func (c *ssConn) Close() error                       { return c.conn.Close() }
func (c *ssConn) LocalAddr() net.Addr                { return c.conn.LocalAddr() }
func (c *ssConn) RemoteAddr() net.Addr               { return c.conn.RemoteAddr() }
func (c *ssConn) SetDeadline(t time.Time) error      { return c.conn.SetDeadline(t) }
func (c *ssConn) SetReadDeadline(t time.Time) error  { return c.conn.SetReadDeadline(t) }
func (c *ssConn) SetWriteDeadline(t time.Time) error { return c.conn.SetWriteDeadline(t) }
