import { lazy, Suspense, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import {
  Bar, CartesianGrid, ComposedChart, Legend, Line, ReferenceLine, ResponsiveContainer, Tooltip, XAxis, YAxis,
} from 'recharts';
import { api, can, type Me } from '../../api';
import { formatAmount, formatDate, formatDateTime, errorText } from '../../util';
import { useBase, useLoad, useRun } from '../wh/Wh';

const RouteView = lazy(() => import('./Route'));
const NewBill = lazy(() => import('../purchases/Bills').then((m) => ({ default: m.NewBill })));

// Smart (app "ai", Magic-style screens): alerts center, AI assistant, 13-week cash forecast, late-payer / churn risk,
// smart reorder, collector route, supplier bills read from a photo, and the thresholds.

type Msg = { ok: boolean; text: string } | null;
function Flash({ m }: { m: Msg }) {
  return m ? <div className={`mx-flash ${m.ok ? 'good' : 'bad'}`}>{m.text}</div> : null;
}

const MODES = ['alerts', 'assistant', 'forecast', 'risk', 'reorder', 'route', 'scans', 'settings'] as const;
type Mode = typeof MODES[number];
const amt = (v: string | number | null | undefined, d = 2) => formatAmount(String(v ?? 0), d);
const qty = (v: number | string | null | undefined) => (v == null || v === '' ? '' : String(Math.round(Number(v) * 1000) / 1000));

export default function Smart({ me, initial }: { me: Me; initial?: string }) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<Mode>((MODES as readonly string[]).includes(initial ?? '') ? initial as Mode : 'alerts');
  return (
    <div className="mx-col">
      <div className="mx-strip"><b>{t(`sm.m.${mode}`)}</b><span className="mx-sp" /><small style={{ opacity: 0.8 }}>{t(`sm.help.${mode}`)}</small></div>
      <div className="mx-modes" role="tablist">
        {MODES.map((m) => <button key={m} role="tab" className="mx-mode" aria-selected={mode === m} onClick={() => setMode(m)}>{t(`sm.m.${m}`)}</button>)}
      </div>
      <div style={{ flex: 1, minHeight: 0, overflow: 'auto', padding: 6 }}>
        {mode === 'alerts' && <Alerts me={me} />}
        {mode === 'assistant' && <Assistant me={me} />}
        {mode === 'forecast' && <Forecast />}
        {mode === 'risk' && <Risk me={me} />}
        {mode === 'reorder' && <Reorder me={me} />}
        {mode === 'route' && <Suspense fallback={null}><RouteView /></Suspense>}
        {mode === 'scans' && <Scans me={me} />}
        {mode === 'settings' && <Settings me={me} />}
      </div>
    </div>
  );
}

// ======================= alerts =======================

export interface Alert {
  id: string; kind: string; severity: string; subject_type: string; subject_id: string | null; subject_label: string; period: string;
  value: number | null; expected: number | null; score: number | null; detail: Record<string, unknown>; status: string; note: string;
  acted_at: string | null; acted_by_name: string | null; created_at: string;
}
const KINDS = ['diesel', 'discount', 'cash_diff', 'cash_repeat', 'voids', 'off_hours', 'count_loss', 'collector_drop', 'dup_bill', 'fleet_fuel', 'price_override'];
const sevPill: Record<string, string> = { high: 'off', medium: 'warn', low: '' };

/** One sentence that explains the alert, from its numbers. */
export function alertText(t: TFunction, a: Alert): string {
  const d = a.detail ?? {};
  const v = { ...d, value: amt(a.value), expected: amt(a.expected) } as Record<string, unknown>;
  if (a.kind === 'dup_bill') v.what = t(d.same_ref ? 'sm.al.sameRef' : 'sm.al.sameAmount');
  if (a.kind === 'fleet_fuel') v.anomaly = t(`sm.al.fa.${String(d.anomaly ?? '')}`, { defaultValue: String(d.anomaly ?? '') });
  if (a.kind === 'voids' || a.kind === 'cash_repeat') v.value = String(a.value ?? 0);
  return t(`sm.al.text.${a.kind}`, v);
}

