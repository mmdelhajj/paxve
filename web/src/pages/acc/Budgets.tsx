import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, can, type Me } from '../../api';
import { useCompany } from '../../company';
import { accountName, errorText, formatAmount } from '../../util';
import DataGrid, { type Col } from '../../components/DataGrid';
import { Help, ccLabel, monthName, useAccounts, useCostCenters, useLoad, years } from './common';
import { openLedger } from './Statements';

interface BRow { account_id: string; code: string; name: string; name_i18n: Record<string, string>; type: string; budget: string[]; actual: string[] }

/** الموازنة: planned income and expenses per month, and how the real figures compare. */
export default function Budgets({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const [mode, setMode] = useState<'report' | 'edit'>('report');
  const [year, setYear] = useState(new Date().getFullYear());
  const [cc, setCc] = useState('');
  const centers = useCostCenters();
  return (
    <div className="mx-col">
      <div className="mx-modes" role="tablist">
        {(['report', 'edit'] as const).map((m) => (
          <button key={m} className="mx-mode" role="tab" aria-selected={mode === m} onClick={() => setMode(m)}>{t(`ax.bu.mode.${m}`)}</button>
        ))}
      </div>
      <div className="mx-strip">
        <label>{t('ax.fy.year')} <select value={year} onChange={(e) => setYear(Number(e.currentTarget.value))}>{years().map((y) => <option key={y}>{y}</option>)}</select></label>
        <label>{t('ax.cc.center')} <select value={cc} onChange={(e) => setCc(e.currentTarget.value)}>
          <option value="">{t('ax.bu.wholeCompany')}</option>{centers.map((c) => <option key={c.id} value={c.id}>{ccLabel(c, i18n.language)}</option>)}</select></label>
        <span className="mx-sp" />
        <Help topic="budget" />
      </div>
      {mode === 'report' ? <BudgetReport year={year} cc={cc} /> : <BudgetEdit me={me} year={year} cc={cc} />}
    </div>
  );
}

function BudgetReport({ year, cc }: { year: number; cc: string }) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const now = new Date();
  const [month, setMonth] = useState(year === now.getFullYear() ? now.getMonth() + 1 : 12);
  const [ytd, setYtd] = useState(true);
  const cid = company?.id;
  const [data, , loading] = useLoad<{ rows: BRow[] }>(cid ? `/companies/${cid}/reports/budget?year=${year}&cost_center_id=${cc}` : null);
  type R = BRow & { b: number; a: number; v: number; p: number | null };
  const rows: R[] = useMemo(() => (data?.rows ?? []).map((r) => {
    const pick = (arr: string[]) => (ytd ? arr.slice(0, month) : [arr[month - 1]]).reduce((s, x) => s + Number(x), 0);
    const b = pick(r.budget), a = pick(r.actual);
    // variance: good when income is above plan or expenses below plan
    const v = r.type === 'income' ? a - b : b - a;
    return { ...r, b, a, v, p: b ? (a / b) * 100 : null };
  }), [data, month, ytd]);
  if (!company) return null;
  const dec = decimalsOf(company.base_currency);
  const cols: Col<R>[] = [
    { key: 'ty', title: t('accounts.type'), get: (r) => t(`accounts.types.${r.type}`), width: 90 },
    { key: 'co', title: t('accounts.code'), get: (r) => r.code, width: 80 },
    { key: 'na', title: t('accounts.name'), get: (r) => accountName(r), width: 240 },
    { key: 'b', title: t('ax.bu.budget'), get: (r) => r.b, num: true, decimals: dec, filter: false, width: 130 },
    { key: 'a', title: t('ax.bu.actual'), get: (r) => r.a, num: true, decimals: dec, filter: false, width: 130 },
    { key: 'v', title: t('ax.bu.variance'), get: (r) => r.v, num: true, decimals: dec, filter: false, width: 130, cls: (r) => (r.v < 0 ? 'red' : 'ok') },
    { key: 'p', title: '%', get: (r) => (r.p === null ? '' : r.p.toFixed(0)), num: true, decimals: 0, filter: false, width: 70 },
  ];
  return (
    <>
      <div className="mx-strip">
        <label>{t('ax.bu.month')} <select value={month} onChange={(e) => setMonth(Number(e.currentTarget.value))}>
          {Array.from({ length: 12 }, (_, i) => i + 1).map((m) => <option key={m} value={m}>{monthName(m, i18n.language)}</option>)}</select></label>
        <label className="mx-check"><input type="checkbox" checked={ytd} onChange={(e) => setYtd(e.currentTarget.checked)} /> {t('ax.bu.ytd')}</label>
      </div>
      <div className="mx-body" style={{ gridTemplateColumns: '1fr' }}>
        <section className="mx-box">
          <h3>{t('ax.bu.reportTitle', { year })} <small>{t('ax.bu.varHint')}</small></h3>
          <DataGrid rows={rows} cols={cols} rowKey={(r) => r.account_id} loading={loading} title={t('ax.bu.reportTitle', { year })}
            onActivate={(r) => openLedger(r.account_id, `${year}-01-01`, `${year}-12-31`)} />
        </section>
      </div>
    </>
  );
}

