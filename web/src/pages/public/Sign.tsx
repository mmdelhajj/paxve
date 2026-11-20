import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api } from '../../api';
import { applyLang } from '../../i18n';
import { errorText, formatDateTime } from '../../util';

// The public signing page (#/sign/<token>): the document (text or the uploaded file), the signers, and a signature
// drawn with the finger / mouse or typed (turned into a picture). Also the marketing opt-out page (#/optout/<token>).

interface SignData {
  request: { number: number; title: string; doc_type: string; body: string; doc_hash: string; message: string; status: string; expires_on: string | null;
    has_file: boolean; file_mime: string | null; file_name: string | null; completed_at: string | null; has_certificate: boolean; company: string; company_ar: string;
    logo: string; locale: string };
  me: { name: string; status: string; signed_at: string | null; sign_method: string; typed_name: string; signature: string; can_sign: boolean };
  signers: { seq: number; name: string; status: string; signed_at: string | null; signature: string }[];
}

const box: React.CSSProperties = { background: '#fff', border: '1px solid #e3e8ef', borderRadius: 14, padding: 16, display: 'flex', flexDirection: 'column', gap: 10 };

function Pad({ onChange }: { onChange: (png: string) => void }) {
  const { t } = useTranslation();
  const ref = useRef<HTMLCanvasElement>(null);
  const drawing = useRef(false);
  const dirty = useRef(false);
  useEffect(() => {
    const c = ref.current;
    if (!c) return;
    c.width = c.clientWidth * 2;
    c.height = c.clientHeight * 2;
    const g = c.getContext('2d');
    if (g) { g.scale(2, 2); g.lineWidth = 2.4; g.lineCap = 'round'; g.lineJoin = 'round'; g.strokeStyle = '#0b1f3a'; }
  }, []);
  const pos = (e: React.PointerEvent<HTMLCanvasElement>) => {
    const r = e.currentTarget.getBoundingClientRect();
    return [e.clientX - r.left, e.clientY - r.top] as const;
  };
  return (
    <div>
      <canvas ref={ref} aria-label={t('pub.sign.drawHere')} style={{ width: '100%', height: 170, background: '#fbfcfe', border: '2px dashed #94a3b8', borderRadius: 12, touchAction: 'none' }}
        onPointerDown={(e) => { drawing.current = true; e.currentTarget.setPointerCapture(e.pointerId); const g = e.currentTarget.getContext('2d'); const [x, y] = pos(e); g?.beginPath(); g?.moveTo(x, y); }}
        onPointerMove={(e) => { if (!drawing.current) return; const g = e.currentTarget.getContext('2d'); const [x, y] = pos(e); g?.lineTo(x, y); g?.stroke(); dirty.current = true; }}
        onPointerUp={(e) => { drawing.current = false; if (dirty.current) onChange(e.currentTarget.toDataURL('image/png')); }} />
      <button type="button" className="mx-btn sm" onClick={() => { const c = ref.current; c?.getContext('2d')?.clearRect(0, 0, c.width, c.height); dirty.current = false; onChange(''); }}>{t('pub.sign.clear')}</button>
    </div>
  );
}

/** A typed name drawn as a signature picture (so every signature is an image). */
function typedPng(name: string): string {
  const c = document.createElement('canvas');
  c.width = 900;
  c.height = 220;
  const g = c.getContext('2d');
  if (!g) return '';
  g.fillStyle = 'rgba(255,255,255,0)';
  g.fillRect(0, 0, c.width, c.height);
  g.fillStyle = '#0b1f3a';
  g.textBaseline = 'middle';
  g.font = 'italic 84px "Segoe Script", "Brush Script MT", "IBM Plex Sans Arabic", cursive';
  const w = g.measureText(name).width;
  const k = Math.min(1, 860 / Math.max(1, w));
  g.save();
  g.translate(20, 110);
  g.scale(k, k);
  g.fillText(name, 0, 0);
  g.restore();
  g.fillRect(20, 190, 860, 2);
  return c.toDataURL('image/png');
}

