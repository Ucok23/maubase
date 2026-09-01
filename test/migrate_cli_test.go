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

	maubasedb "github.com/Ucok23/maubase/internal/db"
)

// Scenarios: spec/migrations-cli.md (MIGCLI-01..41)
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
		cmd := exec.Command("go", "build", "-o", migrateCLIBinPath, "github.com/Ucok23/maubase/cmd/maubase")
		if out, err := cmd.CombinedOutput(); err != nil {
			migrateCLIBuildErr = fmt.Errorf("build maubase CLI: %v\n%s", err, out)
		}
	})
	if migrateCLIBuildErr != nil {
		t.Fatalf("%v", migrateCLIBuildErr)
	}
	return migrateCLIBinPath
}

// runCLI runs the built maubase binary with args verbatim and returns its
// stdout, stderr, and exit code.
func runCLI(t *testing.T, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	bin := buildMaubaseCLI(t)
	cmd := exec.Command(bin, args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			t.Fatalf("run maubase %v: %v", args, err)
		}
	}
	return outBuf.String(), errBuf.String(), exitCode
}

// runMigrateCLI runs `maubase migrate <args...> --db dbPath --dir dir`
// and returns its stdout, stderr, and exit code.
func runMigrateCLI(t *testing.T, dbPath, dir string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	fullArgs := append([]string{"migrate"}, args...)
	fullArgs = append(fullArgs, "--db", dbPath, "--dir", dir)
	return runCLI(t, fullArgs...)
}

