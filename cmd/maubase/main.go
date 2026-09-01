// Command maubase is the single-binary server: it opens the database, runs
// migrations, and serves the HTTP API. `maubase serve` starts it — every
// path (MAUBASE_DB_PATH, MAUBASE_MIGRATIONS_DIR, etc.) resolves relative
// to the current directory by default, so running it from inside a
// specific project directory serves that project, not some other one a
// globally-installed binary happened to run from before. `maubase init`
// scaffolds a brand new deployment (see init.go, spec/project-init.md);
// `maubase migrate up`/`maubase migrate status` manage a deployment's own
// application-schema migrations without starting the server (see
// migrate.go, spec/migrations-cli.md). Bare `maubase`, or an unknown
// command, prints usage rather than guessing what you meant — see
// spec/cli.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Ucok23/maubase/internal/adminui"
	"github.com/Ucok23/maubase/internal/audit"
	"github.com/Ucok23/maubase/internal/auth"
	"github.com/Ucok23/maubase/internal/config"
	"github.com/Ucok23/maubase/internal/db"
	"github.com/Ucok23/maubase/internal/email"
	"github.com/Ucok23/maubase/internal/oauth"
	"github.com/Ucok23/maubase/internal/ownerauth"
	"github.com/Ucok23/maubase/internal/realtime"
	"github.com/Ucok23/maubase/internal/restapi"
	"github.com/Ucok23/maubase/internal/server"
	"github.com/Ucok23/maubase/internal/social"
	"github.com/Ucok23/maubase/internal/storage"
)

func main() {
	// Bare `maubase` — no subcommand at all — prints usage rather than
	// starting the server: every other action here (init, migrate ...)
	// is an explicit verb, so "type nothing, get a long-running server"
	// was the odd one out, and silently binding a port when someone
	// just ran the command to see what it does is exactly the kind of
	// surprise a CLI shouldn't produce.
	if len(os.Args) < 2 {
		printUsage()
		return
	}
	switch os.Args[1] {
	case "serve":
		if err := run(); err != nil {
			log.Fatal(err)
		}
	case "init":
		if err := runInit(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
	case "migrate":
		if err := runMigrate(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
	case "help", "-h", "--help":
		printUsage()
	default:
		printUsage()
		log.Fatalf("unknown command %q", os.Args[1])
	}
}

func run() error {
	cfg := config.Load()

	sqlDB, err := db.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	if err := db.Migrate(sqlDB); err != nil {
		return err
	}
	if err := db.MigrateDir(sqlDB, cfg.MigrationsDir); err != nil {
		return fmt.Errorf("apply application migrations: %w", err)
	}

	authSvc := auth.NewService(sqlDB)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	oauthSvc, err := oauth.NewServer(ctx, sqlDB, authSvc, cfg.Issuer)
	if err != nil {
		return fmt.Errorf("init oauth server: %w", err)
	}

	ownerSvc := ownerauth.NewService(sqlDB)
	if err := bootstrapOwner(ctx, ownerSvc, cfg); err != nil {
		return fmt.Errorf("bootstrap owner: %w", err)
	}

	broker := realtime.NewBroker()
	if cfg.RedisURL != "" {
		relay, err := realtime.NewRedisRelay(cfg.RedisURL, "maubase:realtime")
		if err != nil {
			return fmt.Errorf("init redis relay: %w", err)
		}
		broker = realtime.NewBrokerWithRelay(ctx, relay)
	}

	registry, err := restapi.Discover(ctx, sqlDB)
	if err != nil {
		return fmt.Errorf("discover rest collections: %w", err)
	}
	restapiSvc := restapi.NewServer(sqlDB, registry, oauthSvc, broker, cfg.MaxRequestBodyBytes)

	storageBackend, err := storage.NewLocalBackend(cfg.StorageDir)
	if err != nil {
		return fmt.Errorf("init storage backend: %w", err)
	}
	storageSvc := storage.NewServer(sqlDB, storageBackend, oauthSvc, cfg.MaxUploadBytes)

	realtimeSvc := realtime.NewServer(broker, oauthSvc)

	auditLog := audit.New(sqlDB)

	adminuiSvc := adminui.NewServer(sqlDB, authSvc, ownerSvc, restapiSvc, storageSvc, oauthSvc, auditLog, cfg.LoginRateLimit, cfg.LoginRateWindow)

	// Falls back to a sender that fails loudly rather than silently
	// no-op'ing when Resend isn't configured — see email.NoopSender.
	var emailSender email.Sender = email.NoopSender{}
	if cfg.ResendAPIKey != "" && cfg.EmailFrom != "" {
		emailSender = email.NewResendSender(cfg.ResendAPIKey, cfg.EmailFrom)
	}

	// Each provider only goes in the map if its client id/secret are
	// both set — an unconfigured provider 404s rather than the server
	// refusing to start over it (see spec/social-login.md).
	socialProviders := map[string]social.Provider{}
	if cfg.GoogleClientID != "" && cfg.GoogleClientSecret != "" {
		socialProviders["google"] = social.Google(cfg.GoogleClientID, cfg.GoogleClientSecret, cfg.Issuer+"/api/auth/social/google/callback")
	}
	if cfg.GitHubClientID != "" && cfg.GitHubClientSecret != "" {
		socialProviders["github"] = social.GitHub(cfg.GitHubClientID, cfg.GitHubClientSecret, cfg.Issuer+"/api/auth/social/github/callback")
	}

	srv := server.New(authSvc, oauthSvc, ownerSvc, restapiSvc, storageSvc, realtimeSvc, adminuiSvc, auditLog, cfg.LoginRateLimit, cfg.LoginRateWindow, emailSender, cfg.PasswordResetURL, socialProviders, cfg.SocialLoginRedirectURL)

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go runSessionJanitor(ctx, authSvc, ownerSvc, cfg.SessionCleanupInterval)

	go func() {
		<-ctx.Done()
		log.Println("shutting down...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown error: %v", err)
		}
	}()

	log.Printf("listening on %s (db: %s)", cfg.Addr, cfg.DBPath)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// runSessionJanitor periodically purges expired rows from the customer-
// and owner-plane session tables (see auth.Service/ownerauth.Service's
// PurgeExpiredSessions), plus expired or already-used password-reset
// tokens (PurgeExpiredResetTokens), until ctx is canceled. None of these
// rows ever do anything once expired/used — this is storage hygiene, not
// a correctness requirement, so a missed or slow tick is harmless. Also
// reachable on demand via POST /admin/maintenance/purge-sessions.
func runSessionJanitor(ctx context.Context, authSvc *auth.Service, ownerSvc *ownerauth.Service, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n, err := authSvc.PurgeExpiredSessions(ctx); err != nil {
				log.Printf("session janitor: purge sessions: %v", err)
			} else if n > 0 {
				log.Printf("session janitor: purged %d expired session(s)", n)
			}
			if n, err := ownerSvc.PurgeExpiredSessions(ctx); err != nil {
				log.Printf("session janitor: purge owner sessions: %v", err)
			} else if n > 0 {
				log.Printf("session janitor: purged %d expired owner session(s)", n)
			}
			if n, err := authSvc.PurgeExpiredResetTokens(ctx); err != nil {
				log.Printf("session janitor: purge reset tokens: %v", err)
			} else if n > 0 {
				log.Printf("session janitor: purged %d expired/used reset token(s)", n)
			}
		}
	}
}

