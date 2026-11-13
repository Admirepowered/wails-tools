// Command wt-server is the WailsTools sync server: user registration, per-user
// note/file storage and a TCP relay for reaching hosts behind the server.
// It prefers QUIC over UDP and falls back to TLS over TCP.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"wailstools/server/internal/server"
	"wailstools/server/internal/store"
)

func main() {
	var (
		dataDir   = flag.String("data", defaultDataDir(), "data directory")
		tcpAddr   = flag.String("tcp", ":8642", "TCP listen address (empty to disable)")
		udpAddr   = flag.String("udp", "", "UDP/QUIC listen address (defaults to -tcp)")
		noTCP     = flag.Bool("no-tcp", false, "disable the TCP fallback transport")
		noUDP     = flag.Bool("no-udp", false, "disable the UDP (QUIC) transport")
		allowReg  = flag.Bool("allow-register", true, "allow new user registration")
		relay     = flag.Bool("relay", true, "enable the TCP relay (SSH jump through this server)")
		quota     = flag.Int64("quota-gb", 10, "default per-user storage quota in GiB")
		tokenTTL  = flag.Duration("token-ttl", 30*24*time.Hour, "session token lifetime")
		createUsr = flag.String("create-user", "", "create a user and exit: username[:password]")
		listUsers = flag.Bool("list-users", false, "list registered users and exit")
	)
	flag.Parse()

	if *udpAddr == "" {
		*udpAddr = *tcpAddr
	}

	cfg := server.Config{
		DataDir:           *dataDir,
		TCPAddr:           *tcpAddr,
		UDPAddr:           *udpAddr,
		EnableTCP:         !*noTCP && *tcpAddr != "",
		EnableUDP:         !*noUDP && *udpAddr != "",
		AllowRegistration: *allowReg,
		RelayEnabled:      *relay,
		DefaultQuotaBytes: *quota * (1 << 30),
		TokenTTL:          *tokenTTL,
	}

	if *listUsers || *createUsr != "" {
		if err := adminCommand(*dataDir, *createUsr, *listUsers); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}

	srv, err := server.New(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	fmt.Println("shutting down...")
	shutdownCtx, sc := context.WithTimeout(context.Background(), 5*time.Second)
	defer sc()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		fmt.Fprintln(os.Stderr, "shutdown:", err)
	}
}

func defaultDataDir() string {
	if v := os.Getenv("WT_SERVER_DATA"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "data"
	}
	return home + string(os.PathSeparator) + ".wails-tools-server"
}

func adminCommand(dataDir, create string, list bool) error {
	db, err := store.Open(dataDir)
	if err != nil {
		return err
	}
	defer db.Close()
	if list {
		users, err := db.ListUsers()
		if err != nil {
			return err
		}
		if len(users) == 0 {
			fmt.Println("no users")
			return nil
		}
		for _, u := range users {
			fmt.Printf("%s\t%s\trole=%s\tcreated=%s\tquota=%.1f GiB\n",
				u.ID, u.Username, u.Role, u.CreatedAt, float64(u.QuotaBytes)/(1<<30))
		}
		return nil
	}
	user, pass, err := parseCreateSpec(create)
	if err != nil {
		return err
	}
	hash, err := hashPassword(pass)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	u := store.User{
		ID:           "u-" + randToken(12),
		Username:     user,
		PasswordHash: hash,
		DisplayName:  user,
		Role:         "admin",
		QuotaBytes:   100 << 30,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	created, err := db.CreateUser(u)
	if err != nil {
		return err
	}
	log.Printf("created user %s (%s)", created.Username, created.ID)
	return nil
}
