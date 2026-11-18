import { useCallback, useEffect, useState } from 'react';
import { Button, Center, Checkbox, Divider, Group, Loader, Paper, SegmentedControl, Select, Stack, Table, Text, TextInput } from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { IconArrowLeft, IconPrinter, IconSend } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { api, type Me } from '../../api';
import { useCompany } from '../../company';
import { printPage } from '../../native';
import { formatAmount, formatDate, notifyError, today } from '../../util';
import PageHeader from '../../components/PageHeader';
import ExportMenu from '../../components/ExportMenu';
import type { Area } from '../../billing';
import { DocFooter, DocHeader, SendDocButton, type DocCompany } from './common';

export interface StmtLine { date: string; kind: string; number: number; ref: string; debit: string; credit: string; balance: string; doc_id: string }
export interface StmtCurrency { currency: string; opening: string; debits: string; credits: string; closing: string; lines: StmtLine[] }
interface StmtCustomer { id: string; code: string; name: string; phone: string; email: string; address: string; area_name: string | null }

function monthsAgo(n: number): string {
  const d = new Date();
  d.setMonth(d.getMonth() - n);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-01`;
}

export function useDocCompany(): DocCompany | null {
  const { company } = useCompany();
  const [c, setC] = useState<DocCompany | null>(null);
  useEffect(() => {
    if (!company) return;
    api<{ name_en: string; name_ar: string; logo: string; doc_header: string; doc_footer: string; tax_id: string }>(`/companies/${company.id}/documents/settings`)
      .then((s) => setC({ company_name: s.name_en, company_name_ar: s.name_ar, company_logo: s.logo, company_header: s.doc_header,
        company_footer: s.doc_footer, company_tax_id: s.tax_id }))
      .catch(() => setC({ company_name: company.name_en, company_name_ar: company.name_ar }));
  }, [company]);
  return c;
}

/** One statement (per currency): opening balance, movements with running balance, closing balance. */
export function StatementDoc({ c, customer, from, to, currencies, decimalsOf }: {
  c: DocCompany; customer: Pick<StmtCustomer, 'name' | 'code'> & Partial<StmtCustomer>; from: string; to: string; currencies: StmtCurrency[]; decimalsOf: (c: string) => number;
}) {
  const { t, i18n } = useTranslation();
  return (
    <div dir={i18n.language === 'ar' ? 'rtl' : 'ltr'}>
      <DocHeader c={c} title={t('stmt.title')} lines={[`${formatDate(from)} – ${formatDate(to)}`]} />
      <Divider my="sm" />
      <Text fw={600}>{customer.name} <Text span c="dimmed" size="sm">({customer.code})</Text></Text>
      <Text size="sm">{[customer.phone, customer.area_name, customer.address].filter(Boolean).join(' · ')}</Text>
      {currencies.length === 0 && <Text mt="md">{t('stmt.nothing')}</Text>}
      {currencies.map((cur) => {
        const dec = decimalsOf(cur.currency);
        return (
          <table style={{ marginTop: 12 }} key={cur.currency}>
            <thead><tr>
              <th>{t('invoices.date')}</th><th>{t('stmt.document')}</th>
              <th className="num">{t('stmt.debit')}</th><th className="num">{t('stmt.credit')}</th><th className="num">{t('stmt.balance')} ({cur.currency})</th>
            </tr></thead>
            <tbody>
              <tr><td>{formatDate(from)}</td><td>{t('stmt.opening')}</td><td /><td /><td className="num">{formatAmount(cur.opening, dec)}</td></tr>
              {cur.lines.map((l, i) => (
                <tr key={i}>
                  <td>{formatDate(l.date)}</td>
                  <td>{t(`stmt.kinds.${l.kind}`, { defaultValue: l.kind })} #{l.number}{l.ref ? ` · ${l.ref}` : ''}</td>
                  <td className="num">{Number(l.debit) ? formatAmount(l.debit, dec) : ''}</td>
                  <td className="num">{Number(l.credit) ? formatAmount(l.credit, dec) : ''}</td>
                  <td className="num">{formatAmount(l.balance, dec)}</td>
                </tr>
              ))}
            </tbody>
            <tfoot>
              <tr><td colSpan={2}>{t('stmt.totals')}</td><td className="num">{formatAmount(cur.debits, dec)}</td><td className="num">{formatAmount(cur.credits, dec)}</td><td /></tr>
              <tr><th colSpan={4}>{Number(cur.closing) < 0 ? t('stmt.closingCredit') : t('stmt.closing')}</th>
                <th className="num">{formatAmount(cur.closing, dec)} {cur.currency}</th></tr>
            </tfoot>
          </table>
        );
      })}
      <DocFooter c={c} />
    </div>
  );
}

/** #/statement/<customer id> */
export function CustomerStatement({ id }: { id: string }) {
  const { t } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const docC = useDocCompany();
  const [from, setFrom] = useState(monthsAgo(3));
  const [to, setTo] = useState(today());
  const [d, setD] = useState<{ customer: StmtCustomer; currencies: StmtCurrency[] } | null>(null);
  useEffect(() => {
    if (!company || !id) return;
    setD(null);
    api<typeof d>(`/companies/${company.id}/customers/${id}/statement?from=${from}&to=${to}`).then(setD).catch((e) => notifyError(t, e));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [company, id, from, to]);
  if (!company) return null;
  return (
    <Stack gap="lg" maw={1000}>
      <PageHeader title={t('stmt.title')} subtitle={d?.customer.name}>
        <Button variant="subtle" leftSection={<IconArrowLeft size={16} />} component="a" href={`#/customer/${id}`}>{t('customers.title')}</Button>
        {d && <SendDocButton kind="statement" id={id} from={from} to={to} email={d.customer.email} />}
        {d && <ExportMenu table={() => ({
          title: `${t('stmt.title')} ${d.customer.name}`, subtitle: `${from} – ${to}`,
          columns: [{ label: t('pur.currency') }, { label: t('invoices.date') }, { label: t('stmt.document') }, { label: t('stmt.debit'), numeric: true },
            { label: t('stmt.credit'), numeric: true }, { label: t('stmt.balance'), numeric: true }],
          rows: d.currencies.flatMap((c) => [[c.currency, from, t('stmt.opening'), '', '', c.opening],
            ...c.lines.map((l) => [c.currency, l.date, `${t(`stmt.kinds.${l.kind}`, { defaultValue: l.kind })} #${l.number}`, l.debit, l.credit, l.balance]),
            [c.currency, to, t('stmt.closing'), c.debits, c.credits, c.closing]]),
        })} />}
        <Button leftSection={<IconPrinter size={16} />} onClick={() => void printPage()}>{t('invoices.print')}</Button>
      </PageHeader>
      <Group className="no-print">
        <TextInput type="date" label={t('reports.from')} value={from} onChange={(e) => setFrom(e.currentTarget.value)} />
        <TextInput type="date" label={t('reports.to')} value={to} onChange={(e) => setTo(e.currentTarget.value)} />
      </Group>
      <Paper withBorder p="lg">
        {!d || !docC ? <Center p="xl"><Loader size="sm" /></Center> : (
          <div className="print-doc"><StatementDoc c={docC} customer={d.customer} from={from} to={to} currencies={d.currencies} decimalsOf={decimalsOf} /></div>
        )}
      </Paper>
    </Stack>
  );
}

