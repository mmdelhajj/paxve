import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { api } from '../../api';
import DataGrid, { type Col } from '../../components/DataGrid';
import { errorText, formatAmount, formatDate, formatDateTime, today } from '../../util';
import { Help } from '../acc/common';

type Msg = { ok: boolean; text: string } | null;
function Flash({ m }: { m: Msg }) {
  return m ? <div className={`mx-flash ${m.ok ? 'good' : 'bad'}`}>{m.text}</div> : null;
}
function useLoad<T>(path: string | null, init: T): [T, () => void] {
  const [v, setV] = useState<T>(init);
  const load = useCallback(() => {
    if (path) api<T>(path).then(setV).catch(() => setV(init));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [path]);
  useEffect(load, [load]);
  return [v, load];
}

export const STATES = ['trial', 'active', 'grace', 'expired', 'blocked'] as const;
const METHODS = ['cash', 'whish', 'omt', 'bank', 'card', 'other'];
const MODES = ['dash', 'orgs', 'requests', 'payments', 'plans'] as const;
type Mode = typeof MODES[number];

export interface Org {
  id: string; name: string; slug: string; plan: string; state: string; trial_ends_at: string | null; paid_until: string | null; ends_on: string | null;
  billing_cycle: string; price: string | null; currency: string; price_override: string | null; price_currency: string | null; grace_days: number;
  max_subscribers: number | null; max_users: number | null; max_companies: number | null; is_active: boolean; blocked_at: string | null; blocked_reason: string;
  notes: string; contact_phone: string; contact_email: string; created_at: string; users: number; companies: number; active_subscriptions: number;
  admin_email: string | null; last_activity_at: string | null; country: string | null; business_type: string | null; open_requests: number;
  grandfathered_apps: string[]; plan_apps: string[] | null;
}
interface Setup {
  owner_company: string | null; sales_phone: string; sales_email: string;
  providers: { id: string; kind: string; name: string; is_default: boolean; is_active: boolean; company: string }[];
}
interface Plan {
  code: string; name_en: string; name_ar: string; name_fr: string; price_monthly: string; price_yearly: string; currency: string;
  max_subscribers: number | null; max_users: number | null; max_companies: number | null; apps: string[] | null; is_public: boolean; is_active: boolean;
  sort: number; organisations: number;
}
interface Summary {
  organisations: number; trial: number; active: number; grace: number; expired: number; blocked: number; mrr: string; arr: string; signups_month: number;
  expiring_7d: number; churn_30d: number; open_requests: number; revenue: { month: string; currency: string; amount: string; payments: number }[];
}
interface Payment {
  id: string; tenant_id?: string; tenant_name?: string; amount: string; currency: string; method: string; reference: string; plan: string; cycle: string;
  period_from: string; period_to: string; note: string; recorded_by_email: string; created_at: string;
}
interface Req {
  id: string; tenant_id: string; tenant_name: string; kind: string; plan: string; cycle: string; method: string; reference: string; note: string; status: string;
  created_at: string; handled_at: string | null; handled_by: string; answer: string; created_by_name: string | null; created_by_email: string | null;
  state: string; paid_until: string | null;
}

export function planName(p: { code: string; name_en: string; name_ar?: string; name_fr?: string } | undefined, lang: string, code = '') {
  if (!p) return code;
  if (lang === 'ar' && p.name_ar) return p.name_ar;
  if (lang === 'fr' && p.name_fr) return p.name_fr;
  return p.name_en;
}
export function StatePill({ s }: { s: string }) {
  const { t } = useTranslation();
  return <span className={`mx-pill st-${s}`}>{t(`pc.st.${s}`)}</span>;
}
function daysLeft(d: string | null) {
  if (!d) return null;
  return Math.round((new Date(d.slice(0, 10) + 'T00:00:00Z').getTime() - new Date(today() + 'T00:00:00Z').getTime()) / 86400000);
}
function money(v: string | number | null | undefined, cur: string) {
  return v === null || v === undefined || v === '' ? '—' : `${formatAmount(v, 2)} ${cur}`;
}

/** The Paxve owner's back-office: every organisation, its plan, renewals, payments, plans and requests. */
export default function PlatformConsole({ initial }: { initial?: string }) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<Mode>((MODES as readonly string[]).includes(initial ?? '') ? initial as Mode : 'dash');
  const [orgs, reloadOrgs] = useLoad<Org[]>('/platform/tenants', []);
  const [plans, reloadPlans] = useLoad<Plan[]>('/platform/plans', []);
  const [focus, setFocus] = useState<{ id: string; filter?: string } | null>(null);
  const openReq = orgs.reduce((s, o) => s + Number(o.open_requests || 0), 0);
  const go = (m: Mode, f?: { id: string; filter?: string }) => { setFocus(f ?? null); setMode(m); };
  return (
    <div className="mx-col">
      <div className="mx-strip">
        <b>{t('pc.title')}</b>
        <span className="mx-pill">{t('pc.sum.orgs', { n: orgs.length })}</span>
        {openReq > 0 && <button className="mx-pill warn" style={{ border: 0, cursor: 'pointer' }} onClick={() => go('requests')}>{t('pc.sum.requests', { n: openReq })}</button>}
        <span className="mx-sp" />
        <button className="mx-btn sm" onClick={() => { reloadOrgs(); reloadPlans(); }}>{t('pc.refresh')}</button>
        <Help topic={`pc.help.${mode}`} />
      </div>
      <div className="mx-modes" role="tablist">
        {MODES.map((m) => <button key={m} role="tab" className="mx-mode" aria-selected={mode === m} onClick={() => go(m)}>{t(`pc.m.${m}`)}</button>)}
      </div>
      {mode === 'dash' && <Dash orgs={orgs} go={go} />}
      {mode === 'orgs' && <Orgs orgs={orgs} plans={plans} reload={reloadOrgs} focus={focus} />}
      {mode === 'requests' && <Requests plans={plans} reloadOrgs={reloadOrgs} />}
      {mode === 'payments' && <Payments />}
      {mode === 'plans' && <Plans plans={plans} reload={reloadPlans} />}
    </div>
  );
}

// ---------- dashboard ----------

