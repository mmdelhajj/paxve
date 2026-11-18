package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

const paymentCols = `p.id, p.number, p.customer_id, c.code AS customer_code, c.name AS customer_name, p.payment_date,
	p.currency, p.amount::text AS amount, p.change_currency, p.change_amount::text AS change_amount, p.status,
	p.journal_entry_id, p.notes, u.full_name AS received_by, p.created_at`

type tenderIn struct {
	MethodID string `json:"method_id"`
	Amount   string `json:"amount"`
	Rate     string `json:"rate"`
}

type paymentIn struct {
	CustomerID     string     `json:"customer_id"`
	Date           string     `json:"date"`
	Currency       string     `json:"currency"`
	Amount         string     `json:"amount"` // amount applied to the account; "" = everything received
	Tenders        []tenderIn `json:"tenders"`
	ChangeMethodID string     `json:"change_method_id"`
	Notes          string     `json:"notes"`
}

type method struct {
	ID, Name, Currency, Account string
	Active                      bool
}

func loadMethods(ctx context.Context, tx pgx.Tx, cid string) (map[string]method, error) {
	rows, err := tx.Query(ctx, `SELECT id::text, name, currency::text, account_id::text, is_active FROM payment_methods WHERE company_id = $1`, cid)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (method, error) {
		var m method
		err := row.Scan(&m.ID, &m.Name, &m.Currency, &m.Account, &m.Active)
		return m, err
	})
	out := map[string]method{}
	for _, m := range list {
		out[m.ID] = m
	}
	return out, err
}

