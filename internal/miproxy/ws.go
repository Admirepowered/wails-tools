package miproxy

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	wsOpContinuation = 0x0
	wsOpText         = 0x1
	wsOpBinary       = 0x2
	wsOpClose        = 0x8
	wsOpPing         = 0x9
	wsOpPong         = 0xA
)

// wsConn adapts a WebSocket connection to net.Conn.
type wsConn struct {
	conn   net.Conn
	reader *bufio.Reader

	writeMu sync.Mutex
	readMu  sync.Mutex

	pending []byte // decoded payload bytes not yet consumed
	closed  bool
}

// dialWebSocket performs the client handshake over an established connection.
func dialWebSocket(conn net.Conn, host, path string, header http.Header) (*wsConn, error) {
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)

	var b strings.Builder
	fmt.Fprintf(&b, "GET %s HTTP/1.1\r\n", path)
	fmt.Fprintf(&b, "Host: %s\r\n", host)
	b.WriteString("Upgrade: websocket\r\n")
	b.WriteString("Connection: Upgrade\r\n")
	fmt.Fprintf(&b, "Sec-WebSocket-Key: %s\r\n", key)
	b.WriteString("Sec-WebSocket-Version: 13\r\n")
	b.WriteString("User-Agent: Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36\r\n")
	for k, values := range header {
		for _, v := range values {
			fmt.Fprintf(&b, "%s: %s\r\n", k, v)
		}
	}
	b.WriteString("\r\n")

	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	if _, err := conn.Write([]byte(b.String())); err != nil {
		return nil, fmt.Errorf("ws handshake write: %w", err)
	}
	br := bufio.NewReaderSize(conn, 16*1024)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		return nil, fmt.Errorf("ws handshake read: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return nil, fmt.Errorf("ws upgrade rejected: %s", resp.Status)
	}
	expected := wsAcceptKey(key)
	if got := resp.Header.Get("Sec-WebSocket-Accept"); got != "" && got != expected {
		return nil, errors.New("ws upgrade: bad Sec-WebSocket-Accept")
	}
	_ = conn.SetDeadline(time.Time{})
	return &wsConn{conn: conn, reader: br}, nil
}

func wsAcceptKey(key string) string {
	h := sha1.New()
	h.Write([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// Write sends payload as one (or more) masked binary frames.
func (c *wsConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closed {
		return 0, net.ErrClosed
	}
	written := 0
	for len(p) > 0 {
		chunk := p
		const maxFrame = 32 * 1024
		if len(chunk) > maxFrame {
			chunk = chunk[:maxFrame]
		}
		if err := c.writeFrame(wsOpBinary, chunk); err != nil {
			return written, err
		}
		written += len(chunk)
		p = p[len(chunk):]
	}
	return written, nil
}

func (c *wsConn) writeFrame(opcode byte, payload []byte) error {
	var header []byte
	header = append(header, 0x80|opcode) // FIN + opcode
	mask := make([]byte, 4)
	if _, err := rand.Read(mask); err != nil {
		return err
	}
	length := len(payload)
	switch {
	case length < 126:
		header = append(header, byte(0x80|length))
	case length <= 0xFFFF:
		header = append(header, 0x80|126, byte(length>>8), byte(length))
	default:
		header = append(header, 0x80|127)
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], uint64(length))
		header = append(header, buf[:]...)
	}
	header = append(header, mask...)
	masked := make([]byte, length)
	for i := 0; i < length; i++ {
		masked[i] = payload[i] ^ mask[i%4]
	}
	if _, err := c.conn.Write(header); err != nil {
		return err
	}
	if length > 0 {
		if _, err := c.conn.Write(masked); err != nil {
			return err
		}
	}
	return nil
}

// Read returns stream bytes, decoding frames as needed.
func (c *wsConn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	for len(c.pending) == 0 {
		if c.closed {
			return 0, io.EOF
		}
		if err := c.readFrame(); err != nil {
			return 0, err
		}
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

func (c *wsConn) readFrame() error {
	var head [2]byte
	if _, err := io.ReadFull(c.reader, head[:]); err != nil {
		return err
	}
	fin := head[0]&0x80 != 0
	opcode := head[0] & 0x0F
	masked := head[1]&0x80 != 0
	length := uint64(head[1] & 0x7F)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(c.reader, ext[:]); err != nil {
			return err
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(c.reader, ext[:]); err != nil {
			return err
		}
		length = binary.BigEndian.Uint64(ext[:])
	}
	if length > 64<<20 {
		return errors.New("ws frame too large")
	}
	var maskKey [4]byte
	if masked {
		if _, err := io.ReadFull(c.reader, maskKey[:]); err != nil {
			return err
		}
	}
	payload := make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(c.reader, payload); err != nil {
			return err
		}
		if masked {
			for i := range payload {
				payload[i] ^= maskKey[i%4]
			}
		}
	}

	switch opcode {
	case wsOpBinary, wsOpText, wsOpContinuation:
		c.pending = append(c.pending, payload...)
		_ = fin
		return nil
	case wsOpPing:
		if err := c.writeFrame(wsOpPong, payload); err != nil {
			return err
		}
		return nil
	case wsOpPong:
		return nil
	case wsOpClose:
		c.closed = true
		_ = c.writeFrame(wsOpClose, nil)
		return io.EOF
	default:
		return nil
	}
}

func (c *wsConn) Close() error {
	c.writeMu.Lock()
	if !c.closed {
		c.closed = true
		_ = c.writeFrame(wsOpClose, nil)
	}
	c.writeMu.Unlock()
	return c.conn.Close()
}

func (c *wsConn) LocalAddr() net.Addr                { return c.conn.LocalAddr() }
func (c *wsConn) RemoteAddr() net.Addr               { return c.conn.RemoteAddr() }
func (c *wsConn) SetDeadline(t time.Time) error      { return c.conn.SetDeadline(t) }
func (c *wsConn) SetReadDeadline(t time.Time) error  { return c.conn.SetReadDeadline(t) }
func (c *wsConn) SetWriteDeadline(t time.Time) error { return c.conn.SetWriteDeadline(t) }

// buildTLSConfig creates a client TLS config for the node.
func buildTLSConfig(n Node, host string) *tls.Config {
	serverName := n.SNI
	if serverName == "" {
		serverName = n.Host
	}
	if serverName == "" {
		serverName = host
	}
	return &tls.Config{
		ServerName:         serverName,
		InsecureSkipVerify: n.SkipVerify,
		MinVersion:         tls.VersionTLS12,
		NextProtos:         []string{"http/1.1"},
	}
}

var _ = url.Parse
