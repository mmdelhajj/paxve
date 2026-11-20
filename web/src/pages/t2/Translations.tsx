import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, can, type Me } from '../../api';
import i18n, { languages, setSignedIn } from '../../i18n';
import DataGrid, { type Col } from '../../components/DataGrid';
import { errorText } from '../../util';
import { Help } from '../acc/common';

interface Row { key: string; en: string; shipped: string; own: string }

function flatten(obj: unknown, prefix = '', out: Record<string, string> = {}): Record<string, string> {
  if (obj && typeof obj === 'object') {
    for (const [k, v] of Object.entries(obj as Record<string, unknown>)) {
      if (k === '_meta') continue;
      const key = prefix ? `${prefix}.${k}` : k;
      if (v && typeof v === 'object') flatten(v, key, out);
      else out[key] = String(v);
    }
  }
  return out;
}

/** Settings → Translations: the tenant's own wording for any interface text, in any language. */
export default function Translations({ me }: { me: Me }) {
  const { t } = useTranslation();
  const [lang, setLang] = useState(i18n.language);
  const [own, setOwn] = useState<Record<string, string>>({});
  const [shipped, setShipped] = useState<Record<string, string>>({});
  const [sel, setSel] = useState<Row | null>(null);
  const [value, setValue] = useState('');
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [onlyOwn, setOnlyOwn] = useState(false);
  const canEdit = can(me, 'settings.manage');

  const load = useCallback(() => {
    api<Record<string, string>>(`/translations?lang=${lang}`).then(setOwn).catch(() => setOwn({}));
    // the shipped text of that language (machine translations are loaded on demand)
    const info = languages.find((l) => l.code === lang);
    if (info && 'machine' in info && info.machine) {
      import(`../../locales/extra/${lang}.json`).then((m) => setShipped(flatten(m.default))).catch(() => setShipped({}));
    } else {
      setShipped(flatten(i18n.getResourceBundle(lang, 'translation')));
    }
  }, [lang]);
  useEffect(load, [load]);

  const en = useMemo(() => flatten(i18n.getResourceBundle('en', 'translation')), []);
  const rows: Row[] = useMemo(() => Object.keys(en).sort()
    .map((k) => ({ key: k, en: en[k], shipped: shipped[k] ?? '', own: own[k] ?? '' }))
    .filter((r) => !onlyOwn || r.own), [en, shipped, own, onlyOwn]);

  const machine = languages.find((l) => l.code === lang && 'machine' in l && l.machine);
  const cols: Col<Row>[] = [
    // the texts first; the technical key last, small (it read as untranslated text on the first rows, audit P7-F9)
    { key: 'shipped', title: t('t2.tr.shipped'), get: (r) => r.shipped, width: 300, cls: (r) => (r.shipped === r.en && lang !== 'en' ? 'muted' : '') },
    { key: 'en', title: t('p7.english'), get: (r) => r.en, width: 300 },
    { key: 'own', title: t('t2.tr.own'), get: (r) => r.own, width: 300, cls: () => 'ok' },
    { key: 'key', title: t('t2.tr.key'), get: (r) => r.key, width: 220, cls: () => 'muted', render: (r) => <code dir="ltr" style={{ fontSize: 11 }}>{r.key}</code> },
  ];

  async function save(v: string) {
    if (!sel) return;
    try {
      await api('/translations', { method: 'PUT', body: { lang, key: sel.key, value: v } });
      setMsg({ ok: true, text: t('t2.tr.saved') });
      load();
      setSignedIn(true, true);
    } catch (e) {
      setMsg({ ok: false, text: errorText(t, e) });
    }
  }

  return (
    <div className="mx-col">
      <div className="mx-strip">
        <label>{t('common.language')} <select value={lang} onChange={(e) => { setLang(e.currentTarget.value); setSel(null); }}>
          {languages.map((l) => <option key={l.code} value={l.code}>{l.label}</option>)}
        </select></label>
        <label><input type="checkbox" checked={onlyOwn} onChange={(e) => setOnlyOwn(e.currentTarget.checked)} /> {t('t2.tr.onlyOwn')}</label>
        {machine && <span className="mx-pill off">{t('t2.tr.machine')}</span>}
        <span className="mx-sp" />
        <Help topic="t2.help.translations" />
      </div>
      <div className="mx-body" style={{ gridTemplateColumns: 'minmax(0, 1fr) minmax(280px, 360px)' }}>
        <section className="mx-box">
          <h3>{t('t2.tr.title')} <small>{t('t2.tr.hint')}</small></h3>
          <DataGrid rows={rows} cols={cols} rowKey={(r) => r.key} selected={sel?.key ?? null}
            onSelect={(r) => { setSel(r); setValue(r.own || r.shipped || r.en); setMsg(null); }}
            onActivate={(r) => { setSel(r); setValue(r.own || r.shipped || r.en); setMsg(null); }} title={t('t2.tr.title')} />
        </section>
        <section className="mx-box">
          <h3>{sel ? sel.key : t('t2.tr.pick')}</h3>
          {sel && (
            <div className="mx-pay">
              <div><small>English</small><div>{sel.en}</div></div>
              <div><small>{t('t2.tr.shipped')}</small><div>{sel.shipped || '—'}</div></div>
              <label htmlFor="tr-own"><small>{t('t2.tr.own')}</small></label>
              <textarea id="tr-own" rows={5} value={value} disabled={!canEdit} onChange={(e) => setValue(e.currentTarget.value)}
                dir={languages.find((l) => l.code === lang)?.dir} style={{ width: '100%', font: 'inherit' }} />
              <div className="mx-row">
                <button className="mx-btn main" disabled={!canEdit || !value.trim()} onClick={() => void save(value)}>{t('common.save')}</button>
                {sel.own && <button className="mx-btn" disabled={!canEdit} onClick={() => void save('')}>{t('t2.tr.reset')}</button>}
              </div>
              {msg && <div className={`mx-flash ${msg.ok ? 'good' : 'bad'}`}>{msg.text}</div>}
            </div>
          )}
        </section>
      </div>
    </div>
  );
}
