import { useCallback, useEffect, useState } from 'react';
import {
  ActionIcon, Badge, Button, Card, Center, Group, Loader, Modal, Paper, ScrollArea, SegmentedControl, Select, SimpleGrid, Stack, Table,
  Text, Textarea, TextInput, Tooltip,
} from '@mantine/core';
import { useDebouncedValue } from '@mantine/hooks';
import {
  IconCalendarEvent, IconCheck, IconFileInvoice, IconMail, IconMapPin, IconMessage, IconNote, IconPhone, IconPlus, IconSearch, IconTrash,
  IconTrophy, IconUserPlus, IconUsersGroup, IconX,
} from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { can, api, type Me } from '../../api';
import { useCompany } from '../../company';
import { formatAmount, formatDate, formatDateTime, notifyError, notifySaved, today } from '../../util';
import PageHeader from '../../components/PageHeader';
import { OrderEditor, OrderView } from '../sales/Sales';
import { orderColor } from '../sales/common';

export interface Stage { id: string; name: string; name_i18n: Record<string, string>; seq: number; probability: number; is_won: boolean; is_active: boolean }
export interface Lead {
  id: string; number: number; title: string; contact_name: string; organisation: string; phone: string; email: string; address: string;
  source: string; stage_id: string | null; stage_name: string | null; stage_name_i18n: Record<string, string> | null; status: 'open' | 'won' | 'lost';
  lost_reason: string; expected_revenue: string; currency: string | null; probability: number; expected_close: string | null;
  assigned_to: string | null; assigned_name: string | null; customer_id: string | null; customer_name: string | null; notes: string;
  created_at: string; updated_at: string; next_activity: string | null; quotes: number;
}
export interface Activity {
  id: string; lead_id: string | null; lead_title: string | null; customer_id: string | null; customer_name: string | null;
  kind: 'note' | 'call' | 'meeting' | 'task' | 'whatsapp' | 'email' | 'visit'; summary: string; details: string; due_date: string | null;
  assigned_to: string | null; assigned_name: string | null; done_at: string | null; done_by_name: string | null; created_by_name: string | null; created_at: string;
}

const kindIcon = { note: IconNote, call: IconPhone, meeting: IconUsersGroup, task: IconCheck, whatsapp: IconMessage, email: IconMail, visit: IconMapPin };

function stageLabel(s: { name: string; name_i18n?: Record<string, string> | null }, lang: string) {
  return s.name_i18n?.[lang] || s.name;
}

function dueColor(d: string | null): string | undefined {
  if (!d) return undefined;
  const t = today();
  return d < t ? 'red' : d === t ? 'orange' : 'gray';
}

interface UserLite { id: string; full_name: string; is_active: boolean }
function useUsers(): UserLite[] {
  const { company } = useCompany();
  const [u, setU] = useState<UserLite[]>([]);
  useEffect(() => { if (company) api<UserLite[]>(`/companies/${company.id}/crm/users`).then(setU).catch(() => {}); }, [company]);
  return u;
}

