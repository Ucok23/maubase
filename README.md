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
    `POST /oauth/token`. Refresh tokens rotate on every use; replaying a
    rotated-away one is detected as reuse (not just "not found") and
    revokes the entire downstream grant chain — the standard compromise
    response for a stolen refresh token.
  - Revocation: `POST /oauth/revoke` (RFC 7009)
  - Consent management: `GET /api/auth/me/consents` lists every
    third-party client a signed-in user has a standing scope grant for;
    `DELETE /api/auth/me/consents/{client_id}` revokes one outright —
    deletes the consent record and every outstanding token issued to
    that client — without touching the account or any other client's
    grant. The consent screen itself also shows previously-granted
    scopes alongside newly-requested ones on re-authorization, and
    honors unchecking one as revoking it, rather than always keeping
    every scope a client was ever granted (see spec/oauth-authorize-
    and-consent.md AUTHZ-11).
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
  - A deployment can define its own tables three ways, all of which just
    create an ordinary table `Discover` then picks up — none is more
    "real" than the others: dropping numbered `.sql` files in
    `migrations/` (configurable via `MAUBASE_MIGRATIONS_DIR`), applied on
    every startup — the only one that's disk-versioned and reviewable
    like a normal migration history; the admin UI's **create table** form
    (`/admin/ui/tables/new`, `developer`+), live with no restart; or
    running raw `CREATE TABLE` in **SQL Studio** (`/admin/ui/sql`,
    `owner`-only). See the embedded admin UI section below for the latter
    two. The `migrations/` files can also be scaffolded, applied,
    reverted, redone, or inspected without starting the server: `maubase
    migrate new <name>`, `up`, `down [n]`, `redo [n]`, and `status` (see
    `spec/migrations-cli.md`; more subcommands are tracked in #144). A
    migration's forward SQL goes under a `-- +migrate Up` marker; an
    optional `-- +migrate Down` section is what `down`/`redo` run to
    revert it. `up`/`status` also record and check a checksum of each
    migration's content, warning (never blocking) if an already-applied
    file has been edited since it ran.
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
  change feed. No replay/backfill in v1. Fan-out is in-process by default
  — fine for this project's usual single-server-process design — and
  optionally cross-process via Redis pub/sub (`MAUBASE_REDIS_URL`, see
  below) once more than one instance is running behind a load balancer
  (spec/realtime.md RT-09).

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
  - **Create table** (`/admin/ui/tables/new`, `developer`+): name a
    table, optionally check "row-scoped" (adds a real `owner_id`
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
- `MAUBASE_MAX_REQUEST_BODY_KB` (default `1024`) — largest `/api/data/*`
  create/update request body accepted (`413` past this); same
  reject-before-fully-reading behavior as uploads
- `MAUBASE_RESEND_API_KEY` / `MAUBASE_EMAIL_FROM` / `MAUBASE_PASSWORD_RESET_URL`
  — password reset (see below); all three unset is a valid, common state
  (a deployment that never uses it doesn't need any of them) and gets
  `internal/email.NoopSender`, which fails loudly the first time
  something actually tries to send rather than silently dropping the
  email
- `MAUBASE_GOOGLE_CLIENT_ID` / `_SECRET`, `MAUBASE_GITHUB_CLIENT_ID` / `_SECRET`,
  `MAUBASE_SOCIAL_LOGIN_REDIRECT_URL` — social login (see below); each
  provider is independently optional (unset client id/secret just means
  that one 404s — see `spec/social-login.md` SOCIAL-05)
- `MAUBASE_REDIS_URL` (default unset) — set to a `redis://` connection
  string shared by every instance to upgrade realtime fan-out from
  single-process to cross-process; see "Realtime scaling" below

## Realtime scaling

By default (`MAUBASE_REDIS_URL` unset), `internal/realtime.Broker` fans
events out purely in-process: a subscriber connected to this server
process only ever sees writes that also happened on this same process.
That's the right default — it matches this project's usual single-VPS,
single-process shape, and `internal/db.Open` already pins
`SetMaxOpenConns(1)` since SQLite has one writer anyway.

Running more than one `maubase` process behind a load balancer — a real
option once horizontal scaling of the stateless HTTP layer is the goal,
since SQLite's own file locking already serializes writes across however
many processes point at the same database file — needs each process
started with the same `MAUBASE_REDIS_URL`. That upgrades the broker to
`internal/realtime.RedisRelay`: every write still reaches its own
process's subscribers immediately (a slow or briefly-unreachable Redis
never adds latency to, or fails, the write path that triggered it), and
is additionally relayed over Redis pub/sub so every other process
sharing that Redis delivers it to its own subscribers too. See
`spec/realtime.md` RT-09 and `test/realtime_relay_test.go`.

**Known limitation**: each process's `_policies`-derived access rules
live in its own in-memory `Registry`, reloaded only by that same
process's own admin actions — there's no cross-process invalidation.
After changing `_policies` in a multi-process deployment, restart every
process (not just the one you made the change on) so they all pick it
up; see spec/realtime.md's "cross-process policy-change skew" for the
exact failure mode a stale sibling can otherwise cause.

## Password reset

`POST /api/auth/forgot-password` (`{"email": "..."}`, always `204`,
whether or not that email is registered — never revealing which) emails a
reset link built from `MAUBASE_PASSWORD_RESET_URL` (your own frontend's
page, not something maubase renders) with a one-hour, single-use token
appended as `?token=`. `POST /api/auth/reset-password`
(`{"token": "...", "password": "..."}`) redeems it: sets the new
password and signs the account out everywhere — every session, plus
every OAuth access/refresh token issued to a third-party client while
one of those sessions was active — including whatever session requested
the reset. Delivery is via `internal/email.Sender` —
Resend (`internal/email.ResendSender`) when both `MAUBASE_RESEND_API_KEY`
and `MAUBASE_EMAIL_FROM` are set. See `spec/password-reset.md`.

## Social login

`GET /api/auth/social/{provider}` (`google` or `github`) redirects to
that provider's own sign-in page; `GET
/api/auth/social/{provider}/callback` is where it sends the browser
back to — on success this sets the same identity-layer session cookie
`POST /api/auth/login` does and redirects to
`MAUBASE_SOCIAL_LOGIN_REDIRECT_URL` (your own frontend, not a maubase
page). Completed anonymously, a first-time identity creates an account
(linking to an existing one by email if there's a match, rather than
duplicating it); a returning one just signs in. Completed while
already signed in, it's a "link a second sign-in method" request
instead: a never-before-seen identity links to the *current* session's
account (email-matching doesn't apply here — the active session is
authoritative), and one already linked to a *different* account is
refused (`409`) rather than silently swapping the session to it. See
`spec/social-login.md` SOCIAL-09/10. `internal/social` is maubase
acting as an
OAuth *client* to Google/GitHub — the opposite direction from
`internal/oauth`, which is maubase acting as an OAuth *authorization
server* for third-party apps. See `spec/social-login.md`.

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

## JS/TS client SDK

[`sdk/js/`](sdk/js/) is a TypeScript client (`@maubase/client`, not yet
published) for building an actual app against this backend: `client.auth`
wraps the identity layer above (cookie-based, no setup), and `client.data`
wraps auto-REST — including driving the OAuth PKCE flow `/api/data/*`
requires, which is normally a several-hundred-line undertaking on its
own. See `sdk/js/README.md` for the real explanation of why those are two
different auth models, not one.

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
