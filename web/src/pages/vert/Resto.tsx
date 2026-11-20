import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, can, type Me } from '../../api';
import { useCompany } from '../../company';
import { errorText, formatAmount } from '../../util';
import { newClientRef } from '../../outbox';
import { itemName, type Catalog, type CatItem, type Register } from '../pos/common';

// Restaurant (full screen, tablet first): the floor plan with the open checks, a check (menu by category, modifiers,
// lines with their kitchen status, send to the kitchen, split, move, pay with service charge and tip) and the kitchen
// display (KDS) per station. Both refresh themselves.

interface CheckRow {
  id: string; number: number; register_id: string; currency: string; table_id: string | null; table_name: string | null; kind: string; status: string;
  guests: number; customer_name: string | null; name: string; phone: string; address: string; waiter: string | null; opened_at: string;
  total: string; unpaid: string; ready: number; unsent: number;
}
interface TableRow { id: string; name: string; area: string; seats: number; x: number; y: number; shape: string; open_check: CheckRow | null }
interface Line {
  id: string; item_id: string; item_name: string; item_name_i18n: Record<string, string> | null; quantity: string; unit_price: string; amount: string;
  modifiers: { id: string; name: string; price: string }[]; note: string; station: string; kstatus: string; pos_order_id: string | null;
}
interface CheckDetail { check: CheckRow; lines: Line[]; pos_order?: { order: { number: number; total: string } } }
interface Modifier { id: string; name: string; name_i18n: Record<string, string> | null; grp: string; price: string }

const mins = (iso: string) => Math.max(0, Math.floor((Date.now() - new Date(iso).getTime()) / 60000));
const kPill: Record<string, string> = { new: '', sent: 'warn', cooking: 'warn', ready: 'on', served: '', void: 'off' };
const big = { minHeight: 48, fontSize: 16 } as const;

function useBase() {
  const { company } = useCompany();
  return company ? `/companies/${company.id}` : null;
}

