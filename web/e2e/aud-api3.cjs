// Audit B, API part 3: payroll lifecycle, PO receive/bill, revaluation, ticket, dunning, final tie-outs on both companies.
const { request } = require('playwright');
const fs = require('fs');
const base = 'https://paxve.com';
const H = { 'X-Requested-With': 'paxve', 'Content-Type': 'application/json' };
const st = JSON.parse(fs.readFileSync('/out/state.json', 'utf8'));
const find = (sev, area, msg) => console.log(`FIND [${sev}] ${area}: ${msg}`);
const info = (...a) => console.log('INFO', ...a);
const j = async (r) => { const t = await r.text(); try { return JSON.parse(t); } catch { return t; } };
const short = (x) => (typeof x === 'string' ? x : JSON.stringify(x)).slice(0, 300);
const n = (x) => Number(x ?? 0);
(async () => {
  const qa = await request.newContext({ baseURL: base, extraHTTPHeaders: H });
  await qa.post('/api/v1/auth/login', { data: { email: st.email, password: st.pass } });
  const D = `/api/v1/companies/${st.demoCid}`, A = `/api/v1/companies/${st.cid}`;
  // payroll
  let r = await qa.post(`${D}/payroll`, { data: { period: '2026-11' } }); const run = await j(r); info('payroll create', r.status(), short(run));
  const rid = run.id ?? run.run?.id;
  if (rid) {
    for (const op of ['approve', 'post', 'pay']) {
      r = await qa.post(`${D}/payroll/${rid}/${op}`, { data: {} }); const b = await j(r); info('payroll', op, r.status(), short(b));
      if (r.status() >= 400) find(r.status() >= 500 ? 'high' : 'medium', 'payroll', `${op} -> ${r.status()} ${short(b)}`);
    }
    r = await qa.post(`${D}/payroll`, { data: { period: '2026-11' } }); info('payroll dup period', r.status(), short(await j(r)));
    if (r.status() < 400) find('high', 'payroll', 'second payroll run for the same period accepted');
  }
  // PO receive/bill
  const pos = await j(await qa.get(`${A}/purchase-orders`)); const pl = Array.isArray(pos) ? pos : pos.rows ?? [];
  info('POs', pl.length, short(pl[0] ?? {}).slice(0, 150));
  if (pl[0]) {
    const po = await j(await qa.get(`${A}/purchase-orders/${pl[0].id}`)); const line = po.lines?.[0];
    for (const op of ['confirm', 'send']) { r = await qa.post(`${A}/purchase-orders/${pl[0].id}/${op}`, { data: {} }); info('po', op, r.status(), short(await j(r)).slice(0, 120)); }
    r = await qa.post(`${A}/purchase-orders/${pl[0].id}/receive`, { data: { lines: [{ po_line_id: line.id, quantity: '6' }] } }); info('receive 6', r.status(), short(await j(r)).slice(0, 160));
    r = await qa.post(`${A}/purchase-orders/${pl[0].id}/bill`, { data: { supplier_ref: `S-${st.sfx}`, lines: [{ po_line_id: line.id, quantity: '12', unit_price: '2' }] } }); const ob = await j(r);
    info('bill 12 of 10', r.status(), short(ob).slice(0, 200)); if (r.status() < 400) find('high', 'purchasing', 'bill for more than ordered accepted');
    r = await qa.post(`${A}/purchase-orders/${pl[0].id}/bill`, { data: { supplier_ref: `S-${st.sfx}`, lines: [{ po_line_id: line.id, quantity: '8', unit_price: '2' }] } }); const b8 = await j(r);
    info('bill 8 (received 6)', r.status(), short(b8).slice(0, 200));
    r = await qa.post(`${A}/purchase-orders/${pl[0].id}/bill`, { data: { supplier_ref: `S-${st.sfx}`, lines: [{ po_line_id: line.id, quantity: '1', unit_price: '2' }] } });
    info('dup supplier ref', r.status(), short(await j(r)).slice(0, 200));
  }
  // revaluation
  r = await qa.post(`${D}/fx-revaluation`, { data: { period: '2026-11' } }); info('fx reval', r.status(), short(await j(r)));
  r = await qa.post(`${D}/fx-revaluation`, { data: { period: '2026-11' } }); info('fx reval again', r.status());
  // ticket with Arabic and very long text
  r = await qa.post(`${D}/tickets`, { data: { subject: 'انقطاع الكهرباء', category: 'power_cut', priority: 'high', description: 'ت'.repeat(5000) } }); info('ticket', r.status(), short(await j(r)).slice(0, 160));
  // dunning run
  r = await qa.post(`${D}/dunning/run`, { data: {} }); info('dunning run', r.status(), short(await j(r)).slice(0, 200));
  // final tie-outs
  for (const [tag, P] of [['demo', D], ['supermarket', A]]) {
    const tb = await j(await qa.get(`${P}/reports/trial-balance`));
    const sd = tb.rows.reduce((s, x) => s + n(x.debit), 0), sc = tb.rows.reduce((s, x) => s + n(x.credit), 0);
    const bs = await j(await qa.get(`${P}/reports/balance-sheet`)); const pl2 = await j(await qa.get(`${P}/reports/profit-loss`));
    info(tag, 'TB', sd.toFixed(4), sc.toFixed(4), 'BS TA', bs.total_assets, 'TL+E', bs.total_liabilities_equity, 'CR', bs.current_result, 'PL', pl2.net);
    if (Math.abs(sd - sc) > 0.01) find('critical', `${tag} TB`, `debits ${sd} credits ${sc}`);
    if (Math.abs(n(bs.total_assets) - n(bs.total_liabilities_equity)) > 0.01) find('critical', `${tag} balance sheet`, `assets ${bs.total_assets} != liabilities+equity ${bs.total_liabilities_equity}`);
    if (Math.abs(n(bs.current_result) - n(pl2.net)) > 0.01) find('high', `${tag} BS/PL`, `${bs.current_result} vs ${pl2.net}`);
    const sv = await j(await qa.get(`${P}/reports/stock-valuation`));
    const inv = tb.rows.filter((x) => /^3|^14|^1300|^37/.test(x.code) && x.type === 'asset').reduce((s, x) => s + n(x.balance), 0);
    info(tag, 'stock valuation', short(sv).slice(0, 200), 'inventory accts', inv.toFixed(2));
  }
  console.log('done');
})();
