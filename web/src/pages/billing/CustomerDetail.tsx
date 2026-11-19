import { useCallback, useEffect, useState } from 'react';
import {
  Anchor, Badge, Button, Center, Grid, Group, Loader, Modal, Paper, Select, Stack, Table, Tabs, Text, TextInput, Title,
} from '@mantine/core';
import { IconArrowBack, IconCash, IconEdit, IconFileDescription, IconFileInvoice, IconFileText, IconPlus } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { can, api, type Me } from '../../api';
import { useCompany } from '../../company';
import { formatAmount, formatDate, notifyError, notifySaved, today } from '../../util';
import { invoiceColor, type Area, type Customer, type Invoice, type Payment, type Plan, type Subscription } from '../../billing';
import { CustomerForm } from './Customers';
import { InvoiceView, MoneyList, NewInvoice, PaymentView, ReceivePayment } from './docs';
import { WhatsAppButton } from './Messages';
import PortalShare from '../../components/PortalShare';
import { OrderEditor, OrderView, type Order } from '../sales/Sales';
import { orderColor } from '../sales/common';
import { Activities } from '../crm/Crm';
import Attachments from '../../components/Attachments';
import { CustomFieldsBox } from '../fin/Finish';

interface Detail { customer: Customer; subscriptions: Subscription[]; invoices: Invoice[]; payments: Payment[] }

