import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, can, type Me } from '../../api';
import { useCompany } from '../../company';
import { errorText, formatAmount, formatDate, formatDateTime, today } from '../../util';
import { printPage } from '../../native';
import { openScreen } from '../../nav';
import { Barcode } from '../retail/Retail';

// Warehouse pro (Magic-style screens): operations (receive → QC → putaway, pick → pack → ship), bins and moves,
// batch / wave picking, packages and pallets, the delivery board (trips, drivers, proof of delivery), dropship,
// stock reports and the warehouse setup (bins, steps, putaway rules).

type Msg = { ok: boolean; text: string } | null;
function Flash({ m }: { m: Msg }) {
  return m ? <div className={`mx-flash ${m.ok ? 'good' : 'bad'}`}>{m.text}</div> : null;
}
export function useBase(): string | null {
  const { company } = useCompany();
  return company ? `/companies/${company.id}` : null;
}
export function useLoad<T>(path: string | null | false, init: T): [T, () => void] {
  const [v, setV] = useState<T>(init);
  const load = useCallback(() => {
    if (path) api<T>(path).then(setV).catch(() => setV(init));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [path]);
  useEffect(load, [load]);
  return [v, load];
}
export function useRun(after?: () => void): [Msg, (p: Promise<unknown>, then?: (x: unknown) => void) => Promise<void>, (m: Msg) => void] {
  const { t } = useTranslation();
  const [msg, setMsg] = useState<Msg>(null);
  const run = async (p: Promise<unknown>, then?: (x: unknown) => void) => {
    try { const x = await p; setMsg({ ok: true, text: t('common.saved') }); then?.(x); after?.(); } catch (e) { setMsg({ ok: false, text: errorText(t, e) }); }
  };
  return [msg, run, setMsg];
}

export interface Warehouse {
  id: string; name: string; code: string; is_active: boolean; use_bins: boolean; reception_steps: number; delivery_steps: number;
  removal: string; crossdock: boolean; is_transit: boolean; address: string; locations: number; ops_ready: number; value: string;
}
export interface Loc { id: string; warehouse_id: string; warehouse: string; parent_id: string | null; kind: string; code: string; name: string; barcode: string; sort_key: string; is_active: boolean; items: number; quantity: string }
interface ItemLite { id: string; code: string; name: string; unit: string; barcode?: string; kind: string; is_active: boolean }
interface Op {
  id: string; number: number; warehouse_id: string; warehouse: string; kind: string; status: string; origin: string; sales_order_id: string | null;
  batch_id: string | null; batch_number: number | null; scheduled_date: string; notes: string; done_at: string | null; customer_name: string | null; lines: number; quantity: string;
}
export interface OpLine {
  id: string; line_no: number; item_id: string; item_code: string; item_name: string; unit: string; barcode: string | null; lot_id: string | null; lot_no: string | null;
  quantity: string; done_qty: string; from_location_id: string | null; from_code: string | null; to_location_id: string | null; to_code: string | null;
  package_id: string | null; package_number: number | null; op_id?: string; op_number?: number; origin?: string;
}
interface Pkg {
  id: string; number: number; code: string; warehouse_id: string; warehouse: string; kind: string; parent_id: string | null; parent_code: string | null;
  sales_order_id: string | null; order_number: number | null; customer_name: string | null; customer_address?: string | null; status: string; weight: string;
  length_cm: string; width_cm: string; height_cm: string; notes: string; lines: number; children: number; company_name?: string; company_name_ar?: string | null;
}

export const opCode = (n: number) => `OP${String(n).padStart(6, '0')}`;
const qtyFmt = (v: string | null | undefined) => (v == null ? '' : String(Number(v)));

const MODES = ['ops', 'bins', 'batches', 'packages', 'trips', 'dropship', 'reports', 'setup'] as const;
type Mode = typeof MODES[number];

export default function Wh({ me, initial }: { me: Me; initial?: string }) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<Mode>((MODES as readonly string[]).includes(initial ?? '') ? initial as Mode : 'ops');
  return (
    <div className="mx-col">
      <div className="mx-strip"><b>{t(`wh.m.${mode}`)}</b><span className="mx-sp" /><small style={{ opacity: 0.8 }}>{t(`wh.help.${mode}`)}</small>
        <button className="mx-btn sm" onClick={() => openScreen('scan')}>{t('wh.openScanner')}</button>
      </div>
      <div className="mx-modes" role="tablist">
        {MODES.map((m) => <button key={m} role="tab" className="mx-mode" aria-selected={mode === m} onClick={() => setMode(m)}>{t(`wh.m.${m}`)}</button>)}
      </div>
      <div style={{ flex: 1, minHeight: 0, overflow: 'auto', padding: 6 }}>
        {mode === 'ops' && <Ops me={me} />}
        {mode === 'bins' && <Bins me={me} />}
        {mode === 'batches' && <Batches me={me} />}
        {mode === 'packages' && <Packages me={me} />}
        {mode === 'trips' && <Trips me={me} />}
        {mode === 'dropship' && <Dropship me={me} />}
        {mode === 'reports' && <Reports />}
        {mode === 'setup' && <Setup me={me} />}
      </div>
    </div>
  );
}

/** Warehouse picker (with "all" when allowed). */
function WhSelect({ value, onChange, all, list }: { value: string; onChange: (v: string) => void; all?: boolean; list: Warehouse[] }) {
  const { t } = useTranslation();
  return (
    <label className="mx-fld">{t('wh.warehouse')}
      <select value={value} onChange={(e) => onChange(e.currentTarget.value)}>
        {all && <option value="">{t('wh.all')}</option>}
        {list.map((w) => <option key={w.id} value={w.id}>{w.name}{w.is_transit ? ` (${t('wh.transit')})` : ''}</option>)}
      </select>
    </label>
  );
}

function useWarehouses(): [Warehouse[], () => void] {
  const base = useBase();
  return useLoad<Warehouse[]>(base && `${base}/wh/warehouses`, []);
}

// ======================= operations =======================

