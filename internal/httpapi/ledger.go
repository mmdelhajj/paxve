package httpapi

import (
	"context"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Shared posting engine used by invoices, payments and later modules.

type billingSettings struct {
	Base    string
	AR      *string
	FXGain  *string
	FXLoss  *string
	Revenue *string
}

func loadBillingSettings(ctx context.Context, tx pgx.Tx, cid string) (*billingSettings, error) {
	if !isUUID(cid) {
		return nil, notFound()
	}
	s := &billingSettings{}
	err := tx.QueryRow(ctx, `SELECT base_currency::text, ar_account_id::text, fx_gain_account_id::text,
		fx_loss_account_id::text, default_revenue_account_id::text FROM companies WHERE id = $1`, cid).
		Scan(&s.Base, &s.AR, &s.FXGain, &s.FXLoss, &s.Revenue)
	if err == pgx.ErrNoRows {
		return nil, notFound()
	}
	return s, err
}

func (s *billingSettings) requireAR() error {
	if s.AR == nil {
		return &apiError{status: http.StatusUnprocessableEntity, code: "billing_not_setup"}
	}
	return nil
}

// resolveRate: units of cur per 1 base. Uses the given rate, else the latest official rate on or before date.
func resolveRate(ctx context.Context, tx pgx.Tx, cid, base, cur, date, given string) (string, error) {
	if cur == base {
		return "1", nil
	}
	if given != "" {
		return given, nil
	}
	var rate string
	err := tx.QueryRow(ctx, `SELECT rate::text FROM exchange_rates
		WHERE company_id = $1 AND currency = $2 AND kind = 'official' AND rate_date <= $3::date
		ORDER BY rate_date DESC LIMIT 1`, cid, cur, date).Scan(&rate)
	if err == pgx.ErrNoRows {
		return "", &apiError{status: http.StatusUnprocessableEntity, code: "no_rate", detail: cur}
	}
	return rate, err
}

type jline struct {
	Account    string
	Desc       string
	Currency   string
	Debit      decimal.Decimal
	Credit     decimal.Decimal
	Rate       string // "" = latest official rate
	CostCenter string // "" = the document's cost center (withCostCenter) on income / expense lines
}

type ccKey struct{}
type preferKey struct{}

// withCostCenter: journal entries posted with this context tag their income and expense lines with cc
// (the station / generator / branch the document belongs to), unless a line names its own.
func withCostCenter(ctx context.Context, cc string) context.Context {
	if cc == "" || !isUUID(cc) {
		return ctx
	}
	return context.WithValue(ctx, ccKey{}, cc)
}

func ctxCostCenter(ctx context.Context) string {
	v, _ := ctx.Value(ccKey{}).(string)
	return v
}

// withPreferredInvoice: a payment received with this context settles that invoice first (barcode collection),
// then the other open invoices oldest first.
func withPreferredInvoice(ctx context.Context, invoiceID string) context.Context {
	if !isUUID(invoiceID) {
		return ctx
	}
	return context.WithValue(ctx, preferKey{}, invoiceID)
}

func ctxPreferredInvoice(ctx context.Context) string {
	v, _ := ctx.Value(preferKey{}).(string)
	return v
}

// postJournal creates and posts a journal entry. Any base-currency difference
// caused by exchange rates or rounding is booked to the FX gain/loss account.
func postJournal(ctx context.Context, tx pgx.Tx, cid string, set *billingSettings, date, memo, ref, source string, lines []jline) (string, error) {
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO journal_entries (company_id, entry_date, memo, reference, source)
		VALUES ($1, $2::date, $3, $4, $5) RETURNING id::text`, cid, date, memo, ref, source).Scan(&id); err != nil {
		return "", err
	}
	n := 0
	defCC := ctxCostCenter(ctx)
	insert := func(account, desc, cur string, dr, cr decimal.Decimal, rate, cc string) error {
		n++
		_, err := tx.Exec(ctx, `INSERT INTO journal_lines (entry_id, line_no, account_id, description, currency, debit, credit, rate, cost_center_id)
			VALUES ($1, $2, $3, $4, $5, $6::numeric, $7::numeric, $8::numeric,
			  CASE WHEN $9::text <> '' THEN $9::uuid
			       WHEN $10::text <> '' AND (SELECT a.type FROM accounts a WHERE a.id = $3) IN ('income', 'expense') THEN $10::uuid END)`,
			id, n, account, desc, cur, dr.String(), cr.String(), rate, cc, defCC)
		return err
	}
	for _, l := range lines {
		if l.Debit.IsZero() && l.Credit.IsZero() {
			continue
		}
		rate, err := resolveRate(ctx, tx, cid, set.Base, l.Currency, date, l.Rate)
		if err != nil {
			return "", err
		}
		if err := insert(l.Account, l.Desc, l.Currency, l.Debit, l.Credit, rate, l.CostCenter); err != nil {
			return "", err
		}
	}
	var diffS string
	if err := tx.QueryRow(ctx, `SELECT (coalesce(sum(base_debit), 0) - coalesce(sum(base_credit), 0))::text
		FROM journal_lines WHERE entry_id = $1`, id).Scan(&diffS); err != nil {
		return "", err
	}
	diff, _ := decimal.NewFromString(diffS)
	switch {
	case diff.IsPositive():
		if set.FXGain == nil {
			return "", &apiError{status: http.StatusUnprocessableEntity, code: "billing_not_setup", detail: "fx_gain"}
		}
		if err := insert(*set.FXGain, "FX / rounding", set.Base, decimal.Zero, diff, "1", ""); err != nil {
			return "", err
		}
	case diff.IsNegative():
		if set.FXLoss == nil {
			return "", &apiError{status: http.StatusUnprocessableEntity, code: "billing_not_setup", detail: "fx_loss"}
		}
		if err := insert(*set.FXLoss, "FX / rounding", set.Base, diff.Neg(), decimal.Zero, "1", ""); err != nil {
			return "", err
		}
	}
	if _, err := tx.Exec(ctx, `SELECT post_journal_entry($1)`, id); err != nil {
		return "", err
	}
	return id, nil
}

// openCredit is a payment or credit note with an unapplied amount.
type openCredit struct {
	id, date, rate string // rate: receivable credit rate of the unapplied part ("" = unknown → official rate on date)
	number         int64
	amount         decimal.Decimal
}

// openDebit is an invoice (or refund) with an open balance.
type openDebit struct {
	id, date string
	number   int64
	amount   decimal.Decimal
}

func unappliedCredits(ctx context.Context, tx pgx.Tx, customerID, currency string) ([]openCredit, error) {
	rows, err := tx.Query(ctx, `SELECT p.id::text, p.payment_date::text, coalesce(trim_scale(p.ar_rate)::text, ''), p.number,
		(p.amount - coalesce((SELECT sum(a.amount) FROM payment_allocations a WHERE a.payment_id = p.id), 0))::text
		FROM payments p WHERE p.customer_id = $1 AND p.currency = $2 AND p.status = 'posted'
		  AND p.amount - coalesce((SELECT sum(a.amount) FROM payment_allocations a WHERE a.payment_id = p.id), 0) > 0
		  AND NOT EXISTS (SELECT 1 FROM so_deposits d WHERE d.payment_id = p.id AND d.released_at IS NULL) -- reserved for its order (audit P7-H2)
		ORDER BY p.payment_date, p.created_at`, customerID, currency)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (openCredit, error) {
		var o openCredit
		var s string
		err := row.Scan(&o.id, &o.date, &o.rate, &o.number, &s)
		o.amount, _ = decimal.NewFromString(s)
		return o, err
	})
}

func openDebits(ctx context.Context, tx pgx.Tx, customerID, currency string) ([]openDebit, error) {
	rows, err := tx.Query(ctx, `SELECT id::text, issue_date::text, number, (total - paid)::text FROM invoices
		WHERE customer_id = $1 AND currency = $2 AND status IN ('open', 'partial')
		ORDER BY (id::text = $3) DESC, issue_date, kind, number`, customerID, currency, ctxPreferredInvoice(ctx))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (openDebit, error) {
		var o openDebit
		var s string
		err := row.Scan(&o.id, &o.date, &o.number, &s)
		o.amount, _ = decimal.NewFromString(s)
		return o, err
	})
}

// invoiceARRate is the rate at which an invoice (or refund) was debited to receivables.
func invoiceARRate(ctx context.Context, tx pgx.Tx, invoiceID, ar string) (string, error) {
	var rate string
	err := tx.QueryRow(ctx, `SELECT coalesce((SELECT trim_scale(l.rate)::text FROM journal_lines l JOIN invoices i ON i.journal_entry_id = l.entry_id
		WHERE i.id = $1 AND l.account_id = $2 AND l.debit > 0 LIMIT 1), '')`, invoiceID, ar).Scan(&rate)
	return rate, err
}

// allocate applies amt of a credit (payment / credit note) to an invoice. For foreign-currency documents whose
// receivable entries used different rates, the base-currency difference is booked as realized exchange
// gain/loss, so the receivable account never keeps a residue for settled documents.
// skipFX: the credit was already booked at the invoice's own rate (see arCreditLines).
func allocate(ctx context.Context, tx pgx.Tx, cid string, set *billingSettings, currency string, c openCredit, d openDebit,
	amt decimal.Decimal, skipFX bool) error {
	var fx *string
	if !skipFX && set != nil && set.AR != nil && currency != set.Base {
		cRate := c.rate
		if cRate == "" {
			r, err := resolveRate(ctx, tx, cid, set.Base, currency, c.date, "")
			if err != nil {
				return err
			}
			cRate = r
		}
		iRate, err := invoiceARRate(ctx, tx, d.id, *set.AR)
		if err != nil {
			return err
		}
		if iRate != "" {
			ri, _ := decimal.NewFromString(iRate)
			rc, _ := decimal.NewFromString(cRate)
			if ri.IsPositive() && rc.IsPositive() {
				residue := amt.DivRound(ri, 4).Sub(amt.DivRound(rc, 4)) // base debit left − base credit
				if !residue.IsZero() {
					date := c.date
					if d.date > date {
						date = d.date
					}
					line := jline{Account: *set.AR, Desc: "FX on settlement", Currency: set.Base, Rate: "1"}
					if residue.IsPositive() {
						line.Credit = residue
					} else {
						line.Debit = residue.Neg()
					}
					je, err := postJournal(ctx, tx, cid, set, date, fmt.Sprintf("Exchange difference · #%d → invoice #%d", c.number, d.number),
						fmt.Sprintf("FX-%d-%d", c.number, d.number), "fx", []jline{line})
					if err != nil {
						return err
					}
					fx = &je
				}
			}
		}
	}
	_, err := tx.Exec(ctx, `INSERT INTO payment_allocations (payment_id, invoice_id, amount, fx_entry_id) VALUES ($1, $2, $3::numeric, $4)`,
		c.id, d.id, amt.String(), fx)
	return err
}

// autoAllocate applies a customer's unapplied payments and credit notes to open invoices of the same currency,
// oldest first. fresh: a payment just booked by arCreditLines (already at the invoices' rates: no FX booking).
func autoAllocate(ctx context.Context, tx pgx.Tx, cid string, set *billingSettings, customerID, currency, fresh string) error {
	pays, err := unappliedCredits(ctx, tx, customerID, currency)
	if err != nil || len(pays) == 0 {
		return err
	}
	invs, err := openDebits(ctx, tx, customerID, currency)
	if err != nil {
		return err
	}
	i, j := 0, 0
	for i < len(pays) && j < len(invs) {
		amt := decimal.Min(pays[i].amount, invs[j].amount)
		if err := allocate(ctx, tx, cid, set, currency, pays[i], invs[j], amt, pays[i].id == fresh); err != nil {
			return err
		}
		pays[i].amount = pays[i].amount.Sub(amt)
		invs[j].amount = invs[j].amount.Sub(amt)
		if pays[i].amount.IsZero() {
			i++
		}
		if invs[j].amount.IsZero() {
			j++
		}
	}
	return nil
}

// reverseAllocations un-applies a payment / credit note from its invoices and reverses the FX entries of those allocations.
func reverseAllocations(ctx context.Context, tx pgx.Tx, paymentID, memo string) error {
	rows, err := tx.Query(ctx, `SELECT a.fx_entry_id::text FROM payment_allocations a JOIN journal_entries e ON e.id = a.fx_entry_id
		WHERE a.payment_id = $1 AND e.reversed_by IS NULL AND e.status = 'posted'`, paymentID)
	if err != nil {
		return err
	}
	fxs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, fx := range fxs {
		if _, err := tx.Exec(ctx, `SELECT reverse_journal_entry($1, current_date, $2)`, fx, memo); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO payment_allocations (payment_id, invoice_id, amount)
		SELECT payment_id, invoice_id, -sum(amount) FROM payment_allocations WHERE payment_id = $1
		GROUP BY payment_id, invoice_id HAVING sum(amount) <> 0`, paymentID)
	return err
}

