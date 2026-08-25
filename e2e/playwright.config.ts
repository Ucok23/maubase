import { defineConfig, devices } from '@playwright/test';

// A fixed, uncommon port — the same one e2e/run-server.sh binds to — so
// this suite never collides with a `make run` dev server on :8080.
const PORT = 8811;
const BASE_URL = `http://127.0.0.1:${PORT}`;

export default defineConfig({
  testDir: './tests',
  timeout: 30_000,
  // Sequential, one worker: the flow test signs in as the sole bootstrap
  // owner and mutates shared server state (creates/deletes a customer
  // account) step by step, on purpose — it's a storyboard of one session,
  // not independent cases that benefit from parallelism.
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [
    ['list'],
    ['html', { outputFolder: 'playwright-report', open: 'never' }],
  ],
  outputDir: 'test-results',
  use: {
    baseURL: BASE_URL,
    viewport: { width: 1280, height: 800 },
    trace: 'retain-on-failure',
    // Always on, not just on failure: the point of this suite is the
    // recording itself, not just pass/fail — see README.md.
    video: { mode: 'on', size: { width: 1280, height: 800 } },
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: {
    command: './run-server.sh',
    url: `${BASE_URL}/admin/ui/login`,
    reuseExistingServer: !process.env.CI,
    timeout: 60_000,
  },
});
