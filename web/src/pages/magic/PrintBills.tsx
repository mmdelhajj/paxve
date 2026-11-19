import { useEffect, useMemo, useState } from 'react';
import { createPortal } from 'react-dom';
import QRCode from 'qrcode';
import { useTranslation } from 'react-i18next';
import { api } from '../../api';
import { useCompany } from '../../company';
import { printPage } from '../../native';
import { formatAmount, notifyError } from '../../util';
import { currentPeriod, type Area } from '../../billing';
import DataGrid, { type Col } from '../../components/DataGrid';
import StationSelect from '../../components/StationSelect';
import { BillSheets, type BillData, type BillRow } from '../billing/BulkPrint';
import { Help } from '../acc/common';
import { useActivations } from '../../paneActive';

/** طباعة الإيصالات: pick the month, area and route order, tick the bills, print them (A4 or thermal roll). */
export default function PrintBills({ initialPeriod }: { initialPeriod?: string }) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [period, setPeriod] = useState(initialPeriod && /^\d{4}-\d{2}$/.test(initialPeriod) ? initialPeriod : currentPeriod());
  const [area, setArea] = useState('');
  const [station, setStation] = useState<string | null>(null);
  const [unpaid, setUnpaid] = useState(true);
  const [layout, setLayout] = useState<'a4' | 'thermal'>('a4');
  const [order, setOrder] = useState<'area' | 'number' | 'name'>('area');
  const [areas, setAreas] = useState<Area[]>([]);
  const [data, setData] = useState<BillData | null>(null);
  const [loading, setLoading] = useState(false);
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [qr, setQr] = useState<Record<string, string>>({});
  const [printing, setPrinting] = useState(false);
  const cid = company?.id;
  const act = useActivations();

  useEffect(() => { if (cid) api<Area[]>(`/companies/${cid}/areas`).then(setAreas).catch(() => {}); }, [cid]);
  useEffect(() => {
    if (!cid) return;
    setLoading(true);
    const p = new URLSearchParams({ period, area_id: area, station_id: station ?? '', unpaid: unpaid ? '1' : '' });
    api<BillData>(`/companies/${cid}/invoices/print?${p}`).then((d) => { setData(d); setPicked(new Set(d.rows.map((r) => r.id))); })
      .catch((e) => { notifyError(t, e); setData(null); }).finally(() => setLoading(false));
  }, [cid, period, area, station, unpaid, act, t]);

  const rows = useMemo(() => {
    const list = [...(data?.rows ?? [])];
    if (order === 'number') list.sort((a, b) => a.number - b.number);
    else if (order === 'name') list.sort((a, b) => a.customer_name.localeCompare(b.customer_name));
    else list.sort((a, b) => (a.area_name ?? '').localeCompare(b.area_name ?? '') || (a.box_code ?? '').localeCompare(b.box_code ?? '', undefined, { numeric: true }));
    return list;
  }, [data, order]);
  if (!company) return null;

  async function print() {
    if (!data) return;
    const chosen = rows.filter((r) => picked.has(r.id));
    if (!chosen.length) return;
    const codes: Record<string, string> = {};
    for (const r of chosen) codes[r.id] = await QRCode.toDataURL(data.portal_base + r.portal_token, { margin: 0, width: 120, errorCorrectionLevel: 'M' });
    setQr(codes);
    setPrinting(true);
    setTimeout(() => { void printPage(t('mx.t.printBills')).finally(() => setTimeout(() => setPrinting(false), 500)); }, 80);
  }
  const toggle = (id: string) => setPicked((s) => { const n = new Set(s); if (n.has(id)) n.delete(id); else n.add(id); return n; });
  const cols: Col<BillRow>[] = [
    { key: 'pick', title: '✓', get: (r) => (picked.has(r.id) ? 1 : 0), filter: false, width: 40,
      render: (r) => <input type="checkbox" aria-label={String(r.number)} checked={picked.has(r.id)} onChange={() => toggle(r.id)} /> },
    { key: 'num', title: t('invoices.number'), get: (r) => r.number, width: 80 },
    { key: 'name', title: t('customers.name'), get: (r) => r.customer_name, width: 210 },
    { key: 'area', title: t('customers.area'), get: (r) => r.area_name ?? '', width: 130 },
    { key: 'box', title: t('net.box'), get: (r) => r.box_code ?? '', width: 80 },
    { key: 'meter', title: t('invoices.meter'), get: (r) => r.meter_no ?? '', width: 90 },
    { key: 'due', title: t('bulk.totalDue'), get: (r) => Number(r.balance) + Number(r.previous_balance),
      render: (r) => `${formatAmount(Number(r.balance) + Number(r.previous_balance), decimalsOf(r.currency))} ${r.currency}`, num: true, filter: false, width: 140 },
  ];
  const rtl = i18n.language === 'ar';
  const companyName = data ? (rtl && data.company.name_ar ? data.company.name_ar : data.company.name_en) : '';

  return (
    <div className="mx-col">
      <div className="mx-strip">
        <label>{t('invoices.period')} <input type="month" value={period} onChange={(e) => setPeriod(e.currentTarget.value)} /></label>
        <label>{t('customers.area')} <select value={area} onChange={(e) => setArea(e.currentTarget.value)}>
          <option value="">{t('readings.allAreas')}</option>{areas.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}</select></label>
        <div style={{ width: 160 }}><StationSelect size="xs" value={station} onChange={setStation} /></div>
        <label className="mx-check"><input type="checkbox" checked={unpaid} onChange={(e) => setUnpaid(e.currentTarget.checked)} /> {t('invoices.unpaid')}</label>
        <label>{t('ax.pb.order')} <select value={order} onChange={(e) => setOrder(e.currentTarget.value as typeof order)}>
          <option value="area">{t('ax.pb.byRoute')}</option><option value="number">{t('ax.pb.byNumber')}</option><option value="name">{t('ax.pb.byName')}</option></select></label>
        <label>{t('ax.pb.layout')} <select value={layout} onChange={(e) => setLayout(e.currentTarget.value as 'a4' | 'thermal')}>
          <option value="a4">{t('bulk.a4')}</option><option value="thermal">{t('bulk.thermal')}</option></select></label>
        <span className="mx-sp" />
        <button className="mx-btn" onClick={() => setPicked(new Set(rows.map((r) => r.id)))}>{t('ax.pb.all')}</button>
        <button className="mx-btn" onClick={() => setPicked(new Set())}>{t('ax.pb.none')}</button>
        <button className="mx-btn main" style={{ padding: '4px 16px', fontSize: 14 }} disabled={!picked.size} onClick={() => void print()}>🖨 {t('invoices.print')} ({picked.size})</button>
        <Help topic="printBills" />
      </div>
      <div className="mx-body" style={{ gridTemplateColumns: '1fr' }}>
        <section className="mx-box">
          <h3>{t('mx.t.printBills')} <small>{t('ax.pb.hint')}</small></h3>
          <DataGrid rows={rows} cols={cols} rowKey={(r) => r.id} loading={loading} title={`${t('mx.t.printBills')} ${period}`}
            onActivate={(r) => toggle(r.id)} />
        </section>
      </div>
      {printing && data && createPortal(
        <div className="mx-printhost"><BillSheets rows={rows.filter((r) => picked.has(r.id))} qr={qr} layout={layout} companyName={companyName} rtl={rtl} /></div>,
        document.body)}
    </div>
  );
}
