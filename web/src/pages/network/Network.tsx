import { useCallback, useEffect, useState } from 'react';
import {
  Badge, Button, Center, Grid, Group, Loader, Modal, Paper, Select, Stack, Switch, Table, Tabs, Text, TextInput, Title,
} from '@mantine/core';
import { IconPlus } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { api, type Me } from '../../api';
import { useCompany } from '../../company';
import { formatAmount, formatDate, formatDateTime, notifyError, notifySaved, today } from '../../util';
import PageHeader from '../../components/PageHeader';

interface Station { id: string; name: string; location: string; notes: string; is_active: boolean; generators: number; boxes: number; subscriptions: number }
interface Generator {
  id: string; station_id: string; station_name: string; name: string; kva: string | null; brand: string; serial: string;
  fuel_item_id: string | null; fuel_item_name: string | null; status: 'active' | 'maintenance' | 'retired'; notes: string;
  last_hour_meter: string | null; last_log_date: string | null; in_outage: boolean;
}
interface Box { id: string; station_id: string; station_name: string; code: string; location: string; notes: string; is_active: boolean; subscriptions: number }
interface Item { id: string; name: string; kind: string; unit: string; is_active: boolean }

const statusColor = { active: 'teal', maintenance: 'orange', retired: 'gray' } as const;

