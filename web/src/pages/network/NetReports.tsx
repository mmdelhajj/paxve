import { useEffect, useState } from 'react';
import { Center, Grid, Group, Loader, Paper, SimpleGrid, Stack, Table, Text, TextInput, Title } from '@mantine/core';
import { useTranslation } from 'react-i18next';
import { api } from '../../api';
import { useCompany } from '../../company';
import { formatAmount, notifyError, today } from '../../util';
import PageHeader from '../../components/PageHeader';

function monthStart() {
  const d = new Date();
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-01`;
}

function useData<T>(path: string | null) {
  const { t } = useTranslation();
  const [d, setD] = useState<T | null>(null);
  useEffect(() => {
    if (!path) return;
    setD(null);
    api<T>(path).then(setD).catch((e) => notifyError(t, e));
  }, [path, t]);
  return d;
}

interface VatRow { id: string; name: string; rate: string; collected: string; recoverable: string; taxable_sales: string; taxable_purchases: string }

export function VatReport() {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [from, setFrom] = useState(monthStart());
  const [to, setTo] = useState(today());
  const d = useData<{ base_currency: string; rows: VatRow[]; collected: string; recoverable: string; net: string }>(
    company ? `/companies/${company.id}/reports/vat?from=${from}&to=${to}` : null);
  if (!company) return null;
  const dec = decimalsOf(company.base_currency);
  return (
    <Stack gap="lg" maw={1000}>
      <PageHeader title={t('net.vatReport')} subtitle={`${company.name_en} · ${t('reports.amountsIn', { cur: company.base_currency })}`}>
        <TextInput type="date" size="xs" label={t('reports.from')} value={from} onChange={(e) => setFrom(e.currentTarget.value)} />
        <TextInput type="date" size="xs" label={t('reports.to')} value={to} onChange={(e) => setTo(e.currentTarget.value)} />
      </PageHeader>
      {!d ? <Center p="xl"><Loader size="sm" /></Center> : (
        <>
          <SimpleGrid cols={{ base: 1, sm: 3 }}>
            <Paper withBorder p="md"><Text size="xs" c="dimmed">{t('net.collected')}</Text><Text fw={700} size="xl" dir="ltr">{formatAmount(d.collected, dec)}</Text></Paper>
            <Paper withBorder p="md"><Text size="xs" c="dimmed">{t('net.recoverable')}</Text><Text fw={700} size="xl" dir="ltr">{formatAmount(d.recoverable, dec)}</Text></Paper>
            <Paper withBorder p="md"><Text size="xs" c="dimmed">{t('net.netVat')}</Text>
              <Text fw={800} size="xl" dir="ltr" c={Number(d.net) > 0 ? 'red.7' : 'teal.7'}>{formatAmount(d.net, dec)}</Text></Paper>
          </SimpleGrid>
          <Paper withBorder>
            <Table verticalSpacing="sm" key={i18n.language}>
              <Table.Thead><Table.Tr>
                <Table.Th>{t('net.taxName')}</Table.Th>
                <Table.Th style={{ textAlign: 'end' }}>{t('net.taxableSales')}</Table.Th><Table.Th style={{ textAlign: 'end' }}>{t('net.collected')}</Table.Th>
                <Table.Th style={{ textAlign: 'end' }}>{t('net.taxablePurchases')}</Table.Th><Table.Th style={{ textAlign: 'end' }}>{t('net.recoverable')}</Table.Th>
              </Table.Tr></Table.Thead>
              <Table.Tbody>
                {d.rows.map((r) => (
                  <Table.Tr key={r.id}>
                    <Table.Td fw={500}>{r.name} <Text span c="dimmed" size="xs" dir="ltr">({r.rate}%)</Text></Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(r.taxable_sales, 2)}</Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(r.collected, dec)}</Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(r.taxable_purchases, 2)}</Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(r.recoverable, dec)}</Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
            <Text size="xs" c="dimmed" p="sm">{t('net.vatHint')}</Text>
          </Paper>
        </>
      )}
    </Stack>
  );
}

interface GenRow {
  id: string; name: string; station_id: string; station_name: string; kva: string | null; hours: string; fuel_qty: string; fuel_cost: string;
  outages: number; outage_minutes: number;
}
interface StationRow { id: string; name: string; kwh_billed: string; fuel_cost: string; fuel_qty: string }

export function GeneratorReport() {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [from, setFrom] = useState(monthStart());
  const [to, setTo] = useState(today());
  const d = useData<{ generators: GenRow[]; stations: StationRow[] }>(company ? `/companies/${company.id}/reports/generators?from=${from}&to=${to}` : null);
  if (!company) return null;
  const dec = decimalsOf(company.base_currency);
  return (
    <Stack gap="lg" maw={1200}>
      <PageHeader title={t('net.genReport')} subtitle={`${company.name_en} · ${t('reports.amountsIn', { cur: company.base_currency })}`}>
        <TextInput type="date" size="xs" label={t('reports.from')} value={from} onChange={(e) => setFrom(e.currentTarget.value)} />
        <TextInput type="date" size="xs" label={t('reports.to')} value={to} onChange={(e) => setTo(e.currentTarget.value)} />
      </PageHeader>
      {!d ? <Center p="xl"><Loader size="sm" /></Center> : (
        <Grid>
          <Grid.Col span={{ base: 12, lg: 7 }}>
            <Paper withBorder>
              <Title order={5} p="sm">{t('net.generators')}</Title>
              <Table.ScrollContainer minWidth={640}>
                <Table verticalSpacing="xs" key={i18n.language}>
                  <Table.Thead><Table.Tr>
                    <Table.Th>{t('net.name')}</Table.Th><Table.Th style={{ textAlign: 'end' }}>{t('net.hours')}</Table.Th>
                    <Table.Th style={{ textAlign: 'end' }}>{t('net.fuel')}</Table.Th><Table.Th style={{ textAlign: 'end' }}>{t('net.perHour')}</Table.Th>
                    <Table.Th style={{ textAlign: 'end' }}>{t('net.fuelCost')}</Table.Th><Table.Th style={{ textAlign: 'end' }}>{t('net.outages')}</Table.Th>
                  </Table.Tr></Table.Thead>
                  <Table.Tbody>
                    {d.generators.map((g) => {
                      const h = Number(g.hours), q = Number(g.fuel_qty);
                      return (
                        <Table.Tr key={g.id}>
                          <Table.Td><Text size="sm" fw={500}>{g.name}</Text><Text size="xs" c="dimmed">{g.station_name}{g.kva ? ` · ${g.kva} kVA` : ''}</Text></Table.Td>
                          <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{h ? h : '·'}</Table.Td>
                          <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{q ? formatAmount(q, 0) : '·'}</Table.Td>
                          <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{h && q ? formatAmount(q / h, 2) : '·'}</Table.Td>
                          <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(g.fuel_cost, dec)}</Table.Td>
                          <Table.Td style={{ textAlign: 'end' }}>{g.outages ? `${g.outages} · ${g.outage_minutes} min` : '·'}</Table.Td>
                        </Table.Tr>
                      );
                    })}
                  </Table.Tbody>
                </Table>
              </Table.ScrollContainer>
            </Paper>
          </Grid.Col>
          <Grid.Col span={{ base: 12, lg: 5 }}>
            <Paper withBorder>
              <Title order={5} p="sm">{t('net.stations')}</Title>
              <Table verticalSpacing="xs">
                <Table.Thead><Table.Tr>
                  <Table.Th>{t('net.name')}</Table.Th><Table.Th style={{ textAlign: 'end' }}>{t('net.kwhBilled')}</Table.Th>
                  <Table.Th style={{ textAlign: 'end' }}>{t('net.costPerKwh')}</Table.Th>
                </Table.Tr></Table.Thead>
                <Table.Tbody>
                  {d.stations.map((s) => {
                    const k = Number(s.kwh_billed), c = Number(s.fuel_cost);
                    return (
                      <Table.Tr key={s.id}>
                        <Table.Td fw={500}>{s.name}</Table.Td>
                        <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{k ? formatAmount(k, 0) : '·'}</Table.Td>
                        <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{k && c ? formatAmount(c / k, 4) : '·'}</Table.Td>
                      </Table.Tr>
                    );
                  })}
                </Table.Tbody>
              </Table>
              <Group p="sm"><Text size="xs" c="dimmed">{t('net.genHint')}</Text></Group>
            </Paper>
          </Grid.Col>
        </Grid>
      )}
    </Stack>
  );
}
