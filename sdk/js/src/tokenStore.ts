// Where DataClient (dataClient.ts) persists the OAuth tokens it gets back
// from the token endpoint. Deliberately separate from AuthClient's
// session, which has no token to store at all — that's just the
// browser's own cookie jar (see auth.ts's doc comment).
export interface StoredTokens {
  accessToken: string;
  refreshToken?: string;
  /** Epoch milliseconds. */
  expiresAt: number;
  scope: string;
  /** The OAuth client_id these tokens were issued to — refreshing needs
   * it, so it travels with the tokens rather than needing a separate,
   * reload-surviving cache of its own. */
  clientId: string;
}

export interface TokenStore {
  get(): StoredTokens | null;
  set(tokens: StoredTokens): void;
  clear(): void;
}

/** Tokens live only as long as this object does — gone on page reload or
 * process restart. The fallback for any environment without
 * localStorage; also handy in tests. */
export class MemoryTokenStore implements TokenStore {
  private tokens: StoredTokens | null = null;
  get(): StoredTokens | null {
    return this.tokens;
  }
  set(tokens: StoredTokens): void {
    this.tokens = tokens;
  }
  clear(): void {
    this.tokens = null;
  }
}

export class LocalStorageTokenStore implements TokenStore {
  constructor(private key: string) {}

  get(): StoredTokens | null {
    const raw = window.localStorage.getItem(this.key);
    if (!raw) return null;
    try {
      return JSON.parse(raw) as StoredTokens;
    } catch {
      return null;
    }
  }

  set(tokens: StoredTokens): void {
    window.localStorage.setItem(this.key, JSON.stringify(tokens));
  }

  clear(): void {
    window.localStorage.removeItem(this.key);
  }
}

/** localStorage-backed in a browser; an in-memory store (tokens don't
 * survive a reload) anywhere else.
 *
 * Deliberately checks `window.localStorage` rather than the bare
 * `localStorage` global: recent Node versions (22+) define that global
 * unconditionally but leave it non-functional without a
 * `--localstorage-file` flag, so `typeof localStorage` alone can no
 * longer tell a real browser apart from plain Node. */
export function defaultTokenStore(key: string): TokenStore {
  if (typeof window !== 'undefined' && window.localStorage) {
    return new LocalStorageTokenStore(key);
  }
  return new MemoryTokenStore();
}
