package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Purchase tenders and blanket agreements (phase 8).
//
// A tender asks several suppliers for prices on a list of lines; each supplier's bid is entered (price per line, the
// quantity offered, lead time); the comparison shows the best price per line and the total per supplier; the award
// gives each line (or the whole tender) to a bid and makes one purchase order per supplier, or a blanket agreement.
//
// A blanket agreement fixes the price (and the quantity committed) with one supplier for a period. Purchase orders are
// drawn against it at the agreed price; each line shows what was ordered and what remains.

// createPOTx makes a purchase order (confirmed when confirm) and returns its id.
func createPOTx(ctx context.Context, tx pgx.Tx, cid string, in poIn, confirm bool, tender, agreement string) (string, error) {
	if !in.valid() {
		return "", badRequest("bad_request")
	}
	if _, err := lockSupplier(ctx, tx, cid, in.SupplierID); err != nil {
		return "", err
	}
	var n int64
	if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'purchase_order')`, cid).Scan(&n); err != nil {
		return "", err
	}
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO purchase_orders (company_id, number, supplier_id, order_date, expected_date, currency, supplier_ref,
		notes, cost_center_id, tender_id, agreement_id) VALUES ($1, $2, $3, $4::date, nullif($5, '')::date, $6, $7, $8, nullif($9, '')::uuid,
		nullif($10, '')::uuid, nullif($11, '')::uuid) RETURNING id::text`,
		cid, n, in.SupplierID, in.OrderDate, in.ExpectedDate, in.Currency, strings.TrimSpace(in.SupplierRef), strings.TrimSpace(in.Notes),
		in.CostCenterID, tender, agreement).Scan(&id); err != nil {
		return "", err
	}
	if err := writePOLines(ctx, tx, cid, id, in.Currency, in.Lines); err != nil {
		return "", err
	}
	if confirm {
		if _, err := tx.Exec(ctx, `UPDATE purchase_orders SET status = 'confirmed', confirmed_at = now(), updated_at = now() WHERE id = $1`, id); err != nil {
			return "", err
		}
	}
	return id, nil
}

func nextNumber(ctx context.Context, tx pgx.Tx, table, cid string) (int64, error) {
	var n int64
	// one number at a time per company (the unique index catches the rest)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1 || $2))`, table, cid); err != nil {
		return 0, err
	}
	err := tx.QueryRow(ctx, `SELECT coalesce(max(number), 0) + 1 FROM `+table+` WHERE company_id = $1`, cid).Scan(&n)
	return n, err
}

// ======================= tenders =======================

