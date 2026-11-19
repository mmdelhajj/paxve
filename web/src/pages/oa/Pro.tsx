import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { api, can, type Account, type Me } from '../../api';
import { useCompany } from '../../company';
import DataGrid, { type Col } from '../../components/DataGrid';
import Attachments from '../../components/Attachments';
import { errorText, formatAmount, formatDate, today } from '../../util';
import { printPage } from '../../native';
import { Help } from '../acc/common';

// Odoo audit stage screens: purchase orders, payment reminders (dunning) and payment terms,
// month-end exchange revaluation, deferred revenue / prepaid expenses.

type Msg = { ok: boolean; text: string } | null;
function Flash({ m }: { m: Msg }) {
  return m ? <div className={`mx-flash ${m.ok ? 'good' : 'bad'}`}>{m.text}</div> : null;
}
function useList<T>(path: string | null): [T[], () => void] {
  const [rows, setRows] = useState<T[]>([]);
  const load = useCallback(() => {
    if (path) api<T[]>(path).then(setRows).catch(() => setRows([]));
  }, [path]);
  useEffect(load, [load]);
  return [rows, load];
}
function useBase() {
  const { company } = useCompany();
  return company ? `/companies/${company.id}` : null;
}
function useAccounts(): Account[] {
  const base = useBase();
  const [rows] = useList<Account>(base && `${base}/accounts`);
  return useMemo(() => rows.filter((a) => a.is_postable && a.is_active), [rows]);
}
const thisMonth = () => today().slice(0, 7);
const lastMonth = () => { const d = new Date(); d.setDate(1); d.setMonth(d.getMonth() - 1); return d.toISOString().slice(0, 7); };

function AccountSelect({ value, onChange, types, label }: { value: string; onChange: (v: string) => void; types?: string[]; label: string }) {
  const { t } = useTranslation();
  const accs = useAccounts().filter((a) => !types || types.includes(a.type));
  return (
    <label>{label}<select value={value} onChange={(e) => onChange(e.currentTarget.value)}>
      <option value="">{t('oa.choose')}</option>
      {accs.map((a) => <option key={a.id} value={a.id}>{a.code} · {a.name}</option>)}
    </select></label>
  );
}

// ======================= purchase orders =======================

interface PO {
  id: string; number: number; supplier_id: string; supplier_name: string; supplier_phone: string | null; order_date: string; expected_date: string | null;
  currency: string; status: string; supplier_ref: string; notes: string; total: string; bills: number; billed_total: string; late: boolean;
}
interface POLine {
  id: string; line_no: number; item_id: string | null; item_code: string | null; unit: string | null; description: string; quantity: string;
  unit_price: string; billed_qty: string; open_qty: string; received_qty: string; to_receive: string; unit_name: string | null; tax_name: string | null; warehouse_id: string | null; tax_rate_id: string | null; unit_id: string | null;
}
interface PODetail { order: PO; lines: POLine[]; bills: { id: string; number: number; bill_date: string; total: string; status: string }[]; receipts?: { id: string; number: number; receipt_date: string; delivery_ref: string; status: string; quantity: string }[] }
interface Sup { id: string; code: string; name: string; currency: string | null; status: string }
interface Itm { id: string; code: string; name: string; unit: string; kind: string; is_active: boolean }
interface EditLine { item_id: string; description: string; quantity: string; unit_price: string; tax_rate_id: string }

const PO_ST = ['rfq', 'sent', 'confirmed', 'partial', 'billed', 'cancelled'];

