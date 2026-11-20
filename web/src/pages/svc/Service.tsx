import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { api, can, type Me } from '../../api';
import { useCompany } from '../../company';
import DataGrid, { type Col } from '../../components/DataGrid';
import { errorText, formatAmount, formatDate, formatDateTime, today } from '../../util';
import { printPage } from '../../native';
import { Help } from '../acc/common';
import Chatter from '../../components/Chatter';

type Msg = { ok: boolean; text: string } | null;
function Flash({ m }: { m: Msg }) {
  return m ? <div className={`mx-flash ${m.ok ? 'good' : 'bad'}`}>{m.text}</div> : null;
}
function useList<T>(path: string | null): [T[], () => void] {
  const [rows, setRows] = useState<T[]>([]);
  const load = useCallback(() => {
    if (path) api<T[]>(path).then(setRows).catch(() => setRows([]));
  }, [path]);
  useEffect(load, [load]);
  return [rows, load];
}

const MODES = ['tickets', 'maintenance'] as const;
type Mode = typeof MODES[number];
const CATS = ['outage', 'no_internet', 'slow', 'no_signal', 'meter', 'billing', 'new_connection', 'move', 'disconnect', 'complaint', 'other'];
const PRIOS = ['low', 'normal', 'high', 'urgent'];
const STATUSES = ['new', 'in_progress', 'waiting', 'done', 'cancelled'];
const CHANNELS = ['phone', 'whatsapp', 'office', 'portal', 'field', 'other'];

/** Service desk: customer requests / complaints / faults, and generator maintenance plans. */
export default function Service({ me, initial }: { me: Me; initial?: string }) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<Mode>((MODES as readonly string[]).includes(initial ?? '') ? initial as Mode : 'tickets');
  return (
    <div className="mx-col">
      <div className="mx-strip"><b>{t('svc.title')}</b><Summary /><span className="mx-sp" /><Help topic={`svc.help.${mode}`} /></div>
      <div className="mx-modes" role="tablist">
        {MODES.map((m) => <button key={m} role="tab" className="mx-mode" aria-selected={mode === m} onClick={() => setMode(m)}>{t(`svc.m.${m}`)}</button>)}
      </div>
      {mode === 'tickets' && <Tickets me={me} />}
      {mode === 'maintenance' && <Maintenance me={me} />}
    </div>
  );
}

interface SummaryData { open: number; overdue: number; urgent: number; opened_30d: number; closed_30d: number; avg_hours_30d: string | null }
function Summary() {
  const { t } = useTranslation();
  const { company } = useCompany();
  const [s, setS] = useState<SummaryData | null>(null);
  useEffect(() => { if (company) api<SummaryData>(`/companies/${company.id}/tickets/summary`).then(setS).catch(() => setS(null)); }, [company]);
  if (!s) return null;
  return (
    <span className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
      <span className="mx-pill">{t('svc.sum.open', { n: s.open })}</span>
      {s.urgent > 0 && <span className="mx-pill warn">{t('svc.sum.urgent', { n: s.urgent })}</span>}
      {s.overdue > 0 && <span className="mx-pill warn">{t('svc.sum.overdue', { n: s.overdue })}</span>}
      <span className="mx-pill">{t('svc.sum.month', { opened: s.opened_30d, closed: s.closed_30d, h: s.avg_hours_30d ?? '—' })}</span>
    </span>
  );
}

interface Ticket {
  id: string; number: number; customer_id: string | null; customer_name: string | null; customer_code: string | null; category: string;
  priority: string; status: string; subject: string; description: string; contact_phone: string; channel: string; assigned_to: string | null;
  assigned_name: string | null; due_at: string | null; resolved_at: string | null; resolution: string; created_by_name: string | null;
  created_at: string; overdue: boolean; age_hours: string;
}
interface Note { id: string; body: string; created_by_name: string | null; created_at: string }
interface Cust { id: string; name: string; code: string; phone?: string }
interface UserRow { id: string; full_name: string; is_active: boolean }

