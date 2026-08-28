# Embedded admin UI

The owner plane's human-facing surface: server-rendered HTML under
`/admin/ui/*`, using the same owner-plane session cookie and roles
`internal/ownerauth`'s JSON API (`/admin/auth`, `/admin/owners`,
`/admin/audit-log`, `/admin/maintenance`) already enforces — this is a
second, browser-friendly surface over the same plane, not a new
authorization model. No JS build step: `html/template` + a vendored
htmx/CSS file, `go:embed`'d, the same "deliberately plain, no asset
pipeline" approach the OAuth login/consent screens already take (see
`internal/oauth/templates.go`). See `internal/adminui` for the
implementation, `test/adminui_test.go` for the tests.

## Shell

## ADMINUI-01: An anonymous visitor is redirected to the login page
Given no owner-plane session cookie,
when a browser requests any `/admin/ui/*` page,
then it's redirected (not shown a JSON `401`) to `/admin/ui/login`.

## ADMINUI-02: Signing in redirects to the dashboard
Given the login form at `/admin/ui/login`,
when a bootstrapped owner submits correct credentials,
then they're redirected to `/admin/ui` and an owner-plane session cookie
is set — the same cookie `POST /admin/auth/login` sets (see
`spec/owner-plane.md` OWNR-03).

## ADMINUI-03: Wrong credentials re-show the form with an error
When the login form is submitted with a wrong password,
then the same page is re-rendered (not a redirect) with an error message
visible in the body, and no session cookie is set.

## ADMINUI-04: Logging out clears the session and returns to login
Given a signed-in owner,
when they submit the logout control,
then the session cookie is cleared and they're redirected to
`/admin/ui/login`; the same session no longer authenticates any
`/admin/ui/*` page afterward.

## ADMINUI-05: A page above the signed-in owner's role shows 403, not a redirect
Given a signed-in owner whose role doesn't meet a page's minimum (e.g. a
`viewer` visiting `/admin/ui/owners`, which needs `admin`+),
then the response is `403` with a plain explanation — distinct from
ADMINUI-01's redirect, since this owner *is* authenticated, just not
privileged enough for this page.

## ADMINUI-31: The sidebar and dashboard only link to pages a role can actually open
Given a signed-in owner,
when they view the sidebar or the dashboard,
then a link to a page above their role's minimum simply isn't there —
a `viewer` or `developer` sees no Members/Audit log/Maintenance link
(admin+), and anyone below `owner` sees no SQL Studio link (owner-only) —
rather than a link that leads to ADMINUI-05's Forbidden page. This is
purely about what's offered to click: `GET`ting one of those routes
directly (a stale bookmark, a hand-typed URL) still gets `403` exactly as
ADMINUI-05 describes — hiding the link is a navigation nicety, not a new
or looser authorization boundary.

## Owners (`/admin/ui/owners`, admin+ to view, owner-only to mutate — same as `/admin/owners`'s JSON API)

## ADMINUI-06: The owners page lists every owner account
Given one or more owner-plane accounts,
when an admin+ owner visits `/admin/ui/owners`,
then every account's email and role appear in the page.

## ADMINUI-07: Only an owner-role account can create a new owner from the page
Given the owners page's create-owner form,
when an `owner`-role account submits it,
then a new account is created and appears in the list afterward;
when an `admin`-role account (below `owner`) submits the same form
(directly, bypassing the UI, since the page wouldn't show it a form for
an action it can't take),
then the response is `403` and no account is created.

## ADMINUI-08: An owner-role account can delete another owner, but never the last one
Given more than one owner-plane account,
when an `owner`-role account submits the delete control for a different
account,
then that account is gone and no longer appears in the list;
when there is only one owner-plane account left and its own delete is
attempted,
then it's refused (mirrors `spec/owner-plane.md`'s `ErrLastOwner`
behavior) and the account still exists afterward.

## Users (`/admin/ui/users`, viewer+ to view, developer+ to create/delete/revoke)

