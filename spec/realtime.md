# Realtime subscriptions

Every other surface is request/response HTTP: a client notices a change
only by polling. This is a WebSocket stream that instead pushes row
changes as they happen, layered on top of auto-REST's existing row-level
visibility rules (`owner_id`, and any `_policies` override — see
`spec/access-rules.md`) rather than introducing a separate authorization
model. See `internal/realtime` for the implementation, `test/realtime_test.go`
for the tests (`test/realtime_relay_test.go` for RT-09's cross-process
case).

## The model

`GET /api/realtime` upgrades to a WebSocket connection. Authentication is
the same OAuth access token auto-REST already requires, carried the usual
`Authorization: Bearer` way for non-browser clients, or as an
`?access_token=` query parameter for browser `WebSocket` clients that
can't set arbitrary handshake headers — the connection is rejected before
upgrading if neither is present or valid, so there's no window where an
unauthenticated socket is open.

Once connected, a client subscribes per collection by sending
`{"type":"subscribe","collection":"posts"}`; it can hold several
subscriptions at once on one connection, and drop one with
`{"type":"unsubscribe","collection":"posts"}` without affecting the
others. The server then pushes one JSON message per change:
`{"type":"created"|"updated"|"deleted","collection":"posts","record":{...}}`
(`deleted` carries just the row's id, not stale field data). A change is
pushed to a given subscriber only if that subscriber's token could `GET`
that row through the ordinary REST endpoint right now — the fan-out
lives at the same write path auto-REST's handlers already go through, so
it can't be bypassed by writing some other way (including maubase's own
account-erasure cascades).

There is no replay or backfill in v1: a subscription only ever sees
changes from the moment it was accepted onward. A client that needs
current state fetches it once via a normal `GET /api/data/{table}`
request before or after subscribing; the stream is for staying current
after that, not for initial sync.

