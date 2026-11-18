import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';
import en from './locales/en';
import ar from './locales/ar';
import fr from './locales/fr';

// To add a language: create locales/<code>.ts (same keys as en.ts) and add it here
// and to supportedLocales in internal/httpapi/server.go.
export const languages = [
  { code: 'ar', label: 'عربي', dir: 'rtl' as const, intl: 'ar-LB' },
  { code: 'en', label: 'English', dir: 'ltr' as const, intl: 'en-GB' },
  { code: 'fr', label: 'Français', dir: 'ltr' as const, intl: 'fr-FR' },
];
export type Locale = string;

const resources = { en: { translation: en }, ar: { translation: ar }, fr: { translation: fr } };

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
  lng: savedLang(),
  fallbackLng: 'en',
  interpolation: { escapeValue: false },
});

export function applyLang(lng: string) {
  const info = langInfo(lng);
  if (i18n.language !== info.code) void i18n.changeLanguage(info.code);
  document.documentElement.lang = info.code;
  document.documentElement.dir = info.dir;
  try {
    localStorage.setItem('paxve.lang', info.code);
  } catch {
    /* storage unavailable */
  }
}

export default i18n;
