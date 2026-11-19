import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import { api, can, type Me, type Rate } from '../../api';
import { useCompany } from '../../company';
import { currentPeriod, type Area, type Customer, type Invoice, type Payment, type PaymentDetail, type PaymentMethod } from '../../billing';
import { autoApply, errorText, formatAmount, formatDate, roundChange, today } from '../../util';
import { printPage } from '../../native';
import { printerName, printLines } from '../../bt';
import DataGrid, { type Col } from '../../components/DataGrid';
import { Receipt } from '../billing/docs';
import { receiptLines } from '../collector/CollectorApp';
import { openScreen } from '../../nav';

type Mode = 'one' | 'scan' | 'read' | 'late' | 'col';
const MODES: Mode[] = ['one', 'scan', 'read', 'late', 'col'];

/** الجباية: Magic's collection screen. One subscriber at a time, keyboard only:
 *  type in the yellow filter row → ↓ → Enter → amount → Enter = save + print + back to the search. */
export default function Jibaya({ me, mode: modeParam, active }: { me: Me; mode: string; active: boolean }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const mode: Mode = (MODES as string[]).includes(modeParam) ? (modeParam as Mode) : 'one';
  const [date, setDate] = useState(today());
  const [rates, setRates] = useState<Record<string, string>>({});
  const [rateTyped, setRateTyped] = useState('');
  const [methods, setMethods] = useState<PaymentMethod[]>([]);
  const [picked, setPicked] = useState<Customer | null>(null);

  useEffect(() => {
    if (!company) return;
    api<PaymentMethod[]>(`/companies/${company.id}/payment-methods`).then((m) => setMethods(m.filter((x) => x.is_active))).catch(() => {});
  }, [company]);
  useEffect(() => {
    if (!company) return;
    api<{ rates: Rate[] }>(`/companies/${company.id}/rates/latest?date=${date}`)
      .then((r) => setRates(Object.fromEntries(r.rates.map((x) => [x.currency, x.rate])))).catch(() => {});
  }, [company, date]);

  if (!company) return null;
  const base = company.base_currency;
  const local = company.local_currency;
  const localRate = Number(rateTyped || rates[local] || 0);
  const due = (c: Customer | null, cur: string) => Number(c?.due.find((d) => d.currency === cur)?.due ?? 0);
  const dueBase = picked ? picked.due.reduce((s, d) => s + (d.currency === base ? Number(d.due) : localRate ? Number(d.due) / localRate : 0), 0) : 0;

  const setMode = (m: Mode) => { window.location.hash = `#/jibaya${m === 'one' ? '' : '/' + m}`; };

  return (
    <>
      <div className="mx-strip">
        <label>{t('mx.j.date')} <input type="date" value={date} onChange={(e) => setDate(e.currentTarget.value || today())} /></label>
        {local !== base && (
          <label>{t('mx.j.rate', { cur: local })}
            <input dir="ltr" style={{ width: 100, textAlign: 'center' }} inputMode="decimal" value={rateTyped} placeholder={rates[local] ? String(Number(rates[local])) : '?'}
              onChange={(e) => setRateTyped(e.currentTarget.value.replace(/[^\d.]/g, ''))} />
          </label>
        )}
        <span>{t('mx.j.balance', { cur: base })} <span className={`mx-out ${dueBase > 0.004 ? 'due' : ''}`}>{formatAmount(dueBase, 2)}</span></span>
        {local !== base && (
          <span>{t('mx.j.balance', { cur: local })} <span className={`mx-out ${dueBase > 0.004 ? 'due' : ''}`}>{formatAmount(dueBase * localRate, 0)}</span></span>
        )}
        {picked && <span style={{ fontWeight: 700 }}>{picked.name} <span style={{ color: 'var(--mx-muted)' }}>#{picked.code}</span></span>}
      </div>
      <div className="mx-modes" role="tablist">
        {MODES.map((m) => (
          <button key={m} className="mx-mode" role="tab" aria-selected={mode === m} onClick={() => setMode(m)}>{t(`mx.j.modes.${m}`)}</button>
        ))}
      </div>
      {mode === 'one' && <PayOne me={me} active={active} date={date} rates={rates} rateTyped={rateTyped} methods={methods}
        picked={picked} setPicked={setPicked} due={due} />}
      {mode === 'scan' && <Scan me={me} active={active} date={date} methods={methods} />}
      {mode === 'read' && <ReadingsMode me={me} active={active} />}
      {mode === 'late' && <Late onPick={(c) => { setPicked(c); setMode('one'); }} />}
      {mode === 'col' && <Settle me={me} />}
    </>
  );
}

/** The box that receives money in this currency: the user's own cash box first, then the company's. */
export function pickBox(methods: PaymentMethod[], currency: string, me: Me): PaymentMethod | undefined {
  const inCur = methods.filter((m) => m.currency === currency);
  return inCur.find((m) => m.kind === 'cash' && m.user_id === me.id) ?? inCur.find((m) => m.kind === 'cash' && !m.user_id)
    ?? inCur.find((m) => !m.user_id) ?? inCur[0];
}

type DocRow = { key: string; kind: 'invoice' | 'payment'; id: string; number: number; date: string; period: string; currency: string;
  amount: string; paid: string; balance: string; status: string; inv?: Invoice; pay?: Payment };

function PayOne({ me, active, date, rates, rateTyped, methods, picked, setPicked, due }: {
  me: Me; active: boolean; date: string; rates: Record<string, string>; rateTyped: string; methods: PaymentMethod[];
  picked: Customer | null; setPicked: (c: Customer | null) => void; due: (c: Customer | null, cur: string) => number;
}) {
  const { t } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const cid = company!.id;
  const base = company!.base_currency;
  const local = company!.local_currency;
  const [filters, setFilters] = useState<Record<string, string>>({});
  const [list, setList] = useState<Customer[]>([]);
  const [loading, setLoading] = useState(true);
  const [docs, setDocs] = useState<DocRow[]>([]);
  const [amtBase, setAmtBase] = useState('');
  const [amtLocal, setAmtLocal] = useState('');
  const [busy, setBusy] = useState(false);
  const [flash, setFlash] = useState<{ ok: boolean; text: string } | null>(null);
  const [last, setLast] = useState<PaymentDetail | null>(null);
  const [printDoc, setPrintDoc] = useState<PaymentDetail | null>(null);
  const [autoPrint, setAutoPrint] = useState<boolean>(() => { try { return localStorage.getItem('paxve.autoprint') !== '0'; } catch { return true; } });
  const [confirmFix, setConfirmFix] = useState<string | null>(null);
  const search = useRef<HTMLInputElement>(null);
  const baseInput = useRef<HTMLInputElement>(null);

  // server search on the most selective typed text (name / code / phone); the grid then filters each column
  const q = (filters.name || filters.code || filters.phone || '').trim();
  useEffect(() => {
    setLoading(true);
    const h = setTimeout(() => {
      api<Customer[]>(`/companies/${cid}/customers?q=${encodeURIComponent(q)}&limit=300`)
        .then((r) => { setList(r); setLoading(false); }).catch(() => setLoading(false));
    }, q ? 250 : 0);
    return () => clearTimeout(h);
  }, [cid, q]);

  const loadDocs = useCallback((c: Customer) => {
    Promise.all([
      api<{ rows: Invoice[] }>(`/companies/${cid}/invoices?customer_id=${c.id}&limit=200`),
      api<{ rows: Payment[] }>(`/companies/${cid}/payments?customer_id=${c.id}&kind=all&limit=200`),
    ]).then(([inv, pay]) => {
      const rows: DocRow[] = [
        ...inv.rows.map((i) => ({ key: 'i' + i.id, kind: 'invoice' as const, id: i.id, number: i.number, date: i.issue_date, period: i.period ?? '',
          currency: i.currency, amount: i.total, paid: i.paid, balance: i.balance, status: i.status, inv: i })),
        ...pay.rows.map((p) => ({ key: 'p' + p.id, kind: 'payment' as const, id: p.id, number: p.number, date: p.payment_date, period: '',
          currency: p.currency, amount: p.amount, paid: '', balance: '', status: p.status, pay: p })),
      ].sort((a, b) => b.date.localeCompare(a.date) || b.number - a.number);
      setDocs(rows);
    }).catch(() => setDocs([]));
  }, [cid]);

  const refreshPicked = useCallback(async (id: string) => {
    const c = await api<Customer>(`/companies/${cid}/customers/${id}`).catch(() => null);
    const fresh = c && (c as unknown as { customer?: Customer }).customer ? (c as unknown as { customer: Customer }).customer : c;
    if (fresh) { setPicked(fresh); loadDocs(fresh); setList((l) => l.map((x) => (x.id === fresh.id ? { ...x, due: fresh.due, credit: fresh.credit } : x))); }
    return fresh;
  }, [cid, loadDocs, setPicked]);

  function pick(c: Customer) {
    setPicked(c);
    loadDocs(c);
    setFlash(null);
    const owed = due(c, base);
    setAmtBase(owed > 0 ? String(Math.round(owed * 100) / 100) : '');
    setAmtLocal('');
    setTimeout(() => { baseInput.current?.focus(); baseInput.current?.select(); }, 20);
  }
  function backToSearch() {
    setPicked(null);
    setDocs([]);
    setAmtBase('');
    setAmtLocal('');
    setTimeout(() => { search.current?.focus(); search.current?.select(); }, 20);
  }

  // ---- money preview (the server recomputes with exact decimals) ----
  const rateOf = (cur: string) => (cur === base ? 1 : Number((cur === local && rateTyped) || rates[cur] || 0));
  const payCur = picked?.due[0]?.currency ?? base;
  const payRate = rateOf(payCur);
  const boxBase = pickBox(methods, base, me);
  const boxLocal = local !== base ? pickBox(methods, local, me) : undefined;
  const changeBox = (local !== base ? boxLocal : undefined) ?? boxBase;
  let received = 0;
  if (Number(amtBase) > 0 && payRate) received += (Number(amtBase) / rateOf(base)) * payRate;
  if (Number(amtLocal) > 0 && rateOf(local) && payRate) received += (Number(amtLocal) / rateOf(local)) * payRate;
  const owed = due(picked, payCur);
  const apply = autoApply(received, owed);
  const extra = received - (apply ? Number(apply) : received);
  const changeRate = changeBox ? rateOf(changeBox.currency) : 0;
  const change = extra > 0 && changeBox && changeRate && payRate ? roundChange((extra / payRate) * changeRate, decimalsOf(changeBox.currency), changeBox.change_rounding) : 0;

  async function save() {
    if (!picked || busy) return;
    const tenders = [
      ...(Number(amtBase) > 0 && boxBase ? [{ method_id: boxBase.id, amount: amtBase }] : []),
      ...(Number(amtLocal) > 0 && boxLocal ? [{ method_id: boxLocal.id, amount: amtLocal, rate: rateTyped }] : []),
    ];
    if (!tenders.length) { setFlash({ ok: false, text: t('mx.j.typeAmount') }); return; }
    setBusy(true);
    try {
      const d = await api<PaymentDetail>(`/companies/${cid}/payments`, {
        method: 'POST',
        body: { customer_id: picked.id, date, currency: payCur, amount: apply, tenders, change_method_id: extra > 0.00005 ? changeBox?.id ?? '' : '' },
      });
      setLast(d);
      window.dispatchEvent(new Event('paxve:paid'));
      setFlash({ ok: true, text: t('mx.j.saved', { n: d.payment.number, name: picked.name }) });
      await print(d);
      setList((l) => l.map((x) => (x.id === picked.id ? { ...x, due: d.customer_balance.due, credit: d.customer_balance.credit } : x)));
      setFilters({});
      backToSearch();
    } catch (e) {
      setFlash({ ok: false, text: errorText(t, e) });
    } finally {
      setBusy(false);
    }
  }

  async function print(d: PaymentDetail) {
    if (printerName()) {
      try { await printLines(receiptLines(d, t as never, decimalsOf)); return; } catch { /* fall back to the page printer */ }
    }
    if (!autoPrint) return;
    setPrintDoc(d);
    await new Promise((r) => setTimeout(r, 60));
    await printPage(t('payments.receipt')).catch(() => {});
    setPrintDoc(null);
  }

  async function fixPayment(p: Payment) {
    if (confirmFix !== p.id) { setConfirmFix(p.id); return; }
    setConfirmFix(null);
    setBusy(true);
    try {
      await api(`/companies/${cid}/payments/${p.id}/void`, { method: 'POST', body: { reason: t('mx.j.fixReason') } });
      window.dispatchEvent(new Event('paxve:paid'));
      const c = await refreshPicked(p.customer_id);
      setFlash({ ok: true, text: t('mx.j.fixed', { n: p.number }) });
      if (c) {
        setAmtBase(p.currency === base ? String(Number(p.amount)) : '');
        setAmtLocal(p.currency === local && local !== base ? String(Number(p.amount)) : '');
        setTimeout(() => { baseInput.current?.focus(); baseInput.current?.select(); }, 20);
      }
    } catch (e) {
      setFlash({ ok: false, text: errorText(t, e) });
    } finally {
      setBusy(false);
    }
  }

  // keys while this window is in front: F9 save, Esc back to the search, F2 (from the shell) search
  useEffect(() => {
    if (!active) return;
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (e.key === 'F9') { e.preventDefault(); void save(); }
      else if (e.key === 'Escape' && picked && !document.querySelector('.mantine-Modal-root')) { e.preventDefault(); backToSearch(); }
    };
    const onSearch = () => backToSearch();
    window.addEventListener('keydown', onKey);
    window.addEventListener('paxve:jibaya-search', onSearch);
    return () => { window.removeEventListener('keydown', onKey); window.removeEventListener('paxve:jibaya-search', onSearch); };
  });
  useEffect(() => { if (active && !picked) setTimeout(() => search.current?.focus(), 50); }, [active]); // eslint-disable-line react-hooks/exhaustive-deps
  // arriving with a subscriber already chosen (from "late payers"): load it like a pick
  useEffect(() => { if (picked) pick(picked); }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const amountKey = (e: KeyboardEvent<HTMLInputElement>) => { if (e.key === 'Enter') { e.preventDefault(); void save(); } };
  const dec = (c: string) => decimalsOf(c);

  const subCols: Col<Customer>[] = useMemo(() => [
    { key: 'name', title: t('mx.c.name'), get: (r) => r.name, width: 220 },
    { key: 'code', title: t('mx.c.code'), get: (r) => r.code, width: 80 },
    { key: 'area', title: t('mx.c.area'), get: (r) => r.area_name ?? '', width: 120 },
    { key: 'phone', title: t('mx.c.phone'), get: (r) => r.phone, width: 110 },
    { key: 'address', title: t('mx.c.address'), get: (r) => r.address, width: 160 },
    { key: 'status', title: t('mx.c.status'), get: (r) => t(`customers.statuses.${r.status}`), width: 80,
      render: (r) => <span className={`mx-pill ${r.status === 'active' ? 'on' : 'off'}`}>{t(`customers.statuses.${r.status}`)}</span> },
    { key: 'due', title: t('mx.c.due', { cur: base }), get: (r) => due(r, base) || '', num: true, width: 100, filter: false,
      cls: (r) => (due(r, base) > 0 ? 'red' : ''), total: 'sum' },
    ...(local !== base ? [{ key: 'dueL', title: t('mx.c.due', { cur: local }), get: (r: Customer) => due(r, local) || '', num: true, width: 110, decimals: 0, filter: false,
      cls: (r: Customer) => (due(r, local) > 0 ? 'red' : ''), total: 'sum' as const }] : []),
  ], [t, base, local, due]);

  const docCols: Col<DocRow>[] = useMemo(() => [
    { key: 'kind', title: t('mx.d.kind'), get: (r) => (r.kind === 'invoice' ? (r.inv?.kind === 'refund' ? t('mx.d.refund') : t('mx.d.invoice')) : r.pay?.kind === 'credit_note' ? t('mx.d.creditNote') : t('mx.d.receipt')), width: 90 },
    { key: 'number', title: t('mx.d.number'), get: (r) => r.number, width: 80 },
    { key: 'date', title: t('mx.d.date'), get: (r) => r.date, render: (r) => formatDate(r.date), width: 100 },
    { key: 'period', title: t('mx.d.period'), get: (r) => r.period, width: 80 },
    { key: 'amount', title: t('mx.d.amount'), get: (r) => r.amount, render: (r) => formatAmount(r.amount, dec(r.currency)), num: true, width: 100 },
    { key: 'paid', title: t('mx.d.paid'), get: (r) => r.paid, render: (r) => (r.paid ? formatAmount(r.paid, dec(r.currency)) : ''), num: true, width: 100 },
    { key: 'balance', title: t('mx.d.balance'), get: (r) => r.balance, render: (r) => (r.balance ? formatAmount(r.balance, dec(r.currency)) : ''), num: true, width: 100,
      cls: (r) => (r.kind === 'invoice' && Number(r.balance) > 0 && r.status !== 'void' ? 'red' : r.kind === 'invoice' ? 'ok' : '') },
    { key: 'cur', title: t('mx.d.currency'), get: (r) => r.currency, width: 60 },
    { key: 'status', title: t('mx.d.status'), get: (r) => r.status, width: 90,
      render: (r) => (r.status === 'void' ? <span className="mx-pill off">{t('invoices.statuses.void')}</span>
        : r.kind === 'invoice' ? <span className={`mx-pill ${r.status === 'paid' ? 'on' : r.status === 'partial' ? 'warn' : 'off'}`}>{t(`invoices.statuses.${r.status}`)}</span>
          : <span className="mx-pill on">{t('mx.d.posted')}</span>) },
    { key: 'act', title: '', get: () => '', filter: false, width: 150,
      render: (r) => (r.kind === 'payment' && r.pay && r.status === 'posted' && r.pay.kind !== 'credit_note' && can(me, 'collections.void')
        ? <button className="mx-btn sm" disabled={busy} onClick={(e) => { e.stopPropagation(); void fixPayment(r.pay!); }}>
          {confirmFix === r.id ? t('mx.j.fixConfirm') : t('mx.j.fix')}</button> : null) },
  ], [t, dec, me, busy, confirmFix]); // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <div className="mx-body" style={{ gridTemplateColumns: 'minmax(0, 1.7fr) minmax(290px, 1fr)', gridTemplateRows: 'minmax(0, 1.1fr) minmax(0, 1fr)' }}>
      <section className="mx-box">
        <h3>{t('mx.j.subscribers')} <small>{t('mx.j.searchHint')}</small></h3>
        <DataGrid rows={list} cols={subCols} rowKey={(r) => r.id} title={t('mx.j.subscribers')} loading={loading && !list.length}
          filters={filters} onFilters={setFilters} firstFilterRef={search} selected={picked?.id ?? null} onActivate={pick} footer />
      </section>

      <section className="mx-box" style={{ gridRow: 'span 2' }}>
        <h3>{t('mx.j.payTitle')}</h3>
        <div style={{ padding: 12, display: 'flex', flexDirection: 'column', gap: 10, overflow: 'auto' }}>
          <div style={{ fontWeight: 700, fontSize: 16, color: 'var(--mx-tint-dark)', minHeight: 24 }}>
            {picked ? <>{picked.name}<div style={{ fontWeight: 400, fontSize: 13, color: 'var(--mx-muted)' }}>#{picked.code} · {picked.area_name ?? '—'} · {picked.phone}</div></>
              : <span style={{ fontWeight: 400, fontSize: 13, color: 'var(--mx-muted)' }}>{t('mx.j.pickFirst')}</span>}
          </div>
          {picked && (
            <div style={{ fontSize: 13 }}>
              {t('mx.j.owes')}: {picked.due.length === 0 ? <b style={{ color: 'var(--mx-green)' }}>{t('customers.nothingDue')}</b>
                : picked.due.map((d) => <b key={d.currency} dir="ltr" style={{ color: 'var(--mx-red)', marginInlineEnd: 8 }}>{formatAmount(d.due, dec(d.currency))} {d.currency}</b>)}
              {picked.credit.length > 0 && <span> · {t('mx.j.credit')}: {picked.credit.map((d) => `${formatAmount(d.credit, dec(d.currency))} ${d.currency}`).join(' + ')}</span>}
            </div>
          )}
          <div className="mx-fld"><label htmlFor="mx-amt-base">{t('mx.j.received', { cur: base })}</label>
            <input id="mx-amt-base" ref={baseInput} inputMode="decimal" disabled={!picked || !boxBase} value={amtBase} onKeyDown={amountKey}
              onChange={(e) => setAmtBase(e.currentTarget.value.replace(/[^\d.]/g, ''))} /></div>
          {local !== base && (
            <div className="mx-fld"><label htmlFor="mx-amt-local">{t('mx.j.received', { cur: local })}</label>
              <input id="mx-amt-local" inputMode="decimal" disabled={!picked || !boxLocal} value={amtLocal} onKeyDown={amountKey}
                onChange={(e) => setAmtLocal(e.currentTarget.value.replace(/[^\d.]/g, ''))} /></div>
          )}
          <div className="mx-fld"><span>{t('mx.j.applied', { cur: payCur })}</span><output>{apply ? formatAmount(apply, dec(payCur)) : '0'}</output></div>
          <div className="mx-fld"><span>{t('mx.j.change', { cur: changeBox?.currency ?? local })}</span>
            <output style={{ color: change > 0 ? '#b45309' : undefined }}>{formatAmount(change, changeBox ? dec(changeBox.currency) : 0)}</output></div>
          <button className="mx-btn main" disabled={!picked || busy} onClick={() => void save()}>{t('mx.j.save')} <kbd>F9</kbd></button>
          <label style={{ fontSize: 12.5, color: 'var(--mx-muted)', display: 'flex', gap: 6, alignItems: 'center' }}>
            <input type="checkbox" checked={autoPrint} onChange={(e) => { const v = e.currentTarget.checked; setAutoPrint(v); try { localStorage.setItem('paxve.autoprint', v ? '1' : '0'); } catch { /* */ } }} />
            {t('mx.j.autoPrint')}{printerName() ? ` · ${t('mx.j.btPrinter', { name: printerName() })}` : ''}
          </label>
          {!boxBase && <div className="mx-flash bad">{t('payments.noMethods')}</div>}
          {flash && <div className={`mx-flash ${flash.ok ? 'good' : 'bad'}`} role="status">{flash.text}</div>}
          {last && (
            <div style={{ border: '1px dashed #b9b9b9', padding: '8px 10px', fontSize: 13, lineHeight: 1.7, background: '#fff' }}>
              <b>{t('payments.receipt')} #{last.payment.number}</b> — {last.payment.customer_name}<br />
              <span dir="ltr">{last.tenders.map((x) => `${formatAmount(x.amount, dec(x.currency))} ${x.currency}`).join(' + ')}</span>
              {Number(last.payment.change_amount) > 0 && last.payment.change_currency && <><br />{t('payments.change')}: <span dir="ltr">{formatAmount(last.payment.change_amount, dec(last.payment.change_currency))} {last.payment.change_currency}</span></>}
              <br /><button className="mx-btn sm" onClick={() => void print(last)}>{t('mx.j.reprint')}</button>
            </div>
          )}
          <div style={{ fontSize: 12, color: 'var(--mx-muted)', display: 'flex', flexWrap: 'wrap', gap: '2px 10px' }}>
            <span><kbd>F2</kbd> {t('mx.keyF2')}</span><span><kbd>Enter</kbd> {t('mx.j.enterSaves')}</span><span><kbd>Esc</kbd> {t('mx.keyEsc')}</span>
          </div>
        </div>
      </section>

      <section className="mx-box">
        <h3>{t('mx.j.docs')} {picked && <small>{picked.name}</small>}</h3>
        <DataGrid rows={docs} cols={docCols} rowKey={(r) => r.key} title={`${t('mx.j.docs')} ${picked?.name ?? ''}`} filterRow={false}
          empty={picked ? t('common.empty') : t('mx.j.pickFirst')}
          onActivate={() => { if (picked) openScreen('customer', picked.id); }} />
      </section>
      {printDoc && createPortal(<div className="mx-printhost"><Receipt d={printDoc} /></div>, document.body)}
    </div>
  );
}

