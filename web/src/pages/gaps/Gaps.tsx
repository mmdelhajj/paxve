import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { api, can, type Me } from '../../api';
import { useCompany } from '../../company';
import DataGrid, { type Col } from '../../components/DataGrid';
import Chatter, { ACT_KINDS } from '../../components/Chatter';
import { errorText, formatAmount, formatDate, formatDateTime, today } from '../../util';
import { printPage } from '../../native';
import { openScreen } from '../../nav';
import type { QuoteTpl } from '../sales/Sales';

// Odoo gaps (Magic-style screens): my activities & notifications, analysis (pivot + chart), repair orders,
// sales on instalments, quotation templates, fiscal positions, credit control.

type Msg = { ok: boolean; text: string } | null;
function Flash({ m }: { m: Msg }) {
  return m ? <div className={`mx-flash ${m.ok ? 'good' : 'bad'}`}>{m.text}</div> : null;
}
function useBase(): string | null {
  const { company } = useCompany();
  return company ? `/companies/${company.id}` : null;
}
function useLoad<T>(path: string | null | false, init: T): [T, () => void] {
  const [v, setV] = useState<T>(init);
  const load = useCallback(() => {
    if (path) api<T>(path).then(setV).catch(() => setV(init));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [path]);
  useEffect(load, [load]);
  return [v, load];
}
function useRun(after?: () => void): [Msg, (p: Promise<unknown>, then?: (x: unknown) => void) => Promise<void>, (m: Msg) => void] {
  const { t } = useTranslation();
  const [msg, setMsg] = useState<Msg>(null);
  const run = async (p: Promise<unknown>, then?: (x: unknown) => void) => {
    try { const x = await p; setMsg({ ok: true, text: t('common.saved') }); then?.(x); after?.(); } catch (e) { setMsg({ ok: false, text: errorText(t, e) }); }
  };
  return [msg, run, setMsg];
}

interface Cust { id: string; name: string; code: string; phone?: string }
/** Customer search box (2+ letters). */
function CustomerSearch({ value, onPick }: { value: string; onPick: (c: Cust | null) => void }) {
  const { t } = useTranslation();
  const base = useBase();
  const [q, setQ] = useState(value);
  const [list, setList] = useState<Cust[]>([]);
  const [picked, setPicked] = useState(!!value);
  useEffect(() => {
    if (!base || picked || q.trim().length < 2) { setList([]); return; }
    const h = window.setTimeout(() => api<Cust[]>(`${base}/customers?limit=15&q=${encodeURIComponent(q.trim())}`).then(setList).catch(() => setList([])), 250);
    return () => window.clearTimeout(h);
  }, [base, q, picked]);
  return (
    <div style={{ position: 'relative' }}>
      <input placeholder={t('gp.findCustomer')} value={q} onChange={(e) => { setQ(e.currentTarget.value); setPicked(false); onPick(null); }} />
      {list.length > 0 && (
        <div className="mx-list mx-suggest">{list.map((c) => (
          <button key={c.id} type="button" className="mx-link" onClick={() => { setQ(`${c.code} ${c.name}`); setPicked(true); setList([]); onPick(c); }}>{c.code} · {c.name}</button>
        ))}</div>
      )}
    </div>
  );
}

const MODES = ['inbox', 'analysis', 'repairs', 'instalments', 'templates', 'fiscal', 'credit'] as const;
type Mode = typeof MODES[number];

export default function Gaps({ me, initial }: { me: Me; initial?: string }) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<Mode>((MODES as readonly string[]).includes(initial ?? '') ? initial as Mode : 'inbox');
  return (
    <div className="mx-col">
      <div className="mx-strip"><b>{t(`gp.m.${mode}`)}</b><span className="mx-sp" /><small style={{ opacity: 0.8 }}>{t(`gp.help.${mode}`)}</small></div>
      <div className="mx-modes" role="tablist">
        {MODES.map((m) => <button key={m} role="tab" className="mx-mode" aria-selected={mode === m} onClick={() => setMode(m)}>{t(`gp.m.${m}`)}</button>)}
      </div>
      {mode === 'inbox' && <Inbox me={me} />}
      {mode === 'analysis' && <Analysis />}
      {mode === 'repairs' && <Repairs me={me} />}
      {mode === 'instalments' && <Instalments me={me} />}
      {mode === 'templates' && <Templates me={me} />}
      {mode === 'fiscal' && <Fiscal me={me} />}
      {mode === 'credit' && <Credit me={me} />}
    </div>
  );
}

// ======================= inbox: notifications + my activities =======================

interface Notif { id: string; kind: string; title: string; body: string; entity_type: string | null; entity_id: string | null; read_at: string | null; created_at: string; actor: string | null }
interface Act {
  id: string; entity_type: string | null; entity_id: string | null; kind: string; summary: string; note: string; due_date: string; assigned_to: string;
  assigned_name: string | null; done_at: string | null; done_note: string; state: string; record?: string; created_by_name: string | null;
}
interface Person { id: string; full_name: string }

/** Where a linked record opens (record types with their own screen). */
export function openRecord(type: string | null, id: string | null) {
  if (!type || !id) return;
  const page: Record<string, string> = { customer: 'customer', supplier: 'supplier', item: 'item' };
  if (page[type]) { window.location.hash = `#/${page[type]}/${id}`; return; }
  const screen: Record<string, [string, string]> = { repair: ['gaps', 'repairs'], instalment: ['gaps', 'instalments'], ticket: ['service', ''],
    purchase_order: ['po', ''], invoice: ['invoices', ''], bill: ['bills', ''], sales_order: ['orders', ''], employee: ['hr', 'people'] };
  const s = screen[type];
  if (s) openScreen(s[0], s[1]);
}

