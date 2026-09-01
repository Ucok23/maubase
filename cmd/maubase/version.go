package main

import (
	"fmt"
	"runtime/debug"
)

// versionString reports what's actually running, using Go's own
// automatically-embedded build info rather than anything injected via
// -ldflags — that keeps this correct with zero extra build-system work
// across every supported way of getting this binary:
//
//   - `go install github.com/Ucok23/maubase/cmd/maubase@v1.1.0` (or
//     @latest): Main.Version is the real module version, e.g. "v1.1.0".
//   - `go build`/`make build` from a git checkout: modern Go (1.24+)
//     derives a real pseudo-version from VCS automatically too — e.g.
//     "v1.1.0-2-g5c4f8d5" isn't a real tag, so this prints something
//     like "v1.1.1-0.20260901085303-5c4f8d5cc456+dirty" ("+dirty" if
//     the tree has uncommitted changes) instead of a bare "(devel)".
//   - Built from a source tree with no VCS info at all (e.g. a Docker
//     build, whose context deliberately excludes .git — see
//     .dockerignore, or an older Go toolchain) — Main.Version is
//     literally "(devel)" with nothing to derive from; reported as-is.
func versionString() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		// Only happens for a binary not built by the Go toolchain at
		// all (e.g. via a non-Go build system) — not a real case for
		// this project, but fail informatively rather than panicking.
		return "maubase: unknown version (no build info embedded)"
	}
	return fmt.Sprintf("maubase %s (%s)", moduleVersion(info), info.GoVersion)
}

// moduleVersion reports just the version part of versionString() above,
// with no Go-toolchain suffix — used wherever a version needs to be
// embedded as data rather than printed for a human (e.g. the marker
// skill.go stamps into the scaffolded skill file, so a later re-init can
// tell whether its content is stale against the binary that generated
// it).
func moduleVersion(info *debug.BuildInfo) string {
	version := info.Main.Version
	if version == "" {
		version = "(devel)"
	}

	if version == "(devel)" {
		var commit string
		modified := false
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				commit = s.Value
			case "vcs.modified":
				modified = s.Value == "true"
			}
		}
		if commit != "" {
			if len(commit) > 12 {
				commit = commit[:12]
			}
			if modified {
				commit += ", modified"
			}
			version = fmt.Sprintf("(devel), commit %s", commit)
		}
	}

	return version
}

// currentModuleVersion is moduleVersion for callers (skill.go) that don't
// already have a *debug.BuildInfo in hand.
func currentModuleVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "(unknown)"
	}
	return moduleVersion(info)
}