export default function Crm({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [stages, setStages] = useState<Stage[]>([]);
  const [leads, setLeads] = useState<Lead[] | null>(null);
  const [status, setStatus] = useState('open');
  const [mine, setMine] = useState(false);
  const [view, setView] = useState<'board' | 'list'>('board');
  const [q, setQ] = useState('');
  const [dq] = useDebouncedValue(q, 250);
  const [open, setOpen] = useState<string | 'new' | null>(null);
  const [summary, setSummary] = useState<{ activities: { overdue: number; today: number; mine: number }; leads: { won_30d: number; lost_30d: number; open: number } } | null>(null);
  const [drag, setDrag] = useState<string | null>(null);
  const canEdit = can(me, 'crm.edit');

  const load = useCallback(() => {
    if (!company) return;
    const b = `/companies/${company.id}/crm`;
    api<Stage[]>(`${b}/stages`).then((s) => setStages(s.filter((x) => x.is_active))).catch(() => {});
    api<Lead[]>(`${b}/leads?status=${status}&q=${encodeURIComponent(dq)}${mine ? '&assigned_to=me' : ''}`).then(setLeads).catch((e) => notifyError(t, e));
    api<typeof summary>(`${b}/summary`).then(setSummary).catch(() => {});
  }, [company, status, dq, mine, t]);
  useEffect(load, [load]);

  async function move(leadId: string, stageId: string) {
    if (!company) return;
    setLeads((ls) => ls && ls.map((l) => (l.id === leadId ? { ...l, stage_id: stageId } : l)));
    try {
      await api(`/companies/${company.id}/crm/leads/${leadId}`, { method: 'PATCH', body: { stage_id: stageId } });
      load();
    } catch (e) {
      notifyError(t, e);
      load();
    }
  }

  if (!company) return null;
  const lang = i18n.language;
  const card = (l: Lead) => (
    <Card key={l.id} withBorder padding="xs" radius="md" draggable={canEdit && l.status === 'open'} style={{ cursor: 'pointer', opacity: drag === l.id ? 0.5 : 1 }}
      onDragStart={() => setDrag(l.id)} onDragEnd={() => setDrag(null)} onClick={() => setOpen(l.id)}>
      <Text size="sm" fw={600} lineClamp={2}>{l.title}</Text>
      <Text size="xs" c="dimmed" lineClamp={1}>{[l.organisation, l.contact_name].filter(Boolean).join(' · ') || l.customer_name}</Text>
      <Group justify="space-between" mt={4} gap={4}>
        {Number(l.expected_revenue) > 0
          ? <Text size="xs" fw={600} dir="ltr">{formatAmount(l.expected_revenue, decimalsOf(l.currency ?? company.base_currency))} {l.currency ?? company.base_currency}</Text>
          : <span />}
        <Group gap={4}>
          {l.next_activity && <Badge size="xs" variant="light" color={dueColor(l.next_activity)} leftSection={<IconCalendarEvent size={10} />}>{formatDate(l.next_activity)}</Badge>}
          {l.assigned_name && <Tooltip label={l.assigned_name}><Badge size="xs" variant="outline" color="gray">{l.assigned_name.slice(0, 2).toUpperCase()}</Badge></Tooltip>}
        </Group>
      </Group>
    </Card>
  );

  return (
    <Stack gap="lg">
      <PageHeader title={t('crm.title')} subtitle={company.display ?? company.name_en}>
        <Button variant="default" component="a" href="#/activities" leftSection={<IconCalendarEvent size={16} />}>
          {t('crm.activities')}{summary && summary.activities.overdue > 0 ? ` (${summary.activities.overdue})` : ''}
        </Button>
        {canEdit && <Button leftSection={<IconPlus size={16} />} onClick={() => setOpen('new')}>{t('crm.newLead')}</Button>}
      </PageHeader>
      {summary && (
        <SimpleGrid cols={{ base: 2, sm: 5 }}>
          <Stat label={t('crm.openLeads')} value={summary.leads.open} />
          <Stat label={t('crm.won30')} value={summary.leads.won_30d} color="teal.7" />
          <Stat label={t('crm.lost30')} value={summary.leads.lost_30d} color="red.7" />
          <Stat label={t('crm.overdue')} value={summary.activities.overdue} color={summary.activities.overdue ? 'red.7' : undefined} />
          <Stat label={t('crm.today')} value={summary.activities.today} />
        </SimpleGrid>
      )}
      <Group gap="sm" wrap="wrap">
        <SegmentedControl value={view} onChange={(v) => setView(v as 'board' | 'list')} data={[{ value: 'board', label: t('crm.board') }, { value: 'list', label: t('crm.list') }]} />
        <SegmentedControl value={status} onChange={setStatus} data={[
          { value: 'open', label: t('crm.st.open') }, { value: 'won', label: t('crm.st.won') }, { value: 'lost', label: t('crm.st.lost') }, { value: '', label: t('common.all') },
        ]} />
        <SegmentedControl value={mine ? 'me' : 'all'} onChange={(v) => setMine(v === 'me')} data={[{ value: 'all', label: t('crm.everyone') }, { value: 'me', label: t('crm.mine') }]} />
        <TextInput leftSection={<IconSearch size={16} />} placeholder={t('common.search')} value={q} onChange={(e) => setQ(e.currentTarget.value)} w={220} />
      </Group>
      {!leads ? <Center p="xl"><Loader size="sm" /></Center> : view === 'board' && status === 'open' ? (
        <ScrollArea type="auto" offsetScrollbars>
          <Group align="flex-start" wrap="nowrap" gap="sm">
            {stages.map((s) => {
              const list = leads.filter((l) => l.stage_id === s.id);
              const total = list.reduce((a, l) => a + Number(l.expected_revenue || 0), 0);
              return (
                <Paper key={s.id} withBorder p="xs" w={270} bg="var(--mx-panel)" style={{ flex: 'none', minHeight: 200 }}
                  onDragOver={(e) => e.preventDefault()} onDrop={() => { if (drag) void move(drag, s.id); setDrag(null); }}>
                  <Group justify="space-between" mb="xs">
                    <Text fw={600} size="sm">{stageLabel(s, lang)} <Text span c="dimmed" size="xs">({list.length})</Text></Text>
                    {total > 0 && <Text size="xs" c="dimmed" dir="ltr">{formatAmount(total, 0)}</Text>}
                  </Group>
                  <Stack gap="xs">{list.map(card)}</Stack>
                </Paper>
              );
            })}
          </Group>
        </ScrollArea>
      ) : (
        <Paper withBorder>
          {leads.length === 0 ? <Text c="dimmed" ta="center" p="xl">{t('common.empty')}</Text> : (
            <Table.ScrollContainer minWidth={800}>
              <Table highlightOnHover verticalSpacing="xs">
                <Table.Thead><Table.Tr>
                  <Table.Th>{t('crm.lead')}</Table.Th><Table.Th>{t('crm.stage')}</Table.Th><Table.Th>{t('crm.assigned')}</Table.Th>
                  <Table.Th>{t('crm.nextActivity')}</Table.Th><Table.Th style={{ textAlign: 'end' }}>{t('crm.expected')}</Table.Th>
                </Table.Tr></Table.Thead>
                <Table.Tbody>
                  {leads.map((l) => (
                    <Table.Tr key={l.id} style={{ cursor: 'pointer' }} onClick={() => setOpen(l.id)}>
                      <Table.Td><Text size="sm" fw={500}>{l.title}</Text><Text size="xs" c="dimmed">{[l.organisation, l.contact_name, l.phone].filter(Boolean).join(' · ')}</Text></Table.Td>
                      <Table.Td>{l.status === 'open' ? (l.stage_name ? stageLabel({ name: l.stage_name, name_i18n: l.stage_name_i18n }, lang) : '—')
                        : <Badge color={l.status === 'won' ? 'teal' : 'red'} variant="light">{t(`crm.st.${l.status}`)}</Badge>}</Table.Td>
                      <Table.Td>{l.assigned_name ?? '—'}</Table.Td>
                      <Table.Td>{l.next_activity ? <Text size="sm" c={dueColor(l.next_activity)}>{formatDate(l.next_activity)}</Text> : '—'}</Table.Td>
                      <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{Number(l.expected_revenue) ? `${formatAmount(l.expected_revenue, 2)} ${l.currency ?? ''}` : ''}</Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </Table.ScrollContainer>
          )}
        </Paper>
      )}
      {open && <LeadView id={open === 'new' ? null : open} me={me} stages={stages} onClose={() => setOpen(null)} onChanged={load}
        onCreated={(id) => { setOpen(id); load(); }} />}
    </Stack>
  );
}

function Stat({ label, value, color }: { label: string; value: number; color?: string }) {
  return <Paper withBorder p="sm"><Text size="xs" c="dimmed">{label}</Text><Text fw={700} size="xl" c={color}>{value}</Text></Paper>;
}

function LeadView({ id, me, stages, onClose, onChanged, onCreated }: {
  id: string | null; me: Me; stages: Stage[]; onClose: () => void; onChanged: () => void; onCreated: (id: string) => void;
}) {
  const { t, i18n } = useTranslation();
  const { company, currencies, decimalsOf } = useCompany();
  const users = useUsers();
  const [d, setD] = useState<{ lead: Lead; activities: Activity[]; quotes: { id: string; number: number; order_number: number | null; status: string; currency: string; total: string; quote_date: string }[] } | null>(null);
  const [f, setF] = useState<Partial<Lead>>({ currency: company?.base_currency ?? 'USD' });
  const [busy, setBusy] = useState(false);
  const [lost, setLost] = useState<string | null>(null);
  const [quote, setQuote] = useState<{ id: string; name: string; code: string } | null>(null);
  const [orderView, setOrderView] = useState<string | null>(null);
  const canEdit = can(me, 'crm.edit');

  const reload = useCallback(() => {
    if (!company || !id) return;
    api<NonNullable<typeof d>>(`/companies/${company.id}/crm/leads/${id}`).then((x) => { setD(x); setF(x.lead); }).catch((e) => notifyError(t, e));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [company, id]);
  useEffect(reload, [reload]);

  async function save(patch?: Record<string, unknown>) {
    if (!company) return;
    setBusy(true);
    try {
      const body = patch ?? {
        title: f.title, contact_name: f.contact_name ?? '', organisation: f.organisation ?? '', phone: f.phone ?? '', email: f.email ?? '',
        address: f.address ?? '', source: f.source ?? '', expected_revenue: f.expected_revenue ? String(f.expected_revenue) : '',
        currency: f.currency ?? '', expected_close: f.expected_close ?? '', assigned_to: f.assigned_to ?? '', notes: f.notes ?? '',
        ...(f.stage_id ? { stage_id: f.stage_id } : {}),
      };
      if (id) {
        const r = await api<NonNullable<typeof d>>(`/companies/${company.id}/crm/leads/${id}`, { method: 'PATCH', body });
        setD(r);
        setF(r.lead);
        notifySaved(t);
        onChanged();
      } else {
        const r = await api<NonNullable<typeof d>>(`/companies/${company.id}/crm/leads`, { method: 'POST', body });
        notifySaved(t);
        onCreated(r.lead.id);
      }
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  async function convert(): Promise<{ id: string; name: string; code: string } | null> {
    if (!company || !id) return null;
    try {
      const r = await api<NonNullable<typeof d>>(`/companies/${company.id}/crm/leads/${id}/convert`, { method: 'POST', body: {} });
      setD(r);
      onChanged();
      return { id: r.lead.customer_id!, name: r.lead.customer_name ?? '', code: '' };
    } catch (e) {
      notifyError(t, e);
      return null;
    }
  }

  async function newQuote() {
    const c = d?.lead.customer_id ? { id: d.lead.customer_id, name: d.lead.customer_name ?? '', code: '' } : await convert();
    if (c) setQuote(c);
  }

  const l = d?.lead;
  const set = (k: keyof Lead) => (e: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) => setF({ ...f, [k]: e.currentTarget.value });
  return (
    <Modal opened onClose={onClose} size="xl" title={l ? `${t('crm.lead')} #${l.number} · ${l.title}` : t('crm.newLead')}>
      {id && !d ? <Center p="xl"><Loader size="sm" /></Center> : (
        <Stack>
          {l && (
            <Group justify="space-between">
              <Group gap="xs">
                {l.status === 'open' ? stages.map((s) => (
                  <Button key={s.id} size="compact-sm" variant={l.stage_id === s.id ? 'filled' : 'default'} disabled={!canEdit}
                    onClick={() => void save({ stage_id: s.id })}>{stageLabel(s, i18n.language)}</Button>
                )) : <Badge size="lg" color={l.status === 'won' ? 'teal' : 'red'}>{t(`crm.st.${l.status}`)}{l.lost_reason ? ` · ${l.lost_reason}` : ''}</Badge>}
              </Group>
              {canEdit && (
                <Group gap="xs">
                  {l.status === 'open' ? <>
                    <Button size="xs" color="teal" leftSection={<IconTrophy size={14} />} onClick={() => void save({ status: 'won' })}>{t('crm.markWon')}</Button>
                    <Button size="xs" color="red" variant="light" leftSection={<IconX size={14} />} onClick={() => setLost('')}>{t('crm.markLost')}</Button>
                  </> : <Button size="xs" variant="default" onClick={() => void save({ status: 'open' })}>{t('crm.reopen')}</Button>}
                </Group>
              )}
            </Group>
          )}
          {lost !== null && (
            <Group align="flex-end">
              <TextInput style={{ flex: 1 }} label={t('crm.lostReason')} value={lost} onChange={(e) => setLost(e.currentTarget.value)} />
              <Button color="red" onClick={() => { void save({ status: 'lost', lost_reason: lost }); setLost(null); }}>{t('crm.markLost')}</Button>
            </Group>
          )}
          <SimpleGrid cols={{ base: 1, sm: 2 }}>
            <TextInput label={t('crm.titleField')} required value={f.title ?? ''} onChange={set('title')} disabled={!canEdit} />
            <TextInput label={t('crm.organisation')} value={f.organisation ?? ''} onChange={set('organisation')} disabled={!canEdit} />
            <TextInput label={t('crm.contact')} value={f.contact_name ?? ''} onChange={set('contact_name')} disabled={!canEdit} />
            <TextInput label={t('customers.phone')} dir="ltr" value={f.phone ?? ''} onChange={set('phone')} disabled={!canEdit} />
            <TextInput label={t('customers.email')} dir="ltr" value={f.email ?? ''} onChange={set('email')} disabled={!canEdit} />
            <TextInput label={t('crm.source')} placeholder={t('crm.sourceHint')} value={f.source ?? ''} onChange={set('source')} disabled={!canEdit} />
            <Group grow>
              <TextInput label={t('crm.expected')} dir="ltr" value={f.expected_revenue && Number(f.expected_revenue) ? String(Number(f.expected_revenue)) : (f.expected_revenue ?? '')}
                onChange={(e) => setF({ ...f, expected_revenue: e.currentTarget.value.replace(/[^\d.]/g, '') })} disabled={!canEdit} />
              <Select label={t('pur.currency')} searchable data={currencies.map((c) => c.code)} value={f.currency ?? null}
                onChange={(v) => setF({ ...f, currency: v })} disabled={!canEdit} />
            </Group>
            <Group grow>
              <TextInput type="date" label={t('crm.expectedClose')} value={f.expected_close ?? ''} onChange={set('expected_close')} disabled={!canEdit} />
              <Select label={t('crm.assigned')} clearable data={users.map((u) => ({ value: u.id, label: u.full_name }))} value={f.assigned_to ?? null}
                onChange={(v) => setF({ ...f, assigned_to: v })} disabled={!canEdit} />
            </Group>
            {!id && <Select label={t('crm.stage')} data={stages.filter((s) => !s.is_won).map((s) => ({ value: s.id, label: stageLabel(s, i18n.language) }))}
              value={f.stage_id ?? null} onChange={(v) => setF({ ...f, stage_id: v })} />}
          </SimpleGrid>
          <TextInput label={t('customers.address')} value={f.address ?? ''} onChange={set('address')} disabled={!canEdit} />
          <Textarea label={t('crm.notes')} autosize minRows={2} value={f.notes ?? ''} onChange={set('notes')} disabled={!canEdit} />
          {canEdit && (
            <Group justify="space-between">
              <Group gap="xs">
                {l && !l.customer_id && <Button variant="default" leftSection={<IconUserPlus size={16} />} onClick={() => void convert()}>{t('crm.convert')}</Button>}
                {l?.customer_id && <Button variant="subtle" component="a" href={`#/customer/${l.customer_id}`}>{l.customer_name}</Button>}
                {l && <Button variant="light" leftSection={<IconFileInvoice size={16} />} onClick={() => void newQuote()}>{t('sales.newQuote')}</Button>}
              </Group>
              <Button loading={busy} disabled={!f.title} onClick={() => void save()}>{t('common.save')}</Button>
            </Group>
          )}
          {d && d.quotes.length > 0 && (
            <Paper withBorder p="sm">
              <Text size="sm" fw={600} mb={4}>{t('sales.quotes')}</Text>
              {d.quotes.map((x) => (
                <Group key={x.id} gap="xs" style={{ cursor: 'pointer' }} onClick={() => setOrderView(x.id)}>
                  <Text size="sm" ff="monospace">{x.order_number ? `SO-${x.order_number}` : `Q-${x.number}`}</Text>
                  <Badge size="xs" variant="light" color={orderColor[x.status]}>{t(`sales.st.${x.status}`)}</Badge>
                  <Text size="sm" dir="ltr">{formatAmount(x.total, decimalsOf(x.currency))} {x.currency}</Text>
                </Group>
              ))}
            </Paper>
          )}
          {l && <Activities leadId={l.id} canEdit={canEdit} initial={d?.activities} onChanged={onChanged} />}
          {quote && <OrderEditor customer={quote} leadId={id ?? undefined} onClose={() => setQuote(null)}
            onDone={(oid) => { setQuote(null); reload(); setOrderView(oid); }} />}
          {orderView && <OrderView id={orderView} me={me} onClose={() => setOrderView(null)} onChanged={reload} />}
        </Stack>
      )}
    </Modal>
  );
}

/** Activities & notes of a lead or a customer: timeline + quick add. */
export function Activities({ leadId, customerId, canEdit, initial, onChanged }: {
  leadId?: string; customerId?: string; canEdit: boolean; initial?: Activity[]; onChanged?: () => void;
}) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const [rows, setRows] = useState<Activity[] | null>(initial ?? null);
  const [kind, setKind] = useState<Activity['kind']>('note');
  const [summary, setSummary] = useState('');
  const [due, setDue] = useState('');
  const [busy, setBusy] = useState(false);

  const load = useCallback(() => {
    if (!company) return;
    api<Activity[]>(`/companies/${company.id}/crm/activities?${leadId ? `lead_id=${leadId}` : `customer_id=${customerId}`}`).then(setRows).catch(() => setRows([]));
  }, [company, leadId, customerId]);
  useEffect(() => { if (!initial) load(); }, [load, initial]);
  useEffect(() => { if (initial) setRows(initial); }, [initial]);

  async function add() {
    if (!company || !summary.trim()) return;
    setBusy(true);
    try {
      await api(`/companies/${company.id}/crm/activities`, { method: 'POST', body: {
        lead_id: leadId ?? '', customer_id: customerId ?? '', kind, summary, ...(kind !== 'note' && due ? { due_date: due } : {}),
      } });
      setSummary('');
      setDue('');
      load();
      onChanged?.();
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  async function patch(a: Activity, body: Record<string, unknown>) {
    if (!company) return;
    try {
      await api(`/companies/${company.id}/crm/activities/${a.id}`, { method: 'PATCH', body });
      load();
      onChanged?.();
    } catch (e) {
      notifyError(t, e);
    }
  }

  async function remove(a: Activity) {
    if (!company) return;
    try {
      await api(`/companies/${company.id}/crm/activities/${a.id}`, { method: 'DELETE' });
      load();
    } catch (e) {
      notifyError(t, e);
    }
  }

  return (
    <Paper withBorder p="sm">
      <Text size="sm" fw={600} mb="xs">{t('crm.activitiesNotes')}</Text>
      {canEdit && (
        <Group align="flex-end" gap="xs" mb="sm" wrap="wrap">
          <Select w={130} size="xs" value={kind} allowDeselect={false} onChange={(v) => v && setKind(v as Activity['kind'])}
            data={(Object.keys(kindIcon) as Activity['kind'][]).map((k) => ({ value: k, label: t(`crm.kinds.${k}`) }))} />
          <TextInput size="xs" style={{ flex: 1, minWidth: 200 }} placeholder={kind === 'note' ? t('crm.notePlaceholder') : t('crm.taskPlaceholder')}
            value={summary} onChange={(e) => setSummary(e.currentTarget.value)} onKeyDown={(e) => { if (e.key === 'Enter') void add(); }} />
          {kind !== 'note' && <TextInput size="xs" type="date" value={due} onChange={(e) => setDue(e.currentTarget.value)} />}
          <Button size="xs" loading={busy} disabled={!summary.trim()} onClick={() => void add()}>{t('crm.add')}</Button>
        </Group>
      )}
      {!rows ? <Loader size="xs" /> : rows.length === 0 ? <Text size="sm" c="dimmed">{t('common.empty')}</Text> : (
        <Stack gap={6}>
          {rows.map((a) => {
            const Icon = kindIcon[a.kind];
            return (
              <Group key={a.id} gap="xs" wrap="nowrap" align="flex-start" style={{ opacity: a.done_at ? 0.55 : 1 }}>
                <Icon size={16} style={{ marginTop: 2, flex: 'none' }} />
                <div style={{ flex: 1 }}>
                  <Text size="sm" td={a.done_at && a.kind !== 'note' ? 'line-through' : undefined} style={{ whiteSpace: 'pre-wrap' }}>{a.summary}</Text>
                  <Text size="xs" c="dimmed">
                    {t(`crm.kinds.${a.kind}`)} · {a.created_by_name ?? ''} · {formatDateTime(a.created_at)}
                    {a.due_date && !a.done_at ? <Text span c={dueColor(a.due_date)} size="xs"> · {t('crm.due')} {formatDate(a.due_date)}</Text> : null}
                    {a.assigned_name && a.kind !== 'note' ? ` · ${a.assigned_name}` : ''}
                  </Text>
                </div>
                {canEdit && a.kind !== 'note' && (
                  <Tooltip label={a.done_at ? t('crm.undo') : t('crm.done')}>
                    <ActionIcon size="sm" variant={a.done_at ? 'subtle' : 'light'} color="teal" onClick={() => void patch(a, { done: !a.done_at })}><IconCheck size={14} /></ActionIcon>
                  </Tooltip>
                )}
                {canEdit && <ActionIcon size="sm" variant="subtle" color="gray" onClick={() => void remove(a)}><IconTrash size={14} /></ActionIcon>}
              </Group>
            );
          })}
        </Stack>
      )}
    </Paper>
  );
}

/** #/activities — open activities (mine / everyone), by due date. */
export function ActivitiesPage({ me }: { me: Me }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const [mine, setMine] = useState(true);
  const [rows, setRows] = useState<Activity[] | null>(null);
  const load = useCallback(() => {
    if (!company) return;
    setRows(null);
    api<Activity[]>(`/companies/${company.id}/crm/activities?${mine ? 'mine=1' : 'open=1'}`).then(setRows).catch((e) => notifyError(t, e));
  }, [company, mine, t]);
  useEffect(load, [load]);

  async function done(a: Activity) {
    if (!company) return;
    try {
      await api(`/companies/${company.id}/crm/activities/${a.id}`, { method: 'PATCH', body: { done: true } });
      load();
    } catch (e) {
      notifyError(t, e);
    }
  }

  if (!company) return null;
  return (
    <Stack gap="lg" maw={1000}>
      <PageHeader title={t('crm.activities')} subtitle={company.display ?? company.name_en}>
        <Button variant="default" component="a" href="#/crm">{t('crm.title')}</Button>
      </PageHeader>
      <SegmentedControl style={{ alignSelf: 'flex-start' }} value={mine ? 'me' : 'all'} onChange={(v) => setMine(v === 'me')}
        data={[{ value: 'me', label: t('crm.mine') }, { value: 'all', label: t('crm.everyone') }]} />
      <Paper withBorder>
        {!rows ? <Center p="xl"><Loader size="sm" /></Center> : rows.length === 0 ? <Text c="dimmed" ta="center" p="xl">{t('crm.nothingToDo')}</Text> : (
          <Table verticalSpacing="xs">
            <Table.Tbody>
              {rows.map((a) => {
                const Icon = kindIcon[a.kind];
                return (
                  <Table.Tr key={a.id}>
                    <Table.Td w={30}><Icon size={16} /></Table.Td>
                    <Table.Td>
                      <Text size="sm" fw={500}>{a.summary}</Text>
                      <Text size="xs" c="dimmed">
                        {a.lead_title && <a href="#/crm">{a.lead_title}</a>}{a.lead_title && a.customer_name ? ' · ' : ''}
                        {a.customer_name && <a href={`#/customer/${a.customer_id}`}>{a.customer_name}</a>}
                        {!mine && a.assigned_name ? ` · ${a.assigned_name}` : ''}
                      </Text>
                    </Table.Td>
                    <Table.Td w={130}>{a.due_date ? <Text size="sm" c={dueColor(a.due_date)}>{formatDate(a.due_date)}</Text> : <Text size="sm" c="dimmed">—</Text>}</Table.Td>
                    <Table.Td w={90}>{can(me, 'crm.edit') && <Button size="compact-xs" variant="light" color="teal" leftSection={<IconCheck size={12} />} onClick={() => void done(a)}>{t('crm.done')}</Button>}</Table.Td>
                  </Table.Tr>
                );
              })}
            </Table.Tbody>
          </Table>
        )}
      </Paper>
    </Stack>
  );
}
