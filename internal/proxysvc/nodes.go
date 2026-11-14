package proxysvc

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"wailstools/internal/miproxy"
)

// NodeStore persists user-defined nodes.
type NodeStore struct {
	mu    sync.Mutex
	path  string
	items []miproxy.Node
}

// OpenNodes loads the custom node file (creating the directory if needed).
func OpenNodes(dir string) (*NodeStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &NodeStore{path: filepath.Join(dir, "custom-nodes.json")}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &s.items); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *NodeStore) persist() error {
	data, err := json.MarshalIndent(s.items, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// List returns the custom nodes sorted by name.
func (s *NodeStore) List() []miproxy.Node {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]miproxy.Node(nil), s.items...)
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// Save inserts or updates a custom node.
func (s *NodeStore) Save(n miproxy.Node) (miproxy.Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(n.Server) == "" || n.Port <= 0 {
		return miproxy.Node{}, errors.New("服务器地址与端口必填")
	}
	switch n.Protocol {
	case miproxy.ProtoVMess:
		if n.UUID == "" {
			return miproxy.Node{}, errors.New("VMess 需要 UUID")
		}
	case miproxy.ProtoVLESS:
		if n.UUID == "" {
			return miproxy.Node{}, errors.New("VLESS 需要 UUID")
		}
	case miproxy.ProtoTrojan:
		if n.Password == "" {
			return miproxy.Node{}, errors.New("Trojan 需要密码")
		}
		// Trojan 协议本身基于 TLS，避免用户漏勾。
		n.TLS = true
	case miproxy.ProtoSS:
		if n.Method == "" || n.Password == "" {
			return miproxy.Node{}, errors.New("Shadowsocks 需要加密方式与密码")
		}
	case miproxy.ProtoHTTP, miproxy.ProtoSOCKS5, miproxy.ProtoDirect:
	default:
		return miproxy.Node{}, errors.New("不支持的协议")
	}
	if n.ID == "" {
		n.ID = "custom-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	if n.Name == "" {
		n.Name = string(n.Protocol) + " " + n.Server
	}
	if n.Network == "" {
		n.Network = miproxy.NetTCP
	}
	replaced := false
	for i := range s.items {
		if s.items[i].ID == n.ID {
			s.items[i] = n
			replaced = true
			break
		}
	}
	if !replaced {
		s.items = append(s.items, n)
	}
	if err := s.persist(); err != nil {
		return miproxy.Node{}, err
	}
	return n, nil
}

// Delete removes a custom node.
func (s *NodeStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.items[:0]
	found := false
	for _, item := range s.items {
		if item.ID == id {
			found = true
			continue
		}
		out = append(out, item)
	}
	if !found {
		return errors.New("节点不存在")
	}
	s.items = out
	return s.persist()
}

// ImportLinks adds nodes parsed from a pasted share link (one per line).
func (s *NodeStore) ImportLinks(text string) ([]miproxy.Node, error) {
	parsed := miproxy.ParseSubscription(text)
	if len(parsed) == 0 {
		return nil, errors.New("未解析到可用的节点链接")
	}
	out := make([]miproxy.Node, 0, len(parsed))
	for _, n := range parsed {
		saved, err := s.Save(n)
		if err != nil {
			continue
		}
		out = append(out, saved)
	}
	if len(out) == 0 {
		return nil, errors.New("没有可导入的节点")
	}
	return out, nil
}
