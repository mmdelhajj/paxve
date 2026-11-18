import { useEffect, useState } from 'react';
import { Badge, List, Paper, SimpleGrid, Stack, Text, ThemeIcon, Title } from '@mantine/core';
import { IconClockHour4 } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { api, type Company, type Me, type User } from '../api';

function Stat({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <Paper withBorder p="md">
      <Text size="xs" c="dimmed" tt="uppercase" fw={600}>
        {label}
      </Text>
      <Text size="1.75rem" fw={600} mt={4}>
        {value}
      </Text>
    </Paper>
  );
}

export default function Dashboard({ me }: { me: Me }) {
  const { t } = useTranslation();
  const [companies, setCompanies] = useState<number | null>(null);
  const [users, setUsers] = useState<number | null>(null);

  useEffect(() => {
    api<Company[]>('/companies').then((c) => setCompanies(c.filter((x) => x.is_active).length)).catch(() => {});
    if (me.role === 'admin') api<User[]>('/users').then((u) => setUsers(u.filter((x) => x.is_active).length)).catch(() => {});
  }, [me.role]);

  return (
    <Stack gap="lg" maw={1100}>
      <div>
        <Title order={2}>{t('dashboard.welcome', { name: me.full_name })}</Title>
        <Text c="dimmed">{me.tenant_name}</Text>
      </div>
      <SimpleGrid cols={{ base: 1, xs: 3 }}>
        <Stat label={t('dashboard.companies')} value={companies ?? '–'} />
        {me.role === 'admin' && <Stat label={t('dashboard.users')} value={users ?? '–'} />}
        <Stat label={t('dashboard.role')} value={<Badge size="lg" variant="light">{t(`roles.${me.role}`)}</Badge>} />
      </SimpleGrid>
      <Paper withBorder p="lg">
        <Title order={4} mb="sm">
          {t('dashboard.nextTitle')}
        </Title>
        <List
          spacing="xs"
          icon={
            <ThemeIcon size={22} radius="xl" variant="light">
              <IconClockHour4 size={14} />
            </ThemeIcon>
          }
        >
          {['next1', 'next2', 'next3', 'next4', 'next5'].map((k) => (
            <List.Item key={k}>{t(`dashboard.${k}`)}</List.Item>
          ))}
        </List>
      </Paper>
    </Stack>
  );
}
