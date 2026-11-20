import { useState } from 'react';
import {
  Alert, Anchor, Box, Button, Center, Group, Paper, PasswordInput, Select, SimpleGrid, Stack, Text, TextInput, Title,
} from '@mantine/core';
import { useTranslation } from 'react-i18next';
import { api, type Me } from '../../api';
import { errorText } from '../../util';
import LangSwitch from '../../components/LangSwitch';
import Logo from '../../components/Logo';
import { PACKS, packName } from '../../l10n';

// business packs (apps + defaults + demo data chosen at sign-up)
const BIZ_PACKS = ['supermarket', 'clothing', 'electronics', 'furniture', 'wholesale', 'restaurant', 'pharmacy', 'factory'];
const currencies = ['USD', 'LBP', 'EUR', 'GBP', 'SAR', 'AED', 'QAR', 'KWD', 'BHD', 'OMR', 'JOD', 'IQD', 'SYP', 'EGP', 'TRY', 'MAD', 'DZD', 'TND', 'CAD', 'AUD', 'CHF'];

export default function Signup({ onSignedUp }: { onSignedUp: (m: Me) => void }) {
  const { t, i18n } = useTranslation();
  const [f, setF] = useState({
    organisation: '', company_name: '', full_name: '', email: '', password: '',
    base_currency: 'USD', local_currency: 'LBP', template: 'lebanon',
  });
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [biz, setBiz] = useState('generator');
  const set = (k: keyof typeof f) => (v: string) => setF((x) => ({ ...x, [k]: v }));

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const me = await api<Me>('/signup', { method: 'POST', body: { ...f, locale: i18n.language } });
      // the business pack: apps, defaults (and demo data for a quick start)
      try {
        const comps = await api<{ id: string; apps: string[] }[]>('/companies');
        if (comps[0]) {
          if (BIZ_PACKS.includes(biz)) await api(`/companies/${comps[0].id}/retail/packs/${biz}`, { method: 'POST', body: { demo: true } });
          else if (biz !== 'generator') await api(`/companies/${comps[0].id}/apps`, { method: 'PUT', body: { apps: comps[0].apps ?? [], business_type: biz } });
        }
      } catch { /* the pack can be chosen later from Retail → Business packs */ }
      onSignedUp(me);
    } catch (err) {
      setError(errorText(t, err));
      setBusy(false);
    }
  }

  return (
    <Box mih="100vh" bg="var(--mantine-color-gray-0)" px="md">
      <Group justify="space-between" pt="md">
        <Anchor href="#/"><Logo /></Anchor>
        <LangSwitch />
      </Group>
      <Center py="xl">
        <Stack w="100%" maw={560} gap="lg">
          <Stack gap={4} align="center">
            <Title order={2} ta="center">{t('signup.title')}</Title>
            <Text c="dimmed" ta="center">{t('landing.trialNote')}</Text>
          </Stack>
          <Paper withBorder shadow="sm" p="xl">
            <form onSubmit={submit}>
              <Stack>
                {error && <Alert color="red" variant="light">{error}</Alert>}
                <TextInput label={t('signup.organisation')} required value={f.organisation}
                  onChange={(e) => set('organisation')(e.currentTarget.value)} data-autofocus />
                <TextInput label={`${t('signup.company')} (${t('common.optional')})`} value={f.company_name}
                  placeholder={f.organisation} onChange={(e) => set('company_name')(e.currentTarget.value)} />
                <div>
                  <Text size="sm" fw={500} mb={4}>{t('signup.country')}</Text>
                  <Select searchable allowDeselect={false} value={f.template} aria-label={t('signup.country')}
                    data={PACKS.map((p) => ({ value: p.key, label: packName(p, i18n.language, t('signup.other')) }))}
                    onChange={(v) => {
                      const p = PACKS.find((x) => x.key === v);
                      if (p) setF((x) => ({ ...x, template: p.key, base_currency: p.base, local_currency: p.local }));
                    }} />
                  <Text size="xs" c="dimmed" mt={4}>{t('t2.packHint')}</Text>
                </div>
                <Select label={t('rt.signup.business')} allowDeselect={false} value={biz} onChange={(v) => v && setBiz(v)}
                  data={['generator', 'internet', 'satellite', 'water', ...BIZ_PACKS].map((b) => ({ value: b, label: t(`rt.signup.b.${b}`) }))}
                  description={BIZ_PACKS.includes(biz) ? t('rt.signup.demoHint') : undefined} />
                <SimpleGrid cols={2}>
                  <Select label={t('companies.base')} searchable data={currencies} value={f.base_currency} allowDeselect={false}
                    onChange={(v) => v && set('base_currency')(v)} />
                  <Select label={t('companies.local')} searchable data={currencies} value={f.local_currency} allowDeselect={false}
                    onChange={(v) => v && set('local_currency')(v)} />
                </SimpleGrid>
                <TextInput label={t('users.fullName')} required value={f.full_name} onChange={(e) => set('full_name')(e.currentTarget.value)} />
                <TextInput label={t('login.email')} type="email" dir="ltr" required autoComplete="username" value={f.email}
                  onChange={(e) => set('email')(e.currentTarget.value)} />
                <PasswordInput label={t('password.new')} dir="ltr" required minLength={10} autoComplete="new-password" value={f.password}
                  onChange={(e) => set('password')(e.currentTarget.value)} />
                <Button type="submit" size="md" loading={busy} fullWidth mt="xs">{t('signup.submit')}</Button>
                <Text size="sm" ta="center" c="dimmed">
                  {t('signup.haveAccount')} <Anchor href="#/login">{t('landing.signIn')}</Anchor>
                </Text>
              </Stack>
            </form>
          </Paper>
        </Stack>
      </Center>
    </Box>
  );
}
