import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  ActionIcon, Alert, Badge, Box, Button, Card, Center, Checkbox, Drawer, Grid, Group, Image, Loader, Menu, Modal, Paper, ScrollArea,
  Select, SimpleGrid, Stack, Text, TextInput, UnstyledButton,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import {
  IconArrowBack, IconBarcode, IconBluetooth, IconCamera, IconCash, IconCloudOff, IconCloudUpload, IconLogout, IconMenu2, IconMinus,
  IconPlus, IconPrinter, IconReceipt, IconRefresh, IconSearch, IconShoppingCart, IconTrash, IconUser, IconUserPlus, IconX,
} from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { api, ApiError, type Me } from '../../api';
import { useCompany } from '../../company';
import { applyLang, languages } from '../../i18n';
import { formatAmount, formatDateTime, notifyError, notifySaved, roundChange } from '../../util';
import { printPage } from '../../native';
import Logo from '../../components/Logo';
import {
  bluetoothSupported, ChoosePrinterError, connectPrinter, isNativeApp, paperWidth, printerName, printLines, setPaper,
} from '../../bt';
import { cacheGet, cacheSet, enqueue, newClientRef, outbox, syncOutbox } from '../../outbox';
import { PrinterPicker } from '../collector/CollectorApp';
import {
  itemName, OrderReceipt, orderReceiptLines, round, SessionReport, zReportLines,
  type Catalog, type CatItem, type PosOrder, type PosOrderDetail, type Register, type SessionDetail, type TillMethod,
} from './common';

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

function usePending(cid?: string) {
  const count = () => outbox().filter((p) => p.path?.includes('/pos/') && (!cid || p.company_id === cid)).length;
  const [n, setN] = useState(count);
  useEffect(() => {
    const f = () => setN(count());
    f();
    window.addEventListener('paxve:outbox', f);
    return () => window.removeEventListener('paxve:outbox', f);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cid]);
  return n;
}

const regKey = (cid: string) => `paxve.pos.register.${cid}`;
function savedRegister(cid: string): string | null {
  try { return localStorage.getItem(regKey(cid)); } catch { return null; }
}
function saveRegister(cid: string, id: string | null) {
  try { if (id) localStorage.setItem(regKey(cid), id); else localStorage.removeItem(regKey(cid)); } catch { /* ignore */ }
}

interface CartLine { key: number; item: CatItem; qty: string; price: string; discount: string }
let lineKey = 1;

