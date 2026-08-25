// The customer-plane story: an owner manages a customer account end to
// end through the Users panel (spec/admin-ui.md ADMINUI-25..30) and
// confirms it in the audit trail. Narrated with test.step() and a
// numbered screenshot after each one, with Playwright recording the whole
// run to video throughout (see playwright.config.ts's use.video) — the
// storyboard-plus-video "show your work" artifact this suite exists to
// produce, not a bag of independent assertions.
//
// One of several role/area-focused specs in this directory — see
// owner-console.spec.ts (the rest of the owner-only surface: Members,
// Maintenance, Create Table, Data, SQL Studio) and
// viewer-access.spec.ts/developer-access.spec.ts/admin-access.spec.ts
// (the same admin UI, from every other role's seat, not just owner's).
import { test, expect } from '@playwright/test';
import { OWNER_EMAIL, OWNER_PASSWORD, acceptNextDialog, makeShotter, signIn, signOut } from './support';

const shot = makeShotter(__filename);

test('an owner signs in, manages a customer account end to end, and checks the audit trail', async ({ page }) => {
  const newUserEmail = `e2e-created-${Date.now()}@example.com`;
  const newUserPassword = 'created-password-1';

  await test.step('the sign-in screen', async () => {
    await page.goto('/admin/ui/login');
    await expect(page.locator('input[name="email"]')).toBeVisible();
    await shot(page, 'login');
  });

  await test.step('signing in as the bootstrap owner', async () => {
    await signIn(page, OWNER_EMAIL, OWNER_PASSWORD);
    await expect(page.getByText(OWNER_EMAIL).first()).toBeVisible();
    await shot(page, 'dashboard');
  });

  await test.step('opening the data browser', async () => {
    await page.click('a.nav-item[href="/admin/ui/data"]');
    await page.waitForURL('**/admin/ui/data');
    await shot(page, 'data-collections');
  });

  await test.step('opening the users panel (customer-plane accounts)', async () => {
    await page.click('a.nav-item[href="/admin/ui/users"]');
    await page.waitForURL('**/admin/ui/users');
    await shot(page, 'users-empty');
  });

  await test.step('creating a customer account', async () => {
    const form = page.locator('form[action="/admin/ui/users"]');
    await form.locator('input[name="email"]').fill(newUserEmail);
    await form.locator('input[name="password"]').fill(newUserPassword);
    await Promise.all([
      page.waitForURL('**/admin/ui/users'),
      form.locator('button[type="submit"]').click(),
    ]);
    await expect(page.getByText(newUserEmail)).toBeVisible();
    await shot(page, 'users-after-create');
  });

  await test.step("opening the new account's detail page", async () => {
    await page.click(`a:has-text("${newUserEmail}")`);
    await page.waitForURL('**/admin/ui/users/*');
    await expect(page.getByText('Active sessions')).toBeVisible();
    await shot(page, 'user-detail');
  });

  await test.step('signing the account out everywhere', async () => {
    acceptNextDialog(page);
    await Promise.all([
      page.waitForURL('**/admin/ui/users/*'),
      page.click('button:has-text("Sign out everywhere")'),
    ]);
    await shot(page, 'user-detail-after-revoke');
  });

  await test.step('deleting the account', async () => {
    acceptNextDialog(page);
    await Promise.all([
      page.waitForURL('**/admin/ui/users'),
      page.click('button:has-text("Delete account")'),
    ]);
    await expect(page.getByText(newUserEmail)).toHaveCount(0);
    await shot(page, 'users-after-delete');
  });

  await test.step('checking the audit log recorded every step', async () => {
    await page.click('a.nav-item[href="/admin/ui/audit-log"]');
    await page.waitForURL('**/admin/ui/audit-log');
    for (const event of ['user_create', 'user_sessions_revoked', 'user_delete']) {
      await expect(page.getByText(event).first()).toBeVisible();
    }
    await shot(page, 'audit-log');
  });

  await test.step('signing out', async () => {
    await signOut(page);
    await shot(page, 'logged-out');
  });
});
