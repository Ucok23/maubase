# Migrations CLI

`maubase migrate` manages a deployment's own application-schema
migrations (the `.sql` files in `MAUBASE_MIGRATIONS_DIR`, applied by
`internal/db.MigrateDir` on every server boot — see the auto-REST section
of README.md) as a standalone command, without starting the HTTP server.
It never touches maubase's own embedded internal schema
(`internal/db.Migrate` — users, sessions, oauth, owner tables, etc.),
which the server always applies itself on boot regardless of this
command.

This is part of the fuller migration tooling tracked in issue #144
(closed — the remaining item, drift capture from the admin UI/SQL
Studio, is tracked separately as #149) — `new`, `up`, `down`, `redo`,
`to`, `status`, and checksum verification all exist.

A migration file's forward SQL goes under a `-- +migrate Up` marker line;
an optional `-- +migrate Down` marker line after it holds the SQL that
reverses it (see MIGCLI-13). A file with neither marker (every migration
written before `down` existed) is entirely "up" SQL with no down
section — it can still be applied by `up`, it just can't be reverted by
`down` (MIGCLI-16).

## MIGCLI-08: `maubase migrate new <name>` scaffolds the next-numbered migration file
Given an application migrations directory (possibly empty, possibly
containing files up to some highest numeric prefix),
when an operator runs `maubase migrate new <name>`,
then it creates one `.sql` file in that directory numbered one past the
highest existing prefix (`0001` when the directory is empty or new),
with `<name>` lowercased and slugified into the filename, and it prints
the created file's path. It never opens or creates the database.

## MIGCLI-09: `maubase migrate new` creates the migrations directory if missing
Given `--dir` (or `MAUBASE_MIGRATIONS_DIR`) points at a directory that
doesn't exist yet,
when an operator runs `maubase migrate new <name>`,
then it creates that directory along with the first migration file in
it (`0001_...`) — unlike `up`/`status`, for which a missing directory is
a no-op, `new`'s whole point is to start one.

## MIGCLI-10: The next migration number follows the highest existing one, not the file count
Given a migrations directory where a lower-numbered file has been
deleted (e.g. only `0002_...` and `0003_...` remain, `0001_...` is
gone),
when an operator runs `maubase migrate new <name>`,
then the new file is numbered `0004` — one past the highest number
present — not `0003` (which a naive file-count-based scheme would
collide on).

## MIGCLI-11: `maubase migrate new` requires a name
Given no name argument (only flags, or nothing at all),
when an operator runs `maubase migrate new`,
then it fails with a usage error and creates no file and no directory.

## MIGCLI-01: `maubase migrate up` applies pending migrations in order
Given an application migrations directory containing one or more `.sql`
files that haven't been applied yet,
when an operator runs `maubase migrate up`,
then it applies each of them in filename order — identical ordering and
tracking to the automatic apply-on-boot — and prints the filename of
each one it applied, in the order applied.

## MIGCLI-02: `maubase migrate up` is a clean no-op once everything's applied
Given every `.sql` file in the migrations directory has already been
recorded as applied,
when an operator runs `maubase migrate up` again,
then it makes no schema changes and reports that nothing is pending,
rather than re-running anything or erroring.

## MIGCLI-03: `maubase migrate up` needs neither a running server nor maubase's own embedded schema
Given a brand new SQLite database file that has never been touched by
`maubase`'s own embedded migrations (no `users`/`sessions`/etc. tables
exist),
when an operator runs `maubase migrate up` directly against it,
then it applies the pending application migrations and exits — no HTTP
server starts, and the embedded internal schema is still absent
afterward, since this command only ever touches the application
migrations directory.

## MIGCLI-04: A missing migrations directory is a clean no-op, not an error
Given `--dir` (or `MAUBASE_MIGRATIONS_DIR`) points at a directory that
doesn't exist,
when an operator runs `maubase migrate up`,
then it exits successfully reporting nothing pending, matching
`db.MigrateDir`'s own "missing directory is fine" behavior.

## MIGCLI-05: `maubase migrate status` lists every migration and whether it's applied
Given a migrations directory with a mix of already-applied and
not-yet-applied `.sql` files,
when an operator runs `maubase migrate status`,
then it lists every file in filename order, marking each as applied
(with the timestamp it was applied at) or pending, and applies nothing
itself.