/** Point of sale screen (cashiers see only this; office users open it from the menu). */
export default function PosApp({ me, onLogout }: { me: Me; onLogout: () => void }) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const online = useOnline();
  const pending = usePending(company?.id);
  const [registers, setRegisters] = useState<Register[] | null>(null);
  const [regId, setRegId] = useState<string | null>(null);
  const [cat, setCat] = useState<Catalog | null>(null);
  const [sessionId, setSessionId] = useState<string | null>(null);
  const [cart, setCart] = useState<CartLine[]>([]);
  const [customer, setCustomer] = useState<{ id: string; name: string } | null>(null);
  const [q, setQ] = useState('');
  const [catFilter, setCatFilter] = useState<string | null>(null);
  const [payOpen, setPayOpen] = useState(false);
  const [receipt, setReceipt] = useState<PosOrderDetail | null>(null);
  const [ordersOpen, setOrdersOpen] = useState(false);
  const [closeOpen, setCloseOpen] = useState(false);
  const [custOpen, setCustOpen] = useState(false);
  const [editLine, setEditLine] = useState<CartLine | null>(null);
  const [cartOpen, setCartOpen] = useState(false);
  const [scanOpen, setScanOpen] = useState(false);
  const [printer, setPrinter] = useState(printerName());
  const [pickerOpen, setPickerOpen] = useState(false);
  const searchRef = useRef<HTMLInputElement>(null);
  const cid = company?.id;
  const office = me.role !== 'cashier';
  const lang = i18n.language;

  // registers
  const loadRegisters = useCallback(() => {
    if (!cid) return;
    api<Register[]>(`/companies/${cid}/pos/registers`).then((rs) => {
      const active = rs.filter((r) => r.is_active);
      setRegisters(active);
      cacheSet(`pos.registers.${cid}`, active);
      const saved = savedRegister(cid);
      const pick = active.find((r) => r.id === saved) ?? (active.length === 1 ? active[0] : null);
      setRegId(pick?.id ?? null);
      setSessionId(pick?.open_session?.id ?? null);
    }).catch(() => {
      const cached = cacheGet<Register[]>(`pos.registers.${cid}`)?.value ?? [];
      setRegisters(cached);
      const saved = savedRegister(cid);
      const pick = cached.find((r) => r.id === saved) ?? null;
      setRegId(pick?.id ?? null);
      setSessionId(cacheGet<string>(`pos.session.${pick?.id}`)?.value ?? pick?.open_session?.id ?? null);
    });
  }, [cid]);
  useEffect(loadRegisters, [loadRegisters]);

  // catalog of the chosen register (kept for offline use)
  const loadCatalog = useCallback(() => {
    if (!cid || !regId) return;
    api<Catalog>(`/companies/${cid}/pos/catalog?register_id=${regId}`).then((c) => {
      setCat(c);
      cacheSet(`pos.catalog.${regId}`, c);
      setSessionId(c.register.open_session?.id ?? null);
      cacheSet(`pos.session.${regId}`, c.register.open_session?.id ?? null);
    }).catch(() => setCat(cacheGet<Catalog>(`pos.catalog.${regId}`)?.value ?? null));
  }, [cid, regId]);
  useEffect(loadCatalog, [loadCatalog]);

  // send sales taken offline
  useEffect(() => {
    if (!online) return;
    const run = () => syncOutbox().then((n) => {
      if (n > 0) { notifications.show({ color: 'teal', message: t('pos.synced', { n }) }); loadCatalog(); }
    });
    void run();
    const id = window.setInterval(run, 30000);
    return () => window.clearInterval(id);
  }, [online, loadCatalog, t]);

  const items = cat?.items ?? [];
  const byBarcode = useMemo(() => {
    const m = new Map<string, CatItem>();
    for (const it of items) {
      if (it.barcode) m.set(it.barcode, it);
      m.set(it.code.toLowerCase(), it);
    }
    return m;
  }, [items]);

  const shown = useMemo(() => {
    const s = q.trim().toLowerCase();
    return items.filter((it) => (!catFilter || it.category_id === catFilter) &&
      (!s || itemName(it, lang).toLowerCase().includes(s) || it.name.toLowerCase().includes(s) || it.code.toLowerCase().startsWith(s) || it.barcode === s));
  }, [items, q, catFilter, lang]);

  const add = useCallback((it: CatItem, qty = 1) => {
    setCart((c) => {
      const i = c.findIndex((l) => l.item.id === it.id && l.price === '' && l.discount === '');
      if (i >= 0) return c.map((l, j) => (j === i ? { ...l, qty: String(Number(l.qty) + qty) } : l));
      return [...c, { key: lineKey++, item: it, qty: String(qty), price: '', discount: '' }];
    });
  }, []);

  const scan = useCallback((code: string) => {
    const it = byBarcode.get(code.trim()) ?? byBarcode.get(code.trim().toLowerCase());
    if (it) {
      add(it);
      return true;
    }
    notifications.show({ color: 'orange', message: `${t('pos.notFound')}: ${code}` });
    return false;
  }, [byBarcode, add, t]);

  // keyboard-wedge barcode scanners type fast and end with Enter: catch them even when no field has focus
  useEffect(() => {
    let buf = '';
    let last = 0;
    const onKey = (e: KeyboardEvent) => {
      const el = document.activeElement as HTMLElement | null;
      if (el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.isContentEditable)) return;
      if (payOpen || closeOpen || custOpen || editLine) return;
      const now = Date.now();
      if (now - last > 80) buf = '';
      last = now;
      if (e.key === 'Enter') {
        if (buf.length >= 4) scan(buf);
        buf = '';
      } else if (e.key.length === 1) {
        buf += e.key;
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [scan, payOpen, closeOpen, custOpen, editLine]);

  const reg = cat?.register ?? registers?.find((r) => r.id === regId) ?? null;
  const currency = reg?.currency ?? company?.base_currency ?? 'USD';
  const dec = decimalsOf(currency);
  const canEdit = office || !!reg?.cashier_price_edit;

  const priced = cart.map((l) => {
    const price = l.price !== '' ? Number(l.price) : Number(l.item.price);
    const disc = Number(l.discount || 0);
    const amount = round(Number(l.qty || 0) * price * (100 - disc) / 100, dec);
    const tax = l.item.tax_rate ? round(amount * Number(l.item.tax_rate) / 100, dec) : 0;
    return { ...l, unit: price, amount, tax };
  });
  const subtotal = priced.reduce((s, l) => s + l.amount, 0);
  const taxTotal = priced.reduce((s, l) => s + l.tax, 0);
  const total = round(subtotal + taxTotal, dec);
  const count = cart.reduce((s, l) => s + Number(l.qty || 0), 0);

  function choose(id: string | null) {
    if (!cid) return;
    saveRegister(cid, id);
    setRegId(id);
    setCat(null);
    setCart([]);
    const r = registers?.find((x) => x.id === id);
    setSessionId(r?.open_session?.id ?? null);
  }

  async function pickPrinter() {
    if (isNativeApp()) { setPickerOpen(true); return; }
    try { setPrinter(await connectPrinter()); } catch (e) { notifications.show({ color: 'red', message: String((e as Error).message ?? e) }); }
  }

  async function logout() {
    if (!office) {
      try { await api('/auth/logout', { method: 'POST' }); } finally { onLogout(); }
    } else onLogout();
  }

  if (!company) return <Center h="100vh"><Loader /></Center>;

  const header = (
    <Paper shadow="xs" radius={0} px="sm" py={6} style={{ position: 'sticky', top: 0, zIndex: 20 }}>
      <Group justify="space-between" wrap="nowrap">
        <Group gap={8} wrap="nowrap" style={{ minWidth: 0 }}>
          <Logo />
          {reg && <Text fw={600} size="sm" truncate>{reg.name}{sessionId && cat?.register.open_session ? ` · #${cat.register.open_session.number}` : ''}</Text>}
          {online ? (pending > 0 && <Badge color="orange" leftSection={<IconCloudUpload size={12} />}>{pending}</Badge>)
            : <Badge color="red" leftSection={<IconCloudOff size={12} />}>{t('collector.offline')}</Badge>}
        </Group>
        <Group gap={4} wrap="nowrap">
          {sessionId && <Button size="xs" variant="light" leftSection={<IconReceipt size={14} />} onClick={() => setOrdersOpen(true)} visibleFrom="xs">{t('pos.orders')}</Button>}
          {bluetoothSupported() && (
            <ActionIcon variant={printer ? 'filled' : 'light'} color={printer ? 'teal' : 'gray'} size="lg" onClick={pickPrinter} aria-label={t('collector.printer')}>
              <IconBluetooth size={18} />
            </ActionIcon>
          )}
          <Menu position="bottom-end">
            <Menu.Target><ActionIcon variant="light" color="gray" size="lg" aria-label="menu"><IconMenu2 size={18} /></ActionIcon></Menu.Target>
            <Menu.Dropdown>
              <Menu.Label>{me.full_name} · {company.name_en}</Menu.Label>
              {sessionId && <Menu.Item leftSection={<IconReceipt size={14} />} onClick={() => setOrdersOpen(true)}>{t('pos.orders')}</Menu.Item>}
              {sessionId && <Menu.Item leftSection={<IconCash size={14} />} disabled={!online || pending > 0} onClick={() => setCloseOpen(true)}>{t('pos.closeSession')}</Menu.Item>}
              {(registers?.length ?? 0) > 1 && <Menu.Item leftSection={<IconRefresh size={14} />} onClick={() => choose(null)}>{t('pos.changeRegister')}</Menu.Item>}
              <Menu.Item leftSection={<IconRefresh size={14} />} onClick={loadCatalog}>{t('pos.reload')}</Menu.Item>
              <Menu.Divider />
              {languages.map((l) => <Menu.Item key={l.code} onClick={() => applyLang(l.code)}>{l.label}</Menu.Item>)}
              <Menu.Divider />
              <Menu.Label>{t('collector.paper')}</Menu.Label>
              <Menu.Item onClick={() => setPaper('58')}>58 mm {paperWidth() === 384 && '✓'}</Menu.Item>
              <Menu.Item onClick={() => setPaper('80')}>80 mm {paperWidth() === 576 && '✓'}</Menu.Item>
              <Menu.Divider />
              {office
                ? <Menu.Item leftSection={<IconArrowBack size={14} />} onClick={onLogout}>{t('pos.backToOffice')}</Menu.Item>
                : <Menu.Item color="red" leftSection={<IconLogout size={14} />} onClick={logout}>{t('common.logout')}</Menu.Item>}
            </Menu.Dropdown>
          </Menu>
        </Group>
      </Group>
    </Paper>
  );

  // ---- no register yet / choose one ----
  if (registers === null) return <Box mih="100vh">{header}<Center p="xl"><Loader /></Center></Box>;
  if (!regId) {
    return (
      <Box mih="100vh" bg="var(--mantine-color-gray-0)">
        {header}
        <Center p="md">
          <Stack w="100%" maw={480}>
            {registers.length === 0 ? (
              <Alert color="yellow" title={t('pos.noRegister')}>{office ? t('pos.noRegisterHintOffice') : t('pos.noRegisterHint')}
                {office && <Button mt="sm" size="xs" component="a" href="#/pos-setup">{t('pos.setup')}</Button>}</Alert>
            ) : <>
              <Text fw={600}>{t('pos.chooseRegister')}</Text>
              {registers.map((r) => (
                <Card key={r.id} withBorder padding="md" style={{ cursor: 'pointer' }} onClick={() => choose(r.id)}>
                  <Group justify="space-between">
                    <div><Text fw={600}>{r.name}</Text><Text size="xs" c="dimmed">{r.warehouse} · {r.currency}</Text></div>
                    {r.open_session ? <Badge color="teal" variant="light">{t('pos.status.open')} #{r.open_session.number}</Badge> : <Badge color="gray" variant="light">{t('pos.status.closed')}</Badge>}
                  </Group>
                </Card>
              ))}
            </>}
          </Stack>
        </Center>
      </Box>
    );
  }
  if (!cat) return <Box mih="100vh">{header}<Center p="xl"><Loader /></Center></Box>;

  // ---- no open session: open one ----
  if (!sessionId) {
    return (
      <Box mih="100vh" bg="var(--mantine-color-gray-0)">
        {header}
        <OpenSession cat={cat} online={online} onOpened={(id) => { setSessionId(id); cacheSet(`pos.session.${regId}`, id); loadCatalog(); }} />
      </Box>
    );
  }

  const categories = cat.categories.filter((c) => items.some((it) => it.category_id === c.id));

  const cartPanel = (
    <Stack gap="xs" h="100%">
      <Group justify="space-between" wrap="nowrap">
        <UnstyledButton onClick={() => setCustOpen(true)} style={{ minWidth: 0 }}>
          <Group gap={6} wrap="nowrap">
            <IconUser size={16} />
            <Text size="sm" fw={500} truncate>{customer ? customer.name : t('pos.walkIn')}</Text>
          </Group>
        </UnstyledButton>
        {customer && <ActionIcon size="sm" variant="subtle" color="gray" onClick={() => setCustomer(null)} aria-label="x"><IconX size={14} /></ActionIcon>}
      </Group>
      <ScrollArea style={{ flex: 1 }} mih={120}>
        {priced.length === 0 ? <Text c="dimmed" ta="center" size="sm" p="lg">{t('pos.emptyCart')}</Text> : (
          <Stack gap={4}>
            {priced.map((l) => (
              <Paper key={l.key} withBorder p={6} radius="sm">
                <Group justify="space-between" wrap="nowrap" gap={4}>
                  <UnstyledButton style={{ flex: 1, minWidth: 0 }} onClick={() => setEditLine(l)}>
                    <Text size="sm" fw={500} truncate>{itemName(l.item, lang)}</Text>
                    <Text size="xs" c="dimmed" dir="ltr" ta="start">{formatAmount(l.unit, dec)}{l.discount ? ` −${l.discount}%` : ''}{l.tax ? ` +${l.item.tax_name ?? ''}` : ''}</Text>
                  </UnstyledButton>
                  <Group gap={2} wrap="nowrap">
                    <ActionIcon size="md" variant="light" color="gray" aria-label="-" onClick={() => setCart((c) => c.flatMap((x) => (x.key !== l.key ? [x] : Number(x.qty) > 1 ? [{ ...x, qty: String(Number(x.qty) - 1) }] : [])))}>
                      <IconMinus size={14} />
                    </ActionIcon>
                    <Text w={34} ta="center" ff="monospace" size="sm">{Number(l.qty)}</Text>
                    <ActionIcon size="md" variant="light" color="gray" aria-label="+" onClick={() => setCart((c) => c.map((x) => (x.key === l.key ? { ...x, qty: String(Number(x.qty) + 1) } : x)))}>
                      <IconPlus size={14} />
                    </ActionIcon>
                  </Group>
                  <Text w={72} ta="end" ff="monospace" size="sm" dir="ltr">{formatAmount(l.amount + l.tax, dec)}</Text>
                </Group>
              </Paper>
            ))}
          </Stack>
        )}
      </ScrollArea>
      <Paper withBorder p="sm" bg="var(--mantine-color-gray-0)">
        {taxTotal > 0 && <Group justify="space-between"><Text size="sm">{t('pos.tax')}</Text><Text size="sm" ff="monospace" dir="ltr">{formatAmount(taxTotal, dec)}</Text></Group>}
        <Group justify="space-between"><Text fw={700}>{t('pos.total')}</Text><Text fw={700} size="xl" ff="monospace" dir="ltr">{formatAmount(total, dec)} {currency}</Text></Group>
      </Paper>
      <Group grow>
        <Button variant="default" color="red" leftSection={<IconTrash size={16} />} disabled={!cart.length} onClick={() => { setCart([]); setCustomer(null); }}>{t('pos.clear')}</Button>
        <Button size="md" leftSection={<IconCash size={18} />} disabled={!cart.length || total <= 0} onClick={() => { setCartOpen(false); setPayOpen(true); }}>{t('pos.pay')}</Button>
      </Group>
    </Stack>
  );

  return (
    <Box mih="100vh" bg="var(--mantine-color-gray-0)">
      {header}
      <Grid gap="sm" p="sm" m={0}>
        <Grid.Col span={{ base: 12, md: 7, lg: 8 }}>
          <Stack gap="xs">
            <Group gap="xs" wrap="nowrap">
              <TextInput ref={searchRef} style={{ flex: 1 }} size="md" leftSection={<IconSearch size={18} />} rightSection={<IconBarcode size={18} />}
                placeholder={t('pos.searchOrScan')} value={q} onChange={(e) => setQ(e.currentTarget.value)}
                onKeyDown={(e) => {
                  if (e.key !== 'Enter') return;
                  const v = q.trim();
                  if (!v) return;
                  const exact = byBarcode.get(v) ?? byBarcode.get(v.toLowerCase());
                  if (exact) { add(exact); setQ(''); return; }
                  if (shown.length === 1) { add(shown[0]); setQ(''); }
                }} />
              {'BarcodeDetector' in window && (
                <ActionIcon size={42} variant="light" onClick={() => setScanOpen(true)} aria-label={t('pos.camera')}><IconCamera size={20} /></ActionIcon>
              )}
            </Group>
            {categories.length > 0 && (
              <ScrollArea type="never">
                <Group gap={6} wrap="nowrap">
                  <Button size="compact-sm" variant={catFilter === null ? 'filled' : 'light'} color="gray" onClick={() => setCatFilter(null)}>{t('common.all')}</Button>
                  {categories.map((c) => (
                    <Button key={c.id} size="compact-sm" variant={catFilter === c.id ? 'filled' : 'light'} color={c.color}
                      onClick={() => setCatFilter(catFilter === c.id ? null : c.id)}>{itemName(c, lang)}</Button>
                  ))}
                </Group>
              </ScrollArea>
            )}
            {items.length === 0 ? (
              <Alert color="yellow">{t('pos.noItems')}</Alert>
            ) : (
              <SimpleGrid cols={{ base: 2, xs: 3, sm: 4, lg: 5 }} spacing="xs">
                {shown.slice(0, 300).map((it) => (
                  <Card key={it.id} withBorder padding={6} radius="md" style={{ cursor: 'pointer', userSelect: 'none' }} onClick={() => add(it)}>
                    {it.image ? <Image src={it.image} h={80} fit="cover" radius="sm" alt="" />
                      : <Center h={80} bg="var(--mantine-color-gray-1)" style={{ borderRadius: 6 }}>
                          <Text fw={700} size="xl" c="dimmed">{itemName(it, lang).slice(0, 2).toUpperCase()}</Text>
                        </Center>}
                    <Text size="sm" fw={500} lineClamp={2} mt={4} lh={1.2} mih={34}>{itemName(it, lang)}</Text>
                    <Group justify="space-between" gap={2} wrap="nowrap">
                      <Text size="sm" fw={700} ff="monospace" dir="ltr">{formatAmount(it.price, dec)}</Text>
                      {it.kind === 'stock' && <Text size="xs" c={Number(it.stock) > 0 ? 'dimmed' : 'red'}>{Number(it.stock)}</Text>}
                    </Group>
                  </Card>
                ))}
              </SimpleGrid>
            )}
          </Stack>
        </Grid.Col>
        <Grid.Col span={{ md: 5, lg: 4 }} visibleFrom="md">
          <Paper withBorder p="sm" style={{ position: 'sticky', top: 64, height: 'calc(100vh - 80px)' }}>{cartPanel}</Paper>
        </Grid.Col>
      </Grid>

      {/* phone / small tablet: cart in a drawer opened from a bottom bar */}
      <Box hiddenFrom="md" h={76} />
      <Box hiddenFrom="md" p="xs" bg="white"
        style={{ position: 'fixed', bottom: 0, left: 0, right: 0, zIndex: 15, paddingBottom: 'calc(var(--mantine-spacing-xs) + env(safe-area-inset-bottom))', boxShadow: '0 -2px 8px rgba(0,0,0,.06)' }}>
        <Button fullWidth size="lg" leftSection={<IconShoppingCart size={20} />} onClick={() => setCartOpen(true)} disabled={!cart.length}
          rightSection={<Text fw={700} ff="monospace" dir="ltr">{formatAmount(total, dec)} {currency}</Text>} justify="space-between">
          {t('pos.cart')} ({count})
        </Button>
      </Box>
      <Drawer opened={cartOpen} onClose={() => setCartOpen(false)} position="bottom" size="85%" title={t('pos.cart')} hiddenFrom="md">
        <Box h="calc(85vh - 80px)">{cartPanel}</Box>
      </Drawer>

      {editLine && (
        <LineEditor line={editLine} canEdit={canEdit} dec={dec} lang={lang} onClose={() => setEditLine(null)}
          onSave={(l) => { setCart((c) => c.map((x) => (x.key === l.key ? l : x)).filter((x) => Number(x.qty) > 0)); setEditLine(null); }} />
      )}
      {custOpen && <CustomerPicker onClose={() => setCustOpen(false)} onPick={(c) => { setCustomer(c); setCustOpen(false); }} />}
      {scanOpen && <CameraScanner onClose={() => setScanOpen(false)} onCode={(c) => { if (scan(c)) setScanOpen(false); }} />}
      {payOpen && (
        <PayDialog cat={cat} sessionId={sessionId} customer={customer} total={total} taxTotal={taxTotal} priced={priced} online={online} me={me}
          onClose={() => setPayOpen(false)}
          onDone={(d) => { setPayOpen(false); setCart([]); setCustomer(null); setReceipt(d); if (!d.pending) loadCatalog(); }} />
      )}
      {receipt && <ReceiptSheet d={receipt} onClose={() => { setReceipt(null); searchRef.current?.focus(); }} onNeedPrinter={() => setPickerOpen(true)} />}
      {ordersOpen && (
        <OrdersDrawer sessionId={sessionId} cat={cat} me={me} onClose={() => setOrdersOpen(false)}
          onReceipt={(d) => setReceipt(d)} />
      )}
      {closeOpen && (
        <CloseSession sessionId={sessionId} onClose={() => setCloseOpen(false)} onNeedPrinter={() => setPickerOpen(true)}
          onClosed={() => { setSessionId(null); cacheSet(`pos.session.${regId}`, null); loadCatalog(); loadRegisters(); }} />
      )}
      {pickerOpen && <PrinterPicker onClose={() => setPickerOpen(false)} onConnected={(n) => { setPrinter(n); setPickerOpen(false); }} />}
    </Box>
  );
}

// ---------- open a session ----------

function OpenSession({ cat, online, onOpened }: { cat: Catalog; online: boolean; onOpened: (id: string) => void }) {
  const { t } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [amounts, setAmounts] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const cash = cat.methods.filter((m) => m.kind === 'cash');

  async function open() {
    if (!company) return;
    setBusy(true);
    try {
      const r = await api<SessionDetail>(`/companies/${company.id}/pos/sessions`, {
        method: 'POST', body: { register_id: cat.register.id, opening: Object.entries(amounts).filter(([, v]) => v !== '').map(([method_id, amount]) => ({ method_id, amount })) },
      });
      notifySaved(t);
      onOpened(r.session.id);
    } catch (e) {
      notifyError(t, e);
      if (e instanceof ApiError && e.code === 'session_open') onOpened('');
    } finally {
      setBusy(false);
    }
  }

  return (
    <Center p="md">
      <Paper withBorder p="lg" w="100%" maw={460}>
        <Stack>
          <div>
            <Text fw={700} size="lg">{t('pos.openSession')}</Text>
            <Text size="sm" c="dimmed">{cat.register.name} · {cat.register.warehouse}</Text>
          </div>
          <Text size="sm">{t('pos.openingHint')}</Text>
          {cash.map((m) => (
            <TextInput key={m.id} label={`${m.name}`} dir="ltr" inputMode="decimal" placeholder={formatAmount(0, decimalsOf(m.currency))}
              rightSection={<Text size="xs" c="dimmed">{m.currency}</Text>} rightSectionWidth={44}
              value={amounts[m.id] ?? ''} onChange={(e) => { const v = e.currentTarget.value.replace(/[^\d.]/g, ''); setAmounts((a) => ({ ...a, [m.id]: v })); }} />
          ))}
          {!online && <Alert color="red" icon={<IconCloudOff size={16} />}>{t('pos.needOnline')}</Alert>}
          <Button size="md" loading={busy} disabled={!online} onClick={open}>{t('pos.openSession')}</Button>
        </Stack>
      </Paper>
    </Center>
  );
}

// ---------- edit a cart line ----------

function LineEditor({ line, canEdit, dec, lang, onClose, onSave }: {
  line: CartLine; canEdit: boolean; dec: number; lang: string; onClose: () => void; onSave: (l: CartLine) => void;
}) {
  const { t } = useTranslation();
  const [l, setL] = useState(line);
  const clean = (v: string) => v.replace(/[^\d.]/g, '');
  return (
    <Modal opened onClose={onClose} title={itemName(line.item, lang)} size="sm">
      <Stack>
        <TextInput label={t('pos.qty')} dir="ltr" inputMode="decimal" value={l.qty} data-autofocus onChange={(e) => setL({ ...l, qty: clean(e.currentTarget.value) })} />
        <TextInput label={t('pos.price')} dir="ltr" inputMode="decimal" disabled={!canEdit} placeholder={formatAmount(line.item.price, dec)}
          value={l.price} onChange={(e) => setL({ ...l, price: clean(e.currentTarget.value) })} />
        <TextInput label={t('pos.discount')} dir="ltr" inputMode="decimal" disabled={!canEdit} rightSection="%" value={l.discount}
          onChange={(e) => setL({ ...l, discount: clean(e.currentTarget.value) })} />
        {!canEdit && <Text size="xs" c="dimmed">{t('pos.noPriceEdit')}</Text>}
        <Group justify="space-between">
          <Button variant="subtle" color="red" leftSection={<IconTrash size={14} />} onClick={() => onSave({ ...l, qty: '0' })}>{t('pos.remove')}</Button>
          <Group gap="xs">
            <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
            <Button onClick={() => onSave(l)} disabled={!(Number(l.qty) >= 0) || Number(l.discount || 0) > 100}>{t('common.save')}</Button>
          </Group>
        </Group>
      </Stack>
    </Modal>
  );
}

// ---------- named customer ----------

function CustomerPicker({ onClose, onPick }: { onClose: () => void; onPick: (c: { id: string; name: string }) => void }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const [q, setQ] = useState('');
  const [list, setList] = useState<{ id: string; code: string; name: string; phone: string }[]>([]);
  const [adding, setAdding] = useState(false);
  const [nf, setNf] = useState({ name: '', phone: '' });
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!company || q.trim().length < 2) { setList([]); return; }
    const h = window.setTimeout(() => {
      api<typeof list>(`/companies/${company.id}/customers?limit=20&q=${encodeURIComponent(q.trim())}`).then(setList).catch(() => setList([]));
    }, 250);
    return () => window.clearTimeout(h);
  }, [company, q]);

  async function create() {
    if (!company) return;
    setBusy(true);
    try {
      const c = await api<{ id: string; name: string }>(`/companies/${company.id}/pos/customers`, { method: 'POST', body: nf });
      onPick(c);
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened onClose={onClose} title={t('pos.customer')}>
      {adding ? (
        <Stack>
          <TextInput label={t('customers.name')} value={nf.name} data-autofocus onChange={(e) => setNf({ ...nf, name: e.currentTarget.value })} />
          <TextInput label={t('customers.phone')} dir="ltr" value={nf.phone} onChange={(e) => setNf({ ...nf, phone: e.currentTarget.value })} />
          <Group justify="flex-end">
            <Button variant="default" onClick={() => setAdding(false)}>{t('common.cancel')}</Button>
            <Button loading={busy} disabled={!nf.name.trim()} onClick={create}>{t('common.save')}</Button>
          </Group>
        </Stack>
      ) : (
        <Stack gap="xs">
          <TextInput leftSection={<IconSearch size={16} />} placeholder={t('customers.search')} value={q} data-autofocus onChange={(e) => setQ(e.currentTarget.value)} />
          {list.map((c) => (
            <Button key={c.id} variant="light" justify="space-between" onClick={() => onPick(c)}
              rightSection={<Text size="xs" c="dimmed" dir="ltr">{c.phone}</Text>}>{c.name} ({c.code})</Button>
          ))}
          <Button variant="subtle" leftSection={<IconUserPlus size={16} />} onClick={() => { setNf({ name: q, phone: '' }); setAdding(true); }}>{t('pos.newCustomer')}</Button>
        </Stack>
      )}
    </Modal>
  );
}

