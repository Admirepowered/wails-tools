// Package p2p implements the UDP rendezvous used by the NAT hole punching
// between two clients. It only exchanges public endpoints and a shared secret;
// the actual data path is peer to peer.
package p2p

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net"
	"strings"
	"sync"
	"time"
)

// sessionTTL bounds how long a rendezvous session stays usable.
const sessionTTL = 2 * time.Minute

// Announce is the datagram a client sends from its punch socket.
type Announce struct {
	Session string `json:"s"`
	Role    string `json:"r"` // "a" = initiator/dialer, "b" = responder/listener
}

// Reply tells the client its own public endpoint and, once known, the peer's.
type Reply struct {
	OK    bool   `json:"ok"`
	Error string `json:"err,omitempty"`
	You   string `json:"you,omitempty"`
	Peer  string `json:"peer,omitempty"`
}

// Session is one pending hole-punch attempt.
type Session struct {
	ID      string
	UserID  string
	From    string // device id that initiated
	To      string // device id that should answer
	Secret  string
	Created time.Time
	addrA   *net.UDPAddr
	addrB   *net.UDPAddr
}

// Info is the client-facing view of a session.
type Info struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	To      string `json:"to"`
	Secret  string `json:"secret"`
	Created string `json:"created"`
}

// Hub owns the UDP socket and the session table.
type Hub struct {
	mu       sync.Mutex
	sessions map[string]*Session
	conn     *net.UDPConn
	logf     func(string, ...any)
	closed   chan struct{}
	wg       sync.WaitGroup
}

// NewHub creates an idle hub.
func NewHub(logf func(string, ...any)) *Hub {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Hub{sessions: map[string]*Session{}, logf: logf, closed: make(chan struct{})}
}

// Listen binds the rendezvous socket and starts serving.
func (h *Hub) Listen(addr string) (string, error) {
	if addr == "" {
		addr = ":8643"
	}
	udpAddr, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return "", err
	}
	conn, err := net.ListenUDP("udp4", udpAddr)
	if err != nil {
		return "", err
	}
	h.mu.Lock()
	h.conn = conn
	h.mu.Unlock()
	h.wg.Add(2)
	go h.readLoop(conn)
	go h.pruneLoop()
	return conn.LocalAddr().String(), nil
}

// Close stops the hub.
func (h *Hub) Close() {
	h.mu.Lock()
	if h.conn != nil {
		_ = h.conn.Close()
		h.conn = nil
	}
	h.mu.Unlock()
	close(h.closed)
	h.wg.Wait()
}

// CreateSession registers a punch attempt and returns its id and secret. Both
// devices must belong to the same user.
func (h *Hub) CreateSession(userID, from, to string) (Info, error) {
	from = strings.TrimSpace(from)
	to = strings.TrimSpace(to)
	if from == "" || to == "" {
		return Info{}, errors.New("from and to device ids are required")
	}
	if from == to {
		return Info{}, errors.New("cannot punch to the same device")
	}
	id := randomID(9)
	secret := randomID(24)
	sess := &Session{
		ID: id, UserID: userID, From: from, To: to, Secret: secret, Created: time.Now(),
	}
	h.mu.Lock()
	// One live session per device pair keeps the table small.
	for key, s := range h.sessions {
		if s.UserID == userID && ((s.From == from && s.To == to) || (s.From == to && s.To == from)) {
			delete(h.sessions, key)
		}
	}
	h.sessions[id] = sess
	h.mu.Unlock()
	h.logf("p2p session %s: %s -> %s", id, from, to)
	return Info{ID: id, From: from, To: to, Secret: secret, Created: sess.Created.UTC().Format(time.RFC3339)}, nil
}

// Pending returns unexpired sessions waiting for the given device to answer.
func (h *Hub) Pending(userID, device string) []Info {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := []Info{}
	cutoff := time.Now().Add(-sessionTTL)
	for _, s := range h.sessions {
		if s.UserID != userID || s.To != device || s.Created.Before(cutoff) {
			continue
		}
		out = append(out, Info{ID: s.ID, From: s.From, To: s.To, Secret: s.Secret, Created: s.Created.UTC().Format(time.RFC3339)})
	}
	return out
}

// Lookup returns a session for a device, validating ownership.
func (h *Hub) Lookup(userID, sessionID, device string) (Info, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[sessionID]
	if !ok || s.UserID != userID || time.Since(s.Created) > sessionTTL {
		return Info{}, false
	}
	if s.From != device && s.To != device {
		return Info{}, false
	}
	return Info{ID: s.ID, From: s.From, To: s.To, Secret: s.Secret, Created: s.Created.UTC().Format(time.RFC3339)}, true
}

// Drop removes a session (client cancelled or finished).
func (h *Hub) Drop(userID, sessionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s, ok := h.sessions[sessionID]; ok && s.UserID == userID {
		delete(h.sessions, sessionID)
	}
}

func (h *Hub) readLoop(conn *net.UDPConn) {
	defer h.wg.Done()
	buf := make([]byte, 2048)
	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-h.closed:
				return
			default:
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return
		}
		var req Announce
		if json.Unmarshal(buf[:n], &req) != nil {
			continue
		}
		reply := h.handle(req, addr)
		data, _ := json.Marshal(reply)
		_, _ = conn.WriteToUDP(data, addr)
	}
}

func (h *Hub) handle(req Announce, addr *net.UDPAddr) Reply {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[req.Session]
	if !ok || time.Since(s.Created) > sessionTTL {
		return Reply{Error: "unknown_or_expired_session"}
	}
	role := strings.ToLower(strings.TrimSpace(req.Role))
	switch role {
	case "a":
		s.addrA = addr
	case "b":
		s.addrB = addr
	default:
		return Reply{Error: "role must be a or b"}
	}
	out := Reply{OK: true, You: addr.String()}
	if role == "a" && s.addrB != nil {
		out.Peer = s.addrB.String()
	}
	if role == "b" && s.addrA != nil {
		out.Peer = s.addrA.String()
	}
	return out
}

func (h *Hub) pruneLoop() {
	defer h.wg.Done()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-h.closed:
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-sessionTTL)
			h.mu.Lock()
			for id, s := range h.sessions {
				if s.Created.Before(cutoff) {
					delete(h.sessions, id)
				}
			}
			h.mu.Unlock()
		}
	}
}

func randomID(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return base64.RawURLEncoding.EncodeToString([]byte(time.Now().String()))[:2*n]
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

var _ = log.Printf