function Alerts({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [status, setStatus] = useState('active');
  const [kind, setKind] = useState('');
  const [d, reload] = useLoad<{ alerts: Alert[]; summary: { kind: string; open: number; ack: number; high: number }[]; last_scan_at: string | null } | null>(
    base && `${base}/smart/alerts?status=${status}${kind ? `&kind=${kind}` : ''}`, null);
  const [msg, run, setMsg] = useRun(reload);
  const [note, setNote] = useState<Record<string, string>>({});
  const manage = can(me, 'smart.manage');
  const act = (a: Alert, action: string) => void run(api(`${base}/smart/alerts/${a.id}/${action}`, { method: 'POST', body: { note: note[a.id] ?? '' } }));
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
        {KINDS.map((k) => {
          const s = d?.summary.find((x) => x.kind === k);
          const n = (s?.open ?? 0) + (s?.ack ?? 0);
          return (
            <button key={k} className="mx-btn sm" aria-pressed={kind === k} style={kind === k ? { borderColor: 'var(--mx-primary)' } : undefined}
              onClick={() => setKind(kind === k ? '' : k)}>
              {t(`sm.al.k.${k}`)} <b>{n}</b>{s && s.high > 0 && <span className="mx-pill off" style={{ marginInlineStart: 4 }}>{s.high}</span>}
            </button>);
        })}
      </div>
      <section className="mx-box">
        <h3 style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>{t('sm.m.alerts')}
          <select value={status} onChange={(e) => setStatus(e.currentTarget.value)} aria-label={t('sm.al.status')}>
            {['active', 'open', 'ack', 'resolved', 'dismissed', 'all'].map((s) => <option key={s} value={s}>{t(`sm.al.st.${s}`)}</option>)}
          </select>
          <span className="mx-sp" />
          {d?.last_scan_at && <small>{t('sm.al.lastScan', { at: formatDateTime(d.last_scan_at) })}</small>}
          {manage && <button className="mx-btn main sm" onClick={() => void run(api<{ new: number; open: number }>(`${base}/smart/alerts/scan`, { method: 'POST', body: {} }),
            (x) => setMsg({ ok: true, text: t('sm.al.scanned', { n: (x as { new: number }).new }) }))}>{t('sm.al.scan')}</button>}
        </h3>
        <Flash m={msg} />
        <div style={{ overflowX: 'auto' }}>
          <table className="mx-table">
            <thead><tr><th>{t('sm.al.severity')}</th><th>{t('sm.al.kind')}</th><th>{t('sm.al.subject')}</th><th>{t('sm.al.what')}</th><th>{t('sm.al.period')}</th>
              <th>{t('sm.al.raised')}</th><th>{t('sm.al.status')}</th>{manage && <th />}</tr></thead>
            <tbody>
              {(d?.alerts ?? []).map((a) => (
                <tr key={a.id} style={{ opacity: a.status === 'resolved' || a.status === 'dismissed' ? 0.6 : 1 }}>
                  <td><span className={`mx-pill ${sevPill[a.severity] ?? ''}`}>{t(`sm.al.sev.${a.severity}`)}</span></td>
                  <td>{t(`sm.al.k.${a.kind}`)}</td>
                  <td><b>{a.subject_label}</b></td>
                  <td style={{ whiteSpace: 'normal', minWidth: 260 }}>{alertText(t, a)}{a.note && <><br /><small>📝 {a.note}</small></>}</td>
                  <td dir="ltr">{a.period}</td>
                  <td>{formatDate(a.created_at)}</td>
                  <td>{t(`sm.al.st.${a.status}`)}{a.acted_by_name && <><br /><small>{a.acted_by_name}</small></>}</td>
                  {manage && (
                    <td style={{ whiteSpace: 'nowrap' }}>
                      <input placeholder={t('sm.al.note')} style={{ width: 120 }} value={note[a.id] ?? ''} onChange={(e) => setNote({ ...note, [a.id]: e.currentTarget.value })} />{' '}
                      {a.status === 'open' && <button className="mx-btn sm" onClick={() => act(a, 'ack')}>{t('sm.al.ack')}</button>}{' '}
                      {(a.status === 'open' || a.status === 'ack') && <>
                        <button className="mx-btn sm" onClick={() => act(a, 'resolve')}>{t('sm.al.resolve')}</button>{' '}
                        <button className="mx-btn sm" onClick={() => act(a, 'dismiss')}>{t('sm.al.dismiss')}</button></>}
                      {(a.status === 'resolved' || a.status === 'dismissed') && <button className="mx-btn sm" onClick={() => act(a, 'reopen')}>{t('sm.al.reopen')}</button>}
                    </td>)}
                </tr>))}
              {d && d.alerts.length === 0 && <tr><td colSpan={8} className="mx-empty">{t('sm.al.none')}</td></tr>}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  );
}

// ======================= AI assistant =======================

interface AIStatus { enabled: boolean; model: string; used: number; limit: number | null; can_ask: boolean; can_scan: boolean }
interface Turn { role: 'user' | 'assistant'; content: string; error?: string; tools?: string[] }

export function useAIStatus(): [AIStatus | null, () => void] {
  const base = useBase();
  return useLoad<AIStatus | null>(base && `${base}/smart/ai/status`, null);
}

function AINotOn({ st }: { st: AIStatus | null }) {
  const { t } = useTranslation();
  if (!st || st.enabled) return null;
  return <div className="mx-note" style={{ padding: 10 }}><b>{t('sm.ai.off')}</b><br />{t('sm.ai.offHelp')}</div>;
}

export function Assistant({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [st, reloadSt] = useAIStatus();
  const [turns, setTurns] = useState<Turn[]>([]);
  const [text, setText] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  const end = useRef<HTMLDivElement>(null);
  useEffect(() => { end.current?.scrollIntoView({ block: 'end' }); }, [turns, busy]);
  const canAsk = can(me, 'smart.ai') && !!st?.enabled;
  async function ask(q: string) {
    const question = q.trim();
    if (!question || busy || !base) return;
    const history = turns.filter((x) => !x.error).slice(-18);
    const next: Turn[] = [...turns, { role: 'user', content: question }];
    setTurns(next);
    setText('');
    setErr('');
    setBusy(true);
    try {
      const msgs = [...history, { role: 'user' as const, content: question }].map((x) => ({ role: x.role, content: x.content }));
      const { id } = await api<{ id: string }>(`${base}/smart/ai/ask`, { method: 'POST', body: { messages: msgs } });
      for (let i = 0; i < 160; i++) {
        await new Promise((r) => setTimeout(r, i < 10 ? 800 : 1500));
        const a = await api<{ status: string; answer: string; error: string; tools: string[] }>(`${base}/smart/ai/answers/${id}`);
        if (a.status === 'running') continue;
        setTurns([...next, a.status === 'done'
          ? { role: 'assistant', content: a.answer || t('sm.ai.empty'), tools: a.tools }
          : { role: 'assistant', content: t(`errors.${a.error}`, { defaultValue: t('sm.ai.failed') }), error: a.error }]);
        break;
      }
    } catch (e) {
      setErr(errorText(t, e));
    } finally {
      setBusy(false);
      reloadSt();
    }
  }
  const examples = ['s1', 's2', 's3', 's4', 's5', 's6'].map((k) => t(`sm.ai.ex.${k}`));
  return (
    <div className="mx-row" style={{ gap: 8, alignItems: 'stretch', flexWrap: 'wrap', minHeight: '100%' }}>
      <section className="mx-box" style={{ flex: '999 1 520px', minWidth: 0, display: 'flex', flexDirection: 'column' }}>
        <h3 style={{ display: 'flex', gap: 8 }}>{t('sm.ai.title')}<span className="mx-sp" />
          {st && <small dir="ltr">{st.used}{st.limit != null ? ` / ${st.limit}` : ''} · {t('sm.ai.thisMonth')}</small>}
          {turns.length > 0 && <button className="mx-btn sm" onClick={() => setTurns([])}>{t('sm.ai.new')}</button>}</h3>
        <AINotOn st={st} />
        <div style={{ flex: 1, minHeight: 240, overflow: 'auto', padding: 8, display: 'flex', flexDirection: 'column', gap: 8 }}>
          {turns.length === 0 && <div className="mx-empty" style={{ padding: 20 }}>{t('sm.ai.hello')}</div>}
          {turns.map((x, i) => (
            <div key={i} dir="auto" style={{
              alignSelf: x.role === 'user' ? 'flex-end' : 'flex-start', maxWidth: '85%', whiteSpace: 'pre-wrap', lineHeight: 1.6, padding: '8px 12px', borderRadius: 10,
              background: x.role === 'user' ? 'var(--mx-primary-soft)' : x.error ? 'var(--mx-red-soft)' : '#f4f6f9', border: '1px solid var(--mx-line)',
            }}>
              {x.content}
              {x.tools && x.tools.length > 0 && <div><small style={{ opacity: 0.6 }}>{t('sm.ai.used')}: {[...new Set(x.tools)].map((k) => t(`sm.ai.tool.${k}`, { defaultValue: k })).join(' · ')}</small></div>}
            </div>))}
          {busy && <div className="mx-empty">{t('sm.ai.thinking')}</div>}
          <div ref={end} />
        </div>
        {err && <div className="mx-flash bad">{err}</div>}
        <form className="mx-row" style={{ gap: 6, padding: 6, borderTop: '1px solid var(--mx-line)' }} onSubmit={(e) => { e.preventDefault(); void ask(text); }}>
          <textarea dir="auto" rows={2} style={{ flex: 1, resize: 'vertical', font: 'inherit', padding: 6 }} disabled={!canAsk || busy} placeholder={t('sm.ai.placeholder')}
            value={text} maxLength={4000} onChange={(e) => setText(e.currentTarget.value)}
            onKeyDown={(e) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); void ask(text); } }} />
          <button className="mx-btn main" type="submit" disabled={!canAsk || busy || !text.trim()}>{t('sm.ai.send')}</button>
        </form>
      </section>
      <section className="mx-box" style={{ flex: '1 1 240px', minWidth: 0 }}>
        <h3>{t('sm.ai.tryThese')}</h3>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 4, padding: 6 }}>
          {examples.map((x) => <button key={x} className="mx-btn sm" style={{ textAlign: 'start', whiteSpace: 'normal' }} disabled={!canAsk || busy} onClick={() => void ask(x)}>{x}</button>)}
        </div>
        <p style={{ padding: 6, margin: 0 }}><small>{t('sm.ai.privacy')}</small></p>
      </section>
    </div>
  );
}