// arCreditLines builds the receivable credit for a customer payment of `amount` in `currency`.
//
// For a foreign-currency (non-base) payment, the part that settles open invoices (oldest first,
// the same order autoAllocate uses) is credited at each invoice's own booking rate, so the
// receivable leaves the books at exactly the base value it entered them. Only the part left
// over (an advance) uses the payment-date rate. The difference with the money actually received
// is realized exchange gain/loss, booked automatically by postJournal.
//
// The advance keeps the payment-date rate (stored as payments.ar_rate); when it is later applied to an
// invoice booked at another rate, allocate() books the exchange difference, so no residue is left.
func arCreditLines(ctx context.Context, tx pgx.Tx, ar, base, customerID, name, currency string, amount decimal.Decimal, payRate string) ([]jline, error) {
	if currency == base {
		return []jline{{Account: ar, Desc: name, Currency: currency, Credit: amount, Rate: payRate}}, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT (i.total - i.paid)::text,
		       coalesce((SELECT l.rate::text FROM journal_lines l
		                 WHERE l.entry_id = i.journal_entry_id AND l.account_id = $3 AND l.debit > 0 LIMIT 1), '')
		FROM invoices i
		WHERE i.customer_id = $1 AND i.currency = $2 AND i.status IN ('open', 'partial')
		ORDER BY (i.id::text = $4) DESC, i.issue_date, i.kind, i.number`, customerID, currency, ar, ctxPreferredInvoice(ctx))
	if err != nil {
		return nil, err
	}
	type open struct {
		balance decimal.Decimal
		rate    string
	}
	invs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (open, error) {
		var b, rt string
		err := row.Scan(&b, &rt)
		o := open{rate: rt}
		o.balance, _ = decimal.NewFromString(b)
		return o, err
	})
	if err != nil {
		return nil, err
	}
	remaining := amount
	byRate := map[string]decimal.Decimal{}
	order := []string{}
	add := func(rate string, v decimal.Decimal) {
		if _, ok := byRate[rate]; !ok {
			order = append(order, rate)
		}
		byRate[rate] = byRate[rate].Add(v)
	}
	for _, inv := range invs {
		if !remaining.IsPositive() {
			break
		}
		take := decimal.Min(remaining, inv.balance)
		rate := inv.rate
		if rate == "" {
			rate = payRate
		}
		add(rate, take)
		remaining = remaining.Sub(take)
	}
	if remaining.IsPositive() {
		add(payRate, remaining)
	}
	out := make([]jline, 0, len(order))
	for _, rate := range order {
		out = append(out, jline{Account: ar, Desc: name, Currency: currency, Credit: byRate[rate], Rate: rate})
	}
	return out, nil
}

// lockCustomer serialises money operations of one customer.
func lockCustomer(ctx context.Context, tx pgx.Tx, cid, customerID string) (string, error) {
	if !isUUID(customerID) {
		return "", badRequest("invalid_customer")
	}
	var name string
	err := tx.QueryRow(ctx, `SELECT name FROM customers WHERE id = $1 AND company_id = $2 FOR UPDATE`, customerID, cid).Scan(&name)
	if err == pgx.ErrNoRows {
		return "", badRequest("invalid_customer")
	}
	return name, err
}
