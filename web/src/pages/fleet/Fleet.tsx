import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, can, type Me } from '../../api';
import { useCompany } from '../../company';
import { formatAmount, formatDate, today } from '../../util';
import Chatter from '../../components/Chatter';
import Attachments from '../../components/Attachments';
import { useBase, useLoad, useRun } from '../wh/Wh';
import { usePeople } from '../prj/Prj';

// Fleet (Magic-style screens): vehicles and their papers, fuel (consumption and anomalies), service plans and log,
// alerts, costs per vehicle (posted as expenses) and the expense accounts used.

type Msg = { ok: boolean; text: string } | null;
function Flash({ m }: { m: Msg }) {
  return m ? <div className={`mx-flash ${m.ok ? 'good' : 'bad'}`}>{m.text}</div> : null;
}

interface Vehicle {
  id: string; name: string; plate: string; kind: string; make: string; model: string; year: number | null; vin: string; fuel_type: string; odometer: string;
  fuel_norm: string; purchase_date: string | null; license_expiry: string | null; insurance_expiry: string | null; inspection_expiry: string | null; capacity_kg: string;
  driver_id: string | null; driver_name: string | null; is_active: boolean; notes: string; avg_consumption: string | null; last_fuel: string | null;
  min_doc_days: number | null; services_due: number; anomalies: number;
}
interface Fuel { id: string; vehicle_id: string; vehicle_name: string; plate: string; log_date: string; odometer: string; liters: string; amount: string; currency: string; full_tank: boolean; station: string; driver_name: string | null; consumption: string | null; anomaly: string; expense_number: number | null; status: string }
interface Plan { id: string; vehicle_id: string; vehicle_name: string; task: string; every_km: string | null; every_days: number | null; last_km: string | null; last_date: string | null; due_km: string | null; due_date: string | null; state: string; is_active: boolean; odometer?: string }
interface Svc { id: string; vehicle_id: string; vehicle_name: string; done_date: string; odometer: string | null; task: string; amount: string; currency: string; vendor: string; notes: string; expense_number: number | null; status: string }
interface CostRow { vehicle_id: string; vehicle_name: string; plate: string; liters: string; fuel_cost: string; service_cost: string; other_cost: string; total_cost: string; km: string; cost_per_km: string; consumption: string; base_currency: string }
interface Method { id: string; name: string; currency: string; is_active: boolean }
interface Account { id: string; code: string; name: string; type: string; is_postable: boolean }

const MODES = ['vehicles', 'fuel', 'services', 'alerts', 'costs', 'settings'] as const;
type Mode = typeof MODES[number];
const KINDS = ['car', 'van', 'truck', 'motorbike', 'bus', 'machine', 'other'];
const FUELS = ['diesel', 'gasoline', 'electric', 'hybrid', 'lpg', 'other'];
const amt = (v: string | number | null | undefined) => formatAmount(String(v ?? 0), 2);
const q = (v: string | null | undefined) => (v == null || v === '' ? '' : String(Number(v)));
const num = (s: string) => s.replace(/[^\d.]/g, '');
const stColor: Record<string, string> = { due: '#b42318', soon: '#a15c00', ok: '#0f7b4b', off: '#6b7280' };

export default function Fleet({ me, initial }: { me: Me; initial?: string }) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<Mode>((MODES as readonly string[]).includes(initial ?? '') ? initial as Mode : 'vehicles');
  return (
    <div className="mx-col">
      <div className="mx-strip"><b>{t(`fl.m.${mode}`)}</b><span className="mx-sp" /><small style={{ opacity: 0.8 }}>{t(`fl.help.${mode}`)}</small></div>
      <div className="mx-modes" role="tablist">
        {MODES.map((m) => <button key={m} role="tab" className="mx-mode" aria-selected={mode === m} onClick={() => setMode(m)}>{t(`fl.m.${m}`)}</button>)}
      </div>
      <div style={{ flex: 1, minHeight: 0, overflow: 'auto', padding: 6 }}>
        {mode === 'vehicles' && <Vehicles me={me} />}
        {mode === 'fuel' && <FuelLog me={me} />}
        {mode === 'services' && <Services me={me} />}
        {mode === 'alerts' && <Alerts />}
        {mode === 'costs' && <Costs />}
        {mode === 'settings' && <Settings me={me} />}
      </div>
    </div>
  );
}

function useVehicles(): [Vehicle[], () => void] {
  const base = useBase();
  return useLoad<Vehicle[]>(base && `${base}/fleet/vehicles`, []);
}
function useMethods(): Method[] {
  const base = useBase();
  const [m] = useLoad<Method[]>(base && `${base}/payment-methods`, []);
  return m.filter((x) => x.is_active);
}

