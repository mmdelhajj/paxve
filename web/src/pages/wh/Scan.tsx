import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, type Me } from '../../api';
import { useCompany } from '../../company';
import { errorText } from '../../util';
import { opCode, type OpLine, type Warehouse } from './Wh';

// The warehouse scanner (phone, PWA): look up any code, receive a purchase order, do the ready tasks (putaway, QC,
// pick, pack), move between bins and count a bin. Works with the phone camera (BarcodeDetector) and with a
// Bluetooth / USB scanner (it types the code + Enter into the scan box). Moves, counts and finished tasks made
// while offline wait in a queue on the phone and are sent when the connection is back.

type Mode = 'lookup' | 'tasks' | 'receive' | 'move' | 'count';
interface ScanRes { type: string; data?: Record<string, unknown>; code?: string }
interface Queued { id: string; path: string; body: unknown; label: string; at: string }

const QKEY = 'paxve-wh-queue';
function readQueue(): Queued[] {
  try { return JSON.parse(localStorage.getItem(QKEY) ?? '[]') as Queued[]; } catch { return []; }
}
function writeQueue(q: Queued[]) {
  try { localStorage.setItem(QKEY, JSON.stringify(q)); } catch { /* storage unavailable */ }
}
function isNetworkError(e: unknown): boolean {
  return !navigator.onLine || (e instanceof TypeError);
}

