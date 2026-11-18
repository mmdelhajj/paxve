package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/mmdelhajj/paxve/internal/db"
)

// Monthly billing run. Small runs are done inside the request; large ones (more than billingSyncLimit
// subscriptions, or async: true) become a background job processed in chunks of billingChunk
// subscriptions, one transaction per chunk. Each chunk records its progress and a cursor in
// billing_jobs, so a restart resumes where it stopped, and re-running a period never bills a
// subscription twice (invoices_one_per_period).

const billingSyncLimit = 1500

var billingChunk = 500 // subscriptions per transaction in background runs (a variable so tests can use small chunks)

type billingParams struct {
	Period    string `json:"period"`
	IssueDate string `json:"issue_date"`
	DueDate   string `json:"due_date"`
	AreaID    string `json:"area_id"`
	StationID string `json:"station_id"`
	BoxID     string `json:"box_id"`
}

type billSkip struct {
	Customer string `json:"customer"`
	Reason   string `json:"reason"`
}

type billBatchResult struct {
	processed int
	created   int
	skips     []billSkip
	totals    map[string]decimal.Decimal
	lastName  string
	lastID    string
}

const billingWhere = `s.company_id = $1 AND s.status = 'active' AND c.status = 'active' AND s.start_date <= $3::date
	AND ($4 = '' OR c.area_id = $4::uuid)
	AND ($5 = '' OR s.box_id = $5::uuid)
	AND ($6 = '' OR s.box_id IN (SELECT id FROM boxes WHERE station_id = $6::uuid))
	AND NOT EXISTS (SELECT 1 FROM invoices i WHERE i.subscription_id = s.id AND i.period = $2 AND i.status <> 'void')`

