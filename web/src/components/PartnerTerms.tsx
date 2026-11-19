import { TextInput } from '@mantine/core';
import { useTranslation } from 'react-i18next';
import { api } from '../api';

/** "Days to pay" for one customer or supplier: empty = the company's payment terms. */
export function TermsInput({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const { t } = useTranslation();
  return (
    <TextInput label={t('oa.terms.days')} dir="ltr" maw={200} placeholder={t('oa.terms.companyDefault')} description={t('oa.terms.partnerHint')}
      value={value} onChange={(e) => onChange(e.currentTarget.value.replace(/\D/g, '').slice(0, 3))} />
  );
}

/** Saves the partner's own terms when they changed (needs billing.manage on the server). */
export async function saveTerms(companyId: string, kind: 'customer_id' | 'supplier_id', id: string, before: number | null | undefined, value: string) {
  const next = value.trim() === '' ? null : Math.min(365, Number(value));
  if ((before ?? null) === next) return;
  await api(`/companies/${companyId}/payment-terms`, { method: 'PUT', body: { [kind]: id, due_days: next } });
}
