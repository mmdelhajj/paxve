import { useState } from 'react';
import { Alert, Box, Button, Center, Group, Paper, PasswordInput, Stack, Text, TextInput, Title } from '@mantine/core';
import { useTranslation } from 'react-i18next';
import { api, type Me } from '../api';
import { errorText } from '../util';
import LangSwitch from '../components/LangSwitch';
import Logo from '../components/Logo';

export default function Login({ onLogin }: { onLogin: (me: Me) => void }) {
  const { t } = useTranslation();
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      onLogin(await api<Me>('/auth/login', { method: 'POST', body: { email, password } }));
    } catch (err) {
      setError(errorText(t, err));
      setBusy(false);
    }
  }

  return (
    <Box mih="100vh" bg="var(--mantine-color-gray-0)" px="md">
      <Group justify="flex-end" pt="md">
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
            <form onSubmit={submit}>
              <Stack>
                <Title order={4}>{t('login.title')}</Title>
                {error && (
                  <Alert color="red" variant="light">
                    {error}
                  </Alert>
                )}
                <TextInput
                  label={t('login.email')}
                  type="email"
                  autoComplete="username"
                  dir="ltr"
                  required
                  value={email}
                  onChange={(e) => setEmail(e.currentTarget.value)}
                />
                <PasswordInput
                  label={t('login.password')}
                  autoComplete="current-password"
                  dir="ltr"
                  required
                  value={password}
                  onChange={(e) => setPassword(e.currentTarget.value)}
                />
                <Button type="submit" loading={busy} fullWidth mt="xs">
                  {t('login.submit')}
                </Button>
              </Stack>
            </form>
          </Paper>
        </Stack>
      </Center>
    </Box>
  );
}
