import { AuthClient } from './auth.js';
import { DataClient } from './dataClient.js';
import { defaultTokenStore } from './tokenStore.js';
/**
 * One maubase deployment, as seen by a customer-facing app: `.auth` for
 * signup/login/session (spec/identity.md, cookie-based — no setup
 * needed), `.data` for auto-REST (spec/auto-rest.md, OAuth-gated — see
 * DataClient's own doc comment for what that actually requires).
 */
export class MaubaseClient {
    auth;
    data;
    constructor(url, options = {}) {
        const baseUrl = url.replace(/\/+$/, '');
        const fetchImpl = options.fetch ?? globalThis.fetch;
        if (!fetchImpl) {
            throw new Error('no fetch implementation available in this environment — pass one explicitly via createClient(url, { fetch })');
        }
        this.auth = new AuthClient(baseUrl, fetchImpl);
        this.data = new DataClient({
            baseUrl,
            fetchImpl,
            tokenStore: options.tokenStore ?? defaultTokenStore(`maubase.tokens.${baseUrl}`),
            redirectUri: options.redirectUri,
            clientName: options.clientName,
        });
    }
}
/** `createClient('https://api.example.com')` — see MaubaseClient. */
export function createClient(url, options) {
    return new MaubaseClient(url, options);
}
//# sourceMappingURL=client.js.map