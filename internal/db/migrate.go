package db

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"sort"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate applies every migration in migrations/ that hasn't run yet, in
// filename order (hence the 0001_, 0002_... prefixes). Applied migrations
// are tracked in schema_migrations so this is safe to call on every
// startup. This is baas's own internal schema (identity/oauth/owner
// tables) — see MigrateDir for a deployment's own application schema.
func Migrate(sqlDB *sql.DB) error {
	if err := ensureMigrationsTable(sqlDB); err != nil {
		return err
	}
	return applyMigrations(sqlDB, migrationsFS, "migrations", "")
}

// MigrateDir applies every .sql file in dir (an ordinary directory on
// disk, not embedded in the binary) that hasn't run yet, in filename
// order. This is how a deployment defines its own application tables —
// drop numbered .sql files in, restart, and (per internal/restapi) any
// table that isn't reserved becomes a queryable REST collection.
//
// A missing directory is not an error: most deployments won't have one
// until they add their first table, and every test that doesn't care
// about application schema shouldn't need to create an empty directory
// just to avoid an error here.
func MigrateDir(sqlDB *sql.DB, dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("stat %s: %w", dir, err)
	}
	if err := ensureMigrationsTable(sqlDB); err != nil {
		return err
	}
	// "app:" namespaces these versions in schema_migrations so a disk
	// migration can never collide with (or be confused for) one of
	// baas's own embedded ones, even if the filenames happen to match.
	return applyMigrations(sqlDB, os.DirFS(dir), ".", "app:")
}

func ensureMigrationsTable(sqlDB *sql.DB) error {
	_, err := sqlDB.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	return nil
}

func applyMigrations(sqlDB *sql.DB, fsys fs.FS, root, versionPrefix string) error {
	entries, err := fs.ReadDir(fsys, root)
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		version := versionPrefix + name

		var applied int
		if err := sqlDB.QueryRow(
			`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version,
		).Scan(&applied); err != nil {
			return fmt.Errorf("check migration %s: %w", version, err)
		}
		if applied > 0 {
			continue
		}

		path := name
		if root != "." {
			path = root + "/" + name
		}
		content, err := fs.ReadFile(fsys, path)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", version, err)
		}

		tx, err := sqlDB.Begin()
		if err != nil {
			return fmt.Errorf("begin tx for %s: %w", version, err)
		}
		if _, err := tx.Exec(string(content)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", version, err)
		}
		if _, err := tx.Exec(
			`INSERT INTO schema_migrations (version) VALUES (?)`, version,
		); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %s: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", version, err)
		}
	}
	return nil
}
