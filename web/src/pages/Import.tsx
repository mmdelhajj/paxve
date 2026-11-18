import { useEffect, useMemo, useState } from 'react';
import {
  Alert, Badge, Button, Checkbox, FileButton, Group, Paper, ScrollArea, Select, SimpleGrid, Stack, Stepper, Table, Text, Textarea,
  TextInput, ThemeIcon, UnstyledButton,
} from '@mantine/core';
import {
  IconBook2, IconCheck, IconDownload, IconFileSpreadsheet, IconGauge, IconReceipt2, IconUpload, IconUserCircle, IconUsersGroup,
} from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { api, ApiError, type Account } from '../api';
import { useCompany } from '../company';
import { accountLabel, notifyError, today } from '../util';
import PageHeader from '../components/PageHeader';

type ImportType = 'customers' | 'subscriptions' | 'opening_balances' | 'readings' | 'accounts';

interface FieldDef { key: string; required?: boolean; synonyms: string[]; sample: string }

// Header synonyms (EN / AR / FR / old Magic column names) used to map columns automatically.
const FIELDS: Record<ImportType, FieldDef[]> = {
  customers: [
    { key: 'code', synonyms: ['code', 'customer code', 'id', 'no', 'number', 'رمز', 'الرمز', 'رقم', 'رقم المشترك', 'numéro', 'customerid', 'accountnameid'], sample: '1001' },
    { key: 'name', required: true, synonyms: ['name', 'customer', 'full name', 'الاسم', 'اسم', 'اسم المشترك', 'nom', 'client', 'abonné', 'accountname'], sample: 'Ahmad Haddad' },
    { key: 'phone', synonyms: ['phone', 'mobile', 'tel', 'telephone', 'الهاتف', 'هاتف', 'خليوي', 'téléphone', 'portable', 'phone1'], sample: '03123456' },
    { key: 'phone2', synonyms: ['phone2', 'phone 2', 'second phone', 'هاتف 2', 'هاتف ثان', 'téléphone 2'], sample: '' },
    { key: 'email', synonyms: ['email', 'e-mail', 'mail', 'البريد', 'البريد الإلكتروني', 'courriel'], sample: '' },
    { key: 'address', synonyms: ['address', 'العنوان', 'عنوان', 'adresse'], sample: 'Main street' },
    { key: 'area', synonyms: ['area', 'zone', 'region', 'district', 'المنطقة', 'منطقة', 'الحي', 'quartier'], sample: 'Hay El Saray' },
    { key: 'status', synonyms: ['status', 'الحالة', 'حالة', 'statut', 'état'], sample: 'active' },
    { key: 'notes', synonyms: ['notes', 'note', 'remarks', 'ملاحظات', 'ملاحظة', 'remarques'], sample: '' },
  ],
  subscriptions: [
    { key: 'customer_code', required: true, synonyms: ['customer code', 'code', 'customer', 'رمز المشترك', 'الرمز', 'رقم المشترك', 'code client', 'code abonné'], sample: '1001' },
    { key: 'plan', required: true, synonyms: ['plan', 'package', 'amper', 'ampere', 'الباقة', 'الاشتراك', 'أمبير', 'formule', 'forfait'], sample: '10 Amp fixed' },
    { key: 'meter_no', synonyms: ['meter', 'meter no', 'meter number', 'counter', 'العداد', 'رقم العداد', 'compteur', 'n° compteur'], sample: 'M-100' },
    { key: 'start_date', synonyms: ['start', 'start date', 'date', 'تاريخ البدء', 'تاريخ', 'date de début'], sample: today() },
    { key: 'fee_override', synonyms: ['fee', 'special fee', 'price', 'رسم خاص', 'السعر', 'forfait spécial', 'prix'], sample: '' },
    { key: 'initial_reading', synonyms: ['initial reading', 'reading', 'counter start', 'القراءة الأولى', 'قراءة', 'index de départ', 'index'], sample: '0' },
    { key: 'status', synonyms: ['status', 'الحالة', 'statut'], sample: 'active' },
  ],
  opening_balances: [
    { key: 'customer_code', required: true, synonyms: ['customer code', 'code', 'customer', 'رمز المشترك', 'الرمز', 'code client'], sample: '1001' },
    { key: 'currency', synonyms: ['currency', 'cur', 'العملة', 'عملة', 'devise', 'moneyid'], sample: 'USD' },
    { key: 'amount', required: true, synonyms: ['amount', 'balance', 'due', 'debt', 'المبلغ', 'الرصيد', 'الدين', 'montant', 'solde'], sample: '45.50' },
    { key: 'date', synonyms: ['date', 'التاريخ', 'تاريخ'], sample: today() },
    { key: 'description', synonyms: ['description', 'memo', 'البيان', 'بيان', 'libellé', 'désignation'], sample: 'Balance from old system' },
  ],
  readings: [
    { key: 'customer_code', synonyms: ['customer code', 'code', 'customer', 'رمز المشترك', 'الرمز', 'code client'], sample: '1001' },
    { key: 'meter_no', synonyms: ['meter', 'meter no', 'counter', 'العداد', 'رقم العداد', 'compteur'], sample: '' },
    { key: 'period', required: true, synonyms: ['period', 'month', 'الشهر', 'شهر', 'الفترة', 'mois', 'période'], sample: today().slice(0, 7) },
    { key: 'reading', required: true, synonyms: ['reading', 'counter', 'new reading', 'القراءة', 'قراءة', 'العداد الجديد', 'index', 'relevé', 'newcounter'], sample: '1250' },
  ],
  accounts: [
    { key: 'code', required: true, synonyms: ['code', 'account', 'account code', 'رمز', 'رقم الحساب', 'numéro', 'compte', 'ledgerid'], sample: '5311' },
    { key: 'name', required: true, synonyms: ['name', 'account name', 'الاسم', 'اسم الحساب', 'intitulé', 'libellé'], sample: 'Cash collector 1' },
    { key: 'type', required: true, synonyms: ['type', 'النوع', 'نوع'], sample: 'asset' },
    { key: 'parent_code', synonyms: ['parent', 'parent code', 'الحساب الأب', 'compte parent'], sample: '53' },
    { key: 'is_postable', synonyms: ['postable', 'is postable', 'يمكن الترحيل', 'imputable'], sample: 'yes' },
  ],
};