// createPayment records money received in one or more currencies, gives change, books the journal
// entry and applies the payment to the customer's open invoices (oldest first).
//
// Conversion: every tender is converted to the payment currency through the base currency
// (amount / tender_rate × payment_rate). Change = received − applied, converted to the change
// currency and rounded down to that currency's decimals.
func (s *Server) createPayment(w http.ResponseWriter, r *http.Request) {
	var in paymentIn
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Date == "" {
		in.Date = today()
	}
	if !validDate(in.Date) || !currencyRe.MatchString(in.Currency) || len(in.Tenders) == 0 || len(in.Tenders) > 10 ||
		(in.ChangeMethodID != "" && !isUUID(in.ChangeMethodID)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		if err := set.requireAR(); err != nil {
			return nil, err
		}
		name, err := lockCustomer(ctx, tx, cid, in.CustomerID)
		if err != nil {
			return nil, err
		}
		methods, err := loadMethods(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		payRateS, err := resolveRate(ctx, tx, cid, set.Base, in.Currency, in.Date, "")
		if err != nil {
			return nil, err
		}
		payRate, _ := decimal.NewFromString(payRateS)

		type tender struct {
			m      method
			amount decimal.Decimal
			rate   string
		}
		tenders := []tender{}
		received := decimal.Zero
		for _, t := range in.Tenders {
			m, ok := methods[t.MethodID]
			amt, ok2 := money(t.Amount)
			if !ok || !m.Active || !ok2 || !amt.IsPositive() {
				return nil, badRequest("invalid_tender")
			}
			given := strings.TrimSpace(t.Rate)
			if given != "" {
				if rv, err := decimal.NewFromString(given); err != nil || !rv.IsPositive() {
					return nil, badRequest("invalid_rate")
				}
			}
			rateS, err := resolveRate(ctx, tx, cid, set.Base, m.Currency, in.Date, given)
			if err != nil {
				return nil, err
			}
			rate, _ := decimal.NewFromString(rateS)
			received = received.Add(amt.Mul(payRate).DivRound(rate, 10))
			tenders = append(tenders, tender{m, amt, rateS})
		}
		received = received.Round(4)

		applied := received
		if strings.TrimSpace(in.Amount) != "" {
			a, ok := money(in.Amount)
			if !ok || !a.IsPositive() {
				return nil, badRequest("invalid_amount")
			}
			// allow a rounding tolerance of 0.01 in the payment currency
			if a.Sub(received).GreaterThan(decimal.NewFromFloat(0.01)) {
				return nil, &apiError{status: http.StatusUnprocessableEntity, code: "insufficient_payment",
					detail: received.String() + " " + in.Currency}
			}
			applied = a
		}
		if !applied.IsPositive() {
			return nil, badRequest("invalid_amount")
		}

		var changeM *method
		changeAmt := decimal.Zero
		if extra := received.Sub(applied); extra.IsPositive() {
			if in.ChangeMethodID == "" {
				return nil, badRequest("change_method_required")
			}
			m, ok := methods[in.ChangeMethodID]
			if !ok || !m.Active {
				return nil, badRequest("change_method_required")
			}
			crS, err := resolveRate(ctx, tx, cid, set.Base, m.Currency, in.Date, "")
			if err != nil {
				return nil, err
			}
			cr, _ := decimal.NewFromString(crS)
			dec, err := currencyDecimals(ctx, tx, m.Currency)
			if err != nil {
				return nil, err
			}
			changeAmt = extra.Mul(cr).Div(payRate).RoundFloor(dec)
			changeM = &m
		}

		var number int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'payment')`, cid).Scan(&number); err != nil {
			return nil, err
		}
		lines := []jline{}
		for _, t := range tenders {
			lines = append(lines, jline{Account: t.m.Account, Desc: t.m.Name, Currency: t.m.Currency, Debit: t.amount, Rate: t.rate})
		}
		if changeM != nil && changeAmt.IsPositive() {
			lines = append(lines, jline{Account: changeM.Account, Desc: "Change · " + changeM.Name, Currency: changeM.Currency, Credit: changeAmt})
		}
		lines = append(lines, jline{Account: *set.AR, Desc: name, Currency: in.Currency, Credit: applied, Rate: payRateS})
		je, err := postJournal(ctx, tx, cid, set, in.Date, fmt.Sprintf("Payment #%d · %s", number, name),
			fmt.Sprintf("PAY-%d", number), "payment", lines)
		if err != nil {
			return nil, err
		}

		var changeMethod, changeCur *string
		if changeM != nil && changeAmt.IsPositive() {
			changeMethod, changeCur = &changeM.ID, &changeM.Currency
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO payments (company_id, number, customer_id, payment_date, currency, amount,
			change_method_id, change_currency, change_amount, journal_entry_id, notes)
			VALUES ($1, $2, $3, $4::date, $5, $6::numeric, $7, $8, $9::numeric, $10, $11) RETURNING id::text`,
			cid, number, in.CustomerID, in.Date, in.Currency, applied.String(), changeMethod, changeCur, changeAmt.String(),
			je, strings.TrimSpace(in.Notes)).Scan(&id); err != nil {
			return nil, err
		}
		for _, t := range tenders {
			if _, err := tx.Exec(ctx, `INSERT INTO payment_tenders (payment_id, method_id, currency, amount, rate)
				VALUES ($1, $2, $3, $4::numeric, $5::numeric)`, id, t.m.ID, t.m.Currency, t.amount.String(), t.rate); err != nil {
				return nil, err
			}
		}
		if err := autoAllocate(ctx, tx, in.CustomerID, in.Currency); err != nil {
			return nil, err
		}
		return loadPayment(ctx, tx, cid, id)
	})
}

func loadPayment(ctx context.Context, tx pgx.Tx, cid, id string) (any, error) {
	if !isUUID(id) {
		return nil, notFound()
	}
	pay, err := queryJSONOne(ctx, tx, `SELECT `+paymentCols+`, c.phone AS customer_phone, co.name_en AS company_name,
		co.name_ar AS company_name_ar, co.base_currency AS base_currency
		FROM payments p JOIN customers c ON c.id = p.customer_id JOIN companies co ON co.id = p.company_id
		LEFT JOIN users u ON u.id = p.received_by WHERE p.id = $1 AND p.company_id = $2`, id, cid)
	if err != nil {
		return nil, err
	}
	tenders, err := queryJSON(ctx, tx, `SELECT m.name AS method, t.currency, t.amount::text AS amount, trim_scale(t.rate)::text AS rate
		FROM payment_tenders t JOIN payment_methods m ON m.id = t.method_id WHERE t.payment_id = $1`, id)
	if err != nil {
		return nil, err
	}
	allocs, err := queryJSON(ctx, tx, `SELECT a.invoice_id, i.number AS invoice_number, i.period, a.amount::text AS amount
		FROM payment_allocations a JOIN invoices i ON i.id = a.invoice_id WHERE a.payment_id = $1 ORDER BY a.created_at`, id)
	if err != nil {
		return nil, err
	}
	bal, err := queryJSONOne(ctx, tx, `SELECT `+customerBalances+` FROM customers c WHERE c.id = (SELECT customer_id FROM payments WHERE id = $1)`, id)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"payment": pay, "tenders": tenders, "allocations": allocs, "customer_balance": bal}, nil
}

