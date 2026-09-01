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

// Scenarios: spec/cli.md (CLI-01..04)
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
	for _, want := range []string{"Usage:", "maubase serve", "maubase init", "maubase migrate"} {
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
