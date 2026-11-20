package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Phase 9 — timeline (Gantt) for project tasks (with dependencies), manufacturing work orders (with work-center load)
// and field technicians' jobs, with drag to reschedule and warnings for conflicts / overload.

type ganttBar struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Row      string   `json:"row"` // lane key (assignee, work center, technician)
	RowLabel string   `json:"row_label"`
	Start    string   `json:"start"` // RFC 3339 (tasks: whole days)
	End      string   `json:"end"`
	Status   string   `json:"status"`
	Progress int      `json:"progress"`
	Parent   *string  `json:"parent,omitempty"`
	Deps     []string `json:"deps"`
	Ref      string   `json:"ref"`     // order / project / job number
	Planned  bool     `json:"planned"` // false: the dates are guessed (no start set yet)
}

type ganttWarning struct {
	Kind string   `json:"kind"` // dependency | overlap | overload | late
	IDs  []string `json:"ids"`
	Row  string   `json:"row,omitempty"`
	Day  string   `json:"day,omitempty"`
	Info string   `json:"info,omitempty"`
}

func dayStr(t time.Time) string { return t.Format(time.DateOnly) }

// overlaps: bars of the same lane that overlap in time.
func overlaps(bars []ganttBar) []ganttWarning {
	byRow := map[string][]ganttBar{}
	for _, b := range bars {
		if b.Row != "" && b.Status != "done" && b.Status != "cancelled" {
			byRow[b.Row] = append(byRow[b.Row], b)
		}
	}
	var out []ganttWarning
	for row, list := range byRow {
		sort.Slice(list, func(i, j int) bool { return list[i].Start < list[j].Start })
		for i := 0; i < len(list); i++ {
			for j := i + 1; j < len(list) && list[j].Start < list[i].End; j++ {
				out = append(out, ganttWarning{Kind: "overlap", IDs: []string{list[i].ID, list[j].ID}, Row: row})
			}
		}
	}
	return out
}

// ---------- projects ----------

// GET /gantt/projects/{id}: the project's tasks on a timeline with their dependencies.
func (s *Server) ganttProject(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var pname string
		var pnum int64
		if err := tx.QueryRow(ctx, `SELECT name, number FROM projects WHERE id = $1 AND company_id = $2`, id, cid).Scan(&pname, &pnum); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, notFound()
			}
			return nil, err
		}
		rows, err := tx.Query(ctx, `SELECT t.id::text, '#' || t.number || ' ' || t.title, coalesce(t.assignee_id::text, ''), coalesce(u.full_name, ''),
			t.start_date, t.deadline, t.created_at::date, t.planned_hours::text, t.done_at IS NOT NULL, t.parent_id::text,
			coalesce((SELECT round(100.0 * count(*) FILTER (WHERE (c->>'done')::boolean) / nullif(count(*), 0)) FROM jsonb_array_elements(t.checklist) c), 0)::int,
			coalesce((SELECT array_agg(d.depends_on::text) FROM project_task_deps d WHERE d.task_id = t.id), '{}')
			FROM project_tasks t LEFT JOIN users u ON u.id = t.assignee_id WHERE t.project_id = $1 AND t.company_id = $2 ORDER BY t.seq, t.number`, id, cid)
		if err != nil {
			return nil, err
		}
		bars := []ganttBar{}
		for rows.Next() {
			var b ganttBar
			var start, deadline *time.Time
			var created time.Time
			var hours string
			var done bool
			if err := rows.Scan(&b.ID, &b.Label, &b.Row, &b.RowLabel, &start, &deadline, &created, &hours, &done, &b.Parent, &b.Progress, &b.Deps); err != nil {
				rows.Close()
				return nil, err
			}
			h, _ := decimal.NewFromString(hours)
			days := int(h.Div(decimal.NewFromInt(8)).Ceil().IntPart())
			if days < 1 {
				days = 1
			}
			b.Planned = start != nil
			st := created
			if start != nil {
				st = *start
			} else if deadline != nil {
				st = deadline.AddDate(0, 0, -(days - 1))
			}
			en := st.AddDate(0, 0, days-1)
			if deadline != nil && !deadline.Before(st) {
				en = *deadline
			}
			b.Start, b.End = dayStr(st), dayStr(en.AddDate(0, 0, 1)) // end exclusive
			b.Status = map[bool]string{true: "done", false: "open"}[done]
			if done {
				b.Progress = 100
			}
			b.Ref = "#" + itoa64(pnum)
			bars = append(bars, b)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		byID := map[string]ganttBar{}
		for _, b := range bars {
			byID[b.ID] = b
		}
		warn := []ganttWarning{}
		for _, b := range bars {
			for _, d := range b.Deps {
				if p, ok := byID[d]; ok && b.Start < p.End && b.Status != "done" {
					warn = append(warn, ganttWarning{Kind: "dependency", IDs: []string{d, b.ID}})
				}
			}
			if b.Status != "done" && b.End <= today() {
				warn = append(warn, ganttWarning{Kind: "late", IDs: []string{b.ID}})
			}
		}
		warn = append(warn, overlaps(bars)...)
		return map[string]any{"title": pname, "bars": bars, "warnings": warn, "unit": "day"}, nil
	})
}

