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
	p.currency, p.amount::text AS amount, p.change_currency, p.change_amount::text AS change_amount, p.status, p.kind, p.invoice_id,
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
	ClientRef      string     `json:"client_ref"` // set by offline devices; the same ref is stored only once
}

type method struct {
	ID, Name, Currency, Account string
	UserID                      *string
	Active                      bool
	ChangeRounding              decimal.Decimal // change given from this box is rounded down to this step (0 = currency decimals)
}

func loadMethods(ctx context.Context, tx pgx.Tx, cid string) (map[string]method, error) {
	rows, err := tx.Query(ctx, `SELECT id::text, name, currency::text, account_id::text, user_id::text, is_active,
		coalesce(change_rounding, 0)::text FROM payment_methods WHERE company_id = $1`, cid)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (method, error) {
		var m method
		var step string
		err := row.Scan(&m.ID, &m.Name, &m.Currency, &m.Account, &m.UserID, &m.Active, &step)
		m.ChangeRounding, _ = decimal.NewFromString(step)
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
	in.ClientRef = strings.TrimSpace(in.ClientRef)
	if !validDate(in.Date) || !currencyRe.MatchString(in.Currency) || len(in.Tenders) == 0 || len(in.Tenders) > 10 ||
		(in.ChangeMethodID != "" && !isUUID(in.ChangeMethodID)) || (in.ClientRef != "" && (len(in.ClientRef) < 8 || len(in.ClientRef) > 64)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	collector := isCollector(r)
	me := principal(r).ID
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if in.ClientRef != "" {
			var existing string
			err := tx.QueryRow(ctx, `SELECT id::text FROM payments WHERE client_ref = $1 AND company_id = $2`, in.ClientRef, cid).Scan(&existing)
			if err == nil {
				return loadPayment(ctx, tx, cid, existing) // already received (phone retried after going offline)
			}
			if err != pgx.ErrNoRows {
				return nil, err
			}
		}
		if collector {
			var mine bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM customers c JOIN areas a ON a.id = c.area_id
				WHERE c.id = $1 AND a.collector_id = app_user())`, in.CustomerID).Scan(&mine); err != nil || !mine {
				return nil, &apiError{status: http.StatusForbidden, code: "forbidden"}
			}
		}
		usable := func(m method) bool { return m.Active && (!collector || m.UserID == nil || *m.UserID == me) }
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
			if !ok || !usable(m) || !ok2 || !amt.IsPositive() {
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
			if !ok || !usable(m) {
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
			if m.ChangeRounding.IsPositive() { // e.g. LBP change rounded down to 1,000 or 5,000
				changeAmt = changeAmt.Div(m.ChangeRounding).Floor().Mul(m.ChangeRounding)
			}
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
		// earlier credits (advances, credit notes) settle open invoices first, with their own FX difference
		if err := autoAllocate(ctx, tx, cid, set, in.CustomerID, in.Currency, ""); err != nil {
			return nil, err
		}
		arLines, err := arCreditLines(ctx, tx, *set.AR, set.Base, in.CustomerID, name, in.Currency, applied, payRateS)
		if err != nil {
			return nil, err
		}
		lines = append(lines, arLines...)
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
			change_method_id, change_currency, change_amount, journal_entry_id, notes, client_ref, ar_rate)
			VALUES ($1, $2, $3, $4::date, $5, $6::numeric, $7, $8, $9::numeric, $10, $11, nullif($12, ''), $13::numeric) RETURNING id::text`,
			cid, number, in.CustomerID, in.Date, in.Currency, applied.String(), changeMethod, changeCur, changeAmt.String(),
			je, strings.TrimSpace(in.Notes), in.ClientRef, payRateS).Scan(&id); err != nil {
			return nil, err
		}
		for _, t := range tenders {
			if _, err := tx.Exec(ctx, `INSERT INTO payment_tenders (payment_id, method_id, currency, amount, rate)
				VALUES ($1, $2, $3, $4::numeric, $5::numeric)`, id, t.m.ID, t.m.Currency, t.amount.String(), t.rate); err != nil {
				return nil, err
			}
		}
		if err := autoAllocate(ctx, tx, cid, set, in.CustomerID, in.Currency, id); err != nil {
			return nil, err
		}
		if err := autoMessage(ctx, tx, cid, "payment_received", in.CustomerID, nil, &id); err != nil {
			return nil, err
		}
		if err := reconnectIfCleared(ctx, tx, cid, in.CustomerID); err != nil {
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
	out := map[string]json.RawMessage{"payment": pay, "tenders": tenders, "allocations": allocs, "customer_balance": bal}
	// amount applied shown in the company's second currency (Lebanese bills/receipts in LBP)
	var pcur, pamt, pdate string
	if err := tx.QueryRow(ctx, `SELECT currency::text, amount::text, payment_date::text FROM payments WHERE id = $1`, id).Scan(&pcur, &pamt, &pdate); err == nil {
		if set, err := loadBillingSettings(ctx, tx, cid); err == nil {
			a, _ := decimal.NewFromString(pamt)
			if cur, rate, amt := localSnapshot(ctx, tx, cid, set, pcur, pdate, a); cur != nil {
				b, _ := json.Marshal(map[string]string{"currency": *cur, "rate": *rate, "amount": *amt, "date": pdate})
				out["local"] = b
			}
		}
	}
	return out, nil
}

func (s *Server) getPayment(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if isCollector(r) {
			var mine bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM payments WHERE id::text = $1 AND received_by = app_user())`,
				r.PathValue("id")).Scan(&mine); err != nil || !mine {
				return nil, notFound()
			}
		}
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
	kindCond := " AND p.kind = 'payment'"
	switch q.Get("kind") {
	case "credit_note":
		kindCond = " AND p.kind = 'credit_note'"
	case "all":
		kindCond = ""
	}
	receivedBy := "" // bind parameter $6: only payments received by this user
	if isCollector(r) {
		receivedBy = principal(r).ID
	} else if by := q.Get("received_by"); isUUID(by) {
		receivedBy = by
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		cond := `p.company_id = $1 AND p.payment_date BETWEEN $2::date AND $3::date AND ($4 = '' OR p.customer_id = $4::uuid)
			AND ($5 = '' OR c.name ILIKE '%' || $5 || '%' OR c.code ILIKE $5 || '%' OR p.number::text = $5)
			AND ($6 = '' OR p.received_by = $6::uuid)` + kindCond
		rows, err := queryJSON(ctx, tx, `SELECT `+paymentCols+` FROM payments p JOIN customers c ON c.id = p.customer_id
			LEFT JOIN users u ON u.id = p.received_by WHERE `+cond+` ORDER BY p.payment_date DESC, p.number DESC LIMIT $7 OFFSET $8`,
			r.PathValue("cid"), from, to, customer, search, receivedBy, limit, offset)
		if err != nil {
			return nil, err
		}
		// cash actually received per method and currency (what each cash box should hold)
		byMethod, err := queryJSON(ctx, tx, `SELECT method, currency, sum(received)::text AS received, sum(change)::text AS change_given FROM (
			  SELECT m.name AS method, t.currency, t.amount AS received, 0::numeric AS change
			  FROM payment_tenders t JOIN payments p ON p.id = t.payment_id JOIN customers c ON c.id = p.customer_id
			  JOIN payment_methods m ON m.id = t.method_id WHERE `+cond+` AND p.status = 'posted'
			  UNION ALL
			  SELECT m.name, p.change_currency, 0, p.change_amount
			  FROM payments p JOIN customers c ON c.id = p.customer_id JOIN payment_methods m ON m.id = p.change_method_id
			  WHERE `+cond+` AND p.status = 'posted' AND p.change_amount > 0
			) x GROUP BY method, currency ORDER BY method`,
			r.PathValue("cid"), from, to, customer, search, receivedBy)
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
		var status, customer, kind string
		var je *string
		err := tx.QueryRow(ctx, `SELECT number, status, customer_id::text, journal_entry_id::text, kind FROM payments
			WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&number, &status, &customer, &je, &kind)
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
		label := map[string]string{"payment": "payment", "credit_note": "credit note"}[kind]
		if err := reverseAllocations(ctx, tx, id, fmt.Sprintf("Void %s #%d · FX", label, number)); err != nil {
			return nil, err
		}
		// a refund paid out of this credit must be voided first (the credit would go negative)
		var refunded bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM invoices WHERE customer_id = $1 AND kind = 'refund'
			AND status IN ('open', 'partial'))`, customer).Scan(&refunded); err != nil {
			return nil, err
		}
		if refunded {
			return nil, &apiError{status: http.StatusConflict, code: "refund_uses_credit"}
		}
		var rev *string
		if je != nil {
			var rid string
			if err := tx.QueryRow(ctx, `SELECT reverse_journal_entry($1, current_date, $2)::text`,
				*je, fmt.Sprintf("Void %s #%d %s", label, number, strings.TrimSpace(in.Reason))).Scan(&rid); err != nil {
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
