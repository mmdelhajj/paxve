package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Subscription proration. With companies.prorate_billing on, the monthly run bills only the days a subscription is
// active in the month (start / end dates), and a plan change or a stop in the middle of an already billed month
// charges (extra invoice) or credits (credit note on that month's bill) the difference for the days left.

// GET / PUT /companies/{cid}/billing/proration {enabled}
func (s *Server) getProration(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSONOne(ctx, tx, `SELECT prorate_billing AS enabled FROM companies WHERE id = $1`, r.PathValue("cid"))
	})
}

func (s *Server) putProration(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := tx.Exec(ctx, `UPDATE companies SET prorate_billing = $2 WHERE id = $1`, r.PathValue("cid"), in.Enabled); err != nil {
			return nil, err
		}
		return map[string]bool{"enabled": in.Enabled}, nil
	})
}

type subInfo struct {
	id, customer, customerName, plan, planName, currency, revenue, status, start string
	fee                                                                          decimal.Decimal
	tax                                                                          *string
	end                                                                          *string
}

func lockSubscription(ctx context.Context, tx pgx.Tx, cid, id string) (*subInfo, error) {
	if !isUUID(id) {
		return nil, notFound()
	}
	x := &subInfo{id: id}
	var fee string
	err := tx.QueryRow(ctx, `SELECT s.customer_id::text, c.name, p.id::text, p.name, p.currency::text, p.revenue_account_id::text, s.status,
		s.start_date::text, coalesce(s.fee_override, p.base_fee)::text, p.tax_rate_id::text, s.end_date::text
		FROM subscriptions s JOIN plans p ON p.id = s.plan_id JOIN customers c ON c.id = s.customer_id
		WHERE s.id = $1 AND s.company_id = $2 FOR UPDATE OF s`, id, cid).
		Scan(&x.customer, &x.customerName, &x.plan, &x.planName, &x.currency, &x.revenue, &x.status, &x.start, &fee, &x.tax, &x.end)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFound()
	}
	x.fee, _ = decimal.NewFromString(fee)
	return x, err
}

// daysLeft: days from date to the end of its month (date included) and the days in that month.
func daysLeft(date string) (int, int) {
	d, _ := time.Parse(time.DateOnly, date)
	last := time.Date(d.Year(), d.Month()+1, 0, 0, 0, 0, 0, time.UTC)
	return last.Day() - d.Day() + 1, last.Day()
}

// adjustPeriod charges (diff > 0, a new invoice) or credits (diff < 0, a credit note on the month's bill) a prorated
// difference for the subscription. Returns the invoice / credit note created (nil when nothing was billed yet).
func adjustPeriod(ctx context.Context, tx pgx.Tx, cid string, x *subInfo, date string, diff decimal.Decimal, label string) (*string, *string, error) {
	period := date[:7]
	var inv string
	err := tx.QueryRow(ctx, `SELECT id::text FROM invoices WHERE subscription_id = $1 AND period = $2 AND status <> 'void' ORDER BY number DESC LIMIT 1`,
		x.id, period).Scan(&inv)
	if errors.Is(err, pgx.ErrNoRows) || diff.IsZero() {
		return nil, nil, nil // month not billed yet: the run bills the right plan and days
	}
	if err != nil {
		return nil, nil, err
	}
	set, err := loadBillingSettings(ctx, tx, cid)
	if err != nil {
		return nil, nil, err
	}
	dec, err := currencyDecimals(ctx, tx, x.currency)
	if err != nil {
		return nil, nil, err
	}
	var tax *taxInfo
	if x.tax != nil {
		if tax, err = loadTaxRate(ctx, tx, cid, *x.tax, "sales"); err != nil {
			return nil, nil, err
		}
	}
	amt := diff.Abs().Round(dec)
	if !amt.IsPositive() {
		return nil, nil, nil
	}
	if diff.IsPositive() {
		ln := invLine{Kind: "fee", Description: label, Quantity: decimal.NewFromInt(1), UnitPrice: amt, Amount: amt,
			Meta: map[string]any{"proration": true, "period": period}, Tax: tax, TaxAmount: taxOn(amt, tax, dec)}
		id, _, err := issueInvoice(ctx, tx, cid, set, x.customer, x.customerName, nil, nil, date, date, x.currency, x.revenue, label, []invLine{ln})
		if err != nil {
			return nil, nil, err
		}
		return &id, nil, nil
	}
	withTax := amt.Add(taxOn(amt, tax, dec))
	var bal string
	if err := tx.QueryRow(ctx, `SELECT total::text FROM invoices WHERE id = $1`, inv).Scan(&bal); err != nil {
		return nil, nil, err
	}
	if t, _ := decimal.NewFromString(bal); withTax.GreaterThan(t) {
		withTax = t
	}
	cr, err := creditInvoice(ctx, tx, cid, set, inv, date, label, withTax.String(), nil, "")
	if err != nil {
		return nil, nil, err
	}
	return nil, &cr.id, nil
}

