# Cross-cutting scenarios

Most of this project's specs describe one feature in isolation, each
with its own `testserver.Options` fields to turn it on
(`internal/testserver`'s package doc). That's deliberate — it keeps each
feature's own test suite focused — but it also means no single test has
ever stood up a server that looks like a plausible real deployment,
where several of these optional features are active at once and have to
coexist correctly. This file is for scenarios that specifically span
more than one other spec file's territory.

## XFEAT-01: Several optional features work correctly when combined on one deployment
Given a single server configured with more than two of `testserver.Options`'
optional features active simultaneously — a Redis-backed realtime relay
(`Relay`), a fake social login provider (`SocialProviders`), a
`_policies` override on an application table, real file storage, a
captured password-reset email (`EmailSender`), and a bootstrapped owner
account, all at once, the way a real production deployment combining
Redis-backed realtime + social login + a custom access policy + file
uploads plausibly would —
when a customer signs in via the social provider, obtains an OAuth
access token, creates a row in the `_policies`-governed table, uploads a
file, and a second customer requests a password reset, while a realtime
subscriber (via the Relay-backed broker) and the bootstrapped owner
(via the admin UI) both watch,
then every feature behaves exactly as its own spec says it should with
nothing else active: the realtime subscriber receives the row's
`created` event over the Relay-backed broker (`spec/realtime.md`), the
`_policies` override is honored for both the creator and a
separately-scoped witness token (`spec/access-rules.md`), the upload
succeeds and is retrievable (`spec/storage.md`), the reset email is
captured by the fake sender with a valid link (`spec/password-reset.md`),
the social-login account exists and is visible from `/api/auth/me`
(`spec/social-login.md`), and the owner's admin UI session sees the same
row through the data browser (`spec/admin-ui.md`) — no feature's
behavior changes, breaks, or leaks into another's just because several
are wired into the same process at once.

Found by: cross-cutting audit (GAP-8). See `test/cross_cutting_test.go`.

## AUDIT-CUST-01: Customer-plane actions are audit-logged too, in the same log the owner plane uses
`internal/audit`'s event vocabulary used to be entirely owner-plane
(`login`, `login_failed`, `logout`, `owner_create`, `owner_delete`,
`sessions_purged`, `sql_executed`, `user_create`, `user_delete`,
`user_sessions_revoked`) — every customer-facing action (signup, login,
logout, password reset, social sign-in, self-service account deletion)
left no trace anywhere, a silent asymmetry with `spec/owner-plane.md`'s
own framing of the audit log as serving "a real incident review or
compliance audit": "was this account's password reset from an unusual
pattern of requests?" is exactly that kind of question, asked about a
customer account instead of an owner one.

Given a customer performs one of the actions below,
then a new entry appears in the shared `owner_audit_log` table (visible
at `/admin/audit-log`, the same admin-only view `spec/owner-plane.md`
OWNR-16 already gates), with the event name and actor/target shown:

- `POST /api/auth/signup` (success) → `customer_signup`, actor and
  target both the new account.
- `POST /api/auth/login` → `customer_login` (success, actor the
  account, using the request's own email the same way the owner-plane
  login handler does) or `customer_login_failed` (wrong password *or*
  an email that doesn't correspond to any account — recorded either
  way, the same "a failed attempt is worth capturing regardless of
  whether the account is real" treatment OWNR-12 already gives the
  owner plane — target the attempted email, no actor).
- `POST /api/auth/logout` → `customer_logout`, resolved from the
  session *before* it's invalidated (a missing or already-invalid
  token logs nothing, since there's no one left to name) — the same
  ordering the owner-plane logout handler uses.
- `POST /api/auth/forgot-password` → `customer_password_reset_requested`,
  but **only when the email belongs to a real account** — unlike a
  failed login, a request against an email that doesn't exist has no
  account to attribute the entry to and isn't itself the kind of signal
  this entry exists to help spot. This never changes
  `spec/password-reset.md` PWRESET-02's response (`204` either way) or
  timing; it only decides whether a *log entry* is written, which the
  customer-facing response never reveals either way.
- `POST /api/auth/reset-password` (success) → `customer_password_reset_completed`,
  actor and target both the account whose password changed.
- `DELETE /api/auth/me` (success) → `customer_account_deleted`, actor
  and target both the removed account — recorded using the identity
  captured before deletion, the same "the entry outlives the account"
  treatment OWNR-15/17 give `owner_delete`.
- A successful `GET /api/auth/social/{provider}/callback` round trip
  (`spec/social-login.md`) → `customer_social_sign_in`, actor and
  target the resulting account, with metadata `provider`, `new_account`
  (true only when a brand-new account was created, not when an
  existing one was matched by email or already-signed-in session), and
  `already_signed_in` (true when this was a "link a second sign-in
  method" request rather than an anonymous resolution) — one event name
  covering every outcome `LoginOrCreateViaSocial` can produce, since
  metadata is what an incident review actually needs to tell them
  apart, not three different event names.

Customer-plane events are named distinctly from their owner-plane
counterparts (`customer_login` vs. `login`) even where the shape is
identical, so an admin scanning the shared log can always tell which
plane produced a given entry without inspecting actor/target.
Deliberately not logged: `GET /api/auth/me` (a read, not an action —
none of the owner-plane's own read endpoints are logged either, per
OWNR-16's own list of what *is* recorded), and `GET /api/auth/me/export`
(same reasoning — see `spec/identity.md` IDNT-09, unaffected by this
scenario).
