package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Projects (app "prj"): projects with their own kanban stages, tasks (subtasks, checklist, milestones, comments and
// files through the chatter), timesheets (manual or a running timer per employee), billing (time & material at the
// project rate, reached milestones, or a fixed price in parts), and budget / profitability in the base currency:
// revenue = income lines of the project's invoices, cost = timesheet hours × the employee's hourly cost + linked
// expenses and supplier bills (their journal amounts).

var (
	projectBillings = []string{"none", "tm", "fixed", "milestone"}
	projectStatuses = []string{"active", "on_hold", "done", "cancelled"}
	taskPriorities  = []string{"low", "normal", "high", "urgent"}
)

// ======================= projects =======================

const projectCols = `p.id, p.number, p.code, p.name, p.customer_id, c.name AS customer_name, p.manager_id, mu.full_name AS manager_name,
	p.billing, p.currency, p.hourly_rate::text AS hourly_rate, p.fixed_price::text AS fixed_price, p.budget::text AS budget,
	trim_scale(p.budget_hours)::text AS budget_hours, p.start_date, p.end_date, p.status, p.description, p.color, p.created_at,
	(SELECT count(*) FROM project_tasks t WHERE t.project_id = p.id AND t.parent_id IS NULL) AS tasks,
	(SELECT count(*) FROM project_tasks t WHERE t.project_id = p.id AND t.parent_id IS NULL AND t.done_at IS NOT NULL) AS tasks_done,
	(SELECT trim_scale(coalesce(sum(ts.hours), 0))::text FROM timesheets ts WHERE ts.project_id = p.id) AS hours,
	(p.end_date IS NOT NULL AND p.end_date < current_date AND p.status = 'active') AS late`

const projectFrom = ` FROM projects p LEFT JOIN customers c ON c.id = p.customer_id LEFT JOIN users mu ON mu.id = p.manager_id`

// GET /prj/projects?status=open|all|<status>&q=
func (s *Server) prjProjects(w http.ResponseWriter, r *http.Request) {
	st, q := r.URL.Query().Get("status"), strings.TrimSpace(r.URL.Query().Get("q"))
	if st == "" {
		st = "open"
	}
	if (st != "open" && st != "all" && !contains(projectStatuses, st)) || len(q) > 100 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+projectCols+projectFrom+` WHERE p.company_id = $1
			AND ($2 = 'all' OR ($2 = 'open' AND p.status IN ('active', 'on_hold')) OR p.status = $2)
			AND ($3 = '' OR p.name ILIKE '%' || $3 || '%' OR p.code ILIKE '%' || $3 || '%' OR c.name ILIKE '%' || $3 || '%' OR p.number::text = $3)
			ORDER BY p.status <> 'active', p.number DESC LIMIT 1000`, r.PathValue("cid"), st, q)
	})
}

type projectIn struct {
	Code        *string `json:"code"`
	Name        *string `json:"name"`
	CustomerID  *string `json:"customer_id"`
	ManagerID   *string `json:"manager_id"`
	Billing     *string `json:"billing"`
	Currency    *string `json:"currency"`
	HourlyRate  *string `json:"hourly_rate"`
	FixedPrice  *string `json:"fixed_price"`
	Budget      *string `json:"budget"`
	BudgetHours *string `json:"budget_hours"`
	StartDate   *string `json:"start_date"`
	EndDate     *string `json:"end_date"`
	Status      *string `json:"status"`
	Description *string `json:"description"`
	Color       *string `json:"color"`
}

func (in *projectIn) valid() bool {
	for _, p := range []*string{in.StartDate, in.EndDate} {
		if p != nil && *p != "" && !validDate(*p) {
			return false
		}
	}
	for _, p := range []*string{in.HourlyRate, in.FixedPrice, in.Budget, in.BudgetHours} {
		if p != nil && *p != "" {
			if d, ok := money(*p); !ok || d.IsNegative() {
				return false
			}
		}
	}
	return optUUID(in.CustomerID) && optUUID(in.ManagerID) && (in.Billing == nil || contains(projectBillings, *in.Billing)) &&
		(in.Status == nil || contains(projectStatuses, *in.Status)) && (in.Currency == nil || currencyRe.MatchString(*in.Currency)) &&
		len(deref(in.Code)) <= 30 && len(deref(in.Name)) <= 200 && len(deref(in.Description)) <= 8000 && len(deref(in.Color)) <= 20
}

func zeroIfBlank(p *string) *string {
	if p != nil && strings.TrimSpace(*p) == "" {
		z := "0"
		return &z
	}
	return p
}

// default kanban columns, in the company's language
func projectStages(lang string) []string {
	switch lang {
	case "ar":
		return []string{"للتنفيذ", "قيد العمل", "للمراجعة", "منجز"}
	case "fr":
		return []string{"À faire", "En cours", "À valider", "Terminé"}
	}
	return []string{"To do", "In progress", "Review", "Done"}
}

func loadProject(ctx context.Context, tx pgx.Tx, cid, id string) (any, error) {
	p, err := queryJSONOne(ctx, tx, `SELECT `+projectCols+projectFrom+` WHERE p.id = $1 AND p.company_id = $2`, id, cid)
	if err != nil {
		return nil, err
	}
	stages, err := queryJSON(ctx, tx, `SELECT st.id, st.seq, st.name, st.is_done,
		(SELECT count(*) FROM project_tasks t WHERE t.stage_id = st.id AND t.parent_id IS NULL) AS tasks
		FROM project_stages st WHERE st.project_id = $1 ORDER BY st.seq`, id)
	if err != nil {
		return nil, err
	}
	ms, err := queryJSON(ctx, tx, `SELECT m.id, m.name, m.due_date, m.amount::text AS amount, m.reached_at, m.invoice_id, i.number AS invoice_number,
		(m.invoice_id IS NOT NULL AND i.status <> 'void') AS invoiced,
		(SELECT count(*) FROM project_tasks t WHERE t.milestone_id = m.id) AS tasks,
		(SELECT count(*) FROM project_tasks t WHERE t.milestone_id = m.id AND t.done_at IS NOT NULL) AS tasks_done
		FROM project_milestones m LEFT JOIN invoices i ON i.id = m.invoice_id WHERE m.project_id = $1 ORDER BY m.due_date NULLS LAST, m.created_at`, id)
	if err != nil {
		return nil, err
	}
	prof, err := projectProfit(ctx, tx, cid, id, "all")
	if err != nil {
		return nil, err
	}
	var sum json.RawMessage = []byte("null")
	if len(prof) > 0 {
		sum, _ = json.Marshal(prof[0])
	}
	links, err := queryJSON(ctx, tx, `SELECT l.id, l.doc_type, l.doc_id,
		coalesce(i.number, e.number, b.number) AS number, coalesce(i.issue_date, e.expense_date, b.bill_date) AS doc_date,
		coalesce(i.currency, e.currency, b.currency) AS currency, coalesce(i.total, e.amount, b.total)::text AS amount,
		coalesce(i.status, e.status, b.status) AS status, coalesce(e.description, su.name, cu.name, '') AS label
		FROM project_links l LEFT JOIN invoices i ON l.doc_type = 'invoice' AND i.id = l.doc_id LEFT JOIN customers cu ON cu.id = i.customer_id
		LEFT JOIN expenses e ON l.doc_type = 'expense' AND e.id = l.doc_id
		LEFT JOIN bills b ON l.doc_type = 'bill' AND b.id = l.doc_id LEFT JOIN suppliers su ON su.id = b.supplier_id
		WHERE l.project_id = $1 ORDER BY doc_date DESC NULLS LAST`, id)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"project": p, "stages": stages, "milestones": ms, "summary": sum, "links": links}, nil
}

// GET /prj/projects/{id}
func (s *Server) prjGetProject(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) { return loadProject(ctx, tx, cid, id) })
}

