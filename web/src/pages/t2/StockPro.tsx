import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, can, type Me } from '../../api';
import { useCompany } from '../../company';
import DataGrid, { type Col } from '../../components/DataGrid';
import { errorText, formatDate, today } from '../../util';
import { printPage } from '../../native';
import { Help, useBoxes } from '../acc/common';

interface Item { id: string; code: string; name: string; unit: string; kind: string; is_active: boolean }
interface Wh { id: string; name: string }
type Msg = { ok: boolean; text: string } | null;

function useRef<T>(path: string | null): [T[], () => void] {
  const [rows, setRows] = useState<T[]>([]);
  const load = useCallback(() => {
    if (path) api<T[]>(path).then(setRows).catch(() => setRows([]));
  }, [path]);
  useEffect(load, [load]);
  return [rows, load];
}

function Flash({ m }: { m: Msg }) {
  return m ? <div className={`mx-flash ${m.ok ? 'good' : 'bad'}`}>{m.text}</div> : null;
}

const MODES = ['items', 'lots', 'counts', 'replenish', 'prices', 'landed'] as const;
type Mode = typeof MODES[number];

/** Professional stock: item costing / tracking / units / kits / reorder, lots & serials, counts,
 *  replenishment, price lists and landed costs. */
export default function StockPro({ me, initial }: { me: Me; initial?: string }) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<Mode>((MODES as readonly string[]).includes(initial ?? '') ? initial as Mode : 'items');
  return (
    <div className="mx-col">
      <div className="mx-strip"><b>{t('t2.st.title')}</b><span className="mx-sp" /><Help topic="t2.help.stock" /></div>
      <div className="mx-modes" role="tablist">
        {MODES.map((m) => <button key={m} role="tab" className="mx-mode" aria-selected={mode === m} onClick={() => setMode(m)}>{t(`t2.st.m.${m}`)}</button>)}
      </div>
      {mode === 'items' && <ItemsMode me={me} />}
      {mode === 'lots' && <LotsMode />}
      {mode === 'counts' && <CountsMode me={me} />}
      {mode === 'replenish' && <ReplenishMode />}
      {mode === 'prices' && <PricesMode me={me} />}
      {mode === 'landed' && <LandedMode me={me} />}
    </div>
  );
}

// ---------- items: costing, tracking, units, kit, reorder ----------

interface Extras {
  item: { id: string; costing: string; tracking: string; kind: string; unit: string };
  units: { id: string; name: string; factor: string; barcode: string | null }[];
  components: { component_id: string; name: string; quantity: string; unit: string }[];
  reorder_rules: { warehouse_id: string; warehouse_name: string; min_qty: string; max_qty: string }[];
}

