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

// Fleet (app "flt"): the vehicles of the company (the same vehicles the delivery trips use) with their papers
// (licence, insurance, inspection expiry), fuel logs (litres per 100 km between full tanks, flags for a jump in
// consumption or an odometer going back), service plans by km and / or days with the service log, and every cost
// posted as a normal expense (fuel, service, or any other expense charged to the vehicle).

var vehicleKinds = []string{"car", "van", "truck", "motorbike", "bus", "machine", "other"}
var fuelTypes = []string{"diesel", "gasoline", "electric", "hybrid", "lpg", "other"}

const fleetVehicleCols = `v.id, v.name, v.plate, v.kind, v.make, v.model, v.year, v.vin, v.fuel_type, trim_scale(v.odometer)::text AS odometer,
	trim_scale(v.fuel_norm)::text AS fuel_norm, v.purchase_date, v.license_expiry, v.insurance_expiry, v.inspection_expiry,
	trim_scale(v.capacity_kg)::text AS capacity_kg, v.driver_id, u.full_name AS driver_name, v.cost_center_id, v.is_active, v.notes,
	(SELECT trim_scale(round(avg(f.consumption), 2))::text FROM (SELECT consumption FROM fleet_fuel_logs f WHERE f.vehicle_id = v.id AND f.status = 'posted'
	   AND f.consumption IS NOT NULL ORDER BY f.odometer DESC LIMIT 5) f) AS avg_consumption,
	(SELECT max(f.log_date) FROM fleet_fuel_logs f WHERE f.vehicle_id = v.id AND f.status = 'posted') AS last_fuel,
	least(v.license_expiry, v.insurance_expiry, v.inspection_expiry) - current_date AS min_doc_days,
	(SELECT count(*) FROM fleet_service_plans sp WHERE sp.vehicle_id = v.id AND sp.is_active AND
	   ((sp.every_km IS NOT NULL AND v.odometer >= coalesce(sp.last_km, 0) + sp.every_km)
	    OR (sp.every_days IS NOT NULL AND current_date >= coalesce(sp.last_date, sp.created_at::date) + sp.every_days))) AS services_due,
	(SELECT count(*) FROM fleet_fuel_logs f WHERE f.vehicle_id = v.id AND f.status = 'posted' AND f.anomaly <> '' AND f.log_date > current_date - 60) AS anomalies`

const fleetVehicleFrom = ` FROM vehicles v LEFT JOIN users u ON u.id = v.driver_id`

func (s *Server) fleetVehicles(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+fleetVehicleCols+fleetVehicleFrom+` WHERE v.company_id = $1 ORDER BY v.is_active DESC, v.name`, r.PathValue("cid"))
	})
}

type fleetVehicleIn struct {
	Name             *string `json:"name"`
	Plate            *string `json:"plate"`
	Kind             *string `json:"kind"`
	Make             *string `json:"make"`
	Model            *string `json:"model"`
	Year             *int    `json:"year"`
	VIN              *string `json:"vin"`
	FuelType         *string `json:"fuel_type"`
	Odometer         *string `json:"odometer"`
	FuelNorm         *string `json:"fuel_norm"`
	PurchaseDate     *string `json:"purchase_date"`
	LicenseExpiry    *string `json:"license_expiry"`
	InsuranceExpiry  *string `json:"insurance_expiry"`
	InspectionExpiry *string `json:"inspection_expiry"`
	CapacityKg       *string `json:"capacity_kg"`
	DriverID         *string `json:"driver_id"`
	CostCenterID     *string `json:"cost_center_id"`
	IsActive         *bool   `json:"is_active"`
	Notes            *string `json:"notes"`
}

func (in *fleetVehicleIn) valid() bool {
	for _, p := range []*string{in.PurchaseDate, in.LicenseExpiry, in.InsuranceExpiry, in.InspectionExpiry} {
		if p != nil && *p != "" && !validDate(*p) {
			return false
		}
	}
	for _, p := range []*string{in.Odometer, in.FuelNorm, in.CapacityKg} {
		if p != nil && *p != "" {
			if d, ok := money(*p); !ok || d.GreaterThan(decimal.NewFromInt(100000000)) {
				return false
			}
		}
	}
	return (in.Kind == nil || contains(vehicleKinds, *in.Kind)) && (in.FuelType == nil || contains(fuelTypes, *in.FuelType)) &&
		(in.Year == nil || (*in.Year >= 1950 && *in.Year <= 2100)) && optUUID(in.DriverID) && optUUID(in.CostCenterID) &&
		len(deref(in.Name)) <= 120 && len(deref(in.Plate)) <= 30 && len(deref(in.Make)) <= 60 && len(deref(in.Model)) <= 60 && len(deref(in.VIN)) <= 40 &&
		len(deref(in.Notes)) <= 1000
}