function DocDays({ d }: { d: string | null }) {
  if (!d) return <span />;
  const days = Math.round((new Date(`${d}T00:00:00`).getTime() - new Date(`${today()}T00:00:00`).getTime()) / 86400000);
  return <span className={days < 0 ? 'red' : days <= 30 ? 'warn' : ''} style={days <= 30 && days >= 0 ? { color: 'var(--mx-amber)' } : undefined}>{formatDate(d)}</span>;
}

// ======================= vehicles =======================

function Vehicles({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [rows, reload] = useVehicles();
  const people = usePeople();
  const [sel, setSel] = useState<string | null>(null);
  const [nf, setNf] = useState({ name: '', plate: '', kind: 'van', make: '', model: '', fuel_type: 'diesel', odometer: '', driver_id: '' });
  const [msg, run] = useRun(reload);
  const manage = can(me, 'fleet.manage');
  return (
    <div className="mx-row" style={{ gap: 8, alignItems: 'flex-start', flexWrap: 'wrap' }}>
      <section className="mx-box" style={{ flex: '999 1 560px', minWidth: 0 }}>
        <h3>{t('fl.m.vehicles')}</h3>
        {manage && (
          <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
            <label className="mx-fld">{t('fl.name')}<input value={nf.name} onChange={(e) => setNf({ ...nf, name: e.currentTarget.value })} /></label>
            <label className="mx-fld">{t('fl.plate')}<input dir="ltr" style={{ width: 100 }} value={nf.plate} onChange={(e) => setNf({ ...nf, plate: e.currentTarget.value })} /></label>
            <label className="mx-fld">{t('fl.kind')}<select value={nf.kind} onChange={(e) => setNf({ ...nf, kind: e.currentTarget.value })}>{KINDS.map((k) => <option key={k} value={k}>{t(`fl.k.${k}`)}</option>)}</select></label>
            <label className="mx-fld">{t('fl.make')}<input style={{ width: 90 }} value={nf.make} onChange={(e) => setNf({ ...nf, make: e.currentTarget.value })} /></label>
            <label className="mx-fld">{t('fl.model')}<input style={{ width: 90 }} value={nf.model} onChange={(e) => setNf({ ...nf, model: e.currentTarget.value })} /></label>
            <label className="mx-fld">{t('fl.fuelType')}<select value={nf.fuel_type} onChange={(e) => setNf({ ...nf, fuel_type: e.currentTarget.value })}>{FUELS.map((k) => <option key={k} value={k}>{t(`fl.ft.${k}`)}</option>)}</select></label>
            <label className="mx-fld">{t('fl.odometer')}<input dir="ltr" style={{ width: 90 }} value={nf.odometer} onChange={(e) => setNf({ ...nf, odometer: num(e.currentTarget.value) })} /></label>
            <label className="mx-fld">{t('fl.driver')}<select value={nf.driver_id} onChange={(e) => setNf({ ...nf, driver_id: e.currentTarget.value })}><option value="">—</option>{people.map((u) => <option key={u.id} value={u.id}>{u.full_name}</option>)}</select></label>
            <button className="mx-btn main" disabled={!nf.name.trim()} onClick={() => void run(api<Vehicle>(`${base}/fleet/vehicles`, { method: 'POST', body: { ...nf, driver_id: nf.driver_id || undefined, odometer: nf.odometer || undefined } }),
              (x) => { setSel((x as Vehicle).id); setNf({ ...nf, name: '', plate: '' }); })}>{t('fl.add')}</button>
          </div>)}
        <Flash m={msg} />
        <div style={{ overflowX: 'auto' }}>
          <table className="mx-table">
            <thead><tr><th>{t('fl.name')}</th><th>{t('fl.plate')}</th><th>{t('fl.kind')}</th><th>{t('fl.driver')}</th><th className="num">{t('fl.odometer')}</th><th className="num">{t('fl.l100')}</th>
              <th>{t('fl.license')}</th><th>{t('fl.insurance')}</th><th>{t('fl.inspection')}</th><th>{t('fl.alerts')}</th></tr></thead>
            <tbody>
              {rows.map((v) => (
                <tr key={v.id} className={sel === v.id ? 'sel' : ''} style={{ cursor: 'pointer', opacity: v.is_active ? 1 : 0.55 }} onClick={() => setSel(v.id)}>
                  <td><b>{v.name}</b> <small>{v.make} {v.model}</small></td><td dir="ltr">{v.plate}</td><td>{t(`fl.k.${v.kind}`)}</td><td>{v.driver_name ?? ''}</td>
                  <td className="num">{q(v.odometer)}</td><td className="num">{v.avg_consumption ?? ''}</td>
                  <td><DocDays d={v.license_expiry} /></td><td><DocDays d={v.insurance_expiry} /></td><td><DocDays d={v.inspection_expiry} /></td>
                  <td>{v.services_due > 0 && <span className="mx-pill warn">{t('fl.svcDueN', { n: v.services_due })}</span>}{v.anomalies > 0 && <span className="mx-pill warn">{t('fl.anomN', { n: v.anomalies })}</span>}</td>
                </tr>))}
              {rows.length === 0 && <tr><td colSpan={10} className="mx-empty">{t('fl.none')}</td></tr>}
            </tbody>
          </table>
        </div>
      </section>
      {sel && <VehiclePanel me={me} id={sel} onChange={reload} onClose={() => setSel(null)} />}
    </div>
  );
}

function VehiclePanel({ me, id, onChange, onClose }: { me: Me; id: string; onChange: () => void; onClose: () => void }) {
  const { t } = useTranslation();
  const base = useBase();
  const people = usePeople();
  const [d, reload] = useLoad<{ vehicle: Vehicle; fuel: Fuel[]; plans: Plan[]; log: Svc[]; costs: CostRow | null; trips: { id: string; number: number; trip_date: string; status: string; stops: number }[] } | null>(base && `${base}/fleet/vehicles/${id}`, null);
  const [msg, run] = useRun(() => { reload(); onChange(); });
  if (!d) return null;
  const v = d.vehicle;
  const manage = can(me, 'fleet.manage');
  const patch = (body: Record<string, unknown>) => void run(api(`${base}/fleet/vehicles/${id}`, { method: 'PATCH', body }));
  const dateField = (k: 'license_expiry' | 'insurance_expiry' | 'inspection_expiry' | 'purchase_date') => (
    <label className="mx-fld">{t(`fl.${k === 'purchase_date' ? 'purchased' : k.replace('_expiry', '')}`)}<input type="date" dir="ltr" disabled={!manage} defaultValue={v[k] ?? ''} key={`${k}-${v[k]}`}
      onBlur={(e) => { if (e.currentTarget.value !== (v[k] ?? '')) patch({ [k]: e.currentTarget.value }); }} /></label>);
  return (
    <section className="mx-box" style={{ flex: '1 1 460px', minWidth: 0 }}>
      <h3 style={{ display: 'flex', gap: 6, alignItems: 'center', flexWrap: 'wrap' }}>{v.name} <span dir="ltr">{v.plate}</span><span className="mx-sp" />
        <button className="mx-btn sm" onClick={onClose}>{t('fl.close')}</button></h3>
      <div style={{ padding: 6, display: 'flex', flexDirection: 'column', gap: 6 }}>
        <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap', alignItems: 'end' }}>
          <label className="mx-fld">{t('fl.driver')}<select disabled={!manage} value={v.driver_id ?? ''} onChange={(e) => patch({ driver_id: e.currentTarget.value })}>
            <option value="">—</option>{people.map((u) => <option key={u.id} value={u.id}>{u.full_name}</option>)}</select></label>
          <label className="mx-fld">{t('fl.year')}<input dir="ltr" style={{ width: 60 }} disabled={!manage} defaultValue={v.year ?? ''} key={`y-${v.year}`} onBlur={(e) => { const y = Number(e.currentTarget.value); if (y && y !== v.year) patch({ year: y }); }} /></label>
          <label className="mx-fld">{t('fl.vin')}<input dir="ltr" disabled={!manage} defaultValue={v.vin} key={`v-${v.vin}`} onBlur={(e) => { if (e.currentTarget.value !== v.vin) patch({ vin: e.currentTarget.value }); }} /></label>
          <label className="mx-fld">{t('fl.norm')}<input dir="ltr" style={{ width: 60 }} disabled={!manage} defaultValue={q(v.fuel_norm)} key={`n-${v.fuel_norm}`} onBlur={(e) => patch({ fuel_norm: num(e.currentTarget.value) || '0' })} /></label>
          {dateField('license_expiry')}{dateField('insurance_expiry')}{dateField('inspection_expiry')}{dateField('purchase_date')}
          <label className="mx-fld" style={{ flexDirection: 'row', alignItems: 'center', gap: 4 }}><input type="checkbox" disabled={!manage} checked={v.is_active} onChange={(e) => patch({ is_active: e.currentTarget.checked })} />{t('fl.active')}</label>
        </div>
        {d.costs && <small>{t('fl.yearCosts')}: {t('fl.fuelC')} {amt(d.costs.fuel_cost)} · {t('fl.serviceC')} {amt(d.costs.service_cost)} · {t('fl.otherC')} {amt(d.costs.other_cost)} · <b>{amt(d.costs.total_cost)} {d.costs.base_currency}</b>{d.costs.cost_per_km ? ` · ${d.costs.cost_per_km}/km` : ''}</small>}
        <Flash m={msg} />
        <h4 style={{ margin: '6px 0 2px' }}>{t('fl.m.fuel')}</h4>
        <FuelTable rows={d.fuel.slice(0, 10)} />
        <h4 style={{ margin: '6px 0 2px' }}>{t('fl.plans')}</h4>
        <table className="mx-table"><tbody>
          {d.plans.map((p) => <tr key={p.id}><td>{p.task}</td><td dir="ltr">{p.every_km ? `${q(p.every_km)} km` : ''}{p.every_km && p.every_days ? ' / ' : ''}{p.every_days ? `${p.every_days} d` : ''}</td>
            <td>{p.due_km ? `${q(p.due_km)} km` : ''} {p.due_date ? formatDate(p.due_date) : ''}</td><td style={{ color: stColor[p.state] }}>{t(`fl.ps.${p.state}`)}</td></tr>)}
          {d.plans.length === 0 && <tr><td className="mx-empty">{t('fl.none')}</td></tr>}
        </tbody></table>
        <h4 style={{ margin: '6px 0 2px' }}>{t('fl.m.services')}</h4>
        <table className="mx-table"><tbody>
          {d.log.slice(0, 10).map((s) => <tr key={s.id} style={{ opacity: s.status === 'void' ? 0.5 : 1 }}><td>{formatDate(s.done_date)}</td><td>{s.task}</td><td className="num">{q(s.odometer)}</td><td className="num">{amt(s.amount)} {s.currency}</td><td>{s.vendor}</td></tr>)}
          {d.log.length === 0 && <tr><td className="mx-empty">{t('fl.none')}</td></tr>}
        </tbody></table>
        {d.trips.length > 0 && <small>{t('fl.trips')}: {d.trips.map((x) => `#${x.number} ${formatDate(x.trip_date)}`).join(' · ')}</small>}
        <Attachments type="vehicle" id={v.id} readOnly={!manage} />
        <Chatter type="vehicle" id={v.id} readOnly={!can(me, 'fleet.create')} />
      </div>
    </section>
  );
}

function FuelTable({ rows, onVoid }: { rows: Fuel[]; onVoid?: (id: string) => void }) {
  const { t } = useTranslation();
  return (
    <div style={{ overflowX: 'auto' }}>
      <table className="mx-table">
        <thead><tr><th>{t('fl.date')}</th><th>{t('fl.vehicle')}</th><th className="num">{t('fl.odometer')}</th><th className="num">{t('fl.liters')}</th><th className="num">{t('fl.amount')}</th>
          <th className="num">{t('fl.l100')}</th><th>{t('fl.flag')}</th><th>{t('fl.expense')}</th><th /></tr></thead>
        <tbody>
          {rows.map((f) => (
            <tr key={f.id} style={{ opacity: f.status === 'void' ? 0.5 : 1 }}>
              <td>{formatDate(f.log_date)}</td><td>{f.vehicle_name}</td><td className="num">{q(f.odometer)}</td><td className="num">{q(f.liters)}{f.full_tank ? '' : ' ½'}</td>
              <td className="num">{Number(f.amount) > 0 ? `${amt(f.amount)} ${f.currency ?? ''}` : ''}</td><td className="num">{f.consumption ?? ''}</td>
              <td className={f.anomaly ? 'red' : ''}>{f.anomaly ? t(`fl.an.${f.anomaly}`) : ''}{f.status === 'void' ? ` (${t('fl.void')})` : ''}</td>
              <td dir="ltr">{f.expense_number ? `EXP-${f.expense_number}` : ''}</td>
              <td>{onVoid && f.status !== 'void' && <button className="mx-btn sm" onClick={() => onVoid(f.id)}>{t('fl.void')}</button>}</td>
            </tr>))}
          {rows.length === 0 && <tr><td colSpan={9} className="mx-empty">{t('fl.none')}</td></tr>}
        </tbody>
      </table>
    </div>
  );
}

// ======================= fuel =======================

function FuelLog({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [vehicles] = useVehicles();
  const methods = useMethods();
  const [veh, setVeh] = useState('');
  const [onlyAnom, setOnlyAnom] = useState(false);
  const [rows, reload] = useLoad<Fuel[]>(base && `${base}/fleet/fuel?vehicle_id=${veh}${onlyAnom ? '&anomalies=1' : ''}`, []);
  const [nf, setNf] = useState({ vehicle_id: '', date: today(), odometer: '', liters: '', amount: '', method_id: '', full_tank: true, station: '' });
  const [msg, run] = useRun(reload);
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      {can(me, 'fleet.create') && (
        <section className="mx-box"><h3>{t('fl.addFuel')}</h3>
          <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
            <label className="mx-fld">{t('fl.vehicle')}<select value={nf.vehicle_id} onChange={(e) => setNf({ ...nf, vehicle_id: e.currentTarget.value })}><option value="">—</option>
              {vehicles.filter((v) => v.is_active).map((v) => <option key={v.id} value={v.id}>{v.name} {v.plate}</option>)}</select></label>
            <label className="mx-fld">{t('fl.date')}<input type="date" dir="ltr" value={nf.date} onChange={(e) => setNf({ ...nf, date: e.currentTarget.value })} /></label>
            <label className="mx-fld">{t('fl.odometer')}<input dir="ltr" style={{ width: 90 }} value={nf.odometer} onChange={(e) => setNf({ ...nf, odometer: num(e.currentTarget.value) })} /></label>
            <label className="mx-fld">{t('fl.liters')}<input dir="ltr" style={{ width: 70 }} value={nf.liters} onChange={(e) => setNf({ ...nf, liters: num(e.currentTarget.value) })} /></label>
            <label className="mx-fld">{t('fl.amount')}<input dir="ltr" style={{ width: 80 }} value={nf.amount} onChange={(e) => setNf({ ...nf, amount: num(e.currentTarget.value) })} /></label>
            <label className="mx-fld">{t('fl.paidFrom')}<select value={nf.method_id} onChange={(e) => setNf({ ...nf, method_id: e.currentTarget.value })}><option value="">{t('fl.notPaid')}</option>
              {methods.map((m) => <option key={m.id} value={m.id}>{m.name}</option>)}</select></label>
            <label className="mx-fld">{t('fl.station')}<input style={{ width: 110 }} value={nf.station} onChange={(e) => setNf({ ...nf, station: e.currentTarget.value })} /></label>
            <label className="mx-fld" style={{ flexDirection: 'row', alignItems: 'center', gap: 4 }}><input type="checkbox" checked={nf.full_tank} onChange={(e) => setNf({ ...nf, full_tank: e.currentTarget.checked })} />{t('fl.fullTank')}</label>
            <button className="mx-btn main" disabled={!nf.vehicle_id || !nf.odometer || !Number(nf.liters) || (!!nf.method_id && !Number(nf.amount))}
              onClick={() => void run(api(`${base}/fleet/fuel`, { method: 'POST', body: { ...nf, method_id: nf.method_id || undefined, amount: nf.amount || undefined } }), () => setNf({ ...nf, odometer: '', liters: '', amount: '' }))}>{t('fl.save')}</button>
          </div>
          <Flash m={msg} />
        </section>)}
      <section className="mx-box"><h3>{t('fl.m.fuel')}</h3>
        <div className="mx-row" style={{ gap: 6, padding: 6 }}>
          <select aria-label={t('fl.vehicle')} value={veh} onChange={(e) => setVeh(e.currentTarget.value)}><option value="">{t('fl.all')}</option>{vehicles.map((v) => <option key={v.id} value={v.id}>{v.name}</option>)}</select>
          <label style={{ display: 'flex', gap: 4, alignItems: 'center' }}><input type="checkbox" checked={onlyAnom} onChange={(e) => setOnlyAnom(e.currentTarget.checked)} />{t('fl.onlyAnom')}</label>
        </div>
        <FuelTable rows={rows} onVoid={can(me, 'fleet.manage') ? (id) => void run(api(`${base}/fleet/fuel/${id}/void`, { method: 'POST', body: {} })) : undefined} />
      </section>
    </div>
  );
}

// ======================= services =======================

function Services({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [vehicles] = useVehicles();
  const methods = useMethods();
  const [plans, reloadP] = useLoad<Plan[]>(base && `${base}/fleet/service-plans`, []);
  const [log, reloadL] = useLoad<Svc[]>(base && `${base}/fleet/service-log`, []);
  const reload = () => { reloadP(); reloadL(); };
  const [msg, run] = useRun(reload);
  const [pf, setPf] = useState({ vehicle_id: '', task: '', every_km: '', every_days: '' });
  const [lf, setLf] = useState({ plan_id: '', vehicle_id: '', task: '', date: today(), odometer: '', amount: '', method_id: '', vendor: '' });
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <section className="mx-box"><h3>{t('fl.plans')}</h3>
        {can(me, 'fleet.manage') && (
          <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
            <label className="mx-fld">{t('fl.vehicle')}<select value={pf.vehicle_id} onChange={(e) => setPf({ ...pf, vehicle_id: e.currentTarget.value })}><option value="">—</option>{vehicles.map((v) => <option key={v.id} value={v.id}>{v.name}</option>)}</select></label>
            <label className="mx-fld">{t('fl.task')}<input value={pf.task} onChange={(e) => setPf({ ...pf, task: e.currentTarget.value })} /></label>
            <label className="mx-fld">{t('fl.everyKm')}<input dir="ltr" style={{ width: 80 }} value={pf.every_km} onChange={(e) => setPf({ ...pf, every_km: num(e.currentTarget.value) })} /></label>
            <label className="mx-fld">{t('fl.everyDays')}<input dir="ltr" style={{ width: 60 }} value={pf.every_days} onChange={(e) => setPf({ ...pf, every_days: num(e.currentTarget.value) })} /></label>
            <button className="mx-btn" disabled={!pf.vehicle_id || !pf.task.trim() || (!pf.every_km && !pf.every_days)}
              onClick={() => void run(api(`${base}/fleet/service-plans`, { method: 'POST', body: { ...pf, every_km: pf.every_km || undefined, every_days: pf.every_days ? Number(pf.every_days) : undefined } }), () => setPf({ ...pf, task: '' }))}>{t('fl.add')}</button>
          </div>)}
        <table className="mx-table">
          <thead><tr><th>{t('fl.vehicle')}</th><th>{t('fl.task')}</th><th>{t('fl.every')}</th><th>{t('fl.lastDone')}</th><th>{t('fl.nextDue')}</th><th>{t('fl.state')}</th></tr></thead>
          <tbody>
            {plans.map((p) => (
              <tr key={p.id} style={{ cursor: 'pointer' }} onClick={() => setLf({ ...lf, plan_id: p.id, vehicle_id: p.vehicle_id, task: p.task })}>
                <td>{p.vehicle_name}</td><td>{p.task}</td><td dir="ltr">{p.every_km ? `${q(p.every_km)} km` : ''}{p.every_km && p.every_days ? ' / ' : ''}{p.every_days ? `${p.every_days} d` : ''}</td>
                <td>{p.last_km ? `${q(p.last_km)} km` : ''} {p.last_date ? formatDate(p.last_date) : ''}</td><td>{p.due_km ? `${q(p.due_km)} km` : ''} {p.due_date ? formatDate(p.due_date) : ''}</td>
                <td style={{ color: stColor[p.state], fontWeight: 600 }}>{t(`fl.ps.${p.state}`)}</td></tr>))}
            {plans.length === 0 && <tr><td colSpan={6} className="mx-empty">{t('fl.none')}</td></tr>}
          </tbody>
        </table>
      </section>
      {can(me, 'fleet.create') && (
        <section className="mx-box"><h3>{t('fl.logService')}</h3>
          <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
            <label className="mx-fld">{t('fl.plan')}<select value={lf.plan_id} onChange={(e) => { const p = plans.find((x) => x.id === e.currentTarget.value); setLf({ ...lf, plan_id: e.currentTarget.value, vehicle_id: p?.vehicle_id ?? lf.vehicle_id, task: p?.task ?? lf.task }); }}>
              <option value="">—</option>{plans.map((p) => <option key={p.id} value={p.id}>{p.vehicle_name} · {p.task}</option>)}</select></label>
            {!lf.plan_id && <label className="mx-fld">{t('fl.vehicle')}<select value={lf.vehicle_id} onChange={(e) => setLf({ ...lf, vehicle_id: e.currentTarget.value })}><option value="">—</option>{vehicles.map((v) => <option key={v.id} value={v.id}>{v.name}</option>)}</select></label>}
            <label className="mx-fld">{t('fl.task')}<input value={lf.task} onChange={(e) => setLf({ ...lf, task: e.currentTarget.value })} /></label>
            <label className="mx-fld">{t('fl.date')}<input type="date" dir="ltr" value={lf.date} onChange={(e) => setLf({ ...lf, date: e.currentTarget.value })} /></label>
            <label className="mx-fld">{t('fl.odometer')}<input dir="ltr" style={{ width: 90 }} value={lf.odometer} onChange={(e) => setLf({ ...lf, odometer: num(e.currentTarget.value) })} /></label>
            <label className="mx-fld">{t('fl.amount')}<input dir="ltr" style={{ width: 80 }} value={lf.amount} onChange={(e) => setLf({ ...lf, amount: num(e.currentTarget.value) })} /></label>
            <label className="mx-fld">{t('fl.paidFrom')}<select value={lf.method_id} onChange={(e) => setLf({ ...lf, method_id: e.currentTarget.value })}><option value="">{t('fl.notPaid')}</option>{methods.map((m) => <option key={m.id} value={m.id}>{m.name}</option>)}</select></label>
            <label className="mx-fld">{t('fl.vendor')}<input style={{ width: 110 }} value={lf.vendor} onChange={(e) => setLf({ ...lf, vendor: e.currentTarget.value })} /></label>
            <button className="mx-btn main" disabled={(!lf.plan_id && !lf.vehicle_id) || !lf.task.trim() || (!!lf.method_id && !Number(lf.amount))}
              onClick={() => void run(api(`${base}/fleet/service-log`, { method: 'POST', body: { ...lf, plan_id: lf.plan_id || undefined, vehicle_id: lf.vehicle_id || undefined, method_id: lf.method_id || undefined, amount: lf.amount || undefined, odometer: lf.odometer || undefined } }),
                () => setLf({ ...lf, plan_id: '', task: '', amount: '', odometer: '' }))}>{t('fl.save')}</button>
          </div>
          <Flash m={msg} />
        </section>)}
      <section className="mx-box"><h3>{t('fl.m.services')}</h3>
        <table className="mx-table">
          <thead><tr><th>{t('fl.date')}</th><th>{t('fl.vehicle')}</th><th>{t('fl.task')}</th><th className="num">{t('fl.odometer')}</th><th className="num">{t('fl.amount')}</th><th>{t('fl.vendor')}</th><th>{t('fl.expense')}</th><th /></tr></thead>
          <tbody>
            {log.map((s) => (
              <tr key={s.id} style={{ opacity: s.status === 'void' ? 0.5 : 1 }}><td>{formatDate(s.done_date)}</td><td>{s.vehicle_name}</td><td>{s.task}</td><td className="num">{q(s.odometer)}</td>
                <td className="num">{Number(s.amount) > 0 ? `${amt(s.amount)} ${s.currency ?? ''}` : ''}</td><td>{s.vendor}</td><td dir="ltr">{s.expense_number ? `EXP-${s.expense_number}` : ''}</td>
                <td>{can(me, 'fleet.manage') && s.status !== 'void' && <button className="mx-btn sm" onClick={() => void run(api(`${base}/fleet/service-log/${s.id}/void`, { method: 'POST', body: {} }))}>{t('fl.void')}</button>}</td></tr>))}
            {log.length === 0 && <tr><td colSpan={8} className="mx-empty">{t('fl.none')}</td></tr>}
          </tbody>
        </table>
      </section>
    </div>
  );
}

// ======================= alerts / costs / settings =======================

function Alerts() {
  const { t } = useTranslation();
  const base = useBase();
  const [a] = useLoad<{ documents: { vehicle_id: string; vehicle_name: string; plate: string; kind: string; expiry: string; days: number }[]; services: Plan[]; fuel: Fuel[] } | null>(base && `${base}/fleet/alerts`, null);
  if (!a) return null;
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <section className="mx-box"><h3>{t('fl.al.docs')} ({a.documents.length})</h3>
        <table className="mx-table"><tbody>
          {a.documents.map((x, i) => <tr key={i}><td>{x.vehicle_name} <span dir="ltr">{x.plate}</span></td><td>{t(`fl.${x.kind}`)}</td><td>{formatDate(x.expiry)}</td>
            <td className={x.days < 0 ? 'red' : ''} style={x.days >= 0 ? { color: 'var(--mx-amber)' } : undefined}>{x.days < 0 ? t('fl.expiredAgo', { n: -x.days }) : t('fl.inDays', { n: x.days })}</td></tr>)}
          {a.documents.length === 0 && <tr><td className="mx-empty">{t('fl.allGood')}</td></tr>}
        </tbody></table>
      </section>
      <section className="mx-box"><h3>{t('fl.al.services')} ({a.services.length})</h3>
        <table className="mx-table"><tbody>
          {a.services.map((p) => <tr key={p.id}><td>{p.vehicle_name}</td><td>{p.task}</td><td>{p.due_km ? `${q(p.due_km)} km (${t('fl.now')} ${q(p.odometer)})` : ''} {p.due_date ? formatDate(p.due_date) : ''}</td><td style={{ color: stColor[p.state], fontWeight: 600 }}>{t(`fl.ps.${p.state}`)}</td></tr>)}
          {a.services.length === 0 && <tr><td className="mx-empty">{t('fl.allGood')}</td></tr>}
        </tbody></table>
      </section>
      <section className="mx-box"><h3>{t('fl.al.fuel')} ({a.fuel.length})</h3><FuelTable rows={a.fuel} /></section>
    </div>
  );
}

