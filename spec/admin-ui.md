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