function Tickets({ me }: { me: Me }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const base = company ? `/companies/${company.id}` : null;
  const [filter, setFilter] = useState('open');
  const [q, setQ] = useState('');
  const [rows, reload] = useList<Ticket>(base && `${base}/tickets?status=${filter}&q=${encodeURIComponent(q.trim())}`);
  const [sel, setSel] = useState<Ticket | null>(null);
  const [notes, setNotes] = useState<Note[]>([]);
  const [note, setNote] = useState('');
  const [msg, setMsg] = useState<Msg>(null);
  const [users] = useList<UserRow>(can(me, 'users.view') ? '/users' : null);
  const [nw, setNw] = useState({ subject: '', category: 'outage', priority: 'normal', channel: 'phone', contact_phone: '', description: '', customer_id: '', due_at: '' });
  const [cq, setCq] = useState('');
  const [custs, setCusts] = useState<Cust[]>([]);
  const canCreate = can(me, 'service.create');
  const canEdit = can(me, 'service.edit');

  useEffect(() => {
    if (!base || cq.trim().length < 2) { setCusts([]); return; }
    const h = window.setTimeout(() => api<Cust[]>(`${base}/customers?limit=15&q=${encodeURIComponent(cq.trim())}`).then(setCusts).catch(() => setCusts([])), 250);
    return () => window.clearTimeout(h);
  }, [base, cq]);

  const open = useCallback((r: Ticket) => {
    if (!base) return;
    api<{ ticket: Ticket; notes: Note[] }>(`${base}/tickets/${r.id}`).then((x) => { setSel(x.ticket); setNotes(x.notes); })
      .catch((e) => setMsg({ ok: false, text: errorText(t, e) }));
  }, [base, t]);

  async function run(p: Promise<unknown>, after?: () => void) {
    try { await p; setMsg({ ok: true, text: t('svc.saved') }); after?.(); reload(); } catch (e) { setMsg({ ok: false, text: errorText(t, e) }); }
  }
  function create() {
    if (!base || !nw.subject.trim()) return;
    const body: Record<string, string> = { ...nw };
    if (!body.customer_id) delete body.customer_id;
    if (!body.due_at) delete body.due_at; else body.due_at = new Date(body.due_at).toISOString();
    void run(api(`${base}/tickets`, { method: 'POST', body }), () => { setNw({ ...nw, subject: '', description: '', customer_id: '', contact_phone: '', due_at: '' }); setCq(''); });
  }
  function patch(body: Record<string, string>) {
    if (!base || !sel) return;
    void run(api(`${base}/tickets/${sel.id}`, { method: 'PATCH', body }), () => open(sel));
  }

  const cols: Col<Ticket>[] = [
    { key: 'n', title: '#', get: (r) => r.number, render: (r) => String(r.number), num: true, width: 50 },
    { key: 'prio', title: t('svc.priority'), get: (r) => t(`svc.prio.${r.priority}`), width: 80, cls: (r) => (r.priority === 'urgent' || r.priority === 'high' ? 'red' : '') },
    { key: 'subject', title: t('svc.subject'), get: (r) => r.subject, width: 220 },
    { key: 'cust', title: t('svc.customer'), get: (r) => (r.customer_name ? `${r.customer_code ?? ''} ${r.customer_name}` : r.contact_phone), width: 160 },
    { key: 'cat', title: t('svc.category'), get: (r) => t(`svc.cat.${r.category}`), width: 110 },
    { key: 'status', title: t('svc.status'), get: (r) => t(`svc.st.${r.status}`), width: 90, cls: (r) => (r.overdue ? 'red' : '') },
    { key: 'who', title: t('svc.assigned'), get: (r) => r.assigned_name ?? '', width: 110 },
    { key: 'age', title: t('svc.age'), get: (r) => Number(r.age_hours), render: (r) => ageText(t, Number(r.age_hours)), width: 70 },
    { key: 'at', title: t('svc.opened'), get: (r) => r.created_at, render: (r) => formatDateTime(r.created_at), width: 130 },
  ];

  return (
    <div className="mx-body" style={{ gridTemplateColumns: 'minmax(0, 1fr) minmax(300px, 460px)' }}>
      <section className="mx-box"><h3>{t('svc.m.tickets')}</h3>
        <div className="mx-row" style={{ padding: 6, gap: 6, flexWrap: 'wrap' }}>
          <select value={filter} onChange={(e) => setFilter(e.currentTarget.value)} aria-label={t('svc.status')}>
            <option value="open">{t('svc.f.open')}</option><option value="all">{t('svc.f.all')}</option>
            {STATUSES.map((s) => <option key={s} value={s}>{t(`svc.st.${s}`)}</option>)}
          </select>
          <input placeholder={t('svc.search')} value={q} onChange={(e) => setQ(e.currentTarget.value)} />
        </div>
        <DataGrid rows={rows} cols={cols} rowKey={(r) => r.id} selected={sel?.id ?? null} title={t('svc.m.tickets')}
          onSelect={(r) => { setMsg(null); open(r); }} onActivate={open} />
      </section>
      <section className="mx-box">
        {sel ? (
          <div className="mx-pay" style={{ overflow: 'auto' }}>
            <h3 style={{ margin: 0 }}>#{sel.number} · {sel.subject}</h3>
            <small>{sel.customer_name ? `${sel.customer_code ?? ''} ${sel.customer_name} · ` : ''}{sel.contact_phone} · {t(`svc.ch.${sel.channel}`)} · {formatDateTime(sel.created_at)}{sel.created_by_name ? ` · ${sel.created_by_name}` : ''}</small>
            {sel.description && <p style={{ whiteSpace: 'pre-wrap', margin: '4px 0' }}>{sel.description}</p>}
            <label>{t('svc.status')}<select disabled={!canEdit} value={sel.status} onChange={(e) => patch({ status: e.currentTarget.value })}>
              {STATUSES.map((s) => <option key={s} value={s}>{t(`svc.st.${s}`)}</option>)}</select></label>
            <label>{t('svc.priority')}<select disabled={!canEdit} value={sel.priority} onChange={(e) => patch({ priority: e.currentTarget.value })}>
              {PRIOS.map((s) => <option key={s} value={s}>{t(`svc.prio.${s}`)}</option>)}</select></label>
            <label>{t('svc.assigned')}<select disabled={!canEdit} value={sel.assigned_to ?? ''} onChange={(e) => patch({ assigned_to: e.currentTarget.value })}>
              <option value="">—</option>
              {users.length === 0 && <option value={me.id}>{me.full_name}</option>}
              {users.filter((u) => u.is_active).map((u) => <option key={u.id} value={u.id}>{u.full_name}</option>)}</select></label>
            <label>{t('svc.resolution')}<input disabled={!canEdit} defaultValue={sel.resolution} key={`res-${sel.id}`}
              onBlur={(e) => { if (e.currentTarget.value !== sel.resolution) patch({ resolution: e.currentTarget.value }); }} /></label>
            <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
              {canEdit && sel.status !== 'done' && <button className="mx-btn" onClick={() => patch({ status: 'done' })}>{t('svc.close')}</button>}
              {sel.customer_id && <a className="mx-btn sm" href={`#/customer/${sel.customer_id}`}>{t('svc.openCustomer')}</a>}
              <button className="mx-btn sm" onClick={() => printPage()}>{t('svc.print')}</button>
            </div>
            <h4 style={{ margin: '8px 0 2px' }}>{t('svc.history')}</h4>
            {notes.map((n) => (
              <div key={n.id} className="mx-note"><small>{formatDateTime(n.created_at)} · {n.created_by_name ?? ''}</small><div style={{ whiteSpace: 'pre-wrap' }}>{noteText(t, n.body)}</div></div>
            ))}
            {canCreate && (
              <div className="mx-row" style={{ gap: 6 }}>
                <input style={{ flex: 1 }} placeholder={t('svc.addNote')} value={note} onChange={(e) => setNote(e.currentTarget.value)}
                  onKeyDown={(e) => { if (e.key === 'Enter' && note.trim() && base) void run(api(`${base}/tickets/${sel.id}/notes`, { method: 'POST', body: { body: note } }), () => { setNote(''); open(sel); }); }} />
                <button className="mx-btn sm" disabled={!note.trim()} onClick={() => base && void run(api(`${base}/tickets/${sel.id}/notes`, { method: 'POST', body: { body: note } }), () => { setNote(''); open(sel); })}>{t('svc.add')}</button>
              </div>
            )}
            <Chatter type="ticket" id={sel.id} readOnly={!canEdit} />
            <button className="mx-btn sm" onClick={() => { setSel(null); setNotes([]); }}>{t('svc.newTicket')}</button>
            <Flash m={msg} />
          </div>
        ) : canCreate ? (
          <div className="mx-pay">
            <h3 style={{ margin: 0 }}>{t('svc.newTicket')}</h3>
            <label>{t('svc.customer')}<input placeholder={t('svc.findCustomer')} value={cq} onChange={(e) => { setCq(e.currentTarget.value); setNw({ ...nw, customer_id: '' }); }} /></label>
            {custs.length > 0 && !nw.customer_id && (
              <div className="mx-list">{custs.map((c) => (
                <button key={c.id} className="mx-link" style={{ display: 'block', textAlign: 'start' }}
                  onClick={() => { setNw({ ...nw, customer_id: c.id, contact_phone: nw.contact_phone || c.phone || '' }); setCq(`${c.code} ${c.name}`); setCusts([]); }}>{c.code} · {c.name}</button>
              ))}</div>
            )}
            <label>{t('svc.subject')}<input value={nw.subject} onChange={(e) => setNw({ ...nw, subject: e.currentTarget.value })} /></label>
            <label>{t('svc.category')}<select value={nw.category} onChange={(e) => setNw({ ...nw, category: e.currentTarget.value })}>
              {CATS.map((c) => <option key={c} value={c}>{t(`svc.cat.${c}`)}</option>)}</select></label>
            <label>{t('svc.priority')}<select value={nw.priority} onChange={(e) => setNw({ ...nw, priority: e.currentTarget.value })}>
              {PRIOS.map((c) => <option key={c} value={c}>{t(`svc.prio.${c}`)}</option>)}</select></label>
            <label>{t('svc.channel')}<select value={nw.channel} onChange={(e) => setNw({ ...nw, channel: e.currentTarget.value })}>
              {CHANNELS.map((c) => <option key={c} value={c}>{t(`svc.ch.${c}`)}</option>)}</select></label>
            <label>{t('svc.phone')}<input dir="ltr" value={nw.contact_phone} onChange={(e) => setNw({ ...nw, contact_phone: e.currentTarget.value })} /></label>
            <label>{t('svc.due')}<input type="datetime-local" dir="ltr" value={nw.due_at} onChange={(e) => setNw({ ...nw, due_at: e.currentTarget.value })} /></label>
            <label>{t('svc.description')}<textarea rows={3} value={nw.description} onChange={(e) => setNw({ ...nw, description: e.currentTarget.value })} /></label>
            <button className="mx-btn" disabled={!nw.subject.trim()} onClick={create}>{t('svc.save')}</button>
            <Flash m={msg} />
          </div>
        ) : <p>{t('svc.pick')}</p>}
      </section>
    </div>
  );
}

