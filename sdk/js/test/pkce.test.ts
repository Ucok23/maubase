import { describe, expect, it } from 'vitest';
import { generateCodeVerifier, generateState, challengeFromVerifier } from '../src/pkce.js';

describe('pkce', () => {
  it('derives the S256 challenge matching RFC 7636 Appendix B\'s worked example', async () => {
    // https://www.rfc-editor.org/rfc/rfc7636#appendix-B — a fixed,
    // published verifier/challenge pair, so this checks the actual
    // base64url(SHA-256(verifier)) math, not just "it returns a string".
    const verifier = 'dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk';
    const challenge = await challengeFromVerifier(verifier);
    expect(challenge).toBe('E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM');
  });

  it('generates a URL-safe verifier with no padding', () => {
    const verifier = generateCodeVerifier();
    expect(verifier).toMatch(/^[A-Za-z0-9_-]+$/);
    expect(verifier.length).toBeGreaterThanOrEqual(43); // RFC 7636 §4.1 minimum
  });

  it('generates a different verifier every time', () => {
    const a = generateCodeVerifier();
    const b = generateCodeVerifier();
    expect(a).not.toBe(b);
  });

  it('generates a state well over the 8-character minimum AUTHZ-07 enforces', () => {
    const state = generateState();
    expect(state.length).toBeGreaterThan(8);
  });
});
