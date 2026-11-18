import { useCallback, useEffect, useState } from 'react';
import {
  Badge, Button, Group, Loader, Modal, Paper, PasswordInput, Select, Stack, Switch, Table, Text, TextInput, Title,
} from '@mantine/core';
import { useDisclosure } from '@mantine/hooks';
import { IconPlus } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { api, type Me, type Role, type User } from '../api';
import { formatDate, notifyError, notifySaved } from '../util';

const roles: Role[] = ['admin', 'accountant', 'data_entry', 'read_only'];

export default function Users({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const [rows, setRows] = useState<User[] | null>(null);
  const [opened, modal] = useDisclosure(false);
  const roleOptions = roles.map((r) => ({ value: r, label: t(`roles.${r}`) }));

  const load = useCallback(() => {
    api<User[]>('/users').then(setRows).catch((e) => notifyError(t, e));
  }, [t]);
  useEffect(load, [load]);

  async function patch(u: User, body: Partial<Pick<User, 'role' | 'is_active'>>) {
    try {
      const updated = await api<User>(`/users/${u.id}`, { method: 'PATCH', body });
      setRows((r) => r?.map((x) => (x.id === u.id ? updated : x)) ?? null);
      notifySaved(t);
    } catch (e) {
      notifyError(t, e);
    }
  }

  return (
    <Stack gap="lg" maw={1100}>
      <Group justify="space-between">
        <Title order={2}>{t('users.title')}</Title>
        <Button leftSection={<IconPlus size={16} />} onClick={modal.open}>
          {t('users.new')}
        </Button>
      </Group>
      <Paper withBorder>
        {rows === null ? (
          <Group justify="center" p="xl"><Loader size="sm" /></Group>
        ) : (
          <Table.ScrollContainer minWidth={720}>
            <Table verticalSpacing="sm" highlightOnHover>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>{t('users.fullName')}</Table.Th>
                  <Table.Th>{t('users.email')}</Table.Th>
                  <Table.Th>{t('users.role')}</Table.Th>
                  <Table.Th>{t('common.status')}</Table.Th>
                  <Table.Th>{t('common.created')}</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {rows.map((u) => {
                  const self = u.id === me.id;
                  return (
                    <Table.Tr key={u.id}>
                      <Table.Td fw={500}>
                        {u.full_name}
                        {self && <Badge ms={6} size="xs" variant="light">{t('common.you')}</Badge>}
                      </Table.Td>
                      <Table.Td dir="ltr" style={{ textAlign: 'start' }}>{u.email}</Table.Td>
                      <Table.Td w={180}>
                        <Select size="xs" data={roleOptions} value={u.role} disabled={self} allowDeselect={false}
                          onChange={(v) => v && v !== u.role && patch(u, { role: v as Role })} />
                      </Table.Td>
                      <Table.Td>
                        <Switch checked={u.is_active} disabled={self} onChange={() => patch(u, { is_active: !u.is_active })}
                          label={u.is_active ? t('common.active') : t('common.inactive')} />
                      </Table.Td>
                      <Table.Td c="dimmed" key={i18n.language}>{formatDate(u.created_at)}</Table.Td>
                    </Table.Tr>
                  );
                })}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </Paper>
      <NewUser opened={opened} onClose={modal.close} onCreated={load} roleOptions={roleOptions} />
    </Stack>
  );
}

function NewUser({ opened, onClose, onCreated, roleOptions }: {
  opened: boolean; onClose: () => void; onCreated: () => void; roleOptions: { value: string; label: string }[];
}) {
  const { t } = useTranslation();
  const empty = { full_name: '', email: '', role: 'data_entry', locale: 'ar', password: '' };
  const [form, setForm] = useState(empty);
  const [busy, setBusy] = useState(false);
  const set = (k: keyof typeof empty) => (v: string) => setForm((f) => ({ ...f, [k]: v }));

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      await api('/users', { method: 'POST', body: form });
      notifySaved(t);
      setForm(empty);
      onClose();
      onCreated();
    } catch (err) {
      notifyError(t, err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened={opened} onClose={onClose} title={t('users.new')}>
      <form onSubmit={submit}>
        <Stack>
          <TextInput label={t('users.fullName')} required value={form.full_name}
            onChange={(e) => set('full_name')(e.currentTarget.value)} data-autofocus />
          <TextInput label={t('users.email')} type="email" dir="ltr" required value={form.email}
            onChange={(e) => set('email')(e.currentTarget.value)} />
          <Select label={t('users.role')} data={roleOptions} value={form.role} allowDeselect={false}
            onChange={(v) => v && set('role')(v)} />
          <Select label={t('users.language')} value={form.locale} allowDeselect={false}
            data={[{ value: 'ar', label: 'عربي' }, { value: 'en', label: 'English' }]}
            onChange={(v) => v && set('locale')(v)} />
          <PasswordInput label={t('users.password')} dir="ltr" required minLength={10} value={form.password}
            onChange={(e) => set('password')(e.currentTarget.value)} autoComplete="new-password" />
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
            <Button type="submit" loading={busy}>{t('common.save')}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}
