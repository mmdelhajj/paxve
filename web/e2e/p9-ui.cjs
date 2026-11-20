// Live check of phase 9 on paxve.com: bank files and matching, card terminals, QR table ordering (public menu → staff
// accepts), shipping (manual courier label), timelines (projects, production, technicians), e-mail tab of the chatter.
// Arabic, English and French on desktop, Arabic at phone width, English at tablet width: no console errors, no
// sideways scrolling, no raw keys.
//   docker run --rm --ipc=host -v /srv/paxve/web:/web -v /tmp/p9-out:/out -w /web/e2e mcr.microsoft.com/playwright:v1.65.1-noble node p9-ui.cjs
const { chromium, request } = require('playwright');
const base = 'https://paxve.com';
const H = { 'X-Requested-With': 'paxve', 'Content-Type': 'application/json' };
let fails = 0;
const ok = (name, cond, extra = '') => { console.log(`${cond ? 'PASS' : 'FAIL'} ${name} ${extra}`); if (!cond) fails++; };
const day = (n) => { const d = new Date(); d.setUTCDate(d.getUTCDate() + n); return d.toISOString().slice(0, 10); };

(async () => {
  const sfx = Date.now().toString(36).slice(-5);
  const email = `qa-p9-${sfx}@example.com`, pass = 'Qa-p9-pass-123';
  const qa = await request.newContext({ baseURL: base, extraHTTPHeaders: H });
  let r = await qa.post('/api/v1/signup', { data: { organisation: `QA Ops ${sfx}`, company_name: 'QA Resto', base_currency: 'USD', local_currency: 'LBP',
    template: 'lebanon', full_name: 'QA Owner', email, password: pass, locale: 'ar', business_type: 'restaurant' } });
  ok('signup', r.status() === 201, r.status() === 201 ? '' : await r.text());
  const cid = (await (await qa.get('/api/v1/companies')).json())[0].id;
  const P = (p) => `/api/v1/companies/${cid}${p}`;
  await qa.put(P('/apps'), { data: { apps: ['gen', 'acc', 'col', 'msg', 'stk', 'pur', 'pos', 'hr', 'crm', 'svc', 'mfg', 'prj', 'web'] } });
  await qa.post(P('/rates'), { data: { currency: 'LBP', rate: '89500' } });
  r = await qa.post(P('/retail/packs/restaurant'), { data: { demo: true } });
  ok('restaurant pack', r.status() === 200, r.status() === 200 ? '' : (await r.text()).slice(0, 200));
  const methods = await (await qa.get(P('/payment-methods'))).json();
  const accs = await (await qa.get(P('/accounts'))).json();
  const bank = methods.find((m) => m.kind === 'bank');
  // bank: a CSV statement + a fee rule
  const csv = `Date,Details,Amount,Ref\n${day(-3)},BANK FEE,-4.50,F1\n${day(-2)},Transfer from Rami,120,T1\n`;
  r = await qa.post(P('/bank/statements/parse'), { data: { format: 'csv', content: csv, method_id: bank.id, mapping: { header: true, date: 'Date', amount: 'Amount', description: 'Details', reference: 'Ref' } } });
  const pv = await r.json();
  ok('csv parsed', r.status() === 200 && pv.lines.length === 2, JSON.stringify(pv).slice(0, 200));
  r = await qa.post(P('/bank/statements'), { data: { method_id: bank.id, name: 'QA November', format: 'csv', lines: pv.lines } });
  ok('statement imported', r.status() === 201);
  r = await qa.post(P('/bank/statements'), { data: { method_id: bank.id, name: 'again', format: 'csv', lines: pv.lines } });
  ok('same file refused', r.status() === 409);
  await qa.post(P('/bank/match-rules'), { data: { name: 'رسوم المصرف', pattern: 'FEE', action: 'account', account_id: accs.find((a) => a.code === '627').id } });
  // card box with the simulator
  const clearing = accs.find((a) => a.code === '512');
  r = await qa.post(P('/payment-methods'), { data: { name: 'Visa', kind: 'card', currency: 'USD', account_id: clearing.id } });
  const card = (await r.json()).id;
  r = await qa.put(P(`/pos/terminals/${card}`), { data: { driver: 'simulator', fee_pct: '2', settle_method_id: bank.id } });
  ok('terminal set', r.status() === 200, r.status() === 200 ? '' : await r.text());
  r = await qa.post(P('/pos/terminal/charge'), { data: { method_id: card, amount: '7' } });
  ok('simulator approves', r.status() === 201 && (await r.json()).status === 'approved');
  // QR ordering
  const regs = await (await qa.get(P('/pos/registers'))).json();
  r = await qa.put(P('/rest/qr'), { data: { enabled: true, register_id: regs[0].id } });
  const qs = await r.json();
  ok('QR on', r.status() === 200 && qs.tables.length > 0 && qs.tables[0].url);
  const menuUrl = qs.tables[0].url;
  // shipping: own courier
  r = await qa.post(P('/ship/carriers'), { data: { carrier: 'manual', name: 'موزّعنا', is_default: true, config: { flat_rate: '3', per_kg: '1' } } });
  const car = (await r.json()).id;
  r = await qa.post(P('/ship/shipments'), { data: { account_id: car, recipient: 'سامر حداد', phone: '03412877', city: 'صور', currency: 'USD', cod_amount: '25', book: true } });
  const sh = await r.json();
  ok('parcel booked with a label', r.status() === 201 && sh.tracking_no && sh.label_id, JSON.stringify(sh).slice(0, 200));
  // projects timeline
  r = await qa.post(P('/prj/projects'), { data: { name: 'تجهيز الفرع' } });
  const prj = (await r.json()).project.id;
  const t1 = (await (await qa.post(P('/prj/project-tasks'), { data: { project_id: prj, title: 'الكهرباء', planned_hours: '16' } })).json()).task.id;
  const t2 = (await (await qa.post(P('/prj/project-tasks'), { data: { project_id: prj, title: 'الدهان' } })).json()).task.id;
  await qa.put(P(`/gantt/project-tasks/${t1}`), { data: { start: day(1), end: day(2) } });
  await qa.put(P(`/gantt/project-tasks/${t2}`), { data: { start: day(3), end: day(4) } });
  r = await qa.post(P(`/gantt/project-tasks/${t2}/deps`), { data: { depends_on: t1 } });
  ok('dependency added', r.status() === 201);
  // a technician job
  const cust = (await (await qa.post(P('/customers'), { data: { name: 'رامي', email: 'rami@example.com' } })).json()).id;
  const job = (await (await qa.post(P('/fs/service-jobs'), { data: { customer_id: cust, title: 'صيانة البراد' } })).json()).job.id;
  const me = await (await qa.get('/api/v1/me')).json();
  r = await qa.put(P(`/gantt/service-jobs/${job}`), { data: { start: `${day(1)}T09:00:00Z`, technician_id: me.id } });
  ok('job planned', r.status() === 200);
  const inv = (await (await qa.post(P('/invoices'), { data: { customer_id: cust, currency: 'USD', issue_date: day(0), lines: [{ description: 'صيانة', quantity: '1', unit_price: '40' }] } })).json()).invoice;

  const browser = await chromium.launch();
  // the public menu: a guest orders from table 1
  {
    const ctx = await browser.newContext({ viewport: { width: 400, height: 860 }, locale: 'ar' });
    const page = await ctx.newPage();
    const errors = [];
    page.on('console', (m) => { if (m.type() === 'error' && !/401|403|404|409|422|Failed to load resource/.test(m.text())) errors.push(m.text()); });
    page.on('pageerror', (e) => errors.push(String(e)));
    await page.goto(menuUrl.replace(/^https?:\/\/[^/]+/, base));
    await page.waitForTimeout(2500);
    await page.locator('button.mx-btn.main', { hasText: '+' }).first().click();
    await page.locator('button.mx-btn.main', { hasText: '+' }).first().click();
    await page.waitForTimeout(300);
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    await page.screenshot({ path: '/out/p9-menu-phone.png', fullPage: true });
    await page.locator('div[style*="position: fixed"] button.mx-btn.main').click();
    await page.waitForTimeout(1500);
    const pend = await (await qa.get(P('/rest/qr/orders?status=pending'))).json();
    ok('guest order arrives as pending', pend.length === 1, JSON.stringify(pend).slice(0, 200));
    const raw = await page.evaluate(() => (document.body.innerText.match(/\b(op|errors)\.[a-z][\w.-]+/g) || []).slice(0, 5).join(','));
    ok('public menu (phone)', overflow <= 2 && errors.length === 0 && !raw, `overflow=${overflow} ${raw} ${errors.join(' | ').slice(0, 300)}`);
    await ctx.close();
  }

  const runs = [{ lang: 'ar', w: 1440, h: 900, tag: 'ar' }, { lang: 'en', w: 1440, h: 900, tag: 'en' }, { lang: 'fr', w: 1440, h: 900, tag: 'fr' },
    { lang: 'ar', w: 400, h: 860, tag: 'ar-phone' }, { lang: 'en', w: 820, h: 1100, tag: 'en-tablet' }];
  for (const run of runs) {
    const ctx = await browser.newContext({ viewport: { width: run.w, height: run.h }, locale: run.lang });
    const page = await ctx.newPage();
    const errors = [];
    page.on('console', (m) => { if (m.type() === 'error' && !/401|402|403|404|409|422|Failed to load resource/.test(m.text())) errors.push(m.text()); });
    page.on('pageerror', (e) => errors.push(String(e)));
    await page.goto(`${base}/#/login`);
    await page.waitForSelector('input[type=password]');
    await page.fill('input[type=email]', email);
    await page.fill('input[type=password]', pass);
    await page.keyboard.press('Enter');
    await page.waitForTimeout(2500);
    await page.evaluate(async (l) => { await fetch('/api/v1/me', { method: 'PATCH', credentials: 'include', headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'paxve' }, body: JSON.stringify({ locale: l }) }); }, run.lang);
    await page.reload(); await page.waitForTimeout(1500);
    const check = async (name) => {
      const raw = await page.evaluate(() => (document.body.innerText.match(/\b(op|errors|mx|ep)\.[a-z][\w.-]+/g) || []).slice(0, 5).join(','));
      if (raw) errors.push('untranslated: ' + raw);
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
      await page.screenshot({ path: `/out/p9-${run.tag}-${name}.png`, fullPage: run.w < 900 });
      ok(`${run.tag} ${name}`, overflow <= 2 && errors.length === 0, `overflow=${overflow} ${errors.join(' | ').slice(0, 300)}`);
      errors.length = 0;
    };
    const go = async (h, wait = 1800) => { await page.goto(`${base}/#/${h}`); await page.waitForTimeout(wait); };
    await go('ops/bank');
    await page.locator('[data-mode=statements]').click(); await page.waitForTimeout(800);
    await page.locator('table.mx-table tbody tr', { hasText: 'QA November' }).first().click(); await page.waitForTimeout(1200);
    if (run.tag === 'ar') {
      await page.locator('[data-act=auto]').click(); await page.waitForTimeout(1500);
      ok('ar auto-reconcile booked the fee by the rule', /1/.test(await page.locator('.mx-flash.good').first().innerText().catch(() => '')));
    }
    await check('bank');
    await page.locator('[data-mode=rules]').click(); await page.waitForTimeout(800);
    await check('bank-rules');
    await go('ops/terminals');
    if (run.tag === 'ar') ok('ar terminal listed', (await page.locator('td', { hasText: 'Visa' }).count()) >= 1);
    await check('terminals');
    await go('ops/qr');
    if (run.tag === 'ar') {
      ok('ar QR codes drawn', (await page.locator('svg, canvas, img').count()) > 2);
      const acc = page.locator('button.mx-btn.sm.main').first();
      if (await acc.count()) { await acc.click(); await page.waitForTimeout(1500); }
      const pend = await (await qa.get(P('/rest/qr/orders?status=accepted'))).json();
      ok('ar staff accepted the QR order onto a check', pend.length === 1 && pend[0].check_id);
    }
    await check('qr');
    await go('ops/shipping');
    await check('shipping');
    await go('ops/gantt-projects', 2500);
    if (run.tag === 'ar') ok('ar project bars drawn', (await page.locator('.mx-gantt [role=button]').count()) === 2);
    await check('gantt-projects');
    await go('ops/gantt-mfg', 2500);
    await check('gantt-mfg');
    await go(`ops/gantt-field`, 2500);
    if (run.tag === 'ar') ok('ar technician job drawn', (await page.locator('.mx-gantt [role=button]').count()) >= 1);
    await check('gantt-field');
    if (run.tag === 'ar' || run.tag === 'en') {
      await go('invoices', 2500);
      await check('invoices');
    }
    await ctx.close();
  }
  await browser.close();
  // switch the QA organisation off
  r = await qa.post('/api/v1/auth/logout', { data: {} });
  console.log(fails === 0 ? 'ALL PASS' : `FAILED ${fails}`);
  console.log(`QA_ORG=QA Ops ${sfx} invoice=${inv && inv.id}`);
  process.exit(fails === 0 ? 0 : 1);
})().catch((e) => { console.error('FAILED', e); process.exit(1); });
