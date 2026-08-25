import { beforeEach, describe, expect, it, vi } from 'vitest';
import { DataClient } from '../src/dataClient.js';
import { MemoryTokenStore } from '../src/tokenStore.js';
import { MaubaseError } from '../src/errors.js';

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });
}

// jsdom locks window.location down completely — the property itself is
// non-configurable (a getter/setter pair), and so is Location's own
// .assign — so neither vi.spyOn nor a plain assignment can intercept a
// real navigation call. DataClientOptions.navigate exists largely for
// this: inject a mock instead of fighting jsdom for something a real
// browser would never let a test control anyway.
function makeClient(
  fetchMock: ReturnType<typeof vi.fn>,
  tokenStore = new MemoryTokenStore(),
  navigate: ReturnType<typeof vi.fn> = vi.fn()
) {
  return new DataClient({
    baseUrl: 'https://api.example.com',
    fetchImpl: fetchMock as unknown as typeof fetch,
    tokenStore,
    redirectUri: 'https://app.example.com/callback',
    clientName: 'Test App',
    navigate,
  });
}

beforeEach(() => {
  window.localStorage.clear();
  window.sessionStorage.clear();
  window.history.replaceState(null, '', 'https://app.example.com/callback');
});

describe('DataClient.connect', () => {
  it('registers a public PKCE client once, then reuses the cached client_id', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(201, { client_id: 'client-123' }));
    const navigate = vi.fn();
    const client = makeClient(fetchMock, undefined, navigate);

    await client.connect();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('https://api.example.com/oauth/register');
    const body = JSON.parse(init.body);
    expect(body).toMatchObject({
      redirect_uris: ['https://app.example.com/callback'],
      token_endpoint_auth_method: 'none',
      grant_types: ['authorization_code', 'refresh_token'],
      client_name: 'Test App',
    });

    // Second connect(): no second registration call.
    await client.connect();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(navigate).toHaveBeenCalledTimes(2);
  });

  it('navigates to /oauth/authorize with a valid PKCE challenge and the default scopes', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(201, { client_id: 'client-123' }));
    const navigate = vi.fn();
    const client = makeClient(fetchMock, undefined, navigate);

    await client.connect();

    const target = new URL(navigate.mock.calls[0][0] as string);
    expect(target.pathname).toBe('/oauth/authorize');
    expect(target.searchParams.get('response_type')).toBe('code');
    expect(target.searchParams.get('client_id')).toBe('client-123');
    expect(target.searchParams.get('redirect_uri')).toBe('https://app.example.com/callback');
    expect(target.searchParams.get('scope')).toBe('records:read records:write offline_access');
    expect(target.searchParams.get('code_challenge_method')).toBe('S256');
    expect(target.searchParams.get('code_challenge')).toBeTruthy();
    expect(target.searchParams.get('state')?.length).toBeGreaterThan(8);

    // The verifier/state must be recoverable by handleRedirectCallback
    // after the "navigation" — i.e. actually persisted, not just held
    // in memory.
    const pending = window.sessionStorage.getItem('maubase.oauth.pending.https://api.example.com');
    expect(pending).toBeTruthy();
  });

  it('honors a custom scopes list', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(201, { client_id: 'client-123' }));
    const navigate = vi.fn();
    const client = makeClient(fetchMock, undefined, navigate);

    await client.connect({ scopes: ['profile'] });

    const target = new URL(navigate.mock.calls[0][0] as string);
    expect(target.searchParams.get('scope')).toBe('profile');
  });
});

describe('DataClient.handleRedirectCallback', () => {
  it('is a no-op returning false when the URL carries no OAuth callback', async () => {
    const fetchMock = vi.fn();
    const client = makeClient(fetchMock);
    await expect(client.handleRedirectCallback()).resolves.toBe(false);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('exchanges the code, stores tokens, and strips code/state from the URL', async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(jsonResponse(201, { client_id: 'client-123' }));
    const navigate = vi.fn((url: string) => {
      // Simulate the browser actually landing on that URL for real, so
      // handleRedirectCallback has something to read.
      window.history.replaceState(null, '', url.replace('https://api.example.com', 'https://app.example.com'));
    });
    const tokenStore = new MemoryTokenStore();
    const client = makeClient(fetchMock, tokenStore, navigate);
    await client.connect();

    // Simulate the actual redirect back from maubase with a code+state.
    const state = new URL(window.location.href).searchParams.get('state');
    window.history.replaceState(null, '', `https://app.example.com/callback?code=abc123&state=${state}`);

    fetchMock.mockResolvedValueOnce(
      jsonResponse(200, { access_token: 'at-1', refresh_token: 'rt-1', expires_in: 3600, scope: 'records:read records:write offline_access' })
    );

    const ok = await client.handleRedirectCallback();

    expect(ok).toBe(true);
    const [url, init] = fetchMock.mock.calls[1];
    expect(url).toBe('https://api.example.com/oauth/token');
    const body = new URLSearchParams(init.body as string);
    expect(body.get('grant_type')).toBe('authorization_code');
    expect(body.get('code')).toBe('abc123');
    expect(body.get('client_id')).toBe('client-123');
    expect(body.get('redirect_uri')).toBe('https://app.example.com/callback');
    expect(body.get('code_verifier')).toBeTruthy();

    expect(tokenStore.get()).toMatchObject({ accessToken: 'at-1', refreshToken: 'rt-1', clientId: 'client-123' });
    expect(window.location.search).toBe(''); // code/state stripped
  });

  it('throws on a state mismatch instead of exchanging the code', async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(jsonResponse(201, { client_id: 'client-123' }));
    const client = makeClient(fetchMock);
    await client.connect();

    window.history.replaceState(null, '', 'https://app.example.com/callback?code=abc123&state=not-the-real-state');

    await expect(client.handleRedirectCallback()).rejects.toThrow(/state/i);
    expect(fetchMock).toHaveBeenCalledTimes(1); // only the registration call — never reached /oauth/token
  });

  it('throws with the OAuth error when the user denied consent', async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(jsonResponse(201, { client_id: 'client-123' }));
    const client = makeClient(fetchMock);
    await client.connect();

    window.history.replaceState(null, '', 'https://app.example.com/callback?error=access_denied&error_description=user+said+no');

    await expect(client.handleRedirectCallback()).rejects.toMatchObject({
      constructor: MaubaseError,
      message: 'user said no',
      code: 'access_denied',
    });
  });
});

