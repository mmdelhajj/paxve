// Offline support for collectors and tills: customers / catalog are cached on the device, payments and sales taken without
// internet are queued with a unique client_ref and sent when the connection comes back.
// The server stores a client_ref only once, so retries never create double payments.

import { api, ApiError } from './api';

export interface QueuedPayment {
  client_ref: string;
  company_id: string;
  customer_name: string;
  created_at: string;
  body: Record<string, unknown>;
  error?: string;
  /** API path to send to (default: the company's payments). POS sales use /pos/orders. */
  path?: string;
}

const KEY = 'paxve.outbox';

function read(): QueuedPayment[] {
  try {
    return JSON.parse(localStorage.getItem(KEY) || '[]') as QueuedPayment[];
  } catch {
    return [];
  }
}

function write(list: QueuedPayment[]) {
  try {
    localStorage.setItem(KEY, JSON.stringify(list));
  } catch {
    /* storage full or blocked */
  }
  window.dispatchEvent(new Event('paxve:outbox'));
}

export function outbox(): QueuedPayment[] {
  return read();
}

export function newClientRef(): string {
  const r = crypto.getRandomValues(new Uint8Array(12));
  return 'pxv-' + Array.from(r, (b) => b.toString(16).padStart(2, '0')).join('');
}

export function enqueue(p: QueuedPayment) {
  write([...read(), p]);
}

let syncing = false;

/** Sends queued payments. Returns how many were sent. */
export async function syncOutbox(): Promise<number> {
  if (syncing || !navigator.onLine) return 0;
  syncing = true;
  let sent = 0;
  try {
    for (const p of read()) {
      try {
        // payments carry the time the device took them, so a payment synced late is not read as a night payment
        const extra = p.path ? {} : { client_time: p.created_at };
        await api(p.path ?? `/companies/${p.company_id}/payments`, { method: 'POST', body: { ...extra, ...p.body, client_ref: p.client_ref } });
        write(read().filter((x) => x.client_ref !== p.client_ref));
        sent++;
      } catch (e) {
        if (e instanceof ApiError && e.status >= 400 && e.status < 500) {
          // rejected by the server (e.g. rate missing): keep it, show the error, the office can fix it
          write(read().map((x) => (x.client_ref === p.client_ref ? { ...x, error: e.code } : x)));
        } else {
          break; // network problem: try again later
        }
      }
    }
  } finally {
    syncing = false;
  }
  return sent;
}

export function cacheSet(key: string, value: unknown) {
  try {
    localStorage.setItem('paxve.cache.' + key, JSON.stringify({ at: Date.now(), value }));
  } catch {
    /* ignore */
  }
}

export function cacheGet<T>(key: string): { at: number; value: T } | null {
  try {
    const v = localStorage.getItem('paxve.cache.' + key);
    return v ? (JSON.parse(v) as { at: number; value: T }) : null;
  } catch {
    return null;
  }
}

/** audit P7-L4: on sign-out the phone forgets cached customers, methods, catalogs and queued scans (unsent payments
 *  and sales stay in the outbox: they are money already taken). */
export function clearDeviceCache() {
  try {
    const drop: string[] = [];
    for (let i = 0; i < localStorage.length; i++) {
      const k = localStorage.key(i);
      if (k && (k.startsWith('paxve.cache.') || k === 'paxve-wh-queue')) drop.push(k);
    }
    drop.forEach((k) => localStorage.removeItem(k));
  } catch {
    /* storage unavailable */
  }
}