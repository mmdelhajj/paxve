// Audit B seed: fresh QA org (Lebanon, USD/LBP), demo data, supermarket pack, staff users. Writes /out/state.json.
const { request } = require('playwright');
const fs = require('fs');
const base = 'https://paxve.com';
const H = { 'X-Requested-With': 'paxve', 'Content-Type': 'application/json' };
const log = (...a) => console.log(...a);
(async () => {
  const sfx = Date.now().toString(36).slice(-5);
  const org = `QA AuditB ${sfx}`, email = `qa-auditb-${sfx}@example.com`, pass = 'qa-auditb-pass-1';
  const qa = await request.newContext({ baseURL: base, extraHTTPHeaders: H });
  let r = await qa.post('/api/v1/signup', { data: { organisation: org, company_name: 'مولدات التدقيق', base_currency: 'USD', local_currency: 'LBP', template: 'lebanon', full_name: 'QA Owner', email, password: pass, locale: 'ar' } });
  log('signup', r.status());
  const comps = await (await qa.get('/api/v1/companies')).json();
  const cid = comps[0].id;
  const P = `/api/v1/companies/${cid}`;
  r = await qa.post(`${P}/rates`, { data: { currency: 'LBP', date: '2026-01-01', rate: '89500' } }); log('rate', r.status());
  r = await qa.post('/api/v1/demo-data', { data: { lang: 'ar' } }); const demo = await r.json(); log('demo', r.status(), JSON.stringify(demo));
  r = await qa.post(`/api/v1/companies/${demo.company_id}/rates`, { data: { currency: 'LBP', date: '2026-01-01', rate: '89500' } }); log('demo rate', r.status());
  r = await qa.post(`${P}/retail/packs/supermarket`, { data: { demo: true } }); log('pack', r.status(), (await r.text()).slice(0, 200));
  const users = {};
  for (const role of ['accountant', 'collector', 'cashier', 'data_entry', 'read_only']) {
    const ue = `qa-auditb-${sfx}-${role}@example.com`;
    r = await qa.post('/api/v1/users', { data: { email: ue, full_name: `QA ${role}`, role, password: 'qa-auditb-pass-1', locale: 'ar', company_ids: [] } });
    log('user', role, r.status(), (await r.text()).slice(0, 160));
    users[role] = ue;
  }
  // employee with self-service login
  r = await qa.post(`/api/v1/companies/${demo.company_id}/employees`, { data: { name: 'موظف التدقيق', base_salary: '600', salary_currency: 'USD', start_date: '2026-01-01' } });
  const emp = await r.json().catch(() => ({})); log('employee', r.status(), JSON.stringify(emp).slice(0, 200));
  if (emp.id) { r = await qa.post(`/api/v1/companies/${demo.company_id}/employees/${emp.id}/login`, { data: { email: `qa-auditb-${sfx}-emp@example.com` } }); const t = await r.text(); log('emp login', r.status(), t.slice(0, 200)); try { users.employee = { email: `qa-auditb-${sfx}-emp@example.com`, ...JSON.parse(t) }; } catch {} }
  fs.writeFileSync('/out/state.json', JSON.stringify({ sfx, org, email, pass, cid, demoCid: demo.company_id, users, empId: emp.id }, null, 1));
  // shapes
  for (const p of ['reports/trial-balance', 'reports/balance-sheet', 'reports/profit-loss', 'reports/aging']) {
    r = await qa.get(`/api/v1/companies/${demo.company_id}/${p}`); log('SHAPE', p, r.status(), (await r.text()).slice(0, 700));
  }
})();