The customer-plane counterpart to Owners: `internal/auth`'s `users` table,
the same accounts `spec/identity.md` governs (signup/login/sessions) —
never to be confused with the owner-plane accounts on `/admin/ui/owners`,
which administer the deployment itself. It's also distinct from
`/admin/ui/data`: the `users` table is deliberately excluded from the
generic data browser (ADMINUI-11) since customer accounts need
purpose-built handling — password hashing on create, cascading delete,
session revocation — rather than raw CRUD form fields over a
`password_hash` column. This is the panel Supabase's Auth › Users and
Firebase's Authentication › Users tab are the equivalent of.

## ADMINUI-25: The users page lists every customer account, paginated
Given one or more customer accounts (`internal/auth` users,
spec/identity.md),
when a viewer+ owner visits `/admin/ui/users`,
then every account's id, email, and created_at appear, newest first,
paginated the same way the data browser and audit log already are.

## ADMINUI-26: A user's detail view shows their profile and session count
Given a customer account,
when a viewer+ owner visits `/admin/ui/users/{id}`,
then their id, email, created_at, updated_at, and current active session
count are shown, alongside the delete and revoke-sessions controls gated
per ADMINUI-28/29.

## ADMINUI-27: A developer+ owner can create a customer account directly from the UI
Given the users page's create-user form (email + password),
when a developer+ owner submits it,
then a new customer account is created under the same rules
`POST /api/auth/signup` enforces — password at least 8 characters
(IDNT-03), email not already registered (IDNT-02) — except no session
cookie is set for the admin doing the creating, since this creates an
account on someone else's behalf rather than signing the admin in as
them. It appears in the users list afterward and the given credentials
can sign in normally at `/api/auth/login`.
When a viewer-role owner submits the same form directly (bypassing the
UI), the response is `403` and no account is created.

## ADMINUI-28: A developer+ owner can force-delete a customer account
Given the users page's delete control for an account,
when a developer+ owner submits it,
then the account is deleted with exactly the same consequences as that
customer's own `DELETE /api/auth/me` (IDNT-10/11/13): none of their
sessions authenticate anything afterward, any outstanding OAuth access
token issued on their behalf stops working, and every row they owned in
every owner-scoped auto-REST collection is gone. The only differences
from the self-service path are who initiated it and that it's
audit-logged with the admin as actor (ADMINUI-30) rather than the user
themselves.
When a viewer-role owner attempts the same directly, `403` and no
account is deleted.

## ADMINUI-29: A developer+ owner can revoke a customer account's sessions without deleting it
Given a customer account with one or more active sessions,
when a developer+ owner submits the "sign out everywhere" control for
that account,
then every session belonging to that account is revoked immediately —
subsequent requests using any of those session tokens get `401`, the
same guarantee IDNT-08 makes for a user's own logout — and the account
itself still exists and can sign in again afterward to get a fresh
session.

## ADMINUI-30: Every user-management action is recorded to the audit log
Given the users page,
when a developer+ owner creates a customer account, force-deletes one, or
revokes its sessions,
then a corresponding audit-log entry is recorded (`user_create`,
`user_delete`, or `user_sessions_revoked`) naming the admin as actor and
the affected customer account as target (id, email) — the same
accountability pattern every other mutating action in this admin UI
already follows (OWNR-14/15, ADMINUI-20).

## Audit log (`/admin/ui/audit-log`, admin+)

## ADMINUI-09: The audit log page lists entries newest-first, paginated
Given more entries than fit on one page,
when an admin+ owner visits `/admin/ui/audit-log`,
then the most recent entries appear first, with a way to page further
back — the same ordering `GET /admin/audit-log`'s JSON API already
guarantees.

## Maintenance (`/admin/ui/maintenance`, admin+)

