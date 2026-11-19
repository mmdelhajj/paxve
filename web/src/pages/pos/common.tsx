import { useEffect, useState } from 'react';
import { Badge, Center, Divider, Group, Loader, Paper, SimpleGrid, Table, Text } from '@mantine/core';
import { useTranslation } from 'react-i18next';
import { api } from '../../api';
import { useCompany } from '../../company';
import { formatAmount, formatDate, formatDateTime, notifyError } from '../../util';
import type { PrintLine } from '../../bt';

// ---------- types ----------

export interface Register {
  id: string; name: string; currency: string; warehouse_id: string; warehouse: string; customer_id: string; customer_name: string;
  method_ids: string[]; revenue_account_id: string | null; over_short_account_id: string; cashier_refunds: boolean; cashier_price_edit: boolean;
  receipt_footer: string; is_active: boolean; cashier_quick_item?: boolean; drawer_kick?: boolean; customer_display?: boolean;
  open_session: { id: string; number: number; opened_at: string; opened_by: string | null } | null;
}

export interface CatItem {
  id: string; code: string; name: string; name_i18n: Record<string, string> | null; kind: 'stock' | 'service' | 'consumable'; unit: string;
  barcode: string; category_id: string | null; image: string; price: string; tax_rate_id: string | null; tax_rate: string | null;
  tax_name: string | null; stock: string;
  template_id?: string | null; variant_values?: Record<string, string>; sold_by_weight?: boolean; plu?: number | null; brand?: string;
}

export interface ScalePrefix { prefix: string; mode: 'weight' | 'price'; decimals: number }

export interface Category { id: string; name: string; name_i18n: Record<string, string> | null; color: string }

export interface TillMethod { id: string; name: string; kind: string; currency: string; change_rounding: string | null }

export interface Catalog {
  register: Register; items: CatItem[]; categories: Category[]; methods: TillMethod[];
  rates: { currency: string; rate: string; rate_date: string }[];
  company: { name_en: string; name_ar: string; base_currency: string; local_currency: string; tax_id: string; doc_header: string; doc_footer: string };
  templates?: { id: string; name: string; name_i18n: Record<string, string> | null; attributes: { name: string; values: string[] }[]; image: string; category_id: string | null }[];
  retail?: { scale: { prefixes: ScalePrefix[] }; loyalty: { is_active: boolean; point_value: string; min_redeem: string } | null; promotions: boolean };
}

export interface PosOrder {
  id: string; number: number; kind: 'sale' | 'refund'; order_date: string; session_id: string; session_number: number; register_id: string;
  register_name: string; customer_id: string; customer_name: string; customer_code: string; walk_in: boolean; currency: string; total: string;
  tax_total: string; paid: string; on_account: string; invoice_id: string | null; invoice_number: number | null; payment_id: string | null;
  refund_doc_id: string | null; refund_of: string | null; refund_of_number: number | null;
  cash: { method_id: string; currency: string; amount: string }[]; client_ref: string | null; notes: string; cashier: string | null; created_at: string;
  company_name?: string; company_name_ar?: string; company_tax_id?: string; company_header?: string; receipt_footer?: string;
  promos?: { id: string; name: string; name_i18n?: Record<string, string>; discount: string }[];
  loyalty?: { earned?: string; redeemed?: string; redeemed_value?: string; balance?: string };
  gift?: { code: string; amount: string; kind: 'sold' | 'redeemed' }[];
}

export interface PosOrderDetail {
  order: PosOrder;
  lines: { line_no: number; item_id: string | null; description: string; quantity: string; unit_price: string; amount: string; tax_amount: string; returned?: string }[];
  tenders: { method: string; method_id: string; currency: string; amount: string; rate: string }[];
  change: { currency: string; amount: string; method: string } | null;
  pending?: boolean; // taken offline, not sent yet
}

export interface DrawerLine {
  method_id: string; name: string; currency: string; opening: string; in: string; out: string; expected: string; counted?: string; difference?: string;
}

export interface SessionDetail {
  session: {
    id: string; number: number; status: 'open' | 'closed'; register_id: string; register_name: string; currency: string; opened_at: string;
    opened_by: string | null; closed_at: string | null; closed_by: string | null; notes: string; company_name: string; company_name_ar: string;
    over_short_entry_id: string | null;
  };
  drawer: DrawerLine[];
  summary: { currency: string; sales: number; refunds: number; sales_total: string; refunds_total: string; tax: string; net: string; on_account: string }[];
  items: { description: string; item_id: string | null; quantity: string; amount: string; currency: string }[];
  cashiers: { cashier: string | null; currency: string; sales: number; net: string }[];
}

