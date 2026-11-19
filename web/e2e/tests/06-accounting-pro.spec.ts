import { test, expect } from '@playwright/test';
import { apiCall, loadState, shot, watch } from './helpers';

// Module S: professional accounting screens. A cheque received through the screen, deposited and collected;
// the "?" help panel; drill-down from the trial balance to the ledger; every new screen opens without errors.

test('Accounting pro: cheques, help, drill-down, new screens', async ({ browser }) => {
  test.setTimeout(240_000);
  const st = loadState();
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  await ctx.addInitScript(() => {
    try {
      if (!sessionStorage.getItem('qa6')) {
        localStorage.setItem('paxve.lang', 'en');
        sessionStorage.setItem('paxve.wins', '[]');
        sessionStorage.setItem('qa6', '1');
      }
    } catch { /* */ }
  });
  const page = await ctx.newPage();
  const problems = watch(page);
  await page.goto('/');
  await apiCall(page.request, 'POST', '/auth/login', { email: st.email, password: st.password });
  await apiCall(page.request, 'PATCH', '/me', { locale: 'en' });
  const cid = st.companyId!;
  await ctx.addInitScript((id) => { try { localStorage.setItem('paxve.company', id); } catch { /* */ } }, cid);
  const custs = await apiCall<{ id: string; name: string }[]>(page.request, 'GET', `/companies/${cid}/customers?q=Rima`);
  const rima = custs.find((c) => c.name === 'Rima QA')!;
  const boxes = await apiCall<{ id: string; kind: string; is_active: boolean; currency: string; user_id: string | null }[]>(page.request, 'GET', `/companies/${cid}/payment-methods`);
  const bank = boxes.find((b) => b.kind === 'bank' && b.is_active)!;

  // a cheque through the API, its steps through the screen
  const ch = await apiCall<{ cheque: { id: string } }>(page.request, 'POST', `/companies/${cid}/cheques`, {
    direction: 'in', cheque_no: 'E2E-' + Date.now() % 100000, bank_name: 'BLOM', currency: bank.currency, amount: '5', customer_id: rima.id });
  await page.goto(`/#/cheques/${ch.cheque.id}`);
  await page.reload();
  await expect(page.getByRole('button', { name: 'Deposit at the bank' })).toBeVisible({ timeout: 30_000 });
  await page.getByRole('button', { name: 'Deposit at the bank' }).click();
  await page.locator('#op-box').selectOption(bank.id);
  await page.locator('.mx-opbox .mx-btn.main').click();
  await expect(page.locator('.mx-kv').getByText('Deposited')).toBeVisible();
  await page.getByRole('button', { name: 'Collected (money in)' }).click();
  await page.locator('.mx-opbox .mx-btn.main').click();
  await expect(page.locator('.mx-kv').getByText('Collected')).toBeVisible();
  await shot(page, 'accpro-cheque-en-desktop');

  // help panel
  await page.locator('.mx-pane:not([hidden]) .mx-help').first().click();
  await expect(page.locator('.mantine-Drawer-body')).toContainText('cheques in hand');
  await page.keyboard.press('Escape');

  // trial balance → double-click → ledger of that account
  await page.goto('/#/trial-balance');
  await page.reload();
  const firstRow = page.locator('.mx-pane:not([hidden]) .mx-grid tbody tr[data-key]').first();
  await expect(firstRow).toBeVisible({ timeout: 30_000 });
  await firstRow.dblclick();
  await expect(page).toHaveURL(/#\/ledger\/[0-9a-f-]{36}~/);

  for (const p of ['exchange', 'fiscal-years', 'cost-centers', 'budgets', 'cash-flow', 'compare', 'opening-balances', 'profit-loss',
    'balance-sheet', 'aging', 'collections', 'invoices', 'customers', 'print-bills']) {
    await page.goto('/#/' + p);
    await page.reload();
    await expect(page.locator('.mx-pane:not([hidden]) .mx-help').first()).toBeVisible({ timeout: 30_000 });
  }
  await page.goto('/#/dashboard');
  await page.reload();
  await expect(page.locator('.mx-owner')).toBeVisible({ timeout: 30_000 });
  await shot(page, 'accpro-owner-en-desktop');
  expect(problems.network, problems.network.join('\n')).toEqual([]);
  expect(problems.console, problems.console.join('\n')).toEqual([]);
  await ctx.close();
});
