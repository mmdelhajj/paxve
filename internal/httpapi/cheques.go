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

// Cheques, like Magic's cheque vouchers. Received cheques (in) and cheques we write (out), with every step:
//
//	received  in_hand ──deposit──> deposited ──collect──> collected
//	             │  └──collect (cashed at the counter)──────────┘
//	             ├──endorse──> endorsed (given to a supplier) ──endorse_return──> in_hand
//	             ├──partial_cash (part replaced by cash) ──> … settled when nothing is left
//	             ├──return (given back) ──> returned
//	             └──bounce (from in_hand / deposited / collected) ──> bounced
//	issued    issued ──pay (bank paid it)──> paid ;  issued ──cancel / bounce──> cancelled / bounced
//
// Accounts: received cheques sit in "cheques in hand" (CIH), then "cheques under collection" (CUC) once deposited;
// issued cheques sit in "cheques payable" (CP) until the bank pays them. A cheque from a customer is a customer
// payment (through a hidden "cheque" box on CIH), so it settles invoices like cash; a bounce or return puts the
// amount back on the customer's account as a debit (shown on the statement). A cheque to a supplier is a supplier payment.

const chequeCols = `ch.id, ch.number, ch.direction, ch.cheque_no, ch.bank_name, ch.drawer, ch.receive_date, ch.due_date, ch.currency,
	ch.amount::text AS amount, ch.remaining::text AS remaining, ch.status, ch.frozen, ch.customer_id, c.name AS customer_name, c.code AS customer_code,
	ch.supplier_id, su.name AS supplier_name, ch.account_id, a.code AS account_code, a.name AS account_name,
	ch.bank_method_id, bm.name AS bank_name_box, ch.endorsed_supplier_id, es.name AS endorsed_to, ch.payment_id, ch.supplier_payment_id,
	ch.journal_entry_id, ch.notes, (ch.due_date - current_date) AS days_to_due, u.full_name AS created_by, ch.created_at`

const chequeFrom = ` FROM cheques ch LEFT JOIN customers c ON c.id = ch.customer_id LEFT JOIN suppliers su ON su.id = ch.supplier_id
	LEFT JOIN accounts a ON a.id = ch.account_id LEFT JOIN payment_methods bm ON bm.id = ch.bank_method_id
	LEFT JOIN suppliers es ON es.id = ch.endorsed_supplier_id LEFT JOIN users u ON u.id = ch.created_by`

// GET /companies/{cid}/cheques?direction&status&from&to (dates on the due date)
func (s *Server) listCheques(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	dir, status := q.Get("direction"), q.Get("status")
	from, to := q.Get("from"), q.Get("to")
	if from == "" {
		from = "0001-01-01"
	}
	if to == "" {
		to = "9999-12-31"
	}
	if (dir != "" && dir != "in" && dir != "out") || !validDate(from) || !validDate(to) || len(status) > 20 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		cond := `ch.company_id = $1 AND ($2 = '' OR ch.direction = $2) AND ch.due_date BETWEEN $4::date AND $5::date
			AND ($3 = '' OR ch.status = $3 OR ($3 = 'open' AND ch.status IN ('in_hand', 'deposited', 'issued')))`
		rows, err := queryJSON(ctx, tx, `SELECT `+chequeCols+chequeFrom+` WHERE `+cond+` ORDER BY ch.due_date, ch.number LIMIT 2000`,
			cid, dir, status, from, to)
		if err != nil {
			return nil, err
		}
		totals, err := queryJSON(ctx, tx, `SELECT ch.direction, ch.status, ch.currency, count(*) AS cheques, sum(ch.remaining)::text AS amount
			FROM cheques ch WHERE `+cond+` GROUP BY 1, 2, 3 ORDER BY 1, 2, 3`, cid, dir, status, from, to)
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"rows": rows, "totals": totals}, nil
	})
}

