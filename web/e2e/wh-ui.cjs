// Live check of the warehouse-pro screens on paxve.com with a fresh QA organisation: operations (3-step receipt and
// delivery), bins, batch picking, packages, delivery board, dropship, reports, setup, the phone scanner and the
// driver app. Screenshots, console errors, sideways scrolling, raw keys. Run inside the Playwright image:
//   docker run --rm --ipc=host -v /srv/paxve/web:/web -v /tmp/wh-out:/out -w /web/e2e mcr.microsoft.com/playwright:v1.65.1-noble node wh-ui.cjs
const { chromium, request } = require('playwright');
const base = 'https://paxve.com';
const H = { 'X-Requested-With': 'paxve', 'Content-Type': 'application/json' };
let fails = 0;
const ok = (name, cond, extra = '') => { console.log(`${cond ? 'PASS' : 'FAIL'} ${name} ${extra}`); if (!cond) fails++; };

(async () => {
  const sfx = Date.now().toString(36).slice(-5);
  const orgName = `QA Warehouse ${sfx}`;
  const email = `qa-wh-${sfx}@example.com`, pass = 'qa-wh-pass-12';
  const qa = await request.newContext({ baseURL: base, extraHTTPHeaders: H });
  let r = await qa.post('/api/v1/signup', { data: { organisation: orgName, company_name: orgName, base_currency: 'USD', local_currency: 'LBP', template: 'lebanon',
    full_name: 'QA Owner', email, password: pass, locale: 'en' } });
  ok('signup', r.status() === 201);
  const me = await (await qa.get('/api/v1/me')).json();
  const cid = (await (await qa.get('/api/v1/companies')).json())[0].id;
  // a sign-up starts with its business type's apps only (phase 7); this check uses every app
  await qa.put(`/api/v1/companies/${cid}/apps`, { data: { apps: ['gen', 'acc', 'col', 'msg', 'stk', 'pur', 'net', 'sat', 'pos', 'hr', 'crm', 'svc', 'mfg', 'prj', 'flt', 'web', 'mkt', 'ai'] } });
  const P = `/api/v1/companies/${cid}`;
  await qa.post(`${P}/rates`, { data: { currency: 'LBP', date: '2026-01-01', rate: '89500' } });
  const wh = (await (await qa.get(`${P}/wh/warehouses`)).json())[0].id;
  r = await qa.patch(`${P}/wh/warehouses/${wh}`, { data: { use_bins: true, reception_steps: 3, delivery_steps: 3, removal: 'fefo', crossdock: false } });
  ok('warehouse steps', r.ok(), String(r.status()));
  r = await qa.post(`${P}/wh/locations/generate`, { data: { warehouse_id: wh, zone: 'A', aisles: 2, shelves: 2, bins: 3 } });
  ok('bins generated', r.status() === 201);
  const sup = await (await qa.post(`${P}/suppliers`, { data: { name: 'Ubiquiti Dealer', currency: 'USD' } })).json();
  const rtr = await (await qa.post(`${P}/items`, { data: { code: 'RTR', name: 'Router AX3000', kind: 'stock', unit: 'pc', sale_price: '60', barcode: '6291041500213' } })).json();
  const cbl = await (await qa.post(`${P}/items`, { data: { code: 'CBL', name: 'Cable Cat6 (box)', kind: 'stock', unit: 'pc', sale_price: '35' } })).json();
  const locs = await (await qa.get(`${P}/wh/locations?warehouse_id=${wh}`)).json();
  const bin = (c) => locs.find((l) => l.code === c).id;
  await qa.post(`${P}/wh/putaway-rules`, { data: { warehouse_id: wh, item_id: rtr.id, location_id: bin('A-01-1-01') } });
  await qa.post(`${P}/wh/putaway-rules`, { data: { warehouse_id: wh, item_id: cbl.id, location_id: bin('A-02-1-02') } });
  // receipt: PO → GR → QC → putaway, then the bill
  const po = await (await qa.post(`${P}/purchase-orders`, { data: { supplier_id: sup.id, currency: 'USD', order_date: '2026-11-15',
    lines: [{ item_id: rtr.id, quantity: '20', unit_price: '38' }, { item_id: cbl.id, quantity: '10', unit_price: '22' }] } })).json();
  await qa.post(`${P}/purchase-orders/${po.order.id}/confirm`, { data: {} });
  r = await qa.post(`${P}/purchase-orders/${po.order.id}/receive`, { data: { delivery_ref: 'DN-55' } }); ok('goods received', r.status() === 201);
  const qc = (await (await qa.get(`${P}/wh/ops?kind=qc`)).json())[0];
  r = await qa.post(`${P}/wh/ops/${qc.id}/done`, { data: {} }); const qd = await r.json(); ok('qc done', r.ok());
  r = await qa.post(`${P}/wh/ops/${qd.next.next_op_id}/done`, { data: {} }); ok('putaway done', r.ok());
  r = await qa.post(`${P}/purchase-orders/${po.order.id}/bill`, { data: { bill_date: '2026-11-16', supplier_ref: 'INV-55', confirm: true } }); ok('bill', r.status() === 201);
  // two sales orders → picks; one batch; a package; a pick done to leave a pack task for the screens
  const cust = await (await qa.post(`${P}/customers`, { data: { name: 'Rima Haddad', phone: '70123456', address: 'Hamra, Bliss street, bldg 12' } })).json();
  const cust2 = await (await qa.post(`${P}/customers`, { data: { name: 'Karim Aoun', phone: '70999888', address: 'Jounieh, main road' } })).json();
  const so = [];
  for (const [c, q] of [[cust.id, '3'], [cust2.id, '2']]) {
    const o = await (await qa.post(`${P}/sales/orders`, { data: { customer_id: c, currency: 'USD', lines: [{ item_id: rtr.id, description: 'Router AX3000', quantity: q, unit_price: '60' },
      { item_id: cbl.id, description: 'Cable Cat6', quantity: '1', unit_price: '35' }] } })).json();
    await qa.post(`${P}/sales/orders/${o.order.id}/confirm`, { data: {} });
    so.push(o.order.id);
  }
  const picks = await (await qa.get(`${P}/wh/ops?kind=pick`)).json();
  ok('picks created', picks.length === 2);
  r = await qa.post(`${P}/wh/batches`, { data: { warehouse_id: wh, op_ids: [picks[1].id] } }); ok('batch', r.status() === 201);
  r = await qa.post(`${P}/wh/ops/${picks[0].id}/done`, { data: {} }); const pk = await r.json(); ok('pick done', r.ok());
  const pkg = await (await qa.post(`${P}/wh/packages`, { data: { warehouse_id: wh, kind: 'box', sales_order_id: so[0], weight: '4.2', length_cm: '40', width_cm: '30', height_cm: '20' } })).json();
  const packOp = await (await qa.get(`${P}/wh/ops/${pk.next.next_op_id}`)).json();
  r = await qa.post(`${P}/wh/ops/${packOp.op.id}/done`, { data: { lines: packOp.lines.map((l) => ({ line_id: l.id, done_qty: l.quantity, package_id: pkg.package.id })) } });
  const pd = await r.json(); ok('packed', r.ok());
  r = await qa.post(`${P}/wh/ops/${pd.next.next_op_id}/done`, { data: {} }); ok('shipped', r.ok());
  // delivery board: vehicle, trip with the delivery, the owner as driver
  const veh = await (await qa.post(`${P}/wh/vehicles`, { data: { name: 'Kia K2700', plate: 'M 482913', capacity_kg: '1200' } })).json();
  const todo = await (await qa.get(`${P}/wh/to-schedule`)).json();
  r = await qa.post(`${P}/wh/trips`, { data: { vehicle_id: veh.id, driver_id: me.id, stops: [{ sales_delivery_id: todo.deliveries[0].id }, { sales_order_id: so[1] }] } });
  ok('trip', r.status() === 201);
  // slow stock + dropship data
  const old = await (await qa.post(`${P}/items`, { data: { code: 'OLD', name: 'Old modem', kind: 'stock', unit: 'pc' } })).json();
  await qa.post(`${P}/stock/adjust`, { data: { item_id: old.id, quantity: '4', value: '40', date: '2025-06-01', notes: 'old' } });

  const runs = [
    { lang: 'en', w: 1440, h: 900, tag: 'en' },
    { lang: 'ar', w: 1440, h: 900, tag: 'ar' },
    { lang: 'fr', w: 1440, h: 900, tag: 'fr' },
    { lang: 'ar', w: 400, h: 860, tag: 'ar-phone' },
    { lang: 'fa', w: 400, h: 860, tag: 'fa-phone' },
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
      const raw = await page.evaluate(() => (document.body.innerText.match(/\b(wh|errors|mx)\.[a-z][\w.]+/g) || []).slice(0, 5).join(','));
      if (raw) errors.push('untranslated: ' + raw);
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
      await page.screenshot({ path: `/out/wh-${run.tag}-${name}.png`, fullPage: run.w < 500 });
      ok(`${run.tag} ${name}`, overflow <= 2 && errors.length === 0, `overflow=${overflow} ${errors.join(' | ').slice(0, 300)}`);
      errors.length = 0;
    };
    for (const m of ['ops', 'bins', 'batches', 'packages', 'trips', 'dropship', 'reports', 'setup']) {
      await page.goto(`${base}/#/wh/${m}`); await page.waitForTimeout(1800);
      if (m === 'ops' || m === 'batches' || m === 'packages' || m === 'trips') {
        await page.locator('.mx-table tbody tr').first().click().catch(() => {}); await page.waitForTimeout(1200);
      }
      if (m === 'dropship') { await page.locator('.mx-box select').first().selectOption({ index: 1 }).catch(() => {}); await page.waitForTimeout(1200); }
      await check(m);
      if (m === 'reports') {
        for (const i of [1, 2, 3]) { await page.locator('.mx-box .mx-modes .mx-mode').nth(i).click().catch(() => {}); await page.waitForTimeout(1200); await check(`reports-${i}`); }
      }
    }
    // scanner and driver (phone apps)
    await page.goto(`${base}/#/scan`); await page.waitForTimeout(1800);
    await page.locator('form input').fill('A-01-1-01'); await page.keyboard.press('Enter'); await page.waitForTimeout(1500);
    if (run.tag === 'en') ok('scanner: bin lookup shows the router', (await page.locator('main').innerText()).includes('RTR'));
    if (run.lang === 'ar') ok(`${run.tag} scanner in Arabic`, (await page.locator('header').innerText()).includes('ماسح'));
    await check('scan-lookup');
    await page.locator('nav button').nth(1).click(); await page.waitForTimeout(1500);
    await check('scan-tasks');
    if (run.tag === 'en') {
      await page.locator('main button').first().click().catch(() => {}); await page.waitForTimeout(1200);
      await page.locator('form input').fill('6291041500213'); await page.keyboard.press('Enter'); await page.waitForTimeout(1200);
      ok('scanner: item scan counts one', /1 \/ \d/.test(await page.locator('main').innerText()));
      await check('scan-task-open');
    }
    for (const i of [2, 3, 4]) { await page.locator('nav button').nth(i).click(); await page.waitForTimeout(1000); await check(`scan-${i}`); }
    await page.goto(`${base}/#/driver`); await page.waitForTimeout(1800);
    await page.locator('main button').first().click().catch(() => {}); await page.waitForTimeout(1500);
    await check('driver');
    if (run.tag === 'en') {
      const deliver = page.locator('main button.main').first();
      ok('driver: stop with a deliver button', await deliver.count() > 0);
      await deliver.click(); await page.waitForTimeout(800);
      await page.locator('main input').first().fill('Rima');
      const c = page.locator('canvas');
      const b = await c.boundingBox();
      if (b) { await page.mouse.move(b.x + 20, b.y + 40); await page.mouse.down(); await page.mouse.move(b.x + 120, b.y + 90, { steps: 8 }); await page.mouse.up(); }
      await page.locator('main button.main').last().click(); await page.waitForTimeout(2000);
      ok('driver: proof of delivery saved', (await page.locator('main').innerText()).includes('Delivered'));
      await check('driver-pod');
    }
    await ctx.close();
  }
  await browser.close();
  console.log(`DONE fails=${fails} org=${orgName}`);
})().catch((e) => { console.error('ERROR', e); process.exit(1); });
