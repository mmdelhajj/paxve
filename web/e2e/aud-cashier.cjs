const { chromium } = require('playwright');
const fs = require('fs');
const base = 'https://paxve.com';
const st = JSON.parse(fs.readFileSync('/out/state.json', 'utf8'));
(async () => {
  const browser = await chromium.launch();
  for (const role of ['cashier', 'collector']) {
    const u = st.users[role];
    const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'ar', timezoneId: 'Asia/Beirut' });
    const page = await ctx.newPage();
    await page.goto(`${base}/#/login`); await page.waitForSelector('input[type=password]');
    await page.fill('input[type=email], input[name=email]', u.email); await page.fill('input[type=password]', u.pass);
    await page.keyboard.press('Enter'); await page.waitForTimeout(3500);
    const comps = await page.evaluate(async () => (await (await fetch('/api/v1/companies', { credentials: 'include' })).json()).map((c) => c.name_ar || c.name_en));
    const ls = await page.evaluate(() => JSON.stringify(Object.fromEntries(Object.entries(localStorage))).slice(0, 300));
    console.log(role, 'companies', JSON.stringify(comps), 'ls', ls);
    await page.locator('button').first().click().catch(() => {}); await page.waitForTimeout(1200);
    await page.screenshot({ path: `/out/role-${role}-menu.png` });
    console.log(role, 'menu text', (await page.evaluate(() => document.body.innerText.replace(/\s+/g, ' '))).slice(0, 500));
    await ctx.close();
  }
  await browser.close();
})();
