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

## Example: discovering everything with two GETs

```sh
curl -s http://localhost:8080/.well-known/oauth-authorization-server
```
```json
{
  "issuer": "http://localhost:8080",
  "authorization_endpoint": "http://localhost:8080/oauth/authorize",
  "token_endpoint": "http://localhost:8080/oauth/token",
  "registration_endpoint": "http://localhost:8080/oauth/register",
  "revocation_endpoint": "http://localhost:8080/oauth/revoke",
  "jwks_uri": "http://localhost:8080/.well-known/jwks.json",
  "scopes_supported": ["profile", "records:read", "records:write",
                        "files:read", "files:write", "offline_access"],
  "response_types_supported": ["code"],
  "grant_types_supported": ["authorization_code", "refresh_token"],
  "token_endpoint_auth_methods_supported": ["none", "client_secret_basic", "client_secret_post"],
  "code_challenge_methods_supported": ["S256"]
}
```

```sh
curl -s http://localhost:8080/.well-known/jwks.json
```
```json
{
  "keys": [
    {
      "use": "sig", "kty": "RSA", "alg": "RS256",
      "kid": "95042402-0240-4b1e-b805-6a0d046ae12c",
      "n": "p5XJlPDVgSkdLpc4ColzV_QXWPhkxSBAUFVfjy8-...", "e": "AQAB"
    }
  ]
}
```