// ======================= forecast =======================

interface FcWeek { start: string; end: string; in: Record<string, number>; out: Record<string, number>; net: number; closing: number; closing_local: number | null; optimistic: number; pessimistic: number; below_zero: boolean }
interface Fc {
  base_currency: string; local_currency: string; local_rate: number | null; opening: number; weeks: FcWeek[]; months: { month: string; in: number; out: number; net: number; closing: number }[];
  first_negative_week: string; lowest: number; lowest_week: string; assumptions: Record<string, number | string>;
}
const IN_KEYS = ['receivables', 'subscriptions', 'pos'];
const OUT_KEYS = ['payables', 'payroll', 'recurring', 'expenses'];

function Forecast() {
  const { t } = useTranslation();
  const base = useBase();
  const [f] = useLoad<Fc | null>(base && `${base}/smart/forecast`, null);
  if (!f) return <div className="mx-empty">…</div>;
  const sum = (w: FcWeek, keys: string[], side: 'in' | 'out') => keys.reduce((s, k) => s + (w[side][k] ?? 0), 0);
  const data = f.weeks.map((w) => ({ week: w.start.slice(5).replace('-', '/'), in: sum(w, IN_KEYS, 'in'), out: -sum(w, OUT_KEYS, 'out'), closing: w.closing, optimistic: w.optimistic, pessimistic: w.pessimistic }));
  const cur = f.base_currency;
  const kpi = (label: string, v: string, bad = false, sub = '') => (
    <div className="mx-box" style={{ padding: 10, minWidth: 160, flex: '1 1 160px' }}><small>{label}</small>
      <div style={{ fontSize: 20, fontWeight: 700, color: bad ? 'var(--mx-red)' : undefined }}><span dir="ltr">{v}</span></div>{sub && <small>{sub}</small>}</div>);
  const a = f.assumptions;
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      <div className="mx-row" style={{ gap: 8, flexWrap: 'wrap' }}>
        {kpi(t('sm.fc.opening'), `${amt(f.opening)} ${cur}`)}
        {kpi(t('sm.fc.in13'), `${amt(f.weeks.reduce((s, w) => s + sum(w, IN_KEYS, 'in'), 0))} ${cur}`)}
        {kpi(t('sm.fc.out13'), `${amt(f.weeks.reduce((s, w) => s + sum(w, OUT_KEYS, 'out'), 0))} ${cur}`)}
        {kpi(t('sm.fc.lowest'), `${amt(f.lowest)} ${cur}`, f.lowest < 0, f.lowest_week ? `${t('sm.fc.week')} ${formatDate(f.lowest_week)}` : '')}
        {f.local_rate && kpi(t('sm.fc.inLocal', { cur: f.local_currency }), `${amt(f.weeks[12].closing_local, 0)} ${f.local_currency}`)}
      </div>
      {f.first_negative_week
        ? <div className="mx-flash bad">{t('sm.fc.negative', { week: formatDate(f.first_negative_week) })}</div>
        : <div className="mx-flash good">{t('sm.fc.positive')}</div>}
      <section className="mx-box">
        <h3>{t('sm.fc.chart')}</h3>
        <div style={{ height: 280, padding: 6 }} dir="ltr">
          <ResponsiveContainer width="100%" height="100%">
            <ComposedChart data={data} margin={{ top: 8, right: 12, left: 4, bottom: 0 }}>
              <CartesianGrid strokeDasharray="3 3" stroke="#e5e9f0" />
              <XAxis dataKey="week" tick={{ fontSize: 11 }} />
              <YAxis tick={{ fontSize: 11 }} width={70} />
              <Tooltip formatter={(v) => amt(Number(v))} />
              <Legend />
              <ReferenceLine y={0} stroke="#b42318" />
              <Bar dataKey="in" name={t('sm.fc.moneyIn')} fill="#0f7b4b" />
              <Bar dataKey="out" name={t('sm.fc.moneyOut')} fill="#b42318" />
              <Line dataKey="closing" name={t('sm.fc.closing')} stroke="#1d4ed8" strokeWidth={2} dot={false} />
              <Line dataKey="optimistic" name={t('sm.fc.optimistic')} stroke="#0f7b4b" strokeDasharray="4 3" dot={false} />
              <Line dataKey="pessimistic" name={t('sm.fc.pessimistic')} stroke="#a15c00" strokeDasharray="4 3" dot={false} />
            </ComposedChart>
          </ResponsiveContainer>
        </div>
      </section>
      <section className="mx-box">
        <h3>{t('sm.fc.weeks')} ({cur})</h3>
        <div style={{ overflowX: 'auto' }}>
          <table className="mx-table">
            <thead><tr><th>{t('sm.fc.week')}</th>{IN_KEYS.map((k) => <th key={k} className="num">{t(`sm.fc.k.${k}`)}</th>)}
              {OUT_KEYS.map((k) => <th key={k} className="num">{t(`sm.fc.k.${k}`)}</th>)}<th className="num">{t('sm.fc.net')}</th><th className="num">{t('sm.fc.closing')}</th>
              {f.local_rate && <th className="num">{f.local_currency}</th>}</tr></thead>
            <tbody>
              {f.weeks.map((w) => (
                <tr key={w.start} style={w.below_zero ? { background: 'var(--mx-red-soft)' } : undefined}>
                  <td>{formatDate(w.start)}</td>
                  {IN_KEYS.map((k) => <td key={k} className="num">{w.in[k] ? amt(w.in[k]) : ''}</td>)}
                  {OUT_KEYS.map((k) => <td key={k} className="num">{w.out[k] ? amt(w.out[k]) : ''}</td>)}
                  <td className="num" style={{ color: w.net < 0 ? 'var(--mx-red)' : 'var(--mx-green)' }}>{amt(w.net)}</td>
                  <td className="num"><b>{amt(w.closing)}</b></td>
                  {f.local_rate && <td className="num">{amt(w.closing_local, 0)}</td>}
                </tr>))}
            </tbody>
          </table>
        </div>
      </section>
      <div className="mx-row" style={{ gap: 8, flexWrap: 'wrap', alignItems: 'flex-start' }}>
        <section className="mx-box" style={{ flex: '1 1 320px' }}>
          <h3>{t('sm.fc.months')}</h3>
          <table className="mx-table"><thead><tr><th>{t('sm.fc.month')}</th><th className="num">{t('sm.fc.moneyIn')}</th><th className="num">{t('sm.fc.moneyOut')}</th><th className="num">{t('sm.fc.net')}</th><th className="num">{t('sm.fc.closing')}</th></tr></thead>
            <tbody>{f.months.map((m) => <tr key={m.month}><td dir="ltr">{m.month}</td><td className="num">{amt(m.in)}</td><td className="num">{amt(m.out)}</td><td className="num">{amt(m.net)}</td><td className="num"><b>{amt(m.closing)}</b></td></tr>)}</tbody>
          </table>
        </section>
        <section className="mx-box" style={{ flex: '1 1 320px' }}>
          <h3>{t('sm.fc.assumptions')}</h3>
          <ul style={{ margin: 0, padding: '6px 24px', lineHeight: 1.8 }}>
            <li>{t('sm.fc.a.ar', { open: amt(a.ar_open), exp: amt(a.ar_expected), days: a.avg_days_late, cur })}</li>
            <li>{t('sm.fc.a.subs', { amount: amt(a.subscriptions_monthly), rate: a.collection_rate, days: a.days_to_pay, cur })}</li>
            <li>{t('sm.fc.a.pos', { amount: amt(a.pos_weekly), cur })}</li>
            <li>{t('sm.fc.a.ap', { amount: amt(a.ap_open), cur })}</li>
            <li>{t('sm.fc.a.payroll', { amount: amt(a.payroll_monthly), cur })}</li>
            <li>{t('sm.fc.a.expenses', { amount: amt(a.expenses_weekly), cur })}</li>
            <li>{t('sm.fc.a.scenarios')}</li>
          </ul>
        </section>
      </div>
    </div>
  );
}

