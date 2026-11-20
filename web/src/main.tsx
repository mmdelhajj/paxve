import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import '@mantine/core/styles.css';
import '@mantine/notifications/styles.css';
import '@fontsource/ibm-plex-sans-arabic/400.css';
import '@fontsource/ibm-plex-sans-arabic/500.css';
import '@fontsource/ibm-plex-sans-arabic/600.css';
import '@fontsource/ibm-plex-sans-arabic/700.css';
import './print.css';
import i18n from './i18n'; // applies the saved language
import App from './App';
import { installNativeHooks } from './native';
import { installErrorNet } from './util';

installNativeHooks();
installErrorNet(i18n.t.bind(i18n) as never);

if ('serviceWorker' in navigator && location.protocol === 'https:') {
  window.addEventListener('load', () => {
    navigator.serviceWorker.register('/sw.js').catch(() => {});
    // precache all code chunks of this build so the app keeps working offline
    navigator.serviceWorker.ready.then((reg) => reg.active?.postMessage('sync')).catch(() => {});
  });
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
