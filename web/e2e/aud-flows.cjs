// Audit B, interactive flows in the browser (AR, owner, demo company): empty-save validation on every form screen,
// export from grids, print buttons, collection keyboard flow, then role walks.
const { chromium } = require('playwright');
const fs = require('fs');
const base = 'https://paxve.com';
const st = JSON.parse(fs.readFileSync('/out/state.json', 'utf8'));
const parts = (process.argv[2] || 'forms').split(',');
const log = (...a) => console.log(...a);

async function open(browser, email, pass, lang = 'ar', w = 1440, h = 900, pickDemo = true) {
  const ctx = await browser.newContext({ viewport: { width: w, height: h }, locale: lang, timezoneId: 'Asia/Beirut', acceptDownloads: true });
  const page = await ctx.newPage();
  page.errors = []; page.http = [];
  page.on('console', (m) => { if (m.type() === 'error' && !/Failed to load resource/.test(m.text())) page.errors.push(m.text().slice(0, 200)); });
  page.on('pageerror', (e) => page.errors.push('PAGEERROR ' + String(e).slice(0, 200)));
  page.on('response', (r) => { const s = r.status(); if (s >= 400 && s !== 401 && r.url().includes('/api/')) page.http.push(`${s} ${r.request().method()} ${r.url().replace(base, '').replace(/[0-9a-f-]{36}/g, ':id').slice(0, 90)}`); });
  page.on('dialog', (d) => d.dismiss().catch(() => {}));
  await page.goto(`${base}/#/login`);
  await page.waitForSelector('input[type=password]');
  await page.fill('input[type=email], input[name=email]', email);
  await page.fill('input[type=password]', pass);
  await page.keyboard.press('Enter');
  await page.waitForTimeout(3000);
  if (pickDemo) {
    const sel = page.locator('.mx-title select').first();
    const opts = await sel.locator('option').allTextContents().catch(() => []);
    const demo = opts.find((o) => /Demo|تجريبية/.test(o));
    if (demo) { await sel.selectOption({ label: demo }).catch(() => {}); await page.waitForTimeout(1500); }
  }
  return { ctx, page };
}
const msgs = (page) => page.evaluate(() => [...document.querySelectorAll('.mx-flash, .mantine-Notification-root, .mantine-Notifications-root [role=alert], .mantine-InputWrapper-error, .mx-err, [role=alert]')]
  .map((e) => e.textContent.trim()).filter(Boolean).slice(0, 4).join(' | '));

