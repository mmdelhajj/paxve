package httpapi

// Phase 7 audit fixes (accounting): control accounts, sales-order deposits, POS refunds of insured / trade-in sales.

import (
	"context"
	crand "crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// controlAccountsCTE: the accounts that carry a sub-ledger (customers, suppliers, stock, gift cards, loyalty points,
// insurance claims, trade-ins, tips, subcontracting). Only documents may move them. $1 = company.
const controlAccountsCTE = `ctl AS (
	SELECT unnest(ARRAY[ar_account_id, ap_account_id, inventory_account_id, gift_liability_account_id, loyalty_liability_account_id,
		insurance_account_id, tradein_account_id, tips_liability_account_id, mfg_subcontract_account_id]) AS id
	FROM companies WHERE id = $1
	UNION SELECT DISTINCT inventory_account_id FROM items WHERE company_id = $1 AND inventory_account_id IS NOT NULL)`

// rejectControlAccount refuses a control account where a plain expense / equity account is expected (opening import
// account, fleet fuel / service accounts): posting there would move a balance without its sub-ledger (audit P7-L2).
func rejectControlAccount(ctx context.Context, tx pgx.Tx, cid, account string) error {
	var code string
	err := tx.QueryRow(ctx, `WITH `+controlAccountsCTE+` SELECT a.code FROM accounts a WHERE a.id = $2::uuid AND a.id IN (SELECT id FROM ctl)`,
		cid, account).Scan(&code)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return &apiError{status: http.StatusUnprocessableEntity, code: "control_account", detail: code}
}

// lockClientRef serialises requests carrying the same offline reference (a phone that sends its queue twice at once):
// the second waits for the first to commit, then its lookup finds the first document and returns it instead of a
// 409 from the unique index (audit P7-L5).
func lockClientRef(ctx context.Context, tx pgx.Tx, cid, ref string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('client_ref:' || $1 || ':' || $2, 0))`, cid, ref)
	return err
}

// customerByPhone finds the one active customer whose phone is the same full national number (SQL phone_key: digits,
// without 00 / +961 and the trunk 0). Several matches = nobody (audit P7-M1: never guess between two people).
func customerByPhone(ctx context.Context, tx pgx.Tx, cid, phone string) (*string, error) {
	rows, err := tx.Query(ctx, `SELECT id::text FROM customers WHERE company_id = $1 AND status <> 'closed' AND phone_key($2) <> ''
		AND phone_key(phone) = phone_key($2) LIMIT 2`, cid, phone)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(ids) != 1 {
		return nil, err
	}
	return &ids[0], nil
}

// plainName keeps letters, marks and spaces of a visitor-typed name, at most n runes (no links or text to relay).
func plainName(s string, n int) string {
	out := []rune{}
	for _, r := range s {
		if len(out) >= n {
			break
		}
		if unicode.IsLetter(r) || unicode.IsMark(r) || r == ' ' {
			out = append(out, r)
		}
	}
	return strings.TrimSpace(string(out))
}

// aiReserve counts a new AI request against the limits inside the transaction that records it (audit P7-M3): a
// per-organisation lock serialises parallel requests, so the monthly plan limit can't be passed by sending many at
// once. Also: per user 10 a minute and 200 a day (every plan, pro included), and a platform-wide daily token ceiling
// (PAXVE_AI_DAILY_TOKENS, default 30 million; PAXVE_AI_OFF=1 switches the assistant off for everyone).
func (s *Server) aiReserve(ctx context.Context, tx pgx.Tx, tenant, user string) error {
	if s.ai == nil {
		return errAINotConfigured
	}
	if os.Getenv("PAXVE_AI_OFF") == "1" {
		return &apiError{status: http.StatusServiceUnavailable, code: "ai_paused"}
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('ai:' || $1, 0))`, tenant); err != nil {
		return err
	}
	pool := s.OwnerPool
	if pool == nil {
		pool = s.Pool
	}
	var month, perMin, perDay int
	var tokens int64
	var limit *int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM ai_requests WHERE tenant_id = $1::uuid AND status <> 'error' AND created_at >= date_trunc('month', now()))::int,
		(SELECT count(*) FROM ai_requests WHERE user_id = $2::uuid AND created_at > now() - interval '1 minute')::int,
		(SELECT count(*) FROM ai_requests WHERE user_id = $2::uuid AND status <> 'error' AND created_at > now() - interval '1 day')::int,
		(SELECT coalesce(sum(input_tokens + output_tokens), 0) FROM ai_requests WHERE created_at >= current_date)::bigint,
		(SELECT pp.ai_monthly_limit FROM tenants t JOIN platform_plans pp ON pp.code = t.plan WHERE t.id = $1::uuid)`, tenant, user).
		Scan(&month, &perMin, &perDay, &tokens, &limit); err != nil {
		return err
	}
	if limit != nil && month >= *limit {
		return &apiError{status: http.StatusTooManyRequests, code: "ai_limit_reached", detail: fmt.Sprintf("%d/%d", month, *limit)}
	}
	if perMin >= 10 || perDay >= 200 {
		return &apiError{status: http.StatusTooManyRequests, code: "ai_user_limit"}
	}
	ceiling := int64(30_000_000)
	if v, err := strconv.ParseInt(os.Getenv("PAXVE_AI_DAILY_TOKENS"), 10, 64); err == nil && v > 0 {
		ceiling = v
	}
	if tokens >= ceiling {
		return &apiError{status: http.StatusServiceUnavailable, code: "ai_paused"}
	}
	return nil
}

// randIntn: a uniform random number in [0, n) from the system's secure source (signing codes).
func randIntn(n int64) int64 {
	v, err := crand.Int(crand.Reader, big.NewInt(n))
	if err != nil {
		panic(err)
	}
	return v.Int64()
}

// reservedSlugs: public addresses nobody can take for themselves (the platform's own words and well-known brands of
// the market); a platform admin assigns them to their real owner (audit P7-L10).
var reservedSlugs = map[string]bool{}

func init() {
	for _, s := range strings.Fields(`paxve admin administrator api app apps www web mail shop store careers jobs support help official login
		signup register billing pay payment portal my owner field status docs blog news test demo
		alfa touch ogero omt whish bob bankaudi blom byblos fransabank creditlibanais sgbl bankmed
		spinneys carrefour fahed charcutier aoun abc citymall leb lebanon beirut
		apple samsung huawei xiaomi oppo sony lg hp dell lenovo asus acer microsoft google meta facebook whatsapp instagram tiktok amazon
		nike adidas puma zara hm mango ikea toyota mercedes bmw kia hyundai nissan
		mcdonalds kfc starbucks pizzahut dominos burgerking subway hardees roadster crepaway zaatar malak tabbouleh`) {
		reservedSlugs[s] = true
	}
}

func reservedSlug(slug string) bool {
	s := strings.ReplaceAll(strings.ToLower(slug), "-", "")
	return reservedSlugs[s] || strings.HasPrefix(s, "paxve")
}

// PUT /platform/companies/{id}/slug {slug}: a platform admin gives (or takes back) a public address, reserved ones
// included; the company that held it loses it.
func (s *Server) platformSetSlug(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Slug string `json:"slug"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	in.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	if !isUUID(id) || (in.Slug != "" && !slugRe.MatchString(in.Slug)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.ownerRun(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if in.Slug != "" {
			if _, err := tx.Exec(ctx, `UPDATE companies SET public_slug = NULL WHERE public_slug = $1 AND id <> $2`, in.Slug, id); err != nil {
				return nil, err
			}
		}
		tag, err := tx.Exec(ctx, `UPDATE companies SET public_slug = nullif($2, '') WHERE id = $1`, id, in.Slug)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return map[string]any{"company_id": id, "slug": in.Slug}, nil
	})
}

