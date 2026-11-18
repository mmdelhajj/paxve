import { useEffect, useState } from 'react';
import {
  AppShell, Avatar, Burger, Button, Group, Menu, Modal, NavLink, PasswordInput, Stack, Text, UnstyledButton,
} from '@mantine/core';
import { useDisclosure } from '@mantine/hooks';
import {
  IconBuilding, IconChevronDown, IconHistory, IconKey, IconLayoutDashboard, IconLogout, IconUsers,
} from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { api, type Me, type Role } from './api';
import { notifyError, notifySaved } from './util';
import LangSwitch from './components/LangSwitch';
import Logo from './components/Logo';
import Dashboard from './pages/Dashboard';
import Companies from './pages/Companies';
import Users from './pages/Users';
import Audit from './pages/Audit';

type Page = 'dashboard' | 'companies' | 'users' | 'audit';

const pages: { key: Page; icon: typeof IconBuilding; roles?: Role[] }[] = [
  { key: 'dashboard', icon: IconLayoutDashboard },
  { key: 'companies', icon: IconBuilding },
  { key: 'users', icon: IconUsers, roles: ['admin'] },
  { key: 'audit', icon: IconHistory, roles: ['admin', 'accountant'] },
];

function readHash(): Page {
  const p = window.location.hash.replace(/^#\/?/, '') as Page;
  return pages.some((x) => x.key === p) ? p : 'dashboard';
}

export default function Shell({ me, onMe, onLogout }: { me: Me; onMe: (m: Me) => void; onLogout: () => void }) {
  const { t } = useTranslation();
  const [opened, { toggle, close }] = useDisclosure();
  const [page, setPage] = useState<Page>(readHash);
  const [pwOpen, pw] = useDisclosure(false);

  useEffect(() => {
    const f = () => setPage(readHash());
    window.addEventListener('hashchange', f);
    return () => window.removeEventListener('hashchange', f);
  }, []);

  const visible = pages.filter((p) => !p.roles || p.roles.includes(me.role));
  const current = visible.some((p) => p.key === page) ? page : 'dashboard';

  async function logout() {
    try {
      await api('/auth/logout', { method: 'POST' });
    } finally {
      onLogout();
    }
  }

  function saveLocale(locale: 'en' | 'ar') {
    api<Me>('/me', { method: 'PATCH', body: { locale } }).then(onMe).catch(() => {});
  }

  return (
    <AppShell
      header={{ height: 60 }}
      navbar={{ width: 240, breakpoint: 'sm', collapsed: { mobile: !opened } }}
      padding="lg"
    >
      <AppShell.Header>
        <Group h="100%" px="md" justify="space-between" wrap="nowrap">
          <Group gap="sm" wrap="nowrap">
            <Burger opened={opened} onClick={toggle} hiddenFrom="sm" size="sm" />
            <Logo />
            <Text c="dimmed" size="sm" visibleFrom="sm" truncate>
              {me.tenant_name}
            </Text>
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
                    <Text size="sm" visibleFrom="sm">
                      {me.full_name}
                    </Text>
                    <IconChevronDown size={14} />
                  </Group>
                </UnstyledButton>
              </Menu.Target>
              <Menu.Dropdown>
                <Menu.Label>
                  {me.email} · {t(`roles.${me.role}`)}
                </Menu.Label>
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
        {visible.map((p) => (
          <NavLink
            key={p.key}
            href={`#/${p.key}`}
            label={t(`nav.${p.key}`)}
            leftSection={<p.icon size={18} stroke={1.6} />}
            active={current === p.key}
            onClick={close}
            style={{ borderRadius: 'var(--mantine-radius-md)' }}
          />
        ))}
      </AppShell.Navbar>

      <AppShell.Main bg="var(--mantine-color-gray-0)">
        {current === 'dashboard' && <Dashboard me={me} />}
        {current === 'companies' && <Companies me={me} />}
        {current === 'users' && <Users me={me} />}
        {current === 'audit' && <Audit />}
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
