// Live check of the phase 3 screens on paxve.com with a fresh QA organisation: projects (board, task panel, milestones,
// billing, costs, settings, my tasks, timesheets with timer, profitability), field service (jobs, dispatch board, SLA,
// technician phone view), tickets with SLA, the public rating page, automation (rules, test, templates, log) and fleet
// (vehicles, fuel, maintenance, alerts, costs, settings). Screenshots, console errors, sideways scrolling, raw keys.
// Run inside the Playwright image:
//   docker run --rm --ipc=host -v /srv/paxve/web:/web -v /tmp/p3-out:/out -w /web/e2e mcr.microsoft.com/playwright:v1.65.1-noble node p3-ui.cjs
const { chromium, request } = require('playwright');
const base = 'https://paxve.com';
const H = { 'X-Requested-With': 'paxve', 'Content-Type': 'application/json' };
let fails = 0;
const ok = (name, cond, extra = '') => { console.log(`${cond ? 'PASS' : 'FAIL'} ${name} ${extra}`); if (!cond) fails++; };

(async () => {
  const sfx = Date.now().toString(36).slice(-5);
  const orgName = `QA Projects ${sfx}`;
  const email = `qa-p3-${sfx}@example.com`, pass = 'qa-p3-pass-12';
  const qa = await request.newContext({ baseURL: base, extraHTTPHeaders: H });
  let r = await qa.post('/api/v1/signup', { data: { organisation: orgName, company_name: orgName, base_currency: 'USD', local_currency: 'LBP', template: 'lebanon',
    full_name: 'QA Owner', email, password: pass, locale: 'en' } });
  ok('signup', r.status() === 201);
  const me = await (await qa.get('/api/v1/me')).json();
  const cid = (await (await qa.get('/api/v1/companies')).json())[0].id;
  // a sign-up starts with its business type's apps only (phase 7); this check uses every app
  await qa.put(`/api/v1/companies/${cid}/apps`, { data: { apps: ['gen', 'acc', 'col', 'msg', 'stk', 'pur', 'net', 'sat', 'pos', 'hr', 'crm', 'svc', 'mfg', 'prj', 'flt', 'web', 'mkt', 'ai'] } });
  const P = `/api/v1/companies/${cid}`;
  await qa.post(`${P}/rates`, { data: { currency: 'LBP', date: '2026-01-01', rate: '89500' } });
  const accts = Object.fromEntries((await (await qa.get(`${P}/accounts`)).json()).map((a) => [a.code, a.id]));
  const methods = Object.fromEntries((await (await qa.get(`${P}/payment-methods`)).json()).map((m) => [m.name, m.id]));
  await qa.post(`${P}/employees`, { data: { name: 'QA Owner', currency: 'USD', salary: '1500', user_id: me.id, hourly_cost: '12' } });
  const cust = await (await qa.post(`${P}/customers`, { data: { name: 'Hotel Phoenicia', phone: '70123456', email: 'hotel@example.com' } })).json();
  // project with tasks, time, milestone, a cost and an invoice
  const pr = await (await qa.post(`${P}/prj/projects`, { data: { name: 'Hotel network upgrade', code: 'HNU', customer_id: cust.id, billing: 'tm', hourly_rate: '45', budget: '2000', budget_hours: '60', end_date: '2026-12-31' } })).json();
  ok('project', !!pr.project);
  const pid = pr.project.id;
  const t1 = await (await qa.post(`${P}/prj/project-tasks`, { data: { project_id: pid, title: 'Survey the floors', assignee_id: me.id, priority: 'high', deadline: '2026-11-15',
    checklist: [{ text: 'Ground floor', done: true }, { text: 'Roof', done: false }] } })).json();
  await qa.post(`${P}/prj/project-tasks`, { data: { project_id: pid, title: 'Install access points', assignee_id: me.id, stage_id: pr.stages[1].id } });
  await qa.post(`${P}/prj/project-tasks`, { data: { project_id: pid, parent_id: t1.task.id, title: 'Measure cable runs' } });
  await qa.post(`${P}/prj/timesheets`, { data: { task_id: t1.task.id, date: '2026-11-18', hours: '6', description: 'site survey' } });
  await qa.post(`${P}/prj/milestones`, { data: { project_id: pid, name: 'Design approved', due_date: '2026-11-30', amount: '500' } });
  await qa.post(`${P}/expenses`, { data: { date: '2026-11-18', account_id: accts['611'], method_id: methods['Cash USD'], amount: '80', description: 'Cable', project_id: pid } });
  r = await qa.post(`${P}/prj/projects/${pid}/invoice`, { data: { date: '2026-11-19' } });
  ok('time invoiced', r.status() === 201, String(r.status()));
  await qa.post(`${P}/prj/timesheets`, { data: { task_id: t1.task.id, date: '2026-11-19', hours: '2' } });
  // SLA, tickets, a job planned for the owner (who is also the technician here)
  await qa.post(`${P}/sla/sla-policies`, { data: { name: 'Urgent 4h', priority: 'urgent', response_hours: '1', resolution_hours: '4', escalate_to: me.id } });
  await qa.post(`${P}/sla/sla-policies`, { data: { name: 'Standard', response_hours: '8', resolution_hours: '48' } });
  const tk = await (await qa.post(`${P}/tickets`, { data: { customer_id: cust.id, subject: 'Wi-Fi down on floor 3', priority: 'urgent', category: 'no_internet' } })).json();
  const done = await (await qa.post(`${P}/tickets`, { data: { customer_id: cust.id, subject: 'Slow internet in lobby', priority: 'normal' } })).json();
  const start = new Date(Date.now() + 3 * 3600e3).toISOString();
  const job = await (await qa.post(`${P}/fs/service-jobs`, { data: { ticket_id: tk.id, technician_id: me.id, scheduled_start: start, labour_rate: '25' } })).json();
  ok('job', !!job.job);
  await qa.post(`${P}/fs/service-jobs`, { data: { customer_id: cust.id, title: 'Check the UPS', priority: 'high' } });
  const closed = await (await qa.patch(`${P}/tickets/${done.id}`, { data: { status: 'done' } })).json();
  const rateTok = closed.rating_token;
  ok('rating token', !!rateTok);
  // automation
  await qa.post(`${P}/automation/templates/overdue_invoice`);
  await qa.post(`${P}/automation/templates/ticket_breach`);
  // fleet
  await qa.put(`${P}/fleet/settings`, { data: { fuel_account_id: accts['601'], service_account_id: accts['611'] } });
  const soon = new Date(Date.now() + 12 * 86400e3).toISOString().slice(0, 10);
  const veh = await (await qa.post(`${P}/fleet/vehicles`, { data: { name: 'Service van', plate: 'B 12345', kind: 'van', make: 'Renault', model: 'Kangoo', insurance_expiry: soon, driver_id: me.id } })).json();
  for (const [d, o, l, a] of [['2026-11-01', '1000', '40', ''], ['2026-11-08', '1500', '40', '48'], ['2026-11-15', '2000', '75', '90']]) {
    await qa.post(`${P}/fleet/fuel`, { data: { vehicle_id: veh.id, date: d, odometer: o, liters: l, ...(a ? { amount: a, method_id: methods['Cash USD'] } : {}) } });
  }
  await qa.post(`${P}/fleet/service-plans`, { data: { vehicle_id: veh.id, task: 'Oil change', every_km: '5000', every_days: 180 } });
  await qa.post(`${P}/fleet/service-log`, { data: { vehicle_id: veh.id, task: 'Brake pads', date: '2026-11-16', amount: '60', method_id: methods['Cash USD'], vendor: 'Garage Sami' } });

  const runs = [
    { lang: 'en', w: 1440, h: 900, tag: 'en' },
    { lang: 'ar', w: 1440, h: 900, tag: 'ar' },
    { lang: 'fr', w: 1440, h: 900, tag: 'fr' },
    { lang: 'ar', w: 400, h: 860, tag: 'ar-phone' },
    { lang: 'en', w: 800, h: 1200, tag: 'en-tablet' },
  ];
  const browser = await chromium.launch();
  for (const run of runs) {
    const ctx = await browser.newContext({ viewport: { width: run.w, height: run.h }, locale: run.lang });
    const page = await ctx.newPage();
    page.on('dialog', (d) => d.accept().catch(() => {}));
    const errors = [];
    page.on('console', (m) => { if (m.type() === 'error' && !/401|402|403|404|409|Failed to load resource/.test(m.text())) errors.push(m.text()); });
    page.on('pageerror', (e) => errors.push(String(e)));
    await page.goto(`${base}/#/login`);
    await page.waitForSelector('input[type=password]');
    await page.fill('input[type=email], input[name=email]', email);
    await page.fill('input[type=password]', pass);
    await page.keyboard.press('Enter');
    await page.waitForTimeout(2500);
    await page.evaluate(async (l) => { await fetch('/api/v1/me', { method: 'PATCH', credentials: 'include', headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'paxve' }, body: JSON.stringify({ locale: l }) }); }, run.lang);
    await page.reload(); await page.waitForTimeout(1500);
    const check = async (name, phoneOk = false) => {
      const raw = await page.evaluate(() => (document.body.innerText.match(/\b(pj|fsv|au|fl|svc|errors|mx)\.[a-z][\w.]+/g) || []).slice(0, 5).join(','));
      if (raw) errors.push('untranslated: ' + raw);
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
      await page.screenshot({ path: `/out/p3-${run.tag}-${name}.png`, fullPage: run.w < 900 });
      ok(`${run.tag} ${name}`, (overflow <= 2 || phoneOk) && errors.length === 0, `overflow=${overflow} ${errors.join(' | ').slice(0, 300)}`);
      errors.length = 0;
    };
    const tabs = async (prefix) => {
      const tl = page.locator('section.mx-box [role=tablist] button');
      const n = await tl.count();
      for (let i = 1; i < n; i++) { await tl.nth(i).click(); await page.waitForTimeout(1100); await check(`${prefix}-${i}`); }
    };
    // ---- projects ----
    await page.goto(`${base}/#/projects/projects`); await page.waitForTimeout(1800);
    await check('projects');
    await page.locator('.mx-table tbody tr', { hasText: 'Hotel network upgrade' }).first().click(); await page.waitForTimeout(1600);
    await check('project-board');
    if (run.tag === 'en') {
      const txt = await page.locator('body').innerText();
      ok('board shows tasks and KPIs', txt.includes('Survey the floors') && txt.includes('Install access points') && /Revenue/.test(txt) && /Margin/.test(txt));
      const inp = page.locator('input[placeholder="New task…"]').first();
      await inp.fill('Label the cables'); await inp.press('Enter'); await page.waitForTimeout(1500);
      ok('quick task added to the board', (await page.locator('body').innerText()).includes('Label the cables'));
      await page.locator('a', { hasText: 'Survey the floors' }).first().click(); await page.waitForTimeout(1500);
      const tp = await page.locator('[role=dialog]').innerText();
      ok('task panel: checklist, subtask, time', tp.includes('Ground floor') && tp.includes('Measure cable runs') && tp.includes('site survey'));
      await check('task-panel');
      await page.locator('[role=dialog] button', { hasText: 'Close' }).first().click(); await page.waitForTimeout(600);
    }
    await tabs('project');
    for (const m of ['tasks', 'timesheets', 'reports']) { await page.goto(`${base}/#/projects/${m}`); await page.waitForTimeout(1600); await check(`projects-${m}`); }
    if (run.tag === 'en') {
      await page.goto(`${base}/#/projects/timesheets`); await page.waitForTimeout(1500);
      const sel = page.locator('section.mx-box select').first();
      await sel.selectOption({ label: 'Hotel network upgrade' });
      await page.locator('button', { hasText: 'Start timer' }).first().click(); await page.waitForTimeout(1800);
      ok('timer runs', /\d\d:\d\d:\d\d/.test(await page.locator('body').innerText()));
      await check('timer');
      await page.locator('button', { hasText: 'Stop' }).first().click(); await page.waitForTimeout(1500);
      ok('timer stopped', !/\d\d:\d\d:\d\d/.test(await page.locator('.mx-col').first().innerText()));
    }
    // ---- field service ----
    await page.goto(`${base}/#/field/jobs`); await page.waitForTimeout(1800);
    await page.locator('.mx-table tbody tr').first().click(); await page.waitForTimeout(1500);
    await check('jobs');
    if (run.tag === 'en') ok('job panel from ticket', (await page.locator('body').innerText()).includes('Hotel Phoenicia'));
    await page.goto(`${base}/#/field/board`); await page.waitForTimeout(1800);
    await check('board');
    if (run.tag === 'en') ok('board shows planned and unplanned jobs', (await page.locator('body').innerText()).includes('Check the UPS'));
    await page.goto(`${base}/#/field/sla`); await page.waitForTimeout(1800);
    await check('sla');
    if (run.tag === 'en') {
      const txt = await page.locator('body').innerText();
      ok('sla policies and inbound', txt.includes('Urgent 4h') && txt.includes('Requests by e-mail and WhatsApp'));
      await page.locator('button', { hasText: 'Switch on' }).first().click().catch(() => {}); await page.waitForTimeout(1200);
      ok('inbound addresses shown', await page.locator('input[readonly]').evaluateAll((els) => els.some((e) => e.value.includes('/inbound/'))));
    }
    await page.goto(`${base}/#/service`); await page.waitForTimeout(1800);
    await page.locator('.mx-grid tr[data-key]', { hasText: 'Wi-Fi down' }).first().click().catch(() => {}); await page.waitForTimeout(1300);
    await check('tickets');
    if (run.tag === 'en') ok('ticket shows SLA and job', /Urgent 4h/.test(await page.locator('body').innerText()));
    // technician phone view
    await page.goto(`${base}/#/tech`); await page.waitForTimeout(1800);
    await check('tech-list');
    await page.locator('main button', { hasText: 'Wi-Fi down' }).first().click(); await page.waitForTimeout(1500);
    await check('tech-job');
    if (run.tag === 'en') {
      await page.locator('main button', { hasText: 'Start' }).first().click(); await page.waitForTimeout(1500);
      ok('tech: job started', (await page.locator('main').innerText()).includes('In progress'));
      await page.locator('main button', { hasText: 'Finish' }).first().click(); await page.waitForTimeout(900);
      await page.locator('main textarea').first().fill('Rebooted the switch, replaced a patch cable');
      await check('tech-finish');
      await page.locator('main button', { hasText: 'Job done' }).click(); await page.waitForTimeout(1800);
      ok('tech: job done', (await page.locator('main').innerText()).includes('Done'));
    }
    // ---- automation ----
    await page.goto(`${base}/#/automation/rules`); await page.waitForTimeout(1800);
    await check('auto-rules');
    if (run.tag === 'en') {
      await page.locator('button', { hasText: 'Test' }).first().click(); await page.waitForTimeout(1600);
      ok('rule test result', (await page.locator('body').innerText()).includes('nothing was changed'));
      await page.locator('button', { hasText: 'New rule' }).first().click(); await page.waitForTimeout(800);
      await check('auto-editor');
    }
    for (const m of ['templates', 'log']) { await page.goto(`${base}/#/automation/${m}`); await page.waitForTimeout(1500); await check(`auto-${m}`); }
    // ---- fleet ----
    await page.goto(`${base}/#/fleet/vehicles`); await page.waitForTimeout(1800);
    await page.locator('.mx-table tbody tr', { hasText: 'Service van' }).first().click(); await page.waitForTimeout(1500);
    await check('fleet-vehicle');
    if (run.tag === 'en') {
      const txt = await page.locator('body').innerText();
      ok('vehicle: consumption alert and costs', txt.includes('High consumption') && txt.includes('This year'));
    }
    for (const m of ['fuel', 'services', 'alerts', 'costs', 'settings']) { await page.goto(`${base}/#/fleet/${m}`); await page.waitForTimeout(1500); await check(`fleet-${m}`); }
    // public rating page
    await page.goto(`${base}/#/rate/${rateTok}`); await page.waitForTimeout(1800);
    await check('rate');
    if (run.tag === 'en') {
      await page.locator('button[role=radio]').nth(4).click();
      await page.locator('button', { hasText: 'Send' }).click(); await page.waitForTimeout(1200);
      ok('rating sent', (await page.locator('body').innerText()).includes('Thank you'));
    }
    await ctx.close();
  }
  await browser.close();
  console.log(`DONE fails=${fails} org=${orgName}`);
})().catch((e) => { console.error('ERROR', e); process.exit(1); });
