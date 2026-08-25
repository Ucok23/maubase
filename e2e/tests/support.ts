// Shared by every spec in this directory: one shot()-per-file screenshot
// helper (scripts/build-report.mjs expects exactly one
// screenshots/<spec-basename>/ directory per spec file, numbered in step
// order — see its comment), the native-confirm()-dialog helper the
// delete/revoke buttons scattered across the admin UI all need, and the
// sign-in/sign-out/create-owner flows every spec starts from.
import { type Page } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';

export const OWNER_EMAIL = process.env.MAUBASE_BOOTSTRAP_OWNER_EMAIL ?? 'owner@e2e.test';
export const OWNER_PASSWORD = process.env.MAUBASE_BOOTSTRAP_OWNER_PASSWORD ?? 'e2e-password-123';

// Call once at module scope with `__filename` — wipes and recreates this
// spec's own screenshots/<spec-basename>/ directory, and returns a
// shot(page, name) bound to it. One call per spec file, not per test: a
// spec with several test()s would otherwise all feed the same numbered
// sequence, which is exactly what every spec file in this suite is
// written to avoid (one test per file — see any spec for why).
export function makeShotter(specFile: string) {
  const dir = path.join(path.dirname(specFile), '..', 'screenshots', path.basename(specFile).replace(/\.spec\.ts$/, ''));
  fs.rmSync(dir, { recursive: true, force: true });
  fs.mkdirSync(dir, { recursive: true });
  let index = 0;
  return async function shot(page: Page, name: string) {
    index += 1;
    await page.screenshot({ path: path.join(dir, `${String(index).padStart(2, '0')}-${name}.png`), fullPage: true });
  };
}

// acceptNextDialog primes the page's native confirm() for the next
// onsubmit="return confirm(...)" control it clicks (every delete/revoke
// button in the admin UI uses one) — Playwright never shows these; a
// listener has to accept them or the click just hangs.
export function acceptNextDialog(page: Page) {
  page.once('dialog', (dialog) => dialog.accept());
}

export async function signIn(page: Page, email: string, password: string) {
  await page.goto('/admin/ui/login');
  await page.fill('input[name="email"]', email);
  await page.fill('input[name="password"]', password);
  await Promise.all([
    page.waitForURL('**/admin/ui'),
    page.click('button[type="submit"]'),
  ]);
}

export async function signOut(page: Page) {
  await Promise.all([
    page.waitForURL('**/admin/ui/login'),
    page.click('button:has-text("Log out")'),
  ]);
}

// createOwnerAccount drives the Members page's own "Add an owner" form
// (rather than calling the JSON API directly) — provisioning the test
// accounts other specs sign in as is itself exercised UI, the same form
// a real owner would use, not a shortcut around it.
export async function createOwnerAccount(page: Page, email: string, password: string, role: 'viewer' | 'developer' | 'admin' | 'owner') {
  await page.goto('/admin/ui/owners');
  const form = page.locator('form[action="/admin/ui/owners"]');
  await form.locator('input[name="email"]').fill(email);
  await form.locator('input[name="password"]').fill(password);
  await form.locator('select[name="role"]').selectOption(role);
  await Promise.all([
    page.waitForURL('**/admin/ui/owners'),
    form.locator('button[type="submit"]').click(),
  ]);
}