function ItemsMode({ me }: { me: Me }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const base = company ? `/companies/${company.id}` : null;
  const [items] = useRef<Item>(base && `${base}/items`);
  const [whs] = useRef<Wh>(base && `${base}/warehouses`);
  const [sel, setSel] = useState<Item | null>(null);
  const [x, setX] = useState<Extras | null>(null);
  const [units, setUnits] = useState('');
  const [comps, setComps] = useState<{ component_id: string; quantity: string }[]>([]);
  const [rule, setRule] = useState({ warehouse_id: '', min_qty: '', max_qty: '' });
  const [msg, setMsg] = useState<Msg>(null);
  const canEdit = can(me, 'stock.manage');
  const load = useCallback((id: string) => {
    if (!base) return;
    api<Extras>(`${base}/items/${id}/extras`).then((e) => {
      setX(e);
      setUnits(e.units.map((u) => `${u.name}=${u.factor}`).join(', '));
      setComps(e.components.map((c) => ({ component_id: c.component_id, quantity: c.quantity })));
    }).catch((e) => setMsg({ ok: false, text: errorText(t, e) }));
  }, [base, t]);
  async function run(p: Promise<unknown>) {
    try { await p; setMsg({ ok: true, text: t('t2.saved') }); if (sel) load(sel.id); } catch (e) { setMsg({ ok: false, text: errorText(t, e) }); }
  }
  const cols: Col<Item>[] = [
    { key: 'code', title: t('pur.code'), get: (r) => r.code, width: 90 },
    { key: 'name', title: t('pur.item'), get: (r) => r.name, width: 240 },
    { key: 'unit', title: t('t2.st.unit'), get: (r) => r.unit, width: 70 },
    { key: 'kind', title: t('t2.st.kind'), get: (r) => t(`t2.st.kinds.${r.kind}`, { defaultValue: r.kind }), width: 110 },
  ];
  return (
    <div className="mx-body" style={{ gridTemplateColumns: 'minmax(0, 1fr) minmax(300px, 420px)' }}>
      <section className="mx-box"><h3>{t('mx.t.items')}</h3>
        <DataGrid rows={items} cols={cols} rowKey={(r) => r.id} selected={sel?.id ?? null}
          onSelect={(r) => { setSel(r); setMsg(null); load(r.id); }} onActivate={(r) => { setSel(r); load(r.id); }} />
      </section>
      <section className="mx-box"><h3>{sel ? `${sel.code} · ${sel.name}` : t('t2.st.pickItem')}</h3>
        {sel && x && base && (
          <div className="mx-pay">
            <label>{t('t2.st.costing')} <select disabled={!canEdit} value={x.item.costing}
              onChange={(e) => void run(api(`${base}/items/${sel.id}/stock-settings`, { method: 'PUT', body: { costing: e.currentTarget.value } }))}>
              <option value="avg">{t('t2.st.avg')}</option><option value="fifo">FIFO</option></select></label>
            <label>{t('t2.st.tracking')} <select disabled={!canEdit} value={x.item.tracking}
              onChange={(e) => void run(api(`${base}/items/${sel.id}/stock-settings`, { method: 'PUT', body: { tracking: e.currentTarget.value } }))}>
              <option value="none">{t('t2.st.none')}</option><option value="lot">{t('t2.st.lot')}</option><option value="serial">{t('t2.st.serial')}</option></select></label>
            <label htmlFor="sp-units"><small>{t('t2.st.units', { unit: sel.unit })}</small></label>
            <input id="sp-units" dir="ltr" disabled={!canEdit} placeholder="Barrel=200, Box=12" value={units} onChange={(e) => setUnits(e.currentTarget.value)} />
            <button className="mx-btn" disabled={!canEdit} onClick={() => void run(api(`${base}/items/${sel.id}/units`, { method: 'PUT', body: {
              units: units.split(',').map((s) => s.trim()).filter(Boolean).map((s) => { const [name, factor] = s.split('='); return { name: name.trim(), factor: (factor ?? '').trim() }; }) } }))}>
              {t('t2.st.saveUnits')}</button>
            <small>{t('t2.st.kitHint')}</small>
            {comps.map((c, i) => (
              <div className="mx-row" key={i}>
                <select value={c.component_id} disabled={!canEdit} onChange={(e) => setComps(comps.map((y, j) => (j === i ? { ...y, component_id: e.currentTarget.value } : y)))}>
                  <option value="">—</option>
                  {items.filter((it) => it.kind === 'stock' && it.id !== sel.id).map((it) => <option key={it.id} value={it.id}>{it.code} · {it.name}</option>)}
                </select>
                <input dir="ltr" style={{ width: 80 }} value={c.quantity} disabled={!canEdit}
                  onChange={(e) => setComps(comps.map((y, j) => (j === i ? { ...y, quantity: e.currentTarget.value } : y)))} />
              </div>
            ))}
            <div className="mx-row">
              <button className="mx-btn sm" disabled={!canEdit} onClick={() => setComps([...comps, { component_id: '', quantity: '1' }])}>+ {t('t2.st.component')}</button>
              <button className="mx-btn" disabled={!canEdit} onClick={() => void run(api(`${base}/items/${sel.id}/components`, { method: 'PUT',
                body: { components: comps.filter((c) => c.component_id) } }))}>{t('t2.st.saveKit')}</button>
            </div>
            <small>{t('t2.st.reorder')}</small>
            {x.reorder_rules.map((r) => <div key={r.warehouse_id}>{r.warehouse_name}: {r.min_qty} → {r.max_qty}</div>)}
            <div className="mx-row">
              <select value={rule.warehouse_id} onChange={(e) => setRule({ ...rule, warehouse_id: e.currentTarget.value })}>
                <option value="">{t('pur.warehouse')}</option>{whs.map((w) => <option key={w.id} value={w.id}>{w.name}</option>)}
              </select>
              <input dir="ltr" style={{ width: 70 }} placeholder="min" value={rule.min_qty} onChange={(e) => setRule({ ...rule, min_qty: e.currentTarget.value })} />
              <input dir="ltr" style={{ width: 70 }} placeholder="max" value={rule.max_qty} onChange={(e) => setRule({ ...rule, max_qty: e.currentTarget.value })} />
              <button className="mx-btn sm" disabled={!canEdit || !rule.warehouse_id} onClick={() => void run(api(`${base}/stock/reorder-rules`, { method: 'PUT',
                body: { item_id: sel.id, ...rule } }))}>{t('common.save')}</button>
            </div>
            <Flash m={msg} />
          </div>
        )}
      </section>
    </div>
  );
}

