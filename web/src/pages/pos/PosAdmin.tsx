import { useCallback, useEffect, useState } from 'react';
import {
  Alert, Badge, Button, Center, Group, Loader, Modal, MultiSelect, Paper, SegmentedControl, Select, Stack, Switch, Table, Tabs, Text,
  Textarea, TextInput,
} from '@mantine/core';
import { IconCashRegister, IconEdit, IconPlus, IconPrinter } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { can, api, type Account, type Me } from '../../api';
import { useCompany } from '../../company';
import { accountLabel, formatAmount, formatDateTime, notifyError, notifySaved, today } from '../../util';
import { printPage } from '../../native';
import PageHeader from '../../components/PageHeader';
import ExportMenu from '../../components/ExportMenu';
import type { PaymentMethod } from '../../billing';
import type { Warehouse } from '../purchases/types';
import { SessionReport, type Register } from './common';

interface Category { id: string; name: string; name_i18n: Record<string, string>; color: string; seq: number; is_active: boolean; items: number }

const colors = ['teal', 'blue', 'cyan', 'grape', 'violet', 'pink', 'red', 'orange', 'yellow', 'lime', 'green', 'gray'];

// ---------- setup: registers and categories ----------

export default function PosSetup({ me }: { me: Me }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const [regs, setRegs] = useState<Register[] | null>(null);
  const [cats, setCats] = useState<Category[] | null>(null);
  const [editReg, setEditReg] = useState<Register | 'new' | null>(null);
  const [editCat, setEditCat] = useState<Category | 'new' | null>(null);
  const canManage = can(me, 'pos.manage');

  const load = useCallback(() => {
    if (!company) return;
    api<Register[]>(`/companies/${company.id}/pos/registers`).then(setRegs).catch((e) => notifyError(t, e));
    api<Category[]>(`/companies/${company.id}/pos/categories`).then(setCats).catch(() => setCats([]));
  }, [company, t]);
  useEffect(load, [load]);
  if (!company) return null;

  return (
    <Stack gap="lg" maw={1100}>
      <PageHeader title={t('pos.setup')} subtitle={company.name_en}>
        <Button leftSection={<IconCashRegister size={16} />} component="a" href="#/pos">{t('pos.open')}</Button>
      </PageHeader>
      <Tabs defaultValue="registers" keepMounted={false}>
        <Tabs.List mb="md">
          <Tabs.Tab value="registers">{t('pos.registers')}</Tabs.Tab>
          <Tabs.Tab value="categories">{t('pos.categories')}</Tabs.Tab>
        </Tabs.List>
        <Tabs.Panel value="registers">
          <Stack>
            <Text size="sm" c="dimmed">{t('pos.registersHint')}</Text>
            {canManage && <Button leftSection={<IconPlus size={16} />} style={{ alignSelf: 'flex-start' }} onClick={() => setEditReg('new')}>{t('pos.newRegister')}</Button>}
            <Paper withBorder>
              {regs === null ? <Center p="xl"><Loader size="sm" /></Center> : regs.length === 0 ? <Text c="dimmed" ta="center" p="xl">{t('common.empty')}</Text> : (
                <Table.ScrollContainer minWidth={640}>
                  <Table verticalSpacing="sm">
                    <Table.Thead><Table.Tr>
                      <Table.Th>{t('pos.name')}</Table.Th><Table.Th>{t('pur.currency')}</Table.Th><Table.Th>{t('pos.warehouse')}</Table.Th>
                      <Table.Th>{t('pos.walkIn')}</Table.Th><Table.Th>{t('pos.session')}</Table.Th><Table.Th />
                    </Table.Tr></Table.Thead>
                    <Table.Tbody>
                      {regs.map((r) => (
                        <Table.Tr key={r.id} style={{ opacity: r.is_active ? 1 : 0.5 }}>
                          <Table.Td fw={500}>{r.name}</Table.Td>
                          <Table.Td>{r.currency}</Table.Td>
                          <Table.Td>{r.warehouse}</Table.Td>
                          <Table.Td>{r.customer_name}</Table.Td>
                          <Table.Td>{r.open_session
                            ? <Badge color="teal" variant="light">#{r.open_session.number} · {r.open_session.opened_by}</Badge>
                            : <Badge color="gray" variant="light">{t('pos.status.closed')}</Badge>}</Table.Td>
                          <Table.Td>{canManage && <Button size="compact-sm" variant="subtle" leftSection={<IconEdit size={14} />} onClick={() => setEditReg(r)}>{t('common.edit')}</Button>}</Table.Td>
                        </Table.Tr>
                      ))}
                    </Table.Tbody>
                  </Table>
                </Table.ScrollContainer>
              )}
            </Paper>
          </Stack>
        </Tabs.Panel>
        <Tabs.Panel value="categories">
          <Stack>
            <Text size="sm" c="dimmed">{t('pos.categoriesHint')}</Text>
            {canManage && <Button leftSection={<IconPlus size={16} />} style={{ alignSelf: 'flex-start' }} onClick={() => setEditCat('new')}>{t('pos.newCategory')}</Button>}
            <Paper withBorder>
              {cats === null ? <Center p="xl"><Loader size="sm" /></Center> : cats.length === 0 ? <Text c="dimmed" ta="center" p="xl">{t('common.empty')}</Text> : (
                <Table verticalSpacing="sm">
                  <Table.Tbody>
                    {cats.map((c) => (
                      <Table.Tr key={c.id} style={{ opacity: c.is_active ? 1 : 0.5 }}>
                        <Table.Td><Badge color={c.color} variant="light">{c.name}</Badge></Table.Td>
                        <Table.Td>{c.items} {t('pos.itemsCount')}</Table.Td>
                        <Table.Td ta="end">{canManage && <Button size="compact-sm" variant="subtle" leftSection={<IconEdit size={14} />} onClick={() => setEditCat(c)}>{t('common.edit')}</Button>}</Table.Td>
                      </Table.Tr>
                    ))}
                  </Table.Tbody>
                </Table>
              )}
            </Paper>
          </Stack>
        </Tabs.Panel>
      </Tabs>
      {editReg && <RegisterForm reg={editReg === 'new' ? undefined : editReg} onClose={() => setEditReg(null)} onSaved={() => { setEditReg(null); load(); }} />}
      {editCat && <CategoryForm cat={editCat === 'new' ? undefined : editCat} onClose={() => setEditCat(null)} onSaved={() => { setEditCat(null); load(); }} />}
    </Stack>
  );
}

