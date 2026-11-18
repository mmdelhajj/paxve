package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/mmdelhajj/paxve/internal/db"
)

// Bulk import (migration from the old system or any spreadsheet).
// POST /companies/{cid}/import/{type}  {rows:[{field:value}], dry_run, options}
// Every row is validated first. A dry run only reports errors. A real run writes everything in ONE
// transaction: if any row is invalid nothing is written.

const maxImportRows = 20000

type importReq struct {
	Rows    []map[string]string `json:"rows"`
	DryRun  bool                `json:"dry_run"`
	Options map[string]string   `json:"options"`
}

type importErr struct {
	Row   int    `json:"row"` // 1-based data row
	Field string `json:"field"`
	Code  string `json:"code"`
	Value string `json:"value,omitempty"`
}

type importResult struct {
	Type         string      `json:"type"`
	DryRun       bool        `json:"dry_run"`
	Total        int         `json:"total"`
	Valid        int         `json:"valid"`
	Created      int         `json:"created"`
	Updated      int         `json:"updated"`
	Skipped      int         `json:"skipped"`
	CreatedAreas int         `json:"created_areas"`
	Errors       []importErr `json:"errors"`
	TookMS       int64       `json:"took_ms"`
}

type importer struct {
	ctx  context.Context
	tx   pgx.Tx
	cid  string
	req  importReq
	res  *importResult
	bad  map[int]bool
	opts map[string]string
}

func (im *importer) fail(row int, field, code, value string) {
	im.bad[row] = true
	if len(im.res.Errors) < 1000 {
		im.res.Errors = append(im.res.Errors, importErr{Row: row, Field: field, Code: code, Value: value})
	}
}

func cell(r map[string]string, k string) string { return strings.TrimSpace(r[k]) }

// num accepts "1,250.50", "1 250", "1250,5" (single comma as decimal) and returns a decimal string.
func num(s string) (decimal.Decimal, bool) {
	s = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, " ", ""), " ", ""))
	if s == "" {
		return decimal.Zero, false
	}
	if strings.Count(s, ",") == 1 && !strings.Contains(s, ".") && len(s)-strings.Index(s, ",") != 4 {
		s = strings.Replace(s, ",", ".", 1)
	}
	s = strings.ReplaceAll(s, ",", "")
	d, err := decimal.NewFromString(s)
	if err != nil || d.IsNegative() {
		return decimal.Zero, false
	}
	return d, true
}

// date accepts YYYY-MM-DD, DD/MM/YYYY, DD-MM-YYYY, YYYY/MM/DD.
func date(s string) (string, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.DateOnly, "02/01/2006", "2/1/2006", "02-01-2006", "2006/01/02", "02.01.2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format(time.DateOnly), true
		}
	}
	return "", false
}

func period(s string) (string, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"2006-01", "01/2006", "1/2006", "2006/01", "01-2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format("2006-01"), true
		}
	}
	return "", false
}

var statusWords = map[string]string{
	"": "active", "active": "active", "1": "active", "yes": "active", "actif": "active", "فعال": "active", "فعّال": "active", "نشط": "active",
	"suspended": "suspended", "suspendu": "suspended", "موقوف": "suspended", "0": "suspended",
	"closed": "closed", "clôturé": "closed", "cloture": "closed", "مغلق": "closed", "ended": "ended", "terminé": "ended", "منته": "ended", "منتهٍ": "ended",
}

func (s *Server) importData(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("type")
	switch kind {
	case "customers", "subscriptions", "opening_balances", "readings", "accounts":
	default:
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	var req importReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 40<<20))
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if len(req.Rows) == 0 || len(req.Rows) > maxImportRows {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "import_size", "detail": strconv.Itoa(maxImportRows)})
		return
	}
	if req.Options == nil {
		req.Options = map[string]string{}
	}
	cid := r.PathValue("cid")
	start := time.Now()
	res := &importResult{Type: kind, DryRun: req.DryRun, Total: len(req.Rows), Errors: []importErr{}}
	errRollback := errors.New("rollback")

	err := db.InTenant(r.Context(), s.Pool, scope(r), func(tx pgx.Tx) error {
		if _, err := companyBase(r.Context(), tx, cid); err != nil {
			return err
		}
		im := &importer{ctx: r.Context(), tx: tx, cid: cid, req: req, res: res, bad: map[int]bool{}, opts: req.Options}
		var write func() error
		var err error
		switch kind {
		case "customers":
			write, err = im.customers()
		case "subscriptions":
			write, err = im.subscriptions()
		case "opening_balances":
			write, err = im.openingBalances()
		case "readings":
			write, err = im.readings()
		case "accounts":
			write, err = im.accounts()
		}
		if err != nil {
			return err
		}
		res.Valid = res.Total - len(im.bad)
		if req.DryRun || len(im.bad) > 0 {
			return errRollback
		}
		return write()
	})
	res.TookMS = time.Since(start).Milliseconds()
	switch {
	case errors.Is(err, errRollback):
		if req.DryRun {
			writeJSON(w, http.StatusOK, res)
		} else {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "import_errors", "result": res})
		}
	case err != nil:
		fail(w, r, err)
	default:
		writeJSON(w, http.StatusOK, res)
	}
}

