import { useEffect, useState } from 'react';
import { useDebouncedValue } from '@mantine/hooks';
import { useTranslation } from 'react-i18next';
import { api, can, type Me } from '../../api';
import { useCompany } from '../../company';
import { openScreen } from '../../nav';
import { formatAmount, formatDate, notifyError } from '../../util';
import { currentPeriod, type Invoice } from '../../billing';
import DataGrid, { type Col } from '../../components/DataGrid';
import StationSelect from '../../components/StationSelect';
import { InvoiceView } from '../billing/docs';
import { RunBilling } from '../billing/Invoices';
import { Help } from '../acc/common';
import { useActivations } from '../../paneActive';

interface Totals { currency: string; count: number; total: string; paid: string; balance: string }

/** الايصالات الشهرية: the month's bills in one grid, the billing run and bulk printing one click away. */
export default function MonthlyBills({ me }: { me: Me }) {
  const { t } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [period, setPeriod] = useState(currentPeriod());
  const [status, setStatus] = useState('');
  const [station, setStation] = useState<string | null>(null);
  const [q, setQ] = useState('');
  const [dq] = useDebouncedValue(q, 300);
  const [data, setData] = useState<{ rows: Invoice[]; totals: Totals[] } | null>(null);
  const [loading, setLoading] = useState(false);
  const [view, setView] = useState<string | null>(null);
  const [runOpen, setRunOpen] = useState(false);
  const [n, setN] = useState(0);
  const canRun = can(me, 'billing.create');
  const cid = company?.id;
  const act = useActivations();

  useEffect(() => {
    if (!cid) return;
    setLoading(true);
    const p = new URLSearchParams({ period: /^\d{4}-\d{2}$/.test(period) ? period : '', status, q: dq, station_id: station ?? '', limit: '5000' });
    api<{ rows: Invoice[]; totals: Totals[] }>(`/companies/${cid}/invoices?${p}`).then(setData)
      .catch((e) => { notifyError(t, e); setData(null); }).finally(() => setLoading(false));
  }, [cid, period, status, dq, station, n, act, t]);
  if (!company) return null;

  const cols: Col<Invoice>[] = [
    { key: 'num', title: t('invoices.number'), get: (r) => r.number, width: 80 },
    { key: 'name', title: t('customers.name'), get: (r) => r.customer_name, width: 220 },
    { key: 'code', title: t('customers.code'), get: (r) => r.customer_code, width: 80 },
    { key: 'per', title: t('invoices.period'), get: (r) => r.period ?? '', width: 80 },
    { key: 'date', title: t('invoices.date'), get: (r) => r.issue_date, render: (r) => formatDate(r.issue_date), width: 105 },
    { key: 'due', title: t('invoices.due'), get: (r) => r.due_date, render: (r) => formatDate(r.due_date), width: 105 },
    { key: 'cur', title: t('mx.d.currency'), get: (r) => r.currency, width: 60 },
    { key: 'tot', title: t('invoices.total'), get: (r) => r.total, render: (r) => formatAmount(r.total, decimalsOf(r.currency)), num: true, filter: false, width: 110, total: 'sum' },
    { key: 'paid', title: t('invoices.paid'), get: (r) => r.paid, render: (r) => formatAmount(r.paid, decimalsOf(r.currency)), num: true, filter: false, width: 110, total: 'sum' },
    { key: 'bal', title: t('invoices.balance'), get: (r) => (r.status === 'void' ? 0 : r.balance), render: (r) => (r.status === 'void' ? '—' : formatAmount(r.balance, decimalsOf(r.currency))),
      num: true, filter: false, width: 110, total: 'sum', cls: (r) => (Number(r.balance) > 0 && r.status !== 'void' ? 'red' : 'ok') },
    { key: 'st', title: t('invoices.status'), get: (r) => t(`invoices.statuses.${r.status}`), width: 90,
      render: (r) => <span className={`mx-pill ${r.status === 'paid' ? 'on' : r.status === 'void' ? '' : 'off'}`}>{t(`invoices.statuses.${r.status}`)}</span> },
  ];

  return (
    <div className="mx-col">
      <div className="mx-strip">
        <label>{t('invoices.period')} <input type="month" value={period} onChange={(e) => setPeriod(e.currentTarget.value)} /></label>
        <label>{t('invoices.status')} <select value={status} onChange={(e) => setStatus(e.currentTarget.value)}>
          <option value="">{t('common.all')}</option><option value="unpaid">{t('invoices.unpaid')}</option>
          <option value="paid">{t('invoices.statuses.paid')}</option><option value="void">{t('invoices.statuses.void')}</option></select></label>
        <div style={{ width: 170 }}><StationSelect size="xs" value={station} onChange={setStation} /></div>
        <label>{t('ax.search')} <input value={q} onChange={(e) => setQ(e.currentTarget.value)} placeholder={t('customers.search')} /></label>
        <span className="mx-sp" />
        {canRun && <button className="mx-btn" onClick={() => setRunOpen(true)}>⚡ {t('billing.run')}</button>}
        <button className="mx-btn" onClick={() => openScreen('print-bills', period)}>🖨 {t('mx.t.printBills')}</button>
        <Help topic="monthly" />
      </div>
      {data && data.totals.length > 0 && (
        <div className="mx-sum">
          {data.totals.map((x) => (
            <div key={x.currency}><span>{x.count} · {x.currency}</span>
              <b dir="ltr">{formatAmount(x.total, decimalsOf(x.currency))}</b>
              <b dir="ltr" className="ok">{formatAmount(x.paid, decimalsOf(x.currency))}</b>
              <b dir="ltr" className="red">{formatAmount(x.balance, decimalsOf(x.currency))}</b></div>
          ))}
        </div>
      )}
      <div className="mx-body" style={{ gridTemplateColumns: '1fr' }}>
        <section className="mx-box">
          <h3>{t('mx.t.monthly')} <small>{t('ax.mb.hint')}</small></h3>
          <DataGrid rows={data?.rows ?? []} cols={cols} rowKey={(r) => r.id} loading={loading} footer title={`${t('mx.t.monthly')} ${period}`}
            onActivate={(r) => setView(r.id)} rowClass={(r) => (r.status === 'void' ? 'muted' : '')} />
        </section>
      </div>
      {view && <InvoiceView id={view} canVoid={canRun} onClose={() => setView(null)} onChanged={() => setN((x) => x + 1)} />}
      {runOpen && <RunBilling defaultPeriod={period} onClose={() => setRunOpen(false)} onDone={(p) => { setPeriod(p); setN((x) => x + 1); }} />}
    </div>
  );
}