/** Camera scanner (Chrome on Android: BarcodeDetector). */
function Camera({ onCode, onClose }: { onCode: (c: string) => void; onClose: () => void }) {
  const { t } = useTranslation();
  const video = useRef<HTMLVideoElement>(null);
  const [err, setErr] = useState('');
  useEffect(() => {
    let stream: MediaStream | null = null;
    let timer = 0;
    let stop = false;
    const BD = (window as unknown as { BarcodeDetector?: new (o?: unknown) => { detect: (v: HTMLVideoElement) => Promise<{ rawValue: string }[]> } }).BarcodeDetector;
    if (!BD || !navigator.mediaDevices?.getUserMedia) { setErr(t('wh.scan.noCamera')); return; }
    const det = new BD();
    navigator.mediaDevices.getUserMedia({ video: { facingMode: 'environment' } }).then((s) => {
      stream = s;
      if (!video.current || stop) return;
      video.current.srcObject = s;
      void video.current.play();
      const tick = async () => {
        if (stop || !video.current) return;
        try {
          const found = await det.detect(video.current);
          if (found.length > 0 && found[0].rawValue) { onCode(found[0].rawValue); return; }
        } catch { /* frame not ready */ }
        timer = window.setTimeout(() => void tick(), 250);
      };
      void tick();
    }).catch(() => setErr(t('wh.scan.cameraDenied')));
    return () => { stop = true; window.clearTimeout(timer); stream?.getTracks().forEach((x) => x.stop()); };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  return (
    <div style={{ position: 'fixed', inset: 0, background: '#000', zIndex: 50, display: 'flex', flexDirection: 'column' }}>
      {err ? <div style={{ color: '#fff', padding: 20 }}>{err}</div> : <video ref={video} playsInline muted style={{ flex: 1, objectFit: 'cover', width: '100%' }} />}
      <button className="mx-btn" style={{ margin: 12, minHeight: 48 }} onClick={onClose}>{t('wh.close')}</button>
    </div>
  );
}

export default function Scan({ me, onExit }: { me: Me; onExit: () => void }) {
  const { t, i18n } = useTranslation();
  const { company } = useCompany();
  const base = company ? `/companies/${company.id}` : null;
  const [mode, setMode] = useState<Mode>('lookup');
  const [whs, setWhs] = useState<Warehouse[]>([]);
  const [wh, setWh] = useState('');
  const [code, setCode] = useState('');
  const [cam, setCam] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [queue, setQueue] = useState<Queued[]>(readQueue());
  const [last, setLast] = useState<ScanRes | null>(null);
  const [detail, setDetail] = useState<unknown>(null);
  const input = useRef<HTMLInputElement>(null);
  const handler = useRef<(r: ScanRes, raw: string) => void>(() => {});

  useEffect(() => {
    if (!base) return;
    api<Warehouse[]>(`${base}/wh/warehouses`).then((l) => { setWhs(l); setWh((w) => w || l.find((x) => x.use_bins)?.id || l[0]?.id || ''); }).catch(() => {});
  }, [base]);
  const focus = () => window.setTimeout(() => input.current?.focus(), 50);
  useEffect(() => { focus(); }, [mode]);

  // offline queue
  const send = useCallback(async (path: string, body: unknown, label: string) => {
    try {
      const r = await api(path, { method: 'POST', body });
      setMsg({ ok: true, text: label });
      return r;
    } catch (e) {
      if (isNetworkError(e)) {
        const q = [...readQueue(), { id: String(Date.now()), path, body, label, at: new Date().toISOString() }];
        writeQueue(q); setQueue(q);
        setMsg({ ok: true, text: t('wh.scan.queued') });
        return null;
      }
      setMsg({ ok: false, text: errorText(t, e) });
      throw e;
    }
  }, [t]);
  const sync = useCallback(async () => {
    const q = readQueue();
    const left: Queued[] = [];
    let failed = '';
    for (const x of q) {
      try { await api(x.path, { method: 'POST', body: x.body }); } catch (e) {
        if (isNetworkError(e)) left.push(x); else failed = `${x.label}: ${errorText(t, e)}`;
      }
    }
    writeQueue(left); setQueue(left);
    setMsg(failed ? { ok: false, text: failed } : { ok: true, text: t('wh.scan.synced') });
  }, [t]);
  useEffect(() => {
    const on = () => { if (readQueue().length) void sync(); };
    window.addEventListener('online', on);
    return () => window.removeEventListener('online', on);
  }, [sync]);

  async function scan(raw: string) {
    const c = raw.trim();
    setCode('');
    if (!c || !base) return;
    try {
      const r = await api<ScanRes>(`${base}/wh/scan?code=${encodeURIComponent(c)}`);
      setLast(r);
      if (navigator.vibrate) navigator.vibrate(40);
      handler.current(r, c);
    } catch (e) { setMsg({ ok: false, text: errorText(t, e) }); }
    focus();
  }

  const dir = i18n.dir();
  return (
    <div dir={dir} style={{ minHeight: '100vh', background: '#f3f5f8', display: 'flex', flexDirection: 'column', fontSize: 16 }}>
      <header style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '8px 10px', background: '#14324f', color: '#fff', paddingTop: 'calc(8px + env(safe-area-inset-top, 0px))' }}>
        <b style={{ flex: 1 }}>{t('wh.scan.title')}</b>
        <select value={wh} onChange={(e) => setWh(e.currentTarget.value)} aria-label={t('wh.warehouse')} style={{ maxWidth: 150 }}>
          {whs.map((w) => <option key={w.id} value={w.id}>{w.name}</option>)}
        </select>
        <button className="mx-btn sm" onClick={onExit}>{t('wh.close')}</button>
      </header>
      <nav style={{ display: 'flex', gap: 4, padding: 6, overflowX: 'auto' }}>
        {(['lookup', 'tasks', 'receive', 'move', 'count'] as Mode[]).map((m) => (
          <button key={m} className={`mx-btn ${mode === m ? 'main' : ''}`} style={{ minHeight: 44, whiteSpace: 'nowrap' }} onClick={() => { setMode(m); setDetail(null); setMsg(null); }}>{t(`wh.scan.m.${m}`)}</button>))}
      </nav>
      <form onSubmit={(e) => { e.preventDefault(); void scan(code); }} style={{ display: 'flex', gap: 6, padding: '0 6px' }}>
        <input ref={input} value={code} onChange={(e) => setCode(e.currentTarget.value)} placeholder={t('wh.scan.placeholder')} aria-label={t('wh.scan.placeholder')}
          dir="ltr" autoComplete="off" style={{ flex: 1, minHeight: 48, fontSize: 18, padding: '0 10px' }} />
        <button type="button" className="mx-btn" style={{ minHeight: 48 }} onClick={() => setCam(true)}>{t('wh.scan.camera')}</button>
      </form>
      {queue.length > 0 && <div className="mx-note" style={{ margin: 6 }}>{t('wh.scan.pending', { n: queue.length })} <button className="mx-btn sm" onClick={() => void sync()}>{t('wh.scan.sync')}</button></div>}
      {msg && <div className={`mx-flash ${msg.ok ? 'good' : 'bad'}`} style={{ margin: 6 }}>{msg.text}</div>}
      <main style={{ flex: 1, padding: 6, display: 'flex', flexDirection: 'column', gap: 8 }}>
        {base && mode === 'lookup' && <Lookup base={base} last={last} detail={detail} setDetail={setDetail} handler={handler} />}
        {base && mode === 'tasks' && <Tasks base={base} wh={wh} handler={handler} send={send} me={me} />}
        {base && mode === 'receive' && <Receive base={base} handler={handler} send={send} />}
        {base && mode === 'move' && <Move base={base} wh={wh} handler={handler} send={send} />}
        {base && mode === 'count' && <Count base={base} wh={wh} handler={handler} send={send} />}
      </main>
      {cam && <Camera onClose={() => { setCam(false); focus(); }} onCode={(c) => { setCam(false); void scan(c); }} />}
    </div>
  );
}

