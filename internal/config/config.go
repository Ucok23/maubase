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
	// files live (as opposed to baas's own embedded migrations). Missing
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
}

func Load() Config {
	return Config{
		Addr:                   getEnv("BAAS_ADDR", ":8080"),
		DBPath:                 getEnv("BAAS_DB_PATH", "data/baas.db"),
		Issuer:                 getEnv("BAAS_ISSUER", "http://localhost:8080"),
		BootstrapOwnerEmail:    getEnv("BAAS_BOOTSTRAP_OWNER_EMAIL", ""),
		BootstrapOwnerPassword: getEnv("BAAS_BOOTSTRAP_OWNER_PASSWORD", ""),
		MigrationsDir:          getEnv("BAAS_MIGRATIONS_DIR", "migrations"),
		LoginRateLimit:         getEnvInt("BAAS_LOGIN_RATE_LIMIT", 10),
		LoginRateWindow:        getEnvSeconds("BAAS_LOGIN_RATE_WINDOW_SECONDS", 60*time.Second),
		SessionCleanupInterval: getEnvSeconds("BAAS_SESSION_CLEANUP_INTERVAL_SECONDS", time.Hour),
		StorageDir:             getEnv("BAAS_STORAGE_DIR", "data/storage"),
		MaxUploadBytes:         int64(getEnvInt("BAAS_MAX_UPLOAD_MB", 25)) * 1024 * 1024,
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