// ---------- lots & serials ----------

interface Lot { id: string; lot_no: string; expiry: string | null; item_name: string; item_code: string; tracking: string; quantity: string; expired: boolean;
  locations: { warehouse: string; quantity: string }[] }
interface Trace { lot: { lot_no: string; item_name: string }; moves: { move_date: string; source: string; warehouse: string; quantity: string; notes: string }[] }

function LotsMode() {
  const { t } = useTranslation();
  const { company } = useCompany();
  const [lots] = useRef<Lot>(company ? `/companies/${company.id}/stock/lots` : null);
  const [trace, setTrace] = useState<Trace | null>(null);
  const cols: Col<Lot>[] = [
    { key: 'lot', title: t('t2.st.lotNo'), get: (r) => r.lot_no, width: 140 },
    { key: 'item', title: t('pur.item'), get: (r) => `${r.item_code} · ${r.item_name}`, width: 220 },
    { key: 'exp', title: t('t2.st.expiry'), get: (r) => r.expiry ?? '', render: (r) => (r.expiry ? formatDate(r.expiry) : '—'), width: 110, cls: (r) => (r.expired ? 'red' : '') },
    { key: 'qty', title: t('pur.qty'), get: (r) => r.quantity, num: true, decimals: 2, width: 90, total: 'sum' },
    { key: 'where', title: t('pur.warehouse'), get: (r) => r.locations.map((l) => `${l.warehouse}: ${Number(l.quantity)}`).join(' · '), width: 220 },
  ];
  return (
    <div className="mx-body" style={{ gridTemplateColumns: 'minmax(0, 1fr) minmax(280px, 420px)' }}>
      <section className="mx-box"><h3>{t('t2.st.m.lots')} <small>{t('ax.dblClickLedger')}</small></h3>
        <DataGrid rows={lots} cols={cols} rowKey={(r) => r.id} footer title={t('t2.st.m.lots')}
          onActivate={(r) => company && api<Trace>(`/companies/${company.id}/stock/lots/${r.id}/trace`).then(setTrace).catch(() => setTrace(null))} />
      </section>
      <section className="mx-box"><h3>{trace ? `${trace.lot.lot_no} · ${trace.lot.item_name}` : t('t2.st.trace')}</h3>
        {trace && <table><thead><tr><th>{t('invoices.date')}</th><th>{t('t2.st.doc')}</th><th>{t('pur.warehouse')}</th><th>{t('pur.qty')}</th></tr></thead>
          <tbody>{trace.moves.map((m, i) => <tr key={i}><td>{formatDate(m.move_date)}</td><td>{t(`t2.st.src.${m.source}`, { defaultValue: m.source })} {m.notes}</td>
            <td>{m.warehouse}</td><td className={Number(m.quantity) < 0 ? 'red' : 'ok'} dir="ltr">{Number(m.quantity)}</td></tr>)}</tbody></table>}
      </section>
    </div>
  );
}

