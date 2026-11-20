import { useEffect, useMemo, useState } from 'react';
import { Select } from '@mantine/core';
import { useDebouncedValue } from '@mantine/hooks';
import { useTranslation } from 'react-i18next';
import { api, can, type Me } from '../../api';
import { useCompany } from '../../company';
import { accountLabel, errorText, formatAmount, formatDate, today } from '../../util';
import DataGrid, { type Col } from '../../components/DataGrid';
import { Help, useAccounts, useBoxes, useLoad } from './common';

interface Cheque {
  id: string; number: number; direction: 'in' | 'out'; cheque_no: string; bank_name: string; drawer: string; receive_date: string;
  due_date: string; currency: string; amount: string; remaining: string; status: string; frozen: boolean;
  customer_id: string | null; customer_name: string | null; supplier_id: string | null; supplier_name: string | null;
  account_id: string | null; account_code: string | null; account_name: string | null; bank_name_box: string | null;
  endorsed_to: string | null; notes: string; days_to_due: number; created_by: string;
}
interface Event { id: string; kind: string; event_date: string; amount: string; status_before: string; status_after: string; method: string | null;
  supplier: string | null; undone: boolean; notes: string; created_by: string; created_at: string }
interface Total { direction: string; status: string; currency: string; cheques: number; amount: string }
interface Party { id: string; name: string; code?: string }

const STATUSES = ['open', '', 'in_hand', 'deposited', 'collected', 'endorsed', 'bounced', 'returned', 'settled', 'issued', 'paid', 'cancelled'];

