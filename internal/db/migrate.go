package db

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"time"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate applies every migration in migrations/ that hasn't run yet, in
// filename order (hence the 0001_, 0002_... prefixes). Applied migrations
// are tracked in schema_migrations so this is safe to call on every
// startup. This is maubase's own internal schema (identity/oauth/owner
// tables) — see MigrateDir for a deployment's own application schema.
func Migrate(sqlDB *sql.DB) error {
	if err := ensureMigrationsTable(sqlDB); err != nil {
		return err
	}
	_, err := applyMigrations(sqlDB, migrationsFS, "migrations", "")
	return err
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
	_, err := MigrateDirApplied(sqlDB, dir)
	return err
}

// MigrateDirApplied is MigrateDir, but also reports the filenames it
// newly applied, in the order it applied them (empty, not nil, when
// nothing was pending). This is what `maubase migrate up` calls so it can
// tell the operator what it actually did, rather than just succeeding
// silently the way the boot-time call in cmd/maubase does.
func MigrateDirApplied(sqlDB *sql.DB, dir string) ([]string, error) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("stat %s: %w", dir, err)
	}
	if err := ensureMigrationsTable(sqlDB); err != nil {
		return nil, err
	}
	// "app:" namespaces these versions in schema_migrations so a disk
	// migration can never collide with (or be confused for) one of
	// maubase's own embedded ones, even if the filenames happen to match.
	return applyMigrations(sqlDB, os.DirFS(dir), ".", "app:")
}

// MigrationStatus is one .sql file in an application migrations
// directory, alongside whether (and when) it's been applied. See Status.
type MigrationStatus struct {
	// Name is the migration's filename, e.g. "0001_create_posts.sql" —
	// not including the "app:" schema_migrations namespace prefix.
	Name    string
	Applied bool
	// AppliedAt is the zero time when Applied is false.
	AppliedAt time.Time
}

// Status reports every .sql file in dir, in the same filename order
// MigrateDir applies them in, alongside whether each has already been
// applied. It never applies anything itself. A missing directory reports
// zero migrations rather than an error, matching MigrateDir's own
// "no directory yet" handling — this is what `maubase migrate status`
// calls.
func Status(sqlDB *sql.DB, dir string) ([]MigrationStatus, error) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("stat %s: %w", dir, err)
	}
	if err := ensureMigrationsTable(sqlDB); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	out := make([]MigrationStatus, 0, len(names))
	for _, name := range names {
		var appliedAt sql.NullTime
		err := sqlDB.QueryRow(
			`SELECT applied_at FROM schema_migrations WHERE version = ?`, "app:"+name,
		).Scan(&appliedAt)
		switch {
		case err == sql.ErrNoRows:
			out = append(out, MigrationStatus{Name: name})
		case err != nil:
			return nil, fmt.Errorf("check migration %s: %w", name, err)
		default:
			out = append(out, MigrationStatus{Name: name, Applied: true, AppliedAt: appliedAt.Time})
		}
	}
	return out, nil
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

// applyMigrations returns the filenames (not full "version" strings — no
// versionPrefix) it newly applied, in application order, so callers like
// MigrateDirApplied can report exactly what happened. Always non-nil,
// even when nothing was pending, so callers can range over it directly.
func applyMigrations(sqlDB *sql.DB, fsys fs.FS, root, versionPrefix string) ([]string, error) {
	entries, err := fs.ReadDir(fsys, root)
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	applied := []string{}
	for _, name := range names {
		version := versionPrefix + name

		var count int
		if err := sqlDB.QueryRow(
			`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version,
		).Scan(&count); err != nil {
			return applied, fmt.Errorf("check migration %s: %w", version, err)
		}
		if count > 0 {
			continue
		}

		path := name
		if root != "." {
			path = root + "/" + name
		}
		content, err := fs.ReadFile(fsys, path)
		if err != nil {
			return applied, fmt.Errorf("read migration %s: %w", version, err)
		}

		tx, err := sqlDB.Begin()
		if err != nil {
			return applied, fmt.Errorf("begin tx for %s: %w", version, err)
		}
		if _, err := tx.Exec(string(content)); err != nil {
			tx.Rollback()
			return applied, fmt.Errorf("apply migration %s: %w", version, err)
		}
		if _, err := tx.Exec(
			`INSERT INTO schema_migrations (version) VALUES (?)`, version,
		); err != nil {
			tx.Rollback()
			return applied, fmt.Errorf("record migration %s: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return applied, fmt.Errorf("commit migration %s: %w", version, err)
		}
		applied = append(applied, name)
	}
	return applied, nil
}
