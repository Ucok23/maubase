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

## Example: all four with curl

`$TOKEN` here was granted `records:read` only (see `spec/auto-rest.md`'s
walkthrough for how to get one):

```sh
BASE=http://localhost:8080

# RES-01/RES-03: the right scope works, the wrong one doesn't
curl -s -o /dev/null -w "status=%{http_code}\n" "$BASE/api/data/notes" \
  -H "Authorization: Bearer $TOKEN"                          # status=200
curl -s "$BASE/api/data/notes" -X POST -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"title":"x"}'
# {"error":"invalid or insufficient token"}   (401 — no records:write)

# RES-02: no header at all
curl -s "$BASE/api/data/notes"
# {"error":"missing bearer token"}   (401)

# RES-04: revoke it (RFC 7009 — token + the client_id it was issued to),
# then the exact same token that worked above no longer does
curl -s -o /dev/null -w "status=%{http_code}\n" -X POST "$BASE/oauth/revoke" \
  --data-urlencode "token=$TOKEN" --data-urlencode "client_id=$CLIENT_ID"
# status=200
curl -s "$BASE/api/data/notes" -H "Authorization: Bearer $TOKEN"
# {"error":"invalid or insufficient token"}   (401)
```
