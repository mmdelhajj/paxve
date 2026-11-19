import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useCompany } from '../../company';
import { openScreen } from '../../nav';
import { formatAmount, formatDate, today } from '../../util';
import DataGrid, { type Col } from '../../components/DataGrid';
import { Help, useLoad } from './common';
import { openLedger } from './Statements';

// ---------------- cash flow ----------------

interface CFRow { section: string; account_id: string | null; code: string | null; name: string | null; name_i18n: Record<string, string> | null; amount: string; entries: number }

/** التدفقات النقدية: where the cash came from and where it went, by activity. */
export function CashFlow() {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [from, setFrom] = useState(() => `${new Date().getFullYear()}-01-01`);
  const [to, setTo] = useState(today());
  const cid = company?.id;
  const [data, , loading] = useLoad<{ rows: CFRow[]; opening: string; closing: string }>(cid ? `/companies/${cid}/reports/cash-flow?from=${from}&to=${to}` : null);
  const order = ['operating', 'investing', 'financing', 'transfer'];
  const rows = useMemo(() => [...(data?.rows ?? [])].sort((a, b) => order.indexOf(a.section) - order.indexOf(b.section)),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [data]);
  if (!company) return null;
  const dec = decimalsOf(company.base_currency);
  const sum = (s: string) => rows.filter((r) => r.section === s).reduce((x, r) => x + Number(r.amount), 0);
  const cols: Col<CFRow>[] = [
    { key: 's', title: t('ax.cf.section'), get: (r) => t(`ax.cf.${r.section}`), width: 150 },
    { key: 'c', title: t('accounts.code'), get: (r) => r.code ?? '', width: 80 },
    { key: 'n', title: t('ax.cf.what'), get: (r) => (r.name ? r.name_i18n?.[i18n.language] || r.name : t('ax.cf.betweenBoxes')), width: 280 },
    { key: 'e', title: t('ax.cf.entries'), get: (r) => r.entries, num: true, decimals: 0, filter: false, width: 80 },
    { key: 'a', title: t('ax.amount'), get: (r) => r.amount, num: true, decimals: dec, total: 'sum', filter: false, width: 150, cls: (r) => (Number(r.amount) < 0 ? 'red' : 'ok') },
  ];
  const items = [
    { k: 'opening', v: Number(data?.opening ?? 0) }, { k: 'operating', v: sum('operating') }, { k: 'investing', v: sum('investing') },
    { k: 'financing', v: sum('financing') }, { k: 'closing', v: Number(data?.closing ?? 0) },
  ];
  return (
    <div className="mx-col">
      <div className="mx-strip">
        <label>{t('reports.from')} <input type="date" value={from} onChange={(e) => setFrom(e.currentTarget.value)} /></label>
        <label>{t('reports.to')} <input type="date" value={to} onChange={(e) => setTo(e.currentTarget.value)} /></label>
        <span>{t('reports.amountsIn', { cur: company.base_currency })}</span>
        <span className="mx-sp" />
        <Help topic="cashFlow" />
      </div>
      {data && <div className="mx-sum">{items.map((x) => <div key={x.k}><span>{t(`ax.cf.${x.k}`)}</span><b dir="ltr" className={x.v < 0 ? 'red' : ''}>{formatAmount(x.v, dec)}</b></div>)}</div>}
      <div className="mx-body" style={{ gridTemplateColumns: '1fr' }}>
        <section className="mx-box">
          <h3>{t('ax.t.cashFlow')} <small>{t('ax.dblClickLedger')}</small></h3>
          <DataGrid rows={rows} cols={cols} rowKey={(r) => r.section + (r.account_id ?? '')} loading={loading} footer title={`${t('ax.t.cashFlow')} ${from} → ${to}`}
            onActivate={(r) => { if (r.account_id) openLedger(r.account_id, from, to); }} />
        </section>
      </div>
    </div>
  );
}

// ---------------- period comparison ----------------

interface CmpRow { account_id: string; code: string; name: string; name_i18n: Record<string, string> | null; type: string; values: string[] }

