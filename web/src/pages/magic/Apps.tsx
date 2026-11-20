import { useState } from 'react';
import { Switch } from '@mantine/core';
import { useTranslation } from 'react-i18next';
import { api, can, planHas, type Company, type Me } from '../../api';
import { openScreen } from '../../nav';
import { useCompany } from '../../company';
import { errorText } from '../../util';
import {
  IconBook2, IconBuildingFactory2, IconCash, IconCashRegister, IconDroplet, IconId, IconMessage, IconPackage, IconReceipt,
  IconRouter, IconSatellite, IconTargetArrow, IconUsersGroup, IconHeadset, IconLayoutKanban, IconCar, IconWorldWww, IconSpeakerphone, IconSparkles,
} from '@tabler/icons-react';
import { ALL_APPS, CORE_APPS, RIBBON, type AppId, type Icon } from '../../ribbon';

const GLYPH: Record<AppId | 'wat' | 'mfg', Icon> = {
  gen: IconUsersGroup, acc: IconBook2, col: IconCash, msg: IconMessage, stk: IconPackage, pur: IconReceipt, net: IconRouter,
  sat: IconSatellite, pos: IconCashRegister, hr: IconId, crm: IconTargetArrow, svc: IconHeadset, wat: IconDroplet, mfg: IconBuildingFactory2,
  prj: IconLayoutKanban, flt: IconCar, web: IconWorldWww, mkt: IconSpeakerphone, ai: IconSparkles,
};

const ICON_OF: Partial<Record<AppId, string>> = { gen: 'subs', acc: 'acc', col: 'col', net: 'net', stk: 'stk', pur: 'pur', crm: 'crm', pos: 'pos', hr: 'hr', svc: 'svc', mfg: 'mfg', prj: 'prj', flt: 'flt', web: 'web', mkt: 'mkt', ai: 'ai' };
const PRICE: Record<AppId | 'wat' | 'mfg', string> = {
  gen: 'core', acc: 'core', col: 'core', msg: '8', stk: '8', pur: '5', net: '10', sat: '10', pos: '15', hr: '8', crm: '8', svc: '5', wat: '', mfg: '', prj: '8', flt: '5', web: '10', mkt: '8', ai: '10',
};

// business packs: choosing one switches its apps on and sets its defaults on the server (demo data stays optional)
const PACKS = ['supermarket', 'clothing', 'electronics', 'furniture', 'wholesale', 'restaurant', 'pharmacy', 'factory'];

interface Plan { business_type: string; target: string[]; add: string[]; remove: { app: string; has_data: boolean }[] }

/** التطبيقات: switch modules on or off for this company (Odoo-style). Menus follow at once; the server refuses
 *  calls to switched-off apps. Core apps (subscriptions & billing, accounting, collections) stay on. */