export default function Sign({ token }: { token: string }) {
  const { t, i18n } = useTranslation();
  const [d, setD] = useState<SignData | null | undefined>(undefined);
  const [method, setMethod] = useState<'draw' | 'type'>('draw');
  const [drawn, setDrawn] = useState('');
  const [typed, setTyped] = useState('');
  const [agree, setAgree] = useState(false);
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);
  const [declining, setDeclining] = useState(false);
  const [reason, setReason] = useState('');
  const load = () => api<SignData>(`/sign/${token}`).then((x) => {
    setD(x);
    if (x.request.locale && !localStorage.getItem('paxve.lang')) applyLang(x.request.locale);
    setTyped((v) => v || x.me.name);
  }).catch(() => setD(null));
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => { void load(); }, [token]);
  if (d === undefined) return <div style={{ padding: 20 }}>…</div>;
  if (d === null) return <div style={{ ...box, maxWidth: 520, margin: '40px auto' }}>{t('pub.sign.notFound')}</div>;
  const q = d.request;
  const company = i18n.language === 'ar' && q.company_ar ? q.company_ar : q.company;
  const signature = method === 'draw' ? drawn : (typed.trim() ? typedPng(typed.trim()) : '');
  async function sign() {
    setErr('');
    setBusy(true);
    try {
      await api(`/sign/${token}`, { method: 'POST', body: { method, signature, typed_name: method === 'type' ? typed.trim() : '', agree } });
      await load();
    } catch (e) { setErr(errorText(t, e)); } finally { setBusy(false); }
  }
  async function decline() {
    setErr('');
    try { await api(`/sign/${token}/decline`, { method: 'POST', body: { reason } }); setDeclining(false); await load(); } catch (e) { setErr(errorText(t, e)); }
  }
  const stColor: Record<string, string> = { signed: '#0f7b4b', declined: '#b42318', pending: '#a15c00', viewed: '#1d4ed8', waiting: '#5b6b7f' };
  return (
    <div dir={i18n.dir()} style={{ minHeight: '100vh', background: '#f4f6f9', color: '#0f1b2d' }}>
      <header style={{ background: '#0b1f3a', color: '#fff', padding: '14px 16px', display: 'flex', gap: 12, alignItems: 'center' }}>
        {q.logo && <img src={q.logo} alt="" style={{ height: 36, background: '#fff', borderRadius: 8 }} />}
        <div><b>{company}</b><div style={{ opacity: 0.85, fontSize: 13 }}>{t('pub.sign.title')} #{q.number}</div></div>
      </header>
      <div style={{ maxWidth: 820, margin: '0 auto', padding: 12, display: 'flex', flexDirection: 'column', gap: 12 }}>
        <div style={box}>
          <h2 style={{ margin: 0 }}>{q.title}</h2>
          {q.message && <p style={{ margin: 0, whiteSpace: 'pre-wrap' }}>{q.message}</p>}
          {q.status === 'completed' && <div style={{ background: '#e6f4ec', color: '#0f7b4b', borderRadius: 10, padding: 10 }}>✅ {t('pub.sign.completed')}</div>}
          {q.status === 'declined' && <div style={{ background: '#fdecea', color: '#b42318', borderRadius: 10, padding: 10 }}>{t('pub.sign.declined')}</div>}
          {(q.status === 'cancelled' || q.status === 'expired') && <div style={{ background: '#eef1f5', borderRadius: 10, padding: 10 }}>{t(`pub.sign.st.${q.status}`)}</div>}
        </div>
        <div style={box}>
          <b>{t('pub.sign.document')}</b>
          {q.body && <pre style={{ whiteSpace: 'pre-wrap', fontFamily: 'inherit', margin: 0, background: '#f8fafc', borderRadius: 10, padding: 12, maxHeight: 520, overflow: 'auto' }}>{q.body}</pre>}
          {q.has_file && (q.file_mime === 'application/pdf'
            ? <iframe title={q.file_name ?? 'document'} src={`/api/v1/sign/${token}/file`} style={{ width: '100%', height: 560, border: '1px solid #e3e8ef', borderRadius: 10 }} />
            : <img alt={q.file_name ?? ''} src={`/api/v1/sign/${token}/file`} style={{ maxWidth: '100%', borderRadius: 10 }} />)}
          {q.has_file && <a href={`/api/v1/sign/${token}/file`} target="_blank" rel="noopener noreferrer">{t('pub.sign.openFile')}</a>}
          <small style={{ color: '#5b6b7f', wordBreak: 'break-all' }}>SHA-256: {q.doc_hash}</small>
        </div>
        <div style={box}>
          <b>{t('pub.sign.signers')}</b>
          {d.signers.map((s) => (
            <div key={s.seq} style={{ display: 'flex', gap: 10, alignItems: 'center', flexWrap: 'wrap', borderBottom: '1px solid #eef1f5', paddingBottom: 6 }}>
              <span style={{ flex: 1 }}>{s.seq}. {s.name}</span>
              {s.signature && <img src={s.signature} alt="" style={{ height: 40, background: '#fff' }} />}
              <span style={{ color: stColor[s.status] ?? '#333', fontWeight: 600 }}>{t(`es.sst.${s.status}`)}{s.signed_at ? ` · ${formatDateTime(s.signed_at)}` : ''}</span>
            </div>))}
        </div>
        {d.me.can_sign && !declining && (
          <div style={box}>
            <b>{t('pub.sign.yourSignature', { name: d.me.name })}</b>
            <div role="tablist" style={{ display: 'flex', gap: 8 }}>
              <button role="tab" aria-selected={method === 'draw'} className={`mx-btn ${method === 'draw' ? 'main' : ''}`} onClick={() => setMethod('draw')}>✍ {t('pub.sign.draw')}</button>
              <button role="tab" aria-selected={method === 'type'} className={`mx-btn ${method === 'type' ? 'main' : ''}`} onClick={() => setMethod('type')}>⌨ {t('pub.sign.type')}</button>
            </div>
            {method === 'draw' ? <Pad onChange={setDrawn} /> : <>
              <input aria-label={t('pub.sign.type')} value={typed} onChange={(e) => setTyped(e.currentTarget.value)} style={{ font: 'inherit', fontSize: 16, padding: 10, borderRadius: 10, border: '1px solid #cbd5e1' }} />
              {signature && <img src={signature} alt="" style={{ maxWidth: '100%', height: 70, objectFit: 'contain', background: '#fbfcfe', borderRadius: 10 }} />}
            </>}
            <label style={{ display: 'flex', gap: 8, alignItems: 'flex-start' }}><input type="checkbox" checked={agree} onChange={(e) => setAgree(e.currentTarget.checked)} />
              <span>{t('pub.sign.agree')}</span></label>
            {err && <div className="mx-flash bad">{err}</div>}
            <button className="mx-btn main" style={{ minHeight: 50, fontSize: 17 }} disabled={busy || !agree || !signature} onClick={() => void sign()}>{t('pub.sign.sign')}</button>
            <button className="mx-btn" style={{ alignSelf: 'flex-start' }} onClick={() => setDeclining(true)}>{t('pub.sign.decline')}</button>
          </div>)}
        {declining && (
          <div style={box}>
            <b>{t('pub.sign.declineWhy')}</b>
            <textarea rows={3} value={reason} onChange={(e) => setReason(e.currentTarget.value)} style={{ font: 'inherit', padding: 10, borderRadius: 10, border: '1px solid #cbd5e1' }} />
            {err && <div className="mx-flash bad">{err}</div>}
            <div style={{ display: 'flex', gap: 8 }}><button className="mx-btn" onClick={() => setDeclining(false)}>{t('pub.sign.back')}</button>
              <button className="mx-btn" style={{ color: '#b42318' }} onClick={() => void decline()}>{t('pub.sign.declineConfirm')}</button></div>
          </div>)}
        {d.me.status === 'signed' && <div style={box}>
          <span>✅ {t('pub.sign.youSigned', { at: d.me.signed_at ? formatDateTime(d.me.signed_at) : '' })}</span>
          {d.me.signature && <img src={d.me.signature} alt="" style={{ height: 60, alignSelf: 'flex-start' }} />}
          {!q.has_certificate && <small>{t('pub.sign.waitOthers')}</small>}
        </div>}
        {q.has_certificate && <a className="mx-btn main" style={{ textAlign: 'center', textDecoration: 'none', minHeight: 46, display: 'grid', placeItems: 'center' }}
          href={`/api/v1/sign/${token}/certificate`} target="_blank" rel="noopener noreferrer">⬇ {t('pub.sign.download')}</a>}
        <small style={{ textAlign: 'center', color: '#5b6b7f' }}>{t('pub.sign.legal')}</small>
      </div>
    </div>
  );
}