// POST /fleet/vehicles and PATCH /fleet/vehicles/{id}
func (s *Server) fleetSaveVehicle(w http.ResponseWriter, r *http.Request) {
	var in fleetVehicleIn
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
		if d := deref(in.DriverID); d != "" {
			if err := checkCompanyUser(ctx, tx, cid, d); err != nil {
				return nil, err
			}
		}
		if cc := deref(in.CostCenterID); cc != "" {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cost_centers WHERE id = $1 AND company_id = $2)`, cc, cid).Scan(&ok); err != nil {
				return nil, err
			}
			if !ok {
				return nil, badRequest("invalid_cost_center")
			}
		}
		if creating {
			if err := tx.QueryRow(ctx, `INSERT INTO vehicles (company_id, name, plate, notes) VALUES ($1, trim($2), '', '') RETURNING id::text`, cid, *in.Name).Scan(&id); err != nil {
				if isUniqueViolation(err) {
					return nil, &apiError{status: http.StatusConflict, code: "duplicate"}
				}
				return nil, err
			}
		}
		tag, err := tx.Exec(ctx, `UPDATE vehicles SET name = coalesce(trim($3), name), plate = coalesce(trim($4), plate), kind = coalesce($5, kind),
			make = coalesce(trim($6), make), model = coalesce(trim($7), model), year = CASE WHEN $8::int IS NULL THEN year ELSE $8 END,
			vin = coalesce(trim($9), vin), fuel_type = coalesce($10, fuel_type), odometer = coalesce(nullif($11, '')::numeric, odometer),
			fuel_norm = coalesce(nullif($12, '')::numeric, fuel_norm),
			purchase_date = CASE WHEN $13::text IS NULL THEN purchase_date ELSE nullif($13, '')::date END,
			license_expiry = CASE WHEN $14::text IS NULL THEN license_expiry ELSE nullif($14, '')::date END,
			insurance_expiry = CASE WHEN $15::text IS NULL THEN insurance_expiry ELSE nullif($15, '')::date END,
			inspection_expiry = CASE WHEN $16::text IS NULL THEN inspection_expiry ELSE nullif($16, '')::date END,
			capacity_kg = coalesce(nullif($17, '')::numeric, capacity_kg),
			driver_id = CASE WHEN $18::text IS NULL THEN driver_id ELSE nullif($18, '')::uuid END,
			cost_center_id = CASE WHEN $19::text IS NULL THEN cost_center_id ELSE nullif($19, '')::uuid END,
			is_active = coalesce($20, is_active), notes = coalesce($21, notes)
			WHERE id = $1 AND company_id = $2`, id, cid, in.Name, in.Plate, in.Kind, in.Make, in.Model, in.Year, in.VIN, in.FuelType, in.Odometer, in.FuelNorm,
			in.PurchaseDate, in.LicenseExpiry, in.InsuranceExpiry, in.InspectionExpiry, in.CapacityKg, in.DriverID, in.CostCenterID, in.IsActive, in.Notes)
		if isUniqueViolation(err) {
			return nil, &apiError{status: http.StatusConflict, code: "duplicate"}
		}
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return queryJSONOne(ctx, tx, `SELECT `+fleetVehicleCols+fleetVehicleFrom+` WHERE v.id = $1`, id)
	})
}

const fuelCols = `f.id, f.vehicle_id, v.name AS vehicle_name, v.plate, f.log_date, trim_scale(f.odometer)::text AS odometer, trim_scale(f.liters)::text AS liters,
	f.amount::text AS amount, f.currency, f.full_tank, f.station, f.driver_id, u.full_name AS driver_name, f.notes, trim_scale(f.consumption)::text AS consumption,
	f.anomaly, f.expense_id, e.number AS expense_number, f.status, f.created_at`

const fuelFrom = ` FROM fleet_fuel_logs f JOIN vehicles v ON v.id = f.vehicle_id LEFT JOIN users u ON u.id = f.driver_id LEFT JOIN expenses e ON e.id = f.expense_id`

const serviceLogCols = `l.id, l.vehicle_id, v.name AS vehicle_name, l.plan_id, l.done_date, trim_scale(l.odometer)::text AS odometer, l.task,
	l.amount::text AS amount, l.currency, l.vendor, l.notes, l.expense_id, e.number AS expense_number, l.status, l.created_at`

const serviceLogFrom = ` FROM fleet_service_log l JOIN vehicles v ON v.id = l.vehicle_id LEFT JOIN expenses e ON e.id = l.expense_id`

const servicePlanCols = `sp.id, sp.vehicle_id, v.name AS vehicle_name, sp.task, trim_scale(sp.every_km)::text AS every_km, sp.every_days,
	trim_scale(sp.last_km)::text AS last_km, sp.last_date, sp.is_active,
	CASE WHEN sp.every_km IS NOT NULL THEN trim_scale(coalesce(sp.last_km, 0) + sp.every_km)::text END AS due_km,
	CASE WHEN sp.every_days IS NOT NULL THEN coalesce(sp.last_date, sp.created_at::date) + sp.every_days END AS due_date,
	CASE WHEN NOT sp.is_active THEN 'off'
	     WHEN (sp.every_km IS NOT NULL AND v.odometer >= coalesce(sp.last_km, 0) + sp.every_km)
	       OR (sp.every_days IS NOT NULL AND current_date >= coalesce(sp.last_date, sp.created_at::date) + sp.every_days) THEN 'due'
	     WHEN (sp.every_km IS NOT NULL AND v.odometer >= coalesce(sp.last_km, 0) + sp.every_km * 0.9)
	       OR (sp.every_days IS NOT NULL AND current_date >= coalesce(sp.last_date, sp.created_at::date) + sp.every_days - 14) THEN 'soon'
	     ELSE 'ok' END AS state`

// GET /fleet/vehicles/{id}: the vehicle, its fuel, service plans and log, and costs this year.
func (s *Server) fleetGetVehicle(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		v, err := queryJSONOne(ctx, tx, `SELECT `+fleetVehicleCols+fleetVehicleFrom+` WHERE v.id = $1 AND v.company_id = $2`, id, cid)
		if err != nil {
			return nil, err
		}
		fuel, err := queryJSON(ctx, tx, `SELECT `+fuelCols+fuelFrom+` WHERE f.vehicle_id = $1 ORDER BY f.odometer DESC, f.created_at DESC LIMIT 200`, id)
		if err != nil {
			return nil, err
		}
		plans, err := queryJSON(ctx, tx, `SELECT `+servicePlanCols+` FROM fleet_service_plans sp JOIN vehicles v ON v.id = sp.vehicle_id WHERE sp.vehicle_id = $1 ORDER BY sp.task`, id)
		if err != nil {
			return nil, err
		}
		log, err := queryJSON(ctx, tx, `SELECT `+serviceLogCols+serviceLogFrom+` WHERE l.vehicle_id = $1 ORDER BY l.done_date DESC, l.created_at DESC LIMIT 200`, id)
		if err != nil {
			return nil, err
		}
		costs, err := fleetCosts(ctx, tx, cid, id, time.Now().Format("2006")+"-01-01", today())
		if err != nil {
			return nil, err
		}
		var c json.RawMessage = []byte("null")
		if len(costs) > 0 {
			c, _ = json.Marshal(costs[0])
		}
		trips, err := queryJSON(ctx, tx, `SELECT t.id, t.number, t.trip_date, t.status, (SELECT count(*) FROM trip_stops s WHERE s.trip_id = t.id) AS stops
			FROM delivery_trips t WHERE t.vehicle_id = $1 ORDER BY t.trip_date DESC LIMIT 20`, id)
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"vehicle": v, "fuel": fuel, "plans": plans, "log": log, "costs": c, "trips": trips}, nil
	})
}

// fleetExpense posts the cost of a fuel fill / service as an expense when a payment method is given.
func fleetExpense(ctx context.Context, tx pgx.Tx, cid, which, account, method, date, amount, desc string) (*string, string, error) {
	if method == "" {
		return nil, "", nil
	}
	if account == "" {
		col := map[string]string{"fuel": "fleet_fuel_account_id", "service": "fleet_service_account_id"}[which]
		var a *string
		if err := tx.QueryRow(ctx, `SELECT `+col+`::text FROM companies WHERE id = $1`, cid).Scan(&a); err != nil {
			return nil, "", err
		}
		if a == nil {
			return nil, "", &apiError{status: http.StatusUnprocessableEntity, code: "fleet_not_setup", detail: which}
		}
		account = *a
	}
	if err := rejectControlAccount(ctx, tx, cid, account); err != nil {
		return nil, "", err
	}
	id, err := createExpenseTx(ctx, tx, cid, expenseIn{Date: date, AccountID: account, MethodID: method, Amount: amount, Description: desc})
	if err != nil {
		return nil, "", err
	}
	var cur string
	if err := tx.QueryRow(ctx, `SELECT currency::text FROM expenses WHERE id = $1`, id).Scan(&cur); err != nil {
		return nil, "", err
	}
	return &id, cur, nil
}

// GET /fleet/fuel?vehicle_id=&from=&to=&anomalies=1
func (s *Server) fleetFuel(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	veh, from, to := q.Get("vehicle_id"), q.Get("from"), q.Get("to")
	if from == "" {
		from = "2000-01-01"
	}
	if to == "" {
		to = "2100-01-01"
	}
	if (veh != "" && !isUUID(veh)) || !validDate(from) || !validDate(to) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+fuelCols+fuelFrom+` WHERE f.company_id = $1 AND ($2 = '' OR f.vehicle_id = nullif($2, '')::uuid)
			AND f.log_date BETWEEN $3::date AND $4::date AND (NOT $5 OR (f.anomaly <> '' AND f.status = 'posted'))
			ORDER BY f.log_date DESC, f.odometer DESC LIMIT 2000`, r.PathValue("cid"), veh, from, to, q.Get("anomalies") == "1")
	})
}

// POST /fleet/fuel {vehicle_id, date, odometer, liters, amount, full_tank, station, driver_id, notes, method_id, account_id, currency}
func (s *Server) fleetAddFuel(w http.ResponseWriter, r *http.Request) {
	var in struct {
		VehicleID string `json:"vehicle_id"`
		Date      string `json:"date"`
		Odometer  string `json:"odometer"`
		Liters    string `json:"liters"`
		Amount    string `json:"amount"`
		Currency  string `json:"currency"`
		FullTank  *bool  `json:"full_tank"`
		Station   string `json:"station"`
		DriverID  string `json:"driver_id"`
		Notes     string `json:"notes"`
		MethodID  string `json:"method_id"`
		AccountID string `json:"account_id"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Date == "" {
		in.Date = today()
	}
	odo, ok1 := money(in.Odometer)
	lit, ok2 := money(in.Liters)
	amt := decimal.Zero
	if strings.TrimSpace(in.Amount) != "" {
		a, ok := money(in.Amount)
		if !ok {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
		amt = a
	}
	if !isUUID(in.VehicleID) || !validDate(in.Date) || !ok1 || !ok2 || !lit.IsPositive() || lit.GreaterThan(decimal.NewFromInt(100000)) ||
		(in.DriverID != "" && !isUUID(in.DriverID)) || (in.MethodID != "" && !isUUID(in.MethodID)) || (in.AccountID != "" && !isUUID(in.AccountID)) ||
		(in.Currency != "" && !currencyRe.MatchString(in.Currency)) || len(in.Station) > 120 || len(in.Notes) > 1000 || (in.MethodID != "" && !amt.IsPositive()) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	full := true
	if in.FullTank != nil {
		full = *in.FullTank
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var vname, norm, maxOdo, base string
		err := tx.QueryRow(ctx, `SELECT v.name, v.fuel_norm::text, greatest(v.odometer, coalesce((SELECT max(odometer) FROM fleet_fuel_logs f WHERE f.vehicle_id = v.id AND f.status = 'posted'), 0))::text,
			co.base_currency::text FROM vehicles v JOIN companies co ON co.id = v.company_id WHERE v.id = $1 AND v.company_id = $2 FOR UPDATE OF v`,
			in.VehicleID, cid).Scan(&vname, &norm, &maxOdo, &base)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, badRequest("invalid_vehicle")
		}
		if err != nil {
			return nil, err
		}
		if in.DriverID != "" {
			if err := checkCompanyUser(ctx, tx, cid, in.DriverID); err != nil {
				return nil, err
			}
		}
		anomaly := ""
		mo, _ := decimal.NewFromString(maxOdo)
		if odo.LessThan(mo) {
			anomaly = "odometer_back"
		}
		// consumption since the previous full tank: litres put in after it (this fill included) / km driven
		var cons *decimal.Decimal
		if full && anomaly == "" {
			var prevOdo *string
			_ = tx.QueryRow(ctx, `SELECT odometer::text FROM fleet_fuel_logs WHERE vehicle_id = $1 AND status = 'posted' AND full_tank AND odometer < $2
				ORDER BY odometer DESC LIMIT 1`, in.VehicleID, odo.String()).Scan(&prevOdo)
			if prevOdo != nil {
				po, _ := decimal.NewFromString(*prevOdo)
				var between string
				if err := tx.QueryRow(ctx, `SELECT coalesce(sum(liters), 0)::text FROM fleet_fuel_logs WHERE vehicle_id = $1 AND status = 'posted'
					AND odometer > $2::numeric AND odometer < $3::numeric`, in.VehicleID, po.String(), odo.String()).Scan(&between); err != nil {
					return nil, err
				}
				bl, _ := decimal.NewFromString(between)
				km := odo.Sub(po)
				if km.IsPositive() {
					c := bl.Add(lit).Mul(decimal.NewFromInt(100)).Div(km).Round(2)
					cons = &c
				}
			}
		}
		if cons != nil {
			ref, _ := decimal.NewFromString(norm)
			if !ref.IsPositive() {
				var avg *string
				_ = tx.QueryRow(ctx, `SELECT avg(consumption)::text FROM (SELECT consumption FROM fleet_fuel_logs WHERE vehicle_id = $1 AND status = 'posted'
					AND consumption IS NOT NULL AND anomaly = '' ORDER BY odometer DESC LIMIT 5) x`, in.VehicleID).Scan(&avg)
				if avg != nil {
					ref, _ = decimal.NewFromString(*avg)
				}
			}
			if ref.IsPositive() && cons.GreaterThan(ref.Mul(decimal.NewFromFloat(1.25))) {
				anomaly = "high_consumption"
			}
		}
		desc := fmt.Sprintf("Fuel · %s · %s L", vname, lit.String())
		exp, cur, err := fleetExpense(ctx, tx, cid, "fuel", in.AccountID, in.MethodID, in.Date, amt.String(), desc)
		if err != nil {
			return nil, err
		}
		if cur == "" {
			cur = in.Currency
			if cur == "" {
				cur = base
			}
		}
		drv := in.DriverID
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO fleet_fuel_logs (company_id, vehicle_id, log_date, odometer, liters, amount, currency, full_tank, station, driver_id, notes,
			consumption, anomaly, expense_id) VALUES ($1, $2, $3::date, $4::numeric, $5::numeric, $6::numeric, $7, $8, trim($9), coalesce(nullif($10, '')::uuid, app_user()),
			trim($11), $12::numeric, $13, $14) RETURNING id::text`, cid, in.VehicleID, in.Date, odo.String(), lit.String(), amt.String(), cur, full, in.Station, drv,
			in.Notes, cons, anomaly, exp).Scan(&id); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE vehicles SET odometer = greatest(odometer, $2::numeric) WHERE id = $1`, in.VehicleID, odo.String()); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+fuelCols+fuelFrom+` WHERE f.id = $1`, id)
	})
}

