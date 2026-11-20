import { useCallback, useEffect, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { IconBell, IconChecklist } from '@tabler/icons-react';
import { api, can, type Me, type Rate } from '../api';
import { useCompany } from '../company';
import type { PaymentMethod } from '../billing';
import { companyName, formatAmount, today } from '../util';

/** Bottom bar (office, till and collector screens): user, company, own cash box, exchange rate, what *I*
 *  collected today, key hints. */
export default function StatusBar({ me, keys = true, extra, sticky }: { me: Me; keys?: boolean; extra?: ReactNode; sticky?: boolean }) {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [box, setBox] = useState<string>('');
  const [rate, setRate] = useState<string>('');
  const [todayCash, setToday] = useState<string>('');
  const [inbox, setInbox] = useState<{ unread: number; due: number } | null>(null);

  const load = useCallback(() => {
    if (!company) return;
    const cid = company.id;
    // the bell: unread notifications and my activities due today or late
    api<{ unread: number; activities: { overdue: number; today: number } }>(`/companies/${cid}/notifications`)
      .then((n) => setInbox({ unread: n.unread, due: n.activities.overdue + n.activities.today })).catch(() => setInbox(null));
    if (can(me, 'collections.view')) {
      api<PaymentMethod[]>(`/companies/${cid}/payment-methods`).then((ms) => {
        const mine = ms.filter((m) => m.is_active && m.user_id === me.id);
        setBox(mine.map((m) => m.name).join(' / '));
      }).catch(() => {});
      api<{ by_method: { currency: string; received: string; change_given: string }[] }>(
        `/companies/${cid}/payments?from=${today()}&to=${today()}&received_by=${me.id}&limit=1`).then((r) => {
        const sums = new Map<string, number>();
        for (const m of r.by_method ?? []) sums.set(m.currency, (sums.get(m.currency) ?? 0) + Number(m.received) - Number(m.change_given));
        setToday([...sums].map(([c, v]) => `${formatAmount(v, decimalsOf(c))} ${c}`).join(' + ') || '0');
      }).catch(() => {});
    }
    api<{ rates: Rate[] }>(`/companies/${cid}/rates/latest`).then((r) => {
      const local = company.local_currency !== company.base_currency ? r.rates.find((x) => x.currency === company.local_currency) : undefined;
      setRate(local ? `1 ${company.base_currency} = ${formatAmount(local.rate, 0)} ${local.currency}` : '');
    }).catch(() => {});
  }, [company, me, decimalsOf]);

  useEffect(() => {
    load();
    const id = setInterval(load, 60_000);
    window.addEventListener('paxve:paid', load);
    return () => { clearInterval(id); window.removeEventListener('paxve:paid', load); };
  }, [load]);

  return (
    <footer className="mx-status" style={sticky ? { position: 'sticky', bottom: 0, zIndex: 20 } : undefined}>
      <span>{t('mx.user')}: <b>{me.full_name}</b></span>
      {company && <span>{t('mx.company')}: <b>{companyName(company, i18n.language)}</b></span>}
      {box && <span>{t('mx.box')}: <b>{box}</b></span>}
      {rate && <span>{t('mx.rate')}: <b dir="ltr">{rate}</b></span>}
      {todayCash && <span title={t('mx.todayHint')}>{t('mx.today')}: <b dir="ltr">{todayCash}</b></span>}
      {extra}
      {inbox && (me.permissions ?? []).length > 0 && me.role !== 'collector' && me.role !== 'cashier' && (
        <a className={`mx-bell ${inbox.unread + inbox.due > 0 ? 'hot' : ''}`} href="#/gaps/inbox" title={t('gp.bell', { n: inbox.unread, d: inbox.due })}>
          <IconBell size={13} aria-hidden /> <b>{inbox.unread}</b> · <IconChecklist size={13} aria-hidden /> <b>{inbox.due}</b>
        </a>
      )}
      {keys && <span><kbd>F2</kbd> {t('mx.keyF2')} · <kbd>F9</kbd> {t('mx.keyF9')} · <kbd>Esc</kbd> {t('mx.keyEsc')}</span>}
    </footer>
  );
}
