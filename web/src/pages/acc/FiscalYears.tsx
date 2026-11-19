import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, can, type Me } from '../../api';
import { useCompany } from '../../company';
import { accountLabel, errorText, formatAmount, formatDate, formatDateTime, notifySaved } from '../../util';
import DataGrid, { type Col } from '../../components/DataGrid';
import { Help, useAccounts, useLoad } from './common';

interface Year { year: number; status: 'open' | 'closed' | 'reopened'; result: string; closed_result: string | null; closed_at: string | null;
  closed_by: string | null; reopened_at: string | null; drafts: number; closing_entry_id: string | null }

/** قيود الإقفال و الإفتتاح: close a year (its result goes to retained earnings, the year is locked) or reopen it. */
export default function FiscalYears({ me }: { me: Me }) {
  const { t } = useTranslation();
  const { company, decimalsOf, reload: reloadCompanies } = useCompany();
  const cid = company?.id;
  const [data, reload, loading] = useLoad<{ years: Year[]; lock_date: string | null; base_currency: string }>(cid ? `/companies/${cid}/fiscal-years` : null);
  const [sel, setSel] = useState<Year | null>(null);
  const [confirm, setConfirm] = useState<'close' | 'reopen' | null>(null);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  useEffect(() => { if (sel && data) setSel(data.years.find((y) => y.year === sel.year) ?? null); // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data]);
  if (!company) return null;
  const dec = decimalsOf(company.base_currency);
  const thisYear = new Date().getFullYear();

  async function act(kind: 'close' | 'reopen') {
    if (!cid || !sel || busy) return;
    setBusy(true);
    setMsg(null);
    try {
      await api(`/companies/${cid}/fiscal-years/${kind}`, { method: 'POST', body: { year: sel.year } });
      setMsg({ ok: true, text: t(kind === 'close' ? 'ax.fy.closedOk' : 'ax.fy.reopenedOk', { year: sel.year }) });
      reload();
      reloadCompanies();
    } catch (e) {
      setMsg({ ok: false, text: errorText(t, e) });
    } finally {
      setBusy(false);
      setConfirm(null);
    }
  }

  const cols: Col<Year>[] = [
    { key: 'y', title: t('ax.fy.year'), get: (r) => r.year, width: 80 },
    { key: 's', title: t('ax.status'), get: (r) => t(`ax.fy.st.${r.status}`), width: 120,
      render: (r) => <span className={`mx-pill ${r.status === 'closed' ? 'on' : r.status === 'reopened' ? 'warn' : ''}`}>{t(`ax.fy.st.${r.status}`)}</span> },
    { key: 'r', title: t('ax.fy.result'), get: (r) => r.result, num: true, decimals: dec, filter: false, width: 150, cls: (r) => (Number(r.result) < 0 ? 'red' : 'ok') },
    { key: 'd', title: t('ax.fy.drafts'), get: (r) => r.drafts, num: true, decimals: 0, filter: false, width: 90, cls: (r) => (r.drafts > 0 ? 'red' : '') },
    { key: 'c', title: t('ax.fy.closedAt'), get: (r) => r.closed_at ?? '', render: (r) => (r.closed_at ? `${formatDateTime(r.closed_at)} · ${r.closed_by ?? ''}` : ''), width: 240 },
  ];
  const canClose = can(me, 'accounting.manage');
  const canReopen = can(me, 'accounting.approve');

  return (
    <div className="mx-col">
      <div className="mx-strip">
        <span>{t('ax.fy.lock')}: <b>{data?.lock_date ? formatDate(data.lock_date) : t('ax.fy.noLock')}</b></span>
        <span className="mx-sp" />
        <Help topic="fiscal" />
      </div>
      <div className="mx-body" style={{ gridTemplateColumns: 'minmax(0, 1.4fr) minmax(300px, 1fr)', gridTemplateRows: 'minmax(0, 1fr) auto' }}>
        <section className="mx-box">
          <h3>{t('ax.t.fiscal')}</h3>
          <DataGrid rows={data?.years ?? []} cols={cols} rowKey={(r) => String(r.year)} loading={loading} title={t('ax.t.fiscal')}
            selected={sel ? String(sel.year) : null} onSelect={(r) => { setSel(r); setConfirm(null); setMsg(null); }} filterRow={false} />
        </section>
        <section className="mx-box" style={{ gridRow: 'span 2' }}>
          <h3>{sel ? `${t('ax.fy.year')} ${sel.year}` : t('ax.fy.pick')}</h3>
          <div className="mx-form">
            {sel && (
              <>
                <div className="mx-kv">
                  <span>{t('ax.fy.result')}</span><b dir="ltr" className={Number(sel.result) < 0 ? 'red' : 'ok'}>{formatAmount(sel.result, dec)} {company.base_currency}</b>
                  <span>{t('ax.status')}</span><b>{t(`ax.fy.st.${sel.status}`)}</b>
                </div>
                {sel.status !== 'closed' && sel.year <= thisYear && canClose && (
                  confirm === 'close' ? (
                    <div className="mx-opbox">
                      <p className="mx-note">{t('ax.fy.closeConfirm', { year: sel.year })}</p>
                      <button className="mx-btn main" disabled={busy} onClick={() => void act('close')}>{t('ax.fy.closeYes', { year: sel.year })}</button>
                      <button className="mx-btn" onClick={() => setConfirm(null)}>{t('common.cancel')}</button>
                    </div>
                  ) : <button className="mx-btn main" disabled={sel.drafts > 0} onClick={() => setConfirm('close')}>{t('ax.fy.close', { year: sel.year })}</button>
                )}
                {sel.drafts > 0 && sel.status !== 'closed' && <div className="mx-flash bad">{t('ax.fy.draftsFirst', { n: sel.drafts })}</div>}
                {sel.status === 'closed' && canReopen && (
                  confirm === 'reopen' ? (
                    <div className="mx-opbox">
                      <p className="mx-note">{t('ax.fy.reopenConfirm', { year: sel.year })}</p>
                      <button className="mx-btn main" disabled={busy} onClick={() => void act('reopen')}>{t('ax.fy.reopenYes')}</button>
                      <button className="mx-btn" onClick={() => setConfirm(null)}>{t('common.cancel')}</button>
                    </div>
                  ) : <button className="mx-btn" onClick={() => setConfirm('reopen')}>{t('ax.fy.reopen', { year: sel.year })}</button>
                )}
              </>
            )}
            {msg && <div className={`mx-flash ${msg.ok ? 'good' : 'bad'}`}>{msg.text}</div>}
            <p className="mx-note">{t('ax.fy.explain')}</p>
          </div>
        </section>
        <section className="mx-box">
          <h3>{t('ax.set.title')}</h3>
          <AccountingSettings canEdit={canClose} />
        </section>
      </div>
    </div>
  );
}

function AccountingSettings({ canEdit }: { canEdit: boolean }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const accounts = useAccounts().filter((a) => a.is_postable);
  const [s, setS] = useState<Record<string, string> | null>(null);
  const [msg, setMsg] = useState<string | null>(null);
  useEffect(() => {
    if (company) api<Record<string, string>>(`/companies/${company.id}/accounting/settings`).then(setS).catch(() => setS(null));
  }, [company]);
  if (!company || !s) return null;
  const keys = ['retained_earnings_account_id', 'cheque_in_hand_account_id', 'cheque_collection_account_id', 'cheque_payable_account_id'];
  async function save() {
    try {
      await api(`/companies/${company!.id}/accounting/settings`, { method: 'PUT', body: s });
      notifySaved(t);
      setMsg(null);
    } catch (e) {
      setMsg(errorText(t, e));
    }
  }
  return (
    <div className="mx-form">
      {keys.map((k) => (
        <div className="mx-fld" key={k}><label htmlFor={`as-${k}`}>{t(`ax.set.${k}`)}</label>
          <select id={`as-${k}`} disabled={!canEdit} value={s[k] ?? ''} onChange={(e) => setS({ ...s, [k]: e.currentTarget.value })}>
            {accounts.map((a) => <option key={a.id} value={a.id}>{accountLabel(a)}</option>)}
          </select></div>
      ))}
      {canEdit && <button className="mx-btn" style={{ alignSelf: 'flex-start' }} onClick={() => void save()}>{t('common.save')}</button>}
      {msg && <div className="mx-flash bad">{msg}</div>}
    </div>
  );
}
