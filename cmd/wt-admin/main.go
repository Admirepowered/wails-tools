// Command wt-admin administers a WailsTools sync server over the same wire
// protocol the client uses: list/create/delete users, change roles, manage
// device bindings and flip the public-registration switch.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"wailstools/internal/serverlink"
	"wailstools/internal/settings"
	"wailstools/internal/sniproxy"
)

func main() {
	var (
		host        = flag.String("server", envOr("WT_SERVER", "s2.admirecn.de"), "sync server host")
		port        = flag.Int("port", 8642, "sync server port")
		user        = flag.String("user", envOr("WT_USER", ""), "admin username")
		pass        = flag.String("pass", envOr("WT_PASS", ""), "admin password")
		list        = flag.Bool("list", false, "list users (default action)")
		create      = flag.String("create", "", "create user: name:password[:admin|user]")
		del         = flag.String("delete", "", "delete user by name")
		setRole     = flag.String("set-role", "", "change role: name:admin|user")
		devices     = flag.String("devices", "", "list devices of a user")
		rmDevice    = flag.String("remove-device", "", "remove device: user:deviceID")
		allow       = flag.String("registration", "", "public registration: on|off")
		noUDP       = flag.Bool("no-udp", false, "force TCP transport")
		showAdmin   = flag.Bool("settings", false, "show server admin settings")
		vault       = flag.Bool("vault", false, "list the encrypted secret bundles stored for this account")
		subs        = flag.Bool("subs", false, "show the stored subscription/proxy-node document summary")
		sniRules    = flag.Bool("sni-rules", false, "show the server-published default SNI rules")
		sniUpload   = flag.String("sni-rules-upload", "", "publish an SNI rule file as the server default (admin)")
		sniDefs     = flag.Bool("sni-rules-defaults", false, "publish the client's compiled-in default SNI rules")
		offlineAdd  = flag.String("offline-add", "", "queue a magnet link for server-side offline download")
		offlineList = flag.Bool("offline-list", false, "list server-side offline downloads")
		deviceID    = flag.String("device-id", "wt-admin-cli", "device identity reported on login")
	)
	flag.Parse()

	if *user == "" || *pass == "" {
		fmt.Fprintln(os.Stderr, "error: -user and -pass (or WT_USER/WT_PASS) are required")
		os.Exit(2)
	}

	tmp, err := os.MkdirTemp("", "wt-admin")
	if err != nil {
		fatal(err)
	}
	defer os.RemoveAll(tmp)

	store, err := settings.NewStore(filepath.Join(tmp, "settings.json"))
	if err != nil {
		fatal(err)
	}
	if _, err := store.Update(func(s *settings.Settings) error {
		s.Server.Host = *host
		s.Server.Port = *port
		s.Server.DisableUDP = *noUDP
		// Reuse one stable identity so the CLI never consumes device slots.
		s.Server.DeviceID = *deviceID
		return nil
	}); err != nil {
		fatal(err)
	}

	link := serverlink.New(store, nil)
	link.SetMachineID("")
	defer link.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := link.Connect(ctx); err != nil {
		fatal(fmt.Errorf("connect %s:%d: %w", *host, *port, err))
	}
	if _, err := link.Login(ctx, *user, *pass); err != nil {
		fatal(fmt.Errorf("login as %s: %w", *user, err))
	}
	// The CLI is not a real device: drop the binding so it never eats a slot.
	defer func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer ccancel()
		_ = link.Call(cctx, "admin.devices.selfRemove", map[string]any{"deviceId": *deviceID}, nil)
	}()

	call := func(method string, body any, out any) {
		if err := link.Call(ctx, method, body, out); err != nil {
			fatal(fmt.Errorf("%s: %w", method, err))
		}
	}

	users := func() []adminUser {
		var out struct {
			Users []adminUser `json:"users"`
		}
		call("admin.users.list", nil, &out)
		return out.Users
	}

	byName := func(name string) adminUser {
		for _, u := range users() {
			if strings.EqualFold(u.Username, name) {
				return u
			}
		}
		fatal(fmt.Errorf("user %q not found", name))
		return adminUser{}
	}

	switch {
	case *create != "":
		parts := strings.SplitN(*create, ":", 3)
		if len(parts) < 2 {
			fatal(fmt.Errorf("-create expects name:password[:role]"))
		}
		role := "user"
		if len(parts) == 3 && parts[2] != "" {
			role = parts[2]
		}
		var out map[string]any
		call("admin.users.create", map[string]any{
			"username": parts[0], "password": parts[1], "role": role,
		}, &out)
		fmt.Printf("created %s (%s)\n", parts[0], role)

	case *del != "":
		u := byName(*del)
		call("admin.users.delete", map[string]any{"id": u.ID}, nil)
		fmt.Printf("deleted %s\n", u.Username)

	case *setRole != "":
		parts := strings.SplitN(*setRole, ":", 2)
		if len(parts) != 2 {
			fatal(fmt.Errorf("-set-role expects name:role"))
		}
		u := byName(parts[0])
		call("admin.users.setRole", map[string]any{"id": u.ID, "role": parts[1]}, nil)
		fmt.Printf("%s is now %s\n", u.Username, parts[1])

	case *devices != "":
		u := byName(*devices)
		printDevices(call, u)

	case *rmDevice != "":
		parts := strings.SplitN(*rmDevice, ":", 2)
		if len(parts) != 2 {
			fatal(fmt.Errorf("-remove-device expects user:deviceID"))
		}
		u := byName(parts[0])
		call("admin.devices.remove", map[string]any{"userId": u.ID, "deviceId": parts[1]}, nil)
		fmt.Printf("removed device %s from %s\n", parts[1], u.Username)

	case *allow != "":
		on := strings.EqualFold(*allow, "on") || strings.EqualFold(*allow, "true") || *allow == "1"
		var out map[string]any
		call("admin.settings.set", map[string]any{"allowRegister": on}, &out)
		fmt.Printf("public registration: %v\n", on)

	case *showAdmin:
		var out map[string]any
		call("admin.settings.get", nil, &out)
		fmt.Printf("allowRegister=%v maxDevices=%v\n", out["allowRegister"], out["maxDevices"])

	case *vault:
		var out struct {
			Blobs []struct {
				Kind      string `json:"kind"`
				UpdatedAt string `json:"updatedAt"`
				Size      int    `json:"size"`
			} `json:"blobs"`
		}
		call("vault.list", nil, &out)
		if len(out.Blobs) == 0 {
			fmt.Println("no encrypted secret bundles stored")
			return
		}
		for _, b := range out.Blobs {
			fmt.Printf("%-12s %8d bytes  %s\n", b.Kind, b.Size, b.UpdatedAt)
		}

	case *offlineList:
		var out struct {
			Tasks []struct {
				ID     string `json:"id"`
				Name   string `json:"name"`
				Status string `json:"status"`
				Total  int64  `json:"total"`
				Done   int64  `json:"done"`
				Error  string `json:"error"`
			} `json:"tasks"`
			UsedBytes  int64 `json:"usedBytes"`
			QuotaBytes int64 `json:"quotaBytes"`
		}
		call("offline.list", nil, &out)
		fmt.Printf("quota %d/%d bytes, %d tasks\n", out.UsedBytes, out.QuotaBytes, len(out.Tasks))
		for _, t := range out.Tasks {
			fmt.Printf("  %s %-10s %s %d/%d %s\n", t.ID, t.Status, t.Name, t.Done, t.Total, t.Error)
		}
		return

	case *offlineAdd != "":
		var out map[string]any
		call("offline.add", map[string]any{"magnet": *offlineAdd}, &out)
		fmt.Println("queued offline download")
		return

	case *sniRules:
		var out struct {
			Found     bool   `json:"found"`
			Rules     string `json:"rules"`
			UpdatedAt string `json:"updatedAt"`
			UpdatedBy string `json:"updatedBy"`
		}
		call("sni.rules.get", nil, &out)
		if !out.Found {
			fmt.Println("no server-published SNI rules")
			return
		}
		lines := strings.Count(out.Rules, "\n") + 1
		fmt.Printf("published %s by %s: %d lines, %d bytes\n", out.UpdatedAt, out.UpdatedBy, lines, len(out.Rules))
		return

	case *sniUpload != "", *sniDefs:
		var text string
		if *sniDefs {
			text = sniproxy.DefaultRules
		} else {
			data, err := os.ReadFile(*sniUpload)
			if err != nil {
				fatal(err)
			}
			text = string(data)
		}
		var out map[string]any
		call("sni.rules.put", map[string]any{"rules": text}, &out)
		fmt.Printf("published SNI rules (%d bytes) at %v\n", len(text), out["updatedAt"])
		return

	case *subs:
		var out struct {
			Found         bool            `json:"found"`
			Subscriptions json.RawMessage `json:"subscriptions"`
			UpdatedAt     int64           `json:"updatedAt"`
		}
		call("subs.get", nil, &out)
		if !out.Found || len(out.Subscriptions) == 0 {
			fmt.Println("no subscription document stored")
			return
		}
		trimmed := strings.TrimSpace(string(out.Subscriptions))
		if strings.HasPrefix(trimmed, "[") {
			var legacy []json.RawMessage
			_ = json.Unmarshal(out.Subscriptions, &legacy)
			fmt.Printf("legacy v1 document: %d sources (%d bytes)\n", len(legacy), len(out.Subscriptions))
			return
		}
		var doc struct {
			Version int               `json:"version"`
			Sources []json.RawMessage `json:"sources"`
			Nodes   []json.RawMessage `json:"nodes"`
		}
		if err := json.Unmarshal(out.Subscriptions, &doc); err != nil {
			fatal(err)
		}
		fmt.Printf("v%d document: %d sources, %d custom nodes (%d bytes)\n",
			doc.Version, len(doc.Sources), len(doc.Nodes), len(out.Subscriptions))

	default:
		_ = list
		listUsers(call, users)
	}
}

