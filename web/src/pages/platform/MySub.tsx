import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, can, type Me } from '../../api';
import { errorText, formatAmount, formatDate, formatDateTime } from '../../util';
import { planName, StatePill } from './Console';

interface Sub {
  name: string; plan: string; state: string; trial_ends_at: string | null; paid_until: string | null; billing_cycle: string; grace_days: number;
  grace_ends: string | null; ends_on: string | null; days_left: number | null; price: string | null; currency: string; plan_name_en: string; plan_name_ar: string;
  plan_name_fr: string; max_subscribers: number | null; max_users: number | null; max_companies: number | null; contact_phone: string; contact_email: string;
  active_subscriptions: number; active_users: number; active_companies: number;
}
interface PlanRow { code: string; name_en: string; name_ar: string; name_fr: string; price_monthly: string; price_yearly: string; currency: string; max_subscribers: number | null; max_users: number | null; max_companies: number | null }
interface Data {
  subscription: Sub; plans: PlanRow[];
  payments: { id: string; amount: string; currency: string; method: string; reference: string; plan: string; cycle: string; period_from: string; period_to: string; created_at: string }[];
  requests: { id: string; kind: string; plan: string; cycle: string; method: string; reference: string; note: string; status: string; answer: string; created_at: string }[];
  contact: { phone: string; email: string };
}

function Meter({ used, max }: { used: number; max: number | null }) {
  const pct = max ? Math.min(100, Math.round((used / max) * 100)) : 0;
  return (
    <span className="mx-row" style={{ gap: 8, alignItems: 'center' }}>
      <b dir="ltr">{used} / {max ?? '∞'}</b>
      {max ? <span className={`mx-meter ${used >= max ? 'full' : ''}`}><i style={{ width: `${pct}%` }} /></span> : null}
    </span>
  );
}

