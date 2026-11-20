import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, type Me } from '../../api';
import { formatAmount } from '../../util';
import { useBase, useLoad, useRun } from '../wh/Wh';

// My dashboard (phase 8): widgets picked from a catalogue of safe metrics, arranged by drag and drop (or the arrows),
// saved per user; without one the role's default (set by an admin), the company's, or the built-in one shows.

export interface Widget { id: string; metric: string; size: number; title?: string }
interface Metric { key: string; kind: 'kpi' | 'count' | 'chart' | 'list' }
type Point = { label: string; value: string };
type Row = { label: string; sub?: string; value: string };
interface Datum { kind: string; denied?: boolean; data?: { value?: string | number; currency?: string; series?: Point[]; rows?: Row[] } }

function useMetrics(): Metric[] {
  const base = useBase();
  const [m] = useLoad<Metric[]>(base && `${base}/dashboards/metrics`, []);
  return m;
}

export default function MyDash({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [mine, reload] = useLoad<{ source: string; widgets: Widget[] } | null>(base && `${base}/dashboards/mine`, null);
  const [edit, setEdit] = useState(false);
  const [ws, setWs] = useState<Widget[]>([]);
  const [data, setData] = useState<Record<string, Datum>>({});
  const [msg, run] = useRun(() => { reload(); setEdit(false); });
  useEffect(() => { if (mine) setWs(mine.widgets); }, [mine]);
  const keys = useMemo(() => [...new Set(ws.map((w) => w.metric))], [ws]);
  useEffect(() => {
    if (!base || keys.length === 0) return;
    api<Record<string, Datum>>(`${base}/dashboards/data`, { method: 'POST', body: { metrics: keys } }).then(setData).catch(() => setData({}));
  }, [base, keys]);
  void me;
  return (
    <div className="mx-col">
      <div className="mx-strip"><b>{t('ep.t.mydash')}</b><span className="mx-sp" />
        {mine && <small style={{ opacity: 0.85 }}>{t(`ep.dash.src.${mine.source}`)}</small>}
        {!edit && <button className="mx-btn sm" onClick={() => setEdit(true)}>{t('ep.dash.customize')}</button>}
      </div>
      <div style={{ flex: 1, minHeight: 0, overflow: 'auto', padding: 8 }}>
        {msg && <div className={`mx-flash ${msg.ok ? 'good' : 'bad'}`}>{msg.text}</div>}
        {edit ? (
          <section className="mx-box">
            <WidgetEditor widgets={ws} onChange={setWs} />
            <div className="mx-row" style={{ gap: 6, padding: 6 }}>
              <button className="mx-btn main" onClick={() => void run(api(`${base}/dashboards/mine`, { method: 'PUT', body: { widgets: ws } }))}>{t('ep.save')}</button>
              <button className="mx-btn" onClick={() => void run(api(`${base}/dashboards/mine`, { method: 'DELETE' }))}>{t('ep.dash.backDefault')}</button>
              <button className="mx-btn" onClick={() => { setEdit(false); if (mine) setWs(mine.widgets); }}>{t('ep.cancel')}</button>
            </div>
          </section>
        ) : null}
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(220px, 1fr))', gap: 8, marginTop: edit ? 8 : 0 }}>
          {ws.map((w) => <WidgetCard key={w.id} w={w} d={data[w.metric]} />)}
        </div>
        {ws.length === 0 && <div className="mx-empty">{t('ep.dash.empty')} <button className="mx-btn sm main" onClick={() => setEdit(true)}>{t('ep.dash.customize')}</button></div>}
      </div>
    </div>
  );
}

function WidgetCard({ w, d }: { w: Widget; d?: Datum }) {
  const { t } = useTranslation();
  const span = Math.min(Math.max(w.size, 1), 3);
  const v = d?.data;
  return (
    <section className="mx-box" style={{ gridColumn: `span ${span}`, minWidth: 0, padding: 10 }}>
      <div style={{ fontSize: 12.5, color: 'var(--mx-muted)', fontWeight: 600 }}>{w.title || t(`ep.m.${w.metric}`)}</div>
      {!d ? <div style={{ opacity: 0.5 }}>…</div> : d.denied ? <div className="mx-empty">{t('ep.dash.denied')}</div> : (
        <>
          {(d.kind === 'kpi' || d.kind === 'count') && (
            <div style={{ fontSize: 26, fontWeight: 700, fontVariantNumeric: 'tabular-nums' }} dir="ltr">
              {d.kind === 'kpi' ? formatAmount(String(v?.value ?? 0), 2) : String(v?.value ?? 0)} {d.kind === 'kpi' && <small style={{ fontSize: 13 }}>{v?.currency}</small>}
            </div>)}
          {d.kind === 'chart' && <Bars series={v?.series ?? []} />}
          {d.kind === 'list' && (
            <table className="mx-table" style={{ marginTop: 4 }}>
              <tbody>{(v?.rows ?? []).map((r, i) => (
                <tr key={i}><td>{w.metric === 'alerts_list' ? t(`sm.al.k.${r.label}`, { defaultValue: r.label }) : r.label}{r.sub && <small> · {r.sub}</small>}</td>
                  <td className="num">{w.metric === 'alerts_list' ? t(`sm.al.sev.${r.value}`, { defaultValue: r.value }) : formatAmount(r.value, 2)}</td></tr>))}
                {(v?.rows ?? []).length === 0 && <tr><td className="mx-empty">{t('ep.dash.nothing')}</td></tr>}
              </tbody>
            </table>)}
        </>)}
    </section>
  );
}

