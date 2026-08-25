import { defineConfig } from 'vitest/config';

export default defineConfig({
  test: {
    // This SDK is fundamentally browser-shaped (DataClient's OAuth flow
    // navigates window.location and uses localStorage/sessionStorage) —
    // jsdom gives every test file those globals for free, rather than
    // needing a per-file environment pragma.
    environment: 'jsdom',
    environmentOptions: {
      jsdom: {
        // Tests assert against a fixed app origin (https://app.example.com)
        // for connect()/handleRedirectCallback()'s redirect handling —
        // window.history.replaceState() refuses to cross origins, so
        // jsdom's own document origin has to match rather than default
        // to localhost:3000.
        url: 'https://app.example.com/callback',
      },
    },
  },
});
