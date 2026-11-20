import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  ActionIcon, Alert, Badge, Button, Center, Group, Loader, Modal, Paper, SegmentedControl, Select, Stack, Table, Text,
  TextInput, Tooltip,
} from '@mantine/core';
import { IconAlertTriangle, IconCheck, IconLock, IconPlus, IconTrash } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { notifications } from '@mantine/notifications';
import { can, api, type Account, type Entry, type EntrySummary, type Me, type Rate } from '../api';
import { useCompany } from '../company';
import { accountLabel, formatAmount, formatDate, fromUnits, notifyError, notifySaved, toUnits, today } from '../util';
import PageHeader, { Confirm } from '../components/PageHeader';
import { SourceButton, ccLabel, useCostCenters } from './acc/common';

export default function Journal({ me, openId }: { me: Me; openId?: string }) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [rows, setRows] = useState<EntrySummary[] | null>(null);
  const [status, setStatus] = useState('');
  const [editor, setEditor] = useState<{ entry: Entry | null } | null>(null);
  const [viewId, setViewId] = useState<string | null>(openId || null);
  const canWrite = can(me, 'accounting.create');

  const load = useCallback(() => {
    if (!company) return;
    api<EntrySummary[]>(`/companies/${company.id}/journal?status=${status}`).then(setRows).catch((e) => notifyError(t, e));
  }, [company, status, t]);
  useEffect(load, [load]);

  if (!company) return null;
  const dec = decimalsOf(company.base_currency);

  return (
    <Stack gap="lg" maw={1200}>
      <PageHeader title={t('journal.title')} subtitle={t('reports.amountsIn', { cur: company.base_currency })}>
        <SegmentedControl size="xs" value={status} onChange={setStatus} data={[
          { value: '', label: t('common.all') },
          { value: 'draft', label: t('journal.draft') },
          { value: 'posted', label: t('journal.posted') },
        ]} />
        {canWrite && (
          <Button leftSection={<IconPlus size={16} />} onClick={() => setEditor({ entry: null })}>{t('journal.new')}</Button>
        )}
      </PageHeader>
      <Paper withBorder>
        {rows === null ? (
          <Center p="xl"><Loader size="sm" /></Center>
        ) : rows.length === 0 ? (
          <Text c="dimmed" ta="center" p="xl">{t('common.empty')}</Text>
        ) : (
          <Table.ScrollContainer minWidth={720}>
            <Table verticalSpacing="sm" highlightOnHover key={i18n.language}>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th w={80}>{t('journal.number')}</Table.Th>
                  <Table.Th w={130}>{t('journal.date')}</Table.Th>
                  <Table.Th>{t('journal.memo')}</Table.Th>
                  <Table.Th w={130}>{t('journal.status')}</Table.Th>
                  <Table.Th w={160} style={{ textAlign: 'end' }}>{t('journal.total')}</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {rows.map((e) => (
                  <Table.Tr key={e.id} style={{ cursor: 'pointer' }} onClick={() => setViewId(e.id)}>
                    <Table.Td ff="monospace">{e.number ?? '—'}</Table.Td>
                    <Table.Td>{formatDate(e.date)}</Table.Td>
                    <Table.Td>
                      <Text size="sm" lineClamp={1}>{e.memo || '—'}</Text>
                      {e.reference && <Text size="xs" c="dimmed">{e.reference}</Text>}
                    </Table.Td>
                    <Table.Td><StatusBadge e={e} /></Table.Td>
                    <Table.Td ff="monospace" style={{ textAlign: 'end' }} dir="ltr">{formatAmount(e.total, dec)}</Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </Paper>

      {viewId && (
        <EntryView id={viewId} me={me} onClose={() => setViewId(null)} onChanged={load}
          onEdit={(entry) => { setViewId(null); setEditor({ entry }); }} onOpen={setViewId} />
      )}
      {editor && (
        <EntryEditor me={me} entry={editor.entry} onClose={() => setEditor(null)}
          onSaved={(e) => { setEditor(null); load(); setViewId(e.id); }} />
      )}
    </Stack>
  );
}

function StatusBadge({ e }: { e: EntrySummary }) {
  const { t } = useTranslation();
  if (e.status === 'draft') return <Badge color="yellow" variant="light">{t('journal.draft')}</Badge>;
  if (e.reversed_by) return <Badge color="gray" variant="light">{t('journal.reversed')}</Badge>;
  if (e.reversal_of) return <Badge color="orange" variant="light">{t('journal.reversal')}</Badge>;
  return <Badge color="teal" variant="light" leftSection={<IconLock size={10} />}>{t('journal.posted')}</Badge>;
}

function EntryView({ id, me, onClose, onChanged, onEdit, onOpen }: {
  id: string; me: Me; onClose: () => void; onChanged: () => void; onEdit: (e: Entry) => void; onOpen: (id: string) => void;
}) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [e, setE] = useState<Entry | null>(null);
  const [confirm, setConfirm] = useState<'reverse' | 'discard' | null>(null);
  const [busy, setBusy] = useState(false);
  const canPost = can(me, 'accounting.edit');
  const centers = useCostCenters().filter((c) => c.is_active);

  async function retag(lineId: string, cc: string) {
    if (!company || !e) return;
    try {
      await api(`/companies/${company.id}/journal-lines/${lineId}`, { method: 'PATCH', body: { cost_center_id: cc } });
      setE({ ...e, lines: e.lines.map((x) => (x.id === lineId ? { ...x, cost_center_id: cc || null } : x)) });
      notifySaved(t);
    } catch (err) {
      notifyError(t, err);
    }
  }

  useEffect(() => {
    if (!company) return;
    setE(null);
    api<Entry>(`/companies/${company.id}/journal/${id}`).then(setE).catch((err) => { notifyError(t, err); onClose(); });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [company, id, t]);

  if (!company) return null;
  const base = company.base_currency;

  async function act(kind: 'post' | 'reverse' | 'discard') {
    if (!e || !company) return;
    setBusy(true);
    try {
      if (kind === 'post') setE(await api<Entry>(`/companies/${company.id}/journal/${e.id}/post`, { method: 'POST', body: {} }));
      if (kind === 'reverse') {
        const r = await api<Entry>(`/companies/${company.id}/journal/${e.id}/reverse`, { method: 'POST', body: { date: today() } });
        onOpen(r.id);
      }
      if (kind === 'discard') {
        await api(`/companies/${company.id}/journal/${e.id}`, { method: 'DELETE' });
        onClose();
      }
      notifySaved(t);
      onChanged();
    } catch (err) {
      notifyError(t, err);
    } finally {
      setBusy(false);
      setConfirm(null);
    }
  }

  return (
    <Modal opened onClose={onClose} size="xl"
      title={e ? `${t('journal.entry')} ${e.number ? '#' + e.number : ''} · ${formatDate(e.date)}` : t('journal.entry')}>
      {!e ? (
        <Center p="xl"><Loader size="sm" /></Center>
      ) : (
        <Stack>
          <Group justify="space-between" wrap="wrap">
            <div>
              <Text fw={500}>{e.memo || '—'}</Text>
              {e.reference && <Text size="sm" c="dimmed">{t('journal.reference')}: {e.reference}</Text>}
            </div>
            <StatusBadge e={e} />
          </Group>
          <Table.ScrollContainer minWidth={640}>
            <Table verticalSpacing={6} withTableBorder>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>{t('journal.account')}</Table.Th>
                  <Table.Th w={70}>{t('journal.currency')}</Table.Th>
                  <Table.Th style={{ textAlign: 'end' }}>{t('journal.debit')}</Table.Th>
                  <Table.Th style={{ textAlign: 'end' }}>{t('journal.credit')}</Table.Th>
                  <Table.Th style={{ textAlign: 'end' }}>{t('journal.inBase', { cur: base })}</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {e.lines.map((l) => {
                  const d = decimalsOf(l.currency);
                  return (
                    <Table.Tr key={l.id}>
                      <Table.Td>
                        <Text size="sm"><span dir="ltr" style={{ fontFamily: 'monospace' }}>{l.account_code}</span> · {l.account_name}</Text>
                        {l.description && <Text size="xs" c="dimmed">{l.description}</Text>}
                        {e.status === 'posted' && canPost && centers.length > 0 ? (
                          <select className="mx-ccsel" aria-label={t('ax.cc.title')} value={l.cost_center_id ?? ''}
                            onChange={(ev) => void retag(l.id, ev.currentTarget.value)}>
                            <option value="">{t('ax.cc.none')}</option>
                            {centers.map((c) => <option key={c.id} value={c.id}>{ccLabel(c, i18n.language)}</option>)}
                          </select>
                        ) : l.cost_center_name && <Text size="xs" c="teal.8">◆ {l.cost_center_name}</Text>}
                      </Table.Td>
                      <Table.Td>{l.currency}</Table.Td>
                      <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{l.debit !== '0.0000' ? formatAmount(l.debit, d) : ''}</Table.Td>
                      <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{l.credit !== '0.0000' ? formatAmount(l.credit, d) : ''}</Table.Td>
                      <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }} c="dimmed">
                        {formatAmount(l.base_debit !== '0.0000' ? l.base_debit : '-' + l.base_credit, decimalsOf(base))}
                        {l.currency !== base && <Text span size="xs"> @ {l.rate}</Text>}
                      </Table.Td>
                    </Table.Tr>
                  );
                })}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
          <Text size="xs" c="dimmed">
            {e.created_by && `${t('journal.createdBy')}: ${e.created_by}`}
            {e.posted_by && ` · ${t('journal.postedBy')}: ${e.posted_by}`}
          </Text>
          {e.status === 'posted' && <Alert variant="light" color="gray" icon={<IconLock size={16} />}>{t('journal.locked')}</Alert>}

          {confirm === 'reverse' && (
            <Confirm text={t('journal.reverseConfirm')} yesLabel={t('journal.reverse')} noLabel={t('common.cancel')} color="orange"
              busy={busy} onYes={() => act('reverse')} onNo={() => setConfirm(null)} />
          )}
          {confirm === 'discard' && (
            <Confirm text={t('journal.discardConfirm')} yesLabel={t('journal.discard')} noLabel={t('common.cancel')}
              busy={busy} onYes={() => act('discard')} onNo={() => setConfirm(null)} />
          )}

          <Group justify="space-between">
            <Group gap="xs">
              {e.reversal_of && <Button variant="subtle" size="xs" onClick={() => onOpen(e.reversal_of!)}>← {t('journal.entry')}</Button>}
              {e.reversed_by && <Button variant="subtle" size="xs" onClick={() => onOpen(e.reversed_by!)}>{t('journal.reversal')} →</Button>}
              {e.status === 'posted' && e.source !== 'manual' && <SourceButton entryId={e.id} />}
            </Group>
            <Group gap="xs">
              {e.status === 'draft' && can(me, 'accounting.create') && (
                <>
                  <Button variant="subtle" color="red" onClick={() => setConfirm('discard')}>{t('journal.discard')}</Button>
                  <Button variant="default" onClick={() => onEdit(e)}>{t('common.edit')}</Button>
                  {canPost && <Button loading={busy} onClick={() => act('post')} leftSection={<IconCheck size={16} />}>{t('journal.post')}</Button>}
                </>
              )}
              {e.status === 'posted' && canPost && !e.reversed_by && !e.reversal_of && (
                <Button variant="default" color="orange" onClick={() => setConfirm('reverse')}>{t('journal.reverse')}</Button>
              )}
            </Group>
          </Group>
        </Stack>
      )}
    </Modal>
  );
}

interface LineState { key: number; account_id: string | null; description: string; currency: string; debit: string; credit: string; rate: string }

let lineKey = 1;
const blankLine = (cur: string): LineState => ({ key: lineKey++, account_id: null, description: '', currency: cur, debit: '', credit: '', rate: '' });

/** amount (4-dec units) divided by rate, rounded to 4 decimals — same rule as the database. */
function baseUnits(amount: bigint, rate: string): bigint {
  const m = /^(\d+)(?:\.(\d{0,10}))?$/.exec(rate.trim());
  if (!m) return 0n;
  const r = BigInt(m[1]) * 10n ** 10n + BigInt(((m[2] ?? '') + '0000000000').slice(0, 10));
  if (r === 0n) return 0n;
  const num = amount * 10n ** 10n;
  return (num * 2n + r) / (2n * r);
}

function EntryEditor({ me, entry, onClose, onSaved }: {
  me: Me; entry: Entry | null; onClose: () => void; onSaved: (e: Entry) => void;
}) {
  const { t } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const base = company?.base_currency ?? 'USD';
  const [date, setDate] = useState(entry?.date ?? today());
  const [memo, setMemo] = useState(entry?.memo ?? '');
  const [reference, setReference] = useState(entry?.reference ?? '');
  const [lines, setLines] = useState<LineState[]>(() =>
    entry ? entry.lines.map((l) => ({
      key: lineKey++, account_id: l.account_id, description: l.description, currency: l.currency,
      debit: l.debit === '0.0000' ? '' : l.debit.replace(/\.?0+$/, ''), credit: l.credit === '0.0000' ? '' : l.credit.replace(/\.?0+$/, ''),
      rate: l.currency === base ? '' : l.rate,
    })) : [blankLine(base), blankLine(base)]);
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [rates, setRates] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState<'draft' | 'post' | null>(null);
  const canPost = can(me, 'accounting.edit');

  useEffect(() => {
    if (!company) return;
    api<Account[]>(`/companies/${company.id}/accounts`).then(setAccounts).catch(() => {});
  }, [company]);

  useEffect(() => {
    if (!company || !/^\d{4}-\d{2}-\d{2}$/.test(date)) return;
    api<{ rates: Rate[] }>(`/companies/${company.id}/rates/latest?date=${date}`)
      .then((r) => setRates(Object.fromEntries(r.rates.map((x) => [x.currency, x.rate]))))
      .catch(() => {});
  }, [company, date]);

  const accountOptions = useMemo(
    () => accounts.filter((a) => a.is_postable && a.is_active).map((a) => ({ value: a.id, label: accountLabel(a) })),
    [accounts]);
  const currencyOptions = useMemo(() => {
    const set = new Set([base, company?.local_currency ?? base, ...Object.keys(rates), ...lines.map((l) => l.currency)]);
    return [...set].map((c) => ({ value: c, label: c }));
  }, [base, company, rates, lines]);

  const computed = lines.map((l) => {
    const rate = l.currency === base ? '1' : (l.rate || rates[l.currency] || '');
    return { dr: rate ? baseUnits(toUnits(l.debit), rate) : 0n, cr: rate ? baseUnits(toUnits(l.credit), rate) : 0n, rate };
  });
  const totalDr = computed.reduce((s, c) => s + c.dr, 0n);
  const totalCr = computed.reduce((s, c) => s + c.cr, 0n);
  const diff = totalDr - totalCr;
  const balanced = diff === 0n && totalDr > 0n;
  const bdec = decimalsOf(base);

  function update(key: number, patch: Partial<LineState>) {
    setLines((ls) => ls.map((l) => (l.key === key ? { ...l, ...patch } : l)));
  }

  async function save(post: boolean) {
    if (!company) return;
    setBusy(post ? 'post' : 'draft');
    const body = {
      date, memo, reference, post,
      lines: lines.filter((l) => l.account_id || l.debit || l.credit).map((l) => ({
        account_id: l.account_id ?? '', description: l.description, currency: l.currency,
        debit: l.debit, credit: l.credit, rate: l.currency === base ? '' : l.rate,
      })),
    };
    try {
      const saved = entry
        ? await api<Entry>(`/companies/${company.id}/journal/${entry.id}`, { method: 'PUT', body })
        : await api<Entry>(`/companies/${company.id}/journal`, { method: 'POST', body });
      notifySaved(t);
      const ctl = (saved as Entry & { control_accounts?: string[] }).control_accounts;
      if (ctl?.length) notifications.show({ color: 'orange', autoClose: 12000, message: t('journal.controlWarn', { codes: ctl.join(', ') }) });
      onSaved(saved);
    } catch (err) {
      notifyError(t, err);
    } finally {
      setBusy(null);
    }
  }

  return (
    <Modal opened onClose={onClose} size="90rem" title={entry ? `${t('journal.entry')} · ${t('journal.draft')}` : t('journal.new')}>
      {accountOptions.length === 0 && accounts.length === 0 ? (
        <Alert color="yellow" icon={<IconAlertTriangle size={16} />}>{t('journal.noAccounts')}</Alert>
      ) : (
        <Stack>
          <Group grow align="flex-start">
            <TextInput type="date" label={t('journal.date')} required value={date} onChange={(e) => setDate(e.currentTarget.value)} maw={200} />
            <TextInput label={t('journal.memo')} value={memo} onChange={(e) => setMemo(e.currentTarget.value)} />
            <TextInput label={t('journal.reference')} value={reference} onChange={(e) => setReference(e.currentTarget.value)} maw={220} />
          </Group>
          <Table.ScrollContainer minWidth={980}>
            <Table verticalSpacing={4} withTableBorder>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th miw={260}>{t('journal.account')}</Table.Th>
                  <Table.Th miw={150}>{t('journal.lineDesc')}</Table.Th>
                  <Table.Th w={95}>{t('journal.currency')}</Table.Th>
                  <Table.Th w={140}>{t('journal.debit')}</Table.Th>
                  <Table.Th w={140}>{t('journal.credit')}</Table.Th>
                  <Table.Th w={120}>{t('journal.rate')}</Table.Th>
                  <Table.Th w={130} style={{ textAlign: 'end' }}>{t('journal.inBase', { cur: base })}</Table.Th>
                  <Table.Th w={40} />
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {lines.map((l, i) => {
                  const c = computed[i];
                  const missingRate = l.currency !== base && !c.rate && (l.debit || l.credit);
                  return (
                    <Table.Tr key={l.key}>
                      <Table.Td>
                        <Select size="xs" searchable data={accountOptions} value={l.account_id}
                          onChange={(v) => update(l.key, { account_id: v })} comboboxProps={{ withinPortal: true }} />
                      </Table.Td>
                      <Table.Td><TextInput size="xs" value={l.description} onChange={(e) => update(l.key, { description: e.currentTarget.value })} /></Table.Td>
                      <Table.Td>
                        <Select size="xs" data={currencyOptions} value={l.currency} allowDeselect={false} searchable
                          onChange={(v) => v && update(l.key, { currency: v, rate: '' })} comboboxProps={{ withinPortal: true }} />
                      </Table.Td>
                      <Table.Td>
                        <TextInput size="xs" dir="ltr" inputMode="decimal" value={l.debit} styles={{ input: { textAlign: 'end', fontFamily: 'monospace' } }}
                          onChange={(e) => update(l.key, { debit: e.currentTarget.value.replace(/[^\d.]/g, ''), credit: e.currentTarget.value ? '' : l.credit })} />
                      </Table.Td>
                      <Table.Td>
                        <TextInput size="xs" dir="ltr" inputMode="decimal" value={l.credit} styles={{ input: { textAlign: 'end', fontFamily: 'monospace' } }}
                          onChange={(e) => update(l.key, { credit: e.currentTarget.value.replace(/[^\d.]/g, ''), debit: e.currentTarget.value ? '' : l.debit })} />
                      </Table.Td>
                      <Table.Td>
                        {l.currency === base ? <Text size="xs" c="dimmed" ta="center">1</Text> : (
                          <TextInput size="xs" dir="ltr" inputMode="decimal" value={l.rate} placeholder={rates[l.currency] ?? '?'}
                            error={missingRate ? ' ' : undefined} styles={{ input: { fontFamily: 'monospace' } }}
                            onChange={(e) => update(l.key, { rate: e.currentTarget.value.replace(/[^\d.]/g, '') })} />
                        )}
                      </Table.Td>
                      <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }} c="dimmed">
                        {c.dr ? formatAmount(fromUnits(c.dr), bdec) : c.cr ? formatAmount(fromUnits(-c.cr), bdec) : ''}
                      </Table.Td>
                      <Table.Td>
                        <Tooltip label="✕"><ActionIcon variant="subtle" color="gray" disabled={lines.length <= 2}
                          onClick={() => setLines((ls) => ls.filter((x) => x.key !== l.key))}><IconTrash size={14} /></ActionIcon></Tooltip>
                      </Table.Td>
                    </Table.Tr>
                  );
                })}
              </Table.Tbody>
              <Table.Tfoot>
                <Table.Tr>
                  <Table.Td colSpan={3}>
                    <Button size="xs" variant="subtle" leftSection={<IconPlus size={14} />}
                      onClick={() => setLines((ls) => [...ls, blankLine(ls[ls.length - 1]?.currency ?? base)])}>{t('journal.addLine')}</Button>
                  </Table.Td>
                  <Table.Td ff="monospace" dir="ltr" fw={600} style={{ textAlign: 'end' }}>{formatAmount(fromUnits(totalDr), bdec)}</Table.Td>
                  <Table.Td ff="monospace" dir="ltr" fw={600} style={{ textAlign: 'end' }}>{formatAmount(fromUnits(totalCr), bdec)}</Table.Td>
                  <Table.Td colSpan={3} style={{ textAlign: 'end' }}>
                    {balanced ? (
                      <Badge color="teal" variant="light" leftSection={<IconCheck size={12} />}>{t('journal.balanced')}</Badge>
                    ) : (
                      <Badge color="red" variant="light">{t('journal.difference')}: <span dir="ltr">{formatAmount(fromUnits(diff), 4)}</span> {base}</Badge>
                    )}
                  </Table.Td>
                </Table.Tr>
              </Table.Tfoot>
            </Table>
          </Table.ScrollContainer>
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
            <Button variant="light" loading={busy === 'draft'} disabled={busy !== null} onClick={() => save(false)}>{t('journal.saveDraft')}</Button>
            {canPost && (
              <Button loading={busy === 'post'} disabled={!balanced || busy !== null} onClick={() => save(true)}
                leftSection={<IconCheck size={16} />}>{t('journal.post')}</Button>
            )}
          </Group>
        </Stack>
      )}
    </Modal>
  );
}
