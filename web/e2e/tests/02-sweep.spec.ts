import { test, expect } from '@playwright/test';
import { apiCall, leakedKeys, loadState, overflow, shot, translationKeys, watch } from './helpers';

// Visits every admin page in Arabic (RTL), English and French, at desktop and phone width, on a
// company filled with demo data. Fails on console errors, server errors, raw translation keys and
// horizontal overflow at phone width.

const pages = ['dashboard', 'customers', 'readings', 'invoices', 'payments', 'network', 'tariffs', 'messages', 'cash', 'billing-setup',
  'suppliers', 'bills', 'supplier-payments', 'expenses', 'items', 'stock', 'journal', 'accounts', 'rates', 'recurring', 'bank-rec',
  'employees', 'payroll', 'assets', 'profit-loss', 'balance-sheet', 'trial-balance', 'aging', 'collections', 'ap-aging',
  'stock-valuation', 'expenses-report', 'vat-report', 'generator-report', 'asset-register', 'companies', 'users', 'audit',
  'import', 'developers', 'print-bills'];
const mobilePages = ['dashboard', 'customers', 'invoices', 'payments', 'readings', 'journal', 'aging', 'expenses'];

test('every page in every language', async ({ browser }) => {
  test.setTimeout(900_000);
  const st = loadState();
  const keys = await translationKeys();
  expect(keys.size).toBeGreaterThan(500);
  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  const problems = watch(page);
  await page.goto('/');
  await apiCall(page.request, 'POST', '/auth/login', { email: st.email, password: st.password });
  const demo = await apiCall<{ company_id: string }>(page.request, 'POST', '/demo-data', {}).catch(async (e) => {
    const list = await apiCall<{ id: string; name_en: string }[]>(page.request, 'GET', '/companies');
    const d = list.find((c) => c.name_en.startsWith('Demo Company'));
    if (!d) throw e;
    return { company_id: d.id };
  });
  await ctx.addInitScript((id) => { try { localStorage.setItem('paxve.company', id); } catch { /* */ } }, demo.company_id);
  const tb = await apiCall<{ total_debit: string; total_credit: string }>(page.request, 'GET', `/companies/${demo.company_id}/reports/trial-balance`);
  expect(tb.total_debit).toBe(tb.total_credit);

  const custs = await apiCall<{ id: string }[]>(page.request, 'GET', `/companies/${demo.company_id}/customers?limit=1`);
  if (custs[0]) pages.push(`customer/${custs[0].id}`);
  const failures: string[] = [];
  for (const lang of ['ar', 'en', 'fr']) {
    await apiCall(page.request, 'PATCH', '/me', { locale: lang });
    for (const [width, list] of [[1366, pages], [400, mobilePages]] as const) {
      await page.setViewportSize({ width, height: width > 500 ? 860 : 820 });
      for (const p of list) {
        const before = problems.console.length + problems.network.length;
        await page.goto(`/#/${p}`);
        await page.reload();
        await page.waitForLoadState('networkidle').catch(() => {});
        await page.waitForTimeout(400);
        const leaks = await leakedKeys(page, keys);
        if (leaks.length) failures.push(`${lang} ${width} ${p}: raw keys ${leaks.join(', ')}`);
        if (width < 500) {
          const ov = await overflow(page);
          if (ov > 2) failures.push(`${lang} ${width} ${p}: horizontal overflow ${ov}px`);
        }
        const after = problems.console.length + problems.network.length;
        if (after > before) failures.push(`${lang} ${width} ${p}: ${[...problems.console, ...problems.network].slice(before - after).join(' | ')}`);
        if ((lang === 'ar' || lang === 'en') && ['dashboard', 'customers', 'invoices', 'aging'].includes(p)) {
          await shot(page, `${p}-${lang}-${width < 500 ? 'mobile' : 'desktop'}`);
        }
      }
    }
  }
  await apiCall(page.request, 'PATCH', '/me', { locale: 'en' });
  console.log('SWEEP FAILURES:\n' + failures.join('\n'));
  expect(failures, failures.join('\n')).toEqual([]);
});