// PUT /gantt/tasks/{id} {start, end, shift_dependents}: new dates of a task (end inclusive); dependent tasks that
// would start too early move after it (keeping their length).
func (s *Server) ganttMoveTask(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Start           string `json:"start"`
		End             string `json:"end"`
		ShiftDependents bool   `json:"shift_dependents"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) || !validDate(in.Start) || !validDate(in.End) || in.End < in.Start {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var project string
		if err := tx.QueryRow(ctx, `SELECT project_id::text FROM project_tasks WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&project); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, notFound()
			}
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE project_tasks SET start_date = $2::date, deadline = $3::date, updated_at = now() WHERE id = $1`, id, in.Start, in.End); err != nil {
			return nil, err
		}
		moved := []string{id}
		if in.ShiftDependents {
			queue := []string{id}
			seen := map[string]bool{id: true}
			for len(queue) > 0 && len(moved) < 500 {
				cur := queue[0]
				queue = queue[1:]
				var curEnd time.Time
				if err := tx.QueryRow(ctx, `SELECT coalesce(deadline, start_date, created_at::date) FROM project_tasks WHERE id = $1`, cur).Scan(&curEnd); err != nil {
					return nil, err
				}
				rows, err := tx.Query(ctx, `SELECT t.id::text, coalesce(t.start_date, t.created_at::date), coalesce(t.deadline, t.start_date, t.created_at::date)
					FROM project_task_deps d JOIN project_tasks t ON t.id = d.task_id WHERE d.depends_on = $1 AND t.done_at IS NULL`, cur)
				if err != nil {
					return nil, err
				}
				type dep struct {
					id         string
					start, end time.Time
				}
				deps, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (dep, error) {
					var d dep
					err := row.Scan(&d.id, &d.start, &d.end)
					return d, err
				})
				if err != nil {
					return nil, err
				}
				for _, d := range deps {
					if !d.start.After(curEnd) {
						length := d.end.Sub(d.start)
						if length < 0 {
							length = 0
						}
						ns := curEnd.AddDate(0, 0, 1)
						if _, err := tx.Exec(ctx, `UPDATE project_tasks SET start_date = $2::date, deadline = $3::date, updated_at = now() WHERE id = $1`,
							d.id, dayStr(ns), dayStr(ns.Add(length))); err != nil {
							return nil, err
						}
						moved = append(moved, d.id)
					}
					if !seen[d.id] {
						seen[d.id] = true
						queue = append(queue, d.id)
					}
				}
			}
		}
		return map[string]any{"moved": moved, "project_id": project}, nil
	})
}

// POST /gantt/tasks/{id}/deps {depends_on}: the task starts after another one of the same project (no loops).
func (s *Server) ganttAddDep(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DependsOn string `json:"depends_on"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) || !isUUID(in.DependsOn) || id == in.DependsOn {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var p1, p2 string
		if err := tx.QueryRow(ctx, `SELECT project_id::text FROM project_tasks WHERE id = $1 AND company_id = $2`, id, cid).Scan(&p1); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, notFound()
			}
			return nil, err
		}
		if err := tx.QueryRow(ctx, `SELECT project_id::text FROM project_tasks WHERE id = $1 AND company_id = $2`, in.DependsOn, cid).Scan(&p2); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, badRequest("invalid_task")
			}
			return nil, err
		}
		if p1 != p2 {
			return nil, badRequest("other_project")
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('taskdeps:' || $1))`, p1); err != nil {
			return nil, err
		}
		// a loop: does depends_on already (transitively) depend on id?
		var loop bool
		if err := tx.QueryRow(ctx, `WITH RECURSIVE up AS (SELECT depends_on FROM project_task_deps WHERE task_id = $1
			UNION SELECT d.depends_on FROM project_task_deps d JOIN up ON d.task_id = up.depends_on) SELECT EXISTS (SELECT 1 FROM up WHERE depends_on = $2)`,
			in.DependsOn, id).Scan(&loop); err != nil {
			return nil, err
		}
		if loop {
			return nil, badRequest("dependency_loop")
		}
		var did string
		err := tx.QueryRow(ctx, `INSERT INTO project_task_deps (company_id, task_id, depends_on) VALUES ($1, $2, $3) RETURNING id::text`, cid, id, in.DependsOn).Scan(&did)
		if isUniqueViolation(err) {
			return nil, &apiError{status: http.StatusConflict, code: "already_linked"}
		}
		if err != nil {
			return nil, err
		}
		return map[string]string{"id": did, "task_id": id, "depends_on": in.DependsOn}, nil
	})
}

// DELETE /gantt/deps/{id}
func (s *Server) ganttDeleteDep(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		tag, err := tx.Exec(ctx, `DELETE FROM project_task_deps WHERE id = $1 AND company_id = $2`, id, cid)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			// the link may be given as task + dependency ids
			return nil, notFound()
		}
		return map[string]string{"deleted": id}, nil
	})
}

// GET /gantt/projects/{id}/deps: the dependency links (with their ids, to remove one).
func (s *Server) ganttDeps(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM projects WHERE id = $1 AND company_id = $2)`, id, cid).Scan(&ok); err != nil {
			return nil, err
		}
		if !ok {
			return nil, notFound()
		}
		return queryJSON(ctx, tx, `SELECT d.id, d.task_id, t.number AS task_number, d.depends_on, p.number AS depends_on_number FROM project_task_deps d
			JOIN project_tasks t ON t.id = d.task_id JOIN project_tasks p ON p.id = d.depends_on WHERE t.project_id = $1 AND d.company_id = $2
			ORDER BY t.number, p.number`, id, cid)
	})
}