export default function Resto({ me, onExit }: { me: Me; onExit: () => void }) {
  const { t, i18n } = useTranslation();
  const base = useBase();
  const { decimalsOf } = useCompany();
  const [regs, setRegs] = useState<Register[]>([]);
  const [reg, setReg] = useState<string>(() => { try { return localStorage.getItem('paxve-resto-reg') ?? ''; } catch { return ''; } });
  const [floor, setFloor] = useState<{ tables: TableRow[]; others: CheckRow[] } | null>(null);
  const [cat, setCat] = useState<Catalog | null>(null);
  const [check, setCheck] = useState<CheckDetail | null>(null);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const dir = i18n.dir(i18n.language);
  const register = regs.find((r) => r.id === reg);
  const session = register?.open_session?.id ?? null;
  const dec = decimalsOf(register?.currency ?? 'USD');

  const loadRegs = useCallback(() => {
    if (!base) return;
    api<Register[]>(`${base}/pos/registers`).then((l) => {
      const act = l.filter((r) => r.is_active);
      setRegs(act);
      if (!act.some((r) => r.id === reg) && act[0]) setReg(act[0].id);
    }).catch(() => {});
  }, [base, reg]);
  const loadFloor = useCallback(() => {
    if (base && reg) api<{ tables: TableRow[]; others: CheckRow[] }>(`${base}/rest/floor?register_id=${reg}`).then(setFloor).catch(() => {});
  }, [base, reg]);
  const loadCheck = useCallback((id: string) => {
    if (base) api<CheckDetail>(`${base}/rest/checks/${id}`).then(setCheck).catch((e) => setMsg({ ok: false, text: errorText(t, e) }));
  }, [base, t]);
  useEffect(() => { loadRegs(); }, [loadRegs]);
  useEffect(() => { try { localStorage.setItem('paxve-resto-reg', reg); } catch { /* */ } }, [reg]);
  useEffect(() => {
    loadFloor();
    if (base && reg) api<Catalog>(`${base}/pos/catalog?register_id=${reg}`).then(setCat).catch(() => setCat(null));
  }, [base, reg, loadFloor]);
  useEffect(() => {
    const h = window.setInterval(() => { loadFloor(); if (check) loadCheck(check.check.id); }, 15000);
    return () => window.clearInterval(h);
  }, [loadFloor, loadCheck, check]);

  const call = async (p: Promise<unknown>, done?: (x: unknown) => void) => {
    try { const x = await p; setMsg(null); done?.(x); } catch (e) { setMsg({ ok: false, text: errorText(t, e) }); }
  };
  const openCheck = (body: Record<string, unknown>) => call(api<CheckDetail>(`${base}/rest/checks`, { method: 'POST', body: { register_id: reg, ...body } }),
    (x) => { setCheck(x as CheckDetail); loadFloor(); });
  const openSession = () => call(api(`${base}/pos/sessions`, { method: 'POST', body: { register_id: reg } }), () => loadRegs());

  return (
    <div dir={dir} style={{ minHeight: '100vh', background: '#f3f5f8', display: 'flex', flexDirection: 'column', fontSize: 15 }}>
      <header style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '8px 10px', background: '#7c2d12', color: '#fff', paddingTop: 'calc(8px + env(safe-area-inset-top, 0px))', flexWrap: 'wrap' }}>
        <b style={{ flex: 1, minWidth: 0 }}>{t('vt.t.resto')}{check ? ` · #${check.check.number}${check.check.table_name ? ' · ' + check.check.table_name : ''}` : ''}</b>
        {regs.length > 1 && <select value={reg} onChange={(e) => { setReg(e.currentTarget.value); setCheck(null); }} aria-label={t('vt.r.register')}>
          {regs.map((r) => <option key={r.id} value={r.id}>{r.name}</option>)}</select>}
        {check && <button className="mx-btn sm" onClick={() => { setCheck(null); loadFloor(); }}>{t('vt.r.floor')}</button>}
        <button className="mx-btn sm" onClick={() => { window.location.hash = '#/kds'; }}>{t('vt.t.kds')}</button>
        <button className="mx-btn sm" onClick={onExit}>{t('vt.close')}</button>
      </header>
      <main style={{ flex: 1, padding: 8, display: 'flex', flexDirection: 'column', gap: 8, width: '100%', boxSizing: 'border-box' }}>
        {msg && <div className={`mx-flash ${msg.ok ? 'good' : 'bad'}`}>{msg.text}</div>}
        {register && !session && (
          <div className="mx-box" style={{ padding: 10, display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
            <span>{t('vt.r.noSession')}</span>
            {can(me, 'pos.create') && <button className="mx-btn main" onClick={() => void openSession()}>{t('vt.r.openSession')}</button>}
          </div>
        )}
        {!check && floor && (
          <>
            <div className="mx-row" style={{ gap: 8, flexWrap: 'wrap' }}>
              <button className="mx-btn main" style={big} onClick={() => void openCheck({ kind: 'takeaway' })}>{t('vt.r.takeaway')}</button>
              <button className="mx-btn" style={big} onClick={() => void openCheck({ kind: 'delivery' })}>{t('vt.r.delivery')}</button>
              {floor.others.map((k) => (
                <button key={k.id} className="mx-btn" style={big} onClick={() => loadCheck(k.id)}>
                  #{k.number} {t(`vt.r.k.${k.kind}`)} {k.name} · <span dir="ltr">{formatAmount(k.total, dec)}</span>{k.ready > 0 && ` · ✓${k.ready}`}</button>))}
            </div>
            <div style={{ position: 'relative', minHeight: 420, height: '62vh', background: '#fff', border: '1px solid #dde3ea', borderRadius: 8, overflow: 'auto' }}>
              {floor.tables.map((tb) => {
                const k = tb.open_check;
                const bg = !k ? '#ffffff' : k.ready > 0 ? '#dcfce7' : '#ffedd5';
                return (
                  <button key={tb.id} onClick={() => (k ? loadCheck(k.id) : void openCheck({ table_id: tb.id, guests: Math.min(tb.seats, 2) }))}
                    style={{ position: 'absolute', insetInlineStart: `${Math.min(tb.x, 86)}%`, top: `${Math.min(tb.y, 84)}%`, width: tb.shape === 'long' ? 130 : 96, height: 76,
                      borderRadius: tb.shape === 'round' ? 38 : 10, background: bg, border: `2px solid ${k ? '#c2410c' : '#cbd5e1'}`, cursor: 'pointer', display: 'flex',
                      flexDirection: 'column', alignItems: 'center', justifyContent: 'center', gap: 2, padding: 2 }}>
                    <b style={{ fontSize: 18 }}>{tb.name}</b>
                    {k ? <><small dir="ltr">{formatAmount(k.total, dec)}</small><small>{t('vt.r.minAgo', { n: mins(k.opened_at) })}{k.ready > 0 ? ` · ✓${k.ready}` : ''}</small></>
                      : <small>{t('vt.r.seatsN', { n: tb.seats })}</small>}
                  </button>);
              })}
              {floor.tables.length === 0 && <div className="mx-empty">{t('vt.r.noTables')}</div>}
            </div>
          </>
        )}
        {check && cat && <CheckView me={me} base={base!} cat={cat} d={check} session={session} dec={dec} tables={floor?.tables ?? []}
          onChange={(x) => { setCheck(x); loadFloor(); }} onClosed={(text) => { setCheck(null); setMsg({ ok: true, text }); loadFloor(); }} onError={(e) => setMsg({ ok: false, text: errorText(t, e) })} />}
      </main>
    </div>
  );
}

function CheckView({ me, base, cat, d, session, dec, tables, onChange, onClosed, onError }: {
  me: Me; base: string; cat: Catalog; d: CheckDetail; session: string | null; dec: number; tables: TableRow[];
  onChange: (d: CheckDetail) => void; onClosed: (text: string) => void; onError: (e: unknown) => void;
}) {
  const { t, i18n } = useTranslation();
  const [catId, setCatId] = useState<string | null>(null);
  const [q, setQ] = useState('');
  const [pick, setPick] = useState<{ item: CatItem; mods: Modifier[]; chosen: string[]; note: string; qty: number } | null>(null);
  const [sel, setSel] = useState<string[]>([]);
  const [pay, setPay] = useState(false);
  const lines = d.lines.filter((l) => l.kstatus !== 'void');
  const unpaid = lines.filter((l) => !l.pos_order_id);
  const items = useMemo(() => cat.items.filter((i) => !i.template_id && (!catId || i.category_id === catId)
    && (!q || itemName(i, i18n.language).toLowerCase().includes(q.toLowerCase()) || i.code.toLowerCase().includes(q.toLowerCase()))), [cat.items, catId, q, i18n.language]);
  const run = async (p: Promise<CheckDetail>) => { try { onChange(await p); } catch (e) { onError(e); } };
  const tap = async (item: CatItem) => {
    try {
      const mods = await api<Modifier[]>(`${base}/rest/modifiers?item_id=${item.id}`);
      if (mods.length === 0) { await run(api<CheckDetail>(`${base}/rest/checks/${d.check.id}/lines`, { method: 'POST', body: { lines: [{ item_id: item.id, quantity: '1' }] } })); return; }
      setPick({ item, mods, chosen: [], note: '', qty: 1 });
    } catch (e) { onError(e); }
  };
  const addPicked = () => {
    if (!pick) return;
    void run(api<CheckDetail>(`${base}/rest/checks/${d.check.id}/lines`, { method: 'POST',
      body: { lines: [{ item_id: pick.item.id, quantity: String(pick.qty), modifier_ids: pick.chosen, note: pick.note }] } }));
    setPick(null);
  };
  const groups = pick ? [...new Set(pick.mods.map((m) => m.grp))] : [];
  return (
    <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'flex-start' }}>
      <section className="mx-box" style={{ flex: '2 1 420px', minWidth: 0 }}>
        <div className="mx-row" style={{ gap: 6, padding: 6, flexWrap: 'wrap' }}>
          <button className={`mx-btn sm ${!catId ? 'main' : ''}`} onClick={() => setCatId(null)}>{t('vt.r.allMenu')}</button>
          {cat.categories.map((c) => <button key={c.id} className={`mx-btn sm ${catId === c.id ? 'main' : ''}`} onClick={() => setCatId(c.id)}>{c.name_i18n?.[i18n.language] || c.name}</button>)}
          <input placeholder={t('vt.r.searchMenu')} value={q} onChange={(e) => setQ(e.currentTarget.value)} style={{ flex: 1, minWidth: 120 }} />
        </div>
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(130px, 1fr))', gap: 6, padding: 6 }}>
          {items.map((i) => (
            <button key={i.id} className="mx-btn" style={{ minHeight: 70, display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', whiteSpace: 'normal' }}
              onClick={() => void tap(i)}><b>{itemName(i, i18n.language)}</b><small dir="ltr">{formatAmount(i.price, dec)}</small></button>))}
        </div>
      </section>
      <section className="mx-box" style={{ flex: '1 1 320px', minWidth: 0 }}>
        <h3 className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>#{d.check.number} {d.check.table_name ?? t(`vt.r.k.${d.check.kind}`)}
          <span className="mx-sp" /><b dir="ltr" style={{ fontSize: 20 }}>{formatAmount(d.check.unpaid, dec)}</b></h3>
        {d.check.kind !== 'dine_in' && (
          <div className="mx-row" style={{ gap: 4, padding: 4, flexWrap: 'wrap' }}>
            <input placeholder={t('vt.f.name')} defaultValue={d.check.name} onBlur={(e) => e.currentTarget.value !== d.check.name && void run(api(`${base}/rest/checks/${d.check.id}`, { method: 'PATCH', body: { name: e.currentTarget.value } }))} />
            <input placeholder={t('vt.r.phone')} dir="ltr" defaultValue={d.check.phone} onBlur={(e) => e.currentTarget.value !== d.check.phone && void run(api(`${base}/rest/checks/${d.check.id}`, { method: 'PATCH', body: { phone: e.currentTarget.value } }))} />
            {d.check.kind === 'delivery' && <input placeholder={t('vt.r.address')} defaultValue={d.check.address} style={{ flex: 1 }}
              onBlur={(e) => e.currentTarget.value !== d.check.address && void run(api(`${base}/rest/checks/${d.check.id}`, { method: 'PATCH', body: { address: e.currentTarget.value } }))} />}
          </div>
        )}
        <table className="mx-table">
          <tbody>{lines.map((l) => (
            <tr key={l.id} style={{ opacity: l.pos_order_id ? 0.5 : 1 }}>
              <td>{!l.pos_order_id && <input type="checkbox" checked={sel.includes(l.id)} aria-label={l.item_name}
                onChange={(e) => setSel(e.currentTarget.checked ? [...sel, l.id] : sel.filter((x) => x !== l.id))} />}</td>
              <td dir="ltr">{Number(l.quantity)}×</td>
              <td style={{ whiteSpace: 'normal' }}><b>{l.item_name_i18n?.[i18n.language] || l.item_name}</b>
                {(l.modifiers.length > 0 || l.note) && <><br /><small>{[...l.modifiers.map((m) => m.name), l.note].filter(Boolean).join(', ')}</small></>}</td>
              <td dir="ltr">{formatAmount(l.amount, dec)}</td>
              <td><span className={`mx-pill ${kPill[l.kstatus] ?? ''}`}>{l.pos_order_id ? t('vt.r.paidLine') : t(`vt.k.${l.kstatus}`)}</span></td>
              <td>{!l.pos_order_id && (l.kstatus === 'new' || can(me, 'pos.void')) &&
                <button className="mx-btn sm" aria-label={t('vt.r.void')} onClick={() => void run(api(`${base}/rest/check-lines/${l.id}/void`, { method: 'POST', body: {} }))}>✕</button>}
                {l.kstatus === 'ready' && <button className="mx-btn sm" onClick={() => void run(api(`${base}/rest/check-lines/${l.id}/status`, { method: 'POST', body: { status: 'served' } }))}>{t('vt.k.serve')}</button>}</td>
            </tr>))}
            {lines.length === 0 && <tr><td className="mx-empty">{t('vt.r.empty')}</td></tr>}</tbody>
        </table>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6, padding: 6 }}>
          <button className="mx-btn main" style={big} disabled={!d.check.unsent} onClick={() => void run(api(`${base}/rest/checks/${d.check.id}/send`, { method: 'POST', body: {} }))}>
            {t('vt.r.send', { n: d.check.unsent })}</button>
          <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
            <button className="mx-btn" style={{ ...big, flex: 1 }} disabled={!session || unpaid.length === 0} onClick={() => setPay(true)}>{t('vt.r.pay')}</button>
            <button className="mx-btn" style={{ ...big, flex: 1 }} disabled={sel.length === 0 || sel.length >= unpaid.length} onClick={() => void (async () => {
              try { const x = await api<CheckDetail>(`${base}/rest/checks/${d.check.id}/split`, { method: 'POST', body: { line_ids: sel } }); setSel([]); onChange(x); } catch (e) { onError(e); }
            })()}>{t('vt.r.split', { n: sel.length })}</button>
          </div>
          <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>
            {d.check.kind === 'dine_in' && <select value="" onChange={(e) => e.currentTarget.value && void run(api(`${base}/rest/checks/${d.check.id}`, { method: 'PATCH', body: { table_id: e.currentTarget.value } }))}
              aria-label={t('vt.r.move')}><option value="">{t('vt.r.move')}</option>{tables.filter((x) => !x.open_check).map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}</select>}
            <label className="mx-row" style={{ gap: 4 }}>{t('vt.r.guests')}<input dir="ltr" style={{ width: 50 }} defaultValue={d.check.guests}
              onBlur={(e) => Number(e.currentTarget.value) !== d.check.guests && Number(e.currentTarget.value) > 0 && void run(api(`${base}/rest/checks/${d.check.id}`, { method: 'PATCH', body: { guests: Number(e.currentTarget.value) } }))} /></label>
            <span className="mx-sp" />
            {unpaid.length === lines.length && <button className="mx-btn sm" onClick={() => void (async () => {
              try { await api(`${base}/rest/checks/${d.check.id}/cancel`, { method: 'POST', body: {} }); onClosed(t('vt.r.cancelled')); } catch (e) { onError(e); }
            })()}>{t('vt.r.cancel')}</button>}
          </div>
          {!session && <small>{t('vt.r.noSession')}</small>}
        </div>
      </section>
      {pick && (
        <div role="dialog" aria-modal="true" style={{ position: 'fixed', inset: 0, background: 'rgba(15,27,45,.45)', display: 'grid', placeItems: 'center', zIndex: 300 }} onClick={() => setPick(null)}>
          <div className="mx-box" style={{ background: '#fff', padding: 12, width: 'min(520px, 94vw)', maxHeight: '90vh', overflow: 'auto' }} onClick={(e) => e.stopPropagation()}>
            <h3>{itemName(pick.item, i18n.language)}</h3>
            {groups.map((g) => (
              <div key={g} style={{ padding: 4 }}><small>{g}</small>
                <div className="mx-row" style={{ gap: 6, flexWrap: 'wrap' }}>{pick.mods.filter((m) => m.grp === g).map((m) => {
                  const on = pick.chosen.includes(m.id);
                  return <button key={m.id} className={`mx-btn ${on ? 'main' : ''}`} style={{ minHeight: 44 }}
                    onClick={() => setPick({ ...pick, chosen: on ? pick.chosen.filter((x) => x !== m.id) : [...pick.chosen, m.id] })}>
                    {m.name_i18n?.[i18n.language] || m.name}{Number(m.price) ? ` +${formatAmount(m.price, 2)}` : ''}</button>;
                })}</div>
              </div>))}
            <input placeholder={t('vt.r.note')} value={pick.note} onChange={(e) => setPick({ ...pick, note: e.currentTarget.value })} style={{ width: '100%', marginTop: 6 }} />
            <div className="mx-row" style={{ gap: 6, marginTop: 8 }}>
              <button className="mx-btn" style={big} onClick={() => setPick({ ...pick, qty: Math.max(1, pick.qty - 1) })}>−</button>
              <b style={{ fontSize: 20 }} dir="ltr">{pick.qty}</b>
              <button className="mx-btn" style={big} onClick={() => setPick({ ...pick, qty: pick.qty + 1 })}>+</button>
              <span className="mx-sp" />
              <button className="mx-btn" onClick={() => setPick(null)}>{t('common.cancel')}</button>
              <button className="mx-btn main" style={big} onClick={addPicked}>{t('vt.r.add')}</button>
            </div>
          </div>
        </div>
      )}
      {pay && session && <PayPanel base={base} cat={cat} d={d} session={session} dec={dec} lineIds={sel.length ? sel : []}
        onClose={() => setPay(false)} onPaid={(x) => { setPay(false); setSel([]); if (x.check.status === 'paid') onClosed(t('vt.r.paidDone', { n: x.pos_order?.order.number ?? '' })); else onChange(x); }} onError={onError} />}
    </div>
  );
}

