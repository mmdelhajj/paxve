import { useCallback, useEffect, useState } from 'react';
import { Badge, Button, Center, Group, Loader, Modal, Paper, Select, Stack, Table, Text, TextInput } from '@mantine/core';
import { IconPlus } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { can, api, type Me, type Rate } from '../../api';
import { useCompany } from '../../company';
import { formatAmount, formatDate, notifyError, notifySaved, today } from '../../util';
import PageHeader, { Confirm } from '../../components/PageHeader';
import type { PaymentMethod } from '../../billing';
import { MoneyList } from '../billing/docs';
import type { Supplier, SupplierPayment } from './types';

export default function SupplierPayments({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [from, setFrom] = useState(`${new Date().getFullYear()}-01-01`);
  const [to, setTo] = useState(today());
  const [rows, setRows] = useState<SupplierPayment[] | null>(null);
  const [paying, setPaying] = useState(false);
  const [voiding, setVoiding] = useState<string | null>(null);
  const canManage = can(me, 'purchasing.create');

  const load = useCallback(() => {
    if (!company) return;
    api<SupplierPayment[]>(`/companies/${company.id}/supplier-payments?from=${from}&to=${to}`).then(setRows).catch((e) => notifyError(t, e));
  }, [company, from, to, t]);
  useEffect(load, [load]);
  if (!company) return null;

  async function doVoid(id: string) {
    if (!company) return;
    try {
      await api(`/companies/${company.id}/supplier-payments/${id}/void`, { method: 'POST', body: {} });
      notifySaved(t);
      load();
    } catch (e) {
      notifyError(t, e);
    } finally {
      setVoiding(null);
    }
  }

  return (
    <Stack gap="lg" maw={1200}>
      <PageHeader title={t('pur.supplierPayments')} subtitle={company.name_en}>
        <TextInput type="date" size="sm" value={from} onChange={(e) => setFrom(e.currentTarget.value)} />
        <TextInput type="date" size="sm" value={to} onChange={(e) => setTo(e.currentTarget.value)} />
        {canManage && <Button leftSection={<IconPlus size={16} />} onClick={() => setPaying(true)}>{t('pur.pay')}</Button>}
      </PageHeader>
      {voiding && <Confirm text={t('pur.voidPaymentConfirm')} yesLabel={t('pur.voidPayment')} noLabel={t('common.cancel')} onYes={() => doVoid(voiding)} onNo={() => setVoiding(null)} />}
      <Paper withBorder>
        {rows === null ? <Center p="xl"><Loader size="sm" /></Center> : rows.length === 0 ? <Text c="dimmed" ta="center" p="xl">{t('common.empty')}</Text> : (
          <Table.ScrollContainer minWidth={760}>
            <Table verticalSpacing="xs" key={i18n.language}>
              <Table.Thead><Table.Tr>
                <Table.Th>{t('invoices.number')}</Table.Th><Table.Th>{t('invoices.date')}</Table.Th><Table.Th>{t('pur.supplier')}</Table.Th>
                <Table.Th>{t('pur.paidFrom')}</Table.Th><Table.Th style={{ textAlign: 'end' }}>{t('pur.paidAmount')}</Table.Th>
                <Table.Th style={{ textAlign: 'end' }}>{t('pur.applied')}</Table.Th><Table.Th />
              </Table.Tr></Table.Thead>
              <Table.Tbody>
                {rows.map((p) => (
                  <Table.Tr key={p.id} style={{ opacity: p.status === 'void' ? 0.5 : 1 }}>
                    <Table.Td ff="monospace">{p.number}</Table.Td>
                    <Table.Td>{formatDate(p.payment_date)}</Table.Td>
                    <Table.Td fw={500}><a href={`#/supplier/${p.supplier_id}`} style={{ color: 'inherit' }}>{p.supplier_name}</a></Table.Td>
                    <Table.Td>{p.kind === 'debit_note'
                      ? <Badge size="sm" variant="light" color="orange">{t('gap.debitNote')}{p.bill_number ? ` · ${t('pur.bill')} #${p.bill_number}` : ''}</Badge>
                      : p.method}{p.kind !== 'debit_note' && p.paid_currency !== company.base_currency && <Text span size="xs" c="dimmed"> @ {p.paid_rate}</Text>}</Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(p.paid_amount, decimalsOf(p.paid_currency))} {p.paid_currency}</Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(p.amount, decimalsOf(p.currency))} {p.currency}</Table.Td>
                    <Table.Td>
                      {p.status === 'void' ? <Badge color="gray">{t('invoices.statuses.void')}</Badge>
                        : canManage && <Button size="compact-xs" variant="subtle" color="red" onClick={() => setVoiding(p.id)}>{t('pur.voidPayment')}</Button>}
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </Paper>
      {paying && <PaySupplier onClose={() => setPaying(false)} onDone={() => { setPaying(false); load(); }} />}
    </Stack>
  );
}

export function PaySupplier({ supplier, onClose, onDone }: { supplier?: Supplier; onClose: () => void; onDone: () => void }) {
  const { t } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const base = company?.base_currency ?? 'USD';
  const [suppliers, setSuppliers] = useState<Supplier[]>(supplier ? [supplier] : []);
  const [supplierId, setSupplierId] = useState<string | null>(supplier?.id ?? null);
  const [methods, setMethods] = useState<PaymentMethod[]>([]);
  const [rates, setRates] = useState<Record<string, string>>({});
  const [methodId, setMethodId] = useState<string | null>(null);
  const [paid, setPaid] = useState('');
  const [rate, setRate] = useState('');
  const [applied, setApplied] = useState('');
  const [date, setDate] = useState(today());
  const [busy, setBusy] = useState(false);
  const sup = suppliers.find((s) => s.id === supplierId);
  const currency = sup?.due[0]?.currency ?? sup?.currency ?? base;

  useEffect(() => {
    if (!company) return;
    const b = `/companies/${company.id}`;
    if (!supplier) api<Supplier[]>(`${b}/suppliers`).then(setSuppliers).catch(() => {});
    api<PaymentMethod[]>(`${b}/payment-methods`).then((m) => { const a = m.filter((x) => x.is_active); setMethods(a); if (a[0]) setMethodId(a[0].id); }).catch(() => {});
  }, [company, supplier]);
  useEffect(() => {
    if (!company) return;
    api<{ rates: Rate[] }>(`/companies/${company.id}/rates/latest?date=${date}`)
      .then((r) => setRates(Object.fromEntries(r.rates.map((x) => [x.currency, x.rate])))).catch(() => {});
  }, [company, date]);

  const m = methods.find((x) => x.id === methodId);
  const rateOf = (c: string, custom = '') => (c === base ? 1 : Number(custom || rates[c] || 0));
  const mRate = m ? rateOf(m.currency, rate) : 0;
  const pRate = rateOf(currency);
  const preview = m && mRate && pRate ? (Number(paid || 0) / mRate) * pRate : 0;

  async function submit() {
    if (!company) return;
    setBusy(true);
    try {
      await api(`/companies/${company.id}/supplier-payments`, {
        method: 'POST', body: { supplier_id: supplierId, currency, method_id: methodId, paid_amount: paid, paid_rate: rate, amount: applied, date },
      });
      notifySaved(t);
      onDone();
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened onClose={onClose} title={`${t('pur.pay')}${supplier ? ` · ${supplier.name}` : ''}`}>
      <Stack>
        {!supplier && <Select label={t('pur.supplier')} searchable data={suppliers.map((s) => ({ value: s.id, label: `${s.code} · ${s.name}` }))} value={supplierId} onChange={setSupplierId} />}
        {sup && sup.due.length > 0 && <Group gap="xs"><Text size="sm" c="dimmed">{t('customers.due')}:</Text><MoneyList items={sup.due} /></Group>}
        <TextInput type="date" label={t('invoices.date')} value={date} onChange={(e) => setDate(e.currentTarget.value)} />
        <Select label={t('pur.paidFrom')} data={methods.map((x) => ({ value: x.id, label: `${x.name} (${x.currency})` }))} value={methodId} onChange={(v) => { setMethodId(v); setRate(''); }} allowDeselect={false} />
        <Group grow>
          <TextInput label={t('pur.paidAmount')} dir="ltr" value={paid} rightSection={<Text size="xs" c="dimmed">{m?.currency}</Text>}
            onChange={(e) => setPaid(e.currentTarget.value.replace(/[^\d.]/g, ''))} styles={{ input: { fontFamily: 'monospace' } }} data-autofocus />
          {m && m.currency !== base && <TextInput label={t('pur.rate')} dir="ltr" value={rate} placeholder={rates[m.currency] ?? '?'} onChange={(e) => setRate(e.currentTarget.value.replace(/[^\d.]/g, ''))} />}
        </Group>
        <TextInput label={`${t('pur.applied')} (${t('common.optional')})`} dir="ltr" value={applied} placeholder={preview ? preview.toFixed(decimalsOf(currency)) : ''}
          rightSection={<Text size="xs" c="dimmed">{currency}</Text>} onChange={(e) => setApplied(e.currentTarget.value.replace(/[^\d.]/g, ''))} />
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
          <Button loading={busy} disabled={!supplierId || !methodId || !Number(paid)} onClick={submit}>{t('pur.pay')}</Button>
        </Group>
      </Stack>
    </Modal>
  );
}
