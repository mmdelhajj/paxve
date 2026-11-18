package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Module F: generator / ISP network, generator operations log, official tariffs, taxes, local-currency snapshot.

// ---------------- taxes ----------------

type taxInfo struct {
	ID      string
	Name    string
	Rate    decimal.Decimal // percent
	Account string          // account to book the tax on (payable for sales, recoverable for purchases)
}

// loadTaxRate returns the tax rate usable for sales or purchases. id "" returns nil.
func loadTaxRate(ctx context.Context, tx pgx.Tx, cid, id, use string) (*taxInfo, error) {
	if id == "" {
		return nil, nil
	}
	if !isUUID(id) {
		return nil, badRequest("invalid_tax")
	}
	var t taxInfo
	var rate, appliesTo string
	var acc, pacc *string
	var active bool
	err := tx.QueryRow(ctx, `SELECT id::text, name, rate::text, applies_to, account_id::text, purchase_account_id::text, is_active
		FROM tax_rates WHERE id = $1 AND company_id = $2`, id, cid).Scan(&t.ID, &t.Name, &rate, &appliesTo, &acc, &pacc, &active)
	if err == pgx.ErrNoRows {
		return nil, badRequest("invalid_tax")
	}
	if err != nil {
		return nil, err
	}
	if !active || (appliesTo != "both" && appliesTo != use) {
		return nil, badRequest("invalid_tax")
	}
	t.Rate, _ = decimal.NewFromString(rate)
	if use == "sales" && acc != nil {
		t.Account = *acc
	} else if use == "purchases" && pacc != nil {
		t.Account = *pacc
	}
	if t.Account == "" {
		return nil, badRequest("invalid_tax")
	}
	return &t, nil
}

func checkTaxRate(ctx context.Context, tx pgx.Tx, cid, id, use string) error {
	_, err := loadTaxRate(ctx, tx, cid, id, use)
	return err
}

// taxOn: amount × rate%, rounded to the currency decimals.
func taxOn(amount decimal.Decimal, t *taxInfo, dec int32) decimal.Decimal {
	if t == nil {
		return decimal.Zero
	}
	return amount.Mul(t.Rate).Div(decimal.NewFromInt(100)).Round(dec)
}

const taxCols = `t.id, t.name, trim_scale(t.rate)::text AS rate, t.applies_to, t.account_id, a.code AS account_code,
	t.purchase_account_id, pa.code AS purchase_account_code, t.is_default, t.is_active`

const taxFrom = ` FROM tax_rates t LEFT JOIN accounts a ON a.id = t.account_id LEFT JOIN accounts pa ON pa.id = t.purchase_account_id`

func (s *Server) listTaxRates(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+taxCols+taxFrom+` WHERE t.company_id = $1 ORDER BY t.is_active DESC, t.name`, r.PathValue("cid"))
	})
}

