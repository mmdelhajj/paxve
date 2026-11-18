import { useCallback, useEffect, useState } from 'react';
import { Badge, Button, Center, Checkbox, Group, Loader, Modal, Paper, Select, SimpleGrid, Stack, Table, Text, TextInput } from '@mantine/core';
import { IconPlus } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { api, type Account, type Me } from '../../api';
import { useCompany } from '../../company';
import { accountLabel, accountName, formatAmount, formatDate, notifyError, notifySaved, today } from '../../util';
import PageHeader, { Confirm } from '../../components/PageHeader';
import type { PaymentMethod } from '../../billing';
import type { Expense } from './types';

interface Total { account_id: string; account_code: string; account_name: string; account_name_i18n: Record<string, string>; currency: string; count: number; total: string }

export default function Expenses({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const now = new Date();
  const [from, setFrom] = useState(`${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}-01`);
  const [to, setTo] = useState(today());
  const [data, setData] = useState<{ rows: Expense[]; totals: Total[] } | null>(null);
  const [creating, setCreating] = useState(false);
  const [voiding, setVoiding] = useState<string | null>(null);
  const canEdit = me.role !== 'read_only';
  const canManage = me.role === 'admin' || me.role === 'accountant';

  const load = useCallback(() => {
    if (!company) return;
    api<{ rows: Expense[]; totals: Total[] }>(`/companies/${company.id}/expenses?from=${from}&to=${to}`).then(setData).catch((e) => notifyError(t, e));
  }, [company, from, to, t]);
  useEffect(load, [load]);
  if (!company) return null;

  async function doVoid(id: string) {
    if (!company) return;
    try {
      await api(`/companies/${company.id}/expenses/${id}/void`, { method: 'POST', body: {} });
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
      <PageHeader title={t('pur.expenses')} subtitle={company.name_en}>
        <TextInput type="date" size="sm" value={from} onChange={(e) => setFrom(e.currentTarget.value)} />
        <TextInput type="date" size="sm" value={to} onChange={(e) => setTo(e.currentTarget.value)} />
        {canEdit && <Button leftSection={<IconPlus size={16} />} onClick={() => setCreating(true)}>{t('pur.newExpense')}</Button>}
      </PageHeader>
      {data && data.totals.length > 0 && (
        <SimpleGrid cols={{ base: 1, xs: 2, md: 4 }}>
          {data.totals.map((x) => (
            <Paper withBorder p="md" key={x.account_id + x.currency}>
              <Text size="sm" fw={500} truncate>{x.account_code} · {accountName({ name: x.account_name, name_i18n: x.account_name_i18n })}</Text>
              <Text size="lg" fw={700} dir="ltr">{formatAmount(x.total, decimalsOf(x.currency))} {x.currency}</Text>
              <Text size="xs" c="dimmed">{x.count}</Text>
            </Paper>
          ))}
        </SimpleGrid>
      )}
      {voiding && <Confirm text={t('pur.voidExpenseConfirm')} yesLabel={t('pur.voidExpense')} noLabel={t('common.cancel')} onYes={() => doVoid(voiding)} onNo={() => setVoiding(null)} />}
      <Paper withBorder>
        {data === null ? <Center p="xl"><Loader size="sm" /></Center> : data.rows.length === 0 ? <Text c="dimmed" ta="center" p="xl">{t('common.empty')}</Text> : (
          <Table.ScrollContainer minWidth={760}>
            <Table verticalSpacing="xs" key={i18n.language}>
              <Table.Thead><Table.Tr>
                <Table.Th>{t('invoices.number')}</Table.Th><Table.Th>{t('invoices.date')}</Table.Th><Table.Th>{t('pur.category')}</Table.Th>
                <Table.Th>{t('pur.description')}</Table.Th><Table.Th>{t('pur.paidFrom')}</Table.Th><Table.Th style={{ textAlign: 'end' }}>{t('pur.amount')}</Table.Th><Table.Th />
              </Table.Tr></Table.Thead>
              <Table.Tbody>
                {data.rows.map((e) => (
                  <Table.Tr key={e.id} style={{ opacity: e.status === 'void' ? 0.5 : 1 }}>
                    <Table.Td ff="monospace">{e.number}</Table.Td>
                    <Table.Td>{formatDate(e.expense_date)}</Table.Td>
                    <Table.Td>{e.account_code} · {accountName({ name: e.account_name, name_i18n: e.account_name_i18n })}</Table.Td>
                    <Table.Td>{e.description}{e.supplier_name && <Text size="xs" c="dimmed">{e.supplier_name}</Text>}</Table.Td>
                    <Table.Td c="dimmed">{e.method}</Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(e.amount, decimalsOf(e.currency))} {e.currency}
                      {Number(e.tax_amount) > 0 && <Text size="xs" c="dimmed">{e.tax_name}: {formatAmount(e.tax_amount, decimalsOf(e.currency))}</Text>}</Table.Td>
                    <Table.Td>{e.status === 'void' ? <Badge color="gray">{t('invoices.statuses.void')}</Badge>
                      : canManage && <Button size="compact-xs" variant="subtle" color="red" onClick={() => setVoiding(e.id)}>{t('pur.voidExpense')}</Button>}</Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </Paper>
      {creating && <NewExpense onClose={() => setCreating(false)} onDone={() => { setCreating(false); load(); }} />}
    </Stack>
  );
}

function NewExpense({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [methods, setMethods] = useState<PaymentMethod[]>([]);
  const [f, setF] = useState({ account_id: '', method_id: '', amount: '', description: '', date: today(), tax_rate_id: '', tax_included: false });
  const [taxes, setTaxes] = useState<{ id: string; name: string; rate: string; applies_to: string; is_active: boolean }[]>([]);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (!company) return;
    api<Account[]>(`/companies/${company.id}/accounts`).then((a) => setAccounts(a.filter((x) => x.is_postable && x.is_active && x.type === 'expense'))).catch(() => {});
    api<typeof taxes>(`/companies/${company.id}/tax-rates`).then((x) => setTaxes(x.filter((r) => r.is_active && r.applies_to !== 'sales'))).catch(() => {});
    api<PaymentMethod[]>(`/companies/${company.id}/payment-methods`).then((m) => {
      const a = m.filter((x) => x.is_active); setMethods(a); if (a[0]) setF((x) => ({ ...x, method_id: a[0].id }));
    }).catch(() => {});
  }, [company]);
  const m = methods.find((x) => x.id === f.method_id);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!company) return;
    setBusy(true);
    try {
      await api(`/companies/${company.id}/expenses`, { method: 'POST', body: f });
      notifySaved(t);
      onDone();
    } catch (err) {
      notifyError(t, err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened onClose={onClose} title={t('pur.newExpense')}>
      <form onSubmit={submit}>
        <Stack>
          <TextInput type="date" label={t('invoices.date')} value={f.date} onChange={(e) => setF({ ...f, date: e.currentTarget.value })} />
          <Select label={t('pur.category')} required searchable data={accounts.map((a) => ({ value: a.id, label: accountLabel(a) }))}
            value={f.account_id || null} onChange={(v) => setF({ ...f, account_id: v ?? '' })} />
          <TextInput label={t('pur.description')} value={f.description} onChange={(e) => setF({ ...f, description: e.currentTarget.value })} />
          <Group grow>
            <Select label={t('pur.paidFrom')} data={methods.map((x) => ({ value: x.id, label: `${x.name} (${x.currency})` }))} value={f.method_id || null}
              onChange={(v) => setF({ ...f, method_id: v ?? '' })} allowDeselect={false} />
            <TextInput label={t('pur.amount')} dir="ltr" required value={f.amount} rightSection={<Text size="xs" c="dimmed">{m?.currency}</Text>}
              onChange={(e) => setF({ ...f, amount: e.currentTarget.value.replace(/[^\d.]/g, '') })} />
          </Group>
          {taxes.length > 0 && (
            <Group grow align="flex-end">
              <Select label={t('net.tax')} clearable placeholder={t('net.noTax')} value={f.tax_rate_id || null}
                data={taxes.map((x) => ({ value: x.id, label: `${x.name} (${x.rate}%)` }))} onChange={(v) => setF({ ...f, tax_rate_id: v ?? '' })} />
              <Checkbox label={t('gap.taxIncluded')} checked={f.tax_included} disabled={!f.tax_rate_id} mb={8}
                onChange={(e) => setF({ ...f, tax_included: e.currentTarget.checked })} />
            </Group>
          )}
          {f.tax_rate_id && Number(f.amount) > 0 && (() => {
            const r = Number(taxes.find((x) => x.id === f.tax_rate_id)?.rate ?? 0), a = Number(f.amount);
            const tax = f.tax_included ? a - a * 100 / (100 + r) : a * r / 100;
            return <Text size="sm" c="dimmed" dir="ltr">{t('net.tax')}: {tax.toFixed(2)} · {t('pur.total')}: {(f.tax_included ? a : a + tax).toFixed(2)} {m?.currency}</Text>;
          })()}
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
            <Button type="submit" loading={busy} disabled={!f.account_id || !Number(f.amount)}>{t('common.save')}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}