// ======================= risk =======================

interface RiskRow {
  customer_id: string; code: string; name: string; phone: string; area_name: string | null; score: number; band: string; late_score: number; churn_score: number;
  avg_days_late: number | null; overdue_base: number; factors: { code: string; value: number; points: number }[]; computed_at: string; has_location: boolean;
}
const bandPill: Record<string, string> = { high: 'off', medium: 'warn', low: 'on' };

export function factorText(t: TFunction, f: { code: string; value: number }): string {
  return t(`sm.rf.${f.code}`, { n: Math.round(f.value * 10) / 10 });
}

function Risk({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [band, setBand] = useState('');
  const [q, setQ] = useState('');
  const [d, reload] = useLoad<{ rows: RiskRow[]; summary: { band: string; customers: number; overdue_base: number }[]; computed_at: string | null } | null>(
    base && `${base}/smart/risk?band=${band}&q=${encodeURIComponent(q)}`, null);
  const [msg, run] = useRun(reload);
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      <div className="mx-row" style={{ gap: 8, flexWrap: 'wrap' }}>
        {['high', 'medium', 'low'].map((b) => {
          const s = d?.summary.find((x) => x.band === b);
          return (
            <button key={b} className="mx-box" style={{ padding: 10, minWidth: 170, flex: '1 1 170px', textAlign: 'start', cursor: 'pointer', outline: band === b ? '2px solid var(--mx-primary)' : undefined }}
              onClick={() => setBand(band === b ? '' : b)}>
              <span className={`mx-pill ${bandPill[b]}`}>{t(`sm.rk.band.${b}`)}</span>
              <div style={{ fontSize: 20, fontWeight: 700 }}>{s?.customers ?? 0}</div>
              <small>{t('sm.rk.overdue')}: <span dir="ltr">{amt(s?.overdue_base)}</span></small>
            </button>);
        })}
      </div>
      <section className="mx-box">
        <h3 style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>{t('sm.m.risk')}
          <input placeholder={t('sm.rk.search')} value={q} onChange={(e) => setQ(e.currentTarget.value)} />
          <span className="mx-sp" />
          {d?.computed_at && <small>{t('sm.rk.computed', { at: formatDateTime(d.computed_at) })}</small>}
          {can(me, 'smart.manage') && <button className="mx-btn sm" onClick={() => void run(api(`${base}/smart/risk/recompute`, { method: 'POST', body: {} }))}>{t('sm.rk.recompute')}</button>}
        </h3>
        <Flash m={msg} />
        <div style={{ overflowX: 'auto' }}>
          <table className="mx-table">
            <thead><tr><th>{t('sm.rk.customer')}</th><th>{t('sm.rk.area')}</th><th className="num">{t('sm.rk.score')}</th><th>{t('sm.rk.bandH')}</th>
              <th className="num">{t('sm.rk.late')}</th><th className="num">{t('sm.rk.churn')}</th><th className="num">{t('sm.rk.avgLate')}</th><th className="num">{t('sm.rk.overdue')}</th><th>{t('sm.rk.why')}</th></tr></thead>
            <tbody>
              {(d?.rows ?? []).map((r) => (
                <tr key={r.customer_id}>
                  <td><a className="mx-link" href={`#/customer/${r.customer_id}`}>{r.name}</a> <small dir="ltr">#{r.code} {r.phone}</small></td>
                  <td>{r.area_name ?? ''}</td>
                  <td className="num"><div style={{ display: 'flex', alignItems: 'center', gap: 4, justifyContent: 'flex-end' }}>
                    <span style={{ display: 'inline-block', width: 50, height: 6, background: '#eef1f5', borderRadius: 3 }}>
                      <span style={{ display: 'block', width: `${r.score}%`, height: 6, borderRadius: 3, background: r.band === 'high' ? '#b42318' : r.band === 'medium' ? '#a15c00' : '#0f7b4b' }} /></span>{r.score}</div></td>
                  <td><span className={`mx-pill ${bandPill[r.band]}`}>{t(`sm.rk.band.${r.band}`)}</span></td>
                  <td className="num">{r.late_score}</td><td className="num">{r.churn_score}</td>
                  <td className="num">{r.avg_days_late == null ? '' : Math.round(r.avg_days_late)}</td>
                  <td className="num">{r.overdue_base ? amt(r.overdue_base) : ''}</td>
                  <td style={{ whiteSpace: 'normal', minWidth: 220 }}>{r.factors.slice(0, 3).map((f) => factorText(t, f)).join(' · ')}</td>
                </tr>))}
              {d && d.rows.length === 0 && <tr><td colSpan={9} className="mx-empty">{t('sm.rk.none')}</td></tr>}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  );
}

