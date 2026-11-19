import { printPage } from '../../native';
import { useEffect, useMemo, useState } from 'react';
import {
  ActionIcon, Alert, Badge, Button, Center, Divider, Group, Loader, Modal, Paper, SegmentedControl, Select, SimpleGrid, Stack, Table, Text, TextInput,
} from '@mantine/core';
import { IconPlus, IconPrinter, IconTrash } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { api, type Rate } from '../../api';
import { useCompany } from '../../company';
import { autoApply, formatAmount, formatDate, notifyError, notifySaved, roundChange, today } from '../../util';
import { Confirm } from '../../components/PageHeader';
import { invoiceColor, type InvoiceDetail, type Money, type PaymentDetail, type PaymentMethod, type TaxRate } from '../../billing';
import { WhatsAppButton } from './Messages';
import { DocFooter, DocHeader, itemPrice, SendDocButton, type SaleItem } from '../sales/common';
import { zatcaQR } from '../../zatca';
import Attachments from '../../components/Attachments';
import { ensureBundle, langInfo, languages } from '../../i18n';
import { CustomFieldsBox, EinvoiceButton } from '../fin/Finish';

interface InvoiceLegal {
  company_name?: string; company_tax_id?: string; company_tax_label?: string; company_invoice_title?: string;
  company_mentions?: string; company_einvoice?: string; tax_total?: string; created_at?: string; total: string; kind?: string;
}

/** what the law of the company's country asks on a printed invoice: legal mentions and, in Saudi Arabia, the ZATCA QR code */
export function InvoiceLegal({ inv }: { inv: InvoiceLegal }) {
  const [qr, setQr] = useState('');
  useEffect(() => {
    if (inv.company_einvoice !== 'zatca' || !inv.company_tax_id || inv.kind === 'refund') { setQr(''); return; }
    const ts = new Date(inv.created_at ?? Date.now()).toISOString().replace(/\.\d{3}Z$/, 'Z');
    zatcaQR(inv.company_name ?? '', inv.company_tax_id, ts, Number(inv.total).toFixed(2), Number(inv.tax_total ?? 0).toFixed(2))
      .then(setQr).catch(() => setQr(''));
  }, [inv]);
  if (!qr && !inv.company_mentions) return null;
  return (
    <Group justify="space-between" align="flex-end" mt="sm" wrap="nowrap">
      <Text size="xs" style={{ whiteSpace: 'pre-wrap' }}>{inv.company_mentions}</Text>
      {qr && <img src={qr} alt="ZATCA QR" width={110} height={110} />}
    </Group>
  );
}

export function MoneyList({ items, kind = 'due' }: { items: Money[]; kind?: 'due' | 'credit' }) {
  const { decimalsOf } = useCompany();
  if (!items.length) return <Text span c="dimmed">—</Text>;
  return (
    <Group gap={4} wrap="wrap">
      {items.map((m) => (
        <Badge key={m.currency} variant="light" color={kind === 'due' ? 'red' : 'teal'} style={{ textTransform: 'none' }}>
          <span dir="ltr">{formatAmount(kind === 'due' ? m.due : m.credit, decimalsOf(m.currency))} {m.currency}</span>
        </Badge>
      ))}
    </Group>
  );
}

// ---------- invoice view / print / void ----------