func countBillable(ctx context.Context, tx pgx.Tx, cid string, p billingParams) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM subscriptions s JOIN customers c ON c.id = s.customer_id WHERE `+billingWhere,
		cid, p.Period, periodEnd(p.Period), p.AreaID, p.BoxID, p.StationID).Scan(&n)
	return n, err
}

// billBatch bills up to limit subscriptions after the (name, id) cursor, in customer-name order.
func billBatch(ctx context.Context, tx pgx.Tx, cid string, set *billingSettings, p billingParams, afterName, afterID *string, limit int) (*billBatchResult, error) {
	type sub struct {
		ID, Customer, CustomerName, Plan, PlanName, Type, Currency, Revenue, UnitLabel string
		Fee, Price, Initial                                                            string
		Override, Current, Previous, TaxRateID                                         *string
	}
	rows, err := tx.Query(ctx, `
		SELECT s.id::text, s.customer_id::text, c.name, p.id::text, p.name, p.billing_type, p.currency::text,
		       p.revenue_account_id::text, p.unit_label, p.base_fee::text, p.unit_price::text, s.initial_reading::text,
		       s.fee_override::text, p.tax_rate_id::text,
		       (SELECT r.reading::text FROM meter_readings r WHERE r.subscription_id = s.id AND r.period = $2),
		       (SELECT r.reading::text FROM meter_readings r WHERE r.subscription_id = s.id AND r.period < $2 ORDER BY r.period DESC LIMIT 1)
		FROM subscriptions s
		JOIN plans p ON p.id = s.plan_id
		JOIN customers c ON c.id = s.customer_id
		WHERE `+billingWhere+`
		  AND ($7::text IS NULL OR (c.name, s.id) > ($7::text, $8::uuid))
		ORDER BY c.name, s.id LIMIT $9`, cid, p.Period, periodEnd(p.Period), p.AreaID, p.BoxID, p.StationID, afterName, afterID, limit)
	if err != nil {
		return nil, err
	}
	subs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (sub, error) {
		var x sub
		err := row.Scan(&x.ID, &x.Customer, &x.CustomerName, &x.Plan, &x.PlanName, &x.Type, &x.Currency, &x.Revenue,
			&x.UnitLabel, &x.Fee, &x.Price, &x.Initial, &x.Override, &x.TaxRateID, &x.Current, &x.Previous)
		return x, err
	})
	if err != nil {
		return nil, err
	}
	res := &billBatchResult{processed: len(subs), skips: []billSkip{}, totals: map[string]decimal.Decimal{}}
	decs := map[string]int32{}
	taxes := map[string]*taxInfo{}
	for _, x := range subs {
		res.lastName, res.lastID = x.CustomerName, x.ID
		if _, ok := decs[x.Currency]; !ok {
			if decs[x.Currency], err = currencyDecimals(ctx, tx, x.Currency); err != nil {
				return nil, err
			}
		}
		dec := decs[x.Currency]
		fee, _ := decimal.NewFromString(x.Fee)
		if x.Override != nil {
			fee, _ = decimal.NewFromString(*x.Override)
		}
		lines := []invLine{}
		if fee.IsPositive() {
			lines = append(lines, invLine{Kind: "fee", Description: x.PlanName + " · " + p.Period,
				Quantity: decimal.NewFromInt(1), UnitPrice: fee, Amount: fee.Round(dec),
				Meta: map[string]any{"plan": x.PlanName, "period": p.Period}})
		}
		if x.Type == "metered" {
			if x.Current == nil {
				res.skips = append(res.skips, billSkip{x.CustomerName, "no_reading"})
				continue
			}
			prevS := x.Initial
			if x.Previous != nil {
				prevS = *x.Previous
			}
			prev, _ := decimal.NewFromString(prevS)
			cur, _ := decimal.NewFromString(*x.Current)
			used := cur.Sub(prev)
			if used.IsNegative() {
				res.skips = append(res.skips, billSkip{x.CustomerName, "reading_below_previous"})
				continue
			}
			price, _ := decimal.NewFromString(x.Price)
			if used.IsPositive() {
				lines = append(lines, invLine{Kind: "usage",
					Description: fmt.Sprintf("%s → %s = %s %s × %s", prev.String(), cur.String(), used.String(), x.UnitLabel, price.String()),
					Quantity:    used, UnitPrice: price, Amount: used.Mul(price).Round(dec),
					Meta: map[string]any{"previous": prev.String(), "current": cur.String(), "used": used.String(), "unit": x.UnitLabel}})
			}
		}
		if x.TaxRateID != nil {
			t, ok := taxes[*x.TaxRateID]
			if !ok {
				if t, err = loadTaxRate(ctx, tx, cid, *x.TaxRateID, "sales"); err != nil {
					return nil, err
				}
				taxes[*x.TaxRateID] = t
			}
			for i := range lines {
				lines[i].Tax = t
				lines[i].TaxAmount = taxOn(lines[i].Amount, t, dec)
			}
		}
		sum := decimal.Zero
		for _, l := range lines {
			sum = sum.Add(l.Amount).Add(l.TaxAmount)
		}
		if !sum.IsPositive() {
			res.skips = append(res.skips, billSkip{x.CustomerName, "zero_invoice"})
			continue
		}
		subID, period := x.ID, p.Period
		if _, _, err := issueInvoice(ctx, tx, cid, set, x.Customer, x.CustomerName, &subID, &period,
			p.IssueDate, p.DueDate, x.Currency, x.Revenue, "", lines); err != nil {
			return nil, err
		}
		res.created++
		res.totals[x.Currency] = res.totals[x.Currency].Add(sum)
	}
	return res, nil
}

func totalsJSON(t map[string]decimal.Decimal) map[string]string {
	out := map[string]string{}
	for k, v := range t {
		out[k] = v.String()
	}
	return out
}

// billingLock serialises billing runs of one company and period (two clicks, two browsers).
func billingLock(ctx context.Context, tx pgx.Tx, cid, period string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('billing:' || $1 || ':' || $2))`, cid, period)
	return err
}

