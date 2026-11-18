import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  Badge, Button, Center, Checkbox, Group, Loader, Modal, Paper, Select, Stack, Switch, Table, Text, TextInput, ThemeIcon, Title,
} from '@mantine/core';
import { useDisclosure } from '@mantine/hooks';
import { IconBook2, IconPlus } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { api, type Account, type AccountType, type Me } from '../api';
import { useCompany } from '../company';
import { languages } from '../i18n';
import { accountLabel, accountName, notifyError, notifySaved } from '../util';
import PageHeader from '../components/PageHeader';

const types: AccountType[] = ['asset', 'liability', 'equity', 'income', 'expense'];
const typeColor: Record<AccountType, string> = { asset: 'blue', liability: 'orange', equity: 'grape', income: 'teal', expense: 'red' };

interface Template { key: string; names: Record<string, string>; count: number }

function depthMap(rows: Account[]): Map<string, number> {
  const byId = new Map(rows.map((a) => [a.id, a]));
  const memo = new Map<string, number>();
  const depth = (a: Account, guard = 0): number => {
    if (memo.has(a.id)) return memo.get(a.id)!;
    const p = a.parent_id ? byId.get(a.parent_id) : undefined;
    const d = p && guard < 20 ? depth(p, guard + 1) + 1 : 0;
    memo.set(a.id, d);
    return d;
  };
  rows.forEach((a) => depth(a));
  return memo;
}

