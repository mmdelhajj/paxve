// Windows left in this browser tab by another user's session must not reopen (they asked for a foreign record → 404).
const { chromium } = require('playwright');
const base = 'https://paxve.com';
let fails = 0;
const ok = (n, c, x = '') => { console.log(`${c ? 'PASS' : 'FAIL'} ${n} ${x}`); if (!c) fails++; };
(async () => {
  const browser = await chromium.launch();
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'ar' });
  const page = await ctx.newPage();
  const bad = [], errors = [];
  page.on('response', (r) => { if (r.status() === 404 && r.url().includes('/api/')) bad.push(r.url()); });
  page.on('pageerror', (e) => errors.push(String(e)));
  const lr = await ctx.request.post(`${base}/api/v1/auth/login`, {
    headers: { 'X-Requested-With': 'paxve', 'Content-Type': 'application/json' },
    data: { email: process.env.QA_EMAIL, password: process.env.QA_PASS },
  });
  ok('login', lr.ok(), String(lr.status()));
  await page.goto(`${base}/#/dashboard`);
  await page.waitForSelector('.mx-rtab', { timeout: 30000 });
  // another user's windows, left in this tab, and the address bar still on that user's statement
  const foreign = '01a1f56a-4acd-700c-a0fe-264da8fca44d';
  await page.evaluate((id) => {
    sessionStorage.setItem('paxve.wins', JSON.stringify({ uid: 'someone-else', cid: 'other-co', wins: [{ page: 'statement', param: id }, { page: 'voucher', param: 'expense' }] }));
    history.replaceState(null, '', `#/statement/${id}`);
  }, foreign);
  await page.reload();
  await page.waitForSelector('.mx-rtab', { timeout: 30000 });
  await page.waitForTimeout(3000);
  ok('no 404 for the other session\'s statement', !bad.some((u) => u.includes('/statement')), bad.join(' '));
  ok('address bar moved to the dashboard', /#\/dashboard/.test(page.url()), page.url());
  // same user: an ordinary screen survives a refresh
  await page.evaluate(() => { location.hash = '#/voucher/expense'; });
  await page.waitForTimeout(1500);
  await page.reload();
  await page.waitForSelector('.mx-rtab', { timeout: 30000 });
  await page.waitForTimeout(2000);
  ok('own window kept after refresh', /#\/voucher\/expense/.test(page.url()), page.url());
  const saved = await page.evaluate(() => sessionStorage.getItem('paxve.wins') || '');
  ok('saved windows carry user + company', /"uid":"[^"]+","cid":"[^"]+"/.test(saved), saved.slice(0, 160));
  ok('no page errors', errors.length === 0, errors.join(' | '));
  ok('no API 404 at all', bad.length === 0, bad.join(' '));
  await browser.close();
  console.log(fails ? `FAILED ${fails}` : 'ALL PASS');
  process.exit(fails ? 1 : 0);
})();
