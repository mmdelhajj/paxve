// Bridges to the Paxve Android app (mobile/, native plugin "PaxveApp").
// In a normal browser every helper falls back to the standard web behaviour.
import { isNativeApp } from './bt';

export { isNativeApp };

/* eslint-disable @typescript-eslint/no-explicit-any */

const METHODS = ['getAppInfo', 'print', 'saveFile', 'openExternal', 'exitApp'] as const;
let cached: any;

/** The PaxveApp native plugin, or null in browsers and in APKs built before it existed. */
function plugin(): any {
  if (cached !== undefined) return cached;
  cached = null;
  const c = (window as any).Capacitor;
  if (!c || typeof c.isNativePlatform !== 'function' || !c.isNativePlatform()) return null;
  const registered = Array.isArray(c.PluginHeaders) ? c.PluginHeaders.some((h: any) => h?.name === 'PaxveApp') : false;
  if (c.Plugins?.PaxveApp && (registered || !Array.isArray(c.PluginHeaders))) {
    cached = c.Plugins.PaxveApp;
  } else if (registered && typeof c.nativePromise === 'function') {
    const p: Record<string, (o?: unknown) => Promise<any>> = {};
    for (const m of METHODS) p[m] = (o?: unknown) => c.nativePromise('PaxveApp', m, o ?? {});
    cached = p;
  }
  return cached;
}

/** True when the installed app has the PaxveApp plugin (APK from 2026-11-18 onwards). */
export function hasAppPlugin(): boolean {
  return !!plugin();
}

/** Print the current page: Android print dialog in the app (printer or "Save as PDF"), window.print() elsewhere. */
export async function printPage(title?: string): Promise<void> {
  const p = plugin();
  if (p && hasAppPlugin()) {
    try {
      await p.print({ title: title || document.title || 'Paxve' });
      return;
    } catch {
      /* fall through */
    }
  }
  window.print();
}

function toBase64(bytes: Uint8Array): string {
  let s = '';
  const step = 0x8000;
  for (let i = 0; i < bytes.length; i += step) s += String.fromCharCode(...bytes.subarray(i, i + step));
  return btoa(s);
}

