import type { TokenStore } from './tokenStore.js';
export interface DataClientOptions {
    baseUrl: string;
    fetchImpl: typeof fetch;
    tokenStore: TokenStore;
    /** Where the OAuth consent flow redirects back to. Defaults to the
     * current page (origin + pathname) in a browser; required if you're
     * not in one. */
    redirectUri?: string;
    /** Shown on maubase's own consent screen as the app requesting access. */
    clientName?: string;
    /** How connect() performs the actual navigation to /oauth/authorize.
     * Defaults to `window.location.assign` — override only if you have a
     * specific reason (a test, or an SPA router that wants to intercept
     * it); the destination is always a different origin than your app's,
     * so there's no way to turn this into an in-app route change. */
    navigate?: (url: string) => void;
}
/**
 * Fronts auto-REST (spec/auto-rest.md): `GET/POST/PATCH/DELETE
 * /api/data/{table}`. Every one of those routes requires a real OAuth
 * access token (`records:read`/`records:write` scope) — there is no
 * anonymous access, and the identity-layer session AuthClient uses
 * doesn't work here at all. That means using this class means driving
 * the OAuth authorization-code-with-PKCE flow, which is normally a
 * several-hundred-line undertaking; connect()/handleRedirectCallback()
 * below exist to make it two calls instead. What they can't hide: the
 * user still lands on maubase's own /oauth/authorize login-and-consent
 * pages for a moment — that's a real page navigation, not a modal your
 * app renders, and there's no way around it, PKCE consent is a genuine
 * redirect by design (spec/oauth-authorize-and-consent.md).
 */
export declare class DataClient {
    private opts;
    constructor(opts: DataClientOptions);
    /** True if there's a stored token (possibly expired-but-refreshable —
     * this doesn't make a network call, just checks local state). */
    isConnected(): boolean;
    /** Forgets the locally stored tokens. Does not revoke the standing
     * server-side consent grant — AUTHZ-05 means a later connect() call
     * will skip the consent screen and re-issue tokens silently, by
     * design. Revoking the grant itself isn't exposed by this SDK yet. */
    disconnect(): void;
    /**
     * Starts the OAuth flow: registers this app as a client on first use
     * (cached in localStorage afterward, so repeat calls don't accumulate
     * new oauth_clients rows server-side), builds a fresh PKCE challenge,
     * and navigates the browser to maubase's own /oauth/authorize page.
     * The user signs in and approves consent there — not in your UI — and
     * is sent back to `redirectUri` with `?code=&state=`. Call
     * handleRedirectCallback() on that page's load to finish.
     *
     * Requires a browser: this navigates window.location, and needs
     * sessionStorage to carry the PKCE verifier across that navigation.
     */
    connect(options?: {
        scopes?: string[];
    }): Promise<void>;
    /**
     * Call once, unconditionally, on the page load that follows connect()'s
     * redirect back. Returns false (a plain no-op) if the current URL
     * doesn't carry an authorize-flow callback at all, so it's always
     * safe to call on every page load rather than only conditionally.
     * On success, strips `code`/`state` from the visible URL and returns
     * true. Throws MaubaseError if the user denied consent, or if the
     * `state` doesn't match what connect() stored (a state mismatch is
     * treated as a possible CSRF attempt, not silently ignored).
     */
    handleRedirectCallback(): Promise<boolean>;
    /** A query builder scoped to one auto-REST collection — see
     * CollectionQuery below. `table` must already exist (created via a
     * migration, or the admin UI's create-table/SQL Studio); this doesn't
     * create anything. */
    from<T extends Record<string, unknown> = Record<string, unknown>>(table: string): CollectionQuery<T>;
    /** Resolves to a currently-valid access token, transparently refreshing
     * first if the stored one is expired (or close to it) and a refresh
     * token is available. CollectionQuery calls this before every request
     * — most apps never need to call it directly. */
    getAccessToken(): Promise<string>;
    private resolveRedirectUri;
    /** POST /oauth/register once per redirectUri, then reuse the returned
     * client_id from localStorage forever after — see spec/oauth-client-
     * registration.md REG-01/REG-02: a public ("none"-auth) client is
     * exactly what an app that can't hold a secret (this one) should
     * register as, with PKCE standing in for the secret. */
    private ensureClient;
    private exchangeCode;
    private exchangeRefreshToken;
}
export interface ListResult<T> {
    records: T[];
    limit: number;
    offset: number;
}
/**
 * A query builder for one auto-REST collection, returned by
 * DataClient.from(table). Every method attaches a fresh (auto-refreshed
 * if needed) `Authorization: Bearer` header — see spec/auto-rest.md for
 * the ownership/scoping rules these requests are subject to server-side
 * (a create always gets owner_id set to the token's own subject,
 * regardless of what's in the body, etc.).
 */
export declare class CollectionQuery<T extends Record<string, unknown> = Record<string, unknown>> {
    private table;
    private baseUrl;
    private fetchImpl;
    private getAccessToken;
    constructor(table: string, baseUrl: string, fetchImpl: typeof fetch, getAccessToken: () => Promise<string>);
    /** GET /api/data/{table} */
    select(options?: {
        limit?: number;
        offset?: number;
    }): Promise<ListResult<T>>;
    /** GET /api/data/{table}/{id} */
    get(id: string | number): Promise<T>;
    /** POST /api/data/{table}. Any `owner_id` in body is ignored server-
     * side on an owner-scoped table — it's always set to the token's own
     * subject (spec/auto-rest.md REST-OWNERSHIP-03). */
    insert(body: Partial<T>): Promise<T>;
    /** PATCH /api/data/{table}/{id}. The primary key and owner_id fields
     * are silently dropped from body if present — neither is editable via
     * this route. */
    update(id: string | number, body: Partial<T>): Promise<T>;
    /** DELETE /api/data/{table}/{id} */
    remove(id: string | number): Promise<void>;
    private request;
}