func loadCheque(ctx context.Context, tx pgx.Tx, cid, id string) (any, error) {
	if !isUUID(id) {
		return nil, notFound()
	}
	ch, err := queryJSONOne(ctx, tx, `SELECT `+chequeCols+chequeFrom+` WHERE ch.id = $1 AND ch.company_id = $2`, id, cid)
	if err != nil {
		return nil, err
	}
	events, err := queryJSON(ctx, tx, `SELECT e.id, e.kind, e.event_date, e.amount::text AS amount, e.status_before, e.status_after,
			m.name AS method, s.name AS supplier, e.journal_entry_id, e.payment_id, e.supplier_payment_id, e.invoice_id, e.undone,
			e.notes, u.full_name AS created_by, e.created_at
		FROM cheque_events e LEFT JOIN payment_methods m ON m.id = e.method_id LEFT JOIN suppliers s ON s.id = e.supplier_id
		LEFT JOIN users u ON u.id = e.created_by
		WHERE e.cheque_id = $1 ORDER BY e.created_at`, id)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"cheque": ch, "events": events}, nil
}

func (s *Server) getCheque(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return loadCheque(ctx, tx, r.PathValue("cid"), r.PathValue("id"))
	})
}

// chequeBox: the hidden payment method ("cheque" kind) on account acc for currency cur, created on first use.
func chequeBox(ctx context.Context, tx pgx.Tx, cid, acc, cur, name string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM payment_methods WHERE company_id = $1 AND kind = 'cheque' AND account_id = $2 AND currency = $3
		ORDER BY created_at LIMIT 1`, cid, acc, cur).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != pgx.ErrNoRows {
		return "", err
	}
	err = tx.QueryRow(ctx, `INSERT INTO payment_methods (company_id, name, kind, currency, account_id, is_active, sort)
		VALUES ($1, $2, 'cheque', $3, $4, false, 900) RETURNING id::text`, cid, name+" "+cur, cur, acc).Scan(&id)
	return id, err
}

func chequeEvent(ctx context.Context, tx pgx.Tx, cid, chequeID, kind, date string, amount decimal.Decimal, before, after string,
	method, supplier, je, payment, spay, invoice *string, notes string) error {
	_, err := tx.Exec(ctx, `INSERT INTO cheque_events (company_id, cheque_id, kind, event_date, amount, status_before, status_after,
		method_id, supplier_id, journal_entry_id, payment_id, supplier_payment_id, invoice_id, notes)
		VALUES ($1, $2, $3, $4::date, $5::numeric, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		cid, chequeID, kind, date, amount.String(), before, after, method, supplier, je, payment, spay, invoice, strings.TrimSpace(notes))
	return err
}