const TYPES: { key: ImportType; icon: typeof IconUserCircle }[] = [
  { key: 'customers', icon: IconUserCircle },
  { key: 'subscriptions', icon: IconUsersGroup },
  { key: 'opening_balances', icon: IconReceipt2 },
  { key: 'readings', icon: IconGauge },
  { key: 'accounts', icon: IconBook2 },
];

// ---- CSV / pasted Excel parsing (RFC 4180 quotes, auto delimiter: tab, comma, semicolon) ----

function detectDelimiter(text: string): string {
  const first = text.split(/\r?\n/).find((l) => l.trim()) ?? '';
  const counts: [string, number][] = [['\t', 0], [',', 0], [';', 0]];
  for (const c of counts) c[1] = first.split(c[0]).length - 1;
  counts.sort((a, b) => b[1] - a[1]);
  return counts[0][1] > 0 ? counts[0][0] : ',';
}

export function parseTable(text: string): string[][] {
  text = text.replace(/^﻿/, '');
  const d = detectDelimiter(text);
  const rows: string[][] = [];
  let row: string[] = [];
  let field = '';
  let q = false;
  for (let i = 0; i < text.length; i++) {
    const ch = text[i];
    if (q) {
      if (ch === '"') {
        if (text[i + 1] === '"') { field += '"'; i++; } else q = false;
      } else field += ch;
    } else if (ch === '"' && field === '') q = true;
    else if (ch === d) { row.push(field); field = ''; }
    else if (ch === '\n' || ch === '\r') {
      if (ch === '\r' && text[i + 1] === '\n') i++;
      row.push(field); field = '';
      if (row.some((c) => c.trim() !== '')) rows.push(row);
      row = [];
    } else field += ch;
  }
  row.push(field);
  if (row.some((c) => c.trim() !== '')) rows.push(row);
  return rows;
}

const norm = (s: string) => s.toLowerCase().replace(/[\s_\-.:]+/g, ' ').trim();

