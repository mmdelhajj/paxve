import { useCallback, useEffect, useState } from 'react';
import {
  Alert, Badge, Box, Button, Center, Code, CopyButton, Group, Image, Loader, Modal, Paper, PasswordInput, PinInput, SimpleGrid,
  Stack, Table, Text, TextInput, Title,
} from '@mantine/core';
import { IconCopy, IconDeviceDesktop, IconLogout, IconShieldCheck, IconShieldLock } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import QRCode from 'qrcode';
import { api, type Me } from '../api';
import { formatDateTime, notifyError, notifySaved } from '../util';
import Logo from '../components/Logo';
import LangSwitch from '../components/LangSwitch';

interface SessionRow {
  id: string;
  created_at: string;
  last_seen_at: string;
  ip: string | null;
  user_agent: string;
  mfa: boolean;
  current?: boolean;
}

/** Short device name from a user agent. */
export function deviceName(ua: string): string {
  if (!ua) return '—';
  const os = /Android/i.test(ua) ? 'Android' : /iPhone|iPad/i.test(ua) ? 'iOS' : /Windows/i.test(ua) ? 'Windows'
    : /Mac OS/i.test(ua) ? 'macOS' : /Linux/i.test(ua) ? 'Linux' : '';
  const br = /Edg\//.test(ua) ? 'Edge' : /Chrome\//.test(ua) ? 'Chrome' : /Firefox\//.test(ua) ? 'Firefox'
    : /Safari\//.test(ua) ? 'Safari' : /Paxve/i.test(ua) ? 'Paxve app' : '';
  return [br, os].filter(Boolean).join(' · ') || ua.slice(0, 40);
}

