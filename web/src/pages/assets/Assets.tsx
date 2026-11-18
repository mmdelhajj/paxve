import { useCallback, useEffect, useState } from 'react';
import {
  Badge, Button, Center, Group, Loader, Modal, NumberInput, Paper, Select, SimpleGrid, Stack, Table, Text, TextInput,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { IconPlayerPlay, IconPlus } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { api, type Account, type Me } from '../../api';
import { useCompany } from '../../company';
import { accountLabel, formatAmount, formatDate, notifyError, notifySaved, today } from '../../util';
import PageHeader, { Confirm } from '../../components/PageHeader';
import { currentPeriod } from '../../billing';
import { useMethods } from '../hr/Employees';

interface Asset {
  id: string; code: string; name: string; category: string; purchase_date: string; currency: string; cost: string; cost_base: string;
  salvage_base: string; useful_life_months: number; start_period: string; asset_account_code: string; status: 'active' | 'disposed';
  disposal_date: string | null; accumulated: string; net_book_value: string; monthly: string; last_period: string | null; months_done: number;
}

function useAccounts() {
  const { company } = useCompany();
  const [accounts, setAccounts] = useState<Account[]>([]);
  useEffect(() => {
    if (company) api<Account[]>(`/companies/${company.id}/accounts`).then((a) => setAccounts(a.filter((x) => x.is_postable && x.is_active))).catch(() => {});
  }, [company]);
  return accounts;
}

export default function Assets({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [d, setD] = useState<{ base_currency: string; rows: Asset[] } | null>(null);
  const [creating, setCreating] = useState(false);
  const [disposing, setDisposing] = useState<Asset | null>(null);
  const [period, setPeriod] = useState(currentPeriod());
  const [confirmRun, setConfirmRun] = useState(false);
  const [busy, setBusy] = useState(false);
  const canEdit = me.role === 'admin' || me.role === 'accountant';

  const load = useCallback(() => {
    if (!company) return;
    api<{ base_currency: string; rows: Asset[] }>(`/companies/${company.id}/assets`).then(setD).catch((e) => notifyError(t, e));
  }, [company, t]);
  useEffect(load, [load]);
  if (!company) return null;
  const base = d?.base_currency ?? company.base_currency;
  const dec = decimalsOf(base);
  const totals = (d?.rows ?? []).filter((a) => a.status === 'active').reduce(
    (s, a) => ({ cost: s.cost + Number(a.cost_base), acc: s.acc + Number(a.accumulated), nbv: s.nbv + Number(a.net_book_value) }),
    { cost: 0, acc: 0, nbv: 0 });

  async function runDep() {
    if (!company) return;
    setBusy(true);
    try {
      const r = await api<{ assets: number; total: string }>(`/companies/${company.id}/depreciation/run`, { method: 'POST', body: { period } });
      notifications.show({ color: 'teal', message: r.assets ? t('fa.runDone', { n: r.assets, total: `${formatAmount(r.total, dec)} ${base}` }) : t('fa.nothing') });
      load();
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
      setConfirmRun(false);
    }
  }

  return (
    <Stack gap="lg" maw={1200}>
      <PageHeader title={t('fa.title')} subtitle={`${company.name_en} · ${t('reports.amountsIn', { cur: base })}`}>
        {canEdit && (
          <>
            <TextInput type="month" value={period} onChange={(e) => setPeriod(e.currentTarget.value)} aria-label={t('hr.period')} />
            <Button variant="default" leftSection={<IconPlayerPlay size={16} />} onClick={() => setConfirmRun(true)}>{t('fa.runDep')}</Button>
            <Button leftSection={<IconPlus size={16} />} onClick={() => setCreating(true)}>{t('fa.newAsset')}</Button>
          </>
        )}
      </PageHeader>
      {confirmRun && <Confirm text={`${t('fa.runDep')} · ${period}`} yesLabel={t('fa.runDep')} noLabel={t('common.cancel')} color="teal" busy={busy}
        onYes={runDep} onNo={() => setConfirmRun(false)} />}
      <SimpleGrid cols={{ base: 1, sm: 3 }}>
        <Paper withBorder p="md"><Text size="xs" c="dimmed">{t('fa.cost')}</Text><Text fw={700} size="lg" dir="ltr">{formatAmount(totals.cost, dec)} {base}</Text></Paper>
        <Paper withBorder p="md"><Text size="xs" c="dimmed">{t('fa.accumulated')}</Text><Text fw={700} size="lg" dir="ltr">{formatAmount(totals.acc, dec)} {base}</Text></Paper>
        <Paper withBorder p="md"><Text size="xs" c="dimmed">{t('fa.nbv')}</Text><Text fw={700} size="lg" c="teal.8" dir="ltr">{formatAmount(totals.nbv, dec)} {base}</Text></Paper>
      </SimpleGrid>
      <Paper withBorder>
        {d === null ? <Center p="xl"><Loader size="sm" /></Center> : d.rows.length === 0 ? <Text c="dimmed" ta="center" p="xl">{t('common.empty')}</Text> : (
          <Table.ScrollContainer minWidth={900}>
            <Table verticalSpacing="sm" key={i18n.language}>
              <Table.Thead><Table.Tr>
                <Table.Th>{t('fa.name')}</Table.Th><Table.Th>{t('fa.purchaseDate')}</Table.Th><Table.Th style={{ textAlign: 'end' }}>{t('fa.cost')}</Table.Th>
                <Table.Th style={{ textAlign: 'end' }}>{t('fa.monthly')}</Table.Th><Table.Th style={{ textAlign: 'end' }}>{t('fa.accumulated')}</Table.Th>
                <Table.Th style={{ textAlign: 'end' }}>{t('fa.nbv')}</Table.Th><Table.Th>{t('hr.status')}</Table.Th><Table.Th />
              </Table.Tr></Table.Thead>
              <Table.Tbody>
                {d.rows.map((a) => (
                  <Table.Tr key={a.id} style={{ opacity: a.status === 'disposed' ? 0.5 : 1 }}>
                    <Table.Td><Text size="sm" fw={500}>{a.name}</Text><Text size="xs" c="dimmed">{a.code}{a.category ? ` · ${a.category}` : ''} · {a.asset_account_code}</Text></Table.Td>
                    <Table.Td>{formatDate(a.purchase_date)}<Text size="xs" c="dimmed">{a.months_done}/{a.useful_life_months} {t('fa.months')}</Text></Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(a.cost_base, dec)}
                      {a.currency !== base && <Text size="xs" c="dimmed">{formatAmount(a.cost, decimalsOf(a.currency))} {a.currency}</Text>}</Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(a.monthly, dec)}</Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(a.accumulated, dec)}</Table.Td>
                    <Table.Td ff="monospace" dir="ltr" fw={600} style={{ textAlign: 'end' }}>{formatAmount(a.net_book_value, dec)}</Table.Td>
                    <Table.Td><Badge variant="light" color={a.status === 'active' ? 'teal' : 'gray'}>{t(`fa.statuses.${a.status}`)}</Badge></Table.Td>
                    <Table.Td>{canEdit && a.status === 'active' && <Button size="compact-xs" variant="subtle" color="red" onClick={() => setDisposing(a)}>{t('fa.dispose')}</Button>}</Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </Paper>
      {creating && <NewAsset onClose={() => setCreating(false)} onDone={() => { setCreating(false); load(); }} />}
      {disposing && <Dispose asset={disposing} onClose={() => setDisposing(null)} onDone={() => { setDisposing(null); load(); }} />}
    </Stack>
  );
}

function NewAsset({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const { t } = useTranslation();
  const { company, currencies } = useCompany();
  const accounts = useAccounts();
  const methods = useMethods();
  const [f, setF] = useState({
    name: '', category: '', purchase_date: today(), currency: company?.base_currency ?? 'USD', cost: '', salvage: '',
    useful_life_months: 60 as number | string, asset_account_id: '', paid_from_method_id: '',
  });
  const [busy, setBusy] = useState(false);
  const assetAccounts = accounts.filter((a) => a.type === 'asset');

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!company) return;
    setBusy(true);
    try {
      await api(`/companies/${company.id}/assets`, { method: 'POST', body: { ...f, useful_life_months: Number(f.useful_life_months) } });
      notifySaved(t);
      onDone();
    } catch (err) {
      notifyError(t, err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened onClose={onClose} title={t('fa.newAsset')} size="lg">
      <form onSubmit={submit}>
        <Stack>
          <Group grow>
            <TextInput label={t('fa.name')} required value={f.name} onChange={(e) => setF({ ...f, name: e.currentTarget.value })} data-autofocus />
            <TextInput label={t('fa.category')} value={f.category} onChange={(e) => setF({ ...f, category: e.currentTarget.value })} />
          </Group>
          <Group grow>
            <TextInput type="date" label={t('fa.purchaseDate')} value={f.purchase_date} onChange={(e) => setF({ ...f, purchase_date: e.currentTarget.value })} />
            <Select label={t('plans.currency')} searchable allowDeselect={false} data={currencies.map((c) => c.code)} value={f.currency} onChange={(v) => v && setF({ ...f, currency: v, paid_from_method_id: '' })} />
            <TextInput label={t('fa.cost')} dir="ltr" required value={f.cost} onChange={(e) => setF({ ...f, cost: e.currentTarget.value.replace(/[^\d.]/g, '') })} />
          </Group>
          <Group grow>
            <NumberInput label={t('fa.life')} min={1} max={1200} value={f.useful_life_months} onChange={(v) => setF({ ...f, useful_life_months: v })} />
            <TextInput label={`${t('fa.salvage')} (${company?.base_currency})`} dir="ltr" value={f.salvage} onChange={(e) => setF({ ...f, salvage: e.currentTarget.value.replace(/[^\d.]/g, '') })} />
          </Group>
          <Select label={t('fa.assetAccount')} required searchable data={assetAccounts.map((a) => ({ value: a.id, label: accountLabel(a) }))}
            value={f.asset_account_id || null} onChange={(v) => setF({ ...f, asset_account_id: v ?? '' })} />
          <Select label={t('fa.paidFrom')} clearable placeholder={t('fa.notPaid')} value={f.paid_from_method_id || null}
            data={methods.filter((m) => m.currency === f.currency).map((m) => ({ value: m.id, label: m.name }))}
            onChange={(v) => setF({ ...f, paid_from_method_id: v ?? '' })} />
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
            <Button type="submit" loading={busy} disabled={!f.name || !Number(f.cost) || !f.asset_account_id}>{t('common.save')}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}

function Dispose({ asset, onClose, onDone }: { asset: Asset; onClose: () => void; onDone: () => void }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const methods = useMethods();
  const [f, setF] = useState({ date: today(), proceeds: '', method_id: '' });
  const [busy, setBusy] = useState(false);
  async function submit() {
    if (!company) return;
    setBusy(true);
    try {
      await api(`/companies/${company.id}/assets/${asset.id}/dispose`, { method: 'POST', body: f });
      notifySaved(t);
      onDone();
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal opened onClose={onClose} title={`${t('fa.dispose')} · ${asset.name}`}>
      <Stack>
        <TextInput type="date" label={t('invoices.date')} value={f.date} onChange={(e) => setF({ ...f, date: e.currentTarget.value })} />
        <Group grow>
          <TextInput label={t('fa.proceeds')} dir="ltr" value={f.proceeds} onChange={(e) => setF({ ...f, proceeds: e.currentTarget.value.replace(/[^\d.]/g, '') })} />
          <Select label={t('hr.paidFrom')} clearable value={f.method_id || null} onChange={(v) => setF({ ...f, method_id: v ?? '' })}
            data={methods.map((m) => ({ value: m.id, label: `${m.name} (${m.currency})` }))} />
        </Group>
        <Text size="sm" c="dimmed">{t('fa.nbv')}: <span dir="ltr">{asset.net_book_value}</span></Text>
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
          <Button color="red" loading={busy} disabled={Number(f.proceeds) > 0 && !f.method_id} onClick={submit}>{t('fa.dispose')}</Button>
        </Group>
      </Stack>
    </Modal>
  );
}

interface RegRow { id: string; code: string; name: string; category: string; purchase_date: string; status: string; cost: string; accumulated: string; net_book_value: string; useful_life_months: number; months_done: number }

export function AssetRegister() {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [to, setTo] = useState(today());
  const [d, setD] = useState<{ base_currency: string; rows: RegRow[]; total_cost: string; total_accumulated: string; total_net_book_value: string } | null>(null);
  useEffect(() => {
    if (!company) return;
    setD(null);
    api<typeof d>(`/companies/${company.id}/reports/assets?to=${to}`).then(setD).catch(() => {});
  }, [company, to]);
  if (!company) return null;
  const dec = decimalsOf(company.base_currency);
  return (
    <Stack gap="lg" maw={1100}>
      <PageHeader title={t('fa.register')} subtitle={`${company.name_en} · ${t('reports.amountsIn', { cur: company.base_currency })}`}>
        <TextInput type="date" size="xs" label={t('reports2.asOf')} value={to} onChange={(e) => setTo(e.currentTarget.value)} />
      </PageHeader>
      <Paper withBorder>
        {!d ? <Center p="xl"><Loader size="sm" /></Center> : d.rows.length === 0 ? <Text c="dimmed" ta="center" p="xl">{t('common.empty')}</Text> : (
          <Table.ScrollContainer minWidth={760}>
            <Table verticalSpacing="xs" key={i18n.language}>
              <Table.Thead><Table.Tr>
                <Table.Th>{t('fa.name')}</Table.Th><Table.Th>{t('fa.purchaseDate')}</Table.Th><Table.Th style={{ textAlign: 'end' }}>{t('fa.cost')}</Table.Th>
                <Table.Th style={{ textAlign: 'end' }}>{t('fa.accumulated')}</Table.Th><Table.Th style={{ textAlign: 'end' }}>{t('fa.nbv')}</Table.Th>
              </Table.Tr></Table.Thead>
              <Table.Tbody>
                {d.rows.map((r) => (
                  <Table.Tr key={r.id}>
                    <Table.Td><Text size="sm" fw={500}>{r.name}</Text><Text size="xs" c="dimmed">{r.code} · {r.months_done}/{r.useful_life_months}</Text></Table.Td>
                    <Table.Td>{formatDate(r.purchase_date)}</Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(r.cost, dec)}</Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(r.accumulated, dec)}</Table.Td>
                    <Table.Td ff="monospace" dir="ltr" fw={600} style={{ textAlign: 'end' }}>{formatAmount(r.net_book_value, dec)}</Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
              <Table.Tfoot><Table.Tr>
                <Table.Th colSpan={2}>{t('reports.totals')}</Table.Th>
                <Table.Th ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(d.total_cost, dec)}</Table.Th>
                <Table.Th ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(d.total_accumulated, dec)}</Table.Th>
                <Table.Th ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(d.total_net_book_value, dec)}</Table.Th>
              </Table.Tr></Table.Tfoot>
            </Table>
          </Table.ScrollContainer>
        )}
      </Paper>
    </Stack>
  );
}
