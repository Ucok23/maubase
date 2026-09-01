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
Given `migrations/`, `.env.example`, and/or `.claude/skills/maubase/SKILL.md`
already exist in the target directory,
when an operator runs `maubase init` again,
then it fails with an error naming exactly which of those already
exist, and creates or modifies nothing at all — including not touching
`.gitignore`, even though that file alone existing wouldn't have
blocked it.

## INIT-06: `maubase init <dir>` scaffolds into a named directory, not just the current one
Given a directory path is passed as `maubase init`'s argument,
when it runs,
then every file it creates (`migrations/0001_init.sql`, `.env.example`,
`.claude/skills/maubase/SKILL.md`, `.gitignore`) is created under that
directory, not the current working directory.

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
and so a future regeneration only replaces that block, leaving anything
a person appends below `<!-- maubase:end -->` untouched. (The
regeneration command itself doesn't exist yet — see issue #161 — this
scenario only covers first-time scaffolding.)

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
