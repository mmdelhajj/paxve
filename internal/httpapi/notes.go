package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Credit notes (customer), refunds (money paid back to a customer) and supplier debit notes.
//
//   - A credit note reverses part or all of an invoice: every non-receivable line of the invoice's journal
//     entry (revenue, VAT, …) is debited in proportion, receivables are credited at the invoice's own rate.
//     It is stored with the payments (kind 'credit_note'), applied to its invoice first; any part the invoice
//     no longer needs (already paid) stays as customer credit for later invoices or a refund.
//   - A refund pays customer credit back (Dr receivables / Cr cash box). It is stored as an invoice of kind
//     'refund' and is settled at once by the customer's credit.
//   - A supplier debit note reverses part or all of a purchase bill (Cr expense/inventory/VAT, Dr payables);
//     stock bought on the bill goes back out at the bill's cost.

// prorate splits amount over parts in proportion (rounded to dec); rounding goes to the largest part.
func prorate(parts []decimal.Decimal, amount decimal.Decimal, dec int32) []decimal.Decimal {
	total := decimal.Zero
	for _, p := range parts {
		total = total.Add(p)
	}
	out := make([]decimal.Decimal, len(parts))
	if total.IsZero() {
		return out
	}
	if amount.Equal(total) {
		copy(out, parts)
		return out
	}
	sum := decimal.Zero
	big := 0
	for i, p := range parts {
		out[i] = p.Mul(amount).Div(total).Round(dec)
		sum = sum.Add(out[i])
		if p.GreaterThan(parts[big]) {
			big = i
		}
	}
	out[big] = out[big].Add(amount.Sub(sum))
	return out
}

type jeLine struct {
	account, currency, rate string
	debit, credit           decimal.Decimal
}

func entryLines(ctx context.Context, tx pgx.Tx, je string) ([]jeLine, error) {
	rows, err := tx.Query(ctx, `SELECT account_id::text, currency::text, rate::text, debit::text, credit::text
		FROM journal_lines WHERE entry_id = $1 ORDER BY line_no`, je)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (jeLine, error) {
		var l jeLine
		var d, c string
		err := row.Scan(&l.account, &l.currency, &l.rate, &d, &c)
		l.debit, _ = decimal.NewFromString(d)
		l.credit, _ = decimal.NewFromString(c)
		return l, err
	})
}

// ---------------- customer credit notes ----------------

