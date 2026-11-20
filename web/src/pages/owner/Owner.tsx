import { Suspense, lazy, useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, can, type Me } from '../../api';
import { useCompany } from '../../company';
import { applyLang, languages } from '../../i18n';
import { formatAmount, formatDateTime } from '../../util';
import Logo from '../../components/Logo';
import InstallApp, { setBadge } from '../../components/InstallApp';
import { alertText, type Alert } from '../smart/Smart';
import '../../magic.css';

const Assistant = lazy(() => import('../smart/Smart').then((m) => ({ default: m.Assistant })));

// Phase 6b — "Paxve Owner": the owner's phone app. Today at a glance (collections, sales, cash in tills and banks,
// money owed), the smart alerts, approvals waiting, in-app notifications and the AI assistant. Refreshes every minute.

interface Money { currency: string; amount: string; payments?: number; invoices?: number; customers?: number; overdue?: string }
interface Today {
  day: string; base_currency: string; collected: Money[]; sales: Money[]; cash: Money[]; receivable: Money[];
  counts: { alerts: number; alerts_high: number; approvals: number; orders_today: number }; alerts: Alert[];
}
interface Note { id: string; kind: string; title: string; body: string; entity_type: string | null; read_at: string | null; created_at: string; actor: string | null }

const TABS = ['today', 'alerts', 'ask', 'news'] as const;
type Tab = typeof TABS[number];

