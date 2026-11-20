import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, can, type Me } from '../../api';
import { useCompany } from '../../company';
import { errorText, formatDate } from '../../util';
import { ChecksTable, moCode, type QCheck, type WO } from './Mfg';

// The shop-floor screen (tablet / phone, full screen): the operator picks a work center, sees its work orders in
// order (running, paused, ready, waiting), and for one work order: the instructions, a big timer with start / pause /
// finish, the components, the quality checks to record, and scrap. Refreshes itself every 20 seconds.

interface WCLite { id: string; code: string; name: string; open_workorders: number; running: number; is_active: boolean }
interface WODetail { workorder: WO; checks: QCheck[]; components: { id: string; item_id: string; item_code: string; item_name: string; unit: string; planned_qty: string; reserved_qty: string }[]; times: { id: string; user_name: string | null; started_at: string; ended_at: string | null; minutes: string }[] }

const q = (v: string | null | undefined) => (v == null || v === '' ? '' : String(Number(v)));
const KEY = 'paxve-shopfloor-wc';

function clock(min: number): string {
  const s = Math.max(0, Math.floor(min * 60));
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), sec = s % 60;
  return `${h > 0 ? `${h}:` : ''}${String(m).padStart(h > 0 ? 2 : 1, '0')}:${String(sec).padStart(2, '0')}`;
}

