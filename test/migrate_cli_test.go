package e2e_test

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	maubasedb "maubase/internal/db"
)

// Scenarios: spec/migrations-cli.md (MIGCLI-01..07)
//
// Unlike every other test in this package (which talks HTTP to an
// in-process testserver), these exercise `maubase migrate ...` as an
// actual subprocess — a CLI's behavior is its process's stdout/exit code,
// not an HTTP response.

var (
	migrateCLIBuildOnce sync.Once
	migrateCLIBinPath   string
	migrateCLIBuildErr  error
)

// buildMaubaseCLI compiles the real maubase binary once per test process
// and returns its path.
func buildMaubaseCLI(t *testing.T) string {
	t.Helper()
	migrateCLIBuildOnce.Do(func() {
		binDir, err := os.MkdirTemp("", "maubase-cli-test-*")
		if err != nil {
			migrateCLIBuildErr = err
			return
		}
		migrateCLIBinPath = filepath.Join(binDir, "maubase")
		cmd := exec.Command("go", "build", "-o", migrateCLIBinPath, "maubase/cmd/maubase")
		if out, err := cmd.CombinedOutput(); err != nil {
			migrateCLIBuildErr = fmt.Errorf("build maubase CLI: %v\n%s", err, out)
		}
	})
	if migrateCLIBuildErr != nil {
		t.Fatalf("%v", migrateCLIBuildErr)
	}
	return migrateCLIBinPath
}

// runMigrateCLI runs `maubase migrate <args...> --db dbPath --dir dir`
// and returns its stdout, stderr, and exit code.
func runMigrateCLI(t *testing.T, dbPath, dir string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	bin := buildMaubaseCLI(t)
	fullArgs := append([]string{"migrate"}, args...)
	fullArgs = append(fullArgs, "--db", dbPath, "--dir", dir)
	cmd := exec.Command(bin, fullArgs...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			t.Fatalf("run maubase migrate %v: %v", args, err)
		}
	}
	return outBuf.String(), errBuf.String(), exitCode
}

func writeMigrationFile(t *testing.T, dir, name, sqlText string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(sqlText), 0o644); err != nil {
		t.Fatalf("write migration %s: %v", name, err)
	}
}

// tableExists reports whether name is a table in the SQLite database at
// dbPath, without going through the server at all.
func tableExists(t *testing.T, dbPath, name string) bool {
	t.Helper()
	sqlDB, err := maubasedb.Open(dbPath)
	if err != nil {
		t.Fatalf("open %s: %v", dbPath, err)
	}
	defer sqlDB.Close()
	var got string
	err = sqlDB.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&got)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatalf("query sqlite_master for %s: %v", name, err)
	}
	return true
}

func TestMigrateCLI_UpAppliesPendingMigrationsInOrder(t *testing.T) {
	// MIGCLI-01
	dir := t.TempDir()
	writeMigrationFile(t, dir, "0001_create_widgets.sql", `CREATE TABLE widgets (id INTEGER PRIMARY KEY, name TEXT);`)
	writeMigrationFile(t, dir, "0002_add_widgets_index.sql", `CREATE INDEX idx_widgets_name ON widgets(name);`)
	dbPath := filepath.Join(t.TempDir(), "test.db")

	stdout, stderr, code := runMigrateCLI(t, dbPath, dir, "up")
	if code != 0 {
		t.Fatalf("migrate up: want exit 0, got %d, stderr: %s", code, stderr)
	}
	first := strings.Index(stdout, "0001_create_widgets.sql")
	second := strings.Index(stdout, "0002_add_widgets_index.sql")
	if first < 0 || second < 0 || first > second {
		t.Fatalf("want both filenames reported in order, got: %s", stdout)
	}

	if !tableExists(t, dbPath, "widgets") {
		t.Fatalf("want widgets table to exist after migrate up")
	}
}

func TestMigrateCLI_UpIsNoOpWhenAlreadyApplied(t *testing.T) {
	// MIGCLI-02
	dir := t.TempDir()
	writeMigrationFile(t, dir, "0001_create_widgets.sql", `CREATE TABLE widgets (id INTEGER PRIMARY KEY);`)
	dbPath := filepath.Join(t.TempDir(), "test.db")

	if _, stderr, code := runMigrateCLI(t, dbPath, dir, "up"); code != 0 {
		t.Fatalf("setup: first migrate up: want 0, got %d: %s", code, stderr)
	}

	stdout, stderr, code := runMigrateCLI(t, dbPath, dir, "up")
	if code != 0 {
		t.Fatalf("second migrate up: want exit 0, got %d, stderr: %s", code, stderr)
	}
	if strings.Contains(stdout, "0001_create_widgets.sql") {
		t.Fatalf("want no re-application of an already-applied migration, got: %s", stdout)
	}
	if !strings.Contains(stdout, "no pending") {
		t.Fatalf("want output to say nothing is pending, got: %s", stdout)
	}
}

