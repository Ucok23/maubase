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