function Inbox({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [scope, setScope] = useState('mine');
  const [state, setState] = useState('open');
  const [n, reloadN] = useLoad<{ items: Notif[]; unread: number; activities: { overdue: number; today: number; planned: number } } | null>(base && `${base}/notifications`, null);
  const [acts, reload] = useLoad<Act[]>(base && `${base}/record-activities?scope=${scope}&state=${state}`, []);
  const [people] = useLoad<Person[]>(base && `${base}/mentionable`, []);
  const [na, setNa] = useState({ kind: 'todo', summary: '', due_date: today(), assigned_to: '', note: '' });
  const [msg, run] = useRun(() => { reload(); reloadN(); });
  const canAll = can(me, 'users.view') || can(me, 'settings.manage');
  const cols: Col<Act>[] = [
    { key: 'st', title: t('gp.act.state'), get: (r) => t(`gp.act.s.${r.state}`), width: 80, cls: (r) => (r.state === 'overdue' ? 'red' : '') },
    { key: 'due', title: t('gp.act.due'), get: (r) => r.due_date, render: (r) => formatDate(r.due_date), width: 95 },
    { key: 'kind', title: t('gp.act.kind'), get: (r) => t(`gp.act.k.${r.kind}`), width: 80 },
    { key: 'sum', title: t('gp.act.summary'), get: (r) => r.summary, width: 220 },
    { key: 'rec', title: t('gp.act.record'), get: (r) => r.record ?? '', width: 200 },
    { key: 'who', title: t('gp.act.who'), get: (r) => r.assigned_name ?? '', width: 120 },
    { key: 'by', title: t('gp.act.by'), get: (r) => r.created_by_name ?? '', width: 110 },
    { key: 'done', title: t('gp.act.doneNote'), get: (r) => r.done_note, width: 160 },
  ];
  const [sel, setSel] = useState<Act | null>(null);
  return (
    <div className="mx-body" style={{ gridTemplateColumns: 'minmax(0, 1fr) minmax(280px, 400px)' }}>
      <section className="mx-box"><h3>{t('gp.inbox.acts')}</h3>
        <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap' }}>
          {n && <>
            <span className={`mx-pill ${n.activities.overdue ? 'warn' : ''}`}>{t('gp.act.s.overdue')}: {n.activities.overdue}</span>
            <span className="mx-pill">{t('gp.act.s.today')}: {n.activities.today}</span>
            <span className="mx-pill">{t('gp.act.s.planned')}: {n.activities.planned}</span>
          </>}
          <span className="mx-sp" />
          {canAll && <select value={scope} aria-label={t('gp.inbox.scope')} onChange={(e) => setScope(e.currentTarget.value)}>
            <option value="mine">{t('gp.inbox.mine')}</option><option value="all">{t('gp.inbox.all')}</option></select>}
          <select value={state} aria-label={t('gp.act.state')} onChange={(e) => setState(e.currentTarget.value)}>
            <option value="open">{t('gp.inbox.open')}</option><option value="done">{t('gp.act.s.done')}</option><option value="all">{t('gp.inbox.everything')}</option></select>
        </div>
        <DataGrid rows={acts} cols={cols} rowKey={(r) => r.id} selected={sel?.id ?? null} title={t('gp.inbox.acts')} onSelect={setSel}
          onActivate={(r) => openRecord(r.entity_type, r.entity_id)} empty={t('gp.act.none')} />
        {sel && !sel.done_at && (
          <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
            <b>{sel.summary}</b>
            <label>{t('gp.act.reschedule')}<input type="date" dir="ltr" defaultValue={sel.due_date} key={sel.id}
              onBlur={(e) => { const v = e.currentTarget.value; if (base && v && v !== sel.due_date) void run(api(`${base}/record-activities/${sel.id}`, { method: 'PATCH', body: { due_date: v } })); }} /></label>
            <button className="mx-btn sm main" onClick={() => { const note = window.prompt(t('gp.act.doneNote')) ?? ''; if (base) void run(api(`${base}/record-activities/${sel.id}/done`, { method: 'POST', body: { note } }), () => setSel(null)); }}>{t('gp.act.done')}</button>
            {sel.entity_type && <button className="mx-btn sm" onClick={() => openRecord(sel.entity_type, sel.entity_id)}>{t('gp.act.open')}</button>}
          </div>
        )}
        <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end', borderTop: '1px solid var(--mx-line, #ddd)' }}>
          <b>{t('gp.act.new')}</b>
          <select value={na.kind} aria-label={t('gp.act.kind')} onChange={(e) => setNa({ ...na, kind: e.currentTarget.value })}>{ACT_KINDS.map((k) => <option key={k} value={k}>{t(`gp.act.k.${k}`)}</option>)}</select>
          <input style={{ flex: 1, minWidth: 160 }} placeholder={t('gp.act.summary')} value={na.summary} onChange={(e) => setNa({ ...na, summary: e.currentTarget.value })} />
          <input type="date" dir="ltr" aria-label={t('gp.act.due')} value={na.due_date} onChange={(e) => setNa({ ...na, due_date: e.currentTarget.value })} />
          <select value={na.assigned_to} aria-label={t('gp.act.who')} onChange={(e) => setNa({ ...na, assigned_to: e.currentTarget.value })}>
            <option value="">{t('gp.act.me')}</option>{people.map((p) => <option key={p.id} value={p.id}>{p.full_name}</option>)}</select>
          <button className="mx-btn sm main" disabled={!na.summary.trim()} onClick={() => base && void run(api(`${base}/record-activities`, { method: 'POST', body: {
            kind: na.kind, summary: na.summary, due_date: na.due_date, ...(na.assigned_to ? { assigned_to: na.assigned_to } : {}) } }), () => setNa({ ...na, summary: '' }))}>{t('gp.act.add')}</button>
        </div>
        <Flash m={msg} />
      </section>
      <section className="mx-box"><h3>{t('gp.inbox.notifs')} {n && n.unread > 0 && <span className="mx-badge">{n.unread}</span>}</h3>
        <div className="mx-row" style={{ padding: 6 }}>
          <button className="mx-btn sm" disabled={!n?.unread} onClick={() => base && void run(api(`${base}/notifications/read`, { method: 'POST', body: {} }))}>{t('gp.inbox.readAll')}</button>
        </div>
        <div style={{ overflow: 'auto', padding: 6, display: 'flex', flexDirection: 'column', gap: 4 }}>
          {(n?.items ?? []).length === 0 && <small style={{ opacity: 0.7 }}>{t('gp.inbox.noNotifs')}</small>}
          {(n?.items ?? []).map((x) => (
            <button key={x.id} className={`mx-note mx-notif ${x.read_at ? '' : 'unread'}`} style={{ textAlign: 'start', cursor: 'pointer' }}
              onClick={() => { if (base && !x.read_at) void api(`${base}/notifications/read`, { method: 'POST', body: { ids: [x.id] } }).then(reloadN); openRecord(x.entity_type, x.entity_id); }}>
              <small>{t(`gp.notif.${x.kind}`)} · <bdi>{formatDateTime(x.created_at)}</bdi>{x.actor ? <> · <bdi>{x.actor}</bdi></> : null}</small>
              <div><b><bdi>{x.title}</bdi></b></div>
              {x.body && <div style={{ whiteSpace: 'pre-wrap', fontSize: 12.5 }}><bdi>{x.body}</bdi></div>}
            </button>
          ))}
        </div>
      </section>
    </div>
  );
}

// ======================= analysis: pivot + chart =======================

interface SrcInfo { dims: string[]; measures: string[]; has_currency: boolean }
interface Spec { source: string; rows: string[]; cols: string[]; measure: string; from: string; to: string; currency: string; chart: string; filters?: Record<string, string> }
interface Saved { id: string; name: string; spec: Spec; shared: boolean; mine: boolean; owner_name: string | null }
const DATE_GROUPS = ['date:day', 'date:week', 'date:month', 'date:quarter', 'date:year'];

function dimLabel(t: TFunction, d: string): string {
  return d.startsWith('date:') ? t('gp.an.date', { g: t(`gp.an.g.${d.slice(5)}`) }) : t(`gp.an.d.${d}`, { defaultValue: d });
}
function keyLabel(t: TFunction, dim: string, v: string): string {
  if (['status', 'source', 'direction', 'type', 'line_kind', 'kind', 'billing'].includes(dim)) return t(`gp.an.v.${v}`, { defaultValue: v });
  return v;
}

function Analysis() {
  const { t } = useTranslation();
  const base = useBase();
  const { company, currencies } = useCompany();
  const [sources] = useLoad<Record<string, SrcInfo>>(base && `${base}/analysis/sources`, {});
  const [saved, reloadSaved] = useLoad<Saved[]>(base && `${base}/analysis/reports`, []);
  const y = new Date().getFullYear();
  const [sp, setSp] = useState<Spec>({ source: 'sales', rows: ['customer'], cols: ['date:month'], measure: 'amount', from: `${y}-01-01`, to: `${y}-12-31`, currency: '', chart: 'bar' });
  const [res, setRes] = useState<{ rows: { r: string[]; c: string[]; v: string }[]; currency: string; truncated: boolean } | null>(null);
  const [msg, run, setMsg] = useRun();
  const [name, setName] = useState('');
  const [shared, setShared] = useState(false);
  const [loadedId, setLoadedId] = useState('');
  const src = sources[sp.source];
  const [auto, setAuto] = useState(false);
  useEffect(() => { if (!auto && src) { setAuto(true); void go(); } }, [src, auto]); // eslint-disable-line react-hooks/exhaustive-deps
  const dims = src ? [...src.dims.slice().sort(), ...DATE_GROUPS] : [];

  async function go(s: Spec = sp) {
    if (!base) return;
    setMsg(null);
    try {
      const body = { ...s, rows: s.rows.filter(Boolean), cols: s.cols.filter(Boolean), currency: s.currency };
      setRes(await api(`${base}/analysis/run`, { method: 'POST', body }));
    } catch (e) { setMsg({ ok: false, text: errorText(t, e) }); }
  }
  function setSource(s: string) {
    const info = sources[s];
    const next = { ...sp, source: s, rows: [info?.dims.includes(sp.rows[0]) ? sp.rows[0] : (info?.dims.slice().sort()[0] ?? '')], cols: ['date:month'],
      measure: info?.measures.includes(sp.measure) ? sp.measure : (info?.measures[0] ?? '') };
    setSp(next);
    setRes(null);
  }

  // pivot: row keys × column keys
  const pivot = useMemo(() => {
    if (!res) return null;
    const colKeys = [...new Set(res.rows.map((x) => x.c.join(' / ')))];
    const rowMap = new Map<string, { key: string; label: string[]; vals: Record<string, number>; total: number }>();
    for (const x of res.rows) {
      const rk = x.r.join('\u0001');
      const ck = x.c.join(' / ');
      let row = rowMap.get(rk);
      if (!row) { row = { key: rk, label: x.r, vals: {}, total: 0 }; rowMap.set(rk, row); }
      const v = Number(x.v) || 0;
      row.vals[ck] = (row.vals[ck] ?? 0) + v;
      row.total += v;
    }
    return { colKeys, rows: [...rowMap.values()] };
  }, [res]);

  const dec = res?.currency ? (currencies.find((c) => c.code === res.currency)?.decimals ?? 2) : (['count', 'customers', 'quantity'].includes(sp.measure) ? 0 : 2);
  type PRow = { key: string; label: string[]; vals: Record<string, number>; total: number };
  const pcols: Col<PRow>[] = pivot ? [
    ...sp.rows.filter(Boolean).map((d, i) => ({ key: `r${i}`, title: dimLabel(t, d), get: (r: PRow) => keyLabel(t, d, r.label[i] ?? ''), width: 180 })),
    ...(sp.cols.filter(Boolean).length ? pivot.colKeys.map((ck) => ({ key: `c:${ck}`, title: keyLabel(t, sp.cols[0], ck), num: true, decimals: dec, total: 'sum' as const,
      get: (r: PRow) => r.vals[ck] ?? 0, width: 110 })) : []),
    { key: 'total', title: t('gp.an.total'), num: true, decimals: dec, total: 'sum', get: (r) => r.total, width: 120 },
  ] : [];

  return (
    <div className="mx-body" style={{ gridTemplateColumns: 'minmax(260px, 320px) minmax(0, 1fr)' }}>
      <section className="mx-box"><h3>{t('gp.an.setup')}</h3>
        <div className="mx-pay">
          <label>{t('gp.an.source')}<select value={sp.source} onChange={(e) => setSource(e.currentTarget.value)}>
            {Object.keys(sources).sort().map((s) => <option key={s} value={s}>{t(`gp.an.s.${s}`)}</option>)}</select></label>
          {[0, 1].map((i) => (
            <label key={`r${i}`}>{t('gp.an.rows')} {i + 1}<select value={sp.rows[i] ?? ''} onChange={(e) => { const r = [...sp.rows]; r[i] = e.currentTarget.value; setSp({ ...sp, rows: r }); }}>
              <option value="">—</option>{dims.map((d) => <option key={d} value={d}>{dimLabel(t, d)}</option>)}</select></label>
          ))}
          <label>{t('gp.an.cols')}<select value={sp.cols[0] ?? ''} onChange={(e) => setSp({ ...sp, cols: [e.currentTarget.value] })}>
            <option value="">—</option>{dims.map((d) => <option key={d} value={d}>{dimLabel(t, d)}</option>)}</select></label>
          <label>{t('gp.an.measure')}<select value={sp.measure} onChange={(e) => setSp({ ...sp, measure: e.currentTarget.value })}>
            {(src?.measures ?? []).map((m) => <option key={m} value={m}>{t(`gp.an.m.${m}`, { defaultValue: m })}</option>)}</select></label>
          <label>{t('gp.an.from')}<input type="date" dir="ltr" value={sp.from} onChange={(e) => setSp({ ...sp, from: e.currentTarget.value })} /></label>
          <label>{t('gp.an.to')}<input type="date" dir="ltr" value={sp.to} onChange={(e) => setSp({ ...sp, to: e.currentTarget.value })} /></label>
          {src?.has_currency && <label>{t('gp.an.currency')}<select value={sp.currency} onChange={(e) => setSp({ ...sp, currency: e.currentTarget.value })}>
            <option value="">{company?.base_currency}</option>{currencies.filter((c) => c.code !== company?.base_currency && (c.code === company?.local_currency || c.code === 'USD' || c.code === 'EUR')).map((c) => <option key={c.code} value={c.code}>{c.code}</option>)}</select></label>}
          <label>{t('gp.an.chart')}<select value={sp.chart} onChange={(e) => setSp({ ...sp, chart: e.currentTarget.value })}>
            {['bar', 'line', 'pie', 'table'].map((c) => <option key={c} value={c}>{t(`gp.an.c.${c}`)}</option>)}</select></label>
          <button className="mx-btn main" onClick={() => void go()}>{t('gp.an.run')}</button>
          <h4 style={{ margin: '8px 0 2px' }}>{t('gp.an.saved')}</h4>
          <select value={loadedId} aria-label={t('gp.an.saved')} onChange={(e) => {
            const r = saved.find((x) => x.id === e.currentTarget.value); setLoadedId(e.currentTarget.value);
            if (r) { const s = { ...r.spec, rows: r.spec.rows ?? [], cols: r.spec.cols ?? [], chart: r.spec.chart || 'bar', currency: r.spec.currency ?? '' }; setSp(s); setName(r.name); setShared(r.shared); void go(s); }
          }}>
            <option value="">—</option>{saved.map((r) => <option key={r.id} value={r.id}>{r.name}{r.shared ? ` (${t('gp.an.sharedTag')})` : ''}</option>)}</select>
          <label>{t('gp.an.name')}<input value={name} onChange={(e) => setName(e.currentTarget.value)} /></label>
          <label className="mx-row" style={{ gap: 4 }}><input type="checkbox" checked={shared} onChange={(e) => setShared(e.currentTarget.checked)} />{t('gp.an.share')}</label>
          <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
            <button className="mx-btn sm" disabled={!name.trim() || !base} onClick={() => base && void run(api(`${base}/analysis/reports`, { method: 'POST', body: { name, spec: sp, shared } }), () => reloadSaved())}>{t('gp.an.saveNew')}</button>
            {loadedId && saved.find((x) => x.id === loadedId)?.mine && <>
              <button className="mx-btn sm" onClick={() => base && void run(api(`${base}/analysis/reports/${loadedId}`, { method: 'PUT', body: { name, spec: sp, shared } }), () => reloadSaved())}>{t('common.save')}</button>
              <button className="mx-btn sm danger" onClick={() => base && window.confirm(t('gp.an.deleteQ')) && void run(api(`${base}/analysis/reports/${loadedId}`, { method: 'PUT', body: { name: name || 'x', spec: sp, shared, delete: true } }), () => { setLoadedId(''); reloadSaved(); })}>{t('gp.an.delete')}</button>
            </>}
          </div>
          <Flash m={msg} />
        </div>
      </section>
      <section className="mx-box"><h3>{t('gp.an.result')} {res?.currency && <small>{res.currency}</small>} {res?.truncated && <small className="red">{t('gp.an.truncated')}</small>}</h3>
        {pivot && sp.chart !== 'table' && pivot.rows.length > 0 && <Chart kind={sp.chart} rows={pivot.rows.slice(0, 30).map((r) => ({ label: r.label.map((l, i) => keyLabel(t, sp.rows[i], l)).join(' / '), v: r.total }))} dec={dec} />}
        {pivot ? <DataGrid rows={pivot.rows} cols={pcols} rowKey={(r) => r.key} footer title={`${t(`gp.an.s.${sp.source}`)} · ${t(`gp.an.m.${sp.measure}`, { defaultValue: sp.measure })}`}
          empty={t('gp.an.empty')} /> : <p style={{ padding: 12, opacity: 0.7 }}>{t('gp.an.hint')}</p>}
      </section>
    </div>
  );
}

const PALETTE = ['#2563eb', '#16a34a', '#d97706', '#dc2626', '#7c3aed', '#0891b2', '#db2777', '#65a30d', '#ea580c', '#4f46e5'];

/** A small SVG chart of the row totals (bar / line / pie). */
function Chart({ kind, rows, dec }: { kind: string; rows: { label: string; v: number }[]; dec: number }) {
  const W = 640, H = 220, P = 30;
  const max = Math.max(1, ...rows.map((r) => Math.abs(r.v)));
  if (kind === 'pie') {
    const total = rows.reduce((s, r) => s + Math.max(0, r.v), 0) || 1;
    let a = -Math.PI / 2;
    return (
      <div className="mx-row" style={{ gap: 12, padding: 8, flexWrap: 'wrap', alignItems: 'center' }}>
        <svg viewBox="0 0 200 200" width={180} height={180} role="img" aria-label="pie">
          {rows.map((r, i) => {
            const frac = Math.max(0, r.v) / total; const a2 = a + frac * 2 * Math.PI;
            const large = frac > 0.5 ? 1 : 0;
            const d = frac >= 0.999 ? 'M100,10 A90,90 0 1,1 99.9,10 Z'
              : `M100,100 L${100 + 90 * Math.cos(a)},${100 + 90 * Math.sin(a)} A90,90 0 ${large},1 ${100 + 90 * Math.cos(a2)},${100 + 90 * Math.sin(a2)} Z`;
            a = a2;
            return <path key={i} d={d} fill={PALETTE[i % PALETTE.length]}><title>{`${r.label}: ${formatAmount(r.v, dec)}`}</title></path>;
          })}
        </svg>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 2, fontSize: 12 }}>
          {rows.slice(0, 12).map((r, i) => <span key={i}><span style={{ display: 'inline-block', width: 10, height: 10, background: PALETTE[i % PALETTE.length], marginInlineEnd: 4 }} />
            <bdi>{r.label}</bdi> · <bdi dir="ltr">{formatAmount(r.v, dec)}</bdi> ({Math.round((Math.max(0, r.v) / total) * 100)}%)</span>)}
        </div>
      </div>
    );
  }
  const bw = (W - 2 * P) / Math.max(1, rows.length);
  const yOf = (v: number) => H - P - (Math.abs(v) / max) * (H - 2 * P);
  return (
    <div style={{ padding: 8, overflowX: 'auto' }} dir="ltr">
      <svg viewBox={`0 0 ${W} ${H}`} style={{ width: '100%', maxWidth: W, height: 'auto' }} role="img" aria-label="chart">
        <line x1={P} y1={H - P} x2={W - P} y2={H - P} stroke="#9aa5b1" />
        {kind === 'bar' && rows.map((r, i) => (
          <rect key={i} x={P + i * bw + bw * 0.1} y={yOf(r.v)} width={bw * 0.8} height={H - P - yOf(r.v)} fill={r.v < 0 ? '#dc2626' : '#2563eb'} rx={2}>
            <title>{`${r.label}: ${formatAmount(r.v, dec)}`}</title></rect>
        ))}
        {kind === 'line' && <polyline fill="none" stroke="#2563eb" strokeWidth={2} points={rows.map((r, i) => `${P + i * bw + bw / 2},${yOf(r.v)}`).join(' ')} />}
        {kind === 'line' && rows.map((r, i) => <circle key={i} cx={P + i * bw + bw / 2} cy={yOf(r.v)} r={3} fill="#2563eb"><title>{`${r.label}: ${formatAmount(r.v, dec)}`}</title></circle>)}
        {rows.length <= 16 && rows.map((r, i) => (
          <text key={`t${i}`} x={P + i * bw + bw / 2} y={H - P + 12} fontSize={9} textAnchor="middle" fill="#52606d">{r.label.length > 12 ? `${r.label.slice(0, 11)}…` : r.label}</text>
        ))}
        <text x={P} y={P - 8} fontSize={10} fill="#52606d">{formatAmount(max, dec)}</text>
      </svg>
    </div>
  );
}