/** My Paxve subscription: the organisation's plan, end date, usage against the limits, payments and renewal requests. */
export default function MySub({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const [d, setD] = useState<Data | null>(null);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [f, setF] = useState({ kind: 'renew', plan: '', cycle: 'monthly', method: 'whish', reference: '', note: '' });
  const [phone, setPhone] = useState('');
  const canManage = can(me, 'settings.manage');
  const load = useCallback(() => {
    api<Data>('/subscription').then((x) => {
      setD(x);
      setPhone(x.subscription.contact_phone);
      setF((cur) => ({ ...cur, plan: cur.plan || (x.subscription.plan !== 'trial' ? x.subscription.plan : x.plans[0]?.code ?? ''), cycle: x.subscription.billing_cycle || 'monthly' }));
    }).catch((e) => setMsg({ ok: false, text: errorText(t, e) }));
  }, [t]);
  useEffect(load, [load]);
  if (!d) return msg ? <div className={`mx-flash ${msg.ok ? 'good' : 'bad'}`}>{msg.text}</div> : null;
  const s = d.subscription;
  const pname = planName({ code: s.plan, name_en: s.plan_name_en, name_ar: s.plan_name_ar, name_fr: s.plan_name_fr }, i18n.language);
  const chosen = d.plans.find((p) => p.code === f.plan);
  async function send() {
    try {
      await api('/subscription/requests', { method: 'POST', body: f });
      setMsg({ ok: true, text: t('pc.my.sent') });
      setF({ ...f, reference: '', note: '' });
      load();
    } catch (e) { setMsg({ ok: false, text: errorText(t, e) }); }
  }
  async function savePhone() {
    try { await api('/subscription', { method: 'PATCH', body: { contact_phone: phone } }); setMsg({ ok: true, text: t('pc.saved') }); load(); }
    catch (e) { setMsg({ ok: false, text: errorText(t, e) }); }
  }
  return (
    <div className="mx-paper" style={{ maxWidth: 820 }}>
      <h2>{t('pc.my.title')}</h2>
      <div className="mx-owner">
        <div className="mx-owner-head"><b>{pname}</b><StatePill s={s.state} /></div>
        <div>{s.state === 'active' && !s.ends_on ? t('pc.my.state.activeOpen')
          : t(`pc.my.state.${s.state}`, { date: s.ends_on ? formatDate(s.ends_on) : '∞', until: s.grace_ends ? formatDate(s.grace_ends) : '∞', count: Math.abs(s.days_left ?? 0) })}</div>
        <div className="mx-kv" style={{ marginTop: 6 }}>
          <span>{s.plan === 'trial' ? t('pc.f.trialEnds') : t('pc.f.paidUntil')}</span><b>{s.ends_on ? formatDate(s.ends_on) : '∞'}</b>
          {s.price !== null && s.plan !== 'trial' && <><span>{t('pc.f.price')}</span><b dir="ltr">{formatAmount(s.price, 2)} {s.currency} / {t(`pc.cycle.${s.billing_cycle}`)}</b></>}
          <span>{t('pc.my.subs')}</span><Meter used={Number(s.active_subscriptions)} max={s.max_subscribers} />
          <span>{t('pc.my.users')}</span><Meter used={Number(s.active_users)} max={s.max_users} />
          <span>{t('pc.my.companies')}</span><Meter used={Number(s.active_companies)} max={s.max_companies} />
        </div>
      </div>

      {canManage && (
        <>
          <h3 style={{ margin: '6px 0 0' }}>{t('pc.my.ask')}</h3>
          <small>{t('pc.my.askHint')}</small>
          {(d.contact.phone || d.contact.email) && (
            <div className="mx-note">{t('pc.my.payTo')} {d.contact.phone && <b dir="ltr">{d.contact.phone}</b>} {d.contact.email && <b dir="ltr">{d.contact.email}</b>}</div>
          )}
          <div className="mx-form" style={{ padding: 0 }}>
            <div className="mx-fld"><span>{t('pc.f.kind')}</span>
              <select value={f.kind} onChange={(e) => setF({ ...f, kind: e.currentTarget.value })}>
                <option value="renew">{t('pc.kind.renew')}</option><option value="upgrade">{t('pc.kind.upgrade')}</option></select></div>
            <div className="mx-fld"><span>{t('pc.f.plan')}</span>
              <select value={f.plan} onChange={(e) => setF({ ...f, plan: e.currentTarget.value })}>
                {d.plans.map((p) => <option key={p.code} value={p.code}>{planName(p, i18n.language)} · {formatAmount(p.price_monthly, 0)} {p.currency}/{t('pc.cycle.monthly')} · {formatAmount(p.price_yearly, 0)} {p.currency}/{t('pc.cycle.yearly')}</option>)}
              </select></div>
            {chosen && <small>{t('pc.my.planLimits', { subs: chosen.max_subscribers ?? '∞', users: chosen.max_users ?? '∞', companies: chosen.max_companies ?? '∞' })}</small>}
            <div className="mx-fld"><span>{t('pc.f.cycle')}</span>
              <select value={f.cycle} onChange={(e) => setF({ ...f, cycle: e.currentTarget.value })}>
                <option value="monthly">{t('pc.cycle.monthly')}</option><option value="yearly">{t('pc.cycle.yearly')}</option></select></div>
            <div className="mx-fld"><span>{t('pc.f.method')}</span>
              <select value={f.method} onChange={(e) => setF({ ...f, method: e.currentTarget.value })}>
                {['whish', 'omt', 'bank', 'cash', 'card', 'other'].map((m) => <option key={m} value={m}>{t(`pc.method.${m}`)}</option>)}</select></div>
            <div className="mx-fld"><span>{t('pc.f.reference')}</span><input dir="ltr" placeholder={t('pc.my.refHint')} value={f.reference} onChange={(e) => setF({ ...f, reference: e.currentTarget.value })} /></div>
            <div className="mx-fld"><span>{t('pc.f.note')}</span><input value={f.note} onChange={(e) => setF({ ...f, note: e.currentTarget.value })} /></div>
            <button className="mx-btn main" disabled={!f.plan} onClick={() => void send()}>{t('pc.my.send')}</button>
          </div>
          <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap', alignItems: 'center' }}>
            <span>{t('pc.my.phone')}</span>
            <input dir="ltr" value={phone} onChange={(e) => setPhone(e.currentTarget.value)} style={{ padding: '3px 6px' }} />
            <button className="mx-btn sm" onClick={() => void savePhone()}>{t('pc.save')}</button>
          </div>
        </>
      )}
      {msg && <div className={`mx-flash ${msg.ok ? 'good' : 'bad'}`}>{msg.text}</div>}

      {d.requests.length > 0 && (
        <>
          <h3 style={{ margin: '6px 0 0' }}>{t('pc.my.requests')}</h3>
          <table className="mx-mini"><tbody>
            {d.requests.map((r) => (
              <tr key={r.id}><td><small>{formatDateTime(r.created_at)}</small></td><td>{t(`pc.kind.${r.kind}`)} · {r.plan} · {t(`pc.cycle.${r.cycle}`)}</td>
                <td>{t(`pc.method.${r.method}`)} <small dir="ltr">{r.reference}</small></td><td>{t(`pc.rs.${r.status}`)} {r.answer && <small>· {r.answer}</small>}</td></tr>
            ))}
          </tbody></table>
        </>
      )}
      <h3 style={{ margin: '6px 0 0' }}>{t('pc.my.payments')}</h3>
      {d.payments.length === 0 ? <small>{t('pc.none')}</small> : (
        <table className="mx-mini"><tbody>
          {d.payments.map((p) => (
            <tr key={p.id}><td><small>{formatDate(p.created_at)}</small></td><td className="num" dir="ltr">{formatAmount(p.amount, 2)} {p.currency}</td>
              <td>{t(`pc.method.${p.method}`, { defaultValue: p.method })} <small dir="ltr">{p.reference}</small></td>
              <td><small>{p.plan} · {formatDate(p.period_from)} → {formatDate(p.period_to)}</small></td></tr>
          ))}
        </tbody></table>
      )}
    </div>
  );
}
