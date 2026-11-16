package p2p

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strconv"
	"time"
)

// NATKind classifies how a device is reachable.
type NATKind string

const (
	// NATPublic means the socket address is unchanged by any NAT: inbound
	// connections work, so this device can act as the host.
	NATPublic NATKind = "public"
	// NATCone means both probe sockets map to the same public port
	// (endpoint-independent mapping): classic hole punching works.
	NATCone NATKind = "cone"
	// NATSymmetric means every socket maps to a different public port
	// (address/port-dependent mapping): the peer cannot predict the port, so
	// punching usually fails without port prediction or a relay.
	NATSymmetric NATKind = "symmetric"
	// NATUnknown is returned when the rendezvous server is unreachable.
	NATUnknown NATKind = "unknown"
)

// NATInfo is the result of a NAT type probe.
type NATInfo struct {
	Kind NATKind `json:"kind"`
	// Local and Public are the two socket addresses that were compared.
	Local   string `json:"local,omitempty"`
	Public  string `json:"public,omitempty"`
	Ports   []int  `json:"ports,omitempty"`
	Message string `json:"message,omitempty"`
}

// DetectNAT probes the rendezvous server from two different UDP sockets. If the
// observed public ports are identical the NAT maps endpoints independently
// (cone); if they differ the mapping is per-destination (symmetric).
func DetectNAT(ctx context.Context, rendezvous string, timeout time.Duration, logf func(string, ...any)) NATInfo {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	raddr, err := net.ResolveUDPAddr("udp4", rendezvous)
	if err != nil {
		return NATInfo{Kind: NATUnknown, Message: "rendezvous 地址无效: " + err.Error()}
	}
	id := fmt.Sprintf("nat:%d", time.Now().UnixNano())
	observed := make([]*net.UDPAddr, 0, 2)
	locals := make([]string, 0, 2)
	for _, role := range []string{"n1", "n2"} {
		addr, local, err := natProbeOnce(ctx, raddr, id, role, timeout)
		if err != nil {
			return NATInfo{Kind: NATUnknown, Message: err.Error()}
		}
		observed = append(observed, addr)
		locals = append(locals, local)
	}
	ports := []int{observed[0].Port, observed[1].Port}
	sort.Ints(ports)
	info := NATInfo{Ports: ports, Local: locals[0], Public: observed[0].String()}
	switch {
	case isOwnEndpoint(observed[0]) && observed[0].Port == localPort(locals[0]) &&
		isOwnEndpoint(observed[1]) && observed[1].Port == localPort(locals[1]):
		info.Kind = NATPublic
		info.Message = "本机直接可达（无 NAT），对端可以直接连进来，也能当主机"
	case observed[0].Port == observed[1].Port:
		info.Kind = NATCone
		info.Message = "锥形 NAT：两个端口的公网映射一致（端点无关映射），打洞成功率高"
	default:
		// Different public ports per socket: the mapping is destination
		// dependent, so the peer cannot predict where to send.
		info.Kind = NATSymmetric
		info.Message = fmt.Sprintf("对称型 NAT：两个端口的公网映射不同（%d / %d），打洞需要端口预测或中转", ports[0], ports[1])
	}
	logf("p2p: nat probe %s local=%v public=%v", info.Kind, locals, []string{observed[0].String(), observed[1].String()})
	return info
}

// natProbeOnce announces once from a fresh socket and returns what the server
// observed together with our local address.
func natProbeOnce(ctx context.Context, raddr *net.UDPAddr, id, role string, timeout time.Duration) (*net.UDPAddr, string, error) {
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return nil, "", err
	}
	defer conn.Close()
	payload, _ := json.Marshal(announce{Session: id, Role: role, Local: conn.LocalAddr().String()})
	buf := make([]byte, 2048)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		if _, err := conn.WriteToUDP(payload, raddr); err != nil {
			return nil, "", err
		}
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		if !from.IP.Equal(raddr.IP) || from.Port != raddr.Port {
			continue
		}
		var rep announceReply
		if json.Unmarshal(buf[:n], &rep) != nil || rep.You == "" {
			continue
		}
		addr, err := net.ResolveUDPAddr("udp4", rep.You)
		if err != nil {
			return nil, "", err
		}
		return addr, conn.LocalAddr().String(), nil
	}
	return nil, "", fmt.Errorf("NAT 探测超时：打洞服务不可达")
}

// localPort extracts the port from a host:port string (0 when invalid).
func localPort(addr string) int {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	p, err := strconv.Atoi(portStr)
	if err != nil {
		return 0
	}
	return p
}

// localInterfaceIPs returns the machine's own interface addresses. A socket
// bound to 0.0.0.0 reports "0.0.0.0:port" as its local address, so the only way
// to tell "no NAT" from "port-preserving NAT" is to check whether the observed
// public address is actually one of ours.
func localInterfaceIPs() map[string]bool {
	out := map[string]bool{}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP != nil {
			out[ipnet.IP.String()] = true
			if v4 := ipnet.IP.To4(); v4 != nil {
				out[v4.String()] = true
			}
		}
	}
	return out
}

// isOwnEndpoint reports whether the observed address belongs to this machine.
func isOwnEndpoint(observed *net.UDPAddr) bool {
	if observed == nil || observed.IP == nil {
		return false
	}
	ips := localInterfaceIPs()
	return ips[observed.IP.String()]
}

// EndpointIsLocal reports whether the address the server observed for our socket
// is really ours (i.e. there is no NAT in between).
func EndpointIsLocal(observed, localAddr string) bool {
	obs, err := net.ResolveUDPAddr("udp4", observed)
	if err != nil {
		return false
	}
	_, localPortStr, err := net.SplitHostPort(localAddr)
	if err != nil {
		return false
	}
	if fmt.Sprint(obs.Port) != localPortStr {
		return false
	}
	return isOwnEndpoint(obs)
}
