import { Fragment, useCallback, useEffect, useState } from 'react';
import {
  Alert, Badge, Button, Center, Checkbox, Code, CopyButton, Group, Loader, Modal, Paper, Stack, Switch, Table, Text, TextInput, Title,
} from '@mantine/core';
import { IconCopy, IconPlus, IconWebhook } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { api } from '../api';
import { formatDateTime, notifyError, notifySaved } from '../util';

const events = ['invoice.created', 'payment.created', 'customer.created', 'pos.order.created'];

interface Hook {
  id: string;
  url: string;
  description: string;
  events: string[];
  is_active: boolean;
  created_at: string;
  pending: number;
  failed: number;
  last_sent_at: string | null;
}

interface Delivery {
  id: string;
  event: string;
  status: 'pending' | 'sent' | 'failed';
  attempts: number;
  response_code: number | null;
  last_error: string | null;
  created_at: string;
  sent_at: string | null;
  next_attempt_at: string;
  payload: unknown;
}

const verifySnippet = `// Node.js: check a Paxve webhook
const crypto = require('crypto');
function verify(secret, header, rawBody) {
  const [, t, v1] = header.match(/t=(\\d+),v1=([0-9a-f]+)/) || [];
  const mac = crypto.createHmac('sha256', secret).update(t + '.' + rawBody).digest('hex');
  return v1 && crypto.timingSafeEqual(Buffer.from(mac), Buffer.from(v1)) && Math.abs(Date.now() / 1000 - t) < 300;
}`;