// POST /fleet/fuel/{id}/void and /fleet/service-log/{id}/void: the entry and its expense are cancelled.
func (s *Server) fleetVoid(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	table := "fleet_fuel_logs"
	if strings.Contains(r.Pattern, "/service-log/") {
		table = "fleet_service_log"
	}
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var status string
		var exp *string
		err := tx.QueryRow(ctx, `SELECT status, expense_id::text FROM `+table+` WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&status, &exp)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if status == "void" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if exp != nil {
			var est string
			if err := tx.QueryRow(ctx, `SELECT status FROM expenses WHERE id = $1`, *exp).Scan(&est); err != nil {
				return nil, err
			}
			if est != "void" {
				if err := voidExpenseTx(ctx, tx, cid, *exp); err != nil {
					return nil, err
				}
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE `+table+` SET status = 'void' WHERE id = $1`, id); err != nil {
			return nil, err
		}
		return map[string]string{"voided": id}, nil
	})
}

// GET /fleet/service-plans?vehicle_id=
func (s *Server) fleetServicePlans(w http.ResponseWriter, r *http.Request) {
	veh := r.URL.Query().Get("vehicle_id")
	if veh != "" && !isUUID(veh) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+servicePlanCols+` FROM fleet_service_plans sp JOIN vehicles v ON v.id = sp.vehicle_id
			WHERE sp.company_id = $1 AND ($2 = '' OR sp.vehicle_id = nullif($2, '')::uuid) ORDER BY v.name, sp.task`, r.PathValue("cid"), veh)
	})
}

