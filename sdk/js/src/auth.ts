import { throwIfNotOK } from './errors.js';

// Field names match internal/server/handlers_auth.go's userJSON exactly
// (id, email, created_at — no updated_at: GET /api/auth/me doesn't
// return it, only the admin UI's user-detail page does).
export interface MaubaseUser {
  id: string;
  email: string;
  created_at: string;
}

/** GET /api/auth/me/export's shape — spec/identity.md IDNT-09. records
 * is keyed by collection name; a shared (non-owner-scoped) table is
 * never included, since its rows aren't specifically the caller's. */
export interface ExportedAccount {
  profile: MaubaseUser;
  records: Record<string, unknown[]>;
  files: unknown[];
}

/**
 * The identity layer (spec/identity.md): signup, login, logout, the
 * signed-in user's own profile, and the two GDPR-shaped data-subject
 * actions (export, delete). Every call sends `credentials: 'include'`,
 * so session persistence is just the browser's own cookie jar — there's
 * no token to manage here, unlike DataClient (dataClient.ts), which
 * fronts a completely different, OAuth-gated surface.
 */
export class AuthClient {
  constructor(
    private baseUrl: string,
    private fetchImpl: typeof fetch
  ) {}

  /** POST /api/auth/signup. Per IDNT-01 this also signs the caller in —
   * the response sets the session cookie, so there's no separate signIn()
   * call needed right after. */
  async signUp(email: string, password: string): Promise<MaubaseUser> {
    const res = await this.fetchImpl(`${this.baseUrl}/api/auth/signup`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      credentials: 'include',
      body: JSON.stringify({ email, password }),
    });
    await throwIfNotOK(res);
    const body = (await res.json()) as { user: MaubaseUser };
    return body.user;
  }

  /** POST /api/auth/login. */
  async signIn(email: string, password: string): Promise<{ expiresAt: string }> {
    const res = await this.fetchImpl(`${this.baseUrl}/api/auth/login`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      credentials: 'include',
      body: JSON.stringify({ email, password }),
    });
    await throwIfNotOK(res);
    const body = (await res.json()) as { expires_at: string };
    return { expiresAt: body.expires_at };
  }

  /** POST /api/auth/logout. */
  async signOut(): Promise<void> {
    const res = await this.fetchImpl(`${this.baseUrl}/api/auth/logout`, {
      method: 'POST',
      credentials: 'include',
    });
    await throwIfNotOK(res);
  }

  /** GET /api/auth/me. Throws (401, via throwIfNotOK) if no one is
   * signed in — check for that rather than expecting null back. */
  async getUser(): Promise<MaubaseUser> {
    const res = await this.fetchImpl(`${this.baseUrl}/api/auth/me`, {
      credentials: 'include',
    });
    await throwIfNotOK(res);
    return (await res.json()) as MaubaseUser;
  }

  /** GET /api/auth/me/export — every row the signed-in user owns, across
   * every owner-scoped collection, plus their profile and uploaded-file
   * metadata (not the file bytes — see spec/identity.md IDNT-09). */
  async exportData(): Promise<ExportedAccount> {
    const res = await this.fetchImpl(`${this.baseUrl}/api/auth/me/export`, {
      credentials: 'include',
    });
    await throwIfNotOK(res);
    return (await res.json()) as ExportedAccount;
  }

  /** DELETE /api/auth/me — permanently erases the account: every row it
   * owns, every file it uploaded, every OAuth grant issued on its
   * behalf, then the identity record itself (spec/identity.md
   * IDNT-10/11/13). Irreversible. */
  async deleteAccount(): Promise<void> {
    const res = await this.fetchImpl(`${this.baseUrl}/api/auth/me`, {
      method: 'DELETE',
      credentials: 'include',
    });
    await throwIfNotOK(res);
  }
}
