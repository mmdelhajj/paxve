// Live check of the retail screens on paxve.com with a fresh QA organisation (supermarket pack + demo data):
// screenshots, console errors, sideways scrolling and raw keys, plus a till sale with promotions.
const { chromium, request } = require('playwright');
const base = 'https://paxve.com';
const H = { 'X-Requested-With': 'paxve', 'Content-Type': 'application/json' };
let fails = 0;
const ok = (name, cond, extra = '') => { console.log(`${cond ? 'PASS' : 'FAIL'} ${name} ${extra}`); if (!cond) fails++; };

(async () => {
  const sfx = Date.now().toString(36).slice(-5);
  const orgName = `QA Retail ${sfx}`;
  const email = `qa-retail-${sfx}@example.com`, pass = 'qa-retail-pass-1';
  const qa = await request.newContext({ baseURL: base, extraHTTPHeaders: H });
  let r = await qa.post('/api/v1/signup', { data: { organisation: orgName, company_name: orgName, base_currency: 'USD', local_currency: 'LBP', template: 'lebanon',
    full_name: 'QA Owner', email, password: pass, locale: 'en' } });
  ok('signup', r.status() === 201);
  const cid = (await (await qa.get('/api/v1/companies')).json())[0].id;
  const P = `/api/v1/companies/${cid}`;
  await qa.post(`${P}/rates`, { data: { currency: 'LBP', date: '2026-01-01', rate: '89500' } });
  r = await qa.post(`${P}/retail/packs/supermarket`, { data: { demo: true } });
  const pk = await r.json(); ok('supermarket pack + demo', r.ok() && pk.demo?.items >= 5, JSON.stringify(pk.demo));
  // a template with variants for the variants screen
  const tpl = await (await qa.post(`${P}/items`, { data: { code: 'TSH', name: 'T-shirt', kind: 'stock', sale_price: '12', sale_currency: 'USD' } })).json();
  await qa.put(`${P}/items/${tpl.id}/variant-setup`, { data: { attributes: [{ name: 'Size', values: ['S', 'M', 'L'] }, { name: 'Colour', values: ['White', 'Black'] }] } });
  r = await qa.post(`${P}/items/${tpl.id}/variants/generate`, { data: {} }); ok('variants', (await r.json()).created === 6);
  const regs = await (await qa.get(`${P}/pos/registers`)).json();
  ok('demo till', regs.length === 1);
  const reg = regs[0];
  r = await qa.post(`${P}/pos/sessions`, { data: { register_id: reg.id } }); ok('session', r.status() === 201);
  const sess = (await r.json()).session.id;
  const items = await (await qa.get(`${P}/items`)).json();
  const water = items.find((x) => x.code === 'WTR'), milk = items.find((x) => x.code === 'MLK');
  const methods = await (await qa.get(`${P}/payment-methods`)).json();
  const usd = methods.find((m) => m.kind === 'cash' && m.currency === 'USD');
  r = await qa.post(`${P}/pos/orders`, { data: { session_id: sess, apply_promos: true, lines: [{ item_id: water.id, quantity: '3' }, { item_id: milk.id, quantity: '2' }],
    tenders: [{ method_id: usd.id, amount: '10' }], change_method_id: usd.id } });
  const sale = await r.json();
  ok('till sale with promotions', r.status() === 201 && (sale.order.promos ?? []).length >= 1, JSON.stringify(sale.order?.promos ?? sale).slice(0, 200));
  // a loyalty member and a gift card
  const cust = await (await qa.post(`${P}/pos/customers`, { data: { name: 'Nadia', phone: '70111222' } })).json();
  await qa.put(`${P}/retail/loyalty/customers/${cust.id}`, { data: { card: 'auto' } });
  r = await qa.post(`${P}/pos/orders`, { data: { session_id: sess, customer_id: cust.id, apply_promos: true, gift_sold: [{ code: `QA-${sfx}`.toUpperCase(), amount: '20' }],
    lines: [{ item_id: milk.id, quantity: '5' }], tenders: [{ method_id: usd.id, amount: '40' }], change_method_id: usd.id } });
  const s2 = await r.json(); ok('gift card sold + points earned', r.status() === 201 && Number(s2.order.loyalty?.earned) > 0, JSON.stringify(s2.order?.loyalty ?? s2).slice(0, 200));

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
    const errors = [];
    page.on('console', (m) => { if (m.type() === 'error' && !/401|402|403|404|Failed to load resource/.test(m.text())) errors.push(m.text()); });
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
      const raw = await page.evaluate(() => (document.body.innerText.match(/\b(rt|pos|mx|fin|errors)\.[a-z][\w.]+/g) || []).slice(0, 5).join(','));
      if (raw) errors.push('untranslated: ' + raw);
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
      await page.screenshot({ path: `/out/r-${run.tag}-${name}.png` });
      ok(`${run.tag} ${name}`, overflow <= 2 && errors.length === 0, `overflow=${overflow} ${errors.join(' | ').slice(0, 300)}`);
      errors.length = 0;
    };
    for (const m of ['promos', 'loyalty', 'gift', 'variants', 'labels', 'expiry', 'receive', 'report', 'packs']) {
      await page.goto(`${base}/#/retail/${m}`); await page.waitForTimeout(1800);
      if (m === 'promos') { await page.locator('.mx-box').first().getByText(/Water|مياه|Eau/).first().click().catch(() => {}); await page.waitForTimeout(500); }
      if (m === 'loyalty') { await page.getByText('Nadia').first().click().catch(() => {}); await page.waitForTimeout(800); }
      if (m === 'variants') { await page.getByText('T-shirt').first().click().catch(() => {}); await page.waitForTimeout(1200); }
      await check(m);
    }
    // the till
    await page.goto(`${base}/#/pos`); await page.waitForTimeout(3000);
    const cards = page.locator('.mantine-Card-root');
    const n = await cards.count();
    ok(`${run.tag} till items`, n > 3, `cards=${n}`);
    for (let i = 0; i < 4 && i < n; i++) { await cards.nth(i).click().catch(() => {}); await page.waitForTimeout(300); }
    await page.waitForTimeout(1200);
    await check('pos-cart');
    if (run.w > 1000) {
      await page.keyboard.press('F9'); await page.waitForTimeout(1200);
      await check('pos-pay');
      await page.keyboard.press('Escape'); await page.waitForTimeout(500);
    }
    await ctx.close();
  }
  await browser.close();
  console.log(`fails=${fails} org=${orgName}`);
})();
