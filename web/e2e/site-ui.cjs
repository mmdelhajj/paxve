// Live check of the marketing site (paxve.com, signed out): home, the 12 business-type pages, privacy / terms.
//  - every language: home renders, no page errors, no sideways scrolling, no raw "site.*" keys
//  - Arabic + English on phone (390), tablet (820) and desktop (1440)
//  - header mega menu, mobile drawer, hero product cards, product rows, field map, API code, pricing toggle
//  - every product screenshot loads; the demo form creates a lead; the trial button opens sign-up
//   docker run --rm --ipc=host -v /srv/paxve/web:/web -v /tmp/site-out:/out -w /web/e2e mcr.microsoft.com/playwright:v1.65.1-noble node site-ui.cjs
const { chromium } = require('playwright');
const base = 'https://paxve.com';
const LANGS = ['ar', 'en', 'fr', 'tr', 'fa', 'ur', 'ckb', 'ru', 'pt', 'es', 'de'];
const BIZ = ['generator', 'internet', 'satellite', 'water', 'supermarket', 'clothing', 'electronics', 'furniture', 'wholesale', 'restaurant', 'pharmacy', 'factory'];
let fails = 0;
const ok = (name, cond, extra = '') => { console.log(`${cond ? 'PASS' : 'FAIL'} ${name} ${extra}`); if (!cond) fails++; };

async function open(browser, lang, w, hash = '') {
  const ctx = await browser.newContext({ viewport: { width: w, height: w < 500 ? 844 : 900 }, isMobile: w < 500, hasTouch: w < 500 });
  const page = await ctx.newPage();
  const errors = [];
  // the signed-out "who am I" call answers 401 by design; anything else is a real error
  page.on('console', (m) => { if (m.type() === 'error' && !/status of 401/.test(m.text())) errors.push(m.text()); });
  page.on('pageerror', (e) => errors.push(String(e)));
  await page.goto(`${base}/`);
  await page.evaluate((l) => { try { localStorage.setItem('paxve.lang', l); } catch { /* */ } }, lang);
  await page.goto(`${base}/#/${hash}`);
  await page.reload();
  await page.waitForSelector('.sx-top', { timeout: 30000 });
  await page.waitForTimeout(1200);
  return { ctx, page, errors };
}
async function common(page, errors, label) {
  const over = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
  ok(`${label}: no sideways scroll`, over <= 1, `overflow=${over}`);
  const raw = await page.evaluate(() => (document.body.innerText.match(/\bsite\.[a-zA-Z]+[.\w]*/g) || []).slice(0, 5));
  ok(`${label}: no raw keys`, raw.length === 0, raw.join(' '));
  ok(`${label}: no page errors`, errors.length === 0, errors.slice(0, 3).join(' | '));
}
async function loadAllImages(page) {
  const h = await page.evaluate(() => document.body.scrollHeight);
  for (let y = 0; y < h; y += 600) { await page.evaluate((yy) => window.scrollTo(0, yy), y); await page.waitForTimeout(120); }
  await page.waitForTimeout(800);
  return page.evaluate(() => [...document.querySelectorAll('.sx img')].filter((i) => !i.complete || i.naturalWidth === 0).map((i) => i.currentSrc || i.src));
}

