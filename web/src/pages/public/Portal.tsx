import { printPage } from '../../native';
import { useEffect, useState } from 'react';
import {
  Badge, Box, Button, Center, Container, Divider, Group, Loader, Modal, Paper, Stack, Table, Text, Title,
} from '@mantine/core';
import { IconCheck, IconFileText, IconPrinter } from '@tabler/icons-react';
import { TextInput } from '@mantine/core';
import { DocFooter, DocHeader, orderColor } from '../sales/common';
import { StatementDoc, type StmtCurrency } from '../sales/Statements';
import { useTranslation } from 'react-i18next';
import { api } from '../../api';
import { formatAmount, formatDate } from '../../util';
import LangSwitch from '../../components/LangSwitch';
import { applyLang } from '../../i18n';
import Logo from '../../components/Logo';
import { invoiceColor, type InvoiceDetail, type Money } from '../../billing';

interface PInvoice {
  id: string; number: number; period: string | null; issue_date: string; due_date: string; currency: string; total: string;
  paid: string; balance: string; status: 'open' | 'partial' | 'paid' | 'void'; local_currency: string | null;
  local_rate: string | null; local_currency_total: string | null;
}
interface PortalData {
  company: { name_en: string; name_ar: string; base_currency: string; local_currency: string; logo?: string; doc_header?: string; doc_footer?: string; tax_id?: string };
  quotes?: { id: string; number: number; order_number: number | null; status: string; quote_date: string; valid_until: string | null; currency: string; total: string; accepted_at: string | null }[];
  customer: { name: string; code: string; area_name: string | null; due: Money[]; credit: Money[]; locale?: string | null };
  open_invoices: PInvoice[]; paid_invoices: PInvoice[];
  payments: { number: number; payment_date: string; currency: string; amount: string }[];
}

// Decimals are not known without login; 0 for LBP-like currencies, 2 otherwise.
const decOf = (c: string) => (['LBP', 'SYP', 'IQD', 'IRR', 'YER', 'JPY', 'KRW', 'IDR', 'XOF', 'XAF', 'CLP'].includes(c) ? 0 : 2);

