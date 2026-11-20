import { useEffect, useState, type ReactNode } from 'react';
import { Alert, Button, Group, Menu, Modal, SegmentedControl, Select, Stack, Text, Textarea, TextInput } from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { IconBrandWhatsapp, IconMail, IconMessage, IconSend } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { api } from '../../api';
import { useCompany } from '../../company';
import { openExternal } from '../../native';
import { formatDate, notifyError } from '../../util';
import { DocQR, qrTextOf, type DocLayout } from '../../components/DocLayout';

export interface SaleItem {
  id: string; code: string; name: string; unit: string; kind: 'stock' | 'service' | 'consumable'; sale_price: string; sale_currency: string | null;
  quantity: string; is_active: boolean; is_sellable: boolean; is_purchasable: boolean; sale_tax_rate_id: string | null; description: string;
  prices: { currency: string; price: string }[];
}

/** Sale price of an item in a currency: the price list entry, else the main price when its currency matches (or has none). */
export function itemPrice(it: SaleItem | undefined, currency: string, base: string): string {
  if (!it) return '';
  const p = it.prices?.find((x) => x.currency === currency);
  if (p) return String(Number(p.price));
  if (Number(it.sale_price) && ((it.sale_currency ?? base) === currency)) return String(Number(it.sale_price));
  return '';
}

/** Company document settings (logo, header, footer, tax id) used on printed documents. */
export interface DocCompany {
  company_name?: string; company_name_ar?: string; company_logo?: string; company_header?: string; company_footer?: string; company_tax_id?: string; company_tax_label?: string;
}

/** Header of a printed document: logo + company name + header text on one side, title / number / dates on the other.
 *  layout: the company's choices for this document (logo size, title, header text); title2: the title in the layout's
 *  second language. */
export function DocHeader({ c, title, lines, layout, title2 }: { c: DocCompany; title: string; lines: ReactNode[]; layout?: DocLayout; title2?: string }) {
  const { t, i18n } = useTranslation();
  const rtl = i18n.language === 'ar';
  const l = layout ?? {};
  const logoH = l.logo_size === 'sm' ? 40 : l.logo_size === 'lg' ? 96 : 64;
  const header = l.header || c.company_header;
  return (
    <Group justify="space-between" align="flex-start" wrap="nowrap" gap="md" className="b-header">
      <Group gap="sm" align="flex-start" wrap="nowrap">
        {c.company_logo && l.show_logo !== false ? <img className="f-logo" src={c.company_logo} alt="" style={{ maxHeight: logoH, maxWidth: logoH * 2.5, objectFit: 'contain' }} /> : null}
        <div>
          <Text fw={700} size="lg">{rtl && c.company_name_ar ? c.company_name_ar : c.company_name}</Text>
          {header && <Text size="xs" style={{ whiteSpace: 'pre-wrap' }}>{header}</Text>}
          {c.company_tax_id && <Text size="xs" className="f-tax_id">{c.company_tax_label || t('docs.taxId')}: <span dir="ltr">{c.company_tax_id}</span></Text>}
        </div>
      </Group>
      <div style={{ textAlign: 'end', flex: 'none' }}>
        <Text fw={700} size="lg" className="doc-title">{l.title ? `${l.title} ${title.replace(/^[^#]*/, '')}`.trim() : title}</Text>
        {title2 && title2 !== title && <Text size="sm" className="doc-title" fw={600}>{title2}</Text>}
        {lines.map((x, i) => <Text size="sm" key={i}>{x}</Text>)}
      </div>
    </Group>
  );
}

export function DocFooter({ c, terms, layout, qr }: { c: DocCompany; terms?: string; layout?: DocLayout; qr?: string }) {
  const { t } = useTranslation();
  const l = layout ?? {};
  const footer = l.footer || c.company_footer;
  const q = qr ? qrTextOf(l, qr) : '';
  if (!terms && !footer && !q) return null;
  return (
    <div style={{ marginTop: 16 }} className="b-footer">
      {terms && <div className="f-terms"><Text size="xs" fw={600}>{t('sales.terms')}</Text><Text size="xs" style={{ whiteSpace: 'pre-wrap' }}>{terms}</Text></div>}
      {footer && <Text size="xs" ta="center" mt="sm" className="f-footer" style={{ whiteSpace: 'pre-wrap' }}>{footer}</Text>}
      {q && <DocQR text={q} />}
    </div>
  );
}

export type DocKind = 'quote' | 'order' | 'invoice' | 'statement';

