# @maubase/client

A TypeScript client for a maubase backend: identity (signup/login/session)
and auto-REST data access — with the OAuth PKCE dance auto-REST requires
handled for you.

Lives in this repo (`sdk/js/`) rather than a separate one, at least for
now — see the conversation that started this if you're wondering why;
short version: it's a thin wrapper over this backend's own HTTP surface,
so a breaking API change and the SDK code that needs to follow it belong
in the same PR while this project is small and single-maintainer. Nothing
about the split is permanent.

## Two different things, two different auth models

This client has two independent parts, because the backend does:

- **`client.auth`** — signup, login, session, your own profile
  (`spec/identity.md`). Cookie-based: sign in once, the browser's cookie
  jar does the rest. No token to manage.
- **`client.data`** — reading/writing your own app's tables
  (`spec/auto-rest.md`). Every route requires a real **OAuth access
  token** — there's no anonymous access, and `client.auth`'s session
  doesn't work here at all. That's a deliberate backend design choice,
  not an SDK limitation: `/api/data/*` is meant to be usable by
  third-party apps too, so it's gated the same way regardless of who's
  calling.

That means using `client.data` means your user goes through maubase's own
`/oauth/authorize` login-and-consent page at least once — a real page
navigation, not a modal your app controls. `client.data.connect()` and
`client.data.handleRedirectCallback()` exist to make that two function
calls instead of implementing RFC 6749 + PKCE by hand.

## Install

Not published yet — for now, build it locally:

```sh
cd sdk/js
npm install
npm run build
```

## Usage

```ts
import { createClient } from '@maubase/client';

const client = createClient('https://api.yourapp.com');

// --- client.auth: cookie-based, no setup ------------------------------
await client.auth.signUp('you@example.com', 'correcthorse');
const me = await client.auth.getUser();
await client.auth.signOut();

// --- client.data: needs a one-time connect() ---------------------------
// On whatever page/button starts this:
if (!client.data.isConnected()) {
  await client.data.connect(); // navigates to maubase's consent page
}

// On the page connect() redirects back to (can be the same page —
// redirectUri defaults to the current page):
await client.data.handleRedirectCallback(); // no-op if there's nothing to handle

// Once connected, query like you'd expect:
const { records } = await client.data.from('posts').select({ limit: 20 });
const post = await client.data.from('posts').insert({ title: 'Hello' });
await client.data.from('posts').update(post.id, { title: 'Hello, edited' });
await client.data.from('posts').remove(post.id);
```

A minimal React sketch of the connect/callback dance:

```tsx
function ConnectDataButton({ client }: { client: MaubaseClient }) {
  useEffect(() => {
    client.data.handleRedirectCallback().catch(console.error);
  }, []);

  if (client.data.isConnected()) return null;
  return <button onClick={() => client.data.connect()}>Connect your data</button>;
}
```

## What's not here yet

- **Storage** (`files:read`/`files:write` scopes exist server-side; no
  wrapper here yet).
- **Realtime** subscriptions (`spec/realtime.md`'s WebSocket broker).
- Revoking a granted OAuth consent from the SDK side (the server supports
  it; nothing here calls it yet). `client.data.disconnect()` only forgets
  local tokens — the standing consent grant remains, so a later
  `connect()` skips the consent screen (by design, per
  `spec/oauth-authorize-and-consent.md` AUTHZ-05).
- A non-browser (Node/server-side) path for `client.data` — the OAuth
  flow it drives is inherently a redirect-based, human-in-the-loop thing,
  and the backend doesn't support a machine-to-machine grant type
  (`client_credentials`) at all, so there's currently no "customer" this
  half could serve outside a browser.

## Development

```sh
npm run build        # tsc -> dist/
npm test             # vitest, mocked fetch + jsdom
npm run test:watch
```

Tests run with `NODE_OPTIONS=--no-experimental-webstorage` (baked into
the npm scripts) — recent Node versions define a native `localStorage`
global unconditionally but leave it non-functional without a
`--localstorage-file` flag, which otherwise shadows jsdom's own working
one in a way that's hard to tell apart from "no browser APIs available at
all." See `tokenStore.ts`'s `defaultTokenStore` doc comment for how the
SDK itself works around the same issue at runtime (checking
`window.localStorage` explicitly, never the bare global).