// POST /prj/projects and PATCH /prj/projects/{id}
func (s *Server) prjSaveProject(w http.ResponseWriter, r *http.Request) {
	var in projectIn
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	creating := id == ""
	if !in.valid() || (creating && strings.TrimSpace(deref(in.Name)) == "") || (in.Name != nil && strings.TrimSpace(*in.Name) == "") || (!creating && !isUUID(id)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	status := http.StatusOK
	if creating {
		status = http.StatusCreated
	}
	s.run(w, r, status, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if c := deref(in.CustomerID); c != "" {
			if _, err := lockCustomer(ctx, tx, cid, c); err != nil {
				return nil, err
			}
		}
		if m := deref(in.ManagerID); m != "" {
			if err := checkCompanyUser(ctx, tx, cid, m); err != nil {
				return nil, err
			}
		}
		if creating {
			var base, lang string
			if err := tx.QueryRow(ctx, `SELECT base_currency::text, coalesce(default_locale, 'en') FROM companies WHERE id = $1`, cid).Scan(&base, &lang); err != nil {
				return nil, err
			}
			cur := deref(in.Currency)
			if cur == "" {
				cur = base
			}
			billing := deref(in.Billing)
			if billing == "" {
				billing = "tm"
				if deref(in.CustomerID) == "" {
					billing = "none"
				}
			}
			if billing != "none" && deref(in.CustomerID) == "" {
				return nil, badRequest("project_needs_customer")
			}
			var n int64
			if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'project')`, cid).Scan(&n); err != nil {
				return nil, err
			}
			err := tx.QueryRow(ctx, `INSERT INTO projects (company_id, number, code, name, customer_id, manager_id, billing, currency, hourly_rate, fixed_price,
				budget, budget_hours, start_date, end_date, description, color)
				VALUES ($1, $2, coalesce(trim($3), ''), trim($4), nullif($5, '')::uuid, coalesce(nullif($6, '')::uuid, app_user()), $7, $8,
				coalesce($9::numeric, 0), coalesce($10::numeric, 0), coalesce($11::numeric, 0), coalesce($12::numeric, 0),
				nullif($13, '')::date, nullif($14, '')::date, coalesce($15, ''), coalesce($16, '')) RETURNING id::text`,
				cid, n, in.Code, deref(in.Name), deref(in.CustomerID), deref(in.ManagerID), billing, cur, zeroIfBlank(in.HourlyRate), zeroIfBlank(in.FixedPrice),
				zeroIfBlank(in.Budget), zeroIfBlank(in.BudgetHours), deref(in.StartDate), deref(in.EndDate), in.Description, in.Color).Scan(&id)
			if err != nil {
				return nil, err
			}
			for i, name := range projectStages(lang) {
				if _, err := tx.Exec(ctx, `INSERT INTO project_stages (company_id, project_id, seq, name, is_done) VALUES ($1, $2, $3, $4, $5)`,
					cid, id, i+1, name, i == 3); err != nil {
					return nil, err
				}
			}
			return loadProject(ctx, tx, cid, id)
		}
		tag, err := tx.Exec(ctx, `UPDATE projects SET code = coalesce(trim($3), code), name = coalesce(trim($4), name),
			customer_id = CASE WHEN $5::text IS NULL THEN customer_id ELSE nullif($5, '')::uuid END,
			manager_id = CASE WHEN $6::text IS NULL THEN manager_id ELSE nullif($6, '')::uuid END,
			billing = coalesce($7, billing), currency = coalesce($8, currency), hourly_rate = coalesce($9::numeric, hourly_rate),
			fixed_price = coalesce($10::numeric, fixed_price), budget = coalesce($11::numeric, budget), budget_hours = coalesce($12::numeric, budget_hours),
			start_date = CASE WHEN $13::text IS NULL THEN start_date ELSE nullif($13, '')::date END,
			end_date = CASE WHEN $14::text IS NULL THEN end_date ELSE nullif($14, '')::date END,
			status = coalesce($15, status), description = coalesce($16, description), color = coalesce($17, color), updated_at = now()
			WHERE id = $1 AND company_id = $2`, id, cid, in.Code, in.Name, in.CustomerID, in.ManagerID, in.Billing, in.Currency,
			zeroIfBlank(in.HourlyRate), zeroIfBlank(in.FixedPrice), zeroIfBlank(in.Budget), zeroIfBlank(in.BudgetHours), in.StartDate, in.EndDate,
			in.Status, in.Description, in.Color)
		if err != nil {
			if strings.Contains(err.Error(), "projects_check1") {
				return nil, badRequest("project_needs_customer")
			}
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return loadProject(ctx, tx, cid, id)
	})
}

// PUT /prj/projects/{id}/stages {stages: [{id?, name, is_done}]}: the kanban columns in order. A removed column
// must be empty.
func (s *Server) prjPutStages(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Stages []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			IsDone bool   `json:"is_done"`
		} `json:"stages"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) || len(in.Stages) == 0 || len(in.Stages) > 20 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	for _, st := range in.Stages {
		if strings.TrimSpace(st.Name) == "" || len(st.Name) > 80 || (st.ID != "" && !isUUID(st.ID)) {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if err := lockProject(ctx, tx, cid, id); err != nil {
			return nil, err
		}
		keep := []string{}
		for _, st := range in.Stages {
			if st.ID != "" {
				keep = append(keep, st.ID)
			}
		}
		var used int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM project_tasks WHERE project_id = $1 AND stage_id IS NOT NULL AND NOT (stage_id = ANY ($2::uuid[]))`,
			id, keep).Scan(&used); err != nil {
			return nil, err
		}
		if used > 0 {
			return nil, &apiError{status: http.StatusConflict, code: "stage_not_empty"}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM project_stages WHERE project_id = $1 AND NOT (id = ANY ($2::uuid[]))`, id, keep); err != nil {
			return nil, err
		}
		for i, st := range in.Stages {
			if st.ID == "" {
				if _, err := tx.Exec(ctx, `INSERT INTO project_stages (company_id, project_id, seq, name, is_done) VALUES ($1, $2, $3, trim($4), $5)`,
					cid, id, i+1, st.Name, st.IsDone); err != nil {
					return nil, err
				}
				continue
			}
			tag, err := tx.Exec(ctx, `UPDATE project_stages SET seq = $3, name = trim($4), is_done = $5 WHERE id = $1 AND project_id = $2`, st.ID, id, i+1, st.Name, st.IsDone)
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() == 0 {
				return nil, badRequest("invalid_stage")
			}
		}
		// tasks follow their column's "done" flag
		if _, err := tx.Exec(ctx, `UPDATE project_tasks t SET done_at = CASE WHEN st.is_done THEN coalesce(t.done_at, now()) ELSE NULL END
			FROM project_stages st WHERE st.id = t.stage_id AND t.project_id = $1 AND (t.done_at IS NOT NULL) <> st.is_done`, id); err != nil {
			return nil, err
		}
		return loadProject(ctx, tx, cid, id)
	})
}

func lockProject(ctx context.Context, tx pgx.Tx, cid, id string) error {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT true FROM projects WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound()
	}
	return err
}

// ======================= milestones =======================