// ---------- manufacturing ----------

func ganttRange(r *http.Request, back, ahead int) (time.Time, time.Time, bool) {
	q := r.URL.Query()
	from, to := q.Get("from"), q.Get("to")
	now := time.Now().Truncate(24 * time.Hour)
	f, t := now.AddDate(0, 0, -back), now.AddDate(0, 0, ahead)
	if from != "" {
		v, err := time.Parse(time.DateOnly, from)
		if err != nil {
			return f, t, false
		}
		f = v
	}
	if to != "" {
		v, err := time.Parse(time.DateOnly, to)
		if err != nil {
			return f, t, false
		}
		t = v.AddDate(0, 0, 1)
	}
	return f, t, !t.Before(f) && t.Sub(f) <= 370*24*time.Hour
}

// GET /gantt/mfg?from=&to=: work orders per work center with each center's daily load against its capacity.
func (s *Server) ganttMfg(w http.ResponseWriter, r *http.Request) {
	from, to, ok := ganttRange(r, 7, 30)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		rows, err := tx.Query(ctx, `SELECT wo.id::text, 'MO #' || o.number || ' · ' || wo.name, wo.workcenter_id::text, wc.name, wo.status,
			coalesce(wo.planned_start, wo.started_at, o.planned_start, (coalesce(o.deadline, o.order_date) - 1)::timestamptz + interval '8 hours'),
			wo.planned_start IS NOT NULL, wo.planned_min::text, wo.actual_min::text, wo.finished_at, o.number,
			(SELECT p.id::text FROM mfg_workorders p WHERE p.order_id = wo.order_id AND p.seq < wo.seq ORDER BY p.seq DESC LIMIT 1)
			FROM mfg_workorders wo JOIN mfg_orders o ON o.id = wo.order_id JOIN mfg_workcenters wc ON wc.id = wo.workcenter_id
			WHERE wo.company_id = $1 AND o.status IN ('confirmed', 'progress', 'done') AND wo.status <> 'cancelled'
			  AND coalesce(wo.planned_start, wo.started_at, o.planned_start, o.order_date::timestamptz) < $3
			  AND coalesce(wo.finished_at, wo.planned_start, wo.started_at, o.planned_start, o.order_date::timestamptz) + interval '1 day' >= $2
			ORDER BY wc.name, 6 LIMIT 2000`, cid, from, to)
		if err != nil {
			return nil, err
		}
		bars := []ganttBar{}
		load := map[string]map[string]float64{} // work center → day → minutes
		for rows.Next() {
			var b ganttBar
			var st time.Time
			var planned bool
			var pm, am string
			var fin *time.Time
			var num int64
			var prev *string
			if err := rows.Scan(&b.ID, &b.Label, &b.Row, &b.RowLabel, &b.Status, &st, &planned, &pm, &am, &fin, &num, &prev); err != nil {
				rows.Close()
				return nil, err
			}
			p, _ := decimal.NewFromString(pm)
			mins := p.InexactFloat64()
			if mins < 15 {
				mins = 15
			}
			en := st.Add(time.Duration(mins) * time.Minute)
			if b.Status == "done" && fin != nil && fin.After(st) {
				en = *fin
			}
			b.Start, b.End, b.Planned, b.Ref = st.Format(time.RFC3339), en.Format(time.RFC3339), planned, "MO #"+itoa64(num)
			b.Deps = []string{}
			if prev != nil {
				b.Deps = []string{*prev}
			}
			if b.Status == "done" {
				b.Progress = 100
			}
			bars = append(bars, b)
			if b.Status != "done" {
				if load[b.Row] == nil {
					load[b.Row] = map[string]float64{}
				}
				load[b.Row][dayStr(st)] += p.InexactFloat64()
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		capm := map[string]float64{}
		names := map[string]string{}
		crows, err := tx.Query(ctx, `SELECT id::text, name, (hours_per_day * 60 * capacity * efficiency / 100)::float8 FROM mfg_workcenters WHERE company_id = $1 AND is_active`, cid)
		if err != nil {
			return nil, err
		}
		centers := []map[string]any{}
		for crows.Next() {
			var id, name string
			var c float64
			if err := crows.Scan(&id, &name, &c); err != nil {
				crows.Close()
				return nil, err
			}
			capm[id], names[id] = c, name
			centers = append(centers, map[string]any{"id": id, "name": name, "minutes_per_day": c})
		}
		crows.Close()
		warn := []ganttWarning{}
		loads := []map[string]any{}
		for wc, days := range load {
			for d, m := range days {
				pct := 0.0
				if capm[wc] > 0 {
					pct = m * 100 / capm[wc]
				}
				loads = append(loads, map[string]any{"workcenter_id": wc, "day": d, "minutes": m, "capacity": capm[wc], "pct": int(pct + 0.5)})
				if pct > 100 {
					warn = append(warn, ganttWarning{Kind: "overload", Row: wc, Day: d, Info: names[wc]})
				}
			}
		}
		sort.Slice(loads, func(i, j int) bool { return loads[i]["day"].(string) < loads[j]["day"].(string) })
		byID := map[string]ganttBar{}
		for _, b := range bars {
			byID[b.ID] = b
		}
		for _, b := range bars {
			for _, d := range b.Deps {
				if p, ok := byID[d]; ok && b.Status != "done" && b.Start < p.End {
					warn = append(warn, ganttWarning{Kind: "dependency", IDs: []string{d, b.ID}})
				}
			}
		}
		return map[string]any{"bars": bars, "centers": centers, "load": loads, "warnings": warn, "unit": "hour",
			"from": dayStr(from), "to": dayStr(to.AddDate(0, 0, -1))}, nil
	})
}

