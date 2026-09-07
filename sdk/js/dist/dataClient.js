import { MaubaseError, throwIfNotOK } from './errors.js';
import { generateCodeVerifier, generateState, challengeFromVerifier } from './pkce.js';
const DEFAULT_SCOPES = ['records:read', 'records:write', 'offline_access'];
// A 10s cushion before an access token's real expiry, so a request that
// starts just before expiry doesn't land at the server just after it.
const EXPIRY_SKEW_MS = 10_000;
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
export class DataClient {
    opts;
    constructor(opts) {
        this.opts = opts;
    }
    /** True if there's a stored token (possibly expired-but-refreshable —
     * this doesn't make a network call, just checks local state). */
    isConnected() {
        return this.opts.tokenStore.get() != null;
    }
    /** Forgets the locally stored tokens. Does not revoke the standing
     * server-side consent grant — AUTHZ-05 means a later connect() call
     * will skip the consent screen and re-issue tokens silently, by
     * design. Revoking the grant itself isn't exposed by this SDK yet. */
    disconnect() {
        this.opts.tokenStore.clear();
    }
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
    async connect(options) {
        requireBrowser('connect()');
        const scopes = options?.scopes ?? DEFAULT_SCOPES;
        const redirectUri = this.resolveRedirectUri();
        const client = await this.ensureClient(redirectUri, scopes);
        const verifier = generateCodeVerifier();
        const state = generateState();
        const pending = { verifier, state, clientId: client.clientId, redirectUri };
        window.sessionStorage.setItem(pendingKey(this.opts.baseUrl), JSON.stringify(pending));
        const challenge = await challengeFromVerifier(verifier);
        const url = new URL('/oauth/authorize', this.opts.baseUrl);
        url.searchParams.set('response_type', 'code');
        url.searchParams.set('client_id', client.clientId);
        url.searchParams.set('redirect_uri', redirectUri);
        url.searchParams.set('scope', scopes.join(' '));
        url.searchParams.set('state', state);
        url.searchParams.set('code_challenge', challenge);
        url.searchParams.set('code_challenge_method', 'S256');
        const navigate = this.opts.navigate ?? ((target) => window.location.assign(target));
        navigate(url.toString());
    }
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
    async handleRedirectCallback() {
        requireBrowser('handleRedirectCallback()');
        const params = new URLSearchParams(window.location.search);
        const code = params.get('code');
        const state = params.get('state');
        const error = params.get('error');
        if (!code && !error)
            return false;
        const key = pendingKey(this.opts.baseUrl);
        const raw = window.sessionStorage.getItem(key);
        window.sessionStorage.removeItem(key);
        stripAuthParamsFromUrl();
        if (!raw) {
            throw new MaubaseError('received an OAuth redirect but had no pending authorization for it (connect() must run in the same browser tab first)', 0);
        }
        const pending = JSON.parse(raw);
        if (error) {
            throw new MaubaseError(params.get('error_description') ?? `authorization failed: ${error}`, 0, error);
        }
        if (state !== pending.state) {
            throw new MaubaseError('state parameter did not match — aborting (possible CSRF)', 0);
        }
        const tokens = await this.exchangeCode(code, pending);
        this.opts.tokenStore.set(tokens);
        return true;
    }
    /** A query builder scoped to one auto-REST collection — see
     * CollectionQuery below. `table` must already exist (created via a
     * migration, or the admin UI's create-table/SQL Studio); this doesn't
     * create anything. */
    from(table) {
        return new CollectionQuery(table, this.opts.baseUrl, this.opts.fetchImpl, () => this.getAccessToken());
    }
    /** Resolves to a currently-valid access token, transparently refreshing
     * first if the stored one is expired (or close to it) and a refresh
     * token is available. CollectionQuery calls this before every request
     * — most apps never need to call it directly. */
    async getAccessToken() {
        const stored = this.opts.tokenStore.get();
        if (!stored) {
            throw new MaubaseError('not connected — call connect() (and handleRedirectCallback() on the page it returns to) first', 0);
        }
        if (Date.now() < stored.expiresAt - EXPIRY_SKEW_MS) {
            return stored.accessToken;
        }
        if (!stored.refreshToken) {
            throw new MaubaseError('access token expired and no refresh token was granted — call connect() again', 0);
        }
        const refreshed = await this.exchangeRefreshToken(stored);
        this.opts.tokenStore.set(refreshed);
        return refreshed.accessToken;
    }
    // --- internals -----------------------------------------------------------
    resolveRedirectUri() {
        if (this.opts.redirectUri)
            return this.opts.redirectUri;
        return window.location.origin + window.location.pathname;
    }
    /** POST /oauth/register once per redirectUri, then reuse the returned
     * client_id from localStorage forever after — see spec/oauth-client-
     * registration.md REG-01/REG-02: a public ("none"-auth) client is
     * exactly what an app that can't hold a secret (this one) should
     * register as, with PKCE standing in for the secret. */
    async ensureClient(redirectUri, scopes) {
        const key = clientKey(this.opts.baseUrl, redirectUri);
        // window.localStorage, not bare localStorage: recent Node versions
        // define that global unconditionally but leave it non-functional
        // without a --localstorage-file flag (see defaultTokenStore's doc
        // comment in tokenStore.ts for the same issue).
        if (typeof window !== 'undefined' && window.localStorage) {
            const raw = window.localStorage.getItem(key);
            if (raw) {
                try {
                    return JSON.parse(raw);
                }
                catch {
                    // fall through and re-register
                }
            }
        }
        const res = await this.opts.fetchImpl(`${this.opts.baseUrl}/oauth/register`, {
            method: 'POST',
            headers: { 'content-type': 'application/json' },
            body: JSON.stringify({
                redirect_uris: [redirectUri],
                token_endpoint_auth_method: 'none',
                grant_types: ['authorization_code', 'refresh_token'],
                client_name: this.opts.clientName,
                scope: scopes.join(' '),
            }),
        });
        await throwIfNotOK(res);
        const body = (await res.json());
        const registered = { clientId: body.client_id, redirectUri };
        if (typeof window !== 'undefined' && window.localStorage) {
            window.localStorage.setItem(key, JSON.stringify(registered));
        }
        return registered;
    }
    async exchangeCode(code, pending) {
        const res = await this.opts.fetchImpl(`${this.opts.baseUrl}/oauth/token`, {
            method: 'POST',
            headers: { 'content-type': 'application/x-www-form-urlencoded' },
            body: new URLSearchParams({
                grant_type: 'authorization_code',
                code,
                redirect_uri: pending.redirectUri,
                client_id: pending.clientId,
                code_verifier: pending.verifier,
            }),
        });
        await throwIfNotOK(res);
        const tok = (await res.json());
        return toStoredTokens(tok, pending.clientId);
    }
    async exchangeRefreshToken(stored) {
        const res = await this.opts.fetchImpl(`${this.opts.baseUrl}/oauth/token`, {
            method: 'POST',
            headers: { 'content-type': 'application/x-www-form-urlencoded' },
            body: new URLSearchParams({
                grant_type: 'refresh_token',
                refresh_token: stored.refreshToken,
                client_id: stored.clientId,
            }),
        });
        await throwIfNotOK(res);
        const tok = (await res.json());
        // TOK-05: the old refresh token is rotated out and stops working the
        // moment a new one is issued — always keep whatever came back, and
        // only fall back to the old one on the off chance a response omits
        // it (offline_access wasn't part of the original grant).
        return toStoredTokens(tok, stored.clientId, stored.refreshToken);
    }
}
function toStoredTokens(tok, clientId, fallbackRefreshToken) {
    return {
        accessToken: tok.access_token,
        refreshToken: tok.refresh_token ?? fallbackRefreshToken,
        expiresAt: Date.now() + tok.expires_in * 1000,
        scope: tok.scope,
        clientId,
    };
}
function requireBrowser(what) {
    if (typeof window === 'undefined') {
        throw new MaubaseError(`DataClient.${what} requires a browser environment (it navigates window.location and/or reads it)`, 0);
    }
}
function clientKey(baseUrl, redirectUri) {
    return `maubase.oauth.client.${baseUrl}.${redirectUri}`;
}
function pendingKey(baseUrl) {
    return `maubase.oauth.pending.${baseUrl}`;
}
function stripAuthParamsFromUrl() {
    const url = new URL(window.location.href);
    for (const p of ['code', 'state', 'error', 'error_description'])
        url.searchParams.delete(p);
    window.history.replaceState(window.history.state, '', url.toString());
}
/**
 * A query builder for one auto-REST collection, returned by
 * DataClient.from(table). Every method attaches a fresh (auto-refreshed
 * if needed) `Authorization: Bearer` header — see spec/auto-rest.md for
 * the ownership/scoping rules these requests are subject to server-side
 * (a create always gets owner_id set to the token's own subject,
 * regardless of what's in the body, etc.).
 */
