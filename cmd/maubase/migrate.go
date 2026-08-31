package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"maubase/internal/config"
	"maubase/internal/db"
)

// runMigrate implements `maubase migrate <subcommand>` — see
// spec/migrations-cli.md. This is scoped to a deployment's own
// application-schema migrations (db.MigrateDir's directory), not
// maubase's embedded internal schema (users/sessions/oauth/etc, see
// db.Migrate), which is applied unconditionally on every server boot
// regardless of this command and isn't something an operator manages by
// hand.
func runMigrate(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: maubase migrate <new|up|down|redo|to|status|diff> [--dir path] [--db path]")
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "new":
		// Purely a filesystem operation — deliberately never opens (or
		// creates) the database, unlike down/up/redo/to/status/diff below.
		return runMigrateNew(rest)
	case "down", "redo":
		return runMigrateDownOrRedo(sub, rest)
	case "to":
		return runMigrateTo(rest)
	case "up", "status", "diff":
		return runMigrateUpStatusOrDiff(sub, rest)
	default:
		return fmt.Errorf("maubase migrate: unknown subcommand %q (want \"new\", \"up\", \"down\", \"redo\", \"to\", \"status\", or \"diff\")", sub)
	}
}

// runMigrateUpStatusOrDiff is the shared setup behind "up", "status", and
// "diff" — all three take the same flags (--db/--dir, no positional
// argument) and open the same database.
func runMigrateUpStatusOrDiff(sub string, args []string) error {
	// Defaults come from the same env vars the server itself reads
	// (MAUBASE_DB_PATH, MAUBASE_MIGRATIONS_DIR), so pointing this at a
	// running deployment's database needs no flags at all — only a local
	// dry run against a scratch DB needs to override them.
	cfg := config.Load()
	fs := flag.NewFlagSet("maubase migrate "+sub, flag.ContinueOnError)
	dbPath := fs.String("db", cfg.DBPath, "path to the SQLite database file")
	dir := fs.String("dir", cfg.MigrationsDir, "directory of application migration .sql files")
	if err := fs.Parse(args); err != nil {
		return err
	}

	sqlDB, err := db.Open(*dbPath)
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	switch sub {
	case "up":
		return migrateUp(sqlDB, *dir)
	case "status":
		return migrateStatus(sqlDB, *dir)
	case "diff":
		return migrateDiff(sqlDB, *dir)
	default:
		// Unreachable: sub is validated by the caller.
		return fmt.Errorf("maubase migrate: unknown subcommand %q", sub)
	}
}

func runMigrateNew(args []string) error {
	// "new"'s syntax is <name> [--dir path] — the name comes first, with
	// --dir (if any) typically after it. See extractFlags for why that
	// needs hand-rolled parsing instead of a plain flag.FlagSet.
	values, rest, err := extractFlags(args, "dir")
	if err != nil {
		return err
	}
	dir := config.Load().MigrationsDir
	if v, ok := values["dir"]; ok {
		dir = v
	}
	// "maubase migrate new create posts table" and
	// "maubase migrate new create_posts_table" both work, so a caller
	// doesn't have to remember to quote the name.
	name := strings.Join(rest, " ")
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("usage: maubase migrate new <name> [--dir path]")
	}

	path, err := newMigrationFile(dir, name)
	if err != nil {
		return err
	}
	fmt.Printf("created %s\n", path)
	return nil
}

// runMigrateDownOrRedo is the shared setup behind "down" and "redo" —
// both take the identical [n] [--db path] [--dir path] shape (n defaults
// to 1) and only differ in which db.MigrateDir* function they call.
func runMigrateDownOrRedo(sub string, args []string) error {
	n, dir, dbPath, err := parseOptionalCount(sub, args)
	if err != nil {
		return err
	}

	sqlDB, err := db.Open(dbPath)
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	switch sub {
	case "down":
		return migrateDown(sqlDB, dir, n)
	case "redo":
		return migrateRedo(sqlDB, dir, n)
	default:
		// Unreachable: sub is validated by the caller.
		return fmt.Errorf("maubase migrate: unknown subcommand %q", sub)
	}
}

// runMigrateTo implements "to"'s <version> [--db path] [--dir path]
// shape — a single required positional argument (unlike "down"/"redo"'s
// optional one), so it gets its own thin wrapper around extractFlags
// rather than reusing parseOptionalCount.
func runMigrateTo(args []string) error {
	values, rest, err := extractFlags(args, "dir", "db")
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return fmt.Errorf("usage: maubase migrate to <version> [--db path] [--dir path]")
	}
	target := rest[0]

	cfg := config.Load()
	dir := cfg.MigrationsDir
	if v, ok := values["dir"]; ok {
		dir = v
	}
	dbPath := cfg.DBPath
	if v, ok := values["db"]; ok {
		dbPath = v
	}

	sqlDB, err := db.Open(dbPath)
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	return migrateTo(sqlDB, dir, target)
}

