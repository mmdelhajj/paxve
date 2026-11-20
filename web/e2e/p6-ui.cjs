// Live check of the phase 6a business-pack screens on paxve.com with a fresh QA organisation that loads the demo of every
// new pack (electronics, furniture, wholesale, pharmacy, factory, then restaurant): Business tab screens (serial lookup,
// furniture orders / configurator / showroom, routes / van days / commission / vans, drug search / expiry / controlled /
// settings, claims, restaurant tables / modifiers / food cost / settings, item details), the restaurant floor (open a table,
// add a dish with a modifier, send), the kitchen display, the van app and the apps page. Screenshots, console errors,
// sideways scrolling, raw keys.
//   docker run --rm --ipc=host -v /srv/paxve/web:/web -v /tmp/p6-out:/out -w /web/e2e mcr.microsoft.com/playwright:v1.65.1-noble node p6-ui.cjs
const { chromium, request } = require('playwright');
const base = 'https://paxve.com';
const H = { 'X-Requested-With': 'paxve', 'Content-Type': 'application/json' };
let fails = 0;
const ok = (name, cond, extra = '') => { console.log(`${cond ? 'PASS' : 'FAIL'} ${name} ${extra}`); if (!cond) fails++; };

(async () => {
  const sfx = Date.now().toString(36).slice(-5);
  const email = `qa-p6-${sfx}@example.com`, pass = 'qa-p6-pass-12';
  const qa = await request.newContext({ baseURL: base, extraHTTPHeaders: H });
  let r = await qa.post('/api/v1/signup', { data: { organisation: `QA Packs ${sfx}`, company_name: 'Paxve Demo Shop', base_currency: 'USD', local_currency: 'LBP',
    template: 'lebanon', full_name: 'QA Owner', email, password: pass, locale: 'en' } });
  ok('signup', r.status() === 201);
  const cid = (await (await qa.get('/api/v1/companies')).json())[0].id;
  // a sign-up starts with its business type's apps only (phase 7); this check uses every app
  await qa.put(`/api/v1/companies/${cid}/apps`, { data: { apps: ['gen', 'acc', 'col', 'msg', 'stk', 'pur', 'net', 'sat', 'pos', 'hr', 'crm', 'svc', 'mfg', 'prj', 'flt', 'web', 'mkt', 'ai'] } });
  const P = `/api/v1/companies/${cid}`;
  await qa.post(`${P}/rates`, { data: { currency: 'LBP', date: '2026-01-01', rate: '89500' } });
  for (const k of ['electronics', 'furniture', 'wholesale', 'pharmacy', 'factory', 'restaurant']) {
    r = await qa.post(`${P}/retail/packs/${k}`, { data: { demo: true } });
    ok(`pack ${k}`, r.status() === 200, r.status() === 200 ? '' : await r.text());
  }
  const me = await (await qa.get('/api/v1/me')).json();
  const whs = await (await qa.get(`${P}/verticals/warehouses`)).json();
  const van = whs.find((w) => w.is_van), main = whs.find((w) => !w.is_van && !w.is_showroom);
  await qa.put(`${P}/warehouses/${van.id}/vertical`, { data: { van_user_id: me.id } });
  const items = await (await qa.get(`${P}/items`)).json();
  const byCode = (c) => items.find((i) => i.code === c)?.id;
  r = await qa.post(`${P}/van/days`, { data: { warehouse_id: van.id, source_warehouse_id: main.id, lines: [{ item_id: byCode('WTR-6'), quantity: '20' }] } });
  ok('van day opened', r.status() === 201);
  const serials = await (await qa.get(`${P}/stock/serials?item_id=${byCode('PH-A1')}`)).json();
  ok('demo serials', serials.length === 5);

  const runs = [
    { lang: 'en', w: 1440, h: 900, tag: 'en' }, { lang: 'ar', w: 1440, h: 900, tag: 'ar' }, { lang: 'fr', w: 1440, h: 900, tag: 'fr' },
    { lang: 'ar', w: 400, h: 860, tag: 'ar-phone' }, { lang: 'en', w: 800, h: 1200, tag: 'en-tablet' },
  ];
  const browser = await chromium.launch();
  for (const run of runs) {
    const ctx = await browser.newContext({ viewport: { width: run.w, height: run.h }, locale: run.lang });
    const page = await ctx.newPage();
    const errors = [];
    page.on('console', (m) => { if (m.type() === 'error' && !/401|402|403|404|409|422|Failed to load resource/.test(m.text())) errors.push(m.text()); });
    page.on('pageerror', (e) => errors.push(String(e)));
    await page.goto(`${base}/#/login`);
    await page.waitForSelector('input[type=password]');
    await page.fill('input[type=email], input[name=email]', email);
    await page.fill('input[type=password]', pass);
    await page.keyboard.press('Enter');
    await page.waitForTimeout(2500);
    await page.evaluate(async (l) => { await fetch('/api/v1/me', { method: 'PATCH', credentials: 'include', headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'paxve' }, body: JSON.stringify({ locale: l }) }); }, run.lang);
    await page.reload(); await page.waitForTimeout(1500);
    const check = async (name, phoneOk = false) => {
      const raw = await page.evaluate(() => (document.body.innerText.match(/\b(vt|errors|mx|rt|pc)\.[a-z][\w.]+/g) || []).slice(0, 5).join(','));
      if (raw) errors.push('untranslated: ' + raw);
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
      await page.screenshot({ path: `/out/p6-${run.tag}-${name}.png`, fullPage: run.w < 900 });
      ok(`${run.tag} ${name}`, (overflow <= 2 || phoneOk) && errors.length === 0, `overflow=${overflow} ${errors.join(' | ').slice(0, 300)}`);
      errors.length = 0;
    };
    const tabs = async (group) => {
      await page.goto(`${base}/#/vert/${group}`); await page.waitForTimeout(1500);
      const n = await page.locator('.mx-mode').count();
      for (let i = 0; i < Math.max(1, n); i++) {
        if (n) { await page.locator('.mx-mode').nth(i).click(); await page.waitForTimeout(1000); }
        await check(`${group}-${i}`);
      }
    };
    for (const g of ['electronics', 'furniture', 'wholesale', 'pharmacy', 'claims', 'restaurant', 'items']) await tabs(g);
    if (run.tag === 'en') {
      await page.goto(`${base}/#/vert/electronics`); await page.waitForTimeout(1200);
      await page.fill('input[dir=ltr]', serials[0].serial); await page.keyboard.press('Enter'); await page.waitForTimeout(1500);
      ok('serial lookup shows the card', (await page.locator('text=Warranty card').count()) > 0);
      await check('serial-card');
      await page.goto(`${base}/#/vert/pharmacy`); await page.waitForTimeout(1200);
      await page.fill('input[autofocus], .mx-row input', 'paracetamol'); await page.keyboard.press('Enter'); await page.waitForTimeout(1500);
      ok('drug search', (await page.locator('text=Fevadol').count()) > 0);
      await check('drug-search');
    }
    // restaurant floor: open T1, add fries, send
    await page.goto(`${base}/#/resto`); await page.waitForTimeout(2500);
    await check('resto-floor', run.w < 900);
    if (run.tag === 'en' || run.tag === 'ar') {
      const open = page.locator('button:has(b:text-is("T1"))');
      if (await open.count()) {
        await open.first().click(); await page.waitForTimeout(2000);
        const fries = page.locator('button:has-text("French fries"), button:has-text("بطاطا مقلية")');
        if (await fries.count()) { await fries.first().click(); await page.waitForTimeout(1500); }
        const dlg = page.locator('[role=dialog] button.main'); // modifiers offered: add without any
        if (await dlg.count()) { await dlg.last().click(); await page.waitForTimeout(1500); }
        const sendBtn = page.locator('button.main').filter({ hasText: /\(1\)/ });
        if (await sendBtn.count()) { await sendBtn.first().click(); await page.waitForTimeout(1500); }
        await check('resto-check', run.w < 900);
        if (run.tag === 'en') {
          const kds = await (await qa.get(`${P}/rest/kds`)).json();
          ok('line on the kitchen display', kds.lines.length >= 1, String(kds.lines.length));
        }
      }
    }
    await page.goto(`${base}/#/kds`); await page.waitForTimeout(2500);
    await check('kds');
    await page.goto(`${base}/#/van`); await page.waitForTimeout(2500);
    await check('van');
    await page.goto(`${base}/#/apps`); await page.waitForTimeout(1500);
    await check('apps');
    await ctx.close();
  }
  await browser.close();
  // clean: close the open checks and the van day
  const checks = await (await qa.get(`${P}/rest/checks?status=open`)).json();
  for (const c of checks) await qa.post(`${P}/rest/checks/${c.id}/cancel`, { data: { reason: 'qa' } });
  console.log(fails === 0 ? 'ALL PASS' : `${fails} FAILED`);
  process.exit(fails ? 1 : 0);
})();
