import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, can, type Me } from '../../api';
import { formatDate, formatDateTime } from '../../util';
import { useBase, useLoad, useRun } from '../wh/Wh';
import { CustomerPicker } from '../prj/Prj';

// E-sign (Magic-style screen): signature requests for quotations, subscription agreements, contract texts or uploaded
// PDFs; signers in order with their own link (sent by WhatsApp / SMS / e-mail), reminders, the audit trail and the
// certificate PDF.

type Msg = { ok: boolean; text: string } | null;
/** a phone as wa.me wants it (Lebanese local numbers get 961) */
const waNumber = (p: string) => { let d = p.replace(/[^\d]/g, ''); if (d.startsWith('00')) d = d.slice(2); else if (d.length <= 8) d = '961' + d.replace(/^0/, ''); return d; };
function Flash({ m }: { m: Msg }) {
  return m ? <div className={`mx-flash ${m.ok ? 'good' : 'bad'}`}>{m.text}</div> : null;
}

interface Req {
  id: string; number: number; title: string; doc_type: string; sales_order_id: string | null; quote_number: number | null; order_number: number | null; subscription_id: string | null;
  doc_hash: string; message: string; ordered: boolean; channel: string; status: string; expires_on: string | null; auto_confirm: boolean; remind_days: number;
  sent_at: string | null; completed_at: string | null; certificate_id: string | null; created_at: string; created_by_name: string | null; signers: number; signed: number;
  signer_names: string | null; body?: string;
}
interface Signer { id: string; seq: number; name: string; email: string; phone: string; status: string; viewed_at: string | null; signed_at: string | null; declined_at: string | null;
  decline_reason: string; sign_method: string; typed_name: string; signature: string; ip: string; user_agent: string; notified_at: string | null; reminders: number; link: string }
interface Ev { event: string; created_at: string; ip: string; user_agent: string; detail: string; signer: string | null; by_user: string | null }

const STATUSES = ['', 'draft', 'sent', 'completed', 'declined', 'cancelled', 'expired'];
const stColor: Record<string, string> = { draft: '#6b7280', sent: '#1d4ed8', completed: '#0f7b4b', declined: '#b42318', cancelled: '#6b7280', expired: '#a15c00' };
const sColor: Record<string, string> = { signed: '#0f7b4b', declined: '#b42318', pending: '#a15c00', viewed: '#1d4ed8', waiting: '#6b7280' };