/** #/statements — statements of many customers: print all, or send all by WhatsApp / SMS / e-mail. */
export default function Statements({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const docC = useDocCompany();
  const [from, setFrom] = useState(monthsAgo(1));
  const [to, setTo] = useState(today());
  const [areas, setAreas] = useState<Area[]>([]);
  const [area, setArea] = useState<string | null>(null);
  const [onlyBalance, setOnlyBalance] = useState(true);
  const [rows, setRows] = useState<{ customer: StmtCustomer; currencies: StmtCurrency[] }[] | null>(null);
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [channel, setChannel] = useState('whatsapp');
  const [busy, setBusy] = useState(false);
  const [printing, setPrinting] = useState(false);

  useEffect(() => {
    if (company) api<Area[]>(`/companies/${company.id}/areas`).then(setAreas).catch(() => {});
  }, [company]);
  const load = useCallback(() => {
    if (!company) return;
    setRows(null);
    api<{ rows: { customer: StmtCustomer; currencies: StmtCurrency[] }[] }>(
      `/companies/${company.id}/statements?from=${from}&to=${to}&area_id=${area ?? ''}&only_balance=${onlyBalance ? 1 : 0}`)
      .then((r) => { setRows(r.rows); setPicked(new Set(r.rows.map((x) => x.customer.id))); }).catch((e) => notifyError(t, e));
  }, [company, from, to, area, onlyBalance, t]);
  useEffect(load, [load]);

  async function send() {
    if (!company) return;
    setBusy(true);
    try {
      const r = await api<{ queued: number; skipped: { customer: string; reason: string }[]; has_provider: boolean }>(`/companies/${company.id}/statements/send`, {
        method: 'POST', body: { from, to, channel, locale: i18n.language, customer_ids: [...picked] },
      });
      notifications.show({ color: r.has_provider ? 'teal' : 'orange', autoClose: 8000,
        message: `${t('msg.queuedN', { n: r.queued })}${r.skipped.length ? ` · ${t('msg.skipped')}: ${r.skipped.length}` : ''}${r.has_provider ? '' : ` · ${t('msg.noProvider')}`}` });
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  async function printAll() {
    setPrinting(true);
    await new Promise((r) => setTimeout(r, 100));
    await printPage();
    setPrinting(false);
  }

  if (!company) return null;
  const canSend = me.role !== 'read_only';
  const chosen = (rows ?? []).filter((r) => picked.has(r.customer.id));
  return (
    <Stack gap="lg" maw={1100}>
      <PageHeader title={t('stmt.bulk')} subtitle={company.name_en}>
        <Button variant="default" leftSection={<IconPrinter size={16} />} disabled={!chosen.length} onClick={() => void printAll()}>{t('stmt.printAll', { n: chosen.length })}</Button>
      </PageHeader>
      <Paper withBorder p="md" className="no-print">
        <Group align="flex-end" wrap="wrap">
          <TextInput type="date" label={t('reports.from')} value={from} onChange={(e) => setFrom(e.currentTarget.value)} />
          <TextInput type="date" label={t('reports.to')} value={to} onChange={(e) => setTo(e.currentTarget.value)} />
          <Select label={t('customers.area')} clearable placeholder={t('readings.allAreas')} value={area} onChange={setArea}
            data={areas.map((a) => ({ value: a.id, label: a.name }))} />
          <Checkbox label={t('stmt.onlyBalance')} checked={onlyBalance} onChange={(e) => setOnlyBalance(e.currentTarget.checked)} mb={8} />
        </Group>
        {canSend && (
          <Group mt="md" gap="sm">
            <SegmentedControl value={channel} onChange={setChannel} data={[{ value: 'whatsapp', label: 'WhatsApp' }, { value: 'sms', label: 'SMS' }, { value: 'email', label: 'E-mail' }]} />
            <Button leftSection={<IconSend size={16} />} loading={busy} disabled={!chosen.length} onClick={() => void send()}>{t('stmt.sendAll', { n: chosen.length })}</Button>
          </Group>
        )}
      </Paper>
      <Paper withBorder className="no-print">
        {!rows ? <Center p="xl"><Loader size="sm" /></Center> : rows.length === 0 ? <Text c="dimmed" ta="center" p="xl">{t('common.empty')}</Text> : (
          <Table.ScrollContainer minWidth={700}>
            <Table verticalSpacing="xs">
              <Table.Thead><Table.Tr>
                <Table.Th w={40}><Checkbox checked={picked.size === rows.length} indeterminate={picked.size > 0 && picked.size < rows.length}
                  onChange={(e) => setPicked(e.currentTarget.checked ? new Set(rows.map((r) => r.customer.id)) : new Set())} /></Table.Th>
                <Table.Th>{t('invoices.customer')}</Table.Th><Table.Th>{t('customers.phone')}</Table.Th>
                <Table.Th style={{ textAlign: 'end' }}>{t('stmt.opening')}</Table.Th><Table.Th style={{ textAlign: 'end' }}>{t('stmt.closing')}</Table.Th>
              </Table.Tr></Table.Thead>
              <Table.Tbody>
                {rows.map((r) => (
                  <Table.Tr key={r.customer.id}>
                    <Table.Td><Checkbox checked={picked.has(r.customer.id)} onChange={(e) => {
                      const on = e.currentTarget.checked;
                      setPicked((p) => { const n = new Set(p); if (on) n.add(r.customer.id); else n.delete(r.customer.id); return n; });
                    }} /></Table.Td>
                    <Table.Td><Text size="sm" fw={500} component="a" href={`#/statement/${r.customer.id}`}>{r.customer.name}</Text>
                      <Text size="xs" c="dimmed">{r.customer.code}{r.customer.area_name ? ` · ${r.customer.area_name}` : ''}</Text></Table.Td>
                    <Table.Td dir="ltr"><Text size="sm">{r.customer.phone}</Text><Text size="xs" c="dimmed">{r.customer.email}</Text></Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{r.currencies.map((c) => `${formatAmount(c.opening, decimalsOf(c.currency))} ${c.currency}`).join(' + ')}</Table.Td>
                    <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }} fw={600}>{r.currencies.map((c) => `${formatAmount(c.closing, decimalsOf(c.currency))} ${c.currency}`).join(' + ')}</Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </Paper>
      {printing && docC && (
        <div className="print-doc">
          {chosen.map((r) => (
            <div key={r.customer.id} style={{ breakAfter: 'page', pageBreakAfter: 'always', marginBottom: 24 }}>
              <StatementDoc c={docC} customer={r.customer} from={from} to={to} currencies={r.currencies} decimalsOf={decimalsOf} />
            </div>
          ))}
        </div>
      )}
    </Stack>
  );
}