## MIGCLI-06: `--db`/`--dir` flags override the configured defaults
Given a caller passes `--db <path>` and/or `--dir <path>` to
`maubase migrate up`, `down`, `redo`, `to`, or `status`,
when the command runs,
then it operates against that database file / migrations directory pair
instead of falling back to `MAUBASE_DB_PATH` / `MAUBASE_MIGRATIONS_DIR`
(which remain the defaults when a flag is omitted) — for `down`/`redo`,
this holds whether the flags come before or after the optional `n`
argument; for `to`, before or after the required `<version>` argument.

## MIGCLI-07: An unrecognized `migrate` subcommand fails clearly without touching the database
Given `maubase migrate <x>` where `<x>` isn't `new`, `up`, `down`,
`redo`, `to`, or `status`,
when it runs,
then it exits non-zero with an error naming the unrecognized subcommand,
and never opens (or creates) the database file at all.

## MIGCLI-12: An unrecognized flag to `migrate new` fails clearly instead of becoming part of the name
Given `maubase migrate new <name> --bogus-flag`, or any other token
starting with `-` that isn't `--dir`/`-dir` (in `--dir=value` or
`--dir value` form),
when it runs,
then it fails with an error naming the unrecognized flag, rather than
silently folding that token into the created file's name.

## MIGCLI-13: Only a migration's Up section runs when it's applied
Given a migration file with a `-- +migrate Up` section that creates
something and a `-- +migrate Down` section that would destroy it (e.g.
`DROP TABLE`),
when `maubase migrate up` applies it,
then only the Up section's SQL executes — the Down section's SQL never
runs during apply, even though it's plain text sitting later in the same
file.

## MIGCLI-14: `maubase migrate down` reverts the most recently applied migration by default
Given two or more migrations have been applied, each with a Down
section,
when an operator runs `maubase migrate down` with no argument,
then it reverts only the single most recently applied one — running its
Down SQL and removing its applied record — leaving every earlier one
applied, and prints the reverted filename.

## MIGCLI-15: `maubase migrate down <n>` reverts the last n applied migrations, newest first
Given three migrations have been applied, each with a Down section,
when an operator runs `maubase migrate down 2`,
then it reverts the two most recently applied ones, newest first, and
leaves the oldest one applied.

## MIGCLI-16: A migration with no Down section can't be reverted, and nothing is changed
Given the migration `down` would next revert has no `-- +migrate Down`
section (or an empty one),
when an operator runs `maubase migrate down`,
then it fails with an error naming that migration, and leaves it applied
exactly as before — no schema change, no record change.