export function SessionsTable({ rows, onRevoke }: { rows: SessionRow[]; onRevoke?: (id: string) => void }) {
  const { t } = useTranslation();
  if (rows.length === 0) return <Text c="dimmed" ta="center" p="md">{t('common.empty')}</Text>;
  return (
    <Table.ScrollContainer minWidth={560}>
      <Table verticalSpacing="xs">
        <Table.Thead>
          <Table.Tr>
            <Table.Th>{t('sec.device')}</Table.Th>
            <Table.Th>{t('sec.ip')}</Table.Th>
            <Table.Th>{t('sec.signedInAt')}</Table.Th>
            <Table.Th>{t('sec.lastSeen')}</Table.Th>
            {onRevoke && <Table.Th />}
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {rows.map((s) => (
            <Table.Tr key={s.id}>
              <Table.Td>
                <Group gap={6} wrap="nowrap">
                  <IconDeviceDesktop size={16} />
                  <Text size="sm">{deviceName(s.user_agent)}</Text>
                  {s.current && <Badge size="xs" color="teal">{t('sec.thisDevice')}</Badge>}
                  {s.mfa && <IconShieldCheck size={14} color="var(--mantine-color-teal-6)" />}
                </Group>
              </Table.Td>
              <Table.Td dir="ltr" style={{ textAlign: 'start' }}><Text size="sm">{s.ip ?? '—'}</Text></Table.Td>
              <Table.Td><Text size="sm">{formatDateTime(s.created_at)}</Text></Table.Td>
              <Table.Td><Text size="sm">{formatDateTime(s.last_seen_at)}</Text></Table.Td>
              {onRevoke && (
                <Table.Td style={{ textAlign: 'end' }}>
                  {!s.current && <Button size="compact-xs" variant="subtle" color="red" onClick={() => onRevoke(s.id)}>{t('sec.revoke')}</Button>}
                </Table.Td>
              )}
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </Table.ScrollContainer>
  );
}

export function RecoveryCodes({ codes, onDone }: { codes: string[]; onDone: () => void }) {
  const { t } = useTranslation();
  return (
    <Stack>
      <Alert color="yellow" icon={<IconShieldLock size={16} />} title={t('sec.recoveryTitle')}>{t('sec.recoveryHelp')}</Alert>
      <SimpleGrid cols={2} spacing="xs">
        {codes.map((c) => <Code key={c} ta="center" dir="ltr" fz="md">{c}</Code>)}
      </SimpleGrid>
      <Group justify="flex-end">
        <CopyButton value={codes.join('\n')}>
          {({ copied, copy }) => (
            <Button variant="default" leftSection={<IconCopy size={16} />} onClick={copy}>{copied ? t('sec.copied') : t('dev.copy')}</Button>
          )}
        </CopyButton>
        <Button onClick={onDone}>{t('common.close')}</Button>
      </Group>
    </Stack>
  );
}

/** Two-step sign-in set-up: QR code, confirm with a code, show the recovery codes. */
export function MfaSetup({ onEnabled }: { onEnabled: () => void }) {
  const { t } = useTranslation();
  const [setup, setSetup] = useState<{ secret: string; otpauth_uri: string } | null>(null);
  const [qr, setQr] = useState('');
  const [code, setCode] = useState('');
  const [busy, setBusy] = useState(false);
  const [codes, setCodes] = useState<string[] | null>(null);

  useEffect(() => {
    api<{ secret: string; otpauth_uri: string }>('/me/mfa/setup', { method: 'POST', body: {} })
      .then((s) => {
        setSetup(s);
        QRCode.toDataURL(s.otpauth_uri, { margin: 1, width: 220 }).then(setQr).catch(() => setQr(''));
      })
      .catch((e) => notifyError(t, e));
  }, [t]);

  async function confirm(c = code) {
    if (c.length !== 6) return;
    setBusy(true);
    try {
      const r = await api<{ recovery_codes: string[] }>('/me/mfa/enable', { method: 'POST', body: { code: c } });
      setCodes(r.recovery_codes);
    } catch (e) {
      notifyError(t, e);
      setCode('');
    } finally {
      setBusy(false);
    }
  }

  if (codes) return <RecoveryCodes codes={codes} onDone={onEnabled} />;
  if (!setup) return <Center p="lg"><Loader size="sm" /></Center>;
  return (
    <Stack>
      <Text size="sm">{t('sec.scanQr')}</Text>
      <Center>{qr ? <Image src={qr} w={220} h={220} alt="QR" /> : <Loader size="sm" />}</Center>
      <Code block dir="ltr" ta="center" style={{ wordBreak: 'break-all' }}>{setup.secret.replace(/(.{4})/g, '$1 ').trim()}</Code>
      <Text size="sm" fw={500}>{t('sec.enterCode')}</Text>
      <Center dir="ltr">
        <PinInput length={6} type="number" oneTimeCode value={code} onChange={setCode} onComplete={(v) => void confirm(v)} disabled={busy} />
      </Center>
      <Group justify="flex-end">
        <Button loading={busy} disabled={code.length !== 6} onClick={() => void confirm()}>{t('sec.confirmCode')}</Button>
      </Group>
    </Stack>
  );
}

/** Shown instead of the app when the organisation requires two-step sign-in and the user has not set it up. */
export function ForcedMfa({ onDone, onLogout }: { onDone: () => void; onLogout: () => void }) {
  const { t } = useTranslation();
  return (
    <Box mih="100vh" bg="var(--mantine-color-gray-0)" px="md">
      <Group justify="space-between" pt="md">
        <Logo />
        <Group gap="xs">
          <LangSwitch />
          <Button size="xs" variant="subtle" leftSection={<IconLogout size={14} />} onClick={onLogout}>{t('common.logout')}</Button>
        </Group>
      </Group>
      <Center py="xl">
        <Paper withBorder shadow="sm" p="xl" w="100%" maw={440}>
          <Stack>
            <Title order={4}>{t('sec.mfaRequiredTitle')}</Title>
            <Text size="sm" c="dimmed">{t('sec.mfaRequiredBody')}</Text>
            <MfaSetup onEnabled={onDone} />
          </Stack>
        </Paper>
      </Center>
    </Box>
  );
}

/** My security: two-step sign-in and signed-in devices. */
export default function MySecurity({ me, onMe }: { me: Me; onMe: (m: Me) => void }) {
  const { t } = useTranslation();
  const [sessions, setSessions] = useState<SessionRow[] | null>(null);
  const [setupOpen, setSetupOpen] = useState(false);
  const [disableOpen, setDisableOpen] = useState(false);
  const [codesOpen, setCodesOpen] = useState(false);

  const load = useCallback(() => {
    api<SessionRow[]>('/me/sessions').then(setSessions).catch((e) => notifyError(t, e));
  }, [t]);
  useEffect(load, [load]);

  const refreshMe = () => api<Me>('/me').then(onMe).catch(() => {});

  async function revoke(id: string) {
    try {
      await api(`/me/sessions/${id}/revoke`, { method: 'POST', body: {} });
      load();
    } catch (e) {
      notifyError(t, e);
    }
  }
  async function revokeOthers() {
    try {
      await api('/me/sessions/revoke-others', { method: 'POST', body: {} });
      notifySaved(t);
      load();
    } catch (e) {
      notifyError(t, e);
    }
  }

  return (
    <Stack gap="lg" maw={900}>
      <Title order={2}>{t('sec.mySecurity')}</Title>
      <Paper withBorder p="md">
        <Group justify="space-between" align="flex-start">
          <Stack gap={4} maw={560}>
            <Group gap="xs">
              <Title order={5}>{t('sec.twoFactor')}</Title>
              <Badge color={me.mfa_enabled ? 'teal' : 'gray'}>{me.mfa_enabled ? t('sec.twoFactorOn') : t('sec.twoFactorOff')}</Badge>
            </Group>
            <Text size="sm" c="dimmed">{t('sec.twoFactorHelp')}</Text>
            {me.mfa_required && <Text size="sm" c="orange">{t('sec.requiredByOrg')}</Text>}
          </Stack>
          {me.mfa_enabled ? (
            <Group gap="xs">
              <Button variant="default" onClick={() => setCodesOpen(true)}>{t('sec.newRecovery')}</Button>
              {!me.mfa_required && <Button variant="light" color="red" onClick={() => setDisableOpen(true)}>{t('sec.disable2fa')}</Button>}
            </Group>
          ) : (
            <Button leftSection={<IconShieldCheck size={16} />} onClick={() => setSetupOpen(true)}>{t('sec.enable2fa')}</Button>
          )}
        </Group>
      </Paper>

      <Paper withBorder>
        <Group justify="space-between" p="md">
          <Title order={5}>{t('sec.sessions')}</Title>
          <Button size="xs" variant="light" color="red" onClick={() => void revokeOthers()}>{t('sec.revokeOthers')}</Button>
        </Group>
        {sessions === null ? <Center p="lg"><Loader size="sm" /></Center> : <SessionsTable rows={sessions} onRevoke={(id) => void revoke(id)} />}
      </Paper>

      {setupOpen && (
        <Modal opened onClose={() => setSetupOpen(false)} title={t('sec.twoFactor')}>
          <MfaSetup onEnabled={() => { setSetupOpen(false); void refreshMe(); load(); }} />
        </Modal>
      )}
      {disableOpen && <DisableMfa onClose={() => setDisableOpen(false)} onDone={() => { setDisableOpen(false); void refreshMe(); }} />}
      {codesOpen && <NewCodes onClose={() => setCodesOpen(false)} />}
    </Stack>
  );
}

function DisableMfa({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const { t } = useTranslation();
  const [password, setPassword] = useState('');
  const [code, setCode] = useState('');
  const [busy, setBusy] = useState(false);
  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      const recovery = code.includes('-') || code.length > 6;
      await api('/me/mfa/disable', { method: 'POST', body: recovery ? { password, recovery_code: code } : { password, code } });
      notifySaved(t);
      onDone();
    } catch (err) {
      notifyError(t, err);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal opened onClose={onClose} title={t('sec.disable2fa')}>
      <form onSubmit={submit}>
        <Stack>
          <PasswordInput label={t('password.current')} dir="ltr" required value={password} onChange={(e) => setPassword(e.currentTarget.value)} />
          <TextInput label={t('sec.enterCode')} description={t('sec.codeOrRecovery')} dir="ltr" required value={code}
            onChange={(e) => setCode(e.currentTarget.value.trim())} autoComplete="one-time-code" />
          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
            <Button type="submit" color="red" loading={busy}>{t('sec.disable2fa')}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}

function NewCodes({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  const [code, setCode] = useState('');
  const [codes, setCodes] = useState<string[] | null>(null);
  const [busy, setBusy] = useState(false);
  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      setCodes((await api<{ recovery_codes: string[] }>('/me/mfa/recovery-codes', { method: 'POST', body: { code } })).recovery_codes);
    } catch (err) {
      notifyError(t, err);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal opened onClose={onClose} title={t('sec.newRecovery')}>
      {codes ? <RecoveryCodes codes={codes} onDone={onClose} /> : (
        <form onSubmit={submit}>
          <Stack>
            <TextInput label={t('sec.enterCode')} dir="ltr" required value={code} inputMode="numeric"
              onChange={(e) => setCode(e.currentTarget.value.trim())} autoComplete="one-time-code" />
            <Group justify="flex-end">
              <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
              <Button type="submit" loading={busy}>{t('sec.confirmCode')}</Button>
            </Group>
          </Stack>
        </form>
      )}
    </Modal>
  );
}
