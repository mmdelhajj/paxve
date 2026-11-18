import type { ReactNode } from 'react';
import { Button, Group, Skeleton, Stack, Text, ThemeIcon } from '@mantine/core';
import { IconInbox } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';

/** Placeholder rows shown while a list loads (keeps the layout stable instead of a spinner). */
export function ListSkeleton({ rows = 6 }: { rows?: number }) {
  return (
    <Stack gap={10} p="md" aria-busy="true">
      {Array.from({ length: rows }, (_, i) => (
        <Skeleton key={i} height={18} width={`${92 - ((i * 13) % 30)}%`} radius="sm" />
      ))}
    </Stack>
  );
}

/** Friendly empty state with an optional hint and action. */
export function EmptyState({ title, hint, action }: { title?: string; hint?: string; action?: ReactNode }) {
  const { t } = useTranslation();
  return (
    <Stack align="center" gap={6} p="xl">
      <ThemeIcon size={44} radius="xl" variant="light" color="gray"><IconInbox size={22} /></ThemeIcon>
      <Text fw={500}>{title ?? t('common.empty')}</Text>
      {hint && <Text size="sm" c="dimmed" ta="center" maw={420}>{hint}</Text>}
      {action}
    </Stack>
  );
}

/** Shown under a list that hit its row limit. */
export function ShowMore({ onClick }: { onClick: () => void }) {
  const { t } = useTranslation();
  return (
    <Group justify="center" p="sm"><Button variant="subtle" size="xs" onClick={onClick}>{t('common.showMore')}</Button></Group>
  );
}
