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
	(i.total - i.paid)::text AS balance, i.status, i.kind, i.journal_entry_id, i.void_entry_id, i.notes, i.created_at`

type invLine struct {
	Kind        string
	Description string
	Quantity    decimal.Decimal
	UnitPrice   decimal.Decimal
	Amount      decimal.Decimal // before tax
	Meta        map[string]any
	Tax         *taxInfo // optional sales tax on this line
	TaxAmount   decimal.Decimal
}

// issueInvoice numbers, books (Dr receivables / Cr revenue) and stores an invoice, then applies any customer credit.
func issueInvoice(ctx context.Context, tx pgx.Tx, cid string, set *billingSettings, customerID, customerName string,
	subscriptionID, period *string, issue, due, currency, revenue, notes string, lines []invLine) (string, int64, error) {
	if err := set.requireAR(); err != nil {
		return "", 0, err
	}
	total, net := decimal.Zero, decimal.Zero
	taxByAccount := map[string]decimal.Decimal{}
	taxOrder := []string{}
	for _, l := range lines {
		net = net.Add(l.Amount)
		total = total.Add(l.Amount).Add(l.TaxAmount)
		if l.Tax != nil && l.TaxAmount.IsPositive() {
			if _, ok := taxByAccount[l.Tax.Account]; !ok {
				taxOrder = append(taxOrder, l.Tax.Account)
			}
			taxByAccount[l.Tax.Account] = taxByAccount[l.Tax.Account].Add(l.TaxAmount)
		}
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
	// one rate for every line keeps receivable = revenue + tax exactly in base currency
	rate, err := resolveRate(ctx, tx, cid, set.Base, currency, issue, "")
	if err != nil {
		return "", 0, err
	}
	jl := []jline{{Account: *set.AR, Desc: customerName, Currency: currency, Debit: total, Rate: rate}}
	if net.IsPositive() {
		jl = append(jl, jline{Account: revenue, Desc: customerName, Currency: currency, Credit: net, Rate: rate})
	}
	for _, acc := range taxOrder {
		jl = append(jl, jline{Account: acc, Desc: "Tax · " + customerName, Currency: currency, Credit: taxByAccount[acc], Rate: rate})
	}
	je, err := postJournal(ctx, tx, cid, set, issue, memo, fmt.Sprintf("INV-%d", number), "invoice", jl)
	if err != nil {
		return "", 0, err
	}
	localCur, localRate, localTotal := localSnapshot(ctx, tx, cid, set, currency, issue, total)
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO invoices (company_id, number, customer_id, subscription_id, period, issue_date, due_date,
		currency, total, journal_entry_id, notes, local_currency, local_rate, local_currency_total)
		VALUES ($1, $2, $3, $4, $5, $6::date, $7::date, $8, $9::numeric, $10, $11, $12, $13::numeric, $14::numeric) RETURNING id::text`,
		cid, number, customerID, subscriptionID, period, issue, due, currency, total.String(), je, notes,
		localCur, localRate, localTotal).Scan(&id); err != nil {
		return "", 0, err
	}
	for i, l := range lines {
		meta, _ := json.Marshal(l.Meta)
		if l.Meta == nil {
			meta = []byte("{}")
		}
		var taxID *string
		if l.Tax != nil {
			taxID = &l.Tax.ID
		}
		if _, err := tx.Exec(ctx, `INSERT INTO invoice_lines (invoice_id, line_no, kind, description, quantity, unit_price, amount, meta, tax_rate_id, tax_amount)
			VALUES ($1, $2, $3, $4, $5::numeric, $6::numeric, $7::numeric, $8, $9, $10::numeric)`,
			id, i+1, l.Kind, l.Description, l.Quantity.String(), l.UnitPrice.String(), l.Amount.String(), meta, taxID, l.TaxAmount.String()); err != nil {
			return "", 0, err
		}
	}
	if err := autoAllocate(ctx, tx, cid, set, customerID, currency, ""); err != nil {
		return "", 0, err
	}
	return id, number, autoMessage(ctx, tx, cid, "invoice_issued", customerID, &id, nil)
}

