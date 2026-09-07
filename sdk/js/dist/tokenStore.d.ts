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
export declare class MemoryTokenStore implements TokenStore {
    private tokens;
    get(): StoredTokens | null;
    set(tokens: StoredTokens): void;
    clear(): void;
}
export declare class LocalStorageTokenStore implements TokenStore {
    private key;
    constructor(key: string);
    get(): StoredTokens | null;
    set(tokens: StoredTokens): void;
    clear(): void;
}
/** localStorage-backed in a browser; an in-memory store (tokens don't
 * survive a reload) anywhere else.
 *
 * Deliberately checks `window.localStorage` rather than the bare
 * `localStorage` global: recent Node versions (22+) define that global
 * unconditionally but leave it non-functional without a
 * `--localstorage-file` flag, so `typeof localStorage` alone can no
 * longer tell a real browser apart from plain Node. */
export declare function defaultTokenStore(key: string): TokenStore;