## ADMINUI-10: The purge-sessions control triggers the same purge as the JSON API
Given the maintenance page,
when an admin+ owner submits its "purge expired sessions" control,
then expired session rows are purged exactly as `POST
/admin/maintenance/purge-sessions` already does (spec/maintenance.md
MAINT-01..03, including the audit log entry), and the resulting counts
are shown on the page.

## Data browser (`/admin/ui/data`, viewer+ to read, developer+ to write)

This is deliberately a different surface from the customer-facing
`/api/data/{table}` (`spec/auto-rest.md`): that API is OAuth-token-scoped
and row-filtered to `owner_id`; an owner using this browser is
administering/support-viewing the deployment's actual data, so it shows
**every** row regardless of who owns it, and isn't subject to
`_policies` overrides (`spec/access-rules.md`) at all — those govern only
the customer-facing token-authenticated path, never the owner plane's own
access to its own database.

## ADMINUI-11: The collection list shows every exposed auto-REST collection
Given one or more application tables discovered by auto-REST,
when a viewer+ owner visits `/admin/ui/data`,
then every one of them is listed, and baas's own internal/reserved
tables (`users`, `sessions`, `_policies`, `files`, etc. — the same set
`spec/auto-rest.md` REST-COL-01 excludes) never appear, same as they
never appear via `/api/data/*`.

## ADMINUI-12: A collection's row listing shows every row, not just one caller's
Given an owner-scoped table with rows belonging to more than one
customer user,
when a viewer+ owner visits `/admin/ui/data/{collection}`,
then every row appears, regardless of `owner_id` — unlike
`GET /api/data/{table}` (REST-OWNERSHIP-01), this view is never filtered
by row.

## ADMINUI-13: A developer+ owner can create, edit, and delete rows directly
Given the data browser for a collection,
when a developer+ owner submits its create form, an edit form for an
existing row, or a delete control,
then the row is created/updated/deleted accordingly — including being
able to set `owner_id` explicitly on create or edit, unlike
`POST /api/data/{table}` (REST-OWNERSHIP-03), which always overrides it
to the caller's own subject. This is a deliberate admin-only capability:
reassigning a row's ownership isn't something the customer-facing API
ever allows.

