// Package db owns the database connection and schema migrations.
//
// v1 targets SQLite only (via the pure-Go modernc.org/sqlite driver, so the
// whole project stays cgo-free and cross-compiles into a single static
// binary). A Postgres adapter can be added later behind the same *sql.DB
// call sites, since all queries are plain database/sql with ? placeholders
// kept centralized in the auth/db-adapter packages rather than scattered
// across handlers.
package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Open opens (and creates, if missing) the SQLite database at path with
// pragmas suited to a small, single-node deployment: WAL journaling for
// concurrent readers, foreign keys enforced, and a busy timeout so
// short-lived write contention retries instead of erroring out.
//
// path's parent directory is created if it doesn't exist yet (e.g. the
// default MAUBASE_DB_PATH's "data/" on a brand new project) — SQLite
// itself creates the database file on first use, but never the
// directory it lives in, so without this a fresh checkout's very first
// boot (or `maubase migrate up`) fails with "unable to open database
// file" before it ever gets the chance to create anything.
func Open(path string) (*sql.DB, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create %s: %w", dir, err)
		}
	}

	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)",
		path,
	)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// SQLite allows only one writer at a time regardless of connection count.
	// Serializing at the database/sql pool level (rather than fanning out
	// connections and relying on busy_timeout retries under contention) is
	// the simplest correct thing at small-VPS scale, and keeps write-path
	// error handling boring. Revisit (e.g. split read/write pools) only if
	// profiling shows write contention is actually the bottleneck.
	sqlDB.SetMaxOpenConns(1)

	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	return sqlDB, nil
}
