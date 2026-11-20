package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Inter-company (phase 8). Two companies of the organisation are linked: each one knows the other as a customer
// and as a supplier, and keeps a current account with it (A: asset "due from B", B: liability "due to A").
//   - a sale (invoice) of one side to the other is mirrored as a supplier bill on the other side, automatically
//     (background job, and "sync now"); the same amounts, so the two sides agree.
//   - loans, transfers and repayments post both sides at once (cash / bank against the current accounts).
//   - the consolidated report can eliminate what the companies owe each other and their sales to each other.

type icLink struct {
	ID, A, B, ACust, ASup, BCust, BSup, AAcc, BAcc string
	AutoBills, Active                              bool
	Since                                          time.Time
}

const icLinkSel = `id::text, company_a::text, company_b::text, a_customer_id::text, a_supplier_id::text, b_customer_id::text,
	b_supplier_id::text, a_account_id::text, b_account_id::text, auto_bills, is_active, since`

func scanICLink(row pgx.Row) (*icLink, error) {
	l := &icLink{}
	err := row.Scan(&l.ID, &l.A, &l.B, &l.ACust, &l.ASup, &l.BCust, &l.BSup, &l.AAcc, &l.BAcc, &l.AutoBills, &l.Active, &l.Since)
	return l, err
}

