import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { IconMessageCircle, IconChecklist, IconHistory, IconPaperclip, IconTrash, IconCheck, IconMail } from '@tabler/icons-react';
import { api } from '../api';
import { useCompany } from '../company';
import { errorText, formatDate, formatDateTime, today } from '../util';
import Attachments from './Attachments';
import RecordMail from './RecordMail';

interface Msg { id: string; body: string; author: string | null; created_at: string; mine: boolean; mentioned?: { id: string; name: string }[] }
interface Act {
  id: string; kind: string; summary: string; note: string; due_date: string; assigned_to: string; assigned_name: string | null;
  done_at: string | null; done_note: string; state: 'done' | 'overdue' | 'today' | 'planned'; created_by_name: string | null;
}
interface Change { field: string; from: unknown; to: unknown }
interface Ev { at: string; actor: string | null; action: string; changes: Change[] }
interface Data { messages: Msg[]; activities: Act[]; history: Ev[]; files: number }
interface Person { id: string; full_name: string }

export const ACT_KINDS = ['call', 'meeting', 'todo', 'followup', 'email', 'visit'];

function show(v: unknown): string {
  if (v === null || v === undefined || v === '') return '—';
  if (typeof v === 'boolean') return v ? '✓' : '✗';
  return String(v);
}

