package miproxy

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// ---- VLESS ---------------------------------------------------------------

// vlessConn speaks VLESS (no payload encryption; the transport provides it).
type vlessConn struct {
	conn   net.Conn
	buf    []byte
	header bool
}

func newVLESSConn(node Node, conn net.Conn, target string) (net.Conn, error) {
	uuid, err := uuidBytes(node.UUID)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 0, 64)
	buf = append(buf, 0x00) // version
	buf = append(buf, uuid...)
	buf = append(buf, 0x00) // addon length
	buf = append(buf, 0x01) // command: TCP
	dest, err := encodeDestination(target)
	if err != nil {
		return nil, err
	}
	buf = append(buf, dest...)
	if _, err := conn.Write(buf); err != nil {
		return nil, err
	}
	return &vlessConn{conn: conn}, nil
}

func (c *vlessConn) Read(p []byte) (int, error) {
	if !c.header {
		var hdr [2]byte
		if _, err := io.ReadFull(c.conn, hdr[:]); err != nil {
			return 0, err
		}
		if hdr[0] != 0x00 {
			return 0, errors.New("vless: bad response version")
		}
		if hdr[1] > 0 {
			skip := make([]byte, hdr[1])
			if _, err := io.ReadFull(c.conn, skip); err != nil {
				return 0, err
			}
		}
		c.header = true
	}
	return c.conn.Read(p)
}

func (c *vlessConn) Write(p []byte) (int, error)        { return c.conn.Write(p) }
func (c *vlessConn) Close() error                       { return c.conn.Close() }
func (c *vlessConn) LocalAddr() net.Addr                { return c.conn.LocalAddr() }
func (c *vlessConn) RemoteAddr() net.Addr               { return c.conn.RemoteAddr() }
func (c *vlessConn) SetDeadline(t time.Time) error      { return c.conn.SetDeadline(t) }
func (c *vlessConn) SetReadDeadline(t time.Time) error  { return c.conn.SetReadDeadline(t) }
func (c *vlessConn) SetWriteDeadline(t time.Time) error { return c.conn.SetWriteDeadline(t) }

// ---- Trojan --------------------------------------------------------------

// trojanConn speaks the Trojan protocol over TLS.
type trojanConn struct {
	conn net.Conn
}

func newTrojanConn(node Node, conn net.Conn, target string) (net.Conn, error) {
	sum := sha256.Sum224([]byte(node.Password))
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 0, 128)
	buf = append(buf, []byte(hex.EncodeToString(sum[:]))...)
	buf = append(buf, '\r', '\n')
	buf = append(buf, 0x01) // CONNECT
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			buf = append(buf, 0x01)
			buf = append(buf, v4...)
		} else {
			buf = append(buf, 0x04)
			buf = append(buf, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return nil, errors.New("trojan: domain too long")
		}
		buf = append(buf, 0x03, byte(len(host)))
		buf = append(buf, []byte(host)...)
	}
	var p [2]byte
	binary.BigEndian.PutUint16(p[:], uint16(port))
	buf = append(buf, p[:]...)
	buf = append(buf, '\r', '\n')
	if _, err := conn.Write(buf); err != nil {
		return nil, err
	}
	return &trojanConn{conn: conn}, nil
}

func (c *trojanConn) Read(p []byte) (int, error)         { return c.conn.Read(p) }
func (c *trojanConn) Write(p []byte) (int, error)        { return c.conn.Write(p) }
func (c *trojanConn) Close() error                       { return c.conn.Close() }
func (c *trojanConn) LocalAddr() net.Addr                { return c.conn.LocalAddr() }
func (c *trojanConn) RemoteAddr() net.Addr               { return c.conn.RemoteAddr() }
func (c *trojanConn) SetDeadline(t time.Time) error      { return c.conn.SetDeadline(t) }
func (c *trojanConn) SetReadDeadline(t time.Time) error  { return c.conn.SetReadDeadline(t) }
func (c *trojanConn) SetWriteDeadline(t time.Time) error { return c.conn.SetWriteDeadline(t) }

