import { useCallback, useEffect, useState } from 'react';
import {
  Alert, Badge, Button, Group, Modal, Paper, SegmentedControl, Select, SimpleGrid, Stack, Switch, Table, Text, TextInput, Title,
} from '@mantine/core';
import { IconPlus } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { api, type Account, type Me, type User } from '../../api';
import { useCompany } from '../../company';
import { accountLabel, formatAmount, notifyError, notifySaved } from '../../util';
import PageHeader from '../../components/PageHeader';
import type { Area, PaymentMethod, Plan } from '../../billing';

interface Settings { ar_account_id: string | null; fx_gain_account_id: string | null; fx_loss_account_id: string | null; default_revenue_account_id: string | null }

export default function Setup({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const { company, currencies, decimalsOf } = useCompany();
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [settings, setSettings] = useState<Settings | null>(null);
  const [plans, setPlans] = useState<Plan[]>([]);
  const [areas, setAreas] = useState<Area[]>([]);
  const [methods, setMethods] = useState<PaymentMethod[]>([]);
  const [users, setUsers] = useState<User[]>([]);
  const [edit, setEdit] = useState<{ kind: 'plan' | 'area' | 'method'; item: Plan | Area | PaymentMethod | null } | null>(null);
  const canManage = me.role === 'admin' || me.role === 'accountant';

  const load = useCallback(() => {
    if (!company) return;
    const base = `/companies/${company.id}`;
    api<Account[]>(`${base}/accounts`).then(setAccounts).catch(() => {});
    api<Settings>(`${base}/billing/settings`).then(setSettings).catch(() => {});
    api<Plan[]>(`${base}/plans`).then(setPlans).catch(() => {});
    api<Area[]>(`${base}/areas`).then(setAreas).catch(() => {});
    api<PaymentMethod[]>(`${base}/payment-methods`).then(setMethods).catch(() => {});
    if (me.role === 'admin') api<User[]>('/users').then(setUsers).catch(() => {});
  }, [company, me.role]);
  useEffect(load, [load]);

  if (!company) return null;
  const postable = accounts.filter((a) => a.is_postable && a.is_active).map((a) => ({ value: a.id, label: accountLabel(a) }));

  async function saveSetting(key: keyof Settings, value: string | null) {
    if (!company) return;
    try {
      setSettings(await api<Settings>(`/companies/${company.id}/billing/settings`, { method: 'PUT', body: { [key]: value ?? '' } }));
      notifySaved(t);
    } catch (e) {
      notifyError(t, e);
    }
  }

  return (
    <Stack gap="lg" maw={1200}>
      <PageHeader title={t('setup.title')} subtitle={company.name_en} />
      {settings && !settings.ar_account_id && <Alert color="yellow">{t('setup.notSetup')}</Alert>}

      <Paper withBorder p="md">
        <Title order={5} mb="sm">{t('setup.accounts')}</Title>
        <SimpleGrid cols={{ base: 1, sm: 2 }}>
          {(['ar_account_id', 'default_revenue_account_id', 'fx_gain_account_id', 'fx_loss_account_id'] as const).map((k) => (
            <Select key={k} searchable clearable disabled={!canManage} data={postable} value={settings?.[k] ?? null}
              label={t({ ar_account_id: 'setup.ar', default_revenue_account_id: 'setup.revenue', fx_gain_account_id: 'setup.fxGain', fx_loss_account_id: 'setup.fxLoss' }[k])}
              onChange={(v) => saveSetting(k, v)} />
          ))}
        </SimpleGrid>
      </Paper>

      <Section title={t('plans.title')} onAdd={canManage ? () => setEdit({ kind: 'plan', item: null }) : undefined} addLabel={t('plans.new')}>
        <Table.ScrollContainer minWidth={600}>
          <Table verticalSpacing="xs" key={i18n.language}>
            <Table.Thead><Table.Tr>
              <Table.Th>{t('plans.name')}</Table.Th><Table.Th>{t('plans.type')}</Table.Th><Table.Th>{t('plans.baseFee')}</Table.Th>
              <Table.Th>{t('plans.unitPrice')}</Table.Th><Table.Th>{t('plans.subscribers')}</Table.Th><Table.Th />
            </Table.Tr></Table.Thead>
            <Table.Tbody>
              {plans.map((p) => (
                <Table.Tr key={p.id} style={{ opacity: p.is_active ? 1 : 0.5 }}>
                  <Table.Td fw={500}>{p.name}</Table.Td>
                  <Table.Td><Badge variant="light" color={p.billing_type === 'metered' ? 'blue' : 'grape'}>{t(`plans.${p.billing_type}`)}</Badge></Table.Td>
                  <Table.Td dir="ltr" style={{ textAlign: 'start' }}>{formatAmount(p.base_fee, decimalsOf(p.currency))} {p.currency}</Table.Td>
                  <Table.Td dir="ltr" style={{ textAlign: 'start' }}>{p.billing_type === 'metered' ? `${Number(p.unit_price)} / ${p.unit_label}` : '—'}</Table.Td>
                  <Table.Td>{p.active_subscriptions}</Table.Td>
                  <Table.Td>{canManage && <Button size="compact-xs" variant="subtle" onClick={() => setEdit({ kind: 'plan', item: p })}>{t('common.edit')}</Button>}</Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
      </Section>

      <SimpleGrid cols={{ base: 1, md: 2 }}>
        <Section title={t('methods.title')} onAdd={canManage ? () => setEdit({ kind: 'method', item: null }) : undefined} addLabel={t('methods.new')}>
          <Table verticalSpacing="xs">
            <Table.Tbody>
              {methods.map((m) => (
                <Table.Tr key={m.id} style={{ opacity: m.is_active ? 1 : 0.5 }}>
                  <Table.Td fw={500}>{m.name}{m.user_name && <Text size="xs" c="dimmed">{m.user_name}</Text>}</Table.Td>
                  <Table.Td><Badge variant="light">{m.currency}</Badge></Table.Td>
                  <Table.Td c="dimmed" ff="monospace" dir="ltr">{m.account_code}</Table.Td>
                  <Table.Td>{canManage && <Button size="compact-xs" variant="subtle" onClick={() => setEdit({ kind: 'method', item: m })}>{t('common.edit')}</Button>}</Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Section>
        <Section title={t('areas.title')} onAdd={me.role !== 'read_only' ? () => setEdit({ kind: 'area', item: null }) : undefined} addLabel={t('areas.new')}>
          <Table verticalSpacing="xs">
            <Table.Tbody>
              {areas.map((a) => (
                <Table.Tr key={a.id} style={{ opacity: a.is_active ? 1 : 0.5 }}>
                  <Table.Td fw={500}>{a.name}</Table.Td>
                  <Table.Td c="dimmed">{a.collector_name ?? '—'}</Table.Td>
                  <Table.Td>{a.customers}</Table.Td>
                  <Table.Td>{me.role !== 'read_only' && <Button size="compact-xs" variant="subtle" onClick={() => setEdit({ kind: 'area', item: a })}>{t('common.edit')}</Button>}</Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Section>
      </SimpleGrid>

      {edit?.kind === 'plan' && <PlanForm plan={edit.item as Plan | null} postable={postable} currencies={currencies.map((c) => c.code)}
        onClose={() => setEdit(null)} onSaved={() => { setEdit(null); load(); }} />}
      {edit?.kind === 'method' && <MethodForm method={edit.item as PaymentMethod | null} postable={postable} currencies={currencies.map((c) => c.code)} users={users}
        onClose={() => setEdit(null)} onSaved={() => { setEdit(null); load(); }} />}
      {edit?.kind === 'area' && <AreaForm area={edit.item as Area | null} users={users}
        onClose={() => setEdit(null)} onSaved={() => { setEdit(null); load(); }} />}
    </Stack>
  );
}

function Section({ title, onAdd, addLabel, children }: { title: string; onAdd?: () => void; addLabel: string; children: React.ReactNode }) {
  return (
    <Paper withBorder>
      <Group justify="space-between" p="md" pb="xs">
        <Title order={5}>{title}</Title>
        {onAdd && <Button size="xs" variant="light" leftSection={<IconPlus size={14} />} onClick={onAdd}>{addLabel}</Button>}
      </Group>
      {children}
    </Paper>
  );
}

function useSave(url: string, isNew: boolean, onSaved: () => void) {
  const { t } = useTranslation();
  const [busy, setBusy] = useState(false);
  return {
    busy,
    save: async (body: object) => {
      setBusy(true);
      try {
        await api(url, { method: isNew ? 'POST' : 'PATCH', body });
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

function PlanForm({ plan, postable, currencies, onClose, onSaved }: {
  plan: Plan | null; postable: { value: string; label: string }[]; currencies: string[]; onClose: () => void; onSaved: () => void;
}) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const [f, setF] = useState({
    name: plan?.name ?? '', billing_type: plan?.billing_type ?? 'fixed', currency: plan?.currency ?? company?.base_currency ?? 'USD',
    base_fee: plan ? String(Number(plan.base_fee)) : '', unit_price: plan ? String(Number(plan.unit_price)) : '',
    unit_label: plan?.unit_label ?? 'kWh', revenue_account_id: plan?.revenue_account_id ?? '', is_active: plan?.is_active ?? true,
  });
  const { busy, save } = useSave(`/companies/${company?.id}/plans${plan ? `/${plan.id}` : ''}`, !plan, onSaved);
  const set = (k: keyof typeof f) => (v: string | boolean) => setF((x) => ({ ...x, [k]: v }));
  return (
    <Modal opened onClose={onClose} title={plan?.name ?? t('plans.new')}>
      <form onSubmit={(e) => { e.preventDefault(); void save(f); }}>
        <Stack>
          <TextInput label={t('plans.name')} required value={f.name} onChange={(e) => set('name')(e.currentTarget.value)} placeholder="5 Amp, 10 Amp, Internet 20 Mbps…" data-autofocus />
          <SegmentedControl value={f.billing_type} onChange={(v) => set('billing_type')(v)}
            data={[{ value: 'fixed', label: t('plans.fixed') }, { value: 'metered', label: t('plans.metered') }]} />
          <Group grow>
            <Select label={t('plans.currency')} searchable data={currencies} value={f.currency} allowDeselect={false} onChange={(v) => v && set('currency')(v)} />
            <TextInput label={t('plans.baseFee')} dir="ltr" value={f.base_fee} onChange={(e) => set('base_fee')(e.currentTarget.value.replace(/[^\d.]/g, ''))} />
          </Group>
          {f.billing_type === 'metered' && (
            <Group grow>
              <TextInput label={t('plans.unitPrice')} dir="ltr" value={f.unit_price} onChange={(e) => set('unit_price')(e.currentTarget.value.replace(/[^\d.]/g, ''))} />
              <TextInput label={t('plans.unit')} value={f.unit_label} onChange={(e) => set('unit_label')(e.currentTarget.value)} />
            </Group>
          )}
          <Select label={t('plans.revenue')} searchable clearable data={postable} value={f.revenue_account_id || null}
            placeholder={t('setup.revenue')} onChange={(v) => set('revenue_account_id')(v ?? '')} />
          {plan && <Switch label={t('common.active')} checked={f.is_active} onChange={(e) => set('is_active')(e.currentTarget.checked)} />}
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
            <Button type="submit" loading={busy}>{t('common.save')}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}

function MethodForm({ method, postable, currencies, users, onClose, onSaved }: {
  method: PaymentMethod | null; postable: { value: string; label: string }[]; currencies: string[]; users: User[]; onClose: () => void; onSaved: () => void;
}) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const [f, setF] = useState({
    name: method?.name ?? '', kind: method?.kind ?? 'cash', currency: method?.currency ?? company?.base_currency ?? 'USD',
    account_id: method?.account_id ?? '', is_active: method?.is_active ?? true, user_id: method?.user_id ?? '',
  });
  const { busy, save } = useSave(`/companies/${company?.id}/payment-methods${method ? `/${method.id}` : ''}`, !method, onSaved);
  const set = (k: keyof typeof f) => (v: string | boolean) => setF((x) => ({ ...x, [k]: v }));
  return (
    <Modal opened onClose={onClose} title={method?.name ?? t('methods.new')}>
      <form onSubmit={(e) => { e.preventDefault(); void save(f); }}>
        <Stack>
          <TextInput label={t('methods.name')} required value={f.name} onChange={(e) => set('name')(e.currentTarget.value)} data-autofocus />
          <Group grow>
            <Select label={t('methods.kind')} data={['cash', 'wallet', 'bank', 'card'].map((k) => ({ value: k, label: t(`methods.kinds.${k}`) }))}
              value={f.kind} allowDeselect={false} onChange={(v) => v && set('kind')(v)} />
            <Select label={t('methods.currency')} searchable data={currencies} value={f.currency} allowDeselect={false} onChange={(v) => v && set('currency')(v)} />
          </Group>
          <Select label={t('methods.account')} required searchable data={postable} value={f.account_id || null} onChange={(v) => set('account_id')(v ?? '')} />
          {users.length > 0 && (
            <Select label={t('methods.owner')} clearable placeholder={t('methods.shared')} value={f.user_id || null}
              data={users.filter((u) => u.is_active).map((u) => ({ value: u.id, label: `${u.full_name} · ${t(`roles.${u.role}`)}` }))}
              onChange={(v) => set('user_id')(v ?? '')} />
          )}
          {method && <Switch label={t('common.active')} checked={f.is_active} onChange={(e) => set('is_active')(e.currentTarget.checked)} />}
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
            <Button type="submit" loading={busy} disabled={!f.account_id}>{t('common.save')}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}

function AreaForm({ area, users, onClose, onSaved }: { area: Area | null; users: User[]; onClose: () => void; onSaved: () => void }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const [f, setF] = useState({ name: area?.name ?? '', collector_id: area?.collector_id ?? '', is_active: area?.is_active ?? true });
  const { busy, save } = useSave(`/companies/${company?.id}/areas${area ? `/${area.id}` : ''}`, !area, onSaved);
  return (
    <Modal opened onClose={onClose} title={area?.name ?? t('areas.new')}>
      <form onSubmit={(e) => { e.preventDefault(); void save(f); }}>
        <Stack>
          <TextInput label={t('areas.name')} required value={f.name} onChange={(e) => setF({ ...f, name: e.currentTarget.value })} data-autofocus />
          {users.length > 0 && (
            <Select label={t('areas.collector')} clearable placeholder={t('areas.none')} value={f.collector_id || null}
              data={users.filter((u) => u.is_active).map((u) => ({ value: u.id, label: u.full_name }))}
              onChange={(v) => setF({ ...f, collector_id: v ?? '' })} />
          )}
          {area && <Switch label={t('common.active')} checked={f.is_active} onChange={(e) => setF({ ...f, is_active: e.currentTarget.checked })} />}
          <Text size="xs" c="dimmed" />
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
            <Button type="submit" loading={busy}>{t('common.save')}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}
