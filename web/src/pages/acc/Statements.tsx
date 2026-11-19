import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useCompany } from '../../company';
import { openScreen } from '../../nav';
import { formatAmount, formatDate, today } from '../../util';
import DataGrid, { type Col } from '../../components/DataGrid';
import { Help, useLoad } from './common';

const yearStart = () => `${new Date().getFullYear()}-01-01`;

/** opens the ledger of an account for a period (drill-down from any statement) */
export function openLedger(accountId: string, from: string, to: string) {
  openScreen('ledger', `${accountId}~${from}~${to}`);
}

function Strip({ children, topic }: { children: React.ReactNode; topic: string }) {
  return <div className="mx-strip">{children}<span className="mx-sp" /><Help topic={topic} /></div>;
}

/** presentation currency: statements are kept in the base currency and can be shown in any other one */
function PresentIn({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const { t } = useTranslation();
  const { company, currencies } = useCompany();
  if (!company) return null;
  return (
    <label>{t('t2.showIn')} <select value={value || company.base_currency} onChange={(e) => onChange(e.currentTarget.value === company.base_currency ? '' : e.currentTarget.value)}>
      {currencies.map((c) => <option key={c.code} value={c.code}>{c.code}</option>)}
    </select></label>
  );
}

const inCur = (pc: string) => (pc ? `&currency=${pc}` : '');

function Summary({ items }: { items: { label: string; value: string; cls?: string }[] }) {
  return (
    <div className="mx-sum">
      {items.map((x) => <div key={x.label}><span>{x.label}</span><b className={x.cls} dir="ltr">{x.value}</b></div>)}
    </div>
  );
}

// ---------------- trial balance ----------------

interface TBRow { account_id: string; code: string; name: string; type: string; debit: string; credit: string; balance: string }

export function TrialBalanceGrid() {
  const { t } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [from, setFrom] = useState(yearStart());
  const [to, setTo] = useState(today());
  const [pc, setPc] = useState('');
  const cid = company?.id;
  const [data, , loading] = useLoad<{ rows: TBRow[]; total_debit: string; total_credit: string; base_currency: string }>(
    cid ? `/companies/${cid}/reports/trial-balance?from=${from}&to=${to}${inCur(pc)}` : null);
  if (!company) return null;
  const dec = decimalsOf(pc || company.base_currency);
  const cols: Col<TBRow>[] = [
    { key: 'code', title: t('accounts.code'), get: (r) => r.code, width: 90 },
    { key: 'name', title: t('accounts.name'), get: (r) => r.name, width: 300 },
    { key: 'type', title: t('accounts.type'), get: (r) => t(`accounts.types.${r.type}`), width: 110 },
    { key: 'dr', title: t('reports.debit'), get: (r) => r.debit, num: true, decimals: dec, total: 'sum', filter: false, width: 140 },
    { key: 'cr', title: t('reports.credit'), get: (r) => r.credit, num: true, decimals: dec, total: 'sum', filter: false, width: 140 },
    { key: 'bal', title: t('reports.balance'), get: (r) => r.balance, num: true, decimals: dec, filter: false, width: 150,
      cls: (r) => (Number(r.balance) < 0 ? 'red' : '') },
  ];
  const balanced = data && data.total_debit === data.total_credit;
  return (
    <div className="mx-col">
      <Strip topic="tb">
        <label>{t('reports.from')} <input type="date" value={from} onChange={(e) => setFrom(e.currentTarget.value)} /></label>
        <label>{t('reports.to')} <input type="date" value={to} onChange={(e) => setTo(e.currentTarget.value)} /></label>
        <PresentIn value={pc} onChange={setPc} />
        {data && <span className={`mx-pill ${balanced ? 'on' : 'off'}`}>{balanced ? t('ax.balanced') : t('ax.notBalanced')}</span>}
      </Strip>
      <div className="mx-body" style={{ gridTemplateColumns: '1fr' }}>
        <section className="mx-box">
          <h3>{t('mx.t.trialBalance')} <small>{t('ax.dblClickLedger')}</small></h3>
          <DataGrid rows={data?.rows ?? []} cols={cols} rowKey={(r) => r.account_id} footer loading={loading}
            title={`${t('mx.t.trialBalance')} ${from} → ${to}`} onActivate={(r) => openLedger(r.account_id, from, to)} />
        </section>
      </div>
    </div>
  );
}

// ---------------- profit & loss ----------------

interface PLRow { id: string; code: string; name: string; name_i18n: Record<string, string>; type: string; amount: string }

export function ProfitLossGrid() {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [from, setFrom] = useState(yearStart());
  const [to, setTo] = useState(today());
  const cid = company?.id;
  const [pc, setPc] = useState('');
  const [data, , loading] = useLoad<{ rows: PLRow[]; total_income: string; total_expense: string; net: string }>(
    cid ? `/companies/${cid}/reports/profit-loss?from=${from}&to=${to}${inCur(pc)}` : null);
  const rows = useMemo(() => [...(data?.rows ?? [])].sort((a, b) => (a.type === b.type ? a.code.localeCompare(b.code) : a.type === 'income' ? -1 : 1)), [data]);
  if (!company) return null;
  const cur = pc || company.base_currency;
  const dec = decimalsOf(cur);
  const nm = (r: PLRow) => r.name_i18n?.[i18n.language] || r.name;
  const cols: Col<PLRow>[] = [
    { key: 'type', title: t('accounts.type'), get: (r) => t(`accounts.types.${r.type}`), width: 110 },
    { key: 'code', title: t('accounts.code'), get: (r) => r.code, width: 90 },
    { key: 'name', title: t('accounts.name'), get: nm, width: 320 },
    { key: 'amt', title: t('ax.amount'), get: (r) => r.amount, num: true, decimals: dec, filter: false, width: 160,
      cls: (r) => (r.type === 'income' ? 'ok' : '') },
  ];
  const net = Number(data?.net ?? 0);
  return (
    <div className="mx-col">
      <Strip topic="pl">
        <label>{t('reports.from')} <input type="date" value={from} onChange={(e) => setFrom(e.currentTarget.value)} /></label>
        <label>{t('reports.to')} <input type="date" value={to} onChange={(e) => setTo(e.currentTarget.value)} /></label>
        <PresentIn value={pc} onChange={setPc} />
        <button className="mx-btn sm" onClick={() => openScreen('compare', 'pl')}>{t('ax.compareBtn')}</button>
      </Strip>
      {data && <Summary items={[
        { label: t('ax.income'), value: formatAmount(data.total_income, dec) },
        { label: t('ax.expenses'), value: formatAmount(data.total_expense, dec) },
        { label: net >= 0 ? t('ax.profit') : t('ax.loss'), value: `${formatAmount(Math.abs(net), dec)} ${cur}`, cls: net >= 0 ? 'ok' : 'red' },
      ]} />}
      <div className="mx-body" style={{ gridTemplateColumns: '1fr' }}>
        <section className="mx-box">
          <h3>{t('mx.t.pl')} <small>{t('ax.dblClickLedger')}</small></h3>
          <DataGrid rows={rows} cols={cols} rowKey={(r) => r.id} loading={loading} title={`${t('mx.t.pl')} ${from} → ${to}`}
            onActivate={(r) => openLedger(r.id, from, to)} />
        </section>
      </div>
    </div>
  );
}

// ---------------- balance sheet ----------------

export function BalanceSheetGrid() {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [to, setTo] = useState(today());
  const [pc, setPc] = useState('');
  const cid = company?.id;
  const [data, , loading] = useLoad<{ rows: PLRow[]; total_assets: string; total_liabilities: string; total_equity: string;
    current_result: string; total_liabilities_equity: string }>(cid ? `/companies/${cid}/reports/balance-sheet?to=${to}${inCur(pc)}` : null);
  const order: Record<string, number> = { asset: 0, liability: 1, equity: 2 };
  const rows = useMemo(() => [...(data?.rows ?? [])].sort((a, b) => (order[a.type] - order[b.type]) || a.code.localeCompare(b.code)),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [data]);
  if (!company) return null;
  const dec = decimalsOf(pc || company.base_currency);
  const nm = (r: PLRow) => r.name_i18n?.[i18n.language] || r.name;
  const cols: Col<PLRow>[] = [
    { key: 'type', title: t('accounts.type'), get: (r) => t(`accounts.types.${r.type}`), width: 110 },
    { key: 'code', title: t('accounts.code'), get: (r) => r.code, width: 90 },
    { key: 'name', title: t('accounts.name'), get: nm, width: 320 },
    { key: 'amt', title: t('ax.amount'), get: (r) => r.amount, num: true, decimals: dec, filter: false, width: 160, cls: (r) => (Number(r.amount) < 0 ? 'red' : '') },
  ];
  const ok = data && Math.abs(Number(data.total_assets) - Number(data.total_liabilities_equity)) < 0.005;
  return (
    <div className="mx-col">
      <Strip topic="bs">
        <label>{t('ax.asOf')} <input type="date" value={to} onChange={(e) => setTo(e.currentTarget.value)} /></label>
        <PresentIn value={pc} onChange={setPc} />
        <button className="mx-btn sm" onClick={() => openScreen('compare', 'bs')}>{t('ax.compareBtn')}</button>
        {data && <span className={`mx-pill ${ok ? 'on' : 'off'}`}>{ok ? t('ax.balanced') : t('ax.notBalanced')}</span>}
      </Strip>
      {data && <Summary items={[
        { label: t('ax.assets'), value: formatAmount(data.total_assets, dec) },
        { label: t('ax.liabilities'), value: formatAmount(data.total_liabilities, dec) },
        { label: t('ax.equity'), value: formatAmount(data.total_equity, dec) },
        { label: t('ax.currentResult'), value: formatAmount(data.current_result, dec), cls: Number(data.current_result) >= 0 ? 'ok' : 'red' },
        { label: t('ax.liabEquity'), value: formatAmount(data.total_liabilities_equity, dec) },
      ]} />}
      <div className="mx-body" style={{ gridTemplateColumns: '1fr' }}>
        <section className="mx-box">
          <h3>{t('mx.t.bs')} <small>{formatDate(to)} · {t('ax.dblClickLedger')}</small></h3>
          <DataGrid rows={rows} cols={cols} rowKey={(r) => r.id} loading={loading} title={`${t('mx.t.bs')} ${to}`}
            onActivate={(r) => openLedger(r.id, '0001-01-01', to)} />
        </section>
      </div>
    </div>
  );
}

// ---------------- customers ageing ----------------

interface AgingRow { customer_id: string; code: string; name: string; phone: string; area_name: string | null; currency: string;
  current: string; d30: string; d60: string; d90: string; over90: string; total: string; invoices: number; oldest_due: string }

export function AgingGrid() {
  const { t } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [asOf, setAsOf] = useState(today());
  const [limit, setLimit] = useState(500);
  const cid = company?.id;
  const [data, , loading] = useLoad<{ rows: AgingRow[]; has_more: boolean }>(cid ? `/companies/${cid}/reports/aging?as_of=${asOf}&limit=${limit}` : null);
  if (!company) return null;
  const m = (k: keyof AgingRow, title: string, w = 120): Col<AgingRow> => ({
    key: k, title, get: (r) => r[k] as string, render: (r) => (Number(r[k]) ? formatAmount(r[k] as string, decimalsOf(r.currency)) : ''),
    num: true, filter: false, width: w, total: 'sum',
  });
  const cols: Col<AgingRow>[] = [
    { key: 'code', title: t('customers.code'), get: (r) => r.code, width: 80 },
    { key: 'name', title: t('customers.name'), get: (r) => r.name, width: 220 },
    { key: 'area', title: t('customers.area'), get: (r) => r.area_name ?? '', width: 120 },
    { key: 'phone', title: t('customers.phone'), get: (r) => r.phone, width: 110 },
    { key: 'cur', title: t('mx.d.currency'), get: (r) => r.currency, width: 60 },
    m('current', t('ax.ag.current')), m('d30', t('ax.ag.d30')), m('d60', t('ax.ag.d60')), m('d90', t('ax.ag.d90')),
    { ...m('over90', t('ax.ag.over90')), cls: (r) => (Number(r.over90) > 0 ? 'red' : '') },
    { ...m('total', t('ax.ag.total'), 130), cls: () => 'red' },
    { key: 'n', title: t('ax.ag.bills'), get: (r) => r.invoices, num: true, decimals: 0, filter: false, width: 70 },
    { key: 'old', title: t('ax.ag.oldest'), get: (r) => r.oldest_due, render: (r) => formatDate(r.oldest_due), width: 110 },
  ];
  return (
    <div className="mx-col">
      <Strip topic="aging">
        <label>{t('ax.asOf')} <input type="date" value={asOf} onChange={(e) => setAsOf(e.currentTarget.value)} /></label>
        {data?.has_more && <button className="mx-btn sm" onClick={() => setLimit((l) => l + 500)}>{t('ax.showMore')}</button>}
      </Strip>
      <div className="mx-body" style={{ gridTemplateColumns: '1fr' }}>
        <section className="mx-box">
          <h3>{t('mx.t.aging')} <small>{t('ax.ag.hint')}</small></h3>
          <DataGrid rows={data?.rows ?? []} cols={cols} rowKey={(r) => r.customer_id + r.currency} loading={loading} footer
            title={`${t('mx.t.aging')} ${asOf}`} onActivate={(r) => openScreen('customer', r.customer_id)} />
        </section>
      </div>
    </div>
  );
}

// ---------------- collections ----------------

interface ByUser { user_id: string | null; user_name: string; currency: string; payments: number; amount: string; customers: number }
interface ByDay { date: string; currency: string; payments: number; amount: string }

export function CollectionsGrid() {
  const { t } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [from, setFrom] = useState(() => today().slice(0, 8) + '01');
  const [to, setTo] = useState(today());
  const cid = company?.id;
  const [data, , loading] = useLoad<{ by_user: ByUser[]; by_day: ByDay[] }>(cid ? `/companies/${cid}/reports/collections?from=${from}&to=${to}` : null);
  if (!company) return null;
  const uc: Col<ByUser>[] = [
    { key: 'u', title: t('ax.collector'), get: (r) => r.user_name, width: 200 },
    { key: 'c', title: t('mx.d.currency'), get: (r) => r.currency, width: 70 },
    { key: 'p', title: t('ax.receipts'), get: (r) => r.payments, num: true, decimals: 0, total: 'sum', filter: false, width: 90 },
    { key: 'n', title: t('ax.customersN'), get: (r) => r.customers, num: true, decimals: 0, filter: false, width: 90 },
    { key: 'a', title: t('ax.amount'), get: (r) => r.amount, render: (r) => formatAmount(r.amount, decimalsOf(r.currency)), num: true, filter: false, width: 150 },
  ];
  const dc: Col<ByDay>[] = [
    { key: 'd', title: t('journal.date'), get: (r) => r.date, render: (r) => formatDate(r.date), width: 120 },
    { key: 'c', title: t('mx.d.currency'), get: (r) => r.currency, width: 70 },
    { key: 'p', title: t('ax.receipts'), get: (r) => r.payments, num: true, decimals: 0, total: 'sum', filter: false, width: 90 },
    { key: 'a', title: t('ax.amount'), get: (r) => r.amount, render: (r) => formatAmount(r.amount, decimalsOf(r.currency)), num: true, filter: false, width: 150 },
  ];
  return (
    <div className="mx-col">
      <Strip topic="collections">
        <label>{t('reports.from')} <input type="date" value={from} onChange={(e) => setFrom(e.currentTarget.value)} /></label>
        <label>{t('reports.to')} <input type="date" value={to} onChange={(e) => setTo(e.currentTarget.value)} /></label>
      </Strip>
      <div className="mx-body" style={{ gridTemplateColumns: 'minmax(0, 1fr) minmax(0, 1fr)' }}>
        <section className="mx-box">
          <h3>{t('ax.byCollector')}</h3>
          <DataGrid rows={data?.by_user ?? []} cols={uc} rowKey={(r) => (r.user_id ?? '-') + r.currency} loading={loading} footer title={t('ax.byCollector')} />
        </section>
        <section className="mx-box">
          <h3>{t('ax.byDay')}</h3>
          <DataGrid rows={data?.by_day ?? []} cols={dc} rowKey={(r) => r.date + r.currency} loading={loading} footer title={t('ax.byDay')} />
        </section>
      </div>
    </div>
  );
}