function Bars({ series }: { series: Point[] }) {
  const max = Math.max(1, ...series.map((p) => Number(p.value) || 0));
  const w = 100 / Math.max(series.length, 1);
  return (
    <svg viewBox="0 0 100 44" style={{ width: '100%', height: 110 }} role="img" aria-label={series.map((p) => `${p.label} ${p.value}`).join(', ')} preserveAspectRatio="none">
      {series.map((p, i) => {
        const h = ((Number(p.value) || 0) / max) * 38;
        return <rect key={p.label} x={i * w + w * 0.15} y={40 - h} width={w * 0.7} height={Math.max(h, 0.3)} fill="#1d4ed8" rx={0.6}><title>{`${p.label}: ${p.value}`}</title></rect>;
      })}
      <line x1="0" y1="40.2" x2="100" y2="40.2" stroke="#cbd5e1" strokeWidth="0.3" />
    </svg>
  );
}

/** Pick, size, title and arrange widgets (drag a card onto another, or use the arrows). */
export function WidgetEditor({ widgets, onChange }: { widgets: Widget[]; onChange: (w: Widget[]) => void }) {
  const { t } = useTranslation();
  const metrics = useMetrics();
  const [add, setAdd] = useState('');
  const [drag, setDrag] = useState<number | null>(null);
  const move = (from: number, to: number) => {
    if (from === to || to < 0 || to >= widgets.length) return;
    const n = [...widgets];
    const [x] = n.splice(from, 1);
    n.splice(to, 0, x);
    onChange(n);
  };
  const newId = () => { let i = widgets.length; while (widgets.some((w) => w.id === `w${i}`)) i++; return `w${i}`; };
  return (
    <div className="mx-col" style={{ gap: 6, padding: 6 }}>
      <div className="mx-row" style={{ gap: 6, alignItems: 'end', flexWrap: 'wrap' }}>
        <label className="mx-fld">{t('ep.dash.add')}<select value={add} onChange={(e) => setAdd(e.currentTarget.value)}><option value="">—</option>
          {metrics.map((m) => <option key={m.key} value={m.key}>{t(`ep.m.${m.key}`)}</option>)}</select></label>
        <button className="mx-btn" disabled={!add || widgets.length >= 24} title={!add ? t('ep.dash.pick') : undefined} onClick={() => {
          const k = metrics.find((m) => m.key === add)?.kind;
          onChange([...widgets, { id: newId(), metric: add, size: k === 'chart' || k === 'list' ? 2 : 1 }]); setAdd('');
        }}>{t('ep.add')}</button>
      </div>
      {widgets.map((w, i) => (
        <div key={w.id} draggable onDragStart={() => setDrag(i)} onDragOver={(e) => e.preventDefault()} onDrop={() => { if (drag != null) move(drag, i); setDrag(null); }}
          className="mx-row" style={{ gap: 6, alignItems: 'center', padding: 4, border: '1px dashed var(--mx-line-strong)', borderRadius: 6, background: drag === i ? '#eef3ff' : undefined, cursor: 'grab', flexWrap: 'wrap' }}>
          <span aria-hidden>⠿</span>
          <span style={{ minWidth: 160 }}>{t(`ep.m.${w.metric}`)}</span>
          <input placeholder={t('ep.dash.titleOpt')} aria-label={t('ep.dash.titleOpt')} maxLength={60} value={w.title ?? ''} onChange={(e) => onChange(widgets.map((x, j) => (j === i ? { ...x, title: e.currentTarget.value || undefined } : x)))} />
          <select aria-label={t('ep.dash.size')} value={w.size} onChange={(e) => onChange(widgets.map((x, j) => (j === i ? { ...x, size: Number(e.currentTarget.value) } : x)))}>
            {[1, 2, 3].map((s) => <option key={s} value={s}>{t(`ep.dash.sz${s}`)}</option>)}</select>
          <button className="mx-btn sm" aria-label={t('ep.up')} disabled={i === 0} onClick={() => move(i, i - 1)}>↑</button>
          <button className="mx-btn sm" aria-label={t('ep.down')} disabled={i === widgets.length - 1} onClick={() => move(i, i + 1)}>↓</button>
          <button className="mx-btn sm danger" aria-label={t('ep.remove')} onClick={() => onChange(widgets.filter((_, j) => j !== i))}>×</button>
        </div>))}
      {widgets.length === 0 && <div className="mx-empty">{t('ep.dash.noWidgets')}</div>}
    </div>
  );
}
