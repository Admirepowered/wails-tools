package miproxy

import (
	"bytes"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"os"
	"reflect"
	"sync/atomic"
	"time"
	"unsafe"

	utls "github.com/metacubex/utls"
)

// VLESS flow `xtls-rprx-vision` (port of Xray's proxy.VisionReader/Writer):
//
//	[uuid, first frame only][command][contentLen u16][paddingLen u16][payload][padding]
//
// While padding, every write is wrapped in such a block. As soon as the first
// inner TLS application-data record appears, Xray *splices*: the client sends
// that record in a "direct" block and from then on writes/reads bypass the
// outer TLS entirely (the inner TLS records travel end to end). We reproduce
// that with the underlying TCP connection plus the TLS conn's buffered input.
const (
	visionContinue byte = 0x00
	visionEnd      byte = 0x01
	visionDirect   byte = 0x02

	visionHeaderLen = 16 + 1 + 2 + 2
	visionUUIDLen   = 16
)

var (
	visionDebug = os.Getenv("WT_VISION_DEBUG") != ""
)

func vlog(format string, args ...any) {
	if visionDebug {
		fmt.Fprintf(os.Stderr, "[vision] "+format+"\n", args...)
	}
}

// visionConn wraps a VLESS stream with the Vision record layer.
type visionConn struct {
	tls   net.Conn // outer TLS/REALITY conn, used while padding
	raw   net.Conn // connection under the outer TLS, used after the splice
	inner any      // *tls.Conn or *utls.UConn, for buffer flushing
	uuid  []byte

	wroteUUID bool
	padding   bool // framed writes still in effect

	readUUID bool
	readBuf  []byte
	// framed reads the padding blocks; spliced bypasses the outer TLS.
	framed  bool
	spliced bool

	directBytes int64
}

// newVisionConn wraps the VLESS transport conn; raw must be the connection
// beneath the TLS layer (same peer).
func newVisionConn(tlsConn, rawConn net.Conn, uuid []byte, inner any) net.Conn {
	return &visionConn{tls: tlsConn, raw: rawConn, inner: inner, uuid: uuid, padding: true, framed: true}
}

func visionPad(length int, longPadding bool) int {
	if longPadding {
		if length < 900 {
			return rand.Intn(500) + 900 - length
		}
		return rand.Intn(500)
	}
	return rand.Intn(256)
}

// isTLSApplicationData reports whether p begins a TLS application-data record.
func isTLSApplicationData(p []byte) bool {
	return len(p) >= 3 && p[0] == 0x17 && p[1] == 0x03 && p[2] >= 0x01
}

// writePadded emits one Vision block through the outer TLS connection.
func (c *visionConn) writePadded(p []byte, command byte, longPadding bool) error {
	pad := visionPad(len(p), longPadding)
	frame := make([]byte, 0, visionHeaderLen+len(p)+pad)
	if !c.wroteUUID {
		c.wroteUUID = true
		frame = append(frame, c.uuid...)
	}
	frame = append(frame, command, byte(len(p)>>8), byte(len(p)), byte(pad>>8), byte(pad))
	frame = append(frame, p...)
	if pad > 0 {
		frame = append(frame, make([]byte, pad)...)
	}
	vlog("write frame cmd=%d content=%d padding=%d", command, len(p), pad)
	if _, err := c.tls.Write(frame); err != nil {
		return err
	}
	return nil
}

