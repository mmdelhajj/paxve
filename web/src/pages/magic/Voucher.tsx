import { useEffect, useMemo, useState } from 'react';
import { createPortal } from 'react-dom';
import { Select } from '@mantine/core';
import { useTranslation } from 'react-i18next';
import { api, can, type Account, type Me } from '../../api';
import { useCompany } from '../../company';
import type { Customer, PaymentDetail, PaymentMethod } from '../../billing';
import { accountLabel, errorText, formatAmount, formatDate, today } from '../../util';
import { printPage } from '../../native';
import { amountInWords } from '../../tafqit';
import { pickBox } from './Jibaya';
import { ccLabel, useCostCenters } from '../acc/common';

type Kind = 'receipt' | 'expense' | 'payment';
interface Party { id: string; name: string; code?: string }
interface Done { number: string; party: string; amount: string; currency: string; words: string; about: string; date: string; box: string }

/** إيصال قبض / مصاريف / دفع نقدي: Magic's paper-style cash voucher. Reads like the printed receipt:
 *  "وصلنا من السيد … مبلغ … (بالحروف) … وذلك عن …", then حفظ / طباعة. */
export default function Voucher({ me, kind: kindParam }: { me: Me; kind: string }) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const kind: Kind = kindParam === 'expense' || kindParam === 'payment' ? kindParam : 'receipt';
  const [methods, setMethods] = useState<PaymentMethod[]>([]);
  const [boxId, setBoxId] = useState<string | null>(null);
  const [party, setParty] = useState<string | null>(null);
  const [parties, setParties] = useState<Party[]>([]);
  const [search, setSearch] = useState('');
  const [amount, setAmount] = useState('');
  const [about, setAbout] = useState('');
  const [date, setDate] = useState(today());
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [done, setDone] = useState<Done | null>(null);
  const [printing, setPrinting] = useState(false);
  const [cc, setCc] = useState<string | null>(null);
  const centers = useCostCenters().filter((x) => x.is_active);
  const cid = company?.id;

  useEffect(() => {
    if (!cid) return;
    api<PaymentMethod[]>(`/companies/${cid}/payment-methods`).then((ms) => {
      const a = ms.filter((m) => m.is_active);
      setMethods(a);
      const first = pickBox(a, company!.base_currency, me) ?? a[0];
      if (first) setBoxId(first.id);
    }).catch(() => {});
    if (kind === 'expense') {
      api<Account[]>(`/companies/${cid}/accounts`).then((a) => setParties(a.filter((x) => x.is_postable && x.is_active && x.type === 'expense')
        .map((x) => ({ id: x.id, name: accountLabel(x) })))).catch(() => {});
    } else if (kind === 'payment') {
      api<{ id: string; name: string; code: string }[]>(`/companies/${cid}/suppliers`).then((s) => setParties(s.map((x) => ({ id: x.id, name: x.name, code: x.code })))).catch(() => {});
    }
  }, [cid, kind]); // eslint-disable-line react-hooks/exhaustive-deps

  // receipts: customers are searched on the server as you type
  useEffect(() => {
    if (!cid || kind !== 'receipt') return;
    const h = setTimeout(() => {
      api<Customer[]>(`/companies/${cid}/customers?q=${encodeURIComponent(search)}&limit=30`)
        .then((cs) => setParties((old) => {
          const keep = old.filter((p) => p.id === party);
          return [...keep, ...cs.filter((c) => c.id !== party).map((c) => ({ id: c.id, name: `${c.name} · ${c.code}`, code: c.code }))];
        })).catch(() => {});
    }, 250);
    return () => clearTimeout(h);
  }, [cid, kind, search]); // eslint-disable-line react-hooks/exhaustive-deps

  const box = methods.find((m) => m.id === boxId);
  const currency = box?.currency ?? company?.base_currency ?? 'USD';
  const words = useMemo(() => (Number(amount) > 0 ? amountInWords(amount, currency) : ''), [amount, currency]);
  if (!company) return null;
  const perm = kind === 'receipt' ? 'collections.create' : kind === 'expense' ? 'expenses.create' : 'purchasing.create';
  const partyName = parties.find((p) => p.id === party)?.name ?? '';

  async function save() {
    if (!party || !box || !(Number(amount) > 0) || busy) return;
    setBusy(true);
    setMsg(null);
    try {
      let number = '';
      if (kind === 'receipt') {
        const d = await api<PaymentDetail>(`/companies/${cid}/payments`, {
          method: 'POST', body: { customer_id: party, date, currency, tenders: [{ method_id: box.id, amount }], notes: about },
        });
        number = String(d.payment.number);
        window.dispatchEvent(new Event('paxve:paid'));
      } else if (kind === 'expense') {
        const r = await api<Record<string, unknown>>(`/companies/${cid}/expenses`, {
          method: 'POST', body: { date, account_id: party, method_id: box.id, amount, description: about || partyName, cost_center_id: cc ?? '' },
        });
        number = String((r as { number?: number }).number ?? (r as { expense?: { number?: number } }).expense?.number ?? '');
      } else {
        const r = await api<Record<string, unknown>>(`/companies/${cid}/supplier-payments`, {
          method: 'POST', body: { supplier_id: party, date, method_id: box.id, paid_amount: amount, currency },
        });
        number = String((r as { number?: number }).number ?? (r as { payment?: { number?: number } }).payment?.number ?? '');
      }
      const d: Done = { number, party: partyName, amount, currency, words, about, date, box: box.name };
      setDone(d);
      setMsg({ ok: true, text: t('mx.v.saved', { n: number }) });
      setPrinting(true);
      setTimeout(() => { void printPage(t(`mx.t.${kind}Voucher`)).finally(() => setPrinting(false)); }, 60);
      setAmount('');
      setAbout('');
      setParty(null);
    } catch (e) {
      setMsg({ ok: false, text: errorText(t, e) });
    } finally {
      setBusy(false);
    }
  }

  const fromLabel = kind === 'receipt' ? t('mx.v.from') : kind === 'expense' ? t('mx.v.expenseOf') : t('mx.v.paidTo');
  const companyName = company.name_ar || company.name_en;

  return (
    <div className="mx-paper">
      <div className="mx-row" style={{ justifyContent: 'space-between' }}>
        <b>{companyName}</b>
        <span>{t('mx.v.date')}: <input type="date" className="mx-dots" style={{ minWidth: 150, flex: 'none' }} value={date} onChange={(e) => setDate(e.currentTarget.value || today())} /></span>
      </div>
      <h2>{t(`mx.t.${kind}Voucher`)}</h2>
      <div className="mx-line">
        <span>{fromLabel}</span>
        <div style={{ flex: 1, minWidth: 220 }}>
          <Select searchable clearable data={parties.map((p) => ({ value: p.id, label: p.name }))} value={party} onChange={setParty}
            onSearchChange={kind === 'receipt' ? setSearch : undefined} nothingFoundMessage={t('common.empty')} aria-label={fromLabel}
            placeholder={kind === 'receipt' ? t('customers.search') : ''} filter={kind === 'receipt' ? ({ options }) => options : undefined} />
        </div>
      </div>
      <div className="mx-line">
        <span>{t('mx.v.amount')}</span>
        <input className="mx-dots" dir="ltr" inputMode="decimal" style={{ fontSize: 20, fontWeight: 700, maxWidth: 220 }} aria-label={t('mx.v.amount')}
          value={amount} onChange={(e) => setAmount(e.currentTarget.value.replace(/[^\d.]/g, ''))}
          onKeyDown={(e) => { if (e.key === 'Enter') void save(); }} />
        <select value={boxId ?? ''} aria-label={t('mx.v.box')} onChange={(e) => setBoxId(e.currentTarget.value)} style={{ padding: 4 }}>
          {methods.map((m) => <option key={m.id} value={m.id}>{m.name} ({m.currency})</option>)}
        </select>
      </div>
      {kind === 'expense' && centers.length > 0 && (
        <div className="mx-line">
          <span>{t('ax.cc.center')}</span>
          <div style={{ flex: 1, minWidth: 220 }}>
            <Select clearable data={centers.map((x) => ({ value: x.id, label: ccLabel(x, i18n.language) }))} value={cc} onChange={setCc}
              placeholder={t('ax.cc.none')} aria-label={t('ax.cc.center')} />
          </div>
        </div>
      )}
      <div className="mx-line"><span>{t('mx.v.words')}</span><span className="mx-words">{words}</span></div>
      <div className="mx-line">
        <span>{t('mx.v.about')}</span>
        <input className="mx-dots" value={about} aria-label={t('mx.v.about')} onChange={(e) => setAbout(e.currentTarget.value)} onKeyDown={(e) => { if (e.key === 'Enter') void save(); }} />
      </div>
      <div className="mx-row" style={{ justifyContent: 'flex-end' }}>
        <button className="mx-btn main" style={{ minWidth: 200 }} disabled={!can(me, perm) || !party || !box || !(Number(amount) > 0) || busy} onClick={() => void save()}>{t('mx.j.save')}</button>
      </div>
      {msg && <div className={`mx-flash ${msg.ok ? 'good' : 'bad'}`}>{msg.text}</div>}
      <div className="mx-sign"><span>{t('mx.v.receiverSign')}</span><span>{t('mx.v.accountantSign')}</span></div>
      {done && printing && createPortal(
        <div className="mx-printhost"><div className="print-doc" dir="rtl" style={{ fontSize: 15, lineHeight: 2 }}>
          <div style={{ display: 'flex', justifyContent: 'space-between' }}><b>{companyName}</b><span>{t('mx.v.no')} {done.number} · {formatDate(done.date)}</span></div>
          <h2 style={{ textAlign: 'center', margin: '6px 0' }}>{t(`mx.t.${kind}Voucher`)}</h2>
          <div>{fromLabel}: <b>{done.party}</b></div>
          <div>{t('mx.v.amount')}: <b dir="ltr">{formatAmount(done.amount, decimalsOf(done.currency))} {done.currency}</b></div>
          <div>{t('mx.v.words')}: <b>{done.words}</b></div>
          {done.about && <div>{t('mx.v.about')}: {done.about}</div>}
          <div>{t('mx.v.box')}: {done.box}</div>
          <div style={{ display: 'flex', justifyContent: 'space-between', marginTop: 30 }}><span>{t('mx.v.receiverSign')}: ............</span><span>{t('mx.v.accountantSign')}: ............</span></div>
        </div></div>, document.body)}
    </div>
  );
}