Fan-out is in-process by default (`internal/realtime`'s package doc) —
every write already goes through this server's own auto-REST handlers,
so there's no database-level change feed to plug into or need one. That
covers a single server process, which matches this project's whole
design (`internal/db.Open` already pins `SetMaxOpenConns(1)` since
SQLite has one writer anyway) and is what most deployments run. Running
more than one app instance behind a load balancer — a real option once
horizontal scaling is the reason, since SQLite's own file locking
already serializes writes across as many processes as point at the same
file — needs `MAUBASE_REDIS_URL` set to a shared Redis instance
(`internal/realtime.RedisRelay`): a subscriber connected to one process
then also sees writes made on any other process sharing that Redis. See
RT-09.

## RT-01: Connecting requires the same scope GET would
Given a WebSocket handshake to `/api/realtime` with no access token, or
one without `records:read`,
then the connection is rejected (closed / handshake refused) before any
subscription is accepted — never silently upgraded and left unable to
subscribe.

## RT-02: A subscriber is notified when a matching row is created
Given a connection subscribed to a collection,
when a row is created in that collection via `POST /api/data/{table}`
(by any caller, including a different one from the subscriber) and the
subscriber's token could `GET` that new row,
then the subscriber receives a `created` message carrying the full
record, matching what that `GET` would have returned.

## RT-03: A subscriber is notified when a matching row is updated
Given a connection subscribed to a collection, and an existing row in it
visible to the subscriber,
when that row is updated via `PATCH`,
then the subscriber receives an `updated` message carrying the full,
post-update record — not just the changed fields.

## RT-04: A subscriber is notified when a matching row is deleted
Given a connection subscribed to a collection, and an existing row in it
visible to the subscriber,
when that row is deleted via `DELETE` (directly, or as part of account
erasure's cascading delete — see `spec/identity.md` IDNT-10),
then the subscriber receives a `deleted` message carrying that row's id.

## RT-05: A subscriber never receives events for rows it can't see
Given an owner-scoped table and two different users' connections, each
subscribed to it,
when user A creates, updates, or deletes one of their own rows,
then user B's connection receives no event at all for it — the same
row-level visibility `spec/auto-rest.md`'s REST-OWNERSHIP-01/02 enforce
for `GET` applies identically to what gets pushed.

## RT-06: A denied operation never produces events either
Given a table with a `_policies` rule of `denied` for some operation (see
`spec/access-rules.md` ACCESS-05/06/07),
then no subscriber ever receives an event for that operation on that
table — a `denied` write can't happen through the API to produce one, and
a `denied` read means no one is authorized to be notified of that
collection's changes at all.

## RT-07: Unsubscribing stops only that collection's events
Given a connection subscribed to two different collections,
when it sends `{"type":"unsubscribe","collection":"posts"}` for one of
them,
then it stops receiving events for `posts`, while events for the other,
still-subscribed collection continue arriving unaffected.

## RT-08: Closing the connection ends all of its subscriptions
Given a connection with one or more active subscriptions,
when the connection closes (client-initiated or otherwise),
then no further events are ever delivered to it — there's nothing to
explicitly unsubscribe from first.

## RT-09: A Redis relay delivers events across server processes
Given two separate maubase server processes, each configured with the
same `MAUBASE_REDIS_URL` (and so each running a `Broker` built via
`realtime.NewBrokerWithRelay`), and a subscriber connected to process B,
when a row is created, updated, or deleted via process A — the same as
RT-02/03/04, just against a different process's HTTP endpoint,
then process B's subscriber receives the event exactly as if the write
had happened on process B itself. Without `MAUBASE_REDIS_URL` set
(`Broker`'s default, `NewBroker`), this does not hold — see the note
above.

## RT-10: Account erasure's cascading delete reaches a live subscriber before its own token is revoked
Given a connection subscribed to an owner-scoped collection, authenticated
with an access token belonging to the row's owner, and a row that same
subject owns in it,
when that subject calls `DELETE /api/auth/me` (account erasure,
`spec/identity.md` IDNT-10) — which deletes application data first, then
revokes OAuth grants, then deletes the account itself (in that order;
see `handleDeleteAccount`) —
then the subscriber still receives the `deleted` event for that row,
exactly as RT-04 describes for an ordinary direct delete: the
already-open connection isn't affected by its own token being revoked a
moment later in the same request, since a WebSocket connection's token
is checked once at the handshake, never per message.
But when that same, now-revoked token is used to open a *new*
`/api/realtime` connection afterward,
then it's rejected exactly as RT-01 rejects any invalid token — the
original connection kept receiving events only because it predates the
revocation, not because the token still works.

## RT-11: The admin UI's data browser goes through the same fan-out as /api/data/*; SQL Studio doesn't
Given a subscriber watching a collection,
when a developer+ owner creates, edits, or deletes a row through
`/admin/ui/data/{collection}` (the data browser, `internal/restapi`'s
`AdminCreateRow`/`AdminUpdateRow`/`AdminDeleteRow`),
then that subscriber receives the same `created`/`updated`/`deleted`
event RT-02/03/04 describe for a customer-facing write — the "can't be
bypassed by writing some other way" invariant above applies to the
admin UI's own writes exactly as it does to account-erasure's cascading
delete, since a support engineer fixing a customer's row through the
admin UI is exactly the kind of "some other way" that invariant exists
for.
This does **not** extend to SQL Studio's raw `INSERT`/`UPDATE`/`DELETE`
(`/admin/ui/sql`): unlike the data browser, which always knows exactly
which collection and row a structured create/update/delete touched,
arbitrary SQL text would have to be parsed to determine that — multiple
tables in one statement, rows matched by an arbitrary `WHERE` clause,
no fixed row shape to attach to an event. This is a deliberate,
narrower carve-out than the data browser's, not an oversight: a SQL
Studio write is still fully audit-logged (ADMINUI-20) and still
reflected the next time a subscriber's client re-fetches, just never
pushed live.

## RT-12: A slow subscriber is isolated: it may miss events, but never slows down the writes that produced them
Given two connections subscribed to the same collection, one of which
stops reading its socket entirely while the other keeps reading
normally, and a burst of writes to that collection in quick succession,
then the reading connection receives every one of those events, while
the non-reading one may miss some once its 16-slot per-connection
buffer (`Broker.NewConn`) fills — the documented at-most-once/may-drop
behavior above, not a bug — but critically, none of the writes that
triggered those events are slowed down by the non-reading connection's
backlog: `Broker.publishLocal` holds its mutex only long enough to copy
out the current subscriber list, then sends to each with a non-blocking
`select`/`default`, so one slow subscriber's full channel (or, upstream
of that, an OS socket buffer it isn't draining) can never make a write
wait on it.

## RT-13: Subscribing to a collection leaves no trace behind once every subscriber is gone
Given a collection name with no subscribers left — either every
connection that had subscribed to it called `Unsubscribe`, or every
connection that had subscribed to it closed (`Broker.Close`) — for any
collection name at all, including one that was never a real table
(`readPump` passes `msg.Collection` straight through with no validation
against the actual schema, so this includes attacker-controlled,
one-off, bogus strings),
then `Broker.subs` has no entry for that name at all afterward, not an
empty-but-present map — `Unsubscribe` and `Close` both delete a
collection's own entry the moment its subscriber set becomes empty.
Before this, only individual connections were ever removed from a
collection's set, never the collection's entry itself once emptied: a
client (or attacker) cycling through many distinct, possibly bogus,
one-off collection names — subscribe, then disconnect, repeat — left
one dead, empty map entry behind per name, forever, growing `Broker`'s
memory unboundedly and keyed by strings the caller fully controls. Not
an acute DoS (each entry is tiny), but unbounded growth with no upper
bound is exactly the kind of thing that eventually becomes one.

## Known limitation: cross-process policy-change skew (multi-process only)

Each maubase process holds its own independently-`Discover()`'d
`Registry`, reloaded only by that same process's own `ReloadSchema`
call (triggered by that process's own admin UI — a SQL Studio
statement, a `_policies` edit, a new table). A gating decision
(`ownerID`, computed from a collection's *current* `ReadRule` on the
process that handles a given write) travels with the relayed event
over Redis and is trusted as-is by every other process's `Broker` — it
is never re-derived against the receiving process's own registry. That
means correctness here rests entirely on whichever process happens to
handle each write already having an up-to-date `ReadRule` for that
collection.

In a multi-process deployment (`MAUBASE_REDIS_URL` set), a `_policies`
change made through one process's admin UI takes effect on *that*
process immediately, but not on its siblings until each independently
reloads (their own next schema-changing action, or a restart) — there
is no cross-process invalidation of an in-memory `Registry` today. Until
a sibling reloads, a write it handles computes `ownerID` from its own
stale `ReadRule`: a policy that was just widened to `read: shared` on
one process still narrows delivery to the specific row-owner on a
stale sibling handling a write (under-sharing); a policy just narrowed
to `read: owner` still delivers to every subscriber on a stale sibling
(over-sharing, the more consequential direction). This is specific to
the exact topology `MAUBASE_REDIS_URL` exists for — a single process
never has this skew, since there's only one `Registry` to be stale
against.

**Mitigation today**: restart every process after a `_policies` change
in a multi-process deployment, the same way any other in-memory-cached
config change would need one. A cross-process invalidation broadcast
(publishing a "reload your registry" signal alongside relayed events)
would close this gap properly; tracked as future work, not implemented
in v1.