export default function PurchaseOrders({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const { company, currencies, decimalsOf } = useCompany();
  const [filter, setFilter] = useState('open');
  const [rows, reload] = useList<PO>(base && `${base}/purchase-orders?status=${filter}`);
  const [sups] = useList<Sup>(base && `${base}/suppliers`);
  const [items] = useList<Itm>(base && `${base}/items`);
  const [taxes] = useList<{ id: string; name: string; applies_to: string; is_active: boolean }>(base && `${base}/tax-rates`);
  const [sel, setSel] = useState<PODetail | null>(null);
  const [edit, setEdit] = useState<{ id?: string; supplier_id: string; currency: string; order_date: string; expected_date: string; notes: string; lines: EditLine[] } | null>(null);
  const [billQty, setBillQty] = useState<Record<string, string>>({});
  const [billRef, setBillRef] = useState('');
  const [recvQty, setRecvQty] = useState<Record<string, string>>({});
  const [warn, setWarn] = useState<string[]>([]);
  const [msg, setMsg] = useState<Msg>(null);
  const [text, setText] = useState('');
  const canCreate = can(me, 'purchasing.create');

  // keepText: the RFQ message just prepared by "Send" must stay on screen after the order reloads
  const open = useCallback((id: string, keepText = '') => {
    if (!base) return;
    api<PODetail>(`${base}/purchase-orders/${id}`).then((d) => { setSel(d); setEdit(null); setText(keepText); setBillQty(Object.fromEntries(d.lines.map((l) => [l.id, l.open_qty]))); setRecvQty(Object.fromEntries(d.lines.map((l) => [l.id, l.to_receive]))); setBillRef(d.order.supplier_ref); })
      .catch((e) => setMsg({ ok: false, text: errorText(t, e) }));
  }, [base, t]);

  async function run<T>(p: Promise<T>, after?: (x: T) => void) {
    try { const x = await p; setMsg({ ok: true, text: t('oa.saved') }); after?.(x); reload(); } catch (e) { setMsg({ ok: false, text: errorText(t, e) }); }
  }
  const blankLine = (): EditLine => ({ item_id: '', description: '', quantity: '1', unit_price: '', tax_rate_id: '' });
  function startNew() {
    setSel(null); setMsg(null);
    setEdit({ supplier_id: '', currency: company?.base_currency ?? 'USD', order_date: today(), expected_date: '', notes: '', lines: [blankLine()] });
  }
  function save() {
    if (!base || !edit) return;
    const body = { ...edit, lines: edit.lines.filter((l) => (l.item_id || l.description.trim()) && Number(l.quantity) > 0) };
    const p = edit.id ? api<PODetail>(`${base}/purchase-orders/${edit.id}`, { method: 'PUT', body }) : api<PODetail>(`${base}/purchase-orders`, { method: 'POST', body });
    void run(p, (d) => open(d.order.id));
  }
  function action(a: string) {
    if (!base || !sel) return;
    void run(api<PODetail | { po: PODetail; message: string }>(`${base}/purchase-orders/${sel.order.id}/${a}`, { method: 'POST', body: {} }), (x) => {
      open(sel.order.id, 'message' in x ? x.message : '');
    });
  }
  function bill() {
    if (!base || !sel) return;
    const lines = sel.lines.filter((l) => Number(billQty[l.id] || 0) > 0).map((l) => ({ po_line_id: l.id, quantity: billQty[l.id] }));
    void run(api<{ warnings?: { code: string; line: string }[] }>(`${base}/purchase-orders/${sel.order.id}/bill`, { method: 'POST', body: { bill_date: today(), supplier_ref: billRef, lines } }), (x) => {
      setWarn((x.warnings ?? []).map((w) => t(`fin.match.${w.code}`, { line: w.line })));
      open(sel.order.id);
    });
  }
  function receive() {
    if (!base || !sel) return;
    const lines = sel.lines.filter((l) => Number(recvQty[l.id] || 0) > 0).map((l) => ({ po_line_id: l.id, quantity: recvQty[l.id] }));
    setWarn([]);
    void run(api(`${base}/purchase-orders/${sel.order.id}/receive`, { method: 'POST', body: { date: today(), delivery_ref: billRef, lines } }), () => open(sel.order.id));
  }
  function fromReorder() {
    if (!base) return;
    void run(api<{ created: { id: string; number: number }[] }>(`${base}/purchase-orders/from-reorder`, { method: 'POST', body: {} }), (x) => {
      setMsg({ ok: true, text: t('oa.po.fromReorderDone', { n: x.created.length }) });
      if (x.created[0]) open(x.created[0].id);
    });
  }

  const cols: Col<PO>[] = [
    { key: 'n', title: '#', get: (r) => r.number, num: true, decimals: 0, width: 60 },
    { key: 'd', title: t('oa.po.date'), get: (r) => r.order_date, render: (r) => formatDate(r.order_date), width: 90 },
    { key: 's', title: t('oa.po.supplier'), get: (r) => r.supplier_name, width: 180 },
    { key: 'st', title: t('oa.po.status'), get: (r) => t(`oa.po.st.${r.status}`), width: 100, cls: (r) => (r.late ? 'red' : '') },
    { key: 'e', title: t('oa.po.expected'), get: (r) => r.expected_date ?? '', render: (r) => (r.expected_date ? formatDate(r.expected_date) : ''), width: 90 },
    { key: 't', title: t('oa.po.total'), get: (r) => Number(r.total), render: (r) => `${formatAmount(r.total, decimalsOf(r.currency))} ${r.currency}`, num: true, width: 120 },
    { key: 'b', title: t('oa.po.billed'), get: (r) => Number(r.billed_total), render: (r) => formatAmount(r.billed_total, decimalsOf(r.currency)), num: true, width: 100 },
  ];
  const o = sel?.order;
  const dec = o ? decimalsOf(o.currency) : 2;
  return (
    <div className="mx-col">
      <div className="mx-strip"><b>{t('oa.po.title')}</b><span className="mx-sp" /><Help topic="oa.help.po" /></div>
      <div className="mx-body" style={{ gridTemplateColumns: 'minmax(0, 1fr) minmax(320px, 560px)' }}>
        <section className="mx-box"><h3>{t('oa.po.list')}</h3>
          <div className="mx-row" style={{ padding: 6, gap: 6, flexWrap: 'wrap' }}>
            <select value={filter} onChange={(e) => setFilter(e.currentTarget.value)} aria-label={t('oa.po.status')}>
              <option value="open">{t('oa.po.f.open')}</option><option value="all">{t('oa.po.f.all')}</option>
              {PO_ST.map((s) => <option key={s} value={s}>{t(`oa.po.st.${s}`)}</option>)}
            </select>
            {canCreate && <button className="mx-btn" onClick={startNew}>{t('oa.po.new')}</button>}
            {canCreate && <button className="mx-btn" onClick={fromReorder} title={t('oa.po.fromReorderHint')}>{t('oa.po.fromReorder')}</button>}
          </div>
          <DataGrid rows={rows} cols={cols} rowKey={(r) => r.id} selected={o?.id ?? null} title={t('oa.po.title')} onSelect={(r) => { setMsg(null); open(r.id); }} onActivate={(r) => open(r.id)} />
        </section>
        <section className="mx-box">
          {edit ? (
            <div className="mx-pay" style={{ overflow: 'auto' }}>
              <h3 style={{ margin: 0 }}>{edit.id ? t('oa.po.edit') : t('oa.po.new')}</h3>
              <label>{t('oa.po.supplier')}<select value={edit.supplier_id} onChange={(e) => { const s = sups.find((x) => x.id === e.currentTarget.value); setEdit({ ...edit, supplier_id: e.currentTarget.value, currency: s?.currency || edit.currency }); }}>
                <option value="">{t('oa.choose')}</option>
                {sups.filter((s) => s.status === 'active').map((s) => <option key={s.id} value={s.id}>{s.code} · {s.name}</option>)}</select></label>
              <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
                <label>{t('oa.po.date')}<input type="date" dir="ltr" value={edit.order_date} onChange={(e) => setEdit({ ...edit, order_date: e.currentTarget.value })} /></label>
                <label>{t('oa.po.expected')}<input type="date" dir="ltr" value={edit.expected_date} onChange={(e) => setEdit({ ...edit, expected_date: e.currentTarget.value })} /></label>
                <label>{t('oa.currency')}<select value={edit.currency} onChange={(e) => setEdit({ ...edit, currency: e.currentTarget.value })}>
                  {currencies.map((c) => <option key={c.code} value={c.code}>{c.code}</option>)}</select></label>
              </div>
              <table className="mx-table">
                <thead><tr><th>{t('oa.po.item')}</th><th>{t('oa.po.qty')}</th><th>{t('oa.po.price')}</th><th>{t('oa.po.tax')}</th><th /></tr></thead>
                <tbody>{edit.lines.map((l, i) => {
                  const up = (p: Partial<EditLine>) => setEdit({ ...edit, lines: edit.lines.map((x, j) => (j === i ? { ...x, ...p } : x)) });
                  return (
                    <tr key={i}>
                      <td><select value={l.item_id} onChange={(e) => up({ item_id: e.currentTarget.value })} style={{ maxWidth: 180 }}>
                        <option value="">{t('oa.po.freeText')}</option>
                        {items.filter((x) => x.is_active).map((x) => <option key={x.id} value={x.id}>{x.code} · {x.name}</option>)}</select>
                        {!l.item_id && <input placeholder={t('oa.po.desc')} value={l.description} onChange={(e) => up({ description: e.currentTarget.value })} />}</td>
                      <td><input dir="ltr" style={{ width: 70 }} value={l.quantity} onChange={(e) => up({ quantity: e.currentTarget.value.replace(/[^\d.]/g, '') })} /></td>
                      <td><input dir="ltr" style={{ width: 80 }} value={l.unit_price} onChange={(e) => up({ unit_price: e.currentTarget.value.replace(/[^\d.]/g, '') })} /></td>
                      <td><select value={l.tax_rate_id} onChange={(e) => up({ tax_rate_id: e.currentTarget.value })}>
                        <option value="">—</option>{taxes.filter((x) => x.is_active && x.applies_to !== 'sales').map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}</select></td>
                      <td><button className="mx-btn sm" aria-label={t('oa.remove')} onClick={() => setEdit({ ...edit, lines: edit.lines.filter((_, j) => j !== i) })}>×</button></td>
                    </tr>
                  );
                })}</tbody>
              </table>
              <button className="mx-btn sm" onClick={() => setEdit({ ...edit, lines: [...edit.lines, blankLine()] })}>{t('oa.po.addLine')}</button>
              <label>{t('oa.notes')}<input value={edit.notes} onChange={(e) => setEdit({ ...edit, notes: e.currentTarget.value })} /></label>
              <div className="mx-row" style={{ gap: 6 }}>
                <button className="mx-btn" disabled={!edit.supplier_id} onClick={save}>{t('oa.save')}</button>
                <button className="mx-btn sm" onClick={() => setEdit(null)}>{t('oa.cancel')}</button>
              </div>
              <Flash m={msg} />
            </div>
          ) : o && sel ? (
            <div className="mx-pay" style={{ overflow: 'auto' }}>
              <div className="print-doc">
                <h3 style={{ margin: 0 }}>{t(o.status === 'rfq' || o.status === 'sent' ? 'oa.po.rfqTitle' : 'oa.po.poTitle')} #{o.number}</h3>
                <small>{o.supplier_name} · {formatDate(o.order_date)}{o.expected_date ? ` → ${formatDate(o.expected_date)}` : ''} · <b>{t(`oa.po.st.${o.status}`)}</b>{o.late ? ` · ${t('oa.po.late')}` : ''}</small>
                <table className="mx-table" style={{ marginTop: 6 }}>
                  <thead><tr><th>{t('oa.po.item')}</th><th className="num">{t('oa.po.qty')}</th><th className="num">{t('oa.po.price')}</th><th className="num">{t('fin.po.received')}</th><th className="num">{t('oa.po.billedQty')}</th>
                    {(o.status === 'confirmed' || o.status === 'partial' || o.status === 'billed') && canCreate && <th className="num no-print">{t('fin.po.toReceive')}</th>}
                    {(o.status === 'confirmed' || o.status === 'partial') && canCreate && <th className="num no-print">{t('oa.po.toBill')}</th>}</tr></thead>
                  <tbody>{sel.lines.map((l) => (
                    <tr key={l.id}>
                      <td>{l.description}{l.tax_name ? <small> · {l.tax_name}</small> : null}</td>
                      <td className="num">{l.quantity} {l.unit_name ?? l.unit ?? ''}</td>
                      <td className="num">{formatAmount(l.unit_price, 4).replace(/(\.\d*?)0+$/, '$1').replace(/\.$/, '')}</td>
                      <td className="num">{l.received_qty}</td>
                      <td className="num">{l.billed_qty}</td>
                      {(o.status === 'confirmed' || o.status === 'partial' || o.status === 'billed') && canCreate && <td className="num no-print">
                        <input dir="ltr" style={{ width: 70 }} value={recvQty[l.id] ?? ''} onChange={(e) => setRecvQty({ ...recvQty, [l.id]: e.currentTarget.value.replace(/[^\d.]/g, '') })} /></td>}
                      {(o.status === 'confirmed' || o.status === 'partial') && canCreate && <td className="num no-print">
                        <input dir="ltr" style={{ width: 70 }} value={billQty[l.id] ?? ''} onChange={(e) => setBillQty({ ...billQty, [l.id]: e.currentTarget.value.replace(/[^\d.]/g, '') })} /></td>}
                    </tr>
                  ))}</tbody>
                  <tfoot><tr><th colSpan={(o.status === 'confirmed' || o.status === 'partial') && canCreate ? 6 : (o.status === 'billed' && canCreate ? 5 : 4)}>{t('oa.po.total')}</th><th className="num">{formatAmount(o.total, dec)} {o.currency}</th></tr></tfoot>
                </table>
                {o.notes && <p style={{ margin: '4px 0' }}>{o.notes}</p>}
              </div>
              {warn.length > 0 && <div className="mx-flash bad">{t('fin.match.title')}: {warn.join(' · ')}</div>}
              {(sel.receipts ?? []).length > 0 && <small>{t('fin.po.receipts')}: {(sel.receipts ?? []).map((g) => `#${g.number} (${formatDate(g.receipt_date)} · ${g.quantity}${g.status === 'cancelled' ? ` · ${t('oa.void')}` : ''})`).join(' · ')}</small>}
              {sel.bills.length > 0 && <small>{t('oa.po.billsMade')}: {sel.bills.map((b) => `#${b.number} (${formatAmount(b.total, dec)}${b.status === 'void' ? ` · ${t('oa.void')}` : ''})`).join(' · ')}</small>}
              {canCreate && (
                <div className="mx-row no-print" style={{ gap: 6, flexWrap: 'wrap' }}>
                  {(o.status === 'rfq' || o.status === 'sent') && <button className="mx-btn" onClick={() => action('send')}>{t('oa.po.send')}</button>}
                  {(o.status === 'rfq' || o.status === 'sent') && <button className="mx-btn" onClick={() => action('confirm')}>{t('oa.po.confirm')}</button>}
                  {(o.status === 'rfq' || o.status === 'sent' || o.status === 'confirmed') && <button className="mx-btn sm" onClick={() => setEdit({ id: o.id, supplier_id: o.supplier_id, currency: o.currency, order_date: o.order_date, expected_date: o.expected_date ?? '', notes: o.notes,
                    lines: sel.lines.map((l) => ({ item_id: l.item_id ?? '', description: l.item_id ? '' : l.description, quantity: l.quantity, unit_price: l.unit_price, tax_rate_id: l.tax_rate_id ?? '' })) })}>{t('oa.po.edit')}</button>}
                  {(o.status === 'confirmed' || o.status === 'partial') && <>
                    <input placeholder={t('oa.po.supplierRef')} value={billRef} onChange={(e) => setBillRef(e.currentTarget.value)} style={{ width: 140 }} />
                    <button className="mx-btn" onClick={receive}>{t('fin.po.receive')}</button>
                    <button className="mx-btn" onClick={bill}>{t('oa.po.bill')}</button>
                  </>}
                  {(o.status === 'rfq' || o.status === 'sent' || o.status === 'confirmed') && <button className="mx-btn sm" onClick={() => { if (window.confirm(t('oa.po.cancelQ'))) action('cancel'); }}>{t('oa.po.cancel')}</button>}
                  <button className="mx-btn sm" onClick={() => void printPage(`PO ${o.number}`)}>{t('oa.print')}</button>
                </div>
              )}
              {text && (
                <div className="mx-note no-print"><small>{t('oa.po.messageReady')}</small>
                  <pre style={{ whiteSpace: 'pre-wrap', margin: 0 }}>{text}</pre>
                  <div className="mx-row" style={{ gap: 6 }}>
                    <button className="mx-btn sm" onClick={() => void navigator.clipboard?.writeText(text)}>{t('oa.copy')}</button>
                    {o.supplier_phone && <a className="mx-btn sm" target="_blank" rel="noreferrer" href={`https://wa.me/${o.supplier_phone.replace(/\D/g, '')}?text=${encodeURIComponent(text)}`}>WhatsApp</a>}
                  </div>
                </div>
              )}
              <Attachments type="purchase_order" id={o.id} readOnly={!canCreate} />
              <Flash m={msg} />
            </div>
          ) : <div className="mx-pay"><p>{t('oa.po.pick')}</p><Flash m={msg} /></div>}
        </section>
      </div>
    </div>
  );
}

// ======================= dunning & payment terms =======================

interface Level { level: number; name: string; days_overdue: number; send_message: boolean; message: string; fee_kind: string; fee_amount: string; fee_currency: string | null; min_balance: string; cut_service: boolean; is_active: boolean }
interface DunData { settings: { enabled: boolean; late_fee_account_id: string | null; base_currency: string }; levels: Level[] }
interface DueRow { invoice_id: string; number: number; customer_name: string; currency: string; balance: string; days_overdue: number; level: number; level_name: string; fee: string; fee_currency: string; send_message: boolean; cut_service: boolean }
interface EvRow { id: string; created_at: string; level: number; level_name: string | null; invoice_number: number; customer_name: string; customer_code: string; days_overdue: number; balance: string; currency: string; fee_invoice_number: number | null; fee_total: string | null; fee_currency: string | null; message_queued: boolean; service_cut: boolean }

const DEFAULT_LEVELS: Level[] = [
  { level: 1, name: '', days_overdue: 3, send_message: true, message: '', fee_kind: 'none', fee_amount: '0', fee_currency: null, min_balance: '0', cut_service: false, is_active: true },
  { level: 2, name: '', days_overdue: 10, send_message: true, message: '', fee_kind: 'none', fee_amount: '0', fee_currency: null, min_balance: '0', cut_service: false, is_active: true },
  { level: 3, name: '', days_overdue: 20, send_message: true, message: '', fee_kind: 'none', fee_amount: '0', fee_currency: null, min_balance: '0', cut_service: true, is_active: true },
];

export function Dunning({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const { currencies, decimalsOf } = useCompany();
  const [d, setD] = useState<DunData | null>(null);
  const [levels, setLevels] = useState<Level[]>([]);
  const [enabled, setEnabled] = useState(false);
  const [feeAcc, setFeeAcc] = useState('');
  const [preview, setPreview] = useState<DueRow[]>([]);
  const [events, reloadEv] = useList<EvRow>(base && `${base}/dunning/events`);
  const [terms, setTerms] = useState('0');
  const [msg, setMsg] = useState<Msg>(null);
  const canManage = can(me, 'billing.manage');

  const load = useCallback(() => {
    if (!base) return;
    api<DunData>(`${base}/dunning`).then((x) => {
      setD(x); setEnabled(x.settings.enabled); setFeeAcc(x.settings.late_fee_account_id ?? '');
      // amounts come as 5.0000 from the server: show them as typed (5)
      const trim = (v: string) => (v.includes('.') ? v.replace(/0+$/, '').replace(/\.$/, '') : v);
      setLevels(x.levels.length ? x.levels.map((l) => ({ ...l, fee_amount: trim(l.fee_amount ?? '0'), min_balance: trim(l.min_balance ?? '0') }))
        : DEFAULT_LEVELS.map((l) => ({ ...l, name: t(`oa.dun.default${l.level}`) })));
    }).catch(() => setD(null));
    api<{ rows: DueRow[] }>(`${base}/dunning/preview`).then((x) => setPreview(x.rows)).catch(() => setPreview([]));
    api<{ default_due_days: number }>(`${base}/payment-terms`).then((x) => setTerms(String(x.default_due_days))).catch(() => {});
  }, [base, t]);
  useEffect(load, [load]);

  async function run<T>(p: Promise<T>, ok: (x: T) => string) {
    try { const x = await p; setMsg({ ok: true, text: ok(x) }); load(); reloadEv(); } catch (e) { setMsg({ ok: false, text: errorText(t, e) }); }
  }
  const up = (i: number, p: Partial<Level>) => setLevels(levels.map((l, j) => (j === i ? { ...l, ...p } : l)));
  function save() {
    if (!base) return;
    const body = { enabled, late_fee_account_id: feeAcc, levels: levels.map((l, i) => ({ ...l, level: i + 1, fee_currency: l.fee_kind === 'fixed' ? (l.fee_currency || d?.settings.base_currency) : '' })) };
    void run(api(`${base}/dunning`, { method: 'PUT', body }), () => t('oa.saved'));
  }

  const pcols: Col<DueRow>[] = [
    { key: 'c', title: t('oa.dun.customer'), get: (r) => r.customer_name, width: 160 },
    { key: 'n', title: t('oa.dun.invoice'), get: (r) => r.number, num: true, decimals: 0, width: 70 },
    { key: 'd', title: t('oa.dun.days'), get: (r) => r.days_overdue, num: true, decimals: 0, width: 70 },
    { key: 'b', title: t('oa.dun.balance'), get: (r) => Number(r.balance), render: (r) => `${formatAmount(r.balance, decimalsOf(r.currency))} ${r.currency}`, num: true, width: 120 },
    { key: 'l', title: t('oa.dun.level'), get: (r) => `${r.level} · ${r.level_name}`, width: 140 },
    { key: 'f', title: t('oa.dun.fee'), get: (r) => Number(r.fee), render: (r) => (Number(r.fee) > 0 ? `${formatAmount(r.fee, decimalsOf(r.fee_currency))} ${r.fee_currency}` : '—'), num: true, width: 90 },
    { key: 'x', title: t('oa.dun.actions'), get: (r) => [r.send_message ? t('oa.dun.msg') : '', r.cut_service ? t('oa.dun.cut') : ''].filter(Boolean).join(' + '), width: 140, cls: (r) => (r.cut_service ? 'red' : '') },
  ];
  const ecols: Col<EvRow>[] = [
    { key: 'd', title: t('oa.dun.when'), get: (r) => r.created_at, render: (r) => formatDate(r.created_at.slice(0, 10)), width: 90 },
    { key: 'c', title: t('oa.dun.customer'), get: (r) => `${r.customer_code} ${r.customer_name}`, width: 170 },
    { key: 'n', title: t('oa.dun.invoice'), get: (r) => r.invoice_number, num: true, decimals: 0, width: 70 },
    { key: 'l', title: t('oa.dun.level'), get: (r) => `${r.level} · ${r.level_name ?? ''}`, width: 130 },
    { key: 'f', title: t('oa.dun.fee'), get: (r) => (r.fee_total ? `#${r.fee_invoice_number} · ${formatAmount(r.fee_total, decimalsOf(r.fee_currency ?? ''))} ${r.fee_currency}` : '—'), width: 140 },
    { key: 'm', title: t('oa.dun.msg'), get: (r) => (r.message_queued ? '✓' : ''), width: 60 },
    { key: 'x', title: t('oa.dun.cut'), get: (r) => (r.service_cut ? '✓' : ''), width: 60, cls: (r) => (r.service_cut ? 'red' : '') },
  ];

  return (
    <div className="mx-col">
      <div className="mx-strip"><b>{t('oa.dun.title')}</b>
        <span className="mx-pill">{enabled ? t('oa.dun.on') : t('oa.dun.off')}</span>
        <span className="mx-sp" /><Help topic="oa.help.dunning" /></div>
      <div className="mx-body" style={{ gridTemplateColumns: 'minmax(0, 1fr) minmax(320px, 560px)' }}>
        <section className="mx-box">
          <h3>{t('oa.dun.preview', { n: preview.length })}</h3>
          <DataGrid rows={preview} cols={pcols} rowKey={(r) => r.invoice_id + r.level} title={t('oa.dun.title')} empty={d && d.levels.length === 0 ? t('oa.dun.saveFirst') : t('oa.dun.nothing')} />
          {canManage && <div className="mx-row" style={{ padding: 6, gap: 6 }}>
            <button className="mx-btn" disabled={preview.length === 0} onClick={() => { if (window.confirm(t('oa.dun.runQ', { n: preview.length }))) void run(api<{ applied: number; fee_invoices: number; messages: number; services_cut: number }>(`${base}/dunning/run`, { method: 'POST', body: {} }), (x) => t('oa.dun.done', { a: x.applied, f: x.fee_invoices, m: x.messages, c: x.services_cut })); }}>{t('oa.dun.runNow')}</button>
          </div>}
          <h3>{t('oa.dun.history')}</h3>
          <DataGrid rows={events} cols={ecols} rowKey={(r) => r.id} title={t('oa.dun.history')} />
        </section>
        <section className="mx-box">
          <div className="mx-pay" style={{ overflow: 'auto' }}>
            <h3 style={{ margin: 0 }}>{t('oa.dun.levels')}</h3>
            <label className="mx-row" style={{ gap: 6 }}><input type="checkbox" checked={enabled} disabled={!canManage} onChange={(e) => setEnabled(e.currentTarget.checked)} />{t('oa.dun.auto')}</label>
            {levels.map((l, i) => (
              <fieldset key={i} className="mx-note" style={{ display: 'grid', gap: 4 }} disabled={!canManage}>
                <legend>{t('oa.dun.level')} {i + 1}</legend>
                <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
                  <label>{t('oa.dun.name')}<input value={l.name} onChange={(e) => up(i, { name: e.currentTarget.value })} /></label>
                  <label>{t('oa.dun.after')}<input type="number" min={0} dir="ltr" style={{ width: 70 }} value={l.days_overdue} onChange={(e) => up(i, { days_overdue: Number(e.currentTarget.value) })} /></label>
                </div>
                <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
                  <label>{t('oa.dun.fee')}<select value={l.fee_kind} onChange={(e) => up(i, { fee_kind: e.currentTarget.value })}>
                    <option value="none">{t('oa.dun.feeNone')}</option><option value="fixed">{t('oa.dun.feeFixed')}</option><option value="percent">{t('oa.dun.feePct')}</option></select></label>
                  {l.fee_kind !== 'none' && <label>{t('oa.amount')}<input dir="ltr" style={{ width: 80 }} value={l.fee_amount} onChange={(e) => up(i, { fee_amount: e.currentTarget.value.replace(/[^\d.]/g, '') })} /></label>}
                  {l.fee_kind === 'fixed' && <label>{t('oa.currency')}<select value={l.fee_currency ?? d?.settings.base_currency ?? ''} onChange={(e) => up(i, { fee_currency: e.currentTarget.value })}>
                    {currencies.map((c) => <option key={c.code} value={c.code}>{c.code}</option>)}</select></label>}
                </div>
                <label className="mx-row" style={{ gap: 6 }}><input type="checkbox" checked={l.send_message} onChange={(e) => up(i, { send_message: e.currentTarget.checked })} />{t('oa.dun.sendMsg')}</label>
                {l.send_message && <label>{t('oa.dun.message')}<textarea rows={2} placeholder={t('oa.dun.messageHint')} value={l.message} onChange={(e) => up(i, { message: e.currentTarget.value })} /></label>}
                <label className="mx-row" style={{ gap: 6 }}><input type="checkbox" checked={l.cut_service} onChange={(e) => up(i, { cut_service: e.currentTarget.checked })} />{t('oa.dun.cutService')}</label>
                <button className="mx-btn sm" onClick={() => setLevels(levels.filter((_, j) => j !== i))}>{t('oa.remove')}</button>
              </fieldset>
            ))}
            {canManage && levels.length < 9 && <button className="mx-btn sm" onClick={() => setLevels([...levels, { ...DEFAULT_LEVELS[0], level: levels.length + 1, name: '', days_overdue: (levels[levels.length - 1]?.days_overdue ?? 0) + 10, cut_service: false }])}>{t('oa.dun.addLevel')}</button>}
            {levels.some((l) => l.fee_kind !== 'none') && <AccountSelect label={t('oa.dun.feeAccount')} types={['income']} value={feeAcc} onChange={setFeeAcc} />}
            {canManage && <button className="mx-btn" onClick={save}>{t('oa.save')}</button>}
            <hr />
            <h3 style={{ margin: 0 }}>{t('oa.terms.title')}</h3>
            <small>{t('oa.terms.hint')}</small>
            <div className="mx-row" style={{ gap: 6 }}>
              <label>{t('oa.terms.days')}<input type="number" min={0} max={365} dir="ltr" style={{ width: 80 }} value={terms} disabled={!canManage} onChange={(e) => setTerms(e.currentTarget.value)} /></label>
              {canManage && <button className="mx-btn sm" onClick={() => void run(api(`${base}/payment-terms`, { method: 'PUT', body: { default_due_days: Number(terms) || 0 } }), () => t('oa.saved'))}>{t('oa.save')}</button>}
            </div>
            <Flash m={msg} />
          </div>
        </section>
      </div>
    </div>
  );
}

// ======================= exchange revaluation =======================

interface RevRow { account_code: string; account_name: string; currency: string; balance: string; booked_base: string; rate: string; revalued_base: string; difference: string }
interface RevPreview { period: string; date: string; base_currency: string; rows: RevRow[]; total_gain: string; total_loss: string; net: string; posted: { id: string; entry_id: string } | null }
interface RevPast { id: string; period: string; entry_number: number; reversal_number: number | null; total_gain: string; total_loss: string; created_at: string }

export function FxRevaluation({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const { decimalsOf } = useCompany();
  const [period, setPeriod] = useState(lastMonth());
  const [pv, setPv] = useState<RevPreview | null>(null);
  const [past, reloadPast] = useList<RevPast>(base && `${base}/fx-revaluation`);
  const [msg, setMsg] = useState<Msg>(null);
  const canRun = can(me, 'accounting.manage');
  const load = useCallback(() => {
    if (!base) return;
    api<RevPreview>(`${base}/fx-revaluation/preview?period=${period}`).then(setPv).catch((e) => { setPv(null); setMsg({ ok: false, text: errorText(t, e) }); });
  }, [base, period, t]);
  useEffect(load, [load]);
  const bd = decimalsOf(pv?.base_currency ?? 'USD');
  const cols: Col<RevRow>[] = [
    { key: 'a', title: t('oa.fx.account'), get: (r) => `${r.account_code} ${r.account_name}`, width: 200 },
    { key: 'c', title: t('oa.currency'), get: (r) => r.currency, width: 60 },
    { key: 'b', title: t('oa.fx.balance'), get: (r) => Number(r.balance), render: (r) => formatAmount(r.balance, decimalsOf(r.currency)), num: true, width: 130 },
    { key: 'k', title: t('oa.fx.booked'), get: (r) => Number(r.booked_base), render: (r) => formatAmount(r.booked_base, bd), num: true, width: 100, total: 'sum' },
    { key: 'r', title: t('oa.fx.rate'), get: (r) => Number(r.rate), render: (r) => (r.rate.includes('.') ? r.rate.replace(/0+$/, '').replace(/\.$/, '') : r.rate), num: true, width: 100 },
    { key: 'v', title: t('oa.fx.revalued'), get: (r) => Number(r.revalued_base), render: (r) => formatAmount(r.revalued_base, bd), num: true, width: 100, total: 'sum' },
    { key: 'd', title: t('oa.fx.diff'), get: (r) => Number(r.difference), render: (r) => formatAmount(r.difference, bd), num: true, width: 90, total: 'sum', cls: (r) => (Number(r.difference) < 0 ? 'red' : 'ok') },
  ];
  return (
    <div className="mx-col">
      <div className="mx-strip"><b>{t('oa.fx.title')}</b>
        <label className="mx-row" style={{ gap: 4 }}>{t('oa.fx.month')}<input type="month" dir="ltr" value={period} onChange={(e) => setPeriod(e.currentTarget.value)} /></label>
        <span className="mx-sp" /><Help topic="oa.help.fx" /></div>
      <div className="mx-body" style={{ gridTemplateColumns: 'minmax(0, 1fr) minmax(280px, 420px)' }}>
        <section className="mx-box"><h3>{t('oa.fx.preview', { date: pv ? formatDate(pv.date) : '' })}</h3>
          <DataGrid rows={pv?.rows ?? []} cols={cols} rowKey={(r) => r.account_code + r.currency} footer title={t('oa.fx.title')} empty={t('oa.fx.nothing')} />
        </section>
        <section className="mx-box">
          <div className="mx-pay">
            {pv && <>
              <div>{t('oa.fx.gain')}: <b className="ok" dir="ltr">{formatAmount(pv.total_gain, bd)} {pv.base_currency}</b></div>
              <div>{t('oa.fx.loss')}: <b className="red" dir="ltr">{formatAmount(pv.total_loss, bd)} {pv.base_currency}</b></div>
              <div>{t('oa.fx.net')}: <b dir="ltr">{formatAmount(pv.net, bd)} {pv.base_currency}</b></div>
              <small>{t('oa.fx.explain')}</small>
              {pv.posted ? <div className="mx-flash good">{t('oa.fx.alreadyDone')}</div>
                : canRun && <button className="mx-btn" disabled={pv.rows.length === 0} onClick={() => { if (window.confirm(t('oa.fx.runQ', { p: period }))) api(`${base}/fx-revaluation`, { method: 'POST', body: { period } }).then(() => { setMsg({ ok: true, text: t('oa.fx.done') }); load(); reloadPast(); }).catch((e) => setMsg({ ok: false, text: errorText(t, e) })); }}>{t('oa.fx.run')}</button>}
            </>}
            <Flash m={msg} />
            <h3 style={{ margin: '8px 0 0' }}>{t('oa.fx.past')}</h3>
            {past.length === 0 ? <small>—</small> : past.map((p) => (
              <div key={p.id} className="mx-note"><b>{p.period}</b> · {t('oa.fx.entry')} #{p.entry_number}{p.reversal_number ? ` / #${p.reversal_number}` : ''} · <bdi dir="ltr">+{formatAmount(p.total_gain, bd)} −{formatAmount(p.total_loss, bd)}</bdi></div>
            ))}
          </div>
        </section>
      </div>
    </div>
  );
}

// ======================= deferred revenue / prepaid expenses =======================

interface Def { id: string; number: number; kind: string; name: string; source_type: string; source_number: number | null; party: string; amount: string; months: number; start_period: string; status: string; recognised: string; remaining: string; months_done: number; deferral_account_code: string; target_account_code: string; target_account_name: string }
/** a schedule made from a document is named from its parts, in the reader's language */
function defName(t: TFunction, d: Def) {
  if (d.source_type === 'manual' || !d.party) return d.name;
  return t(`fin.defsrc.${d.source_type}`, { n: d.source_number ?? '', party: d.party });
}
interface DefList { rows: Def[]; settings: { base_currency: string; deferred_revenue_account_id: string | null; prepaid_expense_account_id: string | null } }
interface DefDetail { schedule: Def; lines: { period: string; amount: string; entry_number: number }[]; plan: { n: number; period: string; amount: string }[] }

export function Deferrals({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [data, setData] = useState<DefList | null>(null);
  const [sel, setSel] = useState<DefDetail | null>(null);
  const [nw, setNw] = useState({ kind: 'revenue', source: 'invoice', number: '', name: '', amount: '', months: '12', start_period: thisMonth(), target_account_id: '', deferral_account_id: '' });
  const [msg, setMsg] = useState<Msg>(null);
  const canEdit = can(me, 'accounting.edit');
  const load = useCallback(() => { if (base) api<DefList>(`${base}/deferrals`).then(setData).catch(() => setData(null)); }, [base]);
  useEffect(load, [load]);
  const open = (id: string) => base && api<DefDetail>(`${base}/deferrals/${id}`).then(setSel).catch((e) => setMsg({ ok: false, text: errorText(t, e) }));

  async function create() {
    if (!base) return;
    setMsg(null);
    try {
      const body: Record<string, unknown> = { kind: nw.kind, months: Number(nw.months), name: nw.name, amount: nw.amount, start_period: nw.start_period,
        target_account_id: nw.target_account_id, deferral_account_id: nw.deferral_account_id };
      if (nw.source !== 'manual') {
        // find the document by its number
        const list = await api<{ rows: { id: string; number: number }[] }>(nw.source === 'invoice' ? `${base}/invoices?limit=50&q=${nw.number}`
          : `${base}/bills?from=2000-01-01&to=2100-12-31&q=${nw.number}`);
        const doc = (list.rows ?? []).find((x) => String(x.number) === nw.number.trim());
        if (!doc) { setMsg({ ok: false, text: t('oa.def.noDoc') }); return; }
        body.source_type = nw.source; body.source_id = doc.id;
        if (!nw.start_period) delete body.start_period;
      }
      const d = await api<Def>(`${base}/deferrals`, { method: 'POST', body });
      setMsg({ ok: true, text: t('oa.saved') }); load(); void open(d.id);
    } catch (e) { setMsg({ ok: false, text: errorText(t, e) }); }
  }
  const cols: Col<Def>[] = [
    { key: 'n', title: '#', get: (r) => r.number, num: true, decimals: 0, width: 50 },
    { key: 'k', title: t('oa.def.kind'), get: (r) => t(`oa.def.k.${r.kind}`), width: 110 },
    { key: 'nm', title: t('oa.def.name'), get: (r) => defName(t, r), width: 200 },
    { key: 'a', title: t('oa.amount'), get: (r) => Number(r.amount), render: (r) => formatAmount(r.amount, 2), num: true, width: 100, total: 'sum' },
    { key: 'm', title: t('oa.def.months'), get: (r) => `${r.months_done}/${r.months}`, width: 70 },
    { key: 'r', title: t('oa.def.remaining'), get: (r) => Number(r.remaining), render: (r) => formatAmount(r.remaining, 2), num: true, width: 100, total: 'sum' },
    { key: 's', title: t('oa.def.status'), get: (r) => t(`oa.def.st.${r.status}`), width: 90 },
  ];
  const needDefAcc = nw.kind === 'revenue' ? !data?.settings.deferred_revenue_account_id : !data?.settings.prepaid_expense_account_id;
  return (
    <div className="mx-col">
      <div className="mx-strip"><b>{t('oa.def.title')}</b><span className="mx-sp" />
        {canEdit && <button className="mx-btn sm" onClick={() => base && api<{ entries: number; total: string }>(`${base}/deferrals/run`, { method: 'POST', body: { period: thisMonth() } }).then((x) => { setMsg({ ok: true, text: t('oa.def.ran', { n: x.entries, a: x.total }) }); load(); }).catch((e) => setMsg({ ok: false, text: errorText(t, e) }))}>{t('oa.def.runNow')}</button>}
        <Help topic="oa.help.def" /></div>
      <div className="mx-body" style={{ gridTemplateColumns: 'minmax(0, 1fr) minmax(300px, 460px)' }}>
        <section className="mx-box"><h3>{t('oa.def.list')}</h3>
          <DataGrid rows={data?.rows ?? []} cols={cols} rowKey={(r) => r.id} selected={sel?.schedule.id ?? null} footer title={t('oa.def.title')} onSelect={(r) => void open(r.id)} onActivate={(r) => void open(r.id)} />
        </section>
        <section className="mx-box">
          {sel ? (
            <div className="mx-pay" style={{ overflow: 'auto' }}>
              <h3 style={{ margin: 0 }}>#{sel.schedule.number} · {defName(t, sel.schedule)}</h3>
              <small>{t(`oa.def.k.${sel.schedule.kind}`)} · {sel.schedule.deferral_account_code} → {sel.schedule.target_account_code} {sel.schedule.target_account_name}</small>
              <table className="mx-table"><thead><tr><th>#</th><th>{t('oa.def.month')}</th><th className="num">{t('oa.amount')}</th><th>{t('oa.def.posted')}</th></tr></thead>
                <tbody>{sel.plan.map((p) => { const l = sel.lines.find((x) => x.period === p.period); return (
                  <tr key={p.period}><td>{p.n}</td><td>{p.period}</td><td className="num">{formatAmount(p.amount, 2)}</td><td>{l ? `✓ #${l.entry_number}` : ''}</td></tr>); })}</tbody></table>
              <div className="mx-row" style={{ gap: 6 }}>
                {canEdit && sel.schedule.status === 'active' && <button className="mx-btn sm" onClick={() => { if (window.confirm(t('oa.def.cancelQ'))) api(`${base}/deferrals/${sel.schedule.id}/cancel`, { method: 'POST', body: {} }).then(() => { load(); void open(sel.schedule.id); }).catch((e) => setMsg({ ok: false, text: errorText(t, e) })); }}>{t('oa.def.cancel')}</button>}
                <button className="mx-btn sm" onClick={() => setSel(null)}>{t('oa.def.new')}</button>
              </div>
              <Flash m={msg} />
            </div>
          ) : canEdit ? (
            <div className="mx-pay" style={{ overflow: 'auto' }}>
              <h3 style={{ margin: 0 }}>{t('oa.def.new')}</h3>
              <small>{t('oa.def.hint')}</small>
              <label>{t('oa.def.kind')}<select value={nw.kind} onChange={(e) => setNw({ ...nw, kind: e.currentTarget.value, source: e.currentTarget.value === 'revenue' ? 'invoice' : 'bill' })}>
                <option value="revenue">{t('oa.def.k.revenue')}</option><option value="expense">{t('oa.def.k.expense')}</option></select></label>
              <label>{t('oa.def.from')}<select value={nw.source} onChange={(e) => setNw({ ...nw, source: e.currentTarget.value })}>
                {nw.kind === 'revenue' ? <option value="invoice">{t('oa.def.fromInvoice')}</option> : <option value="bill">{t('oa.def.fromBill')}</option>}
                <option value="manual">{t('oa.def.manual')}</option></select></label>
              {nw.source !== 'manual'
                ? <label>{t('oa.def.docNumber')}<input dir="ltr" value={nw.number} onChange={(e) => setNw({ ...nw, number: e.currentTarget.value.replace(/\D/g, '') })} /></label>
                : <>
                  <label>{t('oa.def.name')}<input value={nw.name} onChange={(e) => setNw({ ...nw, name: e.currentTarget.value })} /></label>
                  <label>{t('oa.amount')}<input dir="ltr" value={nw.amount} onChange={(e) => setNw({ ...nw, amount: e.currentTarget.value.replace(/[^\d.]/g, '') })} /></label>
                  <AccountSelect label={nw.kind === 'revenue' ? t('oa.def.incomeAcc') : t('oa.def.expenseAcc')} types={[nw.kind === 'revenue' ? 'income' : 'expense']} value={nw.target_account_id} onChange={(v) => setNw({ ...nw, target_account_id: v })} />
                </>}
              <div className="mx-row" style={{ gap: 6 }}>
                <label>{t('oa.def.months')}<input type="number" min={2} max={120} dir="ltr" style={{ width: 70 }} value={nw.months} onChange={(e) => setNw({ ...nw, months: e.currentTarget.value })} /></label>
                <label>{t('oa.def.start')}<input type="month" dir="ltr" value={nw.start_period} onChange={(e) => setNw({ ...nw, start_period: e.currentTarget.value })} /></label>
              </div>
              {needDefAcc && <AccountSelect label={nw.kind === 'revenue' ? t('oa.def.deferredAcc') : t('oa.def.prepaidAcc')} types={[nw.kind === 'revenue' ? 'liability' : 'asset']} value={nw.deferral_account_id} onChange={(v) => setNw({ ...nw, deferral_account_id: v })} />}
              <button className="mx-btn" onClick={() => void create()}>{t('oa.save')}</button>
              <Flash m={msg} />
            </div>
          ) : <p>{t('oa.def.pick')}</p>}
        </section>
      </div>
    </div>
  );
}