// ---------- counts ----------

interface CountRow { id: string; number: number; warehouse_name: string; count_date: string; status: string; differences: number }
interface CountDoc { count: CountRow & { warehouse_id: string }; lines: { item_id: string; code: string; name: string; unit: string; expected: string; counted: string | null; difference: string | null }[] }

function CountsMode({ me }: { me: Me }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const base = company ? `/companies/${company.id}` : null;
  const [list, reload] = useRef<CountRow>(base && `${base}/stock/counts`);
  const [whs] = useRef<Wh>(base && `${base}/warehouses`);
  const [doc, setDoc] = useState<CountDoc | null>(null);
  const [counted, setCounted] = useState<Record<string, string>>({});
  const [wh, setWh] = useState('');
  const [msg, setMsg] = useState<Msg>(null);
  const open = (id: string) => base && api<CountDoc>(`${base}/stock/counts/${id}`).then((d) => {
    setDoc(d); setCounted(Object.fromEntries(d.lines.map((l) => [l.item_id, l.counted ?? ''])));
  });
  async function run(p: Promise<CountDoc>) {
    try { const d = await p; setDoc(d); setMsg({ ok: true, text: t('t2.saved') }); reload(); } catch (e) { setMsg({ ok: false, text: errorText(t, e) }); }
  }
  const draft = doc?.count.status === 'draft';
  return (
    <div className="mx-body" style={{ gridTemplateColumns: 'minmax(260px, 360px) minmax(0, 1fr)' }}>
      <section className="mx-box"><h3>{t('t2.st.m.counts')}</h3>
        {can(me, 'stock.create') && base && (
          <div className="mx-row" style={{ padding: 8 }}>
            <select value={wh} onChange={(e) => setWh(e.currentTarget.value)}><option value="">{t('pur.warehouse')}</option>
              {whs.map((w) => <option key={w.id} value={w.id}>{w.name}</option>)}</select>
            <button className="mx-btn sm" onClick={() => void run(api<CountDoc>(`${base}/stock/counts`, { method: 'POST', body: { warehouse_id: wh, date: today() } }))}>
              {t('t2.st.newCount')}</button>
          </div>
        )}
        <DataGrid rows={list} rowKey={(r) => r.id} onActivate={(r) => void open(r.id)} onSelect={(r) => void open(r.id)} cols={[
          { key: 'n', title: '#', get: (r) => r.number, width: 50 },
          { key: 'w', title: t('pur.warehouse'), get: (r) => r.warehouse_name, width: 120 },
          { key: 'd', title: t('invoices.date'), get: (r) => r.count_date, width: 100 },
          { key: 's', title: t('t2.st.status'), get: (r) => t(`t2.st.cs.${r.status}`), width: 80 },
        ]} />
      </section>
      <section className="mx-box"><h3>{doc ? `${t('t2.st.count')} #${doc.count.number} · ${doc.count.warehouse_name} · ${formatDate(doc.count.count_date)}` : t('t2.st.pickCount')}</h3>
        {doc && base && (
          <div className="mx-pay">
            <div className="print-doc">
              <table><thead><tr><th>{t('pur.code')}</th><th>{t('pur.item')}</th><th>{t('t2.st.expected')}</th><th>{t('t2.st.counted')}</th><th>{t('t2.st.diff')}</th></tr></thead>
                <tbody>{doc.lines.map((l) => {
                  const c = counted[l.item_id] ?? '';
                  const diff = c === '' ? '' : String(Number(c) - Number(l.expected));
                  return (
                    <tr key={l.item_id}><td>{l.code}</td><td>{l.name} <small>{l.unit}</small></td><td dir="ltr">{Number(l.expected)}</td>
                      <td>{draft ? <input dir="ltr" inputMode="decimal" style={{ width: 90 }} value={c} aria-label={l.name}
                        onChange={(e) => setCounted({ ...counted, [l.item_id]: e.currentTarget.value.replace(/[^\d.]/g, '') })} /> : (l.counted ?? '')}</td>
                      <td dir="ltr" className={Number(diff) < 0 ? 'red' : Number(diff) > 0 ? 'ok' : ''}>{diff}</td></tr>
                  );
                })}</tbody></table>
            </div>
            <div className="mx-row">
              <button className="mx-btn" onClick={() => void printPage(t('t2.st.count'))}>{t('invoices.print')}</button>
              {draft && <button className="mx-btn" onClick={() => void run(api<CountDoc>(`${base}/stock/counts/${doc.count.id}/lines`, { method: 'PUT',
                body: { lines: Object.entries(counted).map(([item_id, v]) => ({ item_id, counted: v })) } }))}>{t('common.save')}</button>}
              {draft && can(me, 'stock.manage') && <button className="mx-btn main" onClick={() => void run(api<CountDoc>(`${base}/stock/counts/${doc.count.id}/post`, { method: 'POST', body: {} }))}>
                {t('t2.st.postCount')}</button>}
            </div>
            <Flash m={msg} />
          </div>
        )}
      </section>
    </div>
  );
}

