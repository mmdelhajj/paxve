import { useEffect, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent, type ReactNode, type Ref } from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import { API_BASE, ApiError } from '../api';
import { printPage, saveFile } from '../native';
import { formatAmount, notifyError } from '../util';
import { toCSV, type ExportTable } from './ExportMenu';

/** One grid column. `get` gives the raw value (filter, sort, copy, export, totals); `render` the cell content. */
export interface Col<T> {
  key: string;
  title: string;
  get: (r: T) => string | number | null | undefined;
  render?: (r: T) => ReactNode;
  width?: number;
  num?: boolean;            // right-aligned number (sorted numerically)
  decimals?: number;        // for num columns without render
  total?: 'sum' | 'count' | ((rows: T[]) => ReactNode);
  filter?: boolean;         // filter box in the filter row (default true)
  cls?: (r: T) => string;   // extra class for the cell (red, ok, muted)
}

export interface DataGridProps<T> {
  rows: T[];
  cols: Col<T>[];
  rowKey: (r: T) => string;
  title?: string;                     // export / print title
  selected?: string | null;
  onSelect?: (r: T) => void;
  onActivate?: (r: T) => void;        // Enter or double-click
  filterRow?: boolean;
  filters?: Record<string, string>;   // controlled filters (optional)
  onFilters?: (f: Record<string, string>) => void;
  firstFilterRef?: Ref<HTMLInputElement>;
  footer?: boolean;
  empty?: ReactNode;
  loading?: boolean;
  rowClass?: (r: T) => string;
  serverFiltered?: boolean;           // filters are applied by the caller (don't filter again)
  className?: string;
}

const ROW_H = 29; // 28px cell + 1px border (keep in step with .mx-grid td height in magic.css)

function cmp(a: unknown, b: unknown, num: boolean): number {
  if (num) return (Number(a) || 0) - (Number(b) || 0);
  return String(a ?? '').localeCompare(String(b ?? ''), undefined, { numeric: true });
}

function norm(s: string): string {
  // Arabic: ignore hamza forms / ta marbuta / alef maqsura differences when filtering
  return s.toLowerCase().replace(/[أإآ]/g, 'ا').replace(/ة/g, 'ه').replace(/ى/g, 'ي').replace(/\s+/g, ' ');
}

