import { lazy, Suspense, useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api } from '../../api';
import { useCompany } from '../../company';
import { errorText, formatAmount, formatDate } from '../../util';

const RouteMap = lazy(() => import('./RouteMap'));

// Collector route: today's visits (customers with bills to collect) in a short driving order, riskiest / biggest first
// when the list is cut, on a map with Google Maps directions. Locations are captured on the spot from the phone's GPS
// ("I am at this customer"), or typed / clicked on the map in the office.

interface Stop {
  seq: number; customer_id: string; code: string; name: string; phone: string; address: string; area_name: string; lat: number | null; lng: number | null;
  due_base: number; bills: number; oldest_due: string; risk_score: number; risk_band: string; leg_km: number;
}
interface RouteData { base_currency: string; stops: Stop[]; total_km: number; missing_location: Stop[]; maps_links: string[]; start: boolean }

const bandPill: Record<string, string> = { high: 'off', medium: 'warn', low: 'on' };

function gps(): Promise<{ lat: number; lng: number }> {
  return new Promise((res, rej) => {
    if (!navigator.geolocation) { rej(new Error('no_gps')); return; }
    navigator.geolocation.getCurrentPosition((p) => res({ lat: p.coords.latitude, lng: p.coords.longitude }), (e) => rej(e), { enableHighAccuracy: true, timeout: 15000 });
  });
}

