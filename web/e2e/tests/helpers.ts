import { readFileSync, writeFileSync, existsSync, mkdirSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import type { APIRequestContext, Page } from '@playwright/test';

const here = dirname(fileURLToPath(import.meta.url));
export const SCREENS = join(here, '..', 'screens');
const STATE = join(here, '..', '.state.json');

export interface State { email: string; password: string; collectorEmail?: string; collectorPassword?: string; companyId?: string }
export const saveState = (s: State) => writeFileSync(STATE, JSON.stringify(s));
export const loadState = (): State => JSON.parse(readFileSync(STATE, 'utf8'));

/** All translation keys (to detect raw keys like "customers.title" leaking into the UI). */
export async function translationKeys(): Promise<Set<string>> {
  const src = readFileSync(join(here, '..', '..', 'src', 'locales', 'en.ts'), 'utf8')
    .replace(/^import .*$/gm, '').replace(/^export type .*$/gm, '');
  const mod = await import('data:text/javascript;base64,' + Buffer.from(src).toString('base64'));
  const keys = new Set<string>();
  const walk = (o: Record<string, unknown>, p: string) => {
    for (const [k, v] of Object.entries(o)) {
      const key = p ? `${p}.${k}` : k;
      if (v && typeof v === 'object') walk(v as Record<string, unknown>, key);
      else keys.add(key);
    }
  };
  walk(mod.default, '');
  return keys;
}

export interface Problems { console: string[]; network: string[] }

/** Collects console errors and failed requests on a page. */
export function watch(page: Page): Problems {
  const p: Problems = { console: [], network: [] };
  page.on('console', (m) => {
    if (m.type() !== 'error') return;
    const t = m.text();
    if (/status of 401/.test(t)) return; // expected for /me when logged out
    p.console.push(t.slice(0, 300));
  });
  page.on('pageerror', (e) => p.console.push('pageerror: ' + String(e).slice(0, 300)));
  page.on('response', (r) => {
    if (r.status() >= 500) p.network.push(`${r.status()} ${r.request().method()} ${r.url()}`);
  });
  page.on('requestfailed', (r) => {
    const f = r.failure()?.errorText ?? '';
    if (/ERR_ABORTED|NS_BINDING_ABORTED|ERR_INTERNET_DISCONNECTED/.test(f)) return;
    p.network.push(`failed ${r.method()} ${r.url()} ${f}`);
  });
  return p;
}

export async function shot(page: Page, name: string) {
  if (!existsSync(SCREENS)) mkdirSync(SCREENS, { recursive: true });
  await page.screenshot({ path: join(SCREENS, name + '.png'), fullPage: false });
}

/** JSON call to the API using the page's session cookie. */
export async function apiCall<T = unknown>(req: APIRequestContext, method: string, path: string, body?: unknown): Promise<T> {
  const r = await req.fetch('/api/v1' + path, {
    method,
    headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'paxve' },
    data: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await r.text();
  if (!r.ok()) throw new Error(`${method} ${path} -> ${r.status()} ${text.slice(0, 300)}`);
  return (text ? JSON.parse(text) : null) as T;
}

/** Visible text that is exactly a translation key = a missing translation. */
export async function leakedKeys(page: Page, keys: Set<string>): Promise<string[]> {
  const text = await page.evaluate(() => document.body.innerText);
  const sections = new Set([...keys].map((k) => k.split('.')[0]));
  const found = new Set<string>();
  // a known key, or anything shaped like "<known section>.<word>" (= a key missing from en.ts that i18next printed raw)
  for (const tok of text.split(/[\s:،,()«»"]+/)) {
    if (keys.has(tok) || (/^[a-z][a-zA-Z0-9]*(\.[a-zA-Z0-9_]+)+$/.test(tok) && sections.has(tok.split('.')[0]))) found.add(tok);
  }
  return [...found];
}

export async function overflow(page: Page): Promise<number> {
  return page.evaluate(() => {
    const page = document.documentElement.scrollWidth - document.documentElement.clientWidth;
    // fixed header: elements pushed outside the screen don't create page scroll, so check them directly
    let header = 0;
    for (const el of Array.from(document.querySelectorAll('header *'))) {
      const r = el.getBoundingClientRect();
      if (r.width === 0 || r.height === 0) continue;
      header = Math.max(header, -r.left, r.right - window.innerWidth);
    }
    return Math.max(page, Math.round(header));
  });
}
