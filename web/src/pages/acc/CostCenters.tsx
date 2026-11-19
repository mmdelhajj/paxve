import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, can, type Me } from '../../api';
import { useCompany } from '../../company';
import { errorText, formatAmount, today } from '../../util';
import DataGrid, { type Col } from '../../components/DataGrid';
import { Help, ccLabel, useLoad, type CostCenter } from './common';
import { openLedger } from './Statements';

interface Station { id: string; name: string }
interface Gen { id: string; name: string; station_id: string }
interface Center { cost_center_id: string | null; code: string | null; name: string | null; name_i18n: Record<string, string> | null; kind: string | null;
  income: string; expense: string; net: string }
interface Detail { cost_center_id: string | null; account_id: string; code: string; name: string; name_i18n: Record<string, string>; type: string; amount: string }

/** مراكز الكلفة: branches, stations, generators, projects — and the profit of each. */
export default function CostCenters({ me, initialMode }: { me: Me; initialMode?: string }) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<'report' | 'manage'>(initialMode === 'manage' ? 'manage' : 'report');
  return (
    <div className="mx-col">
      <div className="mx-modes" role="tablist">
        {(['report', 'manage'] as const).map((m) => (
          <button key={m} className="mx-mode" role="tab" aria-selected={mode === m} onClick={() => setMode(m)}>{t(`ax.cc.mode.${m}`)}</button>
        ))}
      </div>
      {mode === 'report' ? <CCReport /> : <CCManage me={me} />}
    </div>
  );
}

function CCReport() {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [from, setFrom] = useState(() => `${new Date().getFullYear()}-01-01`);
  const [to, setTo] = useState(today());
  const [sel, setSel] = useState<string | null>(null);
  const cid = company?.id;
  const [data, , loading] = useLoad<{ centers: Center[]; detail: Detail[] }>(cid ? `/companies/${cid}/reports/cost-centers?from=${from}&to=${to}` : null);
  const detail = useMemo(() => (data?.detail ?? []).filter((d) => (d.cost_center_id ?? '-') === (sel ?? '__none')), [data, sel]);
  if (!company) return null;
  const dec = decimalsOf(company.base_currency);
  const cols: Col<Center>[] = [
    { key: 'c', title: t('ax.cc.center'), get: (r) => (r.cost_center_id ? `${r.code} · ${r.name_i18n?.[i18n.language] || r.name}` : t('ax.cc.unassigned')), width: 240 },
    { key: 'k', title: t('ax.cc.kind'), get: (r) => (r.kind ? t(`ax.cc.kinds.${r.kind}`) : ''), width: 100 },
    { key: 'i', title: t('ax.income'), get: (r) => r.income, num: true, decimals: dec, total: 'sum', filter: false, width: 140 },
    { key: 'e', title: t('ax.expenses'), get: (r) => r.expense, num: true, decimals: dec, total: 'sum', filter: false, width: 140 },
    { key: 'n', title: t('ax.result'), get: (r) => r.net, num: true, decimals: dec, total: 'sum', filter: false, width: 150, cls: (r) => (Number(r.net) < 0 ? 'red' : 'ok') },
  ];
  const dcols: Col<Detail>[] = [
    { key: 'ty', title: t('accounts.type'), get: (r) => t(`accounts.types.${r.type}`), width: 90 },
    { key: 'co', title: t('accounts.code'), get: (r) => r.code, width: 80 },
    { key: 'na', title: t('accounts.name'), get: (r) => r.name_i18n?.[i18n.language] || r.name, width: 220 },
    { key: 'am', title: t('ax.amount'), get: (r) => r.amount, num: true, decimals: dec, filter: false, width: 130 },
  ];
  return (
    <>
      <div className="mx-strip">
        <label>{t('reports.from')} <input type="date" value={from} onChange={(e) => setFrom(e.currentTarget.value)} /></label>
        <label>{t('reports.to')} <input type="date" value={to} onChange={(e) => setTo(e.currentTarget.value)} /></label>
        <span className="mx-sp" />
        <Help topic="costCenters" />
      </div>
      <div className="mx-body" style={{ gridTemplateColumns: 'minmax(0, 1.3fr) minmax(0, 1fr)' }}>
        <section className="mx-box">
          <h3>{t('ax.cc.reportTitle')} <small>{t('ax.cc.pickRow')}</small></h3>
          <DataGrid rows={data?.centers ?? []} cols={cols} rowKey={(r) => r.cost_center_id ?? '__none'} loading={loading} footer
            title={t('ax.cc.reportTitle')} selected={sel} onSelect={(r) => setSel(r.cost_center_id ?? '__none')} />
        </section>
        <section className="mx-box">
          <h3>{t('ax.cc.detail')}</h3>
          <DataGrid rows={detail} cols={dcols} rowKey={(r) => r.account_id} title={t('ax.cc.detail')} onActivate={(r) => openLedger(r.account_id, from, to)} />
        </section>
      </div>
    </>
  );
}

