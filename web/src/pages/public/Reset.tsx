import { useEffect, useState } from 'react';
import { Alert, Anchor, Box, Button, Center, Group, Loader, Paper, PasswordInput, Stack, Text, Title } from '@mantine/core';
import { useTranslation } from 'react-i18next';
import { api } from '../../api';
import { errorText } from '../../util';
import LangSwitch from '../../components/LangSwitch';
import Logo from '../../components/Logo';

/** #/reset/<token>: choose a new password from a reset link (e-mail or given by an administrator). */
export default function Reset({ token }: { token: string }) {
  const { t } = useTranslation();
  const [state, setState] = useState<'checking' | 'ok' | 'invalid' | 'done'>('checking');
  const [email, setEmail] = useState('');
  const [pw, setPw] = useState('');
  const [pw2, setPw2] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api<{ email: string }>(`/auth/reset/${encodeURIComponent(token)}`)
      .then((r) => { setEmail(r.email); setState('ok'); })
      .catch(() => setState('invalid'));
  }, [token]);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (pw !== pw2) {
      setError(t('sec.mismatch'));
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await api('/auth/reset', { method: 'POST', body: { token, password: pw } });
      setState('done');
    } catch (err) {
      setError(errorText(t, err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Box mih="100vh" bg="var(--mx-panel)" px="md">
      <Group justify="space-between" pt="md">
        <Anchor href="#/" size="sm">← Paxve</Anchor>
        <LangSwitch />
      </Group>
      <Center mih="calc(100vh - 80px)">
        <Stack w="100%" maw={400} gap="lg">
          <Center><Logo size="xl" /></Center>
          <Paper withBorder shadow="sm" p="xl">
            {state === 'checking' && <Center><Loader size="sm" /></Center>}
            {state === 'invalid' && (
              <Stack>
                <Alert color="red" variant="light">{t('sec.resetInvalid')}</Alert>
                <Button component="a" href="#/login" variant="default">{t('sec.backToLogin')}</Button>
              </Stack>
            )}
            {state === 'done' && (
              <Stack>
                <Alert color="teal" variant="light">{t('sec.resetDone')}</Alert>
                <Button component="a" href="#/login">{t('login.submit')}</Button>
              </Stack>
            )}
            {state === 'ok' && (
              <form onSubmit={submit}>
                <Stack>
                  <Title order={4}>{t('sec.resetTitle')}</Title>
                  <Text size="sm" c="dimmed" dir="auto">{t('sec.resetFor', { email })}</Text>
                  {error && <Alert color="red" variant="light">{error}</Alert>}
                  <PasswordInput label={t('password.new')} description={t('sec.passwordPolicy', { n: 10 })} dir="ltr" required minLength={10}
                    value={pw} onChange={(e) => setPw(e.currentTarget.value)} autoComplete="new-password" />
                  <PasswordInput label={t('sec.repeatPassword')} dir="ltr" required value={pw2} onChange={(e) => setPw2(e.currentTarget.value)}
                    autoComplete="new-password" />
                  <Button type="submit" loading={busy} fullWidth>{t('common.save')}</Button>
                </Stack>
              </form>
            )}
          </Paper>
        </Stack>
      </Center>
    </Box>
  );
}