// ---------- replenishment ----------

interface Rep { item_id: string; code: string; name: string; unit: string; warehouse_name: string; quantity: string; min_qty: string | null; max_qty: string | null;
  to_order: string | null; daily_use: string; days_left: number | null; supplier_name: string | null }

function ReplenishMode() {
  const { t } = useTranslation();
  const { company } = useCompany();
  const [rows, setRows] = useState<Rep[]>([]);
  useEffect(() => {
    if (company) api<{ rows: Rep[] }>(`/companies/${company.id}/reports/stock-replenishment`).then((r) => setRows(r.rows)).catch(() => setRows([]));
  }, [company]);
  const cols: Col<Rep>[] = [
    { key: 'item', title: t('pur.item'), get: (r) => `${r.code} · ${r.name}`, width: 220 },
    { key: 'wh', title: t('pur.warehouse'), get: (r) => r.warehouse_name, width: 120 },
    { key: 'qty', title: t('t2.st.inStock'), get: (r) => r.quantity, num: true, decimals: 2, width: 100 },
    { key: 'min', title: 'Min', get: (r) => r.min_qty ?? '', num: true, decimals: 2, width: 80 },
    { key: 'order', title: t('t2.st.toOrder'), get: (r) => r.to_order ?? '', num: true, decimals: 2, width: 100, cls: (r) => (r.to_order ? 'red' : '') },
    { key: 'use', title: t('t2.st.dailyUse'), get: (r) => r.daily_use, num: true, decimals: 2, width: 100 },
    { key: 'days', title: t('t2.st.daysLeft'), get: (r) => r.days_left ?? '', num: true, decimals: 0, width: 90, cls: (r) => (r.days_left !== null && r.days_left < 7 ? 'red' : '') },
    { key: 'sup', title: t('mx.t.suppliers'), get: (r) => r.supplier_name ?? '', width: 140 },
  ];
  return (
    <div className="mx-body" style={{ gridTemplateColumns: '1fr' }}>
      <section className="mx-box"><h3>{t('t2.st.m.replenish')} <small>{t('t2.st.repHint')}</small></h3>
        <DataGrid rows={rows} cols={cols} rowKey={(r) => r.item_id + r.warehouse_name} title={t('t2.st.m.replenish')} />
      </section>
    </div>
  );
}

// ---------- price lists ----------

interface PL { id: string; name: string; currency: string; is_active: boolean; items: number; customers: number }
interface PLI { item_id: string; code: string; name: string; min_qty: string; price: string }