type Handler = React.MutableRefObject<(r: ScanRes, raw: string) => void>;
type Send = (path: string, body: unknown, label: string) => Promise<unknown>;
const big: React.CSSProperties = { minHeight: 52, fontSize: 18 };
const card: React.CSSProperties = { background: '#fff', borderRadius: 10, padding: 10, border: '1px solid #dde3ea' };
const q = (v: string | number | null | undefined) => (v == null ? '' : String(Number(v)));

function Lookup({ base, last, detail, setDetail, handler }: { base: string; last: ScanRes | null; detail: unknown; setDetail: (d: unknown) => void; handler: Handler }) {
  const { t } = useTranslation();
  handler.current = (r) => {
    const d = (r.data ?? {}) as Record<string, string>;
    if (r.type === 'item') api(`${base}/wh/bin-stock?item_id=${d.id}`).then(setDetail).catch(() => setDetail(null));
    else if (r.type === 'location') api(`${base}/wh/bin-stock?location_id=${d.id}`).then(setDetail).catch(() => setDetail(null));
    else if (r.type === 'package') api(`${base}/wh/packages/${d.id}`).then(setDetail).catch(() => setDetail(null));
    else if (r.type === 'op') api(`${base}/wh/ops/${d.id}`).then(setDetail).catch(() => setDetail(null));
    else setDetail(null);
  };
  if (!last) return <div style={card}>{t('wh.scan.lookupHelp')}</div>;
  const d = (last.data ?? {}) as Record<string, string>;
  const bins = (detail as { bins?: { id: string; location_code: string; item_code: string; item_name: string; lot_no: string | null; quantity: string; warehouse: string }[]; unbinned?: { warehouse: string; quantity: string; item_code: string }[] } | null);
  return (
    <div style={card}>
      <div><span className="mx-pill">{t(`wh.scan.t.${last.type}`, { defaultValue: last.type })}</span> <b dir="ltr">{d.code ?? d.lot_no ?? last.code ?? ''}</b> {d.name ?? d.item_name ?? d.warehouse ?? ''}</div>
      {bins?.bins && <table className="mx-table" style={{ marginTop: 6 }}><tbody>
        {bins.bins.map((b) => <tr key={b.id}><td dir="ltr">{b.location_code}</td><td>{b.item_code} {b.item_name}{b.lot_no ? ` [${b.lot_no}]` : ''}</td><td dir="ltr"><b>{q(b.quantity)}</b></td></tr>)}
        {(bins.unbinned ?? []).map((u, i) => <tr key={'u' + i}><td>{t('wh.unbinned')} ({u.warehouse})</td><td>{u.item_code}</td><td dir="ltr"><b>{q(u.quantity)}</b></td></tr>)}
        {bins.bins.length === 0 && (bins.unbinned ?? []).length === 0 && <tr><td className="mx-empty">{t('wh.bins.none')}</td></tr>}
      </tbody></table>}
      {last.type === 'package' && detail != null && <pre style={{ whiteSpace: 'pre-wrap', fontSize: 13 }}>{(detail as { lines: { item_code: string; quantity: string }[] }).lines.map((l) => `${l.item_code} × ${q(l.quantity)}`).join('\n')}</pre>}
      {last.type === 'op' && detail != null && <div>{t(`wh.k.${(detail as { op: { kind: string } }).op.kind}`)} · {t(`wh.st.${(detail as { op: { status: string } }).op.status}`)}</div>}
      {last.type === 'unknown' && <div>{t('wh.scan.unknown')}</div>}
    </div>
  );
}