export default function Network({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const { company } = useCompany();
  const [stations, setStations] = useState<Station[] | null>(null);
  const [generators, setGenerators] = useState<Generator[]>([]);
  const [boxes, setBoxes] = useState<Box[]>([]);
  const [items, setItems] = useState<Item[]>([]);
  const [edit, setEdit] = useState<{ kind: 'station' | 'generator' | 'box'; item: Station | Generator | Box | null } | null>(null);
  const [log, setLog] = useState<Generator | null>(null);
  const canEdit = me.role !== 'read_only';

  const load = useCallback(() => {
    if (!company) return;
    const b = `/companies/${company.id}`;
    api<Station[]>(`${b}/stations`).then(setStations).catch((e) => notifyError(t, e));
    api<Generator[]>(`${b}/generators`).then(setGenerators).catch(() => {});
    api<Box[]>(`${b}/boxes`).then(setBoxes).catch(() => {});
    api<Item[]>(`${b}/items`).then((x) => setItems(x.filter((i) => i.kind === 'stock' && i.is_active))).catch(() => {});
  }, [company, t]);
  useEffect(load, [load]);

  if (!company) return null;
  if (stations === null) return <Center p="xl"><Loader /></Center>;

  return (
    <Stack gap="lg" maw={1200}>
      <PageHeader title={t('net.title')} subtitle={t('net.subtitle')} />
      <Tabs defaultValue="stations" keepMounted={false}>
        <Tabs.List>
          <Tabs.Tab value="stations">{t('net.stations')} ({stations.length})</Tabs.Tab>
          <Tabs.Tab value="generators">{t('net.generators')} ({generators.length})</Tabs.Tab>
          <Tabs.Tab value="boxes">{t('net.boxes')} ({boxes.length})</Tabs.Tab>
        </Tabs.List>

        <Tabs.Panel value="stations" pt="md">
          <Paper withBorder>
            {canEdit && <Group justify="flex-end" p="sm"><Button size="xs" leftSection={<IconPlus size={14} />} onClick={() => setEdit({ kind: 'station', item: null })}>{t('net.newStation')}</Button></Group>}
            {stations.length === 0 ? <Text c="dimmed" ta="center" p="lg">{t('net.emptyStations')}</Text> : (
              <Table verticalSpacing="sm" key={i18n.language}>
                <Table.Thead><Table.Tr>
                  <Table.Th>{t('net.name')}</Table.Th><Table.Th>{t('net.location')}</Table.Th><Table.Th>{t('net.generators')}</Table.Th>
                  <Table.Th>{t('net.boxes')}</Table.Th><Table.Th>{t('customers.subscriptions')}</Table.Th><Table.Th />
                </Table.Tr></Table.Thead>
                <Table.Tbody>
                  {stations.map((s) => (
                    <Table.Tr key={s.id} style={{ opacity: s.is_active ? 1 : 0.5 }}>
                      <Table.Td fw={500}>{s.name}</Table.Td><Table.Td c="dimmed">{s.location || '—'}</Table.Td>
                      <Table.Td>{s.generators}</Table.Td><Table.Td>{s.boxes}</Table.Td><Table.Td>{s.subscriptions}</Table.Td>
                      <Table.Td>{canEdit && <Button size="compact-xs" variant="subtle" onClick={() => setEdit({ kind: 'station', item: s })}>{t('common.edit')}</Button>}</Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            )}
          </Paper>
        </Tabs.Panel>

        <Tabs.Panel value="generators" pt="md">
          <Paper withBorder>
            {canEdit && stations.length > 0 && <Group justify="flex-end" p="sm"><Button size="xs" leftSection={<IconPlus size={14} />} onClick={() => setEdit({ kind: 'generator', item: null })}>{t('net.newGenerator')}</Button></Group>}
            {generators.length === 0 ? <Text c="dimmed" ta="center" p="lg">{stations.length === 0 ? t('net.needStation') : t('common.empty')}</Text> : (
              <Table.ScrollContainer minWidth={800}>
                <Table verticalSpacing="sm" highlightOnHover key={i18n.language}>
                  <Table.Thead><Table.Tr>
                    <Table.Th>{t('net.name')}</Table.Th><Table.Th>{t('net.station')}</Table.Th><Table.Th>kVA</Table.Th>
                    <Table.Th>{t('net.fuelItem')}</Table.Th><Table.Th>{t('net.hourMeter')}</Table.Th><Table.Th>{t('common.status')}</Table.Th><Table.Th />
                  </Table.Tr></Table.Thead>
                  <Table.Tbody>
                    {generators.map((g) => (
                      <Table.Tr key={g.id} style={{ cursor: 'pointer' }} onClick={() => setLog(g)}>
                        <Table.Td fw={500}>{g.name}<Text size="xs" c="dimmed">{[g.brand, g.serial].filter(Boolean).join(' · ')}</Text></Table.Td>
                        <Table.Td>{g.station_name}</Table.Td>
                        <Table.Td dir="ltr">{g.kva ?? '—'}</Table.Td>
                        <Table.Td c="dimmed">{g.fuel_item_name ?? '—'}</Table.Td>
                        <Table.Td ff="monospace">{g.last_hour_meter ? `${Number(g.last_hour_meter)} h` : '—'}{g.last_log_date && <Text size="xs" c="dimmed">{formatDate(g.last_log_date)}</Text>}</Table.Td>
                        <Table.Td>
                          <Badge variant="light" color={statusColor[g.status]}>{t(`net.statuses.${g.status}`)}</Badge>
                          {g.in_outage && <Badge ms={4} color="red">{t('net.outageNow')}</Badge>}
                        </Table.Td>
                        <Table.Td onClick={(e) => e.stopPropagation()}>{canEdit && <Button size="compact-xs" variant="subtle" onClick={() => setEdit({ kind: 'generator', item: g })}>{t('common.edit')}</Button>}</Table.Td>
                      </Table.Tr>
                    ))}
                  </Table.Tbody>
                </Table>
              </Table.ScrollContainer>
            )}
          </Paper>
        </Tabs.Panel>

        <Tabs.Panel value="boxes" pt="md">
          <Paper withBorder>
            {canEdit && stations.length > 0 && <Group justify="flex-end" p="sm"><Button size="xs" leftSection={<IconPlus size={14} />} onClick={() => setEdit({ kind: 'box', item: null })}>{t('net.newBox')}</Button></Group>}
            {boxes.length === 0 ? <Text c="dimmed" ta="center" p="lg">{stations.length === 0 ? t('net.needStation') : t('common.empty')}</Text> : (
              <Table verticalSpacing="sm" key={i18n.language}>
                <Table.Thead><Table.Tr>
                  <Table.Th>{t('net.boxCode')}</Table.Th><Table.Th>{t('net.station')}</Table.Th><Table.Th>{t('net.location')}</Table.Th>
                  <Table.Th>{t('customers.subscriptions')}</Table.Th><Table.Th />
                </Table.Tr></Table.Thead>
                <Table.Tbody>
                  {boxes.map((b) => (
                    <Table.Tr key={b.id} style={{ opacity: b.is_active ? 1 : 0.5 }}>
                      <Table.Td ff="monospace" fw={500}>{b.code}</Table.Td><Table.Td>{b.station_name}</Table.Td>
                      <Table.Td c="dimmed">{b.location || '—'}</Table.Td><Table.Td>{b.subscriptions}</Table.Td>
                      <Table.Td>{canEdit && <Button size="compact-xs" variant="subtle" onClick={() => setEdit({ kind: 'box', item: b })}>{t('common.edit')}</Button>}</Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            )}
          </Paper>
        </Tabs.Panel>
      </Tabs>

      {edit?.kind === 'station' && <StationForm station={edit.item as Station | null} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); load(); }} />}
      {edit?.kind === 'generator' && <GeneratorForm gen={edit.item as Generator | null} stations={stations} items={items} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); load(); }} />}
      {edit?.kind === 'box' && <BoxForm box={edit.item as Box | null} stations={stations} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); load(); }} />}
      {log && <GeneratorLog gen={log} canEdit={canEdit} onClose={() => { setLog(null); load(); }} />}
    </Stack>
  );
}