func TestMigrateCLI_UpNeedsNeitherServerNorEmbeddedSchema(t *testing.T) {
	// MIGCLI-03
	dir := t.TempDir()
	writeMigrationFile(t, dir, "0001_create_widgets.sql", `CREATE TABLE widgets (id INTEGER PRIMARY KEY);`)
	dbPath := filepath.Join(t.TempDir(), "test.db")

	if _, stderr, code := runMigrateCLI(t, dbPath, dir, "up"); code != 0 {
		t.Fatalf("migrate up: want exit 0, got %d: %s", code, stderr)
	}

	if !tableExists(t, dbPath, "widgets") {
		t.Fatalf("want the application migration to have applied")
	}
	// The embedded internal schema (db.Migrate) is never applied by this
	// command — only the server itself does that on boot.
	if tableExists(t, dbPath, "users") {
		t.Fatalf("want maubase's own embedded schema (users table) to remain untouched by `migrate up`")
	}
}

func TestMigrateCLI_UpWithMissingDirectoryIsCleanNoOp(t *testing.T) {
	// MIGCLI-04
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	dbPath := filepath.Join(t.TempDir(), "test.db")

	stdout, stderr, code := runMigrateCLI(t, dbPath, dir, "up")
	if code != 0 {
		t.Fatalf("migrate up with missing dir: want exit 0, got %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "no pending") {
		t.Fatalf("want output to say nothing is pending, got: %s", stdout)
	}
}

func TestMigrateCLI_StatusListsAppliedAndPendingWithoutApplying(t *testing.T) {
	// MIGCLI-05
	dir := t.TempDir()
	writeMigrationFile(t, dir, "0001_create_widgets.sql", `CREATE TABLE widgets (id INTEGER PRIMARY KEY);`)
	writeMigrationFile(t, dir, "0002_create_gadgets.sql", `CREATE TABLE gadgets (id INTEGER PRIMARY KEY);`)
	dbPath := filepath.Join(t.TempDir(), "test.db")

	if _, stderr, code := runMigrateCLI(t, dbPath, dir, "up"); code != 0 {
		t.Fatalf("setup: migrate up: want 0, got %d: %s", code, stderr)
	}

	// A third migration lands after the first two were already applied.
	writeMigrationFile(t, dir, "0003_create_thingamajigs.sql", `CREATE TABLE thingamajigs (id INTEGER PRIMARY KEY);`)

	stdout, stderr, code := runMigrateCLI(t, dbPath, dir, "status")
	if code != 0 {
		t.Fatalf("migrate status: want exit 0, got %d: %s", code, stderr)
	}

	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 lines (one per migration), got %d: %s", len(lines), stdout)
	}
	if !strings.HasPrefix(lines[0], "applied") || !strings.Contains(lines[0], "0001_create_widgets.sql") {
		t.Fatalf("want line 1 to report 0001 applied, got: %s", lines[0])
	}
	if !strings.HasPrefix(lines[1], "applied") || !strings.Contains(lines[1], "0002_create_gadgets.sql") {
		t.Fatalf("want line 2 to report 0002 applied, got: %s", lines[1])
	}
	if !strings.HasPrefix(lines[2], "pending") || !strings.Contains(lines[2], "0003_create_thingamajigs.sql") {
		t.Fatalf("want line 3 to report 0003 pending, got: %s", lines[2])
	}

	// status must not have applied the pending one.
	if tableExists(t, dbPath, "thingamajigs") {
		t.Fatalf("want migrate status to not apply anything, but thingamajigs table exists")
	}
}

func TestMigrateCLI_DefaultsComeFromEnvWhenFlagsOmitted(t *testing.T) {
	// MIGCLI-06 (env-var side of the default, exercised without --db/--dir)
	dir := t.TempDir()
	writeMigrationFile(t, dir, "0001_create_widgets.sql", `CREATE TABLE widgets (id INTEGER PRIMARY KEY);`)
	dbPath := filepath.Join(t.TempDir(), "test.db")

	bin := buildMaubaseCLI(t)
	cmd := exec.Command(bin, "migrate", "up")
	cmd.Env = append(os.Environ(),
		"MAUBASE_DB_PATH="+dbPath,
		"MAUBASE_MIGRATIONS_DIR="+dir,
	)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &outBuf, &errBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("migrate up via env vars: %v, stderr: %s", err, errBuf.String())
	}
	if !strings.Contains(outBuf.String(), "0001_create_widgets.sql") {
		t.Fatalf("want the migration applied via env-var defaults, got: %s", outBuf.String())
	}
	if !tableExists(t, dbPath, "widgets") {
		t.Fatalf("want widgets table to exist at the env-var-configured db path")
	}
}

func TestMigrateCLI_UnknownSubcommandFailsWithoutTouchingTheDatabase(t *testing.T) {
	// MIGCLI-07
	dir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "test.db")

	stdout, stderr, code := runMigrateCLI(t, dbPath, dir, "bogus")
	if code == 0 {
		t.Fatalf("want a non-zero exit for an unknown subcommand, got 0, stdout: %s", stdout)
	}
	if !strings.Contains(stderr, "bogus") {
		t.Fatalf("want the error to name the unknown subcommand, got: %s", stderr)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("want the database file never created for an unknown subcommand, stat err: %v", err)
	}
}
