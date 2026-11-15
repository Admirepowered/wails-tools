package miproxy

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math/rand"
	"net"
	"time"
)

// VLESS flow `xtls-rprx-vision` framing (port of Xray's vision/padding logic):
//
//	[uuid, first frame only][command][contentLen u16][paddingLen u16][payload][padding]
//
// The client keeps sending "continue" frames; the server answers in the same
// format and may switch the stream to a "direct"/"end" command, after which the
// remaining bytes are read raw.
const (
	visionContinue byte = 0x00
	visionEnd      byte = 0x01
	visionDirect   byte = 0x02

	visionHeaderLen = 16 + 1 + 2 + 2
	visionUUIDLen   = 16
)

// visionConn wraps a VLESS stream with the Vision record layer.
type visionConn struct {
	net.Conn
	uuid []byte

	wroteUUID bool

	readUUID   bool
	readBuffer []byte
	raw        bool

	switchedAt time.Time
}

// newVisionConn wraps conn; uuid is the account id echoed on the first frame.
func newVisionConn(conn net.Conn, uuid []byte) net.Conn {
	return &visionConn{Conn: conn, uuid: uuid}
}

func visionPad(len int, first bool) int {
	if first {
		// Hide the VLESS header inside a large first frame, like Xray does.
		base := 0
		if len < 900 {
			base = 900 - len
		}
		return base + rand.Intn(500)
	}
	return rand.Intn(500)
}

// Write frames the payload and sends it.
func (c *visionConn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	pad := visionPad(len(p), !c.wroteUUID)
	header := make([]byte, 0, visionHeaderLen+len(p)+pad)
	if !c.wroteUUID {
		c.wroteUUID = true
		header = append(header, c.uuid...)
	}
	header = append(header, visionContinue)
	header = append(header, byte(len(p)>>8), byte(len(p)))
	header = append(header, byte(pad>>8), byte(pad))
	header = append(header, p...)
	if pad > 0 {
		header = append(header, make([]byte, pad)...)
	}
	n, err := c.Conn.Write(header)
	if err != nil {
		return 0, err
	}
	if n != len(header) {
		return 0, io.ErrShortWrite
	}
	return len(p), nil
}

// Read decodes frames until it has payload for the caller.
func (c *visionConn) Read(p []byte) (int, error) {
	if c.raw {
		return c.Conn.Read(p)
	}
	for {
		if len(c.readBuffer) > 0 {
			n := copy(p, c.readBuffer)
			c.readBuffer = c.readBuffer[n:]
			if n > 0 {
				return n, nil
			}
		}
		headerLen := visionHeaderLen - visionUUIDLen
		if !c.readUUID {
			headerLen = visionHeaderLen
		}
		header := make([]byte, headerLen)
		if _, err := io.ReadFull(c.Conn, header); err != nil {
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
		if contentLen > 0 {
			buf := make([]byte, contentLen)
			if _, err := io.ReadFull(c.Conn, buf); err != nil {
				return 0, err
			}
			c.readBuffer = buf
		}
		if paddingLen > 0 {
			if _, err := io.CopyN(io.Discard, c.Conn, int64(paddingLen)); err != nil {
				return 0, err
			}
		}
		switch command {
		case visionContinue:
		case visionEnd, visionDirect:
			// The server stopped framing; the rest of the stream is raw.
			c.raw = true
			c.switchedAt = time.Now()
		default:
			return 0, errors.New("vision: 未知的帧命令")
		}
	}
}