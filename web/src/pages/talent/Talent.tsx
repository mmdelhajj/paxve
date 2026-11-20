import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, can, type Me } from '../../api';
import { formatAmount, formatDate, formatDateTime } from '../../util';
import Chatter from '../../components/Chatter';
import Attachments from '../../components/Attachments';
import { useBase, useLoad, useRun } from '../wh/Wh';
import { usePeople } from '../prj/Prj';

// Recruitment and appraisals (HR, Magic-style screens): job positions (published on the careers page), the
// applicant pipeline (drag between columns), interviews, offer and hire (→ employee), review cycles with goals,
// self-review and the reviewer's scores, and the reports. SelfAppraisal is the employee's own screen.

type Msg = { ok: boolean; text: string } | null;
function Flash({ m }: { m: Msg }) {
  return m ? <div className={`mx-flash ${m.ok ? 'good' : 'bad'}`}>{m.text}</div> : null;
}

interface Job { id: string; title: string; department: string; location: string; employment: string; description: string; requirements: string; salary_range: string;
  vacancies: number; is_published: boolean; status: string; applicants: number; in_progress: number; hired: number }
interface Applicant { id: string; number: number; job_id: string | null; job_title: string | null; name: string; email: string; phone: string; source: string; stage: string;
  rating: number; cover_letter: string; expected_salary: string; notes: string; offer_salary: string | null; offer_currency: string | null; offer_start: string | null;
  offer_sent_at: string | null; hired_employee_id: string | null; employee_code: string | null; refuse_reason: string; created_at: string; files: number;
  next_interview: string | null; interview_rating: string | null }
interface Interview { id: string; scheduled_at: string; duration_min: number; interviewer_id: string | null; interviewer_name: string | null; kind: string; location: string;
  status: string; rating: number | null; feedback: string }
interface Goal { title: string; weight: number | string; target?: string; self?: number | null; manager?: number | null; note?: string }
interface Appraisal { id: string; cycle_id: string; cycle_name: string; period_from: string; period_to: string; due_date: string | null; cycle_status: string; self_review: boolean;
  employee_id: string; employee_code: string; employee_name: string; position: string; reviewer_id: string | null; reviewer_name: string | null; goals: Goal[];
  self_comment: string; manager_comment: string; strengths: string; improvements: string; self_score: string | null; manager_score: string | null; final_score: string | null;
  status: string }
interface Cycle { id: string; name: string; period_from: string; period_to: string; due_date: string | null; self_review: boolean; status: string; appraisals: number; reviewed: number; avg_score: string | null }

const STAGES = ['new', 'screening', 'interview', 'offer', 'hired', 'refused'];
const MODES = ['positions', 'applicants', 'appraisals', 'reports'] as const;
type Mode = typeof MODES[number];
const EMP = ['full_time', 'part_time', 'contract', 'internship', 'temporary'];
const Stars = ({ n }: { n: number }) => <span style={{ color: '#f5a524', letterSpacing: 1 }} aria-label={`${n}/5`}>{'★'.repeat(n)}<span style={{ color: '#cbd5e1' }}>{'★'.repeat(5 - n)}</span></span>;

export default function Talent({ me, initial }: { me: Me; initial?: string }) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<Mode>((MODES as readonly string[]).includes(initial ?? '') ? initial as Mode : 'applicants');
  return (
    <div className="mx-col">
      <div className="mx-strip"><b>{t(`tl.m.${mode}`)}</b><span className="mx-sp" /><small style={{ opacity: 0.8 }}>{t(`tl.help.${mode}`)}</small></div>
      <div className="mx-modes" role="tablist">
        {MODES.map((m) => <button key={m} role="tab" className="mx-mode" aria-selected={mode === m} onClick={() => setMode(m)}>{t(`tl.m.${m}`)}</button>)}
      </div>
      <div style={{ flex: 1, minHeight: 0, overflow: 'auto', padding: 6 }}>
        {mode === 'positions' && <Positions me={me} />}
        {mode === 'applicants' && <Pipeline me={me} />}
        {mode === 'appraisals' && <Appraisals me={me} />}
        {mode === 'reports' && <Reports />}
      </div>
    </div>
  );
}

// ======================= positions =======================

