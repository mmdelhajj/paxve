// Live check of the manufacturing-pro screens on paxve.com with a fresh QA organisation: orders, bills of materials
// (structure), work centers and routings, MRP, quality, traceability, reports, scrap and the shop-floor screen (timer,
// measure check, finish). Screenshots, console errors, sideways scrolling, raw keys. Run inside the Playwright image:
//   docker run --rm --ipc=host -v /srv/paxve/web:/web -v /tmp/mfg-out:/out -w /web/e2e mcr.microsoft.com/playwright:v1.65.1-noble node mfg-ui.cjs
const { chromium, request } = require('playwright');
const base = 'https://paxve.com';
const H = { 'X-Requested-With': 'paxve', 'Content-Type': 'application/json' };
let fails = 0;
const ok = (name, cond, extra = '') => { console.log(`${cond ? 'PASS' : 'FAIL'} ${name} ${extra}`); if (!cond) fails++; };

(async () => {
  const sfx = Date.now().toString(36).slice(-5);
  const orgName = `QA Factory ${sfx}`;
  const email = `qa-mfg-${sfx}@example.com`, pass = 'qa-mfg-pass-12';
  const qa = await request.newContext({ baseURL: base, extraHTTPHeaders: H });
  let r = await qa.post('/api/v1/signup', { data: { organisation: orgName, company_name: orgName, base_currency: 'USD', local_currency: 'LBP', template: 'lebanon',
    full_name: 'QA Owner', email, password: pass, locale: 'en' } });
  ok('signup', r.status() === 201);
  const cid = (await (await qa.get('/api/v1/companies')).json())[0].id;
  // a sign-up starts with its business type's apps only (phase 7); this check uses every app
  await qa.put(`/api/v1/companies/${cid}/apps`, { data: { apps: ['gen', 'acc', 'col', 'msg', 'stk', 'pur', 'net', 'sat', 'pos', 'hr', 'crm', 'svc', 'mfg', 'prj', 'flt', 'web', 'mkt', 'ai'] } });
  const P = `/api/v1/companies/${cid}`;
  await qa.post(`${P}/rates`, { data: { currency: 'LBP', date: '2026-01-01', rate: '89500' } });
  const wh = (await (await qa.get(`${P}/wh/warehouses`)).json())[0].id;
  const mk = async (code, name) => (await (await qa.post(`${P}/items`, { data: { code, name, kind: 'stock', unit: 'pc', sale_price: '100' } })).json()).id;
  const steel = await mk('STL', 'Steel bar'), screw = await mk('SCR', 'Screw'), frame = await mk('FRM', 'Chair frame'), box = await mk('BOX', 'Box');
  const chair = await mk('CHR', 'Office chair'), offcut = await mk('OFF', 'Offcuts');
  for (const it of [steel, frame, chair]) await qa.put(`${P}/items/${it}/stock-settings`, { data: { tracking: 'lot' } });
  const sup = await (await qa.post(`${P}/suppliers`, { data: { name: 'Metal Co', currency: 'USD' } })).json();
  r = await qa.post(`${P}/bills`, { data: { supplier_id: sup.id, bill_date: '2026-11-01', currency: 'USD', lines: [
    { item_id: steel, quantity: '50', unit_price: '10', lots: [{ lot_no: 'S-1', quantity: '50' }] }, { item_id: screw, quantity: '200', unit_price: '0.5' },
    { item_id: box, quantity: '20', unit_price: '1' }] } });
  ok('bill', r.status() === 201);
  const weld = await (await qa.post(`${P}/mfg/workcenters`, { data: { code: 'WLD', name: 'Welding cell', cost_per_hour: '60' } })).json();
  const asm = await (await qa.post(`${P}/mfg/workcenters`, { data: { code: 'ASM', name: 'Assembly line', cost_per_hour: '30', capacity: '2' } })).json();
  const rt = await (await qa.post(`${P}/mfg/routings`, { data: { name: 'Chair line', operations: [
    { name: 'Weld frame', workcenter_id: weld.id, minutes: '30', instructions: 'Check the welding gauge before starting.' },
    { name: 'Assemble', workcenter_id: asm.id, minutes: '20' }] } })).json();
  await qa.post(`${P}/mfg/quality-points`, { data: { name: 'Weld width', trigger_on: 'operation', workcenter_id: weld.id, test_type: 'measure', tol_min: '4', tol_max: '6', unit: 'mm' } });
  await qa.post(`${P}/mfg/quality-points`, { data: { name: 'Final inspection', trigger_on: 'production', item_id: chair } });
  await qa.post(`${P}/mfg/boms`, { data: { item_id: frame, lines: [{ item_id: steel, quantity: '2' }, { item_id: screw, quantity: '4' }] } });
  const cb = await (await qa.post(`${P}/mfg/boms`, { data: { item_id: chair, code: 'CH', routing_id: rt.routing.id, overhead_cost: '1',
    lines: [{ item_id: frame, quantity: '1' }, { item_id: screw, quantity: '8' }, { item_id: box, quantity: '1' }], byproducts: [{ item_id: offcut, quantity: '1', cost_share: '10' }] } })).json();
  ok('chair BoM', !!cb.bom);
  const cust = await (await qa.post(`${P}/customers`, { data: { name: 'Office Plus', phone: '70123456' } })).json();
  const so = await (await qa.post(`${P}/sales/orders`, { data: { customer_id: cust.id, currency: 'USD', lines: [{ item_id: chair, description: 'Office chair', quantity: '4', unit_price: '150' }] } })).json();
  await qa.post(`${P}/sales/orders/${so.order.id}/confirm`, { data: {} });
  r = await qa.post(`${P}/mfg/mrp/run`, { data: { warehouse_id: wh } });
  const run1 = await r.json();
  ok('mrp run', r.status() === 201 && run1.mos.length === 2, JSON.stringify(run1).slice(0, 200));
  const mos = await (await qa.get(`${P}/mfg/orders?status=open`)).json();
  const fm = mos.find((m) => m.item_code === 'FRM'), cm = mos.find((m) => m.item_code === 'CHR');
  await qa.post(`${P}/mfg/orders/${fm.id}/confirm`, { data: {} });
  r = await qa.post(`${P}/mfg/orders/${fm.id}/produce`, { data: { lots: [{ lot_no: 'FR-1', quantity: fm.quantity }] } }); ok('frames produced', r.ok(), String(r.status()));
  r = await qa.post(`${P}/mfg/orders/${cm.id}/confirm`, { data: {} }); ok('chair MO confirmed', r.ok());
  // a second (draft) order that will be short of components, for the shortage report
  await qa.post(`${P}/mfg/orders`, { data: { bom_id: cb.bom.id, quantity: '30', confirm: true, deadline: '2026-11-02' } });

  const runs = [
    { lang: 'en', w: 1440, h: 900, tag: 'en' },
    { lang: 'ar', w: 1440, h: 900, tag: 'ar' },
    { lang: 'fr', w: 1440, h: 900, tag: 'fr' },
    { lang: 'ar', w: 400, h: 860, tag: 'ar-phone' },
    { lang: 'en', w: 800, h: 1200, tag: 'en-tablet' },
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
    const check = async (name, phoneOk = false) => {
      const raw = await page.evaluate(() => (document.body.innerText.match(/\b(mf|wh|errors|mx|fin)\.[a-z][\w.]+/g) || []).slice(0, 5).join(','));
      if (raw) errors.push('untranslated: ' + raw);
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
      await page.screenshot({ path: `/out/mfg-${run.tag}-${name}.png`, fullPage: run.w < 900 });
      ok(`${run.tag} ${name}`, (overflow <= 2 || phoneOk) && errors.length === 0, `overflow=${overflow} ${errors.join(' | ').slice(0, 300)}`);
      errors.length = 0;
    };
    for (const m of ['orders', 'boms', 'workcenters', 'mrp', 'quality', 'trace', 'reports', 'scrap']) {
      await page.goto(`${base}/#/mfg/${m}`); await page.waitForTimeout(1800);
      if (m === 'orders' || m === 'boms' || m === 'workcenters') { await page.locator('.mx-table tbody tr').first().click().catch(() => {}); await page.waitForTimeout(1300); }
      if (m === 'boms') {
        await page.locator('.mx-table tbody tr', { hasText: 'CHR' }).first().click().catch(() => {}); await page.waitForTimeout(1200);
        const btns = page.locator('section.mx-box button');
        const n = await btns.count();
        for (let i = 0; i < n; i++) { const tx = await btns.nth(i).innerText(); if (/Structure|الهيكل|Structure et/.test(tx)) { await btns.nth(i).click(); break; } }
        await page.waitForTimeout(1300);
        if (run.tag === 'en') ok('BoM structure shows the frame sub-BoM and the total', /Chair frame/.test(await page.locator('body').innerText()) && /Total/.test(await page.locator('body').innerText()));
      }
      if (m === 'trace') { await page.locator('section.mx-box input').first().fill('S-1'); await page.keyboard.press('Enter'); await page.waitForTimeout(1800);
        if (run.tag === 'en') ok('trace S-1 forward reaches FR-1', (await page.locator('body').innerText()).includes('FR-1')); }
      await check(m);
      if (m === 'quality' || m === 'reports') {
        const tabs = page.locator('section.mx-box [role=tablist] button');
        const n = await tabs.count();
        for (let i = 1; i < n; i++) { await tabs.nth(i).click(); await page.waitForTimeout(1200); await check(`${m}-${i}`); }
      }
    }
    // shop floor
    await page.goto(`${base}/#/shopfloor`); await page.waitForTimeout(1800);
    await page.locator('main button', { hasText: 'Welding cell' }).first().click().catch(() => {}); await page.waitForTimeout(1500);
    await check('floor-list');
    await page.locator('main button').filter({ hasText: 'MO' }).first().click().catch(() => {}); await page.waitForTimeout(1500);
    await check('floor-wo');
    if (run.tag === 'en') {
      const txt = await page.locator('main').innerText();
      ok('floor: instructions and timer', txt.includes('welding gauge') && /\d:\d\d/.test(txt));
      await page.locator('main button.main').first().click(); await page.waitForTimeout(1500); // start
      ok('floor: started', (await page.locator('main').innerText()).includes('Started'));
      await page.locator('main input[aria-label="Measured value"]').fill('5');
      await page.locator('main button', { hasText: 'Record' }).click(); await page.waitForTimeout(1500);
      ok('floor: measure recorded as passed', (await page.locator('main').innerText()).includes('Passed'));
      await page.locator('main input[inputmode=decimal]').first().fill('35');
      await page.locator('main button', { hasText: 'Finish' }).click(); await page.waitForTimeout(1500);
      ok('floor: operation finished', (await page.locator('main').innerText()).includes('Operation finished'));
      await check('floor-finished');
    }
    await ctx.close();
  }
  await browser.close();
  console.log(`DONE fails=${fails} org=${orgName}`);
})().catch((e) => { console.error('ERROR', e); process.exit(1); });
