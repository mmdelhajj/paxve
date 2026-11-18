import { useCallback, useEffect, useState } from 'react';
import {
  Alert, Badge, Button, Center, Group, List, Loader, Modal, Paper, Progress, SegmentedControl, SimpleGrid, Stack, Table, Text, TextInput,
} from '@mantine/core';
import { useDebouncedValue } from '@mantine/hooks';
import { IconBolt, IconPrinter, IconSearch } from '@tabler/icons-react';
import { EmptyState, ListSkeleton, ShowMore } from '../../components/ListState';
import { useTranslation } from 'react-i18next';
import { can, api, ApiError, type Me } from '../../api';
import { useCompany } from '../../company';
import { formatAmount, formatDate, notifyError, today } from '../../util';
import PageHeader, { Confirm } from '../../components/PageHeader';
import StationSelect from '../../components/StationSelect';
import { currentPeriod, invoiceColor, type Invoice } from '../../billing';
import { InvoiceView } from './docs';

interface Totals { currency: string; count: number; total: string; paid: string; balance: string }
interface RunResult { period?: string; created: number; skipped: { customer: string; reason: string }[]; totals: Record<string, string> }
interface BillingJob { id: string; period: string; status: 'running' | 'done' | 'failed'; total: number; processed: number; created: number;
  skipped_count: number; skipped: { customer: string; reason: string }[]; totals: Record<string, string>; error: string }

