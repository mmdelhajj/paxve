import { useCallback, useEffect, useState } from 'react';
import { Badge, Button, Center, Paper, Select, Stack, Table, Text, TextInput, Textarea, Title } from '@mantine/core';
import { useTranslation } from 'react-i18next';
import { api } from '../../api';
import { errorText, formatAmount, formatDate } from '../../util';
import Logo from '../../components/Logo';

// Phase 6b — the customer's installable "My account" app: the portal link is remembered on the phone, and the portal
// shows consumption, tickets (report a problem, rate a fixed one) and online orders with tracking.

const KEY = 'paxve.portals';
export interface SavedPortal { token: string; company: string; name: string; at: number }

export function savedPortals(): SavedPortal[] {
  try {
    const v = JSON.parse(localStorage.getItem(KEY) || '[]') as SavedPortal[];
    return Array.isArray(v) ? v.filter((x) => x && typeof x.token === 'string') : [];
  } catch { return []; }
}

export function rememberPortal(p: Omit<SavedPortal, 'at'>) {
  try {
    const list = savedPortals().filter((x) => x.token !== p.token);
    list.unshift({ ...p, at: Date.now() });
    localStorage.setItem(KEY, JSON.stringify(list.slice(0, 10)));
  } catch { /* storage unavailable */ }
}

/** #/my — the start screen of the installed customer app: open the remembered account(s). */
export function MyAccounts() {
  const { t } = useTranslation();
  const [list, setList] = useState(savedPortals);
  useEffect(() => {
    if (list.length === 1) window.location.replace(`#/p/${list[0].token}`);
  }, [list]);
  return (
    <Center mih="100vh" p="md" bg="var(--mantine-color-gray-0)">
      <Stack maw={420} w="100%" align="stretch">
        <Center><Logo size="xl" /></Center>
        <Title order={3} ta="center">{t('pwa.my.title')}</Title>
        {list.length === 0 ? <Text c="dimmed" ta="center">{t('pwa.my.none')}</Text> : list.map((p) => (
          <Paper key={p.token} withBorder p="md" component="a" href={`#/p/${p.token}`} style={{ textDecoration: 'none', color: 'inherit' }}>
            <Text fw={700}>{p.name}</Text>
            <Text size="sm" c="dimmed">{p.company}</Text>
          </Paper>
        ))}
        {list.length > 0 && <Button variant="subtle" color="gray" size="xs" onClick={() => { try { localStorage.removeItem(KEY); } catch { /* */ } setList([]); }}>{t('pwa.my.forget')}</Button>}
      </Stack>
    </Center>
  );
}

interface More {
  consumption: { meter_no: string; period: string; reading: string; used: string | null }[] | null;
  tickets: { number: number; subject: string; status: string; category: string; created_at: string; rating: number | null; rating_token: string | null }[] | null;
  orders: { number: number; created_at: string; currency: string; total: string; status: string; token: string; order_status: string | null }[] | null;
}

const CATS = ['outage', 'no_internet', 'slow', 'no_signal', 'meter', 'billing', 'new_connection', 'move', 'disconnect', 'complaint', 'other'];
const stColor: Record<string, string> = { new: 'blue', in_progress: 'orange', waiting: 'yellow', done: 'teal', cancelled: 'gray' };

