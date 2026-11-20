import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { api, can, type Me } from '../../api';
import { formatDateTime } from '../../util';
import { useBase, useLoad, useRun } from '../wh/Wh';
import { usePeople } from '../prj/Prj';

// No-code automation: WHEN (a record is created / changed, or on a schedule) IF (conditions) THEN (actions).
// Ready-made rules, a test (dry run) that changes nothing, and the run log.

type Msg = { ok: boolean; text: string } | null;
function Flash({ m }: { m: Msg }) {
  return m ? <div className={`mx-flash ${m.ok ? 'good' : 'bad'}`}>{m.text}</div> : null;
}

interface Field { name: string; kind: string; enum?: string[] }
interface ModelMeta { fields: Field[]; triggers: string[]; settable: Record<string, string[]>; entity: string }
interface Meta { models: Record<string, ModelMeta>; ops: string[]; actions: string[]; templates: { key: string; model: string; trigger: string }[] }
interface Cond { field: string; op: string; value: string }
interface Action { type: string; summary?: string; kind?: string; days?: number; assign?: string; channel?: string; body?: string; field?: string; value?: string; tag?: string; webhook_id?: string }
interface Rule {
  id: string; name: string; model: string; trigger: string; watch_fields: string[]; conditions: Cond[]; actions: Action[]; repeat_days: number; template_key: string;
  is_active: boolean; paused_reason: string; run_count: number; last_run_at: string | null; created_by_name: string | null; errors_7d: number;
}
interface Run { id: string; rule_id: string; rule_name: string; model: string; record_id: string | null; record_label: string; status: string; detail: { actions?: Record<string, unknown>[]; error?: string; checked?: number; matched?: number }; created_at: string }

const MODES = ['rules', 'templates', 'log'] as const;
type Mode = typeof MODES[number];
const NO_VALUE = ['empty', 'not_empty', 'changed'];

export default function Automation({ me, initial }: { me: Me; initial?: string }) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<Mode>((MODES as readonly string[]).includes(initial ?? '') ? initial as Mode : 'rules');
  const base = useBase();
  const [meta] = useLoad<Meta | null>(base && `${base}/automation/meta`, null);
  const [rules, reload] = useLoad<Rule[]>(base && `${base}/automation/rules`, []);
  return (
    <div className="mx-col">
      <div className="mx-strip"><b>{t(`au.m.${mode}`)}</b><span className="mx-sp" /><small style={{ opacity: 0.8 }}>{t(`au.help.${mode}`)}</small></div>
      <div className="mx-modes" role="tablist">
        {MODES.map((m) => <button key={m} role="tab" className="mx-mode" aria-selected={mode === m} onClick={() => setMode(m)}>{t(`au.m.${m}`)}</button>)}
      </div>
      <div style={{ flex: 1, minHeight: 0, overflow: 'auto', padding: 6 }}>
        {meta && mode === 'rules' && <Rules me={me} meta={meta} rules={rules} reload={reload} />}
        {meta && mode === 'templates' && <Templates me={me} meta={meta} rules={rules} reload={() => { reload(); setMode('rules'); }} />}
        {mode === 'log' && <Log />}
      </div>
    </div>
  );
}

function ruleText(t: TFunction, r: Pick<Rule, 'model' | 'trigger' | 'conditions' | 'actions'>) {
  const when = t(`au.when.${r.trigger}`, { model: t(`au.model.${r.model}`) });
  const iff = r.conditions.map((c) => `${t(`au.f.${c.field}`, { defaultValue: c.field })} ${t(`au.op.${c.op}`)}${NO_VALUE.includes(c.op) ? '' : ` ${c.value}`}`).join(` ${t('au.and')} `);
  const then = r.actions.map((a) => t(`au.a.${a.type}`)).join(' + ');
  return `${when}${iff ? ` · ${t('au.if')} ${iff}` : ''} → ${then}`;
}

