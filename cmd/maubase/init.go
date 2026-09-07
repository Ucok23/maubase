package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// runInit implements `maubase init [dir]` — see spec/project-init.md.
// It scaffolds a brand new maubase deployment: a starter migrations/
// directory, a .env.example documenting every MAUBASE_* env var, a
// Claude Code skill (skillRelPath) plus a tool-agnostic fallback
// (agentsRelPath) so an agent working in this project understands what
// maubase is without reading its source, and a .gitignore entry for the
// default data/ directory. Meant to run once, against a fresh (or
// not-yet-maubase-configured) directory — it refuses rather than
// overwrites if migrations/, .env.example, or either agent-context file
// already exist, since any of those existing means this project has
// already been initialized.
//
// Two flags change that:
//
//   - --update-agent-docs skips scaffolding entirely and instead
//     refreshes just the skill/AGENTS.md managed blocks — see
//     runUpdateAgentDocs — the one part of `init` that's meant to be
//     re-run, after a maubase upgrade.
//   - --js-client additionally vendors sdk/js/dist into jsClientRelDir —
//     see writeJSClient. Not combined with --update-agent-docs: which
//     new deployment gets a client is a one-time choice made at
//     scaffold time, not something a docs refresh should decide.
func runInit(args []string) error {
	var (
		updateAgentDocs bool
		jsClient        bool
		positional      []string
	)
	for _, a := range args {
		switch a {
		case "--update-agent-docs":
			updateAgentDocs = true
		case "--js-client":
			jsClient = true
		default:
			if strings.HasPrefix(a, "-") {
				return fmt.Errorf("usage: maubase init [--js-client] [dir], or maubase init --update-agent-docs [dir]")
			}
			positional = append(positional, a)
		}
	}

	dir := "."
	switch len(positional) {
	case 0:
		// use default
	case 1:
		dir = positional[0]
	default:
		return fmt.Errorf("usage: maubase init [--js-client] [dir], or maubase init --update-agent-docs [dir]")
	}

	if updateAgentDocs && jsClient {
		return fmt.Errorf("--js-client can't be combined with --update-agent-docs — run maubase init --js-client on its own to vendor the client into an already-initialized project")
	}
	if updateAgentDocs {
		return runUpdateAgentDocs(dir)
	}

	migrationsDir := filepath.Join(dir, "migrations")
	envExamplePath := filepath.Join(dir, ".env.example")
	skillPath := filepath.Join(dir, skillRelPath)
	agentsPath := filepath.Join(dir, agentsRelPath)
	jsClientDir := filepath.Join(dir, jsClientRelDir)

	var conflicts []string
	if _, err := os.Stat(migrationsDir); err == nil {
		conflicts = append(conflicts, migrationsDir)
	}
	if _, err := os.Stat(envExamplePath); err == nil {
		conflicts = append(conflicts, envExamplePath)
	}
	if _, err := os.Stat(skillPath); err == nil {
		conflicts = append(conflicts, skillPath)
	}
	if _, err := os.Stat(agentsPath); err == nil {
		conflicts = append(conflicts, agentsPath)
	}
	if jsClient {
		if _, err := os.Stat(jsClientDir); err == nil {
			conflicts = append(conflicts, jsClientDir)
		}
	}
	if len(conflicts) > 0 {
		return fmt.Errorf("already initialized: %s already exist(s)", strings.Join(conflicts, ", "))
	}

	if err := os.MkdirAll(migrationsDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", migrationsDir, err)
	}
	firstMigrationPath := filepath.Join(migrationsDir, "0001_init.sql")
	if err := os.WriteFile(firstMigrationPath, []byte(initMigrationTemplate), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", firstMigrationPath, err)
	}
	fmt.Printf("created %s\n", migrationsDir)
	fmt.Printf("created %s\n", firstMigrationPath)

	if err := os.WriteFile(envExamplePath, []byte(envExampleTemplate), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", envExamplePath, err)
	}
	fmt.Printf("created %s\n", envExamplePath)

	// The client, if requested, is vendored before the skill/AGENTS.md
	// are rendered below — renderSkill/renderAgentsDoc take jsClient
	// directly here (we already know it from the flag), but writing the
	// directory first keeps this ordering the same "vendor, then
	// document" shape runUpdateAgentDocs uses via hasJSClient(dir).
	if jsClient {
		if err := os.MkdirAll(jsClientDir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", jsClientDir, err)
		}
		n, err := writeJSClient(dir)
		if err != nil {
			return err
		}
		fmt.Printf("created %s (%d files vendored from sdk/js/dist)\n", jsClientDir, n)
	}

	if err := os.MkdirAll(filepath.Dir(skillPath), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(skillPath), err)
	}
	if err := os.WriteFile(skillPath, []byte(renderSkill(jsClient)), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", skillPath, err)
	}
	fmt.Printf("created %s\n", skillPath)

	if err := os.MkdirAll(filepath.Dir(agentsPath), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(agentsPath), err)
	}
	if err := os.WriteFile(agentsPath, []byte(renderAgentsDoc(jsClient)), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", agentsPath, err)
	}
	fmt.Printf("created %s\n", agentsPath)

	gitignorePath := filepath.Join(dir, ".gitignore")
	if err := ensureGitignoreHasDataDir(gitignorePath); err != nil {
		return err
	}

	fmt.Println()
	fmt.Println(`Next steps:
  cp .env.example .env          # fill in the values you need, or set them however you deploy
  maubase migrate new <name>    # scaffold your first real migration
  maubase migrate up            # or just start the server — it applies pending migrations on boot`)
	return nil
}

