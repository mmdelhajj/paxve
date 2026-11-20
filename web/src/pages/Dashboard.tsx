import { useEffect, useMemo, useState } from 'react';
import {
  Badge, Box, Button, Group, Paper, SegmentedControl, SimpleGrid, Skeleton, Stack, Text, ThemeIcon, Title,
} from '@mantine/core';
import {
  IconAlertTriangle, IconBolt, IconCash, IconGauge, IconUserPlus, IconUsers, IconUserExclamation,
} from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import {
  Bar, BarChart, CartesianGrid, Legend, ResponsiveContainer, Tooltip, XAxis, YAxis,
} from 'recharts';
import { can, canAny, api, type Me } from '../api';
import { useCompany } from '../company';
import { formatAmount, notifyError } from '../util';
import { currentPeriod } from '../billing';
import { EmptyState } from '../components/ListState';
import GettingStarted from '../components/GettingStarted';
import { OwnerSummary } from './acc/Reports3';
import { SUB_BIZ } from '../ribbon';

interface Kpi { currency: string; billed: string; billed_paid: string; collected: string; outstanding: string }
interface Counts { active_subscriptions: number; active_customers: number; customers_owing: number; customers_late_60: number; open_invoices: number }
interface SeriesRow { month: string; currency: string; billed: string; collected: string }
interface Collector { name: string; currency: string; payments: number; amount: string }
interface AreaRow { area: string; currency: string; amount: string; customers: number }
interface Dash { month: string; base_currency: string; kpis: Kpi[]; counts: Counts; series: SeriesRow[]; collectors: Collector[]; outstanding_by_area: AreaRow[] }

function Stat({ label, value, sub, color, icon }: { label: string; value: React.ReactNode; sub?: React.ReactNode; color?: string; icon?: React.ReactNode }) {
  return (
    <Paper withBorder p="md">
      <Group justify="space-between" align="flex-start" wrap="nowrap">
        <div style={{ minWidth: 0 }}>
          <Text size="xs" c="dimmed" tt="uppercase" fw={600}>{label}</Text>
          <Text size="1.6rem" fw={700} c={color} dir="ltr" style={{ textAlign: 'start' }} truncate>{value}</Text>
          {sub && <Text size="xs" c="dimmed" dir="ltr" style={{ textAlign: 'start' }}>{sub}</Text>}
        </div>
        {icon && <ThemeIcon variant="light" color={color?.split('.')[0] ?? 'teal'} size="lg" radius="md">{icon}</ThemeIcon>}
      </Group>
    </Paper>
  );
}

/** Month + year in the reader's language (the browser's own month input shows "November 2026" in English, audit P7-F8). */
function MonthSelect({ value, onChange, label }: { value: string; onChange: (v: string) => void; label: string }) {
  const { i18n } = useTranslation();
  const [y, m] = value.split('-').map(Number);
  const names = useMemo(() => {
    let loc = i18n.language;
    try { new Intl.DateTimeFormat(loc); } catch { loc = 'en'; }
    return Array.from({ length: 12 }, (_, i) => new Intl.DateTimeFormat(loc, { month: 'long' }).format(new Date(2026, i, 1)));
  }, [i18n.language]);
  const set = (yy: number, mm: number) => onChange(`${yy}-${String(mm).padStart(2, '0')}`);
  const now = new Date().getFullYear();
  return (
    <Group gap={4} wrap="nowrap" aria-label={label}>
      <select style={{ padding: '3px 6px', borderRadius: 4, border: '1px solid #ced4da', background: '#fff', font: 'inherit', fontSize: 13 }} aria-label={label} value={m} onChange={(e) => set(y, Number(e.currentTarget.value))}>
        {names.map((n, i) => <option key={i} value={i + 1}>{n}</option>)}
      </select>
      <select style={{ padding: '3px 6px', borderRadius: 4, border: '1px solid #ced4da', background: '#fff', font: 'inherit', fontSize: 13 }} aria-label={label} value={y} onChange={(e) => set(Number(e.currentTarget.value), m)}>
        {Array.from({ length: 8 }, (_, i) => now + 1 - i).map((yy) => <option key={yy} value={yy}>{yy}</option>)}
      </select>
    </Group>
  );
}

