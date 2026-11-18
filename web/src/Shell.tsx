import { useEffect, useState } from 'react';
import {
  AppShell, Avatar, Burger, Button, Group, Menu, Modal, NavLink, PasswordInput, ScrollArea, Select, Stack, Text,
  UnstyledButton,
} from '@mantine/core';
import { useDisclosure } from '@mantine/hooks';
import {
  IconApi, IconArrowsExchange, IconBook2, IconBuilding, IconChevronDown, IconHistory, IconKey, IconLayoutDashboard,
  IconListDetails, IconLogout, IconScale, IconUsers,
} from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { api, type Me, type Role } from './api';
import { useCompany } from './company';
import { notifyError, notifySaved } from './util';
import LangSwitch from './components/LangSwitch';
import Logo from './components/Logo';
import { lazy, Suspense } from 'react';
import { Center, Loader } from '@mantine/core';
import {
  IconFileImport, IconCash, IconFileInvoice, IconGauge, IconSettings, IconUserCircle, IconReportMoney, IconChartBar,
  IconHourglass, IconMoneybag, IconBuildingBank, IconDeviceMobile, IconWorld, IconMessage, IconTruck, IconReceipt,
  IconCashBanknote, IconReceiptTax, IconPackage, IconGasStation, IconPackages, IconChartPie, IconUserDollar, IconBolt,
  IconTopologyStar3, IconReceipt2, IconEngine, IconRepeat, IconId, IconCoins, IconBuildingFactory2,
} from '@tabler/icons-react';
import TrialBanner from './components/TrialBanner';

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
const Ledger = lazy(() => import('./pages/Ledger'));
const Developers = lazy(() => import('./pages/Developers'));
const Import = lazy(() => import('./pages/Import'));
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
const Cash = lazy(() => import('./pages/Cash'));
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

const pageFallback = <Center p="xl"><Loader size="sm" /></Center>;

type Page = 'dashboard' | 'accounts' | 'journal' | 'rates' | 'trial-balance' | 'ledger' | 'companies' | 'users' |
  'audit' | 'developers' | 'customers' | 'customer' | 'readings' | 'invoices' | 'payments' | 'billing-setup' |
  'profit-loss' | 'balance-sheet' | 'aging' | 'collections' | 'cash' | 'collect' | 'platform' | 'import' | 'messages' |
  'suppliers' | 'supplier' | 'bills' | 'supplier-payments' | 'expenses' | 'items' | 'item' | 'stock' | 'ap-aging' | 'stock-valuation' | 'expenses-report' |
  'network' | 'tariffs' | 'vat-report' | 'generator-report' | 'print-bills' |
  'employees' | 'payroll' | 'assets' | 'asset-register' | 'recurring' | 'bank-rec';

interface NavItem { key: Page; label: string; icon: typeof IconBuilding; roles?: Role[] }

