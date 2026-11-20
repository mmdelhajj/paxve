import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, can, type Me } from '../../api';
import { formatAmount, formatDateTime, today } from '../../util';
import { resizeImage } from '../../image';
import { ShipForm } from '../ops/Ops';
import { useBase, useLoad, useRun } from '../wh/Wh';

// Online store (Magic-style screens): online orders (confirm, cancel, send to a delivery trip, message the customer),
// what the shop shows (visible, featured, description), delivery zones, and the shop settings (address, look, stock,
// prices, payments, careers page) with the links to share.

type Msg = { ok: boolean; text: string } | null;
/** a phone as wa.me wants it (Lebanese local numbers get 961) */
const waNumber = (p: string) => { let d = p.replace(/[^\d]/g, ''); if (d.startsWith('00')) d = d.slice(2); else if (d.length <= 8) d = '961' + d.replace(/^0/, ''); return d; };
function Flash({ m }: { m: Msg }) {
  return m ? <div className={`mx-flash ${m.ok ? 'good' : 'bad'}`}>{m.text}</div> : null;
}

interface ShopOrder {
  id: string; number: number; token: string; sales_order_id: string; quote_number: number; order_number: number | null; order_status: string; customer_id: string;
  name: string; phone: string; email: string; address: string; zone_id: string | null; zone: string | null; delivery: string; payment: string; coupon: string;
  currency: string; subtotal: string; discount: string; fee: string; total: string; notes: string; status: string; created_at: string; state: string;
  trip_number: number | null; invoice_number: number | null;
  shipment: { id: string; carrier: string; tracking_no: string; status: string } | null;
}
interface Product {
  id: string; code: string; name: string; kind: string; is_template: boolean; has_image: boolean; shop_visible: boolean; shop_description: string; category: string | null;
  sale_price: string; sale_currency: string; stock: string; featured: boolean; variants: number;
}
interface Zone { id: string; name: string; fee: string; free_from: string | null; eta: string; seq: number; is_active: boolean; orders: number }
interface Settings {
  enabled: boolean; title: string; tagline: string; banner: string; color: string; warehouse_id: string | null; price_list_id: string | null; currency: string | null;
  show_local: boolean; payments: string[]; pay_note: string; whatsapp: string; min_order: string; auto_confirm: boolean; hide_out_of_stock: boolean; featured: string[];
  terms: string; careers_enabled: boolean; careers_intro: string; public_slug: string | null; base_currency: string; local_currency: string; name_en: string; name_ar: string;
}

const MODES = ['orders', 'products', 'zones', 'settings'] as const;
type Mode = typeof MODES[number];
const STATES = ['open', 'received', 'preparing', 'on_the_way', 'delivered', 'cancelled', ''];
const stColor: Record<string, string> = { received: '#1d4ed8', preparing: '#a15c00', on_the_way: '#7c3aed', delivered: '#0f7b4b', cancelled: '#6b7280' };
const PAYMENTS = ['cod', 'pickup', 'whish', 'omt'];

export default function WebShop({ me, initial }: { me: Me; initial?: string }) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<Mode>((MODES as readonly string[]).includes(initial ?? '') ? initial as Mode : 'orders');
  return (
    <div className="mx-col">
      <div className="mx-strip"><b>{t(`sh.m.${mode}`)}</b><span className="mx-sp" /><small style={{ opacity: 0.8 }}>{t(`sh.help.${mode}`)}</small></div>
      <div className="mx-modes" role="tablist">
        {MODES.map((m) => <button key={m} role="tab" className="mx-mode" aria-selected={mode === m} onClick={() => setMode(m)}>{t(`sh.m.${m}`)}</button>)}
      </div>
      <div style={{ flex: 1, minHeight: 0, overflow: 'auto', padding: 6 }}>
        {mode === 'orders' && <Orders me={me} />}
        {mode === 'products' && <Products me={me} />}
        {mode === 'zones' && <Zones me={me} />}
        {mode === 'settings' && <SettingsPane me={me} />}
      </div>
    </div>
  );
}