function Rules({ me, meta, rules, reload }: { me: Me; meta: Meta; rules: Rule[]; reload: () => void }) {
  const { t } = useTranslation();
  const base = useBase();
  const [edit, setEdit] = useState<Rule | null>(null);
  const [test, setTest] = useState<{ rule: string; checked: number; matched: { record_id: string; label: string; actions: Record<string, unknown>[] }[] } | null>(null);
  const [msg, run] = useRun(reload);
  const manage = can(me, 'settings.manage');
  const blank: Rule = { id: '', name: '', model: 'ticket', trigger: 'created', watch_fields: [], conditions: [], actions: [{ type: 'notify', assign: 'assignee', body: '' }], repeat_days: 0,
    template_key: '', is_active: true, paused_reason: '', run_count: 0, last_run_at: null, created_by_name: null, errors_7d: 0 };
  return (
    <div className="mx-row" style={{ gap: 8, alignItems: 'flex-start', flexWrap: 'wrap' }}>
      <section className="mx-box" style={{ flex: '999 1 520px', minWidth: 0 }}>
        <h3>{t('au.m.rules')} {manage && <button className="mx-btn sm main" onClick={() => setEdit(blank)}>+ {t('au.new')}</button>}</h3>
        <Flash m={msg} />
        <div style={{ overflowX: 'auto' }}>
          <table className="mx-table">
            <thead><tr><th>{t('au.active')}</th><th>{t('au.name')}</th><th>{t('au.what')}</th><th className="num">{t('au.runs')}</th><th>{t('au.last')}</th><th /></tr></thead>
            <tbody>
              {rules.map((r) => (
                <tr key={r.id}>
                  <td><input type="checkbox" aria-label={t('au.active')} disabled={!manage} checked={r.is_active}
                    onChange={(e) => void run(api(`${base}/automation/rules/${r.id}`, { method: 'PATCH', body: { is_active: e.currentTarget.checked } }))} /></td>
                  <td><b>{r.name}</b>{r.paused_reason && <small className="red"> · {t(`au.paused.${r.paused_reason}`, { defaultValue: r.paused_reason })}</small>}{r.errors_7d > 0 && <small className="red"> · {t('au.errorsN', { n: r.errors_7d })}</small>}</td>
                  <td><small>{ruleText(t, r)}</small></td><td className="num">{r.run_count}</td><td>{r.last_run_at ? formatDateTime(r.last_run_at) : ''}</td>
                  <td style={{ whiteSpace: 'nowrap' }}>
                    <button className="mx-btn sm" onClick={() => void run(api<{ checked: number; matched: { record_id: string; label: string; actions: Record<string, unknown>[] }[] }>(`${base}/automation/rules/${r.id}/test`, { method: 'POST', body: {} }),
                      (x) => setTest({ rule: r.name, ...(x as { checked: number; matched: { record_id: string; label: string; actions: Record<string, unknown>[] }[] }) }))}>{t('au.test')}</button>
                    {manage && r.trigger === 'scheduled' && <button className="mx-btn sm" onClick={() => void run(api(`${base}/automation/rules/${r.id}/run`, { method: 'POST', body: {} }))}>{t('au.runNow')}</button>}
                    {manage && <button className="mx-btn sm" onClick={() => setEdit(r)}>{t('au.edit')}</button>}
                    {manage && <button className="mx-btn sm" onClick={() => void run(api(`${base}/automation/rules/${r.id}`, { method: 'DELETE' }))}>{t('au.delete')}</button>}
                  </td>
                </tr>))}
              {rules.length === 0 && <tr><td colSpan={6} className="mx-empty">{t('au.none')}</td></tr>}
            </tbody>
          </table>
        </div>
        {test && (
          <div style={{ padding: 6 }}>
            <h4 style={{ margin: '6px 0' }}>{t('au.testResult', { rule: test.rule, checked: test.checked, n: test.matched.length })} <button className="mx-btn sm" onClick={() => setTest(null)}>×</button></h4>
            <table className="mx-table"><tbody>
              {test.matched.map((m) => <tr key={m.record_id}><td>{m.label}</td><td><small>{m.actions.map((a) => `${t(`au.a.${String(a.type)}`)}${a.skipped ? ` (${t('au.skipped')}: ${String(a.skipped)})` : ''}${a.text ? `: ${String(a.text)}` : a.summary ? `: ${String(a.summary)}` : ''}`).join(' · ')}</small></td></tr>)}
              {test.matched.length === 0 && <tr><td className="mx-empty">{t('au.noMatch')}</td></tr>}
            </tbody></table>
          </div>)}
      </section>
      {edit && <Editor meta={meta} rule={edit} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); reload(); }} />}
    </div>
  );
}

