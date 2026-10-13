// Command church-calendar runs the church calendar and room-booking app.
//
//	church-calendar                       start the web server
//	church-calendar create-user NAME [--role member|scheduler|admin | --admin]
//	                                      create a user (prompts for password)
//	church-calendar reset-password NAME   set a new password for a user
//	church-calendar local-login on|off    break-glass: password sign-in even with single sign-on on
//	church-calendar connection clear ID   break-glass: forget a connection's saved settings
//	church-calendar backup FILE           write a consistent copy of the database
//
// Flags such as -db go before the command. Configuration comes from flags
// or environment variables (see README.md).
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // lets TZ work in minimal containers with no zoneinfo

	"golang.org/x/term"

	"github.com/tylergoza/church-calendar/internal/server"
	"github.com/tylergoza/church-calendar/internal/store"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	addr := flag.String("addr", env("ADDR", ":8120"), "listen address (env ADDR)")
	dbPath := flag.String("db", env("DB_PATH", "data/calendar.db"), "SQLite database path (env DB_PATH)")
	dev := flag.Bool("dev", env("DEV", "") == "1", "serve templates/static from ./web and reload on each request (env DEV=1)")
	trustProxy := flag.Bool("trust-proxy", env("TRUST_PROXY", "") == "1", "trust X-Forwarded-* headers from a reverse proxy (env TRUST_PROXY=1)")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	// Check the command before opening the database, so a typo or a
	// misplaced flag doesn't quietly create an empty database somewhere.
	args := flag.Args()
	if err := checkCommand(args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}

	st, err := store.Open(*dbPath)
	if err != nil {
		logger.Error("open database", "path", *dbPath, "err", err,
			"hint", fmt.Sprintf("make sure the folder exists and user id %d can write to it", os.Getuid()))
		os.Exit(1)
	}
	defer st.Close()

	if len(args) > 0 {
		if err := runCommand(st, args); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}

	srv, err := server.New(server.Config{Dev: *dev, TrustProxy: *trustProxy}, st, logger)
	if err != nil {
		logger.Error("init server", "err", err)
		os.Exit(1)
	}

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Periodically clear out expired sessions.
	go func() {
		t := time.NewTicker(6 * time.Hour)
		defer t.Stop()
		for {
			st.PurgeExpiredSessions()
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	// Keep repeating bookings made six months ahead: once now, then every
	// few hours.
	go func() {
		t := time.NewTicker(6 * time.Hour)
		defer t.Stop()
		for {
			srv.TopUpSeries()
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	// Follow the maintenance tracker's places, when it's connected: once
	// now, then every 15 minutes.
	go func() {
		t := time.NewTicker(15 * time.Minute)
		defer t.Stop()
		for {
			srv.SyncTracker(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	go func() {
		logger.Info("listening", "addr", *addr, "db", *dbPath, "dev", *dev, "tz", time.Local.String())
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	httpSrv.Shutdown(shutdownCtx)
}

var commandUsage = map[string]string{
	"create-user":    "create-user USERNAME [--role member|scheduler|admin] [--admin]",
	"reset-password": "reset-password USERNAME",
	"local-login":    "local-login on|off",
	"connection":     "connection clear " + strings.Join(server.ConnectionIDs(), "|"),
	"backup":         "backup DEST_FILE",
}

// checkCommand validates a command line before anything touches the
// database. Flags belong before the command; after it they'd be ignored.
func checkCommand(args []string) error {
	if len(args) == 0 {
		return nil
	}
	usage, ok := commandUsage[args[0]]
	if !ok {
		return fmt.Errorf("unknown command %q (commands: create-user, reset-password, local-login, connection, backup)", args[0])
	}
	rest := args[1:]
	if args[0] == "create-user" {
		if _, rest2, err := parseCreateUser(rest); err != nil {
			return fmt.Errorf("%w (usage: %s)", err, usage)
		} else {
			rest = rest2
		}
	}
	if len(rest) < 1 {
		return errors.New("usage: " + usage)
	}
	if args[0] == "local-login" && rest[0] != "on" && rest[0] != "off" {
		return errors.New("usage: " + usage)
	}
	if args[0] == "connection" && (len(rest) != 2 || rest[0] != "clear" || !slices.Contains(server.ConnectionIDs(), rest[1])) {
		return errors.New("usage: " + usage)
	}
	for _, a := range rest {
		if strings.HasPrefix(a, "-") {
			return fmt.Errorf("%s: flags like %s go before the command, e.g. church-calendar -db /path/to/calendar.db %s", args[0], a, usage)
		}
	}
	return nil
}

func runCommand(st *store.Store, args []string) error {
	switch args[0] {
	case "create-user":
		// --admin and --role may come before or after the username.
		role, names, err := parseCreateUser(args[1:])
		if err != nil {
			return err
		}
		username := names[0]
		if n, _ := st.CountUsers(); n == 0 {
			role = store.RoleAdmin // the first user is always an admin
		}
		pw, err := promptPassword()
		if err != nil {
			return err
		}
		if _, err := st.CreateUser(username, "", pw, role); err != nil {
			return err
		}
		fmt.Printf("Created user %q (role: %s)\n", username, role)
	case "reset-password":
		var id int64
		if err := st.DB.QueryRow(`SELECT id FROM users WHERE username = ?`, args[1]).Scan(&id); err != nil {
			return fmt.Errorf("no user %q", args[1])
		}
		pw, err := promptPassword()
		if err != nil {
			return err
		}
		if err := st.SetPassword(id, pw); err != nil {
			return err
		}
		// Break-glass: make sure they can sign in, even if their access
		// was removed in User Management.
		if err := st.SetActive(id, true); err != nil {
			return err
		}
		fmt.Printf("Password updated for %q (and their account is active here)\n", args[1])
	case "local-login":
		// Break-glass for when User Management is down: brings the
		// password form back on /login. Users who came from single
		// sign-on have no password here; give one with reset-password.
		if err := st.SetSetting("local_login", args[1]); err != nil {
			return err
		}
		if args[1] == "on" {
			fmt.Println("Local login is on: /login shows the password form, even with single sign-on on.")
			fmt.Println("Set a password with reset-password USERNAME if needed. Turn it off again with local-login off.")
		} else {
			fmt.Println("Local login is off: with single sign-on on, /login goes to User Management.")
		}
	case "connection":
		// Break-glass for when a bad saved setting is the problem: the
		// connection goes back to the environment, or off.
		if err := server.ClearConnection(st, args[2]); err != nil {
			return err
		}
		fmt.Printf("Saved settings for the %s connection removed. It goes back to the server's values, or off.\n", args[2])
		if args[2] == "sso" {
			fmt.Println("Sign-in falls back to local passwords unless SSO_URL is set on the server; use reset-password if needed.")
		}
	case "backup":
		if _, err := os.Stat(args[1]); err == nil {
			return fmt.Errorf("%s already exists", args[1])
		}
		if err := st.Backup(context.Background(), args[1]); err != nil {
			return err
		}
		fmt.Println("Backup written to", args[1])
	default:
		return fmt.Errorf("unknown command %q (commands: create-user, reset-password, local-login, connection, backup)", args[0])
	}
	return nil
}

// parseCreateUser picks --admin and --role ROLE out of create-user's
// arguments (default role helper) and returns the arguments left over.
func parseCreateUser(args []string) (role string, rest []string, err error) {
	role = store.RoleMember
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--admin":
			role = store.RoleAdmin
		case a == "--role" || strings.HasPrefix(a, "--role="):
			v := strings.TrimPrefix(a, "--role=")
			if a == "--role" {
				if i+1 >= len(args) {
					return "", nil, errors.New("--role needs member, scheduler or admin")
				}
				i++
				v = args[i]
			}
			if !slices.Contains(store.Roles, v) {
				return "", nil, fmt.Errorf("unknown role %q (use member, scheduler or admin)", v)
			}
			role = v
		default:
			rest = append(rest, a)
		}
	}
	return role, rest, nil
}

func promptPassword() (string, error) {
	read := func(prompt string) (string, error) {
		fmt.Fprint(os.Stderr, prompt)
		if term.IsTerminal(int(os.Stdin.Fd())) {
			b, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			return string(b), err
		}
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		return strings.TrimRight(line, "\r\n"), err
	}
	pw, err := read("Password: ")
	if err != nil {
		return "", err
	}
	if len(pw) < 10 {
		return "", errors.New("password must be at least 10 characters")
	}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		confirm, err := read("Confirm password: ")
		if err != nil {
			return "", err
		}
		if confirm != pw {
			return "", errors.New("passwords do not match")
		}
	}
	return pw, nil
}
