import i18n from './i18n';

export type Role = 'admin' | 'accountant' | 'data_entry' | 'read_only' | 'collector' | 'cashier';

export interface Me {
  id: string;
  tenant_id: string;
  tenant_name: string;
  email: string;
  full_name: string;
  role: Role;
  locale: string;
  plan: string;
  trial_ends_at: string | null;
  is_platform_admin: boolean;
  platform_mfa_deadline?: string; // platform owner without two-step sign-in: must turn it on before this
  /** the organisation's Paxve plan: trial, active, grace (ended, still usable), expired (read-only), blocked */
  sub_state?: 'trial' | 'active' | 'grace' | 'expired' | 'blocked';
  paid_until?: string | null;
  grace_ends?: string | null;
  /** apps the Paxve plan allows besides the core apps (null = every app; kept apps included) */
  plan_apps?: string[] | null;
  /** a platform owner is looking at this organisation (read-only support view) */
  support?: { by: string; by_name: string; since: string } | null;
  role_id: string | null;
  role_name: string;
  permissions: string[];
  company_ids: string[] | null;
  mfa_enabled: boolean;
  mfa_required: boolean;
  mfa_setup_required: boolean;
}

/** The signed-in user holds this permission ("module.action"). The server checks it again on every call. */
export function can(me: Pick<Me, 'permissions'> | null | undefined, perm: string): boolean {
  return !!me && me.permissions.includes(perm);
}

/** The organisation's Paxve plan includes the app (core apps always; no list = every app). */
export function planHas(me: Pick<Me, 'plan_apps'> | null | undefined, app: string): boolean {
  return !me?.plan_apps || ['gen', 'acc', 'col'].includes(app) || me.plan_apps.includes(app);
}

/** Any of the permissions. */
export function canAny(me: Pick<Me, 'permissions'> | null | undefined, ...perms: string[]): boolean {
  return perms.some((p) => can(me, p));
}

export interface Company {
  id: string;
  name_en: string;
  name_ar: string;
  base_currency: string;
  local_currency: string;
  is_active: boolean;
  created_at: string;
  lock_date: string | null;
  apps?: string[];                 // enabled apps (see ribbon.ts)
  business_type?: 'generator' | 'internet' | 'satellite' | 'water' | 'supermarket' | 'clothing' | 'electronics' | 'furniture' | 'wholesale' | 'restaurant'
    | 'pharmacy' | 'factory';
  // localization (country pack)
  country?: string; l10n?: string; einvoice?: '' | 'zatca' | 'eta'; default_locale?: string; fiscal_year_start?: string;
  tax_id_label?: string; invoice_title?: string; legal_mentions?: string;
  timezone?: string; // "today" and report dates follow this zone
  display?: string;  // client-side: the name in the reader's language (set by the company context)
}

export interface User {
  id: string;
  email: string;
  full_name: string;
  role: Role;
  locale: string;
  is_active: boolean;
  created_at: string;
  role_id: string | null;
  role_name: string | null;
  company_ids: string[] | null;
  mfa_enabled: boolean;
  last_seen_at: string | null;
}

export interface CustomRole {
  id: string;
  name: string;
  base: Role;
  permissions: string[];
  description: string;
  is_active: boolean;
  created_at: string;
  users: number;
}

export interface AuditEntry {
  id: number;
  at: string;
  action: string;
  entity: string;
  entity_id: string | null;
  actor: string | null;
  before: Record<string, unknown> | null;
  after: Record<string, unknown> | null;
  ip: string | null;
  company_id?: string | null;
  approved_by?: string | null;
  approval_id?: string | null;
}

export interface Currency {
  code: string;
  name: string;
  symbol: string;
  decimals: number;
  name_ar?: string;
  name_fr?: string;
}

/** a currency's name in the reader's language (English for languages without their own names) */
export function currencyName(c: Currency, lang: string): string {
  if (lang === 'ar' && c.name_ar) return c.name_ar;
  if (lang === 'fr' && c.name_fr) return c.name_fr;
  return c.name;
}

export interface Rate {
  id: string;
  currency: string;
  date: string;
  kind: 'official' | 'buy' | 'sell';
  rate: string;
  updated_at: string;
  source?: 'manual' | 'auto';
}

export type AccountType = 'asset' | 'liability' | 'equity' | 'income' | 'expense';

export interface Account {
  id: string;
  code: string;
  name: string;
  name_i18n: Record<string, string>;
  type: AccountType;
  parent_id: string | null;
  is_postable: boolean;
  currency: string | null;
  is_active: boolean;
}

