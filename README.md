# maubase

A self-hostable Firebase/Supabase-alternative backend: single Go binary,
SQLite by default (Postgres pluggable later), no Docker required.

## Status: early scaffold

What's here now (v1, step 1 of the roadmap):

- Pure-Go SQLite (`modernc.org/sqlite`, no cgo) with WAL mode + embedded
  file-based migrations, run automatically on startup.
- Identity layer: email/password signup, login, cookie- or
  bearer-token-backed sessions, `bcrypt` password hashing.
- Minimal HTTP API on `chi`: `/api/auth/{signup,login,logout,me}`,
  `/healthz`.
- Account export and erasure: `GET /api/auth/me/export` returns the
  caller's profile plus every row they own across every owner-scoped
  auto-REST collection, grouped by collection name; `DELETE /api/auth/me`
  deletes those same owned rows, revokes every outstanding OAuth grant
  (authorize code/access/refresh token/PKCE request) issued to any
  third-party client on the user's behalf, and then deletes the account
  itself (which cascades to its sessions and OAuth consents). Both require
  an authenticated session/token — see `spec/identity.md` IDNT-09..13.
  OAuth grant revocation matches on the subject embedded in each grant's
  stored session via SQLite's JSON1 extension (`internal/oauth.Storage.
  RevokeForSubject`), since these tables don't carry a plain user-id
  column — fine at the call rate account deletion runs at.

- OAuth 2.1 authorization server (Fosite-backed, `internal/oauth`): lets
  *your* users authorize third-party apps — including MCP clients —
  against your API.
  - Dynamic client registration: `POST /oauth/register` (RFC 7591)
  - Authorize + built-in login/consent screens: `GET|POST /oauth/authorize`
  - Token endpoint (authorization_code + refresh_token, PKCE mandatory):
    `POST /oauth/token`
  - Revocation: `POST /oauth/revoke` (RFC 7009)
  - Discovery: `GET /.well-known/oauth-authorization-server` (RFC 8414),
    `GET /.well-known/jwks.json`
  - Access tokens are RS256 JWTs signed with a persisted, rotatable
    keypair; scopes are a fixed vocabulary (`internal/oauth.AllowedScopes`)
  - `GET /api/oauth/whoami` — demo endpoint protected by
    `oauth.RequireScope`, proving the full register→authorize→token→call
    loop; delete once the real auto-REST layer exists

- Owner plane (`internal/ownerauth`): the team running this deployment,
  entirely separate from the customer plane above — different table,
  different session cookie, never reachable through the OAuth
  authorization server. Fixed, linearly-ranked roles (`owner` > `admin` >
  `developer` > `viewer`), not general RBAC — sized for one small team on
  one deployment, not a permissions engine. See `spec/owner-plane.md`.
  - Bootstrap: set `MAUBASE_BOOTSTRAP_OWNER_EMAIL` / `_PASSWORD` on first run
    to create the first `owner` account; a no-op on every run after
  - `POST /admin/auth/login`, `POST /admin/auth/logout`,
    `GET /admin/auth/me`
  - `GET /admin/owners` (admin+), `POST /admin/owners` (owner-only),
    `DELETE /admin/owners/{id}` (owner-only; refuses to delete the last
    remaining owner)
  - `GET /admin/audit-log` (admin+, paginated via `?limit=&offset=`):
    every login (successful or failed), logout, owner-account
    create/delete, newest first. Actor and target identity (id + email)
    are copied into each entry at write time rather than foreign-keyed, so
    an entry stays fully readable — including naming exactly who did what
    to whom — even after the account it refers to is later deleted. See
    `spec/owner-plane.md`'s "Audit log" section, `internal/audit`.

- Auto-REST (`internal/restapi`): every table in the database that isn't
  one of maubase's own internal tables becomes a `/api/data/{table}` CRUD
  resource, discovered by introspecting the schema — no separate
  config/admin step. A table with an `owner_id` column is automatically
  row-scoped to the calling token's subject (create/list/get/update/delete
  all filtered to "your own rows"); a table without one is shared. Gated
  by the same `records:read`/`records:write` OAuth scopes reserved since
  the authorization server was built. See `spec/auto-rest.md`.
  - `GET /api/data/{table}` (paginated via `?limit=&offset=`),
    `GET /api/data/{table}/{id}`, `POST /api/data/{table}`,
    `PATCH /api/data/{table}/{id}` (partial update),
    `DELETE /api/data/{table}/{id}`
  - A deployment defines its own tables by dropping numbered `.sql` files
    in `migrations/` (configurable via `MAUBASE_MIGRATIONS_DIR`) — applied
    after maubase's own embedded migrations, on every startup. This is the
    *only* way to add application tables; there's no dynamic
    schema-creation API in v1.
  - Known v1 limits: no composite primary keys, no BLOB columns, no
    filtering beyond pagination, single fixed owner-column convention
    (`owner_id`) rather than per-table config.

- Session cleanup and login rate-limiting (`internal/ratelimit`,
  `spec/maintenance.md`): a background janitor periodically purges
  expired rows from the customer- and owner-plane session tables — pure
  storage hygiene, since an expired session already fails authentication
  on its own — and is also reachable on demand via `POST
  /admin/maintenance/purge-sessions` (admin+, audit logged). Separately,
  `POST /api/auth/login` and `POST /admin/auth/login` each throttle
  repeated attempts from the same client IP (`429` + `Retry-After` past
  the limit), counting every attempt, not just failures, so it actually
  bounds brute-force credential guessing.

- File storage (`internal/storage`, `spec/storage.md`): upload/download
  endpoints outside auto-REST — uploads and downloads need multipart/raw-
  byte handling generic JSON CRUD doesn't do — gated by the same kind of
  OAuth scopes as `/api/data/*` (`files:read`/`files:write`) and row-
  scoped to whichever subject uploaded each file, same as the `owner_id`
  convention. Raw bytes are written to disk (`internal/storage.
  LocalBackend`, pluggable for a future S3-style backend) under
  `MAUBASE_STORAGE_DIR`; metadata lives in a `files` table, reserved out of
  auto-REST.
  - `POST /api/storage/files` (`multipart/form-data`, field `file`),
    `GET /api/storage/files` (paginated), `GET /api/storage/files/{id}`
    (metadata), `GET /api/storage/files/{id}/content` (bytes),
    `DELETE /api/storage/files/{id}`
  - Account export/erasure (`GET /api/auth/me/export`,
    `DELETE /api/auth/me`) cover uploaded files too: export lists their
    metadata (not raw bytes), and deletion removes both the bytes and the
    metadata row.

- Realtime subscriptions (`internal/realtime`, `spec/realtime.md`):
  `GET /api/realtime` upgrades to a WebSocket connection (same
  `records:read` scope a `GET` would need), then a client subscribes per
  collection (`{"type":"subscribe","collection":"posts"}`) and gets
  pushed `created`/`updated`/`deleted` messages as rows change, gated by
  the exact same row-level visibility auto-REST's `owner_id` convention
  already enforces for `GET` — nothing bypasses it, since fan-out happens
  inside auto-REST's own write handlers rather than through a separate
  change feed. No replay/backfill in v1, and fan-out is in-process only
  (see the spec's "Known v1 limit" — fine for this project's
  single-server-process design, would need an external broker if that
  ever changed).

- Row-level access rules (`internal/restapi/registry.go`'s
  `applyPolicies`, `spec/access-rules.md`): beyond the owner_id
  convention, a deployment can override a specific operation
  (read/create/update/delete) for a specific collection via a row in the
  reserved `_policies` table, declared the same way application tables
  are — its own migrations, no separate admin API. Three rules:
  `owner` (default for an owner-scoped table), `shared` (default for one
  without `owner_id`; unfiltered by row), and `denied` (that operation
  off entirely, `403`, regardless of caller). Operations are independent
  — e.g. `read: shared` with everything else left at its owner default
  gives public read / owner-only write on one table. Realtime respects
  the same rules: a `denied` read means no subscriber is ever notified of
  that collection's changes either (spec/realtime.md RT-06).

- Embedded admin UI (`internal/adminui`, `spec/admin-ui.md`): server-
  rendered HTML under `/admin/ui/*` — login, dashboard, member management
  ("Members" in the UI; the underlying JSON API and `internal/ownerauth`
  keep the `owner`-plane naming, since that's the role hierarchy's actual
  top role, not the account list itself), audit log, a "purge expired
  sessions" button, a data browser, and SQL Studio — using the same
  owner-plane session cookie and roles the JSON `/admin/*` API already
  enforces. No JS build step: `html/template` plus a vendored htmx.js and
  a hand-written `admin.css`, both `go:embed`'d — the "no asset pipeline"
  approach `internal/oauth`'s login/consent screens took, with a real
  design pass on top (dark sidebar, styled tables/forms/badges) instead
  of bare HTML.
  - **Data browser**: a deliberately different surface from
    `/api/data/{table}` — unscoped by `owner_id` (an owner sees every
    row, not just "their own") and unaffected by `_policies` entirely,
    since those govern only the customer-facing OAuth-token-
    authenticated path. Viewing needs `viewer`+; creating/editing/
    deleting rows (including reassigning a row's `owner_id` directly, an
    admin-only capability) needs `developer`+.
  - **Create table** (`/admin/ui/tables/new`, `developer`+): the dynamic
    schema-creation API auto-REST's own docs used to say didn't exist —
    name a table, optionally check "row-scoped" (adds a real `owner_id`
    column), define columns (name/type/required), and it's live at
    `/admin/ui/data/{name}` and `/api/data/{name}` immediately, no
    restart. `internal/restapi.Server.ReloadSchema` (an atomically
    swapped-in fresh `Discover`) is what makes that possible — the
    registry stopped being fixed-at-startup once this needed to change
    it at runtime.
  - **Users** (`/admin/ui/users`, `viewer`+ to view, `developer`+ to
    write): the customer-plane counterpart to Members — `internal/auth`'s
    `users` table, the accounts `spec/identity.md` governs, viewable and
    manageable the way Supabase's Auth › Users or Firebase's
    Authentication tab work. Deliberately not part of the data browser,
    since `users` is one of the reserved tables that's excluded there —
    creating/deleting an account needs purpose-built handling (password
    hashing, cascading delete, session revocation), not raw CRUD form
    fields over a `password_hash` column. A `developer`+ owner can create
    an account directly, force-delete one (the same cascade as that
    customer's own `DELETE /api/auth/me`: owned rows, files, OAuth
    grants, then the identity record itself — just admin-initiated), or
    revoke all of an account's sessions without deleting it. Every one of
    those is audit-logged.
  - **SQL Studio** (`/admin/ui/sql`, `owner`-only): unrestricted raw SQL
    against the whole database, including internal tables — meaningfully
    more dangerous than anything else here, so it's gated a tier above
    Members/Audit-log/Maintenance, and every run (success or failure) is
    audit-logged. A `CREATE`/`ALTER`/`DROP` run here reloads the schema
    too, same as the create-table form. The editor itself is CodeMirror
    5 (vendored, not a CDN — see `internal/adminui/static/NOTICE.md`)
    with a syntax theme hand-matched to the rest of the UI, and every
    run shows a Supabase-style "Success · N rows · Xms" banner instead
    of a bare row count.

## Not yet built (see roadmap)

Nothing outstanding on the original roadmap — see "Compliance posture"
below for what's still explicitly left to a deployer, and the spec files
under `spec/` for anything with more headroom (e.g. `spec/access-rules.md`
covers a single override per operation; multi-condition policies would be
a bigger follow-up, not a v1 gap).

## Compliance posture

maubase is self-hosted software, not a hosted service — so compliance
obligations (GDPR controller duties, a SOC 2 audit, etc.) fall on whoever
*deploys* it, not on this project. What maubase can do is (a) not get in the
way of that, and (b) provide the technical primitives a deployer would
otherwise have to build themselves. Current state, split honestly:

- **Already there**: password hashing (bcrypt), session tokens stored
  only as salted hashes (raw token never touches the DB), OAuth consent
  records (`oauth_consents`) tracking which scopes a user granted to which
  client, a real role-separated owner plane so "who can touch customer
  data" has an actual answer, account export/erasure (`GET
  /api/auth/me/export`, `DELETE /api/auth/me` — see above, including OAuth
  grant revocation) covering the two concrete GDPR data-subject rights
  that are actually code-shaped, and an owner-plane audit log
  (`GET /admin/audit-log`) recording who did what to the deployment
  itself — the first thing a real security review or incident
  investigation asks for.
- **Explicitly left to the deployer**: encryption at rest (disk/OS-level),
  encryption in transit (TLS terminates at whatever reverse proxy sits in
  front of maubase — this project doesn't do TLS itself), backups, and data
  residency (which region the VPS is in). These are infrastructure
  decisions, not something a Go binary should own.
- **Missing**: nothing further code-shaped comes to mind right now — the
  rest of GDPR/SOC 2 is process and organizational, not something software
  can satisfy on a deployer's behalf. (Session/token cleanup and login
  rate-limiting, still on the roadmap above, are general operational
  hygiene rather than compliance-specific.)

None of this should be read as a compliance claim about maubase itself —
there isn't a meaningful sense in which self-hosted OSS "is" GDPR or SOC 2
compliant. It's a description of which primitives exist today and which
ones a deployer would still need to build or configure themselves.

## Running it

```sh
make run
# or
go build -o bin/maubase ./cmd/maubase && ./bin/maubase
```

Config via env vars (see `internal/config`):

- `MAUBASE_ADDR` (default `:8080`)
- `MAUBASE_DB_PATH` (default `data/maubase.db`)
- `MAUBASE_ISSUER` (default `http://localhost:8080`) — this server's own
  public base URL; must be what clients actually use to reach it, since
  it's baked into JWT `iss` claims and discovery metadata
- `MAUBASE_BOOTSTRAP_OWNER_EMAIL` / `MAUBASE_BOOTSTRAP_OWNER_PASSWORD` — set both
  on first run to create the first owner-plane account; a no-op on every
  run after (see the owner plane section below)
- `MAUBASE_MIGRATIONS_DIR` (default `migrations`) — where a deployment's own
  application-schema `.sql` files live; see the auto-REST section below
- `MAUBASE_LOGIN_RATE_LIMIT` (default `10`) / `MAUBASE_LOGIN_RATE_WINDOW_SECONDS`
  (default `60`) — at most this many `POST /api/auth/login` or `POST
  /admin/auth/login` attempts per client IP per window
- `MAUBASE_SESSION_CLEANUP_INTERVAL_SECONDS` (default `3600`) — how often the
  background janitor purges expired session rows
- `MAUBASE_STORAGE_DIR` (default `data/storage`) — where uploaded file bytes
  are written, one file per upload
- `MAUBASE_MAX_UPLOAD_MB` (default `25`) — largest single file upload
  accepted; a bigger request body is rejected before it's fully read

## Testing

Tests are spec-first: [`spec/`](spec/) describes expected behavior from an
external caller's point of view (browser, app, MCP client) — written and
reviewed independently of the implementation, not derived from reading it.
[`test/`](test/) is a black-box suite that drives a real, fully-wired
server over plain HTTP (via `internal/testserver`) and asserts each spec
scenario by its ID (e.g. `IDNT-04`, `AUTHZ-06`). A failing test names the
exact sentence in `spec/` it's checking.

```sh
make test
# or: go test ./...
```

When behavior needs to change, update the spec first, then the code, then
watch the test go from red to green — not the other way around.

[`e2e/`](e2e/) is a separate, small Playwright suite that drives the
embedded admin UI in a real browser and builds a storyboard report —
screenshots per step plus a video, all in one self-contained HTML file —
since that's the one surface here with an actual UI to look at; `test/`
never opens a browser. See `e2e/README.md`.

```sh
make e2e   # then open e2e/report/index.html
```

## Try the auth flow

```sh
curl -c cj.txt -X POST localhost:8080/api/auth/signup \
  -d '{"email":"you@example.com","password":"correcthorse"}'

curl -b cj.txt localhost:8080/api/auth/me
```

## Design notes

- `db.Open` sets `SetMaxOpenConns(1)`: SQLite only has one writer anyway,
  so serializing at the pool level keeps error handling simple. Fine at
  small-VPS scale; revisit only if profiling says otherwise.
- Session tokens are random 32 bytes, stored server-side only as a SHA-256
  hash — the raw token never touches the database.
- The DB layer is plain `database/sql` with `?` placeholders kept out of
  handlers, so a Postgres adapter can slot in later without touching the
  HTTP layer.
- The OAuth authorization server is a distinct layer from the identity
  layer: it issues tokens *to third-party apps* on behalf of an identity,
  and is what a resource server (an MCP server, say) checks — the plain
  `/api/auth/*` session cookie is only for this server's own first-party
  surfaces (its future admin UI, primarily).
- **Known limitation**: Fosite's JWT signer only verifies against the
  *current* signing key, so `oauth.RequireScope` (stateful, same-process
  introspection) can't validate a token signed before a manual key
  rotation. An external resource server verifying JWTs itself against
  `/.well-known/jwks.json` doesn't have this problem — the token's header
  carries the `kid` it was signed with, so the right key is always
  selectable from the published set.
