# OAuth: authorizing a third-party app

This is the "Sign in with example.com" flow: a human, already a user of
this server, is asked whether some other app may act on their behalf.

## AUTHZ-01: An anonymous visitor is asked to sign in first
Given a registered client and a valid authorize request,
when a browser with no session hits `GET /oauth/authorize`,
then it's shown a sign-in form (not a redirect, not an error),
and no authorization code is issued yet.

## AUTHZ-02: A first-time authorization asks for explicit consent
Given a user who has never authorized this particular client before,
when they're signed in and the authorize flow continues,
then they see a consent screen listing exactly the scopes the client
requested,
and no authorization code is issued yet — issuing one requires an
explicit decision.

## AUTHZ-03: Approving consent redirects back with a code
Given a user on the consent screen,
when they approve,
then they're redirected to the client's `redirect_uri` carrying an
authorization `code` and the original `state` value unchanged,
and this scope grant is remembered, so the same client asking for the
same (or a narrower) scope later won't need to ask again.

## AUTHZ-04: Denying consent redirects back with an error, not a code
Given a user on the consent screen,
when they deny,
then they're redirected to the client's `redirect_uri` with an error
parameter and no `code`.

## AUTHZ-05: A returning, already-consented user skips the consent screen
Given a user who previously approved a client for a set of scopes,
when they go through `/oauth/authorize` again requesting the same or a
smaller set of scopes,
then they're redirected straight back to the client with a fresh code —
no consent screen shown, no extra click.

## AUTHZ-06: PKCE is mandatory for every client
Given any client — public or confidential — starting the authorization
code flow,
when its authorize request omits `code_challenge`,
then no authorization code is ever issued for it, even if the user signs
in and approves consent — approving redirects back with an error instead
of a code. This server requires PKCE unconditionally, not only for public
clients, per OAuth 2.1 and the MCP authorization spec.
(The consent screen may still be shown before this is caught — scope/
consent and PKCE are validated at different points in the flow — but the
flow can never complete to an issued code.)

## AUTHZ-07: A weak or missing `state` is rejected up front
When a client's authorize request has no `state` parameter, or one
shorter than 8 characters,
then the request is rejected before any user interaction (redirected
back with `error=invalid_state`), since a short state is too easy to
guess and defeats its purpose as a CSRF token.

## AUTHZ-08: An unregistered redirect_uri is rejected before any redirect
Given a client registered with only `https://legit.example/cb`,
when an authorize request supplies a different `redirect_uri` (e.g.
`https://evil.example/cb`),
then the request is rejected with an error page served directly (no
redirect to either URI — redirecting to an unregistered URI is exactly
the open-redirect / authorization-code-theft this check exists to
prevent), even for a signed-in user who would otherwise sail through
consent.
