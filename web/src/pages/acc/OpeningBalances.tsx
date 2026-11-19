import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, can, type Me } from '../../api';
import { useCompany } from '../../company';
import { accountName, errorText, formatAmount } from '../../util';
import { openScreen } from '../../nav';
import { Help, useAccounts } from './common';

/** الأرصدة الافتتاحية: what the business owns and owes on the day it starts using Paxve, in one entry. */
export default function OpeningBalances({ me }: { me: Me }) {
  const { t } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const accounts = useAccounts().filter((a) => a.is_postable && a.is_active && ['asset', 'liability', 'equity'].includes(a.type))
    .sort((a, b) => a.code.localeCompare(b.code));
  const [date, setDate] = useState(() => `${new Date().getFullYear()}-01-01`);
  const [vals, setVals] = useState<Record<string, { dr: string; cr: string; cur: string }>>({});
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [q, setQ] = useState('');
  const canPost = can(me, 'accounting.manage');
  const base = company?.base_currency ?? 'USD';
  const totals = useMemo(() => {
    let dr = 0, cr = 0;
    for (const v of Object.values(vals)) if (v.cur === base || !v.cur) { dr += Number(v.dr) || 0; cr += Number(v.cr) || 0; }
    return { dr, cr };
  }, [vals, base]);
  if (!company) return null;
  const dec = decimalsOf(base);
  const currencies = [...new Set([company.base_currency, company.local_currency])];
  const set = (id: string, k: 'dr' | 'cr' | 'cur', v: string) => setVals((x) => {
    const prev = x[id] ?? { dr: '', cr: '', cur: base };
    return { ...x, [id]: { ...prev, [k]: v } };
  });
  const shown = accounts.filter((a) => !q || `${a.code} ${accountName(a)}`.toLowerCase().includes(q.toLowerCase()));

  async function post() {
    if (busy) return;
    setBusy(true);
    setMsg(null);
    const lines = Object.entries(vals).filter(([, v]) => Number(v.dr) > 0 || Number(v.cr) > 0)
      .map(([account_id, v]) => ({ account_id, currency: accounts.find((a) => a.id === account_id)?.currency || v.cur || base,
        debit: v.dr || '0', credit: v.cr || '0' }));
    try {
      const r = await api<{ journal_entry_id: string; difference: string }>(`/companies/${company!.id}/opening-balances`, { method: 'POST', body: { date, lines } });
      setMsg({ ok: true, text: t('ax.ob.done', { diff: formatAmount(r.difference, dec) }) });
      setVals({});
    } catch (e) {
      setMsg({ ok: false, text: errorText(t, e) });
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="mx-col">
      <div className="mx-strip">
        <label>{t('ax.ob.date')} <input type="date" value={date} onChange={(e) => setDate(e.currentTarget.value)} /></label>
        <label>{t('ax.ob.search')} <input value={q} onChange={(e) => setQ(e.currentTarget.value)} /></label>
        <button className="mx-btn sm" onClick={() => openScreen('import')}>{t('ax.ob.importCustomers')}</button>
        <span className="mx-sp" />
        <Help topic="opening" />
      </div>
      <div className="mx-sum">
        <div><span>{t('reports.debit')}</span><b dir="ltr">{formatAmount(totals.dr, dec)}</b></div>
        <div><span>{t('reports.credit')}</span><b dir="ltr">{formatAmount(totals.cr, dec)}</b></div>
        <div><span>{t('ax.ob.diff')}</span><b dir="ltr" className={Math.abs(totals.dr - totals.cr) > 0.004 ? 'red' : 'ok'}>{formatAmount(totals.dr - totals.cr, dec)}</b></div>
        {canPost && <button className="mx-btn main" style={{ padding: '4px 18px' }} disabled={busy || (totals.dr === 0 && totals.cr === 0 && !Object.values(vals).some((v) => Number(v.dr) || Number(v.cr)))}
          onClick={() => void post()}>{t('ax.ob.post')}</button>}
        {msg && <span className={`mx-flash ${msg.ok ? 'good' : 'bad'}`}>{msg.text}</span>}
      </div>
      <div className="mx-body" style={{ gridTemplateColumns: '1fr' }}>
        <section className="mx-box">
          <h3>{t('ax.t.opening')} <small>{t('ax.ob.hint')}</small></h3>
          <div className="mx-grid">
            <table>
              <colgroup><col style={{ width: 90 }} /><col style={{ width: 300 }} /><col style={{ width: 110 }} /><col style={{ width: 80 }} /><col style={{ width: 150 }} /><col style={{ width: 150 }} /></colgroup>
              <thead><tr><th>{t('accounts.code')}</th><th>{t('accounts.name')}</th><th>{t('accounts.type')}</th><th>{t('mx.d.currency')}</th>
                <th className="num">{t('reports.debit')}</th><th className="num">{t('reports.credit')}</th></tr></thead>
              <tbody>
                {shown.map((a) => {
                  const v = vals[a.id];
                  return (
                    <tr key={a.id}>
                      <td>{a.code}</td><td>{accountName(a)}</td><td>{t(`accounts.types.${a.type}`)}</td>
                      <td>{a.currency ? a.currency : (
                        <select aria-label={`${a.code} ${t('mx.d.currency')}`} value={v?.cur ?? base} onChange={(e) => set(a.id, 'cur', e.currentTarget.value)}>
                          {currencies.map((c) => <option key={c}>{c}</option>)}</select>)}</td>
                      <td><input className="mx-cell" inputMode="decimal" aria-label={`${a.code} ${t('reports.debit')}`} disabled={!canPost}
                        value={v?.dr ?? ''} onChange={(e) => set(a.id, 'dr', e.currentTarget.value.replace(/[^\d.]/g, ''))} /></td>
                      <td><input className="mx-cell" inputMode="decimal" aria-label={`${a.code} ${t('reports.credit')}`} disabled={!canPost}
                        value={v?.cr ?? ''} onChange={(e) => set(a.id, 'cr', e.currentTarget.value.replace(/[^\d.]/g, ''))} /></td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </section>
      </div>
    </div>
  );
}