function Dash({ orgs, go }: { orgs: Org[]; go: (m: Mode, f?: { id: string; filter?: string }) => void }) {
  const { t } = useTranslation();
  const [s] = useLoad<Summary | null>('/platform/summary', null);
  const expiring = useMemo(() => orgs.filter((o) => {
    if (o.state === 'grace' || o.state === 'expired') return true;
    const d = daysLeft(o.ends_on);
    return (o.state === 'trial' || o.state === 'active') && d !== null && d <= 14;
  }).sort((a, b) => (a.ends_on ?? '').localeCompare(b.ends_on ?? '')), [orgs]);
  const cols: Col<Org>[] = [
    { key: 'name', title: t('pc.f.org'), get: (r) => r.name, width: 200 },
    { key: 'state', title: t('pc.f.state'), get: (r) => t(`pc.st.${r.state}`), render: (r) => <StatePill s={r.state} />, width: 100 },
    { key: 'plan', title: t('pc.f.plan'), get: (r) => r.plan, width: 90 },
    { key: 'ends', title: t('pc.f.ends'), get: (r) => r.ends_on ?? '', render: (r) => (r.ends_on ? formatDate(r.ends_on) : '—'), width: 100 },
    { key: 'left', title: t('pc.f.daysLeft'), get: (r) => daysLeft(r.ends_on) ?? '', num: true, decimals: 0, width: 70, cls: (r) => ((daysLeft(r.ends_on) ?? 99) < 0 ? 'red' : '') },
    { key: 'phone', title: t('pc.f.phone'), get: (r) => r.contact_phone, width: 120 },
    { key: 'email', title: t('pc.f.email'), get: (r) => r.contact_email || r.admin_email || '', width: 180 },
  ];
  const kpi = (label: string, v: string | number, cls = '', onClick?: () => void) => (onClick
    ? <button className={`mx-kpi ${cls}`} onClick={onClick}><span>{label}</span><b dir="ltr">{v}</b></button>
    : <div className={`mx-kpi ${cls}`}><span>{label}</span><b dir="ltr">{v}</b></div>);
  return (
    <div className="mx-body" style={{ gridTemplateColumns: 'minmax(0, 1fr)', gridTemplateRows: 'auto auto minmax(0, 1fr)', overflow: 'auto' }}>
      <section className="mx-box">
        <h3>{t('pc.m.dash')}</h3>
        {s ? (
          <>
            <div className="mx-kpis">
              {kpi(t('pc.k.orgs'), s.organisations, '', () => go('orgs'))}
              {kpi(t('pc.st.trial'), s.trial, '', () => go('orgs', { id: '', filter: 'trial' }))}
              {kpi(t('pc.st.active'), s.active, 'ok', () => go('orgs', { id: '', filter: 'active' }))}
              {kpi(t('pc.st.grace'), s.grace, s.grace ? 'warn' : '', () => go('orgs', { id: '', filter: 'grace' }))}
              {kpi(t('pc.st.expired'), s.expired, s.expired ? 'red' : '', () => go('orgs', { id: '', filter: 'expired' }))}
              {kpi(t('pc.st.blocked'), s.blocked, '', () => go('orgs', { id: '', filter: 'blocked' }))}
              {kpi(t('pc.k.mrr'), `$${formatAmount(s.mrr, 0)}`, 'ok')}
              {kpi(t('pc.k.arr'), `$${formatAmount(s.arr, 0)}`)}
              {kpi(t('pc.k.signups'), s.signups_month)}
              {kpi(t('pc.k.expiring'), s.expiring_7d, s.expiring_7d ? 'warn' : '', () => go('orgs', { id: '', filter: 'expiring' }))}
              {kpi(t('pc.k.churn'), s.churn_30d, s.churn_30d ? 'red' : '')}
              {kpi(t('pc.k.requests'), s.open_requests, s.open_requests ? 'warn' : '', () => go('requests'))}
            </div>
            <div style={{ padding: '0 10px 10px' }}>
              <h4 style={{ margin: '4px 0' }}>{t('pc.k.revenue')}</h4>
              {s.revenue.length === 0 ? <small>{t('pc.none')}</small> : (
                <table className="mx-mini" style={{ maxWidth: 520 }}><tbody>
                  {s.revenue.map((m) => (
                    <tr key={m.month + m.currency}><td dir="ltr">{m.month}</td><td className="num" dir="ltr">{money(m.amount, m.currency)}</td>
                      <td className="num">{t('pc.k.payments', { n: m.payments })}</td></tr>
                  ))}
                </tbody></table>
              )}
            </div>
          </>
        ) : <p style={{ padding: 10 }}>…</p>}
      </section>
      <SetupBox />
      <section className="mx-box" style={{ minHeight: 240 }}>
        <h3>{t('pc.expiringList')} <small>{t('pc.expiringHint')}</small></h3>
        <DataGrid rows={expiring} cols={cols} rowKey={(r) => r.id} title={t('pc.expiringList')} onActivate={(r) => go('orgs', { id: r.id })}
          onSelect={(r) => go('orgs', { id: r.id })} empty={t('pc.none')} />
      </section>
    </div>
  );
}

/** What the platform itself relies on: the owner company's messaging (renewal reminders) and the payment contact. */
function SetupBox() {
  const { t } = useTranslation();
  const [s] = useLoad<Setup | null>('/platform/setup', null);
  if (!s) return null;
  const active = s.providers.filter((p) => p.is_active);
  const real = active.filter((p) => p.kind !== 'log');
  const used = real.find((p) => p.is_default) ?? real[0];
  return (
    <section className="mx-box">
      <h3>{t('pc.setup.title')}</h3>
      <div style={{ padding: '4px 10px 10px', display: 'flex', flexDirection: 'column', gap: 6 }}>
        <b>{t('pc.setup.messaging', { company: s.owner_company ?? '—' })}</b>
        {s.providers.length > 0 && (
          <table className="mx-mini" style={{ maxWidth: 520 }}><tbody>
            {s.providers.map((p) => (
              <tr key={p.id} className={p.is_active ? '' : 'muted'}>
                <td>{p.name}</td><td dir="ltr">{p.kind}</td>
                <td>{p.is_default ? t('pc.setup.def') : ''}{!p.is_active ? ` ${t('pc.setup.off')}` : ''}</td>
              </tr>
            ))}
          </tbody></table>
        )}
        {active.length === 0 ? <div className="mx-flash bad">{t('pc.setup.none')}</div>
          : real.length === 0 ? <div className="mx-flash bad">{t('pc.setup.logOnly')}</div>
            : <div className="mx-flash good">{t('pc.setup.ok', { name: used?.name ?? '' })}</div>}
        <b>{t('pc.setup.sales')}</b>
        {s.sales_phone || s.sales_email
          ? <span dir="ltr">{[s.sales_phone, s.sales_email].filter(Boolean).join(' · ')}</span>
          : <div className="mx-flash bad">{t('pc.setup.salesMissing')}</div>}
      </div>
    </section>
  );
}

// ---------- organisations ----------