export default function CustomerDetail({ me, id }: { me: Me; id: string }) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [d, setD] = useState<Detail | null>(null);
  const [areas, setAreas] = useState<Area[]>([]);
  const [modal, setModal] = useState<null | 'edit' | 'pay' | 'invoice' | 'sub' | 'refund' | 'quote'>(null);
  const [orders, setOrders] = useState<Order[] | null>(null);
  const [viewOrder, setViewOrder] = useState<string | null>(null);
  const [editSub, setEditSub] = useState<Subscription | null>(null);
  const [viewInvoice, setViewInvoice] = useState<string | null>(null);
  const [viewPayment, setViewPayment] = useState<string | null>(null);
  const canEdit = can(me, 'customers.edit');
  const canManage = can(me, 'billing.create');

  const load = useCallback(() => {
    if (!company) return;
    api<Detail>(`/companies/${company.id}/customers/${id}`).then(setD).catch((e) => notifyError(t, e));
    if (can(me, 'sales.view')) api<{ rows: Order[] }>(`/companies/${company.id}/sales/orders?kind=&customer_id=${id}`).then((r) => setOrders(r.rows)).catch(() => setOrders([]));
  }, [company, id, t, me]);
  useEffect(load, [load]);
  useEffect(() => {
    if (company) api<Area[]>(`/companies/${company.id}/areas`).then(setAreas).catch(() => {});
  }, [company]);

  if (!company) return null;
  if (!d) return <Center p="xl"><Loader /></Center>;
  const c = d.customer;

  return (
    <Stack gap="lg" maw={1200}>
      <Anchor href="#/customers" size="sm"><Group gap={4}><IconArrowBack size={14} />{t('customers.back')}</Group></Anchor>
      <Group justify="space-between" align="flex-start" wrap="wrap">
        <div>
          <Group gap="xs">
            <Title order={2}>{c.name}</Title>
            <Badge variant="light" color={c.status === 'active' ? 'teal' : 'orange'}>{t(`customers.statuses.${c.status}`)}</Badge>
          </Group>
          <Text c="dimmed" size="sm">
            #{c.code}{c.phone && <> · <span dir="ltr">{c.phone}</span></>}{c.area_name && ` · ${c.area_name}`}{c.address && ` · ${c.address}`}
          </Text>
        </div>
        {canEdit && (
          <Group gap="xs">
            <Button variant="default" leftSection={<IconEdit size={16} />} onClick={() => setModal('edit')}>{t('common.edit')}</Button>
            <WhatsAppButton customerId={c.id} />
            <PortalShare customerId={c.id} phone={c.phone} canRegenerate={me.role !== 'collector' && can(me, 'customers.create')} />
            <Button variant="default" leftSection={<IconFileText size={16} />} component="a" href={`#/statement/${c.id}`}>{t('stmt.title')}</Button>
            <Button variant="default" leftSection={<IconFileDescription size={16} />} onClick={() => setModal('quote')}>{t('sales.newQuote')}</Button>
            {canManage && <Button variant="default" leftSection={<IconFileInvoice size={16} />} onClick={() => setModal('invoice')}>{t('customers.newInvoice')}</Button>}
            <Button leftSection={<IconCash size={16} />} onClick={() => setModal('pay')}>{t('customers.receivePayment')}</Button>
          </Group>
        )}
      </Group>

      <Grid>
        <Grid.Col span={{ base: 12, sm: 6 }}>
          <Paper withBorder p="md" h="100%">
            <Text size="xs" c="dimmed" tt="uppercase" fw={600}>{t('customers.due')}</Text>
            {c.due.length === 0 ? <Text fw={600} c="teal" mt={4}>{t('customers.nothingDue')}</Text> : (
              <Stack gap={2} mt={4}>
                {c.due.map((m) => <Text key={m.currency} size="1.6rem" fw={700} c="red.7" dir="ltr">{formatAmount(m.due, decimalsOf(m.currency))} {m.currency}</Text>)}
              </Stack>
            )}
          </Paper>
        </Grid.Col>
        <Grid.Col span={{ base: 12, sm: 6 }}>
          <Paper withBorder p="md" h="100%">
            <Text size="xs" c="dimmed" tt="uppercase" fw={600}>{t('customers.credit')}</Text>
            <div style={{ marginTop: 6 }}><MoneyList items={c.credit} kind="credit" /></div>
            {canManage && c.credit.length > 0 && (
              <Button size="xs" variant="light" color="orange" mt="sm" onClick={() => setModal('refund')}>{t('gap.refundNew')}</Button>
            )}
          </Paper>
        </Grid.Col>
      </Grid>

      <Tabs defaultValue="subs" keepMounted={false}>
        <Tabs.List>
          <Tabs.Tab value="subs">{t('customers.subscriptions')} ({d.subscriptions.length})</Tabs.Tab>
          <Tabs.Tab value="inv">{t('bill.invoices')} ({d.invoices.length})</Tabs.Tab>
          <Tabs.Tab value="pay">{t('bill.payments')} ({d.payments.length})</Tabs.Tab>
          <Tabs.Tab value="sales">{t('sales.quotes')} ({orders?.length ?? 0})</Tabs.Tab>
          <Tabs.Tab value="crm">{t('crm.activitiesNotes')}</Tabs.Tab>
        </Tabs.List>

        <Tabs.Panel value="sales" pt="md">
          <Paper withBorder>
            {!orders || orders.length === 0 ? <Text c="dimmed" ta="center" p="lg">{t('common.empty')}</Text> : (
              <Table verticalSpacing="xs" highlightOnHover>
                <Table.Tbody>
                  {orders.map((o) => (
                    <Table.Tr key={o.id} style={{ cursor: 'pointer' }} onClick={() => setViewOrder(o.id)}>
                      <Table.Td ff="monospace">{o.order_number && o.status !== 'draft' && o.status !== 'sent' ? `SO-${o.order_number}` : `Q-${o.number}`}</Table.Td>
                      <Table.Td>{formatDate(o.order_date ?? o.quote_date)}</Table.Td>
                      <Table.Td><Badge variant="light" color={orderColor[o.status]}>{t(`sales.st.${o.status}`)}</Badge></Table.Td>
                      <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(o.total, decimalsOf(o.currency))} {o.currency}</Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            )}
          </Paper>
        </Tabs.Panel>
        <Tabs.Panel value="crm" pt="md">
          <Activities customerId={c.id} canEdit={canEdit} />
        </Tabs.Panel>

        <Tabs.Panel value="subs" pt="md">
          <Paper withBorder>
            {canEdit && (
              <Group justify="flex-end" p="sm">
                <Button size="xs" leftSection={<IconPlus size={14} />} onClick={() => { setEditSub(null); setModal('sub'); }}>{t('customers.addSubscription')}</Button>
              </Group>
            )}
            {d.subscriptions.length === 0 ? <Text c="dimmed" ta="center" p="lg">{t('common.empty')}</Text> : (
              <Table.ScrollContainer minWidth={700}>
                <Table verticalSpacing="sm" key={i18n.language}>
                  <Table.Thead>
                    <Table.Tr>
                      <Table.Th>{t('customers.plan')}</Table.Th>
                      <Table.Th>{t('customers.meter')}</Table.Th>
                      <Table.Th>{t('plans.baseFee')}</Table.Th>
                      <Table.Th>{t('customers.lastReading')}</Table.Th>
                      <Table.Th>{t('customers.start')}</Table.Th>
                      <Table.Th>{t('customers.status')}</Table.Th>
                      <Table.Th />
                    </Table.Tr>
                  </Table.Thead>
                  <Table.Tbody>
                    {d.subscriptions.map((s) => (
                      <Table.Tr key={s.id} style={{ opacity: s.status === 'ended' ? 0.5 : 1 }}>
                        <Table.Td fw={500}>{s.plan_name}<Text size="xs" c="dimmed">{t(`plans.${s.billing_type}`)}</Text></Table.Td>
                        <Table.Td ff="monospace">{s.meter_no || '—'}{s.box_code && <Text size="xs" c="dimmed">{t('net.box')}: {s.box_code}</Text>}</Table.Td>
                        <Table.Td dir="ltr" style={{ textAlign: 'start' }}>
                          {formatAmount(s.fee_override ?? s.base_fee, decimalsOf(s.currency))} {s.currency}
                          {s.fee_override && <Badge ms={4} size="xs" variant="outline">*</Badge>}
                          {s.billing_type === 'metered' && <Text size="xs" c="dimmed">+ {s.unit_price.replace(/\.?0+$/, '')} / {s.unit_label}</Text>}
                        </Table.Td>
                        <Table.Td ff="monospace">{s.billing_type === 'metered' ? `${s.last_reading ?? s.initial_reading}${s.last_period ? ` (${s.last_period})` : ''}` : '—'}</Table.Td>
                        <Table.Td>{formatDate(s.start_date)}</Table.Td>
                        <Table.Td>
                          <Badge variant="light" color={s.status === 'active' ? 'teal' : s.status === 'suspended' ? 'orange' : 'gray'}>{t(`customers.subStatuses.${s.status}`)}</Badge>
                          {s.router_id && <NetworkState s={s} />}
                        </Table.Td>
                        <Table.Td>
                          <Group gap={4} wrap="nowrap">
                            {canEdit && <Button size="compact-xs" variant="subtle" onClick={() => { setEditSub(s); setModal('sub'); }}>{t('common.edit')}</Button>}
                            {canManage && s.router_id && s.network_username && s.status !== 'ended' && (
                              <NetworkButton sub={s} onDone={load} />
                            )}
                          </Group>
                        </Table.Td>
                      </Table.Tr>
                    ))}
                  </Table.Tbody>
                </Table>
              </Table.ScrollContainer>
            )}
          </Paper>
        </Tabs.Panel>

        <Tabs.Panel value="inv" pt="md">
          <Paper withBorder>
            {d.invoices.length === 0 ? <Text c="dimmed" ta="center" p="lg">{t('common.empty')}</Text> : (
              <Table.ScrollContainer minWidth={600}>
                <Table verticalSpacing="xs" highlightOnHover key={i18n.language}>
                  <Table.Thead>
                    <Table.Tr>
                      <Table.Th>{t('invoices.number')}</Table.Th><Table.Th>{t('invoices.date')}</Table.Th><Table.Th>{t('invoices.period')}</Table.Th>
                      <Table.Th style={{ textAlign: 'end' }}>{t('invoices.total')}</Table.Th><Table.Th style={{ textAlign: 'end' }}>{t('invoices.balance')}</Table.Th>
                      <Table.Th>{t('invoices.status')}</Table.Th>
                    </Table.Tr>
                  </Table.Thead>
                  <Table.Tbody>
                    {d.invoices.map((iv) => (
                      <Table.Tr key={iv.id} style={{ cursor: 'pointer' }} onClick={() => setViewInvoice(iv.id)}>
                        <Table.Td ff="monospace">{iv.number}{iv.kind === 'refund' && <Badge ml={6} size="xs" color="orange" variant="light">{iv.cheque_id ? t('stmt.kinds.cheque_return') : t('gap.refund')}</Badge>}</Table.Td>
                        <Table.Td>{formatDate(iv.issue_date)}</Table.Td>
                        <Table.Td>{iv.period ?? '—'}</Table.Td>
                        <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(iv.total, decimalsOf(iv.currency))} {iv.currency}</Table.Td>
                        <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{iv.status === 'void' ? '—' : formatAmount(iv.balance, decimalsOf(iv.currency))}</Table.Td>
                        <Table.Td><Badge variant="light" color={invoiceColor[iv.status]}>{t(`invoices.statuses.${iv.status}`)}</Badge></Table.Td>
                      </Table.Tr>
                    ))}
                  </Table.Tbody>
                </Table>
              </Table.ScrollContainer>
            )}
          </Paper>
        </Tabs.Panel>

        <Tabs.Panel value="pay" pt="md">
          <Paper withBorder>
            {d.payments.length === 0 ? <Text c="dimmed" ta="center" p="lg">{t('common.empty')}</Text> : (
              <Table.ScrollContainer minWidth={600}>
                <Table verticalSpacing="xs" highlightOnHover key={i18n.language}>
                  <Table.Thead>
                    <Table.Tr>
                      <Table.Th>{t('payments.number')}</Table.Th><Table.Th>{t('payments.date')}</Table.Th>
                      <Table.Th style={{ textAlign: 'end' }}>{t('payments.amount')}</Table.Th><Table.Th>{t('payments.receivedBy')}</Table.Th><Table.Th />
                    </Table.Tr>
                  </Table.Thead>
                  <Table.Tbody>
                    {d.payments.map((p) => (
                      <Table.Tr key={p.id} style={{ cursor: 'pointer', opacity: p.status === 'void' ? 0.5 : 1 }} onClick={() => setViewPayment(p.id)}>
                        <Table.Td ff="monospace">{p.number}{p.kind === 'credit_note' && <Badge ml={6} size="xs" color="orange" variant="light">{t('gap.creditNote')}</Badge>}{p.kind === 'opening_credit' && <Badge ml={6} size="xs" color="gray" variant="light">{t('stmt.kinds.opening_credit')}</Badge>}</Table.Td>
                        <Table.Td>{formatDate(p.payment_date)}</Table.Td>
                        <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(p.amount, decimalsOf(p.currency))} {p.currency}</Table.Td>
                        <Table.Td c="dimmed">{p.received_by}</Table.Td>
                        <Table.Td>{p.status === 'void' && <Badge color="gray">{t('invoices.statuses.void')}</Badge>}</Table.Td>
                      </Table.Tr>
                    ))}
                  </Table.Tbody>
                </Table>
              </Table.ScrollContainer>
            )}
          </Paper>
        </Tabs.Panel>
      </Tabs>

      {modal === 'edit' && <CustomerForm areas={areas} customer={c} onClose={() => setModal(null)} onSaved={() => { setModal(null); load(); }} />}
      {modal === 'pay' && (
        <ReceivePayment customerId={c.id} customerName={c.name} due={c.due} onClose={() => setModal(null)}
          onDone={(pid) => { setModal(null); load(); setViewPayment(pid); }} />
      )}
      {modal === 'invoice' && (
        <NewInvoice customerId={c.id} customerName={c.name} onClose={() => setModal(null)}
          onDone={(iid) => { setModal(null); load(); setViewInvoice(iid); }} />
      )}
      {modal === 'refund' && <RefundForm customerId={c.id} credit={c.credit} onClose={() => setModal(null)} onDone={(iid) => { setModal(null); load(); setViewInvoice(iid); }} />}
      {modal === 'sub' && <SubscriptionForm customerId={c.id} sub={editSub} onClose={() => setModal(null)} onSaved={() => { setModal(null); load(); }} />}
      {viewInvoice && <InvoiceView id={viewInvoice} canVoid={canManage} onClose={() => setViewInvoice(null)} onChanged={load} />}
      {viewPayment && <PaymentView id={viewPayment} canVoid={canManage} onClose={() => setViewPayment(null)} onChanged={load} />}
      {modal === 'quote' && <OrderEditor customer={{ id: c.id, name: c.name, code: c.code }} onClose={() => setModal(null)} onDone={(oid) => { setModal(null); load(); setViewOrder(oid); }} />}
      {viewOrder && <OrderView id={viewOrder} me={me} onClose={() => setViewOrder(null)} onChanged={load} />}
      <Attachments type="customer" id={c.id} readOnly={!can(me, 'customers.edit')} />
          <CustomFieldsBox entity="customers" id={c.id} readOnly={!can(me, 'customers.edit')} />
    </Stack>
  );
}

