import { useCallback, useEffect, useState } from 'react';
import { Badge, Button, Center, Group, Loader, Paper, SegmentedControl, Select, SimpleGrid, Stack, Table, Text, TextInput, Title } from '@mantine/core';
import { useTranslation } from 'react-i18next';
import { api, type Me, type Rate } from '../api';
import { useCompany } from '../company';
import { formatAmount, formatDate, notifyError, notifySaved, today } from '../util';
import PageHeader from '../components/PageHeader';

export default function Rates({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const { company, currencies } = useCompany();
  const [latest, setLatest] = useState<Rate[]>([]);
  const [history, setHistory] = useState<Rate[] | null>(null);
  const [currency, setCurrency] = useState<string | null>(null);
  const [date, setDate] = useState(today());
  const [kind, setKind] = useState('official');
  const [value, setValue] = useState('');
  const [busy, setBusy] = useState(false);
  const canEdit = me.role === 'admin' || me.role === 'accountant';
  const base = company?.base_currency ?? 'USD';

  const load = useCallback(() => {
    if (!company) return;
    api<{ rates: Rate[] }>(`/companies/${company.id}/rates/latest`).then((r) => setLatest(r.rates)).catch(() => {});
    api<Rate[]>(`/companies/${company.id}/rates`).then(setHistory).catch((e) => notifyError(t, e));
  }, [company, t]);
  useEffect(load, [load]);

  useEffect(() => {
    if (company && !currency) setCurrency(company.local_currency !== company.base_currency ? company.local_currency : 'EUR');
  }, [company, currency]);

  if (!company) return null;

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!company) return;
    setBusy(true);
    try {
      await api(`/companies/${company.id}/rates`, { method: 'POST', body: { currency, date, kind, rate: value } });
      notifySaved(t);
      setValue('');
      load();
    } catch (err) {
      notifyError(t, err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Stack gap="lg" maw={1100}>
      <PageHeader title={t('rates.title')} subtitle={company.name_en} />
      {latest.length > 0 && (
        <SimpleGrid cols={{ base: 2, sm: 4 }}>
          {latest.map((r) => (
            <Paper key={r.id} withBorder p="md">
              <Text size="xs" c="dimmed">1 {base} =</Text>
              <Text size="xl" fw={600} ff="monospace" dir="ltr">{formatAmount(r.rate, Number(r.rate) < 10 ? 4 : 0)}</Text>
              <Group justify="space-between"><Text fw={500}>{r.currency}</Text><Text size="xs" c="dimmed">{formatDate(r.date)}</Text></Group>
            </Paper>
          ))}
        </SimpleGrid>
      )}
      {canEdit && (
        <Paper withBorder p="md">
          <form onSubmit={submit}>
            <Stack gap="sm">
              <Title order={5}>{t('rates.set')}</Title>
              <Group align="flex-end" wrap="wrap">
                <Select label={t('rates.currency')} w={200} searchable value={currency} onChange={setCurrency} allowDeselect={false}
                  data={currencies.filter((c) => c.code !== base).map((c) => ({ value: c.code, label: `${c.code} · ${c.name}` }))} />
                <TextInput type="date" label={t('rates.date')} value={date} onChange={(e) => setDate(e.currentTarget.value)} w={170} />
                <div>
                  <Text size="sm" fw={500} mb={4}>{t('rates.kind')}</Text>
                  <SegmentedControl value={kind} onChange={setKind} data={['official', 'buy', 'sell'].map((k) => ({ value: k, label: t(`rates.kinds.${k}`) }))} />
                </div>
                <TextInput label={t('rates.explain', { base, cur: currency ?? '' })} dir="ltr" inputMode="decimal" required w={220}
                  value={value} onChange={(e) => setValue(e.currentTarget.value.replace(/[^\d.]/g, ''))}
                  placeholder={currency === 'LBP' ? '89500' : ''} styles={{ input: { fontFamily: 'monospace' } }} />
                <Button type="submit" loading={busy} disabled={!value || !currency}>{t('common.save')}</Button>
              </Group>
            </Stack>
          </form>
        </Paper>
      )}
      <Paper withBorder>
        <Text fw={500} p="md" pb={0}>{t('rates.history')}</Text>
        {history === null ? (
          <Center p="xl"><Loader size="sm" /></Center>
        ) : history.length === 0 ? (
          <Text c="dimmed" ta="center" p="xl">{t('rates.none')}</Text>
        ) : (
          <Table.ScrollContainer minWidth={500}>
            <Table verticalSpacing="xs" key={i18n.language}>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>{t('rates.date')}</Table.Th>
                  <Table.Th>{t('rates.currency')}</Table.Th>
                  <Table.Th>{t('rates.kind')}</Table.Th>
                  <Table.Th style={{ textAlign: 'end' }}>1 {base} =</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {history.map((r) => (
                  <Table.Tr key={r.id}>
                    <Table.Td>{formatDate(r.date)}</Table.Td>
                    <Table.Td fw={500}>{r.currency}</Table.Td>
                    <Table.Td><Badge variant="light" color={r.kind === 'official' ? 'teal' : 'gray'}>{t(`rates.kinds.${r.kind}`)}</Badge></Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{r.rate}</Table.Td>
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