// POST /companies/{cid}/cheques: record a cheque received (from a customer, or against any account) or written
// (to a supplier, or against any account).
func (s *Server) createCheque(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Direction   string `json:"direction"`
		ChequeNo    string `json:"cheque_no"`
		BankName    string `json:"bank_name"`
		Drawer      string `json:"drawer"`
		ReceiveDate string `json:"receive_date"`
		DueDate     string `json:"due_date"`
		Currency    string `json:"currency"`
		Amount      string `json:"amount"`
		CustomerID  string `json:"customer_id"`
		SupplierID  string `json:"supplier_id"`
		AccountID   string `json:"account_id"`
		Notes       string `json:"notes"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.ReceiveDate == "" {
		in.ReceiveDate = today()
	}
	if in.DueDate == "" {
		in.DueDate = in.ReceiveDate
	}
	in.Currency = strings.ToUpper(strings.TrimSpace(in.Currency))
	amt, ok := money(in.Amount)
	parties := 0
	for _, p := range []string{in.CustomerID, in.SupplierID, in.AccountID} {
		if p != "" {
			parties++
			if !isUUID(p) {
				parties = 9
			}
		}
	}
	if (in.Direction != "in" && in.Direction != "out") || !validDate(in.ReceiveDate) || !validDate(in.DueDate) ||
		!currencyRe.MatchString(in.Currency) || !ok || !amt.IsPositive() || parties != 1 ||
		(in.Direction == "in" && in.SupplierID != "") || (in.Direction == "out" && in.CustomerID != "") ||
		len(in.ChequeNo) > 60 || len(in.BankName) > 120 || len(in.Drawer) > 120 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	me := principal(r).ID
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		as, err := loadAcctSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		var number int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'cheque')`, cid).Scan(&number); err != nil {
			return nil, err
		}
		label := fmt.Sprintf("Cheque #%d %s %s", number, strings.TrimSpace(in.ChequeNo), strings.TrimSpace(in.BankName))
		var payID, spayID, je *string
		status := "in_hand"
		if in.Direction == "out" {
			status = "issued"
		}
		switch {
		case in.CustomerID != "":
			box, err := chequeBox(ctx, tx, cid, as.CIH, in.Currency, "Cheques in hand")
			if err != nil {
				return nil, err
			}
			id, err := receivePayment(ctx, tx, cid, paymentIn{CustomerID: in.CustomerID, Date: in.ReceiveDate, Currency: in.Currency,
				Tenders: []tenderIn{{MethodID: box, Amount: amt.String()}}, Notes: strings.TrimSpace(label + " " + in.Notes), allowCheque: true}, false, me)
			if err != nil {
				return nil, err
			}
			payID = &id
		case in.SupplierID != "":
			box, err := chequeBox(ctx, tx, cid, as.CP, in.Currency, "Cheques issued")
			if err != nil {
				return nil, err
			}
			id, err := paySupplier(ctx, tx, cid, supPayIn{SupplierID: in.SupplierID, Date: in.ReceiveDate, Currency: in.Currency,
				MethodID: box, PaidAmount: amt.String(), Notes: strings.TrimSpace(label + " " + in.Notes), allowCheque: true})
			if err != nil {
				return nil, err
			}
			spayID = &id
		default:
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM accounts WHERE id = $1 AND company_id = $2 AND is_postable)`,
				in.AccountID, cid).Scan(&ok); err != nil {
				return nil, err
			}
			if !ok {
				return nil, badRequest("invalid_account")
			}
			hold := as.CIH
			if in.Direction == "out" {
				hold = as.CP
			}
			lines := []jline{
				{Account: hold, Desc: label, Currency: in.Currency, Debit: amt},
				{Account: in.AccountID, Desc: label, Currency: in.Currency, Credit: amt},
			}
			if in.Direction == "out" {
				lines[0].Debit, lines[0].Credit = decimal.Zero, amt
				lines[1].Debit, lines[1].Credit = amt, decimal.Zero
			}
			id, err := postJournal(ctx, tx, cid, set, in.ReceiveDate, label, fmt.Sprintf("CHQ-%d", number), "cheque", lines)
			if err != nil {
				return nil, err
			}
			je = &id
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO cheques (company_id, number, direction, cheque_no, bank_name, drawer, receive_date, due_date,
			currency, amount, remaining, customer_id, supplier_id, account_id, status, payment_id, supplier_payment_id, journal_entry_id, notes)
			VALUES ($1, $2, $3, $4, $5, $6, $7::date, $8::date, $9, $10::numeric, $10::numeric, nullif($11, '')::uuid, nullif($12, '')::uuid,
			        nullif($13, '')::uuid, $14, $15, $16, $17, $18) RETURNING id::text`,
			cid, number, in.Direction, strings.TrimSpace(in.ChequeNo), strings.TrimSpace(in.BankName), strings.TrimSpace(in.Drawer),
			in.ReceiveDate, in.DueDate, in.Currency, amt.String(), in.CustomerID, in.SupplierID, in.AccountID, status, payID, spayID, je,
			strings.TrimSpace(in.Notes)).Scan(&id); err != nil {
			return nil, err
		}
		kind := "receive"
		if in.Direction == "out" {
			kind = "issue"
		}
		if err := chequeEvent(ctx, tx, cid, id, kind, in.ReceiveDate, amt, "", status, nil, nil, je, payID, spayID, nil, in.Notes); err != nil {
			return nil, err
		}
		return loadCheque(ctx, tx, cid, id)
	})
}

