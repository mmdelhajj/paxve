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

// Dunning: overdue bills climb reminder levels (for example day 3 a friendly WhatsApp, day 10 a second reminder with
// a late fee, day 20 the internet / power is cut). Each level is applied once per invoice; the run is idempotent and
// also runs every day by itself for companies that switched it on.

const dunningLevelCols = `id, level, name, days_overdue, send_message, message, fee_kind, fee_amount::text AS fee_amount, fee_currency,
	min_balance::text AS min_balance, cut_service, is_active`

// GET /companies/{cid}/dunning: settings and levels.
func (s *Server) getDunning(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		set, err := queryJSONOne(ctx, tx, `SELECT dunning_enabled AS enabled, late_fee_account_id, base_currency::text AS base_currency
			FROM companies WHERE id = $1`, cid)
		if err != nil {
			return nil, err
		}
		lv, err := queryJSON(ctx, tx, `SELECT `+dunningLevelCols+` FROM dunning_levels WHERE company_id = $1 ORDER BY level`, cid)
		if err != nil {
			return nil, err
		}
		return map[string]any{"settings": set, "levels": lv}, nil
	})
}

// PUT /companies/{cid}/dunning {enabled, late_fee_account_id, levels: [...]}: replaces the levels.
func (s *Server) putDunning(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled          bool    `json:"enabled"`
		LateFeeAccountID *string `json:"late_fee_account_id"`
		Levels           []struct {
			Level       int    `json:"level"`
			Name        string `json:"name"`
			DaysOverdue int    `json:"days_overdue"`
			SendMessage *bool  `json:"send_message"`
			Message     string `json:"message"`
			FeeKind     string `json:"fee_kind"`
			FeeAmount   string `json:"fee_amount"`
			FeeCurrency string `json:"fee_currency"`
			MinBalance  string `json:"min_balance"`
			CutService  bool   `json:"cut_service"`
			IsActive    *bool  `json:"is_active"`
		} `json:"levels"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if (in.LateFeeAccountID != nil && *in.LateFeeAccountID != "" && !isUUID(*in.LateFeeAccountID)) || len(in.Levels) > 9 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	seen := map[int]bool{}
	lastDays := -1
	for _, l := range in.Levels {
		if l.FeeKind == "" {
			l.FeeKind = "none"
		}
		fee, okFee := money(orZero(l.FeeAmount))
		mb, okMB := money(orZero(l.MinBalance))
		if l.Level < 1 || l.Level > 9 || seen[l.Level] || strings.TrimSpace(l.Name) == "" || l.DaysOverdue < 0 || l.DaysOverdue > 3650 ||
			(l.FeeKind != "none" && l.FeeKind != "fixed" && l.FeeKind != "percent") || !okFee || fee.IsNegative() || !okMB || mb.IsNegative() ||
			(l.FeeKind == "fixed" && fee.IsPositive() && !currencyRe.MatchString(l.FeeCurrency)) || (l.FeeKind == "percent" && fee.GreaterThan(decimal.NewFromInt(100))) ||
			l.DaysOverdue <= lastDays || len(l.Message) > 2000 {
			writeErr(w, http.StatusBadRequest, "invalid_level")
			return
		}
		seen[l.Level] = true
		lastDays = l.DaysOverdue
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		acc := ""
		if in.LateFeeAccountID != nil {
			acc = *in.LateFeeAccountID
		}
		if acc != "" {
			if err := requirePostable(ctx, tx, cid, acc); err != nil {
				return nil, err
			}
		}
		tag, err := tx.Exec(ctx, `UPDATE companies SET dunning_enabled = $2,
			late_fee_account_id = CASE WHEN $3::text IS NULL THEN late_fee_account_id ELSE nullif($3, '')::uuid END WHERE id = $1`,
			cid, in.Enabled, in.LateFeeAccountID)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		if in.Levels != nil {
			if _, err := tx.Exec(ctx, `DELETE FROM dunning_levels WHERE company_id = $1`, cid); err != nil {
				return nil, err
			}
			for _, l := range in.Levels {
				fk := l.FeeKind
				if fk == "" {
					fk = "none"
				}
				send, active := true, true
				if l.SendMessage != nil {
					send = *l.SendMessage
				}
				if l.IsActive != nil {
					active = *l.IsActive
				}
				if _, err := tx.Exec(ctx, `INSERT INTO dunning_levels (company_id, level, name, days_overdue, send_message, message, fee_kind, fee_amount,
					fee_currency, min_balance, cut_service, is_active) VALUES ($1, $2, trim($3), $4, $5, $6, $7, $8::numeric, nullif($9, ''), $10::numeric, $11, $12)`,
					cid, l.Level, l.Name, l.DaysOverdue, send, strings.TrimSpace(l.Message), fk, orZero(l.FeeAmount), l.FeeCurrency, orZero(l.MinBalance),
					l.CutService, active); err != nil {
					return nil, err
				}
			}
		}
		return queryJSON(ctx, tx, `SELECT `+dunningLevelCols+` FROM dunning_levels WHERE company_id = $1 ORDER BY level`, cid)
	})
}

func orZero(s string) string {
	if strings.TrimSpace(s) == "" {
		return "0"
	}
	return s
}

type dunLevel struct {
	level, days                    int
	name, message, feeKind, feeCur string
	send, cut                      bool
	fee, minBalance                decimal.Decimal
}

type dunDue struct {
	invoiceID, customerID, customerName, currency string
	number                                        int64
	days                                          int
	balance                                       decimal.Decimal
	lv                                            dunLevel
}

// dunningDue lists, for every overdue open invoice, the highest active level it reached that was not applied yet.
// Late-fee invoices themselves are not dunned again.
func dunningDue(ctx context.Context, tx pgx.Tx, cid, asOf string) ([]dunDue, error) {
	rows, err := tx.Query(ctx, `SELECT level, days_overdue, name, message, fee_kind, coalesce(fee_currency::text, ''), send_message, cut_service,
		fee_amount::text, min_balance::text FROM dunning_levels WHERE company_id = $1 AND is_active ORDER BY level`, cid)
	if err != nil {
		return nil, err
	}
	levels, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (dunLevel, error) {
		var l dunLevel
		var fee, mb string
		err := row.Scan(&l.level, &l.days, &l.name, &l.message, &l.feeKind, &l.feeCur, &l.send, &l.cut, &fee, &mb)
		l.fee, _ = decimal.NewFromString(fee)
		l.minBalance, _ = decimal.NewFromString(mb)
		return l, err
	})
	if err != nil || len(levels) == 0 {
		return nil, err
	}
	rows, err = tx.Query(ctx, `SELECT i.id::text, i.customer_id::text, c.name, i.currency::text, i.number, ($2::date - i.due_date)::int,
		(i.total - i.paid)::text, coalesce((SELECT max(e.level) FROM dunning_events e WHERE e.invoice_id = i.id), 0)
		FROM invoices i JOIN customers c ON c.id = i.customer_id
		WHERE i.company_id = $1 AND i.status IN ('open', 'partial') AND i.kind = 'invoice' AND i.due_date < $2::date
		  AND NOT EXISTS (SELECT 1 FROM dunning_events e WHERE e.fee_invoice_id = i.id)
		ORDER BY i.due_date, i.number LIMIT 5000`, cid, asOf)
	if err != nil {
		return nil, err
	}
	type inv struct {
		id, cust, name, cur string
		number              int64
		days, done          int
		bal                 decimal.Decimal
	}
	invs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (inv, error) {
		var x inv
		var b string
		err := row.Scan(&x.id, &x.cust, &x.name, &x.cur, &x.number, &x.days, &b, &x.done)
		x.bal, _ = decimal.NewFromString(b)
		return x, err
	})
	if err != nil {
		return nil, err
	}
	out := []dunDue{}
	for _, x := range invs {
		var hit *dunLevel
		for i := range levels {
			l := levels[i]
			if x.days >= l.days && l.level > x.done && x.bal.GreaterThanOrEqual(l.minBalance) {
				hit = &levels[i]
			}
		}
		if hit != nil {
			out = append(out, dunDue{invoiceID: x.id, customerID: x.cust, customerName: x.name, currency: x.cur, number: x.number,
				days: x.days, balance: x.bal, lv: *hit})
		}
	}
	return out, nil
}

func (d dunDue) feeAmount() (decimal.Decimal, string) {
	switch d.lv.feeKind {
	case "fixed":
		return d.lv.fee, d.lv.feeCur
	case "percent":
		return d.balance.Mul(d.lv.fee).Div(decimal.NewFromInt(100)), d.currency
	}
	return decimal.Zero, ""
}

// GET /companies/{cid}/dunning/preview: what a run would do today.
func (s *Server) previewDunning(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		list, err := dunningDue(ctx, tx, cid, today())
		if err != nil {
			return nil, err
		}
		rows := []map[string]any{}
		for _, d := range list {
			fee, cur := d.feeAmount()
			rows = append(rows, map[string]any{"invoice_id": d.invoiceID, "number": d.number, "customer_id": d.customerID, "customer_name": d.customerName,
				"currency": d.currency, "balance": d.balance.String(), "days_overdue": d.days, "level": d.lv.level, "level_name": d.lv.name,
				"fee": fee.StringFixed(2), "fee_currency": cur, "send_message": d.lv.send, "cut_service": d.lv.cut})
		}
		return map[string]any{"rows": rows}, nil
	})
}

// POST /companies/{cid}/dunning/run: applies the due levels now.
func (s *Server) runDunningNow(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := companyBase(ctx, tx, cid); err != nil {
			return nil, err
		}
		return applyDunning(ctx, tx, cid)
	})
}

func applyDunning(ctx context.Context, tx pgx.Tx, cid string) (map[string]any, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('dunning:' || $1))`, cid); err != nil {
		return nil, err
	}
	list, err := dunningDue(ctx, tx, cid, today())
	if err != nil {
		return nil, err
	}
	set, err := loadBillingSettings(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	var feeAcc *string
	var msgApp bool
	if err := tx.QueryRow(ctx, `SELECT late_fee_account_id::text, 'msg' = ANY (apps) FROM companies WHERE id = $1`, cid).Scan(&feeAcc, &msgApp); err != nil {
		return nil, err
	}
	if feeAcc == nil {
		feeAcc = set.Revenue
	}
	applied, fees, msgs, cuts := 0, 0, 0, 0
	feeTotals := map[string]decimal.Decimal{}
	for _, d := range list {
		var feeInv *string
		fee, feeCur := d.feeAmount()
		if fee.IsPositive() {
			if feeAcc == nil {
				return nil, notSetup("late_fee_account")
			}
			dec, err := currencyDecimals(ctx, tx, feeCur)
			if err != nil {
				return nil, err
			}
			fee = fee.Round(dec)
			if fee.IsPositive() {
				if _, err := lockCustomer(ctx, tx, cid, d.customerID); err != nil {
					return nil, err
				}
				id, _, err := issueInvoice(quietCtx(ctx), tx, cid, set, d.customerID, d.customerName, nil, nil, today(), today(), feeCur, *feeAcc,
					fmt.Sprintf("Late fee · invoice #%d · %s", d.number, d.lv.name),
					[]invLine{{Kind: "fee", Description: fmt.Sprintf("Late payment fee — invoice #%d (%d days)", d.number, d.days),
						Quantity: decimal.NewFromInt(1), UnitPrice: fee, Amount: fee}})
				if err != nil {
					return nil, err
				}
				feeInv = &id
				fees++
				feeTotals[feeCur] = feeTotals[feeCur].Add(fee)
			}
		}
		queued := false
		if d.lv.send && msgApp {
			ok, err := dunningMessage(ctx, tx, cid, d)
			if err != nil {
				return nil, err
			}
			queued = ok
			if ok {
				msgs++
			}
		}
		cut := false
		if d.lv.cut {
			n, err := cutCustomerService(ctx, tx, cid, d.customerID)
			if err != nil {
				return nil, err
			}
			cut = n > 0
			cuts += n
		}
		if _, err := tx.Exec(ctx, `INSERT INTO dunning_events (company_id, invoice_id, customer_id, level, days_overdue, balance, currency, fee_invoice_id,
			message_queued, service_cut) VALUES ($1, $2, $3, $4, $5, $6::numeric, $7, $8, $9, $10) ON CONFLICT (invoice_id, level) DO NOTHING`,
			cid, d.invoiceID, d.customerID, d.lv.level, d.days, d.balance.String(), d.currency, feeInv, queued, cut); err != nil {
			return nil, err
		}
		applied++
	}
	ft := map[string]string{}
	for c, v := range feeTotals {
		ft[c] = v.String()
	}
	return map[string]any{"applied": applied, "fee_invoices": fees, "fee_totals": ft, "messages": msgs, "services_cut": cuts}, nil
}

