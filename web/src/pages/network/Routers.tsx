import { useCallback, useEffect, useState } from 'react';
import {
  Alert, Badge, Button, Group, Modal, NumberInput, Paper, PasswordInput, Stack, Switch, Table, Text, TextInput, Title,
} from '@mantine/core';
import { IconPlugConnected, IconPlus, IconRefresh } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { api } from '../../api';
import { useCompany } from '../../company';
import { formatDateTime, notifyError, notifySaved } from '../../util';

interface Router {
  id: string; name: string; host: string; port: number; username: string; has_password: boolean; use_tls: boolean; enabled: boolean;
  last_check_at: string | null; last_status: string; identity: string; version: string; subscriptions: number; suspended: number;
}
interface Settings { auto_suspend_enabled: boolean; suspend_after_days: number; reconnect_on_payment: boolean }
interface Action {
  id: string; action: 'disable' | 'enable'; reason: string; status: string; attempts: number; error: string; created_at: string;
  sent_at: string | null; network_username: string; router_name: string; customer_id: string; customer_name: string; created_by: string | null;
}

const statusColor: Record<string, string> = { queued: 'yellow', sending: 'blue', sent: 'teal', failed: 'red', cancelled: 'gray' };

export default function Routers() {
  const { t, i18n } = useTranslation();
  const { company } = useCompany();
  const [routers, setRouters] = useState<Router[]>([]);
  const [settings, setSettings] = useState<Settings | null>(null);
  const [actions, setActions] = useState<Action[]>([]);
  const [edit, setEdit] = useState<Router | 'new' | null>(null);
  const [testing, setTesting] = useState<string | null>(null);
  const [running, setRunning] = useState(false);
  const base = `/companies/${company?.id}`;

  const load = useCallback(() => {
    if (!company) return;
    api<Router[]>(`${base}/routers`).then(setRouters).catch((e) => notifyError(t, e));
    api<Settings>(`${base}/isp/settings`).then(setSettings).catch(() => {});
    api<Action[]>(`${base}/router-actions`).then(setActions).catch(() => {});
  }, [company, base, t]);
  useEffect(load, [load]);

  async function saveSettings(patch: Partial<Settings>) {
    try {
      setSettings(await api<Settings>(`${base}/isp/settings`, { method: 'PUT', body: patch }));
      notifySaved(t);
    } catch (e) {
      notifyError(t, e);
    }
  }

  async function test(r: Router) {
    setTesting(r.id);
    try {
      const res = await api<{ ok: boolean; identity: string; version: string; error: string }>(`${base}/routers/${r.id}/test`, { method: 'POST', body: {} });
      if (res.ok) notifySaved(t);
      load();
    } catch (e) {
      notifyError(t, e);
    } finally {
      setTesting(null);
    }
  }

  async function runNow() {
    setRunning(true);
    try {
      const res = await api<{ suspended: number }>(`${base}/isp/run`, { method: 'POST', body: {} });
      notifySaved(t);
      if (res.suspended >= 0) load();
    } catch (e) {
      notifyError(t, e);
    } finally {
      setRunning(false);
    }
  }

  async function retry(a: Action) {
    try {
      await api(`${base}/router-actions/${a.id}/retry`, { method: 'POST', body: {} });
      load();
    } catch (e) {
      notifyError(t, e);
    }
  }

  return (
    <Stack>
      <Paper withBorder p="md">
        <Group justify="space-between" mb="xs">
          <Title order={6}>{t('isp.rules')}</Title>
          <Button size="xs" variant="light" leftSection={<IconRefresh size={14} />} loading={running} onClick={runNow}>{t('isp.runNow')}</Button>
        </Group>
        {settings && (
          <Group align="flex-end" gap="lg" wrap="wrap">
            <Switch label={t('isp.autoSuspend')} checked={settings.auto_suspend_enabled}
              onChange={(e) => saveSettings({ auto_suspend_enabled: e.currentTarget.checked })} />
            <NumberInput label={t('isp.afterDays')} w={170} min={0} max={365} value={settings.suspend_after_days}
              onBlur={(e) => { const v = Number(e.currentTarget.value); if (v !== settings.suspend_after_days && v >= 0 && v <= 365) void saveSettings({ suspend_after_days: v }); }} />
            <Switch label={t('isp.reconnectOnPay')} checked={settings.reconnect_on_payment}
              onChange={(e) => saveSettings({ reconnect_on_payment: e.currentTarget.checked })} />
          </Group>
        )}
        <Text size="xs" c="dimmed" mt="xs">{t('isp.rulesHint')}</Text>
      </Paper>

      <Paper withBorder>
        <Group justify="space-between" p="sm">
          <Title order={6}>{t('isp.routers')}</Title>
          <Button size="xs" leftSection={<IconPlus size={14} />} onClick={() => setEdit('new')}>{t('isp.newRouter')}</Button>
        </Group>
        {routers.length === 0 ? <Text c="dimmed" ta="center" p="lg">{t('isp.noRouters')}</Text> : (
          <Table.ScrollContainer minWidth={760}>
            <Table verticalSpacing="sm" key={i18n.language}>
              <Table.Thead><Table.Tr>
                <Table.Th>{t('net.name')}</Table.Th><Table.Th>{t('isp.address')}</Table.Th><Table.Th>{t('isp.identity')}</Table.Th>
                <Table.Th>{t('customers.subscriptions')}</Table.Th><Table.Th>{t('isp.lastCheck')}</Table.Th><Table.Th />
              </Table.Tr></Table.Thead>
              <Table.Tbody>
                {routers.map((r) => (
                  <Table.Tr key={r.id} style={{ opacity: r.enabled ? 1 : 0.5 }}>
                    <Table.Td fw={500}>{r.name}{r.use_tls && <Badge ms={6} size="xs" variant="outline">TLS</Badge>}</Table.Td>
                    <Table.Td ff="monospace" dir="ltr">{r.host}:{r.port}</Table.Td>
                    <Table.Td>{r.identity || '—'}{r.version && <Text size="xs" c="dimmed" dir="ltr">RouterOS {r.version}</Text>}</Table.Td>
                    <Table.Td>{r.subscriptions}{r.suspended > 0 && <Badge ms={6} size="xs" color="orange">{r.suspended} {t('isp.suspendedShort')}</Badge>}</Table.Td>
                    <Table.Td>
                      {r.last_check_at ? (
                        <>
                          <Badge size="xs" color={r.last_status === 'ok' ? 'teal' : 'red'} variant="light">{r.last_status === 'ok' ? t('isp.ok') : t('isp.error')}</Badge>
                          <Text size="xs" c="dimmed">{formatDateTime(r.last_check_at)}</Text>
                          {r.last_status !== 'ok' && <Text size="xs" c="red">{r.last_status}</Text>}
                        </>
                      ) : '—'}
                    </Table.Td>
                    <Table.Td>
                      <Group gap={4} wrap="nowrap">
                        <Button size="compact-xs" variant="light" leftSection={<IconPlugConnected size={12} />} loading={testing === r.id} onClick={() => test(r)}>{t('isp.test')}</Button>
                        <Button size="compact-xs" variant="subtle" onClick={() => setEdit(r)}>{t('common.edit')}</Button>
                      </Group>
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </Paper>

      <Paper withBorder>
        <Title order={6} p="sm">{t('isp.actions')}</Title>
        {actions.length === 0 ? <Text c="dimmed" ta="center" p="lg">{t('common.empty')}</Text> : (
          <Table.ScrollContainer minWidth={760}>
            <Table verticalSpacing="xs" key={i18n.language}>
              <Table.Tbody>
                {actions.map((a) => (
                  <Table.Tr key={a.id}>
                    <Table.Td>{formatDateTime(a.created_at)}</Table.Td>
                    <Table.Td><a href={`#/customer/${a.customer_id}`}>{a.customer_name}</a><Text size="xs" c="dimmed" dir="ltr">{a.network_username} @ {a.router_name}</Text></Table.Td>
                    <Table.Td><Badge variant="light" color={a.action === 'disable' ? 'red' : 'teal'}>{t(`isp.do.${a.action}`)}</Badge></Table.Td>
                    <Table.Td c="dimmed">{t(`isp.reasons.${a.reason}`, { defaultValue: a.reason })}</Table.Td>
                    <Table.Td>
                      <Badge variant="light" color={statusColor[a.status] ?? 'gray'}>{t(`isp.st.${a.status}`, { defaultValue: a.status })}</Badge>
                      {a.attempts > 0 && a.status !== 'sent' && <Text size="xs" c="dimmed">{t('isp.attempts', { n: a.attempts })}</Text>}
                      {a.error && a.status !== 'sent' && <Text size="xs" c="red">{a.error}</Text>}
                    </Table.Td>
                    <Table.Td>{a.status === 'failed' && <Button size="compact-xs" variant="subtle" onClick={() => retry(a)}>{t('isp.retry')}</Button>}</Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </Paper>

      {edit && <RouterForm router={edit === 'new' ? null : edit} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); load(); }} />}
    </Stack>
  );
}

function RouterForm({ router, onClose, onSaved }: { router: Router | null; onClose: () => void; onSaved: () => void }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const [f, setF] = useState({
    name: router?.name ?? '', host: router?.host ?? '', port: router?.port ?? 8728, username: router?.username ?? '',
    password: '', use_tls: router?.use_tls ?? false, enabled: router?.enabled ?? true,
  });
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!company) return;
    setBusy(true);
    try {
      const path = router ? `/companies/${company.id}/routers/${router.id}` : `/companies/${company.id}/routers`;
      await api(path, { method: router ? 'PATCH' : 'POST', body: f });
      notifySaved(t);
      onSaved();
    } catch (err) {
      notifyError(t, err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened onClose={onClose} title={router?.name ?? t('isp.newRouter')}>
      <form onSubmit={submit}>
        <Stack>
          <TextInput label={t('net.name')} required value={f.name} onChange={(e) => setF({ ...f, name: e.currentTarget.value })} data-autofocus />
          <Group grow align="flex-start">
            <TextInput label={t('isp.host')} required dir="ltr" placeholder="203.0.113.10" value={f.host}
              onChange={(e) => setF({ ...f, host: e.currentTarget.value.trim() })} />
            <NumberInput label={t('isp.port')} min={1} max={65535} maw={120} value={f.port} onChange={(v) => setF({ ...f, port: Number(v) || 8728 })} />
          </Group>
          <Group grow align="flex-start">
            <TextInput label={t('isp.apiUser')} required dir="ltr" value={f.username} onChange={(e) => setF({ ...f, username: e.currentTarget.value })} />
            <PasswordInput label={t('login.password')} dir="ltr" value={f.password} autoComplete="new-password"
              placeholder={router?.has_password ? t('isp.keepPassword') : ''} onChange={(e) => setF({ ...f, password: e.currentTarget.value })} />
          </Group>
          <Switch label={t('isp.useTls')} checked={f.use_tls}
            onChange={(e) => { const tls = e.currentTarget.checked; setF({ ...f, use_tls: tls, port: tls && f.port === 8728 ? 8729 : !tls && f.port === 8729 ? 8728 : f.port }); }} />
          {router && <Switch label={t('common.active')} checked={f.enabled} onChange={(e) => setF({ ...f, enabled: e.currentTarget.checked })} />}
          <Alert variant="light" color="gray"><Text size="xs">{t('isp.routerHint')}</Text></Alert>
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
            <Button type="submit" loading={busy}>{t('common.save')}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}