// Internet connection state on the router (MikroTik) and the last router action.
function NetworkState({ s }: { s: Subscription }) {
  const { t } = useTranslation();
  const pending = s.last_action_status === 'queued' || s.last_action_status === 'sending';
  return (
    <Text size="xs" c="dimmed" mt={2}>
      {s.router_name} · <span dir="ltr">{s.network_username || '—'}</span>{' '}
      {pending ? <Badge size="xs" color="yellow" variant="light">{t('isp.pending')}</Badge>
        : s.last_action_status === 'failed' ? <Badge size="xs" color="red" variant="light" title={s.last_action_error ?? ''}>{t('isp.failed')}</Badge>
        : s.network_status && <Badge size="xs" color={s.network_status === 'enabled' ? 'teal' : 'red'} variant="dot">{t(`isp.net.${s.network_status}`)}</Badge>}
      {s.suspended_reason && <Text span size="xs"> · {t(`isp.reasons.${s.suspended_reason}`, { defaultValue: s.suspended_reason })}</Text>}
    </Text>
  );
}

function NetworkButton({ sub, onDone }: { sub: Subscription; onDone: () => void }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const [busy, setBusy] = useState(false);
  const disable = sub.status === 'active';
  async function go() {
    if (!company) return;
    setBusy(true);
    try {
      await api(`/companies/${company.id}/subscriptions/${sub.id}/network`, { method: 'POST', body: { action: disable ? 'disable' : 'enable' } });
      notifySaved(t);
      onDone();
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Button size="compact-xs" variant="light" color={disable ? 'red' : 'teal'} loading={busy} onClick={go}>
      {disable ? t('isp.suspend') : t('isp.reconnect')}
    </Button>
  );
}

interface RouterLite { id: string; name: string; enabled: boolean }

function SubscriptionForm({ customerId, sub, onClose, onSaved }: { customerId: string; sub: Subscription | null; onClose: () => void; onSaved: () => void }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const [plans, setPlans] = useState<Plan[]>([]);
  const [boxes, setBoxes] = useState<{ id: string; code: string; station_name: string; is_active: boolean }[]>([]);
  const [routers, setRouters] = useState<RouterLite[]>([]);
  const [f, setF] = useState({
    plan_id: sub?.plan_id ?? '', meter_no: sub?.meter_no ?? '', start_date: sub?.start_date ?? today(),
    fee_override: sub?.fee_override ? String(Number(sub.fee_override)) : '', initial_reading: sub ? String(Number(sub.initial_reading)) : '0',
    status: sub?.status ?? 'active', notes: sub?.notes ?? '', box_id: sub?.box_id ?? '',
    router_id: sub?.router_id ?? '', network_username: sub?.network_username ?? '', service_type: sub?.service_type ?? 'pppoe',
  });
  useEffect(() => {
    if (company) api<RouterLite[]>(`/companies/${company.id}/routers`).then(setRouters).catch(() => setRouters([]));
  }, [company]);
  const [busy, setBusy] = useState(false);
  const set = (k: keyof typeof f) => (v: string) => setF((x) => ({ ...x, [k]: v }));
  const plan = plans.find((p) => p.id === f.plan_id);

  useEffect(() => {
    if (company) api<Plan[]>(`/companies/${company.id}/plans`).then((ps) => setPlans(ps.filter((p) => p.is_active || p.id === sub?.plan_id))).catch(() => {});
    if (company) api<{ id: string; code: string; station_name: string; is_active: boolean }[]>(`/companies/${company.id}/boxes`).then(setBoxes).catch(() => {});
  }, [company, sub]);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!company) return;
    setBusy(true);
    try {
      if (sub) await api(`/companies/${company.id}/subscriptions/${sub.id}`, { method: 'PATCH', body: f });
      else await api(`/companies/${company.id}/customers/${customerId}/subscriptions`, { method: 'POST', body: { ...f, status: undefined } });
      notifySaved(t);
      onSaved();
    } catch (err) {
      notifyError(t, err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened onClose={onClose} title={sub ? sub.plan_name : t('customers.addSubscription')}>
      <form onSubmit={submit}>
        <Stack>
          <Select label={t('customers.plan')} required value={f.plan_id || null} onChange={(v) => set('plan_id')(v ?? '')}
            data={plans.map((p) => ({ value: p.id, label: `${p.name} · ${Number(p.base_fee)} ${p.currency}${p.billing_type === 'metered' ? ` + ${Number(p.unit_price)}/${p.unit_label}` : ''}` }))} />
          <Group grow>
            <TextInput label={t('customers.meter')} dir="ltr" value={f.meter_no} onChange={(e) => set('meter_no')(e.currentTarget.value)} />
            {boxes.length > 0 && (
              <Select label={t('net.box')} clearable searchable value={f.box_id || null} onChange={(v) => set('box_id')(v ?? '')}
                data={boxes.filter((b) => b.is_active || b.id === f.box_id).map((b) => ({ value: b.id, label: `${b.code} · ${b.station_name}` }))} />
            )}
            <TextInput type="date" label={t('customers.start')} value={f.start_date} onChange={(e) => set('start_date')(e.currentTarget.value)} />
          </Group>
          <Group grow>
            <TextInput label={`${t('customers.feeOverride')} (${t('common.optional')})`} dir="ltr" value={f.fee_override}
              onChange={(e) => set('fee_override')(e.currentTarget.value.replace(/[^\d.]/g, ''))} rightSection={<Text size="xs" c="dimmed">{plan?.currency}</Text>} />
            {plan?.billing_type === 'metered' && (
              <TextInput label={t('customers.initialReading')} dir="ltr" value={f.initial_reading}
                onChange={(e) => set('initial_reading')(e.currentTarget.value.replace(/[^\d.]/g, ''))} />
            )}
          </Group>
          {(routers.length > 0 || f.router_id) && (
            <Group grow align="flex-start">
              <Select label={t('isp.router')} clearable value={f.router_id || null} onChange={(v) => set('router_id')(v ?? '')}
                data={routers.map((r) => ({ value: r.id, label: r.name }))} />
              <TextInput label={t('isp.username')} dir="ltr" value={f.network_username} disabled={!f.router_id}
                onChange={(e) => set('network_username')(e.currentTarget.value)} />
              <Select label={t('isp.service')} value={f.service_type} allowDeselect={false} disabled={!f.router_id}
                data={[{ value: 'pppoe', label: 'PPPoE' }, { value: 'hotspot', label: 'Hotspot' }]} onChange={(v) => v && set('service_type')(v)} />
            </Group>
          )}
          {sub && (
            <Select label={t('customers.status')} value={f.status} allowDeselect={false} onChange={(v) => v && set('status')(v)}
              data={['active', 'suspended', 'ended'].map((s) => ({ value: s, label: t(`customers.subStatuses.${s}`) }))} />
          )}
          <TextInput label={t('customers.notes')} value={f.notes} onChange={(e) => set('notes')(e.currentTarget.value)} />
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
            <Button type="submit" loading={busy} disabled={!f.plan_id}>{t('common.save')}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}

function RefundForm({ customerId, credit, onClose, onDone }: { customerId: string; credit: { currency: string; credit?: string }[]; onClose: () => void; onDone: (id: string) => void }) {
  const { t } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [methods, setMethods] = useState<{ id: string; name: string; currency: string; is_active: boolean }[]>([]);
  const [f, setF] = useState({ currency: credit[0]?.currency ?? '', amount: credit[0]?.credit ?? '', method_id: '', date: today(), notes: '' });
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (company) api<typeof methods>(`/companies/${company.id}/payment-methods`).then((m) => {
      const a = m.filter((x) => x.is_active); setMethods(a);
      setF((x) => ({ ...x, method_id: (a.find((y) => y.currency === x.currency) ?? a[0])?.id ?? '' }));
    }).catch(() => {});
  }, [company]);
  const max = credit.find((x) => x.currency === f.currency)?.credit ?? '0';
  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!company) return;
    setBusy(true);
    try {
      const r = await api<{ invoice: { id: string } }>(`/companies/${company.id}/customers/${customerId}/refunds`, { method: 'POST', body: f });
      notifySaved(t);
      onDone(r.invoice.id);
    } catch (err) {
      notifyError(t, err);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal opened onClose={onClose} title={t('gap.refundNew')}>
      <form onSubmit={submit}>
        <Stack>
          <Text size="sm" c="dimmed">{t('gap.refundHint')}</Text>
          <Group grow>
            <Select label={t('gap.currency')} data={credit.map((x) => x.currency)} value={f.currency} allowDeselect={false}
              onChange={(v) => setF({ ...f, currency: v ?? f.currency, amount: credit.find((x) => x.currency === v)?.credit ?? '' })} />
            <TextInput label={t('payments.amount')} dir="ltr" required value={f.amount}
              description={`≤ ${formatAmount(max, decimalsOf(f.currency))} ${f.currency}`}
              onChange={(e) => setF({ ...f, amount: e.currentTarget.value.replace(/[^\d.]/g, '') })} />
          </Group>
          <Select label={t('gap.paidFrom')} data={methods.map((m) => ({ value: m.id, label: `${m.name} (${m.currency})` }))} value={f.method_id || null}
            allowDeselect={false} onChange={(v) => setF({ ...f, method_id: v ?? '' })} />
          <TextInput type="date" label={t('payments.date')} value={f.date} onChange={(e) => setF({ ...f, date: e.currentTarget.value })} />
          <TextInput label={t('pur.notes')} value={f.notes} onChange={(e) => setF({ ...f, notes: e.currentTarget.value })} />
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
            <Button type="submit" color="orange" loading={busy} disabled={!Number(f.amount) || !f.method_id}>{t('gap.refund')}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}