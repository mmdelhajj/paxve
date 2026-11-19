package httpapi

import "testing"

// Service desk: a fault report goes new → in progress → done with notes; open list and summary follow.
func TestTickets(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	cust := o.customer(t, "Hassan", "")
	a := o.c.ok("POST", o.p("/tickets"), map[string]any{"customer_id": cust, "subject": "No power since 8pm", "category": "outage", "priority": "urgent"}, 201)
	b := o.c.ok("POST", o.p("/tickets"), map[string]any{"subject": "New connection request", "category": "new_connection", "contact_phone": "+96170000000"}, 201)
	if a.body["number"].(float64) != 1 || b.body["number"].(float64) != 2 || str(a.body["customer_name"]) != "Hassan" {
		t.Fatalf("numbers: %s / %s", a.raw, b.raw)
	}
	id := str(a.body["id"])
	if r := o.c.do("POST", o.p("/tickets"), map[string]any{"subject": "x", "category": "nope"}); r.status != 400 {
		t.Fatalf("bad category: %d", r.status)
	}
	o.c.ok("PATCH", o.p("/tickets/"+id), map[string]any{"status": "in_progress", "assigned_to": str(o.c.ok("GET", "/me", nil, 200).body["id"])}, 200)
	o.c.ok("POST", o.p("/tickets/"+id+"/notes"), map[string]any{"body": "Breaker replaced on box 12"}, 201)
	done := o.c.ok("PATCH", o.p("/tickets/"+id), map[string]any{"status": "done", "resolution": "Breaker replaced"}, 200)
	if done.body["resolved_at"] == nil {
		t.Fatalf("resolved_at: %s", done.raw)
	}
	full := o.c.ok("GET", o.p("/tickets/"+id), nil, 200)
	if n := len(full.body["notes"].([]any)); n != 3 { // two status changes + one note
		t.Fatalf("notes: %d %s", n, full.raw)
	}
	open := o.c.ok("GET", o.p("/tickets"), nil, 200)
	if len(open.list) != 1 || str(obj(open.list[0])["subject"]) != "New connection request" {
		t.Fatalf("open: %s", open.raw)
	}
	if all := o.c.ok("GET", o.p("/tickets?status=all&customer_id="+cust), nil, 200); len(all.list) != 1 {
		t.Fatalf("customer tickets: %s", all.raw)
	}
	sum := o.c.ok("GET", o.p("/tickets/summary"), nil, 200)
	if sum.body["open"].(float64) != 1 || sum.body["closed_30d"].(float64) != 1 {
		t.Fatalf("summary: %s", sum.raw)
	}
	// app switched off: 403
	o.c.ok("PUT", o.p("/apps"), map[string]any{"apps": []string{"msg"}}, 200)
	if r := o.c.do("GET", o.p("/tickets"), nil); r.status != 403 {
		t.Fatalf("tickets with app off: %d", r.status)
	}
}

// Generator maintenance: an oil change every 250 h becomes "due" when the hour meter passes it and
// moves forward when the work is logged.
func TestGeneratorMaintenance(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	st := str(o.c.ok("POST", o.p("/stations"), map[string]any{"name": "North"}, 201).body["id"])
	gen := str(o.c.ok("POST", o.p("/generators"), map[string]any{"station_id": st, "name": "G1", "kva": "500"}, 201).body["id"])
	o.c.ok("POST", o.p("/generators/"+gen+"/hours"), map[string]any{"date": "2026-11-01", "hour_meter": "1030"}, 200)
	plan := str(o.c.ok("POST", o.p("/maintenance/tasks"), map[string]any{"generator_id": gen, "task": "Oil change", "every_hours": "250",
		"last_done_hours": "800", "last_done_date": "2026-10-01"}, 201).body["id"])
	o.c.ok("POST", o.p("/maintenance/tasks"), map[string]any{"generator_id": gen, "task": "Air filter", "every_days": 90, "last_done_date": "2026-10-01"}, 201)
	if r := o.c.do("POST", o.p("/maintenance/tasks"), map[string]any{"generator_id": gen, "task": "Nothing"}); r.status != 400 {
		t.Fatalf("plan without interval: %d", r.status)
	}
	state := func() map[string]string {
		out := map[string]string{}
		for _, x := range o.c.ok("GET", o.p("/maintenance"), nil, 200).list {
			out[str(obj(x)["task"])] = str(obj(x)["state"])
		}
		return out
	}
	if s := state(); s["Oil change"] != "soon" { // 1030 of 1050: inside the last 10 % of the interval
		t.Fatalf("state: %v", s)
	}
	o.c.ok("POST", o.p("/generators/"+gen+"/hours"), map[string]any{"date": "2026-11-10", "hour_meter": "1060"}, 200)
	if s := state(); s["Oil change"] != "due" {
		t.Fatalf("state after hours: %v", s)
	}
	o.c.ok("POST", o.p("/maintenance/log"), map[string]any{"plan_id": plan, "done_date": "2026-11-10", "cost": "45", "currency": "USD"}, 201)
	if s := state(); s["Oil change"] != "ok" {
		t.Fatalf("state after log: %v", s)
	}
	log := o.c.ok("GET", o.p("/maintenance/log?generator_id="+gen), nil, 200)
	if len(log.list) != 1 || str(obj(log.list[0])["hour_meter"]) != "1060.0" {
		t.Fatalf("log: %s", log.raw)
	}
}