// dunningMessage queues the level's message (or the "reminder" template) for the customer.
func dunningMessage(ctx context.Context, tx pgx.Tx, cid string, d dunDue) (bool, error) {
	if err := seedTemplates(ctx, tx, cid); err != nil {
		return false, err
	}
	var lang string
	if err := tx.QueryRow(ctx, `SELECT coalesce(c.locale, co.default_locale) FROM customers c JOIN companies co ON co.id = c.company_id
		WHERE c.id = $1`, d.customerID).Scan(&lang); err != nil {
		return false, err
	}
	rd, err := newRenderer(ctx, tx, cid, lang)
	if err != nil {
		return false, err
	}
	m, err := rd.customerContext(ctx, tx, d.customerID, &d.invoiceID, nil)
	if err != nil {
		return false, err
	}
	m.Vars["days_overdue"] = fmt.Sprint(d.days)
	m.Vars["level"] = d.lv.name
	var tpl *tplRow
	body := d.lv.message
	if strings.TrimSpace(body) == "" {
		if tpl, err = pickTemplate(ctx, tx, cid, "reminder", "", lang); err != nil {
			return false, nil
		}
		rd.locale = tpl.Locale
		m.Vars["company"] = rd.company()
	}
	prov, err := pickProvider(ctx, tx, cid, "")
	if err != nil {
		return false, err
	}
	ok, _, _, err := queueMessage(ctx, tx, rd, m, "reminder", tpl, body, prov, "whatsapp")
	return ok, err
}

