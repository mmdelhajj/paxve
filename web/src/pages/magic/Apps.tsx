import { useState } from 'react';
import { Switch } from '@mantine/core';
import { useTranslation } from 'react-i18next';
import { api, can, type Company, type Me } from '../../api';
import { useCompany } from '../../company';
import { errorText } from '../../util';
import {
  IconBook2, IconBuildingFactory2, IconCash, IconCashRegister, IconDroplet, IconId, IconMessage, IconPackage, IconReceipt,
  IconRouter, IconSatellite, IconTargetArrow, IconUsersGroup,
} from '@tabler/icons-react';
import { ALL_APPS, CORE_APPS, RIBBON, type AppId, type Icon } from '../../ribbon';

const GLYPH: Record<AppId | 'wat' | 'mfg', Icon> = {
  gen: IconUsersGroup, acc: IconBook2, col: IconCash, msg: IconMessage, stk: IconPackage, pur: IconReceipt, net: IconRouter,
  sat: IconSatellite, pos: IconCashRegister, hr: IconId, crm: IconTargetArrow, wat: IconDroplet, mfg: IconBuildingFactory2,
};

const ICON_OF: Partial<Record<AppId, string>> = { gen: 'subs', acc: 'acc', col: 'col', net: 'net', stk: 'stk', pur: 'pur', crm: 'crm', pos: 'pos', hr: 'hr' };
const PRICE: Record<AppId | 'wat' | 'mfg', string> = {
  gen: 'core', acc: 'core', col: 'core', msg: '8', stk: '8', pur: '5', net: '10', sat: '10', pos: '15', hr: '8', crm: '8', wat: '', mfg: '',
};

/** التطبيقات: switch modules on or off for this company (Odoo-style). Menus follow at once; the server refuses
 *  calls to switched-off apps. Core apps (subscriptions & billing, accounting, collections) stay on. */
export default function Apps({ me }: { me: Me }) {
  const { t } = useTranslation();
  const { company, reload } = useCompany();
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  if (!company) return null;
  const apps = new Set(company.apps ?? ALL_APPS);
  const canEdit = can(me, 'settings.manage');

  async function save(next: Set<string>, business?: string) {
    setBusy(true);
    setMsg(null);
    try {
      await api<Company>(`/companies/${company!.id}/apps`, { method: 'PUT', body: { apps: [...next], business_type: business } });
      reload();
      setMsg({ ok: true, text: t('common.saved') });
    } catch (e) {
      setMsg({ ok: false, text: errorText(t, e) });
    } finally {
      setBusy(false);
    }
  }

  const color = (id: AppId) => RIBBON.find((r) => r.id === ICON_OF[id])?.color ?? '#57606f';
  const list: (AppId | 'wat' | 'mfg')[] = [...ALL_APPS, 'wat', 'mfg'];

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 12, maxWidth: 1200 }}>
      <div className="mx-box">
        <h3>{t('mx.t.apps')} <small>{t('mx.apps.intro')}</small></h3>
        <div style={{ padding: 10, display: 'flex', gap: 16, alignItems: 'center', flexWrap: 'wrap' }}>
          <label style={{ display: 'flex', gap: 8, alignItems: 'center' }}>{t('mx.apps.business')}
            <select value={company.business_type ?? 'generator'} disabled={!canEdit || busy}
              onChange={(e) => void save(apps, e.currentTarget.value)}>
              <option value="generator">{t('mx.apps.biz.generator')}</option>
              <option value="internet">{t('mx.apps.biz.internet')}</option>
              <option value="satellite">{t('mx.apps.biz.satellite')}</option>
            </select>
          </label>
          {msg && <span className={`mx-flash ${msg.ok ? 'good' : 'bad'}`}>{msg.text}</span>}
        </div>
      </div>
      <div className="mx-apps">
        {list.map((id) => {
          const soon = id === 'wat' || id === 'mfg';
          const core = (CORE_APPS as string[]).includes(id);
          const on = soon ? false : core || apps.has(id);
          return (
            <article key={id} className="mx-appc">
              <header>
                <span className="mx-ico" style={{ width: 34, height: 34, borderRadius: 8, display: 'grid', placeItems: 'center', color: '#fff', fontWeight: 700,
                  background: soon ? '#b2bec3' : color(id as AppId) }}>{(() => { const G = GLYPH[id]; return <G size={20} stroke={1.8} />; })()}</span>
                <div><b>{t(`mx.apps.${id}.name`)}</b>{soon && <span className="mx-pill warn" style={{ marginInlineStart: 6 }}>{t('mx.apps.soon')}</span>}</div>
              </header>
              <p>{t(`mx.apps.${id}.desc`)}</p>
              <div className="mx-arow">
                <span className="mx-price">{core ? t('mx.apps.core') : PRICE[id] ? t('mx.apps.price', { n: PRICE[id] }) : ''}</span>
                {core ? <span className="mx-pill on">{t('mx.apps.always')}</span> : (
                <Switch aria-label={t(`mx.apps.${id}.name`)} checked={on} disabled={soon || !canEdit || busy}
                  onChange={(e) => {
                    const next = new Set(apps);
                    if (e.currentTarget.checked) next.add(id); else next.delete(id);
                    void save(next);
                  }} />)}
              </div>
            </article>
          );
        })}
      </div>
    </div>
  );
}
