import { printPage } from '../../native';
import { useCallback, useEffect, useState } from 'react';
import {
  Alert, Badge, Button, Center, Group, Loader, Modal, Paper, Select, SimpleGrid, Stack, Table, Text, TextInput, Title,
} from '@mantine/core';
import { IconPlus, IconPrinter, IconSettings } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { can, api, type Account, type Me } from '../../api';
import { useCompany } from '../../company';
import { accountLabel, formatAmount, formatDate, notifyError, notifySaved, today } from '../../util';
import PageHeader, { Confirm } from '../../components/PageHeader';
import { currentPeriod } from '../../billing';
import { useMethods } from './Employees';

interface Total { currency: string; gross: string; net: string; paid: string; nssf: string }
interface Run {
  id: string; period: string; status: 'draft' | 'posted' | 'void'; posting_date: string | null; journal_entry_id: string | null;
  nssf_employee_rate: string; nssf_employer_rate: string; created_at: string; employees: number; totals: Total[];
}
interface Slip {
  id: string; employee_id: string; code: string; name: string; position: string; nssf_enrolled: boolean; currency: string;
  base: string; transport: string; allowances: string; deductions: string; nssf_employee: string; nssf_employer: string;
  advance_recovery: string; net: string; paid: string; notes: string; advances_open: string;
}
interface RunDetail {
  run: Run; payslips: Slip[]; payments: { id: string; payment_date: string; method: string; currency: string; amount: string }[];
  company: { name_en: string; name_ar: string };
}

const statusColor = { draft: 'yellow', posted: 'teal', void: 'gray' } as const;

