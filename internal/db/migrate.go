package db

import (
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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
	// ChecksumMismatch is true only when Applied and this file's current
	// on-disk content no longer matches the checksum recorded when it
	// was applied — i.e. someone edited an already-applied migration
	// file after the fact. Always false for a pending migration, and
	// also false for an applied one with no stored checksum (recorded
	// before checksum verification existed, or added directly to
	// schema_migrations by hand) — there's nothing to compare against,
	// which isn't evidence of tampering.
	ChecksumMismatch bool
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
		currentChecksum := migrationChecksum(string(content))

		var appliedAt sql.NullTime
		var storedChecksum sql.NullString
		err = sqlDB.QueryRow(
			`SELECT applied_at, checksum FROM schema_migrations WHERE version = ?`, "app:"+name,
		).Scan(&appliedAt, &storedChecksum)
		switch {
		case err == sql.ErrNoRows:
			out = append(out, MigrationStatus{Name: name, HasDown: hasDown})
		case err != nil:
			return nil, fmt.Errorf("check migration %s: %w", name, err)
		default:
			mismatch := storedChecksum.Valid && storedChecksum.String != "" && storedChecksum.String != currentChecksum
			out = append(out, MigrationStatus{
				Name: name, Applied: true, AppliedAt: appliedAt.Time, HasDown: hasDown,
				ChecksumMismatch: mismatch,
			})
		}
	}
	return out, nil
}

// VerifyChecksums reports the filenames of already-applied application
// migrations whose current on-disk content no longer matches the
// checksum recorded when they were applied (see MigrationStatus.
// ChecksumMismatch), in filename order. Always non-nil. This is what
// `maubase migrate up` checks before applying anything, so it can warn
// about a modified migration without blocking on it (see cmd/maubase).
func VerifyChecksums(sqlDB *sql.DB, dir string) ([]string, error) {
	statuses, err := Status(sqlDB, dir)
	if err != nil {
		return nil, err
	}
	mismatches := []string{}
	for _, s := range statuses {
		if s.ChecksumMismatch {
			mismatches = append(mismatches, s.Name)
		}
	}
	return mismatches, nil
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

// MigrateDirRedo reverts the n most recently applied application
// migrations (exactly like MigrateDirDown, including its error behavior
// for a migration with no "-- +migrate Down" section) and then
// immediately reapplies exactly those same migrations, in their
// original forward order. This is deliberately narrower than "revert n,
// then run a general apply-everything-pending Up": a migration that was
// already pending before Redo ran (never applied, so never reverted)
// is left untouched, not swept up into this call just because Up would
// otherwise apply it too. Returns the filenames redone, in forward
// order (always non-nil). If a migration can't be reverted, Redo stops
// there — same as Down — and reapplies nothing.
func MigrateDirRedo(sqlDB *sql.DB, dir string, n int) ([]string, error) {
	reverted, err := MigrateDirDown(sqlDB, dir, n)
	if err != nil {
		return nil, err
	}

	// reverted is newest-first; redo reapplies oldest-first (the reverse)
	// so the schema is rebuilt in the same order it was originally built.
	redone := make([]string, 0, len(reverted))
	for i := len(reverted) - 1; i >= 0; i-- {
		name := reverted[i]
		if err := applyOneMigration(sqlDB, dir, name); err != nil {
			return redone, fmt.Errorf("reapply migration %s: %w", name, err)
		}
		redone = append(redone, name)
	}
	return redone, nil
}

// MigrateDirTo moves the application schema to exactly the migration
// named by target — either its exact filename ("0003_add_index.sql") or
// a bare numeric prefix ("3" or "0003"), see resolveVersion. If target
// is currently pending, it applies every not-yet-applied migration up
// to and including it, in filename order (like a scoped Up). If target
// is currently applied and something after it is also applied, it
// reverts everything after it, newest first — same error behavior as
// MigrateDirDown, including refusing on a migration with no "-- +migrate
// Down" section. If target is already exactly the current state
// (applied, nothing after it applied), it's a no-op.
//
// This assumes the applied set is a normal contiguous prefix of
// filename order, which is always true through ordinary use of
// new/up/down/redo — the same assumption MigrateDirDown itself already
// makes about "the last n applied" being well-defined. Returns the
// filenames it touched, in the order touched, and which direction it
// went ("up", "down", or "" for a no-op).
func MigrateDirTo(sqlDB *sql.DB, dir, target string) (touched []string, direction string, err error) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, "", fmt.Errorf("no migrations directory at %s", dir)
	} else if err != nil {
		return nil, "", fmt.Errorf("stat %s: %w", dir, err)
	}
	if err := ensureMigrationsTable(sqlDB); err != nil {
		return nil, "", err
	}

	targetName, err := resolveVersion(dir, target)
	if err != nil {
		return nil, "", err
	}

	statuses, err := Status(sqlDB, dir)
	if err != nil {
		return nil, "", err
	}
	targetIdx := -1
	for i, s := range statuses {
		if s.Name == targetName {
			targetIdx = i
			break
		}
	}
	if targetIdx < 0 {
		return nil, "", fmt.Errorf("migration %s not found", targetName)
	}

	if statuses[targetIdx].Applied {
		n := 0
		for i := targetIdx + 1; i < len(statuses); i++ {
			if statuses[i].Applied {
				n++
			}
		}
		if n == 0 {
			return []string{}, "", nil
		}
		reverted, err := MigrateDirDown(sqlDB, dir, n)
		return reverted, "down", err
	}

	applied := []string{}
	for i := 0; i <= targetIdx; i++ {
		if statuses[i].Applied {
			continue
		}
		if err := applyOneMigration(sqlDB, dir, statuses[i].Name); err != nil {
			return applied, "up", fmt.Errorf("apply migration %s: %w", statuses[i].Name, err)
		}
		applied = append(applied, statuses[i].Name)
	}
	return applied, "up", nil
}