function Positions({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [rows, reload] = useLoad<Job[]>(base && `${base}/hr/job-positions`, []);
  const [st] = useLoad<{ settings: { public_slug: string | null; careers_enabled: boolean }; public_url: string } | null>(base && `${base}/shop/settings`, null);
  const [msg, run] = useRun(reload);
  const [sel, setSel] = useState<string | null>(null);
  const [title, setTitle] = useState('');
  const manage = can(me, 'talent.manage');
  const j = rows.find((x) => x.id === sel);
  const careers = st?.settings.public_slug ? `${st.public_url}/#/careers/${st.settings.public_slug}` : '';
  const patch = (id: string, body: Record<string, unknown>) => void run(api(`${base}/hr/job-positions/${id}`, { method: 'PATCH', body }));
  return (
    <div className="mx-row" style={{ gap: 8, alignItems: 'flex-start', flexWrap: 'wrap' }}>
      <section className="mx-box" style={{ flex: '1 1 460px', minWidth: 0 }}>
        <h3>{t('tl.m.positions')}</h3>
        <div style={{ padding: 6 }}>{careers && st?.settings.careers_enabled
          ? <small>{t('tl.careersAt')} <a href={careers} target="_blank" rel="noopener noreferrer" dir="ltr">{careers}</a></small>
          : <small className="warn">{t('tl.careersOff')} <a href="#/webshop/settings">{t('tl.setUp')}</a></small>}</div>
        {manage && <div className="mx-row" style={{ gap: 6, padding: 6 }}>
          <input aria-label={t('tl.title')} placeholder={t('tl.newPosition')} value={title} onChange={(e) => setTitle(e.currentTarget.value)} />
          <button className="mx-btn main" disabled={!title.trim()} onClick={() => void run(api<Job>(`${base}/hr/job-positions`, { method: 'POST', body: { title } }), (x) => { setSel((x as Job).id); setTitle(''); })}>{t('tl.add')}</button>
        </div>}
        <Flash m={msg} />
        <table className="mx-table">
          <thead><tr><th>{t('tl.title')}</th><th>{t('tl.department')}</th><th>{t('tl.published')}</th><th className="num">{t('tl.applicants')}</th><th className="num">{t('tl.hired')}</th><th>{t('tl.status')}</th></tr></thead>
          <tbody>{rows.map((x) => <tr key={x.id} className={sel === x.id ? 'sel' : ''} style={{ cursor: 'pointer', opacity: x.status === 'open' ? 1 : 0.55 }} onClick={() => setSel(x.id)}>
            <td><b>{x.title}</b></td><td>{x.department}</td><td>{x.is_published ? '🌐' : ''}</td><td className="num">{x.applicants}</td><td className="num">{x.hired}/{x.vacancies}</td>
            <td>{t(`tl.js.${x.status}`)}</td></tr>)}
            {rows.length === 0 && <tr><td colSpan={6} className="mx-empty">{t('tl.none')}</td></tr>}</tbody>
        </table>
      </section>
      {j && (
        <section className="mx-box" style={{ flex: '1 1 460px', minWidth: 0 }}>
          <h3 style={{ display: 'flex', gap: 6 }}>{j.title}<span className="mx-sp" /><button className="mx-btn sm" onClick={() => setSel(null)}>{t('tl.close')}</button></h3>
          <div style={{ padding: 6, display: 'flex', flexDirection: 'column', gap: 6 }} key={j.id}>
            <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap', alignItems: 'end' }}>
              <label className="mx-fld">{t('tl.title')}<input disabled={!manage} defaultValue={j.title} onBlur={(e) => { if (e.currentTarget.value.trim() && e.currentTarget.value !== j.title) patch(j.id, { title: e.currentTarget.value }); }} /></label>
              <label className="mx-fld">{t('tl.department')}<input disabled={!manage} defaultValue={j.department} onBlur={(e) => { if (e.currentTarget.value !== j.department) patch(j.id, { department: e.currentTarget.value }); }} /></label>
              <label className="mx-fld">{t('tl.location')}<input disabled={!manage} defaultValue={j.location} onBlur={(e) => { if (e.currentTarget.value !== j.location) patch(j.id, { location: e.currentTarget.value }); }} /></label>
              <label className="mx-fld">{t('tl.employment')}<select disabled={!manage} value={j.employment} onChange={(e) => patch(j.id, { employment: e.currentTarget.value })}>{EMP.map((x) => <option key={x} value={x}>{t(`tl.emp.${x}`)}</option>)}</select></label>
              <label className="mx-fld">{t('tl.salaryRange')}<input disabled={!manage} defaultValue={j.salary_range} onBlur={(e) => { if (e.currentTarget.value !== j.salary_range) patch(j.id, { salary_range: e.currentTarget.value }); }} /></label>
              <label className="mx-fld">{t('tl.vacancies')}<input dir="ltr" style={{ width: 60 }} disabled={!manage} defaultValue={j.vacancies} onBlur={(e) => { const n = Number(e.currentTarget.value); if (n >= 1 && n !== j.vacancies) patch(j.id, { vacancies: n }); }} /></label>
            </div>
            <label className="mx-fld">{t('tl.description')}<textarea rows={5} disabled={!manage} defaultValue={j.description} onBlur={(e) => { if (e.currentTarget.value !== j.description) patch(j.id, { description: e.currentTarget.value }); }} /></label>
            <label className="mx-fld">{t('tl.requirements')}<textarea rows={3} disabled={!manage} defaultValue={j.requirements} onBlur={(e) => { if (e.currentTarget.value !== j.requirements) patch(j.id, { requirements: e.currentTarget.value }); }} /></label>
            <div className="mx-row" style={{ gap: 6 }}>
              <label className="mx-fld" style={{ flexDirection: 'row', gap: 4, alignItems: 'center' }}><input type="checkbox" disabled={!manage} checked={j.is_published} onChange={(e) => patch(j.id, { is_published: e.currentTarget.checked })} />{t('tl.publish')}</label>
              {manage && <button className="mx-btn" onClick={() => patch(j.id, { status: j.status === 'open' ? 'closed' : 'open' })}>{j.status === 'open' ? t('tl.closeJob') : t('tl.reopen')}</button>}
            </div>
          </div>
        </section>)}
    </div>
  );
}

// ======================= pipeline =======================

function Pipeline({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [job, setJob] = useState('');
  const [q, setQ] = useState('');
  const [jobs] = useLoad<Job[]>(base && `${base}/hr/job-positions`, []);
  const [rows, reload] = useLoad<Applicant[]>(base && `${base}/hr/applicants?job_id=${job}&q=${encodeURIComponent(q)}`, []);
  const [msg, run] = useRun(reload);
  const [sel, setSel] = useState<string | null>(null);
  const [nf, setNf] = useState({ name: '', phone: '', email: '' });
  const [drag, setDrag] = useState<string | null>(null);
  const edit = can(me, 'talent.edit');
  const move = (id: string, stage: string) => void run(api(`${base}/hr/applicants/${id}`, { method: 'PATCH', body: { stage } }));
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <section className="mx-box">
        <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
          <label className="mx-fld">{t('tl.position')}<select value={job} onChange={(e) => setJob(e.currentTarget.value)}><option value="">{t('tl.all')}</option>
            {jobs.map((j) => <option key={j.id} value={j.id}>{j.title}</option>)}</select></label>
          <label className="mx-fld">{t('tl.search')}<input value={q} onChange={(e) => setQ(e.currentTarget.value)} /></label>
          <span className="mx-sp" />
          {edit && <>
            <label className="mx-fld">{t('tl.name')}<input value={nf.name} onChange={(e) => setNf({ ...nf, name: e.currentTarget.value })} /></label>
            <label className="mx-fld">{t('tl.phone')}<input dir="ltr" style={{ width: 110 }} value={nf.phone} onChange={(e) => setNf({ ...nf, phone: e.currentTarget.value })} /></label>
            <label className="mx-fld">{t('tl.email')}<input dir="ltr" style={{ width: 150 }} value={nf.email} onChange={(e) => setNf({ ...nf, email: e.currentTarget.value })} /></label>
            <button className="mx-btn main" disabled={!nf.name.trim()} onClick={() => void run(api(`${base}/hr/applicants`, { method: 'POST', body: { ...nf, job_id: job || undefined, source: 'manual' } }),
              () => setNf({ name: '', phone: '', email: '' }))}>{t('tl.addApplicant')}</button>
          </>}
        </div>
        <Flash m={msg} />
      </section>
      <div style={{ display: 'grid', gridTemplateColumns: `repeat(${STAGES.length}, minmax(190px, 1fr))`, gap: 6, overflowX: 'auto', alignItems: 'start' }}>
        {STAGES.map((s) => {
          const list = rows.filter((a) => a.stage === s);
          return (
            <div key={s} className="mx-box" style={{ minHeight: 120, background: drag ? '#f8fafc' : undefined }}
              onDragOver={(e) => { if (edit && s !== 'hired') e.preventDefault(); }}
              onDrop={() => { if (drag && s !== 'hired') move(drag, s); setDrag(null); }}>
              <h3>{t(`tl.st.${s}`)} <small>({list.length})</small></h3>
              <div style={{ padding: 4, display: 'flex', flexDirection: 'column', gap: 4 }}>
                {list.map((a) => (
                  <div key={a.id} draggable={edit && a.stage !== 'hired'} onDragStart={() => setDrag(a.id)} onDragEnd={() => setDrag(null)} onClick={() => setSel(a.id)}
                    style={{ border: '1px solid #dde3ea', borderRadius: 6, padding: 6, background: sel === a.id ? '#eef3ff' : '#fff', cursor: 'pointer' }}>
                    <b>{a.name}</b><div><small>{a.job_title ?? '—'}</small></div>
                    <div style={{ display: 'flex', gap: 4, alignItems: 'center', flexWrap: 'wrap' }}><Stars n={a.rating} />{a.files > 0 && <small>📎{a.files}</small>}
                      {a.source === 'careers' && <small>🌐</small>}{a.next_interview && <small>📅 {formatDateTime(a.next_interview)}</small>}</div>
                  </div>))}
              </div>
            </div>);
        })}
      </div>
      {sel && <ApplicantPanel me={me} id={sel} onChange={reload} onClose={() => setSel(null)} />}
    </div>
  );
}

function ApplicantPanel({ me, id, onChange, onClose }: { me: Me; id: string; onChange: () => void; onClose: () => void }) {
  const { t } = useTranslation();
  const base = useBase();
  const people = usePeople();
  const [d, reload] = useLoad<{ applicant: Applicant; interviews: Interview[] } | null>(base && `${base}/hr/applicants/${id}`, null);
  const [msg, run] = useRun(() => { reload(); onChange(); });
  const [iv, setIv] = useState({ at: '', interviewer_id: '', kind: 'onsite', location: '' });
  const [offer, setOffer] = useState({ salary: '', currency: '', start: '' });
  if (!d) return null;
  const a = d.applicant;
  const edit = can(me, 'talent.edit') && a.stage !== 'hired';
  const manage = can(me, 'talent.manage');
  const patch = (body: Record<string, unknown>) => void run(api(`${base}/hr/applicants/${id}`, { method: 'PATCH', body }));
  return (
    <section className="mx-box">
      <h3 style={{ display: 'flex', gap: 6, alignItems: 'center', flexWrap: 'wrap' }}>#{a.number} {a.name} <small>{a.job_title ?? ''}</small>
        <span className="mx-pill">{t(`tl.st.${a.stage}`)}</span><span className="mx-sp" /><button className="mx-btn sm" onClick={onClose}>{t('tl.close')}</button></h3>
      <div className="mx-row" style={{ gap: 8, padding: 6, alignItems: 'flex-start', flexWrap: 'wrap' }}>
        <div style={{ flex: '1 1 380px', display: 'flex', flexDirection: 'column', gap: 6, minWidth: 0 }}>
          <div>📞 <span dir="ltr">{a.phone}</span> · ✉ <span dir="ltr">{a.email}</span> · {t(`tl.src.${a.source}`)} · {formatDate(a.created_at)}</div>
          <div role="radiogroup" aria-label={t('tl.rating')} style={{ display: 'flex', gap: 2 }}>{[1, 2, 3, 4, 5].map((n) => (
            <button key={n} role="radio" aria-checked={a.rating === n} disabled={!edit} onClick={() => patch({ rating: a.rating === n ? 0 : n })}
              style={{ background: 'none', border: 0, fontSize: 22, cursor: edit ? 'pointer' : 'default', color: n <= a.rating ? '#f5a524' : '#cbd5e1' }}>★</button>))}</div>
          {a.expected_salary && <div>{t('tl.expected')}: <b>{a.expected_salary}</b></div>}
          {a.cover_letter && <details open><summary>{t('tl.letter')}</summary><p style={{ whiteSpace: 'pre-wrap', margin: 0 }}>{a.cover_letter}</p></details>}
          <label className="mx-fld">{t('tl.notes')}<textarea rows={3} disabled={!edit} defaultValue={a.notes} key={a.notes} onBlur={(e) => { if (e.currentTarget.value !== a.notes) patch({ notes: e.currentTarget.value }); }} /></label>
          <Flash m={msg} />
          {a.stage === 'hired' ? <div className="mx-flash good">{t('tl.hiredAs', { code: a.employee_code ?? '' })} <a href="#/hr/people">{t('tl.openEmployees')}</a></div> : manage && <>
            <b>{t('tl.offer')}</b>
            <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap', alignItems: 'end' }}>
              <label className="mx-fld">{t('tl.salary')}<input dir="ltr" style={{ width: 90 }} value={offer.salary || (a.offer_salary ? String(Number(a.offer_salary)) : '')}
                onChange={(e) => setOffer({ ...offer, salary: e.currentTarget.value.replace(/[^\d.]/g, '') })} /></label>
              <label className="mx-fld">{t('tl.currency')}<input dir="ltr" style={{ width: 60 }} value={offer.currency || a.offer_currency || ''} onChange={(e) => setOffer({ ...offer, currency: e.currentTarget.value.toUpperCase().slice(0, 3) })} /></label>
              <label className="mx-fld">{t('tl.start')}<input type="date" dir="ltr" value={offer.start || a.offer_start || ''} onChange={(e) => setOffer({ ...offer, start: e.currentTarget.value })} /></label>
              <button className="mx-btn" onClick={() => void run(api(`${base}/hr/applicants/${id}/offer`, { method: 'POST', body: { salary: offer.salary || a.offer_salary || '', currency: offer.currency || a.offer_currency || '', start: offer.start || a.offer_start || '' } }))}>{t('tl.sendOffer')}</button>
              {can(me, 'payroll.create') && <button className="mx-btn main" onClick={() => { if (window.confirm(t('tl.hireQ', { name: a.name }))) void run(api(`${base}/hr/applicants/${id}/hire`, { method: 'POST', body: { salary: offer.salary, currency: offer.currency, start: offer.start } })); }}>{t('tl.hire')}</button>}
              {a.stage !== 'refused' && <button className="mx-btn" style={{ color: '#b42318' }} onClick={() => { const r = window.prompt(t('tl.refuseWhy')); if (r !== null) void run(api(`${base}/hr/applicants/${id}/refuse`, { method: 'POST', body: { reason: r } })); }}>{t('tl.refuse')}</button>}
            </div>
            {a.offer_sent_at && <small>{t('tl.offerSent', { at: formatDateTime(a.offer_sent_at) })}</small>}
          </>}
          <Attachments type="applicant" id={a.id} readOnly={!edit} />
        </div>
        <div style={{ flex: '1 1 380px', display: 'flex', flexDirection: 'column', gap: 6, minWidth: 0 }}>
          <b>{t('tl.interviews')}</b>
          {d.interviews.map((x) => (
            <div key={x.id} className="mx-box" style={{ padding: 6 }}>
              <div>📅 {formatDateTime(x.scheduled_at)} · {t(`tl.kind.${x.kind}`)} · {x.interviewer_name ?? ''} {x.location && `· ${x.location}`} · <b>{t(`tl.is.${x.status}`)}</b></div>
              {edit && <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap', alignItems: 'end' }}>
                <label className="mx-fld">{t('tl.rating')}<select value={x.rating ?? ''} onChange={(e) => void run(api(`${base}/hr/interviews/${x.id}`, { method: 'PATCH', body: { rating: Number(e.currentTarget.value), status: 'done' } }))}>
                  <option value="">—</option>{[1, 2, 3, 4, 5].map((n) => <option key={n} value={n}>{'★'.repeat(n)}</option>)}</select></label>
                <label className="mx-fld" style={{ flex: 1 }}>{t('tl.feedback')}<input defaultValue={x.feedback} key={x.feedback} onBlur={(e) => { if (e.currentTarget.value !== x.feedback) void run(api(`${base}/hr/interviews/${x.id}`, { method: 'PATCH', body: { feedback: e.currentTarget.value } })); }} /></label>
                {x.status === 'planned' && <button className="mx-btn sm" onClick={() => void run(api(`${base}/hr/interviews/${x.id}`, { method: 'PATCH', body: { status: 'cancelled' } }))}>{t('tl.cancelIv')}</button>}
              </div>}
              {!edit && x.feedback && <small>{x.feedback}</small>}
            </div>))}
          {edit && <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap', alignItems: 'end' }}>
            <label className="mx-fld">{t('tl.when')}<input type="datetime-local" dir="ltr" value={iv.at} onChange={(e) => setIv({ ...iv, at: e.currentTarget.value })} /></label>
            <label className="mx-fld">{t('tl.interviewer')}<select value={iv.interviewer_id} onChange={(e) => setIv({ ...iv, interviewer_id: e.currentTarget.value })}><option value="">—</option>
              {people.map((u) => <option key={u.id} value={u.id}>{u.full_name}</option>)}</select></label>
            <label className="mx-fld">{t('tl.kindL')}<select value={iv.kind} onChange={(e) => setIv({ ...iv, kind: e.currentTarget.value })}>{['onsite', 'phone', 'video'].map((k) => <option key={k} value={k}>{t(`tl.kind.${k}`)}</option>)}</select></label>
            <label className="mx-fld">{t('tl.location')}<input value={iv.location} onChange={(e) => setIv({ ...iv, location: e.currentTarget.value })} /></label>
            <button className="mx-btn main" disabled={!iv.at} onClick={() => void run(api(`${base}/hr/interviews`, { method: 'POST', body: { applicant_id: id, scheduled_at: new Date(iv.at).toISOString(),
              interviewer_id: iv.interviewer_id || undefined, kind: iv.kind, location: iv.location } }), () => setIv({ at: '', interviewer_id: '', kind: 'onsite', location: '' }))}>{t('tl.schedule')}</button>
          </div>}
          <Chatter type="applicant" id={a.id} readOnly={!can(me, 'talent.edit')} />
        </div>
      </div>
    </section>
  );
}

// ======================= appraisals =======================

function score(gs: Goal[], side: 'self' | 'manager'): string {
  let sum = 0;
  let w = 0;
  for (const g of gs) {
    const v = g[side];
    if (v == null || Number.isNaN(Number(v))) continue;
    const wt = Number(g.weight) > 0 ? Number(g.weight) : 1;
    sum += Number(v) * wt;
    w += wt;
  }
  return w ? (sum / w).toFixed(2) : '—';
}

function Appraisals({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const people = usePeople();
  const [cycles, reloadC] = useLoad<Cycle[]>(base && `${base}/hr/appraisal-cycles`, []);
  const [cy, setCy] = useState('');
  const cur = cycles.find((c) => c.id === cy) ?? cycles[0];
  const [rows, reload] = useLoad<Appraisal[]>(base && cur && `${base}/hr/appraisals?cycle_id=${cur.id}`, []);
  const [msg, run] = useRun(() => { reload(); reloadC(); });
  const [nf, setNf] = useState({ name: '', period_from: '', period_to: '', due_date: '' });
  const [goals, setGoals] = useState<Goal[]>([{ title: '', weight: 50 }, { title: '', weight: 50 }]);
  const [reviewer, setReviewer] = useState('');
  const [sel, setSel] = useState<string | null>(null);
  const manage = can(me, 'talent.manage');
  const a = rows.find((x) => x.id === sel);
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <section className="mx-box">
        <h3>{t('tl.cycles')}</h3>
        <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
          <select aria-label={t('tl.cycle')} value={cur?.id ?? ''} onChange={(e) => setCy(e.currentTarget.value)}>
            {cycles.map((c) => <option key={c.id} value={c.id}>{c.name} ({t(`tl.cs.${c.status}`)})</option>)}</select>
          {cur && <small>{formatDate(cur.period_from)} → {formatDate(cur.period_to)} · {t('tl.reviewedN', { r: cur.reviewed, n: cur.appraisals })}{cur.avg_score ? ` · ${t('tl.avg')} ${cur.avg_score}` : ''}</small>}
          <span className="mx-sp" />
          {manage && cur?.status === 'open' && <button className="mx-btn" onClick={() => { if (window.confirm(t('tl.closeCycleQ'))) void run(api(`${base}/hr/appraisal-cycles/${cur.id}/close`, { method: 'POST', body: {} })); }}>{t('tl.closeCycle')}</button>}
        </div>
        {manage && <details style={{ padding: 6 }}><summary>{t('tl.newCycle')}</summary>
          <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap', alignItems: 'end' }}>
            <label className="mx-fld">{t('tl.name')}<input value={nf.name} onChange={(e) => setNf({ ...nf, name: e.currentTarget.value })} /></label>
            <label className="mx-fld">{t('tl.from')}<input type="date" dir="ltr" value={nf.period_from} onChange={(e) => setNf({ ...nf, period_from: e.currentTarget.value })} /></label>
            <label className="mx-fld">{t('tl.to')}<input type="date" dir="ltr" value={nf.period_to} onChange={(e) => setNf({ ...nf, period_to: e.currentTarget.value })} /></label>
            <label className="mx-fld">{t('tl.due')}<input type="date" dir="ltr" value={nf.due_date} onChange={(e) => setNf({ ...nf, due_date: e.currentTarget.value })} /></label>
            <label className="mx-fld">{t('tl.reviewer')}<select value={reviewer} onChange={(e) => setReviewer(e.currentTarget.value)}><option value="">{t('tl.me')}</option>
              {people.map((u) => <option key={u.id} value={u.id}>{u.full_name}</option>)}</select></label>
          </div>
          <b>{t('tl.goals')}</b>
          {goals.map((g, i) => (
            <div key={i} className="mx-row" style={{ gap: 6, alignItems: 'end' }}>
              <label className="mx-fld" style={{ flex: 1 }}>{t('tl.goal')}<input value={g.title} onChange={(e) => setGoals(goals.map((x, j) => (j === i ? { ...x, title: e.currentTarget.value } : x)))} /></label>
              <label className="mx-fld">{t('tl.weight')}<input dir="ltr" style={{ width: 60 }} value={g.weight} onChange={(e) => setGoals(goals.map((x, j) => (j === i ? { ...x, weight: e.currentTarget.value.replace(/[^\d]/g, '') } : x)))} /></label>
              <button className="mx-btn sm" onClick={() => setGoals(goals.filter((_, j) => j !== i))}>✕</button>
            </div>))}
          <button className="mx-btn sm" onClick={() => setGoals([...goals, { title: '', weight: 0 }])}>+ {t('tl.goal')}</button>
          <div><button className="mx-btn main" disabled={!nf.name.trim() || !nf.period_from || !nf.period_to} onClick={async () => {
            const g = goals.filter((x) => x.title.trim()).map((x) => ({ title: x.title.trim(), weight: Number(x.weight) || 0 }));
            await run(api<Cycle>(`${base}/hr/appraisal-cycles`, { method: 'POST', body: { name: nf.name, period_from: nf.period_from, period_to: nf.period_to, due_date: nf.due_date } })
              .then((c) => api<Cycle>(`${base}/hr/appraisal-cycles/${c.id}/open`, { method: 'POST', body: { goals: g, reviewer_id: reviewer } })), (x) => setCy((x as Cycle).id));
          }}>{t('tl.openCycle')}</button></div>
        </details>}
        <Flash m={msg} />
      </section>
      <div className="mx-row" style={{ gap: 8, alignItems: 'flex-start', flexWrap: 'wrap' }}>
        <section className="mx-box" style={{ flex: '1 1 460px', minWidth: 0 }}>
          <table className="mx-table">
            <thead><tr><th>{t('tl.employee')}</th><th>{t('tl.reviewer')}</th><th className="num">{t('tl.selfScore')}</th><th className="num">{t('tl.mgrScore')}</th><th className="num">{t('tl.final')}</th><th>{t('tl.status')}</th></tr></thead>
            <tbody>{rows.map((x) => <tr key={x.id} className={sel === x.id ? 'sel' : ''} style={{ cursor: 'pointer' }} onClick={() => setSel(x.id)}>
              <td><b>{x.employee_name}</b> <small dir="ltr">{x.employee_code}</small></td><td>{x.reviewer_name ?? ''}</td>
              <td className="num">{x.self_score ?? ''}</td><td className="num">{x.manager_score ?? ''}</td><td className="num"><b>{x.final_score ?? ''}</b></td><td>{t(`tl.as.${x.status}`)}</td></tr>)}
              {rows.length === 0 && <tr><td colSpan={6} className="mx-empty">{t('tl.none')}</td></tr>}</tbody>
          </table>
        </section>
        {a && <ReviewPanel me={me} a={a} onDone={reload} onClose={() => setSel(null)} />}
      </div>
    </div>
  );
}

function ReviewPanel({ me, a, onDone, onClose }: { me: Me; a: Appraisal; onDone: () => void; onClose: () => void }) {
  const { t } = useTranslation();
  const base = useBase();
  const [goals, setGoals] = useState<Goal[]>(a.goals ?? []);
  const [f, setF] = useState({ manager_comment: a.manager_comment, strengths: a.strengths, improvements: a.improvements });
  const [msg, run] = useRun(onDone);
  const edit = can(me, 'talent.edit') && a.status !== 'closed' && a.cycle_status !== 'closed';
  const save = (submit: boolean) => void run(api(`${base}/hr/appraisals/${a.id}`, { method: 'PATCH', body: {
    goals: goals.map((g) => ({ title: g.title, weight: Number(g.weight) || 0, target: g.target ?? '', manager: g.manager == null || g.manager === ('' as unknown) ? null : Number(g.manager), note: g.note ?? '' })),
    ...f, submit } }));
  return (
    <section className="mx-box" style={{ flex: '1 1 520px', minWidth: 0 }} key={a.id}>
      <h3 style={{ display: 'flex', gap: 6 }}>{a.employee_name} · {a.cycle_name}<span className="mx-sp" /><button className="mx-btn sm" onClick={onClose}>{t('tl.close')}</button></h3>
      <div style={{ padding: 6, display: 'flex', flexDirection: 'column', gap: 6 }}>
        <table className="mx-table">
          <thead><tr><th>{t('tl.goal')}</th><th className="num">{t('tl.weight')}</th><th className="num">{t('tl.selfScore')}</th><th className="num">{t('tl.mgrScore')}</th></tr></thead>
          <tbody>{goals.map((g, i) => <tr key={i}>
            <td>{edit ? <input value={g.title} onChange={(e) => setGoals(goals.map((x, j) => (j === i ? { ...x, title: e.currentTarget.value } : x)))} /> : g.title}</td>
            <td className="num">{edit ? <input dir="ltr" style={{ width: 50 }} value={g.weight} onChange={(e) => setGoals(goals.map((x, j) => (j === i ? { ...x, weight: e.currentTarget.value.replace(/[^\d]/g, '') } : x)))} /> : g.weight}</td>
            <td className="num">{g.self ?? '—'}</td>
            <td className="num">{edit ? <select value={g.manager ?? ''} onChange={(e) => setGoals(goals.map((x, j) => (j === i ? { ...x, manager: e.currentTarget.value === '' ? null : Number(e.currentTarget.value) } : x)))}>
              <option value="">—</option>{[1, 2, 3, 4, 5].map((n) => <option key={n} value={n}>{n}</option>)}</select> : g.manager ?? '—'}</td>
          </tr>)}
            <tr><td><b>{t('tl.total')}</b>{edit && <button className="mx-btn sm" onClick={() => setGoals([...goals, { title: '', weight: 0 }])}>+</button>}</td><td />
              <td className="num"><b>{score(goals, 'self')}</b></td><td className="num"><b>{score(goals, 'manager')}</b></td></tr></tbody>
        </table>
        {a.self_comment && <div className="mx-box" style={{ padding: 6 }}><small>{t('tl.selfComment')}</small><p style={{ margin: 0, whiteSpace: 'pre-wrap' }}>{a.self_comment}</p></div>}
        <label className="mx-fld">{t('tl.strengths')}<textarea rows={2} disabled={!edit} value={f.strengths} onChange={(e) => setF({ ...f, strengths: e.currentTarget.value })} /></label>
        <label className="mx-fld">{t('tl.improvements')}<textarea rows={2} disabled={!edit} value={f.improvements} onChange={(e) => setF({ ...f, improvements: e.currentTarget.value })} /></label>
        <label className="mx-fld">{t('tl.mgrComment')}<textarea rows={3} disabled={!edit} value={f.manager_comment} onChange={(e) => setF({ ...f, manager_comment: e.currentTarget.value })} /></label>
        <Flash m={msg} />
        {edit && <div className="mx-row" style={{ gap: 6 }}><button className="mx-btn" onClick={() => save(false)}>{t('tl.save')}</button><button className="mx-btn main" onClick={() => save(true)}>{t('tl.submitReview')}</button></div>}
      </div>
    </section>
  );
}

// ======================= reports =======================

function Reports() {
  const { t } = useTranslation();
  const base = useBase();
  const [rec] = useLoad<{ jobs: { id: string; title: string; status: string; vacancies: number; applicants: number; new: number; screening: number; interview: number; offer: number; hired: number; refused: number; days_to_hire: string | null }[];
    sources: { source: string; applicants: number; hired: number }[] } | null>(base && `${base}/hr/recruitment-report`, null);
  const [ap] = useLoad<{ rows: { employee_id: string; code: string; name: string; position: string; cycle_name: string; self_score: string | null; manager_score: string | null; score: string | null; status: string }[];
    summary: { appraisals: number; average: string | null; outstanding: number; strong: number; meets: number; below: number; pending: number } } | null>(base && `${base}/hr/appraisal-report`, null);
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <section className="mx-box"><h3>{t('tl.recruitReport')}</h3>
        <div style={{ overflowX: 'auto' }}><table className="mx-table">
          <thead><tr><th>{t('tl.position')}</th>{STAGES.map((s) => <th key={s} className="num">{t(`tl.st.${s}`)}</th>)}<th className="num">{t('tl.daysToHire')}</th></tr></thead>
          <tbody>{(rec?.jobs ?? []).map((j) => <tr key={j.id}><td><b>{j.title}</b> <small>{t(`tl.js.${j.status}`)}</small></td>
            {STAGES.map((s) => <td key={s} className="num">{(j as unknown as Record<string, number>)[s] || ''}</td>)}<td className="num">{j.days_to_hire ?? ''}</td></tr>)}</tbody>
        </table></div>
        <div style={{ padding: 6 }}>{(rec?.sources ?? []).map((s) => <span key={s.source} className="mx-pill">{t(`tl.src.${s.source}`)}: {s.applicants} / {s.hired} {t('tl.hired')}</span>)}</div>
      </section>
      <section className="mx-box"><h3>{t('tl.appraisalReport')}</h3>
        {ap && <div style={{ padding: 6 }}>{t('tl.avg')} <b>{ap.summary.average ?? '—'}</b> · {t('tl.band.outstanding')} {ap.summary.outstanding} · {t('tl.band.strong')} {ap.summary.strong} · {t('tl.band.meets')} {ap.summary.meets} · {t('tl.band.below')} {ap.summary.below} · {t('tl.band.pending')} {ap.summary.pending}</div>}
        <table className="mx-table">
          <thead><tr><th>{t('tl.employee')}</th><th>{t('tl.cycle')}</th><th className="num">{t('tl.selfScore')}</th><th className="num">{t('tl.mgrScore')}</th><th className="num">{t('tl.final')}</th></tr></thead>
          <tbody>{(ap?.rows ?? []).map((r, i) => <tr key={i}><td>{r.name} <small>{r.position}</small></td><td>{r.cycle_name}</td><td className="num">{r.self_score ?? ''}</td><td className="num">{r.manager_score ?? ''}</td>
            <td className="num"><b>{r.score ? formatAmount(r.score, 2) : ''}</b></td></tr>)}</tbody>
        </table>
      </section>
    </div>
  );
}