function useSettings() {
  const base = useBase();
  return useLoad<{ settings: Settings; stats: { today: number; waiting: number; total: number; sales_30d: string }; public_url: string } | null>(base && `${base}/shop/settings`, null);
}

// ======================= orders =======================

function Orders({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [state, setState] = useState('open');
  const [q, setQ] = useState('');
  const [rows, reload] = useLoad<ShopOrder[]>(base && `${base}/shop/orders?state=${state}&q=${encodeURIComponent(q)}`, []);
  const [st] = useSettings();
  const [sel, setSel] = useState<string | null>(null);
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [msg, run] = useRun(reload);
  const order = rows.find((r) => r.id === sel);
  const origin = st?.public_url ?? window.location.origin;
  const tripable = rows.filter((r) => picked.has(r.id) && r.delivery === 'delivery' && r.state === 'preparing' && !r.trip_number);
  return (
    <div className="mx-row" style={{ gap: 8, alignItems: 'flex-start', flexWrap: 'wrap' }}>
      <section className="mx-box" style={{ flex: '999 1 620px', minWidth: 0 }}>
        <h3>{t('sh.m.orders')}{st && <small style={{ fontWeight: 400 }}> · {t('sh.stats', { today: st.stats.today, waiting: st.stats.waiting, sales: formatAmount(st.stats.sales_30d, 2) })}</small>}</h3>
        <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'center' }}>
          {STATES.map((s) => <button key={s || 'all'} className={`mx-btn sm ${state === s ? 'main' : ''}`} onClick={() => setState(s)}>{t(`sh.st.${s || 'all'}`)}</button>)}
          <input aria-label={t('sh.search')} placeholder={t('sh.search')} value={q} onChange={(e) => setQ(e.currentTarget.value)} style={{ width: 160 }} />
          <span className="mx-sp" />
          {can(me, 'stock.create') && <button className="mx-btn" disabled={tripable.length === 0} onClick={() => void run(api(`${base}/wh/trips`, { method: 'POST', body: {
            trip_date: today(), notes: t('sh.tripNote'), stops: tripable.map((o) => ({ sales_order_id: o.sales_order_id, customer_id: o.customer_id, address: o.address, phone: o.phone, note: `#${o.number}` })) } }),
            () => setPicked(new Set()))}>🚚 {t('sh.toTrip', { n: tripable.length })}</button>}
        </div>
        <Flash m={msg} />
        <div style={{ overflowX: 'auto' }}>
          <table className="mx-table">
            <thead><tr><th /><th>#</th><th>{t('sh.date')}</th><th>{t('sh.customer')}</th><th>{t('sh.phone')}</th><th>{t('sh.zone')}</th><th>{t('sh.payment')}</th>
              <th className="num">{t('sh.total')}</th><th>{t('sh.state')}</th><th>{t('sh.docs')}</th></tr></thead>
            <tbody>
              {rows.map((o) => (
                <tr key={o.id} className={sel === o.id ? 'sel' : ''} style={{ cursor: 'pointer' }} onClick={() => setSel(o.id)}>
                  <td onClick={(e) => e.stopPropagation()}><input type="checkbox" aria-label={`#${o.number}`} checked={picked.has(o.id)} onChange={(e) => {
                    const n = new Set(picked); if (e.currentTarget.checked) n.add(o.id); else n.delete(o.id); setPicked(n);
                  }} /></td>
                  <td>{o.number}</td><td>{formatDateTime(o.created_at)}</td><td><b>{o.name}</b></td><td dir="ltr">{o.phone}</td>
                  <td>{o.delivery === 'pickup' ? t('sh.pickup') : o.zone ?? ''}</td><td>{t(`pub.shop.pay.${o.payment}`)}</td>
                  <td className="num">{formatAmount(o.total, 2)} {o.currency}</td>
                  <td><span className="mx-pill" style={{ background: stColor[o.state], color: '#fff' }}>{t(`sh.st.${o.state}`)}</span>{o.status === 'new' && <span className="mx-pill warn">{t('sh.toConfirm')}</span>}</td>
                  <td dir="ltr"><small>{o.order_number ? `SO-${o.order_number}` : `Q-${o.quote_number}`}{o.trip_number ? ` · T-${o.trip_number}` : ''}{o.invoice_number ? ` · INV-${o.invoice_number}` : ''}</small></td>
                </tr>))}
              {rows.length === 0 && <tr><td colSpan={10} className="mx-empty">{t('sh.none')}</td></tr>}
            </tbody>
          </table>
        </div>
      </section>
      {order && (
        <section className="mx-box" style={{ flex: '1 1 380px', minWidth: 0 }}>
          <h3 style={{ display: 'flex', gap: 6, alignItems: 'center' }}>#{order.number} · {order.name}<span className="mx-sp" /><button className="mx-btn sm" onClick={() => setSel(null)}>{t('sh.close')}</button></h3>
          <div style={{ padding: 6, display: 'flex', flexDirection: 'column', gap: 6 }}>
            <div>📞 <span dir="ltr">{order.phone}</span>{order.email && <> · {order.email}</>}</div>
            {order.delivery === 'delivery' ? <div>🚚 {order.zone} — {order.address}</div> : <div>🏪 {t('sh.pickup')}</div>}
            {order.notes && <div>📝 {order.notes}</div>}
            <div>{t('sh.payment')}: <b>{t(`pub.shop.pay.${order.payment}`)}</b>{order.coupon && <> · {t('sh.coupon')} <b dir="ltr">{order.coupon}</b></>}</div>
            <div>{t('sh.subtotal')} {formatAmount(order.subtotal, 2)} · {t('sh.discount')} {formatAmount(order.discount, 2)} · {t('sh.fee')} {formatAmount(order.fee, 2)} · <b>{t('sh.total')} {formatAmount(order.total, 2)} {order.currency}</b></div>
            <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
              {order.status === 'new' && can(me, 'sales.create') && <button className="mx-btn main" onClick={() => void run(api(`${base}/shop/orders/${order.id}/confirm`, { method: 'POST', body: {} }))}>{t('sh.confirm')}</button>}
              {order.state !== 'cancelled' && order.state !== 'delivered' && can(me, 'sales.void') && <button className="mx-btn" style={{ color: '#b42318' }}
                onClick={() => { if (window.confirm(t('sh.cancelQ'))) void run(api(`${base}/shop/orders/${order.id}/cancel`, { method: 'POST', body: {} })); }}>{t('sh.cancel')}</button>}
              <a className="mx-btn" href={`#/orders`}>{t('sh.openOrder')}</a>
              <a className="mx-btn" target="_blank" rel="noopener noreferrer" href={`${origin}/#/order/${order.token}`}>{t('sh.tracking')}</a>
              <a className="mx-btn" target="_blank" rel="noopener noreferrer" href={`https://wa.me/${waNumber(order.phone)}?text=${encodeURIComponent(t('sh.waCustomer', { name: order.name, n: order.number, link: `${origin}/#/order/${order.token}` }))}`}>🟢 {t('sh.whatsapp')}</a>
            </div>
            <small>{t('sh.flowHelp')}</small>
            {order.shipment && <div>📦 {t('op.ship.carrier')}: <b>{t(`op.ship.c.${order.shipment.carrier}`)}</b> · <span dir="ltr">{order.shipment.tracking_no || '—'}</span> · {t(`op.ship.s.${order.shipment.status}`)}</div>}
            {!order.shipment && order.delivery === 'delivery' && order.state !== 'cancelled' && can(me, 'stock.create') && <ShipForm shopOrderId={order.id} onDone={() => setSel(null)} />}
          </div>
        </section>)}
    </div>
  );
}