export default function OwnerApp({ me, onExit, onLogout }: { me: Me; onExit: () => void; onLogout: () => void }) {
  const { t } = useTranslation();
  const { company, companies, setCompanyId, decimalsOf } = useCompany();
  const [tab, setTab] = useState<Tab>('today');
  const [d, setD] = useState<Today | null>(null);
  const [notes, setNotes] = useState<Note[]>([]);
  const [err, setErr] = useState(false);
  const [at, setAt] = useState<string>('');
  const base = company ? `/companies/${company.id}` : null;
  const load = useCallback(() => {
    if (!base) return;
    api<Today>(`${base}/owner/today`).then((x) => { setD(x); setErr(false); setAt(new Date().toISOString()); }).catch(() => setErr(true));
    api<{ items: Note[] | null }>(`${base}/notifications`).then((x) => setNotes(x.items ?? [])).catch(() => setNotes([]));
  }, [base]);
  useEffect(() => {
    load();
    const id = window.setInterval(load, 60000);
    return () => window.clearInterval(id);
  }, [load]);
  const unread = notes.filter((n) => !n.read_at).length;
  useEffect(() => { setBadge((d?.counts.alerts_high ?? 0) + (d?.counts.approvals ?? 0) + unread); }, [d, unread]);

  const m = (x: Money) => `${formatAmount(x.amount, decimalsOf(x.currency))} ${x.currency}`;
  const list = (rows: Money[] | undefined) => (rows && rows.length ? rows.map((x) => <div key={x.currency} dir="ltr" className="ow-num">{m(x)}</div>)
    : <div className="ow-num" dir="ltr">0</div>);
  const smart = can(me, 'smart.view');

  return (
    <div className="ow-app">
      <style>{CSS}</style>
      <header className="ow-top">
        <Logo />
        {companies.length > 1 && company && (
          <select aria-label={t('mx.company')} value={company.id} onChange={(e) => { setD(null); setCompanyId(e.currentTarget.value); }}>
            {companies.map((c) => <option key={c.id} value={c.id}>{c.display ?? c.name_en}</option>)}
          </select>
        )}
        <span style={{ flex: 1 }} />
        <InstallApp className="ow-chip" label={t('pwa.short')} />
        <details className="ow-menu">
          <summary aria-label={t('pwa.owner.menu')}>☰</summary>
          <div>
            <b>{me.full_name}</b>
            {languages.map((l) => <button key={l.code} type="button" onClick={() => applyLang(l.code)}>{l.label}</button>)}
            <button type="button" onClick={onExit}>{t('pwa.owner.full')}</button>
            <button type="button" className="red" onClick={() => { api('/auth/logout', { method: 'POST' }).finally(onLogout); }}>{t('common.logout')}</button>
          </div>
        </details>
      </header>

      <nav className="ow-tabs" role="tablist">
        {TABS.filter((x) => (x !== 'alerts' && x !== 'ask') || smart).map((x) => (
          <button key={x} type="button" role="tab" aria-selected={tab === x} onClick={() => setTab(x)}>
            {t(`pwa.owner.t.${x}`)}
            {x === 'alerts' && (d?.counts.alerts ?? 0) > 0 && <i>{d?.counts.alerts}</i>}
            {x === 'news' && unread > 0 && <i>{unread}</i>}
          </button>
        ))}
      </nav>

      <main className="ow-main">
        {err && <div className="mx-flash bad">{t('pwa.owner.offline')}</div>}
        {tab === 'today' && (!d ? <p className="ow-dim">…</p> : (
          <>
            <p className="ow-dim">{t('pwa.owner.today', { day: d.day })}{at && ` · ${t('pwa.owner.updated', { at: new Date(at).toLocaleTimeString(document.documentElement.lang || 'en', { hour: '2-digit', minute: '2-digit' }) })}`}</p>
            <div className="ow-grid">
              <section className="ow-card main"><h4>{t('pwa.owner.collected')}</h4>{list(d.collected)}
                <small>{t('pwa.owner.nPayments', { n: d.collected.reduce((a, x) => a + (x.payments ?? 0), 0) })}</small></section>
              <section className="ow-card"><h4>{t('pwa.owner.sales')}</h4>{list(d.sales)}
                <small>{t('pwa.owner.nInvoices', { n: d.sales.reduce((a, x) => a + (x.invoices ?? 0), 0) })}</small></section>
              <section className="ow-card"><h4>{t('pwa.owner.cash')}</h4>{list(d.cash)}<small>{t('pwa.owner.cashHint')}</small></section>
              <section className="ow-card"><h4>{t('pwa.owner.owed')}</h4>{list(d.receivable)}
                {d.receivable.map((x) => Number(x.overdue) > 0 && <small key={x.currency} className="red" dir="ltr">{t('pwa.owner.overdue')}: {formatAmount(x.overdue ?? '0', decimalsOf(x.currency))} {x.currency}</small>)}</section>
            </div>
            <div className="ow-row">
              {smart && <button type="button" className={`ow-pill ${d.counts.alerts_high > 0 ? 'bad' : ''}`} onClick={() => setTab('alerts')}>
                {t('pwa.owner.alertsN', { n: d.counts.alerts })}{d.counts.alerts_high > 0 && ` · ${t('pwa.owner.highN', { n: d.counts.alerts_high })}`}</button>}
              <a className={`ow-pill ${d.counts.approvals > 0 ? 'warn' : ''}`} href="#/approvals">{t('pwa.owner.approvalsN', { n: d.counts.approvals })}</a>
              <a className="ow-pill" href="#/dashboard">{t('pwa.owner.ordersN', { n: d.counts.orders_today })}</a>
            </div>
            {smart && d.alerts.length > 0 && (
              <section className="ow-card"><h4>{t('pwa.owner.latest')}</h4>
                {d.alerts.slice(0, 3).map((a) => <AlertRow key={a.id} a={a} />)}
              </section>
            )}
            {smart && <button type="button" className="ow-big" onClick={() => setTab('ask')}>✦ {t('pwa.owner.askBtn')}</button>}
          </>
        ))}
        {tab === 'alerts' && (!d ? null : d.alerts.length === 0 ? <p className="ow-dim">{t('sm.al.none')}</p> : (
          <section className="ow-card">
            {d.alerts.map((a) => <AlertRow key={a.id} a={a} />)}
            <a className="ow-link" href="#/smart/alerts">{t('pwa.owner.allAlerts')}</a>
          </section>
        ))}
        {tab === 'ask' && <div className="ow-ask"><Suspense fallback={<p className="ow-dim">…</p>}><Assistant me={me} /></Suspense></div>}
        {tab === 'news' && (notes.length === 0 ? <p className="ow-dim">{t('common.empty')}</p> : (
          <section className="ow-card">
            {notes.slice(0, 50).map((n) => (
              <div key={n.id} className={`ow-note ${n.read_at ? '' : 'new'}`}>
                <b>{n.title}</b>{n.body && <span>{n.body}</span>}<small>{formatDateTime(n.created_at)}{n.actor ? ` · ${n.actor}` : ''}</small>
              </div>
            ))}
            {unread > 0 && base && <button type="button" className="ow-link" onClick={() => { api(`${base}/notifications/read`, { method: 'POST', body: {} }).then(load).catch(() => {}); }}>{t('pwa.owner.markRead')}</button>}
          </section>
        ))}
      </main>
    </div>
  );
}

function AlertRow({ a }: { a: Alert }) {
  const { t } = useTranslation();
  return (
    <div className={`ow-alert ${a.severity}`}>
      <b>{t(`sm.al.k.${a.kind}`, { defaultValue: a.kind })}{a.subject_label ? ` · ${a.subject_label}` : ''}</b>
      <span>{alertText(t, a)}</span>
    </div>
  );
}

