# Schema introspection (`GET /api/schema`)

Every other spec in this project describes a *fixed* API surface —
routes that exist the same way in every deployment. Auto-REST's actual
collections are the opposite: discovered from whatever tables a
deployment's own migrations (or the admin UI's create-table form, or
SQL Studio) happen to have created, so there is no way to know what
`/api/data/*` actually exposes for a given deployment without asking it
directly. `GET /api/schema` is that ask: the same `internal/restapi.Registry`
that `/api/data/*` itself is built from, over HTTP.

This exists specifically so an agent (or any other tool) working on a
project backed by maubase has a live, authoritative way to find out
what tables/columns/access rules actually exist right now — as opposed
to reading migration files by hand (unreliable: SQL Studio and the
admin UI's create-table form both change the live schema without
touching `migrations/` at all, see `spec/migrations-cli.md`'s
`migrate diff`) or guessing.

Deliberately **not** on by default — see SCHEMA-02.

## SCHEMA-01: The schema endpoint requires the same scope reading data does
Given `MAUBASE_ENV=development` and an access token with the
`records:read` scope,
when the caller `GET`s `/api/schema`,
then the response is `200` with `{"collections": [...]}` — one entry
per collection `/api/data/*` would expose, each with its `name`,
`columns` (`name`, `type`, `not_null`, `primary_key`), `pk_column`,
`pk_is_integer`, `owner_column`, and its resolved `read_rule`/
`create_rule`/`update_rule`/`delete_rule` (`"owner"`/`"shared"`/
`"denied"`, after any `_policies` override — see
`spec/access-rules.md`). No token, or one missing `records:read`,
is rejected exactly like any `/api/data/*` `GET` would be
(`spec/auto-rest.md` REST-CRUD-02) — there is no separate, weaker
auth path for this endpoint.

## SCHEMA-02: The schema endpoint doesn't exist at all outside development
Given `MAUBASE_ENV` unset, or set to anything other than `development`
(including `production`, the default),
when any caller — with or without a valid access token —
`GET`s `/api/schema`,
then the response is `404`, indistinguishable from a route that was
never registered: not `401`/`403`, which would confirm to an
unauthenticated caller that the route exists at all. Same convention
`spec/social-login.md` uses for an unconfigured provider.

## SCHEMA-03: The schema endpoint never exposes maubase's own internal tables
Given `MAUBASE_ENV=development`,
when a caller with `records:read` `GET`s `/api/schema`,
then the response names only collections `/api/data/*` itself would
expose — maubase's own reserved tables (`users`, `sessions`, OAuth
tables, `_policies`, etc. — `spec/auto-rest.md` REST-COL-01) never
appear, regardless of token.

## SCHEMA-04: The schema endpoint reflects a table added after startup, with no restart
Given `MAUBASE_ENV=development`, and a table created at runtime via the
admin UI's create-table form or SQL Studio (which call `ReloadSchema`,
swapping in a freshly discovered registry — same mechanism
`spec/admin-ui.md`'s create-table scenarios rely on),
when a caller with `records:read` `GET`s `/api/schema` afterward, in
the same running process,
then the new table appears — this is the live registry `/api/data/*`
itself reads from, not a snapshot taken at startup.
