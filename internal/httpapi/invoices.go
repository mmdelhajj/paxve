package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

const invoiceCols = `i.id, i.number, i.customer_id, c.code AS customer_code, c.name AS customer_name, i.subscription_id,
	i.period, i.issue_date, i.due_date, i.currency, i.total::text AS total, i.paid::text AS paid,
	(i.total - i.paid)::text AS balance, i.status, i.journal_entry_id, i.void_entry_id, i.notes, i.created_at`

type invLine struct {
	Kind        string
	Description string
	Quantity    decimal.Decimal
	UnitPrice   decimal.Decimal
	Amount      decimal.Decimal
	Meta        map[string]any
}

// issueInvoice numbers, books (Dr receivables / Cr revenue) and stores an invoice, then applies any customer credit.
func issueInvoice(ctx context.Context, tx pgx.Tx, cid string, set *billingSettings, customerID, customerName string,
	subscriptionID, period *string, issue, due, currency, revenue, notes string, lines []invLine) (string, int64, error) {
	if err := set.requireAR(); err != nil {
		return "", 0, err
	}
	total := decimal.Zero
	for _, l := range lines {
		total = total.Add(l.Amount)
	}
	if !total.IsPositive() {
		return "", 0, badRequest("zero_invoice")
	}
	var number int64
	if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'invoice')`, cid).Scan(&number); err != nil {
		return "", 0, err
	}
	memo := fmt.Sprintf("Invoice #%d · %s", number, customerName)
	if period != nil {
		memo += " · " + *period
	}
	je, err := postJournal(ctx, tx, cid, set, issue, memo, fmt.Sprintf("INV-%d", number), "invoice", []jline{
		{Account: *set.AR, Desc: customerName, Currency: currency, Debit: total},
		{Account: revenue, Desc: customerName, Currency: currency, Credit: total},
	})
	if err != nil {
		return "", 0, err
	}
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO invoices (company_id, number, customer_id, subscription_id, period, issue_date, due_date,
		currency, total, journal_entry_id, notes) VALUES ($1, $2, $3, $4, $5, $6::date, $7::date, $8, $9::numeric, $10, $11) RETURNING id::text`,
		cid, number, customerID, subscriptionID, period, issue, due, currency, total.String(), je, notes).Scan(&id); err != nil {
		return "", 0, err
	}
	for i, l := range lines {
		meta, _ := json.Marshal(l.Meta)
		if l.Meta == nil {
			meta = []byte("{}")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO invoice_lines (invoice_id, line_no, kind, description, quantity, unit_price, amount, meta)
			VALUES ($1, $2, $3, $4, $5::numeric, $6::numeric, $7::numeric, $8)`,
			id, i+1, l.Kind, l.Description, l.Quantity.String(), l.UnitPrice.String(), l.Amount.String(), meta); err != nil {
			return "", 0, err
		}
	}
	return id, number, autoAllocate(ctx, tx, customerID, currency)
}

func periodEnd(period string) string {
	t, _ := time.Parse("2006-01", period)
	return t.AddDate(0, 1, -1).Format(time.DateOnly)
}

// runBilling creates this period's invoice for every active subscription that has none yet.
func (s *Server) runBilling(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Period    string `json:"period"`
		IssueDate string `json:"issue_date"`
		DueDate   string `json:"due_date"`
		AreaID    string `json:"area_id"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.IssueDate == "" {
		in.IssueDate = today()
	}
	if in.DueDate == "" {
		t, _ := time.Parse(time.DateOnly, in.IssueDate)
		in.DueDate = t.AddDate(0, 0, 10).Format(time.DateOnly)
	}
	if !validPeriod(in.Period) || !validDate(in.IssueDate) || !validDate(in.DueDate) || in.DueDate < in.IssueDate ||
		(in.AreaID != "" && !isUUID(in.AreaID)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		if err := set.requireAR(); err != nil {
			return nil, err
		}
		type sub struct {
			ID, Customer, CustomerName, Plan, PlanName, Type, Currency, Revenue, UnitLabel string
			Fee, Price, Initial                                                            string
			Override, Current, Previous                                                   *string
		}
		rows, err := tx.Query(ctx, `
			SELECT s.id::text, s.customer_id::text, c.name, p.id::text, p.name, p.billing_type, p.currency::text,
			       p.revenue_account_id::text, p.unit_label, p.base_fee::text, p.unit_price::text, s.initial_reading::text,
			       s.fee_override::text,
			       (SELECT r.reading::text FROM meter_readings r WHERE r.subscription_id = s.id AND r.period = $2),
			       (SELECT r.reading::text FROM meter_readings r WHERE r.subscription_id = s.id AND r.period < $2 ORDER BY r.period DESC LIMIT 1)
			FROM subscriptions s
			JOIN plans p ON p.id = s.plan_id
			JOIN customers c ON c.id = s.customer_id
			WHERE s.company_id = $1 AND s.status = 'active' AND c.status = 'active' AND s.start_date <= $3::date
			  AND ($4 = '' OR c.area_id = $4::uuid)
			  AND NOT EXISTS (SELECT 1 FROM invoices i WHERE i.subscription_id = s.id AND i.period = $2 AND i.status <> 'void')
			ORDER BY c.name`, cid, in.Period, periodEnd(in.Period), in.AreaID)
		if err != nil {
			return nil, err
		}
		subs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (sub, error) {
			var x sub
			err := row.Scan(&x.ID, &x.Customer, &x.CustomerName, &x.Plan, &x.PlanName, &x.Type, &x.Currency, &x.Revenue,
				&x.UnitLabel, &x.Fee, &x.Price, &x.Initial, &x.Override, &x.Current, &x.Previous)
			return x, err
		})
		if err != nil {
			return nil, err
		}
		type skipped struct {
			Customer string `json:"customer"`
			Reason   string `json:"reason"`
		}
		created := 0
		skips := []skipped{}
		totals := map[string]decimal.Decimal{}
		decs := map[string]int32{}
		for _, x := range subs {
			if _, ok := decs[x.Currency]; !ok {
				if decs[x.Currency], err = currencyDecimals(ctx, tx, x.Currency); err != nil {
					return nil, err
				}
			}
			dec := decs[x.Currency]
			fee, _ := decimal.NewFromString(x.Fee)
			if x.Override != nil {
				fee, _ = decimal.NewFromString(*x.Override)
			}
			lines := []invLine{}
			if fee.IsPositive() {
				lines = append(lines, invLine{Kind: "fee", Description: x.PlanName + " · " + in.Period,
					Quantity: decimal.NewFromInt(1), UnitPrice: fee, Amount: fee.Round(dec),
					Meta: map[string]any{"plan": x.PlanName, "period": in.Period}})
			}
			if x.Type == "metered" {
				if x.Current == nil {
					skips = append(skips, skipped{x.CustomerName, "no_reading"})
					continue
				}
				prevS := x.Initial
				if x.Previous != nil {
					prevS = *x.Previous
				}
				prev, _ := decimal.NewFromString(prevS)
				cur, _ := decimal.NewFromString(*x.Current)
				used := cur.Sub(prev)
				if used.IsNegative() {
					skips = append(skips, skipped{x.CustomerName, "reading_below_previous"})
					continue
				}
				price, _ := decimal.NewFromString(x.Price)
				if used.IsPositive() {
					lines = append(lines, invLine{Kind: "usage",
						Description: fmt.Sprintf("%s → %s = %s %s × %s", prev.String(), cur.String(), used.String(), x.UnitLabel, price.String()),
						Quantity:    used, UnitPrice: price, Amount: used.Mul(price).Round(dec),
						Meta:        map[string]any{"previous": prev.String(), "current": cur.String(), "used": used.String(), "unit": x.UnitLabel}})
				}
			}
			sum := decimal.Zero
			for _, l := range lines {
				sum = sum.Add(l.Amount)
			}
			if !sum.IsPositive() {
				skips = append(skips, skipped{x.CustomerName, "zero_invoice"})
				continue
			}
			subID, period := x.ID, in.Period
			if _, _, err := issueInvoice(ctx, tx, cid, set, x.Customer, x.CustomerName, &subID, &period,
				in.IssueDate, in.DueDate, x.Currency, x.Revenue, "", lines); err != nil {
				return nil, err
			}
			created++
			totals[x.Currency] = totals[x.Currency].Add(sum)
		}
		tot := map[string]string{}
		for k, v := range totals {
			tot[k] = v.String()
		}
		return map[string]any{"period": in.Period, "created": created, "skipped": skips, "totals": tot}, nil
	})
}