const CSS = `
.ow-app{min-height:100vh;background:#F4F6F9;color:#0F1B2D;display:flex;flex-direction:column;font-size:14px}
.ow-top{position:sticky;top:0;z-index:20;display:flex;align-items:center;gap:8px;padding:8px 12px;padding-top:calc(8px + env(safe-area-inset-top,0px));background:#0B1F3A;color:#fff}
.ow-top select{max-width:150px;background:#13305A;color:#fff;border:1px solid #24426F;border-radius:6px;padding:4px}
.ow-chip{background:#13305A;color:#fff;border:1px solid #24426F;border-radius:999px;padding:4px 10px;font:inherit;font-size:12.5px;cursor:pointer}
.ow-menu{position:relative}.ow-menu summary{list-style:none;cursor:pointer;font-size:20px;padding:0 6px}.ow-menu summary::-webkit-details-marker{display:none}
.ow-menu>div{position:absolute;inset-inline-end:0;top:120%;background:#fff;color:#0F1B2D;border:1px solid #E3E8EF;border-radius:10px;padding:8px;display:flex;flex-direction:column;gap:2px;min-width:200px;box-shadow:0 8px 24px rgba(15,27,45,.18)}
.ow-menu>div button{font:inherit;text-align:start;background:none;border:0;padding:8px;border-radius:6px;cursor:pointer}.ow-menu>div button:hover{background:#EEF3FF}.ow-menu .red{color:#B42318}
.ow-tabs{display:flex;background:#fff;border-bottom:1px solid #E3E8EF;position:sticky;top:calc(48px + env(safe-area-inset-top,0px));z-index:19}
.ow-tabs button{flex:1;font:inherit;font-weight:600;background:none;border:0;border-bottom:3px solid transparent;padding:10px 4px;cursor:pointer;color:#3B4A5E}
.ow-tabs button[aria-selected=true]{color:#1D4ED8;border-bottom-color:#1D4ED8}
.ow-tabs i{font-style:normal;background:#B42318;color:#fff;border-radius:999px;padding:0 6px;margin-inline-start:4px;font-size:11px}
.ow-main{padding:12px;display:flex;flex-direction:column;gap:12px;max-width:720px;width:100%;margin:0 auto;box-sizing:border-box;padding-bottom:calc(16px + env(safe-area-inset-bottom,0px))}
.ow-dim{color:#5B6B7F;margin:0;font-size:13px}
.ow-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:10px}
.ow-card{background:#fff;border:1px solid #E3E8EF;border-radius:12px;padding:12px;display:flex;flex-direction:column;gap:4px;min-width:0}
.ow-card.main{background:#0B1F3A;color:#fff;border-color:#0B1F3A}.ow-card.main small{color:#B8C6DA}
.ow-card h4{margin:0 0 2px;font-size:12.5px;font-weight:600;color:inherit;opacity:.85}
.ow-num{font-family:'IBM Plex Mono',ui-monospace,monospace;font-size:18px;font-weight:600;text-align:start;overflow-wrap:anywhere}
.ow-card small{color:#5B6B7F}.ow-card small.red{color:#B42318}
.ow-row{display:flex;gap:8px;flex-wrap:wrap}
.ow-pill{font:inherit;font-size:13px;font-weight:600;background:#fff;border:1px solid #D5DCE6;border-radius:999px;padding:6px 12px;color:#0F1B2D;text-decoration:none;cursor:pointer}
.ow-pill.bad{background:#FDECEA;border-color:#F3C4BF;color:#B42318}.ow-pill.warn{background:#FFF4E0;border-color:#F5D9A8;color:#A15C00}
.ow-big{font:inherit;font-size:16px;font-weight:700;background:#1D4ED8;color:#fff;border:0;border-radius:12px;padding:14px;cursor:pointer}
.ow-alert{display:flex;flex-direction:column;gap:2px;padding:8px 10px;border-inline-start:4px solid #D5DCE6;background:#F8FAFC;border-radius:6px;margin-bottom:6px}
.ow-alert.high{border-inline-start-color:#B42318}.ow-alert.medium{border-inline-start-color:#A15C00}.ow-alert span{color:#3B4A5E;font-size:13px}
.ow-link{font:inherit;color:#1D4ED8;background:none;border:0;padding:6px 0;cursor:pointer;text-align:start}
.ow-note{display:flex;flex-direction:column;gap:2px;padding:8px 0;border-bottom:1px solid #EEF1F5;color:inherit;text-decoration:none}
.ow-note.new b::before{content:'● ';color:#1D4ED8}.ow-note span{color:#3B4A5E;font-size:13px}.ow-note small{color:#5B6B7F;font-size:11.5px}
.ow-ask{background:#fff;border:1px solid #E3E8EF;border-radius:12px;padding:6px;min-height:60vh}
@media (max-width:360px){.ow-grid{grid-template-columns:minmax(0,1fr)}}
`;
