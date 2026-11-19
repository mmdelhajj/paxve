import { lazy, Suspense, useEffect, useState } from 'react';
import { Center, DirectionProvider, Loader, MantineProvider, createTheme, useDirection } from '@mantine/core';
import { Notifications } from '@mantine/notifications';
import { useTranslation } from 'react-i18next';
import { api, UNAUTHORIZED_EVENT, type Me } from './api';
import { applyLang, langInfo, setSignedIn } from './i18n';
import { CompanyProvider } from './company';
import Login from './pages/Login';
import AppUpdate from './components/AppUpdate';
const Landing = lazy(() => import('./pages/public/Landing'));
const Signup = lazy(() => import('./pages/public/Signup'));
const Portal = lazy(() => import('./pages/public/Portal'));
const Reset = lazy(() => import('./pages/public/Reset'));
const ForcedMfa = lazy(() => import('./pages/Security').then((m) => ({ default: m.ForcedMfa })));
const Shell = lazy(() => import('./Shell'));

const fullPageLoader = (
  <Center h="100vh">
    <Loader />
  </Center>
);

const font = '"IBM Plex Sans Arabic", system-ui, -apple-system, "Segoe UI", Tahoma, sans-serif';

const theme = createTheme({
  primaryColor: 'blue',
  fontFamily: font,
  headings: { fontFamily: font },
  defaultRadius: 'sm',
});

function DirectionSync() {
  const { i18n } = useTranslation();
  const { setDirection } = useDirection();
  useEffect(() => {
    setDirection(langInfo(i18n.language).dir);
  }, [i18n.language, setDirection]);
  return null;
}

function usePublicRoute(): string {
  const read = () => window.location.hash.replace(/^#\/?/, '').split('/')[0];
  const [r, setR] = useState(read);
  useEffect(() => {
    const f = () => setR(read());
    window.addEventListener('hashchange', f);
    return () => window.removeEventListener('hashchange', f);
  }, []);
  return r;
}

function Root() {
  const [me, setMe] = useState<Me | null | undefined>(undefined);
  const route = usePublicRoute();
  const portalToken = route === 'p' ? window.location.hash.replace(/^#\/?/, '').split('/')[1] ?? '' : '';
  const resetToken = route === 'reset' ? window.location.hash.replace(/^#\/?/, '').split('/')[1] ?? '' : '';

  useEffect(() => {
    api<Me>('/me')
      .then((m) => {
        setSignedIn(true);
        applyLang(m.locale);
        setMe(m);
      })
      .catch(() => setMe(null));
    const onUnauthorized = () => { setSignedIn(false); setMe(null); };
    window.addEventListener(UNAUTHORIZED_EVENT, onUnauthorized);
    return () => window.removeEventListener(UNAUTHORIZED_EVENT, onUnauthorized);
  }, []);

  // the customer portal is public: it works whether or not someone is signed in on this browser
  if (portalToken) return <Suspense fallback={fullPageLoader}><Portal token={portalToken} /></Suspense>;
  if (resetToken) return <Suspense fallback={fullPageLoader}><Reset token={resetToken} /></Suspense>;
  if (me === undefined) return fullPageLoader;
  if (me === null) {
    const signedIn = (m: Me) => {
      setSignedIn(true, true);
      applyLang(m.locale);
      window.location.hash = '#/dashboard';
      setMe(m);
    };
    if (route === 'login') return <Login onLogin={signedIn} />;
    if (route === 'signup') return <Suspense fallback={fullPageLoader}><Signup onSignedUp={signedIn} /></Suspense>;
    return <Suspense fallback={fullPageLoader}><Landing /></Suspense>;
  }
  if (me.mfa_setup_required) {
    const logout = () => { api('/auth/logout', { method: 'POST' }).finally(() => setMe(null)); };
    const done = () => { api<Me>('/me').then(setMe).catch(() => setMe(null)); };
    return <Suspense fallback={fullPageLoader}><ForcedMfa onDone={done} onLogout={logout} /></Suspense>;
  }
  return (
    <CompanyProvider>
      <Suspense fallback={fullPageLoader}>
        <Shell me={me} onMe={setMe} onLogout={() => setMe(null)} />
      </Suspense>
    </CompanyProvider>
  );
}

export default function App() {
  const { i18n } = useTranslation();
  return (
    <DirectionProvider initialDirection={langInfo(i18n.language).dir} detectDirection={false}>
      <MantineProvider theme={theme}>
        <DirectionSync />
        <Notifications position="top-center" />
        <Root />
        <AppUpdate />
      </MantineProvider>
    </DirectionProvider>
  );
}
