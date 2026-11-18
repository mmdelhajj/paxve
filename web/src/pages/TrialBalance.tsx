import { useCallback, useEffect, useState } from 'react';
import { Badge, Button, Center, Group, Loader, Paper, Stack, Table, Text, TextInput } from '@mantine/core';
import { useTranslation } from 'react-i18next';
import { api } from '../api';
import { useCompany } from '../company';
import { formatAmount, notifyError, today } from '../util';
import PageHeader from '../components/PageHeader';
import ExportMenu from '../components/ExportMenu';

interface Row { account_id: string; code: string; name: string; type: string; debit: string; credit: string; balance: string }
interface TB { base_currency: string; rows: Row[]; total_debit: string; total_credit: string }

export default function TrialBalance() {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [from, setFrom] = useState(`${new Date().getFullYear()}-01-01`);
  const [to, setTo] = useState(today());
  const [data, setData] = useState<TB | null>(null);

  const load = useCallback(() => {
    if (!company) return;
    setData(null);
    api<TB>(`/companies/${company.id}/reports/trial-balance?from=${from}&to=${to}`).then(setData).catch((e) => notifyError(t, e));
  }, [company, from, to, t]);
  useEffect(load, [load]);

  if (!company) return null;
  const dec = decimalsOf(company.base_currency);

  return (
    <Stack gap="lg" maw={1100} className="print-area">
      <PageHeader title={t('reports.trialBalance')} subtitle={`${company.name_en} · ${t('reports.amountsIn', { cur: company.base_currency })} · ${from} → ${to}`}>
        <TextInput type="date" size="xs" label={t('reports.from')} value={from} onChange={(e) => setFrom(e.currentTarget.value)} />
        <TextInput type="date" size="xs" label={t('reports.to')} value={to} onChange={(e) => setTo(e.currentTarget.value)} />
        <ExportMenu disabled={!data?.rows.length} table={() => data && ({
          title: t('reports.trialBalance'), subtitle: `${company.name_en} · ${from} → ${to} · ${company.base_currency}`,
          columns: [{ label: t('reports.account') }, { label: t('reports.debit'), numeric: true }, { label: t('reports.credit'), numeric: true }, { label: t('reports.balance'), numeric: true }],
          rows: data.rows.map((r) => [`${r.code} · ${r.name}`, r.debit, r.credit, r.balance]),
          footer: [[t('reports.totals'), data.total_debit, data.total_credit, '']],
        })} />
      </PageHeader>
      <Paper withBorder>
        {data === null ? (
          <Center p="xl"><Loader size="sm" /></Center>
        ) : data.rows.length === 0 ? (
          <Text c="dimmed" ta="center" p="xl">{t('common.empty')}</Text>
        ) : (
          <Table.ScrollContainer minWidth={640}>
            <Table verticalSpacing="xs" highlightOnHover key={i18n.language}>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>{t('reports.account')}</Table.Th>
                  <Table.Th style={{ textAlign: 'end' }}>{t('reports.debit')}</Table.Th>
                  <Table.Th style={{ textAlign: 'end' }}>{t('reports.credit')}</Table.Th>
                  <Table.Th style={{ textAlign: 'end' }}>{t('reports.balance')}</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {data.rows.map((r) => (
                  <Table.Tr key={r.account_id}>
                    <Table.Td>
                      <Button variant="transparent" size="compact-sm" px={0} component="a" href={`#/ledger/${r.account_id}`}>
                        <span dir="ltr" style={{ fontFamily: 'monospace' }}>{r.code}</span>&nbsp;· {r.name}
                      </Button>
                    </Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(r.debit, dec)}</Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(r.credit, dec)}</Table.Td>
                    <Table.Td ff="monospace" dir="ltr" fw={500} style={{ textAlign: 'end' }}
                      c={r.balance.startsWith('-') ? 'red.7' : undefined}>{formatAmount(r.balance, dec)}</Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
              <Table.Tfoot>
                <Table.Tr>
                  <Table.Th>
                    <Group gap="xs">{t('reports.totals')}
                      {data.total_debit === data.total_credit && <Badge color="teal" variant="light">{t('journal.balanced')}</Badge>}
                    </Group>
                  </Table.Th>
                  <Table.Th ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(data.total_debit, dec)}</Table.Th>
                  <Table.Th ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(data.total_credit, dec)}</Table.Th>
                  <Table.Th />
                </Table.Tr>
              </Table.Tfoot>
            </Table>
          </Table.ScrollContainer>
        )}
      </Paper>
    </Stack>
  );
}
