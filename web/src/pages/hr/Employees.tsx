import { useCallback, useEffect, useState } from 'react';
import {
  Badge, Button, Center, Checkbox, Group, Loader, Modal, Paper, Select, Stack, Table, Tabs, Text, TextInput,
} from '@mantine/core';
import { IconPlus } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { can, api, type Me } from '../../api';
import { useCompany } from '../../company';
import { formatAmount, formatDate, notifyError, notifySaved, today } from '../../util';
import PageHeader from '../../components/PageHeader';
import type { PaymentMethod } from '../../billing';
import { ccLabel, useCostCenters } from '../acc/common';

export interface Employee {
  id: string; code: string; name: string; phone: string; position: string; hire_date: string; status: 'active' | 'inactive';
  currency: string; salary: string; transport: string; nssf_enrolled: boolean; method_id: string | null; method_name: string | null;
  notes: string; advances_open: string; cost_center_id?: string | null;
}
interface Advance { id: string; advance_date: string; currency: string; amount: string; installment: string; recovered: string; remaining: string; method: string; notes: string }
interface Slip { id: string; run_id: string; period: string; run_status: string; currency: string; base: string; net: string; paid: string }

export function useMethods() {
  const { company } = useCompany();
  const [methods, setMethods] = useState<PaymentMethod[]>([]);
  useEffect(() => {
    if (company) api<PaymentMethod[]>(`/companies/${company.id}/payment-methods`).then((m) => setMethods(m.filter((x) => x.is_active))).catch(() => {});
  }, [company]);
  return methods;
}

export default function Employees({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [rows, setRows] = useState<Employee[] | null>(null);
  const [edit, setEdit] = useState<Employee | null | 'new'>(null);
  const [view, setView] = useState<string | null>(null);
  const canEdit = can(me, 'payroll.edit');

  const load = useCallback(() => {
    if (!company) return;
    api<Employee[]>(`/companies/${company.id}/employees`).then(setRows).catch((e) => notifyError(t, e));
  }, [company, t]);
  useEffect(load, [load]);
  if (!company) return null;

  return (
    <Stack gap="lg" maw={1200}>
      <PageHeader title={t('hr.employees')} subtitle={company.display ?? company.name_en}>
        {canEdit && <Button leftSection={<IconPlus size={16} />} onClick={() => setEdit('new')}>{t('hr.newEmployee')}</Button>}
      </PageHeader>
      <Paper withBorder>
        {rows === null ? <Center p="xl"><Loader size="sm" /></Center> : rows.length === 0 ? <Text c="dimmed" ta="center" p="xl">{t('common.empty')}</Text> : (
          <Table.ScrollContainer minWidth={760}>
            <Table verticalSpacing="sm" highlightOnHover key={i18n.language}>
              <Table.Thead><Table.Tr>
                <Table.Th>{t('hr.name')}</Table.Th><Table.Th>{t('hr.position')}</Table.Th><Table.Th style={{ textAlign: 'end' }}>{t('hr.salary')}</Table.Th>
                <Table.Th>{t('hr.nssf')}</Table.Th><Table.Th style={{ textAlign: 'end' }}>{t('hr.advancesOpen')}</Table.Th><Table.Th>{t('hr.status')}</Table.Th><Table.Th />
              </Table.Tr></Table.Thead>
              <Table.Tbody>
                {rows.map((e) => {
                  const d = decimalsOf(e.currency);
                  return (
                    <Table.Tr key={e.id} style={{ cursor: 'pointer', opacity: e.status === 'active' ? 1 : 0.5 }} onClick={() => setView(e.id)}>
                      <Table.Td><Text size="sm" fw={500}>{e.name}</Text><Text size="xs" c="dimmed">{e.code}{e.phone ? ` · ${e.phone}` : ''}</Text></Table.Td>
                      <Table.Td c="dimmed">{e.position || '—'}</Table.Td>
                      <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(e.salary, d)} {e.currency}
                        {Number(e.transport) > 0 && <Text size="xs" c="dimmed">+ {formatAmount(e.transport, d)}</Text>}</Table.Td>
                      <Table.Td>{e.nssf_enrolled ? '✓' : '—'}</Table.Td>
                      <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{Number(e.advances_open) ? formatAmount(e.advances_open, d) : '—'}</Table.Td>
                      <Table.Td><Badge variant="light" color={e.status === 'active' ? 'teal' : 'gray'}>{t(`hr.statuses.${e.status}`)}</Badge></Table.Td>
                      <Table.Td>{canEdit && <Button size="compact-xs" variant="subtle" onClick={(ev) => { ev.stopPropagation(); setEdit(e); }}>{t('common.edit')}</Button>}</Table.Td>
                    </Table.Tr>
                  );
                })}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </Paper>
      {edit && <EmployeeForm emp={edit === 'new' ? null : edit} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); load(); }} />}
      {view && <EmployeeView id={view} canEdit={canEdit} onClose={() => setView(null)} onChanged={load} />}
    </Stack>
  );
}

