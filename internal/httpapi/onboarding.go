package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mmdelhajj/paxve/internal/db"
)

// onboarding reports which "getting started" steps a company has completed, detected from its data.
func (s *Server) onboarding(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSONOne(ctx, tx, `
			SELECT EXISTS (SELECT 1 FROM accounts WHERE company_id = c.id) AS chart,
			       (c.base_currency = c.local_currency OR EXISTS (SELECT 1 FROM exchange_rates WHERE company_id = c.id)) AS rate,
			       EXISTS (SELECT 1 FROM plans WHERE company_id = c.id) AS plans,
			       EXISTS (SELECT 1 FROM customers WHERE company_id = c.id) AS customers,
			       EXISTS (SELECT 1 FROM users WHERE role = 'collector' AND is_active) AS collector,
			       EXISTS (SELECT 1 FROM message_providers WHERE company_id = c.id AND is_active) AS messaging,
			       EXISTS (SELECT 1 FROM invoices WHERE company_id = c.id AND period IS NOT NULL) AS billing,
			       t.plan
			FROM companies c JOIN tenants t ON t.id = c.tenant_id WHERE c.id = $1`, r.PathValue("cid"))
	})
}

// callInternal runs one of our own handlers in-process with the caller's identity, so demo data
// goes through exactly the same validation, accounting and audit paths as real user input.
func callInternal(parent *http.Request, h http.HandlerFunc, path map[string]string, body any) (json.RawMessage, error) {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/internal", bytes.NewReader(b)).WithContext(parent.Context())
	req.Header.Set("Content-Type", "application/json")
	for k, v := range path {
		req.SetPathValue(k, v)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code >= 300 {
		return nil, fmt.Errorf("demo step failed (%d): %s", rec.Code, bytes.TrimSpace(rec.Body.Bytes()))
	}
	return rec.Body.Bytes(), nil
}

var demoNames = map[string][]string{
	"en": {"Ahmad Khalil", "Rima Haddad", "Georges Saliba", "Maya Nassar", "Hassan Fakih", "Lina Khoury", "Karim Aoun",
		"Nour Hamdan", "Fadi Abboud", "Rania Itani", "Ziad Mansour", "Hiba Sleiman", "Omar Daher", "Carla Moussa",
		"Bilal Chehab", "Joelle Rizk", "Samir Tannous", "Dana Barakat", "Walid Hajj", "Nadine Karam", "Tarek Ghandour",
		"Sara Hobeika", "Ali Zein", "Yara Frem", "Elie Gemayel", "Mira Daou", "Khaled Sabra", "Tania Azar", "Imad Rahal", "Rola Sfeir"},
	"ar": {"أحمد خليل", "ريما حداد", "جورج صليبا", "مايا نصار", "حسن فقيه", "لينا خوري", "كريم عون", "نور حمدان", "فادي عبود",
		"رانيا عيتاني", "زياد منصور", "هبة سليمان", "عمر ضاهر", "كارلا موسى", "بلال شهاب", "جويل رزق", "سمير طنوس", "دانا بركات",
		"وليد الحاج", "نادين كرم", "طارق غندور", "سارة حبيقة", "علي زين", "يارا فرم", "إيلي الجميل", "ميرا ضو", "خالد صبرا",
		"تانيا عازار", "عماد رحال", "رلى صفير"},
}

// loadDemoData creates a separate company "Demo Company" with a chart of accounts, plans, 30 customers,
// readings, one billing run and some payments, so a trial user can explore everything at once.
// Removing it = deactivating that company (Companies page); real companies are never touched.
func (s *Server) loadDemoData(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	lang := p.Locale
	if _, ok := demoNames[lang]; !ok {
		lang = "en"
	}
	var existing string
	_ = db.InTenant(r.Context(), s.Pool, scope(r), func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `SELECT id::text FROM companies WHERE name_en LIKE 'Demo Company%' AND is_active LIMIT 1`).Scan(&existing)
	})
	if existing != "" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "duplicate", "company_id": existing})
		return
	}
	// "Demo Company", or "Demo Company 2", 3… if earlier (deactivated) demo companies keep the name
	name := "Demo Company"
	_ = db.InTenant(r.Context(), s.Pool, scope(r), func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(r.Context(), `SELECT count(*) FROM companies WHERE name_en LIKE 'Demo Company%'`).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			name = fmt.Sprintf("Demo Company %d", n+1)
		}
		return nil
	})
	raw, err := callInternal(r, s.createCompany, nil, map[string]string{
		"name_en": name, "name_ar": "شركة تجريبية", "base_currency": "USD", "local_currency": "LBP"})
	if err != nil {
		serverErr(w, r, err)
		return
	}
	var comp struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &comp)
	cid := comp.ID
	step := func(h http.HandlerFunc, path map[string]string, body any) json.RawMessage {
		if err != nil {
			return nil
		}
		if path == nil {
			path = map[string]string{}
		}
		path["cid"] = cid
		var out json.RawMessage
		out, err = callInternal(r, h, path, body)
		return out
	}
	now := time.Now()
	period := now.Format("2006-01")
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0).Format(time.DateOnly)

	step(s.loadChartTemplate, nil, map[string]string{"template": "lebanon", "language": lang})
	step(s.upsertRate, nil, map[string]string{"currency": "LBP", "rate": "89500", "date": start})
	areaNames := map[string][]string{"en": {"North quarter", "Main street", "Hill side"}, "ar": {"الحي الشمالي", "الشارع الرئيسي", "حي التلة"}}[lang]
	var areas []string
	for _, a := range areaNames {
		var x struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(step(s.createArea, nil, map[string]string{"name": a}), &x)
		areas = append(areas, x.ID)
	}
	type plan struct {
		ID string `json:"id"`
	}
	var p5, p10, pm plan
	_ = json.Unmarshal(step(s.createPlan, nil, map[string]string{"name": "5A", "billing_type": "fixed", "currency": "USD", "base_fee": "25"}), &p5)
	_ = json.Unmarshal(step(s.createPlan, nil, map[string]string{"name": "10A", "billing_type": "fixed", "currency": "USD", "base_fee": "45"}), &p10)
	_ = json.Unmarshal(step(s.createPlan, nil, map[string]string{"name": "Metered", "billing_type": "metered", "currency": "USD",
		"base_fee": "4.3", "unit_price": "0.54"}), &pm)

	type sub struct{ id, customer string }
	var metered []sub
	var customers []string
	for i, nm := range demoNames[lang] {
		var c struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(step(s.createCustomer, nil, map[string]string{"name": nm, "phone": fmt.Sprintf("03%06d", 100000+i*3137),
			"area_id": areas[(i/3)%len(areas)]}), &c)
		customers = append(customers, c.ID)
		planID := []string{p5.ID, p10.ID, pm.ID}[i%3]
		body := map[string]string{"plan_id": planID, "start_date": start}
		if planID == pm.ID {
			body["meter_no"] = fmt.Sprintf("M-%03d", 100+i)
			body["initial_reading"] = "1000"
		}
		var sb struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(step(s.createSubscription, map[string]string{"id": c.ID}, body), &sb)
		if planID == pm.ID {
			metered = append(metered, sub{sb.ID, c.ID})
		}
	}
	var readings []map[string]string
	for i, m := range metered {
		readings = append(readings, map[string]string{"subscription_id": m.id, "reading": fmt.Sprintf("%d", 1080+i*17)})
	}
	if len(readings) > 0 {
		step(s.saveReadings, nil, map[string]any{"period": period, "readings": readings})
	}
	step(s.runBilling, nil, map[string]string{"period": period, "issue_date": now.Format(time.DateOnly)})

	// payments: about half the customers pay, a few with USD + LBP and change
	var usd, lbp string
	if err == nil {
		err = db.InTenant(r.Context(), s.Pool, scope(r), func(tx pgx.Tx) error {
			return tx.QueryRow(r.Context(), `SELECT
				(SELECT id::text FROM payment_methods WHERE company_id = $1 AND kind = 'cash' AND currency = 'USD' LIMIT 1),
				(SELECT id::text FROM payment_methods WHERE company_id = $1 AND kind = 'cash' AND currency = 'LBP' LIMIT 1)`, cid).Scan(&usd, &lbp)
		})
	}
	for i, c := range customers {
		if i%2 == 1 || err != nil {
			continue
		}
		body := map[string]any{"customer_id": c, "currency": "USD", "tenders": []map[string]string{{"method_id": usd, "amount": "25"}}}
		if i%6 == 0 {
			body["amount"] = "25"
			body["tenders"] = []map[string]string{{"method_id": usd, "amount": "20"}, {"method_id": lbp, "amount": "500000"}}
			body["change_method_id"] = lbp
		}
		step(s.createPayment, nil, body)
	}
	if err != nil {
		serverErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"company_id": cid, "name": name, "customers": len(customers)})
}