func (s *Server) createCreditNote(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Amount         string `json:"amount"` // total incl. tax; "" = everything not yet credited
		Date           string `json:"date"`
		Reason         string `json:"reason"`
		RefundMethodID string `json:"refund_method_id"` // optional: pay back at once the part the invoice does not need
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Date == "" {
		in.Date = today()
	}
	cid, invID := r.PathValue("cid"), r.PathValue("id")
	if !validDate(in.Date) || !isUUID(invID) || (in.RefundMethodID != "" && !isUUID(in.RefundMethodID)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		if err := set.requireAR(); err != nil {
			return nil, err
		}
		var number int64
		var customer, currency, totalS, paidS, status, kind, issue string
		var je *string
		err = tx.QueryRow(ctx, `SELECT number, customer_id::text, currency::text, total::text, paid::text, status, kind, journal_entry_id::text,
			issue_date::text FROM invoices WHERE id = $1 AND company_id = $2 FOR UPDATE`, invID, cid).
			Scan(&number, &customer, &currency, &totalS, &paidS, &status, &kind, &je, &issue)
		if err == pgx.ErrNoRows {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if status == "void" || kind != "invoice" || je == nil {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if in.Date < issue {
			return nil, badRequest("invalid_date")
		}
		name, err := lockCustomer(ctx, tx, cid, customer)
		if err != nil {
			return nil, err
		}
		total, _ := decimal.NewFromString(totalS)
		var creditedS string
		if err := tx.QueryRow(ctx, `SELECT coalesce(sum(amount), 0)::text FROM payments WHERE invoice_id = $1 AND kind = 'credit_note'
			AND status = 'posted'`, invID).Scan(&creditedS); err != nil {
			return nil, err
		}
		credited, _ := decimal.NewFromString(creditedS)
		left := total.Sub(credited)
		amount := left
		if strings.TrimSpace(in.Amount) != "" {
			a, ok := money(in.Amount)
			if !ok {
				return nil, badRequest("invalid_amount")
			}
			amount = a
		}
		if !amount.IsPositive() || amount.GreaterThan(left) {
			return nil, &apiError{status: http.StatusUnprocessableEntity, code: "credit_exceeds_invoice", detail: left.String() + " " + currency}
		}
		dec, err := currencyDecimals(ctx, tx, currency)
		if err != nil {
			return nil, err
		}
		lines, err := entryLines(ctx, tx, *je)
		if err != nil {
			return nil, err
		}
		arRate := ""
		parts, src := []decimal.Decimal{}, []jeLine{}
		for _, l := range lines {
			if l.account == *set.AR && l.debit.IsPositive() {
				arRate = l.rate
				continue
			}
			if l.credit.IsPositive() && l.currency == currency {
				parts = append(parts, l.credit)
				src = append(src, l)
			}
		}
		if arRate == "" || len(parts) == 0 {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'credit_note')`, cid).Scan(&number); err != nil {
			return nil, err
		}
		var invNumber int64
		_ = tx.QueryRow(ctx, `SELECT number FROM invoices WHERE id = $1`, invID).Scan(&invNumber)
		reason := strings.TrimSpace(in.Reason)
		desc := fmt.Sprintf("Credit note #%d · invoice #%d", number, invNumber)
		jl := []jline{}
		for i, p := range prorate(parts, amount, dec) {
			if p.IsPositive() {
				jl = append(jl, jline{Account: src[i].account, Desc: desc, Currency: currency, Debit: p, Rate: src[i].rate})
			}
		}
		jl = append(jl, jline{Account: *set.AR, Desc: name, Currency: currency, Credit: amount, Rate: arRate})
		memo := desc + " · " + name
		if reason != "" {
			memo += " · " + reason
		}
		entry, err := postJournal(ctx, tx, cid, set, in.Date, memo, fmt.Sprintf("CN-%d", number), "credit_note", jl)
		if err != nil {
			return nil, err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO payments (company_id, number, customer_id, payment_date, currency, amount, journal_entry_id,
			notes, kind, invoice_id, ar_rate) VALUES ($1, $2, $3, $4::date, $5, $6::numeric, $7, $8, 'credit_note', $9, $10::numeric) RETURNING id::text`,
			cid, number, customer, in.Date, currency, amount.String(), entry, reason, invID, arRate).Scan(&id); err != nil {
			return nil, err
		}
		paid, _ := decimal.NewFromString(paidS)
		if open := total.Sub(paid); open.IsPositive() {
			apply := decimal.Min(open, amount)
			if err := allocate(ctx, tx, cid, set, currency, openCredit{id: id, date: in.Date, rate: arRate, number: number},
				openDebit{id: invID, date: issue, number: invNumber}, apply, true); err != nil {
				return nil, err
			}
		}
		if err := autoAllocate(ctx, tx, cid, set, customer, currency, ""); err != nil {
			return nil, err
		}
		if in.RefundMethodID != "" {
			var unapplied string
			if err := tx.QueryRow(ctx, `SELECT (p.amount - coalesce((SELECT sum(a.amount) FROM payment_allocations a WHERE a.payment_id = p.id), 0))::text
				FROM payments p WHERE p.id = $1`, id).Scan(&unapplied); err != nil {
				return nil, err
			}
			if u, _ := decimal.NewFromString(unapplied); u.IsPositive() {
				if _, err := issueRefund(ctx, tx, cid, set, customer, name, currency, u, in.RefundMethodID, in.Date, "Credit note #"+fmt.Sprint(number)); err != nil {
					return nil, err
				}
			}
		}
		if err := reconnectIfCleared(ctx, tx, cid, customer); err != nil {
			return nil, err
		}
		return loadPayment(ctx, tx, cid, id)
	})
}

// ---------------- refunds ----------------

func customerCredit(ctx context.Context, tx pgx.Tx, customer, currency string) (decimal.Decimal, error) {
	var s string
	err := tx.QueryRow(ctx, `SELECT coalesce(sum(p.amount - coalesce((SELECT sum(a.amount) FROM payment_allocations a WHERE a.payment_id = p.id), 0)), 0)::text
		FROM payments p WHERE p.customer_id = $1 AND p.currency = $2 AND p.status = 'posted'`, customer, currency).Scan(&s)
	d, _ := decimal.NewFromString(s)
	return d, err
}

