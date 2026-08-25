// PKCE (RFC 7636) helpers. DataClient (dataClient.ts) drives the OAuth
// authorization code flow itself — spec/oauth-authorize-and-consent.md
// AUTHZ-06 requires a code_challenge on every client, public or
// confidential, so there's no way to skip this even for a "trusted"
// first-party app.
function base64UrlEncode(bytes: Uint8Array): string {
  let binary = '';
  for (const b of bytes) binary += String.fromCharCode(b);
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

/** A random code_verifier, per RFC 7636 §4.1 (43-128 chars from the
 * unreserved URL-safe alphabet — 32 random bytes, base64url-encoded,
 * comes out to 43). */
export function generateCodeVerifier(): string {
  const bytes = new Uint8Array(32);
  crypto.getRandomValues(bytes);
  return base64UrlEncode(bytes);
}

/** The S256 code_challenge for a given verifier: base64url(SHA-256(verifier)). */
export async function challengeFromVerifier(verifier: string): Promise<string> {
  const data = new TextEncoder().encode(verifier);
  const digest = await crypto.subtle.digest('SHA-256', data);
  return base64UrlEncode(new Uint8Array(digest));
}

/** An opaque CSRF token for the authorize request's `state` parameter.
 * spec/oauth-authorize-and-consent.md AUTHZ-07 rejects anything under 8
 * characters; this is far longer. Reuses the same generator as the code
 * verifier — the two have identical requirements (random, URL-safe). */
export function generateState(): string {
  return generateCodeVerifier();
}
