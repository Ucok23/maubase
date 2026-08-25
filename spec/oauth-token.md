# OAuth: exchanging a code for tokens

## TOK-01: A valid code with the matching PKCE verifier yields a token
Given an authorization code just issued to a client, tied to a
`code_challenge` from that authorize request,
when the client `POST`s `/oauth/token` with the matching `code_verifier`,
then the response is `200` with an `access_token`, the scopes actually
granted, and an expiry.

## TOK-02: The wrong PKCE verifier is rejected
When the client `POST`s `/oauth/token` with a `code_verifier` that does
not match the original `code_challenge`,
then the response is `400` with `error: "invalid_grant"`,
and no token is issued.

## TOK-03: A code can only be redeemed once
Given a code that has already been successfully exchanged for a token,
when it's submitted to `/oauth/token` a second time,
then the response is `400` (`invalid_grant`) and no new token is issued —
replaying a captured code must not work.

## TOK-04: A refresh token is issued only when `offline_access` was granted
Given a code exchange where the user granted the `offline_access` scope,
then the token response includes a `refresh_token`.
Given a code exchange where `offline_access` was not requested or not
granted,
then the token response has no `refresh_token` — a client shouldn't get
long-lived offline access it never asked for or the user never approved.

## TOK-05: A refresh token is rotated on use
Given a previously issued `refresh_token`,
when the client `POST`s `grant_type=refresh_token` with it,
then it receives a new `access_token` and a new `refresh_token`,
and the old `refresh_token` no longer works — a stolen-but-already-used
refresh token can't be replayed.