type adminUser struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	Role        string `json:"role"`
	Disabled    bool   `json:"disabled"`
	Devices     int    `json:"devices"`
	QuotaBytes  int64  `json:"quotaBytes"`
	CreatedAt   string `json:"createdAt"`
	LastLoginAt string `json:"lastLoginAt"`
}

type adminDevice struct {
	Number   string `json:"number"`
	ID       string `json:"id"`
	Name     string `json:"name"`
	LastSeen string `json:"lastSeen"`
}

func listUsers(call func(string, any, any), users func() []adminUser) {
	list := users()
	fmt.Printf("%-4s %-16s %-6s %-8s %-20s %s\n", "#", "USER", "ROLE", "DEVICES", "LAST LOGIN", "CREATED")
	for i, u := range list {
		last := u.LastLoginAt
		if last == "" {
			last = "-"
		}
		created := u.CreatedAt
		if len(created) >= 10 {
			created = created[:10]
		}
		fmt.Printf("%-4d %-16s %-6s %-8d %-20s %s\n", i+1, u.Username, u.Role, u.Devices, last, created)
	}
	fmt.Printf("total %d user(s)\n", len(list))
}

func printDevices(call func(string, any, any), u adminUser) {
	var out struct {
		Devices []adminDevice `json:"devices"`
	}
	call("admin.devices.list", map[string]any{"userId": u.ID}, &out)
	fmt.Printf("devices of %s (%d):\n", u.Username, len(out.Devices))
	for _, d := range out.Devices {
		fmt.Printf("  %-40s #%-9s %-24s %s\n", d.ID, d.Number, d.Name, d.LastSeen)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}
