import { useCallback, useEffect, useState } from 'react';
import { Badge, Button, FileButton, Group, Loader, Modal, NumberInput, Paper, Stack, Switch, Table, Text, Textarea, TextInput, Title } from '@mantine/core';
import { useDisclosure } from '@mantine/hooks';
import { IconFileDescription, IconPlus, IconUpload } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { api, ApiError, type Company, type Me } from '../api';
import { formatDate, notifyError, notifySaved } from '../util';
import { useCompany } from '../company';
import { Select } from '@mantine/core';

export default function Companies({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const { reload } = useCompany();
  const [rows, setRows] = useState<Company[] | null>(null);
  const [opened, modal] = useDisclosure(false);
  const [docs, setDocs] = useState<Company | null>(null);
  const isAdmin = me.role === 'admin';

  const load = useCallback(() => {
    api<Company[]>('/companies').then(setRows).catch((e) => notifyError(t, e));
    reload();
  }, [t, reload]);
  useEffect(load, [load]);

  async function setLock(c: Company, lock: string) {
    try {
      const updated = await api<Company>(`/companies/${c.id}`, { method: 'PATCH', body: { lock_date: lock } });
      setRows((r) => r?.map((x) => (x.id === c.id ? updated : x)) ?? null);
      notifySaved(t);
    } catch (e) {
      notifyError(t, e);
    }
  }

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
                  <Table.Th>{t('companies.lockDate')}</Table.Th>
                  <Table.Th />
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
                    <Table.Td>
                      {isAdmin ? (
                        <TextInput type="date" size="xs" w={150} defaultValue={c.lock_date ?? ''} title={t('companies.lockHint')}
                          onBlur={(e) => { const v = e.currentTarget.value; if (v !== (c.lock_date ?? '')) void setLock(c, v); }} />
                      ) : (c.lock_date ? formatDate(c.lock_date) : '—')}
                    </Table.Td>
                    <Table.Td>{isAdmin && <Button size="compact-xs" variant="subtle" leftSection={<IconFileDescription size={12} />} onClick={() => setDocs(c)}>{t('docs.title')}</Button>}</Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </Paper>
      <NewCompany opened={opened} onClose={modal.close} onCreated={load} />
      {docs && <DocSettings company={docs} onClose={() => setDocs(null)} />}
    </Stack>
  );
}

function NewCompany({ opened, onClose, onCreated }: { opened: boolean; onClose: () => void; onCreated: () => void }) {
  const { t } = useTranslation();
  const [nameEn, setNameEn] = useState('');
  const [nameAr, setNameAr] = useState('');
  const [baseCur, setBaseCur] = useState<string | null>('USD');
  const [localCur, setLocalCur] = useState<string | null>('LBP');
  const [busy, setBusy] = useState(false);
  const { currencies } = useCompany();
  const curOptions = currencies.map((c) => ({ value: c.code, label: `${c.code} · ${c.name}` }));

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      await api('/companies', { method: 'POST', body: { name_en: nameEn, name_ar: nameAr, base_currency: baseCur, local_currency: localCur } });
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
          <Select label={t('companies.base')} searchable allowDeselect={false} data={curOptions} value={baseCur} onChange={setBaseCur} />
          <Select label={t('companies.local')} searchable allowDeselect={false} data={curOptions} value={localCur} onChange={setLocalCur} />
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
            <Button type="submit" loading={busy}>{t('common.save')}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}

interface DocSettingsData { logo: string; doc_header: string; doc_footer: string; doc_terms: string; tax_id: string; quote_valid_days: number }

/** Logo, header / footer text, terms and tax number printed on quotations, invoices and statements. */
function DocSettings({ company, onClose }: { company: Company; onClose: () => void }) {
  const { t } = useTranslation();
  const [f, setF] = useState<DocSettingsData | null>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    api<DocSettingsData>(`/companies/${company.id}/documents/settings`).then(setF).catch((e) => { notifyError(t, e); onClose(); });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [company.id]);

  function pickLogo(file: File | null) {
    if (!file || !f) return;
    if (file.size > 280_000) {
      notifyError(t, new ApiError(400, 'logo_too_big'));
      return;
    }
    const r = new FileReader();
    r.onload = () => setF({ ...f, logo: String(r.result) });
    r.readAsDataURL(file);
  }

  async function save() {
    if (!f) return;
    setBusy(true);
    try {
      setF(await api<DocSettingsData>(`/companies/${company.id}/documents/settings`, { method: 'PUT', body: f }));
      notifySaved(t);
      onClose();
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened onClose={onClose} size="lg" title={`${t('docs.title')} · ${company.name_en}`}>
      {!f ? <Group justify="center" p="xl"><Loader size="sm" /></Group> : (
        <Stack>
          <Text size="sm" c="dimmed">{t('docs.intro')}</Text>
          <Group align="center">
            {f.logo ? <img src={f.logo} alt="" style={{ maxHeight: 72, maxWidth: 200, objectFit: 'contain', border: '1px solid #eee', padding: 4 }} />
              : <Text size="sm" c="dimmed">{t('docs.noLogo')}</Text>}
            <FileButton onChange={pickLogo} accept="image/png,image/jpeg,image/webp,image/svg+xml">
              {(props) => <Button {...props} variant="default" size="xs" leftSection={<IconUpload size={14} />}>{t('docs.uploadLogo')}</Button>}
            </FileButton>
            {f.logo && <Button size="xs" variant="subtle" color="red" onClick={() => setF({ ...f, logo: '' })}>{t('docs.removeLogo')}</Button>}
          </Group>
          <TextInput label={t('docs.taxId')} value={f.tax_id} onChange={(e) => setF({ ...f, tax_id: e.currentTarget.value })} />
          <Textarea label={t('docs.header')} description={t('docs.headerHint')} autosize minRows={2} value={f.doc_header}
            onChange={(e) => setF({ ...f, doc_header: e.currentTarget.value })} />
          <Textarea label={t('docs.footer')} description={t('docs.footerHint')} autosize minRows={2} value={f.doc_footer}
            onChange={(e) => setF({ ...f, doc_footer: e.currentTarget.value })} />
          <Textarea label={t('docs.terms')} description={t('docs.termsHint')} autosize minRows={3} value={f.doc_terms}
            onChange={(e) => setF({ ...f, doc_terms: e.currentTarget.value })} />
          <NumberInput label={t('docs.validDays')} min={1} max={365} w={200} value={f.quote_valid_days}
            onChange={(v) => setF({ ...f, quote_valid_days: Number(v) || 30 })} />
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
            <Button loading={busy} onClick={() => void save()}>{t('common.save')}</Button>
          </Group>
        </Stack>
      )}
    </Modal>
  );
}