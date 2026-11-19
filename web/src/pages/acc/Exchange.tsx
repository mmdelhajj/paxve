import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, can, type Me } from '../../api';
import { useCompany } from '../../company';
import { errorText, formatAmount, formatDate, today } from '../../util';
import DataGrid, { type Col } from '../../components/DataGrid';
import { Help, useBoxes, useLoad } from './common';

interface Ex {
  id: string; number: number; exchange_date: string; from_method: string; from_currency: string; from_amount: string;
  to_method: string; to_currency: string; to_amount: string; rate: string; counterparty: string; status: string; gain: string; created_by: string;
}

/** شراء / بيع دولار: money leaves one box in one currency and enters another box in another currency. */
export default function Exchange({ me }: { me: Me }) {
  const { t } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const boxes = useBoxes();
  const [from, setFrom] = useState('');
  const [to, setTo] = useState('');
  const [fromAmt, setFromAmt] = useState('');
  const [toAmt, setToAmt] = useState('');
  const [rate, setRate] = useState('');
  const [party, setParty] = useState('');
  const [date, setDate] = useState(today());
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [lfrom, setLfrom] = useState(() => today().slice(0, 8) + '01');
  const [sel, setSel] = useState<Ex | null>(null);
  const cid = company?.id;
  const [list, reload, loading] = useLoad<Ex[]>(cid ? `/companies/${cid}/exchanges?from=${lfrom}` : null);
  const canEdit = can(me, 'collections.edit');
  if (!company) return null;
  const fb = boxes.find((b) => b.id === from);
  const tb = boxes.find((b) => b.id === to);
  const dec = (c?: string) => (c ? decimalsOf(c) : 2);

  // amounts ↔ rate: typing the rate fills the received amount; typing both amounts shows the rate
  function onRate(v: string) {
    setRate(v);
    const r = Number(v), a = Number(fromAmt);
    if (r > 0 && a > 0 && tb) setToAmt(String(Math.round(a * r * 10 ** dec(tb.currency)) / 10 ** dec(tb.currency)));
  }
  function onFrom(v: string) {
    setFromAmt(v);
    const r = Number(rate), a = Number(v);
    if (r > 0 && a > 0 && tb) setToAmt(String(Math.round(a * r * 10 ** dec(tb.currency)) / 10 ** dec(tb.currency)));
  }
  function onTo(v: string) {
    setToAmt(v);
    const a = Number(fromAmt), b = Number(v);
    if (a > 0 && b > 0) setRate(String(+(b / a).toPrecision(8)));
  }

  async function save() {
    if (!cid || busy) return;
    setBusy(true);
    setMsg(null);
    try {
      const r = await api<Ex>(`/companies/${cid}/exchanges`, { method: 'POST', body: { date, from_method_id: from, to_method_id: to,
        from_amount: fromAmt, to_amount: toAmt, counterparty: party } });
      const g = Number(r.gain);
      setMsg({ ok: true, text: `${t('common.saved')} · #${r.number} · ${g >= 0 ? t('ax.ex.gain') : t('ax.ex.loss')} ${formatAmount(Math.abs(g), dec(company!.base_currency))} ${company!.base_currency}` });
      setFromAmt(''); setToAmt(''); setParty('');
      reload();
    } catch (e) {
      setMsg({ ok: false, text: errorText(t, e) });
    } finally {
      setBusy(false);
    }
  }
  async function voidEx(x: Ex) {
    if (!cid || x.status !== 'posted') return;
    try {
      await api(`/companies/${cid}/exchanges/${x.id}/void`, { method: 'POST', body: {} });
      setSel(null);
      reload();
    } catch (e) {
      setMsg({ ok: false, text: errorText(t, e) });
    }
  }

  const cols: Col<Ex>[] = [
    { key: 'n', title: '#', get: (r) => r.number, width: 60 },
    { key: 'd', title: t('journal.date'), get: (r) => r.exchange_date, render: (r) => formatDate(r.exchange_date), width: 110 },
    { key: 'f', title: t('ax.ex.gave'), get: (r) => `${r.from_amount} ${r.from_currency}`, render: (r) => `${formatAmount(r.from_amount, dec(r.from_currency))} ${r.from_currency} · ${r.from_method}`, width: 220 },
    { key: 't', title: t('ax.ex.got'), get: (r) => `${r.to_amount} ${r.to_currency}`, render: (r) => `${formatAmount(r.to_amount, dec(r.to_currency))} ${r.to_currency} · ${r.to_method}`, width: 240 },
    { key: 'r', title: t('ax.ex.rate'), get: (r) => r.rate, width: 110 },
    { key: 'g', title: t('ax.ex.gainLoss'), get: (r) => r.gain, num: true, decimals: dec(company.base_currency), total: 'sum', filter: false, width: 120,
      cls: (r) => (Number(r.gain) < 0 ? 'red' : 'ok') },
    { key: 'p', title: t('ax.ex.party'), get: (r) => r.counterparty, width: 140 },
    { key: 's', title: t('ax.status'), get: (r) => (r.status === 'void' ? t('invoices.statuses.void') : ''), width: 80 },
    { key: 'by', title: t('mx.bx.by'), get: (r) => r.created_by, width: 110 },
  ];

  return (
    <div className="mx-col">
      <div className="mx-strip">
        <label>{t('reports.from')} <input type="date" value={lfrom} onChange={(e) => setLfrom(e.currentTarget.value)} /></label>
        <span className="mx-sp" />
        <Help topic="exchange" />
      </div>
      <div className="mx-body" style={{ gridTemplateColumns: 'minmax(0, 1.7fr) minmax(300px, 1fr)' }}>
        <section className="mx-box">
          <h3>{t('ax.t.exchange')}<span className="mx-sp" />
            {canEdit && sel && sel.status === 'posted' && <button className="mx-btn sm" onClick={() => void voidEx(sel)}>{t('ax.ex.void', { n: sel.number })}</button>}</h3>
          <DataGrid rows={list ?? []} cols={cols} rowKey={(r) => r.id} loading={loading} footer title={t('ax.t.exchange')}
            selected={sel?.id ?? null} onSelect={setSel} rowClass={(r) => (r.status === 'void' ? 'muted' : '')} />
        </section>
        <section className="mx-box">
          <h3>{t('ax.ex.new')}</h3>
          {canEdit ? (
            <div className="mx-form">
              <div className="mx-fld"><label htmlFor="ex-date">{t('journal.date')}</label><input id="ex-date" type="date" value={date} onChange={(e) => setDate(e.currentTarget.value)} /></div>
              <div className="mx-fld"><label htmlFor="ex-from">{t('ax.ex.fromBox')}</label>
                <select id="ex-from" value={from} onChange={(e) => { setFrom(e.currentTarget.value); setTo(''); }}>
                  <option value="" />{boxes.map((b) => <option key={b.id} value={b.id}>{b.name} ({b.currency})</option>)}</select></div>
              <div className="mx-fld"><label htmlFor="ex-fa">{t('ax.ex.gave')} {fb?.currency}</label>
                <input id="ex-fa" inputMode="decimal" value={fromAmt} onChange={(e) => onFrom(e.currentTarget.value.replace(/[^\d.]/g, ''))} /></div>
              <div className="mx-fld"><label htmlFor="ex-to">{t('ax.ex.toBox')}</label>
                <select id="ex-to" value={to} onChange={(e) => setTo(e.currentTarget.value)}>
                  <option value="" />{boxes.filter((b) => fb && b.currency !== fb.currency).map((b) => <option key={b.id} value={b.id}>{b.name} ({b.currency})</option>)}</select></div>
              <div className="mx-fld"><label htmlFor="ex-rate">{t('ax.ex.rate')}</label>
                <input id="ex-rate" inputMode="decimal" value={rate} onChange={(e) => onRate(e.currentTarget.value.replace(/[^\d.]/g, ''))} /></div>
              <div className="mx-fld"><label htmlFor="ex-ta">{t('ax.ex.got')} {tb?.currency}</label>
                <input id="ex-ta" inputMode="decimal" value={toAmt} onChange={(e) => onTo(e.currentTarget.value.replace(/[^\d.]/g, ''))}
                  onKeyDown={(e) => { if (e.key === 'Enter') void save(); }} /></div>
              <div className="mx-fld"><label htmlFor="ex-p">{t('ax.ex.party')}</label><input id="ex-p" value={party} onChange={(e) => setParty(e.currentTarget.value)} /></div>
              <button className="mx-btn main" disabled={busy || !from || !to || !(Number(fromAmt) > 0) || !(Number(toAmt) > 0)} onClick={() => void save()}>{t('ax.ex.save')}</button>
              {msg && <div className={`mx-flash ${msg.ok ? 'good' : 'bad'}`}>{msg.text}</div>}
              <p className="mx-note">{t('ax.ex.explain')}</p>
            </div>
          ) : <div className="mx-empty-box">{t('errors.forbidden')}</div>}
        </section>
      </div>
    </div>
  );
}
