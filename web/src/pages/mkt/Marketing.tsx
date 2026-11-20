import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, can, type Me } from '../../api';
import { formatDateTime } from '../../util';
import { useBase, useLoad, useRun } from '../wh/Wh';
import { CustomerPicker } from '../prj/Prj';

// Marketing (Magic-style screens): campaigns over WhatsApp / SMS / e-mail (audience by filters and / or a list,
// variables, tracked link, coupon, schedule, throttle, test, stats), mailing lists and the opt-out register.

type Msg = { ok: boolean; text: string } | null;
function Flash({ m }: { m: Msg }) {
  return m ? <div className={`mx-flash ${m.ok ? 'good' : 'bad'}`}>{m.text}</div> : null;
}

interface Audience { all_customers?: boolean; area_ids?: string[]; statuses?: string[]; balance_min?: string; overdue_days?: number; tags?: string[];
  bought_days?: number; not_bought_days?: number; has_subscription?: boolean }
interface Stats { queued: number; sent: number; delivered: number; failed: number; skipped: number; clicked: number; clicks: number; opted_out: number }
interface Campaign {
  id: string; number: number; name: string; channel: string; audience: Audience; list_id: string | null; list_name: string | null; subject: string; body: string;
  link_url: string; promotion_id: string | null; coupon: string | null; scheduled_at: string | null; throttle_per_min: number; status: string; recipients: number;
  skipped: number; sent_at: string | null; created_at: string; created_by_name: string | null; stats: Stats;
}
interface List { id: string; name: string; description: string; is_active: boolean; members: number }
interface Member { id: string; customer_id: string | null; customer_code: string | null; name: string; phone: string; email: string }
interface Optout { id: string; channel: string; customer_name: string | null; phone: string; email: string; reason: string; source: string; created_at: string }
interface Preview { total: number; reach: number; skipped: Record<string, number>; sample: { name: string; phone: string; email: string; skip?: string }[] }

const MODES = ['campaigns', 'lists', 'optouts'] as const;
type Mode = typeof MODES[number];
const CHANNELS = ['whatsapp', 'sms', 'email'];
const VARS = ['{name}', '{company}', '{coupon}', '{link}', '{shop}', '{optout}'];
const stColor: Record<string, string> = { draft: '#6b7280', scheduled: '#1d4ed8', sending: '#a15c00', sent: '#0f7b4b', cancelled: '#b42318' };

export default function Marketing({ me, initial }: { me: Me; initial?: string }) {
  const { t } = useTranslation();
  const base = useBase();
  const [mode, setMode] = useState<Mode>((MODES as readonly string[]).includes(initial ?? '') ? initial as Mode : 'campaigns');
  const [ov] = useLoad<{ campaigns: number; scheduled: number; lists: number; optouts: number; sent_30d: number; clicked_30d: number; coupons: number } | null>(base && `${base}/mkt/overview`, null);
  return (
    <div className="mx-col">
      <div className="mx-strip"><b>{t(`mk.m.${mode}`)}</b><span className="mx-sp" />
        {ov && <small style={{ opacity: 0.85 }}>{t('mk.ov', { sent: ov.sent_30d, clicked: ov.clicked_30d, scheduled: ov.scheduled, optouts: ov.optouts })}</small>}</div>
      <div className="mx-modes" role="tablist">
        {MODES.map((m) => <button key={m} role="tab" className="mx-mode" aria-selected={mode === m} onClick={() => setMode(m)}>{t(`mk.m.${m}`)}</button>)}
        <a className="mx-mode" href="#/retail/promos">{t('mk.coupons')}</a>
      </div>
      <div style={{ flex: 1, minHeight: 0, overflow: 'auto', padding: 6 }}>
        {mode === 'campaigns' && <Campaigns me={me} />}
        {mode === 'lists' && <Lists me={me} />}
        {mode === 'optouts' && <Optouts me={me} />}
      </div>
    </div>
  );
}

// ======================= campaigns =======================

