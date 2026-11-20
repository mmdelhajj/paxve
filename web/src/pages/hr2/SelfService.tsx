import { useCallback, useEffect, useState } from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import { ApiError, api } from '../../api';
import { useCompany } from '../../company';
import { errorText, formatAmount, formatDate, today } from '../../util';
import { printPage } from '../../native';
import { PayslipSheet } from './HR';
import { SelfClaims, photoToBase64 } from '../fin/Finish';

interface Home {
  employee: { id: string; code: string; name: string; position: string; department: string; hire_date: string; photo: string; currency: string; currency2: string | null; manager_name: string | null; advances_open: string };
  attendance: { work_date: string; check_in: string | null; check_out: string | null; worked_minutes: number; late_minutes: number }[];
  leave_balances: { type_id: string; code: string; name_en: string; name_ar: string; name_fr: string; accrual: string; entitled: string | null; carried: string; taken: string; pending: string }[];
  leave: { id: string; date_from: string; date_to: string; days: string; status: string; reason: string; name_en: string; name_ar: string; name_fr: string }[];
  leave_types: { id: string; code: string; name_en: string; name_ar: string; name_fr: string }[];
  payslips: { id: string; period: string; currency: string; net: string; currency2: string | null; net2: string }[];
  advances: { advance_date: string; currency: string; amount: string; recovered: string; installment: string }[];
}
interface Team { id: string; name: string; name_en: string; name_ar: string; name_fr: string; date_from: string; date_to: string; days: string; reason: string }
interface SlipDetail {
  payslip: { name: string; code: string; position: string; currency: string; net: string; currency2: string | null; net2: string; period: string; worked_days: string; absent_days: string; overtime_hours: string };
  lines: { code: string; label: string; kind: string; role: string; currency: string; amount: string; basis: string }[];
  company: { name_en: string; name_ar: string };
}

