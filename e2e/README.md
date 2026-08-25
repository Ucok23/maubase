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

One command from the repo root, safe to run from a fresh checkout (or a
fresh agent with no local state — installs are cheap no-ops once cached):

```bash
make e2e
```

That's `npm install && npx playwright install chromium && npm run
test:report` — see the `e2e` target in the root `Makefile`. Equivalently,
from inside `e2e/`:

```bash
npm install
npx playwright install chromium   # first run only; ~300MB, cached afterward
npm run test:report                # runs the suite, then builds report/index.html
```

This builds `maubase` from the repo root and runs it against a throwaway
SQLite DB in `.data/` on port 8811 (see `run-server.sh` and
`playwright.config.ts`'s `webServer` block) — it never touches `data/` or
any real deployment. The bootstrap owner is `owner@e2e.test` /
`e2e-password-123` by default; override via the same
`MAUBASE_BOOTSTRAP_OWNER_EMAIL`/`_PASSWORD` env vars the server itself
reads.

`npm run test:report` (and so `make e2e`) also opens the finished report
in your default browser when it's done — best-effort: on Linux this only
fires if `DISPLAY`/`WAYLAND_DISPLAY` is set (so it's a silent no-op on a
headless box or a sandboxed agent, never a hang or a failure), and a
missing opener binary anywhere just prints the path instead of erroring.

Just the tests, no report (e.g. while iterating on a spec file):
`npm test`. Just rebuilding the report from the last run's output, no
retest: `npm run report:build`. Just (re-)opening the last built report:
`npm run open`.

## Where the output goes

- **`report/index.html`** — the one file to open: every `test.step()`
  narrated with its own screenshot, the full session video, status and
  timing, all self-contained (screenshots and video inlined as data URIs,
  fonts from Google Fonts, no other external requests) — open it directly
  in a browser, no server needed. Built by `scripts/build-report.mjs`
  entirely from `report.json` (Playwright's own `json` reporter output —
  step titles, durations, pass/fail, the video's attachment path) and
  `screenshots/<spec-name>/*.png`; nothing in it is hand-typed, so it
  stays accurate as the test itself changes, and a second spec file
  picked up automatically gets its own section.
- `screenshots/<spec-name>/01-*.png` … — one per `test.step`, namespaced
  by spec file so a second spec can't collide with this one's numbering.
- `test-results/**/video.webm` — the raw recording report/index.html
  embeds (`use.video: 'on'` in the config, captured on every run, not
  just failures).
- `npm run report` opens Playwright's own HTML report (distinct from
  `report/index.html` above), which adds a trace viewer for debugging a
  failure.

All of the above are gitignored — regenerate them by running the suite,
don't expect them to already be present after a fresh checkout.