function Campaigns({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [rows, reload] = useLoad<Campaign[]>(base && `${base}/mkt/campaigns`, []);
  const [sel, setSel] = useState<string | null>(null);
  const [msg, run] = useRun(reload);
  const [nf, setNf] = useState({ name: '', channel: 'whatsapp' });
  return (
    <div className="mx-row" style={{ gap: 8, alignItems: 'flex-start', flexWrap: 'wrap' }}>
      <section className="mx-box" style={{ flex: '1 1 460px', minWidth: 0 }}>
        <h3>{t('mk.m.campaigns')}</h3>
        {can(me, 'marketing.create') && <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
          <label className="mx-fld">{t('mk.name')}<input value={nf.name} onChange={(e) => setNf({ ...nf, name: e.currentTarget.value })} /></label>
          <label className="mx-fld">{t('mk.channel')}<select value={nf.channel} onChange={(e) => setNf({ ...nf, channel: e.currentTarget.value })}>{CHANNELS.map((c) => <option key={c} value={c}>{t(`mk.ch.${c}`)}</option>)}</select></label>
          <button className="mx-btn main" disabled={!nf.name.trim()} onClick={() => void run(api<Campaign>(`${base}/mkt/campaigns`, { method: 'POST', body: { ...nf, body: t('mk.defaultBody'), audience: { all_customers: true } } }),
            (x) => { setSel((x as Campaign).id); setNf({ ...nf, name: '' }); })}>{t('mk.new')}</button>
        </div>}
        <Flash m={msg} />
        <div style={{ overflowX: 'auto' }}>
          <table className="mx-table">
            <thead><tr><th>#</th><th>{t('mk.name')}</th><th>{t('mk.channel')}</th><th>{t('mk.status')}</th><th className="num">{t('mk.s.sent')}</th><th className="num">{t('mk.s.delivered')}</th>
              <th className="num">{t('mk.s.clicked')}</th><th className="num">{t('mk.s.failed')}</th><th>{t('mk.when')}</th></tr></thead>
            <tbody>
              {rows.map((k) => (
                <tr key={k.id} className={sel === k.id ? 'sel' : ''} style={{ cursor: 'pointer' }} onClick={() => setSel(k.id)}>
                  <td>{k.number}</td><td><b>{k.name}</b></td><td>{t(`mk.ch.${k.channel}`)}</td>
                  <td><span className="mx-pill" style={{ background: stColor[k.status], color: '#fff' }}>{t(`mk.st.${k.status}`)}</span></td>
                  <td className="num">{k.stats.sent}</td><td className="num">{k.stats.delivered}</td><td className="num">{k.stats.clicked}</td><td className="num">{k.stats.failed}</td>
                  <td>{k.sent_at ? formatDateTime(k.sent_at) : k.scheduled_at ? `⏰ ${formatDateTime(k.scheduled_at)}` : ''}</td>
                </tr>))}
              {rows.length === 0 && <tr><td colSpan={9} className="mx-empty">{t('mk.none')}</td></tr>}
            </tbody>
          </table>
        </div>
      </section>
      {sel && <CampaignPanel me={me} id={sel} onChange={reload} onClose={() => setSel(null)} />}
    </div>
  );
}

function AudienceEditor({ value, onChange, disabled }: { value: Audience; onChange: (a: Audience) => void; disabled: boolean }) {
  const { t } = useTranslation();
  const base = useBase();
  const [areas] = useLoad<{ id: string; name: string }[]>(base && `${base}/areas`, []);
  const n = (s: string) => (s.replace(/[^\d]/g, '') ? Number(s.replace(/[^\d]/g, '')) : undefined);
  return (
    <div className="mx-row" style={{ gap: 8, flexWrap: 'wrap', alignItems: 'end' }}>
      <label className="mx-fld" style={{ flexDirection: 'row', alignItems: 'center', gap: 4 }}><input type="checkbox" disabled={disabled} checked={!!value.all_customers}
        onChange={(e) => onChange({ ...value, all_customers: e.currentTarget.checked })} />{t('mk.a.all')}</label>
      <label className="mx-fld">{t('mk.a.area')}<select disabled={disabled} value={value.area_ids?.[0] ?? ''} onChange={(e) => onChange({ ...value, area_ids: e.currentTarget.value ? [e.currentTarget.value] : [] })}>
        <option value="">{t('mk.a.any')}</option>{areas.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}</select></label>
      <label className="mx-fld">{t('mk.a.status')}<select disabled={disabled} value={value.statuses?.[0] ?? ''} onChange={(e) => onChange({ ...value, statuses: e.currentTarget.value ? [e.currentTarget.value] : [] })}>
        <option value="">{t('mk.a.notClosed')}</option>{['active', 'suspended', 'closed'].map((s) => <option key={s} value={s}>{t(`mk.cs.${s}`)}</option>)}</select></label>
      <label className="mx-fld">{t('mk.a.balance')}<input dir="ltr" style={{ width: 80 }} disabled={disabled} value={value.balance_min ?? ''} onChange={(e) => onChange({ ...value, balance_min: e.currentTarget.value.replace(/[^\d.]/g, '') || undefined })} /></label>
      <label className="mx-fld">{t('mk.a.overdue')}<input dir="ltr" style={{ width: 70 }} disabled={disabled} value={value.overdue_days ?? ''} onChange={(e) => onChange({ ...value, overdue_days: n(e.currentTarget.value) })} /></label>
      <label className="mx-fld">{t('mk.a.bought')}<input dir="ltr" style={{ width: 70 }} disabled={disabled} value={value.bought_days ?? ''} onChange={(e) => onChange({ ...value, bought_days: n(e.currentTarget.value) })} /></label>
      <label className="mx-fld">{t('mk.a.notBought')}<input dir="ltr" style={{ width: 70 }} disabled={disabled} value={value.not_bought_days ?? ''} onChange={(e) => onChange({ ...value, not_bought_days: n(e.currentTarget.value) })} /></label>
      <label className="mx-fld">{t('mk.a.tags')}<input disabled={disabled} value={(value.tags ?? []).join(', ')} onChange={(e) => onChange({ ...value, tags: e.currentTarget.value.split(',').map((x) => x.trim()).filter(Boolean) })} /></label>
      <label className="mx-fld">{t('mk.a.sub')}<select disabled={disabled} value={value.has_subscription === undefined ? '' : String(value.has_subscription)}
        onChange={(e) => onChange({ ...value, has_subscription: e.currentTarget.value === '' ? undefined : e.currentTarget.value === 'true' })}>
        <option value="">{t('mk.a.any')}</option><option value="true">{t('mk.a.withSub')}</option><option value="false">{t('mk.a.noSub')}</option></select></label>
    </div>
  );
}

function CampaignPanel({ me, id, onChange, onClose }: { me: Me; id: string; onChange: () => void; onClose: () => void }) {
  const { t } = useTranslation();
  const base = useBase();
  const [d, reload] = useLoad<{ campaign: Campaign; recipients: { id: string; name: string; phone: string; email: string; status: string; skip_reason: string; clicks: number;
    opted_out_at: string | null; message_status: string | null; message_error: string | null; sent_at: string | null }[] } | null>(base && `${base}/mkt/campaigns/${id}`, null);
  const [lists] = useLoad<List[]>(base && `${base}/mkt/lists`, []);
  const [promos] = useLoad<{ id: string; name: string; coupon_code: string; is_active: boolean }[]>(base && `${base}/retail/promotions`, []);
  const [msg, run] = useRun(() => { reload(); onChange(); });
  const [edit, setEdit] = useState<Partial<Campaign> | null>(null);
  const [preview, setPreview] = useState<Preview | null>(null);
  const [to, setTo] = useState('');
  if (!d) return null;
  const k = { ...d.campaign, ...(edit ?? {}) } as Campaign;
  const editable = (d.campaign.status === 'draft' || d.campaign.status === 'scheduled') && can(me, 'marketing.create');
  const set = (p: Partial<Campaign>) => setEdit({ ...(edit ?? {}), ...p });
  const save = () => run(api(`${base}/mkt/campaigns/${id}`, { method: 'PATCH', body: {
    name: k.name, channel: k.channel, audience: k.audience ?? {}, list_id: k.list_id ?? '', subject: k.subject, body: k.body, link_url: k.link_url, promotion_id: k.promotion_id ?? '',
    scheduled_at: k.scheduled_at ?? '', throttle_per_min: k.throttle_per_min } }), () => setEdit(null));
  const localDT = k.scheduled_at ? new Date(new Date(k.scheduled_at).getTime() - new Date().getTimezoneOffset() * 60000).toISOString().slice(0, 16) : '';
  const s = d.campaign.stats;
  const bar = (n: number, color: string) => (d.campaign.recipients + d.campaign.skipped > 0 ? <span style={{ display: 'inline-block', height: 8, borderRadius: 4, background: color,
    width: `${Math.round((n * 100) / (d.campaign.recipients + d.campaign.skipped))}%`, minWidth: n ? 4 : 0 }} /> : null);
  return (
    <section className="mx-box" style={{ flex: '1 1 560px', minWidth: 0 }}>
      <h3 style={{ display: 'flex', gap: 6, alignItems: 'center', flexWrap: 'wrap' }}>#{k.number} {k.name}
        <span className="mx-pill" style={{ background: stColor[d.campaign.status], color: '#fff' }}>{t(`mk.st.${d.campaign.status}`)}</span>
        <span className="mx-sp" /><button className="mx-btn sm" onClick={onClose}>{t('mk.close')}</button></h3>
      <div style={{ padding: 6, display: 'flex', flexDirection: 'column', gap: 8 }}>
        {d.campaign.status !== 'draft' && d.campaign.status !== 'scheduled' && (
          <div className="mx-box" style={{ padding: 8 }}>
            {(['sent', 'delivered', 'clicked', 'failed', 'skipped', 'opted_out', 'queued'] as const).map((x) => (
              <div key={x} style={{ display: 'flex', gap: 8, alignItems: 'center' }}><span style={{ width: 110 }}>{t(`mk.s.${x}`)}</span><b style={{ width: 40 }}>{s[x]}</b>
                <span style={{ flex: 1 }}>{bar(s[x], { sent: '#1d4ed8', delivered: '#0f7b4b', clicked: '#7c3aed', failed: '#b42318', skipped: '#94a3b8', opted_out: '#a15c00', queued: '#cbd5e1' }[x])}</span></div>))}
          </div>)}
        <div className="mx-row" style={{ gap: 8, flexWrap: 'wrap', alignItems: 'end' }}>
          <label className="mx-fld">{t('mk.name')}<input disabled={!editable} value={k.name} onChange={(e) => set({ name: e.currentTarget.value })} /></label>
          <label className="mx-fld">{t('mk.channel')}<select disabled={!editable} value={k.channel} onChange={(e) => set({ channel: e.currentTarget.value })}>{CHANNELS.map((c) => <option key={c} value={c}>{t(`mk.ch.${c}`)}</option>)}</select></label>
          <label className="mx-fld">{t('mk.list')}<select disabled={!editable} value={k.list_id ?? ''} onChange={(e) => set({ list_id: e.currentTarget.value || null })}>
            <option value="">—</option>{lists.filter((l) => l.is_active).map((l) => <option key={l.id} value={l.id}>{l.name} ({l.members})</option>)}</select></label>
        </div>
        <b>{t('mk.audience')}</b>
        <AudienceEditor value={k.audience ?? {}} disabled={!editable} onChange={(a) => set({ audience: a })} />
        {k.channel === 'email' && <label className="mx-fld">{t('mk.subject')}<input disabled={!editable} value={k.subject} onChange={(e) => set({ subject: e.currentTarget.value })} /></label>}
        <label className="mx-fld">{t('mk.body')}<textarea rows={5} disabled={!editable} value={k.body} onChange={(e) => set({ body: e.currentTarget.value })} /></label>
        {editable && <div style={{ display: 'flex', gap: 4, flexWrap: 'wrap' }}>{VARS.map((v) => <button key={v} className="mx-btn sm" dir="ltr" onClick={() => set({ body: `${k.body}${k.body.endsWith(' ') || !k.body ? '' : ' '}${v}` })}>{v}</button>)}
          <small style={{ opacity: 0.8 }}>{t('mk.varsHelp')}</small></div>}
        <div className="mx-row" style={{ gap: 8, flexWrap: 'wrap', alignItems: 'end' }}>
          <label className="mx-fld">{t('mk.link')}<input dir="ltr" disabled={!editable} placeholder="https://" value={k.link_url} onChange={(e) => set({ link_url: e.currentTarget.value })} /></label>
          <label className="mx-fld">{t('mk.coupon')}<select disabled={!editable} value={k.promotion_id ?? ''} onChange={(e) => set({ promotion_id: e.currentTarget.value || null })}>
            <option value="">—</option>{promos.filter((p) => p.coupon_code && p.is_active).map((p) => <option key={p.id} value={p.id}>{p.coupon_code} · {p.name}</option>)}</select></label>
          <label className="mx-fld">{t('mk.schedule')}<input type="datetime-local" dir="ltr" disabled={!editable} value={localDT}
            onChange={(e) => set({ scheduled_at: e.currentTarget.value ? new Date(e.currentTarget.value).toISOString() : null })} /></label>
          <label className="mx-fld">{t('mk.throttle')}<input dir="ltr" style={{ width: 70 }} disabled={!editable} value={k.throttle_per_min}
            onChange={(e) => set({ throttle_per_min: Math.max(1, Math.min(1000, Number(e.currentTarget.value.replace(/[^\d]/g, '')) || 1)) })} /></label>
        </div>
        <Flash m={msg} />
        {editable && <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap', alignItems: 'end' }}>
          <button className="mx-btn" disabled={!edit} onClick={() => void save()}>{t('mk.save')}</button>
          <button className="mx-btn" onClick={() => void run(api<Preview>(`${base}/mkt/audience`, { method: 'POST', body: { channel: k.channel, audience: k.audience ?? {}, list_id: k.list_id ?? '' } }),
            (x) => setPreview(x as Preview))}>{t('mk.preview')}</button>
          <input aria-label={t('mk.testTo')} placeholder={t('mk.testTo')} dir="ltr" value={to} onChange={(e) => setTo(e.currentTarget.value)} style={{ width: 150 }} />
          <button className="mx-btn" disabled={!to.trim() || !!edit} onClick={() => void run(api(`${base}/mkt/campaigns/${id}/test`, { method: 'POST', body: { to } }))}>{t('mk.test')}</button>
          <span className="mx-sp" />
          {can(me, 'marketing.manage') && <button className="mx-btn main" disabled={!!edit} onClick={() => {
            if (window.confirm(k.scheduled_at && new Date(k.scheduled_at) > new Date() ? t('mk.scheduleQ') : t('mk.sendQ'))) void run(api(`${base}/mkt/campaigns/${id}/send`, { method: 'POST', body: {} }));
          }}>{k.scheduled_at && new Date(k.scheduled_at) > new Date() ? t('mk.scheduleBtn') : t('mk.sendNow')}</button>}
        </div>}
        {edit && <small className="warn">{t('mk.unsaved')}</small>}
        {preview && <div className="mx-box" style={{ padding: 8 }}>
          <b>{t('mk.reach', { reach: preview.reach, total: preview.total })}</b>
          {Object.entries(preview.skipped).map(([r, n]) => <div key={r}><small>{t(`mk.skip.${r}`)}: {n}</small></div>)}
          <table className="mx-table"><tbody>{preview.sample.map((x, i) => <tr key={i} style={{ opacity: x.skip ? 0.5 : 1 }}><td>{x.name}</td><td dir="ltr">{x.phone}</td><td dir="ltr">{x.email}</td>
            <td>{x.skip ? t(`mk.skip.${x.skip}`) : ''}</td></tr>)}</tbody></table>
        </div>}
        {(d.campaign.status === 'scheduled' || d.campaign.status === 'sent') && can(me, 'marketing.manage') && s.queued > 0 || d.campaign.status === 'scheduled' ?
          <button className="mx-btn" style={{ alignSelf: 'flex-start', color: 'var(--mx-red)' }} onClick={() => { if (window.confirm(t('mk.cancelQ'))) void run(api(`${base}/mkt/campaigns/${id}/cancel`, { method: 'POST', body: {} })); }}>{t('mk.cancel')}</button> : null}
        {d.recipients.length > 0 && <>
          <b>{t('mk.recipients')}</b>
          <div style={{ overflowX: 'auto', maxHeight: 360 }}>
            <table className="mx-table">
              <thead><tr><th>{t('mk.name')}</th><th>{t('mk.contact')}</th><th>{t('mk.status')}</th><th className="num">{t('mk.s.clicks')}</th><th>{t('mk.s.opted_out')}</th></tr></thead>
              <tbody>{d.recipients.map((r) => (
                <tr key={r.id}><td>{r.name}</td><td dir="ltr">{k.channel === 'email' ? r.email : r.phone}</td>
                  <td>{r.status === 'skipped' ? <span className="red">{t(`mk.skip.${r.skip_reason}`, { defaultValue: r.skip_reason })}</span> : t(`mk.ms.${r.message_status ?? 'queued'}`)}
                    {r.message_error && <small className="red"> {r.message_error}</small>}</td>
                  <td className="num">{r.clicks || ''}</td><td>{r.opted_out_at ? '✓' : ''}</td></tr>))}</tbody>
            </table>
          </div>
        </>}
      </div>
    </section>
  );
}

// ======================= lists =======================

function Lists({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [rows, reload] = useLoad<List[]>(base && `${base}/mkt/lists`, []);
  const [sel, setSel] = useState<string | null>(null);
  const [msg, run] = useRun(reload);
  const [name, setName] = useState('');
  return (
    <div className="mx-row" style={{ gap: 8, alignItems: 'flex-start', flexWrap: 'wrap' }}>
      <section className="mx-box" style={{ flex: '1 1 360px', minWidth: 0 }}>
        <h3>{t('mk.m.lists')}</h3>
        {can(me, 'marketing.manage') && <div className="mx-row" style={{ gap: 6, padding: 6 }}>
          <input aria-label={t('mk.name')} placeholder={t('mk.listName')} value={name} onChange={(e) => setName(e.currentTarget.value)} />
          <button className="mx-btn main" disabled={!name.trim()} onClick={() => void run(api<List>(`${base}/mkt/lists`, { method: 'POST', body: { name } }), (x) => { setSel((x as List).id); setName(''); })}>{t('mk.add')}</button>
        </div>}
        <Flash m={msg} />
        <table className="mx-table">
          <thead><tr><th>{t('mk.name')}</th><th className="num">{t('mk.members')}</th></tr></thead>
          <tbody>{rows.map((l) => <tr key={l.id} className={sel === l.id ? 'sel' : ''} style={{ cursor: 'pointer', opacity: l.is_active ? 1 : 0.5 }} onClick={() => setSel(l.id)}>
            <td><b>{l.name}</b> <small>{l.description}</small></td><td className="num">{l.members}</td></tr>)}
            {rows.length === 0 && <tr><td colSpan={2} className="mx-empty">{t('mk.none')}</td></tr>}</tbody>
        </table>
      </section>
      {sel && <ListPanel me={me} id={sel} onChange={reload} />}
    </div>
  );
}

function ListPanel({ me, id, onChange }: { me: Me; id: string; onChange: () => void }) {
  const { t } = useTranslation();
  const base = useBase();
  const [rows, reload] = useLoad<Member[]>(base && `${base}/mkt/lists/${id}/members`, []);
  const [msg, run] = useRun(() => { reload(); onChange(); });
  const [cust, setCust] = useState({ id: '', text: '' });
  const [paste, setPaste] = useState('');
  const [aud, setAud] = useState<Audience>({});
  const edit = can(me, 'marketing.create');
  const contacts = () => paste.split('\n').map((l) => l.split(/[,;\t]/).map((x) => x.trim())).filter((p) => p.some(Boolean)).map(([name = '', phone = '', email = '']) => {
    const e = [name, phone, email].find((x) => x.includes('@')) ?? '';
    const p = [name, phone, email].find((x) => /\d{6,}/.test(x.replace(/[^\d]/g, ''))) ?? '';
    const n = [name, phone, email].find((x) => x && x !== e && x !== p) ?? '';
    return { name: n, phone: p, email: e };
  }).filter((c) => c.phone || c.email);
  return (
    <section className="mx-box" style={{ flex: '2 1 560px', minWidth: 0 }}>
      <h3>{t('mk.members')} ({rows.length})</h3>
      {edit && <div style={{ padding: 6, display: 'flex', flexDirection: 'column', gap: 6 }}>
        <div className="mx-row" style={{ gap: 6, alignItems: 'end', flexWrap: 'wrap' }}>
          <CustomerPicker value={cust} label={t('mk.addCustomer')} onChange={setCust} />
          <button className="mx-btn" disabled={!cust.id} onClick={() => void run(api(`${base}/mkt/lists/${id}/members`, { method: 'POST', body: { customer_ids: [cust.id] } }), () => setCust({ id: '', text: '' }))}>{t('mk.add')}</button>
        </div>
        <details><summary>{t('mk.addByFilter')}</summary>
          <AudienceEditor value={aud} disabled={false} onChange={setAud} />
          <button className="mx-btn" onClick={() => void run(api(`${base}/mkt/lists/${id}/members`, { method: 'POST', body: { audience: aud } }))}>{t('mk.addMatching')}</button>
        </details>
        <details><summary>{t('mk.paste')}</summary>
          <textarea rows={4} dir="ltr" placeholder={'Rima, 70 123 456, rima@example.com'} value={paste} onChange={(e) => setPaste(e.currentTarget.value)} style={{ width: '100%' }} />
          <button className="mx-btn" disabled={contacts().length === 0} onClick={() => void run(api(`${base}/mkt/lists/${id}/members`, { method: 'POST', body: { contacts: contacts() } }), () => setPaste(''))}>
            {t('mk.addN', { n: contacts().length })}</button>
        </details>
      </div>}
      <Flash m={msg} />
      <div style={{ overflowX: 'auto', maxHeight: 460 }}>
        <table className="mx-table">
          <thead><tr><th>{t('mk.name')}</th><th>{t('mk.phone')}</th><th>{t('mk.email')}</th><th /></tr></thead>
          <tbody>{rows.map((m) => <tr key={m.id}><td>{m.customer_code && <small dir="ltr">{m.customer_code} </small>}{m.name}</td><td dir="ltr">{m.phone}</td><td dir="ltr">{m.email}</td>
            <td>{edit && <button className="mx-btn sm" onClick={() => void run(api(`${base}/mkt/list-members/${m.id}`, { method: 'DELETE' }))}>{t('mk.remove')}</button>}</td></tr>)}
            {rows.length === 0 && <tr><td colSpan={4} className="mx-empty">{t('mk.none')}</td></tr>}</tbody>
        </table>
      </div>
    </section>
  );
}

// ======================= opt-outs =======================

function Optouts({ me }: { me: Me }) {
  const { t } = useTranslation();
  const base = useBase();
  const [rows, reload] = useLoad<Optout[]>(base && `${base}/mkt/optouts`, []);
  const [msg, run] = useRun(reload);
  const [nf, setNf] = useState({ phone: '', email: '', channel: 'all', reason: '' });
  return (
    <section className="mx-box">
      <h3>{t('mk.m.optouts')}</h3>
      <small style={{ padding: 6, display: 'block' }}>{t('mk.optoutHelp')}</small>
      {can(me, 'marketing.create') && <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap', alignItems: 'end' }}>
        <label className="mx-fld">{t('mk.phone')}<input dir="ltr" value={nf.phone} onChange={(e) => setNf({ ...nf, phone: e.currentTarget.value })} /></label>
        <label className="mx-fld">{t('mk.email')}<input dir="ltr" value={nf.email} onChange={(e) => setNf({ ...nf, email: e.currentTarget.value })} /></label>
        <label className="mx-fld">{t('mk.channel')}<select value={nf.channel} onChange={(e) => setNf({ ...nf, channel: e.currentTarget.value })}>
          <option value="all">{t('mk.ch.all')}</option>{CHANNELS.map((c) => <option key={c} value={c}>{t(`mk.ch.${c}`)}</option>)}</select></label>
        <label className="mx-fld">{t('mk.reason')}<input value={nf.reason} onChange={(e) => setNf({ ...nf, reason: e.currentTarget.value })} /></label>
        <button className="mx-btn main" disabled={!nf.phone.trim() && !nf.email.trim()} onClick={() => void run(api(`${base}/mkt/optouts`, { method: 'POST', body: nf }),
          () => setNf({ phone: '', email: '', channel: 'all', reason: '' }))}>{t('mk.add')}</button>
      </div>}
      <Flash m={msg} />
      <table className="mx-table">
        <thead><tr><th>{t('mk.when')}</th><th>{t('mk.contact')}</th><th>{t('mk.channel')}</th><th>{t('mk.reason')}</th><th>{t('mk.source')}</th><th /></tr></thead>
        <tbody>{rows.map((o) => <tr key={o.id}><td>{formatDateTime(o.created_at)}</td><td>{o.customer_name ?? ''} <span dir="ltr">{o.phone} {o.email}</span></td>
          <td>{t(`mk.ch.${o.channel}`)}</td><td>{o.reason}</td><td>{t(`mk.src.${o.source}`)}</td>
          <td>{can(me, 'marketing.manage') && <button className="mx-btn sm" onClick={() => { if (window.confirm(t('mk.resubQ'))) void run(api(`${base}/mkt/optouts/${o.id}`, { method: 'DELETE' })); }}>{t('mk.remove')}</button>}</td></tr>)}
          {rows.length === 0 && <tr><td colSpan={6} className="mx-empty">{t('mk.none')}</td></tr>}</tbody>
      </table>
    </section>
  );
}