// ---------- camera barcode scanner (BarcodeDetector: Chrome / Android) ----------

function CameraScanner({ onClose, onCode }: { onClose: () => void; onCode: (code: string) => void }) {
  const { t } = useTranslation();
  const video = useRef<HTMLVideoElement>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    let stream: MediaStream | null = null;
    let stop = false;
    let lastCode = '';
    (async () => {
      try {
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        const Detector = (window as any).BarcodeDetector;
        const detector = new Detector({ formats: ['ean_13', 'ean_8', 'upc_a', 'upc_e', 'code_128', 'code_39', 'qr_code'] });
        stream = await navigator.mediaDevices.getUserMedia({ video: { facingMode: 'environment' } });
        if (!video.current) return;
        video.current.srcObject = stream;
        await video.current.play();
        const tick = async () => {
          if (stop || !video.current) return;
          try {
            const codes = await detector.detect(video.current);
            if (codes.length && codes[0].rawValue !== lastCode) {
              lastCode = codes[0].rawValue;
              onCode(lastCode);
              window.setTimeout(() => { lastCode = ''; }, 1500);
            }
          } catch { /* frame not ready */ }
          window.setTimeout(() => void tick(), 250);
        };
        void tick();
      } catch (e) {
        setError(String((e as Error).message ?? e));
      }
    })();
    return () => { stop = true; stream?.getTracks().forEach((tr) => tr.stop()); };
  }, [onCode]);
  return (
    <Modal opened onClose={onClose} title={t('pos.camera')} size="md">
      {error ? <Alert color="red">{error}</Alert> : <video ref={video} style={{ width: '100%', borderRadius: 8, background: '#000' }} muted playsInline />}
    </Modal>
  );
}

