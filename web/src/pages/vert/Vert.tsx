import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, can, type Me } from '../../api';
import { formatAmount, formatDate, formatDateTime, today } from '../../util';
import { printPage } from '../../native';
import { useBase, useLoad, useRun } from '../wh/Wh';

// Business packs (Magic-style screens): electronics (serial history and warranty card), furniture (orders with deposit,
// delivery and assembly; configurator; showroom), wholesale (routes, van days, commission, vans), pharmacy (drug search,
// expiry board, controlled register, settings, insurance claims), restaurant setup (tables, modifiers, food cost,
// service charge) and the business fields of an item.

type Msg = { ok: boolean; text: string } | null;
function Flash({ m }: { m: Msg }) {
  return m ? <div className={`mx-flash ${m.ok ? 'good' : 'bad'}`}>{m.text}</div> : null;
}
const amt = (v: string | number | null | undefined, d = 2) => formatAmount(String(v ?? 0), d);
const qty = (v: string | number | null | undefined) => (v == null || v === '' ? '' : String(Math.round(Number(v) * 1000) / 1000));

const MODES: Record<string, string[]> = {
  electronics: ['serial'],
  furniture: ['orders', 'configure', 'showroom'],
  wholesale: ['routes', 'days', 'commission', 'vans'],
  pharmacy: ['search', 'expiry', 'controlled', 'psettings'],
  claims: ['claims'],
  restaurant: ['tables', 'modifiers', 'foodcost', 'rsettings'],
  items: ['itemfields'],
};

export default function Vert({ me, initial }: { me: Me; initial?: string }) {
  const { t } = useTranslation();
  const group = initial && MODES[initial] ? initial : 'items';
  const modes = MODES[group];
  const [mode, setMode] = useState(modes[0]);
  return (
    <div className="mx-col">
      <div className="mx-strip"><b>{t(`vt.g.${group}`)}</b><span className="mx-sp" /><small style={{ opacity: 0.8 }}>{t(`vt.help.${mode}`)}</small></div>
      {modes.length > 1 && (
        <div className="mx-modes" role="tablist">
          {modes.map((m) => <button key={m} role="tab" className="mx-mode" aria-selected={mode === m} onClick={() => setMode(m)}>{t(`vt.m.${m}`)}</button>)}
        </div>
      )}
      <div style={{ flex: 1, minHeight: 0, overflow: 'auto', padding: 6 }}>
        {mode === 'serial' && <SerialLookup />}
        {mode === 'orders' && <FurnOrders me={me} />}
        {mode === 'configure' && <Configurator />}
        {mode === 'showroom' && <Showroom />}
        {mode === 'routes' && <Routes me={me} />}
        {mode === 'days' && <VanDays me={me} />}
        {mode === 'commission' && <Commission />}
        {mode === 'vans' && <Vans me={me} />}
        {mode === 'search' && <DrugSearch />}
        {mode === 'expiry' && <Expiry />}
        {mode === 'controlled' && <Controlled />}
        {mode === 'psettings' && <PharmaSettings me={me} />}
        {mode === 'claims' && <Claims me={me} />}
        {mode === 'tables' && <Tables me={me} />}
        {mode === 'modifiers' && <Modifiers me={me} />}
        {mode === 'foodcost' && <FoodCost />}
        {mode === 'rsettings' && <RestSettings me={me} />}
        {mode === 'itemfields' && <ItemFields me={me} />}
      </div>
    </div>
  );
}

// ---------- shared pickers ----------

interface ItemLite { id: string; code: string; name: string; kind: string; is_active: boolean }
function useItems(): ItemLite[] {
  const base = useBase();
  const [items] = useLoad<ItemLite[]>(base && `${base}/items`, []);
  return items;
}
function ItemSelect({ value, onChange, label }: { value: string; onChange: (v: string) => void; label?: string }) {
  const { t } = useTranslation();
  const items = useItems();
  return (
    <label className="mx-row" style={{ gap: 4 }}>{label ?? t('vt.f.item')}
      <select value={value} onChange={(e) => onChange(e.currentTarget.value)} style={{ maxWidth: 320 }}>
        <option value="">—</option>
        {items.filter((i) => i.is_active).map((i) => <option key={i.id} value={i.id}>{i.code} · {i.name}</option>)}
      </select>
    </label>
  );
}
export type CustLite = { id: string; code: string; name: string; phone?: string };
export async function findCustomers(base: string | null, q: string): Promise<CustLite[]> {
  if (!base) return [];
  const r = await api<{ rows?: CustLite[] } | CustLite[]>(`${base}/customers?q=${encodeURIComponent(q)}&limit=30`);
  return Array.isArray(r) ? r : r.rows ?? [];
}
interface UserLite { id: string; full_name: string; email: string; role: string; is_active: boolean }
function useUsers(): UserLite[] {
  const [users] = useLoad<UserLite[]>('/users', []);
  return users;
}

// ======================= electronics =======================

interface SerialHistory {
  serial: string; company_name: string;
  lots: { lot_no: string; item_id: string; item_code: string; item_name: string; warranty_months: number; in_stock: string; warehouse: string | null }[];
  moves: { move_date: string; source: string; quantity: string; warehouse: string; doc_number: number | null; partner: string | null; notes: string }[];
  warranty: { found: boolean; sale_date?: string; warranty_until?: string; under_warranty?: boolean; warranty_months?: number };
  repairs: { id: string; number: number; status: string; fault: string; under_warranty: boolean; received_at: string }[];
}

function SerialLookup() {
  const { t } = useTranslation();
  const base = useBase();
  const [q, setQ] = useState('');
  const [d, setD] = useState<SerialHistory | null>(null);
  const [msg, run] = useRun();
  const look = () => q.trim() && void run(api<SerialHistory>(`${base}/stock/serial-history?serial=${encodeURIComponent(q.trim())}`), (x) => setD(x as SerialHistory));
  const lot = d?.lots[0];
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
        <input autoFocus dir="ltr" placeholder={t('vt.e.serialPh')} value={q} onChange={(e) => setQ(e.currentTarget.value)}
          onKeyDown={(e) => { if (e.key === 'Enter') look(); }} style={{ minWidth: 260 }} />
        <button className="mx-btn main" onClick={look}>{t('vt.e.lookup')}</button>
        {lot && <button className="mx-btn" onClick={() => printPage()}>{t('vt.e.printCard')}</button>}
      </div>
      <Flash m={msg && !msg.ok ? msg : null} />
      {d && !lot && <div className="mx-empty">{t('vt.e.unknown')}</div>}
      {d && lot && <>
        <section className="mx-box print-area">
          <h3>{t('vt.e.card')} — {d.company_name}</h3>
          <table className="mx-table"><tbody>
            <tr><th>{t('vt.f.item')}</th><td>{lot.item_code} · {lot.item_name}</td></tr>
            <tr><th>{t('vt.e.serial')}</th><td dir="ltr"><b>{lot.lot_no}</b></td></tr>
            <tr><th>{t('vt.e.saleDate')}</th><td>{d.warranty.sale_date ? formatDate(d.warranty.sale_date) : '—'}</td></tr>
            <tr><th>{t('vt.e.warranty')}</th><td>{lot.warranty_months ? t('vt.e.months', { n: lot.warranty_months }) : t('vt.e.noWarranty')}
              {d.warranty.warranty_until && <> · {t('vt.e.until', { date: formatDate(d.warranty.warranty_until) })}</>}
              {' '}{d.warranty.sale_date && <span className={`mx-pill ${d.warranty.under_warranty ? 'on' : 'off'}`}>{t(d.warranty.under_warranty ? 'vt.e.covered' : 'vt.e.expired')}</span>}</td></tr>
            <tr><th>{t('vt.e.inStock')}</th><td>{Number(lot.in_stock) > 0 ? `${t('common.yes')} · ${lot.warehouse ?? ''}` : t('common.no')}</td></tr>
          </tbody></table>
        </section>
        <section className="mx-box">
          <h3>{t('vt.e.history')}</h3>
          <div style={{ overflowX: 'auto' }}>
            <table className="mx-table">
              <thead><tr><th>{t('vt.f.date')}</th><th>{t('vt.e.what')}</th><th>{t('vt.e.doc')}</th><th>{t('vt.e.partner')}</th><th>{t('vt.f.warehouse')}</th><th>{t('vt.f.qty')}</th></tr></thead>
              <tbody>{d.moves.map((m, i) => (
                <tr key={i}><td>{formatDate(m.move_date)}</td><td>{t(`vt.src.${m.source}`, { defaultValue: m.source })}</td><td dir="ltr">{m.doc_number ?? ''}</td>
                  <td>{m.partner ?? ''}</td><td>{m.warehouse}</td><td dir="ltr">{qty(m.quantity)}</td></tr>))}</tbody>
            </table>
          </div>
        </section>
        {d.repairs.length > 0 && <section className="mx-box"><h3>{t('vt.e.repairs')}</h3>
          <table className="mx-table"><tbody>{d.repairs.map((r) => (
            <tr key={r.id}><td dir="ltr">#{r.number}</td><td>{formatDate(r.received_at)}</td><td>{r.fault}</td><td>{t(`vt.rep.${r.status}`, { defaultValue: r.status })}</td>
              <td>{r.under_warranty && <span className="mx-pill on">{t('vt.e.covered')}</span>}</td></tr>))}</tbody></table></section>}
      </>}
      <small style={{ padding: 6 }}>{t('vt.e.note')}</small>
    </div>
  );
}