export function InvoiceView({ id, canVoid, onClose, onChanged }: { id: string; canVoid: boolean; onClose: () => void; onChanged?: () => void }) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [d, setD] = useState<InvoiceDetail | null>(null);
  const [confirm, setConfirm] = useState(false);
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  const [credit, setCredit] = useState<{ amount: string; reason: string; refund_method_id: string; mode: string; qty: Record<number, string> } | null>(null);
  const [methods, setMethods] = useState<PaymentMethod[]>([]);

  const reload = () => {
    if (company) api<InvoiceDetail>(`/companies/${company.id}/invoices/${id}`).then(setD).catch((e) => { notifyError(t, e); onClose(); });
  };
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(reload, [company, id]);

  async function openCredit() {
    setCredit({ amount: '', reason: '', refund_method_id: '', mode: 'amount', qty: {} });
    if (company && !methods.length) api<PaymentMethod[]>(`/companies/${company.id}/payment-methods`).then((m) => setMethods(m.filter((x) => x.is_active))).catch(() => {});
  }

  async function doCredit() {
    if (!company || !credit) return;
    setBusy(true);
    try {
      const { mode, qty, ...rest } = credit;
      const lines = Object.entries(qty).filter(([, q]) => Number(q) > 0).map(([n, q]) => ({ line_no: Number(n), quantity: q }));
      await api(`/companies/${company.id}/invoices/${id}/credit-note`, {
        method: 'POST', body: mode === 'lines' ? { ...rest, amount: '', lines } : rest,
      });
      notifySaved(t);
      setCredit(null);
      reload();
      onChanged?.();
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  async function doVoid() {
    if (!company) return;
    setBusy(true);
    try {
      setD(await api<InvoiceDetail>(`/companies/${company.id}/invoices/${id}/void`, { method: 'POST', body: { reason } }));
      notifySaved(t);
      setConfirm(false);
      onChanged?.();
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  const inv = d?.invoice;
  const dec = inv ? decimalsOf(inv.currency) : 2;
  const [printLang, setPrintLang] = useState<string>('');
  useEffect(() => { const l = (d?.invoice as unknown as { customer_locale?: string | null } | undefined)?.customer_locale; if (l && l !== i18n.language) void ensureBundle(l).then(() => setPrintLang(l)); }, [d, i18n.language]);
  const pl = printLang || i18n.language;
  const pt = i18n.getFixedT(pl);

  return (
    <Modal opened onClose={onClose} size="lg" title={inv ? `${inv.kind === 'refund' ? (inv.cheque_id ? t('stmt.kinds.cheque_return') : t('gap.refund')) : t('invoices.invoice')} #${inv.number}` : t('invoices.invoice')}>
      {!d || !inv ? <Center p="xl"><Loader size="sm" /></Center> : (
        <Stack>
          <div className="print-doc" dir={langInfo(pl).dir} lang={pl}>
            <DocHeader c={inv} title={`${inv.kind === 'refund' ? (inv.cheque_id ? pt('stmt.kinds.cheque_return') : pt('gap.refund')) : ((inv as unknown as InvoiceLegal).company_invoice_title || pt('invoices.invoice'))} #${inv.number}`} lines={[
              ...(inv.period ? [inv.period] : []),
              ...(inv.order_number ? [`${pt('sales.order')} SO-${inv.order_number}`] : []),
              `${pt('invoices.date')}: ${formatDate(inv.issue_date)}`,
              `${pt('invoices.due')}: ${formatDate(inv.due_date)}`,
              <Badge key="st" color={invoiceColor[inv.status]} variant="light" className="no-print">{pt(`invoices.statuses.${inv.status}`)}</Badge>,
            ]} />
            <Divider my="sm" />
            <Text fw={600}>{inv.customer_name} <Text span c="dimmed" size="sm">({inv.customer_code})</Text></Text>
            <Text size="sm">{[inv.customer_phone, inv.area_name, inv.customer_address].filter(Boolean).join(' · ')}</Text>
            {inv.meter_no && <Text size="sm">{pt('invoices.meter')}: {inv.meter_no}</Text>}
            <table style={{ marginTop: 12 }}>
              <thead>
                <tr><th>{pt('invoices.description')}</th><th className="num">{pt('invoices.amount')}</th></tr>
              </thead>
              <tbody>
                {d.lines.map((l) => (
                  <tr key={l.line_no}>
                    <td>
                      {l.kind === 'usage' && l.meta?.previous ? (
                        <span dir="ltr">{l.meta.previous} → {l.meta.current} = {l.meta.used} {l.meta.unit} × {formatAmount(l.unit_price, 4).replace(/\.?0+$/, '')}</span>
                      ) : l.description}
                    </td>
                    <td className="num">
                      {formatAmount(l.amount, dec)}
                      {l.tax_amount && Number(l.tax_amount) > 0 && <div style={{ fontSize: 11 }}>+ {l.tax_name}: {formatAmount(l.tax_amount, dec)}</div>}
                    </td>
                  </tr>
                ))}
              </tbody>
              <tfoot>
                <tr><th>{pt('invoices.total')}</th><th className="num">{formatAmount(inv.total, dec)} {inv.currency}</th></tr>
                {inv.local_currency && inv.local_currency_total && (
                  <tr><td>{pt('net.localTotal', { cur: inv.local_currency, rate: Number(inv.local_rate ?? 0).toLocaleString('en-US'), date: formatDate(inv.issue_date) })}</td>
                    <td className="num" style={{ fontWeight: 700 }}>{formatAmount(inv.local_currency_total, decimalsOf(inv.local_currency))} {inv.local_currency}</td></tr>
                )}
                {inv.paid !== '0.0000' && <tr><td>{pt('invoices.paid')}</td><td className="num">{formatAmount(inv.paid, dec)}</td></tr>}
                {inv.status !== 'void' && <tr><th>{pt('invoices.balance')}</th><th className="num">{formatAmount(inv.balance, dec)} {inv.currency}</th></tr>}
              </tfoot>
            </table>
            {inv.status === 'void' && <Text c="red" fw={700} mt="sm">{pt('invoices.statuses.void')} {inv.notes}</Text>}
            <DocFooter c={inv} />
            <InvoiceLegal inv={inv as unknown as InvoiceLegal} />
          </div>
          <div className="mx-row no-print" style={{ gap: 6, flexWrap: 'wrap', alignItems: 'center' }}>
            <label style={{ fontSize: 12 }}>{t('fin.printLang')} <select value={pl} onChange={(e) => { const v = e.currentTarget.value; void ensureBundle(v).then(() => setPrintLang(v)); }}>
              {languages.map((l) => <option key={l.code} value={l.code}>{l.label}</option>)}</select></label>
            <EinvoiceButton invoiceId={inv.id} />
          </div>
          <CustomFieldsBox entity="invoices" id={inv.id} readOnly={!canVoid} />
          <div style={{ display: 'none' }}>
          </div>

          {d.payments.length > 0 && (
            <Paper withBorder p="sm" className="no-print">
              <Text size="sm" fw={500} mb={4}>{t('invoices.payments')}</Text>
              {d.payments.map((p, i) => (
                <Text size="sm" key={i}>#{p.payment_number} · {formatDate(p.payment_date)} · <span dir="ltr">{formatAmount(p.amount, dec)}</span></Text>
              ))}
            </Paper>
          )}
          {confirm && (
            <Stack gap="xs" className="no-print">
              <TextInput label={t('invoices.reason')} value={reason} onChange={(e) => setReason(e.currentTarget.value)} />
              <Confirm text={t('invoices.voidConfirm')} yesLabel={t('invoices.void')} noLabel={t('common.cancel')} busy={busy}
                onYes={doVoid} onNo={() => setConfirm(false)} />
            </Stack>
          )}
          {credit && (
            <Paper withBorder p="sm" className="no-print">
              <Stack gap="xs">
                <Text fw={600} size="sm">{t('gap.creditNoteNew')}</Text>
                <Text size="xs" c="dimmed">{t('gap.creditHint')}</Text>
                <SegmentedControl size="xs" value={credit.mode} onChange={(v) => setCredit({ ...credit, mode: v })}
                  data={[{ value: 'amount', label: t('pos.creditByAmount') }, { value: 'lines', label: t('pos.creditByLine') }]} />
                {credit.mode === 'lines' ? (
                  <Stack gap={4}>
                    {d.lines.map((l) => {
                      const left = Number(l.quantity) - Number(l.credited_quantity ?? 0);
                      return (
                        <Group key={l.line_no} gap="xs" wrap="nowrap" justify="space-between">
                          <Text size="sm" style={{ flex: 1, minWidth: 0 }} truncate>{l.description}</Text>
                          <Text size="xs" c="dimmed" dir="ltr">{t('pos.left')}: {left}</Text>
                          <TextInput w={90} size="xs" dir="ltr" inputMode="decimal" placeholder="0" disabled={left <= 0}
                            value={credit.qty[l.line_no] ?? ''}
                            onChange={(e) => setCredit({ ...credit, qty: { ...credit.qty, [l.line_no]: e.currentTarget.value.replace(/[^\d.]/g, '') } })} />
                        </Group>
                      );
                    })}
                    <TextInput label={t('invoices.reason')} value={credit.reason} onChange={(e) => setCredit({ ...credit, reason: e.currentTarget.value })} />
                  </Stack>
                ) : (
                  <Group grow>
                    <TextInput label={t('gap.creditAmount')} dir="ltr" placeholder={formatAmount(inv.total, dec)} value={credit.amount}
                      rightSection={<Text size="xs" c="dimmed">{inv.currency}</Text>}
                      onChange={(e) => setCredit({ ...credit, amount: e.currentTarget.value.replace(/[^\d.]/g, '') })} />
                    <TextInput label={t('invoices.reason')} value={credit.reason} onChange={(e) => setCredit({ ...credit, reason: e.currentTarget.value })} />
                  </Group>
                )}
                <Select label={t('gap.refundNow')} clearable placeholder={t('gap.keepAsCredit')} value={credit.refund_method_id || null}
                  data={methods.map((m) => ({ value: m.id, label: `${m.name} (${m.currency})` }))}
                  onChange={(v) => setCredit({ ...credit, refund_method_id: v ?? '' })} />
                <Group justify="flex-end">
                  <Button variant="default" size="xs" onClick={() => setCredit(null)}>{t('common.cancel')}</Button>
                  <Button size="xs" color="orange" loading={busy} onClick={doCredit}>{t('gap.creditNote')}</Button>
                </Group>
              </Stack>
            </Paper>
          )}
          <Attachments type="invoice" id={inv.id} />
          <Group justify="space-between" className="no-print">
            <Group gap="xs">
              {canVoid && inv.status !== 'void' && !inv.cheque_id && (inv.paid === '0.0000' || inv.kind === 'refund') && !confirm
                ? <Button variant="subtle" color="red" onClick={() => setConfirm(true)}>{t('invoices.void')}</Button> : null}
              {canVoid && inv.status !== 'void' && inv.kind !== 'refund' && !credit && !confirm
                ? <Button variant="subtle" color="orange" onClick={openCredit}>{t('gap.creditNote')}</Button> : null}
            </Group>
            <Group gap="xs">
              {inv.status !== 'void' && inv.kind !== 'refund' && <SendDocButton kind="invoice" id={inv.id} email={inv.customer_email} />}
              {inv.status !== 'void' && <WhatsAppButton customerId={inv.customer_id} invoiceId={inv.id} event="invoice_issued" />}
              <Button variant="default" onClick={onClose}>{t('common.close')}</Button>
              <Button leftSection={<IconPrinter size={16} />} onClick={() => void printPage()}>{t('invoices.print')}</Button>
            </Group>
          </Group>
        </Stack>
      )}
    </Modal>
  );
}

// ---------- payment receipt ----------

export function PaymentView({ id, canVoid, onClose, onChanged }: { id: string; canVoid: boolean; onClose: () => void; onChanged?: () => void }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const [d, setD] = useState<PaymentDetail | null>(null);
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!company) return;
    api<PaymentDetail>(`/companies/${company.id}/payments/${id}`).then(setD).catch((e) => { notifyError(t, e); onClose(); });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [company, id]);

  async function doVoid() {
    if (!company) return;
    setBusy(true);
    try {
      setD(await api<PaymentDetail>(`/companies/${company.id}/payments/${id}/void`, { method: 'POST', body: { reason: '' } }));
      notifySaved(t);
      setConfirm(false);
      onChanged?.();
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened onClose={onClose} size="md" title={t('payments.receipt')}>
      {!d ? <Center p="xl"><Loader size="sm" /></Center> : (
        <Stack>
          <Receipt d={d} />
          {confirm && (
            <Confirm text={t('payments.voidConfirm')} yesLabel={t('payments.void')} noLabel={t('common.cancel')} busy={busy}
              onYes={doVoid} onNo={() => setConfirm(false)} />
          )}
          <Group justify="space-between" className="no-print">
            {canVoid && d.payment.status === 'posted' && !confirm
              ? <Button variant="subtle" color="red" onClick={() => setConfirm(true)}>{t('payments.void')}</Button> : <span />}
            <Group gap="xs">
              {d.payment.status === 'posted' && <WhatsAppButton customerId={d.payment.customer_id} paymentId={d.payment.id} event="payment_received" />}
              <Button variant="default" onClick={onClose}>{t('common.close')}</Button>
              <Button leftSection={<IconPrinter size={16} />} onClick={() => void printPage()}>{t('invoices.print')}</Button>
            </Group>
          </Group>
        </Stack>
      )}
    </Modal>
  );
}

export function Receipt({ d }: { d: PaymentDetail }) {
  const { t, i18n } = useTranslation();
  const { decimalsOf } = useCompany();
  const p = d.payment;
  const rtl = i18n.language === 'ar';
  return (
    <div className="print-doc receipt" dir={rtl ? 'rtl' : 'ltr'}>
      <Text fw={700} ta="center">{rtl && p.company_name_ar ? p.company_name_ar : p.company_name}</Text>
      <Text ta="center" size="sm">{t('payments.receipt')} #{p.number || '…'} · {formatDate(p.payment_date)}</Text>
      {p.status === 'void' && <Text ta="center" c="red" fw={700}>{t('invoices.statuses.void')}</Text>}
      <Divider my={6} variant="dashed" />
      <Text size="sm" fw={600}>{p.customer_name} ({p.customer_code})</Text>
      <table>
        <tbody>
          {d.tenders.map((x, i) => (
            <tr key={i}>
              <td>{x.method}{x.currency !== p.base_currency && <Text span size="xs" c="dimmed"> @ {x.rate}</Text>}</td>
              <td className="num">{formatAmount(x.amount, decimalsOf(x.currency))} {x.currency}</td>
            </tr>
          ))}
          {p.change_amount !== '0.0000' && p.change_currency && (
            <tr><td>{t('payments.change')}</td><td className="num">-{formatAmount(p.change_amount, decimalsOf(p.change_currency))} {p.change_currency}</td></tr>
          )}
          <tr><th>{t('payments.amount')}</th><th className="num">{formatAmount(p.amount, decimalsOf(p.currency))} {p.currency}</th></tr>
          {d.local && (
            <tr><td>{t('net.localTotal', { cur: d.local.currency, rate: Number(d.local.rate).toLocaleString('en-US'), date: formatDate(d.local.date) })}</td>
              <td className="num">{formatAmount(d.local.amount, decimalsOf(d.local.currency))} {d.local.currency}</td></tr>
          )}
          {d.allocations.map((a, i) => (
            <tr key={'a' + i}><td>{t('invoices.invoice')} #{a.invoice_number}{a.period ? ` (${a.period})` : ''}</td>
              <td className="num">{formatAmount(a.amount, decimalsOf(p.currency))}</td></tr>
          ))}
        </tbody>
      </table>
      <Text size="sm" mt={6}>{t('payments.balanceAfter')}:</Text>
      {d.customer_balance.due.length === 0 && d.customer_balance.credit.length === 0
        ? <Text size="sm" fw={600}>{t('customers.nothingDue')}</Text>
        : <MoneyList items={d.customer_balance.due} />}
      {d.customer_balance.credit.length > 0 && <MoneyList items={d.customer_balance.credit} kind="credit" />}
      <Divider my={6} variant="dashed" />
      <Text size="xs" ta="center">{p.received_by ? `${t('payments.receivedBy')}: ${p.received_by} · ` : ''}{t('payments.thanks')}</Text>
    </div>
  );
}

// ---------- receive payment ----------

interface TenderState { key: number; method_id: string | null; amount: string; rate: string }
let tk = 1;

export function ReceivePayment({ customerId, customerName, due, onClose, onDone }: {
  customerId: string; customerName: string; due: Money[]; onClose: () => void; onDone: (paymentId: string) => void;
}) {
  const { t } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const base = company?.base_currency ?? 'USD';
  const [methods, setMethods] = useState<PaymentMethod[] | null>(null);
  const [rates, setRates] = useState<Record<string, string>>({});
  const [date, setDate] = useState(today());
  const [currency, setCurrency] = useState(due[0]?.currency ?? base);
  // amount to apply follows min(received, due) until the user types a value (partial payments just work)
  const [applyTyped, setApply] = useState<string | null>(null);
  const [tenders, setTenders] = useState<TenderState[]>([{ key: tk++, method_id: null, amount: '', rate: '' }]);
  const [changeMethod, setChangeMethod] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!company) return;
    api<PaymentMethod[]>(`/companies/${company.id}/payment-methods`).then((ms) => {
      const active = ms.filter((m) => m.is_active);
      setMethods(active);
      const first = active.find((m) => m.currency === currency) ?? active[0];
      if (first) setTenders([{ key: tk++, method_id: first.id, amount: '', rate: '' }]);
      const local = active.find((m) => m.kind === 'cash' && m.currency === company.local_currency) ?? active.find((m) => m.kind === 'cash');
      if (local) setChangeMethod(local.id);
    }).catch(() => setMethods([]));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [company]);

  useEffect(() => {
    if (!company) return;
    api<{ rates: Rate[] }>(`/companies/${company.id}/rates/latest?date=${date}`)
      .then((r) => setRates(Object.fromEntries(r.rates.map((x) => [x.currency, x.rate])))).catch(() => {});
  }, [company, date]);

  const byId = useMemo(() => new Map((methods ?? []).map((m) => [m.id, m])), [methods]);
  const rateOf = (cur: string, custom = '') => (cur === base ? 1 : Number(custom || rates[cur] || 0));
  const payRate = rateOf(currency);

  // Preview only (display); the server recomputes everything with exact decimals.
  let received = 0;
  let missingRate = false;
  for (const tn of tenders) {
    const m = tn.method_id ? byId.get(tn.method_id) : undefined;
    const amt = Number(tn.amount || 0);
    if (!m || !amt) continue;
    const r = rateOf(m.currency, tn.rate);
    if (!r || !payRate) { missingRate = true; continue; }
    received += (amt / r) * payRate;
  }
  const apply = applyTyped ?? autoApply(received, Number(due.find((x) => x.currency === currency)?.due ?? 0));
  const applyN = Number(apply || 0);
  const extra = received - (apply ? applyN : received);
  const cm = changeMethod ? byId.get(changeMethod) : undefined;
  const changeRate = cm ? rateOf(cm.currency) : 0;
  const changeDec = cm ? decimalsOf(cm.currency) : 2;
  const change = extra > 0 && cm && changeRate && payRate ? roundChange((extra / payRate) * changeRate, changeDec, cm.change_rounding) : 0;
  const pdec = decimalsOf(currency);
  const short = apply !== '' && applyN - received > 0.01;

  const currencies = [...new Set([...due.map((d) => d.currency), base, company?.local_currency ?? base])];

  async function submit() {
    if (!company) return;
    setBusy(true);
    try {
      const r = await api<PaymentDetail>(`/companies/${company.id}/payments`, {
        method: 'POST',
        body: {
          customer_id: customerId, date, currency, amount: apply,
          tenders: tenders.filter((x) => x.method_id && x.amount).map((x) => ({ method_id: x.method_id, amount: x.amount, rate: x.rate })),
          change_method_id: extra > 0.00005 ? changeMethod ?? '' : '',
        },
      });
      notifySaved(t);
      onDone(r.payment.id);
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  const methodOptions = (methods ?? []).map((m) => ({ value: m.id, label: `${m.name} (${m.currency})` }));

  return (
    <Modal opened onClose={onClose} size="lg" title={`${t('payments.new')} · ${customerName}`}>
      {methods === null ? <Center p="xl"><Loader size="sm" /></Center> : methods.length === 0 ? (
        <Alert color="yellow">{t('payments.noMethods')}</Alert>
      ) : (
        <Stack>
          <Group grow align="flex-end">
            <TextInput type="date" label={t('payments.date')} value={date} onChange={(e) => setDate(e.currentTarget.value)} />
            <Select label={t('payments.currency')} data={currencies} value={currency} allowDeselect={false}
              onChange={(v) => { if (v) { setCurrency(v); setApply(null); } }} />
          </Group>
          {due.length > 0 && <Group gap="xs"><Text size="sm" c="dimmed">{t('payments.owed')}:</Text><MoneyList items={due} /></Group>}

          <Text size="sm" fw={500}>{t('payments.tenders')}</Text>
          {tenders.map((tn) => {
            const m = tn.method_id ? byId.get(tn.method_id) : undefined;
            return (
              <Group key={tn.key} gap="xs" align="flex-end" wrap="nowrap">
                <Select style={{ flex: 2 }} size="sm" data={methodOptions} value={tn.method_id} allowDeselect={false}
                  onChange={(v) => setTenders((ts) => ts.map((x) => (x.key === tn.key ? { ...x, method_id: v, rate: '' } : x)))} />
                <TextInput style={{ flex: 2 }} size="sm" dir="ltr" inputMode="decimal" placeholder="0" value={tn.amount}
                  rightSection={<Text size="xs" c="dimmed">{m?.currency}</Text>} rightSectionWidth={44}
                  styles={{ input: { fontFamily: 'monospace', fontSize: 16 } }} data-autofocus
                  onChange={(e) => { const v = e.currentTarget.value.replace(/[^\d.]/g, ''); setTenders((ts) => ts.map((x) => (x.key === tn.key ? { ...x, amount: v } : x))); }} />
                {m && m.currency !== base && (
                  <TextInput style={{ flex: 1 }} size="sm" dir="ltr" inputMode="decimal" label={t('payments.rate')} value={tn.rate}
                    placeholder={rates[m.currency] ?? '?'} styles={{ input: { fontFamily: 'monospace' } }}
                    onChange={(e) => { const v = e.currentTarget.value.replace(/[^\d.]/g, ''); setTenders((ts) => ts.map((x) => (x.key === tn.key ? { ...x, rate: v } : x))); }} />
                )}
                <ActionIcon variant="subtle" color="gray" mb={4} disabled={tenders.length === 1}
                  onClick={() => setTenders((ts) => ts.filter((x) => x.key !== tn.key))}><IconTrash size={16} /></ActionIcon>
              </Group>
            );
          })}
          <Button variant="subtle" size="xs" leftSection={<IconPlus size={14} />} style={{ alignSelf: 'flex-start' }}
            onClick={() => {
              const used = new Set(tenders.map((x) => x.method_id));
              const next = (methods ?? []).find((m) => !used.has(m.id));
              setTenders((ts) => [...ts, { key: tk++, method_id: next?.id ?? null, amount: '', rate: '' }]);
            }}>{t('payments.addTender')}</Button>

          <Paper withBorder p="md" bg="var(--mantine-color-gray-0)">
            <SimpleGrid cols={{ base: 1, xs: 3 }}>
              <div>
                <Text size="xs" c="dimmed">{t('payments.received')}</Text>
                <Text fw={700} size="lg" dir="ltr">{formatAmount(received, pdec)} {currency}</Text>
              </div>
              <TextInput label={t('payments.toApply')} dir="ltr" inputMode="decimal" value={apply} error={short ? ' ' : undefined}
                rightSection={<Text size="xs" c="dimmed">{currency}</Text>} rightSectionWidth={44}
                styles={{ input: { fontFamily: 'monospace', fontWeight: 600 } }}
                onChange={(e) => setApply(e.currentTarget.value.replace(/[^\d.]/g, ''))} />
              <div>
                <Text size="xs" c="dimmed">{t('payments.change')}</Text>
                <Text fw={700} size="lg" c={change > 0 ? 'orange.8' : undefined} dir="ltr">
                  {cm ? `${formatAmount(change, changeDec)} ${cm.currency}` : '—'}
                </Text>
              </div>
            </SimpleGrid>
            {extra > 0.00005 && (
              <Select mt="sm" size="xs" label={t('payments.changeIn')} value={changeMethod} onChange={setChangeMethod} allowDeselect={false}
                data={(methods ?? []).filter((m) => m.kind === 'cash').map((m) => ({ value: m.id, label: `${m.name} (${m.currency})` }))} />
            )}
            {missingRate && <Text size="xs" c="red" mt="xs">{t('errors.no_rate', { detail: '' })}</Text>}
          </Paper>

          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
            <Button size="md" loading={busy} disabled={received <= 0 || short || missingRate} onClick={submit}>{t('payments.new')}</Button>
          </Group>
        </Stack>
      )}
    </Modal>
  );
}

// ---------- manual invoice ----------

interface ManualLine { key: number; description: string; quantity: string; unit_price: string; item_id?: string | null; tax_rate_id?: string | null }

export function NewInvoice({ customerId, customerName, onClose, onDone }: {
  customerId: string; customerName: string; onClose: () => void; onDone: (id: string) => void;
}) {
  const { t } = useTranslation();
  const { company, currencies } = useCompany();
  const [currency, setCurrency] = useState<string | null>(company?.base_currency ?? 'USD');
  const [date, setDate] = useState(today());
  const [lines, setLines] = useState<ManualLine[]>([{ key: tk++, description: '', quantity: '1', unit_price: '' }]);
  const [busy, setBusy] = useState(false);
  const [items, setItems] = useState<SaleItem[]>([]);
  const [taxes, setTaxes] = useState<TaxRate[]>([]);
  useEffect(() => {
    if (company) api<SaleItem[]>(`/companies/${company.id}/items`).then((x) => setItems(x.filter((i) => i.is_active && i.is_sellable !== false))).catch(() => {});
    if (company) api<TaxRate[]>(`/companies/${company.id}/tax-rates`).then((x) => setTaxes(x.filter((i) => i.is_active && i.applies_to !== 'purchases'))).catch(() => {});
  }, [company]);
  const rateOf = (id?: string | null) => Number(taxes.find((x) => x.id === id)?.rate ?? 0);
  const total = lines.reduce((s, l) => { const a = Number(l.quantity || 0) * Number(l.unit_price || 0); return s + a + a * rateOf(l.tax_rate_id) / 100; }, 0);

  async function submit() {
    if (!company) return;
    setBusy(true);
    try {
      const r = await api<InvoiceDetail>(`/companies/${company.id}/invoices`, {
        method: 'POST',
        body: { customer_id: customerId, currency, issue_date: date, due_date: date,
          lines: lines.filter((l) => l.description && l.unit_price).map(({ description, quantity, unit_price, item_id, tax_rate_id }) => ({ description, quantity, unit_price, item_id: item_id ?? '', tax_rate_id: tax_rate_id ?? '' })) },
      });
      notifySaved(t);
      onDone(r.invoice.id);
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened onClose={onClose} size="lg" title={`${t('customers.newInvoice')} · ${customerName}`}>
      <Stack>
        <Group grow>
          <TextInput type="date" label={t('invoices.date')} value={date} onChange={(e) => setDate(e.currentTarget.value)} />
          <Select label={t('plans.currency')} searchable data={currencies.map((c) => c.code)} value={currency} onChange={setCurrency} allowDeselect={false} />
        </Group>
        <Table>
          <Table.Thead>
            <Table.Tr><Table.Th>{t('invoices.description')}</Table.Th><Table.Th w={90}>{t('invoices.qty')}</Table.Th><Table.Th w={130}>{t('invoices.price')}</Table.Th><Table.Th w={40} /></Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {lines.map((l) => (
              <Table.Tr key={l.key}>
                <Table.Td>
                  <Stack gap={2}>
                    {items.length > 0 && (
                      <Select size="xs" searchable clearable placeholder={t('pur.item')} value={l.item_id ?? null} comboboxProps={{ withinPortal: true }}
                        data={items.map((i) => ({ value: i.id, label: `${i.name}${i.kind === 'stock' ? ` (${Number(i.quantity)} ${i.unit})` : ''}` }))}
                        onChange={(v) => {
                          const it = items.find((i) => i.id === v);
                          setLines((ls) => ls.map((x) => (x.key === l.key ? {
                            ...x, item_id: v, description: it ? it.name : x.description,
                            unit_price: it ? itemPrice(it, currency ?? company?.base_currency ?? 'USD', company?.base_currency ?? 'USD') || x.unit_price : x.unit_price,
                            tax_rate_id: it?.sale_tax_rate_id ?? x.tax_rate_id,
                          } : x)));
                        }} />
                    )}
                    <TextInput size="xs" value={l.description} onChange={(e) => { const v = e.currentTarget.value; setLines((ls) => ls.map((x) => (x.key === l.key ? { ...x, description: v } : x))); }} />
                    {taxes.length > 0 && (
                      <Select size="xs" clearable placeholder={t('net.noTax')} value={l.tax_rate_id ?? null} comboboxProps={{ withinPortal: true }}
                        data={taxes.map((x) => ({ value: x.id, label: x.name }))}
                        onChange={(v) => setLines((ls) => ls.map((x) => (x.key === l.key ? { ...x, tax_rate_id: v } : x)))} />
                    )}
                  </Stack>
                </Table.Td>
                <Table.Td><TextInput size="xs" dir="ltr" value={l.quantity} onChange={(e) => { const v = e.currentTarget.value.replace(/[^\d.]/g, ''); setLines((ls) => ls.map((x) => (x.key === l.key ? { ...x, quantity: v } : x))); }} /></Table.Td>
                <Table.Td><TextInput size="xs" dir="ltr" value={l.unit_price} onChange={(e) => { const v = e.currentTarget.value.replace(/[^\d.]/g, ''); setLines((ls) => ls.map((x) => (x.key === l.key ? { ...x, unit_price: v } : x))); }} /></Table.Td>
                <Table.Td><ActionIcon variant="subtle" color="gray" disabled={lines.length === 1} onClick={() => setLines((ls) => ls.filter((x) => x.key !== l.key))}><IconTrash size={14} /></ActionIcon></Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
        <Group justify="space-between">
          <Button variant="subtle" size="xs" leftSection={<IconPlus size={14} />} onClick={() => setLines((ls) => [...ls, { key: tk++, description: '', quantity: '1', unit_price: '' }])}>{t('invoices.addLine')}</Button>
          <Text fw={700} dir="ltr">{formatAmount(total, 2)} {currency}</Text>
        </Group>
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
          <Button loading={busy} disabled={total <= 0} onClick={submit}>{t('common.save')}</Button>
        </Group>
      </Stack>
    </Modal>
  );
}
