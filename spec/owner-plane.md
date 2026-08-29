# Owner plane: the team running this deployment

This is distinct from `identity.md` and the `oauth-*` specs, which are all
about *customer* users — the end users of whatever app is built on this
backend. The owner plane is about who can administer the backend itself:
schema, OAuth clients, signing keys, other owner accounts. See the
`internal/ownerauth` package doc for the full rationale.

Roles, from least to most privileged: `viewer`, `developer`, `admin`,
`owner`.

## OWNR-01: The first owner is created by bootstrap, not signup
Given a fresh deployment with no owner-plane accounts and
`MAUBASE_BOOTSTRAP_OWNER_EMAIL`/`MAUBASE_BOOTSTRAP_OWNER_PASSWORD` set,
when the server starts,
then an owner-plane account with role `owner` exists for that email,
and it can log in immediately.
There is deliberately no public "sign up as an owner" endpoint — every
owner-plane account traces back to either this bootstrap or an existing
`owner` explicitly creating it.

## OWNR-02: Bootstrap is a no-op once any owner exists
Given an owner-plane account already exists (from a previous bootstrap or
otherwise),
when the server starts again with bootstrap env vars still set,
then no new or duplicate account is created, and existing accounts are
untouched — bootstrap only ever fires once per deployment.

## OWNR-03: An owner can sign in and see their own identity, including role
Given a bootstrapped owner account,
when they `POST /admin/auth/login` with correct credentials,
then the response is `200` with an expiry, and a separate owner-plane
session cookie is set (never the customer-plane cookie).
When they then `GET /admin/auth/me`,
then the response is `200` with their id, email, and role.

## OWNR-04: Wrong credentials are rejected
When someone attempts `POST /admin/auth/login` with a wrong password,
then the response is `401`, and no owner-plane session cookie is set.

## OWNR-05: An anonymous request to any admin route is rejected
Given no owner-plane session cookie,
when a request is made to any `/admin/*` route that requires
authentication,
then the response is `401`.

## OWNR-06: Only an owner-role account can create new owner-plane accounts
Given a signed-in account with role `owner`,
when they `POST /admin/owners` with an email, password, and any valid
role,
then the response is `201` with the new account's id, email, and role.

Given a signed-in account with role `admin`, `developer`, or `viewer`
instead,
when they attempt the same request,
then the response is `403` — creating owner-plane accounts is an
owner-only capability, not just an authenticated one.

## OWNR-07: Listing owner-plane accounts requires at least admin
Given a signed-in account with role `admin` or `owner`,
when they `GET /admin/owners`,
then the response is `200` with the list of accounts.

Given a signed-in account with role `developer` or `viewer`,
when they attempt the same request,
then the response is `403`.

## OWNR-08: The last remaining owner can't be deleted
Given exactly one account with role `owner`,
when an owner attempts to `DELETE /admin/owners/{id}` for that account
(including deleting themselves),
then the response is `409 Conflict`, and the account is not deleted — a
deployment must always retain at least one account that can administer
it.

Given a second `owner`-role account exists,
when one of them is deleted,
then it succeeds, since at least one `owner` remains.

## OWNR-09: Owner-plane sessions and customer-plane sessions never cross
Given a valid owner-plane session cookie,
when it's presented to a customer-plane route (`GET /api/auth/me`),
then it is not accepted as a valid customer session (`401`) — the two
cookies are different, and neither plane's session validates against the
other's.

## OWNR-10: Logging out revokes the owner session immediately
Given a signed-in owner-plane session,
when they `POST /admin/auth/logout`,
then the response is `204`, and that same session no longer
authenticates `GET /admin/auth/me` (`401`).

## Audit log

Every security-relevant owner-plane action is recorded — not just allowed
or denied, but written down: who did it (when known), what happened, and
what/who it happened to. This is what a real incident review or compliance
audit asks for first; see the README's compliance-posture notes.

## OWNR-11: A successful owner login is recorded
Given a bootstrapped owner account,
when they `POST /admin/auth/login` successfully,
then a new entry appears in the audit log with event `login`, that
account as the actor, and a timestamp at or after the request.