// ======================= products =======================

function Products({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [q, setQ] = useState('');
  const [rows, reload] = useLoad<Product[]>(base && `${base}/shop/products?q=${encodeURIComponent(q)}`, []);
  const [st, reloadSt] = useSettings();
  const [msg, run] = useRun(() => { reload(); reloadSt(); });
  const [sel, setSel] = useState<string | null>(null);
  const manage = can(me, 'website.manage');
  const featured = st?.settings.featured ?? [];
  const p = rows.find((r) => r.id === sel);
  const toggleFeatured = (id: string) => {
    const next = featured.includes(id) ? featured.filter((x) => x !== id) : [...featured, id].slice(0, 24);
    void run(api(`${base}/shop/settings`, { method: 'PUT', body: { featured: next } }));
  };
  return (
    <div className="mx-row" style={{ gap: 8, alignItems: 'flex-start', flexWrap: 'wrap' }}>
      <section className="mx-box" style={{ flex: '999 1 560px', minWidth: 0 }}>
        <h3>{t('sh.m.products')}</h3>
        <div className="mx-row" style={{ gap: 6, padding: 6 }}><input aria-label={t('sh.search')} placeholder={t('sh.search')} value={q} onChange={(e) => setQ(e.currentTarget.value)} />
          <small style={{ opacity: 0.8 }}>{t('sh.productsHelp')}</small></div>
        <Flash m={msg} />
        <div style={{ overflowX: 'auto' }}>
          <table className="mx-table">
            <thead><tr><th>{t('sh.show')}</th><th>★</th><th>{t('sh.item')}</th><th>{t('sh.category')}</th><th className="num">{t('sh.price')}</th><th className="num">{t('sh.stock')}</th><th>{t('sh.photo')}</th></tr></thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.id} className={sel === r.id ? 'sel' : ''} onClick={() => setSel(r.id)} style={{ cursor: 'pointer', opacity: r.shop_visible ? 1 : 0.55 }}>
                  <td onClick={(e) => e.stopPropagation()}><input type="checkbox" aria-label={t('sh.show')} disabled={!manage} checked={r.shop_visible}
                    onChange={(e) => void run(api(`${base}/shop/products/${r.id}`, { method: 'PATCH', body: { shop_visible: e.currentTarget.checked } }))} /></td>
                  <td onClick={(e) => e.stopPropagation()}><button className="mx-btn sm" aria-pressed={featured.includes(r.id)} disabled={!manage} title={t('sh.featured')}
                    style={{ color: featured.includes(r.id) ? '#d97706' : '#94a3b8' }} onClick={() => toggleFeatured(r.id)}>★</button></td>
                  <td><b>{r.name}</b> <small dir="ltr">{r.code}</small>{r.is_template && <small> · {t('sh.variantsN', { n: r.variants })}</small>}</td>
                  <td>{r.category ?? ''}</td><td className="num">{formatAmount(r.sale_price, 2)} {r.sale_currency}</td>
                  <td className="num">{r.kind === 'stock' ? Number(r.stock) : '∞'}</td><td>{r.has_image ? '🖼' : <small className="red">{t('sh.noPhoto')}</small>}</td>
                </tr>))}
              {rows.length === 0 && <tr><td colSpan={7} className="mx-empty">{t('sh.none')}</td></tr>}
            </tbody>
          </table>
        </div>
      </section>
      {p && (
        <section className="mx-box" style={{ flex: '1 1 340px', minWidth: 0 }}>
          <h3 style={{ display: 'flex', gap: 6 }}>{p.name}<span className="mx-sp" /><button className="mx-btn sm" onClick={() => setSel(null)}>{t('sh.close')}</button></h3>
          <div style={{ padding: 6, display: 'flex', flexDirection: 'column', gap: 6 }}>
            <label className="mx-fld">{t('sh.description')}<textarea rows={6} disabled={!manage} defaultValue={p.shop_description} key={p.id}
              onBlur={(e) => { if (e.currentTarget.value !== p.shop_description) void run(api(`${base}/shop/products/${p.id}`, { method: 'PATCH', body: { shop_description: e.currentTarget.value } })); }} /></label>
            <small>{t('sh.photoHelp')}</small>
            <a className="mx-btn" href={`#/item/${p.id}`}>{t('sh.openItem')}</a>
            {st?.settings.public_slug && <a className="mx-btn" target="_blank" rel="noopener noreferrer" href={`${st.public_url}/#/shop/${st.settings.public_slug}/p/${p.id}`}>{t('sh.viewInShop')}</a>}
          </div>
        </section>)}
    </div>
  );
}

