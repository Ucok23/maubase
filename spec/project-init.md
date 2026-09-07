# Project scaffolding (`maubase init`)

`maubase init [dir]` is the zero-to-one step for attaching maubase to a
new project — see issue #146. It creates the on-disk pieces a
deployment needs before `maubase migrate`/the server itself has
anything to work with: a starter `migrations/` directory (see
spec/migrations-cli.md), a `.env.example` documenting every `MAUBASE_*`
env var, a Claude Code skill so an AI agent working in the project
understands what maubase is without reading its source (see issue
#161), and a `.gitignore` entry for the default `data/` directory.
`dir` defaults to `.` (the current directory) when omitted.

This is meant to run once, against a fresh or not-yet-maubase-configured
directory — it's a scaffolding command, not an idempotent "sync my
config" one.

## INIT-01: `maubase init` scaffolds a starter migrations directory and an .env.example
Given an empty (or otherwise maubase-unconfigured) directory,
when an operator runs `maubase init`,
then it creates `migrations/0001_init.sql` (a valid, comment-only
starter migration — safe to apply as-is) and `.env.example` (listing
every `MAUBASE_*` env var from `internal/config/config.go`, each showing
its default), printing the paths it created.

## INIT-02: `maubase init` creates a `.gitignore` entry for `data/`
Given a directory with no `.gitignore` yet,
when an operator runs `maubase init`,
then it creates one containing a `data/` entry — the default
`MAUBASE_DB_PATH`/`MAUBASE_STORAGE_DIR` both live under `data/`, which
is deployment state, not something to commit.

## INIT-03: `maubase init` appends to an existing `.gitignore` rather than overwriting it
Given a directory whose `.gitignore` already exists (for unrelated
reasons — most real projects already have one) and doesn't already
cover `data/`,
when an operator runs `maubase init`,
then it appends a `data/` line to the existing file, leaving every
other line untouched — never overwrites or refuses over this file the
way it does over `migrations/`/`.env.example` below.

## INIT-04: `maubase init` is a no-op addition when `.gitignore` already covers `data/`
Given an existing `.gitignore` that already has a `data/` (or `/data`,
`/data/`) entry,
when an operator runs `maubase init`,
then it leaves the file exactly as it was — no duplicate entry added.

## INIT-05: `maubase init` refuses to overwrite an already-initialized project
Given `migrations/`, `.env.example`, `.claude/skills/maubase/SKILL.md`,
and/or `.maubase/AGENTS.md` already exist in the target directory (plus
`maubase-client/` too, when `--js-client` was passed),
when an operator runs `maubase init` again,
then it fails with an error naming exactly which of those already
exist, and creates or modifies nothing at all — including not touching
`.gitignore`, even though that file alone existing wouldn't have
blocked it.

## INIT-06: `maubase init <dir>` scaffolds into a named directory, not just the current one
Given a directory path is passed as `maubase init`'s argument,
when it runs,
then every file it creates (`migrations/0001_init.sql`, `.env.example`,
`.claude/skills/maubase/SKILL.md`, `.maubase/AGENTS.md`, `.gitignore`)
is created under that directory, not the current working directory.

## INIT-07: `maubase init` scaffolds a Claude Code skill pointing at what maubase is
Given an empty (or otherwise maubase-unconfigured) directory,
when an operator runs `maubase init`,
then it creates `.claude/skills/maubase/SKILL.md` — a skill (frontmatter
`name`/`description`) that is deliberately a pointer file, not a
manual: for this project's own current state (its actual tables,
columns, and access rules — created by a migration, the admin UI's
create-table form, or SQL Studio, all *after* this file is generated,
so it can never describe them) it names live sources instead of
embedding a snapshot that goes stale the moment the schema changes —
`GET /api/schema` (spec/schema-introspection.md) foremost, with
`maubase migrate status`/`migrations/*.sql` as the fallback when that's
disabled. For maubase's own fixed concepts (the `serve`/`migrate`/
`version` commands, the migration-always convention and why
`migrate diff` matters, and the API surface every deployment exposes)
it links each spec file rather than restating it as fresh prose — see
the link-pinning paragraph below for why not doing that matters just
as much as the live-state pointer above.