function monthRange(y: number, m: number): string {
  const last = new Date(y, m, 0).getDate();
  return `${y}-${String(m).padStart(2, '0')}-01:${y}-${String(m).padStart(2, '0')}-${last}`;
}

/** مقارنة الفترات: this month against last month / the same month last year, this year against last year. */
export function Compare({ initial }: { initial?: string }) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [kind, setKind] = useState<'pl' | 'bs'>(initial === 'bs' ? 'bs' : 'pl');
  const [mode, setMode] = useState<'mm' | 'my' | 'yy' | 'q'>('mm');
  const now = new Date();
  const [y, setY] = useState(now.getFullYear());
  const [m, setM] = useState(now.getMonth() + 1);
  const periods = useMemo(() => {
    const prev = (yy: number, mm: number) => (mm === 1 ? [yy - 1, 12] : [yy, mm - 1]);
    if (kind === 'bs') {
      const end = (yy: number, mm: number) => monthRange(yy, mm).split(':')[1];
      if (mode === 'yy') return [`${y}-12-31`, `${y - 1}-12-31`];
      if (mode === 'my') return [end(y, m), end(y - 1, m)];
      const [py, pm] = prev(y, m);
      return [end(y, m), end(py, pm)];
    }
    if (mode === 'yy') return [`${y}-01-01:${y}-12-31`, `${y - 1}-01-01:${y - 1}-12-31`];
    if (mode === 'my') return [monthRange(y, m), monthRange(y - 1, m)];
    if (mode === 'q') {
      const out: string[] = [];
      let yy = y, mm = m;
      for (let i = 0; i < 6; i++) { out.push(monthRange(yy, mm)); [yy, mm] = prev(yy, mm); }
      return out;
    }
    const [py, pm] = prev(y, m);
    return [monthRange(y, m), monthRange(py, pm)];
  }, [kind, mode, y, m]);
  const cid = company?.id;
  const [data, , loading] = useLoad<{ rows: CmpRow[]; periods: { From: string; To: string }[] }>(
    cid ? `/companies/${cid}/reports/compare?kind=${kind}&${periods.map((p) => `p=${p}`).join('&')}` : null);
  if (!company) return null;
  const dec = decimalsOf(company.base_currency);
  const label = (p: string) => {
    const [f, to] = p.includes(':') ? p.split(':') : ['', p];
    if (!f) return formatDate(to);
    if (f.slice(5) === '01-01' && to.slice(5) === '12-31') return f.slice(0, 4);
    return new Intl.DateTimeFormat(i18n.language === 'ar' ? 'ar-LB' : i18n.language, { month: 'short', year: 'numeric', numberingSystem: 'latn' }).format(new Date(f + 'T00:00:00'));
  };
  const cols: Col<CmpRow>[] = [
    { key: 'ty', title: t('accounts.type'), get: (r) => t(`accounts.types.${r.type}`), width: 90 },
    { key: 'co', title: t('accounts.code'), get: (r) => r.code, width: 80 },
    { key: 'na', title: t('accounts.name'), get: (r) => r.name_i18n?.[i18n.language] || r.name, width: 240 },
    ...periods.map((p, i): Col<CmpRow> => ({ key: `v${i}`, title: label(p), get: (r) => r.values[i], num: true, decimals: dec, filter: false, width: 120,
      total: (rs) => formatAmount(rs.reduce((s, r) => s + (r.type === 'expense' && kind === 'pl' ? -1 : 1) * Number(r.values[i]), 0), dec) })),
    { key: 'ch', title: t('ax.cmp.change'), get: (r) => Number(r.values[0]) - Number(r.values[1] ?? 0), num: true, decimals: dec, filter: false, width: 120,
      cls: (r) => ((Number(r.values[0]) - Number(r.values[1] ?? 0)) * (r.type === 'expense' ? -1 : 1) < 0 ? 'red' : 'ok') },
    { key: 'pc', title: '%', get: (r) => (Number(r.values[1]) ? (((Number(r.values[0]) - Number(r.values[1])) / Math.abs(Number(r.values[1]))) * 100).toFixed(0) : ''),
      num: true, decimals: 0, filter: false, width: 70 },
  ];
  const order: Record<string, number> = { income: 0, expense: 1, asset: 0, liability: 1, equity: 2 };
  const rows = [...(data?.rows ?? [])].sort((a, b) => (order[a.type] - order[b.type]) || 0);
  return (
    <div className="mx-col">
      <div className="mx-modes" role="tablist">
        {(['pl', 'bs'] as const).map((k) => <button key={k} className="mx-mode" role="tab" aria-selected={kind === k} onClick={() => setKind(k)}>{t(`ax.cmp.kind.${k}`)}</button>)}
      </div>
      <div className="mx-strip">
        <label>{t('ax.cmp.mode')} <select value={mode} onChange={(e) => setMode(e.currentTarget.value as typeof mode)}>
          {(['mm', 'my', 'yy', ...(kind === 'pl' ? ['q'] : [])] as const).map((x) => <option key={x} value={x}>{t(`ax.cmp.modes.${x}`)}</option>)}</select></label>
        <label>{t('ax.fy.year')} <input type="number" value={y} style={{ width: 80 }} onChange={(e) => setY(Number(e.currentTarget.value) || now.getFullYear())} /></label>
        {mode !== 'yy' && <label>{t('ax.bu.month')} <select value={m} onChange={(e) => setM(Number(e.currentTarget.value))}>
          {Array.from({ length: 12 }, (_, i) => i + 1).map((x) => <option key={x} value={x}>{x}</option>)}</select></label>}
        <span className="mx-sp" />
        <Help topic="compare" />
      </div>
      <div className="mx-body" style={{ gridTemplateColumns: '1fr' }}>
        <section className="mx-box">
          <h3>{t(`ax.cmp.kind.${kind}`)} <small>{t('ax.cmp.hint')}</small></h3>
          <DataGrid rows={rows} cols={cols} rowKey={(r) => r.account_id} loading={loading} footer title={t(`ax.cmp.kind.${kind}`)}
            onActivate={(r) => { if (r.account_id !== 'result') openLedger(r.account_id, periods[0].includes(':') ? periods[0].split(':')[0] : '0001-01-01', periods[0].split(':').pop()!); }} />
        </section>
      </div>
    </div>
  );
}