// ======================= repairs =======================

interface Repair {
  id: string; number: number; customer_id: string; customer_code: string; customer_name: string; customer_phone: string | null; item_id: string | null;
  item_name: string | null; device: string; serial: string; accessories: string; fault: string; diagnosis: string; status: string; sale_date: string | null;
  warranty_until: string | null; under_warranty: boolean; promised_date: string | null; currency: string; notify: boolean; invoice_id: string | null;
  invoice_number: number | null; invoice_status: string | null; assigned_to: string | null; assigned_name: string | null; received_at: string;
  to_charge: string; late: boolean;
}
interface RLine { kind: string; item_id: string | null; warehouse_id: string | null; description: string; quantity: string; unit_price: string; covered: boolean }
interface ItemRow { id: string; code: string; name: string; kind: string; sale_price: string | null; is_active: boolean }
const REPAIR_STATUSES = ['received', 'diagnosing', 'waiting_parts', 'repaired', 'delivered', 'cancelled'];

function Repairs({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const { decimalsOf } = useCompany();
  const [filter, setFilter] = useState('open');
  const [q, setQ] = useState('');
  const [rows, reload] = useLoad<Repair[]>(base && `${base}/repairs?status=${filter}&q=${encodeURIComponent(q.trim())}`, []);
  const [items] = useLoad<ItemRow[]>(base && `${base}/items`, []);
  const [people] = useLoad<Person[]>(base && `${base}/mentionable`, []);
  const [sel, setSel] = useState<{ repair: Repair; lines: RLine[] } | null>(null);
  const [lines, setLines] = useState<RLine[]>([]);
  const [nw, setNw] = useState({ customer_id: '', device: '', serial: '', item_id: '', sale_date: '', accessories: '', fault: '', promised_date: '' });
  const [wInfo, setWInfo] = useState<string>('');
  const [msg, run] = useRun(reload);
  const canCreate = can(me, 'service.create');
  const canEdit = can(me, 'service.edit');
  const open = useCallback((id: string) => {
    if (base) api<{ repair: Repair; lines: RLine[] }>(`${base}/repairs/${id}`).then((x) => { setSel(x); setLines(x.lines.map((l) => ({ ...l, quantity: String(Number(l.quantity)), unit_price: String(Number(l.unit_price)) }))); }).catch(() => setSel(null));
  }, [base]);
  function patch(body: Record<string, unknown>) {
    if (base && sel) void run(api(`${base}/repairs/${sel.repair.id}`, { method: 'PATCH', body }), () => open(sel.repair.id));
  }
  async function checkSerial() {
    if (!base || !nw.serial.trim()) return;
    try {
      const w = await api<{ found: boolean; item_name?: string; item_id?: string; sale_date?: string; warranty_until?: string; under_warranty?: boolean; repairs: unknown[] }>(
        `${base}/repairs/warranty?serial=${encodeURIComponent(nw.serial.trim())}`);
      if (w.found) {
        setNw((x) => ({ ...x, item_id: w.item_id ?? x.item_id, sale_date: w.sale_date ?? x.sale_date }));
        setWInfo(t('gp.rep.found', { item: w.item_name, date: w.sale_date ? formatDate(w.sale_date) : '—', until: w.warranty_until ? formatDate(w.warranty_until) : '—' })
          + (w.under_warranty ? ` · ${t('gp.rep.inWarranty')}` : '') + (w.repairs.length ? ` · ${t('gp.rep.repairsBefore', { n: w.repairs.length })}` : ''));
      } else setWInfo(t('gp.rep.notFound'));
    } catch (e) { setWInfo(errorText(t, e)); }
  }
  const cols: Col<Repair>[] = [
    { key: 'n', title: '#', get: (r) => r.number, num: true, decimals: 0, width: 55 },
    { key: 'st', title: t('gp.rep.status'), get: (r) => t(`gp.rep.s.${r.status}`), width: 100, cls: (r) => (r.late ? 'red' : '') },
    { key: 'cust', title: t('gp.customer'), get: (r) => `${r.customer_code} ${r.customer_name}`, width: 170 },
    { key: 'dev', title: t('gp.rep.device'), get: (r) => r.device || r.item_name || '', width: 150 },
    { key: 'sn', title: t('gp.rep.serial'), get: (r) => r.serial, width: 110 },
    { key: 'w', title: t('gp.rep.warranty'), get: (r) => (r.under_warranty ? '✓' : ''), width: 70 },
    { key: 'fault', title: t('gp.rep.fault'), get: (r) => r.fault, width: 200 },
    { key: 'due', title: t('gp.rep.promised'), get: (r) => r.promised_date ?? '', render: (r) => (r.promised_date ? formatDate(r.promised_date) : ''), width: 95 },
    { key: 'amt', title: t('gp.rep.toCharge'), get: (r) => Number(r.to_charge), render: (r) => formatAmount(r.to_charge, decimalsOf(r.currency)), num: true, width: 90 },
    { key: 'at', title: t('gp.rep.received'), get: (r) => r.received_at, render: (r) => formatDate(r.received_at), width: 95 },
  ];
  const r = sel?.repair;
  const locked = !!r && (r.status === 'delivered' || r.status === 'cancelled' || (!!r.invoice_id && r.invoice_status !== 'void'));
  return (
    <div className="mx-body" style={{ gridTemplateColumns: 'minmax(0, 1fr) minmax(320px, 480px)' }}>
      <section className="mx-box"><h3>{t('gp.m.repairs')}</h3>
        <div className="mx-row" style={{ padding: 6, gap: 6, flexWrap: 'wrap' }}>
          <select value={filter} aria-label={t('gp.rep.status')} onChange={(e) => setFilter(e.currentTarget.value)}>
            <option value="open">{t('gp.inbox.open')}</option><option value="all">{t('gp.inbox.everything')}</option>
            {REPAIR_STATUSES.map((s) => <option key={s} value={s}>{t(`gp.rep.s.${s}`)}</option>)}</select>
          <input placeholder={t('gp.rep.search')} value={q} onChange={(e) => setQ(e.currentTarget.value)} />
        </div>
        <DataGrid rows={rows} cols={cols} rowKey={(x) => x.id} selected={r?.id ?? null} title={t('gp.m.repairs')} onSelect={(x) => open(x.id)} onActivate={(x) => open(x.id)} />
      </section>
      <section className="mx-box">
        {r ? (
          <div className="mx-pay" style={{ overflow: 'auto' }}>
            <div className="print-doc">
              <h3 style={{ margin: 0 }}>{t('gp.rep.title', { n: r.number })} · <bdi>{r.device || r.item_name}</bdi></h3>
              <small><bdi>{r.customer_code} {r.customer_name}</bdi>{r.customer_phone ? <> · <bdi dir="ltr">{r.customer_phone}</bdi></> : null} · {formatDateTime(r.received_at)}</small>
              <div>{t('gp.rep.serial')}: <bdi dir="ltr">{r.serial || '—'}</bdi>{r.accessories ? <> · {t('gp.rep.accessories')}: <bdi>{r.accessories}</bdi></> : null}</div>
              <div>{t('gp.rep.fault')}: <bdi>{r.fault}</bdi></div>
              {r.warranty_until && <div className={r.under_warranty ? 'ok' : 'red'}>{t('gp.rep.warrantyUntil', { date: formatDate(r.warranty_until) })} {r.under_warranty ? `· ${t('gp.rep.inWarranty')}` : `· ${t('gp.rep.outWarranty')}`}</div>}
              {r.promised_date && <div>{t('gp.rep.promised')}: {formatDate(r.promised_date)}</div>}
              <small className="print-only">{t('gp.rep.receiptNote')}</small>
            </div>
            <label>{t('gp.rep.status')}<select disabled={!canEdit} value={r.status} onChange={(e) => patch({ status: e.currentTarget.value })}>
              {REPAIR_STATUSES.map((s) => <option key={s} value={s}>{t(`gp.rep.s.${s}`)}</option>)}</select></label>
            <label>{t('gp.rep.diagnosis')}<textarea rows={2} disabled={!canEdit} defaultValue={r.diagnosis} key={`d-${r.id}`}
              onBlur={(e) => { if (e.currentTarget.value !== r.diagnosis) patch({ diagnosis: e.currentTarget.value }); }} /></label>
            <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
              <label>{t('gp.rep.promised')}<input type="date" dir="ltr" disabled={!canEdit} defaultValue={r.promised_date ?? ''} key={`p-${r.id}`}
                onBlur={(e) => { if (e.currentTarget.value !== (r.promised_date ?? '')) patch({ promised_date: e.currentTarget.value }); }} /></label>
              <label>{t('gp.rep.tech')}<select disabled={!canEdit} value={r.assigned_to ?? ''} onChange={(e) => patch({ assigned_to: e.currentTarget.value })}>
                <option value="">—</option>{people.map((p) => <option key={p.id} value={p.id}>{p.full_name}</option>)}</select></label>
              <label className="mx-row" style={{ gap: 4 }}><input type="checkbox" disabled={!canEdit} checked={r.notify} onChange={(e) => patch({ notify: e.currentTarget.checked })} />{t('gp.rep.notify')}</label>
            </div>
            <h4 style={{ margin: '6px 0 2px' }}>{t('gp.rep.lines')}</h4>
            <div style={{ overflowX: 'auto', flex: 'none' }}>
              <table className="mx-table">
                <thead><tr><th>{t('gp.rep.kind')}</th><th>{t('gp.rep.item')}</th><th>{t('gp.rep.desc')}</th><th>{t('gp.qty')}</th><th>{t('gp.price')}</th><th>{t('gp.rep.covered')}</th><th /></tr></thead>
                <tbody>{lines.map((l, i) => {
                  const up = (p: Partial<RLine>) => setLines((ls) => ls.map((x, j) => (j === i ? { ...x, ...p } : x)));
                  return (
                    <tr key={i}>
                      <td><select disabled={locked} value={l.kind} aria-label={t('gp.rep.kind')} onChange={(e) => up({ kind: e.currentTarget.value })}><option value="part">{t('gp.rep.part')}</option><option value="labour">{t('gp.rep.labour')}</option></select></td>
                      <td><select disabled={locked} value={l.item_id ?? ''} aria-label={t('gp.rep.item')} onChange={(e) => { const it = items.find((x) => x.id === e.currentTarget.value); up({ item_id: e.currentTarget.value || null, description: it?.name ?? l.description, unit_price: it?.sale_price ? String(Number(it.sale_price)) : l.unit_price }); }}>
                        <option value="">—</option>{items.filter((x) => x.is_active).map((x) => <option key={x.id} value={x.id}>{x.code} · {x.name}</option>)}</select></td>
                      <td><input disabled={locked} value={l.description} aria-label={t('gp.rep.desc')} onChange={(e) => up({ description: e.currentTarget.value })} /></td>
                      <td><input disabled={locked} dir="ltr" style={{ width: 60 }} value={l.quantity} aria-label={t('gp.qty')} onChange={(e) => up({ quantity: e.currentTarget.value })} /></td>
                      <td><input disabled={locked} dir="ltr" style={{ width: 80 }} value={l.unit_price} aria-label={t('gp.price')} onChange={(e) => up({ unit_price: e.currentTarget.value })} /></td>
                      <td><input type="checkbox" disabled={locked} checked={l.covered} aria-label={t('gp.rep.covered')} onChange={(e) => up({ covered: e.currentTarget.checked })} /></td>
                      <td>{!locked && <button className="mx-link" aria-label={t('gp.remove')} onClick={() => setLines((ls) => ls.filter((_, j) => j !== i))}>✕</button>}</td>
                    </tr>);
                })}</tbody>
              </table>
            </div>
            {!locked && canEdit && <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
              <button className="mx-btn sm" onClick={() => setLines([...lines, { kind: 'part', item_id: null, warehouse_id: null, description: '', quantity: '1', unit_price: '', covered: r.under_warranty }])}>{t('gp.rep.addPart')}</button>
              <button className="mx-btn sm" onClick={() => setLines([...lines, { kind: 'labour', item_id: null, warehouse_id: null, description: t('gp.rep.labour'), quantity: '1', unit_price: '', covered: r.under_warranty }])}>{t('gp.rep.addLabour')}</button>
              <button className="mx-btn sm main" onClick={() => base && void run(api(`${base}/repairs/${r.id}/lines`, { method: 'PUT', body: { lines: lines.map((l) => ({
                kind: l.kind, item_id: l.item_id ?? '', warehouse_id: l.warehouse_id ?? '', description: l.description, quantity: l.quantity, unit_price: l.unit_price || '0', covered: l.covered })) } }), () => open(r.id))}>{t('gp.rep.saveLines')}</button>
            </div>}
            <div>{t('gp.rep.toCharge')}: <b dir="ltr">{formatAmount(r.to_charge, decimalsOf(r.currency))} {r.currency}</b>
              {r.invoice_number && <> · {t('gp.rep.invoice', { n: r.invoice_number })} ({t(`invoices.statuses.${r.invoice_status}`, { defaultValue: r.invoice_status ?? '' })})</>}</div>
            <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
              {can(me, 'billing.create') && (!r.invoice_id || r.invoice_status === 'void') && r.status !== 'cancelled' && r.status !== 'delivered' &&
                <button className="mx-btn main" onClick={() => base && void run(api(`${base}/repairs/${r.id}/invoice`, { method: 'POST', body: {} }), () => open(r.id))}>{t('gp.rep.makeInvoice')}</button>}
              {canEdit && r.status === 'repaired' && <button className="mx-btn" onClick={() => patch({ status: 'delivered' })}>{t('gp.rep.deliver')}</button>}
              <button className="mx-btn sm" onClick={() => void printPage()}>{t('gp.rep.printReceipt')}</button>
              <button className="mx-btn sm" onClick={() => { setSel(null); setLines([]); }}>{t('gp.rep.new')}</button>
            </div>
            <Flash m={msg} />
            <Chatter type="repair" id={r.id} readOnly={!canEdit} />
          </div>
        ) : canCreate ? (
          <div className="mx-pay">
            <h3 style={{ margin: 0 }}>{t('gp.rep.new')}</h3>
            <label>{t('gp.customer')}<CustomerSearch value="" onPick={(c) => setNw({ ...nw, customer_id: c?.id ?? '' })} /></label>
            <label>{t('gp.rep.device')}<input value={nw.device} onChange={(e) => setNw({ ...nw, device: e.currentTarget.value })} /></label>
            <div className="mx-row" style={{ gap: 6, alignItems: 'end' }}>
              <label style={{ flex: 1 }}>{t('gp.rep.serial')}<input dir="ltr" value={nw.serial} onChange={(e) => setNw({ ...nw, serial: e.currentTarget.value })} /></label>
              <button className="mx-btn sm" disabled={!nw.serial.trim()} onClick={() => void checkSerial()}>{t('gp.rep.check')}</button>
            </div>
            {wInfo && <small>{wInfo}</small>}
            <label>{t('gp.rep.item')}<select value={nw.item_id} onChange={(e) => setNw({ ...nw, item_id: e.currentTarget.value })}>
              <option value="">—</option>{items.filter((x) => x.is_active).map((x) => <option key={x.id} value={x.id}>{x.code} · {x.name}</option>)}</select></label>
            <label>{t('gp.rep.saleDate')}<input type="date" dir="ltr" value={nw.sale_date} onChange={(e) => setNw({ ...nw, sale_date: e.currentTarget.value })} /></label>
            <label>{t('gp.rep.accessories')}<input value={nw.accessories} onChange={(e) => setNw({ ...nw, accessories: e.currentTarget.value })} /></label>
            <label>{t('gp.rep.fault')}<textarea rows={2} value={nw.fault} onChange={(e) => setNw({ ...nw, fault: e.currentTarget.value })} /></label>
            <label>{t('gp.rep.promised')}<input type="date" dir="ltr" value={nw.promised_date} onChange={(e) => setNw({ ...nw, promised_date: e.currentTarget.value })} /></label>
            <button className="mx-btn main" disabled={!nw.customer_id || !nw.fault.trim()} onClick={() => base && void run(api<{ repair: Repair }>(`${base}/repairs`, { method: 'POST', body: {
              customer_id: nw.customer_id, device: nw.device, serial: nw.serial, item_id: nw.item_id, sale_date: nw.sale_date, accessories: nw.accessories,
              fault: nw.fault, promised_date: nw.promised_date } }), (x) => { open((x as { repair: Repair }).repair.id); setWInfo(''); })}>{t('gp.rep.receive')}</button>
            <Flash m={msg} />
          </div>
        ) : <p style={{ padding: 12 }}>{t('gp.pick')}</p>}
      </section>
    </div>
  );
}

// ======================= instalments =======================

interface Plan {
  id: string; number: number; invoice_id: string; invoice_number: number; customer_id: string; customer_code: string; customer_name: string;
  customer_phone: string | null; currency: string; total: string; down_payment: string; months: number; fee: string; first_due: string; status: string;
  notes: string; paid: string; balance: string; next_due: string; issue_date: string; overdue: string;
}
interface PLine { seq: number; due_date: string; amount: string; paid: string; state: string }
interface OpenInv { id: string; number: number; total: string; balance: string; currency: string; issue_date: string }

function Instalments({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const { company, decimalsOf } = useCompany();
  const [status, setStatus] = useState('active');
  const [rows, reload] = useLoad<Plan[]>(base && `${base}/instalments?status=${status}`, []);
  const [sel, setSel] = useState<{ plan: Plan; lines: PLine[] } | null>(null);
  const [msg, run] = useRun(reload);
  const canCreate = can(me, 'billing.create');
  const [mode, setMode] = useState<'sale' | 'invoice'>('sale');
  const [nw, setNw] = useState({ customer_id: '', currency: company?.base_currency ?? 'USD', issue_date: today(), description: '', amount: '', down: '', months: '6', first_due: '', fee_percent: '', fee_amount: '', notes: '', invoice_id: '' });
  const [openInv, setOpenInv] = useState<OpenInv[]>([]);
  useEffect(() => {
    if (!base || !nw.customer_id || mode !== 'invoice') { setOpenInv([]); return; }
    api<{ rows: OpenInv[] }>(`${base}/invoices?customer_id=${nw.customer_id}&status=unpaid`).then((x) => setOpenInv(x.rows)).catch(() => setOpenInv([]));
  }, [base, nw.customer_id, mode]);
  const open = useCallback((id: string) => { if (base) api<{ plan: Plan; lines: PLine[] }>(`${base}/instalments/${id}`).then(setSel).catch(() => setSel(null)); }, [base]);
  const cols: Col<Plan>[] = [
    { key: 'n', title: '#', get: (r) => r.number, num: true, decimals: 0, width: 55 },
    { key: 'cust', title: t('gp.customer'), get: (r) => `${r.customer_code} ${r.customer_name}`, width: 180 },
    { key: 'inv', title: t('gp.inst.invoice'), get: (r) => r.invoice_number, num: true, decimals: 0, width: 70 },
    { key: 'tot', title: t('gp.inst.total'), get: (r) => Number(r.total), render: (r) => formatAmount(r.total, decimalsOf(r.currency)), num: true, width: 100 },
    { key: 'bal', title: t('gp.inst.balance'), get: (r) => Number(r.balance), render: (r) => formatAmount(r.balance, decimalsOf(r.currency)), num: true, width: 100, total: 'sum' },
    { key: 'late', title: t('gp.inst.overdue'), get: (r) => Number(r.overdue), render: (r) => formatAmount(r.overdue, decimalsOf(r.currency)), num: true, width: 100, cls: (r) => (Number(r.overdue) > 0 ? 'red' : '') },
    { key: 'next', title: t('gp.inst.next'), get: (r) => r.next_due, render: (r) => formatDate(r.next_due), width: 95 },
    { key: 'cur', title: t('gp.currency'), get: (r) => r.currency, width: 60 },
    { key: 'st', title: t('gp.inst.status'), get: (r) => t(`gp.inst.s.${r.status}`), width: 80 },
  ];
  function create() {
    if (!base || !nw.customer_id) return;
    const body: Record<string, unknown> = { months: Number(nw.months), down_payment: nw.down, first_due: nw.first_due, plan_notes: nw.notes };
    if (mode === 'invoice') body.invoice_id = nw.invoice_id;
    else Object.assign(body, { customer_id: nw.customer_id, currency: nw.currency, issue_date: nw.issue_date, fee_percent: nw.fee_percent, fee_amount: nw.fee_amount,
      lines: [{ description: nw.description, quantity: '1', unit_price: nw.amount }] });
    void run(api<{ plan: Plan; lines: PLine[] }>(`${base}/instalments`, { method: 'POST', body }), (x) => setSel(x as { plan: Plan; lines: PLine[] }));
  }
  const p = sel?.plan;
  const dec = p ? decimalsOf(p.currency) : 2;
  return (
    <div className="mx-body" style={{ gridTemplateColumns: 'minmax(0, 1fr) minmax(320px, 460px)' }}>
      <section className="mx-box"><h3>{t('gp.m.instalments')}</h3>
        <div className="mx-row" style={{ padding: 6, gap: 6 }}>
          <select value={status} aria-label={t('gp.inst.status')} onChange={(e) => setStatus(e.currentTarget.value)}>
            {['active', 'late', 'paid', 'cancelled'].map((s) => <option key={s} value={s}>{t(`gp.inst.s.${s}`)}</option>)}</select>
        </div>
        <DataGrid rows={rows} cols={cols} rowKey={(r) => r.id} selected={p?.id ?? null} title={t('gp.m.instalments')} footer onSelect={(r) => open(r.id)} onActivate={(r) => open(r.id)} />
      </section>
      <section className="mx-box">
        {p ? (
          <div className="mx-pay" style={{ overflow: 'auto' }}>
            <div className="print-doc">
              <h3 style={{ margin: 0 }}>{t('gp.inst.title', { n: p.number })}</h3>
              <div><bdi>{p.customer_code} {p.customer_name}</bdi>{p.customer_phone ? <> · <bdi dir="ltr">{p.customer_phone}</bdi></> : null}</div>
              <div>{t('gp.inst.invoice')} #{p.invoice_number} · {formatDate(p.issue_date)}</div>
              <div>{t('gp.inst.total')}: <b dir="ltr">{formatAmount(p.total, dec)} {p.currency}</b> · {t('gp.inst.down')}: <bdi dir="ltr">{formatAmount(p.down_payment, dec)}</bdi>
                {Number(p.fee) > 0 && <> · {t('gp.inst.fee')}: <bdi dir="ltr">{formatAmount(p.fee, dec)}</bdi></>}</div>
              <table className="mx-table" style={{ marginTop: 6 }}>
                <thead><tr><th>#</th><th>{t('gp.inst.due')}</th><th className="num">{t('gp.inst.amount')}</th><th className="num">{t('gp.inst.paid')}</th><th>{t('gp.inst.state')}</th></tr></thead>
                <tbody>{sel!.lines.map((l) => (
                  <tr key={l.seq} className={l.state === 'late' ? 'red' : ''}>
                    <td>{l.seq === 0 ? t('gp.inst.down') : l.seq}</td><td>{formatDate(l.due_date)}</td>
                    <td className="num" dir="ltr">{formatAmount(l.amount, dec)}</td><td className="num" dir="ltr">{formatAmount(l.paid, dec)}</td><td>{t(`gp.inst.ls.${l.state}`)}</td>
                  </tr>))}</tbody>
                <tfoot><tr><th colSpan={2}>{t('gp.inst.balance')}</th><th className="num" dir="ltr">{formatAmount(p.balance, dec)}</th><th colSpan={2} /></tr></tfoot>
              </table>
              {p.notes && <p style={{ whiteSpace: 'pre-wrap' }}>{p.notes}</p>}
              <div className="print-only" style={{ marginTop: 24, display: 'flex', justifyContent: 'space-between' }}><span>{t('gp.inst.signCustomer')}</span><span>{t('gp.inst.signCompany')}</span></div>
            </div>
            <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
              <button className="mx-btn sm" onClick={() => void printPage()}>{t('gp.inst.print')}</button>
              <a className="mx-btn sm" href={`#/customer/${p.customer_id}`}>{t('gp.openCustomer')}</a>
              {canCreate && p.status === 'active' && <>
                <button className="mx-btn sm" onClick={() => {
                  const w = can(me, 'billing.approve') && Number(p.fee) > 0 ? window.prompt(t('gp.inst.waiveQ', { fee: formatAmount(p.fee, dec) }), '0') : '0';
                  if (w === null || !base) return;
                  void run(api(`${base}/instalments/${p.id}/payoff`, { method: 'POST', body: { waive: w || '0' } }), () => open(p.id));
                }}>{t('gp.inst.payoff')}</button>
                <button className="mx-btn sm danger" onClick={() => base && window.confirm(t('gp.inst.cancelQ')) && void run(api(`${base}/instalments/${p.id}/cancel`, { method: 'POST', body: {} }), () => open(p.id))}>{t('gp.inst.cancel')}</button>
              </>}
              <button className="mx-btn sm" onClick={() => setSel(null)}>{t('gp.inst.new')}</button>
            </div>
            <Flash m={msg} />
            <Chatter type="instalment" id={p.id} readOnly={!canCreate} />
          </div>
        ) : canCreate ? (
          <div className="mx-pay">
            <h3 style={{ margin: 0 }}>{t('gp.inst.new')}</h3>
            <div className="mx-modes" role="tablist">
              <button className="mx-mode" role="tab" aria-selected={mode === 'sale'} onClick={() => setMode('sale')}>{t('gp.inst.fromSale')}</button>
              <button className="mx-mode" role="tab" aria-selected={mode === 'invoice'} onClick={() => setMode('invoice')}>{t('gp.inst.fromInvoice')}</button>
            </div>
            <label>{t('gp.customer')}<CustomerSearch value="" onPick={(c) => setNw({ ...nw, customer_id: c?.id ?? '', invoice_id: '' })} /></label>
            {mode === 'sale' ? <>
              <label>{t('gp.inst.what')}<input value={nw.description} onChange={(e) => setNw({ ...nw, description: e.currentTarget.value })} /></label>
              <div className="mx-row" style={{ gap: 6 }}>
                <label style={{ flex: 1 }}>{t('gp.inst.price')}<input dir="ltr" value={nw.amount} onChange={(e) => setNw({ ...nw, amount: e.currentTarget.value })} /></label>
                <label>{t('gp.currency')}<input dir="ltr" style={{ width: 70 }} value={nw.currency} onChange={(e) => setNw({ ...nw, currency: e.currentTarget.value.toUpperCase() })} /></label>
              </div>
              <label>{t('gp.inst.saleDate')}<input type="date" dir="ltr" value={nw.issue_date} onChange={(e) => setNw({ ...nw, issue_date: e.currentTarget.value })} /></label>
              <div className="mx-row" style={{ gap: 6 }}>
                <label style={{ flex: 1 }}>{t('gp.inst.feePct')}<input dir="ltr" value={nw.fee_percent} onChange={(e) => setNw({ ...nw, fee_percent: e.currentTarget.value })} /></label>
                <label style={{ flex: 1 }}>{t('gp.inst.feeAmt')}<input dir="ltr" value={nw.fee_amount} onChange={(e) => setNw({ ...nw, fee_amount: e.currentTarget.value })} /></label>
              </div>
            </> : (
              <label>{t('gp.inst.invoice')}<select value={nw.invoice_id} onChange={(e) => setNw({ ...nw, invoice_id: e.currentTarget.value })}>
                <option value="">—</option>{openInv.map((i) => <option key={i.id} value={i.id}>#{i.number} · {formatDate(i.issue_date)} · {formatAmount(i.balance, decimalsOf(i.currency))} {i.currency}</option>)}</select></label>
            )}
            <div className="mx-row" style={{ gap: 6 }}>
              <label style={{ flex: 1 }}>{t('gp.inst.down')}<input dir="ltr" value={nw.down} onChange={(e) => setNw({ ...nw, down: e.currentTarget.value })} /></label>
              <label style={{ flex: 1 }}>{t('gp.inst.months')}<input type="number" min={1} max={120} dir="ltr" value={nw.months} onChange={(e) => setNw({ ...nw, months: e.currentTarget.value })} /></label>
            </div>
            <label>{t('gp.inst.firstDue')}<input type="date" dir="ltr" value={nw.first_due} onChange={(e) => setNw({ ...nw, first_due: e.currentTarget.value })} /></label>
            <label>{t('gp.inst.notes')}<textarea rows={2} value={nw.notes} onChange={(e) => setNw({ ...nw, notes: e.currentTarget.value })} /></label>
            <button className="mx-btn main" disabled={!nw.customer_id || (mode === 'sale' ? !nw.description.trim() || !nw.amount : !nw.invoice_id)} onClick={create}>{t('gp.inst.create')}</button>
            <Flash m={msg} />
          </div>
        ) : <p style={{ padding: 12 }}>{t('gp.pick')}</p>}
      </section>
    </div>
  );
}

// ======================= quotation templates =======================

function Templates({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [rows, reload] = useLoad<QuoteTpl[]>(base && `${base}/quote-templates`, []);
  const [items] = useLoad<ItemRow[]>(base && `${base}/items`, []);
  const [sel, setSel] = useState<QuoteTpl | null>(null);
  const blank: QuoteTpl = { id: '', name: '', currency: null, validity_days: null, terms: '', notes: '', is_active: true, lines: [] };
  const [f, setF] = useState<QuoteTpl>(blank);
  const [msg, run] = useRun(reload);
  const canEdit = can(me, 'sales.create');
  const cols: Col<QuoteTpl>[] = [
    { key: 'name', title: t('gp.qt.name'), get: (r) => r.name, width: 240 },
    { key: 'lines', title: t('gp.qt.lines'), get: (r) => r.lines.length, num: true, decimals: 0, width: 70 },
    { key: 'cur', title: t('gp.currency'), get: (r) => r.currency ?? '', width: 70 },
    { key: 'val', title: t('gp.qt.validity'), get: (r) => r.validity_days ?? '', num: true, decimals: 0, width: 80 },
    { key: 'act', title: t('gp.active'), get: (r) => (r.is_active ? '✓' : ''), width: 60 },
  ];
  const up = (i: number, p: Partial<QuoteTpl['lines'][number]>) => setF({ ...f, lines: f.lines.map((x, j) => (j === i ? { ...x, ...p } : x)) });
  return (
    <div className="mx-body" style={{ gridTemplateColumns: 'minmax(0, 1fr) minmax(340px, 560px)' }}>
      <section className="mx-box"><h3>{t('gp.m.templates')}</h3>
        <DataGrid rows={rows} cols={cols} rowKey={(r) => r.id} selected={sel?.id ?? null} title={t('gp.m.templates')} onSelect={(r) => { setSel(r); setF(r); }} />
      </section>
      <section className="mx-box">
        <div className="mx-pay">
          <h3 style={{ margin: 0 }}>{sel ? sel.name : t('gp.qt.new')}</h3>
          <label>{t('gp.qt.name')}<input disabled={!canEdit} value={f.name} onChange={(e) => setF({ ...f, name: e.currentTarget.value })} /></label>
          <div className="mx-row" style={{ gap: 6 }}>
            <label>{t('gp.currency')}<input dir="ltr" style={{ width: 70 }} disabled={!canEdit} value={f.currency ?? ''} onChange={(e) => setF({ ...f, currency: e.currentTarget.value.toUpperCase() || null })} /></label>
            <label>{t('gp.qt.validity')}<input type="number" min={1} dir="ltr" style={{ width: 90 }} disabled={!canEdit} value={f.validity_days ?? ''} onChange={(e) => setF({ ...f, validity_days: e.currentTarget.value ? Number(e.currentTarget.value) : null })} /></label>
            <label className="mx-row" style={{ gap: 4 }}><input type="checkbox" disabled={!canEdit} checked={f.is_active} onChange={(e) => setF({ ...f, is_active: e.currentTarget.checked })} />{t('gp.active')}</label>
          </div>
          <table className="mx-table">
            <thead><tr><th>{t('gp.rep.item')}</th><th>{t('gp.rep.desc')}</th><th>{t('gp.qty')}</th><th>{t('gp.price')}</th><th /></tr></thead>
            <tbody>{f.lines.map((l, i) => (
              <tr key={i}>
                <td><select disabled={!canEdit} value={l.item_id} aria-label={t('gp.rep.item')} onChange={(e) => { const it = items.find((x) => x.id === e.currentTarget.value); up(i, { item_id: e.currentTarget.value, description: it?.name ?? l.description }); }}>
                  <option value="">—</option>{items.filter((x) => x.is_active).map((x) => <option key={x.id} value={x.id}>{x.code} · {x.name}</option>)}</select></td>
                <td><input disabled={!canEdit} value={l.description} aria-label={t('gp.rep.desc')} onChange={(e) => up(i, { description: e.currentTarget.value })} /></td>
                <td><input disabled={!canEdit} dir="ltr" style={{ width: 60 }} value={l.quantity} aria-label={t('gp.qty')} onChange={(e) => up(i, { quantity: e.currentTarget.value })} /></td>
                <td><input disabled={!canEdit} dir="ltr" style={{ width: 80 }} value={l.unit_price} placeholder={t('gp.qt.itemPrice')} aria-label={t('gp.price')} onChange={(e) => up(i, { unit_price: e.currentTarget.value })} /></td>
                <td>{canEdit && <button className="mx-link" aria-label={t('gp.remove')} onClick={() => setF({ ...f, lines: f.lines.filter((_, j) => j !== i) })}>✕</button>}</td>
              </tr>))}</tbody>
          </table>
          {canEdit && <button className="mx-btn sm" onClick={() => setF({ ...f, lines: [...f.lines, { item_id: '', description: '', quantity: '1', unit_price: '', discount_pct: '', tax_rate_id: '' }] })}>{t('gp.qt.addLine')}</button>}
          <label>{t('gp.qt.terms')}<textarea rows={3} disabled={!canEdit} value={f.terms} onChange={(e) => setF({ ...f, terms: e.currentTarget.value })} /></label>
          <label>{t('gp.qt.notes')}<textarea rows={2} disabled={!canEdit} value={f.notes} onChange={(e) => setF({ ...f, notes: e.currentTarget.value })} /></label>
          {canEdit && <div className="mx-row" style={{ gap: 6 }}>
            <button className="mx-btn main" disabled={!f.name.trim() || !base} onClick={() => base && void run(api<QuoteTpl>(sel ? `${base}/quote-templates/${sel.id}` : `${base}/quote-templates`, { method: sel ? 'PUT' : 'POST',
              body: { name: f.name, currency: f.currency ?? '', validity_days: f.validity_days, terms: f.terms, notes: f.notes, is_active: f.is_active,
                lines: f.lines.filter((l) => l.description.trim() || l.item_id) } }), (x) => { setSel(x as QuoteTpl); setF(x as QuoteTpl); })}>{t('common.save')}</button>
            <button className="mx-btn" onClick={() => { setSel(null); setF(blank); }}>{t('gp.qt.new')}</button>
          </div>}
          <small style={{ opacity: 0.75 }}>{t('gp.qt.hint')}</small>
          <Flash m={msg} />
        </div>
      </section>
    </div>
  );
}

// ======================= fiscal positions =======================

interface FPos { id: string; name: string; kind: string; note: string; tax_map: { from: string; to: string }[]; account_map: { from: string; to: string }[]; is_active: boolean; customers: number; suppliers: number }
interface Tax { id: string; name: string; rate: string }
interface Acc { id: string; code: string; name: string; is_postable: boolean; is_active: boolean }

function Fiscal({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [rows, reload] = useLoad<FPos[]>(base && `${base}/fiscal-positions`, []);
  const [taxes] = useLoad<Tax[]>(base && `${base}/tax-rates`, []);
  const [accs] = useLoad<Acc[]>(base && `${base}/accounts`, []);
  const blank: FPos = { id: '', name: '', kind: 'exempt', note: '', tax_map: [], account_map: [], is_active: true, customers: 0, suppliers: 0 };
  const [sel, setSel] = useState<FPos | null>(null);
  const [f, setF] = useState<FPos>(blank);
  const [msg, run] = useRun(reload);
  const canEdit = can(me, 'accounting.manage');
  const post = accs.filter((a) => a.is_postable && a.is_active);
  const cols: Col<FPos>[] = [
    { key: 'name', title: t('gp.fp.name'), get: (r) => r.name, width: 200 },
    { key: 'kind', title: t('gp.fp.kind'), get: (r) => t(`gp.fp.k.${r.kind}`), width: 120 },
    { key: 'tx', title: t('gp.fp.taxes'), get: (r) => r.tax_map.length, num: true, decimals: 0, width: 70 },
    { key: 'ac', title: t('gp.fp.accounts'), get: (r) => r.account_map.length, num: true, decimals: 0, width: 70 },
    { key: 'c', title: t('gp.fp.customers'), get: (r) => r.customers, num: true, decimals: 0, width: 80 },
    { key: 's', title: t('gp.fp.suppliers'), get: (r) => r.suppliers, num: true, decimals: 0, width: 80 },
    { key: 'a', title: t('gp.active'), get: (r) => (r.is_active ? '✓' : ''), width: 60 },
  ];
  return (
    <div className="mx-body" style={{ gridTemplateColumns: 'minmax(0, 1fr) minmax(340px, 520px)' }}>
      <section className="mx-box"><h3>{t('gp.m.fiscal')}</h3>
        <DataGrid rows={rows} cols={cols} rowKey={(r) => r.id} selected={sel?.id ?? null} title={t('gp.m.fiscal')} onSelect={(r) => { setSel(r); setF(r); }} />
      </section>
      <section className="mx-box">
        <div className="mx-pay">
          <h3 style={{ margin: 0 }}>{sel ? sel.name : t('gp.fp.new')}</h3>
          <label>{t('gp.fp.name')}<input disabled={!canEdit} value={f.name} onChange={(e) => setF({ ...f, name: e.currentTarget.value })} /></label>
          <label>{t('gp.fp.kind')}<select disabled={!canEdit} value={f.kind} onChange={(e) => setF({ ...f, kind: e.currentTarget.value })}>
            {['exempt', 'export', 'reverse_charge', 'other'].map((k) => <option key={k} value={k}>{t(`gp.fp.k.${k}`)}</option>)}</select></label>
          <h4 style={{ margin: '6px 0 2px' }}>{t('gp.fp.taxes')}</h4>
          {f.tax_map.map((m, i) => (
            <div key={i} className="mx-row" style={{ gap: 4, flexWrap: 'wrap' }}>
              <select disabled={!canEdit} value={m.from} aria-label={t('gp.fp.from')} onChange={(e) => setF({ ...f, tax_map: f.tax_map.map((x, j) => (j === i ? { ...x, from: e.currentTarget.value } : x)) })}>
                <option value="">—</option><option value="*">{t('gp.fp.anyTax')}</option>{taxes.map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}</select>
              <span>→</span>
              <select disabled={!canEdit} value={m.to} aria-label={t('gp.fp.to')} onChange={(e) => setF({ ...f, tax_map: f.tax_map.map((x, j) => (j === i ? { ...x, to: e.currentTarget.value } : x)) })}>
                <option value="">{t('gp.fp.noTax')}</option>{taxes.map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}</select>
              {canEdit && <button className="mx-link" aria-label={t('gp.remove')} onClick={() => setF({ ...f, tax_map: f.tax_map.filter((_, j) => j !== i) })}>✕</button>}
            </div>
          ))}
          {canEdit && <button className="mx-btn sm" onClick={() => setF({ ...f, tax_map: [...f.tax_map, { from: '', to: '' }] })}>{t('gp.fp.addTax')}</button>}
          <h4 style={{ margin: '6px 0 2px' }}>{t('gp.fp.accounts')}</h4>
          {f.account_map.map((m, i) => (
            <div key={i} className="mx-row" style={{ gap: 4, flexWrap: 'wrap' }}>
              <select disabled={!canEdit} value={m.from} aria-label={t('gp.fp.from')} style={{ maxWidth: 200 }} onChange={(e) => setF({ ...f, account_map: f.account_map.map((x, j) => (j === i ? { ...x, from: e.currentTarget.value } : x)) })}>
                <option value="">—</option>{post.map((a) => <option key={a.id} value={a.id}>{a.code} · {a.name}</option>)}</select>
              <span>→</span>
              <select disabled={!canEdit} value={m.to} aria-label={t('gp.fp.to')} style={{ maxWidth: 200 }} onChange={(e) => setF({ ...f, account_map: f.account_map.map((x, j) => (j === i ? { ...x, to: e.currentTarget.value } : x)) })}>
                <option value="">—</option>{post.map((a) => <option key={a.id} value={a.id}>{a.code} · {a.name}</option>)}</select>
              {canEdit && <button className="mx-link" aria-label={t('gp.remove')} onClick={() => setF({ ...f, account_map: f.account_map.filter((_, j) => j !== i) })}>✕</button>}
            </div>
          ))}
          {canEdit && <button className="mx-btn sm" onClick={() => setF({ ...f, account_map: [...f.account_map, { from: '', to: '' }] })}>{t('gp.fp.addAccount')}</button>}
          <label>{t('gp.fp.note')}<textarea rows={2} disabled={!canEdit} value={f.note} onChange={(e) => setF({ ...f, note: e.currentTarget.value })} /></label>
          <label className="mx-row" style={{ gap: 4 }}><input type="checkbox" disabled={!canEdit} checked={f.is_active} onChange={(e) => setF({ ...f, is_active: e.currentTarget.checked })} />{t('gp.active')}</label>
          {canEdit && <div className="mx-row" style={{ gap: 6 }}>
            <button className="mx-btn main" disabled={!f.name.trim()} onClick={() => base && void run(api<FPos>(sel ? `${base}/fiscal-positions/${sel.id}` : `${base}/fiscal-positions`, { method: sel ? 'PUT' : 'POST',
              body: { name: f.name, kind: f.kind, note: f.note, is_active: f.is_active, tax_map: f.tax_map.filter((m) => m.from), account_map: f.account_map.filter((m) => m.from && m.to) } }),
              (x) => { const v = x as FPos; setSel({ ...f, ...v }); setF({ ...f, ...v }); })}>{t('common.save')}</button>
            <button className="mx-btn" onClick={() => { setSel(null); setF(blank); }}>{t('gp.fp.new')}</button>
          </div>}
          <small style={{ opacity: 0.75 }}>{t('gp.fp.hint')}</small>
          <Flash m={msg} />
        </div>
      </section>
    </div>
  );
}

// ======================= credit control =======================

interface CRow { customer_id: string; code: string; name: string; over_limit: boolean; too_late: boolean;
  credit: { limit: string | null; exposure: string; available: string | null; max_overdue_days: number; overdue_days: number | null; hold: boolean; base_currency: string } }

function Credit({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [s, reloadS] = useLoad<{ policy: string; overdue_days: number | null; base_currency: string } | null>(base && `${base}/credit-settings`, null);
  const [rows, reload] = useLoad<CRow[]>(base && `${base}/credit/report`, []);
  const [f, setF] = useState({ policy: 'off', days: '' });
  useEffect(() => { if (s) setF({ policy: s.policy, days: s.overdue_days === null ? '' : String(s.overdue_days) }); }, [s]);
  const [msg, run] = useRun(() => { reloadS(); reload(); });
  const canEdit = can(me, 'settings.manage');
  const cols: Col<CRow>[] = [
    { key: 'code', title: t('gp.code'), get: (r) => r.code, width: 80 },
    { key: 'name', title: t('gp.customer'), get: (r) => r.name, width: 200 },
    { key: 'lim', title: t('gp.credit.limit'), get: (r) => (r.credit.limit === null ? '' : Number(r.credit.limit)), num: true, decimals: 2, width: 100 },
    { key: 'exp', title: t('gp.credit.exposure'), get: (r) => Number(r.credit.exposure), num: true, decimals: 2, width: 110, total: 'sum', cls: (r) => (r.over_limit ? 'red' : '') },
    { key: 'av', title: t('gp.credit.available'), get: (r) => (r.credit.available === null ? '' : Number(r.credit.available)), num: true, decimals: 2, width: 100 },
    { key: 'late', title: t('gp.credit.maxLate'), get: (r) => r.credit.max_overdue_days, num: true, decimals: 0, width: 90, cls: (r) => (r.too_late ? 'red' : '') },
    { key: 'rule', title: t('gp.credit.days'), get: (r) => r.credit.overdue_days ?? '', num: true, decimals: 0, width: 80 },
    { key: 'hold', title: t('gp.credit.hold'), get: (r) => (r.credit.hold ? '✓' : ''), width: 60 },
  ];
  return (
    <div className="mx-col" style={{ gap: 8 }}>
      <section className="mx-box"><h3>{t('gp.credit.settings')} <small>{s?.base_currency}</small></h3>
        <div className="mx-row" style={{ gap: 8, padding: 8, flexWrap: 'wrap', alignItems: 'end' }}>
          <label>{t('gp.credit.policyLabel')}<select disabled={!canEdit} value={f.policy} onChange={(e) => setF({ ...f, policy: e.currentTarget.value })}>
            {['off', 'warn', 'block'].map((p) => <option key={p} value={p}>{t(`gp.credit.policy.${p}`)}</option>)}</select></label>
          <label>{t('gp.credit.defaultDays')}<input type="number" min={0} dir="ltr" disabled={!canEdit} value={f.days} placeholder="—" onChange={(e) => setF({ ...f, days: e.currentTarget.value })} /></label>
          {canEdit && <button className="mx-btn main" onClick={() => base && void run(api(`${base}/credit-settings`, { method: 'PUT', body: { policy: f.policy, overdue_days: f.days === '' ? null : Number(f.days) } }))}>{t('common.save')}</button>}
          <small style={{ opacity: 0.8, maxWidth: 520 }}>{t('gp.credit.help')}</small>
          <Flash m={msg} />
        </div>
      </section>
      <section className="mx-box" style={{ flex: 1, minHeight: 300 }}><h3>{t('gp.credit.report')}</h3>
        <DataGrid rows={rows} cols={cols} rowKey={(r) => r.customer_id} title={t('gp.credit.report')} footer onActivate={(r) => { window.location.hash = `#/customer/${r.customer_id}`; }} empty={t('gp.credit.none')} />
      </section>
    </div>
  );
}
