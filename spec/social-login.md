# Social login: "Continue with Google" / "Continue with GitHub"

An alternative to email+password sign-up for the identity layer
(`internal/auth`, same accounts `spec/identity.md` covers) — the customer
authenticates with a third-party provider instead, and maubase creates or
signs into a local account on their behalf. This is `internal/social`,
maubase acting as an OAuth *client* to Google/GitHub — the opposite
direction from `internal/oauth`, which is maubase acting as an OAuth
*authorization server* for third-party apps.

Two endpoints, both browser-navigated (not JSON APIs — the first is a
redirect, the second only ever runs as the tail end of one):

- `GET /api/auth/social/{provider}` — starts the flow: redirects to that
  provider's own authorization page.
- `GET /api/auth/social/{provider}/callback` — where the provider sends
  the browser back to, with a `code`. On success, sets the identity-layer
  session cookie (same one `POST /api/auth/login` sets) and redirects to
  `MAUBASE_SOCIAL_LOGIN_REDIRECT_URL` — the deployer's own frontend page;
  maubase doesn't render one of its own.

`provider` is `google` or `github`. Each requires its own client
id/secret (`MAUBASE_GOOGLE_CLIENT_ID`/`_SECRET`,
`MAUBASE_GITHUB_CLIENT_ID`/`_SECRET`) from that provider's own developer
console, with the redirect URI registered there set to
`{MAUBASE_ISSUER}/api/auth/social/{provider}/callback`.

## SOCIAL-01: A first-time sign-in with a new provider identity creates an account
Given a person who has never signed into this deployment before, by any
method,
when they complete the `google` or `github` flow,
then a new account is created — email taken from the provider's own
profile when it supplied one — and they're signed in immediately (the
session cookie is set on the callback's redirect).

## SOCIAL-02: A provider identity whose email matches an existing account links to it
Given an account that already exists (however it originally signed
up — password or a different provider) with email `a@example.com`,
when a provider identity nobody has used before completes the flow and
that provider reports `a@example.com` as the person's email,
then no second account is created — the new identity is linked to the
existing one, and the session that gets signed in is for that same
account.

## SOCIAL-03: A returning provider identity signs in without re-linking
Given a provider identity that has completed the flow before (SOCIAL-01
or SOCIAL-02 already ran for it once),
when it completes the flow again,
then it signs into the same account as before directly — no new account,
no re-checking email, whether or not that email has since changed on the
provider's side.

## SOCIAL-04: A state mismatch is rejected, not silently ignored
Given a completed `GET /api/auth/social/{provider}` (which sets a
short-lived state cookie),
when `/callback` is hit with a `state` query parameter that doesn't match
that cookie's value — or no cookie at all,
then the response is `400`, no code is exchanged with the provider, and
no session is created. This is the flow's CSRF defense: an attacker who
tricks a victim into visiting a callback URL they crafted themselves
can't complete a login as anyone.

## SOCIAL-05: An unconfigured or unknown provider name 404s
Given a deployment that has only set `MAUBASE_GOOGLE_CLIENT_ID`/`_SECRET`
(or neither),
when `GET /api/auth/social/github` (or any name that isn't `google` or
`github` at all) is requested,
then the response is `404` — identical either way, so a caller can't
distinguish "this provider doesn't exist" from "this deployment just
hasn't configured it."

## SOCIAL-06: A provider that doesn't return an email in its main profile still resolves one
Given a GitHub account whose public profile has no email set (the
common case — GitHub only includes it there when the account has made
one public),
when it completes the `github` flow,
then maubase falls back to that account's primary, verified email (a
second call GitHub's own API requires for this), and account
creation/linking (SOCIAL-01/02) proceeds using that address, not an
empty one.
