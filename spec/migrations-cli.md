# Migrations CLI

`maubase migrate` manages a deployment's own application-schema
migrations (the `.sql` files in `MAUBASE_MIGRATIONS_DIR`, applied by
`internal/db.MigrateDir` on every server boot — see the auto-REST section
of README.md) as a standalone command, without starting the HTTP server.
It never touches maubase's own embedded internal schema
(`internal/db.Migrate` — users, sessions, oauth, owner tables, etc.),
which the server always applies itself on boot regardless of this
command.

This is the first slice of the fuller migration tooling tracked in
issue #144 (scaffolding new migration files, rollback/`down`, `redo`,
targeting a specific version, checksum verification, and drift capture
from the admin UI/SQL Studio) — only `up` and `status` exist so far.

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
`maubase migrate up` or `maubase migrate status`,
when the command runs,
then it operates against that database file / migrations directory pair
instead of falling back to `MAUBASE_DB_PATH` / `MAUBASE_MIGRATIONS_DIR`
(which remain the defaults when a flag is omitted).

## MIGCLI-07: An unrecognized `migrate` subcommand fails clearly without touching the database
Given `maubase migrate <x>` where `<x>` isn't `up` or `status`,
when it runs,
then it exits non-zero with an error naming the unrecognized subcommand,
and never opens (or creates) the database file at all.
