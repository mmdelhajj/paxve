import { useCallback, useEffect, useState } from 'react';
import { Badge, Button, Center, Group, Loader, Paper, SegmentedControl, Select, SimpleGrid, Stack, Table, Text, TextInput, Title } from '@mantine/core';
import { useTranslation } from 'react-i18next';
import { can, api, currencyName, type Me, type Rate } from '../api';
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
  const canEdit = can(me, 'accounting.manage');
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
      <AutoRates canEdit={canEdit} onFetched={load} />
      {canEdit && (
        <Paper withBorder p="md">
          <form onSubmit={submit}>
            <Stack gap="sm">
              <Title order={5}>{t('rates.set')}</Title>
              <Group align="flex-end" wrap="wrap">
                <Select label={t('rates.currency')} w={200} searchable value={currency} onChange={setCurrency} allowDeselect={false}
                  data={currencies.filter((c) => c.code !== base).map((c) => ({ value: c.code, label: `${c.code} · ${currencyName(c, i18n.language)}` }))} />
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
                    <Table.Td><Badge variant="light" color={r.kind === 'official' ? 'teal' : 'gray'}>{t(`rates.kinds.${r.kind}`)}</Badge>
                      {r.source === 'auto' && <Badge ml={6} variant="outline" color="blue" size="xs">{t('t2.rates.auto')}</Badge>}</Table.Td>
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

interface RateSettings { auto_rates: boolean; manual_rate_currencies: string[]; auto_rates_at: string | null; base_currency: string; source: string }

/** automatic daily rates (published rates; the currencies kept by hand are never touched) */
function AutoRates({ canEdit, onFetched }: { canEdit: boolean; onFetched: () => void }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const [s, setS] = useState<RateSettings | null>(null);
  const [manual, setManual] = useState('');
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (!company) return;
    api<RateSettings>(`/companies/${company.id}/rates/settings`).then((x) => { setS(x); setManual(x.manual_rate_currencies.join(', ')); }).catch(() => {});
  }, [company]);
  if (!company || !s) return null;
  async function save(patch: Partial<RateSettings>) {
    if (!company) return;
    try {
      setS(await api<RateSettings>(`/companies/${company.id}/rates/settings`, { method: 'PUT', body: patch }));
      notifySaved(t);
    } catch (e) {
      notifyError(t, e);
    }
  }
  async function fetchNow() {
    if (!company) return;
    setBusy(true);
    try {
      const r = await api<{ updated: string[] }>(`/companies/${company.id}/rates/fetch`, { method: 'POST', body: {} });
      notifySaved(t);
      if (r.updated.length === 0) notifyError(t, new Error(t('t2.rates.nothing')));
      onFetched();
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Paper withBorder p="md">
      <Stack gap="xs">
        <Title order={5}>{t('t2.rates.title')}</Title>
        <Text size="sm" c="dimmed">{t('t2.rates.explain', { source: s.source })}</Text>
        <Group align="flex-end" wrap="wrap">
          <SegmentedControl disabled={!canEdit} value={s.auto_rates ? 'on' : 'off'} onChange={(v) => void save({ auto_rates: v === 'on' })}
            data={[{ value: 'off', label: t('t2.rates.off') }, { value: 'on', label: t('t2.rates.on') }]} />
          <TextInput label={t('t2.rates.manual')} w={260} dir="ltr" value={manual} disabled={!canEdit}
            onChange={(e) => setManual(e.currentTarget.value.toUpperCase())}
            onBlur={() => void save({ manual_rate_currencies: manual.split(/[\s,]+/).filter((x) => /^[A-Z]{3}$/.test(x)) })} />
          {canEdit && <Button variant="light" loading={busy} onClick={() => void fetchNow()}>{t('t2.rates.fetchNow')}</Button>}
          {s.auto_rates_at && <Text size="xs" c="dimmed">{t('t2.rates.last')}: {formatDate(s.auto_rates_at.slice(0, 10))}</Text>}
        </Group>
      </Stack>
    </Paper>
  );
}