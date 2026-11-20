import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api } from '../api';
import { useCompany } from '../company';
import { errorText, formatAmount } from '../util';

interface FP { id: string; name: string; is_active: boolean }
interface Credit {
  policy: 'off' | 'warn' | 'block'; base_currency: string; limit: string | null; overdue_days: number | null; hold: boolean; exposure: string;
  open_invoices: string; unapplied_credits: string; open_orders: string; max_overdue_days: number; available: string | null; no_rate?: boolean;
}

function useFPs(base: string | null): FP[] {
  const [l, setL] = useState<FP[]>([]);
  useEffect(() => { if (base) api<FP[]>(`${base}/fiscal-positions`).then(setL).catch(() => setL([])); }, [base]);
  return l;
}

/** Credit limit, overdue rule, hold and fiscal position of one customer, with what they owe now (exposure). */
export function CustomerCredit({ customerId, readOnly }: { customerId: string; readOnly?: boolean }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const base = company ? `/companies/${company.id}` : null;
  const fps = useFPs(base);
  const [c, setC] = useState<Credit | null>(null);
  const [f, setF] = useState({ limit: '', days: '', hold: false, fp: '' });
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const load = useCallback(() => {
    if (!base) return;
    api<{ credit: Credit; customer_overdue_days: number | null; fiscal_position_id: string | null }>(`${base}/customers/${customerId}/credit`).then((x) => {
      setC(x.credit);
      setF({ limit: x.credit.limit ?? '', days: x.customer_overdue_days === null ? '' : String(x.customer_overdue_days), hold: x.credit.hold, fp: x.fiscal_position_id ?? '' });
    }).catch(() => setC(null));
  }, [base, customerId]);
  useEffect(load, [load]);
  if (!c) return null;
  const dec = 2;
  async function save() {
    if (!base) return;
    try {
      await api(`${base}/customers/${customerId}/credit`, { method: 'PUT', body: {
        credit_limit: f.limit.trim(), ...(f.days.trim() === '' ? { clear_overdue_days: true } : { credit_overdue_days: Number(f.days) }),
        credit_hold: f.hold, fiscal_position_id: f.fp } });
      setMsg({ ok: true, text: t('common.saved') });
      load();
    } catch (e) { setMsg({ ok: false, text: errorText(t, e) }); }
  }
  const over = c.limit !== null && Number(c.exposure) > Number(c.limit);
  return (
    <section className="mx-box no-print">
      <h3>{t('gp.credit.title')} <small>{t(`gp.credit.policy.${c.policy}`)}</small></h3>
      <div className="mx-row" style={{ gap: 12, padding: 8, flexWrap: 'wrap' }}>
        <span className={`mx-pill ${over ? 'warn' : ''}`}>{t('gp.credit.exposure')}: <bdi dir="ltr">{formatAmount(c.exposure, dec)} {c.base_currency}</bdi></span>
        {c.available !== null && <span className="mx-pill">{t('gp.credit.available')}: <bdi dir="ltr">{formatAmount(c.available, dec)}</bdi></span>}
        <small>{t('gp.credit.parts', { open: formatAmount(c.open_invoices, dec), orders: formatAmount(c.open_orders, dec), credits: formatAmount(c.unapplied_credits, dec) })}</small>
        {c.max_overdue_days > 0 && <span className="mx-pill warn">{t('gp.credit.late', { n: c.max_overdue_days })}</span>}
        {c.hold && <span className="mx-pill warn">{t('gp.credit.onHold')}</span>}
        {c.no_rate && <small className="red">{t('gp.credit.noRate')}</small>}
      </div>
      <div className="mx-row" style={{ gap: 8, padding: 8, flexWrap: 'wrap', alignItems: 'end' }}>
        <label>{t('gp.credit.limit')} ({c.base_currency})<input dir="ltr" disabled={readOnly} value={f.limit} placeholder={t('gp.credit.noLimit')}
          onChange={(e) => setF({ ...f, limit: e.currentTarget.value })} /></label>
        <label>{t('gp.credit.days')}<input dir="ltr" type="number" min={0} disabled={readOnly} value={f.days} placeholder={c.overdue_days === null ? '—' : String(c.overdue_days)}
          onChange={(e) => setF({ ...f, days: e.currentTarget.value })} /></label>
        <label className="mx-row" style={{ gap: 4 }}><input type="checkbox" disabled={readOnly} checked={f.hold} onChange={(e) => setF({ ...f, hold: e.currentTarget.checked })} />{t('gp.credit.hold')}</label>
        <label>{t('gp.fp.title')}<select disabled={readOnly} value={f.fp} onChange={(e) => setF({ ...f, fp: e.currentTarget.value })}>
          <option value="">—</option>{fps.filter((x) => x.is_active || x.id === f.fp).map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}</select></label>
        {!readOnly && <button className="mx-btn sm main" onClick={() => void save()}>{t('common.save')}</button>}
        {msg && <span className={`mx-flash ${msg.ok ? 'good' : 'bad'}`}>{msg.text}</span>}
      </div>
    </section>
  );
}

