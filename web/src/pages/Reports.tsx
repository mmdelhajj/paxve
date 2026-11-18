import { useCallback, useEffect, useState } from 'react';
import { Badge, Center, Grid, Group, Loader, Paper, SimpleGrid, Stack, Table, Text, TextInput, Title } from '@mantine/core';
import { useTranslation } from 'react-i18next';
import { api } from '../api';
import { useCompany } from '../company';
import { accountName, formatAmount, formatDate, notifyError, today } from '../util';
import PageHeader from '../components/PageHeader';

interface AccRow { id: string; code: string; name: string; name_i18n: Record<string, string>; type: string; amount: string }

function useReport<T>(path: string | null) {
  const { t } = useTranslation();
  const [data, setData] = useState<T | null>(null);
  const load = useCallback(() => {
    if (!path) return;
    setData(null);
    api<T>(path).then(setData).catch((e) => notifyError(t, e));
  }, [path, t]);
  useEffect(load, [load]);
  return data;
}

function AmountRows({ rows, dec }: { rows: AccRow[]; dec: number }) {
  return (
    <Table verticalSpacing={4}>
      <Table.Tbody>
        {rows.map((r) => (
          <Table.Tr key={r.id}>
            <Table.Td><a href={`#/ledger/${r.id}`} style={{ color: 'inherit', textDecoration: 'none' }}>
              <span dir="ltr" style={{ fontFamily: 'monospace', color: 'var(--mantine-color-dimmed)' }}>{r.code}</span> {accountName(r)}
            </a></Table.Td>
            <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(r.amount, dec)}</Table.Td>
          </Table.Tr>
        ))}
      </Table.Tbody>
    </Table>
  );
}

function Total({ label, value, dec, strong, color }: { label: string; value: string; dec: number; strong?: boolean; color?: string }) {
  return (
    <Group justify="space-between" px="sm" py={6} style={{ borderTop: '1px solid var(--mantine-color-gray-3)' }}>
      <Text fw={strong ? 700 : 600}>{label}</Text>
      <Text fw={strong ? 800 : 600} ff="monospace" dir="ltr" c={color} size={strong ? 'lg' : 'sm'}>{formatAmount(value, dec)}</Text>
    </Group>
  );
}

export function ProfitLoss() {
  const { t } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [from, setFrom] = useState(`${new Date().getFullYear()}-01-01`);
  const [to, setTo] = useState(today());
  const d = useReport<{ rows: AccRow[]; total_income: string; total_expense: string; net: string }>(
    company ? `/companies/${company.id}/reports/profit-loss?from=${from}&to=${to}` : null);
  if (!company) return null;
  const dec = decimalsOf(company.base_currency);
  return (
    <Stack gap="lg" maw={900}>
      <PageHeader title={t('reports2.pl')} subtitle={`${company.name_en} · ${t('reports.amountsIn', { cur: company.base_currency })}`}>
        <TextInput type="date" size="xs" label={t('reports.from')} value={from} onChange={(e) => setFrom(e.currentTarget.value)} />
        <TextInput type="date" size="xs" label={t('reports.to')} value={to} onChange={(e) => setTo(e.currentTarget.value)} />
      </PageHeader>
      {!d ? <Center p="xl"><Loader size="sm" /></Center> : (
        <>
          <Paper withBorder>
            <Title order={5} p="sm" pb={0}>{t('reports2.income')}</Title>
            <AmountRows rows={d.rows.filter((r) => r.type === 'income')} dec={dec} />
            <Total label={t('reports2.totalIncome')} value={d.total_income} dec={dec} />
          </Paper>
          <Paper withBorder>
            <Title order={5} p="sm" pb={0}>{t('reports2.expenses')}</Title>
            <AmountRows rows={d.rows.filter((r) => r.type === 'expense')} dec={dec} />
            <Total label={t('reports2.totalExpense')} value={d.total_expense} dec={dec} />
          </Paper>
          <Paper withBorder>
            <Total label={Number(d.net) >= 0 ? t('reports2.profit') : t('reports2.loss')} value={d.net} dec={dec} strong
              color={Number(d.net) >= 0 ? 'teal.8' : 'red.7'} />
          </Paper>
        </>
      )}
    </Stack>
  );
}

