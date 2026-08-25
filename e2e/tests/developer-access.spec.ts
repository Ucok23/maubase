// The admin UI from a developer-role seat: write access to the data
// browser and Users panel (spec/admin-ui.md ADMINUI-13/27, developer+),
// and the create-table form (ADMINUI-21, also developer+) — but the
// sidebar offers nothing above that tier (ADMINUI-31), and Members
// itself, which needs admin+, still blocks a direct visit.
import { test, expect } from '@playwright/test';
import { OWNER_EMAIL, OWNER_PASSWORD, createOwnerAccount, makeShotter, signIn, signOut } from './support';

const shot = makeShotter(__filename);

test('a developer gets write controls on data and users, and can define a table, but is blocked from Members', async ({ page }) => {
  const devEmail = `e2e-developer-${Date.now()}@example.com`;
  const devPassword = 'developer-password-1';

  await test.step('provisioning a developer account (as the bootstrap owner)', async () => {
    await signIn(page, OWNER_EMAIL, OWNER_PASSWORD);
    await createOwnerAccount(page, devEmail, devPassword, 'developer');
    await shot(page, 'provisioned');
    await signOut(page);
  });

  await test.step('signing in as the developer', async () => {
    await signIn(page, devEmail, devPassword);
    await expect(page.locator('.badge-developer').first()).toBeVisible();
    await shot(page, 'dashboard');
  });

  await test.step('the sidebar offers no owner-plane pages at all', async () => {
    for (const href of ['/admin/ui/owners', '/admin/ui/audit-log', '/admin/ui/maintenance', '/admin/ui/sql']) {
      await expect(page.locator(`a.nav-item[href="${href}"]`)).toHaveCount(0);
    }
    await shot(page, 'sidebar-no-owner-plane');
  });

  await test.step('the data browser offers "New table"', async () => {
    await page.click('a.nav-item[href="/admin/ui/data"]');
    await page.waitForURL('**/admin/ui/data');
    await expect(page.getByText('New table')).toBeVisible();
    await shot(page, 'data-with-write-controls');
  });

  await test.step('the Users panel offers a create-account form', async () => {
    await page.click('a.nav-item[href="/admin/ui/users"]');
    await page.waitForURL('**/admin/ui/users');
    await expect(page.getByText('Create a user')).toBeVisible();
    await shot(page, 'users-with-write-controls');
  });

  await test.step('the new-table form itself is reachable', async () => {
    await page.goto('/admin/ui/tables/new');
    await expect(page.locator('input[name="name"]')).toBeVisible();
    await shot(page, 'new-table-form');
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
