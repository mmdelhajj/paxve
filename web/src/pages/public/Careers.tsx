import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api } from '../../api';
import { applyLang } from '../../i18n';
import { errorText } from '../../util';

// The company's public careers page (#/careers/<slug>): open positions and the application form (CV upload).

interface Job { id: string; title: string; department: string; location: string; employment: string; description: string; requirements: string; salary_range: string }
interface Data { company: { name_en: string; name_ar: string; logo: string; default_locale: string; careers_intro: string; color: string; shop: boolean }; jobs: Job[] }

const box: React.CSSProperties = { background: '#fff', border: '1px solid #e3e8ef', borderRadius: 14, padding: 16, display: 'flex', flexDirection: 'column', gap: 10 };
const fld: React.CSSProperties = { display: 'flex', flexDirection: 'column', gap: 4, fontSize: 13, fontWeight: 600, color: '#3b4a5e' };
const inp: React.CSSProperties = { font: 'inherit', fontSize: 15, padding: 10, border: '1px solid #cbd5e1', borderRadius: 10, fontWeight: 400 };

export default function Careers({ slug }: { slug: string }) {
  const { t, i18n } = useTranslation();
  const [d, setD] = useState<Data | null | undefined>(undefined);
  const [open, setOpen] = useState<string | null>(null);
  useEffect(() => {
    api<Data>(`/careers/${slug}`).then((x) => {
      setD(x);
      if (x.company.default_locale && !localStorage.getItem('paxve.lang')) applyLang(x.company.default_locale);
    }).catch(() => setD(null));
  }, [slug]);
  if (d === undefined) return <div style={{ padding: 20 }}>…</div>;
  if (d === null) return <div style={{ ...box, maxWidth: 520, margin: '40px auto' }}>{t('pub.careers.closed')}</div>;
  const color = d.company.color || '#0f766e';
  const name = i18n.language === 'ar' && d.company.name_ar ? d.company.name_ar : d.company.name_en;
  return (
    <div dir={i18n.dir()} style={{ minHeight: '100vh', background: '#f4f6f9', color: '#0f1b2d' }}>
      <header style={{ background: color, color: '#fff', padding: '18px 16px', display: 'flex', gap: 12, alignItems: 'center' }}>
        {d.company.logo && <img src={d.company.logo} alt="" style={{ height: 40, background: '#fff', borderRadius: 8 }} />}
        <div><b style={{ fontSize: 19 }}>{name}</b><div style={{ opacity: 0.9 }}>{t('pub.careers.title')}</div></div>
      </header>
      <div style={{ maxWidth: 820, margin: '0 auto', padding: 12, display: 'flex', flexDirection: 'column', gap: 12 }}>
        {d.company.careers_intro && <div style={box}><p style={{ margin: 0, whiteSpace: 'pre-wrap' }}>{d.company.careers_intro}</p></div>}
        {d.jobs.length === 0 && <div style={box}>{t('pub.careers.none')}</div>}
        {d.jobs.map((j) => (
          <div key={j.id} style={box}>
            <div style={{ display: 'flex', gap: 10, alignItems: 'center', flexWrap: 'wrap' }}>
              <div style={{ flex: 1, minWidth: 200 }}>
                <h3 style={{ margin: 0 }}>{j.title}</h3>
                <small style={{ color: '#5b6b7f' }}>{[j.department, j.location, t(`tl.emp.${j.employment}`), j.salary_range].filter(Boolean).join(' · ')}</small>
              </div>
              <button className="mx-btn main" style={{ background: color, borderColor: color, minHeight: 42 }} onClick={() => setOpen(open === j.id ? null : j.id)}>
                {open === j.id ? t('pub.careers.hide') : t('pub.careers.apply')}</button>
            </div>
            {open === j.id && <>
              {j.description && <p style={{ margin: 0, whiteSpace: 'pre-wrap' }}>{j.description}</p>}
              {j.requirements && <><b>{t('pub.careers.requirements')}</b><p style={{ margin: 0, whiteSpace: 'pre-wrap' }}>{j.requirements}</p></>}
              <Apply slug={slug} job={j} color={color} />
            </>}
          </div>))}
        {d.company.shop && <a href={`#/shop/${slug}`} style={{ color, textAlign: 'center' }}>{t('pub.careers.shop')}</a>}
      </div>
    </div>
  );
}