// ======================= reorder =======================

interface RLine {
  item_id: string; code: string; name: string; unit: string; warehouse_id: string; warehouse_name: string; on_hand: number; on_order: number; weekly_demand: number;
  seasonal_factor: number | null; lead_days: number; lead_source: string; safety_stock: number; suggest_min: number; suggest_max: number; rule_min: number | null;
  rule_max: number | null; to_order: number; days_left: number | null; supplier_id: string | null; supplier_name: string | null; unit_price: number | null;
  currency: string | null; status: string; weeks: number;
}
const stPill: Record<string, string> = { order: 'off', watch: 'warn', ok: 'on' };

function Reorder({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [wh, setWh] = useState('');
  const [whs] = useLoad<{ id: string; name: string }[]>(base && `${base}/warehouses`, []);
  const [d, reload] = useLoad<{ lines: RLine[]; service_level: number; review_days: number; lead_days: number } | null>(base && `${base}/smart/reorder?warehouse_id=${wh}`, null);
  const [sel, setSel] = useState<Record<string, boolean>>({});
  const [qtyOf, setQtyOf] = useState<Record<string, string>>({});
  const [msg, run, setMsg] = useRun(reload);
  const key = (l: RLine) => `${l.item_id}:${l.warehouse_id}`;
  const lines = d?.lines ?? [];
  const chosen = lines.filter((l) => sel[key(l)]);
  return (
    <section className="mx-box">
      <h3 style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>{t('sm.m.reorder')}
        <select value={wh} onChange={(e) => setWh(e.currentTarget.value)} aria-label={t('sm.ro.warehouse')}><option value="">{t('sm.ro.allWh')}</option>{whs.map((w) => <option key={w.id} value={w.id}>{w.name}</option>)}</select>
        <button className="mx-btn sm" onClick={() => setSel(Object.fromEntries(lines.filter((l) => l.status === 'order').map((l) => [key(l), true])))}>{t('sm.ro.pickToOrder')}</button>
        <span className="mx-sp" />
        {d && <small>{t('sm.ro.params', { sl: d.service_level, review: d.review_days, lead: d.lead_days })}</small>}
        {can(me, 'stock.manage') && <button className="mx-btn sm" disabled={chosen.length === 0} onClick={() => void run(api(`${base}/smart/reorder/apply`, {
          method: 'POST', body: { lines: chosen.map((l) => ({ item_id: l.item_id, warehouse_id: l.warehouse_id, min_qty: String(l.suggest_min), max_qty: String(l.suggest_max), supplier_id: l.supplier_id ?? '' })) },
        }), () => setMsg({ ok: true, text: t('sm.ro.applied', { n: chosen.length }) }))}>{t('sm.ro.apply')}</button>}
        {can(me, 'purchasing.create') && <button className="mx-btn main sm" disabled={chosen.filter((l) => l.supplier_id).length === 0} onClick={() => void run(api<{ orders: { number: number }[] }>(`${base}/smart/reorder/purchase`, {
          method: 'POST', body: { lines: chosen.filter((l) => l.supplier_id).map((l) => ({ item_id: l.item_id, warehouse_id: l.warehouse_id, supplier_id: l.supplier_id, quantity: qtyOf[key(l)] || String(l.to_order || l.suggest_max) })) },
        }), (x) => setMsg({ ok: true, text: t('sm.ro.ordered', { list: (x as { orders: { number: number }[] }).orders.map((o) => `#${o.number}`).join(', ') }) }))}>{t('sm.ro.purchase')}</button>}
      </h3>
      <Flash m={msg} />
      <div style={{ overflowX: 'auto' }}>
        <table className="mx-table">
          <thead><tr><th /><th>{t('sm.ro.item')}</th><th>{t('sm.ro.warehouse')}</th><th className="num">{t('sm.ro.onHand')}</th><th className="num">{t('sm.ro.onOrder')}</th>
            <th className="num">{t('sm.ro.weekly')}</th><th className="num">{t('sm.ro.daysLeft')}</th><th className="num">{t('sm.ro.lead')}</th><th className="num">{t('sm.ro.safety')}</th>
            <th className="num">{t('sm.ro.min')}</th><th className="num">{t('sm.ro.max')}</th><th className="num">{t('sm.ro.rule')}</th><th className="num">{t('sm.ro.toOrder')}</th><th>{t('sm.ro.supplier')}</th><th>{t('sm.ro.status')}</th></tr></thead>
          <tbody>
            {lines.map((l) => (
              <tr key={key(l)}>
                <td><input type="checkbox" aria-label={l.name} checked={!!sel[key(l)]} onChange={(e) => setSel({ ...sel, [key(l)]: e.currentTarget.checked })} /></td>
                <td><b>{l.name}</b> <small dir="ltr">{l.code}</small>{l.seasonal_factor != null && <small title={t('sm.ro.seasonal')}> · ×{l.seasonal_factor}</small>}</td>
                <td>{l.warehouse_name}</td>
                <td className="num">{qty(l.on_hand)} {l.unit}</td><td className="num">{l.on_order ? qty(l.on_order) : ''}</td>
                <td className="num">{qty(l.weekly_demand)}</td>
                <td className="num" style={{ color: l.days_left != null && l.days_left < l.lead_days ? 'var(--mx-red)' : undefined }}>{l.days_left == null ? '' : Math.floor(l.days_left)}</td>
                <td className="num" title={t(`sm.ro.ls.${l.lead_source}`)}>{l.lead_days}{l.lead_source === 'default' ? '*' : ''}</td>
                <td className="num">{qty(l.safety_stock)}</td><td className="num"><b>{qty(l.suggest_min)}</b></td><td className="num"><b>{qty(l.suggest_max)}</b></td>
                <td className="num">{l.rule_min == null ? '—' : `${qty(l.rule_min)}–${qty(l.rule_max)}`}</td>
                <td className="num"><input dir="ltr" style={{ width: 70, textAlign: 'end' }} value={qtyOf[key(l)] ?? (l.to_order ? String(l.to_order) : '')}
                  onChange={(e) => setQtyOf({ ...qtyOf, [key(l)]: e.currentTarget.value.replace(/[^\d.]/g, '') })} /></td>
                <td>{l.supplier_name ?? <small style={{ color: 'var(--mx-amber)' }}>{t('sm.ro.noSupplier')}</small>}{l.unit_price != null && <small dir="ltr"> · {amt(l.unit_price, 3)} {l.currency}</small>}</td>
                <td><span className={`mx-pill ${stPill[l.status]}`}>{t(`sm.ro.st.${l.status}`)}</span></td>
              </tr>))}
            {d && lines.length === 0 && <tr><td colSpan={15} className="mx-empty">{t('sm.ro.none')}</td></tr>}
          </tbody>
        </table>
      </div>
      <p style={{ padding: 6, margin: 0 }}><small>{t('sm.ro.how')}</small></p>
    </section>
  );
}

// ======================= bills read from a photo =======================

interface DraftLine { item_id?: string; item_name?: string; description: string; quantity: number; unit_price: number }
interface Draft { supplier_id?: string; supplier_name?: string; supplier_name_read: string; currency?: string; bill_date?: string; due_date?: string; supplier_ref: string;
  total_read: number; tax_read: number; notes: string; duplicate_bill?: number; lines: DraftLine[] }
interface ScanRow { id: string; filename: string; mime: string; size: number; status: string; error: string; draft: Draft | null; bill_id: string | null; bill_number: number | null; created_at: string; created_by_name: string | null }

function fileToDataURL(f: File): Promise<string> {
  return new Promise((res, rej) => { const r = new FileReader(); r.onload = () => res(String(r.result)); r.onerror = () => rej(r.error); r.readAsDataURL(f); });
}

function Scans({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [st] = useAIStatus();
  const [rows, reload] = useLoad<ScanRow[]>(base && `${base}/smart/scans`, []);
  const [sel, setSel] = useState<string | null>(null);
  const [msg, run, setMsg] = useRun(reload);
  const [billFor, setBillFor] = useState<ScanRow | null>(null);
  const cur = rows.find((r) => r.id === sel) ?? null;
  const create = can(me, 'purchasing.create');
  // keep the list fresh while a bill is being read
  useEffect(() => {
    if (!rows.some((r) => r.status === 'reading')) return;
    const id = window.setTimeout(reload, 2000);
    return () => window.clearTimeout(id);
  }, [rows, reload]);
  async function upload(files: FileList | null) {
    if (!files || !base) return;
    for (const f of Array.from(files)) {
      if (f.size > 10 * 1024 * 1024) { setMsg({ ok: false, text: t('sm.sc.tooBig') }); continue; }
      const data = await fileToDataURL(f);
      await run(api<ScanRow>(`${base}/smart/scans`, { method: 'POST', body: { filename: f.name, data, read: !!st?.enabled } }), (x) => setSel((x as ScanRow).id));
    }
  }
  return (
    <div className="mx-row" style={{ gap: 8, alignItems: 'flex-start', flexWrap: 'wrap' }}>
      <section className="mx-box" style={{ flex: '1 1 420px', minWidth: 0 }}>
        <h3 style={{ display: 'flex', gap: 8, alignItems: 'center' }}>{t('sm.m.scans')}<span className="mx-sp" />
          {create && <label className="mx-btn main sm" style={{ cursor: 'pointer' }}>{t('sm.sc.upload')}
            <input type="file" accept="image/*,application/pdf" capture="environment" multiple style={{ display: 'none' }} onChange={(e) => { void upload(e.currentTarget.files); e.currentTarget.value = ''; }} /></label>}</h3>
        <AINotOn st={st} />
        <Flash m={msg} />
        <table className="mx-table">
          <thead><tr><th>{t('sm.sc.file')}</th><th>{t('sm.sc.supplier')}</th><th className="num">{t('sm.sc.total')}</th><th>{t('sm.sc.status')}</th><th>{t('sm.sc.date')}</th></tr></thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.id} className={sel === r.id ? 'sel' : ''} style={{ cursor: 'pointer' }} onClick={() => setSel(r.id)}>
                <td>{r.filename}</td><td>{r.draft?.supplier_name ?? r.draft?.supplier_name_read ?? ''}</td>
                <td className="num">{r.draft ? `${amt(r.draft.total_read)} ${r.draft.currency ?? ''}` : ''}</td>
                <td>{t(`sm.sc.st.${r.status}`)}{r.bill_number != null && ` #${r.bill_number}`}{r.error && <small style={{ color: 'var(--mx-red)' }}> · {t(`errors.${r.error}`, { defaultValue: r.error })}</small>}</td>
                <td>{formatDate(r.created_at)}</td>
              </tr>))}
            {rows.length === 0 && <tr><td colSpan={5} className="mx-empty">{t('sm.sc.none')}</td></tr>}
          </tbody>
        </table>
      </section>
      {cur && (
        <section className="mx-box" style={{ flex: '1 1 460px', minWidth: 0 }}>
          <h3 style={{ display: 'flex', gap: 6, alignItems: 'center', flexWrap: 'wrap' }}>{cur.filename}<span className="mx-sp" />
            {create && cur.status !== 'billed' && cur.status !== 'reading' && st?.enabled &&
              <button className="mx-btn sm" onClick={() => void run(api(`${base}/smart/scans/${cur.id}/read`, { method: 'POST', body: {} }))}>{cur.status === 'new' ? t('sm.sc.read') : t('sm.sc.readAgain')}</button>}
            {create && cur.status === 'read' && <button className="mx-btn main sm" onClick={() => setBillFor(cur)}>{t('sm.sc.makeBill')}</button>}
            {create && cur.status !== 'billed' && cur.status !== 'reading' &&
              <button className="mx-btn sm" onClick={() => void run(api(`${base}/smart/scans/${cur.id}`, { method: 'DELETE' }), () => setSel(null))}>{t('sm.sc.delete')}</button>}
          </h3>
          <div className="mx-row" style={{ gap: 8, padding: 6, alignItems: 'flex-start', flexWrap: 'wrap' }}>
            <div style={{ flex: '1 1 220px', maxWidth: 320 }}>
              {cur.mime === 'application/pdf'
                ? <a className="mx-link" href={`/api/v1${base}/smart/scans/${cur.id}/file`} target="_blank" rel="noreferrer">{t('sm.sc.openPdf')}</a>
                : <img alt={cur.filename} src={`/api/v1${base}/smart/scans/${cur.id}/file`} style={{ maxWidth: '100%', border: '1px solid var(--mx-line)', borderRadius: 6 }} />}
            </div>
            <div style={{ flex: '2 1 260px', minWidth: 0 }}>
              {cur.status === 'reading' && <div className="mx-empty">{t('sm.sc.reading')}</div>}
              {cur.draft && (
                <>
                  {cur.draft.duplicate_bill != null && <div className="mx-flash bad">{t('sm.sc.duplicate', { n: cur.draft.duplicate_bill })}</div>}
                  <table className="mx-table"><tbody>
                    <tr><th>{t('sm.sc.supplier')}</th><td>{cur.draft.supplier_name ?? <span style={{ color: 'var(--mx-amber)' }}>{t('sm.sc.newSupplier', { name: cur.draft.supplier_name_read })}</span>}</td></tr>
                    <tr><th>{t('sm.sc.ref')}</th><td dir="ltr">{cur.draft.supplier_ref}</td></tr>
                    <tr><th>{t('sm.sc.date')}</th><td>{cur.draft.bill_date ? formatDate(cur.draft.bill_date) : '—'}</td></tr>
                    <tr><th>{t('sm.sc.total')}</th><td dir="ltr">{amt(cur.draft.total_read)} {cur.draft.currency ?? '?'}{cur.draft.tax_read ? ` (${t('sm.sc.tax')} ${amt(cur.draft.tax_read)})` : ''}</td></tr>
                  </tbody></table>
                  <table className="mx-table" style={{ marginTop: 6 }}>
                    <thead><tr><th>{t('sm.sc.line')}</th><th>{t('sm.sc.item')}</th><th className="num">{t('sm.sc.qty')}</th><th className="num">{t('sm.sc.price')}</th></tr></thead>
                    <tbody>{cur.draft.lines.map((l, i) => <tr key={i}><td>{l.description}</td><td>{l.item_name ?? <small>{t('sm.sc.noItem')}</small>}</td><td className="num">{qty(l.quantity)}</td><td className="num">{amt(l.unit_price, 3)}</td></tr>)}</tbody>
                  </table>
                  {cur.draft.notes && <p><small>{cur.draft.notes}</small></p>}
                  <p style={{ margin: 0 }}><small>{t('sm.sc.check')}</small></p>
                </>)}
            </div>
          </div>
        </section>)}
      {billFor && billFor.draft && (
        <Suspense fallback={null}>
          <NewBill prefill={{
            supplier_id: billFor.draft.supplier_id, currency: billFor.draft.currency, bill_date: billFor.draft.bill_date, due_date: billFor.draft.due_date,
            supplier_ref: billFor.draft.supplier_ref, lines: billFor.draft.lines.map((l) => ({ item_id: l.item_id, description: l.description, quantity: String(l.quantity), unit_price: String(l.unit_price) })),
          }} onClose={() => setBillFor(null)} onDone={(billId) => {
            const scan = billFor;
            setBillFor(null);
            void run(api(`${base}/smart/scans/${scan.id}/link`, { method: 'POST', body: { bill_id: billId } }));
          }} />
        </Suspense>)}
    </div>
  );
}