func (s *Server) saveTaxRate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name              *string `json:"name"`
		Rate              *string `json:"rate"`
		AppliesTo         *string `json:"applies_to"`
		AccountID         *string `json:"account_id"`
		PurchaseAccountID *string `json:"purchase_account_id"`
		IsDefault         *bool   `json:"is_default"`
		IsActive          *bool   `json:"is_active"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	create := id == ""
	if (create && (in.Name == nil || in.Rate == nil)) || (in.Name != nil && strings.TrimSpace(*in.Name) == "") ||
		(in.AppliesTo != nil && *in.AppliesTo != "sales" && *in.AppliesTo != "purchases" && *in.AppliesTo != "both") ||
		!optUUID(in.AccountID) || !optUUID(in.PurchaseAccountID) || (!create && !isUUID(id)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.Rate != nil {
		if d, ok := money(*in.Rate); !ok || d.GreaterThan(decimal.NewFromInt(100)) {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	s.run(w, r, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[create], func(ctx context.Context, tx pgx.Tx) (any, error) {
		for _, p := range []*string{in.AccountID, in.PurchaseAccountID} {
			if p != nil && *p != "" {
				if err := requirePostable(ctx, tx, cid, *p); err != nil {
					return nil, err
				}
			}
		}
		var err error
		if create {
			applies := "both"
			if in.AppliesTo != nil {
				applies = *in.AppliesTo
			}
			err = tx.QueryRow(ctx, `INSERT INTO tax_rates (company_id, name, rate, applies_to, account_id, purchase_account_id, is_default)
				VALUES ($1, $2, $3::numeric, $4, nullif($5, '')::uuid, nullif($6, '')::uuid, coalesce($7, false)) RETURNING id::text`,
				cid, strings.TrimSpace(*in.Name), *in.Rate, applies, deref(in.AccountID), deref(in.PurchaseAccountID), in.IsDefault).Scan(&id)
		} else {
			var tag interface{ RowsAffected() int64 }
			tag, err = tx.Exec(ctx, `UPDATE tax_rates SET name = coalesce(trim($3), name), rate = coalesce($4::numeric, rate),
				applies_to = coalesce($5, applies_to),
				account_id = CASE WHEN $6::text IS NULL THEN account_id ELSE nullif($6, '')::uuid END,
				purchase_account_id = CASE WHEN $7::text IS NULL THEN purchase_account_id ELSE nullif($7, '')::uuid END,
				is_default = coalesce($8, is_default), is_active = coalesce($9, is_active)
				WHERE id = $1 AND company_id = $2`, id, cid, in.Name, in.Rate, in.AppliesTo, in.AccountID, in.PurchaseAccountID, in.IsDefault, in.IsActive)
			if err == nil && tag.RowsAffected() == 0 {
				return nil, notFound()
			}
		}
		if err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+taxCols+taxFrom+` WHERE t.id = $1`, id)
	})
}

// ensureDefaultTax creates the template's VAT rate (not applied to any plan) when the company has none.
func ensureDefaultTax(ctx context.Context, tx pgx.Tx, cid, templateKey string, ids map[string]string) error {
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM tax_rates WHERE company_id = $1`, cid).Scan(&n); err != nil || n > 0 {
		return err
	}
	switch templateKey {
	case "lebanon":
		if ids["4427"] == "" || ids["4426"] == "" {
			return nil
		}
		_, err := tx.Exec(ctx, `INSERT INTO tax_rates (company_id, name, rate, applies_to, account_id, purchase_account_id, is_default)
			VALUES ($1, 'VAT 11%', 11, 'both', $2, $3, true)`, cid, ids["4427"], ids["4426"])
		return err
	case "generic":
		if ids["2200"] == "" {
			return nil
		}
		_, err := tx.Exec(ctx, `INSERT INTO tax_rates (company_id, name, rate, applies_to, account_id, purchase_account_id, is_default)
			VALUES ($1, 'VAT', 10, 'both', $2, $2, true)`, cid, ids["2200"])
		return err
	}
	return nil
}

// localSnapshot converts an invoice total to the company's second currency at the official rate of the
// issue date, when the company shows bills in a second currency. Missing rates simply skip the snapshot.
func localSnapshot(ctx context.Context, tx pgx.Tx, cid string, set *billingSettings, currency, date string, total decimal.Decimal) (cur *string, rate, amount *string) {
	var show bool
	var local string
	if err := tx.QueryRow(ctx, `SELECT show_local_currency_on_bills, local_currency::text FROM companies WHERE id = $1`, cid).Scan(&show, &local); err != nil {
		return nil, nil, nil
	}
	if !show || local == currency {
		return nil, nil, nil
	}
	rInvS, err1 := resolveRate(ctx, tx, cid, set.Base, currency, date, "")
	rLocS, err2 := resolveRate(ctx, tx, cid, set.Base, local, date, "")
	if err1 != nil || err2 != nil {
		return nil, nil, nil
	}
	rInv, _ := decimal.NewFromString(rInvS)
	rLoc, _ := decimal.NewFromString(rLocS)
	dec, err := currencyDecimals(ctx, tx, local)
	if err != nil || rInv.IsZero() {
		return nil, nil, nil
	}
	conv := rLoc.DivRound(rInv, 10) // units of local per 1 unit of invoice currency
	amt := total.Mul(conv).Round(dec).StringFixed(4)
	r := conv.String()
	return &local, &r, &amt
}

// ---------------- network ----------------

const stationCols = `st.id, st.name, st.location, st.notes, st.is_active,
	(SELECT count(*) FROM generators g WHERE g.station_id = st.id) AS generators,
	(SELECT count(*) FROM boxes b WHERE b.station_id = st.id) AS boxes,
	(SELECT count(*) FROM subscriptions s JOIN boxes b ON b.id = s.box_id WHERE b.station_id = st.id AND s.status = 'active') AS subscriptions`

func (s *Server) listStations(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+stationCols+` FROM stations st WHERE st.company_id = $1 ORDER BY st.is_active DESC, st.name`, r.PathValue("cid"))
	})
}

