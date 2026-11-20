// Appearance switcher ("comfort for the eyes"): theme, text size, row spacing. Quick button in the title bar and the
// phone apps, and the same panel on the security / profile page. Colours and sizes only; the Magic layout stays.
import { useEffect, useState } from 'react';
import { Button, Group, Popover, Select, Text } from '@mantine/core';
import { IconEye } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { api } from '../api';
import { notifyError, notifySaved } from '../util';
import { DENSITIES, SIZES, THEMES, setAppearance, useAppearance, type Appearance, type Theme } from '../appearance';

let timer: ReturnType<typeof setTimeout> | undefined;
/** Save the look on the account (quietly; this device already has it). */
export function saveAppearance(a: Appearance) {
  clearTimeout(timer);
  timer = setTimeout(() => { api('/me', { method: 'PATCH', body: { ui_prefs: a } }).catch(() => { /* this device keeps it */ }); }, 400);
}

// the swatch of each theme: ground, card, bar, accent
const SWATCH: Record<Theme, [string, string, string, string]> = {
  auto: ['#f6f5f1', '#14181f', '#1e2a3b', '#2f5fc4'],
  comfort: ['#f6f5f1', '#fcfbf8', '#1e2a3b', '#2f5fc4'],
  classic: ['#f5f7fa', '#ffffff', '#0f1b2d', '#1d4ed8'],
  dark: ['#14181f', '#1a1f28', '#0d1117', '#6b9cf0'],
  contrast: ['#ffffff', '#ffffff', '#000000', '#0033aa'],
};

function Swatch({ th }: { th: Theme }) {
  const [g, c, b, a] = SWATCH[th];
  if (th === 'auto') {
    return <span className="mx-look-sw" aria-hidden style={{ background: `linear-gradient(135deg, ${g} 50%, ${c} 50%)` }}><i style={{ background: a }} /></span>;
  }
  return <span className="mx-look-sw" aria-hidden style={{ background: g }}><b style={{ background: b }} /><s style={{ background: c }} /><i style={{ background: a }} /></span>;
}

/** The three choices; `local` = this device only (public portal). */
export function AppearancePanel({ local }: { local?: boolean }) {
  const { t } = useTranslation();
  const look = useAppearance();
  const set = (p: Partial<Appearance>) => setAppearance(p, local ? undefined : saveAppearance);
  return (
    <div className="mx-look" data-testid="look-panel">
      <div className="mx-look-h">{t('look.theme')}</div>
      <div className="mx-look-row" role="radiogroup" aria-label={t('look.theme')}>
        {THEMES.map((th) => (
          <button key={th} type="button" role="radio" aria-checked={look.theme === th} className="mx-look-opt" data-look-theme={th} onClick={() => set({ theme: th })}>
            <Swatch th={th} /><span>{t(`look.t.${th}`)}</span>
          </button>
        ))}
      </div>
      <div className="mx-look-h">{t('look.size')}</div>
      <div className="mx-look-row" role="radiogroup" aria-label={t('look.size')}>
        {SIZES.map((s) => (
          <button key={s} type="button" role="radio" aria-checked={look.size === s} className="mx-look-opt txt" data-look-size={s} onClick={() => set({ size: s })}>
            <span className="mx-look-aa" style={{ fontSize: s === 'n' ? 13 : s === 'l' ? 16 : 19 }}>أ A</span><span>{t(`look.s.${s}`)}</span>
          </button>
        ))}
      </div>
      <div className="mx-look-h">{t('look.density')}</div>
      <div className="mx-look-row" role="radiogroup" aria-label={t('look.density')}>
        {DENSITIES.map((d) => (
          <button key={d} type="button" role="radio" aria-checked={look.density === d} className="mx-look-opt txt" data-look-density={d} onClick={() => set({ density: d })}>
            <span className={`mx-look-rows ${d}`} aria-hidden><i /><i /><i /></span><span>{t(`look.d.${d}`)}</span>
          </button>
        ))}
      </div>
      <p className="mx-note">{t(local ? 'look.hintLocal' : 'look.hint')}</p>
    </div>
  );
}

/** Admins: the look of users who have not chosen one (new users start with it). */
export function OrgAppearanceDefault() {
  const { t } = useTranslation();
  const [v, setV] = useState<Partial<Appearance> | null>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => { api<Partial<Appearance>>('/org/ui-default').then((d) => setV(d ?? {})).catch(() => setV({})); }, []);
  if (v === null) return null;
  const opt = (k: string, vals: readonly string[]) => vals.map((x) => ({ value: x, label: t(`look.${k}.${x}`) }));
  async function save() {
    setBusy(true);
    try {
      const clean = Object.fromEntries(Object.entries(v ?? {}).filter(([, x]) => !!x));
      await api('/org/ui-default', { method: 'PUT', body: clean });
      notifySaved(t);
    } catch (e) { notifyError(t, e); } finally { setBusy(false); }
  }
  return (
    <div className="mx-look-org" data-testid="look-org">
      <Text fw={600} size="sm" mt="md">{t('look.orgTitle')}</Text>
      <Text size="xs" c="dimmed" mb={6}>{t('look.orgHint')}</Text>
      <Group gap="xs" align="flex-end" wrap="wrap">
        <Select size="xs" w={170} label={t('look.theme')} data={opt('t', THEMES)} value={v.theme ?? 'auto'} onChange={(x) => setV({ ...v, theme: (x ?? 'auto') as Theme })} allowDeselect={false} />
        <Select size="xs" w={130} label={t('look.size')} data={opt('s', SIZES)} value={v.size ?? 'n'} onChange={(x) => setV({ ...v, size: (x ?? 'n') as Appearance['size'] })} allowDeselect={false} />
        <Select size="xs" w={150} label={t('look.density')} data={opt('d', DENSITIES)} value={v.density ?? 'comfy'} onChange={(x) => setV({ ...v, density: (x ?? 'comfy') as Appearance['density'] })} allowDeselect={false} />
        <Button size="xs" loading={busy} onClick={() => void save()}>{t('common.save')}</Button>
      </Group>
    </div>
  );
}

/** Eye button for the title bar / app headers. */
export function AppearanceButton({ className = 'mx-tbtn', local }: { className?: string; local?: boolean }) {
  const { t } = useTranslation();
  return (
    <Popover position="bottom-end" withArrow shadow="md" width={340}>
      <Popover.Target>
        <button type="button" className={className} aria-label={t('look.title')} title={t('look.title')} data-testid="look-btn">
          <IconEye size={15} />
        </button>
      </Popover.Target>
      <Popover.Dropdown>
        <AppearancePanel local={local} />
      </Popover.Dropdown>
    </Popover>
  );
}
