import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, API_BASE, can, type Me } from '../../api';
import { formatAmount, formatDate, formatDateTime, today } from '../../util';
import { useBase, useLoad, useRun } from '../wh/Wh';
import Gantt, { type GBar, type GLoad, type GWarning } from '../../components/Gantt';
import { DocQR } from '../../components/DocLayout';

// Phase 9 (Magic-style screens): bank files and matching, card terminals, restaurant QR ordering, shipping carriers,
// timelines (projects, manufacturing, technicians).

type Msg = { ok: boolean; text: string } | null;
function Flash({ m }: { m: Msg }) {
  return m ? <div className={`mx-flash ${m.ok ? 'good' : 'bad'}`}>{m.text}</div> : null;
}
const amt = (v: string | number | null | undefined, d = 2) => formatAmount(String(v ?? 0), d);
const dec = (s: string) => s.replace(/[^\d.-]/g, '');

/** Opens a stored file (a carrier label) in a new tab. */
async function openFile(path: string) {
  const r = await fetch(API_BASE + path, { credentials: 'same-origin', headers: { 'X-Requested-With': 'paxve' } });
  if (!r.ok) return;
  const url = URL.createObjectURL(await r.blob());
  window.open(url, '_blank', 'noopener');
  window.setTimeout(() => URL.revokeObjectURL(url), 60000);
}

const PAGES = ['bank', 'terminals', 'qr', 'shipping', 'gantt-projects', 'gantt-mfg', 'gantt-field'] as const;
type Page = typeof PAGES[number];

export default function Ops({ me, initial }: { me: Me; initial?: string }) {
  const { t } = useTranslation();
  const page: Page = (PAGES as readonly string[]).includes(initial ?? '') ? initial as Page : 'bank';
  return (
    <div className="mx-col">
      <div className="mx-strip"><b>{t(`op.t.${page}`)}</b><span className="mx-sp" /><small style={{ opacity: 0.8 }}>{t(`op.help.${page}`)}</small></div>
      <div style={{ flex: 1, minHeight: 0, overflow: 'auto', padding: 6 }}>
        {page === 'bank' && <BankPro me={me} />}
        {page === 'terminals' && <Terminals me={me} />}
        {page === 'qr' && <QrOrdering me={me} />}
        {page === 'shipping' && <Shipping me={me} />}
        {page === 'gantt-projects' && <GanttProjects me={me} />}
        {page === 'gantt-mfg' && <GanttMfg me={me} />}
        {page === 'gantt-field' && <GanttField me={me} />}
      </div>
    </div>
  );
}

interface Method { id: string; name: string; kind: string; currency: string; is_active: boolean; account_id: string }
interface Acc { id: string; code: string; name: string; type: string; is_postable: boolean; is_active?: boolean }
interface Party { id: string; name: string; code?: string }

// ======================= bank =======================

interface PLine { date: string; description: string; amount: string; reference: string; partner: string; bank_id?: string }
interface Stmt { id: string; name: string; method: string; currency: string; created_at: string; lines: number; unmatched: number; date_from: string; date_to: string; total: string; format?: string; skipped?: number }
interface SLine {
  id: string; line_date: string; description: string; reference: string; partner: string; amount: string; status: string; rule: string | null;
  matched_number: number | null; matched_memo: string | null; payment_id: string | null; supplier_payment_id: string | null;
}
interface DocSug { kind: 'invoice' | 'bill'; id: string; number: number; party: string; date: string; currency: string; total: string; balance: string; confidence: number; why: string[] }
interface Rule {
  id: string; name: string; method_id: string | null; method: string | null; field: string; pattern: string; direction: string; amount_min: string | null; amount_max: string | null;
  action: string; account_code: string | null; account_name: string | null; customer_name: string | null; supplier_name: string | null; label: string; priority: number; is_active: boolean;
}

function BankPro({ me }: { me: Me }) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<'import' | 'statements' | 'rules'>('import');
  const [openSt, setOpenSt] = useState<string | null>(null);
  return (
    <div className="mx-col" style={{ gap: 6 }}>
      <div className="mx-modes" role="tablist">
        {(['import', 'statements', 'rules'] as const).map((m) => <button key={m} data-mode={m} role="tab" className="mx-mode" aria-selected={mode === m} onClick={() => setMode(m)}>{t(`op.bank.${m}`)}</button>)}
      </div>
      {mode === 'import' && <BankImport onDone={(id) => { setOpenSt(id); setMode('statements'); }} />}
      {mode === 'statements' && <Statements me={me} open={openSt} setOpen={setOpenSt} />}
      {mode === 'rules' && <Rules me={me} />}
    </div>
  );
}

function useBankMethods(): Method[] {
  const base = useBase();
  const [m] = useLoad<Method[]>(base && `${base}/payment-methods`, []);
  return m.filter((x) => x.is_active && (x.kind === 'bank' || x.kind === 'wallet'));
}

function BankImport({ onDone }: { onDone: (id: string) => void }) {
  const { t } = useTranslation();
  const base = useBase();
  const methods = useBankMethods();
  const [method, setMethod] = useState('');
  const [format, setFormat] = useState('csv');
  const [content, setContent] = useState('');
  const [fname, setFname] = useState('');
  const [map, setMap] = useState({ header: true, delimiter: '', date: '0', amount: '', debit: '', credit: '', description: '1', reference: '', partner: '', date_format: '', decimal: '.' });
  const [prev, setPrev] = useState<{ lines: PLine[]; currency: string; warnings: string[]; duplicates: number; total: string } | null>(null);
  const [msg, run, setMsg] = useRun();
  useEffect(() => { if (!method && methods[0]) setMethod(methods[0].id); }, [methods, method]);
  const header = useMemo(() => (format === 'csv' && content ? content.replace(/^﻿/, '').split(/\r?\n/)[0] : ''), [content, format]);
  function readFile(f: File) {
    setFname(f.name);
    const low = f.name.toLowerCase();
    if (low.endsWith('.ofx') || low.endsWith('.qfx')) setFormat('ofx');
    else if (low.endsWith('.sta') || low.endsWith('.mt940') || low.endsWith('.940')) setFormat('mt940');
    else if (low.endsWith('.xml')) setFormat('camt');
    else setFormat('csv');
    const r = new FileReader();
    r.onload = () => { setContent(String(r.result ?? '')); setPrev(null); };
    r.readAsText(f);
  }
  const preview = () => void run(api<typeof prev>(`${base}/bank/statements/parse`, { method: 'POST', body: { format, content, method_id: method, mapping: map } }), (x) => setPrev(x as typeof prev));
  const importIt = () => void run(api<{ statement: { id: string } }>(`${base}/bank/statements`, { method: 'POST', body: { method_id: method, name: fname || t('op.bank.import'), format, lines: prev?.lines ?? [] } }),
    (x) => { setPrev(null); setContent(''); onDone((x as { statement: { id: string } }).statement.id); });
  const fld = (k: keyof typeof map, w = 70) => (
    <label className="mx-fld">{t(`op.bank.m.${k}`)}<input dir="ltr" style={{ width: w }} value={String(map[k])} onChange={(e) => setMap({ ...map, [k]: e.currentTarget.value })} /></label>);
  return (
    <section className="mx-box">
      <h3>{t('op.bank.importTitle')}</h3>
      <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
        <label className="mx-fld">{t('op.bank.box')}<select value={method} onChange={(e) => setMethod(e.currentTarget.value)}>
          {methods.map((m) => <option key={m.id} value={m.id}>{m.name} ({m.currency})</option>)}</select></label>
        <label className="mx-fld">{t('op.bank.format')}<select value={format} onChange={(e) => { setFormat(e.currentTarget.value); setPrev(null); }}>
          {['csv', 'ofx', 'mt940', 'camt'].map((f) => <option key={f} value={f}>{t(`op.bank.f.${f}`)}</option>)}</select></label>
        <label className="mx-fld">{t('op.bank.file')}<input type="file" accept=".csv,.txt,.ofx,.qfx,.sta,.940,.mt940,.xml" onChange={(e) => { const f = e.currentTarget.files?.[0]; if (f) readFile(f); }} /></label>
      </div>
      {format === 'csv' && content && (
        <>
          <small style={{ display: 'block', padding: '0 6px' }}>{t('op.bank.csvHelp')} <code dir="ltr">{header.slice(0, 160)}</code></small>
          <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
            <label className="mx-fld">{t('op.bank.m.header')}<input type="checkbox" checked={map.header} onChange={(e) => setMap({ ...map, header: e.currentTarget.checked })} /></label>
            {fld('delimiter', 40)}{fld('date')}{fld('amount')}{fld('debit')}{fld('credit')}{fld('description', 110)}{fld('reference')}{fld('partner')}
            <label className="mx-fld">{t('op.bank.m.date_format')}<select value={map.date_format} onChange={(e) => setMap({ ...map, date_format: e.currentTarget.value })}>
              <option value="">{t('op.bank.autoDetect')}</option>{['YYYY-MM-DD', 'DD/MM/YYYY', 'MM/DD/YYYY', 'DD.MM.YYYY', 'DD-MM-YYYY', 'YYYYMMDD', 'DD/MM/YY'].map((f) => <option key={f}>{f}</option>)}</select></label>
            <label className="mx-fld">{t('op.bank.m.decimal')}<select value={map.decimal} onChange={(e) => setMap({ ...map, decimal: e.currentTarget.value })}><option value=".">1,234.50</option><option value=",">1.234,50</option></select></label>
          </div>
        </>)}
      <div className="mx-row" style={{ gap: 6, padding: 6 }}>
        <button className="mx-btn" disabled={!content || !method} title={!content ? t('op.bank.needFile') : undefined} onClick={preview}>{t('op.bank.preview')}</button>
        <button className="mx-btn main" disabled={!prev || prev.lines.length === 0} onClick={importIt}>{t('op.bank.importBtn', { n: prev?.lines.length ?? 0 })}</button>
        {prev && <button className="mx-btn" onClick={() => { setPrev(null); setMsg(null); }}>{t('common.cancel')}</button>}
      </div>
      <Flash m={msg} />
      {prev && (
        <>
          <div className="mx-row" style={{ gap: 12, padding: 6, flexWrap: 'wrap' }}>
            <span>{t('op.bank.lines')}: <b>{prev.lines.length}</b></span><span>{t('op.bank.total')}: <b dir="ltr">{amt(prev.total)}</b> {prev.currency}</span>
            {prev.duplicates > 0 && <span className="red">{t('op.bank.dups', { n: prev.duplicates })}</span>}
            {prev.warnings.map((w) => <small key={w} className="red">{w}</small>)}
          </div>
          <div style={{ overflowX: 'auto', maxHeight: 360 }}>
            <table className="mx-table">
              <thead><tr><th>{t('op.date')}</th><th>{t('op.bank.desc')}</th><th>{t('op.bank.partner')}</th><th>{t('op.bank.ref')}</th><th className="num">{t('op.amount')}</th></tr></thead>
              <tbody>{prev.lines.map((l, i) => (
                <tr key={i}><td dir="ltr">{l.date}</td><td><bdi>{l.description}</bdi></td><td><bdi>{l.partner}</bdi></td><td dir="ltr">{l.reference}</td>
                  <td className={`num ${Number(l.amount) < 0 ? 'red' : ''}`} dir="ltr">{amt(l.amount)}</td></tr>))}</tbody>
            </table>
          </div>
        </>)}
    </section>
  );
}