func (s *Server) listTenders(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT t.id, t.number, t.title, t.currency, t.deadline, t.status, t.created_at,
			(SELECT count(*) FROM tender_lines l WHERE l.tender_id = t.id) AS lines,
			(SELECT count(*) FROM tender_bids b WHERE b.tender_id = t.id) AS bids,
			(SELECT count(*) FROM purchase_orders o WHERE o.tender_id = t.id) AS orders
			FROM purchase_tenders t WHERE t.company_id = $1 ORDER BY t.number DESC LIMIT 500`, r.PathValue("cid"))
	})
}

type tenderLineIn struct {
	ItemID      string `json:"item_id"`
	Description string `json:"description"`
	Quantity    string `json:"quantity"`
}

type ptenderIn struct {
	Title    *string        `json:"title"`
	Currency *string        `json:"currency"`
	Deadline *string        `json:"deadline"`
	Notes    *string        `json:"notes"`
	Lines    []tenderLineIn `json:"lines"`
}

// POST /purchasing/tenders and PUT /purchasing/tenders/{id}. Lines can change while no bid came in.
func (s *Server) saveTender(w http.ResponseWriter, r *http.Request) {
	var in ptenderIn
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	creating := id == ""
	bad := (creating && (strings.TrimSpace(deref(in.Title)) == "" || in.Currency == nil || len(in.Lines) == 0)) ||
		(in.Title != nil && (strings.TrimSpace(*in.Title) == "" || len(*in.Title) > 200)) || (in.Currency != nil && !currencyRe.MatchString(*in.Currency)) ||
		(in.Deadline != nil && *in.Deadline != "" && !validDate(*in.Deadline)) || len(deref(in.Notes)) > 2000 || len(in.Lines) > 200 || (!creating && !isUUID(id))
	for _, l := range in.Lines {
		q, ok := money(l.Quantity)
		if !ok || !q.IsPositive() || (l.ItemID != "" && !isUUID(l.ItemID)) || (l.ItemID == "" && strings.TrimSpace(l.Description) == "") || len(l.Description) > 300 {
			bad = true
		}
	}
	if bad {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	status := http.StatusOK
	if creating {
		status = http.StatusCreated
	}
	s.run(w, r, status, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if creating {
			n, err := nextNumber(ctx, tx, "purchase_tenders", cid)
			if err != nil {
				return nil, err
			}
			if err := tx.QueryRow(ctx, `INSERT INTO purchase_tenders (company_id, number, title, currency) VALUES ($1, $2, trim($3), $4) RETURNING id::text`,
				cid, n, *in.Title, *in.Currency).Scan(&id); err != nil {
				return nil, err
			}
		}
		var st string
		var bids int
		err := tx.QueryRow(ctx, `SELECT status, (SELECT count(*) FROM tender_bids b WHERE b.tender_id = t.id) FROM purchase_tenders t
			WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&st, &bids)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if st == "awarded" || st == "cancelled" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if _, err := tx.Exec(ctx, `UPDATE purchase_tenders SET title = coalesce(trim($3), title), currency = coalesce($4, currency),
			deadline = CASE WHEN $5::text IS NULL THEN deadline ELSE nullif($5, '')::date END, notes = coalesce($6, notes), updated_at = now()
			WHERE id = $1 AND company_id = $2`, id, cid, in.Title, in.Currency, in.Deadline, in.Notes); err != nil {
			return nil, err
		}
		if in.Lines != nil {
			if bids > 0 && !creating {
				return nil, &apiError{status: http.StatusConflict, code: "tender_has_bids"}
			}
			if _, err := tx.Exec(ctx, `DELETE FROM tender_lines WHERE tender_id = $1`, id); err != nil {
				return nil, err
			}
			for i, l := range in.Lines {
				desc := strings.TrimSpace(l.Description)
				if l.ItemID != "" {
					it, err := loadItem(ctx, tx, cid, l.ItemID)
					if err != nil {
						return nil, err
					}
					if desc == "" {
						desc = it.Name
					}
				}
				if _, err := tx.Exec(ctx, `INSERT INTO tender_lines (company_id, tender_id, line_no, item_id, description, quantity)
					VALUES ($1, $2, $3, nullif($4, '')::uuid, $5, $6::numeric)`, cid, id, i+1, l.ItemID, desc, l.Quantity); err != nil {
					return nil, err
				}
			}
		}
		return loadTender(ctx, tx, cid, id)
	})
}

func (s *Server) getTender(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return loadTender(ctx, tx, r.PathValue("cid"), r.PathValue("id"))
	})
}

