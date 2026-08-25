#!/usr/bin/env node
// Turns a Playwright run into a single self-contained HTML storyboard:
// every test.step() narrated with its own screenshot, in order, plus the
// full session video — driven entirely by report.json (the `json`
// reporter configured in playwright.config.ts) and the screenshots each
// spec wrote to screenshots/<spec-basename>/, never hand-typed.
//
// Run after (or as part of) a test run:
//   npm test && npm run report:build     # or: npm run test:report
//
// Output: report/index.html — gitignored, regenerated fresh every run.
// Open it directly in a browser; nothing it references is external.
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.join(__dirname, '..');
const REPORT_JSON = path.join(ROOT, 'report.json');
const SCREENSHOTS_DIR = path.join(ROOT, 'screenshots');
const OUT_DIR = path.join(ROOT, 'report');
const OUT_FILE = path.join(OUT_DIR, 'index.html');

if (!fs.existsSync(REPORT_JSON)) {
  console.error(
    `error: ${path.relative(ROOT, REPORT_JSON)} not found — run the tests first (npm test), which writes it via the 'json' reporter in playwright.config.ts.`
  );
  process.exit(1);
}

const report = JSON.parse(fs.readFileSync(REPORT_JSON, 'utf8'));

// --- walk report.json into a flat list of {suiteFile, specTitle, project,
// result, steps} entries, recursing through describe-block suites ------

function specSlug(suiteFile) {
  return path.basename(suiteFile).replace(/\.spec\.[tj]s$/, '');
}

function collectRuns(suites, inheritedFile) {
  const runs = [];
  for (const suite of suites ?? []) {
    const file = suite.file ?? inheritedFile;
    for (const spec of suite.specs ?? []) {
      for (const test of spec.tests ?? []) {
        // The last result is what actually counts if there were retries.
        const result = test.results?.[test.results.length - 1];
        if (!result) continue;
        runs.push({
          file,
          slug: specSlug(file),
          title: spec.title,
          project: test.projectName,
          status: result.status,
          durationMs: result.duration,
          steps: result.steps ?? [],
          video: (result.attachments ?? []).find((a) => a.name === 'video'),
        });
      }
    }
    runs.push(...collectRuns(suite.suites, file));
  }
  return runs;
}

const runs = collectRuns(report.suites, undefined);
if (runs.length === 0) {
  console.error('error: report.json has no test results to build a report from.');
  process.exit(1);
}

function b64File(file) {
  return fs.readFileSync(file).toString('base64');
}

function readSortedScreenshots(slug) {
  const dir = path.join(SCREENSHOTS_DIR, slug);
  if (!fs.existsSync(dir)) return [];
  return fs
    .readdirSync(dir)
    .filter((f) => f.endsWith('.png'))
    .sort() // filenames are zero-padded "01-*.png", so lexical == numeric order
    .map((f) => path.join(dir, f));
}

function humanDuration(ms) {
  return ms < 1000 ? `${Math.round(ms)}ms` : `${(ms / 1000).toFixed(1)}s`;
}

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

function buildRunSection(run, index) {
  const shots = readSortedScreenshots(run.slug);
  // Steps and screenshots are paired purely by array position (step i's
  // screenshot is shots[i]) — there's no name or id linking a given
  // test.step() to the shot() call inside it. That's fine as long as
  // every step calls shot() exactly once, but silently wrong the moment
  // one doesn't: every screenshot after the gap shifts up by one and
  // ends up captioned with the wrong step's title, with nothing about
  // the output looking obviously broken. Failing loudly here beats
  // shipping a report that's quietly mislabeled throughout.
  if (shots.length !== run.steps.length) {
    throw new Error(
      `${run.file} ("${run.title}"): ${run.steps.length} test.step() call(s) but ${shots.length} screenshot(s) in screenshots/${run.slug}/. ` +
        `Every test.step() in a spec must call shot() exactly once — otherwise every screenshot after the gap gets paired with the wrong step's title. ` +
        `Add the missing shot() call (or remove the extra one), then rerun.`
    );
  }
  const stepsHtml = run.steps
    .map((step, i) => {
      const shotFile = shots[i];
      const img = shotFile
        ? `<img src="data:image/png;base64,${b64File(shotFile)}" alt="Screenshot: ${escapeHtml(step.title)}" loading="lazy">`
        : `<div class="no-shot">no screenshot captured for this step</div>`;
      return `
      <li class="step">
        <div class="step-marker" aria-hidden="true"><span>${String(i + 1).padStart(2, '0')}</span></div>
        <div class="step-body">
          <div class="step-head">
            <h3>${escapeHtml(step.title)}</h3>
            <span class="step-dur">${humanDuration(step.duration)}</span>
          </div>
          <figure class="frame">
            <div class="frame-bar"><span class="dot"></span><span class="dot"></span><span class="dot"></span></div>
            ${img}
          </figure>
        </div>
      </li>`;
    })
    .join('\n');

  const videoHtml = run.video
    ? `<div class="video-card">
        <video controls preload="metadata">
          <source src="data:video/webm;base64,${b64File(run.video.path)}" type="video/webm">
        </video>
        <div class="video-caption"><span>${escapeHtml(run.file)}</span><span>full recording &middot; video/webm</span></div>
      </div>`
    : '';

  const statusClass = run.status === 'passed' ? 'status-pass' : 'status-fail';

  return `
    <section class="run">
      <div class="run-head">
        <div class="eyebrow"><span class="rec-dot ${statusClass}"></span>${escapeHtml(run.file)}</div>
        <h2>${escapeHtml(run.title)}</h2>
        <div class="meta-row">
          <span class="pill ${statusClass}">${run.status}</span>
          <span class="pill">${escapeHtml(run.project)}</span>
          <span class="pill">${humanDuration(run.durationMs)}</span>
          <span class="pill">${run.steps.length} steps</span>
        </div>
      </div>
      ${videoHtml}
      <ol class="steps">
${stepsHtml}
      </ol>
    </section>`;
}

