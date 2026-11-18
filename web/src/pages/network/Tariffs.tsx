import { useCallback, useEffect, useState } from 'react';
import { Alert, Badge, Button, Checkbox, Group, Modal, Paper, Select, Stack, Table, Text, TextInput, Title } from '@mantine/core';
import { IconCheck } from '@tabler/icons-react';
import { useTranslation } from 'react-i18next';
import { can, api, type Me } from '../../api';
import { useCompany } from '../../company';
import { formatAmount, formatDateTime, notifyError, notifySaved } from '../../util';
import PageHeader from '../../components/PageHeader';
import { currentPeriod, type Plan } from '../../billing';

interface Tariff {
  id: string; period: string; currency: string; price_per_kwh: string; fixed_fees: Record<string, string>; notes: string;
  applied_at: string | null; applied_by: string | null;
}

export default function Tariffs({ me }: { me: Me }) {
  const { t, i18n } = useTranslation();
  const { company, currencies, decimalsOf } = useCompany();
  const [list, setList] = useState<Tariff[]>([]);
  const [plans, setPlans] = useState<Plan[]>([]);
  const [period, setPeriod] = useState(currentPeriod());
  const [currency, setCurrency] = useState<string | null>(company?.local_currency ?? 'LBP');
  const [price, setPrice] = useState('');
  const [fees, setFees] = useState<Record<string, string>>({});
  const [notes, setNotes] = useState('');
  const [busy, setBusy] = useState(false);
  const [applying, setApplying] = useState<Tariff | null>(null);
  const canEdit = can(me, 'billing.manage');

  const load = useCallback(() => {
    if (!company) return;
    api<Tariff[]>(`/companies/${company.id}/tariffs`).then(setList).catch((e) => notifyError(t, e));
    api<Plan[]>(`/companies/${company.id}/plans`).then((p) => setPlans(p.filter((x) => x.is_active))).catch(() => {});
  }, [company, t]);
  useEffect(load, [load]);

  if (!company) return null;
  const fixedPlans = plans.filter((p) => p.currency === currency && p.billing_type === 'fixed');

  async function save(e: React.FormEvent) {
    e.preventDefault();
    if (!company) return;
    setBusy(true);
    try {
      await api(`/companies/${company.id}/tariffs`, { method: 'POST', body: { period, currency, price_per_kwh: price, fixed_fees: fees, notes } });
      notifySaved(t);
      load();
    } catch (err) {
      notifyError(t, err);
    } finally {
      setBusy(false);
    }
  }

  function editPeriod(x: Tariff) {
    setPeriod(x.period);
    setCurrency(x.currency);
    setPrice(String(Number(x.price_per_kwh)));
    setFees(Object.fromEntries(Object.entries(x.fixed_fees).map(([k, v]) => [k, String(Number(v))])));
    setNotes(x.notes);
  }

  return (
    <Stack gap="lg" maw={1100}>
      <PageHeader title={t('net.tariffs')} subtitle={t('net.tariffHint')} />
      {canEdit && (
        <Paper withBorder p="md">
          <form onSubmit={save}>
            <Stack>
              <Group align="flex-end" wrap="wrap">
                <TextInput type="month" label={t('billing.period')} value={period} onChange={(e) => setPeriod(e.currentTarget.value)} />
                <Select label={t('plans.currency')} w={120} searchable data={currencies.map((c) => c.code)} value={currency} onChange={setCurrency} allowDeselect={false} />
                <TextInput label={t('net.pricePerKwh')} dir="ltr" required w={180} value={price} onChange={(e) => setPrice(e.currentTarget.value.replace(/[^\d.]/g, ''))}
                  placeholder="48241" styles={{ input: { fontFamily: 'monospace' } }} />
                <TextInput label={t('customers.notes')} value={notes} onChange={(e) => setNotes(e.currentTarget.value)} style={{ flex: 1, minWidth: 200 }} />
              </Group>
              {fixedPlans.length > 0 && (
                <>
                  <Text size="sm" fw={500}>{t('net.fixedFees')}</Text>
                  <Group wrap="wrap">
                    {fixedPlans.map((p) => (
                      <TextInput key={p.id} label={p.name} dir="ltr" w={170} value={fees[p.id] ?? ''} placeholder={String(Number(p.base_fee))}
                        onChange={(e) => { const v = e.currentTarget.value.replace(/[^\d.]/g, ''); setFees((f) => ({ ...f, [p.id]: v })); }} />
                    ))}
                  </Group>
                </>
              )}
              <Group justify="flex-end"><Button type="submit" loading={busy} disabled={!price || !/^\d{4}-\d{2}$/.test(period)}>{t('common.save')}</Button></Group>
            </Stack>
          </form>
        </Paper>
      )}
      <Paper withBorder>
        <Title order={5} p="sm">{t('rates.history')}</Title>
        {list.length === 0 ? <Text c="dimmed" ta="center" p="lg">{t('common.empty')}</Text> : (
          <Table verticalSpacing="sm" key={i18n.language}>
            <Table.Thead><Table.Tr>
              <Table.Th>{t('billing.period')}</Table.Th><Table.Th style={{ textAlign: 'end' }}>{t('net.pricePerKwh')}</Table.Th>
              <Table.Th>{t('net.fixedFees')}</Table.Th><Table.Th>{t('net.applied')}</Table.Th><Table.Th />
            </Table.Tr></Table.Thead>
            <Table.Tbody>
              {list.map((x) => (
                <Table.Tr key={x.id}>
                  <Table.Td fw={500}>{x.period}</Table.Td>
                  <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(x.price_per_kwh, decimalsOf(x.currency))} {x.currency}</Table.Td>
                  <Table.Td>
                    <Group gap={4}>
                      {Object.entries(x.fixed_fees).map(([pid, v]) => (
                        <Badge key={pid} variant="light" style={{ textTransform: 'none' }}>{plans.find((p) => p.id === pid)?.name ?? '?'}: <span dir="ltr">{formatAmount(v, decimalsOf(x.currency))}</span></Badge>
                      ))}
                    </Group>
                  </Table.Td>
                  <Table.Td>{x.applied_at ? <Text size="sm">{formatDateTime(x.applied_at)}<Text span size="xs" c="dimmed"> · {x.applied_by}</Text></Text> : <Text c="dimmed" size="sm">—</Text>}</Table.Td>
                  <Table.Td>
                    {canEdit && (
                      <Group gap={4} justify="flex-end" wrap="nowrap">
                        <Button size="compact-xs" variant="subtle" onClick={() => editPeriod(x)}>{t('common.edit')}</Button>
                        <Button size="compact-xs" leftSection={<IconCheck size={12} />} onClick={() => setApplying(x)}>{t('net.apply')}</Button>
                      </Group>
                    )}
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        )}
      </Paper>
      {applying && <ApplyTariff tariff={applying} plans={plans} onClose={() => setApplying(null)} onDone={() => { setApplying(null); load(); }} />}
    </Stack>
  );
}

function ApplyTariff({ tariff, plans, onClose, onDone }: { tariff: Tariff; plans: Plan[]; onClose: () => void; onDone: () => void }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const eligible = plans.filter((p) => p.currency === tariff.currency && (p.billing_type === 'metered' || tariff.fixed_fees[p.id]));
  const [sel, setSel] = useState<string[]>(eligible.map((p) => p.id));
  const [busy, setBusy] = useState(false);

  async function apply() {
    if (!company) return;
    setBusy(true);
    try {
      const r = await api<{ updated: number }>(`/companies/${company.id}/tariffs/${tariff.id}/apply`, { method: 'POST', body: { plan_ids: sel } });
      notifySaved(t);
      if (r.updated >= 0) onDone();
    } catch (e) {
      notifyError(t, e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal opened onClose={onClose} title={`${t('net.apply')} · ${tariff.period}`}>
      <Stack>
        <Alert color="blue" variant="light">{t('net.applyHint')}</Alert>
        {eligible.length === 0 ? <Text c="dimmed">{t('net.noEligible', { cur: tariff.currency })}</Text> : eligible.map((p) => (
          <Checkbox key={p.id} checked={sel.includes(p.id)} onChange={(e) => { const on = e.currentTarget.checked; setSel((s) => (on ? [...s, p.id] : s.filter((x) => x !== p.id))); }}
            label={`${p.name} — ${p.billing_type === 'metered' ? `${Number(p.unit_price)} → ${Number(tariff.price_per_kwh)} / ${p.unit_label}` : `${Number(p.base_fee)} → ${Number(tariff.fixed_fees[p.id])}`}`} />
        ))}
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>{t('common.cancel')}</Button>
          <Button loading={busy} disabled={sel.length === 0} onClick={apply}>{t('net.apply')}</Button>
        </Group>
      </Stack>
    </Modal>
  );
}