// ensureGitignoreHasDataDir creates path with a "data/" entry if it
// doesn't exist, or appends "data/" to an existing .gitignore if it's
// not already covered — unlike migrations/ and .env.example above, a
// project frequently already has its own .gitignore for unrelated
// reasons, so overwriting (or refusing to init over) it would be both
// destructive and needlessly strict.
func ensureGitignoreHasDataDir(path string) error {
	const entry = "data/"

	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if err := os.WriteFile(path, []byte(entry+"\n"), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		fmt.Printf("created %s\n", path)
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	for _, line := range strings.Split(string(content), "\n") {
		if strings.TrimSpace(line) == entry || strings.TrimSpace(line) == "/data" || strings.TrimSpace(line) == "/data/" {
			return nil // already covered
		}
	}

	updated := string(content)
	if !strings.HasSuffix(updated, "\n") {
		updated += "\n"
	}
	updated += entry + "\n"
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return fmt.Errorf("update %s: %w", path, err)
	}
	fmt.Printf("updated %s (added %q)\n", path, entry)
	return nil
}

const initMigrationTemplate = `-- Your first application migration. maubase's own embedded schema
-- (users, sessions, oauth clients, etc.) is separate and applied
-- automatically — this file, and every one after it, is entirely
-- yours: add your own CREATE TABLE statements under "+migrate Up" below
-- (or just leave this one empty and run "maubase migrate new <name>"
-- to scaffold your first real one instead).

-- +migrate Up


-- +migrate Down

`

// envExampleTemplate mirrors every MAUBASE_* var in
// internal/config/config.go and README.md's "Running it" section — if
// you add or change one there, update this (and the README) too.
const envExampleTemplate = `# Config for maubase. Copy to .env and fill in what you need — every
# line below shows its default, so anything you don't set just falls
# back to that. See internal/config/config.go and README.md's "Running
# it" section for the full explanation of each.

MAUBASE_ADDR=:8080
MAUBASE_DB_PATH=data/maubase.db
# This server's own public base URL — must be what clients actually use
# to reach it, since it's baked into JWT "iss" claims and discovery
# metadata.
MAUBASE_ISSUER=http://localhost:8080

# Set both to create the first owner-plane account on first run; a
# no-op on every run after.
MAUBASE_BOOTSTRAP_OWNER_EMAIL=
MAUBASE_BOOTSTRAP_OWNER_PASSWORD=

# Where this deployment's own application-schema .sql files live.
MAUBASE_MIGRATIONS_DIR=migrations

# At most this many login attempts per client IP per window.
MAUBASE_LOGIN_RATE_LIMIT=10
MAUBASE_LOGIN_RATE_WINDOW_SECONDS=60

# How often the background janitor purges expired session rows.
MAUBASE_SESSION_CLEANUP_INTERVAL_SECONDS=3600

# Where uploaded file bytes are written, one file per upload.
MAUBASE_STORAGE_DIR=data/storage
MAUBASE_MAX_UPLOAD_MB=25
MAUBASE_MAX_REQUEST_BODY_KB=1024

# Password reset email. All three unset is fine if you don't use it —
# you'll just get a clear error the first time something tries to send.
MAUBASE_RESEND_API_KEY=
MAUBASE_EMAIL_FROM=
MAUBASE_PASSWORD_RESET_URL=

# "Continue with Google/GitHub." Each pair is independently optional —
# leaving one unset just 404s that provider, not a startup error.
MAUBASE_GOOGLE_CLIENT_ID=
MAUBASE_GOOGLE_CLIENT_SECRET=
MAUBASE_GITHUB_CLIENT_ID=
MAUBASE_GITHUB_CLIENT_SECRET=
MAUBASE_SOCIAL_LOGIN_REDIRECT_URL=

# Set to a redis:// URL shared by every instance to upgrade realtime
# fan-out from single-process to cross-process. Leave unset for a
# single-instance deployment.
MAUBASE_REDIS_URL=

# Set to "development" to enable GET /api/schema (live collection/column/
# access-rule introspection, useful for local tooling) — 404s outright
# when this isn't "development". Never set this in production.
MAUBASE_ENV=production
`
