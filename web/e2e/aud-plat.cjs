// Audit B: platform console (read-only browsing as the platform owner) + EN/FR visual samples as QA owner.
const { chromium } = require('playwright');
const fs = require('fs');
const base = 'https://paxve.com';
const st = JSON.parse(fs.readFileSync('/out/state.json', 'utf8'));
const pw = process.env.ADMPW;
async function login(browser, email, pass, lang, w = 1440, h = 900) {
  const ctx = await browser.newContext({ viewport: { width: w, height: h }, locale: lang, timezoneId: 'Asia/Beirut' });
  const page = await ctx.newPage(); page.http = []; page.errs = [];
  page.on('response', (r) => { if (r.status() >= 400 && r.status() !== 401 && r.url().includes('/api/')) page.http.push(r.status() + ' ' + r.url().replace(/[0-9a-f-]{36}/g, ':id').replace(base, '').slice(0, 80)); });
  page.on('console', (m) => { if (m.type() === 'error' && !/Failed to load/.test(m.text())) page.errs.push(m.text().slice(0, 150)); });
  page.on('pageerror', (e) => page.errs.push(String(e).slice(0, 150)));
  await page.goto(`${base}/#/login`); await page.waitForSelector('input[type=password]');
  await page.fill('input[type=email], input[name=email]', email); await page.fill('input[type=password]', pass);
  await page.keyboard.press('Enter'); await page.waitForTimeout(3000);
  return { ctx, page };
}
(async () => {
  const browser = await chromium.launch();
  if (pw) {
    const { ctx, page } = await login(browser, 'admin@paxve.com', pw, 'ar');
    for (const p of ['dash', 'orgs', 'requests', 'payments', 'plans']) {
      page.http.length = 0; page.errs.length = 0;
      await page.goto(`${base}/#/platform/${p}`); await page.waitForTimeout(2500);
      if (p === 'orgs') { await page.getByText(st.org).first().click().catch(() => {}); await page.waitForTimeout(1500); }
      await page.screenshot({ path: `/out/p-${p}.png` });
      const raw = await page.evaluate(() => (document.body.innerText.match(/\b(plat|platform|mx|errors)\.[a-z][\w.]+/g) || []).slice(0, 4).join(','));
      console.log('PLATFORM', p, 'http', page.http.join(' ; '), 'console', page.errs.join(' ; '), 'raw', raw);
    }
    await ctx.close();
  }
  for (const lang of ['en', 'fr']) {
    const { ctx, page } = await login(browser, st.email, st.pass, lang);
    await page.evaluate(async (l) => { await fetch('/api/v1/me', { method: 'PATCH', credentials: 'include', headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'paxve' }, body: JSON.stringify({ locale: l }) }); }, lang);
    await page.reload(); await page.waitForTimeout(1500);
    const sel = page.locator('.mx-title select').first();
    const opts = await sel.locator('option').allTextContents().catch(() => []);
    const demo = opts.find((o) => /Demo|تجريبية|démo/i.test(o)); if (demo) { await sel.selectOption({ label: demo }); await page.waitForTimeout(1500); }
    for (const s of ['dashboard', 'jibaya', 'trial-balance', 'hr/payroll', 'retail/promos', 'apps', 'stock-pro', 'my-sub']) {
      await page.goto(`${base}/#/${s}`); await page.waitForTimeout(2200);
      await page.screenshot({ path: `/out/v-${lang}-${s.replace('/', '-')}.png` });
    }
    await ctx.close();
  }
  await browser.close();
  console.log('done');
})();
