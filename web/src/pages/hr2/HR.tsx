import { useCallback, useEffect, useMemo, useState } from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import { API_BASE, ApiError, api, can, type Me } from '../../api';
import { useCompany } from '../../company';
import DataGrid, { type Col } from '../../components/DataGrid';
import { errorText, formatAmount, formatDate, requireFields, today } from '../../util';
import { openExternal, printPage, saveFile } from '../../native';
import { Help, useBoxes, useCostCenters } from '../acc/common';

type Msg = { ok: boolean; text: string; code?: string } | null;
const MODES = ['people', 'attendance', 'leave', 'payroll', 'rules', 'reports'] as const;
type Mode = typeof MODES[number];

function Flash({ m }: { m: Msg }) {
  const { t } = useTranslation();
  if (!m) return null;
  return (
    <div className={`mx-flash ${m.ok ? 'good' : 'bad'}`}>
      {m.text}
      {m.code === 'no_rate' && <> <a href="#/rates" className="mx-link">{t('hr2.openRates')}</a></>}
    </div>
  );
}
function useList<T>(path: string | null, deps: unknown[] = []): [T[], () => void] {
  const [rows, setRows] = useState<T[]>([]);
  const load = useCallback(() => {
    if (path) api<T[]>(path).then(setRows).catch(() => setRows([]));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [path, ...deps]);
  useEffect(load, [load]);
  return [rows, load];
}
function useRun(t: (k: string) => string) {
  const [msg, setMsg] = useState<Msg>(null);
  const run = useCallback(async <T,>(p: Promise<T>, after?: (v: T) => void): Promise<T | undefined> => {
    try {
      const v = await p;
      setMsg({ ok: true, text: t('hr2.saved') });
      after?.(v);
      return v;
    } catch (e) {
      setMsg({ ok: false, text: errorText(t as never, e), code: e instanceof ApiError ? e.code : undefined });
      return undefined;
    }
  }, [t]);
  return { msg, setMsg, run };
}
const month = () => today().slice(0, 7);
const hm = (iso: string | null | undefined) => (iso ? new Date(iso).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit', timeZone: 'Asia/Beirut' }) : '');
const mins = (m: number | null | undefined) => (m ? `${Math.floor(m / 60)}:${String(m % 60).padStart(2, '0')}` : '');
function leaveName(r: { name_en: string; name_ar?: string; name_fr?: string }, lang: string) {
  return (lang === 'ar' && r.name_ar) || (lang === 'fr' && r.name_fr) || r.name_en;
}

/** HR & payroll (module U): people, attendance, leave, payroll runs, country rules and reports. */
export default function HR({ me, initial }: { me: Me; initial?: string }) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<Mode>((MODES as readonly string[]).includes(initial ?? '') ? initial as Mode : 'people');
  return (
    <div className="mx-col">
      <div className="mx-strip"><b>{t('hr2.title')}</b><Overview /><span className="mx-sp" /><Help topic={`hr2.help.${mode}`} /></div>
      <div className="mx-modes" role="tablist">
        {MODES.map((m) => <button key={m} role="tab" className="mx-mode" aria-selected={mode === m} onClick={() => setMode(m)}>{t(`hr2.m.${m}`)}</button>)}
      </div>
      {mode === 'people' && <People me={me} />}
      {mode === 'attendance' && <Attendance me={me} />}
      {mode === 'leave' && <Leave me={me} />}
      {mode === 'payroll' && <PayrollMode me={me} />}
      {mode === 'rules' && <Rules me={me} />}
      {mode === 'reports' && <Reports />}
    </div>
  );
}

interface OverviewData {
  counts: { active: number; present_today: number; late_today: number; on_leave_today: number; pending_leave: number; missing_out: number };
  contracts_ending: { id: string; name: string; contract_end: string }[];
  documents_expiring: { id: string; name: string; document: string; expires_on: string }[];
}
function Overview() {
  const { t } = useTranslation();
  const { company } = useCompany();
  const [o, setO] = useState<OverviewData | null>(null);
  useEffect(() => { if (company) api<OverviewData>(`/companies/${company.id}/hr/overview`).then(setO).catch(() => setO(null)); }, [company]);
  if (!o) return null;
  const c = o.counts;
  return (
    <span className="mx-row" style={{ gap: 12, flexWrap: 'wrap' }}>
      <span>{t('hr2.ov.active')}: <b>{c.active}</b></span>
      <span>{t('hr2.ov.present')}: <b>{c.present_today}</b></span>
      {c.late_today > 0 && <span className="red">{t('hr2.ov.late')}: <b>{c.late_today}</b></span>}
      <span>{t('hr2.ov.onLeave')}: <b>{c.on_leave_today}</b></span>
      {c.pending_leave > 0 && <span className="red">{t('hr2.ov.pending')}: <b>{c.pending_leave}</b></span>}
      {c.missing_out > 0 && <span className="red">{t('hr2.ov.missingOut')}: <b>{c.missing_out}</b></span>}
      {o.contracts_ending.map((x) => <span key={x.id} className="red">{t('hr2.ov.contract', { name: x.name, date: formatDate(x.contract_end) })}</span>)}
      {o.documents_expiring.map((x) => <span key={x.id} className="red">{t('hr2.ov.document', { name: x.name, doc: x.document, date: formatDate(x.expires_on) })}</span>)}
    </span>
  );
}

// ---------------- people ----------------

interface Emp {
  id: string; code: string; name: string; phone: string; position: string; hire_date: string; status: string; currency: string;
  salary: string; transport: string; nssf_enrolled: boolean; method_id: string | null; user_id: string | null; cost_center_id: string | null;
  advances_open: string; notes: string;
}
interface EmpFile {
  employee: Record<string, string | number | boolean | null>;
  components: { id: string; kind: string; name: string; currency: string; amount: string; taxable: boolean; social: boolean }[];
  documents: { id: string; name: string; kind: string; size: number; expires_on: string | null; created_at: string }[];
  leave_balances: Balance[];
  leave: { id: string; date_from: string; date_to: string; days: string; status: string; name_en: string; name_ar: string; name_fr: string }[];
  attendance: { work_date: string; check_in: string | null; check_out: string | null; worked_minutes: number; late_minutes: number; overtime_minutes: number }[];
}
interface Balance { type_id: string; code: string; name_en: string; name_ar: string; name_fr: string; accrual: string; entitled: string | null; carried: string; taken: string; pending: string }
interface UserRow { id: string; email: string; full_name: string }
interface Schedule { id: string; name: string; start_time: string; end_time: string; break_minutes: number; grace_minutes: number; work_days: number[]; employees: number }

const FILE_TABS = ['details', 'contract', 'pay', 'documents', 'leave', 'attendance', 'eos'] as const;

function People({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const { company } = useCompany();
  const base = company ? `/companies/${company.id}` : null;
  const [emps, reload] = useList<Emp>(base && `${base}/employees`);
  const [sel, setSel] = useState<Emp | null>(null);
  const [file, setFile] = useState<EmpFile | null>(null);
  const [tab, setTab] = useState<typeof FILE_TABS[number]>('details');
  const [f, setF] = useState<Record<string, string | number | boolean | null>>({});
  const [b, setB] = useState<Record<string, string | boolean | null>>({});
  const [comps, setComps] = useState<EmpFile['components']>([]);
  const [nw, setNw] = useState({ name: '', currency: company?.base_currency ?? 'USD', salary: '' });
  const [eos, setEos] = useState<Record<string, string> | null>(null);
  const [eosReason, setEosReason] = useState('termination');
  const { msg, setMsg, run } = useRun(t);
  const canEdit = can(me, 'payroll.edit');
  const boxes = useBoxes();
  const ccs = useCostCenters();
  const [users, reloadUsers] = useList<UserRow>(can(me, 'users.view') ? '/users' : null);
  const [schedules] = useList<Schedule>(base && `${base}/hr/schedules`);

  const loadFile = useCallback((e: Emp) => {
    if (!base) return;
    api<EmpFile>(`${base}/employees/${e.id}/file`).then((x) => {
      setFile(x); setF(x.employee); setComps(x.components);
      setB({ name: e.name, phone: e.phone, position: e.position, hire_date: e.hire_date, status: e.status, currency: e.currency, salary: e.salary,
        transport: e.transport, nssf_enrolled: e.nssf_enrolled, method_id: e.method_id, user_id: e.user_id, cost_center_id: e.cost_center_id, notes: e.notes });
    }).catch((err) => setMsg({ ok: false, text: errorText(t, err) }));
  }, [base, setMsg, t]);

  async function save() {
    if (!base || !sel) return;
    const fileBody: Record<string, unknown> = {};
    for (const k of ['email', 'national_id', 'nssf_number', 'nationality', 'birth_date', 'gender', 'marital_status', 'address', 'department',
      'manager_id', 'contract_type', 'contract_start', 'contract_end', 'end_date', 'bank_name', 'bank_account', 'bank_routing', 'currency2',
      'salary2', 'housing', 'commission_pct', 'schedule_id', 'device_id']) fileBody[k] = f[k] == null ? '' : String(f[k]);
    for (const k of ['spouse_working', 'transport_daily', 'tax_exempt', 'track_attendance']) fileBody[k] = !!f[k];
    fileBody.children = Number(f.children ?? 0);
    fileBody.photo = f.photo ?? '';
    fileBody.components = comps.map((c) => ({ kind: c.kind, name: c.name, currency: c.currency, amount: c.amount, taxable: c.taxable, social: c.social }));
    const basics: Record<string, unknown> = { ...b };
    for (const k of ['method_id', 'user_id', 'cost_center_id']) basics[k] = b[k] ?? '';
    await run(api(`${base}/employees/${sel.id}`, { method: 'PATCH', body: basics }).then(() => api(`${base}/employees/${sel.id}/file`, { method: 'PUT', body: fileBody })), () => {
      reload(); loadFile({ ...sel, ...(basics as Partial<Emp>) } as Emp);
    });
  }
  async function create() {
    if (!base || !requireFields(t as never, { [t('hr2.name')]: nw.name })) return;
    await run(api<{ id: string }>(`${base}/employees`, { method: 'POST', body: { name: nw.name.trim(), currency: nw.currency, salary: nw.salary || '0' } }), () => {
      setNw({ ...nw, name: '', salary: '' }); reload();
    });
  }
  async function upload(ev: React.ChangeEvent<HTMLInputElement>) {
    const file0 = ev.currentTarget.files?.[0];
    if (!file0 || !base || !sel) return;
    const data = await new Promise<string>((res, rej) => { const r = new FileReader(); r.onload = () => res(String(r.result)); r.onerror = rej; r.readAsDataURL(file0); });
    const kind = (document.getElementById('hr-doc-kind') as HTMLSelectElement | null)?.value ?? 'other';
    const exp = (document.getElementById('hr-doc-exp') as HTMLInputElement | null)?.value ?? '';
    await run(api(`${base}/employees/${sel.id}/documents`, { method: 'POST', body: { name: file0.name, mime: file0.type, data, kind, expires_on: exp } }), () => loadFile(sel));
    ev.currentTarget.value = '';
  }
  async function download(d: { id: string; name: string }) {
    if (!base || !sel) return;
    const res = await fetch(`${API_BASE}${base}/employees/${sel.id}/documents/${d.id}`, { credentials: 'include' });
    if (!res.ok) { setMsg({ ok: false, text: t('hr2.downloadFailed') }); return; }
    await saveFile(d.name, await res.blob(), res.headers.get('Content-Type') ?? 'application/octet-stream');
  }
  async function photo(ev: React.ChangeEvent<HTMLInputElement>) {
    const file0 = ev.currentTarget.files?.[0];
    if (!file0) return;
    const img = new Image();
    const url = URL.createObjectURL(file0);
    img.src = url;
    await img.decode();
    const c = document.createElement('canvas');
    const s = Math.min(1, 240 / Math.max(img.width, img.height));
    c.width = Math.round(img.width * s); c.height = Math.round(img.height * s);
    c.getContext('2d')?.drawImage(img, 0, 0, c.width, c.height);
    URL.revokeObjectURL(url);
    setF({ ...f, photo: c.toDataURL('image/jpeg', 0.85) });
  }

  const cols: Col<Emp>[] = [
    { key: 'code', title: t('hr2.code'), get: (r) => r.code, width: 70 },
    { key: 'name', title: t('hr2.name'), get: (r) => r.name, width: 200 },
    { key: 'position', title: t('hr2.position'), get: (r) => r.position, width: 140 },
    { key: 'salary', title: t('hr2.salary'), get: (r) => Number(r.salary), render: (r) => `${formatAmount(r.salary)} ${r.currency}`, num: true, width: 120 },
    { key: 'status', title: t('hr2.status'), get: (r) => t(`hr2.st.${r.status}`), width: 80 },
  ];
  const fld = (k: string, label: string, type = 'text') => (
    <label key={k}>{label}<input type={type} dir={type === 'text' ? undefined : 'ltr'} disabled={!canEdit} value={String(f[k] ?? '')}
      onChange={(e) => setF({ ...f, [k]: e.currentTarget.value })} /></label>
  );
  const chk = (k: string, label: string) => (
    <label key={k} className="mx-row"><input type="checkbox" disabled={!canEdit} checked={!!f[k]} onChange={(e) => setF({ ...f, [k]: e.currentTarget.checked })} /> {label}</label>
  );
  const bf = (k: string, label: string, type = 'text') => (
    <label key={k}>{label}<input type={type} dir={type === 'text' ? undefined : 'ltr'} disabled={!canEdit} value={String(b[k] ?? '')}
      onChange={(e) => setB({ ...b, [k]: e.currentTarget.value })} /></label>
  );
  return (
    <div className="mx-body" style={{ gridTemplateColumns: 'minmax(0, 1fr) minmax(320px, 560px)' }}>
      <section className="mx-box"><h3>{t('hr2.employees')}</h3>
        {canEdit && (
          <div className="mx-row" style={{ padding: 6, gap: 6, flexWrap: 'wrap' }}>
            <input placeholder={t('hr2.name')} value={nw.name} onChange={(e) => setNw({ ...nw, name: e.currentTarget.value })} />
            <input dir="ltr" style={{ width: 60 }} value={nw.currency} onChange={(e) => setNw({ ...nw, currency: e.currentTarget.value.toUpperCase() })} aria-label={t('hr2.currency')} />
            <input dir="ltr" style={{ width: 100 }} placeholder={t('hr2.salary')} value={nw.salary} onChange={(e) => setNw({ ...nw, salary: e.currentTarget.value })} />
            <button className="mx-btn" onClick={() => void create()}>{t('hr2.add')}</button>
          </div>
        )}
        <DataGrid rows={emps} cols={cols} rowKey={(r) => r.id} selected={sel?.id ?? null} title={t('hr2.employees')}
          onSelect={(r) => { setSel(r); setMsg(null); setEos(null); loadFile(r); }} onActivate={(r) => { setSel(r); loadFile(r); }} />
      </section>
      <section className="mx-box"><h3>{sel ? `${sel.code} · ${sel.name}` : t('hr2.pickEmployee')}</h3>
        {sel && file && base && (
          <div className="mx-pay" style={{ overflow: 'auto' }}>
            <div className="mx-row" style={{ gap: 10 }}>
              {f.photo ? <img src={String(f.photo)} alt="" style={{ width: 72, height: 72, objectFit: 'cover', borderRadius: 6 }} /> : null}
              {canEdit && <label className="mx-btn sm">{t('hr2.photo')}<input type="file" accept="image/*" hidden onChange={(e) => void photo(e)} /></label>}
            </div>
            <div className="mx-modes" role="tablist">
              {FILE_TABS.map((x) => <button key={x} role="tab" className="mx-mode" aria-selected={tab === x} onClick={() => setTab(x)}>{t(`hr2.tab.${x}`)}</button>)}
            </div>
            {tab === 'details' && (<>
              {bf('name', t('hr2.name'))}{bf('phone', t('hr2.phone'))}{fld('email', t('hr2.email'))}{bf('position', t('hr2.position'))}
              {fld('department', t('hr2.department'))}
              <label>{t('hr2.manager')}<select disabled={!canEdit} value={String(f.manager_id ?? '')} onChange={(e) => setF({ ...f, manager_id: e.currentTarget.value })}>
                <option value="">—</option>{emps.filter((x) => x.id !== sel.id).map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}</select></label>
              {fld('national_id', t('hr2.nationalId'))}{fld('nationality', t('hr2.nationality'))}{fld('birth_date', t('hr2.birthDate'), 'date')}
              <label>{t('hr2.gender')}<select disabled={!canEdit} value={String(f.gender ?? '')} onChange={(e) => setF({ ...f, gender: e.currentTarget.value })}>
                <option value="">—</option><option value="m">{t('hr2.male')}</option><option value="f">{t('hr2.female')}</option></select></label>
              <label>{t('hr2.marital')}<select disabled={!canEdit} value={String(f.marital_status ?? '')} onChange={(e) => setF({ ...f, marital_status: e.currentTarget.value })}>
                <option value="">—</option>{['single', 'married', 'divorced', 'widowed'].map((x) => <option key={x} value={x}>{t(`hr2.ms.${x}`)}</option>)}</select></label>
              {chk('spouse_working', t('hr2.spouseWorking'))}{fld('children', t('hr2.children'), 'number')}{fld('address', t('hr2.address'))}
              <label>{t('hr2.userLink')}<select disabled={!canEdit} value={String(b.user_id ?? '')} onChange={(e) => setB({ ...b, user_id: e.currentTarget.value })}>
                <option value="">—</option>{users.map((u) => <option key={u.id} value={u.id}>{u.full_name} · {u.email}</option>)}</select></label>
              <small>{t('hr2.userLinkHint')}</small>
              {canEdit && can(me, 'users.create') && !b.user_id && (
                <button className="mx-btn sm" disabled={!String(f.email ?? '').includes('@')} title={t('hr2.makeLoginHint')}
                  onClick={() => void run(api<{ user_id: string; email: string; password: string }>(`${base}/employees/${sel.id}/login`, { method: 'POST', body: { email: String(f.email ?? ''), locale: i18n.language } }))
                    .then((x) => { if (x) { setMsg({ ok: true, text: t('hr2.loginMade', { email: x.email, password: x.password }) }); setB({ ...b, user_id: x.user_id }); setSel({ ...sel, user_id: x.user_id }); reload(); reloadUsers(); } })}>
                  {t('hr2.makeLogin')}
                </button>
              )}
            </>)}
            {tab === 'contract' && (<>
              {bf('hire_date', t('hr2.hireDate'), 'date')}
              <label>{t('hr2.contractType')}<select disabled={!canEdit} value={String(f.contract_type ?? 'permanent')} onChange={(e) => setF({ ...f, contract_type: e.currentTarget.value })}>
                {['permanent', 'fixed', 'part_time', 'daily', 'contractor'].map((x) => <option key={x} value={x}>{t(`hr2.ct.${x}`)}</option>)}</select></label>
              {fld('contract_start', t('hr2.contractStart'), 'date')}{fld('contract_end', t('hr2.contractEnd'), 'date')}{fld('end_date', t('hr2.endDate'), 'date')}
              <label>{t('hr2.status')}<select disabled={!canEdit} value={String(b.status ?? 'active')} onChange={(e) => setB({ ...b, status: e.currentTarget.value })}>
                <option value="active">{t('hr2.st.active')}</option><option value="inactive">{t('hr2.st.inactive')}</option></select></label>
              <label>{t('hr2.schedule')}<select disabled={!canEdit} value={String(f.schedule_id ?? '')} onChange={(e) => setF({ ...f, schedule_id: e.currentTarget.value })}>
                <option value="">{t('hr2.defaultSchedule')}</option>{schedules.map((s) => <option key={s.id} value={s.id}>{s.name} ({s.start_time}–{s.end_time})</option>)}</select></label>
              {chk('track_attendance', t('hr2.trackAttendance'))}{fld('device_id', t('hr2.deviceId'))}
              <label>{t('hr2.costCenter')}<select disabled={!canEdit} value={String(b.cost_center_id ?? '')} onChange={(e) => setB({ ...b, cost_center_id: e.currentTarget.value })}>
                <option value="">—</option>{ccs.map((c) => <option key={c.id} value={c.id}>{c.code} · {c.name}</option>)}</select></label>
            </>)}
            {tab === 'pay' && (<>
              <div className="mx-row" style={{ gap: 6 }}>{bf('salary', t('hr2.salary'))}{bf('currency', t('hr2.currency'))}</div>
              <div className="mx-row" style={{ gap: 6 }}>{fld('salary2', t('hr2.salary2'))}{fld('currency2', t('hr2.currency2'))}</div>
              <small>{t('hr2.splitHint')}</small>
              {bf('transport', t('hr2.transportMonthly'))}{chk('transport_daily', t('hr2.transportDaily'))}
              {fld('housing', t('hr2.housing'))}{fld('commission_pct', t('hr2.commission'))}
              <label className="mx-row"><input type="checkbox" disabled={!canEdit} checked={!!b.nssf_enrolled} onChange={(e) => setB({ ...b, nssf_enrolled: e.currentTarget.checked })} /> {t('hr2.socialEnrolled')}</label>
              {fld('nssf_number', t('hr2.socialNumber'))}{chk('tax_exempt', t('hr2.taxExempt'))}
              <label>{t('hr2.payFrom')}<select disabled={!canEdit} value={String(b.method_id ?? '')} onChange={(e) => setB({ ...b, method_id: e.currentTarget.value })}>
                <option value="">—</option>{boxes.map((m) => <option key={m.id} value={m.id}>{m.name} ({m.currency})</option>)}</select></label>
              {fld('bank_name', t('hr2.bankName'))}{fld('bank_account', t('hr2.iban'))}{fld('bank_routing', t('hr2.routing'))}
              <b>{t('hr2.components')}</b>
              {comps.map((c, i) => (
                <div className="mx-row" key={i} style={{ gap: 4, flexWrap: 'wrap' }}>
                  <select disabled={!canEdit} value={c.kind} onChange={(e) => setComps(comps.map((y, j) => (j === i ? { ...y, kind: e.currentTarget.value } : y)))}>
                    <option value="allowance">{t('hr2.allowance')}</option><option value="deduction">{t('hr2.deduction')}</option></select>
                  <input disabled={!canEdit} style={{ width: 110 }} value={c.name} onChange={(e) => setComps(comps.map((y, j) => (j === i ? { ...y, name: e.currentTarget.value } : y)))} />
                  <input disabled={!canEdit} dir="ltr" style={{ width: 80 }} value={c.amount} onChange={(e) => setComps(comps.map((y, j) => (j === i ? { ...y, amount: e.currentTarget.value } : y)))} />
                  <input disabled={!canEdit} dir="ltr" style={{ width: 50 }} value={c.currency} onChange={(e) => setComps(comps.map((y, j) => (j === i ? { ...y, currency: e.currentTarget.value.toUpperCase() } : y)))} />
                  <label><input type="checkbox" disabled={!canEdit} checked={c.taxable} onChange={(e) => setComps(comps.map((y, j) => (j === i ? { ...y, taxable: e.currentTarget.checked } : y)))} />{t('hr2.taxable')}</label>
                  <label><input type="checkbox" disabled={!canEdit} checked={c.social} onChange={(e) => setComps(comps.map((y, j) => (j === i ? { ...y, social: e.currentTarget.checked } : y)))} />{t('hr2.social')}</label>
                  {canEdit && <button className="mx-btn sm" onClick={() => setComps(comps.filter((_, j) => j !== i))}>✕</button>}
                </div>
              ))}
              {canEdit && <button className="mx-btn sm" onClick={() => setComps([...comps, { id: '', kind: 'allowance', name: '', currency: String(b.currency ?? 'USD'), amount: '', taxable: true, social: false }])}>{t('hr2.addComponent')}</button>}
            </>)}
            {tab === 'documents' && (<>
              {file.documents.map((d) => (
                <div className="mx-row" key={d.id} style={{ gap: 6 }}>
                  <button className="mx-btn sm" onClick={() => void download(d)}>{d.name}</button>
                  <small>{t(`hr2.dk.${d.kind}`, { defaultValue: d.kind })} · {Math.ceil(d.size / 1024)} KB{d.expires_on ? ` · ${t('hr2.expires')} ${formatDate(d.expires_on)}` : ''}</small>
                  {canEdit && <button className="mx-btn sm" onClick={() => void run(api(`${base}/employees/${sel.id}/documents/${d.id}`, { method: 'DELETE' }), () => loadFile(sel))}>✕</button>}
                </div>
              ))}
              {canEdit && (<div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
                <select id="hr-doc-kind" defaultValue="contract">{['contract', 'id', 'passport', 'permit', 'certificate', 'medical', 'other'].map((k) => <option key={k} value={k}>{t(`hr2.dk.${k}`)}</option>)}</select>
                <label>{t('hr2.expires')} <input id="hr-doc-exp" type="date" dir="ltr" /></label>
                <label className="mx-btn">{t('hr2.upload')}<input type="file" hidden onChange={(e) => void upload(e)} /></label>
              </div>)}
            </>)}
            {tab === 'leave' && (<>
              <Balances rows={file.leave_balances} />
              {file.leave.map((l) => <div key={l.id} className="mx-row" style={{ gap: 6 }}>{formatDate(l.date_from)} → {formatDate(l.date_to)} · {leaveName(l, i18n.language)} · {l.days} · {t(`hr2.ls.${l.status}`)}</div>)}
            </>)}
            {tab === 'attendance' && (
              <table className="mx-table"><thead><tr><th>{t('hr2.date')}</th><th>{t('hr2.in')}</th><th>{t('hr2.out')}</th><th>{t('hr2.worked')}</th><th>{t('hr2.late')}</th><th>{t('hr2.overtime')}</th></tr></thead>
                <tbody>{file.attendance.map((a) => <tr key={a.work_date}><td>{formatDate(a.work_date)}</td><td>{hm(a.check_in)}</td><td>{hm(a.check_out)}</td><td>{mins(a.worked_minutes)}</td><td className={a.late_minutes ? 'red' : ''}>{mins(a.late_minutes)}</td><td>{mins(a.overtime_minutes)}</td></tr>)}</tbody></table>
            )}
            {tab === 'eos' && (<>
              <small>{t('hr2.eosHint')}</small>
              <div className="mx-row" style={{ gap: 6 }}>
                <select value={eosReason} onChange={(e) => setEosReason(e.currentTarget.value)}><option value="termination">{t('hr2.termination')}</option><option value="resignation">{t('hr2.resignation')}</option></select>
                <button className="mx-btn" onClick={() => void run(api<Record<string, string>>(`${base}/employees/${sel.id}/end-of-service?reason=${eosReason}&date=${String(f.end_date || today())}`), setEos)}>{t('hr2.calculate')}</button>
              </div>
              {eos && <div className="mx-receipt">{t(`hr2.eosMode.${eos.mode}`, { defaultValue: eos.mode })}<br />{t('hr2.eosAmount')}: <b dir="ltr">{formatAmount(eos.amount)} {eos.currency}</b> ({eos.basis})
                {eos.contributions !== undefined && <><br />{t('hr2.eosContrib')}: <b dir="ltr">{formatAmount(eos.contributions)}</b></>}</div>}
            </>)}
            {canEdit && tab !== 'leave' && tab !== 'attendance' && tab !== 'eos' && tab !== 'documents' && <button className="mx-btn main" onClick={() => void save()}>{t('hr2.save')}</button>}
            <Flash m={msg} />
          </div>
        )}
      </section>
    </div>
  );
}

function Balances({ rows }: { rows: Balance[] }) {
  const { t, i18n } = useTranslation();
  return (
    <table className="mx-table"><thead><tr><th>{t('hr2.leaveType')}</th><th>{t('hr2.entitled')}</th><th>{t('hr2.carried')}</th><th>{t('hr2.taken')}</th><th>{t('hr2.pending')}</th><th>{t('hr2.left')}</th></tr></thead>
      <tbody>{rows.map((r) => {
        const left = r.entitled == null ? '∞' : (Number(r.entitled) + Number(r.carried) - Number(r.taken) - Number(r.pending)).toFixed(1);
        return <tr key={r.type_id}><td>{leaveName(r, i18n.language)}</td><td dir="ltr">{r.entitled ?? '—'}</td><td dir="ltr">{r.carried}</td><td dir="ltr">{r.taken}</td><td dir="ltr">{r.pending}</td><td dir="ltr"><b>{left}</b></td></tr>;
      })}</tbody></table>
  );
}

// ---------------- attendance ----------------

interface DayRow {
  employee_id: string; code: string; name: string; department: string; check_in: string | null; check_out: string | null;
  worked_minutes: number; late_minutes: number; overtime_minutes: number; notes: string; on_leave: string | null; holiday: boolean;
}
function Attendance({ me }: { me: Me }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const base = company ? `/companies/${company.id}` : null;
  const [date, setDate] = useState(today());
  const [rows, reload] = useList<DayRow>(base && `${base}/hr/attendance?date=${date}`, [date]);
  const [edit, setEdit] = useState<Record<string, { in: string; out: string }>>({});
  const [csv, setCsv] = useState('');
  const [preview, setPreview] = useState<{ punches: number; days: number; unknown_users: string[] } | null>(null);
  const [schedules, reloadS] = useList<Schedule>(base && `${base}/hr/schedules`);
  const [holidays, reloadH] = useList<{ id: string; day: string; name: string }>(base && `${base}/hr/holidays`);
  const [ns, setNs] = useState({ name: '', start_time: '08:00', end_time: '16:00', break_minutes: '30', grace_minutes: '10', days: '1,2,3,4,5,6' });
  const [nh, setNh] = useState({ day: '', name: '' });
  const { msg, run } = useRun(t);
  const canEdit = can(me, 'payroll.edit');
  const val = (r: DayRow) => edit[r.employee_id] ?? { in: hm(r.check_in), out: hm(r.check_out) };
  const cols: Col<DayRow>[] = [
    { key: 'name', title: t('hr2.name'), get: (r) => r.name, width: 180 },
    { key: 'status', title: t('hr2.status'), get: (r) => (r.holiday ? t('hr2.holiday') : r.on_leave ? t('hr2.onLeave') : r.check_in ? t('hr2.present') : t('hr2.absent')),
      cls: (r) => (!r.check_in && !r.on_leave && !r.holiday ? 'red' : ''), width: 90 },
    { key: 'in', title: t('hr2.in'), get: (r) => val(r).in, width: 90, filter: false,
      render: (r) => <input dir="ltr" type="time" disabled={!canEdit} value={val(r).in} onChange={(e) => setEdit({ ...edit, [r.employee_id]: { ...val(r), in: e.currentTarget.value } })} /> },
    { key: 'out', title: t('hr2.out'), get: (r) => val(r).out, width: 90, filter: false,
      render: (r) => <input dir="ltr" type="time" disabled={!canEdit} value={val(r).out} onChange={(e) => setEdit({ ...edit, [r.employee_id]: { ...val(r), out: e.currentTarget.value } })} /> },
    { key: 'worked', title: t('hr2.worked'), get: (r) => mins(r.worked_minutes), width: 70 },
    { key: 'late', title: t('hr2.late'), get: (r) => mins(r.late_minutes), cls: (r) => (r.late_minutes ? 'red' : ''), width: 70 },
    { key: 'ot', title: t('hr2.overtime'), get: (r) => mins(r.overtime_minutes), width: 70 },
    { key: 'save', title: '', get: () => '', width: 70, filter: false,
      render: (r) => (canEdit && edit[r.employee_id] ? <button className="mx-btn sm" onClick={() => void run(api(`${base}/hr/attendance`, { method: 'PUT', body: {
        employee_id: r.employee_id, work_date: date, check_in: edit[r.employee_id].in, check_out: edit[r.employee_id].out } }), () => { const e2 = { ...edit }; delete e2[r.employee_id]; setEdit(e2); reload(); })}>{t('hr2.save')}</button> : null) },
  ];
  async function readFile(ev: React.ChangeEvent<HTMLInputElement>) {
    const f0 = ev.currentTarget.files?.[0];
    if (!f0) return;
    const text = await f0.text();
    setCsv(text);
    await run(api<{ punches: number; days: number; unknown_users: string[] }>(`${base}/hr/attendance/import`, { method: 'POST', body: { csv: text, dry_run: true } }), setPreview);
  }
  return (
    <div className="mx-body" style={{ gridTemplateColumns: 'minmax(0, 1fr) minmax(280px, 380px)' }}>
      <section className="mx-box"><h3>{t('hr2.daySheet')} <input type="date" dir="ltr" value={date} onChange={(e) => setDate(e.currentTarget.value)} /></h3>
        <DataGrid rows={rows} cols={cols} rowKey={(r) => r.employee_id} title={`${t('hr2.daySheet')} ${date}`} />
        <Flash m={msg} />
      </section>
      <section className="mx-box"><h3>{t('hr2.import')}</h3>
        <div className="mx-pay">
          <small>{t('hr2.importHint')}</small>
          {canEdit && <label className="mx-btn">{t('hr2.chooseCsv')}<input type="file" accept=".csv,.txt,text/csv" hidden onChange={(e) => void readFile(e)} /></label>}
          {preview && <div>{t('hr2.importPreview', { punches: preview.punches, days: preview.days })}{preview.unknown_users.length > 0 && <div className="red">{t('hr2.unknownUsers')}: {preview.unknown_users.join(', ')}</div>}
            <button className="mx-btn main" onClick={() => void run(api(`${base}/hr/attendance/import`, { method: 'POST', body: { csv } }), () => { setPreview(null); reload(); })}>{t('hr2.importNow')}</button></div>}
          <b>{t('hr2.schedules')}</b>
          {schedules.map((s) => <div key={s.id}>{s.name}: <span dir="ltr">{s.start_time}–{s.end_time}</span> · {t('hr2.break')} {s.break_minutes}′ · {s.work_days.map((d) => t(`hr2.dow.${d}`)).join(' ')} ({s.employees})</div>)}
          {canEdit && (<div className="mx-row" style={{ gap: 4, flexWrap: 'wrap' }}>
            <input placeholder={t('hr2.name')} value={ns.name} onChange={(e) => setNs({ ...ns, name: e.currentTarget.value })} />
            <input type="time" dir="ltr" value={ns.start_time} onChange={(e) => setNs({ ...ns, start_time: e.currentTarget.value })} />
            <input type="time" dir="ltr" value={ns.end_time} onChange={(e) => setNs({ ...ns, end_time: e.currentTarget.value })} />
            <input dir="ltr" style={{ width: 50 }} title={t('hr2.break')} value={ns.break_minutes} onChange={(e) => setNs({ ...ns, break_minutes: e.currentTarget.value })} />
            <input dir="ltr" style={{ width: 90 }} title={t('hr2.workDays')} value={ns.days} onChange={(e) => setNs({ ...ns, days: e.currentTarget.value })} />
            <button className="mx-btn sm" onClick={() => requireFields(t as never, { [t('hr2.name')]: ns.name, [t('hr2.in')]: ns.start_time, [t('hr2.out')]: ns.end_time, [t('hr2.workDays')]: ns.days }) && void run(api(`${base}/hr/schedules`, { method: 'POST', body: { name: ns.name, start_time: ns.start_time, end_time: ns.end_time,
              break_minutes: Number(ns.break_minutes), grace_minutes: Number(ns.grace_minutes), work_days: ns.days.split(',').map(Number).filter(Boolean) } }), reloadS)}>{t('hr2.add')}</button>
          </div>)}
          <small>{t('hr2.workDaysHint')}</small>
          <Flash m={msg} />
          <b>{t('hr2.holidays')}</b>
          {holidays.map((h) => <div key={h.id} className="mx-row" style={{ gap: 6 }}>{formatDate(h.day)} · {h.name}
            {canEdit && <button className="mx-btn sm" onClick={() => void run(api(`${base}/hr/holidays/${h.id}`, { method: 'DELETE' }), reloadH)}>✕</button>}</div>)}
          {canEdit && (<div className="mx-row" style={{ gap: 4 }}>
            <input type="date" dir="ltr" value={nh.day} onChange={(e) => setNh({ ...nh, day: e.currentTarget.value })} />
            <input placeholder={t('hr2.name')} value={nh.name} onChange={(e) => setNh({ ...nh, name: e.currentTarget.value })} />
            <button className="mx-btn sm" onClick={() => requireFields(t as never, { [t('hr2.name')]: nh.name, [t('hr2.date')]: nh.day }) && void run(api(`${base}/hr/holidays`, { method: 'POST', body: nh }), reloadH)}>{t('hr2.add')}</button>
          </div>)}
        </div>
      </section>
    </div>
  );
}

// ---------------- leave ----------------

interface LeaveRow { id: string; employee_id: string; name: string; type_code: string; name_en: string; name_ar: string; name_fr: string; date_from: string; date_to: string; days: string; status: string; reason: string; decided_by: string | null }
interface LeaveType { id: string; code: string; name_en: string; name_ar: string; name_fr: string; paid: boolean; days_per_year: string; accrual: string; max_carry: string; is_active: boolean }
function Leave({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const { company } = useCompany();
  const base = company ? `/companies/${company.id}` : null;
  const [year, setYear] = useState(new Date().getFullYear());
  const [rows, reload] = useList<LeaveRow>(base && `${base}/hr/leave?year=${year}`, [year]);
  const [types, reloadT] = useList<LeaveType>(base && `${base}/hr/leave-types`);
  const [emps] = useList<Emp>(base && `${base}/employees?status=active`);
  const [bal, reloadB] = useList<{ employee_id: string; name: string; balances: Balance[] }>(base && `${base}/hr/leave/balances?year=${year}`, [year]);
  const [nl, setNl] = useState({ employee_id: '', type_id: '', date_from: today(), date_to: today(), reason: '', approve: true });
  const [nt, setNt] = useState({ code: '', name_en: '', name_ar: '', paid: true, days_per_year: '0', accrual: 'yearly' });
  const { msg, run } = useRun(t);
  const canEdit = can(me, 'payroll.create');
  const canApprove = can(me, 'payroll.approve');
  const decide = (id: string, decision: string) => void run(api(`${base}/hr/leave/${id}/decide`, { method: 'POST', body: { decision } }), () => { reload(); reloadB(); });
  const cols: Col<LeaveRow>[] = [
    { key: 'name', title: t('hr2.name'), get: (r) => r.name, width: 160 },
    { key: 'type', title: t('hr2.leaveType'), get: (r) => leaveName(r, i18n.language), width: 120 },
    { key: 'from', title: t('hr2.from'), get: (r) => r.date_from, render: (r) => formatDate(r.date_from), width: 90 },
    { key: 'to', title: t('hr2.to'), get: (r) => r.date_to, render: (r) => formatDate(r.date_to), width: 90 },
    { key: 'days', title: t('hr2.days'), get: (r) => Number(r.days), num: true, width: 50 },
    { key: 'status', title: t('hr2.status'), get: (r) => t(`hr2.ls.${r.status}`), cls: (r) => (r.status === 'pending' ? 'red' : ''), width: 90 },
    { key: 'reason', title: t('hr2.reason'), get: (r) => r.reason, width: 140 },
    { key: 'act', title: '', get: () => '', filter: false, width: 150, render: (r) => (<span className="mx-row" style={{ gap: 4 }}>
      {r.status === 'pending' && canApprove && <><button className="mx-btn sm" onClick={() => decide(r.id, 'approve')}>{t('hr2.approve')}</button><button className="mx-btn sm" onClick={() => decide(r.id, 'reject')}>{t('hr2.reject')}</button></>}
      {(r.status === 'pending' || r.status === 'approved') && can(me, 'payroll.edit') && <button className="mx-btn sm" onClick={() => void run(api(`${base}/hr/leave/${r.id}/cancel`, { method: 'POST' }), () => { reload(); reloadB(); })}>{t('hr2.cancel')}</button>}
    </span>) },
  ];
  return (
    <div className="mx-body" style={{ gridTemplateColumns: 'minmax(0, 1fr) minmax(280px, 400px)' }}>
      <section className="mx-box"><h3>{t('hr2.requests')} <input type="number" dir="ltr" style={{ width: 80 }} value={year} onChange={(e) => setYear(Number(e.currentTarget.value))} /></h3>
        <DataGrid rows={rows} cols={cols} rowKey={(r) => r.id} title={t('hr2.requests')} />
        <h3>{t('hr2.balances')}</h3>
        <div className="mx-scroll">
          <table className="mx-table"><thead><tr><th>{t('hr2.name')}</th>{types.filter((x) => x.accrual !== 'none').map((x) => <th key={x.id}>{leaveName(x, i18n.language)}</th>)}</tr></thead>
            <tbody>{bal.map((b) => <tr key={b.employee_id}><td>{b.name}</td>{types.filter((x) => x.accrual !== 'none').map((x) => {
              const v = b.balances.find((y) => y.type_id === x.id);
              return <td key={x.id} dir="ltr">{v ? (Number(v.entitled ?? 0) + Number(v.carried) - Number(v.taken) - Number(v.pending)).toFixed(1) : ''}</td>;
            })}</tr>)}</tbody></table>
        </div>
      </section>
      <section className="mx-box"><h3>{t('hr2.newLeave')}</h3>
        <div className="mx-pay">
          {canEdit && (<>
            <label>{t('hr2.employee')}<select value={nl.employee_id} onChange={(e) => setNl({ ...nl, employee_id: e.currentTarget.value })}><option value="">—</option>{emps.map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}</select></label>
            <label>{t('hr2.leaveType')}<select value={nl.type_id} onChange={(e) => setNl({ ...nl, type_id: e.currentTarget.value })}><option value="">—</option>{types.filter((x) => x.is_active).map((x) => <option key={x.id} value={x.id}>{leaveName(x, i18n.language)}</option>)}</select></label>
            <label>{t('hr2.from')}<input type="date" dir="ltr" value={nl.date_from} onChange={(e) => setNl({ ...nl, date_from: e.currentTarget.value })} /></label>
            <label>{t('hr2.to')}<input type="date" dir="ltr" value={nl.date_to} onChange={(e) => setNl({ ...nl, date_to: e.currentTarget.value })} /></label>
            <label>{t('hr2.reason')}<input value={nl.reason} onChange={(e) => setNl({ ...nl, reason: e.currentTarget.value })} /></label>
            {canApprove && <label className="mx-row"><input type="checkbox" checked={nl.approve} onChange={(e) => setNl({ ...nl, approve: e.currentTarget.checked })} /> {t('hr2.recordApproved')}</label>}
            <button className="mx-btn main" disabled={!nl.employee_id || !nl.type_id} onClick={() => void run(api(`${base}/hr/leave`, { method: 'POST', body: nl }), () => { reload(); reloadB(); })}>{t('hr2.save')}</button>
          </>)}
          <Flash m={msg} />
          <b>{t('hr2.leaveTypes')}</b>
          {types.map((x) => <div key={x.id}>{leaveName(x, i18n.language)} · {x.accrual === 'none' ? t('hr2.unlimited') : `${x.days_per_year} ${t('hr2.daysPerYear')} (${t(`hr2.acc.${x.accrual}`)})`} · {x.paid ? t('hr2.paid') : t('hr2.unpaid')}</div>)}
          {can(me, 'payroll.edit') && (<div className="mx-row" style={{ gap: 4, flexWrap: 'wrap' }}>
            <input dir="ltr" style={{ width: 80 }} placeholder="code" value={nt.code} onChange={(e) => setNt({ ...nt, code: e.currentTarget.value.toLowerCase() })} />
            <input style={{ width: 110 }} placeholder="English" value={nt.name_en} onChange={(e) => setNt({ ...nt, name_en: e.currentTarget.value })} />
            <input style={{ width: 110 }} placeholder="عربي" value={nt.name_ar} onChange={(e) => setNt({ ...nt, name_ar: e.currentTarget.value })} />
            <input dir="ltr" style={{ width: 50 }} value={nt.days_per_year} onChange={(e) => setNt({ ...nt, days_per_year: e.currentTarget.value })} />
            <select value={nt.accrual} onChange={(e) => setNt({ ...nt, accrual: e.currentTarget.value })}>{['yearly', 'monthly', 'none'].map((a) => <option key={a} value={a}>{t(`hr2.acc.${a}`)}</option>)}</select>
            <label><input type="checkbox" checked={nt.paid} onChange={(e) => setNt({ ...nt, paid: e.currentTarget.checked })} />{t('hr2.paid')}</label>
            <button className="mx-btn sm" onClick={() => requireFields(t as never, { code: nt.code, English: nt.name_en }) && void run(api(`${base}/hr/leave-types`, { method: 'POST', body: nt }), reloadT)}>{t('hr2.add')}</button>
          </div>)}
        </div>
      </section>
    </div>
  );
}

// ---------------- payroll ----------------

interface Total { currency: string; gross: string; net: string; paid: string; nssf: string; tax?: string }
interface Run { id: string; period: string; status: string; posting_date: string | null; employees: number; totals: Total[]; approved_by_name: string | null; journal_entry_id: string | null }
interface Line { code: string; label: string; kind: string; role: string; currency: string; amount: string; basis: string }
interface Slip {
  id: string; employee_id: string; code: string; name: string; position: string; currency: string; net: string; paid: string; currency2: string | null;
  net2: string; paid2: string; income_tax: string; worked_days: string; absent_days: string; unpaid_days: string; overtime_hours: string; detailed: boolean;
  manual_allow: string; manual_ded: string; manual_recovery: string | null; advance_recovery: string; lines: Line[]; nssf_employee: string; base: string;
}
interface RunDetail { run: Run; payslips: Slip[]; payments: { id: string; payment_date: string; method: string; currency: string; amount: string }[]; company: { name_en: string; name_ar: string } }

/** Phase 6b: on which date a payroll run is posted — today when the month is running (default) or always the month end. */
function PostDateOption({ base, canEdit }: { base: string | null; canEdit: boolean }) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  useEffect(() => { if (base) api<{ post_date: string }>(`${base}/payroll-options`).then((x) => setMode(x.post_date)).catch(() => setMode(null)); }, [base]);
  if (!base || mode === null) return null;
  return (
    <label className="mx-row" style={{ padding: 6, gap: 6, flexWrap: 'wrap', borderTop: '1px solid var(--mx-line, #e3e8ef)' }}>
      <span>{t('hr2.postDate')}</span>
      <select value={mode} disabled={!canEdit} onChange={(e) => {
        const v = e.currentTarget.value;
        api<{ post_date: string }>(`${base}/payroll-options`, { method: 'PUT', body: { post_date: v } }).then((x) => { setMode(x.post_date); setSaved(true); setTimeout(() => setSaved(false), 2000); }).catch(() => {});
      }}>
        <option value="auto">{t('hr2.postDateAuto')}</option>
        <option value="period_end">{t('hr2.postDateEnd')}</option>
      </select>
      {saved && <small>✓ {t('common.saved')}</small>}
    </label>
  );
}

function PayrollMode({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const base = company ? `/companies/${company.id}` : null;
  const [runs, reloadRuns] = useList<Run>(base && `${base}/payroll`);
  const [period, setPeriod] = useState(month());
  const [cur, setCur] = useState<RunDetail | null>(null);
  const [slip, setSlip] = useState<Slip | null>(null);
  const [man, setMan] = useState({ allow: '', ded: '', rec: '' });
  const [box, setBox] = useState('');
  const [printing, setPrinting] = useState<Slip[] | null>(null);
  const boxes = useBoxes();
  const { msg, run } = useRun(t);
  const canEdit = can(me, 'payroll.edit');
  const open = useCallback((id: string) => {
    if (!base) return;
    api<RunDetail>(`${base}/payroll/${id}`).then((d) => { setCur(d); setSlip((s) => (s ? d.payslips.find((x) => x.id === s.id) ?? null : null)); }).catch(() => setCur(null));
  }, [base]);
  const act = (path: string, body: unknown = {}, method = 'POST') => cur && run(api<RunDetail>(`${base}/payroll/${cur.run.id}${path}`, { method, body }), (d) => {
    reloadRuns(); if (d && (d as RunDetail).run) { setCur(d as RunDetail); } else open(cur.run.id);
  });
  const amt = (v: string, c: string | null) => (c ? `${formatAmount(v, decimalsOf(c))} ${c}` : '');
  const runCols: Col<Run>[] = [
    { key: 'period', title: t('hr2.period'), get: (r) => r.period, width: 80 },
    { key: 'n', title: t('hr2.employees'), get: (r) => r.employees, num: true, decimals: 0, width: 60 },
    { key: 'net', title: t('hr2.net'), get: (r) => r.totals.map((x) => amt(x.net, x.currency)).join(' · '), width: 220 },
    { key: 'status', title: t('hr2.status'), get: (r) => t(`hr2.rs.${r.status}`), cls: (r) => (r.status === 'draft' ? 'red' : ''), width: 90 },
  ];
  const slipCols: Col<Slip>[] = [
    { key: 'name', title: t('hr2.name'), get: (r) => r.name, width: 160 },
    { key: 'net', title: t('hr2.net'), get: (r) => Number(r.net), render: (r) => amt(r.net, r.currency), num: true, width: 120 },
    { key: 'net2', title: t('hr2.net2'), get: (r) => Number(r.net2), render: (r) => (r.currency2 ? amt(r.net2, r.currency2) : ''), num: true, width: 140 },
    { key: 'tax', title: t('hr2.tax'), get: (r) => Number(r.income_tax), num: true, width: 90 },
    { key: 'worked', title: t('hr2.workedDays'), get: (r) => Number(r.worked_days), num: true, width: 60 },
    { key: 'absent', title: t('hr2.absentDays'), get: (r) => Number(r.absent_days) + Number(r.unpaid_days), num: true, width: 60, cls: (r) => (Number(r.absent_days) + Number(r.unpaid_days) > 0 ? 'red' : '') },
    { key: 'ot', title: t('hr2.overtimeH'), get: (r) => Number(r.overtime_hours), num: true, width: 60 },
  ];
  async function wps() {
    if (!cur || !base) return;
    const res = await fetch(`${API_BASE}${base}/payroll/${cur.run.id}/wps`, { credentials: 'include' });
    if (!res.ok) { const j = await res.json().catch(() => ({})); run(Promise.reject(new ApiError(res.status, j.error ?? 'server_error', j.detail))); return; }
    const name = /filename="([^"]+)"/.exec(res.headers.get('Content-Disposition') ?? '')?.[1] ?? 'wps.sif';
    await saveFile(name, await res.blob(), 'text/csv');
  }
  function whatsapp(s: Slip) {
    const lines = s.lines.filter((l) => l.kind !== 'employer').map((l) => `${l.kind === 'deduction' ? '−' : '+'} ${l.label}: ${formatAmount(l.amount, decimalsOf(l.currency))} ${l.currency}`);
    const text = `${company?.display ?? company?.name_en ?? ''} · ${t('hr2.payslip')} ${cur?.run.period}\n${s.name}\n${lines.join('\n')}\n= ${amt(s.net, s.currency)}${s.currency2 ? ` + ${amt(s.net2, s.currency2)}` : ''}`;
    const phone = '';
    openExternal(`https://wa.me/${phone}?text=${encodeURIComponent(text)}`);
  }
  function printSlips(list: Slip[]) {
    setPrinting(list);
    setTimeout(() => { void printPage(t('hr2.payslip')).finally(() => setTimeout(() => setPrinting(null), 400)); }, 80);
  }
  const st = cur?.run.status;
  return (
    <div className="mx-body" style={{ gridTemplateColumns: 'minmax(220px, 340px) minmax(0, 1fr) minmax(280px, 380px)' }}>
      <section className="mx-box"><h3>{t('hr2.runs')}</h3>
        {canEdit && <div className="mx-row" style={{ padding: 6, gap: 6 }}>
          <input type="month" dir="ltr" value={period} onChange={(e) => setPeriod(e.currentTarget.value)} />
          <button className="mx-btn" onClick={() => void run(api<RunDetail>(`${base}/payroll`, { method: 'POST', body: { period } }), (d) => { reloadRuns(); setCur(d); })}>{t('hr2.newRun')}</button>
        </div>}
        <DataGrid rows={runs} cols={runCols} rowKey={(r) => r.id} selected={cur?.run.id ?? null} onSelect={(r) => { setSlip(null); open(r.id); }} />
        <PostDateOption base={base} canEdit={canEdit} />
      </section>
      <section className="mx-box"><h3>{cur ? `${t('hr2.payroll')} ${cur.run.period} · ${t(`hr2.rs.${cur.run.status}`)}${cur.run.approved_by_name ? ` · ${t('hr2.approvedBy')} ${cur.run.approved_by_name}` : ''}` : t('hr2.pickRun')}</h3>
        {cur && (<>
          <div className="mx-row" style={{ padding: 6, gap: 6, flexWrap: 'wrap' }}>
            {st === 'draft' && canEdit && <button className="mx-btn" onClick={() => void act('/recalculate')}>{t('hr2.recalc')}</button>}
            {st === 'draft' && can(me, 'payroll.approve') && <button className="mx-btn main" onClick={() => void act('/approve')}>{t('hr2.approveRun')}</button>}
            {st === 'approved' && can(me, 'payroll.approve') && <button className="mx-btn" onClick={() => void act('/unapprove')}>{t('hr2.unapprove')}</button>}
            {(st === 'approved' || (st === 'draft' && can(me, 'payroll.approve'))) && canEdit && <button className="mx-btn main" onClick={() => void act('/post')}>{t('hr2.post')}</button>}
            {st === 'posted' && canEdit && (<>
              <select value={box} onChange={(e) => setBox(e.currentTarget.value)}><option value="">{t('hr2.payFromBox')}</option>{boxes.map((m) => <option key={m.id} value={m.id}>{m.name} ({m.currency})</option>)}</select>
              <button className="mx-btn" disabled={!box} onClick={() => void act('/pay', { method_id: box, date: today() })}>{t('hr2.pay')}</button>
            </>)}
            {st === 'posted' && can(me, 'payroll.void') && <button className="mx-btn" onClick={() => void act('/void')}>{t('hr2.void')}</button>}
            {st === 'draft' && can(me, 'payroll.void') && <button className="mx-btn" onClick={() => void run(api(`${base}/payroll/${cur.run.id}`, { method: 'DELETE' }), () => { setCur(null); reloadRuns(); })}>{t('hr2.delete')}</button>}
            <button className="mx-btn" onClick={() => printSlips(cur.payslips)}>{t('hr2.printAll')}</button>
            <button className="mx-btn" onClick={() => void wps()}>{t('hr2.wps')}</button>
          </div>
          <div className="mx-row" style={{ padding: '0 6px', gap: 12, flexWrap: 'wrap' }}>
            {cur.run.totals.map((x) => <span key={x.currency} dir="ltr">{x.currency}: {t('hr2.gross')} {formatAmount(x.gross)} · {t('hr2.net')} {formatAmount(x.net)} · {t('hr2.social')} {formatAmount(x.nssf)}{x.tax && Number(x.tax) ? ` · ${t('hr2.tax')} ${formatAmount(x.tax)}` : ''} · {t('hr2.paidAmt')} {formatAmount(x.paid)}</span>)}
          </div>
          <DataGrid rows={cur.payslips} cols={slipCols} rowKey={(r) => r.id} selected={slip?.id ?? null} title={`${t('hr2.payroll')} ${cur.run.period}`}
            onSelect={(r) => { setSlip(r); setMan({ allow: r.manual_allow === '0' ? '' : r.manual_allow, ded: r.manual_ded === '0' ? '' : r.manual_ded, rec: r.manual_recovery ?? '' }); }} />
        </>)}
        <Flash m={msg} />
      </section>
      <section className="mx-box"><h3>{slip ? `${t('hr2.payslip')} · ${slip.name}` : t('hr2.pickSlip')}</h3>
        {slip && cur && (
          <div className="mx-pay">
            <table className="mx-table"><tbody>
              {slip.lines.map((l, i) => <tr key={i} className={l.kind === 'employer' ? 'muted' : ''}><td>{t(`hr2.ln.${l.code}`, { defaultValue: l.label })}<br /><small dir="ltr">{l.basis}</small></td>
                <td dir="ltr" className={l.kind === 'deduction' ? 'red' : ''}>{l.kind === 'deduction' ? '−' : l.kind === 'employer' ? '(' : ''}{formatAmount(l.amount, decimalsOf(l.currency))} {l.currency}{l.kind === 'employer' ? ')' : ''}</td></tr>)}
              <tr><td><b>{t('hr2.net')}</b></td><td dir="ltr"><b>{amt(slip.net, slip.currency)}{slip.currency2 ? ` + ${amt(slip.net2, slip.currency2)}` : ''}</b></td></tr>
            </tbody></table>
            <small>{t('hr2.employerNote')}</small>
            {cur.run.status === 'draft' && canEdit && slip.detailed && (<>
              <label>{t('hr2.manualAllow')}<input dir="ltr" value={man.allow} onChange={(e) => setMan({ ...man, allow: e.currentTarget.value })} /></label>
              <label>{t('hr2.manualDed')}<input dir="ltr" value={man.ded} onChange={(e) => setMan({ ...man, ded: e.currentTarget.value })} /></label>
              <label>{t('hr2.manualRec')}<input dir="ltr" placeholder={t('hr2.auto')} value={man.rec} onChange={(e) => setMan({ ...man, rec: e.currentTarget.value })} /></label>
              <button className="mx-btn" onClick={() => void act(`/payslips/${slip.id}`, { allowances: man.allow || '0', deductions: man.ded || '0', advance_recovery: man.rec }, 'PATCH')}>{t('hr2.apply')}</button>
            </>)}
            <div className="mx-row" style={{ gap: 6 }}>
              <button className="mx-btn" onClick={() => printSlips([slip])}>{t('hr2.print')}</button>
              <button className="mx-btn" onClick={() => whatsapp(slip)}>WhatsApp</button>
            </div>
          </div>
        )}
      </section>
      {printing && cur && createPortal(<div className="mx-printhost"><div className="print-doc" dir={i18n.dir()}>
        {printing.map((s) => <PayslipSheet key={s.id} s={s} period={cur.run.period} companyName={i18n.language === 'ar' ? cur.company.name_ar || cur.company.name_en : cur.company.name_en} />)}
      </div></div>, document.body)}
    </div>
  );
}

export function PayslipSheet({ s, period, companyName }: { s: Pick<Slip, 'name' | 'code' | 'position' | 'currency' | 'net' | 'currency2' | 'net2' | 'lines' | 'worked_days' | 'absent_days' | 'overtime_hours'>; period: string; companyName: string }) {
  const { t } = useTranslation();
  const { decimalsOf } = useCompany();
  const rows = (kind: string) => s.lines.filter((l) => l.kind === kind);
  return (
    <div className="mx-payslip" style={{ breakAfter: 'page', pageBreakAfter: 'always', marginBottom: 24 }}>
      <h2 style={{ margin: 0 }}>{companyName}</h2>
      <div>{t('hr2.payslip')} · <b dir="ltr">{period}</b></div>
      <div><b>{s.name}</b> {s.code ? `(${s.code})` : ''} {s.position ? `· ${s.position}` : ''}</div>
      <div><small>{t('hr2.workedDays')}: {s.worked_days} · {t('hr2.absentDays')}: {s.absent_days} · {t('hr2.overtimeH')}: {s.overtime_hours}</small></div>
      <table style={{ width: '100%', borderCollapse: 'collapse', marginTop: 8 }}><tbody>
        {(['earning', 'deduction', 'employer'] as const).filter((k) => rows(k).length > 0).flatMap((k) => [
          <tr key={k}><td colSpan={2} style={{ paddingTop: 6 }}><b>{t(`hr2.kind.${k}`)}</b></td></tr>,
          ...rows(k).map((l, j) => (
            <tr key={`${k}-${j}`}><td style={{ borderBottom: '1px solid var(--mx-cell-line)' }}>{t(`hr2.ln.${l.code}`, { defaultValue: l.label })} <small dir="ltr">{l.basis}</small></td>
              <td style={{ borderBottom: '1px solid var(--mx-cell-line)', textAlign: 'end' }} dir="ltr">{formatAmount(l.amount, decimalsOf(l.currency))} {l.currency}</td></tr>)),
        ])}
        <tr><td style={{ paddingTop: 8 }}><b>{t('hr2.net')}</b></td><td style={{ textAlign: 'end', paddingTop: 8 }} dir="ltr"><b>{formatAmount(s.net, decimalsOf(s.currency))} {s.currency}{s.currency2 ? ` + ${formatAmount(s.net2, decimalsOf(s.currency2))} ${s.currency2}` : ''}</b></td></tr>
      </tbody></table>
      <div style={{ marginTop: 24, display: 'flex', justifyContent: 'space-between' }}><span>{t('hr2.signEmployee')} ____________</span><span>{t('hr2.signEmployer')} ____________</span></div>
    </div>
  );
}

// ---------------- rules ----------------

interface ParamsData { country: string; date: string; values: Record<string, string>; keys: string[]; defaults: { key: string; value: string; effective_from: string }[];
  overrides: { id: string; key: string; value: string; effective_from: string }[]; wps_employer_id: string; wps_bank_code: string }
function Rules({ me }: { me: Me }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const base = company ? `/companies/${company.id}` : null;
  const [date, setDate] = useState(today());
  const [p, setP] = useState<ParamsData | null>(null);
  const [np, setNp] = useState({ key: '', value: '', effective_from: today() });
  const [wps, setWps] = useState({ id: '', bank: '' });
  const { msg, run } = useRun(t);
  const canEdit = can(me, 'payroll.approve');
  const load = useCallback(() => {
    if (base) api<ParamsData>(`${base}/hr/payroll-params?date=${date}`).then((x) => { setP(x); setWps({ id: x.wps_employer_id, bank: x.wps_bank_code }); }).catch(() => setP(null));
  }, [base, date]);
  useEffect(load, [load]);
  if (!p) return null;
  return (
    <div className="mx-body" style={{ gridTemplateColumns: 'minmax(0, 1fr) minmax(280px, 420px)' }}>
      <section className="mx-box"><h3>{t('hr2.rulesIn', { country: p.country || '—' })} <input type="date" dir="ltr" value={date} onChange={(e) => setDate(e.currentTarget.value)} /></h3>
        <div className="mx-scroll" style={{ overflowX: 'auto' }}><table className="mx-table" style={{ tableLayout: 'fixed', width: '100%' }}><thead><tr><th>{t('hr2.rule')}</th><th>{t('hr2.value')}</th></tr></thead>
          <tbody>{p.keys.filter((k) => p.values[k] !== undefined).map((k) => <tr key={k}><td>{t(`hr2.pk.${k}`, { defaultValue: k })}<br /><small dir="ltr">{k}</small></td><td dir="ltr" style={{ wordBreak: 'break-all' }}>{p.values[k]}</td></tr>)}</tbody></table></div>
        <small>{t('hr2.rulesSource')}</small>
      </section>
      <section className="mx-box"><h3>{t('hr2.override')}</h3>
        <div className="mx-pay">
          {canEdit && (<>
            <label>{t('hr2.rule')}<select value={np.key} onChange={(e) => setNp({ ...np, key: e.currentTarget.value, value: p.values[e.currentTarget.value] ?? '' })}><option value="">—</option>{p.keys.map((k) => <option key={k} value={k}>{t(`hr2.pk.${k}`, { defaultValue: k })}</option>)}</select></label>
            <label>{t('hr2.value')}<input dir="ltr" value={np.value} onChange={(e) => setNp({ ...np, value: e.currentTarget.value })} /></label>
            <label>{t('hr2.effectiveFrom')}<input type="date" dir="ltr" value={np.effective_from} onChange={(e) => setNp({ ...np, effective_from: e.currentTarget.value })} /></label>
            <button className="mx-btn main" disabled={!np.key} onClick={() => void run(api(`${base}/hr/payroll-params`, { method: 'POST', body: np }), load)}>{t('hr2.save')}</button>
          </>)}
          <b>{t('hr2.history')}</b>
          {p.overrides.map((o) => <div key={o.id} className="mx-row" style={{ gap: 6 }}><span dir="ltr">{o.effective_from}</span> {t(`hr2.pk.${o.key}`, { defaultValue: o.key })} = <b dir="ltr" style={{ wordBreak: 'break-all' }}>{o.value}</b>
            {canEdit && <button className="mx-btn sm" onClick={() => void run(api(`${base}/hr/payroll-params`, { method: 'POST', body: { key: o.key, value: '', effective_from: o.effective_from } }), load)}>✕</button>}</div>)}
          <b>{t('hr2.wpsSetup')}</b>
          <label>{t('hr2.wpsEmployer')}<input dir="ltr" disabled={!canEdit} value={wps.id} onChange={(e) => setWps({ ...wps, id: e.currentTarget.value })} /></label>
          <label>{t('hr2.wpsBank')}<input dir="ltr" disabled={!canEdit} value={wps.bank} onChange={(e) => setWps({ ...wps, bank: e.currentTarget.value })} /></label>
          {canEdit && <button className="mx-btn" onClick={() => void run(api(`${base}/hr/payroll-params`, { method: 'POST', body: { wps_employer_id: wps.id, wps_bank_code: wps.bank } }), load)}>{t('hr2.save')}</button>}
          <Flash m={msg} />
        </div>
      </section>
    </div>
  );
}

// ---------------- reports ----------------

const REPORTS = ['register', 'social', 'tax', 'cost', 'attendance', 'leave'] as const;
function Reports() {
  const { t, i18n } = useTranslation();
  const { company } = useCompany();
  const base = company ? `/companies/${company.id}` : null;
  const [kind, setKind] = useState<typeof REPORTS[number]>('register');
  const [period, setPeriod] = useState(month());
  const [group, setGroup] = useState('employee');
  const [rows] = useList<Record<string, unknown>>(base && `${base}/reports/hr/${kind}?period=${period}&group=${group}`, [kind, period, group]);
  const cols = useMemo<Col<Record<string, unknown>>[]>(() => {
    if (kind === 'register') {
      const codes: string[] = [];
      for (const r of rows) for (const l of (r.lines as Line[]) ?? []) if (!codes.includes(`${l.code}|${l.currency}`)) codes.push(`${l.code}|${l.currency}`);
      return [
        { key: 'name', title: t('hr2.name'), get: (r) => String(r.name), width: 160 },
        ...codes.map((c) => { const [code, cur] = c.split('|'); return { key: c, title: `${t(`hr2.ln.${code}`, { defaultValue: code })} ${cur}`, num: true, total: 'sum' as const, width: 110,
          get: (r: Record<string, unknown>) => ((r.lines as Line[]) ?? []).filter((l) => l.code === code && l.currency === cur).reduce((a, l) => a + Number(l.amount), 0) || null }; }),
        { key: 'net', title: t('hr2.net'), get: (r) => Number(r.net), num: true, total: 'sum', width: 110 },
        { key: 'net2', title: t('hr2.net2'), get: (r) => Number(r.net2) || null, num: true, total: 'sum', width: 120 },
      ];
    }
    const keys = rows.length ? Object.keys(rows[0]).filter((k) => !['lines', 'id', 'employee_id'].includes(k)) : [];
    return keys.map((k) => ({ key: k, title: t(`hr2.rc.${k}`, { defaultValue: k }), width: 110,
      num: rows.some((r) => typeof r[k] === 'number' || (typeof r[k] === 'string' && /^-?\d+(\.\d+)?$/.test(String(r[k])) && !['code', 'nssf_number', 'national_id'].includes(k))),
      total: (['employee_share', 'employer_share', 'family_employer', 'eos_employer', 'total', 'tax', 'earnings', 'employer', 'worked_minutes', 'overtime_minutes', 'days'] as string[]).includes(k) ? 'sum' as const : undefined,
      get: (r: Record<string, unknown>) => {
        const v = r[k];
        if (k.startsWith('name_')) return leaveName(r as never, i18n.language);
        return v == null ? '' : typeof v === 'number' ? v : /^-?\d+(\.\d+)?$/.test(String(v)) && !['code', 'nssf_number', 'national_id'].includes(k) ? Number(v) : String(v);
      } }));
  }, [rows, kind, t, i18n.language]);
  return (
    <div className="mx-col">
      <div className="mx-strip">
        <select value={kind} onChange={(e) => setKind(e.currentTarget.value as typeof REPORTS[number])}>{REPORTS.map((r) => <option key={r} value={r}>{t(`hr2.rep.${r}`)}</option>)}</select>
        <input type="month" dir="ltr" value={period} onChange={(e) => setPeriod(e.currentTarget.value)} />
        {kind === 'cost' && <select value={group} onChange={(e) => setGroup(e.currentTarget.value)}>{['employee', 'department', 'cost_center'].map((g) => <option key={g} value={g}>{t(`hr2.grp.${g}`)}</option>)}</select>}
        <small>{t(`hr2.repHint.${kind}`)}</small>
      </div>
      <section className="mx-box" style={{ flex: 1, minHeight: 0 }}>
        <DataGrid rows={rows} cols={cols} rowKey={(r) => String(r.id ?? r.code ?? r.label ?? JSON.stringify(r))} title={`${t(`hr2.rep.${kind}`)} ${period}`} footer />
      </section>
    </div>
  );
}
