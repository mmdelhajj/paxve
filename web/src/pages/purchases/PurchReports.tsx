import { useCallback, useEffect, useState } from 'react';
import { Badge, Center, Group, Loader, Paper, Select, SimpleGrid, Stack, Table, Text, TextInput } from '@mantine/core';
import { useTranslation } from 'react-i18next';
import { api } from '../../api';
import { useCompany } from '../../company';
import { accountName, formatAmount, formatDate, notifyError, today } from '../../util';
import PageHeader from '../../components/PageHeader';
import ExportMenu from '../../components/ExportMenu';

function useData<T>(path: string | null) {
  const { t } = useTranslation();
  const [d, setD] = useState<T | null>(null);
  const load = useCallback(() => {
    if (!path) return;
    setD(null);
    api<T>(path).then(setD).catch((e) => notifyError(t, e));
  }, [path, t]);
  useEffect(load, [load]);
  return d;
}

const buckets = ['current', 'd30', 'd60', 'd90', 'over90'] as const;
interface APRow { supplier_id: string; code: string; name: string; currency: string; current: string; d30: string; d60: string; d90: string; over90: string; total: string; oldest_due: string }

export function APAging() {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const [asOf, setAsOf] = useState(today());
  const d = useData<{ rows: APRow[]; totals: APRow[] }>(company ? `/companies/${company.id}/reports/ap-aging?as_of=${asOf}` : null);
  if (!company) return null;
  return (
    <Stack gap="lg" maw={1200} className="print-area">
      <PageHeader title={t('pur.apAging')} subtitle={`${company.name_en} · ${asOf}`}>
        <TextInput type="date" size="xs" label={t('reports2.asOf')} value={asOf} onChange={(e) => setAsOf(e.currentTarget.value)} />
        <ExportMenu disabled={!d?.rows.length} table={() => d && ({
          title: t('pur.apAging'), subtitle: `${company.name_en} · ${asOf}`,
          columns: [{ label: t('pur.supplier') }, { label: t('gap.currency') }, ...buckets.map((b) => ({ label: t(`reports2.buckets.${b}`), numeric: true })),
            { label: t('pur.total'), numeric: true }],
          rows: d.rows.map((r) => [`${r.name} #${r.code}`, r.currency, ...buckets.map((b) => r[b]), r.total]),
          footer: d.totals.map((x) => [t('reports.totals'), x.currency, ...buckets.map((b) => x[b]), x.total]),
        })} />
      </PageHeader>
      {!d ? <Center p="xl"><Loader size="sm" /></Center> : (
        <>
          <SimpleGrid cols={{ base: 2, sm: 5 }}>
            {d.totals.flatMap((tot) => buckets.map((b) => (
              <Paper withBorder p="sm" key={tot.currency + b}>
                <Text size="xs" c="dimmed">{t(`reports2.buckets.${b}`)}</Text>
                <Text fw={700} dir="ltr">{formatAmount(tot[b], decimalsOf(tot.currency))} {tot.currency}</Text>
              </Paper>
            )))}
          </SimpleGrid>
          <Paper withBorder>
            {d.rows.length === 0 ? <Text c="dimmed" ta="center" p="xl">{t('common.empty')}</Text> : (
              <Table.ScrollContainer minWidth={860}>
                <Table verticalSpacing="xs" highlightOnHover key={i18n.language}>
                  <Table.Thead><Table.Tr>
                    <Table.Th>{t('pur.supplier')}</Table.Th>
                    {buckets.map((b) => <Table.Th key={b} style={{ textAlign: 'end' }}>{t(`reports2.buckets.${b}`)}</Table.Th>)}
                    <Table.Th style={{ textAlign: 'end' }}>{t('pur.total')}</Table.Th>
                  </Table.Tr></Table.Thead>
                  <Table.Tbody>
                    {d.rows.map((r) => (
                      <Table.Tr key={r.supplier_id + r.currency} style={{ cursor: 'pointer' }} onClick={() => { window.location.hash = `#/supplier/${r.supplier_id}`; }}>
                        <Table.Td><Text size="sm" fw={500}>{r.name}</Text><Text size="xs" c="dimmed">#{r.code} · {formatDate(r.oldest_due)}</Text></Table.Td>
                        {buckets.map((b) => <Table.Td key={b} ff="monospace" dir="ltr" style={{ textAlign: 'end' }} c={Number(r[b]) ? undefined : 'dimmed'}>{Number(r[b]) ? formatAmount(r[b], decimalsOf(r.currency)) : '·'}</Table.Td>)}
                        <Table.Td ff="monospace" dir="ltr" fw={700} style={{ textAlign: 'end' }}>{formatAmount(r.total, decimalsOf(r.currency))} {r.currency}</Table.Td>
                      </Table.Tr>
                    ))}
                  </Table.Tbody>
                </Table>
              </Table.ScrollContainer>
            )}
          </Paper>
        </>
      )}
    </Stack>
  );
}

