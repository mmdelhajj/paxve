package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"
)

// Restaurant / café (business pack "restaurant"), on top of the till:
//   tables (floor plan with positions), modifiers (extras, "without", size) priced in the base currency,
//   open checks (dine-in on a table, takeaway, delivery) whose lines go to the kitchen display by station
//   (new → sent → cooking → ready → served), split by lines, paid through a normal till sale (createPosOrderTx) with
//   the service charge (revenue) and the tip (owed to the staff, a liability). A dish is a kit whose components are its
//   ingredients, so a sale takes the ingredients out of stock at cost; the food cost report compares the two per day.

var kitchenFlow = map[string][]string{"sent": {"cooking", "ready"}, "cooking": {"ready", "sent"}, "ready": {"served", "cooking"}, "new": {}}

// ---------- tables ----------

func (s *Server) restTables(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT id, name, area, seats, x, y, shape, is_active FROM rest_tables WHERE company_id = $1 ORDER BY area, name`, r.PathValue("cid"))
	})
}

func (s *Server) restSaveTable(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     *string `json:"name"`
		Area     *string `json:"area"`
		Seats    *int    `json:"seats"`
		X        *int    `json:"x"`
		Y        *int    `json:"y"`
		Shape    *string `json:"shape"`
		IsActive *bool   `json:"is_active"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	inRange := func(p *int, lo, hi int) bool { return p == nil || (*p >= lo && *p <= hi) }
	if (id == "" && (in.Name == nil || strings.TrimSpace(*in.Name) == "")) || (in.Name != nil && (strings.TrimSpace(*in.Name) == "" || len(*in.Name) > 40)) ||
		len(deref(in.Area)) > 40 || !inRange(in.Seats, 1, 100) || !inRange(in.X, 0, 100) || !inRange(in.Y, 0, 100) ||
		(in.Shape != nil && *in.Shape != "square" && *in.Shape != "round" && *in.Shape != "long") || (id != "" && !isUUID(id)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	status := http.StatusOK
	if id == "" {
		status = http.StatusCreated
	}
	s.run(w, r, status, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var err error
		if id == "" {
			err = tx.QueryRow(ctx, `INSERT INTO rest_tables (company_id, name, area, seats, x, y, shape) VALUES ($1, $2, coalesce($3, ''), coalesce($4, 4),
				coalesce($5, 0), coalesce($6, 0), coalesce($7, 'square')) RETURNING id::text`, cid, strings.TrimSpace(*in.Name), in.Area, in.Seats, in.X, in.Y, in.Shape).Scan(&id)
		} else {
			var tag pgconn.CommandTag
			tag, err = tx.Exec(ctx, `UPDATE rest_tables SET name = coalesce(nullif(trim($3), ''), name), area = coalesce($4, area), seats = coalesce($5, seats),
				x = coalesce($6, x), y = coalesce($7, y), shape = coalesce($8, shape), is_active = coalesce($9, is_active) WHERE id = $1 AND company_id = $2`,
				id, cid, in.Name, in.Area, in.Seats, in.X, in.Y, in.Shape, in.IsActive)
			if err == nil && tag.RowsAffected() == 0 {
				return nil, notFound()
			}
		}
		if isUniqueViolation(err) {
			return nil, &apiError{status: http.StatusConflict, code: "duplicate"}
		}
		if err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT id, name, area, seats, x, y, shape, is_active FROM rest_tables WHERE id = $1`, id)
	})
}

func (s *Server) restDeleteTable(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var used bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM rest_checks WHERE table_id = $1)`, id).Scan(&used); err != nil {
			return nil, err
		}
		var tag pgconn.CommandTag
		var err error
		if used { // keep the history: switch it off
			tag, err = tx.Exec(ctx, `UPDATE rest_tables SET is_active = false WHERE id = $1 AND company_id = $2`, id, cid)
		} else {
			tag, err = tx.Exec(ctx, `DELETE FROM rest_tables WHERE id = $1 AND company_id = $2`, id, cid)
		}
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return map[string]bool{"ok": true}, nil
	})
}

// ---------- modifiers ----------

const modifierCols = `id, name, name_i18n, grp, price::text AS price, item_ids, category_ids, sort, is_active`

// GET /rest/modifiers?item_id= : all modifiers, or those that apply to the item.
func (s *Server) restModifiers(w http.ResponseWriter, r *http.Request) {
	item := r.URL.Query().Get("item_id")
	if item != "" && !isUUID(item) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+modifierCols+` FROM rest_modifiers m WHERE m.company_id = $1
			AND ($2 = '' OR (m.is_active AND ((cardinality(m.item_ids) = 0 AND cardinality(m.category_ids) = 0) OR $2::uuid = ANY (m.item_ids)
			  OR (SELECT category_id FROM items WHERE id = $2::uuid) = ANY (m.category_ids))))
			ORDER BY m.grp, m.sort, m.name`, r.PathValue("cid"), item)
	})
}