// createInvoice: a one-off invoice (installation, repair, sale…).
func (s *Server) createInvoice(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CustomerID       string `json:"customer_id"`
		IssueDate        string `json:"issue_date"`
		DueDate          string `json:"due_date"`
		Currency         string `json:"currency"`
		RevenueAccountID string `json:"revenue_account_id"`
		Notes            string `json:"notes"`
		Lines            []struct {
			Description string `json:"description"`
			Quantity    string `json:"quantity"`
			UnitPrice   string `json:"unit_price"`
		} `json:"lines"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.IssueDate == "" {
		in.IssueDate = today()
	}
	if in.DueDate == "" {
		in.DueDate = in.IssueDate
	}
	if !validDate(in.IssueDate) || !validDate(in.DueDate) || in.DueDate < in.IssueDate || !currencyRe.MatchString(in.Currency) ||
		len(in.Lines) == 0 || len(in.Lines) > 100 || (in.RevenueAccountID != "" && !isUUID(in.RevenueAccountID)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		name, err := lockCustomer(ctx, tx, cid, in.CustomerID)
		if err != nil {
			return nil, err
		}
		rev := in.RevenueAccountID
		if rev == "" && set.Revenue != nil {
			rev = *set.Revenue
		}
		if rev == "" {
			return nil, &apiError{status: http.StatusUnprocessableEntity, code: "billing_not_setup", detail: "revenue"}
		}
		if err := requirePostable(ctx, tx, cid, rev); err != nil {
			return nil, err
		}
		dec, err := currencyDecimals(ctx, tx, in.Currency)
		if err != nil {
			return nil, err
		}
		lines := []invLine{}
		for _, l := range in.Lines {
			q, ok1 := money(l.Quantity)
			p, ok2 := money(l.UnitPrice)
			if !ok1 || !ok2 || strings.TrimSpace(l.Description) == "" {
				return nil, badRequest("bad_request")
			}
			lines = append(lines, invLine{Kind: "other", Description: strings.TrimSpace(l.Description), Quantity: q, UnitPrice: p, Amount: q.Mul(p).Round(dec)})
		}
		id, _, err := issueInvoice(ctx, tx, cid, set, in.CustomerID, name, nil, nil, in.IssueDate, in.DueDate, in.Currency, rev, in.Notes, lines)
		if err != nil {
			return nil, err
		}
		return loadInvoice(ctx, tx, cid, id)
	})
}

func (s *Server) listInvoices(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	period, status, customer, search := q.Get("period"), q.Get("status"), q.Get("customer_id"), strings.TrimSpace(q.Get("q"))
	if (period != "" && !validPeriod(period)) || (customer != "" && !isUUID(customer)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	limit, offset := queryInt(r, "limit", 200, 2000), queryInt(r, "offset", 0, 1<<30)
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		cond := `i.company_id = $1 AND ($2 = '' OR i.period = $2) AND ($4 = '' OR i.customer_id = $4::uuid)
			AND ($5 = '' OR c.name ILIKE '%' || $5 || '%' OR c.code ILIKE $5 || '%' OR i.number::text = $5)
			AND (CASE $3 WHEN '' THEN true WHEN 'unpaid' THEN i.status IN ('open', 'partial') ELSE i.status = $3 END)`
		rows, err := queryJSON(ctx, tx, `SELECT `+invoiceCols+` FROM invoices i JOIN customers c ON c.id = i.customer_id
			WHERE `+cond+` ORDER BY i.issue_date DESC, i.number DESC LIMIT $6 OFFSET $7`, r.PathValue("cid"), period, status, customer, search, limit, offset)
		if err != nil {
			return nil, err
		}
		sums, err := queryJSON(ctx, tx, `SELECT i.currency, count(*) AS count, sum(i.total)::text AS total, sum(i.paid)::text AS paid,
			sum(i.total - i.paid)::text AS balance FROM invoices i JOIN customers c ON c.id = i.customer_id
			WHERE `+cond+` AND i.status <> 'void' GROUP BY i.currency ORDER BY i.currency`, r.PathValue("cid"), period, status, customer, search)
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"rows": rows, "totals": sums}, nil
	})
}

func loadInvoice(ctx context.Context, tx pgx.Tx, cid, id string) (any, error) {
	if !isUUID(id) {
		return nil, notFound()
	}
	inv, err := queryJSONOne(ctx, tx, `SELECT `+invoiceCols+`, c.phone AS customer_phone, c.address AS customer_address,
		ar.name AS area_name, s.meter_no, co.name_en AS company_name, co.name_ar AS company_name_ar
		FROM invoices i JOIN customers c ON c.id = i.customer_id JOIN companies co ON co.id = i.company_id
		LEFT JOIN areas ar ON ar.id = c.area_id LEFT JOIN subscriptions s ON s.id = i.subscription_id
		WHERE i.id = $1 AND i.company_id = $2`, id, cid)
	if err != nil {
		return nil, err
	}
	lines, err := queryJSON(ctx, tx, `SELECT line_no, kind, description, quantity::text AS quantity, unit_price::text AS unit_price,
		amount::text AS amount, meta FROM invoice_lines WHERE invoice_id = $1 ORDER BY line_no`, id)
	if err != nil {
		return nil, err
	}
	allocs, err := queryJSON(ctx, tx, `SELECT a.payment_id, p.number AS payment_number, p.payment_date, a.amount::text AS amount, a.created_at
		FROM payment_allocations a JOIN payments p ON p.id = a.payment_id WHERE a.invoice_id = $1 ORDER BY a.created_at`, id)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"invoice": inv, "lines": lines, "payments": allocs}, nil
}

func (s *Server) getInvoice(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return loadInvoice(ctx, tx, r.PathValue("cid"), r.PathValue("id"))
	})
}

// voidInvoice reverses the invoice's journal entry. Only unpaid invoices can be voided.
func (s *Server) voidInvoice(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Reason string `json:"reason"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var number int64
		var status, paid string
		var je *string
		err := tx.QueryRow(ctx, `SELECT number, status, paid::text, journal_entry_id::text FROM invoices
			WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&number, &status, &paid, &je)
		if err == pgx.ErrNoRows {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if status == "void" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if p, _ := decimal.NewFromString(paid); !p.IsZero() {
			return nil, &apiError{status: http.StatusConflict, code: "invoice_has_payments"}
		}
		var rev *string
		if je != nil {
			var rid string
			if err := tx.QueryRow(ctx, `SELECT reverse_journal_entry($1, current_date, $2)::text`,
				*je, fmt.Sprintf("Void invoice #%d %s", number, strings.TrimSpace(in.Reason))).Scan(&rid); err != nil {
				return nil, err
			}
			rev = &rid
		}
		if _, err := tx.Exec(ctx, `UPDATE invoices SET status = 'void', void_entry_id = $2,
			notes = CASE WHEN $3 = '' THEN notes ELSE trim(notes || ' ' || $3) END WHERE id = $1`, id, rev, strings.TrimSpace(in.Reason)); err != nil {
			return nil, err
		}
		return loadInvoice(ctx, tx, cid, id)
	})
}
