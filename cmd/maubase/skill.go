package main

import "strings"

// skillRelPath is where maubase init scaffolds the agent-context skill,
// relative to the project directory — see spec/project-init.md INIT-07.
// Nested under .claude/skills/ (Claude Code's own project-skill
// discovery path) rather than a root-level AGENTS.md/CLAUDE.md: those
// belong to whatever app gets built in this project, and maubase writing
// into a file it doesn't own risks clobbering it in an existing repo. A
// namespaced skill never competes for that.
const skillRelPath = ".claude/skills/maubase/SKILL.md"

// agentsRelPath is where maubase init scaffolds the tool-agnostic
// fallback of the same content, for an agent that isn't Claude Code and
// so won't discover skillRelPath at all — see spec/project-init.md
// INIT-08. Nested under .maubase/, not the repo root: a root AGENTS.md
// is exactly the file this project's own app is likely to have (or want)
// for itself, the same ownership-collision reason skillRelPath isn't a
// root CLAUDE.md either. .maubase/ isn't a directory anything else
// plausibly owns.
const agentsRelPath = ".maubase/AGENTS.md"

// maubaseRepo is where this project's own spec/*.md lives — the
// authoritative, version-matched reference the skill points an agent
// to per topic. Deliberately a pointer, not inlined content: this
// project's tables/columns/access rules are discovered at runtime (a
// migration, the admin UI's create-table form, or SQL Studio can all
// add one after this file was generated) and a static file can't track
// that — see GET /api/schema below, which can. And restating spec/*.md
// as fresh prose here would just be documentation to keep in sync by
// hand forever, the exact failure mode this project's spec-first tests
// exist to avoid everywhere else.
const maubaseRepo = "Ucok23/maubase"

// renderSkill fills skillTemplate (frontmatter + agentDocBody) with the
// maubase version generating it, raw.githubusercontent.com links pinned
// to the exact commit or release tag that binary was built from
// (currentDocRef), and — only when jsClient is true — the vendored-
// client section (see spec/project-init.md INIT-10). jsClient should
// reflect whether jsClientRelDir actually exists in the target
// directory, not just whether --js-client was passed on this
// particular invocation: runUpdateAgentDocs has no such flag of its own
// and relies on hasJSClient(dir) instead, so the two code paths agree
// on what "this project has a vendored client" means.
func renderSkill(jsClient bool) string {
	return renderAgentDoc(skillTemplate, jsClient)
}

// renderAgentsDoc is renderSkill for agentsTemplate — the same
// agentDocBody, minus the Claude-Code-specific frontmatter. See
// agentsRelPath.
func renderAgentsDoc(jsClient bool) string {
	return renderAgentDoc(agentsTemplate, jsClient)
}

// renderAgentDoc is the shared substitution pass behind renderSkill and
// renderAgentsDoc: both templates carry the same three placeholders,
// just wrapped differently (skillTemplate adds YAML frontmatter around
// agentDocBody; agentsTemplate is agentDocBody verbatim).
func renderAgentDoc(tmpl string, jsClient bool) string {
	section := ""
	if jsClient {
		section = jsClientSection
	}
	out := strings.Replace(tmpl, "{{JSCLIENT}}", section, 1)
	out = strings.ReplaceAll(out, "{{VERSION}}", currentModuleVersion())
	out = strings.ReplaceAll(out, "{{DOCREF}}", currentDocRef())
	return out
}

// specURL builds a stable, directly-fetchable link to one spec file at
// ref — raw.githubusercontent.com rather than a github.com/.../blob/
// page, since the audience is an agent's fetch tool, not a browser: raw
// returns the plain markdown with no HTML chrome around it.
func specURL(ref, file string) string {
	return "https://raw.githubusercontent.com/" + maubaseRepo + "/" + ref + "/spec/" + file
}

// skillTemplate is the Claude Code skill: YAML frontmatter (so Claude
// Code's skill discovery can find and describe it) wrapped around the
// same agentDocBody every other agent-context file shares.
//
// A var, not a const: agentDocBody is itself a var (see its own doc
// comment for why), and Go's constant-expression rules don't allow a
// non-constant operand in a const string concatenation.
var skillTemplate = `---
name: maubase
description: Use when working on a project backed by maubase (a self-hosted auth/database/storage/realtime backend) — adding endpoints, changing schema, or touching auth/migrations/access rules in this project.
---

` + agentDocBody

// agentsTemplate is the tool-agnostic fallback (agentsRelPath) for an
// agent that isn't Claude Code and so never discovers skillTemplate at
// all — agentDocBody verbatim, with no frontmatter: nothing here reads
// YAML frontmatter the way Claude Code's skill discovery does, and the
// body's own opening "# maubase" heading already says what the file is.
var agentsTemplate = agentDocBody