function Costs() {
  const { t } = useTranslation();
  const base = useBase();
  const [from, setFrom] = useState(`${new Date().getFullYear()}-01-01`);
  const [to, setTo] = useState(today());
  const [d] = useLoad<{ rows: CostRow[] } | null>(base && `${base}/fleet/reports/costs?from=${from}&to=${to}`, null);
  const rows = d?.rows ?? [];
  const tot = rows.reduce((s, r) => s + Number(r.total_cost), 0);
  return (
    <section className="mx-box"><h3>{t('fl.m.costs')} {rows[0] ? `(${rows[0].base_currency})` : ''}</h3>
      <div className="mx-row" style={{ gap: 6, padding: 6, alignItems: 'end' }}>
        <label className="mx-fld">{t('fl.from')}<input type="date" dir="ltr" value={from} onChange={(e) => setFrom(e.currentTarget.value)} /></label>
        <label className="mx-fld">{t('fl.to')}<input type="date" dir="ltr" value={to} onChange={(e) => setTo(e.currentTarget.value)} /></label>
      </div>
      <div style={{ overflowX: 'auto' }}>
        <table className="mx-table">
          <thead><tr><th>{t('fl.vehicle')}</th><th className="num">{t('fl.km')}</th><th className="num">{t('fl.liters')}</th><th className="num">{t('fl.l100')}</th><th className="num">{t('fl.fuelC')}</th>
            <th className="num">{t('fl.serviceC')}</th><th className="num">{t('fl.otherC')}</th><th className="num">{t('fl.totalC')}</th><th className="num">{t('fl.perKm')}</th></tr></thead>
          <tbody>
            {rows.map((r) => <tr key={r.vehicle_id}><td>{r.vehicle_name} <span dir="ltr">{r.plate}</span></td><td className="num">{q(r.km)}</td><td className="num">{q(r.liters)}</td><td className="num">{r.consumption}</td>
              <td className="num">{amt(r.fuel_cost)}</td><td className="num">{amt(r.service_cost)}</td><td className="num">{amt(r.other_cost)}</td><td className="num"><b>{amt(r.total_cost)}</b></td><td className="num">{r.cost_per_km}</td></tr>)}
            {rows.length === 0 && <tr><td colSpan={9} className="mx-empty">{t('fl.none')}</td></tr>}
          </tbody>
          <tfoot><tr><th colSpan={7}>{t('fl.total')}</th><th className="num">{amt(tot)}</th><th /></tr></tfoot>
        </table>
      </div>
    </section>
  );
}

