/** What an empty screen should say and offer (audit P7-F4): a short hint and one primary action, per route.
 *  `go` opens another screen, `focus` puts the cursor in this screen's own "new" form. */
export interface EmptyHint { hint: string; label?: string; go?: string; focus?: boolean }

const report: EmptyHint = { hint: 'p7.eh.report', label: 'p7.eh.a.opening', go: '#/opening-balances' };
const add: EmptyHint = { hint: 'p7.eh.add', label: 'p7.eh.a.add', focus: true };
const employeesFirst: EmptyHint = { hint: 'p7.eh.hrPeople', label: 'p7.eh.a.employees', go: '#/hr/people' };

export const EMPTY_HINTS: Record<string, EmptyHint> = {
  // reports: empty until something is recorded
  'aging': report, 'balance-sheet': report, 'cash-flow': report, 'compare': report, 'daily': report, 'profit-loss': report, 'trial-balance': report,
  'collections': { hint: 'p7.eh.collections', label: 'p7.eh.a.jibaya', go: '#/jibaya' },
  'hr/reports': employeesFirst, 'hr/attendance': employeesFirst, 'hr/leave': employeesFirst, 'hr/payroll': employeesFirst,
  'retail/report': { hint: 'p7.eh.retailReport', label: 'p7.eh.a.pos', go: '#/pos' },
  // subscriptions and collection
  'customers': add,
  'subscribers': { hint: 'p7.eh.subscribers', label: 'p7.eh.a.customers', go: '#/customers' },
  'invoices': { hint: 'p7.eh.invoices', label: 'p7.eh.a.customers', go: '#/customers' },
  'print-bills': { hint: 'p7.eh.printBills', label: 'p7.eh.a.invoices', go: '#/invoices' },
  'jibaya': { hint: 'p7.eh.jibaya', label: 'p7.eh.a.customers', go: '#/customers' },
  'jibaya/col': { hint: 'p7.eh.late' }, 'jibaya/late': { hint: 'p7.eh.late' },
  'dunning': { hint: 'p7.eh.dunning' },
  'online-pay': add,
  // stock
  'stock-pro': { hint: 'p7.eh.stock', label: 'p7.eh.a.items', go: '#/items' },
  'stock-pro/lots': { hint: 'p7.eh.lots', label: 'p7.eh.a.po', go: '#/po' },
  'stock-pro/landed': { hint: 'p7.eh.landed', label: 'p7.eh.a.bills', go: '#/bills' },
  'stock-pro/counts': add, 'stock-pro/prices': add, 'stock-pro/replenish': add,
  // screens with their own "new" form
  'budgets': add, 'cash': { hint: 'p7.eh.cash', label: 'p7.eh.a.add', focus: true }, 'cheques': add, 'claims': add, 'cost-centers': add, 'custom-fields': add,
  'deferrals': add, 'exchange': add, 'gaps/fiscal': add, 'gaps/instalments': add, 'gaps/repairs': add, 'gaps/templates': add,
  'hr/people': add, 'po': add, 'retail/gift': add, 'retail/loyalty': add, 'service': add, 'service/maintenance': add, 'supplier-prices': add,
};

/** The route key of a hash: "#/stock-pro/lots" → "stock-pro/lots" (ids after it are ignored). */
export function routeKey(hash: string): string {
  const parts = hash.replace(/^#\/?/, '').split('/').filter(Boolean);
  const two = parts.slice(0, 2).join('/');
  return two in EMPTY_HINTS ? two : parts[0] ?? '';
}

/** Put the cursor in the "new" form of the visible screen (outside the grids). */
export function focusNewForm(from: HTMLElement | null) {
  const pane = from?.closest('.mx-pane') ?? document.querySelector('.mx-pane:not([hidden])');
  if (!pane) return;
  const all = Array.from(pane.querySelectorAll<HTMLElement>('input:not([type=hidden]):not([disabled]), select:not([disabled]), textarea:not([disabled])'));
  const el = all.find((x) => !x.closest('.mx-grid') && x.offsetParent !== null);
  if (el) { el.scrollIntoView({ block: 'center' }); el.focus(); }
}
