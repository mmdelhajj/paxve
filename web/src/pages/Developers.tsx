import { useCallback, useEffect, useState } from 'react';
import {
  Alert, Anchor, Badge, Button, Center, Code, CopyButton, Group, Loader, Modal, MultiSelect, Paper, Radio, Select, Stack, Table, Text, TextInput, Title,
} from '@mantine/core';
import { IconCopy, IconExternalLink, IconKey, IconPlus } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { api, type APIKey, type Role } from '../api';
import { useCompany } from '../company';
import { formatDateTime, notifyError } from '../util';
import PageHeader, { Confirm } from '../components/PageHeader';
import Webhooks from './Webhooks';

const scopeLabel = (t: (k: string) => string, p: string) => `${t(`perm.modules.${p.split('.')[0]}`)}: ${t(`perm.actions.${p.split('.')[1]}`)}`;

const roles: Role[] = ['read_only', 'data_entry', 'accountant', 'admin'];

export default function Developers() {
  const { t, i18n } = useTranslation();
  const { company } = useCompany();
  const [keys, setKeys] = useState<APIKey[] | null>(null);
  const [creating, setCreating] = useState(false);
  const [newKey, setNewKey] = useState<string | null>(null);
  const [revoking, setRevoking] = useState<string | null>(null);
  const origin = window.location.origin;

  const load = useCallback(() => {
    api<APIKey[]>('/api-keys').then(setKeys).catch((e) => notifyError(t, e));
  }, [t]);
  useEffect(load, [load]);

  async function revoke(id: string) {
    try {
      await api(`/api-keys/${id}/revoke`, { method: 'POST', body: {} });
      load();
    } catch (e) {
      notifyError(t, e);
    } finally {
      setRevoking(null);
    }
  }

  const example = `curl ${origin}/api/v1/companies${company ? `/${company.id}/accounts` : ''} \\
  -H "Authorization: Bearer pxv_xxxxxxxx_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"`;

  return (
    <Stack gap="lg" maw={1100}>
      <PageHeader title={t('dev.title')} subtitle={t('dev.intro')} />
      <Paper withBorder p="md">
        <Stack gap="xs">
          <Group gap="xs"><Text fw={500}>{t('dev.baseUrl')}:</Text><Code>{origin}/api/v1</Code></Group>
          <Group gap="xs">
            <Anchor href="/api/v1/openapi.yaml" target="_blank">
              <Group gap={4}><IconExternalLink size={14} />{t('dev.docs')}</Group>
            </Anchor>
            <Text size="sm" c="dimmed">— {t('dev.docsHint')}</Text>
          </Group>
          <Text size="sm" fw={500} mt="xs">{t('dev.example')}</Text>
          <Code block dir="ltr">{example}</Code>
        </Stack>
      </Paper>

      <Paper withBorder>
        <Group justify="space-between" p="md">
          <Title order={5}>{t('dev.keys')}</Title>
          <Button size="xs" leftSection={<IconPlus size={14} />} onClick={() => setCreating(true)}>{t('dev.newKey')}</Button>
        </Group>
        {revoking && (
          <Stack px="md" pb="md">
            <Confirm text={t('dev.revokeConfirm')} yesLabel={t('dev.revoke')} noLabel={t('common.cancel')}
              onYes={() => revoke(revoking)} onNo={() => setRevoking(null)} />
          </Stack>
        )}
        {keys === null ? (
          <Center p="xl"><Loader size="sm" /></Center>
        ) : keys.length === 0 ? (
          <Text c="dimmed" ta="center" p="xl">{t('common.empty')}</Text>
        ) : (
          <Table.ScrollContainer minWidth={700}>
            <Table verticalSpacing="sm" key={i18n.language}>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>{t('dev.name')}</Table.Th>
                  <Table.Th>{t('dev.role')}</Table.Th>
                  <Table.Th>{t('common.created')}</Table.Th>
                  <Table.Th>{t('dev.lastUsed')}</Table.Th>
                  <Table.Th />
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {keys.map((k) => (
                  <Table.Tr key={k.id} style={{ opacity: k.revoked_at ? 0.5 : 1 }}>
                    <Table.Td>
                      <Text size="sm" fw={500}>{k.name}</Text>
                      <Text size="xs" c="dimmed" ff="monospace" dir="ltr">pxv_{k.prefix}_…</Text>
                    </Table.Td>
                    <Table.Td>
                      <Badge variant="light">{t(`roles.${k.role}`)}</Badge>
                      {k.scopes && <Text size="xs" c="dimmed" maw={260}>{k.scopes.map((s) => scopeLabel(t, s)).join(', ')}</Text>}
                    </Table.Td>
                    <Table.Td><Text size="sm">{formatDateTime(k.created_at)}</Text><Text size="xs" c="dimmed">{k.created_by}</Text></Table.Td>
                    <Table.Td><Text size="sm">{k.last_used_at ? formatDateTime(k.last_used_at) : t('dev.never')}</Text></Table.Td>
                    <Table.Td style={{ textAlign: 'end' }}>
                      {k.revoked_at ? <Badge color="gray">{t('dev.revoked')}</Badge> : (
                        <Button size="compact-xs" variant="subtle" color="red" onClick={() => setRevoking(k.id)}>{t('dev.revoke')}</Button>
                      )}
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </Paper>

      {company && <Webhooks cid={company.id} />}

      {creating && <NewKey onClose={() => setCreating(false)} onCreated={(key) => { setCreating(false); setNewKey(key); load(); }} />}
      {newKey && (
        <Modal opened onClose={() => setNewKey(null)} title={t('dev.newKey')} size="lg">
          <Stack>
            <Alert color="yellow" icon={<IconKey size={16} />}>{t('dev.copyNow')}</Alert>
            <Code block dir="ltr" style={{ wordBreak: 'break-all', whiteSpace: 'pre-wrap' }}>{newKey}</Code>
            <Group justify="flex-end">
              <CopyButton value={newKey}>
                {({ copied, copy }) => (
                  <Button leftSection={<IconCopy size={16} />} color={copied ? 'teal' : undefined} onClick={copy}>
                    {copied ? t('dev.copied') : t('dev.copy')}
                  </Button>
                )}
              </CopyButton>
              <Button variant="default" onClick={() => setNewKey(null)}>{t('common.close')}</Button>
            </Group>
          </Stack>
        </Modal>
      )}
    </Stack>
  );
}

function NewKey({ onClose, onCreated }: { onClose: () => void; onCreated: (key: string) => void }) {
  const { t } = useTranslation();
  const [name, setName] = useState('');
  const [role, setRole] = useState<string>('read_only');
  const [mode, setMode] = useState('all');
  const [scopes, setScopes] = useState<string[]>([]);
  const [presets, setPresets] = useState<Record<string, string[]>>({});
  const [busy, setBusy] = useState(false);
  useEffect(() => { api<{ presets: Record<string, string[]> }>('/permissions').then((c) => setPresets(c.presets)).catch(() => {}); }, []);
  const allowedScopes = presets[role] ?? [];
  const chosen = scopes.filter((s) => allowedScopes.includes(s));

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      const r = await api<{ key: string }>('/api-keys', { method: 'POST', body: { name, role, ...(mode === 'some' ? { scopes: chosen } : {}) } });
      onCreated(r.key);
    } catch (err) {
      notifyError(t, err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened onClose={onClose} title={t('dev.newKey')}>
      <form onSubmit={submit}>
        <Stack>
          <TextInput label={t('dev.name')} required value={name} onChange={(e) => setName(e.currentTarget.value)} data-autofocus
            placeholder="Website, Power BI…" />
          <Select label={t('dev.role')} value={role} allowDeselect={false} onChange={(v) => v && setRole(v)}
            data={roles.map((r) => ({ value: r, label: t(`roles.${r}`) }))} />
          <Radio.Group label={t('sec.scopes')} value={mode} onChange={setMode}>
            <Stack gap={6} mt={4}>
              <Radio value="all" label={t('sec.scopesAll')} />
              <Radio value="some" label={t('sec.scopesSome')} />
            </Stack>
          </Radio.Group>
          {mode === 'some' && (
            <MultiSelect data={allowedScopes.map((p) => ({ value: p, label: scopeLabel(t, p) }))} value={chosen} onChange={setScopes}
              searchable maxDropdownHeight={260} />
          )}
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
            <Button type="submit" loading={busy} disabled={mode === 'some' && chosen.length === 0}>{t('common.save')}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}