interface TaskOp { id: string; number: number; kind: string; origin: string; customer_name: string | null; lines: number; quantity: string; scheduled_date: string }

function Tasks({ base, wh, handler, send, me }: { base: string; wh: string; handler: Handler; send: Send; me: Me }) {
  const { t } = useTranslation();
  const [ops, setOps] = useState<TaskOp[]>([]);
  const [open, setOpen] = useState<{ op: TaskOp & { warehouse_id: string; sales_order_id: string | null }; lines: OpLine[] } | null>(null);
  const [done, setDone] = useState<Record<string, number>>({});
  const [at, setAt] = useState<string | null>(null);
  const [pkg, setPkg] = useState('');
  const load = useCallback(() => { api<TaskOp[]>(`${base}/wh/ops?status=ready&warehouse_id=${wh}`).then(setOps).catch(() => setOps([])); }, [base, wh]);
  useEffect(load, [load]);
  void me;
  function openOp(id: string) {
    api<typeof open>(`${base}/wh/ops/${id}`).then((d) => { setOpen(d); setDone({}); setAt(null); setPkg(''); }).catch(() => {});
  }
  handler.current = (r, raw) => {
    const d = (r.data ?? {}) as Record<string, string>;
    if (!open) { if (r.type === 'op') openOp(d.id); return; }
    if (r.type === 'location') { setAt(d.id); return; }
    if (r.type === 'package') { setPkg(d.id); return; }
    // an item (or its lot): count one more on the first line still short (at the scanned bin first)
    const match = (l: OpLine) => (r.type === 'item' && l.item_id === d.id) || (r.type === 'lot' && l.item_id === d.item_id) || l.barcode === raw;
    const cands = open.lines.filter(match);
    const pick = cands.find((l) => (done[l.id] ?? 0) < Number(l.quantity) && (!at || l.from_location_id === at || l.to_location_id === at)) ??
      cands.find((l) => (done[l.id] ?? 0) < Number(l.quantity));
    const factor = Number((d as { factor?: string }).factor ?? 1) || 1;
    if (pick) setDone((m) => ({ ...m, [pick.id]: Math.min(Number(pick.quantity), (m[pick.id] ?? 0) + factor) }));
  };
  if (open) {
    const op = open.op;
    const all = open.lines.every((l) => (done[l.id] ?? 0) >= Number(l.quantity));
    return (
      <div style={card}>
        <h3 style={{ margin: 0 }}>{t(`wh.k.${op.kind}`)} <span dir="ltr">{opCode(op.number)}</span></h3>
        <small>{op.origin} {op.customer_name ?? ''}</small>
        {op.kind === 'pack' && <div className="mx-row" style={{ gap: 6, alignItems: 'center', margin: '6px 0' }}>
          <span>{t('wh.package')}: <b dir="ltr">{pkg ? t('wh.scan.pkgSet') : '—'}</b></span>
          <button className="mx-btn sm" onClick={() => api<{ package: { id: string } }>(`${base}/wh/packages`, { method: 'POST', body: { warehouse_id: op.warehouse_id, kind: 'box', sales_order_id: op.sales_order_id ?? '' } })
            .then((x) => setPkg(x.package.id)).catch(() => {})}>{t('wh.pkg.new')}</button>
        </div>}
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6, marginTop: 6 }}>
          {open.lines.map((l) => {
            const n = done[l.id] ?? 0; const full = n >= Number(l.quantity);
            return (
              <div key={l.id} style={{ border: '1px solid #dde3ea', borderRadius: 8, padding: 8, background: full ? '#e8f6ec' : at && l.from_location_id === at ? '#fff8e1' : '#fff' }}>
                <div dir="ltr" style={{ fontSize: 20, fontWeight: 700 }}>{l.from_code ?? (op.kind === 'putaway' ? t('wh.arrival') : t('wh.unbinned'))} → {l.to_code ?? (op.kind === 'ship' ? t('wh.customer') : t('wh.unbinned'))}</div>
                <div>{l.item_code} · {l.item_name}{l.lot_no ? ` [${l.lot_no}]` : ''}</div>
                <div className="mx-row" style={{ gap: 6, alignItems: 'center' }}>
                  <button className="mx-btn" style={big} aria-label="-" onClick={() => setDone({ ...done, [l.id]: Math.max(0, n - 1) })}>−</button>
                  <b dir="ltr" style={{ fontSize: 22 }}>{n} / {q(l.quantity)}</b>
                  <button className="mx-btn" style={big} aria-label="+" onClick={() => setDone({ ...done, [l.id]: Math.min(Number(l.quantity), n + 1) })}>+</button>
                  <button className="mx-btn sm" onClick={() => setDone({ ...done, [l.id]: Number(l.quantity) })}>{t('wh.scan.all')}</button>
                </div>
              </div>);
          })}
        </div>
        <div className="mx-row" style={{ gap: 6, marginTop: 8 }}>
          <button className="mx-btn main" style={{ ...big, flex: 1 }} onClick={() => void send(`${base}/wh/ops/${op.id}/done`, {
            lines: open.lines.map((l) => ({ line_id: l.id, done_qty: String(done[l.id] ?? 0), package_id: op.kind === 'pack' ? pkg : '' })),
          }, `${t(`wh.k.${op.kind}`)} ${opCode(op.number)} ✓`).then(() => { setOpen(null); load(); }).catch(() => {})} disabled={!Object.values(done).some((x) => x > 0)}>
            {all ? t(`wh.doneBtn.${op.kind}`) : t('wh.scan.donePartial')}</button>
          <button className="mx-btn" style={big} onClick={() => { setOpen(null); load(); }}>{t('wh.close')}</button>
        </div>
      </div>
    );
  }
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      {ops.length === 0 && <div style={card}>{t('wh.scan.noTasks')}</div>}
      {ops.map((o) => (
        <button key={o.id} className="mx-btn" style={{ ...card, textAlign: 'start', minHeight: 60 }} onClick={() => openOp(o.id)}>
          <b>{t(`wh.k.${o.kind}`)}</b> <span dir="ltr">{opCode(o.number)}</span><br /><small>{o.origin} {o.customer_name ?? ''} · {o.lines} {t('wh.scan.lines')}</small>
        </button>))}
    </div>
  );
}

