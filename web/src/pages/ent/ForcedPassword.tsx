import { useState } from 'react';
import { Alert, Box, Button, Center, Paper, PasswordInput, Stack, Text, Title } from '@mantine/core';
import { useTranslation } from 'react-i18next';
import { api } from '../../api';
import { errorText } from '../../util';

/** The password is older than the organisation allows (phase 8): only a new password (or signing out) is possible. */
export default function ForcedPassword({ onDone, onLogout }: { onDone: () => void; onLogout: () => void }) {
  const { t } = useTranslation();
  const [cur, setCur] = useState('');
  const [pw, setPw] = useState('');
  const [pw2, setPw2] = useState('');
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  async function save(e: React.FormEvent) {
    e.preventDefault();
    if (pw !== pw2) { setErr(t('ep.pw.mismatch')); return; }
    setBusy(true);
    setErr(null);
    try {
      await api('/me', { method: 'PATCH', body: { current_password: cur, new_password: pw } });
      onDone();
    } catch (x) {
      setErr(errorText(t, x));
      setBusy(false);
    }
  }
  return (
    <Box mih="100vh" bg="var(--mx-panel)" px="md">
      <Center mih="100vh">
        <Paper withBorder shadow="sm" p="xl" w="100%" maw={420}>
          <form onSubmit={save}>
            <Stack>
              <Title order={4}>{t('ep.pw.title')}</Title>
              <Text size="sm" c="dimmed">{t('ep.pw.help')}</Text>
              {err && <Alert color="red" variant="light">{err}</Alert>}
              <PasswordInput label={t('ep.pw.current')} autoComplete="current-password" dir="ltr" required value={cur} onChange={(e) => setCur(e.currentTarget.value)} />
              <PasswordInput label={t('ep.pw.new')} autoComplete="new-password" dir="ltr" required value={pw} onChange={(e) => setPw(e.currentTarget.value)} />
              <PasswordInput label={t('ep.pw.again')} autoComplete="new-password" dir="ltr" required value={pw2} onChange={(e) => setPw2(e.currentTarget.value)} />
              <Button type="submit" loading={busy} fullWidth>{t('ep.pw.save')}</Button>
              <Button variant="subtle" onClick={onLogout}>{t('ep.pw.logout')}</Button>
            </Stack>
          </form>
        </Paper>
      </Center>
    </Box>
  );
}
