import { useEffect, useState } from 'react';
import { Center, DirectionProvider, Loader, MantineProvider, createTheme, useDirection } from '@mantine/core';
import { Notifications } from '@mantine/notifications';
import { useTranslation } from 'react-i18next';
import { api, UNAUTHORIZED_EVENT, type Me } from './api';
import { applyLang, langInfo } from './i18n';
import { CompanyProvider } from './company';
import Login from './pages/Login';
import Shell from './Shell';

const font = '"IBM Plex Sans Arabic", system-ui, -apple-system, "Segoe UI", Tahoma, sans-serif';

const theme = createTheme({
  primaryColor: 'teal',
  fontFamily: font,
  headings: { fontFamily: font },
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

function Root() {
  const [me, setMe] = useState<Me | null | undefined>(undefined);

  useEffect(() => {
    api<Me>('/me')
      .then((m) => {
        applyLang(m.locale);
        setMe(m);
      })
      .catch(() => setMe(null));
    const onUnauthorized = () => setMe(null);
    window.addEventListener(UNAUTHORIZED_EVENT, onUnauthorized);
    return () => window.removeEventListener(UNAUTHORIZED_EVENT, onUnauthorized);
  }, []);

  if (me === undefined) {
    return (
      <Center h="100vh">
        <Loader />
      </Center>
    );
  }
  if (me === null) {
    return (
      <Login
        onLogin={(m) => {
          applyLang(m.locale);
          setMe(m);
        }}
      />
    );
  }
  return (
    <CompanyProvider>
      <Shell me={me} onMe={setMe} onLogout={() => setMe(null)} />
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
      </MantineProvider>
    </DirectionProvider>
  );
}