## ADMINUI-32: Editing a row can happen inline, without leaving the list
Given the data browser for a collection,
when a developer+ owner submits an in-place edit via `POST
/admin/ui/data/{collection}/{id}` as an htmx request (an `HX-Request`
header present — the inline "Save" control on the row itself, as opposed
to the standalone edit page's plain form submission),
then the response is the row's own updated `<tr>` markup, not a redirect
— enough for the row to be swapped back into the table in place. A
non-htmx request to the same route (the full-page edit form, still
reachable directly, as a no-JS fallback) still gets the existing
redirect-to-the-list behavior (ADMINUI-13 is otherwise unaffected: same
authorization, same result). A column holding SQL `NULL` renders as a
distinct, visibly-not-blank marker in the row display, so it can't be
mistaken for an empty string.

## ADMINUI-33: Column headers sort the row listing
Given the data browser for a collection,
when a viewer+ owner requests `/admin/ui/data/{collection}?sort={column}&dir=desc`
naming one of that collection's real columns,
then rows come back ordered by that column, descending (ascending is the
default whenever `dir` isn't exactly `desc`) — clicking a column header
is this same request, not a separate mechanism. A `sort` value that
isn't one of the collection's actual columns is ignored rather than
erroring, falling back to the collection's default order (its primary
key).

## ADMINUI-40: Pagination has a well-defined boundary, even when sorting by a non-unique column
Given a collection (or the users list) with more rows than fit on one
page,
when a viewer+ owner pages through it via `limit`/`offset` (the "Older →"
/ "← Newer" links, or the equivalent manual query string) — including
while sorted by a column whose values repeat across many rows, per
ADMINUI-33 — then each row appears on exactly one page: the row
immediately after a page's last one is the very next page's first,
never repeated and never skipped, for every page boundary the listing
has. `AdminListRows` orders by the requested sort column plus the
primary key as an ascending tie-break whenever they differ (`ORDER BY
{sort}, {primary key}`, mirroring how `ListUsers` already orders by
`created_at DESC, id DESC`) precisely so that ties in the sort column
don't leave the boundary between two pages undefined.

## ADMINUI-14: A viewer-role owner can read but not write
Given the data browser,
when a `viewer`-role owner visits it,
then no create/edit/delete controls are shown, and a direct `POST` to
create, update, or delete a row (bypassing the UI) gets `403`.

## ADMINUI-15: The data browser ignores `_policies` entirely
Given a collection with a `_policies` row of `denied` for some operation
(spec/access-rules.md ACCESS-05/06/07),
when a developer+ owner performs that same operation through
`/admin/ui/data/{collection}`,
then it succeeds normally — `_policies` rules apply only to
`/api/data/*`'s OAuth-token-authenticated callers, never to the owner
plane's own direct access to its database.

## Create table (`/admin/ui/tables/new`, developer+ — same tier as writing existing rows)

Auto-REST's own docs (`spec/auto-rest.md`) say adding a table is only
possible via a deployment's own migrations, with "no dynamic
schema-creation API in v1." This is that API, scoped to the admin UI:
creating a table here is exactly equivalent to a deployer writing a
`CREATE TABLE` migration by hand — same schema shape (a TEXT `id`
primary key on every table, the same `owner_id` convention for row
scoping), same discovery mechanism, just triggered from a form instead
of a file. `internal/restapi.Server.ReloadSchema` (a fresh `Discover`
swapped in atomically) is what makes the result visible without a
restart — see its doc comment.

## ADMINUI-21: A developer+ owner can create a table from the UI, live
Given the "new table" form (a name, an optional "row-scoped" checkbox,
and zero or more column name/type/required rows),
when a developer+ owner submits it,
then the table exists immediately: it appears in `/admin/ui/data`'s
collection list, is browsable/writable at `/admin/ui/data/{name}`, and
is exposed at `/api/data/{name}` too — no restart required.

## ADMINUI-22: The "row-scoped" checkbox adds a real owner_id column
Given the create-table form with "row-scoped" checked,
when the table is created,
then it has an `owner_id` column and behaves exactly like any other
owner-scoped auto-REST table afterward (default `owner` rule on every
operation, per spec/access-rules.md) — this isn't a special case, it's
the same `owner_id`-column convention every other table already uses.

## ADMINUI-23: An invalid or colliding table name is rejected, not silently applied
Given the create-table form,
when the submitted name isn't a valid identifier (doesn't start with a
letter, or contains anything besides lowercase letters/digits/
underscores) or names one of baas's own reserved/internal tables,
then no table is created and the form re-shows with an error explaining
why.

## ADMINUI-24: A viewer can't create a table
Given a `viewer`-role owner,
when they visit `/admin/ui/tables/new` or `POST /admin/ui/tables`
directly,
then both get `403` — the same write-tier gating (developer+) as every
other write in the data browser.

## SQL Studio (`/admin/ui/sql`, owner-only)

Unrestricted raw SQL against the whole database — every table, including
`sessions`, `oauth_clients`, and every other internal table the data
browser and auto-REST both deliberately hide. Meaningfully more
dangerous than anything else in the admin UI, so it's gated to `owner`
rather than `admin`+ like the rest of that tier, and every run is
audit-logged regardless of outcome. One statement per run in v1 — a
simple leading-keyword heuristic (`SELECT`/`PRAGMA`/`EXPLAIN`/`WITH` vs.
everything else) decides whether it's run as a query (rows back) or an
exec (a rows-affected count back), not a real SQL parser.

## ADMINUI-16: Only an owner-role account can open SQL Studio
Given a signed-in owner-plane account below role `owner` (viewer,
developer, or admin),
when they visit `GET /admin/ui/sql` or `POST /admin/ui/sql`,
then both get `403` — distinct from every other admin-only page here,
which only requires `admin`+.