function useSave(path: string, isNew: boolean, onSaved: () => void) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const [busy, setBusy] = useState(false);
  return {
    busy,
    save: async (body: object) => {
      if (!company) return;
      setBusy(true);
      try {
        await api(`/companies/${company.id}${path}`, { method: isNew ? 'POST' : 'PATCH', body });
        notifySaved(t);
        onSaved();
      } catch (e) {
        notifyError(t, e);
      } finally {
        setBusy(false);
      }
    },
  };
}

function Buttons({ busy, onClose }: { busy: boolean; onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <Group justify="flex-end">
      <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
      <Button type="submit" loading={busy}>{t('common.save')}</Button>
    </Group>
  );
}

function StationForm({ station, onClose, onSaved }: { station: Station | null; onClose: () => void; onSaved: () => void }) {
  const { t } = useTranslation();
  const [f, setF] = useState({ name: station?.name ?? '', location: station?.location ?? '', notes: station?.notes ?? '', is_active: station?.is_active ?? true });
  const { busy, save } = useSave(station ? `/stations/${station.id}` : '/stations', !station, onSaved);
  return (
    <Modal opened onClose={onClose} title={station?.name ?? t('net.newStation')}>
      <form onSubmit={(e) => { e.preventDefault(); void save(f); }}>
        <Stack>
          <TextInput label={t('net.name')} required value={f.name} onChange={(e) => setF({ ...f, name: e.currentTarget.value })} data-autofocus />
          <TextInput label={t('net.location')} value={f.location} onChange={(e) => setF({ ...f, location: e.currentTarget.value })} />
          <TextInput label={t('customers.notes')} value={f.notes} onChange={(e) => setF({ ...f, notes: e.currentTarget.value })} />
          {station && <Switch label={t('common.active')} checked={f.is_active} onChange={(e) => setF({ ...f, is_active: e.currentTarget.checked })} />}
          <Buttons busy={busy} onClose={onClose} />
        </Stack>
      </form>
    </Modal>
  );
}