## OWNR-12: A failed owner login is recorded, even for an unknown email
When someone `POST /admin/auth/login`s with a wrong password, or an email
that doesn't correspond to any account,
then a new entry appears in the audit log with event `login_failed` and
the attempted email recorded — a failed authentication attempt is exactly
the kind of event an audit trail exists to capture, whether or not the
account is real.

## OWNR-13: Logging out is recorded
Given a signed-in owner-plane session,
when they `POST /admin/auth/logout`,
then a new entry appears in the audit log with event `logout` and that
account as the actor.

## OWNR-14: Creating an owner-plane account is recorded, including who did it
Given a signed-in `owner`-role account,
when they `POST /admin/owners` to create a new account,
then a new entry appears in the audit log with event `owner_create`, the
creator recorded as actor, and the newly created account recorded as
target (id, email, and the role it was created with).

## OWNR-15: Deleting an owner-plane account is recorded, including who did it and who was removed
Given a signed-in `owner`-role account and a second owner-plane account
that can legally be deleted,
when they `DELETE /admin/owners/{id}` for that second account,
then a new entry appears in the audit log with event `owner_delete`, the
deleter recorded as actor, and the removed account recorded as target
(id, email, and the role it had) — even though, by the time anyone reads
the log, that account no longer exists.

## OWNR-16: Reading the audit log requires at least admin
Given a signed-in account with role `admin` or `owner`,
when they `GET /admin/audit-log`,
then the response is `200` with entries ordered newest first.

Given a signed-in account with role `developer` or `viewer`,
when they attempt the same request,
then the response is `403`.

## OWNR-17: An audit entry survives deletion of the account it refers to
Given an `owner_create` entry recording some account as its target,
when that account is later deleted (via `owner_delete`, which is itself
also recorded per OWNR-15),
then the original `owner_create` entry is unchanged and still shows that
account's email — deleting an account doesn't erase the history of what
it did or what was done to it.