// ======================= furniture =======================

interface FurnOrder {
  id: string; number: number; order_number: number | null; status: string; customer_name: string; customer_phone: string; currency: string; total: string;
  deposit_pct: string; deposit_required: string; deposit_paid: string; delivery_date: string | null; delivery_slot: string; assembly: boolean;
  job_number: number | null; job_status: string | null; trip_number: number | null; trip_status: string | null; made_to_order: boolean;
}
interface Method { id: string; name: string; currency: string; kind: string; is_active: boolean }

function FurnOrders({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [all, setAll] = useState(false);
  const [rows, reload] = useLoad<FurnOrder[]>(base && `${base}/furn/orders${all ? '?status=all' : ''}`, []);
  const [methods] = useLoad<Method[]>(base && `${base}/payment-methods`, []);
  const users = useUsers();
  const [msg, run] = useRun(reload);
  const [dep, setDep] = useState<{ id: string; method: string; amount: string } | null>(null);
  const [sch, setSch] = useState<{ id: string; date: string; slot: string; start: string; assembly: boolean; technician: string } | null>(null);
  return (
    <section className="mx-box">
      <h3 style={{ display: 'flex', gap: 8, alignItems: 'center' }}>{t('vt.m.orders')}<span className="mx-sp" />
        <label className="mx-row" style={{ gap: 4 }}><input type="checkbox" checked={all} onChange={(e) => setAll(e.currentTarget.checked)} />{t('vt.f.showAll')}</label></h3>
      <Flash m={msg} />
      {dep && (
        <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap' }}>
          <b>{t('vt.fu.deposit')}</b>
          <select value={dep.method} onChange={(e) => setDep({ ...dep, method: e.currentTarget.value })}>
            <option value="">—</option>{methods.filter((m) => m.is_active).map((m) => <option key={m.id} value={m.id}>{m.name} ({m.currency})</option>)}
          </select>
          <input dir="ltr" inputMode="decimal" style={{ width: 110 }} value={dep.amount} onChange={(e) => setDep({ ...dep, amount: e.currentTarget.value })} />
          <button className="mx-btn main" disabled={!dep.method || !Number(dep.amount)} onClick={() => void run(api(`${base}/furn/orders/${dep.id}/deposit`,
            { method: 'POST', body: { method_id: dep.method, amount: dep.amount } }), () => setDep(null))}>{t('vt.fu.receive')}</button>
          <button className="mx-btn" onClick={() => setDep(null)}>{t('common.cancel')}</button>
        </div>
      )}
      {sch && (
        <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap' }}>
          <b>{t('vt.fu.schedule')}</b>
          <input type="date" value={sch.date} onChange={(e) => setSch({ ...sch, date: e.currentTarget.value })} />
          <input placeholder={t('vt.fu.slot')} style={{ width: 120 }} value={sch.slot} onChange={(e) => setSch({ ...sch, slot: e.currentTarget.value })} />
          <label className="mx-row" style={{ gap: 4 }}><input type="checkbox" checked={sch.assembly} onChange={(e) => setSch({ ...sch, assembly: e.currentTarget.checked })} />{t('vt.fu.assembly')}</label>
          {sch.assembly && <>
            <input type="time" value={sch.start} onChange={(e) => setSch({ ...sch, start: e.currentTarget.value })} />
            <select value={sch.technician} onChange={(e) => setSch({ ...sch, technician: e.currentTarget.value })} aria-label={t('vt.fu.fitter')}>
              <option value="">{t('vt.fu.fitter')}</option>{users.filter((u) => u.is_active).map((u) => <option key={u.id} value={u.id}>{u.full_name}</option>)}
            </select></>}
          <button className="mx-btn main" onClick={() => void run(api(`${base}/furn/orders/${sch.id}/schedule`, { method: 'POST', body: { date: sch.date, slot: sch.slot,
            start: sch.start, assembly: sch.assembly, technician_id: sch.technician || undefined } }), () => setSch(null))}>{t('common.save')}</button>
          <button className="mx-btn" onClick={() => setSch(null)}>{t('common.cancel')}</button>
        </div>
      )}
      <div style={{ overflowX: 'auto' }}>
        <table className="mx-table">
          <thead><tr><th>#</th><th>{t('vt.f.customer')}</th><th>{t('vt.f.status')}</th><th>{t('vt.f.total')}</th><th>{t('vt.fu.required')}</th><th>{t('vt.fu.paid')}</th>
            <th>{t('vt.fu.balance')}</th><th>{t('vt.fu.delivery')}</th><th>{t('vt.fu.assembly')}</th><th /></tr></thead>
          <tbody>
            {rows.map((o) => {
              const bal = Number(o.total) - Number(o.deposit_paid);
              const short = Number(o.deposit_paid) < Number(o.deposit_required);
              return (
                <tr key={o.id}>
                  <td dir="ltr">{o.order_number ?? o.number}{o.made_to_order && <span className="mx-pill warn" style={{ marginInlineStart: 4 }}>{t('vt.fu.mto')}</span>}</td>
                  <td>{o.customer_name}<br /><small dir="ltr">{o.customer_phone}</small></td>
                  <td>{t(`vt.so.${o.status}`, { defaultValue: o.status })}</td>
                  <td dir="ltr">{amt(o.total)} {o.currency}</td>
                  <td dir="ltr">{amt(o.deposit_required)} <small>({Number(o.deposit_pct)}%)</small></td>
                  <td dir="ltr"><span className={short ? 'mx-pill warn' : 'mx-pill on'}>{amt(o.deposit_paid)}</span></td>
                  <td dir="ltr">{amt(bal)}</td>
                  <td>{o.delivery_date ? <>{formatDate(o.delivery_date)} {o.delivery_slot}<br /><small>{t('vt.fu.trip', { n: o.trip_number })} · {t(`vt.trip.${o.trip_status}`, { defaultValue: o.trip_status ?? '' })}</small></> : '—'}</td>
                  <td>{o.assembly ? <>{t('vt.fu.job', { n: o.job_number })}<br /><small>{t(`vt.job.${o.job_status}`, { defaultValue: o.job_status ?? '' })}</small></> : '—'}</td>
                  <td style={{ whiteSpace: 'nowrap' }}>
                    {can(me, 'collections.create') && o.status !== 'cancelled' && o.status !== 'done' &&
                      <button className="mx-btn sm" onClick={() => setDep({ id: o.id, method: '', amount: String(Math.max(0, Number(o.deposit_required) - Number(o.deposit_paid)) || '') })}>{t('vt.fu.deposit')}</button>}{' '}
                    {can(me, 'sales.create') && o.status === 'confirmed' && (!o.trip_number || o.trip_status === 'cancelled') &&
                      <button className="mx-btn sm" onClick={() => setSch({ id: o.id, date: today(), slot: '', start: '09:00', assembly: true, technician: '' })}>{t('vt.fu.schedule')}</button>}
                  </td>
                </tr>);
            })}
            {rows.length === 0 && <tr><td colSpan={10} className="mx-empty">{t('vt.none')}</td></tr>}
          </tbody>
        </table>
      </div>
      <small style={{ padding: 6, display: 'block' }}>{t('vt.fu.note')}</small>
    </section>
  );
}

interface Opt { name: string; values: { label: string; extra: string }[] }
function Configurator() {
  const { t } = useTranslation();
  const base = useBase();
  const [item, setItem] = useState('');
  const [v] = useLoad<{ options: Opt[]; price_per_m2: string | null; made_to_order: boolean } | null>(base && item && `${base}/items/${item}/vertical`, null);
  const [choices, setChoices] = useState<Record<string, string>>({});
  const [size, setSize] = useState({ w: '', d: '' });
  const [res, setRes] = useState<{ unit_price: string; description: string; currency: string; area_m2: string } | null>(null);
  const [msg, run] = useRun();
  return (
    <section className="mx-box">
      <h3>{t('vt.m.configure')}</h3>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 6, padding: 6 }}>
        <ItemSelect value={item} onChange={(x) => { setItem(x); setChoices({}); setRes(null); }} />
        {(v?.options ?? []).map((o) => (
          <label key={o.name} className="mx-row" style={{ gap: 4 }}>{o.name}
            <select value={choices[o.name] ?? ''} onChange={(e) => setChoices({ ...choices, [o.name]: e.currentTarget.value })}>
              <option value="">—</option>{o.values.map((x) => <option key={x.label} value={x.label}>{x.label}{Number(x.extra) ? ` (+${x.extra})` : ''}</option>)}
            </select>
          </label>
        ))}
        {v?.price_per_m2 && (
          <div className="mx-row" style={{ gap: 6 }}>{t('vt.fu.size')}
            <input dir="ltr" inputMode="decimal" style={{ width: 80 }} placeholder={t('vt.fu.width')} value={size.w} onChange={(e) => setSize({ ...size, w: e.currentTarget.value })} />×
            <input dir="ltr" inputMode="decimal" style={{ width: 80 }} placeholder={t('vt.fu.depth')} value={size.d} onChange={(e) => setSize({ ...size, d: e.currentTarget.value })} /> cm
            <small>({t('vt.fu.perM2', { p: amt(v.price_per_m2) })})</small>
          </div>
        )}
        <div><button className="mx-btn main" disabled={!item} onClick={() => void run(api(`${base}/furn/configure`, { method: 'POST',
          body: { item_id: item, choices: Object.fromEntries(Object.entries(choices).filter(([, x]) => x)), width_cm: size.w, depth_cm: size.d } }), (x) => setRes(x as typeof res))}>{t('vt.fu.price')}</button></div>
        <Flash m={msg && !msg.ok ? msg : null} />
        {res && <div className="mx-box" style={{ padding: 8 }}><b dir="ltr" style={{ fontSize: 20 }}>{amt(res.unit_price)} {res.currency}</b><br />{res.description}
          {Number(res.area_m2) > 0 && <><br /><small>{t('vt.fu.area', { a: res.area_m2 })}</small></>}<br />
          <small>{t('vt.fu.copyHint')}</small></div>}
      </div>
    </section>
  );
}

