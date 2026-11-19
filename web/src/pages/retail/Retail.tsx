import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import { api, can, type Me } from '../../api';
import { useCompany } from '../../company';
import DataGrid, { type Col } from '../../components/DataGrid';
import { printPage, saveFile } from '../../native';
import { errorText, formatAmount, formatDate, formatDateTime, today } from '../../util';

// Retail core part 1: variants, promotions, loyalty, gift cards, scales and labels, near expiry, retail report,
// receiving by scanning, business packs (Magic-style screens).

type Msg = { ok: boolean; text: string } | null;
function Flash({ m }: { m: Msg }) {
  return m ? <div className={`mx-flash ${m.ok ? 'good' : 'bad'}`}>{m.text}</div> : null;
}
function useBase() {
  const { company } = useCompany();
  return company ? `/companies/${company.id}` : null;
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
function useRun(reload?: () => void): [Msg, (p: Promise<unknown>, after?: (x: never) => void) => Promise<void>, (m: Msg) => void] {
  const { t } = useTranslation();
  const [msg, setMsg] = useState<Msg>(null);
  const run = async (p: Promise<unknown>, after?: (x: never) => void) => {
    try { const x = await p; setMsg({ ok: true, text: t('rt.saved') }); after?.(x as never); reload?.(); } catch (e) { setMsg({ ok: false, text: errorText(t, e) }); }
  };
  return [msg, run, setMsg];
}
const num = (s: string) => s.replace(/[^\d.]/g, '');
const amt = (v: string | number | null | undefined, d = 2) => formatAmount(String(v ?? 0), d);
const i18nName = (x: { name: string; name_i18n?: Record<string, string> | null }, lang: string) => x.name_i18n?.[lang] || x.name;

interface ItemLite { id: string; code: string; name: string; name_i18n?: Record<string, string>; barcode?: string; sale_price?: string; quantity?: string }
interface Cat { id: string; name: string; name_i18n?: Record<string, string> }

/** Search items and pick one (or several). */
function ItemSearch({ onPick, placeholder }: { onPick: (it: ItemLite) => void; placeholder?: string }) {
  const { t, i18n } = useTranslation();
  const base = useBase();
  const [q, setQ] = useState('');
  const [rows, setRows] = useState<ItemLite[]>([]);
  useEffect(() => {
    if (!base || q.trim().length < 2) { setRows([]); return; }
    const h = window.setTimeout(() => { api<ItemLite[]>(`${base}/items?q=${encodeURIComponent(q.trim())}`).then((r) => setRows(r.slice(0, 12))).catch(() => setRows([])); }, 250);
    return () => window.clearTimeout(h);
  }, [base, q]);
  return (
    <div className="mx-col" style={{ gap: 4 }}>
      <input value={q} placeholder={placeholder ?? t('rt.searchItem')} onChange={(e) => setQ(e.currentTarget.value)}
        onKeyDown={(e) => { if (e.key === 'Enter' && rows.length === 1) { onPick(rows[0]); setQ(''); } }} />
      {rows.length > 0 && (
        <div className="mx-pay" style={{ flexWrap: 'wrap', display: 'flex', gap: 4 }}>
          {rows.map((r) => <button key={r.id} className="mx-btn sm" onClick={() => { onPick(r); setQ(''); setRows([]); }}>{r.code} · {i18nName(r, i18n.language)}</button>)}
        </div>
      )}
    </div>
  );
}

function Screen({ title, children, tabs }: { title: string; children: ReactNode; tabs?: ReactNode }) {
  return (
    <div className="mx-col">
      <div className="mx-strip"><b>{title}</b></div>
      {tabs}
      <div className="mx-scroll" style={{ padding: 8, overflow: 'auto', display: 'flex', flexDirection: 'column', gap: 10 }}>{children}</div>
    </div>
  );
}

const MODES = ['variants', 'promos', 'loyalty', 'gift', 'labels', 'expiry', 'receive', 'report', 'packs'] as const;
type Mode = (typeof MODES)[number];

export default function Retail({ me, initial }: { me: Me; initial?: string }) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<Mode>((MODES as readonly string[]).includes(initial ?? '') ? (initial as Mode) : 'promos');
  const tabs = (
    <div className="mx-row" role="tablist" style={{ gap: 4, padding: 6, flexWrap: 'wrap' }}>
      {MODES.map((k) => <button key={k} role="tab" aria-selected={mode === k} className={`mx-btn sm ${mode === k ? 'main' : ''}`} onClick={() => setMode(k)}>{t(`rt.m.${k}`)}</button>)}
    </div>
  );
  return (
    <Screen title={t(`rt.m.${mode}`)} tabs={tabs}>
      {mode === 'variants' && <Variants me={me} />}
      {mode === 'promos' && <Promotions me={me} />}
      {mode === 'loyalty' && <Loyalty me={me} />}
      {mode === 'gift' && <GiftCards me={me} />}
      {mode === 'labels' && <Labels me={me} />}
      {mode === 'expiry' && <Expiry me={me} />}
      {mode === 'receive' && <ScanReceive me={me} />}
      {mode === 'report' && <RetailReport />}
      {mode === 'packs' && <Packs me={me} />}
    </Screen>
  );
}

// ======================= variants =======================

interface Attr { name: string; values: string[] }
interface Tpl { id: string; code: string; name: string; name_i18n?: Record<string, string>; attributes: Attr[]; variants: number; quantity: string; sale_price: string }
interface Variant { id: string; code: string; name: string; barcode: string; variant_values: Record<string, string>; sale_price: string; is_active: boolean; quantity: string }
interface VarData { template: { id: string; name: string; attributes: Attr[]; attr_extras: Record<string, string>; is_template: boolean; sale_price: string }; variants: Variant[]; warehouses: { id: string; name: string }[] }