export function BalanceSheet() {
  const { t } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [to, setTo] = useState(today());
  const d = useReport<{ rows: AccRow[]; total_assets: string; total_liabilities: string; total_equity: string; current_result: string; total_liabilities_equity: string }>(
    company ? `/companies/${company.id}/reports/balance-sheet?to=${to}` : null);
  if (!company) return null;
  const dec = decimalsOf(company.base_currency);
  return (
    <Stack gap="lg" maw={1100}>
      <PageHeader title={t('reports2.bs')} subtitle={`${company.name_en} · ${t('reports.amountsIn', { cur: company.base_currency })}`}>
        <TextInput type="date" size="xs" label={t('reports2.asOf')} value={to} onChange={(e) => setTo(e.currentTarget.value)} />
      </PageHeader>
      {!d ? <Center p="xl"><Loader size="sm" /></Center> : (
        <Grid>
          <Grid.Col span={{ base: 12, md: 6 }}>
            <Paper withBorder h="100%">
              <Title order={5} p="sm" pb={0}>{t('accounts.types.asset')}</Title>
              <AmountRows rows={d.rows.filter((r) => r.type === 'asset')} dec={dec} />
              <Total label={t('reports2.totalAssets')} value={d.total_assets} dec={dec} strong />
            </Paper>
          </Grid.Col>
          <Grid.Col span={{ base: 12, md: 6 }}>
            <Paper withBorder h="100%">
              <Title order={5} p="sm" pb={0}>{t('accounts.types.liability')}</Title>
              <AmountRows rows={d.rows.filter((r) => r.type === 'liability')} dec={dec} />
              <Total label={t('reports2.totalLiabilities')} value={d.total_liabilities} dec={dec} />
              <Title order={5} p="sm" pb={0}>{t('accounts.types.equity')}</Title>
              <AmountRows rows={d.rows.filter((r) => r.type === 'equity')} dec={dec} />
              <Group justify="space-between" px="sm" py={4}><Text size="sm">{t('reports2.currentResult')}</Text>
                <Text size="sm" ff="monospace" dir="ltr">{formatAmount(d.current_result, dec)}</Text></Group>
              <Total label={t('reports2.totalLE')} value={d.total_liabilities_equity} dec={dec} strong />
              {d.total_assets === d.total_liabilities_equity && <Group p="sm" pt={0} justify="flex-end"><Badge color="teal" variant="light">{t('journal.balanced')}</Badge></Group>}
            </Paper>
          </Grid.Col>
        </Grid>
      )}
    </Stack>
  );
}

interface AgingRow { customer_id: string; code: string; name: string; phone: string; area_name: string | null; currency: string;
  current: string; d30: string; d60: string; d90: string; over90: string; total: string; invoices: number; oldest_due: string }

export function Aging() {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [asOf, setAsOf] = useState(today());
  const d = useReport<{ rows: AgingRow[]; totals: AgingRow[] }>(company ? `/companies/${company.id}/reports/aging?as_of=${asOf}` : null);
  if (!company) return null;
  const buckets = ['current', 'd30', 'd60', 'd90', 'over90'] as const;
  return (
    <Stack gap="lg" maw={1200}>
      <PageHeader title={t('reports2.aging')} subtitle={company.name_en}>
        <TextInput type="date" size="xs" label={t('reports2.asOf')} value={asOf} onChange={(e) => setAsOf(e.currentTarget.value)} />
      </PageHeader>
      {!d ? <Center p="xl"><Loader size="sm" /></Center> : (
        <>
          <SimpleGrid cols={{ base: 2, sm: 5 }}>
            {d.totals.flatMap((tot) => buckets.map((b) => (
              <Paper withBorder p="sm" key={tot.currency + b}>
                <Text size="xs" c="dimmed">{t(`reports2.buckets.${b}`)}</Text>
                <Text fw={700} c={b === 'current' || !Number(tot[b]) ? undefined : b === 'over90' ? 'red.8' : 'orange.8'} dir="ltr">
                  {formatAmount(tot[b], decimalsOf(tot.currency))} {tot.currency}
                </Text>
              </Paper>
            )))}
          </SimpleGrid>
          <Paper withBorder>
            {d.rows.length === 0 ? <Text c="dimmed" ta="center" p="xl">{t('common.empty')}</Text> : (
              <Table.ScrollContainer minWidth={900}>
                <Table verticalSpacing="xs" highlightOnHover key={i18n.language}>
                  <Table.Thead>
                    <Table.Tr>
                      <Table.Th>{t('invoices.customer')}</Table.Th>
                      {buckets.map((b) => <Table.Th key={b} style={{ textAlign: 'end' }}>{t(`reports2.buckets.${b}`)}</Table.Th>)}
                      <Table.Th style={{ textAlign: 'end' }}>{t('invoices.total')}</Table.Th>
                    </Table.Tr>
                  </Table.Thead>
                  <Table.Tbody>
                    {d.rows.map((r) => {
                      const dec = decimalsOf(r.currency);
                      return (
                        <Table.Tr key={r.customer_id + r.currency} style={{ cursor: 'pointer' }} onClick={() => { window.location.hash = `#/customer/${r.customer_id}`; }}>
                          <Table.Td><Text size="sm" fw={500}>{r.name}</Text><Text size="xs" c="dimmed">#{r.code}{r.area_name ? ` · ${r.area_name}` : ''} · {formatDate(r.oldest_due)}</Text></Table.Td>
                          {buckets.map((b) => (
                            <Table.Td key={b} ff="monospace" dir="ltr" style={{ textAlign: 'end' }} c={Number(r[b]) > 0 && b !== 'current' ? (b === 'over90' ? 'red.8' : 'orange.8') : 'dimmed'}>
                              {Number(r[b]) ? formatAmount(r[b], dec) : '·'}
                            </Table.Td>
                          ))}
                          <Table.Td ff="monospace" dir="ltr" fw={700} style={{ textAlign: 'end' }}>{formatAmount(r.total, dec)} {r.currency}</Table.Td>
                        </Table.Tr>
                      );
                    })}
                  </Table.Tbody>
                </Table>
              </Table.ScrollContainer>
            )}
          </Paper>
        </>
      )}
    </Stack>
  );
}

