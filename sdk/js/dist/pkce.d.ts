/** A random code_verifier, per RFC 7636 §4.1 (43-128 chars from the
 * unreserved URL-safe alphabet — 32 random bytes, base64url-encoded,
 * comes out to 43). */
export declare function generateCodeVerifier(): string;
/** The S256 code_challenge for a given verifier: base64url(SHA-256(verifier)). */
export declare function challengeFromVerifier(verifier: string): Promise<string>;
/** An opaque CSRF token for the authorize request's `state` parameter.
 * spec/oauth-authorize-and-consent.md AUTHZ-07 rejects anything under 8
 * characters; this is far longer. Reuses the same generator as the code
 * verifier — the two have identical requirements (random, URL-safe). */
export declare function generateState(): string;
