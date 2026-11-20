import { printPage } from '../../native';
import { Suspense, lazy, useCallback, useEffect, useMemo, useState } from 'react';
import {
  ActionIcon, Alert, Badge, Box, Button, Card, Center, Container, Group, Loader, Menu, Modal, Paper, SegmentedControl, Select,
  SimpleGrid, Stack, Tabs, Text, TextInput,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import {
  IconBluetooth, IconCloudOff, IconCloudUpload, IconDownload, IconLogout, IconMenu2, IconPlus, IconPrinter, IconSearch, IconTrash,
} from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { api, type Me, type Rate } from '../../api';
import { useCompany } from '../../company';
import { applyLang, languages } from '../../i18n';
import { autoApply, formatAmount, formatDate, notifyError, roundChange, today } from '../../util';
import Logo from '../../components/Logo';
import StatusBar from '../../components/StatusBar';
import '../../magic.css';
import type { Customer, Money, PaymentDetail, PaymentMethod } from '../../billing';
import { MoneyList, Receipt } from '../billing/docs';
import Readings from '../billing/Readings';
import {
  bluetoothSupported, ChoosePrinterError, connectNativePrinter, connectPrinter, isNativeApp, listPairedPrinters, printerName, printLines,
  setPaper, paperWidth, type PairedPrinter, type PrintLine,
} from '../../bt';
import { cacheGet, cacheSet, enqueue, newClientRef, outbox, syncOutbox } from '../../outbox';

const DriverTrips = lazy(() => import('../wh/Driver').then((m) => ({ default: m.DriverTrips })));

function useOnline() {
  const [on, setOn] = useState(navigator.onLine);
  useEffect(() => {
    const up = () => setOn(true);
    const down = () => setOn(false);
    window.addEventListener('online', up);
    window.addEventListener('offline', down);
    return () => { window.removeEventListener('online', up); window.removeEventListener('offline', down); };
  }, []);
  return on;
}

function useOutboxCount() {
  const [n, setN] = useState(outbox().length);
  useEffect(() => {
    const f = () => setN(outbox().length);
    window.addEventListener('paxve:outbox', f);
    return () => window.removeEventListener('paxve:outbox', f);
  }, []);
  return n;
}

export default function CollectorApp({ me, onLogout }: { me: Me; onLogout: () => void }) {
  const { t } = useTranslation();
  const { company, companies, setCompanyId } = useCompany();
  const online = useOnline();
  const pending = useOutboxCount();
  const [customers, setCustomers] = useState<Customer[] | null>(null);
  const [methods, setMethods] = useState<PaymentMethod[]>([]);
  const [rates, setRates] = useState<Record<string, string>>({});
  const [q, setQ] = useState('');
  const [onlyDue, setOnlyDue] = useState('due');
  const [selected, setSelected] = useState<Customer | null>(null);
  const [receipt, setReceipt] = useState<PaymentDetail | null>(null);
  const [printer, setPrinter] = useState(printerName());
  const [pickerOpen, setPickerOpen] = useState(false);
  const cid = company?.id;

  const load = useCallback(() => {
    if (!cid) return;
    if (!navigator.onLine) {
      setCustomers(cacheGet<Customer[]>(`customers.${cid}`)?.value ?? []);
      setMethods(cacheGet<PaymentMethod[]>(`methods.${cid}`)?.value ?? []);
      setRates(cacheGet<Record<string, string>>(`rates.${cid}`)?.value ?? {});
      return;
    }
    api<Customer[]>(`/companies/${cid}/customers?limit=1000`).then((c) => { setCustomers(c); cacheSet(`customers.${cid}`, c); })
      .catch(() => setCustomers(cacheGet<Customer[]>(`customers.${cid}`)?.value ?? []));
    api<PaymentMethod[]>(`/companies/${cid}/payment-methods`).then((m) => { const a = m.filter((x) => x.is_active); setMethods(a); cacheSet(`methods.${cid}`, a); }).catch(() => {});
    api<{ rates: Rate[] }>(`/companies/${cid}/rates/latest`).then((r) => {
      const map = Object.fromEntries(r.rates.map((x) => [x.currency, x.rate]));
      setRates(map);
      cacheSet(`rates.${cid}`, map);
    }).catch(() => {});
  }, [cid]);
  useEffect(load, [load]);

  // send queued payments whenever we are online
  useEffect(() => {
    if (!online) return;
    const run = () => syncOutbox().then((n) => {
      if (n > 0) { notifications.show({ color: 'teal', message: t('collector.synced', { n }) }); load(); }
    });
    void run();
    const id = window.setInterval(run, 30000);
    return () => window.clearInterval(id);
  }, [online, load, t]);

  const list = useMemo(() => {
    const s = q.trim().toLowerCase();
    return (customers ?? []).filter((c) =>
      (onlyDue === 'all' || c.due.length > 0) &&
      (!s || c.name.toLowerCase().includes(s) || c.code.toLowerCase().startsWith(s) || c.phone.includes(s)));
  }, [customers, q, onlyDue]);

  async function pickPrinter() {
    if (isNativeApp()) {
      setPickerOpen(true);
      return;
    }
    try {
      setPrinter(await connectPrinter());
    } catch (e) {
      notifications.show({ color: 'red', message: String((e as Error).message ?? e) });
    }
  }

  if (!company) return <Center h="100vh"><Loader /></Center>;

  return (
    <Box bg="var(--mantine-color-gray-0)" mih="100vh">
      <Paper shadow="xs" radius={0} px="sm" py={8} style={{ position: 'sticky', top: 0, zIndex: 10 }}>
        <Group justify="space-between" wrap="nowrap">
          <Group gap={6} wrap="nowrap" style={{ minWidth: 0 }}>
            <Logo />
            {companies.length > 1 && (
              <select aria-label={t('mx.company')} value={company.id} style={{ maxWidth: 160 }}
                onChange={(e) => { setCustomers(null); setCompanyId(e.currentTarget.value); }}>
                {companies.map((c) => <option key={c.id} value={c.id}>{c.display ?? c.name_en}</option>)}
              </select>
            )}
            {online ? (pending > 0 && <Badge color="orange" leftSection={<IconCloudUpload size={12} />}>{pending}</Badge>)
              : <Badge color="red" leftSection={<IconCloudOff size={12} />}>{t('collector.offline')}</Badge>}
          </Group>
          <Group gap={4} wrap="nowrap">
            {bluetoothSupported() && (
              <ActionIcon variant={printer ? 'filled' : 'light'} color={printer ? 'teal' : 'gray'} size="lg" onClick={pickPrinter}
                aria-label={t('collector.printer')}><IconBluetooth size={18} /></ActionIcon>
            )}
            <Menu position="bottom-end">
              <Menu.Target><ActionIcon variant="light" color="gray" size="lg"><IconMenu2 size={18} /></ActionIcon></Menu.Target>
              <Menu.Dropdown>
                <Menu.Label>{me.full_name} · {company.display ?? company.name_en}</Menu.Label>
                {languages.map((l) => <Menu.Item key={l.code} onClick={() => applyLang(l.code)}>{l.label}</Menu.Item>)}
                <Menu.Divider />
                <Menu.Label>{t('collector.paper')}</Menu.Label>
                <Menu.Item onClick={() => setPaper('58')}>58 mm {paperWidth() === 384 && '✓'}</Menu.Item>
                <Menu.Item onClick={() => setPaper('80')}>80 mm {paperWidth() === 576 && '✓'}</Menu.Item>
                {!isNativeApp() && (
                  <>
                    <Menu.Divider />
                    <Menu.Item component="a" href="/download/paxve.apk" leftSection={<IconDownload size={14} />}>{t('collector.downloadApp')}</Menu.Item>
                  </>
                )}
                <Menu.Divider />
                <Menu.Item color="red" leftSection={<IconLogout size={14} />} onClick={async () => {
                  try { await api('/auth/logout', { method: 'POST' }); } finally { onLogout(); }
                }}>{t('common.logout')}</Menu.Item>
              </Menu.Dropdown>
            </Menu>
          </Group>
        </Group>
      </Paper>

      <Container size="sm" px="sm" py="sm">
        <Tabs defaultValue="customers" keepMounted={false}>
          <Tabs.List grow mb="sm">
            <Tabs.Tab value="customers">{t('bill.customers')}</Tabs.Tab>
            <Tabs.Tab value="day">{t('collector.myDay')}</Tabs.Tab>
            <Tabs.Tab value="readings">{t('bill.readings')}</Tabs.Tab>
        <Tabs.Tab value="trips">{t('wh.drv.tab')}</Tabs.Tab>
          </Tabs.List>

          <Tabs.Panel value="customers">
            <Stack gap="xs">
              <TextInput size="md" leftSection={<IconSearch size={18} />} placeholder={t('customers.search')} value={q}
                onChange={(e) => setQ(e.currentTarget.value)} />
              <SegmentedControl fullWidth value={onlyDue} onChange={setOnlyDue}
                data={[{ value: 'due', label: t('invoices.unpaid') }, { value: 'all', label: t('common.all') }]} />
              {customers === null ? <Center p="xl"><Loader /></Center> : customers.length === 0 && me.role === 'collector' ? (
                <Alert color="yellow" title={t('collector.noAreasTitle')}>{t('collector.noAreasHint')}</Alert>
              ) : list.length === 0 ? (
                <Text c="dimmed" ta="center" p="xl">{t('common.empty')}</Text>
              ) : list.slice(0, 200).map((c) => (
                <Card key={c.id} withBorder padding="sm" onClick={() => setSelected(c)} style={{ cursor: 'pointer' }}>
                  <Group justify="space-between" wrap="nowrap" align="flex-start">
                    <div style={{ minWidth: 0 }}>
                      <Text fw={600} truncate>{c.name}</Text>
                      <Text size="xs" c="dimmed">#{c.code}{c.area_name ? ` · ${c.area_name}` : ''}{c.phone ? ` · ${c.phone}` : ''}</Text>
                    </div>
                    {c.due.length > 0 ? <MoneyList items={c.due} /> : <Badge color="teal" variant="light">{t('customers.nothingDue')}</Badge>}
                  </Group>
                </Card>
              ))}
            </Stack>
          </Tabs.Panel>

          <Tabs.Panel value="day"><MyDay me={me} /></Tabs.Panel>
          <Tabs.Panel value="readings"><Readings me={me} /></Tabs.Panel>
        <Tabs.Panel value="trips"><Suspense fallback={null}><DriverTrips /></Suspense></Tabs.Panel>
        </Tabs>
      </Container>

      {selected && (
        <Collect customer={selected} methods={methods} rates={rates} online={online} me={me}
          onClose={() => setSelected(null)}
          onDone={(r) => { setSelected(null); setReceipt(r); load(); }} />
      )}
      {receipt && <ReceiptSheet d={receipt} onClose={() => setReceipt(null)} onNeedPrinter={() => setPickerOpen(true)} />}
      {pickerOpen && <PrinterPicker onClose={() => setPickerOpen(false)} onConnected={(n) => { setPrinter(n); setPickerOpen(false); }} />}
      <StatusBar me={me} keys={false} sticky />
    </Box>
  );
}

// ---- receipt lines for the thermal printer ----

export function receiptLines(d: PaymentDetail, t: (k: string, o?: Record<string, unknown>) => string, dec: (c: string) => number): PrintLine[] {
  const p = d.payment;
  const rtl = document.dir === 'rtl';
  const L: PrintLine[] = [
    { text: rtl && p.company_name_ar ? p.company_name_ar : p.company_name, bold: true, size: 30, align: 'center' },
    { text: `${t('payments.receipt')} #${p.number || '…'} · ${formatDate(p.payment_date)}`, align: 'center', size: 22 },
    { text: '', rule: true },
    { text: `${p.customer_name} (${p.customer_code})`, bold: true },
  ];
  for (const x of d.tenders) L.push({ text: x.method, right: `${formatAmount(x.amount, dec(x.currency))} ${x.currency}` });
  if (p.change_amount && Number(p.change_amount) > 0 && p.change_currency) {
    L.push({ text: t('payments.change'), right: `-${formatAmount(p.change_amount, dec(p.change_currency))} ${p.change_currency}` });
  }
  L.push({ text: t('payments.amount'), right: `${formatAmount(p.amount, dec(p.currency))} ${p.currency}`, bold: true, size: 28 });
  for (const a of d.allocations) L.push({ text: `${t('invoices.invoice')} #${a.invoice_number}${a.period ? ` (${a.period})` : ''}`, right: formatAmount(a.amount, dec(p.currency)), size: 20 });
  L.push({ text: '', rule: true });
  const due = d.customer_balance.due;
  L.push({ text: t('payments.balanceAfter'), right: due.length ? due.map((m) => `${formatAmount(m.due, dec(m.currency))} ${m.currency}`).join(' + ') : '0', size: 22 });
  if (p.status === 'void') L.push({ text: t('invoices.statuses.void'), bold: true, align: 'center' });
  if (!p.number) L.push({ text: t('collector.pendingSync'), align: 'center', size: 20 });
  L.push({ text: '', rule: true });
  L.push({ text: `${p.received_by ?? ''} · ${t('payments.thanks')}`, align: 'center', size: 20 });
  return L;
}

function ReceiptSheet({ d, onClose, onNeedPrinter }: { d: PaymentDetail; onClose: () => void; onNeedPrinter: () => void }) {
  const { t } = useTranslation();
  const { decimalsOf } = useCompany();
  const [busy, setBusy] = useState(false);

  async function bt() {
    setBusy(true);
    try {
      await printLines(receiptLines(d, t as never, decimalsOf));
    } catch (e) {
      if (e instanceof ChoosePrinterError) { onNeedPrinter(); return; }
      notifications.show({ color: 'red', message: String((e as Error).message ?? e) });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened onClose={onClose} fullScreen title={t('payments.receipt')}>
      <Stack>
        {!d.payment.number && <Alert color="orange" icon={<IconCloudOff size={16} />}>{t('collector.pendingSync')}</Alert>}
        <Receipt d={d} />
        <SimpleGrid cols={bluetoothSupported() ? 2 : 1} className="no-print">
          {bluetoothSupported() && <Button size="lg" leftSection={<IconBluetooth size={18} />} loading={busy} onClick={bt}>{t('collector.printBt')}</Button>}
          <Button size="lg" variant="default" leftSection={<IconPrinter size={18} />} onClick={() => void printPage()}>{t('invoices.print')}</Button>
        </SimpleGrid>
        <Button size="lg" variant="subtle" onClick={onClose} className="no-print">{t('common.close')}</Button>
      </Stack>
    </Modal>
  );
}

// ---- choose a paired classic-Bluetooth printer (Android app) ----

export function PrinterPicker({ onClose, onConnected }: { onClose: () => void; onConnected: (name: string) => void }) {
  const { t } = useTranslation();
  const [list, setList] = useState<PairedPrinter[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  useEffect(() => {
    listPairedPrinters().then(setList).catch((e) => { setList([]); setError(String((e as Error).message ?? e)); });
  }, []);

  async function choose(p: PairedPrinter) {
    setBusy(p.address);
    setError(null);
    try {
      const name = await connectNativePrinter(p);
      notifications.show({ color: 'teal', message: `${t('collector.connected')}: ${name}` });
      onConnected(name);
    } catch (e) {
      setError(String((e as Error).message ?? e));
    } finally {
      setBusy(null);
    }
  }

  return (
    <Modal opened onClose={onClose} title={t('collector.choosePrinter')}>
      <Stack>
        {error && <Alert color="red">{error}</Alert>}
        {list === null ? <Center p="md"><Loader size="sm" /></Center> : list.length === 0 ? (
          <Text c="dimmed" size="sm">{t('collector.noPaired')}</Text>
        ) : list.map((p) => (
          <Button key={p.address} variant="light" size="md" justify="space-between" loading={busy === p.address}
            leftSection={<IconBluetooth size={18} />} rightSection={<Text size="xs" c="dimmed" dir="ltr">{p.address}</Text>}
            onClick={() => choose(p)}>{p.name}</Button>
        ))}
        <Text size="xs" c="dimmed">{t('collector.pairHint')}</Text>
      </Stack>
    </Modal>
  );
}

// ---- collect money from one customer ----

interface Tender { key: number; method_id: string | null; amount: string }
let k = 1;

function Collect({ customer, methods, rates, online, me, onClose, onDone }: {
  customer: Customer; methods: PaymentMethod[]; rates: Record<string, string>; online: boolean; me: Me;
  onClose: () => void; onDone: (d: PaymentDetail) => void;
}) {
  const { t } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const base = company?.base_currency ?? 'USD';
  const mine = [...methods].sort((a, b) => Number(b.user_id === me.id) - Number(a.user_id === me.id));
  const [currency, setCurrency] = useState(customer.due[0]?.currency ?? base);
  const dueIn = (c: string) => customer.due.find((m: Money) => m.currency === c)?.due;
  // amount to apply: follows min(received, due) until the collector types a value (so partial payments just work)
  const [applyTyped, setApply] = useState<string | null>(null);
  const [tenders, setTenders] = useState<Tender[]>([{ key: k++, method_id: (mine.find((m) => m.currency === currency) ?? mine[0])?.id ?? null, amount: '' }]);
  const cashMine = mine.filter((m) => m.kind === 'cash');
  const [changeMethod, setChangeMethod] = useState<string | null>(
    (cashMine.find((m) => m.currency === company?.local_currency) ?? cashMine[0])?.id ?? null);
  const [busy, setBusy] = useState(false);
  const byId = new Map(methods.map((m) => [m.id, m]));
  const rateOf = (c: string) => (c === base ? 1 : Number(rates[c] || 0));
  const payRate = rateOf(currency);

  let received = 0;
  let missing = false;
  for (const tn of tenders) {
    const m = tn.method_id ? byId.get(tn.method_id) : undefined;
    const a = Number(tn.amount || 0);
    if (!m || !a) continue;
    const r = rateOf(m.currency);
    if (!r || !payRate) { missing = true; continue; }
    received += (a / r) * payRate;
  }
  const apply = applyTyped ?? autoApply(received, Number(dueIn(currency) || 0));
  const applyN = apply === '' ? received : Number(apply);
  const extra = received - applyN;
  const cm = changeMethod ? byId.get(changeMethod) : undefined;
  const cdec = cm ? decimalsOf(cm.currency) : 0;
  const change = extra > 0.00005 && cm ? roundChange((extra / payRate) * rateOf(cm.currency), cdec, cm.change_rounding) : 0;
  const short = applyN - received > 0.01;

  async function submit() {
    if (!company) return;
    setBusy(true);
    const clientRef = newClientRef();
    const body = {
      customer_id: customer.id, date: today(), currency, amount: apply,
      tenders: tenders.filter((x) => x.method_id && x.amount).map((x) => ({ method_id: x.method_id, amount: x.amount })),
      change_method_id: extra > 0.00005 ? changeMethod ?? '' : '',
    };
    const offlineReceipt = (): PaymentDetail => ({
      payment: {
        id: clientRef, number: 0, customer_id: customer.id, customer_code: customer.code, customer_name: customer.name,
        payment_date: today(), currency, amount: String(applyN), change_currency: change > 0 ? cm!.currency : null,
        change_amount: String(change), status: 'posted', journal_entry_id: null, notes: '', received_by: me.full_name,
        created_at: new Date().toISOString(), customer_phone: customer.phone, company_name: company.name_en,
        company_name_ar: company.name_ar, base_currency: base,
      },
      tenders: body.tenders.map((x) => { const m = byId.get(x.method_id!)!; return { method: m.name, currency: m.currency, amount: x.amount, rate: rates[m.currency] ?? '1' }; }),
      allocations: [],
      // offline: estimate the balance on the phone (the server recomputes it when the payment syncs)
      customer_balance: {
        due: customer.due.map((m) => (m.currency === currency ? { ...m, due: String(Math.max(0, Number(m.due) - applyN)) } : m))
          .filter((m) => Number(m.due) > 0.00005),
        credit: [],
      },
    });
    try {
      if (!online) throw new TypeError('offline');
      const r = await api<PaymentDetail>(`/companies/${company.id}/payments`, { method: 'POST', body: { ...body, client_ref: clientRef } });
      onDone(r);
    } catch (e) {
      if (e instanceof TypeError) { // network down: keep it on the phone and send later
        enqueue({ client_ref: clientRef, company_id: company.id, customer_name: customer.name, created_at: new Date().toISOString(), body });
        onDone(offlineReceipt());
      } else {
        notifyError(t, e);
      }
    } finally {
      setBusy(false);
    }
  }

  const methodData = mine.map((m) => ({ value: m.id, label: `${m.name} (${m.currency})` }));
  const pdec = decimalsOf(currency);

  return (
    <Modal opened onClose={onClose} fullScreen title={customer.name}>
      <Stack>
        <Paper withBorder p="sm">
          <Text size="xs" c="dimmed">{t('payments.owed')}</Text>
          {customer.due.length ? <MoneyList items={customer.due} /> : <Text fw={600} c="teal">{t('customers.nothingDue')}</Text>}
        </Paper>
        {customer.due.length > 1 && (
          <SegmentedControl value={currency} data={customer.due.map((m) => m.currency)}
            onChange={(v) => { setCurrency(v); setApply(null); }} />
        )}
        <Text fw={500}>{t('payments.tenders')}</Text>
        {tenders.map((tn) => {
          const m = tn.method_id ? byId.get(tn.method_id) : undefined;
          return (
            <Group key={tn.key} gap="xs" wrap="nowrap">
              <Select style={{ flex: 1 }} size="md" data={methodData} value={tn.method_id} allowDeselect={false}
                onChange={(v) => setTenders((ts) => ts.map((x) => (x.key === tn.key ? { ...x, method_id: v } : x)))} />
              <TextInput style={{ flex: 1 }} size="md" dir="ltr" inputMode="decimal" value={tn.amount} placeholder="0"
                rightSection={<Text size="xs" c="dimmed">{m?.currency}</Text>} rightSectionWidth={42}
                styles={{ input: { fontFamily: 'monospace', fontSize: 20 } }}
                onChange={(e) => { const v = e.currentTarget.value.replace(/[^\d.]/g, ''); setTenders((ts) => ts.map((x) => (x.key === tn.key ? { ...x, amount: v } : x))); }} />
              {tenders.length > 1 && <ActionIcon variant="subtle" color="gray" size="lg" onClick={() => setTenders((ts) => ts.filter((x) => x.key !== tn.key))}><IconTrash size={18} /></ActionIcon>}
            </Group>
          );
        })}
        <Button variant="light" leftSection={<IconPlus size={16} />}
          onClick={() => { const used = new Set(tenders.map((x) => x.method_id)); setTenders((ts) => [...ts, { key: k++, method_id: mine.find((m) => !used.has(m.id))?.id ?? null, amount: '' }]); }}>
          {t('payments.addTender')}
        </Button>
        <Paper withBorder p="md">
          <Group justify="space-between"><Text c="dimmed">{t('payments.received')}</Text><Text fw={700} size="xl" dir="ltr">{formatAmount(received, pdec)} {currency}</Text></Group>
          <TextInput mt="xs" size="md" label={t('payments.toApply')} dir="ltr" inputMode="decimal" value={apply} error={short ? ' ' : undefined}
            rightSection={<Text size="xs" c="dimmed">{currency}</Text>} rightSectionWidth={42} styles={{ input: { fontFamily: 'monospace', fontSize: 18 } }}
            onChange={(e) => setApply(e.currentTarget.value.replace(/[^\d.]/g, ''))} />
          {extra > 0.00005 && (
            <>
              <Select mt="xs" size="sm" label={t('payments.changeIn')} value={changeMethod} onChange={setChangeMethod} allowDeselect={false}
                data={cashMine.map((m) => ({ value: m.id, label: `${m.name} (${m.currency})` }))} />
              <Group justify="space-between" mt="xs">
                <Text c="dimmed">{t('payments.change')}</Text>
                <Text fw={800} size="1.6rem" c="orange.8" dir="ltr">{cm ? `${formatAmount(change, cdec)} ${cm.currency}` : '—'}</Text>
              </Group>
            </>
          )}
          {missing && <Text c="red" size="sm" mt="xs">{t('errors.no_rate', { detail: '' })}</Text>}
        </Paper>
        {!online && <Alert color="orange" icon={<IconCloudOff size={16} />}>{t('collector.willSync')}</Alert>}
        <Button size="xl" loading={busy} disabled={received <= 0 || short || missing} onClick={submit}>{t('collector.confirm')}</Button>
        <Button variant="subtle" onClick={onClose}>{t('common.cancel')}</Button>
      </Stack>
    </Modal>
  );
}

// ---- today's totals and my cash box ----

interface Box { id: string; name: string; currency: string; balance: string; user_id: string | null }
interface ByMethod { method: string; currency: string; received: string; change_given: string }

function MyDay({ me }: { me: Me }) {
  const { t } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [boxes, setBoxes] = useState<Box[] | null>(null);
  const [day, setDay] = useState<{ rows: unknown[]; by_method: ByMethod[] } | null>(null);
  const queued = outbox();

  useEffect(() => {
    if (!company || !navigator.onLine) return;
    api<Box[]>(`/companies/${company.id}/cash/balances`).then(setBoxes).catch(() => setBoxes([]));
    api<{ rows: unknown[]; by_method: ByMethod[] }>(`/companies/${company.id}/payments?from=${today()}&to=${today()}`).then(setDay).catch(() => {});
  }, [company]);

  return (
    <Stack>
      <Paper withBorder p="md">
        <Text size="xs" c="dimmed">{t('collector.today')} · {me.full_name}</Text>
        <Text fw={700} size="xl">{day ? `${day.rows.length} ${t('bill.payments')}` : '…'}</Text>
        {day?.by_method.map((m) => (
          <Group key={m.method + m.currency} justify="space-between">
            <Text size="sm">{m.method}</Text>
            <Text fw={600} dir="ltr">{formatAmount(Number(m.received) - Number(m.change_given), decimalsOf(m.currency))} {m.currency}</Text>
          </Group>
        ))}
      </Paper>
      <Paper withBorder p="md">
        <Text size="xs" c="dimmed" mb={4}>{t('collector.myBox')}</Text>
        {boxes === null ? <Loader size="xs" /> : boxes.length === 0 ? <Text c="dimmed" size="sm">—</Text> : boxes.map((b) => (
          <Group key={b.id} justify="space-between"><Text size="sm">{b.name}</Text><Text fw={700} dir="ltr">{formatAmount(b.balance, decimalsOf(b.currency))} {b.currency}</Text></Group>
        ))}
      </Paper>
      {queued.length > 0 && (
        <Paper withBorder p="md">
          <Text size="xs" c="dimmed" mb={4}>{t('collector.queued')} ({queued.length})</Text>
          {queued.map((p) => (
            <Group key={p.client_ref} justify="space-between">
              <Text size="sm">{p.customer_name}</Text>
              {p.error ? <Badge color="red">{t(`errors.${p.error}`, { defaultValue: p.error })}</Badge> : <Badge color="orange">{t('collector.waiting')}</Badge>}
            </Group>
          ))}
          <Button mt="sm" size="xs" variant="light" leftSection={<IconCloudUpload size={14} />} onClick={() => void syncOutbox()}>{t('collector.syncNow')}</Button>
        </Paper>
      )}
    </Stack>
  );
}
