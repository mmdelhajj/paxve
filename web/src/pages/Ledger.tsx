import { useCallback, useEffect, useState } from 'react';
import { Anchor, Center, Loader, Paper, Stack, Table, Text, TextInput } from '@mantine/core';
import { useTranslation } from 'react-i18next';
import { api, type Account } from '../api';
import { useCompany } from '../company';
import { accountLabel, formatAmount, formatDate, notifyError, today } from '../util';
import PageHeader from '../components/PageHeader';

interface Row {
  entry_id: string; number: number; date: string; memo: string; description: string; currency: string;
  debit: string; credit: string; base_debit: string; base_credit: string; balance: string;
}
interface LedgerData { base_currency: string; opening: string; rows: Row[] }

export default function Ledger({ accountId }: { accountId: string }) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [account, setAccount] = useState<Account | null>(null);
  const [from, setFrom] = useState(`${new Date().getFullYear()}-01-01`);
  const [to, setTo] = useState(today());
  const [data, setData] = useState<LedgerData | null>(null);

  useEffect(() => {
    if (!company) return;
    api<Account[]>(`/companies/${company.id}/accounts`).then((list) => setAccount(list.find((a) => a.id === accountId) ?? null)).catch(() => {});
  }, [company, accountId]);

  const load = useCallback(() => {
    if (!company || !accountId) return;
    setData(null);
    api<LedgerData>(`/companies/${company.id}/reports/ledger/${accountId}?from=${from}&to=${to}`).then(setData).catch((e) => notifyError(t, e));
  }, [company, accountId, from, to, t]);
  useEffect(load, [load]);

  if (!company) return null;
  const dec = decimalsOf(company.base_currency);

  return (
    <Stack gap="lg" maw={1200}>
      <PageHeader title={account ? accountLabel(account) : t('reports.ledger')}
        subtitle={`${t('reports.ledger')} · ${t('reports.amountsIn', { cur: company.base_currency })}`}>
        <TextInput type="date" size="xs" label={t('reports.from')} value={from} onChange={(e) => setFrom(e.currentTarget.value)} />
        <TextInput type="date" size="xs" label={t('reports.to')} value={to} onChange={(e) => setTo(e.currentTarget.value)} />
      </PageHeader>
      <Paper withBorder>
        {data === null ? (
          <Center p="xl"><Loader size="sm" /></Center>
        ) : (
          <Table.ScrollContainer minWidth={820}>
            <Table verticalSpacing="xs" highlightOnHover key={i18n.language}>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th w={120}>{t('journal.date')}</Table.Th>
                  <Table.Th w={70}>{t('journal.number')}</Table.Th>
                  <Table.Th>{t('journal.memo')}</Table.Th>
                  <Table.Th style={{ textAlign: 'end' }}>{t('reports.debit')}</Table.Th>
                  <Table.Th style={{ textAlign: 'end' }}>{t('reports.credit')}</Table.Th>
                  <Table.Th style={{ textAlign: 'end' }}>{t('reports.balance')}</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                <Table.Tr>
                  <Table.Td colSpan={5}><Text size="sm" c="dimmed">{t('reports.opening')}</Text></Table.Td>
                  <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(data.opening, dec)}</Table.Td>
                </Table.Tr>
                {data.rows.map((r, i) => (
                  <Table.Tr key={i}>
                    <Table.Td>{formatDate(r.date)}</Table.Td>
                    <Table.Td><Anchor href={`#/journal/${r.entry_id}`} ff="monospace">{r.number}</Anchor></Table.Td>
                    <Table.Td>
                      <Text size="sm">{r.memo}</Text>
                      {(r.description || r.currency !== company.base_currency) && (
                        <Text size="xs" c="dimmed">
                          {r.description}
                          {r.currency !== company.base_currency && <span dir="ltr"> ({formatAmount(r.debit !== '0.0000' ? r.debit : r.credit, decimalsOf(r.currency))} {r.currency})</span>}
                        </Text>
                      )}
                    </Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{r.base_debit !== '0.0000' ? formatAmount(r.base_debit, dec) : ''}</Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{r.base_credit !== '0.0000' ? formatAmount(r.base_credit, dec) : ''}</Table.Td>
                    <Table.Td ff="monospace" dir="ltr" fw={500} style={{ textAlign: 'end' }}>{formatAmount(r.balance, dec)}</Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </Paper>
    </Stack>
  );
}