// POST /fleet/service-plans and PATCH /fleet/service-plans/{id}
func (s *Server) fleetSavePlan(w http.ResponseWriter, r *http.Request) {
	var in struct {
		VehicleID *string `json:"vehicle_id"`
		Task      *string `json:"task"`
		EveryKm   *string `json:"every_km"`
		EveryDays *int    `json:"every_days"`
		LastKm    *string `json:"last_km"`
		LastDate  *string `json:"last_date"`
		IsActive  *bool   `json:"is_active"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	creating := id == ""
	for _, p := range []*string{in.EveryKm, in.LastKm} {
		if p != nil && *p != "" {
			if _, ok := money(*p); !ok {
				writeErr(w, http.StatusBadRequest, "bad_request")
				return
			}
		}
	}
	if (creating && (!isUUID(deref(in.VehicleID)) || strings.TrimSpace(deref(in.Task)) == "" || (deref(in.EveryKm) == "" && in.EveryDays == nil))) ||
		(!creating && (!isUUID(id) || in.VehicleID != nil)) || (in.Task != nil && (strings.TrimSpace(*in.Task) == "" || len(*in.Task) > 160)) ||
		(in.EveryDays != nil && (*in.EveryDays < 0 || *in.EveryDays > 3650)) || (in.LastDate != nil && *in.LastDate != "" && !validDate(*in.LastDate)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	status := http.StatusOK
	if creating {
		status = http.StatusCreated
	}
	s.run(w, r, status, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if creating {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM vehicles WHERE id = $1 AND company_id = $2)`, *in.VehicleID, cid).Scan(&ok); err != nil {
				return nil, err
			}
			if !ok {
				return nil, badRequest("invalid_vehicle")
			}
			if err := tx.QueryRow(ctx, `INSERT INTO fleet_service_plans (company_id, vehicle_id, task, every_km, every_days, last_km, last_date)
				VALUES ($1, $2, trim($3), nullif($4, '')::numeric, nullif($5, 0), coalesce(nullif($6, '')::numeric, (SELECT odometer FROM vehicles WHERE id = $2)),
				coalesce(nullif($7, '')::date, current_date)) RETURNING id::text`,
				cid, *in.VehicleID, *in.Task, deref(in.EveryKm), in.EveryDays, deref(in.LastKm), deref(in.LastDate)).Scan(&id); err != nil {
				return nil, err
			}
		} else {
			tag, err := tx.Exec(ctx, `UPDATE fleet_service_plans SET task = coalesce(trim($3), task),
				every_km = CASE WHEN $4::text IS NULL THEN every_km ELSE nullif($4, '')::numeric END,
				every_days = CASE WHEN $5::int IS NULL THEN every_days ELSE nullif($5, 0) END,
				last_km = CASE WHEN $6::text IS NULL THEN last_km ELSE nullif($6, '')::numeric END,
				last_date = CASE WHEN $7::text IS NULL THEN last_date ELSE nullif($7, '')::date END, is_active = coalesce($8, is_active)
				WHERE id = $1 AND company_id = $2`, id, cid, in.Task, in.EveryKm, in.EveryDays, in.LastKm, in.LastDate, in.IsActive)
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() == 0 {
				return nil, notFound()
			}
		}
		return queryJSONOne(ctx, tx, `SELECT `+servicePlanCols+` FROM fleet_service_plans sp JOIN vehicles v ON v.id = sp.vehicle_id WHERE sp.id = $1`, id)
	})
}