func (s *Server) icLinks(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT k.id, k.company_a, ca.name_en AS company_a_name, ca.base_currency AS a_currency,
			k.company_b, cb.name_en AS company_b_name, cb.base_currency AS b_currency, k.auto_bills, k.is_active, k.since,
			k.a_customer_id, k.a_supplier_id, k.b_customer_id, k.b_supplier_id,
			aa.code AS a_account_code, ab.code AS b_account_code,
			(SELECT trim_scale(round(coalesce(sum(t.base_debit - t.base_credit), 0), 2))::text FROM account_totals(k.company_a, '0001-01-01', current_date) t WHERE t.account_id = k.a_account_id) AS a_balance,
			(SELECT trim_scale(round(coalesce(sum(t.base_credit - t.base_debit), 0), 2))::text FROM account_totals(k.company_b, '0001-01-01', current_date) t WHERE t.account_id = k.b_account_id) AS b_balance,
			(SELECT count(*) FROM ic_docs d WHERE d.link_id = k.id AND d.status = 'done') AS docs_done,
			(SELECT count(*) FROM ic_docs d WHERE d.link_id = k.id AND d.status IN ('pending', 'error')) AS docs_waiting,
			(SELECT count(*) FROM ic_docs d WHERE d.link_id = k.id AND d.status = 'source_void') AS docs_void
			FROM ic_links k JOIN companies ca ON ca.id = k.company_a JOIN companies cb ON cb.id = k.company_b
			JOIN accounts aa ON aa.id = k.a_account_id JOIN accounts ab ON ab.id = k.b_account_id
			ORDER BY ca.name_en, cb.name_en`)
	})
}

// POST /intercompany/links {company_a, company_b, auto_bills}: links two companies. Missing partner records are made:
// in A a customer and a supplier named after B (and the other way round), and the two current accounts.
func (s *Server) createICLink(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CompanyA  string `json:"company_a"`
		CompanyB  string `json:"company_b"`
		AutoBills *bool  `json:"auto_bills"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if !isUUID(in.CompanyA) || !isUUID(in.CompanyB) || strings.EqualFold(in.CompanyA, in.CompanyB) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	p := principal(r)
	if !companyAllowed(p, in.CompanyA) || !companyAllowed(p, in.CompanyB) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	auto := in.AutoBills == nil || *in.AutoBills
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		type comp struct{ id, name, code string }
		load := func(id string) (comp, error) {
			c := comp{id: id}
			err := tx.QueryRow(ctx, `SELECT name_en FROM companies WHERE id = $1 AND is_active`, id).Scan(&c.name)
			if errors.Is(err, pgx.ErrNoRows) {
				return c, notFound()
			}
			c.code = "IC-" + strings.ToUpper(strings.ReplaceAll(id, "-", "")[24:])
			return c, err
		}
		a, err := load(in.CompanyA)
		if err != nil {
			return nil, err
		}
		b, err := load(in.CompanyB)
		if err != nil {
			return nil, err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ic_links WHERE least(company_a, company_b) = least($1::uuid, $2::uuid)
			AND greatest(company_a, company_b) = greatest($1::uuid, $2::uuid))`, a.id, b.id).Scan(&exists); err != nil {
			return nil, err
		}
		if exists {
			return nil, &apiError{status: http.StatusConflict, code: "duplicate"}
		}
		partner := func(in, other comp) (cust, sup string, err error) {
			err = tx.QueryRow(ctx, `SELECT id::text FROM customers WHERE company_id = $1 AND code = $2`, in.id, other.code).Scan(&cust)
			if errors.Is(err, pgx.ErrNoRows) {
				err = tx.QueryRow(ctx, `INSERT INTO customers (company_id, code, name) VALUES ($1, $2, $3) RETURNING id::text`, in.id, other.code, other.name).Scan(&cust)
			}
			if err != nil {
				return
			}
			var cur string
			if err = tx.QueryRow(ctx, `SELECT base_currency::text FROM companies WHERE id = $1`, in.id).Scan(&cur); err != nil {
				return
			}
			err = tx.QueryRow(ctx, `SELECT id::text FROM suppliers WHERE company_id = $1 AND code = $2`, in.id, other.code).Scan(&sup)
			if errors.Is(err, pgx.ErrNoRows) {
				err = tx.QueryRow(ctx, `INSERT INTO suppliers (company_id, code, name, currency) VALUES ($1, $2, $3, $4) RETURNING id::text`,
					in.id, other.code, other.name, cur).Scan(&sup)
			}
			return
		}
		account := func(in, other comp, typ string) (string, error) {
			var id string
			err := tx.QueryRow(ctx, `SELECT id::text FROM accounts WHERE company_id = $1 AND code = $2`, in.id, other.code).Scan(&id)
			if errors.Is(err, pgx.ErrNoRows) {
				name := "Inter-company current account – " + other.name
				err = tx.QueryRow(ctx, `INSERT INTO accounts (company_id, code, name, type, is_postable) VALUES ($1, $2, $3, $4, true) RETURNING id::text`,
					in.id, other.code, name, typ).Scan(&id)
			}
			return id, err
		}
		aCust, aSup, err := partner(a, b)
		if err != nil {
			return nil, err
		}
		bCust, bSup, err := partner(b, a)
		if err != nil {
			return nil, err
		}
		aAcc, err := account(a, b, "asset")
		if err != nil {
			return nil, err
		}
		bAcc, err := account(b, a, "liability")
		if err != nil {
			return nil, err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO ic_links (company_a, company_b, a_customer_id, a_supplier_id, b_customer_id, b_supplier_id,
			a_account_id, b_account_id, auto_bills) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id::text`,
			a.id, b.id, aCust, aSup, bCust, bSup, aAcc, bAcc, auto).Scan(&id); err != nil {
			return nil, err
		}
		return map[string]any{"id": id, "a_customer_id": aCust, "a_supplier_id": aSup, "b_customer_id": bCust, "b_supplier_id": bSup,
			"a_account_id": aAcc, "b_account_id": bAcc}, nil
	})
}