interface ValRow { item_id: string; code: string; name: string; name_i18n: Record<string, string>; unit: string; warehouse: string; quantity: string; avg_cost: string; value: string }

export function StockValuation() {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const d = useData<{ base_currency: string; rows: ValRow[]; total: string; ledger_total: string }>(company ? `/companies/${company.id}/reports/stock-valuation` : null);
  if (!company) return null;
  const dec = decimalsOf(company.base_currency);
  return (
    <Stack gap="lg" maw={1100} className="print-area">
      <PageHeader title={t('pur.valuation')} subtitle={`${company.name_en} · ${t('reports.amountsIn', { cur: company.base_currency })}`}>
        <ExportMenu disabled={!d?.rows.length} table={() => d && ({
          title: t('pur.valuation'), subtitle: `${company.name_en} · ${today()} · ${company.base_currency}`,
          columns: [{ label: t('pur.item') }, { label: t('pur.warehouse') }, { label: t('pur.quantity'), numeric: true }, { label: t('gap.unit') },
            { label: t('pur.avgCost'), numeric: true }, { label: t('pur.value'), numeric: true }],
          rows: d.rows.map((r) => [`${r.name_i18n?.[i18n.language] || r.name} #${r.code}`, r.warehouse, r.quantity, r.unit, r.avg_cost, r.value]),
          footer: [[t('reports.totals'), '', '', '', '', d.total]],
        })} />
      </PageHeader>
      {!d ? <Center p="xl"><Loader size="sm" /></Center> : (
        <>
          <SimpleGrid cols={{ base: 1, sm: 2 }}>
            <Paper withBorder p="md"><Text size="xs" c="dimmed">{t('pur.value')}</Text><Text size="xl" fw={700} dir="ltr">{formatAmount(d.total, dec)}</Text></Paper>
            <Paper withBorder p="md">
              <Text size="xs" c="dimmed">{t('pur.ledgerCheck')}</Text>
              <Group gap="xs"><Text size="xl" fw={700} dir="ltr">{formatAmount(d.ledger_total, dec)}</Text>
                {Number(d.total) === Number(d.ledger_total) && <Badge color="teal" variant="light">{t('pur.matches')}</Badge>}</Group>
            </Paper>
          </SimpleGrid>
          <Paper withBorder>
            {d.rows.length === 0 ? <Text c="dimmed" ta="center" p="xl">{t('common.empty')}</Text> : (
              <Table.ScrollContainer minWidth={700}>
                <Table verticalSpacing="xs" highlightOnHover key={i18n.language}>
                  <Table.Thead><Table.Tr>
                    <Table.Th>{t('pur.item')}</Table.Th><Table.Th>{t('pur.warehouse')}</Table.Th>
                    <Table.Th style={{ textAlign: 'end' }}>{t('pur.quantity')}</Table.Th><Table.Th style={{ textAlign: 'end' }}>{t('pur.avgCost')}</Table.Th>
                    <Table.Th style={{ textAlign: 'end' }}>{t('pur.value')}</Table.Th>
                  </Table.Tr></Table.Thead>
                  <Table.Tbody>
                    {d.rows.map((r, i) => (
                      <Table.Tr key={i} style={{ cursor: 'pointer' }} onClick={() => { window.location.hash = `#/item/${r.item_id}`; }}>
                        <Table.Td fw={500}>{r.name_i18n?.[i18n.language] || r.name} <Text span size="xs" c="dimmed">#{r.code}</Text></Table.Td>
                        <Table.Td c="dimmed">{r.warehouse}</Table.Td>
                        <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{Number(r.quantity)} {r.unit}</Table.Td>
                        <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(r.avg_cost, 4)}</Table.Td>
                        <Table.Td ff="monospace" dir="ltr" fw={600} style={{ textAlign: 'end' }}>{formatAmount(r.value, dec)}</Table.Td>
                      </Table.Tr>
                    ))}
                  </Table.Tbody>
                </Table>
              </Table.ScrollContainer>
            )}
          </Paper>
        </>
      )}
    </Stack>
  );
}

interface ExpRow { id: string; code: string; name: string; name_i18n: Record<string, string>; amount: string; entries: number }

