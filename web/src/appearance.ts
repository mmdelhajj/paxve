// Appearance ("comfort for the eyes"): theme, text size and row density. Same Magic layout; only colours, sizes and
// spacing change. Saved on this device at once (works before sign-in and offline) and on the user's account, so the
// choice follows them to other devices; the organisation can set a default for users who never chose.
import { useSyncExternalStore } from 'react';

export type Theme = 'auto' | 'comfort' | 'classic' | 'dark' | 'contrast';
export type Size = 'n' | 'l' | 'xl';
export type Density = 'compact' | 'comfy';
export interface Appearance { theme: Theme; size: Size; density: Density }

export const THEMES: Theme[] = ['auto', 'comfort', 'classic', 'dark', 'contrast'];
export const SIZES: Size[] = ['n', 'l', 'xl'];
export const DENSITIES: Density[] = ['comfy', 'compact'];
export const ZOOM: Record<Size, number> = { n: 1, l: 1.125, xl: 1.25 };

const KEY = 'paxve.appearance';
export const DEFAULT_APPEARANCE: Appearance = { theme: 'auto', size: 'n', density: 'comfy' };

function sane(v: Partial<Appearance> | null | undefined): Appearance {
  const a = { ...DEFAULT_APPEARANCE };
  if (v && THEMES.includes(v.theme as Theme)) a.theme = v.theme as Theme;
  if (v && SIZES.includes(v.size as Size)) a.size = v.size as Size;
  if (v && DENSITIES.includes(v.density as Density)) a.density = v.density as Density;
  return a;
}

function readLocal(): { a: Appearance; chosen: boolean } {
  try {
    const raw = localStorage.getItem(KEY);
    if (raw) return { a: sane(JSON.parse(raw)), chosen: true };
  } catch { /* storage unavailable */ }
  return { a: { ...DEFAULT_APPEARANCE }, chosen: false };
}

let state = readLocal();
// public pages (marketing site, shop, signing...) keep their own light design: only the app follows the user's choice
let themed = false;
const listeners = new Set<() => void>();
const darkMq = typeof window !== 'undefined' && window.matchMedia ? window.matchMedia('(prefers-color-scheme: dark)') : null;

/** The theme actually shown: "auto" follows the device (dark → dark, else comfort). */
export function resolvedTheme(a: Appearance = state.a): Exclude<Theme, 'auto'> {
  if (!themed) return 'comfort';
  if (a.theme === 'auto') return darkMq?.matches ? 'dark' : 'comfort';
  return a.theme;
}

/** The current zoom of the app area (1 on public pages); pointer maths inside the app divides by it. */
export function uiZoom(): number {
  return themed ? ZOOM[state.a.size] : 1;
}

let snap = { ...state.a, resolved: resolvedTheme(), themed };
function apply() {
  const root = document.documentElement;
  const r = resolvedTheme();
  root.dataset.mxTheme = r;
  root.dataset.mxSize = themed ? state.a.size : 'n';
  root.dataset.mxDensity = themed ? state.a.density : 'comfy';
  root.style.setProperty('--mx-zoom', String(uiZoom()));
  const meta = document.querySelector('meta[name="theme-color"]');
  if (meta) meta.setAttribute('content', r === 'dark' ? '#0d1117' : r === 'contrast' ? '#000000' : r === 'classic' ? '#0f1b2d' : '#1e2a3b');
  snap = { ...state.a, resolved: r, themed };
  listeners.forEach((f) => f());
}

export function getAppearance(): Appearance { return state.a; }
/** This device has an explicit choice (not just the defaults). */
export function chosenOnDevice(): boolean { return state.chosen; }

/** Change the look on this device (and, with save, on the account). */
export function setAppearance(next: Partial<Appearance>, save?: (a: Appearance) => void) {
  state = { a: sane({ ...state.a, ...next }), chosen: true };
  try { localStorage.setItem(KEY, JSON.stringify(state.a)); } catch { /* */ }
  apply();
  save?.(state.a);
}

/** After sign-in: the account's choice (or the organisation's default) wins over this device's, unless the device
 *  has its own explicit choice and the account none. */
export function adoptServerPrefs(p: Partial<Appearance> | null | undefined) {
  if (!p || (!p.theme && !p.size && !p.density)) return;
  state = { a: sane({ ...state.a, ...p }), chosen: true };
  try { localStorage.setItem(KEY, JSON.stringify(state.a)); } catch { /* */ }
  apply();
}

/** The app (signed in, portal, installed apps) follows the choice; public pages do not. */
export function setThemed(v: boolean) {
  if (themed === v) return;
  themed = v;
  apply();
}

export function useAppearance() {
  return useSyncExternalStore((f) => { listeners.add(f); return () => listeners.delete(f); }, () => snap);
}

darkMq?.addEventListener?.('change', () => { if (state.a.theme === 'auto') apply(); });
if (typeof document !== 'undefined') apply();