function PricesMode({ me }: { me: Me }) {
  const { t } = useTranslation();
  const { company, currencies } = useCompany();
  const base = company ? `/companies/${company.id}` : null;
  const [lists, reload] = useRef<PL>(base && `${base}/price-lists`);
  const [items] = useRef<Item>(base && `${base}/items`);
  const [sel, setSel] = useState<PL | null>(null);
  const [rows, setRows] = useState<PLI[]>([]);
  const [nl, setNl] = useState({ name: '', currency: company?.base_currency ?? 'USD' });
  const [msg, setMsg] = useState<Msg>(null);
  const canEdit = can(me, 'stock.manage');
  const open = (p: PL) => { setSel(p); base && api<PLI[]>(`${base}/price-lists/${p.id}/items`).then(setRows).catch(() => setRows([])); };
  async function save() {
    if (!base || !sel) return;
    try {
      setRows(await api<PLI[]>(`${base}/price-lists/${sel.id}/items`, { method: 'PUT', body: { items: rows.filter((r) => r.item_id && r.price !== '') } }));
      setMsg({ ok: true, text: t('t2.saved') }); reload();
    } catch (e) { setMsg({ ok: false, text: errorText(t, e) }); }
  }
  return (
    <div className="mx-body" style={{ gridTemplateColumns: 'minmax(240px, 340px) minmax(0, 1fr)' }}>
      <section className="mx-box"><h3>{t('t2.st.m.prices')}</h3>
        {canEdit && base && (
          <div className="mx-row" style={{ padding: 8 }}>
            <input placeholder={t('t2.st.listName')} value={nl.name} onChange={(e) => setNl({ ...nl, name: e.currentTarget.value })} />
            <select value={nl.currency} onChange={(e) => setNl({ ...nl, currency: e.currentTarget.value })}>{currencies.map((c) => <option key={c.code}>{c.code}</option>)}</select>
            <button className="mx-btn sm" disabled={!nl.name.trim()} onClick={() => void api(`${base}/price-lists`, { method: 'POST', body: nl })
              .then(() => { setNl({ ...nl, name: '' }); reload(); }).catch((e) => setMsg({ ok: false, text: errorText(t, e) }))}>+</button>
          </div>
        )}
        <DataGrid rows={lists} rowKey={(r) => r.id} selected={sel?.id ?? null} onSelect={open} onActivate={open} cols={[
          { key: 'n', title: t('t2.st.listName'), get: (r) => r.name, width: 140 },
          { key: 'c', title: t('pur.currency'), get: (r) => r.currency, width: 60 },
          { key: 'cu', title: t('mx.t.subscribers'), get: (r) => r.customers, num: true, decimals: 0, width: 80 },
        ]} />
      </section>
      <section className="mx-box"><h3>{sel ? `${sel.name} · ${sel.currency}` : t('t2.st.pickList')} <small>{t('t2.st.plHint')}</small></h3>
        {sel && (
          <div className="mx-pay">
            <table><thead><tr><th>{t('pur.item')}</th><th>{t('t2.st.fromQty')}</th><th>{t('pur.unitPrice')}</th></tr></thead>
              <tbody>{rows.map((r, i) => (
                <tr key={i}>
                  <td><select value={r.item_id} disabled={!canEdit} onChange={(e) => setRows(rows.map((y, j) => (j === i ? { ...y, item_id: e.currentTarget.value } : y)))}>
                    <option value="">—</option>{items.map((it) => <option key={it.id} value={it.id}>{it.code} · {it.name}</option>)}</select></td>
                  <td><input dir="ltr" style={{ width: 80 }} value={r.min_qty} disabled={!canEdit} onChange={(e) => setRows(rows.map((y, j) => (j === i ? { ...y, min_qty: e.currentTarget.value } : y)))} /></td>
                  <td><input dir="ltr" style={{ width: 100 }} value={r.price} disabled={!canEdit} onChange={(e) => setRows(rows.map((y, j) => (j === i ? { ...y, price: e.currentTarget.value } : y)))} /></td>
                </tr>))}</tbody></table>
            <div className="mx-row">
              <button className="mx-btn sm" disabled={!canEdit} onClick={() => setRows([...rows, { item_id: '', code: '', name: '', min_qty: '0', price: '' }])}>+</button>
              <button className="mx-btn main" disabled={!canEdit} onClick={() => void save()}>{t('common.save')}</button>
            </div>
            <Flash m={msg} />
          </div>
        )}
      </section>
    </div>
  );
}

