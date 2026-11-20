// Live check of the phase 5 "Smart" screens on paxve.com with a fresh QA organisation: alerts (raised from real data:
// duplicate bill, voids, a till shortage), AI assistant (switched off without a key: the notice), cash forecast, late-payer
// risk, smart reorder, collector route with the OpenStreetMap map, bill reading (upload), settings; the collector app's
// Route tab at phone width (GPS location capture). Screenshots, console errors, sideways scrolling, raw keys.
// Run inside the Playwright image:
//   docker run --rm --ipc=host -v /srv/paxve/web:/web -v /tmp/p5-out:/out -w /web/e2e mcr.microsoft.com/playwright:v1.65.1-noble node p5-ui.cjs
const { chromium, request } = require('playwright');
const base = 'https://paxve.com';
const H = { 'X-Requested-With': 'paxve', 'Content-Type': 'application/json' };
let fails = 0;
const ok = (name, cond, extra = '') => { console.log(`${cond ? 'PASS' : 'FAIL'} ${name} ${extra}`); if (!cond) fails++; };
const day = (n) => new Date(Date.now() - n * 86400000).toISOString().slice(0, 10);
const PNG = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==';

(async () => {
  const sfx = Date.now().toString(36).slice(-5);
  const orgName = `QA Smart ${sfx}`;
  const email = `qa-p5-${sfx}@example.com`, pass = 'qa-p5-pass-12';
  const colEmail = `qa-p5c-${sfx}@example.com`, colPass = 'qa-p5c-pass-12';
  const qa = await request.newContext({ baseURL: base, extraHTTPHeaders: H });
  let r = await qa.post('/api/v1/signup', { data: { organisation: orgName, company_name: 'Nour Power', base_currency: 'USD', local_currency: 'LBP', template: 'lebanon',
    full_name: 'QA Owner', email, password: pass, locale: 'en' } });
  ok('signup', r.status() === 201);
  const cid = (await (await qa.get('/api/v1/companies')).json())[0].id;
  const P = `/api/v1/companies/${cid}`;
  await qa.post(`${P}/rates`, { data: { currency: 'LBP', date: '2026-01-01', rate: '89500' } });
  const methods = await (await qa.get(`${P}/payment-methods`)).json();
  const usd = methods.find((m) => m.name === 'Cash USD').id;
  const accounts = await (await qa.get(`${P}/accounts`)).json();
  const acc611 = accounts.find((a) => a.code === '611').id;
  // stock and a generator
  const sup = await (await qa.post(`${P}/suppliers`, { data: { name: 'Coral Fuel', currency: 'USD' } })).json();
  const diesel = (await (await qa.post(`${P}/items`, { data: { code: 'DSL', name: 'Diesel', kind: 'stock', unit: 'L' } })).json()).id;
  const oil = (await (await qa.post(`${P}/items`, { data: { code: 'OIL', name: 'Engine oil 5L', kind: 'stock', sale_price: '25', sale_currency: 'USD' } })).json()).id;
  await qa.post(`${P}/bills`, { data: { supplier_id: sup.id, bill_date: day(80), currency: 'USD', lines: [{ item_id: diesel, quantity: '3000', unit_price: '0.9' }, { item_id: oil, quantity: '40', unit_price: '15' }] } });
  await qa.post(`${P}/supplier-prices`, { data: { supplier_id: sup.id, item_id: diesel, currency: 'USD', price: '0.88', lead_days: 60 } });
  const station = (await (await qa.post(`${P}/stations`, { data: { name: 'Hamra station' } })).json()).id;
  const gen = (await (await qa.post(`${P}/generators`, { data: { station_id: station, name: 'Perkins 500', kva: '500', fuel_item_id: diesel } })).json()).id;
  for (let k = 7; k >= 0; k--) await qa.post(`${P}/generators/${gen}/hours`, { data: { date: day(7 * k), hour_meter: String(2000 + (7 - k) * 60) } });
  for (let k = 6; k >= 1; k--) await qa.post(`${P}/stock/fuel`, { data: { item_id: diesel, quantity: '150', date: day(7 * k + 2), generator_id: gen } });
  await qa.post(`${P}/stock/fuel`, { data: { item_id: diesel, quantity: '420', date: day(1), generator_id: gen } });
  // duplicate supplier bill
  for (let i = 0; i < 2; i++) {
    await qa.post(`${P}/bills`, { data: { supplier_id: sup.id, bill_date: day(3), currency: 'USD', supplier_ref: 'CF-2291', allow_duplicate: true,
      lines: [{ description: 'Transport', quantity: '1', unit_price: '60', account_id: acc611 }] } });
  }
  // collector, area, customers with locations and bills
  await qa.post('/api/v1/users', { data: { email: colEmail, full_name: 'Jaber Collector', role: 'collector', password: colPass } });
  const users = await (await qa.get('/api/v1/users')).json();
  const col = users.find((u) => u.email === colEmail).id;
  const area = (await (await qa.post(`${P}/areas`, { data: { name: 'Hamra', collector_id: col } })).json()).id;
  const people = [['Rima Haddad', 33.8963, 35.4823], ['Sami Khoury', 33.8991, 35.4790], ['Nada Aoun', 33.8935, 35.4870], ['Karim Saleh', 33.9012, 35.4851], ['Lina Saad', null, null]];
  const custIds = [];
  for (const [i, [name, lat, lng]] of people.entries()) {
    const c = await (await qa.post(`${P}/customers`, { data: { name, phone: `7011122${i}`, area_id: area, address: 'Hamra, Beirut' } })).json();
    custIds.push(c.id);
    await qa.post(`${P}/invoices`, { data: { customer_id: c.id, currency: 'USD', issue_date: day(60 - i * 10), lines: [{ description: 'Generator subscription', quantity: '1', unit_price: String(30 + i * 5) }] } });
    if (lat) await qa.put(`${P}/customers/${c.id}/location`, { data: { lat, lng, source: 'manual' } });
  }
  // voids by the owner this week
  for (let i = 0; i < 4; i++) {
    const inv = await (await qa.post(`${P}/invoices`, { data: { customer_id: custIds[0], currency: 'USD', issue_date: day(0), lines: [{ description: 'Mistake', quantity: '1', unit_price: '5' }] } })).json();
    await qa.post(`${P}/invoices/${inv.invoice.id}/void`, { data: { reason: 'typo' } });
  }
  await qa.post(`${P}/employees`, { data: { name: 'Ali Technician', currency: 'USD', salary: '900' } });
  r = await qa.post(`${P}/smart/alerts/scan`, { data: {} });
  const scan = await r.json();
  ok('scan', r.status() === 200 && scan.new >= 3, JSON.stringify(scan));
  r = await qa.post(`${P}/smart/scans`, { data: { filename: 'coral-fuel-bill.png', data: PNG } });
  ok('scan uploaded', r.status() === 201);
  await qa.post(`${P}/smart/risk/recompute`, { data: {} });

  const runs = [
    { lang: 'en', w: 1440, h: 900, tag: 'en' },
    { lang: 'ar', w: 1440, h: 900, tag: 'ar' },
    { lang: 'fr', w: 1440, h: 900, tag: 'fr' },
    { lang: 'ar', w: 400, h: 860, tag: 'ar-phone' },
    { lang: 'en', w: 800, h: 1200, tag: 'en-tablet' },
  ];
  const browser = await chromium.launch();
  const login = async (page, em, pw, lang) => {
    await page.goto(`${base}/#/login`);
    await page.waitForSelector('input[type=password]');
    await page.fill('input[type=email], input[name=email]', em);
    await page.fill('input[type=password]', pw);
    await page.keyboard.press('Enter');
    await page.waitForTimeout(2500);
    await page.evaluate(async (l) => { await fetch('/api/v1/me', { method: 'PATCH', credentials: 'include', headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'paxve' }, body: JSON.stringify({ locale: l }) }); }, lang);
    await page.reload(); await page.waitForTimeout(1500);
  };
  for (const run of runs) {
    const ctx = await browser.newContext({ viewport: { width: run.w, height: run.h }, locale: run.lang, geolocation: { latitude: 33.8950, longitude: 35.4800 }, permissions: ['geolocation'] });
    const page = await ctx.newPage();
    page.on('dialog', (d) => d.accept().catch(() => {}));
    const errors = [];
    page.on('console', (m) => { if (m.type() === 'error' && !/401|402|403|404|409|Failed to load resource|tile\.openstreetmap/.test(m.text())) errors.push(m.text()); });
    page.on('pageerror', (e) => errors.push(String(e)));
    await login(page, email, pass, run.lang);
    const check = async (name, phoneOk = false) => {
      const raw = await page.evaluate(() => (document.body.innerText.match(/\b(sm|errors|mx|pc)\.[a-z][\w.]+/g) || []).slice(0, 5).join(','));
      if (raw) errors.push('untranslated: ' + raw);
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
      await page.screenshot({ path: `/out/p5-${run.tag}-${name}.png`, fullPage: run.w < 900 });
      ok(`${run.tag} ${name}`, (overflow <= 2 || phoneOk) && errors.length === 0, `overflow=${overflow} ${errors.join(' | ').slice(0, 300)}`);
      errors.length = 0;
    };
    for (const m of ['alerts', 'assistant', 'forecast', 'risk', 'reorder', 'route', 'scans', 'settings']) {
      await page.goto(`${base}/#/smart/${m}`); await page.waitForTimeout(m === 'route' ? 3500 : 1800);
      await check(`smart-${m}`);
      if (run.tag === 'en') {
        const txt = await page.locator('body').innerText();
        if (m === 'alerts') ok('alerts shown with explanations', txt.includes('Duplicate bill') && txt.includes('Diesel use') && /voids, credit notes/.test(txt), '');
        if (m === 'assistant') ok('assistant says it is off', txt.includes('not switched on'));
        if (m === 'forecast') ok('forecast weeks', (await page.locator('.mx-table tbody tr').count()) >= 13);
        if (m === 'risk') ok('risk list', txt.includes('Rima Haddad') && /days overdue/.test(txt));
        if (m === 'reorder') ok('reorder suggests diesel', txt.includes('Diesel') && txt.includes('Order now'));
        if (m === 'route') {
          ok('route stops listed', (await page.locator('.mx-table').first().locator('tbody tr').count()) === 4);
          ok('map drawn', (await page.locator('.leaflet-container').count()) === 1 && (await page.locator('.leaflet-marker-icon').count()) === 4);
          ok('map tiles', (await page.locator('.leaflet-tile-loaded').count()) > 0, String(await page.locator('.leaflet-tile-loaded').count()));
          ok('google maps link', (await page.locator('a[href^="https://www.google.com/maps/dir/"]').count()) >= 1);
        }
        if (m === 'scans') ok('scan listed', txt.includes('coral-fuel-bill.png'));
      }
    }
    if (run.tag === 'en') {
      // act on an alert, start from my position on the route
      await page.goto(`${base}/#/smart/alerts`); await page.waitForTimeout(1500);
      await page.locator('tr', { hasText: 'Duplicate bill' }).first().getByRole('button', { name: 'Seen' }).click(); await page.waitForTimeout(1200);
      ok('alert acknowledged', (await page.locator('tr', { hasText: 'Duplicate bill' }).first().innerText()).includes('Seen'));
      await page.goto(`${base}/#/smart/route`); await page.waitForTimeout(2500);
      await page.getByRole('button', { name: 'Start from where I am' }).click(); await page.waitForTimeout(2500);
      await check('smart-route-from-me');
      // apps page shows the Smart app
      await page.goto(`${base}/#/apps`); await page.waitForTimeout(1500);
      ok('apps page lists Smart', (await page.locator('body').innerText()).includes('Smart'));
    }
    await ctx.close();
  }
  // collector app: Route tab at phone width, GPS location of the customer without one
  const cctx = await browser.newContext({ viewport: { width: 400, height: 860 }, locale: 'ar', geolocation: { latitude: 33.8977, longitude: 35.4811 }, permissions: ['geolocation'] });
  const cp = await cctx.newPage();
  const cerr = [];
  cp.on('pageerror', (e) => cerr.push(String(e)));
  await login(cp, colEmail, colPass, 'ar');
  await cp.getByRole('tab', { name: 'خط السير' }).click(); await cp.waitForTimeout(3500);
  const before = await cp.locator('body').innerText();
  ok('collector route tab', before.includes('Rima Haddad') && before.includes('Lina Saad'), cerr.join(' | '));
  await cp.locator('.mx-box', { hasText: 'Lina Saad' }).getByRole('button', { name: 'أنا هنا: احفظ الموقع' }).click(); await cp.waitForTimeout(2500);
  const ov = await cp.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
  await cp.screenshot({ path: '/out/p5-collector-route.png', fullPage: true });
  const loc = await (await qa.get(`${P}/collect/route`)).json();
  ok('GPS location saved by the collector', loc.stops.some((s) => s.name === 'Lina Saad'), JSON.stringify(loc.missing_location.map((m) => m.name)));
  ok('collector route fits the phone', ov <= 2 && cerr.length === 0, `overflow=${ov} ${cerr.join(' | ')}`);
  await browser.close();
  console.log(`QA org: ${orgName}`);
  console.log(fails === 0 ? 'ALL PASSED' : `${fails} FAILED`);
  process.exit(fails === 0 ? 0 : 1);
})().catch((e) => { console.error(e); process.exit(2); });
