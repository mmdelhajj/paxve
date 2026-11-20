import { useEffect, useState } from 'react';
import {
  Alert, Anchor, Box, Button, Center, Group, Paper, PasswordInput, PinInput, Stack, Text, TextInput, Title,
} from '@mantine/core';
import { useTranslation } from 'react-i18next';
import { api, type Me } from '../api';
import { errorText } from '../util';
import LangSwitch from '../components/LangSwitch';
import Logo from '../components/Logo';

type Step = 'password' | 'mfa' | 'forgot' | 'sent';

export default function Login({ onLogin }: { onLogin: (me: Me) => void }) {
  const { t } = useTranslation();
  const [step, setStep] = useState<Step>('password');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [code, setCode] = useState('');
  const [recovery, setRecovery] = useState<string | null>(null); // null = use the app code
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [sso, setSso] = useState<string[]>([]);

  // Google / Microsoft sign-in (phase 8): the provider sends the browser back to #/login?sso=mfa (two-step code
  // still needed) or #/login?sso_error=<code>
  useEffect(() => {
    api<string[]>('/auth/oidc/providers').then(setSso).catch(() => setSso([]));
    const q = new URLSearchParams(window.location.hash.split('?')[1] ?? '');
    if (q.get('sso') === 'mfa') setStep('mfa');
    const e = q.get('sso_error');
    if (e) setError(t(`ep.sso.${e}`, { defaultValue: t('ep.sso.sso_failed') }));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const r = await api<Me & { mfa_required?: boolean }>('/auth/login', { method: 'POST', body: { email, password } });
      if (r.mfa_required) {
        setStep('mfa');
        setBusy(false);
        return;
      }
      onLogin(r);
    } catch (err) {
      setError(errorText(t, err));
      setBusy(false);
    }
  }

  async function verify(value?: string) {
    setBusy(true);
    setError(null);
    try {
      const body = recovery !== null ? { recovery_code: recovery } : { code: value ?? code };
      onLogin(await api<Me>('/auth/mfa', { method: 'POST', body }));
    } catch (err) {
      setError(errorText(t, err));
      setCode('');
      setBusy(false);
      if (err instanceof Error && err.message === 'mfa_expired') setStep('password');
    }
  }

  async function forgot(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api('/auth/forgot', { method: 'POST', body: { email } });
      setStep('sent');
    } catch (err) {
      setError(errorText(t, err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Box mih="100vh" bg="var(--mantine-color-gray-0)" px="md">
      <Group justify="space-between" pt="md">
        <Anchor href="#/" size="sm">← Paxve</Anchor>
        <LangSwitch />
      </Group>
      <Center mih="calc(100vh - 80px)">
        <Stack w="100%" maw={400} gap="lg">
          <Stack gap={4} align="center">
            <Logo size="xl" />
            <Text c="dimmed" size="sm" ta="center">
              {t('app.tagline')}
            </Text>
          </Stack>
          <Paper withBorder shadow="sm" p="xl">
            {error && (
              <Alert color="red" variant="light" mb="md">
                {error}
              </Alert>
            )}
            {step === 'password' && (
              <form onSubmit={submit}>
                <Stack>
                  <Title order={4}>{t('login.title')}</Title>
                  <TextInput label={t('login.email')} type="email" autoComplete="username" dir="ltr" required value={email}
                    onChange={(e) => setEmail(e.currentTarget.value)} />
                  <PasswordInput label={t('login.password')} autoComplete="current-password" dir="ltr" required value={password}
                    onChange={(e) => setPassword(e.currentTarget.value)} />
                  <Group justify="flex-end" mt={-8}>
                    <Anchor component="button" type="button" size="xs" onClick={() => { setError(null); setStep('forgot'); }}>
                      {t('sec.forgot')}
                    </Anchor>
                  </Group>
                  <Button type="submit" loading={busy} fullWidth>
                    {t('login.submit')}
                  </Button>
                  {sso.map((p) => (
                    <Button key={p} variant="default" fullWidth component="a" href={`/api/v1/auth/oidc/${p}/start`}>
                      {t(`ep.sso.with_${p}`)}
                    </Button>
                  ))}
                  <Text size="sm" ta="center" c="dimmed">
                    {t('signup.noAccount')} <Anchor href="#/signup">{t('landing.startTrial')}</Anchor>
                  </Text>
                </Stack>
              </form>
            )}
            {step === 'mfa' && (
              <Stack>
                <Title order={4}>{t('sec.twoFactor')}</Title>
                {recovery === null ? (
                  <>
                    <Text size="sm">{t('sec.loginCode')}</Text>
                    <Center dir="ltr">
                      <PinInput length={6} type="number" oneTimeCode autoFocus value={code} onChange={setCode}
                        onComplete={(v) => void verify(v)} disabled={busy} />
                    </Center>
                    <Button loading={busy} disabled={code.length !== 6} onClick={() => void verify()} fullWidth>{t('sec.verify')}</Button>
                    <Anchor component="button" type="button" size="sm" ta="center" onClick={() => { setRecovery(''); setError(null); }}>
                      {t('sec.useRecovery')}
                    </Anchor>
                  </>
                ) : (
                  <form onSubmit={(e) => { e.preventDefault(); void verify(); }}>
                    <Stack>
                      <TextInput label={t('sec.recoveryCode')} dir="ltr" required autoFocus value={recovery} autoComplete="off"
                        placeholder="xxxx-xxxx-xxxx" onChange={(e) => setRecovery(e.currentTarget.value)} />
                      <Button type="submit" loading={busy} fullWidth>{t('sec.verify')}</Button>
                      <Anchor component="button" type="button" size="sm" ta="center" onClick={() => { setRecovery(null); setError(null); }}>
                        {t('sec.useCode')}
                      </Anchor>
                    </Stack>
                  </form>
                )}
              </Stack>
            )}
            {step === 'forgot' && (
              <form onSubmit={forgot}>
                <Stack>
                  <Title order={4}>{t('sec.forgotTitle')}</Title>
                  <Text size="sm" c="dimmed">{t('sec.forgotHelp')}</Text>
                  <TextInput label={t('login.email')} type="email" dir="ltr" required value={email} onChange={(e) => setEmail(e.currentTarget.value)} />
                  <Button type="submit" loading={busy} fullWidth>{t('sec.sendLink')}</Button>
                  <Anchor component="button" type="button" size="sm" ta="center" onClick={() => setStep('password')}>{t('sec.backToLogin')}</Anchor>
                </Stack>
              </form>
            )}
            {step === 'sent' && (
              <Stack>
                <Alert color="teal" variant="light">{t('sec.forgotSent')}</Alert>
                <Button variant="default" onClick={() => setStep('password')}>{t('sec.backToLogin')}</Button>
              </Stack>
            )}
          </Paper>
        </Stack>
      </Center>
    </Box>
  );
}
