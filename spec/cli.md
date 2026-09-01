# The `maubase` command itself

`maubase` is a multi-purpose binary (`serve`, `init`, `migrate ...`) — see
spec/project-init.md and spec/migrations-cli.md for those. This file
covers the CLI's own top-level dispatch: what a bare invocation does,
how the server is actually started, and how an unrecognized command is
handled.

Every subcommand's file/database paths (`MAUBASE_DB_PATH`,
`MAUBASE_MIGRATIONS_DIR`, etc.) resolve relative to the current working
directory by default — this matters specifically for `serve`: a single
binary installed once (`go install
github.com/Ucok23/maubase/cmd/maubase@latest`) and run from inside
different project directories serves a different project each time,
not whichever one it happened to run from before.

## CLI-01: A bare `maubase` invocation prints usage instead of starting the server
Given no subcommand at all,
when an operator runs `maubase` with no arguments,
then it prints usage (naming `serve`, `init`, and `migrate` among the
available commands) and exits successfully, without opening a database,
creating any file, or binding a port — every other action this binary
takes (`init`, `migrate ...`) is an explicit verb, and starting a
long-running server is exactly the kind of thing that shouldn't happen
just because someone ran the command to see what it does.

## CLI-02: `maubase serve` starts the server
Given `MAUBASE_ADDR`/`MAUBASE_DB_PATH` (and friends) configured via env
vars,
when an operator runs `maubase serve`,
then it starts listening on the configured address and answers
`GET /healthz` with `200`.

## CLI-03: An unrecognized top-level command fails clearly
Given `maubase <x>` where `<x>` isn't `serve`, `init`, `migrate`, or
`help`,
when it runs,
then it exits non-zero, printing usage and naming the unrecognized
command — the same "fail clearly, don't guess" convention
`migrate`'s own unrecognized-subcommand handling already follows (see
spec/migrations-cli.md MIGCLI-07).

## CLI-04: `maubase help`, `-h`, and `--help` all print usage and exit successfully
Given any of the three help spellings,
when an operator runs one,
then `maubase` prints the same usage `maubase` with no arguments does,
and exits successfully.

## CLI-05: `maubase version`, `-v`, and `--version` all report what's installed
Given any of the three version spellings,
when an operator runs one,
then `maubase` prints a version line and exits successfully — so someone
with three self-hosted projects, each pointed at a globally-installed
`maubase`, can tell which build they're actually running before filing
a bug or deciding whether to reinstall.

The reported version reflects how the binary was actually built, since
that's what Go's toolchain embeds automatically with no extra build
steps required:
- Installed via `go install .../maubase@vX.Y.Z` (or `@latest`): the
  real module version, e.g. `v1.1.0`.
- Built locally from a git checkout (`go build`/`make build`): a
  VCS-derived pseudo-version (e.g. `v1.1.1-0.20260901085303-5c4f8d5cc456`,
  with a `+dirty` suffix if the working tree has uncommitted changes),
  since modern Go embeds VCS info automatically there too.
- Built with no VCS info available at all (e.g. the Docker image,
  whose build context deliberately excludes `.git`, or an older Go
  toolchain): `(devel)` alone.