func (s *Server) saveStation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     *string `json:"name"`
		Location *string `json:"location"`
		Notes    *string `json:"notes"`
		IsActive *bool   `json:"is_active"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	create := id == ""
	if (create && in.Name == nil) || (in.Name != nil && strings.TrimSpace(*in.Name) == "") || (!create && !isUUID(id)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[create], func(ctx context.Context, tx pgx.Tx) (any, error) {
		if create {
			if _, err := companyBase(ctx, tx, cid); err != nil {
				return nil, err
			}
			if err := tx.QueryRow(ctx, `INSERT INTO stations (company_id, name, location, notes) VALUES ($1, $2, $3, $4) RETURNING id::text`,
				cid, strings.TrimSpace(*in.Name), strings.TrimSpace(deref(in.Location)), deref(in.Notes)).Scan(&id); err != nil {
				return nil, err
			}
		} else {
			tag, err := tx.Exec(ctx, `UPDATE stations SET name = coalesce(trim($3), name), location = coalesce(trim($4), location),
				notes = coalesce($5, notes), is_active = coalesce($6, is_active) WHERE id = $1 AND company_id = $2`,
				id, cid, in.Name, in.Location, in.Notes, in.IsActive)
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() == 0 {
				return nil, notFound()
			}
		}
		return queryJSONOne(ctx, tx, `SELECT `+stationCols+` FROM stations st WHERE st.id = $1`, id)
	})
}

func checkStation(ctx context.Context, tx pgx.Tx, cid, id string) error {
	if !isUUID(id) {
		return badRequest("invalid_station")
	}
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM stations WHERE id = $1 AND company_id = $2)`, id, cid).Scan(&ok)
	if err == nil && !ok {
		return badRequest("invalid_station")
	}
	return err
}

const generatorCols = `g.id, g.station_id, st.name AS station_name, g.name, trim_scale(g.kva)::text AS kva, g.brand, g.serial,
	g.fuel_item_id, it.name AS fuel_item_name, g.status, g.notes,
	(SELECT h.hour_meter::text FROM generator_hours h WHERE h.generator_id = g.id ORDER BY h.log_date DESC LIMIT 1) AS last_hour_meter,
	(SELECT h.log_date FROM generator_hours h WHERE h.generator_id = g.id ORDER BY h.log_date DESC LIMIT 1) AS last_log_date,
	EXISTS (SELECT 1 FROM outages o WHERE o.generator_id = g.id AND o.ended_at IS NULL) AS in_outage`

const generatorFrom = ` FROM generators g JOIN stations st ON st.id = g.station_id LEFT JOIN items it ON it.id = g.fuel_item_id`

func (s *Server) listGenerators(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+generatorCols+generatorFrom+` WHERE g.company_id = $1 ORDER BY st.name, g.name`, r.PathValue("cid"))
	})
}

