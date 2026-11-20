// Live check of the appearance system ("comfort for the eyes") on paxve.com: themes comfort / classic / dark / high
// contrast, text size normal / large / extra large, density comfortable / compact. Same Magic layout: ribbon, tiles,
// window tabs, grid and status bar are present in every look. Checks: the choice is saved on the account (and comes
// back on another browser), the organisation default, contrast of text on the grid, zoomed app fills the window
// exactly, Mantine pop-ups stay attached to their field when zoomed, no console errors, no sideways scrolling, no raw
// keys; Arabic desktop + phone, English desktop. Before / after screenshots of the main screens.
//   docker run --rm --ipc=host -v /srv/paxve/web:/web -v /tmp/cf-out:/out -w /web/e2e mcr.microsoft.com/playwright:v1.65.1-noble node comfort-ui.cjs
const { chromium, request } = require('playwright');
const base = 'https://paxve.com';
const H = { 'X-Requested-With': 'paxve', 'Content-Type': 'application/json' };
let fails = 0;
const ok = (name, cond, extra = '') => { console.log(`${cond ? 'PASS' : 'FAIL'} ${name} ${extra}`); if (!cond) fails++; };

// WCAG contrast of two "rgb(r, g, b)" strings
function lum(c) {
  const m = c.match(/[\d.]+/g).slice(0, 3).map(Number).map((v) => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; });
  return 0.2126 * m[0] + 0.7152 * m[1] + 0.0722 * m[2];
}
const contrast = (a, b) => { const x = lum(a), y = lum(b); return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05); };