// runCLIInDir runs the built maubase binary with args verbatim, with its
// working directory set to wd — simulating the ordinary "cd into your
// project and just run maubase" invocation, with no --db/--dir flags and
// no MAUBASE_DB_PATH/MAUBASE_MIGRATIONS_DIR overrides, so
// config.Load()'s literal defaults ("data/maubase.db", "migrations")
// resolve relative to wd exactly like they would for a real user.
func runCLIInDir(t *testing.T, wd string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	bin := buildMaubaseCLI(t)
	cmd := exec.Command(bin, args...)
	cmd.Dir = wd
	// Strip any MAUBASE_* the test process's own environment happens to
	// carry, so this genuinely exercises config.Load()'s hard-coded
	// defaults rather than whatever ambient config this test suite runs
	// under.
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "MAUBASE_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			t.Fatalf("run maubase %v (in %s): %v", args, wd, err)
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

// upDownMigration builds a migration file's content with a "-- +migrate
// Up" section and, if down is non-empty, a "-- +migrate Down" section.
func upDownMigration(up, down string) string {
	s := "-- +migrate Up\n" + up + "\n"
	if down != "" {
		s += "\n-- +migrate Down\n" + down + "\n"
	}
	return s
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

// execSQL runs a write query directly against the SQLite database at
// dbPath, without going through the server or the migrate CLI at all —
// used to simulate database state the CLI itself never produces (e.g. a
// pre-existing schema_migrations row with no checksum).
func execSQL(t *testing.T, dbPath, query string, args ...any) {
	t.Helper()
	sqlDB, err := maubasedb.Open(dbPath)
	if err != nil {
		t.Fatalf("open %s: %v", dbPath, err)
	}
	defer sqlDB.Close()
	if _, err := sqlDB.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
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

func TestMigrateCLI_NewScaffoldsNextNumberedFile(t *testing.T) {
	// MIGCLI-08
	dir := t.TempDir()
	writeMigrationFile(t, dir, "0001_create_widgets.sql", `CREATE TABLE widgets (id INTEGER PRIMARY KEY);`)

	stdout, stderr, code := runCLI(t, "migrate", "new", "add widgets index", "--dir", dir)
	if code != 0 {
		t.Fatalf("migrate new: want exit 0, got %d: %s", code, stderr)
	}
	wantPath := filepath.Join(dir, "0002_add_widgets_index.sql")
	if !strings.Contains(stdout, wantPath) {
		t.Fatalf("want the created path %q reported, got: %s", wantPath, stdout)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("want %s to exist: %v", wantPath, err)
	}
}

func TestMigrateCLI_NewCreatesTheMigrationsDirectoryIfMissing(t *testing.T) {
	// MIGCLI-09
	dir := filepath.Join(t.TempDir(), "not-yet-created")

	stdout, stderr, code := runCLI(t, "migrate", "new", "init", "--dir", dir)
	if code != 0 {
		t.Fatalf("migrate new: want exit 0, got %d: %s", code, stderr)
	}
	wantPath := filepath.Join(dir, "0001_init.sql")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("want %s to exist (dir auto-created): %v", wantPath, err)
	}
	if !strings.Contains(stdout, wantPath) {
		t.Fatalf("want the created path reported, got: %s", stdout)
	}
}

func TestMigrateCLI_NewNumbersByHighestExistingNotFileCount(t *testing.T) {
	// MIGCLI-10
	dir := t.TempDir()
	writeMigrationFile(t, dir, "0002_second.sql", `CREATE TABLE second (id INTEGER PRIMARY KEY);`)
	writeMigrationFile(t, dir, "0003_third.sql", `CREATE TABLE third (id INTEGER PRIMARY KEY);`)
	// Note: no 0001_ file — it was deleted. Only 2 files exist, but the
	// next one must be 0004, not 0003 (file count) or 0002 (a naive
	// "count + 1" off the survivors).

	stdout, stderr, code := runCLI(t, "migrate", "new", "fourth", "--dir", dir)
	if code != 0 {
		t.Fatalf("migrate new: want exit 0, got %d: %s", code, stderr)
	}
	wantPath := filepath.Join(dir, "0004_fourth.sql")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("want %s to exist: %v", wantPath, err)
	}
	if !strings.Contains(stdout, wantPath) {
		t.Fatalf("want the created path reported, got: %s", stdout)
	}
}

func TestMigrateCLI_NewRequiresAName(t *testing.T) {
	// MIGCLI-11
	dir := filepath.Join(t.TempDir(), "should-not-be-created")

	stdout, stderr, code := runCLI(t, "migrate", "new", "--dir", dir)
	if code == 0 {
		t.Fatalf("want a non-zero exit for a missing name, got 0, stdout: %s", stdout)
	}
	// Specifically the "new"-with-no-name usage message, not e.g. an
	// unrecognized-subcommand error that would also happen to satisfy a
	// bare "stderr is non-empty" check.
	if !strings.Contains(stderr, "maubase migrate new <name>") {
		t.Fatalf("want the missing-name usage message, got: %s", stderr)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("want no directory created when the name is missing, stat err: %v", err)
	}
}

func TestMigrateCLI_NewUnrecognizedFlagFailsInsteadOfPollutingTheName(t *testing.T) {
	// MIGCLI-12
	dir := t.TempDir()

	stdout, stderr, code := runCLI(t, "migrate", "new", "create posts", "--bogus-flag", "--dir", dir)
	if code == 0 {
		t.Fatalf("want a non-zero exit for an unrecognized flag, got 0, stdout: %s", stdout)
	}
	if !strings.Contains(stderr, "--bogus-flag") {
		t.Fatalf("want the error to name the unrecognized flag, got: %s", stderr)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("want no file created when a flag is unrecognized, got: %v", entries)
	}
}

func TestMigrateCLI_UpOnlyRunsTheUpSection(t *testing.T) {
	// MIGCLI-13
	dir := t.TempDir()
	writeMigrationFile(t, dir, "0001_foo.sql", upDownMigration(
		`CREATE TABLE foo (id INTEGER PRIMARY KEY);`,
		`DROP TABLE foo;`,
	))
	dbPath := filepath.Join(t.TempDir(), "test.db")

	if _, stderr, code := runMigrateCLI(t, dbPath, dir, "up"); code != 0 {
		t.Fatalf("migrate up: want exit 0, got %d: %s", code, stderr)
	}
	if !tableExists(t, dbPath, "foo") {
		t.Fatalf("want the Up section's table to exist — the Down section's DROP TABLE must not have run during apply")
	}
}

func TestMigrateCLI_DownRevertsMostRecentByDefault(t *testing.T) {
	// MIGCLI-14
	dir := t.TempDir()
	writeMigrationFile(t, dir, "0001_create_a.sql", upDownMigration(
		`CREATE TABLE a (id INTEGER PRIMARY KEY);`, `DROP TABLE a;`,
	))
	writeMigrationFile(t, dir, "0002_create_b.sql", upDownMigration(
		`CREATE TABLE b (id INTEGER PRIMARY KEY);`, `DROP TABLE b;`,
	))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if _, stderr, code := runMigrateCLI(t, dbPath, dir, "up"); code != 0 {
		t.Fatalf("setup: migrate up: want 0, got %d: %s", code, stderr)
	}

	stdout, stderr, code := runMigrateCLI(t, dbPath, dir, "down")
	if code != 0 {
		t.Fatalf("migrate down: want exit 0, got %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "0002_create_b.sql") {
		t.Fatalf("want 0002 reported reverted, got: %s", stdout)
	}
	if strings.Contains(stdout, "0001_create_a.sql") {
		t.Fatalf("want only the most recent migration reverted, got: %s", stdout)
	}
	if tableExists(t, dbPath, "b") {
		t.Fatalf("want table b dropped by the revert")
	}
	if !tableExists(t, dbPath, "a") {
		t.Fatalf("want table a (older migration) left alone")
	}
}

func TestMigrateCLI_DownNRevertsNewestFirst(t *testing.T) {
	// MIGCLI-15
	dir := t.TempDir()
	writeMigrationFile(t, dir, "0001_create_a.sql", upDownMigration(`CREATE TABLE a (id INTEGER PRIMARY KEY);`, `DROP TABLE a;`))
	writeMigrationFile(t, dir, "0002_create_b.sql", upDownMigration(`CREATE TABLE b (id INTEGER PRIMARY KEY);`, `DROP TABLE b;`))
	writeMigrationFile(t, dir, "0003_create_c.sql", upDownMigration(`CREATE TABLE c (id INTEGER PRIMARY KEY);`, `DROP TABLE c;`))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if _, stderr, code := runMigrateCLI(t, dbPath, dir, "up"); code != 0 {
		t.Fatalf("setup: migrate up: want 0, got %d: %s", code, stderr)
	}

	stdout, stderr, code := runMigrateCLI(t, dbPath, dir, "down", "2")
	if code != 0 {
		t.Fatalf("migrate down 2: want exit 0, got %d: %s", code, stderr)
	}
	first := strings.Index(stdout, "0003_create_c.sql")
	second := strings.Index(stdout, "0002_create_b.sql")
	if first < 0 || second < 0 || first > second {
		t.Fatalf("want 0003 reverted before 0002 (newest first), got: %s", stdout)
	}
	if tableExists(t, dbPath, "b") || tableExists(t, dbPath, "c") {
		t.Fatalf("want both b and c dropped")
	}
	if !tableExists(t, dbPath, "a") {
		t.Fatalf("want the oldest migration (a) left applied")
	}
}

func TestMigrateCLI_DownWithNoDownSectionLeavesStateUnchanged(t *testing.T) {
	// MIGCLI-16
	dir := t.TempDir()
	writeMigrationFile(t, dir, "0001_create_a.sql", upDownMigration(`CREATE TABLE a (id INTEGER PRIMARY KEY);`, `DROP TABLE a;`))
	// No down section for this one.
	writeMigrationFile(t, dir, "0002_create_b.sql", upDownMigration(`CREATE TABLE b (id INTEGER PRIMARY KEY);`, ""))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if _, stderr, code := runMigrateCLI(t, dbPath, dir, "up"); code != 0 {
		t.Fatalf("setup: migrate up: want 0, got %d: %s", code, stderr)
	}

	_, stderr, code := runMigrateCLI(t, dbPath, dir, "down")
	if code == 0 {
		t.Fatalf("want a non-zero exit when the migration to revert has no down section")
	}
	if !strings.Contains(stderr, "0002_create_b.sql") {
		t.Fatalf("want the error to name the un-revertible migration, got: %s", stderr)
	}
	if !tableExists(t, dbPath, "b") {
		t.Fatalf("want table b left untouched (still applied) after the failed revert")
	}

	stdout, stderr, code := runMigrateCLI(t, dbPath, dir, "status")
	if code != 0 {
		t.Fatalf("migrate status: want exit 0, got %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "0002_create_b.sql") || !strings.Contains(stdout, "applied") {
		t.Fatalf("want 0002 still reported applied after the failed revert, got: %s", stdout)
	}
}

func TestMigrateCLI_RevertedMigrationIsPendingAndReappliedByUp(t *testing.T) {
	// MIGCLI-17
	dir := t.TempDir()
	writeMigrationFile(t, dir, "0001_create_a.sql", upDownMigration(`CREATE TABLE a (id INTEGER PRIMARY KEY);`, `DROP TABLE a;`))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if _, stderr, code := runMigrateCLI(t, dbPath, dir, "up"); code != 0 {
		t.Fatalf("setup: migrate up: want 0, got %d: %s", code, stderr)
	}
	if _, stderr, code := runMigrateCLI(t, dbPath, dir, "down"); code != 0 {
		t.Fatalf("setup: migrate down: want 0, got %d: %s", code, stderr)
	}
	if tableExists(t, dbPath, "a") {
		t.Fatalf("setup: want table a dropped by the revert")
	}

	stdout, stderr, code := runMigrateCLI(t, dbPath, dir, "up")
	if code != 0 {
		t.Fatalf("migrate up after down: want exit 0, got %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "0001_create_a.sql") {
		t.Fatalf("want the reverted migration reapplied and reported, got: %s", stdout)
	}
	if !tableExists(t, dbPath, "a") {
		t.Fatalf("want table a recreated by the reapply")
	}
}

func TestMigrateCLI_DownWithNothingAppliedIsCleanNoOp(t *testing.T) {
	// MIGCLI-18
	dir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "test.db")

	stdout, stderr, code := runMigrateCLI(t, dbPath, dir, "down")
	if code != 0 {
		t.Fatalf("migrate down: want exit 0, got %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "nothing to revert") {
		t.Fatalf("want a clean-no-op message, got: %s", stdout)
	}
}

func TestMigrateCLI_DownUnrecognizedFlagFailsClearly(t *testing.T) {
	// MIGCLI-19
	dir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "test.db")

	stdout, stderr, code := runMigrateCLI(t, dbPath, dir, "down", "--bogus-flag")
	if code == 0 {
		t.Fatalf("want a non-zero exit for an unrecognized flag, got 0, stdout: %s", stdout)
	}
	if !strings.Contains(stderr, "--bogus-flag") {
		t.Fatalf("want the error to name the unrecognized flag, got: %s", stderr)
	}
}

func TestMigrateCLI_RedoRevertsAndReappliesMostRecentByDefault(t *testing.T) {
	// MIGCLI-20
	dir := t.TempDir()
	writeMigrationFile(t, dir, "0001_create_a.sql", upDownMigration(`CREATE TABLE a (id INTEGER PRIMARY KEY);`, `DROP TABLE a;`))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if _, stderr, code := runMigrateCLI(t, dbPath, dir, "up"); code != 0 {
		t.Fatalf("setup: migrate up: want 0, got %d: %s", code, stderr)
	}

	stdout, stderr, code := runMigrateCLI(t, dbPath, dir, "redo")
	if code != 0 {
		t.Fatalf("migrate redo: want exit 0, got %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "0001_create_a.sql") {
		t.Fatalf("want the migration reported redone, got: %s", stdout)
	}
	if !tableExists(t, dbPath, "a") {
		t.Fatalf("want table a to exist after redo (reapplied)")
	}
	status, stderr, code := runMigrateCLI(t, dbPath, dir, "status")
	if code != 0 {
		t.Fatalf("migrate status: want exit 0, got %d: %s", code, stderr)
	}
	if !strings.Contains(status, "applied") || !strings.Contains(status, "0001_create_a.sql") {
		t.Fatalf("want 0001 reported applied after redo, got: %s", status)
	}
}

func TestMigrateCLI_RedoNReappliesInOriginalForwardOrder(t *testing.T) {
	// MIGCLI-21
	dir := t.TempDir()
	writeMigrationFile(t, dir, "0001_create_a.sql", upDownMigration(`CREATE TABLE a (id INTEGER PRIMARY KEY);`, `DROP TABLE a;`))
	writeMigrationFile(t, dir, "0002_create_b.sql", upDownMigration(`CREATE TABLE b (id INTEGER PRIMARY KEY);`, `DROP TABLE b;`))
	writeMigrationFile(t, dir, "0003_create_c.sql", upDownMigration(`CREATE TABLE c (id INTEGER PRIMARY KEY);`, `DROP TABLE c;`))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if _, stderr, code := runMigrateCLI(t, dbPath, dir, "up"); code != 0 {
		t.Fatalf("setup: migrate up: want 0, got %d: %s", code, stderr)
	}

	stdout, stderr, code := runMigrateCLI(t, dbPath, dir, "redo", "2")
	if code != 0 {
		t.Fatalf("migrate redo 2: want exit 0, got %d: %s", code, stderr)
	}
	// Reapplied oldest-of-the-two first: 0002 before 0003.
	first := strings.Index(stdout, "0002_create_b.sql")
	second := strings.Index(stdout, "0003_create_c.sql")
	if first < 0 || second < 0 || first > second {
		t.Fatalf("want 0002 redone before 0003 (original forward order), got: %s", stdout)
	}
	if !tableExists(t, dbPath, "a") || !tableExists(t, dbPath, "b") || !tableExists(t, dbPath, "c") {
		t.Fatalf("want all three tables to exist after redo")
	}
}

func TestMigrateCLI_RedoNeverTouchesAMigrationThatWasNeverApplied(t *testing.T) {
	// MIGCLI-22
	dir := t.TempDir()
	writeMigrationFile(t, dir, "0001_create_a.sql", upDownMigration(`CREATE TABLE a (id INTEGER PRIMARY KEY);`, `DROP TABLE a;`))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if _, stderr, code := runMigrateCLI(t, dbPath, dir, "up"); code != 0 {
		t.Fatalf("setup: migrate up: want 0, got %d: %s", code, stderr)
	}
	// Added after 0001 was applied; never applied itself.
	writeMigrationFile(t, dir, "0002_never_applied.sql", `-- +migrate Up
CREATE TABLE b (id INTEGER PRIMARY KEY);
`)

	stdout, stderr, code := runMigrateCLI(t, dbPath, dir, "redo")
	if code != 0 {
		t.Fatalf("migrate redo: want exit 0, got %d: %s", code, stderr)
	}
	if strings.Contains(stdout, "0002_never_applied.sql") {
		t.Fatalf("want redo to leave the never-applied migration alone, got: %s", stdout)
	}
	if tableExists(t, dbPath, "b") {
		t.Fatalf("want table b to NOT exist — redo must not have applied the pending migration as a side effect")
	}

	status, stderr, code := runMigrateCLI(t, dbPath, dir, "status")
	if code != 0 {
		t.Fatalf("migrate status: want exit 0, got %d: %s", code, stderr)
	}
	idx := strings.Index(status, "0002_never_applied.sql")
	if idx < 0 || !strings.HasPrefix(status[max(0, idx-9):idx], "pending") {
		t.Fatalf("want 0002 still reported pending, got: %s", status)
	}
}

func TestMigrateCLI_RedoFailsAndReappliesNothingWithNoDownSection(t *testing.T) {
	// MIGCLI-23
	dir := t.TempDir()
	writeMigrationFile(t, dir, "0001_no_down.sql", `-- +migrate Up
CREATE TABLE a (id INTEGER PRIMARY KEY);
`)
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if _, stderr, code := runMigrateCLI(t, dbPath, dir, "up"); code != 0 {
		t.Fatalf("setup: migrate up: want 0, got %d: %s", code, stderr)
	}

	_, stderr, code := runMigrateCLI(t, dbPath, dir, "redo")
	if code == 0 {
		t.Fatalf("want a non-zero exit when the migration to redo has no down section")
	}
	if !strings.Contains(stderr, "0001_no_down.sql") {
		t.Fatalf("want the error to name the un-revertible migration, got: %s", stderr)
	}
	if !tableExists(t, dbPath, "a") {
		t.Fatalf("want table a left untouched (still applied) after the failed redo")
	}
}

func TestMigrateCLI_UpRecordsAChecksumForEachAppliedMigration(t *testing.T) {
	// MIGCLI-24
	dir := t.TempDir()
	writeMigrationFile(t, dir, "0001_create_a.sql", upDownMigration(`CREATE TABLE a (id INTEGER PRIMARY KEY);`, `DROP TABLE a;`))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if _, stderr, code := runMigrateCLI(t, dbPath, dir, "up"); code != 0 {
		t.Fatalf("migrate up: want exit 0, got %d: %s", code, stderr)
	}

	sqlDB, err := maubasedb.Open(dbPath)
	if err != nil {
		t.Fatalf("open %s: %v", dbPath, err)
	}
	defer sqlDB.Close()
	var checksum sql.NullString
	if err := sqlDB.QueryRow(`SELECT checksum FROM schema_migrations WHERE version = 'app:0001_create_a.sql'`).Scan(&checksum); err != nil {
		t.Fatalf("query checksum: %v", err)
	}
	if !checksum.Valid || len(checksum.String) != 64 {
		t.Fatalf("want a recorded 64-char (sha256 hex) checksum, got %q (valid=%v)", checksum.String, checksum.Valid)
	}
}

func TestMigrateCLI_StatusFlagsModifiedMigrationFile(t *testing.T) {
	// MIGCLI-25
	dir := t.TempDir()
	writeMigrationFile(t, dir, "0001_create_a.sql", upDownMigration(`CREATE TABLE a (id INTEGER PRIMARY KEY);`, `DROP TABLE a;`))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if _, stderr, code := runMigrateCLI(t, dbPath, dir, "up"); code != 0 {
		t.Fatalf("setup: migrate up: want 0, got %d: %s", code, stderr)
	}

	// Edit the file after it's already been applied.
	writeMigrationFile(t, dir, "0001_create_a.sql", upDownMigration(`CREATE TABLE a (id INTEGER PRIMARY KEY, extra TEXT);`, `DROP TABLE a;`))

	stdout, stderr, code := runMigrateCLI(t, dbPath, dir, "status")
	if code != 0 {
		t.Fatalf("migrate status: want exit 0, got %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "0001_create_a.sql") || !strings.Contains(stdout, "MODIFIED SINCE APPLIED") {
		t.Fatalf("want the modified migration flagged, got: %s", stdout)
	}
}

func TestMigrateCLI_StatusDoesNotFlagMigrationWithNoStoredChecksum(t *testing.T) {
	// MIGCLI-26
	dir := t.TempDir()
	writeMigrationFile(t, dir, "0001_create_a.sql", upDownMigration(`CREATE TABLE a (id INTEGER PRIMARY KEY);`, `DROP TABLE a;`))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if _, stderr, code := runMigrateCLI(t, dbPath, dir, "up"); code != 0 {
		t.Fatalf("setup: migrate up: want 0, got %d: %s", code, stderr)
	}

	// Simulate a pre-existing row from before checksum verification
	// existed, then edit the file too — even so, with no checksum to
	// compare against, this must not be reported as modified.
	execSQL(t, dbPath, `UPDATE schema_migrations SET checksum = NULL WHERE version = 'app:0001_create_a.sql'`)
	writeMigrationFile(t, dir, "0001_create_a.sql", upDownMigration(`CREATE TABLE a (id INTEGER PRIMARY KEY, extra TEXT);`, `DROP TABLE a;`))

	stdout, stderr, code := runMigrateCLI(t, dbPath, dir, "status")
	if code != 0 {
		t.Fatalf("migrate status: want exit 0, got %d: %s", code, stderr)
	}
	if strings.Contains(stdout, "MODIFIED SINCE APPLIED") {
		t.Fatalf("want no modified flag when there's no stored checksum to compare against, got: %s", stdout)
	}
	if !strings.Contains(stdout, "applied") || !strings.Contains(stdout, "0001_create_a.sql") {
		t.Fatalf("want it still reported as an ordinary applied migration, got: %s", stdout)
	}
}

func TestMigrateCLI_UpWarnsButDoesNotBlockOnModifiedMigration(t *testing.T) {
	// MIGCLI-27
	dir := t.TempDir()
	writeMigrationFile(t, dir, "0001_create_a.sql", upDownMigration(`CREATE TABLE a (id INTEGER PRIMARY KEY);`, `DROP TABLE a;`))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if _, stderr, code := runMigrateCLI(t, dbPath, dir, "up"); code != 0 {
		t.Fatalf("setup: migrate up: want 0, got %d: %s", code, stderr)
	}

	// Modify the already-applied migration, and add an unrelated pending one.
	writeMigrationFile(t, dir, "0001_create_a.sql", upDownMigration(`CREATE TABLE a (id INTEGER PRIMARY KEY, extra TEXT);`, `DROP TABLE a;`))
	writeMigrationFile(t, dir, "0002_create_b.sql", upDownMigration(`CREATE TABLE b (id INTEGER PRIMARY KEY);`, `DROP TABLE b;`))

	stdout, stderr, code := runMigrateCLI(t, dbPath, dir, "up")
	if code != 0 {
		t.Fatalf("migrate up: want exit 0 (warn, don't block), got %d: %s", code, stderr)
	}
	if !strings.Contains(stderr, "0001_create_a.sql") {
		t.Fatalf("want a warning naming the modified migration on stderr, got: %s", stderr)
	}
	if !strings.Contains(stdout, "0002_create_b.sql") {
		t.Fatalf("want the unrelated pending migration still applied and reported, got: %s", stdout)
	}
	if !tableExists(t, dbPath, "b") {
		t.Fatalf("want table b to exist — up must not have been blocked by the warning")
	}
}

func TestMigrateCLI_RelativeDefaultsResolveAgainstCWD(t *testing.T) {
	// MIGCLI-28
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, "migrations"), 0o755); err != nil {
		t.Fatalf("setup: mkdir migrations: %v", err)
	}
	writeMigrationFile(t, filepath.Join(projectDir, "migrations"), "0001_create_widgets.sql", `CREATE TABLE widgets (id INTEGER PRIMARY KEY);`)

	// No --db/--dir, no MAUBASE_DB_PATH/MAUBASE_MIGRATIONS_DIR — just
	// "cd into the project and run it," like the README's own quickstart.
	stdout, stderr, code := runCLIInDir(t, projectDir, "migrate", "up")
	if code != 0 {
		t.Fatalf("migrate up (cwd-relative defaults): want exit 0, got %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "0001_create_widgets.sql") {
		t.Fatalf("want the migration applied, got: %s", stdout)
	}

	wantDBPath := filepath.Join(projectDir, "data", "maubase.db")
	if _, err := os.Stat(wantDBPath); err != nil {
		t.Fatalf("want the default db path (data/maubase.db) resolved relative to the project dir: %v", err)
	}
	if !tableExists(t, wantDBPath, "widgets") {
		t.Fatalf("want widgets table to exist at %s", wantDBPath)
	}

	status, stderr, code := runCLIInDir(t, projectDir, "migrate", "status")
	if code != 0 {
		t.Fatalf("migrate status (cwd-relative defaults): want exit 0, got %d: %s", code, stderr)
	}
	if !strings.Contains(status, "applied") || !strings.Contains(status, "0001_create_widgets.sql") {
		t.Fatalf("want 0001 reported applied, got: %s", status)
	}
}

func TestMigrateCLI_NewWithNoFlagsCreatesMigrationsRelativeToCWD(t *testing.T) {
	// MIGCLI-28
	projectDir := t.TempDir() // a brand new project — no migrations/ yet at all.

	stdout, stderr, code := runCLIInDir(t, projectDir, "migrate", "new", "create_posts")
	if code != 0 {
		t.Fatalf("migrate new (cwd-relative defaults): want exit 0, got %d: %s", code, stderr)
	}
	// Reported relative to cwd ("migrations/0001_create_posts.sql"), same
	// as any ordinary CLI tool — not re-anchored to an absolute path.
	if !strings.Contains(stdout, filepath.Join("migrations", "0001_create_posts.sql")) {
		t.Fatalf("want the created path reported, got: %s", stdout)
	}
	wantPath := filepath.Join(projectDir, "migrations", "0001_create_posts.sql")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("want %s to exist (migrations/ created relative to the project dir): %v", wantPath, err)
	}
}

func threeMigrationsWithDown(t *testing.T, dir string) {
	t.Helper()
	writeMigrationFile(t, dir, "0001_create_a.sql", upDownMigration(`CREATE TABLE a (id INTEGER PRIMARY KEY);`, `DROP TABLE a;`))
	writeMigrationFile(t, dir, "0002_create_b.sql", upDownMigration(`CREATE TABLE b (id INTEGER PRIMARY KEY);`, `DROP TABLE b;`))
	writeMigrationFile(t, dir, "0003_create_c.sql", upDownMigration(`CREATE TABLE c (id INTEGER PRIMARY KEY);`, `DROP TABLE c;`))
}

func TestMigrateCLI_ToAppliesForwardToAnAheadTarget(t *testing.T) {
	// MIGCLI-29
	dir := t.TempDir()
	threeMigrationsWithDown(t, dir)
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if _, stderr, code := runMigrateCLI(t, dbPath, dir, "to", "0001_create_a.sql"); code != 0 {
		t.Fatalf("setup: migrate to 0001: want 0, got %d: %s", code, stderr)
	}

	stdout, stderr, code := runMigrateCLI(t, dbPath, dir, "to", "0003_create_c.sql")
	if code != 0 {
		t.Fatalf("migrate to 0003: want exit 0, got %d: %s", code, stderr)
	}
	first := strings.Index(stdout, "0002_create_b.sql")
	second := strings.Index(stdout, "0003_create_c.sql")
	if first < 0 || second < 0 || first > second {
		t.Fatalf("want 0002 applied before 0003, got: %s", stdout)
	}
	if !tableExists(t, dbPath, "a") || !tableExists(t, dbPath, "b") || !tableExists(t, dbPath, "c") {
		t.Fatalf("want all three tables to exist")
	}
}

func TestMigrateCLI_ToRevertsBackToABehindTarget(t *testing.T) {
	// MIGCLI-30
	dir := t.TempDir()
	threeMigrationsWithDown(t, dir)
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if _, stderr, code := runMigrateCLI(t, dbPath, dir, "up"); code != 0 {
		t.Fatalf("setup: migrate up: want 0, got %d: %s", code, stderr)
	}

	stdout, stderr, code := runMigrateCLI(t, dbPath, dir, "to", "0001_create_a.sql")
	if code != 0 {
		t.Fatalf("migrate to 0001: want exit 0, got %d: %s", code, stderr)
	}
	first := strings.Index(stdout, "0003_create_c.sql")
	second := strings.Index(stdout, "0002_create_b.sql")
	if first < 0 || second < 0 || first > second {
		t.Fatalf("want 0003 reverted before 0002 (newest first), got: %s", stdout)
	}
	if tableExists(t, dbPath, "b") || tableExists(t, dbPath, "c") {
		t.Fatalf("want tables b and c dropped")
	}
	if !tableExists(t, dbPath, "a") {
		t.Fatalf("want table a (the target) left applied")
	}
}

func TestMigrateCLI_ToCurrentStateIsANoOp(t *testing.T) {
	// MIGCLI-31
	dir := t.TempDir()
	threeMigrationsWithDown(t, dir)
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if _, stderr, code := runMigrateCLI(t, dbPath, dir, "up"); code != 0 {
		t.Fatalf("setup: migrate up: want 0, got %d: %s", code, stderr)
	}

	stdout, stderr, code := runMigrateCLI(t, dbPath, dir, "to", "0003_create_c.sql")
	if code != 0 {
		t.Fatalf("migrate to (already there): want exit 0, got %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "already at that version") {
		t.Fatalf("want a no-op message, got: %s", stdout)
	}
}

func TestMigrateCLI_ToAcceptsExactFilenameOrNumericPrefix(t *testing.T) {
	// MIGCLI-32
	dir := t.TempDir()
	threeMigrationsWithDown(t, dir)

	// Bare numeric prefix ("2") reaches the same result as the exact filename.
	dbPath1 := filepath.Join(t.TempDir(), "test1.db")
	if _, stderr, code := runMigrateCLI(t, dbPath1, dir, "to", "0002_create_b.sql"); code != 0 {
		t.Fatalf("to exact filename: want 0, got %d: %s", code, stderr)
	}
	dbPath2 := filepath.Join(t.TempDir(), "test2.db")
	if _, stderr, code := runMigrateCLI(t, dbPath2, dir, "to", "2"); code != 0 {
		t.Fatalf("to numeric prefix: want 0, got %d: %s", code, stderr)
	}

	for _, dbPath := range []string{dbPath1, dbPath2} {
		if !tableExists(t, dbPath, "a") || !tableExists(t, dbPath, "b") {
			t.Fatalf("want a and b applied at %s", dbPath)
		}
		if tableExists(t, dbPath, "c") {
			t.Fatalf("want c NOT applied at %s", dbPath)
		}
	}
}

func TestMigrateCLI_ToFailsClearlyWhenVersionUnresolved(t *testing.T) {
	// MIGCLI-33
	dir := t.TempDir()
	threeMigrationsWithDown(t, dir)
	dbPath := filepath.Join(t.TempDir(), "test.db")

	// No match at all.
	_, stderr, code := runMigrateCLI(t, dbPath, dir, "to", "99")
	if code == 0 {
		t.Fatalf("want a non-zero exit for an unresolvable version")
	}
	if !strings.Contains(stderr, "99") {
		t.Fatalf("want the error to name the unresolved version, got: %s", stderr)
	}
	if tableExists(t, dbPath, "a") {
		t.Fatalf("want nothing applied when the target can't be resolved")
	}

	// Ambiguous match: two files whose numeric prefixes both equal 3.
	dir2 := t.TempDir()
	writeMigrationFile(t, dir2, "3_x.sql", upDownMigration(`CREATE TABLE x (id INTEGER PRIMARY KEY);`, ""))
	writeMigrationFile(t, dir2, "0003_y.sql", upDownMigration(`CREATE TABLE y (id INTEGER PRIMARY KEY);`, ""))
	dbPath2 := filepath.Join(t.TempDir(), "test2.db")
	_, stderr2, code2 := runMigrateCLI(t, dbPath2, dir2, "to", "3")
	if code2 == 0 {
		t.Fatalf("want a non-zero exit for an ambiguous version")
	}
	if !strings.Contains(stderr2, "3_x.sql") || !strings.Contains(stderr2, "0003_y.sql") {
		t.Fatalf("want the error to name both ambiguous matches, got: %s", stderr2)
	}
}

func TestMigrateCLI_ToFailsOnNoDownSectionLeavingStateAsFarAsItGot(t *testing.T) {
	// MIGCLI-34
	dir := t.TempDir()
	writeMigrationFile(t, dir, "0001_create_a.sql", upDownMigration(`CREATE TABLE a (id INTEGER PRIMARY KEY);`, `DROP TABLE a;`))
	writeMigrationFile(t, dir, "0002_no_down.sql", upDownMigration(`CREATE TABLE b (id INTEGER PRIMARY KEY);`, ""))
	writeMigrationFile(t, dir, "0003_create_c.sql", upDownMigration(`CREATE TABLE c (id INTEGER PRIMARY KEY);`, `DROP TABLE c;`))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if _, stderr, code := runMigrateCLI(t, dbPath, dir, "up"); code != 0 {
		t.Fatalf("setup: migrate up: want 0, got %d: %s", code, stderr)
	}

	_, stderr, code := runMigrateCLI(t, dbPath, dir, "to", "0001_create_a.sql")
	if code == 0 {
		t.Fatalf("want a non-zero exit when a migration on the way has no down section")
	}
	if !strings.Contains(stderr, "0002_no_down.sql") {
		t.Fatalf("want the error to name the un-revertible migration, got: %s", stderr)
	}
	// 0003 (newest, reverted first) succeeded before 0002 blocked the rest.
	if tableExists(t, dbPath, "c") {
		t.Fatalf("want table c already reverted before the failure")
	}
	if !tableExists(t, dbPath, "b") || !tableExists(t, dbPath, "a") {
		t.Fatalf("want tables a and b left untouched (still applied) after the failure")
	}
}

// schemaSnapshot returns a stable string summarizing every table's
// definition in dbPath — used to confirm migrate diff never modifies
// the database it inspects.
func schemaSnapshot(t *testing.T, dbPath string) string {
	t.Helper()
	sqlDB, err := maubasedb.Open(dbPath)
	if err != nil {
		t.Fatalf("open %s: %v", dbPath, err)
	}
	defer sqlDB.Close()
	rows, err := sqlDB.Query(`SELECT name, sql FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var name, sqlText string
		if err := rows.Scan(&name, &sqlText); err != nil {
			t.Fatalf("scan sqlite_master row: %v", err)
		}
		fmt.Fprintf(&b, "%s: %s\n", name, sqlText)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate sqlite_master: %v", err)
	}
	return b.String()
}

func TestMigrateCLI_DiffReportsUnexplainedTable(t *testing.T) {
	// MIGCLI-35
	dir := t.TempDir()
	writeMigrationFile(t, dir, "0001_create_posts.sql", upDownMigration(`CREATE TABLE posts (id INTEGER PRIMARY KEY);`, `DROP TABLE posts;`))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if _, stderr, code := runMigrateCLI(t, dbPath, dir, "up"); code != 0 {
		t.Fatalf("setup: migrate up: want 0, got %d: %s", code, stderr)
	}
	execSQL(t, dbPath, `CREATE TABLE widgets (id INTEGER PRIMARY KEY, name TEXT)`)

	stdout, _, code := runMigrateCLI(t, dbPath, dir, "diff")
	if code == 0 {
		t.Fatalf("want a non-zero exit when drift is found, stdout: %s", stdout)
	}
	if !strings.Contains(stdout, "unexplained") || !strings.Contains(stdout, "widgets") {
		t.Fatalf("want widgets reported unexplained, got: %s", stdout)
	}
}

func TestMigrateCLI_DiffReportsAlteredTable(t *testing.T) {
	// MIGCLI-36
	dir := t.TempDir()
	writeMigrationFile(t, dir, "0001_create_posts.sql", upDownMigration(`CREATE TABLE posts (id INTEGER PRIMARY KEY);`, `DROP TABLE posts;`))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if _, stderr, code := runMigrateCLI(t, dbPath, dir, "up"); code != 0 {
		t.Fatalf("setup: migrate up: want 0, got %d: %s", code, stderr)
	}
	execSQL(t, dbPath, `ALTER TABLE posts ADD COLUMN body TEXT`)

	stdout, _, code := runMigrateCLI(t, dbPath, dir, "diff")
	if code == 0 {
		t.Fatalf("want a non-zero exit when drift is found, stdout: %s", stdout)
	}
	if !strings.Contains(stdout, "altered") || !strings.Contains(stdout, "posts") {
		t.Fatalf("want posts reported altered, got: %s", stdout)
	}
}

func TestMigrateCLI_DiffIsCleanWhenMatchingExactly(t *testing.T) {
	// MIGCLI-37
	dir := t.TempDir()
	writeMigrationFile(t, dir, "0001_create_posts.sql", upDownMigration(`CREATE TABLE posts (id INTEGER PRIMARY KEY);`, `DROP TABLE posts;`))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if _, stderr, code := runMigrateCLI(t, dbPath, dir, "up"); code != 0 {
		t.Fatalf("setup: migrate up: want 0, got %d: %s", code, stderr)
	}

	stdout, stderr, code := runMigrateCLI(t, dbPath, dir, "diff")
	if code != 0 {
		t.Fatalf("migrate diff: want exit 0, got %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "no drift") {
		t.Fatalf("want a clean report, got: %s", stdout)
	}
}

func TestMigrateCLI_DiffIgnoresAPendingMigration(t *testing.T) {
	// MIGCLI-38
	dir := t.TempDir()
	writeMigrationFile(t, dir, "0001_create_posts.sql", upDownMigration(`CREATE TABLE posts (id INTEGER PRIMARY KEY);`, `DROP TABLE posts;`))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if _, stderr, code := runMigrateCLI(t, dbPath, dir, "up"); code != 0 {
		t.Fatalf("setup: migrate up: want 0, got %d: %s", code, stderr)
	}
	// Added after up ran — stays pending.
	writeMigrationFile(t, dir, "0002_create_comments.sql", upDownMigration(`CREATE TABLE comments (id INTEGER PRIMARY KEY);`, `DROP TABLE comments;`))

	stdout, stderr, code := runMigrateCLI(t, dbPath, dir, "diff")
	if code != 0 {
		t.Fatalf("migrate diff: want exit 0 (pending migration isn't drift), got %d: %s", code, stdout+stderr)
	}
	if strings.Contains(stdout, "comments") {
		t.Fatalf("want no mention of the pending migration's table, got: %s", stdout)
	}
}

func TestMigrateCLI_DiffReportsMissingTable(t *testing.T) {
	// MIGCLI-39
	dir := t.TempDir()
	writeMigrationFile(t, dir, "0001_create_posts.sql", upDownMigration(`CREATE TABLE posts (id INTEGER PRIMARY KEY);`, `DROP TABLE posts;`))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if _, stderr, code := runMigrateCLI(t, dbPath, dir, "up"); code != 0 {
		t.Fatalf("setup: migrate up: want 0, got %d: %s", code, stderr)
	}
	execSQL(t, dbPath, `DROP TABLE posts`)

	stdout, _, code := runMigrateCLI(t, dbPath, dir, "diff")
	if code == 0 {
		t.Fatalf("want a non-zero exit when drift is found, stdout: %s", stdout)
	}
	if !strings.Contains(stdout, "missing") || !strings.Contains(stdout, "posts") {
		t.Fatalf("want posts reported missing, got: %s", stdout)
	}
}

func TestMigrateCLI_DiffExcludesEmbeddedTables(t *testing.T) {
	// MIGCLI-40
	dir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	// Never touched by db.Migrate at all — this db has been opened only
	// via the migrate CLI, which never applies the embedded schema,
	// simulating a database that's never been through a server boot.

	stdout, stderr, code := runMigrateCLI(t, dbPath, dir, "diff")
	if code != 0 {
		t.Fatalf("migrate diff: want exit 0 (no app migrations to compare), got %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "no drift") {
		t.Fatalf("want a clean report even with zero embedded tables present, got: %s", stdout)
	}
	for _, embeddedTable := range []string{"users", "sessions", "oauth_clients", "owner_users"} {
		if strings.Contains(stdout, embeddedTable) {
			t.Fatalf("want no mention of embedded table %q, got: %s", embeddedTable, stdout)
		}
	}
}

func TestMigrateCLI_DiffNeverModifiesTheDatabase(t *testing.T) {
	// MIGCLI-41
	dir := t.TempDir()
	writeMigrationFile(t, dir, "0001_create_posts.sql", upDownMigration(`CREATE TABLE posts (id INTEGER PRIMARY KEY);`, `DROP TABLE posts;`))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if _, stderr, code := runMigrateCLI(t, dbPath, dir, "up"); code != 0 {
		t.Fatalf("setup: migrate up: want 0, got %d: %s", code, stderr)
	}
	execSQL(t, dbPath, `CREATE TABLE widgets (id INTEGER PRIMARY KEY)`)
	execSQL(t, dbPath, `ALTER TABLE posts ADD COLUMN body TEXT`)

	before := schemaSnapshot(t, dbPath)
	stdout, stderr, code := runMigrateCLI(t, dbPath, dir, "diff")
	if code == 0 {
		t.Fatalf("setup sanity check: want drift found (non-zero exit), stderr: %s", stderr)
	}
	// Specifically drift being reported, not e.g. an unrelated failure
	// (unknown subcommand, can't open db) that would also happen to
	// satisfy a bare "exit code is non-zero" check.
	if !strings.Contains(stdout, "unexplained") && !strings.Contains(stdout, "altered") {
		t.Fatalf("setup sanity check: want the drift actually reported, got stdout: %s stderr: %s", stdout, stderr)
	}
	after := schemaSnapshot(t, dbPath)

	if before != after {
		t.Fatalf("want migrate diff to never modify the database.\nbefore:\n%s\nafter:\n%s", before, after)
	}
}