function Editor({ meta, rule, onClose, onSaved }: { meta: Meta; rule: Rule; onClose: () => void; onSaved: () => void }) {
  const { t } = useTranslation();
  const base = useBase();
  const people = usePeople();
  const [hooks] = useLoad<{ id: string; url: string }[]>(base && `${base}/webhooks`, []);
  const [r, setR] = useState<Rule>(rule);
  const [msg, run] = useRun(onSaved);
  const m = meta.models[r.model];
  const fields = m?.fields ?? [];
  const setCond = (i: number, c: Partial<Cond>) => setR({ ...r, conditions: r.conditions.map((x, j) => (j === i ? { ...x, ...c } : x)) });
  const setAct = (i: number, a: Partial<Action>) => setR({ ...r, actions: r.actions.map((x, j) => (j === i ? { ...x, ...a } : x)) });
  const who = (v: string | undefined, onChange: (v: string) => void) => (
    <select aria-label={t('au.whoL')} value={v ?? 'assignee'} onChange={(e) => onChange(e.currentTarget.value)}>
      <option value="assignee">{t('au.who.assignee')}</option><option value="collector">{t('au.who.collector')}</option><option value="creator">{t('au.who.creator')}</option>
      {people.map((u) => <option key={u.id} value={u.id}>{u.full_name}</option>)}
    </select>);
  const save = () => {
    const body = { name: r.name, model: r.model, trigger: r.trigger, watch_fields: r.trigger === 'updated' ? r.watch_fields : [], conditions: r.conditions,
      actions: r.actions.map((a) => ({ ...a, days: a.days ? Number(a.days) : undefined })), repeat_days: Number(r.repeat_days) || 0, is_active: r.is_active };
    void run(api(r.id ? `${base}/automation/rules/${r.id}` : `${base}/automation/rules`, { method: r.id ? 'PATCH' : 'POST', body }));
  };
  return (
    <section className="mx-box" style={{ flex: '1 1 480px', minWidth: 0 }}>
      <h3>{r.id ? t('au.edit') : t('au.new')}<span className="mx-sp" /><button className="mx-btn sm" onClick={onClose}>×</button></h3>
      <div style={{ padding: 6, display: 'flex', flexDirection: 'column', gap: 8 }}>
        <label className="mx-fld">{t('au.name')}<input value={r.name} onChange={(e) => setR({ ...r, name: e.currentTarget.value })} /></label>
        <fieldset style={{ border: '1px solid #dde3ea', borderRadius: 6 }}><legend>{t('au.whenT')}</legend>
          <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
            <label className="mx-fld">{t('au.modelT')}<select value={r.model} onChange={(e) => { const md = e.currentTarget.value; const tr = meta.models[md].triggers.includes(r.trigger) ? r.trigger : meta.models[md].triggers[0]; setR({ ...r, model: md, trigger: tr, conditions: [], watch_fields: [] }); }}>
              {Object.keys(meta.models).map((k) => <option key={k} value={k}>{t(`au.model.${k}`)}</option>)}</select></label>
            <label className="mx-fld">{t('au.triggerT')}<select value={r.trigger} onChange={(e) => setR({ ...r, trigger: e.currentTarget.value })}>
              {(m?.triggers ?? []).map((k) => <option key={k} value={k}>{t(`au.trig.${k}`)}</option>)}</select></label>
            {r.trigger === 'scheduled' && <label className="mx-fld">{t('au.repeat')}<input dir="ltr" style={{ width: 60 }} value={String(r.repeat_days)} onChange={(e) => setR({ ...r, repeat_days: Number(e.currentTarget.value.replace(/\D/g, '')) || 0 })} /></label>}
          </div>
          {r.trigger === 'updated' && (
            <div className="mx-row" style={{ gap: 8, flexWrap: 'wrap', marginTop: 4 }}><small>{t('au.watch')}:</small>
              {fields.map((f) => <label key={f.name} style={{ display: 'flex', gap: 3, alignItems: 'center' }}><input type="checkbox" checked={r.watch_fields.includes(f.name === 'assignee_id' ? (r.model === 'ticket' ? 'assigned_to' : r.model === 'service_job' ? 'technician_id' : 'assignee_id') : f.name)}
                onChange={(e) => { const col = f.name === 'assignee_id' ? (r.model === 'ticket' ? 'assigned_to' : r.model === 'service_job' ? 'technician_id' : 'assignee_id') : f.name; setR({ ...r, watch_fields: e.currentTarget.checked ? [...r.watch_fields, col] : r.watch_fields.filter((x) => x !== col) }); }} />{t(`au.f.${f.name}`, { defaultValue: f.name })}</label>)}
            </div>)}
        </fieldset>
        <fieldset style={{ border: '1px solid #dde3ea', borderRadius: 6 }}><legend>{t('au.ifT')}</legend>
          {r.conditions.map((c, i) => {
            const f = fields.find((x) => x.name === c.field);
            return (
              <div key={i} className="mx-row" style={{ gap: 4, marginBottom: 4, flexWrap: 'wrap' }}>
                <select aria-label={t('au.field')} value={c.field} onChange={(e) => setCond(i, { field: e.currentTarget.value, value: '' })}>{fields.map((x) => <option key={x.name} value={x.name}>{t(`au.f.${x.name}`, { defaultValue: x.name })}</option>)}</select>
                <select aria-label={t('au.opT')} value={c.op} onChange={(e) => setCond(i, { op: e.currentTarget.value })}>{meta.ops.filter((o) => o !== 'changed' || r.trigger === 'updated').map((o) => <option key={o} value={o}>{t(`au.op.${o}`)}</option>)}</select>
                {!NO_VALUE.includes(c.op) && (f?.enum ? <select aria-label={t('au.value')} value={c.value} onChange={(e) => setCond(i, { value: e.currentTarget.value })}><option value="">—</option>{f.enum.map((v) => <option key={v} value={v}>{v}</option>)}</select>
                  : f?.kind === 'bool' ? <select aria-label={t('au.value')} value={c.value} onChange={(e) => setCond(i, { value: e.currentTarget.value })}><option value="true">{t('au.yes')}</option><option value="false">{t('au.no')}</option></select>
                    : <input aria-label={t('au.value')} dir="ltr" style={{ width: 110 }} value={c.value} onChange={(e) => setCond(i, { value: e.currentTarget.value })} />)}
                <button className="mx-btn sm" aria-label={t('au.remove')} onClick={() => setR({ ...r, conditions: r.conditions.filter((_, j) => j !== i) })}>×</button>
              </div>);
          })}
          <button className="mx-btn sm" disabled={fields.length === 0} onClick={() => setR({ ...r, conditions: [...r.conditions, { field: fields[0].name, op: 'eq', value: fields[0].kind === 'bool' ? 'true' : '' }] })}>+ {t('au.addCond')}</button>
        </fieldset>
        <fieldset style={{ border: '1px solid #dde3ea', borderRadius: 6 }}><legend>{t('au.thenT')}</legend>
          {r.actions.map((a, i) => (
            <div key={i} style={{ borderBottom: '1px dashed #dde3ea', paddingBottom: 4, marginBottom: 4 }}>
              <div className="mx-row" style={{ gap: 4, flexWrap: 'wrap', alignItems: 'center' }}>
                <select aria-label={t('au.actT')} value={a.type} onChange={(e) => setAct(i, { type: e.currentTarget.value })}>
                  {meta.actions.filter((x) => (x !== 'purchase_request' || r.model === 'item_stock') && (x !== 'message' || !['item_stock', 'vehicle'].includes(r.model)) && (x !== 'set_field' || Object.keys(m?.settable ?? {}).length > 0))
                    .map((x) => <option key={x} value={x}>{t(`au.a.${x}`)}</option>)}</select>
                {(a.type === 'activity' || a.type === 'notify') && who(a.assign, (v) => setAct(i, { assign: v }))}
                {a.type === 'activity' && <><select aria-label={t('au.kind')} value={a.kind ?? 'todo'} onChange={(e) => setAct(i, { kind: e.currentTarget.value })}>
                  {['todo', 'call', 'visit', 'meeting', 'followup', 'email'].map((k) => <option key={k} value={k}>{t(`au.k.${k}`)}</option>)}</select>
                  <label style={{ display: 'flex', gap: 3, alignItems: 'center' }}>{t('au.inDays')}<input dir="ltr" style={{ width: 40 }} value={String(a.days ?? 0)} onChange={(e) => setAct(i, { days: Number(e.currentTarget.value.replace(/\D/g, '')) || 0 })} /></label></>}
                {a.type === 'message' && <select aria-label={t('au.channel')} value={a.channel ?? 'whatsapp'} onChange={(e) => setAct(i, { channel: e.currentTarget.value })}>{['whatsapp', 'sms', 'email'].map((c) => <option key={c} value={c}>{c}</option>)}</select>}
                {a.type === 'set_field' && <>
                  <select aria-label={t('au.field')} value={a.field ?? ''} onChange={(e) => setAct(i, { field: e.currentTarget.value, value: '' })}><option value="">—</option>{Object.keys(m?.settable ?? {}).map((f) => <option key={f} value={f}>{t(`au.f.${f}`, { defaultValue: f })}</option>)}</select>
                  {a.field && ((m?.settable[a.field] ?? []).length > 0
                    ? <select aria-label={t('au.value')} value={a.value ?? ''} onChange={(e) => setAct(i, { value: e.currentTarget.value })}><option value="">—</option>{m.settable[a.field].map((v) => <option key={v} value={v}>{v}</option>)}</select>
                    : <select aria-label={t('au.value')} value={a.value ?? ''} onChange={(e) => setAct(i, { value: e.currentTarget.value })}><option value="">—</option>{people.map((u) => <option key={u.id} value={u.id}>{u.full_name}</option>)}</select>)}</>}
                {a.type === 'tag' && <input aria-label={t('au.tag')} placeholder={t('au.tag')} value={a.tag ?? ''} onChange={(e) => setAct(i, { tag: e.currentTarget.value })} />}
                {a.type === 'webhook' && <select aria-label={t('au.webhook')} value={a.webhook_id ?? ''} onChange={(e) => setAct(i, { webhook_id: e.currentTarget.value })}><option value="">—</option>{hooks.map((x) => <option key={x.id} value={x.id}>{x.url}</option>)}</select>}
                <button className="mx-btn sm" aria-label={t('au.remove')} disabled={r.actions.length === 1} onClick={() => setR({ ...r, actions: r.actions.filter((_, j) => j !== i) })}>×</button>
              </div>
              {(a.type === 'activity') && <input style={{ width: '100%', marginTop: 4 }} placeholder={t('au.summary')} value={a.summary ?? ''} onChange={(e) => setAct(i, { summary: e.currentTarget.value })} />}
              {(a.type === 'message' || a.type === 'notify') && <textarea rows={2} style={{ width: '100%', marginTop: 4 }} placeholder={t('au.body')} value={a.body ?? ''} onChange={(e) => setAct(i, { body: e.currentTarget.value })} />}
            </div>))}
          <button className="mx-btn sm" disabled={r.actions.length >= 10} onClick={() => setR({ ...r, actions: [...r.actions, { type: 'notify', assign: 'assignee', body: '' }] })}>+ {t('au.addAct')}</button>
          <div><small style={{ opacity: 0.8 }}>{t('au.placeholders')}: {fields.map((f) => `{${f.name}}`).join(' ')} {'{label}'}</small></div>
        </fieldset>
        <small>{ruleText(t, r)}</small>
        <div className="mx-row" style={{ gap: 6 }}>
          <button className="mx-btn main" disabled={!r.name.trim()} onClick={save}>{t('au.save')}</button>
          <button className="mx-btn" onClick={onClose}>{t('au.cancel')}</button>
        </div>
        <Flash m={msg} />
      </div>
    </section>
  );
}

