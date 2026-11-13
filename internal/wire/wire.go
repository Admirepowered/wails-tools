// Package wire implements the framed request/response protocol shared by the
// client and the server. Every logical operation runs on its own multiplexed
// stream; a request may either return a JSON body or take over the stream for
// raw byte piping (relay, file transfer).
package wire

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// Version is the protocol version negotiated in the stream handshake.
const Version = 1

// maxFrame bounds a single JSON frame (8 MiB).
const maxFrame = 8 << 20

// Error is the structured error carried by a failed response.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

// Errorf builds a *Error.
func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Hello is sent by the client as the first frame on every stream.
type Hello struct {
	Version int    `json:"version"`
	Token   string `json:"token"`
	App     string `json:"app"`
	// Purpose is free-form ("rpc", "relay", "file") and helps server logging.
	Purpose string `json:"purpose"`
}

// Request is a normal (non-stream) call envelope.
type Request struct {
	ID        string          `json:"id"`
	Method    string          `json:"method"`
	Body      json.RawMessage `json:"body,omitempty"`
	Streaming bool            `json:"streaming,omitempty"`
}

// Response answers a Request. When Streaming is true, OK means the peer may
// now use the raw stream for byte-oriented IO.
type Response struct {
	ID        string          `json:"id"`
	OK        bool            `json:"ok"`
	Body      json.RawMessage `json:"body,omitempty"`
	Error     *Error          `json:"error,omitempty"`
	Streaming bool            `json:"streaming,omitempty"`
}

// WriteFrame writes a length-prefixed JSON value.
func WriteFrame(w io.Writer, v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(payload) > maxFrame {
		return errors.New("wire: frame too large")
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err = w.Write(payload)
	return err
}

// ReadFrame reads a length-prefixed JSON value into v.
func ReadFrame(r io.Reader, v any) error {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > maxFrame {
		return fmt.Errorf("wire: invalid frame length %d", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return err
	}
	return json.Unmarshal(buf, v)
}

// Encode marshals v into a RawMessage.
func Encode(v any) (json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	if raw, ok := v.(json.RawMessage); ok {
		return raw, nil
	}
	return json.Marshal(v)
}

// Decode unmarshals a raw body into v.
func Decode(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, v)
}

// Handler serves decoded requests on a single stream.
type Handler interface {
	// Handle executes a non-streaming request and returns a JSON-serialisable body.
	Handle(method string, body json.RawMessage, sess *ServerConn) (any, error)
	// Pipe takes ownership of the stream for raw IO after the ack was written.
	// It returns when the stream is done. deadline guards setup only.
	Pipe(method string, body json.RawMessage, conn net.Conn, sess *ServerConn) error
}

// ServerConn is the per-stream server state passed to handlers.
type ServerConn struct {
	Hello  Hello
	UserID string
	Remote string
}

const (
	// DefaultIOTimeout is used for handshake reads.
	DefaultIOTimeout = 15 * time.Second
	// IdleTimeout bounds an idle RPC stream.
	IdleTimeout = 2 * time.Minute
)

// Serve handles one accepted stream: handshake, then request loop. The caller
// closes conn. auth validates the hello frame.
func Serve(conn net.Conn, authenticator func(Hello) (userID string, err error), h Handler) error {
	_ = conn.SetDeadline(time.Now().Add(DefaultIOTimeout))
	var hello Hello
	if err := ReadFrame(conn, &hello); err != nil {
		return err
	}
	sess := &ServerConn{Hello: hello, Remote: conn.RemoteAddr().String()}
	if hello.Version != Version {
		_ = WriteFrame(conn, &Response{OK: false, Streaming: true, Error: Errorf("version", "protocol version %d unsupported (want %d)", hello.Version, Version)})
		return errors.New("wire: version mismatch")
	}
	if authenticator != nil {
		uid, err := authenticator(hello)
		if err != nil {
			_ = WriteFrame(conn, &Response{OK: false, Streaming: true, Error: toWireError(err)})
			return err
		}
		sess.UserID = uid
	}
	if err := WriteFrame(conn, &Response{OK: true}); err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Time{})

	for {
		_ = conn.SetReadDeadline(time.Now().Add(IdleTimeout))
		var req Request
		if err := ReadFrame(conn, &req); err != nil {
			return err
		}
		_ = conn.SetReadDeadline(time.Time{})
		if req.Streaming {
			if h == nil {
				_ = WriteFrame(conn, &Response{ID: req.ID, OK: false, Error: Errorf("unsupported", "streaming method %q", req.Method)})
				return errors.New("wire: unsupported streaming")
			}
			if err := WriteFrame(conn, &Response{ID: req.ID, OK: true, Streaming: true}); err != nil {
				return err
			}
			if err := h.Pipe(req.Method, req.Body, conn, sess); err != nil {
				return err
			}
			return nil
		}
		var body any
		var err error
		if h != nil {
			body, err = h.Handle(req.Method, req.Body, sess)
		} else {
			err = Errorf("unsupported", "no handler")
		}
		resp := &Response{ID: req.ID, OK: err == nil}
		if err != nil {
			resp.Error = toWireError(err)
		} else {
			if resp.Body, err = Encode(body); err != nil {
				resp.OK = false
				resp.Error = toWireError(err)
			}
		}
		if err := WriteFrame(conn, resp); err != nil {
			return err
		}
	}
}

