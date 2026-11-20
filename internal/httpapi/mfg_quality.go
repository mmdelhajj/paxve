package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Quality (manufacturing pro): control points say what to check (pass / fail or a measure within tolerances) on
// an operation (per work center), on the finished product, or on goods received from a supplier. Checks are created
// when an order is confirmed / goods arrive; a work order cannot be finished, nor an order produced, while one of
// its checks is still to do. A failed check raises a quality alert, followed until it is closed.

const qpCols = `p.id, p.name, p.trigger_on, p.item_id, (SELECT i.code || ' · ' || i.name FROM items i WHERE i.id = p.item_id) AS item,
	p.workcenter_id, (SELECT c.name FROM mfg_workcenters c WHERE c.id = p.workcenter_id) AS workcenter, p.test_type,
	trim_scale(p.norm)::text AS norm, trim_scale(p.tol_min)::text AS tol_min, trim_scale(p.tol_max)::text AS tol_max, p.unit, p.instructions, p.is_active`

func (s *Server) mfgQualityPoints(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+qpCols+` FROM mfg_quality_points p WHERE p.company_id = $1 ORDER BY p.trigger_on, p.name`, r.PathValue("cid"))
	})
}

// POST /mfg/quality-points · PATCH /mfg/quality-points/{id}
func (s *Server) mfgSaveQualityPoint(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name         *string `json:"name"`
		Trigger      *string `json:"trigger_on"`
		ItemID       *string `json:"item_id"`
		WorkcenterID *string `json:"workcenter_id"`
		TestType     *string `json:"test_type"`
		Norm         *string `json:"norm"`
		TolMin       *string `json:"tol_min"`
		TolMax       *string `json:"tol_max"`
		Unit         *string `json:"unit"`
		Instructions *string `json:"instructions"`
		IsActive     *bool   `json:"is_active"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	bad := (id != "" && !isUUID(id)) || (id == "" && (in.Name == nil || strings.TrimSpace(*in.Name) == "" || in.Trigger == nil))
	if in.Trigger != nil && *in.Trigger != "operation" && *in.Trigger != "production" && *in.Trigger != "receipt" {
		bad = true
	}
	if in.TestType != nil && *in.TestType != "passfail" && *in.TestType != "measure" {
		bad = true
	}
	for _, u := range []*string{in.ItemID, in.WorkcenterID} {
		if u != nil && *u != "" && !isUUID(*u) {
			bad = true
		}
	}
	for _, n := range []*string{in.Norm, in.TolMin, in.TolMax} {
		if n != nil && *n != "" {
			if _, ok := money(*n); !ok {
				bad = true
			}
		}
	}
	if bad {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[id == ""], func(ctx context.Context, tx pgx.Tx) (any, error) {
		if in.ItemID != nil && *in.ItemID != "" {
			if _, err := loadItem(ctx, tx, cid, *in.ItemID); err != nil {
				return nil, err
			}
		}
		if in.WorkcenterID != nil && *in.WorkcenterID != "" {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mfg_workcenters WHERE id = $1 AND company_id = $2)`, *in.WorkcenterID, cid).Scan(&ok); err != nil {
				return nil, err
			}
			if !ok {
				return nil, badRequest("invalid_workcenter")
			}
		}
		// "" clears a reference / number; nil keeps it
		ref := func(p *string) (set bool, v *string) {
			if p == nil {
				return false, nil
			}
			if *p == "" {
				return true, nil
			}
			return true, p
		}
		si, vi := ref(in.ItemID)
		sw, vw := ref(in.WorkcenterID)
		sn, vn := ref(in.Norm)
		smin, vmin := ref(in.TolMin)
		smax, vmax := ref(in.TolMax)
		trim := func(p *string) *string {
			if p == nil {
				return nil
			}
			t := strings.TrimSpace(*p)
			return &t
		}
		var err error
		if id == "" {
			err = tx.QueryRow(ctx, `INSERT INTO mfg_quality_points (company_id, name, trigger_on, item_id, workcenter_id, test_type, norm, tol_min, tol_max, unit, instructions, is_active)
				VALUES ($1, $2, $3, $4, $5, coalesce($6, 'passfail'), $7::numeric, $8::numeric, $9::numeric, coalesce($10, ''), coalesce($11, ''), coalesce($12, true))
				RETURNING id::text`, cid, trim(in.Name), in.Trigger, vi, vw, in.TestType, vn, vmin, vmax, trim(in.Unit), trim(in.Instructions), in.IsActive).Scan(&id)
		} else {
			var tag interface{ RowsAffected() int64 }
			tag, err = tx.Exec(ctx, `UPDATE mfg_quality_points SET name = coalesce($3, name), trigger_on = coalesce($4, trigger_on),
				item_id = CASE WHEN $5 THEN $6::uuid ELSE item_id END, workcenter_id = CASE WHEN $7 THEN $8::uuid ELSE workcenter_id END,
				test_type = coalesce($9, test_type), norm = CASE WHEN $10 THEN $11::numeric ELSE norm END,
				tol_min = CASE WHEN $12 THEN $13::numeric ELSE tol_min END, tol_max = CASE WHEN $14 THEN $15::numeric ELSE tol_max END,
				unit = coalesce($16, unit), instructions = coalesce($17, instructions), is_active = coalesce($18, is_active)
				WHERE id = $1 AND company_id = $2`, id, cid, trim(in.Name), in.Trigger, si, vi, sw, vw, in.TestType, sn, vn, smin, vmin, smax, vmax,
				trim(in.Unit), trim(in.Instructions), in.IsActive)
			if err == nil && tag.RowsAffected() == 0 {
				return nil, notFound()
			}
		}
		if err != nil {
			if strings.Contains(err.Error(), "check constraint") {
				return nil, badRequest("invalid_tolerance")
			}
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+qpCols+` FROM mfg_quality_points p WHERE p.id = $1`, id)
	})
}