func (s *Server) saveGenerator(w http.ResponseWriter, r *http.Request) {
	var in struct {
		StationID  *string `json:"station_id"`
		Name       *string `json:"name"`
		KVA        *string `json:"kva"`
		Brand      *string `json:"brand"`
		Serial     *string `json:"serial"`
		FuelItemID *string `json:"fuel_item_id"`
		Status     *string `json:"status"`
		Notes      *string `json:"notes"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	create := id == ""
	okKVA := in.KVA == nil || *in.KVA == ""
	if !okKVA {
		_, okKVA = money(*in.KVA)
	}
	if (create && (in.Name == nil || in.StationID == nil)) || (in.Name != nil && strings.TrimSpace(*in.Name) == "") || !okKVA ||
		!optUUID(in.FuelItemID) || (in.Status != nil && *in.Status != "active" && *in.Status != "maintenance" && *in.Status != "retired") ||
		(!create && !isUUID(id)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[create], func(ctx context.Context, tx pgx.Tx) (any, error) {
		if in.StationID != nil {
			if err := checkStation(ctx, tx, cid, *in.StationID); err != nil {
				return nil, err
			}
		}
		if in.FuelItemID != nil && *in.FuelItemID != "" {
			if _, err := loadItem(ctx, tx, cid, *in.FuelItemID); err != nil {
				return nil, err
			}
		}
		if create {
			if err := tx.QueryRow(ctx, `INSERT INTO generators (company_id, station_id, name, kva, brand, serial, fuel_item_id, notes)
				VALUES ($1, $2, $3, nullif($4, '')::numeric, $5, $6, nullif($7, '')::uuid, $8) RETURNING id::text`,
				cid, *in.StationID, strings.TrimSpace(*in.Name), deref(in.KVA), strings.TrimSpace(deref(in.Brand)),
				strings.TrimSpace(deref(in.Serial)), deref(in.FuelItemID), deref(in.Notes)).Scan(&id); err != nil {
				return nil, err
			}
		} else {
			tag, err := tx.Exec(ctx, `UPDATE generators SET station_id = coalesce($3::uuid, station_id), name = coalesce(trim($4), name),
				kva = CASE WHEN $5::text IS NULL THEN kva ELSE nullif($5, '')::numeric END, brand = coalesce(trim($6), brand),
				serial = coalesce(trim($7), serial),
				fuel_item_id = CASE WHEN $8::text IS NULL THEN fuel_item_id ELSE nullif($8, '')::uuid END,
				status = coalesce($9, status), notes = coalesce($10, notes) WHERE id = $1 AND company_id = $2`,
				id, cid, in.StationID, in.Name, in.KVA, in.Brand, in.Serial, in.FuelItemID, in.Status, in.Notes)
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() == 0 {
				return nil, notFound()
			}
		}
		return queryJSONOne(ctx, tx, `SELECT `+generatorCols+generatorFrom+` WHERE g.id = $1`, id)
	})
}

const boxCols = `b.id, b.station_id, st.name AS station_name, b.code, b.location, b.notes, b.is_active,
	(SELECT count(*) FROM subscriptions s WHERE s.box_id = b.id AND s.status = 'active') AS subscriptions`

func (s *Server) listBoxes(w http.ResponseWriter, r *http.Request) {
	st := r.URL.Query().Get("station_id")
	if st != "" && !isUUID(st) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+boxCols+` FROM boxes b JOIN stations st ON st.id = b.station_id
			WHERE b.company_id = $1 AND ($2 = '' OR b.station_id = $2::uuid) ORDER BY st.name, b.code`, r.PathValue("cid"), st)
	})
}

func (s *Server) saveBox(w http.ResponseWriter, r *http.Request) {
	var in struct {
		StationID *string `json:"station_id"`
		Code      *string `json:"code"`
		Location  *string `json:"location"`
		Notes     *string `json:"notes"`
		IsActive  *bool   `json:"is_active"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	create := id == ""
	if (create && (in.Code == nil || in.StationID == nil)) || (in.Code != nil && strings.TrimSpace(*in.Code) == "") || (!create && !isUUID(id)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[create], func(ctx context.Context, tx pgx.Tx) (any, error) {
		if in.StationID != nil {
			if err := checkStation(ctx, tx, cid, *in.StationID); err != nil {
				return nil, err
			}
		}
		var err error
		if create {
			err = tx.QueryRow(ctx, `INSERT INTO boxes (company_id, station_id, code, location, notes) VALUES ($1, $2, $3, $4, $5) RETURNING id::text`,
				cid, *in.StationID, strings.TrimSpace(*in.Code), strings.TrimSpace(deref(in.Location)), deref(in.Notes)).Scan(&id)
		} else {
			var n int64
			var tagErr error
			tag, e := tx.Exec(ctx, `UPDATE boxes SET station_id = coalesce($3::uuid, station_id), code = coalesce(trim($4), code),
				location = coalesce(trim($5), location), notes = coalesce($6, notes), is_active = coalesce($7, is_active)
				WHERE id = $1 AND company_id = $2`, id, cid, in.StationID, in.Code, in.Location, in.Notes, in.IsActive)
			tagErr = e
			if e == nil {
				n = tag.RowsAffected()
			}
			err = tagErr
			if err == nil && n == 0 {
				return nil, notFound()
			}
		}
		if isUniqueViolation(err) {
			return nil, &apiError{status: http.StatusConflict, code: "duplicate"}
		}
		if err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+boxCols+` FROM boxes b JOIN stations st ON st.id = b.station_id WHERE b.id = $1`, id)
	})
}

func checkBox(ctx context.Context, tx pgx.Tx, cid, id string) error {
	if id == "" {
		return nil
	}
	if !isUUID(id) {
		return badRequest("invalid_box")
	}
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM boxes WHERE id = $1 AND company_id = $2)`, id, cid).Scan(&ok)
	if err == nil && !ok {
		return badRequest("invalid_box")
	}
	return err
}

// ---------------- generator operations ----------------

func checkGenerator(ctx context.Context, tx pgx.Tx, cid, id string) error {
	if !isUUID(id) {
		return badRequest("invalid_generator")
	}
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM generators WHERE id = $1 AND company_id = $2)`, id, cid).Scan(&ok)
	if err == nil && !ok {
		return badRequest("invalid_generator")
	}
	return err
}

