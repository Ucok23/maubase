package e2e_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Scenarios: spec/project-init.md (INIT-01..07)
//
// Same exec-the-binary approach as test/migrate_cli_test.go (buildMaubaseCLI,
// runCLI, runCLIInDir are shared from there) — "maubase init" is purely a
// filesystem operation with no HTTP surface to test against.

func TestInitCLI_ScaffoldsMigrationsDirAndEnvExample(t *testing.T) {
	// INIT-01
	projectDir := t.TempDir()

	stdout, stderr, code := runCLIInDir(t, projectDir, "init")
	if code != 0 {
		t.Fatalf("maubase init: want exit 0, got %d: %s", code, stderr)
	}

	migrationPath := filepath.Join(projectDir, "migrations", "0001_init.sql")
	content, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatalf("want %s to exist: %v", migrationPath, err)
	}
	if !strings.Contains(string(content), "+migrate Up") || !strings.Contains(string(content), "+migrate Down") {
		t.Fatalf("want the starter migration to use the Up/Down marker format, got: %s", content)
	}

	envPath := filepath.Join(projectDir, ".env.example")
	envContent, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("want %s to exist: %v", envPath, err)
	}
	for _, want := range []string{"MAUBASE_DB_PATH", "MAUBASE_ISSUER", "MAUBASE_MIGRATIONS_DIR", "MAUBASE_BOOTSTRAP_OWNER_EMAIL"} {
		if !strings.Contains(string(envContent), want) {
			t.Fatalf("want .env.example to mention %s, got: %s", want, envContent)
		}
	}

	// Reported relative to cwd, same as "migrate new" does.
	if !strings.Contains(stdout, filepath.Join("migrations", "0001_init.sql")) || !strings.Contains(stdout, ".env.example") {
		t.Fatalf("want both created paths reported, got: %s", stdout)
	}
}

func TestInitCLI_ScaffoldsAgentSkill(t *testing.T) {
	// INIT-07
	projectDir := t.TempDir()

	stdout, stderr, code := runCLIInDir(t, projectDir, "init")
	if code != 0 {
		t.Fatalf("maubase init: want exit 0, got %d: %s", code, stderr)
	}

	skillPath := filepath.Join(projectDir, ".claude", "skills", "maubase", "SKILL.md")
	content, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("want %s to exist: %v", skillPath, err)
	}
	got := string(content)

	if !strings.HasPrefix(got, "---\nname: maubase\n") {
		t.Fatalf("want the skill to open with name/description frontmatter, got: %s", got)
	}
	if !strings.Contains(got, "<!-- maubase:begin ") || !strings.Contains(got, "<!-- maubase:end -->") {
		t.Fatalf("want a maubase:begin/end managed block, got: %s", got)
	}
	// The managed block's version marker must match the actual running
	// binary's own "maubase version" output, not a hardcoded string.
	// Extracted via regexp (not a plain field split) since the version
	// itself can legitimately contain spaces, e.g. "(devel), commit
	// abc123, modified".
	versionStdout, _, versionCode := runCLI(t, "version")
	if versionCode != 0 {
		t.Fatalf("maubase version: want exit 0, got %d", versionCode)
	}
	m := regexp.MustCompile(`^maubase (.+) \(go\S+\)$`).FindStringSubmatch(strings.TrimSpace(versionStdout))
	if m == nil {
		t.Fatalf("unexpected `maubase version` output: %q", versionStdout)
	}
	moduleVersion := m[1]
	if !strings.Contains(got, "<!-- maubase:begin "+moduleVersion+" -->") {
		t.Fatalf("want the managed block stamped with %q, got: %s", moduleVersion, got)
	}
	for _, want := range []string{"maubase migrate", "records:read", "records:write", "/api/data/", "/admin/ui/sql", "migrate diff"} {
		if !strings.Contains(got, want) {
			t.Fatalf("want the skill to mention %q, got: %s", want, got)
		}
	}

	if !strings.Contains(stdout, filepath.Join(".claude", "skills", "maubase", "SKILL.md")) {
		t.Fatalf("want the created skill path reported, got: %s", stdout)
	}
}

func TestInitCLI_RefusesWhenOnlyTheSkillFileAlreadyExists(t *testing.T) {
	// INIT-05, isolated to just the skill file conflicting
	projectDir := t.TempDir()
	skillPath := filepath.Join(projectDir, ".claude", "skills", "maubase", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0o755); err != nil {
		t.Fatalf("setup: mkdir: %v", err)
	}
	if err := os.WriteFile(skillPath, []byte("pre-existing\n"), 0o644); err != nil {
		t.Fatalf("setup: write skill file: %v", err)
	}

	stdout, stderr, code := runCLIInDir(t, projectDir, "init")
	if code == 0 {
		t.Fatalf("want a non-zero exit when the skill file alone already exists, got 0, stdout: %s", stdout)
	}
	if !strings.Contains(stderr, "SKILL.md") {
		t.Fatalf("want the error to name the conflicting skill path, got: %s", stderr)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "migrations")); !os.IsNotExist(err) {
		t.Fatalf("want nothing else created when refusing, stat err: %v", err)
	}
}

