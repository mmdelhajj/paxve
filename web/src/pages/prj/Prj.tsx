import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, can, type Me } from '../../api';
import { useCompany } from '../../company';
import { formatAmount, formatDate, formatDateTime, today } from '../../util';
import Chatter from '../../components/Chatter';
import Attachments from '../../components/Attachments';
import { useBase, useLoad, useRun } from '../wh/Wh';

// Projects (Magic-style screens): projects with their kanban board, tasks (subtasks, checklist, comments, files),
// milestones, timesheets with a timer, billing (time, milestones, fixed price), linked costs and profitability.

type Msg = { ok: boolean; text: string } | null;
function Flash({ m }: { m: Msg }) {
  return m ? <div className={`mx-flash ${m.ok ? 'good' : 'bad'}`}>{m.text}</div> : null;
}

export interface Project {
  id: string; number: number; code: string; name: string; customer_id: string | null; customer_name: string | null; manager_id: string | null; manager_name: string | null;
  billing: string; currency: string; hourly_rate: string; fixed_price: string; budget: string; budget_hours: string; start_date: string | null; end_date: string | null;
  status: string; description: string; color: string; tasks: number; tasks_done: number; hours: string; late: boolean;
}
interface Stage { id: string; seq: number; name: string; is_done: boolean; tasks: number }
interface Milestone { id: string; name: string; due_date: string | null; amount: string; reached_at: string | null; invoice_number: number | null; invoiced: boolean; tasks: number; tasks_done: number }
interface Summary {
  hours: string; billable_hours: string; invoiced_hours: string; to_invoice_hours: string; budget_hours: string; labour_cost: string; expenses_cost: string;
  bills_cost: string; total_cost: string; revenue: string; margin: string; margin_pct: string; budget: string; budget_used_pct: string; base_currency: string;
  id: string; number: number; name: string; customer_name: string; status: string; billing: string;
}
interface Link { id: string; doc_type: string; doc_id: string; number: number; doc_date: string; currency: string; amount: string; status: string; label: string }
interface ProjectDetail { project: Project; stages: Stage[]; milestones: Milestone[]; summary: Summary | null; links: Link[] }
export interface Task {
  id: string; number: number; project_id: string; project_name: string; project_color: string; stage_id: string | null; stage_name: string | null; parent_id: string | null;
  milestone_id: string | null; milestone_name: string | null; title: string; description: string; assignee_id: string | null; assignee_name: string | null;
  priority: string; deadline: string | null; planned_hours: string; checklist: { text: string; done: boolean }[]; tags: string[]; seq: number; done_at: string | null;
  subtasks: number; subtasks_done: number; hours: string; comments: number; files: number; late: boolean;
}
interface Timesheet {
  id: string; project_id: string; project_name: string; task_id: string | null; task_number: number | null; task_title: string | null; employee_id: string;
  employee_name: string; work_date: string; hours: string; description: string; billable: boolean; cost: string; running_since: string | null;
  invoice_id: string | null; invoice_number: number | null; invoiced: boolean; elapsed_s?: number;
}
interface Person { id: string; full_name: string }
interface Cust { id: string; name: string; code: string }

const MODES = ['projects', 'tasks', 'timesheets', 'reports'] as const;
type Mode = typeof MODES[number];
const PRIOS = ['low', 'normal', 'high', 'urgent'];
const BILLINGS = ['tm', 'fixed', 'milestone', 'none'];
const PSTATUS = ['active', 'on_hold', 'done', 'cancelled'];
const h = (v: string | null | undefined) => (v == null || v === '' ? '0' : String(Number(v)));
const amt = (v: string | number | null | undefined) => formatAmount(String(v ?? 0), 2);
const num = (s: string) => s.replace(/[^\d.]/g, '');
const prioColor: Record<string, string> = { urgent: '#b42318', high: '#a15c00', normal: '#1d4ed8', low: '#6b7280' };

export default function Prj({ me, initial }: { me: Me; initial?: string }) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<Mode>((MODES as readonly string[]).includes(initial ?? '') ? initial as Mode : 'projects');
  return (
    <div className="mx-col">
      <div className="mx-strip"><b>{t(`pj.m.${mode}`)}</b><Overview /><span className="mx-sp" /><small style={{ opacity: 0.8 }}>{t(`pj.help.${mode}`)}</small></div>
      <div className="mx-modes" role="tablist">
        {MODES.map((m) => <button key={m} role="tab" className="mx-mode" aria-selected={mode === m} onClick={() => setMode(m)}>{t(`pj.m.${m}`)}</button>)}
      </div>
      <div style={{ flex: 1, minHeight: 0, overflow: 'auto', padding: 6 }}>
        {mode === 'projects' && <Projects me={me} />}
        {mode === 'tasks' && <MyTasks me={me} />}
        {mode === 'timesheets' && <Timesheets me={me} />}
        {mode === 'reports' && <Profitability />}
      </div>
    </div>
  );
}

function Overview() {
  const { t } = useTranslation();
  const base = useBase();
  const [o] = useLoad<{ active_projects: number; my_tasks: number; late_tasks: number; hours_week: string; hours_to_invoice: string } | null>(base && `${base}/prj/overview`, null);
  if (!o) return null;
  return (
    <span className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
      <span className="mx-pill">{t('pj.ov.active', { n: o.active_projects })}</span>
      <span className="mx-pill">{t('pj.ov.mine', { n: o.my_tasks })}</span>
      {o.late_tasks > 0 && <span className="mx-pill warn">{t('pj.ov.late', { n: o.late_tasks })}</span>}
      <span className="mx-pill">{t('pj.ov.week', { n: h(o.hours_week) })}</span>
      {Number(o.hours_to_invoice) > 0 && <span className="mx-pill">{t('pj.ov.toInvoice', { n: h(o.hours_to_invoice) })}</span>}
    </span>
  );
}

export function usePeople(): Person[] {
  const base = useBase();
  const [p] = useLoad<Person[]>(base && `${base}/mentionable`, []);
  return p;
}

export function CustomerPicker({ value, label, onChange }: { value: { id: string; text: string }; label: string; onChange: (v: { id: string; text: string }) => void }) {
  const { t } = useTranslation();
  const base = useBase();
  const [q, setQ] = useState(value.text);
  const [list, setList] = useState<Cust[]>([]);
  useEffect(() => setQ(value.text), [value.text]);
  useEffect(() => {
    if (!base || q.trim().length < 2 || value.id) { setList([]); return; }
    const hd = window.setTimeout(() => api<Cust[]>(`${base}/customers?limit=15&q=${encodeURIComponent(q.trim())}`).then(setList).catch(() => setList([])), 250);
    return () => window.clearTimeout(hd);
  }, [base, q, value.id]);
  return (
    <label className="mx-fld" style={{ position: 'relative' }}>{label}
      <input value={q} placeholder={t('pj.findCustomer')} onChange={(e) => { setQ(e.currentTarget.value); onChange({ id: '', text: e.currentTarget.value }); }} />
      {list.length > 0 && (
        <div className="mx-list" style={{ position: 'absolute', top: '100%', insetInlineStart: 0, zIndex: 5, background: '#fff', border: '1px solid #ccd', minWidth: 220 }}>
          {list.map((c) => <button key={c.id} className="mx-link" style={{ display: 'block', textAlign: 'start', width: '100%' }}
            onClick={() => { onChange({ id: c.id, text: `${c.code} ${c.name}` }); setList([]); }}>{c.code} · {c.name}</button>)}
        </div>)}
    </label>
  );
}