func (s *Server) restSaveModifier(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name        *string            `json:"name"`
		NameI18n    *map[string]string `json:"name_i18n"`
		Group       *string            `json:"grp"`
		Price       *string            `json:"price"`
		ItemIDs     *[]string          `json:"item_ids"`
		CategoryIDs *[]string          `json:"category_ids"`
		Sort        *int               `json:"sort"`
		IsActive    *bool              `json:"is_active"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	ok := (id != "" || (in.Name != nil && strings.TrimSpace(*in.Name) != "")) && (in.Name == nil || (strings.TrimSpace(*in.Name) != "" && len(*in.Name) <= 80)) &&
		len(deref(in.Group)) <= 40 && (id == "" || isUUID(id))
	if in.Price != nil {
		if p, good := money(*in.Price); !good || p.IsNegative() {
			ok = false
		}
	}
	if (in.ItemIDs != nil && (len(*in.ItemIDs) > 500 || !allUUID(*in.ItemIDs))) || (in.CategoryIDs != nil && (len(*in.CategoryIDs) > 100 || !allUUID(*in.CategoryIDs))) {
		ok = false
	}
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	var i18n []byte
	if in.NameI18n != nil {
		i18n, _ = json.Marshal(*in.NameI18n)
	}
	cid := r.PathValue("cid")
	status := http.StatusOK
	if id == "" {
		status = http.StatusCreated
	}
	s.run(w, r, status, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if id == "" {
			if err := tx.QueryRow(ctx, `INSERT INTO rest_modifiers (company_id, name, name_i18n, grp, price, item_ids, category_ids, sort)
				VALUES ($1, $2, coalesce($3::jsonb, '{}'), coalesce($4, ''), coalesce($5::numeric, 0), coalesce($6::uuid[], '{}'), coalesce($7::uuid[], '{}'),
				coalesce($8, 0)) RETURNING id::text`, cid, strings.TrimSpace(*in.Name), nullJSON(i18n), in.Group, in.Price, in.ItemIDs, in.CategoryIDs, in.Sort).Scan(&id); err != nil {
				return nil, err
			}
		} else {
			tag, err := tx.Exec(ctx, `UPDATE rest_modifiers SET name = coalesce(nullif(trim($3), ''), name), name_i18n = coalesce($4::jsonb, name_i18n),
				grp = coalesce($5, grp), price = coalesce($6::numeric, price), item_ids = coalesce($7::uuid[], item_ids),
				category_ids = coalesce($8::uuid[], category_ids), sort = coalesce($9, sort), is_active = coalesce($10, is_active)
				WHERE id = $1 AND company_id = $2`, id, cid, in.Name, nullJSON(i18n), in.Group, in.Price, in.ItemIDs, in.CategoryIDs, in.Sort, in.IsActive)
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() == 0 {
				return nil, notFound()
			}
		}
		return queryJSONOne(ctx, tx, `SELECT `+modifierCols+` FROM rest_modifiers WHERE id = $1`, id)
	})
}

func (s *Server) restDeleteModifier(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		tag, err := tx.Exec(ctx, `DELETE FROM rest_modifiers WHERE id = $1 AND company_id = $2`, id, cid)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return map[string]bool{"ok": true}, nil
	})
}

// ---------- settings ----------

func (s *Server) restSettings(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	if r.Method == http.MethodPut {
		var in struct {
			ServiceChargePct string `json:"service_charge_pct"`
		}
		if !decodeBody(w, r, &in) {
			return
		}
		if p, ok := money(in.ServiceChargePct); !ok || p.IsNegative() || p.GreaterThan(decimal.NewFromInt(50)) {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
		s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
			if _, err := tx.Exec(ctx, `UPDATE companies SET service_charge_pct = $2::numeric WHERE id = $1`, cid, in.ServiceChargePct); err != nil {
				return nil, err
			}
			return queryJSONOne(ctx, tx, `SELECT trim_scale(service_charge_pct)::text AS service_charge_pct FROM companies WHERE id = $1`, cid)
		})
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSONOne(ctx, tx, `SELECT trim_scale(service_charge_pct)::text AS service_charge_pct FROM companies WHERE id = $1`, cid)
	})
}

// ---------- checks ----------

const checkCols = `k.id, k.number, k.register_id, g.name AS register, g.currency, k.table_id, t.name AS table_name, t.area, k.kind, k.status, k.guests,
	k.customer_id, c.name AS customer_name, k.name, k.phone, k.address, k.notes, k.waiter_id, u.full_name AS waiter, k.pos_order_ids, k.opened_at, k.closed_at,
	coalesce((SELECT sum(l.quantity * l.unit_price) FROM rest_check_lines l WHERE l.check_id = k.id AND l.kstatus <> 'void'), 0)::text AS total,
	coalesce((SELECT sum(l.quantity * l.unit_price) FROM rest_check_lines l WHERE l.check_id = k.id AND l.kstatus <> 'void' AND l.pos_order_id IS NULL), 0)::text AS unpaid,
	(SELECT count(*) FROM rest_check_lines l WHERE l.check_id = k.id AND l.kstatus = 'ready') AS ready,
	(SELECT count(*) FROM rest_check_lines l WHERE l.check_id = k.id AND l.kstatus = 'new') AS unsent`

const checkFrom = ` FROM rest_checks k JOIN pos_registers g ON g.id = k.register_id LEFT JOIN rest_tables t ON t.id = k.table_id
	LEFT JOIN customers c ON c.id = k.customer_id LEFT JOIN users u ON u.id = k.waiter_id`

func loadCheck(ctx context.Context, tx pgx.Tx, cid, id string) (any, error) {
	k, err := queryJSONOne(ctx, tx, `SELECT `+checkCols+checkFrom+` WHERE k.id = $1 AND k.company_id = $2`, id, cid)
	if err != nil {
		return nil, err
	}
	lines, err := queryJSON(ctx, tx, `SELECT l.id, l.item_id, it.code AS item_code, it.name AS item_name, it.name_i18n AS item_name_i18n, l.quantity::text AS quantity,
		l.unit_price::text AS unit_price, (l.quantity * l.unit_price)::text AS amount, l.modifiers, l.note, l.station, l.kstatus, l.sent_at, l.ready_at, l.served_at,
		l.pos_order_id, o.number AS pos_order_number, l.created_at
		FROM rest_check_lines l JOIN items it ON it.id = l.item_id LEFT JOIN pos_orders o ON o.id = l.pos_order_id WHERE l.check_id = $1 ORDER BY l.created_at, l.id`, id)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"check": k, "lines": lines}, nil
}

// GET /rest/floor?register_id= : the tables with their open check, and the open takeaway / delivery checks.
func (s *Server) restFloor(w http.ResponseWriter, r *http.Request) {
	reg := r.URL.Query().Get("register_id")
	if reg != "" && !isUUID(reg) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		tables, err := queryJSON(ctx, tx, `SELECT t.id, t.name, t.area, t.seats, t.x, t.y, t.shape,
			(SELECT row_to_json(oc) FROM (SELECT `+checkCols+checkFrom+` WHERE k.table_id = t.id AND k.status = 'open' ORDER BY k.opened_at LIMIT 1) oc) AS open_check
			FROM rest_tables t WHERE t.company_id = $1 AND t.is_active ORDER BY t.area, t.name`, cid)
		if err != nil {
			return nil, err
		}
		others, err := queryJSON(ctx, tx, `SELECT `+checkCols+checkFrom+` WHERE k.company_id = $1 AND k.status = 'open' AND k.table_id IS NULL
			AND ($2 = '' OR k.register_id = $2::uuid) ORDER BY k.opened_at`, cid, reg)
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"tables": tables, "others": others}, nil
	})
}

// GET /rest/checks?status=open|paid|cancelled|all&date=
func (s *Server) restChecks(w http.ResponseWriter, r *http.Request) {
	status, date := r.URL.Query().Get("status"), r.URL.Query().Get("date")
	if status == "" {
		status = "open"
	}
	if (status != "all" && status != "open" && status != "paid" && status != "cancelled") || (date != "" && !validDate(date)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+checkCols+checkFrom+` WHERE k.company_id = $1 AND ($2 = 'all' OR k.status = $2)
			AND ($3 = '' OR k.opened_at::date = $3::date) ORDER BY k.opened_at DESC LIMIT 500`, r.PathValue("cid"), status, date)
	})
}

func (s *Server) restGetCheck(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) { return loadCheck(ctx, tx, cid, id) })
}

type checkIn struct {
	RegisterID *string `json:"register_id"`
	TableID    *string `json:"table_id"`
	Kind       *string `json:"kind"`
	Guests     *int    `json:"guests"`
	CustomerID *string `json:"customer_id"`
	Name       *string `json:"name"`
	Phone      *string `json:"phone"`
	Address    *string `json:"address"`
	Notes      *string `json:"notes"`
}

func (in *checkIn) valid() bool {
	return optUUID(in.RegisterID) && optUUID(in.TableID) && optUUID(in.CustomerID) &&
		(in.Kind == nil || *in.Kind == "dine_in" || *in.Kind == "takeaway" || *in.Kind == "delivery") &&
		(in.Guests == nil || (*in.Guests >= 1 && *in.Guests <= 500)) && len(deref(in.Name)) <= 120 && len(deref(in.Phone)) <= 40 &&
		len(deref(in.Address)) <= 500 && len(deref(in.Notes)) <= 500
}

func checkTableFree(ctx context.Context, tx pgx.Tx, cid, table, except string) error {
	var active, busy bool
	err := tx.QueryRow(ctx, `SELECT t.is_active, EXISTS (SELECT 1 FROM rest_checks k WHERE k.table_id = t.id AND k.status = 'open' AND k.id::text <> $3)
		FROM rest_tables t WHERE t.id = $1 AND t.company_id = $2`, table, cid, except).Scan(&active, &busy)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !active) {
		return badRequest("invalid_table")
	}
	if err != nil {
		return err
	}
	if busy {
		return &apiError{status: http.StatusConflict, code: "table_busy"}
	}
	return nil
}

// POST /rest/checks {register_id, table_id, kind, guests, customer_id, name, phone, address, notes}
func (s *Server) restOpenCheck(w http.ResponseWriter, r *http.Request) {
	var in checkIn
	if !decodeBody(w, r, &in) {
		return
	}
	if !in.valid() || deref(in.RegisterID) == "" {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	kind := deref(in.Kind)
	if kind == "" {
		kind = "takeaway"
		if deref(in.TableID) != "" {
			kind = "dine_in"
		}
	}
	if kind == "dine_in" && deref(in.TableID) == "" {
		writeErr(w, http.StatusBadRequest, "table_required")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := loadRegister(ctx, tx, cid, *in.RegisterID); err != nil {
			return nil, err
		}
		if t := deref(in.TableID); t != "" {
			if _, err := tx.Exec(ctx, `SELECT 1 FROM rest_tables WHERE id = $1 FOR UPDATE`, t); err != nil {
				return nil, err
			}
			if err := checkTableFree(ctx, tx, cid, t, ""); err != nil {
				return nil, err
			}
		}
		if c := deref(in.CustomerID); c != "" {
			if _, err := lockCustomer(ctx, tx, cid, c); err != nil {
				return nil, err
			}
		}
		var n int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'rest_check')`, cid).Scan(&n); err != nil {
			return nil, err
		}
		guests := 1
		if in.Guests != nil {
			guests = *in.Guests
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO rest_checks (company_id, number, register_id, table_id, kind, guests, customer_id, name, phone, address, notes)
			VALUES ($1, $2, $3, nullif($4, '')::uuid, $5, $6, nullif($7, '')::uuid, $8, $9, $10, $11) RETURNING id::text`,
			cid, n, *in.RegisterID, deref(in.TableID), kind, guests, deref(in.CustomerID), strings.TrimSpace(deref(in.Name)), strings.TrimSpace(deref(in.Phone)),
			strings.TrimSpace(deref(in.Address)), strings.TrimSpace(deref(in.Notes))).Scan(&id); err != nil {
			return nil, err
		}
		return loadCheck(ctx, tx, cid, id)
	})
}

func lockOpenCheck(ctx context.Context, tx pgx.Tx, cid, id string) (register, currency string, number int64, table *string, err error) {
	if !isUUID(id) {
		return "", "", 0, nil, notFound()
	}
	var status string
	err = tx.QueryRow(ctx, `SELECT k.status, k.register_id::text, g.currency::text, k.number, t.name FROM rest_checks k JOIN pos_registers g ON g.id = k.register_id
		LEFT JOIN rest_tables t ON t.id = k.table_id WHERE k.id = $1 AND k.company_id = $2 FOR UPDATE OF k`, id, cid).Scan(&status, &register, &currency, &number, &table)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", 0, nil, notFound()
	}
	if err != nil {
		return "", "", 0, nil, err
	}
	if status != "open" {
		return "", "", 0, nil, &apiError{status: http.StatusConflict, code: "check_closed"}
	}
	return register, currency, number, table, nil
}

// PATCH /rest/checks/{id}: move to another table, guests, customer, delivery details.
func (s *Server) restUpdateCheck(w http.ResponseWriter, r *http.Request) {
	var in checkIn
	if !decodeBody(w, r, &in) {
		return
	}
	if !in.valid() || in.RegisterID != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, _, _, _, err := lockOpenCheck(ctx, tx, cid, id); err != nil {
			return nil, err
		}
		if t := deref(in.TableID); t != "" {
			if err := checkTableFree(ctx, tx, cid, t, id); err != nil {
				return nil, err
			}
		}
		if c := deref(in.CustomerID); c != "" {
			if _, err := lockCustomer(ctx, tx, cid, c); err != nil {
				return nil, err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE rest_checks SET table_id = CASE WHEN $2::boolean THEN nullif($3, '')::uuid ELSE table_id END,
			kind = coalesce($4, kind), guests = coalesce($5, guests), customer_id = CASE WHEN $6::boolean THEN nullif($7, '')::uuid ELSE customer_id END,
			name = coalesce($8, name), phone = coalesce($9, phone), address = coalesce($10, address), notes = coalesce($11, notes) WHERE id = $1`,
			id, in.TableID != nil, deref(in.TableID), in.Kind, in.Guests, in.CustomerID != nil, deref(in.CustomerID), in.Name, in.Phone, in.Address, in.Notes); err != nil {
			return nil, err
		}
		return loadCheck(ctx, tx, cid, id)
	})
}

