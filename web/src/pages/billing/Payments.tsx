import { useCallback, useEffect, useState } from 'react';
import { Badge, Center, Group, Loader, Paper, SimpleGrid, Stack, Table, Text, TextInput } from '@mantine/core';
import { useDebouncedValue } from '@mantine/hooks';
import { IconSearch } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { api, type Me } from '../../api';
import { useCompany } from '../../company';
import { formatAmount, formatDate, notifyError, today } from '../../util';
import PageHeader from '../../components/PageHeader';
import type { Payment } from '../../billing';
import { PaymentView } from './docs';

interface ByMethod { method: string; currency: string; received: string; change_given: string }

export default function Payments({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [from, setFrom] = useState(today());
  const [to, setTo] = useState(today());
  const [q, setQ] = useState('');
  const [dq] = useDebouncedValue(q, 250);
  const [data, setData] = useState<{ rows: Payment[]; by_method: ByMethod[] } | null>(null);
  const [view, setView] = useState<string | null>(null);
  const canManage = me.role === 'admin' || me.role === 'accountant';

  const load = useCallback(() => {
    if (!company) return;
    setData(null);
    const p = new URLSearchParams({ from, to, q: dq });
    api<{ rows: Payment[]; by_method: ByMethod[] }>(`/companies/${company.id}/payments?${p}`).then(setData).catch((e) => notifyError(t, e));
  }, [company, from, to, dq, t]);
  useEffect(load, [load]);

  if (!company) return null;

  return (
    <Stack gap="lg" maw={1200}>
      <PageHeader title={t('payments.title')} subtitle={company.name_en}>
        <TextInput type="date" size="sm" value={from} onChange={(e) => setFrom(e.currentTarget.value)} aria-label={t('reports.from')} />
        <TextInput type="date" size="sm" value={to} onChange={(e) => setTo(e.currentTarget.value)} aria-label={t('reports.to')} />
        <TextInput size="sm" leftSection={<IconSearch size={16} />} placeholder={t('customers.search')} value={q} onChange={(e) => setQ(e.currentTarget.value)} w={220} />
      </PageHeader>
      {data && data.by_method.length > 0 && (
        <>
          <Text fw={500}>{t('payments.byMethod')}</Text>
          <SimpleGrid cols={{ base: 1, xs: 2, md: 4 }}>
            {data.by_method.map((m) => {
              const d = decimalsOf(m.currency);
              const net = Number(m.received) - Number(m.change_given);
              return (
                <Paper withBorder p="md" key={m.method + m.currency}>
                  <Text size="sm" fw={500}>{m.method}</Text>
                  <Text size="1.4rem" fw={700} dir="ltr">{formatAmount(net, d)} {m.currency}</Text>
                  {m.change_given !== '0' && m.change_given !== '0.0000' && (
                    <Text size="xs" c="dimmed" dir="ltr">{formatAmount(m.received, d)} − {formatAmount(m.change_given, d)} ({t('payments.changeGiven')})</Text>
                  )}
                </Paper>
              );
            })}
          </SimpleGrid>
        </>
      )}
      <Paper withBorder>
        {data === null ? <Center p="xl"><Loader size="sm" /></Center> : data.rows.length === 0 ? (
          <Text c="dimmed" ta="center" p="xl">{t('common.empty')}</Text>
        ) : (
          <Table.ScrollContainer minWidth={700}>
            <Table verticalSpacing="xs" highlightOnHover key={i18n.language}>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th w={70}>{t('payments.number')}</Table.Th>
                  <Table.Th>{t('payments.date')}</Table.Th>
                  <Table.Th>{t('payments.customer')}</Table.Th>
                  <Table.Th style={{ textAlign: 'end' }}>{t('payments.amount')}</Table.Th>
                  <Table.Th>{t('payments.receivedBy')}</Table.Th>
                  <Table.Th />
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {data.rows.map((p) => (
                  <Table.Tr key={p.id} style={{ cursor: 'pointer', opacity: p.status === 'void' ? 0.5 : 1 }} onClick={() => setView(p.id)}>
                    <Table.Td ff="monospace">{p.number}</Table.Td>
                    <Table.Td>{formatDate(p.payment_date)}</Table.Td>
                    <Table.Td><Text size="sm" fw={500}>{p.customer_name}</Text><Text size="xs" c="dimmed">#{p.customer_code}</Text></Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(p.amount, decimalsOf(p.currency))} {p.currency}</Table.Td>
                    <Table.Td c="dimmed">{p.received_by}</Table.Td>
                    <Table.Td>{p.status === 'void' && <Badge color="gray">{t('invoices.statuses.void')}</Badge>}</Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </Paper>
      {view && <PaymentView id={view} canVoid={canManage} onClose={() => setView(null)} onChanged={load} />}
      <Group />
    </Stack>
  );
}