// ======================= opt-out =======================

export function Optout({ token }: { token: string }) {
  const { t, i18n } = useTranslation();
  const [d, setD] = useState<{ company: string; company_ar: string; logo: string; locale: string; channel: string; contact: string; done: boolean } | null | undefined>(undefined);
  const [reason, setReason] = useState('');
  const [done, setDone] = useState(false);
  const [err, setErr] = useState('');
  useEffect(() => {
    api<{ company: string; company_ar: string; logo: string; locale: string; channel: string; contact: string; done: boolean }>(`/optout/${token}`).then((x) => {
      setD(x);
      if (x.locale && !localStorage.getItem('paxve.lang')) applyLang(x.locale);
    }).catch(() => setD(null));
  }, [token]);
  if (d === undefined) return <div style={{ padding: 20 }}>…</div>;
  if (d === null) return <div style={{ ...box, maxWidth: 460, margin: '40px auto' }}>{t('pub.sign.notFound')}</div>;
  const company = i18n.language === 'ar' && d.company_ar ? d.company_ar : d.company;
  async function stop(all: boolean) {
    setErr('');
    try { await api(`/optout/${token}`, { method: 'POST', body: { all, reason } }); setDone(true); } catch (e) { setErr(errorText(t, e)); }
  }
  return (
    <div dir={i18n.dir()} style={{ minHeight: '100vh', background: '#f4f6f9', padding: 12 }}>
      <div style={{ ...box, maxWidth: 460, margin: '40px auto' }}>
        {d.logo && <img src={d.logo} alt="" style={{ maxHeight: 56, alignSelf: 'center' }} />}
        <h2 style={{ margin: 0, textAlign: 'center' }}>{company}</h2>
        {done || d.done ? <p style={{ textAlign: 'center' }}>✅ {t('pub.optout.done')}</p> : <>
          <p style={{ margin: 0 }}>{t('pub.optout.question', { contact: d.contact, channel: t(`mk.ch.${d.channel}`) })}</p>
          <textarea rows={2} placeholder={t('pub.optout.reason')} value={reason} onChange={(e) => setReason(e.currentTarget.value)} style={{ font: 'inherit', padding: 10, borderRadius: 10, border: '1px solid #cbd5e1' }} />
          {err && <div className="mx-flash bad">{err}</div>}
          <button className="mx-btn main" style={{ minHeight: 46 }} onClick={() => void stop(false)}>{t('pub.optout.stop', { channel: t(`mk.ch.${d.channel}`) })}</button>
          <button className="mx-btn" onClick={() => void stop(true)}>{t('pub.optout.stopAll')}</button>
        </>}
      </div>
    </div>
  );
}
