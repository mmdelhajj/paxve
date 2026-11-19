export type Role = 'admin' | 'accountant' | 'data_entry' | 'read_only' | 'collector' | 'cashier';

export interface Me {
  id: string;
  tenant_id: string;
  tenant_name: string;
  email: string;
  full_name: string;
  role: Role;
  locale: string;
  plan: 'trial' | 'starter' | 'business' | 'pro';
  trial_ends_at: string | null;
  is_platform_admin: boolean;
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
  business_type?: 'generator' | 'internet' | 'satellite';
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
}

export interface Rate {
  id: string;
  currency: string;
  date: string;
  kind: 'official' | 'buy' | 'sell';
  rate: string;
  updated_at: string;
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
  constructor(public status: number, public code: string, public detail?: string) {
    super(code);
  }
}

export const UNAUTHORIZED_EVENT = 'paxve:unauthorized';
/** Fired when an action was stored for approval instead of being done (HTTP 202 approval_required). */
export const APPROVAL_EVENT = 'paxve:approval';
export const API_BASE = '/api/v1';

export async function api<T>(path: string, opts: { method?: string; body?: unknown } = {}): Promise<T> {
  const res = await fetch(API_BASE + path, {
    method: opts.method ?? 'GET',
    headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'paxve' },
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
    if (res.status === 401 && path !== '/me' && path !== '/auth/login') {
      window.dispatchEvent(new Event(UNAUTHORIZED_EVENT));
    }
    throw new ApiError(res.status, err.error ?? 'server_error', err.detail);
  }
  if (res.status === 202 && (data as { approval_required?: boolean } | null)?.approval_required) {
    // not done yet: the caller must not treat it as success
    window.dispatchEvent(new Event(APPROVAL_EVENT));
    throw new ApiError(202, 'approval_required');
  }
  return data as T;
}