function Ops({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [whs] = useWarehouses();
  const [wh, setWh] = useState('');
  const [status, setStatus] = useState('open');
  const [kind, setKind] = useState('');
  const [ops, reload] = useLoad<Op[]>(base && `${base}/wh/ops?status=${status}&kind=${kind}&warehouse_id=${wh}`, []);
  const [sel, setSel] = useState<string | null>(null);
  const [picked, setPicked] = useState<string[]>([]);
  const [msg, run] = useRun(reload);
  const canDo = can(me, 'stock.create');
  const picks = ops.filter((o) => o.kind === 'pick' && o.status === 'ready' && !o.batch_id);
  function batch(kindB: 'batch' | 'wave') {
    const first = ops.find((o) => picked.includes(o.id));
    const w = first?.warehouse_id ?? wh;
    if (!base || !w) return;
    void run(api(`${base}/wh/batches`, { method: 'POST', body: kindB === 'wave' ? { warehouse_id: w, kind: 'wave' } : { warehouse_id: w, kind: 'batch', op_ids: picked } }),
      () => { setPicked([]); openScreen('wh', 'batches'); });
  }
  return (
    <div className="mx-row" style={{ gap: 8, alignItems: 'flex-start', flexWrap: 'wrap' }}>
      <section className="mx-box" style={{ flex: '999 1 560px', minWidth: 0 }}>
        <h3>{t('wh.ops.title')}</h3>
        <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
          <WhSelect value={wh} onChange={setWh} all list={whs} />
          <label className="mx-fld">{t('wh.status')}<select value={status} onChange={(e) => setStatus(e.currentTarget.value)}>
            {['open', 'ready', 'done', 'cancelled', 'all'].map((s) => <option key={s} value={s}>{t(`wh.st.${s}`)}</option>)}</select></label>
          <label className="mx-fld">{t('wh.kind')}<select value={kind} onChange={(e) => setKind(e.currentTarget.value)}>
            <option value="">{t('wh.all')}</option>
            {['putaway', 'qc', 'pick', 'pack', 'ship', 'internal'].map((k) => <option key={k} value={k}>{t(`wh.k.${k}`)}</option>)}</select></label>
          <span className="mx-sp" />
          {canDo && picked.length > 0 && <button className="mx-btn main" onClick={() => batch('batch')}>{t('wh.ops.batchSel', { n: picked.length })}</button>}
          {canDo && picks.length > 0 && <button className="mx-btn" onClick={() => batch('wave')} disabled={!wh && !picks[0]}>{t('wh.ops.wave')}</button>}
        </div>
        <div style={{ overflowX: 'auto' }}>
          <table className="mx-table">
            <thead><tr><th /><th>{t('wh.number')}</th><th>{t('wh.kind')}</th><th>{t('wh.origin')}</th><th>{t('wh.customer')}</th><th>{t('wh.warehouse')}</th>
              <th>{t('wh.date')}</th><th>{t('wh.qty')}</th><th>{t('wh.status')}</th></tr></thead>
            <tbody>
              {ops.length === 0 && <tr><td colSpan={9} className="mx-empty">{t('wh.ops.none')}</td></tr>}
              {ops.map((o) => (
                <tr key={o.id} className={sel === o.id ? 'sel' : ''} style={{ cursor: 'pointer' }} onClick={() => setSel(o.id)}>
                  <td onClick={(e) => e.stopPropagation()}>{o.kind === 'pick' && o.status === 'ready' && !o.batch_id &&
                    <input type="checkbox" aria-label={opCode(o.number)} checked={picked.includes(o.id)} onChange={(e) => setPicked(e.currentTarget.checked ? [...picked, o.id] : picked.filter((x) => x !== o.id))} />}</td>
                  <td dir="ltr">{opCode(o.number)}</td>
                  <td><span className={`mx-pill wh-k-${o.kind}`}>{t(`wh.k.${o.kind}`)}</span></td>
                  <td>{o.origin}{o.batch_number ? ` · ${t('wh.batchN', { n: o.batch_number })}` : ''}</td>
                  <td>{o.customer_name ?? ''}</td><td>{o.warehouse}</td><td>{formatDate(o.scheduled_date)}</td>
                  <td dir="ltr">{qtyFmt(o.quantity)} ({o.lines})</td>
                  <td><span className={`mx-pill ${o.status === 'ready' ? 'on' : o.status === 'cancelled' ? 'warn' : ''}`}>{t(`wh.st.${o.status}`)}</span></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <Flash m={msg} />
      </section>
      <aside className="mx-pay" style={{ flex: '1 1 380px', minWidth: 300 }}>
        {sel ? <OpDetail id={sel} me={me} onChanged={reload} onOpen={setSel} /> : canDo ? <NewTransfer whs={whs} onDone={(id) => { reload(); setSel(id); }} /> : <small>{t('wh.ops.pick')}</small>}
      </aside>
    </div>
  );
}

export function OpDetail({ id, me, onChanged, onOpen, compact }: { id: string; me: Me; onChanged?: () => void; onOpen?: (id: string) => void; compact?: boolean }) {
  const { t } = useTranslation();
  const base = useBase();
  const [d, reload] = useLoad<{ op: Op; lines: OpLine[] } | null>(base && `${base}/wh/ops/${id}`, null);
  const [done, setDone] = useState<Record<string, string>>({});
  const [to, setTo] = useState<Record<string, string>>({});
  const [pkg, setPkg] = useState<Record<string, string>>({});
  const [locs] = useLoad<Loc[]>(base && d && `${base}/wh/locations?warehouse_id=${d.op.warehouse_id}`, []);
  const [pkgs, reloadPkgs] = useLoad<Pkg[]>(base && d && d.op.kind === 'pack' && `${base}/wh/packages?status=open&order_id=${d.op.sales_order_id ?? ''}`, []);
  const [msg, run] = useRun(() => { reload(); onChanged?.(); });
  useEffect(() => { setDone({}); setTo({}); setPkg({}); }, [id]);
  if (!d) return <small>…</small>;
  const op = d.op;
  const ready = op.status === 'ready' && can(me, 'stock.create');
  const storage = locs.filter((l) => l.is_active);
  function finish() {
    if (!base) return;
    const lines = d!.lines.map((l) => ({ line_id: l.id, done_qty: done[l.id] ?? l.quantity, to_location_id: to[l.id] ?? '', package_id: pkg[l.id] ?? '' }));
    void run(api<{ next: { next_op_id: string | null; backorder_id: string | null } }>(`${base}/wh/ops/${id}/done`, { method: 'POST', body: { lines } }),
      (x) => { const n = (x as { next: { next_op_id: string | null } }).next; if (n?.next_op_id && onOpen) onOpen(n.next_op_id); });
  }
  async function newPackage() {
    if (!base) return;
    await run(api(`${base}/wh/packages`, { method: 'POST', body: { warehouse_id: op.warehouse_id, kind: 'box', sales_order_id: op.sales_order_id ?? '' } }), reloadPkgs);
  }
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <h3 style={{ margin: 0 }} dir="auto">{t(`wh.k.${op.kind}`)} · <span dir="ltr">{opCode(op.number)}</span> <span className="mx-pill">{t(`wh.st.${op.status}`)}</span></h3>
      <small>{op.origin} {op.customer_name ? `· ${op.customer_name}` : ''} · {op.warehouse} · {formatDate(op.scheduled_date)}</small>
      {!compact && <div className="mx-printhost-none" style={{ textAlign: 'center' }}><div style={{ maxWidth: 220, margin: '0 auto' }}><Barcode code={opCode(op.number)} height={30} /></div></div>}
      <div style={{ overflowX: 'auto' }}>
        <table className="mx-table">
          <thead><tr><th>{t('wh.item')}</th><th>{t('wh.from')}</th><th>{t('wh.to')}</th><th>{t('wh.qty')}</th>{ready && <th>{t('wh.doneQty')}</th>}{op.kind === 'pack' && <th>{t('wh.package')}</th>}</tr></thead>
          <tbody>{d.lines.map((l) => (
            <tr key={l.id}>
              <td>{l.item_code} · {l.item_name}{l.lot_no ? <small dir="ltr"> [{l.lot_no}]</small> : null}</td>
              <td dir="ltr" style={{ whiteSpace: 'nowrap' }}>{l.from_code ?? (op.kind === 'putaway' ? t('wh.arrival') : op.kind === 'ship' ? 'OUTPUT' : t('wh.unbinned'))}</td>
              <td dir="ltr">{ready && op.kind !== 'ship' ? (
                <select value={to[l.id] ?? l.to_location_id ?? ''} onChange={(e) => setTo({ ...to, [l.id]: e.currentTarget.value })} aria-label={t('wh.to')}>
                  <option value="">{l.to_code ?? t('wh.unbinned')}</option>
                  {storage.map((x) => <option key={x.id} value={x.id}>{x.code}</option>)}
                </select>) : (l.to_code ?? (op.kind === 'ship' ? t('wh.customer') : t('wh.unbinned')))}</td>
              <td dir="ltr">{qtyFmt(l.quantity)} {l.unit}</td>
              {ready && <td><input dir="ltr" inputMode="decimal" style={{ width: 70 }} value={done[l.id] ?? qtyFmt(l.quantity)} aria-label={t('wh.doneQty')}
                onChange={(e) => setDone({ ...done, [l.id]: e.currentTarget.value })} /></td>}
              {op.kind === 'pack' && <td>{ready ? (
                <select value={pkg[l.id] ?? l.package_id ?? ''} onChange={(e) => setPkg({ ...pkg, [l.id]: e.currentTarget.value })} aria-label={t('wh.package')}>
                  <option value="">—</option>{pkgs.filter((p) => p.status === 'open').map((p) => <option key={p.id} value={p.id}>{p.code}</option>)}
                </select>) : (l.package_number ? `PKG${String(l.package_number).padStart(6, '0')}` : '')}</td>}
            </tr>))}</tbody>
        </table>
      </div>
      {ready && <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
        <button className="mx-btn main" onClick={finish}>{t(`wh.doneBtn.${op.kind}`)}</button>
        {op.kind === 'pack' && <button className="mx-btn" onClick={() => void newPackage()}>{t('wh.pkg.new')}</button>}
        {can(me, 'stock.manage') && <button className="mx-btn danger" onClick={() => base && void run(api(`${base}/wh/ops/${id}/cancel`, { method: 'POST', body: {} }))}>{t('wh.cancel')}</button>}
      </div>}
      {!compact && <button className="mx-btn sm" onClick={() => void printPage()}>{t('wh.print')}</button>}
      <Flash m={msg} />
    </div>
  );
}

function NewTransfer({ whs, onDone }: { whs: Warehouse[]; onDone: (id: string) => void }) {
  const { t } = useTranslation();
  const base = useBase();
  const binWhs = whs.filter((w) => w.use_bins);
  const [wh, setWh] = useState('');
  const w = wh || binWhs[0]?.id || '';
  const [locs] = useLoad<Loc[]>(base && w && `${base}/wh/locations?warehouse_id=${w}`, []);
  const [items] = useLoad<ItemLite[]>(base && `${base}/items`, []);
  const [lines, setLines] = useState([{ item_id: '', quantity: '1', from_location_id: '', to_location_id: '' }]);
  const [msg, run] = useRun();
  if (binWhs.length === 0) return <div className="mx-note">{t('wh.noBins')} <button className="mx-link" onClick={() => openScreen('wh', 'setup')}>{t('wh.m.setup')}</button></div>;
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <h3 style={{ margin: 0 }}>{t('wh.ops.newTransfer')}</h3>
      <WhSelect value={w} onChange={setWh} list={binWhs} />
      {lines.map((l, i) => (
        <div key={i} className="mx-row" style={{ gap: 4, flexWrap: 'wrap', alignItems: 'end' }}>
          <label className="mx-fld" style={{ flex: 2 }}>{t('wh.item')}<select value={l.item_id} onChange={(e) => setLines(lines.map((x, j) => j === i ? { ...x, item_id: e.currentTarget.value } : x))}>
            <option value="">—</option>{items.filter((x) => x.kind === 'stock' && x.is_active).map((x) => <option key={x.id} value={x.id}>{x.code} · {x.name}</option>)}</select></label>
          <label className="mx-fld">{t('wh.from')}<select value={l.from_location_id} onChange={(e) => setLines(lines.map((x, j) => j === i ? { ...x, from_location_id: e.currentTarget.value } : x))}>
            <option value="">{t('wh.unbinned')}</option>{locs.map((x) => <option key={x.id} value={x.id}>{x.code}</option>)}</select></label>
          <label className="mx-fld">{t('wh.to')}<select value={l.to_location_id} onChange={(e) => setLines(lines.map((x, j) => j === i ? { ...x, to_location_id: e.currentTarget.value } : x))}>
            <option value="">{t('wh.unbinned')}</option>{locs.map((x) => <option key={x.id} value={x.id}>{x.code}</option>)}</select></label>
          <label className="mx-fld">{t('wh.qty')}<input dir="ltr" inputMode="decimal" style={{ width: 70 }} value={l.quantity} onChange={(e) => setLines(lines.map((x, j) => j === i ? { ...x, quantity: e.currentTarget.value } : x))} /></label>
        </div>))}
      <div className="mx-row" style={{ gap: 6 }}>
        <button className="mx-btn sm" onClick={() => setLines([...lines, { item_id: '', quantity: '1', from_location_id: '', to_location_id: '' }])}>{t('wh.addLine')}</button>
        <button className="mx-btn main" disabled={!lines.some((l) => l.item_id)} onClick={() => base && void run(api<{ op: Op }>(`${base}/wh/ops`, { method: 'POST',
          body: { warehouse_id: w, lines: lines.filter((l) => l.item_id) } }), (x) => onDone((x as { op: Op }).op.id))}>{t('wh.ops.create')}</button>
      </div>
      <Flash m={msg} />
    </div>
  );
}

// ======================= bins =======================

interface BinRow { id: string; warehouse: string; location_id: string; location_code: string; location_kind: string; item_id: string; item_code: string; item_name: string; unit: string; lot_no: string | null; expiry: string | null; quantity: string; in_date: string }
interface FreeRow { warehouse_id: string; warehouse: string; item_id: string; item_code: string; item_name: string; unit: string; quantity: string }

function Bins({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [whs] = useWarehouses();
  const [wh, setWh] = useState('');
  const [q, setQ] = useState('');
  const [d, reload] = useLoad<{ bins: BinRow[]; unbinned: FreeRow[] }>(base && `${base}/wh/bin-stock?warehouse_id=${wh}&q=${encodeURIComponent(q)}`, { bins: [], unbinned: [] });
  const [mv, setMv] = useState<{ item_id: string; warehouse_id: string; from: string; to: string; qty: string; label: string } | null>(null);
  const [locs] = useLoad<Loc[]>(base && mv && `${base}/wh/locations?warehouse_id=${mv.warehouse_id}`, []);
  const [msg, run] = useRun(() => { reload(); setMv(null); });
  const canMove = can(me, 'stock.create');
  return (
    <div className="mx-row" style={{ gap: 8, alignItems: 'flex-start', flexWrap: 'wrap' }}>
      <section className="mx-box" style={{ flex: '999 1 560px', minWidth: 0 }}>
        <h3>{t('wh.bins.title')}</h3>
        <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
          <WhSelect value={wh} onChange={setWh} all list={whs.filter((w) => w.use_bins)} />
          <label className="mx-fld">{t('wh.search')}<input value={q} onChange={(e) => setQ(e.currentTarget.value)} placeholder={t('wh.bins.searchHint')} /></label>
          <span className="mx-sp" /><button className="mx-btn sm" onClick={() => void printPage()}>{t('wh.print')}</button>
        </div>
        <div style={{ overflowX: 'auto' }}>
          <table className="mx-table">
            <thead><tr><th>{t('wh.location')}</th><th>{t('wh.item')}</th><th>{t('wh.lot')}</th><th>{t('wh.expiry')}</th><th>{t('wh.qty')}</th><th>{t('wh.since')}</th>{canMove && <th />}</tr></thead>
            <tbody>
              {d.bins.length === 0 && <tr><td colSpan={7} className="mx-empty">{t('wh.bins.none')}</td></tr>}
              {d.bins.map((b) => (
                <tr key={b.id}>
                  <td dir="ltr"><b>{b.location_code}</b> <small>{t(`wh.lk.${b.location_kind}`)}</small></td>
                  <td>{b.item_code} · {b.item_name}</td><td dir="ltr">{b.lot_no ?? ''}</td><td>{b.expiry ? formatDate(b.expiry) : ''}</td>
                  <td dir="ltr">{qtyFmt(b.quantity)} {b.unit}</td><td>{formatDate(b.in_date)}</td>
                  {canMove && <td><button className="mx-link" onClick={() => setMv({ item_id: b.item_id, warehouse_id: whs.find((w) => w.name === b.warehouse)?.id ?? wh, from: b.location_id, to: '', qty: qtyFmt(b.quantity), label: `${b.location_code} · ${b.item_code}` })}>{t('wh.move')}</button></td>}
                </tr>))}
            </tbody>
          </table>
        </div>
        {d.unbinned.length > 0 && <>
          <h3>{t('wh.bins.unbinned')}</h3>
          <table className="mx-table">
            <thead><tr><th>{t('wh.warehouse')}</th><th>{t('wh.item')}</th><th>{t('wh.qty')}</th>{canMove && <th />}</tr></thead>
            <tbody>{d.unbinned.map((u) => (
              <tr key={u.warehouse_id + u.item_id}><td>{u.warehouse}</td><td>{u.item_code} · {u.item_name}</td><td dir="ltr">{qtyFmt(u.quantity)} {u.unit}</td>
                {canMove && <td><button className="mx-link" onClick={() => setMv({ item_id: u.item_id, warehouse_id: u.warehouse_id, from: '', to: '', qty: qtyFmt(u.quantity), label: `${t('wh.unbinned')} · ${u.item_code}` })}>{t('wh.putaway')}</button></td>}</tr>))}</tbody>
          </table></>}
      </section>
      {mv && <aside className="mx-pay" style={{ flex: '1 1 320px' }}>
        <h3 style={{ margin: 0 }}>{t('wh.move')}: {mv.label}</h3>
        <label>{t('wh.to')}<select value={mv.to} onChange={(e) => setMv({ ...mv, to: e.currentTarget.value })}>
          <option value="">{t('wh.unbinned')}</option>{locs.filter((l) => l.id !== mv.from).map((l) => <option key={l.id} value={l.id}>{l.code}</option>)}</select></label>
        <label>{t('wh.qty')}<input dir="ltr" inputMode="decimal" value={mv.qty} onChange={(e) => setMv({ ...mv, qty: e.currentTarget.value })} /></label>
        <div className="mx-row" style={{ gap: 6 }}>
          <button className="mx-btn main" disabled={!mv.to && !mv.from} onClick={() => base && void run(api(`${base}/wh/move`, { method: 'POST',
            body: { warehouse_id: mv.warehouse_id, item_id: mv.item_id, quantity: mv.qty, from_location_id: mv.from, to_location_id: mv.to } }))}>{t('wh.move')}</button>
          <button className="mx-btn" onClick={() => setMv(null)}>{t('wh.close')}</button>
        </div>
        <Flash m={msg} />
      </aside>}
    </div>
  );
}

// ======================= batches =======================

interface Batch { id: string; number: number; warehouse_id: string; warehouse: string; kind: string; status: string; assigned_name: string | null; notes: string; created_at: string; done_at: string | null; ops: number; ops_ready: number }

function Batches({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [status, setStatus] = useState('open');
  const [list, reload] = useLoad<Batch[]>(base && `${base}/wh/batches?status=${status}`, []);
  const [sel, setSel] = useState<string | null>(null);
  const [d, reloadD] = useLoad<{ batch: Batch; ops: Op[]; lines: OpLine[] } | null>(base && sel && `${base}/wh/batches/${sel}`, null);
  const [done, setDone] = useState<Record<string, string>>({});
  const [msg, run] = useRun(() => { reload(); reloadD(); });
  const canDo = can(me, 'stock.create');
  return (
    <div className="mx-row" style={{ gap: 8, alignItems: 'flex-start', flexWrap: 'wrap' }}>
      <section className="mx-box" style={{ flex: '1 1 340px' }}>
        <h3>{t('wh.m.batches')}</h3>
        <div className="mx-row" style={{ gap: 6, padding: 6 }}>
          <label className="mx-fld">{t('wh.status')}<select value={status} onChange={(e) => setStatus(e.currentTarget.value)}>
            {['open', 'done', 'cancelled', 'all'].map((s) => <option key={s} value={s}>{t(`wh.st.${s}`)}</option>)}</select></label>
        </div>
        <table className="mx-table"><thead><tr><th>{t('wh.number')}</th><th>{t('wh.kind')}</th><th>{t('wh.warehouse')}</th><th>{t('wh.ops.count')}</th><th>{t('wh.status')}</th></tr></thead>
          <tbody>{list.length === 0 && <tr><td colSpan={5} className="mx-empty">{t('wh.batches.none')}</td></tr>}
            {list.map((b) => <tr key={b.id} className={sel === b.id ? 'sel' : ''} style={{ cursor: 'pointer' }} onClick={() => { setSel(b.id); setDone({}); }}>
              <td dir="ltr">B{b.number}</td><td>{t(`wh.bk.${b.kind}`)}</td><td>{b.warehouse}</td><td>{b.ops_ready}/{b.ops}</td><td>{t(`wh.st.${b.status}`)}</td></tr>)}</tbody></table>
      </section>
      {d && <section className="mx-box" style={{ flex: '999 1 520px', minWidth: 0 }}>
        <h3>{t('wh.batchN', { n: d.batch.number })} · {t(`wh.bk.${d.batch.kind}`)} · {d.batch.warehouse}</h3>
        <small style={{ padding: 6 }}>{t('wh.batches.walk')}</small>
        <table className="mx-table">
          <thead><tr><th>{t('wh.from')}</th><th>{t('wh.item')}</th><th>{t('wh.qty')}</th><th>{t('wh.op')}</th>{d.batch.status === 'open' && canDo && <th>{t('wh.doneQty')}</th>}</tr></thead>
          <tbody>{d.lines.map((l) => (
            <tr key={l.id}><td dir="ltr"><b>{l.from_code ?? t('wh.unbinned')}</b></td><td>{l.item_code} · {l.item_name}{l.lot_no ? ` [${l.lot_no}]` : ''}</td>
              <td dir="ltr">{qtyFmt(l.quantity)} {l.unit}</td><td dir="ltr">{opCode(l.op_number ?? 0)} <small>{l.origin}</small></td>
              {d.batch.status === 'open' && canDo && <td><input dir="ltr" inputMode="decimal" style={{ width: 70 }} aria-label={t('wh.doneQty')}
                value={done[l.id] ?? qtyFmt(l.quantity)} onChange={(e) => setDone({ ...done, [l.id]: e.currentTarget.value })} /></td>}</tr>))}</tbody>
        </table>
        <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap' }}>
          {d.batch.status === 'open' && canDo && <>
            <button className="mx-btn main" onClick={() => base && void run(api(`${base}/wh/batches/${d.batch.id}/done`, { method: 'POST',
              body: { lines: d.lines.map((l) => ({ line_id: l.id, done_qty: done[l.id] ?? l.quantity })) } }))}>{t('wh.batches.done')}</button>
            <button className="mx-btn" onClick={() => base && void run(api(`${base}/wh/batches/${d.batch.id}/cancel`, { method: 'POST', body: {} }))}>{t('wh.batches.release')}</button>
          </>}
          <button className="mx-btn sm" onClick={() => void printPage()}>{t('wh.batches.print')}</button>
        </div>
        <Flash m={msg} />
      </section>}
    </div>
  );
}

// ======================= packages =======================

function Packages({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const base = useBase();
  const [whs] = useWarehouses();
  const [status, setStatus] = useState('open');
  const [list, reload] = useLoad<Pkg[]>(base && `${base}/wh/packages?status=${status}`, []);
  const [sel, setSel] = useState<string | null>(null);
  const [d, reloadD] = useLoad<{ package: Pkg; lines: { id: string; item_code: string; item_name: string; unit: string; lot_no: string | null; quantity: string }[]; children: { id: string; code: string; kind: string; status: string }[] } | null>(
    base && sel && `${base}/wh/packages/${sel}`, null);
  const [nw, setNw] = useState({ warehouse_id: '', kind: 'box', weight: '' });
  const [ed, setEd] = useState<Partial<Pkg>>({});
  const [msg, run] = useRun(() => { reload(); reloadD(); });
  const canDo = can(me, 'stock.create');
  useEffect(() => setEd({}), [sel]);
  const p = d?.package;
  const pallets = list.filter((x) => x.kind === 'pallet' && x.status !== 'shipped' && x.id !== sel);
  return (
    <div className="mx-row" style={{ gap: 8, alignItems: 'flex-start', flexWrap: 'wrap' }}>
      <section className="mx-box" style={{ flex: '999 1 520px', minWidth: 0 }}>
        <h3>{t('wh.m.packages')}</h3>
        <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
          <label className="mx-fld">{t('wh.status')}<select value={status} onChange={(e) => setStatus(e.currentTarget.value)}>
            {['open', 'shipped', 'cancelled', 'all'].map((s) => <option key={s} value={s}>{t(`wh.st.${s}`)}</option>)}</select></label>
          {canDo && <>
            <span className="mx-sp" />
            <WhSelect value={nw.warehouse_id || whs[0]?.id || ''} onChange={(v) => setNw({ ...nw, warehouse_id: v })} list={whs} />
            <label className="mx-fld">{t('wh.kind')}<select value={nw.kind} onChange={(e) => setNw({ ...nw, kind: e.currentTarget.value })}>
              <option value="box">{t('wh.pk.box')}</option><option value="pallet">{t('wh.pk.pallet')}</option></select></label>
            <button className="mx-btn main" onClick={() => base && void run(api<{ package: Pkg }>(`${base}/wh/packages`, { method: 'POST',
              body: { warehouse_id: nw.warehouse_id || whs[0]?.id, kind: nw.kind } }), (x) => setSel((x as { package: Pkg }).package.id))}>{t('wh.pkg.new')}</button>
          </>}
        </div>
        <table className="mx-table">
          <thead><tr><th>{t('wh.code')}</th><th>{t('wh.kind')}</th><th>{t('wh.order')}</th><th>{t('wh.customer')}</th><th>{t('wh.weight')}</th><th>{t('wh.content')}</th><th>{t('wh.status')}</th></tr></thead>
          <tbody>{list.length === 0 && <tr><td colSpan={7} className="mx-empty">{t('wh.pkg.none')}</td></tr>}
            {list.map((x) => <tr key={x.id} className={sel === x.id ? 'sel' : ''} style={{ cursor: 'pointer' }} onClick={() => setSel(x.id)}>
              <td dir="ltr">{x.code}{x.parent_code ? <small> ⊂ {x.parent_code}</small> : null}</td><td>{t(`wh.pk.${x.kind}`)}</td><td dir="ltr">{x.order_number ?? ''}</td>
              <td>{x.customer_name ?? ''}</td><td dir="ltr">{qtyFmt(x.weight)} kg</td><td>{x.kind === 'pallet' ? x.children : x.lines}</td><td>{t(`wh.st.${x.status}`)}</td></tr>)}</tbody>
        </table>
      </section>
      {p && d && <aside className="mx-pay" style={{ flex: '1 1 360px' }}>
        <h3 style={{ margin: 0 }} dir="ltr">{p.code} <span className="mx-pill">{t(`wh.st.${p.status}`)}</span></h3>
        <div className="mx-paper" style={{ border: '1px dashed #999', padding: 8, background: '#fff', color: '#000' }}>
          <b>{i18n.language === 'ar' && p.company_name_ar ? p.company_name_ar : p.company_name}</b>
          <div style={{ maxWidth: 260 }}><Barcode code={p.code} height={40} /></div>
          {p.customer_name && <div>{t('wh.shipTo')}: <b>{p.customer_name}</b><br /><span style={{ whiteSpace: 'pre-line' }}>{p.customer_address ?? ''}</span></div>}
          <small dir="ltr">{t(`wh.pk.${p.kind}`)} · {qtyFmt(p.weight)} kg · {qtyFmt(p.length_cm)}×{qtyFmt(p.width_cm)}×{qtyFmt(p.height_cm)} cm{p.order_number ? ` · SO ${p.order_number}` : ''}</small>
        </div>
        <table className="mx-table"><thead><tr><th>{t('wh.item')}</th><th>{t('wh.qty')}</th></tr></thead>
          <tbody>{d.lines.map((l) => <tr key={l.id}><td>{l.item_code} · {l.item_name}{l.lot_no ? ` [${l.lot_no}]` : ''}</td><td dir="ltr">{qtyFmt(l.quantity)} {l.unit}</td></tr>)}
            {d.children.map((c) => <tr key={c.id}><td dir="ltr">{c.code}</td><td>{t(`wh.pk.${c.kind}`)}</td></tr>)}</tbody></table>
        {canDo && (p.status === 'open' || p.status === 'sealed') && <>
          <div className="mx-row" style={{ gap: 4, flexWrap: 'wrap' }}>
            {(['weight', 'length_cm', 'width_cm', 'height_cm'] as const).map((k) => (
              <label key={k} className="mx-fld">{t(`wh.dim.${k}`)}<input dir="ltr" inputMode="decimal" style={{ width: 70 }} value={(ed[k] as string | undefined) ?? qtyFmt(p[k])}
                onChange={(e) => setEd({ ...ed, [k]: e.currentTarget.value })} /></label>))}
          </div>
          {p.kind === 'box' && <label>{t('wh.pkg.onPallet')}<select value={(ed.parent_id as string | undefined) ?? p.parent_id ?? ''} onChange={(e) => setEd({ ...ed, parent_id: e.currentTarget.value })}>
            <option value="">—</option>{pallets.map((x) => <option key={x.id} value={x.id}>{x.code}</option>)}</select></label>}
          <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
            <button className="mx-btn main" onClick={() => base && void run(api(`${base}/wh/packages/${p.id}`, { method: 'PATCH', body: ed }))}>{t('wh.save')}</button>
            {p.status === 'open' && <button className="mx-btn" onClick={() => base && void run(api(`${base}/wh/packages/${p.id}`, { method: 'PATCH', body: { status: 'sealed' } }))}>{t('wh.pkg.seal')}</button>}
            {p.status === 'sealed' && <button className="mx-btn" onClick={() => base && void run(api(`${base}/wh/packages/${p.id}`, { method: 'PATCH', body: { status: 'open' } }))}>{t('wh.pkg.reopen')}</button>}
          </div></>}
        <button className="mx-btn sm" onClick={() => void printPage()}>{t('wh.pkg.label')}</button>
        <Flash m={msg} />
      </aside>}
    </div>
  );
}

// ======================= delivery board =======================

interface Vehicle { id: string; name: string; plate: string; capacity_kg: string; driver_id: string | null; driver_name: string | null; is_active: boolean; notes: string }
interface Trip { id: string; number: number; trip_date: string; vehicle_id: string | null; vehicle_name: string | null; plate: string | null; driver_id: string | null; driver_name: string | null; status: string; notes: string; stops: number; delivered: number; failed: number }
export interface Stop {
  id: string; seq: number; sales_delivery_id: string | null; delivery_number: number | null; sales_order_id: string | null; order_number: number | null; customer_name: string | null;
  address: string; phone: string; note: string; status: string; pod_name: string; signed: boolean; pod_note: string; delivered_at: string | null; packages: number;
  package_list: { code: string; kind: string; weight: string }[] | null; photos: number; lat: number | null; lng: number | null;
}
interface ToSched {
  deliveries: { id: string; number: number; delivery_date: string; order_number: number; customer_name: string; address: string; phone: string; packages: number; weight: string }[];
  orders: { id: string; order_number: number; order_date: string; customer_name: string; address: string; phone: string }[];
}

function Trips({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [date, setDate] = useState(today());
  const [trips, reloadTrips] = useLoad<Trip[]>(base && `${base}/wh/trips?date=${date}`, []);
  const [todo, reloadTodo] = useLoad<ToSched>(base && `${base}/wh/to-schedule`, { deliveries: [], orders: [] });
  const [vehicles, reloadV] = useLoad<Vehicle[]>(base && `${base}/wh/vehicles`, []);
  const [people] = useLoad<{ id: string; full_name: string }[]>(base && `${base}/mentionable`, []);
  const [pick, setPick] = useState<string[]>([]);
  const [veh, setVeh] = useState('');
  const [drv, setDrv] = useState('');
  const [sel, setSel] = useState<string | null>(null);
  const [nv, setNv] = useState({ name: '', plate: '', capacity_kg: '' });
  const [msg, run] = useRun(() => { reloadTrips(); reloadTodo(); });
  const canDo = can(me, 'stock.create');
  const stopsOf = (ids: string[]) => ids.map((k) => (k.startsWith('d:') ? { sales_delivery_id: k.slice(2) } : { sales_order_id: k.slice(2) }));
  const totalW = todo.deliveries.filter((x) => pick.includes('d:' + x.id)).reduce((s, x) => s + Number(x.weight || 0), 0);
  const cap = Number(vehicles.find((v) => v.id === veh)?.capacity_kg ?? 0);
  return (
    <div className="mx-row" style={{ gap: 8, alignItems: 'flex-start', flexWrap: 'wrap' }}>
      <section className="mx-box" style={{ flex: '1 1 420px', minWidth: 0 }}>
        <h3>{t('wh.trips.toSchedule')}</h3>
        <table className="mx-table">
          <thead><tr><th /><th>{t('wh.doc')}</th><th>{t('wh.customer')}</th><th>{t('wh.address')}</th><th>{t('wh.weight')}</th></tr></thead>
          <tbody>
            {todo.deliveries.length + todo.orders.length === 0 && <tr><td colSpan={5} className="mx-empty">{t('wh.trips.nothing')}</td></tr>}
            {todo.deliveries.map((x) => { const k = 'd:' + x.id; return (
              <tr key={k}><td><input type="checkbox" aria-label={String(x.number)} checked={pick.includes(k)} onChange={(e) => setPick(e.currentTarget.checked ? [...pick, k] : pick.filter((y) => y !== k))} /></td>
                <td dir="ltr">DEL-{x.number} <small>SO {x.order_number}</small></td><td>{x.customer_name}</td><td>{x.address}</td><td dir="ltr">{qtyFmt(x.weight)} kg{x.packages ? ` · ${x.packages} ${t('wh.pkgShort')}` : ''}</td></tr>); })}
            {todo.orders.map((x) => { const k = 'o:' + x.id; return (
              <tr key={k}><td><input type="checkbox" aria-label={String(x.order_number)} checked={pick.includes(k)} onChange={(e) => setPick(e.currentTarget.checked ? [...pick, k] : pick.filter((y) => y !== k))} /></td>
                <td dir="ltr">SO {x.order_number} <small>{t('wh.trips.notDelivered')}</small></td><td>{x.customer_name}</td><td>{x.address}</td><td /></tr>); })}
          </tbody>
        </table>
        {canDo && <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
          <label className="mx-fld">{t('wh.date')}<input type="date" dir="ltr" value={date} onChange={(e) => setDate(e.currentTarget.value)} /></label>
          <label className="mx-fld">{t('wh.vehicle')}<select value={veh} onChange={(e) => setVeh(e.currentTarget.value)}><option value="">—</option>
            {vehicles.filter((v) => v.is_active).map((v) => <option key={v.id} value={v.id}>{v.name} {v.plate}</option>)}</select></label>
          <label className="mx-fld">{t('wh.driver')}<select value={drv} onChange={(e) => setDrv(e.currentTarget.value)}><option value="">{t('wh.trips.vehicleDriver')}</option>
            {people.map((p) => <option key={p.id} value={p.id}>{p.full_name}</option>)}</select></label>
          <button className="mx-btn main" disabled={pick.length === 0} onClick={() => base && void run(api<{ trip: Trip }>(`${base}/wh/trips`, { method: 'POST',
            body: { trip_date: date, vehicle_id: veh, driver_id: drv, stops: stopsOf(pick) } }), (x) => { setPick([]); setSel((x as { trip: Trip }).trip.id); })}>{t('wh.trips.create', { n: pick.length })}</button>
          {cap > 0 && totalW > cap && <span className="mx-pill warn">{t('wh.trips.overweight', { w: totalW, c: cap })}</span>}
        </div>}
        <Flash m={msg} />
      </section>
      <section className="mx-box" style={{ flex: '1 1 420px', minWidth: 0 }}>
        <h3>{t('wh.trips.title', { d: formatDate(date) })}</h3>
        <div className="mx-row" style={{ gap: 6, padding: 6 }}>
          <label className="mx-fld">{t('wh.date')}<input type="date" dir="ltr" value={date} onChange={(e) => setDate(e.currentTarget.value)} /></label>
        </div>
        <table className="mx-table">
          <thead><tr><th>{t('wh.number')}</th><th>{t('wh.vehicle')}</th><th>{t('wh.driver')}</th><th>{t('wh.trips.stops')}</th><th>{t('wh.status')}</th></tr></thead>
          <tbody>{trips.length === 0 && <tr><td colSpan={5} className="mx-empty">{t('wh.trips.none')}</td></tr>}
            {trips.map((x) => <tr key={x.id} className={sel === x.id ? 'sel' : ''} style={{ cursor: 'pointer' }} onClick={() => setSel(x.id)}>
              <td dir="ltr">T{x.number}</td><td>{x.vehicle_name ?? ''} <small dir="ltr">{x.plate ?? ''}</small></td><td>{x.driver_name ?? ''}</td>
              <td>{x.delivered}/{x.stops}{x.failed ? ` · ✕${x.failed}` : ''}</td><td>{t(`wh.ts.${x.status}`)}</td></tr>)}</tbody>
        </table>
        {sel && <TripDetail id={sel} me={me} onChanged={() => { reloadTrips(); reloadTodo(); }} />}
        {can(me, 'stock.manage') && <details style={{ padding: 6 }}>
          <summary>{t('wh.vehicles')}</summary>
          <table className="mx-table"><tbody>{vehicles.map((v) => <tr key={v.id}><td>{v.name}</td><td dir="ltr">{v.plate}</td><td dir="ltr">{qtyFmt(v.capacity_kg)} kg</td>
            <td><select value={v.driver_id ?? ''} aria-label={t('wh.driver')} onChange={(e) => base && void run(api(`${base}/wh/vehicles/${v.id}`, { method: 'PATCH', body: { driver_id: e.currentTarget.value } }), reloadV)}>
              <option value="">—</option>{people.map((p) => <option key={p.id} value={p.id}>{p.full_name}</option>)}</select></td></tr>)}</tbody></table>
          <div className="mx-row" style={{ gap: 4, flexWrap: 'wrap', alignItems: 'end' }}>
            <label className="mx-fld">{t('wh.name')}<input value={nv.name} onChange={(e) => setNv({ ...nv, name: e.currentTarget.value })} /></label>
            <label className="mx-fld">{t('wh.plate')}<input dir="ltr" value={nv.plate} onChange={(e) => setNv({ ...nv, plate: e.currentTarget.value })} /></label>
            <label className="mx-fld">{t('wh.capacity')}<input dir="ltr" inputMode="decimal" style={{ width: 80 }} value={nv.capacity_kg} onChange={(e) => setNv({ ...nv, capacity_kg: e.currentTarget.value })} /></label>
            <button className="mx-btn" disabled={!nv.name.trim()} onClick={() => base && void run(api(`${base}/wh/vehicles`, { method: 'POST', body: nv }), () => { setNv({ name: '', plate: '', capacity_kg: '' }); reloadV(); })}>{t('wh.add')}</button>
          </div>
        </details>}
      </section>
    </div>
  );
}

function TripDetail({ id, me, onChanged }: { id: string; me: Me; onChanged: () => void }) {
  const { t } = useTranslation();
  const base = useBase();
  const [d, reload] = useLoad<{ trip: Trip; stops: Stop[] } | null>(base && `${base}/wh/trips/${id}`, null);
  const [sig, setSig] = useState<Record<string, string>>({});
  const [msg, run] = useRun(() => { reload(); onChanged(); });
  if (!d) return null;
  const tr = d.trip;
  const canDo = can(me, 'stock.create');
  function showSig(s: Stop) {
    if (!base) return;
    api<{ pod_signature: string }>(`${base}/wh/stops/${s.id}/signature`).then((x) => setSig((m) => ({ ...m, [s.id]: x.pod_signature }))).catch(() => {});
  }
  return (
    <div style={{ borderTop: '1px solid #ccc', marginTop: 6, padding: 6 }}>
      <h3 style={{ margin: 0 }}>{t('wh.trips.trip', { n: tr.number })} · {formatDate(tr.trip_date)} · {tr.vehicle_name ?? ''} · {tr.driver_name ?? ''} <span className="mx-pill">{t(`wh.ts.${tr.status}`)}</span></h3>
      <table className="mx-table">
        <thead><tr><th>#</th><th>{t('wh.customer')}</th><th>{t('wh.address')}</th><th>{t('wh.doc')}</th><th>{t('wh.status')}</th><th>{t('wh.pod')}</th></tr></thead>
        <tbody>{d.stops.map((s) => (
          <tr key={s.id}><td>{s.seq}</td><td>{s.customer_name ?? ''}<br /><small dir="ltr">{s.phone}</small></td><td>{s.address}</td>
            <td dir="ltr">{s.delivery_number ? `DEL-${s.delivery_number}` : s.order_number ? `SO ${s.order_number}` : ''}{s.package_list ? <small> {s.package_list.map((p) => p.code).join(', ')}</small> : null}</td>
            <td><span className={`mx-pill ${s.status === 'delivered' ? 'on' : s.status === 'failed' ? 'warn' : ''}`}>{t(`wh.ss.${s.status}`)}</span></td>
            <td>{s.status !== 'pending' && <>{s.pod_name} {s.delivered_at ? <small>{formatDateTime(s.delivered_at)}</small> : null}
              {s.signed && (sig[s.id] ? <img src={sig[s.id]} alt={t('wh.signature')} style={{ maxHeight: 50, display: 'block', background: '#fff' }} /> : <button className="mx-link" onClick={() => showSig(s)}>{t('wh.signature')}</button>)}
              {s.pod_note && <div><small>{s.pod_note}</small></div>}
              {s.lat != null && s.lng != null && <div><a href={`https://maps.google.com/?q=${s.lat},${s.lng}`} target="_blank" rel="noreferrer" dir="ltr">{s.lat}, {s.lng}</a></div>}</>}</td></tr>))}</tbody>
      </table>
      <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap', paddingTop: 6 }}>
        {canDo && tr.status === 'planned' && <button className="mx-btn main" onClick={() => base && void run(api(`${base}/wh/trips/${id}`, { method: 'PATCH', body: { status: 'out' } }))}>{t('wh.trips.start')}</button>}
        {canDo && (tr.status === 'planned' || tr.status === 'out') && <>
          <button className="mx-btn" onClick={() => base && void run(api(`${base}/wh/trips/${id}`, { method: 'PATCH', body: { status: 'done' } }))}>{t('wh.trips.finish')}</button>
          <button className="mx-btn danger" onClick={() => base && void run(api(`${base}/wh/trips/${id}`, { method: 'PATCH', body: { status: 'cancelled' } }))}>{t('wh.cancel')}</button></>}
        <button className="mx-btn sm" onClick={() => void printPage()}>{t('wh.trips.print')}</button>
        <button className="mx-btn sm" onClick={() => openScreen('driver')}>{t('wh.trips.driverView')}</button>
      </div>
      <Flash m={msg} />
    </div>
  );
}

// ======================= dropship =======================

interface OrderRow { id: string; number: number; order_number: number | null; customer_name: string; currency: string; total: string; status: string }
interface OrderLine { id: string; item_id: string | null; description: string; quantity: string; qty_delivered: string; item_kind?: string }

function Dropship({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [orders] = useLoad<{ rows: OrderRow[] }>(base && `${base}/sales/orders?status=confirmed`, { rows: [] });
  const [suppliers] = useLoad<{ id: string; name: string; code?: string }[]>(base && `${base}/suppliers`, []);
  const [so, setSo] = useState('');
  const [sup, setSup] = useState('');
  const [od] = useLoad<{ order: OrderRow; lines: OrderLine[] } | null>(base && so && `${base}/sales/orders/${so}`, null);
  const [qty, setQty] = useState<Record<string, string>>({});
  const [price, setPrice] = useState<Record<string, string>>({});
  const [msg, run] = useRun();
  const [made, setMade] = useState<{ id: string; number: number } | null>(null);
  const open = (od?.lines ?? []).filter((l) => l.item_id && Number(l.quantity) > Number(l.qty_delivered));
  if (!can(me, 'purchasing.create')) return <div className="mx-note">{t('wh.noPerm')}</div>;
  return (
    <section className="mx-box" style={{ maxWidth: 900 }}>
      <h3>{t('wh.ds.title')}</h3>
      <p style={{ padding: 6, margin: 0 }}>{t('wh.ds.how')}</p>
      <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
        <label className="mx-fld">{t('wh.order')}<select value={so} onChange={(e) => { setSo(e.currentTarget.value); setQty({}); setPrice({}); setMade(null); }}>
          <option value="">—</option>{orders.rows.map((o) => <option key={o.id} value={o.id}>SO {o.order_number ?? o.number} · {o.customer_name}</option>)}</select></label>
        <label className="mx-fld">{t('wh.supplier')}<select value={sup} onChange={(e) => setSup(e.currentTarget.value)}>
          <option value="">—</option>{suppliers.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}</select></label>
      </div>
      {od && <table className="mx-table">
        <thead><tr><th>{t('wh.item')}</th><th>{t('wh.ds.open')}</th><th>{t('wh.qty')}</th><th>{t('wh.ds.cost')}</th></tr></thead>
        <tbody>{open.length === 0 && <tr><td colSpan={4} className="mx-empty">{t('wh.ds.nothing')}</td></tr>}
          {open.map((l) => { const left = Number(l.quantity) - Number(l.qty_delivered); return (
            <tr key={l.id}><td>{l.description}</td><td dir="ltr">{left}</td>
              <td><input dir="ltr" inputMode="decimal" style={{ width: 70 }} value={qty[l.id] ?? String(left)} aria-label={t('wh.qty')} onChange={(e) => setQty({ ...qty, [l.id]: e.currentTarget.value })} /></td>
              <td><input dir="ltr" inputMode="decimal" style={{ width: 90 }} value={price[l.id] ?? ''} placeholder={t('wh.ds.fromList')} aria-label={t('wh.ds.cost')} onChange={(e) => setPrice({ ...price, [l.id]: e.currentTarget.value })} /></td></tr>); })}</tbody>
      </table>}
      <div className="mx-row" style={{ gap: 6, padding: 6 }}>
        <button className="mx-btn main" disabled={!so || !sup || open.length === 0} onClick={() => base && void run(api<{ order: { id: string; number: number } }>(`${base}/wh/dropship`, { method: 'POST',
          body: { order_id: so, supplier_id: sup, lines: open.map((l) => ({ line_id: l.id, quantity: qty[l.id] ?? String(Number(l.quantity) - Number(l.qty_delivered)), unit_price: price[l.id] ?? '' }))
            .filter((l) => Number(l.quantity) > 0) } }), (x) => setMade((x as { order: { id: string; number: number } }).order))}>{t('wh.ds.create')}</button>
        {made && <button className="mx-btn" onClick={() => openScreen('po')}>{t('wh.ds.openPO', { n: made.number })}</button>}
      </div>
      <Flash m={msg} />
    </section>
  );
}

// ======================= reports =======================

function Reports() {
  const { t } = useTranslation();
  const base = useBase();
  const [rep, setRep] = useState<'slow' | 'abc' | 'ageing' | 'valuation'>('slow');
  const [days, setDays] = useState('90');
  const [months, setMonths] = useState('12');
  const path = base && (rep === 'slow' ? `${base}/wh/reports/slow?days=${Number(days) || 90}&dead_days=${Math.max(Number(days) || 90, 365)}`
    : rep === 'abc' ? `${base}/wh/reports/abc?months=${Number(months) || 12}` : `${base}/wh/reports/${rep}`);
  const [data] = useLoad<unknown>(path, null);
  const rows = (Array.isArray(data) ? data : []) as Record<string, string>[];
  const val = (!Array.isArray(data) && data ? data : null) as { base_currency: string; warehouses: Record<string, string>[]; accounts: Record<string, string>[] } | null;
  const abcSum = useMemo(() => ['A', 'B', 'C'].map((c) => ({ c, n: rows.filter((r) => r.class === c).length })), [rows]);
  return (
    <section className="mx-box">
      <h3>{t('wh.m.reports')}</h3>
      <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
        <div className="mx-modes">{(['slow', 'abc', 'ageing', 'valuation'] as const).map((r) => <button key={r} className="mx-mode" aria-selected={rep === r} onClick={() => setRep(r)}>{t(`wh.rep.${r}`)}</button>)}</div>
        {rep === 'slow' && <label className="mx-fld">{t('wh.rep.idleDays')}<input dir="ltr" inputMode="numeric" style={{ width: 70 }} value={days} onChange={(e) => setDays(e.currentTarget.value)} /></label>}
        {rep === 'abc' && <label className="mx-fld">{t('wh.rep.months')}<input dir="ltr" inputMode="numeric" style={{ width: 70 }} value={months} onChange={(e) => setMonths(e.currentTarget.value)} /></label>}
        <span className="mx-sp" /><button className="mx-btn sm" onClick={() => void printPage()}>{t('wh.print')}</button>
      </div>
      <small style={{ padding: 6, display: 'block' }}>{t(`wh.rep.${rep}Help`)}</small>
      <div style={{ overflowX: 'auto' }}>
        {rep === 'slow' && <table className="mx-table"><thead><tr><th>{t('wh.item')}</th><th>{t('wh.warehouse')}</th><th>{t('wh.qty')}</th><th>{t('wh.value')}</th><th>{t('wh.rep.lastOut')}</th><th>{t('wh.rep.idle')}</th></tr></thead>
          <tbody>{rows.length === 0 && <tr><td colSpan={6} className="mx-empty">{t('wh.rep.none')}</td></tr>}
            {rows.map((r) => <tr key={r.item_id + r.warehouse_id}><td>{r.code} · {r.name}</td><td>{r.warehouse}</td><td dir="ltr">{qtyFmt(r.quantity)} {r.unit}</td><td dir="ltr">{formatAmount(r.value)}</td>
              <td>{r.last_out ? formatDate(r.last_out) : t('wh.rep.never')}</td><td><span className={`mx-pill ${Number(r.idle_days) >= 365 ? 'warn' : ''}`}>{r.idle_days} {t('wh.rep.days')}{Number(r.idle_days) >= 365 ? ` · ${t('wh.rep.dead')}` : ''}</span></td></tr>)}</tbody></table>}
        {rep === 'abc' && <>
          <div className="mx-row" style={{ gap: 6, padding: 6 }}>{abcSum.map((x) => <span key={x.c} className="mx-pill">{x.c}: {x.n}</span>)}</div>
          <table className="mx-table"><thead><tr><th>{t('wh.rep.class')}</th><th>{t('wh.item')}</th><th>{t('wh.rep.used')}</th><th>{t('wh.rep.usedQty')}</th><th>{t('wh.rep.share')}</th><th>{t('wh.rep.onHand')}</th></tr></thead>
            <tbody>{rows.length === 0 && <tr><td colSpan={6} className="mx-empty">{t('wh.rep.none')}</td></tr>}
              {rows.map((r) => <tr key={r.item_id}><td><b>{r.class}</b></td><td>{r.code} · {r.name}</td><td dir="ltr">{formatAmount(r.used_value)}</td><td dir="ltr">{qtyFmt(r.used_qty)} {r.unit}</td>
                <td dir="ltr">{r.share}% ({r.cum_share}%)</td><td dir="ltr">{qtyFmt(r.on_hand)}</td></tr>)}</tbody></table></>}
        {rep === 'ageing' && <table className="mx-table"><thead><tr><th>{t('wh.item')}</th><th>{t('wh.warehouse')}</th><th>{t('wh.qty')}</th><th>0–30</th><th>31–90</th><th>91–180</th><th>181–365</th><th>&gt;365</th><th>{t('wh.rep.oldest')}</th></tr></thead>
          <tbody>{rows.length === 0 && <tr><td colSpan={9} className="mx-empty">{t('wh.rep.none')}</td></tr>}
            {rows.map((r) => <tr key={r.item_id + r.warehouse}><td>{r.code} · {r.name}</td><td>{r.warehouse}</td><td dir="ltr">{qtyFmt(r.quantity)}</td>
              {['d30', 'd90', 'd180', 'd365', 'older'].map((k) => <td key={k} dir="ltr">{Number(r[k]) ? qtyFmt(r[k]) : ''}</td>)}<td>{r.oldest_days} {t('wh.rep.days')}</td></tr>)}</tbody></table>}
        {rep === 'valuation' && val && <>
          <table className="mx-table"><thead><tr><th>{t('wh.warehouse')}</th><th>{t('wh.rep.items')}</th><th>{t('wh.value')} ({val.base_currency})</th></tr></thead>
            <tbody>{val.warehouses.map((w) => <tr key={w.id}><td>{w.name}</td><td>{w.items}</td><td dir="ltr">{formatAmount(w.value)}</td></tr>)}</tbody></table>
          <table className="mx-table" style={{ marginTop: 8 }}><thead><tr><th>{t('wh.rep.account')}</th><th>{t('wh.rep.stockValue')}</th><th>{t('wh.rep.ledger')}</th><th>{t('wh.rep.diff')}</th></tr></thead>
            <tbody>{val.accounts.map((a) => <tr key={a.id}><td>{a.code} · {a.name}</td><td dir="ltr">{formatAmount(a.stock_value)}</td><td dir="ltr">{formatAmount(a.ledger)}</td>
              <td dir="ltr"><span className={`mx-pill ${Number(a.difference) === 0 ? 'on' : 'warn'}`}>{Number(a.difference) === 0 ? t('wh.rep.tied') : formatAmount(a.difference)}</span></td></tr>)}</tbody></table></>}
      </div>
    </section>
  );
}

// ======================= setup =======================

interface Rule { id: string; warehouse: string; item_code: string | null; item_name: string | null; category_name: string | null; location_code: string; priority: number }

function Setup({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [whs, reloadW] = useWarehouses();
  const [ed, setEd] = useState<Record<string, Partial<Warehouse>>>({});
  const [wsel, setWsel] = useState('');
  const w = wsel || whs.find((x) => !x.is_transit)?.id || '';
  const [locs, reloadL] = useLoad<Loc[]>(base && w && `${base}/wh/locations?warehouse_id=${w}`, []);
  const [rules, reloadR] = useLoad<Rule[]>(base && `${base}/wh/putaway-rules`, []);
  const [items] = useLoad<ItemLite[]>(base && `${base}/items`, []);
  const [cats] = useLoad<{ id: string; name: string }[]>(base && `${base}/pos/categories`, []);
  const [gen, setGen] = useState({ zone: 'A', aisles: '2', shelves: '3', bins: '4' });
  const [nl, setNl] = useState({ kind: 'bin', code: '', name: '', parent_id: '' });
  const [nr, setNr] = useState({ item_id: '', category_id: '', location_id: '', priority: '10' });
  const [labels, setLabels] = useState(false);
  const [msg, run] = useRun(() => { reloadW(); reloadL(); reloadR(); });
  const manage = can(me, 'stock.manage');
  const val = (x: Warehouse) => ({ ...x, ...ed[x.id] });
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      <section className="mx-box">
        <h3>{t('wh.setup.warehouses')}</h3>
        <div style={{ overflowX: 'auto' }}>
          <table className="mx-table">
            <thead><tr><th>{t('wh.name')}</th><th>{t('wh.code')}</th><th>{t('wh.setup.bins')}</th><th>{t('wh.setup.reception')}</th><th>{t('wh.setup.delivery')}</th>
              <th>{t('wh.setup.removal')}</th><th>{t('wh.setup.crossdock')}</th><th>{t('wh.value')}</th>{manage && <th />}</tr></thead>
            <tbody>{whs.map((x0) => { const x = val(x0); const set = (p: Partial<Warehouse>) => setEd({ ...ed, [x.id]: { ...ed[x.id], ...p } }); return (
              <tr key={x.id}>
                <td><input disabled={!manage} value={x.name} onChange={(e) => set({ name: e.currentTarget.value })} />{x.is_transit && <small> ({t('wh.transit')})</small>}</td>
                <td><input disabled={!manage} dir="ltr" style={{ width: 60 }} value={x.code} onChange={(e) => set({ code: e.currentTarget.value })} /></td>
                <td><input type="checkbox" disabled={!manage || x.is_transit} checked={x.use_bins} aria-label={t('wh.setup.bins')} onChange={(e) => set(e.currentTarget.checked ? { use_bins: true } : { use_bins: false, reception_steps: 1, delivery_steps: 1 })} /></td>
                <td><select disabled={!manage || !x.use_bins} value={x.reception_steps} aria-label={t('wh.setup.reception')} onChange={(e) => set({ reception_steps: Number(e.currentTarget.value) })}>
                  {[1, 2, 3].map((n) => <option key={n} value={n}>{t(`wh.setup.rs${n}`)}</option>)}</select></td>
                <td><select disabled={!manage || !x.use_bins} value={x.delivery_steps} aria-label={t('wh.setup.delivery')} onChange={(e) => set({ delivery_steps: Number(e.currentTarget.value) })}>
                  {[1, 2, 3].map((n) => <option key={n} value={n}>{t(`wh.setup.ds${n}`)}</option>)}</select></td>
                <td><select disabled={!manage} value={x.removal} aria-label={t('wh.setup.removal')} onChange={(e) => set({ removal: e.currentTarget.value })}>
                  {['fifo', 'fefo', 'closest'].map((r) => <option key={r} value={r}>{t(`wh.setup.rm.${r}`)}</option>)}</select></td>
                <td><input type="checkbox" disabled={!manage || !x.use_bins} checked={x.crossdock} aria-label={t('wh.setup.crossdock')} onChange={(e) => set({ crossdock: e.currentTarget.checked })} /></td>
                <td dir="ltr">{formatAmount(x.value)}</td>
                {manage && <td><button className="mx-btn sm main" disabled={!ed[x.id]} onClick={() => base && void run(api(`${base}/wh/warehouses/${x.id}`, { method: 'PATCH', body: ed[x.id] }), () => setEd({ ...ed, [x.id]: undefined as unknown as Partial<Warehouse> }))}>{t('wh.save')}</button></td>}
              </tr>); })}</tbody>
          </table>
        </div>
        <small style={{ padding: 6, display: 'block' }}>{t('wh.setup.stepsHelp')}</small>
      </section>

      <section className="mx-box">
        <h3>{t('wh.setup.locations')}</h3>
        <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
          <WhSelect value={w} onChange={setWsel} list={whs.filter((x) => !x.is_transit)} />
          {manage && <>
            <label className="mx-fld">{t('wh.setup.zone')}<input dir="ltr" style={{ width: 50 }} value={gen.zone} onChange={(e) => setGen({ ...gen, zone: e.currentTarget.value })} /></label>
            <label className="mx-fld">{t('wh.setup.aisles')}<input dir="ltr" inputMode="numeric" style={{ width: 50 }} value={gen.aisles} onChange={(e) => setGen({ ...gen, aisles: e.currentTarget.value })} /></label>
            <label className="mx-fld">{t('wh.setup.shelves')}<input dir="ltr" inputMode="numeric" style={{ width: 50 }} value={gen.shelves} onChange={(e) => setGen({ ...gen, shelves: e.currentTarget.value })} /></label>
            <label className="mx-fld">{t('wh.setup.binsPer')}<input dir="ltr" inputMode="numeric" style={{ width: 50 }} value={gen.bins} onChange={(e) => setGen({ ...gen, bins: e.currentTarget.value })} /></label>
            <button className="mx-btn main" onClick={() => base && void run(api<{ created: number }>(`${base}/wh/locations/generate`, { method: 'POST',
              body: { warehouse_id: w, zone: gen.zone, aisles: Number(gen.aisles), shelves: Number(gen.shelves), bins: Number(gen.bins) } }))}>{t('wh.setup.generate')}</button>
          </>}
          <span className="mx-sp" />
          <button className="mx-btn sm" onClick={() => setLabels(!labels)}>{t('wh.setup.labels')}</button>
        </div>
        {/* the answer of "generate" right under it (a 409 used to show only at the bottom of the screen, audit P7-F5) */}
        <Flash m={msg} />
        {manage && <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
          <label className="mx-fld">{t('wh.kind')}<select value={nl.kind} onChange={(e) => setNl({ ...nl, kind: e.currentTarget.value })}>
            {['zone', 'aisle', 'shelf', 'bin', 'input', 'qc', 'output', 'pack'].map((k) => <option key={k} value={k}>{t(`wh.lk.${k}`)}</option>)}</select></label>
          <label className="mx-fld">{t('wh.setup.parent')}<select value={nl.parent_id} onChange={(e) => setNl({ ...nl, parent_id: e.currentTarget.value })}>
            <option value="">—</option>{locs.filter((l) => ['zone', 'aisle', 'shelf'].includes(l.kind)).map((l) => <option key={l.id} value={l.id}>{l.code}</option>)}</select></label>
          <label className="mx-fld">{t('wh.code')}<input dir="ltr" style={{ width: 110 }} value={nl.code} onChange={(e) => setNl({ ...nl, code: e.currentTarget.value })} /></label>
          <label className="mx-fld">{t('wh.name')}<input value={nl.name} onChange={(e) => setNl({ ...nl, name: e.currentTarget.value })} /></label>
          <button className="mx-btn" disabled={!nl.code.trim()} onClick={() => base && void run(api(`${base}/wh/locations`, { method: 'POST', body: { warehouse_id: w, ...nl } }), () => setNl({ ...nl, code: '', name: '' }))}>{t('wh.add')}</button>
        </div>}
        {labels ? (
          <div className="mx-printhost-in" style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(180px, 1fr))', gap: 8, padding: 6 }}>
            {locs.filter((l) => l.kind === 'bin' || ['input', 'qc', 'output', 'pack'].includes(l.kind)).map((l) => (
              <div key={l.id} style={{ border: '1px solid #999', padding: 4, textAlign: 'center', background: '#fff', color: '#000', breakInside: 'avoid' }}>
                <b dir="ltr" style={{ fontSize: 18 }}>{l.code}</b><Barcode code={l.barcode || l.code} height={34} /></div>))}
            <button className="mx-btn" onClick={() => void printPage()}>{t('wh.print')}</button>
          </div>
        ) : (
          <div style={{ overflowX: 'auto', maxHeight: 360 }}>
            <table className="mx-table"><thead><tr><th>{t('wh.code')}</th><th>{t('wh.kind')}</th><th>{t('wh.name')}</th><th>{t('wh.barcode')}</th><th>{t('wh.rep.items')}</th><th>{t('wh.qty')}</th></tr></thead>
              <tbody>{locs.length === 0 && <tr><td colSpan={6} className="mx-empty">{t('wh.setup.noLocs')}</td></tr>}
                {locs.map((l) => <tr key={l.id} style={{ opacity: l.is_active ? 1 : 0.5 }}><td dir="ltr" style={{ paddingInlineStart: 6 + 12 * (l.sort_key.split('/').length - 1) }}>{l.code}</td>
                  <td>{t(`wh.lk.${l.kind}`)}</td><td>{l.name}</td><td dir="ltr">{l.barcode}</td><td>{l.items}</td><td dir="ltr">{qtyFmt(l.quantity)}</td></tr>)}</tbody></table>
          </div>)}
      </section>

      <section className="mx-box">
        <h3>{t('wh.setup.rules')}</h3>
        <table className="mx-table"><thead><tr><th>{t('wh.warehouse')}</th><th>{t('wh.setup.what')}</th><th>{t('wh.location')}</th><th>{t('wh.setup.priority')}</th>{manage && <th />}</tr></thead>
          <tbody>{rules.length === 0 && <tr><td colSpan={5} className="mx-empty">{t('wh.setup.noRules')}</td></tr>}
            {rules.map((r) => <tr key={r.id}><td>{r.warehouse}</td><td>{r.item_code ? `${r.item_code} · ${r.item_name}` : `${t('wh.setup.category')}: ${r.category_name}`}</td>
              <td dir="ltr">{r.location_code}</td><td>{r.priority}</td>
              {manage && <td><button className="mx-link" onClick={() => base && void run(api(`${base}/wh/putaway-rules/${r.id}`, { method: 'DELETE' }))}>✕</button></td>}</tr>)}</tbody></table>
        {manage && <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
          <label className="mx-fld">{t('wh.item')}<select value={nr.item_id} onChange={(e) => setNr({ ...nr, item_id: e.currentTarget.value, category_id: '' })}>
            <option value="">—</option>{items.filter((x) => x.kind === 'stock' && x.is_active).map((x) => <option key={x.id} value={x.id}>{x.code} · {x.name}</option>)}</select></label>
          <label className="mx-fld">{t('wh.setup.category')}<select value={nr.category_id} disabled={!!nr.item_id} onChange={(e) => setNr({ ...nr, category_id: e.currentTarget.value })}>
            <option value="">—</option>{cats.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}</select></label>
          <label className="mx-fld">{t('wh.location')}<select value={nr.location_id} onChange={(e) => setNr({ ...nr, location_id: e.currentTarget.value })}>
            <option value="">—</option>{locs.map((l) => <option key={l.id} value={l.id}>{l.code}</option>)}</select></label>
          <label className="mx-fld">{t('wh.setup.priority')}<input dir="ltr" inputMode="numeric" style={{ width: 50 }} value={nr.priority} onChange={(e) => setNr({ ...nr, priority: e.currentTarget.value })} /></label>
          <button className="mx-btn" disabled={(!nr.item_id && !nr.category_id) || !nr.location_id} onClick={() => base && void run(api(`${base}/wh/putaway-rules`, { method: 'POST',
            body: { warehouse_id: w, item_id: nr.item_id, category_id: nr.category_id, location_id: nr.location_id, priority: Number(nr.priority) || 10 } }))}>{t('wh.add')}</button>
        </div>}
      </section>
      <Flash m={msg} />
    </div>
  );
}
