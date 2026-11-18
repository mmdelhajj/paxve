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

// creditLineIn: credit `quantity` of invoice line `line_no` (its price and tax in proportion).
type creditLineIn struct {
	LineNo   int    `json:"line_no"`
	Quantity string `json:"quantity"`
}

func (s *Server) createCreditNote(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Amount         string         `json:"amount"` // total incl. tax; "" = everything not yet credited (ignored with lines)
		Lines          []creditLineIn `json:"lines"`  // optional: credit these lines only (goods of these lines come back)
		Date           string         `json:"date"`
		Reason         string         `json:"reason"`
		RefundMethodID string         `json:"refund_method_id"` // optional: pay back at once the part the invoice does not need
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Date == "" {
		in.Date = today()
	}
	cid, invID := r.PathValue("cid"), r.PathValue("id")
	if !validDate(in.Date) || !isUUID(invID) || (in.RefundMethodID != "" && !isUUID(in.RefundMethodID)) || len(in.Lines) > 200 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		cn, err := creditInvoice(ctx, tx, cid, set, invID, in.Date, in.Reason, in.Amount, in.Lines, in.RefundMethodID)
		if err != nil {
			return nil, err
		}
		return loadPayment(ctx, tx, cid, cn.id)
	})
}

type creditResult struct {
	id         string          // the credit note (payments row)
	number     int64           // credit note number
	amount     decimal.Decimal // credited, incl. tax
	tax        decimal.Decimal
	refundID   string          // refund document, when money was paid back at once
	refundPaid decimal.Decimal // paid back, in the refund method's currency
	refundCur  string
}