func (s *Server) listGeneratorLog(w http.ResponseWriter, r *http.Request) {
	cid, gid := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if err := checkGenerator(ctx, tx, cid, gid); err != nil {
			return nil, err
		}
		hours, err := queryJSON(ctx, tx, `SELECT h.id, h.log_date, h.hour_meter::text AS hour_meter,
			(h.hour_meter - lag(h.hour_meter) OVER (ORDER BY h.log_date))::text AS hours_run, h.notes, u.full_name AS created_by
			FROM generator_hours h LEFT JOIN users u ON u.id = h.created_by WHERE h.generator_id = $1 ORDER BY h.log_date DESC LIMIT 120`, gid)
		if err != nil {
			return nil, err
		}
		outs, err := queryJSON(ctx, tx, `SELECT o.id, o.started_at, o.ended_at, o.reason,
			round(extract(epoch FROM (coalesce(o.ended_at, now()) - o.started_at)) / 60)::int AS minutes
			FROM outages o WHERE o.generator_id = $1 ORDER BY o.started_at DESC LIMIT 120`, gid)
		if err != nil {
			return nil, err
		}
		fuel, err := queryJSON(ctx, tx, `SELECT d.id, d.number, d.doc_date, (-d.quantity)::text AS quantity, it.unit, (-d.value)::text AS value, d.notes
			FROM stock_docs d JOIN items it ON it.id = d.item_id WHERE d.generator_id = $1 AND d.kind = 'fuel' AND d.status = 'posted'
			ORDER BY d.doc_date DESC LIMIT 120`, gid)
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"hours": hours, "outages": outs, "fuel": fuel}, nil
	})
}

func (s *Server) saveGeneratorHours(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Date      string `json:"date"`
		HourMeter string `json:"hour_meter"`
		Notes     string `json:"notes"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Date == "" {
		in.Date = today()
	}
	hm, ok := money(in.HourMeter)
	if !validDate(in.Date) || !ok {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid, gid := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if err := checkGenerator(ctx, tx, cid, gid); err != nil {
			return nil, err
		}
		var prev *string
		if err := tx.QueryRow(ctx, `SELECT max(hour_meter)::text FROM generator_hours WHERE generator_id = $1 AND log_date < $2::date`,
			gid, in.Date).Scan(&prev); err != nil {
			return nil, err
		}
		if prev != nil {
			if p, _ := decimal.NewFromString(*prev); hm.LessThan(p) {
				return nil, &apiError{status: http.StatusUnprocessableEntity, code: "reading_below_previous", detail: *prev}
			}
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO generator_hours (generator_id, log_date, hour_meter, notes) VALUES ($1, $2::date, $3::numeric, $4)
			ON CONFLICT (tenant_id, generator_id, log_date) DO UPDATE SET hour_meter = EXCLUDED.hour_meter, notes = EXCLUDED.notes
			RETURNING id::text`, gid, in.Date, hm.String(), strings.TrimSpace(in.Notes)).Scan(&id); err != nil {
			return nil, err
		}
		return map[string]string{"id": id}, nil
	})
}

