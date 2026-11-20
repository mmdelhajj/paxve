// Live check of the phase 4 screens on paxve.com with a fresh QA organisation: online store (orders, products, zones,
// settings) and the public shop (catalog, product, cart, checkout, tracking), marketing (campaigns, lists, opt-outs),
// recruitment (positions, pipeline, applicant) and the public careers page, appraisals (cycles, review, my appraisal,
// reports), e-sign (requests, detail) and the public signing page (draw + sign) and opt-out page.
// Screenshots, console errors, sideways scrolling, raw keys.
// Run inside the Playwright image:
//   docker run --rm --ipc=host -v /srv/paxve/web:/web -v /tmp/p4-out:/out -w /web/e2e mcr.microsoft.com/playwright:v1.65.1-noble node p4-ui.cjs
const { chromium, request } = require('playwright');
const base = 'https://paxve.com';
const H = { 'X-Requested-With': 'paxve', 'Content-Type': 'application/json' };
let fails = 0;
const ok = (name, cond, extra = '') => { console.log(`${cond ? 'PASS' : 'FAIL'} ${name} ${extra}`); if (!cond) fails++; };

(async () => {
  const sfx = Date.now().toString(36).slice(-5);
  const orgName = `QA Growth ${sfx}`;
  const email = `qa-p4-${sfx}@example.com`, pass = 'qa-p4-pass-12';
  const qa = await request.newContext({ baseURL: base, extraHTTPHeaders: H });
  let r = await qa.post('/api/v1/signup', { data: { organisation: orgName, company_name: 'Beirut Gear', base_currency: 'USD', local_currency: 'LBP', template: 'lebanon',
    full_name: 'QA Owner', email, password: pass, locale: 'en' } });
  ok('signup', r.status() === 201);
  const me = await (await qa.get('/api/v1/me')).json();
  const cid = (await (await qa.get('/api/v1/companies')).json())[0].id;
  // a sign-up starts with its business type's apps only (phase 7); this check uses every app
  await qa.put(`/api/v1/companies/${cid}/apps`, { data: { apps: ['gen', 'acc', 'col', 'msg', 'stk', 'pur', 'net', 'sat', 'pos', 'hr', 'crm', 'svc', 'mfg', 'prj', 'flt', 'web', 'mkt', 'ai'] } });
  const P = `/api/v1/companies/${cid}`;
  await qa.post(`${P}/rates`, { data: { currency: 'LBP', date: '2026-01-01', rate: '89500' } });
  await qa.post(`${P}/message-providers`, { data: { kind: 'log', name: 'log', is_default: true } });
  await qa.post(`${P}/employees`, { data: { name: 'QA Owner', currency: 'USD', salary: '1500', user_id: me.id } });
  const sup = await (await qa.post(`${P}/suppliers`, { data: { name: 'Gear Co', currency: 'USD' } })).json();
  const cat = await (await qa.post(`${P}/pos/categories`, { data: { name: 'Network', color: 'blue' } })).json();
  const mk = async (code, name, price, extra = {}) => (await (await qa.post(`${P}/items`, { data: { code, name, kind: 'stock', sale_price: price, sale_currency: 'USD', category_id: cat.id, ...extra } })).json()).id;
  const router = await mk('RTR', 'Wi-Fi router AX3000', '45');
  const cable = await mk('CBL', 'Network cable 10 m', '6');
  const ups = await mk('UPS', 'Mini UPS for router', '35');
  await qa.post(`${P}/items`, { data: { code: 'INS', name: 'Home installation', kind: 'service', sale_price: '15', sale_currency: 'USD' } });
  await qa.post(`${P}/bills`, { data: { supplier_id: sup.id, bill_date: '2026-11-01', currency: 'USD', lines: [
    { item_id: router, quantity: '12', unit_price: '30' }, { item_id: cable, quantity: '80', unit_price: '2' }, { item_id: ups, quantity: '3', unit_price: '20' }] } });
  const slug = `beirut-gear-${sfx}`;
  r = await qa.put(`${P}/shop/settings`, { data: { enabled: true, slug, title: 'Beirut Gear', tagline: 'Internet gear delivered to your door', payments: ['cod', 'pickup', 'whish'],
    whatsapp: '+96170123456', pay_note: 'Whish: 70 123 456', featured: [router, ups], careers_enabled: true, careers_intro: 'Join our team in Beirut.' } });
  ok('shop settings', r.status() === 200, String(r.status()));
  const zone = await (await qa.post(`${P}/shop/zones`, { data: { name: 'Beirut', fee: '3', free_from: '100', eta: 'Same day' } })).json();
  await qa.post(`${P}/shop/zones`, { data: { name: 'Mount Lebanon', fee: '5', eta: '1-2 days' } });
  const promo = await (await qa.post(`${P}/retail/promotions`, { data: { name: 'Welcome', kind: 'basket', value_type: 'amount', value: '5', coupon_code: 'WELCOME5', stackable: true, currency: 'USD' } })).json();
  // an order placed by a visitor
  const pub = await request.newContext({ baseURL: base, extraHTTPHeaders: H });
  r = await pub.post(`/api/v1/shop/${slug}/orders`, { data: { name: 'Lina Saad', phone: '03 123 456', address: 'Hamra, Bliss street', zone_id: zone.id, payment: 'cod',
    coupon: 'WELCOME5', lines: [{ item_id: router, quantity: '1' }, { item_id: cable, quantity: '2' }] } });
  const order = await r.json();
  ok('public order', r.status() === 200 && !!order.token, JSON.stringify(order).slice(0, 200));
  // marketing
  for (const [n, p] of [['Rima Haddad', '70111222'], ['Sami Khoury', '71222333'], ['Nada Aoun', '76333444']]) await qa.post(`${P}/customers`, { data: { name: n, phone: p } });
  const list = await (await qa.post(`${P}/mkt/lists`, { data: { name: 'VIP customers' } })).json();
  await qa.post(`${P}/mkt/lists/${list.id}/members`, { data: { contacts: [{ name: 'Walk-in', phone: '76 555 444' }], audience: { all_customers: true } } });
  await qa.post(`${P}/mkt/optouts`, { data: { phone: '71222333', reason: 'asked by phone' } });
  const camp = await (await qa.post(`${P}/mkt/campaigns`, { data: { name: 'Eid offer', channel: 'whatsapp', list_id: list.id, body: 'Hi {name}, 5$ off with {coupon}: {link}',
    link_url: 'https://example.com/eid', promotion_id: promo.id, throttle_per_min: 30 } })).json();
  r = await qa.post(`${P}/mkt/campaigns/${camp.id}/send`, { data: {} });
  ok('campaign sent', r.status() === 200);
  await qa.post(`${P}/mkt/campaigns`, { data: { name: 'New year (draft)', channel: 'sms', audience: { all_customers: true }, body: 'Happy new year {name}!' } });
  const optTok = (await (await qa.get(`${P}/mkt/campaigns/${camp.id}`)).json()).recipients.length > 0;
  ok('campaign recipients', optTok);
  // recruitment + appraisal
  const job = await (await qa.post(`${P}/hr/job-positions`, { data: { title: 'Sales representative', department: 'Sales', location: 'Beirut', is_published: true,
    description: 'Sell routers and internet plans to homes and offices.', requirements: 'Arabic and English, driving licence.', salary_range: '600-900 USD' } })).json();
  r = await pub.post(`/api/v1/careers/${slug}/jobs/${job.id}/apply`, { data: { name: 'Omar Khalil', email: 'omar@example.com', phone: '70999888', cover_letter: 'I sold phones for 3 years.',
    cv: { filename: 'cv.pdf', data: Buffer.from('%PDF-1.4 cv of Omar').toString('base64') } } });
  ok('application', r.status() === 200);
  await qa.post(`${P}/hr/applicants`, { data: { job_id: job.id, name: 'Maya Fares', phone: '71444555', source: 'referral' } });
  const cy = await (await qa.post(`${P}/hr/appraisal-cycles`, { data: { name: '2026 second half', period_from: '2026-07-01', period_to: '2026-12-31', due_date: '2026-12-20' } })).json();
  await qa.post(`${P}/hr/appraisal-cycles/${cy.id}/open`, { data: { goals: [{ title: 'Sales target', weight: 60 }, { title: 'Customer care', weight: 40 }] } });
  // e-sign
  const sreq = await (await qa.post(`${P}/esign/requests`, { data: { doc_type: 'contract', title: 'Installation contract', body: 'Beirut Gear installs the home network.\nWarranty: 12 months.',
    send: true, signers: [{ name: 'Lina Saad', phone: '03123456' }] } })).json();
  const signTok = sreq.signers[0].link.split('/').pop();
  await qa.post(`${P}/esign/requests`, { data: { doc_type: 'contract', title: 'Supplier agreement', body: 'Draft terms', signers: [{ name: 'Gear Co', email: 'gear@example.com' }] } });
  const optoutTok = await (async () => { const d = await (await qa.get(`${P}/mkt/campaigns/${camp.id}`)).json(); return d.recipients.find((x) => x.status === 'queued') ? 'skip' : ''; })();

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
      const raw = await page.evaluate(() => (document.body.innerText.match(/\b(sh|mk|tl|es|pub|errors|mx)\.[a-z][\w.]+/g) || []).slice(0, 5).join(','));
      if (raw) errors.push('untranslated: ' + raw);
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
      await page.screenshot({ path: `/out/p4-${run.tag}-${name}.png`, fullPage: run.w < 900 });
      ok(`${run.tag} ${name}`, (overflow <= 2 || phoneOk) && errors.length === 0, `overflow=${overflow} ${errors.join(' | ').slice(0, 300)}`);
      errors.length = 0;
    };
    // ---- online store (staff) ----
    for (const m of ['orders', 'products', 'zones', 'settings']) { await page.goto(`${base}/#/webshop/${m}`); await page.waitForTimeout(1700); await check(`webshop-${m}`); }
    await page.goto(`${base}/#/webshop/orders`); await page.waitForTimeout(1500);
    await page.locator('.mx-table tbody tr', { hasText: 'Lina Saad' }).first().click(); await page.waitForTimeout(900);
    await check('webshop-order');
    if (run.tag === 'en') {
      const txt = await page.locator('body').innerText();
      ok('online order listed with coupon and state', txt.includes('WELCOME5') && txt.includes('Preparing'));
    }
    // ---- marketing ----
    await page.goto(`${base}/#/marketing/campaigns`); await page.waitForTimeout(1700);
    await check('mkt-campaigns');
    await page.locator('.mx-table tbody tr', { hasText: 'Eid offer' }).first().click(); await page.waitForTimeout(1300);
    await check('mkt-campaign');
    await page.locator('.mx-table tbody tr', { hasText: 'New year' }).first().click(); await page.waitForTimeout(1300);
    if (run.tag === 'en') {
      await page.locator('button', { hasText: 'Who will receive it?' }).first().click(); await page.waitForTimeout(1300);
      ok('audience preview', /will receive it/.test(await page.locator('body').innerText()));
    }
    await check('mkt-draft');
    for (const m of ['lists', 'optouts']) { await page.goto(`${base}/#/marketing/${m}`); await page.waitForTimeout(1500); await check(`mkt-${m}`); }
    await page.goto(`${base}/#/marketing/lists`); await page.waitForTimeout(1300);
    await page.locator('.mx-table tbody tr', { hasText: 'VIP customers' }).first().click(); await page.waitForTimeout(1200);
    await check('mkt-list');
    // ---- recruitment & appraisals ----
    for (const m of ['positions', 'applicants', 'appraisals', 'reports']) { await page.goto(`${base}/#/talent/${m}`); await page.waitForTimeout(1700); await check(`talent-${m}`, m === 'applicants'); }
    await page.goto(`${base}/#/talent/applicants`); await page.waitForTimeout(1500);
    await page.locator('div[draggable]', { hasText: 'Omar Khalil' }).first().click(); await page.waitForTimeout(1300);
    await check('talent-applicant', true);
    await page.goto(`${base}/#/talent/appraisals`); await page.waitForTimeout(1500);
    await page.locator('.mx-table tbody tr', { hasText: 'QA Owner' }).first().click(); await page.waitForTimeout(1000);
    await check('talent-review');
    await page.goto(`${base}/#/my-appraisal`); await page.waitForTimeout(1500);
    await check('my-appraisal');
    // ---- e-sign ----
    await page.goto(`${base}/#/esign`); await page.waitForTimeout(1600);
    await check('esign');
    await page.locator('.mx-table tbody tr', { hasText: 'Installation contract' }).first().click(); await page.waitForTimeout(1200);
    await check('esign-detail');
    await page.locator('button', { hasText: run.lang === 'ar' ? 'طلب جديد' : run.lang === 'fr' ? 'Nouvelle demande' : 'New request' }).first().click(); await page.waitForTimeout(900);
    await check('esign-new');
    // ---- apps page knows the new apps ----
    await page.goto(`${base}/#/apps`); await page.waitForTimeout(1500);
    await check('apps');
    // ---- public pages ----
    await page.goto(`${base}/#/shop/${slug}`); await page.waitForTimeout(2200);
    await check('shop');
    if (run.tag === 'en') {
      const txt = await page.locator('body').innerText();
      ok('shop: featured, LBP prices, categories', txt.includes('Featured') && txt.includes('LBP') && txt.includes('Network'));
    }
    await page.locator('.psh-card button[aria-label]', { hasText: '' }).first().click(); await page.waitForTimeout(1500);
    await check('shop-product');
    await page.locator('button.psh-btn.main').first().click(); await page.waitForTimeout(1500); // add to cart → cart page
    await check('shop-cart');
    if (run.tag === 'ar-phone' || run.tag === 'en') {
      await page.locator('input[placeholder]').filter({ hasText: '' }).first();
      await page.fill('input[autocomplete=name]', run.tag === 'en' ? 'Karim Test' : 'كريم تجربة');
      await page.fill('input[autocomplete=tel]', '71 888 777');
      await page.fill('textarea[autocomplete=street-address]', 'Achrafieh, Sassine');
      await page.waitForTimeout(800);
      await page.locator('button.psh-btn.main', { hasText: run.tag === 'en' ? 'Place the order' : 'إرسال الطلب' }).click(); await page.waitForTimeout(2200);
      const txt = await page.locator('body').innerText();
      ok(`${run.tag} checkout placed`, /✅/.test(txt));
      await check('shop-done');
      await page.locator('a.psh-btn.main').first().click(); await page.waitForTimeout(2000);
      await check('shop-track');
    }
    await page.goto(`${base}/#/order/${order.token}`); await page.waitForTimeout(1800);
    await check('order-track');
    await page.goto(`${base}/#/careers/${slug}`); await page.waitForTimeout(1800);
    await check('careers');
    await page.locator('button', { hasText: run.lang === 'ar' ? 'تقدّم الآن' : run.lang === 'fr' ? 'Postuler' : 'Apply' }).first().click(); await page.waitForTimeout(800);
    await check('careers-apply');
    await page.goto(`${base}/#/sign/${signTok}`); await page.waitForTimeout(1800);
    await check('sign');
    if (run.tag === 'ar-phone') {
      const box = await page.locator('canvas').boundingBox();
      if (box) {
        await page.mouse.move(box.x + 20, box.y + 80); await page.mouse.down();
        for (let i = 0; i < 12; i++) await page.mouse.move(box.x + 30 + i * 22, box.y + 60 + (i % 3) * 25);
        await page.mouse.up();
      }
      await page.locator('input[type=checkbox]').first().check();
      await page.locator('button.mx-btn.main', { hasText: 'توقيع' }).click(); await page.waitForTimeout(2500);
      const txt = await page.locator('body').innerText();
      ok('signed from the phone', txt.includes('وقّع الجميع') || txt.includes('وقّعت'));
      await check('sign-done');
    }
    await ctx.close();
  }
  await browser.close();
  void optoutTok;
  console.log(`DONE fails=${fails} org=${orgName} slug=${slug}`);
})().catch((e) => { console.error('ERROR', e); process.exit(1); });
