import { useEffect, useRef } from 'react';
import { notifications } from '@mantine/notifications';
import i18n from '../../i18n';
import { api } from '../../api';

/** Idle logout (phase 8): after `minutes` without a key, click, touch or scroll the app signs out (a warning shows one
 *  minute before). 0 = never. The server also ends sessions not seen for that long. */
export function useIdleLogout(minutes: number, onLogout: () => void) {
  const last = useRef(Date.now());
  const warned = useRef(false);
  useEffect(() => {
    if (!minutes || minutes < 1) return;
    const bump = () => { last.current = Date.now(); if (warned.current) { warned.current = false; notifications.hide('idle-logout'); } };
    const evs = ['keydown', 'mousedown', 'touchstart', 'wheel', 'scroll'];
    evs.forEach((e) => window.addEventListener(e, bump, { passive: true }));
    const timer = window.setInterval(() => {
      const idle = (Date.now() - last.current) / 60000;
      if (idle >= minutes) {
        window.clearInterval(timer);
        api('/auth/logout', { method: 'POST' }).catch(() => {}).finally(onLogout);
      } else if (idle >= minutes - 1 && !warned.current) {
        warned.current = true;
        notifications.show({ id: 'idle-logout', color: 'yellow', autoClose: false, message: i18n.t('ep.idleWarn') });
      }
    }, 15000);
    return () => { window.clearInterval(timer); evs.forEach((e) => window.removeEventListener(e, bump)); };
  }, [minutes, onLogout]);
}
