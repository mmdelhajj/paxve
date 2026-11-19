import { useCallback, useEffect, useState } from 'react';
import { Anchor, Badge, Button, Center, Group, Loader, Modal, Paper, Select, Stack, Table, Tabs, Text, TextInput, Title } from '@mantine/core';
import { useDebouncedValue } from '@mantine/hooks';
import { IconArrowBack, IconCash, IconEdit, IconFileInvoice, IconPlus, IconSearch } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { can, api, type Me } from '../../api';
import { useCompany } from '../../company';
import { formatAmount, formatDate, notifyError, notifySaved } from '../../util';
import PageHeader from '../../components/PageHeader';
import { MoneyList } from '../billing/docs';
import { billColor, type Bill, type Supplier, type SupplierPayment } from './types';
import { BillView, NewBill } from './Bills';
import { PaySupplier } from './SupplierPayments';
import { saveTerms, TermsInput } from '../../components/PartnerTerms';

export default function Suppliers({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const { company } = useCompany();
  const [rows, setRows] = useState<Supplier[] | null>(null);
  const [q, setQ] = useState('');
  const [dq] = useDebouncedValue(q, 250);
  const [creating, setCreating] = useState(false);
  const canEdit = can(me, 'suppliers.create');

  const load = useCallback(() => {
    if (!company) return;
    api<Supplier[]>(`/companies/${company.id}/suppliers?q=${encodeURIComponent(dq)}`).then(setRows).catch((e) => notifyError(t, e));
  }, [company, dq, t]);
  useEffect(load, [load]);
  if (!company) return null;

  return (
    <Stack gap="lg" maw={1200}>
      <PageHeader title={t('pur.suppliers')} subtitle={company.name_en}>
        {canEdit && <Button leftSection={<IconPlus size={16} />} onClick={() => setCreating(true)}>{t('pur.newSupplier')}</Button>}
      </PageHeader>
      <TextInput leftSection={<IconSearch size={16} />} placeholder={t('common.search')} value={q} onChange={(e) => setQ(e.currentTarget.value)} w={300} />
      <Paper withBorder>
        {rows === null ? <Center p="xl"><Loader size="sm" /></Center> : rows.length === 0 ? <Text c="dimmed" ta="center" p="xl">{t('common.empty')}</Text> : (
          <Table.ScrollContainer minWidth={700}>
            <Table verticalSpacing="sm" highlightOnHover key={i18n.language}>
              <Table.Thead><Table.Tr>
                <Table.Th w={80}>{t('pur.code')}</Table.Th><Table.Th>{t('pur.name')}</Table.Th><Table.Th>{t('customers.phone')}</Table.Th>
                <Table.Th>{t('customers.due')}</Table.Th><Table.Th>{t('common.status')}</Table.Th>
              </Table.Tr></Table.Thead>
              <Table.Tbody>
                {rows.map((s) => (
                  <Table.Tr key={s.id} style={{ cursor: 'pointer' }} onClick={() => { window.location.hash = `#/supplier/${s.id}`; }}>
                    <Table.Td ff="monospace">{s.code}</Table.Td>
                    <Table.Td fw={500}>{s.name}</Table.Td>
                    <Table.Td dir="ltr" style={{ textAlign: 'start' }}>{s.phone}</Table.Td>
                    <Table.Td><MoneyList items={s.due} />{s.credit.length > 0 && <MoneyList items={s.credit} kind="credit" />}</Table.Td>
                    <Table.Td><Badge variant="light" color={s.status === 'active' ? 'teal' : 'gray'}>{s.status === 'active' ? t('common.active') : t('common.inactive')}</Badge></Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </Paper>
      {creating && <SupplierForm onClose={() => setCreating(false)} onSaved={(s) => { setCreating(false); window.location.hash = `#/supplier/${s.id}`; }} />}
    </Stack>
  );
}

function SupplierForm({ supplier, onClose, onSaved }: { supplier?: Supplier; onClose: () => void; onSaved: (s: Supplier) => void }) {
  const { t } = useTranslation();
  const { company, currencies } = useCompany();
  const [f, setF] = useState({
    code: supplier?.code ?? '', name: supplier?.name ?? '', phone: supplier?.phone ?? '', email: supplier?.email ?? '',
    address: supplier?.address ?? '', tax_id: supplier?.tax_id ?? '', currency: supplier?.currency ?? company?.base_currency ?? '',
    notes: supplier?.notes ?? '', status: supplier?.status ?? 'active',
  });
  const [terms, setTerms] = useState(supplier?.due_days == null ? '' : String(supplier.due_days));
  const [busy, setBusy] = useState(false);
  const set = (k: keyof typeof f) => (v: string) => setF((x) => ({ ...x, [k]: v }));

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!company) return;
    setBusy(true);
    try {
      const s = supplier
        ? await api<Supplier>(`/companies/${company.id}/suppliers/${supplier.id}`, { method: 'PATCH', body: f })
        : await api<Supplier>(`/companies/${company.id}/suppliers`, { method: 'POST', body: { ...f, status: undefined } });
      await saveTerms(company.id, 'supplier_id', s.id, supplier?.due_days, terms);
      notifySaved(t);
      onSaved(s);
    } catch (err) {
      notifyError(t, err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened onClose={onClose} title={supplier?.name ?? t('pur.newSupplier')} size="lg">
      <form onSubmit={submit}>
        <Stack>
          <Group grow align="flex-start">
            <TextInput label={t('pur.name')} required value={f.name} onChange={(e) => set('name')(e.currentTarget.value)} data-autofocus />
            <TextInput label={`${t('pur.code')} (${t('common.optional')})`} value={f.code} onChange={(e) => set('code')(e.currentTarget.value)} maw={160} />
          </Group>
          <Group grow>
            <TextInput label={t('customers.phone')} dir="ltr" value={f.phone} onChange={(e) => set('phone')(e.currentTarget.value)} />
            <TextInput label={t('customers.email')} dir="ltr" value={f.email} onChange={(e) => set('email')(e.currentTarget.value)} />
          </Group>
          <Group grow>
            <TextInput label={t('pur.taxId')} dir="ltr" value={f.tax_id} onChange={(e) => set('tax_id')(e.currentTarget.value)} />
            <Select label={t('pur.currency')} searchable data={currencies.map((c) => c.code)} value={f.currency || null} onChange={(v) => set('currency')(v ?? '')} />
          </Group>
          <TextInput label={t('customers.address')} value={f.address} onChange={(e) => set('address')(e.currentTarget.value)} />
          <TextInput label={t('customers.notes')} value={f.notes} onChange={(e) => set('notes')(e.currentTarget.value)} />
          <TermsInput value={terms} onChange={setTerms} />
          {supplier && (
            <Select label={t('common.status')} value={f.status} allowDeselect={false} onChange={(v) => v && set('status')(v)}
              data={[{ value: 'active', label: t('common.active') }, { value: 'inactive', label: t('common.inactive') }]} />
          )}
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
            <Button type="submit" loading={busy}>{t('common.save')}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}

interface Detail { supplier: Supplier; bills: Bill[]; payments: SupplierPayment[] }

export function SupplierDetail({ me, id }: { me: Me; id: string }) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [d, setD] = useState<Detail | null>(null);
  const [modal, setModal] = useState<null | 'edit' | 'bill' | 'pay'>(null);
  const [viewBill, setViewBill] = useState<string | null>(null);
  const canManage = can(me, 'purchasing.create');

  const load = useCallback(() => {
    if (!company) return;
    api<Detail>(`/companies/${company.id}/suppliers/${id}`).then(setD).catch((e) => notifyError(t, e));
  }, [company, id, t]);
  useEffect(load, [load]);
  if (!company) return null;
  if (!d) return <Center p="xl"><Loader /></Center>;
  const s = d.supplier;

  return (
    <Stack gap="lg" maw={1200}>
      <Anchor href="#/suppliers" size="sm"><Group gap={4}><IconArrowBack size={14} />{t('pur.backSuppliers')}</Group></Anchor>
      <Group justify="space-between" align="flex-start" wrap="wrap">
        <div>
          <Title order={2}>{s.name}</Title>
          <Text c="dimmed" size="sm">#{s.code}{s.phone && <> · <span dir="ltr">{s.phone}</span></>}{s.tax_id && ` · ${t('pur.taxId')} ${s.tax_id}`}</Text>
        </div>
        <Group gap="xs">
          {can(me, 'suppliers.edit') && <Button variant="default" leftSection={<IconEdit size={16} />} onClick={() => setModal('edit')}>{t('common.edit')}</Button>}
          {canManage && <Button variant="default" leftSection={<IconFileInvoice size={16} />} onClick={() => setModal('bill')}>{t('pur.newBill')}</Button>}
          {canManage && <Button leftSection={<IconCash size={16} />} onClick={() => setModal('pay')}>{t('pur.pay')}</Button>}
        </Group>
      </Group>
      <Paper withBorder p="md">
        <Text size="xs" c="dimmed" tt="uppercase" fw={600}>{t('customers.due')}</Text>
        {s.due.length === 0 ? <Text fw={600} c="teal" mt={4}>{t('customers.nothingDue')}</Text> :
          s.due.map((m) => <Text key={m.currency} size="1.5rem" fw={700} c="red.7" dir="ltr">{formatAmount(m.due, decimalsOf(m.currency))} {m.currency}</Text>)}
        {s.credit.length > 0 && <MoneyList items={s.credit} kind="credit" />}
      </Paper>
      <Tabs defaultValue="bills" keepMounted={false}>
        <Tabs.List>
          <Tabs.Tab value="bills">{t('pur.bills')} ({d.bills.length})</Tabs.Tab>
          <Tabs.Tab value="pays">{t('pur.supplierPayments')} ({d.payments.length})</Tabs.Tab>
        </Tabs.List>
        <Tabs.Panel value="bills" pt="md">
          <Paper withBorder>
            <BillsTable rows={d.bills} onOpen={setViewBill} lang={i18n.language} />
          </Paper>
        </Tabs.Panel>
        <Tabs.Panel value="pays" pt="md">
          <Paper withBorder>
            {d.payments.length === 0 ? <Text c="dimmed" ta="center" p="lg">{t('common.empty')}</Text> : (
              <Table verticalSpacing="xs">
                <Table.Tbody>
                  {d.payments.map((p) => (
                    <Table.Tr key={p.id} style={{ opacity: p.status === 'void' ? 0.5 : 1 }}>
                      <Table.Td ff="monospace">{p.number}</Table.Td>
                      <Table.Td>{formatDate(p.payment_date)}</Table.Td>
                      <Table.Td>{p.method}</Table.Td>
                      <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(p.paid_amount, decimalsOf(p.paid_currency))} {p.paid_currency}</Table.Td>
                      <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(p.amount, decimalsOf(p.currency))} {p.currency}</Table.Td>
                      <Table.Td>{p.status === 'void' && <Badge color="gray">{t('invoices.statuses.void')}</Badge>}</Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            )}
          </Paper>
        </Tabs.Panel>
      </Tabs>
      {modal === 'edit' && <SupplierForm supplier={s} onClose={() => setModal(null)} onSaved={() => { setModal(null); load(); }} />}
      {modal === 'bill' && <NewBill supplier={s} onClose={() => setModal(null)} onDone={(bid) => { setModal(null); load(); setViewBill(bid); }} />}
      {modal === 'pay' && <PaySupplier supplier={s} onClose={() => setModal(null)} onDone={() => { setModal(null); load(); }} />}
      {viewBill && <BillView id={viewBill} canVoid={canManage} onClose={() => setViewBill(null)} onChanged={load} />}
    </Stack>
  );
}

export function BillsTable({ rows, onOpen, lang }: { rows: Bill[]; onOpen: (id: string) => void; lang: string }) {
  const { t } = useTranslation();
  const { decimalsOf } = useCompany();
  if (rows.length === 0) return <Text c="dimmed" ta="center" p="lg">{t('common.empty')}</Text>;
  return (
    <Table.ScrollContainer minWidth={700}>
      <Table verticalSpacing="xs" highlightOnHover key={lang}>
        <Table.Thead><Table.Tr>
          <Table.Th w={70}>{t('invoices.number')}</Table.Th><Table.Th>{t('pur.supplier')}</Table.Th><Table.Th>{t('pur.billDate')}</Table.Th>
          <Table.Th style={{ textAlign: 'end' }}>{t('pur.total')}</Table.Th><Table.Th style={{ textAlign: 'end' }}>{t('invoices.balance')}</Table.Th>
          <Table.Th>{t('common.status')}</Table.Th>
        </Table.Tr></Table.Thead>
        <Table.Tbody>
          {rows.map((b) => (
            <Table.Tr key={b.id} style={{ cursor: 'pointer' }} onClick={() => onOpen(b.id)}>
              <Table.Td ff="monospace">{b.number}</Table.Td>
              <Table.Td><Text size="sm" fw={500}>{b.supplier_name}</Text>{b.supplier_ref && <Text size="xs" c="dimmed">{b.supplier_ref}</Text>}</Table.Td>
              <Table.Td>{formatDate(b.bill_date)}</Table.Td>
              <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(b.total, decimalsOf(b.currency))} {b.currency}</Table.Td>
              <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{b.status === 'void' ? '—' : formatAmount(b.balance, decimalsOf(b.currency))}</Table.Td>
              <Table.Td><Badge variant="light" color={billColor[b.status]}>{t(`invoices.statuses.${b.status}`)}</Badge></Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </Table.ScrollContainer>
  );
}
