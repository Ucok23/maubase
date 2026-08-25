# OAuth: discovery

The point of these two endpoints is that neither a human nor a client
implementer should ever need to hardcode this server's OAuth endpoints —
they're discoverable from one well-known URL each.

## DISC-01: A client can find every endpoint from one well-known URL
When a client `GET`s `/.well-known/oauth-authorization-server`,
then it receives (at minimum) `issuer`, `authorization_endpoint`,
`token_endpoint`, `registration_endpoint`, `revocation_endpoint`,
`jwks_uri`, the supported scopes, grant types, and PKCE methods —
everything needed to drive the whole flow without prior configuration.

## DISC-02: A resource server can verify tokens without calling back here
When a resource server `GET`s `/.well-known/jwks.json`,
then it receives the public signing key(s) currently in use, each keyed
by `kid`, sufficient to verify an access token's signature locally —
without needing network access back to this server on every request.