// ---------- جباية / باركود ----------

function Scan({ me, active, date, methods }: { me: Me; active: boolean; date: string; methods: PaymentMethod[] }) {
  const { t } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [code, setCode] = useState('');
  const [busy, setBusy] = useState(false);
  const [log, setLog] = useState<{ ok: boolean; text: string; id: number }[]>([]);
  const input = useRef<HTMLInputElement>(null);
  useEffect(() => { if (active) input.current?.focus(); }, [active]);
  if (!company) return null;
  const cid = company.id;
  let n = 0;

  async function go() {
    const no = code.trim();
    if (!no || busy) return;
    setBusy(true);
    const add = (ok: boolean, text: string) => setLog((l) => [{ ok, text, id: Date.now() + n++ }, ...l].slice(0, 200));
    try {
      const r = await api<{ rows: Invoice[] }>(`/companies/${cid}/invoices?q=${encodeURIComponent(no)}&limit=20`);
      const inv = r.rows.find((x) => String(x.number) === no);
      if (!inv) add(false, t('mx.s.notFound', { no }));
      else if (inv.status === 'void') add(false, t('mx.s.void', { no }));
      else if (Number(inv.balance) <= 0) add(false, t('mx.s.paid', { no, name: inv.customer_name }));
      else {
        const box = pickBox(methods, inv.currency, me);
        if (!box) add(false, t('payments.noMethods'));
        else {
          const bal = String(Number(inv.balance));
          await api<PaymentDetail>(`/companies/${cid}/payments`, {
            method: 'POST', body: { customer_id: inv.customer_id, date, currency: inv.currency, amount: bal, tenders: [{ method_id: box.id, amount: bal }] },
          });
          window.dispatchEvent(new Event('paxve:paid'));
          add(true, t('mx.s.ok', { no, name: inv.customer_name, amount: `${formatAmount(bal, decimalsOf(inv.currency))} ${inv.currency}`, period: inv.period ?? '' }));
        }
      }
    } catch (e) {
      add(false, `${no}: ${errorText(t, e)}`);
    } finally {
      setCode('');
      setBusy(false);
      setTimeout(() => input.current?.focus(), 10);
    }
  }

  return (
    <div className="mx-body" style={{ gridTemplateColumns: '1fr' }}>
      <section className="mx-box">
        <h3>{t('mx.j.modes.scan')} <small>{t('mx.s.hint')}</small></h3>
        <div style={{ padding: 16, display: 'flex', flexDirection: 'column', gap: 12, maxWidth: 680, minHeight: 0 }}>
          <input ref={input} aria-label={t('mx.s.label')} value={code} inputMode="numeric" autoComplete="off" disabled={busy}
            style={{ fontSize: 24, padding: 10, border: '2px solid var(--mx-tint)', borderRadius: 4, textAlign: 'center', letterSpacing: 2, direction: 'ltr' }}
            onChange={(e) => setCode(e.currentTarget.value.replace(/\s/g, ''))} onKeyDown={(e) => { if (e.key === 'Enter') { e.preventDefault(); void go(); } }} />
          <div style={{ fontSize: 12.5, color: 'var(--mx-muted)' }}>{t('mx.s.note')}</div>
          <ul style={{ listStyle: 'none', margin: 0, padding: 0, overflow: 'auto', maxHeight: '55vh' }} aria-live="polite">
            {log.map((x) => <li key={x.id} className={`mx-flash ${x.ok ? 'good' : 'bad'}`} style={{ marginBottom: 3 }}>{x.ok ? '✓ ' : '✗ '}{x.text}</li>)}
          </ul>
        </div>
      </section>
    </div>
  );
}

