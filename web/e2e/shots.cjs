// Screenshots of key screens for the visual polish pass. usage: node shots.cjs <prefix> [runs]
const { chromium } = require('playwright');
const base = 'https://paxve.com';
const prefix = process.argv[2] || 'shot';
const only = process.argv[3] || 'ar';
const runs = [
  { lang: 'ar', w: 1440, h: 900, tag: 'ar' },
  { lang: 'en', w: 1440, h: 900, tag: 'en' },
  { lang: 'fr', w: 1440, h: 900, tag: 'fr' },
  { lang: 'ar', w: 400, h: 860, tag: 'ar-phone' },
].filter((r) => only === 'all' || r.tag === only);
const screens = ['dashboard', 'apps', 'jibaya', 'subscribers', 'invoices', 'trial-balance', 'pos'];

(async () => {
  const browser = await chromium.launch();
  let fails = 0;
  for (const run of runs) {
    const ctx = await browser.newContext({ viewport: { width: run.w, height: run.h }, locale: run.lang });
    const page = await ctx.newPage();
    const errors = [];
    page.on('console', (m) => { if (m.type() === 'error' && !/401|402|403|404|Failed to load resource/.test(m.text())) errors.push(m.text()); });
    page.on('pageerror', (e) => errors.push(String(e)));
    await page.goto(`${base}/#/login`);
    await page.waitForTimeout(1200);
    await page.screenshot({ path: `/out/${prefix}-${run.tag}-login.png` });
    await page.waitForSelector('input[type=password]');
    await page.fill('input[type=email], input[name=email]', 'demo@paxve.com');
    await page.fill('input[type=password]', 'bz7xw2qht33s');
    await page.keyboard.press('Enter');
    await page.waitForTimeout(2500);
    await page.evaluate(async (l) => { await fetch('/api/v1/me', { method: 'PATCH', credentials: 'include', headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'paxve' }, body: JSON.stringify({ locale: l }) }); }, run.lang);
    await page.reload(); await page.waitForTimeout(1500);
    // the demo company with data
    const sel = page.locator('.mx-title select').first();
    const opts = await sel.locator('option').allTextContents().catch(() => []);
    const demo = opts.find((o) => /Demo|تجريبية/.test(o));
    if (demo) { await sel.selectOption({ label: demo }).catch(() => {}); await page.waitForTimeout(1500); }
    for (const s of screens) {
      await page.goto(`${base}/#/${s}`); await page.waitForTimeout(2200);
      if (s === 'dashboard') {
        await page.locator('.mx-rtab').nth(1).click().catch(() => {}); await page.waitForTimeout(500);
      }
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
      await page.screenshot({ path: `/out/${prefix}-${run.tag}-${s}.png` });
      const bad = overflow > 2 || errors.length > 0;
      if (bad) fails++;
      console.log(`${bad ? 'FAIL' : 'PASS'} ${run.tag} ${s} overflow=${overflow} ${errors.join(' | ').slice(0, 300)}`);
      errors.length = 0;
      if (s === 'pos') { await page.goto(`${base}/#/dashboard`); await page.waitForTimeout(800); }
    }
    await ctx.close();
  }
  await browser.close();
  console.log(`fails=${fails}`);
})();