function Statements({ me, open, setOpen }: { me: Me; open: string | null; setOpen: (id: string | null) => void }) {
  const { t } = useTranslation();
  const base = useBase();
  const [list, reloadList] = useLoad<Stmt[]>(base && `${base}/bank/statements`, []);
  const [st, reload] = useLoad<{ statement: Stmt; lines: SLine[] } | null>(base && open && `${base}/bank/statements/${open}`, null);
  const [msg, run] = useRun(() => { reload(); reloadList(); });
  const [sel, setSel] = useState<string | null>(null);
  const [sug, setSug] = useState<{ documents: DocSug[]; rule: { name: string } | null } | null>(null);
  const [accs] = useLoad<Acc[]>(base && `${base}/accounts`, []);
  const [acc, setAcc] = useState({ id: '', label: '' });
  const [auto, setAuto] = useState<Record<string, number> | null>(null);
  const edit = can(me, 'bank.edit');
  useEffect(() => { setSug(null); if (base && sel) api<typeof sug>(`${base}/bank/lines/${sel}/suggestions`).then(setSug).catch(() => setSug(null)); }, [base, sel]);
  const line = st?.lines.find((l) => l.id === sel);
  const postable = accs.filter((a) => a.is_postable && a.is_active !== false && (a.type === 'expense' || a.type === 'income' || a.type === 'liability' || a.type === 'equity'));
  return (
    <div className="mx-row" style={{ gap: 8, alignItems: 'flex-start', flexWrap: 'wrap' }}>
      <section className="mx-box" style={{ flex: '1 1 300px', minWidth: 0 }}>
        <h3>{t('op.bank.statements')}</h3>
        <div style={{ overflowX: 'auto', maxHeight: 520 }}>
          <table className="mx-table">
            <thead><tr><th>{t('op.name')}</th><th>{t('op.bank.box')}</th><th className="num">{t('op.bank.lines')}</th><th className="num">{t('op.bank.open')}</th></tr></thead>
            <tbody>{list.map((s) => (
              <tr key={s.id} className={open === s.id ? 'sel' : ''} style={{ cursor: 'pointer' }} onClick={() => { setOpen(s.id); setSel(null); setAuto(null); }}>
                <td><bdi>{s.name || '—'}</bdi><br /><small dir="ltr">{s.date_from} → {s.date_to}</small></td><td>{s.method}</td><td className="num">{s.lines}</td>
                <td className={`num ${s.unmatched > 0 ? 'red' : ''}`}>{s.unmatched}</td></tr>))}
              {list.length === 0 && <tr><td colSpan={4} className="mx-empty">{t('op.bank.noStatements')}</td></tr>}
            </tbody>
          </table>
        </div>
      </section>
      {st && (
        <section className="mx-box" style={{ flex: '999 1 560px', minWidth: 0 }}>
          <h3 style={{ display: 'flex', gap: 6, alignItems: 'center', flexWrap: 'wrap' }}>
            <bdi>{st.statement.name}</bdi> · {st.statement.method}
            {Number(st.statement.skipped ?? 0) > 0 && <small>({t('op.bank.skipped', { n: st.statement.skipped })})</small>}
            <span className="mx-sp" />
            {edit && <button className="mx-btn main" data-act="auto" onClick={() => void run(api<{ result: Record<string, number> }>(`${base}/bank/statements/${st.statement.id}/auto-reconcile`, { method: 'POST', body: {} }),
              (x) => setAuto((x as { result: Record<string, number> }).result))}>{t('op.bank.auto')}</button>}
          </h3>
          {auto && <div className="mx-flash good">{t('op.bank.autoResult', auto)}</div>}
          <Flash m={msg} />
          <div style={{ overflowX: 'auto', maxHeight: 420 }}>
            <table className="mx-table">
              <thead><tr><th>{t('op.date')}</th><th>{t('op.bank.desc')}</th><th>{t('op.bank.ref')}</th><th className="num">{t('op.amount')}</th><th>{t('op.status')}</th></tr></thead>
              <tbody>{st.lines.map((l) => (
                <tr key={l.id} className={sel === l.id ? 'sel' : ''} style={{ cursor: 'pointer' }} onClick={() => setSel(l.id)}>
                  <td dir="ltr">{l.line_date}</td><td><bdi>{l.description}</bdi>{l.partner && <><br /><small><bdi>{l.partner}</bdi></small></>}</td><td dir="ltr">{l.reference}</td>
                  <td className={`num ${Number(l.amount) < 0 ? 'red' : ''}`} dir="ltr">{amt(l.amount)}</td>
                  <td><span className={`mx-pill ${l.status === 'matched' ? 'on' : l.status === 'unmatched' ? 'warn' : ''}`}>{t(`op.bank.s.${l.status}`)}</span>
                    {l.matched_number && <small> #{l.matched_number}</small>}{l.rule && <small> · {l.rule}</small>}</td>
                </tr>))}</tbody>
            </table>
          </div>
          {line && line.status === 'unmatched' && edit && (
            <div className="mx-box" style={{ marginTop: 6 }}>
              <h3>{t('op.bank.lineTitle', { amount: amt(line.amount), date: line.line_date })}</h3>
              {sug?.rule && <div className="mx-flash good">{t('op.bank.ruleFits', { name: sug.rule.name })}</div>}
              <table className="mx-table">
                <thead><tr><th>{t('op.bank.doc')}</th><th>{t('op.bank.partner')}</th><th className="num">{t('op.bank.balance')}</th><th className="num">{t('op.bank.conf')}</th><th /></tr></thead>
                <tbody>
                  {(sug?.documents ?? []).map((d) => (
                    <tr key={d.id}>
                      <td>{t(`op.bank.k.${d.kind}`)} #{d.number}<br /><small dir="ltr">{formatDate(d.date)}</small></td><td><bdi>{d.party}</bdi></td>
                      <td className="num" dir="ltr">{amt(d.balance)} {d.currency}</td>
                      <td className="num"><b>{d.confidence}%</b><br /><small>{d.why.map((w) => t(`op.bank.why.${w}`)).join(' · ')}</small></td>
                      <td><button className="mx-btn sm main" onClick={() => void run(api(`${base}/bank/lines/${line.id}/reconcile`, { method: 'POST', body: { kind: d.kind, id: d.id } }))}>{t('op.bank.reconcile')}</button></td>
                    </tr>))}
                  {sug && sug.documents.length === 0 && <tr><td colSpan={5} className="mx-empty">{t('op.bank.noDocs')}</td></tr>}
                </tbody>
              </table>
              <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
                <label className="mx-fld">{t('op.bank.bookTo')}<select value={acc.id} onChange={(e) => setAcc({ ...acc, id: e.currentTarget.value })}>
                  <option value="">—</option>{postable.map((a) => <option key={a.id} value={a.id}>{a.code} · {a.name}</option>)}</select></label>
                <label className="mx-fld">{t('op.bank.label')}<input value={acc.label} onChange={(e) => setAcc({ ...acc, label: e.currentTarget.value })} /></label>
                <button className="mx-btn" disabled={!acc.id} title={!acc.id ? t('op.bank.needAccount') : undefined}
                  onClick={() => void run(api(`${base}/bank/lines/${line.id}/reconcile`, { method: 'POST', body: { kind: 'account', id: acc.id, label: acc.label } }), () => setAcc({ id: '', label: '' }))}>{t('op.bank.book')}</button>
                <button className="mx-btn" onClick={() => void run(api(`${base}/bank/lines/${line.id}/ignore`, { method: 'POST', body: {} }))}>{t('op.bank.ignore')}</button>
              </div>
            </div>)}
          {line && line.status !== 'unmatched' && edit && (
            <div className="mx-row" style={{ gap: 6, padding: 6 }}>
              {line.status === 'matched' && !line.payment_id && !line.supplier_payment_id && <button className="mx-btn" onClick={() => void run(api(`${base}/bank/lines/${line.id}/unmatch`, { method: 'POST', body: {} }))}>{t('op.bank.unmatch')}</button>}
              {line.status === 'ignored' && <button className="mx-btn" onClick={() => void run(api(`${base}/bank/lines/${line.id}/unignore`, { method: 'POST', body: {} }))}>{t('op.bank.unignore')}</button>}
              {(line.payment_id || line.supplier_payment_id) && <small>{t('op.bank.paidHere')}</small>}
            </div>)}
        </section>)}
    </div>
  );
}

function Rules({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [rows, reload] = useLoad<Rule[]>(base && `${base}/bank/match-rules`, []);
  const [accs] = useLoad<Acc[]>(base && `${base}/accounts`, []);
  const [customers] = useLoad<Party[]>(base && `${base}/customers?limit=200`, []);
  const [sups] = useLoad<Party[]>(base && `${base}/suppliers`, []);
  const methods = useBankMethods();
  const [msg, run] = useRun(reload);
  const blank = { name: '', pattern: '', field: 'any', direction: 'any', amount_min: '', amount_max: '', action: 'account', account_id: '', customer_id: '', supplier_id: '', label: '', method_id: '', priority: '100' };
  const [f, setF] = useState(blank);
  const edit = can(me, 'bank.edit');
  const target = f.action === 'account' ? f.account_id : f.action === 'customer' ? f.customer_id : f.action === 'supplier' ? f.supplier_id : 'x';
  const save = () => void run(api(`${base}/bank/match-rules`, { method: 'POST', body: { name: f.name, pattern: f.pattern, field: f.field, direction: f.direction,
    amount_min: f.amount_min, amount_max: f.amount_max, action: f.action, account_id: f.action === 'account' ? f.account_id : '', customer_id: f.action === 'customer' ? f.customer_id : '',
    supplier_id: f.action === 'supplier' ? f.supplier_id : '', label: f.label, method_id: f.method_id, priority: Number(f.priority) || 100 } }), () => setF(blank));
  return (
    <section className="mx-box">
      <h3>{t('op.bank.rules')}</h3>
      <small style={{ display: 'block', padding: '0 6px' }}>{t('op.bank.rulesHelp')}</small>
      {edit && (
        <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
          <label className="mx-fld">{t('op.name')}<input value={f.name} onChange={(e) => setF({ ...f, name: e.currentTarget.value })} /></label>
          <label className="mx-fld">{t('op.bank.r.pattern')}<input dir="ltr" style={{ width: 130 }} value={f.pattern} placeholder="FEE|COMMISSION" onChange={(e) => setF({ ...f, pattern: e.currentTarget.value })} /></label>
          <label className="mx-fld">{t('op.bank.r.field')}<select value={f.field} onChange={(e) => setF({ ...f, field: e.currentTarget.value })}>
            {['any', 'description', 'reference', 'partner'].map((x) => <option key={x} value={x}>{t(`op.bank.r.f.${x}`)}</option>)}</select></label>
          <label className="mx-fld">{t('op.bank.r.direction')}<select value={f.direction} onChange={(e) => setF({ ...f, direction: e.currentTarget.value })}>
            {['any', 'in', 'out'].map((x) => <option key={x} value={x}>{t(`op.bank.r.d.${x}`)}</option>)}</select></label>
          <label className="mx-fld">{t('op.bank.r.min')}<input dir="ltr" style={{ width: 70 }} value={f.amount_min} onChange={(e) => setF({ ...f, amount_min: dec(e.currentTarget.value) })} /></label>
          <label className="mx-fld">{t('op.bank.r.max')}<input dir="ltr" style={{ width: 70 }} value={f.amount_max} onChange={(e) => setF({ ...f, amount_max: dec(e.currentTarget.value) })} /></label>
          <label className="mx-fld">{t('op.bank.r.action')}<select value={f.action} onChange={(e) => setF({ ...f, action: e.currentTarget.value })}>
            {['account', 'customer', 'supplier', 'ignore'].map((x) => <option key={x} value={x}>{t(`op.bank.r.a.${x}`)}</option>)}</select></label>
          {f.action === 'account' && <label className="mx-fld">{t('op.bank.r.account')}<select value={f.account_id} onChange={(e) => setF({ ...f, account_id: e.currentTarget.value })}>
            <option value="">—</option>{accs.filter((a) => a.is_postable && a.is_active !== false).map((a) => <option key={a.id} value={a.id}>{a.code} · {a.name}</option>)}</select></label>}
          {f.action === 'customer' && <label className="mx-fld">{t('op.bank.r.customer')}<select value={f.customer_id} onChange={(e) => setF({ ...f, customer_id: e.currentTarget.value })}>
            <option value="">—</option>{customers.map((c) => <option key={c.id} value={c.id}>{c.code ? `${c.code} · ` : ''}{c.name}</option>)}</select></label>}
          {f.action === 'supplier' && <label className="mx-fld">{t('op.bank.r.supplier')}<select value={f.supplier_id} onChange={(e) => setF({ ...f, supplier_id: e.currentTarget.value })}>
            <option value="">—</option>{sups.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}</select></label>}
          <label className="mx-fld">{t('op.bank.label')}<input value={f.label} onChange={(e) => setF({ ...f, label: e.currentTarget.value })} /></label>
          <label className="mx-fld">{t('op.bank.box')}<select value={f.method_id} onChange={(e) => setF({ ...f, method_id: e.currentTarget.value })}>
            <option value="">{t('op.bank.allBoxes')}</option>{methods.map((m) => <option key={m.id} value={m.id}>{m.name}</option>)}</select></label>
          <label className="mx-fld">{t('op.bank.r.priority')}<input dir="ltr" style={{ width: 50 }} value={f.priority} onChange={(e) => setF({ ...f, priority: e.currentTarget.value.replace(/\D/g, '') })} /></label>
          <button className="mx-btn main" disabled={!f.name.trim() || !target} title={!f.name.trim() || !target ? t('op.bank.r.need') : undefined} onClick={save}>{t('op.add')}</button>
        </div>)}
      <Flash m={msg} />
      <div style={{ overflowX: 'auto' }}>
        <table className="mx-table">
          <thead><tr><th className="num">#</th><th>{t('op.name')}</th><th>{t('op.bank.r.when')}</th><th>{t('op.bank.r.then')}</th><th>{t('op.status')}</th><th /></tr></thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.id} style={{ opacity: r.is_active ? 1 : 0.55 }}>
                <td className="num">{r.priority}</td><td><bdi>{r.name}</bdi>{r.method && <><br /><small>{r.method}</small></>}</td>
                <td><small>{t(`op.bank.r.d.${r.direction}`)} · {t(`op.bank.r.f.${r.field}`)} <code dir="ltr">{r.pattern || '*'}</code>
                  {(r.amount_min || r.amount_max) && <> · <span dir="ltr">{r.amount_min ?? '0'} – {r.amount_max ?? '∞'}</span></>}</small></td>
                <td>{t(`op.bank.r.a.${r.action}`)} <bdi>{r.account_code ? `${r.account_code} ${r.account_name}` : r.customer_name ?? r.supplier_name ?? ''}</bdi></td>
                <td>{edit ? <button className="mx-btn sm" onClick={() => void run(api(`${base}/bank/match-rules/${r.id}`, { method: 'PATCH', body: { is_active: !r.is_active } }))}>{r.is_active ? t('op.active') : t('op.inactive')}</button>
                  : (r.is_active ? t('op.active') : t('op.inactive'))}</td>
                <td>{edit && <button className="mx-btn sm" style={{ color: '#b42318' }} onClick={() => { if (window.confirm(t('op.deleteQ'))) void run(api(`${base}/bank/match-rules/${r.id}`, { method: 'DELETE' })); }}>{t('op.delete')}</button>}</td>
              </tr>))}
            {rows.length === 0 && <tr><td colSpan={6} className="mx-empty">{t('op.bank.noRules')}</td></tr>}
          </tbody>
        </table>
      </div>
    </section>
  );
}

