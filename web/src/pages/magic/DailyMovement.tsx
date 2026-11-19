import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, type EntrySummary } from '../../api';
import { useCompany } from '../../company';
import { formatDate, today } from '../../util';
import DataGrid, { type Col } from '../../components/DataGrid';
import { openScreen } from '../../nav';

/** الحركة اليومية: every journal voucher between two dates, newest first. Double-click opens the entry. */
export default function DailyMovement() {
  const { t } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [from, setFrom] = useState(today());
  const [to, setTo] = useState(today());
  const [status, setStatus] = useState('posted');
  const [rows, setRows] = useState<EntrySummary[] | null>(null);
  const cid = company?.id;

  useEffect(() => {
    if (!cid) return;
    let stop = false;
    setRows(null);
    (async () => {
      const all: EntrySummary[] = [];
      for (let off = 0; off < 20_000 && !stop; off += 500) {
        const page = await api<EntrySummary[]>(`/companies/${cid}/journal?from=${from}&to=${to}&status=${status}&limit=500&offset=${off}`).catch(() => [] as EntrySummary[]);
        all.push(...page);
        if (page.length < 500) break;
      }
      if (!stop) setRows(all);
    })();
    return () => { stop = true; };
  }, [cid, from, to, status]);

  if (!company) return null;
  const dec = decimalsOf(company.base_currency);
  const cols: Col<EntrySummary>[] = [
    { key: 'number', title: t('journal.number'), get: (r) => r.number ?? '', width: 80 },
    { key: 'date', title: t('journal.date'), get: (r) => r.date, render: (r) => formatDate(r.date), width: 110 },
    { key: 'memo', title: t('journal.memo'), get: (r) => r.memo, width: 380 },
    { key: 'ref', title: t('mx.dm.reference'), get: (r) => r.reference, width: 120 },
    { key: 'source', title: t('mx.dm.source'), get: (r) => t(`mx.dm.sources.${r.source}`, { defaultValue: r.source }), width: 120 },
    { key: 'total', title: t('mx.dm.total', { cur: company.base_currency }), get: (r) => r.total, num: true, decimals: dec, width: 130, total: 'sum', filter: false },
    { key: 'status', title: t('mx.d.status'), get: (r) => (r.reversed_by ? t('mx.dm.reversed') : r.reversal_of ? t('mx.dm.reversal') : r.status === 'draft' ? t('mx.dm.draft') : t('mx.d.posted')), width: 100,
      cls: (r) => (r.reversed_by || r.reversal_of ? 'muted' : '') },
  ];

  return (
    <>
      <div className="mx-strip">
        <label>{t('reports.from')} <input type="date" value={from} onChange={(e) => setFrom(e.currentTarget.value || today())} /></label>
        <label>{t('reports.to')} <input type="date" value={to} onChange={(e) => setTo(e.currentTarget.value || today())} /></label>
        <label>{t('mx.d.status')}
          <select value={status} onChange={(e) => setStatus(e.currentTarget.value)}>
            <option value="posted">{t('mx.d.posted')}</option>
            <option value="draft">{t('mx.dm.draft')}</option>
            <option value="">{t('mx.dm.all')}</option>
          </select>
        </label>
      </div>
      <div className="mx-body" style={{ gridTemplateColumns: '1fr' }}>
        <section className="mx-box">
          <h3>{t('mx.t.daily')} <small>{t('mx.dm.hint')}</small></h3>
          <DataGrid rows={rows ?? []} cols={cols} rowKey={(r) => r.id} title={`${t('mx.t.daily')} ${from} → ${to}`} loading={rows === null}
            onActivate={(r) => openScreen('journal', r.id)} footer />
        </section>
      </div>
    </>
  );
}
