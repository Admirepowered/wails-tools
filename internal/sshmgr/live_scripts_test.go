package sshmgr

import (
	"context"
	"os"
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// TestLiveScripts runs the two sampling scripts against a real host when
// WT_SSH_ADDR is set. Skipped otherwise.
func TestLiveScripts(t *testing.T) {
	addr := ""
	if v := os.Getenv("WT_SSH_ADDR"); v != "" {
		addr = v
	}
	if addr == "" {
		t.Skip("WT_SSH_ADDR not set")
	}
	client, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            os.Getenv("WT_SSH_USER"),
		Auth:            []ssh.AuthMethod{ssh.Password(os.Getenv("WT_SSH_PASS"))},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	run := func(label, script string) {
		sess, err := client.NewSession()
		if err != nil {
			t.Fatalf("%s session: %v", label, err)
		}
		defer sess.Close()
		out, err := sess.Output(script)
		if err != nil {
			t.Logf("%s err: %v", label, err)
		}
		t.Logf("%s output:\n%s", label, string(out))
	}
	_ = context.Background()
	_ = io.Discard
	_ = net.IPv4len
	run("static", staticScript)
	run("dynamic", dynamicScript)
}

