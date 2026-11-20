import { printPage } from '../../native';
import { useCallback, useEffect, useState } from 'react';
import {
  ActionIcon, Badge, Button, Center, Divider, Group, Loader, Modal, Paper, SegmentedControl, Select, SimpleGrid, Stack, Table, Text, TextInput,
} from '@mantine/core';
import { useDebouncedValue } from '@mantine/hooks';
import { IconPlus, IconPrinter, IconSearch, IconTrash } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { can, api, ApiError, type Account, type Me } from '../../api';
import Chatter from '../../components/Chatter';
import { useCompany } from '../../company';
import { accountLabel, formatAmount, formatDate, notifyError, notifySaved, today } from '../../util';
import PageHeader, { Confirm } from '../../components/PageHeader';
import { billColor, type Bill, type BillDetail, type Item, type Supplier, type Warehouse } from './types';
import { BillsTable } from './Suppliers';
import { ccLabel, useCostCenters } from '../acc/common';

interface Totals { currency: string; count: number; total: string; paid: string; balance: string }

export default function Bills({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [status, setStatus] = useState('');
  const [q, setQ] = useState('');
  const [dq] = useDebouncedValue(q, 250);
  const [data, setData] = useState<{ rows: Bill[]; totals: Totals[] } | null>(null);
  const [view, setView] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const canManage = can(me, 'purchasing.create');

  const load = useCallback(() => {
    if (!company) return;
    setData(null);
    api<{ rows: Bill[]; totals: Totals[] }>(`/companies/${company.id}/bills?status=${status}&q=${encodeURIComponent(dq)}`)
      .then(setData).catch((e) => notifyError(t, e));
  }, [company, status, dq, t]);
  useEffect(load, [load]);
  if (!company) return null;

  return (
    <Stack gap="lg" maw={1200}>
      <PageHeader title={t('pur.bills')} subtitle={company.display ?? company.name_en}>
        {canManage && <Button leftSection={<IconPlus size={16} />} onClick={() => setCreating(true)}>{t('pur.newBill')}</Button>}
      </PageHeader>
      <Group gap="sm" wrap="wrap">
        <SegmentedControl value={status} onChange={setStatus} data={[
          { value: '', label: t('common.all') }, { value: 'unpaid', label: t('invoices.unpaid') },
          { value: 'paid', label: t('invoices.statuses.paid') }, { value: 'void', label: t('invoices.statuses.void') },
        ]} />
        <TextInput leftSection={<IconSearch size={16} />} placeholder={t('common.search')} value={q} onChange={(e) => setQ(e.currentTarget.value)} w={240} />
      </Group>
      {data && data.totals.length > 0 && (
        <SimpleGrid cols={{ base: 1, sm: 3 }}>
          {data.totals.map((x) => (
            <Paper withBorder p="md" key={x.currency}>
              <Text size="xs" c="dimmed">{x.count} · {x.currency}</Text>
              <Group justify="space-between" mt={4}>
                <div><Text size="xs" c="dimmed">{t('pur.total')}</Text><Text fw={600} dir="ltr">{formatAmount(x.total, decimalsOf(x.currency))}</Text></div>
                <div><Text size="xs" c="dimmed">{t('invoices.paid')}</Text><Text fw={600} c="teal.7" dir="ltr">{formatAmount(x.paid, decimalsOf(x.currency))}</Text></div>
                <div><Text size="xs" c="dimmed">{t('invoices.balance')}</Text><Text fw={600} c="red.7" dir="ltr">{formatAmount(x.balance, decimalsOf(x.currency))}</Text></div>
              </Group>
            </Paper>
          ))}
        </SimpleGrid>
      )}
      <Paper withBorder>
        {data === null ? <Center p="xl"><Loader size="sm" /></Center> : <BillsTable rows={data.rows} onOpen={setView} lang={i18n.language} />}
      </Paper>
      {creating && <NewBill onClose={() => setCreating(false)} onDone={(id) => { setCreating(false); load(); setView(id); }} />}
      {view && <BillView id={view} canVoid={canManage} onClose={() => setView(null)} onChanged={load} />}
    </Stack>
  );
}

export function BillView({ id, canVoid, onClose, onChanged }: { id: string; canVoid: boolean; onClose: () => void; onChanged?: () => void }) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [d, setD] = useState<BillDetail | null>(null);
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const [debit, setDebit] = useState<{ amount: string; reason: string } | null>(null);

  useEffect(() => {
    if (!company) return;
    api<BillDetail>(`/companies/${company.id}/bills/${id}`).then(setD).catch((e) => { notifyError(t, e); onClose(); });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [company, id]);

  async function doDebit() {
    if (!company || !debit) return;
    setBusy(true);
    try {
      setD(await api<BillDetail>(`/companies/${company.id}/bills/${id}/debit-note`, { method: 'POST', body: debit }));
      notifySaved(t);
      setDebit(null);
      onChanged?.();
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  async function doVoid() {
    if (!company) return;
    setBusy(true);
    try {
      setD(await api<BillDetail>(`/companies/${company.id}/bills/${id}/void`, { method: 'POST', body: { reason: '' } }));
      notifySaved(t);
      setConfirm(false);
      onChanged?.();
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  const b = d?.bill;
  const dec = b ? decimalsOf(b.currency) : 2;
  const rtl = i18n.language === 'ar';
  return (
    <Modal opened onClose={onClose} size="lg" title={b ? `${t('pur.bill')} #${b.number}` : t('pur.bill')}>
      {!d || !b ? <Center p="xl"><Loader size="sm" /></Center> : (
        <Stack>
          <div className="print-doc" dir={rtl ? 'rtl' : 'ltr'}>
            <Group justify="space-between" align="flex-start">
              <div>
                <Text fw={700} size="lg">{rtl && b.company_name_ar ? b.company_name_ar : b.company_name}</Text>
                <Text size="sm">{t('pur.bill')} #{b.number}{b.supplier_ref ? ` · ${b.supplier_ref}` : ''}</Text>
              </div>
              <div style={{ textAlign: 'end' }}>
                <Text size="sm">{t('pur.billDate')}: {formatDate(b.bill_date)}</Text>
                <Text size="sm">{t('invoices.due')}: {formatDate(b.due_date)}</Text>
                <Badge className="no-print" variant="light" color={billColor[b.status]}>{t(`invoices.statuses.${b.status}`)}</Badge>
              </div>
            </Group>
            <Divider my="sm" />
            <Text fw={600}>{b.supplier_name} <Text span c="dimmed" size="sm">({b.supplier_code})</Text></Text>
            <table style={{ marginTop: 12 }}>
              <thead><tr><th>{t('invoices.description')}</th><th className="num">{t('pur.qty')}</th><th className="num">{t('pur.unitPrice')}</th><th className="num">{t('pur.amount')}</th></tr></thead>
              <tbody>
                {d.lines.map((l) => (
                  <tr key={l.line_no}>
                    <td>{l.description}{l.item_code ? '' : <Text span size="xs" c="dimmed"> ({l.account_code})</Text>}</td>
                    <td className="num">{Number(l.quantity)} {l.unit ?? ''}</td>
                    <td className="num">{formatAmount(l.unit_price, 4).replace(/(\.\d*?)0+$/, '$1').replace(/\.$/, '')}</td>
                    <td className="num">{formatAmount(l.amount, dec)}</td>
                  </tr>
                ))}
              </tbody>
              <tfoot>
                <tr><th colSpan={3}>{t('pur.total')}</th><th className="num">{formatAmount(b.total, dec)} {b.currency}</th></tr>
                {b.paid !== '0.0000' && <tr><td colSpan={3}>{t('invoices.paid')}</td><td className="num">{formatAmount(b.paid, dec)}</td></tr>}
                {b.status !== 'void' && <tr><th colSpan={3}>{t('invoices.balance')}</th><th className="num">{formatAmount(b.balance, dec)} {b.currency}</th></tr>}
              </tfoot>
            </table>
          </div>
          {d.payments.length > 0 && (
            <Paper withBorder p="sm" className="no-print">
              {d.payments.map((p, i) => <Text size="sm" key={i}>#{p.payment_number} · {formatDate(p.payment_date)} · <span dir="ltr">{formatAmount(p.amount, dec)}</span></Text>)}
            </Paper>
          )}
          <Chatter type="bill" id={b.id} />
          {confirm && <Confirm text={t('pur.voidBillConfirm')} yesLabel={t('pur.voidBill')} noLabel={t('common.cancel')} busy={busy} onYes={doVoid} onNo={() => setConfirm(false)} />}
          {debit && (
            <Paper withBorder p="sm" className="no-print">
              <Stack gap="xs">
                <Text fw={600} size="sm">{t('gap.debitNoteNew')}</Text>
                <Text size="xs" c="dimmed">{t('gap.debitHint')}</Text>
                <Group grow>
                  <TextInput label={t('gap.creditAmount')} dir="ltr" placeholder={formatAmount(b.total, dec)} value={debit.amount}
                    rightSection={<Text size="xs" c="dimmed">{b.currency}</Text>}
                    onChange={(e) => setDebit({ ...debit, amount: e.currentTarget.value.replace(/[^\d.]/g, '') })} />
                  <TextInput label={t('invoices.reason')} value={debit.reason} onChange={(e) => setDebit({ ...debit, reason: e.currentTarget.value })} />
                </Group>
                <Group justify="flex-end">
                  <Button variant="default" size="xs" onClick={() => setDebit(null)}>{t('common.cancel')}</Button>
                  <Button size="xs" color="orange" loading={busy} onClick={doDebit}>{t('gap.debitNote')}</Button>
                </Group>
              </Stack>
            </Paper>
          )}
          <Group justify="space-between" className="no-print">
            <Group gap="xs">
              {canVoid && b.status !== 'void' && b.paid === '0.0000' && !confirm
                ? <Button variant="subtle" color="red" onClick={() => setConfirm(true)}>{t('pur.voidBill')}</Button> : null}
              {canVoid && b.status !== 'void' && !debit && !confirm
                ? <Button variant="subtle" color="orange" onClick={() => setDebit({ amount: '', reason: '' })}>{t('gap.debitNote')}</Button> : null}
            </Group>
            <Group gap="xs">
              <Button variant="default" onClick={onClose}>{t('common.close')}</Button>
              <Button leftSection={<IconPrinter size={16} />} onClick={() => void printPage()}>{t('invoices.print')}</Button>
            </Group>
          </Group>
        </Stack>
      )}
    </Modal>
  );
}

interface LineState { key: number; item_id: string | null; account_id: string | null; warehouse_id: string | null; description: string; quantity: string; unit_price: string; tax_rate_id: string | null; unit_id: string | null; lots: string }
interface ItemExtras { item: { tracking: string; unit: string }; units: { id: string; name: string; factor: string }[] }
/** "LOT1:10:2027-05-01; LOT2:5" (lots) or "SN1, SN2" (serials, quantity 1 each) → API lots. */
export function parseLots(text: string, tracking: string): { lot_no: string; quantity: string; expiry: string }[] {
  const parts = text.split(tracking === 'serial' ? /[,;\n]/ : /[;\n]/).map((s) => s.trim()).filter(Boolean);
  return parts.map((p) => {
    if (tracking === 'serial') return { lot_no: p, quantity: '1', expiry: '' };
    const [lot, qty, exp] = p.split(':').map((s) => s.trim());
    return { lot_no: lot, quantity: qty ?? '', expiry: exp ?? '' };
  });
}
let lk = 1;
const blank = (): LineState => ({ key: lk++, item_id: null, account_id: null, warehouse_id: null, description: '', quantity: '1', unit_price: '', tax_rate_id: null, unit_id: null, lots: '' });

/** A bill to start from (e.g. read from a photo of the supplier's bill): everything stays editable before saving. */
export interface BillPrefill {
  supplier_id?: string; currency?: string; bill_date?: string; due_date?: string; supplier_ref?: string;
  lines?: { item_id?: string; description: string; quantity: string; unit_price: string }[];
}

export function NewBill({ supplier, prefill, onClose, onDone }: { supplier?: Supplier; prefill?: BillPrefill; onClose: () => void; onDone: (id: string) => void }) {
  const { t, i18n } = useTranslation();
  const centers = useCostCenters().filter((x) => x.is_active);
  const [cc, setCc] = useState<string | null>(null);
  const { company, currencies, decimalsOf } = useCompany();
  const [suppliers, setSuppliers] = useState<Supplier[]>([]);
  const [items, setItems] = useState<Item[]>([]);
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [warehouses, setWarehouses] = useState<Warehouse[]>([]);
  const [supplierId, setSupplierId] = useState<string | null>(supplier?.id ?? prefill?.supplier_id ?? null);
  const [currency, setCurrency] = useState<string | null>(prefill?.currency ?? supplier?.currency ?? company?.base_currency ?? 'USD');
  const [date, setDate] = useState(prefill?.bill_date ?? today());
  const [due, setDue] = useState(prefill?.due_date ?? ''); // empty = the supplier's payment terms
  const [ref, setRef] = useState(prefill?.supplier_ref ?? '');
  const [lines, setLines] = useState<LineState[]>(() => (prefill?.lines?.length
    ? prefill.lines.map((l) => ({ ...blank(), item_id: l.item_id ?? null, description: l.description, quantity: l.quantity, unit_price: l.unit_price }))
    : [blank()]));
  const [busy, setBusy] = useState(false);
  const [taxes, setTaxes] = useState<{ id: string; name: string; rate: string; applies_to: string; is_active: boolean }[]>([]);

  useEffect(() => {
    if (!company) return;
    const base = `/companies/${company.id}`;
    api<{ id: string; name: string; rate: string; applies_to: string; is_active: boolean }[]>(`${base}/tax-rates`)
      .then((x) => setTaxes(x.filter((r) => r.is_active && r.applies_to !== 'sales'))).catch(() => {});
    if (!supplier) api<Supplier[]>(`${base}/suppliers`).then((s) => setSuppliers(s.filter((x) => x.status === 'active'))).catch(() => {});
    api<Item[]>(`${base}/items`).then((x) => setItems(x.filter((i) => i.is_active))).catch(() => {});
    api<Account[]>(`${base}/accounts`).then((a) => setAccounts(a.filter((x) => x.is_postable && x.is_active))).catch(() => {});
    api<Warehouse[]>(`${base}/warehouses`).then(setWarehouses).catch(() => {});
  }, [company, supplier]);

  const itemById = new Map(items.map((i) => [i.id, i]));
  const [extras, setExtras] = useState<Record<string, ItemExtras>>({});
  const loadExtras = (id: string | null) => {
    if (!id || !company || extras[id] || itemById.get(id)?.kind !== 'stock') return;
    api<ItemExtras>(`/companies/${company.id}/items/${id}/extras`).then((x) => setExtras((m) => ({ ...m, [id]: x }))).catch(() => {});
  };
  const dec = decimalsOf(currency ?? 'USD');
  const taxRate = (id: string | null) => Number(taxes.find((x) => x.id === id)?.rate ?? 0);
  const total = lines.reduce((s, l) => {
    const a = Math.round(Number(l.quantity || 0) * Number(l.unit_price || 0) * 10 ** dec) / 10 ** dec;
    return s + a + Math.round(a * taxRate(l.tax_rate_id) / 100 * 10 ** dec) / 10 ** dec;
  }, 0);
  const upd = (key: number, p: Partial<LineState>) => setLines((ls) => ls.map((x) => (x.key === key ? { ...x, ...p } : x)));

  async function submit(allowDuplicate = false): Promise<void> {
    if (!company) return;
    setBusy(true);
    try {
      const r = await api<BillDetail>(`/companies/${company.id}/bills`, {
        method: 'POST',
        body: {
          supplier_id: supplierId, currency, bill_date: date, due_date: due && due >= date ? due : '', supplier_ref: ref, cost_center_id: cc ?? '',
          allow_duplicate: allowDuplicate,
          lines: lines.filter((l) => l.unit_price !== '' && (l.item_id || l.description)).map((l) => ({
            item_id: l.item_id ?? '', account_id: l.account_id ?? '', warehouse_id: l.warehouse_id ?? '',
            description: l.description, quantity: l.quantity, unit_price: l.unit_price, tax_rate_id: l.tax_rate_id ?? '',
            unit_id: l.unit_id ?? '', ...(l.item_id && extras[l.item_id] && extras[l.item_id].item.tracking !== 'none' && l.lots.trim()
              ? { lots: parseLots(l.lots, extras[l.item_id].item.tracking) } : {}),
          })),
        },
      });
      notifySaved(t);
      onDone(r.bill.id);
    } catch (e) {
      // the same supplier invoice number was already entered: ask before booking it twice
      if (e instanceof ApiError && e.code === 'duplicate_supplier_ref' && window.confirm(t('oa.dupRef', { n: e.detail ?? '' }))) {
        setBusy(false);
        return submit(true);
      }
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened onClose={onClose} size="70rem" title={`${t('pur.newBill')}${supplier ? ` · ${supplier.name}` : ''}`}>
      <Stack>
        <Group grow align="flex-end">
          {!supplier && <Select label={t('pur.supplier')} searchable required data={suppliers.map((s) => ({ value: s.id, label: `${s.code} · ${s.name}` }))}
            value={supplierId} onChange={(v) => { setSupplierId(v); const s = suppliers.find((x) => x.id === v); if (s?.currency) setCurrency(s.currency); }} />}
          <TextInput label={t('pur.supplierRef')} value={ref} onChange={(e) => setRef(e.currentTarget.value)} />
          <Select label={t('pur.currency')} searchable data={currencies.map((c) => c.code)} value={currency} onChange={setCurrency} allowDeselect={false} />
          <TextInput type="date" label={t('pur.billDate')} value={date} onChange={(e) => setDate(e.currentTarget.value)} />
          <TextInput type="date" label={t('invoices.due')} placeholder={t('oa.terms.auto')} description={due ? undefined : t('oa.terms.auto')} value={due} onChange={(e) => setDue(e.currentTarget.value)} />
          {centers.length > 0 && <Select label={t('ax.cc.center')} clearable placeholder={t('ax.cc.none')} value={cc} onChange={setCc}
            data={centers.map((x) => ({ value: x.id, label: ccLabel(x, i18n.language) }))} />}
        </Group>
        <Table.ScrollContainer minWidth={900}>
          <Table withTableBorder verticalSpacing={4}>
            <Table.Thead><Table.Tr>
              <Table.Th miw={200}>{t('pur.item')}</Table.Th><Table.Th miw={180}>{t('invoices.description')} / {t('pur.account')}</Table.Th>
              <Table.Th w={150}>{t('pur.warehouse')}</Table.Th><Table.Th w={100}>{t('pur.qty')}</Table.Th><Table.Th w={120}>{t('pur.unitPrice')}</Table.Th>
              <Table.Th w={120} style={{ textAlign: 'end' }}>{t('pur.amount')}</Table.Th><Table.Th w={36} />
            </Table.Tr></Table.Thead>
            <Table.Tbody>
              {lines.map((l) => {
                const it = l.item_id ? itemById.get(l.item_id) : undefined;
                return (
                  <Table.Tr key={l.key}>
                    <Table.Td>
                      <Select size="xs" searchable clearable placeholder={t('pur.freeLine')} value={l.item_id}
                        data={items.map((i) => ({ value: i.id, label: `${i.code} · ${i.name} (${i.unit})` }))}
                        onChange={(v) => { upd(l.key, { item_id: v, unit_id: null, lots: '', description: v ? itemById.get(v)?.name ?? '' : l.description }); loadExtras(v); }} comboboxProps={{ withinPortal: true }} />
                    </Table.Td>
                    <Table.Td>
                      <Stack gap={2}>
                        <TextInput size="xs" value={l.description} onChange={(e) => upd(l.key, { description: e.currentTarget.value })} />
                        {(!it || it.kind !== 'stock') && (
                          <Select size="xs" searchable clearable placeholder={t('pur.defaultExpense')} value={l.account_id}
                            data={accounts.filter((a) => a.type === 'expense' || a.type === 'asset').map((a) => ({ value: a.id, label: accountLabel(a) }))}
                            onChange={(v) => upd(l.key, { account_id: v })} comboboxProps={{ withinPortal: true }} />
                        )}
                        {taxes.length > 0 && (
                          <Select size="xs" clearable placeholder={t('net.noTax')} value={l.tax_rate_id}
                            data={taxes.map((x) => ({ value: x.id, label: x.name }))}
                            onChange={(v) => upd(l.key, { tax_rate_id: v })} comboboxProps={{ withinPortal: true }} />
                        )}
                      </Stack>
                    </Table.Td>
                    <Table.Td>
                      {it?.kind === 'stock' && warehouses.length > 1 && (
                        <Select size="xs" value={l.warehouse_id} placeholder={warehouses[0]?.name}
                          data={warehouses.map((w) => ({ value: w.id, label: w.name }))} onChange={(v) => upd(l.key, { warehouse_id: v })} comboboxProps={{ withinPortal: true }} />
                      )}
                    </Table.Td>
                    <Table.Td>
                      <Stack gap={2}>
                        <TextInput size="xs" dir="ltr" value={l.quantity} rightSection={!(l.item_id && extras[l.item_id]?.units.length) ? <Text size="xs" c="dimmed">{it?.unit}</Text> : undefined}
                          onChange={(e) => upd(l.key, { quantity: e.currentTarget.value.replace(/[^\d.]/g, '') })} />
                        {l.item_id && (extras[l.item_id]?.units.length ?? 0) > 0 && (
                          <Select size="xs" value={l.unit_id ?? ''} allowDeselect={false} comboboxProps={{ withinPortal: true }}
                            data={[{ value: '', label: it?.unit ?? '' }, ...extras[l.item_id].units.map((u) => ({ value: u.id, label: `${u.name} (${u.factor} ${it?.unit ?? ''})` }))]}
                            onChange={(v) => upd(l.key, { unit_id: v || null })} />
                        )}
                        {l.item_id && extras[l.item_id] && extras[l.item_id].item.tracking !== 'none' && (
                          <TextInput size="xs" dir="ltr" value={l.lots} onChange={(e) => upd(l.key, { lots: e.currentTarget.value })}
                            placeholder={extras[l.item_id].item.tracking === 'serial' ? 'SN001, SN002' : 'LOT-A:10:2027-05-01; LOT-B:5'}
                            title={t(extras[l.item_id].item.tracking === 'serial' ? 'hr2.serialsHint' : 'hr2.lotsHint')} />
                        )}
                      </Stack>
                    </Table.Td>
                    <Table.Td><TextInput size="xs" dir="ltr" value={l.unit_price} onChange={(e) => upd(l.key, { unit_price: e.currentTarget.value.replace(/[^\d.]/g, '') })} /></Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(Number(l.quantity || 0) * Number(l.unit_price || 0), dec)}</Table.Td>
                    <Table.Td><ActionIcon variant="subtle" color="gray" disabled={lines.length === 1} onClick={() => setLines((ls) => ls.filter((x) => x.key !== l.key))}><IconTrash size={14} /></ActionIcon></Table.Td>
                  </Table.Tr>
                );
              })}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
        <Group justify="space-between">
          <Button variant="subtle" size="xs" leftSection={<IconPlus size={14} />} onClick={() => setLines((ls) => [...ls, blank()])}>{t('invoices.addLine')}</Button>
          <Text fw={700} size="lg" dir="ltr">{formatAmount(total, dec)} {currency}</Text>
        </Group>
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
          <Button loading={busy} disabled={!supplierId || total <= 0} onClick={() => void submit()}>{t('common.save')}</Button>
        </Group>
      </Stack>
    </Modal>
  );
}
