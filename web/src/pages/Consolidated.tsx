import { useCallback, useEffect, useState } from 'react';
import { Alert, Button, Center, Group, Loader, MultiSelect, Paper, SegmentedControl, Select, Stack, Table, Text, TextInput, Title } from '@mantine/core';
import { useTranslation } from 'react-i18next';
import { api, type Currency } from '../api';
import { useCompany } from '../company';
import { errorText, formatAmount, today } from '../util';
import ExportMenu, { type ExportTable } from '../components/ExportMenu';

type Report = 'trial-balance' | 'profit-loss' | 'balance-sheet';

interface Result {
  report: Report;
  currency: string;
  companies: { id: string; name: string; base_currency: string; rate: string }[];
  rows: { code: string; name: string; type: string; amounts: Record<string, string>; total: string; debit?: string; credit?: string }[];
  totals: Record<string, string>;
  current_result?: Record<string, string>;
}

const yearStart = () => `${new Date().getFullYear()}-01-01`;

export default function Consolidated() {
  const { t } = useTranslation();
  const { companies, company } = useCompany();
  const [report, setReport] = useState<Report>('profit-loss');
  const [ids, setIds] = useState<string[]>([]);
  const [currency, setCurrency] = useState<string | null>(company?.base_currency ?? 'USD');
  const [from, setFrom] = useState(yearStart());
  const [to, setTo] = useState(today());
  const [data, setData] = useState<Result | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [currencies, setCurrencies] = useState<Currency[]>([]);

  useEffect(() => { api<Currency[]>('/currencies').then(setCurrencies).catch(() => {}); }, []);

  const run = useCallback(() => {
    setBusy(true);
    setError(null);
    const q = new URLSearchParams({ report, from, to, currency: currency ?? '', companies: ids.join(',') });
    api<Result>(`/reports/consolidated?${q}`)
      .then(setData)
      .catch((e) => { setData(null); setError(errorText(t, e)); })
      .finally(() => setBusy(false));
  }, [report, from, to, currency, ids, t]);
  useEffect(run, [run]);

  const label = (k: string) => t(`sec.cons_${k}`);
  const table = (): ExportTable | null => {
    if (!data) return null;
    return {
      title: `${t('sec.consTitle')} — ${label(report)}`,
      subtitle: `${report === 'balance-sheet' ? to : `${from} → ${to}`} · ${data.currency}`,
      columns: [{ label: t('accounts.code') }, { label: t('accounts.name') },
        ...data.companies.map((c) => ({ label: c.name, numeric: true })), { label: t('sec.total'), numeric: true }],
      rows: data.rows.map((r) => [r.code, r.name, ...data.companies.map((c) => r.amounts[c.id] ?? ''), r.total]),
      footer: Object.entries(data.totals).map(([k, v]) => ['', label(k), ...data.companies.map(() => ''), v]),
    };
  };

  return (
    <Stack gap="lg" maw={1300}>
      <Group justify="space-between">
        <Title order={2}>{t('sec.consTitle')}</Title>
        <ExportMenu table={table} disabled={!data} />
      </Group>
      <Text size="sm" c="dimmed">{t('sec.consHelp')}</Text>
      <Paper withBorder p="md">
        <Stack>
          <SegmentedControl value={report} onChange={(v) => setReport(v as Report)} data={[
            { value: 'profit-loss', label: label('profit-loss') }, { value: 'balance-sheet', label: label('balance-sheet') },
            { value: 'trial-balance', label: label('trial-balance') },
          ]} />
          <Group align="flex-end" wrap="wrap">
            <MultiSelect label={t('sec.companiesSel')} placeholder={t('sec.allCompanies')} value={ids} onChange={setIds} w={360}
              data={companies.map((c) => ({ value: c.id, label: c.name_en }))} clearable />
            <Select label={t('sec.currency')} value={currency} onChange={setCurrency} w={140} searchable allowDeselect={false}
              data={currencies.length ? currencies.map((c) => c.code) : [currency ?? 'USD']} />
            {report !== 'balance-sheet' && <TextInput type="date" label={t('reports.from')} value={from} onChange={(e) => setFrom(e.currentTarget.value)} />}
            <TextInput type="date" label={t('reports.to')} value={to} onChange={(e) => setTo(e.currentTarget.value)} />
            <Button variant="light" loading={busy} onClick={run}>{t('sec.refresh')}</Button>
          </Group>
        </Stack>
      </Paper>
      {error && <Alert color="red" variant="light">{error}</Alert>}
      {busy && !data && <Center p="lg"><Loader size="sm" /></Center>}
      {data && (
        <Paper withBorder>
          <Group gap="lg" p="sm">
            {data.companies.map((c) => (
              <Text key={c.id} size="xs" c="dimmed" dir="ltr">{c.name}: 1 {c.base_currency} = {c.rate} {data.currency}</Text>
            ))}
          </Group>
          <Table.ScrollContainer minWidth={600}>
            <Table verticalSpacing={6} striped>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>{t('accounts.code')}</Table.Th>
                  <Table.Th>{t('accounts.name')}</Table.Th>
                  {data.companies.map((c) => <Table.Th key={c.id} ta="end">{c.name}</Table.Th>)}
                  <Table.Th ta="end">{t('sec.total')}</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {data.rows.map((r) => (
                  <Table.Tr key={r.code + r.type}>
                    <Table.Td ff="monospace">{r.code}</Table.Td>
                    <Table.Td>{r.name}</Table.Td>
                    {data.companies.map((c) => <Table.Td key={c.id} ta="end" dir="ltr">{formatAmount(r.amounts[c.id])}</Table.Td>)}
                    <Table.Td ta="end" fw={600} dir="ltr">{formatAmount(r.total)}</Table.Td>
                  </Table.Tr>
                ))}
                {Object.entries(data.totals).map(([k, v]) => (
                  <Table.Tr key={k}>
                    <Table.Td />
                    <Table.Td fw={700}>{label(k)}</Table.Td>
                    {data.companies.map((c) => <Table.Td key={c.id} ta="end" dir="ltr">{k === 'current_result' && data.current_result ? formatAmount(data.current_result[c.id]) : ''}</Table.Td>)}
                    <Table.Td ta="end" fw={700} dir="ltr">{formatAmount(v)}</Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        </Paper>
      )}
    </Stack>
  );
}
