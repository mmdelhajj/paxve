import { useEffect, useState } from 'react';
import { Affix, Alert, Button, Text } from '@mantine/core';
import { IconDownload } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { appInfo, hasAppPlugin, isNativeApp, latestApp, openExternal, type LatestApp } from '../native';

const DISMISS = 'paxve.appUpdateDismissed';

/** Inside the Android app: tells the user when a newer APK is published (downloads/version.json). */
export default function AppUpdate() {
  const { t } = useTranslation();
  const [latest, setLatest] = useState<LatestApp | null>(null);

  useEffect(() => {
    if (!isNativeApp()) return;
    let alive = true;
    void (async () => {
      const [mine, newest] = await Promise.all([appInfo(), latestApp()]);
      if (!alive || !newest || !newest.versionCode) return;
      // APKs without the PaxveApp plugin are older than every version.json
      const installed = mine?.versionCode ?? 0;
      if (newest.versionCode <= installed) return;
      try {
        if (sessionStorage.getItem(DISMISS) === String(newest.versionCode)) return;
      } catch {
        /* ignore */
      }
      setLatest(newest);
    })();
    return () => {
      alive = false;
    };
  }, []);

  if (!latest) return null;
  const dismiss = () => {
    try {
      sessionStorage.setItem(DISMISS, String(latest.versionCode));
    } catch {
      /* ignore */
    }
    setLatest(null);
  };
  const canOpen = hasAppPlugin();
  return (
    <Affix position={{ bottom: 16, left: 16, right: 16 }} zIndex={400} className="no-print">
      <Alert color="teal" variant="filled" title={t('app.updateTitle')} withCloseButton onClose={dismiss} icon={<IconDownload size={18} />}>
        <Text size="sm" mb={canOpen ? 'xs' : 0}>
          {t('app.updateBody', { version: latest.versionName })}
          {!canOpen && <> {t('app.updateManual')}</>}
        </Text>
        {canOpen && (
          <Button size="xs" variant="white" color="teal" leftSection={<IconDownload size={14} />}
            onClick={() => openExternal(new URL(latest.url, location.href).href)}>
            {t('app.updateButton')}
          </Button>
        )}
      </Alert>
    </Affix>
  );
}
