import { useCallback, useEffect, useState } from 'react';
import { Badge, Button, Center, Group, Loader, Paper, Select, Stack, Table, Text, TextInput } from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { useTranslation } from 'react-i18next';
import { api, type Me } from '../../api';
import { useCompany } from '../../company';
import { notifyError } from '../../util';
import PageHeader from '../../components/PageHeader';
import StationSelect from '../../components/StationSelect';
import { currentPeriod, type Area } from '../../billing';

interface Row {
  subscription_id: string; customer_id: string; code: string; name: string; area_name: string | null; meter_no: string;
  plan_name: string; unit_label: string; previous: string; current: string | null; invoiced: boolean;
}

export default function Readings({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const { company } = useCompany();
  const [period, setPeriod] = useState(currentPeriod());
  const [area, setArea] = useState<string | null>(null);
  const [station, setStation] = useState<string | null>(null);
  const [areas, setAreas] = useState<Area[]>([]);
  const [rows, setRows] = useState<Row[] | null>(null);
  const [values, setValues] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const canEdit = me.role !== 'read_only';

  const load = useCallback(() => {
    if (!company || !/^\d{4}-\d{2}$/.test(period)) return;
    setRows(null);
    api<Row[]>(`/companies/${company.id}/readings?period=${period}&area_id=${area ?? ''}&station_id=${station ?? ''}`).then((r) => {
      setRows(r);
      setValues(Object.fromEntries(r.map((x) => [x.subscription_id, x.current ? String(Number(x.current)) : ''])));
    }).catch((e) => notifyError(t, e));
  }, [company, period, area, station, t]);
  useEffect(load, [load]);
  useEffect(() => {
    if (company) api<Area[]>(`/companies/${company.id}/areas`).then(setAreas).catch(() => {});
  }, [company]);

  if (!company) return null;

  const changed = (rows ?? []).filter((r) => !r.invoiced && values[r.subscription_id] !== '' &&
    values[r.subscription_id] !== (r.current ? String(Number(r.current)) : ''));

  async function save() {
    if (!company) return;
    setBusy(true);
    try {
      const res = await api<{ saved: number }>(`/companies/${company.id}/readings`, {
        method: 'POST', body: { period, readings: changed.map((r) => ({ subscription_id: r.subscription_id, reading: values[r.subscription_id] })) },
      });
      notifications.show({ color: 'teal', message: t('readings.saved', { n: res.saved }) });
      load();
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Stack gap="lg" maw={1200}>
      <PageHeader title={t('readings.title')} subtitle={company.name_en}>
        <TextInput type="month" size="sm" value={period} onChange={(e) => setPeriod(e.currentTarget.value)} aria-label={t('readings.period')} />
        <StationSelect value={station} onChange={setStation} />
        <Select size="sm" placeholder={t('readings.allAreas')} clearable value={area} onChange={setArea} w={180}
          data={areas.map((a) => ({ value: a.id, label: a.name }))} />
        {canEdit && <Button loading={busy} disabled={changed.length === 0} onClick={save}>{t('readings.save')} {changed.length > 0 && `(${changed.length})`}</Button>}
      </PageHeader>
      <Paper withBorder>
        {rows === null ? <Center p="xl"><Loader size="sm" /></Center> : rows.length === 0 ? (
          <Text c="dimmed" ta="center" p="xl">{t('readings.empty')}</Text>
        ) : (
          <Table.ScrollContainer minWidth={760}>
            <Table verticalSpacing={6} highlightOnHover key={i18n.language}>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>{t('readings.customer')}</Table.Th>
                  <Table.Th>{t('readings.area')}</Table.Th>
                  <Table.Th>{t('readings.meter')}</Table.Th>
                  <Table.Th style={{ textAlign: 'end' }}>{t('readings.previous')}</Table.Th>
                  <Table.Th w={160}>{t('readings.current')}</Table.Th>
                  <Table.Th style={{ textAlign: 'end' }}>{t('readings.used')}</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {rows.map((r, idx) => {
                  const v = values[r.subscription_id] ?? '';
                  const used = v !== '' ? Number(v) - Number(r.previous) : null;
                  return (
                    <Table.Tr key={r.subscription_id}>
                      <Table.Td><Text size="sm" fw={500}>{r.name}</Text><Text size="xs" c="dimmed">#{r.code} · {r.plan_name}</Text></Table.Td>
                      <Table.Td c="dimmed">{r.area_name ?? '—'}</Table.Td>
                      <Table.Td ff="monospace">{r.meter_no || '—'}</Table.Td>
                      <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{Number(r.previous)}</Table.Td>
                      <Table.Td>
                        {r.invoiced ? <Group gap={6}><Text ff="monospace">{Number(r.current)}</Text><Badge size="xs" color="teal" variant="light">{t('readings.invoiced')}</Badge></Group> : (
                          <TextInput size="xs" dir="ltr" inputMode="decimal" value={v} disabled={!canEdit}
                            error={used !== null && used < 0 ? ' ' : undefined} styles={{ input: { fontFamily: 'monospace', fontSize: 15 } }}
                            data-row={idx}
                            onKeyDown={(e) => {
                              if (e.key === 'Enter') {
                                const next = document.querySelector<HTMLInputElement>(`input[data-row="${idx + 1}"]`);
                                next?.focus();
                              }
                            }}
                            onChange={(e) => { const val = e.currentTarget.value.replace(/[^\d.]/g, ''); setValues((m) => ({ ...m, [r.subscription_id]: val })); }} />
                        )}
                      </Table.Td>
                      <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }} c={used !== null && used < 0 ? 'red' : undefined}>
                        {used !== null ? `${Math.round(used * 100) / 100} ${r.unit_label}` : ''}
                      </Table.Td>
                    </Table.Tr>
                  );
                })}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </Paper>
    </Stack>
  );
}
