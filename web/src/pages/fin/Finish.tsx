import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { api, can, API_BASE, type Account, type Me } from '../../api';
import { useCompany } from '../../company';
import DataGrid, { type Col } from '../../components/DataGrid';
import Attachments from '../../components/Attachments';
import { errorText, formatAmount, formatDate, today } from '../../util';

// Finishing-stage screens: subscription changes with proration, supplier price lists and 3-way matching, expense
// claims, custom fields, manufacturing, HR extras (rotations / roster, leave accruals, clock rules, fingerprint
// devices), online payments, e-invoicing; plus the self-service claims box, the custom-field box and the payment
// simulator page.

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
    try { const x = await p; setMsg({ ok: true, text: t('fin.saved') }); after?.(x as never); reload?.(); } catch (e) { setMsg({ ok: false, text: errorText(t, e) }); }
  };
  return [msg, run, setMsg];
}
function Tabs({ tabs, value, onChange }: { tabs: [string, string][]; value: string; onChange: (v: string) => void }) {
  return (
    <div className="mx-row" role="tablist" style={{ gap: 4, padding: 6, flexWrap: 'wrap' }}>
      {tabs.map(([k, label]) => <button key={k} role="tab" aria-selected={value === k} className={`mx-btn sm ${value === k ? 'main' : ''}`} onClick={() => onChange(k)}>{label}</button>)}
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
const num = (s: string) => s.replace(/[^\d.\-]/g, '');
const amt = (v: string | number | null | undefined, d = 2) => formatAmount(String(v ?? 0), d);

/** Downscale a camera picture to a small JPEG (≤ 640 px) and return it as base64 (no data: prefix). */
export async function photoToBase64(file: File): Promise<string> {
  const url = URL.createObjectURL(file);
  try {
    const img = await new Promise<HTMLImageElement>((res, rej) => { const i = new Image(); i.onload = () => res(i); i.onerror = rej; i.src = url; });
    const scale = Math.min(1, 640 / Math.max(img.width, img.height));
    const c = document.createElement('canvas');
    c.width = Math.round(img.width * scale); c.height = Math.round(img.height * scale);
    c.getContext('2d')?.drawImage(img, 0, 0, c.width, c.height);
    return c.toDataURL('image/jpeg', 0.8).split(',')[1] ?? '';
  } finally { URL.revokeObjectURL(url); }
}
function fileToBase64(file: File): Promise<string> {
  return new Promise((res, rej) => { const r = new FileReader(); r.onload = () => res(String(r.result).split(',')[1] ?? ''); r.onerror = rej; r.readAsDataURL(file); });
}

// ======================= subscriptions: plan change / stop with proration =======================

interface SubRow { id: string; plan_id: string; plan_name?: string; status: string; start_date: string; end_date?: string | null; meter_no?: string }
interface Change { id: string; change_date: string; kind: string; old_plan: string | null; new_plan: string | null; days_left: number; days_in_month: number; amount: string; currency: string; invoice_number: number | null; credit_note_number: number | null }

export function SubsChange({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [pr, loadPr] = useLoad<{ enabled: boolean }>(base && `${base}/billing/proration`, { enabled: false });
  const [q, setQ] = useState('');
  const [custs, setCusts] = useState<{ id: string; code: string; name: string }[]>([]);
  const [cust, setCust] = useState<{ id: string; name: string } | null>(null);
  const [subs, setSubs] = useState<SubRow[]>([]);
  const [sub, setSub] = useState<SubRow | null>(null);
  const [plans] = useLoad<{ id: string; name: string; currency: string; base_fee: string; is_active: boolean }[]>(base && `${base}/plans`, []);
  const [changes, loadChanges] = useLoad<Change[]>(base && sub ? `${base}/subscriptions/${sub.id}/changes` : null, []);
  const [form, setForm] = useState({ plan_id: '', date: today(), stop: today() });
  const [msg, run] = useRun(() => { loadChanges(); if (cust) pick(cust); });
  const edit = can(me, 'customers.edit');
  function search() {
    if (base) api<{ rows?: { id: string; code: string; name: string }[] } | { id: string; code: string; name: string }[]>(`${base}/customers?q=${encodeURIComponent(q)}&limit=30`)
      .then((r) => setCusts(Array.isArray(r) ? r : r.rows ?? [])).catch(() => setCusts([]));
  }
  function pick(c: { id: string; name: string }) {
    setCust(c);
    if (base) api<{ subscriptions: SubRow[] }>(`${base}/customers/${c.id}`).then((d) => { setSubs(d.subscriptions ?? []); setSub((s) => (d.subscriptions ?? []).find((x) => x.id === s?.id) ?? null); }).catch(() => setSubs([]));
  }
  const planName = (id: string) => plans.find((p) => p.id === id)?.name ?? '';
  return (
    <Screen title={t('fin.subs.title')}>
      <section className="mx-box"><h3>{t('fin.subs.proration')}</h3>
        <div className="mx-pay">
          <label className="mx-row" style={{ gap: 6 }}><input type="checkbox" checked={pr.enabled} disabled={!can(me, 'billing.manage')}
            onChange={(e) => void run(api(`${base}/billing/proration`, { method: 'PUT', body: { enabled: e.currentTarget.checked } }), () => loadPr())} />{t('fin.subs.prorateOn')}</label>
          <small>{t('fin.subs.prorateHint')}</small>
        </div>
      </section>
      <section className="mx-box"><h3>{t('fin.subs.find')}</h3>
        <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap' }}>
          <input value={q} placeholder={t('fin.subs.searchPh')} onChange={(e) => setQ(e.currentTarget.value)} onKeyDown={(e) => { if (e.key === 'Enter') search(); }} />
          <button className="mx-btn" onClick={search}>{t('fin.search')}</button>
        </div>
        <div className="mx-pay">{custs.map((c) => <button key={c.id} className={`mx-btn sm ${cust?.id === c.id ? 'main' : ''}`} onClick={() => pick(c)}>{c.code} · {c.name}</button>)}</div>
      </section>
      {cust && (
        <section className="mx-box"><h3>{cust.name}</h3>
          <div className="mx-pay">
            {subs.map((s) => (
              <label key={s.id} className="mx-row" style={{ gap: 6 }}>
                <input type="radio" checked={sub?.id === s.id} onChange={() => setSub(s)} />
                {s.plan_name ?? planName(s.plan_id)} · {formatDate(s.start_date)}{s.end_date ? ` → ${formatDate(s.end_date)}` : ''} · {t(`fin.subs.st.${s.status}`, { defaultValue: s.status })}
              </label>
            ))}
            {sub && edit && sub.status !== 'ended' && (
              <>
                <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
                  <label>{t('fin.subs.newPlan')}<select value={form.plan_id} onChange={(e) => setForm({ ...form, plan_id: e.currentTarget.value })}>
                    <option value="">{t('fin.choose')}</option>
                    {plans.filter((p) => p.is_active && p.id !== sub.plan_id).map((p) => <option key={p.id} value={p.id}>{p.name} · {p.base_fee} {p.currency}</option>)}</select></label>
                  <label>{t('fin.date')}<input type="date" dir="ltr" value={form.date} onChange={(e) => setForm({ ...form, date: e.currentTarget.value })} /></label>
                  <button className="mx-btn" disabled={!form.plan_id} onClick={() => void run(api(`${base}/subscriptions/${sub.id}/change-plan`, { method: 'POST', body: { plan_id: form.plan_id, date: form.date } }))}>{t('fin.subs.change')}</button>
                </div>
                <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
                  <label>{t('fin.subs.stopOn')}<input type="date" dir="ltr" value={form.stop} onChange={(e) => setForm({ ...form, stop: e.currentTarget.value })} /></label>
                  <button className="mx-btn" onClick={() => { if (window.confirm(t('fin.subs.stopQ'))) void run(api(`${base}/subscriptions/${sub.id}/stop`, { method: 'POST', body: { date: form.stop } })); }}>{t('fin.subs.stop')}</button>
                </div>
              </>
            )}
            <Flash m={msg} />
            {sub && changes.length > 0 && (
              <table className="mx-table"><thead><tr><th>{t('fin.date')}</th><th>{t('fin.subs.change')}</th><th className="num">{t('fin.days')}</th><th className="num">{t('fin.amount')}</th><th>{t('fin.doc')}</th></tr></thead>
                <tbody>{changes.map((c) => (
                  <tr key={c.id}><td>{formatDate(c.change_date)}</td><td>{c.kind === 'stop' ? t('fin.subs.stop') : `${c.old_plan ?? ''} → ${c.new_plan ?? ''}`}</td>
                    <td className="num">{c.days_left}/{c.days_in_month}</td><td className="num" dir="ltr">{amt(c.amount)} {c.currency}</td>
                    <td>{c.invoice_number ? `${t('fin.invoice')} #${c.invoice_number}` : c.credit_note_number ? `${t('fin.creditNote')} #${c.credit_note_number}` : '—'}</td></tr>))}</tbody></table>
            )}
          </div>
        </section>
      )}
    </Screen>
  );
}

// ======================= supplier price lists and 3-way matching =======================

interface SP { id: string; supplier_id: string; supplier_name: string; item_id: string; item_code: string; item_name: string; unit: string; currency: string; price: string; min_qty: string; supplier_code: string; lead_days: number; valid_from: string | null; valid_to: string | null }

export function SupplierPrices({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const { company, currencies } = useCompany();
  const [rows, reload] = useLoad<SP[]>(base && `${base}/supplier-prices`, []);
  const [sups] = useLoad<{ id: string; code: string; name: string; currency: string | null }[]>(base && `${base}/suppliers`, []);
  const [items] = useLoad<{ id: string; code: string; name: string; kind: string }[]>(base && `${base}/items`, []);
  const [match, loadMatch] = useLoad<{ mode: string; tolerance: string }>(base && `${base}/purchasing/matching`, { mode: 'warn', tolerance: '2' });
  const blank = { id: '', supplier_id: '', item_id: '', currency: company?.base_currency ?? 'USD', price: '', min_qty: '0', supplier_code: '', lead_days: 0, valid_from: '', valid_to: '' };
  const [f, setF] = useState(blank);
  const [msg, run] = useRun(reload);
  const manage = can(me, 'purchasing.manage');
  const cols: Col<SP>[] = [
    { key: 'i', title: t('fin.item'), get: (r) => `${r.item_code} · ${r.item_name}`, width: 200 },
    { key: 's', title: t('fin.supplier'), get: (r) => r.supplier_name, width: 160 },
    { key: 'c', title: t('fin.supplierCode'), get: (r) => r.supplier_code, width: 100 },
    { key: 'q', title: t('fin.sp.minQty'), get: (r) => Number(r.min_qty), num: true, decimals: 0, width: 80 },
    { key: 'p', title: t('fin.price'), get: (r) => Number(r.price), render: (r) => `${r.price} ${r.currency}`, num: true, width: 110 },
    { key: 'l', title: t('fin.sp.lead'), get: (r) => r.lead_days, num: true, decimals: 0, width: 70 },
    { key: 'v', title: t('fin.sp.valid'), get: (r) => `${r.valid_from ?? ''} ${r.valid_to ?? ''}`, render: (r) => `${r.valid_from ? formatDate(r.valid_from) : '…'} → ${r.valid_to ? formatDate(r.valid_to) : '…'}`, width: 160 },
  ];
  return (
    <Screen title={t('fin.sp.title')}>
      <section className="mx-box"><h3>{t('fin.match.settings')}</h3>
        <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap' }}>
          <label>{t('fin.match.mode')}<select value={match.mode} disabled={!manage} onChange={(e) => void run(api(`${base}/purchasing/matching`, { method: 'PUT', body: { mode: e.currentTarget.value, tolerance: match.tolerance } }), () => loadMatch())}>
            {['off', 'warn', 'block'].map((m) => <option key={m} value={m}>{t(`fin.match.m.${m}`)}</option>)}</select></label>
          <label>{t('fin.match.tolerance')}<input dir="ltr" style={{ width: 60 }} defaultValue={match.tolerance} key={match.tolerance} disabled={!manage}
            onBlur={(e) => void run(api(`${base}/purchasing/matching`, { method: 'PUT', body: { mode: match.mode, tolerance: num(e.currentTarget.value) || '0' } }), () => loadMatch())} /> %</label>
          <small>{t('fin.match.hint')}</small>
        </div>
      </section>
      <section className="mx-box"><h3>{t('fin.sp.list')}</h3>
        <DataGrid rows={rows} cols={cols} rowKey={(r) => r.id} title={t('fin.sp.title')} selected={f.id || null}
          onSelect={(r) => setF({ id: r.id, supplier_id: r.supplier_id, item_id: r.item_id, currency: r.currency, price: r.price, min_qty: r.min_qty, supplier_code: r.supplier_code, lead_days: r.lead_days, valid_from: r.valid_from ?? '', valid_to: r.valid_to ?? '' })} />
      </section>
      {manage && (
        <section className="mx-box"><h3>{f.id ? t('fin.edit') : t('fin.add')}</h3>
          <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap' }}>
            <label>{t('fin.supplier')}<select value={f.supplier_id} onChange={(e) => { const s = sups.find((x) => x.id === e.currentTarget.value); setF({ ...f, supplier_id: e.currentTarget.value, currency: s?.currency || f.currency }); }}>
              <option value="">{t('fin.choose')}</option>{sups.map((s) => <option key={s.id} value={s.id}>{s.code} · {s.name}</option>)}</select></label>
            <label>{t('fin.item')}<select value={f.item_id} onChange={(e) => setF({ ...f, item_id: e.currentTarget.value })}>
              <option value="">{t('fin.choose')}</option>{items.map((s) => <option key={s.id} value={s.id}>{s.code} · {s.name}</option>)}</select></label>
            <label>{t('fin.currency')}<select value={f.currency} onChange={(e) => setF({ ...f, currency: e.currentTarget.value })}>{currencies.map((c) => <option key={c.code}>{c.code}</option>)}</select></label>
            <label>{t('fin.price')}<input dir="ltr" style={{ width: 90 }} value={f.price} onChange={(e) => setF({ ...f, price: num(e.currentTarget.value) })} /></label>
            <label>{t('fin.sp.minQty')}<input dir="ltr" style={{ width: 70 }} value={f.min_qty} onChange={(e) => setF({ ...f, min_qty: num(e.currentTarget.value) })} /></label>
            <label>{t('fin.supplierCode')}<input style={{ width: 100 }} value={f.supplier_code} onChange={(e) => setF({ ...f, supplier_code: e.currentTarget.value })} /></label>
            <label>{t('fin.sp.lead')}<input dir="ltr" style={{ width: 50 }} value={f.lead_days} onChange={(e) => setF({ ...f, lead_days: Number(num(e.currentTarget.value) || 0) })} /></label>
            <label>{t('fin.sp.from')}<input type="date" dir="ltr" value={f.valid_from} onChange={(e) => setF({ ...f, valid_from: e.currentTarget.value })} /></label>
            <label>{t('fin.sp.to')}<input type="date" dir="ltr" value={f.valid_to} onChange={(e) => setF({ ...f, valid_to: e.currentTarget.value })} /></label>
          </div>
          <div className="mx-row" style={{ gap: 6, padding: 6 }}>
            <button className="mx-btn" disabled={!f.supplier_id || !f.item_id || !f.price} onClick={() => {
              const { id, ...body } = f;
              void run(api(id ? `${base}/supplier-prices/${id}` : `${base}/supplier-prices`, { method: id ? 'PUT' : 'POST', body }), () => setF(blank));
            }}>{t('fin.save')}</button>
            {f.id && <button className="mx-btn sm" onClick={() => void run(api(`${base}/supplier-prices/${f.id}`, { method: 'DELETE' }), () => setF(blank))}>{t('fin.delete')}</button>}
            {f.id && <button className="mx-btn sm" onClick={() => setF(blank)}>{t('fin.cancel')}</button>}
          </div>
          <Flash m={msg} />
        </section>
      )}
    </Screen>
  );
}

// ======================= expense claims =======================

interface Claim { id: string; number: number; employee_id: string; employee_name: string; claim_date: string; category: string; description: string; currency: string; amount: string; status: string; pay_via: string; account: string | null; payroll_period: string | null; attachments: number; decision_note: string }
const CLAIM_CATS = ['fuel', 'transport', 'meals', 'lodging', 'phone', 'supplies', 'repairs', 'other'];

export function SelfClaims({ base, currency }: { base: string; currency: string }) {
  const { t } = useTranslation();
  const { currencies } = useCompany();
  const [list, reload] = useLoad<Claim[]>(`${base}/self/claims`, []);
  const blank = { category: 'fuel', description: '', currency, amount: '', pay_via: 'cash', filename: '', data: '' };
  const [f, setF] = useState(blank);
  const [msg, run] = useRun(reload);
  return (
    <section className="mx-box"><h3>{t('fin.claims.mine')}</h3>
      <div className="mx-pay">
        <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
          <label>{t('fin.claims.category')}<select value={f.category} onChange={(e) => setF({ ...f, category: e.currentTarget.value })}>{CLAIM_CATS.map((c) => <option key={c} value={c}>{t(`fin.claims.c.${c}`)}</option>)}</select></label>
          <label>{t('fin.amount')}<input dir="ltr" inputMode="decimal" style={{ width: 90 }} value={f.amount} onChange={(e) => setF({ ...f, amount: num(e.currentTarget.value) })} /></label>
          <label>{t('fin.currency')}<select value={f.currency} onChange={(e) => setF({ ...f, currency: e.currentTarget.value })}>{currencies.map((c) => <option key={c.code}>{c.code}</option>)}</select></label>
        </div>
        <label>{t('fin.description')}<input value={f.description} onChange={(e) => setF({ ...f, description: e.currentTarget.value })} /></label>
        <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
          <label>{t('fin.claims.receipt')}<input type="file" accept="image/*,application/pdf" capture="environment" onChange={(e) => {
            const file = e.currentTarget.files?.[0];
            if (file) void (file.type.startsWith('image/') ? photoToBase64(file) : fileToBase64(file)).then((data) => setF((x) => ({ ...x, data, filename: file.name })));
          }} /></label>
          <label>{t('fin.claims.payVia')}<select value={f.pay_via} onChange={(e) => setF({ ...f, pay_via: e.currentTarget.value })}>
            <option value="cash">{t('fin.claims.v.cash')}</option><option value="payroll">{t('fin.claims.v.payroll')}</option></select></label>
        </div>
        <button className="mx-btn main" disabled={!f.amount || !f.description.trim()} onClick={() => void run(api(`${base}/self/claims`, { method: 'POST', body: f }), () => setF(blank))}>{t('fin.claims.submit')}</button>
        <Flash m={msg} />
        {list.map((c) => (
          <div key={c.id} className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
            <b>#{c.number}</b> {formatDate(c.claim_date)} · {t(`fin.claims.c.${c.category}`)} · {c.description} · <span dir="ltr">{amt(c.amount)} {c.currency}</span> · <b>{t(`fin.claims.st.${c.status}`)}</b>
            {c.status === 'pending' && <button className="mx-btn sm" onClick={() => void run(api(`${base}/self/claims/${c.id}/cancel`, { method: 'POST', body: {} }))}>{t('fin.cancel')}</button>}
          </div>
        ))}
      </div>
    </section>
  );
}

export function Claims({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const { decimalsOf } = useCompany();
  const [status, setStatus] = useState('pending');
  const [rows, reload] = useLoad<Claim[]>(base && `${base}/hr/claims?status=${status}`, []);
  const [accounts] = useLoad<Account[]>(base && `${base}/accounts`, []);
  const [methods] = useLoad<{ id: string; name: string; currency: string; is_active: boolean }[]>(base && `${base}/payment-methods`, []);
  const [sel, setSel] = useState<Claim | null>(null);
  const [d, setD] = useState({ account_id: '', pay_via: 'cash', note: '', method_id: '' });
  const [msg, run] = useRun(reload);
  const expAcc = useMemo(() => accounts.filter((a) => a.is_postable && a.is_active && a.type === 'expense'), [accounts]);
  const cols: Col<Claim>[] = [
    { key: 'n', title: '#', get: (r) => r.number, num: true, decimals: 0, width: 60 },
    { key: 'd', title: t('fin.date'), get: (r) => r.claim_date, render: (r) => formatDate(r.claim_date), width: 90 },
    { key: 'e', title: t('fin.employee'), get: (r) => r.employee_name, width: 150 },
    { key: 'c', title: t('fin.claims.category'), get: (r) => t(`fin.claims.c.${r.category}`), width: 100 },
    { key: 'x', title: t('fin.description'), get: (r) => r.description, width: 200 },
    { key: 'a', title: t('fin.amount'), get: (r) => Number(r.amount), render: (r) => `${amt(r.amount, decimalsOf(r.currency))} ${r.currency}`, num: true, width: 110 },
    { key: 's', title: t('fin.status'), get: (r) => t(`fin.claims.st.${r.status}`), width: 90 },
    { key: 'p', title: t('fin.claims.payVia'), get: (r) => `${t(`fin.claims.v.${r.pay_via}`)}${r.payroll_period ? ` ${r.payroll_period}` : ''}`, width: 110 },
    { key: 'f', title: t('fin.files'), get: (r) => r.attachments, num: true, decimals: 0, width: 50 },
  ];
  return (
    <Screen title={t('fin.claims.title')}>
      <div className="mx-row" style={{ gap: 6 }}>
        <select value={status} onChange={(e) => { setStatus(e.currentTarget.value); setSel(null); }} aria-label={t('fin.status')}>
          <option value="">{t('fin.all')}</option>{['pending', 'approved', 'paid', 'rejected', 'cancelled'].map((s) => <option key={s} value={s}>{t(`fin.claims.st.${s}`)}</option>)}</select>
      </div>
      <DataGrid rows={rows} cols={cols} rowKey={(r) => r.id} title={t('fin.claims.title')} selected={sel?.id ?? null} onSelect={(r) => { setSel(r); setD({ account_id: '', pay_via: r.pay_via, note: '', method_id: '' }); }} />
      {sel && (
        <section className="mx-box"><h3>#{sel.number} · {sel.employee_name}</h3>
          <div className="mx-pay">
            <div>{sel.description} · <b dir="ltr">{amt(sel.amount)} {sel.currency}</b> · {t(`fin.claims.st.${sel.status}`)}{sel.account ? ` · ${sel.account}` : ''}</div>
            {sel.status === 'pending' && (
              <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
                <label>{t('fin.account')}<select value={d.account_id} onChange={(e) => setD({ ...d, account_id: e.currentTarget.value })}>
                  <option value="">{t('fin.claims.defaultAcc')}</option>{expAcc.map((a) => <option key={a.id} value={a.id}>{a.code} · {a.name}</option>)}</select></label>
                <label>{t('fin.claims.payVia')}<select value={d.pay_via} onChange={(e) => setD({ ...d, pay_via: e.currentTarget.value })}>
                  <option value="cash">{t('fin.claims.v.cash')}</option><option value="payroll">{t('fin.claims.v.payroll')}</option></select></label>
                <input placeholder={t('fin.note')} value={d.note} onChange={(e) => setD({ ...d, note: e.currentTarget.value })} />
                <button className="mx-btn main" onClick={() => void run(api(`${base}/hr/claims/${sel.id}/decide`, { method: 'POST', body: { decision: 'approve', account_id: d.account_id, pay_via: d.pay_via, note: d.note } }), () => setSel(null))}>{t('fin.approve')}</button>
                <button className="mx-btn" onClick={() => void run(api(`${base}/hr/claims/${sel.id}/decide`, { method: 'POST', body: { decision: 'reject', note: d.note } }), () => setSel(null))}>{t('fin.reject')}</button>
              </div>
            )}
            {sel.status === 'approved' && sel.pay_via === 'cash' && can(me, 'payroll.edit') && (
              <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
                <label>{t('fin.claims.payFrom')}<select value={d.method_id} onChange={(e) => setD({ ...d, method_id: e.currentTarget.value })}>
                  <option value="">{t('fin.choose')}</option>{methods.filter((m) => m.is_active && m.currency === sel.currency).map((m) => <option key={m.id} value={m.id}>{m.name} ({m.currency})</option>)}</select></label>
                <button className="mx-btn main" disabled={!d.method_id} onClick={() => void run(api(`${base}/hr/claims/${sel.id}/pay`, { method: 'POST', body: { method_id: d.method_id } }), () => setSel(null))}>{t('fin.claims.reimburse')}</button>
              </div>
            )}
            {sel.status === 'approved' && sel.pay_via === 'payroll' && <small>{t('fin.claims.withSalary')}</small>}
            <Attachments type="expense_claim" id={sel.id} readOnly={!can(me, 'payroll.create')} />
            <Flash m={msg} />
          </div>
        </section>
      )}
      {!sel && <Flash m={msg} />}
    </Screen>
  );
}

// ======================= custom fields =======================

interface CF { id: string; entity: string; key: string; label: string; label_i18n: Record<string, string>; type: string; options: string[]; required: boolean; show_in_list: boolean; position: number; is_active: boolean }
const ENTITIES = ['customers', 'suppliers', 'items', 'invoices', 'employees'];
const cfLabel = (f: CF, lang: string) => f.label_i18n?.[lang] || f.label;

/** The custom fields of one record (inside its detail screen). */
export function CustomFieldsBox({ entity, id, readOnly }: { entity: string; id: string; readOnly?: boolean }) {
  const { t, i18n } = useTranslation();
  const base = useBase();
  const [data, reload] = useLoad<{ fields: CF[]; values: Record<string, unknown> | null }>(base && id ? `${base}/custom-values/${entity}/${id}` : null, { fields: [], values: {} });
  const [vals, setVals] = useState<Record<string, unknown>>({});
  const [msg, run] = useRun(reload);
  useEffect(() => { setVals(data.values ?? {}); }, [data]);
  if (!data.fields.length) return null;
  return (
    <div className="mx-note no-print">
      <small><b>{t('fin.cf.box')}</b></small>
      <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
        {data.fields.map((f) => {
          const v = vals[f.key];
          const set = (x: unknown) => setVals({ ...vals, [f.key]: x });
          const label = `${cfLabel(f, i18n.language)}${f.required ? ' *' : ''}`;
          if (f.type === 'bool') return <label key={f.key} className="mx-row" style={{ gap: 4 }}><input type="checkbox" disabled={readOnly} checked={v === true} onChange={(e) => set(e.currentTarget.checked)} />{label}</label>;
          if (f.type === 'choice') return <label key={f.key}>{label}<select disabled={readOnly} value={String(v ?? '')} onChange={(e) => set(e.currentTarget.value)}><option value="">—</option>{f.options.map((o) => <option key={o}>{o}</option>)}</select></label>;
          return <label key={f.key}>{label}<input disabled={readOnly} type={f.type === 'date' ? 'date' : 'text'} dir={f.type === 'text' ? undefined : 'ltr'} value={String(v ?? '')}
            onChange={(e) => set(f.type === 'number' ? num(e.currentTarget.value) : e.currentTarget.value)} /></label>;
        })}
      </div>
      {!readOnly && <button className="mx-btn sm" onClick={() => void run(api(`${base}/custom-values/${entity}/${id}`, { method: 'PUT', body: { values: vals } }))}>{t('fin.save')}</button>}
      <Flash m={msg} />
    </div>
  );
}

export function CustomFields({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const base = useBase();
  const [entity, setEntity] = useState('customers');
  const [defs, reloadDefs] = useLoad<CF[]>(base && `${base}/custom-fields?entity=${entity}`, []);
  const [rep, reloadRep] = useLoad<{ fields: CF[]; rows: { id: string; code: string; name: string; custom: Record<string, unknown> }[] }>(base && `${base}/custom-fields/report?entity=${entity}`, { fields: [], rows: [] });
  const blank = { id: '', key: '', label: '', ar: '', fr: '', type: 'text', options: '', required: false, show_in_list: true, position: 0, is_active: true };
  const [f, setF] = useState(blank);
  const [msg, run] = useRun(() => { reloadDefs(); reloadRep(); });
  const manage = can(me, 'settings.manage');
  type RR = { id: string; code: string; name: string; custom: Record<string, unknown> };
  const cols: Col<RR>[] = [
    { key: 'code', title: t('fin.code'), get: (r) => r.code, width: 90 },
    { key: 'name', title: t('fin.name'), get: (r) => r.name, width: 180 },
    ...rep.fields.filter((x) => x.show_in_list).map((x): Col<RR> => ({ key: `cf_${x.key}`, title: cfLabel(x, i18n.language), width: 130,
      get: (r) => { const v = r.custom?.[x.key]; return v === true ? t('fin.yes') : v === false ? t('fin.no') : v == null ? '' : String(v); }, num: x.type === 'number' })),
  ];
  return (
    <Screen title={t('fin.cf.title')} tabs={<Tabs tabs={ENTITIES.map((e) => [e, t(`fin.cf.e.${e}`)])} value={entity} onChange={(e) => { setEntity(e); setF(blank); }} />}>
      <section className="mx-box"><h3>{t('fin.cf.defs')}</h3>
        <div className="mx-pay">
          {defs.map((x) => (
            <button key={x.id} className="mx-btn sm" style={{ textAlign: 'start' }} onClick={() => setF({ id: x.id, key: x.key, label: x.label, ar: x.label_i18n?.ar ?? '', fr: x.label_i18n?.fr ?? '', type: x.type, options: x.options.join(', '),
              required: x.required, show_in_list: x.show_in_list, position: x.position, is_active: x.is_active })}>
              {cfLabel(x, i18n.language)} <small>({x.key} · {t(`fin.cf.t.${x.type}`)}{x.required ? ' · *' : ''}{x.is_active ? '' : ` · ${t('fin.inactive')}`})</small></button>
          ))}
          {manage && (
            <>
              <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
                <label>{t('fin.cf.key')}<input dir="ltr" disabled={!!f.id} style={{ width: 120 }} value={f.key} onChange={(e) => setF({ ...f, key: e.currentTarget.value.toLowerCase().replace(/[^a-z0-9_]/g, '_') })} /></label>
                <label>{t('fin.cf.label')}<input value={f.label} onChange={(e) => setF({ ...f, label: e.currentTarget.value })} /></label>
                <label>عربي<input value={f.ar} onChange={(e) => setF({ ...f, ar: e.currentTarget.value })} /></label>
                <label>Français<input value={f.fr} onChange={(e) => setF({ ...f, fr: e.currentTarget.value })} /></label>
                <label>{t('fin.cf.type')}<select value={f.type} onChange={(e) => setF({ ...f, type: e.currentTarget.value })}>{['text', 'number', 'date', 'choice', 'bool'].map((x) => <option key={x} value={x}>{t(`fin.cf.t.${x}`)}</option>)}</select></label>
                {f.type === 'choice' && <label>{t('fin.cf.options')}<input value={f.options} placeholder="A, B, C" onChange={(e) => setF({ ...f, options: e.currentTarget.value })} /></label>}
              </div>
              <div className="mx-row" style={{ gap: 10, flexWrap: 'wrap' }}>
                <label className="mx-row" style={{ gap: 4 }}><input type="checkbox" checked={f.required} onChange={(e) => setF({ ...f, required: e.currentTarget.checked })} />{t('fin.cf.required')}</label>
                <label className="mx-row" style={{ gap: 4 }}><input type="checkbox" checked={f.show_in_list} onChange={(e) => setF({ ...f, show_in_list: e.currentTarget.checked })} />{t('fin.cf.inList')}</label>
                <label className="mx-row" style={{ gap: 4 }}><input type="checkbox" checked={f.is_active} onChange={(e) => setF({ ...f, is_active: e.currentTarget.checked })} />{t('fin.active')}</label>
                <button className="mx-btn" disabled={!f.key || !f.label.trim()} onClick={() => {
                  const body = { entity, key: f.key, label: f.label, label_i18n: Object.fromEntries(Object.entries({ ar: f.ar, fr: f.fr }).filter(([, v]) => v)), type: f.type,
                    options: f.options.split(',').map((x) => x.trim()).filter(Boolean), required: f.required, show_in_list: f.show_in_list, position: f.position, is_active: f.is_active };
                  void run(api(f.id ? `${base}/custom-fields/${f.id}` : `${base}/custom-fields`, { method: f.id ? 'PUT' : 'POST', body }), () => setF(blank));
                }}>{t('fin.save')}</button>
                {f.id && <button className="mx-btn sm" onClick={() => setF(blank)}>{t('fin.cancel')}</button>}
              </div>
            </>
          )}
          <Flash m={msg} />
        </div>
      </section>
      <section className="mx-box"><h3>{t('fin.cf.report')}</h3>
        <DataGrid rows={rep.rows} cols={cols} rowKey={(r) => r.id} title={`${t('fin.cf.title')} · ${t(`fin.cf.e.${entity}`)}`} />
      </section>
    </Screen>
  );
}

// ======================= manufacturing =======================

interface BOM { id: string; item_id: string; item_code: string; item_name: string; unit: string; name: string; output_qty: string; labour_cost: string; overhead_cost: string; components: number; is_active: boolean }
interface MO { id: string; number: number; bom_name: string; item_name: string; unit: string; quantity: string; warehouse: string; order_date: string; status: string; components_value: string; labour: string; overhead: string; scrap_qty: string; unit_cost: string }
interface MOLine { id: string; item_code: string; item_name: string; unit: string; planned_qty: string; consumed_qty: string; value: string; in_stock: string }

export function Manufacturing({ me, initial }: { me: Me; initial?: string }) {
  const { t } = useTranslation();
  const base = useBase();
  const { company } = useCompany();
  const bc = company?.base_currency ?? '';
  const [tab, setTab] = useState(initial || 'orders');
  const [boms, reloadBoms] = useLoad<BOM[]>(base && `${base}/mfg/boms`, []);
  const [orders, reloadOrders] = useLoad<MO[]>(base && `${base}/mfg/orders`, []);
  const [items] = useLoad<{ id: string; code: string; name: string; kind: string; is_active: boolean }[]>(base && `${base}/items`, []);
  const [whs] = useLoad<{ id: string; name: string }[]>(base && `${base}/warehouses`, []);
  const stockItems = items.filter((i) => i.kind === 'stock' && i.is_active);
  const blankB = { id: '', item_id: '', name: '', output_qty: '1', labour_cost: '0', overhead_cost: '0', lines: [{ item_id: '', quantity: '1' }] };
  const [b, setB] = useState(blankB);
  const [o, setO] = useState({ bom_id: '', quantity: '1', warehouse_id: '', date: today() });
  const [sel, setSel] = useState<{ order: MO; lines: MOLine[] } | null>(null);
  const [cons, setCons] = useState<Record<string, string>>({});
  const [scrapQ, setScrapQ] = useState('0');
  const [sc, setSc] = useState({ item_id: '', warehouse_id: '', quantity: '', reason: '' });
  const [msg, run] = useRun(() => { reloadBoms(); reloadOrders(); });
  const manage = can(me, 'stock.manage');
  const openOrder = (id: string) => base && api<{ order: MO; lines: MOLine[] }>(`${base}/mfg/orders/${id}`).then((d) => { setSel(d); setCons(Object.fromEntries(d.lines.map((l) => [l.id, l.consumed_qty]))); setScrapQ('0'); });
  const ocols: Col<MO>[] = [
    { key: 'n', title: '#', get: (r) => r.number, num: true, decimals: 0, width: 60 },
    { key: 'd', title: t('fin.date'), get: (r) => r.order_date, render: (r) => formatDate(r.order_date), width: 90 },
    { key: 'i', title: t('fin.item'), get: (r) => r.item_name, width: 170 },
    { key: 'q', title: t('fin.qty'), get: (r) => Number(r.quantity), render: (r) => `${r.quantity} ${r.unit}`, num: true, width: 90 },
    { key: 's', title: t('fin.status'), get: (r) => t(`fin.mfg.st.${r.status}`), width: 90 },
    { key: 'c', title: t('fin.mfg.unitCost'), get: (r) => Number(r.unit_cost), render: (r) => (r.status === 'done' ? `${amt(r.unit_cost, 4)} ${bc}` : ''), num: true, width: 110 },
  ];
  return (
    <Screen title={t('fin.mfg.title')} tabs={<Tabs tabs={[['orders', t('fin.mfg.orders')], ['boms', t('fin.mfg.boms')], ['scrap', t('fin.mfg.scrap')]]} value={tab} onChange={setTab} />}>
      {tab === 'boms' && (
        <>
          <section className="mx-box"><h3>{t('fin.mfg.boms')}</h3>
            <div className="mx-pay">{boms.map((x) => <button key={x.id} className="mx-btn sm" style={{ textAlign: 'start' }} onClick={() => base && api<{ bom: BOM; lines: { item_id: string; quantity: string }[] }>(`${base}/mfg/boms/${x.id}`).then((d) =>
              setB({ id: d.bom.id, item_id: d.bom.item_id, name: d.bom.name, output_qty: d.bom.output_qty, labour_cost: d.bom.labour_cost, overhead_cost: d.bom.overhead_cost, lines: d.lines.map((l) => ({ item_id: l.item_id, quantity: l.quantity })) }))}>
              {x.item_code} · {x.name} <small>({x.components} {t('fin.mfg.components')})</small></button>)}</div>
          </section>
          {manage && (
            <section className="mx-box"><h3>{b.id ? t('fin.edit') : t('fin.mfg.newBom')}</h3>
              <div className="mx-pay">
                <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
                  <label>{t('fin.mfg.makes')}<select value={b.item_id} onChange={(e) => setB({ ...b, item_id: e.currentTarget.value })}><option value="">{t('fin.choose')}</option>{stockItems.map((i) => <option key={i.id} value={i.id}>{i.code} · {i.name}</option>)}</select></label>
                  <label>{t('fin.name')}<input value={b.name} onChange={(e) => setB({ ...b, name: e.currentTarget.value })} /></label>
                  <label>{t('fin.mfg.output')}<input dir="ltr" style={{ width: 60 }} value={b.output_qty} onChange={(e) => setB({ ...b, output_qty: num(e.currentTarget.value) })} /></label>
                  <label>{t('fin.mfg.labour')} ({bc})<input dir="ltr" style={{ width: 80 }} value={b.labour_cost} onChange={(e) => setB({ ...b, labour_cost: num(e.currentTarget.value) })} /></label>
                  <label>{t('fin.mfg.overhead')} ({bc})<input dir="ltr" style={{ width: 80 }} value={b.overhead_cost} onChange={(e) => setB({ ...b, overhead_cost: num(e.currentTarget.value) })} /></label>
                </div>
                <table className="mx-table"><thead><tr><th>{t('fin.mfg.component')}</th><th>{t('fin.qty')}</th><th /></tr></thead>
                  <tbody>{b.lines.map((l, i) => (
                    <tr key={i}><td><select value={l.item_id} onChange={(e) => setB({ ...b, lines: b.lines.map((x, j) => (j === i ? { ...x, item_id: e.currentTarget.value } : x)) })}>
                      <option value="">{t('fin.choose')}</option>{stockItems.filter((s) => s.id !== b.item_id).map((s) => <option key={s.id} value={s.id}>{s.code} · {s.name}</option>)}</select></td>
                      <td><input dir="ltr" style={{ width: 70 }} value={l.quantity} onChange={(e) => setB({ ...b, lines: b.lines.map((x, j) => (j === i ? { ...x, quantity: num(e.currentTarget.value) } : x)) })} /></td>
                      <td><button className="mx-btn sm" aria-label={t('fin.delete')} onClick={() => setB({ ...b, lines: b.lines.filter((_, j) => j !== i) })}>×</button></td></tr>))}</tbody></table>
                <div className="mx-row" style={{ gap: 6 }}>
                  <button className="mx-btn sm" onClick={() => setB({ ...b, lines: [...b.lines, { item_id: '', quantity: '1' }] })}>{t('fin.addLine')}</button>
                  <button className="mx-btn" disabled={!b.item_id || !b.lines.some((l) => l.item_id)} onClick={() => {
                    const { id, ...body } = b;
                    void run(api(id ? `${base}/mfg/boms/${id}` : `${base}/mfg/boms`, { method: id ? 'PUT' : 'POST', body: { ...body, lines: body.lines.filter((l) => l.item_id && Number(l.quantity) > 0) } }), () => setB(blankB));
                  }}>{t('fin.save')}</button>
                  {b.id && <button className="mx-btn sm" onClick={() => setB(blankB)}>{t('fin.cancel')}</button>}
                </div>
                <Flash m={msg} />
              </div>
            </section>
          )}
        </>
      )}
      {tab === 'orders' && (
        <>
          {can(me, 'stock.create') && (
            <section className="mx-box"><h3>{t('fin.mfg.newOrder')}</h3>
              <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap' }}>
                <label>{t('fin.mfg.bom')}<select value={o.bom_id} onChange={(e) => setO({ ...o, bom_id: e.currentTarget.value })}><option value="">{t('fin.choose')}</option>{boms.filter((x) => x.is_active).map((x) => <option key={x.id} value={x.id}>{x.item_code} · {x.name}</option>)}</select></label>
                <label>{t('fin.qty')}<input dir="ltr" style={{ width: 70 }} value={o.quantity} onChange={(e) => setO({ ...o, quantity: num(e.currentTarget.value) })} /></label>
                <label>{t('fin.warehouse')}<select value={o.warehouse_id} onChange={(e) => setO({ ...o, warehouse_id: e.currentTarget.value })}><option value="">{t('fin.mainWh')}</option>{whs.map((w) => <option key={w.id} value={w.id}>{w.name}</option>)}</select></label>
                <label>{t('fin.date')}<input type="date" dir="ltr" value={o.date} onChange={(e) => setO({ ...o, date: e.currentTarget.value })} /></label>
                <button className="mx-btn" disabled={!o.bom_id} onClick={() => void run(api<{ order: MO }>(`${base}/mfg/orders`, { method: 'POST', body: o }), (x: { order: MO }) => void openOrder(x.order.id))}>{t('fin.create')}</button>
              </div>
            </section>
          )}
          <DataGrid rows={orders} cols={ocols} rowKey={(r) => r.id} title={t('fin.mfg.orders')} selected={sel?.order.id ?? null} onSelect={(r) => void openOrder(r.id)} />
          {sel && (
            <section className="mx-box"><h3>#{sel.order.number} · {sel.order.item_name} × {sel.order.quantity}</h3>
              <div className="mx-pay">
                <table className="mx-table"><thead><tr><th>{t('fin.mfg.component')}</th><th className="num">{t('fin.mfg.planned')}</th><th className="num">{t('fin.mfg.inStock')}</th><th className="num">{t('fin.mfg.used')}</th><th className="num">{t('fin.mfg.cost')}</th></tr></thead>
                  <tbody>{sel.lines.map((l) => (
                    <tr key={l.id}><td>{l.item_code} · {l.item_name}</td><td className="num">{l.planned_qty} {l.unit}</td><td className={`num ${Number(l.in_stock) < Number(cons[l.id] ?? l.consumed_qty) ? 'red' : ''}`}>{l.in_stock}</td>
                      <td className="num">{sel.order.status === 'draft' ? <input dir="ltr" style={{ width: 70 }} value={cons[l.id] ?? ''} onChange={(e) => setCons({ ...cons, [l.id]: num(e.currentTarget.value) })} /> : l.consumed_qty}</td>
                      <td className="num">{sel.order.status === 'done' ? amt(l.value) : ''}</td></tr>))}</tbody></table>
                {sel.order.status === 'done' && <div>{t('fin.mfg.summary', { comp: amt(sel.order.components_value), lab: amt(sel.order.labour), ovh: amt(sel.order.overhead), unit: amt(sel.order.unit_cost, 4), cur: bc, scrap: sel.order.scrap_qty })}</div>}
                {sel.order.status === 'draft' && manage && (
                  <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
                    <label>{t('fin.mfg.scrapQty')}<input dir="ltr" style={{ width: 60 }} value={scrapQ} onChange={(e) => setScrapQ(num(e.currentTarget.value))} /></label>
                    <button className="mx-btn main" onClick={() => void run(api(`${base}/mfg/orders/${sel.order.id}/produce`, { method: 'POST', body: { scrap_qty: scrapQ || '0', lines: Object.entries(cons).map(([line_id, quantity]) => ({ line_id, quantity: quantity || '0' })) } }), () => void openOrder(sel.order.id))}>{t('fin.mfg.produce')}</button>
                    <button className="mx-btn sm" onClick={() => void run(api(`${base}/mfg/orders/${sel.order.id}/cancel`, { method: 'POST', body: {} }), () => void openOrder(sel.order.id))}>{t('fin.cancel')}</button>
                  </div>
                )}
                <Attachments type="mfg_order" id={sel.order.id} readOnly={!manage} />
                <Flash m={msg} />
              </div>
            </section>
          )}
        </>
      )}
      {tab === 'scrap' && manage && (
        <section className="mx-box"><h3>{t('fin.mfg.scrap')}</h3>
          <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap' }}>
            <label>{t('fin.item')}<select value={sc.item_id} onChange={(e) => setSc({ ...sc, item_id: e.currentTarget.value })}><option value="">{t('fin.choose')}</option>{stockItems.map((i) => <option key={i.id} value={i.id}>{i.code} · {i.name}</option>)}</select></label>
            <label>{t('fin.warehouse')}<select value={sc.warehouse_id} onChange={(e) => setSc({ ...sc, warehouse_id: e.currentTarget.value })}><option value="">{t('fin.mainWh')}</option>{whs.map((w) => <option key={w.id} value={w.id}>{w.name}</option>)}</select></label>
            <label>{t('fin.qty')}<input dir="ltr" style={{ width: 70 }} value={sc.quantity} onChange={(e) => setSc({ ...sc, quantity: num(e.currentTarget.value) })} /></label>
            <label>{t('fin.reason')}<input value={sc.reason} onChange={(e) => setSc({ ...sc, reason: e.currentTarget.value })} /></label>
            <button className="mx-btn" disabled={!sc.item_id || !sc.quantity || !sc.reason.trim()} onClick={() => void run(api(`${base}/mfg/scrap`, { method: 'POST', body: sc }), () => setSc({ ...sc, quantity: '', reason: '' }))}>{t('fin.mfg.scrapIt')}</button>
          </div>
          <small style={{ padding: 6 }}>{t('fin.mfg.scrapHint')}</small>
          <Flash m={msg} />
        </section>
      )}
    </Screen>
  );
}

// ======================= HR extras =======================

interface Sched { id: string; name: string; start_time: string; end_time: string }
interface Rot { id: string; name: string; pattern: string[]; employees: number }
interface RosterRow { employee_id: string; code: string; name: string; rotation: string; days: { date: string; schedule_id: string; schedule: string; off: boolean; override: boolean }[] }

export function HRExtra({ me, initial }: { me: Me; initial?: string }) {
  const { t, i18n } = useTranslation();
  const base = useBase();
  const [tab, setTab] = useState(initial || 'roster');
  const edit = can(me, 'payroll.edit');
  const [scheds] = useLoad<Sched[]>(base && `${base}/hr/schedules`, []);
  const [rots, reloadRots] = useLoad<Rot[]>(base && `${base}/hr/rotations`, []);
  const [emps] = useLoad<{ id: string; code: string; name: string; status: string }[]>(base && `${base}/employees`, []);
  const [from, setFrom] = useState(today());
  const to = useMemo(() => { const d = new Date(from); d.setDate(d.getDate() + 13); return d.toISOString().slice(0, 10); }, [from]);
  const [roster, reloadRoster] = useLoad<{ rows: RosterRow[] }>(base && tab === 'roster' ? `${base}/hr/roster?from=${from}&to=${to}` : null, { rows: [] });
  const [rot, setRot] = useState({ id: '', name: '', pattern: [] as string[] });
  const [assign, setAssign] = useState({ rotation: '', start: today(), emps: [] as string[] });
  const year = new Date().getFullYear();
  const [acc, reloadAcc] = useLoad<{ id: string; employee_name: string; type_code: string; name_en: string; name_ar: string; name_fr: string; period: string; kind: string; days: string; note: string }[]>(base && tab === 'accruals' ? `${base}/hr/leave/accruals?year=${year}` : null, []);
  const [period, setPeriod] = useState(today().slice(0, 7));
  const [types] = useLoad<{ id: string; code: string; name_en: string; name_ar: string; name_fr: string }[]>(base && `${base}/hr/leave-types`, []);
  const [adj, setAdj] = useState({ employee_id: '', type_id: '', days: '', note: '' });
  const [clock, reloadClock] = useLoad<{ photo: boolean; geofence: boolean; lat: string | null; lng: string | null; radius_m: number }>(base && `${base}/hr/clock-settings`, { photo: false, geofence: false, lat: null, lng: null, radius_m: 200 });
  const [cl, setCl] = useState(clock);
  useEffect(() => setCl(clock), [clock]);
  const [devs, reloadDevs] = useLoad<{ id: string; serial: string; name: string; allowed_ip: string; is_active: boolean; last_seen: string | null; last_ip: string; punches: number; push_token: string }[]>(base && `${base}/hr/devices`, []);
  const [dev, setDev] = useState({ serial: '', name: '', allowed_ip: '' });
  const [msg, run] = useRun(() => { reloadRots(); reloadRoster(); reloadAcc(); reloadClock(); reloadDevs(); });
  const schedName = (id: string) => (id === 'off' ? t('fin.hr.off') : scheds.find((s) => s.id === id)?.name ?? '?');
  const lname = (r: { name_en: string; name_ar: string; name_fr: string }) => (i18n.language === 'ar' && r.name_ar) || (i18n.language === 'fr' && r.name_fr) || r.name_en;
  return (
    <Screen title={t('fin.hr.title')} tabs={<Tabs tabs={[['roster', t('fin.hr.roster')], ['accruals', t('fin.hr.accruals')], ['clock', t('fin.hr.clock')], ['devices', t('fin.hr.devices')]]} value={tab} onChange={setTab} />}>
      {tab === 'roster' && (
        <>
          <section className="mx-box"><h3>{t('fin.hr.rotations')}</h3>
            <div className="mx-pay">
              {rots.map((r) => <button key={r.id} className="mx-btn sm" style={{ textAlign: 'start' }} onClick={() => setRot({ id: r.id, name: r.name, pattern: r.pattern })}>{r.name} <small>({r.pattern.map(schedName).join(' → ')} · {r.employees})</small></button>)}
              {edit && (
                <>
                  <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
                    <label>{t('fin.name')}<input value={rot.name} onChange={(e) => setRot({ ...rot, name: e.currentTarget.value })} /></label>
                    <label>{t('fin.hr.addDay')}<select value="" onChange={(e) => setRot({ ...rot, pattern: [...rot.pattern, e.currentTarget.value] })}>
                      <option value="">{t('fin.choose')}</option>{scheds.map((s) => <option key={s.id} value={s.id}>{s.name} {s.start_time}–{s.end_time}</option>)}<option value="off">{t('fin.hr.off')}</option></select></label>
                    <span>{rot.pattern.map((p, i) => <button key={i} className="mx-btn sm" title={t('fin.delete')} onClick={() => setRot({ ...rot, pattern: rot.pattern.filter((_, j) => j !== i) })}>{i + 1}. {schedName(p)} ×</button>)}</span>
                    <button className="mx-btn" disabled={!rot.name.trim() || !rot.pattern.length} onClick={() => void run(api(rot.id ? `${base}/hr/rotations/${rot.id}` : `${base}/hr/rotations`, { method: rot.id ? 'PUT' : 'POST', body: { name: rot.name, pattern: rot.pattern } }), () => setRot({ id: '', name: '', pattern: [] }))}>{t('fin.save')}</button>
                  </div>
                  <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
                    <label>{t('fin.hr.rotation')}<select value={assign.rotation} onChange={(e) => setAssign({ ...assign, rotation: e.currentTarget.value })}><option value="">{t('fin.choose')}</option>{rots.map((r) => <option key={r.id} value={r.id}>{r.name}</option>)}</select></label>
                    <label>{t('fin.hr.from')}<input type="date" dir="ltr" value={assign.start} onChange={(e) => setAssign({ ...assign, start: e.currentTarget.value })} /></label>
                    <label>{t('fin.employees')}<select multiple size={4} value={assign.emps} onChange={(e) => setAssign({ ...assign, emps: Array.from(e.currentTarget.selectedOptions).map((o) => o.value) })}>
                      {emps.filter((x) => x.status === 'active').map((x) => <option key={x.id} value={x.id}>{x.code} · {x.name}</option>)}</select></label>
                    <button className="mx-btn" disabled={!assign.rotation || !assign.emps.length} onClick={() => void run(api(`${base}/hr/rotations/${assign.rotation}/assign`, { method: 'POST', body: { employee_ids: assign.emps, start: assign.start } }))}>{t('fin.hr.assign')}</button>
                  </div>
                </>
              )}
              <Flash m={msg} />
            </div>
          </section>
          <section className="mx-box"><h3>{t('fin.hr.roster')}</h3>
            <div className="mx-row" style={{ gap: 6, padding: 6 }}><label>{t('fin.hr.from')}<input type="date" dir="ltr" value={from} onChange={(e) => setFrom(e.currentTarget.value)} /></label></div>
            <div style={{ overflowX: 'auto' }}>
              <table className="mx-table"><thead><tr><th>{t('fin.employee')}</th>{roster.rows[0]?.days.map((d) => <th key={d.date} dir="ltr">{d.date.slice(5)}</th>)}</tr></thead>
                <tbody>{roster.rows.map((r) => (
                  <tr key={r.employee_id}><td>{r.name}<br /><small>{r.rotation}</small></td>
                    {r.days.map((d) => (
                      <td key={d.date} className={d.off ? 'muted' : ''} style={{ fontWeight: d.override ? 700 : 400 }}>
                        {edit ? <select value={d.off ? 'off' : d.schedule_id} aria-label={`${r.name} ${d.date}`} onChange={(e) => void run(api(`${base}/hr/roster`, { method: 'PUT', body: { employee_id: r.employee_id, work_date: d.date, schedule_id: e.currentTarget.value === 'off' ? '' : e.currentTarget.value } }))}>
                          {!d.schedule_id && !d.off && <option value="">—</option>}{scheds.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}<option value="off">{t('fin.hr.off')}</option></select> : (d.off ? t('fin.hr.off') : d.schedule)}
                      </td>))}
                  </tr>))}</tbody></table>
            </div>
          </section>
        </>
      )}
      {tab === 'accruals' && (
        <section className="mx-box"><h3>{t('fin.hr.accruals')} {year}</h3>
          <div className="mx-pay">
            {edit && <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
              <label>{t('fin.period')}<input type="month" dir="ltr" value={period} onChange={(e) => setPeriod(e.currentTarget.value)} /></label>
              <button className="mx-btn main" onClick={() => void run(api(`${base}/hr/leave/accrue`, { method: 'POST', body: { period } }))}>{t('fin.hr.accrue')}</button>
              <small>{t('fin.hr.accrueHint')}</small>
            </div>}
            {can(me, 'payroll.approve') && <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
              <label>{t('fin.employee')}<select value={adj.employee_id} onChange={(e) => setAdj({ ...adj, employee_id: e.currentTarget.value })}><option value="">{t('fin.choose')}</option>{emps.map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}</select></label>
              <label>{t('fin.hr.type')}<select value={adj.type_id} onChange={(e) => setAdj({ ...adj, type_id: e.currentTarget.value })}><option value="">{t('fin.choose')}</option>{types.map((x) => <option key={x.id} value={x.id}>{lname(x)}</option>)}</select></label>
              <label>{t('fin.days')} (±)<input dir="ltr" style={{ width: 60 }} value={adj.days} onChange={(e) => setAdj({ ...adj, days: num(e.currentTarget.value) })} /></label>
              <label>{t('fin.note')}<input value={adj.note} onChange={(e) => setAdj({ ...adj, note: e.currentTarget.value })} /></label>
              <button className="mx-btn" disabled={!adj.employee_id || !adj.type_id || !adj.days || !adj.note.trim()} onClick={() => void run(api(`${base}/hr/leave/adjust`, { method: 'POST', body: { ...adj, year } }), () => setAdj({ ...adj, days: '', note: '' }))}>{t('fin.hr.adjust')}</button>
            </div>}
            <Flash m={msg} />
            <table className="mx-table"><thead><tr><th>{t('fin.employee')}</th><th>{t('fin.hr.type')}</th><th>{t('fin.period')}</th><th>{t('fin.hr.kind')}</th><th className="num">{t('fin.days')}</th><th>{t('fin.note')}</th></tr></thead>
              <tbody>{acc.map((a) => <tr key={a.id}><td>{a.employee_name}</td><td>{lname(a)}</td><td dir="ltr">{a.period}</td><td>{t(`fin.hr.k.${a.kind}`)}</td><td className="num">{a.days}</td><td>{a.note}</td></tr>)}</tbody></table>
          </div>
        </section>
      )}
      {tab === 'clock' && (
        <section className="mx-box"><h3>{t('fin.hr.clock')}</h3>
          <div className="mx-pay">
            <label className="mx-row" style={{ gap: 6 }}><input type="checkbox" checked={cl.photo} disabled={!edit} onChange={(e) => setCl({ ...cl, photo: e.currentTarget.checked })} />{t('fin.hr.needPhoto')}</label>
            <label className="mx-row" style={{ gap: 6 }}><input type="checkbox" checked={cl.geofence} disabled={!edit} onChange={(e) => setCl({ ...cl, geofence: e.currentTarget.checked })} />{t('fin.hr.needPlace')}</label>
            <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
              <label>{t('fin.hr.lat')}<input dir="ltr" style={{ width: 110 }} value={cl.lat ?? ''} onChange={(e) => setCl({ ...cl, lat: num(e.currentTarget.value) })} /></label>
              <label>{t('fin.hr.lng')}<input dir="ltr" style={{ width: 110 }} value={cl.lng ?? ''} onChange={(e) => setCl({ ...cl, lng: num(e.currentTarget.value) })} /></label>
              <label>{t('fin.hr.radius')}<input dir="ltr" style={{ width: 80 }} value={cl.radius_m} onChange={(e) => setCl({ ...cl, radius_m: Number(num(e.currentTarget.value) || 0) })} /></label>
              {edit && <button className="mx-btn sm" onClick={() => navigator.geolocation?.getCurrentPosition((p) => setCl((c) => ({ ...c, lat: p.coords.latitude.toFixed(6), lng: p.coords.longitude.toFixed(6) })))}>{t('fin.hr.here')}</button>}
            </div>
            {edit && <button className="mx-btn main" onClick={() => void run(api(`${base}/hr/clock-settings`, { method: 'PUT', body: { photo: cl.photo, geofence: cl.geofence, lat: cl.lat ? Number(cl.lat) : null, lng: cl.lng ? Number(cl.lng) : null, radius_m: cl.radius_m } }))}>{t('fin.save')}</button>}
            <small>{t('fin.hr.clockHint')}</small>
            <Flash m={msg} />
          </div>
        </section>
      )}
      {tab === 'devices' && (
        <section className="mx-box"><h3>{t('fin.hr.devices')}</h3>
          <div className="mx-pay">
            <div className="mx-note"><small>{t('fin.hr.admsHint', { host: window.location.hostname })}</small></div>
            <div className="mx-note"><small>{t('fin.hr.tokenHint')}</small></div>
            <table className="mx-table"><thead><tr><th>{t('fin.hr.serial')}</th><th>{t('fin.name')}</th><th>{t('fin.hr.pushPath')}</th><th>{t('fin.hr.ip')}</th><th>{t('fin.hr.lastSeen')}</th><th className="num">{t('fin.hr.punches')}</th><th /></tr></thead>
              <tbody>{devs.map((d) => <tr key={d.id}><td dir="ltr">{d.serial}</td><td>{d.name}</td><td dir="ltr" style={{ userSelect: 'all', fontSize: 12 }}>{d.push_token ? `/iclock/k/${d.push_token}/` : '—'}</td><td dir="ltr">{d.allowed_ip || '—'}</td><td dir="ltr">{d.last_seen ? `${new Date(d.last_seen).toLocaleString()} · ${d.last_ip}` : '—'}</td><td className="num">{d.punches}</td>
                <td>{edit && <button className="mx-btn sm" onClick={() => void run(api(`${base}/hr/devices/${d.id}`, { method: 'PATCH', body: { name: d.name, allowed_ip: d.allowed_ip, is_active: !d.is_active } }))}>{d.is_active ? t('fin.disable') : t('fin.enable')}</button>}</td></tr>)}</tbody></table>
            {edit && <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
              <label>{t('fin.hr.serial')}<input dir="ltr" value={dev.serial} onChange={(e) => setDev({ ...dev, serial: e.currentTarget.value.trim() })} /></label>
              <label>{t('fin.name')}<input value={dev.name} onChange={(e) => setDev({ ...dev, name: e.currentTarget.value })} /></label>
              <label>{t('fin.hr.ip')}<input dir="ltr" placeholder="*" value={dev.allowed_ip} onChange={(e) => setDev({ ...dev, allowed_ip: e.currentTarget.value.trim() })} /></label>
              <button className="mx-btn" disabled={dev.serial.length < 4} onClick={() => void run(api(`${base}/hr/devices`, { method: 'POST', body: dev }), () => setDev({ serial: '', name: '', allowed_ip: '' }))}>{t('fin.add')}</button>
            </div>}
            <Flash m={msg} />
          </div>
        </section>
      )}
    </Screen>
  );
}

// ======================= online payments =======================

interface GW { id: string; kind: string; name: string; method_id: string; test_mode: boolean; instructions: string; is_active: boolean; config: Record<string, string | boolean>; webhook_url: string }
interface Intent { id: string; gateway_name: string; gateway_kind: string; customer_name: string; customer_code: string; invoice_number: number | null; currency: string; amount: string; status: string; reference: string; payer_phone: string; payment_number: number | null; created_at: string; error: string; test_mode: boolean }
const GW_FIELDS: Record<string, [string, boolean][]> = {
  stripe: [['secret_key', true], ['webhook_secret', true], ['publishable_key', false]],
  hosted: [['provider', false], ['checkout_url', false], ['merchant_id', false], ['shared_secret', true]],
  transfer: [['account_name', false], ['account_number', false], ['phone', false]],
};

export function OnlinePayments({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [gws, reloadG] = useLoad<GW[]>(base && `${base}/payment-gateways`, []);
  const [status, setStatus] = useState('review');
  const [intents, reloadI] = useLoad<Intent[]>(base && `${base}/payment-intents?status=${status}`, []);
  const [methods] = useLoad<{ id: string; name: string; currency: string; is_active: boolean }[]>(base && `${base}/payment-methods`, []);
  const blank = { id: '', kind: 'transfer', name: '', method_id: '', test_mode: true, instructions: '', is_active: true, config: {} as Record<string, string> };
  const [f, setF] = useState(blank);
  const [msg, run] = useRun(() => { reloadG(); reloadI(); });
  const manage = can(me, 'settings.manage');
  const cols: Col<Intent>[] = [
    { key: 'd', title: t('fin.date'), get: (r) => r.created_at, render: (r) => new Date(r.created_at).toLocaleString(), width: 140 },
    { key: 'c', title: t('fin.customer'), get: (r) => `${r.customer_code} · ${r.customer_name}`, width: 170 },
    { key: 'i', title: t('fin.invoice'), get: (r) => r.invoice_number ?? '', width: 70 },
    { key: 'g', title: t('fin.pay.way'), get: (r) => `${r.gateway_name}${r.test_mode ? ` (${t('fin.pay.test')})` : ''}`, width: 130 },
    { key: 'a', title: t('fin.amount'), get: (r) => Number(r.amount), render: (r) => `${amt(r.amount)} ${r.currency}`, num: true, width: 100 },
    { key: 'r', title: t('fin.pay.ref'), get: (r) => `${r.reference} ${r.payer_phone}`, width: 130 },
    { key: 's', title: t('fin.status'), get: (r) => t(`fin.pay.st.${r.status}`), width: 90 },
    { key: 'p', title: t('fin.pay.receipt'), get: (r) => r.payment_number ?? '', width: 70 },
  ];
  const [sel, setSel] = useState<Intent | null>(null);
  return (
    <Screen title={t('fin.pay.title')}>
      <section className="mx-box"><h3>{t('fin.pay.ways')}</h3>
        <div className="mx-pay">
          {gws.map((g) => <button key={g.id} className="mx-btn sm" style={{ textAlign: 'start' }} onClick={() => setF({ id: g.id, kind: g.kind, name: g.name, method_id: g.method_id, test_mode: g.test_mode, instructions: g.instructions, is_active: g.is_active,
            config: Object.fromEntries(Object.entries(g.config).filter(([k]) => !k.startsWith('has_')).map(([k, v]) => [k, String(v)])) })}>
            {g.name} <small>({t(`fin.pay.k.${g.kind}`)}{g.test_mode ? ` · ${t('fin.pay.test')}` : ''}{g.is_active ? '' : ` · ${t('fin.inactive')}`})</small></button>)}
          {manage && (
            <>
              <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
                <label>{t('fin.pay.kind')}<select value={f.kind} disabled={!!f.id} onChange={(e) => setF({ ...f, kind: e.currentTarget.value, config: {} })}>{['transfer', 'hosted', 'stripe'].map((k) => <option key={k} value={k}>{t(`fin.pay.k.${k}`)}</option>)}</select></label>
                <label>{t('fin.name')}<input value={f.name} placeholder={f.kind === 'transfer' ? 'Whish / OMT' : ''} onChange={(e) => setF({ ...f, name: e.currentTarget.value })} /></label>
                <label>{t('fin.pay.into')}<select value={f.method_id} onChange={(e) => setF({ ...f, method_id: e.currentTarget.value })}><option value="">{t('fin.choose')}</option>{methods.filter((m) => m.is_active).map((m) => <option key={m.id} value={m.id}>{m.name} ({m.currency})</option>)}</select></label>
                <label className="mx-row" style={{ gap: 4 }}><input type="checkbox" checked={f.test_mode} onChange={(e) => setF({ ...f, test_mode: e.currentTarget.checked })} />{t('fin.pay.testMode')}</label>
                <label className="mx-row" style={{ gap: 4 }}><input type="checkbox" checked={f.is_active} onChange={(e) => setF({ ...f, is_active: e.currentTarget.checked })} />{t('fin.active')}</label>
              </div>
              <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
                {GW_FIELDS[f.kind].map(([k, secret]) => (
                  <label key={k}>{t(`fin.pay.f.${k}`)}<input dir="ltr" type={secret ? 'password' : 'text'} autoComplete="off" placeholder={secret && f.id ? '••••••' : ''} value={f.config[k] ?? ''}
                    onChange={(e) => setF({ ...f, config: { ...f.config, [k]: e.currentTarget.value } })} /></label>
                ))}
              </div>
              <label>{t('fin.pay.instructions')}<input value={f.instructions} onChange={(e) => setF({ ...f, instructions: e.currentTarget.value })} /></label>
              {f.id && f.kind !== 'transfer' && <small dir="ltr">{t('fin.pay.webhook')}: {gws.find((g) => g.id === f.id)?.webhook_url}</small>}
              <div className="mx-row" style={{ gap: 6 }}>
                <button className="mx-btn" disabled={!f.name.trim() || !f.method_id} onClick={() => {
                  const { id, ...body } = f;
                  void run(api(id ? `${base}/payment-gateways/${id}` : `${base}/payment-gateways`, { method: id ? 'PUT' : 'POST', body }), () => setF(blank));
                }}>{t('fin.save')}</button>
                {f.id && <button className="mx-btn sm" onClick={() => setF(blank)}>{t('fin.cancel')}</button>}
              </div>
              <small>{t('fin.pay.hint')}</small>
            </>
          )}
        </div>
      </section>
      <section className="mx-box"><h3>{t('fin.pay.attempts')}</h3>
        <div className="mx-row" style={{ gap: 6, padding: 6 }}>
          <select value={status} onChange={(e) => setStatus(e.currentTarget.value)} aria-label={t('fin.status')}><option value="">{t('fin.all')}</option>{['review', 'pending', 'paid', 'failed', 'rejected'].map((s) => <option key={s} value={s}>{t(`fin.pay.st.${s}`)}</option>)}</select>
        </div>
        <DataGrid rows={intents} cols={cols} rowKey={(r) => r.id} title={t('fin.pay.attempts')} selected={sel?.id ?? null} onSelect={setSel} />
        {sel && (sel.status === 'review' || sel.status === 'pending') && can(me, 'collections.edit') && (
          <div className="mx-row" style={{ gap: 6, padding: 6 }}>
            <span>{sel.customer_name} · {amt(sel.amount)} {sel.currency} · {sel.reference}</span>
            <button className="mx-btn main" onClick={() => void run(api(`${base}/payment-intents/${sel.id}/confirm`, { method: 'POST', body: {} }), () => setSel(null))}>{t('fin.pay.confirm')}</button>
            <button className="mx-btn" onClick={() => void run(api(`${base}/payment-intents/${sel.id}/reject`, { method: 'POST', body: { reason: '' } }), () => setSel(null))}>{t('fin.reject')}</button>
          </div>
        )}
        <Flash m={msg} />
      </section>
    </Screen>
  );
}

/** Public: the test-mode payment page (#/pay-sim/<token>). */
export function PaySim({ token }: { token: string }) {
  const { t } = useTranslation();
  const [info, setInfo] = useState<{ amount: string; currency: string; status: string; gateway: string; company: string; invoice_number: number | null } | null>(null);
  const [done, setDone] = useState('');
  useEffect(() => { fetch(`${API_BASE}/pay/sim/${token}`).then((r) => (r.ok ? r.json() : null)).then(setInfo).catch(() => setInfo(null)); }, [token]);
  const send = (result: string) => fetch(`${API_BASE}/pay/sim/${token}`, { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'paxve' }, body: JSON.stringify({ result }) })
    .then((r) => setDone(r.ok ? result : 'error'));
  if (!info) return <div className="mx-pay" style={{ maxWidth: 420, margin: '40px auto' }}>{t('fin.pay.simGone')}</div>;
  return (
    <div className="mx-pay" style={{ maxWidth: 420, margin: '40px auto', border: '2px dashed #b7791f', padding: 16 }}>
      <div style={{ background: '#b42318', color: '#fff', fontWeight: 700, textAlign: 'center', padding: '6px 8px', borderRadius: 6 }}>{t('fin.pay.testBanner')}</div>
      <b>{t('fin.pay.simTitle')}</b>
      <div>{info.company} · {t('fin.invoice')} #{info.invoice_number}</div>
      <div style={{ fontSize: 24 }} dir="ltr">{amt(info.amount)} {info.currency}</div>
      <small>{t('fin.pay.simHint')}</small>
      {done ? <div className={`mx-flash ${done === 'paid' ? 'good' : 'bad'}`}>{t(`fin.pay.simDone.${done}`)}</div> : (
        <div className="mx-row" style={{ gap: 6 }}>
          <button className="mx-btn main" onClick={() => void send('paid')}>{t('fin.pay.simPay')}</button>
          <button className="mx-btn" onClick={() => void send('failed')}>{t('fin.pay.simFail')}</button>
        </div>
      )}
    </div>
  );
}

// ======================= e-invoicing =======================

const EINV_FIELDS: Record<string, string[]> = {
  zatca: ['seller_name', 'vat_number', 'crn', 'street', 'building', 'district', 'city', 'postal_code', 'branch', 'business_category', 'egs_serial'],
  eta: ['issuer_name', 'issuer_id', 'issuer_type', 'activity_code', 'branch_id', 'governate', 'region_city', 'street', 'building', 'client_id', 'client_secret', 'signer_url', 'signer_token'],
};
const EINV_SECRET = new Set(['client_secret', 'signer_token']);

export function EInvoice({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const { company } = useCompany();
  const [kind, setKind] = useState<string>(company?.einvoice === 'eta' ? 'eta' : 'zatca');
  const [cfg, reload] = useLoad<{ env: string; config: Record<string, string | boolean>; counter: number }>(base && `${base}/einvoice/config?kind=${kind}`, { env: '', config: {}, counter: 0 });
  const [f, setF] = useState<Record<string, string>>({});
  const [env, setEnv] = useState('');
  const [otp, setOtp] = useState('');
  const [res, setRes] = useState<string>('');
  const [msg, run] = useRun(reload);
  useEffect(() => { setEnv(cfg.env); setF(Object.fromEntries(Object.entries(cfg.config).filter(([k]) => !k.startsWith('has_') && k !== 'csr').map(([k, v]) => [k, String(v)]))); }, [cfg]);
  const manage = can(me, 'settings.manage');
  const envs = kind === 'zatca' ? ['sandbox', 'simulation', 'production'] : ['preprod', 'production'];
  return (
    <Screen title={t('fin.einv.title')} tabs={<Tabs tabs={[['zatca', t('fin.einv.zatca')], ['eta', t('fin.einv.eta')]]} value={kind} onChange={setKind} />}>
      <section className="mx-box"><h3>{t(`fin.einv.${kind}`)}</h3>
        <div className="mx-pay">
          <small>{t(`fin.einv.hint.${kind}`)}</small>
          <label>{t('fin.einv.env')}<select value={env} disabled={!manage} onChange={(e) => setEnv(e.currentTarget.value)}>{envs.map((x) => <option key={x} value={x}>{t(`fin.einv.envs.${x}`)}</option>)}</select></label>
          <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
            {EINV_FIELDS[kind].map((k) => (
              <label key={k}>{t(`fin.einv.f.${k}`)}<input dir={k.includes('name') || k === 'street' || k === 'district' || k === 'city' ? undefined : 'ltr'} disabled={!manage}
                type={EINV_SECRET.has(k) ? 'password' : 'text'} placeholder={EINV_SECRET.has(k) && cfg.config[`has_${k}`] ? '••••••' : ''} value={f[k] ?? ''}
                onChange={(e) => setF({ ...f, [k]: e.currentTarget.value })} /></label>
            ))}
          </div>
          {manage && <button className="mx-btn" onClick={() => void run(api(`${base}/einvoice/config`, { method: 'PUT', body: { kind, env, config: f } }))}>{t('fin.save')}</button>}
          {kind === 'zatca' && (
            <>
              <div>{t('fin.einv.status')}: {cfg.config.pcsid ? t('fin.einv.st.production') : cfg.config.csid ? t('fin.einv.st.compliance') : cfg.config.has_private_key ? t('fin.einv.st.csr') : t('fin.einv.st.none')} · ICV {cfg.counter}</div>
              {manage && <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
                <label>OTP<input dir="ltr" style={{ width: 100 }} value={otp} onChange={(e) => setOtp(e.currentTarget.value.replace(/\D/g, ''))} /></label>
                <button className="mx-btn main" onClick={() => void run(api<Record<string, unknown>>(`${base}/einvoice/zatca/onboard`, { method: 'POST', body: { otp } }), (x: Record<string, unknown>) => setRes(JSON.stringify(x, null, 1)))}>{t('fin.einv.onboard')}</button>
                {Boolean(cfg.config.csid) && <button className="mx-btn" onClick={() => void run(api<Record<string, unknown>>(`${base}/einvoice/zatca/onboard`, { method: 'POST', body: { production: true } }), (x: Record<string, unknown>) => setRes(JSON.stringify(x, null, 1)))}>{t('fin.einv.production')}</button>}
              </div>}
            </>
          )}
          {res && <pre style={{ whiteSpace: 'pre-wrap', maxHeight: 240, overflow: 'auto', margin: 0 }} dir="ltr">{res}</pre>}
          <Flash m={msg} />
        </div>
      </section>
    </Screen>
  );
}

/** Inside the invoice window: make / send the e-invoice (when the company uses ZATCA or ETA). */
export function EinvoiceButton({ invoiceId }: { invoiceId: string }) {
  const { t } = useTranslation();
  const base = useBase();
  const { company } = useCompany();
  const [doc, reload] = useLoad<{ status: string; hash?: string; qr?: string; response?: { http_status?: number } }>(base && company?.einvoice ? `${base}/invoices/${invoiceId}/einvoice` : null, { status: 'none' });
  const [msg, run] = useRun(reload);
  if (!company?.einvoice) return null;
  return (
    <div className="mx-row no-print" style={{ gap: 6, flexWrap: 'wrap' }}>
      <small>{t('fin.einv.doc')}: <b>{t(`fin.einv.ds.${doc.status}`, { defaultValue: doc.status })}</b>{doc.response?.http_status ? ` (${doc.response.http_status})` : ''}</small>
      {doc.status !== 'reported' && doc.status !== 'cleared' && doc.status !== 'submitted' && doc.status !== 'valid' && (
        <button className="mx-btn sm" onClick={() => void run(api(`${base}/invoices/${invoiceId}/einvoice`, { method: 'POST', body: { submit: true } }))}>{t('fin.einv.send')}</button>
      )}
      {doc.status !== 'none' && <a className="mx-btn sm" href={`${API_BASE}${base}/invoices/${invoiceId}/einvoice?xml=1`} target="_blank" rel="noreferrer">{company.einvoice === 'eta' ? 'JSON' : 'XML'}</a>}
      <Flash m={msg} />
    </div>
  );
}
