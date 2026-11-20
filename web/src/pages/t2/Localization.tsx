import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, can, type Company, type Me } from '../../api';
import { useCompany } from '../../company';
import { languages } from '../../i18n';
import { PACKS, packName } from '../../l10n';
import { errorText } from '../../util';
import { Help } from '../acc/common';

/** Settings → Country & language: the company's country pack, the language customers get by default,
 *  and what printed invoices say (tax number label, invoice title, legal mentions). */
// common zones (any IANA zone the server knows is accepted)
const ZONES = ['Asia/Beirut', 'Asia/Riyadh', 'Asia/Dubai', 'Asia/Kuwait', 'Asia/Qatar', 'Asia/Bahrain', 'Asia/Muscat', 'Asia/Amman', 'Asia/Damascus', 'Asia/Baghdad',
  'Africa/Cairo', 'Africa/Casablanca', 'Africa/Algiers', 'Africa/Tunis', 'Europe/Istanbul', 'Europe/Paris', 'Europe/London', 'America/New_York', 'Etc/UTC'];

export default function Localization({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const { company, reload } = useCompany();
  const [f, setF] = useState({ default_locale: 'ar', tax_id_label: '', invoice_title: '', legal_mentions: '', timezone: 'Asia/Beirut' });
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const canEdit = can(me, 'settings.manage');
  useEffect(() => {
    if (company) setF({ default_locale: company.default_locale || 'ar', tax_id_label: company.tax_id_label ?? '',
      invoice_title: company.invoice_title ?? '', legal_mentions: company.legal_mentions ?? '', timezone: company.timezone || 'Asia/Beirut' });
  }, [company]);
  if (!company) return null;
  const pack = PACKS.find((p) => p.key === company.l10n);

  async function save() {
    if (!company) return;
    try {
      await api<Company>(`/companies/${company.id}`, { method: 'PATCH', body: f });
      setMsg({ ok: true, text: t('t2.saved') });
      reload();
    } catch (e) {
      setMsg({ ok: false, text: errorText(t, e) });
    }
  }

  const row = (label: string, value: React.ReactNode) => (
    <div className="mx-line"><span style={{ minWidth: 190 }}>{label}</span><b>{value}</b></div>
  );
  return (
    <div className="mx-col">
      <div className="mx-strip"><b>{t('t2.loc.title')}</b><span className="mx-sp" /><Help topic="t2.help.localization" /></div>
      <div className="mx-body" style={{ gridTemplateColumns: 'repeat(auto-fit, minmax(320px, 1fr))' }}>
        <section className="mx-box">
          <h3>{t('t2.loc.pack')}</h3>
          <div className="mx-pay">
            {row(t('t2.loc.country'), pack ? packName(pack, i18n.language, t('signup.other')) : (company.l10n || '—'))}
            {row(t('t2.loc.currency'), `${company.base_currency}${company.local_currency !== company.base_currency ? ` · ${company.local_currency}` : ''}`)}
            {row(t('t2.loc.fiscal'), company.fiscal_year_start ?? '01-01')}
            {row(t('t2.loc.einvoice'), company.einvoice === 'zatca' ? t('t2.loc.zatca') : company.einvoice === 'eta' ? t('t2.loc.eta') : t('t2.loc.none'))}
            <small>{t('t2.loc.packHint')}</small>
          </div>
        </section>
        <section className="mx-box">
          <h3>{t('t2.loc.docs')}</h3>
          <div className="mx-pay">
            <label htmlFor="loc-lang">{t('t2.loc.defaultLang')}</label>
            <select id="loc-lang" disabled={!canEdit} value={f.default_locale} onChange={(e) => setF({ ...f, default_locale: e.currentTarget.value })}>
              {languages.map((l) => <option key={l.code} value={l.code}>{l.label}</option>)}
            </select>
            <label htmlFor="loc-tz">{t('t2.loc.timezone')}</label>
            <select id="loc-tz" disabled={!canEdit} value={f.timezone} onChange={(e) => setF({ ...f, timezone: e.currentTarget.value })}>
              {[...new Set([f.timezone, ...ZONES])].map((z) => <option key={z} value={z}>{z}</option>)}
            </select>
            <label htmlFor="loc-tax">{t('t2.loc.taxLabel')}</label>
            <input id="loc-tax" disabled={!canEdit} value={f.tax_id_label} onChange={(e) => setF({ ...f, tax_id_label: e.currentTarget.value })} />
            <label htmlFor="loc-title">{t('t2.loc.invoiceTitle')}</label>
            <input id="loc-title" disabled={!canEdit} value={f.invoice_title} placeholder={t('invoices.invoice')}
              onChange={(e) => setF({ ...f, invoice_title: e.currentTarget.value })} />
            <label htmlFor="loc-ment">{t('t2.loc.mentions')}</label>
            <textarea id="loc-ment" rows={3} disabled={!canEdit} value={f.legal_mentions} onChange={(e) => setF({ ...f, legal_mentions: e.currentTarget.value })} />
            <div className="mx-row"><button className="mx-btn main" disabled={!canEdit} onClick={() => void save()}>{t('common.save')}</button></div>
            {msg && <div className={`mx-flash ${msg.ok ? 'good' : 'bad'}`}>{msg.text}</div>}
          </div>
        </section>
      </div>
    </div>
  );
}