// lookup returns a lower(key) -> id map.
func (im *importer) lookup(sql string, args ...any) (map[string]string, error) {
	rows, err := im.tx.Query(im.ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[strings.ToLower(strings.TrimSpace(k))] = v
	}
	return out, rows.Err()
}

func (im *importer) sendBatch(b *pgx.Batch) error {
	if b.Len() == 0 {
		return nil
	}
	return im.tx.SendBatch(im.ctx, b).Close()
}

// ---- customers ----

func (im *importer) customers() (func() error, error) {
	existing, err := im.lookup(`SELECT code, id::text FROM customers WHERE company_id = $1`, im.cid)
	if err != nil {
		return nil, err
	}
	areas, err := im.lookup(`SELECT name, id::text FROM areas WHERE company_id = $1`, im.cid)
	if err != nil {
		return nil, err
	}
	update := im.opts["update_existing"] == "true"
	seen := map[string]int{}
	newAreas := map[string]string{} // lower -> display name
	for i, row := range im.req.Rows {
		n := i + 1
		code, name := cell(row, "code"), cell(row, "name")
		if name == "" {
			im.fail(n, "name", "required", "")
		}
		if len(code) > 30 {
			im.fail(n, "code", "too_long", code)
		}
		if code != "" {
			lc := strings.ToLower(code)
			if p, ok := seen[lc]; ok {
				im.fail(n, "code", "duplicate_in_file", fmt.Sprintf("%s (row %d)", code, p))
			}
			seen[lc] = n
			if _, ok := existing[lc]; ok && !update {
				im.fail(n, "code", "code_exists", code)
			}
		}
		if _, ok := statusWords[strings.ToLower(cell(row, "status"))]; !ok {
			im.fail(n, "status", "invalid", cell(row, "status"))
		}
		if a := cell(row, "area"); a != "" {
			if _, ok := areas[strings.ToLower(a)]; !ok {
				newAreas[strings.ToLower(a)] = a
			}
		}
	}
	return func() error {
		for lc, name := range newAreas {
			var id string
			if err := im.tx.QueryRow(im.ctx, `INSERT INTO areas (company_id, name) VALUES ($1, $2) RETURNING id::text`, im.cid, name).Scan(&id); err != nil {
				return err
			}
			areas[lc] = id
			im.res.CreatedAreas++
		}
		b := &pgx.Batch{}
		flush := func() error {
			err := im.sendBatch(b)
			b = &pgx.Batch{}
			return err
		}
		for _, row := range im.req.Rows {
			code := cell(row, "code")
			area := areas[strings.ToLower(cell(row, "area"))]
			status := statusWords[strings.ToLower(cell(row, "status"))]
			if status == "ended" {
				status = "closed"
			}
			if id, ok := existing[strings.ToLower(code)]; ok && code != "" {
				b.Queue(`UPDATE customers SET name = $2, phone = $3, phone2 = $4, email = $5, address = $6,
					area_id = coalesce(nullif($7, '')::uuid, area_id), status = $8, notes = CASE WHEN $9 = '' THEN notes ELSE $9 END
					WHERE id = $1`, id, cell(row, "name"), cell(row, "phone"), cell(row, "phone2"), cell(row, "email"),
					cell(row, "address"), area, status, cell(row, "notes"))
				im.res.Updated++
			} else {
				b.Queue(`INSERT INTO customers (company_id, code, name, phone, phone2, email, address, area_id, status, notes)
					VALUES ($1, coalesce(nullif($2, ''), next_doc_number($1, 'customer')::text), $3, $4, $5, $6, $7, nullif($8, '')::uuid, $9, $10)`,
					im.cid, code, cell(row, "name"), cell(row, "phone"), cell(row, "phone2"), cell(row, "email"),
					cell(row, "address"), area, status, cell(row, "notes"))
				im.res.Created++
			}
			if b.Len() >= 500 {
				if err := flush(); err != nil {
					return err
				}
			}
		}
		return flush()
	}, nil
}

// ---- subscriptions ----