interface ByUser { user_id: string | null; user_name: string; currency: string; payments: number; amount: string; customers: number }
interface ByDay { date: string; currency: string; payments: number; amount: string }

export function Collections() {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const now = new Date();
  const [from, setFrom] = useState(`${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}-01`);
  const [to, setTo] = useState(today());
  const d = useReport<{ by_user: ByUser[]; by_day: ByDay[] }>(company ? `/companies/${company.id}/reports/collections?from=${from}&to=${to}` : null);
  if (!company) return null;
  return (
    <Stack gap="lg" maw={1100}>
      <PageHeader title={t('reports2.collections')} subtitle={company.name_en}>
        <TextInput type="date" size="xs" label={t('reports.from')} value={from} onChange={(e) => setFrom(e.currentTarget.value)} />
        <TextInput type="date" size="xs" label={t('reports.to')} value={to} onChange={(e) => setTo(e.currentTarget.value)} />
      </PageHeader>
      {!d ? <Center p="xl"><Loader size="sm" /></Center> : (
        <Grid>
          <Grid.Col span={{ base: 12, md: 7 }}>
            <Paper withBorder>
              <Title order={5} p="sm">{t('reports2.byCollector')}</Title>
              <Table verticalSpacing="xs" key={i18n.language}>
                <Table.Thead><Table.Tr>
                  <Table.Th>{t('payments.receivedBy')}</Table.Th><Table.Th>{t('bill.payments')}</Table.Th>
                  <Table.Th>{t('bill.customers')}</Table.Th><Table.Th style={{ textAlign: 'end' }}>{t('payments.amount')}</Table.Th>
                </Table.Tr></Table.Thead>
                <Table.Tbody>
                  {d.by_user.map((u, i) => (
                    <Table.Tr key={i}>
                      <Table.Td fw={500}>{u.user_name}</Table.Td><Table.Td>{u.payments}</Table.Td><Table.Td>{u.customers}</Table.Td>
                      <Table.Td ff="monospace" dir="ltr" fw={600} style={{ textAlign: 'end' }}>{formatAmount(u.amount, decimalsOf(u.currency))} {u.currency}</Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </Paper>
          </Grid.Col>
          <Grid.Col span={{ base: 12, md: 5 }}>
            <Paper withBorder>
              <Title order={5} p="sm">{t('reports2.byDay')}</Title>
              <Table verticalSpacing={4}>
                <Table.Tbody>
                  {d.by_day.map((x, i) => (
                    <Table.Tr key={i}>
                      <Table.Td>{formatDate(x.date)}</Table.Td><Table.Td c="dimmed">{x.payments}</Table.Td>
                      <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(x.amount, decimalsOf(x.currency))} {x.currency}</Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </Paper>
          </Grid.Col>
        </Grid>
      )}
    </Stack>
  );
}
