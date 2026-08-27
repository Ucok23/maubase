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

## SOCIAL-07: Starting a social flow is rate-limited
`GET /api/auth/social/{provider}` shares the same per-IP throttle
`POST /api/auth/login` does. Unlike a brute-force login guess, each
attempt here does a real outbound HTTP exchange with the upstream
provider, so leaving this endpoint unthrottled would let an
unauthenticated caller generate unbounded outbound requests — an
SSRF-adjacent/DoS-amplification surface, and the kind of volume that
gets a deployment's own registered OAuth client flagged by the
provider. The callback endpoint is unaffected: it's reached only via a
redirect from the provider itself after a real user completed that
provider's own login, not something an attacker can drive at volume
directly.

## Starting a social flow while already signed in

Completing `google`/`github` with an existing identity-layer session
already active is a **link a second sign-in method to my account**
request, not an independent identity resolution — email-matching
(SOCIAL-02) never enters into it, since the currently-authenticated
session is what's authoritative here, not whatever a provider happens
to report.

## SOCIAL-09: A never-before-seen identity links to the current session's account
Given a signed-in customer session, and a provider identity that has
never completed this flow before (for *any* account),
when they complete the `google`/`github` flow while that session is
still active,
then no new account is created and no existing different account is
matched by email — the identity links to the *currently signed-in*
account directly, and the same session continues (the cookie is
unchanged, or reissued for the same account). Completing the same
flow again later, now signed out, signs back into that same account
(SOCIAL-03) — the second sign-in method is now real.

## SOCIAL-10: An identity already linked elsewhere is refused, not silently swapped
Given a signed-in customer session for account A, and a provider
identity already linked to a *different* account B (from a previous
SOCIAL-01/02/09 flow),
when they complete that provider's flow while signed in as A,
then the request is rejected (`409`) and account A's session is left
completely unchanged — session B is never silently substituted in.
Before this, the session cookie was unconditionally overwritten with
whatever account the identity resolved to, so a curious click while
signed in could switch the browser to an unrelated account with no
warning at all.
