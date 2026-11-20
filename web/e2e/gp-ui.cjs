// Live check of the Odoo-gap screens on paxve.com with a fresh QA organisation: credit limit, chatter, activities,
// analysis, repairs, instalments, templates, fiscal positions. Screenshots, console errors, sideways scrolling, raw keys.
const { chromium, request } = require('playwright');
const base = 'https://paxve.com';
const H = { 'X-Requested-With': 'paxve', 'Content-Type': 'application/json' };
let fails = 0;
const ok = (name, cond, extra = '') => { console.log(`${cond ? 'PASS' : 'FAIL'} ${name} ${extra}`); if (!cond) fails++; };

(async () => {
  const sfx = Date.now().toString(36).slice(-5);
  const orgName = `QA Gaps ${sfx}`;
  const email = `qa-gaps-${sfx}@example.com`, pass = 'qa-gaps-pass-1';
  const qa = await request.newContext({ baseURL: base, extraHTTPHeaders: H });
  let r = await qa.post('/api/v1/signup', { data: { organisation: orgName, company_name: orgName, base_currency: 'USD', local_currency: 'LBP', template: 'lebanon',
    full_name: 'QA Owner', email, password: pass, locale: 'en' } });
  ok('signup', r.status() === 201);
  const cid = (await (await qa.get('/api/v1/companies')).json())[0].id;
  const P = `/api/v1/companies/${cid}`;
  await qa.post(`${P}/rates`, { data: { currency: 'LBP', date: '2026-01-01', rate: '89500' } });
  await qa.put(`${P}/apps`, { data: { apps: ['gen', 'acc', 'col', 'msg', 'stk', 'pur', 'pos', 'hr', 'crm', 'svc'] } });
  const cust = await (await qa.post(`${P}/customers`, { data: { name: 'Rima Haddad', phone: '70123456' } })).json();
  const cust2 = await (await qa.post(`${P}/customers`, { data: { name: 'Karim Aoun', phone: '70999888' } })).json();
  for (const [c, d, a] of [[cust.id, '2026-09-05', '120'], [cust.id, '2026-10-05', '80'], [cust2.id, '2026-11-02', '60']]) {
    r = await qa.post(`${P}/invoices`, { data: { customer_id: c, currency: 'USD', issue_date: d, lines: [{ description: 'Generator subscription', quantity: '1', unit_price: a }] } });
  }
  ok('invoices', r.status() === 201);
  await qa.put(`${P}/credit-settings`, { data: { policy: 'warn', overdue_days: 45 } });
  r = await qa.put(`${P}/customers/${cust.id}/credit`, { data: { credit_limit: '250' } }); ok('credit limit', r.ok());
  r = await qa.post(`${P}/invoices`, { data: { customer_id: cust.id, currency: 'USD', lines: [{ description: 'Big', quantity: '1', unit_price: '100' }] } });
  ok('credit warning (409)', r.status() === 409 && (await r.json()).error === 'credit_limit');
  r = await qa.post(`${P}/invoices`, { headers: { ...H, 'X-Credit-Confirm': '1' }, data: { customer_id: cust.id, currency: 'USD', lines: [{ description: 'Big', quantity: '1', unit_price: '100' }] } });
  ok('credit confirmed', r.status() === 201);
  const vat = (await (await qa.get(`${P}/tax-rates`)).json())[0];
  r = await qa.post(`${P}/fiscal-positions`, { data: { name: 'NGO — VAT exempt', kind: 'exempt', tax_map: [{ from: vat.id, to: '' }] } }); ok('fiscal position', r.status() === 201);
  r = await qa.post(`${P}/quote-templates`, { data: { name: 'Generator 60 kVA installation', currency: 'USD', validity_days: 15, terms: '50% upfront',
    lines: [{ description: 'Cabling and box', quantity: '1', unit_price: '200' }, { description: 'Breaker 32A', quantity: '2', unit_price: '15' }] } });
  ok('quote template', r.status() === 201);
  r = await qa.post(`${P}/instalments`, { data: { customer_id: cust2.id, currency: 'USD', issue_date: '2026-08-01', months: 4, down_payment: '100', fee_percent: '5',
    lines: [{ description: 'Inverter 5 kVA', quantity: '1', unit_price: '900' }] } });
  const inst = await r.json(); ok('instalment plan', r.status() === 201, JSON.stringify(inst.plan?.total));
  const item = await (await qa.post(`${P}/items`, { data: { code: 'RTR', name: 'Router AX3000', kind: 'stock', sale_price: '60', sale_currency: 'USD' } })).json();
  await qa.put(`${P}/items/${item.id}/stock-settings`, { data: { warranty_months: 12 } });
  r = await qa.post(`${P}/repairs`, { data: { customer_id: cust.id, item_id: item.id, device: 'Router AX3000', serial: 'SN-77AX', fault: 'Restarts every hour',
    sale_date: '2026-07-01', promised_date: '2026-11-25' } });
  const rep = await r.json(); ok('repair received (under warranty)', r.status() === 201 && rep.repair.under_warranty === true);
  await qa.put(`${P}/repairs/${rep.repair.id}/lines`, { data: { lines: [{ kind: 'labour', description: 'Firmware + bench test', quantity: '1', unit_price: '15' }] } });
  r = await qa.post(`${P}/chatter`, { data: { type: 'customer', id: cust.id, body: 'Customer asked to move the meter next week.' } }); ok('comment', r.status() === 201);
  r = await qa.post(`${P}/record-activities`, { data: { entity_type: 'customer', entity_id: cust.id, kind: 'call', summary: 'Call about the late invoice', due_date: '2026-11-10' } });
  ok('activity', r.status() === 201);
  r = await qa.post(`${P}/analysis/reports`, { data: { name: 'Sales by customer and month', shared: true,
    spec: { source: 'sales', rows: ['customer'], cols: ['date:month'], measure: 'amount', from: '2026-01-01', to: '2026-12-31', chart: 'bar' } } });
  ok('saved analysis', r.status() === 201);
  const ag = await (await qa.get(`${P}/reports/aging`)).json();
  ok('aging has the instalment customer', ag.rows.some((x) => x.customer_id === cust2.id));

  const runs = [
    { lang: 'en', w: 1440, h: 900, tag: 'en' },
    { lang: 'ar', w: 1440, h: 900, tag: 'ar' },
    { lang: 'fr', w: 1440, h: 900, tag: 'fr' },
    { lang: 'ar', w: 400, h: 860, tag: 'ar-phone' },
  ];
  const browser = await chromium.launch();
  for (const run of runs) {
    const ctx = await browser.newContext({ viewport: { width: run.w, height: run.h }, locale: run.lang });
    const page = await ctx.newPage();
    page.on('dialog', (d) => d.accept().catch(() => {}));
    const errors = [];
    page.on('console', (m) => { if (m.type() === 'error' && !/401|402|403|404|409|Failed to load resource/.test(m.text())) errors.push(m.text()); });
    page.on('pageerror', (e) => errors.push(String(e)));
    await page.goto(`${base}/#/login`);
    await page.waitForSelector('input[type=password]');
    await page.fill('input[type=email], input[name=email]', email);
    await page.fill('input[type=password]', pass);
    await page.keyboard.press('Enter');
    await page.waitForTimeout(2500);
    await page.evaluate(async (l) => { await fetch('/api/v1/me', { method: 'PATCH', credentials: 'include', headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'paxve' }, body: JSON.stringify({ locale: l }) }); }, run.lang);
    await page.reload(); await page.waitForTimeout(1500);
    const check = async (name) => {
      const raw = await page.evaluate(() => (document.body.innerText.match(/\b(gp|errors|mx|sec)\.[a-z][\w.]+/g) || []).slice(0, 5).join(','));
      if (raw) errors.push('untranslated: ' + raw);
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
      await page.screenshot({ path: `/out/g-${run.tag}-${name}.png` });
      ok(`${run.tag} ${name}`, overflow <= 2 && errors.length === 0, `overflow=${overflow} ${errors.join(' | ').slice(0, 300)}`);
      errors.length = 0;
    };
    for (const m of ['inbox', 'analysis', 'repairs', 'instalments', 'templates', 'fiscal', 'credit']) {
      await page.goto(`${base}/#/gaps/${m}`); await page.waitForTimeout(1800);
      if (m === 'analysis') { await page.locator('.mx-pay select').nth(6).selectOption({ index: 1 }).catch(() => {}); await page.waitForTimeout(1500); }
      if (m === 'repairs' || m === 'instalments' || m === 'templates' || m === 'fiscal') {
        await page.locator('.mx-grid tbody tr').first().click().catch(() => {}); await page.waitForTimeout(1200);
      }
      await check(m);
    }
    // customer file: credit box + chatter (comments, activities, history)
    await page.goto(`${base}/#/customer/${cust.id}`); await page.waitForTimeout(2200);
    await page.locator('.mx-chatter').scrollIntoViewIfNeeded().catch(() => {});
    await check('customer');
    for (const i of [1, 2]) { await page.locator('.mx-chatter-tabs button').nth(i).click().catch(() => {}); await page.waitForTimeout(600); }
    await check('customer-history');
    if (run.tag === 'en') {
      // comment with the UI, then the bell
      await page.locator('.mx-chatter-tabs button').first().click();
      await page.locator('.mx-chatter textarea').fill('Checked from the browser');
      await page.locator('.mx-chatter .mx-btn.main').first().click(); await page.waitForTimeout(1200);
      ok('comment posted in the UI', await page.getByText('Checked from the browser').count() > 0);
      ok('status bar bell', await page.locator('.mx-bell').count() === 1);
      // analysis run
      await page.goto(`${base}/#/gaps/analysis`); await page.waitForTimeout(1500);
      await page.locator('.mx-btn.main', { hasText: /Show/ }).click(); await page.waitForTimeout(1500);
      ok('analysis shows rows', await page.locator('.mx-grid tbody tr').count() >= 2);
      ok('analysis chart', await page.locator('svg[role=img]').count() >= 1);
    }
    await ctx.close();
  }
  await browser.close();
  console.log(`DONE fails=${fails} org=${orgName}`);
})().catch((e) => { console.error('ERROR', e); process.exit(1); });
