package e2e_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Scenarios: spec/project-init.md (INIT-01..10)
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

// currentMaubaseVersion runs the actual CLI under test's own "maubase
// version" and extracts just the version part (not the "(goX.Y)"
// suffix) — used to assert a scaffolded managed block is stamped with
// whatever this binary really reports, not a hardcoded string. Extracted
// via regexp (not a plain field split) since the version itself can
// legitimately contain spaces, e.g. "(devel), commit abc123, modified".
func currentMaubaseVersion(t *testing.T) string {
	t.Helper()
	versionStdout, _, versionCode := runCLI(t, "version")
	if versionCode != 0 {
		t.Fatalf("maubase version: want exit 0, got %d", versionCode)
	}
	m := regexp.MustCompile(`^maubase (.+) \(go\S+\)$`).FindStringSubmatch(strings.TrimSpace(versionStdout))
	if m == nil {
		t.Fatalf("unexpected `maubase version` output: %q", versionStdout)
	}
	return m[1]
}

// assertAgentDocBody runs every check common to SKILL.md and AGENTS.md's
// shared body content (INIT-07/INIT-08): the managed block, its version
// stamp, the fixed-surface links, the no-restated-prose rule, and every
// spec link pinned to one single non-"main" ref.
func assertAgentDocBody(t *testing.T, path, got, moduleVersion string) {
	t.Helper()

	if !strings.Contains(got, "<!-- maubase:begin ") || !strings.Contains(got, "<!-- maubase:end -->") {
		t.Fatalf("want a maubase:begin/end managed block in %s, got: %s", path, got)
	}
	if !strings.Contains(got, "<!-- maubase:begin "+moduleVersion+" -->") {
		t.Fatalf("want %s's managed block stamped with %q, got: %s", path, moduleVersion, got)
	}
	for _, want := range []string{"maubase migrate", "GET /api/schema", "records:read", "MAUBASE_ENV=development", "migrate diff", "/api/data/{table}"} {
		if !strings.Contains(got, want) {
			t.Fatalf("want %s to mention %q, got: %s", path, want, got)
		}
	}
	// This is a pointer file, not a manual: it must not restate the
	// fixed API surface as its own prose (that's a second copy of
	// spec/*.md to keep in sync by hand) — every topic should be a
	// link, not paragraphs describing routes/scopes itself.
	for _, mustNotDuplicate := range []string{"records:write` (POST", "owner_id` can never be set"} {
		if strings.Contains(got, mustNotDuplicate) {
			t.Fatalf("want %s to link to spec/*.md instead of restating its content, but found %q inlined: %s", path, mustNotDuplicate, got)
		}
	}

	// Every topic links to its spec so an agent fetches the actual,
	// version-matched behavior instead of guessing or trusting whatever
	// it might otherwise recall about maubase's source.
	for _, specFile := range []string{
		"identity.md", "auto-rest.md", "access-rules.md", "storage.md",
		"realtime.md", "oauth-token.md", "admin-ui.md", "migrations-cli.md",
		"schema-introspection.md", "README.md",
	} {
		if !strings.Contains(got, "raw.githubusercontent.com/Ucok23/maubase/") || !strings.Contains(got, "/spec/"+specFile) {
			t.Fatalf("want a raw.githubusercontent.com link to spec/%s in %s, got: %s", specFile, path, got)
		}
	}
	// Every such link must be pinned to one single ref (a real tag or a
	// full commit hash) — never "main", which is exactly the kind of
	// drift-prone reference this whole mechanism exists to avoid.
	refs := regexp.MustCompile(`raw\.githubusercontent\.com/Ucok23/maubase/([^/]+)/spec/`).FindAllStringSubmatch(got, -1)
	if len(refs) == 0 {
		t.Fatalf("want at least one spec link in %s, got: %s", path, got)
	}
	firstRef := refs[0][1]
	if firstRef == "main" {
		t.Fatalf("want spec links in %s pinned to a real ref, not the moving \"main\" branch: %s", path, got)
	}
	for _, m := range refs[1:] {
		if m[1] != firstRef {
			t.Fatalf("want every spec link in %s pinned to the same ref (%q), also saw %q", path, firstRef, m[1])
		}
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
	assertAgentDocBody(t, skillPath, got, currentMaubaseVersion(t))

	// A plain "maubase init" (no --js-client) must not mention the
	// vendored client at all — pointing at a directory that doesn't
	// exist would be actively misleading (INIT-10).
	if strings.Contains(got, "maubase-client") {
		t.Fatalf("want no maubase-client/ mention without --js-client, got: %s", got)
	}

	if !strings.Contains(stdout, filepath.Join(".claude", "skills", "maubase", "SKILL.md")) {
		t.Fatalf("want the created skill path reported, got: %s", stdout)
	}
}

func TestInitCLI_ScaffoldsAgentsMdFallback(t *testing.T) {
	// INIT-08
	projectDir := t.TempDir()

	stdout, stderr, code := runCLIInDir(t, projectDir, "init")
	if code != 0 {
		t.Fatalf("maubase init: want exit 0, got %d: %s", code, stderr)
	}

	agentsPath := filepath.Join(projectDir, ".maubase", "AGENTS.md")
	content, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("want %s to exist: %v", agentsPath, err)
	}
	got := string(content)

	// Tool-agnostic: no Claude Code skill frontmatter here.
	if strings.HasPrefix(got, "---\n") {
		t.Fatalf("want AGENTS.md to have no YAML frontmatter, got: %s", got)
	}
	if !strings.HasPrefix(got, "<!-- maubase:begin ") {
		t.Fatalf("want AGENTS.md to open directly with the managed block, got: %s", got)
	}
	assertAgentDocBody(t, agentsPath, got, currentMaubaseVersion(t))

	// Same body as SKILL.md, minus the frontmatter.
	skillPath := filepath.Join(projectDir, ".claude", "skills", "maubase", "SKILL.md")
	skillContent, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read skill for comparison: %v", err)
	}
	if !strings.HasSuffix(strings.TrimRight(string(skillContent), "\n"), strings.TrimRight(got, "\n")) {
		t.Fatalf("want AGENTS.md content to match SKILL.md's body verbatim (minus frontmatter)\nAGENTS.md: %s\nSKILL.md: %s", got, skillContent)
	}

	if !strings.Contains(stdout, filepath.Join(".maubase", "AGENTS.md")) {
		t.Fatalf("want the created AGENTS.md path reported, got: %s", stdout)
	}
}