// ======================= zones =======================

function Zones({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [rows, reload] = useLoad<Zone[]>(base && `${base}/shop/zones`, []);
  const [msg, run] = useRun(reload);
  const [nf, setNf] = useState({ name: '', fee: '', free_from: '', eta: '' });
  const manage = can(me, 'website.manage');
  const num = (s: string) => s.replace(/[^\d.]/g, '');
  const patch = (id: string, body: Record<string, unknown>) => void run(api(`${base}/shop/zones/${id}`, { method: 'PATCH', body }));
  return (
    <section className="mx-box">
      <h3>{t('sh.m.zones')}</h3>
      {manage && <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
        <label className="mx-fld">{t('sh.zone')}<input value={nf.name} onChange={(e) => setNf({ ...nf, name: e.currentTarget.value })} /></label>
        <label className="mx-fld">{t('sh.fee')}<input dir="ltr" style={{ width: 80 }} value={nf.fee} onChange={(e) => setNf({ ...nf, fee: num(e.currentTarget.value) })} /></label>
        <label className="mx-fld">{t('sh.freeFrom')}<input dir="ltr" style={{ width: 90 }} value={nf.free_from} onChange={(e) => setNf({ ...nf, free_from: num(e.currentTarget.value) })} /></label>
        <label className="mx-fld">{t('sh.eta')}<input style={{ width: 120 }} value={nf.eta} onChange={(e) => setNf({ ...nf, eta: e.currentTarget.value })} /></label>
        <button className="mx-btn main" disabled={!nf.name.trim()} onClick={() => void run(api(`${base}/shop/zones`, { method: 'POST', body: { name: nf.name, fee: nf.fee || '0', free_from: nf.free_from, eta: nf.eta } }),
          () => setNf({ name: '', fee: '', free_from: '', eta: '' }))}>{t('sh.add')}</button>
      </div>}
      <Flash m={msg} />
      <table className="mx-table">
        <thead><tr><th>{t('sh.zone')}</th><th className="num">{t('sh.fee')}</th><th className="num">{t('sh.freeFrom')}</th><th>{t('sh.eta')}</th><th>{t('sh.active')}</th><th className="num">{t('sh.ordersN')}</th><th /></tr></thead>
        <tbody>
          {rows.map((z) => (
            <tr key={z.id} style={{ opacity: z.is_active ? 1 : 0.5 }}>
              <td><input disabled={!manage} defaultValue={z.name} key={`n${z.name}`} onBlur={(e) => { if (e.currentTarget.value.trim() && e.currentTarget.value !== z.name) patch(z.id, { name: e.currentTarget.value }); }} /></td>
              <td className="num"><input dir="ltr" style={{ width: 70 }} disabled={!manage} defaultValue={Number(z.fee)} key={`f${z.fee}`} onBlur={(e) => patch(z.id, { fee: num(e.currentTarget.value) || '0' })} /></td>
              <td className="num"><input dir="ltr" style={{ width: 80 }} disabled={!manage} defaultValue={z.free_from ? Number(z.free_from) : ''} key={`x${z.free_from}`} onBlur={(e) => patch(z.id, { free_from: num(e.currentTarget.value) })} /></td>
              <td><input style={{ width: 110 }} disabled={!manage} defaultValue={z.eta} key={`e${z.eta}`} onBlur={(e) => { if (e.currentTarget.value !== z.eta) patch(z.id, { eta: e.currentTarget.value }); }} /></td>
              <td><input type="checkbox" aria-label={t('sh.active')} disabled={!manage} checked={z.is_active} onChange={(e) => patch(z.id, { is_active: e.currentTarget.checked })} /></td>
              <td className="num">{z.orders}</td>
              <td>{manage && <button className="mx-btn sm" onClick={() => void run(api(`${base}/shop/zones/${z.id}`, { method: 'DELETE' }))}>{t('sh.remove')}</button>}</td>
            </tr>))}
          {rows.length === 0 && <tr><td colSpan={7} className="mx-empty">{t('sh.noZones')}</td></tr>}
        </tbody>
      </table>
    </section>
  );
}

// ======================= settings =======================

function SettingsPane({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const base = useBase();
  const [d, reload] = useSettings();
  const [whs] = useLoad<{ id: string; name: string; is_active: boolean }[]>(base && `${base}/warehouses`, []);
  const [lists] = useLoad<{ id: string; name: string; currency: string; is_active: boolean }[]>(base && `${base}/price-lists`, []);
  const [msg, run] = useRun(reload);
  const [slug, setSlug] = useState<string | null>(null);
  const manage = can(me, 'website.manage');
  const s = d?.settings;
  const shopUrl = useMemo(() => (s?.public_slug && d ? `${d.public_url}/#/shop/${s.public_slug}` : ''), [s?.public_slug, d]);
  if (!s || !d) return null;
  const careersUrl = s.public_slug ? `${d.public_url}/#/careers/${s.public_slug}` : '';
  const put = (body: Record<string, unknown>) => void run(api(`${base}/shop/settings`, { method: 'PUT', body }));
  const name = i18n.language === 'ar' && s.name_ar ? s.name_ar : s.name_en;
  const share = t('sh.shareText', { name: s.title || name, link: shopUrl });
  const text = (k: keyof Settings, label: string, rows?: number) => (
    <label className="mx-fld" style={rows ? { flex: '1 1 100%' } : undefined}>{label}
      {rows ? <textarea rows={rows} disabled={!manage} defaultValue={String(s[k] ?? '')} key={`${k}-${String(s[k])}`} onBlur={(e) => { if (e.currentTarget.value !== s[k]) put({ [k]: e.currentTarget.value }); }} />
        : <input disabled={!manage} defaultValue={String(s[k] ?? '')} key={`${k}-${String(s[k])}`} onBlur={(e) => { if (e.currentTarget.value !== s[k]) put({ [k]: e.currentTarget.value }); }} />}
    </label>);
  const check = (k: keyof Settings, label: string) => (
    <label className="mx-fld" style={{ flexDirection: 'row', alignItems: 'center', gap: 6 }}><input type="checkbox" disabled={!manage} checked={Boolean(s[k])} onChange={(e) => put({ [k]: e.currentTarget.checked })} />{label}</label>);
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      <section className="mx-box">
        <h3>{t('sh.address')}</h3>
        <div className="mx-row" style={{ gap: 8, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
          <label className="mx-fld">{t('sh.slug')}<span style={{ display: 'flex', alignItems: 'center', gap: 4 }} dir="ltr"><small>/#/shop/</small>
            <input dir="ltr" disabled={!manage} value={slug ?? s.public_slug ?? ''} onChange={(e) => setSlug(e.currentTarget.value.toLowerCase().replace(/[^a-z0-9-]/g, ''))} style={{ width: 160 }} /></span></label>
          {manage && slug !== null && slug !== (s.public_slug ?? '') && <button className="mx-btn main" onClick={() => void run(api(`${base}/shop/settings`, { method: 'PUT', body: { slug } }), () => setSlug(null))}>{t('sh.saveSlug')}</button>}
          {check('enabled', t('sh.enabled'))}
          {check('careers_enabled', t('sh.careersOn'))}
        </div>
        <Flash m={msg} />
        {shopUrl && <div style={{ padding: 6, display: 'flex', gap: 6, flexWrap: 'wrap', alignItems: 'center' }}>
          <code dir="ltr">{shopUrl}</code>
          <button className="mx-btn sm" onClick={() => void navigator.clipboard?.writeText(shopUrl)}>{t('sh.copy')}</button>
          <a className="mx-btn sm" target="_blank" rel="noopener noreferrer" href={shopUrl}>{t('sh.open')}</a>
          <a className="mx-btn sm" target="_blank" rel="noopener noreferrer" href={`https://wa.me/?text=${encodeURIComponent(share)}`}>🟢 {t('sh.shareWa')}</a>
          {s.careers_enabled && <a className="mx-btn sm" target="_blank" rel="noopener noreferrer" href={careersUrl}>{t('sh.careersPage')}</a>}
        </div>}
        {shopUrl && <div style={{ padding: 6 }}><label className="mx-fld">{t('sh.catalogMsg')}<textarea rows={3} readOnly value={share} /></label></div>}
      </section>
      <section className="mx-box">
        <h3>{t('sh.look')}</h3>
        <div className="mx-row" style={{ gap: 8, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
          {text('title', t('sh.title'))}
          {text('tagline', t('sh.tagline'))}
          <label className="mx-fld">{t('sh.color')}<input type="color" disabled={!manage} value={s.color} onChange={(e) => put({ color: e.currentTarget.value })} /></label>
          <label className="mx-fld">{t('sh.banner')}<input type="file" accept="image/*" disabled={!manage} onChange={async (e) => {
            const f = e.currentTarget.files?.[0]; if (!f) return; put({ banner: await resizeImage(f, 1400, 0.8) });
          }} /></label>
          {s.banner && <><img src={s.banner} alt="" style={{ height: 60, borderRadius: 6 }} />{manage && <button className="mx-btn sm" onClick={() => put({ banner: '' })}>{t('sh.remove')}</button>}</>}
        </div>
      </section>
      <section className="mx-box">
        <h3>{t('sh.selling')}</h3>
        <div className="mx-row" style={{ gap: 8, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
          <label className="mx-fld">{t('sh.warehouse')}<select disabled={!manage} value={s.warehouse_id ?? ''} onChange={(e) => put({ warehouse_id: e.currentTarget.value })}>
            <option value="">{t('sh.defaultWh')}</option>{whs.filter((w) => w.is_active).map((w) => <option key={w.id} value={w.id}>{w.name}</option>)}</select></label>
          <label className="mx-fld">{t('sh.priceList')}<select disabled={!manage} value={s.price_list_id ?? ''} onChange={(e) => put({ price_list_id: e.currentTarget.value })}>
            <option value="">{t('sh.itemPrices')}</option>{lists.filter((l) => l.is_active).map((l) => <option key={l.id} value={l.id}>{l.name} ({l.currency})</option>)}</select></label>
          {!s.price_list_id && <label className="mx-fld">{t('sh.currency')}<select disabled={!manage} value={s.currency ?? ''} onChange={(e) => put({ currency: e.currentTarget.value })}>
            <option value="">{s.base_currency}</option>{s.local_currency && s.local_currency !== s.base_currency && <option value={s.local_currency}>{s.local_currency}</option>}</select></label>}
          {check('show_local', t('sh.showLocal', { cur: s.local_currency }))}
          <label className="mx-fld">{t('sh.minOrder')}<input dir="ltr" style={{ width: 90 }} disabled={!manage} defaultValue={Number(s.min_order) || ''} key={`m${s.min_order}`}
            onBlur={(e) => put({ min_order: e.currentTarget.value.replace(/[^\d.]/g, '') || '0' })} /></label>
          {check('auto_confirm', t('sh.autoConfirm'))}
          {check('hide_out_of_stock', t('sh.hideOOS'))}
        </div>
        <div className="mx-row" style={{ gap: 8, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
          <div className="mx-fld">{t('sh.payments')}<div style={{ display: 'flex', gap: 10, flexWrap: 'wrap' }}>
            {PAYMENTS.map((p) => <label key={p} style={{ display: 'flex', gap: 4, alignItems: 'center', fontWeight: 400 }}><input type="checkbox" disabled={!manage} checked={s.payments.includes(p)}
              onChange={(e) => { const next = e.currentTarget.checked ? [...s.payments, p] : s.payments.filter((x) => x !== p); if (next.length) put({ payments: next }); }} />{t(`pub.shop.pay.${p}`)}</label>)}
          </div></div>
          {text('whatsapp', t('sh.whatsappNo'))}
          {text('pay_note', t('sh.payNote'), 2)}
          {text('terms', t('sh.terms'), 3)}
          {text('careers_intro', t('sh.careersIntro'), 2)}
        </div>
      </section>
    </div>
  );
}
