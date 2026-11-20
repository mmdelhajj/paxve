// Live check of the phase 7 UX fixes (audit C) on paxve.com:
//  - sign-up through the Arabic UI as a pharmacy with phone and LBP rate → only the pharmacy's ribbon tabs, rate in the status bar
//  - a second company from Settings → Companies (restaurant): chart loaded, pack applied, Arabic default names
//  - Apps page: choosing another business type shows the plan (apps to add / switch off) before applying
//  - empty screens with a hint and an action; required-field message on "add employee"; instalment button reason
//  - dashboard month picker in Arabic, no-rate warning, back-to-system button on full-screen modes, phone ribbon arrows
// Screenshots, console errors, sideways scrolling, raw keys, in Arabic desktop / phone and English desktop.
//   docker run --rm --ipc=host -v /srv/paxve/web:/web -v /tmp/p7f-out:/out -w /web/e2e mcr.microsoft.com/playwright:v1.65.1-noble node p7fix2-ui.cjs
const { chromium, request } = require('playwright');
const base = 'https://paxve.com';
const H = { 'X-Requested-With': 'paxve', 'Content-Type': 'application/json' };
let fails = 0;
const ok = (name, cond, extra = '') => { console.log(`${cond ? 'PASS' : 'FAIL'} ${name} ${extra}`); if (!cond) fails++; };

