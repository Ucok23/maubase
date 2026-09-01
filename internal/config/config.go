// Package config loads runtime configuration from environment variables,
// with sane defaults for local/single-VPS deployment.
package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	// Addr is the HTTP listen address, e.g. ":8080".
	Addr string
	// DBPath is the path to the SQLite database file.
	DBPath string
	// Issuer is this server's own public base URL (e.g. "https://example.com"),
	// used as the OAuth/JWT issuer and in discovery metadata. Must match what
	// clients (including MCP clients) will actually use to reach this server.
	Issuer string

	// BootstrapOwnerEmail/Password, if both set, create the first owner-
	// plane account (role "owner") on startup — but only when no owner
	// account exists yet; every run after the first is a no-op. This is
	// the one supported way to get an owner account without already being
	// signed in as one.
	BootstrapOwnerEmail    string
	BootstrapOwnerPassword string

	// MigrationsDir is where a deployment's own application-schema .sql
	// files live (as opposed to maubase's own embedded migrations). Missing
	// entirely is fine — see db.MigrateDir.
	MigrationsDir string

	// LoginRateLimit/Window throttle POST /api/auth/login and
	// POST /admin/auth/login: at most LoginRateLimit attempts (successful
	// or not) per client IP per LoginRateWindow. See internal/ratelimit.
	LoginRateLimit  int
	LoginRateWindow time.Duration

	// SessionCleanupInterval is how often the background janitor purges
	// expired rows from the sessions/owner_sessions tables. See
	// auth.Service.PurgeExpiredSessions.
	SessionCleanupInterval time.Duration

	// StorageDir is where uploaded file bytes are written, one file per
	// upload named by its id. See internal/storage.LocalBackend.
	StorageDir string

	// MaxUploadBytes caps a single file upload's size; a larger request
	// body is rejected before it's fully read. See internal/storage.
	MaxUploadBytes int64

	// MaxRequestBodyBytes caps a single /api/data/* create/update request
	// body's size; a larger body is rejected (413) before it's fully read
	// into memory. See internal/restapi.
	MaxRequestBodyBytes int64

	// ResendAPIKey/EmailFrom configure outgoing transactional email (see
	// internal/email). Both empty is a valid, common state — a
	// deployment that never uses password reset doesn't need either —
	// and gets internal/email.NoopSender, which fails clearly the first
	// time something actually tries to send.
	ResendAPIKey string
	EmailFrom    string
	// PasswordResetURL is the deployment's own frontend page that
	// receives a password-reset link — maubase only issues/validates the
	// token (POST /api/auth/forgot-password, POST /api/auth/reset-password);
	// the page a human actually lands on and enters a new password into
	// is the deployer's own app, not something this server renders.
	// CreateResetToken's raw token is appended as ?token=.
	PasswordResetURL string

	// GoogleClientID/Secret and GitHubClientID/Secret enable "Continue
	// with <provider>" (see internal/social, spec/social-login.md) —
	// each pair empty means that provider simply isn't offered
	// (GET /api/auth/social/{provider} 404s), not a startup error. Get
	// these from Google Cloud Console / a GitHub OAuth App, with the
	// redirect URI set to {Issuer}/api/auth/social/{provider}/callback.
	GoogleClientID     string
	GoogleClientSecret string
	GitHubClientID     string
	GitHubClientSecret string
	// SocialLoginRedirectURL is the deployment's own frontend page a
	// successful social sign-in lands on afterward (the session cookie
	// is already set by then) — same reasoning as PasswordResetURL:
	// maubase issues the session, it doesn't render a page for a human
	// to look at next.
	SocialLoginRedirectURL string

	// RedisURL, if set, upgrades realtime fan-out from single-process
	// (the default — see internal/realtime's package doc) to
	// cross-process via Redis pub/sub (internal/realtime.RedisRelay) —
	// the fix for spec/realtime.md's documented v1 limit. A
	// redis://[:password@]host:port[/db] connection string; empty means
	// "just this one process," which is what most deployments want.
	RedisURL string

	// Env gates anything that's useful for local tooling (an agent
	// introspecting the schema, say) but is extra attack surface on a
	// production deployment — currently just GET /api/schema (see
	// internal/restapi). Defaults to "production", the safe side: a
	// deployment has to opt in to "development" explicitly, never the
	// other way around, so forgetting to set this can't silently expose
	// anything.
	Env string
}

// IsDevelopment reports whether Env opts into development-only surface.
func (c Config) IsDevelopment() bool {
	return c.Env == "development"
}

func Load() Config {
	return Config{
		Addr:                   getEnv("MAUBASE_ADDR", ":8080"),
		DBPath:                 getEnv("MAUBASE_DB_PATH", "data/maubase.db"),
		Issuer:                 getEnv("MAUBASE_ISSUER", "http://localhost:8080"),
		BootstrapOwnerEmail:    getEnv("MAUBASE_BOOTSTRAP_OWNER_EMAIL", ""),
		BootstrapOwnerPassword: getEnv("MAUBASE_BOOTSTRAP_OWNER_PASSWORD", ""),
		MigrationsDir:          getEnv("MAUBASE_MIGRATIONS_DIR", "migrations"),
		LoginRateLimit:         getEnvInt("MAUBASE_LOGIN_RATE_LIMIT", 10),
		LoginRateWindow:        getEnvSeconds("MAUBASE_LOGIN_RATE_WINDOW_SECONDS", 60*time.Second),
		SessionCleanupInterval: getEnvSeconds("MAUBASE_SESSION_CLEANUP_INTERVAL_SECONDS", time.Hour),
		StorageDir:             getEnv("MAUBASE_STORAGE_DIR", "data/storage"),
		MaxUploadBytes:         int64(getEnvInt("MAUBASE_MAX_UPLOAD_MB", 25)) * 1024 * 1024,
		MaxRequestBodyBytes:    int64(getEnvInt("MAUBASE_MAX_REQUEST_BODY_KB", 1024)) * 1024,
		ResendAPIKey:           getEnv("MAUBASE_RESEND_API_KEY", ""),
		EmailFrom:              getEnv("MAUBASE_EMAIL_FROM", ""),
		PasswordResetURL:       getEnv("MAUBASE_PASSWORD_RESET_URL", ""),
		GoogleClientID:         getEnv("MAUBASE_GOOGLE_CLIENT_ID", ""),
		GoogleClientSecret:     getEnv("MAUBASE_GOOGLE_CLIENT_SECRET", ""),
		GitHubClientID:         getEnv("MAUBASE_GITHUB_CLIENT_ID", ""),
		GitHubClientSecret:     getEnv("MAUBASE_GITHUB_CLIENT_SECRET", ""),
		SocialLoginRedirectURL: getEnv("MAUBASE_SOCIAL_LOGIN_REDIRECT_URL", ""),
		RedisURL:               getEnv("MAUBASE_REDIS_URL", ""),
		Env:                    getEnv("MAUBASE_ENV", "production"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func getEnvSeconds(key string, fallback time.Duration) time.Duration {
	n := getEnvInt(key, -1)
	if n < 0 {
		return fallback
	}
	return time.Duration(n) * time.Second
}
