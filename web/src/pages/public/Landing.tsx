import {
  Badge, Box, Button, Card, Container, Divider, Group, List, Paper, SimpleGrid, Stack, Text, ThemeIcon, Title,
} from '@mantine/core';
import {
  IconApi, IconArrowsExchange, IconBluetooth, IconBook2, IconBrandAndroid, IconCheck, IconGauge, IconLanguage, IconShieldLock,
} from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import LangSwitch from '../../components/LangSwitch';
import Logo from '../../components/Logo';
import { isNativeApp } from '../../native';

const features = [
  { key: 'fx', icon: IconArrowsExchange },
  { key: 'collect', icon: IconBluetooth },
  { key: 'meter', icon: IconGauge },
  { key: 'ledger', icon: IconBook2 },
  { key: 'api', icon: IconApi },
  { key: 'lang', icon: IconLanguage },
] as const;

const plans = [
  { key: 'starter', price: 25, highlight: false },
  { key: 'business', price: 59, highlight: true },
  { key: 'pro', price: 129, highlight: false },
] as const;

export default function Landing() {
  const { t } = useTranslation();
  const inApp = isNativeApp();
  const rtl = document.dir === 'rtl';

  return (
    <Box bg="var(--mantine-color-body)">
      <Paper component="header" radius={0} shadow="xs" px="md" py="sm" style={{ position: 'sticky', top: 0, zIndex: 10 }}>
        <Container size="lg" px={0}>
          <Group justify="space-between" wrap="nowrap">
            <Logo />
            <Group gap="xs" wrap="nowrap">
              <LangSwitch />
              <Button variant="subtle" component="a" href="#/login" visibleFrom="xs">{t('landing.signIn')}</Button>
              <Button component="a" href="#/signup">{t('landing.startTrial')}</Button>
            </Group>
          </Group>
        </Container>
      </Paper>

      {/* hero */}
      <Box py={{ base: 48, md: 88 }} style={{ background: 'linear-gradient(180deg, var(--mantine-color-teal-0), var(--mantine-color-body))' }}>
        <Container size="lg">
          <SimpleGrid cols={{ base: 1, md: 2 }} spacing={48} style={{ alignItems: 'center' }}>
            <Stack gap="lg">
              <Badge variant="light" size="lg" style={{ alignSelf: 'flex-start' }}>{t('landing.badge')}</Badge>
              <Title order={1} fz={{ base: 34, md: 48 }} lh={1.15}>{t('landing.title')}</Title>
              <Text size="lg" c="dimmed">{t('landing.subtitle')}</Text>
              <Group>
                <Button size="lg" component="a" href="#/signup">{t('landing.startTrial')}</Button>
                <Button size="lg" variant="default" component="a" href="#/login">{t('landing.signIn')}</Button>
              </Group>
              <Text size="sm" c="dimmed">{t('landing.trialNote')}</Text>
              {!inApp && <Button variant="subtle" color="gray" size="compact-sm" component="a" href="/download/paxve.apk"
                leftSection={<IconBrandAndroid size={16} />} style={{ alignSelf: 'flex-start' }}>{t('landing.downloadApp')}</Button>}
            </Stack>
            <ReceiptMock rtl={rtl} />
          </SimpleGrid>
        </Container>
      </Box>

      {/* features */}
      <Container size="lg" py={64}>
        <Title order={2} ta="center" mb={8}>{t('landing.featuresTitle')}</Title>
        <Text c="dimmed" ta="center" mb={40} maw={640} mx="auto">{t('landing.featuresSubtitle')}</Text>
        <SimpleGrid cols={{ base: 1, sm: 2, md: 3 }} spacing="lg">
          {features.map((f) => (
            <Card key={f.key} withBorder padding="lg">
              <ThemeIcon size={44} radius="md" variant="light" mb="md"><f.icon size={24} /></ThemeIcon>
              <Text fw={600} mb={6}>{t(`landing.features.${f.key}.title`)}</Text>
              <Text size="sm" c="dimmed">{t(`landing.features.${f.key}.text`)}</Text>
            </Card>
          ))}
        </SimpleGrid>
      </Container>

      {/* trust */}
      <Box bg="var(--mantine-color-gray-0)" py={56}>
        <Container size="lg">
          <SimpleGrid cols={{ base: 1, md: 3 }} spacing="xl">
            {(['rules', 'audit', 'tenant'] as const).map((k) => (
              <Group key={k} align="flex-start" wrap="nowrap">
                <ThemeIcon variant="light" color="teal" size={36} radius="xl"><IconShieldLock size={20} /></ThemeIcon>
                <div>
                  <Text fw={600}>{t(`landing.trust.${k}.title`)}</Text>
                  <Text size="sm" c="dimmed">{t(`landing.trust.${k}.text`)}</Text>
                </div>
              </Group>
            ))}
          </SimpleGrid>
        </Container>
      </Box>

      {/* pricing */}
      <Container size="lg" py={64} id="pricing">
        <Title order={2} ta="center" mb={8}>{t('landing.pricingTitle')}</Title>
        <Text c="dimmed" ta="center" mb={40}>{t('landing.pricingSubtitle')}</Text>
        <SimpleGrid cols={{ base: 1, md: 3 }} spacing="lg">
          {plans.map((p) => (
            <Card key={p.key} withBorder padding="xl" shadow={p.highlight ? 'md' : undefined}
              style={p.highlight ? { borderColor: 'var(--mantine-color-teal-6)', borderWidth: 2 } : undefined}>
              <Group justify="space-between" mb="xs">
                <Text fw={700} size="lg">{t(`landing.plans.${p.key}.name`)}</Text>
                {p.highlight && <Badge>{t('landing.popular')}</Badge>}
              </Group>
              <Group gap={4} align="baseline" mb="xs">
                <Text fz={40} fw={800} dir="ltr">${p.price}</Text>
                <Text c="dimmed">/ {t('landing.month')}</Text>
              </Group>
              <Text size="sm" c="dimmed" mb="md">{t(`landing.plans.${p.key}.limit`)}</Text>
              <List spacing={6} size="sm" mb="xl" icon={<ThemeIcon size={18} radius="xl" variant="light"><IconCheck size={12} /></ThemeIcon>}>
                {(t(`landing.plans.${p.key}.items`, { returnObjects: true }) as unknown as string[]).map((it) => <List.Item key={it}>{it}</List.Item>)}
              </List>
              <Button fullWidth variant={p.highlight ? 'filled' : 'light'} component="a" href="#/signup">{t('landing.startTrial')}</Button>
            </Card>
          ))}
        </SimpleGrid>
        <Text ta="center" size="sm" c="dimmed" mt="lg">{t('landing.pricingNote')}</Text>
      </Container>

      {/* final call */}
      <Box py={56} bg="teal.7">
        <Container size="md">
          <Stack align="center" gap="md">
            <Title order={2} c="white" ta="center">{t('landing.ctaTitle')}</Title>
            <Text c="teal.0" ta="center">{t('landing.trialNote')}</Text>
            <Button size="lg" variant="white" component="a" href="#/signup">{t('landing.startTrial')}</Button>
          </Stack>
        </Container>
      </Box>

      <Container size="lg" py="lg">
        <Group justify="space-between">
          <Text size="sm" c="dimmed">© {new Date().getFullYear()} Paxve</Text>
          <Group gap="md">
            <Text size="sm" component="a" href="#pricing" c="dimmed">{t('landing.pricingTitle')}</Text>
            {!inApp && <Text size="sm" component="a" href="/download/paxve.apk" c="dimmed">{t('landing.downloadApp')}</Text>}
            <Text size="sm" component="a" href="/api/v1/openapi.yaml" c="dimmed">API</Text>
            <Text size="sm" component="a" href="#/login" c="dimmed">{t('landing.signIn')}</Text>
          </Group>
        </Group>
      </Container>
    </Box>
  );
}