// POST /rest/checks/{id}/lines {lines: [{item_id, quantity, modifier_ids, note}], send}
func (s *Server) restAddLines(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Lines []struct {
			ItemID      string   `json:"item_id"`
			Quantity    string   `json:"quantity"`
			ModifierIDs []string `json:"modifier_ids"`
			Note        string   `json:"note"`
		} `json:"lines"`
		Send bool `json:"send"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if len(in.Lines) == 0 || len(in.Lines) > 100 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		_, cur, _, _, err := lockOpenCheck(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		rc := newRateCache(ctx, tx, cid, set.Base, today())
		dec, err := rc.decimals(cur)
		if err != nil {
			return nil, err
		}
		for _, l := range in.Lines {
			q, ok := money(l.Quantity)
			if !ok || !q.IsPositive() || !isUUID(l.ItemID) || len(l.Note) > 200 || len(l.ModifierIDs) > 20 || !allUUID(l.ModifierIDs) {
				return nil, badRequest("invalid_line")
			}
			var listPrice *string
			var salePrice, saleCur, station string
			var sellable bool
			err := tx.QueryRow(ctx, `SELECT ip.price::text, it.sale_price::text, coalesce(it.sale_currency, $3)::text, it.kitchen_station, it.is_sellable AND it.is_active
				FROM items it LEFT JOIN item_prices ip ON ip.item_id = it.id AND ip.currency = $4 WHERE it.id = $1 AND it.company_id = $2`,
				l.ItemID, cid, set.Base, cur).Scan(&listPrice, &salePrice, &saleCur, &station, &sellable)
			if errors.Is(err, pgx.ErrNoRows) || (err == nil && !sellable) {
				return nil, badRequest("invalid_item")
			}
			if err != nil {
				return nil, err
			}
			price, err := itemPriceIn(rc, listPrice, salePrice, saleCur, cur)
			if err != nil {
				return nil, err
			}
			mods := []map[string]string{}
			for _, mid := range l.ModifierIDs {
				var name, mp string
				err := tx.QueryRow(ctx, `SELECT m.name, m.price::text FROM rest_modifiers m WHERE m.id = $1 AND m.company_id = $2 AND m.is_active
					AND ((cardinality(m.item_ids) = 0 AND cardinality(m.category_ids) = 0) OR $3::uuid = ANY (m.item_ids)
					  OR (SELECT category_id FROM items WHERE id = $3::uuid) = ANY (m.category_ids))`, mid, cid, l.ItemID).Scan(&name, &mp)
				if errors.Is(err, pgx.ErrNoRows) {
					return nil, badRequest("invalid_modifier")
				}
				if err != nil {
					return nil, err
				}
				p, _ := decimal.NewFromString(mp)
				pc, err := rc.convert(p, set.Base, cur)
				if err != nil {
					return nil, err
				}
				pc = pc.Round(dec)
				price = price.Add(pc)
				mods = append(mods, map[string]string{"id": mid, "name": name, "price": pc.String()})
			}
			if station == "" {
				station = "kitchen"
			}
			mb, _ := json.Marshal(mods)
			kst := "new"
			if in.Send {
				kst = "sent"
			}
			if _, err := tx.Exec(ctx, `INSERT INTO rest_check_lines (company_id, check_id, item_id, quantity, unit_price, modifiers, note, station, kstatus, sent_at)
				VALUES ($1, $2, $3, $4::numeric, $5::numeric, $6, $7, $8, $9, CASE WHEN $9 = 'sent' THEN now() END)`,
				cid, id, l.ItemID, q.String(), price.Round(dec).String(), mb, strings.TrimSpace(l.Note), station, kst); err != nil {
				return nil, err
			}
		}
		return loadCheck(ctx, tx, cid, id)
	})
}

// POST /rest/checks/{id}/send : the new lines go to the kitchen.
func (s *Server) restSend(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, _, _, _, err := lockOpenCheck(ctx, tx, cid, id); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE rest_check_lines SET kstatus = 'sent', sent_at = now() WHERE check_id = $1 AND kstatus = 'new'`, id); err != nil {
			return nil, err
		}
		return loadCheck(ctx, tx, cid, id)
	})
}

