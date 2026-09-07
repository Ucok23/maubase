/** Tokens live only as long as this object does — gone on page reload or
 * process restart. The fallback for any environment without
 * localStorage; also handy in tests. */
export class MemoryTokenStore {
    tokens = null;
    get() {
        return this.tokens;
    }
    set(tokens) {
        this.tokens = tokens;
    }
    clear() {
        this.tokens = null;
    }
}
export class LocalStorageTokenStore {
    key;
    constructor(key) {
        this.key = key;
    }
    get() {
        const raw = window.localStorage.getItem(this.key);
        if (!raw)
            return null;
        try {
            return JSON.parse(raw);
        }
        catch {
            return null;
        }
    }
    set(tokens) {
        window.localStorage.setItem(this.key, JSON.stringify(tokens));
    }
    clear() {
        window.localStorage.removeItem(this.key);
    }
}
/** localStorage-backed in a browser; an in-memory store (tokens don't
 * survive a reload) anywhere else.
 *
 * Deliberately checks `window.localStorage` rather than the bare
 * `localStorage` global: recent Node versions (22+) define that global
 * unconditionally but leave it non-functional without a
 * `--localstorage-file` flag, so `typeof localStorage` alone can no
 * longer tell a real browser apart from plain Node. */
export function defaultTokenStore(key) {
    if (typeof window !== 'undefined' && window.localStorage) {
        return new LocalStorageTokenStore(key);
    }
    return new MemoryTokenStore();
}
//# sourceMappingURL=tokenStore.js.map