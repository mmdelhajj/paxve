import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';
import en from './locales/en';
import ar from './locales/ar';
import fr from './locales/fr';

// Languages. en / ar / fr ship inside the app (locales/*.ts, reviewed). The others are machine
// translations (locales/extra/<code>.json, made by scripts/translate.mjs) loaded on demand; a key they
// lack falls back to English (Kurdish Sorani to Arabic, then English). Each tenant can correct any text
// (Settings → Translations): those corrections are loaded after sign-in and win over the shipped text.
// To add a language: add it here, run `node scripts/translate.mjs <code>`, and add the code to
// supportedLocales in internal/httpapi/server.go.
export const languages = [
  { code: 'ar', label: 'عربي', dir: 'rtl' as const, intl: 'ar-LB' },
  { code: 'en', label: 'English', dir: 'ltr' as const, intl: 'en-GB' },
  { code: 'fr', label: 'Français', dir: 'ltr' as const, intl: 'fr-FR' },
  { code: 'tr', label: 'Türkçe', dir: 'ltr' as const, intl: 'tr-TR', machine: true },
  { code: 'es', label: 'Español', dir: 'ltr' as const, intl: 'es-ES', machine: true },
  { code: 'de', label: 'Deutsch', dir: 'ltr' as const, intl: 'de-DE', machine: true },
  { code: 'pt', label: 'Português', dir: 'ltr' as const, intl: 'pt-PT', machine: true },
  { code: 'ru', label: 'Русский', dir: 'ltr' as const, intl: 'ru-RU', machine: true },
  { code: 'fa', label: 'فارسی', dir: 'rtl' as const, intl: 'fa-IR', machine: true },
  { code: 'ur', label: 'اردو', dir: 'rtl' as const, intl: 'ur-PK', machine: true },
  { code: 'ckb', label: 'کوردی', dir: 'rtl' as const, intl: 'ckb-IQ', machine: true },
];
export type Locale = string;

const resources = { en: { translation: en }, ar: { translation: ar }, fr: { translation: fr } };
const extra = import.meta.glob('./locales/extra/*.json') as Record<string, () => Promise<{ default: Record<string, unknown> }>>;
const loaded = new Set(['en', 'ar', 'fr']);

export function langInfo(code: string) {
  return languages.find((l) => l.code === code) ?? languages[1];
}

function savedLang(): string {
  try {
    const v = localStorage.getItem('paxve.lang');
    if (v && languages.some((l) => l.code === v)) return v;
  } catch {
    /* storage unavailable */
  }
  const nav = navigator.language?.slice(0, 2);
  return languages.some((l) => l.code === nav) ? nav : 'ar';
}

i18n.use(initReactI18next).init({
  resources,
  lng: 'ar',
  fallbackLng: { ckb: ['ar', 'en'], default: ['en'] },
  interpolation: { escapeValue: false },
});

/** load a language's texts (for printing a document in another language than the screen) */
export async function ensureBundle(code: string) {
  if (loaded.has(code)) return;
  const load = extra[`./locales/extra/${code}.json`];
  if (!load) return;
  const mod = await load();
  const { _meta, ...messages } = mod.default as Record<string, unknown> & { _meta?: unknown };
  void _meta;
  i18n.addResourceBundle(code, 'translation', messages, true, true);
  loaded.add(code);
}

// ---- tenant corrections ----

let signedIn = false;
const overridden = new Set<string>();

function nest(flat: Record<string, string>): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const [key, v] of Object.entries(flat)) {
    const parts = key.split('.');
    let o = out;
    for (const p of parts.slice(0, -1)) o = (o[p] ??= {}) as Record<string, unknown>;
    o[parts[parts.length - 1]] = v;
  }
  return out;
}

async function loadOverrides(code: string) {
  if (!signedIn || overridden.has(code)) return;
  overridden.add(code);
  try {
    const res = await fetch(`/api/v1/translations?lang=${code}`, { credentials: 'same-origin' });
    if (!res.ok) return;
    const flat = (await res.json()) as Record<string, string>;
    if (Object.keys(flat).length) i18n.addResourceBundle(code, 'translation', nest(flat), true, true);
  } catch {
    /* offline: the shipped texts stay */
  }
}

/** call after sign-in (and after a correction is saved) so the tenant's own texts are loaded */
export function setSignedIn(v: boolean, reload = false) {
  signedIn = v;
  if (reload) overridden.clear();
  if (v) void loadOverrides(i18n.language).then(() => i18n.emit('languageChanged', i18n.language));
}

let wanted = '';

export function applyLang(lng: string) {
  const info = langInfo(lng);
  wanted = info.code;
  document.documentElement.lang = info.code;
  document.documentElement.dir = info.dir;
  try {
    localStorage.setItem('paxve.lang', info.code);
  } catch {
    /* storage unavailable */
  }
  void ensureBundle(info.code)
    .then(() => loadOverrides(info.code))
    .finally(() => {
      if (wanted !== info.code) return; // a later choice won
      if (i18n.language !== info.code) void i18n.changeLanguage(info.code);
      else i18n.emit('languageChanged', info.code);
    });
}

applyLang(savedLang());

export default i18n;
