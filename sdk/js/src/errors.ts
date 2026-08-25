// maubase's two HTTP surfaces use two different error shapes: the
// identity layer always sends {"error": "message"} (see
// internal/server/handlers_auth.go's writeAuthError), while the OAuth
// endpoints follow RFC 6749's {"error": "code", "error_description":
// "message"}. MaubaseError normalizes both into one thing so callers
// never need to know which surface a failed request hit.
export interface MaubaseErrorBody {
  error?: string;
  error_description?: string;
}

export class MaubaseError extends Error {
  /** HTTP status code, or 0 for an error raised entirely client-side
   * (a state mismatch, a missing browser API) with no response at all. */
  readonly status: number;
  /** The OAuth `error` code (e.g. "invalid_grant"), when there is one. */
  readonly code?: string;

  constructor(message: string, status: number, code?: string) {
    super(message);
    this.name = 'MaubaseError';
    this.status = status;
    this.code = code;
  }
}

/** Throws a MaubaseError if res isn't a 2xx; otherwise returns it
 * unchanged, so callers can chain `await throwIfNotOK(res)` before
 * reading the body. */
export async function throwIfNotOK(res: Response): Promise<Response> {
  if (res.ok) return res;
  let body: MaubaseErrorBody | undefined;
  try {
    body = (await res.json()) as MaubaseErrorBody;
  } catch {
    // Not a JSON body (or no body at all) — fall through to statusText.
  }
  const message = body?.error_description ?? body?.error ?? res.statusText ?? `request failed with status ${res.status}`;
  throw new MaubaseError(message, res.status, body?.error);
}
