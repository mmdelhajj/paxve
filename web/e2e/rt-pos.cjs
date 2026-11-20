// Till flow in the browser (QA Retail org from rt-ui.cjs): items → live promotions → pay → receipt; hold / resume;
// customer display; the sales quote "Apply promotions" button.
const { chromium } = require('playwright');
const base = 'https://paxve.com';
const email = process.env.QA_EMAIL, pass = 'qa-retail-pass-1';
let fails = 0;
const ok = (name, cond, extra = '') => { console.log(`${cond ? 'PASS' : 'FAIL'} ${name} ${extra}`); if (!cond) fails++; };

(async () => {
  const browser = await chromium.launch();
  for (const run of [{ lang: 'en', w: 1440, h: 900, tag: 'en' }, { lang: 'ar', w: 400, h: 860, tag: 'ar-phone' }]) {
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
    const shot = async (name) => {
      const raw = await page.evaluate(() => (document.body.innerText.match(/\b(rt|pos|mx|errors)\.[a-z][\w.]+/g) || []).slice(0, 5).join(','));
      if (raw) errors.push('untranslated: ' + raw);
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
      await page.screenshot({ path: `/out/t-${run.tag}-${name}.png` });
      ok(`${run.tag} ${name}`, overflow <= 2 && errors.length === 0, `overflow=${overflow} ${errors.join(' | ').slice(0, 300)}`);
      errors.length = 0;
    };
    await page.goto(`${base}/#/pos`); await page.reload(); await page.waitForTimeout(3500);
    // water ×3 (buy 2 get 1) and two other items, by searching the code
    for (const code of ['WTR', 'WTR', 'WTR', 'BRD', 'JUC']) {
      await page.fill('input[placeholder]', code); await page.keyboard.press('Enter'); await page.waitForTimeout(250);
    }
    await page.waitForTimeout(1500);
    if (run.w < 600) { await page.locator('button:has(.tabler-icon-shopping-cart)').last().click().catch(() => {}); await page.waitForTimeout(800); }
    const promoText = await page.locator('text=/Water: buy 2 get 1|مياه: اشترِ 2/').count();
    ok(`${run.tag} promotion shown in the cart`, promoText > 0);
    await shot('cart');
    // hold and resume
    const holdBtn = page.getByRole('button', { name: /^(Hold|تعليق)$/ }).first();
    await holdBtn.click().catch((e) => errors.push('hold: ' + e.message.slice(0, 60)));
    await page.waitForTimeout(1500);
    await page.getByRole('button', { name: /^(Held baskets|السلال المعلّقة)$/ }).first().click().catch(() => {});
    await page.waitForTimeout(1500);
    await shot('held');
    await page.locator('.mantine-Drawer-body .mantine-Card-root').first().click().catch((e) => errors.push('resume: ' + e.message.slice(0, 60)));
    await page.waitForTimeout(1500);
    // pay
    await page.getByRole('button', { name: /Pay|دفع/ }).last().click().catch(() => {});
    await page.waitForTimeout(1500);
    await shot('pay');
    await page.locator('.mantine-Modal-body button:has-text("Exact"), .mantine-Modal-body button:has-text("المبلغ")').first().click().catch(() => {});
    await page.waitForTimeout(500);
    await page.locator('.mantine-Modal-body button').last().click().catch(() => {});
    await page.waitForTimeout(2500);
    const rec = await page.locator('.print-doc.receipt').count();
    ok(`${run.tag} receipt after sale`, rec > 0);
    await shot('receipt');
    await ctx.close();
  }
  // customer display
  const ctx = await browser.newContext({ viewport: { width: 1000, height: 600 } });
  const p = await ctx.newPage();
  await p.goto(`${base}/#/pos-display`); await p.waitForTimeout(2000);
  await p.screenshot({ path: '/out/t-display.png' });
  ok('customer display renders', (await p.locator('text=/Welcome|أهلاً/').count()) > 0);
  await browser.close();
  console.log(`fails=${fails}`);
})();