function PayPanel({ base, cat, d, session, dec, lineIds, onClose, onPaid, onError }: {
  base: string; cat: Catalog; d: CheckDetail; session: string; dec: number; lineIds: string[]; onClose: () => void; onPaid: (x: CheckDetail) => void; onError: (e: unknown) => void;
}) {
  const { t } = useTranslation();
  const [sc, setSc] = useState(false);
  const [pct, setPct] = useState(0);
  const [tip, setTip] = useState('');
  const [amounts, setAmounts] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [ref] = useState(newClientRef);
  useEffect(() => { api<{ service_charge_pct: string }>(`${base}/rest/settings`).then((s) => { setPct(Number(s.service_charge_pct)); setSc(d.check.kind === 'dine_in' && Number(s.service_charge_pct) > 0); }).catch(() => {}); }, [base, d.check.kind]);
  const lines = d.lines.filter((l) => l.kstatus !== 'void' && !l.pos_order_id && (lineIds.length === 0 || lineIds.includes(l.id)));
  const sub = lines.reduce((a, l) => a + Number(l.amount), 0);
  const service = sc ? Math.round(sub * pct) / 100 : 0;
  const total = Math.round((sub + service + Number(tip || 0)) * 10 ** dec) / 10 ** dec;
  const cur = cat.register.currency;
  const methods = cat.methods.filter((m) => m.currency === cur);
  const received = methods.reduce((a, m) => a + Number(amounts[m.id] || 0), 0);
  const confirm = async () => {
    setBusy(true);
    try {
      const x = await api<CheckDetail>(`${base}/rest/checks/${d.check.id}/pay`, { method: 'POST', body: {
        session_id: session, line_ids: lineIds, service_charge: sc, tip: tip || '', client_ref: ref,
        tenders: methods.filter((m) => Number(amounts[m.id] || 0) > 0).map((m) => ({ method_id: m.id, amount: amounts[m.id] })),
        change_method_id: received > total ? methods.find((m) => m.kind === 'cash')?.id ?? '' : '' } });
      onPaid(x);
    } catch (e) { onError(e); } finally { setBusy(false); }
  };
  return (
    <div role="dialog" aria-modal="true" style={{ position: 'fixed', inset: 0, background: 'rgba(15,27,45,.45)', display: 'grid', placeItems: 'center', zIndex: 300 }} onClick={onClose}>
      <div className="mx-box" style={{ background: '#fff', padding: 12, width: 'min(460px, 94vw)' }} onClick={(e) => e.stopPropagation()}>
        <h3>{t('vt.r.pay')} · #{d.check.number}</h3>
        <table className="mx-table"><tbody>
          <tr><td>{t('vt.r.subtotal', { n: lines.length })}</td><td dir="ltr">{formatAmount(sub, dec)}</td></tr>
          {pct > 0 && <tr><td><label className="mx-row" style={{ gap: 4 }}><input type="checkbox" checked={sc} onChange={(e) => setSc(e.currentTarget.checked)} />{t('vt.r.serviceN', { n: pct })}</label></td>
            <td dir="ltr">{formatAmount(service, dec)}</td></tr>}
          <tr><td>{t('vt.r.tip')}</td><td><input dir="ltr" inputMode="decimal" style={{ width: 90 }} value={tip} onChange={(e) => setTip(e.currentTarget.value.replace(/[^\d.]/g, ''))} /></td></tr>
          <tr><th>{t('vt.f.total')}</th><th dir="ltr" style={{ fontSize: 20 }}>{formatAmount(total, dec)} {cur}</th></tr>
        </tbody></table>
        <small>{t('vt.r.taxNote')}</small>
        {methods.map((m) => (
          <div key={m.id} className="mx-row" style={{ gap: 6, marginTop: 6 }}>
            <span style={{ flex: 1 }}>{m.name}</span>
            <input dir="ltr" inputMode="decimal" style={{ width: 110, fontSize: 18 }} value={amounts[m.id] ?? ''} onChange={(e) => { const v = e.currentTarget.value.replace(/[^\d.]/g, ''); setAmounts((a) => ({ ...a, [m.id]: v })); }} />
            <button className="mx-btn sm" onClick={() => setAmounts({ [m.id]: String(total) })}>{t('vt.r.exact')}</button>
          </div>))}
        <div className="mx-row" style={{ gap: 6, marginTop: 10 }}>
          <span>{received > total ? t('vt.r.change', { v: formatAmount(received - total, dec) }) : received < total ? t('vt.r.left', { v: formatAmount(total - received, dec) }) : ''}</span>
          <span className="mx-sp" />
          <button className="mx-btn" onClick={onClose}>{t('common.cancel')}</button>
          <button className="mx-btn main" style={big} disabled={busy || received + 0.0001 < total} onClick={() => void confirm()}>{t('vt.r.confirm')}</button>
        </div>
      </div>
    </div>
  );
}

