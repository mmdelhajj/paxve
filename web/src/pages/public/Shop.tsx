import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, ApiError } from '../../api';
import { applyLang } from '../../i18n';
import { errorText, formatAmount, formatDateTime } from '../../util';

// The company's public online store (#/shop/<slug>, #/shop/<slug>/p/<item>, #/shop/<slug>/cart) and the order
// tracking page (#/order/<token>). Mobile-first, Arabic-first, in the company's colour. Prices and the cart total
// always come from the server (the same rules as the sales order).

type I18n = Record<string, string>;
interface Item {
  id: string; code: string; name: string; name_i18n: I18n; unit: string; kind: string; has_image: boolean; category_id: string | null;
  is_template: boolean; variant_values: Record<string, string>; brand: string; description?: string; price: string; price_local: string | null;
  from_price: boolean; in_stock: boolean; stock: string | null; variants?: Item[];
}
interface Zone { id: string; name: string; fee: string; free_from: string | null; eta: string }
interface Category { id: string; name: string; name_i18n: I18n; items: number }
interface Home {
  shop: { name_en: string; name_ar: string; logo: string; default_locale: string; public_slug: string; title: string; tagline: string; banner: string; color: string;
    pay_note: string; whatsapp: string; min_order: string; terms: string; careers: boolean };
  currency: string; local_currency: string; local_rate: string | null; decimals: number; payments: string[]; zones: Zone[]; categories: Category[]; featured: Item[];
}
interface CartLine { item_id: string; quantity: number; name: string; name_i18n: I18n; variant: Record<string, string>; price: string; has_image: boolean }
interface Priced {
  lines: { item_id: string; name: string; quantity: string; unit_price: string; discount: string; amount: string; tax: string; promos: string[] }[];
  problems: { item_id: string; code: string; available?: string }[]; currency: string; subtotal: string; discount: string; tax: string; fee: string;
  total: string; total_local: string | null; coupon_valid: boolean | null; promotions: { name: string; discount: string }[]; min_order: string;
}

const isAr = (l: string) => l.startsWith('ar') || l === 'fa' || l === 'ur' || l === 'ckb';
const nm = (x: { name: string; name_i18n?: I18n }, lang: string) => (x.name_i18n && x.name_i18n[lang]) || x.name;
const variantText = (v: Record<string, string> | undefined) => (v ? Object.values(v).join(' · ') : '');

