// New "two currencies" logo: site header/footer, login, app shell, favicon/icons served. Screens go to /web/e2e/screens/logo.
const { chromium } = require('playwright');
const fs = require('fs');
const base = 'https://paxve.com';
const out = '/web/e2e/screens/logo';
fs.mkdirSync(out, { recursive: true });
let fails = 0;
const ok = (n, c, x = '') => { console.log(`${c ? 'PASS' : 'FAIL'} ${n} ${x}`); if (!c) fails++; };
(async () => {
  const browser = await chromium.launch();
  for (const [lang, w, h] of [['ar', 1440, 900], ['en', 1440, 900], ['ar', 390, 844], ['en', 390, 844]]) {
    const ctx = await browser.newContext({ viewport: { width: w, height: h }, locale: lang, deviceScaleFactor: 2 });
    await ctx.addInitScript((l) => { try { localStorage.setItem('paxve.lang', l); localStorage.setItem('i18nextLng', l); } catch (e) {} }, lang);
    const page = await ctx.newPage();
    const errs = []; page.on('pageerror', (e) => errs.push(String(e)));
    await page.goto(`${base}/#/`); await page.waitForSelector('.sx-brand .pxv-mark', { timeout: 30000 });
    await page.waitForTimeout(800);
    ok(`site mark ${lang} ${w}`, await page.locator('.sx-brand .pxv-mark').first().isVisible());
    ok(`no old P mark ${lang} ${w}`, (await page.locator('.sx-mark').count()) === 0);
    await page.screenshot({ path: `${out}/site-${lang}-${w}.png`, clip: { x: 0, y: 0, width: w, height: Math.min(h, 220) } });
    if (w === 1440 && lang === 'ar') {
      const foot = page.locator('.sx-footbrand'); await foot.scrollIntoViewIfNeeded(); await page.waitForTimeout(400);
      await foot.screenshot({ path: `${out}/footer-ar.png` });
      const map = page.locator('svg:has(text:text("بيروت"))').first();
      if (await map.count()) { await map.scrollIntoViewIfNeeded(); await page.waitForTimeout(600); await map.screenshot({ path: `${out}/map-ar.png` }); }
    }
    ok(`no page errors ${lang} ${w}`, errs.length === 0, errs.join(' | '));
    await ctx.close();
  }
  // login screen
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 }, locale: 'ar', deviceScaleFactor: 2 });
  const page = await ctx.newPage();
  await page.goto(`${base}/#/login`); await page.waitForSelector('.pxv-lockup', { timeout: 30000 }); await page.waitForTimeout(600);
  await page.screenshot({ path: `${out}/login.png` });
  ok('login lockup', await page.locator('.pxv-lockup').first().isVisible());
  // app shell header
  {
    const sfx = Date.now().toString(36);
    const email = `qa-logo-${sfx}@example.com`, password = 'Qa-logo-pass-123';
    const sr = await ctx.request.post(`${base}/api/v1/signup`, { headers: { 'X-Requested-With': 'paxve', 'Content-Type': 'application/json' },
      data: { organisation: `QA Logo ${sfx}`, company_name: 'QA Logo', base_currency: 'USD', local_currency: 'LBP', template: 'lebanon', full_name: 'QA Logo', email, password, locale: 'ar', business_type: 'generator', phone: '', rate: '89500' } });
    ok('qa signup', sr.ok(), String(sr.status()));
    const lr = await ctx.request.post(`${base}/api/v1/auth/login`, { headers: { 'X-Requested-With': 'paxve', 'Content-Type': 'application/json' }, data: { email, password } });
    ok('qa login', lr.ok(), String(lr.status()));
    console.log('QA_ORG', `QA Logo ${sfx}`, (await lr.text()).slice(0, 200), (await ctx.cookies()).map((c) => c.name).join(','));
    const meS = []; const pg = await ctx.newPage();
    pg.on('response', (r) => { if (r.url().includes('/api/v1/me')) meS.push(r.status()); });
    await pg.goto(`${base}/#/dashboard`); await pg.waitForTimeout(6000); console.log('me', meS.join(','));
    await pg.screenshot({ path: `${out}/after-login.png` });
    await pg.waitForSelector('.mx-title .pxv-mark', { timeout: 30000 }).catch(() => {}); await pg.waitForTimeout(1500);
    await pg.screenshot({ path: `${out}/shell.png`, clip: { x: 0, y: 0, width: 1280, height: 160 } });
    ok('shell mark', await pg.locator('.mx-title .pxv-mark').isVisible());
  }
  // assets
  for (const p of ['/favicon.svg', '/icons/main-512.png', '/icons/field-192-maskable.png', '/icons/apple-touch-icon.png', '/og.png', '/brand/paxve-logo.svg', '/brand/paxve-mark-mono.svg']) {
    const r = await ctx.request.get(base + p); ok(`asset ${p}`, r.ok(), String(r.status()));
  }
  const fav = await (await ctx.request.get(base + '/favicon.svg?v=8')).text();
  ok('favicon is the badge mark', fav.includes('M10.4 0H36.6') && fav.includes('M33.1 23.6H53.6'));
  const html = await (await ctx.request.get(base + '/')).text();
  ok('index links versioned favicon', html.includes('/favicon.svg?v=8'));
  const sw = await (await ctx.request.get(base + '/sw.js?fresh=' + Date.now())).text();
  ok('sw cache v11', sw.includes('paxve-shell-v11'));
  await browser.close();
  console.log(fails ? `FAILED ${fails}` : 'ALL PASS');
  process.exit(fails ? 1 : 0);
})();
