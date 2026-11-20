import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api } from '../api';
import { useCompany } from '../company';
import { errorText, formatDateTime } from '../util';

// Phase 9: e-mail from any record (ticket, quotation, invoice, purchase order…) with a PDF of the document and its
// files attached; replies come back here (and to the chatter) by the thread mark in the subject.

interface Mail {
  id: string; direction: 'in' | 'out'; address: string; cc: string; subject: string; body: string; files: { id: string; filename: string }[];
  created_at: string; author: string | null; delivery: string | null; delivery_error: string | null;
}
interface Data { mails: Mail[]; configured: boolean; default_to: string; default_subject: string; reply_to: string }
interface File { id: string; filename: string; size: number }

export default function RecordMail({ type, id, readOnly, onSent }: { type: string; id: string; readOnly?: boolean; onSent?: () => void }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const base = company ? `/companies/${company.id}` : null;
  const [d, setD] = useState<Data | null>(null);
  const [files, setFiles] = useState<File[]>([]);
  const [f, setF] = useState({ to: '', cc: '', subject: '', body: '', pdf: true, ids: [] as string[] });
  const [err, setErr] = useState('');
  const [ok, setOk] = useState('');
  const [busy, setBusy] = useState(false);
  const [open, setOpen] = useState<string | null>(null);

  const load = useCallback(() => {
    if (!base) return;
    api<Data>(`${base}/record-mail?type=${type}&id=${id}`).then((x) => {
      setD(x);
      setF((p) => ({ ...p, to: p.to || x.default_to || '', subject: p.subject || x.default_subject || '' }));
    }).catch((e) => setErr(errorText(t, e)));
    api<File[]>(`${base}/attachments?type=${type}&id=${id}`).then(setFiles).catch(() => setFiles([]));
  }, [base, type, id, t]);
  useEffect(load, [load]);

  async function send() {
    if (!base) return;
    setBusy(true); setErr(''); setOk('');
    try {
      await api(`${base}/record-mail`, { method: 'POST', body: { type, id, to: f.to.trim(), cc: f.cc.trim(), subject: f.subject.trim(), body: f.body,
        attach_pdf: f.pdf, attachment_ids: f.ids } });
      setOk(t('op.mail.queued', { to: f.to.trim() }));
      setF({ ...f, body: '', ids: [] });
      load();
      onSent?.();
    } catch (e) { setErr(errorText(t, e)); } finally { setBusy(false); }
  }

  if (!d) return err ? <div className="mx-flash bad">{err}</div> : <small>…</small>;
  const can = f.to.includes('@') && f.subject.trim() !== '' && f.body.trim() !== '';
  return (
    <div className="mx-col" style={{ gap: 6 }}>
      {!d.configured && <div className="mx-flash bad">{t('op.mail.notConfigured')}</div>}
      {!readOnly && d.configured && (
        <div className="mx-col" style={{ gap: 4 }}>
          <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
            <label className="mx-fld" style={{ flex: '1 1 200px' }}>{t('op.mail.to')}<input dir="ltr" type="email" value={f.to} onChange={(e) => setF({ ...f, to: e.currentTarget.value })} /></label>
            <label className="mx-fld" style={{ flex: '1 1 200px' }}>{t('op.mail.cc')}<input dir="ltr" value={f.cc} placeholder="a@x.com, b@y.com" onChange={(e) => setF({ ...f, cc: e.currentTarget.value })} /></label>
          </div>
          <label className="mx-fld">{t('op.mail.subject')}<input value={f.subject} onChange={(e) => setF({ ...f, subject: e.currentTarget.value })} /></label>
          <label className="mx-fld">{t('op.mail.body')}<textarea rows={4} value={f.body} onChange={(e) => setF({ ...f, body: e.currentTarget.value })} /></label>
          <div className="mx-row" style={{ gap: 10, flexWrap: 'wrap', alignItems: 'center' }}>
            <label><input type="checkbox" checked={f.pdf} onChange={(e) => setF({ ...f, pdf: e.currentTarget.checked })} /> {t('op.mail.pdf')}</label>
            {files.map((x) => (
              <label key={x.id}><input type="checkbox" checked={f.ids.includes(x.id)}
                onChange={(e) => setF({ ...f, ids: e.currentTarget.checked ? [...f.ids, x.id] : f.ids.filter((i) => i !== x.id) })} /> 📎 <bdi>{x.filename}</bdi></label>
            ))}
            <span className="mx-sp" />
            <button type="button" className="mx-btn main" disabled={!can || busy} title={!can ? t('op.mail.needFields') : undefined} onClick={() => void send()}>{t('op.mail.send')}</button>
          </div>
          <small style={{ opacity: 0.75 }}>{d.reply_to ? t('op.mail.replyHint', { box: d.reply_to }) : t('op.mail.replyHintSubject')}</small>
        </div>
      )}
      {ok && <div className="mx-flash good">{ok}</div>}
      {err && <div className="mx-flash bad">{err}</div>}
      {d.mails.length === 0 && <small style={{ opacity: 0.7 }}>{t('op.mail.none')}</small>}
      {d.mails.map((m) => (
        <div key={m.id} className="mx-note" style={{ borderInlineStart: `3px solid ${m.direction === 'in' ? '#2f9e44' : '#1d4ed8'}` }}>
          <small>
            <b>{m.direction === 'in' ? t('op.mail.in') : t('op.mail.out')}</b> · <bdi dir="ltr">{m.address}</bdi> · <bdi>{formatDateTime(m.created_at)}</bdi>
            {m.author && <> · <bdi>{m.author}</bdi></>}
            {m.direction === 'out' && m.delivery && <> · <span className={`mx-pill ${m.delivery === 'failed' ? 'warn' : m.delivery === 'sent' || m.delivery === 'delivered' ? 'on' : ''}`}>{t(`op.mail.st.${m.delivery}`, { defaultValue: m.delivery })}</span></>}
          </small>
          <div><button type="button" className="mx-link" onClick={() => setOpen(open === m.id ? null : m.id)}><b><bdi>{m.subject}</bdi></b></button></div>
          {open === m.id && <div style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{m.body}</div>}
          {m.files.length > 0 && <small>📎 {m.files.map((x) => x.filename).join(', ')}</small>}
          {m.delivery_error && <small className="red"> {m.delivery_error}</small>}
        </div>
      ))}
    </div>
  );
}