// ---------------- owner summary (plain words) ----------------

interface Amt { currency: string; amount: string }
interface Summary {
  base_currency: string;
  collected_today: (Amt & { payments: number })[];
  collectors_cash: { id: string; name: string; currency: string; user_name: string; balance: string }[];
  company_cash: { id: string; name: string; currency: string; balance: string }[];
  customers_owe: (Amt & { customers: number; over60: string; customers_over60: number })[];
  suppliers_owed: (Amt & { suppliers: number })[];
  month: { income: string; expense: string; from: string };
  prev_same_period: { income: string; expense: string };
  prev_month: { income: string; expense: string };
  fuel: { id: string; name: string; unit: string; on_hand: string; per_day: string; days_left: number | null }[];
  attention: { approvals: number; cheques_due: number; cheques_bounced: number; drafts: number; books_unlocked: number };
}

/** ملخص صاحب العمل: the day's important numbers, in sentences, each one a link to the detail. */
export function OwnerSummary() {
  const { t } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const cid = company?.id;
  const [s] = useLoad<Summary>(cid ? `/companies/${cid}/owner-summary` : null);
  if (!company || !s) return null;
  const money = (xs: Amt[]) => xs.filter((x) => Number(x.amount)).map((x) => `${formatAmount(x.amount, decimalsOf(x.currency))} ${x.currency}`).join(' + ') || '0';
  const base = s.base_currency;
  const dec = decimalsOf(base);
  const net = Number(s.month.income) - Number(s.month.expense);
  const prevNet = Number(s.prev_same_period.income) - Number(s.prev_same_period.expense);
  const collectors = s.collectors_cash.filter((c) => Number(c.balance) > 0);
  const owe = s.customers_owe;
  const custN = owe.reduce((n, x) => n + x.customers, 0);
  const over = owe.filter((x) => Number(x.over60) > 0);
  const Link = ({ to, children }: { to: string; children: React.ReactNode }) => {
    const [p, a] = to.split('/');
    return <button type="button" className="mx-link" onClick={() => openScreen(p, a ?? '')}>{children}</button>;
  };
  const att = s.attention;
  return (
    <div className="mx-owner">
      <div className="mx-owner-head"><b>{t('ax.own.title')}</b><Help topic="owner" /></div>
      <ul>
        <li>{t('ax.own.today', { amount: money(s.collected_today), n: s.collected_today.reduce((n, x) => n + x.payments, 0) })} <Link to="collections">{t('ax.own.see')}</Link></li>
        <li>{collectors.length
          ? <>{t('ax.own.collectorsHold')} {collectors.map((c, i) => <span key={c.id}>{i > 0 ? '، ' : ''}<b>{c.user_name}</b> {formatAmount(c.balance, decimalsOf(c.currency))} {c.currency}</span>)}. <Link to="jibaya/col">{t('ax.own.settle')}</Link></>
          : t('ax.own.collectorsNone')}</li>
        <li>{t('ax.own.boxes', { amount: money(s.company_cash.map((c) => ({ currency: c.currency, amount: c.balance }))) })} <Link to="cash">{t('ax.own.see')}</Link></li>
        <li>{custN ? t('ax.own.customersOwe', { n: custN, amount: money(owe) }) : t('ax.own.customersNone')}
          {over.length > 0 && <> {t('ax.own.over60', { n: over.reduce((n, x) => n + x.customers_over60, 0), amount: money(over.map((x) => ({ currency: x.currency, amount: x.over60 }))) })}</>}{' '}
          <Link to="aging">{t('ax.own.see')}</Link></li>
        {s.suppliers_owed.length > 0 && <li>{t('ax.own.suppliers', { amount: money(s.suppliers_owed) })} <Link to="ap-aging">{t('ax.own.see')}</Link></li>}
        <li>{net >= 0 ? t('ax.own.profit', { amount: `${formatAmount(net, dec)} ${base}` }) : t('ax.own.loss', { amount: `${formatAmount(-net, dec)} ${base}` })}{' '}
          {prevNet !== 0 || net !== 0 ? (net >= prevNet ? t('ax.own.better', { amount: `${formatAmount(net - prevNet, dec)} ${base}` }) : t('ax.own.worse', { amount: `${formatAmount(prevNet - net, dec)} ${base}` })) : ''}{' '}
          <Link to="profit-loss">{t('ax.own.see')}</Link></li>
        {s.fuel.map((f) => (
          <li key={f.id} className={f.days_left !== null && f.days_left < 5 ? 'warn' : ''}>
            {f.days_left === null ? t('ax.own.fuelNoUse', { name: f.name, qty: formatAmount(f.on_hand, 0), unit: f.unit })
              : t('ax.own.fuel', { name: f.name, qty: formatAmount(f.on_hand, 0), unit: f.unit, per: formatAmount(f.per_day, 0), days: f.days_left })} <Link to="stock">{t('ax.own.see')}</Link></li>
        ))}
      </ul>
      {(att.approvals + att.cheques_due + att.cheques_bounced + att.drafts + att.books_unlocked) > 0 && (
        <div className="mx-attn">
          <b>{t('ax.own.attention')}</b>
          {att.approvals > 0 && <Link to="approvals">{t('ax.own.approvals', { n: att.approvals })}</Link>}
          {att.cheques_due > 0 && <Link to="cheques">{t('ax.own.chequesDue', { n: att.cheques_due })}</Link>}
          {att.cheques_bounced > 0 && <Link to="cheques">{t('ax.own.chequesBounced', { n: att.cheques_bounced })}</Link>}
          {att.drafts > 0 && <Link to="journal">{t('ax.own.drafts', { n: att.drafts })}</Link>}
          {att.books_unlocked > 0 && <Link to="fiscal-years">{t('ax.own.unlocked')}</Link>}
        </div>
      )}
    </div>
  );
}
