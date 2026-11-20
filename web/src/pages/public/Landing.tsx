import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import '@fontsource/readex-pro/500.css';
import '@fontsource/readex-pro/600.css';
import './site/site.css';
import { api } from '../../api';
import { applyLang, languages } from '../../i18n';
import { errorText } from '../../util';
import { isNativeApp } from '../../native';
import InstallApp from '../../components/InstallApp';
import { Icon } from './site/icons';

// The public marketing site (signed-out visitors at paxve.com): home, one page per business type (#/for/<type>) and the
// privacy / terms pages. Arabic first. Product images are real screens of a demo company (web/public/site/*.webp,
// captured from the "Paxve Showcase" tenant); the small floating cards in the hero repeat figures from those same
// screens. Plans and the sales contact come from the platform (/public/site); the demo form creates a platform lead.

const BIZ = ['generator', 'internet', 'satellite', 'water', 'supermarket', 'clothing', 'electronics', 'furniture', 'wholesale',
  'restaurant', 'pharmacy', 'factory'] as const;
type Biz = typeof BIZ[number];
const isBiz = (x: string): x is Biz => (BIZ as readonly string[]).includes(x);

/** the product screen shown for each business type, and the second one lower on its page */
const BIZ_SHOT: Record<Biz, [string, string]> = {
  generator: ['jibaya', 'subs'], internet: ['subs', 'smart-late'], satellite: ['subs', 'jibaya'], water: ['jibaya', 'smart-cash'],
  supermarket: ['pos', 'items'], clothing: ['pos', 'items'], electronics: ['items', 'pos'], furniture: ['items', 'mfg-bom'],
  wholesale: ['items', 'smart-late'], restaurant: ['pos', 'items'], pharmacy: ['items', 'pos'], factory: ['mfg-bom', 'items'],
};
/** product rows on the home page: [key, screenshot, page the "learn more" link opens] */
const ROWS: [string, string, string][] = [
  ['collect', 'jibaya', '#/for/generator'], ['pos', 'pos', '#/for/supermarket'], ['stock', 'items', '#/for/wholesale'],
  ['mfg', 'mfg-bom', '#/for/factory'], ['cash', 'smart-cash', '#/signup'],
];
const SHOT_ALT: Record<string, string> = {
  jibaya: 'collect', subs: 'subs', pos: 'pos', items: 'items', 'mfg-bom': 'mfg', 'smart-cash': 'cash', 'smart-late': 'late', 'ph-owner': 'owner',
  'ph-collect': 'field', dash: 'dash',
};
/** bumped whenever the product screenshots in web/public/site are re-captured (the CDN caches images by URL) */
const IMG_V = '2';
const CMP = ['currency', 'offline', 'ledger', 'apps', 'alerts', 'shop', 'backup', 'arabic'] as const;
const CMP_VAL: Record<string, [string, string]> = { // [spreadsheets, old systems]: y / n / p (partly)
  currency: ['n', 'p'], offline: ['n', 'p'], ledger: ['n', 'y'], apps: ['n', 'n'], alerts: ['n', 'n'], shop: ['n', 'n'], backup: ['n', 'p'], arabic: ['p', 'p'],
};
const FAQ = ['q1', 'q2', 'q3', 'q4', 'q5', 'q6', 'q7'] as const;
const LEGAL_SECTIONS = ['s1', 's2', 's3', 's4', 's5', 's6'] as const;
const WEBHOOK_EVENTS = ['invoice.created', 'payment.created', 'customer.created', 'pos.order.created'];

interface SitePlan {
  code: string; name_en: string; name_ar: string; name_fr: string; price_monthly: string; price_yearly: string; currency: string;
  max_subscribers: number | null; max_users: number | null; max_companies: number | null; ai_monthly_limit: number | null;
}
interface Site { plans: SitePlan[] | null; phone: string; email: string }
type Page = { kind: 'home' } | { kind: 'for'; biz: Biz } | { kind: 'legal'; doc: 'privacy' | 'terms' };

