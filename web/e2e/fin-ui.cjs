// Screenshot + console/overflow check of the finishing-stage screens (run inside the Playwright image).
const { chromium } = require('playwright');
const pages = ['subs-change', 'supplier-prices', 'po', 'claims', 'custom-fields', 'mfg/orders', 'mfg/boms', 'mfg/scrap', 'hr-extra/roster', 'hr-extra/accruals', 'hr-extra/clock', 'hr-extra/devices', 'online-pay', 'einvoice', 'deferrals', 'apps'];
const runs = [
  { lang: 'ar', w: 1440, h: 900, tag: 'ar-desktop' },
  { lang: 'en', w: 1440, h: 900, tag: 'en-desktop' },
  { lang: 'fr', w: 1440, h: 900, tag: 'fr-desktop' },
  { lang: 'ar', w: 400, h: 860, tag: 'ar-phone' },
  { lang: 'fa', w: 400, h: 860, tag: 'fa-phone' },
];
(async () => {
  const browser = await chromium.launch();
  let problems = 0;
  for (const r of runs) {
    const ctx = await browser.newContext({ viewport: { width: r.w, height: r.h }, locale: r.lang });
    const page = await ctx.newPage();
    const errors = [];
    page.on('console', (m) => { if (m.type() === 'error' && !/401|Failed to load resource/.test(m.text())) errors.push(m.text()); });
    page.on('pageerror', (e) => errors.push(String(e)));
    await page.goto('https://paxve.com/#/login');
    await page.waitForSelector('input[type=password]');
    await page.fill('input[type=email], input[name=email]', process.env.PX_EMAIL);
    await page.fill('input[type=password]', process.env.PX_PASS);
    await page.keyboard.press('Enter');
    await page.waitForTimeout(2500);
    const api = (method, body) => page.evaluate(async ([m, b]) => { const r = await fetch('/api/v1/me', { method: m, credentials: 'include', headers: { 'Content-Type': 'application/json' }, body: b ? JSON.stringify(b) : undefined }); return r.json(); }, [method, body]);
    const orig = (await api('GET')).locale || 'en';
    await api('PATCH', { locale: r.lang });
    await page.reload(); await page.waitForTimeout(1500);
    for (const p of pages) {
      await page.goto(`https://paxve.com/#/${p}`);
      await page.waitForTimeout(2200);
      const raw = await page.evaluate(() => (document.body.innerText.match(/\b(fin|oa|mx|hr2)\.[a-z][\w.]+/g) || []).slice(0, 5).join(','));
      if (raw) errors.push('untranslated: ' + raw);
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
      const file = `/out/f-${r.tag}-${p.replace('/', '-')}.png`;
      await page.screenshot({ path: file });
      const dir = await page.evaluate(() => document.documentElement.dir);
      if (overflow > 2 || errors.length) problems++;
      console.log(`${r.tag} ${p}: dir=${dir} overflow=${overflow} errors=${errors.length ? errors.join(' | ').slice(0, 300) : 0}`);
      errors.length = 0;
    }
    await api('PATCH', { locale: orig });
    await ctx.close();
  }
  await browser.close();
  console.log(`problems=${problems}`);
})();