export default function PortalMore({ token }: { token: string }) {
  const { t } = useTranslation();
  const [d, setD] = useState<More | null>(null);
  const [form, setForm] = useState<{ subject: string; description: string; category: string } | null>(null);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const load = useCallback(() => { api<More>(`/portal/${token}/more`).then(setD).catch(() => setD(null)); }, [token]);
  useEffect(load, [load]);
  if (!d) return null;
  const use = d.consumption ?? [];
  const meters = [...new Set(use.map((u) => u.meter_no))];
  const send = async () => {
    if (!form || !form.subject.trim()) return;
    setBusy(true);
    try {
      const r = await api<{ number: number }>(`/portal/${token}/tickets`, { method: 'POST', body: form });
      setMsg({ ok: true, text: t('pwa.my.sent', { n: r.number }) });
      setForm(null);
      load();
    } catch (e) { setMsg({ ok: false, text: errorText(t, e) }); } finally { setBusy(false); }
  };
  return (
    <>
      {use.length > 0 && (
        <Paper withBorder>
          <Text fw={600} p="sm" pb={0}>{t('pwa.my.consumption')}</Text>
          {meters.map((m) => {
            const rows = use.filter((u) => u.meter_no === m);
            const max = Math.max(1, ...rows.map((r) => Number(r.used ?? 0)));
            return (
              <div key={m} style={{ padding: '6px 12px 12px' }}>
                {m && <Text size="xs" c="dimmed">{t('pwa.my.meter', { m })}</Text>}
                <div style={{ display: 'flex', alignItems: 'flex-end', gap: 4, height: 90, marginTop: 6 }} role="img" aria-label={t('pwa.my.consumption')}>
                  {rows.map((r) => (
                    <div key={r.period} title={`${r.period}: ${r.used ?? '—'}`} style={{ flex: 1, display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 2, minWidth: 0 }}>
                      <span style={{ fontSize: 10, color: '#5B6B7F' }} dir="ltr">{r.used != null ? Math.round(Number(r.used)) : ''}</span>
                      <span style={{ width: '100%', background: '#1D4ED8', borderRadius: '3px 3px 0 0', height: `${r.used != null ? Math.max(2, (Number(r.used) / max) * 60) : 2}px`, opacity: r.used != null ? 1 : 0.25 }} />
                      <span style={{ fontSize: 9.5, color: '#5B6B7F' }} dir="ltr">{r.period.slice(5)}</span>
                    </div>
                  ))}
                </div>
              </div>
            );
          })}
        </Paper>
      )}

      <Paper withBorder p="sm">
        <Text fw={600}>{t('pwa.my.tickets')}</Text>
        {msg && <Text size="sm" c={msg.ok ? 'teal' : 'red'} mt={4}>{msg.text}</Text>}
        {(d.tickets ?? []).length > 0 && (
          <Table verticalSpacing={6} mt={4}><Table.Tbody>{(d.tickets ?? []).map((k) => (
            <Table.Tr key={k.number}>
              <Table.Td><Text size="sm" fw={500}>#{k.number} · {k.subject}</Text><Text size="xs" c="dimmed">{formatDate(k.created_at.slice(0, 10))}</Text></Table.Td>
              <Table.Td style={{ textAlign: 'end' }}>
                <Badge variant="light" color={stColor[k.status] ?? 'gray'}>{t(`pwa.my.st.${k.status}`, { defaultValue: k.status })}</Badge>
                {k.rating_token && <Button size="compact-xs" variant="subtle" component="a" href={`#/rate/${k.rating_token}`}>{t('pwa.my.rate')}</Button>}
                {k.rating != null && <Text size="xs" c="yellow.8">{'★'.repeat(k.rating)}</Text>}
              </Table.Td>
            </Table.Tr>
          ))}</Table.Tbody></Table>
        )}
        {form ? (
          <Stack gap={6} mt="sm">
            <Select label={t('pwa.my.cat')} value={form.category} onChange={(v) => setForm({ ...form, category: v ?? 'other' })}
              data={CATS.map((c) => ({ value: c, label: t(`pwa.my.c.${c}`) }))} allowDeselect={false} />
            <TextInput label={t('pwa.my.subject')} maxLength={120} value={form.subject} onChange={(e) => setForm({ ...form, subject: e.currentTarget.value })} />
            <Textarea label={t('pwa.my.details')} autosize minRows={3} maxLength={4000} value={form.description} onChange={(e) => setForm({ ...form, description: e.currentTarget.value })} />
            <Button loading={busy} disabled={!form.subject.trim()} onClick={() => void send()}>{t('pwa.my.send')}</Button>
            <Button variant="subtle" color="gray" onClick={() => setForm(null)}>{t('common.cancel')}</Button>
          </Stack>
        ) : <Button mt="sm" variant="light" onClick={() => { setMsg(null); setForm({ subject: '', description: '', category: 'other' }); }}>{t('pwa.my.report')}</Button>}
      </Paper>

      {(d.orders ?? []).length > 0 && (
        <Paper withBorder>
          <Text fw={600} p="sm" pb={0}>{t('pwa.my.orders')}</Text>
          <Table verticalSpacing="sm"><Table.Tbody>{(d.orders ?? []).map((o) => (
            <Table.Tr key={o.number} style={{ cursor: 'pointer' }} onClick={() => { window.location.hash = `#/order/${o.token}`; }}>
              <Table.Td><Text size="sm" fw={500}>#{o.number}</Text><Text size="xs" c="dimmed">{formatDate(o.created_at.slice(0, 10))}</Text></Table.Td>
              <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(o.total, 2)} {o.currency}</Table.Td>
              <Table.Td><Badge variant="light">{t('pwa.my.track')}</Badge></Table.Td>
            </Table.Tr>
          ))}</Table.Tbody></Table>
        </Paper>
      )}
    </>
  );
}