export function ExpensesReport() {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const now = new Date();
  const [from, setFrom] = useState(`${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}-01`);
  const [to, setTo] = useState(today());
  const d = useData<{ rows: ExpRow[]; total: string }>(company ? `/companies/${company.id}/reports/expenses?from=${from}&to=${to}` : null);
  if (!company) return null;
  const dec = decimalsOf(company.base_currency);
  const total = Number(d?.total ?? 0);
  return (
    <Stack gap="lg" maw={900} className="print-area">
      <PageHeader title={t('pur.expensesReport')} subtitle={`${company.name_en} · ${t('reports.amountsIn', { cur: company.base_currency })} · ${from} → ${to}`}>
        <TextInput type="date" size="xs" label={t('reports.from')} value={from} onChange={(e) => setFrom(e.currentTarget.value)} />
        <TextInput type="date" size="xs" label={t('reports.to')} value={to} onChange={(e) => setTo(e.currentTarget.value)} />
        <ExportMenu disabled={!d?.rows.length} table={() => d && ({
          title: t('pur.expensesReport'), subtitle: `${company.name_en} · ${from} → ${to} · ${company.base_currency}`,
          columns: [{ label: t('reports.account') }, { label: t('pur.amount'), numeric: true }],
          rows: d.rows.map((r) => [`${r.code} · ${accountName(r)}`, r.amount]),
          footer: [[t('reports.totals'), d.total]],
        })} />
      </PageHeader>
      {!d ? <Center p="xl"><Loader size="sm" /></Center> : (
        <Paper withBorder>
          <Table verticalSpacing="xs" key={i18n.language}>
            <Table.Tbody>
              {d.rows.map((r) => (
                <Table.Tr key={r.id}>
                  <Table.Td><a href={`#/ledger/${r.id}`} style={{ color: 'inherit', textDecoration: 'none' }}><span dir="ltr" style={{ fontFamily: 'monospace', color: 'var(--mantine-color-dimmed)' }}>{r.code}</span> {accountName(r)}</a></Table.Td>
                  <Table.Td w={220}>
                    <div style={{ height: 8, borderRadius: 4, background: 'var(--mantine-color-gray-2)' }}>
                      <div style={{ height: 8, borderRadius: 4, width: `${total ? Math.max(2, (Number(r.amount) / total) * 100) : 0}%`, background: 'var(--mantine-color-teal-6)' }} />
                    </div>
                  </Table.Td>
                  <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(r.amount, dec)}</Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
            <Table.Tfoot>
              <Table.Tr><Table.Th>{t('reports.totals')}</Table.Th><Table.Th /><Table.Th ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(d.total, dec)}</Table.Th></Table.Tr>
            </Table.Tfoot>
          </Table>
        </Paper>
      )}
    </Stack>
  );
}

interface MoveRow { id: string; move_date: string; source: string; item_id: string; item_code: string; item_name: string; unit: string;
  warehouse_id: string; warehouse: string; quantity: string; unit_cost: string; value: string; balance_qty: string; balance_value: string; notes: string }
interface MoveSum { item_id: string; item_code: string; item_name: string; unit: string; warehouse: string; opening_qty: string; opening_value: string;
  in_qty: string; out_qty: string; closing_qty: string; closing_value: string }

export function StockMoves() {
  const { t, i18n } = useTranslation();
  const { company, decimalsOf } = useCompany();
  const now = new Date();
  const [from, setFrom] = useState(`${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}-01`);
  const [to, setTo] = useState(today());
  const [item, setItem] = useState<string | null>(null);
  const [items, setItems] = useState<{ id: string; name: string; kind: string }[]>([]);
  useEffect(() => {
    if (company) api<{ id: string; name: string; kind: string }[]>(`/companies/${company.id}/items`).then((x) => setItems(x.filter((i) => i.kind === 'stock'))).catch(() => {});
  }, [company]);
  const d = useData<{ rows: MoveRow[]; summary: MoveSum[] }>(company ? `/companies/${company.id}/reports/stock-moves?from=${from}&to=${to}&item_id=${item ?? ''}` : null);
  if (!company) return null;
  const dec = decimalsOf(company.base_currency);
  const src = (s: string) => t(`gap.sources.${s}`, { defaultValue: s });
  return (
    <Stack gap="lg" maw={1200} className="print-area">
      <PageHeader title={t('gap.moves')} subtitle={`${company.name_en} · ${t('reports.amountsIn', { cur: company.base_currency })} · ${from} → ${to}`}>
        <Select size="xs" label={t('pur.item')} clearable searchable w={200} data={items.map((i) => ({ value: i.id, label: i.name }))} value={item} onChange={setItem} />
        <TextInput type="date" size="xs" label={t('reports.from')} value={from} onChange={(e) => setFrom(e.currentTarget.value)} />
        <TextInput type="date" size="xs" label={t('reports.to')} value={to} onChange={(e) => setTo(e.currentTarget.value)} />
        <ExportMenu disabled={!d?.rows.length} table={() => d && ({
          title: t('gap.moves'), subtitle: `${company.name_en} · ${from} → ${to} · ${company.base_currency}`,
          columns: [{ label: t('journal.date') }, { label: t('pur.item') }, { label: t('pur.warehouse') }, { label: t('gap.movement') },
            { label: t('pur.quantity'), numeric: true }, { label: t('pur.avgCost'), numeric: true }, { label: t('pur.value'), numeric: true },
            { label: t('gap.balanceQty'), numeric: true }, { label: t('gap.balanceValue'), numeric: true }, { label: t('pur.notes') }],
          rows: d.rows.map((r) => [r.move_date, r.item_name, r.warehouse, src(r.source), r.quantity, r.unit_cost, r.value, r.balance_qty, r.balance_value, r.notes]),
        })} />
      </PageHeader>
      {!d ? <Center p="xl"><Loader size="sm" /></Center> : (
        <>
          {d.summary.length > 0 && (
            <Paper withBorder>
              <Table.ScrollContainer minWidth={800}>
                <Table verticalSpacing="xs" key={i18n.language}>
                  <Table.Thead><Table.Tr>
                    <Table.Th>{t('pur.item')}</Table.Th><Table.Th>{t('pur.warehouse')}</Table.Th>
                    <Table.Th style={{ textAlign: 'end' }}>{t('gap.opening')}</Table.Th><Table.Th style={{ textAlign: 'end' }}>{t('gap.in')}</Table.Th>
                    <Table.Th style={{ textAlign: 'end' }}>{t('gap.out')}</Table.Th><Table.Th style={{ textAlign: 'end' }}>{t('gap.closing')}</Table.Th>
                    <Table.Th style={{ textAlign: 'end' }}>{t('pur.value')}</Table.Th>
                  </Table.Tr></Table.Thead>
                  <Table.Tbody>
                    {d.summary.map((s, i) => (
                      <Table.Tr key={i}>
                        <Table.Td fw={500}>{s.item_name}</Table.Td><Table.Td c="dimmed">{s.warehouse}</Table.Td>
                        <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{Number(s.opening_qty)}</Table.Td>
                        <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }} c="teal.8">{Number(s.in_qty) ? `+${Number(s.in_qty)}` : '·'}</Table.Td>
                        <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }} c="red.7">{Number(s.out_qty) ? `−${Number(s.out_qty)}` : '·'}</Table.Td>
                        <Table.Td ff="monospace" dir="ltr" fw={600} style={{ textAlign: 'end' }}>{Number(s.closing_qty)} {s.unit}</Table.Td>
                        <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(s.closing_value, dec)}</Table.Td>
                      </Table.Tr>
                    ))}
                  </Table.Tbody>
                </Table>
              </Table.ScrollContainer>
            </Paper>
          )}
          <Paper withBorder>
            {d.rows.length === 0 ? <Text c="dimmed" ta="center" p="xl">{t('common.empty')}</Text> : (
              <Table.ScrollContainer minWidth={900}>
                <Table verticalSpacing={4} highlightOnHover key={i18n.language}>
                  <Table.Thead><Table.Tr>
                    <Table.Th>{t('journal.date')}</Table.Th><Table.Th>{t('pur.item')}</Table.Th><Table.Th>{t('gap.movement')}</Table.Th>
                    <Table.Th style={{ textAlign: 'end' }}>{t('pur.quantity')}</Table.Th><Table.Th style={{ textAlign: 'end' }}>{t('pur.value')}</Table.Th>
                    <Table.Th style={{ textAlign: 'end' }}>{t('gap.balanceQty')}</Table.Th><Table.Th style={{ textAlign: 'end' }}>{t('gap.balanceValue')}</Table.Th>
                  </Table.Tr></Table.Thead>
                  <Table.Tbody>
                    {d.rows.map((r) => (
                      <Table.Tr key={r.id}>
                        <Table.Td>{formatDate(r.move_date)}</Table.Td>
                        <Table.Td>{r.item_name}<Text size="xs" c="dimmed">{r.warehouse}</Text></Table.Td>
                        <Table.Td><Badge variant="light" size="sm" color={Number(r.quantity) > 0 ? 'teal' : 'orange'}>{src(r.source)}</Badge>
                          {r.notes && <Text size="xs" c="dimmed">{r.notes}</Text>}</Table.Td>
                        <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{Number(r.quantity) > 0 ? '+' : ''}{Number(r.quantity)} {r.unit}</Table.Td>
                        <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(r.value, dec)}</Table.Td>
                        <Table.Td ff="monospace" dir="ltr" fw={600} style={{ textAlign: 'end' }}>{Number(r.balance_qty)}</Table.Td>
                        <Table.Td ff="monospace" dir="ltr" style={{ textAlign: 'end' }}>{formatAmount(r.balance_value, dec)}</Table.Td>
                      </Table.Tr>
                    ))}
                  </Table.Tbody>
                </Table>
              </Table.ScrollContainer>
            )}
          </Paper>
        </>
      )}
    </Stack>
  );
}