function ageText(t: TFunction, h: number) {
  return h < 48 ? t('svc.hours', { n: Math.round(h) }) : t('svc.days', { n: Math.round(h / 24) });
}
function noteText(t: TFunction, body: string) {
  const m = /^status: (\w+) → (\w+)$/.exec(body);
  return m ? t('svc.statusChange', { from: t(`svc.st.${m[1]}`), to: t(`svc.st.${m[2]}`) }) : body;
}

// ---------- generator maintenance ----------

interface Plan {
  id: string; generator_id: string; generator_name: string; station_name: string; task: string; every_hours: string | null; every_days: number | null;
  last_done_date: string | null; last_done_hours: string | null; is_active: boolean; notes: string; hours_now: string | null; due_hours: string | null;
  due_date: string | null; hours_left: string | null; days_left: number | null; state: string;
}
interface LogRow { id: string; generator_name: string; done_date: string; hour_meter: string | null; task: string; cost: string; currency: string | null; notes: string; created_by_name: string | null }
interface Gen { id: string; name: string }

function Maintenance({ me }: { me: Me }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const base = company ? `/companies/${company.id}` : null;
  const [plans, reload] = useList<Plan>(base && `${base}/maintenance`);
  const [log, reloadLog] = useList<LogRow>(base && `${base}/maintenance/log`);
  const [gens] = useList<Gen>(base && `${base}/generators`);
  const [msg, setMsg] = useState<Msg>(null);
  const [sel, setSel] = useState<Plan | null>(null);
  const [np, setNp] = useState({ generator_id: '', task: '', every_hours: '', every_days: '', last_done_hours: '', last_done_date: '' });
  const [done, setDone] = useState({ done_date: today(), hour_meter: '', cost: '', notes: '' });
  const canManage = can(me, 'service.manage');
  const canLog = can(me, 'service.create');
  const cur = company?.base_currency ?? 'USD';

  async function run(p: Promise<unknown>, after?: () => void) {
    try { await p; setMsg({ ok: true, text: t('svc.saved') }); after?.(); reload(); reloadLog(); } catch (e) { setMsg({ ok: false, text: errorText(t, e) }); }
  }
  const cols: Col<Plan>[] = [
    { key: 'state', title: t('svc.state'), get: (r) => t(`svc.ms.${r.state}`), width: 80, cls: (r) => (r.state === 'due' ? 'red' : r.state === 'soon' ? 'orange' : '') },
    { key: 'gen', title: t('svc.generator'), get: (r) => `${r.generator_name} · ${r.station_name}`, width: 150 },
    { key: 'task', title: t('svc.task'), get: (r) => r.task, width: 160 },
    { key: 'every', title: t('svc.every'), get: (r) => [r.every_hours ? t('svc.everyH', { n: Number(r.every_hours) }) : '', r.every_days ? t('svc.everyD', { n: r.every_days }) : ''].filter(Boolean).join(' / '), width: 130 },
    { key: 'now', title: t('svc.hoursNow'), get: (r) => Number(r.hours_now ?? 0), render: (r) => (r.hours_now ? formatAmount(r.hours_now, 0) : '—'), num: true, width: 80 },
    { key: 'left', title: t('svc.left'), get: (r) => Number(r.hours_left ?? r.days_left ?? 0),
      render: (r) => [r.hours_left != null ? t('svc.hLeft', { n: Math.round(Number(r.hours_left)) }) : '', r.days_left != null ? t('svc.dLeft', { n: r.days_left }) : ''].filter(Boolean).join(' / '), width: 130 },
    { key: 'last', title: t('svc.lastDone'), get: (r) => r.last_done_date ?? '', render: (r) => (r.last_done_date ? formatDate(r.last_done_date) : '—'), width: 100 },
  ];
  const logCols: Col<LogRow>[] = [
    { key: 'd', title: t('svc.date'), get: (r) => r.done_date, render: (r) => formatDate(r.done_date), width: 100 },
    { key: 'g', title: t('svc.generator'), get: (r) => r.generator_name, width: 120 },
    { key: 't', title: t('svc.task'), get: (r) => r.task, width: 160 },
    { key: 'h', title: t('svc.hoursNow'), get: (r) => Number(r.hour_meter ?? 0), render: (r) => (r.hour_meter ? formatAmount(r.hour_meter, 0) : ''), num: true, width: 80 },
    { key: 'c', title: t('svc.cost'), get: (r) => Number(r.cost), render: (r) => (Number(r.cost) ? `${formatAmount(r.cost)} ${r.currency ?? ''}` : ''), num: true, width: 100 },
    { key: 'n', title: t('svc.notes'), get: (r) => r.notes, width: 160 },
  ];
  return (
    <div className="mx-body" style={{ gridTemplateColumns: 'minmax(0, 1fr) minmax(300px, 420px)' }}>
      <section className="mx-box"><h3>{t('svc.plans')}</h3>
        <DataGrid rows={plans} cols={cols} rowKey={(r) => r.id} selected={sel?.id ?? null} title={t('svc.plans')}
          onSelect={(r) => { setSel(r); setMsg(null); setDone({ done_date: today(), hour_meter: r.hours_now ?? '', cost: '', notes: '' }); }} />
        <h3>{t('svc.log')}</h3>
        <DataGrid rows={log} cols={logCols} rowKey={(r) => r.id} title={t('svc.log')} />
      </section>
      <section className="mx-box">
        {sel && (
          <div className="mx-pay">
            <h3 style={{ margin: 0 }}>{sel.generator_name} · {sel.task}</h3>
            <small>{t(`svc.ms.${sel.state}`)}{sel.due_hours ? ` · ${t('svc.dueAtH', { n: Number(sel.due_hours) })}` : ''}{sel.due_date ? ` · ${t('svc.dueOn', { d: formatDate(sel.due_date) })}` : ''}</small>
            {canLog && (<>
              <label>{t('svc.date')}<input type="date" dir="ltr" value={done.done_date} onChange={(e) => setDone({ ...done, done_date: e.currentTarget.value })} /></label>
              <label>{t('svc.hoursNow')}<input dir="ltr" inputMode="decimal" value={done.hour_meter} onChange={(e) => setDone({ ...done, hour_meter: e.currentTarget.value })} /></label>
              <label>{t('svc.cost')} ({cur})<input dir="ltr" inputMode="decimal" value={done.cost} onChange={(e) => setDone({ ...done, cost: e.currentTarget.value })} /></label>
              <label>{t('svc.notes')}<input value={done.notes} onChange={(e) => setDone({ ...done, notes: e.currentTarget.value })} /></label>
              <button className="mx-btn" onClick={() => base && void run(api(`${base}/maintenance/log`, { method: 'POST', body: { plan_id: sel.id, ...done, currency: done.cost ? cur : '' } }), () => setSel(null))}>{t('svc.markDone')}</button>
              <small>{t('svc.costHint')}</small>
            </>)}
            {canManage && <button className="mx-btn sm" onClick={() => base && void run(api(`${base}/maintenance/tasks/${sel.id}`, { method: 'PATCH', body: { is_active: !sel.is_active } }), () => setSel(null))}>
              {sel.is_active ? t('svc.pause') : t('svc.resume')}</button>}
            <button className="mx-btn sm" onClick={() => setSel(null)}>{t('svc.newPlan')}</button>
          </div>
        )}
        {!sel && canManage && (
          <div className="mx-pay">
            <h3 style={{ margin: 0 }}>{t('svc.newPlan')}</h3>
            <label>{t('svc.generator')}<select value={np.generator_id} onChange={(e) => setNp({ ...np, generator_id: e.currentTarget.value })}>
              <option value="">—</option>{gens.map((g) => <option key={g.id} value={g.id}>{g.name}</option>)}</select></label>
            <label>{t('svc.task')}<input list="svc-tasks" value={np.task} onChange={(e) => setNp({ ...np, task: e.currentTarget.value })} /></label>
            <datalist id="svc-tasks">{['oil', 'oilFilter', 'fuelFilter', 'airFilter', 'coolant', 'belts', 'battery', 'overhaul'].map((k) => <option key={k} value={t(`svc.tasks.${k}`)} />)}</datalist>
            <label>{t('svc.everyHours')}<input dir="ltr" inputMode="decimal" value={np.every_hours} onChange={(e) => setNp({ ...np, every_hours: e.currentTarget.value })} /></label>
            <label>{t('svc.everyDays')}<input dir="ltr" inputMode="numeric" value={np.every_days} onChange={(e) => setNp({ ...np, every_days: e.currentTarget.value })} /></label>
            <label>{t('svc.lastHours')}<input dir="ltr" inputMode="decimal" value={np.last_done_hours} onChange={(e) => setNp({ ...np, last_done_hours: e.currentTarget.value })} /></label>
            <label>{t('svc.lastDate')}<input type="date" dir="ltr" value={np.last_done_date} onChange={(e) => setNp({ ...np, last_done_date: e.currentTarget.value })} /></label>
            <button className="mx-btn" disabled={!np.generator_id || !np.task.trim() || (!np.every_hours && !np.every_days)}
              onClick={() => base && void run(api(`${base}/maintenance/tasks`, { method: 'POST', body: {
                generator_id: np.generator_id, task: np.task, every_hours: np.every_hours || undefined, every_days: np.every_days ? Number(np.every_days) : undefined,
                last_done_hours: np.last_done_hours || undefined, last_done_date: np.last_done_date || undefined } }), () => setNp({ ...np, task: '', every_hours: '', every_days: '' }))}>
              {t('svc.save')}</button>
          </div>
        )}
        <Flash m={msg} />
      </section>
    </div>
  );
}