// GET /fleet/service-log?vehicle_id=
func (s *Server) fleetServiceLog(w http.ResponseWriter, r *http.Request) {
	veh := r.URL.Query().Get("vehicle_id")
	if veh != "" && !isUUID(veh) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+serviceLogCols+serviceLogFrom+` WHERE l.company_id = $1 AND ($2 = '' OR l.vehicle_id = nullif($2, '')::uuid)
			ORDER BY l.done_date DESC, l.created_at DESC LIMIT 2000`, r.PathValue("cid"), veh)
	})
}

// POST /fleet/service-log {vehicle_id, plan_id, date, odometer, task, amount, vendor, notes, method_id, account_id, currency}: work done;
// the plan's "last done" moves on and the cost is posted as an expense.
func (s *Server) fleetAddService(w http.ResponseWriter, r *http.Request) {
	var in struct {
		VehicleID string `json:"vehicle_id"`
		PlanID    string `json:"plan_id"`
		Date      string `json:"date"`
		Odometer  string `json:"odometer"`
		Task      string `json:"task"`
		Amount    string `json:"amount"`
		Currency  string `json:"currency"`
		Vendor    string `json:"vendor"`
		Notes     string `json:"notes"`
		MethodID  string `json:"method_id"`
		AccountID string `json:"account_id"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Date == "" {
		in.Date = today()
	}
	amt := decimal.Zero
	if strings.TrimSpace(in.Amount) != "" {
		a, ok := money(in.Amount)
		if !ok {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
		amt = a
	}
	if in.Odometer != "" {
		if _, ok := money(in.Odometer); !ok {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	if (!isUUID(in.VehicleID) && in.PlanID == "") || (in.PlanID != "" && !isUUID(in.PlanID)) || (in.VehicleID != "" && !isUUID(in.VehicleID)) || !validDate(in.Date) ||
		(in.MethodID != "" && (!isUUID(in.MethodID) || !amt.IsPositive())) || (in.AccountID != "" && !isUUID(in.AccountID)) ||
		(in.Currency != "" && !currencyRe.MatchString(in.Currency)) || len(in.Task) > 160 || len(in.Vendor) > 120 || len(in.Notes) > 1000 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if in.PlanID != "" {
			var veh, task string
			err := tx.QueryRow(ctx, `SELECT vehicle_id::text, task FROM fleet_service_plans WHERE id = $1 AND company_id = $2`, in.PlanID, cid).Scan(&veh, &task)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, badRequest("invalid_plan")
			}
			if err != nil {
				return nil, err
			}
			if in.VehicleID != "" && in.VehicleID != veh {
				return nil, badRequest("invalid_plan")
			}
			in.VehicleID = veh
			if strings.TrimSpace(in.Task) == "" {
				in.Task = task
			}
		}
		if strings.TrimSpace(in.Task) == "" {
			return nil, badRequest("bad_request")
		}
		var vname, odoNow, base string
		err := tx.QueryRow(ctx, `SELECT v.name, v.odometer::text, co.base_currency::text FROM vehicles v JOIN companies co ON co.id = v.company_id
			WHERE v.id = $1 AND v.company_id = $2 FOR UPDATE OF v`, in.VehicleID, cid).Scan(&vname, &odoNow, &base)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, badRequest("invalid_vehicle")
		}
		if err != nil {
			return nil, err
		}
		if in.Odometer == "" {
			in.Odometer = odoNow
		}
		exp, cur, err := fleetExpense(ctx, tx, cid, "service", in.AccountID, in.MethodID, in.Date, amt.String(), fmt.Sprintf("Service · %s · %s", vname, strings.TrimSpace(in.Task)))
		if err != nil {
			return nil, err
		}
		if cur == "" {
			cur = in.Currency
			if cur == "" {
				cur = base
			}
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO fleet_service_log (company_id, vehicle_id, plan_id, done_date, odometer, task, amount, currency, vendor, notes, expense_id)
			VALUES ($1, $2, nullif($3, '')::uuid, $4::date, $5::numeric, trim($6), $7::numeric, $8, trim($9), trim($10), $11) RETURNING id::text`,
			cid, in.VehicleID, in.PlanID, in.Date, in.Odometer, in.Task, amt.String(), cur, in.Vendor, in.Notes, exp).Scan(&id); err != nil {
			return nil, err
		}
		if in.PlanID != "" {
			if _, err := tx.Exec(ctx, `UPDATE fleet_service_plans SET last_km = greatest(coalesce(last_km, 0), $2::numeric),
				last_date = greatest(coalesce(last_date, $3::date), $3::date) WHERE id = $1`, in.PlanID, in.Odometer, in.Date); err != nil {
				return nil, err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE vehicles SET odometer = greatest(odometer, $2::numeric) WHERE id = $1`, in.VehicleID, in.Odometer); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+serviceLogCols+serviceLogFrom+` WHERE l.id = $1`, id)
	})
}

// POST /fleet/vehicles/{id}/link-expense {expense_id}: charge an existing expense (tolls, washing, fines…) to the vehicle.
func (s *Server) fleetLinkExpense(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ExpenseID string `json:"expense_id"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) || !isUUID(in.ExpenseID) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var ok, eok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM vehicles WHERE id = $1 AND company_id = $2),
			EXISTS (SELECT 1 FROM expenses WHERE id = $3 AND company_id = $2 AND status = 'posted')`, id, cid, in.ExpenseID).Scan(&ok, &eok); err != nil {
			return nil, err
		}
		if !ok {
			return nil, notFound()
		}
		if !eok {
			return nil, badRequest("invalid_document")
		}
		var used bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM fleet_fuel_logs WHERE expense_id = $1) OR EXISTS (SELECT 1 FROM fleet_service_log WHERE expense_id = $1)`,
			in.ExpenseID).Scan(&used); err != nil {
			return nil, err
		}
		if used {
			return nil, &apiError{status: http.StatusConflict, code: "already_linked"}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO vehicle_costs (company_id, vehicle_id, expense_id) VALUES ($1, $2, $3)`, cid, id, in.ExpenseID); err != nil {
			if isUniqueViolation(err) {
				return nil, &apiError{status: http.StatusConflict, code: "already_linked"}
			}
			return nil, err
		}
		return map[string]string{"linked": in.ExpenseID}, nil
	})
}

// GET /fleet/alerts: papers expiring within 30 days (or expired), services due or soon, fuel anomalies of the last 60 days.
func (s *Server) fleetAlerts(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		docs, err := queryJSON(ctx, tx, `SELECT v.id AS vehicle_id, v.name AS vehicle_name, v.plate, d.kind, d.expiry, d.expiry - current_date AS days
			FROM vehicles v CROSS JOIN LATERAL (VALUES ('license', v.license_expiry), ('insurance', v.insurance_expiry), ('inspection', v.inspection_expiry)) d(kind, expiry)
			WHERE v.company_id = $1 AND v.is_active AND d.expiry IS NOT NULL AND d.expiry <= current_date + 30 ORDER BY d.expiry`, cid)
		if err != nil {
			return nil, err
		}
		svc, err := queryJSON(ctx, tx, `SELECT * FROM (SELECT `+servicePlanCols+`, trim_scale(v.odometer)::text AS odometer FROM fleet_service_plans sp JOIN vehicles v ON v.id = sp.vehicle_id
			WHERE sp.company_id = $1 AND v.is_active) x WHERE state IN ('due', 'soon') ORDER BY state, vehicle_name`, cid)
		if err != nil {
			return nil, err
		}
		fuel, err := queryJSON(ctx, tx, `SELECT `+fuelCols+fuelFrom+` WHERE f.company_id = $1 AND f.status = 'posted' AND f.anomaly <> '' AND f.log_date > current_date - 60
			ORDER BY f.log_date DESC LIMIT 200`, cid)
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"documents": docs, "services": svc, "fuel": fuel}, nil
	})
}

type fleetCostRow struct {
	VehicleID   string `json:"vehicle_id"`
	Name        string `json:"vehicle_name"`
	Plate       string `json:"plate"`
	Liters      string `json:"liters"`
	Fuel        string `json:"fuel_cost"`
	Service     string `json:"service_cost"`
	Other       string `json:"other_cost"`
	Total       string `json:"total_cost"`
	Km          string `json:"km"`
	PerKm       string `json:"cost_per_km"`
	Consumption string `json:"consumption"`
	Currency    string `json:"base_currency"`
}

// fleetCosts: per vehicle in the base currency (posted expenses at their journal amounts; unposted entries count
// when they are in the base currency).
func fleetCosts(ctx context.Context, tx pgx.Tx, cid, vehicle, from, to string) ([]fleetCostRow, error) {
	rows, err := tx.Query(ctx, `
		WITH v AS (SELECT * FROM vehicles WHERE company_id = $1 AND ($2 = '' OR id = nullif($2, '')::uuid)),
		base AS (SELECT base_currency::text AS cur FROM companies WHERE id = $1),
		ebase AS (SELECT e.id, sum(jl.base_debit - jl.base_credit) AS amt FROM expenses e JOIN journal_lines jl ON jl.entry_id = e.journal_entry_id AND jl.account_id = e.account_id
		          WHERE e.company_id = $1 AND e.status = 'posted' GROUP BY e.id),
		fuel AS (SELECT f.vehicle_id, sum(f.liters) AS liters, sum(coalesce(eb.amt, CASE WHEN f.currency = (SELECT cur FROM base) THEN f.amount ELSE 0 END)) AS cost,
		           max(f.odometer) - min(f.odometer) AS km
		         FROM fleet_fuel_logs f LEFT JOIN ebase eb ON eb.id = f.expense_id
		         WHERE f.vehicle_id IN (SELECT id FROM v) AND f.status = 'posted' AND f.log_date BETWEEN $3::date AND $4::date GROUP BY 1),
		svc AS (SELECT l.vehicle_id, sum(coalesce(eb.amt, CASE WHEN l.currency = (SELECT cur FROM base) THEN l.amount ELSE 0 END)) AS cost
		        FROM fleet_service_log l LEFT JOIN ebase eb ON eb.id = l.expense_id
		        WHERE l.vehicle_id IN (SELECT id FROM v) AND l.status = 'posted' AND l.done_date BETWEEN $3::date AND $4::date GROUP BY 1),
		oth AS (SELECT vc.vehicle_id, sum(eb.amt) AS cost FROM vehicle_costs vc JOIN expenses e ON e.id = vc.expense_id AND e.status = 'posted'
		          AND e.expense_date BETWEEN $3::date AND $4::date JOIN ebase eb ON eb.id = e.id WHERE vc.vehicle_id IN (SELECT id FROM v) GROUP BY 1)
		SELECT v.id::text, v.name, v.plate, coalesce(fuel.liters, 0)::text, coalesce(fuel.cost, 0)::text, coalesce(svc.cost, 0)::text, coalesce(oth.cost, 0)::text,
			coalesce(fuel.km, 0)::text, (SELECT cur FROM base)
		FROM v LEFT JOIN fuel ON fuel.vehicle_id = v.id LEFT JOIN svc ON svc.vehicle_id = v.id LEFT JOIN oth ON oth.vehicle_id = v.id
		ORDER BY v.name`, cid, vehicle, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []fleetCostRow{}
	for rows.Next() {
		var x fleetCostRow
		if err := rows.Scan(&x.VehicleID, &x.Name, &x.Plate, &x.Liters, &x.Fuel, &x.Service, &x.Other, &x.Km, &x.Currency); err != nil {
			return nil, err
		}
		d := func(s string) decimal.Decimal { v, _ := decimal.NewFromString(s); return v }
		total := d(x.Fuel).Add(d(x.Service)).Add(d(x.Other))
		x.Total = total.Round(2).String()
		for _, p := range []*string{&x.Liters, &x.Fuel, &x.Service, &x.Other, &x.Km} {
			*p = d(*p).Round(2).String()
		}
		if km := d(x.Km); km.IsPositive() {
			x.PerKm = total.Div(km).Round(3).String()
			x.Consumption = d(x.Liters).Mul(decimal.NewFromInt(100)).Div(km).Round(2).String()
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// GET /fleet/reports/costs?from=&to=
func (s *Server) fleetCostReport(w http.ResponseWriter, r *http.Request) {
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if from == "" {
		from = time.Now().Format("2006") + "-01-01"
	}
	if to == "" {
		to = today()
	}
	if !validDate(from) || !validDate(to) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		rows, err := fleetCosts(ctx, tx, r.PathValue("cid"), "", from, to)
		if err != nil {
			return nil, err
		}
		return map[string]any{"from": from, "to": to, "rows": rows}, nil
	})
}

// GET / PUT /fleet/settings {fuel_account_id, service_account_id}: the expense accounts fleet costs post to.
func (s *Server) fleetSettings(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	if r.Method == http.MethodPut {
		var in struct {
			FuelAccountID    *string `json:"fuel_account_id"`
			ServiceAccountID *string `json:"service_account_id"`
		}
		if !decodeBody(w, r, &in) {
			return
		}
		if !optUUID(in.FuelAccountID) || !optUUID(in.ServiceAccountID) {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
		s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
			for _, a := range []*string{in.FuelAccountID, in.ServiceAccountID} {
				if a != nil && *a != "" {
					if err := requirePostable(ctx, tx, cid, *a); err != nil {
						return nil, err
					}
					if err := rejectControlAccount(ctx, tx, cid, *a); err != nil {
						return nil, err
					}
				}
			}
			if _, err := tx.Exec(ctx, `UPDATE companies SET
				fleet_fuel_account_id = CASE WHEN $2::text IS NULL THEN fleet_fuel_account_id ELSE nullif($2, '')::uuid END,
				fleet_service_account_id = CASE WHEN $3::text IS NULL THEN fleet_service_account_id ELSE nullif($3, '')::uuid END WHERE id = $1`,
				cid, in.FuelAccountID, in.ServiceAccountID); err != nil {
				return nil, err
			}
			return queryJSONOne(ctx, tx, `SELECT fleet_fuel_account_id AS fuel_account_id, fleet_service_account_id AS service_account_id FROM companies WHERE id = $1`, cid)
		})
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSONOne(ctx, tx, `SELECT fleet_fuel_account_id AS fuel_account_id, fleet_service_account_id AS service_account_id FROM companies WHERE id = $1`, cid)
	})
}
