# Maintenance: session cleanup and login rate-limiting

Operational hygiene that isn't identity or owner-plane behavior on its
own, but touches both planes: expired session rows eventually get purged,
and login endpoints on both planes are throttled per client IP.

## Session cleanup

Expired sessions already fail authentication on their own (see IDNT-*'s
and OWNR-*'s expiry handling) — purging them is pure storage hygiene, not
something that changes what a valid or expired token can do. A background
job does this automatically and periodically; the scenarios below cover
the on-demand trigger, since that's the part with externally observable
behavior.

## MAINT-01: Purging sessions requires admin role
Given a signed-in owner-plane session,
when they `POST /admin/maintenance/purge-sessions`,
then a `viewer` or `developer` session gets `403 Forbidden`, an `admin` or
`owner` session gets `200`, and no session/bearer token at all gets `401`.

## MAINT-02: Purging sessions never touches a still-valid session
Given a signed-in customer-plane user and a signed-in owner-plane admin,
when the admin calls `POST /admin/maintenance/purge-sessions`,
then the response is `200` with counts of how many rows were removed,
and afterward the customer's session still authenticates `GET
/api/auth/me` (its `expires_at` hasn't passed, so it isn't touched) —
purging removes only rows whose expiry has already passed.

## MAINT-03: Purging sessions is audit logged
Given a signed-in owner-plane admin,
when they `POST /admin/maintenance/purge-sessions`,
then a `sessions_purged` entry appears in `GET /admin/audit-log` with
that admin as actor.

## Login rate-limiting

Every endpoint that submits a password against `auth.Service.Login` or
`ownerauth.Service.Login` throttles repeated attempts from the same
client IP, regardless of whether an individual attempt succeeds or
fails — the point is bounding brute-force credential guessing, not just
counting failures. That's `POST /api/auth/login`, `POST
/admin/auth/login`, the login step embedded in `POST /oauth/authorize`
(it calls the identical `auth.Service.Login` a plain login does — this
was a complete bypass of the JSON endpoint's own throttle before it was
covered here too), and `POST /admin/ui/login` (the human-facing admin
page). Every deployment ships with a default limit; see the README for
how to tune it.

These aren't all one shared budget. `POST /admin/auth/login` and
`POST /admin/ui/login` (owner plane) each have their own limiter
instance, entirely separate from the one `POST /api/auth/login`,
`POST /api/auth/forgot-password`/`reset-password`, and `/oauth/authorize`'s
login step (customer plane) share among themselves — see MAINT-06. The
customer-plane group sharing one bucket among themselves is deliberate
(they're all "someone repeatedly hitting an auth endpoint for this
IP" abuse); registering an OAuth client (`POST /oauth/register`) is its
own bucket too, separate from every login endpoint, since registration
is incidental to all sorts of legitimate setup unrelated to guessing a
password.

## MAINT-06: Customer-plane and owner-plane login throttles are independent budgets
Given a client IP that has exhausted the customer-plane login budget
(`POST /api/auth/login`, `/forgot-password`, `/reset-password`, or
`/oauth/authorize`'s login step — see above) and gets `429` from all of
them,
when that same IP then attempts `POST /admin/auth/login`,
then it's evaluated against its own, still-fresh budget — unaffected by
the customer-plane exhaustion. The reverse holds too: exhausting
`/admin/auth/login`'s budget doesn't touch the customer-plane one. A
shared office/NAT IP an admin also uses shouldn't let unrelated
customer-plane traffic lock that admin out of the owner plane during an
incident, or vice versa.

## MAINT-04: Excess login attempts are rejected
Given a login endpoint (customer plane, owner plane, the admin UI's own
login page, or the login form embedded in `/oauth/authorize`)
configured with a request limit over a window,
when a client sends more than that many `POST` requests to it within one
window,
then the requests within the limit get their normal response (`200` or
`401`, whichever the credentials warrant — or, for the HTML surfaces,
the re-rendered login page with an error), and requests beyond the
limit get `429 Too Many Requests` with a `Retry-After` header, until the
window resets.
