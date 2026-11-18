import { printPage } from '../../native';
import { useEffect, useState } from 'react';
import {
  Badge, Box, Button, Center, Container, Divider, Group, Loader, Modal, Paper, Stack, Table, Text, Title,
} from '@mantine/core';
import { IconPrinter } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { api } from '../../api';
import { formatAmount, formatDate } from '../../util';
import LangSwitch from '../../components/LangSwitch';
import Logo from '../../components/Logo';
import { invoiceColor, type InvoiceDetail, type Money } from '../../billing';

interface PInvoice {
  id: string; number: number; period: string | null; issue_date: string; due_date: string; currency: string; total: string;
  paid: string; balance: string; status: 'open' | 'partial' | 'paid' | 'void'; local_currency: string | null;
  local_rate: string | null; local_currency_total: string | null;
}
interface PortalData {
  company: { name_en: string; name_ar: string; base_currency: string; local_currency: string };
  customer: { name: string; code: string; area_name: string | null; due: Money[]; credit: Money[] };
  open_invoices: PInvoice[]; paid_invoices: PInvoice[];
  payments: { number: number; payment_date: string; currency: string; amount: string }[];
}

// Decimals are not known without login; 0 for LBP-like currencies, 2 otherwise.
const decOf = (c: string) => (['LBP', 'SYP', 'IQD', 'IRR', 'YER', 'JPY', 'KRW', 'IDR', 'XOF', 'XAF', 'CLP'].includes(c) ? 0 : 2);

export default function Portal({ token }: { token: string }) {
  const { t, i18n } = useTranslation();
  const [d, setD] = useState<PortalData | null | 'missing'>(null);
  const [view, setView] = useState<string | null>(null);

  useEffect(() => {
    api<PortalData>(`/portal/${token}`).then(setD).catch(() => setD('missing'));
  }, [token]);

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
          <LangSwitch />
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
            <Group justify="space-between" align="flex-start">
              <div>
                <Text fw={700} size="lg">{rtl && inv.company_name_ar ? inv.company_name_ar : inv.company_name}</Text>
                <Text size="sm">{t('invoices.invoice')} #{inv.number}{inv.period ? ` · ${inv.period}` : ''}</Text>
              </div>
              <div style={{ textAlign: 'end' }}>
                <Text size="sm">{t('invoices.date')}: {formatDate(inv.issue_date)}</Text>
                <Text size="sm">{t('invoices.due')}: {formatDate(inv.due_date)}</Text>
              </div>
            </Group>
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
