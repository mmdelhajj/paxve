import { useEffect, useState } from 'react';
import { useDebouncedValue } from '@mantine/hooks';
import { useTranslation } from 'react-i18next';
import { api, can, type Me } from '../../api';
import { useCompany } from '../../company';
import { openScreen } from '../../nav';
import { formatAmount, notifyError } from '../../util';
import type { Area, Customer } from '../../billing';
import DataGrid, { type Col } from '../../components/DataGrid';
import StationSelect from '../../components/StationSelect';
import { CustomerForm } from '../billing/Customers';
import { Help } from '../acc/common';
import { useActivations } from '../../paneActive';

/** إدارة الإشتراكات: every subscriber in one grid (search, area, station, who owes); Enter opens the subscriber. */
export default function SubsManage({ me }: { me: Me }) {
  const { t } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [rows, setRows] = useState<Customer[] | null>(null);
  const [areas, setAreas] = useState<Area[]>([]);
  const [q, setQ] = useState('');
  const [dq] = useDebouncedValue(q, 300);
  const [area, setArea] = useState('');
  const [station, setStation] = useState<string | null>(null);
  const [onlyDue, setOnlyDue] = useState(false);
  const [creating, setCreating] = useState(false);
  const [sel, setSel] = useState<string | null>(null);
  const canEdit = can(me, 'customers.create');
  const cid = company?.id;
  const act = useActivations();

  useEffect(() => {
    if (!cid) return;
    setRows(null);
    const p = new URLSearchParams({ q: dq, area_id: area, station_id: station ?? '', due: onlyDue ? '1' : '', limit: '1000' });
    api<Customer[]>(`/companies/${cid}/customers?${p}`).then(setRows).catch((e) => { notifyError(t, e); setRows([]); });
  }, [cid, dq, area, station, onlyDue, act, t]);
  useEffect(() => { if (cid) api<Area[]>(`/companies/${cid}/areas`).then(setAreas).catch(() => {}); }, [cid]);
  if (!company) return null;

  const money = (xs: { currency: string; due?: string; credit?: string }[], k: 'due' | 'credit') =>
    xs.map((x) => `${formatAmount(x[k] ?? '0', decimalsOf(x.currency))} ${x.currency}`).join(' + ');
  const cols: Col<Customer>[] = [
    { key: 'code', title: t('customers.code'), get: (r) => r.code, width: 80 },
    { key: 'name', title: t('customers.name'), get: (r) => r.name, width: 230 },
    { key: 'phone', title: t('customers.phone'), get: (r) => r.phone, width: 120 },
    { key: 'area', title: t('customers.area'), get: (r) => r.area_name ?? '', width: 140 },
    { key: 'addr', title: t('customers.address'), get: (r) => r.address, width: 180 },
    { key: 'subs', title: t('ax.subs.count'), get: (r) => r.active_subscriptions, num: true, decimals: 0, filter: false, width: 80 },
    { key: 'due', title: t('customers.due'), get: (r) => r.due.reduce((s, x) => s + Number(x.due ?? 0), 0),
      render: (r) => money(r.due, 'due'), num: true, filter: false, width: 150, cls: (r) => (r.due.length ? 'red' : '') },
    { key: 'credit', title: t('ax.subs.credit'), get: (r) => money(r.credit, 'credit'), width: 120, filter: false },
    { key: 'st', title: t('customers.status'), get: (r) => t(`customers.statuses.${r.status}`), width: 100,
      render: (r) => <span className={`mx-pill ${r.status === 'active' ? 'on' : r.status === 'suspended' ? 'warn' : 'off'}`}>{t(`customers.statuses.${r.status}`)}</span> },
  ];

  return (
    <div className="mx-col">
      <div className="mx-strip">
        <label>{t('ax.search')} <input value={q} onChange={(e) => setQ(e.currentTarget.value)} placeholder={t('customers.search')} autoFocus /></label>
        <label>{t('customers.area')} <select value={area} onChange={(e) => setArea(e.currentTarget.value)}>
          <option value="">{t('readings.allAreas')}</option>{areas.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}</select></label>
        <div style={{ width: 170 }}><StationSelect size="xs" value={station} onChange={setStation} /></div>
        <label className="mx-check"><input type="checkbox" checked={onlyDue} onChange={(e) => setOnlyDue(e.currentTarget.checked)} /> {t('customers.onlyDue')}</label>
        <span className="mx-sp" />
        {canEdit && <button className="mx-btn" onClick={() => setCreating(true)}>+ {t('customers.new')}</button>}
        {sel && <button className="mx-btn" onClick={() => openScreen('statement', sel)}>{t('ax.subs.statement')}</button>}
        <Help topic="subscribers" />
      </div>
      <div className="mx-body" style={{ gridTemplateColumns: '1fr' }}>
        <section className="mx-box">
          <h3>{t('mx.t.manageSubs')} <small>{t('ax.subs.hint')}</small></h3>
          <DataGrid rows={rows ?? []} cols={cols} rowKey={(r) => r.id} loading={rows === null} footer title={t('mx.t.manageSubs')}
            selected={sel} onSelect={(r) => setSel(r.id)} onActivate={(r) => openScreen('customer', r.id)} />
        </section>
      </div>
      {creating && <CustomerForm areas={areas} onClose={() => setCreating(false)} onSaved={(c) => { setCreating(false); openScreen('customer', c.id); }} />}
    </div>
  );
}
