import { test, expect, type Page } from '@playwright/test';
import { apiCall, saveState, shot, watch, type State } from './helpers';

// A real new customer's first day: sign up → set up → bill → collect (office + offline collector) → reports.

const stamp = Date.now().toString(36);
const state: State = { email: `qa-${stamp}@example.com`, password: `Qa-${stamp}-pass!` };

async function openDialogButton(page: Page, name: string) {
  await page.getByRole('dialog').getByRole('button', { name, exact: true }).click();
}

test.describe.serial('new business journey', () => {
  let page: Page;
  let problems: ReturnType<typeof watch>;

  test.beforeAll(async ({ browser }) => {
    const ctx = await browser.newContext();
    await ctx.addInitScript(() => { try { if (!sessionStorage.getItem('qa')) { localStorage.setItem('paxve.lang', 'en'); sessionStorage.setItem('qa', '1'); } } catch { /* */ } });
    page = await ctx.newPage();
    problems = watch(page);
  });

  test.afterAll(() => {
    saveState(state);
    console.log('journey console errors:', JSON.stringify(problems.console));
    console.log('journey network failures:', JSON.stringify(problems.network));
  });

  test('landing → sign up', async () => {
    await page.goto('/');
    await expect(page.getByRole('link', { name: /start free trial/i }).first()).toBeVisible();
    await shot(page, 'landing-en-desktop');
    await page.goto('/#/signup');
    await page.getByLabel('Business name').fill(`QA Generators ${stamp}`);
    await page.getByLabel('Full name').fill('QA Owner');
    await page.getByLabel('Email').fill(state.email);
    await page.getByLabel(/New password/).fill(state.password);
    await page.getByRole('button', { name: 'Create account and start' }).click();
    await expect(page.getByText('Getting started')).toBeVisible({ timeout: 30_000 });
    await shot(page, 'dashboard-onboarding-en');
  });

  test('exchange rate, plans', async () => {
    await page.goto('/#/rates');
    await page.getByLabel(/1 USD = how many/).fill('89500');
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(page.getByRole('cell', { name: '89500' }).first()).toBeVisible();

    await page.goto('/#/billing-setup');
    await page.getByRole('button', { name: 'New plan' }).click();
    const d = page.getByRole('dialog');
    await d.getByLabel(/^Name/).fill('10A');
    await d.getByLabel('Monthly fee').fill('45');
    await openDialogButton(page, 'Save');
    await expect(page.getByRole('cell', { name: '10A', exact: true })).toBeVisible();
    // metered plan through the API (same endpoint the UI uses)
    const companies = await apiCall<{ id: string }[]>(page.request, 'GET', '/companies');
    state.companyId = companies[0].id;
    await apiCall(page.request, 'POST', `/companies/${state.companyId}/plans`,
      { name: 'Metered', billing_type: 'metered', currency: 'USD', base_fee: '4.3', unit_price: '0.54' });
  });

  test('customer + subscription + reading', async () => {
    await page.goto('/#/customers');
    await page.getByRole('button', { name: 'New customer' }).click();
    await page.getByRole('dialog').getByLabel(/^Name/).fill('Ahmad QA');
    await page.getByRole('dialog').getByLabel(/^Phone/).fill('03123456');
    await openDialogButton(page, 'Save');
    await expect(page.getByRole('heading', { name: 'Ahmad QA' })).toBeVisible();
    await page.getByRole('button', { name: 'Add subscription' }).click();
    await page.getByRole('dialog').getByLabel(/^Plan/).click();
    await page.getByRole('option', { name: /^10A/ }).click();
    await openDialogButton(page, 'Save');
    await expect(page.getByRole('cell', { name: /10A/ }).first()).toBeVisible();

    const cid = state.companyId!;
    const plans = await apiCall<{ id: string; name: string }[]>(page.request, 'GET', `/companies/${cid}/plans`);
    const c2 = await apiCall<{ id: string }>(page.request, 'POST', `/companies/${cid}/customers`, { name: 'Rima QA' });
    await apiCall(page.request, 'POST', `/companies/${cid}/customers/${c2.id}/subscriptions`,
      { plan_id: plans.find((p) => p.name === 'Metered')!.id, meter_no: 'QA-1', initial_reading: '1000', start_date: '2026-01-01' });

    await page.goto('/#/readings');
    const input = page.locator('input[data-row="0"]');
    await input.fill('1150');
    await page.getByRole('button', { name: /Save readings/ }).click();
    await expect(page.getByText(/readings saved/i)).toBeVisible();
  });

  test('monthly billing run', async () => {
    await page.goto('/#/invoices');
    await page.getByRole('button', { name: 'Create monthly bills' }).first().click();
    await openDialogButton(page, 'Create monthly bills');
    await openDialogButton(page, 'Create monthly bills'); // confirm
    await expect(page.getByText('2 bills created')).toBeVisible();
    await openDialogButton(page, 'Close');
    await expect(page.getByText('Ahmad QA').first()).toBeVisible();
  });

  test('payment in USD + LBP with change, receipt', async () => {
    await page.goto('/#/customers');
    // Magic-style grid: a click selects the row, a double-click (or Enter) opens the subscriber
    await page.locator('.mx-pane:not([hidden])').getByText('Ahmad QA').first().dblclick();
    await page.getByRole('button', { name: 'Receive payment' }).click();
    const d = page.getByRole('dialog');
    await d.getByRole('button', { name: 'Add another currency' }).click();
    const amounts = d.locator('input[placeholder="0"]');
    await amounts.nth(0).fill('20');
    await amounts.nth(1).fill('3000000');
    await expect(d.getByText(/LBP/).first()).toBeVisible();
    await shot(page, 'receive-payment-en');
    await d.getByRole('button', { name: 'Receive payment', exact: true }).click();
    await expect(page.getByRole('dialog').getByText('Receipt').first()).toBeVisible();
    await shot(page, 'receipt-en');
    const cid = state.companyId!;
    const pays = await apiCall<{ rows: { amount: string; change_amount: string; change_currency: string }[] }>(page.request, 'GET', `/companies/${cid}/payments`);
    expect(pays.rows[0].amount).toBe('45.0000');
    // 20 + 3,000,000/89,500 = 53.5196 → change 8.5196 USD ≈ 762,500 LBP (rounded down by the box rule)
    expect(Number(pays.rows[0].change_amount)).toBeGreaterThan(700000);
    expect(pays.rows[0].change_currency).toBe('LBP');
    await page.keyboard.press('Escape');
  });

  test('reports render and balance', async () => {
    const cid = state.companyId!;
    for (const r of ['profit-loss', 'balance-sheet', 'trial-balance', 'aging', 'collections']) {
      await page.goto(`/#/${r}`);
      await expect(page.locator('.mx-pane:not([hidden]) .mx-box h3').first()).toBeVisible();
    }
    const tb = await apiCall<{ total_debit: string; total_credit: string }>(page.request, 'GET', `/companies/${cid}/reports/trial-balance`);
    expect(tb.total_debit).toBe(tb.total_credit);
  });

  test('collector works offline and syncs', async ({ browser }) => {
    const cid = state.companyId!;
    state.collectorEmail = `qa-col-${stamp}@example.com`;
    state.collectorPassword = `Col-${stamp}-pass!`;
    await apiCall(page.request, 'POST', '/users', { email: state.collectorEmail, full_name: 'QA Collector', role: 'collector', locale: 'ar', password: state.collectorPassword });
    const users = await apiCall<{ id: string; email: string }[]>(page.request, 'GET', '/users');
    const colId = users.find((u) => u.email === state.collectorEmail)!.id;
    const area = await apiCall<{ id: string }>(page.request, 'POST', `/companies/${cid}/areas`, { name: 'QA Area', collector_id: colId });
    const custs = await apiCall<{ id: string; name: string }[]>(page.request, 'GET', `/companies/${cid}/customers`);
    const rima = custs.find((c) => c.name === 'Rima QA')!;
    await apiCall(page.request, 'PATCH', `/companies/${cid}/customers/${rima.id}`, { area_id: area.id });

    const ctx = await browser.newContext({ viewport: { width: 400, height: 820 } });
    const cp = await ctx.newPage();
    const cprob = watch(cp);
    await cp.goto('/#/login');
    await cp.locator('input[type="email"]').fill(state.collectorEmail);
    await cp.locator('input[type="password"]').fill(state.collectorPassword);
    await cp.locator('button[type="submit"]').click();
    await expect(cp.getByText('Rima QA')).toBeVisible({ timeout: 30_000 });
    await shot(cp, 'collector-ar-mobile');

    await ctx.setOffline(true);
    await cp.getByText('Rima QA').click();
    await cp.getByRole('dialog').locator('input[placeholder="0"]').first().fill('10');
    await cp.getByRole('dialog').getByRole('button').filter({ hasText: /تأكيد|Confirm/ }).click();
    await expect(cp.getByRole('dialog').getByText(/الهاتف|phone/i).first()).toBeVisible();
    await shot(cp, 'collector-offline-receipt-ar-mobile');
    const queued = await cp.evaluate(() => JSON.parse(localStorage.getItem('paxve.outbox') || '[]').length);
    expect(queued).toBe(1);
    await cp.keyboard.press('Escape');

    await ctx.setOffline(false);
    await expect.poll(async () => cp.evaluate(() => JSON.parse(localStorage.getItem('paxve.outbox') || '[]').length), { timeout: 60_000 }).toBe(0);
    const pays = await apiCall<{ rows: { customer_name: string; amount: string; received_by: string }[] }>(page.request, 'GET', `/companies/${cid}/payments`);
    expect(pays.rows.some((p) => p.customer_name === 'Rima QA' && p.amount === '10.0000' && p.received_by === 'QA Collector')).toBeTruthy();
    console.log('collector console errors:', JSON.stringify(cprob.console), 'network:', JSON.stringify(cprob.network));
    await ctx.close();
  });

  test('no unexpected errors during the journey', async () => {
    expect(problems.network, problems.network.join('\n')).toEqual([]);
    expect(problems.console, problems.console.join('\n')).toEqual([]);
  });
});
