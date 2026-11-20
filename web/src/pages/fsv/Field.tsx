import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, can, type Me } from '../../api';
import { useCompany } from '../../company';
import { errorText, formatAmount, formatDateTime, today } from '../../util';
import Chatter from '../../components/Chatter';
import { useBase, useLoad, useRun, type Warehouse } from '../wh/Wh';
import { SignaturePad, shrink } from '../wh/Driver';
import { CustomerPicker, usePeople } from '../prj/Prj';

// Field service (Magic-style screens): service jobs (from a ticket or a customer), the dispatch board (technicians ×
// days), helpdesk SLA (policies, report, inbound e-mail / WhatsApp addresses), and the technician's phone view.

type Msg = { ok: boolean; text: string } | null;
function Flash({ m }: { m: Msg }) {
  return m ? <div className={`mx-flash ${m.ok ? 'good' : 'bad'}`}>{m.text}</div> : null;
}

export interface Job {
  id: string; number: number; customer_id: string; customer_name: string; customer_code: string; ticket_id: string | null; ticket_number: number | null;
  project_id: string | null; project_name: string | null; title: string; description: string; address: string; phone: string; technician_id: string | null;
  technician_name: string | null; scheduled_start: string | null; duration_min: number; priority: string; status: string; warehouse_id: string | null;
  warehouse_name: string | null; currency: string; labour_rate: string; labour_hours: string | null; started_at: string | null; finished_at: string | null;
  report: string; signed_by: string; signed: boolean; lat: string | null; lng: string | null; invoice_id: string | null; invoice_number: number | null;
  invoiced: boolean; created_at: string; photos: number; late: boolean; customer_phone?: string;
}
interface Part { id: string; item_id: string; item_code: string; item_name: string; unit: string; warehouse_id: string | null; description: string; quantity: string; unit_price: string; amount: string }
interface CatalogItem { id: string; code: string; name: string; unit: string; kind: string; sale_price: string; barcode: string | null; on_hand: string }

const MODES = ['jobs', 'board', 'sla'] as const;
type Mode = typeof MODES[number];
const PRIOS = ['low', 'normal', 'high', 'urgent'];
const STATUSES = ['new', 'scheduled', 'in_progress', 'done', 'cancelled'];
const stColor: Record<string, string> = { new: '#b42318', scheduled: '#1d4ed8', in_progress: '#a15c00', done: '#0f7b4b', cancelled: '#6b7280' };
const amt = (v: string | number | null | undefined) => formatAmount(String(v ?? 0), 2);
const num = (s: string) => s.replace(/[^\d.]/g, '');
export const jobCode = (n: number) => `JOB${String(n).padStart(5, '0')}`;
const localInput = (iso: string | null) => {
  if (!iso) return '';
  const d = new Date(iso);
  const p = (x: number) => String(x).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
};

function Pill({ s }: { s: string }) {
  const { t } = useTranslation();
  return <span className="mx-pill" style={{ color: stColor[s], borderColor: stColor[s] }}>{t(`fsv.st.${s}`)}</span>;
}

export default function Field({ me, initial }: { me: Me; initial?: string }) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<Mode>((MODES as readonly string[]).includes(initial ?? '') ? initial as Mode : 'jobs');
  return (
    <div className="mx-col">
      <div className="mx-strip"><b>{t(`fsv.m.${mode}`)}</b><span className="mx-sp" /><small style={{ opacity: 0.8 }}>{t(`fsv.help.${mode}`)}</small>
        <a className="mx-btn sm" href="#/tech">{t('fsv.openTech')}</a></div>
      <div className="mx-modes" role="tablist">
        {MODES.map((m) => <button key={m} role="tab" className="mx-mode" aria-selected={mode === m} onClick={() => setMode(m)}>{t(`fsv.m.${m}`)}</button>)}
      </div>
      <div style={{ flex: 1, minHeight: 0, overflow: 'auto', padding: 6 }}>
        {mode === 'jobs' && <Jobs me={me} />}
        {mode === 'board' && <Board me={me} />}
        {mode === 'sla' && <Sla me={me} />}
      </div>
    </div>
  );
}

// ======================= jobs =======================

