import { throwIfNotOK } from './errors.js';
/**
 * The identity layer (spec/identity.md): signup, login, logout, the
 * signed-in user's own profile, and the two GDPR-shaped data-subject
 * actions (export, delete). Every call sends `credentials: 'include'`,
 * so session persistence is just the browser's own cookie jar — there's
 * no token to manage here, unlike DataClient (dataClient.ts), which
 * fronts a completely different, OAuth-gated surface.
 */
export class AuthClient {
    baseUrl;
    fetchImpl;
    constructor(baseUrl, fetchImpl) {
        this.baseUrl = baseUrl;
        this.fetchImpl = fetchImpl;
    }
    /** POST /api/auth/signup. Per IDNT-01 this also signs the caller in —
     * the response sets the session cookie, so there's no separate signIn()
     * call needed right after. */
    async signUp(email, password) {
        const res = await this.fetchImpl(`${this.baseUrl}/api/auth/signup`, {
            method: 'POST',
            headers: { 'content-type': 'application/json' },
            credentials: 'include',
            body: JSON.stringify({ email, password }),
        });
        await throwIfNotOK(res);
        const body = (await res.json());
        return body.user;
    }
    /** POST /api/auth/login. */
    async signIn(email, password) {
        const res = await this.fetchImpl(`${this.baseUrl}/api/auth/login`, {
            method: 'POST',
            headers: { 'content-type': 'application/json' },
            credentials: 'include',
            body: JSON.stringify({ email, password }),
        });
        await throwIfNotOK(res);
        const body = (await res.json());
        return { expiresAt: body.expires_at };
    }
    /** POST /api/auth/logout. */
    async signOut() {
        const res = await this.fetchImpl(`${this.baseUrl}/api/auth/logout`, {
            method: 'POST',
            credentials: 'include',
        });
        await throwIfNotOK(res);
    }
    /** GET /api/auth/me. Throws (401, via throwIfNotOK) if no one is
     * signed in — check for that rather than expecting null back. */
    async getUser() {
        const res = await this.fetchImpl(`${this.baseUrl}/api/auth/me`, {
            credentials: 'include',
        });
        await throwIfNotOK(res);
        return (await res.json());
    }
    /** GET /api/auth/me/export — every row the signed-in user owns, across
     * every owner-scoped collection, plus their profile and uploaded-file
     * metadata (not the file bytes — see spec/identity.md IDNT-09). */
    async exportData() {
        const res = await this.fetchImpl(`${this.baseUrl}/api/auth/me/export`, {
            credentials: 'include',
        });
        await throwIfNotOK(res);
        return (await res.json());
    }
    /** DELETE /api/auth/me — permanently erases the account: every row it
     * owns, every file it uploaded, every OAuth grant issued on its
     * behalf, then the identity record itself (spec/identity.md
     * IDNT-10/11/13). Irreversible. */
    async deleteAccount() {
        const res = await this.fetchImpl(`${this.baseUrl}/api/auth/me`, {
            method: 'DELETE',
            credentials: 'include',
        });
        await throwIfNotOK(res);
    }
}
//# sourceMappingURL=auth.js.map