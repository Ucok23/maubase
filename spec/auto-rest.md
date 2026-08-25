# Auto-REST: tables become an API automatically

Every table a deployment creates via its own migrations (see
`internal/db.MigrateDir`) — not one of maubase's own internal tables — is
automatically queryable and writable at `/api/data/{table}`, with no
separate schema/config step. A table with an `owner_id` column is
automatically scoped per-user; one without is shared. See
`internal/restapi`'s package doc for the mechanism.

Every route requires an OAuth access token: `records:read` for `GET`,
`records:write` for `POST`/`PATCH`/`DELETE`. There is no anonymous access.

## REST-COL-01: Internal tables are never exposed, regardless of token
Given maubase's own tables (`users`, `oauth_clients`, `owner_users`, etc.),
when any request — with any token, any scope — is made to
`/api/data/{that table}`,
then the response is `404`, identical to a collection name that was never
a table at all. A valid `records:read`/`records:write` token never grants
access to the framework's own internal data.

## REST-CRUD-01: A record can be created and then fetched by id
Given an owner-scoped table (has an `owner_id` column),
when an authenticated customer with `records:write` `POST`s a JSON body
to `/api/data/{table}`,
then the response is `201` with the created record — including a
server-generated id (if the table's primary key is TEXT and none was
supplied) and an `owner_id` equal to the token's own subject, never
whatever the client sent, even if the client included one.
When they then `GET /api/data/{table}/{id}`,
then they get back that same record.

## REST-CRUD-02: Reading requires records:read; writing requires records:write
Given a token with only `records:read` granted,
when it's used for `POST`/`PATCH`/`DELETE`,
then the response is `401` (insufficient scope) — read access never
implies write access, and vice versa.

## REST-CRUD-03: Updating a record only changes the fields sent
Given an existing record,
when a `PATCH` body includes only some of its fields,
then only those fields change — the rest of the record is untouched,
and the response reflects the full, updated record.

## REST-CRUD-04: Deleting a record makes it 404 afterward
Given an existing record,
when it's `DELETE`d,
then the response is `204`,
and a subsequent `GET` for that same id is `404`.

## REST-OWNERSHIP-01: A user only ever sees their own rows in a list
Given two different customer users, each with a record in the same
owner-scoped table,
when user A lists `/api/data/{table}`,
then only user A's own record(s) appear — user B's never do, regardless
of how many records exist in total.

## REST-OWNERSHIP-02: A user can't read, update, or delete another user's record
Given user B owns a record,
when user A (a different, authenticated user) requests `GET`, `PATCH`, or
`DELETE` on that record's id,
then the response is `404` in every case — never `403`. The record's
existence isn't revealed to someone who doesn't own it; a real "doesn't
exist" and someone else's row look identical from the outside.

## REST-OWNERSHIP-03: owner_id can never be set or changed by the client
When a create body includes an `owner_id` field,
then it's ignored — the stored value is always the token's subject.
When an update body includes an `owner_id` field,
then it's ignored — the record's owner never changes via the API.

## REST-SHARED-01: A table without an owner_id column is fully shared
Given a table with no `owner_id` column,
when two different authenticated customers, each with the right scope,
read or write it,
then both see and can modify every row — there is no per-row filtering
on a shared table.

## REST-VALIDATION-01: An unknown field in the request body is rejected
When a create or update body includes a key that isn't a real column on
the table,
then the response is `400` — a typo'd field fails loudly rather than
being silently dropped.

## REST-VALIDATION-02: The primary key can't be changed via update
When an update body includes the primary key column,
then it's ignored — the row's id is exactly what it was before the
request, regardless of what value was sent.