export default function Webhooks({ cid }: { cid: string }) {
  const { t, i18n } = useTranslation();
  const [rows, setRows] = useState<Hook[] | null>(null);
  const [edit, setEdit] = useState<Hook | 'new' | null>(null);
  const [secret, setSecret] = useState<string | null>(null);
  const [log, setLog] = useState<Hook | null>(null);
  const base = `/companies/${cid}`;

  const load = useCallback(() => { api<Hook[]>(`${base}/webhooks`).then(setRows).catch((e) => notifyError(t, e)); }, [base, t]);
  useEffect(load, [load]);

  async function action(h: Hook, what: 'rotate' | 'test') {
    try {
      const r = await api<{ secret?: string }>(`${base}/webhooks/${h.id}/${what}`, { method: 'POST', body: {} });
      if (r.secret) setSecret(r.secret);
      else notifySaved(t);
      load();
    } catch (e) {
      notifyError(t, e);
    }
  }
  async function toggle(h: Hook) {
    try {
      await api(`${base}/webhooks/${h.id}`, { method: 'PATCH', body: { is_active: !h.is_active } });
      load();
    } catch (e) {
      notifyError(t, e);
    }
  }

  return (
    <Paper withBorder>
      <Group justify="space-between" p="md">
        <Group gap="xs"><IconWebhook size={18} /><Title order={5}>{t('sec.webhooks')}</Title></Group>
        <Button size="xs" leftSection={<IconPlus size={14} />} onClick={() => setEdit('new')}>{t('sec.newWebhook')}</Button>
      </Group>
      <Text size="sm" c="dimmed" px="md" pb="sm">{t('sec.webhooksHelp')}</Text>
      {rows === null ? <Center p="lg"><Loader size="sm" /></Center> : rows.length === 0 ? (
        <Text c="dimmed" ta="center" p="lg">{t('common.empty')}</Text>
      ) : (
        <Table.ScrollContainer minWidth={760}>
          <Table verticalSpacing="sm" key={i18n.language}>
            <Table.Thead>
              <Table.Tr>
                <Table.Th>{t('sec.url')}</Table.Th>
                <Table.Th>{t('sec.events')}</Table.Th>
                <Table.Th>{t('sec.deliveries')}</Table.Th>
                <Table.Th>{t('common.status')}</Table.Th>
                <Table.Th />
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {rows.map((h) => (
                <Table.Tr key={h.id} style={{ opacity: h.is_active ? 1 : 0.55 }}>
                  <Table.Td dir="ltr" style={{ textAlign: 'start' }}>
                    <Text size="sm" style={{ wordBreak: 'break-all' }}>{h.url}</Text>
                    {h.description && <Text size="xs" c="dimmed">{h.description}</Text>}
                  </Table.Td>
                  <Table.Td><Group gap={4}>{h.events.map((e) => <Badge key={e} size="xs" variant="light">{e}</Badge>)}</Group></Table.Td>
                  <Table.Td>
                    <Text size="xs">{h.last_sent_at ? formatDateTime(h.last_sent_at) : '—'}</Text>
                    {h.pending > 0 && <Badge size="xs" color="yellow">{t('sec.pending')}: {h.pending}</Badge>}
                    {h.failed > 0 && <Badge size="xs" color="red" ms={4}>{t('sec.failed')}: {h.failed}</Badge>}
                  </Table.Td>
                  <Table.Td><Switch size="xs" checked={h.is_active} onChange={() => void toggle(h)} /></Table.Td>
                  <Table.Td style={{ textAlign: 'end', whiteSpace: 'nowrap' }}>
                    <Button size="compact-xs" variant="subtle" onClick={() => setLog(h)}>{t('sec.deliveries')}</Button>
                    <Button size="compact-xs" variant="subtle" onClick={() => void action(h, 'test')}>{t('sec.test')}</Button>
                    <Button size="compact-xs" variant="subtle" onClick={() => setEdit(h)}>{t('common.edit')}</Button>
                    <Button size="compact-xs" variant="subtle" color="orange" onClick={() => void action(h, 'rotate')}>{t('sec.rotate')}</Button>
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
      )}
      <Stack p="md" gap={4}>
        <Text size="xs" fw={600}>{t('sec.verifyHow')}</Text>
        <Code block dir="ltr">{verifySnippet}</Code>
      </Stack>
      {edit && <HookEditor base={base} hook={edit === 'new' ? null : edit} onClose={() => setEdit(null)}
        onSaved={(s) => { setEdit(null); if (s) setSecret(s); load(); }} />}
      {secret && (
        <Modal opened onClose={() => setSecret(null)} title={t('sec.secret')}>
          <Stack>
            <Alert color="yellow">{t('sec.secretOnce')}</Alert>
            <Code block dir="ltr" style={{ wordBreak: 'break-all' }}>{secret}</Code>
            <Group justify="flex-end">
              <CopyButton value={secret}>{({ copied, copy }) => <Button leftSection={<IconCopy size={16} />} onClick={copy}>{copied ? t('sec.copied') : t('dev.copy')}</Button>}</CopyButton>
              <Button variant="default" onClick={() => setSecret(null)}>{t('common.close')}</Button>
            </Group>
          </Stack>
        </Modal>
      )}
      {log && <DeliveryLog base={base} hook={log} onClose={() => setLog(null)} />}
    </Paper>
  );
}

function HookEditor({ base, hook, onClose, onSaved }: { base: string; hook: Hook | null; onClose: () => void; onSaved: (secret?: string) => void }) {
  const { t } = useTranslation();
  const [url, setUrl] = useState(hook?.url ?? 'https://');
  const [description, setDescription] = useState(hook?.description ?? '');
  const [ev, setEv] = useState<string[]>(hook?.events ?? ['invoice.created', 'payment.created']);
  const [busy, setBusy] = useState(false);
  async function save(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      const r = await api<{ secret?: string }>(hook ? `${base}/webhooks/${hook.id}` : `${base}/webhooks`,
        { method: hook ? 'PATCH' : 'POST', body: { url, description, events: ev } });
      onSaved(r.secret);
    } catch (err) {
      notifyError(t, err);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal opened onClose={onClose} title={hook ? t('common.edit') : t('sec.newWebhook')}>
      <form onSubmit={save}>
        <Stack>
          <TextInput label={t('sec.url')} dir="ltr" required type="url" value={url} onChange={(e) => setUrl(e.currentTarget.value)} />
          <TextInput label={t('sec.description')} value={description} onChange={(e) => setDescription(e.currentTarget.value)} />
          <Checkbox.Group label={t('sec.events')} value={ev} onChange={setEv}>
            <Stack gap={6} mt={4}>{events.map((e) => <Checkbox key={e} value={e} label={<Text size="sm" ff="monospace">{e}</Text>} />)}</Stack>
          </Checkbox.Group>
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
            <Button type="submit" loading={busy} disabled={ev.length === 0}>{t('common.save')}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}

function DeliveryLog({ base, hook, onClose }: { base: string; hook: Hook; onClose: () => void }) {
  const { t } = useTranslation();
  const [rows, setRows] = useState<Delivery[] | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  const load = useCallback(() => { api<Delivery[]>(`${base}/webhooks/${hook.id}/deliveries`).then(setRows).catch((e) => notifyError(t, e)); }, [base, hook.id, t]);
  useEffect(load, [load]);
  async function retry(d: Delivery) {
    try {
      await api(`${base}/webhook-deliveries/${d.id}/retry`, { method: 'POST', body: {} });
      load();
    } catch (e) {
      notifyError(t, e);
    }
  }
  const color = { pending: 'yellow', sent: 'teal', failed: 'red' } as const;
  return (
    <Modal opened onClose={onClose} title={`${t('sec.deliveries')} · ${hook.url}`} size="xl">
      {rows === null ? <Center p="lg"><Loader size="sm" /></Center> : rows.length === 0 ? <Text c="dimmed" ta="center">{t('common.empty')}</Text> : (
        <Table.ScrollContainer minWidth={640}>
          <Table verticalSpacing={6}>
            <Table.Thead>
              <Table.Tr>
                <Table.Th>{t('sec.requested')}</Table.Th>
                <Table.Th>{t('sec.events')}</Table.Th>
                <Table.Th>{t('common.status')}</Table.Th>
                <Table.Th>{t('sec.attempts')}</Table.Th>
                <Table.Th>{t('sec.response')}</Table.Th>
                <Table.Th />
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {rows.map((d) => (
                <Fragment key={d.id}>
                  <Table.Tr style={{ cursor: 'pointer' }} onClick={() => setOpen(open === d.id ? null : d.id)}>
                    <Table.Td><Text size="xs">{formatDateTime(d.created_at)}</Text></Table.Td>
                    <Table.Td ff="monospace"><Text size="xs">{d.event}</Text></Table.Td>
                    <Table.Td><Badge size="xs" color={color[d.status]}>{t(`sec.${d.status}`)}</Badge></Table.Td>
                    <Table.Td>{d.attempts}</Table.Td>
                    <Table.Td><Text size="xs" dir="ltr">{d.response_code ?? ''} {d.last_error ?? ''}</Text></Table.Td>
                    <Table.Td>{d.status !== 'pending' && <Button size="compact-xs" variant="subtle" onClick={(e) => { e.stopPropagation(); void retry(d); }}>{t('sec.retry')}</Button>}</Table.Td>
                  </Table.Tr>
                  {open === d.id && (
                    <Table.Tr>
                      <Table.Td colSpan={6}><Code block dir="ltr" style={{ whiteSpace: 'pre-wrap', maxHeight: 300, overflow: 'auto' }}>{JSON.stringify(d.payload, null, 2)}</Code></Table.Td>
                    </Table.Tr>
                  )}
                </Fragment>
              ))}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
      )}
    </Modal>
  );
}