// cutCustomerService suspends the customer's active subscriptions for debt: internet ones also get a router action;
// generator / satellite ones are listed for the field team (status suspended, reason overdue). Paying clears them
// through the normal reconnect rule.
func cutCustomerService(ctx context.Context, tx pgx.Tx, cid, customerID string) (int, error) {
	rows, err := tx.Query(ctx, `SELECT s.id::text, coalesce(s.router_id::text, ''), s.network_username FROM subscriptions s
		WHERE s.company_id = $1 AND s.customer_id = $2 AND s.status = 'active' FOR UPDATE`, cid, customerID)
	if err != nil {
		return 0, err
	}
	type sub struct{ id, router, user string }
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (sub, error) {
		var x sub
		err := r.Scan(&x.id, &x.router, &x.user)
		return x, err
	})
	if err != nil {
		return 0, err
	}
	for _, x := range list {
		if _, err := tx.Exec(ctx, `UPDATE subscriptions SET status = 'suspended', suspended_reason = 'overdue' WHERE id = $1`, x.id); err != nil {
			return 0, err
		}
		if x.router != "" && x.user != "" {
			if err := queueRouterAction(ctx, tx, cid, x.id, x.router, "disable", "overdue"); err != nil {
				return 0, err
			}
		}
	}
	return len(list), nil
}