/** Warranty months of an item (repairs check a serial's sale date against it). */
export function WarrantyBox({ itemId, readOnly }: { itemId: string; readOnly?: boolean }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const base = company ? `/companies/${company.id}` : null;
  const [m, setM] = useState<string | null>(null);
  const [msg, setMsg] = useState('');
  useEffect(() => {
    if (base) api<{ item: { warranty_months: number } }>(`${base}/items/${itemId}/extras`).then((x) => setM(String(x.item.warranty_months ?? 0))).catch(() => setM(null));
  }, [base, itemId]);
  if (m === null) return null;
  return (
    <section className="mx-box no-print">
      <h3>{t('gp.rep.warranty')}</h3>
      <div className="mx-row" style={{ gap: 8, padding: 8, alignItems: 'end', flexWrap: 'wrap' }}>
        <label>{t('gp.rep.warrantyMonths')}<input type="number" min={0} max={240} dir="ltr" disabled={readOnly} value={m} onChange={(e) => setM(e.currentTarget.value)} /></label>
        {!readOnly && <button className="mx-btn sm" onClick={() => base && api(`${base}/items/${itemId}/stock-settings`, { method: 'PUT', body: { warranty_months: Number(m) || 0 } })
          .then(() => setMsg(t('common.saved'))).catch((e) => setMsg(errorText(t, e)))}>{t('common.save')}</button>}
        {msg && <small>{msg}</small>}
      </div>
    </section>
  );
}

/** Fiscal position of a supplier (tax / account mapping on its bills). */
export function SupplierFiscal({ supplierId, current, readOnly }: { supplierId: string; current: string | null; readOnly?: boolean }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const base = company ? `/companies/${company.id}` : null;
  const fps = useFPs(base);
  const [v, setV] = useState(current ?? '');
  const [msg, setMsg] = useState('');
  if (fps.length === 0 && !current) return null;
  return (
    <section className="mx-box no-print">
      <h3>{t('gp.fp.title')}</h3>
      <div className="mx-row" style={{ gap: 8, padding: 8, alignItems: 'end', flexWrap: 'wrap' }}>
        <select disabled={readOnly} value={v} aria-label={t('gp.fp.title')} onChange={(e) => setV(e.currentTarget.value)}>
          <option value="">—</option>{fps.map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}</select>
        {!readOnly && <button className="mx-btn sm" onClick={() => base && api(`${base}/suppliers/${supplierId}/fiscal-position`, { method: 'PUT', body: { fiscal_position_id: v } })
          .then(() => setMsg(t('common.saved'))).catch((e) => setMsg(errorText(t, e)))}>{t('common.save')}</button>}
        {msg && <small>{msg}</small>}
      </div>
    </section>
  );
}