type chequeRow struct {
	ID, Direction, Status, Currency, Date, ChequeNo string
	Rate                                            string // rate at which the cheque entered the cheque account ("" = official)
	Number                                          int64
	Amount, Remaining                               decimal.Decimal
	Frozen                                          bool
	CustomerID, SupplierID, AccountID, BankMethod   *string
	PaymentID, SupplierPaymentID, JournalEntryID    *string
}

func lockCheque(ctx context.Context, tx pgx.Tx, cid, id string) (*chequeRow, error) {
	if !isUUID(id) {
		return nil, notFound()
	}
	c := &chequeRow{ID: id}
	var amt, rem string
	err := tx.QueryRow(ctx, `SELECT ch.number, ch.direction, ch.status, ch.currency::text, ch.cheque_no, ch.amount::text, ch.remaining::text, ch.frozen,
		ch.customer_id::text, ch.supplier_id::text, ch.account_id::text, ch.bank_method_id::text, ch.payment_id::text, ch.supplier_payment_id::text,
		ch.journal_entry_id::text,
		coalesce((SELECT trim_scale(l.rate)::text FROM journal_lines l
		  WHERE l.entry_id = coalesce(ch.journal_entry_id, (SELECT p.journal_entry_id FROM payments p WHERE p.id = ch.payment_id),
		                              (SELECT sp.journal_entry_id FROM supplier_payments sp WHERE sp.id = ch.supplier_payment_id))
		    AND l.currency = ch.currency
		    AND l.account_id IN (SELECT co.cheque_in_hand_account_id FROM companies co WHERE co.id = ch.company_id
		                         UNION SELECT co.cheque_payable_account_id FROM companies co WHERE co.id = ch.company_id)
		  LIMIT 1), '')
		FROM cheques ch WHERE ch.id = $1 AND ch.company_id = $2 FOR UPDATE OF ch`, id, cid).Scan(
		&c.Number, &c.Direction, &c.Status, &c.Currency, &c.ChequeNo, &amt, &rem, &c.Frozen,
		&c.CustomerID, &c.SupplierID, &c.AccountID, &c.BankMethod, &c.PaymentID, &c.SupplierPaymentID, &c.JournalEntryID, &c.Rate)
	if err == pgx.ErrNoRows {
		return nil, notFound()
	}
	c.Amount, _ = decimal.NewFromString(amt)
	c.Remaining, _ = decimal.NewFromString(rem)
	return c, err
}