/** Save a file: Downloads/Paxve (or the share sheet) in the app, a normal download in browsers. */
export async function saveFile(name: string, data: Blob | string, mime = 'application/octet-stream', share = false): Promise<void> {
  const blob = typeof data === 'string' ? new Blob([data], { type: mime }) : data;
  const p = plugin();
  if (p && hasAppPlugin()) {
    const bytes = new Uint8Array(await blob.arrayBuffer());
    await p.saveFile({ name, mime: blob.type || mime, data: toBase64(bytes), share });
    return;
  }
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

/** Open a link outside the app (WhatsApp, phone, mail, other websites). */
export function openExternal(url: string): void {
  const p = plugin();
  if (p && hasAppPlugin()) {
    p.openExternal({ url }).catch(() => window.open(url, '_blank', 'noopener'));
    return;
  }
  window.open(url, '_blank', 'noopener');
}

export interface AppInfo { versionCode: number; versionName: string }

export async function appInfo(): Promise<AppInfo | null> {
  const p = plugin();
  if (!p || !hasAppPlugin()) return null;
  try {
    const r = await p.getAppInfo();
    return { versionCode: Number(r.versionCode), versionName: String(r.versionName ?? '') };
  } catch {
    return null;
  }
}

export interface LatestApp { versionCode: number; versionName: string; url: string }

export async function latestApp(): Promise<LatestApp | null> {
  try {
    const r = await fetch('/download/version.json', { cache: 'no-store' });
    if (!r.ok) return null;
    const j = await r.json();
    return { versionCode: Number(j.versionCode), versionName: String(j.versionName ?? ''), url: String(j.url || '/download/paxve.apk') };
  } catch {
    return null;
  }
}

const SAME_HOSTS = new Set([location.host, 'paxve.com', 'www.paxve.com']);

/**
 * Install app-only behaviour (call once at start-up):
 *  - Android back button → close an open dialog/menu, else go back in history, else let the app ask to exit
 *  - links to other sites / tel: / mailto: / whatsapp: open in the matching app
 *  - <a download> / blob links are saved through the native plugin
 */
export function installNativeHooks(): void {
  if (!isNativeApp()) return;
  document.documentElement.classList.add('paxve-app');

  (window as any).__paxveBack = (): string => {
    const visible = (el: Element) => {
      const h = el as HTMLElement;
      if (!h.getClientRects().length) return false;
      const cs = getComputedStyle(h);
      return cs.visibility !== 'hidden' && cs.display !== 'none' && cs.opacity !== '0';
    };
    const open = [...document.querySelectorAll('[role="dialog"], [role="menu"], .mantine-Popover-dropdown, .mantine-Drawer-content')].some(visible);
    if (open) {
      const target = (document.activeElement as HTMLElement | null) ?? document.body;
      target.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', code: 'Escape', bubbles: true }));
      // Mantine listens on window for some components
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', code: 'Escape', bubbles: true }));
      const close = document.querySelector<HTMLElement>('[role="dialog"] .mantine-Modal-close, [role="dialog"] .mantine-Drawer-close');
      if (document.querySelector('[role="dialog"]') && close) close.click();
      return 'handled';
    }
    const route = location.hash.replace(/^#\/?/, '');
    const home = ['', 'login', 'dashboard', 'collect'];
    if (!home.includes(route) && history.length > 1) {
      history.back();
      return 'handled';
    }
    return 'exit';
  };

  if (!hasAppPlugin()) return; // older APK: Capacitor's own link handling only

  // remember blobs behind object URLs: the CSP (default-src 'self') forbids fetch('blob:…')
  const blobs = new Map<string, Blob>();
  const create = URL.createObjectURL.bind(URL);
  const revoke = URL.revokeObjectURL.bind(URL);
  URL.createObjectURL = (o: Blob | MediaSource) => {
    const url = create(o);
    if (o instanceof Blob) blobs.set(url, o);
    return url;
  };
  URL.revokeObjectURL = (url: string) => {
    // keep a moment longer: the click that uses it may come after revoke() in the same task
    setTimeout(() => blobs.delete(url), 60000);
    revoke(url);
  };
  const dataUrlBlob = (href: string): Blob | null => {
    const m = /^data:([^;,]*)(;base64)?,(.*)$/s.exec(href);
    if (!m) return null;
    const raw = m[2] ? atob(m[3]) : decodeURIComponent(m[3]);
    const bytes = new Uint8Array(raw.length);
    for (let i = 0; i < raw.length; i++) bytes[i] = raw.charCodeAt(i) & 0xff;
    return new Blob([bytes], { type: m[1] || 'application/octet-stream' });
  };

  document.addEventListener(
    'click',
    (e) => {
      if (e.defaultPrevented || e.button !== 0) return;
      const a = (e.target as Element | null)?.closest?.('a[href]') as HTMLAnchorElement | null;
      if (!a) return;
      const href = a.getAttribute('href') || '';
      if (href.startsWith('#') || href === '') return;
      let u: URL;
      try {
        u = new URL(href, location.href);
      } catch {
        return;
      }
      if (a.hasAttribute('download') && (u.protocol === 'blob:' || u.protocol === 'data:')) {
        e.preventDefault();
        const name = a.getAttribute('download') || 'paxve-file';
        const blob = u.protocol === 'blob:' ? blobs.get(u.href) : dataUrlBlob(u.href);
        if (blob) void saveFile(name, blob, blob.type);
        return;
      }
      if (u.protocol === 'http:' || u.protocol === 'https:') {
        if (SAME_HOSTS.has(u.host)) {
          if (u.pathname.startsWith('/download/') && u.pathname.endsWith('.apk')) {
            e.preventDefault();
            openExternal(u.href); // the browser downloads + installs the update
          }
          return;
        }
      }
      e.preventDefault();
      openExternal(u.href);
    },
    true,
  );

  // window.open('https://wa.me/…') and friends
  const origOpen = window.open.bind(window);
  window.open = ((url?: string | URL, target?: string, features?: string) => {
    if (url) {
      try {
        const u = new URL(String(url), location.href);
        if (!SAME_HOSTS.has(u.host) || !/^https?:$/.test(u.protocol)) {
          openExternal(u.href);
          return null;
        }
      } catch {
        /* ignore */
      }
    }
    return origOpen(url as string, target, features);
  }) as typeof window.open;
}