// loadTender: the tender, its lines with the best bid, the bids with their lines and totals.
func loadTender(ctx context.Context, tx pgx.Tx, cid, id string) (any, error) {
	if !isUUID(id) {
		return nil, notFound()
	}
	t, err := queryJSONOne(ctx, tx, `SELECT id, number, title, currency, deadline, status, notes, created_at FROM purchase_tenders
		WHERE id = $1 AND company_id = $2`, id, cid)
	if err != nil {
		return nil, err
	}
	lines, err := queryJSON(ctx, tx, `SELECT l.id, l.line_no, l.item_id, it.code AS item_code, l.description, trim_scale(l.quantity)::text AS quantity,
		(SELECT b.id FROM tender_bid_lines bl JOIN tender_bids b ON b.id = bl.bid_id WHERE bl.tender_line_id = l.id ORDER BY bl.unit_price, b.lead_days NULLS LAST LIMIT 1) AS best_bid_id,
		(SELECT trim_scale(min(bl.unit_price))::text FROM tender_bid_lines bl WHERE bl.tender_line_id = l.id) AS best_price,
		(SELECT trim_scale(sum(bl.awarded_qty))::text FROM tender_bid_lines bl WHERE bl.tender_line_id = l.id) AS awarded_qty
		FROM tender_lines l LEFT JOIN items it ON it.id = l.item_id WHERE l.tender_id = $1 ORDER BY l.line_no`, id)
	if err != nil {
		return nil, err
	}
	bids, err := queryJSON(ctx, tx, `SELECT b.id, b.supplier_id, s.name AS supplier_name, b.lead_days, b.valid_until, b.notes, b.status,
		trim_scale(round(coalesce((SELECT sum(bl.unit_price * coalesce(bl.quantity, l.quantity)) FROM tender_bid_lines bl JOIN tender_lines l ON l.id = bl.tender_line_id
		   WHERE bl.bid_id = b.id), 0), 2))::text AS total,
		(SELECT count(*) FROM tender_bid_lines bl WHERE bl.bid_id = b.id) AS lines_quoted,
		coalesce((SELECT json_agg(json_build_object('tender_line_id', bl.tender_line_id, 'unit_price', trim_scale(bl.unit_price)::text,
		   'quantity', trim_scale(bl.quantity)::text, 'awarded_qty', trim_scale(bl.awarded_qty)::text) ORDER BY l.line_no)
		   FROM tender_bid_lines bl JOIN tender_lines l ON l.id = bl.tender_line_id WHERE bl.bid_id = b.id), '[]') AS lines
		FROM tender_bids b JOIN suppliers s ON s.id = b.supplier_id WHERE b.tender_id = $1 ORDER BY s.name`, id)
	if err != nil {
		return nil, err
	}
	orders, err := queryJSON(ctx, tx, `SELECT o.id, o.number, o.status, o.total::text AS total, s.name AS supplier_name FROM purchase_orders o
		JOIN suppliers s ON s.id = o.supplier_id WHERE o.tender_id = $1 ORDER BY o.number`, id)
	if err != nil {
		return nil, err
	}
	agreements, err := queryJSON(ctx, tx, `SELECT a.id, a.number, a.status, s.name AS supplier_name FROM purchase_agreements a
		JOIN suppliers s ON s.id = a.supplier_id WHERE a.tender_id = $1 ORDER BY a.number`, id)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"tender": t, "lines": lines, "bids": bids, "orders": orders, "agreements": agreements}, nil
}

// POST /purchasing/tenders/{id}/status {status}: draft → open (sent to suppliers) → closed (no more bids); cancelled.
func (s *Server) tenderStatus(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Status string `json:"status"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	from := map[string][]string{"open": {"draft", "closed"}, "closed": {"open"}, "draft": {"open"}, "cancelled": {"draft", "open", "closed"}}[in.Status]
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if from == nil || !isUUID(id) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		tag, err := tx.Exec(ctx, `UPDATE purchase_tenders SET status = $3, updated_at = now() WHERE id = $1 AND company_id = $2 AND status = ANY ($4::text[])`,
			id, cid, in.Status, from)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		return loadTender(ctx, tx, cid, id)
	})
}

// PUT /purchasing/tenders/{id}/bids {supplier_id, lead_days, valid_until, notes, lines: [{tender_line_id, unit_price, quantity}]}:
// the bid of one supplier (replaced when entered again).
func (s *Server) saveBid(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SupplierID string `json:"supplier_id"`
		LeadDays   *int   `json:"lead_days"`
		ValidUntil string `json:"valid_until"`
		Notes      string `json:"notes"`
		Lines      []struct {
			TenderLineID string `json:"tender_line_id"`
			UnitPrice    string `json:"unit_price"`
			Quantity     string `json:"quantity"`
		} `json:"lines"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	bad := !isUUID(id) || !isUUID(in.SupplierID) || (in.ValidUntil != "" && !validDate(in.ValidUntil)) || len(in.Notes) > 1000 || len(in.Lines) > 200 ||
		(in.LeadDays != nil && (*in.LeadDays < 0 || *in.LeadDays > 3650))
	for _, l := range in.Lines {
		p, ok := money(l.UnitPrice)
		q, okq := money(l.Quantity)
		if !isUUID(l.TenderLineID) || !ok || p.IsNegative() || (l.Quantity != "" && (!okq || !q.IsPositive())) {
			bad = true
		}
	}
	if bad {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var st string
		err := tx.QueryRow(ctx, `SELECT status FROM purchase_tenders WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&st)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if st != "open" && st != "closed" && st != "draft" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if _, err := lockSupplier(ctx, tx, cid, in.SupplierID); err != nil {
			return nil, err
		}
		var bid string
		if err := tx.QueryRow(ctx, `INSERT INTO tender_bids (company_id, tender_id, supplier_id, lead_days, valid_until, notes)
			VALUES ($1, $2, $3, $4, nullif($5, '')::date, $6)
			ON CONFLICT (tender_id, supplier_id) DO UPDATE SET lead_days = EXCLUDED.lead_days, valid_until = EXCLUDED.valid_until, notes = EXCLUDED.notes,
			updated_at = now() RETURNING id::text`, cid, id, in.SupplierID, in.LeadDays, in.ValidUntil, strings.TrimSpace(in.Notes)).Scan(&bid); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM tender_bid_lines WHERE bid_id = $1`, bid); err != nil {
			return nil, err
		}
		for _, l := range in.Lines {
			tag, err := tx.Exec(ctx, `INSERT INTO tender_bid_lines (company_id, bid_id, tender_line_id, unit_price, quantity)
				SELECT $1, $2, tl.id, $4::numeric, nullif($5, '')::numeric FROM tender_lines tl WHERE tl.id = $3 AND tl.tender_id = $6`,
				cid, bid, l.TenderLineID, l.UnitPrice, l.Quantity, id)
			if err != nil {
				if isUniqueViolation(err) {
					return nil, badRequest("bad_request")
				}
				return nil, err
			}
			if tag.RowsAffected() == 0 {
				return nil, badRequest("invalid_line")
			}
		}
		return loadTender(ctx, tx, cid, id)
	})
}