// Write frames the payload until the inner TLS application data starts, then
// splices to the raw connection.
func (c *visionConn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if !c.padding {
		n, err := c.raw.Write(p)
		atomic.AddInt64(&c.directBytes, int64(n))
		return n, err
	}
	if isTLSApplicationData(p) {
		// This block carries the first inner application-data record; mark it
		// "direct" and splice everything after it.
		vlog("write splice content=%d", len(p))
		if err := c.writePadded(p, visionDirect, true); err != nil {
			return 0, err
		}
		c.padding = false
		return len(p), nil
	}
	if err := c.writePadded(p, visionContinue, false); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Read decodes padding blocks until the server splices, then reads the raw
// connection (after draining the TLS conn's already-buffered input).
func (c *visionConn) Read(p []byte) (int, error) {
	for {
		if len(c.readBuf) > 0 {
			n := copy(p, c.readBuf)
			c.readBuf = c.readBuf[n:]
			return n, nil
		}
		if c.spliced {
			n, err := c.raw.Read(p)
			atomic.AddInt64(&c.directBytes, int64(n))
			return n, err
		}
		if !c.framed {
			// The server ended the padding blocks; the rest of the stream is
			// plain TLS payload (no splice).
			return c.tls.Read(p)
		}
		headerLen := visionHeaderLen - visionUUIDLen
		if !c.readUUID {
			headerLen = visionHeaderLen
		}
		header := make([]byte, headerLen)
		if _, err := io.ReadFull(c.tls, header); err != nil {
			// The server may have spliced without a marker: hand over the raw
			// connection so the caller does not lose the stream.
			if c.flushTLSBuffers() {
				continue
			}
			return 0, err
		}
		if !c.readUUID {
			c.readUUID = true
			if !bytes.Equal(header[:visionUUIDLen], c.uuid) {
				return 0, errors.New("vision: 服务端返回的 UUID 不匹配")
			}
			header = header[visionUUIDLen:]
		}
		command := header[0]
		contentLen := int(binary.BigEndian.Uint16(header[1:3]))
		paddingLen := int(binary.BigEndian.Uint16(header[3:5]))
		vlog("read frame cmd=%d content=%d padding=%d", command, contentLen, paddingLen)
		if contentLen > 0 {
			buf := make([]byte, contentLen)
			if _, err := io.ReadFull(c.tls, buf); err != nil {
				return 0, err
			}
			c.readBuf = buf
		}
		if paddingLen > 0 {
			if _, err := io.CopyN(io.Discard, c.tls, int64(paddingLen)); err != nil {
				return 0, err
			}
		}
		switch command {
		case visionContinue:
		case visionEnd:
			// Padding is over but the stream stays inside the outer TLS.
			vlog("read padding end")
			c.framed = false
		case visionDirect:
			vlog("read splice bytes=%d", contentLen)
			c.spliced = true
			if len(c.readBuf) == 0 {
				c.flushTLSBuffers()
			}
		default:
			return 0, errors.New("vision: 未知的帧命令")
		}
	}
}

// flushTLSBuffers moves the plaintext already buffered inside the TLS conn
// into readBuf; without it the splice would drop those bytes.
func (c *visionConn) flushTLSBuffers() bool {
	pending := drainTLSBuffers(c.inner)
	if len(pending) == 0 {
		return false
	}
	vlog("read drain tls buffers bytes=%d", len(pending))
	c.readBuf = append(c.readBuf, pending...)
	return true
}

// drainTLSBuffers extracts and clears the application-data buffers held by a
// crypto/tls or utls connection (the fields Xray reaches via reflection too).
func drainTLSBuffers(conn any) []byte {
	var target any
	switch t := conn.(type) {
	case *tls.Conn:
		target = t
	case *utls.UConn:
		target = t.Conn
	default:
		return nil
	}
	typ := reflect.TypeOf(target)
	if typ.Kind() != reflect.Pointer || typ.Elem().Kind() != reflect.Struct {
		return nil
	}
	typ = typ.Elem()
	p := unsafe.Pointer(reflect.ValueOf(target).Pointer())
	var out []byte
	if f, ok := typ.FieldByName("input"); ok {
		r := (*bytes.Reader)(unsafe.Add(p, f.Offset))
		if r.Len() > 0 {
			buf := make([]byte, r.Len())
			if _, err := r.Read(buf); err == nil {
				out = append(out, buf...)
			}
		}
		*r = bytes.Reader{}
	}
	if f, ok := typ.FieldByName("rawInput"); ok {
		b := (*bytes.Buffer)(unsafe.Add(p, f.Offset))
		if b.Len() > 0 {
			out = append(out, b.Bytes()...)
		}
		*b = bytes.Buffer{}
	}
	return out
}

// ---- net.Conn plumbing ---------------------------------------------------

func (c *visionConn) Close() error                       { return c.tls.Close() }
func (c *visionConn) LocalAddr() net.Addr                { return c.tls.LocalAddr() }
func (c *visionConn) RemoteAddr() net.Addr               { return c.tls.RemoteAddr() }
func (c *visionConn) SetDeadline(t time.Time) error      { return c.tls.SetDeadline(t) }
func (c *visionConn) SetReadDeadline(t time.Time) error  { return c.tls.SetReadDeadline(t) }
func (c *visionConn) SetWriteDeadline(t time.Time) error { return c.tls.SetWriteDeadline(t) }