## ADMINUI-17: A SELECT shows its result rows
Given the SQL Studio page,
when an owner runs a `SELECT` (or `PRAGMA`/`EXPLAIN`/a `WITH` query),
then the response shows the returned rows in a table, with the query's
own column names as headers and a row count.

## ADMINUI-18: A schema-changing statement takes effect immediately, everywhere
Given the SQL Studio page,
when an owner runs `CREATE TABLE`, `ALTER TABLE`, `DROP TABLE`, or any
other DDL,
then it executes (showing rows-affected, same as any other mutation),
and the change is immediately visible in `/admin/ui/data` and at
`/api/data/*` — the same `ReloadSchema` the create-table form triggers
runs after every SQL Studio statement, so nothing here needs a restart
to take effect either (ADMINUI-21's "live" guarantee, extended to raw
SQL).

## ADMINUI-19: A query error is shown inline, not a 500
Given the SQL Studio page,
when the submitted statement is invalid or fails (bad syntax, no such
table, a constraint violation),
then the response is `200` with the database's error message shown on
the page — the page itself still renders, the caller isn't just left
looking at a generic error.

## ADMINUI-20: Every run is recorded to the audit log, regardless of outcome
Given the SQL Studio page,
when an owner submits any statement — whether it succeeds or errors,
whether it's a read or a write,
then a `sql_executed` entry appears in `GET /admin/audit-log` naming
that owner as actor and carrying the statement text (truncated if very
long) — unlike every other audited action here, which only logs the
consequential ones, raw SQL logs every attempt, since the point is a
complete record of who ran what against the database directly.

## Security posture

