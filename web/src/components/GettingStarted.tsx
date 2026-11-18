import { useEffect, useState } from 'react';
import { ActionIcon, Anchor, Button, Group, Paper, Progress, SimpleGrid, Text, ThemeIcon, Tooltip } from '@mantine/core';
import { IconCheck, IconCircleDashed, IconSparkles, IconX } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { api, ApiError, type Me } from '../api';
import { useCompany } from '../company';
import { notifyError } from '../util';

interface Steps { chart: boolean; rate: boolean; plans: boolean; customers: boolean; collector: boolean; messaging: boolean; billing: boolean; plan: string }

const steps: { key: keyof Omit<Steps, 'plan'>; href: string }[] = [
  { key: 'chart', href: '#/accounts' },
  { key: 'rate', href: '#/rates' },
  { key: 'plans', href: '#/billing-setup' },
  { key: 'customers', href: '#/customers' },
  { key: 'collector', href: '#/users' },
  { key: 'messaging', href: '#/messages' },
  { key: 'billing', href: '#/invoices' },
];

function dismissedKey(cid: string) {
  return `paxve.onboard.dismissed.${cid}`;
}

/** "Getting started" checklist, detected from the company's data; dismissible per company. */
export default function GettingStarted({ me }: { me: Me }) {
  const { t } = useTranslation();
  const { company, reload, setCompanyId } = useCompany();
  const [s, setS] = useState<Steps | null>(null);
  const [hidden, setHidden] = useState(false);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!company) return;
    try {
      setHidden(localStorage.getItem(dismissedKey(company.id)) === '1');
    } catch {
      setHidden(false);
    }
    api<Steps>(`/companies/${company.id}/onboarding`).then(setS).catch(() => setS(null));
  }, [company]);

  if (!company || !s || hidden) return null;
  const done = steps.filter((x) => s[x.key]).length;
  const isAdmin = me.role === 'admin';
  if (done === steps.length && s.plan !== 'trial') return null;

  async function loadDemo() {
    setBusy(true);
    try {
      const r = await api<{ company_id: string }>('/demo-data', { method: 'POST', body: {} });
      reload();
      setCompanyId(r.company_id);
    } catch (e) {
      if (e instanceof ApiError && e.code === 'duplicate') notifyError(t, new ApiError(409, 'demo_exists'));
      else notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Paper withBorder p="md" radius="md" style={{ borderColor: 'var(--mantine-color-teal-3)' }}>
      <Group justify="space-between" align="flex-start" wrap="nowrap" mb="xs">
        <div>
          <Text fw={700}>{t('onboard.title')}</Text>
          <Text size="sm" c="dimmed">{t('onboard.progress', { done, total: steps.length })}</Text>
        </div>
        <Tooltip label={t('onboard.hide')}>
          <ActionIcon variant="subtle" color="gray" aria-label={t('onboard.hide')} onClick={() => {
            try { localStorage.setItem(dismissedKey(company.id), '1'); } catch { /* ignore */ }
            setHidden(true);
          }}><IconX size={16} /></ActionIcon>
        </Tooltip>
      </Group>
      <Progress value={(done / steps.length) * 100} color="teal" mb="sm" />
      <SimpleGrid cols={{ base: 1, sm: 2, md: 4 }} spacing="xs">
        {steps.map((x) => (
          <Group key={x.key} gap={8} wrap="nowrap">
            <ThemeIcon size={22} radius="xl" color={s[x.key] ? 'teal' : 'gray'} variant={s[x.key] ? 'filled' : 'light'}>
              {s[x.key] ? <IconCheck size={14} /> : <IconCircleDashed size={14} />}
            </ThemeIcon>
            {s[x.key] ? <Text size="sm" c="dimmed" td="line-through">{t(`onboard.steps.${x.key}`)}</Text>
              : <Anchor size="sm" href={x.href}>{t(`onboard.steps.${x.key}`)}</Anchor>}
          </Group>
        ))}
      </SimpleGrid>
      {isAdmin && s.plan === 'trial' && (
        <Group mt="md" gap="xs">
          <Button size="xs" variant="light" leftSection={<IconSparkles size={14} />} loading={busy} onClick={loadDemo}>{t('onboard.demo')}</Button>
          <Text size="xs" c="dimmed">{t('onboard.demoHint')}</Text>
        </Group>
      )}
    </Paper>
  );
}