// PATCH /intercompany/links/{id} {auto_bills, is_active}
func (s *Server) updateICLink(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AutoBills *bool `json:"auto_bills"`
		IsActive  *bool `json:"is_active"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		tag, err := tx.Exec(ctx, `UPDATE ic_links SET auto_bills = coalesce($2, auto_bills), is_active = coalesce($3, is_active),
			since = CASE WHEN $2 IS TRUE AND NOT auto_bills THEN now() ELSE since END, updated_at = now() WHERE id = $1`, id, in.AutoBills, in.IsActive)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return map[string]bool{"ok": true}, nil
	})
}

// POST /intercompany/sync: mirror the new sales between linked companies now (the background job does it too).
func (s *Server) icSyncNow(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return icSync(ctx, tx)
	})
}

// icSync: for every active link with automatic bills, each invoice of one side to the other (made after the link)
// becomes a supplier bill on the other side. One document at a time inside a savepoint: a failure (no rate, no
// expense account, closed period) is kept on the document and retried later.
func icSync(ctx context.Context, tx pgx.Tx) (map[string]int, error) {
	out := map[string]int{"created": 0, "failed": 0, "voided": 0}
	rows, err := tx.Query(ctx, `SELECT `+icLinkSel+` FROM ic_links WHERE is_active AND auto_bills ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	links, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*icLink, error) { return scanICLink(row) })
	if err != nil {
		return nil, err
	}
	for _, l := range links {
		for _, dir := range [][3]string{{l.A, l.ACust, l.B}, {l.B, l.BCust, l.A}} {
			if _, err := tx.Exec(ctx, `INSERT INTO ic_docs (link_id, src_company, src_invoice, dst_company, currency, amount)
				SELECT $1, i.company_id, i.id, $4, i.currency, i.total FROM invoices i
				WHERE i.company_id = $2 AND i.customer_id = $3 AND i.kind = 'invoice' AND i.status <> 'void' AND i.created_at >= $5
				ON CONFLICT (src_invoice) DO NOTHING`, l.ID, dir[0], dir[1], dir[2], l.Since); err != nil {
				return nil, err
			}
		}
		// a voided sale: its mirrored bill is voided too when nothing was paid on it, else flagged for a person
		vrows, err := tx.Query(ctx, `SELECT d.id::text, d.dst_company::text, d.dst_bill::text FROM ic_docs d JOIN invoices i ON i.id = d.src_invoice
			WHERE d.link_id = $1 AND i.status = 'void' AND d.status <> 'source_void'`, l.ID)
		if err != nil {
			return nil, err
		}
		type vd struct {
			id, dst string
			bill    *string
		}
		var voids []vd
		for vrows.Next() {
			var v vd
			if err := vrows.Scan(&v.id, &v.dst, &v.bill); err != nil {
				vrows.Close()
				return nil, err
			}
			voids = append(voids, v)
		}
		vrows.Close()
		for _, v := range voids {
			note := ""
			if v.bill != nil {
				sp, err := tx.Begin(ctx)
				if err != nil {
					return nil, err
				}
				if err := voidBillTx(ctx, sp, v.dst, *v.bill, "inter-company sale voided"); err != nil {
					_ = sp.Rollback(ctx)
					note = errCode(err)
				} else if err := sp.Commit(ctx); err != nil {
					return nil, err
				}
			}
			if _, err := tx.Exec(ctx, `UPDATE ic_docs SET status = 'source_void', error = $2, updated_at = now() WHERE id = $1`, v.id, note); err != nil {
				return nil, err
			}
			out["voided"]++
		}

		prow, err := tx.Query(ctx, `SELECT d.id::text, d.src_invoice::text, d.src_company::text, d.dst_company::text FROM ic_docs d
			WHERE d.link_id = $1 AND d.status IN ('pending', 'error') AND d.attempts < 20 ORDER BY d.created_at LIMIT 200`, l.ID)
		if err != nil {
			return nil, err
		}
		type pd struct{ id, inv, src, dst string }
		var pend []pd
		for prow.Next() {
			var d pd
			if err := prow.Scan(&d.id, &d.inv, &d.src, &d.dst); err != nil {
				prow.Close()
				return nil, err
			}
			pend = append(pend, d)
		}
		prow.Close()
		for _, d := range pend {
			sup := l.BSup
			if d.dst == l.A {
				sup = l.ASup
			}
			sp, err := tx.Begin(ctx)
			if err != nil {
				return nil, err
			}
			bill, err := icMirrorInvoice(ctx, sp, d.inv, d.src, d.dst, sup)
			if err != nil {
				_ = sp.Rollback(ctx)
				var ae *apiError
				if !errors.As(err, &ae) && !strings.Contains(err.Error(), "SQLSTATE") {
					return nil, err
				}
				if _, err2 := tx.Exec(ctx, `UPDATE ic_docs SET status = 'error', error = left($2, 300), attempts = attempts + 1, updated_at = now() WHERE id = $1`,
					d.id, errCode(err)); err2 != nil {
					return nil, err2
				}
				out["failed"]++
				continue
			}
			if err := sp.Commit(ctx); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `UPDATE ic_docs SET status = 'done', dst_bill = $2, error = '', attempts = attempts + 1, updated_at = now() WHERE id = $1`,
				d.id, bill); err != nil {
				return nil, err
			}
			out["created"]++
		}
	}
	return out, nil
}

