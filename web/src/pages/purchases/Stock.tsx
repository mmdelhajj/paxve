import { useCallback, useEffect, useState } from 'react';
import { Alert, Badge, Button, Center, Grid, Group, Loader, Modal, Paper, SegmentedControl, Select, Stack, Table, Text, TextInput, Title } from '@mantine/core';
import { IconGasStation } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { api, type Me } from '../../api';
import { useCompany } from '../../company';
import { formatAmount, formatDate, notifyError, notifySaved, today } from '../../util';
import PageHeader, { Confirm } from '../../components/PageHeader';
import type { Item, StockDoc, StockTransfer, Warehouse } from './types';

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
  const [kind, setKind] = useState<'fuel' | 'adjust' | 'transfer'>('fuel');
  const [f, setF] = useState({ item_id: '', warehouse_id: '', quantity: '', unit_cost: '', notes: '', date: today(), generator_id: '', to_warehouse_id: '' });
  const [transfers, setTransfers] = useState<StockTransfer[]>([]);
  const [newWh, setNewWh] = useState<string | null>(null);
  const [voiding, setVoiding] = useState<{ path: string } | null>(null);
  const canVoid = me.role === 'admin' || me.role === 'accountant';
  const [generators, setGenerators] = useState<{ id: string; name: string; station_name: string; fuel_item_id: string | null; status: string }[]>([]);
  const [busy, setBusy] = useState(false);
  const canEdit = me.role !== 'read_only';

  const load = useCallback(() => {
    if (!company) return;
    api<{ rows: StockDoc[]; totals: Totals[] }>(`/companies/${company.id}/stock/docs?from=${from}&to=${to}`).then(setData).catch((e) => notifyError(t, e));
    api<Item[]>(`/companies/${company.id}/items`).then((x) => setItems(x.filter((i) => i.kind === 'stock' && i.is_active))).catch(() => {});
    api<StockTransfer[]>(`/companies/${company.id}/stock/transfers?from=${from}&to=${to}`).then(setTransfers).catch(() => {});
  }, [company, from, to, t]);
  useEffect(load, [load]);
  useEffect(() => {
    if (company) api<Warehouse[]>(`/companies/${company.id}/warehouses`).then(setWarehouses).catch(() => {});
    if (company) api<{ id: string; name: string; station_name: string; fuel_item_id: string | null; status: string }[]>(`/companies/${company.id}/generators`)
      .then((g) => setGenerators(g.filter((x) => x.status !== 'retired'))).catch(() => {});
  }, [company]);
  if (!company) return null;
  const bdec = decimalsOf(company.base_currency);
  const sel = items.find((i) => i.id === f.item_id);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!company) return;
    setBusy(true);
    try {
      if (kind === 'transfer') {
        await api(`/companies/${company.id}/stock/transfers`, { method: 'POST', body: {
          item_id: f.item_id, from_warehouse_id: f.warehouse_id || warehouses[0]?.id, to_warehouse_id: f.to_warehouse_id,
          quantity: f.quantity, date: f.date, notes: f.notes } });
      } else {
        await api(`/companies/${company.id}/stock/${kind}`, { method: 'POST', body: kind === 'fuel' ? f : { ...f, generator_id: '' } });
      }
      notifySaved(t);
      setF({ ...f, quantity: '', unit_cost: '', notes: '' });
      load();
    } catch (err) {
      notifyError(t, err);
    } finally {
      setBusy(false);
    }
  }

  async function doVoid(path: string) {
    if (!company) return;
    try {
      await api(`/companies/${company.id}${path}`, { method: 'POST', body: {} });
      notifySaved(t);
      load();
    } catch (err) {
      notifyError(t, err);
    } finally {
      setVoiding(null);
    }
  }
  const fromWh = f.warehouse_id || warehouses[0]?.id || '';

  return (
    <Stack gap="lg" maw={1200}>
      <PageHeader title={t('pur.stock')} subtitle={company.name_en}>
        <TextInput type="date" size="sm" value={from} onChange={(e) => setFrom(e.currentTarget.value)} />
        <TextInput type="date" size="sm" value={to} onChange={(e) => setTo(e.currentTarget.value)} />
        {canVoid && <Button variant="default" size="sm" onClick={() => setNewWh('')}>{t('gap.newWarehouse')}</Button>}
      </PageHeader>
      {newWh !== null && (
        <Modal opened onClose={() => setNewWh(null)} title={t('gap.newWarehouse')}>
          <form onSubmit={async (e) => {
            e.preventDefault();
            try {
              await api(`/companies/${company.id}/warehouses`, { method: 'POST', body: { name: newWh } });
              notifySaved(t);
              setNewWh(null);
              api<Warehouse[]>(`/companies/${company.id}/warehouses`).then(setWarehouses).catch(() => {});
            } catch (err) { notifyError(t, err); }
          }}>
            <Stack>
              <Text size="sm" c="dimmed">{warehouses.map((w) => w.name).join(' · ')}</Text>
              <TextInput label={t('pur.warehouse')} required value={newWh} onChange={(e) => setNewWh(e.currentTarget.value)} data-autofocus />
              <Group justify="flex-end"><Button type="submit" disabled={!newWh.trim()}>{t('common.save')}</Button></Group>
            </Stack>
          </form>
        </Modal>
      )}
      <Grid>
        {canEdit && (
          <Grid.Col span={{ base: 12, md: 5 }}>
            <Paper withBorder p="md">
              <form onSubmit={submit}>
                <Stack>
                  <SegmentedControl value={kind} onChange={(v) => setKind(v as 'fuel' | 'adjust' | 'transfer')}
                    data={[{ value: 'fuel', label: t('pur.fuel') }, { value: 'adjust', label: t('pur.adjust') },
                      ...(warehouses.length > 1 ? [{ value: 'transfer', label: t('gap.transfer') }] : [])]} />
                  <Text size="sm" c="dimmed">{kind === 'fuel' ? t('pur.fuelHint') : kind === 'adjust' ? t('pur.adjustHint') : t('gap.transferHint')}</Text>
                  <TextInput type="date" label={t('invoices.date')} value={f.date} onChange={(e) => setF({ ...f, date: e.currentTarget.value })} />
                  {kind === 'fuel' && generators.length > 0 && (
                    <Select label={t('net.generator')} clearable value={f.generator_id || null}
                      data={generators.map((g) => ({ value: g.id, label: `${g.name} · ${g.station_name}` }))}
                      onChange={(v) => { const g = generators.find((x) => x.id === v); setF({ ...f, generator_id: v ?? '', item_id: g?.fuel_item_id ?? f.item_id }); }} />
                  )}
                  <Select label={t('pur.item')} required searchable data={items.map((i) => ({ value: i.id, label: `${i.name} · ${Number(i.quantity)} ${i.unit}` }))}
                    value={f.item_id || null} onChange={(v) => setF({ ...f, item_id: v ?? '' })} />
                  {warehouses.length > 1 && <Select label={kind === 'transfer' ? t('gap.fromWarehouse') : t('pur.warehouse')} data={warehouses.map((w) => ({ value: w.id, label: w.name }))}
                    value={f.warehouse_id || null} placeholder={warehouses[0]?.name} onChange={(v) => setF({ ...f, warehouse_id: v ?? '' })} />}
                  {kind === 'transfer' && <Select label={t('gap.toWarehouse')} required data={warehouses.filter((w) => w.id !== fromWh).map((w) => ({ value: w.id, label: w.name }))}
                    value={f.to_warehouse_id || null} onChange={(v) => setF({ ...f, to_warehouse_id: v ?? '' })} />}
                  <TextInput label={t('pur.quantity')} dir="ltr" required value={f.quantity} rightSection={<Text size="xs" c="dimmed">{sel?.unit}</Text>}
                    placeholder={kind === 'adjust' ? '-20 / +5' : ''}
                    onChange={(e) => setF({ ...f, quantity: e.currentTarget.value.replace(kind === 'adjust' ? /[^\d.\-]/g : /[^\d.]/g, '') })}
                    styles={{ input: { fontFamily: 'monospace', fontSize: 18 } }} />
                  {kind === 'adjust' && Number(f.quantity) > 0 && (
                    <TextInput label={`${t('pur.unitPrice')} (${company.base_currency}, ${t('common.optional')})`} dir="ltr" value={f.unit_cost}
                      placeholder={sel ? Number(sel.avg_cost).toString() : ''} onChange={(e) => setF({ ...f, unit_cost: e.currentTarget.value.replace(/[^\d.]/g, '') })} />
                  )}
                  {sel && Number(f.quantity) && (kind !== 'adjust' || Number(f.quantity) < 0) ? (
                    <Alert color={Math.abs(Number(f.quantity)) > Number(sel.quantity) ? 'red' : 'gray'} variant="light" icon={<IconGasStation size={16} />}>
                      ≈ {formatAmount(Math.abs(Number(f.quantity)) * Number(sel.avg_cost), bdec)} {company.base_currency} · {t('pur.quantity')}: {Number(sel.quantity)} {sel.unit}
                    </Alert>
                  ) : null}
                  <TextInput label={t('pur.notes')} value={f.notes} onChange={(e) => setF({ ...f, notes: e.currentTarget.value })} />
                  <Button type="submit" loading={busy} disabled={!f.item_id || !Number(f.quantity) || (kind === 'transfer' && !f.to_warehouse_id)}>{t('pur.record')}</Button>
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
                        <Table.Tr key={d.id} style={{ opacity: d.status === 'void' ? 0.5 : 1 }}>
                          <Table.Td>{formatDate(d.doc_date)}</Table.Td>
                          <Table.Td><Badge variant="light" color={d.kind === 'fuel' ? 'orange' : 'blue'}>{t(d.kind === 'fuel' ? 'pur.fuel' : 'pur.adjust')} #{d.number}</Badge></Table.Td>
                          <Table.Td>{d.item_name}<Text size="xs" c="dimmed">{d.warehouse}</Text></Table.Td>
                          <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{Number(d.quantity)} {d.unit}</Table.Td>
                          <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(d.value, bdec)}</Table.Td>
                          <Table.Td c="dimmed">{d.notes}</Table.Td>
                          <Table.Td>{d.status === 'void' ? <Badge color="gray">{t('invoices.statuses.void')}</Badge>
                            : canVoid && <Button size="compact-xs" variant="subtle" color="red" onClick={() => setVoiding({ path: `/stock/docs/${d.id}/void` })}>{t('gap.void')}</Button>}</Table.Td>
                        </Table.Tr>
                      ))}
                    </Table.Tbody>
                  </Table>
                </Table.ScrollContainer>
              )}
            </Paper>
            {transfers.length > 0 && (
              <Paper withBorder>
                <Title order={6} p="sm" pb={0}>{t('gap.transfers')}</Title>
                <Table.ScrollContainer minWidth={600}>
                  <Table verticalSpacing="xs" key={i18n.language}>
                    <Table.Tbody>
                      {transfers.map((x) => (
                        <Table.Tr key={x.id} style={{ opacity: x.status === 'void' ? 0.5 : 1 }}>
                          <Table.Td>{formatDate(x.transfer_date)}</Table.Td>
                          <Table.Td><Badge variant="light" color="grape">{t('gap.transfer')} #{x.number}</Badge></Table.Td>
                          <Table.Td>{x.item_name}<Text size="xs" c="dimmed">{x.from_name} → {x.to_name}</Text></Table.Td>
                          <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{Number(x.quantity)} {x.unit}</Table.Td>
                          <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(x.value, bdec)}</Table.Td>
                          <Table.Td>{x.status === 'void' ? <Badge color="gray">{t('invoices.statuses.void')}</Badge>
                            : canVoid && <Button size="compact-xs" variant="subtle" color="red" onClick={() => setVoiding({ path: `/stock/transfers/${x.id}/void` })}>{t('gap.void')}</Button>}</Table.Td>
                        </Table.Tr>
                      ))}
                    </Table.Tbody>
                  </Table>
                </Table.ScrollContainer>
              </Paper>
            )}
            <Group justify="flex-end"><Button variant="subtle" size="xs" component="a" href="#/stock-moves">{t('gap.moves')} →</Button></Group>
          </Stack>
        </Grid.Col>
      </Grid>
      {voiding && <Confirm text={t('gap.voidConfirm')} yesLabel={t('gap.void')} noLabel={t('common.cancel')} onYes={() => doVoid(voiding.path)} onNo={() => setVoiding(null)} />}
    </Stack>
  );
}
