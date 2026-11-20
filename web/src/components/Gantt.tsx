import { useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';

// Phase 9: a light timeline (Gantt) — lanes on the side, bars you drag to another time (and, for hour timelines, to
// another lane), dependency arrows, conflict / overload marks. Day unit: "YYYY-MM-DD" bars with an exclusive end;
// hour unit: RFC 3339 times.

export interface GBar {
  id: string; label: string; row: string; row_label: string; start: string; end: string; status: string; progress: number;
  deps: string[]; ref: string; planned: boolean;
}
export interface GWarning { kind: string; ids?: string[]; row?: string; day?: string; info?: string }
export interface GLoad { workcenter_id: string; day: string; pct: number }

const DAY = 86400000;
const toMs = (s: string, unit: 'day' | 'hour') => (unit === 'day' ? Date.parse(`${s.slice(0, 10)}T00:00:00Z`) : Date.parse(s));
const isoDay = (ms: number) => new Date(ms).toISOString().slice(0, 10);

export default function Gantt({ bars, warnings, unit, from, to, rows, load, onMove, readOnly }: {
  bars: GBar[]; warnings: GWarning[]; unit: 'day' | 'hour'; from: string; to: string;
  rows?: { key: string; label: string }[]; load?: GLoad[];
  onMove?: (bar: GBar, start: string, end: string, row: string) => void; readOnly?: boolean;
}) {
  const { t, i18n } = useTranslation();
  const rtl = i18n.language === 'ar' || i18n.language === 'fa' || i18n.language === 'ur';
  const pxPerDay = unit === 'day' ? 34 : 120;
  const t0 = Date.parse(`${from}T00:00:00Z`);
  const t1 = Date.parse(`${to}T00:00:00Z`) + DAY;
  const days = Math.max(1, Math.round((t1 - t0) / DAY));
  const width = days * pxPerDay;
  const rowH = 30;
  const lanes = useMemo(() => {
    if (rows && rows.length) return rows;
    const seen = new Map<string, string>();
    for (const b of bars) if (!seen.has(b.row)) seen.set(b.row, b.row_label || '—');
    return [...seen.entries()].map(([key, label]) => ({ key, label }));
  }, [rows, bars]);
  // project timelines: one line per task (the lane is the assignee, shown on the bar)
  const perBar = unit === 'day';
  const lines = perBar ? bars.map((b) => ({ key: b.id, label: b.label, bars: [b] })) : lanes.map((l) => ({ key: l.key, label: l.label, bars: bars.filter((b) => b.row === l.key) }));
  const flagged = new Set<string>();
  for (const w of warnings) for (const id of w.ids ?? []) flagged.add(id);
  const overloaded = new Set(warnings.filter((w) => w.kind === 'overload').map((w) => `${w.row}|${w.day}`));
  const pos = (ms: number) => ((ms - t0) / DAY) * pxPerDay;
  const [drag, setDrag] = useState<{ id: string; x0: number; y0: number; dx: number; dy: number } | null>(null);
  const area = useRef<HTMLDivElement>(null);
  const snap = unit === 'day' ? DAY : DAY / 48; // a day, or 30 minutes

  const barBox = new Map<string, { x: number; w: number; y: number }>();
  lines.forEach((ln, i) => ln.bars.forEach((b) => {
    const s = toMs(b.start, unit), e = Math.max(toMs(b.end, unit), s + snap);
    barBox.set(b.id, { x: pos(s), w: Math.max(6, pos(e) - pos(s)), y: i * rowH });
  }));

  function finish(b: GBar) {
    if (!drag || !onMove) { setDrag(null); return; }
    const dir = rtl ? -1 : 1;
    const shift = Math.round(((drag.dx * dir) / pxPerDay) * DAY / snap) * snap;
    const laneShift = perBar ? 0 : Math.round(drag.dy / rowH);
    setDrag(null);
    if (shift === 0 && laneShift === 0) return;
    const s = toMs(b.start, unit) + shift, e = toMs(b.end, unit) + shift;
    let row = b.row;
    if (laneShift !== 0) {
      const idx = lanes.findIndex((l) => l.key === b.row) + laneShift;
      if (idx >= 0 && idx < lanes.length) row = lanes[idx].key;
    }
    if (unit === 'day') onMove(b, isoDay(s), isoDay(e - DAY), row); // end inclusive for the API
    else onMove(b, new Date(s).toISOString(), new Date(e).toISOString(), row);
  }

  const ticks = Array.from({ length: days }, (_, i) => t0 + i * DAY);
  const todayX = pos(Date.now());
  return (
    <div className="mx-gantt" style={{ display: 'flex', border: '1px solid var(--mx-line, #cfd6e0)', background: '#fff', minHeight: 60 }}>
      <div style={{ flex: '0 0 220px', borderInlineEnd: '1px solid #cfd6e0' }}>
        <div style={{ height: 34, borderBottom: '1px solid #cfd6e0', padding: '6px 8px', fontWeight: 600, fontSize: 12 }}>{perBar ? t('op.g.task') : t('op.g.lane')}</div>
        {lines.map((ln) => (
          <div key={ln.key} title={ln.label} style={{ height: rowH, borderBottom: '1px solid #eef1f5', padding: '0 8px', fontSize: 12.5, display: 'flex', alignItems: 'center', overflow: 'hidden', whiteSpace: 'nowrap', textOverflow: 'ellipsis' }}>
            <bdi>{ln.label}</bdi>
          </div>
        ))}
        {lines.length === 0 && <div className="mx-empty" style={{ padding: 8 }}>{t('op.g.empty')}</div>}
      </div>
      <div ref={area} style={{ flex: 1, overflowX: 'auto', position: 'relative' }}>
        <div style={{ width, position: 'relative' }}>
          <div style={{ display: 'flex', height: 34, borderBottom: '1px solid #cfd6e0', position: 'sticky', top: 0, background: '#f6f8fb' }}>
            {ticks.map((ms) => {
              const d = new Date(ms);
              const we = d.getUTCDay() === 0;
              return (
                <div key={ms} style={{ flex: `0 0 ${pxPerDay}px`, borderInlineStart: '1px solid #e3e8ef', fontSize: 10.5, textAlign: 'center', paddingTop: 3, background: we ? '#f0f2f6' : undefined }}>
                  <div dir="ltr">{d.getUTCDate()}/{d.getUTCMonth() + 1}</div>
                  {unit === 'hour' && <div style={{ display: 'flex', justifyContent: 'space-between', padding: '0 2px', opacity: 0.6 }} dir="ltr"><span>0</span><span>12</span><span>24</span></div>}
                </div>);
            })}
          </div>
          <div style={{ position: 'relative', height: Math.max(1, lines.length) * rowH }}>
            {ticks.map((ms, i) => <div key={ms} style={{ position: 'absolute', insetInlineStart: i * pxPerDay, top: 0, bottom: 0, borderInlineStart: '1px solid #f0f2f6' }} />)}
            {load && !perBar && lines.map((ln, i) => load.filter((l) => l.workcenter_id === ln.key).map((l) => {
              const x = pos(Date.parse(`${l.day}T00:00:00Z`));
              const hot = overloaded.has(`${ln.key}|${l.day}`);
              return <div key={`${ln.key}${l.day}`} title={`${l.pct}%`} style={{ position: 'absolute', insetInlineStart: x, top: i * rowH, width: pxPerDay, height: rowH,
                background: hot ? 'rgba(180,35,24,0.12)' : `rgba(29,78,216,${Math.min(0.12, l.pct / 900)})` }} />;
            }))}
            {todayX >= 0 && todayX <= width && <div style={{ position: 'absolute', insetInlineStart: todayX, top: 0, bottom: 0, borderInlineStart: '2px solid #e8590c' }} />}
            <svg width={width} height={Math.max(1, lines.length) * rowH} style={{ position: 'absolute', inset: 0, pointerEvents: 'none', transform: rtl ? 'scaleX(-1)' : undefined }}>
              {bars.flatMap((b) => b.deps.map((d) => {
                const a = barBox.get(d), c = barBox.get(b.id);
                if (!a || !c) return null;
                const x1 = a.x + a.w, y1 = a.y + rowH / 2, x2 = c.x, y2 = c.y + rowH / 2;
                const bad = flagged.has(b.id) && warnings.some((w) => w.kind === 'dependency' && w.ids?.includes(b.id));
                return <path key={`${d}-${b.id}`} d={`M${x1},${y1} C${x1 + 14},${y1} ${x2 - 14},${y2} ${x2},${y2}`} fill="none" stroke={bad ? '#b42318' : '#7a8699'} strokeWidth={1.4} markerEnd="url(#ga)" />;
              }))}
              <defs><marker id="ga" markerWidth="6" markerHeight="6" refX="5" refY="3" orient="auto"><path d="M0,0 L6,3 L0,6 z" fill="#7a8699" /></marker></defs>
            </svg>
            {lines.map((ln) => ln.bars.map((b) => {
              const box = barBox.get(b.id)!;
              const moving = drag?.id === b.id;
              const done = b.status === 'done';
              const bad = flagged.has(b.id);
              return (
                <div key={b.id} role="button" tabIndex={0} aria-label={`${b.label} ${b.start} → ${b.end}`} title={`${b.label}\n${b.start.replace('T', ' ').slice(0, 16)} → ${b.end.replace('T', ' ').slice(0, 16)}${b.row_label && perBar ? `\n${b.row_label}` : ''}`}
                  onPointerDown={(e) => { if (readOnly || !onMove || done) return; (e.target as HTMLElement).setPointerCapture(e.pointerId); setDrag({ id: b.id, x0: e.clientX, y0: e.clientY, dx: 0, dy: 0 }); }}
                  onPointerMove={(e) => { if (moving) setDrag({ ...drag!, dx: e.clientX - drag!.x0, dy: e.clientY - drag!.y0 }); }}
                  onPointerUp={() => moving && finish(b)}
                  style={{ position: 'absolute', insetInlineStart: box.x, top: box.y + 5, width: box.w, height: rowH - 10, borderRadius: 4, fontSize: 11, color: '#fff',
                    padding: '0 4px', overflow: 'hidden', whiteSpace: 'nowrap', textOverflow: 'ellipsis', cursor: readOnly || done ? 'default' : 'grab', touchAction: 'none',
                    background: done ? '#2f9e44' : b.planned ? '#1d4ed8' : '#8aa0c8', outline: bad ? '2px solid #b42318' : undefined,
                    transform: moving ? `translate(${drag!.dx}px, ${perBar ? 0 : drag!.dy}px)` : undefined, opacity: moving ? 0.85 : 1, zIndex: moving ? 5 : 1 }}>
                  <div style={{ position: 'absolute', insetInlineStart: 0, top: 0, bottom: 0, width: `${b.progress}%`, background: 'rgba(255,255,255,0.22)' }} />
                  <span style={{ position: 'relative' }}><bdi>{perBar ? b.row_label || b.ref : b.label}</bdi></span>
                </div>);
            }))}
          </div>
        </div>
      </div>
    </div>
  );
}