function Orgs({ orgs, plans, reload, focus }: { orgs: Org[]; plans: Plan[]; reload: () => void; focus: { id: string; filter?: string } | null }) {
  const { t, i18n } = useTranslation();
  const [state, setState] = useState(focus?.filter ?? 'all');
  const [plan, setPlan] = useState('all');
  const [country, setCountry] = useState('all');
  const [q, setQ] = useState('');
  const [showOff, setShowOff] = useState(false); // deactivated organisations (old tests…) are hidden unless asked (audit B-L6)
  const [sel, setSel] = useState<string | null>(focus?.id || null);
  const countries = useMemo(() => [...new Set(orgs.map((o) => o.country || '').filter(Boolean))].sort(), [orgs]);
  const hiddenOff = useMemo(() => orgs.filter((o) => !o.is_active).length, [orgs]);
  const rows = useMemo(() => orgs.filter((o) => {
    if (!showOff && !o.is_active && state !== 'blocked' && o.id !== focus?.id) return false;
    if (state === 'expiring') {
      const d = daysLeft(o.ends_on);
      if (!((o.state === 'trial' || o.state === 'active') && d !== null && d >= 0 && d <= 7)) return false;
    } else if (state !== 'all' && o.state !== state) return false;
    if (plan !== 'all' && o.plan !== plan) return false;
    if (country !== 'all' && (o.country || '') !== country) return false;
    const s = q.trim().toLowerCase();
    return !s || [o.name, o.slug, o.admin_email, o.contact_email, o.contact_phone].some((x) => (x ?? '').toLowerCase().includes(s));
  }), [orgs, state, plan, country, q, showOff, focus]);
  const planOf = (c: string) => planName(plans.find((p) => p.code === c), i18n.language, c);
  const cols: Col<Org>[] = [
    { key: 'name', title: t('pc.f.org'), get: (r) => r.name, width: 190, cls: (r) => (r.open_requests > 0 ? 'red' : '') },
    { key: 'state', title: t('pc.f.state'), get: (r) => t(`pc.st.${r.state}`), render: (r) => <StatePill s={r.state} />, width: 96 },
    { key: 'plan', title: t('pc.f.plan'), get: (r) => planOf(r.plan), width: 90 },
    { key: 'ends', title: t('pc.f.ends'), get: (r) => r.ends_on ?? '', render: (r) => (r.ends_on ? formatDate(r.ends_on) : '∞'), width: 96 },
    { key: 'left', title: t('pc.f.daysLeft'), get: (r) => daysLeft(r.ends_on) ?? '', num: true, decimals: 0, width: 64, cls: (r) => ((daysLeft(r.ends_on) ?? 99) < 0 ? 'red' : '') },
    { key: 'price', title: t('pc.f.price'), get: (r) => Number(r.price ?? 0), render: (r) => money(r.price, r.currency), num: true, width: 90 },
    { key: 'cycle', title: t('pc.f.cycle'), get: (r) => t(`pc.cycle.${r.billing_cycle}`), width: 70 },
    { key: 'subs', title: t('pc.f.subs'), get: (r) => Number(r.active_subscriptions), num: true, decimals: 0, width: 70, total: 'sum' },
    { key: 'users', title: t('pc.f.users'), get: (r) => Number(r.users), num: true, decimals: 0, width: 60 },
    { key: 'country', title: t('pc.f.country'), get: (r) => r.country ?? '', width: 60 },
    { key: 'biz', title: t('pc.f.biz'), get: (r) => (r.business_type ? t(`pc.biz.${r.business_type}`, { defaultValue: r.business_type }) : ''), width: 90 },
    { key: 'email', title: t('pc.f.email'), get: (r) => r.admin_email ?? '', width: 170 },
    { key: 'last', title: t('pc.f.lastActivity'), get: (r) => r.last_activity_at ?? '', render: (r) => (r.last_activity_at ? formatDateTime(r.last_activity_at) : '—'), width: 130 },
    { key: 'created', title: t('pc.f.created'), get: (r) => r.created_at, render: (r) => formatDate(r.created_at), width: 96 },
  ];
  const cur = orgs.find((o) => o.id === sel) ?? null;
  return (
    <div className="mx-body" style={{ gridTemplateColumns: 'minmax(0, 1fr) minmax(320px, 470px)' }}>
      <section className="mx-box"><h3>{t('pc.m.orgs')} <small>{rows.length}</small></h3>
        <div className="mx-row" style={{ padding: 6, gap: 6, flexWrap: 'wrap' }}>
          <select value={state} onChange={(e) => setState(e.currentTarget.value)} aria-label={t('pc.f.state')}>
            <option value="all">{t('pc.all')}</option>
            {STATES.map((s) => <option key={s} value={s}>{t(`pc.st.${s}`)}</option>)}
            <option value="expiring">{t('pc.k.expiring')}</option>
          </select>
          <select value={plan} onChange={(e) => setPlan(e.currentTarget.value)} aria-label={t('pc.f.plan')}>
            <option value="all">{t('pc.allPlans')}</option>
            {plans.map((p) => <option key={p.code} value={p.code}>{planName(p, i18n.language)}</option>)}
          </select>
          <select value={country} onChange={(e) => setCountry(e.currentTarget.value)} aria-label={t('pc.f.country')}>
            <option value="all">{t('pc.allCountries')}</option>
            {countries.map((c) => <option key={c} value={c}>{c}</option>)}
          </select>
          <input placeholder={t('pc.search')} value={q} onChange={(e) => setQ(e.currentTarget.value)} />
          {hiddenOff > 0 && <label className="mx-row" style={{ gap: 4 }}><input type="checkbox" checked={showOff} onChange={(e) => setShowOff(e.currentTarget.checked)} />{t('pc.showOff', { n: hiddenOff })}</label>}
        </div>
        <DataGrid rows={rows} cols={cols} rowKey={(r) => r.id} selected={sel} title={t('pc.m.orgs')} onSelect={(r) => setSel(r.id)} onActivate={(r) => setSel(r.id)}
          rowClass={(r) => (r.state === 'blocked' ? 'muted' : '')} />
      </section>
      <section className="mx-box">
        {cur ? <OrgDetail key={cur.id} org={cur} plans={plans} reload={reload} /> : <p style={{ padding: 10 }}>{t('pc.pick')}</p>}
      </section>
    </div>
  );
}

interface Detail {
  tenant: Org;
  users: { id: string; email: string; full_name: string; role: string; role_name: string; is_active: boolean; created_at: string; last_login: string | null; last_seen: string | null }[];
  companies: { id: string; name_en: string; name_ar: string; country: string; business_type: string; apps: string[]; base_currency: string; local_currency: string; is_active: boolean; created_at: string }[];
  usage: { customers: number; active_subscriptions: number; invoices_month: number; payments_month: number; journal_entries: number; storage_bytes: number; active_users: number; active_companies: number };
  payments: Payment[];
  events: { id: number; at: string; actor_email: string; action: string; detail: Record<string, unknown>; ip: string | null }[];
  requests: Req[];
}

const TABS = ['info', 'renew', 'users', 'companies', 'payments', 'events', 'settings'] as const;