## MIGCLI-17: A reverted migration is pending again and gets reapplied by `up`
Given a migration was applied and then reverted via `maubase migrate
down`,
when an operator runs `maubase migrate up` afterward,
then it treats that migration as pending again and reapplies it (its Up
SQL runs again and it's re-recorded) — reverting removes the applied
*record*, not the migration file itself.

## MIGCLI-18: `maubase migrate down` with nothing applied is a clean no-op
Given no application migrations have been applied yet (including when
the migrations directory doesn't exist at all),
when an operator runs `maubase migrate down`,
then it reports nothing to revert and makes no changes, rather than
erroring.

## MIGCLI-19: An unrecognized flag to `migrate down` fails clearly, the same as `new`
Given `maubase migrate down --bogus-flag`, or any other unrecognized
`-`-prefixed token,
when it runs,
then it fails with an error naming the unrecognized flag rather than
misparsing it as the optional `n` argument.

## MIGCLI-20: `maubase migrate redo` reverts then reapplies the most recently applied migration by default
Given a migration has been applied, with a Down section,
when an operator runs `maubase migrate redo` with no argument,
then it reverts that migration's schema change and immediately reapplies
it (its Up SQL runs again, and it's re-recorded as applied), reporting
it as "redone".

## MIGCLI-21: `maubase migrate redo <n>` redoes the last n applied migrations, reapplied in original forward order
Given three migrations have been applied, each with a Down section,
when an operator runs `maubase migrate redo 2`,
then it reverts the two most recently applied ones (newest first, same
as `down 2`) and reapplies them in their original forward order —
oldest of the two first — leaving the oldest (untouched) migration
applied throughout.

## MIGCLI-22: `redo` never touches a migration that was never applied
Given a migration exists on disk but has never been applied (still
pending), alongside migrations that have been applied,
when an operator runs `maubase migrate redo` (with an `n` that only
covers the already-applied ones),
then the pending migration is left exactly as it was — still pending,
not applied as a side effect of `redo`'s internal use of `up`-like
apply logic.

## MIGCLI-23: `redo` fails and reapplies nothing when a migration has no Down section
Given the migration `redo` would need to revert has no `-- +migrate
Down` section,
when an operator runs `maubase migrate redo`,
then it fails the same way `down` would on that migration, and
reapplies nothing — the migration is left applied exactly as before.

## MIGCLI-24: Applying a migration records a checksum of its exact content
Given a migration file,
when `maubase migrate up` applies it,
then a checksum of that file's exact on-disk content at the moment it
was applied is recorded alongside its applied record.

## MIGCLI-25: `maubase migrate status` flags an applied migration whose file has changed since it was applied
Given an already-applied migration's file is edited afterward, so its
current content no longer matches the checksum recorded when it ran,
when an operator runs `maubase migrate status`,
then that migration is reported as modified since it was applied,
distinctly from an ordinary applied line.

## MIGCLI-26: A migration applied before checksum verification existed isn't flagged as modified
Given an applied migration's `schema_migrations` row has no checksum
recorded (the pre-existing case — a row from before this feature, or a
`schema_migrations` table predating the `checksum` column entirely),
when an operator runs `maubase migrate status`,
then it's reported as an ordinary applied migration, not as modified —
there's nothing to compare its current content against, which isn't
evidence of tampering.

## MIGCLI-27: `maubase migrate up` warns about, but doesn't block on, a modified already-applied migration
Given an already-applied migration's file has been edited since it ran,
and a separate, unrelated migration is pending,
when an operator runs `maubase migrate up`,
then it prints a warning naming the modified migration, but still
applies the pending migration normally — a modified migration warns,
it never fails the whole command (this must stay safe to run
unconditionally on every server boot, same as today).

## MIGCLI-28: With no flags and no env overrides, `migrate` resolves its defaults against the current directory, like any ordinary command-line tool
Given an operator has `cd`'d into their project directory (no
`--db`/`--dir` flags, no `MAUBASE_DB_PATH`/`MAUBASE_MIGRATIONS_DIR` set)
and created a `migrations/` folder there themselves,
when they run `maubase migrate new <name>`, then `maubase migrate up`,
then both resolve `MAUBASE_DB_PATH`/`MAUBASE_MIGRATIONS_DIR`'s literal
defaults (`data/maubase.db`, `migrations`) relative to that directory —
the same "just run it" experience as any ordinary CLI tool, not one
that requires flags or env vars to work at all.

## MIGCLI-29: `maubase migrate to <version>` applies forward to a target ahead of the current state
Given three migrations exist and only the first is applied,
when an operator runs `maubase migrate to <version>` naming the third,
then it applies the second and third, in filename order, and stops
there — nothing past the named target is applied even if more
migrations exist after it.

## MIGCLI-30: `maubase migrate to <version>` reverts back to a target behind the current state
Given three migrations are all applied, each with a Down section,
when an operator runs `maubase migrate to <version>` naming the first,
then it reverts the third and the second, newest first (the same order
`down` uses), leaving only the first applied.

## MIGCLI-31: `maubase migrate to <version>` targeting the current state is a no-op
Given the named migration is already exactly the most-recently-applied
one (it's applied, and nothing after it is),
when an operator runs `maubase migrate to <version>` naming it,
then it makes no changes and reports it's already at that version.

## MIGCLI-32: `to` accepts either a migration's exact filename or its bare numeric prefix
Given `<version>` is passed as either a migration's exact filename
(`0002_add_index.sql`) or just its numeric prefix (`2` or `0002`),
when the command runs,
then both forms resolve to the same migration and produce the same
result.

## MIGCLI-33: `to` fails clearly, without changing anything, when the version can't be resolved to exactly one migration
Given `<version>` matches no migration file's name or numeric prefix —
or, with inconsistently-padded filenames (e.g. both `3_x.sql` and
`0003_y.sql` existing), matches more than one —
when the command runs,
then it fails with an error naming the problem, and neither applies nor
reverts anything.

## MIGCLI-34: Reverting past a migration with no Down section fails the same way `down` does
Given moving to an earlier `<version>` would require reverting a
migration that has no `-- +migrate Down` section,
when an operator runs `maubase migrate to <version>`,
then it fails naming that migration (the same failure as MIGCLI-16),
leaving state exactly as far as it got — nothing beyond the
un-revertible migration is touched.