function GeneratorForm({ gen, stations, items, onClose, onSaved }: { gen: Generator | null; stations: Station[]; items: Item[]; onClose: () => void; onSaved: () => void }) {
  const { t } = useTranslation();
  const [f, setF] = useState({
    station_id: gen?.station_id ?? stations[0]?.id ?? '', name: gen?.name ?? '', kva: gen?.kva ?? '', brand: gen?.brand ?? '',
    serial: gen?.serial ?? '', fuel_item_id: gen?.fuel_item_id ?? '', status: gen?.status ?? 'active', notes: gen?.notes ?? '',
  });
  const { busy, save } = useSave(gen ? `/generators/${gen.id}` : '/generators', !gen, onSaved);
  return (
    <Modal opened onClose={onClose} title={gen?.name ?? t('net.newGenerator')}>
      <form onSubmit={(e) => { e.preventDefault(); void save(f); }}>
        <Stack>
          <Group grow>
            <TextInput label={t('net.name')} required value={f.name} onChange={(e) => setF({ ...f, name: e.currentTarget.value })} data-autofocus />
            <Select label={t('net.station')} required value={f.station_id || null} allowDeselect={false}
              data={stations.map((s) => ({ value: s.id, label: s.name }))} onChange={(v) => setF({ ...f, station_id: v ?? '' })} />
          </Group>
          <Group grow>
            <TextInput label="kVA" dir="ltr" value={f.kva} onChange={(e) => setF({ ...f, kva: e.currentTarget.value.replace(/[^\d.]/g, '') })} />
            <TextInput label={t('net.brand')} value={f.brand} onChange={(e) => setF({ ...f, brand: e.currentTarget.value })} />
            <TextInput label={t('net.serial')} dir="ltr" value={f.serial} onChange={(e) => setF({ ...f, serial: e.currentTarget.value })} />
          </Group>
          <Select label={t('net.fuelItem')} clearable value={f.fuel_item_id || null} data={items.map((i) => ({ value: i.id, label: `${i.name} (${i.unit})` }))}
            onChange={(v) => setF({ ...f, fuel_item_id: v ?? '' })} />
          <Select label={t('common.status')} value={f.status} allowDeselect={false}
            data={(['active', 'maintenance', 'retired'] as const).map((s) => ({ value: s, label: t(`net.statuses.${s}`) }))}
            onChange={(v) => setF({ ...f, status: (v ?? 'active') as Generator['status'] })} />
          <TextInput label={t('customers.notes')} value={f.notes} onChange={(e) => setF({ ...f, notes: e.currentTarget.value })} />
          <Buttons busy={busy} onClose={onClose} />
        </Stack>
      </form>
    </Modal>
  );
}

function BoxForm({ box, stations, onClose, onSaved }: { box: Box | null; stations: Station[]; onClose: () => void; onSaved: () => void }) {
  const { t } = useTranslation();
  const [f, setF] = useState({ station_id: box?.station_id ?? stations[0]?.id ?? '', code: box?.code ?? '', location: box?.location ?? '', notes: box?.notes ?? '', is_active: box?.is_active ?? true });
  const { busy, save } = useSave(box ? `/boxes/${box.id}` : '/boxes', !box, onSaved);
  return (
    <Modal opened onClose={onClose} title={box?.code ?? t('net.newBox')}>
      <form onSubmit={(e) => { e.preventDefault(); void save(f); }}>
        <Stack>
          <Group grow>
            <TextInput label={t('net.boxCode')} required dir="ltr" value={f.code} onChange={(e) => setF({ ...f, code: e.currentTarget.value })} data-autofocus />
            <Select label={t('net.station')} required value={f.station_id || null} allowDeselect={false}
              data={stations.map((s) => ({ value: s.id, label: s.name }))} onChange={(v) => setF({ ...f, station_id: v ?? '' })} />
          </Group>
          <TextInput label={t('net.location')} value={f.location} onChange={(e) => setF({ ...f, location: e.currentTarget.value })} />
          <TextInput label={t('customers.notes')} value={f.notes} onChange={(e) => setF({ ...f, notes: e.currentTarget.value })} />
          {box && <Switch label={t('common.active')} checked={f.is_active} onChange={(e) => setF({ ...f, is_active: e.currentTarget.checked })} />}
          <Buttons busy={busy} onClose={onClose} />
        </Stack>
      </form>
    </Modal>
  );
}

interface LogData {
  hours: { id: string; log_date: string; hour_meter: string; hours_run: string | null; notes: string; created_by: string | null }[];
  outages: { id: string; started_at: string; ended_at: string | null; reason: string; minutes: number }[];
  fuel: { id: string; number: number; doc_date: string; quantity: string; unit: string; value: string; notes: string }[];
}