func toWireError(err error) *Error {
	var we *Error
	if errors.As(err, &we) {
		return we
	}
	return &Error{Code: "error", Message: err.Error()}
}

// Client is a single multiplexed stream used as an RPC channel.
type Client struct {
	conn  net.Conn
	Hello Hello
}

// NewClient performs the handshake on conn.
func NewClient(conn net.Conn, hello Hello) (*Client, error) {
	if hello.Version == 0 {
		hello.Version = Version
	}
	_ = conn.SetDeadline(time.Now().Add(DefaultIOTimeout))
	if err := WriteFrame(conn, &hello); err != nil {
		return nil, err
	}
	var resp Response
	if err := ReadFrame(conn, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, resp.Error
	}
	_ = conn.SetDeadline(time.Time{})
	return &Client{conn: conn, Hello: hello}, nil
}

// Conn exposes the underlying stream.
func (c *Client) Conn() net.Conn { return c.conn }

// Close closes the stream.
func (c *Client) Close() error { return c.conn.Close() }

const clientIDBase = "c"

var clientSeq counter

type counter struct{ n uint64 }

func (c *counter) next() uint64 { c.n++; return c.n }

// Call performs one request and decodes the response body into out (if non-nil).
func (c *Client) Call(method string, body any, out any) error {
	raw, err := Encode(body)
	if err != nil {
		return err
	}
	req := &Request{ID: nextID(), Method: method, Body: raw}
	if err := WriteFrame(c.conn, req); err != nil {
		return err
	}
	var resp Response
	if err := ReadFrame(c.conn, &resp); err != nil {
		return err
	}
	if !resp.OK {
		if resp.Error == nil {
			return Errorf("error", "request failed")
		}
		return resp.Error
	}
	if out != nil {
		return Decode(resp.Body, out)
	}
	return nil
}

// OpenStream negotiates a raw pipe on this stream.
func (c *Client) OpenStream(method string, body any) error {
	raw, err := Encode(body)
	if err != nil {
		return err
	}
	req := &Request{ID: nextID(), Method: method, Body: raw, Streaming: true}
	if err := WriteFrame(c.conn, req); err != nil {
		return err
	}
	var resp Response
	if err := ReadFrame(c.conn, &resp); err != nil {
		return err
	}
	if !resp.OK {
		if resp.Error == nil {
			return Errorf("error", "stream refused")
		}
		return resp.Error
	}
	_ = c.conn.SetDeadline(time.Time{})
	return nil
}

func nextID() string {
	return fmt.Sprintf("%s-%d", clientIDBase, clientSeq.next())
}
