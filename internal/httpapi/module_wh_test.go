package httpapi

import (
	"strings"
	"testing"
)

// Warehouse pro (phase 1): bins and putaway rules, 3-step receipt (input → QC → stock), 3-step delivery
// (pick → pack → ship) with a backorder, packages, batch picking, bins following stock out (trigger), counts by bin,
// scanner lookup, dropship (auto delivery when the supplier bills), reports tie to the ledger, delivery trip + POD.

func whBins(t *testing.T, o *org, wh string) map[string]string {
	t.Helper()
	out := map[string]string{}
	r := o.c.ok("GET", o.p("/wh/bin-stock?warehouse_id="+wh), nil, 200)
	for _, b := range r.body["bins"].([]any) {
		x := obj(b)
		out[str(x["location_code"])+"/"+str(x["item_code"])] = str(x["quantity"])
	}
	return out
}

func whOps(t *testing.T, o *org, q string) []map[string]any {
	t.Helper()
	out := []map[string]any{}
	for _, x := range o.c.ok("GET", o.p("/wh/ops?"+q), nil, 200).list {
		out = append(out, obj(x))
	}
	return out
}

func TestWarehouseProFlow(t *testing.T) {
	needDB(t)
	o := signup(t, "lebanon")
	sup := str(o.c.ok("POST", o.p("/suppliers"), map[string]any{"name": "Gear Co", "currency": "USD"}, 201).body["id"])
	itA := str(o.c.ok("POST", o.p("/items"), map[string]any{"code": "RTR", "name": "Router", "kind": "stock", "unit": "pc", "sale_price": "20"}, 201).body["id"])
	itB := str(o.c.ok("POST", o.p("/items"), map[string]any{"code": "CBL", "name": "Cable", "kind": "stock", "unit": "pc", "sale_price": "5"}, 201).body["id"])
	whs := o.c.ok("GET", o.p("/wh/warehouses"), nil, 200).list
	wh := str(obj(whs[0])["id"])

	// steps need bins
	if r := o.c.do("PATCH", o.p("/wh/warehouses/"+wh), map[string]any{"reception_steps": 3}); r.status != 400 {
		t.Fatalf("steps without bins: %d %s", r.status, r.raw)
	}
	o.c.ok("PATCH", o.p("/wh/warehouses/"+wh), map[string]any{"use_bins": true, "reception_steps": 3, "delivery_steps": 3, "removal": "fifo"}, 200)
	g := o.c.ok("POST", o.p("/wh/locations/generate"), map[string]any{"warehouse_id": wh, "zone": "A", "aisles": 1, "shelves": 2, "bins": 2}, 201)
	eqDec(t, "locations generated", g.body["created"], "7")
	locs := map[string]string{}
	for _, l := range o.c.ok("GET", o.p("/wh/locations?warehouse_id="+wh), nil, 200).list {
		locs[str(obj(l)["code"])] = str(obj(l)["id"])
	}
	for _, k := range []string{"INPUT", "QC", "OUTPUT", "PACK", "A-01-1-01", "A-01-2-02"} {
		if locs[k] == "" {
			t.Fatalf("location %s missing: %v", k, locs)
		}
	}
	o.c.ok("POST", o.p("/wh/putaway-rules"), map[string]any{"warehouse_id": wh, "item_id": itA, "location_id": locs["A-01-1-01"]}, 201)

	// ---- 3-step receipt: GR → (INPUT) → QC → putaway ----
	po := o.c.ok("POST", o.p("/purchase-orders"), map[string]any{"supplier_id": sup, "currency": "USD", "order_date": "2026-11-10",
		"lines": []any{map[string]any{"item_id": itA, "quantity": "10", "unit_price": "12"}, map[string]any{"item_id": itB, "quantity": "4", "unit_price": "1"}}}, 201)
	poID := str(obj(po.body["order"])["id"])
	o.c.ok("POST", o.p("/purchase-orders/"+poID+"/confirm"), map[string]any{}, 200)
	o.c.ok("POST", o.p("/purchase-orders/"+poID+"/receive"), map[string]any{}, 201)
	if b := whBins(t, o, wh); b["INPUT/RTR"] != "10" || b["INPUT/CBL"] != "4" {
		t.Fatalf("goods in INPUT: %v", b)
	}
	qc := whOps(t, o, "kind=qc")
	if len(qc) != 1 {
		t.Fatalf("qc ops: %v", qc)
	}
	d := o.c.ok("POST", o.p("/wh/ops/"+str(qc[0]["id"])+"/done"), map[string]any{}, 200)
	put := str(obj(d.body["next"])["next_op_id"])
	pd := o.c.ok("GET", o.p("/wh/ops/"+put), nil, 200)
	lines := []any{}
	for _, l := range pd.body["lines"].([]any) {
		x := obj(l)
		if str(x["item_code"]) == "RTR" && str(x["to_code"]) != "A-01-1-01" {
			t.Fatalf("putaway rule not used: %s", pd.raw)
		}
		ln := map[string]any{"line_id": x["id"], "done_qty": x["quantity"]}
		if str(x["item_code"]) == "CBL" {
			ln["to_location_id"] = locs["A-01-2-02"]
		}
		lines = append(lines, ln)
	}
	o.c.ok("POST", o.p("/wh/ops/"+put+"/done"), map[string]any{"lines": lines}, 200)
	if b := whBins(t, o, wh); b["A-01-1-01/RTR"] != "10" || b["A-01-2-02/CBL"] != "4" || b["QC/RTR"] != "" || b["INPUT/RTR"] != "" {
		t.Fatalf("after putaway: %v", b)
	}
	// stock comes in with the bill (money); bins already hold the goods
	o.c.ok("POST", o.p("/purchase-orders/"+poID+"/bill"), map[string]any{"bill_date": "2026-11-12", "supplier_ref": "G-1", "confirm": true}, 201)

	// ---- 3-step delivery: pick (partial → backorder) → pack (into a package) → ship ----
	cust := o.customer(t, "Shop Customer", "")
	so := o.c.ok("POST", o.p("/sales/orders"), map[string]any{"customer_id": cust, "currency": "USD", "lines": []any{
		map[string]any{"item_id": itA, "description": "Router", "quantity": "3", "unit_price": "20"},
		map[string]any{"item_id": itB, "description": "Cable", "quantity": "1", "unit_price": "5"}}}, 201)
	soID := str(obj(so.body["order"])["id"])
	o.c.ok("POST", o.p("/sales/orders/"+soID+"/confirm"), map[string]any{}, 200)
	if r := o.c.do("POST", o.p("/sales/orders/"+soID+"/deliver"), map[string]any{}); r.status != 409 || !strings.Contains(r.raw, "use_warehouse_steps") {
		t.Fatalf("direct delivery not refused: %d %s", r.status, r.raw)
	}
	picks := whOps(t, o, "kind=pick&order_id="+soID)
	if len(picks) != 1 {
		t.Fatalf("pick ops: %v", picks)
	}
	pk := o.c.ok("GET", o.p("/wh/ops/"+str(picks[0]["id"])), nil, 200)
	plines := []any{}
	for _, l := range pk.body["lines"].([]any) {
		x := obj(l)
		q := str(x["quantity"])
		if str(x["item_code"]) == "RTR" {
			if str(x["from_code"]) != "A-01-1-01" {
				t.Fatalf("pick from: %s", pk.raw)
			}
			q = "2"
		}
		plines = append(plines, map[string]any{"line_id": x["id"], "done_qty": q})
	}
	pdone := o.c.ok("POST", o.p("/wh/ops/"+str(picks[0]["id"])+"/done"), map[string]any{"lines": plines}, 200)
	back := str(obj(pdone.body["next"])["backorder_id"])
	pack := str(obj(pdone.body["next"])["next_op_id"])
	if back == "" || pack == "" {
		t.Fatalf("backorder / pack: %s", pdone.raw)
	}
	if b := whBins(t, o, wh); b["PACK/RTR"] != "2" || b["A-01-1-01/RTR"] != "8" {
		t.Fatalf("after pick: %v", b)
	}
	pkg := o.c.ok("POST", o.p("/wh/packages"), map[string]any{"warehouse_id": wh, "kind": "box", "sales_order_id": soID, "weight": "3.5"}, 201)
	pkgID := str(obj(pkg.body["package"])["id"])
	pl := o.c.ok("GET", o.p("/wh/ops/"+pack), nil, 200)
	packLines := []any{}
	for _, l := range pl.body["lines"].([]any) {
		packLines = append(packLines, map[string]any{"line_id": obj(l)["id"], "done_qty": obj(l)["quantity"], "package_id": pkgID})
	}
	pd2 := o.c.ok("POST", o.p("/wh/ops/"+pack+"/done"), map[string]any{"lines": packLines}, 200)
	ship := str(obj(pd2.body["next"])["next_op_id"])
	if c := o.c.ok("GET", o.p("/wh/packages/"+pkgID), nil, 200); len(c.body["lines"].([]any)) != 2 {
		t.Fatalf("package content: %s", c.raw)
	}
	sd := o.c.ok("POST", o.p("/wh/ops/"+ship+"/done"), map[string]any{}, 200)
	if str(obj(sd.body["next"])["delivery_id"]) == "" {
		t.Fatalf("ship made no delivery: %s", sd.raw)
	}
	if b := whBins(t, o, wh); b["OUTPUT/RTR"] != "" || b["A-01-1-01/RTR"] != "8" || b["A-01-2-02/CBL"] != "3" {
		t.Fatalf("bins after ship (stock out takes OUTPUT first): %v", b)
	}
	if st := str(obj(o.c.ok("GET", o.p("/wh/packages/"+pkgID), nil, 200).body["package"])["status"]); st != "shipped" {
		t.Fatalf("package status: %s", st)
	}

	// ---- batch picking: the backorder pick in a batch ----
	bt := o.c.ok("POST", o.p("/wh/batches"), map[string]any{"warehouse_id": wh, "op_ids": []string{back}}, 201)
	bid := str(obj(bt.body["batch"])["id"])
	if n := len(bt.body["lines"].([]any)); n != 1 {
		t.Fatalf("batch lines: %s", bt.raw)
	}
	bd := o.c.ok("POST", o.p("/wh/batches/"+bid+"/done"), map[string]any{}, 200)
	if str(obj(bd.body["batch"])["status"]) != "done" {
		t.Fatalf("batch: %s", bd.raw)
	}
	// the rest of the chain, then the order is fully delivered
	for _, k := range []string{"pack", "ship"} {
		ops := whOps(t, o, "kind="+k+"&order_id="+soID)
		if len(ops) != 1 {
			t.Fatalf("%s after batch: %v", k, ops)
		}
		o.c.ok("POST", o.p("/wh/ops/"+str(ops[0]["id"])+"/done"), map[string]any{}, 200)
	}
	ord := o.c.ok("GET", o.p("/sales/orders/"+soID), nil, 200)
	for _, l := range ord.body["lines"].([]any) {
		x := obj(l)
		eqDec(t, "delivered "+str(x["description"]), x["qty_delivered"], str(x["quantity"]))
	}
	if b := whBins(t, o, wh); b["A-01-1-01/RTR"] != "7" {
		t.Fatalf("bins after the backorder: %v", b)
	}

	// ---- bins follow stock that leaves by other flows (an invoice with stock): unbinned first, then bins ----
	o.c.ok("PATCH", o.p("/wh/warehouses/"+wh), map[string]any{"delivery_steps": 1}, 200)
	so2 := o.c.ok("POST", o.p("/sales/orders"), map[string]any{"customer_id": cust, "currency": "USD", "lines": []any{
		map[string]any{"item_id": itA, "description": "Router", "quantity": "2", "unit_price": "20"}}}, 201)
	so2ID := str(obj(so2.body["order"])["id"])
	o.c.ok("POST", o.p("/sales/orders/"+so2ID+"/confirm"), map[string]any{}, 200)
	o.c.ok("POST", o.p("/sales/orders/"+so2ID+"/deliver"), map[string]any{}, 201)
	if b := whBins(t, o, wh); b["A-01-1-01/RTR"] != "5" {
		t.Fatalf("trigger: bins after a direct delivery: %v", b)
	}

	// ---- count by bin, scan, move ----
	cnt := o.c.ok("POST", o.p("/wh/count"), map[string]any{"warehouse_id": wh, "location_id": locs["A-01-1-01"],
		"lines": []any{map[string]any{"item_id": itA, "counted": "4"}}}, 200)
	if cnt.body["op_id"] == nil {
		t.Fatalf("count: %s", cnt.raw)
	}
	bs := o.c.ok("GET", o.p("/wh/bin-stock?warehouse_id="+wh+"&item_id="+itA), nil, 200)
	if u := bs.body["unbinned"].([]any); len(u) != 1 || str(obj(u[0])["quantity"]) != "1" {
		t.Fatalf("unbinned after count: %s", bs.raw)
	}
	if sc := o.c.ok("GET", o.p("/wh/scan?code=A-01-1-01"), nil, 200); str(sc.body["type"]) != "location" {
		t.Fatalf("scan location: %s", sc.raw)
	}
	if sc := o.c.ok("GET", o.p("/wh/scan?code=RTR"), nil, 200); str(sc.body["type"]) != "item" {
		t.Fatalf("scan item: %s", sc.raw)
	}
	o.c.ok("POST", o.p("/wh/move"), map[string]any{"warehouse_id": wh, "item_id": itA, "quantity": "1", "from_location_id": locs["A-01-1-01"],
		"to_location_id": locs["A-01-2-01"]}, 201)
	if r := o.c.do("POST", o.p("/wh/move"), map[string]any{"warehouse_id": wh, "item_id": itA, "quantity": "50", "from_location_id": locs["A-01-1-01"],
		"to_location_id": locs["A-01-2-01"]}); r.status != 422 || !strings.Contains(r.raw, "not_enough_in_bin") {
		t.Fatalf("over-move: %d %s", r.status, r.raw)
	}
	if b := whBins(t, o, wh); b["A-01-1-01/RTR"] != "3" || b["A-01-2-01/RTR"] != "1" {
		t.Fatalf("after move: %v", b)
	}

	// ---- dropship: SO → PO to the supplier → supplier bill → the order is delivered from the dropship warehouse ----
	so3 := o.c.ok("POST", o.p("/sales/orders"), map[string]any{"customer_id": cust, "currency": "USD", "lines": []any{
		map[string]any{"item_id": itA, "description": "Router", "quantity": "6", "unit_price": "21"}}}, 201)
	so3ID := str(obj(so3.body["order"])["id"])
	o.c.ok("POST", o.p("/sales/orders/"+so3ID+"/confirm"), map[string]any{}, 200)
	so3Line := str(obj(so3.body["lines"].([]any)[0])["id"])
	ds := o.c.ok("POST", o.p("/wh/dropship"), map[string]any{"order_id": so3ID, "supplier_id": sup,
		"lines": []any{map[string]any{"line_id": so3Line, "quantity": "6", "unit_price": "11"}}}, 201)
	dpo := str(obj(ds.body["order"])["id"])
	eqDec(t, "dropship PO total", obj(ds.body["order"])["total"], "66")
	o.c.ok("POST", o.p("/purchase-orders/"+dpo+"/confirm"), map[string]any{}, 200)
	o.c.ok("POST", o.p("/purchase-orders/"+dpo+"/bill"), map[string]any{"bill_date": "2026-11-16", "supplier_ref": "DS-1", "confirm": true}, 201)
	for _, l := range o.c.ok("GET", o.p("/sales/orders/"+so3ID), nil, 200).body["lines"].([]any) {
		eqDec(t, "dropship delivered on the supplier bill", obj(l)["qty_delivered"], "6")
	}
	if b := whBins(t, o, wh); b["A-01-1-01/RTR"] != "3" {
		t.Fatalf("dropship touched the main warehouse bins: %v", b)
	}

	// ---- reports ----
	val := o.c.ok("GET", o.p("/wh/reports/valuation"), nil, 200)
	for _, a := range val.body["accounts"].([]any) {
		eqDec(t, "stock = ledger on "+str(obj(a)["code"]), obj(a)["difference"], "0")
	}
	for _, p := range []string{"/wh/reports/slow?days=1", "/wh/reports/abc", "/wh/reports/ageing"} {
		o.c.ok("GET", o.p(p), nil, 200)
	}
	abc := o.c.ok("GET", o.p("/wh/reports/abc"), nil, 200)
	if len(abc.list) == 0 || str(obj(abc.list[0])["class"]) != "A" {
		t.Fatalf("abc: %s", abc.raw)
	}

	// ---- delivery board: a trip with the delivery, proof of delivery from the driver ----
	veh := str(o.c.ok("POST", o.p("/wh/vehicles"), map[string]any{"name": "Van 1", "plate": "B 123456", "capacity_kg": "800"}, 201).body["id"])
	todo := o.c.ok("GET", o.p("/wh/to-schedule"), nil, 200)
	dels := todo.body["deliveries"].([]any)
	if len(dels) == 0 {
		t.Fatalf("to schedule: %s", todo.raw)
	}
	driver, driverID := newStaff(t, o, "collector")
	trip := o.c.ok("POST", o.p("/wh/trips"), map[string]any{"vehicle_id": veh, "driver_id": driverID,
		"stops": []any{map[string]any{"sales_delivery_id": obj(dels[0])["id"]}}}, 201)
	tid := str(obj(trip.body["trip"])["id"])
	stop := str(obj(trip.body["stops"].([]any)[0])["id"])
	mine := driver.ok("GET", o.p("/wh/trips?mine=1"), nil, 200)
	if len(mine.list) != 1 {
		t.Fatalf("driver's trips: %s", mine.raw)
	}
	if r := driver.do("GET", o.p("/wh/trips"), nil); r.status != 403 {
		t.Fatalf("driver sees every trip: %d", r.status)
	}
	if r := driver.do("POST", o.p("/wh/stops/"+stop+"/pod"), map[string]any{"status": "delivered"}); r.status != 400 {
		t.Fatalf("POD without name or signature: %d %s", r.status, r.raw)
	}
	pod := driver.ok("POST", o.p("/wh/stops/"+stop+"/pod"), map[string]any{"status": "delivered", "name": "Rami",
		"signature": "data:image/png;base64,iVBORw0KGgo=", "lat": 33.89, "lng": 35.5}, 200)
	if str(obj(pod.body["trip"])["status"]) != "done" {
		t.Fatalf("trip after the last stop: %s", pod.raw)
	}
	driver.ok("POST", o.p("/wh/stops/"+stop+"/photo"), map[string]any{"filename": "door.png", "data": "data:image/png;base64,iVBORw0KGgo="}, 201)
	if r := driver.do("POST", o.p("/wh/stops/"+stop+"/photo"), map[string]any{"data": "data:text/plain;base64,aGVsbG8="}); r.status != 400 {
		t.Fatalf("a non-image as delivery photo: %d", r.status)
	}
	if n := o.c.ok("GET", o.p("/wh/trips/"+tid), nil, 200).body["stops"].([]any); str(obj(n[0])["photos"]) != "1" {
		t.Fatalf("photo count: %v", n)
	}
	o.trialBalanced(t)
}
