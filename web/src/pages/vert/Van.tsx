import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, ApiError, type Me } from '../../api';
import { useCompany } from '../../company';
import { errorText, formatAmount } from '../../util';
import { cacheGet, cacheSet, enqueue, newClientRef, outbox, syncOutbox } from '../../outbox';

// Van sales (phone, full screen, works offline): today's customers of the salesman's route with their balance, a sale
// from the van's stock (the server prices it with the customer's price list and checks the credit limit; offline the
// sale waits in the outbox with its client_ref), the van's stock and the end-of-day count + unload.

interface Stop { customer_id: string; code: string; name: string; phone: string; address: string; route: string; balance: string; credit_limit: string | null; credit_hold: boolean }
interface StockRow { item_id: string; code: string; name: string; unit: string; barcode: string; quantity: string; price: string; price_currency: string }
interface Today {
  van: { id: string; name: string }; currency: string; day: { id: string; number: number; opened_at: string; status: string } | null;
  stops: Stop[]; stock: StockRow[]; methods: { id: string; name: string; currency: string; kind: string }[];
}

const big = { minHeight: 48, fontSize: 16 } as const;

export default function Van({ onExit }: { me: Me; onExit: () => void }) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const base = company ? `/companies/${company.id}` : null;
  const cacheKey = `van-${company?.id ?? ''}`;
  const [d, setD] = useState<Today | null>(() => cacheGet<Today>(cacheKey)?.value ?? null);
  const [tab, setTab] = useState<'customers' | 'stock' | 'close'>('customers');
  const [cust, setCust] = useState<Stop | null>(null);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [pending, setPending] = useState(() => outbox().filter((p) => p.path?.endsWith('/van/sales')).length);
  const [online, setOnline] = useState(navigator.onLine);
  const dec = decimalsOf(d?.currency ?? 'USD');

  const load = useCallback(() => {
    if (!base) return;
    api<Today>(`${base}/van/today`).then((x) => { setD(x); cacheSet(cacheKey, x); }).catch((e) => { if (e instanceof ApiError) setMsg({ ok: false, text: errorText(t, e) }); });
  }, [base, cacheKey, t]);
  useEffect(() => { load(); }, [load]);
  useEffect(() => {
    const on = () => { setOnline(true); void syncOutbox().then(() => { setPending(outbox().filter((p) => p.path?.endsWith('/van/sales')).length); load(); }); };
    const off = () => setOnline(false);
    const ob = () => setPending(outbox().filter((p) => p.path?.endsWith('/van/sales')).length);
    window.addEventListener('online', on); window.addEventListener('offline', off); window.addEventListener('paxve:outbox', ob);
    return () => { window.removeEventListener('online', on); window.removeEventListener('offline', off); window.removeEventListener('paxve:outbox', ob); };
  }, [load]);

  return (
    <div dir={i18n.dir(i18n.language)} style={{ minHeight: '100vh', background: '#f3f5f8', display: 'flex', flexDirection: 'column', fontSize: 16 }}>
      <header style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '8px 10px', background: '#14532d', color: '#fff', paddingTop: 'calc(8px + env(safe-area-inset-top, 0px))' }}>
        <b style={{ flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{t('vt.t.van')}{d ? ` · ${d.van.name}` : ''}</b>
        {!online && <span className="mx-pill warn">{t('vt.v.offline')}</span>}
        {pending > 0 && <button className="mx-btn sm" onClick={() => void syncOutbox().then(() => { setPending(outbox().filter((p) => p.path?.endsWith('/van/sales')).length); load(); })}>⇪ {pending}</button>}
        {cust && <button className="mx-btn sm" onClick={() => setCust(null)}>{t('vt.v.back')}</button>}
        <button className="mx-btn sm" onClick={onExit}>{t('vt.close')}</button>
      </header>
      <main style={{ flex: 1, padding: 8, display: 'flex', flexDirection: 'column', gap: 8, maxWidth: 720, width: '100%', boxSizing: 'border-box', marginInline: 'auto' }}>
        {msg && <div className={`mx-flash ${msg.ok ? 'good' : 'bad'}`}>{msg.text}</div>}
        {d && !d.day && <div className="mx-box" style={{ padding: 12 }}>{t('vt.v.noDay')}</div>}
        {d && !cust && (
          <>
            <div className="mx-modes" role="tablist">
              {(['customers', 'stock', 'close'] as const).map((x) => <button key={x} role="tab" className="mx-mode" aria-selected={tab === x} onClick={() => setTab(x)}>{t(`vt.v.${x}`)}</button>)}
            </div>
            {tab === 'customers' && (
              <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                {d.stops.map((s) => (
                  <button key={s.customer_id} className="mx-box" style={{ textAlign: 'start', padding: 10, display: 'flex', gap: 8, alignItems: 'center', cursor: 'pointer', border: '1px solid #dde3ea' }}
                    onClick={() => { setCust(s); setMsg(null); }} disabled={!d.day}>
                    <div style={{ flex: 1 }}><b>{s.name}</b><br /><small>{s.route} · {s.address}</small></div>
                    <div style={{ textAlign: 'end' }}><b dir="ltr">{formatAmount(s.balance, dec)}</b>{s.credit_hold && <><br /><span className="mx-pill off">{t('vt.v.hold')}</span></>}
                      {s.credit_limit && <><br /><small>{t('vt.v.limit', { v: formatAmount(s.credit_limit, dec) })}</small></>}</div>
                  </button>))}
                {d.stops.length === 0 && <div className="mx-empty">{t('vt.v.noStops')}</div>}
              </div>
            )}
            {tab === 'stock' && (
              <table className="mx-table"><tbody>{d.stock.map((s) => (
                <tr key={s.item_id}><td>{s.code} · {s.name}</td><td dir="ltr"><b>{Number(s.quantity)}</b> {s.unit}</td><td dir="ltr">{formatAmount(s.price, dec)}</td></tr>))}
                {d.stock.length === 0 && <tr><td className="mx-empty">{t('vt.v.empty')}</td></tr>}</tbody></table>
            )}
            {tab === 'close' && d.day && <CloseDay base={base!} d={d} online={online && pending === 0} onDone={(text) => { setMsg({ ok: true, text }); load(); setTab('customers'); }}
              onError={(e) => setMsg({ ok: false, text: errorText(t, e) })} />}
          </>
        )}
        {d && cust && d.day && <Sale base={base!} companyId={company!.id} d={d} cust={cust} dec={dec} online={online}
          onDone={(text, ok) => { setMsg({ ok, text }); setCust(null); load(); setPending(outbox().filter((p) => p.path?.endsWith('/van/sales')).length); }} />}
      </main>
    </div>
  );
}

