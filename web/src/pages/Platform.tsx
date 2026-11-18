import { useCallback, useEffect, useState } from 'react';
import { Badge, Center, Loader, NumberInput, Paper, Select, SimpleGrid, Stack, Switch, Table, Text, TextInput } from '@mantine/core';
import { useTranslation } from 'react-i18next';
import { api } from '../api';
import { formatDate, notifyError, notifySaved } from '../util';
import PageHeader from '../components/PageHeader';

interface Tenant {
  id: string; name: string; slug: string; plan: string; trial_ends_at: string | null; max_subscribers: number | null;
  is_active: boolean; suspended_reason: string; created_at: string; users: number; companies: number;
  active_subscriptions: number; admin_email: string | null;
}

const plans = ['trial', 'starter', 'business', 'pro'];

export default function Platform() {
  const { t, i18n } = useTranslation();
  const [rows, setRows] = useState<Tenant[] | null>(null);

  const load = useCallback(() => {
    api<Tenant[]>('/platform/tenants').then(setRows).catch((e) => notifyError(t, e));
  }, [t]);
  useEffect(load, [load]);

  async function patch(tn: Tenant, body: Record<string, unknown>) {
    try {
      const u = await api<Tenant>(`/platform/tenants/${tn.id}`, { method: 'PATCH', body });
      setRows((r) => r?.map((x) => (x.id === tn.id ? u : x)) ?? null);
      notifySaved(t);
    } catch (e) {
      notifyError(t, e);
    }
  }

  const count = (p: string) => rows?.filter((r) => r.plan === p && r.is_active).length ?? 0;
  const prices: Record<string, number> = { starter: 25, business: 59, pro: 129 };
  const mrr = rows?.filter((r) => r.is_active).reduce((s, r) => s + (prices[r.plan] ?? 0), 0) ?? 0;

  return (
    <Stack gap="lg" maw={1300}>
      <PageHeader title={t('platform.title')} subtitle={t('platform.subtitle')} />
      {rows && (
        <SimpleGrid cols={{ base: 2, sm: 5 }}>
          <Paper withBorder p="md"><Text size="xs" c="dimmed">{t('platform.organisations')}</Text><Text fw={700} size="xl">{rows.length}</Text></Paper>
          {plans.map((p) => (
            <Paper withBorder p="md" key={p}><Text size="xs" c="dimmed">{t(`platform.plans.${p}`)}</Text><Text fw={700} size="xl">{count(p)}</Text></Paper>
          ))}
          <Paper withBorder p="md"><Text size="xs" c="dimmed">{t('platform.mrr')}</Text><Text fw={700} size="xl" dir="ltr">${mrr}</Text></Paper>
        </SimpleGrid>
      )}
      <Paper withBorder>
        {rows === null ? <Center p="xl"><Loader size="sm" /></Center> : (
          <Table.ScrollContainer minWidth={1100}>
            <Table verticalSpacing="sm" key={i18n.language}>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>{t('platform.organisation')}</Table.Th>
                  <Table.Th w={140}>{t('platform.plan')}</Table.Th>
                  <Table.Th w={160}>{t('platform.trialEnds')}</Table.Th>
                  <Table.Th w={130}>{t('platform.limit')}</Table.Th>
                  <Table.Th>{t('platform.usage')}</Table.Th>
                  <Table.Th w={110}>{t('common.status')}</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {rows.map((tn) => (
                  <Table.Tr key={tn.id} style={{ opacity: tn.is_active ? 1 : 0.55 }}>
                    <Table.Td>
                      <Text fw={600} size="sm">{tn.name}</Text>
                      <Text size="xs" c="dimmed" dir="ltr">{tn.admin_email} · {tn.slug} · {formatDate(tn.created_at)}</Text>
                    </Table.Td>
                    <Table.Td>
                      <Select size="xs" value={tn.plan} allowDeselect={false} onChange={(v) => v && v !== tn.plan && patch(tn, { plan: v })}
                        data={plans.map((p) => ({ value: p, label: t(`platform.plans.${p}`) }))} />
                    </Table.Td>
                    <Table.Td>
                      <TextInput size="xs" type="date" defaultValue={tn.trial_ends_at?.slice(0, 10) ?? ''}
                        onBlur={(e) => { const v = e.currentTarget.value; if (v !== (tn.trial_ends_at?.slice(0, 10) ?? '')) patch(tn, { trial_ends_at: v }); }} />
                    </Table.Td>
                    <Table.Td>
                      <NumberInput size="xs" min={0} placeholder="∞" defaultValue={tn.max_subscribers ?? ''} hideControls
                        onBlur={(e) => { const v = Number(e.currentTarget.value || 0); if (v !== (tn.max_subscribers ?? 0)) patch(tn, { max_subscribers: v }); }} />
                    </Table.Td>
                    <Table.Td>
                      <Text size="xs">{tn.active_subscriptions} {t('platform.subs')} · {tn.users} {t('platform.users')} · {tn.companies} {t('platform.companies')}</Text>
                      {tn.max_subscribers !== null && tn.active_subscriptions >= tn.max_subscribers && <Badge size="xs" color="red">{t('platform.full')}</Badge>}
                    </Table.Td>
                    <Table.Td>
                      <Switch size="sm" checked={tn.is_active} onChange={() => patch(tn, { is_active: !tn.is_active })}
                        label={tn.is_active ? t('common.active') : t('common.inactive')} />
                    </Table.Td>
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
