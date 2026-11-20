package httpapi

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Phase 3: projects (stages, tasks, subtasks, checklist, milestones, timesheets and timer, billing by time /
// milestone / fixed price, profitability with linked costs), field service (job from a ticket, technician phone
// flow with parts, signature, invoice), helpdesk SLA (policies, breach, escalation, report, rating link, inbound
// e-mail and WhatsApp), automation rules (templates, events, schedule, dry run, loop guard) and fleet (fuel with
// consumption and anomalies, service plans, papers, costs posted as expenses). Ends with a balanced trial balance.

func TestProjectsServiceFleetFlow(t *testing.T) {
	needDB(t)
	ctx := context.Background()
	o := signup(t, "lebanon")
	me := str(o.c.ok("GET", "/me", nil, 200).body["id"])
	usd := o.methods["Cash USD"]

	// people: the owner is an employee at 10/h; a second employee costs salary / 208
	o.c.ok("POST", o.p("/employees"), map[string]any{"name": "Owner Emp", "currency": "USD", "salary": "1000", "user_id": me, "hourly_cost": "10"}, 201)
	emp2 := str(o.c.ok("POST", o.p("/employees"), map[string]any{"name": "Sami", "currency": "USD", "salary": "2080"}, 201).body["id"])
	techEmail := "tech" + randHex(4) + "@example.com"
	o.c.ok("POST", "/users", map[string]any{"email": techEmail, "full_name": "Tech One", "role": "collector", "password": "tech-secret-pass-9"}, 201)
	var tech string
	for _, u := range o.c.ok("GET", "/users", nil, 200).list {
		if str(obj(u)["email"]) == techEmail {
			tech = str(obj(u)["id"])
		}
	}
	tc := loginAs(t, techEmail, "tech-secret-pass-9")
	area := str(o.c.ok("POST", o.p("/areas"), map[string]any{"name": "North", "collector_id": tech}, 201).body["id"])
	cust := str(o.c.ok("POST", o.p("/customers"), map[string]any{"name": "Rima Haddad", "phone": "70123456", "email": "rima@example.com", "area_id": area}, 201).body["id"])
	o.c.ok("POST", o.p("/message-providers"), map[string]any{"kind": "log", "name": "log", "is_default": true}, 201)

	// ======================= projects =======================
	if r := o.c.do("POST", o.p("/prj/projects"), map[string]any{"name": "No customer", "billing": "tm"}); r.status != 400 {
		t.Fatalf("tm project without customer: %d %s", r.status, r.raw)
	}
	pr := o.c.ok("POST", o.p("/prj/projects"), map[string]any{"name": "Network upgrade", "customer_id": cust, "billing": "tm", "hourly_rate": "50",
		"budget": "1000", "budget_hours": "40"}, 201)
	prj := str(obj(pr.body["project"])["id"])
	stages := pr.body["stages"].([]any)
	if len(stages) != 4 || obj(stages[3])["is_done"] != true {
		t.Fatalf("default stages: %s", pr.raw)
	}
	todo, doneSt := str(obj(stages[0])["id"]), str(obj(stages[3])["id"])
	other := o.c.ok("POST", o.p("/prj/projects"), map[string]any{"name": "Internal"}, 201)
	if str(obj(other.body["project"])["billing"]) != "none" {
		t.Fatalf("project without customer bills nothing: %s", other.raw)
	}
	otherStage := str(obj(other.body["stages"].([]any)[0])["id"])

	task := o.c.ok("POST", o.p("/prj/project-tasks"), map[string]any{"project_id": prj, "title": "Install routers", "assignee_id": me, "priority": "high",
		"deadline": "2026-11-01", "planned_hours": "8", "checklist": []any{map[string]any{"text": "Order", "done": true}, map[string]any{"text": "Install"}}}, 201)
	taskID := str(obj(task.body["task"])["id"])
	if str(obj(task.body["task"])["stage_id"]) != todo || obj(task.body["task"])["late"] != true {
		t.Fatalf("task in first column, late: %s", task.raw)
	}
	o.c.ok("POST", o.p("/prj/project-tasks"), map[string]any{"project_id": prj, "parent_id": taskID, "title": "Configure VLANs"}, 201)
	if r := o.c.do("POST", o.p("/prj/project-tasks"), map[string]any{"project_id": prj, "stage_id": otherStage, "title": "x"}); r.status != 400 {
		t.Fatalf("stage of another project: %d", r.status)
	}
	got := o.c.ok("GET", o.p("/prj/project-tasks/"+taskID), nil, 200)
	if len(got.body["subtasks"].([]any)) != 1 || str(obj(got.body["task"])["subtasks"]) != "1" {
		t.Fatalf("subtask: %s", got.raw)
	}
	mv := o.c.ok("PATCH", o.p("/prj/project-tasks/"+taskID), map[string]any{"stage_id": doneSt}, 200)
	if obj(mv.body["task"])["done_at"] == nil {
		t.Fatalf("done column closes the task: %s", mv.raw)
	}
	mv = o.c.ok("PATCH", o.p("/prj/project-tasks/"+taskID), map[string]any{"done": false}, 200)
	if obj(mv.body["task"])["done_at"] != nil || str(obj(mv.body["task"])["stage_id"]) == doneSt {
		t.Fatalf("reopened: %s", mv.raw)
	}
	// comments and files on a task through the chatter
	o.c.ok("POST", o.p("/chatter"), map[string]any{"type": "project_task", "id": taskID, "body": "Routers ordered"}, 201)
	o.c.ok("POST", o.p("/attachments"), map[string]any{"type": "project_task", "id": taskID, "filename": "plan.txt", "data": "aGVsbG8="}, 201)
	if r := o.c.ok("GET", o.p("/prj/project-tasks/"+taskID), nil, 200); str(obj(r.body["task"])["comments"]) != "1" || str(obj(r.body["task"])["files"]) != "1" {
		t.Fatalf("task comments / files: %s", r.raw)
	}
	// a removed column must be empty
	if r := o.c.do("PUT", o.p("/prj/projects/"+prj+"/stages"), map[string]any{"stages": []any{map[string]any{"id": doneSt, "name": "Done", "is_done": true}}}); r.status != 409 {
		t.Fatalf("remove used column: %d %s", r.status, r.raw)
	}

	// timesheets: own (10/h), someone else's (2080/208 = 10/h), and the timer
	ts1 := o.c.ok("POST", o.p("/prj/timesheets"), map[string]any{"task_id": taskID, "date": "2026-11-10", "hours": "3", "description": "cabling"}, 201)
	eqDec(t, "own timesheet cost", ts1.body["cost"], "30")
	ts2 := o.c.ok("POST", o.p("/prj/timesheets"), map[string]any{"project_id": prj, "employee_id": emp2, "date": "2026-11-11", "hours": "2"}, 201)
	eqDec(t, "salary-based cost", ts2.body["cost"], "20")
	o.c.ok("POST", o.p("/prj/timesheets"), map[string]any{"project_id": prj, "date": "2026-11-11", "hours": "1", "billable": false}, 201)
	o.c.ok("POST", o.p("/prj/timer/start"), map[string]any{"task_id": taskID}, 200)
	if r := o.c.ok("GET", o.p("/prj/timer"), nil, 200); r.body["timer"] == nil {
		t.Fatalf("timer running: %s", r.raw)
	}
	stop := o.c.ok("POST", o.p("/prj/timer/stop"), map[string]any{}, 200)
	if dec(t, stop.body["hours"]).IsZero() {
		t.Fatalf("timer hours: %s", stop.raw)
	}
	timerH := str(stop.body["hours"])
	if r := o.c.do("POST", o.p("/prj/timer/stop"), map[string]any{}); r.status != 409 {
		t.Fatalf("stop without timer: %d", r.status)
	}
	if r := o.c.do("POST", o.p("/prj/timesheets"), map[string]any{"project_id": prj, "hours": "25"}); r.status != 400 {
		t.Fatalf("25 hours: %d", r.status)
	}

	// billing by time: billable hours × 50
	ti := o.c.ok("GET", o.p("/prj/projects/"+prj+"/to-invoice"), nil, 200)
	if len(ti.body["time"].([]any)) == 0 {
		t.Fatalf("to invoice: %s", ti.raw)
	}
	inv := o.c.ok("POST", o.p("/prj/projects/"+prj+"/invoice"), map[string]any{"date": "2026-11-15"}, 201)
	invID := str(obj(inv.body["invoice"])["id"])
	billable := dec(t, "5").Add(dec(t, timerH))
	eqDec(t, "time invoice", obj(inv.body["invoice"])["total"], billable.Mul(dec(t, "50")).String())
	if r := o.c.do("PATCH", o.p("/prj/timesheets/"+str(ts1.body["id"])), map[string]any{"hours": "4"}); r.status != 409 {
		t.Fatalf("change invoiced timesheet: %d %s", r.status, r.raw)
	}
	if r := o.c.do("POST", o.p("/prj/projects/"+prj+"/invoice"), map[string]any{}); r.status != 400 {
		t.Fatalf("nothing left to bill: %d %s", r.status, r.raw)
	}
	// a cost charged on the project
	o.c.ok("POST", o.p("/expenses"), map[string]any{"date": "2026-11-12", "account_id": o.accounts["611"], "method_id": usd, "amount": "100",
		"description": "cable", "project_id": prj}, 201)
	prof := o.c.ok("GET", o.p("/prj/projects/"+prj), nil, 200)
	sum := obj(prof.body["summary"])
	eqDec(t, "revenue", sum["revenue"], billable.Mul(dec(t, "50")).Round(2).String())
	eqDec(t, "expenses cost", sum["expenses_cost"], "100")
	labour := dec(t, "60").Add(dec(t, timerH).Mul(dec(t, "10"))).Round(2)
	eqDec(t, "labour cost", sum["labour_cost"], labour.String())
	eqDec(t, "margin", sum["margin"], billable.Mul(dec(t, "50")).Sub(labour).Sub(dec(t, "100")).Round(2).String())
	if len(prof.body["links"].([]any)) != 2 {
		t.Fatalf("project links: %s", prof.raw)
	}
	// void the invoice: the hours can be billed again
	o.c.ok("POST", o.p("/invoices/"+invID+"/void"), map[string]any{"reason": "redo"}, 200)
	if r := o.c.ok("GET", o.p("/prj/projects/"+prj+"/to-invoice"), nil, 200); len(r.body["time"].([]any)) == 0 {
		t.Fatalf("hours billable again after void: %s", r.raw)
	}
	o.c.ok("POST", o.p("/prj/projects/"+prj+"/invoice"), map[string]any{"date": "2026-11-16"}, 201)

	// milestone billing
	mp := o.c.ok("POST", o.p("/prj/projects"), map[string]any{"name": "Fit-out", "customer_id": cust, "billing": "milestone"}, 201)
	mpID := str(obj(mp.body["project"])["id"])
	ms := o.c.ok("POST", o.p("/prj/milestones"), map[string]any{"project_id": mpID, "name": "Design approved", "amount": "500"}, 201)
	msID := str(obj(ms.body["milestones"].([]any)[0])["id"])
	if r := o.c.do("POST", o.p("/prj/projects/"+mpID+"/invoice"), map[string]any{}); r.status != 400 {
		t.Fatalf("milestone not reached: %d", r.status)
	}
	o.c.ok("PATCH", o.p("/prj/milestones/"+msID), map[string]any{"reached": true}, 200)
	mi := o.c.ok("POST", o.p("/prj/projects/"+mpID+"/invoice"), map[string]any{}, 201)
	eqDec(t, "milestone invoice", obj(mi.body["invoice"])["total"], "500")
	if r := o.c.do("PATCH", o.p("/prj/milestones/"+msID), map[string]any{"amount": "600"}); r.status != 409 {
		t.Fatalf("invoiced milestone amount: %d", r.status)
	}
	// fixed price in parts
	fp := o.c.ok("POST", o.p("/prj/projects"), map[string]any{"name": "Website", "customer_id": cust, "billing": "fixed", "fixed_price": "1000"}, 201)
	fpID := str(obj(fp.body["project"])["id"])
	fi := o.c.ok("POST", o.p("/prj/projects/"+fpID+"/invoice"), map[string]any{"percent": "30"}, 201)
	eqDec(t, "30 % of fixed", obj(fi.body["invoice"])["total"], "300")
	if r := o.c.do("POST", o.p("/prj/projects/"+fpID+"/invoice"), map[string]any{"amount": "800"}); r.status != 409 {
		t.Fatalf("over the fixed price: %d %s", r.status, r.raw)
	}
	if r := o.c.ok("GET", o.p("/prj/projects/"+fpID+"/to-invoice"), nil, 200); str(obj(r.body["fixed"])["remaining"]) != "700" {
		t.Fatalf("fixed remaining: %s", r.raw)
	}
	if rows := o.c.ok("GET", o.p("/prj/reports/profitability?status=all"), nil, 200).list; len(rows) != 4 {
		t.Fatalf("profitability rows: %d", len(rows))
	}
	o.c.ok("GET", o.p("/prj/overview"), nil, 200)

	// ======================= SLA, tickets, field service =======================
	o.c.ok("POST", o.p("/sla/sla-policies"), map[string]any{"name": "Urgent", "priority": "urgent", "response_hours": "1", "resolution_hours": "4", "escalate_to": me}, 201)
	o.c.ok("POST", o.p("/sla/sla-policies"), map[string]any{"name": "Standard", "response_hours": "8", "resolution_hours": "48"}, 201)
	tk := o.c.ok("POST", o.p("/tickets"), map[string]any{"customer_id": cust, "subject": "No power at home", "priority": "urgent", "category": "outage"}, 201)
	tkID := str(tk.body["id"])
	if str(tk.body["sla_name"]) != "Urgent" || tk.body["resolution_due"] == nil || str(tk.body["sla_state"]) != "ok" {
		t.Fatalf("urgent SLA: %s", tk.raw)
	}
	tk2 := o.c.ok("POST", o.p("/tickets"), map[string]any{"subject": "Question", "priority": "low"}, 201)
	if str(tk2.body["sla_name"]) != "Standard" {
		t.Fatalf("catch-all SLA: %s", tk2.raw)
	}
	// breach: deadlines in the past, escalation once
	if _, err := itOwnerPool.Exec(ctx, `UPDATE tickets SET response_due = now() - interval '2 hours', resolution_due = now() - interval '1 minute' WHERE id = $1`, tkID); err != nil {
		t.Fatal(err)
	}
	if r := o.c.ok("GET", o.p("/tickets/"+tkID), nil, 200); str(obj(r.body["ticket"])["sla_state"]) != "breached" {
		t.Fatalf("breached: %s", r.raw)
	}
	if err := itAPI.RunSLAEscalations(ctx); err != nil {
		t.Fatal(err)
	}
	if err := itAPI.RunSLAEscalations(ctx); err != nil {
		t.Fatal(err)
	}
	if n := ownerQuery(t, `SELECT count(*) FROM ticket_notes WHERE ticket_id = $1 AND body LIKE 'SLA breached%'`, tkID); n != "1" {
		t.Fatalf("escalated once: %s", n)
	}
	if n := ownerQuery(t, `SELECT count(*) FROM user_notifications WHERE user_id = $1 AND kind = 'sla'`, me); n != "1" {
		t.Fatalf("manager told: %s", n)
	}
	rep := o.c.ok("GET", o.p("/tickets/sla-report?from=2000-01-01&to=2100-01-01"), nil, 200)
	if str(obj(rep.body["total"])["tickets"]) != "2" || len(rep.body["breached_open"].([]any)) != 1 {
		t.Fatalf("sla report: %s", rep.raw)
	}

	// stock for parts
	sup := str(o.c.ok("POST", o.p("/suppliers"), map[string]any{"name": "Parts Co", "currency": "USD"}, 201).body["id"])
	part := str(o.c.ok("POST", o.p("/items"), map[string]any{"code": "FUSE", "name": "Fuse 32A", "kind": "stock", "sale_price": "15"}, 201).body["id"])
	o.c.ok("POST", o.p("/bills"), map[string]any{"supplier_id": sup, "bill_date": "2026-11-01", "currency": "USD",
		"lines": []any{map[string]any{"item_id": part, "quantity": "10", "unit_price": "5"}}}, 201)
	wh := ownerQuery(t, `SELECT warehouse_id::text FROM stock WHERE item_id = $1`, part)

	// a job from the ticket, planned for the technician
	start := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	job := o.c.ok("POST", o.p("/fs/service-jobs"), map[string]any{"ticket_id": tkID, "technician_id": tech, "scheduled_start": start, "labour_rate": "20",
		"warehouse_id": wh}, 201)
	jobID := str(obj(job.body["job"])["id"])
	if str(obj(job.body["job"])["status"]) != "scheduled" || str(obj(job.body["job"])["customer_id"]) != cust || str(obj(job.body["job"])["title"]) != "No power at home" {
		t.Fatalf("job from ticket: %s", job.raw)
	}
	if r := o.c.ok("GET", o.p("/tickets/"+tkID), nil, 200); str(obj(r.body["ticket"])["status"]) != "in_progress" {
		t.Fatalf("ticket in progress: %s", r.raw)
	}
	board := o.c.ok("GET", o.p("/fs/board?from="+time.Now().Format("2006-01-02")), nil, 200)
	if len(board.body["jobs"].([]any)) != 1 {
		t.Fatalf("board: %s", board.raw)
	}
	// the technician (collector role) works the job from the phone
	mine := tc.ok("GET", o.p("/fs/my-jobs"), nil, 200)
	if len(mine.list) != 1 {
		t.Fatalf("my jobs: %s", mine.raw)
	}
	tc.ok("GET", o.p("/fs/service-jobs/"+jobID), nil, 200)
	if r := tc.do("PATCH", o.p("/fs/service-jobs/"+jobID), map[string]any{"title": "x"}); r.status != 403 {
		t.Fatalf("technician edits plan: %d", r.status)
	}
	tc.ok("POST", o.p("/fs/service-jobs/"+jobID+"/start"), map[string]any{"lat": 33.89, "lng": 35.5}, 200)
	if cat := tc.ok("GET", o.p("/fs/parts-catalog?q=fuse&warehouse_id="+wh), nil, 200); len(cat.list) != 1 || str(obj(cat.list[0])["on_hand"]) != "10" {
		t.Fatalf("parts catalog: %s", cat.raw)
	}
	tc.ok("PUT", o.p("/fs/service-jobs/"+jobID+"/parts"), map[string]any{"parts": []any{map[string]any{"item_id": part, "quantity": "2"}}}, 200)
	tc.ok("POST", o.p("/fs/service-jobs/"+jobID+"/photo"), map[string]any{"filename": "site.png", "data": "data:image/png;base64,iVBORw0KGgo="}, 201)
	fin := tc.ok("POST", o.p("/fs/service-jobs/"+jobID+"/finish"), map[string]any{"report": "Replaced fuse", "signed_by": "Rima",
		"signature": "data:image/png;base64,iVBORw0KGgo=", "labour_hours": "1.5"}, 200)
	if str(obj(fin.body["job"])["status"]) != "done" || obj(fin.body["job"])["signed"] != true {
		t.Fatalf("finished: %s", fin.raw)
	}
	if r := tc.do("POST", o.p("/fs/service-jobs/"+jobID+"/invoice"), map[string]any{}); r.status != 403 {
		t.Fatalf("technician invoices: %d", r.status)
	}
	if r := tc.do("GET", o.p("/fs/service-jobs"), nil); r.status != 403 {
		t.Fatalf("technician lists all jobs: %d", r.status)
	}
	ji := o.c.ok("POST", o.p("/fs/service-jobs/"+jobID+"/invoice"), map[string]any{"date": "2026-11-20"}, 201)
	if obj(ji.body["job"])["invoiced"] != true {
		t.Fatalf("job invoiced: %s", ji.raw)
	}
	jinv := str(obj(ji.body["job"])["invoice_id"])
	eqDec(t, "job invoice (1.5 h × 20 + 2 × 15)", obj(o.c.ok("GET", o.p("/invoices/"+jinv), nil, 200).body["invoice"])["total"], "60")
	if q := ownerQuery(t, `SELECT trim_scale(quantity)::text FROM stock WHERE item_id = $1`, part); q != "8" {
		t.Fatalf("parts left stock: %s", q)
	}
	if r := o.c.do("POST", o.p("/fs/service-jobs/"+jobID+"/invoice"), map[string]any{}); r.status != 409 {
		t.Fatalf("invoice twice: %d", r.status)
	}
	if n := ownerQuery(t, `SELECT count(*) FROM ticket_notes WHERE ticket_id = $1 AND body LIKE 'service job #%: done%'`, tkID); n != "1" {
		t.Fatalf("job outcome on the ticket: %s", n)
	}
	// closing the ticket gives the customer a rating link (sent by WhatsApp)
	closed := o.c.ok("PATCH", o.p("/tickets/"+tkID), map[string]any{"status": "done"}, 200)
	tok := str(closed.body["rating_token"])
	if len(tok) < 32 {
		t.Fatalf("rating token: %s", closed.raw)
	}
	anon := newClient(t)
	if r := anon.ok("GET", "/rate/"+tok, nil, 200); str(r.body["subject"]) != "No power at home" {
		t.Fatalf("rating page: %s", r.raw)
	}
	anon.ok("POST", "/rate/"+tok, map[string]any{"rating": 5, "comment": "Fast!"}, 200)
	if r := anon.do("POST", "/rate/"+strings.Repeat("a", 48), map[string]any{"rating": 5}); r.status != 404 {
		t.Fatalf("unknown rating token: %d", r.status)
	}
	if r := o.c.ok("GET", o.p("/tickets/"+tkID), nil, 200); str(obj(r.body["ticket"])["rating"]) != "5" {
		t.Fatalf("rating stored: %s", r.raw)
	}

	// inbound e-mail and WhatsApp
	in := o.c.ok("POST", o.p("/sla/inbound/rotate"), map[string]any{}, 200)
	itok := str(in.body["verify_token"])
	em := anon.ok("POST", "/inbound/"+itok+"/email", map[string]any{"from": "Rima <rima@example.com>", "subject": "Router down", "text": "Since this morning"}, 200)
	if em.body["created"] != true {
		t.Fatalf("e-mail ticket: %s", em.raw)
	}
	n := str(em.body["ticket"])
	if c := ownerQuery(t, `SELECT customer_id::text FROM tickets WHERE number = $1 AND company_id = $2`, n, o.company); c != cust {
		t.Fatalf("e-mail ticket customer: %s", c)
	}
	re := anon.ok("POST", "/inbound/"+itok+"/email", map[string]any{"from": "rima@example.com", "subject": "Re: [#" + n + "] Router down", "text": "Still down"}, 200)
	if re.body["created"] != false || str(re.body["ticket"]) != n {
		t.Fatalf("reply joins the ticket: %s", re.raw)
	}
	wa := map[string]any{"entry": []any{map[string]any{"changes": []any{map[string]any{"value": map[string]any{
		"contacts": []any{map[string]any{"wa_id": "96170123456", "profile": map[string]any{"name": "Rima"}}},
		"messages": []any{map[string]any{"from": "96170123456", "type": "text", "text": map[string]any{"body": "Meter broken"}}}}}}}}}
	w1 := anon.ok("POST", "/inbound/"+itok+"/whatsapp", wa, 200)
	w2 := anon.ok("POST", "/inbound/"+itok+"/whatsapp", wa, 200)
	r1, r2 := obj(w1.body["received"].([]any)[0]), obj(w2.body["received"].([]any)[0])
	if r1["created"] != true || r2["created"] != false || str(r1["ticket"]) != str(r2["ticket"]) {
		t.Fatalf("whatsapp tickets: %s / %s", w1.raw, w2.raw)
	}
	if c := ownerQuery(t, `SELECT channel || ':' || customer_id::text FROM tickets WHERE number = $1 AND company_id = $2`, str(r1["ticket"]), o.company); c != "whatsapp:"+cust {
		t.Fatalf("whatsapp ticket customer: %s", c)
	}
	if r := anon.do("GET", "/inbound/"+itok+"/whatsapp?hub.mode=subscribe&hub.verify_token="+itok+"&hub.challenge=12345", nil); r.status != 200 || r.raw != "12345" {
		t.Fatalf("whatsapp verify: %d %s", r.status, r.raw)
	}
	if r := anon.do("POST", "/inbound/"+strings.Repeat("b", 48)+"/email", map[string]any{"from": "a@b.c", "text": "x"}); r.status != 404 {
		t.Fatalf("unknown inbound token: %d", r.status)
	}

	// ======================= automation =======================
	if r := o.c.do("POST", o.p("/automation/rules"), map[string]any{"name": "bad", "model": "invoice", "trigger": "scheduled",
		"conditions": []any{map[string]any{"field": "status", "op": "changed"}}, "actions": []any{map[string]any{"type": "tag", "tag": "x"}}}); r.status != 400 {
		t.Fatalf("changed on a schedule: %d", r.status)
	}
	if r := o.c.do("POST", o.p("/automation/rules"), map[string]any{"name": "bad", "model": "item_stock", "trigger": "created",
		"actions": []any{map[string]any{"type": "purchase_request"}}}); r.status != 400 {
		t.Fatalf("stock rules are scheduled only: %d", r.status)
	}
	// overdue invoice reminder + task for the area's collector
	ov := o.c.ok("POST", o.p("/invoices"), map[string]any{"customer_id": cust, "currency": "USD", "issue_date": "2026-09-01", "due_date": "2026-09-10",
		"lines": []any{map[string]any{"description": "September", "quantity": "1", "unit_price": "40"}}}, 201)
	ovID := str(obj(ov.body["invoice"])["id"])
	rule := o.c.ok("POST", o.p("/automation/templates/overdue_invoice"), nil, 201)
	ruleID := str(rule.body["id"])
	dry := o.c.ok("POST", o.p("/automation/rules/"+ruleID+"/test"), map[string]any{}, 200)
	found := false
	for _, m := range dry.body["matched"].([]any) {
		if str(obj(m)["record_id"]) == ovID {
			found = true
		}
	}
	if !found || ownerQuery(t, `SELECT count(*) FROM record_activities WHERE entity_id = $1`, ovID) != "0" {
		t.Fatalf("dry run matches and changes nothing: %s", dry.raw)
	}
	msgsBefore := ownerQuery(t, `SELECT count(*) FROM messages WHERE company_id = $1`, o.company)
	if _, err := itAPI.RunAutomations(ctx, true); err != nil {
		t.Fatal(err)
	}
	if a := ownerQuery(t, `SELECT coalesce((SELECT assigned_to::text || ':' || kind FROM record_activities WHERE entity_id = $1), '')
		|| ' runs: ' || coalesce((SELECT string_agg(status || ' ' || detail::text, ' | ') FROM automation_runs WHERE rule_id = $2), '')`, ovID, ruleID); !strings.HasPrefix(a, tech+":call runs: ") {
		t.Fatalf("collector task: %q", a)
	}
	if m := ownerQuery(t, `SELECT count(*) FROM messages WHERE company_id = $1`, o.company); m == msgsBefore {
		t.Fatalf("reminder message queued: %s", m)
	}
	if _, err := itAPI.RunAutomations(ctx, true); err != nil {
		t.Fatal(err)
	}
	if a := ownerQuery(t, `SELECT count(*) FROM record_activities WHERE entity_id = $1`, ovID); a != "1" {
		t.Fatalf("acts once per record: %s", a)
	}
	// event rule: an urgent ticket is assigned, tagged and announced; the rule's own change queues nothing (loop guard)
	ev := o.c.ok("POST", o.p("/automation/rules"), map[string]any{"name": "Urgent to tech", "model": "ticket", "trigger": "created",
		"conditions": []any{map[string]any{"field": "priority", "op": "eq", "value": "urgent"}},
		"actions": []any{map[string]any{"type": "set_field", "field": "assigned_to", "value": tech}, map[string]any{"type": "tag", "tag": "hot"},
			map[string]any{"type": "notify", "assign": tech, "body": "Urgent: {subject}"}}}, 201)
	upd := o.c.ok("POST", o.p("/automation/rules"), map[string]any{"name": "Ticket assigned or moved", "model": "ticket", "trigger": "updated",
		"watch_fields": []string{"assigned_to", "status"}, "actions": []any{map[string]any{"type": "tag", "tag": "touched"}}}, 201)
	ut := o.c.ok("POST", o.p("/tickets"), map[string]any{"subject": "Fire at station", "priority": "urgent"}, 201)
	utID := str(ut.body["id"])
	if _, err := itAPI.RunAutomations(ctx, false); err != nil {
		t.Fatal(err)
	}
	if a := ownerQuery(t, `SELECT assigned_to::text FROM tickets WHERE id = $1`, utID); a != tech {
		t.Fatalf("rule assigned the ticket: %q", a)
	}
	if tg := ownerQuery(t, `SELECT string_agg(tag, ',' ORDER BY tag) FROM record_tags WHERE entity_id = $1`, utID); tg != "hot" {
		t.Fatalf("tags (the rule's own update must not trigger the update rule): %q", tg)
	}
	if n := ownerQuery(t, `SELECT count(*) FROM user_notifications WHERE user_id = $1 AND kind = 'automation' AND body LIKE 'Urgent: Fire%'`, tech); n != "1" {
		t.Fatalf("technician told: %s", n)
	}
	// a person changing the ticket fires the update rule
	o.c.ok("PATCH", o.p("/tickets/"+utID), map[string]any{"status": "in_progress"}, 200)
	if _, err := itAPI.RunAutomations(ctx, false); err != nil {
		t.Fatal(err)
	}
	if tg := ownerQuery(t, `SELECT string_agg(tag, ',' ORDER BY tag) FROM record_tags WHERE entity_id = $1`, utID); tg != "hot,touched" {
		t.Fatalf("update rule: %q", tg)
	}
	runs := o.c.ok("GET", o.p("/automation/runs?rule_id="+str(ev.body["id"])), nil, 200)
	if len(runs.list) != 1 || str(obj(runs.list[0])["status"]) != "done" {
		t.Fatalf("run log: %s", runs.raw)
	}
	o.c.ok("PATCH", o.p("/automation/rules/"+str(upd.body["id"])), map[string]any{"is_active": false}, 200)
	// low stock → purchase request from the reorder rule's supplier
	o.c.ok("PUT", o.p("/stock/reorder-rules"), map[string]any{"item_id": part, "warehouse_id": wh, "min_qty": "20", "max_qty": "50", "supplier_id": sup}, 200)
	o.c.ok("POST", o.p("/automation/templates/low_stock"), nil, 201)
	if _, err := itAPI.RunAutomations(ctx, true); err != nil {
		t.Fatal(err)
	}
	if q := ownerQuery(t, `SELECT trim_scale(l.quantity)::text FROM purchase_orders o JOIN purchase_order_lines l ON l.order_id = o.id
		WHERE o.company_id = $1 AND o.notes LIKE 'Automation%' AND l.item_id = $2`, o.company, part); q != "42" {
		t.Fatalf("purchase request for 50 - 8: %q", q)
	}
	meta := o.c.ok("GET", o.p("/automation/meta"), nil, 200)
	if len(meta.body["templates"].([]any)) < 6 {
		t.Fatalf("templates: %s", meta.raw)
	}

	// ======================= fleet =======================
	o.c.ok("PUT", o.p("/fleet/settings"), map[string]any{"fuel_account_id": o.accounts["601"], "service_account_id": o.accounts["611"]}, 200)
	soon := time.Now().AddDate(0, 0, 10).Format("2006-01-02")
	veh := o.c.ok("POST", o.p("/fleet/vehicles"), map[string]any{"name": "Van 1", "plate": "M 12345", "kind": "van", "make": "Toyota", "model": "Hiace",
		"year": 2019, "insurance_expiry": soon, "driver_id": tech}, 201)
	vid := str(veh.body["id"])
	if r := o.c.ok("GET", o.p("/wh/vehicles"), nil, 200); len(r.list) != 1 {
		t.Fatalf("the delivery board sees the same vehicle: %s", r.raw)
	}
	fuel := func(date, odo, liters, amount string) resp {
		body := map[string]any{"vehicle_id": vid, "date": date, "odometer": odo, "liters": liters}
		if amount != "" {
			body["amount"], body["method_id"] = amount, usd
		}
		return o.c.ok("POST", o.p("/fleet/fuel"), body, 201)
	}
	fuel("2026-11-01", "1000", "40", "")
	f2 := fuel("2026-11-05", "1500", "40", "50")
	eqDec(t, "consumption", f2.body["consumption"], "8")
	f3 := fuel("2026-11-10", "2000", "70", "80")
	eqDec(t, "consumption jump", f3.body["consumption"], "14")
	if str(f3.body["anomaly"]) != "high_consumption" {
		t.Fatalf("high consumption flagged: %s", f3.raw)
	}
	if f4 := fuel("2026-11-12", "1900", "5", ""); str(f4.body["anomaly"]) != "odometer_back" {
		t.Fatalf("odometer back: %s", f4.raw)
	}
	eqDec(t, "fuel expense", o.baseBalance(t, "601"), "130")
	plan := o.c.ok("POST", o.p("/fleet/service-plans"), map[string]any{"vehicle_id": vid, "task": "Oil change", "every_km": "100"}, 201)
	if str(plan.body["state"]) != "ok" {
		t.Fatalf("new plan: %s", plan.raw)
	}
	if _, err := itOwnerPool.Exec(ctx, `UPDATE vehicles SET odometer = 2150 WHERE id = $1`, vid); err != nil {
		t.Fatal(err)
	}
	al := o.c.ok("GET", o.p("/fleet/alerts"), nil, 200)
	if len(al.body["documents"].([]any)) != 1 || len(al.body["services"].([]any)) != 1 || len(al.body["fuel"].([]any)) != 2 {
		t.Fatalf("alerts: %s", al.raw)
	}
	o.c.ok("POST", o.p("/fleet/service-log"), map[string]any{"plan_id": str(plan.body["id"]), "date": "2026-11-15", "amount": "35", "method_id": usd, "vendor": "Garage"}, 201)
	if r := o.c.ok("GET", o.p("/fleet/service-plans?vehicle_id="+vid), nil, 200); str(obj(r.list[0])["state"]) != "ok" || str(obj(r.list[0])["last_km"]) != "2150" {
		t.Fatalf("plan reset: %s", r.raw)
	}
	costs := o.c.ok("GET", o.p("/fleet/reports/costs?from=2026-01-01&to=2026-12-31"), nil, 200)
	row := obj(costs.body["rows"].([]any)[0])
	eqDec(t, "fuel cost", row["fuel_cost"], "130")
	eqDec(t, "service cost", row["service_cost"], "35")
	eqDec(t, "total cost", row["total_cost"], "165")
	o.c.ok("POST", o.p("/fleet/fuel/"+str(f3.body["id"])+"/void"), map[string]any{}, 200)
	eqDec(t, "fuel expense after void", o.baseBalance(t, "601"), "50")
	// fleet papers reminder (scheduled rule, tells the rule's creator)
	o.c.ok("POST", o.p("/automation/templates/fleet_papers"), nil, 201)
	if _, err := itAPI.RunAutomations(ctx, true); err != nil {
		t.Fatal(err)
	}
	if n := ownerQuery(t, `SELECT count(*) FROM automation_runs r JOIN automation_rules x ON x.id = r.rule_id WHERE x.company_id = $1 AND x.model = 'vehicle' AND r.status = 'done'`,
		o.company); n != "1" {
		t.Fatalf("fleet papers rule: %s", n)
	}
	vd := o.c.ok("GET", o.p("/fleet/vehicles/"+vid), nil, 200)
	if len(vd.body["fuel"].([]any)) != 4 || len(vd.body["log"].([]any)) != 1 {
		t.Fatalf("vehicle detail: %s", vd.raw)
	}

	// permissions: a read-only user sees projects but cannot log time or invoice
	roEmail := "ro" + randHex(4) + "@example.com"
	o.c.ok("POST", "/users", map[string]any{"email": roEmail, "full_name": "RO", "role": "read_only", "password": "ro-secret-pass-9"}, 201)
	ro := loginAs(t, roEmail, "ro-secret-pass-9")
	ro.ok("GET", o.p("/prj/projects"), nil, 200)
	if r := ro.do("POST", o.p("/prj/timesheets"), map[string]any{"project_id": prj, "hours": "1"}); r.status != 403 {
		t.Fatalf("read-only logs time: %d", r.status)
	}
	if r := ro.do("POST", o.p("/fleet/fuel"), map[string]any{"vehicle_id": vid, "odometer": "1", "liters": "1"}); r.status != 403 {
		t.Fatalf("read-only adds fuel: %d", r.status)
	}

	o.trialBalanced(t)
}
