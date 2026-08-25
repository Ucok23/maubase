import { beforeEach, describe, expect, it, vi } from 'vitest';
import { AuthClient } from '../src/auth.js';
import { MaubaseError } from '../src/errors.js';

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  });
}

describe('AuthClient', () => {
  let fetchMock: ReturnType<typeof vi.fn>;
  let auth: AuthClient;

  beforeEach(() => {
    fetchMock = vi.fn();
    auth = new AuthClient('https://api.example.com', fetchMock as unknown as typeof fetch);
  });

  it('signUp posts to /api/auth/signup with credentials included and unwraps {user}', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(201, { user: { id: 'u1', email: 'a@example.com', created_at: '2026-01-01T00:00:00Z' } })
    );

    const user = await auth.signUp('a@example.com', 'correcthorse');

    expect(user).toEqual({ id: 'u1', email: 'a@example.com', created_at: '2026-01-01T00:00:00Z' });
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('https://api.example.com/api/auth/signup');
    expect(init.method).toBe('POST');
    expect(init.credentials).toBe('include');
    expect(JSON.parse(init.body)).toEqual({ email: 'a@example.com', password: 'correcthorse' });
  });

  it('signIn posts to /api/auth/login and returns expiresAt', async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { expires_at: '2026-02-01T00:00:00Z' }));

    const result = await auth.signIn('a@example.com', 'correcthorse');

    expect(result).toEqual({ expiresAt: '2026-02-01T00:00:00Z' });
    expect(fetchMock.mock.calls[0][0]).toBe('https://api.example.com/api/auth/login');
  });

  it('signOut posts to /api/auth/logout', async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));
    await auth.signOut();
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('https://api.example.com/api/auth/logout');
    expect(init.method).toBe('POST');
  });

  it('getUser GETs /api/auth/me, flat (not wrapped in {user})', async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { id: 'u1', email: 'a@example.com', created_at: '2026-01-01T00:00:00Z' }));

    const user = await auth.getUser();

    expect(user.email).toBe('a@example.com');
    expect(fetchMock.mock.calls[0][0]).toBe('https://api.example.com/api/auth/me');
  });

  it('deleteAccount DELETEs /api/auth/me', async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));
    await auth.deleteAccount();
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('https://api.example.com/api/auth/me');
    expect(init.method).toBe('DELETE');
  });

  it('throws a MaubaseError carrying the identity layer\'s {"error": "..."} message', async () => {
    fetchMock.mockResolvedValue(jsonResponse(401, { error: 'invalid email or password' }));

    await expect(auth.signIn('a@example.com', 'wrong')).rejects.toMatchObject({
      constructor: MaubaseError,
      message: 'invalid email or password',
      status: 401,
    });
  });
});