// ======================= kitchen display =======================

interface KLine {
  id: string; check_id: string; check_number: number; kind: string; table_name: string | null; customer: string; item_name: string; item_name_i18n: Record<string, string> | null;
  quantity: string; modifiers: { name: string }[]; note: string; station: string; kstatus: string; sent_at: string; age_s: number;
}

export function Kds({ onExit }: { me: Me; onExit: () => void }) {
  const { t, i18n } = useTranslation();
  const base = useBase();
  const [station, setStation] = useState<string>(() => { try { return localStorage.getItem('paxve-kds-station') ?? ''; } catch { return ''; } });
  const [d, setD] = useState<{ lines: KLine[]; stations: { station: string }[] } | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const load = useCallback(() => {
    if (base) api<{ lines: KLine[]; stations: { station: string }[] }>(`${base}/rest/kds${station ? `?station=${encodeURIComponent(station)}` : ''}`).then((x) => { setD(x); setErr(null); })
      .catch((e) => setErr(errorText(t, e)));
  }, [base, station, t]);
  useEffect(() => { load(); const h = window.setInterval(load, 5000); return () => window.clearInterval(h); }, [load]);
  useEffect(() => { try { localStorage.setItem('paxve-kds-station', station); } catch { /* */ } }, [station]);
  const move = (l: KLine, status: string) => base && api(`${base}/rest/check-lines/${l.id}/status`, { method: 'POST', body: { status } }).then(load).catch((e) => setErr(errorText(t, e)));
  const byCheck = useMemo(() => {
    const m = new Map<string, KLine[]>();
    for (const l of d?.lines ?? []) m.set(l.check_id, [...(m.get(l.check_id) ?? []), l]);
    return [...m.values()];
  }, [d]);
  return (
    <div dir={i18n.dir(i18n.language)} style={{ minHeight: '100vh', background: '#111827', color: '#f9fafb', display: 'flex', flexDirection: 'column' }}>
      <header style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '8px 10px', background: '#000', flexWrap: 'wrap' }}>
        <b style={{ flex: 1 }}>{t('vt.t.kds')}</b>
        <button className={`mx-btn sm ${!station ? 'main' : ''}`} onClick={() => setStation('')}>{t('vt.k.all')}</button>
        {(d?.stations ?? []).map((s) => <button key={s.station} className={`mx-btn sm ${station === s.station ? 'main' : ''}`} onClick={() => setStation(s.station)}>{s.station}</button>)}
        <button className="mx-btn sm" onClick={onExit}>{t('vt.close')}</button>
      </header>
      {err && <div className="mx-flash bad">{err}</div>}
      <main style={{ flex: 1, padding: 8, display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(260px, 1fr))', gap: 8, alignContent: 'start' }}>
        {byCheck.map((ls) => {
          const f = ls[0];
          const age = Math.floor(Math.max(...ls.map((x) => x.age_s)) / 60);
          return (
            <section key={f.check_id} style={{ background: '#1f2937', borderRadius: 8, border: `3px solid ${age >= 20 ? '#dc2626' : age >= 10 ? '#f59e0b' : '#374151'}`, overflow: 'hidden' }}>
              <header style={{ display: 'flex', gap: 6, padding: '6px 8px', background: '#374151', alignItems: 'center' }}>
                <b style={{ fontSize: 20 }}>{f.table_name ?? t(`vt.r.k.${f.kind}`)}</b><span>#{f.check_number}</span>{f.customer && <span>{f.customer}</span>}
                <span style={{ marginInlineStart: 'auto' }} dir="ltr">{age}′</span>
              </header>
              {ls.map((l) => (
                <div key={l.id} style={{ padding: '6px 8px', borderTop: '1px solid #374151', display: 'flex', gap: 6, alignItems: 'center', opacity: l.kstatus === 'ready' ? 0.6 : 1 }}>
                  <div style={{ flex: 1 }}>
                    <b style={{ fontSize: 18 }}><span dir="ltr">{Number(l.quantity)}×</span> {l.item_name_i18n?.[i18n.language] || l.item_name}</b>
                    {(l.modifiers.length > 0 || l.note) && <div style={{ color: '#fbbf24' }}>{[...l.modifiers.map((m) => m.name), l.note].filter(Boolean).join(' · ')}</div>}
                    {!station && <small style={{ opacity: 0.7 }}>{l.station}</small>}
                  </div>
                  {l.kstatus === 'sent' && <button className="mx-btn" style={{ minHeight: 44 }} onClick={() => void move(l, 'cooking')}>{t('vt.k.start')}</button>}
                  {l.kstatus === 'cooking' && <button className="mx-btn main" style={{ minHeight: 44 }} onClick={() => void move(l, 'ready')}>{t('vt.k.ready')}</button>}
                  {l.kstatus === 'ready' && <button className="mx-btn" style={{ minHeight: 44 }} onClick={() => void move(l, 'served')}>{t('vt.k.serve')}</button>}
                </div>))}
            </section>);
        })}
        {d && byCheck.length === 0 && <div style={{ padding: 40, textAlign: 'center', opacity: 0.7, gridColumn: '1 / -1' }}>{t('vt.k.empty')}</div>}
      </main>
    </div>
  );
}