// runBilling creates this period's invoice for every active subscription that has none yet.
func (s *Server) runBilling(w http.ResponseWriter, r *http.Request) {
	var in struct {
		billingParams
		Async bool `json:"async"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	p := in.billingParams
	if p.IssueDate == "" {
		p.IssueDate = today()
	}
	if p.DueDate == "" {
		t, _ := time.Parse(time.DateOnly, p.IssueDate)
		p.DueDate = t.AddDate(0, 0, 10).Format(time.DateOnly)
	}
	if !validPeriod(p.Period) || !validDate(p.IssueDate) || !validDate(p.DueDate) || p.DueDate < p.IssueDate ||
		(p.AreaID != "" && !isUUID(p.AreaID)) || (p.StationID != "" && !isUUID(p.StationID)) || (p.BoxID != "" && !isUUID(p.BoxID)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	var jobID string
	status := http.StatusOK
	var out any
	err := db.InTenant(r.Context(), s.Pool, scope(r), func(tx pgx.Tx) error {
		ctx := r.Context()
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return err
		}
		if err := set.requireAR(); err != nil {
			return err
		}
		var running bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM billing_jobs WHERE company_id = $1 AND period = $2 AND status = 'running')`,
			cid, p.Period).Scan(&running); err != nil {
			return err
		}
		if running {
			return &apiError{status: http.StatusConflict, code: "billing_running"}
		}
		n, err := countBillable(ctx, tx, cid, p)
		if err != nil {
			return err
		}
		if n > billingSyncLimit || in.Async {
			params, _ := json.Marshal(p)
			if err := tx.QueryRow(ctx, `INSERT INTO billing_jobs (company_id, period, params, total) VALUES ($1, $2, $3, $4) RETURNING id::text`,
				cid, p.Period, params, n).Scan(&jobID); err != nil {
				return err
			}
			status = http.StatusAccepted
			return nil
		}
		if err := billingLock(ctx, tx, cid, p.Period); err != nil {
			return err
		}
		res, err := billBatch(ctx, tx, cid, set, p, nil, nil, billingSyncLimit*2)
		if err != nil {
			return err
		}
		out = map[string]any{"period": p.Period, "created": res.created, "skipped": res.skips, "totals": totalsJSON(res.totals)}
		return nil
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	if jobID != "" {
		sc := scope(r)
		go s.processBillingJob(context.WithoutCancel(r.Context()), db.Scope{TenantID: sc.TenantID, UserID: sc.UserID, IP: sc.IP}, cid, jobID)
		s.run(w, r, status, func(ctx context.Context, tx pgx.Tx) (any, error) {
			return loadBillingJob(ctx, tx, cid, jobID)
		})
		return
	}
	writeJSON(w, status, out)
}

const billingJobCols = `id, period, params, status, total, processed, created, skipped_count, skipped, totals, error,
	created_at, updated_at, finished_at, (SELECT u.full_name FROM users u WHERE u.id = billing_jobs.created_by) AS created_by`

func loadBillingJob(ctx context.Context, tx pgx.Tx, cid, id string) (json.RawMessage, error) {
	if !isUUID(id) {
		return nil, notFound()
	}
	return queryJSONOne(ctx, tx, `SELECT `+billingJobCols+` FROM billing_jobs WHERE id = $1 AND company_id = $2`, id, cid)
}

func (s *Server) getBillingJob(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return loadBillingJob(ctx, tx, r.PathValue("cid"), r.PathValue("id"))
	})
}

func (s *Server) listBillingJobs(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+billingJobCols+` FROM billing_jobs WHERE company_id = $1 ORDER BY created_at DESC LIMIT 50`,
			r.PathValue("cid"))
	})
}

var billingJobsActive sync.Map // job id → struct{}: one goroutine per job

