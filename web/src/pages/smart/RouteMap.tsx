import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import L from 'leaflet';
import 'leaflet/dist/leaflet.css';

// The collector's route on an OpenStreetMap map (numbered stops, the path between them, the start point). Loaded only
// when the route screen opens. If the map tiles cannot load (no internet, blocked), the list and the Google Maps
// links still work and a note says so.

export interface MapStop { seq: number; name: string; lat: number; lng: number; band: string }

const bandColor: Record<string, string> = { high: '#b42318', medium: '#a15c00', low: '#0f7b4b', '': '#1d4ed8' };

export default function RouteMap({ stops, start, onPick }: { stops: MapStop[]; start: { lat: number; lng: number } | null; onPick?: (lat: number, lng: number) => void }) {
  const { t } = useTranslation();
  const el = useRef<HTMLDivElement>(null);
  const map = useRef<L.Map | null>(null);
  const layer = useRef<L.LayerGroup | null>(null);
  const [tilesFailed, setTilesFailed] = useState(false);
  const pick = useRef(onPick);
  pick.current = onPick;

  useEffect(() => {
    if (!el.current || map.current) return;
    const m = L.map(el.current, { zoomControl: true, attributionControl: true }).setView([33.89, 35.5], 12);
    let errors = 0;
    L.tileLayer('https://tile.openstreetmap.org/{z}/{x}/{y}.png', {
      maxZoom: 19, attribution: '&copy; OpenStreetMap', referrerPolicy: 'strict-origin-when-cross-origin',
    }).on('tileerror', () => { errors += 1; if (errors >= 4) setTilesFailed(true); }).on('tileload', () => { errors = 0; setTilesFailed(false); }).addTo(m);
    m.on('click', (e: L.LeafletMouseEvent) => pick.current?.(e.latlng.lat, e.latlng.lng));
    layer.current = L.layerGroup().addTo(m);
    map.current = m;
    return () => { m.remove(); map.current = null; };
  }, []);

  useEffect(() => {
    const m = map.current;
    const g = layer.current;
    if (!m || !g) return;
    g.clearLayers();
    const pts: L.LatLngExpression[] = [];
    if (start) {
      L.circleMarker([start.lat, start.lng], { radius: 7, color: '#111827', fillColor: '#fff', fillOpacity: 1, weight: 3 }).bindTooltip(t('sm.rt.you')).addTo(g);
      pts.push([start.lat, start.lng]);
    }
    for (const s of stops) {
      const icon = L.divIcon({
        className: '', iconSize: [26, 26], iconAnchor: [13, 13],
        html: `<div style="width:26px;height:26px;border-radius:50%;background:${bandColor[s.band] ?? bandColor['']};color:#fff;font:700 12px/26px system-ui;text-align:center;border:2px solid #fff;box-shadow:0 1px 3px rgba(0,0,0,.4)">${s.seq}</div>`,
      });
      L.marker([s.lat, s.lng], { icon, keyboard: false }).bindTooltip(`${s.seq}. ${s.name.replace(/[<>&]/g, '')}`).addTo(g);
      pts.push([s.lat, s.lng]);
    }
    if (pts.length > 1) L.polyline(pts, { color: '#1d4ed8', weight: 3, opacity: 0.7 }).addTo(g);
    if (pts.length > 0) m.fitBounds(L.latLngBounds(pts).pad(0.15), { maxZoom: 16 });
    setTimeout(() => m.invalidateSize(), 50);
  }, [stops, start, t]);

  return (
    <div style={{ position: 'relative' }}>
      <div ref={el} style={{ height: 360, borderRadius: 6, border: '1px solid var(--mx-line, #dde3ea)' }} dir="ltr" />
      {tilesFailed && <div className="mx-flash bad" style={{ position: 'absolute', insetInline: 8, top: 8, zIndex: 500 }}>{t('sm.rt.noTiles')}</div>}
    </div>
  );
}
