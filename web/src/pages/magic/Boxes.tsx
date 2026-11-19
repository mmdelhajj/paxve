import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, can, type Me } from '../../api';
import { useCompany } from '../../company';
import { errorText, formatAmount, formatDate, today } from '../../util';
import DataGrid, { type Col } from '../../components/DataGrid';

interface Box { id: string; name: string; kind: string; currency: string; user_id: string | null; user_name: string | null; account_code: string; balance: string }
interface Transfer { id: string; number: number; transfer_date: string; from_method: string; to_method: string; currency: string; amount: string; notes: string; created_by: string }

/** صناديق المستخدمين: balance of every cash box (company boxes and each user's own box) and moves between them. */
export default function Boxes({ me }: { me: Me }) {
  const { t } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [boxes, setBoxes] = useState<Box[] | null>(null);
  const [transfers, setTransfers] = useState<Transfer[] | null>(null);
  const [from, setFrom] = useState('');
  const [to, setTo] = useState('');
  const [amount, setAmount] = useState('');
  const [notes, setNotes] = useState('');
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const cid = company?.id;
  const canMove = can(me, 'collections.edit');

  const load = useCallback(() => {
    if (!cid) return;
    api<Box[]>(`/companies/${cid}/cash/balances`).then(setBoxes).catch(() => setBoxes([]));
    api<Transfer[]>(`/companies/${cid}/cash/transfers?from=${new Date().getFullYear()}-01-01`).then(setTransfers).catch(() => setTransfers([]));
  }, [cid]);
  useEffect(load, [load]);
  if (!company) return null;
  const fromBox = boxes?.find((b) => b.id === from);

  async function move() {
    if (!from || !to || !(Number(amount) > 0) || busy) return;
    setBusy(true);
    try {
      await api(`/companies/${cid}/cash/transfers`, { method: 'POST', body: { from_method_id: from, to_method_id: to, amount, notes, date: today() } });
      setMsg({ ok: true, text: t('common.saved') });
      setAmount('');
      setNotes('');
      load();
    } catch (e) {
      setMsg({ ok: false, text: errorText(t, e) });
    } finally {
      setBusy(false);
    }
  }

  const boxCols: Col<Box>[] = [
    { key: 'name', title: t('mx.col.box'), get: (r) => r.name, width: 200 },
    { key: 'user', title: t('mx.bx.owner'), get: (r) => r.user_name ?? t('mx.bx.company'), width: 150 },
    { key: 'kind', title: t('mx.bx.kind'), get: (r) => t(`methods.kinds.${r.kind}`), width: 90 },
    { key: 'cur', title: t('mx.d.currency'), get: (r) => r.currency, width: 70 },
    { key: 'acc', title: t('mx.col.account'), get: (r) => r.account_code, width: 90 },
    { key: 'bal', title: t('mx.col.balance'), get: (r) => r.balance, render: (r) => formatAmount(r.balance, decimalsOf(r.currency)), num: true, width: 150,
      cls: (r) => (Number(r.balance) < 0 ? 'red' : '') },
  ];
  const trCols: Col<Transfer>[] = [
    { key: 'n', title: t('journal.number'), get: (r) => r.number, width: 70 },
    { key: 'd', title: t('journal.date'), get: (r) => r.transfer_date, render: (r) => formatDate(r.transfer_date), width: 110 },
    { key: 'f', title: t('cash.from'), get: (r) => r.from_method, width: 170 },
    { key: 't', title: t('cash.to'), get: (r) => r.to_method, width: 170 },
    { key: 'a', title: t('mx.d.amount'), get: (r) => r.amount, render: (r) => `${formatAmount(r.amount, decimalsOf(r.currency))} ${r.currency}`, num: true, width: 150 },
    { key: 'no', title: t('customers.notes'), get: (r) => r.notes, width: 180 },
    { key: 'by', title: t('mx.bx.by'), get: (r) => r.created_by, width: 130 },
  ];

  return (
    <div className="mx-body" style={{ gridTemplateColumns: 'minmax(0, 1.5fr) minmax(280px, 1fr)', gridTemplateRows: 'minmax(0, 1fr) minmax(0, 1fr)' }}>
      <section className="mx-box">
        <h3>{t('mx.t.boxes')}</h3>
        <DataGrid rows={boxes ?? []} cols={boxCols} rowKey={(r) => r.id} title={t('mx.t.boxes')} loading={boxes === null}
          selected={from || null} onSelect={(b) => { if (canMove) { setFrom(b.id); setTo(''); } }} />
      </section>
      <section className="mx-box" style={{ gridRow: 'span 2' }}>
        <h3>{t('cash.transfer')}</h3>
        {canMove ? (
          <div style={{ padding: 12, display: 'flex', flexDirection: 'column', gap: 10 }}>
            <div className="mx-fld"><label htmlFor="bx-from">{t('cash.from')}</label>
              <select id="bx-from" value={from} onChange={(e) => { setFrom(e.currentTarget.value); setTo(''); }}>
                <option value="" />
                {(boxes ?? []).map((b) => <option key={b.id} value={b.id}>{b.name} ({b.currency})</option>)}
              </select></div>
            <div className="mx-fld"><label htmlFor="bx-to">{t('cash.to')}</label>
              <select id="bx-to" value={to} onChange={(e) => setTo(e.currentTarget.value)}>
                <option value="" />
                {(boxes ?? []).filter((b) => b.id !== from && (!fromBox || b.currency === fromBox.currency)).map((b) => <option key={b.id} value={b.id}>{b.name} ({b.currency})</option>)}
              </select></div>
            <div className="mx-fld"><label htmlFor="bx-amt">{t('mx.d.amount')}</label>
              <input id="bx-amt" inputMode="decimal" value={amount} onChange={(e) => setAmount(e.currentTarget.value.replace(/[^\d.]/g, ''))}
                onKeyDown={(e) => { if (e.key === 'Enter') void move(); }} /></div>
            {fromBox && <button className="mx-btn sm" style={{ alignSelf: 'flex-start' }} onClick={() => setAmount(String(Number(fromBox.balance)))}>
              {t('cash.all')}: {formatAmount(fromBox.balance, decimalsOf(fromBox.currency))} {fromBox.currency}</button>}
            <div className="mx-fld"><label htmlFor="bx-notes">{t('customers.notes')}</label>
              <input id="bx-notes" value={notes} style={{ fontSize: 14 }} onChange={(e) => setNotes(e.currentTarget.value)} /></div>
            <button className="mx-btn main" disabled={!from || !to || !(Number(amount) > 0) || busy} onClick={() => void move()}>{t('cash.move')}</button>
            {msg && <div className={`mx-flash ${msg.ok ? 'good' : 'bad'}`}>{msg.text}</div>}
          </div>
        ) : <div style={{ padding: 12, color: 'var(--mx-muted)' }}>{t('errors.forbidden')}</div>}
      </section>
      <section className="mx-box">
        <h3>{t('cash.history')}</h3>
        <DataGrid rows={transfers ?? []} cols={trCols} rowKey={(r) => r.id} title={t('cash.history')} loading={transfers === null} />
      </section>
    </div>
  );
}