(async () => {
  const browser = await chromium.launch();
  let shared = null; const owner = async () => (shared ??= (await open(browser, st.email, st.pass)).page);
  if (parts.includes('forms')) {
    const page = await owner();
    const screens = ('subscribers customers voucher/receipt voucher/expense voucher/payment cheques exchange network billing-setup tariffs readings service service/maintenance dunning ' +
      'accounts journal cost-centers budgets recurring assets fx-reval deferrals items stock-pro/counts stock-pro/prices stock-pro/landed mfg/orders mfg/boms mfg/scrap suppliers po supplier-prices bills expenses ' +
      'crm activities quotes orders pos-setup retail/promos retail/loyalty retail/gift retail/variants hr/people hr/leave hr/payroll claims hr-extra/roster hr-extra/devices custom-fields companies users developers').split(/\s+/);
    for (const s of screens) {
      await page.goto(`${base}/#/${s}`); await page.waitForTimeout(2000);
      page.errors.length = 0; page.http.length = 0;
      const newBtn = page.locator('button:visible').filter({ hasText: /^\s*(\+\s*)?(جديد|إضافة|أضف|New|Add)/ }).first();
      let clickedNew = false;
      if (await newBtn.count()) { await newBtn.click().catch(() => {}); clickedNew = true; await page.waitForTimeout(900); }
      const save = page.locator('button:visible').filter({ hasText: /^\s*(حفظ|Save|إنشاء|Create)/ }).first();
      let res = 'no save button';
      if (await save.count()) {
        await save.click().catch(() => {}); await page.waitForTimeout(1300);
        res = (await msgs(page)) || '(no visible message)';
      }
      const shot = `/out/f-${s.replace('/', '-')}.png`;
      await page.screenshot({ path: shot }).catch(() => {});
      log(`FORM ${s} new=${clickedNew} -> ${res.slice(0, 220)} || http: ${[...new Set(page.http)].join(' ; ').slice(0, 220)} || console: ${[...new Set(page.errors)].join(' ; ').slice(0, 200)}`);
      await page.keyboard.press('Escape').catch(() => {});
    }
  }
  if (parts.includes('export')) {
    const page = await owner();
    for (const s of ['subscribers', 'invoices', 'payments', 'trial-balance', 'aging', 'journal', 'items', 'stock-valuation', 'profit-loss', 'balance-sheet', 'collections', 'vat-report', 'sales-report']) {
      await page.goto(`${base}/#/${s}`); await page.waitForTimeout(2300);
      const grid = page.locator('.mx-grid, table, [role=grid]').first();
      let out = '';
      if (await grid.count()) {
        await grid.click({ button: 'right', position: { x: 60, y: 60 } }).catch(() => {});
        await page.waitForTimeout(500);
        const items = await page.locator('.mx-ctx button, .mx-menu button, [role=menu] button, [role=menuitem]').allTextContents().catch(() => []);
        out += 'menu=[' + items.join(',').slice(0, 120) + '] ';
        const ex = page.locator('.mx-ctx button, .mx-menu button, [role=menu] button, [role=menuitem]').filter({ hasText: /Excel|CSV|xlsx|تصدير/ }).first();
        if (await ex.count()) {
          const dl = page.waitForEvent('download', { timeout: 5000 }).catch(() => null);
          await ex.click().catch(() => {});
          const d = await dl;
          if (d) { const p = `/out/dl-${s}-${d.suggestedFilename()}`; await d.saveAs(p).catch(() => {}); const size = fs.existsSync(p) ? fs.statSync(p).size : -1; out += `download=${d.suggestedFilename()} size=${size}`; }
          else out += 'export clicked, no download';
        } else out += 'no export item';
        await page.keyboard.press('Escape').catch(() => {});
      } else out = 'no grid';
      const pb = page.locator('button:visible').filter({ hasText: /طباعة|Print/ }).first();
      if (await pb.count()) {
        const pop = page.waitForEvent('popup', { timeout: 3000 }).catch(() => null);
        await pb.click().catch(() => {}); const pp = await pop; await page.waitForTimeout(800);
        out += ` | print: ${pp ? 'popup ' + (await pp.title().catch(() => '')) : 'no popup'}`;
        if (pp) await pp.close().catch(() => {});
      } else out += ' | no print button';
      log(`EXPORT ${s} ${out} || http: ${[...new Set(page.http)].join(' ; ').slice(0, 200)} || console: ${[...new Set(page.errors)].join(' ; ').slice(0, 160)}`);
      page.errors.length = 0; page.http.length = 0;
    }
  }
  if (parts.includes('jibaya')) {
    const page = await owner();
    await page.goto(`${base}/#/jibaya`); await page.waitForTimeout(2500);
    await page.screenshot({ path: '/out/j-0.png' });
    const search = page.locator('input:visible').first();
    await search.click(); await page.keyboard.type('إيلي'); await page.waitForTimeout(1200);
    await page.screenshot({ path: '/out/j-1-search.png' });
    await page.keyboard.press('Enter'); await page.waitForTimeout(1500);
    await page.screenshot({ path: '/out/j-2-picked.png' });
    const inputs = await page.locator('input:visible').evaluateAll((els) => els.map((e) => (e.getAttribute('aria-label') || e.placeholder || e.name || '') + '=' + e.value).slice(0, 12));
    log('JIBAYA inputs', inputs.join(' ; '));
    await page.keyboard.press('Tab'); await page.keyboard.type('7'); await page.waitForTimeout(400);
    await page.keyboard.press('F9'); await page.waitForTimeout(2500);
    await page.screenshot({ path: '/out/j-3-after-f9.png' });
    log('JIBAYA after F9 msg:', await msgs(page), '|| http', [...new Set(page.http)].join(' ; '), '|| console', page.errors.join(' ; '));
    await page.goto(`${base}/#/payments`); await page.waitForTimeout(2200);
    await page.screenshot({ path: '/out/j-4-payments.png' });
  }
  if (parts.includes('roles')) {
    for (const [role, u] of Object.entries(st.users)) {
      if (!u?.pass) continue;
      const { ctx, page } = await open(browser, u.email, u.pass, 'ar', 1440, 900, false);
      await page.screenshot({ path: `/out/role-${role}-landing.png` });
      const tabs = await page.locator('.mx-rtab, .mx-ribbon button').allTextContents().catch(() => []);
      log(`ROLE ${role} url=${page.url().replace(base, '')} tabs=[${tabs.map((t) => t.trim()).filter(Boolean).join(',').slice(0, 300)}] http=${[...new Set(page.http)].join(' ; ').slice(0, 300)} console=${page.errors.join(' ; ').slice(0, 200)}`);
      for (const s of ['jibaya', 'collect', 'pos', 'self', 'trial-balance', 'users', 'apps']) {
        page.http.length = 0; page.errors.length = 0;
        await page.goto(`${base}/#/${s}`); await page.waitForTimeout(2000);
        const txt = await page.evaluate(() => document.body.innerText.replace(/\s+/g, ' ').slice(0, 160));
        await page.screenshot({ path: `/out/role-${role}-${s}.png` });
        log(`  ${role} ${s}: http=${[...new Set(page.http)].join(' ; ').slice(0, 200)} console=${page.errors.join(' ; ').slice(0, 120)} text=${txt}`);
      }
      await ctx.close();
    }
    // collector at phone width
    const u = st.users.collector;
    const { page } = await open(browser, u.email, u.pass, 'ar', 400, 860, false);
    await page.screenshot({ path: '/out/role-collector-phone.png' });
    await page.goto(`${base}/#/collect`); await page.waitForTimeout(2500);
    await page.screenshot({ path: '/out/role-collector-phone-collect.png' });
    log('collector phone', [...new Set(page.http)].join(' ; '), page.errors.join(' ; '));
  }
  await browser.close();
  log('done');
})();