type restockKey struct{}

// withRestockDishes: a refund that puts a restaurant dish's ingredients back on the shelf (not cooked yet).
func withRestockDishes(ctx context.Context) context.Context {
	return context.WithValue(ctx, restockKey{}, true)
}

func restockDishes(ctx context.Context) bool { v, _ := ctx.Value(restockKey{}).(bool); return v }

// ======================= sales-order deposits (audit P7-H2 / L4) =======================

// applyOrderDeposits settles the order's open invoices with its reserved deposits (never other invoices of the
// customer). A deposit used up is released; what is left stays reserved for the next invoice of the order, unless the
// order is finished, when it becomes ordinary customer credit.
func applyOrderDeposits(ctx context.Context, tx pgx.Tx, cid string, set *billingSettings, order string) error {
	type dep struct {
		id  string
		c   openCredit
		cur string
	}
	rows, err := tx.Query(ctx, `SELECT d.id::text, p.id::text, p.payment_date::text, coalesce(trim_scale(p.ar_rate)::text, ''), p.number,
		p.currency::text, (p.amount - coalesce((SELECT sum(a.amount) FROM payment_allocations a WHERE a.payment_id = p.id), 0))::text
		FROM so_deposits d JOIN payments p ON p.id = d.payment_id AND p.status = 'posted'
		WHERE d.order_id = $1 AND d.company_id = $2 AND d.released_at IS NULL ORDER BY d.created_at`, order, cid)
	if err != nil {
		return err
	}
	deps, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (dep, error) {
		var d dep
		var s string
		err := row.Scan(&d.id, &d.c.id, &d.c.date, &d.c.rate, &d.c.number, &d.cur, &s)
		d.c.amount, _ = decimal.NewFromString(s)
		return d, err
	})
	if err != nil {
		return err
	}
	if len(deps) == 0 {
		return nil
	}
	var customer, status string
	if err := tx.QueryRow(ctx, `SELECT customer_id::text, status FROM sales_orders WHERE id = $1`, order).Scan(&customer, &status); err != nil {
		return err
	}
	for _, d := range deps {
		rows, err := tx.Query(ctx, `SELECT id::text, issue_date::text, number, (total - paid)::text FROM invoices
			WHERE sales_order_id = $1 AND company_id = $2 AND kind = 'invoice' AND currency = $3 AND status IN ('open', 'partial')
			ORDER BY issue_date, number`, order, cid, d.cur)
		if err != nil {
			return err
		}
		invs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (openDebit, error) {
			var o openDebit
			var s string
			err := row.Scan(&o.id, &o.date, &o.number, &s)
			o.amount, _ = decimal.NewFromString(s)
			return o, err
		})
		if err != nil {
			return err
		}
		left := d.c.amount
		for _, inv := range invs {
			if !left.IsPositive() {
				break
			}
			amt := decimal.Min(left, inv.amount)
			if !amt.IsPositive() {
				continue
			}
			if err := allocate(ctx, tx, cid, set, d.cur, d.c, inv, amt, false); err != nil {
				return err
			}
			left = left.Sub(amt)
		}
		if !left.IsPositive() {
			if _, err := tx.Exec(ctx, `UPDATE so_deposits SET released_at = now(), released_how = 'invoiced' WHERE id = $1`, d.id); err != nil {
				return err
			}
		}
	}
	if status == "done" {
		if _, err := releaseOrderDeposits(ctx, tx, cid, set, order, customer, "credit"); err != nil {
			return err
		}
	}
	return nil
}