function autoMap(type: ImportType, headers: string[]): Record<string, string> {
  const map: Record<string, string> = {};
  const used = new Set<number>();
  for (const f of FIELDS[type]) {
    const idx = headers.findIndex((h, i) => !used.has(i) && (norm(h) === norm(f.key) || f.synonyms.some((s) => norm(s) === norm(h))));
    if (idx >= 0) { map[f.key] = String(idx); used.add(idx); }
  }
  return map;
}

function downloadTemplate(type: ImportType) {
  const fields = FIELDS[type];
  const esc = (v: string) => (/[",\n]/.test(v) ? `"${v.replace(/"/g, '""')}"` : v);
  const csv = '﻿' + fields.map((f) => f.key).join(',') + '\n' + fields.map((f) => esc(f.sample)).join(',') + '\n';
  const url = URL.createObjectURL(new Blob([csv], { type: 'text/csv;charset=utf-8' }));
  const a = document.createElement('a');
  a.href = url;
  a.download = `paxve-${type}-template.csv`;
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

interface ImportErr { row: number; field: string; code: string; value?: string }
interface Result { total: number; valid: number; created: number; updated: number; created_areas: number; errors: ImportErr[]; took_ms: number; dry_run: boolean }

export default function Import() {
  const { t } = useTranslation();
  const { company } = useCompany();
  const [step, setStep] = useState(0);
  const [type, setType] = useState<ImportType>('customers');
  const [table, setTable] = useState<string[][]>([]);
  const [paste, setPaste] = useState('');
  const [hasHeader, setHasHeader] = useState(true);
  const [mapping, setMapping] = useState<Record<string, string>>({});
  const [options, setOptions] = useState<Record<string, string>>({});
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [check, setCheck] = useState<Result | null>(null);
  const [done, setDone] = useState<Result | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (company && type === 'opening_balances') api<Account[]>(`/companies/${company.id}/accounts`).then(setAccounts).catch(() => {});
  }, [company, type]);

  const headers = useMemo(() => (table.length ? (hasHeader ? table[0] : table[0].map((_, i) => `#${i + 1}`)) : []), [table, hasHeader]);
  const dataRows = useMemo(() => (hasHeader ? table.slice(1) : table), [table, hasHeader]);
  const rows = useMemo(() => dataRows.map((r) => {
    const o: Record<string, string> = {};
    for (const [k, idx] of Object.entries(mapping)) if (idx !== '') o[k] = (r[Number(idx)] ?? '').trim();
    return o;
  }), [dataRows, mapping]);
  const missingRequired = FIELDS[type].filter((f) => f.required && !mapping[f.key]);
  const readingsKeyMissing = type === 'readings' && !mapping.customer_code && !mapping.meter_no;

  function load(text: string) {
    const tbl = parseTable(text);
    setTable(tbl);
    setMapping(tbl.length ? autoMap(type, tbl[0]) : {});
    setCheck(null);
    setDone(null);
    if (tbl.length) setStep(2);
  }

  async function run(dry: boolean) {
    if (!company) return;
    setBusy(true);
    try {
      const r = await api<Result>(`/companies/${company.id}/import/${type}`, { method: 'POST', body: { rows, dry_run: dry, options } });
      if (dry) setCheck(r); else { setDone(r); setStep(4); }
    } catch (e) {
      notifyError(t, e);
      if (e instanceof ApiError && e.code === 'import_errors') setStep(2);
    } finally {
      setBusy(false);
    }
  }

  if (!company) return null;
  const fieldLabel = (k: string) => t(`import.fields.${k}`, { defaultValue: k });

  return (
    <Stack gap="lg" maw={1200}>
      <PageHeader title={t('import.title')} subtitle={`${company.name_en} · ${t('import.subtitle')}`} />
      <Stepper active={step} onStepClick={(s) => { if (s < step && step < 4) setStep(s); }} size="sm">
        <Stepper.Step label={t('import.step1')}>
          <SimpleGrid cols={{ base: 1, sm: 2, md: 3 }} mt="md">
            {TYPES.map((x) => (
              <UnstyledButton key={x.key} onClick={() => { setType(x.key); setOptions({}); setStep(1); }}>
                <Paper withBorder p="md" style={{ borderColor: type === x.key ? 'var(--mantine-color-teal-5)' : undefined }}>
                  <Group wrap="nowrap" align="flex-start">
                    <ThemeIcon variant="light" size="lg"><x.icon size={20} /></ThemeIcon>
                    <div>
                      <Text fw={600}>{t(`import.types.${x.key}`)}</Text>
                      <Text size="sm" c="dimmed">{t(`import.hints.${x.key}`)}</Text>
                    </div>
                  </Group>
                </Paper>
              </UnstyledButton>
            ))}
          </SimpleGrid>
          <Alert mt="md" variant="light">{t('import.order')}</Alert>
        </Stepper.Step>

        <Stepper.Step label={t('import.step2')}>
          <Stack mt="md">
            <Group>
              <Button variant="default" leftSection={<IconDownload size={16} />} onClick={() => downloadTemplate(type)}>{t('import.template')}</Button>
              <FileButton accept=".csv,.txt,.tsv,text/csv" onChange={(f) => { if (f) f.text().then(load); }}>
                {(props) => <Button leftSection={<IconUpload size={16} />} {...props}>{t('import.chooseFile')}</Button>}
              </FileButton>
            </Group>
            <Text size="sm" c="dimmed">{t('import.excelHint')}</Text>
            <Textarea minRows={6} autosize maxRows={14} placeholder={t('import.pastePlaceholder')} value={paste}
              onChange={(e) => setPaste(e.currentTarget.value)} styles={{ input: { fontFamily: 'monospace', fontSize: 12 } }} />
            <Group><Button variant="light" disabled={!paste.trim()} leftSection={<IconFileSpreadsheet size={16} />} onClick={() => load(paste)}>{t('import.usePasted')}</Button></Group>
          </Stack>
        </Stepper.Step>

        <Stepper.Step label={t('import.step3')}>
          <Stack mt="md">
            <Group justify="space-between">
              <Text>{t('import.rowsFound', { n: dataRows.length })}</Text>
              <Checkbox label={t('import.hasHeader')} checked={hasHeader} onChange={(e) => setHasHeader(e.currentTarget.checked)} />
            </Group>
            <SimpleGrid cols={{ base: 1, sm: 2, md: 3 }}>
              {FIELDS[type].map((f) => (
                <Select key={f.key} label={`${fieldLabel(f.key)}${f.required ? ' *' : ''}`} clearable value={mapping[f.key] ?? null}
                  placeholder={t('import.notInFile')} error={f.required && !mapping[f.key] ? ' ' : undefined}
                  data={headers.map((h, i) => ({ value: String(i), label: h || `#${i + 1}` }))}
                  onChange={(v) => setMapping((m) => ({ ...m, [f.key]: v ?? '' }))} />
              ))}
            </SimpleGrid>
            {type === 'customers' && (
              <Checkbox label={t('import.updateExisting')} checked={options.update_existing === 'true'}
                onChange={(e) => setOptions((o) => ({ ...o, update_existing: String(e.currentTarget.checked) }))} />
            )}
            {type === 'opening_balances' && (
              <Group grow align="flex-end">
                <Select label={t('import.openingAccount')} searchable clearable placeholder={t('import.openingAccountDefault')}
                  data={accounts.filter((a) => a.is_postable && a.is_active).map((a) => ({ value: a.id, label: accountLabel(a) }))}
                  value={options.account_id ?? null} onChange={(v) => setOptions((o) => ({ ...o, account_id: v ?? '' }))} />
                <TextInput type="date" label={t('import.defaultDate')} value={options.date ?? today()}
                  onChange={(e) => { const v = e.currentTarget.value; setOptions((o) => ({ ...o, date: v })); }} />
              </Group>
            )}
            <Paper withBorder>
              <ScrollArea h={260}>
                <Table striped withColumnBorders fz="xs">
                  <Table.Thead><Table.Tr>
                    <Table.Th>#</Table.Th>
                    {FIELDS[type].filter((f) => mapping[f.key]).map((f) => <Table.Th key={f.key}>{fieldLabel(f.key)}</Table.Th>)}
                  </Table.Tr></Table.Thead>
                  <Table.Tbody>
                    {rows.slice(0, 50).map((r, i) => (
                      <Table.Tr key={i}>
                        <Table.Td c="dimmed">{i + 1}</Table.Td>
                        {FIELDS[type].filter((f) => mapping[f.key]).map((f) => <Table.Td key={f.key}>{r[f.key]}</Table.Td>)}
                      </Table.Tr>
                    ))}
                  </Table.Tbody>
                </Table>
              </ScrollArea>
            </Paper>
            {readingsKeyMissing && <Alert color="yellow">{t('import.readingsKey')}</Alert>}
            <Group justify="flex-end">
              <Button disabled={missingRequired.length > 0 || readingsKeyMissing || rows.length === 0}
                onClick={() => { setStep(3); void run(true); }}>{t('import.check')}</Button>
            </Group>
          </Stack>
        </Stepper.Step>

        <Stepper.Step label={t('import.step4')}>
          <Stack mt="md">
            {busy && !check && <Text>{t('import.checking')}</Text>}
            {check && (
              <>
                <SimpleGrid cols={3}>
                  <Paper withBorder p="md"><Text size="xs" c="dimmed">{t('import.total')}</Text><Text fw={700} size="xl">{check.total}</Text></Paper>
                  <Paper withBorder p="md"><Text size="xs" c="dimmed">{t('import.valid')}</Text><Text fw={700} size="xl" c="teal.7">{check.valid}</Text></Paper>
                  <Paper withBorder p="md"><Text size="xs" c="dimmed">{t('import.invalid')}</Text><Text fw={700} size="xl" c={check.errors.length ? 'red.7' : undefined}>{check.total - check.valid}</Text></Paper>
                </SimpleGrid>
                {check.errors.length > 0 ? (
                  <>
                    <Alert color="red">{t('import.fixErrors')}</Alert>
                    <Paper withBorder>
                      <ScrollArea h={300}>
                        <Table fz="sm" striped>
                          <Table.Thead><Table.Tr><Table.Th>{t('import.row')}</Table.Th><Table.Th>{t('import.field')}</Table.Th><Table.Th>{t('import.problem')}</Table.Th><Table.Th>{t('import.value')}</Table.Th></Table.Tr></Table.Thead>
                          <Table.Tbody>
                            {check.errors.map((e, i) => (
                              <Table.Tr key={i}>
                                <Table.Td>{e.row || '—'}</Table.Td>
                                <Table.Td>{fieldLabel(e.field)}</Table.Td>
                                <Table.Td><Badge color="red" variant="light">{t(`import.codes.${e.code}`, { defaultValue: t(`errors.${e.code}`, { defaultValue: e.code, detail: e.value ?? '' }) })}</Badge></Table.Td>
                                <Table.Td ff="monospace">{e.value}</Table.Td>
                              </Table.Tr>
                            ))}
                          </Table.Tbody>
                        </Table>
                      </ScrollArea>
                    </Paper>
                    <Group><Button variant="default" onClick={() => setStep(1)}>{t('import.chooseFile')}</Button><Button variant="default" onClick={() => setStep(2)}>{t('import.step3')}</Button></Group>
                  </>
                ) : (
                  <>
                    <Alert color="teal" icon={<IconCheck size={16} />}>{t('import.allValid', { n: check.valid })}</Alert>
                    <Group justify="flex-end"><Button size="md" loading={busy} onClick={() => run(false)}>{t('import.run', { n: check.valid })}</Button></Group>
                  </>
                )}
              </>
            )}
          </Stack>
        </Stepper.Step>

        <Stepper.Completed>
          {done && (
            <Stack mt="md">
              <Alert color="teal" icon={<IconCheck size={16} />} title={t('import.doneTitle')}>
                {t('import.doneText', { created: done.created, updated: done.updated, areas: done.created_areas, s: (done.took_ms / 1000).toFixed(1) })}
              </Alert>
              <Group>
                <Button onClick={() => { setStep(0); setTable([]); setPaste(''); setCheck(null); setDone(null); }}>{t('import.another')}</Button>
              </Group>
            </Stack>
          )}
        </Stepper.Completed>
      </Stepper>
    </Stack>
  );
}