// ---------- payment ----------

interface Priced extends CartLine { unit: number; amount: number; tax: number }

function PayDialog({ cat, sessionId, customer, total, taxTotal, priced, online, me, onClose, onDone }: {
  cat: Catalog; sessionId: string; customer: { id: string; name: string } | null; total: number; taxTotal: number; priced: Priced[];
  online: boolean; me: Me; onClose: () => void; onDone: (d: PosOrderDetail) => void;
}) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const reg = cat.register;
  const base = cat.company.base_currency;
  const currency = reg.currency;
  const dec = decimalsOf(currency);
  const rates = useMemo(() => Object.fromEntries(cat.rates.map((r) => [r.currency, Number(r.rate)])), [cat.rates]);
  const rateOf = (cur: string) => (cur === base ? 1 : rates[cur] ?? 0);
  const payRate = rateOf(currency);
  const conv = (amount: number, from: string, to: string) => (rateOf(from) ? amount / rateOf(from) * rateOf(to) : 0);
  const [amounts, setAmounts] = useState<Record<string, string>>({});
  const cash = cat.methods.filter((m) => m.kind === 'cash');
  const defaultChange = cash.find((m) => m.currency === cat.company.local_currency) ?? cash.find((m) => m.currency === currency) ?? cash[0];
  const [changeId, setChangeId] = useState<string | null>(defaultChange?.id ?? null);
  const [onAccount, setOnAccount] = useState(false);
  const [busy, setBusy] = useState(false);

  let received = 0;
  let missingRate = false;
  for (const m of cat.methods) {
    const a = Number(amounts[m.id] || 0);
    if (!a) continue;
    if (!rateOf(m.currency) || !payRate) { missingRate = true; continue; }
    received += conv(a, m.currency, currency);
  }
  const extra = received - total;
  const cm = cat.methods.find((m) => m.id === changeId);
  const change = extra > 0.00005 && cm ? roundChange(conv(extra, currency, cm.currency), decimalsOf(cm.currency), cm.change_rounding) : 0;
  const short = total - received > 0.01;
  const canConfirm = !missingRate && (received > 0 || onAccount) && (!short || (onAccount && !!customer));

  function exact(m: TillMethod) {
    const left = Math.max(0, total - received + conv(Number(amounts[m.id] || 0), m.currency, currency));
    const md = decimalsOf(m.currency);
    let v = Math.ceil(conv(left, currency, m.currency) * 10 ** md - 1e-9) / 10 ** md;
    const step = Number(m.change_rounding || 0);
    if (step > 0) v = Math.ceil(v / step - 1e-9) * step;
    setAmounts((a) => ({ ...a, [m.id]: String(v) }));
  }

  async function confirm() {
    if (!company) return;
    const ref = newClientRef();
    const body = {
      session_id: sessionId, customer_id: customer?.id ?? '', on_account: onAccount && !!customer, client_time: new Date().toISOString(),
      lines: priced.map((l) => ({ item_id: l.item.id, quantity: l.qty, unit_price: l.price, discount_pct: l.discount })),
      tenders: cat.methods.filter((m) => Number(amounts[m.id] || 0) > 0).map((m) => ({ method_id: m.id, amount: amounts[m.id] })),
      change_method_id: extra > 0.00005 ? changeId ?? '' : '',
    };
    const local = (): PosOrderDetail => ({
      pending: true,
      order: {
        id: ref, number: 0, kind: 'sale', order_date: new Date().toISOString().slice(0, 10), session_id: sessionId, session_number: reg.open_session?.number ?? 0,
        register_id: reg.id, register_name: reg.name, customer_id: customer?.id ?? reg.customer_id, customer_name: customer?.name ?? reg.customer_name,
        customer_code: '', walk_in: !customer, currency, total: String(total), tax_total: String(taxTotal), paid: String(Math.min(total, received)),
        on_account: String(Math.max(0, round(total - received, dec))), invoice_id: null, invoice_number: null, payment_id: null, refund_doc_id: null,
        refund_of: null, refund_of_number: null, cash: [], client_ref: ref, notes: '', cashier: me.full_name, created_at: new Date().toISOString(),
        company_name: cat.company.name_en, company_name_ar: cat.company.name_ar, company_tax_id: cat.company.tax_id, company_header: cat.company.doc_header,
        receipt_footer: reg.receipt_footer,
      },
      lines: priced.map((l, i) => ({ line_no: i + 1, item_id: l.item.id, description: itemName(l.item, i18n.language) + (l.discount ? ` (-${l.discount}%)` : ''),
        quantity: l.qty, unit_price: String(l.unit), amount: String(l.amount), tax_amount: String(l.tax) })),
      tenders: cat.methods.filter((m) => Number(amounts[m.id] || 0) > 0).map((m) => ({ method: m.name, method_id: m.id, currency: m.currency, amount: amounts[m.id], rate: '' })),
      change: change > 0 && cm ? { currency: cm.currency, amount: String(change), method: cm.name } : null,
    });
    const queue = () => {
      enqueue({ client_ref: ref, company_id: company.id, customer_name: customer?.name ?? t('pos.walkIn'), created_at: new Date().toISOString(), body,
        path: `/companies/${company.id}/pos/orders` });
      notifications.show({ color: 'orange', message: t('pos.savedOffline') });
      onDone(local());
    };
    if (!online) { queue(); return; }
    setBusy(true);
    try {
      const d = await api<PosOrderDetail>(`/companies/${company.id}/pos/orders`, { method: 'POST', body: { ...body, client_ref: ref } });
      onDone(d);
    } catch (e) {
      if (e instanceof ApiError && e.status < 500) notifyError(t, e);
      else queue(); // network down or server busy: keep the sale, send it later (same client_ref = stored once)
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened onClose={onClose} title={t('pos.payment')} size="lg" fullScreen={window.innerWidth < 600}>
      <Stack>
        <Paper withBorder p="md" bg="var(--mantine-color-teal-0)">
          <Group justify="space-between" align="flex-end">
            <div>
              <Text size="sm" c="dimmed">{t('pos.toPay')}</Text>
              <Text fw={800} size="2rem" ff="monospace" dir="ltr" lh={1.1}>{formatAmount(total, dec)} {currency}</Text>
            </div>
            <Stack gap={0} align="flex-end">
              {[...new Set(cat.methods.map((m) => m.currency))].filter((c) => c !== currency).map((c) => (
                <Text key={c} size="sm" ff="monospace" dir="ltr" c="dimmed">≈ {formatAmount(conv(total, currency, c), decimalsOf(c))} {c}</Text>
              ))}
            </Stack>
          </Group>
        </Paper>
        {cat.methods.map((m) => (
          <Group key={m.id} gap="xs" wrap="nowrap" align="flex-end">
            <TextInput style={{ flex: 1 }} size="md" label={m.name} dir="ltr" inputMode="decimal" placeholder="0"
              rightSection={<Text size="xs" c="dimmed">{m.currency}</Text>} rightSectionWidth={44}
              styles={{ input: { fontFamily: 'monospace', fontSize: 18 } }}
              value={amounts[m.id] ?? ''} onChange={(e) => { const v = e.currentTarget.value.replace(/[^\d.]/g, ''); setAmounts((a) => ({ ...a, [m.id]: v })); }} />
            <Button size="md" variant="light" onClick={() => exact(m)}>{t('pos.exact')}</Button>
          </Group>
        ))}
        <Paper withBorder p="md" bg="var(--mantine-color-gray-0)">
          <SimpleGrid cols={2}>
            <div>
              <Text size="xs" c="dimmed">{t('payments.received')}</Text>
              <Text fw={700} size="lg" ff="monospace" dir="ltr">{formatAmount(received, dec)} {currency}</Text>
            </div>
            <div>
              <Text size="xs" c="dimmed">{short ? t('pos.left') : t('payments.change')}</Text>
              <Text fw={800} size="xl" ff="monospace" dir="ltr" c={short ? 'red' : change > 0 ? 'orange.8' : undefined}>
                {short ? `${formatAmount(total - received, dec)} ${currency}` : cm ? `${formatAmount(change, decimalsOf(cm.currency))} ${cm.currency}` : '—'}
              </Text>
            </div>
          </SimpleGrid>
          {extra > 0.00005 && cash.length > 1 && (
            <Select mt="sm" size="sm" label={t('payments.changeIn')} value={changeId} allowDeselect={false} onChange={setChangeId}
              data={cash.map((m) => ({ value: m.id, label: `${m.name} (${m.currency})` }))} />
          )}
          {missingRate && <Text size="xs" c="red" mt="xs">{t('errors.no_rate', { detail: '' })}</Text>}
        </Paper>
        {customer ? (
          <Checkbox label={t('pos.onAccountFor', { name: customer.name })} checked={onAccount} onChange={(e) => setOnAccount(e.currentTarget.checked)} />
        ) : short && <Text size="xs" c="dimmed">{t('pos.walkInFull')}</Text>}
        {!online && <Alert color="orange" icon={<IconCloudOff size={16} />}>{t('pos.offlineSale')}</Alert>}
        <Group grow>
          <Button variant="default" size="md" onClick={onClose}>{t('common.cancel')}</Button>
          <Button size="lg" loading={busy} disabled={!canConfirm} onClick={confirm}>{t('pos.confirm')}</Button>
        </Group>
      </Stack>
    </Modal>
  );
}

