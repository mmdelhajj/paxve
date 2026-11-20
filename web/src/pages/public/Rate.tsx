import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api } from '../../api';
import { applyLang } from '../../i18n';
import { errorText } from '../../util';

// The customer's rating page (#/rate/<token>): opened from the WhatsApp message sent when a request is closed.

interface Info { company: string; company_ar: string | null; logo: string | null; number: number; subject: string; rating: number | null; rating_comment: string; rated_at: string | null; locale: string }

export default function Rate({ token }: { token: string }) {
  const { t, i18n } = useTranslation();
  const [d, setD] = useState<Info | null | undefined>(undefined);
  const [stars, setStars] = useState(0);
  const [comment, setComment] = useState('');
  const [done, setDone] = useState(false);
  const [err, setErr] = useState('');
  useEffect(() => {
    api<Info>(`/rate/${token}`).then((x) => {
      setD(x); setStars(x.rating ?? 0); setComment(x.rating_comment ?? '');
      if (x.locale && !localStorage.getItem('paxve.lang')) applyLang(x.locale);
    }).catch(() => setD(null));
  }, [token]);
  async function send() {
    setErr('');
    try { await api(`/rate/${token}`, { method: 'POST', body: { rating: stars, comment } }); setDone(true); } catch (e) { setErr(errorText(t, e)); }
  }
  const box: React.CSSProperties = { maxWidth: 460, margin: '40px auto', background: '#fff', borderRadius: 12, padding: 20, border: '1px solid #dde3ea', fontFamily: 'inherit' };
  if (d === undefined) return <div style={box}>…</div>;
  if (d === null) return <div style={box}>{t('fsv.rate.notFound')}</div>;
  const name = i18n.language === 'ar' && d.company_ar ? d.company_ar : d.company;
  return (
    <div dir={i18n.dir()} style={{ minHeight: '100vh', background: '#f3f5f8', padding: 12 }}>
      <div style={box}>
        {d.logo && <img src={d.logo} alt="" style={{ maxHeight: 60, display: 'block', margin: '0 auto 10px' }} />}
        <h2 style={{ margin: '0 0 4px', textAlign: 'center' }}>{name}</h2>
        <p style={{ textAlign: 'center', margin: '0 0 12px' }}>{t('fsv.rate.title', { n: d.number })}<br /><b>{d.subject}</b></p>
        {done ? <p style={{ textAlign: 'center', fontSize: 18 }}>{t('fsv.rate.thanks')}</p> : <>
          <div role="radiogroup" aria-label={t('fsv.rate.question')} style={{ display: 'flex', justifyContent: 'center', gap: 6, margin: '8px 0' }}>
            {[1, 2, 3, 4, 5].map((n) => (
              <button key={n} role="radio" aria-checked={stars === n} aria-label={String(n)} onClick={() => setStars(n)}
                style={{ fontSize: 34, background: 'none', border: 0, cursor: 'pointer', color: n <= stars ? '#f5a524' : '#c9d1db', minWidth: 44, minHeight: 44 }}>★</button>))}
          </div>
          <p style={{ textAlign: 'center', margin: 0 }}>{t('fsv.rate.question')}</p>
          <textarea rows={3} style={{ width: '100%', marginTop: 10, boxSizing: 'border-box' }} placeholder={t('fsv.rate.comment')} value={comment} onChange={(e) => setComment(e.currentTarget.value)} />
          {err && <div className="mx-flash bad">{err}</div>}
          <button className="mx-btn main" style={{ width: '100%', minHeight: 48, marginTop: 10 }} disabled={!stars} onClick={() => void send()}>{t('fsv.rate.send')}</button>
        </>}
      </div>
    </div>
  );
}
