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
// Start is a synchronized punch time (unix milliseconds): both sides should
// begin blasting probes then, which is how n2n-style punching beats one-sided
// timing.
type Reply struct {
	OK    bool   `json:"ok"`
	Error string `json:"err,omitempty"`
	You   string `json:"you,omitempty"`
	Peer  string `json:"peer,omitempty"`
	Start int64  `json:"start,omitempty"`
	// YouPublic/PeerPublic are true when the announced address matches the
	// sender's own socket address, i.e. that side has no NAT (directly
	// reachable). In that case the punch can skip all probing.
	YouPublic  bool `json:"youPublic,omitempty"`
	PeerPublic bool `json:"peerPublic,omitempty"`
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
	punchAt time.Time
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
	requests map[string]*Request
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
	return &Hub{sessions: map[string]*Session{}, requests: map[string]*Request{}, logf: logf, closed: make(chan struct{})}
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
	if out.Peer != "" {
		// Both endpoints are known: agree on a moment (slightly in the future)
		// at which both peers start sending probes.
		if s.punchAt.IsZero() || time.Now().After(s.punchAt.Add(5*time.Second)) {
			s.punchAt = time.Now().Add(700 * time.Millisecond)
		}
		out.Start = s.punchAt.UnixMilli()
	}
	if role == "a" {
		out.YouPublic = isPublicEndpoint(s.addrA)
		out.PeerPublic = isPublicEndpoint(s.addrB)
	} else {
		out.YouPublic = isPublicEndpoint(s.addrB)
		out.PeerPublic = isPublicEndpoint(s.addrA)
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

// ---- punch requests (explicit accept) ------------------------------------

// requestTTL bounds how long an unaccepted invitation stays visible.
const requestTTL = 90 * time.Second

// Request is an invitation to punch towards a device. The target must accept
// before any endpoint is exchanged.
type Request struct {
	ID          string
	FromUser    string
	FromDevice  string
	FromNumber  string
	FromName    string
	ToUser      string
	ToDevice    string
	State       string // pending|accepted|declined|canceled
	SameAccount bool
	Session     string
	Secret      string
	Created     time.Time
}

// RequestInfo is the client-facing view of a request.
type RequestInfo struct {
	ID         string `json:"id"`
	FromNumber string `json:"fromNumber"`
	FromName   string `json:"fromName"`
	FromDevice string `json:"fromDevice"`
	ToDevice   string `json:"toDevice"`
	State      string `json:"state"`
	Session    string `json:"session,omitempty"`
	Secret     string `json:"secret,omitempty"`
	Created    string `json:"created"`
	// SameAccount is true when both devices belong to the same account; such
	// invitations are accepted automatically by the client.
	SameAccount bool `json:"sameAccount"`
}

func (r *Request) info() RequestInfo {
	return RequestInfo{
		ID: r.ID, FromNumber: r.FromNumber, FromName: r.FromName, FromDevice: r.FromDevice,
		ToDevice: r.ToDevice, State: r.State, Session: r.Session, Secret: r.Secret,
		Created: r.Created.UTC().Format(time.RFC3339), SameAccount: r.SameAccount,
	}
}

// roleCountsLocked returns how many sessions the device serves and initiates.
func (h *Hub) roleCountsLocked(device string, cutoff time.Time) (serving, initiating int) {
	for _, s := range h.sessions {
		if s.Created.Before(cutoff) {
			continue
		}
		if s.To == device {
			serving++
		}
		if s.From == device {
			initiating++
		}
	}
	return
}

// CreateRequest registers an invitation from device -> target device.
func (h *Hub) CreateRequest(fromUser, fromDevice, fromNumber, fromName, toUser, toDevice string) (RequestInfo, error) {
	if fromDevice == "" || toDevice == "" {
		return RequestInfo{}, errors.New("device ids are required")
	}
	if fromDevice == toDevice {
		return RequestInfo{}, errors.New("不能穿透自己")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if serving, _ := h.roleCountsLocked(fromDevice, time.Now().Add(-sessionTTL)); serving > 0 {
		return RequestInfo{}, errors.New("当前设备作为被穿透端正在被使用，不能发起穿透")
	}
	// Replace a stale invitation for the same pair.
	for id, r := range h.requests {
		if r.FromDevice == fromDevice && r.ToDevice == toDevice && r.State == "pending" {
			delete(h.requests, id)
		}
	}
	r := &Request{
		ID: "pr-" + randomID(6), FromUser: fromUser, FromDevice: fromDevice, FromNumber: fromNumber,
		FromName: fromName, ToUser: toUser, ToDevice: toDevice, State: "pending", Created: time.Now(),
		SameAccount: fromUser == toUser,
	}
	h.requests[r.ID] = r
	h.logf("p2p request %s: %s(#%s) -> %s", r.ID, fromDevice, fromNumber, toDevice)
	return r.info(), nil
}

// Requests lists pending invitations for a target device.
func (h *Hub) Requests(toUser, toDevice string) []RequestInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := []RequestInfo{}
	cutoff := time.Now().Add(-requestTTL)
	for id, r := range h.requests {
		if r.Created.Before(cutoff) {
			delete(h.requests, id)
			continue
		}
		if r.ToUser != toUser || r.ToDevice != toDevice || r.State != "pending" {
			continue
		}
		out = append(out, r.info())
	}
	return out
}

// Respond accepts or declines an invitation. Accepting creates the punch
// session shared with both devices.
func (h *Hub) Respond(toUser, toDevice, requestID string, accept bool) (RequestInfo, error) {
	h.mu.Lock()
	r, ok := h.requests[requestID]
	if !ok || r.ToUser != toUser || r.ToDevice != toDevice || time.Since(r.Created) > requestTTL {
		h.mu.Unlock()
		return RequestInfo{}, errors.New("请求不存在或已过期")
	}
	if r.State != "pending" {
		info := r.info()
		h.mu.Unlock()
		return info, nil
	}
	if !accept {
		r.State = "declined"
		info := r.info()
		h.mu.Unlock()
		return info, nil
	}
	if _, initiating := h.roleCountsLocked(toDevice, time.Now().Add(-sessionTTL)); initiating > 0 {
		h.mu.Unlock()
		return RequestInfo{}, errors.New("当前设备正在穿透其他设备，不能接受穿透")
	}
	h.mu.Unlock()

	info, err := h.CreateSession(r.ToUser, r.FromDevice, r.ToDevice)
	if err != nil {
		return RequestInfo{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	r.State = "accepted"
	r.Session = info.ID
	r.Secret = info.Secret
	return r.info(), nil
}

// RequestState lets either side poll for the invitation state.
func (h *Hub) RequestState(userID, device, requestID string) (RequestInfo, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r, ok := h.requests[requestID]
	if !ok {
		return RequestInfo{}, false
	}
	if r.FromUser != userID || r.FromDevice != device {
		if r.ToUser != userID || r.ToDevice != device {
			return RequestInfo{}, false
		}
	}
	if time.Since(r.Created) > requestTTL && r.State == "pending" {
		r.State = "expired"
	}
	return r.info(), true
}

// CancelRequest withdraws a pending invitation.
func (h *Hub) CancelRequest(userID, device, requestID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if r, ok := h.requests[requestID]; ok && r.FromUser == userID && r.FromDevice == device {
		r.State = "canceled"
	}
}

// privateNetworks lists the address ranges treated as "behind NAT".
var privateNetworks = func() []*net.IPNet {
	var out []*net.IPNet
	for _, cidr := range []string{
		"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10",
		"127.0.0.0/8", "169.254.0.0/16", "fc00::/7", "fe80::/10",
	} {
		if _, n, err := net.ParseCIDR(cidr); err == nil {
			out = append(out, n)
		}
	}
	return out
}()

// isPublicEndpoint reports whether addr is a globally reachable address, i.e.
// the peer announced its own socket address unchanged (no NAT in the path).
func isPublicEndpoint(addr *net.UDPAddr) bool {
	if addr == nil {
		return false
	}
	ip := addr.IP
	if ip == nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, n := range privateNetworks {
		if n.Contains(ip) {
			return false
		}
	}
	return true
}
