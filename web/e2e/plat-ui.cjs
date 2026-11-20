// Live check of the platform console on paxve.com: API flow with a fresh QA organisation, then screenshots,
// console errors, sideways scrolling and raw keys of the console screens (run inside the Playwright image).
const { chromium, request } = require('playwright');
const base = 'https://paxve.com';
const E = process.env.PX_EMAIL, P = process.env.PX_PASS;
const H = { 'X-Requested-With': 'paxve', 'Content-Type': 'application/json' };
let fails = 0;
const ok = (name, cond, extra = '') => { console.log(`${cond ? 'PASS' : 'FAIL'} ${name} ${extra}`); if (!cond) fails++; };
const day = (n) => { const d = new Date(); d.setUTCDate(d.getUTCDate() + n); return d.toISOString().slice(0, 10); };

(async () => {
  const adm = await request.newContext({ baseURL: base, extraHTTPHeaders: H });
  let r = await adm.post('/api/v1/auth/login', { data: { email: E, password: P } });
  ok('owner login', r.ok());
  const sfx = Date.now().toString(36).slice(-5);
  const orgName = `QA Platform ${sfx}`;
  const qaEmail = `qa-plat-${sfx}@example.com`, qaPass = 'qa-platform-pass-1';
  const qa = await request.newContext({ baseURL: base, extraHTTPHeaders: H });
  r = await qa.post('/api/v1/signup', { data: { organisation: orgName, company_name: orgName, base_currency: 'USD', local_currency: 'LBP', template: 'lebanon',
    full_name: 'QA Owner', email: qaEmail, password: qaPass, locale: 'en' } });
  const me = await r.json();
  ok('signup', r.status() === 201, me.error || '');
  const tid = me.tenant_id;
  const cid = (await (await qa.get('/api/v1/companies')).json())[0].id;
  // a sign-up starts with its business type's apps only (phase 7); this check uses every app
  await qa.put(`/api/v1/companies/${cid}/apps`, { data: { apps: ['gen', 'acc', 'col', 'msg', 'stk', 'pur', 'net', 'sat', 'pos', 'hr', 'crm', 'svc', 'mfg', 'prj', 'flt', 'web', 'mkt', 'ai'] } });

  r = await adm.get('/api/v1/platform/summary'); ok('summary', r.ok(), JSON.stringify(await r.json()).slice(0, 160));
  const list = await (await adm.get('/api/v1/platform/tenants')).json();
  const row = list.find((x) => x.id === tid);
  ok('listed as trial', row && row.state === 'trial');
  r = await adm.post(`/api/v1/platform/tenants/${tid}/renew`, { data: { period: 'month', method: 'whish', reference: 'W-QA' } });
  let t = await r.json(); ok('renew month', r.ok() && t.state === 'active', `${t.plan} ${t.paid_until}`);
  r = await adm.patch(`/api/v1/platform/tenants/${tid}`, { data: { paid_until: day(-10) } });
  t = await r.json(); ok('expired', t.state === 'expired');
  r = await qa.post(`/api/v1/companies/${cid}/customers`, { data: { name: 'Refused' } });
  ok('write refused while expired', r.status() === 402, String(r.status()));
  r = await qa.get(`/api/v1/companies/${cid}/customers`); ok('read allowed while expired', r.ok());
  r = await qa.post('/api/v1/subscription/requests', { data: { kind: 'renew', plan: 'starter', cycle: 'monthly', method: 'omt', reference: 'OMT-QA' } });
  const req = await r.json(); ok('renewal request', r.status() === 201);
  const reqs = await (await adm.get('/api/v1/platform/requests')).json();
  ok('request in console', reqs.some((x) => x.id === req.id));
  r = await adm.post(`/api/v1/platform/tenants/${tid}/renew`, { data: { period: 'month', method: 'omt', reference: 'OMT-QA', request_id: req.id } });
  t = await r.json(); ok('renew from request (from today)', t.state === 'active' && t.paid_until > day(25) && t.paid_until < day(32), t.paid_until);
  ok('request closed', !(await (await adm.get('/api/v1/platform/requests')).json()).some((x) => x.id === req.id));
  r = await qa.post(`/api/v1/companies/${cid}/customers`, { data: { name: 'After renewal' } }); ok('write after renewal', r.status() === 201);
  r = await adm.post(`/api/v1/platform/tenants/${tid}/block`, { data: { reason: 'QA unpaid' } }); ok('block', r.ok());
  r = await qa.get('/api/v1/me'); ok('session ended by block', r.status() === 401);
  const qa2 = await request.newContext({ baseURL: base, extraHTTPHeaders: H });
  r = await qa2.post('/api/v1/auth/login', { data: { email: qaEmail, password: qaPass } });
  const bj = await r.json(); ok('login refused with reason', r.status() === 403 && bj.error === 'org_blocked' && bj.detail === 'QA unpaid', JSON.stringify(bj));
  r = await adm.post(`/api/v1/platform/tenants/${tid}/unblock`, { data: {} }); ok('unblock', r.ok());
  r = await qa2.post('/api/v1/auth/login', { data: { email: qaEmail, password: qaPass } }); ok('login after unblock', r.ok());
  r = await adm.post('/api/v1/platform/lifecycle/run', { data: {} }); ok('lifecycle run', r.ok(), JSON.stringify(await r.json()));
  r = await adm.get(`/api/v1/platform/tenants/${tid}`); const det = await r.json();
  ok('detail', r.ok() && det.payments.length === 2 && det.events.length > 3, `payments=${det.payments?.length} events=${det.events?.length}`);
  // grace for the screenshots of the organisation side
  await adm.patch(`/api/v1/platform/tenants/${tid}`, { data: { paid_until: day(-2), grace_days: 7 } });

  // ---------- screens ----------
  const runs = [
    { lang: 'ar', w: 1440, h: 900, tag: 'ar-desktop' },
    { lang: 'en', w: 1440, h: 900, tag: 'en-desktop' },
    { lang: 'fr', w: 1440, h: 900, tag: 'fr-desktop' },
    { lang: 'ar', w: 400, h: 860, tag: 'ar-phone' },
  ];
  const browser = await chromium.launch();
  async function signIn(ctx, email, pass, lang) {
    const page = await ctx.newPage();
    const errors = [];
    page.on('console', (m) => { if (m.type() === 'error' && !/401|402|403|Failed to load resource/.test(m.text())) errors.push(m.text()); });
    page.on('pageerror', (e) => errors.push(String(e)));
    await page.goto(`${base}/#/login`);
    await page.waitForSelector('input[type=password]');
    await page.fill('input[type=email], input[name=email]', email);
    await page.fill('input[type=password]', pass);
    await page.keyboard.press('Enter');
    await page.waitForTimeout(2500);
    const api = (method, path, body) => page.evaluate(async ([m, p, b]) => {
      const x = await fetch('/api/v1' + p, { method: m, credentials: 'include', headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'paxve' }, body: b ? JSON.stringify(b) : undefined });
      return { status: x.status, body: await x.json().catch(() => null) };
    }, [method, path, body]);
    const orig = (await api('GET', '/me')).body?.locale || 'en';
    await api('PATCH', '/me', { locale: lang });
    await page.reload(); await page.waitForTimeout(1500);
    return { page, errors, api, orig };
  }
  async function check(s, tag, name) {
    const raw = await s.page.evaluate(() => (document.body.innerText.match(/\b(pc|mx|fin|oa)\.[a-z][\w.]+/g) || []).slice(0, 5).join(','));
    if (raw) s.errors.push('untranslated: ' + raw);
    const overflow = await s.page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    await s.page.screenshot({ path: `/out/p-${tag}-${name}.png` });
    const dir = await s.page.evaluate(() => document.documentElement.dir);
    ok(`${tag} ${name}`, overflow <= 2 && s.errors.length === 0, `dir=${dir} overflow=${overflow} ${s.errors.join(' | ').slice(0, 300)}`);
    s.errors.length = 0;
  }
  for (const run of runs) {
    const ctx = await browser.newContext({ viewport: { width: run.w, height: run.h }, locale: run.lang });
    const s = await signIn(ctx, E, P, run.lang);
    for (const m of ['dash', 'orgs', 'requests', 'payments', 'plans']) {
      await s.page.goto(`${base}/#/platform/${m}`); await s.page.waitForTimeout(2200);
      if (m === 'orgs') {
        await s.page.fill('.mx-box input[placeholder]', orgName).catch(() => {});
        await s.page.waitForTimeout(500);
        await s.page.getByText(orgName, { exact: true }).first().click().catch((e) => s.errors.push('row click: ' + e.message.slice(0, 80)));
        await s.page.waitForTimeout(1500);
        await check(s, run.tag, 'orgs-detail');
        for (const [i, n] of [[2, 'renew'], [3, 'users'], [6, 'events'], [7, 'settings']]) {
          await s.page.click(`.mx-tabs2 button:nth-child(${i})`).catch(() => {});
          await s.page.waitForTimeout(700);
          await check(s, run.tag, `orgs-${n}`);
        }
        continue;
      }
      if (m === 'plans') {
        await s.page.getByText('starter', { exact: true }).first().click().catch(() => {});
        await s.page.waitForTimeout(800);
      }
      await check(s, run.tag, m);
    }
    await s.page.goto(`${base}/#/my-sub`); await s.page.waitForTimeout(2000);
    await check(s, run.tag, 'owner-my-sub');
    if (run.tag === 'en-desktop') {
      // support view: purple banner, read-only, back to the console
      const st = await s.api('POST', `/platform/tenants/${tid}/support`, {});
      ok('support start', st.status === 200);
      await s.page.goto(`${base}/#/dashboard`); await s.page.reload(); await s.page.waitForTimeout(2500);
      const bar = await s.page.$('.mx-subbar.support');
      ok('support banner', !!bar);
      const w = await s.api('POST', `/companies/${cid}/customers`, { name: 'support wrote' });
      ok('support view read-only', w.status === 403 && w.body?.error === 'support_read_only');
      await check(s, run.tag, 'support-view');
      await s.page.click('.mx-subbar.support button');
      await s.page.waitForTimeout(3000);
      const back = await s.api('GET', '/me');
      ok('support ended', back.body?.is_platform_admin === true);
    }
    await s.api('PATCH', '/me', { locale: s.orig });
    await ctx.close();
    // the organisation's own side (grace banner + My Paxve subscription)
    const ctx2 = await browser.newContext({ viewport: { width: run.w, height: run.h }, locale: run.lang });
    const q = await signIn(ctx2, qaEmail, qaPass, run.lang);
    await q.page.goto(`${base}/#/my-sub`); await q.page.waitForTimeout(2200);
    const banner = await q.page.$('.mx-subbar.warn');
    ok(`${run.tag} grace banner`, !!banner);
    await check(q, run.tag, 'org-my-sub');
    await ctx2.close();
  }
  await browser.close();
  // deactivate the QA organisation
  r = await adm.post(`/api/v1/platform/tenants/${tid}/block`, { data: { reason: 'QA test organisation' } });
  ok('QA organisation deactivated', r.ok());
  console.log(`fails=${fails}`);
})();