func TestInitCLI_RefusesWhenOnlyTheAgentsMdFileAlreadyExists(t *testing.T) {
	// INIT-05/INIT-08, isolated to just AGENTS.md conflicting
	projectDir := t.TempDir()
	agentsPath := filepath.Join(projectDir, ".maubase", "AGENTS.md")
	if err := os.MkdirAll(filepath.Dir(agentsPath), 0o755); err != nil {
		t.Fatalf("setup: mkdir: %v", err)
	}
	if err := os.WriteFile(agentsPath, []byte("pre-existing\n"), 0o644); err != nil {
		t.Fatalf("setup: write AGENTS.md: %v", err)
	}

	stdout, stderr, code := runCLIInDir(t, projectDir, "init")
	if code == 0 {
		t.Fatalf("want a non-zero exit when AGENTS.md alone already exists, got 0, stdout: %s", stdout)
	}
	if !strings.Contains(stderr, "AGENTS.md") {
		t.Fatalf("want the error to name the conflicting AGENTS.md path, got: %s", stderr)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "migrations")); !os.IsNotExist(err) {
		t.Fatalf("want nothing else created when refusing, stat err: %v", err)
	}
}

func TestInitCLI_UpdateAgentDocsCreatesFilesWhenMissing(t *testing.T) {
	// INIT-09, the "project predates this flag" case
	projectDir := t.TempDir()

	stdout, stderr, code := runCLIInDir(t, projectDir, "init", "--update-agent-docs")
	if code != 0 {
		t.Fatalf("maubase init --update-agent-docs: want exit 0, got %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "created") {
		t.Fatalf("want the fresh-create case reported as \"created\", got: %s", stdout)
	}

	skillPath := filepath.Join(projectDir, ".claude", "skills", "maubase", "SKILL.md")
	if _, err := os.Stat(skillPath); err != nil {
		t.Fatalf("want %s to exist: %v", skillPath, err)
	}
	agentsPath := filepath.Join(projectDir, ".maubase", "AGENTS.md")
	if _, err := os.Stat(agentsPath); err != nil {
		t.Fatalf("want %s to exist: %v", agentsPath, err)
	}
	// Nothing else from a plain "init" should have been touched.
	if _, err := os.Stat(filepath.Join(projectDir, "migrations")); !os.IsNotExist(err) {
		t.Fatalf("want no migrations/ from --update-agent-docs alone, stat err: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".env.example")); !os.IsNotExist(err) {
		t.Fatalf("want no .env.example from --update-agent-docs alone, stat err: %v", err)
	}
}

func TestInitCLI_UpdateAgentDocsReplacesManagedBlockButPreservesAppendedContent(t *testing.T) {
	// INIT-09
	projectDir := t.TempDir()
	if _, stderr, code := runCLIInDir(t, projectDir, "init"); code != 0 {
		t.Fatalf("setup: maubase init: want exit 0, got %d: %s", code, stderr)
	}

	skillPath := filepath.Join(projectDir, ".claude", "skills", "maubase", "SKILL.md")
	agentsPath := filepath.Join(projectDir, ".maubase", "AGENTS.md")

	// Simulate staleness (an old version marker) and a person's own
	// note appended below the managed block, in both files.
	for _, path := range []string{skillPath, agentsPath} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("setup: read %s: %v", path, err)
		}
		stale := regexp.MustCompile(`<!-- maubase:begin .*? -->`).ReplaceAllString(string(content), "<!-- maubase:begin v0.0.1-totally-stale -->")
		stale += "\nMy own hand-added note — do not touch.\n"
		if err := os.WriteFile(path, []byte(stale), 0o644); err != nil {
			t.Fatalf("setup: write %s: %v", path, err)
		}
	}

	stdout, stderr, code := runCLIInDir(t, projectDir, "init", "--update-agent-docs")
	if code != 0 {
		t.Fatalf("maubase init --update-agent-docs: want exit 0, got %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "updated") {
		t.Fatalf("want the refresh reported as \"updated\", got: %s", stdout)
	}

	wantVersion := currentMaubaseVersion(t)
	for _, path := range []string{skillPath, agentsPath} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		got := string(content)
		if strings.Contains(got, "v0.0.1-totally-stale") {
			t.Fatalf("want the stale version marker replaced in %s, got: %s", path, got)
		}
		if !strings.Contains(got, "<!-- maubase:begin "+wantVersion+" -->") {
			t.Fatalf("want %s re-stamped with the current version %q, got: %s", path, wantVersion, got)
		}
		if !strings.Contains(got, "My own hand-added note — do not touch.") {
			t.Fatalf("want the hand-added note below the managed block preserved in %s, got: %s", path, got)
		}
	}

	// Running it again with nothing stale should be a no-op.
	stdout2, stderr2, code2 := runCLIInDir(t, projectDir, "init", "--update-agent-docs")
	if code2 != 0 {
		t.Fatalf("second maubase init --update-agent-docs: want exit 0, got %d: %s", code2, stderr2)
	}
	if !strings.Contains(stdout2, "unchanged") {
		t.Fatalf("want a second run with nothing stale reported as \"unchanged\", got: %s", stdout2)
	}
}

