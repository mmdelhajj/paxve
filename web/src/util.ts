import { notifications } from '@mantine/notifications';
import type { TFunction } from 'i18next';
import i18n, { langInfo } from './i18n';
import { ApiError, type Account } from './api';

export function errorText(t: TFunction, e: unknown): string {
  const code = e instanceof ApiError ? e.code : 'server_error';
  const detail = e instanceof ApiError ? e.detail ?? '' : '';
  return t(`errors.${code}`, { defaultValue: t('errors.server_error'), detail });
}

export function notifyError(t: TFunction, e: unknown) {
  notifications.show({ color: 'red', message: errorText(t, e), autoClose: 7000 });
}

export function notifySaved(t: TFunction) {
  notifications.show({ color: 'teal', message: t('common.saved') });
}

const intl = () => langInfo(i18n.language).intl;

export function formatDateTime(iso: string): string {
  return new Intl.DateTimeFormat(intl(), { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(iso));
}

export function formatDate(iso: string): string {
  const d = iso.length === 10 ? new Date(iso + 'T00:00:00') : new Date(iso);
  return new Intl.DateTimeFormat(intl(), { dateStyle: 'medium' }).format(d);
}

export function today(): string {
  const d = new Date();
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}

/** Display a decimal string with grouping. Display only; values stay strings everywhere else. */
export function formatAmount(v: string | number | null | undefined, decimals = 2): string {
  if (v === null || v === undefined || v === '') return '';
  const n = typeof v === 'number' ? v : Number(v);
  if (!Number.isFinite(n)) return String(v);
  return new Intl.NumberFormat(intl(), {
    minimumFractionDigits: decimals,
    maximumFractionDigits: decimals,
    numberingSystem: 'latn',
  }).format(n);
}

/** Exact decimal arithmetic for totals: values scaled to 4 decimals as BigInt. */
export function toUnits(v: string): bigint {
  const s = (v ?? '').trim();
  if (!/^\d+(\.\d{0,4})?$/.test(s)) return 0n;
  const [i, f = ''] = s.split('.');
  return BigInt(i) * 10000n + BigInt((f + '0000').slice(0, 4));
}

export function fromUnits(u: bigint): string {
  const neg = u < 0n;
  const a = neg ? -u : u;
  return `${neg ? '-' : ''}${a / 10000n}.${String(a % 10000n).padStart(4, '0')}`;
}

export function accountLabel(a: Pick<Account, 'code' | 'name' | 'name_i18n'>): string {
  const name = a.name_i18n?.[i18n.language] || a.name;
  return `${a.code} · ${name}`;
}

export function accountName(a: Pick<Account, 'name' | 'name_i18n'>): string {
  return a.name_i18n?.[i18n.language] || a.name;
}
