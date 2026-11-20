import { useEffect, useMemo, useState, type ReactElement } from 'react';
import { useTranslation } from 'react-i18next';
import { api, can, type Me } from '../../api';
import { useCompany } from '../../company';
import { formatAmount, formatDate, today } from '../../util';
import { openScreen } from '../../nav';
import Chatter from '../../components/Chatter';
import { useBase, useLoad, useRun, type Warehouse } from '../wh/Wh';

// Manufacturing pro (Magic-style screens): orders (confirm → reserve → work orders → produce, partial with
// backorder, scrap, subcontracting), bills of materials (versions, phantom kits, by-products, structure and cost),
// work centers and routings, MRP, quality (points, checks, alerts), lot traceability, reports and scrap.

type Msg = { ok: boolean; text: string } | null;
function Flash({ m }: { m: Msg }) {
  return m ? <div className={`mx-flash ${m.ok ? 'good' : 'bad'}`}>{m.text}</div> : null;
}

export interface MO {
  id: string; number: number; bom_id: string; bom_name: string; bom_version: number; item_id: string; item_code: string; item_name: string; unit: string;
  quantity: string; warehouse_id: string; warehouse: string; order_date: string; status: string; components_value: string; labour: string; overhead: string;
  scrap_qty: string; unit_cost: string; notes: string; bom_type: string; deadline: string | null; origin: string; backorder_of: string | null;
  backorder_of_number: number | null; ops_cost: string; service_cost: string; byproducts_value: string; expected_cost: string; standard_cost: string;
  variance: string; components_sent: boolean; service_bill_id: string | null; late: boolean; availability: string | null; workorders: number;
  workorders_done: number; checks_todo: number;
}
interface MOLine {
  id: string; item_id: string; item_code: string; item_name: string; unit: string; tracking: string; planned_qty: string; consumed_qty: string; value: string;
  reserved_qty: string; sent_qty: string; in_stock: string; bins: { code: string; quantity: string; lot_no: string | null }[];
}
interface Byp { id: string; item_code: string; item_name: string; unit: string; planned_qty: string; produced_qty: string; cost_share: string; value: string }
export interface WO {
  id: string; order_id: string; order_number: number; seq: number; name: string; workcenter_id: string; workcenter_code: string; workcenter: string;
  instructions: string; planned_min: string; actual_min: string; status: string; qty_done: string; cost: string; operator: string | null;
  started_at: string | null; finished_at: string | null; item_code: string; item_name: string; unit: string; quantity: string; order_status: string;
  deadline: string | null; running_since: string | null; worked_min: string; checks_todo: number;
}
export interface QCheck {
  id: string; point_name: string; trigger_on: string; test_type: string; norm: string | null; tol_min: string | null; tol_max: string | null; unit: string;
  instructions: string; order_number: number | null; workorder: string | null; receipt_number: number | null; item_code: string | null; item_name: string | null;
  status: string; measured: string | null; note: string; checked_by_name: string | null; checked_at: string | null; alert_id: string | null;
}
interface MODetail { order: MO; lines: MOLine[]; byproducts: Byp[]; workorders: WO[]; checks: QCheck[]; related: { id: string; number: number; status: string; quantity: string; relation: string }[]; scraps: { id: string; number: number; item_code: string; quantity: string; notes: string }[] }
interface BOM {
  id: string; item_id: string; item_code: string; item_name: string; unit: string; name: string; output_qty: string; labour_cost: string; overhead_cost: string;
  notes: string; is_active: boolean; components: number; code: string; version: number; family_id: string; bom_type: string; routing_id: string | null;
  routing_name: string | null; subcontractor_id: string | null; subcontractor_name: string | null; subcontract_price: string; lead_days: number;
  version_note: string; standard_cost: string; versions: number;
}
interface WC { id: string; code: string; name: string; capacity: string; hours_per_day: string; days_per_week: number; efficiency: string; cost_per_hour: string; setup_min: string; notes: string; is_active: boolean; open_workorders: number; running: number }
interface ItemLite { id: string; code: string; name: string; kind: string; is_active: boolean; unit: string }

export const moCode = (n: number) => `MO${String(n).padStart(5, '0')}`;
const q = (v: string | null | undefined) => (v == null || v === '' ? '' : String(Number(v)));
const amt = (v: string | number | null | undefined, d = 2) => formatAmount(String(v ?? 0), d);
const num = (s: string) => s.replace(/[^\d.\-]/g, '');

const MODES = ['orders', 'boms', 'workcenters', 'mrp', 'quality', 'trace', 'reports', 'scrap'] as const;
type Mode = typeof MODES[number];

export default function Mfg({ me, initial }: { me: Me; initial?: string }) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<Mode>((MODES as readonly string[]).includes(initial ?? '') ? initial as Mode : 'orders');
  return (
    <div className="mx-col">
      <div className="mx-strip"><b>{t(`mf.m.${mode}`)}</b><span className="mx-sp" /><small style={{ opacity: 0.8 }}>{t(`mf.help.${mode}`)}</small>
        <button className="mx-btn sm" onClick={() => openScreen('shopfloor')}>{t('mf.openFloor')}</button>
      </div>
      <div className="mx-modes" role="tablist">
        {MODES.map((m) => <button key={m} role="tab" className="mx-mode" aria-selected={mode === m} onClick={() => setMode(m)}>{t(`mf.m.${m}`)}</button>)}
      </div>
      <div style={{ flex: 1, minHeight: 0, overflow: 'auto', padding: 6 }}>
        {mode === 'orders' && <Orders me={me} />}
        {mode === 'boms' && <Boms me={me} />}
        {mode === 'workcenters' && <Workcenters me={me} />}
        {mode === 'mrp' && <Mrp me={me} />}
        {mode === 'quality' && <Quality me={me} />}
        {mode === 'trace' && <Trace />}
        {mode === 'reports' && <Reports />}
        {mode === 'scrap' && <Scrap me={me} />}
      </div>
    </div>
  );
}

function useItems(): ItemLite[] {
  const base = useBase();
  const [items] = useLoad<ItemLite[]>(base && `${base}/items`, []);
  return useMemo(() => items.filter((i) => i.kind === 'stock' && i.is_active), [items]);
}

function ItemSelect({ value, onChange, items, label, exclude }: { value: string; onChange: (v: string) => void; items: ItemLite[]; label?: string; exclude?: string }) {
  const { t } = useTranslation();
  const sel = (
    <select value={value} onChange={(e) => onChange(e.currentTarget.value)} aria-label={label ?? t('mf.item')}>
      <option value="">{t('mf.choose')}</option>
      {items.filter((i) => i.id !== exclude).map((i) => <option key={i.id} value={i.id}>{i.code} · {i.name}</option>)}
    </select>
  );
  return label ? <label className="mx-fld">{label}{sel}</label> : sel;
}

function StatusPill({ s }: { s: string }) {
  const { t } = useTranslation();
  const color: Record<string, string> = { draft: '#6b7280', confirmed: '#1d4ed8', progress: '#a15c00', done: '#0f7b4b', cancelled: '#b42318', ready: '#0f7b4b', pending: '#6b7280', paused: '#a15c00', todo: '#a15c00', pass: '#0f7b4b', fail: '#b42318', new: '#b42318' };
  return <span className="mx-pill" style={{ color: color[s] ?? undefined, borderColor: color[s] ?? undefined }}>{t(`mf.st.${s}`, { defaultValue: s })}</span>;
}

/** Lot / serial input: "L1" for one lot, "S1, S2, S3" for serials (one unit each). */
function lotsFor(text: string, qty: string): { lot_no: string; quantity: string }[] | undefined {
  const parts = text.split(/[,\s]+/).map((s) => s.trim()).filter(Boolean);
  if (parts.length === 0) return undefined;
  if (parts.length === 1) return [{ lot_no: parts[0], quantity: qty }];
  return parts.map((p) => ({ lot_no: p, quantity: '1' }));
}

// ======================= orders =======================

