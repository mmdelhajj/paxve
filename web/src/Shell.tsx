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
import Dashboard from './pages/Dashboard';
import Companies from './pages/Companies';
import Users from './pages/Users';
import Audit from './pages/Audit';
import Accounts from './pages/Accounts';
import Journal from './pages/Journal';
import Rates from './pages/Rates';
import TrialBalance from './pages/TrialBalance';
import Ledger from './pages/Ledger';
import Developers from './pages/Developers';
import Customers from './pages/billing/Customers';
import CustomerDetail from './pages/billing/CustomerDetail';
import Readings from './pages/billing/Readings';
import Invoices from './pages/billing/Invoices';
import Payments from './pages/billing/Payments';
import BillingSetup from './pages/billing/Setup';
import { IconCash, IconFileInvoice, IconGauge, IconSettings, IconUserCircle, IconReportMoney, IconChartBar, IconHourglass, IconMoneybag, IconBuildingBank, IconDeviceMobile } from '@tabler/icons-react';
import { ProfitLoss, BalanceSheet, Aging, Collections } from './pages/Reports';
import Cash from './pages/Cash';
import CollectorApp from './pages/collector/CollectorApp';

type Page = 'dashboard' | 'accounts' | 'journal' | 'rates' | 'trial-balance' | 'ledger' | 'companies' | 'users' |
  'audit' | 'developers' | 'customers' | 'customer' | 'readings' | 'invoices' | 'payments' | 'billing-setup' |
  'profit-loss' | 'balance-sheet' | 'aging' | 'collections' | 'cash' | 'collect';

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
      { key: 'cash', label: 'nav.cash', icon: IconBuildingBank },
      { key: 'collect', label: 'collector.mode', icon: IconDeviceMobile },
      { key: 'billing-setup', label: 'bill.setup', icon: IconSettings },
    ],
  },
  {
    label: 'nav.accounting',
    items: [
      { key: 'journal', label: 'nav.journal', icon: IconListDetails },
      { key: 'accounts', label: 'nav.accounts', icon: IconBook2 },
      { key: 'rates', label: 'nav.rates', icon: IconArrowsExchange },
    ],
  },
  {
    label: 'reports2.nav',
    items: [
      { key: 'profit-loss', label: 'reports2.pl', icon: IconReportMoney },
      { key: 'balance-sheet', label: 'reports2.bs', icon: IconChartBar },
      { key: 'trial-balance', label: 'nav.trialBalance', icon: IconScale },
      { key: 'aging', label: 'reports2.aging', icon: IconHourglass },
      { key: 'collections', label: 'reports2.collections', icon: IconMoneybag },
    ],
  },
  {
    label: 'nav.settings',
    items: [
      { key: 'companies', label: 'nav.companies', icon: IconBuilding },
      { key: 'users', label: 'nav.users', icon: IconUsers, roles: ['admin'] },
      { key: 'audit', label: 'nav.audit', icon: IconHistory, roles: ['admin', 'accountant'] },
      { key: 'developers', label: 'nav.developers', icon: IconApi, roles: ['admin'] },
    ],
  },
];

const allPages: Page[] = ['dashboard', 'accounts', 'journal', 'rates', 'trial-balance', 'ledger', 'companies', 'users',
  'audit', 'developers', 'customers', 'customer', 'readings', 'invoices', 'payments', 'billing-setup', 'profit-loss',
  'balance-sheet', 'aging', 'collections', 'cash', 'collect'];

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

  const allowed = (i: NavItem) => !i.roles || i.roles.includes(me.role);
  const blocked = sections.flatMap((s) => s.items).some((i) => i.key === page && !allowed(i));
  const current: Page = blocked ? 'dashboard' : page;

  if (me.role === 'collector' || current === 'collect') {
    return <CollectorApp me={me} onLogout={me.role === 'collector' ? onLogout : () => { window.location.hash = '#/dashboard'; }} />;
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
                    active={current === p.key || (current === 'ledger' && p.key === 'trial-balance') || (current === 'customer' && p.key === 'customers')}
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