function Variants({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const base = useBase();
  const { company } = useCompany();
  const [list, reload] = useLoad<Tpl[]>(base && `${base}/items/templates`, []);
  const [sel, setSel] = useState<string | null>(null);
  const [data, setData] = useState<VarData | null>(null);
  const [attrs, setAttrs] = useState<{ name: string; values: string }[]>([{ name: '', values: '' }]);
  const [extras, setExtras] = useState('');
  const [order, setOrder] = useState<Record<string, string>>({});
  const [supplier, setSupplier] = useState('');
  const [sups] = useLoad<{ id: string; name: string; currency: string }[]>(base && `${base}/suppliers`, []);
  const [edits, setEdits] = useState<Record<string, { sale_price?: string; barcode?: string }>>({});
  const load = useCallback((id: string) => {
    if (!base) return;
    api<VarData>(`${base}/items/${id}/variants`).then((d) => {
      setData(d);
      setAttrs(d.template.attributes.length ? d.template.attributes.map((a) => ({ name: a.name, values: a.values.join(', ') })) : [{ name: t('rt.v.size'), values: 'S, M, L, XL' }, { name: t('rt.v.colour'), values: '' }]);
      setExtras(Object.entries(d.template.attr_extras ?? {}).map(([k, v]) => `${k}: ${v}`).join('\n'));
      setEdits({}); setOrder({});
    }).catch(() => setData(null));
  }, [base, t]);
  const [msg, run] = useRun(() => { reload(); if (sel) load(sel); });
  const manage = can(me, 'stock.manage');
  function pick(id: string) { setSel(id); load(id); }
  function saveSetup() {
    if (!base || !sel) return;
    const attributes = attrs.filter((a) => a.name.trim() && a.values.trim()).map((a) => ({ name: a.name.trim(), values: a.values.split(',').map((v) => v.trim()).filter(Boolean) }));
    const attr_extras: Record<string, string> = {};
    for (const line of extras.split('\n')) {
      const [k, v] = line.split(':').map((x) => x.trim());
      if (k && v) attr_extras[k] = num(v);
    }
    void run(api(`${base}/items/${sel}/variant-setup`, { method: 'PUT', body: { attributes, attr_extras } }));
  }
  const a1 = data?.template.attributes[0];
  const a2 = data?.template.attributes[1];
  const cell = (v1: string, v2?: string) => data?.variants.find((v) => v.variant_values[a1?.name ?? ''] === v1 && (!a2 || v.variant_values[a2.name] === v2));
  const cols: Col<Tpl>[] = [
    { key: 'code', title: t('rt.code'), get: (r) => r.code, width: 90 },
    { key: 'name', title: t('rt.name'), get: (r) => i18nName(r, i18n.language) },
    { key: 'attrs', title: t('rt.v.attributes'), get: (r) => r.attributes.map((a) => `${a.name} (${a.values.length})`).join(' × ') },
    { key: 'n', title: t('rt.v.variants'), get: (r) => r.variants, num: true, width: 80 },
    { key: 'q', title: t('rt.qty'), get: (r) => Number(r.quantity), num: true, width: 80, decimals: 0 },
  ];
  async function makePO() {
    if (!base || !supplier || !data) return;
    const lines = Object.entries(order).filter(([, q]) => Number(q) > 0).map(([item_id, quantity]) => ({ item_id, quantity, unit_price: '' }));
    const sup = sups.find((s) => s.id === supplier);
    await run(api(`${base}/purchase-orders`, { method: 'POST', body: { supplier_id: supplier, currency: sup?.currency ?? company?.base_currency, lines } }), () => setOrder({}));
  }
  return (
    <>
      <Flash m={msg} />
      <section className="mx-box"><h3>{t('rt.v.makeTemplate')}</h3>
        <small>{t('rt.v.hint')}</small>
        {manage && <ItemSearch onPick={(it) => pick(it.id)} />}
      </section>
      <section className="mx-box" style={{ height: 220 }}>
        <DataGrid rows={list} cols={cols} rowKey={(r) => r.id} selected={sel} onSelect={(r) => pick(r.id)} title={t('rt.m.variants')} empty={t('rt.v.none')} />
      </section>
      {data && (
        <>
          <section className="mx-box"><h3>{data.template.name} · {t('rt.v.attributes')}</h3>
            {attrs.map((a, i) => (
              <div key={i} className="mx-row" style={{ gap: 6, padding: 4, flexWrap: 'wrap' }}>
                <input style={{ width: 140 }} value={a.name} placeholder={t('rt.v.attrName')} onChange={(e) => setAttrs(attrs.map((x, j) => (j === i ? { ...x, name: e.currentTarget.value } : x)))} />
                <input style={{ flex: 1, minWidth: 200 }} value={a.values} placeholder={t('rt.v.attrValues')} onChange={(e) => setAttrs(attrs.map((x, j) => (j === i ? { ...x, values: e.currentTarget.value } : x)))} />
              </div>
            ))}
            <div className="mx-row" style={{ gap: 6, padding: 4, flexWrap: 'wrap' }}>
              {attrs.length < 3 && <button className="mx-btn sm" onClick={() => setAttrs([...attrs, { name: '', values: '' }])}>+ {t('rt.v.addAttr')}</button>}
            </div>
            <label className="mx-col" style={{ padding: 4 }}>{t('rt.v.extras')}
              <textarea rows={3} dir="ltr" value={extras} placeholder="Size=XL: 2" onChange={(e) => setExtras(e.currentTarget.value)} /></label>
            {manage && (
              <div className="mx-row" style={{ gap: 6, padding: 4 }}>
                <button className="mx-btn" onClick={saveSetup}>{t('rt.v.saveSetup')}</button>
                <button className="mx-btn main" disabled={!data.template.is_template} onClick={() => sel && void run(api(`${base}/items/${sel}/variants/generate`, { method: 'POST', body: {} }))}>{t('rt.v.generate')}</button>
              </div>
            )}
          </section>
          {data.variants.length > 0 && a1 && (
            <section className="mx-box"><h3>{t('rt.v.matrix')}</h3>
              <div style={{ overflowX: 'auto' }}>
                <table className="mx-table">
                  <thead><tr><th>{a1.name}{a2 ? ` \\ ${a2.name}` : ''}</th>{(a2 ? a2.values : ['']).map((v) => <th key={v}>{v}</th>)}</tr></thead>
                  <tbody>
                    {a1.values.map((v1) => (
                      <tr key={v1}><th>{v1}</th>
                        {(a2 ? a2.values : [undefined]).map((v2) => {
                          const c = cell(v1, v2);
                          return (
                            <td key={v2 ?? '-'} style={{ textAlign: 'center' }}>
                              {c ? <>
                                <b dir="ltr">{Number(c.quantity)}</b> <small dir="ltr">· {amt(c.sale_price)}</small>
                                {can(me, 'purchasing.create') && <input style={{ width: 56, display: 'block', margin: '2px auto' }} dir="ltr" placeholder={t('rt.v.order')}
                                  value={order[c.id] ?? ''} onChange={(e) => setOrder({ ...order, [c.id]: num(e.currentTarget.value) })} />}
                              </> : '—'}
                            </td>
                          );
                        })}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              {can(me, 'purchasing.create') && (
                <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap' }}>
                  <select value={supplier} onChange={(e) => setSupplier(e.currentTarget.value)}>
                    <option value="">{t('rt.v.supplier')}</option>
                    {sups.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
                  </select>
                  <button className="mx-btn" disabled={!supplier || !Object.values(order).some((q) => Number(q) > 0)} onClick={() => void makePO()}>{t('rt.v.makePO')}</button>
                </div>
              )}
            </section>
          )}
          {data.variants.length > 0 && (
            <section className="mx-box"><h3>{t('rt.v.list')}</h3>
              <div style={{ overflowX: 'auto' }}>
                <table className="mx-table">
                  <thead><tr><th>{t('rt.code')}</th><th>{t('rt.name')}</th><th>{t('rt.barcode')}</th><th>{t('rt.price')}</th><th>{t('rt.qty')}</th></tr></thead>
                  <tbody>{data.variants.map((v) => (
                    <tr key={v.id}><td dir="ltr">{v.code}</td><td>{v.name}</td>
                      <td><input dir="ltr" style={{ width: 130 }} disabled={!manage} value={edits[v.id]?.barcode ?? v.barcode} onChange={(e) => setEdits({ ...edits, [v.id]: { ...edits[v.id], barcode: e.currentTarget.value } })} /></td>
                      <td><input dir="ltr" style={{ width: 80 }} disabled={!manage} value={edits[v.id]?.sale_price ?? Number(v.sale_price).toString()} onChange={(e) => setEdits({ ...edits, [v.id]: { ...edits[v.id], sale_price: num(e.currentTarget.value) } })} /></td>
                      <td dir="ltr">{Number(v.quantity)}</td></tr>
                  ))}</tbody>
                </table>
              </div>
              {manage && <button className="mx-btn" disabled={!Object.keys(edits).length} onClick={() => sel && void run(api(`${base}/items/${sel}/variants`, { method: 'PUT', body: { variants: Object.entries(edits).map(([id, e]) => ({ id, ...e })) } }))}>{t('rt.save')}</button>}
            </section>
          )}
        </>
      )}
    </>
  );
}

// ======================= promotions =======================

interface Promo {
  id: string; name: string; name_i18n: Record<string, string>; kind: string; applies_to: string; item_ids: string[]; category_ids: string[]; brands: string[];
  value_type: string; value: string; buy_qty: string; get_qty: string; tiers: { min_qty: string; pct: string }[]; bundle: { item_id: string; qty: string }[];
  min_amount: string; currency: string | null; coupon_code: string; date_from: string | null; date_to: string | null; days: number[]; time_from: string | null;
  time_to: string | null; register_ids: string[]; price_list_ids: string[]; stackable: boolean; priority: number; max_uses: number | null; uses: number;
  is_active: boolean; discount_given: string;
}
const blankPromo = {
  name: '', name_ar: '', kind: 'discount', applies_to: 'all', item_ids: [] as string[], item_names: {} as Record<string, string>, category_ids: [] as string[], brands: '',
  value_type: 'pct', value: '', buy_qty: '2', get_qty: '1', tiers: '', bundle: [] as { item_id: string; qty: string }[], min_amount: '', currency: '', coupon_code: '',
  date_from: '', date_to: '', days: [] as number[], time_from: '', time_to: '', register_ids: [] as string[], stackable: false, priority: '0', max_uses: '', is_active: true, id: '',
};

function Promotions({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const base = useBase();
  const { company } = useCompany();
  const [list, reload] = useLoad<Promo[]>(base && `${base}/retail/promotions`, []);
  const [cats] = useLoad<Cat[]>(base && `${base}/pos/categories`, []);
  const [regs] = useLoad<{ id: string; name: string }[]>(base && `${base}/pos/registers`, []);
  const [f, setF] = useState({ ...blankPromo });
  const [msg, run] = useRun(reload);
  const manage = can(me, 'pos.manage');
  const days = t('rt.p.dayNames').split(',');
  const [allItems] = useLoad<ItemLite[]>(base && `${base}/items`, []);
  const nameOf = useMemo(() => new Map(allItems.map((x) => [x.id, i18nName(x, i18n.language)])), [allItems, i18n.language]);
  function edit(p: Promo) {
    setF({
      id: p.id, name: p.name, name_ar: p.name_i18n?.ar ?? '', kind: p.kind, applies_to: p.applies_to, item_ids: p.item_ids,
      item_names: Object.fromEntries([...p.item_ids, ...p.bundle.map((b) => b.item_id)].map((x) => [x, nameOf.get(x) ?? x.slice(0, 8)])),
      category_ids: p.category_ids, brands: p.brands.join(', '), value_type: p.value_type, value: Number(p.value).toString(), buy_qty: Number(p.buy_qty).toString(),
      get_qty: Number(p.get_qty).toString(), tiers: p.tiers.map((x) => `${x.min_qty}:${x.pct}`).join(', '), bundle: p.bundle, min_amount: Number(p.min_amount).toString(),
      currency: p.currency ?? '', coupon_code: p.coupon_code, date_from: p.date_from ?? '', date_to: p.date_to ?? '', days: p.days, time_from: p.time_from ?? '', time_to: p.time_to ?? '',
      register_ids: p.register_ids, stackable: p.stackable, priority: String(p.priority), max_uses: p.max_uses ? String(p.max_uses) : '', is_active: p.is_active,
    });
  }
  function save() {
    if (!base) return;
    const body = {
      name: f.name, name_i18n: f.name_ar ? { ar: f.name_ar } : {}, kind: f.kind, applies_to: f.kind === 'basket' || f.kind === 'bundle' ? 'all' : f.applies_to,
      item_ids: f.item_ids, category_ids: f.category_ids, brands: f.brands.split(',').map((x) => x.trim()).filter(Boolean),
      value_type: f.kind === 'bxgy' || f.kind === 'tiers' ? 'pct' : f.value_type, value: f.kind === 'bxgy' && !f.value ? '100' : f.value || '0',
      buy_qty: f.buy_qty, get_qty: f.get_qty,
      tiers: f.tiers.split(',').map((x) => x.split(':').map((y) => y.trim())).filter((x) => x[0] && x[1]).map(([min_qty, pct]) => ({ min_qty, pct })),
      bundle: f.bundle, min_amount: f.min_amount || '0', currency: f.currency || (f.value_type !== 'pct' || f.kind === 'basket' ? company?.base_currency ?? '' : ''),
      coupon_code: f.coupon_code, date_from: f.date_from, date_to: f.date_to, days: f.days, time_from: f.time_from, time_to: f.time_to, register_ids: f.register_ids,
      price_list_ids: [], stackable: f.stackable, priority: Number(f.priority) || 0, max_uses: f.max_uses ? Number(f.max_uses) : null, is_active: f.is_active,
    };
    void run(api(f.id ? `${base}/retail/promotions/${f.id}` : `${base}/retail/promotions`, { method: f.id ? 'PUT' : 'POST', body }), () => setF({ ...blankPromo }));
  }
  const cols: Col<Promo>[] = [
    { key: 'name', title: t('rt.name'), get: (r) => i18nName(r, i18n.language) },
    { key: 'kind', title: t('rt.p.kind'), get: (r) => t(`rt.p.k.${r.kind}`), width: 130 },
    { key: 'val', title: t('rt.p.value'), get: (r) => (r.kind === 'bxgy' ? `${Number(r.buy_qty)}+${Number(r.get_qty)}` : r.value_type === 'pct' ? `${Number(r.value)}%` : `${amt(r.value)} ${r.currency ?? ''}`), width: 100 },
    { key: 'when', title: t('rt.p.when'), get: (r) => [r.date_from && formatDate(r.date_from), r.date_to && `→ ${formatDate(r.date_to)}`, r.time_from && `${r.time_from}-${r.time_to}`, r.coupon_code && `🎟 ${r.coupon_code}`].filter(Boolean).join(' ') },
    { key: 'uses', title: t('rt.p.uses'), get: (r) => r.uses, num: true, width: 70, decimals: 0 },
    { key: 'given', title: t('rt.p.given'), get: (r) => Number(r.discount_given), num: true, width: 90, decimals: 2 },
    { key: 'on', title: t('rt.active'), get: (r) => (r.is_active ? '✓' : '—'), width: 60 },
  ];
  const toggle = <T,>(arr: T[], v: T) => (arr.includes(v) ? arr.filter((x) => x !== v) : [...arr, v]);
  return (
    <>
      <Flash m={msg} />
      <section className="mx-box" style={{ height: 240 }}>
        <DataGrid rows={list} cols={cols} rowKey={(r) => r.id} selected={f.id || null} onSelect={edit} title={t('rt.m.promos')} empty={t('rt.p.none')} />
      </section>
      {manage && (
        <section className="mx-box"><h3>{f.id ? t('rt.p.edit') : t('rt.p.new')}</h3>
          <div className="mx-row" style={{ gap: 8, padding: 6, flexWrap: 'wrap', alignItems: 'flex-end' }}>
            <label>{t('rt.name')}<input value={f.name} onChange={(e) => setF({ ...f, name: e.currentTarget.value })} /></label>
            <label>{t('rt.nameAr')}<input dir="rtl" value={f.name_ar} onChange={(e) => setF({ ...f, name_ar: e.currentTarget.value })} /></label>
            <label>{t('rt.p.kind')}<select value={f.kind} onChange={(e) => setF({ ...f, kind: e.currentTarget.value })}>
              {['discount', 'bxgy', 'tiers', 'bundle', 'basket'].map((k) => <option key={k} value={k}>{t(`rt.p.k.${k}`)}</option>)}</select></label>
            {(f.kind === 'discount' || f.kind === 'bxgy' || f.kind === 'tiers') && (
              <label>{t('rt.p.appliesTo')}<select value={f.applies_to} onChange={(e) => setF({ ...f, applies_to: e.currentTarget.value })}>
                {['all', 'items', 'categories', 'brands'].map((k) => <option key={k} value={k}>{t(`rt.p.a.${k}`)}</option>)}</select></label>
            )}
          </div>
          <small style={{ padding: '0 6px' }}>{t(`rt.p.help.${f.kind}`)}</small>
          {(f.kind === 'discount' || f.kind === 'bundle' || f.kind === 'basket') && (
            <div className="mx-row" style={{ gap: 8, padding: 6, flexWrap: 'wrap', alignItems: 'flex-end' }}>
              <label>{t('rt.p.valueType')}<select value={f.value_type} onChange={(e) => setF({ ...f, value_type: e.currentTarget.value })}>
                {(f.kind === 'basket' ? ['pct', 'amount'] : ['pct', 'amount', 'price']).map((k) => <option key={k} value={k}>{t(`rt.p.vt.${k}`)}</option>)}</select></label>
              <label>{t('rt.p.value')}<input dir="ltr" style={{ width: 90 }} value={f.value} onChange={(e) => setF({ ...f, value: num(e.currentTarget.value) })} /></label>
              {f.value_type !== 'pct' && <label>{t('rt.currency')}<input dir="ltr" style={{ width: 70 }} value={f.currency} placeholder={company?.base_currency} onChange={(e) => setF({ ...f, currency: e.currentTarget.value.toUpperCase() })} /></label>}
              {f.kind === 'basket' && <label>{t('rt.p.minAmount')}<input dir="ltr" style={{ width: 90 }} value={f.min_amount} onChange={(e) => setF({ ...f, min_amount: num(e.currentTarget.value) })} /></label>}
            </div>
          )}
          {f.kind === 'bxgy' && (
            <div className="mx-row" style={{ gap: 8, padding: 6, flexWrap: 'wrap', alignItems: 'flex-end' }}>
              <label>{t('rt.p.buy')}<input dir="ltr" style={{ width: 60 }} value={f.buy_qty} onChange={(e) => setF({ ...f, buy_qty: num(e.currentTarget.value) })} /></label>
              <label>{t('rt.p.get')}<input dir="ltr" style={{ width: 60 }} value={f.get_qty} onChange={(e) => setF({ ...f, get_qty: num(e.currentTarget.value) })} /></label>
              <label>{t('rt.p.getPct')}<input dir="ltr" style={{ width: 60 }} value={f.value} placeholder="100" onChange={(e) => setF({ ...f, value: num(e.currentTarget.value) })} /></label>
            </div>
          )}
          {f.kind === 'tiers' && (
            <label className="mx-col" style={{ padding: 6 }}>{t('rt.p.tiers')}<input dir="ltr" value={f.tiers} placeholder="3:5, 6:10, 12:15" onChange={(e) => setF({ ...f, tiers: e.currentTarget.value })} /></label>
          )}
          {f.kind === 'bundle' && (
            <div className="mx-col" style={{ padding: 6, gap: 4 }}>
              <b>{t('rt.p.bundleItems')}</b>
              {f.bundle.map((b, i) => (
                <div key={b.item_id} className="mx-row" style={{ gap: 6 }}>
                  <span style={{ flex: 1 }}>{f.item_names[b.item_id] ?? b.item_id.slice(0, 8)}</span>
                  <input dir="ltr" style={{ width: 60 }} value={b.qty} onChange={(e) => setF({ ...f, bundle: f.bundle.map((x, j) => (j === i ? { ...x, qty: num(e.currentTarget.value) } : x)) })} />
                  <button className="mx-btn sm" onClick={() => setF({ ...f, bundle: f.bundle.filter((_, j) => j !== i) })}>✕</button>
                </div>
              ))}
              <ItemSearch onPick={(it) => setF({ ...f, bundle: [...f.bundle, { item_id: it.id, qty: '1' }], item_names: { ...f.item_names, [it.id]: it.name } })} />
            </div>
          )}
          {f.applies_to === 'items' && f.kind !== 'bundle' && f.kind !== 'basket' && (
            <div className="mx-col" style={{ padding: 6, gap: 4 }}>
              <div className="mx-row" style={{ gap: 4, flexWrap: 'wrap' }}>
                {f.item_ids.map((x) => <button key={x} className="mx-btn sm" onClick={() => setF({ ...f, item_ids: f.item_ids.filter((y) => y !== x) })}>{f.item_names[x] ?? x.slice(0, 8)} ✕</button>)}
              </div>
              <ItemSearch onPick={(it) => !f.item_ids.includes(it.id) && setF({ ...f, item_ids: [...f.item_ids, it.id], item_names: { ...f.item_names, [it.id]: it.name } })} />
            </div>
          )}
          {f.applies_to === 'categories' && f.kind !== 'bundle' && f.kind !== 'basket' && (
            <div className="mx-row" style={{ gap: 8, padding: 6, flexWrap: 'wrap' }}>
              {cats.map((c) => <label key={c.id} className="mx-row" style={{ gap: 4 }}><input type="checkbox" checked={f.category_ids.includes(c.id)}
                onChange={() => setF({ ...f, category_ids: toggle(f.category_ids, c.id) })} />{i18nName(c, i18n.language)}</label>)}
            </div>
          )}
          {f.applies_to === 'brands' && f.kind !== 'bundle' && f.kind !== 'basket' && (
            <label className="mx-col" style={{ padding: 6 }}>{t('rt.p.brands')}<input value={f.brands} onChange={(e) => setF({ ...f, brands: e.currentTarget.value })} /></label>
          )}
          <details style={{ padding: 6 }}><summary>{t('rt.p.conditions')}</summary>
            <div className="mx-row" style={{ gap: 8, padding: 6, flexWrap: 'wrap', alignItems: 'flex-end' }}>
              <label>{t('rt.from')}<input type="date" dir="ltr" value={f.date_from} onChange={(e) => setF({ ...f, date_from: e.currentTarget.value })} /></label>
              <label>{t('rt.to')}<input type="date" dir="ltr" value={f.date_to} onChange={(e) => setF({ ...f, date_to: e.currentTarget.value })} /></label>
              <label>{t('rt.p.timeFrom')}<input type="time" dir="ltr" value={f.time_from} onChange={(e) => setF({ ...f, time_from: e.currentTarget.value })} /></label>
              <label>{t('rt.p.timeTo')}<input type="time" dir="ltr" value={f.time_to} onChange={(e) => setF({ ...f, time_to: e.currentTarget.value })} /></label>
              <label>{t('rt.p.coupon')}<input dir="ltr" style={{ width: 110 }} value={f.coupon_code} onChange={(e) => setF({ ...f, coupon_code: e.currentTarget.value.toUpperCase() })} /></label>
              <label>{t('rt.p.maxUses')}<input dir="ltr" style={{ width: 70 }} value={f.max_uses} onChange={(e) => setF({ ...f, max_uses: num(e.currentTarget.value) })} /></label>
              <label>{t('rt.p.priority')}<input dir="ltr" style={{ width: 60 }} value={f.priority} onChange={(e) => setF({ ...f, priority: e.currentTarget.value.replace(/[^\d-]/g, '') })} /></label>
            </div>
            <div className="mx-row" style={{ gap: 8, padding: 6, flexWrap: 'wrap' }}>
              {days.map((d, i) => <label key={i} className="mx-row" style={{ gap: 3 }}><input type="checkbox" checked={f.days.includes(i)} onChange={() => setF({ ...f, days: toggle(f.days, i) })} />{d}</label>)}
            </div>
            {regs.length > 1 && <div className="mx-row" style={{ gap: 8, padding: 6, flexWrap: 'wrap' }}>
              {regs.map((g) => <label key={g.id} className="mx-row" style={{ gap: 3 }}><input type="checkbox" checked={f.register_ids.includes(g.id)} onChange={() => setF({ ...f, register_ids: toggle(f.register_ids, g.id) })} />{g.name}</label>)}
            </div>}
            <label className="mx-row" style={{ gap: 6, padding: 6 }}><input type="checkbox" checked={f.stackable} onChange={(e) => setF({ ...f, stackable: e.currentTarget.checked })} />{t('rt.p.stackable')}</label>
          </details>
          <div className="mx-row" style={{ gap: 6, padding: 6 }}>
            <label className="mx-row" style={{ gap: 6 }}><input type="checkbox" checked={f.is_active} onChange={(e) => setF({ ...f, is_active: e.currentTarget.checked })} />{t('rt.active')}</label>
            <button className="mx-btn main" disabled={!f.name.trim()} onClick={save}>{t('rt.save')}</button>
            {f.id && <button className="mx-btn" onClick={() => setF({ ...blankPromo })}>{t('rt.p.new')}</button>}
          </div>
        </section>
      )}
    </>
  );
}

// ======================= loyalty =======================

interface Member { id: string; code: string; name: string; phone: string; loyalty_card: string; points: string; lifetime: string }

function Loyalty({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const { company } = useCompany();
  const [d, reloadP] = useLoad<{ program: { is_active: boolean; earn_per: string; point_value: string; min_redeem: string; expiry_months: number; tiers: { name: string; min_points: string; multiplier: string }[] }; stats: { members: number; points: string } } | null>(base && `${base}/retail/loyalty`, null);
  const [p, setP] = useState({ is_active: false, earn_per: '1', point_value: '0.01', min_redeem: '0', expiry_months: '0', tiers: '' });
  useEffect(() => {
    if (d) setP({ is_active: d.program.is_active, earn_per: String(Number(d.program.earn_per)), point_value: String(Number(d.program.point_value)), min_redeem: String(Number(d.program.min_redeem)),
      expiry_months: String(d.program.expiry_months), tiers: d.program.tiers.map((x) => `${x.name}:${x.min_points}:${x.multiplier}`).join(', ') });
  }, [d]);
  const [q, setQ] = useState('');
  const [members, reloadM] = useLoad<Member[]>(base && `${base}/retail/loyalty/members?q=${encodeURIComponent(q)}`, []);
  const [sel, setSel] = useState<Member | null>(null);
  const [moves, reloadMv] = useLoad<{ id: string; kind: string; points: string; value: string; note: string; created_at: string; pos_number: number | null }[]>(base && sel ? `${base}/retail/loyalty/customers/${sel.id}/moves` : null, []);
  const [adj, setAdj] = useState({ points: '', note: '' });
  const [msg, run] = useRun(() => { reloadP(); reloadM(); reloadMv(); });
  const manage = can(me, 'pos.manage');
  function save() {
    if (!base) return;
    const tiers = p.tiers.split(',').map((x) => x.split(':').map((y) => y.trim())).filter((x) => x[0] && x[1]).map(([name, min_points, multiplier]) => ({ name, min_points, multiplier: multiplier || '1' }));
    void run(api(`${base}/retail/loyalty`, { method: 'PUT', body: { is_active: p.is_active, earn_per: p.earn_per, point_value: p.point_value, min_redeem: p.min_redeem || '0', expiry_months: Number(p.expiry_months) || 0, tiers } }));
  }
  const cols: Col<Member>[] = [
    { key: 'code', title: t('rt.code'), get: (r) => r.code, width: 80 },
    { key: 'name', title: t('rt.name'), get: (r) => r.name },
    { key: 'phone', title: t('rt.l.phone'), get: (r) => r.phone, width: 110 },
    { key: 'card', title: t('rt.l.card'), get: (r) => r.loyalty_card, width: 130 },
    { key: 'pts', title: t('rt.l.points'), get: (r) => Number(r.points), num: true, width: 90, decimals: 0, total: 'sum' },
    { key: 'life', title: t('rt.l.lifetime'), get: (r) => Number(r.lifetime), num: true, width: 90, decimals: 0 },
  ];
  return (
    <>
      <Flash m={msg} />
      <section className="mx-box"><h3>{t('rt.l.program')}</h3>
        <small style={{ padding: 6 }}>{t('rt.l.hint', { cur: company?.base_currency ?? '' })}</small>
        <div className="mx-row" style={{ gap: 8, padding: 6, flexWrap: 'wrap', alignItems: 'flex-end' }}>
          <label className="mx-row" style={{ gap: 6 }}><input type="checkbox" checked={p.is_active} disabled={!manage} onChange={(e) => setP({ ...p, is_active: e.currentTarget.checked })} />{t('rt.active')}</label>
          <label>{t('rt.l.earnPer', { cur: company?.base_currency ?? '' })}<input dir="ltr" style={{ width: 70 }} value={p.earn_per} onChange={(e) => setP({ ...p, earn_per: num(e.currentTarget.value) })} /></label>
          <label>{t('rt.l.pointValue', { cur: company?.base_currency ?? '' })}<input dir="ltr" style={{ width: 80 }} value={p.point_value} onChange={(e) => setP({ ...p, point_value: num(e.currentTarget.value) })} /></label>
          <label>{t('rt.l.minRedeem')}<input dir="ltr" style={{ width: 70 }} value={p.min_redeem} onChange={(e) => setP({ ...p, min_redeem: num(e.currentTarget.value) })} /></label>
          <label>{t('rt.l.tiers')}<input dir="ltr" style={{ width: 260 }} value={p.tiers} placeholder="Silver:0:1, Gold:2000:1.5" onChange={(e) => setP({ ...p, tiers: e.currentTarget.value })} /></label>
          {manage && <button className="mx-btn main" onClick={save}>{t('rt.save')}</button>}
        </div>
        {d && <div className="mx-pay">{t('rt.l.stats', { members: d.stats.members, points: Number(d.stats.points).toLocaleString() })}</div>}
      </section>
      <section className="mx-box">
        <div className="mx-row" style={{ gap: 6, padding: 6 }}><input value={q} placeholder={t('rt.l.search')} onChange={(e) => setQ(e.currentTarget.value)} /></div>
        <div style={{ height: 240 }}><DataGrid rows={members} cols={cols} rowKey={(r) => r.id} selected={sel?.id} onSelect={setSel} title={t('rt.m.loyalty')} footer /></div>
      </section>
      {sel && (
        <section className="mx-box"><h3>{sel.name} · {Number(sel.points)} {t('rt.l.pts')}</h3>
          <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'flex-end' }}>
            {can(me, 'pos.create') && <button className="mx-btn" onClick={() => void run(api(`${base}/retail/loyalty/customers/${sel.id}`, { method: 'PUT', body: { card: 'auto' } }), (x: Member) => setSel(x))}>{t('rt.l.newCard')}</button>}
            {manage && <>
              <label>{t('rt.l.adjust')}<input dir="ltr" style={{ width: 80 }} value={adj.points} placeholder="+50 / -20" onChange={(e) => setAdj({ ...adj, points: e.currentTarget.value.replace(/[^\d.-]/g, '') })} /></label>
              <label>{t('rt.note')}<input value={adj.note} onChange={(e) => setAdj({ ...adj, note: e.currentTarget.value })} /></label>
              <button className="mx-btn" disabled={!adj.points || !adj.note.trim()} onClick={() => void run(api(`${base}/retail/loyalty/customers/${sel.id}/adjust`, { method: 'POST', body: adj }), (x: Member) => { setSel(x); setAdj({ points: '', note: '' }); })}>{t('rt.save')}</button>
            </>}
          </div>
          <table className="mx-table"><thead><tr><th>{t('rt.date')}</th><th>{t('rt.l.kind')}</th><th>{t('rt.l.points')}</th><th>{t('rt.note')}</th></tr></thead>
            <tbody>{moves.map((m) => <tr key={m.id}><td dir="ltr">{formatDateTime(m.created_at)}</td><td>{t(`rt.l.k.${m.kind}`)}</td><td dir="ltr">{Number(m.points)}</td><td>{m.note}</td></tr>)}</tbody></table>
        </section>
      )}
    </>
  );
}

// ======================= gift cards =======================

interface Card { id: string; code: string; currency: string; initial: string; balance: string; status: string; expires_on: string | null; source: string; customer_name: string | null; created_at: string }

function GiftCards({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const { company, decimalsOf } = useCompany();
  const [q, setQ] = useState('');
  const [d, reload] = useLoad<{ rows: Card[]; totals: { currency: string; cards: number; outstanding: string }[] }>(base && `${base}/retail/gift-cards?q=${encodeURIComponent(q)}`, { rows: [], totals: [] });
  const [sel, setSel] = useState<Card | null>(null);
  const [moves] = useLoad<{ kind: string; amount: string; created_at: string; pos_number: number | null }[]>(base && sel ? `${base}/retail/gift-cards/${sel.id}/moves` : null, []);
  const [v, setV] = useState({ amount: '', currency: company?.base_currency ?? 'USD', code: '', expires_on: '', note: '' });
  const [msg, run] = useRun(reload);
  const cols: Col<Card>[] = [
    { key: 'code', title: t('rt.g.code'), get: (r) => r.code, width: 170 },
    { key: 'src', title: t('rt.g.source'), get: (r) => t(`rt.g.src.${r.source}`), width: 110 },
    { key: 'init', title: t('rt.g.initial'), get: (r) => Number(r.initial), num: true, width: 90, decimals: 2 },
    { key: 'bal', title: t('rt.g.balance'), get: (r) => Number(r.balance), num: true, width: 90, decimals: 2 },
    { key: 'cur', title: t('rt.currency'), get: (r) => r.currency, width: 60 },
    { key: 'st', title: t('rt.status'), get: (r) => t(`rt.g.st.${r.status}`), width: 90 },
    { key: 'exp', title: t('rt.g.expires'), get: (r) => (r.expires_on ? formatDate(r.expires_on) : ''), width: 100 },
    { key: 'cust', title: t('rt.customer'), get: (r) => r.customer_name ?? '' },
  ];
  return (
    <>
      <Flash m={msg} />
      <div className="mx-pay">{d.totals.map((x) => <span key={x.currency} style={{ marginInlineEnd: 16 }}>{t('rt.g.outstanding')}: <b dir="ltr">{amt(x.outstanding, decimalsOf(x.currency))} {x.currency}</b> ({x.cards})</span>)}</div>
      <section className="mx-box">
        <div className="mx-row" style={{ gap: 6, padding: 6 }}><input value={q} placeholder={t('rt.g.search')} onChange={(e) => setQ(e.currentTarget.value)} /></div>
        <div style={{ height: 260 }}><DataGrid rows={d.rows} cols={cols} rowKey={(r) => r.id} selected={sel?.id} onSelect={setSel} title={t('rt.m.gift')} /></div>
      </section>
      {sel && (
        <section className="mx-box"><h3 dir="ltr">{sel.code}</h3>
          <table className="mx-table"><tbody>{moves.map((m, i) => <tr key={i}><td dir="ltr">{formatDateTime(m.created_at)}</td><td>{t(`rt.g.mv.${m.kind}`)}</td><td dir="ltr">{amt(m.amount)}</td><td>{m.pos_number ? `POS #${m.pos_number}` : ''}</td></tr>)}</tbody></table>
          {can(me, 'pos.manage') && sel.status === 'active' && <button className="mx-btn" onClick={() => { if (window.confirm(t('rt.g.voidQ'))) void run(api(`${base}/retail/gift-cards/${sel.id}/void`, { method: 'POST', body: {} }), () => setSel(null)); }}>{t('rt.g.void')}</button>}
        </section>
      )}
      {can(me, 'pos.manage') && (
        <section className="mx-box"><h3>{t('rt.g.voucher')}</h3>
          <small style={{ padding: 6 }}>{t('rt.g.voucherHint')}</small>
          <div className="mx-row" style={{ gap: 8, padding: 6, flexWrap: 'wrap', alignItems: 'flex-end' }}>
            <label>{t('rt.amount')}<input dir="ltr" style={{ width: 90 }} value={v.amount} onChange={(e) => setV({ ...v, amount: num(e.currentTarget.value) })} /></label>
            <label>{t('rt.currency')}<input dir="ltr" style={{ width: 60 }} value={v.currency} onChange={(e) => setV({ ...v, currency: e.currentTarget.value.toUpperCase() })} /></label>
            <label>{t('rt.g.code')}<input dir="ltr" style={{ width: 150 }} value={v.code} placeholder={t('rt.g.auto')} onChange={(e) => setV({ ...v, code: e.currentTarget.value.toUpperCase() })} /></label>
            <label>{t('rt.g.expires')}<input type="date" dir="ltr" value={v.expires_on} onChange={(e) => setV({ ...v, expires_on: e.currentTarget.value })} /></label>
            <label>{t('rt.note')}<input value={v.note} onChange={(e) => setV({ ...v, note: e.currentTarget.value })} /></label>
            <button className="mx-btn main" disabled={!(Number(v.amount) > 0)} onClick={() => void run(api(`${base}/retail/gift-cards`, { method: 'POST', body: v }), () => setV({ ...v, amount: '', code: '', note: '' }))}>{t('rt.g.issue')}</button>
          </div>
        </section>
      )}
    </>
  );
}

// ======================= scales, PLU and labels =======================

/** EAN-13 / EAN-8 / any code as an SVG barcode (EAN for 8/13 digits, Code 128-B otherwise). */
export function Barcode({ code, height = 36 }: { code: string; height?: number }) {
  const bars = useMemo(() => encodeBars(code), [code]);
  const w = bars.length;
  return (
    <svg viewBox={`0 0 ${w} ${height + 10}`} width="100%" height={height + 10} preserveAspectRatio="none" aria-label={code}>
      {bars.split('').map((b, i) => (b === '1' ? <rect key={i} x={i} y={0} width={1} height={height} fill="#000" /> : null))}
      <text x={w / 2} y={height + 9} fontSize={9} textAnchor="middle" fontFamily="monospace">{code}</text>
    </svg>
  );
}
const EAN_L = ['0001101', '0011001', '0010011', '0111101', '0100011', '0110001', '0101111', '0111011', '0110111', '0001011'];
const EAN_G = ['0100111', '0110011', '0011011', '0100001', '0011101', '0111001', '0000101', '0010001', '0001001', '0010111'];
const EAN_R = ['1110010', '1100110', '1101100', '1000010', '1011100', '1001110', '1010000', '1000100', '1001000', '1110100'];
const EAN_P = ['LLLLLL', 'LLGLGG', 'LLGGLG', 'LLGGGL', 'LGLLGG', 'LGGLLG', 'LGGGLL', 'LGLGLG', 'LGLGGL', 'LGGLGL'];
const C128 = ['212222', '222122', '222221', '121223', '121322', '131222', '122213', '122312', '132212', '221213', '221312', '231212', '112232', '122132', '122231', '113222', '123122', '123221', '223211', '221132', '221231', '213212', '223112', '312131', '311222', '321122', '321221', '312212', '322112', '322211', '212123', '212321', '232121', '111323', '131123', '131321', '112313', '132113', '132311', '211313', '231113', '231311', '112133', '112331', '132131', '113123', '113321', '133121', '313121', '211331', '231131', '213113', '213311', '213131', '311123', '311321', '331121', '312113', '312311', '332111', '314111', '221411', '431111', '111224', '111422', '121124', '121421', '141122', '141221', '112214', '112412', '122114', '122411', '142112', '142211', '241211', '221114', '413111', '241112', '134111', '111242', '121142', '121241', '114212', '124112', '124211', '411212', '421112', '421211', '212141', '214121', '412121', '111143', '111341', '131141', '114113', '114311', '411113', '411311', '113141', '114131', '311141', '411131', '211412', '211214', '211232', '2331112'];
function encodeBars(code: string): string {
  if (/^\d{13}$/.test(code)) {
    const p = EAN_P[Number(code[0])];
    let s = '101';
    for (let i = 1; i <= 6; i++) s += (p[i - 1] === 'L' ? EAN_L : EAN_G)[Number(code[i])];
    s += '01010';
    for (let i = 7; i <= 12; i++) s += EAN_R[Number(code[i])];
    return `00000${s}10100000`;
  }
  if (/^\d{8}$/.test(code)) {
    let s = '101';
    for (let i = 0; i < 4; i++) s += EAN_L[Number(code[i])];
    s += '01010';
    for (let i = 4; i < 8; i++) s += EAN_R[Number(code[i])];
    return `00000${s}10100000`;
  }
  const vals = [104, ...code.split('').map((c) => Math.max(0, Math.min(94, c.charCodeAt(0) - 32)))];
  const check = vals.reduce((s, v, i) => s + v * (i || 1), 0) % 103;
  let out = '0000000000';
  for (const v of [...vals, check, 106]) {
    const w = C128[v];
    for (let i = 0; i < w.length; i++) out += (i % 2 === 0 ? '1' : '0').repeat(Number(w[i]));
  }
  return out + '0000000000';
}

interface Label { id: string; code: string; name: string; name_i18n: Record<string, string>; barcode: string; unit: string; sold_by_weight: boolean; plu: number | null; price: string; promo_price: string | null; unit_price: string | null; unit_basis: string }

function Labels({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const base = useBase();
  const { company, decimalsOf } = useCompany();
  const [sc, reloadSc] = useLoad<{ config: { prefixes: { prefix: string; mode: string; decimals: number }[] }; variant_barcode_prefix: string } | null>(base && `${base}/retail/scale`, null);
  const [pre, setPre] = useState('');
  const [vpre, setVpre] = useState('');
  useEffect(() => { if (sc) { setPre(sc.config.prefixes.map((p) => `${p.prefix}:${p.mode}:${p.decimals}`).join(', ')); setVpre(sc.variant_barcode_prefix); } }, [sc]);
  const [it, setIt] = useState<{ id: string; name: string } | null>(null);
  const [r, setR] = useState({ plu: '', sold_by_weight: false, brand: '', content_qty: '', content_unit: '' });
  const [picked, setPicked] = useState<ItemLite[]>([]);
  const [copies, setCopies] = useState('1');
  const [layout, setLayout] = useState<'shelf' | 'small'>('shelf');
  const [labels, setLabels] = useState<Label[] | null>(null);
  const [printing, setPrinting] = useState(false);
  const [msg, run, setMsg] = useRun(reloadSc);
  const manage = can(me, 'stock.manage');
  const cur = company?.base_currency ?? 'USD';
  const dec = decimalsOf(cur);
  function saveScale() {
    if (!base) return;
    const prefixes = pre.split(',').map((x) => x.split(':').map((y) => y.trim())).filter((x) => x[0]).map(([prefix, mode, d]) => ({ prefix, mode: mode || 'weight', decimals: Number(d ?? (mode === 'price' ? 2 : 3)) }));
    void run(api(`${base}/retail/scale`, { method: 'PUT', body: { prefixes, variant_barcode_prefix: vpre } }));
  }
  async function exportPLU() {
    if (!base) return;
    try { const d = await api<{ csv: string }>(`${base}/retail/scale/plu`); await saveFile('plu.csv', d.csv, 'text/csv'); } catch (e) { setMsg({ ok: false, text: errorText(t, e) }); }
  }
  async function loadLabels(): Promise<Label[]> {
    if (!base || !picked.length) return [];
    const d = await api<{ labels: Label[] }>(`${base}/retail/labels?ids=${picked.map((p) => p.id).join(',')}&currency=${cur}`);
    const n = Math.max(1, Math.min(50, Number(copies) || 1));
    return d.labels.flatMap((l) => Array.from({ length: n }, () => l));
  }
  async function print() {
    try { const ls = await loadLabels(); setLabels(ls); setPrinting(true); window.setTimeout(() => { void printPage(t('rt.lb.labels')).finally(() => setPrinting(false)); }, 300); } catch (e) { setMsg({ ok: false, text: errorText(t, e) }); }
  }
  async function printerFile(kind: 'zpl' | 'tspl') {
    try {
      const ls = await loadLabels();
      const esc = (s: string) => s.replace(/[\^~"\\]/g, ' ');
      const body = ls.map((l) => {
        const price = `${amt(l.promo_price ?? l.price, dec)} ${cur}`;
        return kind === 'zpl'
          ? `^XA^CI28^FO20,15^A0N,28,28^FD${esc(l.name).slice(0, 32)}^FS^FO20,50^A0N,40,40^FD${price}^FS${l.barcode ? `^FO20,100^BY2^BEN,60,Y,N^FD${l.barcode.slice(0, 12)}^FS` : ''}^XZ`
          : `SIZE 40 mm,25 mm\r\nGAP 2 mm,0\r\nCLS\r\nTEXT 10,10,"3",0,1,1,"${esc(l.name).slice(0, 24)}"\r\nTEXT 10,45,"4",0,1,1,"${price}"\r\n${l.barcode ? `BARCODE 10,90,"EAN13",60,1,0,2,2,"${l.barcode.slice(0, 12)}"\r\n` : ''}PRINT 1\r\n`;
      }).join(kind === 'zpl' ? '\n' : '');
      await saveFile(`labels.${kind === 'zpl' ? 'zpl' : 'prn'}`, body, 'text/plain');
    } catch (e) { setMsg({ ok: false, text: errorText(t, e) }); }
  }
  return (
    <>
      <Flash m={msg} />
      <section className="mx-box"><h3>{t('rt.lb.scale')}</h3>
        <small style={{ padding: 6 }}>{t('rt.lb.scaleHint')}</small>
        <div className="mx-row" style={{ gap: 8, padding: 6, flexWrap: 'wrap', alignItems: 'flex-end' }}>
          <label>{t('rt.lb.prefixes')}<input dir="ltr" style={{ width: 260 }} value={pre} onChange={(e) => setPre(e.currentTarget.value)} placeholder="21:weight:3, 22:price:2" /></label>
          <label>{t('rt.lb.variantPrefix')}<input dir="ltr" style={{ width: 70 }} value={vpre} onChange={(e) => setVpre(e.currentTarget.value.replace(/\D/g, ''))} /></label>
          {manage && <button className="mx-btn" onClick={saveScale}>{t('rt.save')}</button>}
          <button className="mx-btn" onClick={() => void exportPLU()}>{t('rt.lb.exportPLU')}</button>
        </div>
      </section>
      {manage && (
        <section className="mx-box"><h3>{t('rt.lb.itemRetail')}</h3>
          <ItemSearch onPick={(x) => { setIt({ id: x.id, name: x.name }); setR({ plu: '', sold_by_weight: false, brand: '', content_qty: '', content_unit: '' }); }} />
          {it && (
            <div className="mx-row" style={{ gap: 8, padding: 6, flexWrap: 'wrap', alignItems: 'flex-end' }}>
              <b>{it.name}</b>
              <label>PLU<input dir="ltr" style={{ width: 70 }} value={r.plu} onChange={(e) => setR({ ...r, plu: e.currentTarget.value.replace(/\D/g, '') })} /></label>
              <label className="mx-row" style={{ gap: 4 }}><input type="checkbox" checked={r.sold_by_weight} onChange={(e) => setR({ ...r, sold_by_weight: e.currentTarget.checked })} />{t('rt.lb.byWeight')}</label>
              <label>{t('rt.lb.brand')}<input value={r.brand} onChange={(e) => setR({ ...r, brand: e.currentTarget.value })} /></label>
              <label>{t('rt.lb.content')}<input dir="ltr" style={{ width: 70 }} value={r.content_qty} onChange={(e) => setR({ ...r, content_qty: num(e.currentTarget.value) })} /></label>
              <select value={r.content_unit} onChange={(e) => setR({ ...r, content_unit: e.currentTarget.value })}>{['', 'kg', 'g', 'L', 'ml', 'm', 'pcs'].map((u) => <option key={u} value={u}>{u || '—'}</option>)}</select>
              <button className="mx-btn main" onClick={() => void run(api(`${base}/items/${it.id}/retail`, { method: 'PUT', body: { plu: Number(r.plu) || 0, sold_by_weight: r.sold_by_weight, brand: r.brand, content_qty: r.content_qty, content_unit: r.content_unit } }))}>{t('rt.save')}</button>
            </div>
          )}
        </section>
      )}
      <section className="mx-box"><h3>{t('rt.lb.labels')}</h3>
        <ItemSearch onPick={(x) => !picked.some((p) => p.id === x.id) && setPicked([...picked, x])} />
        <div className="mx-row" style={{ gap: 4, padding: 6, flexWrap: 'wrap' }}>
          {picked.map((p) => <button key={p.id} className="mx-btn sm" onClick={() => setPicked(picked.filter((x) => x.id !== p.id))}>{i18nName(p, i18n.language)} ✕</button>)}
        </div>
        <div className="mx-row" style={{ gap: 8, padding: 6, flexWrap: 'wrap', alignItems: 'flex-end' }}>
          <label>{t('rt.lb.layout')}<select value={layout} onChange={(e) => setLayout(e.currentTarget.value as 'shelf' | 'small')}>
            <option value="shelf">{t('rt.lb.shelf')}</option><option value="small">{t('rt.lb.small')}</option></select></label>
          <label>{t('rt.lb.copies')}<input dir="ltr" style={{ width: 50 }} value={copies} onChange={(e) => setCopies(e.currentTarget.value.replace(/\D/g, ''))} /></label>
          <button className="mx-btn main" disabled={!picked.length} onClick={() => void print()}>{t('rt.lb.print')}</button>
          <button className="mx-btn" disabled={!picked.length} onClick={() => void printerFile('zpl')}>ZPL</button>
          <button className="mx-btn" disabled={!picked.length} onClick={() => void printerFile('tspl')}>TSPL</button>
        </div>
      </section>
      {printing && labels && createPortal(
        <div className="mx-printhost"><div className="print-doc" dir={i18n.dir()} style={{ display: 'grid', gridTemplateColumns: layout === 'shelf' ? 'repeat(3, 1fr)' : 'repeat(5, 1fr)', gap: 4 }}>
          {labels.map((l, i) => (
            <div key={i} style={{ border: '1px dashed #999', padding: 4, breakInside: 'avoid', fontSize: layout === 'shelf' ? 12 : 9 }}>
              <div style={{ fontWeight: 700, minHeight: layout === 'shelf' ? 30 : 20 }}>{i18nName(l, i18n.language)}</div>
              {l.promo_price ? <div><s dir="ltr">{amt(l.price, dec)}</s> <b dir="ltr" style={{ fontSize: layout === 'shelf' ? 22 : 13 }}>{amt(l.promo_price, dec)} {cur}</b></div>
                : <b dir="ltr" style={{ fontSize: layout === 'shelf' ? 22 : 13 }}>{amt(l.price, dec)} {cur}{l.sold_by_weight ? ` / ${l.unit}` : ''}</b>}
              {l.unit_price && <div dir="ltr" style={{ fontSize: 10 }}>{amt(l.unit_price, dec)} {cur} / {l.unit_basis}</div>}
              {l.barcode && <Barcode code={l.barcode} height={layout === 'shelf' ? 34 : 22} />}
            </div>
          ))}
        </div></div>, document.body)}
    </>
  );
}

// ======================= near expiry =======================

interface Exp { lot_id: string; lot_no: string; expiry: string; days_left: number; item_id: string; code: string; name: string; name_i18n: Record<string, string>; warehouse: string; quantity: string; suggested_pct: number; has_promo: boolean }

function Expiry({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const base = useBase();
  const [days, setDays] = useState('30');
  const [rows, reload] = useLoad<Exp[]>(base && `${base}/retail/expiry?days=${Number(days) || 30}`, []);
  const [pct, setPct] = useState<Record<string, string>>({});
  const [msg, run] = useRun(reload);
  const cols: Col<Exp>[] = [
    { key: 'exp', title: t('rt.e.expiry'), get: (r) => r.expiry, render: (r) => formatDate(r.expiry), width: 100 },
    { key: 'left', title: t('rt.e.daysLeft'), get: (r) => r.days_left, num: true, width: 80, cls: (r) => (r.days_left < 0 ? 'red' : r.days_left <= 3 ? 'red' : '') },
    { key: 'name', title: t('rt.name'), get: (r) => i18nName(r, i18n.language) },
    { key: 'lot', title: t('rt.e.lot'), get: (r) => r.lot_no, width: 100 },
    { key: 'wh', title: t('rt.e.warehouse'), get: (r) => r.warehouse, width: 110 },
    { key: 'qty', title: t('rt.qty'), get: (r) => Number(r.quantity), num: true, width: 70, decimals: 0 },
    { key: 'sug', title: t('rt.e.suggested'), get: (r) => (r.days_left < 0 ? t('rt.e.remove') : `−${r.suggested_pct}%`), width: 100 },
    {
      key: 'act', title: '', get: () => '', width: 170, filter: false, render: (r) => (r.has_promo ? <span>✓ {t('rt.e.marked')}</span> : r.days_left < 0 || !can(me, 'pos.manage') ? null : (
        <span className="mx-row" style={{ gap: 4 }}>
          <input dir="ltr" style={{ width: 40 }} value={pct[r.lot_id] ?? String(r.suggested_pct)} onChange={(e) => setPct({ ...pct, [r.lot_id]: num(e.currentTarget.value) })} />%
          <button className="mx-btn sm" onClick={() => void run(api(`${base}/retail/expiry/markdown`, { method: 'POST', body: { item_id: r.item_id, pct: pct[r.lot_id] ?? String(r.suggested_pct), days: Math.max(1, r.days_left + 1) } }))}>{t('rt.e.markdown')}</button>
        </span>
      )),
    },
  ];
  return (
    <>
      <Flash m={msg} />
      <div className="mx-row" style={{ gap: 6, padding: 6 }}>{t('rt.e.within')}<input dir="ltr" style={{ width: 60 }} value={days} onChange={(e) => setDays(e.currentTarget.value.replace(/\D/g, ''))} />{t('rt.e.days')}</div>
      <section className="mx-box" style={{ height: 420 }}><DataGrid rows={rows} cols={cols} rowKey={(r) => r.lot_id + r.warehouse} title={t('rt.m.expiry')} empty={t('rt.e.none')} /></section>
      <small style={{ padding: 6 }}>{t('rt.e.returnsHint')}</small>
    </>
  );
}

// ======================= receive by scanning =======================

function ScanReceive({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const base = useBase();
  const { company } = useCompany();
  const [sups] = useLoad<{ id: string; name: string; currency: string }[]>(base && `${base}/suppliers`, []);
  const [sup, setSup] = useState('');
  const [ref, setRef] = useState('');
  const [code, setCode] = useState('');
  const [lines, setLines] = useState<{ item: ItemLite; qty: string; price: string }[]>([]);
  const [msg, run, setMsg] = useRun();
  const cur = sups.find((s) => s.id === sup)?.currency ?? company?.base_currency ?? 'USD';
  async function scan() {
    const c = code.trim();
    if (!base || !c) return;
    setCode('');
    const i = lines.findIndex((l) => l.item.barcode === c || l.item.code.toLowerCase() === c.toLowerCase());
    if (i >= 0) { setLines(lines.map((l, j) => (j === i ? { ...l, qty: String(Number(l.qty) + 1) } : l))); return; }
    const found = (await api<ItemLite[]>(`${base}/items?q=${encodeURIComponent(c)}`).catch(() => [])).find((x) => x.barcode === c || x.code.toLowerCase() === c.toLowerCase());
    if (!found) { setMsg({ ok: false, text: `${t('rt.r.notFound')}: ${c}` }); return; }
    let price = '';
    if (sup) {
      const b = await api<{ price?: string }>(`${base}/supplier-prices/best?supplier_id=${sup}&item_id=${found.id}&currency=${cur}&qty=1`).catch(() => ({ price: '' }));
      price = b.price ? String(Number(b.price)) : '';
    }
    setLines((ls) => [...ls, { item: found, qty: '1', price }]);
  }
  const total = lines.reduce((s, l) => s + Number(l.qty || 0) * Number(l.price || 0), 0);
  return (
    <>
      <Flash m={msg} />
      <section className="mx-box"><h3>{t('rt.r.title')}</h3>
        <small style={{ padding: 6 }}>{t('rt.r.hint')}</small>
        <div className="mx-row" style={{ gap: 8, padding: 6, flexWrap: 'wrap', alignItems: 'flex-end' }}>
          <label>{t('rt.v.supplier')}<select value={sup} onChange={(e) => setSup(e.currentTarget.value)}><option value="">—</option>{sups.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}</select></label>
          <label>{t('rt.r.ref')}<input value={ref} onChange={(e) => setRef(e.currentTarget.value)} /></label>
          <label>{t('rt.r.scan')}<input dir="ltr" autoFocus value={code} onChange={(e) => setCode(e.currentTarget.value)} onKeyDown={(e) => { if (e.key === 'Enter') void scan(); }} /></label>
        </div>
        <table className="mx-table"><thead><tr><th>{t('rt.name')}</th><th>{t('rt.qty')}</th><th>{t('rt.r.cost')} ({cur})</th><th /></tr></thead>
          <tbody>{lines.map((l, i) => (
            <tr key={l.item.id}><td>{i18nName(l.item, i18n.language)}</td>
              <td><input dir="ltr" style={{ width: 70 }} value={l.qty} onChange={(e) => setLines(lines.map((x, j) => (j === i ? { ...x, qty: num(e.currentTarget.value) } : x)))} /></td>
              <td><input dir="ltr" style={{ width: 80 }} value={l.price} onChange={(e) => setLines(lines.map((x, j) => (j === i ? { ...x, price: num(e.currentTarget.value) } : x)))} /></td>
              <td><button className="mx-btn sm" onClick={() => setLines(lines.filter((_, j) => j !== i))}>✕</button></td></tr>
          ))}</tbody></table>
        <div className="mx-row" style={{ gap: 8, padding: 6 }}>
          <b dir="ltr">{amt(total)} {cur}</b>
          {can(me, 'purchasing.create') && <button className="mx-btn main" disabled={!sup || !lines.length || lines.some((l) => !(Number(l.qty) > 0) || l.price === '')}
            onClick={() => void run(api(`${base}/bills`, { method: 'POST', body: { supplier_id: sup, supplier_ref: ref, bill_date: today(), currency: cur, lines: lines.map((l) => ({ item_id: l.item.id, quantity: l.qty, unit_price: l.price })) } }), () => { setLines([]); setRef(''); })}>{t('rt.r.makeBill')}</button>}
        </div>
      </section>
    </>
  );
}

// ======================= retail report =======================

function RetailReport() {
  const { t, i18n } = useTranslation();
  const base = useBase();
  const { company, decimalsOf } = useCompany();
  const [from, setFrom] = useState(today().slice(0, 8) + '01');
  const [to, setTo] = useState(today());
  const [cur, setCur] = useState(company?.base_currency ?? 'USD');
  type Row = { id: string; code?: string; name: string; name_i18n?: Record<string, string>; qty: string; revenue: string; cost?: string; margin?: string; margin_pct?: string | null; documents?: number };
  const [d] = useLoad<{ by_item: Row[]; by_category: Row[]; by_hour: { hour: number; orders: number; total: string }[]; promotions: { id: string; name: string; uses: number; discount: string }[] } | null>(
    base && `${base}/reports/retail?from=${from}&to=${to}&currency=${cur}`, null);
  const dec = decimalsOf(cur);
  const maxH = Math.max(1, ...(d?.by_hour ?? []).map((h) => Number(h.total)));
  const itemCols: Col<Row>[] = [
    { key: 'name', title: t('rt.name'), get: (r) => i18nName(r, i18n.language) },
    { key: 'qty', title: t('rt.qty'), get: (r) => Number(r.qty), num: true, width: 80, decimals: 2 },
    { key: 'rev', title: t('rt.rep.revenue'), get: (r) => Number(r.revenue), num: true, width: 100, decimals: dec, total: 'sum' },
    { key: 'cost', title: t('rt.rep.cost'), get: (r) => Number(r.cost), num: true, width: 100, decimals: dec, total: 'sum' },
    { key: 'mg', title: t('rt.rep.margin'), get: (r) => Number(r.margin), num: true, width: 100, decimals: dec, total: 'sum', cls: (r) => (Number(r.margin) < 0 ? 'red' : '') },
    { key: 'pct', title: '%', get: (r) => (r.margin_pct == null ? '' : Number(r.margin_pct)), num: true, width: 60, decimals: 1 },
  ];
  return (
    <>
      <div className="mx-row" style={{ gap: 8, padding: 6, flexWrap: 'wrap', alignItems: 'flex-end' }}>
        <label>{t('rt.from')}<input type="date" dir="ltr" value={from} onChange={(e) => setFrom(e.currentTarget.value)} /></label>
        <label>{t('rt.to')}<input type="date" dir="ltr" value={to} onChange={(e) => setTo(e.currentTarget.value)} /></label>
        <label>{t('rt.currency')}<input dir="ltr" style={{ width: 60 }} value={cur} onChange={(e) => setCur(e.currentTarget.value.toUpperCase())} /></label>
      </div>
      {d && <>
        <section className="mx-box"><h3>{t('rt.rep.byCategory')}</h3>
          <table className="mx-table"><thead><tr><th>{t('rt.rep.category')}</th><th>{t('rt.qty')}</th><th>{t('rt.rep.revenue')}</th></tr></thead>
            <tbody>{d.by_category.map((c) => <tr key={c.id}><td>{c.name ? i18nName(c, i18n.language) : t('rt.rep.noCategory')}</td><td dir="ltr">{amt(c.qty)}</td><td dir="ltr">{amt(c.revenue, dec)}</td></tr>)}</tbody></table>
        </section>
        <section className="mx-box"><h3>{t('rt.rep.byHour')}</h3>
          <div className="mx-col" style={{ gap: 2, padding: 6 }}>
            {d.by_hour.map((h) => (
              <div key={h.hour} className="mx-row" style={{ gap: 6, alignItems: 'center' }}>
                <span dir="ltr" style={{ width: 48 }}>{String(h.hour).padStart(2, '0')}:00</span>
                <div style={{ height: 14, width: `${(Number(h.total) / maxH) * 70}%`, background: 'var(--mx-accent, #2f6fd6)', minWidth: 2 }} />
                <span dir="ltr">{amt(h.total, dec)} · {h.orders}</span>
              </div>
            ))}
          </div>
        </section>
        <section className="mx-box" style={{ height: 360 }}><DataGrid rows={d.by_item} cols={itemCols} rowKey={(r) => r.id} title={t('rt.rep.byItem')} footer /></section>
        {d.promotions.length > 0 && <section className="mx-box"><h3>{t('rt.m.promos')}</h3>
          <table className="mx-table"><tbody>{d.promotions.map((p) => <tr key={p.id}><td>{p.name}</td><td dir="ltr">{p.uses}</td><td dir="ltr">−{amt(p.discount, dec)}</td></tr>)}</tbody></table></section>}
        <small style={{ padding: 6 }}>{t('rt.rep.note')}</small>
      </>}
    </>
  );
}

// ======================= packs =======================

function Packs({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [d, reload] = useLoad<{ current: string; business_type: string; packs: { key: string; apps: string[] }[] } | null>(base && `${base}/retail/packs`, null);
  const [demo, setDemo] = useState(true);
  const [msg, run] = useRun(reload);
  return (
    <>
      <Flash m={msg} />
      <small style={{ padding: 6 }}>{t('rt.pk.hint')}</small>
      <div className="mx-row" style={{ gap: 12, padding: 6, flexWrap: 'wrap' }}>
        {(d?.packs ?? []).map((p) => (
          <section key={p.key} className="mx-box" style={{ minWidth: 260, flex: 1 }}>
            <h3>{t(`rt.pk.${p.key}`)} {d?.current === p.key && '✓'}</h3>
            <div style={{ padding: 6 }}>{t(`rt.pk.${p.key}Desc`)}</div>
            {can(me, 'settings.manage') && <div className="mx-row" style={{ gap: 6, padding: 6 }}>
              <button className="mx-btn main" onClick={() => void run(api(`${base}/retail/packs/${p.key}`, { method: 'POST', body: { demo } }), () => window.setTimeout(() => window.location.reload(), 800))}>{t('rt.pk.apply')}</button>
            </div>}
          </section>
        ))}
      </div>
      <label className="mx-row" style={{ gap: 6, padding: 6 }}><input type="checkbox" checked={demo} onChange={(e) => setDemo(e.currentTarget.checked)} />{t('rt.pk.demo')}</label>
    </>
  );
}