const sections: { label?: string; items: NavItem[] }[] = [
  { items: [{ key: 'dashboard', label: 'nav.dashboard', icon: IconLayoutDashboard }] },
  {
    label: 'bill.nav',
    items: [
      { key: 'customers', label: 'bill.customers', icon: IconUserCircle },
      { key: 'readings', label: 'bill.readings', icon: IconGauge },
      { key: 'invoices', label: 'bill.invoices', icon: IconFileInvoice },
      { key: 'payments', label: 'bill.payments', icon: IconCash },
      { key: 'network', label: 'net.title', icon: IconTopologyStar3 },
      { key: 'tariffs', label: 'net.tariffs', icon: IconBolt },
      { key: 'messages', label: 'msg.nav', icon: IconMessage },
      { key: 'cash', label: 'nav.cash', icon: IconBuildingBank },
      { key: 'collect', label: 'collector.mode', icon: IconDeviceMobile },
      { key: 'billing-setup', label: 'bill.setup', icon: IconSettings },
    ],
  },
  {
    label: 'pur.nav',
    items: [
      { key: 'suppliers', label: 'pur.suppliers', icon: IconTruck },
      { key: 'bills', label: 'pur.bills', icon: IconReceipt },
      { key: 'supplier-payments', label: 'pur.supplierPayments', icon: IconCashBanknote },
      { key: 'expenses', label: 'pur.expenses', icon: IconReceiptTax },
      { key: 'items', label: 'pur.items', icon: IconPackage },
      { key: 'stock', label: 'pur.stock', icon: IconGasStation },
    ],
  },
  {
    label: 'nav.accounting',
    items: [
      { key: 'journal', label: 'nav.journal', icon: IconListDetails },
      { key: 'accounts', label: 'nav.accounts', icon: IconBook2 },
      { key: 'rates', label: 'nav.rates', icon: IconArrowsExchange },
      { key: 'recurring', label: 'rec.nav', icon: IconRepeat },
      { key: 'bank-rec', label: 'bank.nav', icon: IconBuildingBank, roles: ['admin', 'accountant'] },
    ],
  },
  {
    label: 'hr.nav',
    items: [
      { key: 'employees', label: 'hr.employees', icon: IconId, roles: ['admin', 'accountant'] },
      { key: 'payroll', label: 'hr.payroll', icon: IconCoins, roles: ['admin', 'accountant'] },
    ],
  },
  {
    label: 'fa.nav',
    items: [{ key: 'assets', label: 'fa.title', icon: IconBuildingFactory2 }],
  },
  {
    label: 'reports2.nav',
    items: [
      { key: 'profit-loss', label: 'reports2.pl', icon: IconReportMoney },
      { key: 'balance-sheet', label: 'reports2.bs', icon: IconChartBar },
      { key: 'trial-balance', label: 'nav.trialBalance', icon: IconScale },
      { key: 'aging', label: 'reports2.aging', icon: IconHourglass },
      { key: 'collections', label: 'reports2.collections', icon: IconMoneybag },
      { key: 'ap-aging', label: 'pur.apAging', icon: IconUserDollar },
      { key: 'stock-valuation', label: 'pur.valuation', icon: IconPackages },
      { key: 'expenses-report', label: 'pur.expensesReport', icon: IconChartPie },
      { key: 'vat-report', label: 'net.vatReport', icon: IconReceipt2 },
      { key: 'generator-report', label: 'net.genReport', icon: IconEngine },
      { key: 'asset-register', label: 'fa.register', icon: IconBuildingFactory2 },
    ],
  },
  {
    label: 'nav.settings',
    items: [
      { key: 'companies', label: 'nav.companies', icon: IconBuilding },
      { key: 'users', label: 'nav.users', icon: IconUsers, roles: ['admin'] },
      { key: 'audit', label: 'nav.audit', icon: IconHistory, roles: ['admin', 'accountant'] },
      { key: 'import', label: 'nav.import', icon: IconFileImport, roles: ['admin', 'accountant'] },
      { key: 'developers', label: 'nav.developers', icon: IconApi, roles: ['admin'] },
      { key: 'platform', label: 'platform.title', icon: IconWorld },
    ],
  },
];

const allPages: Page[] = ['dashboard', 'accounts', 'journal', 'rates', 'trial-balance', 'ledger', 'companies', 'users',
  'audit', 'developers', 'customers', 'customer', 'readings', 'invoices', 'payments', 'billing-setup', 'profit-loss',
  'balance-sheet', 'aging', 'collections', 'cash', 'collect', 'platform', 'import', 'messages',
  'suppliers', 'supplier', 'bills', 'supplier-payments', 'expenses', 'items', 'item', 'stock', 'ap-aging', 'stock-valuation', 'expenses-report',
  'network', 'tariffs', 'vat-report', 'generator-report', 'print-bills',
  'employees', 'payroll', 'assets', 'asset-register', 'recurring', 'bank-rec'];