function Apply({ slug, job, color }: { slug: string; job: Job; color: string }) {
  const { t } = useTranslation();
  const [f, setF] = useState({ name: '', email: '', phone: '', expected_salary: '', cover_letter: '', website: '' });
  const [cv, setCv] = useState<{ filename: string; data: string } | null>(null);
  const [err, setErr] = useState('');
  const [done, setDone] = useState(false);
  const [busy, setBusy] = useState(false);
  function pickFile(file: File | undefined) {
    setErr('');
    if (!file) { setCv(null); return; }
    if (file.size > 5 * 1024 * 1024) { setErr(t('pub.careers.tooBig')); return; }
    const r = new FileReader();
    r.onload = () => setCv({ filename: file.name, data: String(r.result) });
    r.readAsDataURL(file);
  }
  async function send() {
    setErr('');
    setBusy(true);
    try {
      await api(`/careers/${slug}/jobs/${job.id}/apply`, { method: 'POST', body: { ...f, cv: cv ?? undefined } });
      setDone(true);
    } catch (e) { setErr(errorText(t, e)); } finally { setBusy(false); }
  }
  if (done) return <div style={{ background: '#e6f4ec', color: '#0f7b4b', borderRadius: 10, padding: 12 }}>{t('pub.careers.thanks')}</div>;
  return (
    <div style={{ display: 'grid', gap: 10, gridTemplateColumns: 'repeat(auto-fit, minmax(220px, 1fr))' }}>
      <label style={fld}>{t('pub.shop.name')}<input style={inp} autoComplete="name" value={f.name} onChange={(e) => setF({ ...f, name: e.currentTarget.value })} /></label>
      <label style={fld}>{t('pub.shop.phone')}<input style={inp} type="tel" dir="ltr" autoComplete="tel" value={f.phone} onChange={(e) => setF({ ...f, phone: e.currentTarget.value })} /></label>
      <label style={fld}>{t('pub.shop.email')}<input style={inp} type="email" dir="ltr" autoComplete="email" value={f.email} onChange={(e) => setF({ ...f, email: e.currentTarget.value })} /></label>
      <label style={fld}>{t('pub.careers.salary')}<input style={inp} value={f.expected_salary} onChange={(e) => setF({ ...f, expected_salary: e.currentTarget.value })} /></label>
      <label style={{ ...fld, gridColumn: '1 / -1' }}>{t('pub.careers.letter')}<textarea style={inp} rows={4} value={f.cover_letter} onChange={(e) => setF({ ...f, cover_letter: e.currentTarget.value })} /></label>
      <label style={{ ...fld, gridColumn: '1 / -1' }}>{t('pub.careers.cv')}<input style={inp} type="file" accept=".pdf,.doc,.docx,image/*" onChange={(e) => pickFile(e.currentTarget.files?.[0])} /></label>
      <label aria-hidden="true" style={{ position: 'absolute', width: 1, height: 1, padding: 0, margin: -1, overflow: 'hidden', clip: 'rect(0,0,0,0)', whiteSpace: 'nowrap', border: 0 }}>Website
        <input tabIndex={-1} autoComplete="off" value={f.website} onChange={(e) => setF({ ...f, website: e.currentTarget.value })} /></label>
      {err && <div className="mx-flash bad" style={{ gridColumn: '1 / -1' }}>{err}</div>}
      <button className="mx-btn main" style={{ gridColumn: '1 / -1', minHeight: 48, background: color, borderColor: color }}
        disabled={busy || !f.name.trim() || (!f.email.trim() && !f.phone.trim())} onClick={() => void send()}>{t('pub.careers.send')}</button>
    </div>
  );
}
