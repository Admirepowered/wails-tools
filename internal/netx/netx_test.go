package netx

import (
	"context"
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

func startServer(t *testing.T, port int) (*Listener, *tlsConfigPair) {
	t.Helper()
	cert, err := generateTestCert()
	if err != nil {
		t.Fatalf("cert: %v", err)
	}
	lis, err := Listen(context.Background(), ListenConfig{
		TCPAddr:   net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		UDPAddr:   net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		TLS:       ServerTLS(cert),
		EnableUDP: true,
		EnableTCP: true,
	})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = lis.Close() })
	return lis, &tlsConfigPair{cert: cert}
}

type tlsConfigPair struct{ cert interface{} }

// echoServer accepts sessions and echoes one stream per session.
func echoServer(t *testing.T, lis *Listener) {
	t.Helper()
	go func() {
		for {
			sess, err := lis.Accept(context.Background())
			if err != nil {
				return
			}
			go func(sess Session) {
				defer sess.Close()
				for {
					stream, err := sess.Accept(context.Background())
					if err != nil {
						return
					}
					go func(c net.Conn) {
						defer c.Close()
						_, _ = io.Copy(c, c)
					}(stream)
				}
			}(sess)
		}
	}()
}

func roundTrip(t *testing.T, sess Session) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := sess.Open(ctx)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer stream.Close()
	payload := []byte("ping-through-multiplexed-stream")
	if _, err := stream.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(stream, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("echo mismatch: %q", got)
	}
}

func TestDialPrefersUDP(t *testing.T) {
	port := freePort(t)
	lis, _ := startServer(t, port)
	echoServer(t, lis)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cert, err := generateTestCert()
	if err != nil {
		t.Fatalf("cert: %v", err)
	}
	sess, report, err := Dial(ctx, DialConfig{
		Host: "127.0.0.1",
		Port: port,
		TLS:  ClientTLS("", func(string) {}),
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer sess.Close()
	if report.Transport != TransportUDP {
		t.Fatalf("expected UDP transport, got %q (udpErr=%s tcpErr=%s)", report.Transport, report.UDPError, report.TCPError)
	}
	if sess.Transport() != TransportUDP {
		t.Fatalf("session reports %q", sess.Transport())
	}
	roundTrip(t, sess)
	_ = cert
}

func TestDialFallsBackToTCP(t *testing.T) {
	port := freePort(t)
	lis, _ := startServer(t, port)
	echoServer(t, lis)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sess, report, err := Dial(ctx, DialConfig{
		Host:       "127.0.0.1",
		Port:       port,
		TLS:        ClientTLS("", nil),
		DisableUDP: true,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer sess.Close()
	if report.Transport != TransportTCP {
		t.Fatalf("expected TCP transport, got %q", report.Transport)
	}
	roundTrip(t, sess)
}

func TestDialFallsBackWhenUDPUnreachable(t *testing.T) {
	port := freePort(t)
	// Listen on TCP only: UDP dial must fail and the client must fall back.
	cert, err := generateTestCert()
	if err != nil {
		t.Fatalf("cert: %v", err)
	}
	lis, err := Listen(context.Background(), ListenConfig{
		TCPAddr:   net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		TLS:       ServerTLS(cert),
		EnableTCP: true,
	})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer lis.Close()
	echoServer(t, lis)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sess, report, err := Dial(ctx, DialConfig{
		Host:             "127.0.0.1",
		Port:             port,
		TLS:              ClientTLS("", nil),
		HandshakeTimeout: 1 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer sess.Close()
	if report.Transport != TransportTCP {
		t.Fatalf("expected TCP fallback, got %q", report.Transport)
	}
	if report.UDPError == "" {
		t.Fatal("expected the UDP attempt to be recorded as failed")
	}
	roundTrip(t, sess)
}

func TestCertificatePinRejected(t *testing.T) {
	port := freePort(t)
	lis, _ := startServer(t, port)
	echoServer(t, lis)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	wrong := "AA:BB:CC:DD"
	_, _, err := Dial(ctx, DialConfig{
		Host:             "127.0.0.1",
		Port:             port,
		TLS:              ClientTLS(wrong, nil),
		HandshakeTimeout: 2 * time.Second,
		DisableUDP:       true,
	})
	if err == nil {
		t.Fatal("expected certificate pin mismatch to fail")
	}
}