function Templates({ me, meta, rules, reload }: { me: Me; meta: Meta; rules: Rule[]; reload: () => void }) {
  const { t } = useTranslation();
  const base = useBase();
  const [msg, run] = useRun(reload);
  const have = new Set(rules.map((r) => r.template_key));
  return (
    <div>
      <Flash m={msg} />
      <div className="mx-apps">
        {meta.templates.map((x) => (
          <article key={x.key} className="mx-appc">
            <header><div><b>{t(`au.tpl.${x.key}.name`)}</b></div></header>
            <p>{t(`au.tpl.${x.key}.desc`)}</p>
            <div className="mx-arow">
              <small>{t(`au.model.${x.model}`)} · {t(`au.trig.${x.trigger}`)}</small>
              {have.has(x.key) ? <span className="mx-pill on">{t('au.added')}</span>
                : can(me, 'settings.manage') && <button className="mx-btn sm main" onClick={() => void run(api(`${base}/automation/templates/${x.key}`, { method: 'POST' }))}>{t('au.use')}</button>}
            </div>
          </article>))}
      </div>
    </div>
  );
}

function Log() {
  const { t } = useTranslation();
  const base = useBase();
  const [status, setStatus] = useState('');
  const [rows] = useLoad<Run[]>(base && `${base}/automation/runs?status=${status}`, []);
  return (
    <section className="mx-box">
      <h3>{t('au.m.log')}</h3>
      <div className="mx-row" style={{ gap: 6, padding: 6 }}>
        <select aria-label={t('au.status')} value={status} onChange={(e) => setStatus(e.currentTarget.value)}>
          <option value="">{t('au.all')}</option>{['done', 'error', 'dry'].map((s) => <option key={s} value={s}>{t(`au.rs.${s}`)}</option>)}</select>
      </div>
      <div style={{ overflowX: 'auto' }}>
        <table className="mx-table">
          <thead><tr><th>{t('au.at')}</th><th>{t('au.name')}</th><th>{t('au.record')}</th><th>{t('au.status')}</th><th>{t('au.detail')}</th></tr></thead>
          <tbody>
            {rows.map((x) => (
              <tr key={x.id}><td>{formatDateTime(x.created_at)}</td><td>{x.rule_name}</td><td>{x.record_label}</td>
                <td className={x.status === 'error' ? 'red' : ''}>{t(`au.rs.${x.status}`, { defaultValue: x.status })}</td>
                <td><small>{x.detail.error ? x.detail.error : x.detail.actions ? x.detail.actions.map((a) => `${t(`au.a.${String(a.type)}`)}${a.skipped ? ` (${t('au.skipped')})` : ''}`).join(' · ')
                  : x.detail.checked != null ? t('au.dryN', { c: x.detail.checked, n: x.detail.matched }) : ''}</small></td></tr>))}
            {rows.length === 0 && <tr><td colSpan={5} className="mx-empty">{t('au.none')}</td></tr>}
          </tbody>
        </table>
      </div>
    </section>
  );
}