export default function RouteView({ collector = false }: { collector?: boolean }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const base = company ? `/companies/${company.id}` : null;
  const [areas, setAreas] = useState<{ id: string; name: string }[]>([]);
  const [area, setArea] = useState('');
  const [limit, setLimit] = useState(25);
  const [start, setStart] = useState<{ lat: number; lng: number } | null>(null);
  const [d, setD] = useState<RouteData | null>(null);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [pickFor, setPickFor] = useState<Stop | null>(null);
  const [typed, setTyped] = useState<Record<string, string>>({});

  const load = useCallback(() => {
    if (!base) return;
    const q = new URLSearchParams({ limit: String(limit) });
    if (area) q.set('area_id', area);
    if (start) { q.set('lat', start.lat.toFixed(6)); q.set('lng', start.lng.toFixed(6)); }
    api<RouteData>(`${base}/collect/route?${q}`).then(setD).catch((e) => setMsg({ ok: false, text: errorText(t, e) }));
  }, [base, area, limit, start, t]);
  useEffect(load, [load]);
  useEffect(() => {
    if (base && !collector) api<{ id: string; name: string }[]>(`${base}/areas`).then(setAreas).catch(() => setAreas([]));
  }, [base, collector]);

  async function setLocation(s: Stop, lat: number, lng: number, source: 'gps' | 'manual') {
    try {
      await api(`${base}/customers/${s.customer_id}/location`, { method: 'PUT', body: { lat, lng, source } });
      setMsg({ ok: true, text: t('sm.rt.saved', { name: s.name }) });
      setPickFor(null);
      load();
    } catch (e) { setMsg({ ok: false, text: errorText(t, e) }); }
  }
  async function here(s: Stop) {
    try { const p = await gps(); await setLocation(s, p.lat, p.lng, 'gps'); } catch { setMsg({ ok: false, text: t('sm.rt.noGps') }); }
  }
  async function fromMe() {
    try { setStart(await gps()); } catch { setMsg({ ok: false, text: t('sm.rt.noGps') }); }
  }
  const mapStops = useMemo(() => (d?.stops ?? []).filter((s) => s.lat != null && s.lng != null)
    .map((s) => ({ seq: s.seq, name: s.name, lat: s.lat as number, lng: s.lng as number, band: s.risk_band })), [d]);
  const cur = d?.base_currency ?? '';

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap', alignItems: 'center' }}>
        {!collector && <select aria-label={t('sm.rt.area')} value={area} onChange={(e) => setArea(e.currentTarget.value)}>
          <option value="">{t('sm.rt.allAreas')}</option>{areas.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}</select>}
        <select aria-label={t('sm.rt.stops')} value={limit} onChange={(e) => setLimit(Number(e.currentTarget.value))}>
          {[10, 15, 25, 40, 60, 80].map((n) => <option key={n} value={n}>{t('sm.rt.nStops', { n })}</option>)}</select>
        <button className="mx-btn sm" onClick={() => void fromMe()}>{t('sm.rt.fromMe')}</button>
        {start && <button className="mx-btn sm" onClick={() => setStart(null)}>{t('sm.rt.clearStart')}</button>}
        <span className="mx-sp" />
        {d && <b>{t('sm.rt.summary', { n: d.stops.length, km: d.total_km })}</b>}
      </div>
      {msg && <div className={`mx-flash ${msg.ok ? 'good' : 'bad'}`}>{msg.text}</div>}
      {pickFor && <div className="mx-flash good">{t('sm.rt.clickMap', { name: pickFor.name })} <button className="mx-btn sm" onClick={() => setPickFor(null)}>{t('sm.rt.cancel')}</button></div>}
      <Suspense fallback={<div style={{ height: 360 }} />}>
        <RouteMap stops={mapStops} start={start} onPick={pickFor ? (lat, lng) => void setLocation(pickFor, lat, lng, 'manual') : undefined} />
      </Suspense>
      {d && d.maps_links.length > 0 && (
        <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
          {d.maps_links.map((l, i) => <a key={l} className="mx-btn sm" href={l} target="_blank" rel="noreferrer">{t('sm.rt.openMaps', { n: i + 1 })}</a>)}
        </div>)}
      {collector && d && (
        // phone: one card per visit, the GPS button under it
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          <b>{t('sm.rt.order')}</b>
          {[...d.stops, ...d.missing_location].map((s) => (
            <div key={s.customer_id} className="mx-box" style={{ padding: 8, display: 'flex', flexDirection: 'column', gap: 4 }}>
              <div style={{ display: 'flex', gap: 6, alignItems: 'baseline', flexWrap: 'wrap' }}>
                {s.seq ? <b style={{ minWidth: 20 }}>{s.seq}.</b> : <small style={{ color: 'var(--mx-amber)' }}>{t('sm.rt.noLoc')}</small>}
                <b>{s.name}</b><small dir="ltr">#{s.code}</small>
                {s.risk_band && <span className={`mx-pill ${bandPill[s.risk_band] ?? ''}`}>{t(`sm.rk.band.${s.risk_band}`)}</span>}
                <span className="mx-sp" style={{ flex: 1 }} />
                <b dir="ltr">{formatAmount(String(s.due_base), 2)} {cur}</b>
              </div>
              <small>{s.address}{s.leg_km ? ` · ${s.leg_km.toFixed(1)} km` : ''} · {t('sm.rt.bills', { n: s.bills, since: formatDate(s.oldest_due) })}</small>
              <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                {s.phone && <a className="mx-btn sm" dir="ltr" href={`tel:${s.phone}`}>{s.phone}</a>}
                <button className="mx-btn sm" onClick={() => void here(s)}>{t('sm.rt.here')}</button>
              </div>
            </div>))}
          {d.stops.length === 0 && d.missing_location.length === 0 && <div className="mx-empty">{t('sm.rt.noStops')}</div>}
        </div>)}
      {!collector && <section className="mx-box">
        <h3>{t('sm.rt.order')}</h3>
        <div style={{ overflowX: 'auto' }}>
          <table className="mx-table">
            <thead><tr><th>#</th><th>{t('sm.rt.customer')}</th><th>{t('sm.rt.area')}</th><th className="num">{t('sm.rt.due')}</th><th>{t('sm.rt.risk')}</th><th className="num">{t('sm.rt.km')}</th><th /></tr></thead>
            <tbody>
              {(d?.stops ?? []).map((s) => (
                <tr key={s.customer_id}>
                  <td><b>{s.seq}</b></td>
                  <td><b>{s.name}</b> <small dir="ltr">#{s.code}</small>{s.phone && <> · <a className="mx-link" dir="ltr" href={`tel:${s.phone}`}>{s.phone}</a></>}<br /><small>{s.address}</small></td>
                  <td>{s.area_name}</td>
                  <td className="num">{formatAmount(String(s.due_base), 2)} {cur}<br /><small>{t('sm.rt.bills', { n: s.bills, since: formatDate(s.oldest_due) })}</small></td>
                  <td>{s.risk_band && <span className={`mx-pill ${bandPill[s.risk_band] ?? ''}`}>{t(`sm.rk.band.${s.risk_band}`)}</span>}</td>
                  <td className="num">{s.leg_km ? s.leg_km.toFixed(1) : ''}</td>
                  <td><button className="mx-btn sm" onClick={() => void here(s)}>{t('sm.rt.here')}</button></td>
                </tr>))}
              {d && d.stops.length === 0 && <tr><td colSpan={7} className="mx-empty">{t('sm.rt.noStops')}</td></tr>}
            </tbody>
          </table>
        </div>
      </section>}
      {!collector && d && d.missing_location.length > 0 && (
        <section className="mx-box">
          <h3>{t('sm.rt.missing', { n: d.missing_location.length })}</h3>
          <p style={{ margin: 0, padding: '0 8px' }}><small>{t('sm.rt.missingHelp')}</small></p>
          <div style={{ overflowX: 'auto' }}>
            <table className="mx-table">
              <tbody>
                {d.missing_location.map((s) => (
                  <tr key={s.customer_id}>
                    <td><b>{s.name}</b> <small dir="ltr">#{s.code}</small><br /><small>{s.address}</small></td>
                    <td>{s.area_name}</td>
                    <td className="num">{formatAmount(String(s.due_base), 2)} {cur}</td>
                    <td style={{ whiteSpace: 'nowrap' }}>
                      <button className="mx-btn sm" onClick={() => void here(s)}>{t('sm.rt.here')}</button>{' '}
                      {!collector && <>
                        <button className="mx-btn sm" onClick={() => setPickFor(s)}>{t('sm.rt.onMap')}</button>{' '}
                        <input dir="ltr" placeholder="33.89, 35.50" style={{ width: 130 }} value={typed[s.customer_id] ?? ''}
                          onChange={(e) => setTyped({ ...typed, [s.customer_id]: e.currentTarget.value })} />{' '}
                        <button className="mx-btn sm" onClick={() => {
                          const m = (typed[s.customer_id] ?? '').match(/(-?\d+(?:\.\d+)?)\s*[, ]\s*(-?\d+(?:\.\d+)?)/);
                          if (m) void setLocation(s, Number(m[1]), Number(m[2]), 'manual'); else setMsg({ ok: false, text: t('sm.rt.badCoords') });
                        }}>{t('sm.rt.save')}</button></>}
                    </td>
                  </tr>))}
              </tbody>
            </table>
          </div>
        </section>)}
    </div>
  );
}

/** The collector app's "Route" tab. */
export function CollectorRoute() {
  return <div style={{ fontSize: 14 }}><RouteView collector /></div>;
}