// POST /companies/{cid}/subscriptions/{id}/change-plan {plan_id, date}
func (s *Server) changeSubscriptionPlan(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PlanID string `json:"plan_id"`
		Date   string `json:"date"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Date == "" {
		in.Date = today()
	}
	if !isUUID(in.PlanID) || !validDate(in.Date) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		x, err := lockSubscription(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		if x.status == "ended" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if in.PlanID == x.plan {
			return nil, badRequest("same_plan")
		}
		var newName, newCur, newFee string
		err = tx.QueryRow(ctx, `SELECT name, currency::text, base_fee::text FROM plans WHERE id = $1 AND company_id = $2 AND is_active`, in.PlanID, cid).
			Scan(&newName, &newCur, &newFee)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, badRequest("invalid_plan")
		}
		if err != nil {
			return nil, err
		}
		if newCur != x.currency {
			return nil, badRequest("currency_mismatch")
		}
		nf, _ := decimal.NewFromString(newFee)
		left, inMonth := daysLeft(in.Date)
		var prorate bool
		if err := tx.QueryRow(ctx, `SELECT prorate_billing FROM companies WHERE id = $1`, cid).Scan(&prorate); err != nil {
			return nil, err
		}
		diff := decimal.Zero
		var invID, cnID *string
		if prorate {
			diff = nf.Sub(x.fee).Mul(decimal.NewFromInt(int64(left))).Div(decimal.NewFromInt(int64(inMonth)))
			label := fmt.Sprintf("%s → %s · %s (%d/%d)", x.planName, newName, in.Date, left, inMonth)
			if invID, cnID, err = adjustPeriod(ctx, tx, cid, x, in.Date, diff, label); err != nil {
				return nil, err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE subscriptions SET plan_id = $2, fee_override = NULL, updated_at = now() WHERE id = $1`, id, in.PlanID); err != nil {
			return nil, err
		}
		return recordSubChange(ctx, tx, cid, id, in.Date, "plan", &x.plan, &in.PlanID, x.fee, nf, left, inMonth, diff, x.currency, invID, cnID)
	})
}

// POST /companies/{cid}/subscriptions/{id}/stop {date}: the subscription ends on date (included). The run bills the
// last month up to that day; a month already billed in full gets the unused days back.
func (s *Server) stopSubscription(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Date string `json:"date"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Date == "" {
		in.Date = today()
	}
	if !validDate(in.Date) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		x, err := lockSubscription(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		if x.status == "ended" || in.Date < x.start {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		left, inMonth := daysLeft(in.Date)
		left-- // the stop day itself is still used
		var prorate bool
		if err := tx.QueryRow(ctx, `SELECT prorate_billing FROM companies WHERE id = $1`, cid).Scan(&prorate); err != nil {
			return nil, err
		}
		diff := decimal.Zero
		var cnID *string
		if prorate && left > 0 {
			diff = x.fee.Neg().Mul(decimal.NewFromInt(int64(left))).Div(decimal.NewFromInt(int64(inMonth)))
			label := fmt.Sprintf("%s · stop %s (%d/%d)", x.planName, in.Date, left, inMonth)
			if _, cnID, err = adjustPeriod(ctx, tx, cid, x, in.Date, diff, label); err != nil {
				return nil, err
			}
		}
		status := "active"
		var billedAfter bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM invoices WHERE subscription_id = $1 AND period >= $2 AND status <> 'void')`,
			id, in.Date[:7]).Scan(&billedAfter); err != nil {
			return nil, err
		}
		if billedAfter || in.Date < today()[:7]+"-01" {
			status = "ended" // its last month is already billed: nothing left for the run
		}
		if _, err := tx.Exec(ctx, `UPDATE subscriptions SET end_date = $2::date, status = $3, updated_at = now() WHERE id = $1`, id, in.Date, status); err != nil {
			return nil, err
		}
		return recordSubChange(ctx, tx, cid, id, in.Date, "stop", &x.plan, nil, x.fee, decimal.Zero, left, inMonth, diff, x.currency, nil, cnID)
	})
}

func recordSubChange(ctx context.Context, tx pgx.Tx, cid, sub, date, kind string, oldPlan, newPlan *string, oldFee, newFee decimal.Decimal,
	left, inMonth int, diff decimal.Decimal, cur string, inv, cn *string) (any, error) {
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO subscription_changes (company_id, subscription_id, change_date, kind, old_plan_id, new_plan_id, old_fee,
		new_fee, days_left, days_in_month, amount, currency, invoice_id, credit_note_id)
		VALUES ($1, $2, $3::date, $4, $5, $6, $7::numeric, $8::numeric, $9, $10, $11::numeric, $12, $13, $14) RETURNING id::text`,
		cid, sub, date, kind, oldPlan, newPlan, oldFee.String(), newFee.String(), left, inMonth, diff.Round(4).String(), cur, inv, cn).Scan(&id); err != nil {
		return nil, err
	}
	return queryJSONOne(ctx, tx, `SELECT `+subChangeCols+` FROM subscription_changes x WHERE x.id = $1`, id)
}

const subChangeCols = `x.id, x.subscription_id, x.change_date, x.kind, x.old_plan_id, (SELECT name FROM plans WHERE id = x.old_plan_id) AS old_plan,
	x.new_plan_id, (SELECT name FROM plans WHERE id = x.new_plan_id) AS new_plan, x.old_fee::text AS old_fee, x.new_fee::text AS new_fee,
	x.days_left, x.days_in_month, x.amount::text AS amount, x.currency, x.invoice_id, (SELECT number FROM invoices WHERE id = x.invoice_id) AS invoice_number,
	x.credit_note_id, (SELECT number FROM payments WHERE id = x.credit_note_id) AS credit_note_number, x.created_at`

// GET /companies/{cid}/subscriptions/{id}/changes
func (s *Server) listSubscriptionChanges(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var mine bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM subscriptions WHERE id = $1 AND company_id = $2)`, id, r.PathValue("cid")).Scan(&mine); err != nil || !mine {
			return nil, notFound()
		}
		return queryJSON(ctx, tx, `SELECT `+subChangeCols+` FROM subscription_changes x WHERE x.subscription_id = $1 AND x.company_id = $2
			ORDER BY x.created_at DESC`, id, r.PathValue("cid"))
	})
}
