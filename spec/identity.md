# Identity: sign up, sign in, sessions

## IDNT-01: A new user can sign up
Given no account exists for an email address,
when they `POST /api/auth/signup` with that email and a password of at
least 8 characters,
then the response is `201` with the created user's id and email,
and a session cookie is set — they're signed in immediately, no separate
login step required.

## IDNT-02: Signing up with an email already in use is rejected
Given an account already exists for an email address,
when someone tries to sign up again with that same email,
then the response is `409 Conflict`,
and no new account is created.

## IDNT-03: Signing up with a weak password is rejected
When someone tries to sign up with a password under 8 characters,
then the response is `400 Bad Request`,
and no account is created.

## IDNT-04: Correct credentials sign a user in
Given a registered account,
when they `POST /api/auth/login` with the correct email and password,
then the response is `200` with the session's expiry,
and a session cookie is set.

## IDNT-05: Wrong credentials are rejected
Given a registered account,
when they try to log in with the correct email but wrong password,
then the response is `401 Unauthorized`,
and no session cookie is set.

## IDNT-06: A signed-in user can fetch their own identity
Given a valid session (cookie, or bearer token in `Authorization`),
when they `GET /api/auth/me`,
then the response is `200` with their id and email.

## IDNT-07: An anonymous request to a protected route is rejected
Given no session cookie and no bearer token,
when a request is made to `GET /api/auth/me`,
then the response is `401 Unauthorized`.

## IDNT-08: Logging out revokes the session immediately
Given a signed-in session,
when they `POST /api/auth/logout`,
then the response is `204`,
and that same session token no longer authenticates `GET /api/auth/me`
(subsequent request gets `401`).

## IDNT-09: A signed-in user can export all their data
Given a signed-in user who owns rows in one or more owner-scoped auto-REST
collections,
when they `GET /api/auth/me/export`,
then the response is `200` with their profile (id, email, created_at) and
every row they own, grouped by collection name — one key per owner-scoped
collection, each holding that user's own rows and no one else's. A shared
(non-owner-scoped) table is not included, since its rows aren't
specifically theirs to export.

## IDNT-10: A signed-in user can delete their account
Given a signed-in user,
when they `DELETE /api/auth/me`,
then the response is `204`, and their session cookie is cleared.
Afterward: `POST /api/auth/login` with their old credentials fails
(`401`), their now-revoked session no longer authenticates anything, and
every row they owned in every owner-scoped auto-REST collection is gone.

## IDNT-11: Deleting an account never deletes another user's data
Given two different users, each owning rows in the same owner-scoped
collection,
when user A deletes their account,
then user B's account and rows in that collection are untouched.

## IDNT-12: Export and delete both require authentication
Given no session cookie and no bearer token,
when a request is made to `GET /api/auth/me/export` or `DELETE
/api/auth/me`,
then the response is `401 Unauthorized` in both cases.

## IDNT-13: Deleting an account revokes its outstanding OAuth grants
Given a user who authorized a third-party OAuth client and holds a live
access token issued on their behalf,
when they `DELETE /api/auth/me`,
then that access token no longer works against any protected resource
(a subsequent request using it gets `401`), even though it hasn't
naturally expired — deleting the account revokes every outstanding grant
issued to any client for that user, not just the identity-layer session.

## IDNT-14: Email matching is case-insensitive everywhere
Given an account signed up as `Jane.Doe@Example.com`,
when someone `POST`s `/api/auth/signup` with `jane.doe@example.com` (or
any other casing of the same address),
then the response is `409` (`email already taken`), not a second
account — signup uniqueness, login lookup, and password-reset-token
lookup all treat email as case-insensitive (normalized to lowercase +
trimmed before every write and lookup), and a case-insensitive unique
index backs this at the schema level too, independent of any single
code path remembering to normalize.
Given that same account,
when a social-login provider later reports `jane.doe@example.com` for
an identity that's never signed in here before (SOCIAL-02's "matching
email links to the existing account" case),
then it links to the existing account rather than silently creating an
unrelated new one — the exact-string mismatch a case-sensitive lookup
would produce is exactly what used to defeat this.

## IDNT-15: Account erasure cascades to linked social identities and outstanding reset tokens
Given a user with a social identity linked to their account
(`social_identities`, SOCIAL-09) and an outstanding, unredeemed
password-reset token (`password_reset_tokens`, `spec/password-reset.md`),
when they `DELETE /api/auth/me`,
then both rows are gone along with the account, via the same
`ON DELETE CASCADE` foreign key (`user_id REFERENCES users(id)`) IDNT-10
already relies on for owned auto-REST rows — not asserted anywhere
before this scenario, despite being one `PRAGMA foreign_keys` flip or a
migration typo away from silently regressing. Afterward: that same
provider identity completing the flow again is treated as never seen
before (SOCIAL-01), creating a brand-new account rather than erroring or
resurrecting the deleted one; and the old reset token no longer redeems
(`400`, the same rejection an already-invalid token gets) rather than
resetting a password on a deleted account or on whoever happens to reuse
that email later.

## IDNT-16: Concurrent duplicate signups for the same email only let one through
Given no account yet exists for a given email,
when several `POST /api/auth/signup` requests for that exact same email
arrive concurrently (not sequentially),
then exactly one succeeds (`201`) and every other one gets `409` — the
same "already taken" response a second, later signup gets normally.
`SignUp` relies on the `users.email` unique index rejecting every insert
but the first, rather than a check-then-insert (which a future
"optimization" could silently reintroduce a race into) — this holds even
when every request starts before any of them has committed.

## IDNT-17: A concurrent export during account erasure never corrupts data or 500s
Given a signed-in user with rows in an owner-scoped collection,
when `GET /api/auth/me/export` and `DELETE /api/auth/me` are in flight
at the same time for that same account,
then the delete always succeeds (`204`) regardless, and the concurrent
export either succeeds (`200`, with a snapshot of whichever collections
hadn't been erased yet by the time each one was read — anywhere from all
of them to none) or gets `401` (if the session was already revoked by
the time the export's own lookup ran) — never a `500`. Every row that
does appear in that snapshot is a complete, real record: `DeleteOwned`'s
per-collection deletes and `ExportOwned`'s per-collection reads are each
one atomic SQL statement, so a collection is never observed half-deleted
— only ever entirely-still-there or entirely-gone. `handleDeleteAccount`
is explicitly not wrapped in a transaction (application data, storage,
OAuth grants, then the account itself, as separate steps — see its own
doc comment), so a racing export genuinely can see partially-erased
state with nothing in the response distinguishing that from a complete,
uncontested one; that's accepted as-is, not fixed here — a real fix
means a transaction spanning auto-REST, storage, and OAuth grants
together, a bigger change than this gap's severity has warranted so
far.
