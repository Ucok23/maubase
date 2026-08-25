# Specs

These describe expected behavior from the point of view of whoever is
outside the server — a browser, an app, a script, an MCP client. They are
written before, and independently of, how the code happens to be
implemented: a spec describes what should be true; a test in `test/`
verifies the running server actually does that.

**Rule: never derive a spec from reading the implementation.** If a scenario
here turns out not to match the code, that's either a bug to fix or a
behavior change to negotiate — not a reason to edit the spec to match
what the code does. Specs only change when the intended behavior changes.

Each scenario has a stable ID (e.g. `IDNT-04`). Tests reference the ID in a
comment so a failing test points back to the exact sentence it's checking,
and so a spec change makes it easy to find what needs re-verifying.

- [identity.md](identity.md) — sign up, sign in, sessions (customer plane)
- [owner-plane.md](owner-plane.md) — the team running this deployment (owner plane)
- [auto-rest.md](auto-rest.md) — tables become an API automatically
- [storage.md](storage.md) — file upload/download
- [oauth-client-registration.md](oauth-client-registration.md) — RFC 7591 dynamic client registration
- [oauth-authorize-and-consent.md](oauth-authorize-and-consent.md) — the "Sign in with example.com" flow
- [oauth-token.md](oauth-token.md) — exchanging a code for tokens
- [oauth-resource-access.md](oauth-resource-access.md) — using an access token
- [oauth-discovery.md](oauth-discovery.md) — well-known endpoints
- [maintenance.md](maintenance.md) — session cleanup and login rate-limiting
- [access-rules.md](access-rules.md) — row-level access rules beyond `owner_id` (design spec, not yet built)
- [realtime.md](realtime.md) — realtime subscriptions (design spec, not yet built)