// ---------- إدخال قيم العدادات ----------

interface ReadRow {
  subscription_id: string; customer_id: string; code: string; name: string; area_name: string | null; meter_no: string;
  plan_name: string; unit_label: string; previous: string; current: string | null; invoiced: boolean;
}

function ReadingsMode({ me, active }: { me: Me; active: boolean }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const [period, setPeriod] = useState(currentPeriod());
  const [areas, setAreas] = useState<Area[]>([]);
  const [area, setArea] = useState('');
  const [rows, setRows] = useState<ReadRow[] | null>(null);
  const [vals, setVals] = useState<Record<string, string>>({});
  const [state, setState] = useState<Record<string, 'ok' | 'bad' | 'busy'>>({});
  const [f, setF] = useState({ name: '', meter: '' });
  const canEdit = can(me, 'collections.create');
  const cid = company?.id;

  useEffect(() => { if (cid) api<Area[]>(`/companies/${cid}/areas`).then(setAreas).catch(() => {}); }, [cid]);
  useEffect(() => {
    if (!cid || !/^\d{4}-\d{2}$/.test(period)) return;
    setRows(null);
    api<ReadRow[]>(`/companies/${cid}/readings?period=${period}&area_id=${area}`).then((r) => {
      setRows(r);
      setVals(Object.fromEntries(r.map((x) => [x.subscription_id, x.current ? String(Number(x.current)) : ''])));
      setState({});
    }).catch(() => setRows([]));
  }, [cid, period, area]);
  useEffect(() => { if (active) setTimeout(() => document.querySelector<HTMLInputElement>('input[data-rd="0"]')?.focus(), 80); }, [active, rows]);

  if (!company) return null;
  const view = (rows ?? []).filter((r) => r.name.includes(f.name) && (r.meter_no ?? '').includes(f.meter));

  async function saveOne(r: ReadRow, idx: number) {
    const v = vals[r.subscription_id] ?? '';
    const next = () => { const el = document.querySelector<HTMLInputElement>(`input[data-rd="${idx + 1}"]`); el?.focus(); el?.select(); };
    if (v === '' || v === (r.current ? String(Number(r.current)) : '')) { next(); return; }
    if (Number(v) < Number(r.previous)) { setState((s) => ({ ...s, [r.subscription_id]: 'bad' })); return; }
    setState((s) => ({ ...s, [r.subscription_id]: 'busy' }));
    next();
    try {
      await api(`/companies/${company!.id}/readings`, { method: 'POST', body: { period, readings: [{ subscription_id: r.subscription_id, reading: v }] } });
      setState((s) => ({ ...s, [r.subscription_id]: 'ok' }));
      setRows((rs) => rs?.map((x) => (x.subscription_id === r.subscription_id ? { ...x, current: v } : x)) ?? rs);
    } catch {
      setState((s) => ({ ...s, [r.subscription_id]: 'bad' }));
    }
  }
  const done = (rows ?? []).filter((r) => r.current).length;

  return (
    <div className="mx-body" style={{ gridTemplateColumns: '1fr' }}>
      <section className="mx-box">
        <h3>{t('mx.j.modes.read')}
          <input type="month" value={period} aria-label={t('readings.period')} onChange={(e) => setPeriod(e.currentTarget.value)} />
          <select value={area} aria-label={t('readings.area')} onChange={(e) => setArea(e.currentTarget.value)}>
            <option value="">{t('readings.allAreas')}</option>
            {areas.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
          </select>
          <small>{t('mx.r.hint')}</small><span className="mx-sp" />
          <small>{t('mx.r.progress', { done, n: rows?.length ?? 0 })}</small>
        </h3>
        <div className="mx-grid">
          <table style={{ width: '100%' }}>
            <thead>
              <tr><th>{t('mx.c.code')}</th><th>{t('mx.c.name')}</th><th>{t('mx.c.area')}</th><th>{t('readings.meter')}</th><th>{t('mx.r.plan')}</th>
                <th className="num">{t('readings.previous')}</th><th style={{ width: 130 }}>{t('readings.current')}</th><th className="num">{t('readings.used')}</th></tr>
              <tr className="mx-frow">
                <td /><td><input aria-label={t('mx.c.name')} value={f.name} onChange={(e) => setF({ ...f, name: e.currentTarget.value })} /></td><td />
                <td><input aria-label={t('readings.meter')} value={f.meter} onChange={(e) => setF({ ...f, meter: e.currentTarget.value })} /></td><td /><td /><td /><td />
              </tr>
            </thead>
            <tbody>
              {rows === null && <tr><td colSpan={8} className="muted">{t('mx.loading')}</td></tr>}
              {rows !== null && view.length === 0 && <tr><td colSpan={8} className="muted">{t('readings.empty')}</td></tr>}
              {view.map((r, idx) => {
                const v = vals[r.subscription_id] ?? '';
                const used = v !== '' ? Number(v) - Number(r.previous) : null;
                const st = state[r.subscription_id];
                return (
                  <tr key={r.subscription_id}>
                    <td>{r.code}</td><td>{r.name}</td><td className="muted">{r.area_name ?? ''}</td><td>{r.meter_no}</td><td className="muted">{r.plan_name}</td>
                    <td className="num">{Number(r.previous)}</td>
                    <td>{r.invoiced ? <span>{Number(r.current)} <span className="mx-pill on">{t('readings.invoiced')}</span></span> : (
                      <input className={`mx-cell ${st === 'ok' ? 'ok' : st === 'bad' ? 'bad' : ''}`} data-rd={idx} inputMode="decimal" disabled={!canEdit}
                        aria-label={`${t('readings.current')} ${r.name}`} value={v}
                        onChange={(e) => { const x = e.currentTarget.value.replace(/[^\d.]/g, ''); setVals((m) => ({ ...m, [r.subscription_id]: x })); setState((s) => { const n = { ...s }; delete n[r.subscription_id]; return n; }); }}
                        onKeyDown={(e) => {
                          if (e.key === 'Enter' || e.key === 'ArrowDown') { e.preventDefault(); void saveOne(r, idx); }
                          else if (e.key === 'ArrowUp') { e.preventDefault(); document.querySelector<HTMLInputElement>(`input[data-rd="${idx - 1}"]`)?.focus(); }
                        }} />
                    )}</td>
                    <td className={`num ${used !== null && used < 0 ? 'red' : ''}`}>{used !== null ? `${Math.round(used * 100) / 100} ${r.unit_label}` : ''}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  );
}

// ---------- متأخرين عن الدفع ----------

function Late({ onPick }: { onPick: (c: Customer) => void }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const [rows, setRows] = useState<Customer[] | null>(null);
  const cid = company?.id;
  useEffect(() => { if (cid) api<Customer[]>(`/companies/${cid}/customers?due=1&limit=1000`).then(setRows).catch(() => setRows([])); }, [cid]);
  if (!company) return null;
  const base = company.base_currency;
  const local = company.local_currency;
  const due = (c: Customer, cur: string) => Number(c.due.find((d) => d.currency === cur)?.due ?? 0);
  const cols: Col<Customer>[] = [
    { key: 'name', title: t('mx.c.name'), get: (r) => r.name, width: 220 },
    { key: 'code', title: t('mx.c.code'), get: (r) => r.code, width: 80 },
    { key: 'area', title: t('mx.c.area'), get: (r) => r.area_name ?? '', width: 130 },
    { key: 'phone', title: t('mx.c.phone'), get: (r) => r.phone, width: 120 },
    { key: 'status', title: t('mx.c.status'), get: (r) => t(`customers.statuses.${r.status}`), width: 90,
      render: (r) => <span className={`mx-pill ${r.status === 'active' ? 'on' : 'off'}`}>{t(`customers.statuses.${r.status}`)}</span> },
    { key: 'due', title: t('mx.c.due', { cur: base }), get: (r) => due(r, base), num: true, width: 110, cls: () => 'red', total: 'sum', filter: false },
    ...(local !== base ? [{ key: 'dueL', title: t('mx.c.due', { cur: local }), get: (r: Customer) => due(r, local), num: true, decimals: 0, width: 130, total: 'sum' as const, filter: false }] : []),
  ];
  return (
    <div className="mx-body" style={{ gridTemplateColumns: '1fr' }}>
      <section className="mx-box">
        <h3>{t('mx.j.modes.late')} <small>{t('mx.l.hint')}</small></h3>
        <DataGrid rows={(rows ?? []).slice().sort((a, b) => due(b, base) - due(a, base))} cols={cols} rowKey={(r) => r.id}
          title={t('mx.j.modes.late')} loading={rows === null} onActivate={onPick} footer />
      </section>
    </div>
  );
}

// ---------- تسليم الجباية ----------

interface Box { id: string; name: string; kind: string; currency: string; user_id: string | null; user_name: string | null; account_code: string; balance: string }

function Settle({ me }: { me: Me }) {
  const { t } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [boxes, setBoxes] = useState<Box[] | null>(null);
  const [sel, setSel] = useState<Box | null>(null);
  const [to, setTo] = useState('');
  const [busy, setBusy] = useState(false);
  const [flash, setFlash] = useState<{ ok: boolean; text: string } | null>(null);
  const [slip, setSlip] = useState<{ box: Box; to: string; amount: string } | null>(null);
  const cid = company?.id;
  const load = useCallback(() => { if (cid) api<Box[]>(`/companies/${cid}/cash/balances`).then(setBoxes).catch(() => setBoxes([])); }, [cid]);
  useEffect(load, [load]);
  if (!company) return null;
  const personal = (boxes ?? []).filter((b) => b.user_id);
  const targets = (boxes ?? []).filter((b) => !b.user_id && sel && b.currency === sel.currency);
  const canMove = can(me, 'collections.edit');

  async function receive() {
    if (!sel || !to || busy) return;
    setBusy(true);
    try {
      const amount = String(Number(sel.balance));
      await api(`/companies/${company!.id}/cash/transfers`, { method: 'POST', body: { from_method_id: sel.id, to_method_id: to, amount, notes: t('mx.j.modes.col'), date: today() } });
      const toName = boxes?.find((b) => b.id === to)?.name ?? '';
      setFlash({ ok: true, text: t('mx.col.done', { name: sel.user_name ?? sel.name, amount: `${formatAmount(amount, decimalsOf(sel.currency))} ${sel.currency}` }) });
      setSlip({ box: sel, to: toName, amount });
      setTimeout(() => { void printPage(t('mx.j.modes.col')).finally(() => setSlip(null)); }, 60);
      setSel(null);
      load();
    } catch (e) {
      setFlash({ ok: false, text: errorText(t, e) });
    } finally {
      setBusy(false);
    }
  }

  const cols: Col<Box>[] = [
    { key: 'user', title: t('mx.col.collector'), get: (r) => r.user_name ?? '', width: 160 },
    { key: 'name', title: t('mx.col.box'), get: (r) => r.name, width: 180 },
    { key: 'cur', title: t('mx.d.currency'), get: (r) => r.currency, width: 70 },
    { key: 'bal', title: t('mx.col.balance'), get: (r) => r.balance, render: (r) => formatAmount(r.balance, decimalsOf(r.currency)), num: true, width: 140,
      cls: (r) => (Number(r.balance) > 0 ? 'red' : 'muted') },
    { key: 'acc', title: t('mx.col.account'), get: (r) => r.account_code, width: 90 },
  ];
  return (
    <div className="mx-body" style={{ gridTemplateColumns: 'minmax(0, 1.6fr) minmax(280px, 1fr)' }}>
      <section className="mx-box">
        <h3>{t('mx.col.title')} <small>{t('mx.col.hint')}</small></h3>
        <DataGrid rows={personal} cols={cols} rowKey={(r) => r.id} title={t('mx.col.title')} loading={boxes === null}
          selected={sel?.id ?? null} onSelect={(b) => { setSel(b); setTo(''); }} onActivate={(b) => { setSel(b); setTo(''); }} />
      </section>
      <section className="mx-box">
        <h3>{t('mx.col.receive')}</h3>
        <div style={{ padding: 12, display: 'flex', flexDirection: 'column', gap: 10 }}>
          {sel ? <>
            <div><b>{sel.user_name ?? sel.name}</b> — {sel.name}</div>
            <div className="mx-fld"><span>{t('mx.col.amount')}</span><output>{formatAmount(sel.balance, decimalsOf(sel.currency))} {sel.currency}</output></div>
            <div className="mx-fld"><label htmlFor="mx-col-to">{t('mx.col.to')}</label>
              <select id="mx-col-to" value={to} onChange={(e) => setTo(e.currentTarget.value)}>
                <option value="" />
                {targets.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
              </select></div>
            <button className="mx-btn main" disabled={!to || busy || !canMove || Number(sel.balance) <= 0} onClick={() => void receive()}>{t('mx.col.receiveBtn')}</button>
          </> : <div style={{ color: 'var(--mx-muted)' }}>{t('mx.col.pick')}</div>}
          {flash && <div className={`mx-flash ${flash.ok ? 'good' : 'bad'}`}>{flash.text}</div>}
        </div>
      </section>
      {slip && createPortal(
        <div className="mx-printhost"><div className="print-doc receipt" dir="rtl">
          <h3 style={{ textAlign: 'center', margin: 0 }}>{t('mx.j.modes.col')}</h3>
          <p>{formatDate(today())}</p>
          <p>{t('mx.col.collector')}: <b>{slip.box.user_name}</b><br />{slip.box.name} → {slip.to}</p>
          <p style={{ fontSize: 16 }}><b>{formatAmount(slip.amount, decimalsOf(slip.box.currency))} {slip.box.currency}</b></p>
          <p>{t('mx.v.receiverSign')}: ..................</p>
        </div></div>, document.body)}
    </div>
  );
}
