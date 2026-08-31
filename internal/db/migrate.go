package db

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	// HasDown reports whether this file has a "-- +migrate Down" section
	// (see splitMigrationSQL) and so can be reverted by MigrateDirDown.
	// Most migrations written before rollback support existed — or ones
	// nobody bothered writing a down for — won't have one; that's
	// expected, not an error.
	HasDown bool
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
		content, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", name, err)
		}
		_, _, hasDown := splitMigrationSQL(string(content))

		var appliedAt sql.NullTime
		err = sqlDB.QueryRow(
			`SELECT applied_at FROM schema_migrations WHERE version = ?`, "app:"+name,
		).Scan(&appliedAt)
		switch {
		case err == sql.ErrNoRows:
			out = append(out, MigrationStatus{Name: name, HasDown: hasDown})
		case err != nil:
			return nil, fmt.Errorf("check migration %s: %w", name, err)
		default:
			out = append(out, MigrationStatus{Name: name, Applied: true, AppliedAt: appliedAt.Time, HasDown: hasDown})
		}
	}
	return out, nil
}

// MigrateDirDown reverts the n most recently applied application
// migrations (newest first, by applied_at then by version to break ties
// deterministically — e.g. every migration in one "up" batch tends to
// share the same applied_at second), using each file's "-- +migrate
// Down" section. Returns the filenames it reverted, in the order
// reverted (always non-nil).
//
// If fewer than n migrations are applied, it reverts however many there
// are. If a migration to be reverted has no "-- +migrate Down" section
// (see splitMigrationSQL) — or its file has been deleted since it was
// applied — Down stops there and returns an error naming it: whatever it
// already reverted earlier in this same call stays reverted, but it
// won't guess at how to undo a migration it has no down SQL for. A
// missing directory reverts nothing, matching MigrateDirApplied/Status's
// own "no directory yet" handling.
func MigrateDirDown(sqlDB *sql.DB, dir string, n int) ([]string, error) {
	if n <= 0 {
		return nil, fmt.Errorf("n must be a positive number of migrations to revert, got %d", n)
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return []string{}, nil
	} else if err != nil {
		return nil, fmt.Errorf("stat %s: %w", dir, err)
	}
	if err := ensureMigrationsTable(sqlDB); err != nil {
		return nil, err
	}

	rows, err := sqlDB.Query(
		`SELECT version FROM schema_migrations WHERE version LIKE 'app:%' ORDER BY applied_at DESC, version DESC LIMIT ?`,
		n,
	)
	if err != nil {
		return nil, fmt.Errorf("list applied migrations: %w", err)
	}
	var versions []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan applied migration: %w", err)
		}
		versions = append(versions, v)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("list applied migrations: %w", err)
	}
	rows.Close()

	reverted := []string{}
	for _, version := range versions {
		name := strings.TrimPrefix(version, "app:")

		content, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return reverted, fmt.Errorf("read migration %s to revert it: %w", name, err)
		}
		_, down, hasDown := splitMigrationSQL(string(content))
		if !hasDown || strings.TrimSpace(down) == "" {
			return reverted, fmt.Errorf(`migration %s has no "-- +migrate Down" section — can't revert it`, name)
		}

		tx, err := sqlDB.Begin()
		if err != nil {
			return reverted, fmt.Errorf("begin tx to revert %s: %w", name, err)
		}
		if _, err := tx.Exec(down); err != nil {
			tx.Rollback()
			return reverted, fmt.Errorf("revert migration %s: %w", name, err)
		}
		if _, err := tx.Exec(`DELETE FROM schema_migrations WHERE version = ?`, version); err != nil {
			tx.Rollback()
			return reverted, fmt.Errorf("unrecord migration %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return reverted, fmt.Errorf("commit revert of %s: %w", name, err)
		}
		reverted = append(reverted, name)
	}
	return reverted, nil
}

const (
	migrateUpMarker   = "-- +migrate Up"
	migrateDownMarker = "-- +migrate Down"
)

// splitMigrationSQL splits a migration file's content into its "up" and
// "down" SQL using "-- +migrate Up" / "-- +migrate Down" marker lines
// (each alone on its own line, surrounding whitespace ignored). This
// keeps a migration's forward and reverse SQL in one file — no separate
// ".down.sql" to keep in sync or forget — while staying entirely
// backward compatible: a file with neither marker (every migration
// written before rollback support existed, and the common case for a new
// one nobody bothered writing a down for) is treated as 100% up SQL with
// no down section, exactly like before this existed. applyMigrations
// always executes only the up half; MigrateDirDown requires a down half
// to exist.
func splitMigrationSQL(content string) (up, down string, hasDown bool) {
	lines := strings.Split(content, "\n")
	upStart, downLine := -1, -1
	for i, line := range lines {
		switch strings.TrimSpace(line) {
		case migrateUpMarker:
			upStart = i + 1
		case migrateDownMarker:
			downLine = i
		}
	}
	if upStart < 0 {
		// No "-- +migrate Up" marker at all: the whole file is up SQL.
		return content, "", false
	}
	if downLine < 0 || downLine < upStart {
		// No down marker, or a malformed file where it comes before the
		// up marker — either way, no down section.
		return strings.Join(lines[upStart:], "\n"), "", false
	}
	return strings.Join(lines[upStart:downLine], "\n"), strings.Join(lines[downLine+1:], "\n"), true
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
		up, _, _ := splitMigrationSQL(string(content))

		tx, err := sqlDB.Begin()
		if err != nil {
			return applied, fmt.Errorf("begin tx for %s: %w", version, err)
		}
		if _, err := tx.Exec(up); err != nil {
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