export default function Accounts({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const { company } = useCompany();
  const [rows, setRows] = useState<Account[] | null>(null);
  const [editing, setEditing] = useState<Account | null>(null);
  const [opened, modal] = useDisclosure(false);
  const canEdit = me.role === 'admin' || me.role === 'accountant';

  const load = useCallback(() => {
    if (!company) return;
    setRows(null);
    api<Account[]>(`/companies/${company.id}/accounts`).then(setRows).catch((e) => notifyError(t, e));
  }, [company, t]);
  useEffect(load, [load]);

  const depth = useMemo(() => depthMap(rows ?? []), [rows]);

  if (!company) return null;

  return (
    <Stack gap="lg" maw={1200}>
      <PageHeader title={t('accounts.title')} subtitle={company.name_en}>
        {canEdit && rows && rows.length > 0 && (
          <Button leftSection={<IconPlus size={16} />} onClick={() => { setEditing(null); modal.open(); }}>
            {t('accounts.new')}
          </Button>
        )}
      </PageHeader>
      <Paper withBorder>
        {rows === null ? (
          <Center p="xl"><Loader size="sm" /></Center>
        ) : rows.length === 0 ? (
          <EmptyChart canEdit={canEdit} companyId={company.id} onLoaded={load}
            onManual={() => { setEditing(null); modal.open(); }} />
        ) : (
          <Table.ScrollContainer minWidth={700}>
            <Table verticalSpacing={6} highlightOnHover key={i18n.language}>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th w={120}>{t('accounts.code')}</Table.Th>
                  <Table.Th>{t('accounts.name')}</Table.Th>
                  <Table.Th w={140}>{t('accounts.type')}</Table.Th>
                  <Table.Th w={110}>{t('journal.currency')}</Table.Th>
                  <Table.Th w={160} />
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {rows.map((a) => {
                  const d = depth.get(a.id) ?? 0;
                  return (
                    <Table.Tr key={a.id} style={{ opacity: a.is_active ? 1 : 0.5 }}>
                      <Table.Td ff="monospace" fw={a.is_postable ? 400 : 700} dir="ltr" style={{ textAlign: 'start' }}>{a.code}</Table.Td>
                      <Table.Td fw={a.is_postable ? 400 : 700} style={{ paddingInlineStart: `calc(var(--mantine-spacing-sm) + ${d * 20}px)` }}>
                        {accountName(a)}
                        {!a.is_postable && <Badge ms={8} size="xs" variant="outline" color="gray">{t('accounts.header')}</Badge>}
                        {!a.is_active && <Badge ms={8} size="xs" color="gray">{t('common.inactive')}</Badge>}
                      </Table.Td>
                      <Table.Td><Badge variant="light" color={typeColor[a.type]}>{t(`accounts.types.${a.type}`)}</Badge></Table.Td>
                      <Table.Td c="dimmed">{a.currency ?? '—'}</Table.Td>
                      <Table.Td>
                        <Group gap={4} justify="flex-end" wrap="nowrap">
                          {a.is_postable && (
                            <Button size="compact-xs" variant="subtle" component="a" href={`#/ledger/${a.id}`}>{t('accounts.ledger')}</Button>
                          )}
                          {canEdit && (
                            <Button size="compact-xs" variant="subtle" onClick={() => { setEditing(a); modal.open(); }}>{t('common.edit')}</Button>
                          )}
                        </Group>
                      </Table.Td>
                    </Table.Tr>
                  );
                })}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </Paper>
      {opened && (
        <AccountForm companyId={company.id} accounts={rows ?? []} editing={editing} onClose={modal.close}
          onSaved={() => { modal.close(); load(); }} />
      )}
    </Stack>
  );
}

function EmptyChart({ canEdit, companyId, onLoaded, onManual }: {
  canEdit: boolean; companyId: string; onLoaded: () => void; onManual: () => void;
}) {
  const { t, i18n } = useTranslation();
  const [templates, setTemplates] = useState<Template[]>([]);
  const [tpl, setTpl] = useState<string | null>('lebanon');
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api<Template[]>('/chart-templates').then(setTemplates).catch(() => {});
  }, []);

  async function loadTemplate() {
    setBusy(true);
    try {
      await api(`/companies/${companyId}/accounts/load-template`, { method: 'POST', body: { template: tpl, language: i18n.language } });
      notifySaved(t);
      onLoaded();
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Stack align="center" p="xl" gap="md">
      <ThemeIcon size={56} radius="xl" variant="light"><IconBook2 size={28} /></ThemeIcon>
      <Title order={4}>{t('accounts.emptyTitle')}</Title>
      <Text c="dimmed" ta="center" maw={460}>{t('accounts.emptyText')}</Text>
      {canEdit && (
        <Group align="flex-end">
          <Select label={t('accounts.template')} w={280} value={tpl} onChange={setTpl} allowDeselect={false}
            data={templates.map((x) => ({ value: x.key, label: `${x.names[i18n.language] ?? x.names.en} (${x.count})` }))} />
          <Button onClick={loadTemplate} loading={busy} disabled={!tpl}>{t('accounts.load')}</Button>
          <Button variant="default" onClick={onManual}>{t('accounts.new')}</Button>
        </Group>
      )}
    </Stack>
  );
}

function AccountForm({ companyId, accounts, editing, onClose, onSaved }: {
  companyId: string; accounts: Account[]; editing: Account | null; onClose: () => void; onSaved: () => void;
}) {
  const { t } = useTranslation();
  const { currencies } = useCompany();
  const [code, setCode] = useState(editing?.code ?? '');
  const [name, setName] = useState(editing?.name ?? '');
  const [i18nNames, setI18nNames] = useState<Record<string, string>>(editing?.name_i18n ?? {});
  const [type, setType] = useState<AccountType>(editing?.type ?? 'asset');
  const [parent, setParent] = useState<string | null>(editing?.parent_id ?? null);
  const [postable, setPostable] = useState(editing?.is_postable ?? true);
  const [currency, setCurrency] = useState<string | null>(editing?.currency ?? null);
  const [active, setActive] = useState(editing?.is_active ?? true);
  const [busy, setBusy] = useState(false);

  const parents = accounts.filter((a) => !a.is_postable && a.id !== editing?.id);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    const body = {
      code, name, type, name_i18n: i18nNames, parent_id: parent ?? '', is_postable: postable, currency: currency ?? '',
      ...(editing ? { is_active: active } : {}),
    };
    try {
      if (editing) await api(`/companies/${companyId}/accounts/${editing.id}`, { method: 'PATCH', body });
      else await api(`/companies/${companyId}/accounts`, { method: 'POST', body });
      notifySaved(t);
      onSaved();
    } catch (err) {
      notifyError(t, err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened onClose={onClose} title={editing ? accountLabel(editing) : t('accounts.new')} size="lg">
      <form onSubmit={submit}>
        <Stack>
          <Group grow align="flex-start">
            <TextInput label={t('accounts.code')} required dir="ltr" value={code} onChange={(e) => setCode(e.currentTarget.value)} data-autofocus />
            <Select label={t('accounts.type')} value={type} allowDeselect={false} onChange={(v) => v && setType(v as AccountType)}
              data={types.map((x) => ({ value: x, label: t(`accounts.types.${x}`) }))} />
          </Group>
          <TextInput label={t('accounts.name')} required value={name} onChange={(e) => setName(e.currentTarget.value)} />
          <Text size="sm" fw={500}>{t('accounts.translations')}</Text>
          <Group grow>
            {languages.map((l) => (
              <TextInput key={l.code} size="xs" label={l.label} dir={l.dir} value={i18nNames[l.code] ?? ''}
                onChange={(e) => { const v = e.currentTarget.value; setI18nNames((m) => ({ ...m, [l.code]: v })); }} />
            ))}
          </Group>
          <Select label={t('accounts.parent')} clearable searchable value={parent} onChange={setParent}
            placeholder={t('accounts.noParent')} data={parents.map((a) => ({ value: a.id, label: accountLabel(a) }))} />
          <Select label={t('accounts.currency')} clearable searchable value={currency} onChange={setCurrency}
            placeholder={t('accounts.anyCurrency')} data={currencies.map((c) => ({ value: c.code, label: `${c.code} · ${c.name}` }))} />
          <Checkbox label={t('accounts.postable')} checked={postable} onChange={(e) => setPostable(e.currentTarget.checked)} />
          {editing && (
            <Switch label={active ? t('common.active') : t('common.inactive')} checked={active} onChange={(e) => setActive(e.currentTarget.checked)} />
          )}
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
            <Button type="submit" loading={busy}>{t('common.save')}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}
