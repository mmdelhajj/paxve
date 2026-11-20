import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, type Me } from '../../api';
import { useCompany } from '../../company';
import { errorText, formatDate } from '../../util';
import type { Stop } from './Wh';

// The driver's phone: today's trips, each stop with call / map links, and the proof of delivery (the receiver's
// name and signature, a photo, the place). Also shown inside the collector app ("Deliveries" tab).

interface Trip { id: string; number: number; trip_date: string; vehicle_name: string | null; plate: string | null; status: string; stops: number; delivered: number }

/** Signature pad (finger or mouse) returning a PNG data URL. */
function SignaturePad({ onChange }: { onChange: (dataUrl: string) => void }) {
  const { t } = useTranslation();
  const ref = useRef<HTMLCanvasElement>(null);
  const drawing = useRef(false);
  const dirty = useRef(false);
  useEffect(() => {
    const c = ref.current;
    if (!c) return;
    const ratio = window.devicePixelRatio || 1;
    c.width = c.clientWidth * ratio;
    c.height = c.clientHeight * ratio;
    const g = c.getContext('2d');
    if (g) { g.scale(ratio, ratio); g.lineWidth = 2.2; g.lineCap = 'round'; g.strokeStyle = '#111'; }
  }, []);
  const pos = (e: React.PointerEvent<HTMLCanvasElement>) => {
    const r = e.currentTarget.getBoundingClientRect();
    return [e.clientX - r.left, e.clientY - r.top] as const;
  };
  return (
    <div>
      <canvas ref={ref} aria-label={t('wh.signature')} style={{ width: '100%', height: 160, background: '#fff', border: '1px solid #bbb', borderRadius: 8, touchAction: 'none' }}
        onPointerDown={(e) => { drawing.current = true; e.currentTarget.setPointerCapture(e.pointerId); const g = e.currentTarget.getContext('2d'); const [x, y] = pos(e); g?.beginPath(); g?.moveTo(x, y); }}
        onPointerMove={(e) => { if (!drawing.current) return; const g = e.currentTarget.getContext('2d'); const [x, y] = pos(e); g?.lineTo(x, y); g?.stroke(); dirty.current = true; }}
        onPointerUp={(e) => { drawing.current = false; if (dirty.current) onChange(e.currentTarget.toDataURL('image/png')); }} />
      <button type="button" className="mx-btn sm" onClick={() => { const c = ref.current; c?.getContext('2d')?.clearRect(0, 0, c.width, c.height); dirty.current = false; onChange(''); }}>{t('wh.drv.clear')}</button>
    </div>
  );
}

/** Shrink a photo before upload (long side 1600 px, JPEG). */
async function shrink(file: File): Promise<string> {
  const url = URL.createObjectURL(file);
  try {
    const img = await new Promise<HTMLImageElement>((ok, bad) => { const i = new Image(); i.onload = () => ok(i); i.onerror = bad; i.src = url; });
    const k = Math.min(1, 1600 / Math.max(img.width, img.height));
    const c = document.createElement('canvas');
    c.width = Math.round(img.width * k); c.height = Math.round(img.height * k);
    c.getContext('2d')?.drawImage(img, 0, 0, c.width, c.height);
    return c.toDataURL('image/jpeg', 0.8);
  } finally { URL.revokeObjectURL(url); }
}