func TestInitCLI_CreatesGitignoreForDataDir(t *testing.T) {
	// INIT-02
	projectDir := t.TempDir()

	if _, stderr, code := runCLIInDir(t, projectDir, "init"); code != 0 {
		t.Fatalf("maubase init: want exit 0, got %d: %s", code, stderr)
	}

	content, err := os.ReadFile(filepath.Join(projectDir, ".gitignore"))
	if err != nil {
		t.Fatalf("want .gitignore to exist: %v", err)
	}
	if !strings.Contains(string(content), "data/") {
		t.Fatalf("want .gitignore to cover data/, got: %s", content)
	}
}

func TestInitCLI_AppendsToExistingGitignoreWithoutClobberingIt(t *testing.T) {
	// INIT-03
	projectDir := t.TempDir()
	gitignorePath := filepath.Join(projectDir, ".gitignore")
	if err := os.WriteFile(gitignorePath, []byte("node_modules/\n*.log\n"), 0o644); err != nil {
		t.Fatalf("setup: write .gitignore: %v", err)
	}

	if _, stderr, code := runCLIInDir(t, projectDir, "init"); code != 0 {
		t.Fatalf("maubase init: want exit 0, got %d: %s", code, stderr)
	}

	content, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	got := string(content)
	if !strings.Contains(got, "node_modules/") || !strings.Contains(got, "*.log") {
		t.Fatalf("want the pre-existing entries preserved, got: %s", got)
	}
	if !strings.Contains(got, "data/") {
		t.Fatalf("want data/ appended, got: %s", got)
	}
}

func TestInitCLI_DoesNotDuplicateAnAlreadyCoveredGitignoreEntry(t *testing.T) {
	// INIT-04
	projectDir := t.TempDir()
	gitignorePath := filepath.Join(projectDir, ".gitignore")
	original := "node_modules/\ndata/\n"
	if err := os.WriteFile(gitignorePath, []byte(original), 0o644); err != nil {
		t.Fatalf("setup: write .gitignore: %v", err)
	}

	if _, stderr, code := runCLIInDir(t, projectDir, "init"); code != 0 {
		t.Fatalf("maubase init: want exit 0, got %d: %s", code, stderr)
	}

	content, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if strings.Count(string(content), "data/") != 1 {
		t.Fatalf("want exactly one data/ entry (no duplicate), got: %s", content)
	}
}

func TestInitCLI_RefusesToOverwriteAnAlreadyInitializedProject(t *testing.T) {
	// INIT-05
	projectDir := t.TempDir()
	if _, stderr, code := runCLIInDir(t, projectDir, "init"); code != 0 {
		t.Fatalf("setup: maubase init: want exit 0, got %d: %s", code, stderr)
	}
	// Mark the migration file so we can tell if a second init overwrote it.
	migrationPath := filepath.Join(projectDir, "migrations", "0001_init.sql")
	if err := os.WriteFile(migrationPath, []byte("-- customized by the user\n"), 0o644); err != nil {
		t.Fatalf("setup: customize migration: %v", err)
	}

	stdout, stderr, code := runCLIInDir(t, projectDir, "init")
	if code == 0 {
		t.Fatalf("want a non-zero exit re-initializing an existing project, got 0, stdout: %s", stdout)
	}
	if !strings.Contains(stderr, "migrations") || !strings.Contains(stderr, ".env.example") {
		t.Fatalf("want the error to name both conflicting paths, got: %s", stderr)
	}

	content, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if string(content) != "-- customized by the user\n" {
		t.Fatalf("want the existing migration untouched, got: %s", content)
	}
}

func TestInitCLI_InitWithDirArgScaffoldsIntoThatDirectory(t *testing.T) {
	// INIT-06
	parent := t.TempDir()

	stdout, stderr, code := runCLI(t, "init", filepath.Join(parent, "myapp"))
	if code != 0 {
		t.Fatalf("maubase init <dir>: want exit 0, got %d: %s", code, stderr)
	}

	for _, rel := range []string{"myapp/migrations/0001_init.sql", "myapp/.env.example", "myapp/.claude/skills/maubase/SKILL.md", "myapp/.gitignore"} {
		if _, err := os.Stat(filepath.Join(parent, rel)); err != nil {
			t.Fatalf("want %s to exist: %v", rel, err)
		}
	}
	if !strings.Contains(stdout, "myapp") {
		t.Fatalf("want the created paths (under myapp/) reported, got: %s", stdout)
	}
	// Nothing should have leaked into parent itself.
	if _, err := os.Stat(filepath.Join(parent, "migrations")); !os.IsNotExist(err) {
		t.Fatalf("want no migrations/ directly under %s, stat err: %v", parent, err)
	}
}
