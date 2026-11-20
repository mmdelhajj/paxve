// No "Magic" for customers: a normal tenant (all apps on) opens every ribbon tab and every tile in Arabic, English
// and French (desktop) and Arabic on a phone, plus the public site, and no screen may show "Magic" / "ماجيك".
// The legacy import screen and API are refused for a tenant admin.
//   docker run --rm --ipc=host -v /srv/paxve/web:/web -v /tmp/nm-out:/out -w /web/e2e mcr.microsoft.com/playwright:v1.65.1-noble node nomagic-ui.cjs
const { chromium, request } = require('playwright');
const base = 'https://paxve.com';
const H = { 'X-Requested-With': 'paxve', 'Content-Type': 'application/json' };
const BAD = /magic|ماجيك|ماجك/i;
let fails = 0;
const ok = (name, cond, extra = '') => { console.log(`${cond ? 'PASS' : 'FAIL'} ${name} ${extra}`); if (!cond) fails++; };

(async () => {
  const sfx = Date.now().toString(36).slice(-5);
  const email = `qa-nm-${sfx}@example.com`, pass = 'Qa-nm-pass-123';
  const qa = await request.newContext({ baseURL: base, extraHTTPHeaders: H });
  let r = await qa.post('/api/v1/signup', { data: { organisation: `QA Plain ${sfx}`, company_name: 'QA Plain', base_currency: 'USD', local_currency: 'LBP',
    template: 'lebanon', full_name: 'QA Owner', email, password: pass, locale: 'ar', business_type: 'generator', rate: '89500' } });
  ok('signup', r.status() === 201, r.status() === 201 ? '' : await r.text());
  const cid = (await (await qa.get('/api/v1/companies')).json())[0].id;
  r = await qa.put(`/api/v1/companies/${cid}/apps`, { data: { apps: ['gen', 'acc', 'col', 'msg', 'stk', 'pur', 'net', 'sat', 'pos', 'hr', 'crm', 'svc', 'mfg', 'prj', 'flt', 'web', 'mkt', 'ai'] } });
  ok('all apps on', r.status() === 200, r.status() === 200 ? '' : (await r.text()).slice(0, 200));

  // API: the legacy import is refused for a tenant admin
  for (const [m, p] of [['GET', '/api/v1/magic/imports'], ['POST', '/api/v1/magic/imports']]) {
    r = m === 'GET' ? await qa.get(p) : await qa.post(p, { data: { file_name: 'x.zip', size: 10, sha256: '0'.repeat(64) } });
    ok(`tenant ${m} ${p} refused`, r.status() === 403, String(r.status()));
  }

  const browser = await chromium.launch();
  const runs = [
    { lang: 'ar', w: 1440, h: 900, tag: 'ar' },
    { lang: 'en', w: 1440, h: 900, tag: 'en' },
    { lang: 'fr', w: 1440, h: 900, tag: 'fr' },
    { lang: 'ar', w: 400, h: 860, tag: 'ar-phone', tabsOnly: true },
  ];
  for (const run of runs) {
    await qa.patch('/api/v1/me', { data: { locale: run.lang } });
    const ctx = await browser.newContext({ viewport: { width: run.w, height: run.h }, locale: run.lang });
    const page = await ctx.newPage();
    const errs = [];
    page.on('pageerror', (e) => errs.push(String(e).slice(0, 160)));
    await page.goto(`${base}/#/login`);
    await page.waitForSelector('input[type=password]');
    await page.fill('input[type=email], input[name=email]', email);
    await page.fill('input[type=password]', pass);
    await page.keyboard.press('Enter');
    await page.waitForSelector('.mx-ribbon', { timeout: 15000 });
    await page.waitForTimeout(1500);
    const hits = [];
    const check = async (where) => {
      const txt = await page.evaluate(() => document.body.innerText + ' ' + [...document.querySelectorAll('[title],[aria-label],[placeholder]')]
        .map((e) => (e.getAttribute('title') || '') + ' ' + (e.getAttribute('aria-label') || '') + ' ' + (e.getAttribute('placeholder') || '')).join(' ') + ' ' + document.title);
      const m = txt.match(new RegExp('.{0,40}(' + BAD.source + ').{0,40}', 'i'));
      if (m) hits.push(`${where}: …${m[0].replace(/\s+/g, ' ')}…`);
    };
    await check('home');
    const nTabs = await page.locator('.mx-rtab').count();
    let tiles = 0;
    for (let i = 0; i < nTabs; i++) {
      if (!(await page.locator('.mx-rtab').count())) {
        await page.goto(`${base}/#/dashboard`);
        await page.waitForSelector('.mx-ribbon', { timeout: 15000 }).catch(() => {});
        await page.waitForTimeout(800);
      }
      const tab = page.locator('.mx-rtab').nth(i);
      await tab.scrollIntoViewIfNeeded().catch(() => {});
      await tab.click({ timeout: 8000 }).catch(() => {});
      await page.waitForTimeout(300);
      const tabName = (await tab.innerText()).trim();
      await check(`tab ${tabName}`);
      if (run.tabsOnly) continue;
      const nTiles = await page.locator('.mx-tiles .mx-tile').count();
      for (let j = 0; j < nTiles; j++) {
        // a tile can open a full-screen mode (till, collector…): come back to the ribbon first
        if (!(await page.locator('.mx-rtab').count())) {
          await page.goto(`${base}/#/dashboard`);
          await page.waitForSelector('.mx-ribbon', { timeout: 15000 }).catch(() => {});
          await page.waitForTimeout(800);
        }
        if (!(await page.locator('.mx-tiles').isVisible().catch(() => false))) {
          const tb = page.locator('.mx-rtab').nth(i);
          await tb.scrollIntoViewIfNeeded().catch(() => {});
          await tb.click({ timeout: 5000 }).catch(() => {});
          await page.waitForTimeout(250);
        }
        if (j >= await page.locator('.mx-tiles .mx-tile').count()) continue;
        const tile = page.locator('.mx-tiles .mx-tile').nth(j);
        const name = (await tile.innerText().catch(() => '?')).trim();
        await tile.click().catch(() => {});
        await page.waitForTimeout(1300);
        await check(`${tabName} › ${name}`);
        tiles++;
        // close the window again so the window tabs don't pile up
        await page.keyboard.press('Escape').catch(() => {});
      }
    }
    // the legacy import route itself shows nothing for a tenant
    await page.goto(`${base}/#/magic-import`); await page.waitForTimeout(1500);
    await check('#/magic-import');
    await page.goto(`${base}/#/translations`); await page.waitForTimeout(2500);
    await check('#/translations');
    await page.screenshot({ path: `/out/nm-${run.tag}-translations.png` }).catch(() => {});
    ok(`${run.tag}: ${nTabs} tabs, ${tiles} screens, no "Magic"`, hits.length === 0, hits.slice(0, 8).join(' | '));
    ok(`${run.tag}: no page errors`, errs.length === 0, errs.slice(0, 3).join(' | '));
    await ctx.close();
  }

  // public site, signed out
  for (const lang of ['ar', 'en', 'fr']) {
    const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: lang });
    const page = await ctx.newPage();
    await page.goto(`${base}/?lang=${lang}`); await page.waitForTimeout(2500);
    const html = await page.content();
    const txt = await page.evaluate(() => document.body.innerText);
    const m = (txt + ' ' + html.replace(/<script[\s\S]*?<\/script>/g, '')).match(new RegExp('.{0,40}(' + BAD.source + ').{0,40}', 'i'));
    ok(`site ${lang}: no "Magic"`, !m, m ? m[0] : '');
    await ctx.close();
  }
  await browser.close();

  // switch the QA org off
  r = await qa.patch('/api/v1/me', { data: { locale: 'ar' } });
  console.log(`QA org: QA Plain ${sfx} (${email})`);
  console.log(fails ? `FAILED ${fails}` : 'ALL PASSED');
  process.exit(fails ? 1 : 0);
})();