export default function ESign({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [status, setStatus] = useState('');
  const [rows, reload] = useLoad<Req[]>(base && `${base}/esign/requests?status=${status}`, []);
  const [sel, setSel] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  return (
    <div className="mx-col">
      <div className="mx-strip"><b>{t('es.title')}</b><span className="mx-sp" /><small style={{ opacity: 0.8 }}>{t('es.help')}</small></div>
      <div style={{ flex: 1, minHeight: 0, overflow: 'auto', padding: 6 }}>
        <div className="mx-row" style={{ gap: 8, alignItems: 'flex-start', flexWrap: 'wrap' }}>
          <section className="mx-box" style={{ flex: '1 1 520px', minWidth: 0 }}>
            <h3 style={{ display: 'flex', gap: 6, alignItems: 'center' }}>{t('es.requests')}<span className="mx-sp" />
              {can(me, 'esign.create') && <button className="mx-btn main" onClick={() => { setCreating(true); setSel(null); }}>{t('es.new')}</button>}</h3>
            <div className="mx-row" style={{ gap: 4, padding: 6, flexWrap: 'wrap' }}>
              {STATUSES.map((s) => <button key={s || 'all'} className={`mx-btn sm ${status === s ? 'main' : ''}`} onClick={() => setStatus(s)}>{t(`es.st.${s || 'all'}`)}</button>)}
            </div>
            <div style={{ overflowX: 'auto' }}>
              <table className="mx-table">
                <thead><tr><th>#</th><th>{t('es.doc')}</th><th>{t('es.type')}</th><th>{t('es.signers')}</th><th>{t('es.status')}</th><th>{t('es.date')}</th></tr></thead>
                <tbody>{rows.map((r) => (
                  <tr key={r.id} className={sel === r.id ? 'sel' : ''} style={{ cursor: 'pointer' }} onClick={() => { setSel(r.id); setCreating(false); }}>
                    <td>{r.number}</td><td><b>{r.title}</b></td><td>{t(`es.dt.${r.doc_type}`)}</td><td>{r.signed}/{r.signers} <small>{r.signer_names}</small></td>
                    <td><span className="mx-pill" style={{ background: stColor[r.status], color: '#fff' }}>{t(`es.st.${r.status}`)}</span></td>
                    <td>{formatDate(r.completed_at ?? r.sent_at ?? r.created_at)}</td></tr>))}
                  {rows.length === 0 && <tr><td colSpan={6} className="mx-empty">{t('es.none')}</td></tr>}</tbody>
              </table>
            </div>
          </section>
          {creating && <NewRequest onDone={(id) => { setCreating(false); setSel(id); reload(); }} onClose={() => setCreating(false)} />}
          {sel && <Detail me={me} id={sel} onChange={reload} onClose={() => setSel(null)} />}
        </div>
      </div>
    </div>
  );
}

function NewRequest({ onDone, onClose }: { onDone: (id: string) => void; onClose: () => void }) {
  const { t } = useTranslation();
  const base = useBase();
  const [docType, setDocType] = useState('quote');
  const [quotes] = useLoad<{ rows: { id: string; number: number; customer_id: string; customer_name: string; customer_phone: string; customer_email: string; total: string; currency: string; status: string }[] }>(
    base && `${base}/sales/orders?kind=quotes`, { rows: [] });
  const [f, setF] = useState({ title: '', sales_order_id: '', subscription_id: '', body: '', message: '', channel: 'whatsapp', expires_on: '', remind_days: '3', ordered: true, auto_confirm: true, send: true });
  const [cust, setCust] = useState({ id: '', text: '' });
  const [subs] = useLoad<{ subscriptions: { id: string; plan_name?: string; status: string; start_date: string }[] } | null>(base && cust.id && docType === 'subscription' ? `${base}/customers/${cust.id}` : null, null);
  const [file, setFile] = useState<{ filename: string; data: string } | null>(null);
  const [signers, setSigners] = useState([{ name: '', phone: '', email: '', customer_id: '' }]);
  const [msg, run] = useRun();
  const pickQuote = (id: string) => {
    const q = quotes.rows.find((x) => x.id === id);
    setF({ ...f, sales_order_id: id });
    if (q && !signers[0].name) setSigners([{ name: q.customer_name, phone: q.customer_phone ?? '', email: q.customer_email ?? '', customer_id: q.customer_id }, ...signers.slice(1)]);
  };
  const ok = signers.every((s) => s.name.trim()) && (docType === 'quote' ? !!f.sales_order_id : docType === 'subscription' ? !!f.subscription_id : docType === 'upload' ? !!file : !!f.title.trim() && !!f.body.trim());
  return (
    <section className="mx-box" style={{ flex: '1 1 520px', minWidth: 0 }}>
      <h3 style={{ display: 'flex' }}>{t('es.new')}<span className="mx-sp" /><button className="mx-btn sm" onClick={onClose}>{t('es.close')}</button></h3>
      <div style={{ padding: 6, display: 'flex', flexDirection: 'column', gap: 6 }}>
        <div role="radiogroup" className="mx-row" style={{ gap: 4, flexWrap: 'wrap' }}>
          {['quote', 'subscription', 'contract', 'upload'].map((d) => <button key={d} role="radio" aria-checked={docType === d} className={`mx-btn sm ${docType === d ? 'main' : ''}`} onClick={() => setDocType(d)}>{t(`es.dt.${d}`)}</button>)}
        </div>
        {docType === 'quote' && <label className="mx-fld">{t('es.quote')}<select value={f.sales_order_id} onChange={(e) => pickQuote(e.currentTarget.value)}><option value="">—</option>
          {quotes.rows.map((q) => <option key={q.id} value={q.id}>#{q.number} · {q.customer_name} · {q.total} {q.currency}</option>)}</select></label>}
        {docType === 'subscription' && <div className="mx-row" style={{ gap: 6, alignItems: 'end', flexWrap: 'wrap' }}>
          <CustomerPicker value={cust} label={t('es.customer')} onChange={(c) => { setCust(c); setF({ ...f, subscription_id: '' }); }} />
          <label className="mx-fld">{t('es.subscription')}<select value={f.subscription_id} onChange={(e) => {
            setF({ ...f, subscription_id: e.currentTarget.value });
            if (!signers[0].name) setSigners([{ name: cust.text.replace(/^\S+\s/, ''), phone: '', email: '', customer_id: cust.id }]);
          }}><option value="">—</option>{(subs?.subscriptions ?? []).map((s) => <option key={s.id} value={s.id}>{s.plan_name ?? s.id.slice(0, 8)} · {formatDate(s.start_date)} · {s.status}</option>)}</select></label>
        </div>}
        {(docType === 'contract' || docType === 'subscription' || docType === 'upload') && <label className="mx-fld">{t('es.docTitle')}<input value={f.title} onChange={(e) => setF({ ...f, title: e.currentTarget.value })} /></label>}
        {docType === 'contract' && <label className="mx-fld">{t('es.body')}<textarea rows={10} value={f.body} onChange={(e) => setF({ ...f, body: e.currentTarget.value })} /></label>}
        {docType === 'subscription' && <label className="mx-fld">{t('es.bodyOptional')}<textarea rows={4} value={f.body} onChange={(e) => setF({ ...f, body: e.currentTarget.value })} /></label>}
        {docType === 'upload' && <label className="mx-fld">{t('es.file')}<input type="file" accept="application/pdf,image/*" onChange={(e) => {
          const fl = e.currentTarget.files?.[0]; if (!fl) return;
          const r = new FileReader(); r.onload = () => setFile({ filename: fl.name, data: String(r.result) }); r.readAsDataURL(fl);
        }} /></label>}
        <b>{t('es.signers')}</b>
        {signers.map((s, i) => (
          <div key={i} className="mx-row" style={{ gap: 6, alignItems: 'end', flexWrap: 'wrap' }}>
            <span>{i + 1}.</span>
            <label className="mx-fld">{t('es.name')}<input value={s.name} onChange={(e) => setSigners(signers.map((x, j) => (j === i ? { ...x, name: e.currentTarget.value } : x)))} /></label>
            <label className="mx-fld">{t('es.phone')}<input dir="ltr" style={{ width: 120 }} value={s.phone} onChange={(e) => setSigners(signers.map((x, j) => (j === i ? { ...x, phone: e.currentTarget.value } : x)))} /></label>
            <label className="mx-fld">{t('es.email')}<input dir="ltr" style={{ width: 170 }} value={s.email} onChange={(e) => setSigners(signers.map((x, j) => (j === i ? { ...x, email: e.currentTarget.value } : x)))} /></label>
            {signers.length > 1 && <button className="mx-btn sm" onClick={() => setSigners(signers.filter((_, j) => j !== i))}>✕</button>}
          </div>))}
        {signers.length < 10 && <button className="mx-btn sm" style={{ alignSelf: 'flex-start' }} onClick={() => setSigners([...signers, { name: '', phone: '', email: '', customer_id: '' }])}>+ {t('es.addSigner')}</button>}
        <div className="mx-row" style={{ gap: 8, flexWrap: 'wrap', alignItems: 'end' }}>
          <label className="mx-fld">{t('es.channel')}<select value={f.channel} onChange={(e) => setF({ ...f, channel: e.currentTarget.value })}>{['whatsapp', 'sms', 'email', 'none'].map((c) => <option key={c} value={c}>{t(`es.ch.${c}`)}</option>)}</select></label>
          <label className="mx-fld">{t('es.expires')}<input type="date" dir="ltr" value={f.expires_on} onChange={(e) => setF({ ...f, expires_on: e.currentTarget.value })} /></label>
          <label className="mx-fld">{t('es.remind')}<input dir="ltr" style={{ width: 60 }} value={f.remind_days} onChange={(e) => setF({ ...f, remind_days: e.currentTarget.value.replace(/[^\d]/g, '') })} /></label>
          <label className="mx-fld" style={{ flexDirection: 'row', gap: 4, alignItems: 'center' }}><input type="checkbox" checked={f.ordered} onChange={(e) => setF({ ...f, ordered: e.currentTarget.checked })} />{t('es.ordered')}</label>
          {docType === 'quote' && <label className="mx-fld" style={{ flexDirection: 'row', gap: 4, alignItems: 'center' }}><input type="checkbox" checked={f.auto_confirm} onChange={(e) => setF({ ...f, auto_confirm: e.currentTarget.checked })} />{t('es.autoConfirm')}</label>}
          <label className="mx-fld" style={{ flexDirection: 'row', gap: 4, alignItems: 'center' }}><input type="checkbox" checked={f.send} onChange={(e) => setF({ ...f, send: e.currentTarget.checked })} />{t('es.sendNow')}</label>
        </div>
        <label className="mx-fld">{t('es.message')}<textarea rows={2} value={f.message} onChange={(e) => setF({ ...f, message: e.currentTarget.value })} /></label>
        <Flash m={msg} />
        <button className="mx-btn main" disabled={!ok} onClick={() => void run(api<{ request: Req }>(`${base}/esign/requests`, { method: 'POST', body: {
          doc_type: docType, title: f.title, sales_order_id: docType === 'quote' ? f.sales_order_id : '', subscription_id: docType === 'subscription' ? f.subscription_id : '',
          body: docType === 'contract' || docType === 'subscription' ? f.body : '', file: docType === 'upload' ? file : undefined, message: f.message, channel: f.channel,
          expires_on: f.expires_on, remind_days: Number(f.remind_days || 0), ordered: f.ordered, auto_confirm: docType === 'quote' && f.auto_confirm, send: f.send,
          signers: signers.map((s) => ({ name: s.name, phone: s.phone, email: s.email, customer_id: s.customer_id })) } }), (x) => onDone((x as { request: Req }).request.id))}>
          {f.send ? t('es.createSend') : t('es.createDraft')}</button>
      </div>
    </section>
  );
}

function Detail({ me, id, onChange, onClose }: { me: Me; id: string; onChange: () => void; onClose: () => void }) {
  const { t } = useTranslation();
  const base = useBase();
  const [d, reload] = useLoad<{ request: Req; signers: Signer[]; events: Ev[] } | null>(base && `${base}/esign/requests/${id}`, null);
  const [msg, run] = useRun(() => { reload(); onChange(); });
  if (!d) return null;
  const q = d.request;
  const act = (a: string) => void run(api(`${base}/esign/requests/${id}/${a}`, { method: 'POST', body: {} }));
  const edit = can(me, 'esign.create');
  return (
    <section className="mx-box" style={{ flex: '1 1 560px', minWidth: 0 }}>
      <h3 style={{ display: 'flex', gap: 6, alignItems: 'center', flexWrap: 'wrap' }}>#{q.number} {q.title}
        <span className="mx-pill" style={{ background: stColor[q.status], color: '#fff' }}>{t(`es.st.${q.status}`)}</span><span className="mx-sp" />
        <button className="mx-btn sm" onClick={onClose}>{t('es.close')}</button></h3>
      <div style={{ padding: 6, display: 'flex', flexDirection: 'column', gap: 6 }}>
        <small>{t(`es.dt.${q.doc_type}`)}{q.quote_number ? ` · Q-${q.quote_number}` : ''}{q.order_number ? ` → SO-${q.order_number}` : ''} · {t(`es.ch.${q.channel}`)}
          {q.expires_on ? ` · ${t('es.expires')} ${formatDate(q.expires_on)}` : ''}{q.auto_confirm ? ` · ${t('es.autoConfirm')}` : ''}</small>
        <small style={{ wordBreak: 'break-all' }} dir="ltr">SHA-256 {q.doc_hash}</small>
        <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
          {edit && q.status === 'draft' && <button className="mx-btn main" onClick={() => act('send')}>{t('es.send')}</button>}
          {edit && q.status === 'sent' && <button className="mx-btn" onClick={() => act('remind')}>{t('es.remindNow')}</button>}
          {edit && (q.status === 'draft' || q.status === 'sent') && <button className="mx-btn" style={{ color: '#b42318' }} onClick={() => { if (window.confirm(t('es.cancelQ'))) act('cancel'); }}>{t('es.cancel')}</button>}
          <a className="mx-btn" target="_blank" rel="noopener noreferrer" href={`/api/v1${base}/esign/requests/${id}/certificate`}>⬇ {q.status === 'completed' ? t('es.certificate') : t('es.draftPdf')}</a>
          {q.sales_order_id && <a className="mx-btn" href="#/quotes">{t('es.openQuote')}</a>}
        </div>
        <Flash m={msg} />
        <table className="mx-table">
          <thead><tr><th>#</th><th>{t('es.name')}</th><th>{t('es.status')}</th><th>{t('es.signature')}</th><th>{t('es.link')}</th></tr></thead>
          <tbody>{d.signers.map((s) => (
            <tr key={s.id}>
              <td>{s.seq}</td><td><b>{s.name}</b><div><small dir="ltr">{s.phone} {s.email}</small></div></td>
              <td><span style={{ color: sColor[s.status], fontWeight: 600 }}>{t(`es.sst.${s.status}`)}</span>
                {s.signed_at && <div><small>{formatDateTime(s.signed_at)} · IP {s.ip} · {t(`es.m.${s.sign_method}`)}</small></div>}
                {s.declined_at && <div><small className="red">{s.decline_reason}</small></div>}
                {s.reminders > 0 && <div><small>{t('es.remindersN', { n: s.reminders })}</small></div>}</td>
              <td>{s.signature && <img src={s.signature} alt="" style={{ height: 40, background: '#fff' }} />}</td>
              <td>{(q.status === 'sent' || q.status === 'completed') && <div style={{ display: 'flex', gap: 4, flexWrap: 'wrap' }}>
                <button className="mx-btn sm" onClick={() => void navigator.clipboard?.writeText(s.link)}>{t('es.copy')}</button>
                {s.phone && <a className="mx-btn sm" target="_blank" rel="noopener noreferrer" href={`https://wa.me/${waNumber(s.phone)}?text=${encodeURIComponent(`${q.title}\n${s.link}`)}`}>🟢</a>}
              </div>}</td>
            </tr>))}</tbody>
        </table>
        {q.body && <details><summary>{t('es.document')}</summary><pre style={{ whiteSpace: 'pre-wrap', fontFamily: 'inherit', margin: 0, maxHeight: 300, overflow: 'auto' }}>{q.body}</pre></details>}
        <b>{t('es.trail')}</b>
        <table className="mx-table"><tbody>{d.events.map((e, i) => (
          <tr key={i}><td>{formatDateTime(e.created_at)}</td><td>{t(`es.ev.${e.event}`)}</td><td>{e.signer ?? e.by_user ?? ''}</td><td dir="ltr"><small>{e.ip}</small></td>
            <td><small style={{ wordBreak: 'break-all' }}>{e.detail}</small></td></tr>))}</tbody></table>
      </div>
    </section>
  );
}
