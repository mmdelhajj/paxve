// Live check of phase 6b on paxve.com: the marketing site (signed out; AR / EN / FR desktop, AR / EN phone — sections,
// business tabs, plans from the platform, FAQ, demo form, no sideways scrolling / console errors / raw keys), SEO files,
// the installable apps (manifests, /field /owner /my start addresses, icons), the owner app, the field app tabs, the
// customer app (portal: report a problem, remembered account), the payroll posting-date option and the supplier
// balances import card. Screenshots of the site go to /out/site-*.png.
//   docker run --rm --ipc=host -v /srv/paxve/web:/web -v /tmp/p6b-out:/out -w /web/e2e mcr.microsoft.com/playwright:v1.65.1-noble node p6b-ui.cjs
const { chromium, request } = require('playwright');
const base = 'https://paxve.com';
const H = { 'X-Requested-With': 'paxve', 'Content-Type': 'application/json' };
let fails = 0;
const ok = (name, cond, extra = '') => { console.log(`${cond ? 'PASS' : 'FAIL'} ${name} ${extra}`); if (!cond) fails++; };
const RAW = /\b(site|pwa|pc|vt|mx|sm|hr2|import)\.[a-z][\w]*\.[\w.]+/g;

(async () => {
  const sfx = Date.now().toString(36).slice(-5);
  const anon = await request.newContext({ baseURL: base, extraHTTPHeaders: H });
  // ---------- static files ----------
  for (const [p, want] of [['/robots.txt', 'Sitemap'], ['/sitemap.xml', '<urlset'], ['/pwa.js', 'beforeinstallprompt'], ['/manifest-field.webmanifest', 'Paxve Field'],
    ['/manifest-owner.webmanifest', 'Paxve Owner'], ['/manifest-my.webmanifest', '/my'], ['/manifest.webmanifest', 'shortcuts']]) {
    const r = await anon.get(p);
    ok(`file ${p}`, r.status() === 200 && (await r.text()).includes(want), String(r.status()));
  }
  for (const p of ['/og.png', '/icons/field-512.png', '/icons/owner-192-maskable.png', '/icons/my-512.png']) {
    const r = await anon.get(p);
    ok(`image ${p}`, r.status() === 200 && /image\/png/.test(r.headers()['content-type'] || ''), String(r.status()));
  }
  const site = await (await anon.get('/api/v1/public/site')).json();
  ok('public plans', Array.isArray(site.plans) && site.plans.length >= 1, String(site.plans?.length));

  const browser = await chromium.launch();
  // ---------- marketing site ----------
  const runs = [
    { lang: 'ar', w: 1440, h: 900, tag: 'ar' }, { lang: 'en', w: 1440, h: 900, tag: 'en' }, { lang: 'fr', w: 1440, h: 900, tag: 'fr' },
    { lang: 'ar', w: 390, h: 844, tag: 'ar-phone' }, { lang: 'en', w: 390, h: 844, tag: 'en-phone' }, { lang: 'ar', w: 820, h: 1180, tag: 'ar-tablet' }, { lang: 'tr', w: 1280, h: 800, tag: 'tr' },
  ];
  for (const run of runs) {
    const ctx = await browser.newContext({ viewport: { width: run.w, height: run.h }, locale: run.lang });
    const page = await ctx.newPage();
    const errors = [];
    page.on('console', (m) => { if (m.type() === 'error' && !/401|Failed to load resource/.test(m.text())) errors.push(m.text()); });
    page.on('pageerror', (e) => errors.push(String(e)));
    await page.goto(`${base}/?lang=${run.lang}`);
    await page.waitForSelector('.px-hero h1', { timeout: 20000 });
    await page.waitForTimeout(1500);
    const dir = await page.evaluate(() => document.documentElement.dir);
    ok(`${run.tag} direction`, dir === (run.lang === 'ar' ? 'rtl' : 'ltr'), dir);
    ok(`${run.tag} sections`, (await page.locator('#business, #features, #pricing, #faq, #contact').count()) === 5);
    ok(`${run.tag} plans`, (await page.locator('.px-plan').count()) === site.plans.length, String(await page.locator('.px-plan').count()));
    ok(`${run.tag} 12 business types`, (await page.locator('.px-biz').count()) === 12);
    await page.locator('.px-biz').nth(10).click(); await page.waitForTimeout(300);
    ok(`${run.tag} business card changes`, (await page.locator('.px-bizcard .px-checks li').count()) === 4);
    await page.locator('.px-toggle button').nth(1).click(); await page.waitForTimeout(200);
    await page.locator('.px-faq summary').first().click();
    if (run.w < 900) {
      await page.locator('.px-burger').click(); await page.waitForTimeout(300);
      ok(`${run.tag} phone menu opens`, await page.locator('.px-nav.open').isVisible());
      await page.locator('.px-nav.open button').first().click(); await page.waitForTimeout(500);
    }
    const raw = await page.evaluate((re) => (document.body.innerText.match(new RegExp(re, 'g')) || []).slice(0, 5).join(','), RAW.source);
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    const title = await page.title();
    await page.screenshot({ path: `/out/site-${run.tag}.png`, fullPage: true });
    await page.evaluate(() => window.scrollTo(0, 0));
    await page.screenshot({ path: `/out/site-${run.tag}-top.png` });
    ok(`${run.tag} clean`, overflow <= 2 && !raw && errors.length === 0 && title.includes('Paxve'), `overflow=${overflow} raw=${raw} ${errors.join(' | ').slice(0, 300)}`);
    if (run.tag === 'en') {
      await page.fill('.px-form input[autocomplete=name]', `QA Lead ${sfx}`);
      await page.fill('.px-form input[autocomplete=tel]', '70000111');
      await page.selectOption('.px-form select', 'restaurant');
      await page.click('.px-form button[type=submit]');
      await page.waitForTimeout(1500);
      ok('demo request sent', (await page.locator('.px-form.done').count()) === 1);
    }
    await ctx.close();
  }

  // ---------- QA organisation ----------
  const email = `qa-p6b-${sfx}@example.com`, pass = 'qa-p6b-pass-12';
  const qa = await request.newContext({ baseURL: base, extraHTTPHeaders: H });
  let r = await qa.post('/api/v1/signup', { data: { organisation: `QA Apps ${sfx}`, company_name: 'Paxve Apps Co', base_currency: 'USD', local_currency: 'LBP',
    template: 'lebanon', full_name: 'QA Owner', email, password: pass, locale: 'en' } });
  ok('signup', r.status() === 201);
  const cid = (await (await qa.get('/api/v1/companies')).json())[0].id;
  // a sign-up starts with its business type's apps only (phase 7); this check uses every app
  await qa.put(`/api/v1/companies/${cid}/apps`, { data: { apps: ['gen', 'acc', 'col', 'msg', 'stk', 'pur', 'net', 'sat', 'pos', 'hr', 'crm', 'svc', 'mfg', 'prj', 'flt', 'web', 'mkt', 'ai'] } });
  const P = `/api/v1/companies/${cid}`;
  await qa.post(`${P}/rates`, { data: { currency: 'LBP', date: '2026-01-01', rate: '89500' } });
  const cust = await (await qa.post(`${P}/customers`, { data: { name: 'Portal Customer' } })).json();
  const methods = await (await qa.get(`${P}/payment-methods`)).json();
  const today = new Date().toISOString().slice(0, 10);
  const usd = methods.find((m) => m.name === 'Cash USD');
  const inv = await qa.post(`${P}/invoices`, { data: { customer_id: cust.id, issue_date: today, currency: 'USD', lines: [{ description: 'Service', quantity: '1', unit_price: '50' }] } });
  ok('invoice', inv.status() === 201, String(inv.status()));
  r = await qa.post(`${P}/payments`, { data: { customer_id: cust.id, date: today, currency: 'USD', tenders: [{ method_id: usd.id, amount: '20' }] } });
  ok('payment', r.status() === 201, String(r.status()));
  const tok = (await (await qa.get(`${P}/customers/${cust.id}/portal`)).json()).token;
  const today2 = await (await qa.get(`${P}/owner/today`)).json();
  ok('owner today api', today2.collected.length >= 1 && today2.collected[0].amount.startsWith('20'), JSON.stringify(today2.collected));

  for (const run of [{ lang: 'ar', w: 390, h: 844, tag: 'ar-phone' }, { lang: 'en', w: 390, h: 844, tag: 'en-phone' }, { lang: 'fr', w: 1280, h: 800, tag: 'fr' }]) {
    const ctx = await browser.newContext({ viewport: { width: run.w, height: run.h }, locale: run.lang });
    const page = await ctx.newPage();
    const errors = [];
    page.on('console', (m) => { if (m.type() === 'error' && !/401|402|403|404|409|422|Failed to load resource/.test(m.text())) errors.push(m.text()); });
    page.on('pageerror', (e) => errors.push(String(e)));
    const check = async (name) => {
      const raw = await page.evaluate((re) => (document.body.innerText.match(new RegExp(re, 'g')) || []).slice(0, 5).join(','), RAW.source);
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
      await page.screenshot({ path: `/out/app-${run.tag}-${name}.png`, fullPage: run.w < 900 });
      ok(`${run.tag} ${name}`, overflow <= 2 && !raw && errors.length === 0, `overflow=${overflow} raw=${raw} ${errors.join(' | ').slice(0, 300)}`);
      errors.length = 0;
    };
    // the owner app start address opens the sign-in screen, then the owner screen
    await page.goto(`${base}/owner`);
    await page.waitForSelector('input[type=password]', { timeout: 20000 });
    ok(`${run.tag} /owner keeps its hash`, page.url().endsWith('#/owner'), page.url());
    ok(`${run.tag} owner manifest`, (await page.getAttribute('link[rel=manifest]', 'href')) === '/manifest-owner.webmanifest');
    await page.fill('input[type=email], input[name=email]', email);
    await page.fill('input[type=password]', pass);
    await page.keyboard.press('Enter');
    await page.waitForTimeout(2500);
    await page.evaluate(async (l) => { await fetch('/api/v1/me', { method: 'PATCH', credentials: 'include', headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'paxve' }, body: JSON.stringify({ locale: l }) }); }, run.lang);
    await page.goto(`${base}/#/owner`); await page.reload(); await page.waitForTimeout(2500);
    ok(`${run.tag} owner cards`, (await page.locator('.ow-card').count()) >= 4, String(await page.locator('.ow-card').count()));
    await check('owner-today');
    const tabsN = await page.locator('.ow-tabs button').count();
    for (let i = 1; i < tabsN; i++) { await page.locator('.ow-tabs button').nth(i).click(); await page.waitForTimeout(1200); await check(`owner-tab${i}`); }
    // field app
    await page.goto(`${base}/#/collect`); await page.waitForTimeout(2500);
    ok(`${run.tag} field tabs`, (await page.locator('[role=tab]').count()) >= 4);
    await check('field');
    // payroll option and supplier balances import (office screens)
    if (run.w > 900) {
      await page.goto(`${base}/#/hr/payroll`); await page.waitForTimeout(2500);
      ok(`${run.tag} payroll posting-date option`, (await page.locator('select option[value=period_end]').count()) === 1);
      await check('payroll');
      await page.goto(`${base}/#/import`); await page.waitForTimeout(2000);
      ok(`${run.tag} supplier balances import`, (await page.locator('text=' + (run.lang === 'fr' ? 'Soldes fournisseurs' : 'Supplier balances')).count()) >= 1);
      await check('import');
    }
    await ctx.close();
    // customer app: the portal, report a problem, then the remembered account at #/my
    const cctx = await browser.newContext({ viewport: { width: 390, height: 844 }, locale: run.lang });
    const cp = await cctx.newPage();
    cp.on('pageerror', (e) => errors.push(String(e)));
    await cp.goto(`${base}/#/p/${tok}`); await cp.waitForTimeout(2500);
    ok(`${run.tag} portal manifest`, (await cp.getAttribute('link[rel=manifest]', 'href')) === '/manifest-my.webmanifest');
    // the portal speaks the customer's language (the company default here), whatever the phone's language is
    const report = cp.locator('button:has-text("أبلغ عن مشكلة"), button:has-text("Report a problem"), button:has-text("Signaler un problème")');
    if (await report.count()) {
      await report.click(); await cp.waitForTimeout(400);
      await cp.locator('input[maxlength="120"]').fill(`QA problem ${run.tag}`);
      await cp.locator('button:has-text("إرسال"), button:has-text("Send"), button:has-text("Envoyer")').first().click();
      await cp.waitForTimeout(1500);
      ok(`${run.tag} portal ticket sent`, (await cp.locator(`text=QA problem ${run.tag}`).count()) >= 1);
    } else ok(`${run.tag} portal report button`, false);
    const ov = await cp.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    await cp.screenshot({ path: `/out/app-${run.tag}-portal.png`, fullPage: true });
    ok(`${run.tag} portal clean`, ov <= 2 && errors.length === 0, `overflow=${ov} ${errors.join(' | ').slice(0, 200)}`);
    errors.length = 0;
    // since the P7 security fix the link is kept only when asked ("Remember on this phone") or inside the installed app
    await cp.locator('button[type=button]').filter({ hasText: /تذكّر|Remember|Retenir/ }).first().click({ timeout: 3000 }).catch(() => {});
    await cp.goto(`${base}/my`); await cp.waitForTimeout(2500);
    ok(`${run.tag} /my opens the remembered account`, cp.url().includes(`#/p/${tok}`), cp.url());
    await cctx.close();
  }
  await browser.close();
  console.log(fails === 0 ? 'ALL PASS' : `${fails} FAILED`);
  process.exit(fails ? 1 : 0);
})();