export default function Apps({ me }: { me: Me }) {
  const { t } = useTranslation();
  const { company, reload } = useCompany();
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [plan, setPlan] = useState<(Plan & { off: Set<string> }) | null>(null);
  const [names, setNames] = useState<{ kind: string; from: string; to: string }[] | null>(null);
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

  // choosing a business type: first show what changes (apps to add, apps it does not need — those holding data are
  // left on unless ticked), then apply (audit P7-F1)
  async function pickBusiness(b: string) {
    setMsg(null);
    try {
      const p = await api<Plan>(`/companies/${company!.id}/apps/plan?business_type=${b}`);
      setPlan({ ...p, off: new Set(p.remove.filter((x) => !x.has_data).map((x) => x.app)) });
    } catch (e) {
      setMsg({ ok: false, text: errorText(t, e) });
    }
  }
  async function applyPlan() {
    if (!plan) return;
    setBusy(true);
    setMsg(null);
    try {
      const b = plan.business_type;
      if (PACKS.includes(b)) {
        await api(`/companies/${company!.id}/retail/packs/${b}`, { method: 'POST', body: { demo: false, remove: [...plan.off] } });
      } else {
        const next = new Set([...apps].filter((a) => !plan.off.has(a)));
        plan.add.forEach((a) => next.add(a));
        await api<Company>(`/companies/${company!.id}/apps`, { method: 'PUT', body: { apps: [...next], business_type: b } });
      }
      setPlan(null);
      reload();
      setMsg({ ok: true, text: t('common.saved') });
    } catch (e) {
      setMsg({ ok: false, text: errorText(t, e) });
    } finally {
      setBusy(false);
    }
  }
  // rename the names the system created in English (accounts, stages, warehouse, till, demo items) — preview first
  async function translateNames(run: boolean) {
    setBusy(true);
    setMsg(null);
    try {
      const r = await api<{ changes: { kind: string; from: string; to: string }[] }>(`/companies/${company!.id}/translate-defaults`, { method: 'POST', body: { dry_run: !run } });
      if (run) { setNames(null); reload(); setMsg({ ok: true, text: t('p7.tr.done', { n: r.changes.length }) }); }
      else setNames(r.changes);
    } catch (e) {
      setMsg({ ok: false, text: errorText(t, e) });
    } finally {
      setBusy(false);
    }
  }

  const color = (id: AppId) => RIBBON.find((r) => r.id === ICON_OF[id])?.color ?? '#57606f';
  const list: (AppId | 'wat')[] = [...ALL_APPS, 'wat'];

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 12, maxWidth: 1200 }}>
      <div className="mx-box">
        <h3>{t('mx.t.apps')} <small>{t('mx.apps.intro')}</small></h3>
        <div style={{ padding: 10, display: 'flex', gap: 16, alignItems: 'center', flexWrap: 'wrap' }}>
          <label style={{ display: 'flex', gap: 8, alignItems: 'center' }}>{t('mx.apps.business')}
            <select value={company.business_type ?? 'generator'} disabled={!canEdit || busy}
              onChange={(e) => void pickBusiness(e.currentTarget.value)}>
              <option value="generator">{t('mx.apps.biz.generator')}</option>
              <option value="internet">{t('mx.apps.biz.internet')}</option>
              <option value="satellite">{t('mx.apps.biz.satellite')}</option>
              <option value="water">{t('mx.apps.biz.water')}</option>
              <optgroup label={t('mx.apps.biz.packs')}>
                {PACKS.map((b) => <option key={b} value={b}>{t(`mx.apps.biz.${b}`)}</option>)}
              </optgroup>
            </select>
          </label>
          <button className="mx-btn" onClick={() => openScreen('retail', 'packs')}>{t('mx.apps.biz.packs')}</button>
          <small style={{ opacity: 0.75 }}>{t('mx.apps.biz.packHint')}</small>
          {msg && <span className={`mx-flash ${msg.ok ? 'good' : 'bad'}`}>{msg.text}</span>}
        </div>
        {plan && (
          <div className="mx-note" style={{ margin: 10, display: 'flex', flexDirection: 'column', gap: 6 }}>
            <b>{t('p7.plan.title', { biz: t(`mx.apps.biz.${plan.business_type}`) })}</b>
            {plan.add.length > 0 && <span>{t('p7.plan.add')}: {plan.add.map((a) => t(`mx.apps.${a}.name`)).join('، ')}</span>}
            {plan.remove.length > 0 ? (<>
              <span>{t('p7.plan.remove')}</span>
              <div style={{ display: 'flex', flexWrap: 'wrap', gap: 12 }}>
                {plan.remove.map((x) => (
                  <label key={x.app} style={{ display: 'flex', gap: 4, alignItems: 'center' }}>
                    <input type="checkbox" checked={plan.off.has(x.app)} onChange={(e) => {
                      const off = new Set(plan.off);
                      if (e.currentTarget.checked) off.add(x.app); else off.delete(x.app);
                      setPlan({ ...plan, off });
                    }} />
                    {t(`mx.apps.${x.app}.name`)}{x.has_data && <span className="mx-pill warn">{t('p7.plan.hasData')}</span>}
                  </label>
                ))}
              </div>
            </>) : <span>{t('p7.plan.nothingOff')}</span>}
            <div style={{ display: 'flex', gap: 8 }}>
              <button className="mx-btn main" disabled={busy} onClick={() => void applyPlan()}>{t('p7.plan.apply')}</button>
              <button className="mx-btn" disabled={busy} onClick={() => setPlan(null)}>{t('common.cancel')}</button>
            </div>
          </div>
        )}
        {canEdit && (
          <div style={{ padding: '0 10px 10px', display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
            <button className="mx-btn" disabled={busy} onClick={() => void translateNames(false)}>{t('p7.tr.btn')}</button>
            <small style={{ opacity: 0.75 }}>{t('p7.tr.hint')}</small>
            {names && (names.length === 0 ? <span className="mx-flash good">{t('p7.tr.none')}</span> : (
              <div className="mx-note" style={{ width: '100%', display: 'flex', flexDirection: 'column', gap: 4 }}>
                <b>{t('p7.tr.preview', { n: names.length })}</b>
                <div style={{ maxHeight: 160, overflow: 'auto', fontSize: 12 }}>{names.slice(0, 60).map((c, i) => <div key={i}>{c.from} ← {c.to}</div>)}</div>
                <div style={{ display: 'flex', gap: 8 }}>
                  <button className="mx-btn main" disabled={busy} onClick={() => void translateNames(true)}>{t('p7.tr.run')}</button>
                  <button className="mx-btn" onClick={() => setNames(null)}>{t('common.cancel')}</button>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
      <div className="mx-apps">
        {list.map((id) => {
          const soon = false;
          const core = (CORE_APPS as string[]).includes(id);
          // water is a business type (metered m³ billing on the same engine), not a separate app
          const on = id === 'wat' ? company.business_type === 'water' : core || apps.has(id);
          // the Paxve plan decides which apps can be switched on (water is a business type, always allowed)
          const inPlan = id === 'wat' || planHas(me, id);
          return (
            <article key={id} className="mx-appc">
              <header>
                <span className="mx-ico" style={{ width: 34, height: 34, borderRadius: 8, display: 'grid', placeItems: 'center', color: '#fff', fontWeight: 700,
                  background: soon ? '#b2bec3' : color(id as AppId) }}>{(() => { const G = GLYPH[id]; return <G size={20} stroke={1.8} />; })()}</span>
                <div><b>{t(`mx.apps.${id}.name`)}</b>{soon && <span className="mx-pill warn" style={{ marginInlineStart: 6 }}>{t('mx.apps.soon')}</span>}</div>
              </header>
              <p>{t(`mx.apps.${id}.desc`)}</p>
              {!inPlan && on && <p className="mx-note" style={{ margin: 0 }}>{t('mx.apps.notInPlanOn')}</p>}
              <div className="mx-arow">
                <span className="mx-price">{core ? t('mx.apps.core') : PRICE[id] ? t('mx.apps.price', { n: PRICE[id] }) : ''}</span>
                {!core && !inPlan && (
                  <button className="mx-pill warn" style={{ border: 0, cursor: 'pointer' }} title={t('mx.upgrade')} onClick={() => openScreen('my-sub')}>
                    {t('mx.apps.notInPlan')}</button>
                )}
                {core ? <span className="mx-pill on">{t('mx.apps.always')}</span> : (
                <Switch aria-label={t(`mx.apps.${id}.name`)} checked={on} disabled={soon || !canEdit || busy || (!inPlan && !on)}
                  onChange={(e) => {
                    if (id === 'wat') { void save(apps, e.currentTarget.checked ? 'water' : 'generator'); return; }
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