/** إدارة الشيكات: received and issued cheques, every step of their life, due-date register. */
export default function Cheques({ me, openId }: { me: Me; openId: string }) {
  const { t } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [dir, setDir] = useState<'in' | 'out'>('in');
  const [status, setStatus] = useState('open');
  const [sel, setSel] = useState<string | null>(openId || null);
  const [creating, setCreating] = useState(!openId);
  const cid = company?.id;
  const [data, reload, loading] = useLoad<{ rows: Cheque[]; totals: Total[] }>(cid ? `/companies/${cid}/cheques?direction=${dir}&status=${status}` : null);
  useEffect(() => { if (openId) { setSel(openId); setCreating(false); } }, [openId]);
  const canEdit = can(me, 'collections.edit');
  if (!company) return null;

  const cols: Col<Cheque>[] = [
    { key: 'n', title: '#', get: (r) => r.number, width: 60 },
    { key: 'no', title: t('ax.ch.no'), get: (r) => r.cheque_no, width: 100 },
    { key: 'bank', title: t('ax.ch.bank'), get: (r) => r.bank_name, width: 120 },
    { key: 'party', title: dir === 'in' ? t('ax.ch.from') : t('ax.ch.to'),
      get: (r) => r.customer_name ?? r.supplier_name ?? (r.account_code ? `${r.account_code} · ${r.account_name}` : ''), width: 200 },
    { key: 'due', title: t('ax.ch.due'), get: (r) => r.due_date, render: (r) => formatDate(r.due_date), width: 110,
      cls: (r) => (['in_hand', 'deposited', 'issued'].includes(r.status) && r.days_to_due <= 0 ? 'red' : '') },
    { key: 'days', title: t('ax.ch.days'), get: (r) => (['in_hand', 'deposited', 'issued'].includes(r.status) ? r.days_to_due : ''), num: true, decimals: 0, filter: false, width: 95 },
    { key: 'amt', title: t('ax.amount'), get: (r) => r.remaining, render: (r) => `${formatAmount(r.remaining, decimalsOf(r.currency))} ${r.currency}`,
      num: true, filter: false, width: 150 },
    { key: 'st', title: t('ax.status'), get: (r) => t(`ax.ch.st.${r.status}`), width: 120,
      render: (r) => <span className={`mx-pill ${['bounced', 'returned', 'cancelled'].includes(r.status) ? 'off' : ['collected', 'paid', 'settled'].includes(r.status) ? 'on' : 'warn'}`}>
        {t(`ax.ch.st.${r.status}`)}{r.frozen ? ` · ${t('ax.ch.frozen')}` : ''}</span> },
  ];

  return (
    <div className="mx-col">
      <div className="mx-modes" role="tablist">
        {(['in', 'out'] as const).map((d) => (
          <button key={d} className="mx-mode" role="tab" aria-selected={dir === d} onClick={() => { setDir(d); setSel(null); }}>{t(`ax.ch.dir.${d}`)}</button>
        ))}
      </div>
      <div className="mx-strip">
        <label>{t('ax.status')}
          <select value={status} onChange={(e) => setStatus(e.currentTarget.value)}>
            {STATUSES.filter((s) => s === '' || s === 'open' || (dir === 'in' ? !['issued', 'paid', 'cancelled'].includes(s) : ['issued', 'paid', 'bounced', 'cancelled'].includes(s)))
              .map((s) => <option key={s} value={s}>{s === '' ? t('common.all') : s === 'open' ? t('ax.ch.openOnly') : t(`ax.ch.st.${s}`)}</option>)}
          </select>
        </label>
        {(data?.totals ?? []).filter((x) => x.direction === dir).map((x) => (
          <span key={x.status + x.currency}>{t(`ax.ch.st.${x.status}`)}: <b dir="ltr">{x.cheques} · {formatAmount(x.amount, decimalsOf(x.currency))} {x.currency}</b></span>
        ))}
        <span className="mx-sp" />
        {canEdit && <button className="mx-btn sm" onClick={() => {
          setCreating(true); setSel(null);
          // the form may already be on screen: bring it into view and put the cursor in it (audit P7-F5)
          setTimeout(() => { const el = document.querySelector<HTMLElement>('.mx-pane:not([hidden]) [data-ch-new] select'); el?.scrollIntoView({ block: 'center' }); el?.focus(); }, 30);
        }}>+ {t(`ax.ch.new.${dir}`)}</button>}
        <Help topic="cheques" />
      </div>
      <div className="mx-body" style={{ gridTemplateColumns: 'minmax(0, 1.6fr) minmax(300px, 1fr)' }}>
        <section className="mx-box">
          <h3>{t('ax.t.cheques')} <small>{t('ax.ch.hint')}</small></h3>
          <DataGrid rows={data?.rows ?? []} cols={cols} rowKey={(r) => r.id} loading={loading} title={t('ax.t.cheques')}
            selected={sel} onSelect={(r) => { setSel(r.id); setCreating(false); }} onActivate={(r) => { setSel(r.id); setCreating(false); }} />
        </section>
        <section className="mx-box" style={{ overflow: 'auto' }}>
          {creating && canEdit
            ? <NewCheque dir={dir} onDone={(id) => { reload(); setSel(id); setCreating(false); }} />
            : sel ? <ChequeDetail id={sel} canEdit={canEdit} onChanged={reload} /> : <div className="mx-empty-box">{t('ax.ch.pick')}</div>}
        </section>
      </div>
    </div>
  );
}

function usePartySearch(kind: 'customers' | 'suppliers', q: string): Party[] {
  const { company } = useCompany();
  const [list, setList] = useState<Party[]>([]);
  const [dq] = useDebouncedValue(q, 250);
  useEffect(() => {
    if (!company) return;
    const path = kind === 'customers' ? `/companies/${company.id}/customers?q=${encodeURIComponent(dq)}&limit=30` : `/companies/${company.id}/suppliers`;
    api<Party[]>(path).then(setList).catch(() => setList([]));
  }, [company, kind, dq]);
  return list;
}