func (im *importer) subscriptions() (func() error, error) {
	customers, err := im.lookup(`SELECT code, id::text FROM customers WHERE company_id = $1`, im.cid)
	if err != nil {
		return nil, err
	}
	plans, err := im.lookup(`SELECT name, id::text FROM plans WHERE company_id = $1 AND is_active`, im.cid)
	if err != nil {
		return nil, err
	}
	type sub struct{ customer, plan, meter, start, fee, initial, status string }
	subs := make([]sub, len(im.req.Rows))
	active := 0
	for i, row := range im.req.Rows {
		n := i + 1
		x := sub{meter: cell(row, "meter_no"), start: time.Now().Format(time.DateOnly), initial: "0"}
		var ok bool
		if x.customer, ok = customers[strings.ToLower(cell(row, "customer_code"))]; !ok {
			im.fail(n, "customer_code", "not_found", cell(row, "customer_code"))
		}
		if x.plan, ok = plans[strings.ToLower(cell(row, "plan"))]; !ok {
			im.fail(n, "plan", "not_found", cell(row, "plan"))
		}
		if v := cell(row, "start_date"); v != "" {
			if x.start, ok = date(v); !ok {
				im.fail(n, "start_date", "invalid_date", v)
			}
		}
		if v := cell(row, "fee_override"); v != "" {
			d, ok := num(v)
			if !ok {
				im.fail(n, "fee_override", "invalid_number", v)
			}
			x.fee = d.String()
		}
		if v := cell(row, "initial_reading"); v != "" {
			d, ok := num(v)
			if !ok {
				im.fail(n, "initial_reading", "invalid_number", v)
			}
			x.initial = d.String()
		}
		st, ok := statusWords[strings.ToLower(cell(row, "status"))]
		if !ok {
			im.fail(n, "status", "invalid", cell(row, "status"))
		}
		if st == "closed" {
			st = "ended"
		}
		x.status = st
		if st == "active" {
			active++
		}
		subs[i] = x
	}
	// plan limit for the whole file
	if active > 0 && len(im.bad) == 0 {
		var max *int
		var plan string
		var trialEnds *time.Time
		if err := im.tx.QueryRow(im.ctx, `SELECT plan, max_subscribers, trial_ends_at FROM tenants WHERE id = app_tenant()`).Scan(&plan, &max, &trialEnds); err != nil {
			return nil, err
		}
		if plan == "trial" && trialEnds != nil && trialEnds.Before(time.Now()) {
			return nil, &apiError{status: http.StatusPaymentRequired, code: "trial_expired"}
		}
		if max != nil {
			var cur int
			if err := im.tx.QueryRow(im.ctx, `SELECT count(*) FROM subscriptions WHERE status = 'active'`).Scan(&cur); err != nil {
				return nil, err
			}
			if cur+active > *max {
				return nil, &apiError{status: http.StatusPaymentRequired, code: "plan_limit", detail: strconv.Itoa(*max)}
			}
		}
	}
	return func() error {
		b := &pgx.Batch{}
		for _, x := range subs {
			b.Queue(`INSERT INTO subscriptions (company_id, customer_id, plan_id, meter_no, start_date, fee_override, initial_reading, status)
				VALUES ($1, $2, $3, $4, $5::date, nullif($6, '')::numeric, $7::numeric, $8)`,
				im.cid, x.customer, x.plan, x.meter, x.start, x.fee, x.initial, x.status)
			im.res.Created++
			if b.Len() >= 500 {
				if err := im.sendBatch(b); err != nil {
					return err
				}
				b = &pgx.Batch{}
			}
		}
		return im.sendBatch(b)
	}, nil
}

// ---- opening balances (debts carried over from the old system) ----