interface PO { id: string; number: number; supplier_name: string; status: string }
interface POLine { id: string; item_id: string | null; item_code: string | null; description: string; to_receive: string; unit: string | null }

function Receive({ base, handler, send }: { base: string; handler: Handler; send: Send }) {
  const { t } = useTranslation();
  const [pos, setPos] = useState<PO[]>([]);
  const [po, setPo] = useState<{ order: PO; lines: POLine[] } | null>(null);
  const [n, setN] = useState<Record<string, number>>({});
  const load = useCallback(() => { api<PO[]>(`${base}/purchase-orders?status=open`).then((l) => setPos(l.filter((x) => x.status === 'confirmed' || x.status === 'partial'))).catch(() => setPos([])); }, [base]);
  useEffect(load, [load]);
  handler.current = (r) => {
    if (!po || r.type !== 'item') return;
    const d = (r.data ?? {}) as Record<string, string>;
    const l = po.lines.find((x) => x.item_id === d.id && (n[x.id] ?? 0) < Number(x.to_receive));
    const factor = Number(d.factor ?? 1) || 1;
    if (l) setN((m) => ({ ...m, [l.id]: Math.min(Number(l.to_receive), (m[l.id] ?? 0) + factor) }));
  };
  if (po) return (
    <div style={card}>
      <h3 style={{ margin: 0 }}>PO {po.order.number} · {po.order.supplier_name}</h3>
      {po.lines.filter((l) => Number(l.to_receive) > 0).map((l) => (
        <div key={l.id} className="mx-row" style={{ gap: 6, alignItems: 'center', borderBottom: '1px solid #eee', padding: '6px 0' }}>
          <span style={{ flex: 1 }}>{l.item_code ?? ''} {l.description}</span>
          <input dir="ltr" inputMode="decimal" style={{ width: 70, ...big }} aria-label={l.description} value={String(n[l.id] ?? 0)} onChange={(e) => setN({ ...n, [l.id]: Number(e.currentTarget.value) || 0 })} />
          <span dir="ltr">/ {q(l.to_receive)}</span>
        </div>))}
      <div className="mx-row" style={{ gap: 6, marginTop: 8 }}>
        <button className="mx-btn main" style={{ ...big, flex: 1 }} disabled={!Object.values(n).some((x) => x > 0)} onClick={() => void send(`${base}/purchase-orders/${po.order.id}/receive`, {
          lines: Object.entries(n).filter(([, v]) => v > 0).map(([k, v]) => ({ po_line_id: k, quantity: String(v) })) }, `PO ${po.order.number} ✓`).then(() => { setPo(null); setN({}); load(); }).catch(() => {})}>{t('wh.scan.receive')}</button>
        <button className="mx-btn" style={big} onClick={() => setPo(null)}>{t('wh.close')}</button>
      </div>
    </div>);
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      {pos.length === 0 && <div style={card}>{t('wh.scan.noPO')}</div>}
      {pos.map((p) => <button key={p.id} className="mx-btn" style={{ ...card, textAlign: 'start', minHeight: 56 }}
        onClick={() => api<{ order: PO; lines: POLine[] }>(`${base}/purchase-orders/${p.id}`).then((d) => { setPo(d); setN({}); }).catch(() => {})}>PO {p.number} · {p.supplier_name}</button>)}
    </div>
  );
}

