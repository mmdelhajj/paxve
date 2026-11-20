import { useEffect, useMemo, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { api } from '../../api';
import { applyLang, languages } from '../../i18n';
import { errorText } from '../../util';
import { isNativeApp } from '../../native';
import InstallApp from '../../components/InstallApp';

// Phase 6b: the public marketing site (signed-out visitors at paxve.com). Arabic first, English / French; the
// Paxve navy palette; no heavy libraries. Plans and the sales contact come from the platform (/public/site);
// the demo form creates a lead in the platform console.

const BIZ = ['generator', 'internet', 'satellite', 'water', 'supermarket', 'clothing', 'electronics', 'furniture', 'wholesale',
  'restaurant', 'pharmacy', 'factory'] as const;
const BIZ_ICON: Record<string, string> = {
  generator: '⚡', internet: '📶', satellite: '📡', water: '💧', supermarket: '🛒', clothing: '👕', electronics: '📱', furniture: '🛋️',
  wholesale: '🚚', restaurant: '🍽️', pharmacy: '💊', factory: '🏭',
};
const FEAT = ['money', 'field', 'stock', 'mfg', 'smart', 'shop', 'esign', 'apps', 'security'] as const;
const FEAT_ICON: Record<string, string> = { money: '💱', field: '📲', stock: '📦', mfg: '⚙️', smart: '✦', shop: '🛍️', esign: '✍️', apps: '📱', security: '🔒' };
const CMP = ['currency', 'offline', 'ledger', 'apps', 'alerts', 'shop', 'backup', 'arabic'] as const;
const CMP_VAL: Record<string, [string, string]> = { // [spreadsheets, old systems]: y / n / p (partly)
  currency: ['n', 'p'], offline: ['n', 'p'], ledger: ['n', 'y'], apps: ['n', 'n'], alerts: ['n', 'n'], shop: ['n', 'n'], backup: ['n', 'p'], arabic: ['p', 'p'],
};
const FAQ = ['q1', 'q2', 'q3', 'q4', 'q5', 'q6', 'q7'] as const;

interface SitePlan {
  code: string; name_en: string; name_ar: string; name_fr: string; price_monthly: string; price_yearly: string; currency: string;
  max_subscribers: number | null; max_users: number | null; max_companies: number | null; ai_monthly_limit: number | null;
}
interface Site { plans: SitePlan[] | null; phone: string; email: string }

export default function Landing() {
  const { t, i18n } = useTranslation();
  const inApp = isNativeApp();
  const lang = i18n.language;
  const [site, setSite] = useState<Site | null>(null);
  const [biz, setBiz] = useState<string>('generator');
  const [yearly, setYearly] = useState(false);
  const [menu, setMenu] = useState(false);

  useEffect(() => {
    // ?lang=en|fr|ar (links from the sitemap and ads) picks the language once
    const q = new URLSearchParams(window.location.search).get('lang');
    if (q && languages.some((l) => l.code === q) && q !== lang) applyLang(q);
    api<Site>('/public/site').then(setSite).catch(() => setSite({ plans: [], phone: '', email: '' }));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  useEffect(() => { document.title = t('site.metaTitle'); }, [t, lang]);

  const plans = site?.plans ?? [];
  const planName = (p: SitePlan) => (lang === 'ar' && p.name_ar ? p.name_ar : lang === 'fr' && p.name_fr ? p.name_fr : p.name_en);
  const go = (id: string) => { setMenu(false); document.getElementById(id)?.scrollIntoView({ behavior: 'smooth', block: 'start' }); };

  return (
    <div className="px-site">
      <style>{CSS}</style>
      <header className="px-top">
        <div className="px-wrap px-topin">
          <a className="px-brand" href="#/" onClick={(e) => { e.preventDefault(); window.scrollTo({ top: 0, behavior: 'smooth' }); }}>
            <span className="px-mark" aria-hidden>P</span><b>Paxve</b>
          </a>
          <nav className={`px-nav ${menu ? 'open' : ''}`} aria-label={t('site.nav.label')}>
            <button type="button" onClick={() => go('business')}>{t('site.nav.business')}</button>
            <button type="button" onClick={() => go('features')}>{t('site.nav.features')}</button>
            <button type="button" onClick={() => go('pricing')}>{t('site.nav.pricing')}</button>
            <button type="button" onClick={() => go('faq')}>{t('site.nav.faq')}</button>
            <button type="button" onClick={() => go('contact')}>{t('site.nav.contact')}</button>
          </nav>
          <span className="px-sp" />
          <select className="px-lang" aria-label="Language" value={lang} onChange={(e) => applyLang(e.currentTarget.value)}>
            {languages.map((l) => <option key={l.code} value={l.code}>{l.label}</option>)}
          </select>
          <a className="px-btn ghost hide-sm" href="#/login">{t('site.signIn')}</a>
          <a className="px-btn main" href="#/signup">{t('site.trial')}</a>
          <button type="button" className="px-burger" aria-label={t('site.nav.label')} aria-expanded={menu} onClick={() => setMenu((m) => !m)}>☰</button>
        </div>
      </header>

      {/* hero */}
      <section className="px-hero">
        <div className="px-wrap px-herogrid">
          <div className="px-herotext">
            <span className="px-badge">{t('site.hero.badge')}</span>
            <h1>{t('site.hero.title')}</h1>
            <p className="px-lead">{t('site.hero.lead')}</p>
            <div className="px-row">
              <a className="px-btn main lg" href="#/signup">{t('site.trial')}</a>
              <button type="button" className="px-btn light lg" onClick={() => go('contact')}>{t('site.demo')}</button>
            </div>
            <p className="px-small">{t('site.hero.note')}</p>
            <ul className="px-ticks">
              {(['t1', 't2', 't3'] as const).map((k) => <li key={k}>{t(`site.hero.${k}`)}</li>)}
            </ul>
            <div className="px-row" style={{ gap: 8 }}>
              {!inApp && <a className="px-link" href="/download/paxve.apk">{t('site.android')}</a>}
              <InstallApp className="px-link" label={t('site.installWeb')} />
            </div>
          </div>
          <HeroMock />
        </div>
      </section>

      {/* numbers strip */}
      <section className="px-strip">
        <div className="px-wrap px-stats">
          {(['s1', 's2', 's3', 's4'] as const).map((k) => (
            <div key={k}><b>{t(`site.stats.${k}.v`)}</b><span>{t(`site.stats.${k}.l`)}</span></div>
          ))}
        </div>
      </section>

      {/* business types */}
      <section id="business" className="px-sec">
        <div className="px-wrap">
          <h2>{t('site.biz.title')}</h2>
          <p className="px-sub">{t('site.biz.sub')}</p>
          <div className="px-bizgrid" role="tablist">
            {BIZ.map((b) => (
              <button key={b} type="button" role="tab" aria-selected={biz === b} className="px-biz" onClick={() => setBiz(b)}>
                <span aria-hidden>{BIZ_ICON[b]}</span>{t(`site.biz.${b}.t`)}
              </button>
            ))}
          </div>
          <article className="px-bizcard" role="tabpanel" id={`biz-${biz}`}>
            <div>
              <h3><span aria-hidden>{BIZ_ICON[biz]}</span> {t(`site.biz.${biz}.t`)}</h3>
              <p>{t(`site.biz.${biz}.d`)}</p>
              <a className="px-btn main" href="#/signup">{t('site.bizTry', { name: t(`site.biz.${biz}.t`) })}</a>
            </div>
            <ul className="px-checks">
              {listOf(t(`site.biz.${biz}.f`, { returnObjects: true })).map((x) => <li key={x}>{x}</li>)}
            </ul>
          </article>
        </div>
      </section>

      {/* features */}
      <section id="features" className="px-sec alt">
        <div className="px-wrap">
          <h2>{t('site.feat.title')}</h2>
          <p className="px-sub">{t('site.feat.sub')}</p>
          <div className="px-cards">
            {FEAT.map((f) => (
              <div key={f} className="px-card">
                <span className="px-ic" aria-hidden>{FEAT_ICON[f]}</span>
                <h3>{t(`site.feat.${f}.t`)}</h3>
                <p>{t(`site.feat.${f}.d`)}</p>
              </div>
            ))}
          </div>
        </div>
      </section>

      {/* comparison */}
      <section className="px-sec">
        <div className="px-wrap">
          <h2>{t('site.cmp.title')}</h2>
          <p className="px-sub">{t('site.cmp.sub')}</p>
          <div className="px-tablewrap">
            <table className="px-table">
              <thead><tr><th>{' '}</th><th className="us">Paxve</th><th>{t('site.cmp.sheets')}</th><th>{t('site.cmp.old')}</th></tr></thead>
              <tbody>
                {CMP.map((k) => (
                  <tr key={k}>
                    <td>{t(`site.cmp.r.${k}`)}</td>
                    <td className="us"><Mark v="y" /></td>
                    <td><Mark v={CMP_VAL[k][0]} /></td>
                    <td><Mark v={CMP_VAL[k][1]} /></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      </section>

      {/* pricing */}
      <section id="pricing" className="px-sec alt">
        <div className="px-wrap">
          <h2>{t('site.price.title')}</h2>
          <p className="px-sub">{t('site.price.sub')}</p>
          <div className="px-toggle" role="group" aria-label={t('site.price.cycle')}>
            <button type="button" aria-pressed={!yearly} onClick={() => setYearly(false)}>{t('site.price.monthly')}</button>
            <button type="button" aria-pressed={yearly} onClick={() => setYearly(true)}>{t('site.price.yearly')}</button>
          </div>
          <div className="px-plans">
            {plans.length === 0 && <p className="px-sub">{site ? t('site.price.ask') : '…'}</p>}
            {plans.map((p) => {
              const hot = p.code === 'business';
              const price = yearly ? Number(p.price_yearly) : Number(p.price_monthly);
              const lim = (n: number | null) => (n == null || n === 0 ? t('site.price.unlimited') : n.toLocaleString(lang === 'ar' ? 'ar-LB' : lang));
              return (
                <div key={p.code} className={`px-plan ${hot ? 'hot' : ''}`}>
                  {hot && <span className="px-pop">{t('site.price.popular')}</span>}
                  <h3>{planName(p)}</h3>
                  <div className="px-price" dir="ltr"><b>{p.currency === 'USD' ? '$' : ''}{price.toLocaleString('en')}</b>{p.currency !== 'USD' && ` ${p.currency}`}<span>/ {yearly ? t('site.price.year') : t('site.price.month')}</span></div>
                  <ul className="px-checks">
                    <li>{t('site.price.subs', { n: lim(p.max_subscribers) })}</li>
                    <li>{t('site.price.users', { n: lim(p.max_users) })}</li>
                    <li>{t('site.price.companies', { n: lim(p.max_companies) })}</li>
                    <li>{t('site.price.ai', { n: lim(p.ai_monthly_limit) })}</li>
                    <li>{t('site.price.all')}</li>
                  </ul>
                  <a className={`px-btn ${hot ? 'main' : 'light'} block`} href="#/signup">{t('site.trial')}</a>
                </div>
              );
            })}
          </div>
          <p className="px-small center">{t('site.price.note')}</p>
        </div>
      </section>

      {/* FAQ */}
      <section id="faq" className="px-sec">
        <div className="px-wrap narrow">
          <h2>{t('site.faq.title')}</h2>
          {FAQ.map((q) => (
            <details key={q} className="px-faq">
              <summary>{t(`site.faq.${q}.q`)}</summary>
              <p>{t(`site.faq.${q}.a`)}</p>
            </details>
          ))}
        </div>
      </section>

      {/* contact + demo request */}
      <section id="contact" className="px-sec dark">
        <div className="px-wrap px-contact">
          <div>
            <h2>{t('site.contact.title')}</h2>
            <p className="px-lead">{t('site.contact.sub')}</p>
            {site?.phone && <p className="px-cline">☎ <span dir="ltr">{site.phone}</span>{' · '}
              <a href={`https://wa.me/${site.phone.replace(/[^\d]/g, '')}`} target="_blank" rel="noreferrer">WhatsApp</a></p>}
            {site?.email && <p className="px-cline">✉ <span dir="ltr">{site.email}</span></p>}
            <a className="px-btn main lg" href="#/signup">{t('site.trial')}</a>
          </div>
          <DemoForm />
        </div>
      </section>

      <footer className="px-foot">
        <div className="px-wrap px-footin">
          <span>© {new Date().getFullYear()} Paxve · {t('site.foot.made')}</span>
          <span className="px-sp" />
          <button type="button" onClick={() => go('pricing')}>{t('site.nav.pricing')}</button>
          {!inApp && <a href="/download/paxve.apk">{t('site.android')}</a>}
          <a href="/api/v1/openapi.yaml">API</a>
          <a href="#/login">{t('site.signIn')}</a>
        </div>
      </footer>
    </div>
  );
}

/** A list text: an array in AR / EN / FR, an {0: …, 1: …} object in the machine-translated languages. */
function listOf(v: unknown): string[] {
  if (Array.isArray(v)) return v.map(String);
  if (v && typeof v === 'object') return Object.keys(v).sort((a, b) => Number(a) - Number(b)).map((k) => String((v as Record<string, unknown>)[k]));
  return [];
}

function Mark({ v }: { v: string }) {
  const { t } = useTranslation();
  const label = v === 'y' ? t('site.cmp.yes') : v === 'p' ? t('site.cmp.part') : t('site.cmp.no');
  return <span className={`px-mk ${v}`} aria-label={label} title={label}>{v === 'y' ? '✓' : v === 'p' ? '~' : '✕'}</span>;
}

function DemoForm() {
  const { t, i18n } = useTranslation();
  const [f, setF] = useState({ name: '', company: '', phone: '', email: '', business: 'generator', city: '', message: '', website: '' });
  const [state, setState] = useState<'idle' | 'busy' | 'done'>('idle');
  const [err, setErr] = useState('');
  const set = (k: keyof typeof f) => (e: { currentTarget: { value: string } }) => { const v = e.currentTarget.value; setF((x) => ({ ...x, [k]: v })); };
  const ok = useMemo(() => f.name.trim() !== '' && (f.phone.trim().length >= 6 || f.email.includes('@')), [f]);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!ok) return;
    setState('busy');
    setErr('');
    try {
      await api('/public/demo-request', { method: 'POST', body: { ...f, lang: i18n.language } });
      setState('done');
    } catch (x) { setErr(errorText(t, x)); setState('idle'); }
  };
  if (state === 'done') return <div className="px-form done"><h3>✓ {t('site.form.thanks')}</h3><p>{t('site.form.thanksSub')}</p></div>;
  return (
    <form className="px-form" onSubmit={(e) => void submit(e)}>
      <h3>{t('site.form.title')}</h3>
      <label>{t('site.form.name')} *<input required maxLength={120} value={f.name} onChange={set('name')} autoComplete="name" /></label>
      <label>{t('site.form.company')}<input maxLength={160} value={f.company} onChange={set('company')} autoComplete="organization" /></label>
      <div className="px-2col">
        <label>{t('site.form.phone')}<input dir="ltr" inputMode="tel" maxLength={40} value={f.phone} onChange={set('phone')} autoComplete="tel" /></label>
        <label>{t('site.form.email')}<input dir="ltr" type="email" maxLength={200} value={f.email} onChange={set('email')} autoComplete="email" /></label>
      </div>
      <div className="px-2col">
        <label>{t('site.form.business')}
          <select value={f.business} onChange={set('business')}>
            {BIZ.map((b) => <option key={b} value={b}>{t(`site.biz.${b}.t`)}</option>)}
            <option value="other">{t('site.form.other')}</option>
          </select>
        </label>
        <label>{t('site.form.city')}<input maxLength={80} value={f.city} onChange={set('city')} /></label>
      </div>
      <label>{t('site.form.message')}<textarea rows={3} maxLength={2000} value={f.message} onChange={set('message')} /></label>
      {/* honeypot: hidden from people, bots fill it */}
      <label className="px-hp" aria-hidden="true">Website<input tabIndex={-1} autoComplete="off" value={f.website} onChange={set('website')} /></label>
      <small>{t('site.form.hint')}</small>
      {err && <p className="px-err">{err}</p>}
      <button className="px-btn main lg block" type="submit" disabled={!ok || state === 'busy'}>{state === 'busy' ? '…' : t('site.form.send')}</button>
    </form>
  );
}

/** A screen mock-up: the collector screen with a receipt in USD + LBP (pure HTML / CSS, no image to download). */
function HeroMock() {
  const { t } = useTranslation();
  return (
    <div className="px-mock" aria-hidden>
      <div className="px-mbar"><span className="px-mark sm">P</span><b>{t('site.mock.app')}</b><span className="px-sp" /><span className="px-chip">1 USD = 89,500 LBP</span></div>
      <div className="px-mbody">
        <div className="px-mrow"><span className="px-av">{t('site.mock.initials')}</span><div><b>{t('site.mock.customer')}</b><small>#1042 · A-12 · 10A</small></div><span className="px-chip warn">{t('site.mock.late')}</span></div>
        <div className="px-mline"><span>{t('site.mock.bill1')}</span><b dir="ltr">$38.00</b></div>
        <div className="px-mline"><span>{t('site.mock.bill2')}</span><b dir="ltr">$35.00</b></div>
        <div className="px-mline tot"><span>{t('site.mock.total')}</span><b dir="ltr">$73.00 = 6,533,500 LBP</b></div>
        <div className="px-mpay"><div><small>USD</small><b dir="ltr">50</b></div><div><small>LBP</small><b dir="ltr">2,058,500</b></div></div>
        <div className="px-mbtn">{t('site.mock.pay')} <kbd>F9</kbd></div>
      </div>
      <div className="px-mfoot"><span>{t('site.mock.today')}</span><b dir="ltr">$1,240 + 18,450,000 LBP</b></div>
    </div>
  );
}

const CSS = `
.px-site{--navy:#0B1F3A;--navy2:#13305A;--blue:#1D4ED8;--ink:#0F1B2D;--mut:#5B6B7F;--line:#E3E8EF;--bg:#F4F6F9;--green:#0F7B4B;--red:#B42318;--amber:#A15C00;
  background:#fff;color:var(--ink);font-family:'IBM Plex Sans Arabic',system-ui,-apple-system,'Segoe UI',Tahoma,sans-serif;font-size:16px;line-height:1.6;overflow-x:hidden}
.px-site *{box-sizing:border-box}
.px-wrap{max-width:1180px;margin:0 auto;padding:0 20px}.px-wrap.narrow{max-width:820px}
.px-sp{flex:1}
.px-top{position:sticky;top:0;z-index:30;background:rgba(11,31,58,.97);color:#fff;backdrop-filter:saturate(1.2) blur(6px);padding-top:env(safe-area-inset-top,0px)}
.px-topin{display:flex;align-items:center;gap:12px;height:64px}
.px-brand{display:flex;align-items:center;gap:8px;color:#fff;text-decoration:none;font-size:20px}
.px-mark{width:32px;height:32px;border-radius:8px;background:var(--blue);display:grid;place-items:center;font-weight:700;color:#fff}.px-mark.sm{width:24px;height:24px;font-size:13px;border-radius:6px}
.px-nav{display:flex;gap:2px}.px-nav button{font:inherit;font-size:14.5px;color:#D6E0EE;background:none;border:0;padding:8px 10px;border-radius:8px;cursor:pointer}
.px-nav button:hover{background:var(--navy2);color:#fff}
.px-lang{font:inherit;font-size:13px;background:var(--navy2);color:#fff;border:1px solid #24426F;border-radius:8px;padding:6px 8px}
.px-burger{display:none;font-size:22px;background:none;border:0;color:#fff;cursor:pointer;padding:4px 8px}
.px-btn{display:inline-flex;align-items:center;justify-content:center;gap:6px;font:inherit;font-weight:600;font-size:15px;border-radius:10px;padding:9px 16px;
  text-decoration:none;cursor:pointer;border:1px solid transparent;white-space:nowrap}
.px-btn.main{background:var(--blue);color:#fff}.px-btn.main:hover{background:#1E40AF}
.px-btn.light{background:#fff;color:var(--ink);border-color:#D5DCE6}.px-btn.light:hover{border-color:var(--blue);color:var(--blue)}
.px-btn.ghost{color:#fff;border-color:#24426F}.px-btn.lg{font-size:16.5px;padding:12px 22px}.px-btn.block{display:flex;width:100%}
.px-btn:disabled{opacity:.55;cursor:not-allowed}
.px-link{font:inherit;font-size:14px;color:var(--blue);background:none;border:0;padding:0;cursor:pointer;text-decoration:underline;text-underline-offset:3px}
.px-hero{background:linear-gradient(180deg,#EEF3FF 0%,#fff 100%);padding:64px 0 56px}
.px-herogrid{display:grid;grid-template-columns:minmax(0,1.05fr) minmax(0,.95fr);gap:48px;align-items:center}
.px-badge{display:inline-block;background:#fff;border:1px solid #C7D4F5;color:var(--blue);border-radius:999px;padding:4px 12px;font-size:13.5px;font-weight:600}
.px-hero h1{font-size:46px;line-height:1.18;margin:16px 0 12px;letter-spacing:-.01em}
.px-lead{font-size:18.5px;color:#3B4A5E;margin:0 0 22px}.px-sec.dark .px-lead{color:#C9D5E6}
.px-row{display:flex;gap:12px;flex-wrap:wrap;align-items:center}
.px-small{font-size:13.5px;color:var(--mut);margin:12px 0}.px-small.center{text-align:center;margin-top:20px}
.px-ticks{list-style:none;padding:0;margin:0 0 14px;display:flex;flex-wrap:wrap;gap:6px 18px}.px-ticks li::before{content:'✓ ';color:var(--green);font-weight:700}
.px-mock{background:#fff;border:1px solid var(--line);border-radius:16px;box-shadow:0 30px 60px -20px rgba(11,31,58,.35);overflow:hidden;font-size:14px;max-width:480px;justify-self:center;width:100%}
.px-mbar{display:flex;align-items:center;gap:8px;background:var(--navy);color:#fff;padding:10px 14px}
.px-chip{font-size:12px;background:var(--navy2);border:1px solid #24426F;border-radius:999px;padding:2px 10px;color:#D6E0EE}.px-chip.warn{background:#FFF4E0;border-color:#F5D9A8;color:var(--amber)}
.px-mbody{padding:14px;display:flex;flex-direction:column;gap:8px;background:var(--bg)}
.px-mrow{display:flex;align-items:center;gap:10px;background:#fff;border:1px solid var(--line);border-radius:10px;padding:10px}.px-mrow small{display:block;color:var(--mut)}
.px-av{width:38px;height:38px;border-radius:10px;background:#EEF3FF;color:var(--blue);display:grid;place-items:center;font-weight:700}
.px-mrow .px-chip{margin-inline-start:auto}
.px-mline{display:flex;justify-content:space-between;background:#fff;border:1px solid var(--line);border-radius:8px;padding:8px 10px}
.px-mline.tot{background:#F8FAFC;font-weight:600}
.px-mpay{display:grid;grid-template-columns:1fr 1fr;gap:8px}.px-mpay div{background:#fff;border:1px solid #CBD5E1;border-radius:8px;padding:6px 10px}.px-mpay small{display:block;color:var(--mut);font-size:11.5px}
.px-mpay b{font-family:'IBM Plex Mono',ui-monospace,monospace;font-size:18px}
.px-mbtn{background:var(--blue);color:#fff;text-align:center;border-radius:10px;padding:11px;font-weight:700}
.px-mbtn kbd{font-family:'IBM Plex Mono',monospace;font-size:11px;background:#1E40AF;border-radius:4px;padding:1px 6px}
.px-mfoot{display:flex;justify-content:space-between;gap:8px;background:var(--navy);color:#B8C6DA;padding:8px 14px;font-size:12.5px}.px-mfoot b{color:#fff}
.px-strip{background:var(--navy);color:#fff;padding:22px 0}
.px-stats{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:16px;text-align:center}.px-stats b{display:block;font-size:28px}.px-stats span{color:#B8C6DA;font-size:14px}
.px-sec{padding:72px 0}.px-sec.alt{background:var(--bg)}.px-sec.dark{background:var(--navy);color:#fff}
.px-sec h2{font-size:34px;line-height:1.25;margin:0 0 8px;text-align:center}.px-sec.dark h2{text-align:start}
.px-sub{color:var(--mut);text-align:center;max-width:720px;margin:0 auto 32px;font-size:17px}
.px-bizgrid{display:grid;grid-template-columns:repeat(6,minmax(0,1fr));gap:10px;margin-bottom:18px}
.px-biz{font:inherit;font-size:14.5px;font-weight:600;display:flex;flex-direction:column;align-items:center;gap:6px;padding:14px 8px;border-radius:12px;border:1px solid var(--line);background:#fff;cursor:pointer;color:var(--ink)}
.px-biz span{font-size:24px}.px-biz:hover{border-color:#9DB4EE}.px-biz[aria-selected=true]{border-color:var(--blue);background:#EEF3FF;color:var(--blue);box-shadow:0 0 0 1px var(--blue) inset}
.px-bizcard{display:grid;grid-template-columns:minmax(0,1fr) minmax(0,1fr);gap:32px;border:1px solid var(--line);border-radius:16px;padding:28px;background:#fff;box-shadow:0 12px 30px -18px rgba(11,31,58,.25)}
.px-bizcard h3{font-size:24px;margin:0 0 8px}.px-bizcard p{color:#3B4A5E;margin:0 0 18px}
.px-checks{list-style:none;padding:0;margin:0;display:flex;flex-direction:column;gap:8px}.px-checks li{padding-inline-start:26px;position:relative}
.px-checks li::before{content:'✓';position:absolute;inset-inline-start:0;top:0;width:20px;height:20px;border-radius:999px;background:#E6F4EC;color:var(--green);font-size:12px;font-weight:700;display:grid;place-items:center;margin-top:3px}
.px-cards{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:16px}
.px-card{background:#fff;border:1px solid var(--line);border-radius:14px;padding:22px}.px-card h3{margin:10px 0 6px;font-size:18px}.px-card p{margin:0;color:#3B4A5E;font-size:15px}
.px-ic{width:44px;height:44px;border-radius:12px;background:#EEF3FF;display:grid;place-items:center;font-size:22px}
.px-tablewrap{overflow-x:auto;border:1px solid var(--line);border-radius:14px}
.px-table{width:100%;border-collapse:collapse;min-width:560px;font-size:15px}
.px-table th,.px-table td{padding:13px 16px;border-bottom:1px solid #EEF1F5;text-align:center}.px-table td:first-child,.px-table th:first-child{text-align:start}
.px-table thead th{background:#F8FAFC;font-size:14px;color:var(--mut)}.px-table .us{background:#EEF3FF;color:var(--blue);font-weight:700}
.px-mk{display:inline-grid;place-items:center;width:26px;height:26px;border-radius:999px;font-weight:700}.px-mk.y{background:#E6F4EC;color:var(--green)}
.px-mk.n{background:#FDECEA;color:var(--red)}.px-mk.p{background:#FFF4E0;color:var(--amber)}
.px-toggle{display:flex;justify-content:center;gap:0;margin:-12px auto 26px;width:max-content;border:1px solid #D5DCE6;border-radius:999px;padding:3px;background:#fff}
.px-toggle button{font:inherit;font-size:14px;font-weight:600;border:0;background:none;padding:6px 16px;border-radius:999px;cursor:pointer;color:var(--mut)}
.px-toggle button[aria-pressed=true]{background:var(--navy);color:#fff}
.px-plans{display:grid;grid-template-columns:repeat(auto-fit,minmax(240px,1fr));gap:16px;align-items:stretch}
.px-plan{position:relative;background:#fff;border:1px solid var(--line);border-radius:16px;padding:24px;display:flex;flex-direction:column;gap:12px}
.px-plan.hot{border:2px solid var(--blue);box-shadow:0 20px 40px -22px rgba(29,78,216,.5)}
.px-pop{position:absolute;top:-12px;inset-inline-start:20px;background:var(--blue);color:#fff;border-radius:999px;padding:2px 12px;font-size:12.5px;font-weight:600}
.px-plan h3{margin:0;font-size:19px}.px-price b{font-size:40px;font-weight:800}.px-price span{color:var(--mut);margin-inline-start:6px}
.px-plan .px-checks{flex:1}
.px-faq{border:1px solid var(--line);border-radius:12px;margin-bottom:10px;background:#fff}
.px-faq summary{cursor:pointer;padding:16px 18px;font-weight:600;list-style:none}.px-faq summary::-webkit-details-marker{display:none}
.px-faq summary::after{content:'+';float:inline-end;font-size:20px;color:var(--blue)}.px-faq[open] summary::after{content:'−'}
.px-faq p{margin:0;padding:0 18px 16px;color:#3B4A5E}
.px-contact{display:grid;grid-template-columns:minmax(0,1fr) minmax(0,1fr);gap:40px;align-items:start}
.px-cline{font-size:17px}.px-cline a{color:#9DB4EE}
.px-form{background:#fff;color:var(--ink);border-radius:16px;padding:22px;display:flex;flex-direction:column;gap:10px}
.px-form h3{margin:0 0 4px}.px-form label{display:flex;flex-direction:column;gap:4px;font-size:14px;font-weight:600;color:#3B4A5E;min-width:0}
.px-form input,.px-form select,.px-form textarea{font:inherit;font-size:15px;font-weight:400;border:1px solid #CBD5E1;border-radius:8px;padding:9px 10px;color:var(--ink);background:#fff;width:100%}
.px-form input:focus,.px-form select:focus,.px-form textarea:focus{outline:2px solid var(--blue);outline-offset:1px;border-color:var(--blue)}
.px-form small{color:var(--mut)}.px-form.done{text-align:center;padding:40px 22px}
.px-2col{display:grid;grid-template-columns:1fr 1fr;gap:10px}
.px-hp{position:absolute!important;width:1px;height:1px;overflow:hidden;clip:rect(0 0 0 0);clip-path:inset(50%);white-space:nowrap;margin:-1px;padding:0;border:0}
.px-err{color:var(--red);margin:0}
.px-foot{background:#081629;color:#8FA3BF;padding:22px 0;padding-bottom:calc(22px + env(safe-area-inset-bottom,0px));font-size:14px}
.px-footin{display:flex;align-items:center;gap:16px;flex-wrap:wrap}.px-foot a,.px-foot button{font:inherit;color:#B8C6DA;background:none;border:0;cursor:pointer;text-decoration:none;padding:0}
@media (max-width:980px){
  .px-herogrid,.px-bizcard,.px-contact{grid-template-columns:minmax(0,1fr)}
  .px-hero h1{font-size:36px}.px-bizgrid{grid-template-columns:repeat(4,minmax(0,1fr))}.px-cards{grid-template-columns:repeat(2,minmax(0,1fr))}
  .px-nav{display:none;position:absolute;top:64px;inset-inline:0;flex-direction:column;background:var(--navy);padding:8px 16px 14px;border-top:1px solid #24426F}
  .px-nav.open{display:flex}.px-nav button{text-align:start;font-size:16px;padding:12px 8px}.px-burger{display:block}
}
@media (max-width:620px){
  .px-wrap{padding:0 16px}.px-topin{gap:8px}.hide-sm{display:none}.px-brand b{display:none}
  .px-hero{padding:36px 0}.px-hero h1{font-size:30px}.px-lead{font-size:16.5px}
  .px-sec{padding:52px 0}.px-sec h2{font-size:26px}.px-bizgrid{grid-template-columns:repeat(3,minmax(0,1fr))}
  .px-cards,.px-2col{grid-template-columns:minmax(0,1fr)}.px-stats{grid-template-columns:repeat(2,minmax(0,1fr))}.px-bizcard{padding:20px}
  .px-lang{max-width:92px}.px-btn.main{padding:8px 12px}
}
`;