func (im *importer) openingBalances() (func() error, error) {
	set, err := loadBillingSettings(im.ctx, im.tx, im.cid)
	if err != nil {
		return nil, err
	}
	if err := set.requireAR(); err != nil {
		return nil, err
	}
	account := im.opts["account_id"]
	if account == "" {
		_ = im.tx.QueryRow(im.ctx, `SELECT id::text FROM accounts WHERE company_id = $1 AND code IN ('12', '3200') AND is_postable ORDER BY code LIMIT 1`, im.cid).Scan(&account)
	}
	if account == "" && set.Revenue != nil {
		account = *set.Revenue
	}
	if !isUUID(account) {
		return nil, &apiError{status: http.StatusUnprocessableEntity, code: "billing_not_setup", detail: "opening_account"}
	}
	if err := requirePostable(im.ctx, im.tx, im.cid, account); err != nil {
		return nil, err
	}
	type cust struct{ id, name string }
	custs := map[string]cust{}
	rows, err := im.tx.Query(im.ctx, `SELECT lower(code), id::text, name FROM customers WHERE company_id = $1`, im.cid)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k string
		var c cust
		if err := rows.Scan(&k, &c.id, &c.name); err != nil {
			rows.Close()
			return nil, err
		}
		custs[k] = c
	}
	rows.Close()
	currencies, err := im.lookup(`SELECT code, decimals::text FROM currencies`)
	if err != nil {
		return nil, err
	}
	type ob struct {
		c               cust
		cur, date, desc string
		amount          decimal.Decimal
	}
	obs := make([]ob, len(im.req.Rows))
	defaultDate := time.Now().Format(time.DateOnly)
	if v, ok := date(im.opts["date"]); ok {
		defaultDate = v
	}
	needRate := map[string]string{}
	for i, row := range im.req.Rows {
		n := i + 1
		x := ob{date: defaultDate, desc: cell(row, "description")}
		var ok bool
		if x.c, ok = custs[strings.ToLower(cell(row, "customer_code"))]; !ok {
			im.fail(n, "customer_code", "not_found", cell(row, "customer_code"))
		}
		x.cur = strings.ToUpper(cell(row, "currency"))
		if x.cur == "" {
			x.cur = set.Base
		}
		decS, ok := currencies[strings.ToLower(x.cur)]
		if !ok {
			im.fail(n, "currency", "invalid", x.cur)
		}
		amt, ok := num(cell(row, "amount"))
		if !ok || !amt.IsPositive() {
			im.fail(n, "amount", "invalid_number", cell(row, "amount"))
		}
		d, _ := strconv.Atoi(decS)
		x.amount = amt.Round(int32(d))
		if v := cell(row, "date"); v != "" {
			if x.date, ok = date(v); !ok {
				im.fail(n, "date", "invalid_date", v)
			}
		}
		if x.desc == "" {
			x.desc = "Opening balance"
		}
		if x.cur != set.Base {
			if old, ok := needRate[x.cur]; !ok || x.date < old {
				needRate[x.cur] = x.date
			}
		}
		obs[i] = x
	}
	// foreign currencies need an exchange rate on or before the earliest date used
	for cur, d := range needRate {
		if _, err := resolveRate(im.ctx, im.tx, im.cid, set.Base, cur, d, ""); err != nil {
			im.fail(0, "currency", "no_rate", cur+" "+d)
		}
	}
	return func() error {
		for _, x := range obs {
			if _, _, err := issueInvoice(im.ctx, im.tx, im.cid, set, x.c.id, x.c.name, nil, nil, x.date, x.date, x.cur, account,
				"Opening balance (import)", []invLine{{Kind: "other", Description: x.desc, Quantity: decimal.NewFromInt(1), UnitPrice: x.amount, Amount: x.amount}}); err != nil {
				return err
			}
			im.res.Created++
		}
		return nil
	}, nil
}

// ---- meter readings ----