// POST /prj/milestones {project_id, name, due_date, amount} and PATCH /prj/milestones/{id} {..., reached}
func (s *Server) prjSaveMilestone(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ProjectID *string `json:"project_id"`
		Name      *string `json:"name"`
		DueDate   *string `json:"due_date"`
		Amount    *string `json:"amount"`
		Reached   *bool   `json:"reached"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	creating := id == ""
	if (creating && (!isUUID(deref(in.ProjectID)) || strings.TrimSpace(deref(in.Name)) == "")) || (!creating && (!isUUID(id) || in.ProjectID != nil)) ||
		(in.Name != nil && (strings.TrimSpace(*in.Name) == "" || len(*in.Name) > 200)) || (in.DueDate != nil && *in.DueDate != "" && !validDate(*in.DueDate)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.Amount != nil && *in.Amount != "" {
		if d, ok := money(*in.Amount); !ok || d.IsNegative() {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	status := http.StatusOK
	if creating {
		status = http.StatusCreated
	}
	s.run(w, r, status, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var project string
		if creating {
			if err := lockProject(ctx, tx, cid, *in.ProjectID); err != nil {
				return nil, err
			}
			project = *in.ProjectID
			if err := tx.QueryRow(ctx, `INSERT INTO project_milestones (company_id, project_id, name, due_date, amount, reached_at)
				VALUES ($1, $2, trim($3), nullif($4, '')::date, coalesce($5::numeric, 0), CASE WHEN coalesce($6, false) THEN now() END) RETURNING id::text`,
				cid, project, *in.Name, deref(in.DueDate), zeroIfBlank(in.Amount), in.Reached).Scan(&id); err != nil {
				return nil, err
			}
		} else {
			var inv *string
			err := tx.QueryRow(ctx, `SELECT m.project_id::text, CASE WHEN i.status <> 'void' THEN m.invoice_id::text END FROM project_milestones m
				LEFT JOIN invoices i ON i.id = m.invoice_id WHERE m.id = $1 AND m.company_id = $2 FOR UPDATE OF m`, id, cid).Scan(&project, &inv)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, notFound()
			}
			if err != nil {
				return nil, err
			}
			if inv != nil && (in.Amount != nil || (in.Reached != nil && !*in.Reached)) {
				return nil, &apiError{status: http.StatusConflict, code: "milestone_invoiced"}
			}
			if _, err := tx.Exec(ctx, `UPDATE project_milestones SET name = coalesce(trim($2), name),
				due_date = CASE WHEN $3::text IS NULL THEN due_date ELSE nullif($3, '')::date END, amount = coalesce($4::numeric, amount),
				reached_at = CASE WHEN $5::boolean IS NULL THEN reached_at WHEN $5 THEN coalesce(reached_at, now()) ELSE NULL END WHERE id = $1`,
				id, in.Name, in.DueDate, zeroIfBlank(in.Amount), in.Reached); err != nil {
				return nil, err
			}
		}
		return loadProject(ctx, tx, cid, project)
	})
}

// ======================= tasks =======================

const taskCols = `t.id, t.number, t.project_id, p.name AS project_name, p.color AS project_color, t.stage_id, st.name AS stage_name, t.parent_id,
	t.milestone_id, ms.name AS milestone_name, t.ticket_id, t.title, t.description, t.assignee_id, au.full_name AS assignee_name, t.priority,
	t.deadline, trim_scale(t.planned_hours)::text AS planned_hours, t.checklist, t.tags, t.seq, t.done_at, t.created_at, t.updated_at,
	(SELECT count(*) FROM project_tasks x WHERE x.parent_id = t.id) AS subtasks,
	(SELECT count(*) FROM project_tasks x WHERE x.parent_id = t.id AND x.done_at IS NOT NULL) AS subtasks_done,
	(SELECT trim_scale(coalesce(sum(ts.hours), 0))::text FROM timesheets ts WHERE ts.task_id = t.id) AS hours,
	(SELECT count(*) FROM record_messages m WHERE m.entity_type = 'project_task' AND m.entity_id = t.id AND m.deleted_at IS NULL) AS comments,
	(SELECT count(*) FROM attachments a WHERE a.entity_type = 'project_task' AND a.entity_id = t.id AND a.deleted_at IS NULL) AS files,
	(t.deadline IS NOT NULL AND t.deadline < current_date AND t.done_at IS NULL) AS late`

const taskFrom = ` FROM project_tasks t JOIN projects p ON p.id = t.project_id LEFT JOIN project_stages st ON st.id = t.stage_id
	LEFT JOIN project_milestones ms ON ms.id = t.milestone_id LEFT JOIN users au ON au.id = t.assignee_id`

// GET /prj/project-tasks?project_id=&mine=1&open=1&q=&parent_id=
func (s *Server) prjTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	proj, parent, text := q.Get("project_id"), q.Get("parent_id"), strings.TrimSpace(q.Get("q"))
	if (proj != "" && !isUUID(proj)) || (parent != "" && !isUUID(parent)) || len(text) > 100 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+taskCols+taskFrom+` WHERE t.company_id = $1
			AND ($2 = '' OR t.project_id = nullif($2, '')::uuid)
			AND ($3 = '' OR t.parent_id = nullif($3, '')::uuid) AND (NOT $7 OR t.parent_id IS NULL)
			AND (NOT $4 OR t.assignee_id = app_user()) AND (NOT $5 OR t.done_at IS NULL)
			AND ($6 = '' OR t.title ILIKE '%' || $6 || '%' OR t.number::text = $6)
			ORDER BY st.seq NULLS LAST, t.seq, t.number LIMIT 2000`, r.PathValue("cid"), proj, parent, q.Get("mine") == "1", q.Get("open") == "1", text,
			q.Get("top") == "1")
	})
}

func loadTask(ctx context.Context, tx pgx.Tx, cid, id string) (any, error) {
	t, err := queryJSONOne(ctx, tx, `SELECT `+taskCols+taskFrom+` WHERE t.id = $1 AND t.company_id = $2`, id, cid)
	if err != nil {
		return nil, err
	}
	subs, err := queryJSON(ctx, tx, `SELECT `+taskCols+taskFrom+` WHERE t.parent_id = $1 ORDER BY t.seq, t.number`, id)
	if err != nil {
		return nil, err
	}
	ts, err := queryJSON(ctx, tx, `SELECT `+timesheetCols+timesheetFrom+` WHERE ts.task_id = $1 ORDER BY ts.work_date DESC, ts.created_at DESC LIMIT 500`, id)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"task": t, "subtasks": subs, "timesheets": ts}, nil
}

// GET /prj/project-tasks/{id}
func (s *Server) prjGetTask(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) { return loadTask(ctx, tx, cid, id) })
}

type checkItem struct {
	Text string `json:"text"`
	Done bool   `json:"done"`
}

type taskIn struct {
	ProjectID    *string      `json:"project_id"`
	StageID      *string      `json:"stage_id"`
	ParentID     *string      `json:"parent_id"`
	MilestoneID  *string      `json:"milestone_id"`
	TicketID     *string      `json:"ticket_id"`
	Title        *string      `json:"title"`
	Description  *string      `json:"description"`
	AssigneeID   *string      `json:"assignee_id"`
	Priority     *string      `json:"priority"`
	Deadline     *string      `json:"deadline"`
	PlannedHours *string      `json:"planned_hours"`
	Checklist    *[]checkItem `json:"checklist"`
	Tags         *[]string    `json:"tags"`
	Seq          *int         `json:"seq"`
	Done         *bool        `json:"done"`
}

func (in *taskIn) valid() bool {
	if in.Deadline != nil && *in.Deadline != "" && !validDate(*in.Deadline) {
		return false
	}
	if in.PlannedHours != nil && *in.PlannedHours != "" {
		if d, ok := money(*in.PlannedHours); !ok || d.IsNegative() || d.GreaterThan(decimal.NewFromInt(100000)) {
			return false
		}
	}
	if in.Checklist != nil {
		if len(*in.Checklist) > 100 {
			return false
		}
		for _, c := range *in.Checklist {
			if strings.TrimSpace(c.Text) == "" || len(c.Text) > 300 {
				return false
			}
		}
	}
	if in.Tags != nil {
		if len(*in.Tags) > 20 {
			return false
		}
		for _, t := range *in.Tags {
			if strings.TrimSpace(t) == "" || len(t) > 40 {
				return false
			}
		}
	}
	return optUUID(in.ProjectID) && optUUID(in.StageID) && optUUID(in.ParentID) && optUUID(in.MilestoneID) && optUUID(in.TicketID) && optUUID(in.AssigneeID) &&
		(in.Priority == nil || contains(taskPriorities, *in.Priority)) && len(deref(in.Title)) <= 300 && len(deref(in.Description)) <= 8000
}

// taskRefs checks that the stage / parent / milestone / ticket / assignee belong to the project and company.
func taskRefs(ctx context.Context, tx pgx.Tx, cid, project string, in *taskIn) error {
	one := func(sql string, id string, code string) error {
		var ok bool
		if err := tx.QueryRow(ctx, sql, id, project, cid).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return badRequest(code)
		}
		return nil
	}
	if v := deref(in.StageID); v != "" {
		if err := one(`SELECT EXISTS (SELECT 1 FROM project_stages WHERE id = $1 AND project_id = $2 AND company_id = $3)`, v, "invalid_stage"); err != nil {
			return err
		}
	}
	if v := deref(in.ParentID); v != "" {
		if err := one(`SELECT EXISTS (SELECT 1 FROM project_tasks WHERE id = $1 AND project_id = $2 AND company_id = $3 AND parent_id IS NULL)`, v, "invalid_parent"); err != nil {
			return err
		}
	}
	if v := deref(in.MilestoneID); v != "" {
		if err := one(`SELECT EXISTS (SELECT 1 FROM project_milestones WHERE id = $1 AND project_id = $2 AND company_id = $3)`, v, "invalid_milestone"); err != nil {
			return err
		}
	}
	if v := deref(in.TicketID); v != "" {
		if err := one(`SELECT EXISTS (SELECT 1 FROM tickets WHERE id = $1 AND $2::uuid IS NOT NULL AND company_id = $3)`, v, "invalid_ticket"); err != nil {
			return err
		}
	}
	if v := deref(in.AssigneeID); v != "" {
		if err := checkCompanyUser(ctx, tx, cid, v); err != nil {
			return err
		}
	}
	return nil
}

func cleanTags(p *[]string) []string {
	out := []string{}
	if p == nil {
		return out
	}
	seen := map[string]bool{}
	for _, t := range *p {
		t = strings.TrimSpace(t)
		if t != "" && !seen[strings.ToLower(t)] {
			seen[strings.ToLower(t)] = true
			out = append(out, t)
		}
	}
	return out
}

// POST /prj/project-tasks
func (s *Server) prjCreateTask(w http.ResponseWriter, r *http.Request) {
	var in taskIn
	if !decodeBody(w, r, &in) {
		return
	}
	if !in.valid() || !isUUID(deref(in.ProjectID)) || strings.TrimSpace(deref(in.Title)) == "" {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		project := *in.ProjectID
		var pstatus string
		err := tx.QueryRow(ctx, `SELECT status FROM projects WHERE id = $1 AND company_id = $2`, project, cid).Scan(&pstatus)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if pstatus == "cancelled" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if err := taskRefs(ctx, tx, cid, project, &in); err != nil {
			return nil, err
		}
		stage := deref(in.StageID)
		if stage == "" {
			_ = tx.QueryRow(ctx, `SELECT id::text FROM project_stages WHERE project_id = $1 ORDER BY seq LIMIT 1`, project).Scan(&stage)
		}
		var n int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'task')`, cid).Scan(&n); err != nil {
			return nil, err
		}
		cl := []checkItem{}
		if in.Checklist != nil {
			cl = *in.Checklist
		}
		clj, _ := json.Marshal(cl)
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO project_tasks (company_id, project_id, number, stage_id, parent_id, milestone_id, ticket_id, title, description,
			assignee_id, priority, deadline, planned_hours, checklist, tags, seq,
			done_at)
			VALUES ($1, $2, $3, nullif($4, '')::uuid, nullif($5, '')::uuid, nullif($6, '')::uuid, nullif($7, '')::uuid, trim($8), coalesce($9, ''),
			nullif($10, '')::uuid, coalesce($11, 'normal'), nullif($12, '')::date, coalesce($13::numeric, 0), $14::jsonb, $15,
			coalesce($16, (SELECT coalesce(max(seq), 0) + 1 FROM project_tasks WHERE project_id = $2)),
			CASE WHEN (SELECT is_done FROM project_stages WHERE id = nullif($4, '')::uuid) THEN now() END)
			RETURNING id::text`, cid, project, n, stage, deref(in.ParentID), deref(in.MilestoneID), deref(in.TicketID), *in.Title, in.Description,
			deref(in.AssigneeID), in.Priority, deref(in.Deadline), zeroIfBlank(in.PlannedHours), string(clj), cleanTags(in.Tags), in.Seq).Scan(&id); err != nil {
			return nil, err
		}
		if a := deref(in.AssigneeID); a != "" {
			if err := notifyUsers(ctx, tx, cid, []string{a}, "task", fmt.Sprintf("#%d %s", n, strings.TrimSpace(*in.Title)), principal(r).FullName, "project_task", id); err != nil {
				return nil, err
			}
		}
		return loadTask(ctx, tx, cid, id)
	})
}

// PATCH /prj/project-tasks/{id}: any field; moving to a "done" column (or done: true) closes the task.
func (s *Server) prjUpdateTask(w http.ResponseWriter, r *http.Request) {
	var in taskIn
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) || !in.valid() || in.ProjectID != nil || (in.Title != nil && strings.TrimSpace(*in.Title) == "") {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var project string
		var assignee *string
		var number int64
		var title string
		err := tx.QueryRow(ctx, `SELECT project_id::text, assignee_id::text, number, title FROM project_tasks WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).
			Scan(&project, &assignee, &number, &title)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if err := taskRefs(ctx, tx, cid, project, &in); err != nil {
			return nil, err
		}
		if deref(in.ParentID) == id {
			return nil, badRequest("invalid_parent")
		}
		var clj *string
		if in.Checklist != nil {
			b, _ := json.Marshal(*in.Checklist)
			x := string(b)
			clj = &x
		}
		var tags []string
		if in.Tags != nil {
			tags = cleanTags(in.Tags)
		}
		// done follows the column, unless done is given explicitly
		if in.Done != nil && in.StageID == nil {
			var st *string
			_ = tx.QueryRow(ctx, `SELECT id::text FROM project_stages WHERE project_id = $1 AND is_done = $2 ORDER BY seq LIMIT 1`, project, *in.Done).Scan(&st)
			if *in.Done {
				in.StageID = st
			} else {
				_ = tx.QueryRow(ctx, `SELECT id::text FROM project_stages WHERE project_id = $1 AND NOT is_done ORDER BY seq LIMIT 1`, project).Scan(&st)
				in.StageID = st
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE project_tasks SET
			stage_id = CASE WHEN $3::text IS NULL THEN stage_id ELSE nullif($3, '')::uuid END,
			parent_id = CASE WHEN $4::text IS NULL THEN parent_id ELSE nullif($4, '')::uuid END,
			milestone_id = CASE WHEN $5::text IS NULL THEN milestone_id ELSE nullif($5, '')::uuid END,
			ticket_id = CASE WHEN $6::text IS NULL THEN ticket_id ELSE nullif($6, '')::uuid END,
			title = coalesce(trim($7), title), description = coalesce($8, description),
			assignee_id = CASE WHEN $9::text IS NULL THEN assignee_id ELSE nullif($9, '')::uuid END,
			priority = coalesce($10, priority), deadline = CASE WHEN $11::text IS NULL THEN deadline ELSE nullif($11, '')::date END,
			planned_hours = coalesce($12::numeric, planned_hours), checklist = coalesce($13::jsonb, checklist), tags = coalesce($14, tags),
			seq = coalesce($15, seq), updated_at = now()
			WHERE id = $1 AND company_id = $2`, id, cid, in.StageID, in.ParentID, in.MilestoneID, in.TicketID, in.Title, in.Description, in.AssigneeID,
			in.Priority, in.Deadline, zeroIfBlank(in.PlannedHours), clj, tags, in.Seq); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE project_tasks t SET done_at = CASE WHEN coalesce(st.is_done, false) THEN coalesce(t.done_at, now()) ELSE NULL END
			FROM project_tasks x LEFT JOIN project_stages st ON st.id = x.stage_id WHERE x.id = t.id AND t.id = $1`, id); err != nil {
			return nil, err
		}
		if a := deref(in.AssigneeID); a != "" && (assignee == nil || *assignee != a) {
			if err := notifyUsers(ctx, tx, cid, []string{a}, "task", fmt.Sprintf("#%d %s", number, title), principal(r).FullName, "project_task", id); err != nil {
				return nil, err
			}
		}
		return loadTask(ctx, tx, cid, id)
	})
}