## OWNR-18: Concurrent deletes of both remaining owners still leave one standing
Given exactly two owner-role accounts,
when each deletes itself via its own session, concurrently (not
sequentially),
then exactly one succeeds (`204`) and the other is rejected (`409`,
OWNR-08's "last remaining owner" guard) — the guard must hold even when
two requests race to be the one that empties the deployment down to
zero owners, not just when a second request arrives after the first has
already committed.

## OWNR-19: Deleting an owner invalidates their live session immediately
Given an owner-plane account currently signed in with a live session
cookie,
when a second owner deletes that account via `DELETE /admin/owners/{id}`,
then the deleted account's session cookie no longer authenticates any
`/admin/*` or `/admin/ui/*` route right away — not just once its 7-day
TTL eventually elapses. `owner_sessions.owner_id REFERENCES
owner_users(id) ON DELETE CASCADE` (with `foreign_keys` enforcement on)
is what makes this true; this scenario exercises the actual outcome
rather than trusting the schema declaration, so a regression here (a
migration tool that disables FK enforcement, a driver/DSN change) is
caught immediately instead of leaving a just-removed owner fully
authenticated for up to a week with nothing to notice.

## OWNR-20: Owner email matching is case-insensitive too
Given an owner-plane account created as `Admin@Example.com`,
when a second `POST /admin/owners` (or the bootstrap step) is attempted
with `admin@example.com`,
then the response is `409` (email already taken), not a second account
with the same effective address — the same normalize-before-write/
lookup treatment `spec/identity.md` IDNT-14 describes for the customer
plane, backed by the same kind of case-insensitive unique index at the
schema level.

## OWNR-21: An out-of-range or invalid limit/offset on the audit log is rejected, not silently substituted
Given `GET /admin/audit-log`,
when `limit`/`offset` are both omitted, the response uses the documented
defaults exactly as before; but when `limit` or `offset` *is* given and
is invalid — non-numeric, zero or negative for `limit`, negative for
`offset`, or a `limit` over the stated maximum of 200 —
then the response is `400` naming the problem, rather than silently
substituting the default as if the parameter had been omitted — the
same treatment `spec/auto-rest.md` REST-PAGINATION-01 gives
`GET /api/data/{table}`, applied here for the same reason: an admin
explicitly asking for more entries than the max used to silently get
the default page back with no indication their request was truncated.

## OWNR-22: Creating owner-plane accounts is refused one rung down from owner too
Given a signed-in account with role `admin` — the rung immediately
below `owner`, not the widest possible gap —
when they attempt `POST /admin/owners`,
then the response is `403`, same as OWNR-06 already establishes for
`developer`/`viewer`. OWNR-06's own test only ever checked the widest
gap (owner vs. developer); the adjacent rung is where a
`role.AtLeast(...)` off-by-one would actually show up.

## OWNR-23: Reading owner-plane accounts or the audit log is refused one rung down from admin too
Given a signed-in account with role `developer` — the rung immediately
below `admin`, not the widest possible gap —
when they attempt `GET /admin/owners` or `GET /admin/audit-log`,
then the response is `403` for both, same as OWNR-07/16 already
establish for `viewer`. Both of those scenarios' own tests only ever
checked the widest gap (admin/owner vs. viewer); the adjacent rung is
where a `role.AtLeast(...)` off-by-one would actually show up.

## OWNR-24: An expired owner session is rejected
Given an owner-plane session whose `expires_at` has already passed (but
hasn't been purged yet — see `spec/maintenance.md`'s purge-sessions
maintenance action, a separate concern from expiry itself),
when its cookie is presented to any authenticated `/admin/*` or
`/admin/ui/*` route,
then the response is `401`, identical to a session that never existed —
`ValidateSession`'s own `time.Now().After(expiresAt)` check rejects it
directly, without needing a purge to run first. `maintenance.md`'s intro
has stated "expired sessions already fail authentication on their own"
since it was written, but no scenario anywhere ever actually exercised
this for the owner plane specifically — the only expiry-adjacent test
checks that a *customer* session survives a purge unaffected, never the
owner-plane rejection path itself.

## OWNR-25: Deleting a nonexistent owner is a clean 404, not a leaked internal error
Given an id that doesn't correspond to any owner-plane account (never
existed, or was already deleted by an earlier request),
when a signed-in owner calls `DELETE /admin/owners/{id}`,
then the response is `404` with a plain "owner not found" message —
never a `500` with the underlying SQL error string leaked into the
response body. `GetOwner`/`DeleteOwner`'s row lookup returns
`ErrOwnerNotFound` for this case specifically, which
`writeOwnerAuthError` maps to `404`; before this, `sql.ErrNoRows` was
wrapped as a generic `fmt.Errorf("lookup owner: %w", err)` with no
`errors.Is` case matching it, so it fell through the same handler's
default and surfaced as `{"error":"lookup owner: sql: no rows in
result set"}` — a raw internal error string leaked to the client for
the mundane, everyday case of deleting an id that's already gone.

## OWNR-26: Creating an owner account with an invalid role string is rejected
Given a signed-in `owner`-role account,
when they `POST /admin/owners` with `role: "superadmin"` (anything
outside the closed `viewer`/`developer`/`admin`/`owner` vocabulary,
including an empty string),
then the response is `400` with `ErrInvalidRole`'s message, and no
account is created. `Role.IsValid()`/`ErrInvalidRole` exist specifically
for this, and `writeOwnerAuthError` already maps `ErrInvalidRole` to
`400` — this was true by construction but never actually exercised on
either the JSON or HTML admin-ui surface.

## OWNR-27: A malformed or garbage owner session cookie value is a clean 401
Given a request carrying a `maubase_owner_session` cookie whose value is
garbage — an empty string, an extremely long random string, or a value
shaped like a SQL-metacharacter injection attempt — never a value this
deployment actually issued,
when it's presented to `GET /admin/auth/me` (or any other authenticated
`/admin/*` route),
then the response is `401`, the same as no cookie at all — never a
`500`. `ValidateSession` hashes the raw token and looks it up via a
parameterized query, so this was very likely already safe by
construction, but untested — cheap insurance against a future refactor
that starts handling the raw token unsafely (string-concatenated into a
query, say).