func (im *importer) readings() (func() error, error) {
	byCustomer, err := im.lookup(`SELECT DISTINCT ON (c.code) c.code, s.id::text FROM subscriptions s
		JOIN customers c ON c.id = s.customer_id JOIN plans p ON p.id = s.plan_id AND p.billing_type = 'metered'
		WHERE s.company_id = $1 AND s.status = 'active' ORDER BY c.code, s.start_date`, im.cid)
	if err != nil {
		return nil, err
	}
	byMeter, err := im.lookup(`SELECT meter_no, id::text FROM subscriptions WHERE company_id = $1 AND meter_no <> '' AND status = 'active'`, im.cid)
	if err != nil {
		return nil, err
	}
	type rd struct{ sub, period, value string }
	list := make([]rd, len(im.req.Rows))
	seen := map[string]int{}
	for i, row := range im.req.Rows {
		n := i + 1
		var x rd
		var ok bool
		if m := cell(row, "meter_no"); m != "" {
			if x.sub, ok = byMeter[strings.ToLower(m)]; !ok {
				im.fail(n, "meter_no", "not_found", m)
			}
		} else if x.sub, ok = byCustomer[strings.ToLower(cell(row, "customer_code"))]; !ok {
			im.fail(n, "customer_code", "not_found", cell(row, "customer_code"))
		}
		if x.period, ok = period(cell(row, "period")); !ok {
			im.fail(n, "period", "invalid_period", cell(row, "period"))
		}
		v, ok := num(cell(row, "reading"))
		if !ok {
			im.fail(n, "reading", "invalid_number", cell(row, "reading"))
		}
		x.value = v.String()
		key := x.sub + "|" + x.period
		if p, dup := seen[key]; dup && x.sub != "" {
			im.fail(n, "period", "duplicate_in_file", fmt.Sprintf("row %d", p))
		}
		seen[key] = n
		list[i] = x
	}
	// check against previous readings and already-billed periods
	for i, x := range list {
		if x.sub == "" || x.period == "" || im.bad[i+1] {
			continue
		}
		var prev string
		var invoiced bool
		if err := im.tx.QueryRow(im.ctx, `SELECT coalesce((SELECT r.reading FROM meter_readings r WHERE r.subscription_id = s.id AND r.period < $2
				ORDER BY r.period DESC LIMIT 1), s.initial_reading)::text,
			EXISTS (SELECT 1 FROM invoices i WHERE i.subscription_id = s.id AND i.period = $2 AND i.status <> 'void')
			FROM subscriptions s WHERE s.id = $1`, x.sub, x.period).Scan(&prev, &invoiced); err != nil {
			return nil, err
		}
		if invoiced {
			im.fail(i+1, "period", "already_invoiced", x.period)
			continue
		}
		p, _ := decimal.NewFromString(prev)
		v, _ := decimal.NewFromString(x.value)
		if v.LessThan(p) {
			im.fail(i+1, "reading", "reading_below_previous", prev)
		}
	}
	return func() error {
		b := &pgx.Batch{}
		for _, x := range list {
			b.Queue(`INSERT INTO meter_readings (subscription_id, period, reading) VALUES ($1, $2, $3::numeric)
				ON CONFLICT (tenant_id, subscription_id, period) DO UPDATE SET reading = EXCLUDED.reading, read_at = now(), read_by = app_user()`,
				x.sub, x.period, x.value)
			im.res.Created++
			if b.Len() >= 500 {
				if err := im.sendBatch(b); err != nil {
					return err
				}
				b = &pgx.Batch{}
			}
		}
		return im.sendBatch(b)
	}, nil
}

// ---- chart of accounts additions ----

var typeWords = map[string]string{
	"asset": "asset", "assets": "asset", "actif": "asset", "أصول": "asset", "اصول": "asset",
	"liability": "liability", "liabilities": "liability", "passif": "liability", "خصوم": "liability",
	"equity": "equity", "capitaux propres": "equity", "حقوق ملكية": "equity",
	"income": "income", "revenue": "income", "produit": "income", "إيرادات": "income", "ايرادات": "income",
	"expense": "expense", "expenses": "expense", "charge": "expense", "مصاريف": "expense",
}

func (im *importer) accounts() (func() error, error) {
	existing, err := im.lookup(`SELECT code, id::text FROM accounts WHERE company_id = $1`, im.cid)
	if err != nil {
		return nil, err
	}
	inFile := map[string]int{}
	for i, row := range im.req.Rows {
		n := i + 1
		code := cell(row, "code")
		if !accountCodeRe.MatchString(code) {
			im.fail(n, "code", "invalid", code)
		}
		if _, ok := existing[strings.ToLower(code)]; ok {
			im.fail(n, "code", "code_exists", code)
		}
		if p, ok := inFile[strings.ToLower(code)]; ok {
			im.fail(n, "code", "duplicate_in_file", fmt.Sprintf("row %d", p))
		}
		inFile[strings.ToLower(code)] = n
		if cell(row, "name") == "" {
			im.fail(n, "name", "required", "")
		}
		if _, ok := typeWords[strings.ToLower(cell(row, "type"))]; !ok {
			im.fail(n, "type", "invalid", cell(row, "type"))
		}
		if p := strings.ToLower(cell(row, "parent_code")); p != "" {
			_, inDB := existing[p]
			row, inF := inFile[p]
			if !inDB && (!inF || row >= n) {
				im.fail(n, "parent_code", "not_found", cell(im.req.Rows[i], "parent_code"))
			}
		}
	}
	return func() error {
		for _, row := range im.req.Rows {
			postable := true
			switch strings.ToLower(cell(row, "is_postable")) {
			case "0", "no", "false", "non", "لا", "group", "header":
				postable = false
			}
			var id string
			if err := im.tx.QueryRow(im.ctx, `INSERT INTO accounts (company_id, code, name, type, parent_id, is_postable)
				VALUES ($1, $2, $3, $4, nullif($5, '')::uuid, $6) RETURNING id::text`,
				im.cid, cell(row, "code"), cell(row, "name"), typeWords[strings.ToLower(cell(row, "type"))],
				existing[strings.ToLower(cell(row, "parent_code"))], postable).Scan(&id); err != nil {
				return err
			}
			existing[strings.ToLower(cell(row, "code"))] = id
			im.res.Created++
		}
		return nil
	}, nil
}
