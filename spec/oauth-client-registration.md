# OAuth: dynamic client registration (RFC 7591)

The point of this endpoint is that a third-party app — an MCP client,
above all — can start using this server as its "Sign in with..." provider
with zero manual setup: no admin creates a client_id for them ahead of
time.

## REG-01: A new app can register itself with no human involvement
Given a third-party app that has never talked to this server before,
when it `POST`s `/oauth/register` with at least one `redirect_uris` entry,
then the response is `201` with a `client_id`,
and, if it registered as a confidential client, a `client_secret` —
returned exactly this once; there is no way to retrieve it again later.

## REG-02: A public client (one that can't keep a secret) can register
Given an app that can't securely store a secret (native, CLI, or
browser-based),
when it registers with `token_endpoint_auth_method: "none"`,
then the response has no `client_secret`,
and that client can still complete the authorization code flow — PKCE
stands in for a client secret.

## REG-03: Registering without any redirect_uris is rejected
When an app `POST`s `/oauth/register` with an empty or missing
`redirect_uris`,
then the response is `400` with `error: "invalid_redirect_uri"`,
and no client is created.

## REG-04: Registering with an unsupported grant type is rejected
When an app requests a `grant_types` value this server doesn't support
(e.g. `"implicit"` or `"password"`),
then the response is `400` with `error: "invalid_client_metadata"`,
and no client is created.

## REG-05: Registering with an unknown scope is rejected
When an app's registration requests a `scope` value outside this server's
published scope vocabulary,
then the response is `400` with `error: "invalid_client_metadata"`,
and no client is created.

## REG-06: Omitted fields fall back to sane defaults
When an app registers with only `redirect_uris` set,
then it's granted `token_endpoint_auth_method: "client_secret_basic"`,
`grant_types: ["authorization_code"]`, and `response_types: ["code"]` by
default, per RFC 7591 — not rejected for omitting them.

## REG-07: This anonymous endpoint has bounded blast radius
Given the endpoint is unauthenticated by design (RFC 7591's whole point
— zero setup for a new client),
when an oversized request body, an excessive number of `redirect_uris`,
an overlong single `redirect_uri`, or an overlong `client_name` is sent,
then the response is `400` (or the body is rejected outright before
being fully read/decoded) rather than the server buffering an unbounded
payload or growing an unbounded `oauth_clients` row — the same
reject-before-fully-reading posture every other write endpoint in this
project takes. Repeated registration attempts from one IP are also
throttled, sharing the same per-IP budget login attempts do.

## REG-08: A malformed redirect_uri is rejected at registration, not discovered later
When a `redirect_uris` entry isn't a well-formed absolute URI (garbage
text, a relative path, or a URI carrying a fragment component — which
RFC 6749 §3.1.2 forbids for a redirection endpoint),
then the response is `400` with `error: "invalid_redirect_uri"`, and no
client is created — catching this at registration time, rather than
storing it verbatim and having it surface as a confusing failure later
at `/oauth/authorize`.