// issueRefund pays amount of the customer's credit (in currency) out of a cash box / bank.
func issueRefund(ctx context.Context, tx pgx.Tx, cid string, set *billingSettings, customer, name, currency string, amount decimal.Decimal,
	methodID, date, notes string) (string, error) {
	credit, err := customerCredit(ctx, tx, customer, currency)
	if err != nil {
		return "", err
	}
	if amount.GreaterThan(credit) {
		return "", &apiError{status: http.StatusUnprocessableEntity, code: "refund_exceeds_credit", detail: credit.String() + " " + currency}
	}
	methods, err := loadMethods(ctx, tx, cid)
	if err != nil {
		return "", err
	}
	m, ok := methods[methodID]
	if !ok || !m.Active {
		return "", badRequest("invalid_tender")
	}
	cRateS, err := resolveRate(ctx, tx, cid, set.Base, currency, date, "")
	if err != nil {
		return "", err
	}
	mRateS, err := resolveRate(ctx, tx, cid, set.Base, m.Currency, date, "")
	if err != nil {
		return "", err
	}
	mDec, err := currencyDecimals(ctx, tx, m.Currency)
	if err != nil {
		return "", err
	}
	cRate, _ := decimal.NewFromString(cRateS)
	mRate, _ := decimal.NewFromString(mRateS)
	paid := amount.Mul(mRate).Div(cRate).Round(mDec)
	var number int64
	if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'refund')`, cid).Scan(&number); err != nil {
		return "", err
	}
	je, err := postJournal(ctx, tx, cid, set, date, fmt.Sprintf("Refund #%d · %s", number, name), fmt.Sprintf("REF-%d", number), "refund",
		[]jline{
			{Account: *set.AR, Desc: name, Currency: currency, Debit: amount, Rate: cRateS},
			{Account: m.Account, Desc: "Refund · " + m.Name, Currency: m.Currency, Credit: paid, Rate: mRateS},
		})
	if err != nil {
		return "", err
	}
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO invoices (company_id, number, customer_id, issue_date, due_date, currency, total, journal_entry_id,
		notes, kind, refund_method_id) VALUES ($1, $2, $3, $4::date, $4::date, $5, $6::numeric, $7, $8, 'refund', $9) RETURNING id::text`,
		cid, number, customer, date, currency, amount.String(), je, notes, m.ID).Scan(&id); err != nil {
		return "", err
	}
	desc := fmt.Sprintf("Refund · %s %s %s", m.Name, paid.String(), m.Currency)
	if _, err := tx.Exec(ctx, `INSERT INTO invoice_lines (invoice_id, line_no, kind, description, quantity, unit_price, amount)
		VALUES ($1, 1, 'other', $2, 1, $3::numeric, $3::numeric)`, id, desc, amount.String()); err != nil {
		return "", err
	}
	return id, autoAllocate(ctx, tx, cid, set, customer, currency, "")
}