// ======================= timesheets =======================

const timesheetCols = `ts.id, ts.project_id, p.name AS project_name, ts.task_id, t.number AS task_number, t.title AS task_title, ts.employee_id,
	e.name AS employee_name, ts.work_date, trim_scale(ts.hours)::text AS hours, ts.description, ts.billable, ts.cost::text AS cost,
	ts.running_since, ts.invoice_id, i.number AS invoice_number, (ts.invoice_id IS NOT NULL AND i.status <> 'void') AS invoiced, ts.created_at`

const timesheetFrom = ` FROM timesheets ts JOIN projects p ON p.id = ts.project_id JOIN employees e ON e.id = ts.employee_id
	LEFT JOIN project_tasks t ON t.id = ts.task_id LEFT JOIN invoices i ON i.id = ts.invoice_id`

// GET /prj/timesheets?project_id=&employee_id=&from=&to=&mine=1
func (s *Server) prjTimesheets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	proj, emp, from, to := q.Get("project_id"), q.Get("employee_id"), q.Get("from"), q.Get("to")
	if from == "" {
		from = "2000-01-01"
	}
	if to == "" {
		to = "2100-01-01"
	}
	if (proj != "" && !isUUID(proj)) || (emp != "" && !isUUID(emp)) || !validDate(from) || !validDate(to) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	mine := q.Get("mine") == "1"
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		rows, err := queryJSON(ctx, tx, `SELECT `+timesheetCols+timesheetFrom+` WHERE ts.company_id = $1
			AND ($2 = '' OR ts.project_id = nullif($2, '')::uuid) AND ($3 = '' OR ts.employee_id = nullif($3, '')::uuid)
			AND ts.work_date BETWEEN $4::date AND $5::date AND (NOT $6 OR e.user_id = app_user())
			ORDER BY ts.work_date DESC, ts.created_at DESC LIMIT 3000`, r.PathValue("cid"), proj, emp, from, to, mine)
		if err != nil {
			return nil, err
		}
		var total, billable string
		if err := tx.QueryRow(ctx, `SELECT trim_scale(coalesce(sum(ts.hours), 0))::text, trim_scale(coalesce(sum(ts.hours) FILTER (WHERE ts.billable), 0))::text
			FROM timesheets ts JOIN employees e ON e.id = ts.employee_id WHERE ts.company_id = $1
			AND ($2 = '' OR ts.project_id = nullif($2, '')::uuid) AND ($3 = '' OR ts.employee_id = nullif($3, '')::uuid)
			AND ts.work_date BETWEEN $4::date AND $5::date AND (NOT $6 OR e.user_id = app_user())`,
			r.PathValue("cid"), proj, emp, from, to, mine).Scan(&total, &billable); err != nil {
			return nil, err
		}
		return map[string]any{"rows": rows, "total_hours": total, "billable_hours": billable}, nil
	})
}