// PUT /gantt/workorders/{id} {start, workcenter_id}: plans a work order (not started or paused ones only).
func (s *Server) ganttMoveWorkorder(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Start        string `json:"start"`
		WorkcenterID string `json:"workcenter_id"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	st, err := time.Parse(time.RFC3339, in.Start)
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if err != nil || !isUUID(id) || (in.WorkcenterID != "" && !isUUID(in.WorkcenterID)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM mfg_workorders WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, notFound()
			}
			return nil, err
		}
		if status == "done" || status == "cancelled" || status == "progress" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if in.WorkcenterID != "" {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mfg_workcenters WHERE id = $1 AND company_id = $2 AND is_active)`, in.WorkcenterID, cid).Scan(&ok); err != nil {
				return nil, err
			}
			if !ok {
				return nil, badRequest("invalid_workcenter")
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE mfg_workorders SET planned_start = $2, workcenter_id = coalesce(nullif($3, '')::uuid, workcenter_id) WHERE id = $1`,
			id, st, in.WorkcenterID); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT id, planned_start, workcenter_id, status FROM mfg_workorders WHERE id = $1`, id)
	})
}

// POST /gantt/mfg/plan {order_id, start}: plans the order's open work orders one after the other from start, each in
// its work center's working hours (8:00 + hours per day), skipping time the center is already planned.
func (s *Server) ganttPlanOrder(w http.ResponseWriter, r *http.Request) {
	var in struct {
		OrderID string `json:"order_id"`
		Start   string `json:"start"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	st, err := time.Parse(time.RFC3339, in.Start)
	if err != nil || !isUUID(in.OrderID) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM mfg_orders WHERE id = $1 AND company_id = $2 FOR UPDATE`, in.OrderID, cid).Scan(&status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, notFound()
			}
			return nil, err
		}
		if status != "confirmed" && status != "progress" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		rows, err := tx.Query(ctx, `SELECT wo.id::text, wo.workcenter_id::text, wo.planned_min::float8, wc.hours_per_day::float8, wc.days_per_week
			FROM mfg_workorders wo JOIN mfg_workcenters wc ON wc.id = wo.workcenter_id
			WHERE wo.order_id = $1 AND wo.status IN ('pending', 'ready', 'paused') ORDER BY wo.seq`, in.OrderID)
		if err != nil {
			return nil, err
		}
		type wo struct {
			id, wc    string
			mins, hpd float64
			days      int
		}
		list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (wo, error) {
			var x wo
			err := row.Scan(&x.id, &x.wc, &x.mins, &x.hpd, &x.days)
			return x, err
		})
		if err != nil {
			return nil, err
		}
		cursor := st
		planned := []map[string]string{}
		for _, x := range list {
			// busy until: the end of the center's last planned open work order of other orders
			var busy *time.Time
			if err := tx.QueryRow(ctx, `SELECT max(planned_start + make_interval(mins => greatest(planned_min, 15)::int)) FROM mfg_workorders
				WHERE workcenter_id = $1 AND order_id <> $2 AND status IN ('pending', 'ready', 'paused', 'progress') AND planned_start IS NOT NULL`, x.wc, in.OrderID).Scan(&busy); err != nil {
				return nil, err
			}
			start := cursor
			if busy != nil && busy.After(start) {
				start = *busy
			}
			start = workTime(start, x.hpd, x.days)
			if _, err := tx.Exec(ctx, `UPDATE mfg_workorders SET planned_start = $2 WHERE id = $1`, x.id, start); err != nil {
				return nil, err
			}
			planned = append(planned, map[string]string{"id": x.id, "start": start.Format(time.RFC3339)})
			cursor = addWorkMinutes(start, x.mins, x.hpd, x.days)
		}
		if _, err := tx.Exec(ctx, `UPDATE mfg_orders SET planned_start = $2 WHERE id = $1`, in.OrderID, st); err != nil {
			return nil, err
		}
		return map[string]any{"planned": planned, "end": cursor.Format(time.RFC3339)}, nil
	})
}

