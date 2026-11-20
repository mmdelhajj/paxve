import { Fragment, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, can, type Me } from '../../api';
import { useCompany } from '../../company';
import { companyName, formatAmount, formatDate, formatDateTime, today } from '../../util';
import { languages, ensureBundle } from '../../i18n';
import { useBase, useLoad, useRun } from '../wh/Wh';
import { DocFooter, DocHeader } from '../sales/common';
import { forgetLayouts, LayoutOverride, LayoutStyle, LAYOUT_BLOCKS, LAYOUT_FIELDS, type DocLayout, type DocType } from '../../components/DocLayout';
import { WidgetEditor, type Widget } from './MyDash';

// Phase 8 (Magic-style screens): branches, inter-company, consolidation with eliminations, document layouts, role
// dashboards, sign-in policy, purchase tenders and blanket agreements.

type Msg = { ok: boolean; text: string } | null;
function Flash({ m }: { m: Msg }) {
  return m ? <div className={`mx-flash ${m.ok ? 'good' : 'bad'}`}>{m.text}</div> : null;
}

const MODES = ['branches', 'interco', 'consolidation', 'layouts', 'dashboards', 'security', 'tenders', 'agreements'] as const;
type Mode = typeof MODES[number];
const amt = (v: string | number | null | undefined, d = 2) => formatAmount(String(v ?? 0), d);
const num = (s: string) => s.replace(/[^\d.]/g, '');

export default function Enterprise({ me, initial }: { me: Me; initial?: string }) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<Mode>((MODES as readonly string[]).includes(initial ?? '') ? initial as Mode : 'branches');
  return (
    <div className="mx-col">
      <div className="mx-strip"><b>{t(`ep.t.${mode}`)}</b><span className="mx-sp" /><small style={{ opacity: 0.8 }}>{t(`ep.help.${mode}`)}</small></div>
      <div className="mx-modes" role="tablist">
        {MODES.map((m) => <button key={m} role="tab" className="mx-mode" aria-selected={mode === m} onClick={() => setMode(m)}>{t(`ep.t.${m}`)}</button>)}
      </div>
      <div style={{ flex: 1, minHeight: 0, overflow: 'auto', padding: 6 }}>
        {mode === 'branches' && <Branches me={me} />}
        {mode === 'interco' && <Interco me={me} />}
        {mode === 'consolidation' && <Consolidation />}
        {mode === 'layouts' && <Layouts me={me} />}
        {mode === 'dashboards' && <RoleDashboards me={me} />}
        {mode === 'security' && <SecurityPolicy me={me} />}
        {mode === 'tenders' && <Tenders me={me} />}
        {mode === 'agreements' && <Agreements me={me} />}
      </div>
    </div>
  );
}

// ======================= branches =======================

interface Branch { id: string; code: string; name: string; address: string; phone: string; cost_center_code: string | null; is_active: boolean; users: number }
interface BUser { id: string; full_name: string; email: string; role: string; branch_ids: string[] }
interface BRow { id: string | null; code: string; name: string; income: string; expense: string; invoices: number; payments: number; bills: number; pos_orders: number }

