import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, can, type Me } from '../api';
import { openScreen } from '../nav';
import { errorText, formatDate } from '../util';

/** One line under the ribbon: support view (platform owner), trial days left, plan ending, grace, read-only. */
export default function SubBanner({ me }: { me: Me }) {
  const { t } = useTranslation();
  const [err, setErr] = useState('');

  if (me.support) {
    const end = async () => {
      try {
        await api('/platform/support/end', { method: 'POST', body: {} });
        window.location.hash = '#/platform/orgs';
        window.location.reload();
      } catch (e) { setErr(errorText(t, e)); }
    };
    return (
      <div className="mx-subbar support" role="status">
        <b>{t('pc.banner.support', { org: me.tenant_name })}</b>
        <span>{t('pc.banner.supportAs', { user: me.email, by: me.support.by })}</span>
        <span className="mx-sp" />
        {err && <span>{err}</span>}
        <button className="mx-btn sm" onClick={end}>{t('pc.banner.endSupport')}</button>
      </div>
    );
  }

  const state = me.sub_state;
  const daysTo = (d?: string | null) => (d ? Math.ceil((new Date(d.length === 10 ? d + 'T23:59:59' : d).getTime() - Date.now()) / 86400000) : null);
  let cls = '';
  let text = '';
  if (state === 'trial' && me.trial_ends_at) {
    const n = Math.max(0, daysTo(me.trial_ends_at) ?? 0);
    cls = n <= 3 ? 'warn' : 'info';
    text = t('pc.banner.trial', { count: n });
  } else if (state === 'active' && me.paid_until) {
    const n = daysTo(me.paid_until);
    if (n === null || n > 7) return null;
    cls = 'warn';
    text = t('pc.banner.ending', { date: formatDate(me.paid_until), count: Math.max(0, n) });
  } else if (state === 'grace') {
    cls = 'warn';
    text = t('pc.banner.grace', { date: formatDate(me.paid_until ?? ''), until: formatDate(me.grace_ends ?? '') });
  } else if (state === 'expired') {
    cls = 'bad';
    text = t('pc.banner.expired');
  } else {
    return null;
  }
  return (
    <div className={`mx-subbar ${cls}`} role="status">
      <span>{text}</span>
      <span className="mx-sp" />
      {can(me, 'settings.view') && <button className="mx-btn sm" onClick={() => openScreen('my-sub')}>{t('pc.banner.renew')}</button>}
    </div>
  );
}