// ======================= the employee's own appraisal =======================

export function SelfAppraisal() {
  const { t } = useTranslation();
  const base = useBase();
  const [rows, reload] = useLoad<Appraisal[]>(base && `${base}/self/appraisals`, []);
  const [msg, run] = useRun(reload);
  const [draft, setDraft] = useState<Record<string, { goals: Goal[]; comment: string }>>({});
  if (rows.length === 0) return <div className="mx-box" style={{ margin: 8, padding: 12 }}>{t('tl.selfNone')}</div>;
  return (
    <div style={{ padding: 8, display: 'flex', flexDirection: 'column', gap: 8, maxWidth: 820 }}>
      {rows.map((a) => {
        const d = draft[a.id] ?? { goals: a.goals ?? [], comment: a.self_comment };
        const open = a.cycle_status === 'open' && a.self_review && (a.status === 'pending' || a.status === 'self_done');
        const set = (x: Partial<typeof d>) => setDraft({ ...draft, [a.id]: { ...d, ...x } });
        const save = (submit: boolean) => void run(api(`${base}/self/appraisals/${a.id}`, { method: 'PATCH', body: { goals: d.goals.map((g) => ({ title: g.title, weight: Number(g.weight) || 0, self: g.self ?? null })),
          self_comment: d.comment, submit } }));
        return (
          <section key={a.id} className="mx-box">
            <h3>{a.cycle_name} <small>{formatDate(a.period_from)} → {formatDate(a.period_to)}</small> <span className="mx-pill">{t(`tl.as.${a.status}`)}</span></h3>
            <div style={{ padding: 6, display: 'flex', flexDirection: 'column', gap: 6 }}>
              {a.due_date && <small>{t('tl.dueBy', { d: formatDate(a.due_date) })}</small>}
              <table className="mx-table">
                <thead><tr><th>{t('tl.goal')}</th><th className="num">{t('tl.weight')}</th><th>{t('tl.myScore')}</th>{a.status === 'closed' && <th className="num">{t('tl.mgrScore')}</th>}</tr></thead>
                <tbody>{d.goals.map((g, i) => <tr key={i}><td>{g.title}</td><td className="num">{g.weight}</td>
                  <td><div role="radiogroup" aria-label={g.title} style={{ display: 'flex', gap: 2 }}>{[1, 2, 3, 4, 5].map((n) => (
                    <button key={n} role="radio" aria-checked={g.self === n} disabled={!open} onClick={() => set({ goals: d.goals.map((x, j) => (j === i ? { ...x, self: n } : x)) })}
                      style={{ background: 'none', border: 0, fontSize: 22, minWidth: 30, cursor: open ? 'pointer' : 'default', color: g.self != null && n <= Number(g.self) ? '#f5a524' : '#cbd5e1' }}>★</button>))}</div></td>
                  {a.status === 'closed' && <td className="num">{g.manager ?? ''}</td>}</tr>)}</tbody>
              </table>
              <label className="mx-fld">{t('tl.selfComment')}<textarea rows={3} disabled={!open} value={d.comment} onChange={(e) => set({ comment: e.currentTarget.value })} /></label>
              {a.status === 'closed' && <div className="mx-box" style={{ padding: 6 }}><b>{t('tl.final')}: {a.final_score ?? '—'}</b>
                {a.strengths && <p style={{ margin: 0 }}>{t('tl.strengths')}: {a.strengths}</p>}{a.improvements && <p style={{ margin: 0 }}>{t('tl.improvements')}: {a.improvements}</p>}
                {a.manager_comment && <p style={{ margin: 0, whiteSpace: 'pre-wrap' }}>{a.manager_comment}</p>}</div>}
              <Flash m={msg} />
              {open && <div className="mx-row" style={{ gap: 6 }}><button className="mx-btn" onClick={() => save(false)}>{t('tl.save')}</button>
                <button className="mx-btn main" onClick={() => save(true)}>{t('tl.submitSelf')}</button></div>}
            </div>
          </section>);
      })}
    </div>
  );
}
