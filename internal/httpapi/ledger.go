package httpapi

import (
	"context"
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
	Account  string
	Desc     string
	Currency string
	Debit    decimal.Decimal
	Credit   decimal.Decimal
	Rate     string // "" = latest official rate
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
	insert := func(account, desc, cur string, dr, cr decimal.Decimal, rate string) error {
		n++
		_, err := tx.Exec(ctx, `INSERT INTO journal_lines (entry_id, line_no, account_id, description, currency, debit, credit, rate)
			VALUES ($1, $2, $3, $4, $5, $6::numeric, $7::numeric, $8::numeric)`,
			id, n, account, desc, cur, dr.String(), cr.String(), rate)
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
		if err := insert(l.Account, l.Desc, l.Currency, l.Debit, l.Credit, rate); err != nil {
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
		if err := insert(*set.FXGain, "FX / rounding", set.Base, decimal.Zero, diff, "1"); err != nil {
			return "", err
		}
	case diff.IsNegative():
		if set.FXLoss == nil {
			return "", &apiError{status: http.StatusUnprocessableEntity, code: "billing_not_setup", detail: "fx_loss"}
		}
		if err := insert(*set.FXLoss, "FX / rounding", set.Base, diff.Neg(), decimal.Zero, "1"); err != nil {
			return "", err
		}
	}
	if _, err := tx.Exec(ctx, `SELECT post_journal_entry($1)`, id); err != nil {
		return "", err
	}
	return id, nil
}

// autoAllocate applies a customer's unallocated payments to open invoices of the same currency, oldest first.
func autoAllocate(ctx context.Context, tx pgx.Tx, customerID, currency string) error {
	type open struct {
		id     string
		amount decimal.Decimal
	}
	collect := func(sql string) ([]open, error) {
		rows, err := tx.Query(ctx, sql, customerID, currency)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, func(row pgx.CollectableRow) (open, error) {
			var o open
			var s string
			err := row.Scan(&o.id, &s)
			o.amount, _ = decimal.NewFromString(s)
			return o, err
		})
	}
	pays, err := collect(`SELECT p.id::text, (p.amount - coalesce(sum(a.amount), 0))::text
		FROM payments p LEFT JOIN payment_allocations a ON a.payment_id = p.id
		WHERE p.customer_id = $1 AND p.currency = $2 AND p.status = 'posted'
		GROUP BY p.id HAVING p.amount - coalesce(sum(a.amount), 0) > 0
		ORDER BY p.payment_date, p.number`)
	if err != nil || len(pays) == 0 {
		return err
	}
	invs, err := collect(`SELECT id::text, (total - paid)::text FROM invoices
		WHERE customer_id = $1 AND currency = $2 AND status IN ('open', 'partial')
		ORDER BY issue_date, number`)
	if err != nil {
		return err
	}
	i, j := 0, 0
	for i < len(pays) && j < len(invs) {
		amt := decimal.Min(pays[i].amount, invs[j].amount)
		if _, err := tx.Exec(ctx, `INSERT INTO payment_allocations (payment_id, invoice_id, amount) VALUES ($1, $2, $3::numeric)`,
			pays[i].id, invs[j].id, amt.String()); err != nil {
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

// arCreditLines builds the receivable credit for a customer payment of `amount` in `currency`.
//
// For a foreign-currency (non-base) payment, the part that settles open invoices (oldest first,
// the same order autoAllocate uses) is credited at each invoice's own booking rate, so the
// receivable leaves the books at exactly the base value it entered them. Only the part left
// over (an advance) uses the payment-date rate. The difference with the money actually received
// is realized exchange gain/loss, booked automatically by postJournal.
//
// Known limit: an advance later applied to a new invoice keeps its payment-date rate, so a small
// base-currency residue can stay on the receivable account for that customer (no FX adjustment
// is booked at allocation time).
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
		ORDER BY i.issue_date, i.number`, customerID, currency, ar)
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
