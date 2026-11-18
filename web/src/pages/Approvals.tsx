import { useCallback, useEffect, useState } from 'react';
import {
  Badge, Button, Center, Checkbox, Code, Group, Loader, Modal, Paper, SegmentedControl, Stack, Table, Text, Textarea, TextInput, Title,
} from '@mantine/core';
import { useTranslation } from 'react-i18next';
import { api, APPROVAL_EVENT, can, type Me } from '../api';
import { useCompany } from '../company';
import { formatAmount, formatDateTime, notifyError, notifySaved } from '../util';

interface Approval {
  id: string;
  kind: 'void' | 'credit_note' | 'payment';
  permission: string;
  method: string;
  path: string;
  body: Record<string, unknown> | null;
  summary: string;
  amount: string | null;
  currency: string | null;
  status: 'pending' | 'approved' | 'rejected' | 'cancelled' | 'failed';
  requested_at: string;
  requested_by: string;
  requested_by_name: string | null;
  decided_at: string | null;
  decided_by_name: string | null;
  note: string;
  error: string | null;
  can_decide: boolean;
}

const statusColor: Record<Approval['status'], string> = {
  pending: 'yellow', approved: 'teal', rejected: 'red', cancelled: 'gray', failed: 'red',
};

/** What the stored call does, in words: "Void · invoices" etc. */
function actionLabel(t: (k: string) => string, a: Approval): string {
  const parts = a.path.split('/').filter(Boolean); // api v1 companies <cid> <thing> <id> <action>
  const thing = parts[4] ?? '';
  const act = parts[6] ?? (a.method === 'POST' ? 'create' : a.method.toLowerCase());
  return `${t(`perm.modules.${a.permission.split('.')[0]}`)} · ${thing} · ${act}`;
}