(async () => {
  const browser = await chromium.launch();

  // 1. every language, desktop
  for (const lang of LANGS) {
    const { ctx, page, errors } = await open(browser, lang, 1440);
    const h1 = (await page.locator('.sx-hero h1').innerText()).trim();
    ok(`${lang}: hero headline`, h1.length > 8, h1.slice(0, 60));
    ok(`${lang}: 12 business types`, (await page.locator('.sx-type').count()) === 12);
    ok(`${lang}: hero cards, product rows, field map, API code`, (await page.locator('.sx-card').count()) === 4 && (await page.locator('.sx-rows .sx-row').count()) === 5
      && (await page.locator('.sx-map svg').count()) === 1 && (await page.locator('.sx-code pre').count()) === 1);
    ok(`${lang}: direction`, (await page.locator('.sx').getAttribute('dir')) === (['ar', 'fa', 'ur', 'ckb'].includes(lang) ? 'rtl' : 'ltr'));
    await common(page, errors, `${lang} desktop`);
    await ctx.close();
  }

  // 2. Arabic + English at phone / tablet / desktop, with screenshots and image loading
  for (const lang of ['ar', 'en']) {
    for (const w of [390, 820, 1440]) {
      const { ctx, page, errors } = await open(browser, lang, w);
      const broken = await loadAllImages(page);
      ok(`${lang} ${w}: all screenshots load`, broken.length === 0, broken.join(' '));
      await page.evaluate(() => window.scrollTo(0, 0));
      await page.waitForTimeout(400);
      await page.screenshot({ path: `/out/site-${lang}-${w}.png`, fullPage: true });
      await common(page, errors, `${lang} ${w}`);
      if (w === 390) {
        await page.locator('.sx-burger').first().click();
        await page.waitForTimeout(400);
        ok(`${lang} phone: drawer opens`, await page.locator('.sx-drawer').isVisible());
        ok(`${lang} phone: drawer lists the 12 business types`, (await page.locator('.sx-drawer .sx-dgrid a').count()) === 12);
        await page.screenshot({ path: `/out/site-${lang}-drawer.png` });
        await page.locator('.sx-drawer .sx-burger').click();
        ok(`${lang} phone: drawer closes`, (await page.locator('.sx-drawer').count()) === 0);
      }
      if (w === 1440) {
        await page.locator('.sx-nav button[aria-controls=sx-mega]').click();
        await page.waitForTimeout(300);
        ok(`${lang}: mega menu with 12 types`, (await page.locator('.sx-mega .sx-megaitem').count()) === 12);
        await page.screenshot({ path: `/out/site-${lang}-mega.png` });
        await page.keyboard.press('Escape');
        ok(`${lang}: mega menu closes on Escape`, (await page.locator('.sx-mega').count()) === 0);
        // pricing toggle (plans come from the platform)
        const plans = await page.locator('.sx-plan-h .pn').count();
        ok(`${lang}: plans from the platform`, plans >= 1, `${plans}`);
        const yearPrice = await page.locator('.sx-plan-h .pr').first().innerText();
        await page.locator('.sx-cycle button').first().click();
        const monthPrice = await page.locator('.sx-plan-h .pr').first().innerText();
        ok(`${lang}: monthly / yearly toggle changes the price`, yearPrice !== monthPrice, `${yearPrice} → ${monthPrice}`);
      }
      await ctx.close();
    }
  }

  // 3. the 12 business-type pages (Arabic desktop, English phone) and the legal pages
  for (const b of BIZ) {
    for (const [lang, w] of [['ar', 1440], ['en', 390]]) {
      const { ctx, page, errors } = await open(browser, lang, w, `for/${b}`);
      ok(`${b} ${lang}: page`, (await page.locator('.sx-pains tbody tr').count()) === 3 && (await page.locator('.sx-ihero h1').innerText()).length > 8);
      ok(`${b} ${lang}: other types listed`, (await page.locator('.sx-others a').count()) === 11);
      const broken = await loadAllImages(page);
      ok(`${b} ${lang}: screenshots load`, broken.length === 0, broken.join(' '));
      await common(page, errors, `${b} ${lang}`);
      if (b === 'generator' || b === 'pharmacy') await page.screenshot({ path: `/out/for-${b}-${lang}.png`, fullPage: true });
      await ctx.close();
    }
  }
  for (const doc of ['privacy', 'terms']) {
    const { ctx, page, errors } = await open(browser, 'ar', 1440, doc);
    ok(`${doc}: 6 sections`, (await page.locator('.sx-legal section').count()) === 6);
    await common(page, errors, doc);
    await ctx.close();
  }

  // 4. demo request → lead, and the trial button opens sign-up
  {
    const { ctx, page, errors } = await open(browser, 'ar', 1440);
    await page.locator('#contact').scrollIntoViewIfNeeded();
    await page.fill('#sx-f-name', 'QA Site Check');
    await page.fill('#sx-f-phone', '+961 70 000 999');
    await page.fill('#sx-f-msg', 'QA: automated site check, please ignore');
    await page.locator('#contact form button[type=submit]').click();
    await page.waitForTimeout(2500);
    ok('demo form: thank-you shown', await page.locator('#contact .sx-form.done').isVisible());
    await page.evaluate(() => window.scrollTo(0, 0));
    await page.locator('.sx-hero a[href="#/signup"]').first().click();
    await page.waitForTimeout(2500);
    ok('trial button opens sign-up', /#\/signup/.test(page.url()) && (await page.locator('input[type=password]').count()) > 0, page.url());
    ok('demo / sign-up: no page errors', errors.length === 0, errors.join(' | '));
    await ctx.close();
  }

  await browser.close();
  console.log(fails ? `FAILED ${fails}` : 'ALL PASS');
  process.exit(fails ? 1 : 0);
})();