(async () => {
  const sfx = Date.now().toString(36).slice(-5);
  const email = `qa-cf-${sfx}@example.com`, pass = 'Qa-cf-pass-123';
  const qa = await request.newContext({ baseURL: base, extraHTTPHeaders: H });
  let r = await qa.post('/api/v1/signup', { data: { organisation: `QA Comfort ${sfx}`, company_name: 'QA Comfort', base_currency: 'USD', local_currency: 'LBP',
    template: 'lebanon', full_name: 'QA Owner', email, password: pass, locale: 'ar', business_type: 'supermarket', rate: '89500' } });
  ok('signup', r.status() === 201, r.status() === 201 ? '' : await r.text());
  const cid = (await (await qa.get('/api/v1/companies')).json())[0].id;
  const P = (p) => `/api/v1/companies/${cid}${p}`;
  await qa.put(P('/apps'), { data: { apps: ['gen', 'acc', 'col', 'msg', 'stk', 'pur', 'pos', 'crm'] } });
  r = await qa.post(P('/retail/packs/supermarket'), { data: { demo: true } });
  ok('supermarket demo', r.status() === 200, r.status() === 200 ? '' : (await r.text()).slice(0, 200));

  // ---- API: prefs saved, validated, org default ----
  r = await qa.patch('/api/v1/me', { data: { ui_prefs: { theme: 'pink' } } });
  ok('bad theme refused', r.status() === 400);
  r = await qa.put('/api/v1/org/ui-default', { data: { theme: 'comfort', size: 'n', density: 'comfy' } });
  ok('org default saved', r.status() === 200);

  const browser = await chromium.launch();
  const login = async (ctx) => {
    const page = await ctx.newPage();
    await page.goto(`${base}/#/login`);
    await page.waitForSelector('input[type=password]');
    await page.fill('input[type=email], input[name=email]', email);
    await page.fill('input[type=password]', pass);
    await page.keyboard.press('Enter');
    await page.waitForSelector('.mx-ribbon', { timeout: 15000 });
    try { await page.evaluate((c) => localStorage.setItem('paxve.company', c), cid); } catch { /* */ }
    return page;
  };
  const setLook = async (page, look) => {
    await page.evaluate(async (l) => {
      localStorage.setItem('paxve.appearance', JSON.stringify(l));
      await fetch('/api/v1/me', { method: 'PATCH', credentials: 'include', headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'paxve' }, body: JSON.stringify({ ui_prefs: l }) });
    }, look);
  };

  // ---- 1. the panel itself: pick dark + large from the eye button; saved on the account ----
  {
    const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'ar' });
    const page = await login(ctx);
    const errors = [];
    page.on('pageerror', (e) => errors.push(String(e)));
    ok('default look is comfort (org default)', (await page.evaluate(() => document.documentElement.dataset.mxTheme)) === 'comfort');
    await page.click('[data-testid=look-btn]');
    await page.waitForSelector('[data-testid=look-panel]');
    await page.screenshot({ path: '/out/cf-panel.png' });
    await page.click('[data-look-theme=dark]');
    await page.click('[data-look-size=l]');
    await page.waitForTimeout(1200);
    const st = await page.evaluate(() => ({ th: document.documentElement.dataset.mxTheme, sz: document.documentElement.dataset.mxSize, zoom: getComputedStyle(document.getElementById('root')).zoom,
      scheme: document.documentElement.getAttribute('data-mantine-color-scheme') }));
    ok('dark + large applied at once', st.th === 'dark' && st.sz === 'l' && Math.abs(parseFloat(st.zoom) - 1.125) < 0.001 && st.scheme === 'dark', JSON.stringify(st));
    const me = await (await qa.get('/api/v1/me')).json();
    ok('choice saved on the account', me.ui_prefs && me.ui_prefs.theme === 'dark' && me.ui_prefs.size === 'l', JSON.stringify(me.ui_prefs));
    ok('no page errors in the panel', errors.length === 0, errors.join(' | '));
    await ctx.close();
  }
  // another browser (empty storage) gets the account's look
  {
    const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'ar' });
    const page = await login(ctx);
    await page.waitForTimeout(800);
    ok('look follows the account to another browser', (await page.evaluate(() => document.documentElement.dataset.mxTheme)) === 'dark');
    await ctx.close();
  }

  // ---- 2. every look on the main screens ----
  const looks = [
    { tag: 'before-classic', theme: 'classic', size: 'n', density: 'compact' },
    { tag: 'comfort', theme: 'comfort', size: 'n', density: 'comfy' },
    { tag: 'dark', theme: 'dark', size: 'n', density: 'comfy' },
    { tag: 'contrast', theme: 'contrast', size: 'n', density: 'comfy' },
    { tag: 'large', theme: 'comfort', size: 'l', density: 'comfy' },
    { tag: 'xlarge', theme: 'comfort', size: 'xl', density: 'comfy' },
  ];
  const screens = [['dashboard', 'dashboard'], ['apps', 'apps'], ['jibaya', 'jibaya'], ['accounts', 'accounts'], ['items', 'items']];
  const runs = [{ lang: 'ar', w: 1440, h: 900, tag: 'ar' }, { lang: 'ar', w: 400, h: 860, tag: 'ar-phone' }, { lang: 'en', w: 1440, h: 900, tag: 'en' }];
  for (const run of runs) {
    const ctx = await browser.newContext({ viewport: { width: run.w, height: run.h }, locale: run.lang });
    const page = await login(ctx);
    await page.evaluate(async (l) => { await fetch('/api/v1/me', { method: 'PATCH', credentials: 'include', headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'paxve' }, body: JSON.stringify({ locale: l }) }); }, run.lang);
    const errors = [];
    page.on('console', (m) => { if (m.type() === 'error' && !/401|402|403|404|409|422|Failed to load resource/.test(m.text())) errors.push(m.text()); });
    page.on('pageerror', (e) => errors.push(String(e)));
    for (const look of looks) {
      if (run.tag !== 'ar' && !['comfort', 'dark', 'xlarge'].includes(look.tag)) continue;
      await setLook(page, { theme: look.theme, size: look.size, density: look.density });
      for (const [route, name] of screens) {
        if (run.tag !== 'ar' && !['dashboard', 'accounts'].includes(name)) continue;
        await page.goto(`${base}/#/${route}`); await page.reload();
        await page.waitForSelector('.mx-status', { timeout: 15000 }); await page.waitForTimeout(1600);
        const m = await page.evaluate(() => {
          const q = (s) => document.querySelector(s);
          const cs = (el) => (el ? getComputedStyle(el) : null);
          const cell = q('.mx-grid tbody td') || q('.mx-box') || q('.mx-pane');
          const cellBg = (() => { let el = cell; while (el) { const b = getComputedStyle(el).backgroundColor; if (b && !/rgba\(0, 0, 0, 0\)|transparent/.test(b)) return b; el = el.parentElement; } return 'rgb(255, 255, 255)'; })();
          const app = q('.mx-app');
          return {
            theme: document.documentElement.dataset.mxTheme,
            parts: ['.mx-title', '.mx-ribbon', '.mx-wins', '.mx-work', '.mx-status'].every((s) => !!q(s)),
            ink: cs(cell)?.color, cellBg,
            appBottom: app ? app.getBoundingClientRect().bottom : 0, vh: window.innerHeight,
            scrollH: document.scrollingElement.scrollHeight,
            overflow: document.documentElement.scrollWidth - window.innerWidth,
            raw: (document.body.innerText.match(/\b(look|mx|p7|errors|sec)\.[a-z][\w.]+/g) || []).slice(0, 4).join(','),
            rowH: q('.mx-grid tbody td') ? q('.mx-grid tbody td').getBoundingClientRect().height : 0,
          };
        });
        const cr = m.ink ? contrast(m.ink, m.cellBg) : 0;
        const need = look.theme === 'contrast' ? 7 : 4.5;
        let good = m.theme === look.theme && m.parts && m.overflow <= 2 && !m.raw && cr >= need && errors.length === 0;
        // desktop: the zoomed app fills the window exactly (no page scroll, no gap)
        if (run.w > 900) good = good && Math.abs(m.appBottom - m.vh) <= 2 && m.scrollH <= m.vh + 2;
        ok(`${run.tag} ${look.tag} ${name}`, good, `theme=${m.theme} layout=${m.parts} contrast=${cr.toFixed(1)} bottom=${m.appBottom}/${m.vh} scroll=${m.scrollH} overflow=${m.overflow} rowH=${m.rowH.toFixed(1)} raw=${m.raw} ${errors.join(' | ').slice(0, 300)}`);
        errors.length = 0;
        await page.screenshot({ path: `/out/cf-${run.tag}-${look.tag}-${name}.png`, fullPage: run.w < 900 });
      }
      if (run.tag === 'ar' && look.tag === 'dark') {
        const bg = await page.evaluate(() => getComputedStyle(document.querySelector('.mx-work')).backgroundColor);
        ok('dark ground is low-glare, not black', lum(bg) < 0.03 && lum(bg) > 0.004, bg);
      }
      if (run.tag === 'ar' && look.tag === 'comfort') {
        const bg = await page.evaluate(() => getComputedStyle(document.querySelector('.mx-work')).backgroundColor);
        ok('comfort ground is a soft off-white, not pure white', bg !== 'rgb(255, 255, 255)' && lum(bg) > 0.85, bg);
      }
      if (run.tag === 'ar' && (look.tag === 'xlarge' || look.tag === 'large')) {
        // a Mantine Select opened inside the zoomed app stays attached to its field (below, or above near the bottom)
        await page.goto(`${base}/#/security`); await page.reload(); await page.waitForSelector('[data-testid=look-org]', { timeout: 15000 });
        const inp = page.locator('[data-testid=look-org] .mantine-Select-root input').first();
        await inp.click(); await page.waitForTimeout(500);
        const a = await inp.boundingBox();
        const lb = page.locator('[role=listbox]:visible').first();
        const b = (await lb.count()) ? await lb.boundingBox() : null;
        ok(`${look.tag}: dropdown attached to its field`, !!b && (Math.abs(b.y - (a.y + a.height)) < 24 || Math.abs(b.y + b.height - a.y) < 24) && b.x < a.x + a.width && b.x + b.width > a.x, `field=${JSON.stringify(a)} list=${JSON.stringify(b)}`);
        await page.screenshot({ path: `/out/cf-${look.tag}-security.png` });
        await page.keyboard.press('Escape');
      }
    }
    // POS and the collector app in comfort and dark
    if (run.tag !== 'en') {
      for (const look of [looks[1], looks[2], looks[4]]) {
        await setLook(page, { theme: look.theme, size: look.size, density: look.density });
        for (const route of ['pos', 'collect']) {
          await page.goto(`${base}/#/${route}`); await page.reload(); await page.waitForTimeout(3000);
          const m = await page.evaluate(() => ({ theme: document.documentElement.dataset.mxTheme, overflow: document.documentElement.scrollWidth - window.innerWidth,
            bg: getComputedStyle(document.body).backgroundColor, btn: !!document.querySelector('.mx-look-ai') }));
          ok(`${run.tag} ${look.tag} ${route}`, m.theme === look.theme && m.overflow <= 2 && m.btn && errors.length === 0, `${JSON.stringify(m)} ${errors.join(' | ').slice(0, 200)}`);
          errors.length = 0;
          await page.screenshot({ path: `/out/cf-${run.tag}-${look.tag}-${route}.png` });
        }
      }
      await page.goto(`${base}/#/dashboard`);
    }
    await ctx.close();
  }

  // ---- 3. reset this user to the defaults; the org default applies again ----
  r = await qa.patch('/api/v1/me', { data: { ui_prefs: null } });
  const me = await (await qa.get('/api/v1/me')).json();
  ok('cleared look falls back to the organisation default', r.status() === 200 && me.ui_prefs && me.ui_prefs.theme === 'comfort', JSON.stringify(me.ui_prefs));

  // the marketing site keeps its own light design whatever the device's choice
  {
    const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 }, locale: 'ar', colorScheme: 'dark' });
    const page = await ctx.newPage();
    await page.goto(`${base}/`); await page.waitForTimeout(1500);
    const th = await page.evaluate(() => ({ th: document.documentElement.dataset.mxTheme, scheme: document.documentElement.getAttribute('data-mantine-color-scheme'), sz: document.documentElement.dataset.mxSize }));
    ok('public site stays light and unzoomed', th.th === 'comfort' && th.scheme === 'light' && th.sz === 'n', JSON.stringify(th));
    // an app user with no choice on a dark phone gets dark ("automatic")
    await ctx.close();
  }

  // switch the QA org off
  await qa.post('/api/v1/auth/logout');
  console.log(`\n${fails === 0 ? 'ALL PASS' : fails + ' FAIL'}`);
  await browser.close();
  process.exit(fails ? 1 : 0);
})();