// POST /purchasing/tenders/{id}/award {bid_id | lines: [{tender_line_id, bid_id, quantity}], as_agreement, start_date, end_date,
// expected_date, warehouse_id}: each supplier gets one purchase order (confirmed) for its lines, or one blanket agreement.
func (s *Server) awardTender(w http.ResponseWriter, r *http.Request) {
	var in struct {
		BidID string `json:"bid_id"`
		Lines []struct {
			TenderLineID string `json:"tender_line_id"`
			BidID        string `json:"bid_id"`
			Quantity     string `json:"quantity"`
		} `json:"lines"`
		AsAgreement  bool   `json:"as_agreement"`
		StartDate    string `json:"start_date"`
		EndDate      string `json:"end_date"`
		ExpectedDate string `json:"expected_date"`
		WarehouseID  string `json:"warehouse_id"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if in.StartDate == "" {
		in.StartDate = today()
	}
	bad := !isUUID(id) || (in.BidID == "" && len(in.Lines) == 0) || (in.BidID != "" && !isUUID(in.BidID)) || len(in.Lines) > 200 ||
		(in.WarehouseID != "" && !isUUID(in.WarehouseID)) || (in.ExpectedDate != "" && !validDate(in.ExpectedDate)) ||
		(in.AsAgreement && (!validDate(in.StartDate) || !validDate(in.EndDate) || in.EndDate < in.StartDate))
	for _, l := range in.Lines {
		q, ok := money(l.Quantity)
		if !isUUID(l.TenderLineID) || !isUUID(l.BidID) || (l.Quantity != "" && (!ok || !q.IsPositive())) {
			bad = true
		}
	}
	if bad {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var st, cur, title string
		var number int64
		err := tx.QueryRow(ctx, `SELECT status, currency::text, title, number FROM purchase_tenders WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).
			Scan(&st, &cur, &title, &number)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if st != "open" && st != "closed" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		type pick struct{ line, bid, qty string }
		var picks []pick
		if in.BidID != "" {
			rows, err := tx.Query(ctx, `SELECT bl.tender_line_id::text, bl.bid_id::text, '' FROM tender_bid_lines bl JOIN tender_bids b ON b.id = bl.bid_id
				WHERE b.id = $1 AND b.tender_id = $2`, in.BidID, id)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var p pick
				if err := rows.Scan(&p.line, &p.bid, &p.qty); err != nil {
					rows.Close()
					return nil, err
				}
				picks = append(picks, p)
			}
			rows.Close()
			if len(picks) == 0 {
				return nil, badRequest("bid_empty")
			}
		} else {
			for _, l := range in.Lines {
				picks = append(picks, pick{l.TenderLineID, l.BidID, l.Quantity})
			}
		}
		type pl struct {
			item, desc, price, qty string
			blID                   string
		}
		bySup := map[string][]pl{}
		var supOrder []string
		bidOf := map[string]bool{}
		for _, p := range picks {
			var sup, item, desc, price, qty, offered, blID string
			err := tx.QueryRow(ctx, `SELECT b.supplier_id::text, coalesce(tl.item_id::text, ''), tl.description, trim_scale(bl.unit_price)::text,
				trim_scale(tl.quantity)::text, coalesce(trim_scale(bl.quantity)::text, ''), bl.id::text
				FROM tender_bid_lines bl JOIN tender_bids b ON b.id = bl.bid_id JOIN tender_lines tl ON tl.id = bl.tender_line_id
				WHERE bl.bid_id = $1 AND bl.tender_line_id = $2 AND b.tender_id = $3`, p.bid, p.line, id).
				Scan(&sup, &item, &desc, &price, &qty, &offered, &blID)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, badRequest("invalid_line")
			}
			if err != nil {
				return nil, err
			}
			if p.qty != "" {
				qty = p.qty
			} else if offered != "" {
				q1, _ := decimal.NewFromString(qty)
				q2, _ := decimal.NewFromString(offered)
				qty = decimal.Min(q1, q2).String()
			}
			if _, ok := bySup[sup]; !ok {
				supOrder = append(supOrder, sup)
			}
			bySup[sup] = append(bySup[sup], pl{item, desc, price, qty, blID})
			bidOf[p.bid] = true
			if _, err := tx.Exec(ctx, `UPDATE tender_bid_lines SET awarded_qty = awarded_qty + $2::numeric WHERE id = $1`, blID, qty); err != nil {
				return nil, err
			}
		}
		var made []map[string]any
		for _, sup := range supOrder {
			lines := bySup[sup]
			if in.AsAgreement {
				n, err := nextNumber(ctx, tx, "purchase_agreements", cid)
				if err != nil {
					return nil, err
				}
				var aid string
				if err := tx.QueryRow(ctx, `INSERT INTO purchase_agreements (company_id, number, supplier_id, title, currency, start_date, end_date, status, tender_id)
					VALUES ($1, $2, $3, $4, $5, $6::date, $7::date, 'active', $8) RETURNING id::text`, cid, n, sup, title, cur, in.StartDate, in.EndDate, id).Scan(&aid); err != nil {
					return nil, err
				}
				for i, l := range lines {
					if _, err := tx.Exec(ctx, `INSERT INTO purchase_agreement_lines (company_id, agreement_id, line_no, item_id, description, unit_price, quantity)
						VALUES ($1, $2, $3, nullif($4, '')::uuid, $5, $6::numeric, $7::numeric)`, cid, aid, i+1, l.item, l.desc, l.price, l.qty); err != nil {
						return nil, err
					}
				}
				made = append(made, map[string]any{"kind": "agreement", "id": aid, "number": n, "supplier_id": sup})
				continue
			}
			po := poIn{SupplierID: sup, Currency: cur, ExpectedDate: in.ExpectedDate, SupplierRef: "", Notes: "Tender T-" + itoa64(number) + " " + title}
			for _, l := range lines {
				po.Lines = append(po.Lines, poLineIn{ItemID: l.item, Description: l.desc, Quantity: l.qty, UnitPrice: l.price, WarehouseID: in.WarehouseID})
			}
			pid, err := createPOTx(ctx, tx, cid, po, true, id, "")
			if err != nil {
				return nil, err
			}
			made = append(made, map[string]any{"kind": "order", "id": pid, "supplier_id": sup})
		}
		// bids: awarded (every line it quoted won), partial (some), lost
		if _, err := tx.Exec(ctx, `UPDATE tender_bids b SET status = CASE
			WHEN NOT EXISTS (SELECT 1 FROM tender_bid_lines bl WHERE bl.bid_id = b.id AND bl.awarded_qty > 0) THEN 'lost'
			WHEN EXISTS (SELECT 1 FROM tender_bid_lines bl WHERE bl.bid_id = b.id AND bl.awarded_qty = 0) THEN 'partial' ELSE 'awarded' END,
			updated_at = now() WHERE b.tender_id = $1`, id); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE purchase_tenders SET status = 'awarded', updated_at = now() WHERE id = $1`, id); err != nil {
			return nil, err
		}
		t, err := loadTender(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"tender": t, "made": made}, nil
	})
}

func itoa64(n int64) string { return decimal.NewFromInt(n).String() }

// ======================= blanket agreements =======================

const agreementTotals = `
	trim_scale(round(coalesce((SELECT sum(l.unit_price * l.quantity) FROM purchase_agreement_lines l WHERE l.agreement_id = a.id), 0), 2))::text AS committed_value,
	trim_scale(round(coalesce((SELECT sum(pl.unit_price * pl.quantity) FROM purchase_order_lines pl JOIN purchase_orders o ON o.id = pl.order_id
	   JOIN purchase_agreement_lines l ON l.id = pl.agreement_line_id WHERE l.agreement_id = a.id AND o.status <> 'cancelled'), 0), 2))::text AS ordered_value`

func (s *Server) listAgreements(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT a.id, a.number, a.title, a.supplier_id, s.name AS supplier_name, a.currency, a.start_date, a.end_date, a.status,
			(a.status = 'active' AND current_date BETWEEN a.start_date AND a.end_date) AS usable, `+agreementTotals+`
			FROM purchase_agreements a JOIN suppliers s ON s.id = a.supplier_id WHERE a.company_id = $1 ORDER BY a.number DESC LIMIT 500`, r.PathValue("cid"))
	})
}