function OrgDetail({ org, plans, reload, request }: { org: Org; plans: Plan[]; reload: () => void; request?: Req }) {
  const { t, i18n } = useTranslation();
  const [tab, setTab] = useState<typeof TABS[number]>(request ? 'renew' : 'info');
  const [d, setD] = useState<Detail | null>(null);
  const [msg, setMsg] = useState<Msg>(null);
  const [secret, setSecret] = useState<{ email: string; password: string } | null>(null);
  const [reason, setReason] = useState('');
  const [trialDays, setTrialDays] = useState('7');
  const load = useCallback(() => { api<Detail>(`/platform/tenants/${org.id}`).then(setD).catch((e) => setMsg({ ok: false, text: errorText(t, e) })); }, [org.id, t]);
  useEffect(load, [load]);
  async function act(p: Promise<unknown>, ok = t('pc.saved')) {
    try { await p; setMsg({ ok: true, text: ok }); reload(); load(); } catch (e) { setMsg({ ok: false, text: errorText(t, e) }); }
  }
  const post = (op: string, body: unknown = {}) => api(`/platform/tenants/${org.id}/${op}`, { method: 'POST', body });
  async function support() {
    try {
      await post('support');
      window.location.hash = '#/dashboard';
      window.location.reload();
    } catch (e) { setMsg({ ok: false, text: errorText(t, e) }); }
  }
  async function resetPw(userId?: string) {
    try { setSecret(await post('reset-password', userId ? { user_id: userId } : {}) as { email: string; password: string }); load(); } catch (e) { setMsg({ ok: false, text: errorText(t, e) }); }
  }
  const o = d?.tenant ?? org;
  const left = daysLeft(o.ends_on);
  return (
    <div style={{ display: 'flex', flexDirection: 'column', minHeight: 0, overflow: 'auto' }}>
      <h3 style={{ margin: 0, padding: '6px 10px', display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
        {o.name} <StatePill s={o.state} /> <small dir="ltr">{o.slug}</small>
      </h3>
      <div className="mx-actions" style={{ padding: '0 10px 6px' }}>
        <button className="mx-btn sm" onClick={() => setTab('renew')}>{t('pc.a.renew')}</button>
        {o.is_active
          ? <button className="mx-btn sm" onClick={() => setTab('info')}>{t('pc.a.block')}</button>
          : <button className="mx-btn sm" onClick={() => void act(post('unblock'))}>{t('pc.a.unblock')}</button>}
        {o.plan === 'trial' && <button className="mx-btn sm" onClick={() => void act(post('extend-trial', { days: Number(trialDays) || 7 }))}>{t('pc.a.extendTrial', { n: Number(trialDays) || 7 })}</button>}
        <button className="mx-btn sm" onClick={() => void resetPw()}>{t('pc.a.resetPw')}</button>
        {o.is_active && <button className="mx-btn sm" onClick={() => void support()}>{t('pc.a.support')}</button>}
      </div>
      {secret && (
        <div style={{ padding: '0 10px 8px', display: 'flex', flexDirection: 'column', gap: 4 }}>
          <small>{t('pc.secretHint', { email: secret.email })}</small>
          <div className="mx-secret">{secret.password}</div>
          <button className="mx-btn sm" style={{ alignSelf: 'start' }} onClick={() => setSecret(null)}>{t('pc.hide')}</button>
        </div>
      )}
      <div style={{ padding: '0 10px' }}><Flash m={msg} /></div>
      <div className="mx-tabs2" role="tablist">
        {TABS.map((x) => <button key={x} role="tab" aria-selected={tab === x} onClick={() => setTab(x)}>{t(`pc.tab.${x}`)}</button>)}
      </div>
      <div style={{ padding: 10, display: 'flex', flexDirection: 'column', gap: 8 }}>
        {tab === 'info' && (
          <>
            <div className="mx-kv">
              <span>{t('pc.f.plan')}</span><b>{planName(plans.find((p) => p.code === o.plan), i18n.language, o.plan)} · {t(`pc.cycle.${o.billing_cycle}`)}</b>
              <span>{t('pc.f.price')}</span><b dir="ltr">{money(o.price, o.currency)}</b>
              <span>{o.plan === 'trial' ? t('pc.f.trialEnds') : t('pc.f.paidUntil')}</span>
              <b className={left !== null && left < 0 ? 'red' : ''}>{o.ends_on ? `${formatDate(o.ends_on)} (${(left ?? 0) < 0 ? t('pc.daysAgo', { count: -(left ?? 0) }) : t('pc.daysLeft', { count: left ?? 0 })})` : '∞'}</b>
              <span>{t('pc.f.grace')}</span><b>{t('pc.days', { count: o.grace_days })}</b>
              <span>{t('pc.f.limits')}</span><b dir="ltr">{o.active_subscriptions}/{o.max_subscribers ?? '∞'} · {o.users}/{o.max_users ?? '∞'} · {o.companies}/{o.max_companies ?? '∞'}</b>
              <span>{t('pc.gf.apps')}</span><b>{o.plan_apps ? o.plan_apps.map((a) => t(`mx.apps.${a}.name`, { defaultValue: a })).join(', ') || '—' : t('pc.allApps')}</b>
              {(o.grandfathered_apps ?? []).length > 0 && <>
                <span>{t('pc.gf.kept')}</span>
                <b>{o.grandfathered_apps.map((a) => t(`mx.apps.${a}.name`, { defaultValue: a })).join(', ')}{' '}
                  <button className="mx-btn sm" onClick={() => void act(api(`/platform/tenants/${o.id}`, { method: 'PATCH', body: { grandfathered_apps: [] } }))}>
                    {t('pc.gf.remove')}</button><br /><small style={{ fontWeight: 400 }}>{t('pc.gf.hint')}</small></b>
              </>}
              <span>{t('pc.f.country')}</span><b>{o.country || '—'} · {o.business_type ? t(`pc.biz.${o.business_type}`, { defaultValue: o.business_type }) : ''}</b>
              <span>{t('pc.f.email')}</span><b dir="ltr">{o.contact_email || o.admin_email}</b>
              <span>{t('pc.f.phone')}</span><b dir="ltr">{o.contact_phone || '—'}</b>
              <span>{t('pc.f.created')}</span><b>{formatDateTime(o.created_at)}</b>
              <span>{t('pc.f.lastActivity')}</span><b>{o.last_activity_at ? formatDateTime(o.last_activity_at) : '—'}</b>
              {!o.is_active && <><span>{t('pc.f.blocked')}</span><b className="red">{o.blocked_at ? formatDateTime(o.blocked_at) : ''} {o.blocked_reason}</b></>}
            </div>
            {(o.state === 'expired' || o.state === 'blocked') && <div className="mx-note">{t('pc.jobs.hint')}</div>}
            {d && (
              <div className="mx-kv">
                <span>{t('pc.u.customers')}</span><b>{d.usage.customers}</b>
                <span>{t('pc.u.invoices')}</span><b>{d.usage.invoices_month}</b>
                <span>{t('pc.u.payments')}</span><b>{d.usage.payments_month}</b>
                <span>{t('pc.u.entries')}</span><b>{d.usage.journal_entries}</b>
                <span>{t('pc.u.storage')}</span><b dir="ltr">{(Number(d.usage.storage_bytes) / 1048576).toFixed(1)} MB</b>
              </div>
            )}
            {o.plan === 'trial' && (
              <label className="mx-row" style={{ gap: 6 }}>{t('pc.trialDays')}
                <input type="number" min={1} max={365} style={{ width: 70 }} value={trialDays} onChange={(e) => setTrialDays(e.currentTarget.value)} /></label>
            )}
            {o.is_active && (
              <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
                <input style={{ flex: 1, minWidth: 160 }} placeholder={t('pc.blockReason')} value={reason} onChange={(e) => setReason(e.currentTarget.value)} />
                <button className="mx-btn sm" disabled={!reason.trim()} onClick={() => void act(post('block', { reason }), t('pc.blockedOk'))}>{t('pc.a.block')}</button>
              </div>
            )}
            {d && d.requests.filter((r) => r.status === 'open').map((r) => (
              <div key={r.id} className="mx-note"><b>{t(`pc.kind.${r.kind}`)}</b> · {r.plan} · {t(`pc.cycle.${r.cycle}`)} · {t(`pc.method.${r.method}`)} {r.reference && <span dir="ltr">· {r.reference}</span>}
                <div>{r.note}</div><small>{formatDateTime(r.created_at)} · {r.created_by_name}</small></div>
            ))}
          </>
        )}
        {tab === 'renew' && <RenewForm org={o} plans={plans} request={request} onDone={(text) => { setMsg({ ok: true, text }); reload(); load(); }} onError={(text) => setMsg({ ok: false, text })} />}
        {tab === 'users' && d && (
          <table className="mx-mini"><tbody>
            {d.users.map((u) => (
              <tr key={u.id} className={u.is_active ? '' : 'muted'}>
                <td><b>{u.full_name}</b><br /><small dir="ltr">{u.email}</small></td>
                <td>{u.role_name || t(`roles.${u.role}`, { defaultValue: u.role })}</td>
                <td><small>{t('pc.lastLogin')}: {u.last_login ? formatDateTime(u.last_login) : '—'}</small></td>
                <td className="num">
                  <button className="mx-btn sm" onClick={() => void act(api(`/platform/tenants/${o.id}/users/${u.id}`, { method: 'PATCH', body: { is_active: !u.is_active } }))}>
                    {u.is_active ? t('pc.a.disable') : t('pc.a.enable')}</button>{' '}
                  <button className="mx-btn sm" onClick={() => void resetPw(u.id)}>{t('pc.a.resetPw')}</button>
                </td>
              </tr>
            ))}
          </tbody></table>
        )}
        {tab === 'companies' && d && (
          <table className="mx-mini"><tbody>
            {d.companies.map((c) => (
              <tr key={c.id} className={c.is_active ? '' : 'muted'}>
                <td><b>{i18n.language === 'ar' && c.name_ar ? c.name_ar : c.name_en}</b></td>
                <td>{c.country || '—'}</td>
                <td>{t(`pc.biz.${c.business_type}`, { defaultValue: c.business_type })}</td>
                <td dir="ltr">{c.base_currency}/{c.local_currency}</td>
                <td><small>{(c.apps ?? []).join(', ')}</small></td>
              </tr>
            ))}
          </tbody></table>
        )}
        {tab === 'payments' && d && <PaymentsTable rows={d.payments} />}
        {tab === 'events' && d && (
          <table className="mx-mini"><tbody>
            {d.events.map((e) => (
              <tr key={e.id}><td><small>{formatDateTime(e.at)}</small></td><td>{t(`pc.ev.${e.action}`, { defaultValue: e.action })}</td>
                <td><small>{eventText(t, e.action, e.detail)}</small></td><td><small dir="ltr">{e.actor_email}</small></td></tr>
            ))}
            {d.events.length === 0 && <tr><td>{t('pc.none')}</td></tr>}
          </tbody></table>
        )}
        {tab === 'settings' && <SettingsForm org={o} plans={plans} onSave={(body) => act(api(`/platform/tenants/${o.id}`, { method: 'PATCH', body }))} />}
      </div>
    </div>
  );
}

function eventText(t: TFunction, action: string, d: Record<string, unknown>) {
  const s = (k: string) => String(d?.[k] ?? '');
  switch (action) {
    case 'renew': return `${s('plan')} · ${s('from')} → ${s('to')} · ${s('amount')} ${s('currency')} · ${t(`pc.method.${s('method')}`, { defaultValue: s('method') })}`;
    case 'block': return d?.jobs ? `${s('reason')} · ${t(`pc.jobs.${s('jobs')}`)}` : s('reason');
    case 'unblock': return d?.jobs ? t(`pc.jobs.${s('jobs')}`) : '';
    case 'plan_update': return Number(d?.grandfathered) > 0 ? `${s('code')} · ${t('pc.gf.count', { n: Number(d.grandfathered) })}` : s('code');
    case 'state': return `${t(`pc.st.${s('from')}`, { defaultValue: s('from') })} → ${t(`pc.st.${s('to')}`, { defaultValue: s('to') })}`
      + (d?.jobs ? ` · ${t(`pc.jobs.${s('jobs')}`)}` : '');
    case 'extend_trial': return s('until');
    case 'reminder': return `${t(`pc.rem.${s('kind')}`, { defaultValue: s('kind') })} · ${s('for')} · ${s('channels') || t('pc.inAppOnly')}`;
    case 'support_start': case 'reset_password': case 'user_enable': case 'user_disable': return s('as') || s('email');
    default: return '';
  }
}

function RenewForm({ org, plans, request, onDone, onError }: { org: Org; plans: Plan[]; request?: Req; onDone: (t: string) => void; onError: (t: string) => void }) {
  const { t, i18n } = useTranslation();
  const startPlan = request?.plan ?? (org.plan === 'trial' ? (plans.find((p) => p.code === 'starter') ? 'starter' : plans.find((p) => p.code !== 'trial')?.code ?? '') : org.plan);
  const [f, setF] = useState({
    period: request ? (request.cycle === 'yearly' ? 'year' : 'month') : (org.billing_cycle === 'yearly' ? 'year' : 'month'),
    until: '', plan: startPlan, amount: '', currency: '', method: request?.method && request.method !== 'other' ? request.method : 'cash',
    reference: request?.reference ?? '', note: '',
  });
  const p = plans.find((x) => x.code === f.plan);
  const defAmount = p ? (f.period === 'year' ? p.price_yearly : p.price_monthly) : '';
  async function submit() {
    const body: Record<string, string> = { period: f.period, plan: f.plan, method: f.method, reference: f.reference, note: f.note };
    if (f.period === 'custom') body.until = f.until;
    if (f.amount) body.amount = f.amount;
    if (f.currency) body.currency = f.currency;
    if (request) body.request_id = request.id;
    try {
      const r = await api<Org>(`/platform/tenants/${org.id}/renew`, { method: 'POST', body });
      onDone(t('pc.renewedOk', { date: formatDate(r.paid_until ?? '') }));
    } catch (e) { onError(errorText(t, e)); }
  }
  return (
    <div className="mx-form" style={{ padding: 0 }}>
      <p style={{ margin: 0 }}><small>{t('pc.renewHint')}</small></p>
      <div className="mx-fld"><span>{t('pc.f.period')}</span>
        <select value={f.period} onChange={(e) => setF({ ...f, period: e.currentTarget.value })}>
          <option value="month">{t('pc.period.month')}</option><option value="year">{t('pc.period.year')}</option><option value="custom">{t('pc.period.custom')}</option>
        </select></div>
      {f.period === 'custom' && <div className="mx-fld"><span>{t('pc.f.until')}</span><input type="date" dir="ltr" value={f.until} onChange={(e) => setF({ ...f, until: e.currentTarget.value })} /></div>}
      <div className="mx-fld"><span>{t('pc.f.plan')}</span>
        <select value={f.plan} onChange={(e) => setF({ ...f, plan: e.currentTarget.value })}>
          {plans.filter((x) => x.code !== 'trial' && x.is_active).map((x) => <option key={x.code} value={x.code}>{planName(x, i18n.language)} · {formatAmount(x.price_monthly, 0)}/{formatAmount(x.price_yearly, 0)} {x.currency}</option>)}
        </select></div>
      <div className="mx-fld"><span>{t('pc.f.amount')}</span><input inputMode="decimal" placeholder={defAmount ? `${formatAmount(defAmount, 2)} ${p?.currency ?? ''}` : ''} value={f.amount} onChange={(e) => setF({ ...f, amount: e.currentTarget.value })} /></div>
      <div className="mx-fld"><span>{t('pc.f.currency')}</span><input dir="ltr" maxLength={3} placeholder={org.price_currency || p?.currency || 'USD'} value={f.currency} onChange={(e) => setF({ ...f, currency: e.currentTarget.value.toUpperCase() })} /></div>
      <div className="mx-fld"><span>{t('pc.f.method')}</span>
        <select value={f.method} onChange={(e) => setF({ ...f, method: e.currentTarget.value })}>{METHODS.map((m) => <option key={m} value={m}>{t(`pc.method.${m}`)}</option>)}</select></div>
      <div className="mx-fld"><span>{t('pc.f.reference')}</span><input dir="ltr" value={f.reference} onChange={(e) => setF({ ...f, reference: e.currentTarget.value })} /></div>
      <div className="mx-fld"><span>{t('pc.f.note')}</span><input value={f.note} onChange={(e) => setF({ ...f, note: e.currentTarget.value })} /></div>
      <button className="mx-btn main" disabled={!f.plan || (f.period === 'custom' && !f.until)} onClick={() => void submit()}>{t('pc.a.renewSave')}</button>
    </div>
  );
}

function SettingsForm({ org, plans, onSave }: { org: Org; plans: Plan[]; onSave: (body: Record<string, unknown>) => void }) {
  const { t, i18n } = useTranslation();
  const [f, setF] = useState({
    name: org.name, plan: org.plan, billing_cycle: org.billing_cycle, paid_until: org.paid_until ?? '', trial_ends_at: org.trial_ends_at?.slice(0, 10) ?? '',
    price_override: org.price_override ?? '', price_currency: org.price_currency ?? '', grace_days: String(org.grace_days),
    max_subscribers: org.max_subscribers === null ? '' : String(org.max_subscribers), max_users: org.max_users === null ? '' : String(org.max_users),
    max_companies: org.max_companies === null ? '' : String(org.max_companies), contact_phone: org.contact_phone, contact_email: org.contact_email, notes: org.notes,
  });
  const set = (k: keyof typeof f) => (e: { currentTarget: { value: string } }) => setF({ ...f, [k]: e.currentTarget.value });
  function save() {
    const body: Record<string, unknown> = {
      name: f.name, billing_cycle: f.billing_cycle, paid_until: f.paid_until, price_override: f.price_override, price_currency: f.price_currency,
      grace_days: Number(f.grace_days) || 0, contact_phone: f.contact_phone, contact_email: f.contact_email, notes: f.notes,
    };
    if (f.plan !== org.plan) body.plan = f.plan; // a new plan brings its own limits unless set here
    const lim = (k: 'max_subscribers' | 'max_users' | 'max_companies', cur: number | null) => {
      const v = f[k].trim() === '' ? 0 : Number(f[k]);
      if (v !== (cur ?? 0) || f.plan === org.plan) body[k] = v;
    };
    if (f.plan === org.plan) { lim('max_subscribers', org.max_subscribers); lim('max_users', org.max_users); lim('max_companies', org.max_companies); }
    if (org.plan === 'trial' || f.plan === 'trial') body.trial_ends_at = f.trial_ends_at;
    onSave(body);
  }
  return (
    <div className="mx-form" style={{ padding: 0 }}>
      <div className="mx-fld"><span>{t('pc.f.org')}</span><input value={f.name} onChange={set('name')} /></div>
      <div className="mx-fld"><span>{t('pc.f.plan')}</span><select value={f.plan} onChange={set('plan')}>
        {plans.map((p) => <option key={p.code} value={p.code}>{planName(p, i18n.language)}</option>)}</select></div>
      <div className="mx-fld"><span>{t('pc.f.cycle')}</span><select value={f.billing_cycle} onChange={set('billing_cycle')}>
        <option value="monthly">{t('pc.cycle.monthly')}</option><option value="yearly">{t('pc.cycle.yearly')}</option></select></div>
      {(org.plan === 'trial' || f.plan === 'trial') && <div className="mx-fld"><span>{t('pc.f.trialEnds')}</span><input type="date" dir="ltr" value={f.trial_ends_at} onChange={set('trial_ends_at')} /></div>}
      <div className="mx-fld"><span>{t('pc.f.paidUntil')}</span><input type="date" dir="ltr" value={f.paid_until} onChange={set('paid_until')} /></div>
      <div className="mx-fld"><span>{t('pc.f.priceOverride')}</span><input inputMode="decimal" placeholder={t('pc.planPrice')} value={f.price_override} onChange={set('price_override')} /></div>
      <div className="mx-fld"><span>{t('pc.f.currency')}</span><input dir="ltr" maxLength={3} value={f.price_currency} onChange={(e) => setF({ ...f, price_currency: e.currentTarget.value.toUpperCase() })} /></div>
      <div className="mx-fld"><span>{t('pc.f.grace')}</span><input type="number" min={0} max={90} value={f.grace_days} onChange={set('grace_days')} /></div>
      {f.plan === org.plan ? (
        <>
          <div className="mx-fld"><span>{t('pc.f.maxSubs')}</span><input type="number" min={0} placeholder="∞" value={f.max_subscribers} onChange={set('max_subscribers')} /></div>
          <div className="mx-fld"><span>{t('pc.f.maxUsers')}</span><input type="number" min={0} placeholder="∞" value={f.max_users} onChange={set('max_users')} /></div>
          <div className="mx-fld"><span>{t('pc.f.maxCompanies')}</span><input type="number" min={0} placeholder="∞" value={f.max_companies} onChange={set('max_companies')} /></div>
        </>
      ) : <small>{t('pc.planLimitsHint')}</small>}
      <div className="mx-fld"><span>{t('pc.f.phone')}</span><input dir="ltr" value={f.contact_phone} onChange={set('contact_phone')} /></div>
      <div className="mx-fld"><span>{t('pc.f.email')}</span><input dir="ltr" value={f.contact_email} onChange={set('contact_email')} /></div>
      <div className="mx-fld"><span>{t('pc.f.notes')}</span><textarea rows={3} value={f.notes} onChange={set('notes')} /></div>
      <button className="mx-btn main" onClick={save}>{t('pc.save')}</button>
    </div>
  );
}

function PaymentsTable({ rows }: { rows: Payment[] }) {
  const { t } = useTranslation();
  if (rows.length === 0) return <small>{t('pc.none')}</small>;
  return (
    <table className="mx-mini"><tbody>
      {rows.map((p) => (
        <tr key={p.id}><td><small>{formatDate(p.created_at)}</small></td><td className="num" dir="ltr">{money(p.amount, p.currency)}</td>
          <td>{t(`pc.method.${p.method}`, { defaultValue: p.method })} <small dir="ltr">{p.reference}</small></td>
          <td><small>{p.plan} · {formatDate(p.period_from)} → {formatDate(p.period_to)}</small></td></tr>
      ))}
    </tbody></table>
  );
}

// ---------- requests ----------

function Requests({ plans, reloadOrgs }: { plans: Plan[]; reloadOrgs: () => void }) {
  const { t } = useTranslation();
  const [status, setStatus] = useState('open');
  const [rows, reload] = useLoad<Req[]>(`/platform/requests?status=${status}`, []);
  const [orgs, reloadList] = useLoad<Org[]>('/platform/tenants', []);
  const [sel, setSel] = useState<Req | null>(null);
  const [answer, setAnswer] = useState('');
  const [msg, setMsg] = useState<Msg>(null);
  const cols: Col<Req>[] = [
    { key: 'at', title: t('pc.f.date'), get: (r) => r.created_at, render: (r) => formatDateTime(r.created_at), width: 130 },
    { key: 'org', title: t('pc.f.org'), get: (r) => r.tenant_name, width: 180 },
    { key: 'kind', title: t('pc.f.kind'), get: (r) => t(`pc.kind.${r.kind}`), width: 80 },
    { key: 'plan', title: t('pc.f.plan'), get: (r) => r.plan, width: 80 },
    { key: 'cycle', title: t('pc.f.cycle'), get: (r) => t(`pc.cycle.${r.cycle}`), width: 70 },
    { key: 'method', title: t('pc.f.method'), get: (r) => t(`pc.method.${r.method}`), width: 80 },
    { key: 'ref', title: t('pc.f.reference'), get: (r) => r.reference, width: 110 },
    { key: 'state', title: t('pc.f.state'), get: (r) => t(`pc.st.${r.state}`), render: (r) => <StatePill s={r.state} />, width: 90 },
    { key: 'status', title: t('pc.f.status'), get: (r) => t(`pc.rs.${r.status}`), width: 80 },
  ];
  async function op(o: 'done' | 'reject') {
    if (!sel) return;
    try { await api(`/platform/requests/${sel.id}/${o}`, { method: 'POST', body: { answer } }); setMsg({ ok: true, text: t('pc.saved') }); setSel(null); setAnswer(''); reload(); reloadOrgs(); }
    catch (e) { setMsg({ ok: false, text: errorText(t, e) }); }
  }
  const org = sel ? orgs.find((o) => o.id === sel.tenant_id) : undefined;
  return (
    <div className="mx-body" style={{ gridTemplateColumns: 'minmax(0, 1fr) minmax(320px, 460px)' }}>
      <section className="mx-box"><h3>{t('pc.m.requests')}</h3>
        <div className="mx-row" style={{ padding: 6, gap: 6 }}>
          <select value={status} onChange={(e) => setStatus(e.currentTarget.value)}>
            <option value="open">{t('pc.rs.open')}</option><option value="done">{t('pc.rs.done')}</option><option value="rejected">{t('pc.rs.rejected')}</option><option value="all">{t('pc.all')}</option>
          </select>
        </div>
        <DataGrid rows={rows} cols={cols} rowKey={(r) => r.id} selected={sel?.id ?? null} title={t('pc.m.requests')} onSelect={(r) => { setSel(r); setMsg(null); }} empty={t('pc.none')} />
      </section>
      <section className="mx-box">
        {sel ? (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 8, padding: 10, overflow: 'auto' }}>
            <h3 style={{ margin: 0 }}>{sel.tenant_name} · {t(`pc.kind.${sel.kind}`)}</h3>
            <div className="mx-kv">
              <span>{t('pc.f.plan')}</span><b>{sel.plan} · {t(`pc.cycle.${sel.cycle}`)}</b>
              <span>{t('pc.f.method')}</span><b>{t(`pc.method.${sel.method}`)} <span dir="ltr">{sel.reference}</span></b>
              <span>{t('pc.f.by')}</span><b>{sel.created_by_name} <small dir="ltr">{sel.created_by_email}</small></b>
              <span>{t('pc.f.paidUntil')}</span><b>{sel.paid_until ? formatDate(sel.paid_until) : '—'}</b>
              {sel.note && <><span>{t('pc.f.note')}</span><b style={{ whiteSpace: 'pre-wrap', fontWeight: 400 }}>{sel.note}</b></>}
              {sel.status !== 'open' && <><span>{t('pc.f.status')}</span><b>{t(`pc.rs.${sel.status}`)} · {sel.handled_by} {sel.answer}</b></>}
            </div>
            <Flash m={msg} />
            {sel.status === 'open' && org && (
              <>
                <h4 style={{ margin: '6px 0 0' }}>{t('pc.renewFromRequest')}</h4>
                <RenewForm key={sel.id} org={org} plans={plans} request={sel}
                  onDone={(text) => { setMsg({ ok: true, text }); setSel(null); reload(); reloadOrgs(); reloadList(); }} onError={(text) => setMsg({ ok: false, text })} />
                <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
                  <input style={{ flex: 1, minWidth: 160 }} placeholder={t('pc.answer')} value={answer} onChange={(e) => setAnswer(e.currentTarget.value)} />
                  <button className="mx-btn sm" onClick={() => void op('done')}>{t('pc.a.markDone')}</button>
                  <button className="mx-btn sm" onClick={() => void op('reject')}>{t('pc.a.reject')}</button>
                </div>
              </>
            )}
          </div>
        ) : <><p style={{ padding: 10 }}>{t('pc.pickRequest')}</p><div style={{ padding: '0 10px' }}><Flash m={msg} /></div></>}
      </section>
    </div>
  );
}