// processBillingJob bills the job's subscriptions chunk by chunk until none are left.
func (s *Server) processBillingJob(ctx context.Context, sc db.Scope, cid, jobID string) {
	if _, dup := billingJobsActive.LoadOrStore(jobID, struct{}{}); dup {
		return
	}
	defer billingJobsActive.Delete(jobID)
	for {
		done := false
		err := db.InTenant(ctx, s.Pool, sc, func(tx pgx.Tx) error {
			var status string
			var paramsRaw, totalsRaw, skippedRaw []byte
			var curName, curID *string
			if err := tx.QueryRow(ctx, `SELECT status, params, totals, skipped, cursor_name, cursor_id::text FROM billing_jobs
				WHERE id = $1 AND company_id = $2 FOR UPDATE`, jobID, cid).Scan(&status, &paramsRaw, &totalsRaw, &skippedRaw, &curName, &curID); err != nil {
				return err
			}
			if status != "running" {
				done = true
				return nil
			}
			var p billingParams
			_ = json.Unmarshal(paramsRaw, &p)
			if err := billingLock(ctx, tx, cid, p.Period); err != nil {
				return err
			}
			set, err := loadBillingSettings(ctx, tx, cid)
			if err != nil {
				return err
			}
			if err := set.requireAR(); err != nil {
				return err
			}
			res, err := billBatch(ctx, tx, cid, set, p, curName, curID, billingChunk)
			if err != nil {
				return err
			}
			totals := map[string]string{}
			_ = json.Unmarshal(totalsRaw, &totals)
			for k, v := range res.totals {
				old, _ := decimal.NewFromString(totals[k])
				totals[k] = old.Add(v).String()
			}
			skipped := []billSkip{}
			_ = json.Unmarshal(skippedRaw, &skipped)
			for _, sk := range res.skips {
				if len(skipped) < 500 {
					skipped = append(skipped, sk)
				}
			}
			tj, _ := json.Marshal(totals)
			sj, _ := json.Marshal(skipped)
			done = res.processed < billingChunk
			var name, id *string
			if res.processed > 0 {
				name, id = &res.lastName, &res.lastID
			} else {
				name, id = curName, curID
			}
			_, err = tx.Exec(ctx, `UPDATE billing_jobs SET processed = processed + $2, created = created + $3,
				skipped_count = skipped_count + $4, skipped = $5, totals = $6, cursor_name = $7, cursor_id = $8::uuid,
				status = CASE WHEN $9 THEN 'done' ELSE status END, finished_at = CASE WHEN $9 THEN now() END
				WHERE id = $1`, jobID, res.processed, res.created, len(res.skips), sj, tj, name, id, done)
			return err
		})
		if err != nil {
			code := err.Error()
			var pg *pgconn.PgError
			var ae *apiError
			if errors.As(err, &ae) {
				code = ae.code
				if ae.detail != "" {
					code += ": " + ae.detail
				}
			} else if errors.As(err, &pg) {
				code = pg.Code + " " + pg.Message
			}
			if len(code) > 300 {
				code = code[:300]
			}
			slog.Error("billing job", "job", jobID, "err", err)
			_ = db.InTenant(context.WithoutCancel(ctx), s.Pool, sc, func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `UPDATE billing_jobs SET status = 'failed', error = $2, finished_at = now() WHERE id = $1 AND status = 'running'`, jobID, code)
				return err
			})
			return
		}
		if done {
			return
		}
	}
}

// ResumeBillingJobs restarts billing jobs that were running when the server stopped.
func (s *Server) ResumeBillingJobs(ctx context.Context) {
	if s.OwnerPool == nil {
		return
	}
	rows, err := s.OwnerPool.Query(ctx, `SELECT id::text, tenant_id::text, company_id::text, coalesce(created_by::text, '')
		FROM billing_jobs WHERE status = 'running'`)
	if err != nil {
		slog.Error("resume billing jobs", "err", err)
		return
	}
	type job struct{ id, tenant, company, user string }
	jobs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (job, error) {
		var j job
		err := row.Scan(&j.id, &j.tenant, &j.company, &j.user)
		return j, err
	})
	if err != nil {
		slog.Error("resume billing jobs", "err", err)
		return
	}
	for _, j := range jobs {
		go s.processBillingJob(ctx, db.Scope{TenantID: j.tenant, UserID: j.user}, j.company, j.id)
	}
}
