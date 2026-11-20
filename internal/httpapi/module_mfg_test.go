package httpapi

import (
	"strings"
	"testing"
)

// Manufacturing pro (phase 2): multi-level BoM (sub-assembly + phantom kit + by-product), cycle guard, versions
// (one active), structure and rolled-up cost, standard cost; MRP from a sales order (MOs for the product and the
// sub-assembly, a PO from a reorder rule, no duplicates on a second run); confirm → work orders + quality checks +
// reservation; shop-floor start / pause / finish with a measure that fails (alert) and finish blocked while a check
// is to do; partial production with a backorder; costing with work center time, by-product share and the standard
// cost variance; scrap on an order; subcontracting (send components, receive, service bill); receipt checks;
// lot traceability backward and forward; reports; stock = ledger and a balanced trial balance.

func TestManufacturingProFlow(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	mk := func(code, name string) string {
		return str(o.c.ok("POST", o.p("/items"), map[string]any{"code": code, "name": name, "kind": "stock", "unit": "pc", "sale_price": "100"}, 201).body["id"])
	}
	steel, screw, frame, box, label, kitp, chair, offcut, bracket := mk("STL", "Steel bar"), mk("SCR", "Screw"), mk("FRM", "Chair frame"),
		mk("BOX", "Box"), mk("LBL", "Label"), mk("PKG", "Packaging kit"), mk("CHR", "Chair"), mk("OFF", "Offcuts"), mk("BRK", "Bracket")
	for _, it := range []string{steel, frame, chair} {
		o.c.ok("PUT", o.p("/items/"+it+"/stock-settings"), map[string]any{"tracking": "lot"}, 200)
	}
	sup := str(o.c.ok("POST", o.p("/suppliers"), map[string]any{"name": "Metal Co", "currency": "USD"}, 201).body["id"])
	painter := str(o.c.ok("POST", o.p("/suppliers"), map[string]any{"name": "Paint Co", "currency": "USD"}, 201).body["id"])
	o.c.ok("POST", o.p("/bills"), map[string]any{"supplier_id": sup, "bill_date": "2026-11-01", "currency": "USD", "lines": []any{
		map[string]any{"item_id": steel, "quantity": "50", "unit_price": "10", "lots": []any{map[string]any{"lot_no": "S-1", "quantity": "50"}}},
		map[string]any{"item_id": screw, "quantity": "200", "unit_price": "0.5"},
		map[string]any{"item_id": box, "quantity": "20", "unit_price": "1"},
		map[string]any{"item_id": label, "quantity": "20", "unit_price": "0.2"}}}, 201)
	wh := str(obj(o.c.ok("GET", o.p("/wh/warehouses"), nil, 200).list[0])["id"])

	// ---- work centers, routing, quality points ----
	weld := str(obj(o.c.ok("POST", o.p("/mfg/workcenters"), map[string]any{"code": "WLD", "name": "Welding", "cost_per_hour": "60", "capacity": "1"}, 201).body)["id"])
	asm := str(obj(o.c.ok("POST", o.p("/mfg/workcenters"), map[string]any{"code": "ASM", "name": "Assembly", "cost_per_hour": "30"}, 201).body)["id"])
	if r := o.c.do("POST", o.p("/mfg/workcenters"), map[string]any{"code": "WLD", "name": "Again"}); r.status != 409 {
		t.Fatalf("duplicate work center: %d %s", r.status, r.raw)
	}
	rt := o.c.ok("POST", o.p("/mfg/routings"), map[string]any{"name": "Chair line", "operations": []any{
		map[string]any{"name": "Weld", "workcenter_id": weld, "minutes": "30"}, map[string]any{"name": "Assemble", "workcenter_id": asm, "minutes": "20"}}}, 201)
	routing := str(obj(rt.body["routing"])["id"])
	o.c.ok("POST", o.p("/mfg/quality-points"), map[string]any{"name": "Weld width", "trigger_on": "operation", "workcenter_id": weld, "test_type": "measure",
		"norm": "5", "tol_min": "4", "tol_max": "6", "unit": "mm"}, 201)
	o.c.ok("POST", o.p("/mfg/quality-points"), map[string]any{"name": "Final inspection", "trigger_on": "production", "item_id": chair}, 201)
	o.c.ok("POST", o.p("/mfg/quality-points"), map[string]any{"name": "Steel certificate", "trigger_on": "receipt", "item_id": steel}, 201)
	if r := o.c.do("POST", o.p("/mfg/quality-points"), map[string]any{"name": "Bad", "trigger_on": "production", "test_type": "measure", "tol_min": "6", "tol_max": "4"}); r.status != 400 {
		t.Fatalf("bad tolerances: %d %s", r.status, r.raw)
	}

	// ---- BoMs: frame (sub-assembly), phantom packaging kit, chair with routing and a by-product ----
	o.c.ok("POST", o.p("/mfg/boms"), map[string]any{"item_id": frame, "lines": []any{map[string]any{"item_id": steel, "quantity": "2"}, map[string]any{"item_id": screw, "quantity": "4"}}}, 201)
	o.c.ok("POST", o.p("/mfg/boms"), map[string]any{"item_id": kitp, "bom_type": "phantom", "lines": []any{map[string]any{"item_id": box, "quantity": "1"}, map[string]any{"item_id": label, "quantity": "1"}}}, 201)
	cb := o.c.ok("POST", o.p("/mfg/boms"), map[string]any{"item_id": chair, "code": "CH", "routing_id": routing, "overhead_cost": "1",
		"lines":      []any{map[string]any{"item_id": frame, "quantity": "1"}, map[string]any{"item_id": screw, "quantity": "8"}, map[string]any{"item_id": kitp, "quantity": "1"}},
		"byproducts": []any{map[string]any{"item_id": offcut, "quantity": "1", "cost_share": "10"}}}, 201)
	chairBom := str(obj(cb.body["bom"])["id"])
	if r := o.c.do("POST", o.p("/mfg/boms"), map[string]any{"item_id": steel, "lines": []any{map[string]any{"item_id": chair, "quantity": "1"}}}); r.status != 422 || !strings.Contains(r.raw, "bom_cycle") {
		t.Fatalf("cycle not refused: %d %s", r.status, r.raw)
	}
	if r := o.c.do("POST", o.p("/mfg/boms"), map[string]any{"item_id": bracket, "bom_type": "subcontract", "lines": []any{map[string]any{"item_id": steel, "quantity": "1"}}}); r.status != 400 {
		t.Fatalf("subcontract without subcontractor: %d", r.status)
	}
	// versions: v2 activated deactivates v1, then back to v1
	v2 := str(obj(o.c.ok("POST", o.p("/mfg/boms/"+chairBom+"/new-version"), map[string]any{"version_note": "longer weld"}, 201).body["bom"])["id"])
	o.c.ok("POST", o.p("/mfg/boms/"+v2+"/activate"), map[string]any{}, 200)
	b1 := o.c.ok("GET", o.p("/mfg/boms/"+chairBom), nil, 200)
	if obj(b1.body["bom"])["is_active"] != false || len(b1.body["versions"].([]any)) != 2 {
		t.Fatalf("versions: %s", b1.raw)
	}
	o.c.ok("POST", o.p("/mfg/boms/"+chairBom+"/activate"), map[string]any{}, 200)
	if b := o.c.ok("GET", o.p("/mfg/boms/"+v2), nil, 200); obj(b.body["bom"])["is_active"] != false || obj(b.body["bom"])["version"] != float64(2) {
		t.Fatalf("v2 should be inactive: %s", b.raw)
	}
	// structure / cost: frame 2×10 + 4×0.5 = 22, screws 4, kit (phantom) 1.2, weld 30 min × 60/h = 30, assembly 20 min × 30/h = 10,
	// overhead 1 → 68.2, by-product 10 % = 6.82 → chair 61.38
	stc := o.c.ok("GET", o.p("/mfg/boms/"+chairBom+"/structure?qty=1"), nil, 200)
	eqDec(t, "chair rolled-up cost", stc.body["unit_cost"], "61.38")
	if comps := stc.body["components"].([]any); obj(comps[0])["sub"] == nil || str(obj(comps[0])["source"]) != "bom" {
		t.Fatalf("frame sub-BoM: %s", stc.raw)
	}
	o.c.ok("POST", o.p("/mfg/boms/"+chairBom+"/set-standard"), map[string]any{}, 200)

	// ---- MRP from a sales order ----
	cust := o.customer(t, "Furniture Shop", "")
	so := o.c.ok("POST", o.p("/sales/orders"), map[string]any{"customer_id": cust, "currency": "USD", "lines": []any{
		map[string]any{"item_id": chair, "description": "Chair", "quantity": "5", "unit_price": "150"}}}, 201)
	soID := str(obj(so.body["order"])["id"])
	o.c.ok("POST", o.p("/sales/orders/"+soID+"/confirm"), map[string]any{}, 200)
	if r := o.c.do("PUT", o.p("/stock/reorder-rules"), map[string]any{"item_id": label, "warehouse_id": wh, "min_qty": "18", "max_qty": "30", "supplier_id": sup}); r.status >= 300 {
		t.Fatalf("reorder rule: %d %s", r.status, r.raw)
	}
	mrp := o.c.ok("GET", o.p("/mfg/mrp?warehouse_id="+wh), nil, 200)
	sugg := map[string]map[string]any{}
	for _, l := range mrp.body["lines"].([]any) {
		sugg[str(obj(l)["item_code"])] = obj(l)
	}
	if str(sugg["CHR"]["kind"]) != "mo" || str(sugg["FRM"]["kind"]) != "mo" || str(sugg["LBL"]["kind"]) != "po" || str(sugg["STL"]["kind"]) != "" {
		t.Fatalf("mrp suggestions: %s", mrp.raw)
	}
	eqDec(t, "chairs to make", sugg["CHR"]["suggest"], "5")
	eqDec(t, "frames to make", sugg["FRM"]["suggest"], "5")
	eqDec(t, "labels to buy (20 - 5 → up to 30)", sugg["LBL"]["suggest"], "15")
	eqDec(t, "steel demand from the frame MO", sugg["STL"]["demand"], "10")
	run := o.c.ok("POST", o.p("/mfg/mrp/run"), map[string]any{"warehouse_id": wh}, 201)
	if len(run.body["mos"].([]any)) != 2 || len(run.body["pos"].([]any)) != 1 {
		t.Fatalf("mrp run: %s", run.raw)
	}
	for _, l := range o.c.ok("GET", o.p("/mfg/mrp?warehouse_id="+wh), nil, 200).body["lines"].([]any) {
		if !dec(t, obj(l)["suggest"]).IsZero() {
			t.Fatalf("second MRP still suggests: %v", l)
		}
	}
	mos := map[string]string{}
	for _, m := range o.c.ok("GET", o.p("/mfg/orders?status=open"), nil, 200).list {
		mos[str(obj(m)["item_code"])] = str(obj(m)["id"])
	}

	// ---- frame MO: confirm (no routing), produce with a lot ----
	o.c.ok("POST", o.p("/mfg/orders/"+mos["FRM"]+"/confirm"), map[string]any{}, 200)
	fd := o.c.ok("POST", o.p("/mfg/orders/"+mos["FRM"]+"/produce"), map[string]any{"date": "2026-11-10", "lots": []any{map[string]any{"lot_no": "FR-1", "quantity": "5"}}}, 200)
	eqDec(t, "frame unit cost", obj(fd.body["order"])["unit_cost"], "22")

	// ---- chair MO: confirm → work orders, checks, reservation ----
	cm := mos["CHR"]
	c1 := o.c.ok("POST", o.p("/mfg/orders/"+cm+"/confirm"), map[string]any{}, 200)
	if len(c1.body["workorders"].([]any)) != 2 || len(c1.body["checks"].([]any)) != 2 || str(obj(c1.body["order"])["availability"]) != "ready" {
		t.Fatalf("confirmed chair MO: %s", c1.raw)
	}
	if lines := c1.body["lines"].([]any); len(lines) != 4 { // frame, screw, box, label (kit exploded)
		t.Fatalf("phantom not exploded: %s", c1.raw)
	}
	wos := c1.body["workorders"].([]any)
	wWeld, wAsm := str(obj(wos[0])["id"]), str(obj(wos[1])["id"])
	eqDec(t, "weld planned minutes (5 × 30)", obj(wos[0])["planned_min"], "150")
	if str(obj(wos[0])["status"]) != "ready" || str(obj(wos[1])["status"]) != "pending" {
		t.Fatalf("work order states: %s", c1.raw)
	}
	o.c.ok("POST", o.p("/mfg/workorders/"+wWeld+"/start"), map[string]any{}, 200)
	if r := o.c.do("POST", o.p("/mfg/workorders/"+wWeld+"/finish"), map[string]any{}); r.status != 409 || !strings.Contains(r.raw, "quality_pending") {
		t.Fatalf("finish with a check to do: %d %s", r.status, r.raw)
	}
	wd := o.c.ok("GET", o.p("/mfg/workorders/"+wWeld), nil, 200)
	chk := str(obj(wd.body["checks"].([]any)[0])["id"])
	cr := o.c.ok("POST", o.p("/mfg/quality-checks/"+chk), map[string]any{"measured": "7", "note": "too wide"}, 200)
	if str(cr.body["status"]) != "fail" || cr.body["alert_id"] == nil {
		t.Fatalf("measure out of tolerance: %s", cr.raw)
	}
	if al := o.c.ok("GET", o.p("/mfg/quality-alerts?stage=open"), nil, 200); len(al.list) != 1 {
		t.Fatalf("alerts: %s", al.raw)
	} else {
		o.c.ok("PATCH", o.p("/mfg/quality-alerts/"+str(obj(al.list[0])["id"])), map[string]any{"stage": "done", "action": "re-welded"}, 200)
	}
	f1 := o.c.ok("POST", o.p("/mfg/workorders/"+wWeld+"/finish"), map[string]any{"minutes": "150"}, 200)
	if f1.body["all_done"] != false {
		t.Fatalf("all done too early: %s", f1.raw)
	}
	o.c.ok("POST", o.p("/mfg/workorders/"+wAsm+"/start"), map[string]any{}, 200)
	o.c.ok("POST", o.p("/mfg/workorders/"+wAsm+"/pause"), map[string]any{}, 200)
	o.c.ok("POST", o.p("/mfg/workorders/"+wAsm+"/start"), map[string]any{}, 200)
	if wl := o.c.ok("GET", o.p("/mfg/workorders?workcenter_id="+asm), nil, 200); len(wl.list) != 1 || obj(wl.list[0])["running_since"] == nil {
		t.Fatalf("shop floor list: %s", wl.raw)
	}
	f2 := o.c.ok("POST", o.p("/mfg/workorders/"+wAsm+"/finish"), map[string]any{"minutes": "100"}, 200)
	if f2.body["all_done"] != true {
		t.Fatalf("all done: %s", f2.raw)
	}
	if r := o.c.do("POST", o.p("/mfg/orders/"+cm+"/produce"), map[string]any{}); r.status != 409 || !strings.Contains(r.raw, "quality_pending") {
		t.Fatalf("produce with the final check to do: %d %s", r.status, r.raw)
	}
	for _, c := range o.c.ok("GET", o.p("/mfg/quality-checks?status=todo&order_id="+cm), nil, 200).list {
		o.c.ok("POST", o.p("/mfg/quality-checks/"+str(obj(c)["id"])), map[string]any{"result": "pass"}, 200)
	}
	// ---- partial production: 3 of 5 → backorder of 2 ----
	pd := o.c.ok("POST", o.p("/mfg/orders/"+cm+"/produce"), map[string]any{"date": "2026-11-12", "quantity": "3", "lots": []any{map[string]any{"lot_no": "CH-001", "quantity": "3"}}}, 200)
	po := obj(pd.body["order"])
	// components 3 × (22 + 4 + 1.2) = 81.6, work centers 150/60×60 + 100/60×30 = 200, overhead 3 → 284.6; offcuts 10 % = 28.46;
	// chair 256.14 against standard 3 × 61.38 = 184.14 → variance 72
	eqDec(t, "components", po["components_value"], "81.6")
	eqDec(t, "work center cost", po["ops_cost"], "200")
	eqDec(t, "by-products", po["byproducts_value"], "28.46")
	eqDec(t, "variance", po["variance"], "72")
	eqDec(t, "unit cost = standard", po["unit_cost"], "61.38")
	back := str(pd.body["backorder_id"])
	if back == "" {
		t.Fatalf("no backorder: %s", pd.raw)
	}
	bo := o.c.ok("GET", o.p("/mfg/orders/"+back), nil, 200)
	eqDec(t, "backorder qty", obj(bo.body["order"])["quantity"], "2")
	if str(obj(bo.body["order"])["status"]) != "confirmed" || len(bo.body["workorders"].([]any)) != 2 || len(bo.body["checks"].([]any)) != 2 {
		t.Fatalf("backorder: %s", bo.raw)
	}
	eqDec(t, "backorder weld minutes", obj(bo.body["workorders"].([]any)[0])["planned_min"], "60")
	// scrap on the backorder
	o.c.ok("POST", o.p("/mfg/orders/"+back+"/scrap"), map[string]any{"item_id": screw, "quantity": "1", "reason": "dropped"}, 201)
	if b := o.c.ok("GET", o.p("/mfg/orders/"+back), nil, 200); len(b.body["scraps"].([]any)) != 1 {
		t.Fatalf("order scrap: %s", b.raw)
	}
	for _, c := range o.c.ok("GET", o.p("/mfg/quality-checks?status=todo&order_id="+back), nil, 200).list {
		body := map[string]any{"result": "pass"}
		if str(obj(c)["test_type"]) == "measure" {
			body = map[string]any{"measured": "5"}
		}
		o.c.ok("POST", o.p("/mfg/quality-checks/"+str(obj(c)["id"])), body, 200)
	}
	bd := o.c.ok("POST", o.p("/mfg/orders/"+back+"/produce"), map[string]any{"date": "2026-11-13", "lots": []any{map[string]any{"lot_no": "CH-002", "quantity": "2"}}}, 200)
	eqDec(t, "backorder work center cost (planned 60 + 40 min)", obj(bd.body["order"])["ops_cost"], "80")

	// ---- deliver the chairs (forward trace target) ----
	sd := o.c.ok("GET", o.p("/sales/orders/"+soID), nil, 200)
	o.c.ok("POST", o.p("/sales/orders/"+soID+"/deliver"), map[string]any{"date": "2026-11-14", "lines": []any{
		map[string]any{"line_id": obj(sd.body["lines"].([]any)[0])["id"], "quantity": "5"}}}, 201)

	// ---- subcontracting ----
	sb := o.c.ok("POST", o.p("/mfg/boms"), map[string]any{"item_id": bracket, "bom_type": "subcontract", "subcontractor_id": painter, "subcontract_price": "3",
		"lines": []any{map[string]any{"item_id": steel, "quantity": "1"}}}, 201)
	sm := o.c.ok("POST", o.p("/mfg/orders"), map[string]any{"bom_id": str(obj(sb.body["bom"])["id"]), "quantity": "4", "date": "2026-11-15", "confirm": true}, 201)
	smID := str(obj(sm.body["order"])["id"])
	if r := o.c.do("POST", o.p("/mfg/orders/"+smID+"/produce"), map[string]any{}); r.status != 409 || !strings.Contains(r.raw, "components_not_sent") {
		t.Fatalf("receive before sending: %d %s", r.status, r.raw)
	}
	o.c.ok("POST", o.p("/mfg/orders/"+smID+"/send-components"), map[string]any{"date": "2026-11-15"}, 200)
	if r := o.c.do("POST", o.p("/mfg/orders/"+smID+"/cancel"), map[string]any{}); r.status != 409 {
		t.Fatalf("cancel after sending: %d", r.status)
	}
	sp := o.c.ok("POST", o.p("/mfg/orders/"+smID+"/produce"), map[string]any{"date": "2026-11-16", "supplier_ref": "PC-77"}, 200)
	spo := obj(sp.body["order"])
	eqDec(t, "subcontracted unit cost (10 + 3)", spo["unit_cost"], "13")
	if spo["service_bill_id"] == nil {
		t.Fatalf("no subcontractor bill: %s", sp.raw)
	}
	bill := o.c.ok("GET", o.p("/bills/"+str(spo["service_bill_id"])), nil, 200)
	eqDec(t, "subcontractor bill", obj(bill.body["bill"])["total"], "12")

	// ---- receipt quality check ----
	pur := o.c.ok("POST", o.p("/purchase-orders"), map[string]any{"supplier_id": sup, "currency": "USD", "lines": []any{map[string]any{"item_id": steel, "quantity": "10", "unit_price": "10"}}}, 201)
	purID := str(obj(pur.body["order"])["id"])
	o.c.ok("POST", o.p("/purchase-orders/"+purID+"/confirm"), map[string]any{}, 200)
	rc := o.c.ok("POST", o.p("/purchase-orders/"+purID+"/receive"), map[string]any{}, 201)
	rid := str(obj(rc.body["receipts"].([]any)[0])["id"])
	if q := o.c.ok("GET", o.p("/mfg/quality-checks?receipt_id="+rid), nil, 200); len(q.list) != 1 || str(obj(q.list[0])["point_name"]) != "Steel certificate" {
		t.Fatalf("receipt check: %s", q.raw)
	}

	// ---- traceability ----
	m := o.c.ok("GET", o.p("/mfg/trace?q=CH-001"), nil, 200)
	ch1 := str(obj(m.body["matches"].([]any)[0])["id"])
	tr := o.c.ok("GET", o.p("/mfg/trace?lot_id="+ch1), nil, 200)
	if !strings.Contains(tr.raw, `"FR-1"`) || !strings.Contains(tr.raw, `"S-1"`) || !strings.Contains(tr.raw, "Metal Co") {
		t.Fatalf("backward trace of CH-001 must reach FR-1 and S-1 from Metal Co: %s", tr.raw)
	}
	s1 := str(obj(o.c.ok("GET", o.p("/mfg/trace?q=S-1"), nil, 200).body["matches"].([]any)[0])["id"])
	fw := o.c.ok("GET", o.p("/mfg/trace?lot_id="+s1), nil, 200)
	if !strings.Contains(fw.raw, "CH-001") || !strings.Contains(fw.raw, "Furniture Shop") {
		t.Fatalf("forward trace of S-1 must reach CH-001 and the customer: %s", fw.raw)
	}

	// ---- shortages, reports ----
	big := o.c.ok("POST", o.p("/mfg/orders"), map[string]any{"bom_id": chairBom, "quantity": "50", "confirm": true, "deadline": "2026-11-01"}, 201)
	bigID := str(obj(big.body["order"])["id"])
	if a := str(obj(big.body["order"])["availability"]); a == "ready" {
		t.Fatalf("50 chairs cannot be ready: %s", big.raw)
	}
	sh := o.c.ok("GET", o.p("/mfg/reports/shortages"), nil, 200)
	if len(sh.list) == 0 {
		t.Fatalf("shortages: %s", sh.raw)
	}
	st := o.c.ok("GET", o.p("/mfg/reports/status"), nil, 200)
	if len(st.body["late"].([]any)) != 1 {
		t.Fatalf("late orders: %s", st.raw)
	}
	o.c.ok("POST", o.p("/mfg/orders/"+bigID+"/cancel"), map[string]any{}, 200)
	ld := o.c.ok("GET", o.p("/mfg/reports/load?days=5"), nil, 200)
	if len(ld.list) != 2 {
		t.Fatalf("load: %s", ld.raw)
	}
	va := o.c.ok("GET", o.p("/mfg/reports/variance"), nil, 200)
	found := false
	for _, v := range va.list {
		if str(obj(v)["item_code"]) == "CHR" && dec(t, obj(v)["variance"]).Equal(dec(t, "72")) {
			found = true
		}
	}
	if !found {
		t.Fatalf("variance report: %s", va.raw)
	}

	// ---- books ----
	val := o.c.ok("GET", o.p("/wh/reports/valuation"), nil, 200)
	for _, a := range val.body["accounts"].([]any) {
		eqDec(t, "stock = ledger on "+str(obj(a)["code"]), obj(a)["difference"], "0")
	}
	o.trialBalanced(t)
}