function Move({ base, wh, handler, send }: { base: string; wh: string; handler: Handler; send: Send }) {
  const { t } = useTranslation();
  const [from, setFrom] = useState<{ id: string; code: string } | null>(null);
  const [item, setItem] = useState<{ id: string; code: string; name: string } | null>(null);
  const [to, setTo] = useState<{ id: string; code: string } | null>(null);
  const [qty, setQty] = useState('1');
  const [unbinned, setUnbinned] = useState(false);
  handler.current = (r) => {
    const d = (r.data ?? {}) as Record<string, string>;
    if (r.type === 'location') { if (!from && !unbinned) setFrom({ id: d.id, code: d.code }); else setTo({ id: d.id, code: d.code }); }
    else if (r.type === 'item') { if (item?.id === d.id) setQty((x) => String((Number(x) || 0) + (Number(d.factor ?? 1) || 1))); else { setItem({ id: d.id, code: d.code, name: d.name }); setQty(String(Number(d.factor ?? 1) || 1)); } }
  };
  const reset = () => { setFrom(null); setItem(null); setTo(null); setQty('1'); setUnbinned(false); };
  return (
    <div style={card}>
      <ol style={{ margin: 0, paddingInlineStart: 20, display: 'flex', flexDirection: 'column', gap: 8 }}>
        <li>{t('wh.scan.from')}: <b dir="ltr">{from?.code ?? (unbinned ? t('wh.unbinned') : '…')}</b>
          {!from && !unbinned && <button className="mx-btn sm" onClick={() => setUnbinned(true)}>{t('wh.unbinned')}</button>}</li>
        <li>{t('wh.item')}: <b>{item ? `${item.code} · ${item.name}` : '…'}</b>
          {item && <input dir="ltr" inputMode="decimal" aria-label={t('wh.qty')} style={{ width: 80, marginInlineStart: 6, ...big }} value={qty} onChange={(e) => setQty(e.currentTarget.value)} />}</li>
        <li>{t('wh.scan.to')}: <b dir="ltr">{to?.code ?? '…'}</b></li>
      </ol>
      <div className="mx-row" style={{ gap: 6, marginTop: 10 }}>
        <button className="mx-btn main" style={{ ...big, flex: 1 }} disabled={!item || !to || !(from || unbinned)} onClick={() => void send(`${base}/wh/move`, {
          warehouse_id: wh, item_id: item!.id, quantity: qty, from_location_id: from?.id ?? '', to_location_id: to!.id }, `${item!.code} ${qty} → ${to!.code}`).then(reset).catch(() => {})}>{t('wh.move')}</button>
        <button className="mx-btn" style={big} onClick={reset}>{t('wh.scan.reset')}</button>
      </div>
    </div>
  );
}