// releaseOrderDeposits frees the order's reserved deposits (how: credit | refund); they become ordinary customer
// credit (auto-applied to the customer's open invoices when how = credit). Returns the unapplied amount per currency.
func releaseOrderDeposits(ctx context.Context, tx pgx.Tx, cid string, set *billingSettings, order, customer, how string) (map[string]decimal.Decimal, error) {
	rows, err := tx.Query(ctx, `UPDATE so_deposits d SET released_at = now(), released_how = $3
		WHERE d.order_id = $1 AND d.company_id = $2 AND d.released_at IS NULL
		RETURNING d.currency::text, (SELECT p.amount - coalesce((SELECT sum(a.amount) FROM payment_allocations a WHERE a.payment_id = p.id), 0)
		  FROM payments p WHERE p.id = d.payment_id AND p.status = 'posted')::text`, order, cid, how)
	if err != nil {
		return nil, err
	}
	out := map[string]decimal.Decimal{}
	for rows.Next() {
		var cur string
		var s *string
		if err := rows.Scan(&cur, &s); err != nil {
			rows.Close()
			return nil, err
		}
		if s != nil {
			v, _ := decimal.NewFromString(*s)
			out[cur] = out[cur].Add(v)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if how == "credit" {
		for cur := range out {
			if err := autoAllocate(ctx, tx, cid, set, customer, cur, ""); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// orderDepositOpen: the reserved (not yet released) deposits of an order, per currency.
func orderDepositOpen(ctx context.Context, tx pgx.Tx, cid, order string) (decimal.Decimal, string, error) {
	var s, cur string
	err := tx.QueryRow(ctx, `SELECT coalesce(sum(p.amount - coalesce((SELECT sum(a.amount) FROM payment_allocations a WHERE a.payment_id = p.id), 0)), 0)::text,
		coalesce(min(d.currency::text), '')
		FROM so_deposits d JOIN payments p ON p.id = d.payment_id AND p.status = 'posted'
		WHERE d.order_id = $1 AND d.company_id = $2 AND d.released_at IS NULL`, order, cid).Scan(&s, &cur)
	v, _ := decimal.NewFromString(s)
	return v, cur, err
}

// cancelOrderDeposits: an order with a deposit is cancelled only once the user says what happens to the money
// (audit P7-L4): "refund" pays it back from method, "credit" keeps it as customer credit.
func cancelOrderDeposits(ctx context.Context, tx pgx.Tx, cid, order, choice, methodID string) error {
	open, cur, err := orderDepositOpen(ctx, tx, cid, order)
	if err != nil || !open.IsPositive() {
		return err
	}
	if choice != "refund" && choice != "credit" {
		return &apiError{status: http.StatusConflict, code: "order_has_deposit", detail: open.String() + " " + cur}
	}
	if choice == "refund" && !isUUID(methodID) {
		return badRequest("method_required")
	}
	set, err := loadBillingSettings(ctx, tx, cid)
	if err != nil {
		return err
	}
	var customer string
	var number *int64
	if err := tx.QueryRow(ctx, `SELECT customer_id::text, order_number FROM sales_orders WHERE id = $1`, order).Scan(&customer, &number); err != nil {
		return err
	}
	freed, err := releaseOrderDeposits(ctx, tx, cid, set, order, customer, choice)
	if err != nil || choice != "refund" {
		return err
	}
	name, err := lockCustomer(ctx, tx, cid, customer)
	if err != nil {
		return err
	}
	n := int64(0)
	if number != nil {
		n = *number
	}
	for c, v := range freed {
		if !v.IsPositive() {
			continue
		}
		if _, _, _, err := issueRefund(ctx, tx, cid, set, customer, name, c, v, methodID, today(), fmt.Sprintf("Deposit refund · order #%d", n)); err != nil {
			return err
		}
	}
	return nil
}

// ======================= POS refunds: money back only for money paid (audit P7-H1 / M4) =======================

// posRefundSettle runs after the credit note of a POS refund. The insurer's share cancels (part of) the insurance
// claim; cash goes back only up to what the customer paid in real money on that order (less earlier refunds); the
// rest (trade-in, gift card, loyalty share) stays as customer credit.
func posRefundSettle(ctx context.Context, tx pgx.Tx, cid string, set *billingSettings, orderID, customer, currency string,
	cn *creditResult, invTotal decimal.Decimal, fullyCredited bool, methodID, date string) (keptCredit decimal.Decimal, err error) {
	name, err := lockCustomer(ctx, tx, cid, customer)
	if err != nil {
		return decimal.Zero, err
	}
	unapplied := func() (decimal.Decimal, error) {
		var s string
		err := tx.QueryRow(ctx, `SELECT (p.amount - coalesce((SELECT sum(a.amount) FROM payment_allocations a WHERE a.payment_id = p.id), 0))::text
			FROM payments p WHERE p.id = $1`, cn.id).Scan(&s)
		v, _ := decimal.NewFromString(s)
		return v, err
	}
	dec, err := currencyDecimals(ctx, tx, currency)
	if err != nil {
		return decimal.Zero, err
	}
	// 1. the insurer's share: cancel that much of the claim (Dr receivable of the customer / Cr insurance receivable,
	//    at the rate the claim was booked at)
	var claimID, status, amountS, refundedS string
	var rate *string
	var claimNo int64
	err = tx.QueryRow(ctx, `SELECT id::text, status, amount::text, refunded::text, trim_scale(rate)::text, number FROM pharma_claims
		WHERE pos_order_id = $1 AND company_id = $2 FOR UPDATE`, orderID, cid).Scan(&claimID, &status, &amountS, &refundedS, &rate, &claimNo)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return decimal.Zero, err
	}
	if err == nil && status != "cancelled" {
		if status == "paid" || status == "rejected" {
			return decimal.Zero, &apiError{status: http.StatusConflict, code: "insurance_claim_settled", detail: fmt.Sprintf("#%d %s", claimNo, status)}
		}
		amt, _ := decimal.NewFromString(amountS)
		done, _ := decimal.NewFromString(refundedS)
		open := amt.Sub(done)
		share := open
		if !fullyCredited && invTotal.IsPositive() {
			share = decimal.Min(open, amt.Mul(cn.amount).Div(invTotal).Round(dec))
		}
		u, err := unapplied()
		if err != nil {
			return decimal.Zero, err
		}
		share = decimal.Min(share, u)
		if share.IsPositive() {
			mid, err := ensureSystemMethod(ctx, tx, cid, "insurance", currency)
			if err != nil {
				return decimal.Zero, err
			}
			r := ""
			if rate != nil {
				r = *rate
			}
			if _, _, _, err := issueRefundAt(ctx, tx, cid, set, customer, name, currency, share, mid, date,
				fmt.Sprintf("Insurance claim #%d reduced · credit note #%d", claimNo, cn.number), r); err != nil {
				return decimal.Zero, err
			}
			if _, err := tx.Exec(ctx, `UPDATE pharma_claims SET refunded = refunded + $2::numeric,
				status = CASE WHEN refunded + $2::numeric >= amount THEN 'cancelled' ELSE status END,
				note = left(trim(note || ' · ' || $3), 500) WHERE id = $1`, claimID, share.String(),
				fmt.Sprintf("returned %s (CN #%d)", share.String(), cn.number)); err != nil {
				return decimal.Zero, err
			}
		}
	}
	// 2. cash: at most the real money the customer paid on the order, less what earlier refunds already paid back
	u, err := unapplied()
	if err != nil {
		return decimal.Zero, err
	}
	if !u.IsPositive() {
		return decimal.Zero, nil
	}
	if methodID == "" {
		return u, nil
	}
	var moneyS string
	if err := tx.QueryRow(ctx, `SELECT greatest(coalesce(p.amount, 0)
		- coalesce((SELECT sum(t.amount) FROM payment_tenders t JOIN payment_methods m ON m.id = t.method_id
		            WHERE t.payment_id = o.payment_id AND m.kind IN ('gift', 'loyalty', 'tradein', 'insurance')), 0)
		- coalesce((SELECT sum(r.paid) FROM pos_orders r WHERE r.refund_of = o.id AND r.kind = 'refund'), 0), 0)::text
		FROM pos_orders o LEFT JOIN payments p ON p.id = o.payment_id WHERE o.id = $1`, orderID).Scan(&moneyS); err != nil {
		return decimal.Zero, err
	}
	money, _ := decimal.NewFromString(moneyS)
	cash := decimal.Min(u, money)
	if cash.IsPositive() {
		id, paidOut, cur, err := issueRefund(ctx, tx, cid, set, customer, name, currency, cash, methodID, date, "Credit note #"+fmt.Sprint(cn.number))
		if err != nil {
			return decimal.Zero, err
		}
		cn.refundID, cn.refundPaid, cn.refundCur = id, paidOut, cur
	}
	return u.Sub(cash), nil
}
