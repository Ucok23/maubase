# Password reset

A customer who forgot their password gets a link, emailed to the address
on file, that lets them set a new one — without needing their old
password (that's the whole point) and without maubase ever revealing
whether that address even has an account.

This is the identity layer (`internal/auth`, same package `spec/
identity.md` covers), not the owner plane — an owner-plane account has no
password-reset path of its own yet; `spec/owner-plane.md`'s existing
account management (an `owner`-role account creating/deleting others)
is how that gets handled today.

Two endpoints:

- `POST /api/auth/forgot-password` — `{"email": "..."}`. Always `204`,
  regardless of whether the email is registered.
- `POST /api/auth/reset-password` — `{"token": "...", "password": "..."}`.
  Redeems a token exactly once.

Delivery is via `internal/email.Sender` — Resend
(`internal/email.ResendSender`) when `MAUBASE_RESEND_API_KEY` and
`MAUBASE_EMAIL_FROM` are both set, otherwise `NoopSender`, which fails
loudly the first time anything tries to send rather than silently
dropping the email. The link itself points at `MAUBASE_PASSWORD_RESET_URL`
(the *deployer's own frontend* page — maubase only issues and validates
the token, it doesn't render a page a human fills a new password into)
with the raw token appended as `?token=`.

## PWRESET-01: Requesting a reset for a real account emails a working link
Given a registered account,
when they `POST /api/auth/forgot-password` with their email,
then the response is `204`, and an email is sent to that address
containing a link built from `MAUBASE_PASSWORD_RESET_URL` with a `token`
query parameter — the same raw token `POST /api/auth/reset-password`
accepts.

## PWRESET-02: Requesting a reset for an unknown email looks identical
When someone `POST`s `/api/auth/forgot-password` with an email that
doesn't correspond to any account,
then the response is still `204`, indistinguishable from PWRESET-01's
response, and no email is sent — never revealing whether an address is
registered, the same posture `spec/owner-plane.md` OWNR-12 and `spec/
identity.md` IDNT-05 already take on the equivalent question for login.

## PWRESET-03: A valid token sets the new password and signs out every session
Given a reset token just issued for an account that has one or more
active sessions,
when they `POST /api/auth/reset-password` with that token and a new
password of at least 8 characters,
then the response is `204`, the account's password is changed (a
subsequent login with the old password fails, with the new one
succeeds), and every session that account had — including the one that
requested the reset, if any — stops authenticating anything immediately.
A password reset is exactly the moment you want anyone already signed in
(most importantly, whoever's access prompted the reset) signed out
everywhere.

## PWRESET-04: A weak new password is rejected
When `POST /api/auth/reset-password` is called with a password under 8
characters,
then the response is `400`, and the account's password is unchanged —
the same minimum `spec/identity.md` IDNT-03 enforces on signup.

## PWRESET-05: A token can only be redeemed once
Given a token that has already been used to successfully reset a
password,
when it's submitted to `/api/auth/reset-password` again,
then the response is `400`, and the (already-changed) password is
unaffected — replaying a captured or logged token must not work.

## PWRESET-06: An expired token is rejected
Given a token older than one hour,
when it's submitted to `/api/auth/reset-password`,
then the response is `400` and the password is unchanged.

## PWRESET-07: A garbage or unknown token is rejected
When `/api/auth/reset-password` is called with a token that was never
issued,
then the response is `400` — the same outcome as an expired or
already-used one, so a caller can't distinguish "wrong" from "expired"
from "already used" by response shape alone.

## PWRESET-08: Forgot-password is rate-limited like login
`POST /api/auth/forgot-password` shares the same per-IP throttle `POST
/api/auth/login` does (`spec/maintenance.md`'s login-rate-limit
configuration) — repeatedly requesting a reset for the same or different
addresses from one IP eventually gets `429`, the same protection against
outright email-bombing a victim that already exists against credential
stuffing.