function Showroom() {
  const { t } = useTranslation();
  const base = useBase();
  const [rows] = useLoad<{ warehouse: string; item_code: string; item_name: string; quantity: string; value: string; sale_price: string; days: number | null }[]>(base && `${base}/furn/showroom`, []);
  return (
    <section className="mx-box">
      <h3>{t('vt.m.showroom')}</h3>
      <table className="mx-table">
        <thead><tr><th>{t('vt.f.warehouse')}</th><th>{t('vt.f.item')}</th><th>{t('vt.f.qty')}</th><th>{t('vt.f.value')}</th><th>{t('vt.f.price')}</th><th>{t('vt.fu.days')}</th></tr></thead>
        <tbody>{rows.map((r, i) => (
          <tr key={i}><td>{r.warehouse}</td><td>{r.item_code} · {r.item_name}</td><td dir="ltr">{qty(r.quantity)}</td><td dir="ltr">{amt(r.value)}</td><td dir="ltr">{amt(r.sale_price)}</td>
            <td dir="ltr"><span className={`mx-pill ${(r.days ?? 0) > 180 ? 'off' : (r.days ?? 0) > 90 ? 'warn' : ''}`}>{r.days ?? '—'}</span></td></tr>))}
          {rows.length === 0 && <tr><td colSpan={6} className="mx-empty">{t('vt.fu.noShowroom')}</td></tr>}</tbody>
      </table>
    </section>
  );
}

// ======================= wholesale =======================

interface Route { id: string; name: string; user_id: string | null; user_name: string | null; warehouse_id: string | null; warehouse: string | null; weekdays: number[]; is_active: boolean; stops: number }
interface WhV { id: string; name: string; is_active: boolean; is_showroom: boolean; is_van: boolean; van_user_id: string | null; van_user_name: string | null; value: string }
const DAYS = [0, 1, 2, 3, 4, 5, 6];