/** "Send" button: WhatsApp (opens the app with the text and link), or queue by WhatsApp/SMS provider, or e-mail. */
export function SendDocButton({ kind, id, from, to, email, onSent, size = 'sm' }: {
  kind: DocKind; id: string; from?: string; to?: string; email?: string; onSent?: () => void; size?: string;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState<null | 'email' | 'provider'>(null);
  const { company } = useCompany();
  const { i18n } = useTranslation();

  async function wa() {
    if (!company) return;
    try {
      const r = await api<{ wa_link: string }>(`/companies/${company.id}/send-document`, {
        method: 'POST', body: { kind, id, from: from ?? '', to: to ?? '', channel: 'link', locale: i18n.language },
      });
      openExternal(r.wa_link);
      onSent?.();
    } catch (e) {
      notifyError(t, e);
    }
  }

  return (
    <>
      <Menu position="bottom-end" withinPortal>
        <Menu.Target>
          <Button variant="default" size={size} leftSection={<IconSend size={16} />}>{t('sales.send')}</Button>
        </Menu.Target>
        <Menu.Dropdown>
          <Menu.Item leftSection={<IconBrandWhatsapp size={16} color="#25D366" />} onClick={() => void wa()}>{t('msg.openWhatsApp')}</Menu.Item>
          <Menu.Item leftSection={<IconMessage size={16} />} onClick={() => setOpen('provider')}>{t('sales.sendAuto')}</Menu.Item>
          <Menu.Item leftSection={<IconMail size={16} />} onClick={() => setOpen('email')}>{t('sales.sendEmail')}</Menu.Item>
        </Menu.Dropdown>
      </Menu>
      {open && <SendDialog kind={kind} id={id} from={from} to={to} email={email} mode={open} onClose={() => setOpen(null)} onSent={onSent} />}
    </>
  );
}

function SendDialog({ kind, id, from, to, email, mode, onClose, onSent }: {
  kind: DocKind; id: string; from?: string; to?: string; email?: string; mode: 'email' | 'provider'; onClose: () => void; onSent?: () => void;
}) {
  const { t, i18n } = useTranslation();
  const { company } = useCompany();
  const [channel, setChannel] = useState(mode === 'email' ? 'email' : 'whatsapp');
  const [address, setAddress] = useState(email ?? '');
  const [text, setText] = useState('');
  const [subject, setSubject] = useState('');
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!company) return;
    api<{ text: string; subject: string }>(`/companies/${company.id}/send-document`, {
      method: 'POST', body: { kind, id, from: from ?? '', to: to ?? '', channel: 'link', locale: i18n.language },
    }).then((r) => { setText(r.text); setSubject(r.subject); }).catch(() => {});
    // the preview is only fetched once
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [company]);

  async function send() {
    if (!company) return;
    setBusy(true);
    try {
      const r = await api<{ to: string; has_provider?: boolean }>(`/companies/${company.id}/send-document`, {
        method: 'POST', body: { kind, id, from: from ?? '', to: to ?? '', channel, to_address: address, locale: i18n.language, message: text },
      });
      notifications.show({ color: r.has_provider === false ? 'orange' : 'teal', message: r.has_provider === false ? t('msg.noProvider') : t('sales.sentTo', { to: r.to }) });
      onSent?.();
      onClose();
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened onClose={onClose} title={t('sales.send')} size="lg">
      <Stack>
        {mode === 'provider' && (
          <SegmentedControl value={channel} onChange={setChannel} data={[{ value: 'whatsapp', label: 'WhatsApp' }, { value: 'sms', label: 'SMS' }]} />
        )}
        <TextInput label={channel === 'email' ? t('customers.email') : t('customers.phone')} dir="ltr" value={address}
          placeholder={channel === 'email' ? 'name@example.com' : t('sales.customerPhone')} onChange={(e) => setAddress(e.currentTarget.value)} />
        {channel === 'email' && subject && <Text size="sm"><b>{t('sales.subject')}:</b> {subject}</Text>}
        <Textarea label={t('msg.text')} autosize minRows={3} value={text} onChange={(e) => setText(e.currentTarget.value)} />
        {channel === 'email' && <Alert color="blue" variant="light">{t('sales.emailHint')}</Alert>}
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
          <Button leftSection={<IconSend size={16} />} loading={busy} disabled={!text} onClick={send}>{t('sales.send')}</Button>
        </Group>
      </Stack>
    </Modal>
  );
}

/** Customer select that searches the server as you type (companies can have thousands of customers). */
export function CustomerPicker({ value, onChange, label, initial }: {
  value: string | null; onChange: (id: string | null, c?: { id: string; name: string; code: string; email: string }) => void;
  label?: string; initial?: { id: string; name: string; code: string };
}) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const [q, setQ] = useState('');
  const [rows, setRows] = useState<{ id: string; name: string; code: string; email: string }[]>(initial ? [{ ...initial, email: '' }] : []);
  useEffect(() => {
    if (!company) return;
    const h = window.setTimeout(() => {
      api<{ id: string; name: string; code: string; email: string }[]>(`/companies/${company.id}/customers?limit=50&q=${encodeURIComponent(q)}`)
        .then((r) => setRows((old) => {
          const keep = old.filter((o) => o.id === value && !r.some((x) => x.id === o.id));
          return [...keep, ...r];
        })).catch(() => {});
    }, 250);
    return () => window.clearTimeout(h);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [company, q]);
  return (
    <SelectSearch label={label ?? t('invoices.customer')} value={value} searchValue={q} onSearchChange={setQ}
      data={rows.map((c) => ({ value: c.id, label: `${c.code} · ${c.name}` }))}
      onChange={(v) => onChange(v, rows.find((r) => r.id === v))} />
  );
}

function SelectSearch(props: {
  label: string; value: string | null; searchValue: string; onSearchChange: (s: string) => void;
  data: { value: string; label: string }[]; onChange: (v: string | null) => void;
}) {
  return (
    <Select label={props.label} required searchable value={props.value} searchValue={props.searchValue}
      onSearchChange={props.onSearchChange} data={props.data} onChange={props.onChange} filter={({ options }) => options}
      nothingFoundMessage="—" comboboxProps={{ withinPortal: true }} />
  );
}

export const orderColor: Record<string, string> = { draft: 'gray', sent: 'blue', confirmed: 'teal', done: 'green', cancelled: 'red' };

export function DateLine({ label, value }: { label: string; value: string | null | undefined }) {
  if (!value) return null;
  return <>{label}: {formatDate(value)}</>;
}
