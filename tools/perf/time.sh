#!/usr/bin/env bash
# Time the main pages against the throwaway perf app (127.0.0.1:18080)
B=http://127.0.0.1:18080/api/v1
CID=$(cat /tmp/perf.cid)
J=/tmp/perf.jar
curl -s -c $J -b $J -H 'Content-Type: application/json' -X POST $B/auth/login -d '{"email":"perf@example.com","password":"perf-password-1"}' >/dev/null
PSQL="docker exec paxve-perf-pg psql -U postgres -d postgres -At -c"
CUST=$($PSQL "select id from customers where code='C777'")
AR=$($PSQL "select ar_account_id from companies where id='$CID'")
CASH=$($PSQL "select account_id from payment_methods where company_id='$CID' and name='Cash USD'")
INV=$($PSQL "select id from invoices where customer_id='$CUST' limit 1")
C=$B/companies/$CID
urls=(
 "$C/dashboard" "$C/onboarding"
 "$C/customers" "$C/customers?q=Customer%2049" "$C/customers?q=70012345" "$C/customers/$CUST" "$C/customers/$CUST/statement?from=2026-01-01&to=2026-11-30"
 "$C/invoices?period=2026-11" "$C/invoices" "$C/invoices?status=open" "$C/invoices/$INV" "$C/payments" "$C/payments?from=2026-11-01&to=2026-11-30"
 "$C/readings?period=2026-11" "$C/journal" "$C/journal?from=2026-11-01&to=2026-11-30"
 "$C/reports/trial-balance?from=2026-01-01&to=2026-11-30" "$C/reports/ledger/$AR?from=2026-11-01&to=2026-11-30" "$C/reports/ledger/$CASH"
 "$C/reports/profit-loss?from=2026-01-01&to=2026-11-30" "$C/reports/balance-sheet?to=2026-11-30" "$C/reports/aging" "$C/reports/collections?from=2026-11-01&to=2026-11-30"
 "$C/reports/vat?from=2026-01-01&to=2026-11-30" "$C/reports/sales?from=2026-01-01&to=2026-11-30" "$C/statements?from=2026-11-01&to=2026-11-30"
 "$C/statements?from=2026-11-01&to=2026-11-30&only_balance=1" "$C/cash/balances" "$C/billing/jobs" "$C/messages" "$C/crm/summary"
 "$C/reports/pos?from=2026-01-01&to=2026-11-30" "$C/reports/expenses?from=2026-01-01&to=2026-11-30" "$C/reports/ap-aging" "$C/reports/generators?from=2026-11-01&to=2026-11-30"
 "$B/audit" "$B/reports/consolidated?report=trial-balance&from=2026-01-01&to=2026-11-30&currency=USD" "$C/invoices/print?period=2026-11"
)
for u in "${urls[@]}"; do
  curl -s -b $J -o /tmp/perf.out -w '' "$u"   # warm
  t=$(curl -s -b $J -o /tmp/perf.out -w '%{http_code} %{time_total} %{size_download}' "$u")
  printf '%s %s\n' "$t" "${u#$B}"
done | sort -k2 -n -r | awk '{printf "%s %6.0f ms %9d B  %s\n", $1, $2*1000, $3, $4}' | sed "s#/companies/$CID##"