export default function Approvals({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const { company } = useCompany();
  const [status, setStatus] = useState('pending');
  const [rows, setRows] = useState<Approval[] | null>(null);
  const [open, setOpen] = useState<Approval | null>(null);

  const load = useCallback(() => {
    if (!company) return;
    api<Approval[]>(`/companies/${company.id}/approvals?status=${status === 'all' ? '' : status}`).then(setRows).catch((e) => notifyError(t, e));
  }, [company, status, t]);
  useEffect(load, [load]);
  useEffect(() => {
    window.addEventListener(APPROVAL_EVENT, load);
    return () => window.removeEventListener(APPROVAL_EVENT, load);
  }, [load]);

  if (!company) return null;
  return (
    <Stack gap="lg" maw={1150}>
      <Title order={2}>{t('sec.approvals')}</Title>
      <Text size="sm" c="dimmed">{t('sec.approvalsHelp')}</Text>
      {can(me, 'settings.manage') && <ApprovalRules cid={company.id} />}
      <Group>
        <SegmentedControl value={status} onChange={setStatus} data={[
          { value: 'pending', label: t('sec.st_pending') }, { value: 'approved', label: t('sec.st_approved') },
          { value: 'rejected', label: t('sec.st_rejected') }, { value: 'failed', label: t('sec.st_failed') }, { value: 'all', label: t('common.all') },
        ]} />
      </Group>
      <Paper withBorder>
        {rows === null ? <Center p="lg"><Loader size="sm" /></Center> : rows.length === 0 ? (
          <Text c="dimmed" ta="center" p="xl">{t('common.empty')}</Text>
        ) : (
          <Table.ScrollContainer minWidth={820}>
            <Table verticalSpacing="sm" highlightOnHover key={i18n.language}>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>{t('sec.requested')}</Table.Th>
                  <Table.Th>{t('sec.kind')}</Table.Th>
                  <Table.Th>{t('sec.action')}</Table.Th>
                  <Table.Th>{t('sec.requestedBy')}</Table.Th>
                  <Table.Th ta="end">{t('sec.amount')}</Table.Th>
                  <Table.Th>{t('common.status')}</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {rows.map((a) => (
                  <Table.Tr key={a.id} style={{ cursor: 'pointer' }} onClick={() => setOpen(a)}>
                    <Table.Td><Text size="sm">{formatDateTime(a.requested_at)}</Text></Table.Td>
                    <Table.Td>{t(`sec.kind_${a.kind}`)}</Table.Td>
                    <Table.Td><Text size="sm">{actionLabel(t, a)}</Text>{a.summary && <Text size="xs" c="dimmed">{a.summary}</Text>}</Table.Td>
                    <Table.Td>{a.requested_by_name ?? '—'}</Table.Td>
                    <Table.Td ta="end" dir="ltr">{a.amount ? `${formatAmount(a.amount)} ${a.currency ?? ''}` : ''}</Table.Td>
                    <Table.Td><Badge color={statusColor[a.status]} variant="light">{t(`sec.st_${a.status}`)}</Badge></Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </Paper>
      {open && <Decide me={me} cid={company.id} a={open} onClose={() => setOpen(null)} onDone={() => { setOpen(null); load(); }} />}
    </Stack>
  );
}

function Decide({ me, cid, a, onClose, onDone }: { me: Me; cid: string; a: Approval; onClose: () => void; onDone: () => void }) {
  const { t } = useTranslation();
  const [note, setNote] = useState('');
  const [busy, setBusy] = useState(false);
  async function act(what: 'approve' | 'reject' | 'cancel') {
    setBusy(true);
    try {
      const r = await api<{ status: string }>(`/companies/${cid}/approvals/${a.id}/${what}`, { method: 'POST', body: what === 'cancel' ? {} : { note } });
      if (r.status === 'failed') notifyError(t, new Error('server_error'));
      else notifySaved(t);
      onDone();
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }
  const mine = a.requested_by === me.id;
  return (
    <Modal opened onClose={onClose} title={t(`sec.kind_${a.kind}`)} size="lg">
      <Stack>
        <Text size="sm">{actionLabel(t, a)}</Text>
        <Text size="sm" c="dimmed">{t('sec.requestedBy')}: {a.requested_by_name} · {formatDateTime(a.requested_at)}</Text>
        {a.body && Object.keys(a.body).length > 0 && (
          <Code block dir="ltr" style={{ whiteSpace: 'pre-wrap' }}>{JSON.stringify(a.body, null, 2)}</Code>
        )}
        {a.status !== 'pending' && (
          <Text size="sm">{t(`sec.st_${a.status}`)}{a.decided_by_name ? ` · ${a.decided_by_name}` : ''}{a.note ? ` — ${a.note}` : ''}</Text>
        )}
        {a.error && <Code block dir="ltr" c="red">{a.error}</Code>}
        {a.status === 'pending' && a.can_decide && !mine && (
          <>
            <Textarea label={t('sec.note')} autosize minRows={1} value={note} onChange={(e) => setNote(e.currentTarget.value)} />
            <Group justify="flex-end">
              <Button color="red" variant="light" loading={busy} onClick={() => void act('reject')}>{t('sec.reject')}</Button>
              <Button loading={busy} onClick={() => void act('approve')}>{t('sec.approve')}</Button>
            </Group>
          </>
        )}
        {a.status === 'pending' && mine && (
          <Group justify="flex-end">
            <Button variant="default" loading={busy} onClick={() => void act('cancel')}>{t('sec.cancelReq')}</Button>
          </Group>
        )}
      </Stack>
    </Modal>
  );
}

function ApprovalRules({ cid }: { cid: string }) {
  const { t } = useTranslation();
  const [rules, setRules] = useState<{ void: boolean; credit_note: boolean; payment_over: string | null } | null>(null);
  const [base, setBase] = useState('');
  const [limit, setLimit] = useState('');
  useEffect(() => {
    api<{ rules: NonNullable<typeof rules>; base_currency: string }>(`/companies/${cid}/approval-rules`)
      .then((r) => { setRules(r.rules); setBase(r.base_currency); setLimit(r.rules.payment_over ?? ''); })
      .catch((e) => notifyError(t, e));
  }, [cid, t]);
  async function save(next: NonNullable<typeof rules>) {
    try {
      const r = await api<{ rules: NonNullable<typeof rules> }>(`/companies/${cid}/approval-rules`, { method: 'PUT', body: next });
      setRules(r.rules);
      notifySaved(t);
    } catch (e) {
      notifyError(t, e);
    }
  }
  if (!rules) return null;
  return (
    <Paper withBorder p="md">
      <Stack gap="sm">
        <Title order={5}>{t('sec.approvalRules')}</Title>
        <Checkbox label={t('sec.ruleVoid')} checked={rules.void} onChange={(e) => void save({ ...rules, void: e.currentTarget.checked })} />
        <Checkbox label={t('sec.ruleCredit')} checked={rules.credit_note} onChange={(e) => void save({ ...rules, credit_note: e.currentTarget.checked })} />
        <Group align="flex-end">
          <TextInput label={t('sec.rulePayment')} description={t('sec.rulePaymentHelp', { currency: base })} dir="ltr" w={320}
            inputMode="decimal" value={limit} onChange={(e) => setLimit(e.currentTarget.value.replace(/[^\d.]/g, ''))} rightSection={<Text size="xs">{base}</Text>} />
          <Button variant="light" disabled={(rules.payment_over ?? '') === limit} onClick={() => void save({ ...rules, payment_over: limit || null })}>
            {t('common.save')}
          </Button>
        </Group>
      </Stack>
    </Paper>
  );
}
