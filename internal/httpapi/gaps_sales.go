package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// ======================= fiscal positions =======================
// A fiscal position maps taxes and accounts for some customers / suppliers: an exempt NGO (VAT → none), an
// export customer (VAT 11 % → export 0 %, revenue → export revenue), a foreign supplier under reverse charge…
// tax_map: [{"from": "<tax id>" | "*", "to": "<tax id>" | ""}]; account_map: [{"from": "<account id>", "to": "<account id>"}].

type fpMap struct{ From, To string }

type fiscalPos struct {
	taxes    map[string]string
	accounts map[string]string
}

// tax maps a tax rate id ("" stays "": a line without tax is not given one).
func (f *fiscalPos) tax(id string) string {
	if f == nil || id == "" {
		return id
	}
	if to, ok := f.taxes[id]; ok {
		return to
	}
	if to, ok := f.taxes["*"]; ok {
		return to
	}
	return id
}

func (f *fiscalPos) account(id string) string {
	if f == nil || id == "" {
		return id
	}
	if to, ok := f.accounts[id]; ok && to != "" {
		return to
	}
	return id
}

// partnerFiscalPosition loads the active fiscal position of a customer or supplier (nil = none).
func partnerFiscalPosition(ctx context.Context, tx pgx.Tx, cid, table, id string) (*fiscalPos, error) {
	if table != "customers" && table != "suppliers" {
		return nil, errors.New("bad partner table")
	}
	var tm, am []byte
	err := tx.QueryRow(ctx, `SELECT f.tax_map, f.account_map FROM `+table+` p JOIN fiscal_positions f ON f.id = p.fiscal_position_id
		WHERE p.id = $1 AND p.company_id = $2 AND f.is_active`, id, cid).Scan(&tm, &am)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	f := &fiscalPos{taxes: map[string]string{}, accounts: map[string]string{}}
	var t, a []fpMap
	_ = json.Unmarshal(tm, &t)
	_ = json.Unmarshal(am, &a)
	for _, x := range t {
		f.taxes[x.From] = x.To
	}
	for _, x := range a {
		f.accounts[x.From] = x.To
	}
	return f, nil
}

func checkFiscalPosition(ctx context.Context, tx pgx.Tx, cid, id string) error {
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM fiscal_positions WHERE id = $1 AND company_id = $2 AND is_active)`, id, cid).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return badRequest("invalid_fiscal_position")
	}
	return nil
}

func (s *Server) listFiscalPositions(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT f.id, f.name, f.kind, f.note, f.tax_map, f.account_map, f.is_active, f.created_at,
			(SELECT count(*) FROM customers c WHERE c.fiscal_position_id = f.id) AS customers,
			(SELECT count(*) FROM suppliers s WHERE s.fiscal_position_id = f.id) AS suppliers
			FROM fiscal_positions f WHERE f.company_id = $1 ORDER BY f.name`, r.PathValue("cid"))
	})
}