func errCode(err error) string {
	var ae *apiError
	if errors.As(err, &ae) {
		if ae.detail != "" {
			return ae.code + ": " + ae.detail
		}
		return ae.code
	}
	return err.Error()
}

// icMirrorInvoice books, in company dst, the supplier bill of invoice inv of company src: same date, currency and
// amounts (tax included). A line of an item also known in dst (same code) is bought as that item; any other line
// goes to dst's default expense account.
func icMirrorInvoice(ctx context.Context, tx pgx.Tx, inv, src, dst, supplier string) (string, error) {
	var number int64
	var issue, due, cur, srcName string
	if err := tx.QueryRow(ctx, `SELECT i.number, i.issue_date::text, i.due_date::text, i.currency::text, c.name_en FROM invoices i
		JOIN companies c ON c.id = i.company_id WHERE i.id = $1 AND i.company_id = $2`, inv, src).Scan(&number, &issue, &due, &cur, &srcName); err != nil {
		return "", err
	}
	var expense *string
	if err := tx.QueryRow(ctx, `SELECT default_expense_account_id::text FROM companies WHERE id = $1`, dst).Scan(&expense); err != nil {
		return "", err
	}
	rows, err := tx.Query(ctx, `SELECT l.description, l.quantity::text, (l.amount + coalesce(l.tax_amount, 0))::text,
		(SELECT d.id::text FROM items s JOIN items d ON d.company_id = $2 AND d.code = s.code AND d.is_active WHERE s.id = l.item_id),
		(SELECT d.kind FROM items s JOIN items d ON d.company_id = $2 AND d.code = s.code AND d.is_active WHERE s.id = l.item_id)
		FROM invoice_lines l WHERE l.invoice_id = $1 ORDER BY l.line_no`, inv, dst)
	if err != nil {
		return "", err
	}
	type ln struct {
		desc, qty, amt string
		item, kind     *string
	}
	var lines []ln
	for rows.Next() {
		var x ln
		if err := rows.Scan(&x.desc, &x.qty, &x.amt, &x.item, &x.kind); err != nil {
			rows.Close()
			return "", err
		}
		lines = append(lines, x)
	}
	rows.Close()
	if len(lines) == 0 {
		return "", badRequest("ic_empty_invoice")
	}
	wh := ""
	in := billInput{SupplierID: supplier, SupplierRef: fmt.Sprintf("IC-%d", number), BillDate: issue, Currency: cur,
		Notes: fmt.Sprintf("Inter-company: invoice #%d of %s", number, srcName)}
	if due >= issue {
		in.DueDate = due
	}
	for _, x := range lines {
		q, _ := decimal.NewFromString(x.qty)
		a, _ := decimal.NewFromString(x.amt)
		if a.IsZero() {
			continue
		}
		if !q.IsPositive() {
			q = decimal.NewFromInt(1)
		}
		desc := strings.TrimSpace(x.desc)
		if desc == "" {
			desc = in.Notes
		}
		bl := billLineIn{Description: desc, Quantity: q.String(), UnitPrice: a.DivRound(q, 6).String()}
		switch {
		case x.item != nil && x.kind != nil && *x.kind == "stock":
			if wh == "" {
				w, err := defaultWarehouse(ctx, tx, dst)
				if err != nil {
					return "", err
				}
				wh = w
			}
			bl.ItemID, bl.WarehouseID = *x.item, wh
		case x.item != nil:
			bl.ItemID = *x.item
		default:
			if expense == nil {
				return "", badRequest("ic_no_expense_account")
			}
			bl.AccountID = *expense
		}
		in.Lines = append(in.Lines, bl)
	}
	if len(in.Lines) == 0 {
		return "", badRequest("ic_empty_invoice")
	}
	return createBillTx(ctx, tx, dst, in)
}

