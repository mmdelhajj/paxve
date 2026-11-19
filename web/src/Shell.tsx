import { lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { Alert, Button, Center, Group, Loader, Menu, Modal, PasswordInput, Stack } from '@mantine/core';
import { useDisclosure } from '@mantine/hooks';
import { IconChevronDown, IconKey, IconLogout, IconShieldLock } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { api, can, type Me, type Rate } from './api';
import { useCompany } from './company';
import { applyLang, languages } from './i18n';
import { formatAmount, notifyError, notifySaved, today } from './util';
import { ALL_APPS, appOfPage, RIBBON, tileOf, type Tile } from './ribbon';
import { openScreen, takeNewWindowRequest } from './nav';
import type { PaymentMethod } from './billing';
import TrialBanner from './components/TrialBanner';
import './magic.css';

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
const TrialBalance = lazy(() => import('./pages/TrialBalance'));
const Developers = lazy(() => import('./pages/Developers'));
const Import = lazy(() => import('./pages/Import'));
const MagicImport = lazy(() => import('./pages/MagicImport'));
const Customers = lazy(() => import('./pages/billing/Customers'));
const CustomerDetail = lazy(() => import('./pages/billing/CustomerDetail'));
const Readings = lazy(() => import('./pages/billing/Readings'));
const Invoices = lazy(() => import('./pages/billing/Invoices'));
const BulkPrint = lazy(() => import('./pages/billing/BulkPrint'));
const Payments = lazy(() => import('./pages/billing/Payments'));
const BillingSetup = lazy(() => import('./pages/billing/Setup'));
const Messages = lazy(() => import('./pages/billing/Messages'));
const ProfitLoss = lazy(() => named(import('./pages/Reports'), 'ProfitLoss'));
const BalanceSheet = lazy(() => named(import('./pages/Reports'), 'BalanceSheet'));
const Aging = lazy(() => named(import('./pages/Reports'), 'Aging'));
const Collections = lazy(() => named(import('./pages/Reports'), 'Collections'));
const Platform = lazy(() => import('./pages/Platform'));
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
const Employees = lazy(() => import('./pages/hr/Employees'));
const Payroll = lazy(() => import('./pages/hr/Payroll'));
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

const pageFallback = <Center p="xl"><Loader size="sm" /></Center>;

/** Screens that open one window per record (the param is part of the window identity). */
const PER_RECORD = new Set(['customer', 'supplier', 'item', 'statement', 'voucher']);
/** Screens that use the whole work area themselves (no padding, own scrolling). */
const FULL = new Set(['jibaya', 'subscribers', 'daily', 'ledger', 'cash']);
const KNOWN = new Set(['dashboard', 'accounts', 'journal', 'rates', 'trial-balance', 'ledger', 'companies', 'users', 'audit', 'developers',
  'customers', 'customer', 'readings', 'invoices', 'payments', 'billing-setup', 'profit-loss', 'balance-sheet', 'aging', 'collections',
  'cash', 'collect', 'platform', 'import', 'messages', 'suppliers', 'supplier', 'bills', 'supplier-payments', 'expenses', 'items', 'item',
  'stock', 'ap-aging', 'stock-valuation', 'stock-moves', 'expenses-report', 'network', 'tariffs', 'vat-report', 'generator-report',
  'print-bills', 'employees', 'payroll', 'assets', 'asset-register', 'recurring', 'bank-rec', 'crm', 'activities', 'quotes', 'orders',
  'statements', 'statement', 'sales-report', 'pos', 'pos-setup', 'pos-sessions', 'pos-report', 'magic-import', 'security', 'approvals',
  'consolidated', 'jibaya', 'voucher', 'subscribers', 'daily', 'apps']);

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
  const winsRef = useRef(wins);
  winsRef.current = wins;
  const activeRef = useRef(active);
  activeRef.current = active;

  const apps = useMemo(() => new Set(company?.apps ?? ALL_APPS), [company]);
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
    return tile.any ? true : can(me, tile.perm ?? 'reports.view');
  }, [apps, me]);

  const tabs = RIBBON.filter((tab) => (!tab.app || apps.has(tab.app)) && tab.tiles.some(allowed));
  // a screen the user may not use (by permission) shows a notice instead of failing calls; the server checks again
  const blocked = (p: string) => {
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

  // full-screen modes that replace the whole shell
  if (me.role === 'cashier' || page === 'pos') {
    return <Suspense fallback={pageFallback}><PosApp me={me} onLogout={me.role === 'cashier' ? onLogout : () => { window.location.hash = '#/dashboard'; }} /></Suspense>;
  }
  if (me.role === 'collector' || page === 'collect') {
    return <Suspense fallback={pageFallback}><CollectorApp me={me} onLogout={me.role === 'collector' ? onLogout : () => { window.location.hash = '#/dashboard'; }} /></Suspense>;
  }
  if (page === 'print-bills') {
    return <Suspense fallback={pageFallback}><BulkPrint initialPeriod={param} /></Suspense>;
  }

  async function logout() {
    try { await api('/auth/logout', { method: 'POST' }); } finally { onLogout(); }
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

      <nav className="mx-ribbon" role="tablist" aria-label={t('mx.menus')}>
        {tabs.map((tab) => (
          <button key={tab.id} className="mx-rtab" role="tab" aria-selected={visibleTab === tab.id}
            onClick={() => setOpenTab(openTab === tab.id && !pinned ? null : tab.id)}>{t(tab.label)}</button>
        ))}
        <button className="mx-pin" onClick={() => {
          const v = !pinned;
          setPinned(v);
          try { localStorage.setItem('paxve.ribbonPinned', v ? '1' : '0'); } catch { /* */ }
          if (!v) setOpenTab(null);
        }}>{pinned ? t('mx.unpin') : t('mx.pin')}</button>
      </nav>
      {shownTab && (
        <div className="mx-tiles" role="toolbar" aria-label={t(shownTab.label)}>
          {shownTab.tiles.filter(allowed).map((tile) => (
            <button key={tile.page + (tile.param ?? '') + tile.label} className="mx-tile"
              onClick={() => { if (!pinned) setOpenTab(null); openScreen(tile.page, tile.param ?? ''); }}>
              <span className="mx-ico" style={{ background: `linear-gradient(${shownTab.color}cc, ${shownTab.color})` }}><tile.icon size={22} stroke={1.7} /></span>
              <span>{t(tile.label)}</span>
            </button>
          ))}
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
            {w.id === active && !FULL.has(w.page) && <TrialBanner me={me} />}
            <Suspense fallback={pageFallback}>
              {blocked(w.page)
                ? <Alert color="red" maw={640}>{t('errors.forbidden')}</Alert>
                : <PageView me={me} onMe={onMe} page={w.page} param={w.param} active={w.id === active} apps={apps} />}
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
  if (app && !apps.has(app)) {
    return (
      <Alert color="yellow" maw={640} title={t('mx.appOffTitle')}>
        {t('mx.appOff', { app: t(`mx.apps.${app}.name`) })}{' '}
        {can(me, 'settings.manage') && <Button size="xs" variant="light" onClick={() => openScreen('apps')}>{t('mx.t.apps')}</Button>}
      </Alert>
    );
  }
  const views: Record<string, () => ReactNode> = {
    dashboard: () => <Dashboard me={me} />,
    accounts: () => <Accounts me={me} />,
    journal: () => <Journal me={me} openId={param} />,
    rates: () => <Rates me={me} />,
    'trial-balance': () => <TrialBalance />,
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
    customers: () => <Customers me={me} />,
    customer: () => <CustomerDetail me={me} id={param} key={param} />,
    readings: () => <Readings me={me} />,
    invoices: () => <Invoices me={me} />,
    payments: () => <Payments me={me} />,
    'billing-setup': () => <BillingSetup me={me} />,
    'profit-loss': () => <ProfitLoss />,
    'balance-sheet': () => <BalanceSheet />,
    aging: () => <Aging />,
    collections: () => <Collections />,
    cash: () => <Boxes me={me} />,
    network: () => <Network me={me} />,
    tariffs: () => <Tariffs me={me} />,
    'vat-report': () => <VatReport />,
    'generator-report': () => <GeneratorReport />,
    employees: () => <Employees me={me} />,
    payroll: () => <Payroll me={me} />,
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
    platform: () => (me.is_platform_admin ? <Platform /> : null),
    security: () => <MySecurity me={me} onMe={onMe} />,
    approvals: () => <Approvals me={me} />,
    consolidated: () => <Consolidated />,
    jibaya: () => <Jibaya me={me} mode={param} active={active} />,
    voucher: () => <Voucher me={me} kind={param} key={param} />,
    subscribers: () => <Subscribers me={me} />,
    daily: () => <DailyMovement />,
    apps: () => <Apps me={me} />,
  };
  return <>{(views[page] ?? views.dashboard)()}</>;
}

interface Box { id: string; name: string; currency: string; user_id: string | null; balance: string }

/** Bottom bar: user, own cash box, exchange rate, today's collections, key hints. */
function StatusBar({ me }: { me: Me }) {
  const { t } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [box, setBox] = useState<string>('');
  const [rate, setRate] = useState<string>('');
  const [todayCash, setToday] = useState<string>('');

  const load = useCallback(() => {
    if (!company) return;
    const cid = company.id;
    if (can(me, 'collections.view')) {
      api<PaymentMethod[]>(`/companies/${cid}/payment-methods`).then((ms) => {
        const mine = ms.filter((m) => m.is_active && m.user_id === me.id);
        setBox(mine.map((m) => m.name).join(' / '));
      }).catch(() => {});
      api<{ by_method: { currency: string; received: string; change_given: string }[] }>(
        `/companies/${cid}/payments?from=${today()}&to=${today()}&received_by=${me.id}&limit=1`).then((r) => {
        const sums = new Map<string, number>();
        for (const m of r.by_method ?? []) sums.set(m.currency, (sums.get(m.currency) ?? 0) + Number(m.received) - Number(m.change_given));
        setToday([...sums].map(([c, v]) => `${formatAmount(v, decimalsOf(c))} ${c}`).join(' + ') || '0');
      }).catch(() => {});
    }
    api<{ rates: Rate[] }>(`/companies/${cid}/rates/latest`).then((r) => {
      const local = company.local_currency !== company.base_currency ? r.rates.find((x) => x.currency === company.local_currency) : undefined;
      setRate(local ? `1 ${company.base_currency} = ${formatAmount(local.rate, 0)} ${local.currency}` : '');
    }).catch(() => {});
  }, [company, me, decimalsOf]);

  useEffect(() => {
    load();
    const id = setInterval(load, 60_000);
    window.addEventListener('paxve:paid', load);
    return () => { clearInterval(id); window.removeEventListener('paxve:paid', load); };
  }, [load]);

  return (
    <footer className="mx-status">
      <span>{t('mx.user')}: <b>{me.full_name}</b></span>
      {box && <span>{t('mx.box')}: <b>{box}</b></span>}
      {rate && <span>{t('mx.rate')}: <b dir="ltr">{rate}</b></span>}
      {todayCash && <span>{t('mx.today')}: <b dir="ltr">{todayCash}</b></span>}
      <span><kbd>F2</kbd> {t('mx.keyF2')} · <kbd>F9</kbd> {t('mx.keyF9')} · <kbd>Esc</kbd> {t('mx.keyEsc')}</span>
    </footer>
  );
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