function Jobs({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const { company } = useCompany();
  const people = usePeople();
  const [status, setStatus] = useState('open');
  const [tech, setTech] = useState('');
  const [q, setQ] = useState('');
  const [rows, reload] = useLoad<Job[]>(base && `${base}/fs/service-jobs?status=${status}&technician_id=${tech}&q=${encodeURIComponent(q.trim())}`, []);
  const [whs] = useLoad<Warehouse[]>(base && `${base}/wh/warehouses`, []);
  const [sel, setSel] = useState<string | null>(null);
  const [cust, setCust] = useState({ id: '', text: '' });
  const [nf, setNf] = useState({ title: '', description: '', technician_id: '', scheduled_start: '', duration_min: '60', priority: 'normal', labour_rate: '', warehouse_id: '' });
  const [msg, run] = useRun(reload);
  return (
    <div className="mx-row" style={{ gap: 8, alignItems: 'flex-start', flexWrap: 'wrap' }}>
      <section className="mx-box" style={{ flex: '999 1 560px', minWidth: 0 }}>
        <h3>{t('fsv.m.jobs')}</h3>
        <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
          <label className="mx-fld">{t('fsv.status')}<select value={status} onChange={(e) => setStatus(e.currentTarget.value)}>
            {['open', ...STATUSES, 'all'].map((s) => <option key={s} value={s}>{t(`fsv.st.${s}`)}</option>)}</select></label>
          <label className="mx-fld">{t('fsv.technician')}<select value={tech} onChange={(e) => setTech(e.currentTarget.value)}>
            <option value="">{t('fsv.all')}</option>{people.map((u) => <option key={u.id} value={u.id}>{u.full_name}</option>)}</select></label>
          <label className="mx-fld">{t('fsv.search')}<input value={q} onChange={(e) => setQ(e.currentTarget.value)} /></label>
        </div>
        {can(me, 'service.create') && (
          <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end', borderTop: '1px solid var(--mx-line, #dde3ea)' }}>
            <CustomerPicker label={t('fsv.customer')} value={cust} onChange={setCust} />
            <label className="mx-fld">{t('fsv.title')}<input value={nf.title} onChange={(e) => setNf({ ...nf, title: e.currentTarget.value })} /></label>
            <label className="mx-fld">{t('fsv.technician')}<select value={nf.technician_id} onChange={(e) => setNf({ ...nf, technician_id: e.currentTarget.value })}>
              <option value="">—</option>{people.map((u) => <option key={u.id} value={u.id}>{u.full_name}</option>)}</select></label>
            <label className="mx-fld">{t('fsv.when')}<input type="datetime-local" dir="ltr" value={nf.scheduled_start} onChange={(e) => setNf({ ...nf, scheduled_start: e.currentTarget.value })} /></label>
            <label className="mx-fld">{t('fsv.duration')}<input dir="ltr" style={{ width: 60 }} value={nf.duration_min} onChange={(e) => setNf({ ...nf, duration_min: num(e.currentTarget.value) })} /></label>
            <label className="mx-fld">{t('fsv.priority')}<select value={nf.priority} onChange={(e) => setNf({ ...nf, priority: e.currentTarget.value })}>
              {PRIOS.map((p) => <option key={p} value={p}>{t(`fsv.prio.${p}`)}</option>)}</select></label>
            <label className="mx-fld">{t('fsv.labourRate')} ({company?.base_currency})<input dir="ltr" style={{ width: 70 }} value={nf.labour_rate} onChange={(e) => setNf({ ...nf, labour_rate: num(e.currentTarget.value) })} /></label>
            <label className="mx-fld">{t('fsv.van')}<select value={nf.warehouse_id} onChange={(e) => setNf({ ...nf, warehouse_id: e.currentTarget.value })}>
              <option value="">{t('fsv.mainWh')}</option>{whs.filter((w) => !w.is_transit).map((w) => <option key={w.id} value={w.id}>{w.name}</option>)}</select></label>
            <button className="mx-btn main" disabled={!cust.id || !nf.title.trim()} onClick={() => void run(api<{ job: Job }>(`${base}/fs/service-jobs`, { method: 'POST', body: {
              ...nf, customer_id: cust.id, duration_min: Number(nf.duration_min) || 60, scheduled_start: nf.scheduled_start ? new Date(nf.scheduled_start).toISOString() : undefined,
              technician_id: nf.technician_id || undefined, warehouse_id: nf.warehouse_id || undefined } }), (x) => { setSel((x as { job: Job }).job.id); setNf({ ...nf, title: '', description: '' }); })}>{t('fsv.create')}</button>
          </div>)}
        <Flash m={msg} />
        <div style={{ overflowX: 'auto' }}>
          <table className="mx-table">
            <thead><tr><th>#</th><th>{t('fsv.when')}</th><th>{t('fsv.customer')}</th><th>{t('fsv.title')}</th><th>{t('fsv.technician')}</th><th>{t('fsv.priority')}</th><th>{t('fsv.ticket')}</th><th>{t('fsv.status')}</th><th>{t('fsv.invoice')}</th></tr></thead>
            <tbody>
              {rows.map((j) => (
                <tr key={j.id} className={sel === j.id ? 'sel' : ''} style={{ cursor: 'pointer' }} onClick={() => setSel(j.id)}>
                  <td dir="ltr">{jobCode(j.number)}</td><td className={j.late ? 'red' : ''}>{j.scheduled_start ? formatDateTime(j.scheduled_start) : ''}</td>
                  <td>{j.customer_name}</td><td>{j.title}</td><td>{j.technician_name ?? ''}</td><td>{t(`fsv.prio.${j.priority}`)}</td>
                  <td dir="ltr">{j.ticket_number ? `#${j.ticket_number}` : ''}</td><td><Pill s={j.status} /></td><td>{j.invoiced ? `#${j.invoice_number}` : ''}</td>
                </tr>))}
              {rows.length === 0 && <tr><td colSpan={9} className="mx-empty">{t('fsv.none')}</td></tr>}
            </tbody>
          </table>
        </div>
      </section>
      {sel && <JobPanel me={me} id={sel} onChange={reload} onClose={() => setSel(null)} />}
    </div>
  );
}

function JobPanel({ me, id, onChange, onClose }: { me: Me; id: string; onChange: () => void; onClose: () => void }) {
  const { t } = useTranslation();
  const base = useBase();
  const people = usePeople();
  const [d, reload] = useLoad<{ job: Job; parts: Part[] } | null>(base && `${base}/fs/service-jobs/${id}`, null);
  const [msg, run] = useRun(() => { reload(); onChange(); });
  const [sig, setSig] = useState<string | null>(null);
  if (!d) return null;
  const j = d.job;
  const edit = can(me, 'service.edit') && !j.invoiced && j.status !== 'cancelled';
  const patch = (body: Record<string, unknown>) => void run(api(`${base}/fs/service-jobs/${id}`, { method: 'PATCH', body }));
  const act = (a: string, body: Record<string, unknown> = {}) => void run(api(`${base}/fs/service-jobs/${id}/${a}`, { method: 'POST', body }));
  const partsTotal = d.parts.reduce((s, p) => s + Number(p.amount), 0);
  const labour = Number(j.labour_hours ?? 0) * Number(j.labour_rate);
  return (
    <section className="mx-box" style={{ flex: '1 1 440px', minWidth: 0 }}>
      <h3 style={{ display: 'flex', gap: 6, alignItems: 'center', flexWrap: 'wrap' }}>
        <span dir="ltr">{jobCode(j.number)}</span> · {j.title} <Pill s={j.status} /><span className="mx-sp" /><button className="mx-btn sm" onClick={onClose}>{t('fsv.close')}</button>
      </h3>
      <div style={{ padding: 6, display: 'flex', flexDirection: 'column', gap: 6 }}>
        <small>{j.customer_code} {j.customer_name} · <span dir="ltr">{j.phone}</span>{j.address ? ` · ${j.address}` : ''}{j.ticket_number ? ` · ${t('fsv.ticket')} #${j.ticket_number}` : ''}</small>
        {j.description && <p style={{ whiteSpace: 'pre-wrap', margin: 0 }}>{j.description}</p>}
        <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap', alignItems: 'end' }}>
          <label className="mx-fld">{t('fsv.technician')}<select disabled={!edit} value={j.technician_id ?? ''} onChange={(e) => patch({ technician_id: e.currentTarget.value })}>
            <option value="">—</option>{people.map((u) => <option key={u.id} value={u.id}>{u.full_name}</option>)}</select></label>
          <label className="mx-fld">{t('fsv.when')}<input type="datetime-local" dir="ltr" disabled={!edit} defaultValue={localInput(j.scheduled_start)} key={`s-${j.scheduled_start}`}
            onBlur={(e) => { const v = e.currentTarget.value; if (v !== localInput(j.scheduled_start)) patch({ scheduled_start: v ? new Date(v).toISOString() : '' }); }} /></label>
          <label className="mx-fld">{t('fsv.priority')}<select disabled={!edit} value={j.priority} onChange={(e) => patch({ priority: e.currentTarget.value })}>
            {PRIOS.map((p) => <option key={p} value={p}>{t(`fsv.prio.${p}`)}</option>)}</select></label>
          <label className="mx-fld">{t('fsv.labourRate')}<input dir="ltr" style={{ width: 70 }} disabled={!edit} defaultValue={j.labour_rate} key={`r-${j.labour_rate}`} onBlur={(e) => patch({ labour_rate: num(e.currentTarget.value) || '0' })} /></label>
        </div>
        <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
          {['new', 'scheduled'].includes(j.status) && <button className="mx-btn" onClick={() => act('start')}>{t('fsv.act.start')}</button>}
          {['new', 'scheduled', 'in_progress'].includes(j.status) && <button className="mx-btn main" onClick={() => act('finish', sig ? { signature: sig } : {})}>{t('fsv.act.finish')}</button>}
          {can(me, 'service.edit') && !['done', 'cancelled'].includes(j.status) && <button className="mx-btn sm" onClick={() => act('cancel')}>{t('fsv.act.cancel')}</button>}
          {can(me, 'service.edit') && ['done', 'cancelled'].includes(j.status) && !j.invoiced && <button className="mx-btn sm" onClick={() => act('reopen')}>{t('fsv.act.reopen')}</button>}
          {can(me, 'billing.create') && j.status === 'done' && !j.invoiced && <button className="mx-btn main" onClick={() => act('invoice', { date: today() })}>{t('fsv.act.invoice')}</button>}
          {j.invoice_id && <a className="mx-btn sm" href={`#/invoices`}>{t('fsv.invoice')} #{j.invoice_number}</a>}
        </div>
        {j.status === 'in_progress' && <details><summary>{t('fsv.signature')}</summary><SignaturePad onChange={setSig} /></details>}
        <PartsEditor me={me} job={j} parts={d.parts} onSaved={() => { reload(); onChange(); }} />
        <small>{t('fsv.labour')}: {j.labour_hours ?? '—'}h × {amt(j.labour_rate)} = {amt(labour)} · {t('fsv.parts')}: {amt(partsTotal)} · {t('fsv.total')} {amt(labour + partsTotal)} {j.currency}</small>
        {j.started_at && <small>{t('fsv.started')} {formatDateTime(j.started_at)}{j.finished_at ? ` · ${t('fsv.finished')} ${formatDateTime(j.finished_at)}` : ''}{j.signed ? ` · ✍ ${j.signed_by}` : ''}
          {j.lat && j.lng ? <> · <a target="_blank" rel="noreferrer" href={`https://www.google.com/maps/search/?api=1&query=${j.lat},${j.lng}`}>{t('fsv.map')}</a></> : null}</small>}
        {j.report && <p style={{ whiteSpace: 'pre-wrap', margin: 0 }}><b>{t('fsv.report')}:</b> {j.report}</p>}
        <Flash m={msg} />
        <Chatter type="service_job" id={j.id} readOnly={!can(me, 'service.create')} />
      </div>
    </section>
  );
}

function PartsEditor({ me, job, parts, onSaved }: { me: Me; job: Job; parts: Part[]; onSaved: () => void }) {
  const { t } = useTranslation();
  const base = useBase();
  const [q, setQ] = useState('');
  const [cat, setCat] = useState<CatalogItem[]>([]);
  const [msg, run] = useRun(onSaved);
  const editable = !job.invoiced && job.status !== 'cancelled' && (can(me, 'service.edit') || job.technician_id === me.id);
  useEffect(() => {
    if (!base || q.trim().length < 2) { setCat([]); return; }
    const hd = window.setTimeout(() => api<CatalogItem[]>(`${base}/fs/parts-catalog?q=${encodeURIComponent(q.trim())}${job.warehouse_id ? `&warehouse_id=${job.warehouse_id}` : ''}`).then(setCat).catch(() => setCat([])), 250);
    return () => window.clearTimeout(hd);
  }, [base, q, job.warehouse_id]);
  const save = (list: { item_id: string; quantity: string; unit_price?: string }[]) => void run(api(`${base}/fs/service-jobs/${job.id}/parts`, { method: 'PUT', body: { parts: list } }));
  const cur = parts.map((p) => ({ item_id: p.item_id, quantity: p.quantity, unit_price: p.unit_price }));
  return (
    <div>
      <h4 style={{ margin: '6px 0 2px' }}>{t('fsv.parts')}</h4>
      <table className="mx-table"><tbody>
        {parts.map((p, i) => (
          <tr key={p.id}><td>{p.item_code} · {p.item_name}</td>
            <td className="num"><input dir="ltr" style={{ width: 60 }} disabled={!editable} defaultValue={String(Number(p.quantity))} aria-label={t('fsv.qty')}
              onBlur={(e) => { const v = num(e.currentTarget.value); if (v && v !== String(Number(p.quantity))) save(cur.map((x, j) => (j === i ? { ...x, quantity: v } : x))); }} /> {p.unit}</td>
            <td className="num">{amt(p.unit_price)}</td><td className="num">{amt(p.amount)}</td>
            <td>{editable && <button className="mx-btn sm" aria-label={t('fsv.remove')} onClick={() => save(cur.filter((_, j) => j !== i))}>×</button>}</td></tr>))}
        {parts.length === 0 && <tr><td className="mx-empty">{t('fsv.noParts')}</td></tr>}
      </tbody></table>
      {editable && (
        <div style={{ position: 'relative' }}>
          <input style={{ width: '100%', minHeight: 36 }} placeholder={t('fsv.findPart')} value={q} onChange={(e) => setQ(e.currentTarget.value)} />
          {cat.length > 0 && <div className="mx-list" style={{ background: '#fff', border: '1px solid #ccd' }}>
            {cat.map((c) => <button key={c.id} className="mx-link" style={{ display: 'block', width: '100%', textAlign: 'start', minHeight: 36 }}
              onClick={() => { save([...cur, { item_id: c.id, quantity: '1' }]); setQ(''); setCat([]); }}>{c.code} · {c.name} · {amt(c.sale_price)} · {t('fsv.onHand', { n: Number(c.on_hand) })}</button>)}
          </div>}
        </div>)}
      <Flash m={msg} />
    </div>
  );
}

// ======================= dispatch board =======================

function Board({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const base = useBase();
  const [from, setFrom] = useState(today());
  const days = useMemo(() => Array.from({ length: 7 }, (_, i) => { const d = new Date(`${from}T00:00:00`); d.setDate(d.getDate() + i); return today(d); }), [from]);
  const [b, reload] = useLoad<{ technicians: { id: string; full_name: string; open_jobs: number }[]; jobs: Job[]; unplanned: Job[] } | null>(base && `${base}/fs/board?from=${from}&to=${days[6]}`, null);
  const [msg, run] = useRun(reload);
  const [drag, setDrag] = useState<Job | null>(null);
  const [sel, setSel] = useState<string | null>(null);
  const edit = can(me, 'service.edit');
  const shift = (n: number) => { const d = new Date(`${from}T00:00:00`); d.setDate(d.getDate() + n); setFrom(today(d)); };
  const drop = (tech: string, day: string) => {
    if (!drag || !edit) return;
    const old = drag.scheduled_start ? new Date(drag.scheduled_start) : null;
    const at = new Date(`${day}T${old ? `${String(old.getHours()).padStart(2, '0')}:${String(old.getMinutes()).padStart(2, '0')}` : '09:00'}:00`);
    void run(api(`${base}/fs/service-jobs/${drag.id}`, { method: 'PATCH', body: { technician_id: tech, scheduled_start: at.toISOString() } }));
    setDrag(null);
  };
  const dayName = (d: string) => new Date(`${d}T00:00:00`).toLocaleDateString(i18n.language, { weekday: 'short', day: 'numeric', month: 'numeric' });
  const card = (j: Job) => (
    <div key={j.id} draggable={edit} onDragStart={() => setDrag(j)} onClick={() => setSel(j.id)}
      style={{ background: '#fff', border: '1px solid #dde3ea', borderInlineStart: `3px solid ${stColor[j.status]}`, borderRadius: 5, padding: 4, marginBottom: 4, cursor: 'pointer', fontSize: 12 }}>
      <b dir="ltr">{j.scheduled_start ? new Date(j.scheduled_start).toLocaleTimeString(i18n.language, { hour: '2-digit', minute: '2-digit' }) : ''}</b> {j.title}<br /><small>{j.customer_name}</small>
    </div>);
  if (!b) return null;
  return (
    <div>
      <div className="mx-row" style={{ gap: 6, marginBottom: 6, alignItems: 'center' }}>
        <button className="mx-btn sm" onClick={() => shift(-7)}>«</button>
        <input type="date" dir="ltr" aria-label={t('fsv.from')} value={from} onChange={(e) => setFrom(e.currentTarget.value)} />
        <button className="mx-btn sm" onClick={() => shift(7)}>»</button>
        <small style={{ opacity: 0.8 }}>{t('fsv.boardHint')}</small>
      </div>
      <Flash m={msg} />
      <div className="mx-row" style={{ gap: 8, alignItems: 'flex-start', flexWrap: 'wrap' }}>
        <section className="mx-box" style={{ flex: '1 1 200px', maxWidth: 260 }} onDragOver={(e) => e.preventDefault()}>
          <h3>{t('fsv.unplanned')} ({b.unplanned.length})</h3>
          <div style={{ padding: 4 }}>{b.unplanned.map(card)}</div>
        </section>
        <div style={{ flex: '999 1 600px', overflowX: 'auto' }}>
          <table className="mx-table" style={{ tableLayout: 'fixed', minWidth: 900 }}>
            <thead><tr><th style={{ width: 130 }}>{t('fsv.technician')}</th>{days.map((d) => <th key={d}>{dayName(d)}</th>)}</tr></thead>
            <tbody>
              {b.technicians.map((u) => (
                <tr key={u.id}>
                  <td><b>{u.full_name}</b><br /><small>{t('fsv.openN', { n: u.open_jobs })}</small></td>
                  {days.map((d) => (
                    <td key={d} style={{ verticalAlign: 'top', height: 70, background: d === today() ? '#fffbe8' : undefined }} onDragOver={(e) => { if (edit) e.preventDefault(); }} onDrop={() => drop(u.id, d)}>
                      {b.jobs.filter((j) => j.technician_id === u.id && j.scheduled_start && today(new Date(j.scheduled_start)) === d).map(card)}
                    </td>))}
                </tr>))}
            </tbody>
          </table>
        </div>
      </div>
      {sel && <div style={{ marginTop: 8 }}><JobPanel me={me} id={sel} onChange={reload} onClose={() => setSel(null)} /></div>}
    </div>
  );
}

// ======================= SLA =======================

interface Policy { id: string; name: string; priority: string | null; category: string | null; response_hours: string; resolution_hours: string; escalate_to: string | null; escalate_name: string | null; is_active: boolean; tickets: number }
interface Agg { name?: string; tickets: number; response_met: number; response_breached: number; resolution_met: number; resolution_breached: number; avg_response_hours: string | null; avg_resolution_hours: string | null; avg_rating: string | null; ratings: number }
const CATS = ['outage', 'no_internet', 'slow', 'no_signal', 'meter', 'billing', 'new_connection', 'move', 'disconnect', 'complaint', 'other'];

function Sla({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const people = usePeople();
  const [rows, reload] = useLoad<Policy[]>(base && `${base}/sla/sla-policies`, []);
  const [from, setFrom] = useState(() => { const d = new Date(); d.setDate(d.getDate() - 30); return today(d); });
  const [to, setTo] = useState(today());
  const [rep] = useLoad<{ total: Agg; by_policy: Agg[]; by_agent: Agg[]; breached_open: { id: string; number: number; subject: string; customer_name: string | null; assigned_name: string | null; resolution_due: string | null; priority: string }[] } | null>(
    base && `${base}/tickets/sla-report?from=${from}&to=${to}`, null);
  const [nf, setNf] = useState({ name: '', priority: '', category: '', response_hours: '4', resolution_hours: '24', escalate_to: '' });
  const [msg, run] = useRun(reload);
  const manage = can(me, 'service.manage');
  const pct = (a: number, b: number) => (a + b > 0 ? `${Math.round((a * 100) / (a + b))}%` : '—');
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      <section className="mx-box">
        <h3>{t('fsv.sla.policies')}</h3>
        {manage && (
          <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
            <label className="mx-fld">{t('fsv.sla.name')}<input value={nf.name} onChange={(e) => setNf({ ...nf, name: e.currentTarget.value })} /></label>
            <label className="mx-fld">{t('fsv.priority')}<select value={nf.priority} onChange={(e) => setNf({ ...nf, priority: e.currentTarget.value })}>
              <option value="">{t('fsv.sla.any')}</option>{PRIOS.map((p) => <option key={p} value={p}>{t(`fsv.prio.${p}`)}</option>)}</select></label>
            <label className="mx-fld">{t('fsv.sla.category')}<select value={nf.category} onChange={(e) => setNf({ ...nf, category: e.currentTarget.value })}>
              <option value="">{t('fsv.sla.any')}</option>{CATS.map((c) => <option key={c} value={c}>{t(`svc.cat.${c}`)}</option>)}</select></label>
            <label className="mx-fld">{t('fsv.sla.respondH')}<input dir="ltr" style={{ width: 60 }} value={nf.response_hours} onChange={(e) => setNf({ ...nf, response_hours: num(e.currentTarget.value) })} /></label>
            <label className="mx-fld">{t('fsv.sla.resolveH')}<input dir="ltr" style={{ width: 60 }} value={nf.resolution_hours} onChange={(e) => setNf({ ...nf, resolution_hours: num(e.currentTarget.value) })} /></label>
            <label className="mx-fld">{t('fsv.sla.escalate')}<select value={nf.escalate_to} onChange={(e) => setNf({ ...nf, escalate_to: e.currentTarget.value })}>
              <option value="">—</option>{people.map((u) => <option key={u.id} value={u.id}>{u.full_name}</option>)}</select></label>
            <button className="mx-btn main" disabled={!nf.name.trim() || !Number(nf.response_hours) || !Number(nf.resolution_hours)}
              onClick={() => void run(api(`${base}/sla/sla-policies`, { method: 'POST', body: nf }), () => setNf({ ...nf, name: '' }))}>{t('fsv.add')}</button>
          </div>)}
        <Flash m={msg} />
        <table className="mx-table">
          <thead><tr><th>{t('fsv.sla.name')}</th><th>{t('fsv.priority')}</th><th>{t('fsv.sla.category')}</th><th className="num">{t('fsv.sla.respondH')}</th><th className="num">{t('fsv.sla.resolveH')}</th><th>{t('fsv.sla.escalate')}</th><th className="num">{t('fsv.sla.tickets')}</th><th>{t('fsv.sla.active')}</th></tr></thead>
          <tbody>
            {rows.map((p) => (
              <tr key={p.id}><td>{p.name}</td><td>{p.priority ? t(`fsv.prio.${p.priority}`) : t('fsv.sla.any')}</td><td>{p.category ? t(`svc.cat.${p.category}`) : t('fsv.sla.any')}</td>
                <td className="num">{p.response_hours}</td><td className="num">{p.resolution_hours}</td><td>{p.escalate_name ?? ''}</td><td className="num">{p.tickets}</td>
                <td><input type="checkbox" aria-label={t('fsv.sla.active')} disabled={!manage} checked={p.is_active} onChange={(e) => void run(api(`${base}/sla/sla-policies/${p.id}`, { method: 'PATCH', body: { is_active: e.currentTarget.checked } }))} /></td></tr>))}
            {rows.length === 0 && <tr><td colSpan={8} className="mx-empty">{t('fsv.sla.nonePol')}</td></tr>}
          </tbody>
        </table>
      </section>
      {rep && (
        <section className="mx-box">
          <h3>{t('fsv.sla.report')}</h3>
          <div className="mx-row" style={{ gap: 6, padding: 6, alignItems: 'end' }}>
            <label className="mx-fld">{t('fsv.from')}<input type="date" dir="ltr" value={from} onChange={(e) => setFrom(e.currentTarget.value)} /></label>
            <label className="mx-fld">{t('fsv.to')}<input type="date" dir="ltr" value={to} onChange={(e) => setTo(e.currentTarget.value)} /></label>
          </div>
          <div style={{ overflowX: 'auto' }}>
            <table className="mx-table">
              <thead><tr><th /><th className="num">{t('fsv.sla.tickets')}</th><th className="num">{t('fsv.sla.respOk')}</th><th className="num">{t('fsv.sla.resOk')}</th><th className="num">{t('fsv.sla.avgResp')}</th><th className="num">{t('fsv.sla.avgRes')}</th><th className="num">{t('fsv.sla.rating')}</th></tr></thead>
              <tbody>
                {[{ ...rep.total, name: t('fsv.total') }, ...rep.by_policy.map((x) => ({ ...x, name: x.name || t('fsv.sla.noPolicy') })), ...rep.by_agent.map((x) => ({ ...x, name: `👤 ${x.name || t('fsv.sla.unassigned')}` }))].map((x, i) => (
                  <tr key={i}><td>{x.name}</td><td className="num">{x.tickets}</td><td className="num">{pct(x.response_met, x.response_breached)}</td><td className="num">{pct(x.resolution_met, x.resolution_breached)}</td>
                    <td className="num">{x.avg_response_hours ?? '—'}</td><td className="num">{x.avg_resolution_hours ?? '—'}</td><td className="num">{x.avg_rating ? `${x.avg_rating} (${x.ratings})` : '—'}</td></tr>))}
              </tbody>
            </table>
          </div>
          <h4 style={{ margin: '8px 6px 2px' }} className="red">{t('fsv.sla.breachedOpen', { n: rep.breached_open.length })}</h4>
          <table className="mx-table"><tbody>
            {rep.breached_open.map((x) => <tr key={x.id}><td dir="ltr">#{x.number}</td><td>{x.subject}</td><td>{x.customer_name ?? ''}</td><td>{x.assigned_name ?? ''}</td><td className="red">{x.resolution_due ? formatDateTime(x.resolution_due) : ''}</td></tr>)}
          </tbody></table>
        </section>)}
      {manage && <Inbound />}
    </div>
  );
}

function Inbound() {
  const { t } = useTranslation();
  const base = useBase();
  const [d, setD] = useState<{ enabled: boolean; email_url?: string; whatsapp_url?: string; verify_token?: string; whatsapp_signed?: boolean; email_signed?: boolean } | null>(null);
  const [wa, setWa] = useState('');
  const [mk, setMk] = useState('');
  const [msg, run] = useRun();
  useEffect(() => { if (base) api<typeof d>(`${base}/sla/inbound`).then(setD).catch(() => setD(null)); }, [base]);
  if (!d) return null;
  return (
    <section className="mx-box">
      <h3>{t('fsv.in.title')}</h3>
      <div style={{ padding: 6, display: 'flex', flexDirection: 'column', gap: 6 }}>
        <small>{t('fsv.in.help')}</small>
        {d.enabled ? <>
          <label className="mx-fld">{t('fsv.in.email')}<input dir="ltr" readOnly value={d.email_url} onFocus={(e) => e.currentTarget.select()} /></label>
          <label className="mx-fld">{t('fsv.in.whatsapp')}<input dir="ltr" readOnly value={d.whatsapp_url} onFocus={(e) => e.currentTarget.select()} /></label>
          <label className="mx-fld">{t('fsv.in.verify')}<input dir="ltr" readOnly value={d.verify_token} onFocus={(e) => e.currentTarget.select()} /></label>
          {/* audit P7-L1: with the provider's signing secret, only messages really sent by the provider are accepted */}
          <small>{t('fsv.in.sigHelp')}</small>
          <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap', alignItems: 'flex-end' }}>
            <label className="mx-fld">{t('fsv.in.waSecret')} {d.whatsapp_signed ? '✅' : ''}<input dir="ltr" type="password" autoComplete="off" value={wa} onChange={(e) => setWa(e.currentTarget.value)} /></label>
            <label className="mx-fld">{t('fsv.in.mailKey')} {d.email_signed ? '✅' : ''}<input dir="ltr" type="password" autoComplete="off" value={mk} onChange={(e) => setMk(e.currentTarget.value)} /></label>
            <button className="mx-btn sm" disabled={!wa && !mk} onClick={() => void run(api(`${base}/sla/inbound/rotate`, { method: 'POST',
              body: { keep_address: true, ...(wa ? { whatsapp_secret: wa } : {}), ...(mk ? { email_key: mk } : {}) } }), (x) => { setD(x as typeof d); setWa(''); setMk(''); })}>{t('fsv.in.saveSig')}</button>
            {(d.whatsapp_signed || d.email_signed) && <button className="mx-btn sm" onClick={() => void run(api(`${base}/sla/inbound/rotate`, { method: 'POST',
              body: { keep_address: true, whatsapp_secret: '', email_key: '' } }), (x) => setD(x as typeof d))}>{t('fsv.in.clearSig')}</button>}
          </div>
        </> : <small>{t('fsv.in.off')}</small>}
        <div className="mx-row" style={{ gap: 6 }}>
          <button className="mx-btn" onClick={() => void run(api(`${base}/sla/inbound/rotate`, { method: 'POST', body: { enabled: true } }), (x) => setD(x as typeof d))}>{d.enabled ? t('fsv.in.rotate') : t('fsv.in.enable')}</button>
          {d.enabled && <button className="mx-btn sm" onClick={() => void run(api(`${base}/sla/inbound/rotate`, { method: 'POST', body: { enabled: false } }), (x) => setD(x as typeof d))}>{t('fsv.in.disable')}</button>}
        </div>
        <Flash m={msg} />
      </div>
    </section>
  );
}

// ======================= technician phone view =======================

export function TechJobs({ me }: { me: Me }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const base = company ? `/companies/${company.id}` : null;
  const [jobs, setJobs] = useState<Job[]>([]);
  const [sel, setSel] = useState<string | null>(null);
  const [d, setD] = useState<{ job: Job; parts: Part[] } | null>(null);
  const [finish, setFinish] = useState(false);
  const [f, setF] = useState({ report: '', signed_by: '', signature: '', labour_hours: '' });
  const [photo, setPhoto] = useState<File | null>(null);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<Msg>(null);
  const load = useCallback(() => { if (base) api<Job[]>(`${base}/fs/my-jobs`).then(setJobs).catch(() => setJobs([])); }, [base]);
  const loadJob = useCallback((id: string) => { if (base) api<{ job: Job; parts: Part[] }>(`${base}/fs/service-jobs/${id}`).then(setD).catch(() => setD(null)); }, [base]);
  useEffect(load, [load]);
  useEffect(() => { if (sel) loadJob(sel); }, [sel, loadJob]);
  const where = async () => {
    try {
      const p = await new Promise<GeolocationPosition>((ok, bad) => navigator.geolocation ? navigator.geolocation.getCurrentPosition(ok, bad, { timeout: 4000, maximumAge: 60000 }) : bad(new Error('no geo')));
      return { lat: Math.round(p.coords.latitude * 1e6) / 1e6, lng: Math.round(p.coords.longitude * 1e6) / 1e6 };
    } catch { return {}; }
  };
  async function act(path: string, body: Record<string, unknown>) {
    if (!base || !d) return;
    setBusy(true); setMsg(null);
    try {
      await api(`${base}/fs/service-jobs/${d.job.id}/${path}`, { method: 'POST', body });
      if (path === 'finish' && photo) await api(`${base}/fs/service-jobs/${d.job.id}/photo`, { method: 'POST', body: { filename: photo.name || 'photo.jpg', data: await shrink(photo) } });
      setMsg({ ok: true, text: t('fsv.tech.saved') });
      setFinish(false); setPhoto(null);
      loadJob(d.job.id); load();
    } catch (e) { setMsg({ ok: false, text: errorText(t, e) }); } finally { setBusy(false); }
  }
  void me;
  const box: React.CSSProperties = { background: '#fff', borderRadius: 10, padding: 10, border: '1px solid #dde3ea' };
  if (sel && d) {
    const j = d.job;
    if (finish) return (
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        <div style={box}><b>{j.title}</b><div>{j.customer_name}</div></div>
        <label>{t('fsv.report')}<textarea rows={3} style={{ width: '100%' }} value={f.report} onChange={(e) => setF({ ...f, report: e.currentTarget.value })} /></label>
        <label>{t('fsv.tech.hours')}<input dir="ltr" inputMode="decimal" style={{ minHeight: 44, width: '100%' }} value={f.labour_hours} placeholder={t('fsv.tech.auto')} onChange={(e) => setF({ ...f, labour_hours: num(e.currentTarget.value) })} /></label>
        <label>{t('fsv.tech.signedBy')}<input style={{ minHeight: 44, width: '100%' }} value={f.signed_by} onChange={(e) => setF({ ...f, signed_by: e.currentTarget.value })} /></label>
        <SignaturePad onChange={(s) => setF((x) => ({ ...x, signature: s }))} />
        <label>{t('fsv.tech.photo')}<input type="file" accept="image/*" capture="environment" onChange={(e) => setPhoto(e.currentTarget.files?.[0] ?? null)} /></label>
        {msg && <div className={`mx-flash ${msg.ok ? 'good' : 'bad'}`}>{msg.text}</div>}
        <div className="mx-row" style={{ gap: 6 }}>
          <button className="mx-btn main" style={{ flex: 1, minHeight: 52 }} disabled={busy}
            onClick={() => void (async () => act('finish', { ...f, labour_hours: f.labour_hours || undefined, ...(await where()) }))()}>{t('fsv.tech.done')}</button>
          <button className="mx-btn" style={{ minHeight: 52 }} onClick={() => setFinish(false)}>{t('fsv.close')}</button>
        </div>
      </div>
    );
    return (
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        <div className="mx-row" style={{ gap: 6, alignItems: 'center' }}>
          <b style={{ flex: 1 }}>{jobCode(j.number)} · {j.title}</b><Pill s={j.status} />
          <button className="mx-btn sm" onClick={() => { setSel(null); setD(null); setMsg(null); }}>{t('fsv.close')}</button>
        </div>
        <div style={box}>
          <b>{j.customer_name}</b>{j.scheduled_start && <div>{formatDateTime(j.scheduled_start)}</div>}<div>{j.address}</div>
          {j.description && <p style={{ whiteSpace: 'pre-wrap', margin: '4px 0' }}>{j.description}</p>}
          <div className="mx-row" style={{ gap: 6, marginTop: 6, flexWrap: 'wrap' }}>
            {j.phone && <a className="mx-btn" style={{ minHeight: 44, display: 'inline-flex', alignItems: 'center' }} href={`tel:${j.phone}`} dir="ltr">{t('fsv.tech.call')} {j.phone}</a>}
            {j.address && <a className="mx-btn" style={{ minHeight: 44, display: 'inline-flex', alignItems: 'center' }} target="_blank" rel="noreferrer"
              href={`https://www.google.com/maps/search/?api=1&query=${encodeURIComponent(j.address)}`}>{t('fsv.map')}</a>}
          </div>
        </div>
        {msg && <div className={`mx-flash ${msg.ok ? 'good' : 'bad'}`}>{msg.text}</div>}
        {['new', 'scheduled'].includes(j.status) && <button className="mx-btn main" style={{ minHeight: 52 }} disabled={busy} onClick={() => void (async () => act('start', await where()))()}>▶ {t('fsv.act.start')}</button>}
        <div style={box}><PartsEditor me={me} job={j} parts={d.parts} onSaved={() => loadJob(j.id)} /></div>
        {['new', 'scheduled', 'in_progress'].includes(j.status) && <button className="mx-btn main" style={{ minHeight: 52 }} onClick={() => { setF({ report: j.report, signed_by: '', signature: '', labour_hours: '' }); setFinish(true); }}>✔ {t('fsv.act.finish')}</button>}
      </div>
    );
  }
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      {jobs.length === 0 && <div style={box}>{t('fsv.tech.none')}</div>}
      {jobs.map((j) => (
        <button key={j.id} className="mx-btn" style={{ ...box, textAlign: 'start', minHeight: 64, background: j.status === 'done' ? '#e8f6ec' : '#fff' }} onClick={() => setSel(j.id)}>
          <b>{j.title}</b> · {j.customer_name}<br />
          <small>{j.scheduled_start ? formatDateTime(j.scheduled_start) : t('fsv.unplanned')} · {t(`fsv.st.${j.status}`)} · {t(`fsv.prio.${j.priority}`)}</small>
        </button>))}
    </div>
  );
}

export function Tech({ me, onExit }: { me: Me; onExit: () => void }) {
  const { t, i18n } = useTranslation();
  return (
    <div dir={i18n.dir()} style={{ minHeight: '100vh', background: '#f3f5f8', display: 'flex', flexDirection: 'column' }}>
      <header style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '8px 10px', background: '#14324f', color: '#fff', paddingTop: 'calc(8px + env(safe-area-inset-top, 0px))' }}>
        <b style={{ flex: 1 }}>{t('fsv.tech.title')}</b>
        <button className="mx-btn sm" onClick={onExit}>{t('fsv.close')}</button>
      </header>
      <main style={{ padding: 8 }}><TechJobs me={me} /></main>
    </div>
  );
}
