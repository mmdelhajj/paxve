import { useCallback, useEffect, useState } from 'react';
import {
  ActionIcon, Alert, Badge, Button, Center, Divider, Group, Loader, Modal, NumberInput, Paper, SegmentedControl, Select, SimpleGrid,
  Stack, Table, Text, Textarea, TextInput,
} from '@mantine/core';
import { useDebouncedValue } from '@mantine/hooks';
import { notifications } from '@mantine/notifications';
import { IconCheck, IconFileInvoice, IconPencil, IconPlus, IconPrinter, IconSearch, IconTrash, IconTruckDelivery, IconX } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { can, api, ApiError, type Me } from '../../api';
import { useCompany } from '../../company';
import { printPage } from '../../native';
import { formatAmount, formatDate, notifyError, notifySaved, today } from '../../util';
import PageHeader, { Confirm } from '../../components/PageHeader';
import ExportMenu from '../../components/ExportMenu';
import type { TaxRate } from '../../billing';
import { InvoiceView } from '../billing/docs';
import type { Warehouse } from '../purchases/types';
import { CustomerPicker, DocFooter, DocHeader, itemPrice, orderColor, SendDocButton, type DocCompany, type SaleItem } from './common';
import Chatter from '../../components/Chatter';
import { LayoutStyle, useDocLayout } from '../../components/DocLayout';

export interface Order {
  id: string; number: number; order_number: number | null; customer_id: string; customer_code: string; customer_name: string;
  customer_phone: string; customer_email: string; customer_address: string; lead_id: string | null; currency: string;
  quote_date: string; valid_until: string | null; order_date: string | null; status: 'draft' | 'sent' | 'confirmed' | 'done' | 'cancelled';
  subtotal: string; tax_total: string; total: string; revenue_account_id: string | null; salesperson_id: string | null;
  salesperson_name: string | null; notes: string; terms: string; accepted_at: string | null; accepted_name: string;
  delivery_status: 'none' | 'partial' | 'full' | null; invoice_status: 'none' | 'partial' | 'full' | null; created_at: string;
}

export interface OrderLine {
  id: string; line_no: number; item_id: string | null; item_code: string | null; item_kind: string | null; unit: string | null;
  description: string; quantity: string; unit_price: string; discount_pct: string; amount: string; tax_rate_id: string | null;
  tax_name: string | null; tax_amount: string; warehouse_id: string | null; warehouse_name: string | null; qty_delivered: string; qty_invoiced: string;
}

export interface OrderDetail {
  order: Order & DocCompany & { portal_token: string };
  lines: OrderLine[];
  deliveries: { id: string; number: number; delivery_date: string; status: string; lines: { line_id: string; quantity: string; description: string }[] }[];
  invoices: { id: string; number: number; issue_date: string; total: string; paid: string; status: string }[];
}

const docNumber = (o: Pick<Order, 'status' | 'number' | 'order_number'>) =>
  o.order_number && o.status !== 'draft' && o.status !== 'sent' ? `SO-${o.order_number}` : `Q-${o.number}`;