// employeeHourly: the employee's hourly cost in the base currency (set, else monthly salary / 208 converted).
func employeeHourly(ctx context.Context, tx pgx.Tx, cid, emp string) (decimal.Decimal, error) {
	var hourly, salary, cur, base string
	if err := tx.QueryRow(ctx, `SELECT e.hourly_cost::text, e.salary::text, e.currency::text, c.base_currency::text FROM employees e
		JOIN companies c ON c.id = $2 WHERE e.id = $1`, emp, cid).Scan(&hourly, &salary, &cur, &base); err != nil {
		return decimal.Zero, err
	}
	h, _ := decimal.NewFromString(hourly)
	if h.IsPositive() {
		return h, nil
	}
	sal, _ := decimal.NewFromString(salary)
	if !sal.IsPositive() {
		return decimal.Zero, nil
	}
	h = sal.Div(decimal.NewFromInt(208))
	if cur != base {
		rate, err := resolveRate(ctx, tx, cid, base, cur, today(), "")
		if err != nil {
			return decimal.Zero, nil // no rate: cost unknown rather than refusing the entry
		}
		rd, _ := decimal.NewFromString(rate)
		if rd.IsPositive() {
			h = h.Div(rd)
		}
	}
	return h.Round(4), nil
}

// timesheetEmployee: the employee to log for. Someone else's needs projects.manage; none given = the signed-in
// user's own employee record.
func timesheetEmployee(ctx context.Context, tx pgx.Tx, r *http.Request, cid, emp string) (string, error) {
	if emp != "" {
		var mine bool
		err := tx.QueryRow(ctx, `SELECT coalesce(user_id = app_user(), false) FROM employees WHERE id = $1 AND company_id = $2 AND status = 'active'`, emp, cid).Scan(&mine)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", badRequest("invalid_employee")
		}
		if err != nil {
			return "", err
		}
		if !mine && !can(r, "projects.manage") {
			return "", &apiError{status: http.StatusForbidden, code: "forbidden"}
		}
		return emp, nil
	}
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM employees WHERE company_id = $1 AND user_id = app_user() AND status = 'active' ORDER BY created_at LIMIT 1`, cid).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", badRequest("no_employee")
	}
	return id, err
}

// timesheetTarget resolves project / task (the task's project wins) and refuses closed projects.
func timesheetTarget(ctx context.Context, tx pgx.Tx, cid, project, task string) (string, *string, error) {
	if task != "" {
		var p string
		err := tx.QueryRow(ctx, `SELECT project_id::text FROM project_tasks WHERE id = $1 AND company_id = $2`, task, cid).Scan(&p)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil, badRequest("invalid_task")
		}
		if err != nil {
			return "", nil, err
		}
		if project != "" && project != p {
			return "", nil, badRequest("invalid_task")
		}
		project = p
	}
	if project == "" {
		return "", nil, badRequest("bad_request")
	}
	var st string
	err := tx.QueryRow(ctx, `SELECT status FROM projects WHERE id = $1 AND company_id = $2`, project, cid).Scan(&st)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, badRequest("invalid_project")
	}
	if err != nil {
		return "", nil, err
	}
	if st == "cancelled" || st == "done" {
		return "", nil, &apiError{status: http.StatusConflict, code: "project_closed"}
	}
	var tp *string
	if task != "" {
		tp = &task
	}
	return project, tp, nil
}

// POST /prj/timesheets {project_id, task_id, employee_id, date, hours, description, billable}
func (s *Server) prjCreateTimesheet(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ProjectID   string `json:"project_id"`
		TaskID      string `json:"task_id"`
		EmployeeID  string `json:"employee_id"`
		Date        string `json:"date"`
		Hours       string `json:"hours"`
		Description string `json:"description"`
		Billable    *bool  `json:"billable"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Date == "" {
		in.Date = today()
	}
	h, ok := money(in.Hours)
	if !ok || !h.IsPositive() || h.GreaterThan(decimal.NewFromInt(24)) || !validDate(in.Date) || (in.ProjectID != "" && !isUUID(in.ProjectID)) ||
		(in.TaskID != "" && !isUUID(in.TaskID)) || (in.EmployeeID != "" && !isUUID(in.EmployeeID)) || len(in.Description) > 2000 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		project, task, err := timesheetTarget(ctx, tx, cid, in.ProjectID, in.TaskID)
		if err != nil {
			return nil, err
		}
		emp, err := timesheetEmployee(ctx, tx, r, cid, in.EmployeeID)
		if err != nil {
			return nil, err
		}
		hourly, err := employeeHourly(ctx, tx, cid, emp)
		if err != nil {
			return nil, err
		}
		billable := true
		if in.Billable != nil {
			billable = *in.Billable
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO timesheets (company_id, project_id, task_id, employee_id, user_id, work_date, hours, description, billable, hourly_cost, cost)
			VALUES ($1, $2, $3, $4, app_user(), $5::date, $6::numeric, trim($7), $8, $9::numeric, round($6::numeric * $9::numeric, 4)) RETURNING id::text`,
			cid, project, task, emp, in.Date, h.Round(2).String(), in.Description, billable, hourly.String()).Scan(&id); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+timesheetCols+timesheetFrom+` WHERE ts.id = $1`, id)
	})
}

// lockTimesheet: the row, if it is not invoiced and the user may change it (own, or projects.manage).
func lockTimesheet(ctx context.Context, tx pgx.Tx, r *http.Request, cid, id string) (bool, error) {
	var mine, invoiced, running bool
	err := tx.QueryRow(ctx, `SELECT coalesce(e.user_id = app_user(), false), (ts.invoice_id IS NOT NULL AND coalesce(i.status, '') <> 'void'), ts.running_since IS NOT NULL
		FROM timesheets ts JOIN employees e ON e.id = ts.employee_id LEFT JOIN invoices i ON i.id = ts.invoice_id
		WHERE ts.id = $1 AND ts.company_id = $2 FOR UPDATE OF ts`, id, cid).Scan(&mine, &invoiced, &running)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, notFound()
	}
	if err != nil {
		return false, err
	}
	if !mine && !can(r, "projects.manage") {
		return false, &apiError{status: http.StatusForbidden, code: "forbidden"}
	}
	if invoiced {
		return false, &apiError{status: http.StatusConflict, code: "timesheet_invoiced"}
	}
	return running, nil
}

// PATCH /prj/timesheets/{id} {date, hours, description, billable}
func (s *Server) prjUpdateTimesheet(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Date        *string `json:"date"`
		Hours       *string `json:"hours"`
		Description *string `json:"description"`
		Billable    *bool   `json:"billable"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	var hours *string
	if in.Hours != nil {
		h, ok := money(*in.Hours)
		if !ok || h.IsNegative() || h.GreaterThan(decimal.NewFromInt(24)) {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
		x := h.Round(2).String()
		hours = &x
	}
	if !isUUID(id) || (in.Date != nil && !validDate(*in.Date)) || len(deref(in.Description)) > 2000 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := lockTimesheet(ctx, tx, r, cid, id); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE timesheets SET work_date = coalesce($2::date, work_date), hours = coalesce($3::numeric, hours),
			description = coalesce(trim($4), description), billable = coalesce($5, billable), cost = round(coalesce($3::numeric, hours) * hourly_cost, 4),
			updated_at = now() WHERE id = $1`, id, in.Date, hours, in.Description, in.Billable); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+timesheetCols+timesheetFrom+` WHERE ts.id = $1`, id)
	})
}

// DELETE /prj/timesheets/{id}
func (s *Server) prjDeleteTimesheet(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := lockTimesheet(ctx, tx, r, cid, id); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM timesheets WHERE id = $1`, id); err != nil {
			return nil, err
		}
		return map[string]string{"deleted": id}, nil
	})
}

