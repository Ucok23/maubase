export class MaubaseError extends Error {
    /** HTTP status code, or 0 for an error raised entirely client-side
     * (a state mismatch, a missing browser API) with no response at all. */
    status;
    /** The OAuth `error` code (e.g. "invalid_grant"), when there is one. */
    code;
    constructor(message, status, code) {
        super(message);
        this.name = 'MaubaseError';
        this.status = status;
        this.code = code;
    }
}
/** Throws a MaubaseError if res isn't a 2xx; otherwise returns it
 * unchanged, so callers can chain `await throwIfNotOK(res)` before
 * reading the body. */
export async function throwIfNotOK(res) {
    if (res.ok)
        return res;
    let body;
    try {
        body = (await res.json());
    }
    catch {
        // Not a JSON body (or no body at all) — fall through to statusText.
    }
    const message = body?.error_description ?? body?.error ?? res.statusText ?? `request failed with status ${res.status}`;
    throw new MaubaseError(message, res.status, body?.error);
}
//# sourceMappingURL=errors.js.map