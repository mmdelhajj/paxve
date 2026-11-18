export type Role = 'admin' | 'accountant' | 'data_entry' | 'read_only';
export type Locale = 'en' | 'ar';

export interface Me {
  id: string;
  tenant_id: string;
  tenant_name: string;
  email: string;
  full_name: string;
  role: Role;
  locale: Locale;
}

export interface Company {
  id: string;
  name_en: string;
  name_ar: string;
  base_currency: string;
  local_currency: string;
  is_active: boolean;
  created_at: string;
}

export interface User {
  id: string;
  email: string;
  full_name: string;
  role: Role;
  locale: Locale;
  is_active: boolean;
  created_at: string;
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
}

export class ApiError extends Error {
  constructor(public status: number, public code: string) {
    super(code);
  }
}

export const UNAUTHORIZED_EVENT = 'paxve:unauthorized';

export async function api<T>(path: string, opts: { method?: string; body?: unknown } = {}): Promise<T> {
  const res = await fetch('/api' + path, {
    method: opts.method ?? 'GET',
    headers: { 'Content-Type': 'application/json' },
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
    const code = (data as { error?: string } | null)?.error ?? 'server_error';
    if (res.status === 401 && path !== '/me' && path !== '/auth/login') {
      window.dispatchEvent(new Event(UNAUTHORIZED_EVENT));
    }
    throw new ApiError(res.status, code);
  }
  return data as T;
}
