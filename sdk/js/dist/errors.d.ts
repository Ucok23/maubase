export interface MaubaseErrorBody {
    error?: string;
    error_description?: string;
}
export declare class MaubaseError extends Error {
    /** HTTP status code, or 0 for an error raised entirely client-side
     * (a state mismatch, a missing browser API) with no response at all. */
    readonly status: number;
    /** The OAuth `error` code (e.g. "invalid_grant"), when there is one. */
    readonly code?: string;
    constructor(message: string, status: number, code?: string);
}
/** Throws a MaubaseError if res isn't a 2xx; otherwise returns it
 * unchanged, so callers can chain `await throwIfNotOK(res)` before
 * reading the body. */
export declare function throwIfNotOK(res: Response): Promise<Response>;