const short = (n: number) => new Intl.NumberFormat('en', { notation: 'compact', maximumFractionDigits: 1 }).format(n);

export default function Dashboard({ me }: { me: Me }) {
  const { t } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [month, setMonth] = useState(currentPeriod());
  const [d, setD] = useState<Dash | null>(null);
  const [cur, setCur] = useState<string | null>(null);

  useEffect(() => {
    if (!company || !/^\d{4}-\d{2}$/.test(month)) return;
    setD(null);
    api<Dash>(`/companies/${company.id}/dashboard?month=${month}`).then((x) => {
      setD(x);
      setCur((c) => (c && x.kpis.some((k) => k.currency === c) ? c : x.kpis[0]?.currency ?? x.base_currency));
    }).catch((e) => notifyError(t, e));
  }, [company, month, t]);

  const [noRate, setNoRate] = useState(false);
  useEffect(() => {
    if (!company || company.local_currency === company.base_currency) { setNoRate(false); return; }
    api<{ rates: { currency: string }[] }>(`/companies/${company.id}/rates/latest`)
      .then((r) => setNoRate(!r.rates.some((x) => x.currency === company.local_currency))).catch(() => setNoRate(false));
  }, [company]);

  const series = useMemo(() => (d?.series ?? []).filter((s) => s.currency === cur)
    .map((s) => ({ month: s.month.slice(2), billed: Number(s.billed), collected: Number(s.collected) })), [d, cur]);
  const collectors = useMemo(() => (d?.collectors ?? []).filter((c) => c.currency === cur)
    .map((c) => ({ name: c.name, amount: Number(c.amount) })), [d, cur]);
  const areas = useMemo(() => (d?.outstanding_by_area ?? []).filter((a) => a.currency === cur)
    .map((a) => ({ name: a.area, amount: Number(a.amount), customers: a.customers })), [d, cur]);

  if (!company) return null;
  const k = d?.kpis.find((x) => x.currency === cur);
  const dec = decimalsOf(cur ?? 'USD');
  const rate = k && Number(k.billed) > 0 ? Math.round((Number(k.billed_paid) / Number(k.billed)) * 100) : null;
  const others = (field: keyof Kpi) => d?.kpis.filter((x) => x.currency !== cur && Number(x[field]) !== 0)
    .map((x) => `${formatAmount(x[field], decimalsOf(x.currency))} ${x.currency}`).join(' · ');
  const empty = d && d.kpis.length === 0;
  const subBiz = SUB_BIZ.includes(company.business_type ?? 'generator');
  const canBill = can(me, 'billing.create');

  return (
    <Stack gap="lg" maw={1200}>
      <Group justify="space-between" align="flex-end" wrap="wrap">
        <div>
          <Title order={2}>{t('dashboard.welcome', { name: me.full_name })}</Title>
          <Text c="dimmed" size="sm">{company.display ?? company.name_en} · {me.tenant_name}</Text>
        </div>
        <Group gap="xs">
          {d && d.kpis.length > 1 && (
            <SegmentedControl size="xs" value={cur ?? ''} onChange={setCur} data={d.kpis.map((x) => x.currency)} />
          )}
          <MonthSelect value={month} onChange={setMonth} label={t('readings.period')} />
        </Group>
      </Group>
      {noRate && (
        // a Lebanese company without the LBP rate shows "≈ 0 LBP" everywhere: ask for it first (audit P7-F6)
        <Paper withBorder p="sm" bg="var(--mantine-color-orange-0)">
          <Group justify="space-between" wrap="wrap">
            <Text size="sm" fw={600} c="orange.9">{t('p7.noRateHint')}</Text>
            {can(me, 'accounting.view') && <Button size="xs" color="orange" onClick={() => { window.location.hash = '#/rates'; }}>{t('p7.noRate', { cur: company.local_currency })}</Button>}
          </Group>
        </Paper>
      )}

      {can(me, 'reports.view') && <OwnerSummary />}
      {canAny(me, 'settings.manage', 'billing.manage') && <GettingStarted me={me} />}

      {subBiz ? (
        <Group gap="xs" wrap="wrap">
          <Button size="xs" variant="light" component="a" href="#/customers" leftSection={<IconUserPlus size={14} />}>{t('bill.customers')}</Button>
          <Button size="xs" variant="light" component="a" href="#/readings" leftSection={<IconGauge size={14} />}>{t('bill.readings')}</Button>
          {canBill && <Button size="xs" variant="light" component="a" href="#/invoices" leftSection={<IconBolt size={14} />}>{t('billing.run')}</Button>}
          <Button size="xs" variant="light" component="a" href="#/payments" leftSection={<IconCash size={14} />}>{t('bill.payments')}</Button>
        </Group>
      ) : (
        // shops, restaurants, factories: their own first steps, not meters and monthly bills (audit P7-F1)
        <Group gap="xs" wrap="wrap">
          {company.apps?.includes('pos') && <Button size="xs" variant="light" component="a" href="#/pos" leftSection={<IconCash size={14} />}>{t('p7.eh.a.pos')}</Button>}
          <Button size="xs" variant="light" component="a" href="#/items" leftSection={<IconBolt size={14} />}>{t('p7.eh.a.items')}</Button>
          <Button size="xs" variant="light" component="a" href="#/bills" leftSection={<IconGauge size={14} />}>{t('p7.eh.a.bills')}</Button>
          <Button size="xs" variant="light" component="a" href="#/customers" leftSection={<IconUserPlus size={14} />}>{t('p7.customersAll')}</Button>
        </Group>
      )}

      {!d ? (
        <SimpleGrid cols={{ base: 1, xs: 2, md: 4 }}>{[1, 2, 3, 4].map((i) => <Skeleton key={i} h={96} radius="md" />)}</SimpleGrid>
      ) : empty ? (
        <Paper withBorder>
          {subBiz ? <EmptyState title={t('dash.emptyTitle')} hint={t('dash.emptyHint')}
            action={<Button component="a" href="#/billing-setup" mt="xs">{t('bill.setup')}</Button>} />
            : <EmptyState title={t('p7.shopEmptyTitle')} hint={t('p7.shopEmptyHint')}
              action={<Button component="a" href={company.apps?.includes('pos') ? '#/pos' : '#/items'} mt="xs">{company.apps?.includes('pos') ? t('p7.eh.a.pos') : t('p7.eh.a.items')}</Button>} />}
        </Paper>
      ) : (
        <>
          <SimpleGrid cols={{ base: 1, xs: 2, md: 4 }}>
            <Stat label={t('dash.billed')} value={k ? `${formatAmount(k.billed, dec)} ${cur}` : '—'} sub={others('billed')} />
            <Stat label={t('dash.collected')} value={k ? `${formatAmount(k.collected, dec)} ${cur}` : '—'} sub={others('collected')} color="teal.8" icon={<IconCash size={18} />} />
            <Stat label={t('dash.outstanding')} value={k ? `${formatAmount(k.outstanding, dec)} ${cur}` : '—'} sub={others('outstanding')} color="red.7" />
            <Stat label={t('dash.rate')} value={rate === null ? '—' : `${rate}%`} sub={t('dash.rateHint')}
              color={rate === null ? undefined : rate >= 80 ? 'teal.8' : rate >= 50 ? 'orange.8' : 'red.7'} />
          </SimpleGrid>
          <SimpleGrid cols={{ base: 1, xs: 3 }}>
            <Stat label={t('dash.activeSubs')} value={d.counts.active_subscriptions} sub={`${d.counts.active_customers} ${t('bill.customers')}`} icon={<IconUsers size={18} />} />
            <Stat label={t('dash.owing')} value={d.counts.customers_owing} sub={`${d.counts.open_invoices} ${t('invoices.unpaid')}`} color="orange.8" icon={<IconUserExclamation size={18} />} />
            <Stat label={t('dash.late60')} value={d.counts.customers_late_60} color={d.counts.customers_late_60 ? 'red.7' : undefined}
              icon={<IconAlertTriangle size={18} />} sub={<a href="#/aging">{t('reports2.aging')} →</a>} />
          </SimpleGrid>

          <Paper withBorder p="md">
            <Group justify="space-between" mb="sm">
              <Text fw={600}>{t('dash.trend')}</Text>
              <Badge variant="light">{cur}</Badge>
            </Group>
            <Box h={280} dir="ltr">
              <ResponsiveContainer width="100%" height="100%">
                <BarChart data={series} margin={{ left: 0, right: 8, top: 4 }}>
                  <CartesianGrid strokeDasharray="3 3" vertical={false} />
                  <XAxis dataKey="month" tick={{ fontSize: 11 }} />
                  <YAxis tickFormatter={short} tick={{ fontSize: 11 }} width={48} />
                  <Tooltip formatter={(v) => formatAmount(Number(v), dec)} />
                  <Legend formatter={(v) => (v === 'billed' ? t('dash.billedShort') : t('dash.collectedShort'))} />
                  <Bar dataKey="billed" fill="#adb5bd" radius={[3, 3, 0, 0]} />
                  <Bar dataKey="collected" fill="#0f766e" radius={[3, 3, 0, 0]} />
                </BarChart>
              </ResponsiveContainer>
            </Box>
          </Paper>

          <SimpleGrid cols={{ base: 1, md: 2 }}>
            <Paper withBorder p="md">
              <Text fw={600} mb="sm">{t('dash.byCollector')}</Text>
              {collectors.length === 0 ? <EmptyState /> : (
                <Box h={Math.max(160, collectors.length * 36)} dir="ltr">
                  <ResponsiveContainer width="100%" height="100%">
                    <BarChart data={collectors} layout="vertical" margin={{ left: 8, right: 16 }}>
                      <XAxis type="number" tickFormatter={short} tick={{ fontSize: 11 }} />
                      <YAxis type="category" dataKey="name" width={110} tick={{ fontSize: 12 }} />
                      <Tooltip formatter={(v) => formatAmount(Number(v), dec)} />
                      <Bar dataKey="amount" fill="#0f766e" radius={[0, 3, 3, 0]} />
                    </BarChart>
                  </ResponsiveContainer>
                </Box>
              )}
            </Paper>
            <Paper withBorder p="md">
              <Text fw={600} mb="sm">{t('dash.byArea')}</Text>
              {areas.length === 0 ? <EmptyState title={t('customers.nothingDue')} /> : (
                <Box h={Math.max(160, areas.length * 36)} dir="ltr">
                  <ResponsiveContainer width="100%" height="100%">
                    <BarChart data={areas} layout="vertical" margin={{ left: 8, right: 16 }}>
                      <XAxis type="number" tickFormatter={short} tick={{ fontSize: 11 }} />
                      <YAxis type="category" dataKey="name" width={110} tick={{ fontSize: 12 }} />
                      <Tooltip formatter={(v) => formatAmount(Number(v), dec)} />
                      <Bar dataKey="amount" fill="#e8590c" radius={[0, 3, 3, 0]} />
                    </BarChart>
                  </ResponsiveContainer>
                </Box>
              )}
            </Paper>
          </SimpleGrid>
        </>
      )}
    </Stack>
  );
}
