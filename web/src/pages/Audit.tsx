import { useEffect, useState } from 'react';
import { Badge, Button, Group, Loader, Paper, Stack, Table, Text, Title } from '@mantine/core';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { api, type AuditEntry } from '../api';
import { formatDateTime, notifyError } from '../util';

const actionColor: Record<string, string> = { insert: 'teal', update: 'blue', delete: 'red', login: 'gray' };
const hidden = new Set(['id', 'tenant_id', 'created_at']);

function show(v: unknown): string {
  if (v === null || v === undefined || v === '') return '—';
  if (typeof v === 'boolean') return v ? '✓' : '✗';
  return String(v);
}

function changes(e: AuditEntry, t: TFunction): string {
  if (e.action === 'update' && e.before && e.after) {
    const out: string[] = [];
    for (const k of Object.keys(e.after)) {
      if (k === 'password_changed') out.push(t('audit.passwordChanged'));
      else if (!hidden.has(k) && JSON.stringify(e.before[k]) !== JSON.stringify(e.after[k]))
        out.push(`${k}: ${show(e.before[k])} → ${show(e.after[k])}`);
    }
    return out.join(' · ');
  }
  const row = e.after ?? e.before;
  if (!row) return e.ip ? `IP ${e.ip}` : '';
  return ['name_en', 'name_ar', 'full_name', 'email', 'role', 'name']
    .filter((k) => row[k])
    .map((k) => show(row[k]))
    .join(' · ');
}

export default function Audit() {
  const { t, i18n } = useTranslation();
  const [rows, setRows] = useState<AuditEntry[] | null>(null);
  const [more, setMore] = useState(false);
  const [busy, setBusy] = useState(false);
  const PAGE = 200;

  useEffect(() => {
    api<AuditEntry[]>(`/audit?limit=${PAGE}`).then((r) => { setRows(r); setMore(r.length === PAGE); }).catch((e) => notifyError(t, e));
  }, [t]);

  async function loadMore() {
    if (!rows || rows.length === 0) return;
    setBusy(true);
    try {
      const r = await api<AuditEntry[]>(`/audit?limit=${PAGE}&before=${rows[rows.length - 1].id}`);
      setRows([...rows, ...r]);
      setMore(r.length === PAGE);
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Stack gap="lg" maw={1100}>
      <Title order={2}>{t('audit.title')}</Title>
      <Paper withBorder>
        {rows === null ? (
          <Group justify="center" p="xl"><Loader size="sm" /></Group>
        ) : rows.length === 0 ? (
          <Text c="dimmed" ta="center" p="xl">{t('common.empty')}</Text>
        ) : (
          <Table.ScrollContainer minWidth={760}>
            <Table verticalSpacing="xs" striped key={i18n.language}>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>{t('audit.when')}</Table.Th>
                  <Table.Th>{t('audit.who')}</Table.Th>
                  <Table.Th>{t('audit.action')}</Table.Th>
                  <Table.Th>{t('audit.record')}</Table.Th>
                  <Table.Th>{t('audit.changes')}</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {rows.map((e) => (
                  <Table.Tr key={e.id}>
                    <Table.Td style={{ whiteSpace: 'nowrap' }} c="dimmed">{formatDateTime(e.at)}</Table.Td>
                    <Table.Td>
                      {e.actor ?? t('audit.system')}
                      {e.approved_by && <Text size="xs" c="dimmed">{t('audit.approvedBy', { name: e.approved_by })}</Text>}
                    </Table.Td>
                    <Table.Td>
                      <Badge variant="light" color={actionColor[e.action] ?? 'gray'}>
                        {t(`audit.actions.${e.action}`, { defaultValue: e.action })}
                      </Badge>
                    </Table.Td>
                    <Table.Td>{t(`audit.entities.${e.entity}`, { defaultValue: e.entity })}</Table.Td>
                    <Table.Td><Text size="sm" style={{ wordBreak: 'break-word' }}>{changes(e, t)}</Text></Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </Paper>
      {more && (
        <Group justify="center">
          <Button variant="default" loading={busy} onClick={() => void loadMore()}>{t('common.showMore')}</Button>
        </Group>
      )}
    </Stack>
  );
}
