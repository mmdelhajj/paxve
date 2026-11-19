import { printPage } from '../../native';
import { useEffect, useState } from 'react';
import { Anchor, Box, Button, Center, Checkbox, Group, Loader, Paper, SegmentedControl, Select, Text, TextInput } from '@mantine/core';
import { IconArrowBack, IconPrinter } from '@tabler/icons-react';
import QRCode from 'qrcode';
import { useTranslation } from 'react-i18next';
import { api } from '../../api';
import { useCompany } from '../../company';
import { formatAmount, formatDate, notifyError } from '../../util';
import StationSelect from '../../components/StationSelect';
import { currentPeriod, type Area } from '../../billing';

interface Line { kind: string; description: string; amount: string; unit_price: string; tax_amount: string | null; meta: Record<string, string> }
export type BillRow = Row;
export interface BillData { company: { name_en: string; name_ar: string }; rows: Row[]; portal_base: string }
interface Row {
  id: string; number: number; period: string | null; issue_date: string; due_date: string; currency: string; total: string;
  paid: string; balance: string; status: string; local_currency: string | null; local_rate: string | null; local_currency_total: string | null;
  customer_code: string; customer_name: string; customer_phone: string; customer_address: string; portal_token: string;
  area_name: string | null; meter_no: string | null; box_code: string | null; lines: Line[] | null; previous_balance: string;
}
interface Data { company: { name_en: string; name_ar: string }; rows: Row[]; portal_base: string }