function Branches({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [rows, reload] = useLoad<Branch[]>(base && `${base}/branches`, []);
  const [users, reloadUsers] = useLoad<BUser[]>(base && can(me, 'users.view') && `${base}/branches/users`, []);
  const [nf, setNf] = useState({ code: '', name: '', address: '', phone: '' });
  const [msg, run] = useRun(() => { reload(); reloadUsers(); });
  const [mv, setMv] = useState({ doc: 'invoice', number: '', branch_id: '' });
  const [from, setFrom] = useState(today().slice(0, 8) + '01');
  const [to, setTo] = useState(today());
  const [rep, loadRep] = useLoad<{ currency: string; rows: BRow[] } | null>(base && `${base}/branches/report?from=${from}&to=${to}`, null);
  const manage = can(me, 'settings.manage');
  const editUsers = can(me, 'users.edit');
  const active = rows.filter((b) => b.is_active);
  const toggle = (u: BUser, id: string) => {
    const ids = u.branch_ids.includes(id) ? u.branch_ids.filter((x) => x !== id) : [...u.branch_ids, id];
    void run(api(`${base}/branches/users/${u.id}`, { method: 'PUT', body: { branch_ids: ids } }));
  };
  return (
    <div className="mx-row" style={{ gap: 8, alignItems: 'flex-start', flexWrap: 'wrap' }}>
      <section className="mx-box" style={{ flex: '999 1 520px', minWidth: 0 }}>
        <h3>{t('ep.br.list')}</h3>
        {manage && (
          <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
            <label className="mx-fld">{t('ep.code')}<input dir="ltr" style={{ width: 80 }} value={nf.code} onChange={(e) => setNf({ ...nf, code: e.currentTarget.value.replace(/[^A-Za-z0-9_-]/g, '').toUpperCase() })} /></label>
            <label className="mx-fld">{t('ep.name')}<input value={nf.name} onChange={(e) => setNf({ ...nf, name: e.currentTarget.value })} /></label>
            <label className="mx-fld">{t('ep.br.address')}<input value={nf.address} onChange={(e) => setNf({ ...nf, address: e.currentTarget.value })} /></label>
            <label className="mx-fld">{t('ep.br.phone')}<input dir="ltr" style={{ width: 110 }} value={nf.phone} onChange={(e) => setNf({ ...nf, phone: e.currentTarget.value })} /></label>
            <button className="mx-btn main" disabled={!nf.code || !nf.name.trim()} title={!nf.code || !nf.name.trim() ? t('ep.needCodeName') : undefined}
              onClick={() => void run(api(`${base}/branches`, { method: 'POST', body: nf }), () => setNf({ code: '', name: '', address: '', phone: '' }))}>{t('ep.add')}</button>
          </div>)}
        <Flash m={msg} />
        <div style={{ overflowX: 'auto' }}>
          <table className="mx-table">
            <thead><tr><th>{t('ep.code')}</th><th>{t('ep.name')}</th><th>{t('ep.br.phone')}</th><th>{t('ep.br.cc')}</th><th className="num">{t('ep.br.users')}</th><th>{t('ep.status')}</th></tr></thead>
            <tbody>
              {rows.map((b) => (
                <tr key={b.id} style={{ opacity: b.is_active ? 1 : 0.55 }}>
                  <td dir="ltr"><b>{b.code}</b></td><td>{b.name}<br /><small>{b.address}</small></td><td dir="ltr">{b.phone}</td><td dir="ltr">{b.cost_center_code}</td>
                  <td className="num">{b.users}</td>
                  <td>{manage ? <button className="mx-btn sm" onClick={() => void run(api(`${base}/branches/${b.id}`, { method: 'PATCH', body: { is_active: !b.is_active } }))}>
                    {b.is_active ? t('ep.active') : t('ep.inactive')}</button> : (b.is_active ? t('ep.active') : t('ep.inactive'))}</td>
                </tr>))}
              {rows.length === 0 && <tr><td colSpan={6} className="mx-empty">{t('ep.br.none')}</td></tr>}
            </tbody>
          </table>
        </div>
        {manage && active.length > 0 && (
          <>
            <h3>{t('ep.br.move')}</h3>
            <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
              <label className="mx-fld">{t('ep.br.doc')}<select value={mv.doc} onChange={(e) => setMv({ ...mv, doc: e.currentTarget.value })}>
                {['invoice', 'bill', 'payment', 'sales_order', 'purchase_order', 'expense', 'supplier_payment'].map((d) => <option key={d} value={d}>{t(`ep.br.d.${d}`)}</option>)}</select></label>
              <label className="mx-fld">{t('ep.number')}<input dir="ltr" style={{ width: 90 }} value={mv.number} onChange={(e) => setMv({ ...mv, number: num(e.currentTarget.value) })} /></label>
              <label className="mx-fld">{t('ep.br.to')}<select value={mv.branch_id} onChange={(e) => setMv({ ...mv, branch_id: e.currentTarget.value })}>
                <option value="">{t('ep.br.companyWide')}</option>{active.map((b) => <option key={b.id} value={b.id}>{b.code} · {b.name}</option>)}</select></label>
              <button className="mx-btn" disabled={!mv.number} onClick={() => void run(api(`${base}/branches/assign`, { method: 'PUT', body: { doc: mv.doc, number: Number(mv.number), branch_id: mv.branch_id } }), () => loadRep())}>{t('ep.br.moveBtn')}</button>
            </div>
          </>)}
      </section>
      <section className="mx-box" style={{ flex: '1 1 380px', minWidth: 0 }}>
        <h3>{t('ep.br.usersTitle')}</h3>
        <small style={{ display: 'block', padding: '0 6px' }}>{t('ep.br.usersHelp')}</small>
        <div style={{ overflowX: 'auto' }}>
          <table className="mx-table">
            <thead><tr><th>{t('ep.br.user')}</th>{active.map((b) => <th key={b.id} dir="ltr">{b.code}</th>)}</tr></thead>
            <tbody>
              {users.map((u) => (
                <tr key={u.id}>
                  <td>{u.full_name}<br /><small dir="ltr">{u.email}</small>{u.branch_ids.length === 0 && <small> · {t('ep.br.all')}</small>}</td>
                  {active.map((b) => (
                    <td key={b.id} style={{ textAlign: 'center' }}>
                      <input type="checkbox" aria-label={`${u.full_name} ${b.code}`} disabled={!editUsers} checked={u.branch_ids.includes(b.id)} onChange={() => toggle(u, b.id)} />
                      {u.branch_ids[0] === b.id && <small> ★</small>}
                    </td>))}
                </tr>))}
            </tbody>
          </table>
        </div>
      </section>
      <section className="mx-box" style={{ flex: '1 1 100%', minWidth: 0 }}>
        <h3>{t('ep.br.report')}</h3>
        <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
          <label className="mx-fld">{t('ep.from')}<input type="date" dir="ltr" value={from} onChange={(e) => setFrom(e.currentTarget.value)} /></label>
          <label className="mx-fld">{t('ep.to')}<input type="date" dir="ltr" value={to} onChange={(e) => setTo(e.currentTarget.value)} /></label>
        </div>
        <div style={{ overflowX: 'auto' }}>
          <table className="mx-table">
            <thead><tr><th>{t('ep.br.branch')}</th><th className="num">{t('ep.income')}</th><th className="num">{t('ep.expense')}</th><th className="num">{t('ep.result')}</th>
              <th className="num">{t('ep.br.d.invoice')}</th><th className="num">{t('ep.br.d.payment')}</th><th className="num">{t('ep.br.d.bill')}</th><th className="num">{t('ep.br.pos')}</th></tr></thead>
            <tbody>
              {(rep?.rows ?? []).map((r) => {
                const res = Number(r.income) - Number(r.expense);
                return (
                  <tr key={r.id ?? 'none'}>
                    <td>{r.id ? <><b dir="ltr">{r.code}</b> {r.name}</> : <i>{t('ep.br.noBranch')}</i>}</td>
                    <td className="num">{amt(r.income)}</td><td className="num">{amt(r.expense)}</td><td className={`num ${res < 0 ? 'red' : ''}`}><b>{amt(res)}</b></td>
                    <td className="num">{r.invoices}</td><td className="num">{r.payments}</td><td className="num">{r.bills}</td><td className="num">{r.pos_orders}</td>
                  </tr>);
              })}
            </tbody>
          </table>
        </div>
        {rep && <small style={{ padding: 6, display: 'block' }}>{t('ep.inCurrency', { cur: rep.currency })}</small>}
      </section>
    </div>
  );
}

// ======================= inter-company =======================

interface ICLink {
  id: string; company_a: string; company_a_name: string; a_currency: string; company_b: string; company_b_name: string; b_currency: string;
  auto_bills: boolean; is_active: boolean; a_balance: string; b_balance: string; docs_done: number; docs_waiting: number; docs_void: number;
  a_account_code: string; b_account_code: string;
}
interface ICDoc { id: string; status: string; error: string; currency: string; amount: string; src_name: string; invoice_number: number; issue_date: string; dst_name: string; bill_number: number | null; bill_status: string | null }
interface ICTransfer { id: string; kind: string; transfer_date: string; currency: string; amount: string; memo: string; status: string; from_name: string; to_name: string; from_account: string; to_account: string }
interface Acc { id: string; code: string; name: string; type: string; is_postable: boolean; is_active?: boolean }

function useAccounts(cid: string): Acc[] {
  const [a] = useLoad<Acc[]>(cid && `/companies/${cid}/accounts`, []);
  return a.filter((x) => x.is_postable && x.type === 'asset' && x.is_active !== false);
}

function Interco({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const { companies } = useCompany();
  const [links, reload] = useLoad<ICLink[]>('/intercompany/links', []);
  const [docs, reloadDocs] = useLoad<ICDoc[]>('/intercompany/docs', []);
  const [trs, reloadTrs] = useLoad<ICTransfer[]>('/intercompany/transfers', []);
  const [msg, run] = useRun(() => { reload(); reloadDocs(); reloadTrs(); });
  const [nl, setNl] = useState({ a: '', b: '' });
  const [tf, setTf] = useState({ link: '', dir: 'a', kind: 'loan', date: today(), currency: 'USD', amount: '', from: '', to: '', memo: '' });
  const manage = can(me, 'accounting.manage');
  const edit = can(me, 'accounting.edit');
  const link = links.find((l) => l.id === tf.link);
  const payer = link ? (tf.dir === 'a' ? link.company_a : link.company_b) : '';
  const recv = link ? (tf.dir === 'a' ? link.company_b : link.company_a) : '';
  const payAcc = useAccounts(payer);
  const recvAcc = useAccounts(recv);
  const act = companies.filter((c) => c.is_active);
  return (
    <div className="mx-col" style={{ gap: 8 }}>
      <section className="mx-box">
        <h3>{t('ep.ic.links')}</h3>
        {manage && act.length > 1 && (
          <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
            <label className="mx-fld">{t('ep.ic.a')}<select value={nl.a} onChange={(e) => setNl({ ...nl, a: e.currentTarget.value })}><option value="">—</option>
              {act.map((c) => <option key={c.id} value={c.id}>{companyName(c, i18n.language)}</option>)}</select></label>
            <label className="mx-fld">{t('ep.ic.b')}<select value={nl.b} onChange={(e) => setNl({ ...nl, b: e.currentTarget.value })}><option value="">—</option>
              {act.filter((c) => c.id !== nl.a).map((c) => <option key={c.id} value={c.id}>{companyName(c, i18n.language)}</option>)}</select></label>
            <button className="mx-btn main" disabled={!nl.a || !nl.b} title={!nl.a || !nl.b ? t('ep.ic.pickTwo') : undefined}
              onClick={() => void run(api('/intercompany/links', { method: 'POST', body: { company_a: nl.a, company_b: nl.b } }), () => setNl({ a: '', b: '' }))}>{t('ep.ic.link')}</button>
            {edit && <button className="mx-btn" onClick={() => void run(api('/intercompany/sync', { method: 'POST', body: {} }))}>{t('ep.ic.sync')}</button>}
          </div>)}
        {act.length < 2 && <div className="mx-empty">{t('ep.ic.needTwo')}</div>}
        <Flash m={msg} />
        <div style={{ overflowX: 'auto' }}>
          <table className="mx-table">
            <thead><tr><th>{t('ep.ic.a')}</th><th>{t('ep.ic.b')}</th><th className="num">{t('ep.ic.aBal')}</th><th className="num">{t('ep.ic.bBal')}</th><th>{t('ep.ic.docs')}</th><th>{t('ep.ic.auto')}</th></tr></thead>
            <tbody>
              {links.map((l) => (
                <tr key={l.id} style={{ opacity: l.is_active ? 1 : 0.55 }}>
                  <td><b>{l.company_a_name}</b><br /><small dir="ltr">{l.a_account_code}</small></td>
                  <td><b>{l.company_b_name}</b><br /><small dir="ltr">{l.b_account_code}</small></td>
                  <td className="num">{amt(l.a_balance)} {l.a_currency}</td><td className="num">{amt(l.b_balance)} {l.b_currency}</td>
                  <td>{t('ep.ic.docsN', { done: l.docs_done, wait: l.docs_waiting })}{l.docs_void > 0 && <span className="mx-pill warn">{t('ep.ic.voidN', { n: l.docs_void })}</span>}</td>
                  <td><input type="checkbox" aria-label={t('ep.ic.auto')} disabled={!manage} checked={l.auto_bills}
                    onChange={(e) => void run(api(`/intercompany/links/${l.id}`, { method: 'PATCH', body: { auto_bills: e.currentTarget.checked } }))} /></td>
                </tr>))}
              {links.length === 0 && <tr><td colSpan={6} className="mx-empty">{t('ep.ic.none')}</td></tr>}
            </tbody>
          </table>
        </div>
      </section>
      {links.length > 0 && (
        <section className="mx-box">
          <h3>{t('ep.ic.transfers')}</h3>
          {edit && (
            <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
              <label className="mx-fld">{t('ep.ic.link1')}<select value={tf.link} onChange={(e) => setTf({ ...tf, link: e.currentTarget.value, from: '', to: '' })}><option value="">—</option>
                {links.filter((l) => l.is_active).map((l) => <option key={l.id} value={l.id}>{l.company_a_name} ↔ {l.company_b_name}</option>)}</select></label>
              {link && <label className="mx-fld">{t('ep.ic.payer')}<select value={tf.dir} onChange={(e) => setTf({ ...tf, dir: e.currentTarget.value, from: '', to: '' })}>
                <option value="a">{link.company_a_name} → {link.company_b_name}</option><option value="b">{link.company_b_name} → {link.company_a_name}</option></select></label>}
              <label className="mx-fld">{t('ep.ic.kind')}<select value={tf.kind} onChange={(e) => setTf({ ...tf, kind: e.currentTarget.value })}>
                {['loan', 'transfer', 'repayment'].map((k) => <option key={k} value={k}>{t(`ep.ic.k.${k}`)}</option>)}</select></label>
              <label className="mx-fld">{t('ep.date')}<input type="date" dir="ltr" value={tf.date} onChange={(e) => setTf({ ...tf, date: e.currentTarget.value })} /></label>
              <label className="mx-fld">{t('ep.amount')}<input dir="ltr" style={{ width: 100 }} value={tf.amount} onChange={(e) => setTf({ ...tf, amount: num(e.currentTarget.value) })} /></label>
              <label className="mx-fld">{t('ep.currency')}<input dir="ltr" style={{ width: 60 }} maxLength={3} value={tf.currency} onChange={(e) => setTf({ ...tf, currency: e.currentTarget.value.toUpperCase() })} /></label>
              {link && <label className="mx-fld">{t('ep.ic.fromAcc')}<select value={tf.from} onChange={(e) => setTf({ ...tf, from: e.currentTarget.value })}><option value="">—</option>
                {payAcc.map((a) => <option key={a.id} value={a.id}>{a.code} · {a.name}</option>)}</select></label>}
              {link && <label className="mx-fld">{t('ep.ic.toAcc')}<select value={tf.to} onChange={(e) => setTf({ ...tf, to: e.currentTarget.value })}><option value="">—</option>
                {recvAcc.map((a) => <option key={a.id} value={a.id}>{a.code} · {a.name}</option>)}</select></label>}
              <label className="mx-fld">{t('ep.memo')}<input value={tf.memo} onChange={(e) => setTf({ ...tf, memo: e.currentTarget.value })} /></label>
              <button className="mx-btn main" disabled={!link || !tf.amount || !tf.from || !tf.to} title={!link || !tf.amount || !tf.from || !tf.to ? t('ep.ic.fillTransfer') : undefined}
                onClick={() => void run(api('/intercompany/transfers', { method: 'POST', body: { link_id: tf.link, from_company: payer, kind: tf.kind, date: tf.date,
                  currency: tf.currency, amount: tf.amount, from_account_id: tf.from, to_account_id: tf.to, memo: tf.memo } }), () => setTf({ ...tf, amount: '', memo: '' }))}>{t('ep.ic.post')}</button>
            </div>)}
          <div style={{ overflowX: 'auto' }}>
            <table className="mx-table">
              <thead><tr><th>{t('ep.date')}</th><th>{t('ep.ic.kind')}</th><th>{t('ep.ic.payer')}</th><th>{t('ep.ic.fromAcc')}</th><th>{t('ep.ic.toAcc')}</th><th className="num">{t('ep.amount')}</th><th>{t('ep.status')}</th><th /></tr></thead>
              <tbody>
                {trs.map((x) => (
                  <tr key={x.id} style={{ opacity: x.status === 'void' ? 0.5 : 1 }}>
                    <td>{formatDate(x.transfer_date)}</td><td>{t(`ep.ic.k.${x.kind}`)}</td><td>{x.from_name} → {x.to_name}</td><td>{x.from_account}</td><td>{x.to_account}</td>
                    <td className="num">{amt(x.amount)} {x.currency}</td><td>{t(`ep.st.${x.status}`)}</td>
                    <td>{x.status === 'posted' && can(me, 'accounting.void') && <button className="mx-btn sm danger" onClick={() => void run(api(`/intercompany/transfers/${x.id}/void`, { method: 'POST', body: {} }))}>{t('ep.void')}</button>}</td>
                  </tr>))}
                {trs.length === 0 && <tr><td colSpan={8} className="mx-empty">{t('ep.ic.noTransfers')}</td></tr>}
              </tbody>
            </table>
          </div>
        </section>)}
      {docs.length > 0 && (
        <section className="mx-box">
          <h3>{t('ep.ic.mirrored')}</h3>
          <div style={{ overflowX: 'auto' }}>
            <table className="mx-table">
              <thead><tr><th>{t('ep.date')}</th><th>{t('ep.ic.sale')}</th><th>{t('ep.ic.purchase')}</th><th className="num">{t('ep.amount')}</th><th>{t('ep.status')}</th></tr></thead>
              <tbody>
                {docs.map((d) => (
                  <tr key={d.id}>
                    <td>{formatDate(d.issue_date)}</td><td>{d.src_name} · #{d.invoice_number}</td><td>{d.dst_name}{d.bill_number ? ` · #${d.bill_number}` : ''}</td>
                    <td className="num">{amt(d.amount)} {d.currency}</td>
                    <td><span className={`mx-pill ${d.status === 'done' ? 'good' : d.status === 'error' ? 'bad' : 'warn'}`}>{t(`ep.ic.ds.${d.status}`)}</span>{d.error && <small> {d.error}</small>}</td>
                  </tr>))}
              </tbody>
            </table>
          </div>
        </section>)}
    </div>
  );
}

// ======================= consolidation =======================

interface CRow { code: string; name: string; type: string; amounts: Record<string, string>; total: string; elimination?: string; consolidated?: string }
interface CRep {
  currency: string; companies: { id: string; name: string; base_currency: string; rate: string }[]; rows: CRow[]; totals: Record<string, string>;
  totals_after?: Record<string, string>; eliminations?: { code: string; name: string; type: string; reason: string; amount: string }[];
}

function Consolidation() {
  const { t, i18n } = useTranslation();
  const { companies } = useCompany();
  const act = companies.filter((c) => c.is_active);
  const [sel, setSel] = useState<string[]>(() => act.map((c) => c.id));
  const [report, setReport] = useState<'balance-sheet' | 'profit-loss'>('balance-sheet');
  const [cur, setCur] = useState('USD');
  const [from, setFrom] = useState(today().slice(0, 4) + '-01-01');
  const [to, setTo] = useState(today());
  const [elim, setElim] = useState(true);
  const [rep, setRep] = useState<CRep | null>(null);
  const [err, setErr] = useState<Msg>(null);
  const load = () => {
    setErr(null);
    const q = new URLSearchParams({ report, companies: sel.join(','), currency: cur, from, to, eliminate: elim ? '1' : '0' });
    api<CRep>(`/reports/consolidated?${q}`).then(setRep).catch((e) => { setRep(null); setErr({ ok: false, text: String((e as Error).message) }); });
  };
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(load, []);
  const typeOrder = report === 'balance-sheet' ? ['asset', 'liability', 'equity'] : ['income', 'expense'];
  const tot = rep?.totals ?? {};
  const after = rep?.totals_after;
  const keys = report === 'balance-sheet' ? ['assets', 'liabilities', 'equity', 'current_result', 'liabilities_equity'] : ['income', 'expense', 'net'];
  return (
    <div className="mx-col" style={{ gap: 8 }}>
      <section className="mx-box">
        <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
          <label className="mx-fld">{t('ep.cons.report')}<select value={report} onChange={(e) => setReport(e.currentTarget.value as 'balance-sheet' | 'profit-loss')}>
            <option value="balance-sheet">{t('ep.cons.bs')}</option><option value="profit-loss">{t('ep.cons.pl')}</option></select></label>
          <label className="mx-fld">{t('ep.currency')}<input dir="ltr" style={{ width: 60 }} maxLength={3} value={cur} onChange={(e) => setCur(e.currentTarget.value.toUpperCase())} /></label>
          {report === 'profit-loss' && <label className="mx-fld">{t('ep.from')}<input type="date" dir="ltr" value={from} onChange={(e) => setFrom(e.currentTarget.value)} /></label>}
          <label className="mx-fld">{t('ep.to')}<input type="date" dir="ltr" value={to} onChange={(e) => setTo(e.currentTarget.value)} /></label>
          <label><input type="checkbox" checked={elim} onChange={(e) => setElim(e.currentTarget.checked)} /> {t('ep.cons.eliminate')}</label>
          <button className="mx-btn main" disabled={sel.length === 0} onClick={load}>{t('ep.show')}</button>
        </div>
        <div className="mx-row" style={{ gap: 10, padding: 6, flexWrap: 'wrap' }}>
          {act.map((c) => <label key={c.id}><input type="checkbox" checked={sel.includes(c.id)} onChange={() => setSel(sel.includes(c.id) ? sel.filter((x) => x !== c.id) : [...sel, c.id])} /> {companyName(c, i18n.language)}</label>)}
        </div>
        <Flash m={err} />
      </section>
      {rep && (
        <section className="mx-box">
          <div style={{ overflowX: 'auto' }}>
            <table className="mx-table">
              <thead><tr><th>{t('ep.cons.account')}</th>{rep.companies.map((c) => <th key={c.id} className="num">{c.name}<br /><small dir="ltr">{c.base_currency} × {Number(c.rate)}</small></th>)}
                <th className="num">{t('ep.cons.total')}</th>{after && <><th className="num">{t('ep.cons.elim')}</th><th className="num">{t('ep.cons.after')}</th></>}</tr></thead>
              <tbody>
                {typeOrder.map((ty) => (
                  <Fragment key={ty}>
                    <tr><td colSpan={rep.companies.length + (after ? 4 : 2)}><b>{t(`ep.cons.ty.${ty}`)}</b></td></tr>
                    {rep.rows.filter((r) => r.type === ty).map((r) => (
                      <tr key={r.code + r.type}>
                        <td><span dir="ltr">{r.code}</span> {r.name}</td>
                        {rep.companies.map((c) => <td key={c.id} className="num">{r.amounts[c.id] ? amt(r.amounts[c.id]) : ''}</td>)}
                        <td className="num">{amt(r.total)}</td>
                        {after && <><td className={`num ${Number(r.elimination) ? 'red' : ''}`}>{Number(r.elimination) ? amt(r.elimination) : ''}</td><td className="num"><b>{amt(r.consolidated ?? r.total)}</b></td></>}
                      </tr>))}
                  </Fragment>))}
              </tbody>
            </table>
          </div>
          <div className="mx-row" style={{ gap: 16, padding: 6, flexWrap: 'wrap' }}>
            {keys.map((k) => <div key={k}><small>{t(`ep.cons.k.${k}`)}</small><br /><b>{amt(tot[k])}</b>{after && after[k] !== tot[k] && <> → <b>{amt(after[k])}</b></>} {rep.currency}</div>)}
          </div>
          {rep.eliminations && rep.eliminations.length > 0 && (
            <>
              <h3>{t('ep.cons.elims')}</h3>
              <table className="mx-table">
                <thead><tr><th>{t('ep.cons.account')}</th><th>{t('ep.cons.why')}</th><th className="num">{t('ep.amount')}</th></tr></thead>
                <tbody>{rep.eliminations.map((e, i) => <tr key={i}><td><span dir="ltr">{e.code}</span> {e.name}</td><td>{t(`ep.cons.r.${e.reason}`)}</td><td className="num">{amt(e.amount)}</td></tr>)}</tbody>
              </table>
            </>)}
          <small style={{ display: 'block', padding: 6 }}>{t('ep.cons.note')}</small>
        </section>)}
    </div>
  );
}

// ======================= document layouts =======================

const DOCS: DocType[] = ['invoice', 'receipt', 'quote', 'order', 'po', 'bill'];

function Layouts({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const base = useBase();
  const { company } = useCompany();
  const [all, reload] = useLoad<Record<string, DocLayout>>(base && `${base}/doc-layouts`, {});
  const [doc, setDoc] = useState<DocType>('invoice');
  const [l, setL] = useState<DocLayout>({});
  const [msg, run] = useRun(() => { reload(); if (company) forgetLayouts(company.id); });
  useEffect(() => { setL(all[doc] ?? {}); }, [all, doc]);
  useEffect(() => { if (l.bilingual) void ensureBundle(l.bilingual); }, [l.bilingual]);
  const manage = can(me, 'settings.manage');
  const up = (p: Partial<DocLayout>) => setL({ ...l, ...p });
  const order = l.order && l.order.length ? l.order : [...LAYOUT_BLOCKS];
  const move = (i: number, d: number) => { const o = [...order]; const j = i + d; if (j < 0 || j >= o.length) return; [o[i], o[j]] = [o[j], o[i]]; up({ order: o }); };
  const ov = useMemo(() => ({ [doc]: l }), [doc, l]);
  const t2 = l.bilingual && l.bilingual !== i18n.language ? i18n.getFixedT(l.bilingual) : null;
  const titleKey = doc === 'invoice' ? 'invoices.invoice' : doc === 'receipt' ? 'payments.receipt' : doc === 'quote' ? 'sales.quote' : doc === 'order' ? 'sales.order' : doc === 'po' ? 'oa.po.poTitle' : 'pur.bill';
  const c = { company_name: company ? companyName(company, i18n.language) : 'Paxve', company_header: '', company_footer: '' };
  return (
    <div className="mx-row" style={{ gap: 8, alignItems: 'flex-start', flexWrap: 'wrap' }}>
      <section className="mx-box" style={{ flex: '1 1 380px', minWidth: 0 }}>
        <div className="mx-modes" role="tablist">
          {DOCS.map((d) => <button key={d} role="tab" className="mx-mode" aria-selected={doc === d} onClick={() => setDoc(d)}>{t(`ep.lay.d.${d}`)}</button>)}
        </div>
        <div className="mx-col" style={{ gap: 6, padding: 6 }}>
          <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap', alignItems: 'end' }}>
            <label className="mx-fld">{t('ep.lay.accent')}<input type="color" value={l.accent || '#1d4ed8'} onChange={(e) => up({ accent: e.currentTarget.value })} /></label>
            <label><input type="checkbox" checked={l.show_logo !== false} onChange={(e) => up({ show_logo: e.currentTarget.checked })} /> {t('ep.lay.logo')}</label>
            <label className="mx-fld">{t('ep.lay.logoSize')}<select value={l.logo_size || 'md'} onChange={(e) => up({ logo_size: e.currentTarget.value as DocLayout['logo_size'] })}>
              {['sm', 'md', 'lg'].map((s) => <option key={s} value={s}>{t(`ep.lay.sz.${s}`)}</option>)}</select></label>
            <label className="mx-fld">{t('ep.lay.paper')}<select value={l.paper || 'a4'} onChange={(e) => up({ paper: e.currentTarget.value as DocLayout['paper'] })}>
              {['a4', 'a5', '80', '58'].map((s) => <option key={s} value={s}>{t(`ep.lay.p.${s}`)}</option>)}</select></label>
            <label className="mx-fld">{t('ep.lay.font')}<input type="number" min={9} max={18} style={{ width: 60 }} value={l.font_size || ''} onChange={(e) => up({ font_size: Number(e.currentTarget.value) || undefined })} /></label>
            <label className="mx-fld">{t('ep.lay.align')}<select value={l.align || 'start'} onChange={(e) => up({ align: e.currentTarget.value as DocLayout['align'] })}>
              <option value="start">{t('ep.lay.al.start')}</option><option value="center">{t('ep.lay.al.center')}</option></select></label>
            <label><input type="checkbox" checked={!!l.border} onChange={(e) => up({ border: e.currentTarget.checked })} /> {t('ep.lay.border')}</label>
          </div>
          <label className="mx-fld">{t('ep.lay.title')}<input value={l.title || ''} maxLength={80} placeholder={t(titleKey)} onChange={(e) => up({ title: e.currentTarget.value })} /></label>
          <label className="mx-fld">{t('ep.lay.header')}<textarea rows={2} maxLength={600} value={l.header || ''} onChange={(e) => up({ header: e.currentTarget.value })} /></label>
          <label className="mx-fld">{t('ep.lay.footer')}<textarea rows={2} maxLength={600} value={l.footer || ''} onChange={(e) => up({ footer: e.currentTarget.value })} /></label>
          <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap', alignItems: 'end' }}>
            <label className="mx-fld">{t('ep.lay.second')}<select value={l.bilingual || ''} onChange={(e) => up({ bilingual: e.currentTarget.value || undefined })}>
              <option value="">{t('ep.lay.none')}</option>{languages.map((x) => <option key={x.code} value={x.code}>{x.label}</option>)}</select></label>
            <label className="mx-fld">{t('ep.lay.qr')}<select value={l.qr || 'none'} onChange={(e) => up({ qr: e.currentTarget.value as DocLayout['qr'] })}>
              {['none', 'doc', 'custom'].map((s) => <option key={s} value={s}>{t(`ep.lay.q.${s}`)}</option>)}</select></label>
            {l.qr === 'custom' && <label className="mx-fld" style={{ flex: 1 }}>{t('ep.lay.qrText')}<input dir="ltr" maxLength={300} value={l.qr_text || ''} onChange={(e) => up({ qr_text: e.currentTarget.value })} /></label>}
          </div>
          <fieldset style={{ border: '1px solid var(--mx-line-strong)', borderRadius: 6 }}>
            <legend>{t('ep.lay.fields')}</legend>
            <div className="mx-row" style={{ gap: 10, flexWrap: 'wrap' }}>
              {LAYOUT_FIELDS.map((f) => (
                <label key={f}><input type="checkbox" checked={!(l.hide ?? []).includes(f)}
                  onChange={(e) => up({ hide: e.currentTarget.checked ? (l.hide ?? []).filter((x) => x !== f) : [...(l.hide ?? []), f] })} /> {t(`ep.lay.f.${f}`)}</label>))}
            </div>
          </fieldset>
          <fieldset style={{ border: '1px solid var(--mx-line-strong)', borderRadius: 6 }}>
            <legend>{t('ep.lay.order')}</legend>
            {order.map((b, i) => (
              <div key={b} className="mx-row" style={{ gap: 6, alignItems: 'center' }}>
                <span style={{ minWidth: 120 }}>{i + 1}. {t(`ep.lay.b.${b}`)}</span>
                <button className="mx-btn sm" aria-label={t('ep.up')} disabled={i === 0} onClick={() => move(i, -1)}>↑</button>
                <button className="mx-btn sm" aria-label={t('ep.down')} disabled={i === order.length - 1} onClick={() => move(i, 1)}>↓</button>
              </div>))}
          </fieldset>
          <Flash m={msg} />
          {manage && (
            <div className="mx-row" style={{ gap: 6 }}>
              <button className="mx-btn main" onClick={() => void run(api(`${base}/doc-layouts/${doc}`, { method: 'PUT', body: l }))}>{t('ep.save')}</button>
              <button className="mx-btn" onClick={() => void run(api(`${base}/doc-layouts/${doc}`, { method: 'PUT', body: {} }))}>{t('ep.lay.reset')}</button>
            </div>)}
        </div>
      </section>
      <section className="mx-box" style={{ flex: '1 1 460px', minWidth: 0, background: 'var(--mx-card)' }}>
        <h3>{t('ep.lay.preview')}</h3>
        <LayoutOverride.Provider value={ov}>
          <div className="print-doc" data-doc={doc} style={{ padding: 12 }}>
            <LayoutStyle type={doc} layout={l} />
            <DocHeader c={c} layout={l} title={`${t(titleKey)} #1024`} title2={t2 ? `${t2(titleKey)} #1024` : undefined}
              lines={[<span key="d">{t('ep.date')}: {formatDate(today())}</span>, <span key="du" className="f-due_date">{t('ep.lay.f.due_date')}: {formatDate(today())}</span>]} />
            <div className="b-customer">
              <hr className="doc-rule" />
              <b>{t('ep.lay.sampleCustomer')}</b> <small className="f-customer_code">(C-0042)</small><br />
              <small><span className="f-customer_phone">03 412 877 · </span><span className="f-customer_address">{t('ep.lay.sampleAddress')}</span></small>
            </div>
            <table className="b-lines" style={{ marginTop: 10, width: '100%' }}>
              <thead><tr><th>{t('ep.lay.desc')}</th><th className="num">{t('ep.amount')}</th></tr></thead>
              <tbody><tr><td>{t('ep.lay.sampleLine')}</td><td className="num">38.00</td></tr><tr><td>{t('ep.lay.sampleLine2')}</td><td className="num">12.00</td></tr></tbody>
              <tfoot><tr><th>{t('ep.cons.total')}</th><th className="num">50.00 USD</th></tr>
                <tr className="f-local_total"><td>LBP</td><td className="num">4,475,000</td></tr>
                <tr className="f-paid"><td>{t('ep.lay.f.paid')}</td><td className="num">20.00</td></tr>
                <tr className="f-balance"><th>{t('ep.lay.f.balance')}</th><th className="num">30.00 USD</th></tr></tfoot>
            </table>
            <div className="b-notes f-legal"><small>{t('ep.lay.sampleLegal')}</small></div>
            <DocFooter c={c} layout={l} qr={`${c.company_name} #1024 50.00 USD`} />
          </div>
        </LayoutOverride.Provider>
      </section>
    </div>
  );
}

// ======================= role dashboards =======================

const BUILTIN_ROLES = ['admin', 'accountant', 'data_entry', 'read_only', 'collector', 'cashier'];

function RoleDashboards({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [saved, reload] = useLoad<{ role: string; widgets: Widget[] }[]>(base && `${base}/dashboards/roles`, []);
  const [roles] = useLoad<{ id: string; name: string; is_active: boolean }[]>(can(me, 'users.view') && '/roles', []);
  const [role, setRole] = useState('company');
  const [ws, setWs] = useState<Widget[]>([]);
  const [msg, run] = useRun(reload);
  const keyOf = (id: string) => id.replace(/-/g, '').slice(0, 32);
  useEffect(() => { setWs(saved.find((s) => s.role === role)?.widgets ?? []); }, [saved, role]);
  const manage = can(me, 'settings.manage');
  return (
    <section className="mx-box">
      <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
        <label className="mx-fld">{t('ep.dash.for')}<select value={role} onChange={(e) => setRole(e.currentTarget.value)}>
          <option value="company">{t('ep.dash.everyone')}</option>
          {BUILTIN_ROLES.map((r) => <option key={r} value={r}>{t(`roles.${r}`, { defaultValue: r })}</option>)}
          {roles.filter((r) => r.is_active).map((r) => <option key={r.id} value={keyOf(r.id)}>{r.name}</option>)}</select></label>
        <small>{saved.some((s) => s.role === role) ? t('ep.dash.saved') : t('ep.dash.notSaved')}</small>
      </div>
      <WidgetEditor widgets={ws} onChange={setWs} />
      <Flash m={msg} />
      {manage && (
        <div className="mx-row" style={{ gap: 6, padding: 6 }}>
          <button className="mx-btn main" onClick={() => void run(api(`${base}/dashboards/roles/${role}`, { method: 'PUT', body: { widgets: ws } }))}>{t('ep.save')}</button>
          <button className="mx-btn" onClick={() => void run(api(`${base}/dashboards/roles/${role}`, { method: 'DELETE' }))}>{t('ep.dash.remove')}</button>
        </div>)}
    </section>
  );
}

// ======================= sign-in policy =======================

interface SecSet { require_mfa: boolean; password_min_length: number; password_complexity: boolean; password_expiry_days: number; sso_allowed: boolean; users: number; users_with_mfa: number }
interface SSO { allowed: boolean; providers: Record<string, boolean>; redirect_google: string; redirect_microsoft: string }
interface LoginEv { id: string; at: string; email: string; ip: string | null; user_agent: string; method: string; success: boolean; reason: string; full_name: string | null }

function SecurityPolicy({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [s, reload] = useLoad<SecSet | null>('/security/settings', null);
  const [sso, reloadSso] = useLoad<SSO | null>('/security/sso', null);
  const [idle, reloadIdle] = useLoad<{ minutes: number } | null>(base && `${base}/idle-logout`, null);
  const [failed, setFailed] = useState(false);
  const [hist] = useLoad<LoginEv[]>(`/security/login-history${failed ? '?failed=1' : ''}`, []);
  const [msg, run] = useRun(() => { reload(); reloadSso(); reloadIdle(); });
  const [exp, setExp] = useState('');
  const [mins, setMins] = useState('');
  useEffect(() => { if (s) setExp(String(s.password_expiry_days)); }, [s]);
  useEffect(() => { if (idle) setMins(String(idle.minutes)); }, [idle]);
  const edit = can(me, 'users.edit');
  const put = (body: Record<string, unknown>) => void run(api('/security/settings', { method: 'PUT', body }));
  return (
    <div className="mx-row" style={{ gap: 8, alignItems: 'flex-start', flexWrap: 'wrap' }}>
      <section className="mx-box" style={{ flex: '1 1 380px', minWidth: 0 }}>
        <h3>{t('ep.sec.policy')}</h3>
        {s && (
          <div className="mx-col" style={{ gap: 8, padding: 6 }}>
            <label><input type="checkbox" disabled={!edit} checked={s.password_complexity} onChange={(e) => put({ password_complexity: e.currentTarget.checked })} /> {t('ep.sec.complexity')}</label>
            <small>{t('ep.sec.complexityHelp')}</small>
            <div className="mx-row" style={{ gap: 6, alignItems: 'end' }}>
              <label className="mx-fld">{t('ep.sec.expiry')}<input type="number" min={0} max={730} style={{ width: 80 }} disabled={!edit} value={exp} onChange={(e) => setExp(e.currentTarget.value)} /></label>
              {edit && <button className="mx-btn" disabled={exp === String(s.password_expiry_days)} onClick={() => put({ password_expiry_days: Number(exp) || 0 })}>{t('ep.save')}</button>}
            </div>
            <small>{t('ep.sec.expiryHelp')}</small>
            <div className="mx-row" style={{ gap: 6, alignItems: 'end' }}>
              <label className="mx-fld">{t('ep.sec.idle')}<input type="number" min={0} max={1440} style={{ width: 80 }} disabled={!edit} value={mins} onChange={(e) => setMins(e.currentTarget.value)} /></label>
              {edit && <button className="mx-btn" disabled={!idle || mins === String(idle.minutes)} title={Number(mins) > 0 && Number(mins) < 5 ? t('ep.sec.idleMin') : undefined}
                onClick={() => void run(api(`${base}/idle-logout`, { method: 'PUT', body: { minutes: Number(mins) || 0 } }))}>{t('ep.save')}</button>}
            </div>
            <small>{t('ep.sec.idleHelp')}</small>
            <small>{t('ep.sec.mfaCount', { n: s.users_with_mfa, total: s.users })}</small>
          </div>)}
        <h3>{t('ep.sec.sso')}</h3>
        {sso && (
          <div className="mx-col" style={{ gap: 6, padding: 6 }}>
            <label><input type="checkbox" disabled={!edit} checked={sso.allowed} onChange={(e) => put({ sso_allowed: e.currentTarget.checked })} /> {t('ep.sec.ssoAllow')}</label>
            {(['google', 'microsoft'] as const).map((p) => (
              <div key={p}><b>{t(`ep.sec.p.${p}`)}</b>: {sso.providers[p] ? <span className="mx-pill good">{t('ep.sec.ready')}</span> : <span className="mx-pill warn">{t('ep.sec.notSet')}</span>}
                {!sso.providers[p] && <small style={{ display: 'block' }}>{t('ep.sec.howTo')} <code dir="ltr">{p === 'google' ? sso.redirect_google : sso.redirect_microsoft}</code></small>}</div>))}
          </div>)}
        <Flash m={msg} />
      </section>
      <section className="mx-box" style={{ flex: '2 1 520px', minWidth: 0 }}>
        <h3>{t('ep.sec.history')}</h3>
        <label style={{ padding: 6, display: 'block' }}><input type="checkbox" checked={failed} onChange={(e) => setFailed(e.currentTarget.checked)} /> {t('ep.sec.onlyFailed')}</label>
        <div style={{ overflowX: 'auto' }}>
          <table className="mx-table">
            <thead><tr><th>{t('ep.sec.when')}</th><th>{t('ep.br.user')}</th><th>{t('ep.sec.how')}</th><th>{t('ep.sec.ip')}</th><th>{t('ep.status')}</th></tr></thead>
            <tbody>
              {hist.map((h) => (
                <tr key={h.id}>
                  <td>{formatDateTime(h.at)}</td><td>{h.full_name ?? ''}<br /><small dir="ltr">{h.email}</small></td><td>{t(`ep.sec.m.${h.method}`)}</td><td dir="ltr">{h.ip ?? ''}</td>
                  <td>{h.success ? <span className="mx-pill good">{t('ep.sec.ok')}</span> : <span className="mx-pill bad">{t(`ep.sec.r.${h.reason || 'failed'}`, { defaultValue: h.reason })}</span>}</td>
                </tr>))}
              {hist.length === 0 && <tr><td colSpan={5} className="mx-empty">{t('ep.sec.noHistory')}</td></tr>}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  );
}

// ======================= tenders =======================

interface Tender { id: string; number: number; title: string; currency: string; deadline: string | null; status: string; lines: number; bids: number; orders: number }
interface TLine { id: string; line_no: number; description: string; quantity: string; best_bid_id: string | null; best_price: string | null; awarded_qty: string | null }
interface TBid { id: string; supplier_id: string; supplier_name: string; lead_days: number | null; valid_until: string | null; status: string; total: string; lines: { tender_line_id: string; unit_price: string; quantity: string | null }[] }
interface TDetail { tender: Tender & { notes: string }; lines: TLine[]; bids: TBid[]; orders: { id: string; number: number; status: string; total: string; supplier_name: string }[]; agreements: { id: string; number: number; supplier_name: string }[] }
interface Sup { id: string; code: string; name: string; status: string }

function useSuppliers(): Sup[] {
  const base = useBase();
  const [s] = useLoad<Sup[]>(base && `${base}/suppliers`, []);
  return s.filter((x) => x.status === 'active');
}

function Tenders({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const { company } = useCompany();
  const [rows, reload] = useLoad<Tender[]>(base && `${base}/purchasing/tenders`, []);
  const [sel, setSel] = useState<string | null>(null);
  const [nt, setNt] = useState({ title: '', deadline: '', lines: [{ description: '', quantity: '' }] });
  const [msg, run] = useRun(reload);
  const create = can(me, 'purchasing.create');
  return (
    <div className="mx-row" style={{ gap: 8, alignItems: 'flex-start', flexWrap: 'wrap' }}>
      <section className="mx-box" style={{ flex: '1 1 360px', minWidth: 0 }}>
        <h3>{t('ep.td.list')}</h3>
        {create && (
          <div className="mx-col" style={{ gap: 6, padding: 6 }}>
            <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap', alignItems: 'end' }}>
              <label className="mx-fld" style={{ flex: 1 }}>{t('ep.title')}<input value={nt.title} onChange={(e) => setNt({ ...nt, title: e.currentTarget.value })} /></label>
              <label className="mx-fld">{t('ep.td.deadline')}<input type="date" dir="ltr" value={nt.deadline} onChange={(e) => setNt({ ...nt, deadline: e.currentTarget.value })} /></label>
            </div>
            {nt.lines.map((l, i) => (
              <div key={i} className="mx-row" style={{ gap: 6 }}>
                <input style={{ flex: 1 }} placeholder={t('ep.lay.desc')} aria-label={t('ep.lay.desc')} value={l.description}
                  onChange={(e) => setNt({ ...nt, lines: nt.lines.map((x, j) => (j === i ? { ...x, description: e.currentTarget.value } : x)) })} />
                <input dir="ltr" style={{ width: 80 }} placeholder={t('ep.qty')} aria-label={t('ep.qty')} value={l.quantity}
                  onChange={(e) => setNt({ ...nt, lines: nt.lines.map((x, j) => (j === i ? { ...x, quantity: num(e.currentTarget.value) } : x)) })} />
              </div>))}
            <div className="mx-row" style={{ gap: 6 }}>
              <button className="mx-btn sm" onClick={() => setNt({ ...nt, lines: [...nt.lines, { description: '', quantity: '' }] })}>{t('ep.addLine')}</button>
              <button className="mx-btn main" disabled={!nt.title.trim() || !nt.lines.some((l) => l.description && Number(l.quantity) > 0)} title={!nt.title.trim() ? t('ep.td.needTitle') : undefined}
                onClick={() => void run(api<TDetail>(`${base}/purchasing/tenders`, { method: 'POST', body: { title: nt.title, currency: company?.base_currency ?? 'USD', deadline: nt.deadline || undefined,
                  lines: nt.lines.filter((l) => l.description && Number(l.quantity) > 0) } }), (x) => { setSel((x as TDetail).tender.id); setNt({ title: '', deadline: '', lines: [{ description: '', quantity: '' }] }); })}>{t('ep.td.create')}</button>
            </div>
          </div>)}
        <Flash m={msg} />
        <table className="mx-table">
          <thead><tr><th>#</th><th>{t('ep.title')}</th><th>{t('ep.status')}</th><th className="num">{t('ep.td.bids')}</th></tr></thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.id} className={sel === r.id ? 'sel' : ''} style={{ cursor: 'pointer' }} onClick={() => setSel(r.id)}>
                <td dir="ltr">T-{r.number}</td><td>{r.title}{r.deadline && <small> · {formatDate(r.deadline)}</small>}</td><td>{t(`ep.td.st.${r.status}`)}</td><td className="num">{r.bids}</td>
              </tr>))}
            {rows.length === 0 && <tr><td colSpan={4} className="mx-empty">{t('ep.td.none')}</td></tr>}
          </tbody>
        </table>
      </section>
      {sel && <TenderDetail key={sel} id={sel} me={me} onChanged={reload} />}
    </div>
  );
}

function TenderDetail({ id, me, onChanged }: { id: string; me: Me; onChanged: () => void }) {
  const { t } = useTranslation();
  const base = useBase();
  const sups = useSuppliers();
  const [d, reload] = useLoad<TDetail | null>(base && `${base}/purchasing/tenders/${id}`, null);
  const [msg, run] = useRun(() => { reload(); onChanged(); });
  const [bid, setBid] = useState<{ supplier_id: string; lead_days: string; prices: Record<string, string> }>({ supplier_id: '', lead_days: '', prices: {} });
  const [pick, setPick] = useState<Record<string, string>>({});
  const [asAg, setAsAg] = useState(false);
  const [agEnd, setAgEnd] = useState(`${new Date().getFullYear() + 1}-12-31`);
  useEffect(() => {
    if (!d) return;
    const p: Record<string, string> = {};
    d.lines.forEach((l) => { if (l.best_bid_id) p[l.id] = l.best_bid_id; });
    setPick(p);
  }, [d]);
  if (!d) return null;
  const tn = d.tender;
  const open = tn.status === 'open' || tn.status === 'closed' || tn.status === 'draft';
  const create = can(me, 'purchasing.create');
  const approve = can(me, 'purchasing.approve');
  const status = (s: string) => void run(api(`${base}/purchasing/tenders/${id}/status`, { method: 'POST', body: { status: s } }));
  const priceOf = (b: TBid, line: string) => b.lines.find((x) => x.tender_line_id === line)?.unit_price;
  return (
    <section className="mx-box" style={{ flex: '999 1 560px', minWidth: 0 }}>
      <h3>T-{tn.number} · {tn.title} <small>({t(`ep.td.st.${tn.status}`)})</small></h3>
      {create && (
        <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap' }}>
          {(tn.status === 'draft' || tn.status === 'closed') && <button className="mx-btn" onClick={() => status('open')}>{t('ep.td.open')}</button>}
          {tn.status === 'open' && <button className="mx-btn" onClick={() => status('closed')}>{t('ep.td.close')}</button>}
          {open && <button className="mx-btn danger" onClick={() => status('cancelled')}>{t('ep.td.cancel')}</button>}
        </div>)}
      <Flash m={msg} />
      <div style={{ overflowX: 'auto' }}>
        <table className="mx-table">
          <thead><tr><th>{t('ep.lay.desc')}</th><th className="num">{t('ep.qty')}</th>{d.bids.map((b) => <th key={b.id} className="num">{b.supplier_name}<br /><small>{b.lead_days != null ? t('ep.td.days', { n: b.lead_days }) : ''}</small></th>)}
            {open && approve && d.bids.length > 0 && <th>{t('ep.td.award')}</th>}</tr></thead>
          <tbody>
            {d.lines.map((l) => (
              <tr key={l.id}>
                <td>{l.description}</td><td className="num">{Number(l.quantity)}</td>
                {d.bids.map((b) => {
                  const p = priceOf(b, l.id);
                  return <td key={b.id} className="num" style={l.best_bid_id === b.id ? { background: 'var(--mx-green-soft)', fontWeight: 700 } : undefined}>{p != null ? amt(p, 2) : '—'}</td>;
                })}
                {open && approve && d.bids.length > 0 && <td><select aria-label={t('ep.td.award')} value={pick[l.id] ?? ''} onChange={(e) => setPick({ ...pick, [l.id]: e.currentTarget.value })}>
                  <option value="">—</option>{d.bids.filter((b) => priceOf(b, l.id) != null).map((b) => <option key={b.id} value={b.id}>{b.supplier_name}</option>)}</select></td>}
              </tr>))}
            {d.bids.length > 0 && <tr><th>{t('ep.cons.total')}</th><th />{d.bids.map((b) => <th key={b.id} className="num">{amt(b.total)} {tn.currency}</th>)}{open && approve && <th />}</tr>}
          </tbody>
        </table>
      </div>
      {open && approve && d.bids.length > 0 && (
        <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
          <label><input type="checkbox" checked={asAg} onChange={(e) => setAsAg(e.currentTarget.checked)} /> {t('ep.td.asAgreement')}</label>
          {asAg && <label className="mx-fld">{t('ep.ag.end')}<input type="date" dir="ltr" value={agEnd} onChange={(e) => setAgEnd(e.currentTarget.value)} /></label>}
          <button className="mx-btn main" disabled={!Object.values(pick).some(Boolean)} onClick={() => void run(api(`${base}/purchasing/tenders/${id}/award`, { method: 'POST', body: {
            lines: Object.entries(pick).filter(([, b]) => b).map(([line, b]) => ({ tender_line_id: line, bid_id: b })), as_agreement: asAg, start_date: today(), end_date: asAg ? agEnd : undefined } }))}>
            {asAg ? t('ep.td.awardAg') : t('ep.td.awardPo')}</button>
        </div>)}
      {open && create && (
        <>
          <h3>{t('ep.td.enterBid')}</h3>
          <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
            <label className="mx-fld">{t('ep.td.supplier')}<select value={bid.supplier_id} onChange={(e) => {
              const sid = e.currentTarget.value; const ex = d.bids.find((b) => b.supplier_id === sid);
              setBid({ supplier_id: sid, lead_days: ex?.lead_days != null ? String(ex.lead_days) : '', prices: Object.fromEntries((ex?.lines ?? []).map((x) => [x.tender_line_id, String(Number(x.unit_price))])) });
            }}><option value="">—</option>{sups.map((s) => <option key={s.id} value={s.id}>{s.code} · {s.name}</option>)}</select></label>
            <label className="mx-fld">{t('ep.td.lead')}<input dir="ltr" style={{ width: 60 }} value={bid.lead_days} onChange={(e) => setBid({ ...bid, lead_days: num(e.currentTarget.value) })} /></label>
            {d.lines.map((l) => (
              <label key={l.id} className="mx-fld">{l.description}<input dir="ltr" style={{ width: 90 }} value={bid.prices[l.id] ?? ''}
                onChange={(e) => setBid({ ...bid, prices: { ...bid.prices, [l.id]: num(e.currentTarget.value) } })} /></label>))}
            <button className="mx-btn" disabled={!bid.supplier_id || !Object.values(bid.prices).some(Boolean)} title={!bid.supplier_id ? t('ep.td.pickSupplier') : undefined}
              onClick={() => void run(api(`${base}/purchasing/tenders/${id}/bids`, { method: 'PUT', body: { supplier_id: bid.supplier_id, lead_days: bid.lead_days ? Number(bid.lead_days) : null,
                lines: Object.entries(bid.prices).filter(([, p]) => p !== '').map(([line, p]) => ({ tender_line_id: line, unit_price: p })) } }), () => setBid({ supplier_id: '', lead_days: '', prices: {} }))}>{t('ep.td.saveBid')}</button>
          </div>
        </>)}
      {(d.orders.length > 0 || d.agreements.length > 0) && (
        <small style={{ display: 'block', padding: 6 }}>
          {d.orders.map((o) => `PO #${o.number} ${o.supplier_name} (${amt(o.total)})`).join(' · ')}
          {d.agreements.map((a) => ` BA-${a.number} ${a.supplier_name}`).join(' · ')}
        </small>)}
    </section>
  );
}

// ======================= agreements =======================

interface Agreement { id: string; number: number; title: string; supplier_name: string; currency: string; start_date: string; end_date: string; status: string; usable: boolean; committed_value: string; ordered_value: string }
interface AgLine { id: string; description: string; unit_price: string; quantity: string; ordered: string; remaining: string }
interface AgDetail { agreement: Agreement; lines: AgLine[]; orders: { id: string; number: number; order_date: string; status: string; total: string }[] }

function Agreements({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const sups = useSuppliers();
  const { company } = useCompany();
  const [rows, reload] = useLoad<Agreement[]>(base && `${base}/purchasing/agreements`, []);
  const [sel, setSel] = useState<string | null>(null);
  const [na, setNa] = useState({ supplier_id: '', title: '', start: today(), end: `${new Date().getFullYear()}-12-31`, lines: [{ description: '', unit_price: '', quantity: '' }] });
  const [msg, run] = useRun(reload);
  const manage = can(me, 'purchasing.manage');
  const okNew = na.supplier_id && na.title.trim() && na.lines.some((l) => l.description && Number(l.quantity) > 0 && l.unit_price !== '');
  return (
    <div className="mx-row" style={{ gap: 8, alignItems: 'flex-start', flexWrap: 'wrap' }}>
      <section className="mx-box" style={{ flex: '1 1 380px', minWidth: 0 }}>
        <h3>{t('ep.ag.list')}</h3>
        {manage && (
          <div className="mx-col" style={{ gap: 6, padding: 6 }}>
            <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap', alignItems: 'end' }}>
              <label className="mx-fld">{t('ep.td.supplier')}<select value={na.supplier_id} onChange={(e) => setNa({ ...na, supplier_id: e.currentTarget.value })}>
                <option value="">—</option>{sups.map((s) => <option key={s.id} value={s.id}>{s.code} · {s.name}</option>)}</select></label>
              <label className="mx-fld" style={{ flex: 1 }}>{t('ep.title')}<input value={na.title} onChange={(e) => setNa({ ...na, title: e.currentTarget.value })} /></label>
              <label className="mx-fld">{t('ep.ag.start')}<input type="date" dir="ltr" value={na.start} onChange={(e) => setNa({ ...na, start: e.currentTarget.value })} /></label>
              <label className="mx-fld">{t('ep.ag.end')}<input type="date" dir="ltr" value={na.end} onChange={(e) => setNa({ ...na, end: e.currentTarget.value })} /></label>
            </div>
            {na.lines.map((l, i) => (
              <div key={i} className="mx-row" style={{ gap: 6 }}>
                <input style={{ flex: 1 }} placeholder={t('ep.lay.desc')} aria-label={t('ep.lay.desc')} value={l.description} onChange={(e) => setNa({ ...na, lines: na.lines.map((x, j) => (j === i ? { ...x, description: e.currentTarget.value } : x)) })} />
                <input dir="ltr" style={{ width: 80 }} placeholder={t('ep.price')} aria-label={t('ep.price')} value={l.unit_price} onChange={(e) => setNa({ ...na, lines: na.lines.map((x, j) => (j === i ? { ...x, unit_price: num(e.currentTarget.value) } : x)) })} />
                <input dir="ltr" style={{ width: 80 }} placeholder={t('ep.qty')} aria-label={t('ep.qty')} value={l.quantity} onChange={(e) => setNa({ ...na, lines: na.lines.map((x, j) => (j === i ? { ...x, quantity: num(e.currentTarget.value) } : x)) })} />
              </div>))}
            <div className="mx-row" style={{ gap: 6 }}>
              <button className="mx-btn sm" onClick={() => setNa({ ...na, lines: [...na.lines, { description: '', unit_price: '', quantity: '' }] })}>{t('ep.addLine')}</button>
              <button className="mx-btn main" disabled={!okNew} title={!okNew ? t('ep.ag.need') : undefined}
                onClick={() => void run(api<AgDetail>(`${base}/purchasing/agreements`, { method: 'POST', body: { supplier_id: na.supplier_id, title: na.title, currency: company?.base_currency ?? 'USD',
                  start_date: na.start, end_date: na.end, lines: na.lines.filter((l) => l.description && Number(l.quantity) > 0 && l.unit_price !== '') } }), (x) => setSel((x as AgDetail).agreement.id))}>{t('ep.ag.create')}</button>
            </div>
          </div>)}
        <Flash m={msg} />
        <table className="mx-table">
          <thead><tr><th>#</th><th>{t('ep.td.supplier')}</th><th>{t('ep.status')}</th><th className="num">{t('ep.ag.used')}</th></tr></thead>
          <tbody>
            {rows.map((r) => {
              const pct = Number(r.committed_value) ? Math.round((Number(r.ordered_value) / Number(r.committed_value)) * 100) : 0;
              return (
                <tr key={r.id} className={sel === r.id ? 'sel' : ''} style={{ cursor: 'pointer' }} onClick={() => setSel(r.id)}>
                  <td dir="ltr">BA-{r.number}</td><td>{r.supplier_name}<br /><small>{r.title} · {formatDate(r.start_date)} → {formatDate(r.end_date)}</small></td>
                  <td>{t(`ep.ag.st.${r.status}`)}{r.status === 'active' && !r.usable && <span className="mx-pill warn">{t('ep.ag.outOfPeriod')}</span>}</td>
                  <td className="num">{pct}%<br /><small>{amt(r.ordered_value)} / {amt(r.committed_value)}</small></td>
                </tr>);
            })}
            {rows.length === 0 && <tr><td colSpan={4} className="mx-empty">{t('ep.ag.none')}</td></tr>}
          </tbody>
        </table>
      </section>
      {sel && <AgreementDetail key={sel} id={sel} me={me} onChanged={reload} />}
    </div>
  );
}

function AgreementDetail({ id, me, onChanged }: { id: string; me: Me; onChanged: () => void }) {
  const { t } = useTranslation();
  const base = useBase();
  const [d, reload] = useLoad<AgDetail | null>(base && `${base}/purchasing/agreements/${id}`, null);
  const [msg, run] = useRun(() => { reload(); onChanged(); });
  const [qty, setQty] = useState<Record<string, string>>({});
  const [date, setDate] = useState(today());
  if (!d) return null;
  const a = d.agreement;
  const manage = can(me, 'purchasing.manage');
  const create = can(me, 'purchasing.create');
  const status = (s: string) => void run(api(`${base}/purchasing/agreements/${id}/status`, { method: 'POST', body: { status: s } }));
  return (
    <section className="mx-box" style={{ flex: '999 1 520px', minWidth: 0 }}>
      <h3>BA-{a.number} · {a.supplier_name} · {a.title} <small>({t(`ep.ag.st.${a.status}`)})</small></h3>
      {manage && (
        <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap' }}>
          {a.status === 'draft' && <button className="mx-btn main" onClick={() => status('active')}>{t('ep.ag.activate')}</button>}
          {a.status === 'active' && <button className="mx-btn" onClick={() => status('closed')}>{t('ep.ag.close')}</button>}
          {a.status === 'closed' && <button className="mx-btn" onClick={() => status('active')}>{t('ep.ag.reopen')}</button>}
          {(a.status === 'draft' || (a.status === 'active' && d.orders.length === 0)) && <button className="mx-btn danger" onClick={() => status('cancelled')}>{t('ep.td.cancel')}</button>}
        </div>)}
      <Flash m={msg} />
      <div style={{ overflowX: 'auto' }}>
        <table className="mx-table">
          <thead><tr><th>{t('ep.lay.desc')}</th><th className="num">{t('ep.price')}</th><th className="num">{t('ep.ag.committed')}</th><th className="num">{t('ep.ag.ordered')}</th><th className="num">{t('ep.ag.remaining')}</th>
            {a.status === 'active' && a.usable && create && <th className="num">{t('ep.ag.orderNow')}</th>}</tr></thead>
          <tbody>
            {d.lines.map((l) => (
              <tr key={l.id}>
                <td>{l.description}</td><td className="num">{amt(l.unit_price, 2)} {a.currency}</td><td className="num">{Number(l.quantity)}</td><td className="num">{Number(l.ordered)}</td>
                <td className="num"><b>{Number(l.remaining)}</b></td>
                {a.status === 'active' && a.usable && create && <td className="num"><input dir="ltr" style={{ width: 80 }} aria-label={t('ep.ag.orderNow')} value={qty[l.id] ?? ''}
                  onChange={(e) => setQty({ ...qty, [l.id]: num(e.currentTarget.value) })} /></td>}
              </tr>))}
          </tbody>
        </table>
      </div>
      {a.status === 'active' && a.usable && create && (
        <div className="mx-row" style={{ gap: 6, padding: 6, alignItems: 'end' }}>
          <label className="mx-fld">{t('ep.date')}<input type="date" dir="ltr" value={date} onChange={(e) => setDate(e.currentTarget.value)} /></label>
          <button className="mx-btn main" disabled={!Object.values(qty).some((q) => Number(q) > 0)} title={!Object.values(qty).some((q) => Number(q) > 0) ? t('ep.ag.needQty') : undefined}
            onClick={() => void run(api(`${base}/purchasing/agreements/${id}/order`, { method: 'POST', body: { order_date: date,
              lines: Object.entries(qty).filter(([, q]) => Number(q) > 0).map(([line, q]) => ({ agreement_line_id: line, quantity: q })) } }), () => setQty({}))}>{t('ep.ag.makeOrder')}</button>
        </div>)}
      {d.orders.length > 0 && <small style={{ display: 'block', padding: 6 }}>{t('ep.ag.orders')}: {d.orders.map((o) => `PO #${o.number} ${formatDate(o.order_date)} (${amt(o.total)})`).join(' · ')}</small>}
    </section>
  );
}