function readPage(): Page {
  const [p, a] = window.location.hash.replace(/^#\/?/, '').split('?')[0].split('/');
  if (p === 'for' && a && isBiz(a)) return { kind: 'for', biz: a };
  if (p === 'privacy' || p === 'terms') return { kind: 'legal', doc: p };
  return { kind: 'home' };
}

/** A list text: an array in AR / EN / FR, an {0: …, 1: …} object in the machine-translated languages. */
function listOf(v: unknown): string[] {
  if (Array.isArray(v)) return v.map(String);
  if (v && typeof v === 'object') return Object.keys(v).sort((a, b) => Number(a) - Number(b)).map((k) => String((v as Record<string, unknown>)[k]));
  return [];
}

export default function Landing() {
  const { t, i18n } = useTranslation();
  const lang = i18n.language;
  const [page, setPage] = useState<Page>(readPage);
  const [site, setSite] = useState<Site | null>(null);
  const pending = useRef<string | null>(null);

  useEffect(() => {
    // ?lang=en|fr|ar (links from the sitemap and ads) picks the language once
    const q = new URLSearchParams(window.location.search).get('lang');
    if (q && languages.some((l) => l.code === q) && q !== lang) applyLang(q);
    api<Site>('/public/site').then(setSite).catch(() => setSite({ plans: [], phone: '', email: '' }));
    const f = () => setPage(readPage());
    window.addEventListener('hashchange', f);
    return () => window.removeEventListener('hashchange', f);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const pageKey = page.kind === 'for' ? `for/${page.biz}` : page.kind === 'legal' ? page.doc : 'home';
  useEffect(() => {
    document.title = page.kind === 'for' ? `${t(`site.biz.${page.biz}.t`)} · Paxve`
      : page.kind === 'legal' ? `${t(`site.legal.${page.doc}.title`)} · Paxve` : t('site.metaTitle');
  }, [t, lang, page]);
  useEffect(() => {
    if (pending.current && page.kind === 'home') {
      const id = pending.current;
      pending.current = null;
      requestAnimationFrame(() => document.getElementById(id)?.scrollIntoView({ block: 'start' }));
    } else window.scrollTo({ top: 0 });
  }, [pageKey]); // eslint-disable-line react-hooks/exhaustive-deps

  const go = useCallback((id: string) => {
    if (readPage().kind !== 'home') { pending.current = id; window.location.hash = '#/'; return; }
    document.getElementById(id)?.scrollIntoView({ behavior: 'smooth', block: 'start' });
  }, []);

  return (
    <div className="sx" dir={i18n.dir()}>
      <Header go={go} />
      <main>
        {page.kind === 'home' && <Home site={site} go={go} />}
        {page.kind === 'for' && <IndustryPage biz={page.biz} go={go} />}
        {page.kind === 'legal' && <Legal doc={page.doc} />}
      </main>
      <Footer site={site} go={go} />
    </div>
  );
}

// ---------------------------------------------------------------- header

function Header({ go }: { go: (id: string) => void }) {
  const { t } = useTranslation();
  const [mega, setMega] = useState(false);
  const [drawer, setDrawer] = useState(false);
  const ref = useRef<HTMLElement>(null);
  useEffect(() => {
    if (!mega && !drawer) return;
    const esc = (e: KeyboardEvent) => { if (e.key === 'Escape') { setMega(false); setDrawer(false); } };
    const out = (e: MouseEvent) => { if (mega && ref.current && !ref.current.contains(e.target as Node)) setMega(false); };
    window.addEventListener('keydown', esc);
    window.addEventListener('mousedown', out);
    document.body.style.overflow = drawer ? 'hidden' : '';
    return () => { window.removeEventListener('keydown', esc); window.removeEventListener('mousedown', out); document.body.style.overflow = ''; };
  }, [mega, drawer]);
  useEffect(() => {
    const f = () => { setMega(false); setDrawer(false); };
    window.addEventListener('hashchange', f);
    return () => window.removeEventListener('hashchange', f);
  }, []);
  const nav = (id: string) => { setMega(false); setDrawer(false); go(id); };
  const links: [string, string][] = [['product', 'site.v3.nav.product'], ['pricing', 'site.nav.pricing'], ['api', 'site.v3.nav.dev'], ['contact', 'site.nav.contact']];

  return (
    <header className="sx-top" ref={ref}>
      <div className="sx-wrap sx-topin">
        <Brand />
        <nav className="sx-nav" aria-label={t('site.nav.label')}>
          <button type="button" onClick={() => nav(links[0][0])}>{t(links[0][1])}</button>
          <button type="button" aria-expanded={mega} aria-controls="sx-mega" onClick={() => setMega((m) => !m)}>
            {t('site.v3.nav.types')}<Icon name="chevron" size={15} className="chev" />
          </button>
          {links.slice(1).map(([id, k]) => <button key={id} type="button" onClick={() => nav(id)}>{t(k)}</button>)}
        </nav>
        <span className="sx-sp" />
        <LangSelect id="sx-lang-top" />
        <a className="sx-signin hide-m" href="#/login">{t('site.signIn')}</a>
        <a className="sx-btn pri sm" href="#/signup">{t('site.trial')}</a>
        <button type="button" className="sx-burger" aria-label={t('site.nav.label')} aria-expanded={drawer} onClick={() => setDrawer(true)}>
          <Icon name="menu" />
        </button>
      </div>
      {mega && (
        <div className="sx-mega" id="sx-mega">
          <div className="sx-wrap sx-megain">
            {BIZ.map((b) => (
              <a key={b} className="sx-megaitem" href={`#/for/${b}`}>
                <b>{t(`site.biz.${b}.t`)}</b><small>{t(`site.v3.types.${b}`)}</small>
              </a>
            ))}
          </div>
        </div>
      )}
      {drawer && (
        <div className="sx-drawer" role="dialog" aria-modal="true" aria-label={t('site.nav.label')}>
          <div className="sx-wrap sx-topin">
            <Brand />
            <span className="sx-sp" />
            <button type="button" className="sx-burger" aria-label={t('site.v3.nav.close')} onClick={() => setDrawer(false)}><Icon name="close" /></button>
          </div>
          <div className="sx-wrap">
            <div className="sx-dsec">
              {links.map(([id, k]) => <button key={id} type="button" className="sx-dlink" onClick={() => nav(id)}>{t(k)}</button>)}
            </div>
            <div className="sx-dsec">
              <h4>{t('site.v3.nav.types')}</h4>
              <div className="sx-dgrid">
                {BIZ.map((b) => <a key={b} className="sx-dlink" href={`#/for/${b}`}>{t(`site.biz.${b}.t`)}</a>)}
              </div>
            </div>
            <div className="sx-dact">
              <a className="sx-btn pri block" href="#/signup">{t('site.trial')}</a>
              <a className="sx-btn line block" href="#/login">{t('site.signIn')}</a>
              <LangSelect id="sx-lang-drawer" wide />
            </div>
          </div>
        </div>
      )}
    </header>
  );
}

function Brand() {
  return (
    <a className="sx-brand" href="#/" aria-label="Paxve">
      <span className="sx-mark" aria-hidden="true">P</span><span className="nm">Paxve</span>
    </a>
  );
}

function LangSelect({ id, wide }: { id: string; wide?: boolean }) {
  const { i18n } = useTranslation();
  return (
    <span className="sx-langw" style={wide ? { width: '100%' } : undefined}>
      <Icon name="globe" size={16} />
      <select id={id} className="sx-lang" aria-label="Language" value={i18n.language} onChange={(e) => applyLang(e.currentTarget.value)}
        style={wide ? { width: '100%', minHeight: 46 } : undefined}>
        {languages.map((l) => <option key={l.code} value={l.code}>{l.label}</option>)}
      </select>
    </span>
  );
}

// ---------------------------------------------------------------- pictures

/** images fade in once loaded (and show at once when they come from the cache) */
const fadeRef = (el: HTMLImageElement | null) => { if (el && el.complete && el.naturalWidth > 0) el.classList.add('on'); };
const fadeLoad = (e: { currentTarget: HTMLImageElement }) => e.currentTarget.classList.add('on');

function Shot({ n, eager, sizes = '(max-width: 980px) 100vw, 760px' }: { n: string; eager?: boolean; sizes?: string }) {
  const { t } = useTranslation();
  return (
    <div className="sx-shot">
      <img ref={fadeRef} onLoad={fadeLoad} src={`/site/${n}-1440.webp?v=${IMG_V}`}
        srcSet={`/site/${n}-720.webp?v=${IMG_V} 720w, /site/${n}-1440.webp?v=${IMG_V} 1440w, /site/${n}-2400.webp?v=${IMG_V} 2400w`} sizes={sizes}
        width={1440} height={900} alt={t(`site.alt.${SHOT_ALT[n] ?? 'dash'}`)} loading={eager ? 'eager' : 'lazy'} decoding="async"
        {...(eager ? { fetchPriority: 'high' as const } : {})} />
    </div>
  );
}

function Phone({ n, eager, className = '' }: { n: string; eager?: boolean; className?: string }) {
  const { t } = useTranslation();
  return (
    <div className={`sx-phone ${className}`}>
      <img ref={fadeRef} onLoad={fadeLoad} src={`/site/${n}-390.webp?v=${IMG_V}`} srcSet={`/site/${n}-390.webp?v=${IMG_V} 390w, /site/${n}-780.webp?v=${IMG_V} 780w`}
        sizes="260px" width={390} height={844} alt={t(`site.alt.${SHOT_ALT[n] ?? 'field'}`)} loading={eager ? 'eager' : 'lazy'} decoding="async" />
    </div>
  );
}

function Head({ eyebrow, title, sub, more, children, light }: { eyebrow?: string; title: string; sub?: string; more?: [string, string]; children?: ReactNode; light?: boolean }) {
  return (
    <div className={`sx-head ${light ? 'light' : ''}`}>
      {eyebrow && <span className="sx-eyebrow">{eyebrow}</span>}
      <h2 className="sx-h2">{title}</h2>
      {sub && <p className="sx-sub">{sub}</p>}
      {more && <a className="sx-more" href={more[0]}>{more[1]}<Icon name="arrow" size={16} className="flip" /></a>}
      {children}
    </div>
  );
}

const N = ({ children }: { children: ReactNode }) => <span className="sx-n" dir="ltr">{children}</span>;

/** the signature colour band: hero and closing call to action only */
const Band = ({ full }: { full?: boolean }) => <div className={`sx-band ${full ? 'full' : ''}`} aria-hidden="true" />;

// ---------------------------------------------------------------- home

function Home({ site, go }: { site: Site | null; go: (id: string) => void }) {
  return (
    <>
      <Hero go={go} />
      <Types />
      <Product />
      <Field />
      <Api />
      <Compare />
      <Protect />
      <Pricing site={site} />
      <Faq go={go} />
      <Cta go={go} />
      <Contact site={site} />
    </>
  );
}

function Hero({ go }: { go: (id: string) => void }) {
  const { t } = useTranslation();
  const inApp = isNativeApp();
  return (
    <section className="sx-hero">
      <Band />
      <div className="sx-wrap sx-herogrid">
        <div className="sx-herotext">
          <h1>{t('site.v3.hero.h1a')} <em>{t('site.v3.hero.h1b')}</em> {t('site.v3.hero.h1c')}</h1>
          <p className="sx-herolead">{t('site.v3.hero.lead')}</p>
          <div className="sx-heroact">
            <a className="sx-btn pri" href="#/signup">{t('site.trial')}<Icon name="arrow" size={17} className="flip" /></a>
            <button type="button" className="sx-more" onClick={() => go('contact')}>{t('site.v3.hero.demo')}<Icon name="arrow" size={16} className="flip" /></button>
          </div>
          <p className="sx-facts">{(['f1', 'f2', 'f3'] as const).map((k) => <span key={k}>{t(`site.v3.hero.${k}`)}</span>)}</p>
          <div className="sx-applinks">
            {!inApp && <a href="/download/paxve.apk">{t('site.android')}</a>}
            <InstallApp className="" label={t('site.installWeb')} />
          </div>
        </div>
        <HeroCards />
      </div>
    </section>
  );
}

/** Floating cards built from the demo company's real screens (same names and figures as the screenshots). */
function HeroCards() {
  const { t } = useTranslation();
  const c = (k: string) => t(`site.v3.card.${k}`);
  return (
    <div className="sx-cards">
      <div className="sx-cards-base"><Shot n="subs" eager sizes="(max-width: 980px) 100vw, 640px" /></div>
      <div className="sx-card rc">
        <div className="hd"><b>{c('rcT')}</b><span className="sx-tag">USD · LBP</span></div>
        <p className="who">{c('rcCust')}</p>
        <dl>
          <div><dt>{c('rcBill')}</dt><dd><N>84.22 USD</N></dd></div>
          <div><dt>{c('rcPaid')}</dt><dd><N>25.00 USD</N></dd></div>
          <div className="due"><dt>{c('rcRest')}</dt><dd><N>59.22 USD</N></dd></div>
          <div><dt>{c('rcRate')}</dt><dd><N>1 USD = 89,500 LBP</N></dd></div>
        </dl>
        <div className="sx-cardbtn">{c('rcPrint')} <kbd>F9</kbd></div>
      </div>
      <div className="sx-card pos">
        <div className="hd"><b>{c('posT')}</b></div>
        <ul>
          <li><span>{c('posTomato')} <small>× 2</small></span><N>3.00</N></li>
          <li><span>{c('posCheese')}</span><N>9.00</N></li>
          <li><span>{c('posMilk')}</span><N>1.40</N></li>
          <li><span>{c('posSoap')}</span><N>2.50</N></li>
        </ul>
        <div className="tot"><span>{c('posTotal')}</span><N>15.90 USD</N></div>
        <div className="sub"><N>≈ 1,423,050 LBP</N></div>
      </div>
      <div className="sx-card st">
        <div className="hd"><b>{c('stT')}</b></div>
        <table>
          <thead><tr><th>{c('stItem')}</th><th>{c('stQty')}</th><th>{c('stVal')}</th></tr></thead>
          <tbody>
            <tr><td>{c('stBread')}</td><td><N>100 pcs</N></td><td><N>60.00</N></td></tr>
            <tr><td>{c('posCheese')}</td><td><N>15 kg</N></td><td><N>90.00</N></td></tr>
            <tr><td>{c('stWater')}</td><td><N>120 pcs</N></td><td><N>30.00</N></td></tr>
          </tbody>
        </table>
        <div className="tot"><span>{c('stTotal')}</span><N>499.60 USD</N></div>
      </div>
      <div className="sx-card mo">
        <div className="hd"><b>{c('mT')}</b></div>
        <dl>
          <div><dt>{c('mBilled')}</dt><dd><N>1,588.10</N></dd></div>
          <div className="ok"><dt>{c('mCollected')}</dt><dd><N>375.00</N></dd></div>
          <div className="due"><dt>{c('mDue')}</dt><dd><N>1,213.10</N></dd></div>
        </dl>
        <div className="bar" role="img" aria-label={`${c('mRate')} 24%`}><i style={{ width: '24%' }} /></div>
        <small>{c('mRate')} <N>24%</N></small>
      </div>
    </div>
  );
}

function Types() {
  const { t } = useTranslation();
  return (
    <section id="business" className="sx-sec">
      <div className="sx-wrap">
        <Head eyebrow={t('site.v3.types.eyebrow')} title={t('site.v3.types.h')} sub={t('site.v3.types.p')} />
        <div className="sx-types">
          {BIZ.map((b) => (
            <a key={b} href={`#/for/${b}`} className="sx-type">
              <Icon name={b} size={22} strokeWidth={1.4} />
              <b>{t(`site.biz.${b}.t`)}</b>
              <span>{t(`site.v3.types.${b}`)}</span>
              <i aria-hidden="true"><Icon name="arrow" size={16} className="flip" /></i>
            </a>
          ))}
        </div>
      </div>
    </section>
  );
}

function Product() {
  const { t } = useTranslation();
  return (
    <section id="product" className="sx-sec warm">
      <div className="sx-wrap sx-rows">
        {ROWS.map(([k, shot, href], i) => (
          <div key={k} className={`sx-row ${i % 2 ? 'rev' : ''}`}>
            <div className="sx-rowtext">
              <span className="sx-eyebrow">{t(`site.v3.rows.${k}.eyebrow`)}</span>
              <h3>{t(`site.v3.rows.${k}.h`)}</h3>
              <p>{t(`site.v3.rows.${k}.p`)}</p>
              <a className="sx-more" href={href}>{href === '#/signup' ? t('site.v3.tryIt') : t('site.v3.more')}<Icon name="arrow" size={16} className="flip" /></a>
            </div>
            <figure className="sx-fig">
              <Shot n={shot} />
              <figcaption className="sx-cap">{t(`site.v3.rows.${k}.cap`)}</figcaption>
            </figure>
          </div>
        ))}
      </div>
    </section>
  );
}

/** a simplified outline of Lebanon with a collector's route near Beirut */
function LebanonMap() {
  const { t } = useTranslation();
  const city = (k: string, x: number, y: number) => (
    <g key={k}>
      <circle cx={x} cy={y} r="2.6" fill="#8ea3c2" />
      <text x={x + 7} y={y + 4} fontSize="11" fill="#a8afba">{t(`site.v3.field.${k}`)}</text>
    </g>
  );
  const route: [number, number][] = [[88, 214], [94, 204], [101, 197], [108, 191], [115, 184], [109, 176], [101, 182]];
  return (
    <figure className="sx-map">
      <svg viewBox="0 0 300 420" role="img" aria-label={t('site.v3.field.map')} direction="ltr">
        <path d="M18 396 L36 355 L67 286 L81 245 L90 206 L112 185 L117 151 L119 120 L151 74 L175 29 L257 24 L288 120 L243 180 L180 240 L153 312 L135 348 L108 396 Z"
          fill="#1d2633" stroke="#3a4657" strokeWidth="1.2" />
        {city('tripoli', 151, 74)}{city('jounieh', 122, 172)}{city('zahle', 162, 216)}{city('baalbek', 218, 180)}
        {city('saida', 67, 286)}{city('tyre', 36, 355)}
        <circle cx="90" cy="206" r="3" fill="#eceef1" />
        <text x="84" y="232" textAnchor="end" fontSize="11.5" fill="#eceef1">{t('site.v3.field.beirut')}</text>
        <polyline points={route.map((p) => p.join(',')).join(' ')} fill="none" stroke="#f5a524" strokeWidth="1.6" strokeDasharray="3 3" />
        {route.map(([x, y], i) => <circle key={i} cx={x} cy={y} r="3.2" fill={i < 4 ? '#19c3a5' : '#f5a524'} stroke="#15191f" strokeWidth="1" />)}
      </svg>
      <figcaption>{t('site.v3.field.map')}</figcaption>
    </figure>
  );
}

function Field() {
  const { t } = useTranslation();
  return (
    <section id="field" className="sx-sec dark">
      <div className="sx-wrap sx-fieldg">
        <div>
          <Head eyebrow={t('site.v3.field.eyebrow')} title={t('site.v3.field.h')} sub={t('site.v3.field.p')} light />
          <dl className="sx-caps">
            {(['s1', 's2', 's3', 's4'] as const).map((s) => (
              <div key={s}><dt dir="auto">{t(`site.v3.field.${s}t`)}</dt><dd>{t(`site.v3.field.${s}d`)}</dd></div>
            ))}
          </dl>
        </div>
        <div className="sx-fieldvis">
          <Phone n="ph-collect" className="fp" />
          <LebanonMap />
        </div>
      </div>
    </section>
  );
}

function Api() {
  const { t } = useTranslation();
  const code = `# record a payment: 20 USD + 2,500,000 LBP on a 45 USD balance, change from the LBP box
curl -X POST https://paxve.com/api/v1/companies/$CID/payments \\
  -H "Authorization: Bearer $PAXVE_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{"customer_id":"<id>", "currency":"USD", "amount":"45",
       "tenders":[{"method_id":"<Cash USD>","amount":"20"},
                  {"method_id":"<Cash LBP>","amount":"2500000"}],
       "change_method_id":"<Cash LBP>",
       "client_ref":"my-system-000123"}'`;
  return (
    <section id="api" className="sx-sec">
      <div className="sx-wrap sx-apig">
        <div>
          <Head eyebrow={t('site.v3.api.eyebrow')} title={t('site.v3.api.h')} sub={t('site.v3.api.p')} more={['/api/v1/openapi.yaml', t('site.v3.api.docs')]} />
          <h4 className="sx-evh">{t('site.v3.api.events')}</h4>
          <ul className="sx-events">{WEBHOOK_EVENTS.map((e) => <li key={e} dir="ltr"><code>{e}</code></li>)}</ul>
        </div>
        <div className="sx-code" dir="ltr">
          <div className="bar"><span>POST /companies/{'{cid}'}/payments</span><span>curl</span></div>
          <pre><code>{code}</code></pre>
          <div className="bar foot"><span>X-Paxve-Signature: t=…,v1=HMAC-SHA256</span></div>
        </div>
      </div>
    </section>
  );
}

function Mark({ v }: { v: string }) {
  const { t } = useTranslation();
  const label = v === 'y' ? t('site.cmp.yes') : v === 'p' ? t('site.cmp.part') : t('site.cmp.no');
  if (v === 'y') return <span className="sx-yes" title={label}><Icon name="check" size={17} strokeWidth={2} /><span className="sx-hp">{label}</span></span>;
  if (v === 'p') return <span className="sx-part">{label}</span>;
  return <span className="sx-no" title={label}>—<span className="sx-hp">{label}</span></span>;
}

function Compare() {
  const { t } = useTranslation();
  return (
    <section id="why" className="sx-sec warm">
      <div className="sx-wrap">
        <Head eyebrow={t('site.v3.cmp.eyebrow')} title={t('site.cmp.title')} sub={t('site.cmp.sub')} />
        <div className="sx-tw">
          <table className="sx-table">
            <thead><tr><th scope="col"><span className="sx-hp">{t('site.px.feature')}</span></th><th scope="col" className="us">Paxve</th>
              <th scope="col">{t('site.cmp.sheets')}</th><th scope="col">{t('site.cmp.old')}</th></tr></thead>
            <tbody>
              {CMP.map((k) => (
                <tr key={k}>
                  <th scope="row">{t(`site.cmp.r.${k}`)}</th>
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
  );
}

function Protect() {
  const { t } = useTranslation();
  const items: [string, string][] = [
    ...(['i1', 'i2', 'i3', 'i4', 'i5'] as const).map((k): [string, string] => [t(`site.trust.${k}.t`), t(`site.trust.${k}.d`)]),
    [t('site.v3.protect.i6t'), t('site.v3.protect.i6d')],
  ];
  return (
    <section id="trust" className="sx-sec">
      <div className="sx-wrap">
        <Head eyebrow={t('site.v3.protect.eyebrow')} title={t('site.v3.protect.h')} sub={t('site.trust.sub')} />
        <div className="sx-protect">
          {items.map(([h, d]) => <div key={h}><h3>{h}</h3><p>{d}</p></div>)}
        </div>
      </div>
    </section>
  );
}

function Pricing({ site }: { site: Site | null }) {
  const { t, i18n } = useTranslation();
  const lang = i18n.language;
  const [yearly, setYearly] = useState(true);
  const plans = site?.plans ?? [];
  const planName = (p: SitePlan) => (lang === 'ar' && p.name_ar ? p.name_ar : lang === 'fr' && p.name_fr ? p.name_fr : p.name_en);
  const lim = (n: number | null) => (n == null || n === 0 ? t('site.price.unlimited') : n.toLocaleString('en'));
  const money = (p: SitePlan) => {
    const v = yearly ? Number(p.price_yearly) : Number(p.price_monthly);
    return `${p.currency === 'USD' ? '$' : ''}${v.toLocaleString('en')}${p.currency !== 'USD' ? ` ${p.currency}` : ''}`;
  };
  const hot = (p: SitePlan, i: number) => p.code === 'business' || (!plans.some((x) => x.code === 'business') && i === Math.floor(plans.length / 2));
  const rows: { k: string; v: (p: SitePlan) => ReactNode }[] = [
    { k: 'subsT', v: (p) => lim(p.max_subscribers) }, { k: 'usersT', v: (p) => lim(p.max_users) }, { k: 'companiesT', v: (p) => lim(p.max_companies) },
    { k: 'aiT', v: (p) => lim(p.ai_monthly_limit) }, { k: 'appsT', v: () => <Mark v="y" /> }, { k: 'phoneT', v: () => <Mark v="y" /> },
    { k: 'backupT', v: () => <Mark v="y" /> }, { k: 'supportT', v: () => <Mark v="y" /> },
  ];
  return (
    <section id="pricing" className="sx-sec warm">
      <div className="sx-wrap">
        <Head eyebrow={t('site.v3.price.eyebrow')} title={t('site.v3.price.h')} sub={t('site.v3.price.p')}>
          <div className="sx-cycle" role="group" aria-label={t('site.price.cycle')}>
            <button type="button" aria-pressed={!yearly} onClick={() => setYearly(false)}>{t('site.price.monthly')}</button>
            <button type="button" aria-pressed={yearly} onClick={() => setYearly(true)}>{t('site.px.yearly')}<span className="save">{t('site.px.save')}</span></button>
          </div>
        </Head>
        {plans.length === 0 ? <p className="sx-pnote">{site ? t('site.price.ask') : '…'}</p> : (
          <div className="sx-tw">
            <table className="sx-table sx-ptable">
              <thead>
                <tr>
                  <th scope="col" className="sx-plan-h"><span className="sx-hp">{t('site.px.feature')}</span></th>
                  {plans.map((p, i) => (
                    <th key={p.code} scope="col" className={`sx-plan-h ${hot(p, i) ? 'hot' : ''}`}>
                      <span className="tag">{hot(p, i) ? t('site.price.popular') : ' '}</span>
                      <span className="pn">{planName(p)}</span>
                      <span className="pr" dir="ltr">{money(p)}<small>/ {yearly ? t('site.price.year') : t('site.price.month')}</small></span>
                      <a className={`sx-btn ${hot(p, i) ? 'pri' : 'line'} sm block`} href="#/signup">{t('site.trial')}</a>
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {rows.map((r) => (
                  <tr key={r.k}><th scope="row">{t(`site.px.${r.k}`)}</th>{plans.map((p, i) => <td key={p.code} className={hot(p, i) ? 'hot' : ''}>{r.v(p)}</td>)}</tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        <p className="sx-pnote">{t('site.price.note')}</p>
      </div>
    </section>
  );
}

function Faq({ go }: { go: (id: string) => void }) {
  const { t } = useTranslation();
  return (
    <section id="faq" className="sx-sec">
      <div className="sx-wrap sx-faqg">
        <div>
          <Head eyebrow={t('site.v3.faq.eyebrow')} title={t('site.faq.title')} />
          <button type="button" className="sx-more" style={{ marginTop: 18 }} onClick={() => go('contact')}>{t('site.nav.contact')}<Icon name="arrow" size={16} className="flip" /></button>
        </div>
        <div>
          {FAQ.map((q) => (
            <details key={q} className="sx-qa">
              <summary>{t(`site.faq.${q}.q`)}<span className="pm" aria-hidden="true">+</span></summary>
              <p>{t(`site.faq.${q}.a`)}</p>
            </details>
          ))}
        </div>
      </div>
    </section>
  );
}

function Cta({ go }: { go: (id: string) => void }) {
  const { t } = useTranslation();
  return (
    <section className="sx-cta">
      <Band full />
      <div className="sx-wrap sx-ctain">
        <div>
          <h2>{t('site.v3.cta.h')}</h2>
          <p>{t('site.v3.cta.p')}</p>
        </div>
        <div className="sx-heroact">
          <a className="sx-btn white" href="#/signup">{t('site.trial')}<Icon name="arrow" size={17} className="flip" /></a>
          <button type="button" className="sx-more onband" onClick={() => go('contact')}>{t('site.v3.hero.demo')}<Icon name="arrow" size={16} className="flip" /></button>
        </div>
      </div>
    </section>
  );
}

function Contact({ site }: { site: Site | null }) {
  const { t } = useTranslation();
  return (
    <section id="contact" className="sx-sec">
      <div className="sx-wrap sx-contact">
        <div>
          <Head eyebrow={t('site.v3.contact.eyebrow')} title={t('site.contact.title')} sub={t('site.contact.sub')} />
          {site?.phone && (
            <p className="sx-cline"><span dir="ltr">{site.phone}</span> · <a href={`https://wa.me/${site.phone.replace(/[^\d]/g, '')}`} target="_blank" rel="noreferrer">WhatsApp</a></p>
          )}
          {site?.email && <p className="sx-cline"><span dir="ltr">{site.email}</span></p>}
          <ul className="sx-facts2">{(['c1', 'c2', 'c3'] as const).map((k) => <li key={k}>{t(`site.v3.contact.${k}`)}</li>)}</ul>
        </div>
        <DemoForm />
      </div>
    </section>
  );
}

function DemoForm({ biz }: { biz?: Biz }) {
  const { t, i18n } = useTranslation();
  const [f, setF] = useState({ name: '', company: '', phone: '', email: '', business: biz ?? 'generator', city: '', message: '', website: '' });
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
  if (state === 'done') {
    return <div className="sx-form done" role="status"><h3>{t('site.form.thanks')}</h3><p style={{ color: 'var(--ink2)', marginTop: 6 }}>{t('site.form.thanksSub')}</p></div>;
  }
  return (
    <form className="sx-form" onSubmit={(e) => void submit(e)}>
      <h3>{t('site.form.title')}</h3>
      <label htmlFor="sx-f-name">{t('site.form.name')} *<input id="sx-f-name" required maxLength={120} value={f.name} onChange={set('name')} autoComplete="name" /></label>
      <label htmlFor="sx-f-company">{t('site.form.company')}<input id="sx-f-company" maxLength={160} value={f.company} onChange={set('company')} autoComplete="organization" /></label>
      <div className="sx-2">
        <label htmlFor="sx-f-phone">{t('site.form.phone')}<input id="sx-f-phone" dir="ltr" inputMode="tel" maxLength={40} value={f.phone} onChange={set('phone')} autoComplete="tel" /></label>
        <label htmlFor="sx-f-email">{t('site.form.email')}<input id="sx-f-email" dir="ltr" type="email" maxLength={200} value={f.email} onChange={set('email')} autoComplete="email" /></label>
      </div>
      <div className="sx-2">
        <label htmlFor="sx-f-biz">{t('site.form.business')}
          <select id="sx-f-biz" value={f.business} onChange={set('business')}>
            {BIZ.map((b) => <option key={b} value={b}>{t(`site.biz.${b}.t`)}</option>)}
            <option value="other">{t('site.form.other')}</option>
          </select>
        </label>
        <label htmlFor="sx-f-city">{t('site.form.city')}<input id="sx-f-city" maxLength={80} value={f.city} onChange={set('city')} /></label>
      </div>
      <label htmlFor="sx-f-msg">{t('site.form.message')}<textarea id="sx-f-msg" rows={3} maxLength={2000} value={f.message} onChange={set('message')} /></label>
      {/* honeypot: hidden from people, bots fill it */}
      <label className="sx-hp" aria-hidden="true">Website<input tabIndex={-1} autoComplete="off" value={f.website} onChange={set('website')} /></label>
      <small>{t('site.form.hint')}</small>
      {err && <p className="sx-err">{err}</p>}
      <button className="sx-btn pri block" type="submit" disabled={!ok || state === 'busy'}>{state === 'busy' ? '…' : t('site.form.send')}</button>
    </form>
  );
}

// ---------------------------------------------------------------- one page per business type

function IndustryPage({ biz, go }: { biz: Biz; go: (id: string) => void }) {
  const { t, i18n } = useTranslation();
  const name = t(`site.biz.${biz}.t`);
  const [shot, shot2] = BIZ_SHOT[biz];
  return (
    <>
      <section className="sx-hero sx-ihero">
        <Band />
        <div className="sx-wrap sx-herogrid">
          <div className="sx-herotext">
            <a className="sx-crumb" href="#/" onClick={(e) => { e.preventDefault(); go('business'); }}>
              <Icon name="arrow" size={15} style={{ transform: i18n.dir() === 'rtl' ? 'none' : 'scaleX(-1)' }} />{t('site.ind.back')}
            </a>
            <span className="sx-eyebrow" style={{ marginTop: 26 }}><Icon name={biz} size={18} strokeWidth={1.5} />{name}</span>
            <h1>{t(`site.ind.${biz}.h`)}</h1>
            <p className="sx-herolead">{t(`site.v3.types.${biz}`)}</p>
            <div className="sx-heroact">
              <a className="sx-btn pri" href="#/signup">{t('site.bizTry', { name })}<Icon name="arrow" size={17} className="flip" /></a>
              <button type="button" className="sx-more" onClick={() => document.getElementById('ind-demo')?.scrollIntoView({ behavior: 'smooth' })}>{t('site.v3.hero.demo')}<Icon name="arrow" size={16} className="flip" /></button>
            </div>
          </div>
          <div className="sx-ishot"><Shot n={shot} eager sizes="(max-width: 980px) 100vw, 640px" /></div>
        </div>
      </section>

      <section className="sx-sec">
        <div className="sx-wrap narrow">
          <Head eyebrow={name} title={t('site.ind.pains')} sub={t('site.ind.painsSub')} />
          <div className="sx-tw sx-pains">
            <table className="sx-table">
              <thead><tr><th scope="col">{t('site.ind.problem')}</th><th scope="col">{t('site.ind.solution')}</th></tr></thead>
              <tbody>
                {(['1', '2', '3'] as const).map((n) => (
                  <tr key={n}><td>{t(`site.ind.${biz}.p${n}`)}</td><td>{t(`site.ind.${biz}.s${n}`)}</td></tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      </section>

      <section className="sx-sec warm">
        <div className="sx-wrap sx-row">
          <div className="sx-rowtext">
            <span className="sx-eyebrow">{t('site.ind.kicker')}</span>
            <h3>{t('site.ind.features', { name })}</h3>
            <ul className="sx-facts2">{listOf(t(`site.biz.${biz}.f`, { returnObjects: true })).map((x) => <li key={x}>{x}</li>)}</ul>
          </div>
          <figure className="sx-fig"><Shot n={shot2} /><figcaption className="sx-cap">{t(`site.alt.${SHOT_ALT[shot2]}`)}</figcaption></figure>
        </div>
      </section>

      <section className="sx-sec">
        <div className="sx-wrap sx-contact" id="ind-demo">
          <div>
            <Head eyebrow={t('site.v3.contact.eyebrow')} title={t('site.ind.ctaTitle', { name })} sub={t('site.contact.sub')} />
            <h4 className="sx-evh" style={{ marginTop: 32 }}>{t('site.ind.others')}</h4>
            <div className="sx-others">
              {BIZ.filter((b) => b !== biz).map((b) => <a key={b} href={`#/for/${b}`}>{t(`site.biz.${b}.t`)}</a>)}
            </div>
          </div>
          <DemoForm key={biz} biz={biz} />
        </div>
      </section>
    </>
  );
}

// ---------------------------------------------------------------- legal (texts drafted for the owner to review)

function Legal({ doc }: { doc: 'privacy' | 'terms' }) {
  const { t } = useTranslation();
  return (
    <div className="sx-wrap narrow sx-legal">
      <span className="sx-eyebrow">{t('site.legal.kicker')}</span>
      <h1>{t(`site.legal.${doc}.title`)}</h1>
      <p className="upd">{t('site.legal.updated')}</p>
      {LEGAL_SECTIONS.map((s) => (
        <section key={s}>
          <h2>{t(`site.legal.${doc}.${s}.h`)}</h2>
          <p>{t(`site.legal.${doc}.${s}.p`)}</p>
        </section>
      ))}
      <p style={{ marginTop: 28 }}><a className="sx-more" href={doc === 'privacy' ? '#/terms' : '#/privacy'}>{t(`site.legal.${doc === 'privacy' ? 'terms' : 'privacy'}.title`)}<Icon name="arrow" size={16} className="flip" /></a></p>
    </div>
  );
}

// ---------------------------------------------------------------- footer

function Footer({ site, go }: { site: Site | null; go: (id: string) => void }) {
  const { t } = useTranslation();
  const inApp = isNativeApp();
  const prod: [string, string][] = [['collect', 'product'], ['pos', 'product'], ['stock', 'product'], ['mfg', 'product'], ['acc', 'product'], ['field', 'field']];
  return (
    <footer className="sx-foot">
      <div className="sx-wrap">
        <div className="sx-footg">
          <div className="sx-footbrand">
            <Brand />
            <p>{t('site.foot.tagline')}</p>
            {site?.phone && <p dir="ltr" style={{ textAlign: 'start' }}>{site.phone}</p>}
            {site?.email && <p dir="ltr" style={{ textAlign: 'start' }}>{site.email}</p>}
            <LangSelect id="sx-lang-foot" />
          </div>
          <div>
            <h4>{t('site.v3.foot.products')}</h4>
            <ul>{prod.map(([k, id]) => <li key={k}><button type="button" onClick={() => go(id)}>{t(`site.v3.foot.${k}`)}</button></li>)}
              <li><button type="button" onClick={() => go('pricing')}>{t('site.nav.pricing')}</button></li></ul>
          </div>
          <div>
            <h4>{t('site.v3.nav.types')}</h4>
            <ul>{BIZ.slice(0, 6).map((b) => <li key={b}><a href={`#/for/${b}`}>{t(`site.biz.${b}.t`)}</a></li>)}</ul>
          </div>
          <div>
            <h4 aria-hidden="true">&nbsp;</h4>
            <ul>{BIZ.slice(6).map((b) => <li key={b}><a href={`#/for/${b}`}>{t(`site.biz.${b}.t`)}</a></li>)}</ul>
          </div>
          <div>
            <h4>{t('site.v3.foot.resources')}</h4>
            <ul>
              <li><a href="/api/v1/openapi.yaml">API</a></li>
              <li><button type="button" onClick={() => go('api')}>Webhooks</button></li>
              {!inApp && <li><a href="/download/paxve.apk">{t('site.android')}</a></li>}
              <li><button type="button" onClick={() => go('faq')}>{t('site.nav.faq')}</button></li>
              <li><button type="button" onClick={() => go('contact')}>{t('site.nav.contact')}</button></li>
              <li><a href="#/login">{t('site.signIn')}</a></li>
            </ul>
          </div>
        </div>
        <div className="sx-footbot">
          <span>© {new Date().getFullYear()} Paxve · {t('site.foot.rights')}</span>
          <a href="#/privacy">{t('site.legal.privacy.title')}</a>
          <a href="#/terms">{t('site.legal.terms.title')}</a>
          <span className="sx-sp" />
          <span>{t('site.foot.made')}</span>
        </div>
      </div>
    </footer>
  );
}