export class CollectionQuery {
    table;
    baseUrl;
    fetchImpl;
    getAccessToken;
    constructor(table, baseUrl, fetchImpl, getAccessToken) {
        this.table = table;
        this.baseUrl = baseUrl;
        this.fetchImpl = fetchImpl;
        this.getAccessToken = getAccessToken;
    }
    /** GET /api/data/{table} */
    async select(options) {
        const params = new URLSearchParams();
        if (options?.limit != null)
            params.set('limit', String(options.limit));
        if (options?.offset != null)
            params.set('offset', String(options.offset));
        const qs = params.toString();
        return this.request(qs ? `?${qs}` : '');
    }
    /** GET /api/data/{table}/{id} */
    async get(id) {
        return this.request(`/${encodeURIComponent(id)}`);
    }
    /** POST /api/data/{table}. Any `owner_id` in body is ignored server-
     * side on an owner-scoped table — it's always set to the token's own
     * subject (spec/auto-rest.md REST-OWNERSHIP-03). */
    async insert(body) {
        return this.request('', { method: 'POST', body: JSON.stringify(body) });
    }
    /** PATCH /api/data/{table}/{id}. The primary key and owner_id fields
     * are silently dropped from body if present — neither is editable via
     * this route. */
    async update(id, body) {
        return this.request(`/${encodeURIComponent(id)}`, { method: 'PATCH', body: JSON.stringify(body) });
    }
    /** DELETE /api/data/{table}/{id} */
    async remove(id) {
        await this.request(`/${encodeURIComponent(id)}`, { method: 'DELETE' });
    }
    async request(path, init) {
        const token = await this.getAccessToken();
        const headers = { authorization: `Bearer ${token}` };
        if (init?.body)
            headers['content-type'] = 'application/json';
        const res = await this.fetchImpl(`${this.baseUrl}/api/data/${this.table}${path}`, { ...init, headers });
        await throwIfNotOK(res);
        if (res.status === 204)
            return undefined;
        return (await res.json());
    }
}
//# sourceMappingURL=dataClient.js.map