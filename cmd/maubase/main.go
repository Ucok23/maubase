// Command maubase is the single-binary server: it opens the database, runs
// migrations, and serves the HTTP API.
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

	"maubase/internal/adminui"
	"maubase/internal/audit"
	"maubase/internal/auth"
	"maubase/internal/config"
	"maubase/internal/db"
	"maubase/internal/email"
	"maubase/internal/oauth"
	"maubase/internal/ownerauth"
	"maubase/internal/realtime"
	"maubase/internal/restapi"
	"maubase/internal/server"
	"maubase/internal/social"
	"maubase/internal/storage"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
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
	restapiSvc := restapi.NewServer(sqlDB, registry, oauthSvc, broker)

	storageBackend, err := storage.NewLocalBackend(cfg.StorageDir)
	if err != nil {
		return fmt.Errorf("init storage backend: %w", err)
	}
	storageSvc := storage.NewServer(sqlDB, storageBackend, oauthSvc, cfg.MaxUploadBytes)

	realtimeSvc := realtime.NewServer(broker, oauthSvc)

	auditLog := audit.New(sqlDB)

	adminuiSvc := adminui.NewServer(sqlDB, authSvc, ownerSvc, restapiSvc, storageSvc, oauthSvc, auditLog)

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
// PurgeExpiredSessions) until ctx is canceled. Expired sessions already
// fail validation on their own, so a missed or slow tick is harmless —
// this is storage hygiene, not a correctness requirement. Also reachable
// on demand via POST /admin/maintenance/purge-sessions.
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