function Sale({ base, companyId, d, cust, dec, online, onDone }: {
  base: string; companyId: string; d: Today; cust: Stop; dec: number; online: boolean; onDone: (text: string, ok: boolean) => void;
}) {
  const { t } = useTranslation();
  const [qty, setQty] = useState<Record<string, string>>({});
  const [paid, setPaid] = useState('');
  const [method, setMethod] = useState(d.methods.find((m) => m.kind === 'cash' && m.currency === d.currency)?.id ?? d.methods[0]?.id ?? '');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [q, setQ] = useState('');
  const lines = d.stock.filter((s) => Number(qty[s.item_id] || 0) > 0).map((s) => ({ item_id: s.item_id, quantity: qty[s.item_id] }));
  const estimate = d.stock.reduce((a, s) => a + Number(qty[s.item_id] || 0) * Number(s.price), 0);
  const over = d.stock.some((s) => Number(qty[s.item_id] || 0) > Number(s.quantity));
  const save = async () => {
    const ref = newClientRef();
    const body = { customer_id: cust.customer_id, warehouse_id: d.van.id, lines, method_id: Number(paid) > 0 ? method : '', paid: paid || '' };
    const queue = () => {
      enqueue({ client_ref: ref, company_id: companyId, customer_name: cust.name, created_at: new Date().toISOString(), body, path: `${base}/van/sales` });
      onDone(t('vt.v.queued'), true);
    };
    if (!online) { queue(); return; }
    setBusy(true);
    try {
      const r = await api<{ invoice: { invoice: { number: number; total: string; paid: string } } }>(`${base}/van/sales`, { method: 'POST', body: { ...body, client_ref: ref } });
      onDone(t('vt.v.saved', { n: r.invoice.invoice.number, total: formatAmount(r.invoice.invoice.total, dec), paid: formatAmount(r.invoice.invoice.paid, dec) }), true);
    } catch (e) {
      if (e instanceof ApiError && e.status < 500) setErr(errorText(t, e));
      else queue();
    } finally { setBusy(false); }
  };
  return (
    <section className="mx-box" style={{ padding: 8 }}>
      <h3>{cust.name} <small dir="ltr">{cust.phone}</small></h3>
      <div className="mx-row" style={{ gap: 6, padding: '4px 0' }}><span>{t('vt.v.balance')}</span><b dir="ltr">{formatAmount(cust.balance, dec)}</b></div>
      <input placeholder={t('vt.v.find')} value={q} onChange={(e) => setQ(e.currentTarget.value)} style={{ width: '100%', marginBottom: 6 }} />
      <table className="mx-table"><tbody>
        {d.stock.filter((s) => !q || s.name.toLowerCase().includes(q.toLowerCase()) || s.code.toLowerCase().includes(q.toLowerCase()) || s.barcode === q).map((s) => (
          <tr key={s.item_id}>
            <td style={{ whiteSpace: 'normal' }}><b>{s.name}</b><br /><small dir="ltr">{s.code} · {formatAmount(s.price, dec)} · {t('vt.v.left', { n: Number(s.quantity) })}</small></td>
            <td style={{ whiteSpace: 'nowrap' }}>
              <button className="mx-btn" style={{ minHeight: 40, minWidth: 40 }} onClick={() => setQty({ ...qty, [s.item_id]: String(Math.max(0, Number(qty[s.item_id] || 0) - 1)) })}>−</button>
              <input dir="ltr" inputMode="decimal" style={{ width: 56, fontSize: 18, textAlign: 'center' }} value={qty[s.item_id] ?? ''} aria-label={s.name}
                onChange={(e) => setQty({ ...qty, [s.item_id]: e.currentTarget.value.replace(/[^\d.]/g, '') })} />
              <button className="mx-btn" style={{ minHeight: 40, minWidth: 40 }} onClick={() => setQty({ ...qty, [s.item_id]: String(Number(qty[s.item_id] || 0) + 1) })}>+</button>
            </td>
          </tr>))}
      </tbody></table>
      <div className="mx-row" style={{ gap: 6, padding: '8px 0', flexWrap: 'wrap' }}>
        <span>{t('vt.v.estimate')}</span><b dir="ltr" style={{ fontSize: 20 }}>{formatAmount(estimate, dec)} {d.currency}</b>
      </div>
      <small>{t('vt.v.priceNote')}</small>
      <div className="mx-row" style={{ gap: 6, padding: '8px 0', flexWrap: 'wrap' }}>
        <label className="mx-row" style={{ gap: 4 }}>{t('vt.v.paid')}<input dir="ltr" inputMode="decimal" style={{ width: 100, fontSize: 18 }} value={paid} onChange={(e) => setPaid(e.currentTarget.value.replace(/[^\d.]/g, ''))} /></label>
        <select value={method} onChange={(e) => setMethod(e.currentTarget.value)} aria-label={t('vt.v.method')}>{d.methods.map((m) => <option key={m.id} value={m.id}>{m.name}</option>)}</select>
        <button className="mx-btn sm" onClick={() => setPaid(String(Math.round(estimate * 10 ** dec) / 10 ** dec))}>{t('vt.v.all')}</button>
      </div>
      {err && <div className="mx-flash bad">{err}</div>}
      {over && <div className="mx-flash bad">{t('vt.v.over')}</div>}
      <button className="mx-btn main" style={{ ...big, width: '100%' }} disabled={busy || lines.length === 0 || over} onClick={() => void save()}>{t('vt.v.save')}</button>
    </section>
  );
}