function EmployeeForm({ emp, onClose, onSaved }: { emp: Employee | null; onClose: () => void; onSaved: () => void }) {
  const { t, i18n } = useTranslation();
  const centers = useCostCenters().filter((x) => x.is_active);
  const { company, currencies } = useCompany();
  const methods = useMethods();
  const [f, setF] = useState({
    name: emp?.name ?? '', phone: emp?.phone ?? '', position: emp?.position ?? '', hire_date: emp?.hire_date ?? today(),
    currency: emp?.currency ?? company?.base_currency ?? 'USD', salary: emp ? String(Number(emp.salary)) : '',
    transport: emp ? String(Number(emp.transport)) : '', nssf_enrolled: emp?.nssf_enrolled ?? true, method_id: emp?.method_id ?? '',
    status: emp?.status ?? 'active', notes: emp?.notes ?? '', cost_center_id: emp?.cost_center_id ?? '',
  });
  const [busy, setBusy] = useState(false);
  const set = (k: keyof typeof f) => (v: string | boolean) => setF((x) => ({ ...x, [k]: v }));

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!company) return;
    setBusy(true);
    try {
      if (emp) await api(`/companies/${company.id}/employees/${emp.id}`, { method: 'PATCH', body: f });
      else await api(`/companies/${company.id}/employees`, { method: 'POST', body: { ...f, status: undefined } });
      notifySaved(t);
      onSaved();
    } catch (err) {
      notifyError(t, err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened onClose={onClose} title={emp ? emp.name : t('hr.newEmployee')} size="lg">
      <form onSubmit={submit}>
        <Stack>
          <Group grow>
            <TextInput label={t('hr.name')} required value={f.name} onChange={(e) => set('name')(e.currentTarget.value)} data-autofocus />
            <TextInput label={t('hr.phone')} dir="ltr" value={f.phone} onChange={(e) => set('phone')(e.currentTarget.value)} />
          </Group>
          <Group grow>
            <TextInput label={t('hr.position')} value={f.position} onChange={(e) => set('position')(e.currentTarget.value)} />
            <TextInput type="date" label={t('hr.hireDate')} value={f.hire_date} onChange={(e) => set('hire_date')(e.currentTarget.value)} />
          </Group>
          <Group grow>
            <Select label={t('hr.currency')} searchable allowDeselect={false} data={currencies.map((c) => c.code)} value={f.currency} onChange={(v) => v && set('currency')(v)} />
            <TextInput label={t('hr.salary')} dir="ltr" value={f.salary} onChange={(e) => set('salary')(e.currentTarget.value.replace(/[^\d.]/g, ''))} />
            <TextInput label={t('hr.transport')} dir="ltr" value={f.transport} onChange={(e) => set('transport')(e.currentTarget.value.replace(/[^\d.]/g, ''))} />
          </Group>
          <Select label={t('hr.method')} clearable data={methods.map((m) => ({ value: m.id, label: `${m.name} (${m.currency})` }))}
            value={f.method_id || null} onChange={(v) => set('method_id')(v ?? '')} />
          {centers.length > 0 && <Select label={t('ax.cc.center')} clearable placeholder={t('ax.cc.none')} value={f.cost_center_id || null}
            data={centers.map((x) => ({ value: x.id, label: ccLabel(x, i18n.language) }))} onChange={(v) => set('cost_center_id')(v ?? '')} />}
          <Checkbox label={t('hr.nssf')} checked={f.nssf_enrolled} onChange={(e) => set('nssf_enrolled')(e.currentTarget.checked)} />
          {emp && (
            <Select label={t('hr.status')} allowDeselect={false} value={f.status} onChange={(v) => v && set('status')(v)}
              data={['active', 'inactive'].map((s) => ({ value: s, label: t(`hr.statuses.${s}`) }))} />
          )}
          <TextInput label={t('hr.notes')} value={f.notes} onChange={(e) => set('notes')(e.currentTarget.value)} />
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
            <Button type="submit" loading={busy}>{t('common.save')}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}

function EmployeeView({ id, canEdit, onClose, onChanged }: { id: string; canEdit: boolean; onClose: () => void; onChanged: () => void }) {
  const { t } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const methods = useMethods();
  const [d, setD] = useState<{ employee: Employee; advances: Advance[]; payslips: Slip[] } | null>(null);
  const [adv, setAdv] = useState({ amount: '', installment: '', method_id: '', date: today(), notes: '' });
  const [busy, setBusy] = useState(false);

  const load = useCallback(() => {
    if (!company) return;
    api<{ employee: Employee; advances: Advance[]; payslips: Slip[] }>(`/companies/${company.id}/employees/${id}`).then(setD).catch((e) => notifyError(t, e));
  }, [company, id, t]);
  useEffect(load, [load]);

  async function give() {
    if (!company) return;
    setBusy(true);
    try {
      await api(`/companies/${company.id}/employees/${id}/advances`, { method: 'POST', body: adv });
      notifySaved(t);
      setAdv({ ...adv, amount: '', installment: '', notes: '' });
      load();
      onChanged();
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  const e = d?.employee;
  const dec = e ? decimalsOf(e.currency) : 2;
  return (
    <Modal opened onClose={onClose} size="xl" title={e?.name ?? '…'}>
      {!d || !e ? <Center p="xl"><Loader size="sm" /></Center> : (
        <Tabs defaultValue="adv">
          <Tabs.List>
            <Tabs.Tab value="adv">{t('hr.advances')}</Tabs.Tab>
            <Tabs.Tab value="slips">{t('hr.payroll')}</Tabs.Tab>
          </Tabs.List>
          <Tabs.Panel value="adv" pt="md">
            <Stack>
              {canEdit && (
                <Paper withBorder p="sm">
                  <Group align="flex-end" wrap="wrap">
                    <TextInput type="date" label={t('invoices.date')} value={adv.date} onChange={(ev) => setAdv({ ...adv, date: ev.currentTarget.value })} w={150} />
                    <TextInput label={t('hr.amount')} dir="ltr" w={130} value={adv.amount} rightSection={<Text size="xs" c="dimmed">{e.currency}</Text>}
                      onChange={(ev) => setAdv({ ...adv, amount: ev.currentTarget.value.replace(/[^\d.]/g, '') })} />
                    <TextInput label={t('hr.installment')} dir="ltr" w={130} value={adv.installment}
                      onChange={(ev) => setAdv({ ...adv, installment: ev.currentTarget.value.replace(/[^\d.]/g, '') })} />
                    <Select label={t('hr.paidFrom')} w={200} value={adv.method_id || null} onChange={(v) => setAdv({ ...adv, method_id: v ?? '' })}
                      data={methods.filter((m) => m.currency === e.currency).map((m) => ({ value: m.id, label: m.name }))} />
                    <Button loading={busy} disabled={!Number(adv.amount) || !adv.method_id} onClick={give}>{t('hr.newAdvance')}</Button>
                  </Group>
                </Paper>
              )}
              {d.advances.length === 0 ? <Text c="dimmed" ta="center">{t('common.empty')}</Text> : (
                <Table verticalSpacing="xs">
                  <Table.Thead><Table.Tr><Table.Th>{t('invoices.date')}</Table.Th><Table.Th style={{ textAlign: 'end' }}>{t('hr.amount')}</Table.Th>
                    <Table.Th style={{ textAlign: 'end' }}>{t('hr.installment')}</Table.Th><Table.Th style={{ textAlign: 'end' }}>{t('hr.recovered')}</Table.Th>
                    <Table.Th style={{ textAlign: 'end' }}>{t('hr.remaining')}</Table.Th><Table.Th>{t('hr.paidFrom')}</Table.Th></Table.Tr></Table.Thead>
                  <Table.Tbody>
                    {d.advances.map((a) => (
                      <Table.Tr key={a.id}>
                        <Table.Td>{formatDate(a.advance_date)}</Table.Td>
                        <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(a.amount, dec)}</Table.Td>
                        <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(a.installment, dec)}</Table.Td>
                        <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(a.recovered, dec)}</Table.Td>
                        <Table.Td ff="monospace" dir="ltr" fw={600} style={{ textAlign: 'end' }}>{formatAmount(a.remaining, dec)}</Table.Td>
                        <Table.Td c="dimmed">{a.method}</Table.Td>
                      </Table.Tr>
                    ))}
                  </Table.Tbody>
                </Table>
              )}
            </Stack>
          </Tabs.Panel>
          <Tabs.Panel value="slips" pt="md">
            {d.payslips.length === 0 ? <Text c="dimmed" ta="center">{t('common.empty')}</Text> : (
              <Table verticalSpacing="xs">
                <Table.Tbody>
                  {d.payslips.map((s) => (
                    <Table.Tr key={s.id}>
                      <Table.Td>{s.period}</Table.Td>
                      <Table.Td><Badge variant="light">{t(`hr.statuses.${s.run_status}`)}</Badge></Table.Td>
                      <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(s.net, decimalsOf(s.currency))} {s.currency}</Table.Td>
                      <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }} c="dimmed">{formatAmount(s.paid, decimalsOf(s.currency))}</Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            )}
          </Tabs.Panel>
        </Tabs>
      )}
    </Modal>
  );
}