// workTime moves t into the working hours of a center (from 08:00 for hpd hours, days_per_week days from Monday).
func workTime(t time.Time, hpd float64, days int) time.Time {
	for i := 0; i < 30; i++ {
		day := time.Date(t.Year(), t.Month(), t.Day(), 8, 0, 0, 0, t.Location())
		end := day.Add(time.Duration(hpd * float64(time.Hour)))
		wd := (int(t.Weekday()) + 6) % 7 // Monday = 0
		if wd < days {
			if t.Before(day) {
				return day
			}
			if t.Before(end) {
				return t
			}
		}
		t = day.AddDate(0, 0, 1)
	}
	return t
}

func addWorkMinutes(t time.Time, mins, hpd float64, days int) time.Time {
	left := time.Duration(mins * float64(time.Minute))
	for i := 0; i < 400 && left > 0; i++ {
		t = workTime(t, hpd, days)
		end := time.Date(t.Year(), t.Month(), t.Day(), 8, 0, 0, 0, t.Location()).Add(time.Duration(hpd * float64(time.Hour)))
		avail := end.Sub(t)
		if left <= avail {
			return t.Add(left)
		}
		left -= avail
		t = end
	}
	return t
}

// ---------- field service ----------

// GET /gantt/field?from=&to=: technicians' jobs with double bookings.
func (s *Server) ganttField(w http.ResponseWriter, r *http.Request) {
	from, to, ok := ganttRange(r, 1, 14)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		rows, err := tx.Query(ctx, `SELECT j.id::text, 'Job #' || j.number || ' · ' || j.title || ' · ' || c.name, coalesce(j.technician_id::text, ''),
			coalesce(u.full_name, ''), j.status, j.scheduled_start, j.duration_min, j.number
			FROM service_jobs j JOIN customers c ON c.id = j.customer_id LEFT JOIN users u ON u.id = j.technician_id
			WHERE j.company_id = $1 AND j.scheduled_start IS NOT NULL AND j.scheduled_start < $3
			  AND j.scheduled_start + make_interval(mins => j.duration_min) >= $2 AND j.status <> 'cancelled'
			ORDER BY u.full_name NULLS LAST, j.scheduled_start LIMIT 2000`, cid, from, to)
		if err != nil {
			return nil, err
		}
		bars := []ganttBar{}
		for rows.Next() {
			var b ganttBar
			var st time.Time
			var dur int
			var num int64
			if err := rows.Scan(&b.ID, &b.Label, &b.Row, &b.RowLabel, &b.Status, &st, &dur, &num); err != nil {
				rows.Close()
				return nil, err
			}
			b.Start, b.End, b.Planned, b.Ref, b.Deps = st.Format(time.RFC3339), st.Add(time.Duration(dur)*time.Minute).Format(time.RFC3339), true, "Job #"+itoa64(num), []string{}
			if b.Status == "done" {
				b.Progress = 100
			}
			bars = append(bars, b)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		techs, err := queryJSON(ctx, tx, `SELECT id, full_name FROM users WHERE is_active AND (company_ids IS NULL OR $1::uuid = ANY (company_ids)) ORDER BY full_name LIMIT 300`, cid)
		if err != nil {
			return nil, err
		}
		unplanned, err := queryJSON(ctx, tx, `SELECT j.id, j.number, j.title, c.name AS customer, j.duration_min, j.priority FROM service_jobs j
			JOIN customers c ON c.id = j.customer_id WHERE j.company_id = $1 AND j.scheduled_start IS NULL AND j.status = 'new' ORDER BY j.created_at LIMIT 200`, cid)
		if err != nil {
			return nil, err
		}
		warn := overlaps(bars)
		if warn == nil {
			warn = []ganttWarning{}
		}
		return map[string]any{"bars": bars, "technicians": techs, "unplanned": unplanned, "warnings": warn, "unit": "hour",
			"from": dayStr(from), "to": dayStr(to.AddDate(0, 0, -1))}, nil
	})
}

