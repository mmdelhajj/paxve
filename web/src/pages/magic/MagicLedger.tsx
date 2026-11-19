import { useEffect, useMemo, useState } from 'react';
import { Select } from '@mantine/core';
import { useTranslation } from 'react-i18next';
import { api, type Account } from '../../api';
import { useCompany } from '../../company';
import { accountLabel, formatAmount, formatDate, notifyError, today } from '../../util';
import DataGrid, { type Col } from '../../components/DataGrid';
import { openScreen } from '../../nav';

interface Row {
  entry_id: string; number: number; date: string; memo: string; description: string; currency: string;
  debit: string; credit: string; base_debit: string; base_credit: string; balance: string; _k?: string;
}
interface LedgerData { base_currency: string; opening: string; rows: Row[] }

/** دفتر الأستاذ: one account's movements with the running balance. Pick the account at the top. */
export default function MagicLedger({ accountId }: { accountId: string }) {
  const { t } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [acc, setAcc] = useState<string | null>(accountId || null);
  const [from, setFrom] = useState(`${new Date().getFullYear()}-01-01`);
  const [to, setTo] = useState(today());
  const [data, setData] = useState<LedgerData | null>(null);
  const [loading, setLoading] = useState(false);
  const cid = company?.id;

  useEffect(() => { if (accountId) setAcc(accountId); }, [accountId]);
  useEffect(() => { if (cid) api<Account[]>(`/companies/${cid}/accounts`).then((a) => setAccounts(a.filter((x) => x.is_postable))).catch(() => {}); }, [cid]);
  useEffect(() => {
    if (!cid || !acc) { setData(null); return; }
    setLoading(true);
    api<LedgerData>(`/companies/${cid}/reports/ledger/${acc}?from=${from}&to=${to}`)
      .then(setData).catch((e) => { notifyError(t, e); setData(null); }).finally(() => setLoading(false));
  }, [cid, acc, from, to, t]);

  const dec = company ? decimalsOf(company.base_currency) : 2;
  const rows = useMemo(() => (data ? [
    { entry_id: '', number: 0, date: from, memo: t('reports.opening'), description: '', currency: data.base_currency, debit: '0', credit: '0',
      base_debit: '0', base_credit: '0', balance: data.opening } as Row, ...data.rows].map((r, i) => ({ ...r, _k: String(i) })) : []), [data, from, t]);
  if (!company) return null;
  const account = accounts.find((a) => a.id === acc);

  const cols: Col<Row>[] = [
    { key: 'date', title: t('journal.date'), get: (r) => r.date, render: (r) => formatDate(r.date), width: 110 },
    { key: 'number', title: t('journal.number'), get: (r) => r.number || '', width: 80 },
    { key: 'memo', title: t('journal.memo'), get: (r) => [r.memo, r.description].filter(Boolean).join(' · '), width: 360 },
    { key: 'fx', title: t('mx.lg.original'), get: (r) => (r.currency !== company.base_currency && r.entry_id ? `${formatAmount(Number(r.debit) || Number(r.credit), decimalsOf(r.currency))} ${r.currency}` : ''), width: 140 },
    { key: 'dr', title: t('reports.debit'), get: (r) => (Number(r.base_debit) ? r.base_debit : ''), num: true, decimals: dec, width: 120, total: 'sum', filter: false },
    { key: 'cr', title: t('reports.credit'), get: (r) => (Number(r.base_credit) ? r.base_credit : ''), num: true, decimals: dec, width: 120, total: 'sum', filter: false },
    { key: 'bal', title: t('reports.balance'), get: (r) => r.balance, num: true, decimals: dec, width: 130, filter: false,
      cls: (r) => (Number(r.balance) < 0 ? 'red' : ''), total: () => (data ? formatAmount(data.rows.length ? data.rows[data.rows.length - 1].balance : data.opening, dec) : '') },
  ];

  return (
    <>
      <div className="mx-strip">
        <div style={{ minWidth: 280, flex: '1 1 280px', maxWidth: 480 }}>
          <Select size="xs" searchable placeholder={t('mx.lg.pick')} aria-label={t('mx.lg.pick')} value={acc} onChange={setAcc}
            data={accounts.map((a) => ({ value: a.id, label: accountLabel(a) }))} nothingFoundMessage={t('common.empty')} />
        </div>
        <label>{t('reports.from')} <input type="date" value={from} onChange={(e) => setFrom(e.currentTarget.value)} /></label>
        <label>{t('reports.to')} <input type="date" value={to} onChange={(e) => setTo(e.currentTarget.value)} /></label>
        <span>{t('reports.amountsIn', { cur: company.base_currency })}</span>
      </div>
      <div className="mx-body" style={{ gridTemplateColumns: '1fr' }}>
        <section className="mx-box">
          <h3>{t('mx.t.ledger')} {account && <small>{accountLabel(account)}</small>}</h3>
          <DataGrid rows={rows} cols={cols} rowKey={(r) => r._k ?? ''}
            title={`${t('mx.t.ledger')} ${account ? accountLabel(account) : ''}`} loading={loading}
            empty={acc ? t('common.empty') : t('mx.lg.pick')} footer
            onActivate={(r) => { if (r.entry_id) openScreen('journal', r.entry_id); }} />
        </section>
      </div>
    </>
  );
}
