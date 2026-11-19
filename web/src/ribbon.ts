import {
  IconApi, IconArrowsExchange, IconBook2, IconBolt, IconBuilding, IconBuildingBank, IconBuildingFactory2, IconBuildingSkyscraper,
  IconCash, IconCashBanknote, IconCashRegister, IconChartBar, IconChartLine, IconChartPie, IconChecklist, IconCoins,
  IconDatabaseImport, IconDeviceMobile, IconEngine, IconFileDescription, IconFileImport, IconFileInvoice, IconFileText,
  IconGasStation, IconGauge, IconHistory, IconHourglass, IconId, IconLayoutDashboard, IconLayoutGrid, IconListDetails,
  IconMessage, IconMoneybag, IconPackage, IconPackages, IconPrinter, IconReceipt, IconReceipt2, IconReceiptTax, IconRepeat,
  IconReportMoney, IconRouter, IconScale, IconShieldLock, IconShoppingCart, IconTargetArrow, IconTopologyStar3, IconTruck,
  IconUserCircle, IconUserDollar, IconUsers, IconUsersGroup, IconWorld, IconBarcode, IconWallet, IconCalendarStats,
  IconFileDollar, IconCurrencyDollar, IconSitemap, IconCalendarCheck, IconDoorEnter, IconTimeline, IconArrowsLeftRight, IconLanguage,
} from '@tabler/icons-react';

/** Apps a company can switch on or off (companies.apps). Core apps are always on. */
export type AppId = 'gen' | 'acc' | 'col' | 'msg' | 'stk' | 'pur' | 'net' | 'sat' | 'pos' | 'hr' | 'crm';
export const CORE_APPS: AppId[] = ['gen', 'acc', 'col'];
export const ALL_APPS: AppId[] = ['gen', 'acc', 'col', 'msg', 'stk', 'pur', 'net', 'sat', 'pos', 'hr', 'crm'];

export type Icon = typeof IconCash;

export interface Tile {
  page: string;          // route: "#/<page>/<param>"
  param?: string;
  label: string;         // i18n key
  icon: Icon;
  perm?: string;         // permission needed (default reports.view)
  app?: AppId;
  platform?: boolean;    // only for platform owners
  any?: boolean;         // every signed-in user
}

export interface RibbonTab { id: string; label: string; color: string; app?: AppId; tiles: Tile[] }

