import { useEffect, useState, type CSSProperties } from 'react';
import { useTranslation } from 'react-i18next';
import { isNativeApp } from '../native';

// Phase 6b: "Install the app" button for the installable web apps (Paxve Field, My account, Paxve Owner, Paxve).
// /pwa.js keeps the browser's install offer in window.__pxvInstall; on iPhone (no offer) it explains "Add to Home Screen".

interface InstallEvent extends Event { prompt: () => Promise<void>; userChoice: Promise<{ outcome: string }> }
declare global { interface Window { __pxvInstall?: InstallEvent | null } }

export function isStandalone(): boolean {
  try {
    return window.matchMedia('(display-mode: standalone)').matches || (navigator as Navigator & { standalone?: boolean }).standalone === true;
  } catch { return false; }
}

const isIOS = () => /iphone|ipad|ipod/i.test(navigator.userAgent) && !/crios|fxios/i.test(navigator.userAgent);

export function useInstallable(): [boolean, () => void] {
  const [ev, setEv] = useState<InstallEvent | null>(() => window.__pxvInstall ?? null);
  useEffect(() => {
    const f = () => setEv(window.__pxvInstall ?? null);
    window.addEventListener('paxve:installable', f);
    return () => window.removeEventListener('paxve:installable', f);
  }, []);
  const install = () => {
    if (!ev) return;
    void ev.prompt().then(() => ev.userChoice).finally(() => { window.__pxvInstall = null; setEv(null); });
  };
  return [!!ev, install];
}

/** A compact button; renders nothing when already installed, inside the Android app, or when the browser can't install. */
export default function InstallApp({ className = 'mx-btn sm', label, style }: { className?: string; label?: string; style?: CSSProperties }) {
  const { t } = useTranslation();
  const [can, install] = useInstallable();
  const [hint, setHint] = useState(false);
  if (isStandalone() || isNativeApp()) return null;
  if (can) return <button type="button" className={className} style={style} onClick={install}>⤓ {label ?? t('pwa.install')}</button>;
  if (!isIOS()) return null;
  return (
    <span style={{ position: 'relative' }}>
      <button type="button" className={className} style={style} onClick={() => setHint((h) => !h)}>⤓ {label ?? t('pwa.install')}</button>
      {hint && <span role="note" style={{ position: 'absolute', insetInlineEnd: 0, top: '110%', zIndex: 50, width: 240, background: '#0F1B2D', color: '#fff',
        padding: 8, borderRadius: 8, fontSize: 12.5, lineHeight: 1.6 }}>{t('pwa.iosHint')}</span>}
    </span>
  );
}

/** The app icon badge (Android / desktop installed apps): pending work count. */
export function setBadge(n: number) {
  const nav = navigator as Navigator & { setAppBadge?: (n?: number) => Promise<void>; clearAppBadge?: () => Promise<void> };
  try {
    if (n > 0) void nav.setAppBadge?.(n).catch(() => {});
    else void nav.clearAppBadge?.().catch(() => {});
  } catch { /* not supported */ }
}