export default function Quotes({ me, kind: initialKind }: { me: Me; kind?: string }) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [kind, setKind] = useState(initialKind || 'quotes');
  const [status, setStatus] = useState('');
  const [q, setQ] = useState('');
  const [dq] = useDebouncedValue(q, 250);
  const [data, setData] = useState<{ rows: Order[]; totals: { status: string; currency: string; count: number; total: string }[] } | null>(null);
  const [view, setView] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const canEdit = can(me, 'sales.create');

  const load = useCallback(() => {
    if (!company) return;
    setData(null);
    api<typeof data>(`/companies/${company.id}/sales/orders?kind=${kind}&status=${status}&q=${encodeURIComponent(dq)}`)
      .then(setData).catch((e) => notifyError(t, e));
  }, [company, kind, status, dq, t]);
  useEffect(load, [load]);
  if (!company) return null;

  return (
    <Stack gap="lg" maw={1200}>
      <PageHeader title={t('sales.title')} subtitle={company.display ?? company.name_en}>
        <ExportMenu disabled={!data} table={() => data && ({
          title: t('sales.title'),
          columns: [{ label: t('sales.number') }, { label: t('invoices.date') }, { label: t('invoices.customer') }, { label: t('common.status') },
            { label: t('sales.total'), numeric: true }, { label: t('pur.currency') }],
          rows: data.rows.map((o) => [docNumber(o), o.quote_date, o.customer_name, t(`sales.st.${o.status}`), o.total, o.currency]),
        })} />
        {canEdit && <Button leftSection={<IconPlus size={16} />} onClick={() => setCreating(true)}>{t('sales.newQuote')}</Button>}
      </PageHeader>
      <Group gap="sm" wrap="wrap">
        <SegmentedControl value={kind} onChange={(v) => { setKind(v); setStatus(''); }} data={[
          { value: 'quotes', label: t('sales.quotes') }, { value: 'orders', label: t('sales.orders') }, { value: '', label: t('common.all') },
        ]} />
        <Select w={170} clearable placeholder={t('common.status')} value={status || null} onChange={(v) => setStatus(v ?? '')}
          data={['draft', 'sent', 'confirmed', 'done', 'cancelled'].map((s) => ({ value: s, label: t(`sales.st.${s}`) }))} />
        <TextInput leftSection={<IconSearch size={16} />} placeholder={t('common.search')} value={q} onChange={(e) => setQ(e.currentTarget.value)} w={240} />
      </Group>
      {data && data.totals.length > 0 && (
        <SimpleGrid cols={{ base: 2, sm: 4 }}>
          {data.totals.map((x) => (
            <Paper withBorder p="sm" key={x.status + x.currency}>
              <Text size="xs" c="dimmed">{t(`sales.st.${x.status}`)} · {x.count}</Text>
              <Text fw={700} dir="ltr">{formatAmount(x.total, decimalsOf(x.currency))} {x.currency}</Text>
            </Paper>
          ))}
        </SimpleGrid>
      )}
      <Paper withBorder>
        {!data ? <Center p="xl"><Loader size="sm" /></Center> : data.rows.length === 0 ? <Text c="dimmed" ta="center" p="xl">{t('common.empty')}</Text> : (
          <Table.ScrollContainer minWidth={760}>
            <Table highlightOnHover verticalSpacing="xs" key={i18n.language}>
              <Table.Thead><Table.Tr>
                <Table.Th>{t('sales.number')}</Table.Th><Table.Th>{t('invoices.date')}</Table.Th><Table.Th>{t('invoices.customer')}</Table.Th>
                <Table.Th>{t('common.status')}</Table.Th><Table.Th>{t('sales.progress')}</Table.Th><Table.Th style={{ textAlign: 'end' }}>{t('sales.total')}</Table.Th>
              </Table.Tr></Table.Thead>
              <Table.Tbody>
                {data.rows.map((o) => (
                  <Table.Tr key={o.id} style={{ cursor: 'pointer' }} onClick={() => setView(o.id)}>
                    <Table.Td ff="monospace">{docNumber(o)}</Table.Td>
                    <Table.Td>{formatDate(o.order_date ?? o.quote_date)}</Table.Td>
                    <Table.Td><Text size="sm" fw={500}>{o.customer_name}</Text><Text size="xs" c="dimmed">{o.salesperson_name}</Text></Table.Td>
                    <Table.Td>
                      <Badge variant="light" color={orderColor[o.status]}>{t(`sales.st.${o.status}`)}</Badge>
                      {o.accepted_at && o.status === 'sent' && <Badge ms={4} variant="outline" color="teal" size="sm">{t('sales.accepted')}</Badge>}
                    </Table.Td>
                    <Table.Td>
                      {(o.status === 'confirmed' || o.status === 'done') && (
                        <Group gap={4}>
                          {o.delivery_status && <Badge size="sm" variant="dot" color={o.delivery_status === 'full' ? 'teal' : o.delivery_status === 'partial' ? 'orange' : 'gray'}>{t(`sales.delivery.${o.delivery_status}`)}</Badge>}
                          <Badge size="sm" variant="dot" color={o.invoice_status === 'full' ? 'teal' : o.invoice_status === 'partial' ? 'orange' : 'gray'}>{t(`sales.invoicing.${o.invoice_status ?? 'none'}`)}</Badge>
                        </Group>
                      )}
                    </Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(o.total, decimalsOf(o.currency))} {o.currency}</Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </Paper>
      {creating && <OrderEditor onClose={() => setCreating(false)} onDone={(id) => { setCreating(false); load(); setView(id); }} />}
      {view && <OrderView id={view} me={me} onClose={() => setView(null)} onChanged={load} />}
    </Stack>
  );
}

// ---------- editor ----------

interface EditLine { key: number; item_id: string | null; description: string; quantity: string; unit_price: string; discount_pct: string; tax_rate_id: string | null; warehouse_id: string | null }
let ek = 1;
export interface QuoteTpl {
  id: string; name: string; currency: string | null; validity_days: number | null; terms: string; notes: string; is_active: boolean;
  lines: { item_id: string; description: string; quantity: string; unit_price: string; discount_pct: string; tax_rate_id: string }[];
}
const blankLine = (): EditLine => ({ key: ek++, item_id: null, description: '', quantity: '1', unit_price: '', discount_pct: '', tax_rate_id: null, warehouse_id: null });

export function OrderEditor({ detail, customer, leadId, onClose, onDone }: {
  detail?: OrderDetail; customer?: { id: string; name: string; code: string }; leadId?: string; onClose: () => void; onDone: (id: string) => void;
}) {
  const { t } = useTranslation();
  const { company, currencies, decimalsOf } = useCompany();
  const o = detail?.order;
  const base = company?.base_currency ?? 'USD';
  const [customerId, setCustomerId] = useState<string | null>(o?.customer_id ?? customer?.id ?? null);
  const [currency, setCurrency] = useState<string>(o?.currency ?? base);
  const [date, setDate] = useState(o?.quote_date ?? today());
  const [validUntil, setValidUntil] = useState(o?.valid_until ?? '');
  const [notes, setNotes] = useState(o?.notes ?? '');
  const [terms, setTerms] = useState<string | null>(o ? o.terms : null);
  const [lines, setLines] = useState<EditLine[]>(detail ? detail.lines.map((l) => ({
    key: ek++, item_id: l.item_id, description: l.description, quantity: String(Number(l.quantity)), unit_price: String(Number(l.unit_price)),
    discount_pct: Number(l.discount_pct) ? String(Number(l.discount_pct)) : '', tax_rate_id: l.tax_rate_id, warehouse_id: l.warehouse_id,
  })) : [blankLine()]);
  const [items, setItems] = useState<SaleItem[]>([]);
  const [taxes, setTaxes] = useState<TaxRate[]>([]);
  const [warehouses, setWarehouses] = useState<Warehouse[]>([]);
  const [busy, setBusy] = useState(false);
  const [tpls, setTpls] = useState<QuoteTpl[]>([]);

  useEffect(() => {
    if (!company) return;
    const b = `/companies/${company.id}`;
    api<QuoteTpl[]>(`${b}/quote-templates`).then((x) => setTpls(x.filter((q) => q.is_active))).catch(() => setTpls([]));
    api<SaleItem[]>(`${b}/items`).then((x) => setItems(x.filter((i) => i.is_active && i.is_sellable !== false))).catch(() => {});
    api<TaxRate[]>(`${b}/tax-rates`).then((x) => setTaxes(x.filter((r) => r.is_active && r.applies_to !== 'purchases'))).catch(() => {});
    api<Warehouse[]>(`${b}/warehouses`).then(setWarehouses).catch(() => {});
  }, [company]);

  const dec = decimalsOf(currency);
  const byId = new Map(items.map((i) => [i.id, i]));
  const rate = (id: string | null) => Number(taxes.find((x) => x.id === id)?.rate ?? 0);
  const r = (v: number) => Math.round(v * 10 ** dec) / 10 ** dec;
  const lineAmt = (l: EditLine) => r(Number(l.quantity || 0) * Number(l.unit_price || 0) * (100 - Number(l.discount_pct || 0)) / 100);
  const sub = lines.reduce((s, l) => s + lineAmt(l), 0);
  const tax = lines.reduce((s, l) => s + r(lineAmt(l) * rate(l.tax_rate_id) / 100), 0);
  const upd = (key: number, p: Partial<EditLine>) => setLines((ls) => ls.map((x) => (x.key === key ? { ...x, ...p } : x)));

  // a quotation template fills the lines, terms, notes and validity
  function applyTemplate(id: string | null) {
    const tp = tpls.find((x) => x.id === id);
    if (!tp) return;
    if (tp.currency) setCurrency(tp.currency);
    if (tp.terms) setTerms(tp.terms);
    if (tp.notes) setNotes(tp.notes);
    if (tp.validity_days) {
      const dd = new Date(`${date}T00:00:00Z`);
      dd.setUTCDate(dd.getUTCDate() + tp.validity_days);
      setValidUntil(dd.toISOString().slice(0, 10));
    }
    const cur = tp.currency ?? currency;
    const tl = tp.lines.map((l) => {
      const it = l.item_id ? byId.get(l.item_id) : undefined;
      return { key: ek++, item_id: l.item_id || null, description: l.description, quantity: l.quantity || '1',
        unit_price: l.unit_price || (it ? itemPrice(it, cur, base) : ''), discount_pct: l.discount_pct ?? '',
        tax_rate_id: l.tax_rate_id || it?.sale_tax_rate_id || null, warehouse_id: null } as EditLine;
    });
    if (tl.length) setLines((ls) => [...ls.filter((x) => x.item_id || x.description.trim()), ...tl]);
  }

  function pickItem(key: number, id: string | null) {
    const it = id ? byId.get(id) : undefined;
    upd(key, {
      item_id: id, description: it ? it.name : '', unit_price: it ? itemPrice(it, currency, base) : '',
      tax_rate_id: it?.sale_tax_rate_id ?? null,
    });
  }

  // the active promotions, turned into a discount % on each line (same engine as the till)
  async function applyPromos() {
    if (!company) return;
    const withItem = lines.filter((l) => l.item_id && Number(l.quantity) > 0 && Number(l.unit_price) > 0);
    try {
      const ev = await api<{ lines: string[]; total: string }>(`/companies/${company.id}/retail/promotions/evaluate`, { method: 'POST', body: {
        currency, customer_id: customerId ?? '', lines: withItem.map((l) => ({ item_id: l.item_id, quantity: l.quantity, amount: String(lineAmt(l)) })) } });
      const disc = new Map(withItem.map((l, i) => [l.key, Number(ev.lines[i] ?? 0)]));
      setLines((ls) => ls.map((l) => {
        const d = disc.get(l.key) ?? 0;
        const gross = Number(l.quantity) * Number(l.unit_price);
        if (!d || !gross) return l;
        const pct = Math.min(100, ((gross - (lineAmt(l) - d)) / gross) * 100);
        return { ...l, discount_pct: String(Math.round(pct * 10000) / 10000) };
      }));
      notifications.show({ color: Number(ev.total) > 0 ? 'pink' : 'gray', message: `${t('rt.pos.saved')}: ${formatAmount(ev.total, dec)} ${currency}` });
    } catch (e) { notifyError(t, e); }
  }

  async function save() {
    if (!company || !customerId) return;
    setBusy(true);
    try {
      const body = {
        customer_id: customerId, currency, quote_date: date, valid_until: validUntil, notes, lead_id: o?.lead_id ?? leadId ?? '',
        ...(terms !== null ? { terms } : {}),
        lines: lines.filter((l) => (l.item_id || l.description.trim()) && l.unit_price !== '').map((l) => ({
          item_id: l.item_id ?? '', description: l.description, quantity: l.quantity, unit_price: l.unit_price, discount_pct: l.discount_pct,
          tax_rate_id: l.tax_rate_id ?? '', warehouse_id: l.warehouse_id ?? '',
        })),
      };
      const res = o
        ? await api<OrderDetail>(`/companies/${company.id}/sales/orders/${o.id}`, { method: 'PUT', body })
        : await api<OrderDetail>(`/companies/${company.id}/sales/orders`, { method: 'POST', body });
      notifySaved(t);
      onDone(res.order.id);
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened onClose={onClose} size="75rem" title={o ? `${t('common.edit')} · Q-${o.number}` : t('sales.newQuote')}>
      <Stack>
        <SimpleGrid cols={{ base: 1, sm: 4 }}>
          <CustomerPicker value={customerId} onChange={(v) => setCustomerId(v)}
            initial={o ? { id: o.customer_id, name: o.customer_name, code: o.customer_code } : customer} />
          <Select label={t('pur.currency')} searchable allowDeselect={false} data={currencies.map((c) => c.code)} value={currency}
            onChange={(v) => v && setCurrency(v)} />
          <TextInput type="date" label={t('sales.quoteDate')} value={date} onChange={(e) => setDate(e.currentTarget.value)} />
          <TextInput type="date" label={t('sales.validUntil')} value={validUntil} onChange={(e) => setValidUntil(e.currentTarget.value)}
            description={!validUntil ? t('sales.validDefault') : undefined} />
        </SimpleGrid>
        {tpls.length > 0 && (
          <Select size="xs" maw={360} clearable placeholder={t('gp.qt.apply')} data={tpls.map((x) => ({ value: x.id, label: x.name }))}
            value={null} onChange={(v) => applyTemplate(v)} />
        )}
        <Table.ScrollContainer minWidth={980}>
          <Table withTableBorder verticalSpacing={4}>
            <Table.Thead><Table.Tr>
              <Table.Th miw={220}>{t('sales.product')}</Table.Th><Table.Th miw={200}>{t('invoices.description')}</Table.Th>
              <Table.Th w={90}>{t('invoices.qty')}</Table.Th><Table.Th w={120}>{t('invoices.price')}</Table.Th>
              <Table.Th w={80}>{t('sales.discount')}</Table.Th><Table.Th w={140}>{t('net.tax')}</Table.Th>
              <Table.Th w={110} style={{ textAlign: 'end' }}>{t('pur.amount')}</Table.Th><Table.Th w={36} />
            </Table.Tr></Table.Thead>
            <Table.Tbody>
              {lines.map((l) => {
                const it = l.item_id ? byId.get(l.item_id) : undefined;
                return (
                  <Table.Tr key={l.key}>
                    <Table.Td>
                      <Stack gap={2}>
                        <Select size="xs" searchable clearable placeholder={t('sales.freeLine')} value={l.item_id} comboboxProps={{ withinPortal: true }}
                          data={items.map((i) => ({ value: i.id, label: `${i.code} · ${i.name}${i.kind === 'stock' ? ` (${Number(i.quantity)} ${i.unit})` : ''}` }))}
                          onChange={(v) => pickItem(l.key, v)} />
                        {it?.kind === 'stock' && warehouses.length > 1 && (
                          <Select size="xs" placeholder={warehouses[0]?.name} value={l.warehouse_id} comboboxProps={{ withinPortal: true }}
                            data={warehouses.map((w) => ({ value: w.id, label: w.name }))} onChange={(v) => upd(l.key, { warehouse_id: v })} />
                        )}
                      </Stack>
                    </Table.Td>
                    <Table.Td><Textarea size="xs" autosize minRows={1} value={l.description} onChange={(e) => upd(l.key, { description: e.currentTarget.value })} /></Table.Td>
                    <Table.Td><TextInput size="xs" dir="ltr" value={l.quantity} rightSection={<Text size="xs" c="dimmed">{it?.unit}</Text>}
                      onChange={(e) => upd(l.key, { quantity: e.currentTarget.value.replace(/[^\d.]/g, '') })} /></Table.Td>
                    <Table.Td><TextInput size="xs" dir="ltr" value={l.unit_price} onChange={(e) => upd(l.key, { unit_price: e.currentTarget.value.replace(/[^\d.]/g, '') })} /></Table.Td>
                    <Table.Td><TextInput size="xs" dir="ltr" value={l.discount_pct} rightSection={<Text size="xs" c="dimmed">%</Text>}
                      onChange={(e) => upd(l.key, { discount_pct: e.currentTarget.value.replace(/[^\d.]/g, '') })} /></Table.Td>
                    <Table.Td><Select size="xs" clearable placeholder={t('net.noTax')} value={l.tax_rate_id} comboboxProps={{ withinPortal: true }}
                      data={taxes.map((x) => ({ value: x.id, label: x.name }))} onChange={(v) => upd(l.key, { tax_rate_id: v })} /></Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(lineAmt(l), dec)}</Table.Td>
                    <Table.Td><ActionIcon variant="subtle" color="gray" disabled={lines.length === 1} onClick={() => setLines((ls) => ls.filter((x) => x.key !== l.key))}><IconTrash size={14} /></ActionIcon></Table.Td>
                  </Table.Tr>
                );
              })}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
        <Group justify="space-between" align="flex-start">
          <Group gap="xs">
            <Button variant="subtle" size="xs" leftSection={<IconPlus size={14} />} onClick={() => setLines((ls) => [...ls, blankLine()])}>{t('invoices.addLine')}</Button>
            <Button variant="light" color="pink" size="xs" disabled={!lines.some((l) => l.item_id)} onClick={() => void applyPromos()}>{t('rt.p.applyDoc')}</Button>
          </Group>
          <Stack gap={0} align="flex-end">
            <Text size="sm" dir="ltr">{t('sales.subtotal')}: {formatAmount(sub, dec)}</Text>
            {tax > 0 && <Text size="sm" dir="ltr">{t('net.tax')}: {formatAmount(tax, dec)}</Text>}
            <Text fw={700} size="lg" dir="ltr">{formatAmount(sub + tax, dec)} {currency}</Text>
          </Stack>
        </Group>
        <SimpleGrid cols={{ base: 1, sm: 2 }}>
          <Textarea label={t('sales.notes')} autosize minRows={2} value={notes} onChange={(e) => setNotes(e.currentTarget.value)} />
          <Textarea label={t('sales.terms')} autosize minRows={2} value={terms ?? ''} placeholder={t('sales.termsDefault')}
            onChange={(e) => setTerms(e.currentTarget.value)} />
        </SimpleGrid>
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
          <Button loading={busy} disabled={!customerId || sub + tax <= 0} onClick={save}>{t('common.save')}</Button>
        </Group>
      </Stack>
    </Modal>
  );
}

// ---------- view / print / workflow ----------

export function OrderView({ id, me, onClose, onChanged }: { id: string; me: Me; onClose: () => void; onChanged?: () => void }) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [d, setD] = useState<OrderDetail | null>(null);
  const [busy, setBusy] = useState(false);
  const [editing, setEditing] = useState(false);
  const [step, setStep] = useState<null | 'deliver' | 'invoice' | 'cancel'>(null);
  const [invoice, setInvoice] = useState<string | null>(null);
  const [proforma, setProforma] = useState(false);
  const [note, setNote] = useState<string | null>(null);
  const canEdit = can(me, 'sales.create');
  const canInvoice = can(me, 'billing.create');
  // pro-forma: the same document titled "Pro-forma invoice" (no accounting), printed then back to normal
  function printProforma() {
    setProforma(true);
    window.setTimeout(() => { void printPage(); window.setTimeout(() => setProforma(false), 500); }, 50);
  }

  const reload = useCallback(() => {
    if (company) api<OrderDetail>(`/companies/${company.id}/sales/orders/${id}`).then(setD).catch((e) => { notifyError(t, e); onClose(); });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [company, id]);
  useEffect(reload, [reload]);

  async function act(action: string, body: Record<string, string> = {}) {
    if (!company) return;
    setBusy(true);
    try {
      setD(await api<OrderDetail>(`/companies/${company.id}/sales/orders/${id}/${action}`, { method: 'POST', body }));
      notifySaved(t);
      setStep(null);
      onChanged?.();
    } catch (e) {
      // audit P7-L4: an order with a deposit: keep it as the customer's credit (refund it from the customer's account)
      if (action === 'cancel' && e instanceof ApiError && e.code === 'order_has_deposit') {
        setBusy(false);
        if (window.confirm(t('sales.depositKeepQ', { detail: e.detail ?? '' }))) return act('cancel', { deposit: 'credit' });
        return;
      }
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  async function voidDelivery(did: string) {
    if (!company) return;
    try {
      setD(await api<OrderDetail>(`/companies/${company.id}/sales/deliveries/${did}/void`, { method: 'POST', body: {} }));
      notifySaved(t);
      onChanged?.();
    } catch (e) {
      notifyError(t, e);
    }
  }

  const o = d?.order;
  const dec = o ? decimalsOf(o.currency) : 2;
  const rtl = i18n.language === 'ar';
  const isOrder = o && (o.status === 'confirmed' || o.status === 'done');
  const hasStock = d?.lines.some((l) => l.item_kind === 'stock');
  const leftToDeliver = d?.lines.some((l) => l.item_kind === 'stock' && Number(l.qty_delivered) < Number(l.quantity));
  const leftToInvoice = d?.lines.some((l) => Number(l.qty_invoiced) < Number(l.quantity));
  const docType = isOrder ? 'order' : 'quote';
  const lay = useDocLayout(docType);
  const t2 = lay.bilingual && lay.bilingual !== i18n.language ? i18n.getFixedT(lay.bilingual) : null;

  return (
    <Modal opened onClose={onClose} size="xl" title={o ? `${isOrder ? t('sales.order') : t('sales.quote')} ${docNumber(o)}` : t('sales.quote')}>
      {!d || !o ? <Center p="xl"><Loader size="sm" /></Center> : (
        <Stack>
          <Group gap="xs" className="no-print">
            <Badge variant="light" color={orderColor[o.status]}>{t(`sales.st.${o.status}`)}</Badge>
            {o.accepted_at && <Badge variant="outline" color="teal">{t('sales.acceptedBy', { name: o.accepted_name, date: formatDate(o.accepted_at) })}</Badge>}
            {isOrder && hasStock && <Badge variant="dot">{t(`sales.delivery.${o.delivery_status ?? 'none'}`)}</Badge>}
            {isOrder && <Badge variant="dot">{t(`sales.invoicing.${o.invoice_status ?? 'none'}`)}</Badge>}
          </Group>
          <div className={note ? 'no-print' : 'print-doc'} data-doc={docType} dir={rtl ? 'rtl' : 'ltr'}>
            <LayoutStyle type={docType} layout={lay} />
            <DocHeader c={o} layout={lay} title2={t2 ? (isOrder ? t2('sales.order') : t2('sales.quote')) : undefined} title={proforma ? t('gp.pf.title') : isOrder ? t('sales.order') : t('sales.quote')} lines={[
              <span dir="ltr" key="n">{proforma ? `PF-${o.order_number ?? o.number}` : docNumber(o)}</span>,
              ...(proforma ? [t('gp.pf.note')] : []),
              `${t('invoices.date')}: ${formatDate(o.order_date ?? o.quote_date)}`,
              ...(o.valid_until && !isOrder ? [`${t('sales.validUntil')}: ${formatDate(o.valid_until)}`] : []),
            ]} />
            <Divider my="sm" />
            <Text fw={600}>{o.customer_name} <Text span c="dimmed" size="sm">({o.customer_code})</Text></Text>
            <Text size="sm"><span className="f-customer_phone">{[o.customer_phone, o.customer_email].filter(Boolean).join(' · ')}</span>{o.customer_address ? <span className="f-customer_address"> · {o.customer_address}</span> : null}</Text>
            <table style={{ marginTop: 12 }}>
              <thead><tr>
                <th>{t('invoices.description')}</th><th className="num">{t('invoices.qty')}</th><th className="num">{t('invoices.price')}</th>
                {d.lines.some((l) => Number(l.discount_pct)) && <th className="num">{t('sales.discount')}</th>}
                <th className="num">{t('pur.amount')}</th>
              </tr></thead>
              <tbody>
                {d.lines.map((l) => (
                  <tr key={l.id}>
                    <td style={{ whiteSpace: 'pre-wrap' }}>{l.description}
                      {isOrder && <div className="no-print" style={{ fontSize: 11, color: '#666' }}>
                        {l.item_kind === 'stock' && <>{t('sales.delivered')}: {Number(l.qty_delivered)} · </>}{t('sales.invoiced')}: {Number(l.qty_invoiced)}
                      </div>}
                    </td>
                    <td className="num">{Number(l.quantity)} {l.unit ?? ''}</td>
                    <td className="num">{formatAmount(l.unit_price, dec)}</td>
                    {d.lines.some((x) => Number(x.discount_pct)) && <td className="num">{Number(l.discount_pct) ? `${Number(l.discount_pct)}%` : ''}</td>}
                    <td className="num">{formatAmount(l.amount, dec)}
                      {Number(l.tax_amount) > 0 && <div style={{ fontSize: 11 }}>+ {l.tax_name}: {formatAmount(l.tax_amount, dec)}</div>}</td>
                  </tr>
                ))}
              </tbody>
              <tfoot>
                {Number(o.tax_total) > 0 && <tr><td colSpan={d.lines.some((l) => Number(l.discount_pct)) ? 4 : 3}>{t('sales.subtotal')}</td><td className="num">{formatAmount(o.subtotal, dec)}</td></tr>}
                {Number(o.tax_total) > 0 && <tr><td colSpan={d.lines.some((l) => Number(l.discount_pct)) ? 4 : 3}>{t('net.tax')}</td><td className="num">{formatAmount(o.tax_total, dec)}</td></tr>}
                <tr><th colSpan={d.lines.some((l) => Number(l.discount_pct)) ? 4 : 3}>{t('sales.total')}</th><th className="num">{formatAmount(o.total, dec)} {o.currency}</th></tr>
              </tfoot>
            </table>
            {o.notes && <Text size="sm" mt="sm" style={{ whiteSpace: 'pre-wrap' }}>{o.notes}</Text>}
            <DocFooter c={o} terms={o.terms} layout={lay} qr={`${o.customer_name} ${docNumber(o)} ${o.total} ${o.currency}`} />
          </div>

          {(d.deliveries.length > 0 || d.invoices.length > 0) && (
            <SimpleGrid cols={{ base: 1, sm: 2 }} className="no-print">
              {d.deliveries.length > 0 && (
                <Paper withBorder p="sm">
                  <Text size="sm" fw={600} mb={4}>{t('sales.deliveries')}</Text>
                  {d.deliveries.map((x) => (
                    <Group key={x.id} justify="space-between" gap="xs">
                      <Text size="sm" td={x.status === 'void' ? 'line-through' : undefined}>
                        DEL-{x.number} · {formatDate(x.delivery_date)} · {x.lines.map((l) => `${Number(l.quantity)}× ${l.description}`).join(', ')}
                      </Text>
                      {x.status !== 'void' && <Button size="compact-xs" variant="subtle" onClick={() => setNote(x.id)}>{t('op.dn.print')}</Button>}
                      {x.status !== 'void' && canInvoice && <Button size="compact-xs" variant="subtle" color="red" onClick={() => void voidDelivery(x.id)}>{t('gap.void')}</Button>}
                    </Group>
                  ))}
                </Paper>
              )}
              {d.invoices.length > 0 && (
                <Paper withBorder p="sm">
                  <Text size="sm" fw={600} mb={4}>{t('bill.invoices')}</Text>
                  {d.invoices.map((x) => (
                    <Text key={x.id} size="sm" style={{ cursor: 'pointer' }} c={x.status === 'void' ? 'dimmed' : 'teal.8'} onClick={() => setInvoice(x.id)}
                      td={x.status === 'void' ? 'line-through' : undefined}>
                      #{x.number} · {formatDate(x.issue_date)} · <span dir="ltr">{formatAmount(x.total, dec)}</span> · {t(`invoices.statuses.${x.status}`)}
                    </Text>
                  ))}
                </Paper>
              )}
            </SimpleGrid>
          )}

          {note && <DeliveryNote d={d} deliveryId={note} onClose={() => setNote(null)} />}
          {step === 'cancel' && <Confirm text={t('sales.cancelConfirm')} yesLabel={t('sales.cancelOrder')} noLabel={t('common.close')} busy={busy}
            onYes={() => void act('cancel')} onNo={() => setStep(null)} />}
          {(step === 'deliver' || step === 'invoice') && (
            <QtyStep mode={step} d={d} busy={busy} setBusy={setBusy} onCancel={() => setStep(null)}
              onDone={(next, invoiceId) => { setD(next ?? d); setStep(null); reload(); onChanged?.(); if (invoiceId) setInvoice(invoiceId); }} />
          )}

          <Group justify="space-between" className="no-print">
            <Group gap="xs">
              {canEdit && (o.status === 'draft' || o.status === 'sent') && <>
                <Button variant="default" leftSection={<IconPencil size={16} />} onClick={() => setEditing(true)}>{t('common.edit')}</Button>
                <Button color="teal" leftSection={<IconCheck size={16} />} loading={busy} onClick={() => void act('confirm')}>{t('sales.confirm')}</Button>
              </>}
              {canEdit && o.status === 'confirmed' && leftToDeliver && !step &&
                <Button variant="light" leftSection={<IconTruckDelivery size={16} />} onClick={() => setStep('deliver')}>{t('sales.deliver')}</Button>}
              {canInvoice && o.status === 'confirmed' && leftToInvoice && !step &&
                <Button variant="light" leftSection={<IconFileInvoice size={16} />} onClick={() => setStep('invoice')}>{t('sales.createInvoice')}</Button>}
              {canEdit && o.status !== 'cancelled' && o.status !== 'done' && !step &&
                <Button variant="subtle" color="red" leftSection={<IconX size={16} />} onClick={() => setStep('cancel')}>{t('sales.cancelOrder')}</Button>}
              {canEdit && o.status === 'cancelled' && <Button variant="subtle" onClick={() => void act('reopen')}>{t('sales.reopen')}</Button>}
            </Group>
            <Group gap="xs">
              {o.status !== 'cancelled' && <SendDocButton kind={isOrder ? 'order' : 'quote'} id={o.id} email={o.customer_email} onSent={reload} />}
              <Button variant="default" onClick={onClose}>{t('common.close')}</Button>
              {o.status !== 'cancelled' && <Button variant="light" leftSection={<IconPrinter size={16} />} onClick={printProforma}>{t('gp.pf.print')}</Button>}
              <Button leftSection={<IconPrinter size={16} />} onClick={() => void printPage()}>{t('invoices.print')}</Button>
            </Group>
          </Group>
          <Chatter type="sales_order" id={o.id} />
          {editing && <OrderEditor detail={d} onClose={() => setEditing(false)} onDone={() => { setEditing(false); reload(); onChanged?.(); }} />}
          {invoice && <InvoiceView id={invoice} canVoid={canInvoice} onClose={() => setInvoice(null)} onChanged={() => { reload(); onChanged?.(); }} />}
        </Stack>
      )}
    </Modal>
  );
}

function QtyStep({ mode, d, busy, setBusy, onCancel, onDone }: {
  mode: 'deliver' | 'invoice'; d: OrderDetail; busy: boolean; setBusy: (b: boolean) => void; onCancel: () => void;
  onDone: (d: OrderDetail | null, invoiceId?: string) => void;
}) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const lines = d.lines.filter((l) => (mode === 'deliver' ? l.item_kind === 'stock' : true));
  const left = (l: OrderLine) => Number(l.quantity) - Number(mode === 'deliver' ? l.qty_delivered : l.qty_invoiced);
  const [qty, setQty] = useState<Record<string, number | string>>(Object.fromEntries(lines.map((l) => [l.id, left(l)])));
  const [date, setDate] = useState(today());

  async function run() {
    if (!company) return;
    setBusy(true);
    try {
      const body = { date, lines: lines.filter((l) => Number(qty[l.id] || 0) > 0).map((l) => ({ line_id: l.id, quantity: String(qty[l.id]) })) };
      const path = `/companies/${company.id}/sales/orders/${d.order.id}/${mode}`;
      if (mode === 'deliver') onDone(await api<OrderDetail>(path, { method: 'POST', body }));
      else {
        const r = await api<{ invoice_id: string }>(path, { method: 'POST', body });
        onDone(null, r.invoice_id);
      }
      notifySaved(t);
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Paper withBorder p="sm" className="no-print">
      <Stack gap="xs">
        <Group justify="space-between">
          <Text fw={600} size="sm">{mode === 'deliver' ? t('sales.deliver') : t('sales.createInvoice')}</Text>
          <TextInput size="xs" type="date" value={date} onChange={(e) => setDate(e.currentTarget.value)} />
        </Group>
        {mode === 'deliver' && <Text size="xs" c="dimmed">{t('sales.deliverHint')}</Text>}
        {mode === 'invoice' && <Text size="xs" c="dimmed">{t('sales.invoiceHint')}</Text>}
        <Table verticalSpacing={4}>
          <Table.Tbody>
            {lines.map((l) => (
              <Table.Tr key={l.id}>
                <Table.Td><Text size="sm">{l.description}</Text>
                  <Text size="xs" c="dimmed">{t('sales.remaining')}: {left(l)} {l.unit ?? ''}{l.warehouse_name ? ` · ${l.warehouse_name}` : ''}</Text></Table.Td>
                <Table.Td w={140}><NumberInput size="xs" min={0} max={left(l)} decimalScale={4} value={qty[l.id]}
                  onChange={(v) => setQty((q) => ({ ...q, [l.id]: v }))} /></Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
        <Group justify="flex-end">
          <Button size="xs" variant="default" onClick={onCancel}>{t('common.cancel')}</Button>
          <Button size="xs" loading={busy} onClick={() => void run()}>{mode === 'deliver' ? t('sales.deliver') : t('sales.createInvoice')}</Button>
        </Group>
      </Stack>
    </Paper>
  );
}

// ---------- report ----------

export function SalesReport() {
  const { t } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [from, setFrom] = useState(today().slice(0, 8) + '01');
  const [to, setTo] = useState(today());
  const [d, setD] = useState<{
    salespeople: { salesperson: string; currency: string; quotes: number; orders: number; ordered: string; open_quotes: string }[];
    products: { code: string; name: string; currency: string; quantity: string; amount: string; delivered: string; invoiced: string }[];
  } | null>(null);
  useEffect(() => {
    if (!company) return;
    setD(null);
    api<typeof d>(`/companies/${company.id}/reports/sales?from=${from}&to=${to}`).then(setD).catch((e) => notifyError(t, e));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [company, from, to]);
  if (!company) return null;
  return (
    <Stack gap="lg" maw={1100} className="print-area">
      <PageHeader title={t('sales.report')} subtitle={company.display ?? company.name_en}>
        <ExportMenu disabled={!d} table={() => d && ({
          title: t('sales.report'), subtitle: `${from} – ${to}`,
          columns: [{ label: t('sales.product') }, { label: t('pur.currency') }, { label: t('invoices.qty'), numeric: true },
            { label: t('pur.amount'), numeric: true }, { label: t('sales.delivered'), numeric: true }, { label: t('sales.invoiced'), numeric: true }],
          rows: d.products.map((p) => [p.code ? `${p.code} · ${p.name}` : p.name, p.currency, p.quantity, p.amount, p.delivered, p.invoiced]),
        })} />
      </PageHeader>
      <Group className="no-print">
        <TextInput type="date" label={t('reports.from')} value={from} onChange={(e) => setFrom(e.currentTarget.value)} />
        <TextInput type="date" label={t('reports.to')} value={to} onChange={(e) => setTo(e.currentTarget.value)} />
      </Group>
      {!d ? <Center p="xl"><Loader size="sm" /></Center> : <>
        <Paper withBorder>
          <Text fw={600} p="sm">{t('sales.bySalesperson')}</Text>
          {d.salespeople.length === 0 ? <Text c="dimmed" p="md">{t('common.empty')}</Text> : (
            <Table>
              <Table.Thead><Table.Tr><Table.Th>{t('sales.salesperson')}</Table.Th><Table.Th>{t('sales.quotes')}</Table.Th><Table.Th>{t('sales.orders')}</Table.Th>
                <Table.Th style={{ textAlign: 'end' }}>{t('sales.ordered')}</Table.Th><Table.Th style={{ textAlign: 'end' }}>{t('sales.openQuotes')}</Table.Th></Table.Tr></Table.Thead>
              <Table.Tbody>
                {d.salespeople.map((s, i) => (
                  <Table.Tr key={i}><Table.Td>{s.salesperson}</Table.Td><Table.Td>{s.quotes}</Table.Td><Table.Td>{s.orders}</Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(s.ordered, decimalsOf(s.currency))} {s.currency}</Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(s.open_quotes, decimalsOf(s.currency))} {s.currency}</Table.Td></Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          )}
        </Paper>
        <Paper withBorder>
          <Text fw={600} p="sm">{t('sales.byProduct')}</Text>
          {d.products.length === 0 ? <Text c="dimmed" p="md">{t('common.empty')}</Text> : (
            <Table.ScrollContainer minWidth={700}>
              <Table>
                <Table.Thead><Table.Tr><Table.Th>{t('sales.product')}</Table.Th><Table.Th style={{ textAlign: 'end' }}>{t('invoices.qty')}</Table.Th>
                  <Table.Th style={{ textAlign: 'end' }}>{t('pur.amount')}</Table.Th><Table.Th style={{ textAlign: 'end' }}>{t('sales.delivered')}</Table.Th>
                  <Table.Th style={{ textAlign: 'end' }}>{t('sales.invoiced')}</Table.Th></Table.Tr></Table.Thead>
                <Table.Tbody>
                  {d.products.map((p, i) => (
                    <Table.Tr key={i}><Table.Td>{p.code && <Text span c="dimmed" ff="monospace">{p.code} </Text>}{p.name}</Table.Td>
                      <Table.Td ff="monospace" style={{ textAlign: 'end' }}>{Number(p.quantity)}</Table.Td>
                      <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(p.amount, decimalsOf(p.currency))} {p.currency}</Table.Td>
                      <Table.Td ff="monospace" style={{ textAlign: 'end' }}>{Number(p.delivered)}</Table.Td>
                      <Table.Td ff="monospace" style={{ textAlign: 'end' }}>{Number(p.invoiced)}</Table.Td></Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </Table.ScrollContainer>
          )}
        </Paper>
      </>}
      {d && d.products.length === 0 && d.salespeople.length === 0 && <Alert color="gray" variant="light">{t('sales.reportEmpty')}</Alert>}
    </Stack>
  );
}

/** Phase 9: the delivery note of one delivery (what left, to whom), in the company's "delivery" print layout, with
 *  places for the driver's and the receiver's signatures. */
function DeliveryNote({ d, deliveryId, onClose }: { d: OrderDetail; deliveryId: string; onClose: () => void }) {
  const { t, i18n } = useTranslation();
  const lay = useDocLayout('delivery');
  const t2 = lay.bilingual && lay.bilingual !== i18n.language ? i18n.getFixedT(lay.bilingual) : null;
  const x = d.deliveries.find((v) => v.id === deliveryId);
  const o = d.order;
  if (!x) return null;
  return (
    <Modal opened onClose={onClose} size="lg" title={`DEL-${x.number}`}>
      <div className="print-doc" data-doc="delivery" dir={i18n.language === 'ar' ? 'rtl' : 'ltr'}>
        <LayoutStyle type="delivery" layout={lay} />
        <DocHeader c={o} layout={lay} title={t('op.dn.title')} title2={t2 ? t2('op.dn.title') : undefined} lines={[
          <span dir="ltr" key="n">DEL-{x.number}</span>,
          `${t('invoices.date')}: ${formatDate(x.delivery_date)}`,
          `${t('sales.order')}: ${docNumber(o)}`,
        ]} />
        <Divider my="sm" />
        <Text fw={600}>{o.customer_name} <Text span c="dimmed" size="sm">({o.customer_code})</Text></Text>
        <Text size="sm"><span className="f-customer_phone">{o.customer_phone}</span>{o.customer_address ? <span className="f-customer_address"> · {o.customer_address}</span> : null}</Text>
        <table style={{ marginTop: 12 }}>
          <thead><tr><th>#</th><th>{t('invoices.description')}</th><th className="num">{t('invoices.qty')}</th></tr></thead>
          <tbody>{x.lines.map((l, i) => <tr key={l.line_id + i}><td>{i + 1}</td><td style={{ whiteSpace: 'pre-wrap' }}>{l.description}</td><td className="num">{Number(l.quantity)}</td></tr>)}</tbody>
        </table>
        <div style={{ display: 'flex', justifyContent: 'space-between', gap: 24, marginTop: 40 }}>
          <div style={{ flex: 1, borderTop: '1px solid #333', paddingTop: 4, fontSize: 12 }}>{t('op.dn.driver')}</div>
          <div style={{ flex: 1, borderTop: '1px solid #333', paddingTop: 4, fontSize: 12 }}>{t('op.dn.receiver')}</div>
        </div>
        <DocFooter c={o} layout={lay} qr={`DEL-${x.number} ${o.customer_name}`} />
      </div>
      <Group justify="flex-end" mt="md" className="no-print">
        <Button variant="default" onClick={onClose}>{t('common.close')}</Button>
        <Button leftSection={<IconPrinter size={16} />} onClick={() => void printPage()}>{t('invoices.print')}</Button>
      </Group>
    </Modal>
  );
}