func TestInitCLI_UpdateAgentDocsRefusesFileWithNoManagedBlock(t *testing.T) {
	// INIT-09
	projectDir := t.TempDir()
	if _, stderr, code := runCLIInDir(t, projectDir, "init"); code != 0 {
		t.Fatalf("setup: maubase init: want exit 0, got %d: %s", code, stderr)
	}

	skillPath := filepath.Join(projectDir, ".claude", "skills", "maubase", "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("a person rewrote this file entirely by hand\n"), 0o644); err != nil {
		t.Fatalf("setup: rewrite skill: %v", err)
	}

	stdout, stderr, code := runCLIInDir(t, projectDir, "init", "--update-agent-docs")
	if code == 0 {
		t.Fatalf("want a non-zero exit when a file has no managed block, got 0, stdout: %s", stdout)
	}
	if !strings.Contains(stderr, "SKILL.md") {
		t.Fatalf("want the error to name the offending file, got: %s", stderr)
	}

	content, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read skill: %v", err)
	}
	if string(content) != "a person rewrote this file entirely by hand\n" {
		t.Fatalf("want the hand-rewritten file left untouched, got: %s", content)
	}
}

func TestInitCLI_RejectsCombiningJSClientAndUpdateAgentDocs(t *testing.T) {
	// INIT-09/INIT-10
	projectDir := t.TempDir()

	stdout, stderr, code := runCLIInDir(t, projectDir, "init", "--update-agent-docs", "--js-client")
	if code == 0 {
		t.Fatalf("want a non-zero exit combining --update-agent-docs and --js-client, got 0, stdout: %s", stdout)
	}
	if !strings.Contains(stderr, "--js-client") || !strings.Contains(stderr, "--update-agent-docs") {
		t.Fatalf("want the error to name both flags, got: %s", stderr)
	}
}

