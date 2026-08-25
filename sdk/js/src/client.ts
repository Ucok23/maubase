import { AuthClient } from './auth.js';
import { DataClient } from './dataClient.js';
import { defaultTokenStore, type TokenStore } from './tokenStore.js';

export interface CreateClientOptions {
  /** Overrides the global fetch — mainly for a non-browser runtime
   * without one, or for tests. */
  fetch?: typeof fetch;
  /** Where DataClient's OAuth flow redirects back to; defaults to the
   * current page in a browser. See DataClient.connect(). */
  redirectUri?: string;
  /** Shown on maubase's own consent screen as the app requesting access. */
  clientName?: string;
  /** Where DataClient persists its OAuth tokens; defaults to
   * localStorage in a browser, or an in-memory store (lost on reload)
   * anywhere else. */
  tokenStore?: TokenStore;
}

/**
 * One maubase deployment, as seen by a customer-facing app: `.auth` for
 * signup/login/session (spec/identity.md, cookie-based — no setup
 * needed), `.data` for auto-REST (spec/auto-rest.md, OAuth-gated — see
 * DataClient's own doc comment for what that actually requires).
 */
export class MaubaseClient {
  readonly auth: AuthClient;
  readonly data: DataClient;

  constructor(url: string, options: CreateClientOptions = {}) {
    const baseUrl = url.replace(/\/+$/, '');
    const fetchImpl = options.fetch ?? globalThis.fetch;
    if (!fetchImpl) {
      throw new Error(
        'no fetch implementation available in this environment — pass one explicitly via createClient(url, { fetch })'
      );
    }
    this.auth = new AuthClient(baseUrl, fetchImpl);
    this.data = new DataClient({
      baseUrl,
      fetchImpl,
      tokenStore: options.tokenStore ?? defaultTokenStore(`maubase.tokens.${baseUrl}`),
      redirectUri: options.redirectUri,
      clientName: options.clientName,
    });
  }
}

/** `createClient('https://api.example.com')` — see MaubaseClient. */
export function createClient(url: string, options?: CreateClientOptions): MaubaseClient {
  return new MaubaseClient(url, options);
}
