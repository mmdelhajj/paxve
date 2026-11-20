// Audit B: create staff users + employee login in the seeded org (updates /out/state.json)
const { request } = require('playwright');
const fs = require('fs');
const base = 'https://paxve.com';
const H = { 'X-Requested-With': 'paxve', 'Content-Type': 'application/json' };
(async () => {
  const st = JSON.parse(fs.readFileSync('/out/state.json', 'utf8'));
  const qa = await request.newContext({ baseURL: base, extraHTTPHeaders: H });
  let r = await qa.post('/api/v1/auth/login', { data: { email: st.email, password: st.pass } }); console.log('login', r.status());
  const tries = [
    { email: `x1-${st.sfx}@example.com`, full_name: 'X1', role: 'accountant' },
    { email: `x2-${st.sfx}@example.com`, full_name: 'X2', role: 'accountant', password: 'qa-auditb-pass-1' },
    { email: `x3-${st.sfx}@example.com`, full_name: 'X3', role: 'accountant', password: 'qa-auditb-pass-1', locale: 'ar', company_ids: [st.cid] },
  ];
  for (const t of tries) { r = await qa.post('/api/v1/users', { data: t }); console.log('try', JSON.stringify(t).slice(0, 80), r.status(), (await r.text()).slice(0, 200)); }
  const users = {};
  for (const role of ['accountant', 'collector', 'cashier', 'data_entry', 'read_only']) {
    const ue = `qa-auditb-${st.sfx}-${role}@example.com`;
    r = await qa.post('/api/v1/users', { data: { email: ue, full_name: `QA ${role}`, role, password: 'qa-auditb-pass-1', locale: 'ar' } });
    const t = await r.text(); console.log('user', role, r.status(), t.slice(0, 200));
    users[role] = { email: ue, pass: 'qa-auditb-pass-1' };
    try { const j = JSON.parse(t); if (j.password) users[role].pass = j.password; if (j.temp_password) users[role].pass = j.temp_password; } catch {}
  }
  r = await qa.post(`/api/v1/companies/${st.demoCid}/employees`, { data: { name: 'موظف التدقيق', salary: '600', currency: 'USD', hire_date: '2026-01-01' } });
  const emp = await r.json().catch(() => ({})); console.log('employee', r.status(), JSON.stringify(emp).slice(0, 300));
  if (emp.id) {
    r = await qa.post(`/api/v1/companies/${st.demoCid}/employees/${emp.id}/login`, { data: {} });
    const t = await r.text(); console.log('emp login', r.status(), t.slice(0, 300));
    try { const j = JSON.parse(t); users.employee = { email: j.email ?? j.user?.email, pass: j.password ?? j.temp_password }; } catch {}
  }
  st.users = users; st.empId = emp.id;
  fs.writeFileSync('/out/state.json', JSON.stringify(st, null, 1));
})();