func periodEnd(period string) string {
	t, _ := time.Parse("2006-01", period)
	return t.AddDate(0, 1, -1).Format(time.DateOnly)
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
			ItemID      string `json:"item_id"` // optional: sell a stock item (takes it out of stock at average cost)
			WarehouseID string `json:"warehouse_id"`
			TaxRateID   string `json:"tax_rate_id"`
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
		sales := []saleLine{}
		for _, l := range in.Lines {
			q, ok1 := money(l.Quantity)
			p, ok2 := money(l.UnitPrice)
			if l.ItemID != "" {
				it, err := loadItem(ctx, tx, cid, l.ItemID)
				if err != nil {
					return nil, err
				}
				if strings.TrimSpace(l.Description) == "" {
					l.Description = it.Name
				}
				if it.Kind == "stock" && ok1 && q.IsPositive() {
					sales = append(sales, saleLine{ItemID: it.ID, WarehouseID: l.WarehouseID, Qty: q})
				}
			}
			if !ok1 || !ok2 || strings.TrimSpace(l.Description) == "" {
				return nil, badRequest("bad_request")
			}
			tax, err := loadTaxRate(ctx, tx, cid, l.TaxRateID, "sales")
			if err != nil {
				return nil, err
			}
			amt := q.Mul(p).Round(dec)
			lines = append(lines, invLine{Kind: "other", Description: strings.TrimSpace(l.Description), Quantity: q, UnitPrice: p, Amount: amt,
				Tax: tax, TaxAmount: taxOn(amt, tax, dec)})
		}
		id, number, err := issueInvoice(ctx, tx, cid, set, in.CustomerID, name, nil, nil, in.IssueDate, in.DueDate, in.Currency, rev, in.Notes, lines)
		if err != nil {
			return nil, err
		}
		if err := postSaleCOGS(ctx, tx, cid, set, id, number, in.IssueDate, sales); err != nil {
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
		ar.name AS area_name, s.meter_no, co.name_en AS company_name, co.name_ar AS company_name_ar,
		i.local_currency, trim_scale(i.local_rate)::text AS local_rate, i.local_currency_total::text AS local_currency_total,
		bx.code AS box_code
		FROM invoices i JOIN customers c ON c.id = i.customer_id JOIN companies co ON co.id = i.company_id
		LEFT JOIN areas ar ON ar.id = c.area_id LEFT JOIN subscriptions s ON s.id = i.subscription_id
		LEFT JOIN boxes bx ON bx.id = s.box_id
		WHERE i.id = $1 AND i.company_id = $2`, id, cid)
	if err != nil {
		return nil, err
	}
	lines, err := queryJSON(ctx, tx, `SELECT line_no, kind, description, quantity::text AS quantity, unit_price::text AS unit_price,
		amount::text AS amount, meta, tax_amount::text AS tax_amount,
		(SELECT t.name FROM tax_rates t WHERE t.id = invoice_lines.tax_rate_id) AS tax_name
		FROM invoice_lines WHERE invoice_id = $1 ORDER BY line_no`, id)
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
		var status, paid, kind, customer, currency string
		var je *string
		err := tx.QueryRow(ctx, `SELECT number, status, paid::text, journal_entry_id::text, kind, customer_id::text, currency::text FROM invoices
			WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&number, &status, &paid, &je, &kind, &customer, &currency)
		if err == pgx.ErrNoRows {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if status == "void" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if kind == "refund" {
			set, err := loadBillingSettings(ctx, tx, cid)
			if err != nil {
				return nil, err
			}
			if _, err := lockCustomer(ctx, tx, cid, customer); err != nil {
				return nil, err
			}
			if _, err := voidRefund(ctx, tx, cid, set, id, customer, currency, number, je, strings.TrimSpace(in.Reason)); err != nil {
				return nil, err
			}
			return loadInvoice(ctx, tx, cid, id)
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
		if err := reverseSaleCOGS(ctx, tx, cid, id, number); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE invoices SET status = 'void', void_entry_id = $2,
			notes = CASE WHEN $3 = '' THEN notes ELSE trim(notes || ' ' || $3) END WHERE id = $1`, id, rev, strings.TrimSpace(in.Reason)); err != nil {
			return nil, err
		}
		return loadInvoice(ctx, tx, cid, id)
	})
}
