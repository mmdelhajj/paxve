import { test, expect } from '@playwright/test';
import { apiCall, loadState, shot, watch } from './helpers';

// Module R: the Magic-style shell (ribbon, open-screen tabs, apps on/off) and the الجباية keyboard flow:
// type in the yellow filter row → ↓ → Enter → amount → Enter = saved, back in the search box.

test('Magic shell and collection screen', async ({ browser }) => {
  test.setTimeout(240_000);
  const st = loadState();
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  await ctx.addInitScript(() => {
    try {
      if (!sessionStorage.getItem('qa5')) {
        localStorage.setItem('paxve.lang', 'ar');
        localStorage.setItem('paxve.autoprint', '0'); // no print dialog in the test browser
        sessionStorage.setItem('paxve.wins', '[]');
        sessionStorage.setItem('qa5', '1');
      }
    } catch { /* */ }
  });
  const page = await ctx.newPage();
  const problems = watch(page);
  await page.goto('/');
  await apiCall(page.request, 'POST', '/auth/login', { email: st.email, password: st.password });
  await apiCall(page.request, 'PATCH', '/me', { locale: 'ar' });
  const cid = st.companyId!;
  await ctx.addInitScript((id) => { try { localStorage.setItem('paxve.company', id); } catch { /* */ } }, cid);
  const custs = await apiCall<{ id: string; name: string; due: { currency: string; due: string }[] }[]>(page.request, 'GET', `/companies/${cid}/customers?q=Rima`);
  const rima = custs.find((c) => c.name === 'Rima QA')!;
  expect(rima).toBeTruthy();

  // ribbon → tile → new window
  await page.goto('/#/dashboard');
  await page.reload();
  await expect(page.getByRole('tab', { name: 'الأعمال اليومية' })).toBeVisible({ timeout: 30_000 });
  await page.getByRole('tab', { name: 'الأعمال اليومية' }).click();
  await shot(page, 'magic-ribbon-ar-desktop');
  await page.getByRole('button', { name: 'الجباية', exact: true }).click();
  await expect(page.getByRole('tab', { name: /^الجباية/ })).toBeVisible();
  await expect(page.getByRole('tab', { name: /لوحة المعلومات/ })).toBeVisible(); // the dashboard window stays open

  // keyboard flow
  const search = page.locator('[data-filter="name"]').first();
  await expect(search).toBeFocused();
  await search.fill('Rima');
  await expect(page.getByRole('cell', { name: 'Rima QA' })).toBeVisible();
  await search.press('ArrowDown');
  await page.keyboard.press('Enter');
  const amount = page.locator('#mx-amt-base');
  await expect(amount).toBeFocused();
  const owed = Number(rima.due.find((d) => d.currency === 'USD')?.due ?? 0);
  if (owed > 0) await expect(amount).toHaveValue(String(Math.round(owed * 100) / 100));
  await amount.fill('20');
  await shot(page, 'magic-jibaya-ar-desktop');
  await amount.press('Enter');
  await expect(page.locator('.mx-flash.good').first()).toBeVisible();
  await expect(search).toBeFocused();
  const pays = await apiCall<{ rows: { id: string; amount: string; status: string; customer_name: string }[] }>(page.request, 'GET', `/companies/${cid}/payments?customer_id=${rima.id}`);
  const p20 = pays.rows.find((p) => p.amount === '20.0000' && p.status === 'posted');
  expect(p20).toBeTruthy();

  // تصحيح الدفعة: void by reversal, the amount comes back into the payment box
  await search.fill('Rima');
  await search.press('Enter');
  await expect(amount).toBeFocused();
  await page.getByRole('button', { name: 'تصحيح الدفعة' }).first().click();
  await page.getByRole('button', { name: 'إلغاء هذا الإيصال؟' }).click();
  await expect(page.locator('.mx-flash.good').first()).toContainText('أُلغي');
  await expect.poll(async () => (await apiCall<{ rows: { id: string; status: string }[] }>(page.request, 'GET', `/companies/${cid}/payments?customer_id=${rima.id}&kind=all`))
    .rows.find((p) => p.id === p20!.id)?.status).toBe('void');

  // a second window keeps the first one's state
  await page.getByRole('tab', { name: 'الأعمال اليومية' }).click();
  await page.getByRole('button', { name: 'عرض الإشتراكات', exact: true }).click();
  await expect(page.getByRole('tab', { name: /^عرض الإشتراكات/ })).toBeVisible();
  await expect(page.getByRole('cell', { name: 'Rima QA' }).first()).toBeVisible();
  await page.getByRole('tab', { name: /^الجباية/ }).click();
  await expect(page.locator('#mx-amt-base')).toBeEnabled(); // Rima is still the chosen subscriber

  // barcode mode
  await page.getByRole('tab', { name: 'جباية / باركود' }).click();
  const scan = page.getByLabel('رقم الإيصال');
  await expect(scan).toBeFocused();
  await scan.fill('99999999');
  await scan.press('Enter');
  await expect(page.locator('.mx-flash.bad').first()).toContainText('غير موجود');

  // apps: switch payroll off, its menu disappears; then everything back on
  await apiCall(page.request, 'PUT', `/companies/${cid}/apps`, { apps: ['msg', 'stk', 'pur', 'net', 'sat', 'pos', 'crm'] });
  await page.goto('/#/apps');
  await page.reload();
  await expect(page.locator('.mx-appc').first()).toBeVisible();
  await expect(page.getByRole('tab', { name: 'الموظفين' })).toHaveCount(0);
  await shot(page, 'magic-apps-ar-desktop');
  await page.getByRole('switch', { name: 'الموظفون و الرواتب' }).click({ force: true });
  await expect(page.getByRole('tab', { name: 'الموظفين' })).toBeVisible();

  // paper voucher with the amount in words
  await page.goto('/#/voucher/receipt');
  await page.getByLabel('مبلغ وقدره').fill('150');
  await expect(page.locator('.mx-words')).toContainText('مئة وخمسون');
  await shot(page, 'magic-voucher-ar-desktop');

  // English still works
  await apiCall(page.request, 'PATCH', '/me', { locale: 'en' });
  await page.evaluate(() => localStorage.setItem('paxve.lang', 'en'));
  await page.goto('/#/jibaya');
  await page.reload();
  await expect(page.getByRole('tab', { name: 'Daily work' })).toBeVisible();
  await expect(page.locator('[data-filter="name"]').first()).toBeVisible();
  await shot(page, 'magic-jibaya-en-desktop');

  // phone width: menus in one scrollable row, no page overflow
  await page.setViewportSize({ width: 400, height: 820 });
  await page.goto('/#/subscribers');
  await page.reload();
  await expect(page.getByRole('cell', { name: 'Rima QA' }).first()).toBeVisible();
  const ov = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(ov).toBeLessThanOrEqual(2);
  await shot(page, 'magic-subscribers-en-mobile');

  console.log('magic shell errors:', JSON.stringify(problems));
  expect(problems.network, problems.network.join('\n')).toEqual([]);
  expect(problems.console, problems.console.join('\n')).toEqual([]);
  await ctx.close();
});
