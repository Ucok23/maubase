// The rest of the owner-only surface admin-ui-flow.spec.ts doesn't touch:
// Members (adding/removing another owner-plane account), Maintenance,
// defining a table live from the UI, CRUD on its rows, and SQL Studio —
// including confirming a DDL statement run there shows up in the data
// browser immediately (ADMINUI-18), the same live-reload guarantee the
// create-table form itself makes (ADMINUI-21).
import { test, expect } from '@playwright/test';
import { OWNER_EMAIL, OWNER_PASSWORD, acceptNextDialog, createOwnerAccount, makeShotter, signIn, signOut } from './support';

const shot = makeShotter(__filename);

test('an owner tours Members, Maintenance, a live-created table, and SQL Studio', async ({ page }) => {
  const tempAdminEmail = `e2e-temp-admin-${Date.now()}@example.com`;
  const tableName = 'e2e_widgets';

  await test.step('signing in as the bootstrap owner', async () => {
    await signIn(page, OWNER_EMAIL, OWNER_PASSWORD);
    await shot(page, 'dashboard');
  });

  await test.step('opening Members', async () => {
    await page.click('a.nav-item[href="/admin/ui/owners"]');
    await page.waitForURL('**/admin/ui/owners');
    await expect(page.getByText(OWNER_EMAIL).first()).toBeVisible();
    await shot(page, 'members');
  });

  await test.step('adding another owner-plane account', async () => {
    await createOwnerAccount(page, tempAdminEmail, 'temp-admin-password-1', 'admin');
    await expect(page.getByText(tempAdminEmail)).toBeVisible();
    await shot(page, 'members-after-create');
  });

  await test.step('removing it again', async () => {
    const row = page.locator('tr', { hasText: tempAdminEmail });
    acceptNextDialog(page);
    await row.locator('button:has-text("Delete")').click();
    await expect(page.getByText(tempAdminEmail)).toHaveCount(0);
    await shot(page, 'members-after-delete');
  });

  await test.step('opening Maintenance', async () => {
    await page.click('a.nav-item[href="/admin/ui/maintenance"]');
    await page.waitForURL('**/admin/ui/maintenance');
    await shot(page, 'maintenance');
  });

  await test.step('purging expired sessions', async () => {
    await page.click('button:has-text("Purge expired sessions")');
    await expect(page.getByText('customer session')).toBeVisible();
    await shot(page, 'maintenance-after-purge');
  });

  await test.step('opening the new-table form', async () => {
    await page.click('a.nav-item[href="/admin/ui/data"]');
    await page.waitForURL('**/admin/ui/data');
    await page.click('a:has-text("New table")');
    await page.waitForURL('**/admin/ui/tables/new');
    await shot(page, 'new-table-form');
  });

  await test.step('defining a table live, no restart', async () => {
    await page.fill('input[name="name"]', tableName);
    await page.fill('input[name="col_name_0"]', 'label');
    await Promise.all([
      page.waitForURL(`**/admin/ui/data/${tableName}`),
      page.click('button:has-text("Create table")'),
    ]);
    await shot(page, 'table-created');
  });

  await test.step('creating a row', async () => {
    await page.fill('input[name="label"]', 'First widget');
    await Promise.all([
      page.waitForURL(`**/admin/ui/data/${tableName}`),
      page.click('button:has-text("Create")'),
    ]);
    await expect(page.getByText('First widget')).toBeVisible();
    await shot(page, 'row-created');
  });

  await test.step('editing the row', async () => {
    await page.click('a:has-text("Edit")');
    await page.waitForURL(`**/admin/ui/data/${tableName}/*/edit`);
    await page.fill('input[name="label"]', 'Updated widget');
    await Promise.all([
      page.waitForURL(`**/admin/ui/data/${tableName}`),
      page.click('button:has-text("Save changes")'),
    ]);
    await expect(page.getByText('Updated widget')).toBeVisible();
    await shot(page, 'row-edited');
  });

  await test.step('opening SQL Studio', async () => {
    await page.click('a.nav-item[href="/admin/ui/sql"]');
    await page.waitForURL('**/admin/ui/sql');
    await shot(page, 'sql-studio');
  });

  await test.step('running a SELECT', async () => {
    await page.fill('textarea[name="query"]', 'select 1 as answer');
    await page.click('button:has-text("Run query")');
    await expect(page.getByText('1 row(s) returned')).toBeVisible();
    await shot(page, 'sql-select-result');
  });

  await test.step('running DDL that takes effect immediately', async () => {
    await page.fill('textarea[name="query"]', `alter table ${tableName} add column note text`);
    await page.click('button:has-text("Run query")');
    await expect(page.getByText('row(s) affected')).toBeVisible();
    await shot(page, 'sql-ddl-result');
  });

  await test.step('confirming the new column is live in the data browser', async () => {
    await page.click('a.nav-item[href="/admin/ui/data"]');
    await page.waitForURL('**/admin/ui/data');
    await page.click(`a:has-text("${tableName}")`);
    await page.waitForURL(`**/admin/ui/data/${tableName}`);
    // The row created earlier is still here — proof this is the same
    // live table, not a fresh one — with a header for the column SQL
    // Studio's ALTER TABLE just added.
    await expect(page.getByText('Updated widget')).toBeVisible();
    await expect(page.locator('th', { hasText: 'note' })).toBeVisible();
    await shot(page, 'data-browser-shows-new-column');
  });

  await test.step('deleting the row', async () => {
    const row = page.locator('tr', { hasText: 'Updated widget' });
    acceptNextDialog(page);
    await row.locator('button:has-text("Delete")').click();
    await expect(page.getByText('Updated widget')).toHaveCount(0);
    await shot(page, 'row-deleted');
  });

  await test.step('signing out', async () => {
    await signOut(page);
    await shot(page, 'logged-out');
  });
});