// versionNumberRe matches a migration filename's leading "NNNN_" numeric
// prefix — kept independent of cmd/maubase's own copy of this pattern
// (used there for "migrate new"'s next-number scan), since that's a
// different package.
var versionNumberRe = regexp.MustCompile(`^(\d+)_`)

// resolveVersion finds the single migration file in dir matching
// target: first by exact filename, then (if target parses as an
// integer) by numeric prefix value, so "3", "03", and "0003" all match
// "0003_add_index.sql". Fails if target matches nothing, or matches more
// than one file — possible only with inconsistently-padded filenames
// nobody created via "maubase migrate new" (which always keeps one
// consistent width).
func resolveVersion(dir, target string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read migrations dir: %w", err)
	}

	for _, e := range entries {
		if !e.IsDir() && e.Name() == target {
			return e.Name(), nil
		}
	}

	targetN, convErr := strconv.Atoi(target)
	if convErr != nil {
		return "", fmt.Errorf("no migration named %q in %s", target, dir)
	}
	var matches []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := versionNumberRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		if n, err := strconv.Atoi(m[1]); err == nil && n == targetN {
			matches = append(matches, e.Name())
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no migration matching %q in %s", target, dir)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("%q matches more than one migration file: %v", target, matches)
	}
}

// applyOneMigration applies (or reapplies, after MigrateDirDown reverted
// it) a single named migration file's Up section and records it, in one
// transaction. Unlike applyMigrations, it doesn't check whether the
// migration is already recorded as applied — callers (MigrateDirRedo)
// are responsible for calling this only on a migration they know isn't
// currently applied.
func applyOneMigration(sqlDB *sql.DB, dir, name string) error {
	content, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return fmt.Errorf("read migration %s: %w", name, err)
	}
	up, _, _ := splitMigrationSQL(string(content))
	version := "app:" + name

	tx, err := sqlDB.Begin()
	if err != nil {
		return fmt.Errorf("begin tx for %s: %w", name, err)
	}
	if _, err := tx.Exec(up); err != nil {
		tx.Rollback()
		return fmt.Errorf("apply migration %s: %w", name, err)
	}
	if _, err := tx.Exec(
		`INSERT INTO schema_migrations (version, checksum) VALUES (?, ?)`, version, migrationChecksum(string(content)),
	); err != nil {
		tx.Rollback()
		return fmt.Errorf("record migration %s: %w", name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", name, err)
	}
	return nil
}

// DiffResult is one discrepancy Diff found between the live database's
// schema and what its currently-applied migrations account for.
type DiffResult struct {
	Table string
	// Kind is "unexplained" (exists live, no applied migration explains
	// it), "missing" (an applied migration should have created it, but
	// it doesn't exist live), or "altered" (exists in both, but its live
	// definition no longer matches what its migration(s) produced).
	Kind string
	// LiveSQL/ExpectedSQL are the two CREATE TABLE statements being
	// compared (sqlite_master's own stored text) — empty on whichever
	// side the table doesn't exist.
	LiveSQL     string
	ExpectedSQL string
}