const nm = (r: { name_en: string; name_ar?: string; name_fr?: string }, lang: string) => (lang === 'ar' && r.name_ar) || (lang === 'fr' && r.name_fr) || r.name_en;
const hm = (iso: string | null) => (iso ? new Date(iso).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' }) : '—');

/** Employee self-service (phone friendly): clock in/out, leave, payslips, advances; managers approve their team's leave. */
export default function SelfService() {
  const { t, i18n } = useTranslation();
  const { company } = useCompany();
  const base = company ? `/companies/${company.id}` : null;
  const [home, setHome] = useState<Home | null>(null);
  const [err, setErr] = useState('');
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [team, setTeam] = useState<Team[]>([]);
  const [req, setReq] = useState({ type_id: '', date_from: today(), date_to: today(), reason: '' });
  const [slip, setSlip] = useState<SlipDetail | null>(null);
  const [busy, setBusy] = useState(false);
  const [selfie, setSelfie] = useState<string>('');
  const load = useCallback(() => {
    if (!base) return;
    api<Home & { no_employee_file?: boolean }>(`${base}/self`).then((h) => {
      if (h.no_employee_file) { setErr('no_file'); return; }
      setHome(h); setErr('');
      api<Team[]>(`${base}/self/team-leave`).then(setTeam).catch(() => setTeam([]));
    }).catch((e) => setErr(e instanceof ApiError && e.code === 'no_employee_file' ? 'no_file' : errorText(t, e)));
  }, [base, t]);
  useEffect(load, [load]);
  async function act<T>(p: Promise<T>, ok: string) {
    try { await p; setMsg({ ok: true, text: ok }); load(); } catch (e) { setMsg({ ok: false, text: errorText(t, e) }); }
  }
  async function clock() {
    if (!base) return;
    setBusy(true);
    const pos = await new Promise<GeolocationPosition | null>((res) => {
      if (!navigator.geolocation) { res(null); return; }
      navigator.geolocation.getCurrentPosition(res, () => res(null), { enableHighAccuracy: true, timeout: 8000 });
    });
    const body: Record<string, unknown> = pos ? { lat: pos.coords.latitude, lng: pos.coords.longitude } : {};
    if (selfie) body.photo = selfie;
    await act(api(`${base}/self/clock`, { method: 'POST', body }), t('hr2.self.clocked'));
    setSelfie('');
    setBusy(false);
  }
  if (err === 'no_file') return <div className="mx-pay"><p>{t('hr2.self.noFile')}</p></div>;
  if (err) return <div className="mx-pay"><div className="mx-flash bad">{err}</div></div>;
  if (!home) return null;
  const e = home.employee;
  const todayRow = home.attendance.find((a) => a.work_date.slice(0, 10) === today());
  const clockLabel = !todayRow?.check_in ? t('hr2.self.clockIn') : t('hr2.self.clockOut');
  return (
    <div className="mx-self" style={{ maxWidth: 640, margin: '0 auto', padding: 12, display: 'flex', flexDirection: 'column', gap: 14 }}>
      <div className="mx-row" style={{ gap: 12 }}>
        {e.photo ? <img src={e.photo} alt="" style={{ width: 64, height: 64, borderRadius: '50%', objectFit: 'cover' }} /> : null}
        <div><b style={{ fontSize: 18 }}>{e.name}</b><br /><small>{e.position}{e.department ? ` · ${e.department}` : ''}{e.manager_name ? ` · ${t('hr2.manager')}: ${e.manager_name}` : ''}</small></div>
      </div>
      <section className="mx-box"><h3>{t('hr2.self.today')}</h3>
        <div className="mx-pay">
          <div>{t('hr2.in')}: <b dir="ltr">{hm(todayRow?.check_in ?? null)}</b> · {t('hr2.out')}: <b dir="ltr">{hm(todayRow?.check_out ?? null)}</b>
            {todayRow?.late_minutes ? <span className="red"> · {t('hr2.late')} {todayRow.late_minutes}′</span> : null}</div>
          <button className="mx-btn main" style={{ fontSize: 20, padding: '14px 0' }} disabled={busy} onClick={() => void clock()}>{clockLabel}</button>
          <label className="mx-row" style={{ gap: 6 }}>{t('fin.clock.selfie')}<input type="file" accept="image/*" capture="user" onChange={(ev) => { const f = ev.currentTarget.files?.[0]; if (f) void photoToBase64(f).then(setSelfie); }} />{selfie ? ' OK' : ''}</label>
          <small>{t('hr2.self.locationHint')}</small>
          {msg && <div className={`mx-flash ${msg.ok ? 'good' : 'bad'}`}>{msg.text}</div>}
        </div>
      </section>
      {base && <SelfClaims base={base} currency={e.currency} />}
      {team.length > 0 && (
        <section className="mx-box"><h3>{t('hr2.self.team')}</h3>
          <div className="mx-pay">{team.map((r) => (
            <div key={r.id} className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
              <b>{r.name}</b> {nm(r, i18n.language)} · {formatDate(r.date_from)} → {formatDate(r.date_to)} ({r.days}) {r.reason}
              <button className="mx-btn sm" onClick={() => void act(api(`${base}/hr/leave/${r.id}/decide`, { method: 'POST', body: { decision: 'approve' } }), t('hr2.saved'))}>{t('hr2.approve')}</button>
              <button className="mx-btn sm" onClick={() => void act(api(`${base}/hr/leave/${r.id}/decide`, { method: 'POST', body: { decision: 'reject' } }), t('hr2.saved'))}>{t('hr2.reject')}</button>
            </div>))}</div>
        </section>
      )}
      <section className="mx-box"><h3>{t('hr2.balances')}</h3>
        <div className="mx-pay">
          {home.leave_balances.filter((b) => b.accrual !== 'none').map((b) => (
            <div key={b.type_id}>{nm(b, i18n.language)}: <b dir="ltr">{(Number(b.entitled ?? 0) + Number(b.carried) - Number(b.taken) - Number(b.pending)).toFixed(1)}</b> {t('hr2.days')}</div>))}
          <b>{t('hr2.self.request')}</b>
          <select value={req.type_id} onChange={(ev) => setReq({ ...req, type_id: ev.currentTarget.value })}><option value="">{t('hr2.leaveType')}</option>
            {home.leave_types.map((x) => <option key={x.id} value={x.id}>{nm(x, i18n.language)}</option>)}</select>
          <div className="mx-row" style={{ gap: 6 }}>
            <input type="date" dir="ltr" value={req.date_from} onChange={(ev) => setReq({ ...req, date_from: ev.currentTarget.value })} aria-label={t('hr2.from')} />
            <input type="date" dir="ltr" value={req.date_to} onChange={(ev) => setReq({ ...req, date_to: ev.currentTarget.value })} aria-label={t('hr2.to')} />
          </div>
          <input placeholder={t('hr2.reason')} value={req.reason} onChange={(ev) => setReq({ ...req, reason: ev.currentTarget.value })} />
          <button className="mx-btn main" disabled={!req.type_id} onClick={() => void act(api(`${base}/self/leave`, { method: 'POST', body: req }), t('hr2.self.sent'))}>{t('hr2.self.send')}</button>
          {home.leave.map((l) => (
            <div key={l.id} className="mx-row" style={{ gap: 6 }}>{nm(l, i18n.language)} · {formatDate(l.date_from)} → {formatDate(l.date_to)} · {t(`hr2.ls.${l.status}`)}
              {l.status === 'pending' && <button className="mx-btn sm" onClick={() => void act(api(`${base}/self/leave/${l.id}/cancel`, { method: 'POST' }), t('hr2.saved'))}>{t('hr2.cancel')}</button>}</div>))}
        </div>
      </section>
      <section className="mx-box"><h3>{t('hr2.self.payslips')}</h3>
        <div className="mx-pay">
          {home.payslips.length === 0 && <small>{t('hr2.self.noPayslips')}</small>}
          {home.payslips.map((p) => (
            <button key={p.id} className="mx-btn" onClick={() => void api<SlipDetail>(`${base}/self/payslips/${p.id}`).then((d) => {
              setSlip(d); setTimeout(() => { void printPage(t('hr2.payslip')).finally(() => setTimeout(() => setSlip(null), 400)); }, 80);
            }).catch((x) => setMsg({ ok: false, text: errorText(t, x) }))}>
              <span dir="ltr">{p.period}</span> · {formatAmount(p.net)} {p.currency}{p.currency2 ? ` + ${formatAmount(p.net2, 0)} ${p.currency2}` : ''}
            </button>))}
          {Number(e.advances_open) > 0 && <div>{t('hr2.self.advancesOpen')}: <b dir="ltr">{formatAmount(e.advances_open)} {e.currency}</b></div>}
        </div>
      </section>
      {slip && createPortal(<div className="mx-printhost"><div className="print-doc" dir={i18n.dir()}>
        <PayslipSheet s={{ ...slip.payslip, lines: slip.lines }} period={slip.payslip.period} companyName={i18n.language === 'ar' ? slip.company.name_ar || slip.company.name_en : slip.company.name_en} />
      </div></div>, document.body)}
    </div>
  );
}
