package miproxy

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
)

func TestParseVLESSRealityLink(t *testing.T) {
	raw := "vless://79c66798-1237-40e8-932f-5dce99a439df@142.202.242.179:443?type=tcp&encryption=none&security=reality&flow=xtls-rprx-vision&fp=ios&sni=tv.apple.com&pbk=gkl6bqL44xulia53I8gd_BtiVRzU1rsIu4WeffywLBY&sid=78f5a5a5#node"
	node, ok := ParseLink(raw)
	if !ok {
		t.Fatal("link not parsed")
	}
	if !node.Reality || !node.TLS {
		t.Fatalf("reality flags: reality=%v tls=%v", node.Reality, node.TLS)
	}
	if node.Flow != "xtls-rprx-vision" || node.SNI != "tv.apple.com" || node.Fingerprint != "ios" {
		t.Fatalf("params: %+v", node)
	}
	if node.RealityPublicKey == "" || node.RealityShortID != "78f5a5a5" {
		t.Fatalf("reality credentials: %+v", node)
	}
	if _, err := newRealityOptions(node); err != nil {
		t.Fatalf("reality options: %v", err)
	}
}

func TestRealityOptionsRejectBadInput(t *testing.T) {
	if _, err := newRealityOptions(Node{Reality: true}); err == nil {
		t.Fatal("missing pbk must fail")
	}
	if _, err := newRealityOptions(Node{Reality: true, RealityPublicKey: "not-base64"}); err == nil {
		t.Fatal("bad pbk must fail")
	}
	if _, err := newRealityOptions(Node{Reality: true, RealityPublicKey: "gkl6bqL44xulia53I8gd_BtiVRzU1rsIu4WeffywLBY", RealityShortID: "zzzz"}); err == nil {
		t.Fatal("bad shortId must fail")
	}
}

// The Vision writer must prefix the first frame with the uuid and keep the
// padding/content layout readable by Xray.
func TestVisionWriteFraming(t *testing.T) {
	a, b := net.Pipe()
	uuid := make([]byte, 16)
	_, _ = rand.Read(uuid)
	vc := newVisionConn(a, a, uuid, nil)
	payload := []byte("hello-vision")
	go func() {
		_, _ = vc.Write(payload)
		_, _ = vc.Write([]byte("second"))
		_ = a.Close()
	}()

	readFrame := func(withUUID bool) []byte {
		hdr := make([]byte, 21)
		if !withUUID {
			hdr = make([]byte, 5)
		}
		if _, err := io.ReadFull(b, hdr); err != nil {
			t.Fatalf("header: %v", err)
		}
		if withUUID {
			if !bytes.Equal(hdr[:16], uuid) {
				t.Fatalf("uuid mismatch")
			}
			hdr = hdr[16:]
		}
		if hdr[0] != visionContinue {
			t.Fatalf("command = %d", hdr[0])
		}
		contentLen := int(binary.BigEndian.Uint16(hdr[1:3]))
		paddingLen := int(binary.BigEndian.Uint16(hdr[3:5]))
		content := make([]byte, contentLen)
		if _, err := io.ReadFull(b, content); err != nil {
			t.Fatalf("content: %v", err)
		}
		if paddingLen > 0 {
			if _, err := io.ReadFull(b, make([]byte, paddingLen)); err != nil {
				t.Fatalf("padding: %v", err)
			}
		}
		return content
	}

	if got := readFrame(true); !bytes.Equal(got, payload) {
		t.Fatalf("first frame = %q", got)
	}
	if got := readFrame(false); string(got) != "second" {
		t.Fatalf("second frame = %q", got)
	}
}

// Reading frames (including the direct switch) must yield the payload only.
func TestVisionReadFraming(t *testing.T) {
	a, b := net.Pipe()
	uuid := make([]byte, 16)
	_, _ = rand.Read(uuid)
	vc := newVisionConn(a, a, uuid, nil)

	go func() {
		write := func(withUUID bool, cmd byte, payload []byte, padding int) {
			buf := make([]byte, 0, 32+len(payload)+padding)
			if withUUID {
				buf = append(buf, uuid...)
			}
			buf = append(buf, cmd, byte(len(payload)>>8), byte(len(payload)))
			buf = append(buf, byte(padding>>8), byte(padding))
			buf = append(buf, payload...)
			buf = append(buf, make([]byte, padding)...)
			_, _ = b.Write(buf)
		}
		write(true, visionContinue, []byte("first"), 7)
		write(false, visionContinue, []byte("second"), 0)
		write(false, visionDirect, []byte("direct"), 0)
		_, _ = b.Write([]byte("raw-bytes"))
		_ = b.Close()
	}()

	got := make([]byte, 0, 64)
	buf := make([]byte, 32)
	for {
		n, err := vc.Read(buf)
		if n > 0 {
			got = append(got, buf[:n]...)
		}
		if err != nil {
			break
		}
	}
	want := "firstseconddirectraw-bytes"
	if string(got) != want {
		t.Fatalf("decoded = %q, want %q", got, want)
	}
}

func TestEncodeVLESSAddons(t *testing.T) {
	if got := encodeVLESSAddons(""); got != nil {
		t.Fatalf("empty flow should add nothing: %v", got)
	}
	flow := "xtls-rprx-vision"
	got := encodeVLESSAddons(flow)
	if len(got) != 2+len(flow) || got[0] != 0x0a || int(got[1]) != len(flow) || !strings.HasSuffix(string(got), flow) {
		t.Fatalf("unexpected addons: %v", got)
	}
}