The admin UI's entire CSRF defense is `SameSite=Lax` on the owner
session cookie (`internal/ownerauth.SetCookie`), plus `HttpOnly` and
`Secure`. There is no CSRF token and no Origin/Referer check anywhere in
`internal/adminui` or the owner-plane routes. `SameSite=Lax` is a real,
currently-adequate defense against classic cross-site form CSRF (a
`Lax` cookie isn't sent on a cross-site `POST`), but it is the *entire*
defense protecting every state-changing admin-UI action — including SQL
Studio, the single most destructive surface in the product. A future
change that loosens `SameSite` (e.g. to fix an unrelated cross-subdomain
login issue) would silently reopen CSRF on all of it; ADMINUI-34 exists
to catch that regression at the cookie-attribute level, before it ever
gets the chance to matter.

Rendering is `html/template` throughout `internal/adminui`, whose
auto-escaping is what keeps hostile content in emails, table names, and
data-browser row values from executing as markup — there are no
`template.HTML(...)` casts anywhere in the package today. ADMINUI-35
exists to catch a future regression here too (a cast added for "just
this one rich-text field"), the same way ADMINUI-34 does for the cookie
attributes.

## ADMINUI-34: The owner session cookie carries every defensive attribute
Given a successful `POST /admin/auth/login`,
when the response's `Set-Cookie` header for the owner session cookie is
inspected,
then it carries `SameSite=Lax`, `HttpOnly`, and `Secure` — the complete
CSRF defense described above, locked in at the attribute level so a
regression here is caught immediately rather than discovered as a live
vulnerability.

## ADMINUI-35: Hostile markup in rendered content is escaped, not executed
Given an account or data-browser row created with a value containing
markup (e.g. an email like
`<script>window.__xss=1</script>@example.com`, or a row field
containing `<img onerror=...>`),
when a page that renders that value (the users list, a data-browser row)
is requested,
then the response body contains the HTML-entity-encoded form of that
value, never the raw, executable markup — proving `html/template`'s
auto-escaping is actually in effect for hostile input, not just assumed.
A very long field value (near RFC 5321's ~254-character email limit, or
several kilobytes for a data-browser value) also renders without an
error and without breaking the page layout badly enough to hide its
action buttons.

## ADMINUI-36: Every hidden route rejects direct navigation, for every under-tier role
Given every route ADMINUI-31 hides a link to for some role — the
admin+ pages (`/admin/ui/owners`, `/admin/ui/audit-log`,
`/admin/ui/maintenance` and its purge action) and the owner-only pages
and actions (creating/deleting an owner, SQL Studio's page and its run
action),
when a signed-in owner below that route's minimum role requests it
directly (bypassing the UI — a stale bookmark, a hand-typed URL, a
raw HTTP client),
then the response is `403` for every one of those (route, role) pairs,
not just the one combination ADMINUI-31 happens to check on its way to
asserting the sidebar's own contents — including the owner-only delete
action specifically, which no other test exercises via direct
navigation (only its create counterpart, ADMINUI-07, does).

## ADMINUI-37: The audit log page renders each entry's metadata
Given an audit-log entry that carries metadata — the SQL text for
`sql_executed`, the role an account was created/removed with for
`owner_create`/`owner_delete`, the revoked-session count for
`user_sessions_revoked` — every one of these already captured per
ADMINUI-20/OWNR-14/15,
when an admin+ owner visits `/admin/ui/audit-log`,
then that metadata is visible on the page, not just retrievable via
`GET /admin/audit-log`'s JSON API — an admin using the actual product
surface can see what SQL an owner ran, or what role an account was
created with, without hand-crafting a JSON request.

## ADMINUI-38: An edit form can set a field to NULL, and shows when it already is
Given an existing row with a nullable column currently set to a real
value,
when a developer+ owner opens its edit form,
then that field's text input is pre-filled with the current value, and
a "Set to NULL" checkbox next to it is unchecked.
When they check that box and save (regardless of whatever the text
input still holds — the checkbox overrides it),
then the field is stored as SQL `NULL`, not an empty string — the data
browser's NULL-vs-empty-string distinction (ADMINUI-32) extends to
actually *setting* NULL from the edit view, not just displaying a NULL
that arrived some other way.
Given a row whose nullable column is already `NULL`,
when its edit form is opened again,
then the checkbox is shown already checked and the text input empty —
an admin can tell they're looking at a NULL field before touching
anything, rather than a field that just happens to be blank.

## ADMINUI-39: Concurrent edits to the same row silently last-write-wins
Given a row two developer+ owners are both editing (support work on the
same customer's data, say — the data browser has no lock or lease
mechanism to stop this), each having loaded the edit form before the
other saves,
when the first submits their edit, and then the second submits theirs
(built off the same pre-edit values, unaware of the first's change),
then the second submission succeeds unconditionally — `200` for an
htmx inline edit or the usual redirect for the full-page form, exactly
as if it were the only edit made — and the row ends up holding the
second owner's values, silently overwriting the first's, with no
error, no conflict warning, and nothing in the audit log distinguishing
this from an uncontested edit. `AdminUpdateRow` writes a bare
`UPDATE ... WHERE id = ?`; this is deliberate v1 behavior, not a bug —
optimistic concurrency (a version or `updated_at` check) is future
scope if two-editors-at-once support work turns out to be common
enough to need it.

## ADMINUI-41: An out-of-range or invalid limit/offset is rejected, not silently substituted
Given `/admin/ui/users` or `/admin/ui/data/{collection}`,
when `limit`/`offset` are both omitted, the page renders with the
documented defaults exactly as before; but when `limit` or `offset` *is*
given in the query string and is invalid — non-numeric, zero or negative
for `limit`, negative for `offset`, or a `limit` over the stated maximum
of 200 (a hand-edited URL, since the page's own "Older →"/"← Newer"
links never generate one) —
then the response is `400` with a plain-text error naming the problem,
rather than silently substituting the default as if the parameter had
been omitted — the same treatment `spec/auto-rest.md`
REST-PAGINATION-01 and `spec/owner-plane.md` OWNR-21 give their own
paginated endpoints.
