package githubx

import (
	"net"
	"sync"
	"time"
)

// fragmentConn splits the first bytes written on a connection into small TCP
// segments. crypto/tls writes the ClientHello in a single Write call, so this
// scatters the SNI extension across several packets and defeats DPI that looks
// for a complete ClientHello in one segment.
type fragmentConn struct {
	net.Conn
	mu        sync.Mutex
	size      int
	remaining int
	delay     time.Duration
}

// fragmentBudget bounds how many bytes are fragmented; after that the stream is
// passed through untouched so large uploads keep their throughput.
const fragmentBudget = 2048

func newFragmentConn(conn net.Conn, size int) *fragmentConn {
	if size <= 0 {
		size = 4
	}
	return &fragmentConn{Conn: conn, size: size, remaining: fragmentBudget, delay: 700 * time.Microsecond}
}

func (c *fragmentConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.remaining <= 0 || len(p) <= c.size {
		c.remaining -= len(p)
		return c.Conn.Write(p)
	}
	total := 0
	for total < len(p) {
		chunk := c.size
		if remainingBudget := c.remaining; remainingBudget <= 0 {
			chunk = len(p) - total
		} else if chunk > remainingBudget {
			chunk = remainingBudget
		}
		if chunk > len(p)-total {
			chunk = len(p) - total
		}
		n, err := c.Conn.Write(p[total : total+chunk])
		total += n
		c.remaining -= n
		if err != nil {
			return total, err
		}
		if c.remaining > 0 && c.delay > 0 {
			time.Sleep(c.delay)
		}
	}
	return total, nil
}
