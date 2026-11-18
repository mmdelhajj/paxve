import { useCallback, useEffect, useRef, useState } from 'react';
import {
  Alert, Badge, Button, Checkbox, Code, FileButton, Group, List, NumberInput, Paper, Progress, Select, SimpleGrid, Stack, Table, Text,
  TextInput, ThemeIcon, Title, Tooltip,
} from '@mantine/core';
import { IconAlertTriangle, IconCheck, IconDatabaseImport, IconDownload, IconUpload } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { api, ApiError } from '../api';
import { useCompany } from '../company';
import { errorText, formatAmount, formatDateTime, notifyError, today } from '../util';
import PageHeader, { Confirm } from '../components/PageHeader';

// Migration from the old Magic system: the owner runs tools/magic-export/Export-Magic.ps1 on his Magic computer
// (read-only), uploads the zip here, checks the dry run per Magic company and imports everything in one go.

interface Warning { code: string; count: number; examples?: string[] }
interface Summary {
  company_id: number; name: string; type_id: number;
  counts: Record<string, number>;
  totals: {
    open_bills: Record<string, string>; receivables: Record<string, string>; customer_debits: Record<string, string>;
    customer_credits: Record<string, string>; supplier_balances: Record<string, string>; magic_imbalance_usd: string;
    opening_entry: Record<string, { debit: string; credit: string }>;
  };
  warnings: Warning[];
  subscriptions_by_status: Record<string, number>;
  already_imported: { company_id: string; name: string }[];
}
interface CompanyResult {
  magic_company: number; magic_name: string; company_id: string; company_name: string;
  created: Record<string, number>; updated: Record<string, number>; skipped: Record<string, number>;
  notes: string[]; accounts_created: string[]; financial_skipped: boolean;
}
interface MagicImport {
  id: string; file_name: string; size: number; received: number;
  status: 'uploading' | 'uploaded' | 'invalid' | 'running' | 'done' | 'failed';
  analysis: { manifest: { exported_at: string; database: string; months: number; tables: Record<string, number> }; default_rate: string; companies: Summary[] } | null;
  options: { opening_date: string; rate: string; readings_months: number; include_ended: boolean } | null;
  result: { companies: CompanyResult[]; seconds: number } | null;
  progress: { step?: string; magic_company?: number; done?: number; total?: number; seconds?: number };
  error: string | null; created_at: string; finished_at: string | null; created_by: string | null;
}
interface ImportRow { id: string; file_name: string; size: number; status: MagicImport['status']; created_at: string; created_by: string | null }

const CHUNK = 2 * 1024 * 1024;

async function sha256Hex(buf: ArrayBuffer): Promise<string> {
  const h = await crypto.subtle.digest('SHA-256', buf);
  return Array.from(new Uint8Array(h)).map((b) => b.toString(16).padStart(2, '0')).join('');
}