export default function Invoices({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [period, setPeriod] = useState(currentPeriod());
  const [status, setStatus] = useState('');
  const [q, setQ] = useState('');
  const [dq] = useDebouncedValue(q, 250);
  const [data, setData] = useState<{ rows: Invoice[]; totals: Totals[] } | null>(null);
  const [view, setView] = useState<string | null>(null);
  const [runOpen, setRunOpen] = useState(false);
  const [limit, setLimit] = useState(200);
  const canManage = can(me, 'billing.create');

  const load = useCallback(() => {
    if (!company) return;
    const p = new URLSearchParams({ period: /^\d{4}-\d{2}$/.test(period) ? period : '', status, q: dq, limit: String(limit) });
    api<{ rows: Invoice[]; totals: Totals[] }>(`/companies/${company.id}/invoices?${p}`).then(setData).catch((e) => notifyError(t, e));
  }, [company, period, status, dq, limit, t]);
  useEffect(load, [load]);

  if (!company) return null;

  return (
    <Stack gap="lg" maw={1200}>
      <PageHeader title={t('invoices.title')} subtitle={company.name_en}>
        <Button variant="default" leftSection={<IconPrinter size={16} />} component="a" href={`#/print-bills/${period}`}>{t('bulk.title')}</Button>
        {canManage && <Button leftSection={<IconBolt size={16} />} onClick={() => setRunOpen(true)}>{t('billing.run')}</Button>}
      </PageHeader>
      <Group gap="sm" wrap="wrap">
        <TextInput type="month" value={period} onChange={(e) => setPeriod(e.currentTarget.value)} aria-label={t('invoices.period')} />
        <SegmentedControl value={status} onChange={setStatus} data={[
          { value: '', label: t('common.all') }, { value: 'unpaid', label: t('invoices.unpaid') },
          { value: 'paid', label: t('invoices.statuses.paid') }, { value: 'void', label: t('invoices.statuses.void') },
        ]} />
        <TextInput leftSection={<IconSearch size={16} />} placeholder={t('customers.search')} value={q} onChange={(e) => setQ(e.currentTarget.value)} w={240} />
      </Group>
      {data && data.totals.length > 0 && (
        <SimpleGrid cols={{ base: 1, sm: 3 }}>
          {data.totals.map((x) => (
            <Paper withBorder p="md" key={x.currency}>
              <Text size="xs" c="dimmed">{x.count} · {x.currency}</Text>
              <Group justify="space-between" mt={4}>
                <div><Text size="xs" c="dimmed">{t('invoices.total')}</Text><Text fw={600} dir="ltr">{formatAmount(x.total, decimalsOf(x.currency))}</Text></div>
                <div><Text size="xs" c="dimmed">{t('invoices.paid')}</Text><Text fw={600} c="teal.7" dir="ltr">{formatAmount(x.paid, decimalsOf(x.currency))}</Text></div>
                <div><Text size="xs" c="dimmed">{t('invoices.balance')}</Text><Text fw={600} c="red.7" dir="ltr">{formatAmount(x.balance, decimalsOf(x.currency))}</Text></div>
              </Group>
            </Paper>
          ))}
        </SimpleGrid>
      )}
      <Paper withBorder>
        {data === null ? <ListSkeleton /> : data.rows.length === 0 ? (
          <EmptyState hint={canManage ? t('dash.noBillsHint') : undefined} />
        ) : (
          <Table.ScrollContainer minWidth={760}>
            <Table verticalSpacing="xs" highlightOnHover key={i18n.language}>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th w={70}>{t('invoices.number')}</Table.Th>
                  <Table.Th>{t('invoices.customer')}</Table.Th>
                  <Table.Th>{t('invoices.date')}</Table.Th>
                  <Table.Th style={{ textAlign: 'end' }}>{t('invoices.total')}</Table.Th>
                  <Table.Th style={{ textAlign: 'end' }}>{t('invoices.balance')}</Table.Th>
                  <Table.Th>{t('invoices.status')}</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {data.rows.map((iv) => (
                  <Table.Tr key={iv.id} style={{ cursor: 'pointer' }} onClick={() => setView(iv.id)}>
                    <Table.Td ff="monospace">{iv.number}{iv.kind === 'refund' && <Badge ml={6} size="xs" color="orange" variant="light">{t('gap.refund')}</Badge>}</Table.Td>
                    <Table.Td><Text size="sm" fw={500}>{iv.customer_name}</Text><Text size="xs" c="dimmed">#{iv.customer_code}{iv.period ? ` · ${iv.period}` : ''}</Text></Table.Td>
                    <Table.Td>{formatDate(iv.issue_date)}</Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(iv.total, decimalsOf(iv.currency))} {iv.currency}</Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{iv.status === 'void' ? '—' : formatAmount(iv.balance, decimalsOf(iv.currency))}</Table.Td>
                    <Table.Td><Badge variant="light" color={invoiceColor[iv.status]}>{t(`invoices.statuses.${iv.status}`)}</Badge></Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
        {data && data.rows.length >= limit && limit < 2000 && <ShowMore onClick={() => setLimit((l) => Math.min(2000, l + 500))} />}
      </Paper>
      {view && <InvoiceView id={view} canVoid={canManage} onClose={() => setView(null)} onChanged={load} />}
      {runOpen && <RunBilling defaultPeriod={period} onClose={() => setRunOpen(false)} onDone={(p) => { setPeriod(p); load(); }} />}
    </Stack>
  );
}

function RunBilling({ defaultPeriod, onClose, onDone }: { defaultPeriod: string; onClose: () => void; onDone: (period: string) => void }) {
  const { t } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [period, setPeriod] = useState(defaultPeriod);
  const [issue, setIssue] = useState(today());
  const [station, setStation] = useState<string | null>(null);
  const [due, setDue] = useState(() => { const d = new Date(); d.setDate(d.getDate() + 10); return d.toISOString().slice(0, 10); });
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<RunResult | null>(null);
  const [job, setJob] = useState<BillingJob | null>(null);

  async function run() {
    if (!company) return;
    setBusy(true);
    try {
      const r = await api<RunResult | BillingJob>(`/companies/${company.id}/billing/run`, { method: 'POST', body: { period, issue_date: issue, due_date: due, station_id: station ?? '' } });
      if ('status' in r && 'id' in r) {
        // large run: processed in the background, poll its progress
        let j: BillingJob = r;
        setJob(j);
        while (j.status === 'running') {
          await new Promise((res) => setTimeout(res, 1500));
          j = await api<BillingJob>(`/companies/${company.id}/billing/jobs/${j.id}`);
          setJob(j);
        }
        if (j.status === 'failed') notifyError(t, new ApiError(500, j.error.split(':')[0] || 'internal'));
        setResult({ created: j.created, skipped: j.skipped, totals: j.totals });
      } else {
        setResult(r);
      }
      onDone(period);
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
      setConfirm(false);
    }
  }

  return (
    <Modal opened onClose={onClose} title={t('billing.run')}>
      {job && job.status === 'running' ? (
        <Stack>
          <Text>{t('gap.billingRunning', { done: job.processed, total: job.total })}</Text>
          <Progress value={job.total ? (job.processed / job.total) * 100 : 0} animated />
          <Text size="xs" c="dimmed">{t('gap.billingRunningHint')}</Text>
        </Stack>
      ) : result ? (
        <Stack>
          <Alert color="teal" title={t('billing.runResult', { n: result.created })}>
            {Object.entries(result.totals).map(([c, v]) => <Text key={c} dir="ltr">{formatAmount(v, decimalsOf(c))} {c}</Text>)}
          </Alert>
          {result.skipped.length > 0 && (
            <Alert color="yellow" title={`${t('billing.skipped')} (${result.skipped.length})`}>
              <List size="sm">
                {result.skipped.slice(0, 30).map((s, i) => <List.Item key={i}>{s.customer}: {t(`billing.reasons.${s.reason}`, { defaultValue: s.reason })}</List.Item>)}
              </List>
            </Alert>
          )}
          <Group justify="flex-end"><Button onClick={onClose}>{t('common.close')}</Button></Group>
        </Stack>
      ) : (
        <Stack>
          <TextInput type="month" label={t('billing.period')} value={period} onChange={(e) => setPeriod(e.currentTarget.value)} />
          <StationSelect value={station} onChange={setStation} label={t('net.station')} w={300} />
          <Group grow>
            <TextInput type="date" label={t('billing.issueDate')} value={issue} onChange={(e) => setIssue(e.currentTarget.value)} />
            <TextInput type="date" label={t('billing.dueDate')} value={due} onChange={(e) => setDue(e.currentTarget.value)} />
          </Group>
          {confirm ? (
            <Confirm text={t('billing.confirmRun', { period })} yesLabel={t('billing.run')} noLabel={t('common.cancel')} color="teal"
              busy={busy} onYes={run} onNo={() => setConfirm(false)} />
          ) : (
            <Group justify="flex-end">
              <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
              <Button onClick={() => setConfirm(true)} disabled={!/^\d{4}-\d{2}$/.test(period)}>{t('billing.run')}</Button>
            </Group>
          )}
        </Stack>
      )}
    </Modal>
  );
}