function Orders({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const { company } = useCompany();
  const bc = company?.base_currency ?? '';
  const [status, setStatus] = useState('open');
  const [orders, reload] = useLoad<MO[]>(base && `${base}/mfg/orders?status=${status === 'all' ? '' : status}`, []);
  const [boms] = useLoad<BOM[]>(base && `${base}/mfg/boms`, []);
  const [whs] = useLoad<Warehouse[]>(base && `${base}/wh/warehouses`, []);
  const [sel, setSel] = useState<MODetail | null>(null);
  const [nf, setNf] = useState({ bom_id: '', quantity: '1', warehouse_id: '', date: today(), deadline: '', confirm: true });
  const [msg, run] = useRun(reload);
  const open = (id: string) => base && api<MODetail>(`${base}/mfg/orders/${id}`).then(setSel).catch(() => setSel(null));
  const refresh = () => { reload(); if (sel) void open(sel.order.id); };
  return (
    <div className="mx-row" style={{ gap: 8, alignItems: 'flex-start', flexWrap: 'wrap' }}>
      <section className="mx-box" style={{ flex: '999 1 560px', minWidth: 0 }}>
        <h3>{t('mf.orders.title')}</h3>
        <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
          <label className="mx-fld">{t('mf.status')}<select value={status} onChange={(e) => setStatus(e.currentTarget.value)}>
            {['open', 'draft', 'confirmed', 'progress', 'done', 'cancelled', 'all'].map((s) => <option key={s} value={s}>{t(`mf.st.${s}`)}</option>)}</select></label>
        </div>
        {can(me, 'stock.create') && (
          <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end', borderTop: '1px solid var(--mx-line, #dde3ea)' }}>
            <label className="mx-fld">{t('mf.bom')}<select value={nf.bom_id} onChange={(e) => setNf({ ...nf, bom_id: e.currentTarget.value })}>
              <option value="">{t('mf.choose')}</option>
              {boms.filter((b) => b.is_active && b.bom_type !== 'phantom').map((b) => <option key={b.id} value={b.id}>{b.item_code} · {b.name} (v{b.version}){b.bom_type === 'subcontract' ? ` · ${t('mf.bt.subcontract')}` : ''}</option>)}</select></label>
            <label className="mx-fld">{t('mf.qty')}<input dir="ltr" style={{ width: 70 }} value={nf.quantity} onChange={(e) => setNf({ ...nf, quantity: num(e.currentTarget.value) })} /></label>
            <label className="mx-fld">{t('mf.warehouse')}<select value={nf.warehouse_id} onChange={(e) => setNf({ ...nf, warehouse_id: e.currentTarget.value })}>
              <option value="">{t('mf.mainWh')}</option>{whs.filter((w) => !w.is_transit).map((w) => <option key={w.id} value={w.id}>{w.name}</option>)}</select></label>
            <label className="mx-fld">{t('mf.date')}<input type="date" dir="ltr" value={nf.date} onChange={(e) => setNf({ ...nf, date: e.currentTarget.value })} /></label>
            <label className="mx-fld">{t('mf.deadline')}<input type="date" dir="ltr" value={nf.deadline} onChange={(e) => setNf({ ...nf, deadline: e.currentTarget.value })} /></label>
            <label className="mx-fld" style={{ flexDirection: 'row', alignItems: 'center', gap: 4 }}><input type="checkbox" checked={nf.confirm} onChange={(e) => setNf({ ...nf, confirm: e.currentTarget.checked })} />{t('mf.orders.confirmNow')}</label>
            <button className="mx-btn main" disabled={!nf.bom_id || !Number(nf.quantity)} onClick={() => void run(api<MODetail>(`${base}/mfg/orders`, { method: 'POST', body: nf }), (x) => setSel(x as MODetail))}>{t('mf.orders.create')}</button>
          </div>
        )}
        <Flash m={sel ? null : msg} />
        <div style={{ overflowX: 'auto' }}>
          <table className="mx-table">
            <thead><tr><th>{t('mf.number')}</th><th>{t('mf.date')}</th><th>{t('mf.item')}</th><th className="num">{t('mf.qty')}</th><th>{t('mf.warehouse')}</th>
              <th>{t('mf.deadline')}</th><th>{t('mf.components')}</th><th>{t('mf.wos')}</th><th>{t('mf.origin')}</th><th>{t('mf.status')}</th><th className="num">{t('mf.unitCost')}</th></tr></thead>
            <tbody>
              {orders.map((o) => (
                <tr key={o.id} className={sel?.order.id === o.id ? 'sel' : ''} onClick={() => void open(o.id)} style={{ cursor: 'pointer' }}>
                  <td dir="ltr">{moCode(o.number)}</td><td>{formatDate(o.order_date)}</td><td>{o.item_code} · {o.item_name}</td>
                  <td className="num" dir="ltr">{q(o.quantity)} {o.unit}</td><td>{o.warehouse}</td>
                  <td className={o.late ? 'red' : ''}>{o.deadline ? formatDate(o.deadline) : ''}</td>
                  <td>{o.availability ? t(`mf.av.${o.availability}`) : o.bom_type === 'subcontract' && o.components_sent ? t('mf.sub.sent') : ''}</td>
                  <td dir="ltr">{o.workorders ? `${o.workorders_done}/${o.workorders}` : ''}</td>
                  <td>{o.backorder_of_number ? `${t('mf.backorderOf')} ${moCode(o.backorder_of_number)}` : o.origin}</td>
                  <td><StatusPill s={o.status} />{o.checks_todo > 0 && <small className="red"> · {t('mf.q.todoN', { n: o.checks_todo })}</small>}</td>
                  <td className="num">{o.status === 'done' ? `${amt(o.unit_cost, 4)} ${bc}` : ''}</td>
                </tr>))}
              {orders.length === 0 && <tr><td colSpan={11} className="mx-empty">{t('mf.none')}</td></tr>}
            </tbody>
          </table>
        </div>
      </section>
      {sel && <OrderPanel me={me} d={sel} bc={bc} onChange={refresh} onOpen={(id) => void open(id)} onClose={() => setSel(null)} />}
    </div>
  );
}

function OrderPanel({ me, d, bc, onChange, onOpen, onClose }: { me: Me; d: MODetail; bc: string; onChange: () => void; onOpen: (id: string) => void; onClose: () => void }) {
  const { t } = useTranslation();
  const base = useBase();
  const o = d.order;
  const [msg, run] = useRun(onChange);
  const [pq, setPq] = useState(q(o.quantity));
  const [scrapQ, setScrapQ] = useState('0');
  const [lot, setLot] = useState('');
  const [cons, setCons] = useState<Record<string, string>>({});
  const [sc, setSc] = useState({ item_id: '', quantity: '', reason: '' });
  useEffect(() => { setPq(q(o.quantity)); setScrapQ('0'); setLot(''); setCons({}); }, [o.id, o.quantity]);
  const manage = can(me, 'stock.manage');
  const doer = can(me, 'stock.create');
  const active = ['draft', 'confirmed', 'progress'].includes(o.status);
  const good = String(Math.max(0, Number(pq || 0) - Number(scrapQ || 0)));
  const post = (path: string, body: unknown = {}) => run(api(`${base}/mfg/orders/${o.id}/${path}`, { method: 'POST', body }));
  const actual = Number(o.components_value) + Number(o.labour) + Number(o.ops_cost) + Number(o.overhead) + Number(o.service_cost) - Number(o.byproducts_value);
  return (
    <section className="mx-box" style={{ flex: '1 1 460px', minWidth: 0 }}>
      <h3 style={{ display: 'flex', gap: 6, alignItems: 'center', flexWrap: 'wrap' }}>
        <span dir="ltr">{moCode(o.number)}</span> · {o.item_name} × <span dir="ltr">{q(o.quantity)} {o.unit}</span> <StatusPill s={o.status} />
        <span className="mx-sp" /><button className="mx-btn sm" onClick={onClose}>{t('mf.close')}</button>
      </h3>
      <div style={{ padding: 6, display: 'flex', flexDirection: 'column', gap: 8 }}>
        <small>{t('mf.bom')}: {o.bom_name} v{o.bom_version} · {t(`mf.bt.${o.bom_type}`)} · {o.warehouse}{o.deadline ? ` · ${t('mf.deadline')} ${formatDate(o.deadline)}` : ''}
          {o.backorder_of && <> · <a href="#" onClick={(e) => { e.preventDefault(); onOpen(o.backorder_of as string); }}>{t('mf.backorderOf')} {moCode(o.backorder_of_number ?? 0)}</a></>}</small>
        {d.related.length > 0 && <small>{d.related.map((r) => <a key={r.id} href="#" style={{ marginInlineEnd: 8 }} onClick={(e) => { e.preventDefault(); onOpen(r.id); }}>{t(`mf.rel.${r.relation}`)} {moCode(r.number)} ({q(r.quantity)}, {t(`mf.st.${r.status}`)})</a>)}</small>}

        {active && (
          <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
            {o.status === 'draft' && doer && <button className="mx-btn main" onClick={() => void post('confirm')}>{t('mf.act.confirm')}</button>}
            {o.status !== 'draft' && doer && !o.components_sent && <button className="mx-btn" onClick={() => void post('reserve')}>{t('mf.act.reserve')}</button>}
            {o.bom_type === 'subcontract' && o.status !== 'draft' && !o.components_sent && doer && <button className="mx-btn" onClick={() => void post('send-components', { date: today() })}>{t('mf.act.send')}</button>}
            {can(me, 'stock.void') && !o.components_sent && <button className="mx-btn sm" onClick={() => void post('cancel')}>{t('mf.act.cancel')}</button>}
          </div>
        )}

        <table className="mx-table">
          <thead><tr><th>{t('mf.component')}</th><th className="num">{t('mf.planned')}</th><th className="num">{t('mf.reserved')}</th><th className="num">{t('mf.inStock')}</th>
            <th className="num">{t('mf.used')}</th>{o.status === 'done' && <th className="num">{t('mf.costCol')}</th>}<th>{t('mf.bins')}</th></tr></thead>
          <tbody>{d.lines.map((l) => (
            <tr key={l.id}><td>{l.item_code} · {l.item_name}</td><td className="num" dir="ltr">{q(l.planned_qty)} {l.unit}</td>
              <td className={`num ${Number(l.reserved_qty) + Number(l.sent_qty) < Number(l.planned_qty) && o.status !== 'draft' && active ? 'red' : ''}`} dir="ltr">{Number(l.sent_qty) > 0 ? `${q(l.sent_qty)} ${t('mf.sub.sentShort')}` : q(l.reserved_qty)}</td>
              <td className="num" dir="ltr">{q(l.in_stock)}</td>
              <td className="num">{active && manage ? <input dir="ltr" style={{ width: 70 }} value={cons[l.id] ?? ''} placeholder={q(l.consumed_qty)} onChange={(e) => setCons({ ...cons, [l.id]: num(e.currentTarget.value) })} /> : q(l.consumed_qty)}</td>
              {o.status === 'done' && <td className="num">{amt(l.value)}</td>}
              <td dir="ltr"><small>{l.bins.map((b) => `${b.code}${b.lot_no ? `[${b.lot_no}]` : ''}:${q(b.quantity)}`).join(' · ')}</small></td></tr>))}</tbody>
        </table>

        {d.byproducts.length > 0 && <table className="mx-table"><thead><tr><th>{t('mf.byproduct')}</th><th className="num">{t('mf.planned')}</th><th className="num">{t('mf.costShare')}</th>{o.status === 'done' && <th className="num">{t('mf.value')}</th>}</tr></thead>
          <tbody>{d.byproducts.map((b) => <tr key={b.id}><td>{b.item_code} · {b.item_name}</td><td className="num" dir="ltr">{q(o.status === 'done' ? b.produced_qty : b.planned_qty)} {b.unit}</td><td className="num">{q(b.cost_share)} %</td>{o.status === 'done' && <td className="num">{amt(b.value)}</td>}</tr>)}</tbody></table>}

        {d.workorders.length > 0 && (
          <table className="mx-table"><thead><tr><th>#</th><th>{t('mf.operation')}</th><th>{t('mf.workcenter')}</th><th className="num">{t('mf.plannedMin')}</th><th className="num">{t('mf.actualMin')}</th><th>{t('mf.status')}</th><th /></tr></thead>
            <tbody>{d.workorders.map((w) => (
              <tr key={w.id}><td>{w.seq}</td><td>{w.name}</td><td>{w.workcenter}</td><td className="num">{q(w.planned_min)}</td><td className="num">{q(w.status === 'done' ? w.actual_min : w.worked_min)}</td>
                <td><StatusPill s={w.status} />{w.checks_todo > 0 && <small className="red"> {t('mf.q.todoN', { n: w.checks_todo })}</small>}</td>
                <td>{doer && ['ready', 'pending', 'paused'].includes(w.status) && <button className="mx-btn sm" onClick={() => void run(api(`${base}/mfg/workorders/${w.id}/start`, { method: 'POST', body: {} }))}>{t('mf.wo.start')}</button>}
                  {doer && w.status === 'progress' && <button className="mx-btn sm" onClick={() => void run(api(`${base}/mfg/workorders/${w.id}/pause`, { method: 'POST', body: {} }))}>{t('mf.wo.pause')}</button>}
                  {doer && !['done', 'cancelled'].includes(w.status) && <button className="mx-btn sm" onClick={() => void run(api(`${base}/mfg/workorders/${w.id}/finish`, { method: 'POST', body: {} }))}>{t('mf.wo.finish')}</button>}</td></tr>))}</tbody></table>
        )}

        {d.checks.length > 0 && <ChecksTable checks={d.checks} me={me} onDone={onChange} />}

        {active && manage && (
          <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap', alignItems: 'end', borderTop: '1px solid var(--mx-line)', paddingTop: 6 }}>
            <label className="mx-fld">{t('mf.produceQty')}<input dir="ltr" style={{ width: 70 }} value={pq} onChange={(e) => setPq(num(e.currentTarget.value))} /></label>
            <label className="mx-fld">{t('mf.scrapQty')}<input dir="ltr" style={{ width: 60 }} value={scrapQ} onChange={(e) => setScrapQ(num(e.currentTarget.value))} /></label>
            <label className="mx-fld">{t('mf.lotNo')}<input dir="ltr" style={{ width: 130 }} value={lot} placeholder={t('mf.lotHint')} onChange={(e) => setLot(e.currentTarget.value)} /></label>
            <button className="mx-btn main" disabled={!Number(good)} onClick={() => void post('produce', {
              quantity: pq, scrap_qty: scrapQ || '0', lots: lotsFor(lot, good),
              lines: Object.entries(cons).filter(([, v]) => v !== '').map(([line_id, quantity]) => ({ line_id, quantity })),
            })}>{o.bom_type === 'subcontract' ? t('mf.act.receive') : t('mf.act.produce')}</button>
            {Number(pq) < Number(o.quantity) && Number(pq) > 0 && <small>{t('mf.backorderHint', { n: Number(o.quantity) - Number(pq) })}</small>}
          </div>
        )}
        {o.status !== 'cancelled' && manage && (
          <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap', alignItems: 'end' }}>
            <label className="mx-fld">{t('mf.scrapItem')}<select value={sc.item_id} onChange={(e) => setSc({ ...sc, item_id: e.currentTarget.value })}>
              <option value="">{t('mf.choose')}</option><option value={o.item_id}>{o.item_code} · {o.item_name}</option>
              {d.lines.map((l) => <option key={l.id} value={l.item_id}>{l.item_code} · {l.item_name}</option>)}</select></label>
            <label className="mx-fld">{t('mf.qty')}<input dir="ltr" style={{ width: 60 }} value={sc.quantity} onChange={(e) => setSc({ ...sc, quantity: num(e.currentTarget.value) })} /></label>
            <label className="mx-fld">{t('mf.reason')}<input value={sc.reason} onChange={(e) => setSc({ ...sc, reason: e.currentTarget.value })} /></label>
            <button className="mx-btn sm" disabled={!sc.item_id || !Number(sc.quantity) || !sc.reason.trim()} onClick={() => void run(api(`${base}/mfg/orders/${o.id}/scrap`, { method: 'POST', body: sc }), () => setSc({ item_id: '', quantity: '', reason: '' }))}>{t('mf.scrapIt')}</button>
          </div>
        )}
        {d.scraps.length > 0 && <small>{t('mf.scrapped')}: {d.scraps.map((s) => `${s.item_code} × ${q(String(Math.abs(Number(s.quantity))))} (${s.notes})`).join(' · ')}</small>}

        {o.status === 'done' && (
          <table className="mx-table"><tbody>
            <tr><td>{t('mf.cost.components')}</td><td className="num">{amt(o.components_value)} {bc}</td></tr>
            <tr><td>{t('mf.cost.ops')}</td><td className="num">{amt(Number(o.ops_cost) + Number(o.labour))}</td></tr>
            <tr><td>{t('mf.cost.overhead')}</td><td className="num">{amt(o.overhead)}</td></tr>
            {Number(o.service_cost) > 0 && <tr><td>{t('mf.cost.service')}</td><td className="num">{amt(o.service_cost)}</td></tr>}
            {Number(o.byproducts_value) > 0 && <tr><td>{t('mf.cost.byproducts')}</td><td className="num">−{amt(o.byproducts_value)}</td></tr>}
            <tr><td><b>{t('mf.cost.actual')}</b></td><td className="num"><b>{amt(actual)}</b></td></tr>
            <tr><td>{t('mf.cost.expected')}</td><td className="num">{amt(o.expected_cost)}</td></tr>
            {Number(o.standard_cost) > 0 && <tr><td>{t('mf.cost.variance', { std: amt(o.standard_cost, 4) })}</td><td className={`num ${Number(o.variance) > 0 ? 'red' : ''}`}>{amt(o.variance)}</td></tr>}
            <tr><td>{t('mf.unitCost')}</td><td className="num">{amt(o.unit_cost, 4)} {bc}{Number(o.scrap_qty) > 0 ? ` · ${t('mf.scrapQty')} ${q(o.scrap_qty)}` : ''}</td></tr>
          </tbody></table>
        )}
        <Flash m={msg} />
        <Chatter type="mfg_order" id={o.id} readOnly={!manage} />
      </div>
    </section>
  );
}

export function ChecksTable({ checks, me, onDone, big }: { checks: QCheck[]; me: Me; onDone: () => void; big?: boolean }) {
  const { t } = useTranslation();
  const base = useBase();
  const [vals, setVals] = useState<Record<string, string>>({});
  const [msg, run] = useRun(onDone);
  const doer = can(me, 'stock.create');
  const rec = (c: QCheck, body: Record<string, string>) => run(api(`${base}/mfg/quality-checks/${c.id}`, { method: 'POST', body: { ...body, note: vals[`n${c.id}`] ?? '' } }));
  const bs = big ? { minHeight: 44, minWidth: 80 } : undefined;
  return (
    <div>
      <table className="mx-table">
        <thead><tr><th>{t('mf.q.check')}</th><th>{t('mf.q.on')}</th><th>{t('mf.q.norm')}</th><th>{t('mf.status')}</th><th /></tr></thead>
        <tbody>{checks.map((c) => (
          <tr key={c.id}>
            <td>{c.point_name}{c.instructions ? <div><small>{c.instructions}</small></div> : null}</td>
            <td>{c.workorder ?? (c.receipt_number ? `GR ${c.receipt_number}` : c.order_number ? moCode(c.order_number) : '')}{c.item_code ? ` · ${c.item_code}` : ''}</td>
            <td dir="ltr">{c.test_type === 'measure' ? `${q(c.tol_min)} – ${q(c.tol_max)} ${c.unit}` : t('mf.q.passfail')}</td>
            <td><StatusPill s={c.status} />{c.measured != null && <span dir="ltr"> {q(c.measured)} {c.unit}</span>}{c.alert_id && <small className="red"> · {t('mf.q.alerted')}</small>}</td>
            <td>{c.status === 'todo' && doer && (
              <span className="mx-row" style={{ gap: 4, flexWrap: 'wrap' }}>
                {c.test_type === 'measure' ? <>
                  <input dir="ltr" style={{ width: 70, minHeight: bs?.minHeight }} aria-label={t('mf.q.measured')} value={vals[c.id] ?? ''} onChange={(e) => setVals({ ...vals, [c.id]: num(e.currentTarget.value) })} />
                  <button className="mx-btn sm main" style={bs} disabled={!vals[c.id]} onClick={() => void rec(c, { measured: vals[c.id] })}>{t('mf.q.record')}</button>
                </> : <>
                  <button className="mx-btn sm main" style={bs} onClick={() => void rec(c, { result: 'pass' })}>{t('mf.q.pass')}</button>
                  <button className="mx-btn sm" style={{ ...bs, color: 'var(--mx-red)' }} onClick={() => void rec(c, { result: 'fail' })}>{t('mf.q.fail')}</button>
                </>}
                <input style={{ width: 110, minHeight: bs?.minHeight }} placeholder={t('mf.q.note')} aria-label={t('mf.q.note')} value={vals[`n${c.id}`] ?? ''} onChange={(e) => setVals({ ...vals, [`n${c.id}`]: e.currentTarget.value })} />
              </span>)}</td>
          </tr>))}</tbody>
      </table>
      <Flash m={msg} />
    </div>
  );
}

// ======================= bills of materials =======================

interface BomEdit {
  id: string; item_id: string; name: string; code: string; bom_type: string; routing_id: string; subcontractor_id: string; subcontract_price: string;
  output_qty: string; labour_cost: string; overhead_cost: string; lead_days: string; version_note: string; is_active: boolean;
  lines: { item_id: string; quantity: string }[]; byproducts: { item_id: string; quantity: string; cost_share: string }[];
}
interface TreeNode { bom_name: string; bom_type: string; version: number; item_code: string; item_name: string; unit: string; quantity: string; cost: string; unit_cost: string; labour: string; overhead: string; service: string;
  components: { item_code: string; item_name: string; unit: string; quantity: string; unit_cost: string; cost: string; source: string; sub?: TreeNode }[] | null;
  operations: { name: string; workcenter: string; minutes: string; cost: string }[] | null; byproducts: { item_code: string; item_name: string; quantity: string; cost: string; cost_share: string }[] | null }

function Boms({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const items = useItems();
  const [boms, reload] = useLoad<BOM[]>(base && `${base}/mfg/boms`, []);
  const [routings] = useLoad<{ id: string; name: string }[]>(base && `${base}/mfg/routings`, []);
  const [sups] = useLoad<{ id: string; name: string }[]>(base && `${base}/suppliers`, []);
  const blank: BomEdit = { id: '', item_id: '', name: '', code: '', bom_type: 'normal', routing_id: '', subcontractor_id: '', subcontract_price: '0', output_qty: '1',
    labour_cost: '0', overhead_cost: '0', lead_days: '0', version_note: '', is_active: true, lines: [{ item_id: '', quantity: '1' }], byproducts: [] };
  const [b, setB] = useState<BomEdit>(blank);
  const [versions, setVersions] = useState<{ id: string; version: number; is_active: boolean; version_note: string }[]>([]);
  const [tree, setTree] = useState<TreeNode | null>(null);
  const [treeQty, setTreeQty] = useState('1');
  const [filter, setFilter] = useState('');
  const [msg, run] = useRun(reload);
  const manage = can(me, 'stock.manage');
  const edit = (id: string) => base && api<{ bom: BOM; lines: { item_id: string; quantity: string }[]; byproducts: { item_id: string; quantity: string; cost_share: string }[]; versions: typeof versions }>(`${base}/mfg/boms/${id}`).then((d) => {
    setB({ id: d.bom.id, item_id: d.bom.item_id, name: d.bom.name, code: d.bom.code, bom_type: d.bom.bom_type, routing_id: d.bom.routing_id ?? '', subcontractor_id: d.bom.subcontractor_id ?? '',
      subcontract_price: q(d.bom.subcontract_price), output_qty: q(d.bom.output_qty), labour_cost: q(d.bom.labour_cost), overhead_cost: q(d.bom.overhead_cost), lead_days: String(d.bom.lead_days),
      version_note: d.bom.version_note, is_active: d.bom.is_active, lines: d.lines.map((l) => ({ item_id: l.item_id, quantity: q(l.quantity) })),
      byproducts: d.byproducts.map((y) => ({ item_id: y.item_id, quantity: q(y.quantity), cost_share: q(y.cost_share) })) });
    setVersions(d.versions); setTree(null);
  });
  const showTree = (id: string, qty = treeQty) => base && api<TreeNode>(`${base}/mfg/boms/${id}/structure?qty=${encodeURIComponent(qty || '1')}`).then(setTree).catch(() => setTree(null));
  const save = () => {
    const { id, lines, byproducts, lead_days, ...rest } = b;
    const body = { ...rest, lead_days: Number(lead_days) || 0, lines: lines.filter((l) => l.item_id && Number(l.quantity) > 0), byproducts: byproducts.filter((y) => y.item_id && Number(y.quantity) > 0) };
    void run(api<{ bom: BOM }>(id ? `${base}/mfg/boms/${id}` : `${base}/mfg/boms`, { method: id ? 'PUT' : 'POST', body }), (x) => void edit((x as { bom: BOM }).bom.id));
  };
  const shown = boms.filter((x) => !filter || `${x.item_code} ${x.item_name} ${x.name} ${x.code}`.toLowerCase().includes(filter.toLowerCase()));
  return (
    <div className="mx-row" style={{ gap: 8, alignItems: 'flex-start', flexWrap: 'wrap' }}>
      <section className="mx-box" style={{ flex: '1 1 420px', minWidth: 0 }}>
        <h3>{t('mf.m.boms')}</h3>
        <div className="mx-row" style={{ gap: 6, padding: 6 }}><input placeholder={t('mf.search')} aria-label={t('mf.search')} value={filter} onChange={(e) => setFilter(e.currentTarget.value)} />
          {manage && <button className="mx-btn sm" onClick={() => { setB(blank); setVersions([]); setTree(null); }}>{t('mf.boms.new')}</button>}</div>
        <div style={{ overflowX: 'auto' }}>
          <table className="mx-table">
            <thead><tr><th>{t('mf.item')}</th><th>{t('mf.name')}</th><th>{t('mf.boms.type')}</th><th>v</th><th className="num">{t('mf.components')}</th><th>{t('mf.routing')}</th><th>{t('mf.status')}</th></tr></thead>
            <tbody>{shown.map((x) => (
              <tr key={x.id} className={b.id === x.id ? 'sel' : ''} style={{ cursor: 'pointer', opacity: x.is_active ? 1 : 0.6 }} onClick={() => void edit(x.id)}>
                <td>{x.item_code} · {x.item_name}</td><td>{x.name}{x.code ? ` [${x.code}]` : ''}</td><td>{t(`mf.bt.${x.bom_type}`)}</td><td>{x.version}{x.versions > 1 ? `/${x.versions}` : ''}</td>
                <td className="num">{x.components}</td><td>{x.routing_name ?? x.subcontractor_name ?? ''}</td><td>{x.is_active ? t('mf.active') : t('mf.inactive')}</td></tr>))}
              {shown.length === 0 && <tr><td colSpan={7} className="mx-empty">{t('mf.none')}</td></tr>}</tbody>
          </table>
        </div>
      </section>
      <section className="mx-box" style={{ flex: '1 1 520px', minWidth: 0 }}>
        <h3>{b.id ? `${t('mf.boms.edit')} · v${versions.find((v) => v.id === b.id)?.version ?? ''}` : t('mf.boms.new')}</h3>
        <div style={{ padding: 6, display: 'flex', flexDirection: 'column', gap: 6 }}>
          <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap', alignItems: 'end' }}>
            <ItemSelect label={t('mf.makes')} value={b.item_id} onChange={(v) => setB({ ...b, item_id: v })} items={items} />
            <label className="mx-fld">{t('mf.name')}<input value={b.name} onChange={(e) => setB({ ...b, name: e.currentTarget.value })} /></label>
            <label className="mx-fld">{t('mf.code')}<input dir="ltr" style={{ width: 80 }} value={b.code} onChange={(e) => setB({ ...b, code: e.currentTarget.value })} /></label>
            <label className="mx-fld">{t('mf.boms.type')}<select value={b.bom_type} onChange={(e) => setB({ ...b, bom_type: e.currentTarget.value })}>
              {['normal', 'phantom', 'subcontract'].map((x) => <option key={x} value={x}>{t(`mf.bt.${x}`)}</option>)}</select></label>
            <label className="mx-fld">{t('mf.output')}<input dir="ltr" style={{ width: 60 }} value={b.output_qty} onChange={(e) => setB({ ...b, output_qty: num(e.currentTarget.value) })} /></label>
          </div>
          <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap', alignItems: 'end' }}>
            {b.bom_type === 'normal' && <label className="mx-fld">{t('mf.routing')}<select value={b.routing_id} onChange={(e) => setB({ ...b, routing_id: e.currentTarget.value })}>
              <option value="">{t('mf.noRouting')}</option>{routings.map((r) => <option key={r.id} value={r.id}>{r.name}</option>)}</select></label>}
            {b.bom_type === 'subcontract' && <>
              <label className="mx-fld">{t('mf.subcontractor')}<select value={b.subcontractor_id} onChange={(e) => setB({ ...b, subcontractor_id: e.currentTarget.value })}>
                <option value="">{t('mf.choose')}</option>{sups.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}</select></label>
              <label className="mx-fld">{t('mf.subPrice')}<input dir="ltr" style={{ width: 80 }} value={b.subcontract_price} onChange={(e) => setB({ ...b, subcontract_price: num(e.currentTarget.value) })} /></label>
            </>}
            <label className="mx-fld">{t('mf.labour')}<input dir="ltr" style={{ width: 70 }} value={b.labour_cost} onChange={(e) => setB({ ...b, labour_cost: num(e.currentTarget.value) })} /></label>
            <label className="mx-fld">{t('mf.overhead')}<input dir="ltr" style={{ width: 70 }} value={b.overhead_cost} onChange={(e) => setB({ ...b, overhead_cost: num(e.currentTarget.value) })} /></label>
            <label className="mx-fld">{t('mf.leadDays')}<input dir="ltr" style={{ width: 50 }} value={b.lead_days} onChange={(e) => setB({ ...b, lead_days: num(e.currentTarget.value) })} /></label>
            <label className="mx-fld" style={{ flexDirection: 'row', alignItems: 'center', gap: 4 }}><input type="checkbox" checked={b.is_active} onChange={(e) => setB({ ...b, is_active: e.currentTarget.checked })} />{t('mf.active')}</label>
          </div>
          <small>{t(`mf.btHelp.${b.bom_type}`)}</small>
          <table className="mx-table"><thead><tr><th>{t('mf.component')}</th><th>{t('mf.qty')}</th><th /></tr></thead>
            <tbody>{b.lines.map((l, i) => (
              <tr key={i}><td><ItemSelect value={l.item_id} exclude={b.item_id} items={items} onChange={(v) => setB({ ...b, lines: b.lines.map((x, j) => (j === i ? { ...x, item_id: v } : x)) })} /></td>
                <td><input dir="ltr" style={{ width: 70 }} aria-label={t('mf.qty')} value={l.quantity} onChange={(e) => setB({ ...b, lines: b.lines.map((x, j) => (j === i ? { ...x, quantity: num(e.currentTarget.value) } : x)) })} /></td>
                <td><button className="mx-btn sm" aria-label={t('mf.remove')} onClick={() => setB({ ...b, lines: b.lines.filter((_, j) => j !== i) })}>×</button></td></tr>))}</tbody></table>
          <table className="mx-table"><thead><tr><th>{t('mf.byproduct')}</th><th>{t('mf.qty')}</th><th>{t('mf.costShare')} %</th><th /></tr></thead>
            <tbody>{b.byproducts.map((y, i) => (
              <tr key={i}><td><ItemSelect value={y.item_id} exclude={b.item_id} items={items} onChange={(v) => setB({ ...b, byproducts: b.byproducts.map((x, j) => (j === i ? { ...x, item_id: v } : x)) })} /></td>
                <td><input dir="ltr" style={{ width: 60 }} aria-label={t('mf.qty')} value={y.quantity} onChange={(e) => setB({ ...b, byproducts: b.byproducts.map((x, j) => (j === i ? { ...x, quantity: num(e.currentTarget.value) } : x)) })} /></td>
                <td><input dir="ltr" style={{ width: 50 }} aria-label={t('mf.costShare')} value={y.cost_share} onChange={(e) => setB({ ...b, byproducts: b.byproducts.map((x, j) => (j === i ? { ...x, cost_share: num(e.currentTarget.value) } : x)) })} /></td>
                <td><button className="mx-btn sm" aria-label={t('mf.remove')} onClick={() => setB({ ...b, byproducts: b.byproducts.filter((_, j) => j !== i) })}>×</button></td></tr>))}</tbody></table>
          {manage && (
            <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
              <button className="mx-btn sm" onClick={() => setB({ ...b, lines: [...b.lines, { item_id: '', quantity: '1' }] })}>{t('mf.addComponent')}</button>
              <button className="mx-btn sm" onClick={() => setB({ ...b, byproducts: [...b.byproducts, { item_id: '', quantity: '1', cost_share: '0' }] })}>{t('mf.addByproduct')}</button>
              <span className="mx-sp" />
              <button className="mx-btn main" disabled={!b.item_id || !b.lines.some((l) => l.item_id) || (b.bom_type === 'subcontract' && !b.subcontractor_id)} onClick={save}>{t('mf.save')}</button>
            </div>
          )}
          {b.id && (
            <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap', alignItems: 'end', borderTop: '1px solid var(--mx-line)', paddingTop: 6 }}>
              <span>{t('mf.boms.versions')}:</span>
              {versions.map((v) => <button key={v.id} className={`mx-btn sm ${v.id === b.id ? 'main' : ''}`} title={v.version_note} onClick={() => void edit(v.id)}>v{v.version}{v.is_active ? ' ✓' : ''}</button>)}
              {manage && <button className="mx-btn sm" onClick={() => void run(api<{ bom: BOM }>(`${base}/mfg/boms/${b.id}/new-version`, { method: 'POST', body: { version_note: b.version_note } }), (x) => void edit((x as { bom: BOM }).bom.id))}>{t('mf.boms.newVersion')}</button>}
              {manage && !b.is_active && <button className="mx-btn sm" onClick={() => void run(api(`${base}/mfg/boms/${b.id}/activate`, { method: 'POST', body: {} }), () => void edit(b.id))}>{t('mf.boms.activate')}</button>}
              <span className="mx-sp" />
              <label className="mx-fld">{t('mf.qty')}<input dir="ltr" style={{ width: 60 }} value={treeQty} onChange={(e) => setTreeQty(num(e.currentTarget.value))} /></label>
              <button className="mx-btn sm" onClick={() => void showTree(b.id)}>{t('mf.boms.structure')}</button>
              {manage && <button className="mx-btn sm" onClick={() => void run(api(`${base}/mfg/boms/${b.id}/set-standard`, { method: 'POST', body: {} }), () => void showTree(b.id))}>{t('mf.boms.setStandard')}</button>}
            </div>
          )}
          <Flash m={msg} />
          {tree && <BomTree n={tree} />}
        </div>
      </section>
    </div>
  );
}

function BomTree({ n }: { n: TreeNode }) {
  const { t } = useTranslation();
  const rows: ReactElement[] = [];
  const walk = (x: TreeNode, depth: number) => {
    (x.components ?? []).forEach((c, i) => {
      rows.push(<tr key={`${depth}-${rows.length}-${i}`}><td style={{ paddingInlineStart: 8 + depth * 18 }}>{depth > 0 ? '└ ' : ''}{c.item_code} · {c.item_name}{c.sub ? ` (${t(`mf.bt.${c.sub.bom_type}`)})` : ''}</td>
        <td className="num" dir="ltr">{q(c.quantity)} {c.unit}</td><td className="num">{amt(c.unit_cost, 4)}</td><td className="num">{amt(c.cost)}</td><td><small>{t(`mf.src.${c.source}`)}</small></td></tr>);
      if (c.sub) walk(c.sub, depth + 1);
    });
    (x.operations ?? []).forEach((o, i) => rows.push(<tr key={`op${depth}-${rows.length}-${i}`}><td style={{ paddingInlineStart: 8 + depth * 18 }}>⚙ {o.name} · {o.workcenter}</td><td className="num">{q(o.minutes)} {t('mf.min')}</td><td /><td className="num">{amt(o.cost)}</td><td /></tr>));
  };
  walk(n, 0);
  return (
    <div style={{ overflowX: 'auto' }}>
      <table className="mx-table">
        <thead><tr><th>{n.item_code} · {n.item_name} × {q(n.quantity)} {n.unit}</th><th className="num">{t('mf.qty')}</th><th className="num">{t('mf.unitCost')}</th><th className="num">{t('mf.costCol')}</th><th /></tr></thead>
        <tbody>{rows}
          {Number(n.labour) + Number(n.overhead) > 0 && <tr><td>{t('mf.labour')} + {t('mf.overhead')}</td><td /><td /><td className="num">{amt(Number(n.labour) + Number(n.overhead))}</td><td /></tr>}
          {Number(n.service) > 0 && <tr><td>{t('mf.cost.service')}</td><td /><td /><td className="num">{amt(n.service)}</td><td /></tr>}
          {(n.byproducts ?? []).map((y, i) => <tr key={`y${i}`}><td>− {y.item_code} · {y.item_name} ({q(y.cost_share)} %)</td><td className="num">{q(y.quantity)}</td><td /><td className="num">−{amt(y.cost)}</td><td /></tr>)}
          <tr><td><b>{t('mf.total')}</b></td><td /><td className="num"><b>{amt(n.unit_cost, 4)}</b></td><td className="num"><b>{amt(n.cost)}</b></td><td /></tr>
        </tbody>
      </table>
    </div>
  );
}

// ======================= work centers and routings =======================

function Workcenters({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [wcs, reloadW] = useLoad<WC[]>(base && `${base}/mfg/workcenters`, []);
  const [routings, reloadR] = useLoad<{ id: string; name: string; operations: number; minutes: string; boms: number; is_active: boolean }[]>(base && `${base}/mfg/routings`, []);
  const blankW = { id: '', code: '', name: '', capacity: '1', hours_per_day: '8', days_per_week: '6', efficiency: '100', cost_per_hour: '0', setup_min: '0', is_active: true };
  const [w, setW] = useState(blankW);
  const blankR = { id: '', name: '', is_active: true, operations: [{ name: '', workcenter_id: '', minutes: '0', instructions: '' }] };
  const [r, setR] = useState(blankR);
  const [msg, run] = useRun(() => { reloadW(); reloadR(); });
  const manage = can(me, 'stock.manage');
  const editR = (id: string) => base && api<{ routing: { id: string; name: string; is_active: boolean }; operations: { name: string; workcenter_id: string; minutes: string; instructions: string }[] }>(`${base}/mfg/routings/${id}`)
    .then((d) => setR({ id: d.routing.id, name: d.routing.name, is_active: d.routing.is_active, operations: d.operations.map((o) => ({ name: o.name, workcenter_id: o.workcenter_id, minutes: q(o.minutes), instructions: o.instructions })) }));
  return (
    <div className="mx-row" style={{ gap: 8, alignItems: 'flex-start', flexWrap: 'wrap' }}>
      <section className="mx-box" style={{ flex: '1 1 520px', minWidth: 0 }}>
        <h3>{t('mf.wc.title')}</h3>
        <div style={{ overflowX: 'auto' }}>
          <table className="mx-table">
            <thead><tr><th>{t('mf.code')}</th><th>{t('mf.name')}</th><th className="num">{t('mf.wc.capacity')}</th><th className="num">{t('mf.wc.hours')}</th><th className="num">{t('mf.wc.eff')}</th><th className="num">{t('mf.wc.cph')}</th><th className="num">{t('mf.wos')}</th></tr></thead>
            <tbody>{wcs.map((x) => (
              <tr key={x.id} className={w.id === x.id ? 'sel' : ''} style={{ cursor: 'pointer', opacity: x.is_active ? 1 : 0.6 }} onClick={() => setW({ id: x.id, code: x.code, name: x.name, capacity: q(x.capacity), hours_per_day: q(x.hours_per_day), days_per_week: String(x.days_per_week), efficiency: q(x.efficiency), cost_per_hour: q(x.cost_per_hour), setup_min: q(x.setup_min), is_active: x.is_active })}>
                <td dir="ltr">{x.code}</td><td>{x.name}</td><td className="num">{q(x.capacity)}</td><td className="num">{q(x.hours_per_day)} × {x.days_per_week}</td><td className="num">{q(x.efficiency)} %</td><td className="num">{amt(x.cost_per_hour)}</td>
                <td className="num">{x.open_workorders}{x.running ? ` (${x.running} ▶)` : ''}</td></tr>))}
              {wcs.length === 0 && <tr><td colSpan={7} className="mx-empty">{t('mf.none')}</td></tr>}</tbody>
          </table>
        </div>
        {manage && (
          <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
            <label className="mx-fld">{t('mf.code')}<input dir="ltr" style={{ width: 70 }} value={w.code} onChange={(e) => setW({ ...w, code: e.currentTarget.value })} /></label>
            <label className="mx-fld">{t('mf.name')}<input value={w.name} onChange={(e) => setW({ ...w, name: e.currentTarget.value })} /></label>
            {(['capacity', 'hours_per_day', 'days_per_week', 'efficiency', 'cost_per_hour', 'setup_min'] as const).map((k) => (
              <label key={k} className="mx-fld">{t(`mf.wc.f.${k}`)}<input dir="ltr" style={{ width: 64 }} value={w[k]} onChange={(e) => setW({ ...w, [k]: num(e.currentTarget.value) })} /></label>))}
            <label className="mx-fld" style={{ flexDirection: 'row', alignItems: 'center', gap: 4 }}><input type="checkbox" checked={w.is_active} onChange={(e) => setW({ ...w, is_active: e.currentTarget.checked })} />{t('mf.active')}</label>
            <button className="mx-btn main" disabled={!w.code.trim() || !w.name.trim()} onClick={() => {
              const { id, days_per_week, ...rest } = w;
              void run(api(id ? `${base}/mfg/workcenters/${id}` : `${base}/mfg/workcenters`, { method: id ? 'PATCH' : 'POST', body: { ...rest, days_per_week: Number(days_per_week) || 6 } }), () => setW(blankW));
            }}>{t('mf.save')}</button>
            {w.id && <button className="mx-btn sm" onClick={() => setW(blankW)}>{t('mf.new')}</button>}
          </div>
        )}
      </section>
      <section className="mx-box" style={{ flex: '1 1 460px', minWidth: 0 }}>
        <h3>{t('mf.rt.title')}</h3>
        <table className="mx-table">
          <thead><tr><th>{t('mf.name')}</th><th className="num">{t('mf.rt.ops')}</th><th className="num">{t('mf.min')}</th><th className="num">{t('mf.m.boms')}</th></tr></thead>
          <tbody>{routings.map((x) => <tr key={x.id} className={r.id === x.id ? 'sel' : ''} style={{ cursor: 'pointer' }} onClick={() => void editR(x.id)}><td>{x.name}</td><td className="num">{x.operations}</td><td className="num">{q(x.minutes)}</td><td className="num">{x.boms}</td></tr>)}
            {routings.length === 0 && <tr><td colSpan={4} className="mx-empty">{t('mf.none')}</td></tr>}</tbody>
        </table>
        {manage && (
          <div style={{ padding: 6, display: 'flex', flexDirection: 'column', gap: 6 }}>
            <label className="mx-fld">{t('mf.name')}<input value={r.name} onChange={(e) => setR({ ...r, name: e.currentTarget.value })} /></label>
            <table className="mx-table"><thead><tr><th>#</th><th>{t('mf.operation')}</th><th>{t('mf.workcenter')}</th><th>{t('mf.rt.minBatch')}</th><th>{t('mf.rt.instr')}</th><th /></tr></thead>
              <tbody>{r.operations.map((o, i) => (
                <tr key={i}><td>{(i + 1) * 10}</td>
                  <td><input aria-label={t('mf.operation')} value={o.name} onChange={(e) => setR({ ...r, operations: r.operations.map((x, j) => (j === i ? { ...x, name: e.currentTarget.value } : x)) })} /></td>
                  <td><select aria-label={t('mf.workcenter')} value={o.workcenter_id} onChange={(e) => setR({ ...r, operations: r.operations.map((x, j) => (j === i ? { ...x, workcenter_id: e.currentTarget.value } : x)) })}>
                    <option value="">{t('mf.choose')}</option>{wcs.filter((x) => x.is_active).map((x) => <option key={x.id} value={x.id}>{x.code} · {x.name}</option>)}</select></td>
                  <td><input dir="ltr" style={{ width: 60 }} aria-label={t('mf.rt.minBatch')} value={o.minutes} onChange={(e) => setR({ ...r, operations: r.operations.map((x, j) => (j === i ? { ...x, minutes: num(e.currentTarget.value) } : x)) })} /></td>
                  <td><input aria-label={t('mf.rt.instr')} value={o.instructions} onChange={(e) => setR({ ...r, operations: r.operations.map((x, j) => (j === i ? { ...x, instructions: e.currentTarget.value } : x)) })} /></td>
                  <td><button className="mx-btn sm" aria-label={t('mf.remove')} onClick={() => setR({ ...r, operations: r.operations.filter((_, j) => j !== i) })}>×</button></td></tr>))}</tbody></table>
            <div className="mx-row" style={{ gap: 6 }}>
              <button className="mx-btn sm" onClick={() => setR({ ...r, operations: [...r.operations, { name: '', workcenter_id: '', minutes: '0', instructions: '' }] })}>{t('mf.rt.addOp')}</button>
              <span className="mx-sp" />
              {r.id && <button className="mx-btn sm" onClick={() => setR(blankR)}>{t('mf.new')}</button>}
              <button className="mx-btn main" disabled={!r.name.trim() || !r.operations.some((o) => o.name.trim() && o.workcenter_id)} onClick={() => {
                const { id, ...body } = r;
                void run(api(id ? `${base}/mfg/routings/${id}` : `${base}/mfg/routings`, { method: id ? 'PUT' : 'POST', body: { ...body, operations: body.operations.filter((o) => o.name.trim() && o.workcenter_id) } }), () => setR(blankR));
              }}>{t('mf.save')}</button>
            </div>
          </div>
        )}
        <Flash m={msg} />
      </section>
    </div>
  );
}

// ======================= MRP =======================

interface MrpLine { item_id: string; item_code: string; item_name: string; unit: string; level: number; on_hand: string; incoming: string; demand: string; projected: string;
  min_qty: string | null; max_qty: string | null; suggest: string; kind: string; supplier_name?: string; unit_price?: string; currency?: string; need_date: string; order_date: string; sources: string[] }

function Mrp({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [whs] = useLoad<Warehouse[]>(base && `${base}/wh/warehouses`, []);
  const [wh, setWh] = useState('');
  const [data, reload] = useLoad<{ warehouse_id: string; lines: MrpLine[] }>(base && `${base}/mfg/mrp?warehouse_id=${wh}`, { warehouse_id: '', lines: [] });
  const [picked, setPicked] = useState<string[]>([]);
  const [onlyNeeds, setOnlyNeeds] = useState(true);
  const [msg, run] = useRun(reload);
  const [result, setResult] = useState<{ mos: { number: number; item_code: string }[]; pos: { number: number; lines: number }[]; skipped: string[] } | null>(null);
  const lines = data.lines.filter((l) => !onlyNeeds || Number(l.suggest) > 0);
  const go = (ids: string[]) => void run(api(`${base}/mfg/mrp/run`, { method: 'POST', body: { warehouse_id: data.warehouse_id, item_ids: ids } }), (x) => { setResult(x as typeof result); setPicked([]); });
  return (
    <section className="mx-box">
      <h3>{t('mf.mrp.title')}</h3>
      <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
        <label className="mx-fld">{t('mf.warehouse')}<select value={wh || data.warehouse_id} onChange={(e) => setWh(e.currentTarget.value)}>
          {whs.filter((w) => !w.is_transit).map((w) => <option key={w.id} value={w.id}>{w.name}</option>)}</select></label>
        <label className="mx-fld" style={{ flexDirection: 'row', alignItems: 'center', gap: 4 }}><input type="checkbox" checked={onlyNeeds} onChange={(e) => setOnlyNeeds(e.currentTarget.checked)} />{t('mf.mrp.onlyNeeds')}</label>
        <button className="mx-btn sm" onClick={reload}>{t('mf.refresh')}</button>
        <span className="mx-sp" />
        {can(me, 'purchasing.create') && picked.length > 0 && <button className="mx-btn" onClick={() => go(picked)}>{t('mf.mrp.createSel', { n: picked.length })}</button>}
        {can(me, 'purchasing.create') && data.lines.some((l) => Number(l.suggest) > 0) && <button className="mx-btn main" onClick={() => go([])}>{t('mf.mrp.createAll')}</button>}
      </div>
      <div style={{ overflowX: 'auto' }}>
        <table className="mx-table">
          <thead><tr><th /><th>{t('mf.item')}</th><th className="num">{t('mf.mrp.level')}</th><th className="num">{t('mf.mrp.onHand')}</th><th className="num">{t('mf.mrp.incoming')}</th><th className="num">{t('mf.mrp.demand')}</th>
            <th className="num">{t('mf.mrp.projected')}</th><th className="num">{t('mf.mrp.minmax')}</th><th className="num">{t('mf.mrp.suggest')}</th><th>{t('mf.mrp.action')}</th><th>{t('mf.mrp.needBy')}</th><th>{t('mf.mrp.sources')}</th></tr></thead>
          <tbody>{lines.map((l) => (
            <tr key={l.item_id}>
              <td>{Number(l.suggest) > 0 && <input type="checkbox" aria-label={l.item_code} checked={picked.includes(l.item_id)} onChange={(e) => setPicked(e.currentTarget.checked ? [...picked, l.item_id] : picked.filter((x) => x !== l.item_id))} />}</td>
              <td>{l.item_code} · {l.item_name}</td><td className="num">{l.level}</td><td className="num">{q(l.on_hand)}</td><td className="num">{q(l.incoming)}</td><td className="num">{q(l.demand)}</td>
              <td className={`num ${Number(l.projected) < 0 ? 'red' : ''}`}>{q(l.projected)}</td><td className="num" dir="ltr">{l.min_qty != null ? `${q(l.min_qty)} / ${q(l.max_qty)}` : ''}</td>
              <td className="num"><b>{Number(l.suggest) > 0 ? `${q(l.suggest)} ${l.unit}` : ''}</b></td>
              <td>{l.kind === 'mo' ? t('mf.mrp.make') : l.kind === 'po' ? `${t('mf.mrp.buy')}${l.supplier_name ? ` · ${l.supplier_name}` : ` · ${t('mf.mrp.noSupplier')}`}` : ''}</td>
              <td>{Number(l.suggest) > 0 ? `${formatDate(l.order_date)} → ${formatDate(l.need_date)}` : ''}</td>
              <td><small dir="ltr">{l.sources.join(', ')}</small></td></tr>))}
            {lines.length === 0 && <tr><td colSpan={12} className="mx-empty">{t('mf.mrp.nothing')}</td></tr>}</tbody>
        </table>
      </div>
      <Flash m={msg} />
      {result && <div className="mx-flash good">{t('mf.mrp.done', { mos: result.mos.length, pos: result.pos.length })}{result.skipped.length > 0 ? ` · ${t('mf.mrp.skipped', { items: result.skipped.join(', ') })}` : ''}
        {' '}<button className="mx-btn sm" onClick={() => openScreen('po')}>{t('mf.mrp.openPO')}</button></div>}
    </section>
  );
}

// ======================= quality =======================

function Quality({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const items = useItems();
  const [tab, setTab] = useState<'checks' | 'alerts' | 'points'>('checks');
  const [st, setSt] = useState('todo');
  const [checks, reloadC] = useLoad<QCheck[]>(base && `${base}/mfg/quality-checks?status=${st}`, []);
  const [alerts, reloadA] = useLoad<{ id: string; number: number; title: string; item: string | null; order_number: number | null; priority: number; stage: string; root_cause: string; action: string; created_at: string }[]>(base && `${base}/mfg/quality-alerts?stage=open`, []);
  const [points, reloadP] = useLoad<{ id: string; name: string; trigger_on: string; item: string | null; workcenter: string | null; test_type: string; tol_min: string | null; tol_max: string | null; unit: string; instructions: string; is_active: boolean }[]>(base && `${base}/mfg/quality-points`, []);
  const [wcs] = useLoad<WC[]>(base && `${base}/mfg/workcenters`, []);
  const blank = { name: '', trigger_on: 'production', item_id: '', workcenter_id: '', test_type: 'passfail', norm: '', tol_min: '', tol_max: '', unit: '', instructions: '' };
  const [p, setP] = useState(blank);
  const [al, setAl] = useState({ title: '', item_id: '', priority: 1 });
  const [edits, setEdits] = useState<Record<string, { root_cause?: string; action?: string }>>({});
  const [msg, run] = useRun(() => { reloadC(); reloadA(); reloadP(); });
  const manage = can(me, 'stock.manage');
  return (
    <section className="mx-box">
      <div className="mx-row" role="tablist" style={{ gap: 4, padding: 6 }}>
        {(['checks', 'alerts', 'points'] as const).map((x) => <button key={x} role="tab" aria-selected={tab === x} className={`mx-btn sm ${tab === x ? 'main' : ''}`} onClick={() => setTab(x)}>{t(`mf.q.tab.${x}`)}{x === 'alerts' && alerts.length ? ` (${alerts.length})` : ''}</button>)}
      </div>
      {tab === 'checks' && <>
        <div className="mx-row" style={{ gap: 6, padding: 6 }}><label className="mx-fld">{t('mf.status')}<select value={st} onChange={(e) => setSt(e.currentTarget.value)}>
          {['todo', 'pass', 'fail', 'all'].map((s) => <option key={s} value={s}>{t(`mf.st.${s}`)}</option>)}</select></label></div>
        {checks.length ? <ChecksTable checks={checks} me={me} onDone={() => { reloadC(); reloadA(); }} /> : <div className="mx-empty">{t('mf.none')}</div>}
      </>}
      {tab === 'alerts' && <>
        <div style={{ overflowX: 'auto' }}><table className="mx-table">
          <thead><tr><th>#</th><th>{t('mf.q.title')}</th><th>{t('mf.q.priority')}</th><th>{t('mf.q.cause')}</th><th>{t('mf.q.action')}</th><th>{t('mf.status')}</th><th /></tr></thead>
          <tbody>{alerts.map((a) => (
            <tr key={a.id}><td dir="ltr">QA{a.number}</td><td>{a.title}{a.order_number ? <small> · {moCode(a.order_number)}</small> : null}</td><td>{'★'.repeat(a.priority)}</td>
              <td><input aria-label={t('mf.q.cause')} value={edits[a.id]?.root_cause ?? a.root_cause} onChange={(e) => setEdits({ ...edits, [a.id]: { ...edits[a.id], root_cause: e.currentTarget.value } })} /></td>
              <td><input aria-label={t('mf.q.action')} value={edits[a.id]?.action ?? a.action} onChange={(e) => setEdits({ ...edits, [a.id]: { ...edits[a.id], action: e.currentTarget.value } })} /></td>
              <td><StatusPill s={a.stage} /></td>
              <td>{can(me, 'stock.create') && <span className="mx-row" style={{ gap: 4 }}>
                {a.stage === 'new' && <button className="mx-btn sm" onClick={() => void run(api(`${base}/mfg/quality-alerts/${a.id}`, { method: 'PATCH', body: { ...edits[a.id], stage: 'progress' } }))}>{t('mf.q.take')}</button>}
                <button className="mx-btn sm main" onClick={() => void run(api(`${base}/mfg/quality-alerts/${a.id}`, { method: 'PATCH', body: { ...edits[a.id], stage: 'done' } }))}>{t('mf.q.close')}</button></span>}</td></tr>))}
            {alerts.length === 0 && <tr><td colSpan={7} className="mx-empty">{t('mf.q.noAlerts')}</td></tr>}</tbody></table></div>
        {can(me, 'stock.create') && <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
          <label className="mx-fld">{t('mf.q.title')}<input value={al.title} onChange={(e) => setAl({ ...al, title: e.currentTarget.value })} /></label>
          <ItemSelect label={t('mf.item')} value={al.item_id} onChange={(v) => setAl({ ...al, item_id: v })} items={items} />
          <label className="mx-fld">{t('mf.q.priority')}<select value={al.priority} onChange={(e) => setAl({ ...al, priority: Number(e.currentTarget.value) })}>{[0, 1, 2, 3].map((x) => <option key={x} value={x}>{x}</option>)}</select></label>
          <button className="mx-btn" disabled={!al.title.trim()} onClick={() => void run(api(`${base}/mfg/quality-alerts`, { method: 'POST', body: al }), () => setAl({ title: '', item_id: '', priority: 1 }))}>{t('mf.q.newAlert')}</button>
        </div>}
      </>}
      {tab === 'points' && <>
        <div style={{ overflowX: 'auto' }}><table className="mx-table">
          <thead><tr><th>{t('mf.name')}</th><th>{t('mf.q.when')}</th><th>{t('mf.item')}</th><th>{t('mf.workcenter')}</th><th>{t('mf.q.test')}</th><th>{t('mf.status')}</th></tr></thead>
          <tbody>{points.map((x) => (
            <tr key={x.id} style={{ opacity: x.is_active ? 1 : 0.6 }}><td>{x.name}</td><td>{t(`mf.q.trig.${x.trigger_on}`)}</td><td>{x.item ?? t('mf.all')}</td><td>{x.workcenter ?? (x.trigger_on === 'operation' ? t('mf.all') : '')}</td>
              <td dir="ltr">{x.test_type === 'measure' ? `${q(x.tol_min)} – ${q(x.tol_max)} ${x.unit}` : t('mf.q.passfail')}</td>
              <td>{manage ? <button className="mx-btn sm" onClick={() => void run(api(`${base}/mfg/quality-points/${x.id}`, { method: 'PATCH', body: { is_active: !x.is_active } }))}>{x.is_active ? t('mf.active') : t('mf.inactive')}</button> : x.is_active ? t('mf.active') : t('mf.inactive')}</td></tr>))}
            {points.length === 0 && <tr><td colSpan={6} className="mx-empty">{t('mf.none')}</td></tr>}</tbody></table></div>
        {manage && <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
          <label className="mx-fld">{t('mf.name')}<input value={p.name} onChange={(e) => setP({ ...p, name: e.currentTarget.value })} /></label>
          <label className="mx-fld">{t('mf.q.when')}<select value={p.trigger_on} onChange={(e) => setP({ ...p, trigger_on: e.currentTarget.value })}>{['operation', 'production', 'receipt'].map((x) => <option key={x} value={x}>{t(`mf.q.trig.${x}`)}</option>)}</select></label>
          <ItemSelect label={t('mf.item')} value={p.item_id} onChange={(v) => setP({ ...p, item_id: v })} items={items} />
          {p.trigger_on === 'operation' && <label className="mx-fld">{t('mf.workcenter')}<select value={p.workcenter_id} onChange={(e) => setP({ ...p, workcenter_id: e.currentTarget.value })}><option value="">{t('mf.all')}</option>{wcs.map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}</select></label>}
          <label className="mx-fld">{t('mf.q.test')}<select value={p.test_type} onChange={(e) => setP({ ...p, test_type: e.currentTarget.value })}><option value="passfail">{t('mf.q.passfail')}</option><option value="measure">{t('mf.q.measure')}</option></select></label>
          {p.test_type === 'measure' && <>
            <label className="mx-fld">{t('mf.q.min')}<input dir="ltr" style={{ width: 60 }} value={p.tol_min} onChange={(e) => setP({ ...p, tol_min: num(e.currentTarget.value) })} /></label>
            <label className="mx-fld">{t('mf.q.max')}<input dir="ltr" style={{ width: 60 }} value={p.tol_max} onChange={(e) => setP({ ...p, tol_max: num(e.currentTarget.value) })} /></label>
            <label className="mx-fld">{t('mf.q.unit')}<input dir="ltr" style={{ width: 50 }} value={p.unit} onChange={(e) => setP({ ...p, unit: e.currentTarget.value })} /></label>
          </>}
          <label className="mx-fld">{t('mf.rt.instr')}<input value={p.instructions} onChange={(e) => setP({ ...p, instructions: e.currentTarget.value })} /></label>
          <button className="mx-btn main" disabled={!p.name.trim() || (p.test_type === 'measure' && (!p.tol_min || !p.tol_max))} onClick={() => {
            const body = Object.fromEntries(Object.entries(p).filter(([k, v]) => v !== '' || k === 'name'));
            void run(api(`${base}/mfg/quality-points`, { method: 'POST', body }), () => setP(blank));
          }}>{t('mf.save')}</button>
        </div>}
      </>}
      <Flash m={msg} />
    </section>
  );
}

// ======================= traceability =======================

interface TNode { lot_id: string; lot_no: string; item_code: string; item_name: string; expiry: string | null; events: { date: string; source: string; ref: string; partner: string; quantity: string }[];
  orders: { order_id: string; number: number; date: string; lots: TNode[] }[]; untraced?: { item_code: string; item_name: string; quantity: string }[] }

function TraceTree({ n, depth, forward }: { n: TNode; depth: number; forward: boolean }) {
  const { t } = useTranslation();
  return (
    <div style={{ marginInlineStart: depth ? 18 : 0, borderInlineStart: depth ? '2px solid #dde3ea' : undefined, paddingInlineStart: depth ? 8 : 0, marginTop: 4 }}>
      <div><b dir="ltr">{n.lot_no}</b> · {n.item_code} {n.item_name}{n.expiry ? ` · ${t('mf.trace.exp')} ${formatDate(n.expiry)}` : ''}</div>
      {n.events.map((e, i) => <div key={i}><small>{formatDate(e.date)} · {t(`mf.trace.src.${e.source}`, { defaultValue: e.source })} <span dir="ltr">{e.ref}</span> {e.partner && `· ${e.partner}`} · <span dir="ltr">{q(e.quantity)}</span></small></div>)}
      {n.orders.map((o) => (
        <div key={o.order_id} style={{ marginInlineStart: 12 }}>
          <small>{forward ? t('mf.trace.usedIn') : t('mf.trace.madeBy')} <b dir="ltr">{moCode(o.number)}</b> ({formatDate(o.date)})</small>
          {o.lots.map((l) => <TraceTree key={l.lot_id + o.order_id} n={l} depth={depth + 1} forward={forward} />)}
        </div>))}
      {(n.untraced ?? []).length > 0 && <div style={{ marginInlineStart: 12 }}><small>{t('mf.trace.untraced')}: {(n.untraced ?? []).map((u) => `${u.item_code} × ${q(u.quantity)}`).join(', ')}</small></div>}
    </div>
  );
}

function Trace() {
  const { t } = useTranslation();
  const base = useBase();
  const [text, setText] = useState('');
  const [matches, setMatches] = useState<{ id: string; lot_no: string; item_code: string; item_name: string; expiry: string | null }[]>([]);
  const [res, setRes] = useState<{ backward: TNode; forward: TNode } | null>(null);
  const search = () => base && text.trim() && api<{ matches: typeof matches }>(`${base}/mfg/trace?q=${encodeURIComponent(text.trim())}`).then((r) => {
    setMatches(r.matches); setRes(null);
    if (r.matches.length === 1) void open(r.matches[0].id);
  }).catch(() => setMatches([]));
  const open = (id: string) => base && api<{ backward: TNode; forward: TNode }>(`${base}/mfg/trace?lot_id=${id}`).then(setRes).catch(() => setRes(null));
  return (
    <section className="mx-box">
      <h3>{t('mf.trace.title')}</h3>
      <div className="mx-row" style={{ gap: 6, padding: 6 }}>
        <input dir="ltr" aria-label={t('mf.trace.lot')} placeholder={t('mf.trace.lot')} value={text} onChange={(e) => setText(e.currentTarget.value)} onKeyDown={(e) => { if (e.key === 'Enter') void search(); }} />
        <button className="mx-btn main" onClick={() => void search()}>{t('mf.search')}</button>
      </div>
      {matches.length > 1 && <div className="mx-row" style={{ gap: 4, padding: 6, flexWrap: 'wrap' }}>{matches.map((m) => <button key={m.id} className="mx-btn sm" onClick={() => void open(m.id)}><span dir="ltr">{m.lot_no}</span> · {m.item_code}</button>)}</div>}
      {matches.length === 0 && text && <div className="mx-empty">{t('mf.trace.hint')}</div>}
      {res && (
        <div className="mx-row" style={{ gap: 8, alignItems: 'flex-start', flexWrap: 'wrap', padding: 6 }}>
          <div className="mx-box" style={{ flex: '1 1 360px', padding: 8 }}><h3>{t('mf.trace.backward')}</h3><TraceTree n={res.backward} depth={0} forward={false} /></div>
          <div className="mx-box" style={{ flex: '1 1 360px', padding: 8 }}><h3>{t('mf.trace.forward')}</h3><TraceTree n={res.forward} depth={0} forward /></div>
        </div>
      )}
    </section>
  );
}

// ======================= reports =======================

function Reports() {
  const { t } = useTranslation();
  const base = useBase();
  const [rep, setRep] = useState<'status' | 'load' | 'variance' | 'shortages'>('status');
  const [status] = useLoad<{ counts: { status: string; orders: number; quantity: string }[]; late: MO[]; open: MO[]; open_alerts: number; checks_todo: number } | null>(rep === 'status' && base && `${base}/mfg/reports/status`, null);
  const [days, setDays] = useState('10');
  const [load] = useLoad<{ id: string; code: string; name: string; open_min: string; open_workorders: number; capacity_min: string; load_pct: string | null; done_min_30d: string }[]>(rep === 'load' && base && `${base}/mfg/reports/load?days=${days || 10}`, []);
  const [var_] = useLoad<{ id: string; number: number; order_date: string; item_code: string; item_name: string; good_qty: string; actual: string; expected: string; diff_expected: string; standard_cost: string; variance: string; unit_cost: string }[]>(rep === 'variance' && base && `${base}/mfg/reports/variance`, []);
  const [short] = useLoad<{ order_id: string; order_number: number; status: string; deadline: string | null; item_code: string; item_name: string; unit: string; required: string; reserved: string; missing: string; on_hand: string; on_order: string }[]>(rep === 'shortages' && base && `${base}/mfg/reports/shortages`, []);
  return (
    <section className="mx-box">
      <div className="mx-row" role="tablist" style={{ gap: 4, padding: 6, flexWrap: 'wrap' }}>
        {(['status', 'load', 'variance', 'shortages'] as const).map((x) => <button key={x} role="tab" aria-selected={rep === x} className={`mx-btn sm ${rep === x ? 'main' : ''}`} onClick={() => setRep(x)}>{t(`mf.r.${x}`)}</button>)}
      </div>
      <div style={{ overflowX: 'auto', padding: 6 }}>
        {rep === 'status' && status && <>
          <div className="mx-row" style={{ gap: 8, flexWrap: 'wrap', marginBottom: 8 }}>
            {status.counts.map((c) => <div key={c.status} className="mx-box" style={{ padding: 8, minWidth: 110 }}><small>{t(`mf.st.${c.status}`)}</small><div style={{ fontSize: 20, fontWeight: 700 }}>{c.orders}</div></div>)}
            <div className="mx-box" style={{ padding: 8, minWidth: 110 }}><small>{t('mf.r.late')}</small><div style={{ fontSize: 20, fontWeight: 700, color: status.late.length ? '#b42318' : undefined }}>{status.late.length}</div></div>
            <div className="mx-box" style={{ padding: 8, minWidth: 110 }}><small>{t('mf.r.checksTodo')}</small><div style={{ fontSize: 20, fontWeight: 700 }}>{status.checks_todo}</div></div>
            <div className="mx-box" style={{ padding: 8, minWidth: 110 }}><small>{t('mf.r.alerts')}</small><div style={{ fontSize: 20, fontWeight: 700, color: status.open_alerts ? '#b42318' : undefined }}>{status.open_alerts}</div></div>
          </div>
          <table className="mx-table"><thead><tr><th>{t('mf.number')}</th><th>{t('mf.item')}</th><th className="num">{t('mf.qty')}</th><th>{t('mf.deadline')}</th><th>{t('mf.components')}</th><th>{t('mf.wos')}</th><th>{t('mf.status')}</th></tr></thead>
            <tbody>{status.open.map((o) => <tr key={o.id}><td dir="ltr">{moCode(o.number)}</td><td>{o.item_code} · {o.item_name}</td><td className="num">{q(o.quantity)}</td><td className={o.late ? 'red' : ''}>{o.deadline ? formatDate(o.deadline) : ''}</td>
              <td>{o.availability ? t(`mf.av.${o.availability}`) : ''}</td><td dir="ltr">{o.workorders ? `${o.workorders_done}/${o.workorders}` : ''}</td><td><StatusPill s={o.status} /></td></tr>)}
              {status.open.length === 0 && <tr><td colSpan={7} className="mx-empty">{t('mf.none')}</td></tr>}</tbody></table>
        </>}
        {rep === 'load' && <>
          <label className="mx-fld" style={{ maxWidth: 160 }}>{t('mf.r.days')}<input dir="ltr" value={days} onChange={(e) => setDays(num(e.currentTarget.value))} /></label>
          <table className="mx-table"><thead><tr><th>{t('mf.workcenter')}</th><th className="num">{t('mf.wos')}</th><th className="num">{t('mf.r.openMin')}</th><th className="num">{t('mf.r.capMin')}</th><th>{t('mf.r.loadCol')}</th><th className="num">{t('mf.r.done30')}</th></tr></thead>
            <tbody>{load.map((l) => { const pct = Number(l.load_pct ?? 0); return (
              <tr key={l.id}><td>{l.code} · {l.name}</td><td className="num">{l.open_workorders}</td><td className="num">{q(l.open_min)}</td><td className="num">{q(l.capacity_min)}</td>
                <td><div style={{ display: 'flex', alignItems: 'center', gap: 6 }}><div style={{ width: 120, height: 10, background: 'var(--mx-head)', borderRadius: 5, overflow: 'hidden' }}><div style={{ width: `${Math.min(100, pct)}%`, height: '100%', background: pct > 100 ? '#b42318' : pct > 80 ? '#a15c00' : '#0f7b4b' }} /></div><span dir="ltr">{pct} %</span></div></td>
                <td className="num">{q(l.done_min_30d)}</td></tr>); })}
              {load.length === 0 && <tr><td colSpan={6} className="mx-empty">{t('mf.none')}</td></tr>}</tbody></table>
        </>}
        {rep === 'variance' && (
          <table className="mx-table"><thead><tr><th>{t('mf.number')}</th><th>{t('mf.date')}</th><th>{t('mf.item')}</th><th className="num">{t('mf.qty')}</th><th className="num">{t('mf.cost.expected')}</th><th className="num">{t('mf.cost.actual')}</th><th className="num">{t('mf.r.diff')}</th><th className="num">{t('mf.r.std')}</th><th className="num">{t('mf.r.varianceCol')}</th></tr></thead>
            <tbody>{var_.map((v) => <tr key={v.id}><td dir="ltr">{moCode(v.number)}</td><td>{formatDate(v.order_date)}</td><td>{v.item_code} · {v.item_name}</td><td className="num">{q(v.good_qty)}</td><td className="num">{amt(v.expected)}</td><td className="num">{amt(v.actual)}</td>
              <td className={`num ${Number(v.diff_expected) > 0 ? 'red' : ''}`}>{amt(v.diff_expected)}</td><td className="num">{Number(v.standard_cost) > 0 ? amt(v.standard_cost, 4) : ''}</td><td className={`num ${Number(v.variance) > 0 ? 'red' : ''}`}>{Number(v.standard_cost) > 0 ? amt(v.variance) : ''}</td></tr>)}
              {var_.length === 0 && <tr><td colSpan={9} className="mx-empty">{t('mf.none')}</td></tr>}</tbody></table>
        )}
        {rep === 'shortages' && (
          <table className="mx-table"><thead><tr><th>{t('mf.number')}</th><th>{t('mf.deadline')}</th><th>{t('mf.component')}</th><th className="num">{t('mf.r.required')}</th><th className="num">{t('mf.reserved')}</th><th className="num">{t('mf.r.missing')}</th><th className="num">{t('mf.mrp.onHand')}</th><th className="num">{t('mf.r.onOrder')}</th></tr></thead>
            <tbody>{short.map((s, i) => <tr key={i}><td dir="ltr">{moCode(s.order_number)}</td><td>{s.deadline ? formatDate(s.deadline) : ''}</td><td>{s.item_code} · {s.item_name}</td><td className="num">{q(s.required)}</td><td className="num">{q(s.reserved)}</td>
              <td className="num red"><b>{q(s.missing)}</b></td><td className="num">{q(s.on_hand)}</td><td className="num">{q(s.on_order)}</td></tr>)}
              {short.length === 0 && <tr><td colSpan={8} className="mx-empty">{t('mf.r.noShort')}</td></tr>}</tbody></table>
        )}
      </div>
    </section>
  );
}

// ======================= scrap =======================

function Scrap({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const items = useItems();
  const [whs] = useLoad<Warehouse[]>(base && `${base}/wh/warehouses`, []);
  const [sc, setSc] = useState({ item_id: '', warehouse_id: '', quantity: '', reason: '', date: today() });
  const [msg, run] = useRun();
  if (!can(me, 'stock.manage')) return <div className="mx-empty">{t('mf.noPerm')}</div>;
  return (
    <section className="mx-box"><h3>{t('mf.m.scrap')}</h3>
      <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
        <ItemSelect label={t('mf.item')} value={sc.item_id} onChange={(v) => setSc({ ...sc, item_id: v })} items={items} />
        <label className="mx-fld">{t('mf.warehouse')}<select value={sc.warehouse_id} onChange={(e) => setSc({ ...sc, warehouse_id: e.currentTarget.value })}><option value="">{t('mf.mainWh')}</option>{whs.map((w) => <option key={w.id} value={w.id}>{w.name}</option>)}</select></label>
        <label className="mx-fld">{t('mf.qty')}<input dir="ltr" style={{ width: 70 }} value={sc.quantity} onChange={(e) => setSc({ ...sc, quantity: num(e.currentTarget.value) })} /></label>
        <label className="mx-fld">{t('mf.date')}<input type="date" dir="ltr" value={sc.date} onChange={(e) => setSc({ ...sc, date: e.currentTarget.value })} /></label>
        <label className="mx-fld">{t('mf.reason')}<input value={sc.reason} onChange={(e) => setSc({ ...sc, reason: e.currentTarget.value })} /></label>
        <button className="mx-btn main" disabled={!sc.item_id || !Number(sc.quantity) || !sc.reason.trim()} onClick={() => void run(api(`${base}/mfg/scrap`, { method: 'POST', body: sc }), () => setSc({ ...sc, quantity: '', reason: '' }))}>{t('mf.scrapIt')}</button>
      </div>
      <small style={{ padding: 6, display: 'block' }}>{t('mf.scrapHint')}</small>
      <Flash m={msg} />
    </section>
  );
}

