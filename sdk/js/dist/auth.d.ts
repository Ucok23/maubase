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
export declare class AuthClient {
    private baseUrl;
    private fetchImpl;
    constructor(baseUrl: string, fetchImpl: typeof fetch);
    /** POST /api/auth/signup. Per IDNT-01 this also signs the caller in —
     * the response sets the session cookie, so there's no separate signIn()
     * call needed right after. */
    signUp(email: string, password: string): Promise<MaubaseUser>;
    /** POST /api/auth/login. */
    signIn(email: string, password: string): Promise<{
        expiresAt: string;
    }>;
    /** POST /api/auth/logout. */
    signOut(): Promise<void>;
    /** GET /api/auth/me. Throws (401, via throwIfNotOK) if no one is
     * signed in — check for that rather than expecting null back. */
    getUser(): Promise<MaubaseUser>;
    /** GET /api/auth/me/export — every row the signed-in user owns, across
     * every owner-scoped collection, plus their profile and uploaded-file
     * metadata (not the file bytes — see spec/identity.md IDNT-09). */
    exportData(): Promise<ExportedAccount>;
    /** DELETE /api/auth/me — permanently erases the account: every row it
     * owns, every file it uploaded, every OAuth grant issued on its
     * behalf, then the identity record itself (spec/identity.md
     * IDNT-10/11/13). Irreversible. */
    deleteAccount(): Promise<void>;
}
