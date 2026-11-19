import { Group, Text, ThemeIcon } from '@mantine/core';

export default function Logo({ size = 'lg' }: { size?: 'lg' | 'xl' }) {
  return (
    <Group gap={8} wrap="nowrap">
      <ThemeIcon size={size === 'xl' ? 40 : 30} radius="md" color="blue.8">
        <svg viewBox="0 0 64 64" width="70%" height="70%" aria-hidden>
          <path d="M22 48V16h12a10 10 0 0 1 0 20H22" fill="none" stroke="currentColor" strokeWidth="7"
            strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      </ThemeIcon>
      <Text fw={600} size={size === 'xl' ? '1.6rem' : 'lg'} style={{ letterSpacing: '-0.01em' }}>
        Paxve
      </Text>
    </Group>
  );
}