const qcCols = `q.id, q.point_id, p.name AS point_name, p.trigger_on, p.test_type, trim_scale(p.norm)::text AS norm, trim_scale(p.tol_min)::text AS tol_min,
	trim_scale(p.tol_max)::text AS tol_max, p.unit, p.instructions, q.order_id, (SELECT o.number FROM mfg_orders o WHERE o.id = q.order_id) AS order_number,
	q.workorder_id, (SELECT x.name FROM mfg_workorders x WHERE x.id = q.workorder_id) AS workorder, q.receipt_id,
	(SELECT g.number FROM goods_receipts g WHERE g.id = q.receipt_id) AS receipt_number, q.item_id, it.code AS item_code, it.name AS item_name,
	q.status, trim_scale(q.measured)::text AS measured, q.note, q.checked_by, (SELECT u.full_name FROM users u WHERE u.id = q.checked_by) AS checked_by_name,
	q.checked_at, q.created_at, (SELECT a.id FROM mfg_quality_alerts a WHERE a.check_id = q.id LIMIT 1) AS alert_id`

const qcFrom = ` FROM mfg_quality_checks q JOIN mfg_quality_points p ON p.id = q.point_id LEFT JOIN items it ON it.id = q.item_id`

// GET /mfg/quality-checks?status=todo|pass|fail|all&order_id=&workorder_id=&receipt_id=
func (s *Server) mfgQualityChecks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	st := defaultStr(q.Get("status"), "all")
	ids := []string{q.Get("order_id"), q.Get("workorder_id"), q.Get("receipt_id")}
	for _, x := range ids {
		if x != "" && !isUUID(x) {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+qcCols+qcFrom+` WHERE q.company_id = $1 AND ($2 = 'all' OR q.status = $2)
			AND ($3 = '' OR q.order_id = nullif($3, '')::uuid) AND ($4 = '' OR q.workorder_id = nullif($4, '')::uuid) AND ($5 = '' OR q.receipt_id = nullif($5, '')::uuid)
			ORDER BY q.status = 'todo' DESC, q.created_at DESC LIMIT 1000`, r.PathValue("cid"), st, ids[0], ids[1], ids[2])
	})
}