function GeneratorLog({ gen, canEdit, onClose }: { gen: Generator; canEdit: boolean; onClose: () => void }) {
  const { t } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [d, setD] = useState<LogData | null>(null);
  const [date, setDate] = useState(today());
  const [meter, setMeter] = useState('');
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  const base = `/companies/${company?.id}/generators/${gen.id}`;

  const load = useCallback(() => {
    api<LogData>(`${base}/log`).then(setD).catch((e) => notifyError(t, e));
  }, [base, t]);
  useEffect(load, [load]);

  async function call(path: string, body: object, method = 'POST') {
    setBusy(true);
    try {
      await api(`${base}${path}`, { method, body });
      notifySaved(t);
      load();
      return true;
    } catch (e) {
      notifyError(t, e);
      return false;
    } finally {
      setBusy(false);
    }
  }

  const open = d?.outages.find((o) => !o.ended_at);
  const bdec = company ? decimalsOf(company.base_currency) : 2;

  return (
    <Modal opened onClose={onClose} size="xl" title={`${gen.name} · ${gen.station_name}`}>
      {!d ? <Center p="xl"><Loader size="sm" /></Center> : (
        <Grid>
          <Grid.Col span={{ base: 12, md: 6 }}>
            <Paper withBorder p="sm">
              <Title order={6} mb="xs">{t('net.hourMeter')}</Title>
              {canEdit && (
                <Group align="flex-end" gap="xs" mb="sm">
                  <TextInput type="date" size="xs" value={date} onChange={(e) => setDate(e.currentTarget.value)} />
                  <TextInput size="xs" dir="ltr" w={110} placeholder="h" value={meter} onChange={(e) => setMeter(e.currentTarget.value.replace(/[^\d.]/g, ''))} />
                  <Button size="xs" loading={busy} disabled={!meter} onClick={async () => { if (await call('/hours', { date, hour_meter: meter })) setMeter(''); }}>{t('common.save')}</Button>
                </Group>
              )}
              <Table verticalSpacing={2}>
                <Table.Tbody>
                  {d.hours.map((h) => (
                    <Table.Tr key={h.id}>
                      <Table.Td>{formatDate(h.log_date)}</Table.Td>
                      <Table.Td ff="monospace" dir="ltr">{Number(h.hour_meter)} h</Table.Td>
                      <Table.Td ff="monospace" dir="ltr" c="dimmed">{h.hours_run ? `+${Number(h.hours_run)}` : ''}</Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </Paper>
          </Grid.Col>
          <Grid.Col span={{ base: 12, md: 6 }}>
            <Paper withBorder p="sm" mb="sm">
              <Title order={6} mb="xs">{t('net.outages')}</Title>
              {canEdit && (open ? (
                <Button size="xs" color="teal" loading={busy} mb="sm" onClick={() => call(`/outages/${open.id}`, { ended_at: 'now' }, 'PATCH')}>{t('net.endOutage')}</Button>
              ) : (
                <Group gap="xs" mb="sm">
                  <TextInput size="xs" placeholder={t('net.reason')} value={reason} onChange={(e) => setReason(e.currentTarget.value)} />
                  <Button size="xs" color="red" loading={busy} onClick={async () => { if (await call('/outages', { reason })) setReason(''); }}>{t('net.startOutage')}</Button>
                </Group>
              ))}
              <Table verticalSpacing={2}>
                <Table.Tbody>
                  {d.outages.map((o) => (
                    <Table.Tr key={o.id}>
                      <Table.Td>{formatDateTime(o.started_at)}</Table.Td>
                      <Table.Td>{o.ended_at ? `${o.minutes} min` : <Badge color="red" size="xs">{t('net.outageNow')}</Badge>}</Table.Td>
                      <Table.Td c="dimmed">{o.reason}</Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </Paper>
            <Paper withBorder p="sm">
              <Title order={6} mb="xs">{t('net.fuel')}</Title>
              <Table verticalSpacing={2}>
                <Table.Tbody>
                  {d.fuel.map((x) => (
                    <Table.Tr key={x.id}>
                      <Table.Td>{formatDate(x.doc_date)}</Table.Td>
                      <Table.Td ff="monospace" dir="ltr">{Number(x.quantity)} {x.unit}</Table.Td>
                      <Table.Td ff="monospace" dir="ltr" c="dimmed">{formatAmount(x.value, bdec)}</Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
              {d.fuel.length === 0 && <Text size="xs" c="dimmed">{t('net.fuelHint')}</Text>}
            </Paper>
          </Grid.Col>
        </Grid>
      )}
    </Modal>
  );
}