const runsHtml = runs.map(buildRunSection).join('\n');
const generatedAt = new Date().toISOString();
const totalDuration = runs.reduce((sum, r) => sum + (r.durationMs ?? 0), 0);
const passCount = runs.filter((r) => r.status === 'passed').length;

const html = `<title>Playwright Run Report</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Big+Shoulders:wght@500;700;800&family=Source+Sans+3:wght@400;500;600&family=IBM+Plex+Mono:wght@400;500&display=swap" rel="stylesheet">
<style>
  :root {
    --bg: #F2F4F7; --surface: #FFFFFF; --surface-raised: #FBFCFD;
    --ink: #111820; --muted: #5B6672; --faint: #8C97A2; --border: #DCE2E7;
    --accent: #B96A17; --accent-strong: #9C580F;
    --success: #2A8C58; --success-bg: #E4F3EA;
    --fail: #C24444; --fail-bg: #FBEAEA;
    --shadow: 0 1px 2px rgba(17,24,32,0.04), 0 8px 24px -12px rgba(17,24,32,0.12);
    --radius: 6px;
    --font-display: "Big Shoulders", "Arial Narrow", sans-serif;
    --font-body: "Source Sans 3", "Segoe UI", system-ui, sans-serif;
    --font-mono: "IBM Plex Mono", ui-monospace, "SFMono-Regular", Menlo, monospace;
  }
  @media (prefers-color-scheme: dark) {
    :root:not([data-theme="light"]) {
      --bg: #0B1116; --surface: #121A21; --surface-raised: #16202A;
      --ink: #E8EDF0; --muted: #97A3AD; --faint: #63707B; --border: #223038;
      --accent: #E9A24C; --accent-strong: #F2B366;
      --success: #4FBE86; --success-bg: #123122;
      --fail: #E17777; --fail-bg: #331616;
      --shadow: 0 1px 2px rgba(0,0,0,0.3), 0 12px 32px -16px rgba(0,0,0,0.6);
    }
  }
  :root[data-theme="dark"] {
    --bg: #0B1116; --surface: #121A21; --surface-raised: #16202A;
    --ink: #E8EDF0; --muted: #97A3AD; --faint: #63707B; --border: #223038;
    --accent: #E9A24C; --accent-strong: #F2B366;
    --success: #4FBE86; --success-bg: #123122;
    --fail: #E17777; --fail-bg: #331616;
    --shadow: 0 1px 2px rgba(0,0,0,0.3), 0 12px 32px -16px rgba(0,0,0,0.6);
  }
  * { box-sizing: border-box; }
  html, body { margin: 0; padding: 0; }
  body { background: var(--bg); color: var(--ink); font-family: var(--font-body); font-size: 16px; line-height: 1.55; -webkit-font-smoothing: antialiased; }
  img { max-width: 100%; display: block; }
  .wrap { max-width: 900px; margin: 0 auto; padding: 3rem 1.5rem 5rem; }

  .page-head { margin-bottom: 2.5rem; }
  .page-eyebrow { font-family: var(--font-mono); font-size: 0.72rem; letter-spacing: 0.14em; text-transform: uppercase; color: var(--accent-strong); }
  h1 { font-family: var(--font-display); font-weight: 800; font-size: clamp(2.1rem, 5vw, 3rem); line-height: 1.02; letter-spacing: -0.01em; margin: 0.35rem 0 0.6rem; text-wrap: balance; }
  .page-stats { font-family: var(--font-mono); font-size: 0.85rem; color: var(--muted); }

  .run { padding: 2rem 0 2.5rem; border-top: 1px solid var(--border); }
  .run:first-of-type { border-top: none; padding-top: 0; }
  .run-head { margin-bottom: 1.25rem; }
  .eyebrow { font-family: var(--font-mono); font-size: 0.72rem; letter-spacing: 0.1em; text-transform: uppercase; color: var(--faint); display: flex; align-items: center; gap: 0.5rem; }
  .rec-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--accent); box-shadow: 0 0 0 3px color-mix(in srgb, var(--accent) 20%, transparent); }
  .rec-dot.status-fail { background: var(--fail); box-shadow: 0 0 0 3px color-mix(in srgb, var(--fail) 20%, transparent); }
  h2 { font-family: var(--font-display); font-weight: 700; font-size: 1.7rem; letter-spacing: -0.005em; margin: 0.3rem 0 0.75rem; text-wrap: balance; }
  .meta-row { display: flex; flex-wrap: wrap; gap: 0.5rem; }
  .pill { font-family: var(--font-mono); font-size: 0.78rem; padding: 0.3rem 0.65rem; border-radius: 999px; border: 1px solid var(--border); background: var(--surface); color: var(--muted); white-space: nowrap; }
  .pill.status-pass { background: var(--success-bg); border-color: color-mix(in srgb, var(--success) 35%, var(--border)); color: var(--success); font-weight: 600; }
  .pill.status-fail { background: var(--fail-bg); border-color: color-mix(in srgb, var(--fail) 35%, var(--border)); color: var(--fail); font-weight: 600; }

  .video-card { background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius); box-shadow: var(--shadow); overflow: hidden; margin: 1.25rem 0 2rem; }
  .video-card video { display: block; width: 100%; background: #000; }
  .video-caption { display: flex; justify-content: space-between; gap: 1rem; padding: 0.6rem 0.9rem; font-family: var(--font-mono); font-size: 0.74rem; color: var(--faint); border-top: 1px solid var(--border); }

  ol.steps { list-style: none; margin: 0; padding: 0; position: relative; }
  ol.steps::before { content: ""; position: absolute; left: 20px; top: 8px; bottom: 8px; width: 1px; background: var(--border); }
  .step { display: grid; grid-template-columns: 42px 1fr; gap: 1.1rem; padding-bottom: 2.25rem; position: relative; }
  .step:last-child { padding-bottom: 0; }
  .step-marker { width: 42px; height: 42px; border-radius: 50%; background: var(--surface); border: 1px solid var(--border); display: flex; align-items: center; justify-content: center; z-index: 1; box-shadow: var(--shadow); }
  .step-marker span { font-family: var(--font-mono); font-size: 0.78rem; color: var(--muted); }
  .step-head { display: flex; align-items: baseline; justify-content: space-between; gap: 1rem; margin: 0.15rem 0 0.6rem; }
  .step-head h3 { font-family: var(--font-display); font-weight: 700; font-size: 1.2rem; letter-spacing: -0.005em; margin: 0; text-wrap: balance; }
  .step-dur { font-family: var(--font-mono); font-size: 0.74rem; color: var(--faint); white-space: nowrap; }

  .frame { margin: 0; background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius); box-shadow: var(--shadow); overflow: hidden; }
  .frame-bar { display: flex; gap: 0.4rem; padding: 0.5rem 0.7rem; background: var(--surface-raised); border-bottom: 1px solid var(--border); }
  .frame-bar .dot { width: 8px; height: 8px; border-radius: 50%; background: var(--border); }
  .frame img { width: 100%; }
  .no-shot { padding: 2rem; text-align: center; color: var(--faint); font-family: var(--font-mono); font-size: 0.85rem; }

  .foot { margin-top: 3rem; padding-top: 1.5rem; border-top: 1px solid var(--border); font-family: var(--font-mono); font-size: 0.78rem; color: var(--faint); }

  @media (max-width: 560px) {
    .step { grid-template-columns: 32px 1fr; gap: 0.8rem; }
    .step-marker { width: 32px; height: 32px; }
    ol.steps::before { left: 15px; }
  }
</style>

<div class="wrap">
  <header class="page-head">
    <div class="page-eyebrow">Playwright &middot; e2e run report</div>
    <h1>Run report</h1>
    <div class="page-stats">${runs.length} test${runs.length === 1 ? '' : 's'} &middot; ${passCount}/${runs.length} passed &middot; ${humanDuration(totalDuration)} total &middot; generated ${generatedAt}</div>
  </header>

${runsHtml}

  <footer class="foot">Built by scripts/build-report.mjs from report.json + screenshots/ &mdash; regenerate with <code>npm run report:build</code>.</footer>
</div>
`;

fs.mkdirSync(OUT_DIR, { recursive: true });
fs.writeFileSync(OUT_FILE, html);
console.log(`wrote ${path.relative(ROOT, OUT_FILE)} (${(fs.statSync(OUT_FILE).size / 1024 / 1024).toFixed(2)} MiB) from ${runs.length} test result(s)`);