export default function Payroll({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [runs, setRuns] = useState<Run[] | null>(null);
  const [period, setPeriod] = useState(currentPeriod());
  const [open, setOpen] = useState<string | null>(null);
  const [settings, setSettings] = useState(false);
  const [busy, setBusy] = useState(false);
  const canEdit = can(me, 'payroll.edit');

  const load = useCallback(() => {
    if (!company) return;
    api<Run[]>(`/companies/${company.id}/payroll`).then(setRuns).catch((e) => notifyError(t, e));
  }, [company, t]);
  useEffect(load, [load]);
  if (!company) return null;

  async function create() {
    if (!company) return;
    setBusy(true);
    try {
      const r = await api<RunDetail>(`/companies/${company.id}/payroll`, { method: 'POST', body: { period } });
      load();
      setOpen(r.run.id);
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Stack gap="lg" maw={1200}>
      <PageHeader title={t('hr.payroll')} subtitle={company.display ?? company.name_en}>
        {canEdit && (
          <>
            <Button variant="default" leftSection={<IconSettings size={16} />} onClick={() => setSettings(true)}>{t('hr.settings')}</Button>
            <TextInput type="month" value={period} onChange={(e) => setPeriod(e.currentTarget.value)} aria-label={t('hr.period')} />
            <Button leftSection={<IconPlus size={16} />} loading={busy} onClick={create} disabled={!/^\d{4}-\d{2}$/.test(period)}>{t('hr.run')}</Button>
          </>
        )}
      </PageHeader>
      <Paper withBorder>
        {runs === null ? <Center p="xl"><Loader size="sm" /></Center> : runs.length === 0 ? <Text c="dimmed" ta="center" p="xl">{t('common.empty')}</Text> : (
          <Table verticalSpacing="sm" highlightOnHover key={i18n.language}>
            <Table.Thead><Table.Tr>
              <Table.Th>{t('hr.period')}</Table.Th><Table.Th>{t('hr.employeesCount')}</Table.Th><Table.Th>{t('hr.gross')}</Table.Th>
              <Table.Th>{t('hr.net')}</Table.Th><Table.Th>{t('hr.paid')}</Table.Th><Table.Th>{t('hr.status')}</Table.Th>
            </Table.Tr></Table.Thead>
            <Table.Tbody>
              {runs.map((r) => (
                <Table.Tr key={r.id} style={{ cursor: 'pointer', opacity: r.status === 'void' ? 0.5 : 1 }} onClick={() => setOpen(r.id)}>
                  <Table.Td fw={600}>{r.period}</Table.Td>
                  <Table.Td>{r.employees}</Table.Td>
                  {(['gross', 'net', 'paid'] as const).map((k) => (
                    <Table.Td key={k} ff="monospace" dir="ltr">{r.totals.map((x) => `${formatAmount(x[k], decimalsOf(x.currency))} ${x.currency}`).join(' · ')}</Table.Td>
                  ))}
                  <Table.Td><Badge variant="light" color={statusColor[r.status]}>{t(`hr.statuses.${r.status}`)}</Badge></Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        )}
      </Paper>
      {open && <RunView id={open} canEdit={canEdit} onClose={() => setOpen(null)} onChanged={load} />}
      {settings && <PayrollSettings onClose={() => setSettings(false)} />}
    </Stack>
  );
}

function RunView({ id, canEdit, onClose, onChanged }: { id: string; canEdit: boolean; onClose: () => void; onChanged: () => void }) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const methods = useMethods();
  const [d, setD] = useState<RunDetail | null>(null);
  const [confirm, setConfirm] = useState<'post' | 'void' | 'delete' | null>(null);
  const [payMethod, setPayMethod] = useState<string | null>(null);
  const [payDate, setPayDate] = useState(today());
  const [busy, setBusy] = useState(false);
  const [printing, setPrinting] = useState(false);

  const load = useCallback(() => {
    if (!company) return;
    api<RunDetail>(`/companies/${company.id}/payroll/${id}`).then(setD).catch((e) => { notifyError(t, e); onClose(); });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [company, id, t]);
  useEffect(load, [load]);
  if (!company) return null;
  const base = `/companies/${company.id}/payroll/${id}`;

  async function act(fn: () => Promise<RunDetail | unknown>, close = false) {
    setBusy(true);
    try {
      const r = await fn();
      if (r && typeof r === 'object' && 'run' in (r as object)) setD(r as RunDetail);
      notifySaved(t);
      onChanged();
      if (close) onClose();
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
      setConfirm(null);
    }
  }

  async function patchSlip(s: Slip, field: string, value: string) {
    await act(() => api<RunDetail>(`${base}/payslips/${s.id}`, { method: 'PATCH', body: { [field]: value || '0' } }));
  }

  const run = d?.run;
  const draft = run?.status === 'draft';
  const unpaidCurrencies = [...new Set((d?.payslips ?? []).filter((s) => Number(s.paid) < Number(s.net)).map((s) => s.currency))];

  if (printing && d) return <Payslips d={d} onClose={() => setPrinting(false)} />;

  return (
    <Modal opened onClose={onClose} size="95rem" title={run ? `${t('hr.payroll')} · ${run.period}` : '…'}>
      {!d || !run ? <Center p="xl"><Loader size="sm" /></Center> : (
        <Stack>
          <Group justify="space-between">
            <Group gap="xs">
              <Badge size="lg" variant="light" color={statusColor[run.status]}>{t(`hr.statuses.${run.status}`)}</Badge>
              <Text size="sm" c="dimmed">NSSF {run.nssf_employee_rate}% / {run.nssf_employer_rate}%{run.posting_date ? ` · ${formatDate(run.posting_date)}` : ''}</Text>
            </Group>
            <Button variant="default" size="xs" leftSection={<IconPrinter size={14} />} onClick={() => setPrinting(true)}>{t('hr.print')}</Button>
          </Group>
          <SimpleGrid cols={{ base: 1, sm: 3 }}>
            {run.totals.map((x) => (
              <Paper withBorder p="sm" key={x.currency}>
                <Text size="xs" c="dimmed">{x.currency}</Text>
                <Group justify="space-between"><Text size="sm">{t('hr.gross')}</Text><Text ff="monospace" dir="ltr">{formatAmount(x.gross, decimalsOf(x.currency))}</Text></Group>
                <Group justify="space-between"><Text size="sm">{t('hr.net')}</Text><Text ff="monospace" fw={700} dir="ltr">{formatAmount(x.net, decimalsOf(x.currency))}</Text></Group>
                <Group justify="space-between"><Text size="sm">{t('hr.paid')}</Text><Text ff="monospace" dir="ltr" c="teal.8">{formatAmount(x.paid, decimalsOf(x.currency))}</Text></Group>
              </Paper>
            ))}
          </SimpleGrid>
          <Table.ScrollContainer minWidth={1100}>
            <Table verticalSpacing={4} withTableBorder key={i18n.language}>
              <Table.Thead><Table.Tr>
                <Table.Th>{t('hr.name')}</Table.Th><Table.Th>{t('hr.base')}</Table.Th><Table.Th>{t('hr.transport')}</Table.Th>
                <Table.Th>{t('hr.allowances')}</Table.Th><Table.Th>{t('hr.deductions')}</Table.Th><Table.Th>{t('hr.nssfEmployee')}</Table.Th>
                <Table.Th>{t('hr.recovery')}</Table.Th><Table.Th style={{ textAlign: 'end' }}>{t('hr.net')}</Table.Th><Table.Th style={{ textAlign: 'end' }}>{t('hr.paid')}</Table.Th>
              </Table.Tr></Table.Thead>
              <Table.Tbody>
                {d.payslips.map((s) => {
                  const dec = decimalsOf(s.currency);
                  const cell = (field: keyof Slip) => draft && canEdit ? (
                    <TextInput size="xs" dir="ltr" w={110} defaultValue={String(Number(s[field] as string))} key={`${s.id}-${field}-${s[field]}`}
                      styles={{ input: { fontFamily: 'monospace', textAlign: 'end' } }}
                      onBlur={(e) => { const v = e.currentTarget.value.replace(/[^\d.]/g, ''); if (Number(v) !== Number(s[field])) void patchSlip(s, field, v); }} />
                  ) : <Text size="sm" ff="monospace" dir="ltr">{formatAmount(s[field] as string, dec)}</Text>;
                  return (
                    <Table.Tr key={s.id}>
                      <Table.Td><Text size="sm" fw={500}>{s.name}</Text><Text size="xs" c="dimmed">{s.code} · {s.currency}{s.nssf_enrolled ? '' : ' · no NSSF'}</Text></Table.Td>
                      <Table.Td>{cell('base')}</Table.Td>
                      <Table.Td>{cell('transport')}</Table.Td>
                      <Table.Td>{cell('allowances')}</Table.Td>
                      <Table.Td>{cell('deductions')}</Table.Td>
                      <Table.Td ff="monospace" dir="ltr">{formatAmount(s.nssf_employee, dec)}</Table.Td>
                      <Table.Td>{cell('advance_recovery')}{Number(s.advances_open) > 0 && <Text size="xs" c="dimmed" dir="ltr">/ {formatAmount(s.advances_open, dec)}</Text>}</Table.Td>
                      <Table.Td ff="monospace" dir="ltr" fw={700} style={{ textAlign: 'end' }}>{formatAmount(s.net, dec)}</Table.Td>
                      <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }} c={Number(s.paid) >= Number(s.net) ? 'teal.8' : 'dimmed'}>{formatAmount(s.paid, dec)}</Table.Td>
                    </Table.Tr>
                  );
                })}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
          {d.payments.length > 0 && (
            <Paper withBorder p="sm">
              {d.payments.map((p) => <Text key={p.id} size="sm">{formatDate(p.payment_date)} · {p.method} · <span dir="ltr">{formatAmount(p.amount, decimalsOf(p.currency))} {p.currency}</span></Text>)}
            </Paper>
          )}
          {confirm === 'post' && <Confirm text={t('hr.postConfirm')} yesLabel={t('hr.post')} noLabel={t('common.cancel')} color="teal" busy={busy}
            onYes={() => act(() => api<RunDetail>(`${base}/post`, { method: 'POST', body: {} }))} onNo={() => setConfirm(null)} />}
          {confirm === 'void' && <Confirm text={t('hr.voidConfirm')} yesLabel={t('hr.voidRun')} noLabel={t('common.cancel')} busy={busy}
            onYes={() => act(() => api<RunDetail>(`${base}/void`, { method: 'POST', body: {} }))} onNo={() => setConfirm(null)} />}
          {confirm === 'delete' && <Confirm text={t('hr.deleteRun')} yesLabel={t('hr.deleteRun')} noLabel={t('common.cancel')} busy={busy}
            onYes={() => act(() => api(base, { method: 'DELETE' }), true)} onNo={() => setConfirm(null)} />}
          {canEdit && !confirm && (
            <Group justify="space-between">
              <Group gap="xs">
                {draft && <Button variant="subtle" color="red" onClick={() => setConfirm('delete')}>{t('hr.deleteRun')}</Button>}
                {run.status === 'posted' && d.payments.length === 0 && <Button variant="subtle" color="red" onClick={() => setConfirm('void')}>{t('hr.voidRun')}</Button>}
              </Group>
              <Group gap="xs" align="flex-end">
                {draft && <Button onClick={() => setConfirm('post')}>{t('hr.post')}</Button>}
                {run.status === 'posted' && unpaidCurrencies.length > 0 && (
                  <>
                    <TextInput type="date" size="sm" value={payDate} onChange={(e) => setPayDate(e.currentTarget.value)} />
                    <Select size="sm" w={220} placeholder={t('hr.payFrom')} value={payMethod} onChange={setPayMethod}
                      data={methods.filter((m) => unpaidCurrencies.includes(m.currency)).map((m) => ({ value: m.id, label: `${m.name} (${m.currency})` }))} />
                    <Button loading={busy} disabled={!payMethod} onClick={() => act(() => api<RunDetail>(`${base}/pay`, { method: 'POST', body: { method_id: payMethod, date: payDate } }))}>
                      {t('hr.pay')}
                    </Button>
                  </>
                )}
              </Group>
            </Group>
          )}
        </Stack>
      )}
    </Modal>
  );
}

function Payslips({ d, onClose }: { d: RunDetail; onClose: () => void }) {
  const { t, i18n } = useTranslation();
  const { decimalsOf } = useCompany();
  const rtl = i18n.language === 'ar';
  const name = rtl && d.company.name_ar ? d.company.name_ar : d.company.name_en;
  return (
    <Modal opened onClose={onClose} fullScreen title={t('hr.print')}>
      <Group className="no-print" mb="md"><Button leftSection={<IconPrinter size={16} />} onClick={() => void printPage()}>{t('invoices.print')}</Button>
        <Button variant="default" onClick={onClose}>{t('common.close')}</Button></Group>
      <div className="print-doc" dir={rtl ? 'rtl' : 'ltr'}>
        {d.payslips.map((s) => {
          const dec = decimalsOf(s.currency);
          const row = (label: string, v: string, neg = false) => (
            <tr><td>{label}</td><td className="num">{neg && Number(v) ? '-' : ''}{formatAmount(v, dec)} {s.currency}</td></tr>
          );
          return (
            <div key={s.id} style={{ pageBreakInside: 'avoid', border: '1px solid #999', padding: 12, marginBottom: 16, maxWidth: 520 }}>
              <Title order={5}>{name}</Title>
              <Text size="sm">{t('hr.payslip')} · {d.run.period}</Text>
              <Text fw={600} mt={4}>{s.name} <Text span size="sm" c="dimmed">({s.code}{s.position ? ` · ${s.position}` : ''})</Text></Text>
              <table style={{ marginTop: 8 }}>
                <tbody>
                  {row(t('hr.base'), s.base)}
                  {Number(s.transport) > 0 && row(t('hr.transport'), s.transport)}
                  {Number(s.allowances) > 0 && row(t('hr.allowances'), s.allowances)}
                  {Number(s.deductions) > 0 && row(t('hr.deductions'), s.deductions, true)}
                  {Number(s.nssf_employee) > 0 && row(t('hr.nssfEmployee'), s.nssf_employee, true)}
                  {Number(s.advance_recovery) > 0 && row(t('hr.recovery'), s.advance_recovery, true)}
                  <tr><th>{t('hr.net')}</th><th className="num">{formatAmount(s.net, dec)} {s.currency}</th></tr>
                </tbody>
              </table>
              <Text size="xs" mt={6}>{t('hr.paid')}: <span dir="ltr">{formatAmount(s.paid, dec)}</span></Text>
            </div>
          );
        })}
      </div>
    </Modal>
  );
}

const settingKeys = ['salary_expense_account_id', 'nssf_expense_account_id', 'salary_payable_account_id', 'nssf_payable_account_id', 'advances_account_id'] as const;

function PayrollSettings({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const [s, setS] = useState<Record<string, string | null> | null>(null);
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (!company) return;
    api<Record<string, string | null>>(`/companies/${company.id}/backoffice/settings`).then(setS).catch((e) => notifyError(t, e));
    api<Account[]>(`/companies/${company.id}/accounts`).then((a) => setAccounts(a.filter((x) => x.is_postable && x.is_active))).catch(() => {});
  }, [company, t]);
  const labels: Record<string, string> = {
    salary_expense_account_id: '641 · ' + t('hr.gross'), nssf_expense_account_id: '645 · ' + t('hr.nssfEmployer'),
    salary_payable_account_id: '421 · ' + t('hr.net'), nssf_payable_account_id: '431 · NSSF', advances_account_id: t('hr.advances'),
  };

  async function save() {
    if (!company || !s) return;
    setBusy(true);
    try {
      const body: Record<string, string> = {};
      for (const k of [...settingKeys, 'nssf_employee_rate', 'nssf_employer_rate']) body[k] = s[k] ?? '';
      setS(await api(`/companies/${company.id}/backoffice/settings`, { method: 'PUT', body }));
      notifySaved(t);
      onClose();
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened onClose={onClose} title={t('hr.settings')}>
      {!s ? <Center p="xl"><Loader size="sm" /></Center> : (
        <Stack>
          <Group grow>
            <TextInput label={t('hr.empRate')} dir="ltr" value={s.nssf_employee_rate ?? ''} onChange={(e) => setS({ ...s, nssf_employee_rate: e.currentTarget.value })} />
            <TextInput label={t('hr.erRate')} dir="ltr" value={s.nssf_employer_rate ?? ''} onChange={(e) => setS({ ...s, nssf_employer_rate: e.currentTarget.value })} />
          </Group>
          <Alert color="gray" variant="light">{t('hr.rateNote')}</Alert>
          {settingKeys.map((k) => (
            <Select key={k} label={labels[k]} searchable clearable data={accounts.map((a) => ({ value: a.id, label: accountLabel(a) }))}
              value={s[k]} onChange={(v) => setS({ ...s, [k]: v })} />
          ))}
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
            <Button loading={busy} onClick={save}>{t('common.save')}</Button>
          </Group>
        </Stack>
      )}
    </Modal>
  );
}
