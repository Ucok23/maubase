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
