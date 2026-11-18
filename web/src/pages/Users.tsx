import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  Alert, Badge, Button, Center, Checkbox, Code, CopyButton, Group, Loader, Menu, Modal, MultiSelect, NumberInput, Paper,
  PasswordInput, Radio, ScrollArea, Select, Stack, Switch, Table, Tabs, Text, Textarea, TextInput, Title,
} from '@mantine/core';
import { useDisclosure } from '@mantine/hooks';
import { IconCopy, IconDots, IconPlus, IconShieldCheck } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { api, can, type CustomRole, type Me, type Role, type User } from '../api';
import { useCompany } from '../company';
import { formatDate, formatDateTime, notifyError, notifySaved } from '../util';
import { languages } from '../i18n';
import { SessionsTable } from './Security';

const roles: Role[] = ['admin', 'accountant', 'data_entry', 'read_only', 'collector', 'cashier'];

interface Catalogue {
  modules: { module: string; actions: string[] }[];
  presets: Record<string, string[]>;
}

function useCatalogue() {
  const [cat, setCat] = useState<Catalogue | null>(null);
  useEffect(() => { api<Catalogue>('/permissions').then(setCat).catch(() => {}); }, []);
  return cat;
}

/** "builtin:accountant" or "custom:<id>" — one select for built-in and custom roles. */
const roleKey = (u: { role: Role; role_id: string | null }) => (u.role_id ? `custom:${u.role_id}` : `builtin:${u.role}`);
function roleBody(key: string): { role?: Role; role_id?: string } {
  const [kind, v] = key.split(':');
  return kind === 'custom' ? { role_id: v } : { role: v as Role, role_id: '' };
}

export default function Users({ me }: { me: Me }) {
  const { t } = useTranslation();
  const [tab, setTab] = useState<string | null>('users');
  return (
    <Stack gap="lg" maw={1150}>
      <Title order={2}>{t('users.title')}</Title>
      <Tabs value={tab} onChange={setTab} keepMounted={false}>
        <Tabs.List>
          <Tabs.Tab value="users">{t('nav.users')}</Tabs.Tab>
          <Tabs.Tab value="roles">{t('sec.roles')}</Tabs.Tab>
          <Tabs.Tab value="security">{t('sec.orgSettings')}</Tabs.Tab>
        </Tabs.List>
        <Tabs.Panel value="users" pt="md"><UsersTab me={me} /></Tabs.Panel>
        <Tabs.Panel value="roles" pt="md"><RolesTab me={me} /></Tabs.Panel>
        <Tabs.Panel value="security" pt="md"><OrgSecurity me={me} /></Tabs.Panel>
      </Tabs>
    </Stack>
  );
}

