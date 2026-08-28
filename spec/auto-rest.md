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

## REST-OWNERSHIP-04: owner_id must have TEXT affinity
Given a deployment migration that declares `owner_id` with any type
other than one that gives it SQLite TEXT affinity (e.g. `owner_id
INTEGER`, `owner_id NUMERIC`, `owner_id REAL`, or `owner_id` given a
bare/blob-affinity type) — `Discover` only checked the column *name*
against `spec/auto-rest.md`'s `owner_id` convention, never its declared
type,
when the server starts and discovers that table,
then startup fails with an error naming the table and its declared
type, the same "loud failure over a silent misconfiguration" treatment
`spec/access-rules.md` ACCESS-08 already gives an `owner` policy on a
table with no `owner_id` column at all. A non-TEXT affinity is a real
data-isolation risk, not just a style nit: every subject this codebase
issues is a UUID (`uuid.NewString()`), which never looks like a
canonical numeric literal, so it always survives storage as TEXT — but
a numeric affinity column (INTEGER, REAL, or the NUMERIC catch-all)
coerces any value that *does* look numeric before storing or comparing
it, so differently-formatted subjects that coerce to the same number
(`"7"`, `"07"`, and `"7.0"` all become the stored integer `7`) would
end up scoped to each other's rows — every caller filtering by any one
of those three subject strings would see all three owners' rows,
indistinguishable from one being genuinely shared. `owner_id TEXT NOT
NULL` (what every migration and `AdminCreateTable`-generated table in
this codebase already uses) has no such coercion and is unaffected.

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

## REST-VALIDATION-03: An oversized request body is rejected before being fully read
When a create or update request body exceeds the server's configured
size limit (`MAUBASE_MAX_REQUEST_BODY_KB`),
then the response is `413`, and the body is never fully buffered/decoded
into memory first — an authenticated `records:write` caller can't force
unbounded memory use by sending an arbitrarily large payload.

## REST-VALIDATION-04: A constraint violation is a 400/409, never a 500
When a create or update body would violate a `NOT NULL`, `CHECK`, or
foreign-key constraint on the table (an omitted or explicit-`null`
required field with no default, a value outside a `CHECK`'d range, a
reference to a row that doesn't exist), or would duplicate a `UNIQUE`
value,
then the response names the actual problem — `400` for `NOT NULL`/
`CHECK`/foreign-key (`409` for `UNIQUE`, unchanged from before) — rather
than a generic `500` indistinguishable from an actual internal fault.
This applies to both create and update; update previously had no
constraint handling at all and always 500'd.

## REST-VALIDATION-05: A non-scalar field value is rejected before it reaches the database
When a create or update body's value for a real column is a JSON array
or object rather than a string, number, boolean, or `null`,
then the response is `400` naming that field — no SQLite column type
can ever bind a nested structure, so this is caught before the query is
even built rather than surfacing as an opaque `500` from the database
driver.

## REST-PAGINATION-01: An out-of-range or invalid limit/offset is rejected, not silently substituted
Given `GET /api/data/{table}`,
when `limit`/`offset` are both omitted,
then the response uses the documented defaults (`limit=50`) exactly as
before — this is the common case, and omitting them is not an error.
But when `limit` or `offset` *is* given and is invalid — non-numeric,
zero or negative for `limit`, negative for `offset`, or a `limit` over
the stated maximum of 200 —
then the response is `400` naming the problem (e.g. `limit must not
exceed 200, got 500`), rather than silently substituting the default as
if the parameter had been omitted. A caller explicitly asking for
`limit=999999` used to silently get the *default* 50 back with nothing
in the response distinguishing that from "there are only 50 rows total"
— easy to mistake a truncated page for a complete result, e.g. when
building an export that assumes it saw everything. `PATCH`/`DELETE`
aren't paginated and are unaffected; this applies to every `GET
/api/data/{table}` list request.
