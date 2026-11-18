import { test, expect, type Page } from '@playwright/test';
import { apiCall, leakedKeys, overflow, shot, translationKeys, watch } from './helpers';

// Point of sale, end to end on the live site: a manager sets up a till, a cashier opens a session, scans and sells
// (mixed USD / LBP with change), sells while offline (queued, then sent once), returns a line and closes the drawer.
// Also sweeps the POS screens in Arabic / English / French at desktop and phone width.

const rnd = () => Math.random().toString(36).slice(2, 8);

test('point of sale', async ({ browser }) => {
  test.setTimeout(600_000);
  const keys = await translationKeys();
  const failures: string[] = [];

  // ---- manager: sign up, rate, stock, register, cashier ----
  const mctx = await browser.newContext();
  const mp = await mctx.newPage();
  await mp.goto('/');
  const email = `qa-pos-${rnd()}@example.com`;
  await apiCall(mp.request, 'POST', '/signup', {
    organisation: `QA POS ${rnd()}`, company_name: 'QA Shop', base_currency: 'USD', local_currency: 'LBP', template: 'lebanon',
    full_name: 'QA Manager', email, password: 'qa-manager-pass-1', locale: 'en',
  });
  const companies = await apiCall<{ id: string }[]>(mp.request, 'GET', '/companies');
  const cid = companies[0].id;
  const P = (p: string) => `/companies/${cid}${p}`;
  await apiCall(mp.request, 'POST', P('/rates'), { currency: 'LBP', date: '2026-01-01', rate: '89500' });
  const methods = await apiCall<{ id: string; name: string }[]>(mp.request, 'GET', P('/payment-methods'));
  const usd = methods.find((m) => m.name === 'Cash USD')!.id;
  const lbp = methods.find((m) => m.name === 'Cash LBP')!.id;
  const cat = await apiCall<{ id: string }>(mp.request, 'POST', P('/pos/categories'), { name: 'Drinks', name_i18n: { ar: 'مشروبات', fr: 'Boissons' }, color: 'blue' });
  const water = await apiCall<{ id: string }>(mp.request, 'POST', P('/items'), {
    code: 'WTR', name: 'Water 1.5L', name_i18n: { ar: 'مياه 1.5 ل' }, kind: 'stock', sale_price: '0.5', sale_currency: 'USD',
    barcode: '5280000000017', category_id: cat.id,
  });
  const juice = await apiCall<{ id: string }>(mp.request, 'POST', P('/items'), {
    code: 'JCE', name: 'Orange juice', name_i18n: { ar: 'عصير برتقال' }, kind: 'stock', sale_price: '1.25', sale_currency: 'USD', category_id: cat.id,
  });
  const sup = await apiCall<{ id: string }>(mp.request, 'POST', P('/suppliers'), { name: 'Drinks Co', currency: 'USD' });
  await apiCall(mp.request, 'POST', P('/bills'), {
    supplier_id: sup.id, bill_date: '2026-01-02', currency: 'USD',
    lines: [{ item_id: water.id, quantity: '100', unit_price: '0.2' }, { item_id: juice.id, quantity: '50', unit_price: '0.6' }],
  });
  const reg = await apiCall<{ id: string }>(mp.request, 'POST', P('/pos/registers'), { name: 'Front till', currency: 'USD', method_ids: [usd, lbp], cashier_refunds: true });
  const cashierEmail = `qa-cashier-${rnd()}@example.com`;
  await apiCall(mp.request, 'POST', '/users', { email: cashierEmail, full_name: 'Rana Cashier', role: 'cashier', locale: 'ar', password: 'qa-cashier-pass-1' });

  // ---- cashier ----
  const cctx = await browser.newContext({ viewport: { width: 1280, height: 820 } });
  await cctx.addInitScript((id) => { try { localStorage.setItem('paxve.company', id); } catch { /* */ } }, cid);
  const page = await cctx.newPage();
  const problems = watch(page);
  await page.goto('/');
  await apiCall(page.request, 'POST', '/auth/login', { email: cashierEmail, password: 'qa-cashier-pass-1' });
  await apiCall(page.request, 'PATCH', '/me', { locale: 'en' });
  await page.goto('/#/dashboard');
  await page.reload();
  // a cashier lands on the till, the register is picked automatically (only one), no session yet
  await expect(page.getByText('Open the session').first()).toBeVisible();
  await shot(page, 'pos-open-en-desktop');
  await page.getByLabel('Cash USD').fill('20');
  await page.getByRole('button', { name: 'Open the session' }).click();
  await expect(page.getByPlaceholder('Search or scan a barcode')).toBeVisible();

  // scan the water barcode (keyboard-wedge scanner types into the search field and presses Enter) x2, click the juice
  const search = page.getByPlaceholder('Search or scan a barcode');
  for (let i = 0; i < 2; i++) {
    await search.fill('5280000000017');
    await search.press('Enter');
  }
  await page.getByText('Orange juice').first().click();
  await expect(page.getByText('2.25 USD').first()).toBeVisible(); // 2 x 0.5 + 1.25
  await shot(page, 'pos-cart-en-desktop');
  await page.getByRole('button', { name: 'Pay', exact: true }).click();
  // pay everything in LBP: "Exact" fills 201,375 LBP rounded up; change comes back in LBP
  await page.getByLabel('Cash LBP').fill('250000');
  await expect(page.getByText(/48,625|48,624/).first()).toBeVisible();
  await shot(page, 'pos-pay-en-desktop');
  await page.getByRole('button', { name: 'Confirm sale' }).click();
  await expect(page.getByRole('button', { name: 'New sale' })).toBeVisible();
  await shot(page, 'pos-receipt-en-desktop');
  await page.getByRole('button', { name: 'New sale' }).click();

  // offline sale: kept on the device, sent once when back online
  await search.fill('5280000000017');
  await search.press('Enter');
  await cctx.setOffline(true);
  await page.getByRole('button', { name: 'Pay', exact: true }).click();
  await page.getByRole('button', { name: 'Exact' }).first().click();
  await page.getByRole('button', { name: 'Confirm sale' }).click();
  await expect(page.getByText(/Saved on the phone/).first()).toBeVisible();
  await page.getByRole('button', { name: 'New sale' }).click();
  await cctx.setOffline(false);
  await page.evaluate(() => window.dispatchEvent(new Event('online')));
  await expect.poll(async () => (await apiCall<unknown[]>(page.request, 'GET', P(`/pos/orders?limit=50`))).length, { timeout: 60_000 }).toBe(2);

  // return the juice of the first sale (cashier refunds allowed on this till), paid back in USD
  await page.getByRole('button', { name: 'Sales' }).first().click();
  const orders = page.locator('.mantine-Drawer-body');
  await orders.getByText('#1', { exact: true }).click();
  await orders.getByRole('button', { name: 'Return' }).click();
  await orders.getByRole('button', { name: 'All' }).nth(1).click();
  await orders.getByRole('button', { name: 'Confirm return' }).click();
  await expect(page.getByText('Return receipt').first()).toBeVisible();
  await page.getByRole('button', { name: 'New sale' }).click();
  await page.keyboard.press('Escape');

  // drawer: USD 20 - 1.25 + 0.5 = 19.25 ; LBP 250,000 - change
  const regs = await apiCall<{ id: string; open_session: { id: string } | null }[]>(page.request, 'GET', P('/pos/registers'));
  const sid = regs.find((r) => r.id === reg.id)!.open_session!.id;
  const z = await apiCall<{ drawer: { method_id: string; expected: string }[] }>(page.request, 'GET', P(`/pos/sessions/${sid}`));
  const expUSD = Number(z.drawer.find((d) => d.method_id === usd)!.expected);
  expect(expUSD).toBeCloseTo(19.25, 4);
  const expLBP = z.drawer.find((d) => d.method_id === lbp)!.expected;

  // close: count the drawer (1 USD short)
  await page.getByRole('button', { name: 'menu' }).click();
  await page.getByRole('menuitem', { name: 'Close the session' }).click();
  const dlg = page.getByRole('dialog');
  await dlg.getByLabel(/Cash USD/).fill(String(expUSD - 1));
  await dlg.getByLabel(/Cash LBP/).fill(String(Number(expLBP)));
  await shot(page, 'pos-close-en-desktop');
  await dlg.getByRole('button', { name: 'Close the session' }).click();
  await expect(page.getByText('Z report').first()).toBeVisible();
  await shot(page, 'pos-z-en-desktop');

  // the books: balanced, stock = ledger, 1 USD over/short
  const tb = await apiCall<{ total_debit: string; total_credit: string }>(mp.request, 'GET', P('/reports/trial-balance'));
  expect(tb.total_debit).toBe(tb.total_credit);
  const val = await apiCall<{ total: string; ledger_total: string }>(mp.request, 'GET', P('/reports/stock-valuation'));
  expect(Number(val.total)).toBeCloseTo(Number(val.ledger_total), 4);

  // ---- sweep: till screens (cashier) and office POS pages, 3 languages, desktop + phone ----
  await page.getByRole('dialog').getByRole('button', { name: 'Close' }).last().click();
  await expect(page.getByText('Open the session').first()).toBeVisible();
  await apiCall(page.request, 'POST', P('/pos/sessions'), { register_id: reg.id, opening: [] });
  const sweep = async (pg: Page, who: string, routes: string[], lang: string, width: number) => {
    await pg.setViewportSize({ width, height: width > 500 ? 860 : 820 });
    for (const r of routes) {
      const before = problems.console.length + problems.network.length;
      await pg.goto(`/#/${r}`);
      await pg.reload();
      await pg.waitForLoadState('networkidle').catch(() => {});
      await pg.waitForTimeout(500);
      const leaks = await leakedKeys(pg, keys);
      if (leaks.length) failures.push(`${who} ${lang} ${width} ${r}: raw keys ${leaks.join(', ')}`);
      if (width < 500) {
        const ov = await overflow(pg);
        if (ov > 2) failures.push(`${who} ${lang} ${width} ${r}: overflow ${ov}px`);
      }
      if (pg === page) {
        const after = problems.console.length + problems.network.length;
        if (after > before) failures.push(`${who} ${lang} ${width} ${r}: ${[...problems.console, ...problems.network].slice(before - after).join(' | ')}`);
      }
      await shot(pg, `${who}-${r.replace(/\W/g, '')}-${lang}-${width < 500 ? 'mobile' : 'desktop'}`);
    }
  };
  for (const lang of ['ar', 'en', 'fr']) {
    await apiCall(page.request, 'PATCH', '/me', { locale: lang });
    for (const w of [1280, 400]) {
      await sweep(page, 'cashier', ['dashboard'], lang, w);
      // a product in the cart so the cart / bottom bar is shown
      await page.getByText(lang === 'ar' ? 'مياه 1.5 ل' : 'Water 1.5L').first().click().catch(() => {});
      await shot(page, `cashier-cart-${lang}-${w < 500 ? 'mobile' : 'desktop'}`);
    }
  }
  await apiCall(mp.request, 'PATCH', '/me', { locale: 'ar' });
  const mProblems = watch(mp);
  for (const lang of ['ar', 'en', 'fr']) {
    await apiCall(mp.request, 'PATCH', '/me', { locale: lang });
    for (const w of [1366, 400]) await sweep(mp, 'office', ['pos-setup', 'pos-sessions', 'pos-report', 'items'], lang, w);
  }
  if (mProblems.console.length || mProblems.network.length) failures.push('office: ' + [...mProblems.console, ...mProblems.network].join(' | '));

  // forbidden for the cashier: the back office API
  const r = await page.request.fetch('/api/v1' + P('/accounts'));
  expect(r.status()).toBe(403);

  // clean up: deactivate the QA tenant is done by the platform admin (see docs); leave it inactive-able
  console.log('POS FAILURES:\n' + failures.join('\n'));
  expect(failures, failures.join('\n')).toEqual([]);
});
