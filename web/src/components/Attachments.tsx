import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { IconPaperclip, IconTrash, IconDownload, IconEye } from '@tabler/icons-react';
import { api, API_BASE } from '../api';
import { useCompany } from '../company';
import { errorText, formatDateTime } from '../util';
import { saveFile } from '../native';

interface Att { id: string; filename: string; mime: string; size: number; note: string; created_at: string; created_by_name: string | null }

const MAX = 10 * 1024 * 1024;

/** Files attached to a document (scanned bill, contract, meter photo…): add, open, download, remove. */
export default function Attachments({ type, id, readOnly }: { type: string; id: string; readOnly?: boolean }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const base = company ? `/companies/${company.id}` : null;
  const [rows, setRows] = useState<Att[]>([]);
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);
  const input = useRef<HTMLInputElement>(null);

  const load = useCallback(() => {
    if (!base) return;
    api<Att[]>(`${base}/attachments?type=${type}&id=${id}`).then(setRows).catch(() => setRows([]));
  }, [base, type, id]);
  useEffect(load, [load]);

  async function add(files: FileList | null) {
    if (!base || !files) return;
    setErr('');
    setBusy(true);
    try {
      for (const f of Array.from(files)) {
        if (f.size > MAX) { setErr(t('oa.att.tooBig', { name: f.name })); continue; }
        const data = await new Promise<string>((res, rej) => {
          const r = new FileReader();
          r.onload = () => res(String(r.result));
          r.onerror = () => rej(r.error);
          r.readAsDataURL(f);
        });
        await api(`${base}/attachments`, { method: 'POST', body: { type, id, filename: f.name, mime: f.type, data } });
      }
      load();
    } catch (e) {
      setErr(errorText(t, e));
    } finally {
      setBusy(false);
      if (input.current) input.current.value = '';
    }
  }

  async function fetchFile(a: Att): Promise<Blob> {
    const r = await fetch(`${API_BASE}${base}/attachments/${a.id}/file`, { credentials: 'same-origin', headers: { 'X-Requested-With': 'paxve' } });
    if (!r.ok) throw new Error(String(r.status));
    return r.blob();
  }
  async function view(a: Att) {
    try {
      const b = await fetchFile(a);
      const url = URL.createObjectURL(new Blob([b], { type: a.mime }));
      window.open(url, '_blank', 'noopener');
      window.setTimeout(() => URL.revokeObjectURL(url), 60_000);
    } catch (e) { setErr(errorText(t, e)); }
  }
  async function download(a: Att) {
    try { await saveFile(a.filename, await fetchFile(a), a.mime); } catch (e) { setErr(errorText(t, e)); }
  }
  async function remove(a: Att) {
    if (!base || !window.confirm(t('oa.att.removeQ', { name: a.filename }))) return;
    try { await api(`${base}/attachments/${a.id}`, { method: 'DELETE' }); load(); } catch (e) { setErr(errorText(t, e)); }
  }
  const viewable = (m: string) => (m.startsWith('image/') && m !== 'image/svg+xml') || m === 'application/pdf';

  return (
    <div className="mx-att no-print" style={{ border: '1px solid var(--mx-line, #ccd)', borderRadius: 4, padding: 6, display: 'flex', flexDirection: 'column', gap: 4 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
        <IconPaperclip size={16} aria-hidden />
        <b style={{ fontSize: 13 }}>{t('oa.att.title')}{rows.length ? ` (${rows.length})` : ''}</b>
        <span style={{ flex: 1 }} />
        {!readOnly && (
          <>
            <input ref={input} type="file" multiple hidden onChange={(e) => void add(e.currentTarget.files)} accept="image/*,application/pdf,.doc,.docx,.xls,.xlsx,.csv,.txt" />
            <button type="button" className="mx-btn sm" disabled={busy} onClick={() => input.current?.click()}>{busy ? t('oa.att.uploading') : t('oa.att.add')}</button>
          </>
        )}
      </div>
      {rows.length === 0 ? <small style={{ opacity: 0.7 }}>{t('oa.att.none')}</small> : rows.map((a) => (
        <div key={a.id} style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 13, flexWrap: 'wrap' }}>
          <span style={{ flex: 1, minWidth: 120, overflowWrap: 'anywhere' }}>{a.filename}</span>
          <small style={{ opacity: 0.7 }}><bdi dir="ltr">{Math.max(1, Math.round(a.size / 1024))} KB</bdi> · <bdi>{formatDateTime(a.created_at)}</bdi>{a.created_by_name ? <> · <bdi>{a.created_by_name}</bdi></> : null}</small>
          {viewable(a.mime) && <button type="button" className="mx-btn sm" title={t('oa.att.view')} aria-label={t('oa.att.view')} onClick={() => void view(a)}><IconEye size={14} /></button>}
          <button type="button" className="mx-btn sm" title={t('oa.att.download')} aria-label={t('oa.att.download')} onClick={() => void download(a)}><IconDownload size={14} /></button>
          {!readOnly && <button type="button" className="mx-btn sm" title={t('oa.att.remove')} aria-label={t('oa.att.remove')} onClick={() => void remove(a)}><IconTrash size={14} /></button>}
        </div>
      ))}
      {err && <div className="mx-flash bad">{err}</div>}
    </div>
  );
}