func (s *Server) getPayment(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return loadPayment(ctx, tx, r.PathValue("cid"), r.PathValue("id"))
	})
}

func (s *Server) listPayments(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, to, customer, search := q.Get("from"), q.Get("to"), q.Get("customer_id"), strings.TrimSpace(q.Get("q"))
	if from == "" {
		from = "0001-01-01"
	}
	if to == "" {
		to = "9999-12-31"
	}
	if !validDate(from) || !validDate(to) || (customer != "" && !isUUID(customer)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	limit, offset := queryInt(r, "limit", 200, 2000), queryInt(r, "offset", 0, 1<<30)
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		cond := `p.company_id = $1 AND p.payment_date BETWEEN $2::date AND $3::date AND ($4 = '' OR p.customer_id = $4::uuid)
			AND ($5 = '' OR c.name ILIKE '%' || $5 || '%' OR c.code ILIKE $5 || '%' OR p.number::text = $5)`
		rows, err := queryJSON(ctx, tx, `SELECT `+paymentCols+` FROM payments p JOIN customers c ON c.id = p.customer_id
			LEFT JOIN users u ON u.id = p.received_by WHERE `+cond+` ORDER BY p.payment_date DESC, p.number DESC LIMIT $6 OFFSET $7`,
			r.PathValue("cid"), from, to, customer, search, limit, offset)
		if err != nil {
			return nil, err
		}
		// cash actually received per method and currency (what each cash box should hold)
		byMethod, err := queryJSON(ctx, tx, `SELECT m.name AS method, t.currency, sum(t.amount)::text AS received,
			coalesce((SELECT sum(p2.change_amount) FROM payments p2 JOIN customers c2 ON c2.id = p2.customer_id
			          WHERE p2.change_method_id = m.id AND p2.status = 'posted' AND p2.company_id = $1
			            AND p2.payment_date BETWEEN $2::date AND $3::date AND ($4 = '' OR p2.customer_id = $4::uuid)), 0)::text AS change_given
			FROM payment_tenders t JOIN payments p ON p.id = t.payment_id JOIN customers c ON c.id = p.customer_id
			JOIN payment_methods m ON m.id = t.method_id
			WHERE `+cond+` AND p.status = 'posted' GROUP BY m.id, m.name, t.currency ORDER BY m.name`,
			r.PathValue("cid"), from, to, customer, search)
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"rows": rows, "by_method": byMethod}, nil
	})
}

// voidPayment reverses the journal entry and un-applies the payment from its invoices.
func (s *Server) voidPayment(w http.ResponseWriter, r *http.Request) {
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
		var status, customer string
		var je *string
		err := tx.QueryRow(ctx, `SELECT number, status, customer_id::text, journal_entry_id::text FROM payments
			WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&number, &status, &customer, &je)
		if err == pgx.ErrNoRows {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if status == "void" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if _, err := lockCustomer(ctx, tx, cid, customer); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO payment_allocations (payment_id, invoice_id, amount)
			SELECT payment_id, invoice_id, -sum(amount) FROM payment_allocations WHERE payment_id = $1
			GROUP BY payment_id, invoice_id HAVING sum(amount) <> 0`, id); err != nil {
			return nil, err
		}
		var rev *string
		if je != nil {
			var rid string
			if err := tx.QueryRow(ctx, `SELECT reverse_journal_entry($1, current_date, $2)::text`,
				*je, fmt.Sprintf("Void payment #%d %s", number, strings.TrimSpace(in.Reason))).Scan(&rid); err != nil {
				return nil, err
			}
			rev = &rid
		}
		if _, err := tx.Exec(ctx, `UPDATE payments SET status = 'void', void_entry_id = $2,
			notes = CASE WHEN $3 = '' THEN notes ELSE trim(notes || ' ' || $3) END WHERE id = $1`, id, rev, strings.TrimSpace(in.Reason)); err != nil {
			return nil, err
		}
		return loadPayment(ctx, tx, cid, id)
	})
}