/** Printable sheet of many bills (A4, 3 per page, or 80 mm thermal roll) for collectors. */
export default function BulkPrint({ initialPeriod }: { initialPeriod?: string }) {
  const { t, i18n } = useTranslation();
  const { company } = useCompany();
  const [period, setPeriod] = useState(initialPeriod && /^\d{4}-\d{2}$/.test(initialPeriod) ? initialPeriod : currentPeriod());
  const [area, setArea] = useState<string | null>(null);
  const [station, setStation] = useState<string | null>(null);
  const [unpaid, setUnpaid] = useState(true);
  const [layout, setLayout] = useState<'a4' | 'thermal'>('a4');
  const [areas, setAreas] = useState<Area[]>([]);
  const [data, setData] = useState<Data | null>(null);
  const [qr, setQr] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (company) api<Area[]>(`/companies/${company.id}/areas`).then(setAreas).catch(() => {});
  }, [company]);

  async function load() {
    if (!company) return;
    setBusy(true);
    setData(null);
    try {
      const p = new URLSearchParams({ period, area_id: area ?? '', station_id: station ?? '', unpaid: unpaid ? '1' : '' });
      const d = await api<Data>(`/companies/${company.id}/invoices/print?${p}`);
      const codes: Record<string, string> = {};
      for (const r of d.rows) {
        codes[r.id] = await QRCode.toDataURL(d.portal_base + r.portal_token, { margin: 0, width: 120, errorCorrectionLevel: 'M' });
      }
      setQr(codes);
      setData(d);
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => { void load(); /* first load */ // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [company]);

  if (!company) return null;
  const rtl = i18n.language === 'ar';
  const companyName = data ? (rtl && data.company.name_ar ? data.company.name_ar : data.company.name_en) : '';

  return (
    <Box p="md" bg="var(--mantine-color-gray-0)" mih="100vh">
      <Paper withBorder p="sm" mb="md" className="no-print">
        <Group justify="space-between" wrap="wrap" gap="sm">
          <Anchor href="#/invoices" size="sm"><Group gap={4}><IconArrowBack size={14} />{t('invoices.title')}</Group></Anchor>
          <Group gap="xs" wrap="wrap" align="flex-end">
            <TextInput type="month" size="xs" label={t('invoices.period')} value={period} onChange={(e) => setPeriod(e.currentTarget.value)} />
            <Select size="xs" w={160} label={t('customers.area')} placeholder={t('readings.allAreas')} clearable value={area} onChange={setArea}
              data={areas.map((a) => ({ value: a.id, label: a.name }))} />
            <StationSelect size="xs" w={160} label={t('net.station')} value={station} onChange={setStation} />
            <Checkbox size="xs" label={t('invoices.unpaid')} checked={unpaid} onChange={(e) => setUnpaid(e.currentTarget.checked)} mb={6} />
            <SegmentedControl size="xs" value={layout} onChange={(v) => setLayout(v as 'a4' | 'thermal')}
              data={[{ value: 'a4', label: t('bulk.a4') }, { value: 'thermal', label: t('bulk.thermal') }]} />
            <Button size="xs" variant="light" onClick={load} loading={busy}>{t('bulk.load')}</Button>
            <Button size="xs" leftSection={<IconPrinter size={14} />} disabled={!data?.rows.length} onClick={() => void printPage()}>
              {t('invoices.print')} {data ? `(${data.rows.length})` : ''}
            </Button>
          </Group>
        </Group>
      </Paper>

      {busy ? <Center p="xl"><Loader /></Center> : data && data.rows.length === 0 ? (
        <Text c="dimmed" ta="center" p="xl">{t('common.empty')}</Text>
      ) : data && <BillSheets rows={data.rows} qr={qr} layout={layout} companyName={companyName} rtl={rtl} />}
    </Box>
  );
}

/** The printed bills (A4 three per page, or 80 mm roll), shared by the bulk print page and the الإيصالات print screen. */
export function BillSheets({ rows, qr, layout, companyName, rtl }: { rows: BillRow[]; qr: Record<string, string>; layout: 'a4' | 'thermal'; companyName: string; rtl: boolean }) {
  const { t } = useTranslation();
  const { decimalsOf } = useCompany();
  return (
    <div className={`print-doc bulk ${layout}`} dir={rtl ? 'rtl' : 'ltr'}>
      {rows.map((r) => {
        const dec = decimalsOf(r.currency);
        const prev = Number(r.previous_balance);
        return (
          <div className="bill" key={r.id}>
            <div className="bill-head">
              <div>
                <strong style={{ fontSize: 15 }}>{companyName}</strong>
                <div>{t('invoices.invoice')} #{r.number}{r.period ? ` · ${r.period}` : ''} · {formatDate(r.issue_date)}</div>
                <div><strong>{r.customer_name}</strong> ({r.customer_code}){r.customer_phone ? ` · ${r.customer_phone}` : ''}</div>
                <div className="muted">{[r.area_name, r.box_code && `${t('net.box')} ${r.box_code}`, r.meter_no && `${t('invoices.meter')} ${r.meter_no}`].filter(Boolean).join(' · ')}</div>
              </div>
              {qr[r.id] && <img src={qr[r.id]} alt="" className="qr" />}
            </div>
            <table>
              <tbody>
                {(r.lines ?? []).map((l, i) => (
                  <tr key={i}>
                    <td>{l.kind === 'usage' && l.meta?.previous
                      ? <span dir="ltr">{l.meta.previous} → {l.meta.current} = {l.meta.used} {l.meta.unit} × {Number(l.unit_price)}</span>
                      : l.description}</td>
                    <td className="num">{formatAmount(l.amount, dec)}</td>
                  </tr>
                ))}
                {Number(r.paid) > 0 && <tr><td>{t('invoices.paid')}</td><td className="num">-{formatAmount(r.paid, dec)}</td></tr>}
                {prev > 0 && <tr><td>{t('bulk.previous')}</td><td className="num">{formatAmount(prev, dec)}</td></tr>}
              </tbody>
              <tfoot>
                <tr><th>{t('bulk.totalDue')}</th><th className="num">{formatAmount(Number(r.balance) + prev, dec)} {r.currency}</th></tr>
                {r.local_currency_total && r.local_currency && r.local_currency !== r.currency && (
                  <tr><td>{t('bulk.inLocal', { cur: r.local_currency, rate: r.local_rate })}</td>
                    <td className="num">{formatAmount(r.local_currency_total, decimalsOf(r.local_currency))} {r.local_currency}</td></tr>
                )}
              </tfoot>
            </table>
            <div className="muted small">{t('invoices.due')}: {formatDate(r.due_date)} · {t('bulk.scan')}</div>
          </div>
        );
      })}
    </div>
  );
}