// ======================= card terminals =======================

interface Terminal { id: string; name: string; currency: string; terminal_driver: string; terminal_ref: string; fee_pct: string; fee_fixed: string; settle_method_id: string | null; settle_method: string | null; is_active: boolean; open_txns: number; open_amount: string }
interface Txn { id: string; method: string; currency: string; amount: string; status: string; approval_code: string; rrn: string; card_brand: string; card_last4: string; message: string; pos_order_number: number | null; settlement_number: number | null; created_at: string; created_by_name: string | null }
interface Settle { id: string; number: number; method: string; bank_method: string; settle_date: string; currency: string; gross: string; fee: string; net: string; txns: number; batch_ref: string; status: string }

function Terminals({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [rows, reload] = useLoad<Terminal[]>(base && `${base}/pos/terminals`, []);
  const [state, setState] = useState('open');
  const [txns, reloadTx] = useLoad<Txn[]>(base && `${base}/pos/terminal/txns?state=${state}`, []);
  const [sets, reloadSets] = useLoad<Settle[]>(base && can(me, 'pos.view') && `${base}/pos/terminal/settlements`, []);
  const [methods] = useLoad<Method[]>(base && `${base}/payment-methods`, []);
  const [msg, run] = useRun(() => { reload(); reloadTx(); reloadSets(); });
  const [ed, setEd] = useState<Record<string, { driver: string; terminal_ref: string; fee_pct: string; fee_fixed: string; settle_method_id: string }>>({});
  const [sf, setSf] = useState({ method_id: '', date: today(), fee: '', batch_ref: '' });
  const manage = can(me, 'pos.manage');
  const banks = methods.filter((m) => m.is_active && m.kind !== 'card' && m.kind !== 'cheque');
  const row = (r: Terminal) => ed[r.id] ?? { driver: r.terminal_driver, terminal_ref: r.terminal_ref, fee_pct: r.fee_pct, fee_fixed: r.fee_fixed, settle_method_id: r.settle_method_id ?? '' };
  const set = (r: Terminal, k: string, v: string) => setEd({ ...ed, [r.id]: { ...row(r), [k]: v } });
  return (
    <div className="mx-col" style={{ gap: 8 }}>
      <section className="mx-box">
        <h3>{t('op.card.boxes')}</h3>
        <small style={{ display: 'block', padding: '0 6px' }}>{t('op.card.help')}</small>
        <Flash m={msg} />
        <div style={{ overflowX: 'auto' }}>
          <table className="mx-table">
            <thead><tr><th>{t('op.card.box')}</th><th>{t('op.card.driver')}</th><th>{t('op.card.ref')}</th><th className="num">{t('op.card.feePct')}</th><th className="num">{t('op.card.feeFixed')}</th>
              <th>{t('op.card.settleTo')}</th><th className="num">{t('op.card.waiting')}</th><th /></tr></thead>
            <tbody>
              {rows.map((r) => { const v = row(r); return (
                <tr key={r.id}>
                  <td><b>{r.name}</b> <small>{r.currency}</small></td>
                  <td><select disabled={!manage} value={v.driver} onChange={(e) => set(r, 'driver', e.currentTarget.value)}>
                    {['', 'simulator', 'manual'].map((d) => <option key={d} value={d}>{t(`op.card.d.${d || 'none'}`)}</option>)}</select></td>
                  <td><input disabled={!manage} dir="ltr" style={{ width: 90 }} value={v.terminal_ref} onChange={(e) => set(r, 'terminal_ref', e.currentTarget.value)} /></td>
                  <td className="num"><input disabled={!manage} dir="ltr" style={{ width: 55 }} value={v.fee_pct} onChange={(e) => set(r, 'fee_pct', dec(e.currentTarget.value))} /></td>
                  <td className="num"><input disabled={!manage} dir="ltr" style={{ width: 60 }} value={v.fee_fixed} onChange={(e) => set(r, 'fee_fixed', dec(e.currentTarget.value))} /></td>
                  <td><select disabled={!manage} value={v.settle_method_id} onChange={(e) => set(r, 'settle_method_id', e.currentTarget.value)}>
                    <option value="">—</option>{banks.filter((b) => b.currency === r.currency).map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}</select></td>
                  <td className="num">{r.open_txns} · <span dir="ltr">{amt(r.open_amount)}</span></td>
                  <td>{manage && <button className="mx-btn sm main" disabled={!ed[r.id]} onClick={() => void run(api(`${base}/pos/terminals/${r.id}`, { method: 'PUT', body: v }), () => { const n = { ...ed }; delete n[r.id]; setEd(n); })}>{t('op.save')}</button>}</td>
                </tr>); })}
              {rows.length === 0 && <tr><td colSpan={8} className="mx-empty">{t('op.card.noBoxes')}</td></tr>}
            </tbody>
          </table>
        </div>
      </section>
      <div className="mx-row" style={{ gap: 8, alignItems: 'flex-start', flexWrap: 'wrap' }}>
        <section className="mx-box" style={{ flex: '999 1 520px', minWidth: 0 }}>
          <h3 style={{ display: 'flex', gap: 6, alignItems: 'center' }}>{t('op.card.charges')}<span className="mx-sp" />
            <select value={state} onChange={(e) => setState(e.currentTarget.value)}>{['open', 'unused', 'settled', 'all'].map((s) => <option key={s} value={s}>{t(`op.card.st.${s}`)}</option>)}</select></h3>
          <div style={{ overflowX: 'auto', maxHeight: 380 }}>
            <table className="mx-table">
              <thead><tr><th>{t('op.date')}</th><th>{t('op.card.box')}</th><th className="num">{t('op.amount')}</th><th>{t('op.card.approval')}</th><th>{t('op.card.card')}</th><th>{t('op.card.sale')}</th><th /></tr></thead>
              <tbody>{txns.map((x) => (
                <tr key={x.id} style={{ opacity: x.status === 'approved' ? 1 : 0.55 }}>
                  <td dir="ltr">{formatDateTime(x.created_at)}</td><td>{x.method}</td><td className="num" dir="ltr">{amt(x.amount)} {x.currency}</td>
                  <td dir="ltr">{x.approval_code || <span className="red">{t(`op.card.s.${x.status}`)}</span>}</td><td dir="ltr">{x.card_brand} {x.card_last4 && `•••• ${x.card_last4}`}</td>
                  <td>{x.pos_order_number ? `#${x.pos_order_number}` : '—'}{x.settlement_number && <small> · {t('op.card.batch')} {x.settlement_number}</small>}</td>
                  <td>{x.status === 'approved' && !x.pos_order_number && <button className="mx-btn sm" onClick={() => void run(api(`${base}/pos/terminal/txns/${x.id}/void`, { method: 'POST', body: {} }))}>{t('op.card.void')}</button>}</td>
                </tr>))}
                {txns.length === 0 && <tr><td colSpan={7} className="mx-empty">{t('op.card.noCharges')}</td></tr>}</tbody>
            </table>
          </div>
        </section>
        <section className="mx-box" style={{ flex: '1 1 380px', minWidth: 0 }}>
          <h3>{t('op.card.settlements')}</h3>
          {manage && (
            <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
              <label className="mx-fld">{t('op.card.box')}<select value={sf.method_id} onChange={(e) => setSf({ ...sf, method_id: e.currentTarget.value })}>
                <option value="">—</option>{rows.map((r) => <option key={r.id} value={r.id}>{r.name}</option>)}</select></label>
              <label className="mx-fld">{t('op.date')}<input type="date" dir="ltr" value={sf.date} onChange={(e) => setSf({ ...sf, date: e.currentTarget.value })} /></label>
              <label className="mx-fld">{t('op.card.feeOverride')}<input dir="ltr" style={{ width: 70 }} value={sf.fee} onChange={(e) => setSf({ ...sf, fee: dec(e.currentTarget.value) })} /></label>
              <label className="mx-fld">{t('op.card.batchRef')}<input dir="ltr" style={{ width: 80 }} value={sf.batch_ref} onChange={(e) => setSf({ ...sf, batch_ref: e.currentTarget.value })} /></label>
              <button className="mx-btn main" disabled={!sf.method_id} title={!sf.method_id ? t('op.card.needBox') : undefined}
                onClick={() => void run(api(`${base}/pos/terminal/settlements`, { method: 'POST', body: sf }), () => setSf({ ...sf, fee: '', batch_ref: '' }))}>{t('op.card.settle')}</button>
            </div>)}
          <div style={{ overflowX: 'auto' }}>
            <table className="mx-table">
              <thead><tr><th>#</th><th>{t('op.date')}</th><th className="num">{t('op.card.gross')}</th><th className="num">{t('op.card.fee')}</th><th className="num">{t('op.card.net')}</th><th /></tr></thead>
              <tbody>{sets.map((s) => (
                <tr key={s.id} style={{ opacity: s.status === 'void' ? 0.5 : 1 }}>
                  <td>{s.number}<br /><small>{s.method} → {s.bank_method}</small></td><td dir="ltr">{formatDate(s.settle_date)}</td>
                  <td className="num" dir="ltr">{amt(s.gross)}</td><td className="num red" dir="ltr">{amt(s.fee)}</td><td className="num" dir="ltr"><b>{amt(s.net)}</b> {s.currency}</td>
                  <td>{s.status === 'posted' && can(me, 'pos.void') && <button className="mx-btn sm" onClick={() => { if (window.confirm(t('op.card.voidQ'))) void run(api(`${base}/pos/terminal/settlements/${s.id}/void`, { method: 'POST', body: {} })); }}>{t('op.card.void')}</button>}
                    {s.status === 'void' && <small>{t('op.card.s.voided')}</small>}</td>
                </tr>))}
                {sets.length === 0 && <tr><td colSpan={6} className="mx-empty">{t('op.card.noSettlements')}</td></tr>}</tbody>
            </table>
          </div>
        </section>
      </div>
    </div>
  );
}

// ======================= QR ordering =======================

interface QrSet { settings: { enabled: boolean; register_id: string | null; register: string | null }; tables: { id: string; name: string; area: string; url: string | null }[] }
interface QrOrder { id: string; number: number; table_name: string; area: string; name: string; note: string; lines: { name: string; quantity: string; price: string; note: string }[]; total: string; currency: string; status: string; check_number: number | null; created_at: string; decided_by_name: string | null }

function QrOrdering({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [qs, reload] = useLoad<QrSet | null>(base && `${base}/rest/qr`, null);
  const [regs] = useLoad<{ id: string; name: string; currency: string }[]>(base && `${base}/pos/registers`, []);
  const [status, setStatus] = useState('pending');
  const [orders, reloadO] = useLoad<QrOrder[]>(base && `${base}/rest/qr/orders?status=${status}`, []);
  const [msg, run] = useRun(() => { reload(); reloadO(); });
  const [reg, setReg] = useState('');
  const [print, setPrint] = useState(false);
  useEffect(() => { if (qs?.settings.register_id) setReg(qs.settings.register_id); }, [qs]);
  useEffect(() => { const h = window.setInterval(reloadO, 15000); return () => window.clearInterval(h); }, [reloadO]);
  const manage = can(me, 'pos.manage');
  if (!qs) return null;
  return (
    <div className="mx-col" style={{ gap: 8 }}>
      <section className="mx-box no-print">
        <h3>{t('op.qr.settings')}</h3>
        <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
          <label className="mx-fld">{t('op.qr.register')}<select disabled={!manage} value={reg} onChange={(e) => setReg(e.currentTarget.value)}>
            <option value="">—</option>{regs.map((r) => <option key={r.id} value={r.id}>{r.name} ({r.currency})</option>)}</select></label>
          {manage && <button className={`mx-btn ${qs.settings.enabled ? '' : 'main'}`} disabled={!qs.settings.enabled && !reg} title={!reg ? t('op.qr.needRegister') : undefined}
            onClick={() => void run(api(`${base}/rest/qr`, { method: 'PUT', body: { enabled: !qs.settings.enabled, register_id: reg } }))}>{qs.settings.enabled ? t('op.qr.disable') : t('op.qr.enable')}</button>}
          {manage && qs.settings.enabled && reg !== (qs.settings.register_id ?? '') && <button className="mx-btn" onClick={() => void run(api(`${base}/rest/qr`, { method: 'PUT', body: { enabled: true, register_id: reg } }))}>{t('op.save')}</button>}
          <span className={`mx-pill ${qs.settings.enabled ? 'on' : ''}`}>{qs.settings.enabled ? t('op.qr.on') : t('op.qr.off')}</span>
          {qs.settings.enabled && <button className="mx-btn" onClick={() => { setPrint(true); window.setTimeout(() => { window.print(); setPrint(false); }, 200); }}>{t('op.qr.print')}</button>}
        </div>
        <Flash m={msg} />
      </section>
      {qs.settings.enabled && (
        <section className={`mx-box ${print ? '' : ''}`}>
          <h3 className="no-print">{t('op.qr.tables')}</h3>
          <div className="mx-row print-doc" style={{ gap: 10, padding: 6, flexWrap: 'wrap' }}>
            {qs.tables.map((tb) => (
              <div key={tb.id} style={{ border: '1px solid #cfd6e0', borderRadius: 6, padding: 8, width: 170, textAlign: 'center', breakInside: 'avoid' }}>
                <b><bdi>{tb.name}</bdi></b> <small>{tb.area}</small>
                {tb.url ? <div style={{ display: 'flex', justifyContent: 'center', margin: '6px 0' }}><DocQR text={tb.url} size={120} /></div> : <div className="mx-empty">—</div>}
                <small>{t('op.qr.scan')}</small>
                {manage && <div className="no-print"><button className="mx-btn sm" onClick={() => { if (window.confirm(t('op.qr.newLinkQ'))) void run(api(`${base}/rest/tables/${tb.id}/qr`, { method: 'POST', body: {} })); }}>{t('op.qr.newLink')}</button>
                  {tb.url && <a className="mx-link" href={tb.url} target="_blank" rel="noopener noreferrer"> {t('op.qr.open')}</a>}</div>}
              </div>))}
          </div>
        </section>)}
      <section className="mx-box no-print">
        <h3 style={{ display: 'flex', gap: 6, alignItems: 'center' }}>{t('op.qr.orders')}<span className="mx-sp" />
          <select value={status} onChange={(e) => setStatus(e.currentTarget.value)}>{['pending', 'accepted', 'rejected', 'all'].map((s) => <option key={s} value={s}>{t(`op.qr.s.${s}`)}</option>)}</select></h3>
        <div style={{ overflowX: 'auto' }}>
          <table className="mx-table">
            <thead><tr><th>#</th><th>{t('op.qr.table')}</th><th>{t('op.qr.items')}</th><th className="num">{t('op.total')}</th><th>{t('op.status')}</th><th /></tr></thead>
            <tbody>{orders.map((o) => (
              <tr key={o.id}>
                <td>{o.number}<br /><small dir="ltr">{formatDateTime(o.created_at)}</small></td><td><b><bdi>{o.table_name}</bdi></b> <small>{o.area}</small>{o.name && <><br /><small>👤 <bdi>{o.name}</bdi></small></>}</td>
                <td>{o.lines.map((l, i) => <div key={i}><b dir="ltr">{Number(l.quantity)}×</b> <bdi>{l.name}</bdi>{l.note && <small> · <bdi>{l.note}</bdi></small>}</div>)}{o.note && <small>📝 <bdi>{o.note}</bdi></small>}</td>
                <td className="num" dir="ltr">{amt(o.total)} {o.currency}</td>
                <td><span className={`mx-pill ${o.status === 'accepted' ? 'on' : o.status === 'pending' ? 'warn' : ''}`}>{t(`op.qr.s.${o.status}`)}</span>{o.check_number && <small> · {t('op.qr.check')} #{o.check_number}</small>}</td>
                <td>{o.status === 'pending' && can(me, 'pos.create') && <span style={{ display: 'flex', gap: 4 }}>
                  <button className="mx-btn sm main" onClick={() => void run(api(`${base}/rest/qr/orders/${o.id}/accept`, { method: 'POST', body: {} }))}>{t('op.qr.accept')}</button>
                  <button className="mx-btn sm" onClick={() => void run(api(`${base}/rest/qr/orders/${o.id}/reject`, { method: 'POST', body: {} }))}>{t('op.qr.reject')}</button></span>}</td>
              </tr>))}
              {orders.length === 0 && <tr><td colSpan={6} className="mx-empty">{t('op.qr.none')}</td></tr>}</tbody>
          </table>
        </div>
      </section>
    </div>
  );
}

// ======================= shipping =======================

interface Carrier { id: string; carrier: string; name: string; config: Record<string, unknown>; is_active: boolean; is_default: boolean; configured: boolean }
interface Shipment {
  id: string; number: number; account: string; carrier: string; order_number: number | null; shop_order_number: number | null; recipient: string; phone: string; address: string; city: string; country: string;
  weight_kg: string; pieces: number; currency: string; cod_amount: string; rate: string | null; tracking_no: string; label_id: string | null; status: string;
  events: { at: string; status: string; text: string; location?: string }[]; error: string; created_at: string;
}
const CFG: Record<string, string[]> = {
  manual: ['flat_rate', 'per_kg', 'currency', 'days', 'prefix', 'from_city', 'from_address', 'from_phone'],
  aramex: ['username', 'password', 'account_number', 'account_pin', 'account_entity', 'from_city', 'from_address', 'from_country', 'from_phone', 'sandbox'],
  dhl: ['api_key', 'secret', 'account_number', 'from_city', 'from_address', 'from_country', 'from_phone', 'sandbox'],
};

function Shipping({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [carriers, reloadC] = useLoad<Carrier[]>(base && `${base}/ship/carriers`, []);
  const [status, setStatus] = useState('open');
  const [rows, reload] = useLoad<Shipment[]>(base && `${base}/ship/shipments?status=${status}`, []);
  const [msg, run] = useRun(() => { reload(); reloadC(); });
  const [nc, setNc] = useState<{ carrier: string; name: string; config: Record<string, string> }>({ carrier: 'manual', name: '', config: {} });
  const [sel, setSel] = useState<string | null>(null);
  const manage = can(me, 'stock.manage');
  const sh = rows.find((r) => r.id === sel);
  return (
    <div className="mx-col" style={{ gap: 8 }}>
      <section className="mx-box">
        <h3>{t('op.ship.carriers')}</h3>
        <div style={{ overflowX: 'auto' }}>
          <table className="mx-table">
            <thead><tr><th>{t('op.name')}</th><th>{t('op.ship.carrier')}</th><th>{t('op.ship.ready')}</th><th>{t('op.status')}</th></tr></thead>
            <tbody>{carriers.map((c) => (
              <tr key={c.id} style={{ opacity: c.is_active ? 1 : 0.55 }}>
                <td><b><bdi>{c.name}</bdi></b>{c.is_default && <small> ★</small>}</td><td>{t(`op.ship.c.${c.carrier}`)}</td>
                <td>{c.configured ? <span className="mx-pill on">{t('op.ship.configured')}</span> : <span className="mx-pill warn">{t('op.ship.needKeys')}</span>}</td>
                <td>{manage ? <button className="mx-btn sm" onClick={() => void run(api(`${base}/ship/carriers/${c.id}`, { method: 'PATCH', body: { is_active: !c.is_active } }))}>{c.is_active ? t('op.active') : t('op.inactive')}</button> : (c.is_active ? t('op.active') : t('op.inactive'))}</td>
              </tr>))}
              {carriers.length === 0 && <tr><td colSpan={4} className="mx-empty">{t('op.ship.noCarriers')}</td></tr>}</tbody>
          </table>
        </div>
        {manage && (
          <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
            <label className="mx-fld">{t('op.ship.carrier')}<select value={nc.carrier} onChange={(e) => setNc({ carrier: e.currentTarget.value, name: nc.name, config: {} })}>
              {['manual', 'aramex', 'dhl'].map((c) => <option key={c} value={c}>{t(`op.ship.c.${c}`)}</option>)}</select></label>
            <label className="mx-fld">{t('op.name')}<input value={nc.name} onChange={(e) => setNc({ ...nc, name: e.currentTarget.value })} /></label>
            {CFG[nc.carrier].map((k) => (
              <label key={k} className="mx-fld">{t(`op.ship.k.${k}`)}{k === 'sandbox'
                ? <input type="checkbox" checked={nc.config[k] === 'true'} onChange={(e) => setNc({ ...nc, config: { ...nc.config, [k]: e.currentTarget.checked ? 'true' : '' } })} />
                : <input dir="ltr" style={{ width: k.startsWith('from_a') ? 150 : 100 }} type={k === 'password' || k === 'secret' || k === 'api_key' || k === 'account_pin' ? 'password' : 'text'}
                  value={nc.config[k] ?? ''} onChange={(e) => setNc({ ...nc, config: { ...nc.config, [k]: e.currentTarget.value } })} />}</label>))}
            <button className="mx-btn main" disabled={!nc.name.trim()} title={!nc.name.trim() ? t('op.ship.needName') : undefined}
              onClick={() => void run(api(`${base}/ship/carriers`, { method: 'POST', body: { carrier: nc.carrier, name: nc.name, config: Object.fromEntries(Object.entries(nc.config).filter(([, v]) => v !== '')) } }),
                () => setNc({ carrier: 'manual', name: '', config: {} }))}>{t('op.add')}</button>
          </div>)}
        <small style={{ display: 'block', padding: 6 }}>{t('op.ship.carrierHelp')}</small>
      </section>
      <ShipForm onDone={reload} />
      <Flash m={msg} />
      <div className="mx-row" style={{ gap: 8, alignItems: 'flex-start', flexWrap: 'wrap' }}>
        <section className="mx-box" style={{ flex: '999 1 560px', minWidth: 0 }}>
          <h3 style={{ display: 'flex', gap: 6, alignItems: 'center' }}>{t('op.ship.shipments')}<span className="mx-sp" />
            <select value={status} onChange={(e) => setStatus(e.currentTarget.value)}>{['open', 'all', 'delivered', 'cancelled'].map((s) => <option key={s} value={s}>{t(`op.ship.s.${s}`)}</option>)}</select></h3>
          <div style={{ overflowX: 'auto' }}>
            <table className="mx-table">
              <thead><tr><th>#</th><th>{t('op.ship.recipient')}</th><th>{t('op.ship.carrier')}</th><th>{t('op.ship.tracking')}</th><th className="num">{t('op.ship.cod')}</th><th>{t('op.status')}</th></tr></thead>
              <tbody>{rows.map((r) => (
                <tr key={r.id} className={sel === r.id ? 'sel' : ''} style={{ cursor: 'pointer' }} onClick={() => setSel(r.id)}>
                  <td>{r.number}{r.shop_order_number && <><br /><small>{t('op.ship.web')} #{r.shop_order_number}</small></>}{r.order_number && <><br /><small>{t('op.ship.so')} #{r.order_number}</small></>}</td>
                  <td><bdi>{r.recipient}</bdi><br /><small><bdi>{r.city}</bdi> {r.country}</small></td><td>{r.account}</td><td dir="ltr">{r.tracking_no || '—'}</td>
                  <td className="num" dir="ltr">{Number(r.cod_amount) > 0 ? `${amt(r.cod_amount)} ${r.currency}` : '—'}</td>
                  <td><span className={`mx-pill ${r.status === 'delivered' ? 'on' : r.status === 'exception' ? 'warn' : ''}`}>{t(`op.ship.s.${r.status}`)}</span></td>
                </tr>))}
                {rows.length === 0 && <tr><td colSpan={6} className="mx-empty">{t('op.ship.none')}</td></tr>}</tbody>
            </table>
          </div>
        </section>
        {sh && <ShipmentCard sh={sh} onChanged={reload} onClose={() => setSel(null)} me={me} />}
      </div>
    </div>
  );
}

function ShipmentCard({ sh, me, onChanged, onClose }: { sh: Shipment; me: Me; onChanged: () => void; onClose: () => void }) {
  const { t } = useTranslation();
  const base = useBase();
  const [msg, run] = useRun(onChanged);
  const act = (a: string, body: object = {}) => void run(api(`${base}/ship/shipments/${sh.id}/${a}`, { method: 'POST', body }));
  const edit = can(me, 'stock.create');
  return (
    <section className="mx-box" style={{ flex: '1 1 360px', minWidth: 0 }}>
      <h3 style={{ display: 'flex', gap: 6, alignItems: 'center' }}>#{sh.number} · <bdi>{sh.recipient}</bdi><span className="mx-sp" /><button className="mx-btn sm" onClick={onClose}>{t('op.close')}</button></h3>
      <div style={{ padding: 6, display: 'flex', flexDirection: 'column', gap: 4 }}>
        <div>📞 <span dir="ltr">{sh.phone}</span> · <bdi>{sh.address}</bdi> <bdi>{sh.city}</bdi> {sh.country}</div>
        <div>{t('op.ship.weight')} <span dir="ltr">{Number(sh.weight_kg)}</span> kg · {sh.pieces} · {sh.rate && <>{t('op.ship.rate')} <span dir="ltr">{amt(sh.rate)}</span></>}</div>
        {sh.tracking_no && <div>{t('op.ship.tracking')}: <b dir="ltr">{sh.tracking_no}</b></div>}
        {sh.error && <div className="red">{sh.error}</div>}
        <Flash m={msg} />
        {edit && <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
          {sh.status === 'draft' && <button className="mx-btn main" onClick={() => act('book')}>{t('op.ship.book')}</button>}
          {sh.label_id && <button className="mx-btn" onClick={() => void openFile(`${base}/attachments/${sh.label_id}/file`)}>{t('op.ship.label')}</button>}
          {sh.tracking_no && sh.carrier !== 'manual' && sh.status !== 'cancelled' && <button className="mx-btn" onClick={() => act('track')}>{t('op.ship.track')}</button>}
          {sh.status !== 'draft' && sh.status !== 'delivered' && sh.status !== 'cancelled' && ['in_transit', 'delivered', 'exception'].map((s) => (
            <button key={s} className="mx-btn" onClick={() => act('status', { status: s })}>{t(`op.ship.s.${s}`)}</button>))}
          {sh.status !== 'delivered' && sh.status !== 'cancelled' && <button className="mx-btn" style={{ color: '#b42318' }} onClick={() => { if (window.confirm(t('op.ship.cancelQ'))) act('cancel'); }}>{t('op.ship.cancel')}</button>}
        </div>}
        <table className="mx-table">
          <tbody>{sh.events.slice().reverse().map((e, i) => (
            <tr key={i}><td dir="ltr"><small>{e.at.replace('T', ' ').slice(0, 16)}</small></td><td>{t(`op.ship.s.${e.status}`, { defaultValue: e.status })}</td><td><bdi>{e.text}</bdi> {e.location && <small>· {e.location}</small>}</td></tr>))}</tbody>
        </table>
      </div>
    </section>
  );
}

/** New shipment (optionally for an online order / sales order / delivery stop): quotes, then book with the carrier. */
export function ShipForm({ shopOrderId, salesOrderId, tripStopId, onDone }: { shopOrderId?: string; salesOrderId?: string; tripStopId?: string; onDone?: () => void }) {
  const { t } = useTranslation();
  const base = useBase();
  const [carriers] = useLoad<Carrier[]>(base && `${base}/ship/carriers`, []);
  const [f, setF] = useState({ account_id: '', recipient: '', phone: '', address: '', city: '', country: 'LB', weight_kg: '1', pieces: '1', contents: '', currency: 'USD', cod_amount: '0', service: '', rate: '' });
  const [rates, setRates] = useState<{ account_id: string; name: string; service: string; amount: string; currency: string; days?: string }[] | null>(null);
  const [errs, setErrs] = useState<{ name: string; error: string; detail?: string }[]>([]);
  const [msg, run] = useRun(onDone);
  const q = shopOrderId ? `shop_order_id=${shopOrderId}` : salesOrderId ? `sales_order_id=${salesOrderId}` : tripStopId ? `trip_stop_id=${tripStopId}` : '';
  useEffect(() => {
    if (!base || !q) return;
    api<Record<string, string | null>>(`${base}/ship/prefill?${q}`).then((p) => setF((x) => ({ ...x, recipient: p.recipient ?? '', phone: p.phone ?? '', address: p.address ?? '',
      city: p.city ?? '', currency: p.currency ?? x.currency, cod_amount: String(Number(p.cod_amount ?? 0)) }))).catch(() => {});
  }, [base, q]);
  useEffect(() => { const d = carriers.find((c) => c.is_default && c.is_active) ?? carriers.find((c) => c.is_active); if (d && !f.account_id) setF((x) => ({ ...x, account_id: d.id })); }, [carriers, f.account_id]);
  const body = () => ({ ...f, pieces: Number(f.pieces) || 1, ...(shopOrderId ? { shop_order_id: shopOrderId } : {}), ...(salesOrderId ? { sales_order_id: salesOrderId } : {}), ...(tripStopId ? { trip_stop_id: tripStopId } : {}) });
  const quote = () => void run(api<{ rates: typeof rates; errors: typeof errs }>(`${base}/ship/quote`, { method: 'POST', body: { ...body(), account_id: '' } }),
    (x) => { const r = x as { rates: NonNullable<typeof rates>; errors: typeof errs }; setRates(r.rates); setErrs(r.errors); });
  const create = (book: boolean) => void run(api(`${base}/ship/shipments`, { method: 'POST', body: { ...body(), book } }), () => setRates(null));
  const inp = (k: keyof typeof f, w = 110, ltr = false) => (
    <label className="mx-fld">{t(`op.ship.f.${k}`)}<input dir={ltr ? 'ltr' : undefined} style={{ width: w }} value={f[k]} onChange={(e) => setF({ ...f, [k]: e.currentTarget.value })} /></label>);
  const can = f.account_id && f.recipient.trim();
  return (
    <section className="mx-box">
      <h3>{t('op.ship.new')}</h3>
      <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
        <label className="mx-fld">{t('op.ship.carrier')}<select value={f.account_id} onChange={(e) => setF({ ...f, account_id: e.currentTarget.value })}>
          <option value="">—</option>{carriers.filter((c) => c.is_active).map((c) => <option key={c.id} value={c.id}>{c.name}{c.configured ? '' : ` (${t('op.ship.needKeys')})`}</option>)}</select></label>
        {inp('recipient', 150)}{inp('phone', 110, true)}{inp('address', 180)}{inp('city')}{inp('country', 40, true)}{inp('weight_kg', 60, true)}{inp('pieces', 40, true)}
        {inp('contents', 140)}{inp('cod_amount', 70, true)}{inp('currency', 50, true)}
      </div>
      <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap' }}>
        <button className="mx-btn" disabled={!f.recipient.trim()} onClick={quote}>{t('op.ship.quote')}</button>
        <button className="mx-btn" disabled={!can} title={!can ? t('op.ship.needRecipient') : undefined} onClick={() => create(false)}>{t('op.ship.draft')}</button>
        <button className="mx-btn main" disabled={!can} title={!can ? t('op.ship.needRecipient') : undefined} onClick={() => create(true)}>{t('op.ship.createBook')}</button>
      </div>
      <Flash m={msg} />
      {rates && (
        <table className="mx-table">
          <thead><tr><th>{t('op.ship.carrier')}</th><th>{t('op.ship.service')}</th><th className="num">{t('op.ship.rate')}</th><th>{t('op.ship.days')}</th><th /></tr></thead>
          <tbody>
            {rates.map((r, i) => (
              <tr key={i}><td><bdi>{r.name}</bdi></td><td dir="ltr">{r.service}</td><td className="num" dir="ltr">{amt(r.amount)} {r.currency}</td><td>{r.days ?? ''}</td>
                <td><button className="mx-btn sm" onClick={() => setF({ ...f, account_id: r.account_id, service: r.service, rate: r.amount })}>{t('op.ship.choose')}</button></td></tr>))}
            {errs.map((e, i) => <tr key={`e${i}`}><td><bdi>{e.name}</bdi></td><td colSpan={4} className="red">{t(`errors.${e.error}`, { defaultValue: e.error })} {e.detail ?? ''}</td></tr>)}
            {rates.length === 0 && errs.length === 0 && <tr><td colSpan={5} className="mx-empty">{t('op.ship.noRates')}</td></tr>}
          </tbody>
        </table>)}
    </section>
  );
}

// ======================= timelines =======================

function addDays(d: string, n: number) { const x = new Date(`${d}T00:00:00Z`); x.setUTCDate(x.getUTCDate() + n); return x.toISOString().slice(0, 10); }

function GanttProjects({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [projects] = useLoad<{ id: string; number: number; name: string }[]>(base && `${base}/prj/projects?status=open`, []);
  const [pid, setPid] = useState('');
  const [g, reload] = useLoad<{ title: string; bars: GBar[]; warnings: GWarning[] } | null>(base && pid && `${base}/gantt/projects/${pid}`, null);
  const [deps, reloadDeps] = useLoad<{ id: string; task_number: number; depends_on_number: number }[]>(base && pid && `${base}/gantt/projects/${pid}/deps`, []);
  const [msg, run] = useRun(() => { reload(); reloadDeps(); });
  const [shift, setShift] = useState(true);
  const [nd, setNd] = useState({ task: '', dep: '' });
  useEffect(() => { if (!pid && projects[0]) setPid(projects[0].id); }, [projects, pid]);
  const range = useMemo(() => {
    const bars = g?.bars ?? [];
    let a = addDays(today(), -7), b = addDays(today(), 30);
    for (const x of bars) { if (x.start < a) a = x.start; if (x.end > b) b = x.end; }
    return { from: a, to: b };
  }, [g]);
  const edit = can(me, 'projects.edit');
  return (
    <div className="mx-col" style={{ gap: 6 }}>
      <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap', alignItems: 'end' }}>
        <label className="mx-fld">{t('op.g.project')}<select value={pid} onChange={(e) => setPid(e.currentTarget.value)}>
          {projects.map((p) => <option key={p.id} value={p.id}>#{p.number} · {p.name}</option>)}</select></label>
        {edit && <label><input type="checkbox" checked={shift} onChange={(e) => setShift(e.currentTarget.checked)} /> {t('op.g.shift')}</label>}
        <Legend warnings={g?.warnings ?? []} />
      </div>
      <Flash m={msg} />
      {g && <Gantt bars={g.bars} warnings={g.warnings} unit="day" from={range.from} to={range.to} readOnly={!edit}
        onMove={(b, s, e) => void run(api(`${base}/gantt/project-tasks/${b.id}`, { method: 'PUT', body: { start: s, end: e < s ? s : e, shift_dependents: shift } }))} />}
      {g && edit && (
        <section className="mx-box">
          <h3>{t('op.g.deps')}</h3>
          <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
            <label className="mx-fld">{t('op.g.task')}<select value={nd.task} onChange={(e) => setNd({ ...nd, task: e.currentTarget.value })}>
              <option value="">—</option>{g.bars.map((b) => <option key={b.id} value={b.id}>{b.label}</option>)}</select></label>
            <label className="mx-fld">{t('op.g.after')}<select value={nd.dep} onChange={(e) => setNd({ ...nd, dep: e.currentTarget.value })}>
              <option value="">—</option>{g.bars.filter((b) => b.id !== nd.task).map((b) => <option key={b.id} value={b.id}>{b.label}</option>)}</select></label>
            <button className="mx-btn main" disabled={!nd.task || !nd.dep} onClick={() => void run(api(`${base}/gantt/project-tasks/${nd.task}/deps`, { method: 'POST', body: { depends_on: nd.dep } }), () => setNd({ task: '', dep: '' }))}>{t('op.add')}</button>
          </div>
          <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap' }}>
            {deps.map((d) => <span key={d.id} className="mx-pill">#{d.depends_on_number} → #{d.task_number}
              <button className="mx-link" aria-label={t('op.delete')} onClick={() => void run(api(`${base}/gantt/deps/${d.id}`, { method: 'DELETE' }))}> ✕</button></span>)}
            {deps.length === 0 && <small>{t('op.g.noDeps')}</small>}
          </div>
        </section>)}
    </div>
  );
}

function Legend({ warnings }: { warnings: GWarning[] }) {
  const { t } = useTranslation();
  const n = (k: string) => warnings.filter((w) => w.kind === k).length;
  return (
    <span className="mx-row" style={{ gap: 8, flexWrap: 'wrap', fontSize: 12 }}>
      <span><i style={{ display: 'inline-block', width: 12, height: 8, background: '#1d4ed8' }} /> {t('op.g.planned')}</span>
      <span><i style={{ display: 'inline-block', width: 12, height: 8, background: '#8aa0c8' }} /> {t('op.g.guess')}</span>
      <span><i style={{ display: 'inline-block', width: 12, height: 8, background: '#2f9e44' }} /> {t('op.g.done')}</span>
      {['dependency', 'overlap', 'overload', 'late'].filter((k) => n(k) > 0).map((k) => <span key={k} className="mx-pill warn">{t(`op.g.w.${k}`, { n: n(k) })}</span>)}
    </span>
  );
}

function GanttMfg({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [from, setFrom] = useState(addDays(today(), -1));
  const [to, setTo] = useState(addDays(today(), 13));
  const [g, reload] = useLoad<{ bars: GBar[]; centers: { id: string; name: string }[]; load: GLoad[]; warnings: GWarning[] } | null>(base && `${base}/gantt/mfg?from=${from}&to=${to}`, null);
  const [orders] = useLoad<{ id: string; number: number; status: string; item_name?: string; quantity: string }[]>(base && `${base}/mfg/orders?status=confirmed`, []);
  const [msg, run] = useRun(reload);
  const [plan, setPlan] = useState({ order_id: '', start: `${today()}T08:00` });
  const edit = can(me, 'stock.create');
  const rows = (g?.centers ?? []).map((c) => ({ key: c.id, label: c.name }));
  return (
    <div className="mx-col" style={{ gap: 6 }}>
      <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap', alignItems: 'end' }}>
        <label className="mx-fld">{t('op.from')}<input type="date" dir="ltr" value={from} onChange={(e) => setFrom(e.currentTarget.value)} /></label>
        <label className="mx-fld">{t('op.to')}<input type="date" dir="ltr" value={to} onChange={(e) => setTo(e.currentTarget.value)} /></label>
        {edit && <>
          <label className="mx-fld">{t('op.g.order')}<select value={plan.order_id} onChange={(e) => setPlan({ ...plan, order_id: e.currentTarget.value })}>
            <option value="">—</option>{orders.map((o) => <option key={o.id} value={o.id}>MO #{o.number} {o.item_name ?? ''} × {Number(o.quantity)}</option>)}</select></label>
          <label className="mx-fld">{t('op.g.startAt')}<input type="datetime-local" dir="ltr" value={plan.start} onChange={(e) => setPlan({ ...plan, start: e.currentTarget.value })} /></label>
          <button className="mx-btn main" disabled={!plan.order_id} title={!plan.order_id ? t('op.g.needOrder') : undefined}
            onClick={() => void run(api(`${base}/gantt/mfg/plan`, { method: 'POST', body: { order_id: plan.order_id, start: new Date(plan.start).toISOString() } }))}>{t('op.g.plan')}</button>
        </>}
        <Legend warnings={g?.warnings ?? []} />
      </div>
      <small>{t('op.g.mfgHelp')}</small>
      <Flash m={msg} />
      {g && <Gantt bars={g.bars} warnings={g.warnings} unit="hour" from={from} to={to} rows={rows} load={g.load} readOnly={!edit}
        onMove={(b, s, _e, row) => void run(api(`${base}/gantt/workorders/${b.id}`, { method: 'PUT', body: { start: s, workcenter_id: row !== b.row ? row : '' } }))} />}
    </div>
  );
}

function GanttField({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [from, setFrom] = useState(today());
  const [to, setTo] = useState(addDays(today(), 6));
  const [g, reload] = useLoad<{ bars: GBar[]; technicians: { id: string; full_name: string }[]; unplanned: { id: string; number: number; title: string; customer: string; duration_min: number; priority: string }[]; warnings: GWarning[] } | null>(
    base && `${base}/gantt/field?from=${from}&to=${to}`, null);
  const [msg, run, setMsg] = useRun(reload);
  const [pl, setPl] = useState<{ id: string; tech: string; start: string }>({ id: '', tech: '', start: `${today()}T09:00` });
  const edit = can(me, 'service.edit');
  const techs = g?.technicians ?? [];
  const used = new Set((g?.bars ?? []).map((b) => b.row).filter(Boolean));
  const rows = [...techs.filter((x) => used.has(x.id)), ...techs.filter((x) => !used.has(x.id))].map((x) => ({ key: x.id, label: x.full_name }));
  const move = (id: string, start: string, tech: string) => void run(api<{ conflicts: string[] }>(`${base}/gantt/service-jobs/${id}`, { method: 'PUT', body: { start, technician_id: tech } }),
    (x) => { const c = (x as { conflicts: string[] }).conflicts; if (c.length) setMsg({ ok: false, text: t('op.g.conflict', { n: c.length }) }); });
  return (
    <div className="mx-col" style={{ gap: 6 }}>
      <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap', alignItems: 'end' }}>
        <label className="mx-fld">{t('op.from')}<input type="date" dir="ltr" value={from} onChange={(e) => setFrom(e.currentTarget.value)} /></label>
        <label className="mx-fld">{t('op.to')}<input type="date" dir="ltr" value={to} onChange={(e) => setTo(e.currentTarget.value)} /></label>
        <Legend warnings={g?.warnings ?? []} />
      </div>
      <Flash m={msg} />
      {g && <Gantt bars={g.bars} warnings={g.warnings} unit="hour" from={from} to={to} rows={rows} readOnly={!edit} onMove={(b, s, _e, row) => move(b.id, s, row)} />}
      {g && g.unplanned.length > 0 && (
        <section className="mx-box">
          <h3>{t('op.g.unplanned')}</h3>
          {edit && <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
            <label className="mx-fld">{t('op.g.job')}<select value={pl.id} onChange={(e) => setPl({ ...pl, id: e.currentTarget.value })}>
              <option value="">—</option>{g.unplanned.map((j) => <option key={j.id} value={j.id}>#{j.number} · {j.title} · {j.customer}</option>)}</select></label>
            <label className="mx-fld">{t('op.g.tech')}<select value={pl.tech} onChange={(e) => setPl({ ...pl, tech: e.currentTarget.value })}>
              <option value="">—</option>{techs.map((x) => <option key={x.id} value={x.id}>{x.full_name}</option>)}</select></label>
            <label className="mx-fld">{t('op.g.startAt')}<input type="datetime-local" dir="ltr" value={pl.start} onChange={(e) => setPl({ ...pl, start: e.currentTarget.value })} /></label>
            <button className="mx-btn main" disabled={!pl.id || !pl.tech} title={!pl.id || !pl.tech ? t('op.g.needJobTech') : undefined}
              onClick={() => { move(pl.id, new Date(pl.start).toISOString(), pl.tech); setPl({ ...pl, id: '' }); }}>{t('op.g.plan')}</button>
          </div>}
          <table className="mx-table"><tbody>{g.unplanned.map((j) => (
            <tr key={j.id}><td>#{j.number}</td><td><bdi>{j.title}</bdi></td><td><bdi>{j.customer}</bdi></td><td className="num">{j.duration_min} min</td><td>{j.priority}</td></tr>))}</tbody></table>
        </section>)}
    </div>
  );
}

