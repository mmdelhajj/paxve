// Audit B, browser crawl: every ribbon screen in AR/EN/FR desktop + AR phone, as the owner, in the demo company.
// Records console errors, page errors, HTTP >=400 (except 401), raw i18n keys, generic error texts, horizontal overflow.
const { chromium } = require('playwright');
const fs = require('fs');
const base = 'https://paxve.com';
const st = JSON.parse(fs.readFileSync('/out/state.json', 'utf8'));
const which = process.argv[2] || 'all';
const runs = [
  { lang: 'ar', w: 1440, h: 900, tag: 'ar', shots: true },
  { lang: 'en', w: 1440, h: 900, tag: 'en' },
  { lang: 'fr', w: 1440, h: 900, tag: 'fr' },
  { lang: 'ar', w: 400, h: 860, tag: 'ar-phone', shots: true },
].filter((r) => which === 'all' || r.tag === which);
const screens = ('jibaya invoices print-bills jibaya/read subscribers customers voucher/receipt voucher/expense voucher/payment payments cash cheques exchange dashboard ' +
  'subs-change network billing-setup tariffs readings statements messages service service/maintenance jibaya/late jibaya/col jibaya/scan dunning online-pay collect collections ' +
  'accounts journal ledger daily trial-balance profit-loss balance-sheet cash-flow compare cost-centers budgets fiscal-years opening-balances rates recurring bank-rec assets fx-reval deferrals vat-report consolidated ' +
  'aging generator-report asset-register sales-report pos-report ap-aging expenses-report stock-valuation stock-moves ' +
  'items stock stock-pro stock-pro/counts stock-pro/lots stock-pro/replenish stock-pro/prices stock-pro/landed mfg/orders mfg/boms mfg/scrap ' +
  'suppliers po supplier-prices bills supplier-payments expenses crm activities quotes orders pos pos-sessions pos-setup ' +
  'retail/promos retail/loyalty retail/gift retail/variants retail/labels retail/expiry retail/receive retail/report retail/packs ' +
  'hr/people hr/attendance hr/leave hr/payroll hr/rules claims hr-extra/roster hr-extra/accruals hr-extra/devices hr/reports self ' +
  'apps localization translations custom-fields einvoice companies users approvals audit import magic-import developers security my-sub').split(/\s+/);

(async () => {
  const browser = await chromium.launch();
  const results = [];
  for (const run of runs) {
    const ctx = await browser.newContext({ viewport: { width: run.w, height: run.h }, locale: run.lang });
    const page = await ctx.newPage();
    let errors = [], http = [];
    page.on('console', (m) => { if (m.type() === 'error' && !/Failed to load resource/.test(m.text())) errors.push(m.text().slice(0, 200)); });
    page.on('pageerror', (e) => errors.push('PAGEERROR ' + String(e).slice(0, 200)));
    page.on('response', (r) => { const s = r.status(); if (s >= 400 && s !== 401 && r.url().includes('/api/')) http.push(`${s} ${r.request().method()} ${r.url().replace(base, '').replace(/[0-9a-f-]{36}/g, ':id').slice(0, 90)}`); });
    await page.goto(`${base}/#/login`);
    await page.waitForSelector('input[type=password]');
    await page.fill('input[type=email], input[name=email]', st.email);
    await page.fill('input[type=password]', st.pass);
    await page.keyboard.press('Enter');
    await page.waitForTimeout(2500);
    await page.evaluate(async ({ l, cid }) => {
      const h = { 'Content-Type': 'application/json', 'X-Requested-With': 'paxve' };
      await fetch('/api/v1/me', { method: 'PATCH', credentials: 'include', headers: h, body: JSON.stringify({ locale: l }) });
      try { localStorage.setItem('paxve.company', cid); } catch {}
    }, { l: run.lang, cid: st.demoCid });
    await page.reload(); await page.waitForTimeout(1500);
    const sel = page.locator('.mx-title select').first();
    const opts = await sel.locator('option').allTextContents().catch(() => []);
    const demo = opts.find((o) => /Demo|تجريبية/.test(o));
    if (demo) { await sel.selectOption({ label: demo }).catch(() => {}); await page.waitForTimeout(1500); }
    errors = []; http = [];
    for (const s of screens) {
      await page.goto(`${base}/#/${s}`); await page.waitForTimeout(2300);
      const ev = await page.evaluate(() => {
        const txt = document.body.innerText;
        const raw = (txt.match(/\b(rt|pos|mx|fin|errors|t2|common|hr|stk|acc|plat|platform|svc|oa|mfg|retail|pf|ap|sl)\.[a-z][\w.]+/g) || []).slice(0, 5);
        const generic = /Something went wrong|حدث خطأ|Une erreur|undefined|NaN|\[object Object\]/.exec(txt);
        return { raw, generic: generic ? generic[0] : '', overflow: document.documentElement.scrollWidth - window.innerWidth, len: txt.length, title: (document.querySelector('.mx-mdi .on, .mx-tab.on')?.textContent || '').trim().slice(0, 40) };
      });
      const issues = [];
      if (errors.length) issues.push('console: ' + [...new Set(errors)].join(' | '));
      if (http.length) issues.push('http: ' + [...new Set(http)].join(' | '));
      if (ev.raw.length) issues.push('raw keys: ' + ev.raw.join(','));
      if (ev.generic) issues.push('text: ' + ev.generic);
      if (ev.overflow > 2) issues.push('overflow ' + ev.overflow + 'px');
      if (ev.len < 200) issues.push('almost empty page (' + ev.len + ' chars)');
      const file = `/out/c-${run.tag}-${s.replace('/', '-')}.png`;
      if (run.shots || issues.length) await page.screenshot({ path: file }).catch(() => {});
      results.push({ run: run.tag, screen: s, issues, file });
      console.log(`${issues.length ? 'ISSUE' : 'OK'} ${run.tag} ${s} ${issues.join(' || ').slice(0, 600)}`);
      errors = []; http = [];
    }
    await ctx.close();
  }
  await browser.close();
  fs.writeFileSync(`/out/crawl-${which}.json`, JSON.stringify(results, null, 1));
  console.log('done');
})();
