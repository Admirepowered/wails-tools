package wire

import (
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

type testHandler struct {
	pipePayload string
}

func (h *testHandler) Handle(method string, body json.RawMessage, sess *ServerConn) (any, error) {
	switch method {
	case "echo":
		var in map[string]any
		_ = Decode(body, &in)
		return map[string]any{"ok": true, "echo": in, "user": sess.UserID}, nil
	case "fail":
		return nil, Errorf("boom", "intentional failure")
	default:
		return nil, Errorf("unknown_method", "no such method %q", method)
	}
}

func (h *testHandler) Pipe(method string, body json.RawMessage, conn net.Conn, sess *ServerConn) error {
	if method != "pipe" {
		return Errorf("unknown_method", "no such stream %q", method)
	}
	// Echo back exactly what the client sends until EOF.
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			if _, werr := conn.Write(buf[:n]); werr != nil {
				return werr
			}
		}
		if err != nil {
			return nil
		}
	}
}

func startPipeServer(t *testing.T, h *testHandler) net.Conn {
	t.Helper()
	cli, srv := net.Pipe()
	go func() {
		_ = Serve(srv, func(hello Hello) (string, error) {
			if hello.Token == "" {
				return "", Errorf("unauthorized", "token required")
			}
			return "user-42", nil
		}, h)
		_ = srv.Close()
	}()
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

func TestClientCallRoundTrip(t *testing.T) {
	conn := startPipeServer(t, &testHandler{})
	client, err := NewClient(conn, Hello{Token: "secret", App: "test"})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	var out struct {
		OK   bool           `json:"ok"`
		Echo map[string]any `json:"echo"`
		User string         `json:"user"`
	}
	if err := client.Call("echo", map[string]any{"value": 7}, &out); err != nil {
		t.Fatalf("call: %v", err)
	}
	if !out.OK || out.User != "user-42" {
		t.Fatalf("unexpected response: %+v", out)
	}
	if got, _ := out.Echo["value"].(float64); got != 7 {
		t.Fatalf("echo mismatch: %+v", out.Echo)
	}
}

func TestClientCallError(t *testing.T) {
	conn := startPipeServer(t, &testHandler{})
	client, err := NewClient(conn, Hello{Token: "secret"})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	err = client.Call("fail", nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var we *Error
	if !asError(err, &we) || we.Code != "boom" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func asError(err error, target **Error) bool {
	we, ok := err.(*Error)
	if ok {
		*target = we
	}
	return ok
}

func TestHandshakeRejectsMissingToken(t *testing.T) {
	conn := startPipeServer(t, &testHandler{})
	if _, err := NewClient(conn, Hello{}); err == nil {
		t.Fatal("expected handshake failure without token")
	}
}

func TestStreamingPipe(t *testing.T) {
	conn := startPipeServer(t, &testHandler{})
	client, err := NewClient(conn, Hello{Token: "secret"})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if err := client.OpenStream("pipe", map[string]any{"x": 1}); err != nil {
		t.Fatalf("open stream: %v", err)
	}
	payload := strings.Repeat("hello-stream-", 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = client.Conn().Write([]byte(payload))
	}()
	got := make([]byte, len(payload))
	_ = client.Conn().SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := readFull(client.Conn(), got); err != nil {
		t.Fatalf("read echoed payload: %v", err)
	}
	if string(got) != payload {
		t.Fatalf("payload mismatch: %d bytes", len(got))
	}
	<-done
}

func readFull(r net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func TestFrameLimits(t *testing.T) {
	var buf strings.Builder
	if err := WriteFrame(&buf, map[string]string{"a": "b"}); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	var out map[string]string
	if err := ReadFrame(strings.NewReader(buf.String()), &out); err != nil {
		t.Fatalf("read frame: %v", err)
	}
	if out["a"] != "b" {
		t.Fatalf("unexpected payload: %+v", out)
	}
	// A zero-length frame must be rejected.
	if err := ReadFrame(strings.NewReader("\x00\x00\x00\x00"), &out); err == nil {
		t.Fatal("expected zero-length frame rejection")
	}
}
