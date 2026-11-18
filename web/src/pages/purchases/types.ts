import type { Money } from '../../billing';

export interface Supplier {
  id: string; code: string; name: string; phone: string; email: string; address: string; tax_id: string;
  currency: string | null; notes: string; status: 'active' | 'inactive'; created_at: string; due: Money[]; credit: Money[];
}

export interface Item {
  id: string; code: string; name: string; name_i18n: Record<string, string>; kind: 'stock' | 'service' | 'consumable';
  unit: string; sale_price: string; sale_currency: string | null; inventory_account_id: string | null;
  expense_account_id: string | null; revenue_account_id: string | null; is_active: boolean;
  quantity: string; value: string; avg_cost: string;
  is_sellable?: boolean; is_purchasable?: boolean; sale_tax_rate_id?: string | null; description?: string;
  prices?: { currency: string; price: string }[];
  barcode?: string; category_id?: string | null; image?: string; pos_visible?: boolean;
}

export interface Warehouse { id: string; name: string; is_active: boolean; value: string }

export interface Bill {
  id: string; number: number; supplier_id: string; supplier_code: string; supplier_name: string; supplier_ref: string;
  bill_date: string; due_date: string; currency: string; total: string; paid: string; balance: string;
  status: 'open' | 'partial' | 'paid' | 'void'; journal_entry_id: string | null; notes: string; created_at: string;
}

export interface BillDetail {
  bill: Bill & { supplier_phone: string; company_name: string; company_name_ar: string };
  lines: { line_no: number; item_id: string | null; item_code: string | null; unit: string | null; account_code: string;
    description: string; quantity: string; unit_price: string; amount: string }[];
  payments: { payment_id: string; payment_number: number; payment_date: string; amount: string }[];
}

export interface SupplierPayment {
  id: string; number: number; supplier_id: string; supplier_name: string; payment_date: string; currency: string; amount: string;
  method_id: string | null; method: string | null; paid_currency: string; paid_amount: string; paid_rate: string; status: 'posted' | 'void';
  kind: 'payment' | 'debit_note'; bill_id: string | null; bill_number: number | null;
  notes: string; created_at: string;
}

export interface Expense {
  id: string; number: number; expense_date: string; account_id: string; account_code: string; account_name: string;
  account_name_i18n: Record<string, string>; method: string; currency: string; amount: string; description: string;
  tax_amount: string; tax_name: string | null;
  supplier_name: string | null; status: 'posted' | 'void'; created_by: string | null;
}

export interface StockDoc {
  id: string; number: number; kind: 'fuel' | 'adjust'; doc_date: string; item_code: string; item_name: string; unit: string;
  warehouse: string; quantity: string; value: string; account_code: string; account_name: string; notes: string; created_by: string | null;
  status: 'posted' | 'void';
}

export interface StockTransfer {
  id: string; number: number; transfer_date: string; item_id: string; item_code: string; item_name: string; unit: string;
  from_warehouse: string; from_name: string; to_warehouse: string; to_name: string; quantity: string; value: string;
  status: 'posted' | 'void'; notes: string; created_by: string | null;
}

export const billColor: Record<string, string> = { open: 'red', partial: 'orange', paid: 'teal', void: 'gray' };