// GET /companies/{cid}/dunning/events?customer_id=
func (s *Server) listDunningEvents(w http.ResponseWriter, r *http.Request) {
	cust := r.URL.Query().Get("customer_id")
	if cust != "" && !isUUID(cust) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT e.id, e.created_at, e.level, (SELECT l.name FROM dunning_levels l WHERE l.company_id = e.company_id AND l.level = e.level) AS level_name,
			e.invoice_id, i.number AS invoice_number, e.customer_id, c.name AS customer_name, c.code AS customer_code, e.days_overdue,
			e.balance::text AS balance, e.currency, e.fee_invoice_id, fi.number AS fee_invoice_number, fi.total::text AS fee_total, fi.currency AS fee_currency,
			e.message_queued, e.service_cut
			FROM dunning_events e JOIN invoices i ON i.id = e.invoice_id JOIN customers c ON c.id = e.customer_id
			LEFT JOIN invoices fi ON fi.id = e.fee_invoice_id
			WHERE e.company_id = $1 AND ($2 = '' OR e.customer_id = nullif($2, '')::uuid)
			ORDER BY e.created_at DESC LIMIT 1000`, r.PathValue("cid"), cust)
	})
}

// ---------- daily worker ----------

func (s *Server) StartDunningWorker(ctx context.Context) {
	if s.OwnerPool == nil {
		return
	}
	go func() {
		timer := time.NewTimer(2 * time.Minute)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				if err := s.RunDunning(ctx); err != nil {
					slog.Error("dunning worker", "err", err)
				}
				if err := s.RunDeferrals(ctx); err != nil {
					slog.Error("deferrals worker", "err", err)
				}
				if err := s.RunLeaveAccruals(ctx); err != nil {
					slog.Error("leave accrual worker", "err", err)
				}
				timer.Reset(6 * time.Hour)
			}
		}
	}()
}

// RunDunning applies due dunning levels for every company that switched dunning on.
func (s *Server) RunDunning(ctx context.Context) error {
	rows, err := s.OwnerPool.Query(ctx, `SELECT c.id::text, c.tenant_id::text FROM companies c
		WHERE c.dunning_enabled AND c.is_active AND tenant_jobs_ok(c.tenant_id) AND EXISTS (SELECT 1 FROM dunning_levels l WHERE l.company_id = c.id AND l.is_active)`)
	if err != nil {
		return err
	}
	type co struct{ id, tenant string }
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (co, error) {
		var x co
		err := r.Scan(&x.id, &x.tenant)
		return x, err
	})
	if err != nil {
		return err
	}
	for _, c := range list {
		err := pgx.BeginFunc(ctx, s.OwnerPool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, c.tenant); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `SET LOCAL ROLE paxve_app`); err != nil {
				return err
			}
			res, err := applyDunning(ctx, tx, c.id)
			if err == nil && res["applied"].(int) > 0 {
				slog.Info("dunning applied", "company", c.id, "count", res["applied"])
			}
			return err
		})
		var ae *apiError
		if err != nil && !errors.As(err, &ae) {
			slog.Error("dunning failed", "company", c.id, "err", err)
		} else if err != nil {
			slog.Warn("dunning skipped", "company", c.id, "code", ae.code)
		}
	}
	return nil
}