func lockCheckLine(ctx context.Context, tx pgx.Tx, cid, id string) (check, kstatus string, paid bool, err error) {
	if !isUUID(id) {
		return "", "", false, notFound()
	}
	err = tx.QueryRow(ctx, `SELECT check_id::text, kstatus, pos_order_id IS NOT NULL FROM rest_check_lines WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).
		Scan(&check, &kstatus, &paid)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", false, notFound()
	}
	return check, kstatus, paid, err
}

// POST /rest/check-lines/{id}/status {status: cooking | ready | served | sent}: the kitchen / the waiter moves a line on.
func (s *Server) restLineStatus(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Status string `json:"status"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		check, kst, _, err := lockCheckLine(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		if !contains(kitchenFlow[kst], in.Status) {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status", detail: kst + " → " + in.Status}
		}
		if _, err := tx.Exec(ctx, `UPDATE rest_check_lines SET kstatus = $2, ready_at = CASE WHEN $2 = 'ready' THEN now() ELSE ready_at END,
			served_at = CASE WHEN $2 = 'served' THEN now() ELSE served_at END WHERE id = $1`, id, in.Status); err != nil {
			return nil, err
		}
		return loadCheck(ctx, tx, cid, check)
	})
}

// POST /rest/check-lines/{id}/void {reason}: a line not yet paid comes off the check (once sent to the kitchen it
// needs the void right).
func (s *Server) restVoidLine(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Reason string `json:"reason"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		check, kst, paid, err := lockCheckLine(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		if _, _, _, _, err := lockOpenCheck(ctx, tx, cid, check); err != nil {
			return nil, err
		}
		if paid || kst == "void" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if kst != "new" && !can(r, "pos.void") {
			return nil, &apiError{status: http.StatusForbidden, code: "forbidden"}
		}
		if _, err := tx.Exec(ctx, `UPDATE rest_check_lines SET kstatus = 'void', note = left(trim(note || ' · void ' || $2), 200) WHERE id = $1`,
			id, strings.TrimSpace(in.Reason)); err != nil {
			return nil, err
		}
		return loadCheck(ctx, tx, cid, check)
	})
}

// GET /rest/kds?station= : what the kitchen has to make (sent, cooking, ready), oldest first.
func (s *Server) restKDS(w http.ResponseWriter, r *http.Request) {
	station := strings.TrimSpace(r.URL.Query().Get("station"))
	if len(station) > 40 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		lines, err := queryJSON(ctx, tx, `SELECT l.id, l.check_id, k.number AS check_number, k.kind, t.name AS table_name, k.name AS customer, it.name AS item_name,
			it.name_i18n AS item_name_i18n, l.quantity::text AS quantity, l.modifiers, l.note, l.station, l.kstatus, l.sent_at, l.ready_at,
			extract(epoch FROM now() - l.sent_at)::int AS age_s
			FROM rest_check_lines l JOIN rest_checks k ON k.id = l.check_id JOIN items it ON it.id = l.item_id LEFT JOIN rest_tables t ON t.id = k.table_id
			WHERE l.company_id = $1 AND l.kstatus IN ('sent', 'cooking', 'ready') AND ($2 = '' OR l.station = $2) AND k.status <> 'cancelled'
			ORDER BY l.sent_at, l.created_at LIMIT 500`, cid, station)
		if err != nil {
			return nil, err
		}
		stations, err := queryJSON(ctx, tx, `SELECT DISTINCT coalesce(nullif(kitchen_station, ''), 'kitchen') AS station FROM items
			WHERE company_id = $1 AND is_active AND is_sellable ORDER BY 1`, cid)
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"lines": lines, "stations": stations}, nil
	})
}

// POST /rest/checks/{id}/split {line_ids}: the lines move to a new check on the same table (pay separately).
func (s *Server) restSplit(w http.ResponseWriter, r *http.Request) {
	var in struct {
		LineIDs []string `json:"line_ids"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if len(in.LineIDs) == 0 || len(in.LineIDs) > 200 || !allUUID(in.LineIDs) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		reg, _, _, _, err := lockOpenCheck(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		var movable, left int
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE id = ANY ($2::uuid[])), count(*) FILTER (WHERE NOT (id = ANY ($2::uuid[])))
			FROM rest_check_lines WHERE check_id = $1 AND kstatus <> 'void' AND pos_order_id IS NULL`, id, in.LineIDs).Scan(&movable, &left); err != nil {
			return nil, err
		}
		if movable != len(in.LineIDs) || left == 0 {
			return nil, badRequest("invalid_split")
		}
		var n int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'rest_check')`, cid).Scan(&n); err != nil {
			return nil, err
		}
		var nid string
		if err := tx.QueryRow(ctx, `INSERT INTO rest_checks (company_id, number, register_id, table_id, kind, guests, customer_id, name, phone, address, notes)
			SELECT company_id, $2, $3, table_id, kind, 1, customer_id, name, phone, address, 'split from #' || number FROM rest_checks WHERE id = $1
			RETURNING id::text`, id, n, reg).Scan(&nid); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE rest_check_lines SET check_id = $2 WHERE check_id = $1 AND id = ANY ($3::uuid[])`, id, nid, in.LineIDs); err != nil {
			return nil, err
		}
		return loadCheck(ctx, tx, cid, nid)
	})
}

// POST /rest/checks/{id}/pay {session_id, line_ids (none = all unpaid), tenders, change_method_id, customer_id, on_account,
// service_charge, tip, client_ref}: a till sale of the lines (modifier prices included); the check closes when nothing is
// left to pay.
func (s *Server) restPay(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SessionID      string     `json:"session_id"`
		LineIDs        []string   `json:"line_ids"`
		Tenders        []tenderIn `json:"tenders"`
		ChangeMethodID string     `json:"change_method_id"`
		CustomerID     string     `json:"customer_id"`
		OnAccount      bool       `json:"on_account"`
		ServiceCharge  bool       `json:"service_charge"`
		Tip            string     `json:"tip"`
		ClientRef      string     `json:"client_ref"`
		ApplyPromos    bool       `json:"apply_promos"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if !isUUID(in.SessionID) || len(in.LineIDs) > 200 || !allUUID(in.LineIDs) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if ref := strings.TrimSpace(in.ClientRef); ref != "" { // sent again (till offline): already paid
			var done bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pos_orders WHERE company_id = $1 AND client_ref = $2 AND check_id = $3)`, cid, ref, id).Scan(&done); err != nil {
				return nil, err
			}
			if done {
				return loadCheck(ctx, tx, cid, id)
			}
		}
		reg, _, number, table, err := lockOpenCheck(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		var sessReg string
		if err := tx.QueryRow(ctx, `SELECT register_id::text FROM pos_sessions WHERE id = $1 AND company_id = $2`, in.SessionID, cid).Scan(&sessReg); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, notFound()
			}
			return nil, err
		}
		if sessReg != reg {
			return nil, badRequest("wrong_register")
		}
		var customer *string
		if err := tx.QueryRow(ctx, `SELECT customer_id::text FROM rest_checks WHERE id = $1`, id).Scan(&customer); err != nil {
			return nil, err
		}
		rows, err := tx.Query(ctx, `SELECT l.id::text, l.item_id::text, l.quantity::text, l.unit_price::text, l.modifiers, l.note FROM rest_check_lines l
			WHERE l.check_id = $1 AND l.kstatus <> 'void' AND l.pos_order_id IS NULL AND (coalesce(cardinality($2::uuid[]), 0) = 0 OR l.id = ANY ($2::uuid[]))
			ORDER BY l.created_at, l.id`, id, in.LineIDs)
		if err != nil {
			return nil, err
		}
		type pl struct {
			id   string
			line posLineIn
		}
		pls, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (pl, error) {
			var x pl
			var mods []byte
			var note string
			err := row.Scan(&x.id, &x.line.ItemID, &x.line.Quantity, &x.line.UnitPrice, &mods, &note)
			var ms []struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(mods, &ms)
			parts := []string{}
			for _, m := range ms {
				parts = append(parts, m.Name)
			}
			if note != "" {
				parts = append(parts, note)
			}
			x.line.Note = strings.Join(parts, ", ")
			x.line.trusted = true
			return x, err
		})
		if err != nil {
			return nil, err
		}
		if len(pls) == 0 || (len(in.LineIDs) > 0 && len(pls) != len(in.LineIDs)) {
			return nil, badRequest("nothing_to_pay")
		}
		cust := in.CustomerID
		if cust == "" && customer != nil {
			cust = *customer
		}
		tbl := ""
		if table != nil {
			tbl = *table
		}
		po := posOrderIn{ClientRef: in.ClientRef, SessionID: in.SessionID, CustomerID: cust, Tenders: in.Tenders, ChangeMethodID: in.ChangeMethodID,
			OnAccount: in.OnAccount, ServiceCharge: in.ServiceCharge, Tip: in.Tip, ApplyPromos: in.ApplyPromos,
			Notes: strings.TrimSpace(fmt.Sprintf("Check #%d %s", number, tbl)), checkID: id, table: tbl}
		ids := []string{}
		for _, x := range pls {
			po.Lines = append(po.Lines, x.line)
			ids = append(ids, x.id)
		}
		if !po.valid() {
			return nil, badRequest("bad_request")
		}
		order, err := createPosOrderTx(ctx, tx, r, cid, &po)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE rest_check_lines SET pos_order_id = $2 WHERE id = ANY ($1::uuid[])`, ids, order); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE rest_checks SET pos_order_ids = pos_order_ids || $2::uuid,
			status = CASE WHEN EXISTS (SELECT 1 FROM rest_check_lines WHERE check_id = $1 AND kstatus <> 'void' AND pos_order_id IS NULL) THEN status ELSE 'paid' END,
			closed_at = CASE WHEN EXISTS (SELECT 1 FROM rest_check_lines WHERE check_id = $1 AND kstatus <> 'void' AND pos_order_id IS NULL) THEN closed_at ELSE now() END
			WHERE id = $1`, id, order); err != nil {
			return nil, err
		}
		out, err := loadCheck(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		receipt, err := loadPosOrder(ctx, tx, cid, order)
		if err != nil {
			return nil, err
		}
		m := out.(map[string]json.RawMessage)
		rb, _ := json.Marshal(receipt)
		m["pos_order"] = rb
		return m, nil
	})
}

// POST /rest/checks/{id}/cancel {reason}: a check with nothing paid (lines sent to the kitchen need the void right).
func (s *Server) restCancel(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Reason string `json:"reason"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, _, _, _, err := lockOpenCheck(ctx, tx, cid, id); err != nil {
			return nil, err
		}
		var paid, sent bool
		if err := tx.QueryRow(ctx, `SELECT bool_or(pos_order_id IS NOT NULL), bool_or(kstatus NOT IN ('new', 'void')) FROM rest_check_lines WHERE check_id = $1`,
			id).Scan(&paid, &sent); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if paid {
			return nil, &apiError{status: http.StatusConflict, code: "check_partly_paid"}
		}
		if sent && !can(r, "pos.void") {
			return nil, &apiError{status: http.StatusForbidden, code: "forbidden"}
		}
		if _, err := tx.Exec(ctx, `UPDATE rest_check_lines SET kstatus = 'void' WHERE check_id = $1 AND kstatus <> 'void'`, id); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE rest_checks SET status = 'cancelled', closed_at = now(), notes = left(trim(notes || ' · ' || $2), 500) WHERE id = $1`,
			id, strings.TrimSpace(in.Reason)); err != nil {
			return nil, err
		}
		return loadCheck(ctx, tx, cid, id)
	})
}

// GET /rest/food-cost?from=&to= : per day, food sold (item lines of till sales, base currency, before tax) against
// the cost of what left stock for them (ingredients of the dishes included).
func (s *Server) restFoodCost(w http.ResponseWriter, r *http.Request) {
	from, to, ok := dateRange(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid_date")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		days, err := queryJSON(ctx, tx, `WITH o AS (
			  SELECT po.order_date AS day, po.invoice_id,
			    (SELECT jl.base_debit / nullif(jl.debit, 0) FROM journal_lines jl JOIN invoices i ON jl.entry_id = i.journal_entry_id
			     WHERE i.id = po.invoice_id AND jl.debit > 0 ORDER BY jl.line_no LIMIT 1) AS rate
			  FROM pos_orders po WHERE po.company_id = $1 AND po.kind = 'sale' AND po.order_date BETWEEN $2::date AND $3::date),
			d AS (SELECT o.day, count(*) AS orders,
			  sum(coalesce((SELECT sum(l.amount) FROM invoice_lines l WHERE l.invoice_id = o.invoice_id AND l.item_id IS NOT NULL), 0) * coalesce(o.rate, 1)) AS sales,
			  sum(coalesce((SELECT -sum(m.value) FROM stock_moves m WHERE m.source = 'sale' AND m.source_id = o.invoice_id), 0)) AS cost
			  FROM o GROUP BY o.day)
			SELECT day, orders, round(sales, 2)::text AS food_sales, round(cost, 2)::text AS food_cost,
			  CASE WHEN sales > 0 THEN round(cost / sales * 100, 1) END::text AS food_cost_pct FROM d ORDER BY day`, cid, from, to)
		if err != nil {
			return nil, err
		}
		dishes, err := queryJSON(ctx, tx, `SELECT it.id AS item_id, it.name, sum(l.quantity)::text AS quantity,
			round(sum(l.amount * coalesce((SELECT jl.base_debit / nullif(jl.debit, 0) FROM journal_lines jl WHERE jl.entry_id = i.journal_entry_id AND jl.debit > 0
			  ORDER BY jl.line_no LIMIT 1), 1)), 2)::text AS sales,
			round(coalesce((SELECT sum(c.quantity * coalesce(s.avg_cost, 0)) FROM item_components c LEFT JOIN LATERAL (SELECT avg(avg_cost) AS avg_cost FROM stock
			  WHERE item_id = c.component_id) s ON true WHERE c.kit_id = it.id), 0), 4)::text AS recipe_cost
			FROM pos_orders po JOIN invoices i ON i.id = po.invoice_id JOIN invoice_lines l ON l.invoice_id = i.id JOIN items it ON it.id = l.item_id
			WHERE po.company_id = $1 AND po.kind = 'sale' AND po.order_date BETWEEN $2::date AND $3::date AND it.kind = 'kit'
			GROUP BY it.id, it.name ORDER BY sum(l.amount) DESC LIMIT 100`, cid, from, to)
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"days": days, "dishes": dishes}, nil
	})
}