// GET /prj/timer: the signed-in user's running timer (null when none).
func (s *Server) prjTimer(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		t, err := queryJSONOne(ctx, tx, `SELECT `+timesheetCols+`, extract(epoch FROM now() - ts.running_since)::bigint AS elapsed_s`+timesheetFrom+`
			WHERE ts.company_id = $1 AND ts.running_since IS NOT NULL AND e.user_id = app_user() LIMIT 1`, r.PathValue("cid"))
		if errors.Is(err, pgx.ErrNoRows) || isNotFound(err) {
			return map[string]any{"timer": nil}, nil
		}
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"timer": t}, nil
	})
}

// POST /prj/timer/start {project_id, task_id, description}: starts the user's timer (a running one is stopped first).
// POST /prj/timer/stop: stops it; the elapsed time is added to the row (at least one minute).
func (s *Server) prjTimerAction(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ProjectID   string `json:"project_id"`
		TaskID      string `json:"task_id"`
		EmployeeID  string `json:"employee_id"`
		Description string `json:"description"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	action := r.PathValue("action")
	if (action != "start" && action != "stop") || (in.ProjectID != "" && !isUUID(in.ProjectID)) || (in.TaskID != "" && !isUUID(in.TaskID)) ||
		(in.EmployeeID != "" && !isUUID(in.EmployeeID)) || len(in.Description) > 2000 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		emp, err := timesheetEmployee(ctx, tx, r, cid, in.EmployeeID)
		if err != nil {
			return nil, err
		}
		// stop whatever runs for this employee
		var stopped *string
		_ = tx.QueryRow(ctx, `UPDATE timesheets SET hours = least(24, hours + greatest(round((extract(epoch FROM now() - running_since) / 3600)::numeric, 2), 0.02)),
			running_since = NULL, updated_at = now() WHERE employee_id = $1 AND company_id = $2 AND running_since IS NOT NULL RETURNING id::text`, emp, cid).Scan(&stopped)
		if stopped != nil {
			if _, err := tx.Exec(ctx, `UPDATE timesheets SET cost = round(hours * hourly_cost, 4) WHERE id = $1`, *stopped); err != nil {
				return nil, err
			}
		}
		if action == "stop" {
			if stopped == nil {
				return nil, &apiError{status: http.StatusConflict, code: "no_timer"}
			}
			return queryJSONOne(ctx, tx, `SELECT `+timesheetCols+timesheetFrom+` WHERE ts.id = $1`, *stopped)
		}
		project, task, err := timesheetTarget(ctx, tx, cid, in.ProjectID, in.TaskID)
		if err != nil {
			return nil, err
		}
		hourly, err := employeeHourly(ctx, tx, cid, emp)
		if err != nil {
			return nil, err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO timesheets (company_id, project_id, task_id, employee_id, user_id, work_date, hours, description, hourly_cost, running_since)
			VALUES ($1, $2, $3, $4, app_user(), current_date, 0, trim($5), $6::numeric, now()) RETURNING id::text`,
			cid, project, task, emp, in.Description, hourly.String()).Scan(&id); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+timesheetCols+timesheetFrom+` WHERE ts.id = $1`, id)
	})
}

// ======================= billing =======================

type prjBillable struct {
	Time       json.RawMessage `json:"time"`
	Milestones json.RawMessage `json:"milestones"`
	Fixed      map[string]any  `json:"fixed"`
}

func projectToInvoice(ctx context.Context, tx pgx.Tx, id string) (*prjBillable, error) {
	time_, err := queryJSON(ctx, tx, `SELECT ts.task_id, coalesce(t.title, '') AS task_title, t.number AS task_number, count(*) AS entries,
		trim_scale(sum(ts.hours))::text AS hours, json_agg(ts.id) AS ids, (p.hourly_rate * sum(ts.hours))::text AS amount
		FROM timesheets ts JOIN projects p ON p.id = ts.project_id LEFT JOIN project_tasks t ON t.id = ts.task_id LEFT JOIN invoices i ON i.id = ts.invoice_id
		WHERE ts.project_id = $1 AND ts.billable AND ts.running_since IS NULL AND ts.hours > 0 AND (ts.invoice_id IS NULL OR i.status = 'void')
		GROUP BY ts.task_id, t.title, t.number, p.hourly_rate ORDER BY t.number NULLS FIRST`, id)
	if err != nil {
		return nil, err
	}
	ms, err := queryJSON(ctx, tx, `SELECT m.id, m.name, m.amount::text AS amount, m.reached_at FROM project_milestones m LEFT JOIN invoices i ON i.id = m.invoice_id
		WHERE m.project_id = $1 AND m.reached_at IS NOT NULL AND m.amount > 0 AND (m.invoice_id IS NULL OR i.status = 'void') ORDER BY m.due_date NULLS LAST, m.created_at`, id)
	if err != nil {
		return nil, err
	}
	var price, invoiced string
	if err := tx.QueryRow(ctx, `SELECT p.fixed_price::text, coalesce((SELECT sum(il.amount) FROM project_links l JOIN invoices i ON i.id = l.doc_id AND i.status <> 'void'
		JOIN invoice_lines il ON il.invoice_id = i.id WHERE l.project_id = p.id AND l.doc_type = 'invoice'), 0)::text FROM projects p WHERE p.id = $1`, id).Scan(&price, &invoiced); err != nil {
		return nil, err
	}
	pd, _ := decimal.NewFromString(price)
	id_, _ := decimal.NewFromString(invoiced)
	rem := pd.Sub(id_)
	if rem.IsNegative() {
		rem = decimal.Zero
	}
	return &prjBillable{Time: time_, Milestones: ms, Fixed: map[string]any{"price": price, "invoiced": invoiced, "remaining": rem.String()}}, nil
}

// GET /prj/projects/{id}/to-invoice
func (s *Server) prjToInvoice(w http.ResponseWriter, r *http.Request) {
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
		return projectToInvoice(ctx, tx, id)
	})
}

