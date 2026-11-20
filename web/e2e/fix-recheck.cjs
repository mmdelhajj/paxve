// Re-check of the 2026-11-20 audit fixes on the live site: cashier lands on the company with a till, collector
// explanation and area message, leave types on first visit, login back-off, company time zone.
const { chromium, request } = require('playwright');
const fs = require('fs');
const base = process.env.E2E_BASE_URL || 'https://paxve.com';
const out = '/web/e2e/screens/fix';
fs.mkdirSync(out, { recursive: true });
const sfx = Math.random().toString(36).slice(2, 7);
const results = [];
const check = (name, ok, info = '') => { results.push({ name, ok }); console.log(`${ok ? 'PASS' : 'FAIL'} ${name} ${info}`); };

(async () => {
  const owner = { email: `qa-fix-${sfx}@example.com`, pass: 'qa-fix-pass-123' };
  const api = await request.newContext({ baseURL: base, extraHTTPHeaders: { 'X-Requested-With': 'paxve' } });
  let r = await api.post('/api/v1/signup', { data: { organisation: `QA Fix ${sfx}`, company_name: 'QA Fix A', base_currency: 'USD', local_currency: 'LBP',
    template: 'lebanon', full_name: 'QA Owner', email: owner.email, password: owner.pass, locale: 'ar' } });
  check('signup', r.status() === 201, r.status());
  const comps = await (await api.get('/api/v1/companies')).json();
  const A = comps[0].id;
  check('company time zone', comps[0].timezone === 'Asia/Beirut', comps[0].timezone);
  r = await api.post('/api/v1/companies', { data: { name_en: 'AAA Empty', name_ar: 'أ شركة فارغة', base_currency: 'USD', local_currency: 'LBP' } });
  const B = (await r.json()).id;
  check('second company', !!B, r.status());
  // the till lives in company A only; the empty company B sorts first, so it is the default company
  const ms = await (await api.get(`/api/v1/companies/${A}/payment-methods`)).json();
  r = await api.post(`/api/v1/companies/${A}/pos/registers`, { data: { name: 'Till B', currency: 'USD', method_ids: ms.filter((m) => m.currency === 'USD' && m.kind === 'cash').map((m) => m.id).slice(0, 3) } });
  check('till in company A', r.status() === 201, `${r.status()} ${(await r.text()).slice(0, 160)}`);
  const users = {};
  for (const role of ['cashier', 'collector', 'data_entry']) {
    const u = { email: `qa-fix-${sfx}-${role}@example.com`, pass: `${role}-pass-123` };
    r = await api.post('/api/v1/users', { data: { email: u.email, full_name: `QA ${role}`, role, password: u.pass, locale: 'ar' } });
    check(`user ${role}`, r.status() === 201, r.status());
    users[role] = u;
  }
  // leave types: several screens at once on a new company
  const codes = await Promise.all([1, 2, 3, 4, 5].map(() => api.get(`/api/v1/companies/${A}/hr/leave-types`).then((x) => x.status())));
  check('leave types first visit (5 at once)', codes.every((c) => c === 200), codes.join(','));
  // a customer without an area: a collector may not collect from it, and is told why
  const cust = await (await api.post(`/api/v1/companies/${A}/customers`, { data: { name: 'زبون بلا منطقة' } })).json();
  const methods = await (await api.get(`/api/v1/companies/${A}/payment-methods`)).json();
  const usdBox = methods.find((m) => m.currency === 'USD' && m.kind !== 'gift' && m.kind !== 'loyalty');
  const col = await request.newContext({ baseURL: base, extraHTTPHeaders: { 'X-Requested-With': 'paxve' } });
  await col.post('/api/v1/auth/login', { data: { email: users.collector.email, password: users.collector.pass } });
  r = await col.post(`/api/v1/companies/${A}/payments`, { data: { customer_id: cust.id, date: new Date().toISOString().slice(0, 10), currency: 'USD',
    tenders: [{ method_id: usdBox?.id, amount: '5' }] } });
  const pj = await r.json().catch(() => ({}));
  check('collector outside own areas gets not_your_area', r.status() === 403 && pj.error === 'not_your_area', `${r.status()} ${JSON.stringify(pj)}`);

  const browser = await chromium.launch();
  const login = async (u, w = 1440, h = 900) => {
    const ctx = await browser.newContext({ viewport: { width: w, height: h }, locale: 'ar', timezoneId: 'Asia/Beirut' });
    const page = await ctx.newPage();
    const errors = [];
    page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()); });
    await page.goto(`${base}/#/login`); await page.waitForSelector('input[type=password]');
    await page.fill('input[type=email], input[name=email]', u.email); await page.fill('input[type=password]', u.pass);
    await page.keyboard.press('Enter'); await page.waitForTimeout(5000);
    errors.length = 0; // the 401 of the signed-out landing page is expected
    await page.reload(); await page.waitForTimeout(4000);
    return { ctx, page, errors };
  };
  // cashier: lands on company B (the one with a till) with a company switcher and the status bar
  {
    const { ctx, page, errors } = await login(users.cashier);
    const txt = await page.evaluate(() => document.body.innerText);
    const saved = await page.evaluate(() => localStorage.getItem('paxve.company'));
    await page.screenshot({ path: `${out}/cashier.png` });
    check('cashier lands on the company with the till', saved === A && txt.includes('Till B') && !txt.includes('لا يوجد صندوق بعد'), `saved=${saved === B ? 'B' : saved === A ? 'A' : saved}`);
    check('cashier has company switcher + status bar', (await page.locator('select[aria-label]').count()) > 0 && (await page.locator('footer.mx-status').count()) > 0);
    check('cashier console clean', errors.length === 0, errors.slice(0, 2).join(' | '));
    await ctx.close();
  }
  // collector (phone width): explanation instead of an empty list
  {
    const { ctx, page, errors } = await login(users.collector, 400, 860);
    const txt = await page.evaluate(() => document.body.innerText);
    await page.screenshot({ path: `${out}/collector-phone.png` });
    check('collector sees the areas explanation', txt.includes('لا يوجد مشتركون للعرض'), txt.slice(0, 120).replace(/\s+/g, ' '));
    check('collector console clean', errors.length === 0, errors.slice(0, 2).join(' | '));
    await ctx.close();
  }
  // owner: leave screen and "my page" without employee file, no console errors
  {
    const { ctx, page, errors } = await login(owner);
    await page.goto(`${base}/#/hr/leave`); await page.waitForTimeout(3000);
    await page.screenshot({ path: `${out}/owner-leave.png` });
    await page.goto(`${base}/#/self`); await page.waitForTimeout(2500);
    await page.screenshot({ path: `${out}/owner-self.png` });
    check('owner leave + my page: no console errors', errors.length === 0, errors.slice(0, 3).join(' | '));
    await ctx.close();
  }
  await browser.close();

  // login back-off: the owner mistypes 6 times from this address; a colleague on the same address still signs in
  const att = await request.newContext({ baseURL: base, extraHTTPHeaders: { 'X-Requested-With': 'paxve' } });
  let last = 0;
  for (let i = 0; i < 6; i++) last = (await att.post('/api/v1/auth/login', { data: { email: owner.email, password: 'wrong-' + i } })).status();
  check('owner held back after repeated mistakes', last === 429, last);
  const mate = await request.newContext({ baseURL: base, extraHTTPHeaders: { 'X-Requested-With': 'paxve' } });
  r = await mate.post('/api/v1/auth/login', { data: { email: users.data_entry.email, password: users.data_entry.pass } });
  check('colleague on the same address signs in', r.status() === 200, r.status());

  // gateway callbacks posted as a form reach the handler (404 unknown gateway, not 415)
  r = await att.post('/api/v1/pay/webhook/00000000-0000-0000-0000-000000000000', { form: { order: 'x', status: 'paid' } });
  check('webhook form body accepted by the router', r.status() === 404, r.status());

  fs.writeFileSync(`${out}/results.json`, JSON.stringify({ sfx, results }, null, 1));
  console.log(`SUMMARY ${results.filter((x) => x.ok).length}/${results.length} passed · org QA Fix ${sfx}`);
})().catch((e) => { console.error('ERROR', e); process.exit(1); });