// GET /intercompany/docs?link_id=
func (s *Server) icDocs(w http.ResponseWriter, r *http.Request) {
	link := r.URL.Query().Get("link_id")
	if link != "" && !isUUID(link) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT d.id, d.link_id, d.status, d.error, d.attempts, d.currency, d.amount::text AS amount, d.created_at,
			d.src_company, cs.name_en AS src_name, i.number AS invoice_number, i.issue_date,
			d.dst_company, cd.name_en AS dst_name, b.number AS bill_number, b.status AS bill_status
			FROM ic_docs d JOIN companies cs ON cs.id = d.src_company JOIN companies cd ON cd.id = d.dst_company
			JOIN invoices i ON i.id = d.src_invoice LEFT JOIN bills b ON b.id = d.dst_bill
			WHERE ($1 = '' OR d.link_id = nullif($1, '')::uuid) ORDER BY d.created_at DESC LIMIT 500`, link)
	})
}

// ---------- loans and transfers ----------

type icTransferIn struct {
	LinkID      string `json:"link_id"`
	FromCompany string `json:"from_company"`
	Kind        string `json:"kind"`
	Date        string `json:"date"`
	Currency    string `json:"currency"`
	Amount      string `json:"amount"`
	FromAccount string `json:"from_account_id"`
	ToAccount   string `json:"to_account_id"`
	Memo        string `json:"memo"`
}

// POST /intercompany/transfers: money from one company to the other (a loan, a transfer or a repayment). The payer
// books Dr current account / Cr its cash or bank; the receiver books Dr its cash or bank / Cr current account.
func (s *Server) createICTransfer(w http.ResponseWriter, r *http.Request) {
	var in icTransferIn
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Date == "" {
		in.Date = today()
	}
	amt, ok := money(in.Amount)
	if !ok || !amt.IsPositive() || !isUUID(in.LinkID) || !isUUID(in.FromCompany) || !isUUID(in.FromAccount) || !isUUID(in.ToAccount) ||
		!validDate(in.Date) || !currencyRe.MatchString(in.Currency) || !contains([]string{"loan", "transfer", "repayment"}, in.Kind) || len(in.Memo) > 300 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	p := principal(r)
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		l, err := scanICLink(tx.QueryRow(ctx, `SELECT `+icLinkSel+` FROM ic_links WHERE id = $1 AND is_active`, in.LinkID))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if !companyAllowed(p, l.A) || !companyAllowed(p, l.B) {
			return nil, notFound()
		}
		from, to, fromIC, toIC := l.A, l.B, l.AAcc, l.BAcc
		if strings.EqualFold(in.FromCompany, l.B) {
			from, to, fromIC, toIC = l.B, l.A, l.BAcc, l.AAcc
		} else if !strings.EqualFold(in.FromCompany, l.A) {
			return nil, badRequest("bad_request")
		}
		for _, x := range [][2]string{{from, in.FromAccount}, {to, in.ToAccount}} {
			if err := requirePostable(ctx, tx, x[0], x[1]); err != nil {
				return nil, err
			}
		}
		memo := strings.TrimSpace(in.Memo)
		if memo == "" {
			memo = "Inter-company " + in.Kind
		}
		post := func(cid, dr, cr string) (string, error) {
			set, err := loadBillingSettings(ctx, tx, cid)
			if err != nil {
				return "", err
			}
			return postJournal(ctx, tx, cid, set, in.Date, memo, "IC", "intercompany", []jline{
				{Account: dr, Desc: memo, Currency: in.Currency, Debit: amt},
				{Account: cr, Desc: memo, Currency: in.Currency, Credit: amt},
			})
		}
		fromJE, err := post(from, fromIC, in.FromAccount)
		if err != nil {
			return nil, err
		}
		toJE, err := post(to, in.ToAccount, toIC)
		if err != nil {
			return nil, err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO ic_transfers (link_id, from_company, to_company, kind, transfer_date, currency, amount, from_account,
			to_account, from_entry_id, to_entry_id, memo) VALUES ($1, $2, $3, $4, $5::date, $6, $7::numeric, $8, $9, $10, $11, $12) RETURNING id::text`,
			l.ID, from, to, in.Kind, in.Date, in.Currency, amt.String(), in.FromAccount, in.ToAccount, fromJE, toJE, memo).Scan(&id); err != nil {
			return nil, err
		}
		return map[string]string{"id": id, "from_entry_id": fromJE, "to_entry_id": toJE}, nil
	})
}