function CCManage({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const { company } = useCompany();
  const cid = company?.id;
  const [list, reload, loading] = useLoad<CostCenter[]>(cid ? `/companies/${cid}/cost-centers` : null);
  const [stations, setStations] = useState<Station[]>([]);
  const [gens, setGens] = useState<Gen[]>([]);
  const empty = { id: '', code: '', name: '', name_ar: '', kind: 'station', station_id: '', generator_id: '', is_active: true };
  const [f, setF] = useState(empty);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const canEdit = can(me, 'accounting.manage');
  useEffect(() => {
    if (!cid) return;
    api<Station[]>(`/companies/${cid}/stations`).then(setStations).catch(() => {});
    api<Gen[]>(`/companies/${cid}/generators`).then(setGens).catch(() => {});
  }, [cid]);
  if (!company) return null;

  async function save() {
    if (!cid) return;
    setMsg(null);
    const body = { code: f.code, name: f.name, name_i18n: f.name_ar ? { ar: f.name_ar } : {}, kind: f.kind, station_id: f.station_id,
      generator_id: f.generator_id, is_active: f.is_active };
    try {
      if (f.id) await api(`/companies/${cid}/cost-centers/${f.id}`, { method: 'PATCH', body });
      else await api(`/companies/${cid}/cost-centers`, { method: 'POST', body });
      setMsg({ ok: true, text: t('common.saved') });
      setF(empty);
      reload();
    } catch (e) {
      setMsg({ ok: false, text: errorText(t, e) });
    }
  }
  const cols: Col<CostCenter>[] = [
    { key: 'c', title: t('accounts.code'), get: (r) => r.code, width: 90 },
    { key: 'n', title: t('accounts.name'), get: (r) => ccLabel(r, i18n.language).split(' · ').slice(1).join(' · '), width: 220 },
    { key: 'k', title: t('ax.cc.kind'), get: (r) => t(`ax.cc.kinds.${r.kind}`), width: 100 },
    { key: 's', title: t('net.station'), get: (r) => r.station_name ?? '', width: 140 },
    { key: 'g', title: t('ax.cc.generator'), get: (r) => r.generator_name ?? '', width: 140 },
    { key: 'a', title: t('ax.status'), get: (r) => (r.is_active ? '' : t('ax.cc.inactive')), width: 90 },
  ];
  return (
    <>
      <div className="mx-strip"><span>{t('ax.cc.manageHint')}</span><span className="mx-sp" /><Help topic="costCenters" /></div>
      <div className="mx-body" style={{ gridTemplateColumns: 'minmax(0, 1.4fr) minmax(300px, 1fr)' }}>
        <section className="mx-box">
          <h3>{t('ax.t.costCenters')}</h3>
          <DataGrid rows={list ?? []} cols={cols} rowKey={(r) => r.id} loading={loading} title={t('ax.t.costCenters')}
            selected={f.id || null} onSelect={(r) => setF({ id: r.id, code: r.code, name: r.name, name_ar: r.name_i18n?.ar ?? '', kind: r.kind,
              station_id: r.station_id ?? '', generator_id: r.generator_id ?? '', is_active: r.is_active })} />
        </section>
        <section className="mx-box">
          <h3>{f.id ? `${f.code} · ${f.name}` : t('ax.cc.new')}{f.id && <><span className="mx-sp" /><button className="mx-btn sm" onClick={() => setF(empty)}>+ {t('ax.cc.new')}</button></>}</h3>
          {canEdit ? (
            <div className="mx-form">
              <div className="mx-fld"><label htmlFor="cc-code">{t('accounts.code')}</label><input id="cc-code" value={f.code} onChange={(e) => setF({ ...f, code: e.currentTarget.value })} /></div>
              <div className="mx-fld"><label htmlFor="cc-name">{t('ax.cc.nameEn')}</label><input id="cc-name" value={f.name} onChange={(e) => setF({ ...f, name: e.currentTarget.value })} /></div>
              <div className="mx-fld"><label htmlFor="cc-ar">{t('ax.cc.nameAr')}</label><input id="cc-ar" dir="rtl" value={f.name_ar} onChange={(e) => setF({ ...f, name_ar: e.currentTarget.value })} /></div>
              <div className="mx-fld"><label htmlFor="cc-kind">{t('ax.cc.kind')}</label>
                <select id="cc-kind" value={f.kind} onChange={(e) => setF({ ...f, kind: e.currentTarget.value })}>
                  {['branch', 'station', 'generator', 'project', 'department', 'other'].map((k) => <option key={k} value={k}>{t(`ax.cc.kinds.${k}`)}</option>)}</select></div>
              <div className="mx-fld"><label htmlFor="cc-st">{t('net.station')}</label>
                <select id="cc-st" value={f.station_id} onChange={(e) => setF({ ...f, station_id: e.currentTarget.value })}>
                  <option value="">—</option>{stations.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}</select></div>
              <div className="mx-fld"><label htmlFor="cc-gen">{t('ax.cc.generator')}</label>
                <select id="cc-gen" value={f.generator_id} onChange={(e) => setF({ ...f, generator_id: e.currentTarget.value })}>
                  <option value="">—</option>{gens.map((g) => <option key={g.id} value={g.id}>{g.name}</option>)}</select></div>
              <label className="mx-check"><input type="checkbox" checked={f.is_active} onChange={(e) => setF({ ...f, is_active: e.currentTarget.checked })} /> {t('ax.cc.active')}</label>
              <button className="mx-btn main" disabled={!f.code.trim() || !f.name.trim()} onClick={() => void save()}>{t('common.save')}</button>
              {msg && <div className={`mx-flash ${msg.ok ? 'good' : 'bad'}`}>{msg.text}</div>}
              <p className="mx-note">{t('ax.cc.explain')}</p>
            </div>
          ) : <div className="mx-empty-box">{t('errors.forbidden')}</div>}
        </section>
      </div>
    </>
  );
}