// ---------- money helpers (display / preview only: the server recomputes with exact decimals) ----------

export function round(v: number, dec: number): number {
  const f = 10 ** dec;
  return Math.round((v + Math.sign(v) * 1e-9) * f) / f;
}

export function itemName(it: { name: string; name_i18n: Record<string, string> | null }, lang: string): string {
  return it.name_i18n?.[lang] || it.name;
}

// ---------- receipt ----------

type T = (k: string, o?: Record<string, unknown>) => string;

export function orderReceiptLines(d: PosOrderDetail, t: T, dec: (c: string) => number): PrintLine[] {
  const o = d.order;
  const rtl = document.dir === 'rtl';
  const cd = dec(o.currency);
  const title = o.kind === 'refund' ? t('pos.refundReceipt') : t('pos.receipt');
  const L: PrintLine[] = [
    { text: (rtl && o.company_name_ar ? o.company_name_ar : o.company_name) || '', bold: true, size: 30, align: 'center' },
  ];
  if (o.company_header) for (const h of o.company_header.split('\n').slice(0, 3)) L.push({ text: h, align: 'center', size: 20 });
  if (o.company_tax_id) L.push({ text: `${t('docs.taxId')}: ${o.company_tax_id}`, align: 'center', size: 20 });
  L.push({ text: `${title} #${o.number || '…'} · ${o.order_date ? formatDate(o.order_date) : ''}`, align: 'center', size: 22 });
  L.push({ text: `${o.register_name}${o.cashier ? ' · ' + o.cashier : ''}`, align: 'center', size: 20 });
  if (!o.walk_in) L.push({ text: `${o.customer_name} (${o.customer_code})`, bold: true, size: 22 });
  L.push({ text: '', rule: true });
  for (const l of d.lines) {
    L.push({ text: l.description, size: 22 });
    L.push({ text: `  ${Number(l.quantity)} × ${formatAmount(l.unit_price, cd)}`, right: formatAmount(l.amount, cd), size: 22 });
  }
  L.push({ text: '', rule: true });
  if (Number(o.tax_total) > 0) L.push({ text: t('pos.tax'), right: formatAmount(o.tax_total, cd), size: 22 });
  L.push({ text: t('pos.total'), right: `${formatAmount(o.total, cd)} ${o.currency}`, bold: true, size: 30 });
  for (const x of d.tenders) L.push({ text: x.method, right: `${formatAmount(x.amount, dec(x.currency))} ${x.currency}`, size: 22 });
  if (d.change && Number(d.change.amount) > 0) {
    L.push({ text: t('payments.change'), right: `${formatAmount(d.change.amount, dec(d.change.currency))} ${d.change.currency}`, bold: true, size: 24 });
  }
  if (o.kind === 'sale' && Number(o.on_account) > 0) L.push({ text: t('pos.onAccount'), right: `${formatAmount(o.on_account, cd)} ${o.currency}`, size: 22 });
  if (o.kind === 'refund' && Number(o.paid) > 0) L.push({ text: t('pos.paidBack'), right: `${formatAmount(o.paid, cd)} ${o.currency}`, size: 22 });
  const lang = document.documentElement.lang || 'en';
  const saved = (o.promos ?? []).reduce((s, p) => s + Number(p.discount), 0);
  for (const p of o.promos ?? []) L.push({ text: p.name_i18n?.[lang] || p.name, right: `-${formatAmount(p.discount, cd)}`, size: 20 });
  if (saved > 0) L.push({ text: t('rt.pos.saved'), right: `${formatAmount(saved, cd)} ${o.currency}`, bold: true, size: 22 });
  for (const g of o.gift ?? []) L.push({ text: `${t(g.kind === 'sold' ? 'rt.pos.giftSold' : 'rt.pos.giftUsed')} ${g.code}`, right: formatAmount(g.amount, cd), size: 20 });
  if (o.loyalty && o.loyalty.balance !== undefined) L.push({ text: t('rt.pos.points', { earned: Number(o.loyalty.earned ?? 0), balance: Number(o.loyalty.balance) }), size: 20 });
  if (d.pending) L.push({ text: t('collector.pendingSync'), align: 'center', size: 20 });
  L.push({ text: '', rule: true });
  L.push({ text: o.receipt_footer || t('payments.thanks'), align: 'center', size: 20 });
  return L;
}

