import { useCallback, useEffect, useState } from 'react';
import {
  Badge, Button, Center, Grid, Group, Loader, Modal, Paper, Select, SimpleGrid, Stack, Table, Text, Textarea, TextInput, Title,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { IconFileImport, IconWand } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { api, type Account, type Me } from '../../api';
import { useCompany } from '../../company';
import { accountLabel, formatAmount, formatDate, notifyError, notifySaved, today } from '../../util';
import PageHeader from '../../components/PageHeader';
import { useMethods } from '../hr/Employees';

interface Suggestion { id: string; number: number; entry_date: string; memo: string; description: string; amount: string }
interface Line {
  id: string; line_date: string; description: string; reference: string; amount: string; status: 'unmatched' | 'matched' | 'ignored';
  journal_line_id: string | null; matched_number: number | null; matched_memo: string | null; suggestions: Suggestion[];
}
interface Statement { id: string; name: string; method: string; currency: string; created_at: string; lines?: number; unmatched?: number; date_from?: string; date_to?: string; total?: string }
interface Rec {
  currency: string; book_balance: string; statement_balance: string; difference: string;
  unmatched_statement: { id: string; line_date: string; description: string; amount: string }[];
  unreconciled_book: { id: string; number: number; entry_date: string; memo: string; amount: string }[];
}

// Parses CSV / pasted Excel rows: date, description, amount, reference (header row optional).
function parseRows(text: string) {
  const out: { date: string; description: string; amount: string; reference: string }[] = [];
  for (const raw of text.split(/\r?\n/)) {
    if (!raw.trim()) continue;
    const sep = raw.includes('\t') ? '\t' : raw.includes(';') ? ';' : ',';
    const cells = raw.split(sep).map((c) => c.trim().replace(/^"|"$/g, ''));
    let date = cells[0] ?? '';
    const m = /^(\d{1,2})[/.-](\d{1,2})[/.-](\d{4})$/.exec(date);
    if (m) date = `${m[3]}-${m[2].padStart(2, '0')}-${m[1].padStart(2, '0')}`;
    if (!/^\d{4}-\d{2}-\d{2}$/.test(date)) continue; // header or junk
    out.push({ date, description: cells[1] ?? '', amount: (cells[2] ?? '').replace(/[\s,]/g, ''), reference: cells[3] ?? '' });
  }
  return out;
}

export default function BankRec({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const methods = useMethods();
  const [method, setMethod] = useState<string | null>(null);
  const [statements, setStatements] = useState<Statement[]>([]);
  const [open, setOpen] = useState<string | null>(null);
  const [rec, setRec] = useState<Rec | null>(null);
  const [importing, setImporting] = useState(false);
  const canEdit = me.role === 'admin' || me.role === 'accountant';

  useEffect(() => {
    if (!method && methods.length) setMethod((methods.find((m) => m.kind === 'bank') ?? methods[0]).id);
  }, [methods, method]);

  const load = useCallback(() => {
    if (!company || !method) return;
    api<Statement[]>(`/companies/${company.id}/bank/statements?method_id=${method}`).then(setStatements).catch(() => {});
    api<Rec>(`/companies/${company.id}/bank/reconciliation?method_id=${method}&to=${today()}`).then(setRec).catch((e) => notifyError(t, e));
  }, [company, method, t]);
  useEffect(load, [load]);
  if (!company) return null;
  const cur = rec?.currency ?? 'USD';
  const dec = decimalsOf(cur);

  return (
    <Stack gap="lg" maw={1200}>
      <PageHeader title={t('bank.title')} subtitle={company.name_en}>
        <Select w={240} label={t('bank.account')} value={method} onChange={setMethod} allowDeselect={false}
          data={methods.map((m) => ({ value: m.id, label: `${m.name} (${m.currency})` }))} />
        {canEdit && method && <Button leftSection={<IconFileImport size={16} />} onClick={() => setImporting(true)}>{t('bank.import')}</Button>}
      </PageHeader>
      {rec && (
        <SimpleGrid cols={{ base: 1, sm: 3 }}>
          <Paper withBorder p="md"><Text size="xs" c="dimmed">{t('bank.book')}</Text><Text fw={700} size="lg" dir="ltr">{formatAmount(rec.book_balance, dec)} {cur}</Text></Paper>
          <Paper withBorder p="md"><Text size="xs" c="dimmed">{t('bank.statement')}</Text><Text fw={700} size="lg" dir="ltr">{formatAmount(rec.statement_balance, dec)} {cur}</Text></Paper>
          <Paper withBorder p="md"><Text size="xs" c="dimmed">{t('bank.difference')}</Text>
            <Text fw={700} size="lg" dir="ltr" c={Number(rec.difference) === 0 ? 'teal.8' : 'red.7'}>{formatAmount(rec.difference, dec)} {cur}</Text></Paper>
        </SimpleGrid>
      )}
      <Paper withBorder>
        <Title order={5} p="sm">{t('bank.statements')}</Title>
        {statements.length === 0 ? <Text c="dimmed" ta="center" p="lg">{t('common.empty')}</Text> : (
          <Table verticalSpacing="xs" highlightOnHover key={i18n.language}>
            <Table.Tbody>
              {statements.map((s) => (
                <Table.Tr key={s.id} style={{ cursor: 'pointer' }} onClick={() => setOpen(s.id)}>
                  <Table.Td fw={500}>{s.name || formatDate(s.created_at)}</Table.Td>
                  <Table.Td c="dimmed">{s.date_from && `${formatDate(s.date_from)} → ${formatDate(s.date_to!)}`}</Table.Td>
                  <Table.Td>{s.lines} {t('bank.lines')}</Table.Td>
                  <Table.Td>{s.unmatched ? <Badge color="orange" variant="light">{s.unmatched} {t('bank.unmatched')}</Badge> : <Badge color="teal" variant="light">✓</Badge>}</Table.Td>
                  <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(s.total ?? 0, dec)} {s.currency}</Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        )}
      </Paper>
      {rec && (rec.unmatched_statement.length > 0 || rec.unreconciled_book.length > 0) && (
        <Grid>
          <Grid.Col span={{ base: 12, md: 6 }}>
            <Paper withBorder>
              <Title order={6} p="sm">{t('bank.unmatchedStatement')}</Title>
              <Table verticalSpacing={4}><Table.Tbody>
                {rec.unmatched_statement.map((l) => (
                  <Table.Tr key={l.id}><Table.Td>{formatDate(l.line_date)}</Table.Td><Table.Td>{l.description}</Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(l.amount, dec)}</Table.Td></Table.Tr>
                ))}
              </Table.Tbody></Table>
            </Paper>
          </Grid.Col>
          <Grid.Col span={{ base: 12, md: 6 }}>
            <Paper withBorder>
              <Title order={6} p="sm">{t('bank.unreconciledBook')}</Title>
              <Table verticalSpacing={4}><Table.Tbody>
                {rec.unreconciled_book.map((l) => (
                  <Table.Tr key={l.id}><Table.Td>{formatDate(l.entry_date)}</Table.Td><Table.Td>#{l.number} {l.memo}</Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(l.amount, dec)}</Table.Td></Table.Tr>
                ))}
              </Table.Tbody></Table>
            </Paper>
          </Grid.Col>
        </Grid>
      )}
      {importing && method && <ImportStatement methodId={method} onClose={() => setImporting(false)} onDone={(id) => { setImporting(false); load(); setOpen(id); }} />}
      {open && <StatementView id={open} canEdit={canEdit} onClose={() => { setOpen(null); load(); }} />}
    </Stack>
  );
}

function ImportStatement({ methodId, onClose, onDone }: { methodId: string; onClose: () => void; onDone: (id: string) => void }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const [name, setName] = useState('');
  const [text, setText] = useState('');
  const [busy, setBusy] = useState(false);
  const rows = parseRows(text);

  async function submit() {
    if (!company) return;
    setBusy(true);
    try {
      const r = await api<{ statement: { id: string } }>(`/companies/${company.id}/bank/statements`, { method: 'POST', body: { method_id: methodId, name, lines: rows } });
      notifySaved(t);
      onDone(r.statement.id);
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened onClose={onClose} title={t('bank.import')} size="lg">
      <Stack>
        <TextInput label={t('bank.name')} value={name} onChange={(e) => setName(e.currentTarget.value)} />
        <input type="file" accept=".csv,text/csv,text/plain" onChange={(e) => { const f = e.currentTarget.files?.[0]; if (f) void f.text().then(setText); }} />
        <Textarea label={t('bank.paste')} autosize minRows={6} maxRows={14} dir="ltr" value={text} onChange={(e) => setText(e.currentTarget.value)}
          placeholder={'2026-03-02,Deposit,1000,REF1\n2026-03-06,Bank fee,-5,'} styles={{ input: { fontFamily: 'monospace', fontSize: 12 } }} />
        <Text size="sm" c="dimmed">{t('bank.parsed', { n: rows.length })}</Text>
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
          <Button loading={busy} disabled={rows.length === 0} onClick={submit}>{t('bank.import')}</Button>
        </Group>
      </Stack>
    </Modal>
  );
}

function StatementView({ id, canEdit, onClose }: { id: string; canEdit: boolean; onClose: () => void }) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [d, setD] = useState<{ statement: Statement; lines: Line[] } | null>(null);
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [booking, setBooking] = useState<Line | null>(null);
  const [acct, setAcct] = useState<string | null>(null);
  const [desc, setDesc] = useState('');
  const [busy, setBusy] = useState(false);

  const load = useCallback(() => {
    if (!company) return;
    api<{ statement: Statement; lines: Line[] }>(`/companies/${company.id}/bank/statements/${id}`).then(setD).catch((e) => notifyError(t, e));
  }, [company, id, t]);
  useEffect(load, [load]);
  useEffect(() => {
    if (company) api<Account[]>(`/companies/${company.id}/accounts`).then((a) => setAccounts(a.filter((x) => x.is_postable && x.is_active))).catch(() => {});
  }, [company]);
  if (!company) return null;
  const base = `/companies/${company.id}/bank`;

  async function call(path: string, body: object = {}) {
    setBusy(true);
    try {
      type Detail = { statement: Statement; lines: Line[] };
      const r = await api<Detail | { matched: number; statement: Detail }>(path, { method: 'POST', body });
      if ('matched' in r) {
        notifications.show({ color: 'teal', message: t('bank.matchedN', { n: r.matched }) });
        setD(r.statement);
      } else setD(r);
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  const dec = d ? decimalsOf(d.statement.currency) : 2;
  return (
    <Modal opened onClose={onClose} size="90rem" title={d ? `${d.statement.method} · ${d.statement.name || formatDate(d.statement.created_at)}` : '…'}>
      {!d ? <Center p="xl"><Loader size="sm" /></Center> : (
        <Stack>
          {canEdit && <Group><Button leftSection={<IconWand size={16} />} loading={busy} onClick={() => call(`${base}/statements/${id}/auto-match`)}>{t('bank.autoMatch')}</Button></Group>}
          <Table.ScrollContainer minWidth={980}>
            <Table verticalSpacing="xs" withTableBorder key={i18n.language}>
              <Table.Thead><Table.Tr>
                <Table.Th w={110}>{t('invoices.date')}</Table.Th><Table.Th>{t('invoices.description')}</Table.Th>
                <Table.Th w={130} style={{ textAlign: 'end' }}>{t('invoices.amount')}</Table.Th><Table.Th>{t('bank.suggestions')}</Table.Th><Table.Th w={210} />
              </Table.Tr></Table.Thead>
              <Table.Tbody>
                {d.lines.map((l) => (
                  <Table.Tr key={l.id} style={{ opacity: l.status === 'ignored' ? 0.5 : 1 }}>
                    <Table.Td>{formatDate(l.line_date)}</Table.Td>
                    <Table.Td>{l.description}{l.reference && <Text size="xs" c="dimmed">{l.reference}</Text>}</Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }} c={Number(l.amount) < 0 ? 'red.7' : 'teal.8'}>{formatAmount(l.amount, dec)}</Table.Td>
                    <Table.Td>
                      {l.status === 'matched' ? <Badge color="teal" variant="light">✓ #{l.matched_number} {l.matched_memo}</Badge>
                        : l.status === 'ignored' ? <Badge color="gray">{t('bank.ignored')}</Badge>
                        : l.suggestions.length === 0 ? <Text size="xs" c="dimmed">—</Text>
                        : <Stack gap={2}>{l.suggestions.map((s) => (
                          <Group key={s.id} gap={6} wrap="nowrap">
                            <Text size="xs">#{s.number} · {formatDate(s.entry_date)} · {s.memo}</Text>
                            {canEdit && <Button size="compact-xs" variant="light" onClick={() => call(`${base}/lines/${l.id}/match`, { journal_line_id: s.id })}>{t('bank.match')}</Button>}
                          </Group>
                        ))}</Stack>}
                    </Table.Td>
                    <Table.Td>
                      {canEdit && (
                        <Group gap={4} justify="flex-end" wrap="nowrap">
                          {l.status === 'matched' && <Button size="compact-xs" variant="subtle" onClick={() => call(`${base}/lines/${l.id}/unmatch`)}>{t('bank.unmatch')}</Button>}
                          {l.status === 'unmatched' && <Button size="compact-xs" variant="light" onClick={() => { setBooking(l); setDesc(l.description); setAcct(null); }}>{t('bank.createEntry')}</Button>}
                          {l.status === 'unmatched' && <Button size="compact-xs" variant="subtle" color="gray" onClick={() => call(`${base}/lines/${l.id}/ignore`)}>{t('bank.ignore')}</Button>}
                          {l.status === 'ignored' && <Button size="compact-xs" variant="subtle" onClick={() => call(`${base}/lines/${l.id}/unignore`)}>{t('bank.unignore')}</Button>}
                        </Group>
                      )}
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
          {booking && (
            <Paper withBorder p="sm">
              <Group align="flex-end" wrap="wrap">
                <Text size="sm" fw={500} dir="ltr">{formatAmount(booking.amount, dec)} · {booking.description}</Text>
                <Select w={300} label={t('bank.entryAccount')} searchable data={accounts.map((a) => ({ value: a.id, label: accountLabel(a) }))} value={acct} onChange={setAcct} />
                <TextInput label={t('invoices.description')} value={desc} onChange={(e) => setDesc(e.currentTarget.value)} />
                <Button loading={busy} disabled={!acct} onClick={async () => { await call(`${base}/lines/${booking.id}/entry`, { account_id: acct, description: desc }); setBooking(null); }}>{t('bank.createEntry')}</Button>
                <Button variant="default" onClick={() => setBooking(null)}>{t('common.cancel')}</Button>
              </Group>
            </Paper>
          )}
        </Stack>
      )}
    </Modal>
  );
}