// parseOptionalCount parses "down"/"redo"'s shared [n] [--db path] [--dir
// path] shape: an optional leading positive-integer count (default 1),
// with --dir/--db (see extractFlags) in any position. Same
// leading-positional-argument shape as "new".
func parseOptionalCount(sub string, args []string) (n int, dir, dbPath string, err error) {
	values, rest, err := extractFlags(args, "dir", "db")
	if err != nil {
		return 0, "", "", err
	}
	if len(rest) > 1 {
		return 0, "", "", fmt.Errorf("usage: maubase migrate %s [n] [--db path] [--dir path]", sub)
	}
	n = 1
	if len(rest) == 1 {
		parsed, convErr := strconv.Atoi(rest[0])
		if convErr != nil || parsed <= 0 {
			return 0, "", "", fmt.Errorf("n must be a positive integer, got %q", rest[0])
		}
		n = parsed
	}

	cfg := config.Load()
	dir = cfg.MigrationsDir
	if v, ok := values["dir"]; ok {
		dir = v
	}
	dbPath = cfg.DBPath
	if v, ok := values["db"]; ok {
		dbPath = v
	}
	return n, dir, dbPath, nil
}

// extractFlags pulls "--name value" / "--name=value" (single- or
// double-dash) out of args from wherever they appear, for the flag names
// listed, returning their values and every other token in order. "new"
// and "down" both need this instead of a plain flag.FlagSet: each takes
// a leading positional argument (a name / a count) that may be followed
// by flags, a shape Go's flag package can't express — it only parses
// flags up to the first positional argument, so "maubase migrate new
// <name> --dir x" would otherwise silently fold "--dir" and "x" into the
// name instead of recognizing them. Any other token starting with "-"
// fails clearly (a typo'd or wrong-subcommand flag) rather than quietly
// becoming part of the positional argument.
func extractFlags(args []string, names ...string) (values map[string]string, rest []string, err error) {
	values = map[string]string{}
	known := make(map[string]bool, len(names))
	for _, n := range names {
		known[n] = true
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		trimmed := strings.TrimPrefix(strings.TrimPrefix(a, "--"), "-")
		if trimmed == a || a == "-" {
			// No leading dash at all: an ordinary positional token.
			rest = append(rest, a)
			continue
		}
		if eq := strings.IndexByte(trimmed, '='); eq >= 0 {
			name, val := trimmed[:eq], trimmed[eq+1:]
			if !known[name] {
				return nil, nil, fmt.Errorf("flag provided but not defined: %s", a)
			}
			values[name] = val
			continue
		}
		if !known[trimmed] {
			return nil, nil, fmt.Errorf("flag provided but not defined: %s", a)
		}
		if i+1 >= len(args) {
			return nil, nil, fmt.Errorf("flag needs an argument: %s", a)
		}
		values[trimmed] = args[i+1]
		i++
	}
	return values, rest, nil
}

var (
	migrationNumberRe = regexp.MustCompile(`^(\d+)_`)
	migrationSlugRe   = regexp.MustCompile(`[^a-z0-9]+`)
)

// newMigrationFile creates the next-numbered .sql file in dir for the
// given human-readable name, creating dir itself if it doesn't exist yet
// (unlike MigrateDirApplied/Status, for which a missing directory is a
// silent no-op — here the operator's clear intent is to start one). The
// next number is one past the highest existing numeric prefix in dir, not
// the count of files in it, so a deleted or renamed migration doesn't
// cause a collision; the zero-padding width matches whatever that
// highest-numbered file already used (defaulting to 4 digits, matching
// maubase's own embedded migrations, when dir is empty or new).
func newMigrationFile(dir, name string) (string, error) {
	slug := migrationSlugRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "_")
	slug = strings.Trim(slug, "_")
	if slug == "" {
		return "", fmt.Errorf("migration name must contain at least one letter or digit, got %q", name)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create migrations dir %s: %w", dir, err)
	}
	number, width, err := nextMigrationNumber(dir)
	if err != nil {
		return "", fmt.Errorf("determine next migration number in %s: %w", dir, err)
	}

	filename := fmt.Sprintf("%0*d_%s.sql", width, number, slug)
	path := filepath.Join(dir, filename)
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("%s already exists", path)
	}

	content := fmt.Sprintf(`-- %s
-- Created by "maubase migrate new". Add your schema changes under
-- "+migrate Up" below — it runs once, in a transaction, the next time
-- "maubase migrate up" runs (or the server boots). The "+migrate Down"
-- section is optional but lets "maubase migrate down" undo this
-- migration later; a migration with no Down section simply can't be
-- reverted.

-- +migrate Up


-- +migrate Down

`, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}

// nextMigrationNumber scans dir for files matching the "NNNN_..." naming
// convention and returns one past the highest number found, along with
// that file's zero-padding width. An empty or not-yet-existing dir (or
// one with no numbered files in it yet) starts at 1, width 4.
func nextMigrationNumber(dir string) (number, width int, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0, err
	}
	width = 4
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := migrationNumberRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		if n >= number {
			number = n
			width = len(m[1])
		}
	}
	return number + 1, width, nil
}