function UsersTab({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const { companies } = useCompany();
  const [rows, setRows] = useState<User[] | null>(null);
  const [customRoles, setCustomRoles] = useState<CustomRole[]>([]);
  const [opened, modal] = useDisclosure(false);
  const [access, setAccess] = useState<User | null>(null);
  const [link, setLink] = useState<{ url: string; expires_at: string } | null>(null);
  const [sessionsOf, setSessionsOf] = useState<User | null>(null);
  const canEdit = can(me, 'users.edit');

  const roleOptions = useMemo(() => [
    { group: t('sec.builtIn'), items: roles.map((r) => ({ value: `builtin:${r}`, label: t(`roles.${r}`) })) },
    ...(customRoles.length ? [{ group: t('sec.customRole'), items: customRoles.filter((r) => r.is_active).map((r) => ({ value: `custom:${r.id}`, label: r.name })) }] : []),
  ], [customRoles, t]);

  const load = useCallback(() => {
    api<User[]>('/users').then(setRows).catch((e) => notifyError(t, e));
    api<CustomRole[]>('/roles').then(setCustomRoles).catch(() => setCustomRoles([]));
  }, [t]);
  useEffect(load, [load]);

  async function patch(u: User, body: Record<string, unknown>) {
    try {
      const updated = await api<User>(`/users/${u.id}`, { method: 'PATCH', body });
      setRows((r) => r?.map((x) => (x.id === u.id ? updated : x)) ?? null);
      notifySaved(t);
      return true;
    } catch (e) {
      notifyError(t, e);
      return false;
    }
  }
  async function resetLink(u: User) {
    try {
      setLink(await api<{ url: string; expires_at: string }>(`/users/${u.id}/reset-link`, { method: 'POST', body: {} }));
    } catch (e) {
      notifyError(t, e);
    }
  }
  async function signOut(u: User) {
    try {
      await api(`/users/${u.id}/sessions/revoke`, { method: 'POST', body: {} });
      notifySaved(t);
    } catch (e) {
      notifyError(t, e);
    }
  }
  async function resetMfa(u: User) {
    try {
      await api(`/users/${u.id}/mfa/reset`, { method: 'POST', body: {} });
      notifySaved(t);
      load();
    } catch (e) {
      notifyError(t, e);
    }
  }
  const companyName = (id: string) => companies.find((c) => c.id === id)?.name_en ?? id.slice(0, 8);

  return (
    <Stack>
      {can(me, 'users.create') && (
        <Group justify="flex-end">
          <Button leftSection={<IconPlus size={16} />} onClick={modal.open}>{t('users.new')}</Button>
        </Group>
      )}
      <Paper withBorder>
        {rows === null ? (
          <Group justify="center" p="xl"><Loader size="sm" /></Group>
        ) : (
          <Table.ScrollContainer minWidth={900}>
            <Table verticalSpacing="sm" highlightOnHover key={i18n.language}>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>{t('users.fullName')}</Table.Th>
                  <Table.Th>{t('users.email')}</Table.Th>
                  <Table.Th>{t('users.role')}</Table.Th>
                  <Table.Th>{t('sec.companiesAccess')}</Table.Th>
                  <Table.Th>{t('common.status')}</Table.Th>
                  <Table.Th>{t('sec.lastSeen')}</Table.Th>
                  <Table.Th />
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {rows.map((u) => {
                  const self = u.id === me.id;
                  return (
                    <Table.Tr key={u.id}>
                      <Table.Td fw={500}>
                        <Group gap={6} wrap="nowrap">
                          {u.full_name}
                          {self && <Badge size="xs" variant="light">{t('common.you')}</Badge>}
                          {u.mfa_enabled && <IconShieldCheck size={14} color="var(--mantine-color-teal-6)" aria-label={t('sec.twoFactor')} />}
                        </Group>
                      </Table.Td>
                      <Table.Td dir="ltr" style={{ textAlign: 'start' }}>{u.email}</Table.Td>
                      <Table.Td w={200}>
                        <Select size="xs" data={roleOptions} value={roleKey(u)} disabled={self || !canEdit} allowDeselect={false}
                          onChange={(v) => v && v !== roleKey(u) && void patch(u, roleBody(v))} />
                      </Table.Td>
                      <Table.Td>
                        <Text size="sm" c={u.company_ids ? undefined : 'dimmed'}>
                          {u.company_ids ? u.company_ids.map(companyName).join(', ') : t('sec.allCompanies')}
                        </Text>
                      </Table.Td>
                      <Table.Td>
                        <Switch checked={u.is_active} disabled={self || !canEdit} onChange={() => void patch(u, { is_active: !u.is_active })}
                          label={u.is_active ? t('common.active') : t('common.inactive')} />
                      </Table.Td>
                      <Table.Td c="dimmed"><Text size="xs">{u.last_seen_at ? formatDateTime(u.last_seen_at) : formatDate(u.created_at)}</Text></Table.Td>
                      <Table.Td>
                        {canEdit && !self && (
                          <Menu position="bottom-end" withinPortal>
                            <Menu.Target><Button size="compact-xs" variant="subtle"><IconDots size={16} /></Button></Menu.Target>
                            <Menu.Dropdown>
                              <Menu.Item onClick={() => setAccess(u)}>{t('sec.companiesAccess')}</Menu.Item>
                              <Menu.Item onClick={() => void resetLink(u)}>{t('sec.makeResetLink')}</Menu.Item>
                              <Menu.Item onClick={() => setSessionsOf(u)}>{t('sec.sessions')}</Menu.Item>
                              <Menu.Item color="red" onClick={() => void signOut(u)}>{t('sec.revokeAll')}</Menu.Item>
                              {u.mfa_enabled && <Menu.Item color="red" onClick={() => void resetMfa(u)}>{t('sec.resetMfa')}</Menu.Item>}
                            </Menu.Dropdown>
                          </Menu>
                        )}
                      </Table.Td>
                    </Table.Tr>
                  );
                })}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </Paper>
      <NewUser opened={opened} onClose={modal.close} onCreated={load} roleOptions={roleOptions} />
      {access && <CompanyAccess user={access} onClose={() => setAccess(null)} onSave={async (body) => { if (await patch(access, body)) setAccess(null); }} />}
      {link && (
        <Modal opened onClose={() => setLink(null)} title={t('sec.resetLink')} size="lg">
          <Stack>
            <Text size="sm">{t('sec.resetLinkHelp')}</Text>
            <Code block dir="ltr" style={{ wordBreak: 'break-all', whiteSpace: 'pre-wrap' }}>{link.url}</Code>
            <Group justify="flex-end">
              <CopyButton value={link.url}>
                {({ copied, copy }) => <Button leftSection={<IconCopy size={16} />} onClick={copy}>{copied ? t('sec.copied') : t('dev.copy')}</Button>}
              </CopyButton>
              <Button variant="default" onClick={() => setLink(null)}>{t('common.close')}</Button>
            </Group>
          </Stack>
        </Modal>
      )}
      {sessionsOf && <UserSessions user={sessionsOf} onClose={() => setSessionsOf(null)} />}
    </Stack>
  );
}

function UserSessions({ user, onClose }: { user: User; onClose: () => void }) {
  const { t } = useTranslation();
  const [rows, setRows] = useState<Parameters<typeof SessionsTable>[0]['rows'] | null>(null);
  useEffect(() => { api<typeof rows>(`/users/${user.id}/sessions`).then(setRows).catch((e) => notifyError(t, e)); }, [user.id, t]);
  return (
    <Modal opened onClose={onClose} title={`${t('sec.sessions')} · ${user.full_name}`} size="xl">
      {rows === null ? <Center p="lg"><Loader size="sm" /></Center> : <SessionsTable rows={rows} />}
    </Modal>
  );
}

function CompanyAccess({ user, onClose, onSave }: { user: User; onClose: () => void; onSave: (b: Record<string, unknown>) => void }) {
  const { t } = useTranslation();
  const { companies } = useCompany();
  const [mode, setMode] = useState(user.company_ids ? 'some' : 'all');
  const [ids, setIds] = useState<string[]>(user.company_ids ?? []);
  return (
    <Modal opened onClose={onClose} title={`${t('sec.companiesAccess')} · ${user.full_name}`}>
      <Stack>
        <Radio.Group value={mode} onChange={setMode}>
          <Stack gap="xs">
            <Radio value="all" label={t('sec.allCompanies')} />
            <Radio value="some" label={t('sec.someCompanies')} />
          </Stack>
        </Radio.Group>
        {mode === 'some' && (
          <MultiSelect data={companies.map((c) => ({ value: c.id, label: c.name_en }))} value={ids} onChange={setIds} searchable />
        )}
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
          <Button disabled={mode === 'some' && ids.length === 0}
            onClick={() => onSave(mode === 'all' ? { all_companies: true } : { company_ids: ids })}>{t('common.save')}</Button>
        </Group>
      </Stack>
    </Modal>
  );
}

function NewUser({ opened, onClose, onCreated, roleOptions }: {
  opened: boolean; onClose: () => void; onCreated: () => void; roleOptions: { group: string; items: { value: string; label: string }[] }[];
}) {
  const { t } = useTranslation();
  const { companies } = useCompany();
  const empty = { full_name: '', email: '', role: 'builtin:data_entry', locale: 'ar', password: '' };
  const [form, setForm] = useState(empty);
  const [ids, setIds] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const set = (k: keyof typeof empty) => (v: string) => setForm((f) => ({ ...f, [k]: v }));

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      const { role, ...rest } = form;
      const rb = roleBody(role);
      await api('/users', {
        method: 'POST',
        body: { ...rest, role: rb.role ?? '', ...(rb.role_id ? { role_id: rb.role_id } : {}), ...(ids.length ? { company_ids: ids } : {}) },
      });
      notifySaved(t);
      setForm(empty);
      setIds([]);
      onClose();
      onCreated();
    } catch (err) {
      notifyError(t, err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened={opened} onClose={onClose} title={t('users.new')}>
      <form onSubmit={submit}>
        <Stack>
          <TextInput label={t('users.fullName')} required value={form.full_name}
            onChange={(e) => set('full_name')(e.currentTarget.value)} data-autofocus />
          <TextInput label={t('users.email')} type="email" dir="ltr" required value={form.email}
            onChange={(e) => set('email')(e.currentTarget.value)} />
          <Select label={t('users.role')} data={roleOptions} value={form.role} allowDeselect={false}
            onChange={(v) => v && set('role')(v)} />
          {companies.length > 1 && (
            <MultiSelect label={t('sec.companiesAccess')} placeholder={t('sec.allCompanies')} value={ids} onChange={setIds}
              data={companies.map((c) => ({ value: c.id, label: c.name_en }))} />
          )}
          <Select label={t('users.language')} value={form.locale} allowDeselect={false}
            data={languages.map((l) => ({ value: l.code, label: l.label }))}
            onChange={(v) => v && set('locale')(v)} />
          <PasswordInput label={t('users.password')} description={t('sec.passwordPolicy', { n: 10 })} dir="ltr" required minLength={10}
            value={form.password} onChange={(e) => set('password')(e.currentTarget.value)} autoComplete="new-password" />
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
            <Button type="submit" loading={busy}>{t('common.save')}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}

// ---- roles ----

function RolesTab({ me }: { me: Me }) {
  const { t } = useTranslation();
  const cat = useCatalogue();
  const [rows, setRows] = useState<CustomRole[] | null>(null);
  const [edit, setEdit] = useState<CustomRole | 'new' | null>(null);
  const [view, setView] = useState<Role | null>(null);
  const load = useCallback(() => { api<CustomRole[]>('/roles').then(setRows).catch((e) => notifyError(t, e)); }, [t]);
  useEffect(load, [load]);
  const canEdit = can(me, 'users.edit');

  return (
    <Stack>
      <Group justify="space-between">
        <Text size="sm" c="dimmed" maw={700}>{t('sec.rolesHelp')}</Text>
        {canEdit && <Button leftSection={<IconPlus size={16} />} onClick={() => setEdit('new')}>{t('sec.newRole')}</Button>}
      </Group>
      <Paper withBorder>
        <Table verticalSpacing="sm">
          <Table.Thead>
            <Table.Tr>
              <Table.Th>{t('sec.roleName')}</Table.Th>
              <Table.Th>{t('sec.baseRole')}</Table.Th>
              <Table.Th>{t('sec.permissions')}</Table.Th>
              <Table.Th>{t('sec.usersCount')}</Table.Th>
              <Table.Th />
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {roles.map((r) => (
              <Table.Tr key={r}>
                <Table.Td><Group gap={6}>{t(`roles.${r}`)}<Badge size="xs" variant="light" color="gray">{t('sec.builtIn')}</Badge></Group></Table.Td>
                <Table.Td>—</Table.Td>
                <Table.Td>{cat?.presets[r]?.length ?? ''}</Table.Td>
                <Table.Td />
                <Table.Td style={{ textAlign: 'end' }}><Button size="compact-xs" variant="subtle" onClick={() => setView(r)}>{t('sec.show')}</Button></Table.Td>
              </Table.Tr>
            ))}
            {rows === null ? (
              <Table.Tr><Table.Td colSpan={5}><Center p="md"><Loader size="sm" /></Center></Table.Td></Table.Tr>
            ) : rows.map((r) => (
              <Table.Tr key={r.id} style={{ opacity: r.is_active ? 1 : 0.5 }}>
                <Table.Td fw={500}>{r.name}{r.description && <Text size="xs" c="dimmed">{r.description}</Text>}</Table.Td>
                <Table.Td>{t(`roles.${r.base}`)}</Table.Td>
                <Table.Td>{r.permissions.length}</Table.Td>
                <Table.Td>{r.users}</Table.Td>
                <Table.Td style={{ textAlign: 'end' }}>
                  <Button size="compact-xs" variant="subtle" onClick={() => setEdit(r)}>{canEdit ? t('common.edit') : t('sec.show')}</Button>
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      </Paper>
      {cat && edit && <RoleEditor cat={cat} me={me} role={edit === 'new' ? null : edit} readOnly={!canEdit}
        onClose={() => setEdit(null)} onSaved={() => { setEdit(null); load(); }} />}
      {cat && view && (
        <Modal opened onClose={() => setView(null)} title={t(`roles.${view}`)} size="xl">
          <PermissionMatrix cat={cat} value={cat.presets[view] ?? []} onChange={() => {}} readOnly />
        </Modal>
      )}
    </Stack>
  );
}

export function PermissionMatrix({ cat, value, onChange, readOnly, allowed }: {
  cat: Catalogue; value: string[]; onChange: (v: string[]) => void; readOnly?: boolean; allowed?: string[];
}) {
  const { t } = useTranslation();
  const actions = ['view', 'create', 'edit', 'void', 'manage', 'approve', 'export'];
  const set = new Set(value);
  const toggle = (p: string) => {
    const next = new Set(set);
    if (next.has(p)) next.delete(p);
    else next.add(p);
    onChange([...next].sort());
  };
  const toggleRow = (mod: string, acts: string[]) => {
    const perms = acts.map((a) => `${mod}.${a}`).filter((p) => !allowed || allowed.includes(p));
    const all = perms.every((p) => set.has(p));
    const next = new Set(set);
    perms.forEach((p) => (all ? next.delete(p) : next.add(p)));
    onChange([...next].sort());
  };
  return (
    <ScrollArea type="auto">
      <Table verticalSpacing={4} miw={720} withRowBorders>
        <Table.Thead>
          <Table.Tr>
            <Table.Th>{t('sec.module')}</Table.Th>
            {actions.map((a) => <Table.Th key={a} ta="center"><Text size="xs" fw={600}>{t(`perm.actions.${a}`)}</Text></Table.Th>)}
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {cat.modules.map((m) => (
            <Table.Tr key={m.module}>
              <Table.Td>
                <Text size="sm" style={{ cursor: readOnly ? undefined : 'pointer' }} onClick={() => !readOnly && toggleRow(m.module, m.actions)}>
                  {t(`perm.modules.${m.module}`)}
                </Text>
              </Table.Td>
              {actions.map((a) => {
                const p = `${m.module}.${a}`;
                if (!m.actions.includes(a)) return <Table.Td key={a} />;
                return (
                  <Table.Td key={a} ta="center">
                    <Checkbox size="xs" mx="auto" checked={set.has(p)} disabled={readOnly || (allowed && !allowed.includes(p) && !set.has(p))}
                      onChange={() => toggle(p)} aria-label={p} />
                  </Table.Td>
                );
              })}
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </ScrollArea>
  );
}

function RoleEditor({ cat, me, role, readOnly, onClose, onSaved }: {
  cat: Catalogue; me: Me; role: CustomRole | null; readOnly: boolean; onClose: () => void; onSaved: () => void;
}) {
  const { t } = useTranslation();
  const [name, setName] = useState(role?.name ?? '');
  const [description, setDescription] = useState(role?.description ?? '');
  const [base, setBase] = useState<Role>(role?.base ?? 'data_entry');
  const [perms, setPerms] = useState<string[]>(role?.permissions ?? cat.presets['data_entry'] ?? []);
  const [active, setActive] = useState(role?.is_active ?? true);
  const [busy, setBusy] = useState(false);

  async function save() {
    setBusy(true);
    try {
      const body = { name, description, base, permissions: perms, ...(role ? { is_active: active } : {}) };
      await api(role ? `/roles/${role.id}` : '/roles', { method: role ? 'PATCH' : 'POST', body });
      notifySaved(t);
      onSaved();
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened onClose={onClose} title={role ? role.name : t('sec.newRole')} size="xl">
      <Stack>
        <Group grow align="flex-start">
          <TextInput label={t('sec.roleName')} required value={name} onChange={(e) => setName(e.currentTarget.value)} disabled={readOnly} />
          <Select label={t('sec.baseRole')} description={t('sec.baseHelp')} value={base} allowDeselect={false} disabled={readOnly}
            data={roles.map((r) => ({ value: r, label: t(`roles.${r}`) }))} onChange={(v) => v && setBase(v as Role)} />
        </Group>
        <Textarea label={t('sec.description')} autosize minRows={1} value={description} onChange={(e) => setDescription(e.currentTarget.value)} disabled={readOnly} />
        {!readOnly && (
          <Group gap="xs">
            <Text size="sm">{t('sec.startFrom')}:</Text>
            {roles.map((r) => (
              <Button key={r} size="compact-xs" variant="default"
                onClick={() => setPerms((cat.presets[r] ?? []).filter((p) => me.permissions.includes(p)))}>{t(`roles.${r}`)}</Button>
            ))}
          </Group>
        )}
        <PermissionMatrix cat={cat} value={perms} onChange={setPerms} readOnly={readOnly} allowed={me.permissions} />
        {role && <Switch label={t('common.active')} checked={active} onChange={(e) => setActive(e.currentTarget.checked)} disabled={readOnly} />}
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>{t('common.close')}</Button>
          {!readOnly && <Button loading={busy} disabled={!name.trim()} onClick={() => void save()}>{t('common.save')}</Button>}
        </Group>
      </Stack>
    </Modal>
  );
}

// ---- organisation security ----

function OrgSecurity({ me }: { me: Me }) {
  const { t } = useTranslation();
  const [s, setS] = useState<{ require_mfa: boolean; password_min_length: number; users: number; users_with_mfa: number } | null>(null);
  const [minLen, setMinLen] = useState<number>(10);
  const load = useCallback(() => {
    api<NonNullable<typeof s>>('/security/settings').then((x) => { setS(x); setMinLen(x.password_min_length); }).catch((e) => notifyError(t, e));
  }, [t]);
  useEffect(load, [load]);
  const canEdit = can(me, 'users.edit');

  async function put(body: Record<string, unknown>) {
    try {
      await api('/security/settings', { method: 'PUT', body });
      notifySaved(t);
      load();
    } catch (e) {
      notifyError(t, e);
    }
  }
  if (!s) return <Center p="lg"><Loader size="sm" /></Center>;
  return (
    <Stack maw={700}>
      <Paper withBorder p="md">
        <Stack>
          <Switch label={t('sec.requireMfa')} description={t('sec.requireMfaHelp')} checked={s.require_mfa} disabled={!canEdit}
            onChange={(e) => void put({ require_mfa: e.currentTarget.checked })} />
          {!me.mfa_enabled && !s.require_mfa && <Alert color="yellow" variant="light">{t('errors.mfa_enable_first')}</Alert>}
          <Text size="sm" c="dimmed">{t('sec.usersWithMfa', { n: s.users_with_mfa, total: s.users })}</Text>
        </Stack>
      </Paper>
      <Paper withBorder p="md">
        <Group align="flex-end">
          <NumberInput label={t('sec.minLength')} min={10} max={64} value={minLen} onChange={(v) => setMinLen(Number(v) || 10)} disabled={!canEdit} w={200} />
          {canEdit && <Button variant="light" disabled={minLen === s.password_min_length} onClick={() => void put({ password_min_length: minLen })}>{t('common.save')}</Button>}
        </Group>
      </Paper>
    </Stack>
  );
}