// ---------- landed costs ----------

interface BillRow { id: string; number: number; supplier_name: string; bill_date: string; currency: string; total: string; status: string }

function LandedMode({ me }: { me: Me }) {
  const { t } = useTranslation();
  const { company, currencies } = useCompany();
  const base = company ? `/companies/${company.id}` : null;
  const [bills, setBills] = useState<BillRow[]>([]);
  const boxes = useBoxes();
  const [sel, setSel] = useState<BillRow | null>(null);
  const [f, setF] = useState({ amount: '', currency: company?.base_currency ?? 'USD', method: 'value', method_id: '', description: '', date: today() });
  const [msg, setMsg] = useState<Msg>(null);
  useEffect(() => {
    if (base) api<{ rows: BillRow[] }>(`${base}/bills`).then((r) => setBills(r.rows.filter((b) => b.status !== 'void'))).catch(() => setBills([]));
  }, [base]);
  async function save() {
    if (!base || !sel) return;
    try {
      await api(`${base}/bills/${sel.id}/landed-costs`, { method: 'POST', body: f });
      setMsg({ ok: true, text: t('t2.saved') }); setF({ ...f, amount: '', description: '' });
    } catch (e) { setMsg({ ok: false, text: errorText(t, e) }); }
  }
  return (
    <div className="mx-body" style={{ gridTemplateColumns: 'minmax(0, 1fr) minmax(280px, 380px)' }}>
      <section className="mx-box"><h3>{t('mx.t.bills')}</h3>
        <DataGrid rows={bills} rowKey={(r) => r.id} selected={sel?.id ?? null} onSelect={setSel} onActivate={setSel} cols={[
          { key: 'n', title: '#', get: (r) => r.number, width: 60 },
          { key: 's', title: t('pur.supplier'), get: (r) => r.supplier_name, width: 180 },
          { key: 'd', title: t('invoices.date'), get: (r) => r.bill_date, width: 100 },
          { key: 't', title: t('invoices.total'), get: (r) => r.total, num: true, decimals: 2, width: 110 },
          { key: 'c', title: '', get: (r) => r.currency, width: 50 },
        ]} />
      </section>
      <section className="mx-box"><h3>{sel ? `${t('t2.st.landed')} · #${sel.number}` : t('t2.st.pickBill')}</h3>
        {sel && (
          <div className="mx-pay">
            <small>{t('t2.st.landedHint')}</small>
            <div className="mx-row">
              <input dir="ltr" inputMode="decimal" placeholder={t('pur.amount')} value={f.amount} onChange={(e) => setF({ ...f, amount: e.currentTarget.value.replace(/[^\d.]/g, '') })} />
              <select value={f.currency} onChange={(e) => setF({ ...f, currency: e.currentTarget.value, method_id: '' })}>{currencies.map((c) => <option key={c.code}>{c.code}</option>)}</select>
            </div>
            <select value={f.method_id} onChange={(e) => setF({ ...f, method_id: e.currentTarget.value })}>
              <option value="">{t('t2.st.paidFrom')}</option>
              {boxes.filter((b) => b.currency === f.currency).map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
            </select>
            <select value={f.method} onChange={(e) => setF({ ...f, method: e.currentTarget.value })}>
              <option value="value">{t('t2.st.byValue')}</option><option value="quantity">{t('t2.st.byQty')}</option></select>
            <input placeholder={t('invoices.description')} value={f.description} onChange={(e) => setF({ ...f, description: e.currentTarget.value })} />
            <input type="date" value={f.date} onChange={(e) => setF({ ...f, date: e.currentTarget.value })} />
            <button className="mx-btn main" disabled={!can(me, 'purchasing.create') || !f.amount || !f.method_id} onClick={() => void save()}>{t('common.save')}</button>
            <Flash m={msg} />
          </div>
        )}
      </section>
    </div>
  );
}