function NewCheque({ dir, onDone }: { dir: 'in' | 'out'; onDone: (id: string) => void }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const accounts = useAccounts();
  const [partyKind, setPartyKind] = useState<'party' | 'account'>('party');
  const [q, setQ] = useState('');
  const parties = usePartySearch(dir === 'in' ? 'customers' : 'suppliers', q);
  const [f, setF] = useState({ cheque_no: '', bank_name: '', drawer: '', receive_date: today(), due_date: today(),
    currency: company?.base_currency ?? 'USD', amount: '', party: '', account: '', notes: '' });
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<string | null>(null);
  const set = (k: keyof typeof f) => (v: string) => setF((x) => ({ ...x, [k]: v }));
  if (!company) return null;
  const currencies = [...new Set([company.base_currency, company.local_currency])];

  async function save() {
    if (!company || busy) return;
    setBusy(true);
    setMsg(null);
    try {
      const body: Record<string, string> = { direction: dir, cheque_no: f.cheque_no, bank_name: f.bank_name, drawer: f.drawer,
        receive_date: f.receive_date, due_date: f.due_date, currency: f.currency, amount: f.amount, notes: f.notes };
      if (partyKind === 'account') body.account_id = f.account;
      else body[dir === 'in' ? 'customer_id' : 'supplier_id'] = f.party;
      const r = await api<{ cheque: { id: string } }>(`/companies/${company.id}/cheques`, { method: 'POST', body });
      onDone(r.cheque.id);
    } catch (e) {
      setMsg(errorText(t, e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <h3>{t(`ax.ch.new.${dir}`)}</h3>
      <div className="mx-form" data-ch-new>
        <div className="mx-fld"><label>{t('ax.ch.party')}</label>
          <select value={partyKind} onChange={(e) => setPartyKind(e.currentTarget.value as 'party' | 'account')}>
            <option value="party">{dir === 'in' ? t('ax.ch.customer') : t('ax.ch.supplier')}</option>
            <option value="account">{t('ax.ch.otherAccount')}</option>
          </select></div>
        {partyKind === 'party' ? (
          <Select searchable size="sm" placeholder={t('ax.ch.searchParty')} value={f.party || null} onChange={(v) => set('party')(v ?? '')}
            onSearchChange={setQ} data={parties.map((p) => ({ value: p.id, label: p.code ? `${p.code} · ${p.name}` : p.name }))}
            nothingFoundMessage={t('common.empty')} aria-label={t('ax.ch.party')} />
        ) : (
          <Select searchable size="sm" value={f.account || null} onChange={(v) => set('account')(v ?? '')} aria-label={t('ax.ch.otherAccount')}
            data={accounts.filter((a) => a.is_postable).map((a) => ({ value: a.id, label: accountLabel(a) }))} />
        )}
        <div className="mx-fld"><label htmlFor="ch-no">{t('ax.ch.no')}</label><input id="ch-no" value={f.cheque_no} onChange={(e) => set('cheque_no')(e.currentTarget.value)} /></div>
        <div className="mx-fld"><label htmlFor="ch-bank">{t('ax.ch.bank')}</label><input id="ch-bank" value={f.bank_name} onChange={(e) => set('bank_name')(e.currentTarget.value)} /></div>
        <div className="mx-fld"><label htmlFor="ch-drawer">{t('ax.ch.drawer')}</label><input id="ch-drawer" value={f.drawer} onChange={(e) => set('drawer')(e.currentTarget.value)} /></div>
        <div className="mx-fld"><label htmlFor="ch-rd">{dir === 'in' ? t('ax.ch.received') : t('ax.ch.issuedOn')}</label>
          <input id="ch-rd" type="date" value={f.receive_date} onChange={(e) => set('receive_date')(e.currentTarget.value)} /></div>
        <div className="mx-fld"><label htmlFor="ch-due">{t('ax.ch.due')}</label><input id="ch-due" type="date" value={f.due_date} onChange={(e) => set('due_date')(e.currentTarget.value)} /></div>
        <div className="mx-fld"><label htmlFor="ch-cur">{t('mx.d.currency')}</label>
          <select id="ch-cur" value={f.currency} onChange={(e) => set('currency')(e.currentTarget.value)}>{currencies.map((c) => <option key={c}>{c}</option>)}</select></div>
        <div className="mx-fld"><label htmlFor="ch-amt">{t('ax.amount')}</label>
          <input id="ch-amt" inputMode="decimal" value={f.amount} onChange={(e) => set('amount')(e.currentTarget.value.replace(/[^\d.]/g, ''))} /></div>
        <div className="mx-fld"><label htmlFor="ch-notes">{t('customers.notes')}</label><input id="ch-notes" value={f.notes} onChange={(e) => set('notes')(e.currentTarget.value)} /></div>
        <button className="mx-btn main" disabled={busy || !(Number(f.amount) > 0) || !(partyKind === 'party' ? f.party : f.account)} onClick={() => void save()}>{t('common.save')}</button>
        {msg && <div className="mx-flash bad">{msg}</div>}
        <p className="mx-note">{dir === 'in' ? t('ax.ch.inExplain') : t('ax.ch.outExplain')}</p>
      </div>
    </>
  );
}

const ACTIONS: Record<string, string[]> = {
  in_hand: ['deposit', 'collect', 'partial_cash', 'endorse', 'return', 'bounce', 'freeze', 'unfreeze', 'undo'],
  deposited: ['collect', 'bounce', 'freeze', 'unfreeze', 'undo'],
  collected: ['bounce', 'undo'],
  endorsed: ['endorse_return'],
  issued: ['pay', 'bounce', 'cancel'],
  paid: ['undo'],
};

function ChequeDetail({ id, canEdit, onChanged }: { id: string; canEdit: boolean; onChanged: () => void }) {
  const { t } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const boxes = useBoxes();
  const [sq, setSq] = useState('');
  const suppliers = usePartySearch('suppliers', sq);
  const [n, setN] = useState(0);
  const [data] = useLoad<{ cheque: Cheque; events: Event[] }>(company ? `/companies/${company.id}/cheques/${id}?n=${n}` : null);
  const [op, setOp] = useState<string | null>(null);
  const [f, setF] = useState({ date: today(), method_id: '', supplier_id: '', amount: '', notes: '' });
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const c = data?.cheque;
  const ops = useMemo(() => (c ? (ACTIONS[c.status] ?? []).filter((o) => (o === 'freeze' ? !c.frozen : o === 'unfreeze' ? c.frozen : true)) : []), [c]);
  useEffect(() => { setOp(null); setMsg(null); }, [id]);
  if (!company || !c) return <div className="mx-empty-box">{t('mx.loading')}</div>;
  const dec = decimalsOf(c.currency);
  const needBox = op === 'deposit' || op === 'pay' || op === 'partial_cash' || (op === 'collect' && c.status === 'in_hand') || (op === 'bounce' && c.status === 'collected');
  const myBoxes = boxes.filter((b) => b.currency === c.currency && (op === 'partial_cash' ? b.kind !== 'bank' : true));

  async function run() {
    if (!company || !op || busy) return;
    setBusy(true);
    setMsg(null);
    try {
      await api(`/companies/${company.id}/cheques/${id}/${op}`, { method: 'POST', body: f });
      setMsg({ ok: true, text: t(`ax.ch.done.${op}`, { defaultValue: t('common.saved') }) });
      setOp(null);
      setF((x) => ({ ...x, amount: '', notes: '' }));
      setN((x) => x + 1);
      onChanged();
    } catch (e) {
      setMsg({ ok: false, text: errorText(t, e) });
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <h3>{t('ax.ch.cheque')} #{c.number} <small>{c.cheque_no} · {c.bank_name}</small></h3>
      <div className="mx-form">
        <div className="mx-kv">
          <span>{c.direction === 'in' ? t('ax.ch.from') : t('ax.ch.to')}</span><b>{c.customer_name ?? c.supplier_name ?? `${c.account_code} · ${c.account_name}`}</b>
          <span>{t('ax.amount')}</span><b dir="ltr">{formatAmount(c.amount, dec)} {c.currency}{c.remaining !== c.amount ? ` (${t('ax.ch.left')} ${formatAmount(c.remaining, dec)})` : ''}</b>
          <span>{t('ax.ch.due')}</span><b>{formatDate(c.due_date)}</b>
          <span>{t('ax.status')}</span><b>{t(`ax.ch.st.${c.status}`)}{c.frozen ? ` · ${t('ax.ch.frozen')}` : ''}</b>
          {c.drawer && <><span>{t('ax.ch.drawer')}</span><b>{c.drawer}</b></>}
          {c.bank_name_box && <><span>{t('ax.ch.bankBox')}</span><b>{c.bank_name_box}</b></>}
          {c.endorsed_to && <><span>{t('ax.ch.endorsedTo')}</span><b>{c.endorsed_to}</b></>}
        </div>
        {canEdit && ops.length > 0 && (
          <div className="mx-actions">
            {ops.map((o) => <button key={o} className={`mx-btn sm ${op === o ? 'on' : ''}`} onClick={() => { setOp(o); setMsg(null); }}>{t(`ax.ch.op.${o}`)}</button>)}
          </div>
        )}
        {op && (
          <div className="mx-opbox">
            <p className="mx-note">{t(`ax.ch.explain.${op}`)}</p>
            <div className="mx-fld"><label htmlFor="op-date">{t('journal.date')}</label>
              <input id="op-date" type="date" value={f.date} onChange={(e) => setF({ ...f, date: e.currentTarget.value })} /></div>
            {needBox && (
              <div className="mx-fld"><label htmlFor="op-box">{op === 'partial_cash' ? t('mx.col.box') : t('ax.ch.bankBox')}</label>
                <select id="op-box" value={f.method_id} onChange={(e) => setF({ ...f, method_id: e.currentTarget.value })}>
                  <option value="" />
                  {myBoxes.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
                </select></div>
            )}
            {op === 'endorse' && (
              <Select searchable size="sm" placeholder={t('ax.ch.supplier')} value={f.supplier_id || null} onChange={(v) => setF({ ...f, supplier_id: v ?? '' })}
                onSearchChange={setSq} data={suppliers.map((p) => ({ value: p.id, label: p.name }))} aria-label={t('ax.ch.supplier')} />
            )}
            {op === 'partial_cash' && (
              <div className="mx-fld"><label htmlFor="op-amt">{t('ax.amount')}</label>
                <input id="op-amt" inputMode="decimal" value={f.amount} onChange={(e) => setF({ ...f, amount: e.currentTarget.value.replace(/[^\d.]/g, '') })} /></div>
            )}
            <div className="mx-fld"><label htmlFor="op-notes">{t('customers.notes')}</label>
              <input id="op-notes" value={f.notes} onChange={(e) => setF({ ...f, notes: e.currentTarget.value })} /></div>
            <button className="mx-btn main" disabled={busy || (needBox && !f.method_id && !(op === 'collect' && c.bank_name_box)) || (op === 'endorse' && !f.supplier_id)}
              onClick={() => void run()}>{t(`ax.ch.op.${op}`)}</button>
          </div>
        )}
        {msg && <div className={`mx-flash ${msg.ok ? 'good' : 'bad'}`}>{msg.text}</div>}
        <h4 className="mx-sub">{t('ax.ch.history')}</h4>
        <table className="mx-mini">
          <tbody>
            {(data?.events ?? []).map((e) => (
              <tr key={e.id} className={e.undone ? 'muted' : ''}>
                <td>{formatDate(e.event_date)}</td>
                <td>{t(`ax.ch.ev.${e.kind}`, { defaultValue: e.kind })}{e.undone ? ` (${t('ax.ch.undone')})` : ''}</td>
                <td dir="ltr" className="num">{Number(e.amount) ? formatAmount(e.amount, dec) : ''}</td>
                <td>{e.method ?? e.supplier ?? ''}</td>
                <td className="muted">{e.created_by}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </>
  );
}