/** Chatter (Odoo style) on any record: comments with @mentions, activities, change history and files. */
export default function Chatter({ type, id, readOnly }: { type: string; id: string; readOnly?: boolean }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const base = company ? `/companies/${company.id}` : null;
  const [tab, setTab] = useState<'msg' | 'act' | 'hist' | 'files' | 'mail'>('msg');
  const [d, setD] = useState<Data | null>(null);
  const [err, setErr] = useState('');
  const [text, setText] = useState('');
  const [people, setPeople] = useState<Person[]>([]);
  const [mentions, setMentions] = useState<Person[]>([]);
  const [na, setNa] = useState({ kind: 'call', summary: '', due_date: today(), assigned_to: '', note: '' });

  const load = useCallback(() => {
    if (!base) return;
    api<Data>(`${base}/chatter?type=${type}&id=${id}`).then((x) => { setD(x); setErr(''); }).catch((e) => setErr(errorText(t, e)));
  }, [base, type, id, t]);
  useEffect(load, [load]);
  useEffect(() => { if (base) api<Person[]>(`${base}/mentionable`).then(setPeople).catch(() => setPeople([])); }, [base]);

  // "@na" at the end of the text → suggestions
  const q = useMemo(() => { const m = /@([^\s@]{1,30})$/.exec(text); return m ? m[1].toLowerCase() : null; }, [text]);
  const sugg = q === null ? [] : people.filter((p) => p.full_name.toLowerCase().includes(q)).slice(0, 6);
  function pick(p: Person) {
    setText(text.replace(/@([^\s@]{1,30})$/, `@${p.full_name} `));
    if (!mentions.some((m) => m.id === p.id)) setMentions([...mentions, p]);
  }

  async function run(p: Promise<unknown>, after?: () => void) {
    try { await p; setErr(''); after?.(); load(); } catch (e) { setErr(errorText(t, e)); }
  }
  function send() {
    if (!base || !text.trim()) return;
    const used = mentions.filter((m) => text.includes(`@${m.full_name}`)).map((m) => m.id);
    void run(api(`${base}/chatter`, { method: 'POST', body: { type, id, body: text.trim(), mentions: used } }), () => { setText(''); setMentions([]); });
  }
  function addAct() {
    if (!base || !na.summary.trim()) return;
    const body: Record<string, string> = { entity_type: type, entity_id: id, kind: na.kind, summary: na.summary.trim(), due_date: na.due_date, note: na.note };
    if (na.assigned_to) body.assigned_to = na.assigned_to;
    void run(api(`${base}/record-activities`, { method: 'POST', body }), () => setNa({ ...na, summary: '', note: '' }));
  }
  function done(a: Act) {
    if (!base) return;
    const note = window.prompt(t('gp.act.doneNote')) ?? '';
    void run(api(`${base}/record-activities/${a.id}/done`, { method: 'POST', body: { note } }));
  }

  const openActs = d?.activities.filter((a) => !a.done_at).length ?? 0;
  const tabs: { k: typeof tab; icon: typeof IconHistory; label: string; n?: number }[] = [
    { k: 'msg', icon: IconMessageCircle, label: t('gp.ch.comments'), n: d?.messages.length },
    { k: 'act', icon: IconChecklist, label: t('gp.ch.activities'), n: openActs },
    { k: 'hist', icon: IconHistory, label: t('gp.ch.history') },
    { k: 'files', icon: IconPaperclip, label: t('gp.ch.files'), n: d?.files },
    { k: 'mail', icon: IconMail, label: t('op.mail.tab') },
  ];

  return (
    <div className="mx-chatter no-print">
      <div className="mx-chatter-tabs" role="tablist">
        {tabs.map((x) => (
          <button key={x.k} role="tab" aria-selected={tab === x.k} className="mx-mode" onClick={() => setTab(x.k)}>
            <x.icon size={14} aria-hidden /> {x.label}{x.n ? <span className="mx-badge">{x.n}</span> : null}
          </button>
        ))}
      </div>
      {tab === 'msg' && (
        <div className="mx-chatter-body">
          {!readOnly && (
            <div style={{ position: 'relative' }}>
              <textarea rows={2} value={text} placeholder={t('gp.ch.placeholder')} onChange={(e) => setText(e.currentTarget.value)}
                onKeyDown={(e) => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) send(); }} style={{ width: '100%', boxSizing: 'border-box' }} />
              {sugg.length > 0 && (
                <div className="mx-list mx-suggest">{sugg.map((p) => (
                  <button key={p.id} type="button" className="mx-link" onClick={() => pick(p)}>@{p.full_name}</button>
                ))}</div>
              )}
              <div className="mx-row" style={{ gap: 6, justifyContent: 'space-between' }}>
                <small style={{ opacity: 0.7 }}>{t('gp.ch.hint')}</small>
                <button type="button" className="mx-btn sm main" disabled={!text.trim()} onClick={send}>{t('gp.ch.send')}</button>
              </div>
            </div>
          )}
          {d?.messages.length === 0 && <small style={{ opacity: 0.7 }}>{t('gp.ch.noComments')}</small>}
          {d?.messages.map((m) => (
            <div key={m.id} className="mx-note">
              <small><b><bdi>{m.author ?? '—'}</bdi></b> · <bdi>{formatDateTime(m.created_at)}</bdi>
                {m.mine && !readOnly && (
                  <button type="button" className="mx-link" aria-label={t('gp.ch.delete')} title={t('gp.ch.delete')}
                    onClick={() => { if (base && window.confirm(t('gp.ch.deleteQ'))) void run(api(`${base}/chatter/${m.id}`, { method: 'DELETE' })); }}>
                    <IconTrash size={12} /></button>
                )}
              </small>
              <div style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{m.body}</div>
            </div>
          ))}
        </div>
      )}
      {tab === 'act' && (
        <div className="mx-chatter-body">
          {!readOnly && (
            <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap', alignItems: 'end' }}>
              <label>{t('gp.act.kind')}<select value={na.kind} onChange={(e) => setNa({ ...na, kind: e.currentTarget.value })}>
                {ACT_KINDS.map((k) => <option key={k} value={k}>{t(`gp.act.k.${k}`)}</option>)}</select></label>
              <label style={{ flex: 1, minWidth: 140 }}>{t('gp.act.summary')}<input value={na.summary} onChange={(e) => setNa({ ...na, summary: e.currentTarget.value })} /></label>
              <label>{t('gp.act.due')}<input type="date" dir="ltr" value={na.due_date} onChange={(e) => setNa({ ...na, due_date: e.currentTarget.value })} /></label>
              <label>{t('gp.act.who')}<select value={na.assigned_to} onChange={(e) => setNa({ ...na, assigned_to: e.currentTarget.value })}>
                <option value="">{t('gp.act.me')}</option>
                {people.map((p) => <option key={p.id} value={p.id}>{p.full_name}</option>)}</select></label>
              <button type="button" className="mx-btn sm main" disabled={!na.summary.trim()} onClick={addAct}>{t('gp.act.add')}</button>
            </div>
          )}
          {d?.activities.length === 0 && <small style={{ opacity: 0.7 }}>{t('gp.act.none')}</small>}
          {d?.activities.map((a) => (
            <div key={a.id} className={`mx-note mx-act-${a.state}`}>
              <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
                <span className={`mx-pill ${a.state === 'overdue' ? 'warn' : a.state === 'done' ? 'on' : ''}`}>{t(`gp.act.s.${a.state}`)}</span>
                <b>{t(`gp.act.k.${a.kind}`)}</b> <bdi>{a.summary}</bdi>
                <small><bdi dir="ltr">{formatDate(a.due_date)}</bdi> · <bdi>{a.assigned_name ?? ''}</bdi></small>
                <span className="mx-sp" />
                {!a.done_at && !readOnly && <button type="button" className="mx-btn sm" onClick={() => done(a)}><IconCheck size={13} /> {t('gp.act.done')}</button>}
              </div>
              {a.note && <div style={{ whiteSpace: 'pre-wrap' }}>{a.note}</div>}
              {a.done_note && <small style={{ opacity: 0.8 }}>✓ {a.done_note}</small>}
            </div>
          ))}
        </div>
      )}
      {tab === 'hist' && (
        <div className="mx-chatter-body">
          {d?.history.length === 0 && <small style={{ opacity: 0.7 }}>{t('gp.ch.noHistory')}</small>}
          {d?.history.map((e, i) => (
            <div key={i} className="mx-note">
              <small><b><bdi>{e.actor ?? t('gp.ch.system')}</bdi></b> · <bdi>{formatDateTime(e.at)}</bdi> · {t(`gp.ch.a.${e.action}`, { defaultValue: e.action })}</small>
              {e.changes.map((c) => (
                <div key={c.field} style={{ fontSize: 12.5, overflowWrap: 'anywhere' }}>
                  <code>{c.field}</code>: <bdi>{show(c.from)}</bdi> → <b><bdi>{show(c.to)}</bdi></b>
                </div>
              ))}
            </div>
          ))}
        </div>
      )}
      {tab === 'files' && <div className="mx-chatter-body"><Attachments type={type} id={id} readOnly={readOnly} /></div>}
      {tab === 'mail' && <div className="mx-chatter-body"><RecordMail type={type} id={id} readOnly={readOnly} onSent={load} /></div>}
      {err && <div className="mx-flash bad">{err}</div>}
    </div>
  );
}
