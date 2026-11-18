import type { ReactNode } from 'react';
import { Button, Group, Text, Title } from '@mantine/core';

export default function PageHeader({ title, subtitle, children }: { title: string; subtitle?: ReactNode; children?: ReactNode }) {
  return (
    <Group justify="space-between" align="flex-end" wrap="wrap" gap="sm">
      <div>
        <Title order={2}>{title}</Title>
        {subtitle && <Text c="dimmed" size="sm">{subtitle}</Text>}
      </div>
      {children && <Group gap="xs">{children}</Group>}
    </Group>
  );
}

/** Inline confirmation box (the app never uses browser confirm dialogs). */
export function Confirm({ text, onYes, onNo, yesLabel, noLabel, color = 'red', busy }: {
  text: string; onYes: () => void; onNo: () => void; yesLabel: string; noLabel: string; color?: string; busy?: boolean;
}) {
  return (
    <Group justify="space-between" wrap="wrap" p="sm"
      style={{ border: `1px solid var(--mantine-color-${color}-3)`, borderRadius: 'var(--mantine-radius-md)' }}>
      <Text size="sm" style={{ flex: 1, minWidth: 200 }}>{text}</Text>
      <Group gap="xs">
        <Button size="xs" variant="default" onClick={onNo}>{noLabel}</Button>
        <Button size="xs" color={color} onClick={onYes} loading={busy}>{yesLabel}</Button>
      </Group>
    </Group>
  );
}