// POST /mfg/quality-checks/{id} {result: pass|fail, measured, note}: a measure is judged against the tolerances.
// A failed check raises a quality alert.
func (s *Server) mfgRecordCheck(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Result   string `json:"result"`
		Measured string `json:"measured"`
		Note     string `json:"note"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if !isUUID(id) {
			return nil, notFound()
		}
		var st, typ, pname string
		var tmin, tmax, item, order *string
		err := tx.QueryRow(ctx, `SELECT q.status, p.test_type, p.name, p.tol_min::text, p.tol_max::text, q.item_id::text, q.order_id::text
			FROM mfg_quality_checks q JOIN mfg_quality_points p ON p.id = q.point_id WHERE q.id = $1 AND q.company_id = $2 FOR UPDATE OF q`, id, cid).
			Scan(&st, &typ, &pname, &tmin, &tmax, &item, &order)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if st != "todo" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		result := in.Result
		var measured *string
		if typ == "measure" {
			m, ok := money(in.Measured)
			if !ok {
				return nil, badRequest("measure_required")
			}
			lo, _ := decimal.NewFromString(*tmin)
			hi, _ := decimal.NewFromString(*tmax)
			result = "pass"
			if m.LessThan(lo) || m.GreaterThan(hi) {
				result = "fail"
			}
			ms := m.String()
			measured = &ms
		} else if result != "pass" && result != "fail" {
			return nil, badRequest("bad_request")
		}
		if _, err := tx.Exec(ctx, `UPDATE mfg_quality_checks SET status = $2, measured = $3::numeric, note = $4, checked_by = app_user(), checked_at = now() WHERE id = $1`,
			id, result, measured, strings.TrimSpace(in.Note)); err != nil {
			return nil, err
		}
		if result == "fail" {
			title := "Failed: " + pname
			if item != nil {
				var iname string
				if err := tx.QueryRow(ctx, `SELECT name FROM items WHERE id = $1`, *item).Scan(&iname); err == nil {
					title += " · " + iname
				}
			}
			if measured != nil {
				title += fmt.Sprintf(" (%s)", *measured)
			}
			if _, err := newQualityAlert(ctx, tx, cid, title, &id, item, order, 2, strings.TrimSpace(in.Note)); err != nil {
				return nil, err
			}
		}
		return queryJSONOne(ctx, tx, `SELECT `+qcCols+qcFrom+` WHERE q.id = $1`, id)
	})
}

func newQualityAlert(ctx context.Context, tx pgx.Tx, cid, title string, check, item, order *string, priority int, cause string) (string, error) {
	var n int64
	if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'mfg_quality_alert')`, cid).Scan(&n); err != nil {
		return "", err
	}
	if len(title) > 200 {
		title = title[:200]
	}
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO mfg_quality_alerts (company_id, number, title, check_id, item_id, order_id, priority, root_cause)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id::text`, cid, n, title, check, item, order, priority, cause).Scan(&id)
	return id, err
}

const qaCols = `a.id, a.number, a.title, a.check_id, a.item_id, (SELECT i.code || ' · ' || i.name FROM items i WHERE i.id = a.item_id) AS item,
	a.order_id, (SELECT o.number FROM mfg_orders o WHERE o.id = a.order_id) AS order_number, a.priority, a.stage, a.root_cause, a.action,
	a.assigned_to, (SELECT u.full_name FROM users u WHERE u.id = a.assigned_to) AS assigned_name, a.created_at, a.closed_at`

// GET /mfg/quality-alerts?stage=open|all|<stage>
func (s *Server) mfgQualityAlerts(w http.ResponseWriter, r *http.Request) {
	st := defaultStr(r.URL.Query().Get("stage"), "all")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+qaCols+` FROM mfg_quality_alerts a WHERE a.company_id = $1
			AND ($2 = 'all' OR ($2 = 'open' AND a.stage <> 'done') OR a.stage = $2) ORDER BY a.stage = 'done', a.priority DESC, a.number DESC LIMIT 1000`,
			r.PathValue("cid"), st)
	})
}

// POST /mfg/quality-alerts {title, item_id, order_id, priority, root_cause} · PATCH /mfg/quality-alerts/{id} {stage, root_cause, action, priority, assigned_to}
func (s *Server) mfgSaveQualityAlert(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title      *string `json:"title"`
		ItemID     *string `json:"item_id"`
		OrderID    *string `json:"order_id"`
		Priority   *int    `json:"priority"`
		Stage      *string `json:"stage"`
		RootCause  *string `json:"root_cause"`
		Action     *string `json:"action"`
		AssignedTo *string `json:"assigned_to"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	bad := (id != "" && !isUUID(id)) || (id == "" && (in.Title == nil || strings.TrimSpace(*in.Title) == ""))
	if in.Priority != nil && (*in.Priority < 0 || *in.Priority > 3) {
		bad = true
	}
	if in.Stage != nil && *in.Stage != "new" && *in.Stage != "progress" && *in.Stage != "done" {
		bad = true
	}
	for _, u := range []*string{in.ItemID, in.OrderID, in.AssignedTo} {
		if u != nil && *u != "" && !isUUID(*u) {
			bad = true
		}
	}
	if bad {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[id == ""], func(ctx context.Context, tx pgx.Tx) (any, error) {
		nz := func(p *string) *string {
			if p == nil || *p == "" {
				return nil
			}
			return p
		}
		if v := nz(in.ItemID); v != nil {
			if _, err := loadItem(ctx, tx, cid, *v); err != nil {
				return nil, err
			}
		}
		if v := nz(in.OrderID); v != nil {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mfg_orders WHERE id = $1 AND company_id = $2)`, *v, cid).Scan(&ok); err != nil {
				return nil, err
			}
			if !ok {
				return nil, badRequest("invalid_order")
			}
		}
		if v := nz(in.AssignedTo); v != nil {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)`, *v).Scan(&ok); err != nil {
				return nil, err
			}
			if !ok {
				return nil, badRequest("invalid_user")
			}
		}
		if id == "" {
			p := 1
			if in.Priority != nil {
				p = *in.Priority
			}
			cause := ""
			if in.RootCause != nil {
				cause = strings.TrimSpace(*in.RootCause)
			}
			var err error
			if id, err = newQualityAlert(ctx, tx, cid, strings.TrimSpace(*in.Title), nil, nz(in.ItemID), nz(in.OrderID), p, cause); err != nil {
				return nil, err
			}
		} else {
			trim := func(p *string) *string {
				if p == nil {
					return nil
				}
				t := strings.TrimSpace(*p)
				return &t
			}
			tag, err := tx.Exec(ctx, `UPDATE mfg_quality_alerts SET title = coalesce($3, title), priority = coalesce($4, priority), stage = coalesce($5, stage),
				root_cause = coalesce($6, root_cause), action = coalesce($7, action),
				assigned_to = CASE WHEN $8::text IS NULL THEN assigned_to ELSE nullif($8, '')::uuid END,
				closed_at = CASE WHEN coalesce($5, stage) = 'done' THEN coalesce(closed_at, now()) ELSE NULL END
				WHERE id = $1 AND company_id = $2`, id, cid, trim(in.Title), in.Priority, in.Stage, trim(in.RootCause), trim(in.Action), in.AssignedTo)
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() == 0 {
				return nil, notFound()
			}
		}
		return queryJSONOne(ctx, tx, `SELECT `+qaCols+` FROM mfg_quality_alerts a WHERE a.id = $1`, id)
	})
}

// mfgOnReceipt: goods received from a supplier get the receipt quality checks of their items.
func mfgOnReceipt(ctx context.Context, tx pgx.Tx, cid, receiptID string) error {
	_, err := tx.Exec(ctx, `INSERT INTO mfg_quality_checks (company_id, point_id, receipt_id, item_id)
		SELECT DISTINCT $1::uuid, p.id, $2::uuid, pl.item_id FROM goods_receipt_lines gl JOIN purchase_order_lines pl ON pl.id = gl.po_line_id
		JOIN mfg_quality_points p ON p.company_id = $1 AND p.is_active AND p.trigger_on = 'receipt' AND (p.item_id IS NULL OR p.item_id = pl.item_id)
		WHERE gl.receipt_id = $2 AND pl.item_id IS NOT NULL`, cid, receiptID)
	return err
}
