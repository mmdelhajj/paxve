import { notifications } from '@mantine/notifications';
import type { TFunction } from 'i18next';
import i18n from './i18n';
import { ApiError } from './api';

export function errorText(t: TFunction, e: unknown): string {
  const code = e instanceof ApiError ? e.code : 'server_error';
  return t(`errors.${code}`, { defaultValue: t('errors.server_error') });
}

export function notifyError(t: TFunction, e: unknown) {
  notifications.show({ color: 'red', message: errorText(t, e) });
}

export function notifySaved(t: TFunction) {
  notifications.show({ color: 'teal', message: t('common.saved') });
}

export function formatDateTime(iso: string): string {
  const locale = i18n.language === 'ar' ? 'ar-LB' : 'en-GB';
  return new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(iso));
}

export function formatDate(iso: string): string {
  const locale = i18n.language === 'ar' ? 'ar-LB' : 'en-GB';
  return new Intl.DateTimeFormat(locale, { dateStyle: 'medium' }).format(new Date(iso));
}
