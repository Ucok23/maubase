# Realtime subscriptions

**Design spec — not yet implemented.** Every existing surface is
request/response HTTP: a client notices a change only by polling. This
spec describes a WebSocket stream that instead pushes row changes as they
happen, layered on top of auto-REST's existing row-level visibility rules
(`owner_id`, and any `_policies` override — see `spec/access-rules.md`)
rather than introducing a separate authorization model. Written first,
per this repo's spec-first convention (`spec/README.md`).

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
