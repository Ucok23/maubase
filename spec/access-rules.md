# Row-level access rules

`internal/restapi`'s baseline authorization rule is applied identically
to all four operations (read/create/update/delete): a table with an
`owner_id` column is scoped to whoever created each row (see
`spec/auto-rest.md`'s REST-OWNERSHIP-*), a table without one is fully
shared (REST-SHARED-01). This spec describes a second, optional mechanism
that sits on top of that default, letting a deployment override specific
operations per collection. See `internal/restapi/registry.go`'s
`applyPolicies` for the implementation, `test/access_rules_test.go` for
the tests.

## The model

A deployment can declare a **policy** per collection per operation,
overriding that operation's default for that table. Policies are rows in
a new reserved table, `_policies` (never exposed via auto-REST, same as
`files` — see `spec/storage.md`), with one row per (collection,
operation) pair a deployment wants to override. `operation` is one of
`read` (covers both list and get-by-id), `create`, `update`, `delete`.
`rule` is a closed vocabulary, matching this project's existing preference
for fixed vocabularies over free-form config (OAuth scopes, owner roles):

- `owner` — only the row's `owner_id` may act on it. Requires the table
  to have an `owner_id` column; this is also the default for owner-scoped
  tables when no policy overrides it.
- `shared` — any caller with the operation's scope, unfiltered. Default
  for tables with no `owner_id` column.
- `denied` — that operation is turned off for that collection through
  this API entirely, regardless of caller or scope. For data a deployment
  wants readable/writable only by its own backend logic, never directly
  by an end-user token.

`create` is the one operation where `owner` and `shared` behave
identically: there's no existing row to scope a brand-new one against,
so either rule lets any caller with the write scope create one (still
owned by their own subject — see ACCESS-04). `denied` is the only rule
that changes `create`'s behavior.

An operation with no matching `_policies` row keeps today's default
behavior exactly — this is additive, not a breaking change to
`spec/auto-rest.md`'s existing scenarios.

## ACCESS-01: No declared policy preserves today's default behavior
Given a table with no `_policies` rows for it,
when any operation is performed,
then behavior is exactly `spec/auto-rest.md`'s existing rules: `owner`
for an owner-scoped table, `shared` for one without `owner_id`.

## ACCESS-02: Policies are declared per operation independently
Given a table with a `read: shared` policy and no policy for
create/update/delete,
when a caller reads it,
then every row is visible to any caller with `records:read`, regardless
of `owner_id` — but create/update/delete still follow that table's
default (`owner`, if it has an `owner_id` column), unaffected by the read
policy. Declaring one operation's policy never implicitly changes another
operation's behavior.

## ACCESS-03: Public read, owner-only write
Given an owner-scoped table with `read: shared` (and no override for the
write operations, so they stay at the `owner` default),
when any authenticated caller with `records:read` lists or gets rows,
then they see every row, including ones they don't own,
but when they `PATCH` or `DELETE` a row they don't own,
then the response is still `404` (REST-OWNERSHIP-02 continues to apply
to non-`shared` operations) — read visibility never implies write access.

## ACCESS-04: A shared-write table still auto-stamps ownership on create
Given an owner-scoped table with `create: shared` declared,
when any authenticated caller with `records:write` creates a row,
then the response is `201` and the created row's `owner_id` is still the
creating caller's own subject (REST-OWNERSHIP-03 continues to apply) — a
`shared` create policy widens *who* may create, not what the resulting
row's ownership is.

## ACCESS-05: A denied operation is refused for every caller
Given a table with `delete: denied`,
when any caller — any token, any scope, even the row's own owner —
attempts `DELETE` on a row in it,
then the response is `403`, distinct from the `404` an ownership
violation or an unknown collection gets: the operation exists and the
caller may well be authorized in general, but this specific action has
been turned off for this collection at the API layer entirely.

## ACCESS-06: A denied read makes the collection invisible via that verb
Given a table with `read: denied`,
when any caller attempts `GET /api/data/{table}` or
`GET /api/data/{table}/{id}`,
then the response is `403` — distinct from `404` (REST-COL-01's response
for a collection that isn't a table at all, or a `GET` a caller merely
lacks scope for, which stays `401`).

## ACCESS-07: A shared table can still deny specific operations
Given a table with no `owner_id` column (default `shared` for every
operation) and a `create: denied` policy declared on it,
when any caller attempts to `POST` a new row,
then the response is `403`, even though `GET` on the same table remains
`shared` and unaffected — read-only reference data a deployment seeds
through its own migrations, never through the API, is the intended use.

## ACCESS-08: An owner rule on a table without owner_id is rejected at startup
Given a table with no `owner_id` column,
when a deployment declares an `owner` policy for any operation on it,
then the server refuses to start (or, if hot-reloadable in a later
version, refuses to apply the policy) with an error naming the table and
missing column — an `owner` rule with no `owner_id` to scope by is a
deployment configuration error, not a runtime 500 waiting to happen.

## ACCESS-09: Realtime events on a shared-write, owner-read table are gated by the row's owner, not the writer
Given an owner-scoped table with `update: shared` (or `delete: shared`)
declared and `read` left at its `owner` default — "anyone with write
scope can change any row, but each caller only reads their own" — and two
authenticated subjects A and B, where a row belongs to B,
when A updates or deletes that row,
then the realtime `updated`/`deleted` event reaches a subscriber
authenticated as B (the row's actual owner, per spec/realtime.md
RT-03/RT-04's "only that subject" guarantee), even though B didn't make
the write,
and the event does **not** reach a subscriber authenticated as A (the
writer) merely because A happened to be subscribed — A could never `GET`
this row (spec/realtime.md RT-05), so seeing its content over the
subscription would leak it just the same as an API response would.
