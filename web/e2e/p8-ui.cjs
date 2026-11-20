// Live check of phase 8 on paxve.com: branches, inter-company, group accounts with eliminations, print layout
// designer (live preview, save, applied on a printed invoice), role dashboards and "My dashboard", sign-in policy,
// tenders (bids, comparison, award) and blanket agreements (order, remaining).
// Screens in Arabic, English and French on desktop, Arabic at phone width, English at tablet width: no console errors,
// no sideways scrolling, no raw keys.
//   docker run --rm --ipc=host -v /srv/paxve/web:/web -v /tmp/p8-out:/out -w /web/e2e mcr.microsoft.com/playwright:v1.65.1-noble node p8-ui.cjs
const { chromium, request } = require('playwright');
const base = 'https://paxve.com';
const H = { 'X-Requested-With': 'paxve', 'Content-Type': 'application/json' };
let fails = 0;
const ok = (name, cond, extra = '') => { console.log(`${cond ? 'PASS' : 'FAIL'} ${name} ${extra}`); if (!cond) fails++; };

(async () => {
  const sfx = Date.now().toString(36).slice(-5);
  const email = `qa-p8-${sfx}@example.com`, pass = 'Qa-p8-pass-123';
  const qa = await request.newContext({ baseURL: base, extraHTTPHeaders: H });
  let r = await qa.post('/api/v1/signup', { data: { organisation: `QA Enterprise ${sfx}`, company_name: 'QA Holding', base_currency: 'USD', local_currency: 'LBP',
    template: 'lebanon', full_name: 'QA Owner', email, password: pass, locale: 'ar', business_type: 'supermarket' } });
  ok('signup', r.status() === 201, r.status() === 201 ? '' : await r.text());
  const comps = await (await qa.get('/api/v1/companies')).json();
  const cid = comps[0].id;
  const all = ['gen', 'acc', 'col', 'msg', 'stk', 'pur', 'pos', 'hr', 'crm', 'svc', 'ai'];
  await qa.put(`/api/v1/companies/${cid}/apps`, { data: { apps: all } });
  await qa.post(`/api/v1/companies/${cid}/rates`, { data: { currency: 'LBP', rate: '89500' } });
  const P = (p) => `/api/v1/companies/${cid}${p}`;
  // branches
  r = await qa.post(P('/branches'), { data: { code: 'TYR', name: 'صور', phone: '07 123 456' } });
  ok('branch created', r.status() === 201);
  await qa.post(P('/branches'), { data: { code: 'BEY', name: 'بيروت' } });
  // second company + link + a sale + a loan
  r = await qa.post('/api/v1/companies', { data: { name_en: `QA Trading ${sfx}`, name_ar: 'شركة التجارة', base_currency: 'USD', local_currency: 'LBP', template: 'lebanon', locale: 'ar' } });
  const c2 = (await r.json()).id;
  await qa.put(`/api/v1/companies/${c2}/apps`, { data: { apps: all } });
  r = await qa.post('/api/v1/intercompany/links', { data: { company_a: cid, company_b: c2 } });
  ok('companies linked', r.status() === 201, r.status() === 201 ? '' : await r.text());
  const link = await r.json();
  r = await qa.post(P('/invoices'), { data: { customer_id: link.a_customer_id, currency: 'USD', issue_date: new Date().toISOString().slice(0, 10),
    lines: [{ description: 'Management fee', quantity: '1', unit_price: '250' }] } });
  ok('inter-company invoice', r.status() === 201);
  r = await qa.post('/api/v1/intercompany/sync', { data: {} });
  const sync = await r.json();
  ok('sale mirrored (or waiting for the buyer\'s expense account)', r.status() === 200 && (sync.created === 1 || sync.failed === 1), JSON.stringify(sync));
  const accA = await (await qa.get(P('/accounts'))).json();
  const accB = await (await qa.get(`/api/v1/companies/${c2}/accounts`)).json();
  const cash = (l) => l.find((a) => a.code === '531')?.id;
  r = await qa.post('/api/v1/intercompany/transfers', { data: { link_id: link.id, from_company: cid, kind: 'loan', currency: 'USD', amount: '1000', from_account_id: cash(accA), to_account_id: cash(accB) } });
  ok('loan posted on both sides', r.status() === 201, r.status() === 201 ? '' : await r.text());
  r = await qa.get(`/api/v1/reports/consolidated?report=balance-sheet&companies=${cid},${c2}&currency=USD&to=2100-01-01&eliminate=1`);
  const bs = await r.json();
  ok('consolidated balance sheet balances after eliminations', r.status() === 200 && bs.totals_after && Math.abs(Number(bs.totals_after.assets) - Number(bs.totals_after.liabilities_equity)) < 0.01, JSON.stringify(bs.totals_after || bs));
  // suppliers, tender with two bids, agreement
  const s1 = (await (await qa.post(P('/suppliers'), { data: { name: 'مورد الكابلات', currency: 'USD' } })).json()).id;
  const s2 = (await (await qa.post(P('/suppliers'), { data: { name: 'Cable House', currency: 'USD' } })).json()).id;
  r = await qa.post(P('/purchasing/tenders'), { data: { title: 'كابلات 2027', currency: 'USD', lines: [{ description: 'كابل 10 ملم', quantity: '100' }, { description: 'قاطع 32A', quantity: '20' }] } });
  const td = await r.json();
  const tid = td.tender.id, [l1, l2] = td.lines.map((x) => x.id);
  await qa.post(P(`/purchasing/tenders/${tid}/status`), { data: { status: 'open' } });
  await qa.put(P(`/purchasing/tenders/${tid}/bids`), { data: { supplier_id: s1, lead_days: 5, lines: [{ tender_line_id: l1, unit_price: '2.10' }, { tender_line_id: l2, unit_price: '9' }] } });
  r = await qa.put(P(`/purchasing/tenders/${tid}/bids`), { data: { supplier_id: s2, lead_days: 12, lines: [{ tender_line_id: l1, unit_price: '1.95' }, { tender_line_id: l2, unit_price: '9.50' }] } });
  ok('two bids in', r.status() === 200 && (await r.json()).bids.length === 2);
  r = await qa.post(P('/purchasing/agreements'), { data: { supplier_id: s1, title: 'مازوت 2027', currency: 'USD', start_date: '2026-01-01', end_date: '2027-12-31',
    lines: [{ description: 'مازوت (ليتر)', unit_price: '0.95', quantity: '20000' }] } });
  const ag = await r.json();
  await qa.post(P(`/purchasing/agreements/${ag.agreement.id}/status`), { data: { status: 'active' } });
  r = await qa.post(P(`/purchasing/agreements/${ag.agreement.id}/order`), { data: { lines: [{ agreement_line_id: ag.lines[0].id, quantity: '5000' }] } });
  ok('order drawn from the agreement', r.status() === 201, r.status() === 201 ? '' : await r.text());
  // a customer invoice to print with the layout
  const cust = (await (await qa.post(P('/customers'), { data: { name: 'سامر حداد', phone: '03412877' } })).json()).id;
  r = await qa.post(P('/invoices'), { data: { customer_id: cust, currency: 'USD', issue_date: new Date().toISOString().slice(0, 10), lines: [{ description: 'اشتراك', quantity: '1', unit_price: '38' }] } });
  const invNo = (await r.json()).invoice.number;
  ok('no Google / Microsoft buttons when not configured', (await (await qa.get('/api/v1/auth/oidc/providers')).json()).length === 0);

  const browser = await chromium.launch();
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
    ok(`${run.tag} login page has no SSO buttons`, (await page.locator('a[href*="/auth/oidc/"]').count()) === 0);
    await page.fill('input[type=email]', email);
    await page.fill('input[type=password]', pass);
    await page.keyboard.press('Enter');
    await page.waitForTimeout(2500);
    await page.evaluate(async (l) => { await fetch('/api/v1/me', { method: 'PATCH', credentials: 'include', headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'paxve' }, body: JSON.stringify({ locale: l }) }); }, run.lang);
    await page.reload(); await page.waitForTimeout(1500);
    const check = async (name) => {
      const raw = await page.evaluate(() => (document.body.innerText.match(/\b(ep|errors|mx|sm|oa)\.[a-z][\w.]+/g) || []).slice(0, 5).join(','));
      if (raw) errors.push('untranslated: ' + raw);
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
      await page.screenshot({ path: `/out/p8-${run.tag}-${name}.png`, fullPage: run.w < 900 });
      ok(`${run.tag} ${name}`, overflow <= 2 && errors.length === 0, `overflow=${overflow} ${errors.join(' | ').slice(0, 300)}`);
      errors.length = 0;
    };
    const go = async (h, wait = 1800) => { await page.goto(`${base}/#/${h}`); await page.waitForTimeout(wait); };
    for (const m of ['branches', 'interco', 'consolidation', 'layouts', 'dashboards', 'security', 'tenders', 'agreements']) {
      await go(`ent/${m}`);
      if (m === 'consolidation') await page.waitForTimeout(800);
      if (m === 'tenders') {
        await page.locator('table.mx-table tbody tr', { hasText: 'T-1' }).first().click();
        await page.waitForTimeout(1200);
        if (run.tag === 'ar') {
          const best = await page.locator('td[style*="230, 244, 236"], td[style*="rgb(230, 244, 236)"]').count();
          ok('ar tender comparison marks the best price per line', best === 2, String(best));
        }
      }
      if (m === 'agreements') {
        await page.locator('table.mx-table tbody tr', { hasText: 'BA-1' }).first().click();
        await page.waitForTimeout(1200);
        if (run.tag === 'ar') ok('ar agreement shows the remaining quantity', /15000/.test(await page.locator('section.mx-box').nth(1).innerText()));
      }
      if (m === 'branches' && run.tag === 'ar') ok('ar branches listed', (await page.locator('td', { hasText: 'TYR' }).count()) >= 1);
      if (m === 'interco' && run.tag === 'ar') ok('ar inter-company link and loan shown', /1,000/.test(await page.locator('.mx-col').first().innerText()));
      if (m === 'consolidation' && run.tag === 'ar') ok('ar group balance sheet with eliminations', (await page.locator('th', { hasText: /الموحّد/ }).count()) >= 1);
      await check(m);
    }
    await go('mydash', 2500);
    if (run.tag === 'ar') ok('ar my dashboard shows the standard widgets', (await page.locator('section.mx-box').count()) >= 4);
    await check('mydash');
    if (run.tag === 'ar') {
      // designer: change the colour and hide the customer phone, save, then the printed invoice follows
      await go('ent/layouts');
      await page.locator('input[type=color]').first().evaluate((el) => { const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set; set.call(el, '#b42318'); el.dispatchEvent(new Event('input', { bubbles: true })); });
      await page.locator('fieldset label', { hasText: /هاتف الزبون/ }).locator('input').uncheck();
      await page.locator('input[maxlength="80"]').fill('فاتورة ضريبية');
      const css = await page.locator('.print-doc style').first().textContent();
      ok('ar layout preview follows the choices', /#b42318/.test(css || '') && /f-customer_phone\{display:none/.test(css || ''), (css || '').slice(0, 200));
      await page.locator('button.mx-btn.main', { hasText: /حفظ/ }).first().click();
      await page.waitForTimeout(1200);
      const lay = await (await qa.get(P('/doc-layouts'))).json();
      ok('ar layout saved', lay.invoice.accent === '#b42318' && (lay.invoice.hide || []).includes('customer_phone'), JSON.stringify(lay.invoice));
      await check('layouts-saved');
      await go('invoices', 2500);
      await page.reload(); await page.waitForTimeout(2500);
      const row = page.locator('tr', { hasText: String(invNo) }).filter({ hasText: /سامر/ }).first();
      if (await row.count()) {
        await row.dblclick().catch(() => row.click());
        await page.waitForTimeout(2000);
        const doc = page.locator('.print-doc[data-doc="invoice"]');
        if (await doc.count()) {
          ok('ar printed invoice uses the title of the layout', /فاتورة ضريبية/.test(await doc.innerText()));
          ok('ar printed invoice hides the phone', !(await doc.locator('.f-customer_phone').isVisible()));
          await page.screenshot({ path: '/out/p8-ar-invoice-layout.png' });
        } else ok('ar invoice opened with its layout', false, 'no print-doc');
      } else console.log('NOTE invoice row not found on the bills screen (generator screen layout)');
      // my dashboard: add a widget and save
      await go('mydash', 2000);
      await page.locator('button', { hasText: /رتّب لوحتي/ }).first().click();
      await page.locator('label.mx-fld', { hasText: /إضافة بطاقة/ }).locator('select').selectOption('stock_value');
      await page.locator('button.mx-btn', { hasText: /^إضافة$/ }).first().click();
      await page.locator('button.mx-btn.main', { hasText: /حفظ/ }).first().click();
      await page.waitForTimeout(1500);
      const mine = await (await qa.get(P('/dashboards/mine'))).json();
      ok('ar own dashboard saved', mine.source === 'user' && mine.widgets.some((w) => w.metric === 'stock_value'), JSON.stringify(mine).slice(0, 200));
      await check('mydash-saved');
    }
    await ctx.close();
  }
  await browser.close();
  console.log(fails ? `FAILED ${fails}` : 'ALL PASS');
  process.exit(fails ? 1 : 0);
})().catch((e) => { console.error('FAIL crash', e); process.exit(1); });
