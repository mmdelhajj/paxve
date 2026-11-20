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
const PaySim = lazy(() => import('./pages/fin/Finish').then((m) => ({ default: m.PaySim })));
const Reset = lazy(() => import('./pages/public/Reset'));
const Rate = lazy(() => import('./pages/public/Rate'));
const Shop = lazy(() => import('./pages/public/Shop'));
const OrderTrack = lazy(() => import('./pages/public/Shop').then((m) => ({ default: m.OrderTrack })));
const Careers = lazy(() => import('./pages/public/Careers'));
const SignPage = lazy(() => import('./pages/public/Sign'));
const Optout = lazy(() => import('./pages/public/Sign').then((m) => ({ default: m.Optout })));
const MyAccounts = lazy(() => import('./pages/public/PortalMore').then((m) => ({ default: m.MyAccounts })));
const PosDisplay = lazy(() => import('./pages/pos/PosApp').then((m) => ({ default: m.CustomerDisplay })));
const ForcedMfa = lazy(() => import('./pages/Security').then((m) => ({ default: m.ForcedMfa })));
const ForcedPassword = lazy(() => import('./pages/ent/ForcedPassword'));
const Shell = lazy(() => import('./Shell'));

// phase 6b: the installed apps (Paxve Field, Paxve Owner) open straight on the sign-in screen, not the marketing site
const APP_ROUTES = ['owner', 'collect', 'driver', 'tech', 'van', 'scan'];

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
  primaryShade: 7,
  defaultRadius: 'md',
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
  const read = () => window.location.hash.replace(/^#\/?/, '').split('/')[0].split('?')[0];
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
  if (route === 'pos-display') return <Suspense fallback={fullPageLoader}><PosDisplay /></Suspense>;
  if (route === 'pay-sim') return <Suspense fallback={fullPageLoader}><PaySim token={window.location.hash.replace(/^#\/?/, '').split('/')[1] ?? ''} /></Suspense>;
  if (resetToken) return <Suspense fallback={fullPageLoader}><Reset token={resetToken} /></Suspense>;
  if (route === 'rate') return <Suspense fallback={fullPageLoader}><Rate token={window.location.hash.replace(/^#\/?/, '').split('/')[1] ?? ''} /></Suspense>;
  // phase 4 public pages: the online store, order tracking, careers, signing and marketing opt-out
  const arg = window.location.hash.replace(/^#\/?/, '').split('/')[1] ?? '';
  if (route === 'shop' && arg) return <Suspense fallback={fullPageLoader}><Shop /></Suspense>;
  if (route === 'order' && arg) return <Suspense fallback={fullPageLoader}><OrderTrack token={arg} key={arg} /></Suspense>;
  if (route === 'careers' && arg) return <Suspense fallback={fullPageLoader}><Careers slug={arg} key={arg} /></Suspense>;
  if (route === 'sign' && arg) return <Suspense fallback={fullPageLoader}><SignPage token={arg} key={arg} /></Suspense>;
  if (route === 'optout' && arg) return <Suspense fallback={fullPageLoader}><Optout token={arg} key={arg} /></Suspense>;
  // phase 6b: the installed customer app starts here and opens the account remembered on this phone
  if (route === 'my') return <Suspense fallback={fullPageLoader}><MyAccounts /></Suspense>;
  if (me === undefined) return fullPageLoader;
  if (me === null) {
    const signedIn = (m: Me) => {
      setSignedIn(true, true);
      applyLang(m.locale);
      if (!APP_ROUTES.includes(route)) window.location.hash = '#/dashboard'; // an installed app keeps its own screen
      setMe(m);
    };
    if (route === 'login' || APP_ROUTES.includes(route)) return <Login onLogin={signedIn} />;
    if (route === 'signup') return <Suspense fallback={fullPageLoader}><Signup onSignedUp={signedIn} /></Suspense>;
    return <Suspense fallback={fullPageLoader}><Landing /></Suspense>;
  }
  if (me.mfa_setup_required) {
    const logout = () => { api('/auth/logout', { method: 'POST' }).finally(() => setMe(null)); };
    const done = () => { api<Me>('/me').then(setMe).catch(() => setMe(null)); };
    return <Suspense fallback={fullPageLoader}><ForcedMfa onDone={done} onLogout={logout} /></Suspense>;
  }
  if (me.password_expired) {
    const logout = () => { api('/auth/logout', { method: 'POST' }).finally(() => setMe(null)); };
    const done = () => { api<Me>('/me').then(setMe).catch(() => setMe(null)); };
    return <Suspense fallback={fullPageLoader}><ForcedPassword onDone={done} onLogout={logout} /></Suspense>;
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