// ======================= settings =======================

interface SmartSettings {
  params: Record<string, number>; defaults: Record<string, number>; kinds: string[]; all_kinds: string[]; digest_enabled: boolean; digest_hour: number; digest_phone: string;
  last_scan_at: string | null; last_risk_at: string | null; last_digest_on: string | null;
}
const PARAMS = ['z', 'min_history', 'diesel_min_pct', 'discount_min_pct', 'cash_limit', 'void_min', 'work_start', 'work_end', 'count_loss_limit', 'collector_drop_pct',
  'lead_days', 'review_days', 'service_level'];

function Settings({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [s, reload] = useLoad<SmartSettings | null>(base && `${base}/smart/settings`, null);
  const [f, setF] = useState<Record<string, string>>({});
  const [kinds, setKinds] = useState<string[] | null>(null);
  const [dg, setDg] = useState<{ digest_enabled?: boolean; digest_hour?: number; digest_phone?: string }>({});
  const [msg, run] = useRun(() => { reload(); setF({}); setKinds(null); setDg({}); });
  const manage = can(me, 'smart.manage');
  if (!s) return null;
  const k = kinds ?? s.kinds;
  return (
    <div className="mx-row" style={{ gap: 8, alignItems: 'flex-start', flexWrap: 'wrap' }}>
      <section className="mx-box" style={{ flex: '1 1 420px' }}>
        <h3>{t('sm.st.thresholds')}</h3>
        <table className="mx-table"><tbody>
          {PARAMS.map((p) => (
            <tr key={p}><td>{t(`sm.st.p.${p}`)}</td>
              <td><input dir="ltr" style={{ width: 90 }} disabled={!manage} value={f[p] ?? String(s.params[p])} onChange={(e) => setF({ ...f, [p]: e.currentTarget.value.replace(/[^\d.]/g, '') })} /></td>
              <td><small>{t('sm.st.default', { v: s.defaults[p] })}</small></td></tr>))}
        </tbody></table>
      </section>
      <section className="mx-box" style={{ flex: '1 1 320px' }}>
        <h3>{t('sm.st.checks')}</h3>
        <div style={{ padding: 6, display: 'flex', flexDirection: 'column', gap: 4 }}>
          {s.all_kinds.map((x) => <label key={x} className="mx-check"><input type="checkbox" disabled={!manage} checked={k.includes(x)}
            onChange={(e) => setKinds(e.currentTarget.checked ? [...k, x] : k.filter((y) => y !== x))} /> {t(`sm.al.k.${x}`)}</label>)}
        </div>
        <h3>{t('sm.st.digest')}</h3>
        <div style={{ padding: 6, display: 'flex', flexDirection: 'column', gap: 6 }}>
          <label className="mx-check"><input type="checkbox" disabled={!manage} checked={dg.digest_enabled ?? s.digest_enabled} onChange={(e) => setDg({ ...dg, digest_enabled: e.currentTarget.checked })} /> {t('sm.st.digestOn')}</label>
          <label className="mx-fld">{t('sm.st.digestHour')}<select disabled={!manage} value={dg.digest_hour ?? s.digest_hour} onChange={(e) => setDg({ ...dg, digest_hour: Number(e.currentTarget.value) })}>
            {Array.from({ length: 24 }, (_, h) => <option key={h} value={h}>{String(h).padStart(2, '0')}:00</option>)}</select></label>
          <label className="mx-fld">{t('sm.st.digestPhone')}<input dir="ltr" disabled={!manage} value={dg.digest_phone ?? s.digest_phone} onChange={(e) => setDg({ ...dg, digest_phone: e.currentTarget.value })} /></label>
          <small>{t('sm.st.digestHelp')}</small>
          <small>{t('sm.st.last', { scan: s.last_scan_at ? formatDateTime(s.last_scan_at) : '—', risk: s.last_risk_at ? formatDateTime(s.last_risk_at) : '—', digest: s.last_digest_on ? formatDate(s.last_digest_on) : '—' })}</small>
        </div>
        {manage && <div style={{ padding: 6 }}><button className="mx-btn main" onClick={() => {
          const params: Record<string, number> = {};
          for (const [p, v] of Object.entries(f)) if (v !== '') params[p] = Number(v);
          void run(api(`${base}/smart/settings`, { method: 'PUT', body: { params, ...(kinds ? { kinds } : {}), ...dg } }));
        }}>{t('sm.st.save')}</button></div>}
        <Flash m={msg} />
      </section>
    </div>
  );
}

// ======================= platform owner =======================

export function PlatformAI() {
  const { t, i18n } = useTranslation();
  const [d] = useLoad<{ enabled: boolean; model: string; default_model: string; usage: { tenant_id: string; tenant_name: string; plan: string; requests: number; bills_read: number; errors: number; input_tokens: number; output_tokens: number; ai_monthly_limit: number | null }[];
    plans: { code: string; name_en: string; name_ar: string; ai_monthly_limit: number | null; has_app: boolean }[] } | null>('/platform/ai', null);
  if (!d) return null;
  return (
    <div className="mx-row" style={{ gap: 8, padding: 8, alignItems: 'flex-start', flexWrap: 'wrap' }}>
      <section className="mx-box" style={{ flex: '1 1 380px' }}>
        <h3>{t('sm.pf.title')}</h3>
        <div style={{ padding: 8 }}>
          {d.enabled
            ? <div className="mx-flash good">{t('sm.pf.on', { model: d.model })}</div>
            : <div className="mx-flash bad">{t('sm.pf.off')}</div>}
          <ol style={{ lineHeight: 1.9, paddingInlineStart: 20 }}>
            {['1', '2', '3', '4', '5'].map((n) => <li key={n}>{t(`sm.pf.step${n}`, { model: d.default_model })}</li>)}
          </ol>
          <small>{t('sm.pf.cost')}</small>
        </div>
        <h3>{t('sm.pf.plans')}</h3>
        <table className="mx-table"><thead><tr><th>{t('pc.f.code')}</th><th>{t('pc.f.name')}</th><th className="num">{t('sm.pf.limit')}</th><th>{t('sm.pf.app')}</th></tr></thead>
          <tbody>{d.plans.map((p) => <tr key={p.code}><td dir="ltr">{p.code}</td><td>{i18n.language === 'ar' && p.name_ar ? p.name_ar : p.name_en}</td><td className="num">{p.ai_monthly_limit ?? '∞'}</td><td>{p.has_app ? '✓' : '—'}</td></tr>)}</tbody>
        </table>
        <p style={{ padding: 6, margin: 0 }}><small>{t('sm.pf.plansHelp')}</small></p>
      </section>
      <section className="mx-box" style={{ flex: '2 1 480px', minWidth: 0 }}>
        <h3>{t('sm.pf.usage')}</h3>
        <div style={{ overflowX: 'auto' }}>
          <table className="mx-table"><thead><tr><th>{t('sm.pf.org')}</th><th>{t('sm.pf.plan')}</th><th className="num">{t('sm.pf.requests')}</th><th className="num">{t('sm.pf.bills')}</th>
            <th className="num">{t('sm.pf.errors')}</th><th className="num">{t('sm.pf.tokensIn')}</th><th className="num">{t('sm.pf.tokensOut')}</th></tr></thead>
            <tbody>
              {d.usage.map((u) => <tr key={u.tenant_id}><td>{u.tenant_name}</td><td dir="ltr">{u.plan}</td><td className="num">{u.requests}{u.ai_monthly_limit != null ? ` / ${u.ai_monthly_limit}` : ''}</td>
                <td className="num">{u.bills_read}</td><td className="num">{u.errors}</td><td className="num">{formatAmount(String(u.input_tokens), 0)}</td><td className="num">{formatAmount(String(u.output_tokens), 0)}</td></tr>)}
              {d.usage.length === 0 && <tr><td colSpan={7} className="mx-empty">{t('sm.pf.noUse')}</td></tr>}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  );
}
