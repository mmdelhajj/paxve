import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, type Me } from '../../api';
import { useCompany } from '../../company';
import type { Customer } from '../../billing';
import { formatDate } from '../../util';
import DataGrid, { type Col } from '../../components/DataGrid';
import { openScreen } from '../../nav';

/** عرض الإشتراكات: every subscriber in one dense grid (filter row, sort, totals, export). Double-click opens the file. */
export default function Subscribers({ me }: { me: Me }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const [rows, setRows] = useState<Customer[] | null>(null);
  const [loaded, setLoaded] = useState(0);
  const [reload, setReload] = useState(0);
  const cid = company?.id;
  void me;

  useEffect(() => {
    if (!cid) return;
    let stop = false;
    (async () => {
      const all: Customer[] = [];
      for (let off = 0; off < 100_000 && !stop; off += 1000) {
        const page = await api<Customer[]>(`/companies/${cid}/customers?limit=1000&offset=${off}`).catch(() => [] as Customer[]);
        all.push(...page);
        if (stop) return;
        setLoaded(all.length);
        setRows([...all]);
        if (page.length < 1000) break;
      }
    })();
    return () => { stop = true; };
  }, [cid, reload]);

  if (!company) return null;
  const base = company.base_currency;
  const local = company.local_currency;
  const due = (c: Customer, cur: string) => Number(c.due.find((d) => d.currency === cur)?.due ?? 0);
  const cols: Col<Customer>[] = [
    { key: 'name', title: t('mx.c.name'), get: (r) => r.name, width: 220 },
    { key: 'code', title: t('mx.c.code'), get: (r) => r.code, width: 80 },
    { key: 'area', title: t('mx.c.area'), get: (r) => r.area_name ?? '', width: 130 },
    { key: 'phone', title: t('mx.c.phone'), get: (r) => r.phone, width: 110 },
    { key: 'phone2', title: t('mx.c.phone2'), get: (r) => r.phone2, width: 110 },
    { key: 'address', title: t('mx.c.address'), get: (r) => r.address, width: 180 },
    { key: 'subs', title: t('mx.c.subs'), get: (r) => r.active_subscriptions, width: 70, total: (v) => v.reduce((s, r) => s + r.active_subscriptions, 0) },
    { key: 'status', title: t('mx.c.status'), get: (r) => t(`customers.statuses.${r.status}`), width: 90,
      render: (r) => <span className={`mx-pill ${r.status === 'active' ? 'on' : 'off'}`}>{t(`customers.statuses.${r.status}`)}</span> },
    { key: 'due', title: t('mx.c.due', { cur: base }), get: (r) => due(r, base) || '', num: true, width: 110, filter: false, total: 'sum',
      cls: (r) => (due(r, base) > 0 ? 'red' : '') },
    ...(local !== base ? [{ key: 'dueL', title: t('mx.c.due', { cur: local }), get: (r: Customer) => due(r, local) || '', num: true, decimals: 0,
      width: 130, filter: false, total: 'sum' as const, cls: (r: Customer) => (due(r, local) > 0 ? 'red' : '') }] : []),
    { key: 'created', title: t('mx.c.since'), get: (r) => r.created_at.slice(0, 10), render: (r) => formatDate(r.created_at), width: 110 },
  ];

  return (
    <div className="mx-body" style={{ gridTemplateColumns: '1fr' }}>
      <section className="mx-box">
        <h3>{t('mx.t.subscribers')} <small>{rows === null ? t('mx.loading') : t('mx.sub.count', { n: loaded })}</small>
          <span className="mx-sp" />
          <button className="mx-btn sm" onClick={() => openScreen('customers')}>{t('mx.t.manageSubs')}</button>
          <button className="mx-btn sm" onClick={() => setReload((x) => x + 1)}>{t('mx.refresh')}</button>
        </h3>
        <DataGrid rows={rows ?? []} cols={cols} rowKey={(r) => r.id} title={t('mx.t.subscribers')} loading={rows === null}
          onActivate={(r) => openScreen('customer', r.id)} footer />
      </section>
    </div>
  );
}