func (s *Server) saveFiscalPosition(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name       string  `json:"name"`
		Kind       string  `json:"kind"`
		Note       string  `json:"note"`
		TaxMap     []fpMap `json:"tax_map"`
		AccountMap []fpMap `json:"account_map"`
		IsActive   *bool   `json:"is_active"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Kind == "" {
		in.Kind = "other"
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 120 || len(in.Note) > 1000 || len(in.TaxMap) > 50 || len(in.AccountMap) > 50 ||
		(in.Kind != "exempt" && in.Kind != "export" && in.Kind != "reverse_charge" && in.Kind != "other") {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if id != "" && !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[id == ""], func(ctx context.Context, tx pgx.Tx) (any, error) {
		for _, m := range in.TaxMap {
			if m.From != "*" && !isUUID(m.From) || (m.To != "" && !isUUID(m.To)) {
				return nil, badRequest("bad_request")
			}
			for _, t := range []string{m.From, m.To} {
				if t == "" || t == "*" {
					continue
				}
				var ok bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tax_rates WHERE id = $1 AND company_id = $2)`, t, cid).Scan(&ok); err != nil {
					return nil, err
				}
				if !ok {
					return nil, badRequest("invalid_tax")
				}
			}
		}
		for _, m := range in.AccountMap {
			if !isUUID(m.From) || !isUUID(m.To) {
				return nil, badRequest("bad_request")
			}
			if err := requirePostable(ctx, tx, cid, m.From); err != nil {
				return nil, err
			}
			if err := requirePostable(ctx, tx, cid, m.To); err != nil {
				return nil, err
			}
		}
		if in.TaxMap == nil {
			in.TaxMap = []fpMap{}
		}
		if in.AccountMap == nil {
			in.AccountMap = []fpMap{}
		}
		tm, _ := json.Marshal(lowerKeys(in.TaxMap))
		am, _ := json.Marshal(lowerKeys(in.AccountMap))
		if id == "" {
			return dmlJSONOne(ctx, tx, `INSERT INTO fiscal_positions (company_id, name, kind, note, tax_map, account_map, is_active)
				VALUES ($1, $2, $3, $4, $5, $6, coalesce($7, true)) RETURNING id, name, kind, note, tax_map, account_map, is_active`,
				cid, in.Name, in.Kind, strings.TrimSpace(in.Note), tm, am, in.IsActive)
		}
		return dmlJSONOne(ctx, tx, `UPDATE fiscal_positions SET name = $3, kind = $4, note = $5, tax_map = $6, account_map = $7,
			is_active = coalesce($8, is_active), updated_at = now() WHERE id = $1 AND company_id = $2 RETURNING id, name, kind, note, tax_map, account_map, is_active`, id, cid, in.Name, in.Kind, strings.TrimSpace(in.Note), tm, am, in.IsActive)
	})
}

func lowerKeys(m []fpMap) []map[string]string {
	out := make([]map[string]string, 0, len(m))
	for _, x := range m {
		out = append(out, map[string]string{"from": x.From, "to": x.To})
	}
	return out
}

// UnmarshalJSON accepts {"from", "to"} in lower case (the stored form).
func (m *fpMap) UnmarshalJSON(b []byte) error {
	var x struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := json.Unmarshal(b, &x); err != nil {
		return err
	}
	m.From, m.To = x.From, x.To
	return nil
}

// PUT /companies/{cid}/suppliers/{id}/fiscal-position {fiscal_position_id ("" = none)}
func (s *Server) putSupplierFiscalPosition(w http.ResponseWriter, r *http.Request) {
	var in struct {
		FiscalPos string `json:"fiscal_position_id"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) || (in.FiscalPos != "" && !isUUID(in.FiscalPos)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if in.FiscalPos != "" {
			if err := checkFiscalPosition(ctx, tx, cid, in.FiscalPos); err != nil {
				return nil, err
			}
		}
		return dmlJSONOne(ctx, tx, `UPDATE suppliers SET fiscal_position_id = nullif($3, '')::uuid WHERE id = $1 AND company_id = $2
			RETURNING id, fiscal_position_id`, id, cid, in.FiscalPos)
	})
}

// ======================= quotation templates =======================
// Reusable lines, terms and validity for quotations ("Generator 60 kVA installation", "Fibre home 50 Mb"…).

type tplLine struct {
	ItemID      string `json:"item_id"`
	Description string `json:"description"`
	Quantity    string `json:"quantity"`
	UnitPrice   string `json:"unit_price"`
	DiscountPct string `json:"discount_pct"`
	TaxRateID   string `json:"tax_rate_id"`
}

func (s *Server) listQuoteTemplates(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT id, name, currency, validity_days, terms, notes, lines, is_active, updated_at
			FROM quote_templates WHERE company_id = $1 ORDER BY is_active DESC, name`, r.PathValue("cid"))
	})
}

