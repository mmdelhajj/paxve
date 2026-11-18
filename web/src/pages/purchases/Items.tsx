import { useCallback, useEffect, useState } from 'react';
import {
  Anchor, Badge, Button, Center, Group, Loader, Modal, Paper, Select, SimpleGrid, Stack, Switch, Table, Text, TextInput, Title,
} from '@mantine/core';
import { useDebouncedValue } from '@mantine/hooks';
import { IconArrowBack, IconEdit, IconPlus, IconSearch, IconSettings } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { api, type Account, type Me } from '../../api';
import { useCompany } from '../../company';
import { accountLabel, formatAmount, formatDate, notifyError, notifySaved } from '../../util';
import PageHeader from '../../components/PageHeader';
import type { Item } from './types';

export default function Items({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [rows, setRows] = useState<Item[] | null>(null);
  const [q, setQ] = useState('');
  const [dq] = useDebouncedValue(q, 250);
  const [modal, setModal] = useState<null | 'new' | 'settings'>(null);
  const canManage = me.role === 'admin' || me.role === 'accountant';

  const load = useCallback(() => {
    if (!company) return;
    api<Item[]>(`/companies/${company.id}/items?q=${encodeURIComponent(dq)}`).then(setRows).catch((e) => notifyError(t, e));
  }, [company, dq, t]);
  useEffect(load, [load]);
  if (!company) return null;
  const bdec = decimalsOf(company.base_currency);
  const totalValue = (rows ?? []).reduce((s, r) => s + Number(r.value), 0);

  return (
    <Stack gap="lg" maw={1200}>
      <PageHeader title={t('pur.items')} subtitle={`${company.name_en} · ${t('pur.value')}: ${formatAmount(totalValue, bdec)} ${company.base_currency}`}>
        {canManage && <Button variant="default" leftSection={<IconSettings size={16} />} onClick={() => setModal('settings')}>{t('pur.settings')}</Button>}
        {canManage && <Button leftSection={<IconPlus size={16} />} onClick={() => setModal('new')}>{t('pur.newItem')}</Button>}
      </PageHeader>
      <TextInput leftSection={<IconSearch size={16} />} placeholder={t('common.search')} value={q} onChange={(e) => setQ(e.currentTarget.value)} w={300} />
      <Paper withBorder>
        {rows === null ? <Center p="xl"><Loader size="sm" /></Center> : rows.length === 0 ? <Text c="dimmed" ta="center" p="xl">{t('common.empty')}</Text> : (
          <Table.ScrollContainer minWidth={760}>
            <Table verticalSpacing="sm" highlightOnHover key={i18n.language}>
              <Table.Thead><Table.Tr>
                <Table.Th w={80}>{t('pur.code')}</Table.Th><Table.Th>{t('pur.name')}</Table.Th><Table.Th>{t('pur.kind')}</Table.Th>
                <Table.Th style={{ textAlign: 'end' }}>{t('pur.quantity')}</Table.Th><Table.Th style={{ textAlign: 'end' }}>{t('pur.avgCost')}</Table.Th>
                <Table.Th style={{ textAlign: 'end' }}>{t('pur.value')}</Table.Th>
              </Table.Tr></Table.Thead>
              <Table.Tbody>
                {rows.map((it) => (
                  <Table.Tr key={it.id} style={{ cursor: 'pointer', opacity: it.is_active ? 1 : 0.5 }} onClick={() => { window.location.hash = `#/item/${it.id}`; }}>
                    <Table.Td ff="monospace">{it.code}</Table.Td>
                    <Table.Td fw={500}>{it.name_i18n?.[i18n.language] || it.name}</Table.Td>
                    <Table.Td><Badge variant="light" color={it.kind === 'stock' ? 'blue' : 'gray'}>{t(`pur.kinds.${it.kind}`)}</Badge></Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{it.kind === 'stock' ? `${Number(it.quantity)} ${it.unit}` : '—'}</Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{it.kind === 'stock' ? formatAmount(it.avg_cost, 4) : '—'}</Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{it.kind === 'stock' ? formatAmount(it.value, bdec) : '—'}</Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </Paper>
      {modal === 'new' && <ItemForm onClose={() => setModal(null)} onSaved={() => { setModal(null); load(); }} />}
      {modal === 'settings' && <PurchSettings onClose={() => setModal(null)} />}
    </Stack>
  );
}

function ItemForm({ item, onClose, onSaved }: { item?: Item; onClose: () => void; onSaved: () => void }) {
  const { t } = useTranslation();
  const { company, currencies } = useCompany();
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [f, setF] = useState({
    code: item?.code ?? '', name: item?.name ?? '', kind: item?.kind ?? 'stock', unit: item?.unit ?? 'pcs',
    sale_price: item ? String(Number(item.sale_price)) : '', sale_currency: item?.sale_currency ?? '',
    inventory_account_id: item?.inventory_account_id ?? '', expense_account_id: item?.expense_account_id ?? '',
    revenue_account_id: item?.revenue_account_id ?? '', is_active: item?.is_active ?? true,
  });
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (company) api<Account[]>(`/companies/${company.id}/accounts`).then((a) => setAccounts(a.filter((x) => x.is_postable && x.is_active))).catch(() => {});
  }, [company]);
  const opts = (types: string[]) => accounts.filter((a) => types.includes(a.type)).map((a) => ({ value: a.id, label: accountLabel(a) }));
  const set = (k: keyof typeof f) => (v: string | boolean) => setF((x) => ({ ...x, [k]: v }));

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!company) return;
    setBusy(true);
    try {
      const body = item ? { ...f, kind: undefined } : { ...f, is_active: undefined };
      if (item) await api(`/companies/${company.id}/items/${item.id}`, { method: 'PATCH', body });
      else await api(`/companies/${company.id}/items`, { method: 'POST', body });
      notifySaved(t);
      onSaved();
    } catch (err) {
      notifyError(t, err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened onClose={onClose} title={item?.name ?? t('pur.newItem')} size="lg">
      <form onSubmit={submit}>
        <Stack>
          <Group grow align="flex-start">
            <TextInput label={t('pur.name')} required value={f.name} onChange={(e) => set('name')(e.currentTarget.value)} data-autofocus placeholder="Diesel, Cable 10mm…" />
            <TextInput label={`${t('pur.code')} (${t('common.optional')})`} value={f.code} onChange={(e) => set('code')(e.currentTarget.value)} maw={150} />
          </Group>
          <Group grow>
            <Select label={t('pur.kind')} disabled={!!item} value={f.kind} allowDeselect={false} onChange={(v) => v && set('kind')(v)}
              data={['stock', 'consumable', 'service'].map((k) => ({ value: k, label: t(`pur.kinds.${k}`) }))} />
            <TextInput label={t('pur.unit')} value={f.unit} onChange={(e) => set('unit')(e.currentTarget.value)} placeholder="L, kg, pcs, m" />
          </Group>
          <Group grow>
            <TextInput label={t('pur.salePrice')} dir="ltr" value={f.sale_price} onChange={(e) => set('sale_price')(e.currentTarget.value.replace(/[^\d.]/g, ''))} />
            <Select label={t('pur.currency')} searchable clearable data={currencies.map((c) => c.code)} value={f.sale_currency || null} onChange={(v) => set('sale_currency')(v ?? '')} />
          </Group>
          {f.kind === 'stock' && <Select label={t('pur.inventoryAccount')} searchable clearable placeholder={t('pur.inventory')} data={opts(['asset'])}
            value={f.inventory_account_id || null} onChange={(v) => set('inventory_account_id')(v ?? '')} />}
          <Select label={t('pur.expenseAccount')} searchable clearable data={opts(['expense'])} value={f.expense_account_id || null} onChange={(v) => set('expense_account_id')(v ?? '')} />
          <Select label={t('pur.revenueAccount')} searchable clearable data={opts(['income'])} value={f.revenue_account_id || null} onChange={(v) => set('revenue_account_id')(v ?? '')} />
          {item && <Switch label={t('common.active')} checked={f.is_active} onChange={(e) => set('is_active')(e.currentTarget.checked)} />}
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
            <Button type="submit" loading={busy}>{t('common.save')}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}

type Settings = Record<'ap_account_id' | 'inventory_account_id' | 'cogs_account_id' | 'fuel_account_id' | 'stock_adjust_account_id' | 'default_expense_account_id', string | null>;

function PurchSettings({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [s, setS] = useState<Settings | null>(null);
  useEffect(() => {
    if (!company) return;
    api<Account[]>(`/companies/${company.id}/accounts`).then((a) => setAccounts(a.filter((x) => x.is_postable && x.is_active))).catch(() => {});
    api<Settings>(`/companies/${company.id}/purchasing/settings`).then(setS).catch(() => {});
  }, [company]);
  async function save(k: keyof Settings, v: string | null) {
    if (!company) return;
    try {
      setS(await api<Settings>(`/companies/${company.id}/purchasing/settings`, { method: 'PUT', body: { [k]: v ?? '' } }));
      notifySaved(t);
    } catch (e) {
      notifyError(t, e);
    }
  }
  const data = accounts.map((a) => ({ value: a.id, label: accountLabel(a) }));
  const labels: Record<keyof Settings, string> = {
    ap_account_id: t('pur.ap'), inventory_account_id: t('pur.inventory'), cogs_account_id: t('pur.cogs'),
    fuel_account_id: t('pur.fuelAccount'), stock_adjust_account_id: t('pur.adjustAccount'), default_expense_account_id: t('pur.defaultExpense'),
  };
  return (
    <Modal opened onClose={onClose} title={t('pur.settings')} size="lg">
      {!s ? <Center p="xl"><Loader size="sm" /></Center> : (
        <SimpleGrid cols={{ base: 1, sm: 2 }}>
          {(Object.keys(labels) as (keyof Settings)[]).map((k) => (
            <Select key={k} label={labels[k]} searchable clearable data={data} value={s[k]} onChange={(v) => save(k, v)} />
          ))}
        </SimpleGrid>
      )}
    </Modal>
  );
}

interface ItemDetailData {
  item: Item;
  stock: { warehouse_id: string; warehouse: string; quantity: string; avg_cost: string; value: string }[];
  moves: { id: string; move_date: string; warehouse: string; quantity: string; unit_cost: string; value: string; source: string; notes: string }[];
}

export function ItemDetail({ me, id }: { me: Me; id: string }) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [d, setD] = useState<ItemDetailData | null>(null);
  const [edit, setEdit] = useState(false);
  const load = useCallback(() => {
    if (!company) return;
    api<ItemDetailData>(`/companies/${company.id}/items/${id}`).then(setD).catch((e) => notifyError(t, e));
  }, [company, id, t]);
  useEffect(load, [load]);
  if (!company) return null;
  if (!d) return <Center p="xl"><Loader /></Center>;
  const it = d.item;
  const bdec = decimalsOf(company.base_currency);
  return (
    <Stack gap="lg" maw={1200}>
      <Anchor href="#/items" size="sm"><Group gap={4}><IconArrowBack size={14} />{t('pur.backItems')}</Group></Anchor>
      <Group justify="space-between">
        <div>
          <Title order={2}>{it.name}</Title>
          <Text c="dimmed" size="sm">#{it.code} · {t(`pur.kinds.${it.kind}`)} · {it.unit}</Text>
        </div>
        {(me.role === 'admin' || me.role === 'accountant') && <Button variant="default" leftSection={<IconEdit size={16} />} onClick={() => setEdit(true)}>{t('common.edit')}</Button>}
      </Group>
      {it.kind === 'stock' && (
        <SimpleGrid cols={{ base: 1, sm: 3 }}>
          <Paper withBorder p="md"><Text size="xs" c="dimmed">{t('pur.quantity')}</Text><Text size="xl" fw={700} dir="ltr">{Number(it.quantity)} {it.unit}</Text></Paper>
          <Paper withBorder p="md"><Text size="xs" c="dimmed">{t('pur.avgCost')}</Text><Text size="xl" fw={700} dir="ltr">{formatAmount(it.avg_cost, 4)} {company.base_currency}</Text></Paper>
          <Paper withBorder p="md"><Text size="xs" c="dimmed">{t('pur.value')}</Text><Text size="xl" fw={700} dir="ltr">{formatAmount(it.value, bdec)} {company.base_currency}</Text></Paper>
        </SimpleGrid>
      )}
      {d.stock.length > 1 && (
        <Paper withBorder>
          <Table verticalSpacing="xs"><Table.Tbody>
            {d.stock.map((s) => (
              <Table.Tr key={s.warehouse_id}><Table.Td>{s.warehouse}</Table.Td><Table.Td ff="monospace" dir="ltr">{Number(s.quantity)} {it.unit}</Table.Td>
                <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(s.value, bdec)}</Table.Td></Table.Tr>
            ))}
          </Table.Tbody></Table>
        </Paper>
      )}
      <Paper withBorder>
        <Title order={5} p="sm">{t('pur.moves')}</Title>
        {d.moves.length === 0 ? <Text c="dimmed" ta="center" p="lg">{t('common.empty')}</Text> : (
          <Table.ScrollContainer minWidth={700}>
            <Table verticalSpacing="xs" key={i18n.language}>
              <Table.Thead><Table.Tr>
                <Table.Th>{t('invoices.date')}</Table.Th><Table.Th>{t('pur.kind')}</Table.Th><Table.Th>{t('pur.warehouse')}</Table.Th>
                <Table.Th style={{ textAlign: 'end' }}>{t('pur.qty')}</Table.Th><Table.Th style={{ textAlign: 'end' }}>{t('pur.unitPrice')}</Table.Th>
                <Table.Th style={{ textAlign: 'end' }}>{t('pur.value')}</Table.Th><Table.Th>{t('pur.notes')}</Table.Th>
              </Table.Tr></Table.Thead>
              <Table.Tbody>
                {d.moves.map((m) => (
                  <Table.Tr key={m.id}>
                    <Table.Td>{formatDate(m.move_date)}</Table.Td>
                    <Table.Td><Badge variant="light" color={Number(m.quantity) > 0 ? 'teal' : 'orange'}>{t(`pur.sources.${m.source}`)}</Badge></Table.Td>
                    <Table.Td c="dimmed">{m.warehouse}</Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }} c={Number(m.quantity) < 0 ? 'red.7' : undefined}>{Number(m.quantity)}</Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(m.unit_cost, 4)}</Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(m.value, bdec)}</Table.Td>
                    <Table.Td c="dimmed">{m.notes}</Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </Paper>
      {edit && <ItemForm item={it} onClose={() => setEdit(false)} onSaved={() => { setEdit(false); load(); }} />}
    </Stack>
  );
}
