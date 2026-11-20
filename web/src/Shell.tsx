import { lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { Alert, Button, Center, Group, Loader, Menu, Modal, PasswordInput, Stack } from '@mantine/core';
import { useIdleLogout } from './pages/ent/idle';
import { useDisclosure } from '@mantine/hooks';
import { IconChevronDown, IconKey, IconLogout, IconShieldLock } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { api, can, planHas, type Me, type Rate } from './api';
import { useCompany } from './company';
import { applyLang, languages } from './i18n';
import { formatAmount, notifyError, notifySaved, today } from './util';
import { ALL_APPS, appOfPage, RIBBON, tileOf, type Tile } from './ribbon';
import { openScreen, takeNewWindowRequest } from './nav';
import type { PaymentMethod } from './billing';
import SubBanner from './components/SubBanner';
import StatusBar from './components/StatusBar';
import './magic.css';
import { PaneActive } from './paneActive';
import { clearDeviceCache } from './outbox';

// Pages are loaded on demand (code splitting) so the first screen downloads only what it needs.
const named = <T extends Record<string, unknown>>(p: Promise<T>, k: keyof T) =>
  p.then((m) => ({ default: m[k] as React.ComponentType<any> })); // eslint-disable-line @typescript-eslint/no-explicit-any
const Dashboard = lazy(() => import('./pages/Dashboard'));
const Companies = lazy(() => import('./pages/Companies'));
const Users = lazy(() => import('./pages/Users'));
const Audit = lazy(() => import('./pages/Audit'));
const Accounts = lazy(() => import('./pages/Accounts'));
const Journal = lazy(() => import('./pages/Journal'));
const Rates = lazy(() => import('./pages/Rates'));
const Developers = lazy(() => import('./pages/Developers'));
const Import = lazy(() => import('./pages/Import'));
const MagicImport = lazy(() => import('./pages/MagicImport'));
const CustomerDetail = lazy(() => import('./pages/billing/CustomerDetail'));
const Readings = lazy(() => import('./pages/billing/Readings'));
const Payments = lazy(() => import('./pages/billing/Payments'));
const BillingSetup = lazy(() => import('./pages/billing/Setup'));
const Messages = lazy(() => import('./pages/billing/Messages'));
const TrialBalanceGrid = lazy(() => named(import('./pages/acc/Statements'), 'TrialBalanceGrid'));
const ProfitLossGrid = lazy(() => named(import('./pages/acc/Statements'), 'ProfitLossGrid'));
const BalanceSheetGrid = lazy(() => named(import('./pages/acc/Statements'), 'BalanceSheetGrid'));
const AgingGrid = lazy(() => named(import('./pages/acc/Statements'), 'AgingGrid'));
const CollectionsGrid = lazy(() => named(import('./pages/acc/Statements'), 'CollectionsGrid'));
const Cheques = lazy(() => import('./pages/acc/Cheques'));
const Exchange = lazy(() => import('./pages/acc/Exchange'));
const FiscalYears = lazy(() => import('./pages/acc/FiscalYears'));
const CostCenters = lazy(() => import('./pages/acc/CostCenters'));
const Budgets = lazy(() => import('./pages/acc/Budgets'));
const OpeningBalances = lazy(() => import('./pages/acc/OpeningBalances'));
const CashFlow = lazy(() => named(import('./pages/acc/Reports3'), 'CashFlow'));
const Compare = lazy(() => named(import('./pages/acc/Reports3'), 'Compare'));
const MonthlyBills = lazy(() => import('./pages/magic/MonthlyBills'));
const SubsManage = lazy(() => import('./pages/magic/SubsManage'));
const PrintBills = lazy(() => import('./pages/magic/PrintBills'));
const PlatformConsole = lazy(() => import('./pages/platform/Console'));
const MySub = lazy(() => import('./pages/platform/MySub'));
const CollectorApp = lazy(() => import('./pages/collector/CollectorApp'));
const Suppliers = lazy(() => import('./pages/purchases/Suppliers'));
const SupplierDetail = lazy(() => named(import('./pages/purchases/Suppliers'), 'SupplierDetail'));
const Bills = lazy(() => import('./pages/purchases/Bills'));
const SupplierPayments = lazy(() => import('./pages/purchases/SupplierPayments'));
const Expenses = lazy(() => import('./pages/purchases/Expenses'));
const Items = lazy(() => import('./pages/purchases/Items'));
const ItemDetail = lazy(() => named(import('./pages/purchases/Items'), 'ItemDetail'));
const Stock = lazy(() => import('./pages/purchases/Stock'));
const APAging = lazy(() => named(import('./pages/purchases/PurchReports'), 'APAging'));
const StockValuation = lazy(() => named(import('./pages/purchases/PurchReports'), 'StockValuation'));
const StockMoves = lazy(() => named(import('./pages/purchases/PurchReports'), 'StockMoves'));
const ExpensesReport = lazy(() => named(import('./pages/purchases/PurchReports'), 'ExpensesReport'));
const Network = lazy(() => import('./pages/network/Network'));
const Tariffs = lazy(() => import('./pages/network/Tariffs'));
const VatReport = lazy(() => named(import('./pages/network/NetReports'), 'VatReport'));
const GeneratorReport = lazy(() => named(import('./pages/network/NetReports'), 'GeneratorReport'));
const HR = lazy(() => import('./pages/hr2/HR'));
const SelfService = lazy(() => import('./pages/hr2/SelfService'));
const Assets = lazy(() => import('./pages/assets/Assets'));
const AssetRegister = lazy(() => named(import('./pages/assets/Assets'), 'AssetRegister'));
const Recurring = lazy(() => import('./pages/accounting/Recurring'));
const BankRec = lazy(() => import('./pages/accounting/BankRec'));
const Quotes = lazy(() => import('./pages/sales/Sales'));
const SalesReport = lazy(() => named(import('./pages/sales/Sales'), 'SalesReport'));
const Statements = lazy(() => import('./pages/sales/Statements'));
const CustomerStatement = lazy(() => named(import('./pages/sales/Statements'), 'CustomerStatement'));
const Crm = lazy(() => import('./pages/crm/Crm'));
const ActivitiesPage = lazy(() => named(import('./pages/crm/Crm'), 'ActivitiesPage'));
const PosApp = lazy(() => import('./pages/pos/PosApp'));
const PosSetup = lazy(() => import('./pages/pos/PosAdmin'));
const PosSessions = lazy(() => named(import('./pages/pos/PosAdmin'), 'PosSessions'));
const PosReport = lazy(() => named(import('./pages/pos/PosAdmin'), 'PosReport'));
const MySecurity = lazy(() => import('./pages/Security'));
const Approvals = lazy(() => import('./pages/Approvals'));
const Consolidated = lazy(() => import('./pages/Consolidated'));
// Magic-style screens
const Jibaya = lazy(() => import('./pages/magic/Jibaya'));
const Voucher = lazy(() => import('./pages/magic/Voucher'));
const Subscribers = lazy(() => import('./pages/magic/Subscribers'));
const DailyMovement = lazy(() => import('./pages/magic/DailyMovement'));
const MagicLedger = lazy(() => import('./pages/magic/MagicLedger'));
const Boxes = lazy(() => import('./pages/magic/Boxes'));
const Apps = lazy(() => import('./pages/magic/Apps'));
const Translations = lazy(() => import('./pages/t2/Translations'));
const Localization = lazy(() => import('./pages/t2/Localization'));
const StockPro = lazy(() => import('./pages/t2/StockPro'));
const Service = lazy(() => import('./pages/svc/Service'));
const PurchaseOrders = lazy(() => import('./pages/oa/Pro'));
const Dunning = lazy(() => named(import('./pages/oa/Pro'), 'Dunning'));
const FxRevaluation = lazy(() => named(import('./pages/oa/Pro'), 'FxRevaluation'));
const Deferrals = lazy(() => named(import('./pages/oa/Pro'), 'Deferrals'));
const SubsChange = lazy(() => named(import('./pages/fin/Finish'), 'SubsChange'));
const SupplierPrices = lazy(() => named(import('./pages/fin/Finish'), 'SupplierPrices'));
const Claims = lazy(() => named(import('./pages/fin/Finish'), 'Claims'));
const CustomFields = lazy(() => named(import('./pages/fin/Finish'), 'CustomFields'));
const HRExtra = lazy(() => named(import('./pages/fin/Finish'), 'HRExtra'));
const OnlinePayments = lazy(() => named(import('./pages/fin/Finish'), 'OnlinePayments'));
const EInvoice = lazy(() => named(import('./pages/fin/Finish'), 'EInvoice'));
const Retail = lazy(() => import('./pages/retail/Retail'));
const Gaps = lazy(() => import('./pages/gaps/Gaps'));
const Wh = lazy(() => import('./pages/wh/Wh'));
const WhScan = lazy(() => import('./pages/wh/Scan'));
const WhDriver = lazy(() => import('./pages/wh/Driver'));
const Mfg = lazy(() => import('./pages/mfg/Mfg'));
const ShopFloor = lazy(() => import('./pages/mfg/ShopFloor'));
const Prj = lazy(() => import('./pages/prj/Prj'));
const Field = lazy(() => import('./pages/fsv/Field'));
const Tech = lazy(() => named(import('./pages/fsv/Field'), 'Tech'));
const Automation = lazy(() => import('./pages/auto/Automation'));
const Fleet = lazy(() => import('./pages/fleet/Fleet'));
const WebShop = lazy(() => import('./pages/web/WebShop'));
const Marketing = lazy(() => import('./pages/mkt/Marketing'));
const Talent = lazy(() => import('./pages/talent/Talent'));
const SelfAppraisal = lazy(() => named(import('./pages/talent/Talent'), 'SelfAppraisal'));
const ESign = lazy(() => import('./pages/esign/ESign'));
const Smart = lazy(() => import('./pages/smart/Smart'));
const Vert = lazy(() => import('./pages/vert/Vert'));
const Resto = lazy(() => import('./pages/vert/Resto'));
const Kds = lazy(() => named(import('./pages/vert/Resto'), 'Kds'));
const Van = lazy(() => import('./pages/vert/Van'));
const OwnerApp = lazy(() => import('./pages/owner/Owner'));
const Enterprise = lazy(() => import('./pages/ent/Enterprise'));
const MyDash = lazy(() => import('./pages/ent/MyDash'));

const pageFallback = <Center p="xl"><Loader size="sm" /></Center>;

/** Screens that open one window per record (the param is part of the window identity). */
const PER_RECORD = new Set(['customer', 'supplier', 'item', 'statement', 'voucher']);
/** Screens that use the whole work area themselves (no padding, own scrolling). */
const FULL = new Set(['jibaya', 'subscribers', 'daily', 'ledger', 'cash', 'invoices', 'customers', 'print-bills', 'trial-balance', 'profit-loss',
  'balance-sheet', 'aging', 'collections', 'cheques', 'exchange', 'fiscal-years', 'cost-centers', 'budgets', 'cash-flow', 'compare', 'opening-balances',
  'localization', 'translations', 'stock-pro', 'hr', 'service', 'po', 'dunning', 'fx-reval', 'deferrals', 'subs-change', 'supplier-prices', 'claims', 'custom-fields', 'mfg', 'hr-extra', 'online-pay', 'einvoice', 'platform', 'retail', 'gaps', 'wh',
  'projects', 'field', 'automation', 'fleet', 'webshop', 'marketing', 'talent', 'esign', 'smart', 'vert', 'ent', 'mydash']);
const KNOWN = new Set(['dashboard', 'accounts', 'journal', 'rates', 'trial-balance', 'ledger', 'companies', 'users', 'audit', 'developers',
  'customers', 'customer', 'readings', 'invoices', 'payments', 'billing-setup', 'profit-loss', 'balance-sheet', 'aging', 'collections',
  'cash', 'collect', 'platform', 'import', 'messages', 'suppliers', 'supplier', 'bills', 'supplier-payments', 'expenses', 'items', 'item',
  'stock', 'ap-aging', 'stock-valuation', 'stock-moves', 'expenses-report', 'network', 'tariffs', 'vat-report', 'generator-report',
  'print-bills', 'employees', 'payroll', 'assets', 'asset-register', 'recurring', 'bank-rec', 'crm', 'activities', 'quotes', 'orders',
  'statements', 'statement', 'sales-report', 'pos', 'pos-setup', 'pos-sessions', 'pos-report', 'magic-import', 'security', 'approvals',
  'consolidated', 'jibaya', 'voucher', 'subscribers', 'daily', 'apps', 'cheques', 'exchange', 'fiscal-years', 'cost-centers', 'budgets',
  'cash-flow', 'compare', 'opening-balances', 'localization', 'translations', 'stock-pro', 'hr', 'self', 'service', 'po', 'dunning', 'fx-reval', 'deferrals', 'subs-change', 'supplier-prices', 'claims', 'custom-fields', 'mfg', 'hr-extra', 'online-pay', 'einvoice', 'my-sub', 'retail', 'gaps', 'wh', 'scan', 'driver', 'shopfloor',
  'projects', 'field', 'tech', 'automation', 'fleet', 'webshop', 'marketing', 'talent', 'my-appraisal', 'esign', 'smart', 'vert', 'resto', 'kds', 'van', 'owner', 'ent', 'mydash']);

interface Win { id: number; page: string; param: string }

function readHash(): [string, string] {
  const [p, ...rest] = window.location.hash.replace(/^#\/?/, '').split('/');
  return [KNOWN.has(p) ? p : 'dashboard', rest.join('/')];
}
const winKey = (page: string, param: string) => (PER_RECORD.has(page) ? `${page}/${param}` : page);

let nextWin = 1;

function loadWins(): Win[] {
  try {
    const v = JSON.parse(sessionStorage.getItem('paxve.wins') || '[]') as Win[];
    if (Array.isArray(v)) return v.filter((w) => KNOWN.has(w.page)).map((w) => ({ ...w, id: nextWin++ }));
  } catch { /* storage unavailable */ }
  return [];
}

export default function Shell({ me, onMe, onLogout }: { me: Me; onMe: (m: Me) => void; onLogout: () => void }) {
  const { t, i18n } = useTranslation();
  const { companies, company, setCompanyId } = useCompany();
  useIdleLogout(me.idle_logout_min ?? 0, onLogout);
  const [[page, param], setRoute] = useState<[string, string]>(readHash);
  const [wins, setWins] = useState<Win[]>(() => {
    const [p, a] = readHash();
    const saved = loadWins();
    const k = winKey(p, a);
    const hit = saved.find((w) => winKey(w.page, w.param) === k);
    if (hit) { hit.param = a; return saved; }
    return [...saved, { id: nextWin++, page: p, param: a }].slice(-12);
  });
  const [active, setActive] = useState<number>(() => wins.find((w) => winKey(w.page, w.param) === winKey(...readHash()))?.id ?? wins[wins.length - 1]?.id ?? 0);
  const [openTab, setOpenTab] = useState<string | null>(null);
  const [pinned, setPinned] = useState<boolean>(() => { try { return localStorage.getItem('paxve.ribbonPinned') === '1'; } catch { return false; } });
  const [pwOpen, pw] = useDisclosure(false);
  // phone width: the menu tabs scroll sideways; arrows + a fade show there is more (audit P7-F1)
  const ribRef = useRef<HTMLElement>(null);
  const [ribMore, setRibMore] = useState({ start: false, end: false });
  const ribCheck = useCallback(() => {
    const el = ribRef.current;
    if (!el) return;
    const max = el.scrollWidth - el.clientWidth;
    const pos = Math.abs(el.scrollLeft); // RTL scrollLeft is negative
    const next = { start: max > 4 && pos > 4, end: max > 4 && pos < max - 4 };
    setRibMore((o) => (o.start === next.start && o.end === next.end ? o : next));
  }, []);
  const ribScroll = (dir: number) => {
    const el = ribRef.current;
    if (!el) return;
    const rtl = getComputedStyle(el).direction === 'rtl';
    el.scrollBy({ left: dir * (rtl ? -1 : 1) * Math.max(120, el.clientWidth * 0.6), behavior: 'smooth' });
  };
  useEffect(() => {
    ribCheck();
    window.addEventListener('resize', ribCheck);
    return () => window.removeEventListener('resize', ribCheck);
  });
  const winsRef = useRef(wins);
  winsRef.current = wins;
  const activeRef = useRef(active);
  activeRef.current = active;

  // apps the company switched on, minus those its Paxve plan does not include (their screens show an upgrade hint)
  const apps = useMemo(() => new Set((company?.apps ?? ALL_APPS).filter((a) => planHas(me, a))), [company, me]);
  const biz = company?.business_type ?? 'generator';

  // hash → window: a new window when asked for, the existing window of that screen, or the current window
  useEffect(() => {
    const f = () => {
      const [p, a] = readHash();
      const wantNewWindow = takeNewWindowRequest();
      setRoute([p, a]);
      const ws = winsRef.current;
      const k = winKey(p, a);
      const hit = ws.find((w) => winKey(w.page, w.param) === k);
      if (hit) {
        if (hit.param !== a) setWins(ws.map((w) => (w.id === hit.id ? { ...w, param: a } : w)));
        setActive(hit.id);
      } else if (wantNewWindow || !ws.some((w) => w.id === activeRef.current)) {
        const w = { id: nextWin++, page: p, param: a };
        let next = [...ws, w];
        if (next.length > 12) next = next.filter((x) => x.id !== next.find((y) => y.id !== activeRef.current)!.id);
        setWins(next);
        setActive(w.id);
      } else {
        setWins(ws.map((w) => (w.id === activeRef.current ? { ...w, page: p, param: a } : w)));
      }
    };
    window.addEventListener('hashchange', f);
    return () => window.removeEventListener('hashchange', f);
  }, []);

  useEffect(() => {
    try { sessionStorage.setItem('paxve.wins', JSON.stringify(wins.map(({ page: p, param: a }) => ({ page: p, param: a })))); } catch { /* */ }
  }, [wins]);

  // global keys: F2 = الجباية (search), Ctrl+F4 closes the current window
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'F2' && can(me, 'collections.create')) {
        e.preventDefault();
        openScreen('jibaya');
        setTimeout(() => window.dispatchEvent(new Event('paxve:jibaya-search')), 30);
      } else if (e.key === 'F4' && e.ctrlKey) {
        e.preventDefault();
        closeWin(activeRef.current);
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [me]);

  const allowed = useCallback((tile: Tile) => {
    if (tile.platform) return me.is_platform_admin;
    if (tile.app && !apps.has(tile.app)) return false;
    if (tile.biz && !tile.biz.includes(biz)) return false;
    return tile.any ? true : can(me, tile.perm ?? 'reports.view');
  }, [apps, me, biz]);

  // a tab shows when its app is on, it fits the business type (a pharmacy has no meter lists) and has a usable tile
  const tabs = RIBBON.filter((tab) => (!tab.app || apps.has(tab.app)) && (!tab.biz || tab.biz.includes(biz)) && tab.tiles.some(allowed));
  // a screen the user may not use (by permission) shows a notice instead of failing calls; the server checks again
  const blocked = (p: string) => {
    if (p === 'dashboard' && (me.permissions ?? []).length === 0) return false; // employees: their own page
    const ts = RIBBON.flatMap((tab) => tab.tiles).filter((x) => x.page === p);
    return ts.length > 0 && !ts.some((x) => (x.platform ? me.is_platform_admin : x.any || can(me, x.perm ?? 'reports.view')));
  };

  function closeWin(id: number) {
    const ws = winsRef.current;
    const i = ws.findIndex((w) => w.id === id);
    if (i < 0) return;
    const next = ws.filter((w) => w.id !== id);
    setWins(next);
    if (id === activeRef.current) {
      const n = next[Math.min(i, next.length - 1)];
      if (n) { setActive(n.id); window.location.hash = `#/${n.page}${n.param ? '/' + n.param : ''}`; }
      else { setActive(0); window.location.hash = '#/dashboard'; }
    }
  }
  function focusWin(w: Win) {
    setActive(w.id);
    const h = `#/${w.page}${w.param ? '/' + w.param : ''}`;
    if (window.location.hash !== h) window.location.hash = h;
  }

  // full-screen modes: an office user always has a way back to the system (audit P7-F10); collectors and cashiers live there
  const office = me.role !== 'collector' && me.role !== 'cashier';
  const back = office ? <button className="mx-backsys no-print" onClick={() => { window.location.hash = '#/dashboard'; }}>{t('p7.back')}</button> : null;
  if (page === 'shopfloor') {
    return <Suspense fallback={pageFallback}>{back}<ShopFloor me={me} onExit={() => { window.location.hash = '#/mfg/orders'; }} /></Suspense>;
  }
  if (page === 'tech') {
    return <Suspense fallback={pageFallback}>{back}<Tech me={me} onExit={() => { window.location.hash = me.role === 'collector' ? '#/collect' : '#/field/jobs'; }} /></Suspense>;
  }
  if (page === 'scan' || page === 'driver') {
    const exit = () => { window.location.hash = me.role === 'collector' ? '#/collect' : '#/wh/ops'; };
    return <Suspense fallback={pageFallback}>{back}{page === 'scan' ? <WhScan me={me} onExit={exit} /> : <WhDriver me={me} onExit={exit} />}</Suspense>;
  }
  if (page === 'owner' && me.role !== 'collector' && me.role !== 'cashier') {
    return <Suspense fallback={pageFallback}>{back}<OwnerApp me={me} onLogout={onLogout} onExit={() => { window.location.hash = '#/dashboard'; }} /></Suspense>;
  }
  if (page === 'resto' || page === 'kds' || page === 'van') {
    const exit = () => { window.location.hash = me.role === 'cashier' ? '#/pos' : me.role === 'collector' ? '#/collect' : page === 'van' ? '#/vert/wholesale' : '#/vert/restaurant'; };
    return <Suspense fallback={pageFallback}>{back}{page === 'resto' ? <Resto me={me} onExit={exit} /> : page === 'kds' ? <Kds me={me} onExit={exit} /> : <Van me={me} onExit={exit} />}</Suspense>;
  }
  if (me.role === 'cashier' || page === 'pos') {
    return <Suspense fallback={pageFallback}>{back}<PosApp me={me} onLogout={me.role === 'cashier' ? onLogout : () => { window.location.hash = '#/dashboard'; }} /></Suspense>;
  }
  if (me.role === 'collector' || page === 'collect') {
    return <Suspense fallback={pageFallback}>{back}<CollectorApp me={me} onLogout={me.role === 'collector' ? onLogout : () => { window.location.hash = '#/dashboard'; }} /></Suspense>;
  }

  async function logout() {
    try { await api('/auth/logout', { method: 'POST' }); } finally { clearDeviceCache(); onLogout(); }
  }
  function saveLocale(locale: string) {
    api<Me>('/me', { method: 'PATCH', body: { locale } }).then(onMe).catch(() => {});
  }

  const tabCaption = (w: Win) => {
    const tile = tileOf(w.page, w.param);
    return tile ? t(tile.label) : t(`mx.page.${w.page}`, { defaultValue: w.page });
  };
  const visibleTab = openTab ?? (pinned ? tabs[0]?.id ?? null : null);
  const shownTab = tabs.find((x) => x.id === visibleTab);
  const year = new Date().getFullYear();
  const companyName = company ? (i18n.language === 'ar' && company.name_ar ? company.name_ar : company.name_en) : '';

  return (
    <div className={`mx-app mx-biz-${biz}`}>
      <div className="mx-title">
        <span className="mx-brand">Paxve</span>
        <span>{companyName} — {year} — {me.full_name}</span>
        <span className="mx-sp" />
        {companies.length > 1 && (
          <select className="mx-tsel" aria-label={t('common.company')} value={company?.id ?? ''} onChange={(e) => setCompanyId(e.currentTarget.value)}>
            {companies.map((c) => <option key={c.id} value={c.id}>{i18n.language === 'ar' && c.name_ar ? c.name_ar : c.name_en}</option>)}
          </select>
        )}
        <select className="mx-tsel" aria-label={t('common.language')} value={i18n.language} onChange={(e) => { applyLang(e.currentTarget.value); saveLocale(e.currentTarget.value); }}>
          {languages.map((l) => <option key={l.code} value={l.code}>{l.label}</option>)}
        </select>
        <Menu position="bottom-end" withArrow>
          <Menu.Target>
            <button className="mx-tbtn" aria-label={me.full_name}>{me.full_name.split(' ')[0]} <IconChevronDown size={12} /></button>
          </Menu.Target>
          <Menu.Dropdown>
            <Menu.Label>{me.tenant_name} · {me.role_name || t(`roles.${me.role}`)}</Menu.Label>
            <Menu.Label>{me.email}</Menu.Label>
            <Menu.Item leftSection={<IconShieldLock size={16} />} onClick={() => openScreen('security')}>{t('sec.mySecurity')}</Menu.Item>
            <Menu.Item leftSection={<IconKey size={16} />} onClick={pw.open}>{t('common.changePassword')}</Menu.Item>
            <Menu.Item color="red" leftSection={<IconLogout size={16} />} onClick={logout}>{t('common.logout')}</Menu.Item>
          </Menu.Dropdown>
        </Menu>
      </div>

      <div className={`mx-ribwrap ${ribMore.start ? 'more-start' : ''} ${ribMore.end ? 'more-end' : ''}`}>
      {ribMore.start && <button className="mx-ribarrow start" aria-label={t('p7.ribPrev')} onClick={() => ribScroll(-1)}>{i18n.dir() === 'rtl' ? '›' : '‹'}</button>}
      <nav ref={ribRef} onScroll={ribCheck} className="mx-ribbon" role="tablist" aria-label={t('mx.menus')}>
        {tabs.map((tab) => (
          <button key={tab.id} className="mx-rtab" role="tab" aria-selected={visibleTab === tab.id} style={{ '--c': tab.color } as React.CSSProperties}
            onClick={() => setOpenTab(openTab === tab.id && !pinned ? null : tab.id)}>{t(tab.label)}</button>
        ))}
        <button className="mx-pin" onClick={() => {
          const v = !pinned;
          setPinned(v);
          try { localStorage.setItem('paxve.ribbonPinned', v ? '1' : '0'); } catch { /* */ }
          if (!v) setOpenTab(null);
        }}>{pinned ? t('mx.unpin') : t('mx.pin')}</button>
      </nav>
      {ribMore.end && <button className="mx-ribarrow end" aria-label={t('p7.ribNext')} onClick={() => ribScroll(1)}>{i18n.dir() === 'rtl' ? '‹' : '›'}</button>}
      </div>
      {shownTab && (
        <div className="mx-tiles" role="toolbar" aria-label={t(shownTab.label)}>
          {shownTab.tiles.filter(allowed).map((tile) => (
            <button key={tile.page + (tile.param ?? '') + tile.label} className="mx-tile"
              onClick={() => { if (!pinned) setOpenTab(null); openScreen(tile.page, tile.param ?? ''); }}>
              <span className="mx-ico" style={{ '--c': shownTab.color } as React.CSSProperties}><tile.icon size={21} stroke={1.6} /></span>
              <span>{t(tile.label)}</span>
            </button>
          ))}
        </div>
      )}

      <SubBanner me={me} />
      {me.is_platform_admin && !me.mfa_enabled && me.platform_mfa_deadline && (
        <div className="mx-flash bad" style={{ margin: 0, borderRadius: 0 }}>
          {t('sec.platformMfa', { date: new Date(me.platform_mfa_deadline).toLocaleDateString() })}{' '}
          <button className="mx-btn sm" onClick={() => openScreen('security')}>{t('sec.turnOn')}</button>
        </div>
      )}

      <div className="mx-wins" role="tablist" aria-label={t('mx.openScreens')}>
        {wins.map((w) => (
          <div key={w.id} className="mx-win" role="tab" tabIndex={0} aria-selected={w.id === active}
            onClick={() => focusWin(w)} onKeyDown={(e) => { if (e.key === 'Enter') focusWin(w); }}
            onAuxClick={(e) => { if (e.button === 1) closeWin(w.id); }}>
            <span>{tabCaption(w)}</span>
            <button className="mx-x" aria-label={t('common.close')} onClick={(e) => { e.stopPropagation(); closeWin(w.id); }}>✕</button>
          </div>
        ))}
      </div>

      <main className="mx-work" onClick={() => { if (openTab && !pinned) setOpenTab(null); }}>
        {wins.map((w) => (
          <section key={w.id} className={`mx-pane ${FULL.has(w.page) ? 'mx-full' : ''}`} hidden={w.id !== active} aria-label={tabCaption(w)}>
            <Suspense fallback={pageFallback}>
              {blocked(w.page)
                ? <Alert color="red" maw={640}>{t('errors.forbidden')}</Alert>
                : <PaneActive.Provider value={w.id === active}><PageView me={me} onMe={onMe} page={w.page} param={w.param} active={w.id === active} apps={apps} /></PaneActive.Provider>}
            </Suspense>
          </section>
        ))}
        {wins.length === 0 && <Center h="100%" c="dimmed">{t('mx.noScreens')}</Center>}
      </main>

      <StatusBar me={me} />
      <ChangePassword opened={pwOpen} onClose={pw.close} />
    </div>
  );
}

function PageView({ me, onMe, page, param, active, apps }: { me: Me; onMe: (m: Me) => void; page: string; param: string; active: boolean; apps: Set<string> }) {
  const { t } = useTranslation();
  const app = appOfPage(page);
  if (app && !planHas(me, app)) {
    return (
      <Alert color="yellow" maw={640} title={t('mx.notInPlanTitle')}>
        {t('mx.notInPlan', { app: t(`mx.apps.${app}.name`) })}{' '}
        <Button size="xs" variant="light" onClick={() => openScreen('my-sub')}>{t('mx.upgrade')}</Button>
      </Alert>
    );
  }
  if (app && !apps.has(app)) {
    return (
      <Alert color="yellow" maw={640} title={t('mx.appOffTitle')}>
        {t('mx.appOff', { app: t(`mx.apps.${app}.name`) })}{' '}
        {can(me, 'settings.manage') && <Button size="xs" variant="light" onClick={() => openScreen('apps')}>{t('mx.t.apps')}</Button>}
      </Alert>
    );
  }
  const views: Record<string, () => ReactNode> = {
    // a user with no office permissions (an employee) lands on their self-service page
    dashboard: () => ((me.permissions ?? []).length === 0 ? <SelfService /> : <Dashboard me={me} />),
    accounts: () => <Accounts me={me} />,
    journal: () => <Journal me={me} openId={param} />,
    rates: () => <Rates me={me} />,
    'trial-balance': () => <TrialBalanceGrid />,
    ledger: () => <MagicLedger accountId={param} />,
    companies: () => <Companies me={me} />,
    users: () => <Users me={me} />,
    audit: () => <Audit />,
    developers: () => <Developers />,
    import: () => <Import />,
    'magic-import': () => <MagicImport />,
    messages: () => <Messages me={me} />,
    suppliers: () => <Suppliers me={me} />,
    supplier: () => <SupplierDetail me={me} id={param} key={param} />,
    bills: () => <Bills me={me} />,
    'supplier-payments': () => <SupplierPayments me={me} />,
    expenses: () => <Expenses me={me} />,
    items: () => <Items me={me} />,
    item: () => <ItemDetail me={me} id={param} key={param} />,
    stock: () => <Stock me={me} />,
    'ap-aging': () => <APAging />,
    'stock-valuation': () => <StockValuation />,
    'stock-moves': () => <StockMoves />,
    'expenses-report': () => <ExpensesReport />,
    customers: () => <SubsManage me={me} />,
    customer: () => <CustomerDetail me={me} id={param} key={param} />,
    readings: () => <Readings me={me} />,
    invoices: () => <MonthlyBills me={me} />,
    payments: () => <Payments me={me} />,
    'billing-setup': () => <BillingSetup me={me} />,
    'profit-loss': () => <ProfitLossGrid />,
    'balance-sheet': () => <BalanceSheetGrid />,
    aging: () => <AgingGrid />,
    collections: () => <CollectionsGrid />,
    'print-bills': () => <PrintBills initialPeriod={param} />,
    cheques: () => <Cheques me={me} openId={param} />,
    exchange: () => <Exchange me={me} />,
    'fiscal-years': () => <FiscalYears me={me} />,
    'cost-centers': () => <CostCenters me={me} initialMode={param} />,
    budgets: () => <Budgets me={me} />,
    'cash-flow': () => <CashFlow />,
    compare: () => <Compare initial={param} />,
    'opening-balances': () => <OpeningBalances me={me} />,
    cash: () => <Boxes me={me} />,
    network: () => <Network me={me} />,
    tariffs: () => <Tariffs me={me} />,
    'vat-report': () => <VatReport />,
    'generator-report': () => <GeneratorReport />,
    employees: () => <HR me={me} initial="people" />,
    payroll: () => <HR me={me} initial="payroll" />,
    hr: () => <HR me={me} initial={param} key={param} />,
    self: () => <SelfService />,
    assets: () => <Assets me={me} />,
    'asset-register': () => <AssetRegister />,
    recurring: () => <Recurring me={me} />,
    'bank-rec': () => <BankRec me={me} />,
    crm: () => <Crm me={me} />,
    activities: () => <ActivitiesPage me={me} />,
    quotes: () => <Quotes me={me} kind="quotes" key="quotes" />,
    orders: () => <Quotes me={me} kind="orders" key="orders" />,
    statements: () => <Statements me={me} />,
    statement: () => <CustomerStatement id={param} key={param} />,
    'sales-report': () => <SalesReport />,
    'pos-setup': () => <PosSetup me={me} />,
    'pos-sessions': () => <PosSessions />,
    'pos-report': () => <PosReport />,
    platform: () => (me.is_platform_admin ? <PlatformConsole initial={param} key={param} /> : null),
    'my-sub': () => <MySub me={me} />,
    security: () => <MySecurity me={me} onMe={onMe} />,
    approvals: () => <Approvals me={me} />,
    consolidated: () => <Consolidated />,
    jibaya: () => <Jibaya me={me} mode={param} active={active} />,
    voucher: () => <Voucher me={me} kind={param} key={param} />,
    subscribers: () => <Subscribers me={me} />,
    daily: () => <DailyMovement />,
    apps: () => <Apps me={me} />,
    translations: () => <Translations me={me} />,
    localization: () => <Localization me={me} />,
    'stock-pro': () => <StockPro me={me} initial={param} key={param} />,
    service: () => <Service me={me} initial={param} key={param} />,
    po: () => <PurchaseOrders me={me} />,
    dunning: () => <Dunning me={me} />,
    'fx-reval': () => <FxRevaluation me={me} />,
    deferrals: () => <Deferrals me={me} />,
    'subs-change': () => <SubsChange me={me} />,
    'supplier-prices': () => <SupplierPrices me={me} />,
    claims: () => <Claims me={me} />,
    'custom-fields': () => <CustomFields me={me} />,
    mfg: () => <Mfg me={me} initial={param} key={param} />,
    retail: () => <Retail me={me} initial={param} key={param} />,
    gaps: () => <Gaps me={me} initial={param} key={param} />,
    wh: () => <Wh me={me} initial={param} key={param} />,
    projects: () => <Prj me={me} initial={param} key={param} />,
    field: () => <Field me={me} initial={param} key={param} />,
    automation: () => <Automation me={me} initial={param} key={param} />,
    fleet: () => <Fleet me={me} initial={param} key={param} />,
    ent: () => <Enterprise me={me} initial={param} key={param} />,
    mydash: () => <MyDash me={me} />,
    webshop: () => <WebShop me={me} initial={param} key={param} />,
    marketing: () => <Marketing me={me} initial={param} key={param} />,
    talent: () => <Talent me={me} initial={param} key={param} />,
    'my-appraisal': () => <SelfAppraisal />,
    esign: () => <ESign me={me} />,
    smart: () => <Smart me={me} initial={param} key={param} />,
    vert: () => <Vert me={me} initial={param} key={param} />,
    'hr-extra': () => <HRExtra me={me} initial={param} key={param} />,
    'online-pay': () => <OnlinePayments me={me} />,
    einvoice: () => <EInvoice me={me} />,
  };
  return <>{(views[page] ?? views.dashboard)()}</>;
}


function ChangePassword({ opened, onClose }: { opened: boolean; onClose: () => void }) {
  const { t } = useTranslation();
  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      await api('/me', { method: 'PATCH', body: { current_password: current, new_password: next } });
      notifySaved(t);
      setCurrent('');
      setNext('');
      onClose();
    } catch (err) {
      notifyError(t, err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened={opened} onClose={onClose} title={t('common.changePassword')}>
      <form onSubmit={submit}>
        <Stack>
          <PasswordInput label={t('password.current')} dir="ltr" required value={current}
            onChange={(e) => setCurrent(e.currentTarget.value)} autoComplete="current-password" />
          <PasswordInput label={t('password.new')} dir="ltr" required minLength={10} value={next}
            onChange={(e) => setNext(e.currentTarget.value)} autoComplete="new-password" />
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
            <Button type="submit" loading={busy}>{t('common.save')}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}
