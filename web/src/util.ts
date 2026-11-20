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
  if (e instanceof ApiError && e.code === 'approval_required') {
    // not an error: the action waits for someone who may approve it
    notifications.show({ color: 'blue', message: t('sec.sentForApproval'), autoClose: 7000 });
    return;
  }
  notifications.show({ color: 'red', message: errorText(t, e), autoClose: 7000 });
}

export function notifySaved(t: TFunction) {
  notifications.show({ color: 'teal', message: t('common.saved') });
}

const intl = () => langInfo(i18n.language).intl;

export function formatDateTime(iso: string): string {
  return new Intl.DateTimeFormat(intl(), { dateStyle: 'medium', timeStyle: 'short', numberingSystem: 'latn' }).format(new Date(iso));
}

export function formatDate(iso: string): string {
  const d = iso.length === 10 ? new Date(iso + 'T00:00:00') : new Date(iso);
  return new Intl.DateTimeFormat(intl(), { dateStyle: 'medium', numberingSystem: 'latn' }).format(d);
}

// "today" follows the company's time zone, not the device's (audit B-M2): set by the company context.
let companyTZ: string | undefined;
export function setCompanyTZ(tz: string | undefined) {
  companyTZ = tz || undefined;
}

/** YYYY-MM-DD of a date (default: now) in the company's time zone. */
export function today(d: Date = new Date()): string {
  if (companyTZ) {
    try {
      // en-CA formats as YYYY-MM-DD
      return new Intl.DateTimeFormat('en-CA', { timeZone: companyTZ, year: 'numeric', month: '2-digit', day: '2-digit' }).format(d);
    } catch {
      /* unknown zone in this browser: fall back to the device */
    }
  }
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}

/** The company's name in the reader's language (Arabic name when reading Arabic and it is set). */
export function companyName(c: { name_en: string; name_ar?: string | null }, lang: string): string {
  return (lang.startsWith('ar') || lang === 'fa' || lang === 'ur' || lang === 'ckb') && c.name_ar ? c.name_ar : c.name_en;
}

/** Display a decimal string with grouping. Display only; values stay strings everywhere else. */
export function formatAmount(v: string | number | null | undefined, decimals = 2): string {
  if (v === null || v === undefined || v === '') return '';
  const n = typeof v === 'number' ? v : Number(v);
  if (!Number.isFinite(n)) return String(v);
  // Money always uses Latin digits with '.' decimals and ',' grouping (how USD/LBP amounts are written in Lebanon),
  // except French, which uses its own separators.
  return new Intl.NumberFormat(intl().startsWith('fr') ? 'fr-FR' : 'en-US', {
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

/** Change preview, same rule as the server: round down to the currency decimals, then down to the box's step (e.g. 1000 LBP). */
export function roundChange(value: number, decimals: number, step?: string | null): number {
  let v = Math.floor(value * 10 ** decimals + 1e-9) / 10 ** decimals;
  const s = Number(step || 0);
  if (s > 0) v = Math.floor(v / s + 1e-9) * s;
  return v;
}
/** Default "amount to apply" for a payment: everything received, capped at what is due
 *  (rounded down to 4 decimals so it never exceeds the money received). Empty when nothing is due or received. */
export function autoApply(received: number, due: number): string {
  const r = Math.floor(received * 10000) / 10000;
  if (r > 0) return String(due > 0 ? Math.min(r, due) : r);
  return due > 0 ? String(due) : '';
}