export interface EntrySummary {
  id: string;
  number: number | null;
  date: string;
  memo: string;
  reference: string;
  status: 'draft' | 'posted';
  source: string;
  reversal_of: string | null;
  reversed_by: string | null;
  total: string;
  created_at: string;
}

export interface EntryLine {
  id: string;
  line_no: number;
  account_id: string;
  account_code: string;
  account_name: string;
  description: string;
  currency: string;
  debit: string;
  credit: string;
  rate: string;
  base_debit: string;
  base_credit: string;
  cost_center_id?: string | null;
  cost_center_name?: string | null;
}

export interface Entry extends EntrySummary {
  base_currency: string;
  created_by: string | null;
  posted_by: string | null;
  posted_at: string | null;
  lines: EntryLine[];
}

export interface APIKey {
  id: string;
  name: string;
  prefix: string;
  role: Role;
  created_by: string;
  created_at: string;
  last_used_at: string | null;
  revoked_at: string | null;
  scopes: string[] | null;
}

export class ApiError extends Error {
  /** set once a screen has shown this error, so the global safety net does not show it twice */
  shown = false;
  constructor(public status: number, public code: string, public detail?: string, public method = 'GET') {
    super(code);
  }
}

export const UNAUTHORIZED_EVENT = 'paxve:unauthorized';
/** Fired when an action was stored for approval instead of being done (HTTP 202 approval_required). */
export const APPROVAL_EVENT = 'paxve:approval';
export const API_BASE = '/api/v1';

interface CreditDetail {
  policy: 'warn' | 'block'; reasons: string[]; limit: string | null; exposure: string; amount: string; after: string;
  base_currency: string; overdue_days: number | null; max_overdue_days: number; can_override: boolean;
}

/** A sale refused by the customer's credit rules (409 credit_limit): explain, then confirm (warn), override (allowed
 *  users) or send it for approval, by calling again with the matching header. Returns the headers to retry with. */
function creditDecision(detail: string | undefined): Record<string, string> | null {
  let d: CreditDetail;
  try { d = JSON.parse(detail ?? '') as CreditDetail; } catch { return null; }
  const t = i18n.t.bind(i18n);
  const why = d.reasons.map((r) => t(`gp.credit.r.${r}`, { limit: d.limit ?? '—', cur: d.base_currency, days: d.overdue_days ?? 0, max: d.max_overdue_days })).join('\n');
  const head = t('gp.credit.head', { exposure: d.exposure, after: d.after, cur: d.base_currency });
  if (d.policy === 'warn') return window.confirm(`${head}\n${why}\n\n${t('gp.credit.confirm')}`) ? { 'X-Credit-Confirm': '1' } : null;
  if (d.can_override) return window.confirm(`${head}\n${why}\n\n${t('gp.credit.override')}`) ? { 'X-Credit-Override': '1' } : null;
  return window.confirm(`${head}\n${why}\n\n${t('gp.credit.ask')}`) ? { 'X-Credit-Request-Approval': '1' } : null;
}

export async function api<T>(path: string, opts: { method?: string; body?: unknown; headers?: Record<string, string> } = {}): Promise<T> {
  const res = await fetch(API_BASE + path, {
    method: opts.method ?? 'GET',
    headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'paxve', ...(opts.headers ?? {}) },
    body: opts.body === undefined ? undefined : JSON.stringify(opts.body),
    credentials: 'same-origin',
  });
  const text = await res.text();
  let data: unknown = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    /* not JSON */
  }
  if (!res.ok) {
    const err = (data as { error?: string; detail?: string } | null) ?? {};
    if (res.status === 409 && err.error === 'credit_limit' && !opts.headers) {
      const retry = creditDecision(err.detail);
      if (retry) return api<T>(path, { ...opts, headers: retry });
    }
    if (res.status === 401 && path !== '/me' && path !== '/auth/login') {
      window.dispatchEvent(new Event(UNAUTHORIZED_EVENT));
    }
    throw new ApiError(res.status, err.error ?? 'server_error', err.detail, opts.method ?? 'GET');
  }
  if (res.status === 202 && (data as { approval_required?: boolean } | null)?.approval_required) {
    // not done yet: the caller must not treat it as success
    window.dispatchEvent(new Event(APPROVAL_EVENT));
    throw new ApiError(202, 'approval_required');
  }
  return data as T;
}