func (s *Server) icTransfers(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT t.id, t.link_id, t.kind, t.transfer_date, t.currency, t.amount::text AS amount, t.memo, t.status, t.created_at,
			t.from_company, cf.name_en AS from_name, t.to_company, ct.name_en AS to_name,
			af.code || ' ' || af.name AS from_account, at.code || ' ' || at.name AS to_account
			FROM ic_transfers t JOIN companies cf ON cf.id = t.from_company JOIN companies ct ON ct.id = t.to_company
			JOIN accounts af ON af.id = t.from_account JOIN accounts at ON at.id = t.to_account
			ORDER BY t.transfer_date DESC, t.created_at DESC LIMIT 500`)
	})
}

// POST /intercompany/transfers/{id}/void: both entries are reversed (opposite entries, same date).
func (s *Server) voidICTransfer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var from, to, status, date string
		var fje, tje *string
		err := tx.QueryRow(ctx, `SELECT from_company::text, to_company::text, status, transfer_date::text, from_entry_id::text, to_entry_id::text
			FROM ic_transfers WHERE id = $1 FOR UPDATE`, id).Scan(&from, &to, &status, &date, &fje, &tje)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if status != "posted" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		for _, x := range [][2]*string{{&from, fje}, {&to, tje}} {
			if x[1] == nil {
				continue
			}
			if err := reverseEntryLines(ctx, tx, *x[0], *x[1], date, "Void inter-company transfer"); err != nil {
				return nil, err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE ic_transfers SET status = 'void' WHERE id = $1`, id); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	})
}