function RegisterForm({ reg, onClose, onSaved }: { reg?: Register; onClose: () => void; onSaved: () => void }) {
  const { t } = useTranslation();
  const { company, currencies } = useCompany();
  const [methods, setMethods] = useState<PaymentMethod[]>([]);
  const [warehouses, setWarehouses] = useState<Warehouse[]>([]);
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [f, setF] = useState({
    name: reg?.name ?? '', currency: reg?.currency ?? company?.base_currency ?? 'USD', warehouse_id: reg?.warehouse_id ?? '',
    method_ids: reg?.method_ids ?? [], revenue_account_id: reg?.revenue_account_id ?? '', over_short_account_id: reg?.over_short_account_id ?? '',
    cashier_refunds: reg?.cashier_refunds ?? false, cashier_price_edit: reg?.cashier_price_edit ?? false, receipt_footer: reg?.receipt_footer ?? '',
    is_active: reg?.is_active ?? true, cashier_quick_item: reg?.cashier_quick_item ?? false, drawer_kick: reg?.drawer_kick ?? false,
    customer_display: reg?.customer_display ?? false,
  });
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (!company) return;
    api<PaymentMethod[]>(`/companies/${company.id}/payment-methods`).then((m) => {
      const active = m.filter((x) => x.is_active && !x.user_id);
      setMethods(active);
      if (!reg) setF((x) => ({ ...x, method_ids: active.filter((y) => y.kind === 'cash').map((y) => y.id) }));
    }).catch(() => {});
    api<Warehouse[]>(`/companies/${company.id}/warehouses`).then((w) => {
      setWarehouses(w.filter((x) => x.is_active));
      if (!reg && w[0]) setF((x) => ({ ...x, warehouse_id: x.warehouse_id || w[0].id }));
    }).catch(() => {});
    api<Account[]>(`/companies/${company.id}/accounts`).then((a) => setAccounts(a.filter((x) => x.is_postable && x.is_active))).catch(() => {});
  }, [company, reg]);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!company) return;
    setBusy(true);
    try {
      if (reg) await api(`/companies/${company.id}/pos/registers/${reg.id}`, { method: 'PATCH', body: f });
      else await api(`/companies/${company.id}/pos/registers`, { method: 'POST', body: { ...f, is_active: undefined } });
      notifySaved(t);
      onSaved();
    } catch (err) {
      notifyError(t, err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened onClose={onClose} title={reg ? reg.name : t('pos.newRegister')} size="lg">
      <form onSubmit={submit}>
        <Stack>
          <Group grow>
            <TextInput label={t('pos.name')} required value={f.name} data-autofocus placeholder={t('pos.namePlaceholder')} onChange={(e) => setF({ ...f, name: e.currentTarget.value })} />
            <Select label={t('pos.priceCurrency')} searchable allowDeselect={false} data={currencies.map((c) => c.code)} value={f.currency}
              onChange={(v) => v && setF({ ...f, currency: v })} maw={160} />
          </Group>
          <Select label={t('pos.warehouse')} allowDeselect={false} data={warehouses.map((w) => ({ value: w.id, label: w.name }))} value={f.warehouse_id || null}
            onChange={(v) => v && setF({ ...f, warehouse_id: v })} />
          <MultiSelect label={t('pos.boxes')} description={t('pos.boxesHint')} data={methods.map((m) => ({ value: m.id, label: `${m.name} (${m.currency})` }))}
            value={f.method_ids} onChange={(v) => setF({ ...f, method_ids: v })} />
          <Select label={t('pos.revenueAccount')} clearable searchable placeholder={t('pos.revenueDefault')}
            data={accounts.filter((a) => a.type === 'income').map((a) => ({ value: a.id, label: accountLabel(a) }))}
            value={f.revenue_account_id || null} onChange={(v) => setF({ ...f, revenue_account_id: v ?? '' })} />
          {reg && <Select label={t('pos.overShortAccount')} searchable allowDeselect={false}
            data={accounts.map((a) => ({ value: a.id, label: accountLabel(a) }))}
            value={f.over_short_account_id || null} onChange={(v) => v && setF({ ...f, over_short_account_id: v })} />}
          <Switch label={t('pos.cashierRefunds')} checked={f.cashier_refunds} onChange={(e) => setF({ ...f, cashier_refunds: e.currentTarget.checked })} />
          <Switch label={t('pos.cashierPriceEdit')} checked={f.cashier_price_edit} onChange={(e) => setF({ ...f, cashier_price_edit: e.currentTarget.checked })} />
          <Switch label={t('rt.pos.allowQuick')} checked={f.cashier_quick_item} onChange={(e) => setF({ ...f, cashier_quick_item: e.currentTarget.checked })} />
          <Switch label={t('rt.pos.drawerKick')} description={t('rt.pos.drawerKickHint')} checked={f.drawer_kick} onChange={(e) => setF({ ...f, drawer_kick: e.currentTarget.checked })} />
          <Textarea label={t('pos.receiptFooter')} autosize minRows={2} maxLength={500} value={f.receipt_footer}
            onChange={(e) => setF({ ...f, receipt_footer: e.currentTarget.value })} />
          {reg && <Switch label={t('common.active')} checked={f.is_active} onChange={(e) => setF({ ...f, is_active: e.currentTarget.checked })} />}
          {!reg && <Text size="xs" c="dimmed">{t('pos.walkInHint')}</Text>}
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
            <Button type="submit" loading={busy} disabled={!f.name.trim() || f.method_ids.length === 0}>{t('common.save')}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}

function CategoryForm({ cat, onClose, onSaved }: { cat?: Category; onClose: () => void; onSaved: () => void }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const [f, setF] = useState({
    name: cat?.name ?? '', ar: cat?.name_i18n?.ar ?? '', fr: cat?.name_i18n?.fr ?? '', color: cat?.color ?? 'teal', seq: String(cat?.seq ?? 0),
    is_active: cat?.is_active ?? true,
  });
  const [busy, setBusy] = useState(false);
  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!company) return;
    setBusy(true);
    const name_i18n: Record<string, string> = {};
    if (f.ar.trim()) name_i18n.ar = f.ar.trim();
    if (f.fr.trim()) name_i18n.fr = f.fr.trim();
    const body = { name: f.name, name_i18n, color: f.color, seq: Number(f.seq) || 0, is_active: f.is_active };
    try {
      if (cat) await api(`/companies/${company.id}/pos/categories/${cat.id}`, { method: 'PATCH', body });
      else await api(`/companies/${company.id}/pos/categories`, { method: 'POST', body });
      notifySaved(t);
      onSaved();
    } catch (err) {
      notifyError(t, err);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal opened onClose={onClose} title={cat ? cat.name : t('pos.newCategory')}>
      <form onSubmit={submit}>
        <Stack>
          <TextInput label={t('pos.name')} required value={f.name} data-autofocus onChange={(e) => setF({ ...f, name: e.currentTarget.value })} />
          <Group grow>
            <TextInput label="العربية" dir="rtl" value={f.ar} onChange={(e) => setF({ ...f, ar: e.currentTarget.value })} />
            <TextInput label="Français" value={f.fr} onChange={(e) => setF({ ...f, fr: e.currentTarget.value })} />
          </Group>
          <Group grow>
            <Select label={t('pos.color')} allowDeselect={false} data={colors} value={f.color} onChange={(v) => v && setF({ ...f, color: v })}
              renderOption={({ option }) => <Badge color={option.value} variant="filled">{option.value}</Badge>} />
            <TextInput label={t('pos.order')} dir="ltr" value={f.seq} onChange={(e) => setF({ ...f, seq: e.currentTarget.value.replace(/[^\d-]/g, '') })} maw={120} />
          </Group>
          {cat && <Switch label={t('common.active')} checked={f.is_active} onChange={(e) => setF({ ...f, is_active: e.currentTarget.checked })} />}
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
            <Button type="submit" loading={busy}>{t('common.save')}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}

// ---------- sessions (Z reports) ----------

interface SessionRow {
  id: string; number: number; status: 'open' | 'closed'; register_id: string; register_name: string; currency: string; opened_at: string;
  opened_by: string | null; closed_at: string | null; closed_by: string | null; sales: number; net: string;
  counted: { difference?: string; currency: string; name: string }[];
}

export function PosSessions() {
  const { t } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [rows, setRows] = useState<SessionRow[] | null>(null);
  const [status, setStatus] = useState('');
  const [open, setOpen] = useState<string | null>(null);
  useEffect(() => {
    if (!company) return;
    setRows(null);
    api<SessionRow[]>(`/companies/${company.id}/pos/sessions?status=${status}`).then(setRows).catch((e) => notifyError(t, e));
  }, [company, status, t]);
  if (!company) return null;
  return (
    <Stack gap="lg" maw={1100}>
      <PageHeader title={t('pos.sessions')} subtitle={company.name_en}>
        <ExportMenu disabled={!rows} table={() => rows && ({
          title: t('pos.sessions'),
          columns: [{ label: '#' }, { label: t('pos.register') }, { label: t('pos.openedAt') }, { label: t('pos.closedAt') }, { label: t('pos.cashier') },
            { label: t('pos.sales'), numeric: true }, { label: t('pos.net'), numeric: true }, { label: t('pur.currency') }],
          rows: rows.map((r) => [r.number, r.register_name, formatDateTime(r.opened_at), r.closed_at ? formatDateTime(r.closed_at) : '', r.opened_by ?? '',
            r.sales, r.net, r.currency]),
        })} />
      </PageHeader>
      <SegmentedControl style={{ alignSelf: 'flex-start' }} value={status} onChange={setStatus}
        data={[{ value: '', label: t('common.all') }, { value: 'open', label: t('pos.status.open') }, { value: 'closed', label: t('pos.status.closed') }]} />
      <Paper withBorder>
        {rows === null ? <Center p="xl"><Loader size="sm" /></Center> : rows.length === 0 ? <Text c="dimmed" ta="center" p="xl">{t('common.empty')}</Text> : (
          <Table.ScrollContainer minWidth={760}>
            <Table highlightOnHover verticalSpacing="sm">
              <Table.Thead><Table.Tr>
                <Table.Th>#</Table.Th><Table.Th>{t('pos.register')}</Table.Th><Table.Th>{t('pos.openedAt')}</Table.Th><Table.Th>{t('pos.cashier')}</Table.Th>
                <Table.Th>{t('pos.sales')}</Table.Th><Table.Th className="num">{t('pos.net')}</Table.Th><Table.Th>{t('pos.difference')}</Table.Th>
              </Table.Tr></Table.Thead>
              <Table.Tbody>
                {rows.map((r) => {
                  const diffs = (r.counted ?? []).filter((c) => c.difference && Number(c.difference) !== 0);
                  return (
                    <Table.Tr key={r.id} style={{ cursor: 'pointer' }} onClick={() => setOpen(r.id)}>
                      <Table.Td ff="monospace">{r.number}</Table.Td>
                      <Table.Td>{r.register_name} {r.status === 'open' && <Badge size="xs" color="teal" variant="light">{t('pos.status.open')}</Badge>}</Table.Td>
                      <Table.Td>{formatDateTime(r.opened_at)}</Table.Td>
                      <Table.Td>{r.opened_by}{r.closed_by && r.closed_by !== r.opened_by ? ` / ${r.closed_by}` : ''}</Table.Td>
                      <Table.Td>{r.sales}</Table.Td>
                      <Table.Td className="num" ff="monospace" dir="ltr">{formatAmount(r.net, decimalsOf(r.currency))} {r.currency}</Table.Td>
                      <Table.Td>{diffs.length === 0 ? (r.status === 'closed' ? <Badge color="teal" variant="light">0</Badge> : '—') : diffs.map((c, i) => (
                        <Badge key={i} color={Number(c.difference) < 0 ? 'red' : 'teal'} variant="light" mr={4} style={{ textTransform: 'none' }}>
                          <span dir="ltr">{formatAmount(c.difference, decimalsOf(c.currency))} {c.currency}</span>
                        </Badge>
                      ))}</Table.Td>
                    </Table.Tr>
                  );
                })}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </Paper>
      {open && (
        <Modal opened onClose={() => setOpen(null)} size="lg" title={t('pos.zReport')}>
          <Stack>
            <SessionReport id={open} />
            <Group justify="flex-end" className="no-print">
              <Button variant="default" onClick={() => setOpen(null)}>{t('common.close')}</Button>
              <Button leftSection={<IconPrinter size={16} />} onClick={() => void printPage()}>{t('invoices.print')}</Button>
            </Group>
          </Stack>
        </Modal>
      )}
    </Stack>
  );
}

// ---------- report ----------

interface ReportRow {
  key: string; currency: string; sales?: number; refunds?: number; sales_total?: string; refunds_total?: string; tax?: string; net: string;
  sold?: string; returned?: string; quantity?: string; received?: string; paid_out?: string;
}

export function PosReport() {
  const { t } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [from, setFrom] = useState(today().slice(0, 8) + '01');
  const [to, setTo] = useState(today());
  const [group, setGroup] = useState('day');
  const [d, setD] = useState<{ rows: ReportRow[]; totals: ReportRow[] } | null>(null);
  useEffect(() => {
    if (!company) return;
    setD(null);
    api<typeof d>(`/companies/${company.id}/reports/pos?from=${from}&to=${to}&group=${group}`).then(setD).catch((e) => notifyError(t, e));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [company, from, to, group]);
  if (!company) return null;
  const isProduct = group === 'product';
  const isMethod = group === 'method';
  const cols = isProduct
    ? [t('pos.sold'), t('pos.returned'), t('pos.qty'), t('pos.net')]
    : isMethod ? [t('pos.cashIn'), t('pos.cashOut'), t('pos.net')]
      : [t('pos.sales'), t('pos.salesTotal'), t('pos.refunds'), t('pos.tax'), t('pos.net')];
  const cells = (r: ReportRow): (string | number)[] => {
    const dd = decimalsOf(r.currency);
    if (isProduct) return [Number(r.sold ?? 0), Number(r.returned ?? 0), Number(r.quantity ?? 0), formatAmount(r.net, dd)];
    if (isMethod) return [formatAmount(r.received, dd), formatAmount(r.paid_out, dd), formatAmount(r.net, dd)];
    return [r.sales ?? 0, formatAmount(r.sales_total, dd), `${r.refunds ?? 0} · ${formatAmount(r.refunds_total, dd)}`, formatAmount(r.tax, dd), formatAmount(r.net, dd)];
  };
  const keyLabel = (k: string) => (group === 'day' ? k.slice(0, 10) : k);
  return (
    <Stack gap="lg" maw={1100} className="print-area">
      <PageHeader title={t('pos.report')} subtitle={`${company.name_en} · ${from} – ${to}`}>
        <ExportMenu disabled={!d} table={() => d && ({
          title: `${t('pos.report')} · ${t(`pos.groups.${group}`)}`, subtitle: `${from} – ${to}`,
          columns: [{ label: t(`pos.groups.${group}`) }, { label: t('pur.currency') }, ...cols.map((c) => ({ label: c, numeric: true }))],
          rows: d.rows.map((r) => [keyLabel(r.key), r.currency, ...cells(r)]),
          footer: isProduct || isMethod ? [] : d.totals.map((r) => [t('invoices.total'), r.currency, ...cells(r)]),
        })} />
      </PageHeader>
      <Group className="no-print" align="flex-end">
        <TextInput type="date" label={t('reports.from')} value={from} onChange={(e) => setFrom(e.currentTarget.value)} />
        <TextInput type="date" label={t('reports.to')} value={to} onChange={(e) => setTo(e.currentTarget.value)} />
        <Select label={t('pos.groupBy')} allowDeselect={false} value={group} onChange={(v) => v && setGroup(v)}
          data={['day', 'session', 'cashier', 'register', 'product', 'method'].map((g) => ({ value: g, label: t(`pos.groups.${g}`) }))} />
      </Group>
      {!d ? <Center p="xl"><Loader size="sm" /></Center> : d.rows.length === 0 ? <Alert color="gray" variant="light">{t('pos.noSales')}</Alert> : (
        <Paper withBorder>
          <Table.ScrollContainer minWidth={700}>
            <Table verticalSpacing="sm" striped>
              <Table.Thead><Table.Tr>
                <Table.Th>{t(`pos.groups.${group}`)}</Table.Th><Table.Th>{t('pur.currency')}</Table.Th>
                {cols.map((c) => <Table.Th key={c} className="num">{c}</Table.Th>)}
              </Table.Tr></Table.Thead>
              <Table.Tbody>
                {d.rows.map((r, i) => (
                  <Table.Tr key={i}>
                    <Table.Td>{keyLabel(r.key)}</Table.Td><Table.Td>{r.currency}</Table.Td>
                    {cells(r).map((c, j) => <Table.Td key={j} className="num" ff="monospace" dir="ltr">{c}</Table.Td>)}
                  </Table.Tr>
                ))}
              </Table.Tbody>
              {!isProduct && !isMethod && (
                <Table.Tfoot>
                  {d.totals.map((r, i) => (
                    <Table.Tr key={i}>
                      <Table.Th>{t('invoices.total')}</Table.Th><Table.Th>{r.currency}</Table.Th>
                      {cells(r).map((c, j) => <Table.Th key={j} className="num" ff="monospace" dir="ltr">{c}</Table.Th>)}
                    </Table.Tr>
                  ))}
                </Table.Tfoot>
              )}
            </Table>
          </Table.ScrollContainer>
        </Paper>
      )}
    </Stack>
  );
}