export default function DataGrid<T>(p: DataGridProps<T>) {
  const { t, i18n } = useTranslation();
  const [ownFilters, setOwnFilters] = useState<Record<string, string>>({});
  const filters = p.filters ?? ownFilters;
  const setFilters = (f: Record<string, string>) => (p.onFilters ? p.onFilters(f) : setOwnFilters(f));
  const [sort, setSort] = useState<{ key: string; dir: 1 | -1 }[]>([]);
  const [hl, setHl] = useState(0);
  const [scroll, setScroll] = useState({ top: 0, h: 600 });
  const [ctx, setCtx] = useState<{ x: number; y: number; row: T | null; col: Col<T> | null } | null>(null);
  const [printing, setPrinting] = useState(false);
  const box = useRef<HTMLDivElement>(null);

  const view = useMemo(() => {
    let out = p.rows;
    if (!p.serverFiltered) {
      const active = p.cols.filter((c) => (filters[c.key] ?? '').trim() !== '');
      if (active.length) {
        const needles = active.map((c) => [c, norm(filters[c.key].trim())] as const);
        out = out.filter((r) => needles.every(([c, n]) => norm(String(c.get(r) ?? '')).includes(n)));
      }
    }
    if (sort.length) {
      const byKey = new Map(p.cols.map((c) => [c.key, c]));
      out = [...out].sort((a, b) => {
        for (const s of sort) {
          const c = byKey.get(s.key);
          if (!c) continue;
          const d = cmp(c.get(a), c.get(b), !!c.num) * s.dir;
          if (d) return d;
        }
        return 0;
      });
    }
    return out;
  }, [p.rows, p.cols, filters, sort, p.serverFiltered]);

  // highlighted row follows the selected key when the caller controls it
  useEffect(() => {
    if (p.selected == null) return;
    const i = view.findIndex((r) => p.rowKey(r) === p.selected);
    if (i >= 0) setHl(i);
  }, [p.selected, view, p]);
  useEffect(() => { if (hl >= view.length) setHl(Math.max(0, view.length - 1)); }, [view.length, hl]);

  useLayoutEffect(() => {
    const el = box.current;
    if (!el) return;
    const upd = () => setScroll({ top: el.scrollTop, h: el.clientHeight || 600 });
    upd();
    const ro = new ResizeObserver(upd);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  useEffect(() => {
    if (!ctx) return;
    const close = () => setCtx(null);
    window.addEventListener('click', close);
    window.addEventListener('scroll', close, true);
    return () => { window.removeEventListener('click', close); window.removeEventListener('scroll', close, true); };
  }, [ctx]);

  const headH = ROW_H * (p.filterRow === false ? 1 : 2);
  const start = Math.max(0, Math.floor((scroll.top - headH) / ROW_H) - 10);
  const count = Math.ceil(scroll.h / ROW_H) + 20;
  const end = Math.min(view.length, start + count);
  const slice = view.slice(start, end);

  function scrollTo(i: number) {
    const el = box.current;
    if (!el) return;
    const top = headH + i * ROW_H;
    const footH = p.footer ? ROW_H : 0;
    if (top < el.scrollTop + headH) el.scrollTop = top - headH;
    else if (top + ROW_H > el.scrollTop + el.clientHeight - footH) el.scrollTop = top + ROW_H - el.clientHeight + footH;
  }
  function move(i: number) {
    const n = Math.max(0, Math.min(view.length - 1, i));
    setHl(n);
    scrollTo(n);
    if (view[n]) p.onSelect?.(view[n]);
  }

  function onKey(e: KeyboardEvent) {
    if (!view.length) return;
    const page = Math.max(1, Math.floor(scroll.h / ROW_H) - 3);
    if (e.key === 'ArrowDown') { e.preventDefault(); move(hl + 1); }
    else if (e.key === 'ArrowUp') { e.preventDefault(); move(hl - 1); }
    else if (e.key === 'PageDown') { e.preventDefault(); move(hl + page); }
    else if (e.key === 'PageUp') { e.preventDefault(); move(hl - page); }
    else if (e.key === 'Home' && e.ctrlKey) { e.preventDefault(); move(0); }
    else if (e.key === 'End' && e.ctrlKey) { e.preventDefault(); move(view.length - 1); }
    else if (e.key === 'Enter' && view[hl]) { e.preventDefault(); p.onActivate?.(view[hl]); }
  }
  // from the filter row: ↓ goes into the results, Enter opens the highlighted row
  function onFilterKey(e: KeyboardEvent<HTMLInputElement>) {
    if (e.key === 'ArrowDown') { e.preventDefault(); if (view.length) { box.current?.focus(); move(Math.min(hl, view.length - 1)); } }
    else if (e.key === 'Enter' && view[hl]) { e.preventDefault(); p.onActivate?.(view[hl]); }
  }

  function table(): ExportTable {
    return {
      title: p.title ?? 'paxve',
      columns: p.cols.map((c) => ({ label: c.title, numeric: c.num })),
      rows: view.map((r) => p.cols.map((c) => { const v = c.get(r); return v === undefined ? null : v; })),
      footer: p.footer ? [p.cols.map((c) => totalText(c))] : undefined,
    };
  }
  function totalValue(c: Col<T>): ReactNode {
    if (!c.total) return '';
    if (c.total === 'count') return view.length;
    if (c.total === 'sum') return formatAmount(view.reduce((s, r) => s + (Number(c.get(r)) || 0), 0), c.decimals ?? 2);
    return c.total(view);
  }
  function totalText(c: Col<T>): string | number | null {
    const v = totalValue(c);
    return typeof v === 'string' || typeof v === 'number' ? v : null;
  }
  const tsv = (rows: T[]) => rows.map((r) => p.cols.map((c) => String(c.get(r) ?? '')).join('\t')).join('\n');
  async function copy(text: string) {
    try { await navigator.clipboard.writeText(text); } catch { /* clipboard blocked */ }
  }
  async function exportCsv() {
    const tb = table();
    await saveFile(`${(tb.title || 'paxve').replace(/[^\p{L}\p{N}_.-]+/gu, '-')}.csv`, toCSV(tb), 'text/csv;charset=utf-8');
  }
  async function exportXlsx() {
    try {
      const tb = table();
      const res = await fetch(API_BASE + '/export/xlsx', {
        method: 'POST', credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'paxve' },
        body: JSON.stringify({ ...tb, rtl: i18n.dir() === 'rtl' }),
      });
      if (!res.ok) throw new ApiError(res.status, 'export_failed');
      await saveFile(`${(tb.title || 'paxve').replace(/[^\p{L}\p{N}_.-]+/gu, '-')}.xlsx`, await res.blob(),
        'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet');
    } catch (e) {
      notifyError(t, e);
    }
  }
  function doPrint() {
    setPrinting(true);
    setTimeout(() => { void printPage(p.title).finally(() => setTimeout(() => setPrinting(false), 500)); }, 50);
  }

  const toggleSort = (key: string, multi: boolean) => {
    setSort((s) => {
      const cur = s.find((x) => x.key === key);
      const next = cur ? (cur.dir === 1 ? { key, dir: -1 as const } : null) : { key, dir: 1 as const };
      const rest = multi ? s.filter((x) => x.key !== key) : [];
      return next ? [...rest, next] : rest;
    });
  };

  const width = p.cols.reduce((s, c) => s + (c.width ?? 120), 0);

  return (
    <>
      <div ref={box} className={`mx-grid ${p.className ?? ''}`} tabIndex={0} onKeyDown={onKey}
        onScroll={(e) => setScroll({ top: e.currentTarget.scrollTop, h: e.currentTarget.clientHeight })}
        onContextMenu={(e) => { e.preventDefault(); setCtx({ x: e.clientX, y: e.clientY, row: null, col: null }); }}>
        <table style={{ width: Math.max(width, 0) }}>
          <colgroup>{p.cols.map((c) => <col key={c.key} style={{ width: c.width ?? 120 }} />)}</colgroup>
          <thead>
            <tr>
              {p.cols.map((c) => {
                const s = sort.find((x) => x.key === c.key);
                return (
                  <th key={c.key} className={c.num ? 'num' : undefined} title={c.title} onClick={(e) => toggleSort(c.key, e.shiftKey)}>
                    {c.title}{s && <span className="mx-sort">{s.dir === 1 ? '▲' : '▼'}</span>}
                  </th>
                );
              })}
            </tr>
            {p.filterRow !== false && (
              <tr className="mx-frow">
                {p.cols.map((c, i) => (
                  <td key={c.key}>
                    {c.filter !== false && (
                      <input aria-label={c.title} value={filters[c.key] ?? ''} ref={i === firstFilterIndex(p.cols) ? p.firstFilterRef : undefined}
                        data-filter={c.key} onKeyDown={onFilterKey}
                        onChange={(e) => { setHl(0); setFilters({ ...filters, [c.key]: e.currentTarget.value }); }} />
                    )}
                  </td>
                ))}
              </tr>
            )}
          </thead>
          <tbody>
            {start > 0 && <tr style={{ height: start * ROW_H }} aria-hidden />}
            {slice.map((r, i) => {
              const idx = start + i;
              const key = p.rowKey(r);
              const sel = p.selected != null ? p.selected === key : idx === hl;
              return (
                <tr key={key} className={`${sel ? 'sel' : ''} ${idx % 2 ? 'alt' : ''} ${p.rowClass?.(r) ?? ''}`} data-key={key}
                  onClick={() => { setHl(idx); p.onSelect?.(r); }}
                  onDoubleClick={() => p.onActivate?.(r)}
                  onContextMenu={(e) => { e.preventDefault(); e.stopPropagation(); setHl(idx); setCtx({ x: e.clientX, y: e.clientY, row: r, col: colAt(e.target as HTMLElement, p.cols) }); }}>
                  {p.cols.map((c) => (
                    <td key={c.key} className={`${c.num ? 'num' : ''} ${c.cls?.(r) ?? ''}`}>
                      {c.render ? c.render(r) : c.num && c.get(r) != null && c.get(r) !== '' ? formatAmount(c.get(r) as string, c.decimals ?? 2) : c.get(r)}
                    </td>
                  ))}
                </tr>
              );
            })}
            {end < view.length && <tr style={{ height: (view.length - end) * ROW_H }} aria-hidden />}
          </tbody>
          {p.footer && (
            <tfoot>
              <tr>{p.cols.map((c, i) => <td key={c.key} className={c.num ? 'num' : undefined}>{i === 0 && !c.total ? t('mx.total') : totalValue(c)}</td>)}</tr>
            </tfoot>
          )}
        </table>
        {p.loading ? <div className="mx-empty">{t('mx.loading')}</div> : view.length === 0 && <div className="mx-empty">{p.empty ?? t('common.empty')}</div>}
      </div>
      <div className="mx-gridinfo no-print">
        <span>{t('mx.rows', { n: view.length })}</span>
        {view.length !== p.rows.length && !p.serverFiltered && <span>{t('mx.ofRows', { n: p.rows.length })}</span>}
        <span>{t('mx.gridHint')}</span>
      </div>
      {ctx && createPortal(
        <div className="mx-ctx" style={{ top: Math.min(ctx.y, window.innerHeight - 220), left: Math.min(ctx.x, window.innerWidth - 190) }} onClick={(e) => e.stopPropagation()}>
          {ctx.row && ctx.col && <button onClick={() => { void copy(String(ctx.col!.get(ctx.row!) ?? '')); setCtx(null); }}>{t('mx.copyCell')}</button>}
          {ctx.row && <button onClick={() => { void copy(tsv([ctx.row!])); setCtx(null); }}>{t('mx.copyRow')}</button>}
          <button onClick={() => { void copy(p.cols.map((c) => c.title).join('\t') + '\n' + tsv(view)); setCtx(null); }}>{t('mx.copyAll')}</button>
          {ctx.row && p.onActivate && <button onClick={() => { p.onActivate!(ctx.row!); setCtx(null); }}>{t('mx.open')}</button>}
          <button onClick={() => { void exportXlsx(); setCtx(null); }}>{t('gap.exportXlsx')}</button>
          <button onClick={() => { void exportCsv(); setCtx(null); }}>{t('gap.exportCsv')}</button>
          <button onClick={() => { setCtx(null); doPrint(); }}>{t('gap.print')}</button>
          {Object.values(filters).some((v) => v) && <button onClick={() => { setFilters({}); setCtx(null); }}>{t('mx.clearFilters')}</button>}
        </div>, document.body)}
      {printing && createPortal(
        <div className="mx-printhost"><div className="print-doc" dir={i18n.dir()}>
          <h3 style={{ margin: '0 0 6px' }}>{p.title}</h3>
          <table>
            <thead><tr>{p.cols.map((c) => <th key={c.key} className={c.num ? 'num' : undefined}>{c.title}</th>)}</tr></thead>
            <tbody>
              {view.map((r) => (
                <tr key={p.rowKey(r)}>{p.cols.map((c) => <td key={c.key} className={c.num ? 'num' : undefined}>
                  {c.num && c.get(r) != null && c.get(r) !== '' ? formatAmount(c.get(r) as string, c.decimals ?? 2) : String(c.get(r) ?? '')}</td>)}</tr>
              ))}
            </tbody>
            {p.footer && <tfoot><tr>{p.cols.map((c) => <th key={c.key} className={c.num ? 'num' : undefined}>{totalText(c) ?? ''}</th>)}</tr></tfoot>}
          </table>
        </div></div>, document.body)}
    </>
  );
}

function firstFilterIndex<T>(cols: Col<T>[]): number {
  return cols.findIndex((c) => c.filter !== false);
}

function colAt<T>(el: HTMLElement, cols: Col<T>[]): Col<T> | null {
  const td = el.closest('td');
  if (!td || !td.parentElement) return null;
  const i = Array.from(td.parentElement.children).indexOf(td);
  return cols[i] ?? null;
}

/** Sum of a decimal-string column, for footers of grids whose cells use render(). */
export function sumOf<T>(rows: T[], get: (r: T) => string | number | null | undefined, decimals = 2): string {
  return formatAmount(rows.reduce((s, r) => s + (Number(get(r)) || 0), 0), decimals);
}
