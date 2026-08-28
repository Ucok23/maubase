# File storage

Upload/download endpoints outside auto-REST (`/api/data/*`), since raw
bytes and multipart uploads need handling generic JSON CRUD doesn't do.
Every file belongs to the caller that uploaded it — the same row-level
ownership auto-REST's `owner_id` convention gives a table — and is gated
by OAuth access-token scopes (`files:read` / `files:write`), same as
`/api/data/*`'s `records:read` / `records:write`.

## STOR-01: An authorized caller can upload a file
Given an access token with the `files:write` scope,
when they `POST /api/storage/files` with a `multipart/form-data` body
whose `file` field carries the bytes,
then the response is `201` with the created file's id, filename, content
type, and size.

## STOR-02: A caller can list their own uploaded files
Given an access token with the `files:read` scope that has uploaded one
or more files,
when they `GET /api/storage/files`,
then the response is `200` with metadata for exactly the files that
token's subject uploaded, paginated via `?limit=&offset=` the same way
`/api/data/{table}` is.

## STOR-03: A caller can fetch a file's metadata and content
Given a file uploaded by an access token's subject,
when they `GET /api/storage/files/{id}` (metadata) or `GET
/api/storage/files/{id}/content` (bytes),
then the response is `200`; the content response's body is byte-for-byte
what was uploaded, with the original content type and filename echoed via
`Content-Type` and `Content-Disposition`.

## STOR-04: A caller can delete their own file
Given a file uploaded by an access token's subject,
when they `DELETE /api/storage/files/{id}`,
then the response is `204`, and afterward both `GET
/api/storage/files/{id}` and its `/content` route return `404`.

## STOR-05: A file is invisible to every caller but its uploader
Given two different access tokens (different subjects), one of which
uploaded a file,
when the other token requests that file's metadata, content, or deletes
it,
then every one of those requests gets `404` — indistinguishable from the
file not existing at all — and the file is untouched.

## STOR-06: Every storage route requires the matching scope
Given a request to any `/api/storage/files*` route,
when it carries no access token, or one without the scope that route
requires (`files:read` for `GET`, `files:write` for `POST`/`DELETE`),
then the response is `401`.

## STOR-07: Account export includes uploaded file metadata
Given a signed-in user who has uploaded one or more files,
when they `GET /api/auth/me/export` (see spec/identity.md IDNT-09),
then the response's `files` key lists metadata for every file they
uploaded — not the raw bytes, which are downloaded individually via
`GET /api/storage/files/{id}/content`.

## STOR-08: Account deletion erases uploaded files
Given a signed-in user who has uploaded one or more files,
when they `DELETE /api/auth/me` (see spec/identity.md IDNT-10),
then every file they uploaded is gone: an access token that could
previously read it now gets `404` from both its metadata and content
routes.

## STOR-13: A partial failure mid-erasure leaves consistent, retryable state
Given a user with 3 uploaded files, where deleting the 2nd file's bytes
fails (a permissions error, or bytes already gone in a way that isn't
tolerated as "already deleted"),
when their account (or otherwise-triggered erasure) processes these in
upload order,
then the 1st file — processed before the failure — is fully erased,
both bytes and metadata; the 2nd file's metadata is left in place
(still discoverable via `GET /api/storage/files/{id}`, its bytes still
intact via `.../content`) rather than orphaned pointing at bytes that
silently vanished; and the 3rd file, never reached, is completely
untouched. A retry once the transient failure clears converges to fully
erased — nothing is left permanently orphaned.