// reverseEntryLines posts the opposite of a posted entry (same amounts and rates).
func reverseEntryLines(ctx context.Context, tx pgx.Tx, cid, entry, date, memo string) error {
	set, err := loadBillingSettings(ctx, tx, cid)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT account_id::text, description, currency::text, debit::text, credit::text, rate::text, coalesce(cost_center_id::text, '')
		FROM journal_lines WHERE entry_id = $1 ORDER BY line_no`, entry)
	if err != nil {
		return err
	}
	var lines []jline
	for rows.Next() {
		var l jline
		var dr, cr string
		if err := rows.Scan(&l.Account, &l.Desc, &l.Currency, &dr, &cr, &l.Rate, &l.CostCenter); err != nil {
			rows.Close()
			return err
		}
		l.Credit, _ = decimal.NewFromString(dr)
		l.Debit, _ = decimal.NewFromString(cr)
		lines = append(lines, l)
	}
	rows.Close()
	_, err = postJournal(ctx, tx, cid, set, date, memo, "IC", "intercompany", lines)
	return err
}

// ---------- consolidation eliminations ----------

type consComp struct {
	ID, Name, Base string
	Rate           decimal.Decimal // report currency per 1 base
}

type consElim struct {
	Code, Name, Type, Reason string
	Amount                   decimal.Decimal // in the report's sign (assets / expenses debit, the others credit), report currency
}

// icEliminations: what the consolidated report takes out for the linked companies inside it.
//   - balance sheet: the current accounts of both sides, and the open inter-company invoices (receivable of the seller)
//     and their bills (payable of the buyer). Any difference (timing, rates) goes to an equity line, so the
//     consolidated balance sheet still balances.
//   - profit and loss: inter-company sales of the period against the expenses booked by the buyer's bills (same
//     amount on both sides, so the result does not change). Goods the buyer still holds in stock keep their margin
//     (unrealised profit is not eliminated).
func icEliminations(ctx context.Context, tx pgx.Tx, comps []consComp, report, from, to string) ([]consElim, error) {
	in := map[string]consComp{}
	for _, c := range comps {
		in[strings.ToLower(c.ID)] = c
	}
	rows, err := tx.Query(ctx, `SELECT `+icLinkSel+` FROM ic_links ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	links, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*icLink, error) { return scanICLink(row) })
	if err != nil {
		return nil, err
	}
	var out []consElim
	acct := func(id string) (code, name, typ string, err error) {
		err = tx.QueryRow(ctx, `SELECT code, name, type FROM accounts WHERE id = $1`, id).Scan(&code, &name, &typ)
		return
	}
	add := func(accID string, amt decimal.Decimal, reason string) error {
		if amt.IsZero() {
			return nil
		}
		code, name, typ, err := acct(accID)
		if err != nil {
			return err
		}
		out = append(out, consElim{Code: code, Name: name, Type: typ, Reason: reason, Amount: amt})
		return nil
	}
	diff := decimal.Zero // balance sheet: liabilities removed - assets removed
	for _, l := range links {
		ca, okA := in[strings.ToLower(l.A)]
		cb, okB := in[strings.ToLower(l.B)]
		if !okA || !okB {
			continue
		}
		cs := map[string]consComp{strings.ToLower(l.A): ca, strings.ToLower(l.B): cb}
		if report == "balance-sheet" {
			bal := func(cid, acc string) (decimal.Decimal, error) {
				var v string
				err := tx.QueryRow(ctx, `SELECT coalesce(sum(t.base_debit - t.base_credit), 0)::text FROM account_totals($1, '0001-01-01', $2::date) t
					WHERE t.account_id = $3`, cid, to, acc).Scan(&v)
				d, _ := decimal.NewFromString(v)
				return d, err
			}
			aBal, err := bal(l.A, l.AAcc) // debit balance of A's asset
			if err != nil {
				return nil, err
			}
			bBal, err := bal(l.B, l.BAcc) // debit balance of B's liability (normally negative)
			if err != nil {
				return nil, err
			}
			assetOut := aBal.Mul(ca.Rate)
			liabOut := bBal.Neg().Mul(cb.Rate)
			if err := add(l.AAcc, assetOut.Neg(), "current_account"); err != nil {
				return nil, err
			}
			if err := add(l.BAcc, liabOut.Neg(), "current_account"); err != nil {
				return nil, err
			}
			diff = diff.Add(liabOut).Sub(assetOut)

			// open inter-company invoices and their bills, at the report date
			drows, err := tx.Query(ctx, `SELECT d.src_company::text, d.dst_company::text, i.currency::text, (i.total - i.paid)::text,
				coalesce((b.total - b.paid)::text, '0'), b.currency::text,
				(SELECT ar_account_id::text FROM companies WHERE id = d.src_company), (SELECT ap_account_id::text FROM companies WHERE id = d.dst_company)
				FROM ic_docs d JOIN invoices i ON i.id = d.src_invoice LEFT JOIN bills b ON b.id = d.dst_bill AND b.status <> 'void'
				WHERE d.link_id = $1 AND d.status = 'done' AND i.status <> 'void' AND i.issue_date <= $2::date`, l.ID, to)
			if err != nil {
				return nil, err
			}
			type od struct {
				src, dst, icur, iopen, bopen string
				bcur, ar, ap                 *string
			}
			var ods []od
			for drows.Next() {
				var x od
				if err := drows.Scan(&x.src, &x.dst, &x.icur, &x.iopen, &x.bopen, &x.bcur, &x.ar, &x.ap); err != nil {
					drows.Close()
					return nil, err
				}
				ods = append(ods, x)
			}
			drows.Close()
			for _, x := range ods {
				srcC, dstC := cs[strings.ToLower(x.src)], cs[strings.ToLower(x.dst)]
				toBase := func(c consComp, cur, amount string) (decimal.Decimal, error) {
					a, _ := decimal.NewFromString(amount)
					if a.IsZero() {
						return decimal.Zero, nil
					}
					rate, err := resolveRate(ctx, tx, c.ID, c.Base, cur, to, "")
					if err != nil {
						return decimal.Zero, err
					}
					rd, _ := decimal.NewFromString(rate)
					if !rd.IsPositive() {
						return decimal.Zero, nil
					}
					return a.DivRound(rd, 6).Mul(c.Rate), nil
				}
				recv, err := toBase(srcC, x.icur, x.iopen)
				if err != nil {
					return nil, err
				}
				pay := decimal.Zero
				if x.bcur != nil {
					if pay, err = toBase(dstC, *x.bcur, x.bopen); err != nil {
						return nil, err
					}
				}
				if x.ar != nil {
					if err := add(*x.ar, recv.Neg(), "open_invoices"); err != nil {
						return nil, err
					}
					diff = diff.Sub(recv)
				}
				if x.ap != nil {
					if err := add(*x.ap, pay.Neg(), "open_invoices"); err != nil {
						return nil, err
					}
					diff = diff.Add(pay)
				}
			}
			continue
		}
		// profit and loss: sales of the period mirrored as expenses of the buyer
		drows, err := tx.Query(ctx, `SELECT d.src_company::text, d.dst_company::text, i.journal_entry_id::text, b.journal_entry_id::text
			FROM ic_docs d JOIN invoices i ON i.id = d.src_invoice JOIN bills b ON b.id = d.dst_bill
			WHERE d.link_id = $1 AND d.status = 'done' AND i.status <> 'void' AND b.status <> 'void'
			  AND i.issue_date BETWEEN $2::date AND $3::date AND i.journal_entry_id IS NOT NULL AND b.journal_entry_id IS NOT NULL`, l.ID, from, to)
		if err != nil {
			return nil, err
		}
		type pd struct{ src, dst, ije, bje string }
		var pds []pd
		for drows.Next() {
			var x pd
			if err := drows.Scan(&x.src, &x.dst, &x.ije, &x.bje); err != nil {
				drows.Close()
				return nil, err
			}
			pds = append(pds, x)
		}
		drows.Close()
		for _, x := range pds {
			srcC, dstC := cs[strings.ToLower(x.src)], cs[strings.ToLower(x.dst)]
			type amt struct {
				acc string
				v   decimal.Decimal
			}
			lines := func(je, typ, sql string, rate decimal.Decimal) ([]amt, decimal.Decimal, error) {
				rs, err := tx.Query(ctx, `SELECT l.account_id::text, sum(`+sql+`)::text FROM journal_lines l JOIN accounts a ON a.id = l.account_id
					WHERE l.entry_id = $1 AND a.type = $2 GROUP BY 1`, je, typ)
				if err != nil {
					return nil, decimal.Zero, err
				}
				defer rs.Close()
				var res []amt
				tot := decimal.Zero
				for rs.Next() {
					var a amt
					var v string
					if err := rs.Scan(&a.acc, &v); err != nil {
						return nil, decimal.Zero, err
					}
					a.v, _ = decimal.NewFromString(v)
					a.v = a.v.Mul(rate)
					if !a.v.IsZero() {
						res = append(res, a)
						tot = tot.Add(a.v)
					}
				}
				return res, tot, rs.Err()
			}
			inc, incTot, err := lines(x.ije, "income", "l.base_credit - l.base_debit", srcC.Rate)
			if err != nil {
				return nil, err
			}
			exp, expTot, err := lines(x.bje, "expense", "l.base_debit - l.base_credit", dstC.Rate)
			if err != nil {
				return nil, err
			}
			e := decimal.Min(incTot, expTot)
			if !e.IsPositive() || !incTot.IsPositive() || !expTot.IsPositive() {
				continue
			}
			for _, a := range inc {
				if err := add(a.acc, a.v.Mul(e).DivRound(incTot, 6).Neg(), "ic_sales"); err != nil {
					return nil, err
				}
			}
			for _, a := range exp {
				if err := add(a.acc, a.v.Mul(e).DivRound(expTot, 6).Neg(), "ic_sales"); err != nil {
					return nil, err
				}
			}
		}
	}
	if report == "balance-sheet" && !diff.Round(2).IsZero() {
		out = append(out, consElim{Code: "IC-DIFF", Name: "Inter-company difference", Type: "equity", Reason: "difference", Amount: diff})
	}
	return out, nil
}

// ---------- background job ----------

// StartIntercoWorker mirrors inter-company sales every 2 minutes.
func (s *Server) StartIntercoWorker(ctx context.Context) {
	if s.OwnerPool == nil {
		return
	}
	go func() {
		tick := time.NewTicker(2 * time.Minute)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if err := s.RunIntercoJobs(ctx); err != nil {
					slog.Error("interco worker", "err", err)
				}
			}
		}
	}()
}

func (s *Server) RunIntercoJobs(ctx context.Context) error {
	rows, err := s.OwnerPool.Query(ctx, `SELECT DISTINCT tenant_id::text FROM ic_links WHERE is_active AND auto_bills AND tenant_jobs_ok(tenant_id)`)
	if err != nil {
		return err
	}
	tenants, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, t := range tenants {
		if err := s.inJobTx(ctx, t, func(tx pgx.Tx) error {
			_, err := icSync(ctx, tx)
			return err
		}); err != nil {
			slog.Error("interco sync", "tenant", t, "err", err)
		}
	}
	return nil
}