// ======================= projects =======================

function Projects({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [status, setStatus] = useState('open');
  const [q, setQ] = useState('');
  const [rows, reload] = useLoad<Project[]>(base && `${base}/prj/projects?status=${status}&q=${encodeURIComponent(q.trim())}`, []);
  const [sel, setSel] = useState<string | null>(null);
  const [nf, setNf] = useState({ name: '', code: '', billing: 'tm', hourly_rate: '', fixed_price: '', budget: '', start_date: today(), end_date: '' });
  const [cust, setCust] = useState({ id: '', text: '' });
  const [msg, run] = useRun(reload);
  const manage = can(me, 'projects.manage');
  return (
    <div className="mx-row" style={{ gap: 8, alignItems: 'flex-start', flexWrap: 'wrap' }}>
      <section className="mx-box" style={{ flex: sel ? '1 1 380px' : '999 1 600px', minWidth: 0 }}>
        <h3>{t('pj.m.projects')}</h3>
        <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
          <label className="mx-fld">{t('pj.status')}<select value={status} onChange={(e) => setStatus(e.currentTarget.value)}>
            {['open', ...PSTATUS, 'all'].map((s) => <option key={s} value={s}>{t(`pj.ps.${s}`)}</option>)}</select></label>
          <label className="mx-fld">{t('pj.search')}<input value={q} onChange={(e) => setQ(e.currentTarget.value)} /></label>
        </div>
        {manage && (
          <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end', borderTop: '1px solid var(--mx-line, #dde3ea)' }}>
            <label className="mx-fld">{t('pj.name')}<input value={nf.name} onChange={(e) => setNf({ ...nf, name: e.currentTarget.value })} /></label>
            <label className="mx-fld">{t('pj.code')}<input style={{ width: 80 }} value={nf.code} onChange={(e) => setNf({ ...nf, code: e.currentTarget.value })} /></label>
            <CustomerPicker label={t('pj.customer')} value={cust} onChange={setCust} />
            <label className="mx-fld">{t('pj.billing')}<select value={nf.billing} onChange={(e) => setNf({ ...nf, billing: e.currentTarget.value })}>
              {BILLINGS.map((b) => <option key={b} value={b}>{t(`pj.bill.${b}`)}</option>)}</select></label>
            {nf.billing === 'tm' && <label className="mx-fld">{t('pj.rate')}<input dir="ltr" style={{ width: 80 }} value={nf.hourly_rate} onChange={(e) => setNf({ ...nf, hourly_rate: num(e.currentTarget.value) })} /></label>}
            {nf.billing === 'fixed' && <label className="mx-fld">{t('pj.fixed')}<input dir="ltr" style={{ width: 90 }} value={nf.fixed_price} onChange={(e) => setNf({ ...nf, fixed_price: num(e.currentTarget.value) })} /></label>}
            <label className="mx-fld">{t('pj.budget')}<input dir="ltr" style={{ width: 90 }} value={nf.budget} onChange={(e) => setNf({ ...nf, budget: num(e.currentTarget.value) })} /></label>
            <label className="mx-fld">{t('pj.start')}<input type="date" dir="ltr" value={nf.start_date} onChange={(e) => setNf({ ...nf, start_date: e.currentTarget.value })} /></label>
            <label className="mx-fld">{t('pj.end')}<input type="date" dir="ltr" value={nf.end_date} onChange={(e) => setNf({ ...nf, end_date: e.currentTarget.value })} /></label>
            <button className="mx-btn main" disabled={!nf.name.trim() || (nf.billing !== 'none' && !cust.id)}
              onClick={() => void run(api<ProjectDetail>(`${base}/prj/projects`, { method: 'POST', body: { ...nf, billing: cust.id ? nf.billing : 'none', customer_id: cust.id || undefined } }),
                (x) => { setSel((x as ProjectDetail).project.id); setNf({ ...nf, name: '', code: '' }); setCust({ id: '', text: '' }); })}>{t('pj.create')}</button>
          </div>
        )}
        <Flash m={msg} />
        <div style={{ overflowX: 'auto' }}>
          <table className="mx-table">
            <thead><tr><th>#</th><th>{t('pj.name')}</th><th>{t('pj.customer')}</th><th>{t('pj.billing')}</th><th>{t('pj.progress')}</th><th className="num">{t('pj.hours')}</th><th>{t('pj.end')}</th><th>{t('pj.status')}</th></tr></thead>
            <tbody>
              {rows.map((p) => (
                <tr key={p.id} className={sel === p.id ? 'sel' : ''} style={{ cursor: 'pointer' }} onClick={() => setSel(p.id)}>
                  <td dir="ltr">{p.code || `P${p.number}`}</td><td><b>{p.name}</b></td><td>{p.customer_name ?? ''}</td><td>{t(`pj.bill.${p.billing}`)}</td>
                  <td dir="ltr">{p.tasks_done}/{p.tasks}</td><td className="num">{h(p.hours)}</td>
                  <td className={p.late ? 'red' : ''}>{p.end_date ? formatDate(p.end_date) : ''}</td><td>{t(`pj.ps.${p.status}`)}</td>
                </tr>))}
              {rows.length === 0 && <tr><td colSpan={8} className="mx-empty">{t('pj.none')}</td></tr>}
            </tbody>
          </table>
        </div>
      </section>
      {sel && <ProjectPanel me={me} id={sel} onChange={reload} onClose={() => setSel(null)} />}
    </div>
  );
}

function ProjectPanel({ me, id, onChange, onClose }: { me: Me; id: string; onChange: () => void; onClose: () => void }) {
  const { t } = useTranslation();
  const base = useBase();
  const [d, reloadD] = useLoad<ProjectDetail | null>(base && `${base}/prj/projects/${id}`, null);
  const [tasks, reloadT] = useLoad<Task[]>(base && `${base}/prj/project-tasks?project_id=${id}&top=1`, []);
  const [tab, setTab] = useState<'board' | 'milestones' | 'billing' | 'costs' | 'setup'>('board');
  const [taskSel, setTaskSel] = useState<string | null>(null);
  const reload = () => { reloadD(); reloadT(); onChange(); };
  const [msg, run] = useRun(reload);
  if (!d) return null;
  const p = d.project;
  const s = d.summary;
  const manage = can(me, 'projects.manage');
  return (
    <section className="mx-box" style={{ flex: '999 1 640px', minWidth: 0 }}>
      <h3 style={{ display: 'flex', gap: 6, alignItems: 'center', flexWrap: 'wrap' }}>
        <span dir="ltr">{p.code || `P${p.number}`}</span> · {p.name}{p.customer_name ? ` · ${p.customer_name}` : ''}
        <span className="mx-pill">{t(`pj.bill.${p.billing}`)}</span>
        <span className="mx-sp" />
        {manage && <select aria-label={t('pj.status')} value={p.status} onChange={(e) => void run(api(`${base}/prj/projects/${id}`, { method: 'PATCH', body: { status: e.currentTarget.value } }))}>
          {PSTATUS.map((x) => <option key={x} value={x}>{t(`pj.ps.${x}`)}</option>)}</select>}
        <button className="mx-btn sm" onClick={onClose}>{t('pj.close')}</button>
      </h3>
      {s && (
        <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap' }}>
          <Kpi label={t('pj.k.hours')} value={`${h(s.hours)}${Number(s.budget_hours) > 0 ? ` / ${h(s.budget_hours)}` : ''}`} />
          <Kpi label={t('pj.k.toInvoice')} value={h(s.to_invoice_hours)} />
          <Kpi label={t('pj.k.revenue')} value={`${amt(s.revenue)} ${s.base_currency}`} />
          <Kpi label={t('pj.k.cost')} value={`${amt(s.total_cost)} ${s.base_currency}`} />
          <Kpi label={t('pj.k.margin')} value={`${amt(s.margin)}${s.margin_pct ? ` (${s.margin_pct}%)` : ''}`} bad={Number(s.margin) < 0} />
          {Number(s.budget) > 0 && <Kpi label={t('pj.k.budget')} value={`${s.budget_used_pct || '0'}%`} bad={Number(s.budget_used_pct) > 100} />}
        </div>
      )}
      <div className="mx-modes" role="tablist">
        {(['board', 'milestones', 'billing', 'costs', 'setup'] as const).map((x) => <button key={x} role="tab" className="mx-mode" aria-selected={tab === x} onClick={() => setTab(x)}>{t(`pj.tab.${x}`)}</button>)}
      </div>
      <Flash m={msg} />
      {tab === 'board' && <Board me={me} project={p} stages={d.stages} tasks={tasks} milestones={d.milestones} onOpen={setTaskSel} onChange={reload} />}
      {tab === 'milestones' && <Milestones me={me} projectId={id} rows={d.milestones} onChange={reload} />}
      {tab === 'billing' && <Billing me={me} project={p} onChange={reload} />}
      {tab === 'costs' && <Costs me={me} projectId={id} links={d.links} onChange={reload} />}
      {tab === 'setup' && <Setup me={me} d={d} onChange={reload} />}
      {taskSel && <TaskPanel me={me} id={taskSel} milestones={d.milestones} onChange={reload} onClose={() => setTaskSel(null)} onOpen={setTaskSel} />}
    </section>
  );
}

function Kpi({ label, value, bad }: { label: string; value: string; bad?: boolean }) {
  return (
    <div style={{ border: '1px solid #dde3ea', borderRadius: 6, padding: '4px 10px', minWidth: 110, background: '#fff' }}>
      <small style={{ opacity: 0.75 }}>{label}</small><div className={bad ? 'red' : ''} style={{ fontWeight: 600 }} dir="ltr">{value}</div>
    </div>
  );
}

function Board({ me, project, stages, tasks, milestones, onOpen, onChange }: { me: Me; project: Project; stages: Stage[]; tasks: Task[]; milestones: Milestone[]; onOpen: (id: string) => void; onChange: () => void }) {
  const { t } = useTranslation();
  const base = useBase();
  const people = usePeople();
  const [msg, run] = useRun(onChange);
  const [adding, setAdding] = useState<Record<string, string>>({});
  const [who, setWho] = useState('');
  const [drag, setDrag] = useState<string | null>(null);
  const editable = can(me, 'projects.edit');
  const move = (task: Task, stage: string) => { if (task.stage_id !== stage) void run(api(`${base}/prj/project-tasks/${task.id}`, { method: 'PATCH', body: { stage_id: stage } })); };
  const shown = who ? tasks.filter((x) => x.assignee_id === who) : tasks;
  void milestones;
  return (
    <div style={{ padding: 6 }}>
      <div className="mx-row" style={{ gap: 6, marginBottom: 6, flexWrap: 'wrap' }}>
        <label className="mx-fld">{t('pj.assignee')}<select value={who} onChange={(e) => setWho(e.currentTarget.value)}>
          <option value="">{t('pj.everyone')}</option>{people.map((u) => <option key={u.id} value={u.id}>{u.full_name}</option>)}</select></label>
      </div>
      <Flash m={msg} />
      <div style={{ display: 'grid', gridAutoFlow: 'column', gridAutoColumns: 'minmax(220px, 1fr)', gap: 8, overflowX: 'auto' }}>
        {stages.map((st, i) => (
          <div key={st.id} className="pj-col" style={{ background: st.is_done ? '#eef7f1' : '#f4f6f9', borderRadius: 8, padding: 6, minHeight: 180 }}
            onDragOver={(e) => { if (editable) e.preventDefault(); }} onDrop={() => { const x = tasks.find((y) => y.id === drag); if (x) move(x, st.id); setDrag(null); }}>
            <div className="mx-row" style={{ gap: 4, alignItems: 'center', marginBottom: 6 }}><b>{st.name}</b><span className="mx-pill">{shown.filter((x) => x.stage_id === st.id).length}</span></div>
            {shown.filter((x) => x.stage_id === st.id).map((x) => (
              <div key={x.id} draggable={editable} onDragStart={() => setDrag(x.id)} className="pj-card"
                style={{ background: '#fff', border: '1px solid #dde3ea', borderInlineStart: `3px solid ${prioColor[x.priority] ?? '#ccc'}`, borderRadius: 6, padding: 6, marginBottom: 6 }}>
                <a href="#" onClick={(e) => { e.preventDefault(); onOpen(x.id); }} style={{ fontWeight: 600, display: 'block' }}>#{x.number} {x.title}</a>
                <small style={{ display: 'block', opacity: 0.85 }}>
                  {x.assignee_name ?? t('pj.unassigned')}
                  {x.deadline ? <span className={x.late ? 'red' : ''}> · {formatDate(x.deadline)}</span> : null}
                  {x.checklist.length > 0 ? ` · ☑ ${x.checklist.filter((c) => c.done).length}/${x.checklist.length}` : ''}
                  {x.subtasks > 0 ? ` · ${t('pj.subN', { d: x.subtasks_done, n: x.subtasks })}` : ''}
                  {Number(x.hours) > 0 ? ` · ${h(x.hours)}h` : ''}{x.comments > 0 ? ` · 💬${x.comments}` : ''}
                </small>
                {editable && (
                  <div className="mx-row" style={{ gap: 4, marginTop: 4 }}>
                    {i > 0 && <button className="mx-btn sm" aria-label={t('pj.moveBack')} onClick={() => move(x, stages[i - 1].id)}>‹</button>}
                    {i < stages.length - 1 && <button className="mx-btn sm" aria-label={t('pj.moveNext')} onClick={() => move(x, stages[i + 1].id)}>›</button>}
                  </div>
                )}
              </div>))}
            {can(me, 'projects.create') && project.status !== 'cancelled' && (
              <div className="mx-row" style={{ gap: 4 }}>
                <input style={{ flex: 1, minWidth: 0 }} placeholder={t('pj.addTask')} value={adding[st.id] ?? ''} onChange={(e) => setAdding({ ...adding, [st.id]: e.currentTarget.value })}
                  onKeyDown={(e) => { if (e.key === 'Enter' && (adding[st.id] ?? '').trim()) void run(api(`${base}/prj/project-tasks`, { method: 'POST', body: { project_id: project.id, stage_id: st.id, title: adding[st.id] } }), () => setAdding({ ...adding, [st.id]: '' })); }} />
                <button className="mx-btn sm" disabled={!(adding[st.id] ?? '').trim()} onClick={() => void run(api(`${base}/prj/project-tasks`, { method: 'POST', body: { project_id: project.id, stage_id: st.id, title: adding[st.id] } }), () => setAdding({ ...adding, [st.id]: '' }))}>+</button>
              </div>)}
          </div>))}
      </div>
    </div>
  );
}

export function TaskPanel({ me, id, milestones, onChange, onClose, onOpen }: { me: Me; id: string; milestones?: Milestone[]; onChange: () => void; onClose: () => void; onOpen: (id: string) => void }) {
  const { t } = useTranslation();
  const base = useBase();
  const people = usePeople();
  const [d, reload] = useLoad<{ task: Task; subtasks: Task[]; timesheets: Timesheet[] } | null>(base && `${base}/prj/project-tasks/${id}`, null);
  const [msg, run] = useRun(() => { reload(); onChange(); });
  const [newCheck, setNewCheck] = useState('');
  const [sub, setSub] = useState('');
  const [log, setLog] = useState({ date: today(), hours: '', description: '' });
  if (!d) return null;
  const x = d.task;
  const edit = can(me, 'projects.edit');
  const patch = (body: Record<string, unknown>) => void run(api(`${base}/prj/project-tasks/${id}`, { method: 'PATCH', body }));
  const setChecklist = (cl: { text: string; done: boolean }[]) => patch({ checklist: cl });
  return (
    <div role="dialog" aria-label={x.title} style={{ position: 'fixed', insetBlock: 0, insetInlineEnd: 0, width: 'min(520px, 100vw)', background: '#fff', boxShadow: '0 0 24px rgba(0,0,0,.25)', zIndex: 50, overflow: 'auto', padding: 10 }}>
      <div className="mx-row" style={{ gap: 6, alignItems: 'center' }}>
        <b style={{ flex: 1 }}>#{x.number} · {x.project_name}</b>
        {x.parent_id && <button className="mx-btn sm" onClick={() => onOpen(x.parent_id as string)}>{t('pj.parent')}</button>}
        <button className="mx-btn sm" onClick={onClose}>{t('pj.close')}</button>
      </div>
      <label className="mx-fld">{t('pj.title')}<input disabled={!edit} defaultValue={x.title} key={`t-${x.id}-${x.title}`} onBlur={(e) => { if (e.currentTarget.value.trim() && e.currentTarget.value !== x.title) patch({ title: e.currentTarget.value }); }} /></label>
      <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
        <label className="mx-fld">{t('pj.assignee')}<select disabled={!edit} value={x.assignee_id ?? ''} onChange={(e) => patch({ assignee_id: e.currentTarget.value })}>
          <option value="">{t('pj.unassigned')}</option>{people.map((u) => <option key={u.id} value={u.id}>{u.full_name}</option>)}</select></label>
        <label className="mx-fld">{t('pj.priority')}<select disabled={!edit} value={x.priority} onChange={(e) => patch({ priority: e.currentTarget.value })}>
          {PRIOS.map((p) => <option key={p} value={p}>{t(`pj.prio.${p}`)}</option>)}</select></label>
        <label className="mx-fld">{t('pj.deadline')}<input type="date" dir="ltr" disabled={!edit} value={x.deadline ?? ''} onChange={(e) => patch({ deadline: e.currentTarget.value })} /></label>
        <label className="mx-fld">{t('pj.planned')}<input dir="ltr" style={{ width: 60 }} disabled={!edit} defaultValue={h(x.planned_hours)} key={`ph-${x.id}-${x.planned_hours}`} onBlur={(e) => patch({ planned_hours: num(e.currentTarget.value) || '0' })} /></label>
        {milestones && milestones.length > 0 && <label className="mx-fld">{t('pj.milestone')}<select disabled={!edit} value={x.milestone_id ?? ''} onChange={(e) => patch({ milestone_id: e.currentTarget.value })}>
          <option value="">—</option>{milestones.map((m) => <option key={m.id} value={m.id}>{m.name}</option>)}</select></label>}
        <label className="mx-fld" style={{ flexDirection: 'row', alignItems: 'center', gap: 4 }}><input type="checkbox" disabled={!edit} checked={!!x.done_at} onChange={(e) => patch({ done: e.currentTarget.checked })} />{t('pj.done')}</label>
      </div>
      <label className="mx-fld">{t('pj.description')}<textarea rows={3} disabled={!edit} defaultValue={x.description} key={`d-${x.id}`} onBlur={(e) => { if (e.currentTarget.value !== x.description) patch({ description: e.currentTarget.value }); }} /></label>
      <h4 style={{ margin: '8px 0 2px' }}>{t('pj.checklist')}</h4>
      {x.checklist.map((c, i) => (
        <label key={i} style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
          <input type="checkbox" disabled={!edit} checked={c.done} onChange={(e) => setChecklist(x.checklist.map((y, j) => (j === i ? { ...y, done: e.currentTarget.checked } : y)))} />
          <span style={{ flex: 1, textDecoration: c.done ? 'line-through' : undefined }}>{c.text}</span>
          {edit && <button className="mx-btn sm" aria-label={t('pj.remove')} onClick={() => setChecklist(x.checklist.filter((_, j) => j !== i))}>×</button>}
        </label>))}
      {edit && <div className="mx-row" style={{ gap: 4 }}><input style={{ flex: 1 }} placeholder={t('pj.addCheck')} value={newCheck} onChange={(e) => setNewCheck(e.currentTarget.value)} />
        <button className="mx-btn sm" disabled={!newCheck.trim()} onClick={() => { setChecklist([...x.checklist, { text: newCheck.trim(), done: false }]); setNewCheck(''); }}>+</button></div>}
      {!x.parent_id && <>
        <h4 style={{ margin: '8px 0 2px' }}>{t('pj.subtasks')}</h4>
        {d.subtasks.map((s) => <div key={s.id}><a href="#" onClick={(e) => { e.preventDefault(); onOpen(s.id); }}>{s.done_at ? '✔ ' : ''}#{s.number} {s.title}</a> <small>{s.assignee_name ?? ''}</small></div>)}
        {can(me, 'projects.create') && <div className="mx-row" style={{ gap: 4 }}><input style={{ flex: 1 }} placeholder={t('pj.addSub')} value={sub} onChange={(e) => setSub(e.currentTarget.value)} />
          <button className="mx-btn sm" disabled={!sub.trim()} onClick={() => void run(api(`${base}/prj/project-tasks`, { method: 'POST', body: { project_id: x.project_id, parent_id: x.id, title: sub } }), () => setSub(''))}>+</button></div>}
      </>}
      <h4 style={{ margin: '8px 0 2px' }}>{t('pj.time')} · {h(x.hours)}h</h4>
      {can(me, 'projects.create') && (
        <div className="mx-row" style={{ gap: 4, flexWrap: 'wrap', alignItems: 'end' }}>
          <input type="date" dir="ltr" aria-label={t('pj.date')} value={log.date} onChange={(e) => setLog({ ...log, date: e.currentTarget.value })} />
          <input dir="ltr" style={{ width: 60 }} placeholder={t('pj.hours')} aria-label={t('pj.hours')} value={log.hours} onChange={(e) => setLog({ ...log, hours: num(e.currentTarget.value) })} />
          <input style={{ flex: 1, minWidth: 120 }} placeholder={t('pj.what')} value={log.description} onChange={(e) => setLog({ ...log, description: e.currentTarget.value })} />
          <button className="mx-btn sm" disabled={!Number(log.hours)} onClick={() => void run(api(`${base}/prj/timesheets`, { method: 'POST', body: { task_id: x.id, ...log } }), () => setLog({ ...log, hours: '', description: '' }))}>{t('pj.log')}</button>
          <button className="mx-btn sm" onClick={() => void run(api(`${base}/prj/timer/start`, { method: 'POST', body: { task_id: x.id } }))}>▶ {t('pj.timer.start')}</button>
        </div>)}
      <table className="mx-table"><tbody>
        {d.timesheets.map((ts) => <tr key={ts.id}><td>{formatDate(ts.work_date)}</td><td>{ts.employee_name}</td><td className="num">{ts.running_since ? '⏱' : h(ts.hours)}</td><td>{ts.description}</td><td>{ts.invoiced ? t('pj.invoiced') : ''}</td></tr>)}
      </tbody></table>
      <Flash m={msg} />
      <Attachments type="project_task" id={x.id} />
      <Chatter type="project_task" id={x.id} readOnly={!can(me, 'projects.create')} />
    </div>
  );
}

function Milestones({ me, projectId, rows, onChange }: { me: Me; projectId: string; rows: Milestone[]; onChange: () => void }) {
  const { t } = useTranslation();
  const base = useBase();
  const [msg, run] = useRun(onChange);
  const [nf, setNf] = useState({ name: '', due_date: '', amount: '' });
  return (
    <div style={{ padding: 6 }}>
      {can(me, 'projects.manage') && (
        <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap', alignItems: 'end' }}>
          <label className="mx-fld">{t('pj.name')}<input value={nf.name} onChange={(e) => setNf({ ...nf, name: e.currentTarget.value })} /></label>
          <label className="mx-fld">{t('pj.due')}<input type="date" dir="ltr" value={nf.due_date} onChange={(e) => setNf({ ...nf, due_date: e.currentTarget.value })} /></label>
          <label className="mx-fld">{t('pj.amount')}<input dir="ltr" style={{ width: 90 }} value={nf.amount} onChange={(e) => setNf({ ...nf, amount: num(e.currentTarget.value) })} /></label>
          <button className="mx-btn" disabled={!nf.name.trim()} onClick={() => void run(api(`${base}/prj/milestones`, { method: 'POST', body: { project_id: projectId, ...nf } }), () => setNf({ name: '', due_date: '', amount: '' }))}>{t('pj.add')}</button>
        </div>)}
      <Flash m={msg} />
      <table className="mx-table">
        <thead><tr><th>{t('pj.name')}</th><th>{t('pj.due')}</th><th className="num">{t('pj.amount')}</th><th>{t('pj.progress')}</th><th>{t('pj.reached')}</th><th>{t('pj.invoice')}</th></tr></thead>
        <tbody>
          {rows.map((m) => (
            <tr key={m.id}><td>{m.name}</td><td>{m.due_date ? formatDate(m.due_date) : ''}</td><td className="num">{amt(m.amount)}</td><td dir="ltr">{m.tasks_done}/{m.tasks}</td>
              <td><input type="checkbox" aria-label={t('pj.reached')} disabled={!can(me, 'projects.edit') || m.invoiced} checked={!!m.reached_at}
                onChange={(e) => void run(api(`${base}/prj/milestones/${m.id}`, { method: 'PATCH', body: { reached: e.currentTarget.checked } }))} /></td>
              <td>{m.invoiced ? `#${m.invoice_number}` : ''}</td></tr>))}
          {rows.length === 0 && <tr><td colSpan={6} className="mx-empty">{t('pj.none')}</td></tr>}
        </tbody>
      </table>
    </div>
  );
}

function Billing({ me, project, onChange }: { me: Me; project: Project; onChange: () => void }) {
  const { t } = useTranslation();
  const base = useBase();
  const [b, reload] = useLoad<{ time: { task_title: string; task_number: number | null; hours: string; amount: string; entries: number }[]; milestones: { id: string; name: string; amount: string }[]; fixed: { price: string; invoiced: string; remaining: string } } | null>(base && `${base}/prj/projects/${project.id}/to-invoice`, null);
  const [msg, run] = useRun(() => { reload(); onChange(); });
  const [pct, setPct] = useState('');
  const [date, setDate] = useState(today());
  const ok = can(me, 'billing.create') && !!project.customer_id;
  if (!b) return null;
  const inv = (body: Record<string, unknown>) => void run(api(`${base}/prj/projects/${project.id}/invoice`, { method: 'POST', body: { date, ...body } }));
  const timeTotal = b.time.reduce((s, x) => s + Number(x.amount), 0);
  return (
    <div style={{ padding: 6, display: 'flex', flexDirection: 'column', gap: 8 }}>
      <label className="mx-fld">{t('pj.date')}<input type="date" dir="ltr" value={date} onChange={(e) => setDate(e.currentTarget.value)} /></label>
      <section className="mx-box"><h3>{t('pj.bill.tm')} · {t('pj.rate')} {amt(project.hourly_rate)} {project.currency}</h3>
        <table className="mx-table"><tbody>
          {b.time.map((x, i) => <tr key={i}><td>{x.task_number ? `#${x.task_number} ${x.task_title}` : project.name}</td><td className="num">{h(x.hours)}h</td><td className="num">{amt(x.amount)}</td></tr>)}
          {b.time.length === 0 && <tr><td className="mx-empty">{t('pj.nothing')}</td></tr>}
        </tbody></table>
        {ok && b.time.length > 0 && <button className="mx-btn main" onClick={() => inv({ kind: 'time' })}>{t('pj.invoiceTime', { a: amt(timeTotal), c: project.currency })}</button>}
      </section>
      <section className="mx-box"><h3>{t('pj.bill.milestone')}</h3>
        <table className="mx-table"><tbody>
          {b.milestones.map((m) => <tr key={m.id}><td>{m.name}</td><td className="num">{amt(m.amount)}</td></tr>)}
          {b.milestones.length === 0 && <tr><td className="mx-empty">{t('pj.nothing')}</td></tr>}
        </tbody></table>
        {ok && b.milestones.length > 0 && <button className="mx-btn" onClick={() => inv({ kind: 'milestones' })}>{t('pj.invoiceMs')}</button>}
      </section>
      {Number(b.fixed.price) > 0 && (
        <section className="mx-box"><h3>{t('pj.bill.fixed')}: {amt(b.fixed.price)} · {t('pj.invoicedSoFar')} {amt(b.fixed.invoiced)} · {t('pj.remaining')} {amt(b.fixed.remaining)}</h3>
          {ok && Number(b.fixed.remaining) > 0 && <div className="mx-row" style={{ gap: 6, padding: 6 }}>
            <input dir="ltr" style={{ width: 60 }} placeholder="%" aria-label="%" value={pct} onChange={(e) => setPct(num(e.currentTarget.value))} />
            <button className="mx-btn" disabled={!Number(pct)} onClick={() => inv({ kind: 'fixed', percent: pct })}>{t('pj.invoicePct')}</button>
            <button className="mx-btn" onClick={() => inv({ kind: 'fixed' })}>{t('pj.invoiceRest')}</button>
          </div>}
        </section>)}
      <Flash m={msg} />
    </div>
  );
}

function Costs({ me, projectId, links, onChange }: { me: Me; projectId: string; links: Link[]; onChange: () => void }) {
  const { t } = useTranslation();
  const base = useBase();
  const [kind, setKind] = useState<'expense' | 'bill'>('expense');
  const manage = can(me, 'projects.manage');
  const [docs, reloadDocs] = useLoad<{ id: string; number: number; doc_date: string; currency: string; amount: string; label: string }[]>(manage && base ? `${base}/prj/linkable?doc_type=${kind}` : null, []);
  const [pick, setPick] = useState('');
  const [msg, run] = useRun(() => { reloadDocs(); onChange(); });
  return (
    <div style={{ padding: 6 }}>
      {manage && (
        <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap', alignItems: 'end' }}>
          <label className="mx-fld">{t('pj.docType')}<select value={kind} onChange={(e) => { setKind(e.currentTarget.value as 'expense' | 'bill'); setPick(''); }}>
            <option value="expense">{t('pj.doc.expense')}</option><option value="bill">{t('pj.doc.bill')}</option></select></label>
          <label className="mx-fld">{t('pj.document')}<select value={pick} onChange={(e) => setPick(e.currentTarget.value)}>
            <option value="">—</option>{docs.map((x) => <option key={x.id} value={x.id}>#{x.number} · {formatDate(x.doc_date)} · {amt(x.amount)} {x.currency} · {x.label}</option>)}</select></label>
          <button className="mx-btn" disabled={!pick} onClick={() => void run(api(`${base}/prj/projects/${projectId}/link`, { method: 'POST', body: { doc_type: kind, doc_id: pick } }), () => setPick(''))}>{t('pj.link')}</button>
        </div>)}
      <Flash m={msg} />
      <table className="mx-table">
        <thead><tr><th>{t('pj.docType')}</th><th>#</th><th>{t('pj.date')}</th><th>{t('pj.label')}</th><th className="num">{t('pj.amount')}</th><th>{t('pj.status')}</th><th /></tr></thead>
        <tbody>
          {links.map((l) => (
            <tr key={l.id}><td>{t(`pj.doc.${l.doc_type}`)}</td><td dir="ltr">{l.number}</td><td>{l.doc_date ? formatDate(l.doc_date) : ''}</td><td>{l.label}</td>
              <td className="num">{amt(l.amount)} {l.currency}</td><td>{l.status}</td>
              <td>{manage && l.doc_type !== 'invoice' && <button className="mx-btn sm" onClick={() => void run(api(`${base}/prj/project-links/${l.id}`, { method: 'DELETE' }))}>{t('pj.unlink')}</button>}</td></tr>))}
          {links.length === 0 && <tr><td colSpan={7} className="mx-empty">{t('pj.none')}</td></tr>}
        </tbody>
      </table>
    </div>
  );
}

function Setup({ me, d, onChange }: { me: Me; d: ProjectDetail; onChange: () => void }) {
  const { t } = useTranslation();
  const base = useBase();
  const p = d.project;
  const [f, setF] = useState({ name: p.name, code: p.code, billing: p.billing, hourly_rate: p.hourly_rate, fixed_price: p.fixed_price, budget: p.budget, budget_hours: p.budget_hours,
    start_date: p.start_date ?? '', end_date: p.end_date ?? '', description: p.description });
  const [stages, setStages] = useState(d.stages.map((s) => ({ id: s.id, name: s.name, is_done: s.is_done })));
  const [msg, run] = useRun(onChange);
  const manage = can(me, 'projects.manage');
  return (
    <div style={{ padding: 6, display: 'flex', flexDirection: 'column', gap: 8 }}>
      <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap', alignItems: 'end' }}>
        <label className="mx-fld">{t('pj.name')}<input disabled={!manage} value={f.name} onChange={(e) => setF({ ...f, name: e.currentTarget.value })} /></label>
        <label className="mx-fld">{t('pj.code')}<input disabled={!manage} style={{ width: 80 }} value={f.code} onChange={(e) => setF({ ...f, code: e.currentTarget.value })} /></label>
        <label className="mx-fld">{t('pj.billing')}<select disabled={!manage} value={f.billing} onChange={(e) => setF({ ...f, billing: e.currentTarget.value })}>
          {BILLINGS.map((b) => <option key={b} value={b}>{t(`pj.bill.${b}`)}</option>)}</select></label>
        <label className="mx-fld">{t('pj.rate')}<input dir="ltr" disabled={!manage} style={{ width: 80 }} value={f.hourly_rate} onChange={(e) => setF({ ...f, hourly_rate: num(e.currentTarget.value) })} /></label>
        <label className="mx-fld">{t('pj.fixed')}<input dir="ltr" disabled={!manage} style={{ width: 90 }} value={f.fixed_price} onChange={(e) => setF({ ...f, fixed_price: num(e.currentTarget.value) })} /></label>
        <label className="mx-fld">{t('pj.budget')}<input dir="ltr" disabled={!manage} style={{ width: 90 }} value={f.budget} onChange={(e) => setF({ ...f, budget: num(e.currentTarget.value) })} /></label>
        <label className="mx-fld">{t('pj.budgetHours')}<input dir="ltr" disabled={!manage} style={{ width: 70 }} value={f.budget_hours} onChange={(e) => setF({ ...f, budget_hours: num(e.currentTarget.value) })} /></label>
        <label className="mx-fld">{t('pj.start')}<input type="date" dir="ltr" disabled={!manage} value={f.start_date} onChange={(e) => setF({ ...f, start_date: e.currentTarget.value })} /></label>
        <label className="mx-fld">{t('pj.end')}<input type="date" dir="ltr" disabled={!manage} value={f.end_date} onChange={(e) => setF({ ...f, end_date: e.currentTarget.value })} /></label>
      </div>
      <label className="mx-fld">{t('pj.description')}<textarea rows={3} disabled={!manage} value={f.description} onChange={(e) => setF({ ...f, description: e.currentTarget.value })} /></label>
      {manage && <button className="mx-btn main" style={{ alignSelf: 'start' }} onClick={() => void run(api(`${base}/prj/projects/${p.id}`, { method: 'PATCH', body: f }))}>{t('pj.save')}</button>}
      <h4 style={{ margin: '8px 0 2px' }}>{t('pj.stages')}</h4>
      {stages.map((s, i) => (
        <div key={s.id || i} className="mx-row" style={{ gap: 6, alignItems: 'center' }}>
          <input disabled={!manage} value={s.name} aria-label={t('pj.stage')} onChange={(e) => setStages(stages.map((x, j) => (j === i ? { ...x, name: e.currentTarget.value } : x)))} />
          <label style={{ display: 'flex', gap: 4, alignItems: 'center' }}><input type="checkbox" disabled={!manage} checked={s.is_done} onChange={(e) => setStages(stages.map((x, j) => (j === i ? { ...x, is_done: e.currentTarget.checked } : x)))} />{t('pj.doneCol')}</label>
          {manage && <button className="mx-btn sm" aria-label={t('pj.remove')} onClick={() => setStages(stages.filter((_, j) => j !== i))}>×</button>}
        </div>))}
      {manage && <div className="mx-row" style={{ gap: 6 }}>
        <button className="mx-btn sm" onClick={() => setStages([...stages, { id: '', name: t('pj.newStage'), is_done: false }])}>+ {t('pj.stage')}</button>
        <button className="mx-btn" onClick={() => void run(api(`${base}/prj/projects/${p.id}/stages`, { method: 'PUT', body: { stages: stages.map((s) => (s.id ? s : { name: s.name, is_done: s.is_done })) } }))}>{t('pj.saveStages')}</button>
      </div>}
      <Flash m={msg} />
      <Chatter type="project" id={p.id} readOnly={!manage} />
    </div>
  );
}

// ======================= my tasks =======================

function MyTasks({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [scope, setScope] = useState<'mine' | 'all'>('mine');
  const [rows, reload] = useLoad<Task[]>(base && `${base}/prj/project-tasks?open=1${scope === 'mine' ? '&mine=1' : ''}`, []);
  const [sel, setSel] = useState<string | null>(null);
  return (
    <section className="mx-box">
      <h3>{t('pj.m.tasks')}</h3>
      <div className="mx-row" style={{ gap: 6, padding: 6 }}>
        <select aria-label={t('pj.scope')} value={scope} onChange={(e) => setScope(e.currentTarget.value as 'mine' | 'all')}>
          <option value="mine">{t('pj.mine')}</option><option value="all">{t('pj.everyone')}</option></select>
      </div>
      <div style={{ overflowX: 'auto' }}>
        <table className="mx-table">
          <thead><tr><th>#</th><th>{t('pj.title')}</th><th>{t('pj.project')}</th><th>{t('pj.stage')}</th><th>{t('pj.priority')}</th><th>{t('pj.deadline')}</th><th>{t('pj.assignee')}</th><th className="num">{t('pj.hours')}</th></tr></thead>
          <tbody>
            {rows.map((x) => (
              <tr key={x.id} style={{ cursor: 'pointer' }} onClick={() => setSel(x.id)}>
                <td dir="ltr">{x.number}</td><td>{x.title}</td><td>{x.project_name}</td><td>{x.stage_name ?? ''}</td>
                <td style={{ color: prioColor[x.priority] }}>{t(`pj.prio.${x.priority}`)}</td><td className={x.late ? 'red' : ''}>{x.deadline ? formatDate(x.deadline) : ''}</td>
                <td>{x.assignee_name ?? ''}</td><td className="num">{h(x.hours)}</td>
              </tr>))}
            {rows.length === 0 && <tr><td colSpan={8} className="mx-empty">{t('pj.none')}</td></tr>}
          </tbody>
        </table>
      </div>
      {sel && <TaskPanel me={me} id={sel} onChange={reload} onClose={() => setSel(null)} onOpen={setSel} />}
    </section>
  );
}

// ======================= timesheets =======================

function Timer({ onChange, tick }: { onChange: () => void; tick: number }) {
  const { t } = useTranslation();
  const base = useBase();
  const [d, reload] = useLoad<{ timer: Timesheet | null } | null>(base && `${base}/prj/timer?r=${tick}`, null);
  const [msg, run] = useRun(() => { reload(); onChange(); });
  const [now, setNow] = useState(Date.now());
  useEffect(() => { const i = window.setInterval(() => setNow(Date.now()), 1000); return () => window.clearInterval(i); }, []);
  const tm = d?.timer;
  if (!tm) return <Flash m={msg} />;
  const secs = Math.max(0, Math.floor((now - new Date(tm.running_since as string).getTime()) / 1000));
  const clock = `${String(Math.floor(secs / 3600)).padStart(2, '0')}:${String(Math.floor((secs % 3600) / 60)).padStart(2, '0')}:${String(secs % 60).padStart(2, '0')}`;
  return (
    <div className="mx-row" style={{ gap: 8, alignItems: 'center', padding: 8, background: '#fff8e6', border: '1px solid #f0d18a', borderRadius: 8, marginBottom: 6 }}>
      <b dir="ltr" style={{ fontSize: 20, fontFamily: 'monospace' }}>{clock}</b>
      <span>{tm.project_name}{tm.task_title ? ` · #${tm.task_number} ${tm.task_title}` : ''}</span>
      <span className="mx-sp" />
      <button className="mx-btn main" onClick={() => void run(api(`${base}/prj/timer/stop`, { method: 'POST', body: {} }))}>■ {t('pj.timer.stop')}</button>
      <Flash m={msg} />
    </div>
  );
}

function Timesheets({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const monday = useMemo(() => { const d = new Date(); d.setDate(d.getDate() - ((d.getDay() + 6) % 7)); return d.toISOString().slice(0, 10); }, []);
  const [f, setF] = useState({ from: monday, to: today(), mine: true, project_id: '' });
  const [d, reload] = useLoad<{ rows: Timesheet[]; total_hours: string; billable_hours: string }>(base && `${base}/prj/timesheets?from=${f.from}&to=${f.to}${f.mine ? '&mine=1' : ''}${f.project_id ? `&project_id=${f.project_id}` : ''}`, { rows: [], total_hours: '0', billable_hours: '0' });
  const [projects] = useLoad<Project[]>(base && `${base}/prj/projects?status=active`, []);
  const [tasks] = useLoad<Task[]>(base && `${base}/prj/project-tasks?open=1`, []);
  const [nf, setNf] = useState({ project_id: '', task_id: '', date: today(), hours: '', description: '', billable: true });
  const [tick, setTick] = useState(0);
  const [msg, run] = useRun(() => { reload(); setTick((x) => x + 1); });
  const create = can(me, 'projects.create');
  return (
    <div>
      <Timer onChange={reload} tick={tick} />
      {create && (
        <section className="mx-box" style={{ marginBottom: 6 }}>
          <h3>{t('pj.logTime')}</h3>
          <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
            <label className="mx-fld">{t('pj.project')}<select value={nf.project_id} onChange={(e) => setNf({ ...nf, project_id: e.currentTarget.value, task_id: '' })}>
              <option value="">—</option>{projects.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}</select></label>
            <label className="mx-fld">{t('pj.task')}<select value={nf.task_id} onChange={(e) => setNf({ ...nf, task_id: e.currentTarget.value })}>
              <option value="">—</option>{tasks.filter((x) => !nf.project_id || x.project_id === nf.project_id).map((x) => <option key={x.id} value={x.id}>#{x.number} {x.title}</option>)}</select></label>
            <label className="mx-fld">{t('pj.date')}<input type="date" dir="ltr" value={nf.date} onChange={(e) => setNf({ ...nf, date: e.currentTarget.value })} /></label>
            <label className="mx-fld">{t('pj.hours')}<input dir="ltr" style={{ width: 60 }} value={nf.hours} onChange={(e) => setNf({ ...nf, hours: num(e.currentTarget.value) })} /></label>
            <label className="mx-fld">{t('pj.what')}<input value={nf.description} onChange={(e) => setNf({ ...nf, description: e.currentTarget.value })} /></label>
            <label className="mx-fld" style={{ flexDirection: 'row', alignItems: 'center', gap: 4 }}><input type="checkbox" checked={nf.billable} onChange={(e) => setNf({ ...nf, billable: e.currentTarget.checked })} />{t('pj.billable')}</label>
            <button className="mx-btn main" disabled={(!nf.project_id && !nf.task_id) || !Number(nf.hours)}
              onClick={() => void run(api(`${base}/prj/timesheets`, { method: 'POST', body: { ...nf, project_id: nf.project_id || undefined, task_id: nf.task_id || undefined } }), () => setNf({ ...nf, hours: '', description: '' }))}>{t('pj.log')}</button>
            <button className="mx-btn" disabled={!nf.project_id && !nf.task_id}
              onClick={() => void run(api(`${base}/prj/timer/start`, { method: 'POST', body: { project_id: nf.project_id || undefined, task_id: nf.task_id || undefined, description: nf.description } }))}>▶ {t('pj.timer.start')}</button>
          </div>
          <Flash m={msg} />
        </section>)}
      <section className="mx-box">
        <h3>{t('pj.m.timesheets')} · {t('pj.totalH', { n: h(d.total_hours), b: h(d.billable_hours) })}</h3>
        <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
          <label className="mx-fld">{t('pj.from')}<input type="date" dir="ltr" value={f.from} onChange={(e) => setF({ ...f, from: e.currentTarget.value })} /></label>
          <label className="mx-fld">{t('pj.to')}<input type="date" dir="ltr" value={f.to} onChange={(e) => setF({ ...f, to: e.currentTarget.value })} /></label>
          <label className="mx-fld">{t('pj.project')}<select value={f.project_id} onChange={(e) => setF({ ...f, project_id: e.currentTarget.value })}>
            <option value="">{t('pj.all')}</option>{projects.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}</select></label>
          <label className="mx-fld" style={{ flexDirection: 'row', alignItems: 'center', gap: 4 }}><input type="checkbox" checked={f.mine} onChange={(e) => setF({ ...f, mine: e.currentTarget.checked })} />{t('pj.mine')}</label>
        </div>
        <div style={{ overflowX: 'auto' }}>
          <table className="mx-table">
            <thead><tr><th>{t('pj.date')}</th><th>{t('pj.employee')}</th><th>{t('pj.project')}</th><th>{t('pj.task')}</th><th>{t('pj.what')}</th><th className="num">{t('pj.hours')}</th><th>{t('pj.billable')}</th><th>{t('pj.invoice')}</th><th /></tr></thead>
            <tbody>
              {d.rows.map((x) => (
                <tr key={x.id}><td>{formatDate(x.work_date)}</td><td>{x.employee_name}</td><td>{x.project_name}</td><td>{x.task_number ? `#${x.task_number} ${x.task_title}` : ''}</td>
                  <td>{x.description}</td><td className="num">{x.running_since ? `⏱ ${formatDateTime(x.running_since)}` : h(x.hours)}</td><td>{x.billable ? '✔' : ''}</td>
                  <td>{x.invoiced ? `#${x.invoice_number}` : ''}</td>
                  <td>{create && !x.invoiced && !x.running_since && <button className="mx-btn sm" onClick={() => void run(api(`${base}/prj/timesheets/${x.id}`, { method: 'DELETE' }))}>{t('pj.remove')}</button>}</td></tr>))}
              {d.rows.length === 0 && <tr><td colSpan={9} className="mx-empty">{t('pj.none')}</td></tr>}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  );
}

// ======================= profitability =======================

function Profitability() {
  const { t } = useTranslation();
  const base = useBase();
  const { company } = useCompany();
  const [status, setStatus] = useState('all');
  const [rows] = useLoad<Summary[]>(base && `${base}/prj/reports/profitability?status=${status}`, []);
  const bc = company?.base_currency ?? '';
  const tot = rows.reduce((a, r) => ({ rev: a.rev + Number(r.revenue), cost: a.cost + Number(r.total_cost) }), { rev: 0, cost: 0 });
  return (
    <section className="mx-box">
      <h3>{t('pj.m.reports')} ({bc})</h3>
      <div className="mx-row" style={{ gap: 6, padding: 6 }}>
        <select aria-label={t('pj.status')} value={status} onChange={(e) => setStatus(e.currentTarget.value)}>{['all', 'open', ...PSTATUS].map((s) => <option key={s} value={s}>{t(`pj.ps.${s}`)}</option>)}</select>
      </div>
      <div style={{ overflowX: 'auto' }}>
        <table className="mx-table">
          <thead><tr><th>{t('pj.project')}</th><th>{t('pj.customer')}</th><th className="num">{t('pj.hours')}</th><th className="num">{t('pj.k.toInvoice')}</th>
            <th className="num">{t('pj.k.labour')}</th><th className="num">{t('pj.k.expenses')}</th><th className="num">{t('pj.k.bills')}</th><th className="num">{t('pj.k.revenue')}</th>
            <th className="num">{t('pj.k.margin')}</th><th className="num">%</th><th className="num">{t('pj.k.budget')}</th></tr></thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.id}><td>{r.name}</td><td>{r.customer_name}</td><td className="num">{h(r.hours)}</td><td className="num">{h(r.to_invoice_hours)}</td>
                <td className="num">{amt(r.labour_cost)}</td><td className="num">{amt(r.expenses_cost)}</td><td className="num">{amt(r.bills_cost)}</td><td className="num">{amt(r.revenue)}</td>
                <td className={`num ${Number(r.margin) < 0 ? 'red' : ''}`}>{amt(r.margin)}</td><td className="num">{r.margin_pct}</td>
                <td className={`num ${Number(r.budget_used_pct) > 100 ? 'red' : ''}`}>{r.budget_used_pct ? `${r.budget_used_pct}%` : ''}</td></tr>))}
            {rows.length === 0 && <tr><td colSpan={11} className="mx-empty">{t('pj.none')}</td></tr>}
          </tbody>
          <tfoot><tr><th colSpan={7}>{t('pj.total')}</th><th className="num">{amt(tot.rev)}</th><th className="num">{amt(tot.rev - tot.cost)}</th><th colSpan={2} /></tr></tfoot>
        </table>
      </div>
    </section>
  );
}