// agentDocBody is wrapped in a <!-- maubase:begin/end --> managed block
// so `maubase init --update-agent-docs` (see runUpdateAgentDocs) can
// replace just this content later — after an upgrade, say — without
// touching the frontmatter above it (skillTemplate only) or anything a
// person appends below the closing marker in either file.
//
// A var, not a const: it's built with specURL() calls baked in at
// program-init time (each one already carrying the literal "{{DOCREF}}"
// placeholder as its ref argument, replaced for real by renderAgentDoc),
// which Go's constant-expression rules don't allow in a const.
//
// Deliberately short. This is a pointer file, not a manual: every link
// below is either a live command (reflects this project's actual,
// current, possibly-just-changed state) or a spec file pinned to the
// exact commit that generated this — never prose restating either one,
// which would just be a second copy to keep in sync by hand.
var agentDocBody = `<!-- maubase:begin {{VERSION}} -->
# maubase

This project's backend is maubase — self-hosted auth, a database with
an automatic REST API, file storage, realtime, and OAuth 2.1, on one
SQLite file. One ` + "`maubase serve`" + ` process serves exactly this project.

Generated by ` + "`maubase init`" + ` for **{{VERSION}}**. Below is either a live
command (this project's actual current state) or a spec link pinned to
the exact commit that generated this file — not prose written from
memory. Prefer both of those over training data or anything you might
otherwise recall about maubase's own source: neither is this project's
actual version, and both can simply be wrong for it.

## What actually exists in this project right now

Tables, columns, and access rules are created by a migration, the admin
UI's create-table form, or SQL Studio — all *after* this file was
generated, so nothing below describes them; ask the running server
instead:

- ` + "`GET /api/schema`" + ` — every collection ` + "`/api/data/*`" + ` exposes right
  now (columns, types, owner scoping, resolved read/write rules).
  Requires a ` + "`records:read`" + ` access token, and only exists at all when
  this project's ` + "`.env`" + ` sets ` + "`MAUBASE_ENV=development`" + ` — 404s
  otherwise. (` + specURL("{{DOCREF}}", "schema-introspection.md") + `)
- Not in development mode, or need history rather than current state:
  ` + "`maubase migrate status`" + ` plus ` + "`migrations/*.sql`" + `. Keep in mind the
  admin UI/SQL Studio can change the live schema without a matching
  migration file — ` + "`maubase migrate diff`" + ` catches that drift; run it
  before trusting migrations/ as a complete picture.
{{JSCLIENT}}
## What this backend's fixed API surface is — spec, not summary

- Auth (signup/login/session): ` + specURL("{{DOCREF}}", "identity.md") + `
- Auto-REST (` + "`/api/data/{table}`" + `): ` + specURL("{{DOCREF}}", "auto-rest.md") + `
- Access rules beyond owner_id (` + "`_policies`" + `): ` + specURL("{{DOCREF}}", "access-rules.md") + `
- Storage: ` + specURL("{{DOCREF}}", "storage.md") + `
- Realtime: ` + specURL("{{DOCREF}}", "realtime.md") + `
- OAuth 2.1 (where access tokens come from): ` + specURL("{{DOCREF}}", "oauth-token.md") + `
- Admin UI (` + "`/admin/ui/*`" + `, operating this deployment): ` + specURL("{{DOCREF}}", "admin-ui.md") + `
- Full index: ` + specURL("{{DOCREF}}", "README.md") + `

## Commands

` + "`maubase serve`" + `, ` + "`maubase migrate new/up/down/redo/to/status/diff`" + `, ` + "`maubase version`" + `,
` + "`maubase init --update-agent-docs`" + ` (refreshes this file after a
` + "`maubase`" + ` upgrade — see below)

## The one rule with no exception

Never edit the live schema (raw SQL, admin UI create-table, SQL Studio)
without also writing a migration for it. Run ` + "`maubase migrate diff`" + `
before considering schema work done. Full migration workflow (new/up/
down/redo/to/status/diff, the Up/Down marker format, checksums):
` + specURL("{{DOCREF}}", "migrations-cli.md") + `

## Keeping this file current

This file is pinned to **{{VERSION}}** — every link above resolves to
that exact commit/tag's spec, not whatever a newer maubase might say.
After upgrading the ` + "`maubase`" + ` binary this project runs, re-run
` + "`maubase init --update-agent-docs`" + ` to refresh the version marker and
every link above to match; it only replaces the content between this
comment and the matching ` + "`<!-- maubase:end -->`" + ` below, so anything
you've added elsewhere in this file (including below that line) is left
alone.
<!-- maubase:end -->
`

// jsClientSection is spliced into agentDocBody's {{JSCLIENT}} placeholder
// only when this project has a vendored client (see hasJSClient) — most
// projects don't opt into --js-client, and a section pointing at a
// directory that doesn't exist would be actively misleading rather than
// just unused.
var jsClientSection = `
## Client library

This project has maubase's TypeScript client vendored at
` + "`maubase-client/`" + ` (` + "`maubase init --js-client`" + `, generated for
{{VERSION}}) — prefer it over raw fetch calls for auth (` + "`client.auth`" + `)
and auto-REST (` + "`client.data`" + `). It does not cover storage or realtime;
see ` + "`maubase-client/README.md`" + `, or the spec links below, for those.
`
