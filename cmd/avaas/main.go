package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"golang.org/x/sys/unix"

	"github.com/benachilds/avaas/internal/api"
	"github.com/benachilds/avaas/internal/auth"
	"github.com/benachilds/avaas/internal/clamav"
	"github.com/benachilds/avaas/internal/config"
	logstore "github.com/benachilds/avaas/internal/logging"
	"github.com/benachilds/avaas/internal/notify"
	"github.com/benachilds/avaas/internal/store"
	"github.com/benachilds/avaas/internal/worker"
)

var version = "dev"
var commit = "unknown"
var buildDate = "unknown"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "avaas:", err)
		os.Exit(1)
	}
}
func run(args []string) error {
	global := flag.NewFlagSet("avaas", flag.ContinueOnError)
	configPath := global.String("config", "", "path to KEY=VALUE configuration file")
	if err := global.Parse(args); err != nil {
		return err
	}
	args = global.Args()
	if len(args) == 0 {
		return usage()
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch args[0] {
	case "version":
		if len(args) > 1 && args[1] == "--json" {
			return json.NewEncoder(os.Stdout).Encode(map[string]string{"version": version, "commit": commit, "build_date": buildDate})
		}
		fmt.Printf("AVaaS %s (commit %s, built %s)\n", version, commit, buildDate)
		return nil
	case "migrate":
		migrateFlags := flag.NewFlagSet("migrate", flag.ContinueOnError)
		migrateJSON := migrateFlags.Bool("json", false, "emit JSON")
		if err := migrateFlags.Parse(args[1:]); err != nil {
			return err
		}
		s, err := openStore(ctx, cfg)
		if err != nil {
			return err
		}
		defer func() { _ = s.Close() }()
		if *migrateJSON {
			return json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": true, "message": "database migrations applied"})
		}
		fmt.Println("database migrations applied")
		return nil
	case "keygen":
		return keygen(ctx, cfg, args[1:])
	case "keys":
		return keys(ctx, cfg, args[1:])
	case "stats":
		return stats(ctx, cfg, args[1:])
	case "serve":
		return serve(ctx, cfg)
	case "worker":
		return work(ctx, cfg)
	case "doctor":
		return doctor(ctx, cfg, args[1:])
	default:
		return usage()
	}
}
func usage() error {
	fmt.Fprintln(os.Stderr, `AVaaS (Anti-Virus as a Service)
Usage: avaas [--config FILE] <serve|worker|migrate|keygen|keys|stats|doctor|version>`)
	return errors.New("a valid command is required")
}
func openStore(ctx context.Context, c config.Config) (*store.Store, error) {
	s, err := store.Open(c.DatabasePath)
	if err != nil {
		return nil, err
	}
	if err = s.Migrate(ctx); err != nil {
		return nil, errors.Join(err, s.Close())
	}
	return s, nil
}
func cryptoConfig(c config.Config) ([]byte, *auth.URLCipher, error) {
	pepper, err := config.ReadSecret(c.PepperFile, 32)
	if err != nil {
		return nil, nil, fmt.Errorf("read API-key pepper: %w", err)
	}
	key, err := config.ReadSecret(c.EncryptionKeyFile, 32)
	if err != nil {
		return nil, nil, fmt.Errorf("read URL encryption key: %w", err)
	}
	cipher, err := auth.NewURLCipher(key)
	return pepper, cipher, err
}
func serve(ctx context.Context, c config.Config) error {
	s, err := openStore(ctx, c)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	pepper, cipher, err := cryptoConfig(c)
	if err != nil {
		return err
	}
	log, err := logstore.New("api", c.LogDir, c.LogMaxBytes, c.LogMaxFiles)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: c.ListenAddress, Handler: api.New(c, s, pepper, cipher, log).Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024}
	errs := make(chan error, 1)
	go func() {
		log.Info("AVaaS API listening", "address", c.ListenAddress, "version", version)
		errs <- server.ListenAndServe()
	}()
	select {
	case err = <-errs:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}
func work(ctx context.Context, c config.Config) error {
	s, err := openStore(ctx, c)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	_, cipher, err := cryptoConfig(c)
	if err != nil {
		return err
	}
	log, err := logstore.New("worker", c.LogDir, c.LogMaxBytes, c.LogMaxFiles)
	if err != nil {
		return err
	}
	pub, err := notify.New(c)
	if err != nil {
		return err
	}
	defer pub.Close()
	w, err := worker.New(c, s, cipher, pub, log)
	if err != nil {
		return err
	}
	log.Info("AVaaS worker started", "concurrency", c.WorkerConcurrency, "valkey_enabled", c.ValkeyEnabled)
	err = w.Run(ctx)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
func stats(ctx context.Context, c config.Config, args []string) error {
	if len(args) == 0 || args[0] != "reset" {
		return errors.New("usage: avaas stats reset --yes [--json]")
	}
	fs := flag.NewFlagSet("stats reset", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "confirm resetting aggregate statistics")
	jsonOut := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if !*yes {
		return errors.New("stats reset requires --yes; jobs and API keys are preserved")
	}
	s, err := openStore(ctx, c)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	result, err := store.ResetStats(ctx, s)
	if err != nil {
		return err
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	fmt.Printf("statistics reset (buckets=%d, notification_history=%d, pending_notification_failures=%d); jobs and API keys were preserved\n", result.BucketsDeleted, result.NotificationHistoryDeleted, result.PendingNotificationFailuresReset)
	return nil
}
func keygen(ctx context.Context, c config.Config, args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	description := fs.String("description", "", "human-readable key description")
	scopeText := fs.String("scopes", "scan,admin", "comma-separated scopes")
	jsonOut := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	s, err := openStore(ctx, c)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	pepper, err := config.ReadSecret(c.PepperFile, 32)
	if err != nil {
		return err
	}
	scopes := cleanScopes(*scopeText)
	if len(scopes) == 0 {
		return errors.New("at least one valid scope (scan or admin) is required")
	}
	k, token, err := store.GenerateKey(ctx, s, pepper, *description, scopes)
	if err != nil {
		return err
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"key": k, "token": token, "warning": "the token will not be shown again"})
	}
	fmt.Printf("Key ID: %s\nDescription: %s\nScopes: %s\nToken (shown once): %s\n", k.ID, k.Description, strings.Join(k.Scopes, ","), token)
	return nil
}
func keys(ctx context.Context, c config.Config, args []string) error {
	if len(args) == 0 {
		return errors.New("keys requires list or revoke")
	}
	s, err := openStore(ctx, c)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("keys list", flag.ContinueOnError)
		all := fs.Bool("include-revoked", false, "include revoked keys")
		jsonOut := fs.Bool("json", false, "emit JSON")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		ks, err := store.ListKeys(ctx, s, *all)
		if err != nil {
			return err
		}
		if *jsonOut {
			return json.NewEncoder(os.Stdout).Encode(ks)
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		if _, err := fmt.Fprintln(w, "ID\tDESCRIPTION\tSCOPES\tCREATED\tLAST USED\tREVOKED"); err != nil {
			return err
		}
		for _, k := range ks {
			if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", k.ID, k.Description, strings.Join(k.Scopes, ","), k.CreatedAt.Format(time.RFC3339), formatTime(k.LastUsedAt), formatTime(k.RevokedAt)); err != nil {
				return err
			}
		}
		return w.Flush()
	case "revoke":
		fs := flag.NewFlagSet("keys revoke", flag.ContinueOnError)
		reason := fs.String("reason", "", "revocation reason")
		yes := fs.Bool("yes", false, "confirm potentially revoking the last admin key")
		jsonOut := fs.Bool("json", false, "emit JSON")
		revokeArgs := args[1:]
		target := ""
		if len(revokeArgs) > 0 && !strings.HasPrefix(revokeArgs[0], "-") {
			target = revokeArgs[0]
			revokeArgs = revokeArgs[1:]
		}
		if err := fs.Parse(revokeArgs); err != nil {
			return err
		}
		if target == "" && fs.NArg() == 1 {
			target = fs.Arg(0)
		}
		if target == "" || fs.NArg() > 1 {
			return errors.New("usage: avaas keys revoke <key-id> [--reason TEXT] [--yes]")
		}
		changed, err := store.RevokeKey(ctx, s, target, *reason, *yes)
		if err != nil {
			return err
		}
		if changed {
			if *jsonOut {
				return json.NewEncoder(os.Stdout).Encode(map[string]any{"id": target, "revoked": true})
			}
			fmt.Println("key revoked")
		} else {
			if *jsonOut {
				return json.NewEncoder(os.Stdout).Encode(map[string]any{"id": target, "revoked": true, "already_revoked": true})
			}
			fmt.Println("key was already revoked")
		}
		return nil
	default:
		return errors.New("keys requires list or revoke")
	}
}
func doctor(ctx context.Context, c config.Config, args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	checks := map[string]string{}
	s, err := openStore(ctx, c)
	if err != nil {
		checks["database"] = err.Error()
	} else {
		checks["database"] = "ok"
		defer func() { _ = s.Close() }()
	}
	if _, _, err := cryptoConfig(c); err != nil {
		checks["secrets"] = err.Error()
	} else {
		checks["secrets"] = "ok"
	}
	cl := clamav.Client{Socket: c.ClamdSocket, Timeout: 5 * time.Second}
	clamVersion, clamErr := cl.Version(ctx)
	if clamErr != nil {
		checks["clamav"] = clamErr.Error()
	} else {
		checks["clamav"] = "ok"
		info := clamav.ParseVersion(clamVersion)
		if info.DefinitionDate != nil && info.DefinitionDate.Before(time.Now().Add(-c.DefinitionMaxAge)) {
			checks["definitions"] = "stale"
		} else {
			checks["definitions"] = "ok"
		}
	}
	var fsStat unix.Statfs_t
	if err := unix.Statfs(c.SpoolDir, &fsStat); err != nil {
		checks["spool"] = err.Error()
	} else if int64(fsStat.Bavail)*int64(fsStat.Bsize) < c.MaxFileBytes {
		checks["spool"] = "insufficient free space"
	} else {
		checks["spool"] = "ok"
	}
	if c.ValkeyEnabled {
		p, err := notify.New(c)
		if err != nil {
			checks["valkey"] = err.Error()
		} else {
			pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			if err = p.Ping(pingCtx); err != nil {
				checks["valkey"] = err.Error()
			} else {
				checks["valkey"] = "ok"
			}
			cancel()
			p.Close()
		}
	} else {
		checks["valkey"] = "disabled"
	}
	ok := true
	for _, v := range checks {
		if v != "ok" && v != "disabled" {
			ok = false
		}
	}
	if *jsonOut {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": ok, "checks": checks})
	} else {
		for k, v := range checks {
			fmt.Printf("%-10s %s\n", k, v)
		}
	}
	if !ok {
		return errors.New("one or more checks failed")
	}
	return nil
}
func cleanScopes(v string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range strings.Split(v, ",") {
		s = strings.TrimSpace(s)
		if (s == "scan" || s == "admin") && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
func formatTime(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Format(time.RFC3339)
}

// Keep slog linked in minimal CLI-only builds for consistent toolchain diagnostics.
var _ = slog.LevelInfo