export function DriverTrips() {
  const { t } = useTranslation();
  const { company } = useCompany();
  const base = company ? `/companies/${company.id}` : null;
  const [trips, setTrips] = useState<Trip[]>([]);
  const [sel, setSel] = useState<string | null>(null);
  const [d, setD] = useState<{ trip: Trip; stops: Stop[] } | null>(null);
  const [stop, setStop] = useState<Stop | null>(null);
  const [f, setF] = useState({ name: '', note: '', signature: '', status: 'delivered' as 'delivered' | 'failed' });
  const [photo, setPhoto] = useState<File | null>(null);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const load = useCallback(() => { if (base) api<Trip[]>(`${base}/wh/trips?mine=1`).then(setTrips).catch(() => setTrips([])); }, [base]);
  const loadTrip = useCallback((id: string) => { if (base) api<{ trip: Trip; stops: Stop[] }>(`${base}/wh/trips/${id}`).then(setD).catch(() => setD(null)); }, [base]);
  useEffect(load, [load]);
  useEffect(() => { if (sel) loadTrip(sel); }, [sel, loadTrip]);

  async function save() {
    if (!base || !stop) return;
    setBusy(true); setMsg(null);
    try {
      let lat: number | undefined; let lng: number | undefined;
      try {
        const p = await new Promise<GeolocationPosition>((ok, bad) => navigator.geolocation ? navigator.geolocation.getCurrentPosition(ok, bad, { timeout: 4000, maximumAge: 60000 }) : bad(new Error('no geo')));
        lat = Math.round(p.coords.latitude * 1e6) / 1e6; lng = Math.round(p.coords.longitude * 1e6) / 1e6;
      } catch { /* location is optional */ }
      await api(`${base}/wh/stops/${stop.id}/pod`, { method: 'POST', body: { status: f.status, name: f.name, note: f.note, signature: f.signature, lat, lng } });
      if (photo) await api(`${base}/wh/stops/${stop.id}/photo`, { method: 'POST', body: { filename: photo.name || 'photo.jpg', data: await shrink(photo) } });
      setMsg({ ok: true, text: t('wh.drv.saved') });
      setStop(null); setPhoto(null); setF({ name: '', note: '', signature: '', status: 'delivered' });
      if (sel) loadTrip(sel);
      load();
    } catch (e) { setMsg({ ok: false, text: errorText(t, e) }); } finally { setBusy(false); }
  }

  const box: React.CSSProperties = { background: '#fff', borderRadius: 10, padding: 10, border: '1px solid #dde3ea' };
  if (stop) return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      <div style={box}><b>{stop.customer_name ?? ''}</b><div>{stop.address}</div></div>
      <div className="mx-row" style={{ gap: 6 }}>
        <button className={`mx-btn ${f.status === 'delivered' ? 'main' : ''}`} style={{ flex: 1, minHeight: 48 }} onClick={() => setF({ ...f, status: 'delivered' })}>{t('wh.ss.delivered')}</button>
        <button className={`mx-btn ${f.status === 'failed' ? 'danger' : ''}`} style={{ flex: 1, minHeight: 48 }} onClick={() => setF({ ...f, status: 'failed' })}>{t('wh.ss.failed')}</button>
      </div>
      {f.status === 'delivered' && <>
        <label>{t('wh.drv.receiver')}<input style={{ minHeight: 44, width: '100%' }} value={f.name} onChange={(e) => setF({ ...f, name: e.currentTarget.value })} /></label>
        <SignaturePad onChange={(s) => setF((x) => ({ ...x, signature: s }))} />
      </>}
      <label>{t('wh.drv.note')}<textarea rows={2} style={{ width: '100%' }} value={f.note} onChange={(e) => setF({ ...f, note: e.currentTarget.value })} /></label>
      <label>{t('wh.drv.photo')}<input type="file" accept="image/*" capture="environment" onChange={(e) => setPhoto(e.currentTarget.files?.[0] ?? null)} /></label>
      {msg && <div className={`mx-flash ${msg.ok ? 'good' : 'bad'}`}>{msg.text}</div>}
      <div className="mx-row" style={{ gap: 6 }}>
        <button className="mx-btn main" style={{ flex: 1, minHeight: 52 }} disabled={busy || (f.status === 'delivered' && !f.name.trim() && !f.signature)} onClick={() => void save()}>{t('wh.drv.confirm')}</button>
        <button className="mx-btn" style={{ minHeight: 52 }} onClick={() => setStop(null)}>{t('wh.close')}</button>
      </div>
    </div>
  );
  if (sel && d) return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      <div className="mx-row" style={{ gap: 6, alignItems: 'center' }}>
        <b style={{ flex: 1 }}>{t('wh.trips.trip', { n: d.trip.number })} · {d.trip.vehicle_name ?? ''}</b>
        <button className="mx-btn sm" onClick={() => { setSel(null); setD(null); }}>{t('wh.close')}</button>
      </div>
      {msg && <div className={`mx-flash ${msg.ok ? 'good' : 'bad'}`}>{msg.text}</div>}
      {d.stops.map((s) => (
        <div key={s.id} style={{ ...box, background: s.status === 'delivered' ? '#e8f6ec' : s.status === 'failed' ? '#fdecea' : '#fff' }}>
          <div className="mx-row" style={{ gap: 6, alignItems: 'baseline' }}><b>{s.seq}. {s.customer_name ?? ''}</b><span className="mx-sp" /><span className="mx-pill">{t(`wh.ss.${s.status}`)}</span></div>
          <div>{s.address}</div>
          {s.package_list && <small dir="ltr">{s.package_list.map((p) => p.code).join(', ')}</small>}
          {s.note && <div><small>{s.note}</small></div>}
          <div className="mx-row" style={{ gap: 6, marginTop: 6, flexWrap: 'wrap' }}>
            {s.phone && <a className="mx-btn" style={{ minHeight: 44, display: 'inline-flex', alignItems: 'center' }} href={`tel:${s.phone}`} dir="ltr">{t('wh.drv.call')} {s.phone}</a>}
            {s.address && <a className="mx-btn" style={{ minHeight: 44, display: 'inline-flex', alignItems: 'center' }} target="_blank" rel="noreferrer"
              href={`https://www.google.com/maps/search/?api=1&query=${encodeURIComponent(s.address)}`}>{t('wh.drv.map')}</a>}
            {s.status === 'pending' && <button className="mx-btn main" style={{ minHeight: 44 }} onClick={() => setStop(s)}>{t('wh.drv.deliver')}</button>}
          </div>
        </div>))}
    </div>
  );
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      {msg && <div className={`mx-flash ${msg.ok ? 'good' : 'bad'}`}>{msg.text}</div>}
      {trips.length === 0 && <div style={box}>{t('wh.drv.noTrips')}</div>}
      {trips.map((x) => (
        <button key={x.id} className="mx-btn" style={{ ...box, textAlign: 'start', minHeight: 64 }} onClick={() => setSel(x.id)}>
          <b>{t('wh.trips.trip', { n: x.number })}</b> · {formatDate(x.trip_date)} · {x.vehicle_name ?? ''} <span dir="ltr">{x.plate ?? ''}</span>
          <br /><small>{x.delivered}/{x.stops} · {t(`wh.ts.${x.status}`)}</small>
        </button>))}
    </div>
  );
}

export default function Driver({ me, onExit }: { me: Me; onExit: () => void }) {
  const { t, i18n } = useTranslation();
  void me;
  return (
    <div dir={i18n.dir()} style={{ minHeight: '100vh', background: '#f3f5f8', display: 'flex', flexDirection: 'column' }}>
      <header style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '8px 10px', background: '#14324f', color: '#fff', paddingTop: 'calc(8px + env(safe-area-inset-top, 0px))' }}>
        <b style={{ flex: 1 }}>{t('wh.drv.title')}</b>
        <button className="mx-btn sm" onClick={onExit}>{t('wh.close')}</button>
      </header>
      <main style={{ padding: 8 }}><DriverTrips /></main>
    </div>
  );
}