// POST /prj/projects/{id}/invoice {date, kind: time|milestones|fixed (default from the project's billing), amount (fixed),
// percent (fixed), timesheet_ids (time: only these)}: one customer invoice; the billed rows remember it (void the
// invoice to bill them again).
func (s *Server) prjInvoice(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Date         string   `json:"date"`
		Kind         string   `json:"kind"`
		Amount       string   `json:"amount"`
		Percent      string   `json:"percent"`
		TimesheetIDs []string `json:"timesheet_ids"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Date == "" {
		in.Date = today()
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) || !validDate(in.Date) || (in.Kind != "" && in.Kind != "time" && in.Kind != "milestones" && in.Kind != "fixed") || len(in.TimesheetIDs) > 2000 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	for _, x := range in.TimesheetIDs {
		if !isUUID(x) {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	if in.TimesheetIDs == nil {
		in.TimesheetIDs = []string{}
	}
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var billing, currency, name, rate string
		var number int64
		var customer *string
		err := tx.QueryRow(ctx, `SELECT billing, currency::text, name, number, hourly_rate::text, customer_id::text FROM projects WHERE id = $1 AND company_id = $2 FOR UPDATE`,
			id, cid).Scan(&billing, &currency, &name, &number, &rate, &customer)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if customer == nil {
			return nil, badRequest("project_needs_customer")
		}
		kind := in.Kind
		if kind == "" {
			kind = map[string]string{"tm": "time", "milestone": "milestones", "fixed": "fixed"}[billing]
		}
		if kind == "" {
			return nil, badRequest("nothing_to_invoice")
		}
		label := fmt.Sprintf("Project #%d · %s", number, name)
		bill := invoiceIn{CustomerID: *customer, IssueDate: in.Date, Currency: currency, Notes: label}
		var tsIDs, msIDs []string
		switch kind {
		case "time":
			rd, _ := decimal.NewFromString(rate)
			if !rd.IsPositive() {
				return nil, badRequest("project_no_rate")
			}
			rows, err := tx.Query(ctx, `SELECT ts.id::text, coalesce(t.number::text, ''), coalesce(t.title, ''), ts.hours::text FROM timesheets ts
				LEFT JOIN project_tasks t ON t.id = ts.task_id LEFT JOIN invoices i ON i.id = ts.invoice_id
				WHERE ts.project_id = $1 AND ts.billable AND ts.running_since IS NULL AND ts.hours > 0 AND (ts.invoice_id IS NULL OR i.status = 'void')
				AND (cardinality($2::uuid[]) = 0 OR ts.id = ANY ($2::uuid[])) ORDER BY t.number NULLS FIRST, ts.work_date
				FOR UPDATE OF ts`, id, in.TimesheetIDs)
			if err != nil {
				return nil, err
			}
			type grp struct {
				label string
				hours decimal.Decimal
			}
			groups := map[string]*grp{}
			order := []string{}
			for rows.Next() {
				var tid, tn, tt, h string
				if err := rows.Scan(&tid, &tn, &tt, &h); err != nil {
					rows.Close()
					return nil, err
				}
				tsIDs = append(tsIDs, tid)
				key := tn
				if groups[key] == nil {
					l := name
					if tn != "" {
						l = "#" + tn + " " + tt
					}
					groups[key] = &grp{label: l}
					order = append(order, key)
				}
				hd, _ := decimal.NewFromString(h)
				groups[key].hours = groups[key].hours.Add(hd)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return nil, err
			}
			for _, k := range order {
				g := groups[k]
				bill.Lines = append(bill.Lines, invoiceLineIn{Description: g.label + " (h)", Quantity: g.hours.String(), UnitPrice: rd.String()})
			}
		case "milestones":
			rows, err := tx.Query(ctx, `SELECT m.id::text, m.name, m.amount::text FROM project_milestones m LEFT JOIN invoices i ON i.id = m.invoice_id
				WHERE m.project_id = $1 AND m.reached_at IS NOT NULL AND m.amount > 0 AND (m.invoice_id IS NULL OR i.status = 'void')
				ORDER BY m.due_date NULLS LAST, m.created_at FOR UPDATE OF m`, id)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var mid, mn, amt string
				if err := rows.Scan(&mid, &mn, &amt); err != nil {
					rows.Close()
					return nil, err
				}
				msIDs = append(msIDs, mid)
				bill.Lines = append(bill.Lines, invoiceLineIn{Description: name + " · " + mn, Quantity: "1", UnitPrice: amt})
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return nil, err
			}
		case "fixed":
			b, err := projectToInvoice(ctx, tx, id)
			if err != nil {
				return nil, err
			}
			rem, _ := decimal.NewFromString(b.Fixed["remaining"].(string))
			price, _ := decimal.NewFromString(b.Fixed["price"].(string))
			amt := rem
			if in.Percent != "" {
				p, ok := money(in.Percent)
				if !ok || !p.IsPositive() || p.GreaterThan(decimal.NewFromInt(100)) {
					return nil, badRequest("bad_request")
				}
				amt = price.Mul(p).Div(decimal.NewFromInt(100)).Round(4)
			} else if in.Amount != "" {
				a, ok := money(in.Amount)
				if !ok || !a.IsPositive() {
					return nil, badRequest("bad_request")
				}
				amt = a
			}
			if !amt.IsPositive() {
				return nil, badRequest("nothing_to_invoice")
			}
			if amt.GreaterThan(rem) {
				return nil, &apiError{status: http.StatusConflict, code: "over_fixed_price", detail: rem.String()}
			}
			bill.Lines = append(bill.Lines, invoiceLineIn{Description: label, Quantity: "1", UnitPrice: amt.String()})
		}
		if len(bill.Lines) == 0 {
			return nil, badRequest("nothing_to_invoice")
		}
		inv, err := createInvoiceTx(ctx, tx, r, cid, &bill)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO project_links (company_id, project_id, doc_type, doc_id) VALUES ($1, $2, 'invoice', $3)`, cid, id, inv); err != nil {
			return nil, err
		}
		if len(tsIDs) > 0 {
			if _, err := tx.Exec(ctx, `UPDATE timesheets SET invoice_id = $2, updated_at = now() WHERE id = ANY ($1::uuid[])`, tsIDs, inv); err != nil {
				return nil, err
			}
		}
		if len(msIDs) > 0 {
			if _, err := tx.Exec(ctx, `UPDATE project_milestones SET invoice_id = $2 WHERE id = ANY ($1::uuid[])`, msIDs, inv); err != nil {
				return nil, err
			}
		}
		return loadInvoice(ctx, tx, cid, inv)
	})
}

// POST /prj/projects/{id}/link {doc_type: expense|bill|invoice, doc_id} and DELETE /prj/links/{id}: charge an
// existing expense or supplier bill (or count an invoice) on the project.
func (s *Server) prjLink(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DocType string `json:"doc_type"`
		DocID   string `json:"doc_id"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	table := map[string]string{"expense": "expenses", "bill": "bills", "invoice": "invoices"}[in.DocType]
	if !isUUID(id) || table == "" || !isUUID(in.DocID) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if err := lockProject(ctx, tx, cid, id); err != nil {
			return nil, err
		}
		var st string
		err := tx.QueryRow(ctx, `SELECT status FROM `+table+` WHERE id = $1 AND company_id = $2`, in.DocID, cid).Scan(&st)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, badRequest("invalid_document")
		}
		if err != nil {
			return nil, err
		}
		if st == "void" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO project_links (company_id, project_id, doc_type, doc_id) VALUES ($1, $2, $3, $4)`, cid, id, in.DocType, in.DocID); err != nil {
			if isUniqueViolation(err) {
				return nil, &apiError{status: http.StatusConflict, code: "already_linked"}
			}
			return nil, err
		}
		return loadProject(ctx, tx, cid, id)
	})
}

func (s *Server) prjUnlink(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var project string
		err := tx.QueryRow(ctx, `DELETE FROM project_links WHERE id = $1 AND company_id = $2 RETURNING project_id::text`, id, cid).Scan(&project)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		return loadProject(ctx, tx, cid, project)
	})
}

// GET /prj/linkable?doc_type=expense|bill: recent documents not linked to a project yet.
func (s *Server) prjLinkable(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("doc_type")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		cid := r.PathValue("cid")
		switch kind {
		case "expense":
			return queryJSON(ctx, tx, `SELECT e.id, e.number, e.expense_date AS doc_date, e.currency, e.amount::text AS amount, e.description AS label FROM expenses e
				WHERE e.company_id = $1 AND e.status = 'posted' AND NOT EXISTS (SELECT 1 FROM project_links l WHERE l.doc_type = 'expense' AND l.doc_id = e.id)
				ORDER BY e.expense_date DESC, e.number DESC LIMIT 300`, cid)
		case "bill":
			return queryJSON(ctx, tx, `SELECT b.id, b.number, b.bill_date AS doc_date, b.currency, b.total::text AS amount, s.name AS label FROM bills b
				JOIN suppliers s ON s.id = b.supplier_id WHERE b.company_id = $1 AND b.status <> 'void'
				AND NOT EXISTS (SELECT 1 FROM project_links l WHERE l.doc_type = 'bill' AND l.doc_id = b.id)
				ORDER BY b.bill_date DESC, b.number DESC LIMIT 300`, cid)
		}
		return nil, badRequest("bad_request")
	})
}

