// Audit B, API part 2: collector/cashier real flows, POS, retail, purchasing, HR, misc edge checks.
const { request } = require('playwright');
const fs = require('fs');
const base = 'https://paxve.com';
const H = { 'X-Requested-With': 'paxve', 'Content-Type': 'application/json' };
const st = JSON.parse(fs.readFileSync('/out/state.json', 'utf8'));
const find = (sev, area, msg) => console.log(`FIND [${sev}] ${area}: ${msg}`);
const info = (...a) => console.log('INFO', ...a);
async function login(email, pass) { const c = await request.newContext({ baseURL: base, extraHTTPHeaders: H }); const r = await c.post('/api/v1/auth/login', { data: { email, password: pass } }); return { c, s: r.status() }; }
const j = async (r) => { const t = await r.text(); try { return JSON.parse(t); } catch { return t; } };
const short = (x) => (typeof x === 'string' ? x : JSON.stringify(x)).slice(0, 260);
(async () => {
  const { c: qa } = await login(st.email, st.pass);
  const D = `/api/v1/companies/${st.demoCid}`, A = `/api/v1/companies/${st.cid}`;
  const methods = await j(await qa.get(`${D}/payment-methods`));
  info('methods', short(methods.map((m) => [m.name, m.kind, m.currency, m.user_id ?? ''])));
  const custs = await j(await qa.get(`${D}/customers?limit=10`)); const cl = Array.isArray(custs) ? custs : custs.rows ?? [];
  // collector flow
  const col = st.users.collector;
  const { c: cc } = await login(col.email, col.pass);
  let r = await cc.get('/api/v1/me'); const me = await j(r); info('collector me', short(me));
  r = await cc.get(`${D}/payment-methods`); const cm = await j(r); info('collector methods', r.status(), short(cm));
  const mine = Array.isArray(cm) ? cm.find((m) => m.currency === 'USD') : null;
  r = await cc.post(`${D}/payments`, { data: { customer_id: cl[3].id, currency: 'USD', tenders: [{ method_id: (mine ?? methods[0]).id, amount: '2' }] } });
  info('collector pay', r.status(), short(await j(r)));
  if (r.status() >= 400) find('high', 'collector', `new collector user cannot record a payment with company boxes: ${r.status()} ${short(await j(r))} (needs a personal cash box first? UI must say so)`);
  // cashier: POS session on supermarket company A
  const cas = st.users.cashier; const { c: ca } = await login(cas.email, cas.pass);
  const regs = await j(await qa.get(`${A}/pos/registers`)); info('registers', short(regs));
  r = await ca.post(`${A}/pos/sessions`, { data: { register_id: regs[0]?.id } }); const ses = await j(r); info('cashier open session', r.status(), short(ses));
  if (r.status() >= 400) find('high', 'POS cashier', `cashier cannot open a till session: ${r.status()} ${short(ses)}`);
  // owner POS: promos, loyalty, gift, big qty, zero qty, negative price
  r = await qa.post(`${A}/pos/sessions`, { data: { register_id: regs[0]?.id } }); let os = await j(r); info('owner session', r.status(), short(os));
  const sid = os.session?.id ?? os.id ?? (await j(await qa.get(`${A}/pos/sessions?open=1`)))?.[0]?.id;
  const items = await j(await qa.get(`${A}/items`)); const il = Array.isArray(items) ? items : items.rows ?? [];
  const am = await j(await qa.get(`${A}/payment-methods`)); const ausd = am.find((m) => m.kind === 'cash' && m.currency === 'USD'); const albp = am.find((m) => m.kind === 'cash' && m.currency === 'LBP');
  const it = il.find((x) => x.kind === 'stock') ?? il[0];
  const order = (lines, tenders, extra = {}) => qa.post(`${A}/pos/orders`, { data: { session_id: sid, apply_promos: true, lines, tenders, change_method_id: ausd.id, ...extra } });
  for (const [name, lines, tenders, bad] of [
    ['zero qty', [{ item_id: it.id, quantity: '0' }], [{ method_id: ausd.id, amount: '5' }], true],
    ['negative qty', [{ item_id: it.id, quantity: '-2' }], [{ method_id: ausd.id, amount: '5' }], true],
    ['underpaid', [{ item_id: it.id, quantity: '50' }], [{ method_id: ausd.id, amount: '1' }], true],
    ['huge qty', [{ item_id: it.id, quantity: '99999999999' }], [{ method_id: ausd.id, amount: '1' }], true],
    ['LBP only', [{ item_id: it.id, quantity: '1' }], [{ method_id: albp.id, amount: '500000' }], false],
    ['weighed 0.375', [{ item_id: it.id, quantity: '0.375' }], [{ method_id: ausd.id, amount: '20' }], false],
  ]) {
    r = await order(lines, tenders); const b = await j(r);
    if (r.status() >= 500) find('high', 'POS order', `${name} -> 500 ${short(b)}`);
    else if ((r.status() >= 400) !== bad) find('medium', 'POS order', `${name} -> ${r.status()} ${short(b)}`);
    else info('pos', name, r.status(), short(b).slice(0, 160));
  }
  // retail: gift card overdraw
  r = await order([{ item_id: it.id, quantity: '1' }], [{ method_id: ausd.id, amount: '50' }], { gift_sold: [{ code: `AUD${st.sfx}`.toUpperCase(), amount: '5' }] });
  info('gift sold', r.status(), short(await j(r)).slice(0, 200));
  const gm = am.find((m) => m.kind === 'gift' || /gift/i.test(m.name));
  info('gift method', short(gm ?? 'none'));
  // purchasing: PO -> receive -> bill > ordered
  const sups = await j(await qa.get(`${A}/suppliers`)); const sl = Array.isArray(sups) ? sups : sups.rows ?? [];
  info('suppliers', sl.length);
  let sup = sl[0];
  if (!sup) { r = await qa.post(`${A}/suppliers`, { data: { name: 'مورد التدقيق', currency: 'USD' } }); sup = await j(r); info('new supplier', r.status(), short(sup)); }
  r = await qa.post(`${A}/purchase-orders`, { data: { supplier_id: sup.id, currency: 'USD', lines: [{ item_id: it.id, quantity: '10', unit_price: '2' }] } });
  const po = await j(r); info('PO', r.status(), short(po));
  // HR: payroll run for demo company
  r = await qa.get(`${D}/payroll`); info('payroll list', r.status(), short(await j(r)).slice(0, 200));
  // statement for a customer with Arabic name, print endpoints
  r = await qa.get(`${D}/customers/${cl[0].id}/statement`); info('statement', r.status(), short(await j(r)).slice(0, 160));
  // portal link
  r = await qa.post(`${D}/customers/${cl[0].id}/portal-link`, { data: {} }); info('portal link', r.status(), short(await j(r)).slice(0, 200));
  // custom field with arabic name
  r = await qa.post(`${D}/custom-fields`, { data: { entity: 'customer', label: 'رقم العقد', kind: 'text' } }); info('custom field', r.status(), short(await j(r)).slice(0, 200));
  // api key + webhook
  r = await qa.post('/api/v1/api-keys', { data: { name: 'audit', role: 'read_only' } }); const ak = await j(r); info('api key', r.status(), short(ak).slice(0, 120));
  // 2FA setup start
  r = await qa.post('/api/v1/me/mfa/setup', { data: {} }); info('mfa setup', r.status(), short(await j(r)).slice(0, 100));
  // platform as org owner must be 403
  r = await qa.get('/api/v1/platform/tenants'); if (r.status() !== 403) find('critical', 'platform', `org owner reached platform tenants (${r.status()})`);
  // my subscription
  r = await qa.get('/api/v1/my-subscription'); info('my-sub', r.status(), short(await j(r)).slice(0, 200));
  // password change weak
  r = await qa.post('/api/v1/me/password', { data: { current: st.pass, password: '123' } }); info('weak pw change', r.status(), short(await j(r)).slice(0, 100));
  // login brute force lockout
  const anon = await request.newContext({ baseURL: base, extraHTTPHeaders: H });
  let last = 0; for (let i = 0; i < 12; i++) { const x = await anon.post('/api/v1/auth/login', { data: { email: st.users.read_only.email, password: 'wrong-pass-xx' } }); last = x.status(); }
  info('12 bad logins ->', last); if (last !== 429) find('medium', 'auth', `12 wrong passwords in a row still return ${last} (no lockout/rate limit)`);
  console.log('done');
})();
