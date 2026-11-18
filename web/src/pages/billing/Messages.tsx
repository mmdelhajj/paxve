import { useCallback, useEffect, useState } from 'react';
import {
  Alert, Badge, Button, Center, Group, Loader, Menu, Modal, NumberInput, Paper, PasswordInput, SegmentedControl, Select,
  SimpleGrid, Stack, Switch, Table, Tabs, Text, Textarea, TextInput, Title,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { IconBrandWhatsapp, IconPlus, IconRefresh, IconSend } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { api, type Me } from '../../api';
import { useCompany } from '../../company';
import { formatDateTime, notifyError, notifySaved } from '../../util';
import { languages } from '../../i18n';
import PageHeader from '../../components/PageHeader';
import type { Area } from '../../billing';

interface Msg {
  id: string; customer_id: string | null; customer_name: string | null; phone: string; channel: string; event: string; body: string;
  status: 'queued' | 'sending' | 'sent' | 'failed'; error: string; attempts: number; created_at: string; sent_at: string | null;
  provider_name: string | null; created_by: string | null;
}
interface Counts { queued: number; sending: number; sent: number; failed: number }
interface Provider { id: string; kind: string; name: string; config: Record<string, string>; is_default: boolean; is_active: boolean }
interface Template { id: string; event: string; locale: string; name: string; body: string; wa_template: string; is_active: boolean }
interface SendResult { queued: number; skipped: { customer: string; reason: string }[]; preview: { customer: string; phone: string; text: string }[]; dry_run: boolean; has_provider: boolean }

const statusColor: Record<string, string> = { queued: 'gray', sending: 'blue', sent: 'teal', failed: 'red' };
const events = ['reminder', 'invoice_issued', 'payment_received', 'custom'];

const providerFields: Record<string, { key: string; secret?: boolean }[]> = {
  whatsapp_cloud: [{ key: 'phone_number_id' }, { key: 'token', secret: true }, { key: 'api_version' }],
  twilio_sms: [{ key: 'account_sid' }, { key: 'auth_token', secret: true }, { key: 'from' }],
  generic_http: [{ key: 'url' }, { key: 'bearer', secret: true }],
  log: [],
};

export default function Messages({ me }: { me: Me }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const canManage = me.role === 'admin' || me.role === 'accountant';
  if (!company) return null;
  return (
    <Stack gap="lg" maw={1200}>
      <PageHeader title={t('msg.title')} subtitle={company.name_en} />
      <Tabs defaultValue="outbox" keepMounted={false}>
        <Tabs.List>
          <Tabs.Tab value="outbox">{t('msg.outbox')}</Tabs.Tab>
          {me.role !== 'read_only' && <Tabs.Tab value="send">{t('msg.sendReminders')}</Tabs.Tab>}
          {canManage && <Tabs.Tab value="settings">{t('msg.settings')}</Tabs.Tab>}
        </Tabs.List>
        <Tabs.Panel value="outbox" pt="md"><Outbox canRetry={canManage} /></Tabs.Panel>
        <Tabs.Panel value="send" pt="md"><SendWizard /></Tabs.Panel>
        <Tabs.Panel value="settings" pt="md"><Settings /></Tabs.Panel>
      </Tabs>
    </Stack>
  );
}

function Outbox({ canRetry }: { canRetry: boolean }) {
  const { t, i18n } = useTranslation();
  const { company } = useCompany();
  const [status, setStatus] = useState('');
  const [data, setData] = useState<{ rows: Msg[]; counts: Counts } | null>(null);

  const load = useCallback(() => {
    if (!company) return;
    api<{ rows: Msg[]; counts: Counts }>(`/companies/${company.id}/messages?status=${status}`).then(setData).catch((e) => notifyError(t, e));
  }, [company, status, t]);
  useEffect(() => {
    load();
    const id = window.setInterval(load, 5000);
    return () => window.clearInterval(id);
  }, [load]);

  async function retry(id: string) {
    try {
      await api(`/companies/${company!.id}/messages/${id}/retry`, { method: 'POST', body: {} });
      load();
    } catch (e) {
      notifyError(t, e);
    }
  }

  return (
    <Stack>
      {data && (
        <SimpleGrid cols={{ base: 2, sm: 4 }}>
          {(['queued', 'sending', 'sent', 'failed'] as const).map((k) => (
            <Paper withBorder p="sm" key={k}>
              <Text size="xs" c="dimmed">{t(`msg.status.${k}`)}</Text>
              <Text fw={700} size="xl" c={k === 'failed' && data.counts[k] > 0 ? 'red.7' : undefined}>{data.counts[k]}</Text>
            </Paper>
          ))}
        </SimpleGrid>
      )}
      <SegmentedControl value={status} onChange={setStatus} style={{ alignSelf: 'flex-start' }} data={[
        { value: '', label: t('common.all') }, ...(['queued', 'sent', 'failed'].map((s) => ({ value: s, label: t(`msg.status.${s}`) }))),
      ]} />
      <Paper withBorder>
        {!data ? <Center p="xl"><Loader size="sm" /></Center> : data.rows.length === 0 ? (
          <Text c="dimmed" ta="center" p="xl">{t('common.empty')}</Text>
        ) : (
          <Table.ScrollContainer minWidth={800}>
            <Table verticalSpacing="xs" key={i18n.language}>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>{t('msg.when')}</Table.Th><Table.Th>{t('invoices.customer')}</Table.Th>
                  <Table.Th>{t('msg.text')}</Table.Th><Table.Th>{t('common.status')}</Table.Th><Table.Th />
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {data.rows.map((m) => (
                  <Table.Tr key={m.id}>
                    <Table.Td style={{ whiteSpace: 'nowrap' }}><Text size="sm">{formatDateTime(m.created_at)}</Text>
                      <Text size="xs" c="dimmed">{t(`msg.events.${m.event}`, { defaultValue: m.event })} · {m.channel}</Text></Table.Td>
                    <Table.Td><Text size="sm" fw={500}>{m.customer_name ?? '—'}</Text><Text size="xs" c="dimmed" dir="ltr">{m.phone}</Text></Table.Td>
                    <Table.Td maw={420}><Text size="sm" lineClamp={2}>{m.body}</Text>
                      {m.error && <Text size="xs" c="red" lineClamp={1}>{m.error}</Text>}</Table.Td>
                    <Table.Td><Badge color={statusColor[m.status]} variant="light">{t(`msg.status.${m.status}`)}</Badge>
                      {m.attempts > 1 && <Text size="xs" c="dimmed">×{m.attempts}</Text>}</Table.Td>
                    <Table.Td>{canRetry && m.status === 'failed' && (
                      <Button size="compact-xs" variant="subtle" leftSection={<IconRefresh size={12} />} onClick={() => retry(m.id)}>{t('msg.retry')}</Button>
                    )}</Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </Paper>
    </Stack>
  );
}

function SendWizard() {
  const { t, i18n } = useTranslation();
  const { company } = useCompany();
  const [areas, setAreas] = useState<Area[]>([]);
  const [area, setArea] = useState<string | null>(null);
  const [days, setDays] = useState<number | string>(0);
  const [locale, setLocale] = useState(i18n.language);
  const [event, setEvent] = useState('reminder');
  const [body, setBody] = useState('');
  const [preview, setPreview] = useState<SendResult | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (company) api<Area[]>(`/companies/${company.id}/areas`).then(setAreas).catch(() => {});
  }, [company]);

  const payload = (dry: boolean) => ({
    unpaid: { area_id: area ?? '', min_days_late: Number(days) || 0 }, event, locale, dry_run: dry,
    ...(event === 'custom' ? { body } : {}),
  });

  async function run(dry: boolean) {
    if (!company) return;
    setBusy(true);
    try {
      const r = await api<SendResult>(`/companies/${company.id}/messages/send`, { method: 'POST', body: payload(dry) });
      if (dry) setPreview(r);
      else {
        notifications.show({ color: 'teal', message: t('msg.queuedN', { n: r.queued }) });
        setPreview(null);
      }
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Stack maw={800}>
      <Paper withBorder p="md">
        <Stack>
          <Text size="sm" c="dimmed">{t('msg.wizardIntro')}</Text>
          <SimpleGrid cols={{ base: 1, sm: 2 }}>
            <Select label={t('customers.area')} clearable placeholder={t('readings.allAreas')} value={area} onChange={setArea}
              data={areas.map((a) => ({ value: a.id, label: a.name }))} />
            <NumberInput label={t('msg.minDaysLate')} min={0} value={days} onChange={setDays} />
            <Select label={t('msg.event')} value={event} allowDeselect={false} onChange={(v) => v && setEvent(v)}
              data={['reminder', 'custom'].map((e) => ({ value: e, label: t(`msg.events.${e}`) }))} />
            <Select label={t('common.language')} value={locale} allowDeselect={false} onChange={(v) => v && setLocale(v)}
              data={languages.map((l) => ({ value: l.code, label: l.label }))} />
          </SimpleGrid>
          {event === 'custom' && (
            <Textarea label={t('msg.text')} autosize minRows={3} value={body} onChange={(e) => setBody(e.currentTarget.value)}
              description={`${t('msg.placeholders')} {{customer}} {{amount}} {{currency}} {{period}} {{due_date}} {{balance}} {{number}} {{company}}`} />
          )}
          <Group justify="flex-end">
            <Button variant="default" loading={busy} onClick={() => run(true)}>{t('msg.preview')}</Button>
          </Group>
        </Stack>
      </Paper>
      {preview && (
        <Paper withBorder p="md">
          <Stack>
            <Title order={5}>{t('msg.willSend', { n: preview.queued })}</Title>
            {!preview.has_provider && <Alert color="yellow">{t('msg.noProvider')}</Alert>}
            {preview.preview.map((p, i) => (
              <Paper key={i} p="sm" bg="var(--mantine-color-teal-0)" radius="md">
                <Text size="xs" c="dimmed">{p.customer} · <span dir="ltr">{p.phone}</span></Text>
                <Text size="sm" style={{ whiteSpace: 'pre-wrap' }}>{p.text}</Text>
              </Paper>
            ))}
            {preview.skipped.length > 0 && (
              <Text size="sm" c="dimmed">{t('msg.skipped')}: {preview.skipped.slice(0, 20).map((s) => `${s.customer} (${t(`msg.reasons.${s.reason}`, { defaultValue: s.reason })})`).join(', ')}</Text>
            )}
            <Group justify="flex-end">
              <Button leftSection={<IconSend size={16} />} loading={busy} disabled={preview.queued === 0} onClick={() => run(false)}>
                {t('msg.sendN', { n: preview.queued })}
              </Button>
            </Group>
          </Stack>
        </Paper>
      )}
    </Stack>
  );
}

function Settings() {
  const { t, i18n } = useTranslation();
  const { company } = useCompany();
  const [providers, setProviders] = useState<Provider[]>([]);
  const [templates, setTemplates] = useState<Template[]>([]);
  const [settings, setSettings] = useState<{ auto_send_on_payment: boolean; auto_send_on_invoice: boolean; phone_country: string } | null>(null);
  const [editP, setEditP] = useState<Provider | 'new' | null>(null);
  const [editT, setEditT] = useState<Template | 'new' | null>(null);

  const load = useCallback(() => {
    if (!company) return;
    const b = `/companies/${company.id}`;
    api<Provider[]>(`${b}/message-providers`).then(setProviders).catch(() => {});
    api<Template[]>(`${b}/message-templates`).then(setTemplates).catch(() => {});
    api<typeof settings>(`${b}/messages/settings`).then(setSettings).catch(() => {});
  }, [company]);
  useEffect(load, [load]);

  async function saveSettings(patch: Record<string, unknown>) {
    try {
      setSettings(await api(`/companies/${company!.id}/messages/settings`, { method: 'PUT', body: patch }));
      notifySaved(t);
    } catch (e) {
      notifyError(t, e);
    }
  }

  return (
    <Stack>
      {settings && (
        <Paper withBorder p="md">
          <Title order={5} mb="sm">{t('msg.automatic')}</Title>
          <Stack gap="xs">
            <Switch label={t('msg.autoPayment')} checked={settings.auto_send_on_payment} onChange={(e) => saveSettings({ auto_send_on_payment: e.currentTarget.checked })} />
            <Switch label={t('msg.autoInvoice')} checked={settings.auto_send_on_invoice} onChange={(e) => saveSettings({ auto_send_on_invoice: e.currentTarget.checked })} />
            <TextInput label={t('msg.country')} w={160} dir="ltr" leftSection="+" defaultValue={settings.phone_country}
              onBlur={(e) => e.currentTarget.value !== settings.phone_country && saveSettings({ phone_country: e.currentTarget.value })} />
          </Stack>
        </Paper>
      )}
      <Paper withBorder>
        <Group justify="space-between" p="md" pb="xs">
          <Title order={5}>{t('msg.providers')}</Title>
          <Button size="xs" variant="light" leftSection={<IconPlus size={14} />} onClick={() => setEditP('new')}>{t('msg.newProvider')}</Button>
        </Group>
        {providers.length === 0 ? <Text c="dimmed" p="md" size="sm">{t('msg.noProvider')}</Text> : (
          <Table verticalSpacing="xs">
            <Table.Tbody>
              {providers.map((p) => (
                <Table.Tr key={p.id} style={{ opacity: p.is_active ? 1 : 0.5 }}>
                  <Table.Td fw={500}>{p.name} {p.is_default && <Badge size="xs" ms={4}>{t('msg.default')}</Badge>}</Table.Td>
                  <Table.Td>{t(`msg.kinds.${p.kind}`)}</Table.Td>
                  <Table.Td><Button size="compact-xs" variant="subtle" onClick={() => setEditP(p)}>{t('common.edit')}</Button></Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        )}
      </Paper>
      <Paper withBorder>
        <Group justify="space-between" p="md" pb="xs">
          <Title order={5}>{t('msg.templates')}</Title>
          <Button size="xs" variant="light" leftSection={<IconPlus size={14} />} onClick={() => setEditT('new')}>{t('msg.newTemplate')}</Button>
        </Group>
        <Table verticalSpacing="xs" key={i18n.language}>
          <Table.Tbody>
            {templates.map((tp) => (
              <Table.Tr key={tp.id} style={{ opacity: tp.is_active ? 1 : 0.5 }}>
                <Table.Td><Text size="sm" fw={500}>{tp.name}</Text><Text size="xs" c="dimmed">{t(`msg.events.${tp.event}`)} · {tp.locale}</Text></Table.Td>
                <Table.Td maw={500}><Text size="sm" lineClamp={2} dir={tp.locale === 'ar' ? 'rtl' : 'ltr'}>{tp.body}</Text></Table.Td>
                <Table.Td><Button size="compact-xs" variant="subtle" onClick={() => setEditT(tp)}>{t('common.edit')}</Button></Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      </Paper>
      {editP && <ProviderForm p={editP === 'new' ? null : editP} onClose={() => setEditP(null)} onSaved={() => { setEditP(null); load(); }} />}
      {editT && <TemplateForm tp={editT === 'new' ? null : editT} onClose={() => setEditT(null)} onSaved={() => { setEditT(null); load(); }} />}
    </Stack>
  );
}

function ProviderForm({ p, onClose, onSaved }: { p: Provider | null; onClose: () => void; onSaved: () => void }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const [kind, setKind] = useState(p?.kind ?? 'whatsapp_cloud');
  const [name, setName] = useState(p?.name ?? '');
  const [cfg, setCfg] = useState<Record<string, string>>(p?.config ?? {});
  const [isDefault, setIsDefault] = useState(p?.is_default ?? false);
  const [active, setActive] = useState(p?.is_active ?? true);
  const [busy, setBusy] = useState(false);

  async function save() {
    setBusy(true);
    try {
      const body = { kind, name, config: cfg, is_default: isDefault, is_active: active };
      if (p) await api(`/companies/${company!.id}/message-providers/${p.id}`, { method: 'PATCH', body });
      else await api(`/companies/${company!.id}/message-providers`, { method: 'POST', body });
      notifySaved(t);
      onSaved();
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened onClose={onClose} title={p?.name ?? t('msg.newProvider')}>
      <Stack>
        <Select label={t('msg.kind')} value={kind} allowDeselect={false} onChange={(v) => v && setKind(v)}
          data={Object.keys(providerFields).map((k) => ({ value: k, label: t(`msg.kinds.${k}`) }))} />
        <TextInput label={t('methods.name')} required value={name} onChange={(e) => setName(e.currentTarget.value)} />
        {providerFields[kind].map((f) => f.secret ? (
          <PasswordInput key={f.key} label={f.key} dir="ltr" value={cfg[f.key] ?? ''} onChange={(e) => { const v = e.currentTarget.value; setCfg((c) => ({ ...c, [f.key]: v })); }} />
        ) : (
          <TextInput key={f.key} label={f.key} dir="ltr" value={cfg[f.key] ?? ''} onChange={(e) => { const v = e.currentTarget.value; setCfg((c) => ({ ...c, [f.key]: v })); }} />
        ))}
        <Text size="xs" c="dimmed">{t(`msg.help.${kind}`)}</Text>
        <Switch label={t('msg.default')} checked={isDefault} onChange={(e) => setIsDefault(e.currentTarget.checked)} />
        {p && <Switch label={t('common.active')} checked={active} onChange={(e) => setActive(e.currentTarget.checked)} />}
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
          <Button loading={busy} disabled={!name} onClick={save}>{t('common.save')}</Button>
        </Group>
      </Stack>
    </Modal>
  );
}

function TemplateForm({ tp, onClose, onSaved }: { tp: Template | null; onClose: () => void; onSaved: () => void }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const [f, setF] = useState({ event: tp?.event ?? 'reminder', locale: tp?.locale ?? 'ar', name: tp?.name ?? '', body: tp?.body ?? '', wa_template: tp?.wa_template ?? '', is_active: tp?.is_active ?? true });
  const [busy, setBusy] = useState(false);

  async function save() {
    setBusy(true);
    try {
      if (tp) await api(`/companies/${company!.id}/message-templates/${tp.id}`, { method: 'PATCH', body: f });
      else await api(`/companies/${company!.id}/message-templates`, { method: 'POST', body: f });
      notifySaved(t);
      onSaved();
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened onClose={onClose} size="lg" title={tp?.name ?? t('msg.newTemplate')}>
      <Stack>
        <Group grow>
          <Select label={t('msg.event')} value={f.event} allowDeselect={false} onChange={(v) => v && setF({ ...f, event: v })}
            data={events.map((e) => ({ value: e, label: t(`msg.events.${e}`) }))} />
          <Select label={t('common.language')} value={f.locale} allowDeselect={false} onChange={(v) => v && setF({ ...f, locale: v })}
            data={languages.map((l) => ({ value: l.code, label: l.label }))} />
        </Group>
        <TextInput label={t('methods.name')} value={f.name} onChange={(e) => setF({ ...f, name: e.currentTarget.value })} />
        <Textarea label={t('msg.text')} autosize minRows={4} dir={f.locale === 'ar' ? 'rtl' : 'ltr'} value={f.body}
          onChange={(e) => setF({ ...f, body: e.currentTarget.value })} description={`${t('msg.placeholders')} {{customer}} {{amount}} {{currency}} {{period}} {{due_date}} {{balance}} {{number}} {{company}}`} />
        <TextInput label={t('msg.waTemplate')} dir="ltr" value={f.wa_template} onChange={(e) => setF({ ...f, wa_template: e.currentTarget.value })}
          description={t('msg.waTemplateHelp')} />
        {tp && <Switch label={t('common.active')} checked={f.is_active} onChange={(e) => setF({ ...f, is_active: e.currentTarget.checked })} />}
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
          <Button loading={busy} disabled={!f.name || !f.body} onClick={save}>{t('common.save')}</Button>
        </Group>
      </Stack>
    </Modal>
  );
}

/** "WhatsApp" button: open WhatsApp with the text (wa.me, no provider needed) or queue it for automatic sending. */
export function WhatsAppButton({ customerId, invoiceId, paymentId, event = 'reminder', size = 'sm' }: {
  customerId: string; invoiceId?: string; paymentId?: string; event?: string; size?: string;
}) {
  const { t, i18n } = useTranslation();
  const { company } = useCompany();
  const [busy, setBusy] = useState(false);

  async function open() {
    if (!company) return;
    try {
      const r = await api<{ wa_link: string }>(`/companies/${company.id}/messages/link`, {
        method: 'POST', body: { customer_id: customerId, invoice_id: invoiceId ?? '', payment_id: paymentId ?? '', event, locale: i18n.language },
      });
      window.open(r.wa_link, '_blank', 'noopener');
    } catch (e) {
      notifyError(t, e);
    }
  }

  async function queue() {
    if (!company) return;
    setBusy(true);
    try {
      const body: Record<string, unknown> = { event, locale: i18n.language };
      if (invoiceId) body.invoice_ids = [invoiceId];
      else if (paymentId) body.payment_ids = [paymentId];
      else body.customer_ids = [customerId];
      const r = await api<SendResult>(`/companies/${company.id}/messages/send`, { method: 'POST', body });
      if (r.queued > 0) notifications.show({ color: 'teal', message: t('msg.queuedN', { n: r.queued }) });
      else notifications.show({ color: 'orange', message: t(`msg.reasons.${r.skipped[0]?.reason ?? 'no_phone'}`) });
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Menu position="bottom-end">
      <Menu.Target>
        <Button variant="default" size={size} color="green" loading={busy} leftSection={<IconBrandWhatsapp size={16} color="#25D366" />}>WhatsApp</Button>
      </Menu.Target>
      <Menu.Dropdown>
        <Menu.Item onClick={open}>{t('msg.openWhatsApp')}</Menu.Item>
        <Menu.Item onClick={queue}>{t('msg.queueSend')}</Menu.Item>
      </Menu.Dropdown>
    </Menu>
  );
}