The generated content is wrapped in a `<!-- maubase:begin
vX.Y.Z -->`/`<!-- maubase:end -->` managed block naming the exact
maubase version (`maubase version`'s own version string) that generated
it, so staleness after a later upgrade is visible rather than silent —
and so `maubase init --update-agent-docs` (INIT-09) only replaces that
block later, leaving anything a person appends below
`<!-- maubase:end -->` untouched.

Each API area also links to its own spec file at a stable
`raw.githubusercontent.com/Ucok23/maubase/<ref>/spec/*.md` URL — never
`main`, which drifts. `<ref>` is resolved from the same build info the
version marker above comes from: the exact release tag for a `go
install .../maubase@vX.Y.Z` build, or the exact commit hash for a local
`go build`/`make build` from a git checkout (falling back to `main`
only when neither is available at all, e.g. a Docker build, whose
context excludes `.git`). The point isn't just staleness-avoidance: an
agent working in the scaffolded project has no reliable way to know
maubase's actual behavior on its own — training data predates this
version, and any memory it might have of maubase's own source from
unrelated context isn't this project's build — so the skill explicitly
tells it to fetch the pinned spec rather than guess or trust either.

## INIT-08: `maubase init` also scaffolds a tool-agnostic `.maubase/AGENTS.md`
Given an empty (or otherwise maubase-unconfigured) directory,
when an operator runs `maubase init`,
then, alongside `.claude/skills/maubase/SKILL.md` (INIT-07), it also
creates `.maubase/AGENTS.md` — the same content, minus the Claude Code
skill's YAML frontmatter, for an agent that doesn't discover
`.claude/skills/*` at all. `.maubase/` rather than a root-level
`AGENTS.md`: a root `AGENTS.md` is exactly the kind of file the
project's own app is likely to have (or want) for itself, the same
ownership-collision reason `SKILL.md` isn't a root `CLAUDE.md` either
(see INIT-07's own rationale) — `.maubase/` isn't a directory anything
else plausibly owns.

Both files share one managed block per INIT-07's version-pinning
mechanism; `--update-agent-docs` (INIT-09) refreshes both together.

## INIT-09: `maubase init --update-agent-docs` refreshes the managed block in place
Given a project previously scaffolded by `maubase init` (or, for that
matter, one where the agent-context files don't exist yet at all — see
below), and a `maubase` binary that may be a different version than
whatever generated them,
when an operator runs `maubase init --update-agent-docs [dir]`,
then for each of `.claude/skills/maubase/SKILL.md` and
`.maubase/AGENTS.md`:

- if the file doesn't exist yet, it's created fresh — same content
  `maubase init` itself would write, so a project that predates this
  flag entirely can still adopt it with no separate migration step;
- if it exists and has a `<!-- maubase:begin -->`...`<!-- maubase:end
  -->` block, only that block's content is replaced with a freshly
  rendered one (new version marker, spec links re-pinned to the ref
  this binary was built from) — everything before it (the skill's YAML
  frontmatter) and everything a person appended after
  `<!-- maubase:end -->` is left untouched;
- if it exists but has no such block at all (a person deleted or
  rewrote it by hand), the command refuses and names the file, rather
  than guessing where regenerated content belongs in a file it no
  longer recognizes — and does not touch the other of the two files
  just because one refused.

Unlike a bare `maubase init`, this never fails over files already
existing, and never touches `migrations/`, `.env.example`, or
`.gitignore` — those aren't version-pinned content the way the
agent-context files are. `--js-client` can't be combined with this
flag (INIT-10 covers why).

## INIT-10: `maubase init --js-client` vendors the built TypeScript client
Given the `--js-client` flag,
when an operator runs `maubase init --js-client [dir]` against a
directory not yet initialized (including `maubase-client/` itself not
already existing — an existing one is a conflict, refused exactly like
`migrations/` or `.env.example` per INIT-05),
then it additionally copies the built `@maubase/client` output
(`sdk/js/dist/*`, embedded into the `maubase` binary at build time —
see `sdk/js/embed.go`) into `maubase-client/` at the project root, plus
a generated `maubase-client/README.md` naming the exact maubase version
it was vendored from and — carried over from `sdk/js/README.md`'s own
"What's not here yet" — that it covers only auth and auto-REST, not
storage or realtime. Nothing here publishes to npm or introduces a
second version number: whatever `sdk/js/dist` contained when this
`maubase` binary was built is exactly what gets vendored, tied 1:1 to
`maubase version`'s own output.

`maubase-client/` is meant to be committed (vendored source, not
instance data like `data/`) — `maubase init` never adds it to
`.gitignore`.

When `--js-client` was passed, the `SKILL.md`/`AGENTS.md` content
generated in the same run (INIT-07/INIT-08) additionally links
`maubase-client/`, telling an agent to prefer it over raw fetch calls
for auth and auto-REST while repeating the storage/realtime gap; a
plain `maubase init` with no `--js-client` omits that section entirely
rather than pointing at a directory that doesn't exist.
