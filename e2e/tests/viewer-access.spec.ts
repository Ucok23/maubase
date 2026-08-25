// The admin UI from a viewer-role seat — the least-privileged role, and
// (before this spec existed) a seat the browser suite never actually sat
// in: every other spec here signs in as owner. Confirms, by rendering
// them in a real browser rather than just asserting a status code, that
// write controls are absent for a viewer (spec/admin-ui.md ADMINUI-14)
// and that a page above their role renders the Forbidden page, not a
// silent redirect (ADMINUI-05) — the same guarantee
// test/adminui_test.go's TestAdminUI_RoleBelowPageMinimumGets403 checks
// over raw HTTP, here checked as what actually paints on screen.
import { test, expect } from '@playwright/test';
import { OWNER_EMAIL, OWNER_PASSWORD, createOwnerAccount, makeShotter, signIn, signOut } from './support';

const shot = makeShotter(__filename);

test('a viewer can read the admin UI but sees no write controls, and is blocked from an admin+ page', async ({ page }) => {
  const viewerEmail = `e2e-viewer-${Date.now()}@example.com`;
  const viewerPassword = 'viewer-password-1';

  await test.step('provisioning a viewer account (as the bootstrap owner)', async () => {
    await signIn(page, OWNER_EMAIL, OWNER_PASSWORD);
    await createOwnerAccount(page, viewerEmail, viewerPassword, 'viewer');
    await signOut(page);
  });

  await test.step('signing in as the viewer', async () => {
    await signIn(page, viewerEmail, viewerPassword);
    await expect(page.locator('.badge-viewer').first()).toBeVisible();
    await shot(page, 'dashboard');
  });

  await test.step('the data browser has no "New table" control', async () => {
    await page.click('a.nav-item[href="/admin/ui/data"]');
    await page.waitForURL('**/admin/ui/data');
    await expect(page.getByText('New table')).toHaveCount(0);
    await shot(page, 'data-read-only');
  });

  await test.step('the Users panel has no create-account form', async () => {
    await page.click('a.nav-item[href="/admin/ui/users"]');
    await page.waitForURL('**/admin/ui/users');
    await expect(page.getByText('Create a user')).toHaveCount(0);
    await shot(page, 'users-read-only');
  });

  await test.step('Members (admin+) renders Forbidden, not a redirect', async () => {
    const response = await page.goto('/admin/ui/owners');
    expect(response?.status()).toBe(403);
    await expect(page.locator('h2', { hasText: '403' })).toBeVisible();
    await shot(page, 'members-forbidden');
  });

  await test.step('signing out', async () => {
    await signOut(page);
    await shot(page, 'logged-out');
  });
});