function toBase64(bytes: Uint8Array): string {
  let s = '';
  for (let i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(s);
}

function Amounts({ m }: { m: Record<string, string> | undefined }) {
  const entries = Object.entries(m ?? {}).filter(([, v]) => Number(v) !== 0);
  if (entries.length === 0) return <Text span c="dimmed">—</Text>;
  return <>{entries.map(([cur, v]) => <Text span key={cur} ff="monospace" me="sm" dir="ltr" style={{ unicodeBidi: 'isolate' }}>{formatAmount(v, cur === 'LBP' ? 0 : 2)} {cur}</Text>)}</>;
}

export default function MagicImport() {
  const { t } = useTranslation();
  const { companies, reload } = useCompany();
  const [imp, setImp] = useState<MagicImport | null>(null);
  const [history, setHistory] = useState<ImportRow[]>([]);
  const [upload, setUpload] = useState<{ sent: number; size: number } | null>(null);
  const [mapping, setMapping] = useState<Record<number, string>>({});
  const [opts, setOpts] = useState({ opening_date: today(), rate: '', readings_months: 12, include_ended: false });
  const [busy, setBusy] = useState(false);
  const [confirm, setConfirm] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const poll = useRef<number | null>(null);

  const loadHistory = useCallback(() => {
    api<ImportRow[]>('/magic/imports').then(setHistory).catch(() => {});
  }, []);
  useEffect(loadHistory, [loadHistory]);
  useEffect(() => () => { if (poll.current) window.clearTimeout(poll.current); }, []);

  const adopt = useCallback((x: MagicImport) => {
    setImp(x);
    if (x.options) {
      setOpts({ opening_date: x.options.opening_date, rate: x.options.rate, readings_months: x.options.readings_months, include_ended: x.options.include_ended });
    }
    if (x.analysis) {
      setMapping((old) => {
        const next: Record<number, string> = {};
        for (const c of x.analysis!.companies) {
          const empty = (c.counts.customers ?? 0) === 0 && (c.counts.gl_lines ?? 0) === 0;
          next[c.company_id] = old[c.company_id] ?? (c.already_imported[0]?.company_id ?? (empty ? 'skip' : 'new'));
        }
        return next;
      });
    }
  }, []);

  const watch = useCallback((id: string) => {
    const tick = () => {
      api<MagicImport>(`/magic/imports/${id}`).then((x) => {
        adopt(x);
        if (x.status === 'running') poll.current = window.setTimeout(tick, 1500);
        else { loadHistory(); reload(); }
      }).catch((e) => { setErr(errorText(t, e)); });
    };
    tick();
  }, [adopt, loadHistory, reload, t]);

  const open = (id: string) => {
    setErr(null);
    api<MagicImport>(`/magic/imports/${id}`).then((x) => { adopt(x); if (x.status === 'running') watch(id); }).catch((e) => notifyError(t, e));
  };

  const onFile = async (file: File | null) => {
    if (!file) return;
    setErr(null); setImp(null); setMapping({});
    try {
      const buf = await file.arrayBuffer();
      const bytes = new Uint8Array(buf);
      const sum = await sha256Hex(buf);
      let x = await api<MagicImport>('/magic/imports', { method: 'POST', body: { file_name: file.name, size: bytes.length, sha256: sum } });
      setUpload({ sent: 0, size: bytes.length });
      for (let off = 0; off < bytes.length; off += CHUNK) {
        const part = bytes.subarray(off, Math.min(off + CHUNK, bytes.length));
        let tries = 0;
        for (;;) {
          try {
            x = await api<MagicImport>(`/magic/imports/${x.id}/chunk`, { method: 'POST', body: { offset: off, data: toBase64(part) } });
            break;
          } catch (e) {
            // a lost connection: try the same piece again (the server refuses a piece it already has)
            if (++tries >= 4 || (e instanceof ApiError && e.status < 500 && e.code !== 'server_error')) throw e;
            await new Promise((r) => setTimeout(r, 1500 * tries));
          }
        }
        setUpload({ sent: off + part.length, size: bytes.length });
      }
      setUpload(null);
      adopt(x);
      loadHistory();
      if (x.status === 'invalid') setErr(t('magic.invalid', { detail: x.error ?? '' }));
    } catch (e) {
      setUpload(null);
      setErr(errorText(t, e));
    }
  };

  const reanalyze = () => {
    if (!imp) return;
    setBusy(true); setErr(null);
    api<MagicImport>(`/magic/imports/${imp.id}/analyze`, { method: 'POST', body: { ...opts, readings_months: Number(opts.readings_months) } })
      .then(adopt).catch((e) => setErr(errorText(t, e))).finally(() => setBusy(false));
  };

  const commit = () => {
    if (!imp?.analysis) return;
    setBusy(true); setErr(null); setConfirm(false);
    const map = imp.analysis.companies.map((c) => {
      const target = mapping[c.company_id] ?? 'skip';
      return { magic_company: c.company_id, target, name: target === 'new' ? c.name : '' };
    });
    api<MagicImport>(`/magic/imports/${imp.id}/commit`, { method: 'POST', body: { mapping: map, options: { ...opts, readings_months: Number(opts.readings_months) } } })
      .then((x) => { adopt(x); watch(x.id); }).catch((e) => setErr(errorText(t, e))).finally(() => setBusy(false));
  };

  const targets = [
    { value: 'skip', label: t('magic.skip') },
    { value: 'new', label: t('magic.newCompany') },
    ...companies.map((c) => ({ value: c.id, label: c.name_en })),
  ];
  const chosen = imp?.analysis?.companies.filter((c) => (mapping[c.company_id] ?? 'skip') !== 'skip') ?? [];
  const running = imp?.status === 'running';
  const countKeys = ['customers', 'subscriptions', 'plans', 'boxes', 'collectors', 'readings', 'open_bills', 'suppliers'];

  return (
    <Stack>
      <PageHeader title={t('magic.title')} subtitle={t('magic.subtitle')} />

      <Paper withBorder p="md" radius="md">
        <Group align="flex-start" wrap="nowrap" gap="md">
          <ThemeIcon variant="light" size="lg" radius="xl">1</ThemeIcon>
          <Stack gap={6} style={{ flex: 1 }}>
            <Title order={4}>{t('magic.step1')}</Title>
            <Text size="sm">{t('magic.step1Text')}</Text>
            <List size="sm" spacing={4}>
              <List.Item>{t('magic.step1a')}</List.Item>
              <List.Item>{t('magic.step1b')} <Code>powershell -ExecutionPolicy Bypass -File Export-Magic.ps1</Code></List.Item>
              <List.Item>{t('magic.step1c')}</List.Item>
            </List>
            <Text size="xs" c="dimmed">{t('magic.readOnly')}</Text>
            <Group>
              <Button component="a" href="/tools/Export-Magic.ps1" download="Export-Magic.ps1" variant="light" size="xs" leftSection={<IconDownload size={14} />}>
                {t('magic.downloadScript')}
              </Button>
            </Group>
          </Stack>
        </Group>
      </Paper>

      <Paper withBorder p="md" radius="md">
        <Group align="flex-start" wrap="nowrap" gap="md">
          <ThemeIcon variant="light" size="lg" radius="xl">2</ThemeIcon>
          <Stack gap={6} style={{ flex: 1 }}>
            <Title order={4}>{t('magic.step2')}</Title>
            <Text size="sm">{t('magic.step2Text')}</Text>
            <Group>
              <FileButton onChange={onFile} accept=".zip,application/zip">
                {(props) => <Button {...props} leftSection={<IconUpload size={16} />} loading={!!upload} disabled={running}>{t('magic.upload')}</Button>}
              </FileButton>
            </Group>
            {upload && (
              <Stack gap={2}>
                <Progress value={(upload.sent / upload.size) * 100} animated />
                <Text size="xs" c="dimmed">{t('magic.uploading', { sent: (upload.sent / 1048576).toFixed(1), size: (upload.size / 1048576).toFixed(1) })}</Text>
              </Stack>
            )}
          </Stack>
        </Group>
      </Paper>

      {err && <Alert color="red" icon={<IconAlertTriangle size={16} />}>{err}</Alert>}

      {imp?.analysis && (
        <Paper withBorder p="md" radius="md">
          <Group align="flex-start" wrap="nowrap" gap="md">
            <ThemeIcon variant="light" size="lg" radius="xl">3</ThemeIcon>
            <Stack gap="sm" style={{ flex: 1, minWidth: 0 }}>
              <Title order={4}>{t('magic.step3')}</Title>
              <Text size="sm" c="dimmed">
                {t('magic.fileInfo', { file: imp.file_name, db: imp.analysis.manifest.database, at: formatDateTime(imp.analysis.manifest.exported_at) })}
              </Text>
              <SimpleGrid cols={{ base: 1, sm: 2, md: 4 }}>
                <TextInput type="date" label={t('magic.openingDate')} description={t('magic.openingDateHint')} value={opts.opening_date}
                  onChange={(e) => setOpts({ ...opts, opening_date: e.currentTarget.value })} />
                <TextInput label={t('magic.rate')} description={t('magic.rateHint')} value={opts.rate} placeholder={imp.analysis.default_rate}
                  onChange={(e) => setOpts({ ...opts, rate: e.currentTarget.value })} />
                <NumberInput label={t('magic.readingsMonths')} description={t('magic.readingsHint')} min={0} max={120} value={opts.readings_months}
                  onChange={(v) => setOpts({ ...opts, readings_months: Number(v) || 0 })} />
                <Checkbox mt="xl" label={t('magic.includeEnded')} checked={opts.include_ended}
                  onChange={(e) => setOpts({ ...opts, include_ended: e.currentTarget.checked })} />
              </SimpleGrid>
              <Group><Button size="xs" variant="default" onClick={reanalyze} loading={busy} disabled={running}>{t('magic.recalculate')}</Button></Group>

              {imp.analysis.companies.map((c) => (
                <Paper key={c.company_id} withBorder p="sm" radius="md">
                  <Group justify="space-between" wrap="wrap" gap="xs">
                    <div>
                      <Text fw={600}>{c.name || `Magic ${c.company_id}`} <Text span c="dimmed" size="sm">· Magic #{c.company_id}</Text></Text>
                      {c.already_imported.length > 0 && (
                        <Badge color="teal" variant="light" mt={4}>{t('magic.alreadyImported', { name: c.already_imported.map((a) => a.name).join(', ') })}</Badge>
                      )}
                    </div>
                    <Select w={260} data={targets} value={mapping[c.company_id] ?? 'skip'} allowDeselect={false} disabled={running}
                      onChange={(v) => setMapping({ ...mapping, [c.company_id]: v ?? 'skip' })} aria-label={t('magic.target')} />
                  </Group>
                  <Group gap="xs" mt="xs">
                    {countKeys.map((k) => (
                      <Badge key={k} variant="default" radius="sm">{t(`magic.counts.${k}`)}: {(c.counts[k] ?? 0).toLocaleString()}</Badge>
                    ))}
                    {Object.entries(c.subscriptions_by_status).map(([s, n]) => (
                      <Badge key={s} variant="light" radius="sm" color={s === 'active' ? 'green' : s === 'suspended' ? 'yellow' : 'gray'}>{t(`magic.status.${s}`)}: {n}</Badge>
                    ))}
                  </Group>
                  <Table mt="xs" withRowBorders={false} verticalSpacing={2} fz="sm">
                    <Table.Tbody>
                      <Table.Tr><Table.Td w="40%">{t('magic.totals.open_bills')}</Table.Td><Table.Td><Amounts m={c.totals.open_bills} /></Table.Td></Table.Tr>
                      <Table.Tr><Table.Td>{t('magic.totals.receivables')}</Table.Td><Table.Td><Amounts m={c.totals.receivables} /></Table.Td></Table.Tr>
                      <Table.Tr><Table.Td>{t('magic.totals.customer_credits')}</Table.Td><Table.Td><Amounts m={c.totals.customer_credits} /></Table.Td></Table.Tr>
                      <Table.Tr><Table.Td>{t('magic.totals.supplier_balances')}</Table.Td><Table.Td><Amounts m={c.totals.supplier_balances} /></Table.Td></Table.Tr>
                      <Table.Tr>
                        <Table.Td>{t('magic.totals.imbalance')}</Table.Td>
                        <Table.Td><Text span ff="monospace" dir="ltr" style={{ unicodeBidi: 'isolate' }} c={Number(c.totals.magic_imbalance_usd) !== 0 ? 'orange' : undefined}>{formatAmount(c.totals.magic_imbalance_usd, 2)} USD</Text></Table.Td>
                      </Table.Tr>
                    </Table.Tbody>
                  </Table>
                  {c.warnings.length > 0 && (
                    <Stack gap={2} mt="xs">
                      {c.warnings.map((w) => (
                        <Tooltip key={w.code} label={(w.examples ?? []).join(' · ') || w.code} multiline maw={420} withArrow>
                          <Text size="xs" c="orange.8">⚠ {t(`magic.warn.${w.code}`, { defaultValue: w.code })} ({w.count})</Text>
                        </Tooltip>
                      ))}
                    </Stack>
                  )}
                </Paper>
              ))}

              {imp.status !== 'running' && imp.status !== 'invalid' && (
                confirm ? (
                  <Confirm text={t('magic.confirm', { n: chosen.length })} onYes={commit} onNo={() => setConfirm(false)} color="blue"
                    yesLabel={t('magic.import')} noLabel={t('common.cancel')} busy={busy} />
                ) : (
                  <Group>
                    <Button leftSection={<IconDatabaseImport size={16} />} disabled={chosen.length === 0} onClick={() => setConfirm(true)} loading={busy}>
                      {t('magic.import')}
                    </Button>
                    <Text size="xs" c="dimmed">{t('magic.allOrNothing')}</Text>
                  </Group>
                )
              )}
            </Stack>
          </Group>
        </Paper>
      )}

      {imp && running && (
        <Alert color="blue" title={t('magic.running')}>
          <Progress value={imp.progress.total ? ((imp.progress.done ?? 0) / imp.progress.total) * 100 : 5} animated mb="xs" />
          <Text size="sm">
            {imp.progress.step ? t(`magic.steps.${imp.progress.step}`, { defaultValue: imp.progress.step }) : '…'}
            {imp.progress.magic_company ? ` · Magic #${imp.progress.magic_company}` : ''}
            {imp.progress.seconds !== undefined ? ` · ${imp.progress.seconds}s` : ''}
          </Text>
        </Alert>
      )}
      {imp?.status === 'failed' && <Alert color="red" title={t('magic.failed')}>{t('magic.failedText')} <Code>{imp.error}</Code></Alert>}
      {imp?.status === 'done' && imp.result && (
        <Alert color="green" icon={<IconCheck size={16} />} title={t('magic.done', { s: imp.result.seconds })}>
          <Stack gap="xs">
            {imp.result.companies.map((r) => (
              <div key={r.company_id}>
                <Text fw={600} size="sm">Magic #{r.magic_company} {r.magic_name} → {r.company_name}</Text>
                <Text size="sm">
                  {Object.entries(r.created).filter(([, n]) => n > 0).map(([k, n]) => `${t(`magic.created.${k}`, { defaultValue: k })}: ${n.toLocaleString()}`).join(' · ') || '—'}
                </Text>
                {r.financial_skipped && <Text size="xs" c="dimmed">{t('magic.financialSkipped')}</Text>}
                {r.accounts_created.length > 0 && <Text size="xs" c="dimmed">{t('magic.accountsCreated')}: {r.accounts_created.join(', ')}</Text>}
              </div>
            ))}
            <Text size="sm">{t('magic.nextSteps')}</Text>
          </Stack>
        </Alert>
      )}

      {history.length > 0 && (
        <Paper withBorder p="md" radius="md">
          <Title order={5} mb="xs">{t('magic.history')}</Title>
          <Table.ScrollContainer minWidth={520}>
            <Table fz="sm" highlightOnHover>
              <Table.Tbody>
                {history.map((h) => (
                  <Table.Tr key={h.id} style={{ cursor: 'pointer' }} onClick={() => open(h.id)}>
                    <Table.Td>{formatDateTime(h.created_at)}</Table.Td>
                    <Table.Td>{h.file_name}</Table.Td>
                    <Table.Td dir="ltr">{h.size < 1048576 ? `${Math.ceil(h.size / 1024)} KB` : `${(h.size / 1048576).toFixed(1)} MB`}</Table.Td>
                    <Table.Td>{h.created_by}</Table.Td>
                    <Table.Td><Badge variant="light" color={h.status === 'done' ? 'green' : h.status === 'failed' || h.status === 'invalid' ? 'red' : 'blue'}>{t(`magic.st.${h.status}`)}</Badge></Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        </Paper>
      )}
    </Stack>
  );
}
