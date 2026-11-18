import { test, expect } from '@playwright/test';
import { apiCall, shot, watch } from './helpers';

// Module Q in the browser: a user limited to one company never sees another company, even by typing
// its links; an approval request shows a translated action, runs in the requester's name, and the
// audit log names the approver. Signs up its own QA organisation (deactivate "QA Isolation *" afterwards).

test('company isolation and approvals', async ({ browser }) => {
  test.setTimeout(300_000);
  const stamp = Date.now().toString(36);
  const st = { email: `qa-iso-${stamp}@example.com`, password: `Qa-${stamp}-pass!` };

  // owner: a second company with a customer, approvals on voids, two users
  const owner = await browser.newContext();
  const op = await owner.newPage();
  await op.goto('/');
  await apiCall(op.request, 'POST', '/signup', { organisation: `QA Isolation ${stamp}`, company_name: 'QA First', base_currency: 'USD',
    local_currency: 'LBP', template: 'lebanon', full_name: 'QA Owner', email: st.email, password: st.password, locale: 'en' });
  const companies = await apiCall<{ id: string; name_en: string }[]>(op.request, 'GET', '/companies');
  const a = companies[0].id;
  const b = await apiCall<{ id: string }>(op.request, 'POST', '/companies', { name_en: `QA Second ${stamp}` });
  await apiCall(op.request, 'POST', `/companies/${b.id}/accounts/load-template`, { template: 'lebanon' });
  const secret = await apiCall<{ id: string }>(op.request, 'POST', `/companies/${b.id}/customers`, { name: `Hidden ${stamp}` });
  await apiCall(op.request, 'PUT', `/companies/${a}/approval-rules`, { void: true });
  const limEmail = `qa-lim-${stamp}@example.com`;
  const accEmail = `qa-acc-${stamp}@example.com`;
  const lim = await apiCall<{ id: string }>(op.request, 'POST', '/users', { email: limEmail, full_name: 'Limited QA', role: 'admin', password: `Lim-${stamp}-pass!` });
  await apiCall(op.request, 'PATCH', `/users/${lim.id}`, { company_ids: [a] });
  await apiCall(op.request, 'POST', '/users', { email: accEmail, full_name: 'Accountant QA', role: 'accountant', password: `Acc-${stamp}-pass!` });

  // the limited user: one company in the switcher, the other company's customer is "not found"
  const lc = await browser.newContext();
  const lp = await lc.newPage();
  const problems = watch(lp);
  await lp.goto('/');
  await apiCall(lp.request, 'POST', '/auth/login', { email: limEmail, password: `Lim-${stamp}-pass!` });
  const visible = await apiCall<{ id: string }[]>(lp.request, 'GET', '/companies');
  expect(visible.map((c) => c.id)).toEqual([a]);
  for (const path of [`/companies/${b.id}/customers`, `/companies/${a}/customers/${secret.id}`]) {
    const r = await lp.request.get('/api/v1' + path);
    expect(r.status(), path).toBe(404);
  }
  await lp.goto(`/#/customer/${secret.id}`);
  await lp.waitForLoadState('networkidle').catch(() => {});
  await expect(lp.getByText(`Hidden ${stamp}`)).toHaveCount(0);
  await lp.goto('/#/customers');
  await lp.waitForLoadState('networkidle').catch(() => {});
  await expect(lp.getByText(`Hidden ${stamp}`)).toHaveCount(0);

  // the accountant asks to void an invoice of company A: it waits for approval
  const ac = await browser.newContext();
  const ap = await ac.newPage();
  await ap.goto('/');
  await apiCall(ap.request, 'POST', '/auth/login', { email: accEmail, password: `Acc-${stamp}-pass!` });
  const cust = await apiCall<{ id: string }>(ap.request, 'POST', `/companies/${a}/customers`, { name: `Approval ${stamp}` });
  const inv = await apiCall<{ invoice: { id: string } }>(ap.request, 'POST', `/companies/${a}/invoices`, {
    customer_id: cust.id, currency: 'USD', lines: [{ description: 'QA', quantity: '1', unit_price: '12' }] });
  const req = await ap.request.post(`/api/v1/companies/${a}/invoices/${inv.invoice.id}/void`, {
    headers: { 'Content-Type': 'application/json' }, data: JSON.stringify({ reason: 'qa' }) });
  expect(req.status()).toBe(202);

  // the owner sees it with a translated action, in Arabic, and approves it
  await apiCall(op.request, 'PATCH', '/me', { locale: 'ar' });
  await owner.addInitScript((id) => { try { localStorage.setItem('paxve.company', id); } catch { /* */ } }, a);
  await op.goto('/#/approvals');
  await op.reload();
  await expect(op.getByText('إلغاء فاتورة').first()).toBeVisible({ timeout: 20_000 });
  await shot(op, 'approvals-ar-desktop');
  await op.getByText('إلغاء فاتورة').first().click();
  await op.getByRole('dialog').getByRole('button', { name: 'موافقة وتنفيذ' }).click();
  await expect(op.getByRole('dialog')).toHaveCount(0, { timeout: 20_000 });
  const after = await apiCall<{ invoice: { status: string } }>(op.request, 'GET', `/companies/${a}/invoices/${inv.invoice.id}`);
  expect(after.invoice.status).toBe('void');

  // audit: done by the accountant, approved by the owner
  const log = await apiCall<{ entity: string; action: string; actor: string; approved_by: string | null; after: { status?: string } }[]>(
    op.request, 'GET', `/audit?entity_id=${inv.invoice.id}`);
  const voided = log.find((e) => e.entity === 'invoices' && e.action === 'update' && e.after?.status === 'void');
  expect(voided?.actor).toBe('Accountant QA');
  expect(voided?.approved_by).toBe('QA Owner');
  await op.goto('/#/audit');
  await expect(op.getByText(/بموافقة/).first()).toBeVisible({ timeout: 20_000 });
  await shot(op, 'audit-approved-ar-desktop');
  await apiCall(op.request, 'PATCH', '/me', { locale: 'en' });
  await apiCall(op.request, 'PUT', `/companies/${a}/approval-rules`, { void: false });
  await apiCall(op.request, 'PATCH', `/companies/${b.id}`, { is_active: false });
  expect(problems.console, problems.console.join('\n')).toEqual([]);
});