// ======================= profitability =======================

type projectProfitRow struct {
	ID            string `json:"id"`
	Number        int64  `json:"number"`
	Name          string `json:"name"`
	CustomerName  string `json:"customer_name"`
	Status        string `json:"status"`
	Billing       string `json:"billing"`
	Hours         string `json:"hours"`
	BillableHours string `json:"billable_hours"`
	InvoicedHours string `json:"invoiced_hours"`
	ToInvoiceHrs  string `json:"to_invoice_hours"`
	BudgetHours   string `json:"budget_hours"`
	Labour        string `json:"labour_cost"`
	Expenses      string `json:"expenses_cost"`
	Bills         string `json:"bills_cost"`
	Cost          string `json:"total_cost"`
	Revenue       string `json:"revenue"`
	Margin        string `json:"margin"`
	MarginPct     string `json:"margin_pct"`
	Budget        string `json:"budget"`
	BudgetUsedPct string `json:"budget_used_pct"`
	Currency      string `json:"base_currency"`
}

// projectProfit: revenue and costs in the base currency, per project (one project when id is set).
func projectProfit(ctx context.Context, tx pgx.Tx, cid, id, status string) ([]projectProfitRow, error) {
	rows, err := tx.Query(ctx, `
		WITH p AS (SELECT p.* FROM projects p WHERE p.company_id = $1 AND ($2 = '' OR p.id = nullif($2, '')::uuid)
		             AND ($3 IN ('', 'all') OR ($3 = 'open' AND p.status IN ('active', 'on_hold')) OR p.status = $3)),
		ts AS (SELECT ts.project_id, sum(ts.hours) AS hours, sum(ts.hours) FILTER (WHERE ts.billable) AS billable,
		         sum(ts.hours) FILTER (WHERE ts.invoice_id IS NOT NULL AND i.status <> 'void') AS invoiced,
		         sum(ts.hours) FILTER (WHERE ts.billable AND (ts.invoice_id IS NULL OR i.status = 'void')) AS to_invoice, sum(ts.cost) AS cost
		       FROM timesheets ts LEFT JOIN invoices i ON i.id = ts.invoice_id WHERE ts.project_id IN (SELECT id FROM p) GROUP BY 1),
		rev AS (SELECT l.project_id, sum(jl.base_credit - jl.base_debit) AS amt FROM project_links l
		          JOIN invoices i ON i.id = l.doc_id AND i.status <> 'void' JOIN journal_lines jl ON jl.entry_id = i.journal_entry_id
		          JOIN accounts a ON a.id = jl.account_id AND a.type = 'income'
		        WHERE l.doc_type = 'invoice' AND l.project_id IN (SELECT id FROM p) GROUP BY 1),
		exp AS (SELECT l.project_id, sum(jl.base_debit - jl.base_credit) AS amt FROM project_links l
		          JOIN expenses e ON e.id = l.doc_id AND e.status = 'posted' JOIN journal_lines jl ON jl.entry_id = e.journal_entry_id AND jl.account_id = e.account_id
		        WHERE l.doc_type = 'expense' AND l.project_id IN (SELECT id FROM p) GROUP BY 1),
		bil AS (SELECT l.project_id, sum(jl.base_debit) AS amt FROM project_links l
		          JOIN bills b ON b.id = l.doc_id AND b.status <> 'void' JOIN journal_lines jl ON jl.entry_id = b.journal_entry_id
		          JOIN accounts a ON a.id = jl.account_id AND a.type IN ('expense', 'asset')
		        WHERE l.doc_type = 'bill' AND l.project_id IN (SELECT id FROM p) AND jl.base_debit > 0
		          AND NOT EXISTS (SELECT 1 FROM tax_rates tr WHERE tr.company_id = b.company_id AND jl.account_id IN (tr.account_id, tr.purchase_account_id))
		        GROUP BY 1)
		SELECT p.id::text, p.number, p.name, coalesce(c.name, ''), p.status, p.billing,
			coalesce(ts.hours, 0)::text, coalesce(ts.billable, 0)::text, coalesce(ts.invoiced, 0)::text, coalesce(ts.to_invoice, 0)::text, p.budget_hours::text,
			coalesce(ts.cost, 0)::text, coalesce(exp.amt, 0)::text, coalesce(bil.amt, 0)::text, coalesce(rev.amt, 0)::text, p.budget::text, co.base_currency::text
		FROM p JOIN companies co ON co.id = p.company_id LEFT JOIN customers c ON c.id = p.customer_id LEFT JOIN ts ON ts.project_id = p.id
		LEFT JOIN rev ON rev.project_id = p.id LEFT JOIN exp ON exp.project_id = p.id LEFT JOIN bil ON bil.project_id = p.id
		ORDER BY p.number DESC LIMIT 2000`, cid, id, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []projectProfitRow{}
	for rows.Next() {
		var x projectProfitRow
		if err := rows.Scan(&x.ID, &x.Number, &x.Name, &x.CustomerName, &x.Status, &x.Billing, &x.Hours, &x.BillableHours, &x.InvoicedHours,
			&x.ToInvoiceHrs, &x.BudgetHours, &x.Labour, &x.Expenses, &x.Bills, &x.Revenue, &x.Budget, &x.Currency); err != nil {
			return nil, err
		}
		d := func(s string) decimal.Decimal { v, _ := decimal.NewFromString(s); return v }
		cost := d(x.Labour).Add(d(x.Expenses)).Add(d(x.Bills))
		margin := d(x.Revenue).Sub(cost)
		x.Cost, x.Margin = cost.Round(2).String(), margin.Round(2).String()
		for _, p := range []*string{&x.Hours, &x.BillableHours, &x.InvoicedHours, &x.ToInvoiceHrs, &x.BudgetHours, &x.Labour, &x.Expenses, &x.Bills, &x.Revenue, &x.Budget} {
			*p = d(*p).Round(2).String()
		}
		if rv := d(x.Revenue); rv.IsPositive() {
			x.MarginPct = margin.Mul(decimal.NewFromInt(100)).Div(rv).Round(1).String()
		}
		if b := d(x.Budget); b.IsPositive() {
			x.BudgetUsedPct = cost.Mul(decimal.NewFromInt(100)).Div(b).Round(1).String()
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// GET /prj/reports/profitability?status=open|all|<status>
func (s *Server) prjProfitability(w http.ResponseWriter, r *http.Request) {
	st := r.URL.Query().Get("status")
	if st != "" && st != "open" && st != "all" && !contains(projectStatuses, st) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return projectProfit(ctx, tx, r.PathValue("cid"), "", st)
	})
}

// GET /prj/overview: counts for the projects home (open projects, my open tasks, late tasks, hours this week, to invoice).
func (s *Server) prjOverview(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		monday := time.Now().AddDate(0, 0, -((int(time.Now().Weekday()) + 6) % 7)).Format(time.DateOnly)
		return queryJSONOne(ctx, tx, `SELECT
			(SELECT count(*) FROM projects WHERE company_id = $1 AND status = 'active') AS active_projects,
			(SELECT count(*) FROM project_tasks WHERE company_id = $1 AND assignee_id = app_user() AND done_at IS NULL) AS my_tasks,
			(SELECT count(*) FROM project_tasks WHERE company_id = $1 AND done_at IS NULL AND deadline < current_date) AS late_tasks,
			(SELECT trim_scale(coalesce(sum(hours), 0))::text FROM timesheets WHERE company_id = $1 AND work_date >= $2::date) AS hours_week,
			(SELECT trim_scale(coalesce(sum(ts.hours), 0))::text FROM timesheets ts LEFT JOIN invoices i ON i.id = ts.invoice_id JOIN projects p ON p.id = ts.project_id
			  WHERE ts.company_id = $1 AND ts.billable AND p.billing = 'tm' AND (ts.invoice_id IS NULL OR i.status = 'void')) AS hours_to_invoice`,
			r.PathValue("cid"), monday)
	})
}
