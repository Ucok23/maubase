package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
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
		return fmt.Errorf("usage: maubase migrate <up|status> [--db path] [--dir path]")
	}
	sub := args[0]
	if sub != "up" && sub != "status" {
		return fmt.Errorf("maubase migrate: unknown subcommand %q (want \"up\" or \"status\")", sub)
	}

	// Defaults come from the same env vars the server itself reads
	// (MAUBASE_DB_PATH, MAUBASE_MIGRATIONS_DIR), so pointing this at a
	// running deployment's database needs no flags at all — only a local
	// dry run against a scratch DB needs to override them.
	cfg := config.Load()
	fs := flag.NewFlagSet("maubase migrate "+sub, flag.ContinueOnError)
	dbPath := fs.String("db", cfg.DBPath, "path to the SQLite database file")
	dir := fs.String("dir", cfg.MigrationsDir, "directory of application migration .sql files")
	if err := fs.Parse(args[1:]); err != nil {
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
	default:
		// Unreachable: sub is validated above.
		return fmt.Errorf("maubase migrate: unknown subcommand %q", sub)
	}
}

func migrateUp(sqlDB *sql.DB, dir string) error {
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
		if s.Applied {
			fmt.Printf("applied  %s  (applied %s)\n", s.Name, s.AppliedAt.Format(time.RFC3339))
		} else {
			fmt.Printf("pending  %s\n", s.Name)
		}
	}
	return nil
}

func printMigrateUsage() {
	fmt.Fprint(os.Stderr, `maubase: a self-hostable backend

Usage:
  maubase                   Start the server
  maubase migrate up        Apply pending application migrations
  maubase migrate status    List application migrations and whether each is applied
  maubase help              Show this message

Flags for "migrate" subcommands:
  -db string    path to the SQLite database file (default: $MAUBASE_DB_PATH, or data/maubase.db)
  -dir string   application migrations directory (default: $MAUBASE_MIGRATIONS_DIR, or migrations)
`)
}
