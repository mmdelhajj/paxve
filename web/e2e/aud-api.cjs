// Audit B, API layer: flows, edge cases, report tie-outs, role matrix. Prints FIND lines.
const { request } = require('playwright');
const fs = require('fs');
const base = 'https://paxve.com';
const H = { 'X-Requested-With': 'paxve', 'Content-Type': 'application/json' };
const st = JSON.parse(fs.readFileSync('/out/state.json', 'utf8'));
const find = (sev, area, msg) => console.log(`FIND [${sev}] ${area}: ${msg}`);
const info = (...a) => console.log('INFO', ...a);
const n = (x) => Number(x ?? 0);
const near = (a, b, eps = 0.011) => Math.abs(n(a) - n(b)) <= eps;
async function ctxFor(email, pass) {
  const c = await request.newContext({ baseURL: base, extraHTTPHeaders: H });
  const r = await c.post('/api/v1/auth/login', { data: { email, password: pass } });
  return { c, status: r.status(), body: await r.text() };
}
async function j(r) { const t = await r.text(); try { return JSON.parse(t); } catch { return t; } }

(async () => {
  const { c: qa } = await ctxFor(st.email, st.pass);
  const D = `/api/v1/companies/${st.demoCid}`;
  const A = `/api/v1/companies/${st.cid}`;

  // ---------- integrity of reports ----------
  async function tieOut(P, tag) {
    const tb = await j(await qa.get(`${P}/reports/trial-balance`));
    const sd = tb.rows.reduce((s, x) => s + n(x.debit), 0), sc = tb.rows.reduce((s, x) => s + n(x.credit), 0);
    if (!near(sd, sc)) find('critical', `${tag} trial balance`, `debits ${sd.toFixed(4)} != credits ${sc.toFixed(4)}`);
    const bs = await j(await qa.get(`${P}/reports/balance-sheet`));
    const pl = await j(await qa.get(`${P}/reports/profit-loss`));
    if (!near(bs.current_result, pl.net)) find('high', `${tag} BS vs P&L`, `BS current_result ${bs.current_result} != P&L net ${pl.net}`);
    const ta = n(bs.total_assets), tle = n(bs.total_liabilities) + n(bs.total_equity) + (bs.total_equity_includes_result ? 0 : 0);
    info(tag, 'BS keys', Object.keys(bs).join(','), 'TA', bs.total_assets, 'TL', bs.total_liabilities, 'TE', bs.total_equity, 'CR', bs.current_result);
    // asset-side from TB vs BS
    const tbAssets = tb.rows.filter((x) => x.type === 'asset').reduce((s, x) => s + n(x.balance), 0);
    if (!near(tbAssets, ta)) find('high', `${tag} BS assets`, `TB asset balances ${tbAssets.toFixed(2)} != BS total_assets ${ta}`);
    const ag = await j(await qa.get(`${P}/reports/aging`));
    const agUSD = (ag.rows ?? []).filter((x) => x.currency === 'USD').reduce((s, x) => s + n(x.total), 0);
    const ar = tb.rows.filter((x) => x.code === '411' || x.code === '1200').reduce((s, x) => s + n(x.balance), 0);
    info(tag, 'aging USD', agUSD.toFixed(2), 'AR acct', ar.toFixed(2), 'aging rows', (ag.rows ?? []).length, 'has_more', ag.has_more);
    if (!near(agUSD, ar, 0.05)) find('medium', `${tag} aging vs AR`, `aging USD total ${agUSD.toFixed(2)} != AR account balance ${ar.toFixed(2)} (customer credits/LBP may explain)`);
    return { tb, bs, pl };
  }
  await tieOut(D, 'demo');

  // ---------- payments edge cases ----------
  const methods = await j(await qa.get(`${D}/payment-methods`));
  const usd = methods.find((m) => m.kind === 'cash' && m.currency === 'USD'), lbp = methods.find((m) => m.kind === 'cash' && m.currency === 'LBP');
  const custs = await j(await qa.get(`${D}/customers?limit=50`));
  const clist = Array.isArray(custs) ? custs : custs.rows ?? custs.customers ?? [];
  info('customers', clist.length, 'methods', methods.length);
  const c0 = clist[0], c1 = clist[1], c2 = clist[2];
  const pay = (body) => qa.post(`${D}/payments`, { data: body });
  const cases = [
    ['zero', { customer_id: c0.id, currency: 'USD', tenders: [{ method_id: usd.id, amount: '0' }] }, (s) => s >= 400],
    ['negative', { customer_id: c0.id, currency: 'USD', tenders: [{ method_id: usd.id, amount: '-5' }] }, (s) => s >= 400],
    ['text', { customer_id: c0.id, currency: 'USD', tenders: [{ method_id: usd.id, amount: 'abc' }] }, (s) => s >= 400],
    ['huge', { customer_id: c0.id, currency: 'USD', tenders: [{ method_id: usd.id, amount: '999999999999999999' }] }, (s) => s >= 400 && s < 500],
    ['5 decimals', { customer_id: c0.id, currency: 'USD', tenders: [{ method_id: usd.id, amount: '1.12345' }] }, (s) => s >= 400],
    ['no customer', { customer_id: '', currency: 'USD', tenders: [{ method_id: usd.id, amount: '5' }] }, (s) => s >= 400],
    ['bad uuid', { customer_id: 'x', currency: 'USD', tenders: [{ method_id: usd.id, amount: '5' }] }, (s) => s >= 400 && s < 500],
  ];
  for (const [name, body, okf] of cases) {
    const r = await pay(body); const t = (await r.text()).slice(0, 160);
    if (!okf(r.status())) find(r.status() >= 500 ? 'high' : 'medium', 'payments API', `edge "${name}" -> ${r.status()} ${t}`);
    else info('pay edge', name, r.status(), t);
  }
  // mixed tender with change, LBP rounding
  let r = await pay({ customer_id: c1.id, currency: 'USD', amount: '10', notes: 'دفعة تجريبية بالعربية', tenders: [{ method_id: usd.id, amount: '5' }, { method_id: lbp.id, amount: '500000' }], change_method_id: lbp.id });
  let pj = await j(r); info('mixed pay', r.status(), JSON.stringify(pj).slice(0, 400));
  if (r.status() >= 400) find('high', 'payments', `mixed USD+LBP with change failed ${r.status()} ${JSON.stringify(pj).slice(0, 200)}`);
  else {
    const chg = JSON.stringify(pj).match(/"change[^"]*":"?([\d.]+)/);
    info('change', chg && chg[1]);
  }
  // 1 LBP payment (tiny)
  r = await pay({ customer_id: c2.id, currency: 'USD', tenders: [{ method_id: lbp.id, amount: '1' }] });
  info('1 LBP pay', r.status(), (await r.text()).slice(0, 200));

  // ---------- customers edge cases ----------
  const cc = async (name, body, expectFail) => {
    const r = await qa.post(`${D}/customers`, { data: body }); const t = (await r.text()).slice(0, 160);
    const failed = r.status() >= 400;
    if (r.status() >= 500) find('high', 'customers API', `${name} -> 500 ${t}`);
    else if (failed !== expectFail) find('medium', 'customers API', `${name} -> ${r.status()} (expected ${expectFail ? 'refusal' : 'ok'}) ${t}`);
    else info('cust', name, r.status());
    return t;
  };
  await cc('empty name', { name: '' }, true);
  await cc('spaces name', { name: '   ' }, true);
  await cc('arabic', { name: 'زبون اختبار عربي ١٢٣', phone: '٠٣١٢٣٤٥٦' }, false);
  await cc('500 chars', { name: 'ب'.repeat(500) }, true);
  await cc('dup code', { name: 'Dup', code: clist[0].code }, true);
  await cc('bad email', { name: 'BadMail', email: 'not-an-email' }, true);
  await cc('html', { name: '<img src=x onerror=alert(1)>' }, false);

  // ---------- journal ----------
  const accts = await j(await qa.get(`${D}/accounts`));
  const al = Array.isArray(accts) ? accts : accts.rows ?? [];
  const cash = al.find((a) => a.code === '531'), rev = al.find((a) => a.code === '701') ?? al.find((a) => a.type === 'income');
  const exp = al.find((a) => a.type === 'expense');
  info('accounts', al.length, !!cash, !!rev, !!exp);
  const je = (lines, date = '2026-11-15') => qa.post(`${D}/journal`, { data: { date, memo: 'تدقيق', lines, post: true } });
  r = await je([{ account_id: exp.id, debit: '10', currency: 'USD' }, { account_id: cash.id, credit: '9', currency: 'USD' }]);
  if (r.status() < 400) find('critical', 'journal', 'unbalanced entry accepted');
  else info('unbalanced', r.status(), (await r.text()).slice(0, 120));
  r = await je([{ account_id: exp.id, debit: '0', currency: 'USD' }, { account_id: cash.id, credit: '0', currency: 'USD' }]);
  info('zero entry', r.status(), (await r.text()).slice(0, 120)); if (r.status() < 400) find('low', 'journal', 'all-zero journal entry accepted');
  r = await je([{ account_id: exp.id, debit: '123456789012.12', currency: 'USD' }, { account_id: cash.id, credit: '123456789012.12', currency: 'USD' }]);
  info('huge entry', r.status(), (await r.text()).slice(0, 160));
  if (r.status() >= 500) find('high', 'journal', `huge amount -> 500`);
  r = await je([{ account_id: exp.id, debit: '1', currency: 'USD' }, { account_id: cash.id, credit: '1', currency: 'USD' }], '2026-02-30');
  if (r.status() < 400) find('medium', 'journal', 'invalid date 2026-02-30 accepted'); else info('bad date', r.status());
  r = await je([{ account_id: exp.id, debit: '89500', currency: 'LBP' }, { account_id: cash.id, credit: '89500', currency: 'LBP' }]);
  info('LBP entry', r.status(), (await r.text()).slice(0, 200));
  await tieOut(D, 'demo after edge postings');

  // ---------- exchange voucher ----------
  r = await qa.post(`${D}/exchanges`, { data: { from_method_id: usd.id, from_amount: '100', to_method_id: lbp.id, rate: '89000', counterparty: 'صراف' } });
  info('exchange', r.status(), (await r.text()).slice(0, 300));
  if (r.status() >= 400) find('medium', 'exchange', `USD->LBP exchange failed ${r.status()}`);

  // ---------- billing run twice ----------
  const period = '2026-12';
  r = await qa.post(`${D}/billing/run`, { data: { period } }); const b1 = await j(r); info('billing 1', r.status(), JSON.stringify(b1).slice(0, 300));
  r = await qa.post(`${D}/billing/run`, { data: { period } }); const b2 = await j(r); info('billing 2', r.status(), JSON.stringify(b2).slice(0, 300));
  if (b2 && n(b2.created) > 0) find('critical', 'billing run', `second run for same period created ${b2.created} duplicate bills`);
  r = await qa.post(`${D}/billing/run`, { data: { period: '2026-13' } }); if (r.status() < 400) find('medium', 'billing run', 'period 2026-13 accepted');
  await tieOut(D, 'demo after billing');

  // ---------- year close ----------
  r = await qa.post(`${D}/fiscal-years/close`, { data: { year: 2025 } }); info('close 2025', r.status(), (await r.text()).slice(0, 300));
  r = await je([{ account_id: exp.id, debit: '1', currency: 'USD' }, { account_id: cash.id, credit: '1', currency: 'USD' }], '2025-06-01');
  if (r.status() < 400) find('critical', 'year close', 'posting into closed 2025 accepted'); else info('post into closed', r.status(), (await r.text()).slice(0, 120));
  r = await qa.post(`${D}/fiscal-years/close`, { data: { year: 2030 } }); if (r.status() < 400) find('medium', 'year close', 'future year 2030 close accepted');

  // ---------- employee login ----------
  if (st.empId) {
    r = await qa.post(`${D}/employees/${st.empId}/login`, { data: { email: `qa-auditb-${st.sfx}-emp@example.com` } });
    const t = await j(r); info('emp login', r.status(), JSON.stringify(t).slice(0, 300));
    const pw = t.password ?? t.temp_password ?? t.one_time_password;
    st.users.employee = { email: `qa-auditb-${st.sfx}-emp@example.com`, pass: pw };
    fs.writeFileSync('/out/state.json', JSON.stringify(st, null, 1));
  }

  // ---------- role matrix ----------
  const probes = [
    ['GET', `${D}/reports/profit-loss`, 'P&L'],
    ['GET', `${D}/reports/trial-balance`, 'TB'],
    ['GET', `${D}/customers`, 'customers'],
    ['GET', `/api/v1/users`, 'users list'],
    ['GET', `/api/v1/audit`, 'audit log'],
    ['GET', `${D}/employees`, 'employees'],
    ['GET', `${D}/payroll/runs`, 'payroll runs'],
    ['POST', `${D}/journal`, 'post journal', { date: '2026-11-15', memo: 'role', post: true, lines: [{ account_id: exp.id, debit: '1', currency: 'USD' }, { account_id: cash.id, credit: '1', currency: 'USD' }] }],
    ['POST', `${D}/customers`, 'create customer', { name: 'role test' }],
    ['POST', `${D}/payments`, 'collect payment', { customer_id: c0.id, currency: 'USD', tenders: [{ method_id: usd.id, amount: '1' }] }],
    ['POST', `/api/v1/users`, 'create user', { email: `esc-${Date.now()}@example.com`, full_name: 'esc', role: 'admin', password: 'qa-auditb-pass-1' }],
    ['PUT', `${D}/apps`, 'change apps', { apps: ['gen', 'acc', 'col'] }],
    ['GET', `/api/v1/platform/tenants`, 'platform'],
  ];
  const expect = {
    accountant: { 'create user': 'deny', 'change apps': 'deny', platform: 'deny', 'users list': 'deny' },
    collector: { 'P&L': 'deny', TB: 'deny', 'users list': 'deny', 'audit log': 'deny', employees: 'deny', 'payroll runs': 'deny', 'post journal': 'deny', 'create user': 'deny', 'change apps': 'deny', platform: 'deny', 'collect payment': 'allow' },
    cashier: { 'P&L': 'deny', TB: 'deny', 'users list': 'deny', 'audit log': 'deny', employees: 'deny', 'payroll runs': 'deny', 'post journal': 'deny', 'create user': 'deny', 'change apps': 'deny', platform: 'deny' },
    data_entry: { 'create user': 'deny', 'change apps': 'deny', platform: 'deny', 'users list': 'deny' },
    read_only: { 'post journal': 'deny', 'create customer': 'deny', 'collect payment': 'deny', 'create user': 'deny', 'change apps': 'deny', platform: 'deny' },
    employee: { 'P&L': 'deny', TB: 'deny', customers: 'deny', 'users list': 'deny', 'audit log': 'deny', employees: 'deny', 'payroll runs': 'deny', 'post journal': 'deny', 'create customer': 'deny', 'collect payment': 'deny', 'create user': 'deny', 'change apps': 'deny', platform: 'deny' },
  };
  for (const [role, u] of Object.entries(st.users)) {
    if (!u || !u.pass) { find('medium', 'roles', `no password for ${role}`); continue; }
    const { c, status } = await ctxFor(u.email, u.pass);
    if (status !== 200) { find('high', 'roles', `${role} cannot log in (${status})`); continue; }
    const row = [];
    for (const [m, url, name, body] of probes) {
      const rr = m === 'GET' ? await c.get(url) : m === 'PUT' ? await c.put(url, { data: body }) : await c.post(url, { data: body });
      const s = rr.status();
      row.push(`${name}=${s}`);
      const exp = expect[role]?.[name];
      if (exp === 'deny' && s < 400) find(name.match(/user|apps|platform|journal/) ? 'critical' : 'high', 'roles', `${role} can ${name} (${s})`);
      if (exp === 'allow' && s >= 400) find('high', 'roles', `${role} refused ${name} (${s})`);
      if (s >= 500) find('high', 'roles', `${role} ${name} -> ${s}`);
    }
    info('ROLE', role, row.join(' '));
  }
  console.log('done');
})();