func (s *Server) saveOutage(w http.ResponseWriter, r *http.Request) {
	var in struct {
		StartedAt *string `json:"started_at"`
		EndedAt   *string `json:"ended_at"` // "" = still ongoing; "now" = ends now
		Reason    *string `json:"reason"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, gid, oid := r.PathValue("cid"), r.PathValue("id"), r.PathValue("oid")
	parse := func(p *string) (*time.Time, bool) {
		if p == nil || *p == "" {
			return nil, true
		}
		if *p == "now" {
			t := time.Now()
			return &t, true
		}
		t, err := time.Parse(time.RFC3339, *p)
		if err != nil {
			if t2, err2 := time.ParseInLocation("2006-01-02T15:04", *p, time.Local); err2 == nil {
				return &t2, true
			}
			return nil, false
		}
		return &t, true
	}
	start, ok1 := parse(in.StartedAt)
	end, ok2 := parse(in.EndedAt)
	if !ok1 || !ok2 || (oid != "" && !isUUID(oid)) || (start != nil && end != nil && end.Before(*start)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if err := checkGenerator(ctx, tx, cid, gid); err != nil {
			return nil, err
		}
		if oid == "" {
			if start == nil {
				t := time.Now()
				start = &t
			}
			var id string
			if err := tx.QueryRow(ctx, `INSERT INTO outages (generator_id, started_at, ended_at, reason) VALUES ($1, $2, $3, $4) RETURNING id::text`,
				gid, *start, end, strings.TrimSpace(deref(in.Reason))).Scan(&id); err != nil {
				return nil, err
			}
			return map[string]string{"id": id}, nil
		}
		tag, err := tx.Exec(ctx, `UPDATE outages SET started_at = coalesce($3, started_at), ended_at = coalesce($4, ended_at),
			reason = coalesce(trim($5), reason) WHERE id = $1 AND generator_id = $2`, oid, gid, start, end, in.Reason)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return map[string]string{"id": oid}, nil
	})
}

// ---------------- tariffs ----------------

const tariffCols = `t.id, t.period, t.currency, t.price_per_kwh::text AS price_per_kwh, t.fixed_fees, t.notes, t.applied_at,
	u.full_name AS applied_by, t.created_at`

func (s *Server) listTariffs(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+tariffCols+` FROM tariffs t LEFT JOIN users u ON u.id = t.applied_by
			WHERE t.company_id = $1 ORDER BY t.period DESC LIMIT 120`, r.PathValue("cid"))
	})
}

// saveTariff creates or updates the tariff of a period. fixed_fees: {"<plan_id>": "385000", ...}.
func (s *Server) saveTariff(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Period      string            `json:"period"`
		Currency    string            `json:"currency"`
		PricePerKWh string            `json:"price_per_kwh"`
		FixedFees   map[string]string `json:"fixed_fees"`
		Notes       string            `json:"notes"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	price, ok := money(in.PricePerKWh)
	if !validPeriod(in.Period) || !currencyRe.MatchString(in.Currency) || !ok {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	fees := map[string]string{}
	for k, v := range in.FixedFees {
		if strings.TrimSpace(v) == "" {
			continue
		}
		d, ok := money(v)
		if !isUUID(k) || !ok {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
		fees[k] = d.String()
	}
	feesJSON, _ := json.Marshal(fees)
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := companyBase(ctx, tx, cid); err != nil {
			return nil, err
		}
		for k := range fees {
			if err := checkPlanAny(ctx, tx, cid, k); err != nil {
				return nil, err
			}
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO tariffs (company_id, period, currency, price_per_kwh, fixed_fees, notes)
			VALUES ($1, $2, $3, $4::numeric, $5, $6)
			ON CONFLICT (tenant_id, company_id, period) DO UPDATE SET currency = EXCLUDED.currency, price_per_kwh = EXCLUDED.price_per_kwh,
			  fixed_fees = EXCLUDED.fixed_fees, notes = EXCLUDED.notes RETURNING id::text`,
			cid, in.Period, in.Currency, price.String(), feesJSON, strings.TrimSpace(in.Notes)).Scan(&id); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+tariffCols+` FROM tariffs t LEFT JOIN users u ON u.id = t.applied_by WHERE t.id = $1`, id)
	})
}

func checkPlanAny(ctx context.Context, tx pgx.Tx, cid, plan string) error {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM plans WHERE id = $1 AND company_id = $2)`, plan, cid).Scan(&ok)
	if err == nil && !ok {
		return badRequest("invalid_plan")
	}
	return err
}

// applyTariff copies the tariff into the selected plans: metered plans get price_per_kwh as unit price,
// plans listed in fixed_fees get that monthly fee. Plans must be in the tariff currency. Audit-logged by triggers.
func (s *Server) applyTariff(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PlanIDs []string `json:"plan_ids"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) || len(in.PlanIDs) == 0 || len(in.PlanIDs) > 500 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var cur, price string
		var feesRaw []byte
		err := tx.QueryRow(ctx, `SELECT currency::text, price_per_kwh::text, fixed_fees FROM tariffs WHERE id = $1 AND company_id = $2 FOR UPDATE`,
			id, cid).Scan(&cur, &price, &feesRaw)
		if err == pgx.ErrNoRows {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		fees := map[string]string{}
		_ = json.Unmarshal(feesRaw, &fees)
		updated := 0
		for _, pid := range in.PlanIDs {
			if !isUUID(pid) {
				return nil, badRequest("invalid_plan")
			}
			var ptype, pcur string
			err := tx.QueryRow(ctx, `SELECT billing_type, currency::text FROM plans WHERE id = $1 AND company_id = $2 FOR UPDATE`, pid, cid).Scan(&ptype, &pcur)
			if err == pgx.ErrNoRows {
				return nil, badRequest("invalid_plan")
			}
			if err != nil {
				return nil, err
			}
			if pcur != cur {
				return nil, &apiError{status: http.StatusUnprocessableEntity, code: "currency_mismatch", detail: pcur}
			}
			var unit, fee *string
			if ptype == "metered" {
				unit = &price
			}
			if f, ok := fees[pid]; ok {
				fee = &f
			}
			if unit == nil && fee == nil {
				continue
			}
			if _, err := tx.Exec(ctx, `UPDATE plans SET unit_price = coalesce($2::numeric, unit_price), base_fee = coalesce($3::numeric, base_fee)
				WHERE id = $1`, pid, unit, fee); err != nil {
				return nil, err
			}
			updated++
		}
		if _, err := tx.Exec(ctx, `UPDATE tariffs SET applied_at = now(), applied_by = app_user() WHERE id = $1`, id); err != nil {
			return nil, err
		}
		return map[string]int{"updated": updated}, nil
	})
}

// ---------------- reports ----------------

// vatReport: tax collected on sales vs recoverable on purchases, from posted journal lines on the tax accounts.
func (s *Server) vatReport(w http.ResponseWriter, r *http.Request) {
	from, to, ok := dateRange(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid_date")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		base, err := companyBase(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		rows, err := queryJSON(ctx, tx, `
			SELECT t.id, t.name, trim_scale(t.rate)::text AS rate,
			  coalesce((SELECT sum(l.base_credit - l.base_debit) FROM journal_lines l JOIN journal_entries e ON e.id = l.entry_id AND e.status = 'posted'
			            WHERE l.account_id = t.account_id AND e.company_id = $1 AND e.entry_date BETWEEN $2::date AND $3::date), 0)::text AS collected,
			  coalesce((SELECT sum(l.base_debit - l.base_credit) FROM journal_lines l JOIN journal_entries e ON e.id = l.entry_id AND e.status = 'posted'
			            WHERE l.account_id = t.purchase_account_id AND t.purchase_account_id IS DISTINCT FROM t.account_id
			              AND e.company_id = $1 AND e.entry_date BETWEEN $2::date AND $3::date), 0)::text AS recoverable,
			  -- taxable bases in base currency (each document at its own booking rate), less credit / debit notes in the period
			  round(coalesce((SELECT sum(il.amount / `+docRate("i")+`) FROM invoice_lines il JOIN invoices i ON i.id = il.invoice_id
			            WHERE il.tax_rate_id = t.id AND i.status <> 'void' AND i.issue_date BETWEEN $2::date AND $3::date), 0)
			  - coalesce((SELECT sum(p.amount / nullif(i.total, 0) * (SELECT sum(il.amount) FROM invoice_lines il WHERE il.invoice_id = i.id AND il.tax_rate_id = t.id)
			                         / `+docRate("i")+`)
			            FROM payments p JOIN invoices i ON i.id = p.invoice_id
			            WHERE p.kind = 'credit_note' AND p.status = 'posted' AND p.payment_date BETWEEN $2::date AND $3::date), 0), 4)::text AS taxable_sales,
			  round(coalesce((SELECT sum(bl.amount / `+docRate("b")+`) FROM bill_lines bl JOIN bills b ON b.id = bl.bill_id
			            WHERE bl.tax_rate_id = t.id AND b.status <> 'void' AND b.bill_date BETWEEN $2::date AND $3::date), 0)
			  - coalesce((SELECT sum(p.amount / nullif(b.total, 0) * (SELECT sum(bl.amount) FROM bill_lines bl WHERE bl.bill_id = b.id AND bl.tax_rate_id = t.id)
			                         / `+docRate("b")+`)
			            FROM supplier_payments p JOIN bills b ON b.id = p.bill_id
			            WHERE p.kind = 'debit_note' AND p.status = 'posted' AND p.payment_date BETWEEN $2::date AND $3::date), 0)
			  + coalesce((SELECT sum((e.amount - e.tax_amount) / `+docRate("e")+`) FROM expenses e
			            WHERE e.tax_rate_id = t.id AND e.status = 'posted' AND e.expense_date BETWEEN $2::date AND $3::date), 0), 4)::text AS taxable_purchases
			FROM tax_rates t WHERE t.company_id = $1 ORDER BY t.name`, cid, from, to)
		if err != nil {
			return nil, err
		}
		var collected, recoverable string
		if err := tx.QueryRow(ctx, `
			SELECT coalesce(sum(l.base_credit - l.base_debit) FILTER (WHERE l.account_id IN (SELECT account_id FROM tax_rates WHERE company_id = $1)), 0)::text,
			       coalesce(sum(l.base_debit - l.base_credit) FILTER (WHERE l.account_id IN (SELECT purchase_account_id FROM tax_rates WHERE company_id = $1)
			                AND l.account_id NOT IN (SELECT account_id FROM tax_rates WHERE company_id = $1 AND account_id IS NOT NULL)), 0)::text
			FROM journal_lines l JOIN journal_entries e ON e.id = l.entry_id AND e.status = 'posted'
			WHERE e.company_id = $1 AND e.entry_date BETWEEN $2::date AND $3::date`, cid, from, to).Scan(&collected, &recoverable); err != nil {
			return nil, err
		}
		c, _ := decimal.NewFromString(collected)
		rc, _ := decimal.NewFromString(recoverable)
		return map[string]any{"base_currency": base, "from": from, "to": to, "rows": rows,
			"collected": collected, "recoverable": recoverable, "net": c.Sub(rc).StringFixed(4)}, nil
	})
}

// docRate: SQL for the booking rate (document currency per base) of document alias a, from its own journal entry.
func docRate(a string) string {
	return `coalesce((SELECT l.rate FROM journal_lines l WHERE l.entry_id = ` + a + `.journal_entry_id AND l.currency = ` + a +
		`.currency ORDER BY l.line_no LIMIT 1), 1)`
}

// generatorReport: per generator hours run, fuel used, litres/hour, outages; per station kWh billed and fuel cost per kWh.
func (s *Server) generatorReport(w http.ResponseWriter, r *http.Request) {
	from, to, ok := dateRange(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid_date")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		base, err := companyBase(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		gens, err := queryJSON(ctx, tx, `
			SELECT g.id, g.name, st.id AS station_id, st.name AS station_name, trim_scale(g.kva)::text AS kva,
			  coalesce((SELECT max(h.hour_meter) FROM generator_hours h WHERE h.generator_id = g.id AND h.log_date <= $3::date)
			         - coalesce((SELECT max(h.hour_meter) FROM generator_hours h WHERE h.generator_id = g.id AND h.log_date < $2::date),
			                    (SELECT min(h.hour_meter) FROM generator_hours h WHERE h.generator_id = g.id AND h.log_date BETWEEN $2::date AND $3::date)), 0)::text AS hours,
			  coalesce((SELECT -sum(d.quantity) FROM stock_docs d WHERE d.generator_id = g.id AND d.kind = 'fuel' AND d.status = 'posted' AND d.doc_date BETWEEN $2::date AND $3::date), 0)::text AS fuel_qty,
			  coalesce((SELECT -sum(d.value) FROM stock_docs d WHERE d.generator_id = g.id AND d.kind = 'fuel' AND d.status = 'posted' AND d.doc_date BETWEEN $2::date AND $3::date), 0)::text AS fuel_cost,
			  (SELECT count(*) FROM outages o WHERE o.generator_id = g.id AND o.started_at::date BETWEEN $2::date AND $3::date) AS outages,
			  coalesce((SELECT round(sum(extract(epoch FROM (coalesce(o.ended_at, now()) - o.started_at))) / 60) FROM outages o
			            WHERE o.generator_id = g.id AND o.started_at::date BETWEEN $2::date AND $3::date), 0)::int AS outage_minutes
			FROM generators g JOIN stations st ON st.id = g.station_id WHERE g.company_id = $1 ORDER BY st.name, g.name`, cid, from, to)
		if err != nil {
			return nil, err
		}
		// kWh billed: usage lines of invoices whose month falls in the range, for subscriptions on the station's boxes
		stations, err := queryJSON(ctx, tx, `
			SELECT st.id, st.name,
			  coalesce((SELECT sum(il.quantity) FROM invoice_lines il JOIN invoices i ON i.id = il.invoice_id
			            JOIN subscriptions s ON s.id = i.subscription_id JOIN boxes b ON b.id = s.box_id
			            WHERE b.station_id = st.id AND il.kind = 'usage' AND i.status <> 'void'
			              AND i.period BETWEEN to_char($2::date, 'YYYY-MM') AND to_char($3::date, 'YYYY-MM')), 0)::text AS kwh_billed,
			  coalesce((SELECT -sum(d.value) FROM stock_docs d JOIN generators g ON g.id = d.generator_id
			            WHERE g.station_id = st.id AND d.kind = 'fuel' AND d.status = 'posted' AND d.doc_date BETWEEN $2::date AND $3::date), 0)::text AS fuel_cost,
			  coalesce((SELECT -sum(d.quantity) FROM stock_docs d JOIN generators g ON g.id = d.generator_id
			            WHERE g.station_id = st.id AND d.kind = 'fuel' AND d.status = 'posted' AND d.doc_date BETWEEN $2::date AND $3::date), 0)::text AS fuel_qty
			FROM stations st WHERE st.company_id = $1 ORDER BY st.name`, cid, from, to)
		if err != nil {
			return nil, err
		}
		return map[string]any{"base_currency": base, "from": from, "to": to, "generators": gens, "stations": stations}, nil
	})
}
