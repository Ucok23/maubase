# Browser e2e: the admin UI, storyboarded

A Playwright suite that drives the embedded admin UI (`internal/adminui`,
`spec/admin-ui.md`) in a real browser and produces a visual record of the
run: a numbered PNG per step in `screenshots/`, plus Playwright's own video
recording of the whole session — the "show your work" artifact this suite
exists to produce, not just pass/fail. It's deliberately separate from the
Go end-to-end suite in `/test`: that one never opens a browser (it drives
HTTP directly), this one only exists because the admin UI is the one
surface in this repo with an actual UI to look at.

The one test (`tests/admin-ui-flow.spec.ts`) walks the whole loop: sign in
as the bootstrap owner → dashboard → data browser → Users panel → create a
customer account → view its detail page → sign it out everywhere → delete
it → confirm all three actions landed in the audit log → sign out.

## Platform note

This machine runs Omarchy (Arch-based), which Playwright doesn't
officially support — `npx playwright install` prints a "BEWARE: your OS is
not officially supported" warning and falls back to an Ubuntu 24.04 build.
That fallback works fine here: confirmed by actually launching headless
Chromium and running the suite, not just by installing it. **Docker isn't
needed on this machine.** If a *different* machine (a stripped-down CI
runner, a from-scratch install) turns out to be missing some shared
library Chromium needs — a runtime error like `error while loading shared
libraries: libnss3.so`, not an install-time warning — fall back to
Playwright's own Docker image, which ships every dependency preinstalled:

```bash
docker run --rm -v "$(pwd)/..":/work -w /work/e2e \
  mcr.microsoft.com/playwright:v1.55.0-noble \
  bash -c "npm ci && npx playwright test"
```

(Match the image tag to the `@playwright/test` version in `package.json`.)

## Running it

```bash
cd e2e
npm install
npx playwright install chromium   # first run only; ~300MB, cached afterward
npm test
```

`npm test` builds `maubase` from the repo root and runs it against a
throwaway SQLite DB in `.data/` on port 8811 (see `run-server.sh` and
`playwright.config.ts`'s `webServer` block) — it never touches `data/` or
any real deployment. The bootstrap owner is `owner@e2e.test` /
`e2e-password-123` by default; override via the same
`MAUBASE_BOOTSTRAP_OWNER_EMAIL`/`_PASSWORD` env vars the server itself
reads.

## Where the output goes

- `screenshots/01-login.png` … `10-logged-out.png` — one per `test.step`,
  overwritten fresh on every run.
- `test-results/**/video.webm` — the full recording (`use.video: 'on'` in
  the config, so this is captured on every run, not just failures).
- `npm run report` opens the HTML report, which links both of the above
  plus a trace viewer for the run.

All three are gitignored — regenerate them by running the suite, don't
expect them to already be present after a fresh checkout.