// PUT /gantt/jobs/{id} {start, technician_id, duration_min}: plans / moves a technician's job.
func (s *Server) ganttMoveJob(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Start        string `json:"start"`
		TechnicianID string `json:"technician_id"`
		DurationMin  int    `json:"duration_min"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	st, err := time.Parse(time.RFC3339, in.Start)
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if err != nil || !isUUID(id) || (in.TechnicianID != "" && !isUUID(in.TechnicianID)) || (in.DurationMin != 0 && (in.DurationMin < 5 || in.DurationMin > 10080)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM service_jobs WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, notFound()
			}
			return nil, err
		}
		if status == "done" || status == "cancelled" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if in.TechnicianID != "" {
			if err := checkCompanyUser(ctx, tx, cid, in.TechnicianID); err != nil {
				return nil, err
			}
		}
		var dur *int
		if in.DurationMin > 0 {
			dur = &in.DurationMin
		}
		if _, err := tx.Exec(ctx, `UPDATE service_jobs SET scheduled_start = $2, technician_id = coalesce(nullif($3, '')::uuid, technician_id),
			duration_min = coalesce($4, duration_min), status = CASE WHEN status = 'new' THEN 'scheduled' ELSE status END, updated_at = now() WHERE id = $1`,
			id, st, in.TechnicianID, dur); err != nil {
			return nil, err
		}
		var conflicts []string
		rows, err := tx.Query(ctx, `SELECT o.id::text FROM service_jobs j JOIN service_jobs o ON o.technician_id = j.technician_id AND o.id <> j.id
			AND o.status NOT IN ('done', 'cancelled') AND o.scheduled_start IS NOT NULL
			AND o.scheduled_start < j.scheduled_start + make_interval(mins => j.duration_min) AND j.scheduled_start < o.scheduled_start + make_interval(mins => o.duration_min)
			WHERE j.id = $1`, id)
		if err != nil {
			return nil, err
		}
		if conflicts, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			return nil, err
		}
		if conflicts == nil {
			conflicts = []string{}
		}
		job, err := queryJSONOne(ctx, tx, `SELECT id, scheduled_start, technician_id, duration_min, status FROM service_jobs WHERE id = $1`, id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"job": json.RawMessage(job), "conflicts": conflicts}, nil
	})
}
