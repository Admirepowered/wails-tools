package sshmgr

import (
	"errors"
	"io"
	"sync"

	"golang.org/x/crypto/ssh"
)

// Terminal is an interactive PTY-backed shell.
type Terminal struct {
	mu     sync.Mutex
	sess   *ssh.Session
	stdin  io.WriteCloser
	closed bool
	id     string
}

// TerminalOptions configures the PTY.
type TerminalOptions struct {
	Cols    int
	Rows    int
	Term    string
	OnData  func([]byte)
	OnClose func(error)
}

// OpenTerminal starts an interactive shell on the given session.
func (m *Manager) OpenTerminal(sessionID string, opts TerminalOptions) (*Terminal, error) {
	c, ok := m.Client(sessionID)
	if !ok || c.SSH == nil {
		return nil, errors.New("ssh session not found")
	}
	sess, err := c.SSH.NewSession()
	if err != nil {
		return nil, err
	}
	if opts.Cols <= 0 {
		opts.Cols = 120
	}
	if opts.Rows <= 0 {
		opts.Rows = 32
	}
	if opts.Term == "" {
		opts.Term = "xterm-256color"
	}
	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := sess.RequestPty(opts.Term, opts.Rows, opts.Cols, modes); err != nil {
		_ = sess.Close()
		return nil, err
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		_ = sess.Close()
		return nil, err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		_ = sess.Close()
		return nil, err
	}
	stderr, err := sess.StderrPipe()
	if err != nil {
		_ = sess.Close()
		return nil, err
	}
	if err := sess.Shell(); err != nil {
		_ = sess.Close()
		return nil, err
	}
	t := &Terminal{sess: sess, stdin: stdin, id: sessionID}
	pump := func(r io.Reader) {
		buf := make([]byte, 32*1024)
		for {
			n, err := r.Read(buf)
			if n > 0 && opts.OnData != nil {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				opts.OnData(chunk)
			}
			if err != nil {
				return
			}
		}
	}
	go pump(stdout)
	go pump(stderr)
	go func() {
		err := sess.Wait()
		t.mu.Lock()
		t.closed = true
		t.mu.Unlock()
		if opts.OnClose != nil {
			opts.OnClose(err)
		}
	}()
	return t, nil
}

// Write sends keystrokes to the shell.
func (t *Terminal) Write(p []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return errors.New("terminal closed")
	}
	_, err := t.stdin.Write(p)
	return err
}

// Resize updates the terminal window size.
func (t *Terminal) Resize(cols, rows int) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return errors.New("terminal closed")
	}
	return t.sess.WindowChange(rows, cols)
}

// Close terminates the shell.
func (t *Terminal) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	stdin := t.stdin
	sess := t.sess
	t.mu.Unlock()
	_ = stdin.Close()
	return sess.Close()
}