function Routes({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [rows, reload] = useLoad<Route[]>(base && `${base}/van/routes`, []);
  const [whs] = useLoad<WhV[]>(base && `${base}/verticals/warehouses`, []);
  const users = useUsers();
  const [msg, run] = useRun(reload);
  const [edit, setEdit] = useState<{ id: string; name: string; user_id: string; warehouse_id: string; weekdays: number[] } | null>(null);
  const [sel, setSel] = useState<string | null>(null);
  const [detail, reloadDetail] = useLoad<{ stops: { customer_id: string; code: string; name: string; phone: string; balance: string }[] } | null>(base && sel && `${base}/van/routes/${sel}`, null);
  const [custQ, setCustQ] = useState('');
  const [found, setFound] = useState<{ id: string; code: string; name: string }[]>([]);
  const manage = can(me, 'stock.manage');
  const stops = detail?.stops ?? [];
  const putStops = (ids: string[]) => void run(api(`${base}/van/routes/${sel}/stops`, { method: 'PUT', body: { customer_ids: ids } }), () => reloadDetail());
  return (
    <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap', alignItems: 'flex-start' }}>
      <section className="mx-box" style={{ flex: '1 1 420px' }}>
        <h3 style={{ display: 'flex', gap: 8 }}>{t('vt.m.routes')}<span className="mx-sp" />
          {manage && <button className="mx-btn sm main" onClick={() => setEdit({ id: '', name: '', user_id: '', warehouse_id: '', weekdays: [] })}>{t('vt.new')}</button>}</h3>
        <Flash m={msg} />
        {edit && (
          <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap' }}>
            <input placeholder={t('vt.f.name')} value={edit.name} onChange={(e) => setEdit({ ...edit, name: e.currentTarget.value })} />
            <select value={edit.user_id} onChange={(e) => setEdit({ ...edit, user_id: e.currentTarget.value })} aria-label={t('vt.w.salesman')}>
              <option value="">{t('vt.w.salesman')}</option>{users.map((u) => <option key={u.id} value={u.id}>{u.full_name}</option>)}</select>
            <select value={edit.warehouse_id} onChange={(e) => setEdit({ ...edit, warehouse_id: e.currentTarget.value })} aria-label={t('vt.w.van')}>
              <option value="">{t('vt.w.van')}</option>{whs.filter((w) => w.is_van).map((w) => <option key={w.id} value={w.id}>{w.name}</option>)}</select>
            <span className="mx-row" style={{ gap: 2 }}>{DAYS.map((d) => (
              <label key={d} style={{ fontSize: 12 }}><input type="checkbox" checked={edit.weekdays.includes(d)}
                onChange={(e) => setEdit({ ...edit, weekdays: e.currentTarget.checked ? [...edit.weekdays, d] : edit.weekdays.filter((x) => x !== d) })} />{t(`vt.dow.${d}`)}</label>))}</span>
            <button className="mx-btn main" disabled={!edit.name.trim()} onClick={() => void run(api(`${base}/van/routes${edit.id ? '/' + edit.id : ''}`, { method: edit.id ? 'PATCH' : 'POST',
              body: { name: edit.name, user_id: edit.user_id, warehouse_id: edit.warehouse_id, weekdays: edit.weekdays } }), () => setEdit(null))}>{t('common.save')}</button>
            <button className="mx-btn" onClick={() => setEdit(null)}>{t('common.cancel')}</button>
          </div>
        )}
        <table className="mx-table">
          <thead><tr><th>{t('vt.f.name')}</th><th>{t('vt.w.salesman')}</th><th>{t('vt.w.van')}</th><th>{t('vt.w.days')}</th><th>{t('vt.w.stops')}</th><th /></tr></thead>
          <tbody>{rows.map((r) => (
            <tr key={r.id} style={{ opacity: r.is_active ? 1 : 0.5, background: sel === r.id ? 'var(--mx-sel, #fff3c4)' : undefined }} onClick={() => setSel(r.id)}>
              <td><b>{r.name}</b></td><td>{r.user_name ?? '—'}</td><td>{r.warehouse ?? '—'}</td>
              <td>{r.weekdays.length ? r.weekdays.map((d) => t(`vt.dow.${d}`)).join(' ') : t('vt.w.everyDay')}</td><td dir="ltr">{r.stops}</td>
              <td>{manage && <button className="mx-btn sm" onClick={(e) => { e.stopPropagation(); setEdit({ id: r.id, name: r.name, user_id: r.user_id ?? '', warehouse_id: r.warehouse_id ?? '', weekdays: r.weekdays }); }}>{t('common.edit')}</button>}</td>
            </tr>))}
            {rows.length === 0 && <tr><td colSpan={6} className="mx-empty">{t('vt.none')}</td></tr>}</tbody>
        </table>
      </section>
      {sel && (
        <section className="mx-box" style={{ flex: '1 1 360px' }}>
          <h3>{t('vt.w.stopsOf', { name: rows.find((r) => r.id === sel)?.name ?? '' })}</h3>
          {manage && (
            <div className="mx-row" style={{ gap: 6, padding: 6 }}>
              <input placeholder={t('vt.w.findCustomer')} value={custQ} onChange={(e) => setCustQ(e.currentTarget.value)}
                onKeyDown={(e) => { if (e.key === 'Enter') findCustomers(base, custQ).then(setFound).catch(() => setFound([])); }} />
              {found.length > 0 && <select onChange={(e) => { const id = e.currentTarget.value; if (id && !stops.some((s) => s.customer_id === id)) putStops([...stops.map((s) => s.customer_id), id]); }} value="">
                <option value="">{t('vt.w.add')}</option>{found.map((c) => <option key={c.id} value={c.id}>{c.code} · {c.name}</option>)}</select>}
            </div>
          )}
          <table className="mx-table">
            <tbody>{stops.map((s, i) => (
              <tr key={s.customer_id}><td dir="ltr">{i + 1}</td><td>{s.name}<br /><small dir="ltr">{s.phone}</small></td><td dir="ltr">{amt(s.balance)}</td>
                <td style={{ whiteSpace: 'nowrap' }}>{manage && <>
                  <button className="mx-btn sm" disabled={i === 0} aria-label="up" onClick={() => { const ids = stops.map((x) => x.customer_id); [ids[i - 1], ids[i]] = [ids[i], ids[i - 1]]; putStops(ids); }}>▲</button>
                  <button className="mx-btn sm" aria-label={t('vt.del')} onClick={() => putStops(stops.filter((x) => x.customer_id !== s.customer_id).map((x) => x.customer_id))}>✕</button></>}</td>
              </tr>))}
              {stops.length === 0 && <tr><td className="mx-empty">{t('vt.none')}</td></tr>}</tbody>
          </table>
        </section>
      )}
    </div>
  );
}

interface VanDay {
  id: string; number: number; day: string; status: string; warehouse: string; user_name: string | null; opened_at: string; closed_at: string | null;
  summary: { sales?: number; sales_total?: string; collected?: string; differences?: { item_name: string; expected: string; counted: string; difference: string }[] };
}
function VanDays({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [rows, reload] = useLoad<VanDay[]>(base && `${base}/van/days?from=${today().slice(0, 8)}01&to=${today()}`, []);
  const [whs] = useLoad<WhV[]>(base && `${base}/verticals/warehouses`, []);
  const [msg, run] = useRun(reload);
  const [open, setOpen] = useState<{ van: string; src: string; lines: { item_id: string; quantity: string }[] } | null>(null);
  const [line, setLine] = useState({ item_id: '', quantity: '' });
  return (
    <section className="mx-box">
      <h3 style={{ display: 'flex', gap: 8 }}>{t('vt.m.days')}<span className="mx-sp" />
        {can(me, 'stock.create') && <button className="mx-btn sm main" onClick={() => setOpen({ van: whs.find((w) => w.is_van)?.id ?? '', src: whs.find((w) => !w.is_van && !w.is_showroom)?.id ?? '', lines: [] })}>{t('vt.w.openDay')}</button>}
        <button className="mx-btn sm" onClick={() => { window.location.hash = '#/van'; }}>{t('vt.t.van')}</button></h3>
      <Flash m={msg} />
      {open && (
        <div style={{ padding: 6, display: 'flex', flexDirection: 'column', gap: 6 }}>
          <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
            <label className="mx-row" style={{ gap: 4 }}>{t('vt.w.van')}<select value={open.van} onChange={(e) => setOpen({ ...open, van: e.currentTarget.value })}>
              {whs.filter((w) => w.is_van).map((w) => <option key={w.id} value={w.id}>{w.name}</option>)}</select></label>
            <label className="mx-row" style={{ gap: 4 }}>{t('vt.w.from')}<select value={open.src} onChange={(e) => setOpen({ ...open, src: e.currentTarget.value })}>
              {whs.filter((w) => !w.is_van).map((w) => <option key={w.id} value={w.id}>{w.name}</option>)}</select></label>
          </div>
          <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
            <ItemSelect value={line.item_id} onChange={(v) => setLine({ ...line, item_id: v })} />
            <input dir="ltr" inputMode="decimal" style={{ width: 90 }} placeholder={t('vt.f.qty')} value={line.quantity} onChange={(e) => setLine({ ...line, quantity: e.currentTarget.value })} />
            <button className="mx-btn" disabled={!line.item_id || !Number(line.quantity)} onClick={() => { setOpen({ ...open, lines: [...open.lines, line] }); setLine({ item_id: '', quantity: '' }); }}>{t('vt.w.add')}</button>
          </div>
          {open.lines.length > 0 && <small>{t('vt.w.toLoad', { n: open.lines.length })}</small>}
          <div className="mx-row" style={{ gap: 6 }}>
            <button className="mx-btn main" disabled={!open.van || !open.src} onClick={() => void run(api(`${base}/van/days`, { method: 'POST',
              body: { warehouse_id: open.van, source_warehouse_id: open.src, lines: open.lines } }), () => setOpen(null))}>{t('vt.w.loadOpen')}</button>
            <button className="mx-btn" onClick={() => setOpen(null)}>{t('common.cancel')}</button>
          </div>
        </div>
      )}
      <table className="mx-table">
        <thead><tr><th>#</th><th>{t('vt.f.date')}</th><th>{t('vt.w.van')}</th><th>{t('vt.w.salesman')}</th><th>{t('vt.f.status')}</th><th>{t('vt.w.sales')}</th><th>{t('vt.w.collected')}</th><th>{t('vt.w.diffs')}</th></tr></thead>
        <tbody>{rows.map((d) => (
          <tr key={d.id}><td dir="ltr">{d.number}</td><td>{formatDate(d.day)}</td><td>{d.warehouse}</td><td>{d.user_name ?? '—'}</td>
            <td><span className={`mx-pill ${d.status === 'open' ? 'warn' : 'on'}`}>{t(`vt.w.st.${d.status}`)}</span></td>
            <td dir="ltr">{d.summary.sales ?? ''} {d.summary.sales_total ? `· ${amt(d.summary.sales_total)}` : ''}</td><td dir="ltr">{d.summary.collected ? amt(d.summary.collected) : ''}</td>
            <td style={{ whiteSpace: 'normal' }}>{(d.summary.differences ?? []).map((x) => `${x.item_name} ${Number(x.difference) > 0 ? '+' : ''}${qty(x.difference)}`).join(' · ')}</td></tr>))}
          {rows.length === 0 && <tr><td colSpan={8} className="mx-empty">{t('vt.none')}</td></tr>}</tbody>
      </table>
    </section>
  );
}

function Commission() {
  const { t } = useTranslation();
  const base = useBase();
  const [range, setRange] = useState({ from: `${today().slice(0, 8)}01`, to: today() });
  const [rows] = useLoad<{ user_id: string; full_name: string; currency: string; invoices: number; net: string; collected: string; commission_pct: string;
    commission_on_sales: string; commission_on_collected: string }[]>(base && `${base}/van/commission?from=${range.from}&to=${range.to}`, []);
  return (
    <section className="mx-box">
      <h3 className="mx-row" style={{ gap: 6 }}>{t('vt.m.commission')}
        <input type="date" value={range.from} onChange={(e) => setRange({ ...range, from: e.currentTarget.value })} />
        <input type="date" value={range.to} onChange={(e) => setRange({ ...range, to: e.currentTarget.value })} /></h3>
      <table className="mx-table">
        <thead><tr><th>{t('vt.w.salesman')}</th><th>{t('vt.w.invoices')}</th><th>{t('vt.w.net')}</th><th>{t('vt.w.collected')}</th><th>%</th><th>{t('vt.w.onSales')}</th><th>{t('vt.w.onCollected')}</th></tr></thead>
        <tbody>{rows.map((r) => (
          <tr key={r.user_id + r.currency}><td>{r.full_name}</td><td dir="ltr">{r.invoices}</td><td dir="ltr">{amt(r.net)} {r.currency}</td><td dir="ltr">{amt(r.collected)}</td>
            <td dir="ltr">{Number(r.commission_pct)}</td><td dir="ltr"><b>{amt(r.commission_on_sales)}</b></td><td dir="ltr"><b>{amt(r.commission_on_collected)}</b></td></tr>))}
          {rows.length === 0 && <tr><td colSpan={7} className="mx-empty">{t('vt.none')}</td></tr>}</tbody>
      </table>
      <small style={{ padding: 6, display: 'block' }}>{t('vt.w.commissionNote')}</small>
    </section>
  );
}

function Vans({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [rows, reload] = useLoad<WhV[]>(base && `${base}/verticals/warehouses`, []);
  const users = useUsers();
  const [msg, run] = useRun(reload);
  const manage = can(me, 'stock.manage');
  const put = (w: WhV, body: Record<string, unknown>) => void run(api(`${base}/warehouses/${w.id}/vertical`, { method: 'PUT', body }));
  return (
    <section className="mx-box">
      <h3>{t('vt.m.vans')}</h3>
      <Flash m={msg} />
      <table className="mx-table">
        <thead><tr><th>{t('vt.f.warehouse')}</th><th>{t('vt.w.isVan')}</th><th>{t('vt.w.salesman')}</th><th>{t('vt.w.isShowroom')}</th><th>{t('vt.f.value')}</th></tr></thead>
        <tbody>{rows.map((w) => (
          <tr key={w.id} style={{ opacity: w.is_active ? 1 : 0.5 }}><td><b>{w.name}</b></td>
            <td><input type="checkbox" disabled={!manage} checked={w.is_van} onChange={(e) => put(w, { is_van: e.currentTarget.checked })} aria-label={t('vt.w.isVan')} /></td>
            <td>{w.is_van ? <select disabled={!manage} value={w.van_user_id ?? ''} onChange={(e) => put(w, { van_user_id: e.currentTarget.value })} aria-label={t('vt.w.salesman')}>
              <option value="">—</option>{users.map((u) => <option key={u.id} value={u.id}>{u.full_name}</option>)}</select> : '—'}</td>
            <td><input type="checkbox" disabled={!manage} checked={w.is_showroom} onChange={(e) => put(w, { is_showroom: e.currentTarget.checked })} aria-label={t('vt.w.isShowroom')} /></td>
            <td dir="ltr">{amt(w.value)}</td></tr>))}</tbody>
      </table>
    </section>
  );
}

// ======================= pharmacy =======================

interface Drug {
  id: string; code: string; name: string; barcode: string; generic_name: string; active_ingredient: string; strength: string; dosage_form: string;
  rx_required: boolean; controlled: boolean; sale_price: string; sale_currency: string | null; in_stock: string; next_expiry: string | null; alternatives: number;
}
function DrugFlags({ d }: { d: { rx_required: boolean; controlled: boolean } }) {
  const { t } = useTranslation();
  return <>{d.controlled && <span className="mx-pill off">{t('vt.p.controlled')}</span>}{d.rx_required && !d.controlled && <span className="mx-pill warn">{t('vt.p.rx')}</span>}</>;
}
function DrugSearch() {
  const { t } = useTranslation();
  const base = useBase();
  const [q, setQ] = useState('');
  const [rows, setRows] = useState<Drug[] | null>(null);
  const [alt, setAlt] = useState<{ of: string; rows: (Drug & { same_strength: boolean })[] } | null>(null);
  const [msg, run] = useRun();
  const search = () => q.trim().length >= 2 && void run(api<Drug[]>(`${base}/pharma/search?q=${encodeURIComponent(q.trim())}`), (x) => { setRows(x as Drug[]); setAlt(null); });
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <div className="mx-row" style={{ gap: 6 }}>
        <input autoFocus placeholder={t('vt.p.searchPh')} value={q} onChange={(e) => setQ(e.currentTarget.value)} onKeyDown={(e) => { if (e.key === 'Enter') search(); }} style={{ minWidth: 300 }} />
        <button className="mx-btn main" onClick={search}>{t('vt.p.search')}</button>
      </div>
      <Flash m={msg && !msg.ok ? msg : null} />
      {rows && <section className="mx-box"><div style={{ overflowX: 'auto' }}><table className="mx-table">
        <thead><tr><th>{t('vt.f.item')}</th><th>{t('vt.p.generic')}</th><th>{t('vt.p.strength')}</th><th>{t('vt.f.price')}</th><th>{t('vt.p.stock')}</th><th>{t('vt.p.nextExpiry')}</th><th /></tr></thead>
        <tbody>{rows.map((d) => (
          <tr key={d.id}><td><b>{d.name}</b> <DrugFlags d={d} /><br /><small dir="ltr">{d.code} {d.barcode}</small></td>
            <td>{d.generic_name}<br /><small>{d.active_ingredient}</small></td><td>{d.strength} {d.dosage_form}</td>
            <td dir="ltr">{amt(d.sale_price)} {d.sale_currency ?? ''}</td>
            <td dir="ltr"><span className={`mx-pill ${Number(d.in_stock) > 0 ? 'on' : 'off'}`}>{qty(d.in_stock)}</span></td>
            <td>{d.next_expiry ? formatDate(d.next_expiry) : '—'}</td>
            <td>{d.alternatives > 0 && <button className="mx-btn sm" onClick={() => api<(Drug & { same_strength: boolean })[]>(`${base}/pharma/alternatives?item_id=${d.id}`)
              .then((x) => setAlt({ of: d.name, rows: x })).catch(() => setAlt(null))}>{t('vt.p.alternatives', { n: d.alternatives })}</button>}</td></tr>))}
          {rows.length === 0 && <tr><td colSpan={7} className="mx-empty">{t('vt.none')}</td></tr>}</tbody>
      </table></div></section>}
      {alt && <section className="mx-box"><h3>{t('vt.p.altOf', { name: alt.of })}</h3><table className="mx-table"><tbody>
        {alt.rows.map((d) => <tr key={d.id}><td><b>{d.name}</b> <DrugFlags d={d} /></td><td>{d.strength} {d.dosage_form}{d.same_strength && <span className="mx-pill on" style={{ marginInlineStart: 4 }}>{t('vt.p.same')}</span>}</td>
          <td dir="ltr">{amt(d.sale_price)}</td><td dir="ltr"><span className={`mx-pill ${Number(d.in_stock) > 0 ? 'on' : 'off'}`}>{qty(d.in_stock)}</span></td></tr>)}</tbody></table></section>}
    </div>
  );
}

function Expiry() {
  const { t } = useTranslation();
  const base = useBase();
  const [days, setDays] = useState('');
  const [d] = useLoad<{ days: number; expired: number; expiring: number; block_expired: boolean; lots: { lot_no: string; expiry: string; days_left: number; item_code: string;
    item_name: string; warehouse: string; quantity: string; value: string; expired: boolean }[] } | null>(base && `${base}/pharma/expiry${days ? `?days=${days}` : ''}`, null);
  return (
    <section className="mx-box">
      <h3 className="mx-row" style={{ gap: 8, flexWrap: 'wrap' }}>{t('vt.m.expiry')}
        <span className="mx-pill off">{t('vt.p.expiredN', { n: d?.expired ?? 0 })}</span><span className="mx-pill warn">{t('vt.p.expiringN', { n: d?.expiring ?? 0, d: d?.days ?? '' })}</span>
        <span className="mx-sp" /><label className="mx-row" style={{ gap: 4 }}>{t('vt.p.within')}<input dir="ltr" style={{ width: 60 }} value={days} placeholder={String(d?.days ?? 90)}
          onChange={(e) => setDays(e.currentTarget.value.replace(/\D/g, ''))} /></label></h3>
      {d && !d.block_expired && <div className="mx-flash bad">{t('vt.p.notBlocked')}</div>}
      <div style={{ overflowX: 'auto' }}><table className="mx-table">
        <thead><tr><th>{t('vt.f.item')}</th><th>{t('vt.p.lot')}</th><th>{t('vt.p.expiry')}</th><th>{t('vt.p.daysLeft')}</th><th>{t('vt.f.warehouse')}</th><th>{t('vt.f.qty')}</th><th>{t('vt.f.value')}</th></tr></thead>
        <tbody>{(d?.lots ?? []).map((l, i) => (
          <tr key={i} style={{ background: l.expired ? 'var(--mx-bad-bg, #fde2e2)' : undefined }}><td>{l.item_code} · {l.item_name}</td><td dir="ltr">{l.lot_no}</td><td>{formatDate(l.expiry)}</td>
            <td dir="ltr"><span className={`mx-pill ${l.expired ? 'off' : l.days_left <= 30 ? 'warn' : ''}`}>{l.expired ? t('vt.p.expired') : l.days_left}</span></td>
            <td>{l.warehouse}</td><td dir="ltr">{qty(l.quantity)}</td><td dir="ltr">{amt(l.value)}</td></tr>))}
          {d && d.lots.length === 0 && <tr><td colSpan={7} className="mx-empty">{t('vt.p.noExpiry')}</td></tr>}</tbody>
      </table></div>
      <small style={{ padding: 6, display: 'block' }}>{t('vt.p.expiryNote')}</small>
    </section>
  );
}

function Controlled() {
  const { t } = useTranslation();
  const base = useBase();
  const [range, setRange] = useState({ from: `${today().slice(0, 8)}01`, to: today(), all: false });
  const [rows] = useLoad<{ pos_number: number; created_at: string; customer_name: string; item_name: string; controlled: boolean; quantity: string;
    rx: { doctor?: string; number?: string; patient?: string }; sold_by: string | null }[]>(base && `${base}/pharma/controlled?from=${range.from}&to=${range.to}${range.all ? '&all=1' : ''}`, []);
  return (
    <section className="mx-box">
      <h3 className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>{t('vt.m.controlled')}
        <input type="date" value={range.from} onChange={(e) => setRange({ ...range, from: e.currentTarget.value })} />
        <input type="date" value={range.to} onChange={(e) => setRange({ ...range, to: e.currentTarget.value })} />
        <label className="mx-row" style={{ gap: 4 }}><input type="checkbox" checked={range.all} onChange={(e) => setRange({ ...range, all: e.currentTarget.checked })} />{t('vt.p.withRx')}</label>
        <span className="mx-sp" /><button className="mx-btn sm" onClick={() => printPage()}>{t('vt.print')}</button></h3>
      <div style={{ overflowX: 'auto' }} className="print-area"><table className="mx-table">
        <thead><tr><th>{t('vt.f.date')}</th><th>{t('vt.p.sale')}</th><th>{t('vt.f.item')}</th><th>{t('vt.f.qty')}</th><th>{t('vt.p.patient')}</th><th>{t('vt.p.doctor')}</th><th>{t('vt.p.rxNo')}</th><th>{t('vt.p.by')}</th></tr></thead>
        <tbody>{rows.map((r, i) => (
          <tr key={i}><td>{formatDateTime(r.created_at)}</td><td dir="ltr">#{r.pos_number}</td><td>{r.item_name} {r.controlled && <span className="mx-pill off">{t('vt.p.controlled')}</span>}</td>
            <td dir="ltr">{qty(r.quantity)}</td><td>{r.rx?.patient || r.customer_name}</td><td>{r.rx?.doctor ?? ''}</td><td dir="ltr">{r.rx?.number ?? ''}</td><td>{r.sold_by ?? ''}</td></tr>))}
          {rows.length === 0 && <tr><td colSpan={8} className="mx-empty">{t('vt.none')}</td></tr>}</tbody>
      </table></div>
    </section>
  );
}

function PharmaSettings({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [d, reload] = useLoad<{ block_expired: boolean; expiry_alert_days: number } | null>(base && `${base}/pharma/settings`, null);
  const [msg, run] = useRun(reload);
  const [days, setDays] = useState('');
  if (!d) return null;
  const manage = can(me, 'stock.manage');
  return (
    <section className="mx-box" style={{ maxWidth: 600 }}>
      <h3>{t('vt.m.psettings')}</h3>
      <Flash m={msg} />
      <div style={{ padding: 8, display: 'flex', flexDirection: 'column', gap: 8 }}>
        <label className="mx-row" style={{ gap: 6 }}><input type="checkbox" disabled={!manage} checked={d.block_expired}
          onChange={(e) => void run(api(`${base}/pharma/settings`, { method: 'PUT', body: { block_expired: e.currentTarget.checked } }))} />{t('vt.p.block')}</label>
        <label className="mx-row" style={{ gap: 6 }}>{t('vt.p.alertDays')}
          <input dir="ltr" style={{ width: 70 }} disabled={!manage} placeholder={String(d.expiry_alert_days)} value={days} onChange={(e) => setDays(e.currentTarget.value.replace(/\D/g, ''))} />
          <button className="mx-btn sm" disabled={!manage || !days} onClick={() => void run(api(`${base}/pharma/settings`, { method: 'PUT', body: { expiry_alert_days: Number(days) } }), () => setDays(''))}>{t('common.save')}</button></label>
        <small>{t('vt.p.settingsNote')}</small>
      </div>
    </section>
  );
}

interface Claim { id: string; number: number; pos_number: number; order_date: string; insurer: string; patient: string; policy_no: string; currency: string; amount: string; status: string; note: string }
function Claims({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [status, setStatus] = useState('open');
  const [d, reload] = useLoad<{ claims: Claim[]; open_totals: { insurer: string; currency: string; status: string; claims: number; amount: string }[] } | null>(base && `${base}/pharma/claims?status=${status === 'all' ? '' : status}`, null);
  const [methods] = useLoad<Method[]>(base && `${base}/payment-methods`, []);
  const [msg, run] = useRun(reload);
  const [settle, setSettle] = useState<{ id: string; method: string; currency: string } | null>(null);
  const act = can(me, 'collections.create');
  return (
    <section className="mx-box">
      <h3 className="mx-row" style={{ gap: 6 }}>{t('vt.m.claims')}
        <select value={status} onChange={(e) => setStatus(e.currentTarget.value)} aria-label={t('vt.f.status')}>
          {['open', 'pending', 'submitted', 'paid', 'rejected', 'all'].map((s) => <option key={s} value={s}>{t(`vt.cl.${s}`)}</option>)}</select></h3>
      <Flash m={msg} />
      {(d?.open_totals ?? []).length > 0 && <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap' }}>
        {d!.open_totals.map((x, i) => <span key={i} className="mx-pill warn">{x.insurer} · {t(`vt.cl.${x.status}`)} · {x.claims} · {amt(x.amount)} {x.currency}</span>)}</div>}
      {settle && (
        <div className="mx-row" style={{ gap: 6, padding: 6 }}>
          <select value={settle.method} onChange={(e) => setSettle({ ...settle, method: e.currentTarget.value })} aria-label={t('vt.cl.paidInto')}>
            <option value="">{t('vt.cl.paidInto')}</option>{methods.filter((m) => m.is_active && m.currency === settle.currency).map((m) => <option key={m.id} value={m.id}>{m.name}</option>)}</select>
          <button className="mx-btn main" disabled={!settle.method} onClick={() => void run(api(`${base}/pharma/claims/${settle.id}/settle`, { method: 'POST', body: { method_id: settle.method } }), () => setSettle(null))}>{t('vt.cl.settle')}</button>
          <button className="mx-btn" onClick={() => setSettle(null)}>{t('common.cancel')}</button>
        </div>
      )}
      <div style={{ overflowX: 'auto' }}><table className="mx-table">
        <thead><tr><th>#</th><th>{t('vt.f.date')}</th><th>{t('vt.cl.insurer')}</th><th>{t('vt.p.patient')}</th><th>{t('vt.cl.policy')}</th><th>{t('vt.f.total')}</th><th>{t('vt.f.status')}</th><th /></tr></thead>
        <tbody>{(d?.claims ?? []).map((c) => (
          <tr key={c.id}><td dir="ltr">{c.number}<br /><small>POS #{c.pos_number}</small></td><td>{formatDate(c.order_date)}</td><td>{c.insurer}</td><td>{c.patient}</td><td dir="ltr">{c.policy_no}</td>
            <td dir="ltr">{amt(c.amount)} {c.currency}</td><td>{t(`vt.cl.${c.status}`)}{c.note && <><br /><small>{c.note}</small></>}</td>
            <td style={{ whiteSpace: 'nowrap' }}>{act && (c.status === 'pending' || c.status === 'submitted') && <>
              {c.status === 'pending' && <button className="mx-btn sm" onClick={() => void run(api(`${base}/pharma/claims/${c.id}/submit`, { method: 'POST', body: {} }))}>{t('vt.cl.submit')}</button>}{' '}
              <button className="mx-btn sm main" onClick={() => setSettle({ id: c.id, method: '', currency: c.currency })}>{t('vt.cl.settle')}</button>{' '}
              <button className="mx-btn sm" onClick={() => void run(api(`${base}/pharma/claims/${c.id}/reject`, { method: 'POST', body: {} }))}>{t('vt.cl.reject')}</button></>}</td></tr>))}
          {d && d.claims.length === 0 && <tr><td colSpan={8} className="mx-empty">{t('vt.none')}</td></tr>}</tbody>
      </table></div>
      <small style={{ padding: 6, display: 'block' }}>{t('vt.cl.note')}</small>
    </section>
  );
}

// ======================= restaurant setup =======================

interface Table { id: string; name: string; area: string; seats: number; x: number; y: number; shape: string; is_active: boolean }
function Tables({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [rows, reload] = useLoad<Table[]>(base && `${base}/rest/tables`, []);
  const [msg, run] = useRun(reload);
  const [f, setF] = useState({ name: '', area: '', seats: '4' });
  const manage = can(me, 'pos.manage');
  const patch = (x: Table, body: Record<string, unknown>) => void run(api(`${base}/rest/tables/${x.id}`, { method: 'PATCH', body }));
  return (
    <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap', alignItems: 'flex-start' }}>
      <section className="mx-box" style={{ flex: '1 1 420px' }}>
        <h3>{t('vt.m.tables')}</h3>
        <Flash m={msg} />
        {manage && <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap' }}>
          <input placeholder={t('vt.f.name')} style={{ width: 90 }} value={f.name} onChange={(e) => setF({ ...f, name: e.currentTarget.value })} />
          <input placeholder={t('vt.r.area')} style={{ width: 110 }} value={f.area} onChange={(e) => setF({ ...f, area: e.currentTarget.value })} />
          <input dir="ltr" style={{ width: 60 }} value={f.seats} onChange={(e) => setF({ ...f, seats: e.currentTarget.value.replace(/\D/g, '') })} aria-label={t('vt.r.seats')} />
          <button className="mx-btn main" disabled={!f.name.trim()} onClick={() => void run(api(`${base}/rest/tables`, { method: 'POST', body: { name: f.name, area: f.area, seats: Number(f.seats || 4) } }),
            () => setF({ name: '', area: f.area, seats: f.seats }))}>{t('vt.new')}</button></div>}
        <table className="mx-table">
          <thead><tr><th>{t('vt.f.name')}</th><th>{t('vt.r.area')}</th><th>{t('vt.r.seats')}</th><th>X / Y</th><th /></tr></thead>
          <tbody>{rows.map((x) => (
            <tr key={x.id} style={{ opacity: x.is_active ? 1 : 0.5 }}><td><b>{x.name}</b></td><td>{x.area}</td><td dir="ltr">{x.seats}</td>
              <td dir="ltr">{manage ? <>
                <input style={{ width: 44 }} defaultValue={x.x} onBlur={(e) => Number(e.currentTarget.value) !== x.x && patch(x, { x: Math.min(100, Number(e.currentTarget.value) || 0) })} aria-label="x" />
                <input style={{ width: 44 }} defaultValue={x.y} onBlur={(e) => Number(e.currentTarget.value) !== x.y && patch(x, { y: Math.min(100, Number(e.currentTarget.value) || 0) })} aria-label="y" /></> : `${x.x} / ${x.y}`}</td>
              <td>{manage && <button className="mx-btn sm" onClick={() => void run(api(`${base}/rest/tables/${x.id}`, { method: 'DELETE' }))}>{t('vt.del')}</button>}</td></tr>))}</tbody>
        </table>
      </section>
      <section className="mx-box" style={{ flex: '1 1 360px' }}>
        <h3>{t('vt.r.plan')}</h3>
        <div className="vt-floor" style={{ position: 'relative', height: 320, background: 'var(--mx-panel, #f4f6f9)', borderRadius: 6, overflow: 'hidden' }}>
          {rows.filter((x) => x.is_active).map((x) => (
            <div key={x.id} style={{ position: 'absolute', insetInlineStart: `${Math.min(x.x, 88)}%`, top: `${Math.min(x.y, 85)}%`, width: x.shape === 'long' ? 90 : 56, height: 44,
              borderRadius: x.shape === 'round' ? 22 : 6, background: '#fff', border: '1px solid #c8d1dc', display: 'grid', placeItems: 'center', fontSize: 12 }}>
              <b>{x.name}</b><small>{x.seats}</small></div>))}
        </div>
      </section>
    </div>
  );
}

interface Modifier { id: string; name: string; grp: string; price: string; item_ids: string[]; category_ids: string[]; sort: number; is_active: boolean }
function Modifiers({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [rows, reload] = useLoad<Modifier[]>(base && `${base}/rest/modifiers`, []);
  const [msg, run] = useRun(reload);
  const [f, setF] = useState({ name: '', grp: '', price: '' });
  const manage = can(me, 'pos.manage');
  return (
    <section className="mx-box">
      <h3>{t('vt.m.modifiers')}</h3>
      <Flash m={msg} />
      {manage && <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap' }}>
        <input placeholder={t('vt.f.name')} value={f.name} onChange={(e) => setF({ ...f, name: e.currentTarget.value })} />
        <input placeholder={t('vt.r.group')} style={{ width: 110 }} value={f.grp} onChange={(e) => setF({ ...f, grp: e.currentTarget.value })} />
        <input dir="ltr" inputMode="decimal" style={{ width: 80 }} placeholder={t('vt.f.price')} value={f.price} onChange={(e) => setF({ ...f, price: e.currentTarget.value })} />
        <button className="mx-btn main" disabled={!f.name.trim()} onClick={() => void run(api(`${base}/rest/modifiers`, { method: 'POST', body: { name: f.name, grp: f.grp, price: f.price || '0' } }),
          () => setF({ name: '', grp: f.grp, price: '' }))}>{t('vt.new')}</button></div>}
      <table className="mx-table">
        <thead><tr><th>{t('vt.r.group')}</th><th>{t('vt.f.name')}</th><th>{t('vt.f.price')}</th><th>{t('vt.r.appliesTo')}</th><th>{t('vt.f.active')}</th><th /></tr></thead>
        <tbody>{rows.map((m) => (
          <tr key={m.id}><td>{m.grp}</td><td><b>{m.name}</b></td><td dir="ltr">{Number(m.price) ? `+${amt(m.price)}` : '0'}</td>
            <td>{m.item_ids.length || m.category_ids.length ? t('vt.r.some', { n: m.item_ids.length + m.category_ids.length }) : t('vt.r.all')}</td>
            <td><input type="checkbox" disabled={!manage} checked={m.is_active} onChange={(e) => void run(api(`${base}/rest/modifiers/${m.id}`, { method: 'PATCH', body: { is_active: e.currentTarget.checked } }))} aria-label={t('vt.f.active')} /></td>
            <td>{manage && <button className="mx-btn sm" onClick={() => void run(api(`${base}/rest/modifiers/${m.id}`, { method: 'DELETE' }))}>{t('vt.del')}</button>}</td></tr>))}</tbody>
      </table>
      <small style={{ padding: 6, display: 'block' }}>{t('vt.r.modNote')}</small>
    </section>
  );
}

function FoodCost() {
  const { t } = useTranslation();
  const base = useBase();
  const [range, setRange] = useState({ from: `${today().slice(0, 8)}01`, to: today() });
  const [d] = useLoad<{ days: { day: string; orders: number; food_sales: string; food_cost: string; food_cost_pct: string | null }[];
    dishes: { item_id: string; name: string; quantity: string; sales: string; recipe_cost: string }[] } | null>(base && `${base}/rest/food-cost?from=${range.from}&to=${range.to}`, null);
  const tot = (d?.days ?? []).reduce((a, x) => ({ s: a.s + Number(x.food_sales), c: a.c + Number(x.food_cost) }), { s: 0, c: 0 });
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <section className="mx-box">
        <h3 className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>{t('vt.m.foodcost')}
          <input type="date" value={range.from} onChange={(e) => setRange({ ...range, from: e.currentTarget.value })} />
          <input type="date" value={range.to} onChange={(e) => setRange({ ...range, to: e.currentTarget.value })} />
          <span className="mx-sp" /><b dir="ltr">{tot.s > 0 ? `${(tot.c / tot.s * 100).toFixed(1)} %` : '—'}</b></h3>
        <table className="mx-table">
          <thead><tr><th>{t('vt.f.date')}</th><th>{t('vt.r.orders')}</th><th>{t('vt.r.sales')}</th><th>{t('vt.r.cost')}</th><th>%</th></tr></thead>
          <tbody>{(d?.days ?? []).map((x) => (
            <tr key={x.day}><td>{formatDate(x.day)}</td><td dir="ltr">{x.orders}</td><td dir="ltr">{amt(x.food_sales)}</td><td dir="ltr">{amt(x.food_cost)}</td>
              <td dir="ltr"><span className={`mx-pill ${Number(x.food_cost_pct) > 40 ? 'off' : Number(x.food_cost_pct) > 32 ? 'warn' : 'on'}`}>{x.food_cost_pct ?? '—'}</span></td></tr>))}
            {d && d.days.length === 0 && <tr><td colSpan={5} className="mx-empty">{t('vt.none')}</td></tr>}</tbody>
        </table>
      </section>
      <section className="mx-box">
        <h3>{t('vt.r.dishes')}</h3>
        <table className="mx-table">
          <thead><tr><th>{t('vt.f.item')}</th><th>{t('vt.f.qty')}</th><th>{t('vt.r.sales')}</th><th>{t('vt.r.recipeCost')}</th><th>%</th></tr></thead>
          <tbody>{(d?.dishes ?? []).map((x) => {
            const unitSale = Number(x.sales) / Math.max(1, Number(x.quantity));
            const pct = unitSale > 0 ? Number(x.recipe_cost) / unitSale * 100 : 0;
            return <tr key={x.item_id}><td>{x.name}</td><td dir="ltr">{qty(x.quantity)}</td><td dir="ltr">{amt(x.sales)}</td><td dir="ltr">{amt(x.recipe_cost, 3)}</td><td dir="ltr">{pct ? pct.toFixed(1) : '—'}</td></tr>;
          })}</tbody>
        </table>
        <small style={{ padding: 6, display: 'block' }}>{t('vt.r.recipeNote')}</small>
      </section>
    </div>
  );
}

function RestSettings({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [d, reload] = useLoad<{ service_charge_pct: string } | null>(base && `${base}/rest/settings`, null);
  const [msg, run] = useRun(reload);
  const [v, setV] = useState('');
  return (
    <section className="mx-box" style={{ maxWidth: 560 }}>
      <h3>{t('vt.m.rsettings')}</h3>
      <Flash m={msg} />
      <div className="mx-row" style={{ gap: 6, padding: 8 }}>{t('vt.r.service')}
        <input dir="ltr" style={{ width: 70 }} placeholder={d?.service_charge_pct ?? '0'} value={v} onChange={(e) => setV(e.currentTarget.value.replace(/[^\d.]/g, ''))} disabled={!can(me, 'pos.manage')} /> %
        <button className="mx-btn sm" disabled={!v || !can(me, 'pos.manage')} onClick={() => void run(api(`${base}/rest/settings`, { method: 'PUT', body: { service_charge_pct: v } }), () => setV(''))}>{t('common.save')}</button>
      </div>
      <div style={{ padding: 8, display: 'flex', gap: 6, flexWrap: 'wrap' }}>
        <button className="mx-btn main" onClick={() => { window.location.hash = '#/resto'; }}>{t('vt.t.resto')}</button>
        <button className="mx-btn" onClick={() => { window.location.hash = '#/kds'; }}>{t('vt.t.kds')}</button>
      </div>
      <small style={{ padding: 8, display: 'block' }}>{t('vt.r.settingsNote')}</small>
    </section>
  );
}

// ======================= business fields of an item =======================

interface ItemV {
  made_to_order: boolean; options: Opt[]; price_per_m2: string | null; kitchen_station: string; generic_name: string; active_ingredient: string;
  strength: string; dosage_form: string; rx_required: boolean; controlled: boolean; warranty_months: number; tracking: string;
}
function ItemFields({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [item, setItem] = useState('');
  const [v, reload] = useLoad<ItemV | null>(base && item && `${base}/items/${item}/vertical`, null);
  const [msg, run] = useRun(reload);
  const [optText, setOptText] = useState<string | null>(null);
  const manage = can(me, 'stock.manage');
  const put = (body: Record<string, unknown>) => void run(api(`${base}/items/${item}/vertical`, { method: 'PUT', body }));
  const optsToText = (o: Opt[]) => o.map((x) => `${x.name}: ${x.values.map((y) => (Number(y.extra) ? `${y.label}=${y.extra}` : y.label)).join(', ')}`).join('\n');
  const textToOpts = (s: string): Opt[] => s.split('\n').map((l) => l.trim()).filter(Boolean).map((l) => {
    const [name, rest = ''] = l.split(':');
    return { name: name.trim(), values: rest.split(',').map((x) => x.trim()).filter(Boolean).map((x) => { const [label, extra = '0'] = x.split('='); return { label: label.trim(), extra: extra.trim() }; }) };
  }).filter((o) => o.name && o.values.length);
  const Text = ({ k, label }: { k: keyof ItemV; label: string }) => (
    <label className="mx-row" style={{ gap: 4 }}>{label}<input disabled={!manage} defaultValue={String(v?.[k] ?? '')} key={`${item}-${k}-${String(v?.[k] ?? '')}`}
      onBlur={(e) => e.currentTarget.value !== String(v?.[k] ?? '') && put({ [k]: e.currentTarget.value })} /></label>);
  return (
    <section className="mx-box" style={{ maxWidth: 900 }}>
      <h3>{t('vt.m.itemfields')}</h3>
      <div style={{ padding: 6 }}><ItemSelect value={item} onChange={(x) => { setItem(x); setOptText(null); }} /></div>
      <Flash m={msg} />
      {v && <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(260px, 1fr))', gap: 10, padding: 6 }}>
        <fieldset className="mx-box" style={{ padding: 8 }}><legend>{t('vt.g.furniture')}</legend>
          <label className="mx-row" style={{ gap: 4 }}><input type="checkbox" disabled={!manage} checked={v.made_to_order} onChange={(e) => put({ made_to_order: e.currentTarget.checked })} />{t('vt.fu.mto')}</label>
          <label className="mx-row" style={{ gap: 4 }}>{t('vt.fu.ppm')}<input dir="ltr" style={{ width: 90 }} disabled={!manage} defaultValue={v.price_per_m2 ?? ''} key={`${item}-ppm-${v.price_per_m2}`}
            onBlur={(e) => e.currentTarget.value !== (v.price_per_m2 ?? '') && put({ price_per_m2: e.currentTarget.value })} /></label>
          <label style={{ display: 'block' }}>{t('vt.fu.options')}<br />
            <textarea rows={4} style={{ width: '100%' }} disabled={!manage} value={optText ?? optsToText(v.options)} onChange={(e) => setOptText(e.currentTarget.value)} placeholder="Fabric: Linen, Velvet=150" /></label>
          {optText !== null && <button className="mx-btn sm main" onClick={() => { put({ options: textToOpts(optText) }); setOptText(null); }}>{t('common.save')}</button>}
          <small>{t('vt.fu.optionsHint')}</small>
        </fieldset>
        <fieldset className="mx-box" style={{ padding: 8 }}><legend>{t('vt.g.pharmacy')}</legend>
          <Text k="generic_name" label={t('vt.p.generic')} />
          <Text k="active_ingredient" label={t('vt.p.ingredient')} />
          <Text k="strength" label={t('vt.p.strength')} />
          <Text k="dosage_form" label={t('vt.p.form')} />
          <label className="mx-row" style={{ gap: 4 }}><input type="checkbox" disabled={!manage} checked={v.rx_required} onChange={(e) => put({ rx_required: e.currentTarget.checked })} />{t('vt.p.rx')}</label>
          <label className="mx-row" style={{ gap: 4 }}><input type="checkbox" disabled={!manage} checked={v.controlled} onChange={(e) => put({ controlled: e.currentTarget.checked })} />{t('vt.p.controlled')}</label>
          {v.tracking !== 'lot' && <small className="mx-pill warn">{t('vt.p.needLots')}</small>}
        </fieldset>
        <fieldset className="mx-box" style={{ padding: 8 }}><legend>{t('vt.g.restaurant')} · {t('vt.g.electronics')}</legend>
          <Text k="kitchen_station" label={t('vt.r.station')} />
          <small>{t('vt.r.stationHint')}</small><br />
          <small>{t('vt.e.trackingNow', { tracking: t(`vt.tr.${v.tracking}`, { defaultValue: v.tracking }), m: v.warranty_months })}</small>
        </fieldset>
      </div>}
    </section>
  );
}