// ---------- payments ----------

function Payments() {
  const { t } = useTranslation();
  const [from, setFrom] = useState(() => today().slice(0, 8) + '01');
  const [to, setTo] = useState(today());
  const [rows] = useLoad<Payment[]>(`/platform/payments?from=${from}&to=${to}`, []);
  const byCur = useMemo(() => {
    const m = new Map<string, number>();
    for (const r of rows) m.set(r.currency, (m.get(r.currency) ?? 0) + Number(r.amount));
    return [...m];
  }, [rows]);
  const cols: Col<Payment>[] = [
    { key: 'at', title: t('pc.f.date'), get: (r) => r.created_at, render: (r) => formatDateTime(r.created_at), width: 130 },
    { key: 'org', title: t('pc.f.org'), get: (r) => r.tenant_name ?? '', width: 190 },
    { key: 'amount', title: t('pc.f.amount'), get: (r) => Number(r.amount), num: true, decimals: 2, width: 90, total: 'sum' },
    { key: 'cur', title: t('pc.f.currency'), get: (r) => r.currency, width: 60 },
    { key: 'method', title: t('pc.f.method'), get: (r) => t(`pc.method.${r.method}`, { defaultValue: r.method }), width: 80 },
    { key: 'ref', title: t('pc.f.reference'), get: (r) => r.reference, width: 110 },
    { key: 'plan', title: t('pc.f.plan'), get: (r) => r.plan, width: 80 },
    { key: 'period', title: t('pc.f.period'), get: (r) => `${r.period_from} → ${r.period_to}`, render: (r) => `${formatDate(r.period_from)} → ${formatDate(r.period_to)}`, width: 170 },
    { key: 'by', title: t('pc.f.by'), get: (r) => r.recorded_by_email, width: 150 },
    { key: 'note', title: t('pc.f.note'), get: (r) => r.note, width: 150 },
  ];
  return (
    <div className="mx-col">
      <div className="mx-strip">
        <label>{t('pc.from')}<input type="date" dir="ltr" value={from} onChange={(e) => setFrom(e.currentTarget.value)} /></label>
        <label>{t('pc.to')}<input type="date" dir="ltr" value={to} onChange={(e) => setTo(e.currentTarget.value)} /></label>
        {byCur.map(([c, v]) => <span key={c} className="mx-out" dir="ltr">{formatAmount(v, 2)} {c}</span>)}
      </div>
      <div className="mx-body" style={{ gridTemplateColumns: 'minmax(0, 1fr)' }}>
        <section className="mx-box"><h3>{t('pc.m.payments')} <small>{rows.length}</small></h3>
          <DataGrid rows={rows} cols={cols} rowKey={(r) => r.id} title={t('pc.m.payments')} footer empty={t('pc.none')} />
        </section>
      </div>
    </div>
  );
}

