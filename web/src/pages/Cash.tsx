import { useCallback, useEffect, useState } from 'react';
import { Badge, Button, Center, Group, Loader, Paper, Select, SimpleGrid, Stack, Table, Text, TextInput, Title } from '@mantine/core';
import { IconArrowRight } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { can, api, type Me } from '../api';
import { useCompany } from '../company';
import { formatAmount, formatDate, notifyError, notifySaved, today } from '../util';
import PageHeader from '../components/PageHeader';

interface Box { id: string; name: string; kind: string; currency: string; user_id: string | null; user_name: string | null; account_code: string; balance: string }
interface Transfer { id: string; number: number; transfer_date: string; from_method: string; to_method: string; currency: string; amount: string; notes: string; created_by: string }

export default function Cash({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [boxes, setBoxes] = useState<Box[] | null>(null);
  const [transfers, setTransfers] = useState<Transfer[]>([]);
  const [from, setFrom] = useState<string | null>(null);
  const [to, setTo] = useState<string | null>(null);
  const [amount, setAmount] = useState('');
  const [notes, setNotes] = useState('');
  const [busy, setBusy] = useState(false);
  const canMove = can(me, 'collections.edit');

  const load = useCallback(() => {
    if (!company) return;
    api<Box[]>(`/companies/${company.id}/cash/balances`).then(setBoxes).catch((e) => notifyError(t, e));
    api<Transfer[]>(`/companies/${company.id}/cash/transfers?from=${new Date().getFullYear()}-01-01`).then(setTransfers).catch(() => {});
  }, [company, t]);
  useEffect(load, [load]);

  if (!company) return null;
  const fromBox = boxes?.find((b) => b.id === from);

  async function move() {
    if (!company) return;
    setBusy(true);
    try {
      await api(`/companies/${company.id}/cash/transfers`, { method: 'POST', body: { from_method_id: from, to_method_id: to, amount, notes, date: today() } });
      notifySaved(t);
      setAmount('');
      setNotes('');
      load();
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Stack gap="lg" maw={1100}>
      <PageHeader title={t('cash.title')} subtitle={company.name_en} />
      {boxes === null ? <Center p="xl"><Loader size="sm" /></Center> : (
        <SimpleGrid cols={{ base: 1, xs: 2, md: 4 }}>
          {boxes.map((b) => (
            <Paper withBorder p="md" key={b.id}>
              <Group justify="space-between" wrap="nowrap">
                <Text fw={500} truncate>{b.name}</Text>
                {b.user_name && <Badge size="xs" variant="light">{b.user_name}</Badge>}
              </Group>
              <Text size="1.4rem" fw={700} dir="ltr" c={Number(b.balance) < 0 ? 'red.7' : undefined}>{formatAmount(b.balance, decimalsOf(b.currency))} {b.currency}</Text>
              <Text size="xs" c="dimmed">{t(`methods.kinds.${b.kind}`)} · <span dir="ltr">{b.account_code}</span></Text>
            </Paper>
          ))}
        </SimpleGrid>
      )}
      {canMove && boxes && (
        <Paper withBorder p="md">
          <Title order={5} mb="sm">{t('cash.transfer')}</Title>
          <Group align="flex-end" wrap="wrap">
            <Select label={t('cash.from')} w={220} value={from} onChange={(v) => { setFrom(v); setTo(null); }}
              data={boxes.map((b) => ({ value: b.id, label: `${b.name} (${b.currency})` }))} />
            <IconArrowRight style={{ marginBottom: 8, transform: document.dir === 'rtl' ? 'scaleX(-1)' : undefined }} />
            <Select label={t('cash.to')} w={220} value={to} onChange={setTo}
              data={boxes.filter((b) => b.id !== from && (!fromBox || b.currency === fromBox.currency)).map((b) => ({ value: b.id, label: `${b.name} (${b.currency})` }))} />
            <TextInput label={t('invoices.amount')} dir="ltr" w={160} value={amount} onChange={(e) => setAmount(e.currentTarget.value.replace(/[^\d.]/g, ''))}
              rightSection={<Text size="xs" c="dimmed">{fromBox?.currency}</Text>} />
            <TextInput label={t('customers.notes')} w={200} value={notes} onChange={(e) => setNotes(e.currentTarget.value)} />
            <Button loading={busy} disabled={!from || !to || !amount} onClick={move}>{t('cash.move')}</Button>
          </Group>
          {fromBox && (
            <Button variant="subtle" size="xs" mt={6} onClick={() => setAmount(String(Number(fromBox.balance)))}>
              {t('cash.all')}: <span dir="ltr">&nbsp;{formatAmount(fromBox.balance, decimalsOf(fromBox.currency))} {fromBox.currency}</span>
            </Button>
          )}
        </Paper>
      )}
      <Paper withBorder>
        <Title order={5} p="sm">{t('cash.history')}</Title>
        {transfers.length === 0 ? <Text c="dimmed" ta="center" p="lg">{t('common.empty')}</Text> : (
          <Table.ScrollContainer minWidth={640}>
            <Table verticalSpacing="xs" key={i18n.language}>
              <Table.Tbody>
                {transfers.map((x) => (
                  <Table.Tr key={x.id}>
                    <Table.Td ff="monospace">{x.number}</Table.Td>
                    <Table.Td>{formatDate(x.transfer_date)}</Table.Td>
                    <Table.Td>{x.from_method} → {x.to_method}</Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(x.amount, decimalsOf(x.currency))} {x.currency}</Table.Td>
                    <Table.Td c="dimmed">{x.notes}</Table.Td>
                    <Table.Td c="dimmed">{x.created_by}</Table.Td>
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
