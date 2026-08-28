# Cross-cutting scenarios

Most of this project's specs describe one feature in isolation, each
with its own `testserver.Options` fields to turn it on
(`internal/testserver`'s package doc). That's deliberate — it keeps each
feature's own test suite focused — but it also means no single test has
ever stood up a server that looks like a plausible real deployment,
where several of these optional features are active at once and have to
coexist correctly. This file is for scenarios that specifically span
more than one other spec file's territory.

## XFEAT-01: Several optional features work correctly when combined on one deployment
Given a single server configured with more than two of `testserver.Options`'
optional features active simultaneously — a Redis-backed realtime relay
(`Relay`), a fake social login provider (`SocialProviders`), a
`_policies` override on an application table, real file storage, a
captured password-reset email (`EmailSender`), and a bootstrapped owner
account, all at once, the way a real production deployment combining
Redis-backed realtime + social login + a custom access policy + file
uploads plausibly would —
when a customer signs in via the social provider, obtains an OAuth
access token, creates a row in the `_policies`-governed table, uploads a
file, and a second customer requests a password reset, while a realtime
subscriber (via the Relay-backed broker) and the bootstrapped owner
(via the admin UI) both watch,
then every feature behaves exactly as its own spec says it should with
nothing else active: the realtime subscriber receives the row's
`created` event over the Relay-backed broker (`spec/realtime.md`), the
`_policies` override is honored for both the creator and a
separately-scoped witness token (`spec/access-rules.md`), the upload
succeeds and is retrievable (`spec/storage.md`), the reset email is
captured by the fake sender with a valid link (`spec/password-reset.md`),
the social-login account exists and is visible from `/api/auth/me`
(`spec/social-login.md`), and the owner's admin UI session sees the same
row through the data browser (`spec/admin-ui.md`) — no feature's
behavior changes, breaks, or leaks into another's just because several
are wired into the same process at once.

Found by: cross-cutting audit (GAP-8). See `test/cross_cutting_test.go`.
