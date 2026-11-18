import { Alert, Anchor } from '@mantine/core';
import { IconClockHour4 } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import type { Me } from '../api';

export default function TrialBanner({ me }: { me: Me }) {
  const { t } = useTranslation();
  if (me.plan !== 'trial' || !me.trial_ends_at) return null;
  const days = Math.ceil((new Date(me.trial_ends_at).getTime() - Date.now()) / 86400000);
  const expired = days <= 0;
  return (
    <Alert mb="md" variant="light" color={expired ? 'red' : days <= 3 ? 'orange' : 'teal'} icon={<IconClockHour4 size={18} />}>
      {expired ? t('trial.expired') : t('trial.daysLeft', { count: days })}{' '}
      <Anchor href="mailto:sales@paxve.com" size="sm">{t('trial.upgrade')}</Anchor>
    </Alert>
  );
}