/** A small receipt illustration: paid in USD + LBP, change in LBP. */
function ReceiptMock({ rtl }: { rtl: boolean }) {
  const { t } = useTranslation();
  const row = (a: string, b: string, bold = false) => (
    <Group justify="space-between" wrap="nowrap">
      <Text size="sm" fw={bold ? 700 : 400}>{a}</Text>
      <Text size="sm" fw={bold ? 700 : 400} ff="monospace" dir="ltr">{b}</Text>
    </Group>
  );
  return (
    <Paper shadow="xl" radius="lg" p="lg" maw={380} mx="auto" w="100%" withBorder dir={rtl ? 'rtl' : 'ltr'}
      style={{ transform: 'rotate(-1.5deg)' }}>
      <Text ta="center" fw={700}>{t('landing.mock.company')}</Text>
      <Text ta="center" size="xs" c="dimmed">{t('payments.receipt')} #1042</Text>
      <Divider my="sm" variant="dashed" />
      <Text size="sm" fw={600} mb={4}>{t('landing.mock.customer')}</Text>
      {row(t('landing.mock.bill'), '45.00 USD')}
      <Divider my="xs" variant="dashed" />
      {row('Cash USD', '20.00 USD')}
      {row('Cash LBP', '2,500,000 LBP')}
      {row(t('payments.change'), '-262,503 LBP')}
      <Divider my="xs" variant="dashed" />
      {row(t('payments.amount'), '45.00 USD', true)}
      <Group justify="center" mt="md"><Badge color="teal" variant="light" leftSection={<IconBluetooth size={12} />}>{t('landing.mock.printed')}</Badge></Group>
    </Paper>
  );
}