func (s *Server) saveQuoteTemplate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name         string    `json:"name"`
		Currency     string    `json:"currency"`
		ValidityDays *int      `json:"validity_days"`
		Terms        string    `json:"terms"`
		Notes        string    `json:"notes"`
		Lines        []tplLine `json:"lines"`
		IsActive     *bool     `json:"is_active"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 120 || (in.Currency != "" && !currencyRe.MatchString(in.Currency)) || len(in.Lines) > 200 ||
		(in.ValidityDays != nil && (*in.ValidityDays < 1 || *in.ValidityDays > 3650)) || len(in.Terms) > 8000 || len(in.Notes) > 4000 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if id != "" && !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[id == ""], func(ctx context.Context, tx pgx.Tx) (any, error) {
		for i, l := range in.Lines {
			if l.ItemID != "" {
				it, err := loadItem(ctx, tx, cid, l.ItemID)
				if err != nil {
					return nil, err
				}
				if strings.TrimSpace(l.Description) == "" {
					in.Lines[i].Description = it.Name
				}
			}
			if l.TaxRateID != "" {
				if _, err := loadTaxRate(ctx, tx, cid, l.TaxRateID, "sales"); err != nil {
					return nil, err
				}
			}
			if _, ok := money(l.Quantity); !ok || strings.TrimSpace(in.Lines[i].Description) == "" {
				return nil, badRequest("invalid_line")
			}
			if l.UnitPrice != "" {
				if _, ok := money(l.UnitPrice); !ok {
					return nil, badRequest("invalid_line")
				}
			}
		}
		if in.Lines == nil {
			in.Lines = []tplLine{}
		}
		lj, _ := json.Marshal(in.Lines)
		if id == "" {
			return dmlJSONOne(ctx, tx, `INSERT INTO quote_templates (company_id, name, currency, validity_days, terms, notes, lines, is_active)
				VALUES ($1, $2, nullif($3, ''), $4, $5, $6, $7, coalesce($8, true)) RETURNING id, name, currency, validity_days, terms, notes, lines, is_active`,
				cid, in.Name, in.Currency, in.ValidityDays, in.Terms, in.Notes, lj, in.IsActive)
		}
		return dmlJSONOne(ctx, tx, `UPDATE quote_templates SET name = $3, currency = nullif($4, ''), validity_days = $5, terms = $6, notes = $7,
			lines = $8, is_active = coalesce($9, is_active), updated_at = now() WHERE id = $1 AND company_id = $2 RETURNING id, name, currency, validity_days, terms, notes, lines, is_active`, id, cid, in.Name, in.Currency, in.ValidityDays, in.Terms, in.Notes, lj, in.IsActive)
	})
}

// ======================= sales on instalments =======================
// A sale (or an existing open invoice) paid as a down payment + N monthly instalments, with an optional fee
// (fixed or % of the financed part, added to the invoice as its own line). The invoice is the receivable; the
// schedule splits it for aging and reminders (view ar_open_items) and the invoice is due on its first unpaid
// instalment (trigger). Payments are applied in order: the oldest instalment is paid first.

type instalmentIn struct {
	invoiceIn
	InvoiceID   string `json:"invoice_id"`   // an existing open invoice instead of a new sale
	DownPayment string `json:"down_payment"` // due on the sale date
	Months      int    `json:"months"`
	FirstDue    string `json:"first_due"`   // default: one month after the sale date
	FeePct      string `json:"fee_percent"` // of the financed part (total − down payment)
	FeeAmount   string `json:"fee_amount"`
	PlanNotes   string `json:"plan_notes"`
}

func (s *Server) createInstalment(w http.ResponseWriter, r *http.Request) {
	var in instalmentIn
	if !decodeBody(w, r, &in) {
		return
	}
	if in.IssueDate == "" {
		in.IssueDate = today()
	}
	if in.Months < 1 || in.Months > 120 || (in.FirstDue != "" && !validDate(in.FirstDue)) || len(in.PlanNotes) > 2000 ||
		(in.InvoiceID == "" && !in.valid()) || (in.InvoiceID != "" && (!isUUID(in.InvoiceID) || len(in.Lines) > 0)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	down, feePct, feeAmt := decimal.Zero, decimal.Zero, decimal.Zero
	for _, p := range []struct {
		s string
		d *decimal.Decimal
	}{{in.DownPayment, &down}, {in.FeePct, &feePct}, {in.FeeAmount, &feeAmt}} {
		if strings.TrimSpace(p.s) == "" {
			continue
		}
		v, ok := money(p.s)
		if !ok {
			writeErr(w, http.StatusBadRequest, "invalid_amount")
			return
		}
		*p.d = v
	}
	if feePct.GreaterThan(decimal.NewFromInt(100)) || (in.InvoiceID != "" && (feePct.IsPositive() || feeAmt.IsPositive())) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var invID, customer, currency, issue string
		var total decimal.Decimal
		if in.InvoiceID != "" {
			var tot, paid, status string
			err := tx.QueryRow(ctx, `SELECT customer_id::text, currency::text, total::text, paid::text, status, issue_date::text FROM invoices
				WHERE id = $1 AND company_id = $2 AND kind = 'invoice' FOR UPDATE`, in.InvoiceID, cid).Scan(&customer, &currency, &tot, &paid, &status, &issue)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, notFound()
			}
			if err != nil {
				return nil, err
			}
			if status != "open" && status != "partial" {
				return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
			}
			total, _ = decimal.NewFromString(tot)
			invID = in.InvoiceID
		} else {
			// the fee is part of the sale: one more invoice line, computed on the financed part
			if feePct.IsPositive() || feeAmt.IsPositive() {
				dec, err := currencyDecimals(ctx, tx, in.Currency)
				if err != nil {
					return nil, err
				}
				gross := decimal.Zero
				for _, l := range in.Lines {
					q, ok1 := money(l.Quantity)
					p, ok2 := money(l.UnitPrice)
					if !ok1 || !ok2 {
						return nil, badRequest("bad_request")
					}
					gross = gross.Add(q.Mul(p).Round(dec))
				}
				fee := feeAmt.Add(gross.Sub(down).Mul(feePct).Div(decimal.NewFromInt(100))).Round(dec)
				if fee.IsPositive() {
					in.Lines = append(in.Lines, invoiceLineIn{Description: fmt.Sprintf("Instalment fee (%d months)", in.Months), Quantity: "1", UnitPrice: fee.String()})
				}
			}
			id, err := createInvoiceTx(ctx, tx, r, cid, &in.invoiceIn)
			if err != nil {
				return nil, err
			}
			var tot string
			if err := tx.QueryRow(ctx, `SELECT customer_id::text, currency::text, total::text, issue_date::text FROM invoices WHERE id = $1`, id).
				Scan(&customer, &currency, &tot, &issue); err != nil {
				return nil, err
			}
			total, _ = decimal.NewFromString(tot)
			invID = id
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM instalment_plans WHERE invoice_id = $1 AND status = 'active')`, invID).Scan(&exists); err != nil {
			return nil, err
		}
		if exists {
			return nil, &apiError{status: http.StatusConflict, code: "already_on_instalments"}
		}
		if down.GreaterThanOrEqual(total) {
			return nil, badRequest("down_payment_too_big")
		}
		dec, err := currencyDecimals(ctx, tx, currency)
		if err != nil {
			return nil, err
		}
		start, _ := time.Parse(time.DateOnly, issue)
		first := start.AddDate(0, 1, 0)
		if in.FirstDue != "" {
			first, _ = time.Parse(time.DateOnly, in.FirstDue)
			if first.Before(start) {
				return nil, badRequest("bad_request")
			}
		}
		var number int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'instalment')`, cid).Scan(&number); err != nil {
			return nil, err
		}
		fee := decimal.Zero
		if in.InvoiceID == "" && (feePct.IsPositive() || feeAmt.IsPositive()) {
			var f string
			if err := tx.QueryRow(ctx, `SELECT coalesce(sum(amount + tax_amount), 0)::text FROM invoice_lines WHERE invoice_id = $1 AND description LIKE 'Instalment fee%'`, invID).Scan(&f); err == nil {
				fee, _ = decimal.NewFromString(f)
			}
		}
		var planID string
		if err := tx.QueryRow(ctx, `INSERT INTO instalment_plans (company_id, number, invoice_id, customer_id, currency, total, down_payment, months, fee, first_due, notes)
			VALUES ($1, $2, $3, $4, $5, $6::numeric, $7::numeric, $8, $9::numeric, $10::date, $11) RETURNING id::text`,
			cid, number, invID, customer, currency, total.String(), down.String(), in.Months, fee.String(), first.Format(time.DateOnly),
			strings.TrimSpace(in.PlanNotes)).Scan(&planID); err != nil {
			return nil, err
		}
		seq := 0
		if down.IsPositive() {
			if _, err := tx.Exec(ctx, `INSERT INTO instalment_lines (company_id, plan_id, seq, due_date, amount) VALUES ($1, $2, 0, $3::date, $4::numeric)`,
				cid, planID, issue, down.String()); err != nil {
				return nil, err
			}
		}
		rest := total.Sub(down)
		each := rest.Div(decimal.NewFromInt(int64(in.Months))).RoundDown(dec)
		for i := 1; i <= in.Months; i++ {
			amt := each
			if i == in.Months {
				amt = rest.Sub(each.Mul(decimal.NewFromInt(int64(in.Months - 1))))
			}
			seq++
			if _, err := tx.Exec(ctx, `INSERT INTO instalment_lines (company_id, plan_id, seq, due_date, amount) VALUES ($1, $2, $3, $4::date, $5::numeric)`,
				cid, planID, seq, addMonthsClamped(first, i-1).Format(time.DateOnly), amt.String()); err != nil {
				return nil, err
			}
		}
		// align the invoice's due date with the schedule (the trigger keeps it right on every payment)
		if _, err := tx.Exec(ctx, `UPDATE invoices SET paid = paid WHERE id = $1`, invID); err != nil {
			return nil, err
		}
		return loadInstalment(ctx, tx, cid, planID)
	})
}

// addMonthsClamped: the same day n months later, clamped to the month's last day (31 Jan + 1 → 28/29 Feb).
func addMonthsClamped(t time.Time, n int) time.Time {
	y, m, d := t.Date()
	first := time.Date(y, m+time.Month(n), 1, 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, 1, -1).Day()
	if d > last {
		d = last
	}
	return time.Date(first.Year(), first.Month(), d, 0, 0, 0, 0, time.UTC)
}

const instalmentCols = `p.id, p.number, p.invoice_id, i.number AS invoice_number, p.customer_id, c.code AS customer_code, c.name AS customer_name,
	c.phone AS customer_phone, p.currency, p.total::text AS total, p.down_payment::text AS down_payment, p.months, p.fee::text AS fee, p.first_due,
	p.status, p.notes, p.created_at, i.paid::text AS paid, (i.total - i.paid)::text AS balance, i.due_date AS next_due, i.issue_date,
	(SELECT coalesce(sum(o.balance), 0) FROM ar_open_items o WHERE o.id = i.id AND o.due_date < current_date)::text AS overdue`

func loadInstalment(ctx context.Context, tx pgx.Tx, cid, id string) (any, error) {
	plan, err := queryJSONOne(ctx, tx, `SELECT `+instalmentCols+` FROM instalment_plans p JOIN invoices i ON i.id = p.invoice_id
		JOIN customers c ON c.id = p.customer_id WHERE p.id = $1 AND p.company_id = $2`, id, cid)
	if err != nil {
		return nil, err
	}
	lines, err := queryJSON(ctx, tx, `SELECT l.seq, l.due_date, l.amount::text AS amount,
		least(l.amount, greatest(0, i.paid - (l.cum - l.amount)))::text AS paid,
		CASE WHEN i.paid >= l.cum THEN 'paid' WHEN l.due_date < current_date THEN 'late' WHEN i.paid > l.cum - l.amount THEN 'partial' ELSE 'due' END AS state
		FROM (SELECT seq, due_date, amount, plan_id, sum(amount) OVER (ORDER BY seq) AS cum FROM instalment_lines WHERE plan_id = $1) l
		JOIN instalment_plans p ON p.id = l.plan_id JOIN invoices i ON i.id = p.invoice_id ORDER BY l.seq`, id)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"plan": plan, "lines": lines}, nil
}

func (s *Server) listInstalments(w http.ResponseWriter, r *http.Request) {
	status, cust := r.URL.Query().Get("status"), r.URL.Query().Get("customer_id")
	if (status != "" && status != "active" && status != "paid" && status != "cancelled" && status != "late") || (cust != "" && !isUUID(cust)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT * FROM (SELECT `+instalmentCols+` FROM instalment_plans p JOIN invoices i ON i.id = p.invoice_id
			JOIN customers c ON c.id = p.customer_id WHERE p.company_id = $1 AND ($3 = '' OR p.customer_id = nullif($3, '')::uuid)) x
			WHERE ($2 = '' OR ($2 = 'late' AND status = 'active' AND overdue::numeric > 0) OR status = $2)
			ORDER BY next_due, number LIMIT 1000`, r.PathValue("cid"), status, cust)
	})
}

