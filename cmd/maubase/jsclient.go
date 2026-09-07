package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	sdkjs "github.com/Ucok23/maubase/sdk/js"
)

// jsClientRelDir is where `maubase init --js-client` vendors the built
// @maubase/client output — see spec/project-init.md INIT-10. A plain
// project-root directory, not nested under sdk/ or vendor/: it's meant
// to be imported straight from application code
// (e.g. `./maubase-client/index.js`), not treated as maubase's own
// source the way sdk/js/ is in this repo.
const jsClientRelDir = "maubase-client"

// hasJSClient reports whether dir already has a vendored client. Used
// instead of threading a --js-client flag through every call site that
// renders the skill/AGENTS.md content: runInit has that flag directly,
// but runUpdateAgentDocs (run later, standalone, after an upgrade) has
// no flag of its own to know this from — checking the filesystem is the
// one source of truth both agree on.
func hasJSClient(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, jsClientRelDir))
	return err == nil
}

// writeJSClient copies the embedded sdk/js/dist build output (see
// sdk/js/embed.go) into dir/jsClientRelDir byte for byte, plus a
// generated README.md explaining what it is and what it doesn't cover,
// and returns how many files it wrote (for runInit's "created ..."
// summary line). Callers must check hasJSClient(dir) first — like every
// other file runInit scaffolds, this refuses nothing itself and will
// happily write over an existing directory's contents.
func writeJSClient(dir string) (int, error) {
	root := filepath.Join(dir, jsClientRelDir)
	n := 0
	err := fs.WalkDir(sdkjs.DistFS, "dist", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel("dist", path)
		if err != nil {
			return err
		}
		target := filepath.Join(root, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := fs.ReadFile(sdkjs.DistFS, path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return err
		}
		n++
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("vendor js client: %w", err)
	}

	readmePath := filepath.Join(root, "README.md")
	if err := os.WriteFile(readmePath, []byte(renderJSClientReadme()), 0o644); err != nil {
		return 0, fmt.Errorf("write %s: %w", readmePath, err)
	}
	n++

	return n, nil
}

// renderJSClientReadme fills jsClientReadmeTemplate the same way
// renderSkill fills skillTemplate — see that function's doc comment.
func renderJSClientReadme() string {
	out := strings.ReplaceAll(jsClientReadmeTemplate, "{{VERSION}}", currentModuleVersion())
	out = strings.ReplaceAll(out, "{{DOCREF}}", currentDocRef())
	return out
}

// jsClientReadmeTemplate documents the one thing that's easy to get
// wrong about this vendored copy: it's a snapshot, not a live-updating
// dependency, and it doesn't cover the whole API surface — carrying
// forward sdk/js/README.md's own "What's not here yet" caveat so an
// agent (or person) reading only maubase-client/ still sees it, not
// just the source README this directory was copied out of.
var jsClientReadmeTemplate = `# @maubase/client (vendored)

This is maubase's own TypeScript client (auth + auto-REST), vendored
here by ` + "`maubase init --js-client`" + ` from **{{VERSION}}**'s
` + "`sdk/js/dist`" + ` — not a separate package, not npm-installed, just
copied in so it's exactly what this maubase binary shipped. Import it
with a relative path:

` + "```ts" + `
import { createClient } from "./maubase-client/index.js";
` + "```" + `

## What this does not cover yet

Only auth (` + "`client.auth`" + `) and auto-REST (` + "`client.data`" + `) —
carried over directly from the source SDK's own limits, not something
specific to vendoring it here:

- **Storage** — no wrapper; call ` + "`/api/storage/*`" + ` directly, see
  ` + specURL("{{DOCREF}}", "storage.md") + `
- **Realtime** — no wrapper; use the WebSocket broker directly, see
  ` + specURL("{{DOCREF}}", "realtime.md") + `

## Getting a newer client

This directory is a snapshot, not something ` + "`maubase init`" + ` updates in
place. To pick up a client matching a newer maubase binary: delete
` + "`maubase-client/`" + ` and re-run ` + "`maubase init --js-client`" + `.
`
