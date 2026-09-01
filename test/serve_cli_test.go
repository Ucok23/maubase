package e2e_test

import (
	"bytes"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Scenarios: spec/cli.md (CLI-01..05)
//
// Same exec-the-binary approach as test/migrate_cli_test.go
// (buildMaubaseCLI, runCLI, runCLIInDir are shared from there) — the
// top-level dispatch these scenarios cover has no HTTP surface of its
// own to test against.

func TestServeCLI_BareInvocationPrintsUsageWithoutTouchingAnything(t *testing.T) {
	// CLI-01
	dir := t.TempDir()

	stdout, stderr, code := runCLIInDir(t, dir)
	if code != 0 {
		t.Fatalf("bare maubase: want exit 0, got %d: %s", code, stdout+stderr)
	}
	out := stdout + stderr
	for _, want := range []string{"Usage:", "maubase serve", "maubase init", "maubase migrate", "maubase version"} {
		if !strings.Contains(out, want) {
			t.Fatalf("want usage to mention %q, got: %s", want, out)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("want bare invocation to create nothing, got: %v", entries)
	}
}

func TestServeCLI_HelpSpellingsAllPrintUsage(t *testing.T) {
	// CLI-04
	for _, args := range [][]string{{"help"}, {"-h"}, {"--help"}} {
		stdout, stderr, code := runCLI(t, args...)
		if code != 0 {
			t.Fatalf("maubase %v: want exit 0, got %d: %s", args, code, stdout+stderr)
		}
		if !strings.Contains(stdout+stderr, "Usage:") {
			t.Fatalf("maubase %v: want usage printed, got: %s", args, stdout+stderr)
		}
	}
}

func TestServeCLI_UnrecognizedCommandFailsClearly(t *testing.T) {
	// CLI-03
	stdout, stderr, code := runCLI(t, "bogus")
	if code == 0 {
		t.Fatalf("want a non-zero exit for an unrecognized command, got 0, stdout: %s", stdout)
	}
	if !strings.Contains(stderr, "bogus") {
		t.Fatalf("want the error to name the unrecognized command, got: %s", stderr)
	}
	if !strings.Contains(stdout+stderr, "Usage:") {
		t.Fatalf("want usage printed alongside the error, got: %s", stdout+stderr)
	}
}

func TestServeCLI_VersionSpellingsAllReportTheSameVersion(t *testing.T) {
	// CLI-05
	var outputs []string
	for _, args := range [][]string{{"version"}, {"-v"}, {"--version"}} {
		stdout, stderr, code := runCLI(t, args...)
		if code != 0 {
			t.Fatalf("maubase %v: want exit 0, got %d: %s", args, code, stdout+stderr)
		}
		if !strings.Contains(stdout, "maubase") {
			t.Fatalf("maubase %v: want output to mention \"maubase\", got: %s", args, stdout)
		}
		outputs = append(outputs, stdout)
	}
	for _, out := range outputs[1:] {
		if out != outputs[0] {
			t.Fatalf("want all three version spellings to report the same thing, got %q and %q", outputs[0], out)
		}
	}

	// The test binary is a plain `go build` from this git checkout (see
	// buildMaubaseCLI), not a `go install .../maubase@vX.Y.Z` pinned to
	// an exact tag — so it won't report a clean release version. Modern
	// Go derives a VCS-based pseudo-version automatically in this case
	// (see spec/cli.md CLI-05); assert the Go version it names instead
	// of the exact version string, which depends on repo state (commits
	// since the last tag, working-tree dirtiness) that varies by when
	// and where this test runs.
	if !strings.Contains(outputs[0], "(go1.") {
		t.Fatalf("want the output to name the Go version it was built with, got: %s", outputs[0])
	}
}

func TestServeCLI_ServeStartsTheServer(t *testing.T) {
	// CLI-02
	bin := buildMaubaseCLI(t)
	dir := t.TempDir()
	const addr = ":18234"

	cmd := exec.Command(bin, "serve")
	cmd.Dir = dir // safety net against any relative-path default straying outside the temp dir
	cmd.Env = append(os.Environ(),
		"MAUBASE_ADDR="+addr,
		"MAUBASE_ISSUER=http://localhost"+addr,
		"MAUBASE_DB_PATH="+filepath.Join(dir, "serve.db"),
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start maubase serve: %v", err)
	}
	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}()

	baseURL := "http://localhost" + addr
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return // success
			}
			lastErr = nil
		} else {
			lastErr = err
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("maubase serve never answered /healthz with 200: %v\nstdout: %s\nstderr: %s", lastErr, stdout.String(), stderr.String())
}
