export interface Money { currency: string; due?: string; credit?: string }

export interface Customer {
  id: string; code: string; name: string; phone: string; phone2: string; email: string; address: string;
  area_id: string | null; area_name: string | null; notes: string; status: 'active' | 'suspended' | 'closed';
  created_at: string; active_subscriptions: number; due: Money[]; credit: Money[];
}

export interface Plan {
  id: string; name: string; billing_type: 'fixed' | 'metered'; currency: string; base_fee: string; unit_price: string;
  unit_label: string; revenue_account_id: string; revenue_account_code: string; is_active: boolean; active_subscriptions: number;
  tax_rate_id: string | null; tax_rate_name: string | null;
}

export interface Area { id: string; name: string; collector_id: string | null; collector_name: string | null; is_active: boolean; customers: number }

export interface PaymentMethod {
  id: string; name: string; kind: 'cash' | 'wallet' | 'bank' | 'card'; currency: string; account_id: string;
  account_code: string; account_name: string; is_active: boolean; sort: number; user_id: string | null; user_name: string | null; change_rounding: string | null;
}

export interface Subscription {
  id: string; customer_id: string; plan_id: string; plan_name: string; billing_type: 'fixed' | 'metered'; currency: string;
  base_fee: string; unit_price: string; unit_label: string; meter_no: string; start_date: string;
  status: 'active' | 'suspended' | 'ended'; fee_override: string | null; initial_reading: string; notes: string;
  last_reading: string | null; last_period: string | null; box_id: string | null; box_code: string | null;
  router_id: string | null; router_name: string | null; network_username: string; service_type: 'pppoe' | 'hotspot';
  network_status: '' | 'enabled' | 'disabled'; suspended_reason: string; last_action_status: string | null; last_action_error: string | null;
}

export interface Invoice {
  id: string; number: number; customer_id: string; customer_code: string; customer_name: string; subscription_id: string | null;
  period: string | null; issue_date: string; due_date: string; currency: string; total: string; paid: string; balance: string;
  status: 'open' | 'partial' | 'paid' | 'void'; journal_entry_id: string | null; notes: string; created_at: string;
  kind?: 'invoice' | 'refund';
}

export interface InvoiceDetail {
  invoice: Invoice & {
    customer_phone: string; customer_address: string; area_name: string | null; meter_no: string | null;
    company_name: string; company_name_ar: string;
    local_currency: string | null; local_rate: string | null; local_currency_total: string | null; box_code: string | null;
    customer_email?: string; company_logo?: string; company_header?: string; company_footer?: string; company_tax_id?: string;
    sales_order_id?: string | null; order_number?: number | null;
  };
  lines: { line_no: number; kind: 'fee' | 'usage' | 'other'; description: string; quantity: string; unit_price: string; amount: string;
    meta: Record<string, string>; tax_amount?: string; tax_name?: string | null; id?: string; item_id?: string | null; credited_quantity?: string }[];
  payments: { payment_id: string; payment_number: number; payment_date: string; amount: string }[];
}

export interface Payment {
  id: string; number: number; customer_id: string; customer_code: string; customer_name: string; payment_date: string;
  currency: string; amount: string; change_currency: string | null; change_amount: string; status: 'posted' | 'void';
  journal_entry_id: string | null; notes: string; received_by: string | null; created_at: string;
  kind?: 'payment' | 'credit_note' | 'opening_credit'; invoice_id?: string | null;
}

export interface PaymentDetail {
  payment: Payment & { customer_phone: string; company_name: string; company_name_ar: string; base_currency: string };
  tenders: { method: string; currency: string; amount: string; rate: string }[];
  allocations: { invoice_id: string; invoice_number: number; period: string | null; amount: string }[];
  customer_balance: { due: Money[]; credit: Money[] };
  local?: { currency: string; rate: string; amount: string; date: string };
}

export interface TaxRate {
  id: string; name: string; rate: string; applies_to: 'sales' | 'purchases' | 'both'; account_id: string | null; account_code: string | null;
  purchase_account_id: string | null; purchase_account_code: string | null; is_default: boolean; is_active: boolean;
}

export function currentPeriod(): string {
  const d = new Date();
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`;
}

export const invoiceColor: Record<string, string> = { open: 'red', partial: 'orange', paid: 'teal', void: 'gray' };