describe('DataClient.getAccessToken', () => {
  it('returns the stored access token without a network call while it is still valid', async () => {
    const fetchMock = vi.fn();
    const tokenStore = new MemoryTokenStore();
    tokenStore.set({ accessToken: 'still-good', expiresAt: Date.now() + 60_000, scope: 'records:read', clientId: 'c1' });
    const client = makeClient(fetchMock, tokenStore);

    await expect(client.getAccessToken()).resolves.toBe('still-good');
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('transparently refreshes an expired token and persists the rotated refresh token', async () => {
    const tokenStore = new MemoryTokenStore();
    tokenStore.set({
      accessToken: 'expired',
      refreshToken: 'old-refresh',
      expiresAt: Date.now() - 1000,
      scope: 'records:read',
      clientId: 'c1',
    });
    const fetchMock = vi
      .fn()
      .mockResolvedValue(jsonResponse(200, { access_token: 'fresh', refresh_token: 'new-refresh', expires_in: 3600, scope: 'records:read' }));
    const client = makeClient(fetchMock, tokenStore);

    const token = await client.getAccessToken();

    expect(token).toBe('fresh');
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('https://api.example.com/oauth/token');
    const body = new URLSearchParams(init.body as string);
    expect(body.get('grant_type')).toBe('refresh_token');
    expect(body.get('refresh_token')).toBe('old-refresh');
    expect(body.get('client_id')).toBe('c1');
    expect(tokenStore.get()).toMatchObject({ accessToken: 'fresh', refreshToken: 'new-refresh' });
  });

  it('throws instead of silently returning nothing when never connected', async () => {
    const client = makeClient(vi.fn());
    await expect(client.getAccessToken()).rejects.toThrow(/not connected/i);
  });

  it('throws when expired with no refresh token, rather than looping forever', async () => {
    const tokenStore = new MemoryTokenStore();
    tokenStore.set({ accessToken: 'expired', expiresAt: Date.now() - 1000, scope: 'records:read', clientId: 'c1' });
    const client = makeClient(vi.fn(), tokenStore);
    await expect(client.getAccessToken()).rejects.toThrow(/no refresh token/i);
  });
});

describe('DataClient.from (CollectionQuery)', () => {
  function connectedClient(fetchMock: ReturnType<typeof vi.fn>) {
    const tokenStore = new MemoryTokenStore();
    tokenStore.set({ accessToken: 'at-1', expiresAt: Date.now() + 60_000, scope: 'records:read records:write', clientId: 'c1' });
    return makeClient(fetchMock, tokenStore);
  }

  it('select() GETs the collection with a bearer token and optional pagination', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, { records: [{ id: '1' }], limit: 50, offset: 0 }));
    const client = connectedClient(fetchMock);

    const result = await client.from('posts').select({ limit: 10, offset: 20 });

    expect(result.records).toEqual([{ id: '1' }]);
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('https://api.example.com/api/data/posts?limit=10&offset=20');
    expect(init.headers.authorization).toBe('Bearer at-1');
  });

  it('get(id) GETs a single record', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, { id: '1', title: 'hi' }));
    const client = connectedClient(fetchMock);
    const record = await client.from('posts').get('1');
    expect(record).toEqual({ id: '1', title: 'hi' });
    expect(fetchMock.mock.calls[0][0]).toBe('https://api.example.com/api/data/posts/1');
  });

  it('insert() POSTs a JSON body', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(201, { id: '1', title: 'hi' }));
    const client = connectedClient(fetchMock);
    await client.from('posts').insert({ title: 'hi' });
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('https://api.example.com/api/data/posts');
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body)).toEqual({ title: 'hi' });
  });

  it('update(id) PATCHes a JSON body', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, { id: '1', title: 'updated' }));
    const client = connectedClient(fetchMock);
    await client.from('posts').update('1', { title: 'updated' });
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('https://api.example.com/api/data/posts/1');
    expect(init.method).toBe('PATCH');
  });

  it('remove(id) DELETEs and resolves with no value on 204', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
    const client = connectedClient(fetchMock);
    await expect(client.from('posts').remove('1')).resolves.toBeUndefined();
    expect(fetchMock.mock.calls[0][1].method).toBe('DELETE');
  });

  it('surfaces a 404 as a MaubaseError instead of throwing a JSON-parse error', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(404, { error: 'not found' }));
    const client = connectedClient(fetchMock);
    await expect(client.from('posts').get('missing')).rejects.toMatchObject({ status: 404, message: 'not found' });
  });
});
