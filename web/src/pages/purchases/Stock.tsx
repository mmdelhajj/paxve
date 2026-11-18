import { useCallback, useEffect, useState } from 'react';
import { Alert, Badge, Button, Center, Grid, Group, Loader, Paper, SegmentedControl, Select, Stack, Table, Text, TextInput, Title } from '@mantine/core';
import { IconGasStation } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { api, type Me } from '../../api';
import { useCompany } from '../../company';
import { formatAmount, formatDate, notifyError, notifySaved, today } from '../../util';
import PageHeader from '../../components/PageHeader';
import type { Item, StockDoc, Warehouse } from './types';

interface Totals { item_name: string; unit: string; kind: string; quantity: string; value: string }

export default function Stock({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const now = new Date();
  const [from, setFrom] = useState(`${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}-01`);
  const [to, setTo] = useState(today());
  const [data, setData] = useState<{ rows: StockDoc[]; totals: Totals[] } | null>(null);
  const [items, setItems] = useState<Item[]>([]);
  const [warehouses, setWarehouses] = useState<Warehouse[]>([]);
  const [kind, setKind] = useState<'fuel' | 'adjust'>('fuel');
  const [f, setF] = useState({ item_id: '', warehouse_id: '', quantity: '', unit_cost: '', notes: '', date: today() });
  const [busy, setBusy] = useState(false);
  const canEdit = me.role !== 'read_only';

  const load = useCallback(() => {
    if (!company) return;
    api<{ rows: StockDoc[]; totals: Totals[] }>(`/companies/${company.id}/stock/docs?from=${from}&to=${to}`).then(setData).catch((e) => notifyError(t, e));
    api<Item[]>(`/companies/${company.id}/items`).then((x) => setItems(x.filter((i) => i.kind === 'stock' && i.is_active))).catch(() => {});
  }, [company, from, to, t]);
  useEffect(load, [load]);
  useEffect(() => {
    if (company) api<Warehouse[]>(`/companies/${company.id}/warehouses`).then(setWarehouses).catch(() => {});
  }, [company]);
  if (!company) return null;
  const bdec = decimalsOf(company.base_currency);
  const sel = items.find((i) => i.id === f.item_id);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!company) return;
    setBusy(true);
    try {
      await api(`/companies/${company.id}/stock/${kind}`, { method: 'POST', body: f });
      notifySaved(t);
      setF({ ...f, quantity: '', unit_cost: '', notes: '' });
      load();
    } catch (err) {
      notifyError(t, err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Stack gap="lg" maw={1200}>
      <PageHeader title={t('pur.stock')} subtitle={company.name_en}>
        <TextInput type="date" size="sm" value={from} onChange={(e) => setFrom(e.currentTarget.value)} />
        <TextInput type="date" size="sm" value={to} onChange={(e) => setTo(e.currentTarget.value)} />
      </PageHeader>
      <Grid>
        {canEdit && (
          <Grid.Col span={{ base: 12, md: 5 }}>
            <Paper withBorder p="md">
              <form onSubmit={submit}>
                <Stack>
                  <SegmentedControl value={kind} onChange={(v) => setKind(v as 'fuel' | 'adjust')}
                    data={[{ value: 'fuel', label: t('pur.fuel') }, { value: 'adjust', label: t('pur.adjust') }]} />
                  <Text size="sm" c="dimmed">{kind === 'fuel' ? t('pur.fuelHint') : t('pur.adjustHint')}</Text>
                  <TextInput type="date" label={t('invoices.date')} value={f.date} onChange={(e) => setF({ ...f, date: e.currentTarget.value })} />
                  <Select label={t('pur.item')} required searchable data={items.map((i) => ({ value: i.id, label: `${i.name} · ${Number(i.quantity)} ${i.unit}` }))}
                    value={f.item_id || null} onChange={(v) => setF({ ...f, item_id: v ?? '' })} />
                  {warehouses.length > 1 && <Select label={t('pur.warehouse')} data={warehouses.map((w) => ({ value: w.id, label: w.name }))}
                    value={f.warehouse_id || null} placeholder={warehouses[0]?.name} onChange={(v) => setF({ ...f, warehouse_id: v ?? '' })} />}
                  <TextInput label={t('pur.quantity')} dir="ltr" required value={f.quantity} rightSection={<Text size="xs" c="dimmed">{sel?.unit}</Text>}
                    placeholder={kind === 'adjust' ? '-20 / +5' : ''}
                    onChange={(e) => setF({ ...f, quantity: e.currentTarget.value.replace(kind === 'adjust' ? /[^\d.\-]/g : /[^\d.]/g, '') })}
                    styles={{ input: { fontFamily: 'monospace', fontSize: 18 } }} />
                  {kind === 'adjust' && Number(f.quantity) > 0 && (
                    <TextInput label={`${t('pur.unitPrice')} (${company.base_currency}, ${t('common.optional')})`} dir="ltr" value={f.unit_cost}
                      placeholder={sel ? Number(sel.avg_cost).toString() : ''} onChange={(e) => setF({ ...f, unit_cost: e.currentTarget.value.replace(/[^\d.]/g, '') })} />
                  )}
                  {sel && Number(f.quantity) && (kind === 'fuel' || Number(f.quantity) < 0) ? (
                    <Alert color={Math.abs(Number(f.quantity)) > Number(sel.quantity) ? 'red' : 'gray'} variant="light" icon={<IconGasStation size={16} />}>
                      ≈ {formatAmount(Math.abs(Number(f.quantity)) * Number(sel.avg_cost), bdec)} {company.base_currency} · {t('pur.quantity')}: {Number(sel.quantity)} {sel.unit}
                    </Alert>
                  ) : null}
                  <TextInput label={t('pur.notes')} value={f.notes} onChange={(e) => setF({ ...f, notes: e.currentTarget.value })} />
                  <Button type="submit" loading={busy} disabled={!f.item_id || !Number(f.quantity)}>{t('pur.record')}</Button>
                </Stack>
              </form>
            </Paper>
          </Grid.Col>
        )}
        <Grid.Col span={{ base: 12, md: canEdit ? 7 : 12 }}>
          <Stack>
            {data && data.totals.length > 0 && (
              <Paper withBorder p="md">
                <Title order={6} mb={4}>{t('reports.totals')}</Title>
                {data.totals.map((x, i) => (
                  <Group key={i} justify="space-between">
                    <Text size="sm">{x.item_name} · {t(x.kind === 'fuel' ? 'pur.fuel' : 'pur.adjust')}</Text>
                    <Text size="sm" fw={600} dir="ltr">{Number(x.quantity)} {x.unit} · {formatAmount(x.value, bdec)} {company.base_currency}</Text>
                  </Group>
                ))}
              </Paper>
            )}
            <Paper withBorder>
              {data === null ? <Center p="xl"><Loader size="sm" /></Center> : data.rows.length === 0 ? <Text c="dimmed" ta="center" p="xl">{t('common.empty')}</Text> : (
                <Table.ScrollContainer minWidth={600}>
                  <Table verticalSpacing="xs" key={i18n.language}>
                    <Table.Tbody>
                      {data.rows.map((d) => (
                        <Table.Tr key={d.id}>
                          <Table.Td>{formatDate(d.doc_date)}</Table.Td>
                          <Table.Td><Badge variant="light" color={d.kind === 'fuel' ? 'orange' : 'blue'}>{t(d.kind === 'fuel' ? 'pur.fuel' : 'pur.adjust')} #{d.number}</Badge></Table.Td>
                          <Table.Td>{d.item_name}</Table.Td>
                          <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{Number(d.quantity)} {d.unit}</Table.Td>
                          <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(d.value, bdec)}</Table.Td>
                          <Table.Td c="dimmed">{d.notes}</Table.Td>
                        </Table.Tr>
                      ))}
                    </Table.Tbody>
                  </Table>
                </Table.ScrollContainer>
              )}
            </Paper>
          </Stack>
        </Grid.Col>
      </Grid>
    </Stack>
  );
}