function Settings({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const { company } = useCompany();
  const [s, reload] = useLoad<{ fuel_account_id: string | null; service_account_id: string | null } | null>(base && `${base}/fleet/settings`, null);
  const [accounts] = useLoad<Account[]>(base && `${base}/accounts`, []);
  const [msg, run] = useRun(reload);
  const exp = accounts.filter((a) => a.type === 'expense' && a.is_postable);
  if (!s) return null;
  const manage = can(me, 'fleet.manage');
  return (
    <section className="mx-box"><h3>{t('fl.m.settings')} · {company?.name_en}</h3>
      <div style={{ padding: 6, display: 'flex', flexDirection: 'column', gap: 6, maxWidth: 520 }}>
        <small>{t('fl.setHelp')}</small>
        <label className="mx-fld">{t('fl.fuelAcc')}<select disabled={!manage} value={s.fuel_account_id ?? ''} onChange={(e) => void run(api(`${base}/fleet/settings`, { method: 'PUT', body: { fuel_account_id: e.currentTarget.value } }))}>
          <option value="">—</option>{exp.map((a) => <option key={a.id} value={a.id}>{a.code} · {a.name}</option>)}</select></label>
        <label className="mx-fld">{t('fl.serviceAcc')}<select disabled={!manage} value={s.service_account_id ?? ''} onChange={(e) => void run(api(`${base}/fleet/settings`, { method: 'PUT', body: { service_account_id: e.currentTarget.value } }))}>
          <option value="">—</option>{exp.map((a) => <option key={a.id} value={a.id}>{a.code} · {a.name}</option>)}</select></label>
        <Flash m={msg} />
      </div>
    </section>
  );
}