export default function ShopFloor({ me, onExit }: { me: Me; onExit: () => void }) {
  const { t, i18n } = useTranslation();
  const { company } = useCompany();
  const base = company ? `/companies/${company.id}` : null;
  const [wcs, setWcs] = useState<WCLite[]>([]);
  const [wc, setWc] = useState<string>(() => { try { return localStorage.getItem(KEY) ?? ''; } catch { return ''; } });
  const [list, setList] = useState<WO[]>([]);
  const [sel, setSel] = useState<WODetail | null>(null);
  const [loadedAt, setLoadedAt] = useState(Date.now());
  const [now, setNow] = useState(Date.now());
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [mins, setMins] = useState('');
  const [scrap, setScrap] = useState({ item_id: '', quantity: '', reason: '' });
  const dir = i18n.dir(i18n.language);
  const doer = can(me, 'stock.create');

  const loadList = useCallback(() => {
    if (!base) return;
    api<WCLite[]>(`${base}/mfg/workcenters`).then((l) => setWcs(l.filter((x) => x.is_active))).catch(() => {});
    if (wc) api<WO[]>(`${base}/mfg/workorders?workcenter_id=${wc}`).then((l) => { setList(l); setLoadedAt(Date.now()); }).catch(() => setList([]));
  }, [base, wc]);
  const loadSel = useCallback((id: string) => {
    if (!base) return;
    api<WODetail>(`${base}/mfg/workorders/${id}`).then((d) => { setSel(d); setLoadedAt(Date.now()); }).catch(() => setSel(null));
  }, [base]);
  useEffect(() => { loadList(); }, [loadList]);
  useEffect(() => { try { localStorage.setItem(KEY, wc); } catch { /* storage unavailable */ } }, [wc]);
  useEffect(() => {
    const t1 = window.setInterval(() => setNow(Date.now()), 1000);
    const t2 = window.setInterval(() => { loadList(); if (sel) loadSel(sel.workorder.id); }, 20000);
    return () => { window.clearInterval(t1); window.clearInterval(t2); };
  }, [loadList, loadSel, sel]);

  const act = async (id: string, action: string, body: unknown = {}) => {
    if (!base) return;
    try {
      const r = await api<WODetail & { all_done: boolean }>(`${base}/mfg/workorders/${id}/${action}`, { method: 'POST', body });
      setSel(r); setLoadedAt(Date.now());
      setMsg({ ok: true, text: action === 'finish' && r.all_done ? t('mf.sf.allDone') : t(`mf.sf.did.${action}`) });
      setMins('');
      loadList();
    } catch (e) { setMsg({ ok: false, text: errorText(t, e) }); }
  };
  const doScrap = async () => {
    if (!base || !sel) return;
    try {
      await api(`${base}/mfg/orders/${sel.workorder.order_id}/scrap`, { method: 'POST', body: scrap });
      setMsg({ ok: true, text: t('mf.sf.scrapped') }); setScrap({ item_id: '', quantity: '', reason: '' });
    } catch (e) { setMsg({ ok: false, text: errorText(t, e) }); }
  };

  const big = { minHeight: 56, fontSize: 18, flex: '1 1 120px' } as const;
  const card = { background: 'var(--mx-card)', border: '1px solid var(--mx-line)', borderRadius: 10, padding: 10 } as const;
  const w = sel?.workorder;
  const elapsed = w ? Number(w.worked_min || 0) + (w.running_since ? (now - loadedAt) / 60000 : 0) : 0;
  const wcName = wcs.find((x) => x.id === wc)?.name ?? '';

  return (
    <div dir={dir} style={{ minHeight: '100vh', background: 'var(--mx-panel)', display: 'flex', flexDirection: 'column', fontSize: 16 }}>
      <header style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '8px 10px', background: '#4e342e', color: '#fff', paddingTop: 'calc(8px + env(safe-area-inset-top, 0px))' }}>
        <b style={{ flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{t('mf.sf.title')}{wcName ? ` · ${wcName}` : ''}</b>
        {sel && <button className="mx-btn sm" onClick={() => { setSel(null); setMsg(null); }}>{t('mf.sf.back')}</button>}
        <button className="mx-btn sm" onClick={onExit}>{t('mf.close')}</button>
      </header>
      <main style={{ flex: 1, padding: 8, display: 'flex', flexDirection: 'column', gap: 8, maxWidth: 900, width: '100%', boxSizing: 'border-box', marginInline: 'auto' }}>
        {msg && <div className={`mx-flash ${msg.ok ? 'good' : 'bad'}`}>{msg.text}</div>}
        {!sel && (
          <>
            <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
              {wcs.map((x) => (
                <button key={x.id} className={`mx-btn ${wc === x.id ? 'main' : ''}`} style={{ minHeight: 52, flex: '1 1 140px' }} onClick={() => { setWc(x.id); setMsg(null); }}>
                  {x.name} <small>({x.open_workorders}{x.running ? ` · ${x.running} ▶` : ''})</small></button>))}
              {wcs.length === 0 && <div style={card}>{t('mf.sf.noWc')}</div>}
            </div>
            {wc && list.map((x) => (
              <button key={x.id} onClick={() => { loadSel(x.id); setMsg(null); }} style={{ ...card, textAlign: 'start', cursor: 'pointer', display: 'flex', flexDirection: 'column', gap: 2,
                borderInlineStart: `6px solid ${x.status === 'progress' ? '#a15c00' : x.status === 'ready' ? '#0f7b4b' : x.status === 'paused' ? '#b45309' : '#9ca3af'}` }}>
                <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
                  <b dir="ltr">{moCode(x.order_number)}</b><span>{x.name}</span><span className="mx-sp" />
                  <span className="mx-pill">{t(`mf.st.${x.status}`)}</span>
                </div>
                <div>{x.item_code} · {x.item_name} × <b dir="ltr">{q(x.quantity)} {x.unit}</b></div>
                <small>{t('mf.plannedMin')}: {q(x.planned_min)} · {t('mf.sf.worked')}: {clock(Number(x.worked_min || 0))}{x.deadline ? ` · ${t('mf.deadline')} ${formatDate(x.deadline)}` : ''}
                  {x.checks_todo > 0 ? ` · ${t('mf.q.todoN', { n: x.checks_todo })}` : ''}</small>
              </button>))}
            {wc && list.length === 0 && <div style={card}>{t('mf.sf.nothing')}</div>}
          </>
        )}
        {sel && w && (
          <>
            <div style={card}>
              <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
                <b dir="ltr" style={{ fontSize: 20 }}>{moCode(w.order_number)}</b><b style={{ fontSize: 18 }}>{w.seq}. {w.name}</b><span className="mx-sp" /><span className="mx-pill">{t(`mf.st.${w.status}`)}</span>
              </div>
              <div style={{ fontSize: 18 }}>{w.item_code} · {w.item_name} × <b dir="ltr">{q(w.quantity)} {w.unit}</b></div>
              {w.instructions && <div style={{ marginTop: 6, padding: 8, background: 'var(--mx-amber-soft)', borderRadius: 6, whiteSpace: 'pre-wrap' }}>{w.instructions}</div>}
            </div>
            <div style={{ ...card, textAlign: 'center' }}>
              <div dir="ltr" aria-live="polite" style={{ fontSize: 56, fontWeight: 700, fontVariantNumeric: 'tabular-nums', color: w.status === 'progress' ? '#a15c00' : '#1f2937' }}>{clock(elapsed)}</div>
              <small>{t('mf.plannedMin')}: {q(w.planned_min)} {t('mf.min')}{w.operator ? ` · ${w.operator}` : ''}</small>
              {doer && !['done', 'cancelled'].includes(w.status) && (
                <div style={{ display: 'flex', gap: 8, marginTop: 10, flexWrap: 'wrap' }}>
                  {w.status !== 'progress' && <button className="mx-btn main" style={big} onClick={() => void act(w.id, 'start')}>▶ {t('mf.wo.start')}</button>}
                  {w.status === 'progress' && <button className="mx-btn" style={big} onClick={() => void act(w.id, 'pause')}>⏸ {t('mf.wo.pause')}</button>}
                  <button className="mx-btn" style={{ ...big, background: '#0f7b4b', color: '#fff', borderColor: '#0f7b4b' }} onClick={() => void act(w.id, 'finish', mins ? { minutes: mins } : {})}>✓ {t('mf.wo.finish')}</button>
                </div>
              )}
              {doer && !['done', 'cancelled'].includes(w.status) && (
                <label style={{ display: 'flex', gap: 6, alignItems: 'center', justifyContent: 'center', marginTop: 8 }}>{t('mf.sf.typeMinutes')}
                  <input dir="ltr" inputMode="decimal" style={{ width: 80, minHeight: 40 }} value={mins} onChange={(e) => setMins(e.currentTarget.value.replace(/[^\d.]/g, ''))} /></label>
              )}
            </div>
            {sel.checks.length > 0 && <div style={card}><b>{t('mf.q.tab.checks')}</b><ChecksTable checks={sel.checks} me={me} big onDone={() => loadSel(w.id)} /></div>}
            <div style={card}>
              <b>{t('mf.components')}</b>
              <table className="mx-table"><tbody>{sel.components.map((c) => (
                <tr key={c.id}><td>{c.item_code} · {c.item_name}</td><td className="num" dir="ltr">{q(c.planned_qty)} {c.unit}</td>
                  <td className={`num ${Number(c.reserved_qty) < Number(c.planned_qty) ? 'red' : ''}`} dir="ltr">{q(c.reserved_qty)}</td></tr>))}</tbody></table>
            </div>
            {can(me, 'stock.manage') && w.status !== 'cancelled' && (
              <div style={card}>
                <b>{t('mf.sf.scrap')}</b>
                <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap', alignItems: 'center', marginTop: 6 }}>
                  <select aria-label={t('mf.item')} style={{ minHeight: 44 }} value={scrap.item_id} onChange={(e) => setScrap({ ...scrap, item_id: e.currentTarget.value })}>
                    <option value="">{t('mf.choose')}</option>
                    {sel.components.map((c) => <option key={c.id} value={c.item_id}>{c.item_code} · {c.item_name}</option>)}
                  </select>
                  <input dir="ltr" inputMode="decimal" aria-label={t('mf.qty')} placeholder={t('mf.qty')} style={{ width: 70, minHeight: 44 }} value={scrap.quantity} onChange={(e) => setScrap({ ...scrap, quantity: e.currentTarget.value.replace(/[^\d.]/g, '') })} />
                  <input aria-label={t('mf.reason')} placeholder={t('mf.reason')} style={{ flex: '1 1 120px', minHeight: 44 }} value={scrap.reason} onChange={(e) => setScrap({ ...scrap, reason: e.currentTarget.value })} />
                  <button className="mx-btn" style={{ minHeight: 44 }} disabled={!scrap.item_id || !Number(scrap.quantity) || !scrap.reason.trim()} onClick={() => void doScrap()}>{t('mf.scrapIt')}</button>
                </div>
              </div>
            )}
            {sel.times.length > 0 && <div style={card}><small>{sel.times.map((x) => `${x.user_name ?? ''} ${new Date(x.started_at).toLocaleTimeString(i18n.language, { hour: '2-digit', minute: '2-digit' })}${x.ended_at ? ` → ${q(x.minutes)} ${t('mf.min')}` : ' ▶'}`).join(' · ')}</small></div>}
          </>
        )}
      </main>
    </div>
  );
}