export function OrderReceipt({ d }: { d: PosOrderDetail }) {
  const { t, i18n } = useTranslation();
  const { decimalsOf } = useCompany();
  const o = d.order;
  const rtl = i18n.language === 'ar';
  const cd = decimalsOf(o.currency);
  return (
    <div className="print-doc receipt" dir={rtl ? 'rtl' : 'ltr'}>
      <Text fw={700} ta="center">{(rtl && o.company_name_ar ? o.company_name_ar : o.company_name) || ''}</Text>
      {o.company_header && <Text ta="center" size="xs" style={{ whiteSpace: 'pre-line' }}>{o.company_header}</Text>}
      {o.company_tax_id && <Text ta="center" size="xs">{t('docs.taxId')}: {o.company_tax_id}</Text>}
      <Text ta="center" size="sm">{o.kind === 'refund' ? t('pos.refundReceipt') : t('pos.receipt')} #{o.number || '…'}
        {o.order_date ? ` · ${formatDate(o.order_date)}` : ''}</Text>
      <Text ta="center" size="xs" c="dimmed">{o.register_name}{o.cashier ? ` · ${o.cashier}` : ''}
        {o.refund_of_number ? ` · ${t('pos.returnOf')} #${o.refund_of_number}` : ''}</Text>
      {!o.walk_in && <Text size="sm" fw={600}>{o.customer_name} ({o.customer_code})</Text>}
      <Divider my={6} variant="dashed" />
      <table>
        <tbody>
          {d.lines.map((l) => (
            <tr key={l.line_no}>
              <td>{l.description}<div style={{ fontSize: 11 }} dir="ltr">{Number(l.quantity)} × {formatAmount(l.unit_price, cd)}</div></td>
              <td className="num">{formatAmount(l.amount, cd)}</td>
            </tr>
          ))}
        </tbody>
        <tfoot>
          {Number(o.tax_total) > 0 && <tr><td>{t('pos.tax')}</td><td className="num">{formatAmount(o.tax_total, cd)}</td></tr>}
          <tr><th>{t('pos.total')}</th><th className="num">{formatAmount(o.total, cd)} {o.currency}</th></tr>
          {d.tenders.map((x, i) => (
            <tr key={'t' + i}><td>{x.method}</td><td className="num">{formatAmount(x.amount, decimalsOf(x.currency))} {x.currency}</td></tr>
          ))}
          {d.change && Number(d.change.amount) > 0 && (
            <tr><th>{t('payments.change')}</th><th className="num">{formatAmount(d.change.amount, decimalsOf(d.change.currency))} {d.change.currency}</th></tr>
          )}
          {o.kind === 'sale' && Number(o.on_account) > 0 && (
            <tr><td>{t('pos.onAccount')}</td><td className="num">{formatAmount(o.on_account, cd)} {o.currency}</td></tr>
          )}
          {o.kind === 'refund' && Number(o.paid) > 0 && (
            <tr><td>{t('pos.paidBack')}</td><td className="num">{formatAmount(o.paid, cd)} {o.currency}</td></tr>
          )}
          {(o.promos ?? []).map((p) => (
            <tr key={'p' + p.id}><td>{p.name_i18n?.[i18n.language] || p.name}</td><td className="num">-{formatAmount(p.discount, cd)}</td></tr>
          ))}
          {(o.promos ?? []).length > 0 && <tr><th>{t('rt.pos.saved')}</th><th className="num">{formatAmount((o.promos ?? []).reduce((s, p) => s + Number(p.discount), 0), cd)} {o.currency}</th></tr>}
          {(o.gift ?? []).map((g) => (
            <tr key={'g' + g.code + g.kind}><td>{t(g.kind === 'sold' ? 'rt.pos.giftSold' : 'rt.pos.giftUsed')} <span dir="ltr">{g.code}</span></td><td className="num">{formatAmount(g.amount, cd)}</td></tr>
          ))}
          {o.loyalty && o.loyalty.balance !== undefined && (
            <tr><td colSpan={2}>{t('rt.pos.points', { earned: Number(o.loyalty.earned ?? 0), balance: Number(o.loyalty.balance) })}</td></tr>
          )}
        </tfoot>
      </table>
      <Divider my={6} variant="dashed" />
      <Text size="xs" ta="center" style={{ whiteSpace: 'pre-line' }}>{o.receipt_footer || t('payments.thanks')}</Text>
    </div>
  );
}

