import { useCallback, useEffect, useState } from 'react';
import { Badge, Button, Group, Loader, Modal, Paper, Stack, Switch, Table, Text, TextInput, Title } from '@mantine/core';
import { useDisclosure } from '@mantine/hooks';
import { IconPlus } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { api, type Company, type Me } from '../api';
import { formatDate, notifyError, notifySaved } from '../util';

export default function Companies({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const [rows, setRows] = useState<Company[] | null>(null);
  const [opened, modal] = useDisclosure(false);
  const isAdmin = me.role === 'admin';

  const load = useCallback(() => {
    api<Company[]>('/companies').then(setRows).catch((e) => notifyError(t, e));
  }, [t]);
  useEffect(load, [load]);

  async function toggle(c: Company) {
    try {
      const updated = await api<Company>(`/companies/${c.id}`, { method: 'PATCH', body: { is_active: !c.is_active } });
      setRows((r) => r?.map((x) => (x.id === c.id ? updated : x)) ?? null);
    } catch (e) {
      notifyError(t, e);
    }
  }

  return (
    <Stack gap="lg" maw={1100}>
      <Group justify="space-between">
        <Title order={2}>{t('companies.title')}</Title>
        {isAdmin && (
          <Button leftSection={<IconPlus size={16} />} onClick={modal.open}>
            {t('companies.new')}
          </Button>
        )}
      </Group>
      <Paper withBorder>
        {rows === null ? (
          <Group justify="center" p="xl"><Loader size="sm" /></Group>
        ) : rows.length === 0 ? (
          <Text c="dimmed" ta="center" p="xl">{t('common.empty')}</Text>
        ) : (
          <Table.ScrollContainer minWidth={600}>
            <Table verticalSpacing="sm" highlightOnHover>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>{t('companies.nameEn')}</Table.Th>
                  <Table.Th>{t('companies.nameAr')}</Table.Th>
                  <Table.Th>{t('companies.currencies')}</Table.Th>
                  <Table.Th>{t('common.status')}</Table.Th>
                  <Table.Th>{t('common.created')}</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {rows.map((c) => (
                  <Table.Tr key={c.id}>
                    <Table.Td fw={500} dir="ltr" style={{ textAlign: 'start' }}>{c.name_en}</Table.Td>
                    <Table.Td dir="rtl" style={{ textAlign: 'start' }}>{c.name_ar || '—'}</Table.Td>
                    <Table.Td>
                      <Group gap={4}>
                        <Badge variant="light" color="gray">{c.base_currency}</Badge>
                        <Badge variant="light" color="gray">{c.local_currency}</Badge>
                      </Group>
                    </Table.Td>
                    <Table.Td>
                      {isAdmin ? (
                        <Switch checked={c.is_active} onChange={() => toggle(c)}
                          label={c.is_active ? t('common.active') : t('common.inactive')} />
                      ) : (
                        <Badge color={c.is_active ? 'teal' : 'gray'} variant="light">
                          {c.is_active ? t('common.active') : t('common.inactive')}
                        </Badge>
                      )}
                    </Table.Td>
                    <Table.Td c="dimmed" key={i18n.language}>{formatDate(c.created_at)}</Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </Paper>
      <NewCompany opened={opened} onClose={modal.close} onCreated={load} />
    </Stack>
  );
}

function NewCompany({ opened, onClose, onCreated }: { opened: boolean; onClose: () => void; onCreated: () => void }) {
  const { t } = useTranslation();
  const [nameEn, setNameEn] = useState('');
  const [nameAr, setNameAr] = useState('');
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      await api('/companies', { method: 'POST', body: { name_en: nameEn, name_ar: nameAr } });
      notifySaved(t);
      setNameEn('');
      setNameAr('');
      onClose();
      onCreated();
    } catch (err) {
      notifyError(t, err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened={opened} onClose={onClose} title={t('companies.new')}>
      <form onSubmit={submit}>
        <Stack>
          <TextInput label={t('companies.nameEn')} dir="ltr" required value={nameEn}
            onChange={(e) => setNameEn(e.currentTarget.value)} data-autofocus />
          <TextInput label={t('companies.nameAr')} dir="rtl" value={nameAr}
            onChange={(e) => setNameAr(e.currentTarget.value)} />
          <Text size="xs" c="dimmed">{t('companies.currencies')}: USD / LBP</Text>
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
            <Button type="submit" loading={busy}>{t('common.save')}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}
