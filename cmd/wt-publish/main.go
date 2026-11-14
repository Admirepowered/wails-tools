// Command wt-publish uploads a built client to the WailsTools sync server so
// other clients can auto-update from it (hash based).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"wailstools/internal/serverlink"
	"wailstools/internal/settings"
	"wailstools/internal/version"
)

func main() {
	var (
		host      = flag.String("server", envOr("WT_SERVER", "s2.admirecn.de"), "sync server host")
		port      = flag.Int("port", 8642, "sync server port")
		user      = flag.String("user", envOr("WT_USER", ""), "admin username")
		pass      = flag.String("pass", envOr("WT_PASS", ""), "admin password")
		file      = flag.String("file", filepath.Join("build", "bin", "WailsTools.exe"), "client binary to publish")
		channel   = flag.String("channel", "stable", "release channel")
		notes     = flag.String("notes", "", "release notes")
		ver       = flag.String("version", version.Version, "version string")
		platform  = flag.String("platform", runtime.GOOS, "target platform")
		arch      = flag.String("arch", runtime.GOARCH, "target arch")
		mandatory = flag.Bool("mandatory", false, "mark the release as mandatory")
		noUDP     = flag.Bool("no-udp", false, "force TCP transport")
	)
	flag.Parse()

	if *user == "" || *pass == "" {
		fmt.Fprintln(os.Stderr, "error: -user and -pass (or WT_USER/WT_PASS) are required")
		os.Exit(2)
	}
	if _, err := os.Stat(*file); err != nil {
		fmt.Fprintf(os.Stderr, "error: binary not found: %s\n", *file)
		os.Exit(2)
	}
	abs, _ := filepath.Abs(*file)

	tmp, err := os.MkdirTemp("", "wt-publish")
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
		return nil
	}); err != nil {
		fatal(err)
	}

	link := serverlink.New(store, func(format string, args ...any) {
		fmt.Printf("  · "+format+"\n", args...)
	})
	defer link.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	status, err := link.Connect(ctx)
	if err != nil {
		fatal(fmt.Errorf("connect %s:%d: %w", *host, *port, err))
	}
	fmt.Printf("connected via %s to %s\n", strings.ToUpper(status.Transport), status.Address)

	if _, err := link.Login(ctx, *user, *pass); err != nil {
		fatal(fmt.Errorf("login: %w", err))
	}
	me, err := link.Me(ctx)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("logged in as %s (%s)\n", me.Username, me.Role)

	info, err := os.Stat(abs)
	if err != nil {
		fatal(err)
	}
	start := time.Now()
	res, err := link.UploadRelease(ctx, abs, map[string]any{
		"platform":  strings.ToLower(*platform),
		"arch":      strings.ToLower(*arch),
		"channel":   *channel,
		"version":   *ver,
		"filename":  filepath.Base(abs),
		"notes":     *notes,
		"mandatory": *mandatory,
	}, func(done, total int64) {
		if total > 0 {
			fmt.Printf("\r  uploading %d/%d KB (%.0f%%)", done/1024, total/1024, float64(done)/float64(total)*100)
		}
	})
	fmt.Println()
	if err != nil {
		fatal(fmt.Errorf("upload: %w", err))
	}
	fmt.Printf("published %s %s/%s v%s (%d bytes) in %s\n", filepath.Base(abs), *platform, *arch, *ver, info.Size(), time.Since(start).Round(time.Millisecond))
	if rel, ok := res["release"].(map[string]any); ok {
		fmt.Printf("release id: %v\nsha256: %v\n", rel["id"], rel["sha256"])
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
