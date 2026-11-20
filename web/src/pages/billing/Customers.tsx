import { useCallback, useEffect, useState } from 'react';
import { Badge, Button, Center, Checkbox, Group, Loader, Modal, Paper, Select, Stack, Table, Text, TextInput } from '@mantine/core';
import { useDebouncedValue } from '@mantine/hooks';
import { IconPlus, IconSearch } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { can, api, type Me } from '../../api';
import { useCompany } from '../../company';
import { notifyError, notifySaved } from '../../util';
import PageHeader from '../../components/PageHeader';
import { EmptyState, ListSkeleton, ShowMore } from '../../components/ListState';
import StationSelect from '../../components/StationSelect';
import type { Area, Customer } from '../../billing';
import { MoneyList } from './docs';
import { languages } from '../../i18n';
import { saveTerms, TermsInput } from '../../components/PartnerTerms';

export default function Customers({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const { company } = useCompany();
  const [rows, setRows] = useState<Customer[] | null>(null);
  const [areas, setAreas] = useState<Area[]>([]);
  const [q, setQ] = useState('');
  const [dq] = useDebouncedValue(q, 250);
  const [area, setArea] = useState<string | null>(null);
  const [station, setStation] = useState<string | null>(null);
  const [onlyDue, setOnlyDue] = useState(false);
  const [creating, setCreating] = useState(false);
  const [limit, setLimit] = useState(100);
  const canEdit = can(me, 'customers.create');

  const load = useCallback(() => {
    if (!company) return;
    const p = new URLSearchParams({ q: dq, area_id: area ?? '', station_id: station ?? '', due: onlyDue ? '1' : '', limit: String(limit) });
    api<Customer[]>(`/companies/${company.id}/customers?${p}`).then(setRows).catch((e) => notifyError(t, e));
  }, [company, dq, area, station, onlyDue, limit, t]);
  useEffect(load, [load]);
  useEffect(() => {
    if (company) api<Area[]>(`/companies/${company.id}/areas`).then(setAreas).catch(() => {});
  }, [company]);

  if (!company) return null;

  return (
    <Stack gap="lg" maw={1200}>
      <PageHeader title={t('customers.title')} subtitle={company.display ?? company.name_en}>
        {canEdit && <Button leftSection={<IconPlus size={16} />} onClick={() => setCreating(true)}>{t('customers.new')}</Button>}
      </PageHeader>
      <Group gap="sm" wrap="wrap">
        <TextInput leftSection={<IconSearch size={16} />} placeholder={t('customers.search')} value={q}
          onChange={(e) => setQ(e.currentTarget.value)} w={300} />
        <StationSelect value={station} onChange={setStation} w={200} />
        <Select placeholder={t('readings.allAreas')} clearable value={area} onChange={setArea} w={200}
          data={areas.map((a) => ({ value: a.id, label: a.name }))} />
        <Checkbox label={t('customers.onlyDue')} checked={onlyDue} onChange={(e) => setOnlyDue(e.currentTarget.checked)} />
      </Group>
      <Paper withBorder>
        {rows === null ? <ListSkeleton /> : rows.length === 0 ? (
          <EmptyState />
        ) : (
          <Table.ScrollContainer minWidth={760}>
            <Table verticalSpacing="sm" highlightOnHover key={i18n.language}>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th w={80}>{t('customers.code')}</Table.Th>
                  <Table.Th>{t('customers.name')}</Table.Th>
                  <Table.Th>{t('customers.phone')}</Table.Th>
                  <Table.Th>{t('customers.area')}</Table.Th>
                  <Table.Th>{t('customers.due')}</Table.Th>
                  <Table.Th w={110}>{t('customers.status')}</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {rows.map((c) => (
                  <Table.Tr key={c.id} style={{ cursor: 'pointer' }} onClick={() => { window.location.hash = `#/customer/${c.id}`; }}>
                    <Table.Td ff="monospace">{c.code}</Table.Td>
                    <Table.Td fw={500}>{c.name}</Table.Td>
                    <Table.Td dir="ltr" style={{ textAlign: 'start' }}>{c.phone}</Table.Td>
                    <Table.Td c="dimmed">{c.area_name ?? '—'}</Table.Td>
                    <Table.Td><MoneyList items={c.due} />{c.credit.length > 0 && <MoneyList items={c.credit} kind="credit" />}</Table.Td>
                    <Table.Td>
                      <Badge variant="light" color={c.status === 'active' ? 'teal' : c.status === 'suspended' ? 'orange' : 'gray'}>
                        {t(`customers.statuses.${c.status}`)}
                      </Badge>
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
        {rows && rows.length >= limit && limit < 1000 && <ShowMore onClick={() => setLimit((l) => Math.min(1000, l + 300))} />}
      </Paper>
      {creating && (
        <CustomerForm areas={areas} onClose={() => setCreating(false)}
          onSaved={(c) => { setCreating(false); window.location.hash = `#/customer/${c.id}`; }} />
      )}
    </Stack>
  );
}

export function CustomerForm({ areas, customer, onClose, onSaved }: {
  areas: Area[]; customer?: Customer; onClose: () => void; onSaved: (c: Customer) => void;
}) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const [f, setF] = useState({
    code: customer?.code ?? '', name: customer?.name ?? '', phone: customer?.phone ?? '', phone2: customer?.phone2 ?? '',
    email: customer?.email ?? '', address: customer?.address ?? '', area_id: customer?.area_id ?? '', notes: customer?.notes ?? '',
    status: customer?.status ?? 'active', locale: customer?.locale ?? '',
  });
  const [terms, setTerms] = useState(customer?.due_days == null ? '' : String(customer.due_days));
  const [busy, setBusy] = useState(false);
  const set = (k: keyof typeof f) => (v: string) => setF((x) => ({ ...x, [k]: v }));

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!company) return;
    setBusy(true);
    try {
      const body = customer ? f : { ...f, status: undefined };
      const c = customer
        ? await api<Customer>(`/companies/${company.id}/customers/${customer.id}`, { method: 'PATCH', body })
        : await api<Customer>(`/companies/${company.id}/customers`, { method: 'POST', body });
      await saveTerms(company.id, 'customer_id', c.id, customer?.due_days, terms);
      notifySaved(t);
      onSaved(c);
    } catch (err) {
      notifyError(t, err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened onClose={onClose} title={customer ? customer.name : t('customers.new')} size="lg">
      <form onSubmit={submit}>
        <Stack>
          <Group grow align="flex-start">
            <TextInput label={t('customers.name')} required value={f.name} onChange={(e) => set('name')(e.currentTarget.value)} data-autofocus />
            <TextInput label={`${t('customers.code')} (${t('common.optional')})`} value={f.code} onChange={(e) => set('code')(e.currentTarget.value)} maw={160} />
          </Group>
          <Group grow>
            <TextInput label={t('customers.phone')} dir="ltr" value={f.phone} onChange={(e) => set('phone')(e.currentTarget.value)} />
            <TextInput label={t('customers.phone2')} dir="ltr" value={f.phone2} onChange={(e) => set('phone2')(e.currentTarget.value)} />
          </Group>
          <Group grow>
            <Select label={t('customers.area')} clearable placeholder={t('customers.noArea')} value={f.area_id || null}
              onChange={(v) => set('area_id')(v ?? '')} data={areas.filter((a) => a.is_active).map((a) => ({ value: a.id, label: a.name }))} />
            <TextInput label={t('customers.email')} dir="ltr" value={f.email} onChange={(e) => set('email')(e.currentTarget.value)} />
          </Group>
          <TextInput label={t('customers.address')} value={f.address} onChange={(e) => set('address')(e.currentTarget.value)} />
          <TextInput label={t('customers.notes')} value={f.notes} onChange={(e) => set('notes')(e.currentTarget.value)} />
          <Select label={t('t2.custLang')} clearable placeholder={t('t2.companyDefault')} value={f.locale || null} onChange={(v) => set('locale')(v ?? '')}
            data={languages.map((l) => ({ value: l.code, label: l.label }))} />
          <TermsInput value={terms} onChange={setTerms} />
          {customer && (
            <Select label={t('customers.status')} value={f.status} allowDeselect={false} onChange={(v) => v && set('status')(v)}
              data={['active', 'suspended', 'closed'].map((s) => ({ value: s, label: t(`customers.statuses.${s}`) }))} />
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