export default function Portal({ token }: { token: string }) {
  const { t, i18n } = useTranslation();
  const [d, setD] = useState<PortalData | null | 'missing'>(null);
  const [view, setView] = useState<string | null>(null);
  const [quote, setQuote] = useState<string | null>(null);
  const [stmt, setStmt] = useState<[string, string] | null>(null);

  const load = () => api<PortalData>(`/portal/${token}`).then((x) => {
    // first visit: the customer's own language (a choice made on this page is kept afterwards)
    let chosen = false;
    try { chosen = localStorage.getItem('paxve.portal.lang') === '1'; } catch { /* storage unavailable */ }
    if (!chosen && x.customer.locale) applyLang(x.customer.locale);
    setD(x);
  }).catch(() => setD('missing'));
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => { void load(); }, [token]);
  // deep links: #/p/<token>/quote/<id> · /invoice/<id> · /statement/<from>/<to>
  useEffect(() => {
    const route = () => {
      const [, , sub, a, b] = window.location.hash.replace(/^#\/?/, '').split('/');
      if (sub === 'quote' && a) setQuote(a);
      if (sub === 'invoice' && a) setView(a);
      if (sub === 'statement' && a && b) setStmt([a, b]);
    };
    route();
    window.addEventListener('hashchange', route);
    return () => window.removeEventListener('hashchange', route);
  }, []);

  if (d === null) return <Center h="100vh"><Loader /></Center>;
  if (d === 'missing') {
    return (
      <Center h="100vh" p="md">
        <Stack align="center"><Logo size="xl" /><Text c="dimmed">{t('portal.invalid')}</Text></Stack>
      </Center>
    );
  }
  const company = i18n.language === 'ar' && d.company.name_ar ? d.company.name_ar : d.company.name_en;

  const row = (iv: PInvoice) => (
    <Table.Tr key={iv.id} style={{ cursor: 'pointer' }} onClick={() => setView(iv.id)}>
      <Table.Td><Text size="sm" fw={500}>#{iv.number}{iv.period ? ` · ${iv.period}` : ''}</Text><Text size="xs" c="dimmed">{formatDate(iv.issue_date)}</Text></Table.Td>
      <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>
        {formatAmount(iv.status === 'paid' ? iv.total : iv.balance, decOf(iv.currency))} {iv.currency}
        {iv.local_currency_total && iv.local_currency && iv.local_currency !== iv.currency && (
          <Text size="xs" c="dimmed">{formatAmount(iv.local_currency_total, decOf(iv.local_currency))} {iv.local_currency}</Text>
        )}
      </Table.Td>
      <Table.Td><Badge variant="light" color={invoiceColor[iv.status]}>{t(`invoices.statuses.${iv.status}`)}</Badge></Table.Td>
    </Table.Tr>
  );

  return (
    <Box mih="100vh" bg="var(--mantine-color-gray-0)">
      <Paper radius={0} shadow="xs" px="md" py="xs">
        <Group justify="space-between" wrap="nowrap">
          <Text fw={700} truncate>{company}</Text>
          <LangSwitch onChange={() => { try { localStorage.setItem('paxve.portal.lang', '1'); } catch { /* storage unavailable */ } }} />
        </Group>
      </Paper>
      <Container size="sm" py="lg" px="md">
        <Stack>
          <div>
            <Title order={3}>{d.customer.name}</Title>
            <Text size="sm" c="dimmed">#{d.customer.code}{d.customer.area_name ? ` · ${d.customer.area_name}` : ''}</Text>
          </div>
          <Paper withBorder p="lg">
            <Text size="xs" c="dimmed" tt="uppercase" fw={600}>{t('portal.youOwe')}</Text>
            {d.customer.due.length === 0 ? (
              <Text size="1.8rem" fw={800} c="teal.8">{t('customers.nothingDue')}</Text>
            ) : d.customer.due.map((m) => (
              <Text key={m.currency} size="2rem" fw={800} c="red.7" dir="ltr" style={{ textAlign: 'start' }}>
                {formatAmount(m.due, decOf(m.currency))} {m.currency}
              </Text>
            ))}
            {d.customer.credit.map((m) => (
              <Text key={m.currency} size="sm" c="teal.8" dir="ltr" style={{ textAlign: 'start' }}>
                {t('customers.credit')}: {formatAmount(m.credit, decOf(m.currency))} {m.currency}
              </Text>
            ))}
          </Paper>

          {d.quotes && d.quotes.length > 0 && (
            <Paper withBorder>
              <Text fw={600} p="sm" pb={0}>{t('sales.quotes')}</Text>
              <Table verticalSpacing="sm"><Table.Tbody>{d.quotes.map((q) => (
                <Table.Tr key={q.id} style={{ cursor: 'pointer' }} onClick={() => setQuote(q.id)}>
                  <Table.Td><Text size="sm" fw={500}>{q.order_number && q.status !== 'sent' ? `SO-${q.order_number}` : `Q-${q.number}`}</Text><Text size="xs" c="dimmed">{formatDate(q.quote_date)}</Text></Table.Td>
                  <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(q.total, decOf(q.currency))} {q.currency}</Table.Td>
                  <Table.Td><Badge variant="light" color={q.accepted_at && q.status === 'sent' ? 'teal' : orderColor[q.status]}>{q.accepted_at && q.status === 'sent' ? t('sales.accepted') : t(`sales.st.${q.status}`)}</Badge></Table.Td>
                </Table.Tr>
              ))}</Table.Tbody></Table>
            </Paper>
          )}
          <Button variant="default" leftSection={<IconFileText size={16} />} style={{ alignSelf: 'flex-start' }} onClick={() => {
            const d0 = new Date(); d0.setMonth(d0.getMonth() - 3);
            const pad = (n: number) => String(n).padStart(2, '0');
            const now = new Date();
            setStmt([`${d0.getFullYear()}-${pad(d0.getMonth() + 1)}-01`, `${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}`]);
          }}>{t('stmt.title')}</Button>
          {d.open_invoices.length > 0 && (
            <Paper withBorder>
              <Text fw={600} p="sm" pb={0}>{t('portal.unpaid')}</Text>
              <Table verticalSpacing="sm"><Table.Tbody>{d.open_invoices.map(row)}</Table.Tbody></Table>
            </Paper>
          )}
          {d.paid_invoices.length > 0 && (
            <Paper withBorder>
              <Text fw={600} p="sm" pb={0}>{t('portal.paid')}</Text>
              <Table verticalSpacing="sm"><Table.Tbody>{d.paid_invoices.map(row)}</Table.Tbody></Table>
            </Paper>
          )}
          {d.payments.length > 0 && (
            <Paper withBorder>
              <Text fw={600} p="sm" pb={0}>{t('bill.payments')}</Text>
              <Table verticalSpacing="xs">
                <Table.Tbody>
                  {d.payments.map((p) => (
                    <Table.Tr key={p.number}>
                      <Table.Td>#{p.number} · {formatDate(p.payment_date)}</Table.Td>
                      <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(p.amount, decOf(p.currency))} {p.currency}</Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </Paper>
          )}
          <Group justify="center" gap={6} c="dimmed"><Text size="xs">{t('portal.poweredBy')}</Text><Logo /></Group>
        </Stack>
      </Container>
      {view && <PortalInvoice token={token} id={view} onClose={() => setView(null)} />}
      {quote && <PortalQuote token={token} id={quote} onClose={() => setQuote(null)} onAccepted={() => void load()} />}
      {stmt && <PortalStatement token={token} from={stmt[0]} to={stmt[1]} company={d.company} customer={d.customer} onClose={() => setStmt(null)} />}
    </Box>
  );
}

function PortalInvoice({ token, id, onClose }: { token: string; id: string; onClose: () => void }) {
  const { t, i18n } = useTranslation();
  const [d, setD] = useState<InvoiceDetail | null>(null);
  useEffect(() => {
    api<InvoiceDetail>(`/portal/${token}/invoices/${id}`).then(setD).catch(onClose);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [token, id]);
  const inv = d?.invoice as (InvoiceDetail['invoice'] & { local_currency?: string; local_rate?: string; local_currency_total?: string }) | undefined;
  const rtl = i18n.language === 'ar';
  return (
    <Modal opened onClose={onClose} size="lg" title={inv ? `${t('invoices.invoice')} #${inv.number}` : t('invoices.invoice')}>
      {!d || !inv ? <Center p="xl"><Loader size="sm" /></Center> : (
        <Stack>
          <div className="print-doc" dir={rtl ? 'rtl' : 'ltr'}>
            <DocHeader c={inv} title={`${t('invoices.invoice')} #${inv.number}`} lines={[...(inv.period ? [inv.period] : []),
              `${t('invoices.date')}: ${formatDate(inv.issue_date)}`, `${t('invoices.due')}: ${formatDate(inv.due_date)}`]} />
            <Divider my="sm" />
            <Text fw={600}>{inv.customer_name} ({inv.customer_code})</Text>
            {inv.meter_no && <Text size="sm">{t('invoices.meter')}: {inv.meter_no}</Text>}
            <table style={{ marginTop: 12 }}>
              <tbody>
                {d.lines.map((l) => (
                  <tr key={l.line_no}>
                    <td>{l.kind === 'usage' && l.meta?.previous
                      ? <span dir="ltr">{l.meta.previous} → {l.meta.current} = {l.meta.used} {l.meta.unit}</span> : l.description}</td>
                    <td className="num">{formatAmount(l.amount, decOf(inv.currency))}</td>
                  </tr>
                ))}
              </tbody>
              <tfoot>
                <tr><th>{t('invoices.total')}</th><th className="num">{formatAmount(inv.total, decOf(inv.currency))} {inv.currency}</th></tr>
                {inv.local_currency_total && inv.local_currency && inv.local_currency !== inv.currency && (
                  <tr><td>{t('bulk.inLocal', { cur: inv.local_currency, rate: inv.local_rate })}</td>
                    <td className="num">{formatAmount(inv.local_currency_total, decOf(inv.local_currency))} {inv.local_currency}</td></tr>
                )}
                {inv.status !== 'paid' && <tr><th>{t('invoices.balance')}</th><th className="num">{formatAmount(inv.balance, decOf(inv.currency))} {inv.currency}</th></tr>}
              </tfoot>
            </table>
          </div>
          <Group justify="flex-end" className="no-print">
            <Button variant="default" onClick={onClose}>{t('common.close')}</Button>
            <Button leftSection={<IconPrinter size={16} />} onClick={() => void printPage()}>{t('invoices.print')}</Button>
          </Group>
        </Stack>
      )}
    </Modal>
  );
}

interface PQuote {
  order: { id: string; number: number; order_number: number | null; status: string; quote_date: string; valid_until: string | null; order_date: string | null;
    currency: string; subtotal: string; tax_total: string; total: string; notes: string; terms: string; accepted_at: string | null; accepted_name: string;
    customer_name: string; customer_code: string; company_name: string; company_name_ar: string; company_logo: string; company_header: string;
    company_footer: string; company_tax_id: string };
  lines: { id: string; description: string; quantity: string; unit: string | null; unit_price: string; discount_pct: string; amount: string; tax_amount: string; tax_name: string | null }[];
}

function PortalQuote({ token, id, onClose, onAccepted }: { token: string; id: string; onClose: () => void; onAccepted: () => void }) {
  const { t, i18n } = useTranslation();
  const [d, setD] = useState<PQuote | null>(null);
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  const load = () => api<PQuote>(`/portal/${token}/quotes/${id}`).then(setD).catch(onClose);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => { void load(); }, [token, id]);

  async function accept() {
    setBusy(true);
    setErr('');
    try {
      await api(`/portal/${token}/quotes/${id}/accept`, { method: 'POST', body: { name } });
      await load();
      onAccepted();
    } catch {
      setErr(t('sales.acceptFailed'));
    } finally {
      setBusy(false);
    }
  }

  const o = d?.order;
  const dec = o ? decOf(o.currency) : 2;
  const isOrder = o && (o.status === 'confirmed' || o.status === 'done');
  const expired = o?.valid_until && o.valid_until < new Date().toISOString().slice(0, 10);
  return (
    <Modal opened onClose={onClose} size="lg" title={o ? (isOrder ? `${t('sales.order')} SO-${o.order_number}` : `${t('sales.quote')} Q-${o.number}`) : t('sales.quote')}>
      {!d || !o ? <Center p="xl"><Loader size="sm" /></Center> : (
        <Stack>
          <div className="print-doc" dir={i18n.language === 'ar' ? 'rtl' : 'ltr'}>
            <DocHeader c={o} title={isOrder ? t('sales.order') : t('sales.quote')} lines={[
              isOrder ? `SO-${o.order_number}` : `Q-${o.number}`, `${t('invoices.date')}: ${formatDate(o.order_date ?? o.quote_date)}`,
              ...(o.valid_until && !isOrder ? [`${t('sales.validUntil')}: ${formatDate(o.valid_until)}`] : []),
            ]} />
            <Divider my="sm" />
            <Text fw={600}>{o.customer_name} ({o.customer_code})</Text>
            <table style={{ marginTop: 12 }}>
              <thead><tr><th>{t('invoices.description')}</th><th className="num">{t('invoices.qty')}</th><th className="num">{t('invoices.price')}</th><th className="num">{t('pur.amount')}</th></tr></thead>
              <tbody>
                {d.lines.map((l) => (
                  <tr key={l.id}>
                    <td style={{ whiteSpace: 'pre-wrap' }}>{l.description}{Number(l.discount_pct) ? ` (-${Number(l.discount_pct)}%)` : ''}</td>
                    <td className="num">{Number(l.quantity)} {l.unit ?? ''}</td>
                    <td className="num">{formatAmount(l.unit_price, dec)}</td>
                    <td className="num">{formatAmount(l.amount, dec)}{Number(l.tax_amount) > 0 && <div style={{ fontSize: 11 }}>+ {l.tax_name}: {formatAmount(l.tax_amount, dec)}</div>}</td>
                  </tr>
                ))}
              </tbody>
              <tfoot>
                {Number(o.tax_total) > 0 && <tr><td colSpan={3}>{t('net.tax')}</td><td className="num">{formatAmount(o.tax_total, dec)}</td></tr>}
                <tr><th colSpan={3}>{t('sales.total')}</th><th className="num">{formatAmount(o.total, dec)} {o.currency}</th></tr>
              </tfoot>
            </table>
            {o.notes && <Text size="sm" mt="sm" style={{ whiteSpace: 'pre-wrap' }}>{o.notes}</Text>}
            <DocFooter c={o} terms={o.terms} />
          </div>
          {o.status === 'sent' && !o.accepted_at && !expired && (
            <Paper withBorder p="sm" className="no-print">
              <Text size="sm" fw={600}>{t('sales.acceptTitle')}</Text>
              <Text size="xs" c="dimmed" mb="xs">{t('sales.acceptHint')}</Text>
              <Group align="flex-end">
                <TextInput style={{ flex: 1 }} label={t('sales.yourName')} value={name} onChange={(e) => setName(e.currentTarget.value)} />
                <Button color="teal" leftSection={<IconCheck size={16} />} loading={busy} disabled={!name.trim()} onClick={() => void accept()}>{t('sales.accept')}</Button>
              </Group>
              {err && <Text size="sm" c="red" mt={4}>{err}</Text>}
            </Paper>
          )}
          {o.accepted_at && <Badge color="teal" size="lg" variant="light">{t('sales.acceptedBy', { name: o.accepted_name, date: formatDate(o.accepted_at) })}</Badge>}
          {expired && o.status === 'sent' && !o.accepted_at && <Text c="orange" size="sm">{t('sales.expired')}</Text>}
          <Group justify="flex-end" className="no-print">
            <Button variant="default" onClick={onClose}>{t('common.close')}</Button>
            <Button leftSection={<IconPrinter size={16} />} onClick={() => void printPage()}>{t('invoices.print')}</Button>
          </Group>
        </Stack>
      )}
    </Modal>
  );
}

function PortalStatement({ token, from, to, company, customer, onClose }: {
  token: string; from: string; to: string; company: PortalData['company']; customer: PortalData['customer']; onClose: () => void;
}) {
  const { t } = useTranslation();
  const [cur, setCur] = useState<StmtCurrency[] | null>(null);
  const [f, setF] = useState(from);
  const [tt, setT] = useState(to);
  useEffect(() => {
    setCur(null);
    api<{ currencies: StmtCurrency[] }>(`/portal/${token}/statement/${f}/${tt}`).then((r) => setCur(r.currencies)).catch(() => setCur([]));
  }, [token, f, tt]);
  const c = { company_name: company.name_en, company_name_ar: company.name_ar, company_logo: company.logo, company_header: company.doc_header,
    company_footer: company.doc_footer, company_tax_id: company.tax_id };
  return (
    <Modal opened onClose={onClose} size="xl" title={t('stmt.title')}>
      <Stack>
        <Group className="no-print">
          <TextInput type="date" label={t('reports.from')} value={f} onChange={(e) => setF(e.currentTarget.value)} />
          <TextInput type="date" label={t('reports.to')} value={tt} onChange={(e) => setT(e.currentTarget.value)} />
        </Group>
        {!cur ? <Center p="xl"><Loader size="sm" /></Center> : (
          <div className="print-doc"><StatementDoc c={c} customer={customer} from={f} to={tt} currencies={cur} decimalsOf={decOf} /></div>
        )}
        <Group justify="flex-end" className="no-print">
          <Button variant="default" onClick={onClose}>{t('common.close')}</Button>
          <Button leftSection={<IconPrinter size={16} />} onClick={() => void printPage()}>{t('invoices.print')}</Button>
        </Group>
      </Stack>
    </Modal>
  );
}