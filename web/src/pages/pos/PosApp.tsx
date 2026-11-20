import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  ActionIcon, Alert, Badge, Box, Button, Card, Center, Checkbox, Drawer, Grid, Group, Image, Loader, Menu, Modal, Paper, ScrollArea,
  Select, SimpleGrid, Stack, Text, TextInput, UnstyledButton,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import {
  IconArrowBack, IconBarcode, IconBluetooth, IconCamera, IconCash, IconCloudOff, IconCloudUpload, IconLogout, IconMenu2, IconMinus,
  IconPlus, IconPrinter, IconReceipt, IconRefresh, IconSearch, IconShoppingCart, IconTrash, IconUser, IconUserPlus, IconX,
  IconDiscount2, IconGift, IconStar, IconPlayerPause, IconDeviceDesktop, IconTicket,
} from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { api, ApiError, type Me } from '../../api';
import { useCompany } from '../../company';
import { applyLang, languages } from '../../i18n';
import { companyName, formatAmount, formatDateTime, notifyError, notifySaved, roundChange, today } from '../../util';
import StatusBar from '../../components/StatusBar';
import '../../magic.css';
import { printPage } from '../../native';
import Logo from '../../components/Logo';
import {
  bluetoothSupported, ChoosePrinterError, connectPrinter, isNativeApp, kickDrawer, paperWidth, printerName, printLines, setPaper,
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

interface GiftLine { code: string; amount: string }
interface PromoEval { lines: string[]; line_promos: string[][]; applied: { id: string; name: string; name_i18n?: Record<string, string>; discount: string }[]; total: string; coupon_valid: boolean | null }
interface Member { id: string; code: string; name: string; points: string; tier: string; point_value: string; min_redeem: string; active: boolean; value?: string }

/** Scale / in-store barcode (EAN-13 starting 20-29): prefix + PLU (5) + weight or price (5) + check digit. */
export function parseScale(code: string, prefixes: { prefix: string; mode: string; decimals: number }[]): { plu: number; qty?: number; price?: number } | null {
  if (!/^2\d{12}$/.test(code)) return null;
  const p = prefixes.find((x) => code.startsWith(x.prefix));
  if (!p) return null;
  const plu = Number(code.slice(2, 7));
  const v = Number(code.slice(7, 12)) / 10 ** (p.decimals ?? (p.mode === 'price' ? 2 : 3));
  return p.mode === 'price' ? { plu, price: v } : { plu, qty: v };
}

const displayChannel = (): BroadcastChannel | null => { try { return new BroadcastChannel('paxve-pos-display'); } catch { return null; } };

/** The customer-facing second screen: shows the basket the till broadcasts (same browser). */
export function CustomerDisplay() {
  const { t } = useTranslation();
  const [s, setS] = useState<{ lines: { name: string; qty: string; amount: string }[]; total: string; currency: string; saved: string; company: string } | null>(null);
  useEffect(() => {
    const ch = displayChannel();
    if (!ch) return;
    ch.onmessage = (e) => setS(e.data);
    ch.postMessage({ hello: true });
    return () => ch.close();
  }, []);
  return (
    <Box mih="100vh" p="xl" bg="var(--mantine-color-dark-8)" c="white">
      <Text fw={700} size="xl" ta="center">{s?.company ?? 'Paxve'}</Text>
      {!s || s.lines.length === 0 ? <Center h="70vh"><Text size="2rem">{t('rt.pos.welcome')}</Text></Center> : (
        <Stack mt="lg">
          {s.lines.map((l, i) => (
            <Group key={i} justify="space-between"><Text size="xl">{Number(l.qty)} × {l.name}</Text><Text size="xl" ff="monospace" dir="ltr">{l.amount}</Text></Group>
          ))}
          {Number(s.saved) > 0 && <Group justify="space-between"><Text size="xl" c="teal.3">{t('rt.pos.saved')}</Text><Text size="xl" c="teal.3" ff="monospace" dir="ltr">−{s.saved}</Text></Group>}
          <Group justify="space-between" mt="xl"><Text fw={800} size="3rem">{t('pos.total')}</Text><Text fw={800} size="3rem" ff="monospace" dir="ltr">{s.total} {s.currency}</Text></Group>
        </Stack>
      )}
    </Box>
  );
}

/** Point of sale screen (cashiers see only this; office users open it from the menu). */
export default function PosApp({ me, onLogout }: { me: Me; onLogout: () => void }) {
  const { t, i18n } = useTranslation();
  const { company, companies, setCompanyId, decimalsOf } = useCompany();
  const online = useOnline();
  const pending = usePending(company?.id);
  const pickedCompany = useRef(false); // the cashier chose a company by hand: never jump away from it
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
  const [giftSold, setGiftSold] = useState<GiftLine[]>([]);
  const [giftOpen, setGiftOpen] = useState(false);
  const [coupon, setCoupon] = useState('');
  const [couponOpen, setCouponOpen] = useState(false);
  const [promo, setPromo] = useState<PromoEval | null>(null);
  const [member, setMember] = useState<Member | null>(null);
  const [heldOpen, setHeldOpen] = useState(false);
  const [quick, setQuick] = useState<string | null>(null);
  const [variantOf, setVariantOf] = useState<string | null>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const cid = company?.id;
  const office = me.role !== 'cashier';
  const lang = i18n.language;

  // registers
  const loadRegisters = useCallback(() => {
    if (!cid) return;
    api<Register[]>(`/companies/${cid}/pos/registers`).then(async (rs) => {
      const active = rs.filter((r) => r.is_active);
      // audit B-H1: a cashier who can open several companies lands on the one that has a till
      if (active.length === 0 && !pickedCompany.current && companies.length > 1) {
        pickedCompany.current = true;
        for (const c of companies) {
          if (c.id === cid) continue;
          const other = await api<Register[]>(`/companies/${c.id}/pos/registers`).catch(() => [] as Register[]);
          if (other.some((r) => r.is_active)) { setCompanyId(c.id); return; }
        }
      }
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
  }, [cid, companies, setCompanyId]);
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

  const byPlu = useMemo(() => {
    const m = new Map<number, CatItem>();
    for (const it of items) if (it.plu) m.set(it.plu, it);
    return m;
  }, [items]);
  const templates = useMemo(() => (cat?.templates ?? []).filter((tp) => items.some((it) => it.template_id === tp.id)), [cat, items]);

  const shown = useMemo(() => {
    const s = q.trim().toLowerCase();
    return items.filter((it) => (!catFilter || it.category_id === catFilter) && (s !== '' || !it.template_id) &&
      (!s || itemName(it, lang).toLowerCase().includes(s) || it.name.toLowerCase().includes(s) || it.code.toLowerCase().startsWith(s) || it.barcode === s));
  }, [items, q, catFilter, lang]);

  const add = useCallback((it: CatItem, qty = 1, price = '') => {
    setCart((c) => {
      const i = price === '' && !it.sold_by_weight ? c.findIndex((l) => l.item.id === it.id && l.price === '' && l.discount === '') : -1;
      if (i >= 0) return c.map((l, j) => (j === i ? { ...l, qty: String(Number(l.qty) + qty) } : l));
      return [...c, { key: lineKey++, item: it, qty: String(qty), price, discount: '' }];
    });
  }, []);

  const office0 = me.role !== 'cashier';
  const scan = useCallback((code: string) => {
    const c = code.trim();
    const it = byBarcode.get(c) ?? byBarcode.get(c.toLowerCase());
    if (it) {
      add(it);
      return true;
    }
    // a scale label: PLU + weight (or price) in the barcode
    const sc = parseScale(c, cat?.retail?.scale?.prefixes ?? []);
    const w = sc ? byPlu.get(sc.plu) : undefined;
    if (sc && w) {
      if (sc.qty !== undefined) add(w, Math.round(sc.qty * 1000) / 1000);
      else if (sc.price !== undefined && Number(w.price) > 0) add(w, Math.round((sc.price / Number(w.price)) * 1000) / 1000);
      return true;
    }
    if (office0 || cat?.register.cashier_quick_item) { setQuick(c); return true; }
    notifications.show({ color: 'orange', message: `${t('pos.notFound')}: ${code}` });
    return false;
  }, [byBarcode, byPlu, add, t, cat, office0]);

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

  // the named customer's price list (same rule as the server: best quantity break in the till currency)
  const [plist, setPlist] = useState<{ name: string | null; items: { item_id: string; min_qty: string; price: string }[] }>({ name: null, items: [] });
  useEffect(() => {
    if (!cid || !customer || !online) { setPlist({ name: null, items: [] }); return; }
    api<typeof plist>(`/companies/${cid}/pos/price-list?customer_id=${customer.id}&currency=${currency}`).then(setPlist).catch(() => setPlist({ name: null, items: [] }));
  }, [cid, customer, currency, online]);
  const listPrice = (itemId: string, qty: number): number | null => {
    let best: { q: number; p: number } | null = null;
    for (const x of plist.items) {
      if (x.item_id === itemId && Number(x.min_qty) <= qty && (!best || Number(x.min_qty) > best.q)) best = { q: Number(x.min_qty), p: Number(x.price) };
    }
    return best ? best.p : null;
  };

  const gross = cart.map((l) => {
    const price = l.price !== '' ? Number(l.price) : (listPrice(l.item.id, Number(l.qty || 0)) ?? Number(l.item.price));
    const disc = Number(l.discount || 0);
    return { ...l, unit: price, before: round(Number(l.qty || 0) * price * (100 - disc) / 100, dec) };
  });
  // live promotions (the server applies exactly the same rules when the sale is saved)
  const promoKey = JSON.stringify([gross.map((l) => [l.item.id, l.qty, l.before]), coupon, customer?.id]);
  const usePromos = online && !!cat?.retail?.promotions;
  useEffect(() => {
    if (!cid || !usePromos || !gross.length) { setPromo(null); return; }
    const h = window.setTimeout(() => {
      api<PromoEval>(`/companies/${cid}/retail/promotions/evaluate`, { method: 'POST', body: {
        currency, register_id: regId ?? '', customer_id: customer?.id ?? '', coupon,
        lines: gross.map((l) => ({ item_id: l.item.id, quantity: l.qty, amount: String(l.before) })) } }).then(setPromo).catch(() => setPromo(null));
    }, 250);
    return () => window.clearTimeout(h);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [promoKey, usePromos, cid, currency, regId]);
  const promoOk = !!promo && promo.lines.length === gross.length;
  const priced = gross.map((l, i) => {
    const pd = promoOk ? Number(promo!.lines[i]) : 0;
    const amount = round(l.before - pd, dec);
    const tax = l.item.tax_rate ? round(amount * Number(l.item.tax_rate) / 100, dec) : 0;
    return { ...l, amount, tax, promoDisc: pd, promoNames: promoOk ? promo!.line_promos[i] : [] };
  });
  const giftTotal = giftSold.reduce((s, g) => s + Number(g.amount || 0), 0);
  const subtotal = priced.reduce((s, l) => s + l.amount, 0) + giftTotal;
  const taxTotal = priced.reduce((s, l) => s + l.tax, 0);
  const total = round(subtotal + taxTotal, dec);
  const saved = promoOk ? Number(promo!.total) : 0;
  const count = cart.reduce((s, l) => s + Number(l.qty || 0), 0);

  // the customer display (second screen in the same browser)
  const chan = useMemo(() => displayChannel(), []);
  useEffect(() => () => chan?.close(), [chan]);
  useEffect(() => {
    chan?.postMessage({ company: company?.display ?? company?.name_en ?? '', currency, total: formatAmount(total, dec), saved: formatAmount(saved, dec),
      lines: [...priced.map((l) => ({ name: itemName(l.item, lang), qty: l.qty, amount: formatAmount(l.amount + l.tax, dec) })),
        ...giftSold.map((g) => ({ name: `${t('rt.pos.giftCard')} ${g.code}`, qty: '1', amount: formatAmount(Number(g.amount), dec) }))] });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [chan, total, saved, cart.length, giftSold.length, promoKey]);

  // loyalty card of the chosen customer
  useEffect(() => {
    const code = (customer as { code?: string } | null)?.code;
    if (!cid || !customer || !online || !code || !cat?.retail?.loyalty?.is_active) { setMember(null); return; }
    api<Member>(`/companies/${cid}/retail/loyalty/lookup?code=${encodeURIComponent(code)}`).then(setMember).catch(() => setMember(null));
  }, [cid, customer, online, cat]);

  async function hold() {
    if (!cid || !regId || (!cart.length && !giftSold.length)) return;
    try {
      await api(`/companies/${cid}/pos/held`, { method: 'POST', body: { register_id: regId, label: customer?.name ?? '', payload: { cart, customer, giftSold, coupon } } });
      setCart([]); setCustomer(null); setGiftSold([]); setCoupon('');
      notifications.show({ color: 'teal', message: t('rt.pos.held') });
    } catch (e) { notifyError(t, e); }
  }

  // keyboard shortcuts: F2 search · F4 customer · F7 coupon · F8 hold · F9 pay
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (payOpen || closeOpen || editLine) return;
      if (e.key === 'F2') { e.preventDefault(); searchRef.current?.focus(); }
      else if (e.key === 'F4') { e.preventDefault(); setCustOpen(true); }
      else if (e.key === 'F7') { e.preventDefault(); setCouponOpen(true); }
      else if (e.key === 'F8') { e.preventDefault(); void hold(); }
      else if (e.key === 'F9' && (cart.length || giftSold.length) && total > 0) { e.preventDefault(); setPayOpen(true); }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  });

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
          {companies.length > 1 && (
            <select aria-label={t('mx.company')} value={company.id} style={{ maxWidth: 180 }}
              onChange={(e) => { pickedCompany.current = true; setCompanyId(e.currentTarget.value); setRegisters(null); setRegId(null); setSessionId(null); setCat(null); setCart([]); }}>
              {companies.map((c) => <option key={c.id} value={c.id}>{companyName(c, lang)}</option>)}
            </select>
          )}
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
              <Menu.Label>{me.full_name} · {companyName(company, lang)}</Menu.Label>
              {sessionId && <Menu.Item leftSection={<IconReceipt size={14} />} onClick={() => setOrdersOpen(true)}>{t('pos.orders')}</Menu.Item>}
              {sessionId && <Menu.Item leftSection={<IconCash size={14} />} disabled={!online || pending > 0} onClick={() => setCloseOpen(true)}>{t('pos.closeSession')}</Menu.Item>}
              {(registers?.length ?? 0) > 1 && <Menu.Item leftSection={<IconRefresh size={14} />} onClick={() => choose(null)}>{t('pos.changeRegister')}</Menu.Item>}
              <Menu.Item leftSection={<IconRefresh size={14} />} onClick={loadCatalog}>{t('pos.reload')}</Menu.Item>
              <Menu.Item leftSection={<IconDeviceDesktop size={14} />} onClick={() => window.open(`${window.location.pathname}#/pos-display`, 'paxve-display', 'width=900,height=600')}>{t('rt.pos.display')}</Menu.Item>
              {sessionId && <Menu.Item leftSection={<IconPlayerPause size={14} />} onClick={() => setHeldOpen(true)}>{t('rt.pos.heldList')}</Menu.Item>}
              <Menu.Label>{t('rt.pos.keys')}</Menu.Label>
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
        <StatusBar me={me} keys={false} sticky />
      </Box>
    );
  }
  if (!cat) return <Box mih="100vh">{header}<Center p="xl"><Loader /></Center></Box>;

  // ---- no open session: open one ----
  if (!sessionId) {
    return (
      <Box mih="100vh" bg="var(--mantine-color-gray-0)" style={{ display: 'flex', flexDirection: 'column' }}>
        {header}
        <OpenSession cat={cat} online={online} onOpened={(id) => { setSessionId(id); cacheSet(`pos.session.${regId}`, id); loadCatalog(); }} />
        <div style={{ flex: 1 }} />
        <StatusBar me={me} keys={false} />
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
            {customer && plist.name && <Text size="xs" c="teal" truncate>{t('pos.priceList', { name: plist.name })}</Text>}
            {member && <Badge size="sm" color="yellow" leftSection={<IconStar size={10} />}>{Number(member.points)}{member.tier ? ` · ${member.tier}` : ''}</Badge>}
          </Group>
        </UnstyledButton>
        {customer && <ActionIcon size="sm" variant="subtle" color="gray" onClick={() => setCustomer(null)} aria-label="x"><IconX size={14} /></ActionIcon>}
      </Group>
      <Group gap={4} wrap="wrap">
        <Button size="compact-xs" variant="light" color="grape" leftSection={<IconGift size={12} />} onClick={() => setGiftOpen(true)}>{t('rt.pos.sellGift')}</Button>
        {usePromos && <Button size="compact-xs" variant={coupon ? 'filled' : 'light'} color="pink" leftSection={<IconTicket size={12} />} onClick={() => setCouponOpen(true)}>{coupon || t('rt.pos.coupon')}</Button>}
        <Button size="compact-xs" variant="light" color="gray" leftSection={<IconPlayerPause size={12} />} disabled={!online || (!cart.length && !giftSold.length)} onClick={() => void hold()}>{t('rt.pos.hold')}</Button>
        <Button size="compact-xs" variant="subtle" color="gray" disabled={!online} onClick={() => setHeldOpen(true)}>{t('rt.pos.heldList')}</Button>
      </Group>
      {promo && coupon && promo.coupon_valid === false && <Text size="xs" c="red">{t('rt.pos.badCoupon')}</Text>}
      <ScrollArea style={{ flex: 1 }} mih={120}>
        {priced.length === 0 && giftSold.length === 0 ? <Text c="dimmed" ta="center" size="sm" p="lg">{t('pos.emptyCart')}</Text> : (
          <Stack gap={4}>
            {priced.map((l) => (
              <Paper key={l.key} withBorder p={6} radius="sm">
                <Group justify="space-between" wrap="nowrap" gap={4}>
                  <UnstyledButton style={{ flex: 1, minWidth: 0 }} onClick={() => setEditLine(l)}>
                    <Text size="sm" fw={500} truncate>{itemName(l.item, lang)}</Text>
                    <Text size="xs" c="dimmed" dir="ltr" ta="start">{formatAmount(l.unit, dec)}{l.item.sold_by_weight ? ` / ${l.item.unit}` : ''}{l.discount ? ` −${l.discount}%` : ''}{l.tax ? ` +${l.item.tax_name ?? ''}` : ''}</Text>
                    {l.promoDisc > 0 && <Text size="xs" c="pink" truncate><IconDiscount2 size={10} /> {l.promoNames.join(', ')} −{formatAmount(l.promoDisc, dec)}</Text>}
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
            {giftSold.map((g, i) => (
              <Paper key={'g' + i} withBorder p={6} radius="sm" bg="var(--mantine-color-grape-0)">
                <Group justify="space-between" wrap="nowrap">
                  <Text size="sm"><IconGift size={14} /> {t('rt.pos.giftCard')} <span dir="ltr">{g.code || t('rt.pos.newCode')}</span></Text>
                  <Group gap={4} wrap="nowrap">
                    <Text ff="monospace" size="sm" dir="ltr">{formatAmount(Number(g.amount), dec)}</Text>
                    <ActionIcon size="sm" variant="subtle" color="red" onClick={() => setGiftSold(giftSold.filter((_, j) => j !== i))} aria-label="x"><IconX size={12} /></ActionIcon>
                  </Group>
                </Group>
              </Paper>
            ))}
          </Stack>
        )}
      </ScrollArea>
      <Paper withBorder p="sm" bg="var(--mantine-color-gray-0)">
        {saved > 0 && <Group justify="space-between"><Text size="sm" c="pink" fw={600}>{t('rt.pos.saved')}</Text><Text size="sm" c="pink" fw={600} ff="monospace" dir="ltr">−{formatAmount(saved, dec)}</Text></Group>}
        {taxTotal > 0 && <Group justify="space-between"><Text size="sm">{t('pos.tax')}</Text><Text size="sm" ff="monospace" dir="ltr">{formatAmount(taxTotal, dec)}</Text></Group>}
        <Group justify="space-between"><Text fw={700}>{t('pos.total')}</Text><Text fw={700} size="xl" ff="monospace" dir="ltr">{formatAmount(total, dec)} {currency}</Text></Group>
      </Paper>
      <Group grow>
        <Button variant="default" color="red" leftSection={<IconTrash size={16} />} disabled={!cart.length && !giftSold.length} onClick={() => { setCart([]); setCustomer(null); setGiftSold([]); setCoupon(''); }}>{t('pos.clear')}</Button>
        <Button size="md" leftSection={<IconCash size={18} />} disabled={(!cart.length && !giftSold.length) || total <= 0} onClick={() => { setCartOpen(false); setPayOpen(true); }}>{t('pos.pay')} <Text span size="xs" ml={4} opacity={0.7}>F9</Text></Button>
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
                {!q.trim() && templates.filter((tp) => !catFilter || tp.category_id === catFilter).map((tp) => (
                  <Card key={'t' + tp.id} withBorder padding={6} radius="md" style={{ cursor: 'pointer', userSelect: 'none' }} onClick={() => setVariantOf(tp.id)}>
                    {tp.image ? <Image src={tp.image} h={80} fit="cover" radius="sm" alt="" />
                      : <Center h={80} bg="var(--mantine-color-indigo-0)" style={{ borderRadius: 6 }}><Text fw={700} size="xl" c="indigo">{itemName(tp, lang).slice(0, 2).toUpperCase()}</Text></Center>}
                    <Text size="sm" fw={500} lineClamp={2} mt={4} lh={1.2} mih={34}>{itemName(tp, lang)}</Text>
                    <Text size="xs" c="dimmed">{tp.attributes.map((a) => a.name).join(' × ')}</Text>
                  </Card>
                ))}
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
        <Button fullWidth size="lg" leftSection={<IconShoppingCart size={20} />} onClick={() => setCartOpen(true)} disabled={!cart.length && !giftSold.length}
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
          giftSold={giftSold} coupon={coupon} promos={usePromos && promoOk} member={member}
          onClose={() => setPayOpen(false)}
          onDone={(d) => {
            setPayOpen(false); setCart([]); setCustomer(null); setGiftSold([]); setCoupon(''); setReceipt(d);
            if (cat.register.drawer_kick && d.order.cash.some((x) => Number(x.amount) !== 0)) void kickDrawer().catch(() => undefined);
            if (!d.pending) loadCatalog();
          }} />
      )}
      {giftOpen && <GiftSellDialog dec={dec} currency={currency} onClose={() => setGiftOpen(false)} onAdd={(g) => { setGiftSold([...giftSold, g]); setGiftOpen(false); }} />}
      {couponOpen && (
        <Modal opened onClose={() => setCouponOpen(false)} title={t('rt.pos.coupon')} size="xs">
          <Stack>
            <TextInput dir="ltr" data-autofocus defaultValue={coupon} id="pos-coupon" onKeyDown={(e) => { if (e.key === 'Enter') { setCoupon(e.currentTarget.value.trim().toUpperCase()); setCouponOpen(false); } }} />
            <Group grow>
              <Button variant="default" onClick={() => { setCoupon(''); setCouponOpen(false); }}>{t('rt.pos.removeCoupon')}</Button>
              <Button onClick={() => { setCoupon(((document.getElementById('pos-coupon') as HTMLInputElement | null)?.value ?? '').trim().toUpperCase()); setCouponOpen(false); }}>{t('common.save')}</Button>
            </Group>
          </Stack>
        </Modal>
      )}
      {heldOpen && regId && (
        <HeldDrawer registerId={regId} onClose={() => setHeldOpen(false)} onResume={(p) => {
          setCart(p.cart ?? []); setCustomer(p.customer ?? null); setGiftSold(p.giftSold ?? []); setCoupon(p.coupon ?? ''); setHeldOpen(false);
        }} />
      )}
      {quick !== null && (
        <QuickItemDialog barcode={quick} registerId={regId ?? ''} categories={cat.categories} currency={currency} onClose={() => setQuick(null)}
          onCreated={(it) => { setCat((c) => (c ? { ...c, items: [...c.items, it] } : c)); add(it); setQuick(null); }} />
      )}
      {variantOf && (
        <Modal opened onClose={() => setVariantOf(null)} title={itemName(templates.find((x) => x.id === variantOf) ?? { name: '', name_i18n: null }, lang)} size="lg">
          <SimpleGrid cols={{ base: 2, sm: 3 }} spacing="xs">
            {items.filter((it) => it.template_id === variantOf).map((it) => (
              <Button key={it.id} variant="light" h="auto" py={6} onClick={() => { add(it); setVariantOf(null); }} disabled={it.kind === 'stock' && Number(it.stock) <= 0}>
                <Stack gap={0} align="center">
                  <Text size="sm" fw={600}>{Object.values(it.variant_values ?? {}).join(' / ')}</Text>
                  <Text size="xs" ff="monospace" dir="ltr">{formatAmount(it.price, dec)} · {Number(it.stock)}</Text>
                </Stack>
              </Button>
            ))}
          </SimpleGrid>
        </Modal>
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
      <StatusBar me={me} keys={false} sticky />
    </Box>
  );
}

// ---------- retail: gift card sale, held baskets, quick item ----------

function GiftSellDialog({ dec, currency, onClose, onAdd }: { dec: number; currency: string; onClose: () => void; onAdd: (g: GiftLine) => void }) {
  const { t } = useTranslation();
  const [g, setG] = useState({ code: '', amount: '' });
  return (
    <Modal opened onClose={onClose} title={t('rt.pos.sellGift')} size="sm">
      <Stack>
        <TextInput label={t('rt.pos.giftCode')} dir="ltr" placeholder={t('rt.pos.newCode')} value={g.code} onChange={(e) => setG({ ...g, code: e.currentTarget.value.toUpperCase().replace(/[^A-Z0-9-]/g, '') })} />
        <TextInput label={`${t('rt.amount')} (${currency})`} dir="ltr" inputMode="decimal" data-autofocus placeholder={formatAmount(0, dec)} value={g.amount}
          onChange={(e) => setG({ ...g, amount: e.currentTarget.value.replace(/[^\d.]/g, '') })} />
        <Group grow>
          {[10, 25, 50, 100].map((v) => <Button key={v} variant="light" size="xs" onClick={() => setG({ ...g, amount: String(v) })}>{v}</Button>)}
        </Group>
        <Text size="xs" c="dimmed">{t('rt.pos.giftHint')}</Text>
        <Button disabled={!(Number(g.amount) > 0)} onClick={() => onAdd(g)}>{t('rt.pos.addToCart')}</Button>
      </Stack>
    </Modal>
  );
}

interface Held { id: string; label: string; created_at: string; created_by_name: string | null; payload: { cart?: CartLine[]; customer?: { id: string; name: string } | null; giftSold?: GiftLine[]; coupon?: string } }

function HeldDrawer({ registerId, onClose, onResume }: { registerId: string; onClose: () => void; onResume: (p: Held['payload']) => void }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const [rows, setRows] = useState<Held[] | null>(null);
  useEffect(() => {
    if (company) api<Held[]>(`/companies/${company.id}/pos/held?register_id=${registerId}`).then(setRows).catch(() => setRows([]));
  }, [company, registerId]);
  async function resume(h: Held) {
    if (!company) return;
    try { await api(`/companies/${company.id}/pos/held/${h.id}`, { method: 'DELETE' }); onResume(h.payload); } catch (e) { notifyError(t, e); }
  }
  return (
    <Drawer opened onClose={onClose} position="right" size="sm" title={t('rt.pos.heldList')}>
      {rows === null ? <Center p="md"><Loader size="sm" /></Center> : rows.length === 0 ? <Text c="dimmed" ta="center">{t('rt.pos.noHeld')}</Text> : (
        <Stack gap={6}>
          {rows.map((h) => (
            <Card key={h.id} withBorder padding="xs" style={{ cursor: 'pointer' }} onClick={() => void resume(h)}>
              <Text size="sm" fw={600}>{h.label || t('pos.walkIn')} · {(h.payload.cart ?? []).length} {t('rt.pos.lines')}</Text>
              <Text size="xs" c="dimmed">{formatDateTime(h.created_at)}{h.created_by_name ? ` · ${h.created_by_name}` : ''}</Text>
            </Card>
          ))}
        </Stack>
      )}
    </Drawer>
  );
}

function QuickItemDialog({ barcode, registerId, categories, currency, onClose, onCreated }: {
  barcode: string; registerId: string; categories: { id: string; name: string; name_i18n: Record<string, string> | null }[]; currency: string;
  onClose: () => void; onCreated: (it: CatItem) => void;
}) {
  const { t, i18n } = useTranslation();
  const { company } = useCompany();
  const [f, setF] = useState({ name: '', price: '', category_id: '' });
  const [busy, setBusy] = useState(false);
  async function save() {
    if (!company) return;
    setBusy(true);
    try {
      onCreated(await api<CatItem>(`/companies/${company.id}/pos/quick-item`, { method: 'POST', body: { register_id: registerId, barcode, ...f } }));
    } catch (e) { notifyError(t, e); } finally { setBusy(false); }
  }
  return (
    <Modal opened onClose={onClose} title={t('rt.pos.quickItem')} size="sm">
      <Stack>
        <Text size="sm">{t('pos.notFound')}: <b dir="ltr">{barcode}</b></Text>
        <TextInput label={t('rt.name')} data-autofocus value={f.name} onChange={(e) => setF({ ...f, name: e.currentTarget.value })} />
        <TextInput label={`${t('rt.price')} (${currency})`} dir="ltr" inputMode="decimal" value={f.price} onChange={(e) => setF({ ...f, price: e.currentTarget.value.replace(/[^\d.]/g, '') })} />
        {categories.length > 0 && <Select label={t('rt.rep.category')} clearable value={f.category_id || null} onChange={(v) => setF({ ...f, category_id: v ?? '' })}
          data={categories.map((c) => ({ value: c.id, label: itemName(c, i18n.language) }))} />}
        <Group grow>
          <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
          <Button loading={busy} disabled={!f.name.trim() || f.price === ''} onClick={save}>{t('rt.pos.createAndAdd')}</Button>
        </Group>
      </Stack>
    </Modal>
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

function CustomerPicker({ onClose, onPick }: { onClose: () => void; onPick: (c: { id: string; name: string; code?: string }) => void }) {
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
          <TextInput leftSection={<IconSearch size={16} />} placeholder={t('rt.pos.custSearch')} value={q} data-autofocus onChange={(e) => setQ(e.currentTarget.value)}
            onKeyDown={(e) => {
              if (e.key !== 'Enter' || !company || q.trim().length < 4) return;
              api<{ id: string; code: string; name: string }>(`/companies/${company.id}/retail/loyalty/lookup?code=${encodeURIComponent(q.trim())}`).then((c) => onPick(c)).catch(() => undefined);
            }} />
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

interface Priced extends CartLine { unit: number; amount: number; tax: number; promoDisc?: number; promoNames?: string[] }

function PayDialog({ cat, sessionId, customer, total, taxTotal, priced, online, me, giftSold, coupon, promos, member, onClose, onDone }: {
  cat: Catalog; sessionId: string; customer: { id: string; name: string } | null; total: number; taxTotal: number; priced: Priced[];
  online: boolean; me: Me; giftSold: GiftLine[]; coupon: string; promos: boolean; member: Member | null; onClose: () => void; onDone: (d: PosOrderDetail) => void;
}) {
  const [giftCode, setGiftCode] = useState('');
  const [giftAmt, setGiftAmt] = useState('');
  const [giftInfo, setGiftInfo] = useState<{ balance: string; currency: string; status: string; expired: boolean } | null>(null);
  const [points, setPoints] = useState('');
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

  // gift card and loyalty points paid on this sale (till currency)
  const giftPay = giftInfo && !giftInfo.expired && giftInfo.status === 'active' && giftInfo.currency === currency ? Math.min(Number(giftAmt || 0), Number(giftInfo.balance)) : 0;
  const pointsVal = member && Number(points) > 0 && Number(points) <= Number(member.points) && Number(points) >= Number(member.min_redeem)
    ? round(conv(Number(points) * Number(member.point_value), base, currency), dec) : 0;
  async function checkGift() {
    if (!company || !giftCode.trim()) return;
    try {
      const g = await api<{ balance: string; currency: string; status: string; expired: boolean }>(`/companies/${company.id}/retail/gift-cards/lookup?code=${encodeURIComponent(giftCode.trim())}`);
      setGiftInfo(g);
      if (!giftAmt) setGiftAmt(String(Math.min(Number(g.balance), total)));
    } catch (e) { setGiftInfo(null); notifyError(t, e); }
  }
  let received = giftPay + pointsVal;
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
      apply_promos: online && promos, coupon: online && promos ? coupon : '',
      gift_sold: giftSold.map((g) => ({ code: g.code, amount: g.amount })),
      gift_redeem: giftPay > 0 ? [{ code: giftCode.trim(), amount: String(giftPay) }] : [],
      redeem_points: pointsVal > 0 ? points : '',
    };
    if (!online && (giftPay > 0 || pointsVal > 0 || giftSold.length)) { notifications.show({ color: 'red', message: t('rt.pos.needOnline') }); return; }
    const local = (): PosOrderDetail => ({
      pending: true,
      order: {
        id: ref, number: 0, kind: 'sale', order_date: today(), session_id: sessionId, session_number: reg.open_session?.number ?? 0,
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
        {online && (
          <Group gap="xs" wrap="nowrap" align="flex-end">
            <TextInput style={{ flex: 1 }} size="sm" label={t('rt.pos.payGift')} dir="ltr" placeholder="GC-…" value={giftCode}
              leftSection={<IconGift size={14} />} onChange={(e) => { setGiftCode(e.currentTarget.value.toUpperCase()); setGiftInfo(null); }} onBlur={() => void checkGift()} />
            <TextInput w={110} size="sm" dir="ltr" inputMode="decimal" placeholder="0" value={giftAmt} disabled={!giftInfo}
              onChange={(e) => setGiftAmt(e.currentTarget.value.replace(/[^\d.]/g, ''))}
              description={giftInfo ? `${formatAmount(giftInfo.balance, decimalsOf(giftInfo.currency))} ${giftInfo.currency}${giftInfo.status !== 'active' || giftInfo.expired ? ' ✕' : ''}` : undefined} />
          </Group>
        )}
        {online && member?.active && Number(member.points) > 0 && (
          <Group gap="xs" wrap="nowrap" align="flex-end">
            <TextInput style={{ flex: 1 }} size="sm" leftSection={<IconStar size={14} />} label={t('rt.pos.payPoints', { points: Number(member.points), min: Number(member.min_redeem) })}
              dir="ltr" inputMode="numeric" value={points} onChange={(e) => setPoints(e.currentTarget.value.replace(/\D/g, ''))} />
            <Text w={110} ff="monospace" dir="ltr" mb={6}>= {formatAmount(pointsVal, dec)} {currency}</Text>
          </Group>
        )}
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
  const [denoms, setDenoms] = useState<Record<string, Record<string, string>>>({});
  const [countOpen, setCountOpen] = useState<string | null>(null);
  const DEN: Record<string, number[]> = { USD: [100, 50, 20, 10, 5, 1], LBP: [100000, 50000, 20000, 10000, 5000, 1000], EUR: [200, 100, 50, 20, 10, 5, 2, 1], SAR: [500, 100, 50, 10, 5, 1], AED: [1000, 500, 100, 50, 20, 10, 5] };
  function setDen(methodId: string, cur: string, d: string, n: string) {
    const next = { ...(denoms[methodId] ?? {}), [d]: n.replace(/\D/g, '') };
    setDenoms({ ...denoms, [methodId]: next });
    const sum = Object.entries(next).reduce((s, [k, v]) => s + Number(k) * Number(v || 0), 0);
    setCounted((x) => ({ ...x, [methodId]: String(round(sum, decimalsOf(cur))) }));
  }
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
        method: 'POST', body: { notes, counted: d.drawer.map((m) => ({ method_id: m.method_id, amount: counted[m.method_id] ?? '0' })),
          denominations: Object.fromEntries(Object.entries(denoms).map(([m, x]) => [m, Object.fromEntries(Object.entries(x).filter(([, v]) => v !== '').map(([k, v]) => [k, Number(v)]))])) },
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
                {DEN[m.currency] && <Button size="xs" variant={countOpen === m.method_id ? 'filled' : 'light'} mb={6} onClick={() => setCountOpen(countOpen === m.method_id ? null : m.method_id)}>{t('rt.pos.countNotes')}</Button>}
              </Group>
            );
          })}
          {countOpen && (() => {
            const m = d.drawer.find((x) => x.method_id === countOpen);
            if (!m) return null;
            return (
              <Paper withBorder p="xs" className="no-print">
                <SimpleGrid cols={{ base: 2, sm: 3 }} spacing={6}>
                  {DEN[m.currency].map((v) => (
                    <TextInput key={v} size="xs" dir="ltr" inputMode="numeric" leftSection={<Text size="xs" w={60} ta="end" pr={4}>{v.toLocaleString()} ×</Text>} leftSectionWidth={70}
                      value={denoms[m.method_id]?.[String(v)] ?? ''} onChange={(e) => setDen(m.method_id, m.currency, String(v), e.currentTarget.value)} />
                  ))}
                </SimpleGrid>
              </Paper>
            );
          })()}
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
