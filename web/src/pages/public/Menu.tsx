import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api } from '../../api';
import { applyLang } from '../../i18n';
import { errorText, formatAmount } from '../../util';

// Phase 9: the restaurant's menu for one table (#/menu/<token>, from the QR code on the table). The guest's order waits
// for the staff, who accept it onto the table's check (and the kitchen screen).

interface Item { id: string; name: string; name_i18n: Record<string, string>; description: string; category_id: string | null; price: string }
interface Info { name_en: string; name_ar: string | null; logo: string | null; default_locale: string; table_name: string; area: string }
interface Data { info: Info; currency: string; decimals: number; items: Item[]; categories: { id: string; name: string; name_i18n: Record<string, string> }[] }
interface Mine { number: number; lines: { name: string; quantity: string }[]; total: string; currency: string; status: string; created_at: string }

export default function Menu({ token }: { token: string }) {
  const { t, i18n } = useTranslation();
  const [d, setD] = useState<Data | null | undefined>(undefined);
  const [cart, setCart] = useState<Record<string, { qty: number; note: string }>>({});
  const [cat, setCat] = useState('');
  const [name, setName] = useState('');
  const [note, setNote] = useState('');
  const [hp, setHp] = useState('');
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);
  const [mine, setMine] = useState<Mine[]>([]);
  const loadMine = () => api<Mine[]>(`/menu/${token}/orders`).then(setMine).catch(() => {});
  useEffect(() => {
    api<Data>(`/menu/${token}`).then((x) => { setD(x); if (x.info.default_locale && !localStorage.getItem('paxve.lang')) applyLang(x.info.default_locale); }).catch(() => setD(null));
    void loadMine();
    const h = window.setInterval(() => void loadMine(), 20000);
    return () => window.clearInterval(h);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [token]);
  const nameOf = (x: { name: string; name_i18n: Record<string, string> }) => x.name_i18n?.[i18n.language] || x.name;
  const items = useMemo(() => (d?.items ?? []).filter((x) => !cat || x.category_id === cat), [d, cat]);
  const lines = Object.entries(cart).filter(([, v]) => v.qty > 0);
  const total = d ? lines.reduce((s, [id, v]) => s + Number(d.items.find((x) => x.id === id)?.price ?? 0) * v.qty, 0) : 0;
  const add = (id: string, n: number) => setCart((c) => ({ ...c, [id]: { qty: Math.max(0, Math.min(50, (c[id]?.qty ?? 0) + n)), note: c[id]?.note ?? '' } }));
  async function send() {
    setErr(''); setBusy(true);
    try {
      await api(`/menu/${token}/orders`, { method: 'POST', body: { name: name.trim(), note: note.trim(), website: hp, lines: lines.map(([id, v]) => ({ item_id: id, quantity: String(v.qty), note: v.note.trim() })) } });
      setCart({}); setNote('');
      void loadMine();
    } catch (e) { setErr(errorText(t, e)); } finally { setBusy(false); }
  }
  const page: React.CSSProperties = { minHeight: '100vh', background: '#f3f5f8', padding: 10, paddingBottom: 140, fontFamily: 'inherit' };
  if (d === undefined) return <div style={page}>…</div>;
  if (d === null) return <div style={page}><div className="mx-box" style={{ maxWidth: 480, margin: '40px auto', padding: 20 }}>{t('op.menu.closed')}</div></div>;
  const company = i18n.language === 'ar' && d.info.name_ar ? d.info.name_ar : d.info.name_en;
  return (
    <div dir={i18n.dir()} style={page}>
      <div style={{ maxWidth: 560, margin: '0 auto' }}>
        <header style={{ textAlign: 'center', padding: '8px 0' }}>
          {d.info.logo && <img src={d.info.logo} alt="" style={{ maxHeight: 56 }} />}
          <h2 style={{ margin: '4px 0' }}>{company}</h2>
          <div>{t('op.menu.table', { table: d.info.table_name })} {d.info.area && <small>· {d.info.area}</small>}</div>
        </header>
        <div style={{ display: 'flex', gap: 6, overflowX: 'auto', padding: '6px 0' }}>
          <button className={`mx-btn sm ${cat === '' ? 'main' : ''}`} onClick={() => setCat('')}>{t('op.menu.all')}</button>
          {d.categories.filter((c) => d.items.some((x) => x.category_id === c.id)).map((c) => (
            <button key={c.id} className={`mx-btn sm ${cat === c.id ? 'main' : ''}`} onClick={() => setCat(c.id)}>{nameOf(c)}</button>))}
        </div>
        {items.map((x) => (
          <div key={x.id} className="mx-box" style={{ display: 'flex', alignItems: 'center', gap: 8, padding: 10, marginBottom: 6 }}>
            <div style={{ flex: 1, minWidth: 0 }}>
              <b><bdi>{nameOf(x)}</bdi></b>
              {x.description && <div style={{ fontSize: 12.5, opacity: 0.75 }}><bdi>{x.description}</bdi></div>}
              <div dir="ltr" style={{ fontFamily: 'monospace' }}>{formatAmount(x.price, d.decimals)} {d.currency}</div>
              {(cart[x.id]?.qty ?? 0) > 0 && <input placeholder={t('op.menu.note')} value={cart[x.id].note} maxLength={120} style={{ width: '100%', marginTop: 4 }}
                onChange={(e) => setCart({ ...cart, [x.id]: { ...cart[x.id], note: e.currentTarget.value } })} />}
            </div>
            <div style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
              {(cart[x.id]?.qty ?? 0) > 0 && <button className="mx-btn" style={{ minWidth: 40, minHeight: 40 }} aria-label={t('op.menu.less')} onClick={() => add(x.id, -1)}>−</button>}
              {(cart[x.id]?.qty ?? 0) > 0 && <b style={{ minWidth: 20, textAlign: 'center' }}>{cart[x.id].qty}</b>}
              <button className="mx-btn main" style={{ minWidth: 40, minHeight: 40 }} aria-label={t('op.menu.more')} onClick={() => add(x.id, 1)}>+</button>
            </div>
          </div>))}
        {mine.length > 0 && (
          <div className="mx-box" style={{ padding: 10, marginTop: 10 }}>
            <b>{t('op.menu.mine')}</b>
            {mine.map((m) => (
              <div key={m.number} style={{ display: 'flex', justifyContent: 'space-between', gap: 6, borderTop: '1px solid #eef1f5', padding: '4px 0', fontSize: 13 }}>
                <span>#{m.number} · {m.lines.map((l) => `${Number(l.quantity)}× ${l.name}`).join(', ')}</span>
                <span className={`mx-pill ${m.status === 'accepted' ? 'on' : m.status === 'pending' ? 'warn' : ''}`}>{t(`op.qr.s.${m.status}`)}</span>
              </div>))}
          </div>)}
      </div>
      {lines.length > 0 && (
        <div style={{ position: 'fixed', insetInline: 0, bottom: 0, background: '#fff', borderTop: '1px solid #dde3ea', padding: 10, paddingBottom: 'calc(10px + env(safe-area-inset-bottom, 0px))' }}>
          <div style={{ maxWidth: 560, margin: '0 auto', display: 'flex', flexDirection: 'column', gap: 6 }}>
            <div style={{ display: 'flex', gap: 6 }}>
              <input placeholder={t('op.menu.yourName')} value={name} maxLength={60} onChange={(e) => setName(e.currentTarget.value)} style={{ flex: 1, minWidth: 0 }} />
              <input placeholder={t('op.menu.orderNote')} value={note} maxLength={300} onChange={(e) => setNote(e.currentTarget.value)} style={{ flex: 2, minWidth: 0 }} />
              <input tabIndex={-1} aria-hidden autoComplete="off" value={hp} onChange={(e) => setHp(e.currentTarget.value)} style={{ position: 'absolute', width: 1, height: 1, opacity: 0, overflow: 'hidden', insetInlineStart: 0 }} name="website" />
            </div>
            {err && <div className="mx-flash bad">{err}</div>}
            <button className="mx-btn main" style={{ minHeight: 48 }} disabled={busy} onClick={() => void send()}>
              {t('op.menu.send', { n: lines.reduce((s, [, v]) => s + v.qty, 0) })} · <span dir="ltr">{formatAmount(String(total), d.decimals)} {d.currency}</span></button>
            <small style={{ textAlign: 'center', opacity: 0.75 }}>{t('op.menu.waitStaff')}</small>
          </div>
        </div>)}
    </div>
  );
}
