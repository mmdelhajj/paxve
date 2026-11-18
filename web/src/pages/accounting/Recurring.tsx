import { useCallback, useEffect, useState } from 'react';
import {
  ActionIcon, Alert, Badge, Button, Center, Group, Loader, Modal, Paper, SegmentedControl, Select, Stack, Switch, Table, Text, TextInput,
} from '@mantine/core';
import { IconPlayerPlay, IconPlus, IconTrash } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { can, api, type Account, type Me } from '../../api';
import { useCompany } from '../../company';
import { accountLabel, formatAmount, formatDate, notifyError, notifySaved, today } from '../../util';
import PageHeader from '../../components/PageHeader';
import { useMethods } from '../hr/Employees';

interface RecLine { account_id: string; description: string; debit: string; credit: string }
interface Tpl {
  id: string; name: string; kind: 'journal' | 'expense'; frequency: 'monthly' | 'quarterly' | 'yearly'; next_date: string; end_date: string | null;
  is_active: boolean; currency: string; amount: string | null; account_id: string | null; account_code: string | null; method_id: string | null;
  method_name: string | null; lines: RecLine[]; memo: string; runs: number; last_run_at: string | null; last_error: string;
}

export default function Recurring({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [rows, setRows] = useState<Tpl[] | null>(null);
  const [edit, setEdit] = useState<Tpl | 'new' | null>(null);
  const canEdit = can(me, 'accounting.edit');

  const load = useCallback(() => {
    if (!company) return;
    api<Tpl[]>(`/companies/${company.id}/recurring`).then(setRows).catch((e) => notifyError(t, e));
  }, [company, t]);
  useEffect(load, [load]);
  if (!company) return null;

  async function runNow(id: string) {
    if (!company) return;
    try {
      await api(`/companies/${company.id}/recurring/${id}/run`, { method: 'POST', body: {} });
      notifySaved(t);
      load();
    } catch (e) {
      notifyError(t, e);
    }
  }

  const amountOf = (r: Tpl) => r.kind === 'expense' ? Number(r.amount ?? 0) : r.lines.reduce((s, l) => s + Number(l.debit || 0), 0);

  return (
    <Stack gap="lg" maw={1200}>
      <PageHeader title={t('rec.title')} subtitle={t('rec.auto')}>
        {canEdit && <Button leftSection={<IconPlus size={16} />} onClick={() => setEdit('new')}>{t('rec.newTpl')}</Button>}
      </PageHeader>
      <Paper withBorder>
        {rows === null ? <Center p="xl"><Loader size="sm" /></Center> : rows.length === 0 ? <Text c="dimmed" ta="center" p="xl">{t('common.empty')}</Text> : (
          <Table.ScrollContainer minWidth={820}>
            <Table verticalSpacing="sm" key={i18n.language}>
              <Table.Thead><Table.Tr>
                <Table.Th>{t('rec.name')}</Table.Th><Table.Th>{t('rec.frequency')}</Table.Th><Table.Th>{t('rec.nextDate')}</Table.Th>
                <Table.Th style={{ textAlign: 'end' }}>{t('rec.amount')}</Table.Th><Table.Th>{t('rec.runs')}</Table.Th><Table.Th />
              </Table.Tr></Table.Thead>
              <Table.Tbody>
                {rows.map((r) => (
                  <Table.Tr key={r.id} style={{ opacity: r.is_active ? 1 : 0.5 }}>
                    <Table.Td><Text size="sm" fw={500}>{r.name}</Text><Text size="xs" c="dimmed">{t(`rec.kinds.${r.kind}`)}{r.method_name ? ` · ${r.method_name}` : ''}</Text>
                      {r.last_error && <Badge color="red" variant="light" size="xs">{t(`errors.${r.last_error}`, { defaultValue: r.last_error })}</Badge>}</Table.Td>
                    <Table.Td>{t(`rec.frequencies.${r.frequency}`)}</Table.Td>
                    <Table.Td>{formatDate(r.next_date)}</Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(amountOf(r), decimalsOf(r.currency))} {r.currency}</Table.Td>
                    <Table.Td>{r.runs}{r.last_run_at && <Text size="xs" c="dimmed">{formatDate(r.last_run_at)}</Text>}</Table.Td>
                    <Table.Td>
                      {canEdit && (
                        <Group gap={4} justify="flex-end" wrap="nowrap">
                          {r.is_active && <Button size="compact-xs" variant="light" leftSection={<IconPlayerPlay size={12} />} onClick={() => runNow(r.id)}>{t('rec.runNow')}</Button>}
                          <Button size="compact-xs" variant="subtle" onClick={() => setEdit(r)}>{t('common.edit')}</Button>
                        </Group>
                      )}
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </Paper>
      {edit && <TplForm tpl={edit === 'new' ? null : edit} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); load(); }} />}
    </Stack>
  );
}

function TplForm({ tpl, onClose, onSaved }: { tpl: Tpl | null; onClose: () => void; onSaved: () => void }) {
  const { t } = useTranslation();
  const { company, currencies } = useCompany();
  const methods = useMethods();
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [f, setF] = useState({
    name: tpl?.name ?? '', kind: tpl?.kind ?? 'expense', frequency: tpl?.frequency ?? 'monthly', next_date: tpl?.next_date ?? today(),
    end_date: tpl?.end_date ?? '', currency: tpl?.currency ?? company?.base_currency ?? 'USD', amount: tpl?.amount ? String(Number(tpl.amount)) : '',
    account_id: tpl?.account_id ?? '', method_id: tpl?.method_id ?? '', memo: tpl?.memo ?? '', is_active: tpl?.is_active ?? true,
  });
  const [lines, setLines] = useState<RecLine[]>(tpl?.lines?.length ? tpl.lines : [
    { account_id: '', description: '', debit: '', credit: '' }, { account_id: '', description: '', debit: '', credit: '' }]);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (company) api<Account[]>(`/companies/${company.id}/accounts`).then((a) => setAccounts(a.filter((x) => x.is_postable && x.is_active))).catch(() => {});
  }, [company]);
  const opts = accounts.map((a) => ({ value: a.id, label: accountLabel(a) }));
  const dr = lines.reduce((s, l) => s + Number(l.debit || 0), 0);
  const cr = lines.reduce((s, l) => s + Number(l.credit || 0), 0);
  const method = methods.find((m) => m.id === f.method_id);

  async function submit() {
    if (!company) return;
    setBusy(true);
    const body: Record<string, unknown> = { ...f, currency: f.kind === 'expense' && method ? method.currency : f.currency };
    if (f.kind === 'journal') body.lines = lines.filter((l) => l.account_id);
    if (tpl) delete body.kind;
    try {
      if (tpl) await api(`/companies/${company.id}/recurring/${tpl.id}`, { method: 'PATCH', body });
      else await api(`/companies/${company.id}/recurring`, { method: 'POST', body });
      notifySaved(t);
      onSaved();
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened onClose={onClose} title={tpl?.name ?? t('rec.newTpl')} size="xl">
      <Stack>
        <Group grow>
          <TextInput label={t('rec.name')} required value={f.name} onChange={(e) => setF({ ...f, name: e.currentTarget.value })} placeholder="Rent, Internet…" data-autofocus />
          <Select label={t('rec.frequency')} allowDeselect={false} value={f.frequency} onChange={(v) => v && setF({ ...f, frequency: v as Tpl['frequency'] })}
            data={(['monthly', 'quarterly', 'yearly'] as const).map((x) => ({ value: x, label: t(`rec.frequencies.${x}`) }))} />
        </Group>
        {!tpl && <SegmentedControl value={f.kind} onChange={(v) => setF({ ...f, kind: v as Tpl['kind'] })}
          data={(['expense', 'journal'] as const).map((x) => ({ value: x, label: t(`rec.kinds.${x}`) }))} />}
        <Group grow>
          <TextInput type="date" label={t('rec.nextDate')} value={f.next_date} onChange={(e) => setF({ ...f, next_date: e.currentTarget.value })} />
          <TextInput type="date" label={t('rec.endDate')} value={f.end_date} onChange={(e) => setF({ ...f, end_date: e.currentTarget.value })} />
        </Group>
        <TextInput label={t('rec.memo')} value={f.memo} onChange={(e) => setF({ ...f, memo: e.currentTarget.value })} />
        {f.kind === 'expense' ? (
          <Group grow align="flex-end">
            <Select label={t('rec.account')} searchable data={accounts.filter((a) => a.type === 'expense').map((a) => ({ value: a.id, label: accountLabel(a) }))}
              value={f.account_id || null} onChange={(v) => setF({ ...f, account_id: v ?? '' })} />
            <Select label={t('rec.method')} data={methods.map((m) => ({ value: m.id, label: `${m.name} (${m.currency})` }))}
              value={f.method_id || null} onChange={(v) => setF({ ...f, method_id: v ?? '' })} />
            <TextInput label={t('rec.amount')} dir="ltr" value={f.amount} rightSection={<Text size="xs" c="dimmed">{method?.currency}</Text>}
              onChange={(e) => setF({ ...f, amount: e.currentTarget.value.replace(/[^\d.]/g, '') })} />
          </Group>
        ) : (
          <>
            <Select label={t('plans.currency')} w={160} searchable allowDeselect={false} data={currencies.map((c) => c.code)} value={f.currency}
              onChange={(v) => v && setF({ ...f, currency: v })} />
            <Table withTableBorder verticalSpacing={4}>
              <Table.Thead><Table.Tr><Table.Th>{t('journal.account')}</Table.Th><Table.Th>{t('journal.lineDesc')}</Table.Th>
                <Table.Th w={120}>{t('journal.debit')}</Table.Th><Table.Th w={120}>{t('journal.credit')}</Table.Th><Table.Th w={40} /></Table.Tr></Table.Thead>
              <Table.Tbody>
                {lines.map((l, i) => (
                  <Table.Tr key={i}>
                    <Table.Td><Select size="xs" searchable data={opts} value={l.account_id || null}
                      onChange={(v) => setLines((ls) => ls.map((x, j) => (j === i ? { ...x, account_id: v ?? '' } : x)))} /></Table.Td>
                    <Table.Td><TextInput size="xs" value={l.description} onChange={(e) => { const v = e.currentTarget.value; setLines((ls) => ls.map((x, j) => (j === i ? { ...x, description: v } : x))); }} /></Table.Td>
                    <Table.Td><TextInput size="xs" dir="ltr" value={l.debit} onChange={(e) => { const v = e.currentTarget.value.replace(/[^\d.]/g, ''); setLines((ls) => ls.map((x, j) => (j === i ? { ...x, debit: v, credit: v ? '' : x.credit } : x))); }} /></Table.Td>
                    <Table.Td><TextInput size="xs" dir="ltr" value={l.credit} onChange={(e) => { const v = e.currentTarget.value.replace(/[^\d.]/g, ''); setLines((ls) => ls.map((x, j) => (j === i ? { ...x, credit: v, debit: v ? '' : x.debit } : x))); }} /></Table.Td>
                    <Table.Td><ActionIcon variant="subtle" color="gray" disabled={lines.length <= 2} onClick={() => setLines((ls) => ls.filter((_, j) => j !== i))}><IconTrash size={14} /></ActionIcon></Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
            <Group justify="space-between">
              <Button size="xs" variant="subtle" leftSection={<IconPlus size={14} />} onClick={() => setLines((ls) => [...ls, { account_id: '', description: '', debit: '', credit: '' }])}>{t('journal.addLine')}</Button>
              {dr === cr && dr > 0 ? <Badge color="teal" variant="light">{t('journal.balanced')}</Badge> : <Badge color="red" variant="light">{t('journal.difference')}: {Math.round((dr - cr) * 100) / 100}</Badge>}
            </Group>
          </>
        )}
        {tpl && <Switch label={t('rec.active')} checked={f.is_active} onChange={(e) => setF({ ...f, is_active: e.currentTarget.checked })} />}
        {tpl?.last_error && <Alert color="red" variant="light">{t('rec.lastError')}: {t(`errors.${tpl.last_error}`, { defaultValue: tpl.last_error })}</Alert>}
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
          <Button loading={busy} onClick={submit} disabled={!f.name || (f.kind === 'journal' ? !(dr === cr && dr > 0) : !(Number(f.amount) && f.account_id && f.method_id))}>
            {t('common.save')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}