function Count({ base, wh, handler, send }: { base: string; wh: string; handler: Handler; send: Send }) {
  const { t } = useTranslation();
  const [loc, setLoc] = useState<{ id: string; code: string } | null>(null);
  const [rows, setRows] = useState<{ item_id: string; code: string; name: string; expected: number; counted: number }[]>([]);
  handler.current = (r) => {
    const d = (r.data ?? {}) as Record<string, string>;
    if (r.type === 'location') {
      setLoc({ id: d.id, code: d.code });
      api<{ bins: { item_id: string; item_code: string; item_name: string; quantity: string }[] }>(`${base}/wh/bin-stock?location_id=${d.id}`).then((x) => {
        const m = new Map<string, { item_id: string; code: string; name: string; expected: number; counted: number }>();
        for (const b of x.bins) { const e = m.get(b.item_id); if (e) e.expected += Number(b.quantity); else m.set(b.item_id, { item_id: b.item_id, code: b.item_code, name: b.item_name, expected: Number(b.quantity), counted: 0 }); }
        setRows([...m.values()]);
      }).catch(() => setRows([]));
    } else if (r.type === 'item' && loc) {
      const f = Number(d.factor ?? 1) || 1;
      setRows((rs) => rs.some((x) => x.item_id === d.id) ? rs.map((x) => x.item_id === d.id ? { ...x, counted: x.counted + f } : x)
        : [...rs, { item_id: d.id, code: d.code, name: d.name, expected: 0, counted: f }]);
    }
  };
  if (!loc) return <div style={card}>{t('wh.scan.countHelp')}</div>;
  return (
    <div style={card}>
      <h3 style={{ margin: 0 }} dir="ltr">{loc.code}</h3>
      {rows.map((x) => (
        <div key={x.item_id} className="mx-row" style={{ gap: 6, alignItems: 'center', borderBottom: '1px solid #eee', padding: '6px 0' }}>
          <span style={{ flex: 1 }}>{x.code} · {x.name} <small>({t('wh.scan.expected')} {x.expected})</small></span>
          <input dir="ltr" inputMode="decimal" style={{ width: 70, ...big }} aria-label={x.code} value={String(x.counted)} onChange={(e) => setRows(rows.map((y) => y.item_id === x.item_id ? { ...y, counted: Number(e.currentTarget.value) || 0 } : y))} />
        </div>))}
      <div className="mx-row" style={{ gap: 6, marginTop: 8 }}>
        <button className="mx-btn main" style={{ ...big, flex: 1 }} onClick={() => void send(`${base}/wh/count`, { warehouse_id: wh, location_id: loc.id,
          lines: rows.map((x) => ({ item_id: x.item_id, counted: String(x.counted) })) }, `${loc.code} ✓`).then(() => { setLoc(null); setRows([]); }).catch(() => {})}>{t('wh.scan.saveCount')}</button>
        <button className="mx-btn" style={big} onClick={() => { setLoc(null); setRows([]); }}>{t('wh.close')}</button>
      </div>
    </div>
  );
}