// ---------- Z report (session) ----------

export function zReportLines(d: SessionDetail, t: T, dec: (c: string) => number): PrintLine[] {
  const s = d.session;
  const rtl = document.dir === 'rtl';
  const L: PrintLine[] = [
    { text: rtl && s.company_name_ar ? s.company_name_ar : s.company_name, bold: true, size: 28, align: 'center' },
    { text: `${s.status === 'closed' ? t('pos.zReport') : t('pos.xReport')} · ${s.register_name} #${s.number}`, align: 'center', size: 22 },
    { text: `${formatDateTime(s.opened_at)}${s.closed_at ? ' → ' + formatDateTime(s.closed_at) : ''}`, align: 'center', size: 18 },
    { text: '', rule: true },
  ];
  for (const x of d.summary) {
    const cd = dec(x.currency);
    L.push({ text: `${t('pos.sales')} (${x.sales})`, right: `${formatAmount(x.sales_total, cd)} ${x.currency}`, size: 22 });
    if (x.refunds > 0) L.push({ text: `${t('pos.refunds')} (${x.refunds})`, right: `-${formatAmount(x.refunds_total, cd)}`, size: 22 });
    L.push({ text: t('pos.tax'), right: formatAmount(x.tax, cd), size: 22 });
    if (Number(x.on_account) > 0) L.push({ text: t('pos.onAccount'), right: formatAmount(x.on_account, cd), size: 22 });
    L.push({ text: t('pos.net'), right: `${formatAmount(x.net, cd)} ${x.currency}`, bold: true, size: 26 });
  }
  L.push({ text: '', rule: true });
  for (const m of d.drawer) {
    const md = dec(m.currency);
    L.push({ text: `${m.name} (${m.currency})`, bold: true, size: 22 });
    L.push({ text: `  ${t('pos.opening')}`, right: formatAmount(m.opening, md), size: 20 });
    L.push({ text: `  ${t('pos.cashIn')}`, right: formatAmount(m.in, md), size: 20 });
    L.push({ text: `  ${t('pos.cashOut')}`, right: `-${formatAmount(m.out, md)}`, size: 20 });
    L.push({ text: `  ${t('pos.expected')}`, right: formatAmount(m.expected, md), size: 20 });
    if (m.counted !== undefined) {
      L.push({ text: `  ${t('pos.counted')}`, right: formatAmount(m.counted, md), size: 20 });
      L.push({ text: `  ${t('pos.difference')}`, right: formatAmount(m.difference, md), bold: true, size: 20 });
    }
  }
  if (d.items.length) {
    L.push({ text: '', rule: true });
    for (const it of d.items.slice(0, 40)) L.push({ text: `${Number(it.quantity)} × ${it.description}`, right: formatAmount(it.amount, dec(it.currency)), size: 20 });
  }
  L.push({ text: '', rule: true });
  L.push({ text: `${s.opened_by ?? ''}${s.closed_by ? ' / ' + s.closed_by : ''}`, align: 'center', size: 18 });
  return L;
}