func (s *Server) createRefund(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Currency string `json:"currency"`
		Amount   string `json:"amount"`
		MethodID string `json:"method_id"`
		Date     string `json:"date"`
		Notes    string `json:"notes"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Date == "" {
		in.Date = today()
	}
	amt, ok := money(in.Amount)
	if !validDate(in.Date) || !currencyRe.MatchString(in.Currency) || !isUUID(in.MethodID) || !ok || !amt.IsPositive() {
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
		name, err := lockCustomer(ctx, tx, cid, r.PathValue("id"))
		if err != nil {
			return nil, err
		}
		id, err := issueRefund(ctx, tx, cid, set, r.PathValue("id"), name, in.Currency, amt, in.MethodID, in.Date, strings.TrimSpace(in.Notes))
		if err != nil {
			return nil, err
		}
		return loadInvoice(ctx, tx, cid, id)
	})
}

// voidRefund reverses a refund: its credit allocations are undone (credit is available again) and the entry reversed.
func voidRefund(ctx context.Context, tx pgx.Tx, cid string, set *billingSettings, id, customer, currency string, number int64, je *string, reason string) (*string, error) {
	rows, err := tx.Query(ctx, `SELECT a.fx_entry_id::text FROM payment_allocations a JOIN journal_entries e ON e.id = a.fx_entry_id
		WHERE a.invoice_id = $1 AND e.reversed_by IS NULL AND e.status = 'posted'`, id)
	if err != nil {
		return nil, err
	}
	fxs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	for _, fx := range fxs {
		if _, err := tx.Exec(ctx, `SELECT reverse_journal_entry($1, current_date, $2)`, fx, fmt.Sprintf("Void refund #%d · FX", number)); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO payment_allocations (payment_id, invoice_id, amount)
		SELECT payment_id, invoice_id, -sum(amount) FROM payment_allocations WHERE invoice_id = $1
		GROUP BY payment_id, invoice_id HAVING sum(amount) <> 0`, id); err != nil {
		return nil, err
	}
	var rev *string
	if je != nil {
		var rid string
		if err := tx.QueryRow(ctx, `SELECT reverse_journal_entry($1, current_date, $2)::text`, *je,
			strings.TrimSpace(fmt.Sprintf("Void refund #%d %s", number, reason))).Scan(&rid); err != nil {
			return nil, err
		}
		rev = &rid
	}
	if _, err := tx.Exec(ctx, `UPDATE invoices SET status = 'void', void_entry_id = $2,
		notes = CASE WHEN $3 = '' THEN notes ELSE trim(notes || ' ' || $3) END WHERE id = $1`, id, rev, reason); err != nil {
		return nil, err
	}
	return rev, autoAllocate(ctx, tx, cid, set, customer, currency, "")
}

// ---------------- supplier debit notes ----------------