// creditInvoice books a credit note on an invoice: by amount (prorated over the invoice's revenue and tax) or by
// lines (each line's own revenue account and tax; only the goods of those lines go back to stock).
func creditInvoice(ctx context.Context, tx pgx.Tx, cid string, set *billingSettings, invID, date, reasonIn, amountIn string,
	lineReq []creditLineIn, refundMethodID string) (*creditResult, error) {
	if err := set.requireAR(); err != nil {
		return nil, err
	}
	var number int64
	var customer, currency, totalS, paidS, status, kind, issue string
	var je *string
	err := tx.QueryRow(ctx, `SELECT number, customer_id::text, currency::text, total::text, paid::text, status, kind, journal_entry_id::text,
		issue_date::text FROM invoices WHERE id = $1 AND company_id = $2 FOR UPDATE`, invID, cid).
		Scan(&number, &customer, &currency, &totalS, &paidS, &status, &kind, &je, &issue)
	if err == pgx.ErrNoRows {
		return nil, notFound()
	}
	if err != nil {
		return nil, err
	}
	invNumber := number
	if status == "void" || kind != "invoice" || je == nil {
		return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
	}
	if date < issue {
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
	dec, err := currencyDecimals(ctx, tx, currency)
	if err != nil {
		return nil, err
	}
	lines, err := entryLines(ctx, tx, *je)
	if err != nil {
		return nil, err
	}
	arRate := ""
	for _, l := range lines {
		if l.account == *set.AR && l.debit.IsPositive() {
			arRate = l.rate
			break
		}
	}
	if arRate == "" {
		return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
	}

	type debit struct {
		account, rate string
		value         decimal.Decimal
	}
	debits := []debit{}
	addDebit := func(account, rate string, v decimal.Decimal) {
		if !v.IsPositive() {
			return
		}
		for i := range debits {
			if debits[i].account == account && debits[i].rate == rate {
				debits[i].value = debits[i].value.Add(v)
				return
			}
		}
		debits = append(debits, debit{account, rate, v})
	}
	type credLine struct {
		lineID    string
		qty, amt  decimal.Decimal
		tax       decimal.Decimal
		item, wh  *string
		stockItem bool
	}
	credLines := []credLine{}
	amount, taxTotal := decimal.Zero, decimal.Zero

	if len(lineReq) > 0 {
		// tax accounts of the invoice (a line without its own revenue account falls back to the invoice's revenue lines)
		type invLineRow struct {
			id                 string
			qty, amt, tax      decimal.Decimal
			cq, ca, ct         decimal.Decimal
			taxAcc, revAcc     *string
			item, wh, itemKind *string
		}
		rows, err := tx.Query(ctx, `SELECT l.id::text, l.line_no, l.quantity::text, l.amount::text, l.tax_amount::text,
			coalesce(c.q, 0)::text, coalesce(c.a, 0)::text, coalesce(c.t, 0)::text,
			(SELECT t.account_id::text FROM tax_rates t WHERE t.id = l.tax_rate_id), l.revenue_account_id::text,
			l.item_id::text, l.warehouse_id::text, it.kind
			FROM invoice_lines l LEFT JOIN items it ON it.id = l.item_id
			LEFT JOIN LATERAL (SELECT sum(cl.quantity) q, sum(cl.amount) a, sum(cl.tax_amount) t FROM credit_note_lines cl
			  JOIN payments p ON p.id = cl.payment_id AND p.status = 'posted' WHERE cl.invoice_line_id = l.id) c ON true
			WHERE l.invoice_id = $1`, invID)
		if err != nil {
			return nil, err
		}
		byNo := map[int]invLineRow{}
		for rows.Next() {
			var x invLineRow
			var no int
			var q, a, t, cq, ca, ct string
			if err := rows.Scan(&x.id, &no, &q, &a, &t, &cq, &ca, &ct, &x.taxAcc, &x.revAcc, &x.item, &x.wh, &x.itemKind); err != nil {
				rows.Close()
				return nil, err
			}
			x.qty, _ = decimal.NewFromString(q)
			x.amt, _ = decimal.NewFromString(a)
			x.tax, _ = decimal.NewFromString(t)
			x.cq, _ = decimal.NewFromString(cq)
			x.ca, _ = decimal.NewFromString(ca)
			x.ct, _ = decimal.NewFromString(ct)
			byNo[no] = x
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		// revenue / tax credit lines of the invoice entry, for lines without a stored revenue account
		taxAccs := map[string]bool{}
		for _, x := range byNo {
			if x.taxAcc != nil {
				taxAccs[*x.taxAcc] = true
			}
		}
		revParts, revSrc := []decimal.Decimal{}, []jeLine{}
		for _, l := range lines {
			if l.credit.IsPositive() && l.currency == currency && l.account != *set.AR && !taxAccs[l.account] {
				revParts = append(revParts, l.credit)
				revSrc = append(revSrc, l)
			}
		}
		seen := map[int]bool{}
		for _, req := range lineReq {
			x, ok := byNo[req.LineNo]
			q, okq := money(req.Quantity)
			if !ok || !okq || !q.IsPositive() || seen[req.LineNo] {
				return nil, badRequest("invalid_line")
			}
			seen[req.LineNo] = true
			remaining := x.qty.Sub(x.cq)
			if q.GreaterThan(remaining) {
				return nil, &apiError{status: http.StatusUnprocessableEntity, code: "credit_exceeds_invoice",
					detail: fmt.Sprintf("#%d: %s", req.LineNo, remaining.String())}
			}
			var amt, tax decimal.Decimal
			if q.Equal(remaining) {
				amt, tax = x.amt.Sub(x.ca), x.tax.Sub(x.ct)
			} else {
				amt = x.amt.Mul(q).Div(x.qty).Round(dec)
				tax = x.tax.Mul(q).Div(x.qty).Round(dec)
			}
			if amt.IsNegative() {
				amt = decimal.Zero
			}
			if tax.IsNegative() {
				tax = decimal.Zero
			}
			if x.revAcc != nil {
				addDebit(*x.revAcc, arRate, amt)
			} else if len(revSrc) > 0 {
				for i, p := range prorate(revParts, amt, dec) {
					addDebit(revSrc[i].account, revSrc[i].rate, p)
				}
			} else {
				return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
			}
			if tax.IsPositive() {
				if x.taxAcc == nil {
					return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
				}
				addDebit(*x.taxAcc, arRate, tax)
			}
			amount = amount.Add(amt).Add(tax)
			taxTotal = taxTotal.Add(tax)
			credLines = append(credLines, credLine{lineID: x.id, qty: q, amt: amt, tax: tax, item: x.item, wh: x.wh,
				stockItem: x.itemKind != nil && *x.itemKind == "stock" && x.wh != nil})
		}
		if !amount.IsPositive() || amount.GreaterThan(left) {
			return nil, &apiError{status: http.StatusUnprocessableEntity, code: "credit_exceeds_invoice", detail: left.String() + " " + currency}
		}
	} else {
		amount = left
		if strings.TrimSpace(amountIn) != "" {
			a, ok := money(amountIn)
			if !ok {
				return nil, badRequest("invalid_amount")
			}
			amount = a
		}
		if !amount.IsPositive() || amount.GreaterThan(left) {
			return nil, &apiError{status: http.StatusUnprocessableEntity, code: "credit_exceeds_invoice", detail: left.String() + " " + currency}
		}
		parts, src := []decimal.Decimal{}, []jeLine{}
		for _, l := range lines {
			if l.account == *set.AR {
				continue
			}
			if l.credit.IsPositive() && l.currency == currency {
				parts = append(parts, l.credit)
				src = append(src, l)
			}
		}
		if len(parts) == 0 {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		for i, p := range prorate(parts, amount, dec) {
			addDebit(src[i].account, src[i].rate, p)
		}
	}

	if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'credit_note')`, cid).Scan(&number); err != nil {
		return nil, err
	}
	reason := strings.TrimSpace(reasonIn)
	desc := fmt.Sprintf("Credit note #%d · invoice #%d", number, invNumber)
	jl := []jline{}
	for _, d := range debits {
		jl = append(jl, jline{Account: d.account, Desc: desc, Currency: currency, Debit: d.value, Rate: d.rate})
	}
	jl = append(jl, jline{Account: *set.AR, Desc: name, Currency: currency, Credit: amount, Rate: arRate})
	memo := desc + " · " + name
	if reason != "" {
		memo += " · " + reason
	}
	entry, err := postJournal(ctx, tx, cid, set, date, memo, fmt.Sprintf("CN-%d", number), "credit_note", jl)
	if err != nil {
		return nil, err
	}
	res := &creditResult{number: number, amount: amount, tax: taxTotal}
	if err := tx.QueryRow(ctx, `INSERT INTO payments (company_id, number, customer_id, payment_date, currency, amount, journal_entry_id,
		notes, kind, invoice_id, ar_rate) VALUES ($1, $2, $3, $4::date, $5, $6::numeric, $7, $8, 'credit_note', $9, $10::numeric) RETURNING id::text`,
		cid, number, customer, date, currency, amount.String(), entry, reason, invID, arRate).Scan(&res.id); err != nil {
		return nil, err
	}
	if len(lineReq) > 0 {
		back := map[string]decimal.Decimal{}
		for _, c := range credLines {
			if _, err := tx.Exec(ctx, `INSERT INTO credit_note_lines (payment_id, invoice_line_id, quantity, amount, tax_amount)
				VALUES ($1, $2, $3::numeric, $4::numeric, $5::numeric)`, res.id, c.lineID, c.qty.String(), c.amt.String(), c.tax.String()); err != nil {
				return nil, err
			}
			if c.stockItem {
				k := *c.item + "|" + *c.wh
				back[k] = back[k].Add(c.qty)
			}
		}
		if err := returnSaleStock(ctx, tx, cid, set, invID, res.id, number, invNumber, date, false, decimal.Zero, back); err != nil {
			return nil, err
		}
	} else if err := returnSaleStock(ctx, tx, cid, set, invID, res.id, number, invNumber, date, amount.Equal(left), amount.Div(total), nil); err != nil {
		return nil, err
	}
	paid, _ := decimal.NewFromString(paidS)
	if open := total.Sub(paid); open.IsPositive() {
		apply := decimal.Min(open, amount)
		if err := allocate(ctx, tx, cid, set, currency, openCredit{id: res.id, date: date, rate: arRate, number: number},
			openDebit{id: invID, date: issue, number: invNumber}, apply, true); err != nil {
			return nil, err
		}
	}
	if err := autoAllocate(ctx, tx, cid, set, customer, currency, ""); err != nil {
		return nil, err
	}
	if refundMethodID != "" {
		var unapplied string
		if err := tx.QueryRow(ctx, `SELECT (p.amount - coalesce((SELECT sum(a.amount) FROM payment_allocations a WHERE a.payment_id = p.id), 0))::text
			FROM payments p WHERE p.id = $1`, res.id).Scan(&unapplied); err != nil {
			return nil, err
		}
		if u, _ := decimal.NewFromString(unapplied); u.IsPositive() {
			id, paidOut, cur, err := issueRefund(ctx, tx, cid, set, customer, name, currency, u, refundMethodID, date, "Credit note #"+fmt.Sprint(number))
			if err != nil {
				return nil, err
			}
			res.refundID, res.refundPaid, res.refundCur = id, paidOut, cur
		}
	}
	if err := reconnectIfCleared(ctx, tx, cid, customer); err != nil {
		return nil, err
	}
	return res, nil
}

// returnSaleStock: a credit note on an invoice that sold stock items brings goods back into stock at the cost they
// left with (Dr inventory / Cr cost of sales). byQty (item|warehouse → quantity): return exactly those quantities
// (credit note by lines). Otherwise all: this credit note completes the invoice, so everything not yet returned
// comes back (no rounding residue); else ratio = credited amount / invoice total.
func returnSaleStock(ctx context.Context, tx pgx.Tx, cid string, set *billingSettings, invID, cnID string, cnNumber, invNumber int64,
	date string, all bool, ratio decimal.Decimal, byQty map[string]decimal.Decimal) error {
	if byQty != nil && len(byQty) == 0 {
		return nil
	}
	type mv struct {
		item, wh, name, kind string
		inv, exp             *string
		qty, value           decimal.Decimal // still out (positive)
		soldQty, soldValue   decimal.Decimal
	}
	rows, err := tx.Query(ctx, `SELECT m.item_id::text, m.warehouse_id::text, it.name, it.kind, it.inventory_account_id::text, it.expense_account_id::text,
		(-sum(m.quantity))::text, (-sum(m.value))::text,
		(-sum(m.quantity) FILTER (WHERE m.source = 'sale'))::text, (-sum(m.value) FILTER (WHERE m.source = 'sale'))::text
		FROM stock_moves m JOIN items it ON it.id = m.item_id
		WHERE (m.source = 'sale' AND m.source_id = $1)
		   OR (m.source IN ('credit_note', 'credit_note_void')
		       AND m.source_id IN (SELECT p.id FROM payments p WHERE p.invoice_id = $1 AND p.kind = 'credit_note'))
		GROUP BY m.item_id, m.warehouse_id, it.name, it.kind, it.inventory_account_id, it.expense_account_id
		HAVING sum(m.quantity) FILTER (WHERE m.source = 'sale') < 0`, invID)
	if err != nil {
		return err
	}
	moves, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (mv, error) {
		var m mv
		var q, v, sq, sv string
		err := row.Scan(&m.item, &m.wh, &m.name, &m.kind, &m.inv, &m.exp, &q, &v, &sq, &sv)
		m.qty, _ = decimal.NewFromString(q)
		m.value, _ = decimal.NewFromString(v)
		m.soldQty, _ = decimal.NewFromString(sq)
		m.soldValue, _ = decimal.NewFromString(sv)
		return m, err
	})
	if err != nil || len(moves) == 0 {
		return err
	}
	ps, err := loadPurchSettings(ctx, tx, cid)
	if err != nil {
		return err
	}
	type back struct {
		m          mv
		qty, value decimal.Decimal
	}
	backs := []back{}
	jl := []jline{}
	for _, m := range moves {
		if !m.qty.IsPositive() || m.inv == nil {
			continue
		}
		q, v := m.qty, m.value
		if byQty != nil {
			want, ok := byQty[m.item+"|"+m.wh]
			if !ok {
				continue
			}
			if want.LessThan(m.qty) {
				q = want
				v = decimal.Zero
				if m.soldQty.IsPositive() {
					v = decimal.Min(m.soldValue.Mul(want).Div(m.soldQty).Round(4), m.value)
				}
			}
		} else if !all {
			q = decimal.Min(m.soldQty.Mul(ratio).Round(4), m.qty)
			v = decimal.Min(m.soldValue.Mul(ratio).Round(4), m.value)
		}
		if !q.IsPositive() {
			continue
		}
		cogs := ""
		if m.exp != nil {
			cogs = *m.exp
		} else if ps.COGS != nil {
			cogs = *ps.COGS
		}
		if cogs == "" {
			return notSetup("cogs")
		}
		backs = append(backs, back{m, q, v})
		if v.IsPositive() {
			jl = append(jl, jline{Account: *m.inv, Desc: m.name, Currency: set.Base, Debit: v},
				jline{Account: cogs, Desc: m.name, Currency: set.Base, Credit: v})
		}
	}
	var je *string
	if len(jl) > 0 {
		id, err := postJournal(ctx, tx, cid, set, date, fmt.Sprintf("Goods returned - credit note #%d, invoice #%d", cnNumber, invNumber),
			fmt.Sprintf("CN-%d", cnNumber), "sale", jl)
		if err != nil {
			return err
		}
		je = &id
	}
	for _, b := range backs {
		if err := stockIn(ctx, tx, cid, b.m.item, b.m.wh, b.qty, b.value, date, "credit_note", cnID, je,
			fmt.Sprintf("Credit note #%d (invoice #%d)", cnNumber, invNumber)); err != nil {
			return err
		}
	}
	return nil
}

// voidCreditNoteStock takes back out the goods a voided credit note had returned, and reverses that cost entry.
func voidCreditNoteStock(ctx context.Context, tx pgx.Tx, cid, cnID string, number int64) error {
	var je *string
	err := tx.QueryRow(ctx, `SELECT journal_entry_id::text FROM stock_moves WHERE source = 'credit_note' AND source_id = $1 LIMIT 1`, cnID).Scan(&je)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	var rev *string
	if je != nil {
		var rid string
		if err := tx.QueryRow(ctx, `SELECT reverse_journal_entry($1, current_date, $2)::text`, *je,
			fmt.Sprintf("Void credit note #%d - goods", number)).Scan(&rid); err != nil {
			return err
		}
		rev = &rid
	}
	return reverseStockMoves(ctx, tx, cid, "credit_note", "credit_note_void", cnID, rev, fmt.Sprintf("Void credit note #%d", number))
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
	methodID, date, notes string) (string, decimal.Decimal, string, error) {
	credit, err := customerCredit(ctx, tx, customer, currency)
	if err != nil {
		return "", decimal.Zero, "", err
	}
	if amount.GreaterThan(credit) {
		return "", decimal.Zero, "", &apiError{status: http.StatusUnprocessableEntity, code: "refund_exceeds_credit", detail: credit.String() + " " + currency}
	}
	methods, err := loadMethods(ctx, tx, cid)
	if err != nil {
		return "", decimal.Zero, "", err
	}
	m, ok := methods[methodID]
	if !ok || !m.Active {
		return "", decimal.Zero, "", badRequest("invalid_tender")
	}
	cRateS, err := resolveRate(ctx, tx, cid, set.Base, currency, date, "")
	if err != nil {
		return "", decimal.Zero, "", err
	}
	mRateS, err := resolveRate(ctx, tx, cid, set.Base, m.Currency, date, "")
	if err != nil {
		return "", decimal.Zero, "", err
	}
	mDec, err := currencyDecimals(ctx, tx, m.Currency)
	if err != nil {
		return "", decimal.Zero, "", err
	}
	cRate, _ := decimal.NewFromString(cRateS)
	mRate, _ := decimal.NewFromString(mRateS)
	paid := amount.Mul(mRate).Div(cRate).Round(mDec)
	var number int64
	if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'refund')`, cid).Scan(&number); err != nil {
		return "", decimal.Zero, "", err
	}
	je, err := postJournal(ctx, tx, cid, set, date, fmt.Sprintf("Refund #%d · %s", number, name), fmt.Sprintf("REF-%d", number), "refund",
		[]jline{
			{Account: *set.AR, Desc: name, Currency: currency, Debit: amount, Rate: cRateS},
			{Account: m.Account, Desc: "Refund · " + m.Name, Currency: m.Currency, Credit: paid, Rate: mRateS},
		})
	if err != nil {
		return "", decimal.Zero, "", err
	}
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO invoices (company_id, number, customer_id, issue_date, due_date, currency, total, journal_entry_id,
		notes, kind, refund_method_id) VALUES ($1, $2, $3, $4::date, $4::date, $5, $6::numeric, $7, $8, 'refund', $9) RETURNING id::text`,
		cid, number, customer, date, currency, amount.String(), je, notes, m.ID).Scan(&id); err != nil {
		return "", decimal.Zero, "", err
	}
	desc := fmt.Sprintf("Refund · %s %s %s", m.Name, paid.String(), m.Currency)
	if _, err := tx.Exec(ctx, `INSERT INTO invoice_lines (invoice_id, line_no, kind, description, quantity, unit_price, amount)
		VALUES ($1, 1, 'other', $2, 1, $3::numeric, $3::numeric)`, id, desc, amount.String()); err != nil {
		return "", decimal.Zero, "", err
	}
	return id, paid, m.Currency, autoAllocate(ctx, tx, cid, set, customer, currency, "")
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
		id, _, _, err := issueRefund(ctx, tx, cid, set, r.PathValue("id"), name, in.Currency, amt, in.MethodID, in.Date, strings.TrimSpace(in.Notes))
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
		if err := autoAllocateSupplier(ctx, tx, cid, supplier, currency); err != nil {
			return nil, err
		}
		return loadBill(ctx, tx, cid, billID)
	})
}