// ---- plain upstreams -----------------------------------------------------

// newSocks5Upstream dials target through an upstream SOCKS5 proxy.
func newSocks5Upstream(node Node, conn net.Conn, target string) (net.Conn, error) {
	methods := []byte{0x00}
	if node.Username != "" {
		methods = []byte{0x00, 0x02}
	}
	if _, err := conn.Write(append([]byte{0x05, byte(len(methods))}, methods...)); err != nil {
		return nil, err
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return nil, err
	}
	if reply[0] != 0x05 {
		return nil, errors.New("socks5 upstream: bad version")
	}
	switch reply[1] {
	case 0x00:
	case 0x02:
		user, pass := []byte(node.Username), []byte(node.Password)
		msg := append([]byte{0x01, byte(len(user))}, user...)
		msg = append(msg, byte(len(pass)))
		msg = append(msg, pass...)
		if _, err := conn.Write(msg); err != nil {
			return nil, err
		}
		auth := make([]byte, 2)
		if _, err := io.ReadFull(conn, auth); err != nil {
			return nil, err
		}
		if auth[1] != 0x00 {
			return nil, errors.New("socks5 upstream: auth failed")
		}
	default:
		return nil, errors.New("socks5 upstream: no acceptable auth method")
	}
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return nil, err
	}
	port, _ := strconv.Atoi(portStr)
	req := []byte{0x05, 0x01, 0x00}
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			req = append(req, 0x01)
			req = append(req, v4...)
		} else {
			req = append(req, 0x04)
			req = append(req, ip.To16()...)
		}
	} else {
		req = append(req, 0x03, byte(len(host)))
		req = append(req, []byte(host)...)
	}
	var p [2]byte
	binary.BigEndian.PutUint16(p[:], uint16(port))
	req = append(req, p[:]...)
	if _, err := conn.Write(req); err != nil {
		return nil, err
	}
	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		return nil, err
	}
	if head[1] != 0x00 {
		return nil, fmt.Errorf("socks5 upstream: connect failed (code %d)", head[1])
	}
	switch head[3] {
	case 0x01:
		_, err = io.ReadFull(conn, make([]byte, 4+2))
	case 0x04:
		_, err = io.ReadFull(conn, make([]byte, 16+2))
	case 0x03:
		var l [1]byte
		if _, err := io.ReadFull(conn, l[:]); err != nil {
			return nil, err
		}
		_, err = io.ReadFull(conn, make([]byte, int(l[0])+2))
	default:
		return nil, errors.New("socks5 upstream: bad address type")
	}
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// newHTTPUpstream tunnels target through an upstream HTTP proxy.
func newHTTPUpstream(node Node, conn net.Conn, target string) (net.Conn, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", target, target)
	if node.Username != "" {
		b.WriteString("Proxy-Authorization: Basic " + basicAuth(node.Username, node.Password) + "\r\n")
	}
	b.WriteString("Proxy-Connection: Keep-Alive\r\n\r\n")
	if _, err := conn.Write([]byte(b.String())); err != nil {
		return nil, err
	}
	buf := make([]byte, 0, 128)
	tmp := make([]byte, 1)
	for {
		if _, err := io.ReadFull(conn, tmp); err != nil {
			return nil, err
		}
		buf = append(buf, tmp[0])
		if len(buf) >= 4 && string(buf[len(buf)-4:]) == "\r\n\r\n" {
			break
		}
		if len(buf) > 16*1024 {
			return nil, errors.New("http upstream: response too large")
		}
	}
	line := string(buf)
	if !strings.Contains(line, " 200 ") {
		return nil, fmt.Errorf("http upstream: %s", strings.SplitN(line, "\r\n", 2)[0])
	}
	return conn, nil
}

func basicAuth(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}

var _ = tls.Config{}