(async () => {
  const sfx = Date.now().toString(36).slice(-5);
  const email = `qa-p7f-${sfx}@example.com`, pass = 'qa-p7f-pass-12';
  const browser = await chromium.launch();

  // ---- 1. sign-up through the Arabic UI ----
  {
    const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'ar' });
    const page = await ctx.newPage();
    const errors = [];
    page.on('pageerror', (e) => errors.push(String(e)));
    await page.goto(`${base}/#/signup`);
    await page.waitForSelector('input[type=password]');
    await page.evaluate(() => { try { localStorage.setItem('paxve.lang', 'ar'); } catch { /* */ } });
    await page.reload(); await page.waitForSelector('input[type=password]');
    const inputs = page.locator('form input:not([type=password]):not([type=email]):not([type=tel])');
    await inputs.nth(0).fill(`QA UX Fix ${sfx}`);
    // business type select (Mantine): open and choose pharmacy
    const biz = page.locator('form .mantine-Select-root').nth(1).locator('input');
    await biz.click(); await page.waitForTimeout(300);
    await page.locator('[role=option]').filter({ hasText: /صيدلية|Pharmacy/ }).first().click();
    const rate = page.locator('form input[inputmode=decimal]');
    ok('signup asks the LBP rate', (await rate.count()) === 1 && (await rate.inputValue()) === '89500');
    await page.fill('form input[type=tel]', '+961 70 000 111');
    const name = page.locator('form input').filter({ hasNot: page.locator('[type]') });
    void name;
    // full name = the text input right before the email
    const texts = page.locator('form input[type=text], form input:not([type])');
    const n = await texts.count();
    for (let i = 0; i < n; i++) {
      const el = texts.nth(i);
      const lab = await el.evaluate((x) => x.closest('.mantine-InputWrapper-root')?.querySelector('label')?.textContent || '');
      if (/الاسم الكامل|Full name/.test(lab)) await el.fill('مالك الفحص');
    }
    await page.fill('form input[type=email]', email);
    await page.fill('form input[type=password]', pass);
    await page.screenshot({ path: '/out/p7f-signup.png', fullPage: true });
    await page.locator('form button[type=submit]').click();
    await page.waitForTimeout(6000);
    const tabs = await page.locator('.mx-rtab').allTextContents();
    ok('signed up into the shell', tabs.length > 3, tabs.join('|'));
    ok('pharmacy has no subscriptions / network / manufacturing tabs', !tabs.some((x) => /الاشتراكات|الإشتراكات|الشبكة|التصنيع|الأسطول|المشاريع/.test(x)), tabs.join('|'));
    ok('pharmacy has stock and till tabs', tabs.some((x) => /المستودع|المخزون/.test(x)) && tabs.some((x) => /نقطة البيع|الكاشير|البيع/.test(x)), tabs.join('|'));
    const status = await page.locator('.mx-status').innerText();
    ok('status bar shows the rate', /89,500/.test(status), status.slice(0, 200));
    ok('no page errors at sign-up', errors.length === 0, errors.join(' | '));
    await page.screenshot({ path: '/out/p7f-after-signup.png' });
    await ctx.close();
  }

  const qa = await request.newContext({ baseURL: base, extraHTTPHeaders: H });
  let r = await qa.post('/api/v1/auth/login', { data: { email, password: pass } });
  ok('api login', r.status() === 200);
  const comps = await (await qa.get('/api/v1/companies')).json();
  const cid = comps[0].id;
  ok('pharmacy company apps', !comps[0].apps.includes('net') && !comps[0].apps.includes('mfg') && comps[0].apps.includes('pos'), comps[0].apps.join(','));
  ok('pharmacy demo items in Arabic', (await (await qa.get(`/api/v1/companies/${cid}/items`)).json()).some((i) => i.code === 'PAN500' && /بنادول/.test(i.name)));

  // ---- 2. second company from Settings (API, same call the screen makes) ----
  r = await qa.post('/api/v1/companies', { data: { name_en: `QA Resto ${sfx}`, name_ar: 'مطعم الفحص', base_currency: 'USD', local_currency: 'LBP', business_type: 'restaurant', template: 'lebanon', locale: 'ar' } });
  ok('company created', r.status() === 201);
  const c2 = (await r.json()).id;
  const acc2 = await (await qa.get(`/api/v1/companies/${c2}/accounts`)).json();
  ok('new company has its chart', acc2.length > 20, String(acc2.length));
  await qa.post(`/api/v1/companies/${c2}/rates`, { data: { currency: 'LBP', rate: '89500' } });
  r = await qa.post(`/api/v1/companies/${c2}/retail/packs/restaurant`, { data: { demo: true } });
  ok('restaurant pack with demo on the new company', r.status() === 200, r.status() === 200 ? '' : await r.text());
  const regs = await (await qa.get(`/api/v1/companies/${c2}/pos/registers`)).json();
  ok('till named in Arabic', regs.some((x) => x.name === 'الصندوق 1'), regs.map((x) => x.name).join(','));

  // ---- 3. screens ----
  const runs = [{ lang: 'ar', w: 1440, h: 900, tag: 'ar' }, { lang: 'ar', w: 400, h: 860, tag: 'ar-phone' }, { lang: 'en', w: 1440, h: 900, tag: 'en' }];
  for (const run of runs) {
    const ctx = await browser.newContext({ viewport: { width: run.w, height: run.h }, locale: run.lang });
    const page = await ctx.newPage();
    const errors = [];
    page.on('console', (m) => { if (m.type() === 'error' && !/401|402|403|404|409|422|Failed to load resource/.test(m.text())) errors.push(m.text()); });
    page.on('pageerror', (e) => errors.push(String(e)));
    await page.goto(`${base}/#/login`);
    await page.waitForSelector('input[type=password]');
    await page.fill('input[type=email], input[name=email]', email);
    await page.fill('input[type=password]', pass);
    await page.keyboard.press('Enter');
    await page.waitForTimeout(2500);
    await page.evaluate(async (l) => { await fetch('/api/v1/me', { method: 'PATCH', credentials: 'include', headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'paxve' }, body: JSON.stringify({ locale: l }) }); }, run.lang);
    await page.reload(); await page.waitForTimeout(1500);
    const check = async (name) => {
      const raw = await page.evaluate(() => (document.body.innerText.match(/\b(p7|errors|mx|rt|pc|vt|gp|hr2)\.[a-z][\w.]+/g) || []).slice(0, 5).join(','));
      if (raw) errors.push('untranslated: ' + raw);
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
      await page.screenshot({ path: `/out/p7f-${run.tag}-${name}.png`, fullPage: run.w < 900 });
      ok(`${run.tag} ${name}`, overflow <= 2 && errors.length === 0, `overflow=${overflow} ${errors.join(' | ').slice(0, 300)}`);
      errors.length = 0;
    };
    await page.goto(`${base}/#/dashboard`); await page.waitForTimeout(2000);
    if (run.lang === 'ar') {
      const months = await page.locator('select[aria-label] option').allTextContents();
      ok(`${run.tag} dashboard month picker in Arabic`, months.some((m) => /تشرين|كانون|نوفمبر|يناير/.test(m)), months.slice(0, 3).join(','));
    }
    await check('dashboard');
    if (run.w < 900) {
      const arrows = await page.locator('.mx-ribarrow').count();
      ok(`${run.tag} ribbon shows a scroll arrow`, arrows >= 1, String(arrows));
    }
    // empty screens: hint + action
    // screens that are empty on this company (the pharmacy demo has stock, lots and entries)
    for (const route of ['hr/people', 'budgets', 'dunning', 'cheques']) {
      await page.goto(`${base}/#/${route}`); await page.waitForTimeout(1800);
      const hint = await page.locator('.mx-emptyhint').count();
      ok(`${run.tag} ${route} empty state has a hint`, hint >= 1, String(hint));
      await check(route.replace('/', '-'));
    }
    // add employee with no name: a message, not silence
    await page.goto(`${base}/#/hr/people`); await page.waitForTimeout(1500);
    const add = page.locator('.mx-pane:not([hidden]) button').filter({ hasText: /^(إضافة|Add)$/ });
    if (await add.count()) {
      await add.first().click(); await page.waitForTimeout(800);
      const note = await page.locator('.mantine-Notification-root, .mantine-Notifications-notification, [class*="Notification"]').count();
      ok(`${run.tag} empty employee name explains`, note >= 1, String(note));
    } else ok(`${run.tag} add employee button found`, false);
    // instalments: the disabled button says why
    await page.goto(`${base}/#/gaps/instalments`); await page.waitForTimeout(1500);
    const why = await page.locator('.mx-pane:not([hidden]) small').filter({ hasText: /اختر الزبون|Choose a customer/ }).count();
    ok(`${run.tag} instalment button reason`, why >= 1, String(why));
    await check('instalments');
    // apps: choose another business type → a plan before applying
    await page.goto(`${base}/#/apps`); await page.waitForTimeout(1500);
    await page.locator('.mx-pane:not([hidden]) select').first().selectOption('supermarket');
    await page.waitForTimeout(1500);
    const plan = await page.locator('.mx-pane:not([hidden]) .mx-note').count();
    ok(`${run.tag} business type change shows the plan`, plan >= 1, String(plan));
    await check('apps-plan');
    const cancel = page.locator('.mx-pane:not([hidden]) .mx-note button').filter({ hasText: /إلغاء|Cancel/ });
    if (await cancel.count()) await cancel.first().click();
    // rename defaults: preview only
    await page.locator('.mx-pane:not([hidden]) button').filter({ hasText: /تعريب الأسماء|Rename default names/ }).first().click();
    await page.waitForTimeout(1500);
    await check('apps-translate-preview');
    // full-screen mode: a way back
    await page.goto(`${base}/#/scan`); await page.waitForTimeout(2000);
    ok(`${run.tag} scanner has back-to-system`, (await page.locator('.mx-backsys').count()) === 1);
    await page.locator('.mx-backsys').click(); await page.waitForTimeout(1500);
    ok(`${run.tag} back-to-system returns to the shell`, (await page.locator('.mx-rtab').count()) > 0);
    // companies: the new-company form has business type and chart
    if (run.tag === 'ar') {
      await page.goto(`${base}/#/companies`); await page.waitForTimeout(1500);
      await page.locator('.mx-pane:not([hidden]) button').filter({ hasText: /شركة جديدة|جديد|New/ }).first().click().catch(() => {});
      await page.waitForTimeout(800);
      const sels = await page.locator('[role=dialog] .mantine-Select-root').count();
      ok('new company form has business type and chart', sels >= 4, String(sels));
      await check('company-new');
      await page.keyboard.press('Escape');
    }
    await ctx.close();
  }
  await browser.close();
  console.log(fails === 0 ? 'ALL PASS' : `${fails} FAILED`);
  process.exit(fails ? 1 : 0);
})();