// Diff compares the live database's application-schema table
// definitions against a "shadow" database built by replaying only the
// app migrations Status reports as Applied (not merely present in dir,
// so a still-pending migration is never mistaken for drift — see
// spec/migrations-cli.md MIGCLI-38). Scoped to app-schema only, same as
// every other "migrate" subcommand: maubase's own embedded tables
// (users, sessions, oauth_*, etc.) are excluded from comparison
// entirely — Diff doesn't require them to be present, absent, or
// unaltered, since that's db.Migrate's business, not this command's.
// Anywhere sqlite_master otherwise disagrees between the two is
// reported:
//
//   - "unexplained": an app table exists live but no applied migration
//     explains it — created outside any migration file, e.g. via the
//     admin UI's create-table form or a raw CREATE TABLE in SQL Studio.
//   - "missing": an applied migration should have created this table,
//     but it doesn't exist live — e.g. someone DROP TABLE'd it by hand
//     without touching the migration record.
//   - "altered": exists in both, but the live CREATE TABLE text no
//     longer matches what replaying its migration(s) produced — e.g. an
//     ALTER TABLE run outside any migration file.
//
// Diff only ever reads — it never modifies the live database, and (see
// issue #149) doesn't generate a migration capturing what it finds;
// that's deliberately out of scope for now, given how much harder it is
// to do safely for an altered (as opposed to wholly new) table.
func Diff(sqlDB *sql.DB, dir string) ([]DiffResult, error) {
	shadow, err := Open(":memory:")
	if err != nil {
		return nil, fmt.Errorf("open shadow database: %w", err)
	}
	defer shadow.Close()

	if err := Migrate(shadow); err != nil {
		return nil, fmt.Errorf("build shadow database's embedded schema: %w", err)
	}
	// Every table name db.Migrate alone produces — excluded from
	// comparison below, in both directions, regardless of whether the
	// live database has even been through a server boot yet (the only
	// thing that actually applies these there).
	reserved, err := tableDefinitions(shadow)
	if err != nil {
		return nil, fmt.Errorf("read shadow embedded schema: %w", err)
	}

	statuses, err := Status(sqlDB, dir)
	if err != nil {
		return nil, err
	}
	for _, s := range statuses {
		if !s.Applied {
			continue
		}
		if err := applyOneMigration(shadow, dir, s.Name); err != nil {
			return nil, fmt.Errorf("replay migration %s into shadow database: %w", s.Name, err)
		}
	}

	live, err := tableDefinitions(sqlDB)
	if err != nil {
		return nil, fmt.Errorf("read live schema: %w", err)
	}
	expected, err := tableDefinitions(shadow)
	if err != nil {
		return nil, fmt.Errorf("read shadow schema: %w", err)
	}

	results := []DiffResult{}
	for name, liveSQL := range live {
		if _, isReserved := reserved[name]; isReserved {
			continue
		}
		switch expectedSQL, ok := expected[name]; {
		case !ok:
			results = append(results, DiffResult{Table: name, Kind: "unexplained", LiveSQL: liveSQL})
		case liveSQL != expectedSQL:
			results = append(results, DiffResult{Table: name, Kind: "altered", LiveSQL: liveSQL, ExpectedSQL: expectedSQL})
		}
	}
	for name, expectedSQL := range expected {
		if _, isReserved := reserved[name]; isReserved {
			continue
		}
		if _, ok := live[name]; !ok {
			results = append(results, DiffResult{Table: name, Kind: "missing", ExpectedSQL: expectedSQL})
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Table < results[j].Table })
	return results, nil
}

// tableDefinitions returns every user table's name -> CREATE TABLE text
// (sqlite_master's own stored copy), excluding SQLite's own internal
// bookkeeping tables (sqlite_sequence and the like).
func tableDefinitions(sqlDB *sql.DB) (map[string]string, error) {
	rows, err := sqlDB.Query(`SELECT name, sql FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, sqlText string
		if err := rows.Scan(&name, &sqlText); err != nil {
			return nil, err
		}
		out[name] = sqlText
	}
	return out, rows.Err()
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
	if _, err := sqlDB.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			checksum   TEXT
		)
	`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	return ensureChecksumColumn(sqlDB)
}

// ensureChecksumColumn adds schema_migrations.checksum on the fly for a
// database created before checksum verification existed — the CREATE
// TABLE above already includes it for a brand new one, but "IF NOT
// EXISTS" is a no-op against an existing table missing the column. NULL
// for every row already in such a table, which VerifyChecksums/Status
// treat as "nothing to compare against," not evidence of tampering.
func ensureChecksumColumn(sqlDB *sql.DB) error {
	rows, err := sqlDB.Query(`PRAGMA table_info(schema_migrations)`)
	if err != nil {
		return fmt.Errorf("inspect schema_migrations columns: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return fmt.Errorf("scan schema_migrations column info: %w", err)
		}
		if name == "checksum" {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspect schema_migrations columns: %w", err)
	}
	if _, err := sqlDB.Exec(`ALTER TABLE schema_migrations ADD COLUMN checksum TEXT`); err != nil {
		return fmt.Errorf("add schema_migrations.checksum column: %w", err)
	}
	return nil
}

// migrationChecksum hashes a migration file's exact on-disk content
// (the whole file, not just its Up section — an edit to the Down half
// matters too) so a later Status/VerifyChecksums call can tell whether
// it's been edited since it was applied.
func migrationChecksum(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
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
			`INSERT INTO schema_migrations (version, checksum) VALUES (?, ?)`, version, migrationChecksum(string(content)),
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