// bootstrapOwner creates the first owner-plane account from
// MAUBASE_BOOTSTRAP_OWNER_EMAIL/_PASSWORD if both are set. It's a no-op once
// any owner account exists (including on every later restart), and does
// nothing at all if the env vars aren't set — the only way to get an
// owner account is this, or an existing owner creating one via the API.
func bootstrapOwner(ctx context.Context, svc *ownerauth.Service, cfg config.Config) error {
	if cfg.BootstrapOwnerEmail == "" || cfg.BootstrapOwnerPassword == "" {
		return nil
	}
	owner, err := svc.Bootstrap(ctx, cfg.BootstrapOwnerEmail, cfg.BootstrapOwnerPassword)
	if errors.Is(err, ownerauth.ErrAlreadyBootstrapped) {
		return nil
	}
	if err != nil {
		return err
	}
	log.Printf("bootstrapped first owner: %s", owner.Email)
	return nil
}

func printUsage() {
	fmt.Fprint(os.Stderr, `maubase: a self-hostable backend

Usage:
  maubase serve                Start the server
  maubase init [dir]           Scaffold a brand new deployment (migrations/, .env.example, .gitignore)
  maubase migrate new <name>   Scaffold the next-numbered application migration file
  maubase migrate up           Apply pending application migrations
  maubase migrate down [n]     Revert the last n applied migrations (default 1)
  maubase migrate redo [n]     Revert then reapply the last n applied migrations (default 1)
  maubase migrate to <ver>     Move to exactly <ver> (a filename or numeric prefix), forward or back
  maubase migrate status       List application migrations and whether each is applied
  maubase migrate diff         Report tables the database has that no applied migration explains (or vice versa)
  maubase help                 Show this message

Every path below (and MAUBASE_DB_PATH/MAUBASE_MIGRATIONS_DIR generally)
resolves relative to the current directory — run "maubase serve" from
inside a specific project to serve that project, not whichever one a
globally-installed binary last happened to run from.

A migration file's SQL goes under a "-- +migrate Up" marker; an optional
"-- +migrate Down" section (see "maubase migrate new"'s template) is what
"migrate down"/"redo"/"to" run to revert it — a migration with no Down
section can't be reverted.

"migrate diff" catches schema drift from the admin UI's create-table
form or SQL Studio, which change the live schema without ever touching
migrations/ — it only reports (exits non-zero if it finds anything), it
never modifies the database or writes a migration for you.

Flags for "migrate" subcommands:
  -dir string   application migrations directory (default: $MAUBASE_MIGRATIONS_DIR, or migrations)
  -db string    path to the SQLite database file, "up"/"down"/"redo"/"to"/"status"/"diff" only (default: $MAUBASE_DB_PATH, or data/maubase.db)
`)
}