// chequeDebit puts amount back on the customer's account (bounced / returned cheque): a receivable debit
// (stored like a refund so statements, ageing and balances include it), credited to the account holding the cheque.
func chequeDebit(ctx context.Context, tx pgx.Tx, cid string, set *billingSettings, c *chequeRow, holding, holdRate, date, what string) (string, error) {
	if err := set.requireAR(); err != nil {
		return "", err
	}
	name, err := lockCustomer(ctx, tx, cid, *c.CustomerID)
	if err != nil {
		return "", err
	}
	var number int64
	if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'refund')`, cid).Scan(&number); err != nil {
		return "", err
	}
	desc := fmt.Sprintf("%s · cheque %s (#%d)", what, c.ChequeNo, c.Number)
	je, err := postJournal(ctx, tx, cid, set, date, desc+" · "+name, fmt.Sprintf("CHQ-%d", c.Number), "cheque", []jline{
		{Account: *set.AR, Desc: name, Currency: c.Currency, Debit: c.Remaining},
		{Account: holding, Desc: desc, Currency: c.Currency, Credit: c.Remaining, Rate: holdRate},
	})
	if err != nil {
		return "", err
	}
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO invoices (company_id, number, customer_id, issue_date, due_date, currency, total, journal_entry_id,
		notes, kind) VALUES ($1, $2, $3, $4::date, $4::date, $5, $6::numeric, $7, $8, 'refund') RETURNING id::text`,
		cid, number, *c.CustomerID, date, c.Currency, c.Remaining.String(), je, desc).Scan(&id); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO invoice_lines (invoice_id, line_no, kind, description, quantity, unit_price, amount)
		VALUES ($1, 1, 'other', $2, 1, $3::numeric, $3::numeric)`, id, desc, c.Remaining.String()); err != nil {
		return "", err
	}
	return id, autoAllocate(ctx, tx, cid, set, *c.CustomerID, c.Currency, "")
}

// POST /companies/{cid}/cheques/{id}/{op} {date, method_id, supplier_id, amount, notes}
func (s *Server) chequeAction(w http.ResponseWriter, r *http.Request) {
	op := r.PathValue("op")
	var in struct {
		Date       string `json:"date"`
		MethodID   string `json:"method_id"`
		SupplierID string `json:"supplier_id"`
		Amount     string `json:"amount"`
		Notes      string `json:"notes"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Date == "" {
		in.Date = today()
	}
	if !validDate(in.Date) || (in.MethodID != "" && !isUUID(in.MethodID)) || (in.SupplierID != "" && !isUUID(in.SupplierID)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	known := map[string]bool{"deposit": true, "collect": true, "endorse": true, "endorse_return": true, "bounce": true, "return": true,
		"partial_cash": true, "freeze": true, "unfreeze": true, "pay": true, "cancel": true, "undo": true}
	if !known[op] {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		c, err := lockCheque(ctx, tx, cid, r.PathValue("id"))
		if err != nil {
			return nil, err
		}
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		as, err := loadAcctSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		wrong := func() error { return &apiError{status: http.StatusConflict, code: "wrong_status", detail: c.Status} }
		in_ := c.Direction == "in"
		label := fmt.Sprintf("Cheque #%d %s", c.Number, c.ChequeNo)
		box := func(required bool) (*method, error) {
			id := in.MethodID
			if id == "" && c.BankMethod != nil {
				id = *c.BankMethod
			}
			if id == "" {
				if required {
					return nil, badRequest("method_required")
				}
				return nil, nil
			}
			methods, err := loadMethods(ctx, tx, cid)
			if err != nil {
				return nil, err
			}
			m, ok := methods[id]
			if !ok || !m.Active || m.Kind == "cheque" {
				return nil, badRequest("invalid_tender")
			}
			if m.Currency != c.Currency {
				return nil, badRequest("currency_mismatch")
			}
			return &m, nil
		}
		post := func(memo string, lines []jline) (*string, error) {
			id, err := postJournal(ctx, tx, cid, set, in.Date, memo, fmt.Sprintf("CHQ-%d", c.Number), "cheque", lines)
			return &id, err
		}
		update := func(status string, remaining decimal.Decimal, extra string, args ...any) error {
			_, err := tx.Exec(ctx, `UPDATE cheques SET status = $2, remaining = $3::numeric`+extra+` WHERE id = $1`,
				append([]any{c.ID, status, remaining.String()}, args...)...)
			return err
		}
		notFrozen := func() error {
			if c.Frozen {
				return &apiError{status: http.StatusConflict, code: "cheque_frozen"}
			}
			return nil
		}
		before := c.Status
		switch op {
		case "freeze", "unfreeze":
			if !in_ || (c.Status != "in_hand" && c.Status != "deposited") {
				return nil, wrong()
			}
			if _, err := tx.Exec(ctx, `UPDATE cheques SET frozen = $2 WHERE id = $1`, c.ID, op == "freeze"); err != nil {
				return nil, err
			}
			if err := chequeEvent(ctx, tx, cid, c.ID, op, in.Date, decimal.Zero, before, before, nil, nil, nil, nil, nil, nil, in.Notes); err != nil {
				return nil, err
			}

		case "deposit":
			if !in_ || c.Status != "in_hand" {
				return nil, wrong()
			}
			if err := notFrozen(); err != nil {
				return nil, err
			}
			m, err := box(true)
			if err != nil {
				return nil, err
			}
			je, err := post("Deposit · "+label, []jline{
				{Account: as.CUC, Desc: label, Currency: c.Currency, Debit: c.Remaining, Rate: c.Rate},
				{Account: as.CIH, Desc: label, Currency: c.Currency, Credit: c.Remaining, Rate: c.Rate},
			})
			if err != nil {
				return nil, err
			}
			if err := update("deposited", c.Remaining, ", bank_method_id = $4", m.ID); err != nil {
				return nil, err
			}
			if err := chequeEvent(ctx, tx, cid, c.ID, op, in.Date, c.Remaining, before, "deposited", &m.ID, nil, je, nil, nil, nil, in.Notes); err != nil {
				return nil, err
			}

		case "collect":
			if !in_ || (c.Status != "in_hand" && c.Status != "deposited") {
				return nil, wrong()
			}
			if err := notFrozen(); err != nil {
				return nil, err
			}
			m, err := box(true)
			if err != nil {
				return nil, err
			}
			from := as.CIH
			if c.Status == "deposited" {
				from = as.CUC
			}
			je, err := post("Cheque collected · "+label, []jline{
				{Account: m.Account, Desc: label, Currency: c.Currency, Debit: c.Remaining},
				{Account: from, Desc: label, Currency: c.Currency, Credit: c.Remaining, Rate: c.Rate},
			})
			if err != nil {
				return nil, err
			}
			if err := update("collected", c.Remaining, ", bank_method_id = $4", m.ID); err != nil {
				return nil, err
			}
			if err := chequeEvent(ctx, tx, cid, c.ID, op, in.Date, c.Remaining, before, "collected", &m.ID, nil, je, nil, nil, nil, in.Notes); err != nil {
				return nil, err
			}

		case "partial_cash":
			if !in_ || c.Status != "in_hand" {
				return nil, wrong()
			}
			amt, ok := money(in.Amount)
			if !ok || !amt.IsPositive() || amt.GreaterThan(c.Remaining) {
				return nil, badRequest("invalid_amount")
			}
			m, err := box(true)
			if err != nil {
				return nil, err
			}
			je, err := post("Cash on cheque · "+label, []jline{
				{Account: m.Account, Desc: label, Currency: c.Currency, Debit: amt},
				{Account: as.CIH, Desc: label, Currency: c.Currency, Credit: amt, Rate: c.Rate},
			})
			if err != nil {
				return nil, err
			}
			rest := c.Remaining.Sub(amt)
			st := "in_hand"
			if rest.IsZero() {
				st = "settled"
			}
			if err := update(st, rest, ""); err != nil {
				return nil, err
			}
			if err := chequeEvent(ctx, tx, cid, c.ID, op, in.Date, amt, before, st, &m.ID, nil, je, nil, nil, nil, in.Notes); err != nil {
				return nil, err
			}

		case "endorse":
			if !in_ || c.Status != "in_hand" {
				return nil, wrong()
			}
			if err := notFrozen(); err != nil {
				return nil, err
			}
			if in.SupplierID == "" {
				return nil, badRequest("supplier_required")
			}
			cb, err := chequeBox(ctx, tx, cid, as.CIH, c.Currency, "Cheques in hand")
			if err != nil {
				return nil, err
			}
			sp, err := paySupplier(ctx, tx, cid, supPayIn{SupplierID: in.SupplierID, Date: in.Date, Currency: c.Currency, MethodID: cb,
				PaidAmount: c.Remaining.String(), PaidRate: c.Rate, Notes: strings.TrimSpace("Endorsed " + label + " " + in.Notes), allowCheque: true})
			if err != nil {
				return nil, err
			}
			if err := update("endorsed", c.Remaining, ", endorsed_supplier_id = $4", in.SupplierID); err != nil {
				return nil, err
			}
			if err := chequeEvent(ctx, tx, cid, c.ID, op, in.Date, c.Remaining, before, "endorsed", nil, &in.SupplierID, nil, nil, &sp, nil, in.Notes); err != nil {
				return nil, err
			}

		case "endorse_return": // the supplier gives the endorsed cheque back: their payment is undone
			if !in_ || c.Status != "endorsed" {
				return nil, wrong()
			}
			var ev, sp string
			if err := tx.QueryRow(ctx, `SELECT id::text, supplier_payment_id::text FROM cheque_events WHERE cheque_id = $1 AND kind = 'endorse'
				AND NOT undone ORDER BY created_at DESC LIMIT 1`, c.ID).Scan(&ev, &sp); err != nil {
				return nil, err
			}
			if err := voidSupplierPaymentTx(ctx, tx, cid, sp); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `UPDATE cheque_events SET undone = true WHERE id = $1`, ev); err != nil {
				return nil, err
			}
			if err := update("in_hand", c.Remaining, ", endorsed_supplier_id = NULL"); err != nil {
				return nil, err
			}
			if err := chequeEvent(ctx, tx, cid, c.ID, op, in.Date, c.Remaining, before, "in_hand", nil, nil, nil, nil, &sp, nil, in.Notes); err != nil {
				return nil, err
			}

		case "bounce", "return":
			if c.Direction == "out" {
				if op != "bounce" || c.Status != "issued" {
					return nil, wrong()
				}
				je, err := undoIssued(ctx, tx, cid, set, as, c, in.Date, "Bounced "+label)
				if err != nil {
					return nil, err
				}
				if err := update("bounced", c.Remaining, ""); err != nil {
					return nil, err
				}
				if err := chequeEvent(ctx, tx, cid, c.ID, op, in.Date, c.Remaining, before, "bounced", nil, nil, je, nil, c.SupplierPaymentID, nil, in.Notes); err != nil {
					return nil, err
				}
				break
			}
			okStatus := c.Status == "in_hand" || (op == "bounce" && (c.Status == "deposited" || c.Status == "collected"))
			if !okStatus {
				return nil, wrong()
			}
			holding, holdRate := as.CIH, c.Rate
			switch c.Status {
			case "deposited":
				holding = as.CUC
			case "collected":
				m, err := box(true) // the bank takes the money back
				if err != nil {
					return nil, err
				}
				holding, holdRate = m.Account, ""
			}
			after := "bounced"
			what := "Bounced"
			if op == "return" {
				after, what = "returned", "Returned"
			}
			var je, inv *string
			switch {
			case c.CustomerID != nil && op == "return" && c.Remaining.Equal(c.Amount) && c.PaymentID != nil:
				// given back untouched: the payment simply never happened
				if err := voidPaymentTx(ctx, tx, cid, *c.PaymentID, "Returned "+label); err != nil {
					return nil, err
				}
			case c.CustomerID != nil:
				id, err := chequeDebit(ctx, tx, cid, set, c, holding, holdRate, in.Date, what)
				if err != nil {
					return nil, err
				}
				inv = &id
			case c.AccountID != nil:
				je, err = post(what+" · "+label, []jline{
					{Account: *c.AccountID, Desc: label, Currency: c.Currency, Debit: c.Remaining},
					{Account: holding, Desc: label, Currency: c.Currency, Credit: c.Remaining, Rate: holdRate},
				})
				if err != nil {
					return nil, err
				}
			default:
				return nil, wrong()
			}
			if err := update(after, c.Remaining, ", frozen = false"); err != nil {
				return nil, err
			}
			if err := chequeEvent(ctx, tx, cid, c.ID, op, in.Date, c.Remaining, before, after, nil, nil, je, c.PaymentID, nil, inv, in.Notes); err != nil {
				return nil, err
			}

		case "pay": // our cheque was paid by the bank
			if in_ || c.Status != "issued" {
				return nil, wrong()
			}
			m, err := box(true)
			if err != nil {
				return nil, err
			}
			je, err := post("Cheque paid by bank · "+label, []jline{
				{Account: as.CP, Desc: label, Currency: c.Currency, Debit: c.Remaining, Rate: c.Rate},
				{Account: m.Account, Desc: label, Currency: c.Currency, Credit: c.Remaining},
			})
			if err != nil {
				return nil, err
			}
			if err := update("paid", c.Remaining, ", bank_method_id = $4", m.ID); err != nil {
				return nil, err
			}
			if err := chequeEvent(ctx, tx, cid, c.ID, op, in.Date, c.Remaining, before, "paid", &m.ID, nil, je, nil, nil, nil, in.Notes); err != nil {
				return nil, err
			}

		case "cancel": // a cheque we wrote is torn up before the bank paid it
			if in_ || c.Status != "issued" {
				return nil, wrong()
			}
			je, err := undoIssued(ctx, tx, cid, set, as, c, in.Date, "Cancelled "+label)
			if err != nil {
				return nil, err
			}
			if err := update("cancelled", c.Remaining, ""); err != nil {
				return nil, err
			}
			if err := chequeEvent(ctx, tx, cid, c.ID, op, in.Date, c.Remaining, before, "cancelled", nil, nil, je, nil, c.SupplierPaymentID, nil, in.Notes); err != nil {
				return nil, err
			}

		case "undo": // the last deposit / collection / cash part / bank payment is reversed
			var ev, kind, sb, sa string
			var je *string
			var amtS string
			err := tx.QueryRow(ctx, `SELECT id::text, kind, status_before, status_after, journal_entry_id::text, amount::text FROM cheque_events
				WHERE cheque_id = $1 AND NOT undone AND kind NOT IN ('freeze', 'unfreeze') ORDER BY created_at DESC LIMIT 1`, c.ID).Scan(&ev, &kind, &sb, &sa, &je, &amtS)
			if err == pgx.ErrNoRows || (err == nil && !strings.Contains(" deposit collect partial_cash pay ", " "+kind+" ")) {
				return nil, &apiError{status: http.StatusConflict, code: "nothing_to_undo"}
			}
			if err != nil {
				return nil, err
			}
			if sa != c.Status {
				return nil, wrong()
			}
			var rev *string
			if je != nil {
				var rid string
				if err := tx.QueryRow(ctx, `SELECT reverse_journal_entry($1, NULL, $2)::text`, *je, "Undo · "+label).Scan(&rid); err != nil {
					return nil, err
				}
				rev = &rid
			}
			amt, _ := decimal.NewFromString(amtS)
			rest := c.Remaining
			if kind == "partial_cash" {
				rest = rest.Add(amt)
			}
			extra := ""
			if kind == "deposit" {
				extra = ", bank_method_id = NULL"
			}
			if err := update(sb, rest, extra); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `UPDATE cheque_events SET undone = true, undo_entry_id = $2 WHERE id = $1`, ev, rev); err != nil {
				return nil, err
			}
			if err := chequeEvent(ctx, tx, cid, c.ID, "undo_"+kind, in.Date, amt, c.Status, sb, nil, nil, rev, nil, nil, nil, in.Notes); err != nil {
				return nil, err
			}
		}
		return loadCheque(ctx, tx, cid, c.ID)
	})
}

// undoIssued reverses the writing of one of our cheques (cancelled or bounced): the supplier payment is voided
// (the supplier is owed again) or, for a cheque against an account, the entry is reversed.
func undoIssued(ctx context.Context, tx pgx.Tx, cid string, set *billingSettings, as *acctSettings, c *chequeRow, date, memo string) (*string, error) {
	if c.SupplierPaymentID != nil {
		return nil, voidSupplierPaymentTx(ctx, tx, cid, *c.SupplierPaymentID)
	}
	if c.AccountID == nil {
		return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
	}
	id, err := postJournal(ctx, tx, cid, set, date, memo, fmt.Sprintf("CHQ-%d", c.Number), "cheque", []jline{
		{Account: as.CP, Desc: memo, Currency: c.Currency, Debit: c.Remaining, Rate: c.Rate},
		{Account: *c.AccountID, Desc: memo, Currency: c.Currency, Credit: c.Remaining},
	})
	return &id, err
}