func (s *Server) createDebitNote(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Amount string `json:"amount"` // total incl. tax; "" = everything not yet returned
		Date   string `json:"date"`
		Reason string `json:"reason"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Date == "" {
		in.Date = today()
	}
	cid, billID := r.PathValue("cid"), r.PathValue("id")
	if !validDate(in.Date) || !isUUID(billID) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		ps, err := loadPurchSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		if ps.AP == nil {
			return nil, notSetup("ap")
		}
		var billNo int64
		var supplier, currency, totalS, paidS, status, billDate string
		var je *string
		err = tx.QueryRow(ctx, `SELECT number, supplier_id::text, currency::text, total::text, paid::text, status, journal_entry_id::text,
			bill_date::text FROM bills WHERE id = $1 AND company_id = $2 FOR UPDATE`, billID, cid).
			Scan(&billNo, &supplier, &currency, &totalS, &paidS, &status, &je, &billDate)
		if err == pgx.ErrNoRows {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if status == "void" || je == nil {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if in.Date < billDate {
			return nil, badRequest("invalid_date")
		}
		name, err := lockSupplier(ctx, tx, cid, supplier)
		if err != nil {
			return nil, err
		}
		total, _ := decimal.NewFromString(totalS)
		var doneS string
		if err := tx.QueryRow(ctx, `SELECT coalesce(sum(amount), 0)::text FROM supplier_payments WHERE bill_id = $1 AND kind = 'debit_note'
			AND status = 'posted'`, billID).Scan(&doneS); err != nil {
			return nil, err
		}
		done, _ := decimal.NewFromString(doneS)
		left := total.Sub(done)
		amount := left
		if strings.TrimSpace(in.Amount) != "" {
			a, ok := money(in.Amount)
			if !ok {
				return nil, badRequest("invalid_amount")
			}
			amount = a
		}
		if !amount.IsPositive() || amount.GreaterThan(left) {
			return nil, &apiError{status: http.StatusUnprocessableEntity, code: "credit_exceeds_invoice", detail: left.String() + " " + currency}
		}
		dec, err := currencyDecimals(ctx, tx, currency)
		if err != nil {
			return nil, err
		}
		// bill lines: amount → line account (stock items: inventory, with quantity), tax → the tax's purchase account
		type part struct {
			account     string
			value       decimal.Decimal
			item, wh    *string
			qty         decimal.Decimal
			isStockLine bool
		}
		rows, err := tx.Query(ctx, `SELECT l.account_id::text, l.amount::text, l.tax_amount::text,
			coalesce((SELECT t.purchase_account_id::text FROM tax_rates t WHERE t.id = l.tax_rate_id), ''),
			CASE WHEN it.kind = 'stock' THEN l.item_id::text END, l.warehouse_id::text, l.quantity::text
			FROM bill_lines l LEFT JOIN items it ON it.id = l.item_id WHERE l.bill_id = $1 ORDER BY l.line_no`, billID)
		if err != nil {
			return nil, err
		}
		parts := []part{}
		for rows.Next() {
			var acc, amt, tax, taxAcc, qty string
			var item, wh *string
			if err := rows.Scan(&acc, &amt, &tax, &taxAcc, &item, &wh, &qty); err != nil {
				rows.Close()
				return nil, err
			}
			a, _ := decimal.NewFromString(amt)
			t, _ := decimal.NewFromString(tax)
			q, _ := decimal.NewFromString(qty)
			if a.IsPositive() {
				parts = append(parts, part{account: acc, value: a, item: item, wh: wh, qty: q, isStockLine: item != nil})
			}
			if t.IsPositive() && taxAcc != "" {
				parts = append(parts, part{account: taxAcc, value: t})
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		vals := make([]decimal.Decimal, len(parts))
		for i, p := range parts {
			vals[i] = p.value
		}
		split := prorate(vals, amount, dec)
		var rateS string
		if err := tx.QueryRow(ctx, `SELECT trim_scale(rate)::text FROM journal_lines WHERE entry_id = $1 AND account_id = $2 AND credit > 0 LIMIT 1`,
			*je, *ps.AP).Scan(&rateS); err != nil {
			return nil, err
		}
		rate, _ := decimal.NewFromString(rateS)
		var number int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'debit_note')`, cid).Scan(&number); err != nil {
			return nil, err
		}
		reason := strings.TrimSpace(in.Reason)
		desc := fmt.Sprintf("Debit note #%d · bill #%d", number, billNo)
		jl := []jline{{Account: *ps.AP, Desc: name, Currency: currency, Debit: amount, Rate: rateS}}
		for i, p := range parts {
			if split[i].IsPositive() {
				jl = append(jl, jline{Account: p.account, Desc: desc, Currency: currency, Credit: split[i], Rate: rateS})
			}
		}
		memo := desc + " · " + name
		if reason != "" {
			memo += " · " + reason
		}
		entry, err := postJournal(ctx, tx, cid, set, in.Date, memo, fmt.Sprintf("DN-%d", number), "debit_note", jl)
		if err != nil {
			return nil, err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO supplier_payments (company_id, number, supplier_id, payment_date, currency, amount,
			paid_currency, paid_amount, paid_rate, journal_entry_id, notes, kind, bill_id)
			VALUES ($1, $2, $3, $4::date, $5, $6::numeric, $5, $6::numeric, $7::numeric, $8, $9, 'debit_note', $10) RETURNING id::text`,
			cid, number, supplier, in.Date, currency, amount.String(), rateS, entry, reason, billID).Scan(&id); err != nil {
			return nil, err
		}
		// goods returned: take the stock back out at the value credited to inventory
		for i, p := range parts {
			if !p.isStockLine || !split[i].IsPositive() {
				continue
			}
			value := split[i].DivRound(rate, 4)
			qty := p.qty.Mul(split[i]).Div(p.value).Round(4)
			if !qty.IsPositive() {
				continue
			}
			have, _, err := lockStock(ctx, tx, *p.item, *p.wh)
			if err != nil {
				return nil, err
			}
			held, err := stockValueOf(ctx, tx, *p.item, *p.wh)
			if err != nil {
				return nil, err
			}
			if have.LessThan(qty) || held.LessThan(value) {
				var itemName string
				_ = tx.QueryRow(ctx, `SELECT name FROM items WHERE id = $1`, *p.item).Scan(&itemName)
				return nil, insufficientStock(itemName, have)
			}
			if err := stockOut(ctx, tx, cid, *p.item, *p.wh, qty, value.DivRound(qty, 6), value, in.Date, "debit_note", id, &entry, desc); err != nil {
				return nil, err
			}
		}
		paid, _ := decimal.NewFromString(paidS)
		if open := total.Sub(paid); open.IsPositive() {
			if _, err := tx.Exec(ctx, `INSERT INTO supplier_allocations (payment_id, bill_id, amount) VALUES ($1, $2, $3::numeric)`,
				id, billID, decimal.Min(open, amount).String()); err != nil {
				return nil, err
			}
		}
		if err := autoAllocateSupplier(ctx, tx, supplier, currency); err != nil {
			return nil, err
		}
		return loadBill(ctx, tx, cid, billID)
	})
}
