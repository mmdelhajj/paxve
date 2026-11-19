import { useEffect, useState } from 'react';
import { Drawer, Text } from '@mantine/core';
import { useTranslation } from 'react-i18next';
import { api, type Account } from '../../api';
import { useCompany } from '../../company';
import { openScreen } from '../../nav';
import { notifyError } from '../../util';
import { useActivations } from '../../paneActive';
import type { PaymentMethod } from '../../billing';

/** The "؟" button: a plain-language explanation of the screen (what it shows, how to read it, when to use it). */
export function Help({ topic }: { topic: string }) {
  const { t, i18n } = useTranslation();
  const [open, setOpen] = useState(false);
  const rtl = i18n.dir() === 'rtl';
  const body = String(t(`ax.help.${topic}.body`));
  return (
    <>
      <button type="button" className="mx-help no-print" aria-label={t('ax.helpButton')} title={t('ax.helpButton')} onClick={() => setOpen(true)}>
        {rtl ? '؟' : '?'}
      </button>
      <Drawer opened={open} onClose={() => setOpen(false)} position={rtl ? 'left' : 'right'} size="md"
        title={<b>{t(`ax.help.${topic}.title`)}</b>}>
        {body.split('\n\n').map((p, i) => <Text key={i} size="sm" mb="sm" style={{ lineHeight: 1.75 }}>{p}</Text>)}
      </Drawer>
    </>
  );
}

export interface CostCenter {
  id: string; code: string; name: string; name_i18n: Record<string, string>; kind: string;
  station_id: string | null; station_name: string | null; generator_id: string | null; generator_name: string | null; is_active: boolean;
}

export function ccLabel(c: Pick<CostCenter, 'code' | 'name' | 'name_i18n'>, lang: string): string {
  return `${c.code} · ${c.name_i18n?.[lang] || c.name}`;
}

export function useCostCenters(): CostCenter[] {
  const { company } = useCompany();
  const [list, setList] = useState<CostCenter[]>([]);
  useEffect(() => {
    if (company) api<CostCenter[]>(`/companies/${company.id}/cost-centers`).then(setList).catch(() => setList([]));
  }, [company]);
  return list;
}

export function useAccounts(): Account[] {
  const { company } = useCompany();
  const [list, setList] = useState<Account[]>([]);
  useEffect(() => {
    if (company) api<Account[]>(`/companies/${company.id}/accounts`).then(setList).catch(() => setList([]));
  }, [company]);
  return list;
}

/** Usable money boxes (cash, wallets, banks, cards — not the hidden cheque boxes). */
export function useBoxes(): PaymentMethod[] {
  const { company } = useCompany();
  const [list, setList] = useState<PaymentMethod[]>([]);
  useEffect(() => {
    if (company) api<PaymentMethod[]>(`/companies/${company.id}/payment-methods`)
      .then((ms) => setList(ms.filter((m) => m.is_active && m.kind !== 'cheque'))).catch(() => setList([]));
  }, [company]);
  return list;
}

interface SourceDoc { kind: string; id: string; number: number | null; party_id: string | null; other_id: string | null }

/** Opens the screen of the document behind a journal entry (receipt → customer, bill → supplier, cheque → cheques…). */
export function openSourceDoc(d: SourceDoc) {
  switch (d.kind) {
    case 'invoice': case 'payment': if (d.party_id) openScreen('customer', d.party_id); break;
    case 'bill': case 'supplier_payment': if (d.party_id) openScreen('supplier', d.party_id); else openScreen('bills'); break;
    case 'expense': openScreen('expenses'); break;
    case 'transfer': openScreen('cash'); break;
    case 'stock': openScreen('stock'); break;
    case 'cheque': openScreen('cheques', d.id); break;
    case 'exchange': openScreen('exchange'); break;
    case 'payroll': openScreen('payroll'); break;
    case 'asset': openScreen('assets'); break;
    case 'advance': openScreen('employees'); break;
    case 'delivery': openScreen('orders'); break;
    case 'fiscal_year': openScreen('fiscal-years'); break;
    case 'pos_session': openScreen('pos-sessions'); break;
    default: break;
  }
}

/** "Source document" button for a journal entry. */
export function SourceButton({ entryId }: { entryId: string }) {
  const { t } = useTranslation();
  const { company } = useCompany();
  const [doc, setDoc] = useState<SourceDoc | null | undefined>(undefined);
  useEffect(() => {
    if (!company) return;
    api<{ document?: SourceDoc }>(`/companies/${company.id}/journal/${entryId}/source`)
      .then((r) => setDoc(r.document ?? null)).catch(() => setDoc(null));
  }, [company, entryId]);
  if (!doc) return null;
  return (
    <button type="button" className="mx-btn sm" onClick={() => openSourceDoc(doc)}>
      {t('ax.source')}: {t(`ax.docKinds.${doc.kind}`)}{doc.number ? ` #${doc.number}` : ''} →
    </button>
  );
}

/** Year selector values: this year and the five before. */
export function years(): number[] {
  const y = new Date().getFullYear();
  return [y + 1, y, y - 1, y - 2, y - 3, y - 4, y - 5];
}

export function monthName(m: number, lang: string): string {
  return new Intl.DateTimeFormat(lang === 'ar' ? 'ar-LB' : lang, { month: 'short', numberingSystem: 'latn' }).format(new Date(2026, m - 1, 1));
}

export function useLoad<T>(path: string | null, deps: unknown[] = []): [T | null, () => void, boolean] {
  const { t } = useTranslation();
  const [data, setData] = useState<T | null>(null);
  const [loading, setLoading] = useState(false);
  const [n, setN] = useState(0);
  const act = useActivations();
  useEffect(() => {
    if (!path) { setData(null); return; }
    setLoading(true);
    api<T>(path).then(setData).catch((e) => { notifyError(t, e); setData(null); }).finally(() => setLoading(false));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [path, n, act, ...deps]);
  return [data, () => setN((x) => x + 1), loading];
}