func TestInitCLI_JSClientVendorsTheBuiltClientAndMentionsItInTheDocs(t *testing.T) {
	// INIT-10
	projectDir := t.TempDir()

	stdout, stderr, code := runCLIInDir(t, projectDir, "init", "--js-client")
	if code != 0 {
		t.Fatalf("maubase init --js-client: want exit 0, got %d: %s", code, stderr)
	}

	clientDir := filepath.Join(projectDir, "maubase-client")
	indexPath := filepath.Join(clientDir, "index.js")
	if _, err := os.Stat(indexPath); err != nil {
		t.Fatalf("want %s to exist: %v", indexPath, err)
	}
	dtsPath := filepath.Join(clientDir, "index.d.ts")
	if _, err := os.Stat(dtsPath); err != nil {
		t.Fatalf("want %s to exist: %v", dtsPath, err)
	}

	readmeContent, err := os.ReadFile(filepath.Join(clientDir, "README.md"))
	if err != nil {
		t.Fatalf("want maubase-client/README.md to exist: %v", err)
	}
	readme := string(readmeContent)
	wantVersion := currentMaubaseVersion(t)
	if !strings.Contains(readme, wantVersion) {
		t.Fatalf("want maubase-client/README.md stamped with %q, got: %s", wantVersion, readme)
	}
	for _, want := range []string{"Storage", "Realtime", "spec/storage.md", "spec/realtime.md"} {
		if !strings.Contains(readme, want) {
			t.Fatalf("want maubase-client/README.md to mention %q (the coverage gap), got: %s", want, readme)
		}
	}

	if !strings.Contains(stdout, "maubase-client") {
		t.Fatalf("want the vendored client path reported, got: %s", stdout)
	}

	// The skill/AGENTS.md generated in the same run must reference it.
	for _, path := range []string{
		filepath.Join(projectDir, ".claude", "skills", "maubase", "SKILL.md"),
		filepath.Join(projectDir, ".maubase", "AGENTS.md"),
	} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !strings.Contains(string(content), "maubase-client/") {
			t.Fatalf("want %s to mention the vendored client, got: %s", path, content)
		}
	}
}

func TestInitCLI_JSClientConflictsWhenDirAlreadyExists(t *testing.T) {
	// INIT-05/INIT-10
	projectDir := t.TempDir()
	clientDir := filepath.Join(projectDir, "maubase-client")
	if err := os.MkdirAll(clientDir, 0o755); err != nil {
		t.Fatalf("setup: mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(clientDir, "keep-me.js"), []byte("// pre-existing\n"), 0o644); err != nil {
		t.Fatalf("setup: write file: %v", err)
	}

	stdout, stderr, code := runCLIInDir(t, projectDir, "init", "--js-client")
	if code == 0 {
		t.Fatalf("want a non-zero exit when maubase-client/ already exists, got 0, stdout: %s", stdout)
	}
	if !strings.Contains(stderr, "maubase-client") {
		t.Fatalf("want the error to name the conflicting maubase-client path, got: %s", stderr)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "migrations")); !os.IsNotExist(err) {
		t.Fatalf("want nothing else created when refusing, stat err: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(clientDir, "keep-me.js"))
	if err != nil {
		t.Fatalf("read pre-existing file: %v", err)
	}
	if string(content) != "// pre-existing\n" {
		t.Fatalf("want the pre-existing maubase-client/ contents untouched, got: %s", content)
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

	for _, rel := range []string{"myapp/migrations/0001_init.sql", "myapp/.env.example", "myapp/.claude/skills/maubase/SKILL.md", "myapp/.maubase/AGENTS.md", "myapp/.gitignore"} {
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