// ---------- plans ----------

const APPS = ['gen', 'acc', 'col', 'msg', 'stk', 'pur', 'net', 'sat', 'pos', 'hr', 'crm', 'svc', 'mfg'];

function Plans({ plans, reload }: { plans: Plan[]; reload: () => void }) {
  const { t, i18n } = useTranslation();
  const [sel, setSel] = useState<string | null>(null);
  const [msg, setMsg] = useState<Msg>(null);
  const cur = plans.find((p) => p.code === sel) ?? null;
  const cols: Col<Plan>[] = [
    { key: 'code', title: t('pc.f.code'), get: (r) => r.code, width: 90 },
    { key: 'name', title: t('pc.f.name'), get: (r) => planName(r, i18n.language), width: 130 },
    { key: 'pm', title: t('pc.f.priceMonthly'), get: (r) => Number(r.price_monthly), num: true, decimals: 2, width: 90 },
    { key: 'py', title: t('pc.f.priceYearly'), get: (r) => Number(r.price_yearly), num: true, decimals: 2, width: 90 },
    { key: 'cur', title: t('pc.f.currency'), get: (r) => r.currency, width: 60 },
    { key: 'subs', title: t('pc.f.maxSubs'), get: (r) => r.max_subscribers ?? '∞', width: 80 },
    { key: 'users', title: t('pc.f.maxUsers'), get: (r) => r.max_users ?? '∞', width: 70 },
    { key: 'comp', title: t('pc.f.maxCompanies'), get: (r) => r.max_companies ?? '∞', width: 70 },
    { key: 'orgs', title: t('pc.k.orgs'), get: (r) => Number(r.organisations), num: true, decimals: 0, width: 70 },
    { key: 'on', title: t('pc.f.status'), get: (r) => (r.is_active ? (r.is_public ? t('pc.planPublic') : t('pc.planHidden')) : t('pc.planOff')), width: 90 },
  ];
  return (
    <div className="mx-body" style={{ gridTemplateColumns: 'minmax(0, 1fr) minmax(320px, 440px)' }}>
      <section className="mx-box"><h3>{t('pc.m.plans')}</h3>
        <DataGrid rows={plans} cols={cols} rowKey={(r) => r.code} selected={sel} title={t('pc.m.plans')} onSelect={(r) => { setSel(r.code); setMsg(null); }} />
      </section>
      <section className="mx-box" style={{ overflow: 'auto' }}>
        <div style={{ padding: 10, display: 'flex', flexDirection: 'column', gap: 6 }}>
          <div className="mx-row" style={{ gap: 6 }}>
            <h3 style={{ margin: 0, flex: 1 }}>{cur ? planName(cur, i18n.language) : t('pc.newPlan')}</h3>
            {cur && <button className="mx-btn sm" onClick={() => setSel(null)}>{t('pc.newPlan')}</button>}
          </div>
          <PlanForm key={cur?.code ?? 'new'} plan={cur} onSaved={(code) => { setMsg({ ok: true, text: t('pc.saved') }); setSel(code); reload(); }} onError={(text) => setMsg({ ok: false, text })} />
          <Flash m={msg} />
        </div>
      </section>
    </div>
  );
}