function readHash(): [Page, string] {
  const [p, param = ''] = window.location.hash.replace(/^#\/?/, '').split('/');
  return [allPages.includes(p as Page) ? (p as Page) : 'dashboard', param];
}

export default function Shell({ me, onMe, onLogout }: { me: Me; onMe: (m: Me) => void; onLogout: () => void }) {
  const { t } = useTranslation();
  const { companies, company, setCompanyId } = useCompany();
  const [opened, { toggle, close }] = useDisclosure();
  const [[page, param], setRoute] = useState<[Page, string]>(readHash);
  const [pwOpen, pw] = useDisclosure(false);

  useEffect(() => {
    const f = () => setRoute(readHash());
    window.addEventListener('hashchange', f);
    return () => window.removeEventListener('hashchange', f);
  }, []);

  const allowed = (i: NavItem) => (i.key === 'platform' ? me.is_platform_admin : !i.roles || i.roles.includes(me.role));
  const blocked = sections.flatMap((s) => s.items).some((i) => i.key === page && !allowed(i));
  const current: Page = blocked ? 'dashboard' : page;

  if (me.role === 'collector' || current === 'collect') {
    return <Suspense fallback={pageFallback}><CollectorApp me={me} onLogout={me.role === 'collector' ? onLogout : () => { window.location.hash = '#/dashboard'; }} /></Suspense>;
  }
  if (current === 'print-bills') {
    return <Suspense fallback={pageFallback}><BulkPrint initialPeriod={param} /></Suspense>;
  }

  async function logout() {
    try {
      await api('/auth/logout', { method: 'POST' });
    } finally {
      onLogout();
    }
  }

  function saveLocale(locale: string) {
    api<Me>('/me', { method: 'PATCH', body: { locale } }).then(onMe).catch(() => {});
  }

  return (
    <AppShell
      header={{ height: 60 }}
      navbar={{ width: 250, breakpoint: 'sm', collapsed: { mobile: !opened } }}
      padding="lg"
    >
      <AppShell.Header>
        <Group h="100%" px="md" justify="space-between" wrap="nowrap">
          <Group gap="sm" wrap="nowrap">
            <Burger opened={opened} onClick={toggle} hiddenFrom="sm" size="sm" />
            <Logo />
            {companies.length > 0 && (
              <Select
                size="xs"
                w={{ base: 140, sm: 220 }}
                aria-label={t('common.company')}
                leftSection={<IconBuilding size={14} />}
                value={company?.id ?? null}
                allowDeselect={false}
                data={companies.map((c) => ({ value: c.id, label: c.name_ar && document.dir === 'rtl' ? c.name_ar : c.name_en }))}
                onChange={(v) => v && setCompanyId(v)}
              />
            )}
          </Group>
          <Group gap="sm" wrap="nowrap">
            <LangSwitch onChange={saveLocale} />
            <Menu position="bottom-end" withArrow>
              <Menu.Target>
                <UnstyledButton>
                  <Group gap={6} wrap="nowrap">
                    <Avatar size="sm" color="teal" radius="xl">
                      {me.full_name.slice(0, 1).toUpperCase()}
                    </Avatar>
                    <Text size="sm" visibleFrom="md">
                      {me.full_name}
                    </Text>
                    <IconChevronDown size={14} />
                  </Group>
                </UnstyledButton>
              </Menu.Target>
              <Menu.Dropdown>
                <Menu.Label>
                  {me.tenant_name} · {t(`roles.${me.role}`)}
                </Menu.Label>
                <Menu.Label>{me.email}</Menu.Label>
                <Menu.Item leftSection={<IconKey size={16} />} onClick={pw.open}>
                  {t('common.changePassword')}
                </Menu.Item>
                <Menu.Item color="red" leftSection={<IconLogout size={16} />} onClick={logout}>
                  {t('common.logout')}
                </Menu.Item>
              </Menu.Dropdown>
            </Menu>
          </Group>
        </Group>
      </AppShell.Header>

      <AppShell.Navbar p="sm">
        <ScrollArea>
          {sections.map((s, i) => {
            const items = s.items.filter(allowed);
            if (items.length === 0) return null;
            return (
              <Stack key={i} gap={2} mb="md">
                {s.label && (
                  <Text size="xs" fw={600} c="dimmed" tt="uppercase" px="sm" mb={4}>
                    {t(s.label)}
                  </Text>
                )}
                {items.map((p) => (
                  <NavLink
                    key={p.key}
                    href={`#/${p.key}`}
                    label={t(p.label)}
                    leftSection={<p.icon size={18} stroke={1.6} />}
                    active={current === p.key || (current === 'ledger' && p.key === 'trial-balance') || (current === 'customer' && p.key === 'customers') || (current === 'supplier' && p.key === 'suppliers') || (current === 'item' && p.key === 'items')}
                    onClick={close}
                    style={{ borderRadius: 'var(--mantine-radius-md)' }}
                  />
                ))}
              </Stack>
            );
          })}
        </ScrollArea>
      </AppShell.Navbar>

      <AppShell.Main bg="var(--mantine-color-gray-0)">
        <TrialBanner me={me} />
        <Suspense fallback={pageFallback}>
        {current === 'dashboard' && <Dashboard me={me} />}
        {current === 'accounts' && <Accounts me={me} />}
        {current === 'journal' && <Journal me={me} openId={param} />}
        {current === 'rates' && <Rates me={me} />}
        {current === 'trial-balance' && <TrialBalance />}
        {current === 'ledger' && <Ledger accountId={param} />}
        {current === 'companies' && <Companies me={me} />}
        {current === 'users' && <Users me={me} />}
        {current === 'audit' && <Audit />}
        {current === 'developers' && <Developers />}
        {current === 'import' && <Import />}
        {current === 'messages' && <Messages me={me} />}
        {current === 'suppliers' && <Suppliers me={me} />}
        {current === 'supplier' && <SupplierDetail me={me} id={param} key={param} />}
        {current === 'bills' && <Bills me={me} />}
        {current === 'supplier-payments' && <SupplierPayments me={me} />}
        {current === 'expenses' && <Expenses me={me} />}
        {current === 'items' && <Items me={me} />}
        {current === 'item' && <ItemDetail me={me} id={param} key={param} />}
        {current === 'stock' && <Stock me={me} />}
        {current === 'ap-aging' && <APAging />}
        {current === 'stock-valuation' && <StockValuation />}
        {current === 'expenses-report' && <ExpensesReport />}
        {current === 'customers' && <Customers me={me} />}
        {current === 'customer' && <CustomerDetail me={me} id={param} key={param} />}
        {current === 'readings' && <Readings me={me} />}
        {current === 'invoices' && <Invoices me={me} />}
        {current === 'payments' && <Payments me={me} />}
        {current === 'billing-setup' && <BillingSetup me={me} />}
        {current === 'profit-loss' && <ProfitLoss />}
        {current === 'balance-sheet' && <BalanceSheet />}
        {current === 'aging' && <Aging />}
        {current === 'collections' && <Collections />}
        {current === 'cash' && <Cash me={me} />}
        {current === 'network' && <Network me={me} />}
        {current === 'tariffs' && <Tariffs me={me} />}
        {current === 'vat-report' && <VatReport />}
        {current === 'generator-report' && <GeneratorReport />}
        {current === 'employees' && <Employees me={me} />}
        {current === 'payroll' && <Payroll me={me} />}
        {current === 'assets' && <Assets me={me} />}
        {current === 'asset-register' && <AssetRegister />}
        {current === 'recurring' && <Recurring me={me} />}
        {current === 'bank-rec' && <BankRec me={me} />}
        {current === 'platform' && me.is_platform_admin && <Platform />}
        </Suspense>
      </AppShell.Main>

      <ChangePassword opened={pwOpen} onClose={pw.close} />
    </AppShell>
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
