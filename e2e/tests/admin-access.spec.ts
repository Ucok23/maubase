// The admin UI from an admin-role seat: full read on Members/Audit
// log/Maintenance (admin+, spec/admin-ui.md's per-page tiers), but the
// create/delete-owner controls stay owner-only regardless — admin is
// below owner, not at it (see owners.html's role check, and OWNR-06/08)
// — and SQL Studio stays out of reach entirely, since it's gated a tier
// above the rest of this group (ADMINUI-16).
import { test, expect } from '@playwright/test';
import { OWNER_EMAIL, OWNER_PASSWORD, createOwnerAccount, makeShotter, signIn, signOut } from './support';

const shot = makeShotter(__filename);

test('an admin can read Members/Audit log/Maintenance, but not mutate owners or reach SQL Studio', async ({ page }) => {
  const adminEmail = `e2e-admin-${Date.now()}@example.com`;
  const adminPassword = 'admin-password-1';

  await test.step('provisioning an admin account (as the bootstrap owner)', async () => {
    await signIn(page, OWNER_EMAIL, OWNER_PASSWORD);
    await createOwnerAccount(page, adminEmail, adminPassword, 'admin');
    await shot(page, 'provisioned');
    await signOut(page);
  });

  await test.step('signing in as the admin', async () => {
    await signIn(page, adminEmail, adminPassword);
    await expect(page.locator('.badge-admin').first()).toBeVisible();
    await shot(page, 'dashboard');
  });

  await test.step('Members lists accounts but offers no create/delete controls', async () => {
    await page.click('a.nav-item[href="/admin/ui/owners"]');
    await page.waitForURL('**/admin/ui/owners');
    await expect(page.locator('td', { hasText: adminEmail })).toBeVisible();
    await expect(page.getByText('Add an owner')).toHaveCount(0);
    await expect(page.getByText('Delete')).toHaveCount(0);
    await shot(page, 'members-read-only');
  });

  await test.step('the audit log is reachable', async () => {
    await page.click('a.nav-item[href="/admin/ui/audit-log"]');
    await page.waitForURL('**/admin/ui/audit-log');
    await shot(page, 'audit-log');
  });

  await test.step('maintenance is reachable and usable', async () => {
    await page.click('a.nav-item[href="/admin/ui/maintenance"]');
    await page.waitForURL('**/admin/ui/maintenance');
    await page.click('button:has-text("Purge expired sessions")');
    await expect(page.getByText('customer session')).toBeVisible();
    await shot(page, 'maintenance');
  });

  await test.step('SQL Studio (owner-only) renders Forbidden, not a redirect', async () => {
    const response = await page.goto('/admin/ui/sql');
    expect(response?.status()).toBe(403);
    await expect(page.locator('h2', { hasText: '403' })).toBeVisible();
    await shot(page, 'sql-studio-forbidden');
  });

  await test.step('signing out', async () => {
    await signOut(page);
    await shot(page, 'logged-out');
  });
});