// Magic's ribbon, in its own words (Arabic captions live in locales/ar.ts under mx.rib / mx.t).
export const RIBBON: RibbonTab[] = [
  {
    id: 'daily', label: 'mx.rib.daily', color: '#2f6fd6', tiles: [
      { page: 'jibaya', label: 'mx.t.jibaya', icon: IconCash, perm: 'collections.create' },
      { page: 'invoices', label: 'mx.t.monthly', icon: IconFileInvoice, perm: 'billing.view' },
      { page: 'print-bills', label: 'mx.t.printBills', icon: IconPrinter, perm: 'billing.view' },
      { page: 'jibaya', param: 'read', label: 'mx.t.meterLists', icon: IconGauge, perm: 'collections.view' },
      { page: 'subscribers', label: 'mx.t.subscribers', icon: IconUsersGroup, perm: 'customers.view' },
      { page: 'customers', label: 'mx.t.manageSubs', icon: IconUserCircle, perm: 'customers.view' },
      { page: 'voucher', param: 'receipt', label: 'mx.t.receiptVoucher', icon: IconReceipt, perm: 'collections.create' },
      { page: 'voucher', param: 'expense', label: 'mx.t.expenseVoucher', icon: IconReceiptTax, perm: 'expenses.create', app: 'pur' },
      { page: 'voucher', param: 'payment', label: 'mx.t.paymentVoucher', icon: IconCashBanknote, perm: 'purchasing.create', app: 'pur' },
      { page: 'payments', label: 'mx.t.payments', icon: IconWallet, perm: 'collections.view' },
      { page: 'cash', label: 'mx.t.boxes', icon: IconBuildingBank, perm: 'collections.view' },
      { page: 'cheques', label: 'ax.t.cheques', icon: IconFileDollar, perm: 'collections.view' },
      { page: 'exchange', label: 'ax.t.exchange', icon: IconCurrencyDollar, perm: 'collections.view' },
      { page: 'dashboard', label: 'mx.t.dashboard', icon: IconLayoutDashboard, perm: 'reports.view' },
    ],
  },
  {
    id: 'subs', label: 'mx.rib.subs', color: '#3d8f5a', app: 'gen', tiles: [
      { page: 'customers', label: 'mx.t.manageSubs', icon: IconUserCircle, perm: 'customers.view' },
      { page: 'network', label: 'mx.t.stations', icon: IconTopologyStar3, perm: 'network.view' },
      { page: 'billing-setup', label: 'mx.t.pricing', icon: IconFileDescription, perm: 'billing.view' },
      { page: 'tariffs', label: 'mx.t.tariffs', icon: IconBolt, perm: 'billing.view' },
      { page: 'readings', label: 'mx.t.readingsSheet', icon: IconGauge, perm: 'collections.view' },
      { page: 'statements', label: 'mx.t.statements', icon: IconFileText, perm: 'customers.view' },
      { page: 'messages', label: 'mx.t.messages', icon: IconMessage, perm: 'messaging.view', app: 'msg' },
    ],
  },
  {
    id: 'net', label: 'mx.rib.net', color: '#6b8e23', app: 'net', tiles: [
      { page: 'network', label: 'mx.t.routers', icon: IconRouter, perm: 'network.view' },
      { page: 'jibaya', param: 'late', label: 'mx.t.late', icon: IconHourglass, perm: 'collections.view' },
    ],
  },
  {
    id: 'col', label: 'mx.rib.col', color: '#b7791f', tiles: [
      { page: 'jibaya', param: 'col', label: 'mx.t.settle', icon: IconMoneybag, perm: 'collections.view' },
      { page: 'jibaya', param: 'scan', label: 'mx.t.barcode', icon: IconBarcode, perm: 'collections.create' },
      { page: 'jibaya', param: 'late', label: 'mx.t.late', icon: IconHourglass, perm: 'collections.view' },
      { page: 'collect', label: 'mx.t.collectorMode', icon: IconDeviceMobile, perm: 'collections.create' },
      { page: 'collections', label: 'mx.t.collectionsReport', icon: IconChartBar },
    ],
  },
  {
    id: 'acc', label: 'mx.rib.acc', color: '#7b4fb0', tiles: [
      { page: 'accounts', label: 'mx.t.chart', icon: IconBook2, perm: 'accounting.view' },
      { page: 'journal', label: 'mx.t.doubleEntry', icon: IconListDetails, perm: 'accounting.view' },
      { page: 'ledger', label: 'mx.t.ledger', icon: IconBook2 },
      { page: 'daily', label: 'mx.t.daily', icon: IconCalendarStats, perm: 'accounting.view' },
      { page: 'trial-balance', label: 'mx.t.trialBalance', icon: IconScale },
      { page: 'profit-loss', label: 'mx.t.pl', icon: IconReportMoney },
      { page: 'balance-sheet', label: 'mx.t.bs', icon: IconChartBar },
      { page: 'cash-flow', label: 'ax.t.cashFlow', icon: IconTimeline },
      { page: 'compare', label: 'ax.t.compare', icon: IconArrowsLeftRight },
      { page: 'cheques', label: 'ax.t.cheques', icon: IconFileDollar, perm: 'collections.view' },
      { page: 'exchange', label: 'ax.t.exchange', icon: IconCurrencyDollar, perm: 'collections.view' },
      { page: 'cost-centers', label: 'ax.t.costCenters', icon: IconSitemap, perm: 'accounting.view' },
      { page: 'budgets', label: 'ax.t.budgets', icon: IconTargetArrow, perm: 'accounting.view' },
      { page: 'fiscal-years', label: 'ax.t.fiscal', icon: IconCalendarCheck, perm: 'accounting.view' },
      { page: 'opening-balances', label: 'ax.t.opening', icon: IconDoorEnter, perm: 'accounting.manage' },
      { page: 'rates', label: 'mx.t.rates', icon: IconArrowsExchange, perm: 'accounting.view' },
      { page: 'recurring', label: 'mx.t.recurring', icon: IconRepeat, perm: 'accounting.view' },
      { page: 'bank-rec', label: 'mx.t.bank', icon: IconBuildingBank, perm: 'bank.edit' },
      { page: 'assets', label: 'mx.t.assets', icon: IconBuildingFactory2, perm: 'assets.view' },
      { page: 'vat-report', label: 'mx.t.vat', icon: IconReceipt2 },
      { page: 'consolidated', label: 'mx.t.consolidated', icon: IconBuildingSkyscraper },
    ],
  },
  {
    id: 'rep', label: 'mx.rib.rep', color: '#c0392b', tiles: [
      { page: 'aging', label: 'mx.t.aging', icon: IconHourglass },
      { page: 'cash-flow', label: 'ax.t.cashFlow', icon: IconTimeline },
      { page: 'compare', label: 'ax.t.compare', icon: IconArrowsLeftRight },
      { page: 'cost-centers', label: 'ax.t.ccReport', icon: IconSitemap },
      { page: 'collections', label: 'mx.t.collectionsReport', icon: IconMoneybag },
      { page: 'generator-report', label: 'mx.t.genReport', icon: IconEngine, app: 'gen' },
      { page: 'asset-register', label: 'mx.t.assetRegister', icon: IconBuildingFactory2 },
      { page: 'sales-report', label: 'mx.t.salesReport', icon: IconChartLine, app: 'crm' },
      { page: 'pos-report', label: 'mx.t.posReport', icon: IconCashRegister, app: 'pos' },
      { page: 'ap-aging', label: 'mx.t.apAging', icon: IconUserDollar, app: 'pur' },
      { page: 'expenses-report', label: 'mx.t.expensesReport', icon: IconChartPie, app: 'pur' },
      { page: 'stock-valuation', label: 'mx.t.stockValue', icon: IconPackages, app: 'stk' },
      { page: 'stock-moves', label: 'mx.t.stockMoves', icon: IconPackages, app: 'stk' },
    ],
  },
  {
    id: 'stk', label: 'mx.rib.stk', color: '#8b5a2b', app: 'stk', tiles: [
      { page: 'items', label: 'mx.t.items', icon: IconPackage, perm: 'stock.view' },
      { page: 'stock', label: 'mx.t.stock', icon: IconGasStation, perm: 'stock.view' },
      { page: 'stock-pro', label: 't2.st.m.items', icon: IconPackages, perm: 'stock.view' },
      { page: 'stock-pro', param: 'counts', label: 't2.st.m.counts', icon: IconChecklist, perm: 'stock.view' },
      { page: 'stock-pro', param: 'lots', label: 't2.st.m.lots', icon: IconBarcode, perm: 'stock.view' },
      { page: 'stock-pro', param: 'replenish', label: 't2.st.m.replenish', icon: IconHourglass, perm: 'stock.view' },
      { page: 'stock-pro', param: 'prices', label: 't2.st.m.prices', icon: IconCoins, perm: 'stock.view' },
      { page: 'stock-pro', param: 'landed', label: 't2.st.m.landed', icon: IconTruck, perm: 'purchasing.view', app: 'pur' },
    ],
  },
  {
    id: 'pur', label: 'mx.rib.pur', color: '#4b6584', app: 'pur', tiles: [
      { page: 'suppliers', label: 'mx.t.suppliers', icon: IconTruck, perm: 'suppliers.view' },
      { page: 'bills', label: 'mx.t.bills', icon: IconReceipt, perm: 'purchasing.view' },
      { page: 'supplier-payments', label: 'mx.t.supplierPayments', icon: IconCashBanknote, perm: 'purchasing.view' },
      { page: 'expenses', label: 'mx.t.expenses', icon: IconReceiptTax, perm: 'expenses.view' },
    ],
  },
  {
    id: 'crm', label: 'mx.rib.crm', color: '#0f8a8a', app: 'crm', tiles: [
      { page: 'crm', label: 'mx.t.crm', icon: IconTargetArrow, perm: 'crm.view' },
      { page: 'activities', label: 'mx.t.activities', icon: IconChecklist, perm: 'crm.view' },
      { page: 'quotes', label: 'mx.t.quotes', icon: IconFileDescription, perm: 'sales.view' },
      { page: 'orders', label: 'mx.t.orders', icon: IconShoppingCart, perm: 'sales.view' },
    ],
  },
  {
    id: 'pos', label: 'mx.rib.pos', color: '#2d3436', app: 'pos', tiles: [
      { page: 'pos', label: 'mx.t.posOpen', icon: IconCashRegister, perm: 'pos.create' },
      { page: 'pos-sessions', label: 'mx.t.posSessions', icon: IconReceipt2 },
      { page: 'pos-setup', label: 'mx.t.posSetup', icon: IconLayoutGrid, perm: 'pos.manage' },
    ],
  },
  {
    id: 'hr', label: 'mx.rib.hr', color: '#a55eea', app: 'hr', tiles: [
      { page: 'hr', param: 'people', label: 'mx.t.employees', icon: IconId, perm: 'payroll.view' },
      { page: 'hr', param: 'attendance', label: 'hr2.m.attendance', icon: IconChecklist, perm: 'payroll.view' },
      { page: 'hr', param: 'leave', label: 'hr2.m.leave', icon: IconHourglass, perm: 'payroll.view' },
      { page: 'hr', param: 'payroll', label: 'mx.t.payroll', icon: IconCoins, perm: 'payroll.view' },
      { page: 'hr', param: 'rules', label: 'hr2.m.rules', icon: IconChecklist, perm: 'payroll.view' },
      { page: 'hr', param: 'reports', label: 'hr2.m.reports', icon: IconChartBar, perm: 'payroll.view' },
      { page: 'self', label: 'hr2.m.self', icon: IconId },
    ],
  },
  {
    id: 'set', label: 'mx.rib.set', color: '#57606f', tiles: [
      { page: 'apps', label: 'mx.t.apps', icon: IconLayoutGrid, perm: 'settings.view' },
      { page: 'localization', label: 't2.t.localization', icon: IconWorld, perm: 'settings.view' },
      { page: 'translations', label: 't2.t.translations', icon: IconLanguage, perm: 'settings.view' },
      { page: 'companies', label: 'mx.t.companies', icon: IconBuilding, perm: 'settings.view' },
      { page: 'users', label: 'mx.t.users', icon: IconUsers, perm: 'users.view' },
      { page: 'approvals', label: 'mx.t.approvals', icon: IconChecklist, any: true },
      { page: 'audit', label: 'mx.t.audit', icon: IconHistory, perm: 'audit.view' },
      { page: 'import', label: 'mx.t.import', icon: IconFileImport, perm: 'import.create' },
      { page: 'magic-import', label: 'mx.t.magicImport', icon: IconDatabaseImport, perm: 'import.manage' },
      { page: 'developers', label: 'mx.t.developers', icon: IconApi, perm: 'api.view' },
      { page: 'security', label: 'mx.t.security', icon: IconShieldLock, any: true },
      { page: 'platform', label: 'mx.t.platform', icon: IconWorld, platform: true },
    ],
  },
];

/** Pages that are only reached from inside another page (detail screens), with the app they need. */
export const PAGE_APP: Record<string, AppId | undefined> = {
  supplier: 'pur', item: 'stk', statement: undefined, customer: undefined,
};

/** The app a page belongs to (from the ribbon), so a page of a switched-off app shows a notice. */
export function appOfPage(page: string): AppId | undefined {
  if (page in PAGE_APP) return PAGE_APP[page];
  for (const tab of RIBBON) {
    for (const tile of tab.tiles) {
      if (tile.page === page && !tile.param) return tile.app ?? tab.app;
    }
  }
  return undefined;
}

/** First tile for a page (its caption and icon), used for window titles. */
export function tileOf(page: string, param?: string): Tile | undefined {
  for (const tab of RIBBON) for (const tile of tab.tiles) if (tile.page === page && (tile.param ?? '') === (param ?? '')) return tile;
  for (const tab of RIBBON) for (const tile of tab.tiles) if (tile.page === page) return tile;
  return undefined;
}