type agreementIn struct {
	SupplierID *string `json:"supplier_id"`
	Title      *string `json:"title"`
	Currency   *string `json:"currency"`
	StartDate  *string `json:"start_date"`
	EndDate    *string `json:"end_date"`
	Notes      *string `json:"notes"`
	Lines      []struct {
		ItemID      string `json:"item_id"`
		Description string `json:"description"`
		UnitPrice   string `json:"unit_price"`
		Quantity    string `json:"quantity"`
	} `json:"lines"`
}

// POST /purchasing/agreements and PUT /purchasing/agreements/{id}: lines change only while it is a draft; an active
// agreement can still get a later end date and notes.
func (s *Server) saveAgreement(w http.ResponseWriter, r *http.Request) {
	var in agreementIn
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	creating := id == ""
	bad := (creating && (in.SupplierID == nil || strings.TrimSpace(deref(in.Title)) == "" || in.Currency == nil || in.StartDate == nil || in.EndDate == nil || len(in.Lines) == 0)) ||
		(in.SupplierID != nil && !isUUID(*in.SupplierID)) || (in.Title != nil && (strings.TrimSpace(*in.Title) == "" || len(*in.Title) > 200)) ||
		(in.Currency != nil && !currencyRe.MatchString(*in.Currency)) || (in.StartDate != nil && !validDate(*in.StartDate)) ||
		(in.EndDate != nil && !validDate(*in.EndDate)) || len(deref(in.Notes)) > 2000 || len(in.Lines) > 200 || (!creating && !isUUID(id))
	for _, l := range in.Lines {
		p, ok1 := money(l.UnitPrice)
		q, ok2 := money(l.Quantity)
		if !ok1 || !ok2 || p.IsNegative() || !q.IsPositive() || (l.ItemID != "" && !isUUID(l.ItemID)) || (l.ItemID == "" && strings.TrimSpace(l.Description) == "") || len(l.Description) > 300 {
			bad = true
		}
	}
	if bad {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	status := http.StatusOK
	if creating {
		status = http.StatusCreated
	}
	s.run(w, r, status, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if in.SupplierID != nil {
			if _, err := lockSupplier(ctx, tx, cid, *in.SupplierID); err != nil {
				return nil, err
			}
		}
		if creating {
			n, err := nextNumber(ctx, tx, "purchase_agreements", cid)
			if err != nil {
				return nil, err
			}
			if err := tx.QueryRow(ctx, `INSERT INTO purchase_agreements (company_id, number, supplier_id, title, currency, start_date, end_date)
				VALUES ($1, $2, $3, trim($4), $5, $6::date, $7::date) RETURNING id::text`, cid, n, *in.SupplierID, *in.Title, *in.Currency, *in.StartDate, *in.EndDate).Scan(&id); err != nil {
				if strings.Contains(err.Error(), "23514") {
					return nil, badRequest("invalid_date")
				}
				return nil, err
			}
		}
		var st string
		err := tx.QueryRow(ctx, `SELECT status FROM purchase_agreements WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&st)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		draft := st == "draft"
		if !draft && (st != "active" || in.Lines != nil || in.SupplierID != nil || in.Currency != nil || in.StartDate != nil) {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if _, err := tx.Exec(ctx, `UPDATE purchase_agreements SET supplier_id = coalesce($3, supplier_id), title = coalesce(trim($4), title),
			currency = coalesce($5, currency), start_date = coalesce($6::date, start_date), end_date = coalesce($7::date, end_date),
			notes = coalesce($8, notes), updated_at = now() WHERE id = $1 AND company_id = $2`,
			id, cid, in.SupplierID, in.Title, in.Currency, in.StartDate, in.EndDate, in.Notes); err != nil {
			if strings.Contains(err.Error(), "23514") {
				return nil, badRequest("invalid_date")
			}
			return nil, err
		}
		if in.Lines != nil {
			if _, err := tx.Exec(ctx, `DELETE FROM purchase_agreement_lines WHERE agreement_id = $1`, id); err != nil {
				return nil, err
			}
			for i, l := range in.Lines {
				desc := strings.TrimSpace(l.Description)
				if l.ItemID != "" {
					it, err := loadItem(ctx, tx, cid, l.ItemID)
					if err != nil {
						return nil, err
					}
					if desc == "" {
						desc = it.Name
					}
				}
				if _, err := tx.Exec(ctx, `INSERT INTO purchase_agreement_lines (company_id, agreement_id, line_no, item_id, description, unit_price, quantity)
					VALUES ($1, $2, $3, nullif($4, '')::uuid, $5, $6::numeric, $7::numeric)`, cid, id, i+1, l.ItemID, desc, l.UnitPrice, l.Quantity); err != nil {
					return nil, err
				}
			}
		}
		return loadAgreement(ctx, tx, cid, id)
	})
}

func (s *Server) getAgreement(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return loadAgreement(ctx, tx, r.PathValue("cid"), r.PathValue("id"))
	})
}

func loadAgreement(ctx context.Context, tx pgx.Tx, cid, id string) (any, error) {
	if !isUUID(id) {
		return nil, notFound()
	}
	a, err := queryJSONOne(ctx, tx, `SELECT a.id, a.number, a.title, a.supplier_id, s.name AS supplier_name, a.currency, a.start_date, a.end_date,
		a.status, a.notes, a.tender_id, (a.status = 'active' AND current_date BETWEEN a.start_date AND a.end_date) AS usable, `+agreementTotals+`
		FROM purchase_agreements a JOIN suppliers s ON s.id = a.supplier_id WHERE a.id = $1 AND a.company_id = $2`, id, cid)
	if err != nil {
		return nil, err
	}
	lines, err := queryJSON(ctx, tx, `SELECT l.id, l.line_no, l.item_id, it.code AS item_code, l.description, trim_scale(l.unit_price)::text AS unit_price,
		trim_scale(l.quantity)::text AS quantity, trim_scale(o.ordered)::text AS ordered, trim_scale(greatest(l.quantity - o.ordered, 0))::text AS remaining
		FROM purchase_agreement_lines l LEFT JOIN items it ON it.id = l.item_id,
		LATERAL (SELECT coalesce(sum(pl.quantity), 0) AS ordered FROM purchase_order_lines pl JOIN purchase_orders po ON po.id = pl.order_id
		         WHERE pl.agreement_line_id = l.id AND po.status <> 'cancelled') o
		WHERE l.agreement_id = $1 ORDER BY l.line_no`, id)
	if err != nil {
		return nil, err
	}
	orders, err := queryJSON(ctx, tx, `SELECT o.id, o.number, o.order_date, o.status, o.total::text AS total FROM purchase_orders o
		WHERE o.agreement_id = $1 ORDER BY o.number`, id)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"agreement": a, "lines": lines, "orders": orders}, nil
}

// POST /purchasing/agreements/{id}/status {status}: draft → active → closed; cancelled while nothing was ordered.
func (s *Server) agreementStatus(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Status string `json:"status"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	from := map[string][]string{"active": {"draft", "closed"}, "closed": {"active"}, "draft": {"active"}, "cancelled": {"draft", "active"}}[in.Status]
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if from == nil || !isUUID(id) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if in.Status == "cancelled" || in.Status == "draft" {
			var used bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM purchase_orders WHERE agreement_id = $1 AND status <> 'cancelled')`, id).Scan(&used); err != nil {
				return nil, err
			}
			if used {
				return nil, &apiError{status: http.StatusConflict, code: "agreement_used"}
			}
		}
		tag, err := tx.Exec(ctx, `UPDATE purchase_agreements SET status = $3, updated_at = now() WHERE id = $1 AND company_id = $2 AND status = ANY ($4::text[])`,
			id, cid, in.Status, from)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		return loadAgreement(ctx, tx, cid, id)
	})
}

// POST /purchasing/agreements/{id}/order {lines: [{agreement_line_id, quantity}], order_date, expected_date, warehouse_id, allow_over}:
// a confirmed purchase order at the agreed prices. More than what remains is refused unless allow_over.
func (s *Server) orderFromAgreement(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Lines []struct {
			AgreementLineID string `json:"agreement_line_id"`
			Quantity        string `json:"quantity"`
		} `json:"lines"`
		OrderDate    string `json:"order_date"`
		ExpectedDate string `json:"expected_date"`
		WarehouseID  string `json:"warehouse_id"`
		AllowOver    bool   `json:"allow_over"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if in.OrderDate == "" {
		in.OrderDate = today()
	}
	bad := !isUUID(id) || len(in.Lines) == 0 || len(in.Lines) > 200 || !validDate(in.OrderDate) || (in.ExpectedDate != "" && !validDate(in.ExpectedDate)) ||
		(in.WarehouseID != "" && !isUUID(in.WarehouseID))
	for _, l := range in.Lines {
		q, ok := money(l.Quantity)
		if !isUUID(l.AgreementLineID) || !ok || !q.IsPositive() {
			bad = true
		}
	}
	if bad {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var st, sup, cur, title, start, end string
		var number int64
		err := tx.QueryRow(ctx, `SELECT status, supplier_id::text, currency::text, title, number, start_date::text, end_date::text FROM purchase_agreements
			WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&st, &sup, &cur, &title, &number, &start, &end)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if st != "active" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if in.OrderDate < start || in.OrderDate > end {
			return nil, &apiError{status: http.StatusConflict, code: "agreement_expired"}
		}
		po := poIn{SupplierID: sup, OrderDate: in.OrderDate, ExpectedDate: in.ExpectedDate, Currency: cur, SupplierRef: "BA-" + itoa64(number),
			Notes: "Blanket agreement BA-" + itoa64(number) + " " + title}
		var lineIDs []string
		for _, l := range in.Lines {
			var item, desc, price, remaining string
			err := tx.QueryRow(ctx, `SELECT coalesce(l.item_id::text, ''), l.description, trim_scale(l.unit_price)::text,
				(l.quantity - coalesce((SELECT sum(pl.quantity) FROM purchase_order_lines pl JOIN purchase_orders o ON o.id = pl.order_id
				  WHERE pl.agreement_line_id = l.id AND o.status <> 'cancelled'), 0))::text
				FROM purchase_agreement_lines l WHERE l.id = $1 AND l.agreement_id = $2`, l.AgreementLineID, id).Scan(&item, &desc, &price, &remaining)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, badRequest("invalid_line")
			}
			if err != nil {
				return nil, err
			}
			q, _ := money(l.Quantity)
			rem, _ := decimal.NewFromString(remaining)
			if q.GreaterThan(rem) && !in.AllowOver {
				return nil, &apiError{status: http.StatusConflict, code: "agreement_exceeded", detail: desc}
			}
			po.Lines = append(po.Lines, poLineIn{ItemID: item, Description: desc, Quantity: q.String(), UnitPrice: price, WarehouseID: in.WarehouseID})
			lineIDs = append(lineIDs, l.AgreementLineID)
		}
		pid, err := createPOTx(ctx, tx, cid, po, true, "", id)
		if err != nil {
			return nil, err
		}
		for i, al := range lineIDs {
			if _, err := tx.Exec(ctx, `UPDATE purchase_order_lines SET agreement_line_id = $3 WHERE order_id = $1 AND line_no = $2`, pid, i+1, al); err != nil {
				return nil, err
			}
		}
		a, err := loadAgreement(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"order_id": pid, "agreement": a}, nil
	})
}