function PlanForm({ plan, onSaved, onError }: { plan: Plan | null; onSaved: (code: string) => void; onError: (t: string) => void }) {
  const { t } = useTranslation();
  const n = (v: number | null | undefined) => (v === null || v === undefined ? '' : String(v));
  const [f, setF] = useState({
    code: plan?.code ?? '', name_en: plan?.name_en ?? '', name_ar: plan?.name_ar ?? '', name_fr: plan?.name_fr ?? '',
    price_monthly: plan?.price_monthly ?? '0', price_yearly: plan?.price_yearly ?? '0', currency: plan?.currency ?? 'USD',
    max_subscribers: n(plan?.max_subscribers), max_users: n(plan?.max_users), max_companies: n(plan?.max_companies),
    apps: plan?.apps ?? [], is_public: plan?.is_public ?? true, is_active: plan?.is_active ?? true, sort: String(plan?.sort ?? 10), apply_limits: false,
  });
  const set = (k: keyof typeof f) => (e: { currentTarget: { value: string } }) => setF({ ...f, [k]: e.currentTarget.value });
  async function save() {
    const body: Record<string, unknown> = {
      name_en: f.name_en, name_ar: f.name_ar, name_fr: f.name_fr, price_monthly: f.price_monthly || '0', price_yearly: f.price_yearly || '0', currency: f.currency,
      max_subscribers: Number(f.max_subscribers || 0), max_users: Number(f.max_users || 0), max_companies: Number(f.max_companies || 0),
      apps: f.apps, is_public: f.is_public, is_active: f.is_active, sort: Number(f.sort || 0),
    };
    try {
      if (plan) await api(`/platform/plans/${plan.code}`, { method: 'PATCH', body: { ...body, apply_limits: f.apply_limits } });
      else await api('/platform/plans', { method: 'POST', body: { ...body, code: f.code } });
      onSaved(plan?.code ?? f.code);
    } catch (e) { onError(errorText(t, e)); }
  }
  return (
    <div className="mx-form" style={{ padding: 0 }}>
      {!plan && <div className="mx-fld"><span>{t('pc.f.code')}</span><input dir="ltr" placeholder="gold" value={f.code} onChange={(e) => setF({ ...f, code: e.currentTarget.value.toLowerCase() })} /></div>}
      <div className="mx-fld"><span>{t('pc.f.nameEn')}</span><input value={f.name_en} onChange={set('name_en')} /></div>
      <div className="mx-fld"><span>{t('pc.f.nameAr')}</span><input dir="rtl" value={f.name_ar} onChange={set('name_ar')} /></div>
      <div className="mx-fld"><span>{t('pc.f.nameFr')}</span><input value={f.name_fr} onChange={set('name_fr')} /></div>
      <div className="mx-fld"><span>{t('pc.f.priceMonthly')}</span><input inputMode="decimal" value={f.price_monthly} onChange={set('price_monthly')} /></div>
      <div className="mx-fld"><span>{t('pc.f.priceYearly')}</span><input inputMode="decimal" value={f.price_yearly} onChange={set('price_yearly')} /></div>
      <div className="mx-fld"><span>{t('pc.f.currency')}</span><input dir="ltr" maxLength={3} value={f.currency} onChange={(e) => setF({ ...f, currency: e.currentTarget.value.toUpperCase() })} /></div>
      <div className="mx-fld"><span>{t('pc.f.maxSubs')}</span><input type="number" min={0} placeholder="∞" value={f.max_subscribers} onChange={set('max_subscribers')} /></div>
      <div className="mx-fld"><span>{t('pc.f.maxUsers')}</span><input type="number" min={0} placeholder="∞" value={f.max_users} onChange={set('max_users')} /></div>
      <div className="mx-fld"><span>{t('pc.f.maxCompanies')}</span><input type="number" min={0} placeholder="∞" value={f.max_companies} onChange={set('max_companies')} /></div>
      <div className="mx-fld"><span>{t('pc.f.sort')}</span><input type="number" value={f.sort} onChange={set('sort')} /></div>
      <div><small>{t('pc.f.apps')}</small>
        <div className="mx-row" style={{ gap: 8, flexWrap: 'wrap' }}>
          {APPS.map((a) => (
            <label key={a} className="mx-check"><input type="checkbox" checked={f.apps.length === 0 || f.apps.includes(a)}
              onChange={(e) => {
                const all = f.apps.length === 0 ? APPS : f.apps;
                const next = e.currentTarget.checked ? [...new Set([...all, a])] : all.filter((x) => x !== a);
                setF({ ...f, apps: next.length === APPS.length ? [] : next });
              }} /> {t(`mx.apps.${a}.name`, { defaultValue: a })}</label>
          ))}
        </div>
        <small>{t('pc.appsHint')}</small>
        {plan && <><br /><small>{t('pc.gf.planHint')}</small></>}
      </div>
      <label className="mx-check"><input type="checkbox" checked={f.is_public} onChange={(e) => setF({ ...f, is_public: e.currentTarget.checked })} /> {t('pc.f.public')}</label>
      <label className="mx-check"><input type="checkbox" checked={f.is_active} disabled={plan?.code === 'trial'} onChange={(e) => setF({ ...f, is_active: e.currentTarget.checked })} /> {t('pc.f.active')}</label>
      {plan && <label className="mx-check"><input type="checkbox" checked={f.apply_limits} onChange={(e) => setF({ ...f, apply_limits: e.currentTarget.checked })} /> {t('pc.applyLimits', { n: plan.organisations })}</label>}
      <button className="mx-btn main" disabled={!f.name_en.trim() || (!plan && !/^[a-z][a-z0-9_]{1,30}$/.test(f.code))} onClick={() => void save()}>{t('pc.save')}</button>
    </div>
  );
}
