# OAuth: using an access token against a protected resource

## RES-01: A valid bearer token with the required scope is accepted
Given an access token that was granted the `profile` scope,
when a request carrying `Authorization: Bearer <token>` is made to a
resource that requires `profile`,
then the request succeeds,
and the resource can identify which user it's acting on behalf of (the
token's subject).

## RES-02: A missing bearer token is rejected
When a request to a protected resource carries no `Authorization` header,
then the response is `401`.

## RES-03: A token lacking the required scope is rejected
Given an access token that was only granted `records:read`,
when it's used against a resource that requires `records:write`,
then the request is rejected (not silently allowed with reduced access).

## RES-04: A revoked token stops working immediately
Given a valid access token,
when its underlying grant is revoked via `POST /oauth/revoke`,
then subsequent requests using that same access token are rejected, even
though the token itself hasn't expired yet.