function CloseDay({ base, d, online, onDone, onError }: { base: string; d: Today; online: boolean; onDone: (text: string) => void; onError: (e: unknown) => void }) {
  const { t } = useTranslation();
  const [counted, setCounted] = useState<Record<string, string>>(() => Object.fromEntries(d.stock.map((s) => [s.item_id, String(Number(s.quantity))])));
  const [unload, setUnload] = useState(true);
  const [busy, setBusy] = useState(false);
  const close = async () => {
    setBusy(true);
    try {
      const r = await api<{ summary?: { sales: number; sales_total: string; collected: string; differences: unknown[] }; pending_approval?: boolean; differences?: unknown[] }>(
        `${base}/van/days/${d.day!.id}/close`, { method: 'POST',
          body: { unload, counted: Object.entries(counted).map(([item_id, quantity]) => ({ item_id, quantity: quantity || '0' })) } });
      // a shortage is approved by a stock manager: the day stays open until then
      if (r.pending_approval || !r.summary) onDone(t('vt.v.pendingApproval', { d: (r.differences ?? []).length }));
      else onDone(t('vt.v.closed', { n: r.summary.sales, total: r.summary.sales_total, cash: r.summary.collected, d: r.summary.differences.length }));
    } catch (e) { onError(e); } finally { setBusy(false); }
  };
  return (
    <section className="mx-box" style={{ padding: 8 }}>
      <small>{t('vt.v.countHint')}</small>
      <table className="mx-table"><tbody>{d.stock.map((s) => {
        const diff = Number(counted[s.item_id] || 0) - Number(s.quantity);
        return <tr key={s.item_id}><td>{s.name}</td><td dir="ltr">{Number(s.quantity)}</td>
          <td><input dir="ltr" inputMode="decimal" style={{ width: 70, fontSize: 18 }} value={counted[s.item_id] ?? ''} aria-label={s.name}
            onChange={(e) => setCounted({ ...counted, [s.item_id]: e.currentTarget.value.replace(/[^\d.]/g, '') })} /></td>
          <td dir="ltr">{diff ? <span className={`mx-pill ${diff < 0 ? 'off' : 'warn'}`}>{diff > 0 ? '+' : ''}{diff}</span> : ''}</td></tr>;
      })}</tbody></table>
      <label className="mx-row" style={{ gap: 6, padding: 6 }}><input type="checkbox" checked={unload} onChange={(e) => setUnload(e.currentTarget.checked)} />{t('vt.v.unload')}</label>
      {!online && <div className="mx-flash bad">{t('vt.v.needOnline')}</div>}
      <button className="mx-btn main" style={{ ...big, width: '100%' }} disabled={busy || !online} onClick={() => void close()}>{t('vt.v.closeDay')}</button>
    </section>
  );
}