function BudgetEdit({ me, year, cc }: { me: Me; year: number; cc: string }) {
  const { t, i18n } = useTranslation();
  const { company } = useCompany();
  const accounts = useAccounts().filter((a) => a.is_postable && a.is_active && (a.type === 'income' || a.type === 'expense'))
    .sort((a, b) => (a.type === b.type ? a.code.localeCompare(b.code) : a.type === 'income' ? -1 : 1));
  const [vals, setVals] = useState<Record<string, string[]>>({});
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const canEdit = can(me, 'accounting.manage');
  useEffect(() => {
    if (!company) return;
    api<{ account_id: string; month: number; amount: string }[]>(`/companies/${company.id}/budgets?year=${year}&cost_center_id=${cc}`).then((ls) => {
      const v: Record<string, string[]> = {};
      for (const l of ls) {
        v[l.account_id] ??= Array(12).fill('');
        v[l.account_id][l.month - 1] = String(Number(l.amount));
      }
      setVals(v);
    }).catch(() => setVals({}));
  }, [company, year, cc]);
  if (!company) return null;
  const setCell = (acc: string, m: number, v: string) => setVals((x) => {
    const row = [...(x[acc] ?? Array(12).fill(''))];
    if (m < 0) row.fill(v); else row[m] = v;
    return { ...x, [acc]: row };
  });
  async function save() {
    if (!company || busy) return;
    setBusy(true);
    setMsg(null);
    const lines = Object.entries(vals).flatMap(([account_id, ms]) => ms.map((amount, i) => ({ account_id, month: i + 1, amount: amount || '0' })))
      .filter((l) => Number(l.amount) > 0);
    try {
      await api(`/companies/${company.id}/budgets`, { method: 'PUT', body: { year, cost_center_id: cc, lines } });
      setMsg({ ok: true, text: t('common.saved') });
    } catch (e) {
      setMsg({ ok: false, text: errorText(t, e) });
    } finally {
      setBusy(false);
    }
  }
  const total = (acc: string) => (vals[acc] ?? []).reduce((s, x) => s + (Number(x) || 0), 0);
  return (
    <div className="mx-body" style={{ gridTemplateColumns: '1fr' }}>
      <section className="mx-box">
        <h3>{t('ax.bu.editTitle', { year })} <small>{t('ax.bu.editHint')}</small><span className="mx-sp" />
          {canEdit && <button className="mx-btn sm" disabled={busy} onClick={() => void save()}>{t('common.save')}</button>}
          {msg && <span className={`mx-flash ${msg.ok ? 'good' : 'bad'}`}>{msg.text}</span>}</h3>
        <div className="mx-grid">
          <table>
            <colgroup><col style={{ width: 260 }} /><col style={{ width: 100 }} />{Array.from({ length: 12 }, (_, i) => <col key={i} style={{ width: 90 }} />)}<col style={{ width: 110 }} /></colgroup>
            <thead><tr><th>{t('accounts.name')}</th><th>{t('ax.bu.each')}</th>{Array.from({ length: 12 }, (_, i) => <th key={i}>{monthName(i + 1, i18n.language)}</th>)}<th className="num">{t('ax.bu.total')}</th></tr></thead>
            <tbody>
              {accounts.map((a) => (
                <tr key={a.id}>
                  <td title={a.code}>{a.code} · {accountName(a)}</td>
                  <td><input className="mx-cell" inputMode="decimal" disabled={!canEdit} aria-label={`${a.code} ${t('ax.bu.each')}`}
                    onChange={(e) => setCell(a.id, -1, e.currentTarget.value.replace(/[^\d.]/g, ''))} /></td>
                  {Array.from({ length: 12 }, (_, m) => (
                    <td key={m}><input className="mx-cell" inputMode="decimal" disabled={!canEdit} aria-label={`${a.code} ${m + 1}`}
                      value={vals[a.id]?.[m] ?? ''} onChange={(e) => setCell(a.id, m, e.currentTarget.value.replace(/[^\d.]/g, ''))} /></td>
                  ))}
                  <td className="num">{total(a.id) ? formatAmount(total(a.id), 0) : ''}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  );
}