func (s *Server) getInstalment(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) { return loadInstalment(ctx, tx, cid, id) })
}

// POST /companies/{cid}/instalments/{id}/{action}: payoff (every unpaid instalment becomes due today, with an
// optional waiver of the unearned part of the fee as a credit note) | cancel (the invoice goes back to one due date).
func (s *Server) instalmentAction(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Waive string `json:"waive"` // payoff: amount of the fee given back (credit note)
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id, action := r.PathValue("cid"), r.PathValue("id"), r.PathValue("action")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	waive := decimal.Zero
	if strings.TrimSpace(in.Waive) != "" {
		v, ok := money(in.Waive)
		if !ok {
			writeErr(w, http.StatusBadRequest, "invalid_amount")
			return
		}
		waive = v
		if waive.IsPositive() && !can(r, "billing.approve") {
			writeErr(w, http.StatusForbidden, "forbidden") // giving money back to a customer is a credit note: approvers only
			return
		}
	}
	var invoiceID string
	ok := false
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var status, fee string
		err := tx.QueryRow(ctx, `SELECT status, invoice_id::text, fee::text FROM instalment_plans WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).
			Scan(&status, &invoiceID, &fee)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if status != "active" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		switch action {
		case "payoff":
			f, _ := decimal.NewFromString(fee)
			if waive.GreaterThan(f) {
				return nil, badRequest("waive_over_fee")
			}
			if _, err := tx.Exec(ctx, `UPDATE instalment_lines l SET due_date = least(l.due_date, current_date)
				WHERE l.plan_id = $1`, id); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `UPDATE instalment_plans SET notes = trim(notes || E'\nEarly payoff ' || current_date::text), updated_at = now() WHERE id = $1`, id); err != nil {
				return nil, err
			}
		case "cancel":
			if _, err := tx.Exec(ctx, `UPDATE instalment_plans SET status = 'cancelled', updated_at = now() WHERE id = $1`, id); err != nil {
				return nil, err
			}
		default:
			return nil, notFound()
		}
		if _, err := tx.Exec(ctx, `UPDATE invoices SET paid = paid WHERE id = $1`, invoiceID); err != nil {
			return nil, err
		}
		ok = true
		return loadInstalment(ctx, tx, cid, id)
	})
	// the fee waiver is a normal credit note on the invoice (its own transaction, same checks and approvals)
	if ok && action == "payoff" && waive.IsPositive() {
		_, _ = callInternal(r, s.createCreditNote, map[string]string{"cid": cid, "id": invoiceID},
			map[string]any{"amount": waive.String(), "reason": "Instalment fee waived (early payoff)"})
	}
}
