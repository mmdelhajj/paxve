import { createContext, useContext, useEffect, useState } from 'react';
import QRCode from 'qrcode';
import { api } from '../api';
import { useCompany } from '../company';

// Document layouts (phase 8): the choices a company made for each printed document (colour, logo, header / footer
// text, hidden fields, block order, second language for titles, QR code, paper). Print screens read them with
// useDocLayout, draw <LayoutStyle> inside their .print-doc and tag their parts with f-<field> / b-<block> classes.

export type DocType = 'invoice' | 'receipt' | 'quote' | 'order' | 'delivery' | 'po' | 'bill';
export interface DocLayout {
  accent?: string; show_logo?: boolean; logo_size?: 'sm' | 'md' | 'lg'; title?: string; header?: string; footer?: string;
  hide?: string[]; order?: string[]; bilingual?: string; qr?: 'none' | 'doc' | 'custom'; qr_text?: string;
  paper?: 'a4' | 'a5' | '58' | '80'; font_size?: number; align?: 'start' | 'center'; border?: boolean;
}

export const LAYOUT_FIELDS = ['logo', 'tax_id', 'customer_phone', 'customer_address', 'customer_code', 'due_date', 'period', 'meter',
  'local_total', 'paid', 'balance', 'terms', 'footer', 'legal', 'cashier', 'points'] as const;
export const LAYOUT_BLOCKS = ['header', 'customer', 'lines', 'notes', 'footer'] as const;

const cache = new Map<string, Promise<Record<string, DocLayout>>>();
export function forgetLayouts(cid: string) { cache.delete(cid); }

/** The layout designer shows its unsaved choices through this override. */
export const LayoutOverride = createContext<Partial<Record<DocType, DocLayout>> | null>(null);

export function useDocLayout(type: DocType): DocLayout {
  const ov = useContext(LayoutOverride);
  const { company } = useCompany();
  const [all, setAll] = useState<Record<string, DocLayout>>({});
  useEffect(() => {
    if (ov || !company) return;
    let p = cache.get(company.id);
    if (!p) {
      p = api<Record<string, DocLayout>>(`/companies/${company.id}/doc-layouts`).catch(() => ({}));
      cache.set(company.id, p);
    }
    let live = true;
    void p.then((x) => { if (live) setAll(x); });
    return () => { live = false; };
  }, [company, ov]);
  return (ov ? ov[type] : all[type]) ?? {};
}

/** CSS of one layout, scoped to .print-doc[data-doc=<type>]. */
export function layoutCSS(type: DocType, l: DocLayout): string {
  const s = `.print-doc[data-doc="${type}"]`;
  const out: string[] = [];
  const acc = l.accent && /^#[0-9a-fA-F]{6}$/.test(l.accent) ? l.accent : '';
  if (acc) {
    out.push(`${s} .doc-title{color:${acc}}`);
    out.push(`${s} table th{background:${acc}1a;border-bottom:2px solid ${acc}}`);
    out.push(`${s} .doc-rule{border-color:${acc}}`);
  }
  if (l.font_size) out.push(`${s}{font-size:${l.font_size}px}`);
  if (l.border) out.push(`${s}{border:1px solid ${acc || '#cbd5e1'};padding:14px;border-radius:6px}`);
  if (l.align === 'center') out.push(`${s} .b-header{text-align:center}`);
  for (const f of l.hide ?? []) out.push(`${s} .f-${f}{display:none!important}`);
  if (l.order && l.order.length) {
    out.push(`${s}{display:flex;flex-direction:column}`);
    l.order.forEach((b, i) => out.push(`${s} > .b-${b}{order:${i + 1}}`));
  }
  if (l.paper === '58' || l.paper === '80') {
    out.push(`${s}{max-width:${l.paper}mm;margin-inline:auto}`);
    out.push(`@media print{@page{size:${l.paper}mm auto;margin:2mm}}`);
  } else if (l.paper === 'a5') {
    out.push('@media print{@page{size:A5;margin:10mm}}');
  }
  return out.join('\n');
}

export function LayoutStyle({ type, layout }: { type: DocType; layout: DocLayout }) {
  const css = layoutCSS(type, layout);
  return css ? <style>{css}</style> : null;
}

export function DocQR({ text, size = 96 }: { text: string; size?: number }) {
  const [src, setSrc] = useState('');
  useEffect(() => {
    if (!text) { setSrc(''); return; }
    QRCode.toDataURL(text, { margin: 1, width: size * 2 }).then(setSrc).catch(() => setSrc(''));
  }, [text, size]);
  return src ? <img src={src} alt="QR" width={size} height={size} className="f-qr" style={{ display: 'block', margin: '8px auto 0' }} /> : null;
}

/** The QR text of a layout for a document ("" = none). */
export function qrTextOf(l: DocLayout, docText: string): string {
  if (l.qr === 'doc') return docText;
  if (l.qr === 'custom') return l.qr_text ?? '';
  return '';
}

/** Extra header text of the layout (for documents without the shared DocHeader). */
export function LayoutBanner({ layout }: { layout: DocLayout }) {
  return layout.header ? <div className="b-header" style={{ whiteSpace: 'pre-wrap', fontSize: 12, marginBottom: 6 }}>{layout.header}</div> : null;
}

/** Footer text + QR code of the layout (for documents without the shared DocFooter). */
export function LayoutFooter({ layout, qr }: { layout: DocLayout; qr: string }) {
  const q = qrTextOf(layout, qr);
  if (!layout.footer && !q) return null;
  return (
    <div className="b-footer" style={{ marginTop: 10, textAlign: 'center' }}>
      {layout.footer && <div className="f-footer" style={{ whiteSpace: 'pre-wrap', fontSize: 12 }}>{layout.footer}</div>}
      {q && <DocQR text={q} />}
    </div>
  );
}

/** Layout style + header text for a print screen that does not read the layout itself. */
export function LayoutHead({ type }: { type: DocType }) {
  const l = useDocLayout(type);
  return <><LayoutStyle type={type} layout={l} /><LayoutBanner layout={l} /></>;
}

/** Layout footer + QR for a print screen that does not read the layout itself. */
export function LayoutTail({ type, qr }: { type: DocType; qr: string }) {
  const l = useDocLayout(type);
  return <LayoutFooter layout={l} qr={qr} />;
}