function readHash(): string[] {
  return window.location.hash.replace(/^#\/?/, '').split('/');
}

function useCart(slug: string): [CartLine[], (c: CartLine[]) => void] {
  const key = `paxve.cart.${slug}`;
  const [cart, setCart] = useState<CartLine[]>(() => {
    try { return JSON.parse(localStorage.getItem(key) ?? '[]') as CartLine[]; } catch { return []; }
  });
  const save = useCallback((c: CartLine[]) => {
    setCart(c);
    try { localStorage.setItem(key, JSON.stringify(c)); } catch { /* private mode */ }
  }, [key]);
  return [cart, save];
}

const css = `
.psh{min-height:100vh;background:#f4f6f9;font-family:inherit;color:#0f1b2d}
.psh-top{position:sticky;top:0;z-index:5;display:flex;align-items:center;gap:10px;padding:10px 14px;color:#fff}
.psh-top img{height:34px;width:34px;object-fit:contain;background:#fff;border-radius:8px}
.psh-top b{font-size:17px}
.psh-sp{flex:1}
.psh-btn{font:inherit;border:1px solid #d5dce6;background:#fff;color:#0f1b2d;border-radius:10px;padding:8px 14px;min-height:42px;cursor:pointer;font-weight:600}
.psh-btn.main{color:#fff;border:0}
.psh-btn:disabled{opacity:.5;cursor:default}
.psh-icon{font:inherit;background:rgba(255,255,255,.18);border:0;color:#fff;border-radius:10px;min-width:42px;min-height:42px;cursor:pointer;font-size:15px;position:relative}
.psh-badge{position:absolute;top:-4px;inset-inline-end:-4px;background:#e11d48;color:#fff;border-radius:999px;font-size:11px;min-width:18px;padding:1px 5px}
.psh-wrap{max-width:1100px;margin:0 auto;padding:12px}
.psh-banner{width:100%;max-height:260px;object-fit:cover;border-radius:14px;display:block}
.psh-chips{display:flex;gap:8px;overflow-x:auto;padding:10px 0}
.psh-chip{flex:none;border:1px solid #d5dce6;background:#fff;border-radius:999px;padding:6px 14px;font:inherit;cursor:pointer;min-height:36px}
.psh-chip[aria-pressed=true]{color:#fff;border-color:transparent}
.psh-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:10px}
@media(min-width:700px){.psh-grid{grid-template-columns:repeat(4,minmax(0,1fr))}}
.psh-card{background:#fff;border:1px solid #e3e8ef;border-radius:14px;overflow:hidden;display:flex;flex-direction:column}
.psh-img{aspect-ratio:1/1;background:#eef1f5;display:grid;place-items:center;font-size:34px;color:#94a3b8;font-weight:700;overflow:hidden}
.psh-img img{width:100%;height:100%;object-fit:cover}
.psh-card .b{padding:8px 10px;display:flex;flex-direction:column;gap:4px;flex:1}
.psh-name{font-weight:600;font-size:14px;line-height:1.35}
.psh-price{font-weight:700;direction:ltr;unicode-bidi:isolate}
.psh-local{color:#5b6b7f;font-size:12px;direction:ltr;unicode-bidi:isolate}
.psh-out{color:#b42318;font-size:12px;font-weight:600}
.psh-box{background:#fff;border:1px solid #e3e8ef;border-radius:14px;padding:14px;display:flex;flex-direction:column;gap:10px}
.psh-fld{display:flex;flex-direction:column;gap:4px;font-size:13px;font-weight:600;color:#3b4a5e}
.psh-fld input,.psh-fld textarea,.psh-fld select{font:inherit;font-size:15px;padding:10px;border:1px solid #cbd5e1;border-radius:10px;font-weight:400;color:#0f1b2d;background:#fff}
.psh-row{display:flex;justify-content:space-between;gap:10px}
.psh-qty{display:inline-flex;align-items:center;border:1px solid #cbd5e1;border-radius:10px;overflow:hidden}
.psh-qty button{font:inherit;border:0;background:#f4f6f9;min-width:38px;min-height:38px;cursor:pointer;font-size:18px}
.psh-qty span{min-width:34px;text-align:center}
.psh-err{background:#fdecea;color:#b42318;border-radius:10px;padding:8px 12px}
.psh-ok{background:#e6f4ec;color:#0f7b4b;border-radius:10px;padding:10px 12px}
.psh-steps{display:flex;gap:4px}
.psh-steps div{flex:1;text-align:center;font-size:12px;padding-top:8px;border-top:4px solid #d5dce6;color:#5b6b7f}
.psh-steps div.on{color:#0f1b2d;font-weight:700}
.psh-opt{display:flex;gap:8px;flex-wrap:wrap}
.psh-opt button{font:inherit;border:1px solid #cbd5e1;background:#fff;border-radius:10px;padding:8px 12px;min-height:40px;cursor:pointer}
.psh-opt button[aria-pressed=true]{border-width:2px}
.psh-opt button:disabled{opacity:.4;text-decoration:line-through}
.psh-hp{position:absolute;width:1px;height:1px;padding:0;margin:-1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap;border:0}
`;

function Money({ v, cur, local, localCur, dec }: { v: string; cur: string; local?: string | null; localCur?: string; dec: number }) {
  return (
    <span style={{ display: 'inline-flex', flexDirection: 'column' }}>
      <span className="psh-price">{formatAmount(v, dec)} {cur}</span>
      {local != null && localCur && <span className="psh-local">≈ {formatAmount(local, 0)} {localCur}</span>}
    </span>
  );
}

function Pic({ slug, item, big }: { slug: string; item: { id: string; has_image: boolean; name: string }; big?: boolean }) {
  const [bad, setBad] = useState(false);
  return (
    <div className="psh-img" style={big ? { borderRadius: 14 } : undefined}>
      {item.has_image && !bad ? <img loading="lazy" alt="" src={`/api/v1/shop/${slug}/items/${item.id}/image`} onError={() => setBad(true)} /> : item.name.slice(0, 1).toUpperCase()}
    </div>
  );
}

export default function Shop() {
  const { t, i18n } = useTranslation();
  const [route, setRoute] = useState(readHash);
  useEffect(() => {
    const f = () => { setRoute(readHash()); window.scrollTo(0, 0); };
    window.addEventListener('hashchange', f);
    return () => window.removeEventListener('hashchange', f);
  }, []);
  const slug = route[1] ?? '';
  const view = route[2] ?? '';
  const [home, setHome] = useState<Home | null | undefined>(undefined);
  const [closed, setClosed] = useState(false);
  const [cart, setCart] = useCart(slug);
  const lang = i18n.language;

  useEffect(() => {
    api<Home>(`/shop/${slug}`).then((h) => {
      setHome(h);
      if (h.shop.default_locale && !localStorage.getItem('paxve.lang')) applyLang(h.shop.default_locale);
      document.title = h.shop.title || (isAr(lang) && h.shop.name_ar ? h.shop.name_ar : h.shop.name_en);
    }).catch((e) => { setClosed(e instanceof ApiError && e.code === 'shop_closed'); setHome(null); });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [slug]);

  if (home === undefined) return <div className="psh"><style>{css}</style><div className="psh-wrap">…</div></div>;
  if (home === null) return <div className="psh"><style>{css}</style><div className="psh-wrap"><div className="psh-box">{closed ? t('pub.shop.closed') : t('pub.shop.notFound')}</div></div></div>;

  const color = home.shop.color || '#0f766e';
  const company = isAr(lang) && home.shop.name_ar ? home.shop.name_ar : home.shop.name_en;
  const count = cart.reduce((s, l) => s + l.quantity, 0);
  const go = (p: string) => { window.location.hash = `#/shop/${slug}${p}`; };
  const add = (it: Item, qty = 1) => {
    const ex = cart.find((l) => l.item_id === it.id);
    if (ex) setCart(cart.map((l) => (l.item_id === it.id ? { ...l, quantity: l.quantity + qty } : l)));
    else setCart([...cart, { item_id: it.id, quantity: qty, name: it.name, name_i18n: it.name_i18n, variant: it.variant_values, price: it.price, has_image: it.has_image }]);
  };
  const shareShop = () => {
    const url = `${window.location.origin}/#/shop/${slug}`;
    const text = `${home.shop.title || company}\n${url}`;
    if (navigator.share) void navigator.share({ title: company, url }).catch(() => undefined);
    else window.open(`https://wa.me/?text=${encodeURIComponent(text)}`, '_blank', 'noopener');
  };

  return (
    <div className="psh" dir={i18n.dir()}>
      <style>{css}</style>
      <header className="psh-top" style={{ background: color }}>
        <button className="psh-icon" aria-label={t('pub.shop.home')} onClick={() => go('')} style={{ background: 'transparent', padding: 0 }}>
          {home.shop.logo ? <img src={home.shop.logo} alt="" /> : '🏪'}
        </button>
        <b style={{ cursor: 'pointer' }} onClick={() => go('')}>{home.shop.title || company}</b>
        <span className="psh-sp" />
        <LangPick />
        <button className="psh-icon" aria-label={t('pub.shop.share')} onClick={shareShop}>⤴</button>
        <button className="psh-icon" aria-label={t('pub.shop.cart')} onClick={() => go('/cart')}>🛒{count > 0 && <span className="psh-badge">{count}</span>}</button>
      </header>
      <div className="psh-wrap">
        {view === 'p' && route[3] ? <Product slug={slug} id={route[3]} home={home} color={color} onAdd={(it, q) => { add(it, q); go('/cart'); }} /> :
          view === 'cart' ? <Cart slug={slug} home={home} color={color} cart={cart} setCart={setCart} /> :
            <Catalog slug={slug} home={home} color={color} onAdd={add} onOpen={(id) => go(`/p/${id}`)} />}
        <footer style={{ textAlign: 'center', color: '#5b6b7f', fontSize: 12, padding: '24px 0' }}>
          {home.shop.careers && <a href={`#/careers/${slug}`} style={{ color }}>{t('pub.careers.title')}</a>} · {company} · Paxve
        </footer>
      </div>
    </div>
  );
}

function LangPick() {
  const { i18n } = useTranslation();
  return (
    <select aria-label="Language" value={i18n.language.slice(0, 2)} onChange={(e) => applyLang(e.currentTarget.value)}
      style={{ font: 'inherit', background: 'rgba(255,255,255,.18)', color: '#fff', border: 0, borderRadius: 10, minHeight: 42, padding: '0 6px' }}>
      <option value="ar" style={{ color: '#000' }}>ع</option><option value="en" style={{ color: '#000' }}>EN</option><option value="fr" style={{ color: '#000' }}>FR</option>
    </select>
  );
}

function Catalog({ slug, home, color, onAdd, onOpen }: { slug: string; home: Home; color: string; onAdd: (it: Item) => void; onOpen: (id: string) => void }) {
  const { t, i18n } = useTranslation();
  const lang = i18n.language;
  const [cat, setCat] = useState('');
  const [q, setQ] = useState('');
  const [items, setItems] = useState<Item[]>([]);
  const [more, setMore] = useState(false);
  const [loading, setLoading] = useState(false);
  const seq = useRef(0);
  const load = useCallback((offset: number) => {
    const n = ++seq.current;
    setLoading(true);
    api<{ items: Item[]; more: boolean }>(`/shop/${slug}/items?category=${cat}&q=${encodeURIComponent(q.trim())}&offset=${offset}`)
      .then((r) => { if (n !== seq.current) return; setItems((prev) => (offset ? [...prev, ...r.items] : r.items)); setMore(r.more); })
      .catch(() => undefined).finally(() => setLoading(false));
  }, [slug, cat, q]);
  useEffect(() => { const h = setTimeout(() => load(0), q ? 300 : 0); return () => clearTimeout(h); }, [load, q]);
  const dec = home.decimals;
  const card = (it: Item) => (
    <div key={it.id} className="psh-card">
      <button onClick={() => onOpen(it.id)} style={{ all: 'unset', cursor: 'pointer' }} aria-label={nm(it, lang)}><Pic slug={slug} item={it} /></button>
      <div className="b">
        <span className="psh-name">{nm(it, lang)}</span>
        <span>{it.from_price && <small>{t('pub.shop.from')} </small>}<Money v={it.price} cur={home.currency} local={it.price_local} localCur={home.local_currency} dec={dec} /></span>
        {!it.in_stock && <span className="psh-out">{t('pub.shop.out')}</span>}
        {it.in_stock && it.stock && <small style={{ color: '#a15c00' }}>{t('pub.shop.left', { n: Number(it.stock) })}</small>}
        <span style={{ flex: 1 }} />
        <button className="psh-btn main" style={{ background: color }} disabled={!it.in_stock} onClick={() => (it.is_template ? onOpen(it.id) : onAdd(it))}>
          {it.is_template ? t('pub.shop.choose') : t('pub.shop.add')}
        </button>
      </div>
    </div>
  );
  return (
    <>
      {home.shop.banner && <img className="psh-banner" src={home.shop.banner} alt="" />}
      {home.shop.tagline && <p style={{ margin: '10px 2px', color: '#3b4a5e' }}>{home.shop.tagline}</p>}
      <input aria-label={t('pub.shop.search')} placeholder={t('pub.shop.search')} value={q} onChange={(e) => setQ(e.currentTarget.value)}
        style={{ width: '100%', boxSizing: 'border-box', font: 'inherit', fontSize: 16, padding: 12, borderRadius: 12, border: `2px solid ${color}`, marginTop: 10 }} />
      <div className="psh-chips" role="group" aria-label={t('pub.shop.categories')}>
        <button className="psh-chip" aria-pressed={cat === ''} style={cat === '' ? { background: color } : undefined} onClick={() => setCat('')}>{t('pub.shop.all')}</button>
        {home.categories.filter((c) => c.items > 0).map((c) => (
          <button key={c.id} className="psh-chip" aria-pressed={cat === c.id} style={cat === c.id ? { background: color } : undefined} onClick={() => setCat(c.id)}>{nm(c, lang)}</button>))}
      </div>
      {!cat && !q && home.featured.length > 0 && <>
        <h3 style={{ margin: '6px 2px 8px' }}>{t('pub.shop.featured')}</h3>
        <div className="psh-grid" style={{ marginBottom: 14 }}>{home.featured.map(card)}</div>
        <h3 style={{ margin: '6px 2px 8px' }}>{t('pub.shop.allProducts')}</h3>
      </>}
      <div className="psh-grid">{items.map(card)}</div>
      {!loading && items.length === 0 && <div className="psh-box" style={{ marginTop: 10 }}>{t('pub.shop.nothing')}</div>}
      {more && <button className="psh-btn" style={{ width: '100%', marginTop: 12 }} disabled={loading} onClick={() => load(items.length)}>{t('pub.shop.more')}</button>}
    </>
  );
}

function Product({ slug, id, home, color, onAdd }: { slug: string; id: string; home: Home; color: string; onAdd: (it: Item, qty: number) => void }) {
  const { t, i18n } = useTranslation();
  const lang = i18n.language;
  const [d, setD] = useState<{ item: Item; attributes: { name: string; values: string[] }[] } | null | undefined>(undefined);
  const [pick, setPick] = useState<Record<string, string>>({});
  const [qty, setQty] = useState(1);
  useEffect(() => { api<{ item: Item; attributes: { name: string; values: string[] }[] }>(`/shop/${slug}/items/${id}`).then(setD).catch(() => setD(null)); }, [slug, id]);
  if (d === undefined) return <div className="psh-box">…</div>;
  if (d === null) return <div className="psh-box">{t('pub.shop.notFound')}</div>;
  const it = d.item;
  const variant = it.is_template ? it.variants?.find((v) => Object.entries(pick).length === (d.attributes ?? []).length && Object.entries(pick).every(([k, val]) => v.variant_values[k] === val)) : undefined;
  const chosen = it.is_template ? variant : it;
  const url = `${window.location.origin}/#/shop/${slug}/p/${id}`;
  return (
    <div style={{ display: 'grid', gap: 14, gridTemplateColumns: 'repeat(auto-fit, minmax(280px, 1fr))' }}>
      <Pic slug={slug} item={chosen?.has_image ? chosen : it} big />
      <div className="psh-box">
        <h2 style={{ margin: 0 }}>{nm(it, lang)}</h2>
        {it.brand && <small>{it.brand}</small>}
        <Money v={chosen?.price ?? it.price} cur={home.currency} local={chosen?.price_local ?? it.price_local} localCur={home.local_currency} dec={home.decimals} />
        {(d.attributes ?? []).map((a) => (
          <div key={a.name}>
            <div style={{ fontWeight: 600, marginBottom: 6 }}>{a.name}</div>
            <div className="psh-opt">
              {a.values.map((val) => {
                const possible = it.variants?.some((v) => v.variant_values[a.name] === val && v.in_stock);
                return <button key={val} aria-pressed={pick[a.name] === val} disabled={!possible} style={pick[a.name] === val ? { borderColor: color } : undefined}
                  onClick={() => setPick({ ...pick, [a.name]: val })}>{val}</button>;
              })}
            </div>
          </div>))}
        {chosen && !chosen.in_stock && <span className="psh-out">{t('pub.shop.out')}</span>}
        {chosen?.in_stock && chosen.stock && <small style={{ color: '#a15c00' }}>{t('pub.shop.left', { n: Number(chosen.stock) })}</small>}
        <div style={{ display: 'flex', gap: 10, alignItems: 'center', flexWrap: 'wrap' }}>
          <span className="psh-qty"><button aria-label="-" onClick={() => setQty(Math.max(1, qty - 1))}>−</button><span>{qty}</span><button aria-label="+" onClick={() => setQty(qty + 1)}>+</button></span>
          <button className="psh-btn main" style={{ background: color, flex: 1 }} disabled={!chosen || !chosen.in_stock}
            onClick={() => chosen && onAdd({ ...chosen, name: chosen === it ? it.name : `${it.name} (${variantText(chosen.variant_values)})`, name_i18n: chosen === it ? it.name_i18n : {} }, qty)}>
            {it.is_template && !chosen ? t('pub.shop.pickOptions') : t('pub.shop.addToCart')}
          </button>
        </div>
        {it.description && <p style={{ whiteSpace: 'pre-wrap', margin: 0, color: '#3b4a5e' }}>{it.description}</p>}
        <a className="psh-btn" style={{ textAlign: 'center', textDecoration: 'none' }} target="_blank" rel="noopener noreferrer"
          href={`https://wa.me/?text=${encodeURIComponent(`${nm(it, lang)} — ${formatAmount(it.price, home.decimals)} ${home.currency}\n${url}`)}`}>{t('pub.shop.shareWa')}</a>
        <button className="psh-btn" onClick={() => window.history.back()}>{t('pub.shop.back')}</button>
      </div>
    </div>
  );
}

function Cart({ slug, home, color, cart, setCart }: { slug: string; home: Home; color: string; cart: CartLine[]; setCart: (c: CartLine[]) => void }) {
  const { t, i18n } = useTranslation();
  const lang = i18n.language;
  const [delivery, setDelivery] = useState<'delivery' | 'pickup'>(home.zones.length || !home.payments.includes('pickup') ? 'delivery' : 'pickup');
  const [zone, setZone] = useState(home.zones[0]?.id ?? '');
  const [coupon, setCoupon] = useState('');
  const [couponIn, setCouponIn] = useState('');
  const [priced, setPriced] = useState<Priced | null>(null);
  const [f, setF] = useState({ name: '', phone: '', email: '', address: '', notes: '', payment: home.payments[0] ?? 'cod', website: '' });
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState<{ number: number; token: string; total: string; currency: string; status: string } | null>(null);
  const lines = useMemo(() => cart.map((l) => ({ item_id: l.item_id, quantity: String(l.quantity) })), [cart]);
  useEffect(() => {
    if (!lines.length) { setPriced(null); return; }
    const h = setTimeout(() => {
      api<Priced>(`/shop/${slug}/cart`, { method: 'POST', body: { lines, zone_id: delivery === 'delivery' ? zone : '', delivery, coupon } }).then(setPriced).catch(() => setPriced(null));
    }, 250);
    return () => clearTimeout(h);
  }, [slug, lines, zone, delivery, coupon]);
  const setQty = (id: string, q: number) => setCart(q <= 0 ? cart.filter((l) => l.item_id !== id) : cart.map((l) => (l.item_id === id ? { ...l, quantity: q } : l)));
  const problem = (id: string) => priced?.problems.find((p) => p.item_id === id);
  const dec = home.decimals;
  if (done) {
    const link = `#/order/${done.token}`;
    return (
      <div className="psh-box" style={{ textAlign: 'center' }}>
        <div style={{ fontSize: 46 }}>✅</div>
        <h2 style={{ margin: 0 }}>{t('pub.shop.thanks')}</h2>
        <p style={{ margin: 0 }}>{t('pub.shop.orderNo', { n: done.number })} — <b className="psh-price">{formatAmount(done.total, dec)} {done.currency}</b></p>
        <p style={{ margin: 0, color: '#5b6b7f' }}>{done.status === 'new' ? t('pub.shop.waitConfirm') : t('pub.shop.confirmed')}</p>
        {home.shop.pay_note && (f.payment === 'whish' || f.payment === 'omt') && <div className="psh-ok" style={{ whiteSpace: 'pre-wrap' }}>{home.shop.pay_note}</div>}
        <a className="psh-btn main" style={{ background: color, textDecoration: 'none' }} href={link}>{t('pub.shop.track')}</a>
      </div>
    );
  }
  if (!cart.length) return <div className="psh-box" style={{ textAlign: 'center' }}><div style={{ fontSize: 40 }}>🛒</div>{t('pub.shop.empty')}
    <a className="psh-btn" href={`#/shop/${slug}`} style={{ textDecoration: 'none' }}>{t('pub.shop.continue')}</a></div>;
  const waText = () => {
    const rows = cart.map((l) => `• ${l.quantity} × ${nm(l, lang)}${variantText(l.variant) ? ` (${variantText(l.variant)})` : ''}`).join('\n');
    return `${t('pub.shop.waHello')}\n${rows}\n${t('pub.shop.total')}: ${priced ? formatAmount(priced.total, dec) : ''} ${home.currency}${f.name ? `\n${f.name} ${f.phone}` : ''}`;
  };
  const wa = home.shop.whatsapp.replace(/[^\d]/g, '');
  async function place() {
    setErr('');
    setBusy(true);
    try {
      const r = await api<{ number: number; token: string; total: string; currency: string; status: string }>(`/shop/${slug}/orders`, {
        method: 'POST', body: { lines, zone_id: delivery === 'delivery' ? zone : '', delivery, coupon, name: f.name, phone: f.phone, email: f.email, address: f.address,
          payment: f.payment, notes: f.notes, website: f.website },
      });
      setCart([]);
      setDone(r);
    } catch (e) {
      if (e instanceof ApiError && e.code === 'cart_changed') setErr(t('pub.shop.cartChanged'));
      else if (e instanceof ApiError && e.code === 'min_order') setErr(t('pub.shop.minOrder', { v: e.detail }));
      else setErr(errorText(t, e));
    } finally { setBusy(false); }
  }
  const canPlace = f.name.trim() && f.phone.trim().length >= 6 && (delivery === 'pickup' || (f.address.trim() && (zone || !home.zones.length))) && priced && !priced.problems.length && !busy;
  return (
    <div style={{ display: 'grid', gap: 12, gridTemplateColumns: 'repeat(auto-fit, minmax(300px, 1fr))', alignItems: 'start' }}>
      <div className="psh-box">
        <h3 style={{ margin: 0 }}>{t('pub.shop.cart')}</h3>
        {cart.map((l) => {
          const pl = priced?.lines.find((x) => x.item_id === l.item_id);
          const p = problem(l.item_id);
          return (
            <div key={l.item_id} style={{ display: 'flex', gap: 10, alignItems: 'center', borderBottom: '1px solid #eef1f5', paddingBottom: 8 }}>
              <div style={{ width: 54, flex: 'none' }}><Pic slug={slug} item={{ id: l.item_id, has_image: l.has_image, name: l.name }} /></div>
              <div style={{ flex: 1, minWidth: 0 }}>
                <div className="psh-name">{nm(l, lang)}</div>
                {pl && <small className="psh-price">{formatAmount(pl.amount, dec)} {home.currency}</small>}
                {pl && Number(pl.discount) > 0 && <small style={{ color: '#0f7b4b' }}> −{formatAmount(pl.discount, dec)}</small>}
                {p && <div className="psh-out">{p.code === 'out_of_stock' ? t('pub.shop.onlyLeft', { n: Number(p.available ?? 0) }) : t('pub.shop.unavailable')}</div>}
              </div>
              <span className="psh-qty"><button aria-label="-" onClick={() => setQty(l.item_id, l.quantity - 1)}>−</button><span>{l.quantity}</span>
                <button aria-label="+" onClick={() => setQty(l.item_id, l.quantity + 1)}>+</button></span>
            </div>);
        })}
        <div style={{ display: 'flex', gap: 8 }}>
          <input aria-label={t('pub.shop.coupon')} placeholder={t('pub.shop.coupon')} value={couponIn} onChange={(e) => setCouponIn(e.currentTarget.value.toUpperCase())}
            style={{ flex: 1, font: 'inherit', padding: 10, borderRadius: 10, border: '1px solid #cbd5e1', minWidth: 0 }} />
          <button className="psh-btn" onClick={() => setCoupon(couponIn.trim())}>{t('pub.shop.apply')}</button>
        </div>
        {priced?.coupon_valid === false && <div className="psh-err">{t('pub.shop.badCoupon')}</div>}
        {priced?.coupon_valid === true && <div className="psh-ok">{t('pub.shop.goodCoupon')}</div>}
        {priced && <div>
          <div className="psh-row"><span>{t('pub.shop.subtotal')}</span><span className="psh-price">{formatAmount(priced.subtotal, dec)}</span></div>
          {Number(priced.discount) > 0 && <div className="psh-row" style={{ color: '#0f7b4b' }}><span>{t('pub.shop.discount')}</span><span className="psh-price">−{formatAmount(priced.discount, dec)}</span></div>}
          {Number(priced.tax) > 0 && <div className="psh-row"><span>{t('pub.shop.tax')}</span><span className="psh-price">{formatAmount(priced.tax, dec)}</span></div>}
          {delivery === 'delivery' && <div className="psh-row"><span>{t('pub.shop.fee')}</span><span className="psh-price">{Number(priced.fee) > 0 ? formatAmount(priced.fee, dec) : t('pub.shop.free')}</span></div>}
          <div className="psh-row" style={{ fontWeight: 700, fontSize: 18 }}><span>{t('pub.shop.total')}</span>
            <Money v={priced.total} cur={home.currency} local={priced.total_local} localCur={home.local_currency} dec={dec} /></div>
        </div>}
      </div>
      <div className="psh-box">
        <h3 style={{ margin: 0 }}>{t('pub.shop.checkout')}</h3>
        <div className="psh-opt" role="radiogroup" aria-label={t('pub.shop.howGet')}>
          <button aria-pressed={delivery === 'delivery'} style={delivery === 'delivery' ? { borderColor: color } : undefined} onClick={() => setDelivery('delivery')}>🚚 {t('pub.shop.delivery')}</button>
          <button aria-pressed={delivery === 'pickup'} style={delivery === 'pickup' ? { borderColor: color } : undefined} onClick={() => setDelivery('pickup')}>🏪 {t('pub.shop.pickup')}</button>
        </div>
        {delivery === 'delivery' && home.zones.length > 0 && <label className="psh-fld">{t('pub.shop.zone')}
          <select value={zone} onChange={(e) => setZone(e.currentTarget.value)}>
            {home.zones.map((z) => <option key={z.id} value={z.id}>{z.name} — {Number(z.fee) > 0 ? `${formatAmount(z.fee, dec)} ${home.currency}` : t('pub.shop.free')}{z.eta ? ` · ${z.eta}` : ''}</option>)}
          </select></label>}
        <label className="psh-fld">{t('pub.shop.name')}<input autoComplete="name" value={f.name} onChange={(e) => setF({ ...f, name: e.currentTarget.value })} /></label>
        <label className="psh-fld">{t('pub.shop.phone')}<input type="tel" dir="ltr" autoComplete="tel" value={f.phone} onChange={(e) => setF({ ...f, phone: e.currentTarget.value })} /></label>
        {delivery === 'delivery' && <label className="psh-fld">{t('pub.shop.address')}<textarea rows={2} autoComplete="street-address" value={f.address} onChange={(e) => setF({ ...f, address: e.currentTarget.value })} /></label>}
        <label className="psh-fld">{t('pub.shop.email')}<input type="email" dir="ltr" autoComplete="email" value={f.email} onChange={(e) => setF({ ...f, email: e.currentTarget.value })} /></label>
        <label className="psh-fld">{t('pub.shop.notes')}<input value={f.notes} onChange={(e) => setF({ ...f, notes: e.currentTarget.value })} /></label>
        <label className="psh-hp" aria-hidden="true">Website<input tabIndex={-1} autoComplete="off" value={f.website} onChange={(e) => setF({ ...f, website: e.currentTarget.value })} /></label>
        <div className="psh-fld">{t('pub.shop.payment')}
          <div className="psh-opt" role="radiogroup">
            {home.payments.map((p) => <button key={p} aria-pressed={f.payment === p} style={f.payment === p ? { borderColor: color } : undefined} onClick={() => setF({ ...f, payment: p })}>{t(`pub.shop.pay.${p}`)}</button>)}
          </div>
        </div>
        {home.shop.pay_note && (f.payment === 'whish' || f.payment === 'omt') && <small style={{ whiteSpace: 'pre-wrap' }}>{home.shop.pay_note}</small>}
        {Number(home.shop.min_order) > 0 && <small>{t('pub.shop.minOrder', { v: `${formatAmount(home.shop.min_order, dec)} ${home.currency}` })}</small>}
        {err && <div className="psh-err">{err}</div>}
        <button className="psh-btn main" style={{ background: color, minHeight: 52, fontSize: 17 }} disabled={!canPlace} onClick={() => void place()}>
          {t('pub.shop.place')}{priced ? ` · ${formatAmount(priced.total, dec)} ${home.currency}` : ''}</button>
        {wa && <a className="psh-btn" style={{ textAlign: 'center', textDecoration: 'none', borderColor: '#25d366', color: '#128c4a' }} target="_blank" rel="noopener noreferrer"
          href={`https://wa.me/${wa}?text=${encodeURIComponent(waText())}`}>🟢 {t('pub.shop.orderWa')}</a>}
        {home.shop.terms && <details><summary>{t('pub.shop.terms')}</summary><p style={{ whiteSpace: 'pre-wrap', fontSize: 13 }}>{home.shop.terms}</p></details>}
      </div>
    </div>
  );
}

// ======================= order tracking =======================

interface Track {
  order: { number: number; name: string; phone: string; address: string; delivery: string; payment: string; coupon: string; currency: string; subtotal: string;
    discount: string; fee: string; tax: string; total: string; created_at: string; zone: string | null; eta: string | null; state: string; company: string;
    company_ar: string; logo: string; locale: string; slug: string | null; color: string | null; whatsapp: string | null; pay_note: string | null };
  lines: { description: string; quantity: string; unit_price: string; amount: string; delivered: string }[];
}

const STATES = ['received', 'preparing', 'on_the_way', 'delivered'];

export function OrderTrack({ token }: { token: string }) {
  const { t, i18n } = useTranslation();
  const [d, setD] = useState<Track | null | undefined>(undefined);
  useEffect(() => {
    const load = () => api<Track>(`/shop-orders/${token}`).then((x) => {
      setD(x);
      if (x.order.locale && !localStorage.getItem('paxve.lang')) applyLang(x.order.locale);
    }).catch(() => setD(null));
    load();
    const h = setInterval(load, 60000);
    return () => clearInterval(h);
  }, [token]);
  if (d === undefined) return <div className="psh"><style>{css}</style><div className="psh-wrap">…</div></div>;
  if (d === null) return <div className="psh"><style>{css}</style><div className="psh-wrap"><div className="psh-box">{t('pub.shop.notFound')}</div></div></div>;
  const o = d.order;
  const color = o.color || '#0f766e';
  const company = isAr(i18n.language) && o.company_ar ? o.company_ar : o.company;
  const idx = STATES.indexOf(o.state);
  const wa = (o.whatsapp ?? '').replace(/[^\d]/g, '');
  return (
    <div className="psh" dir={i18n.dir()}>
      <style>{css}</style>
      <header className="psh-top" style={{ background: color }}>
        {o.logo && <img src={o.logo} alt="" />}<b>{company}</b><span className="psh-sp" /><LangPick />
      </header>
      <div className="psh-wrap" style={{ maxWidth: 640 }}>
        <div className="psh-box">
          <h2 style={{ margin: 0 }}>{t('pub.shop.orderNo', { n: o.number })}</h2>
          <small>{formatDateTime(o.created_at)} · {o.name}</small>
          {o.state === 'cancelled' ? <div className="psh-err">{t('pub.shop.st.cancelled')}</div> :
            <div className="psh-steps">{STATES.map((s, i) => (
              <div key={s} className={i <= idx ? 'on' : ''} style={i <= idx ? { borderTopColor: color } : undefined}>{t(`pub.shop.st.${s}`)}</div>))}</div>}
          {o.delivery === 'delivery' ? <small>🚚 {o.zone ?? ''}{o.eta ? ` · ${o.eta}` : ''} — {o.address}</small> : <small>🏪 {t('pub.shop.pickup')}</small>}
          <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 14 }}><tbody>
            {d.lines.map((l, i) => <tr key={i} style={{ borderBottom: '1px solid #eef1f5' }}><td style={{ padding: '6px 0' }}>{l.quantity} × {l.description}</td>
              <td className="psh-price" style={{ textAlign: 'end' }}>{formatAmount(l.amount, 2)}</td></tr>)}
          </tbody></table>
          {Number(o.discount) > 0 && <div className="psh-row" style={{ color: '#0f7b4b' }}><span>{t('pub.shop.discount')}</span><span className="psh-price">−{formatAmount(o.discount, 2)}</span></div>}
          {Number(o.tax) > 0 && <div className="psh-row"><span>{t('pub.shop.tax')}</span><span className="psh-price">{formatAmount(o.tax, 2)}</span></div>}
          <div className="psh-row" style={{ fontWeight: 700, fontSize: 18 }}><span>{t('pub.shop.total')}</span><span className="psh-price">{formatAmount(o.total, 2)} {o.currency}</span></div>
          <small>{t('pub.shop.payment')}: {t(`pub.shop.pay.${o.payment}`)}</small>
          {o.pay_note && (o.payment === 'whish' || o.payment === 'omt') && <small style={{ whiteSpace: 'pre-wrap' }}>{o.pay_note}</small>}
          {wa && <a className="psh-btn" style={{ textAlign: 'center', textDecoration: 'none' }} target="_blank" rel="noopener noreferrer"
            href={`https://wa.me/${wa}?text=${encodeURIComponent(t('pub.shop.waAbout', { n: o.number }))}`}>🟢 {t('pub.shop.contact')}</a>}
          {o.slug && <a className="psh-btn" style={{ textAlign: 'center', textDecoration: 'none' }} href={`#/shop/${o.slug}`}>{t('pub.shop.continue')}</a>}
        </div>
      </div>
    </div>
  );
}