// ---------- receipt after a sale ----------

function ReceiptSheet({ d, onClose, onNeedPrinter }: { d: PosOrderDetail; onClose: () => void; onNeedPrinter: () => void }) {
  const { t } = useTranslation();
  const { decimalsOf } = useCompany();
  const [busy, setBusy] = useState(false);
  async function bt() {
    setBusy(true);
    try {
      await printLines(orderReceiptLines(d, t as never, decimalsOf));
    } catch (e) {
      if (e instanceof ChoosePrinterError) { onNeedPrinter(); return; }
      notifications.show({ color: 'red', message: String((e as Error).message ?? e) });
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal opened onClose={onClose} size="sm" title={d.order.kind === 'refund' ? t('pos.refundReceipt') : t('pos.receipt')}>
      <Stack>
        {d.pending && <Alert color="orange" icon={<IconCloudOff size={16} />}>{t('collector.pendingSync')}</Alert>}
        {d.change && Number(d.change.amount) > 0 && (
          <Paper withBorder p="sm" bg="var(--mantine-color-orange-0)" className="no-print">
            <Text size="sm" c="dimmed">{t('payments.change')}</Text>
            <Text fw={800} size="1.8rem" ff="monospace" dir="ltr">{formatAmount(d.change.amount, decimalsOf(d.change.currency))} {d.change.currency}</Text>
          </Paper>
        )}
        <OrderReceipt d={d} />
        <SimpleGrid cols={bluetoothSupported() ? 2 : 1} className="no-print">
          {bluetoothSupported() && <Button leftSection={<IconBluetooth size={18} />} loading={busy} onClick={bt}>{t('collector.printBt')}</Button>}
          <Button variant="default" leftSection={<IconPrinter size={18} />} onClick={() => void printPage()}>{t('invoices.print')}</Button>
        </SimpleGrid>
        <Button size="lg" onClick={onClose} className="no-print" data-autofocus>{t('pos.newSale')}</Button>
      </Stack>
    </Modal>
  );
}

// ---------- orders of the session, returns ----------

function OrdersDrawer({ sessionId, cat, me, onClose, onReceipt }: {
  sessionId: string; cat: Catalog; me: Me; onClose: () => void; onReceipt: (d: PosOrderDetail) => void;
}) {
  const { t } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [rows, setRows] = useState<PosOrder[] | null>(null);
  const [open, setOpen] = useState<PosOrderDetail | null>(null);
  const [ret, setRet] = useState<Record<number, string> | null>(null);
  const [methodId, setMethodId] = useState<string | null>(cat.methods.find((m) => m.kind === 'cash' && m.currency === cat.register.currency)?.id ?? cat.methods[0]?.id ?? null);
  const [busy, setBusy] = useState(false);
  const canRefund = me.role !== 'cashier' || cat.register.cashier_refunds;
  const queued = outbox().filter((p) => p.path?.includes('/pos/orders') && p.company_id === company?.id);

  const load = useCallback(() => {
    if (!company) return;
    api<PosOrder[]>(`/companies/${company.id}/pos/orders?session_id=${sessionId}`).then(setRows).catch(() => setRows([]));
  }, [company, sessionId]);
  useEffect(load, [load]);

  async function show(id: string) {
    if (!company) return;
    try {
      setOpen(await api<PosOrderDetail>(`/companies/${company.id}/pos/orders/${id}`));
      setRet(null);
    } catch (e) {
      notifyError(t, e);
    }
  }

  async function refund() {
    if (!company || !open || !ret) return;
    setBusy(true);
    try {
      const lines = Object.entries(ret).filter(([, q]) => Number(q) > 0).map(([n, q]) => ({ line_no: Number(n), quantity: q }));
      const d = await api<PosOrderDetail>(`/companies/${company.id}/pos/orders/${open.order.id}/refund`, {
        method: 'POST', body: { session_id: sessionId, lines, method_id: methodId ?? '', client_ref: newClientRef() },
      });
      notifySaved(t);
      setOpen(null);
      setRet(null);
      load();
      onReceipt(d);
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Drawer opened onClose={onClose} position="right" size="md" title={t('pos.orders')}>
      {open ? (
        <Stack>
          <Button variant="subtle" size="xs" leftSection={<IconArrowBack size={14} />} style={{ alignSelf: 'flex-start' }} onClick={() => { setOpen(null); setRet(null); }}>
            {t('pos.orders')}
          </Button>
          <OrderReceipt d={open} />
          {ret === null ? (
            <Group grow>
              <Button variant="default" leftSection={<IconPrinter size={16} />} onClick={() => onReceipt(open)}>{t('pos.reprint')}</Button>
              {open.order.kind === 'sale' && canRefund && (
                <Button color="orange" variant="light" onClick={() => setRet({})}>{t('pos.return')}</Button>
              )}
            </Group>
          ) : (
            <Paper withBorder p="sm">
              <Stack gap={6}>
                <Text fw={600} size="sm">{t('pos.returnWhat')}</Text>
                {open.lines.map((l) => {
                  const left = Number(l.quantity) - Number(l.returned ?? 0);
                  return (
                    <Group key={l.line_no} wrap="nowrap" gap="xs">
                      <Text size="sm" style={{ flex: 1, minWidth: 0 }} truncate>{l.description}</Text>
                      <Text size="xs" c="dimmed">{t('pos.left')}: {left}</Text>
                      <TextInput w={80} size="xs" dir="ltr" inputMode="decimal" disabled={left <= 0} placeholder="0" value={ret[l.line_no] ?? ''}
                        onChange={(e) => { const v = e.currentTarget.value.replace(/[^\d.]/g, ''); setRet((r) => ({ ...(r ?? {}), [l.line_no]: v })); }} />
                      <Button size="compact-xs" variant="subtle" disabled={left <= 0} onClick={() => setRet((r) => ({ ...(r ?? {}), [l.line_no]: String(left) }))}>{t('pos.all')}</Button>
                    </Group>
                  );
                })}
                <Select size="sm" label={t('pos.payBackFrom')} clearable placeholder={t('gap.keepAsCredit')} value={methodId} onChange={setMethodId}
                  data={cat.methods.map((m) => ({ value: m.id, label: `${m.name} (${m.currency})` }))} />
                <Group justify="flex-end">
                  <Button variant="default" size="xs" onClick={() => setRet(null)}>{t('common.cancel')}</Button>
                  <Button color="orange" size="xs" loading={busy} disabled={!Object.values(ret).some((q) => Number(q) > 0)} onClick={refund}>{t('pos.confirmReturn')}</Button>
                </Group>
              </Stack>
            </Paper>
          )}
        </Stack>
      ) : (
        <Stack gap={6}>
          {queued.length > 0 && <Alert color="orange" icon={<IconCloudUpload size={16} />}>{t('pos.queued', { n: queued.length })}
            {queued.some((x) => x.error) && <Text size="xs" c="red">{queued.filter((x) => x.error).map((x) => t(`errors.${x.error}`, { defaultValue: x.error ?? '' })).join(' · ')}</Text>}
          </Alert>}
          {rows === null ? <Center p="md"><Loader size="sm" /></Center> : rows.length === 0 ? <Text c="dimmed" ta="center" p="md">{t('pos.noSales')}</Text> : rows.map((o) => (
            <Card key={o.id} withBorder padding="xs" style={{ cursor: 'pointer' }} onClick={() => void show(o.id)}>
              <Group justify="space-between" wrap="nowrap">
                <div style={{ minWidth: 0 }}>
                  <Text size="sm" fw={600}>#{o.number} {o.kind === 'refund' && <Badge size="xs" color="orange" variant="light">{t('pos.return')} #{o.refund_of_number}</Badge>}</Text>
                  <Text size="xs" c="dimmed" truncate>{formatDateTime(o.created_at)} · {o.walk_in ? t('pos.walkIn') : o.customer_name}</Text>
                </div>
                <Text ff="monospace" fw={600} dir="ltr" c={o.kind === 'refund' ? 'orange' : undefined}>
                  {o.kind === 'refund' ? '-' : ''}{formatAmount(o.total, decimalsOf(o.currency))} {o.currency}
                </Text>
              </Group>
            </Card>
          ))}
        </Stack>
      )}
    </Drawer>
  );
}

// ---------- close the session (count the drawer) ----------

function CloseSession({ sessionId, onClose, onClosed, onNeedPrinter }: {
  sessionId: string; onClose: () => void; onClosed: () => void; onNeedPrinter: () => void;
}) {
  const { t } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [d, setD] = useState<SessionDetail | null>(null);
  const [counted, setCounted] = useState<Record<string, string>>({});
  const [notes, setNotes] = useState('');
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState<SessionDetail | null>(null);

  useEffect(() => {
    if (company) api<SessionDetail>(`/companies/${company.id}/pos/sessions/${sessionId}`).then(setD).catch((e) => notifyError(t, e));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [company, sessionId]);

  async function close() {
    if (!company || !d) return;
    setBusy(true);
    try {
      const r = await api<SessionDetail>(`/companies/${company.id}/pos/sessions/${sessionId}/close`, {
        method: 'POST', body: { notes, counted: d.drawer.map((m) => ({ method_id: m.method_id, amount: counted[m.method_id] ?? '0' })) },
      });
      setDone(r); // the till goes back to 'open a session' when this Z report is closed
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  async function bt(x: SessionDetail) {
    try {
      await printLines(zReportLines(x, t as never, decimalsOf));
    } catch (e) {
      if (e instanceof ChoosePrinterError) { onNeedPrinter(); return; }
      notifications.show({ color: 'red', message: String((e as Error).message ?? e) });
    }
  }

  const finish = () => { if (done) onClosed(); onClose(); };
  return (
    <Modal opened onClose={finish} size="lg" title={done ? t('pos.zReport') : t('pos.closeSession')}>
      {done ? (
        <Stack>
          <SessionReport id={sessionId} data={done} />
          <Group grow className="no-print">
            {bluetoothSupported() && <Button leftSection={<IconBluetooth size={16} />} onClick={() => void bt(done)}>{t('collector.printBt')}</Button>}
            <Button variant="default" leftSection={<IconPrinter size={16} />} onClick={() => void printPage()}>{t('invoices.print')}</Button>
          </Group>
          <Button onClick={finish} className="no-print">{t('common.close')}</Button>
        </Stack>
      ) : !d ? <Center p="xl"><Loader size="sm" /></Center> : (
        <Stack>
          <SessionReport id={sessionId} data={d} />
          <Text fw={600} size="sm" className="no-print">{t('pos.countHint')}</Text>
          {d.drawer.map((m) => {
            const md = decimalsOf(m.currency);
            const c = counted[m.method_id];
            const diff = c === undefined || c === '' ? null : Number(c) - Number(m.expected);
            return (
              <Group key={m.method_id} gap="xs" wrap="nowrap" align="flex-end" className="no-print">
                <TextInput style={{ flex: 1 }} label={`${m.name} · ${t('pos.expected')} ${formatAmount(m.expected, md)}`} dir="ltr" inputMode="decimal"
                  rightSection={<Text size="xs" c="dimmed">{m.currency}</Text>} rightSectionWidth={44} value={c ?? ''}
                  onChange={(e) => { const v = e.currentTarget.value.replace(/[^\d.]/g, ''); setCounted((x) => ({ ...x, [m.method_id]: v })); }} />
                <Text w={110} ta="end" ff="monospace" dir="ltr" mb={8} c={diff === null ? 'dimmed' : diff < 0 ? 'red' : diff > 0 ? 'teal' : undefined}>
                  {diff === null ? '—' : `${diff > 0 ? '+' : ''}${formatAmount(diff, md)}`}
                </Text>
              </Group>
            );
          })}
          <TextInput label={t('pos.notes')} value={notes} onChange={(e) => setNotes(e.currentTarget.value)} className="no-print" />
          <Group justify="space-between" className="no-print">
            <Button variant="default" leftSection={<IconPrinter size={16} />} onClick={() => (bluetoothSupported() ? void bt(d) : void printPage())}>{t('pos.xReport')}</Button>
            <Group gap="xs">
              <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
              <Button color="red" loading={busy} disabled={d.drawer.some((m) => (counted[m.method_id] ?? '') === '')} onClick={close}>{t('pos.closeSession')}</Button>
            </Group>
          </Group>
        </Stack>
      )}
    </Modal>
  );
}