func migrateUp(sqlDB *sql.DB, dir string) error {
	// Checked before applying anything, so a warning about a migration
	// modified since it ran doesn't get lost among the new "applied"
	// lines below it. This warns, it doesn't refuse to proceed — up
	// still needs to be safe to call unconditionally on every server
	// boot (see db.MigrateDir), which a hard failure here would break.
	if mismatches, err := db.VerifyChecksums(sqlDB, dir); err != nil {
		return err
	} else if len(mismatches) > 0 {
		fmt.Fprintln(os.Stderr, "warning: these already-applied migrations have been modified since they ran:")
		for _, name := range mismatches {
			fmt.Fprintf(os.Stderr, "  %s\n", name)
		}
		fmt.Fprintln(os.Stderr, `if that was intentional, "maubase migrate redo" reapplies a migration with its current content`)
	}

	applied, err := db.MigrateDirApplied(sqlDB, dir)
	if err != nil {
		return err
	}
	if len(applied) == 0 {
		fmt.Println("no pending migrations")
		return nil
	}
	for _, name := range applied {
		fmt.Printf("applied %s\n", name)
	}
	return nil
}

func migrateStatus(sqlDB *sql.DB, dir string) error {
	statuses, err := db.Status(sqlDB, dir)
	if err != nil {
		return err
	}
	if len(statuses) == 0 {
		fmt.Printf("no migrations found in %s\n", dir)
		return nil
	}
	for _, s := range statuses {
		switch {
		case s.Applied && s.ChecksumMismatch:
			fmt.Printf("applied  %s  (applied %s, MODIFIED SINCE APPLIED — file content no longer matches what was recorded)\n", s.Name, s.AppliedAt.Format(time.RFC3339))
		case s.Applied && s.HasDown:
			fmt.Printf("applied  %s  (applied %s)\n", s.Name, s.AppliedAt.Format(time.RFC3339))
		case s.Applied:
			fmt.Printf("applied  %s  (applied %s, no down — can't be reverted)\n", s.Name, s.AppliedAt.Format(time.RFC3339))
		default:
			fmt.Printf("pending  %s\n", s.Name)
		}
	}
	return nil
}

func migrateDown(sqlDB *sql.DB, dir string, n int) error {
	reverted, err := db.MigrateDirDown(sqlDB, dir, n)
	if err != nil {
		return err
	}
	if len(reverted) == 0 {
		fmt.Println("nothing to revert")
		return nil
	}
	for _, name := range reverted {
		fmt.Printf("reverted %s\n", name)
	}
	return nil
}

func migrateRedo(sqlDB *sql.DB, dir string, n int) error {
	redone, err := db.MigrateDirRedo(sqlDB, dir, n)
	if err != nil {
		return err
	}
	if len(redone) == 0 {
		fmt.Println("nothing to redo")
		return nil
	}
	for _, name := range redone {
		fmt.Printf("redone %s\n", name)
	}
	return nil
}

func migrateTo(sqlDB *sql.DB, dir, target string) error {
	touched, direction, err := db.MigrateDirTo(sqlDB, dir, target)
	if err != nil {
		return err
	}
	if len(touched) == 0 {
		fmt.Println("already at that version")
		return nil
	}
	verb := "applied"
	if direction == "down" {
		verb = "reverted"
	}
	for _, name := range touched {
		fmt.Printf("%s %s\n", verb, name)
	}
	return nil
}

// migrateDiff exits non-zero (via a returned error, same as every other
// migrate subcommand's failure convention) when drift is found — this
// is what makes "maubase migrate diff" usable as a CI/deploy check, not
// just an interactive report.
func migrateDiff(sqlDB *sql.DB, dir string) error {
	results, err := db.Diff(sqlDB, dir)
	if err != nil {
		return err
	}
	if len(results) == 0 {
		fmt.Println("no drift: every live table is accounted for by an applied migration")
		return nil
	}
	for _, r := range results {
		switch r.Kind {
		case "unexplained":
			fmt.Printf("unexplained  %s  (exists in the database, not accounted for by any applied migration)\n", r.Table)
		case "missing":
			fmt.Printf("missing      %s  (an applied migration should have created this table, but it doesn't exist)\n", r.Table)
		case "altered":
			fmt.Printf("altered      %s  (live definition differs from what its migration(s) produced)\n", r.Table)
		}
	}
	return fmt.Errorf("drift found in %d table(s), see above", len(results))
}

func printMigrateUsage() {
	fmt.Fprint(os.Stderr, `maubase: a self-hostable backend

Usage:
  maubase                     Start the server
  maubase init [dir]          Scaffold a brand new deployment (migrations/, .env.example, .gitignore)
  maubase migrate new <name>  Scaffold the next-numbered application migration file
  maubase migrate up          Apply pending application migrations
  maubase migrate down [n]    Revert the last n applied migrations (default 1)
  maubase migrate redo [n]    Revert then reapply the last n applied migrations (default 1)
  maubase migrate to <ver>    Move to exactly <ver> (a filename or numeric prefix), forward or back
  maubase migrate status      List application migrations and whether each is applied
  maubase migrate diff        Report tables the database has that no applied migration explains (or vice versa)
  maubase help                Show this message

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