export function SessionReport({ id, data }: { id: string; data?: SessionDetail | null }) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [d, setD] = useState<SessionDetail | null>(data ?? null);
  useEffect(() => {
    if (data) { setD(data); return; }
    if (company) api<SessionDetail>(`/companies/${company.id}/pos/sessions/${id}`).then(setD).catch((e) => notifyError(t, e));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [company, id, data]);
  if (!d) return <Center p="xl"><Loader size="sm" /></Center>;
  const s = d.session;
  return (
    <div className="print-doc" dir={i18n.language === 'ar' ? 'rtl' : 'ltr'}>
      <Group justify="space-between" align="flex-start">
        <div>
          <Text fw={700}>{s.status === 'closed' ? t('pos.zReport') : t('pos.xReport')} · {s.register_name} #{s.number}</Text>
          <Text size="xs" c="dimmed">{formatDateTime(s.opened_at)}{s.closed_at ? ` → ${formatDateTime(s.closed_at)}` : ''}</Text>
          <Text size="xs" c="dimmed">{s.opened_by}{s.closed_by ? ` / ${s.closed_by}` : ''}</Text>
        </div>
        <Badge color={s.status === 'open' ? 'teal' : 'gray'} variant="light">{t(`pos.status.${s.status}`)}</Badge>
      </Group>
      <SimpleGrid cols={{ base: 1, sm: Math.max(1, Math.min(3, d.summary.length)) }} mt="sm">
        {d.summary.length === 0 ? <Text c="dimmed" size="sm">{t('pos.noSales')}</Text> : d.summary.map((x) => {
          const cd = decimalsOf(x.currency);
          return (
            <Paper withBorder p="sm" key={x.currency}>
              <Text size="xs" c="dimmed">{t('pos.net')} · {x.currency}</Text>
              <Text fw={700} size="lg" dir="ltr">{formatAmount(x.net, cd)}</Text>
              <Text size="xs">{t('pos.sales')}: {x.sales} · <span dir="ltr">{formatAmount(x.sales_total, cd)}</span></Text>
              {x.refunds > 0 && <Text size="xs">{t('pos.refunds')}: {x.refunds} · <span dir="ltr">-{formatAmount(x.refunds_total, cd)}</span></Text>}
              <Text size="xs">{t('pos.tax')}: <span dir="ltr">{formatAmount(x.tax, cd)}</span></Text>
              {Number(x.on_account) > 0 && <Text size="xs">{t('pos.onAccount')}: <span dir="ltr">{formatAmount(x.on_account, cd)}</span></Text>}
            </Paper>
          );
        })}
      </SimpleGrid>
      <Text fw={600} size="sm" mt="md" mb={4}>{t('pos.drawer')}</Text>
      <Table.ScrollContainer minWidth={520}>
        <Table withTableBorder>
          <Table.Thead><Table.Tr>
            <Table.Th>{t('pos.box')}</Table.Th><Table.Th className="num">{t('pos.opening')}</Table.Th><Table.Th className="num">{t('pos.cashIn')}</Table.Th>
            <Table.Th className="num">{t('pos.cashOut')}</Table.Th><Table.Th className="num">{t('pos.expected')}</Table.Th>
            {s.status === 'closed' && <><Table.Th className="num">{t('pos.counted')}</Table.Th><Table.Th className="num">{t('pos.difference')}</Table.Th></>}
          </Table.Tr></Table.Thead>
          <Table.Tbody>
            {d.drawer.map((m) => {
              const md = decimalsOf(m.currency);
              const diff = Number(m.difference ?? 0);
              return (
                <Table.Tr key={m.method_id}>
                  <Table.Td>{m.name} <Text span size="xs" c="dimmed">{m.currency}</Text></Table.Td>
                  <Table.Td className="num" ff="monospace" dir="ltr">{formatAmount(m.opening, md)}</Table.Td>
                  <Table.Td className="num" ff="monospace" dir="ltr">{formatAmount(m.in, md)}</Table.Td>
                  <Table.Td className="num" ff="monospace" dir="ltr">{formatAmount(m.out, md)}</Table.Td>
                  <Table.Td className="num" ff="monospace" dir="ltr" fw={600}>{formatAmount(m.expected, md)}</Table.Td>
                  {s.status === 'closed' && <>
                    <Table.Td className="num" ff="monospace" dir="ltr">{formatAmount(m.counted, md)}</Table.Td>
                    <Table.Td className="num" ff="monospace" dir="ltr" c={diff < 0 ? 'red' : diff > 0 ? 'teal' : undefined} fw={600}>{formatAmount(m.difference, md)}</Table.Td>
                  </>}
                </Table.Tr>
              );
            })}
          </Table.Tbody>
        </Table>
      </Table.ScrollContainer>
      {d.items.length > 0 && <>
        <Text fw={600} size="sm" mt="md" mb={4}>{t('pos.itemsSold')}</Text>
        <Table withTableBorder>
          <Table.Tbody>
            {d.items.map((it, i) => (
              <Table.Tr key={i}>
                <Table.Td>{it.description}</Table.Td>
                <Table.Td className="num" ff="monospace">{Number(it.quantity)}</Table.Td>
                <Table.Td className="num" ff="monospace" dir="ltr">{formatAmount(it.amount, decimalsOf(it.currency))} {it.currency}</Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      </>}
      {d.cashiers.length > 1 && <>
        <Text fw={600} size="sm" mt="md" mb={4}>{t('pos.byCashier')}</Text>
        {d.cashiers.map((c, i) => (
          <Text size="sm" key={i}>{c.cashier ?? '?'} · {c.sales} · <span dir="ltr">{formatAmount(c.net, decimalsOf(c.currency))} {c.currency}</span></Text>
        ))}
      </>}
    </div>
  );
}
