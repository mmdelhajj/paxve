package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Retail core part 1 (continued): loyalty points, gift cards / vouchers, scale barcodes and labels, POS held
// baskets and quick items, near-expiry markdowns, retail sales reports, business packs.
//
// Loyalty accounting: points earned are a cost of the sale promise → Dr loyalty & promotions expense / Cr loyalty
// points liability (points × point value, base currency). Redeeming points is a tender whose "cash box" is that
// liability (Dr liability / Cr receivables). Gift cards sold at the till are a liability, not revenue (no VAT on the
// card); paying with a card debits that liability. A voided card's unused balance is released to income.

// ======================= loyalty =======================

type loyaltyProg struct {
	Active                      bool
	EarnPer, PointValue, MinRed decimal.Decimal
	Expiry                      int
	Tiers                       []struct {
		Name       string          `json:"name"`
		MinPoints  decimal.Decimal `json:"min_points"`
		Multiplier decimal.Decimal `json:"multiplier"`
	}
}

func loadLoyalty(ctx context.Context, tx pgx.Tx, cid string) (*loyaltyProg, error) {
	p := &loyaltyProg{}
	var earn, val, minR, tiers string
	err := tx.QueryRow(ctx, `SELECT is_active, earn_per::text, point_value::text, min_redeem::text, expiry_months, tiers::text
		FROM loyalty_programs WHERE company_id = $1`, cid).Scan(&p.Active, &earn, &val, &minR, &p.Expiry, &tiers)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.EarnPer, _ = decimal.NewFromString(earn)
	p.PointValue, _ = decimal.NewFromString(val)
	p.MinRed, _ = decimal.NewFromString(minR)
	_ = json.Unmarshal([]byte(tiers), &p.Tiers)
	return p, nil
}

func (p *loyaltyProg) tier(lifetime decimal.Decimal) (string, decimal.Decimal) {
	name, mult := "", decimal.NewFromInt(1)
	best := decimal.NewFromInt(-1)
	for _, t := range p.Tiers {
		if lifetime.GreaterThanOrEqual(t.MinPoints) && t.MinPoints.GreaterThan(best) {
			best, name = t.MinPoints, t.Name
			if t.Multiplier.IsPositive() {
				mult = t.Multiplier
			}
		}
	}
	return name, mult
}

// loyaltyEarn gives the customer points for baseAmount (base currency) and books their value.
func loyaltyEarn(ctx context.Context, tx pgx.Tx, cid string, set *billingSettings, p *loyaltyProg, customer string, baseAmount decimal.Decimal,
	source, sourceID, date, memo string) (decimal.Decimal, error) {
	if p == nil || !p.Active || !p.EarnPer.IsPositive() || !baseAmount.IsPositive() {
		return decimal.Zero, nil
	}
	var lifeS string
	if err := tx.QueryRow(ctx, `SELECT loyalty_lifetime::text FROM customers WHERE id = $1 AND company_id = $2 FOR UPDATE`, customer, cid).Scan(&lifeS); err != nil {
		return decimal.Zero, err
	}
	life, _ := decimal.NewFromString(lifeS)
	_, mult := p.tier(life)
	pts := baseAmount.Mul(p.EarnPer).Mul(mult).Floor()
	if !pts.IsPositive() {
		return decimal.Zero, nil
	}
	value := pts.Mul(p.PointValue).Round(4)
	var je *string
	if value.IsPositive() {
		exp, err := ensureRetailAccount(ctx, tx, cid, "loyalty_expense_account_id")
		if err != nil {
			return decimal.Zero, err
		}
		liab, err := ensureRetailAccount(ctx, tx, cid, "loyalty_liability_account_id")
		if err != nil {
			return decimal.Zero, err
		}
		id, err := postJournal(ctx, tx, cid, set, date, memo, "", "loyalty", []jline{
			{Account: exp, Desc: memo, Currency: set.Base, Debit: value},
			{Account: liab, Desc: memo, Currency: set.Base, Credit: value}})
		if err != nil {
			return decimal.Zero, err
		}
		je = &id
	}
	if _, err := tx.Exec(ctx, `INSERT INTO loyalty_moves (company_id, customer_id, kind, points, value, source, source_id, journal_entry_id, note)
		VALUES ($1, $2, 'earn', $3::numeric, $4::numeric, $5, $6::uuid, $7, $8)`, cid, customer, pts.String(), value.String(), source,
		nullUUID(sourceID), je, memo); err != nil {
		return decimal.Zero, err
	}
	_, err := tx.Exec(ctx, `UPDATE customers SET loyalty_points = loyalty_points + $2::numeric, loyalty_lifetime = loyalty_lifetime + $2::numeric WHERE id = $1`,
		customer, pts.String())
	return pts, err
}

func nullUUID(s string) *string {
	if isUUID(s) {
		return &s
	}
	return nil
}

func (s *Server) getLoyalty(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		prog, err := queryJSON(ctx, tx, `SELECT is_active, earn_per::text AS earn_per, point_value::text AS point_value, min_redeem::text AS min_redeem,
			expiry_months, tiers FROM loyalty_programs WHERE company_id = $1`, cid)
		if err != nil {
			return nil, err
		}
		stats, err := queryJSONOne(ctx, tx, `SELECT count(*) FILTER (WHERE loyalty_card <> '' OR loyalty_lifetime > 0) AS members,
			coalesce(sum(loyalty_points), 0)::text AS points FROM customers WHERE company_id = $1`, cid)
		if err != nil {
			return nil, err
		}
		var list []json.RawMessage
		_ = json.Unmarshal(prog, &list)
		var p json.RawMessage = []byte(`{"is_active": false, "earn_per": "1", "point_value": "0.01", "min_redeem": "0", "expiry_months": 0, "tiers": []}`)
		if len(list) > 0 {
			p = list[0]
		}
		return map[string]json.RawMessage{"program": p, "stats": stats}, nil
	})
}

func (s *Server) putLoyalty(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IsActive     bool   `json:"is_active"`
		EarnPer      string `json:"earn_per"`
		PointValue   string `json:"point_value"`
		MinRedeem    string `json:"min_redeem"`
		ExpiryMonths int    `json:"expiry_months"`
		Tiers        []struct {
			Name       string `json:"name"`
			MinPoints  string `json:"min_points"`
			Multiplier string `json:"multiplier"`
		} `json:"tiers"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.MinRedeem == "" {
		in.MinRedeem = "0"
	}
	if !okMoney(in.EarnPer) || !okMoney(in.MinRedeem) || in.ExpiryMonths < 0 || in.ExpiryMonths > 120 || len(in.Tiers) > 10 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if v, err := decimal.NewFromString(strings.TrimSpace(in.PointValue)); err != nil || v.IsNegative() {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	tiers := []map[string]any{}
	for _, t := range in.Tiers {
		mp, ok1 := money(t.MinPoints)
		mu, ok2 := money(t.Multiplier)
		if !ok1 || !ok2 || strings.TrimSpace(t.Name) == "" {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
		tiers = append(tiers, map[string]any{"name": strings.TrimSpace(t.Name), "min_points": mp, "multiplier": mu})
	}
	tb, _ := json.Marshal(tiers)
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := tx.Exec(ctx, `INSERT INTO loyalty_programs (company_id, is_active, earn_per, point_value, min_redeem, expiry_months, tiers)
			VALUES ($1, $2, $3::numeric, $4::numeric, $5::numeric, $6, $7)
			ON CONFLICT (company_id) DO UPDATE SET is_active = $2, earn_per = $3::numeric, point_value = $4::numeric, min_redeem = $5::numeric,
			  expiry_months = $6, tiers = $7, updated_at = now()`, cid, in.IsActive, in.EarnPer, strings.TrimSpace(in.PointValue), in.MinRedeem,
			in.ExpiryMonths, tb); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	})
}

const loyaltyCustCols = `c.id, c.code, c.name, c.phone, c.loyalty_card, c.loyalty_points::text AS points, c.loyalty_lifetime::text AS lifetime`

// GET /retail/loyalty/lookup?code= (card number, phone or customer code)
func (s *Server) loyaltyLookup(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" || len(code) > 40 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		raw, err := queryJSONOne(ctx, tx, `SELECT `+loyaltyCustCols+` FROM customers c WHERE c.company_id = $1
			AND (c.loyalty_card = $2 OR c.code = $2 OR (length($2) >= 6 AND regexp_replace(c.phone, '\D', '', 'g') = regexp_replace($2, '\D', '', 'g')))
			ORDER BY (c.loyalty_card = $2) DESC LIMIT 1`, cid, code)
		if err != nil {
			return nil, err
		}
		return withTier(ctx, tx, cid, raw)
	})
}

func withTier(ctx context.Context, tx pgx.Tx, cid string, raw json.RawMessage) (any, error) {
	m := map[string]any{}
	_ = json.Unmarshal(raw, &m)
	p, err := loadLoyalty(ctx, tx, cid)
	if err != nil {
		return nil, err
	}
	m["tier"], m["point_value"], m["min_redeem"], m["active"] = "", "0", "0", false
	if p != nil {
		life, _ := decimal.NewFromString(fmt.Sprint(m["lifetime"]))
		name, _ := p.tier(life)
		pts, _ := decimal.NewFromString(fmt.Sprint(m["points"]))
		m["tier"], m["point_value"], m["min_redeem"], m["active"] = name, p.PointValue.String(), p.MinRed.String(), p.Active
		m["value"] = pts.Mul(p.PointValue).Round(4).String()
	}
	return m, nil
}

// GET /retail/loyalty/members?q=
func (s *Server) loyaltyMembers(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+loyaltyCustCols+` FROM customers c WHERE c.company_id = $1
			AND (c.loyalty_card <> '' OR c.loyalty_lifetime > 0 OR $2 <> '')
			AND ($2 = '' OR c.name ILIKE '%' || $2 || '%' OR c.loyalty_card = $2 OR c.phone LIKE '%' || $2 || '%' OR c.code = $2)
			ORDER BY c.loyalty_points DESC, c.name LIMIT 300`, r.PathValue("cid"), q)
	})
}

// PUT /retail/loyalty/customers/{id} {card: "" | "auto" | "<number>"}
func (s *Server) setLoyaltyCard(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Card string `json:"card"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	in.Card = strings.TrimSpace(in.Card)
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) || len(in.Card) > 40 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		card := in.Card
		if strings.EqualFold(card, "auto") {
			card = ean13(fmt.Sprintf("28%010d", time.Now().UnixMilli()%10000000000))
		}
		tag, err := tx.Exec(ctx, `UPDATE customers SET loyalty_card = $3 WHERE id = $1 AND company_id = $2`, id, cid, card)
		if isUniqueViolation(err) {
			return nil, &apiError{status: http.StatusConflict, code: "card_taken"}
		}
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		raw, err := queryJSONOne(ctx, tx, `SELECT `+loyaltyCustCols+` FROM customers c WHERE c.id = $1`, id)
		if err != nil {
			return nil, err
		}
		return withTier(ctx, tx, cid, raw)
	})
}

// POST /retail/loyalty/customers/{id}/adjust {points (+/-), note}
func (s *Server) adjustLoyalty(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Points string `json:"points"`
		Note   string `json:"note"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	pts, err := decimal.NewFromString(strings.TrimSpace(in.Points))
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if err != nil || pts.IsZero() || pts.Exponent() < -2 || !isUUID(id) || strings.TrimSpace(in.Note) == "" || len(in.Note) > 300 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		p, err := loadLoyalty(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		if p == nil {
			return nil, &apiError{status: http.StatusUnprocessableEntity, code: "loyalty_off"}
		}
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		var have string
		if err := tx.QueryRow(ctx, `SELECT loyalty_points::text FROM customers WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&have); err != nil {
			if err == pgx.ErrNoRows {
				return nil, notFound()
			}
			return nil, err
		}
		h, _ := decimal.NewFromString(have)
		if h.Add(pts).IsNegative() {
			return nil, &apiError{status: http.StatusUnprocessableEntity, code: "not_enough_points", detail: h.String()}
		}
		value := pts.Mul(p.PointValue).Round(4)
		var je *string
		if !value.IsZero() {
			exp, err := ensureRetailAccount(ctx, tx, cid, "loyalty_expense_account_id")
			if err != nil {
				return nil, err
			}
			liab, err := ensureRetailAccount(ctx, tx, cid, "loyalty_liability_account_id")
			if err != nil {
				return nil, err
			}
			memo := "Loyalty points adjustment: " + strings.TrimSpace(in.Note)
			lines := []jline{{Account: exp, Desc: memo, Currency: set.Base, Debit: value}, {Account: liab, Desc: memo, Currency: set.Base, Credit: value}}
			if value.IsNegative() {
				lines = []jline{{Account: liab, Desc: memo, Currency: set.Base, Debit: value.Neg()}, {Account: exp, Desc: memo, Currency: set.Base, Credit: value.Neg()}}
			}
			jid, err := postJournal(ctx, tx, cid, set, today(), memo, "", "loyalty", lines)
			if err != nil {
				return nil, err
			}
			je = &jid
		}
		if _, err := tx.Exec(ctx, `INSERT INTO loyalty_moves (company_id, customer_id, kind, points, value, source, journal_entry_id, note)
			VALUES ($1, $2, 'adjust', $3::numeric, $4::numeric, 'manual', $5, $6)`, cid, id, pts.String(), value.String(), je, strings.TrimSpace(in.Note)); err != nil {
			return nil, err
		}
		lifeAdd := decimal.Max(pts, decimal.Zero)
		if _, err := tx.Exec(ctx, `UPDATE customers SET loyalty_points = loyalty_points + $2::numeric, loyalty_lifetime = loyalty_lifetime + $3::numeric WHERE id = $1`,
			id, pts.String(), lifeAdd.String()); err != nil {
			return nil, err
		}
		raw, err := queryJSONOne(ctx, tx, `SELECT `+loyaltyCustCols+` FROM customers c WHERE c.id = $1`, id)
		if err != nil {
			return nil, err
		}
		return withTier(ctx, tx, cid, raw)
	})
}

// GET /retail/loyalty/customers/{id}/moves
func (s *Server) loyaltyMoves(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := queryJSONOne(ctx, tx, `SELECT id FROM customers WHERE id = $1 AND company_id = $2`, id, cid); err != nil {
			return nil, err
		}
		return queryJSON(ctx, tx, `SELECT m.id, m.kind, m.points::text AS points, m.value::text AS value, m.source, m.source_id, m.note, m.created_at,
			o.number AS pos_number FROM loyalty_moves m LEFT JOIN pos_orders o ON o.id = m.source_id
			WHERE m.company_id = $1 AND m.customer_id = $2 ORDER BY m.created_at DESC LIMIT 300`, cid, id)
	})
}

// ======================= gift cards =======================

type giftIn struct {
	Code   string `json:"code"`
	Amount string `json:"amount"`
}

var giftCodeRe = regexp.MustCompile(`^[A-Z0-9-]{4,32}$`)

const giftCols = `g.id, g.code, g.currency, g.initial::text AS initial, g.balance::text AS balance, g.status, g.expires_on, g.source,
	g.customer_id, c.name AS customer_name, g.created_at`

func newGiftCode() string {
	h := strings.ToUpper(randHex(6))
	return "GC-" + h[:4] + "-" + h[4:8] + "-" + h[8:12]
}

func normGiftCode(s string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), " ", ""))
}

func (s *Server) listGiftCards(w http.ResponseWriter, r *http.Request) {
	q := normGiftCode(r.URL.Query().Get("q"))
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		rows, err := queryJSON(ctx, tx, `SELECT `+giftCols+` FROM gift_cards g LEFT JOIN customers c ON c.id = g.customer_id
			WHERE g.company_id = $1 AND ($2 = '' OR g.code LIKE '%' || $2 || '%') ORDER BY g.created_at DESC LIMIT 500`, r.PathValue("cid"), q)
		if err != nil {
			return nil, err
		}
		tot, err := queryJSON(ctx, tx, `SELECT currency, count(*) AS cards, sum(balance)::text AS outstanding FROM gift_cards
			WHERE company_id = $1 AND status = 'active' GROUP BY currency ORDER BY currency`, r.PathValue("cid"))
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"rows": rows, "totals": tot}, nil
	})
}

// GET /retail/gift-cards/lookup?code=
func (s *Server) giftLookup(w http.ResponseWriter, r *http.Request) {
	code := normGiftCode(r.URL.Query().Get("code"))
	if code == "" || len(code) > 32 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSONOne(ctx, tx, `SELECT `+giftCols+`, (g.expires_on IS NOT NULL AND g.expires_on < current_date) AS expired
			FROM gift_cards g LEFT JOIN customers c ON c.id = g.customer_id WHERE g.company_id = $1 AND g.code = $2`, r.PathValue("cid"), code)
	})
}

// POST /retail/gift-cards {amount, currency, code?, customer_id?, expires_on?, note}: a free voucher (marketing).
func (s *Server) issueVoucher(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Amount     string `json:"amount"`
		Currency   string `json:"currency"`
		Code       string `json:"code"`
		CustomerID string `json:"customer_id"`
		ExpiresOn  string `json:"expires_on"`
		Note       string `json:"note"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	amt, ok := money(in.Amount)
	in.Code = normGiftCode(in.Code)
	if !ok || !amt.IsPositive() || !currencyRe.MatchString(in.Currency) || (in.CustomerID != "" && !isUUID(in.CustomerID)) ||
		(in.Code != "" && !giftCodeRe.MatchString(in.Code)) ||
		(in.ExpiresOn != "" && !validDate(in.ExpiresOn)) || len(in.Note) > 300 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		if in.CustomerID != "" {
			if _, err := lockCustomer(ctx, tx, cid, in.CustomerID); err != nil {
				return nil, err
			}
		}
		exp, err := ensureRetailAccount(ctx, tx, cid, "loyalty_expense_account_id")
		if err != nil {
			return nil, err
		}
		liab, err := ensureRetailAccount(ctx, tx, cid, "gift_liability_account_id")
		if err != nil {
			return nil, err
		}
		code := in.Code
		if code == "" {
			code = newGiftCode()
		}
		memo := "Voucher " + code + " " + strings.TrimSpace(in.Note)
		je, err := postJournal(ctx, tx, cid, set, today(), memo, code, "gift_card", []jline{
			{Account: exp, Desc: memo, Currency: in.Currency, Debit: amt}, {Account: liab, Desc: memo, Currency: in.Currency, Credit: amt}})
		if err != nil {
			return nil, err
		}
		id, err := insertGiftCard(ctx, tx, cid, code, in.Currency, amt, in.CustomerID, in.ExpiresOn, "promo", "", je)
		if err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+giftCols+` FROM gift_cards g LEFT JOIN customers c ON c.id = g.customer_id WHERE g.id = $1`, id)
	})
}

func insertGiftCard(ctx context.Context, tx pgx.Tx, cid, code, cur string, amt decimal.Decimal, customer, expires, source, sourceID, je string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO gift_cards (company_id, code, currency, initial, balance, customer_id, expires_on, source, source_id, journal_entry_id)
		VALUES ($1, $2, $3, $4::numeric, $4::numeric, $5::uuid, nullif($6, '')::date, $7, $8::uuid, $9::uuid) RETURNING id::text`,
		cid, code, cur, amt.String(), nullUUID(customer), expires, source, nullUUID(sourceID), nullUUID(je)).Scan(&id)
	if isUniqueViolation(err) {
		return "", &apiError{status: http.StatusConflict, code: "gift_code_taken"}
	}
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO gift_card_moves (company_id, card_id, kind, amount, source, source_id) VALUES ($1, $2, 'issue', $3::numeric, $4, $5::uuid)`,
		cid, id, amt.String(), source, nullUUID(sourceID))
	return id, err
}

// POST /retail/gift-cards/{id}/void: the unused balance is released (vouchers: back to the promotions expense; sold cards: income).
func (s *Server) voidGiftCard(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var code, cur, bal, status, source string
		err := tx.QueryRow(ctx, `SELECT code, currency::text, balance::text, status, source FROM gift_cards WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).
			Scan(&code, &cur, &bal, &status, &source)
		if err == pgx.ErrNoRows {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if status != "active" {
			return nil, &apiError{status: http.StatusConflict, code: "gift_not_active"}
		}
		b, _ := decimal.NewFromString(bal)
		if b.IsPositive() {
			set, err := loadBillingSettings(ctx, tx, cid)
			if err != nil {
				return nil, err
			}
			liab, err := ensureRetailAccount(ctx, tx, cid, "gift_liability_account_id")
			if err != nil {
				return nil, err
			}
			to := ""
			if source == "promo" {
				to, err = ensureRetailAccount(ctx, tx, cid, "loyalty_expense_account_id")
				if err != nil {
					return nil, err
				}
			} else {
				var g *string
				_ = tx.QueryRow(ctx, `SELECT coalesce(goods_revenue_account_id, default_revenue_account_id)::text FROM companies WHERE id = $1`, cid).Scan(&g)
				if g == nil {
					return nil, notSetup("revenue")
				}
				to = *g
			}
			memo := "Gift card " + code + " voided (unused balance)"
			if _, err := postJournal(ctx, tx, cid, set, today(), memo, code, "gift_card", []jline{
				{Account: liab, Desc: memo, Currency: cur, Debit: b}, {Account: to, Desc: memo, Currency: cur, Credit: b}}); err != nil {
				return nil, err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE gift_cards SET status = 'void', balance = 0 WHERE id = $1`, id); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO gift_card_moves (company_id, card_id, kind, amount, source) VALUES ($1, $2, 'void', $3::numeric, 'manual')`,
			cid, id, b.Neg().String()); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+giftCols+` FROM gift_cards g LEFT JOIN customers c ON c.id = g.customer_id WHERE g.id = $1`, id)
	})
}

// GET /retail/gift-cards/{id}/moves
func (s *Server) giftMoves(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := queryJSONOne(ctx, tx, `SELECT id FROM gift_cards WHERE id = $1 AND company_id = $2`, id, cid); err != nil {
			return nil, err
		}
		return queryJSON(ctx, tx, `SELECT m.kind, m.amount::text AS amount, m.source, m.source_id, m.created_at, o.number AS pos_number
			FROM gift_card_moves m LEFT JOIN pos_orders o ON o.id = m.source_id WHERE m.company_id = $1 AND m.card_id = $2 ORDER BY m.created_at`, cid, id)
	})
}

// ======================= scale barcodes, PLU, labels =======================

type scalePrefix struct {
	Prefix   string `json:"prefix"`
	Mode     string `json:"mode"` // weight | price
	Decimals int    `json:"decimals"`
}

func (s *Server) getScale(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSONOne(ctx, tx, `SELECT scale_config AS config, variant_barcode_prefix FROM companies WHERE id = $1`, r.PathValue("cid"))
	})
}

func (s *Server) putScale(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Prefixes             []scalePrefix `json:"prefixes"`
		VariantBarcodePrefix string        `json:"variant_barcode_prefix"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	seen := map[string]bool{}
	for _, p := range in.Prefixes {
		if len(p.Prefix) != 2 || p.Prefix < "20" || p.Prefix > "29" || seen[p.Prefix] || (p.Mode != "weight" && p.Mode != "price") ||
			p.Decimals < 0 || p.Decimals > 3 || strings.HasPrefix(in.VariantBarcodePrefix, p.Prefix) {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
		seen[p.Prefix] = true
	}
	if len(in.Prefixes) > 10 || (in.VariantBarcodePrefix != "" && (len(in.VariantBarcodePrefix) < 2 || len(in.VariantBarcodePrefix) > 4 || strings.Trim(in.VariantBarcodePrefix, "0123456789") != "")) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.Prefixes == nil {
		in.Prefixes = []scalePrefix{}
	}
	b, _ := json.Marshal(map[string]any{"prefixes": in.Prefixes})
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := tx.Exec(ctx, `UPDATE companies SET scale_config = $2, variant_barcode_prefix = coalesce(nullif($3, ''), variant_barcode_prefix) WHERE id = $1`,
			cid, b, in.VariantBarcodePrefix); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT scale_config AS config, variant_barcode_prefix FROM companies WHERE id = $1`, cid)
	})
}

// GET /retail/scale/plu: items sold by weight (or with a PLU) and a CSV for the scale software.
func (s *Server) pluExport(w http.ResponseWriter, r *http.Request) {
	cur := r.URL.Query().Get("currency")
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		if !currencyRe.MatchString(cur) {
			cur = set.Base
		}
		rc := newRateCache(ctx, tx, cid, set.Base, today())
		rows, err := tx.Query(ctx, `SELECT it.id::text, it.plu, it.code, it.name, it.unit, it.sold_by_weight, ip.price::text, it.sale_price::text,
			coalesce(it.sale_currency, $3)::text FROM items it LEFT JOIN item_prices ip ON ip.item_id = it.id AND ip.currency = $2
			WHERE it.company_id = $1 AND it.is_active AND (it.plu IS NOT NULL OR it.sold_by_weight) ORDER BY it.plu NULLS LAST, it.name`, cid, cur, set.Base)
		if err != nil {
			return nil, err
		}
		type row struct {
			ID     string `json:"id"`
			PLU    *int   `json:"plu"`
			Code   string `json:"code"`
			Name   string `json:"name"`
			Unit   string `json:"unit"`
			Weight bool   `json:"sold_by_weight"`
			Price  string `json:"price"`
		}
		list := []row{}
		type raw struct {
			r             row
			list          *string
			sale, saleCur string
		}
		raws := []raw{}
		for rows.Next() {
			var x raw
			if err := rows.Scan(&x.r.ID, &x.r.PLU, &x.r.Code, &x.r.Name, &x.r.Unit, &x.r.Weight, &x.list, &x.sale, &x.saleCur); err != nil {
				rows.Close()
				return nil, err
			}
			raws = append(raws, x)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		var csv strings.Builder
		csv.WriteString("PLU;Code;Name;Price;Unit;ByWeight\r\n")
		for _, x := range raws {
			p, err := itemPriceIn(rc, x.list, x.sale, x.saleCur, cur)
			if err != nil {
				return nil, err
			}
			x.r.Price = p.String()
			list = append(list, x.r)
			plu := ""
			if x.r.PLU != nil {
				plu = strconv.Itoa(*x.r.PLU)
			}
			byW := "0"
			if x.r.Weight {
				byW = "1"
			}
			csv.WriteString(fmt.Sprintf("%s;%s;%s;%s;%s;%s\r\n", plu, csvField(x.r.Code), csvField(x.r.Name), p.StringFixed(2), x.r.Unit, byW))
		}
		return map[string]any{"currency": cur, "items": list, "csv": csv.String()}, nil
	})
}

func csvField(s string) string {
	s = strings.ReplaceAll(s, ";", ",")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

// PUT /items/{id}/retail {plu, sold_by_weight, brand, content_qty, content_unit}
func (s *Server) putItemRetail(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PLU          *int    `json:"plu"`
		SoldByWeight *bool   `json:"sold_by_weight"`
		Brand        *string `json:"brand"`
		ContentQty   *string `json:"content_qty"`
		ContentUnit  *string `json:"content_unit"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) || (in.PLU != nil && (*in.PLU < 0 || *in.PLU > 99999)) || (in.Brand != nil && len(*in.Brand) > 80) ||
		(in.ContentQty != nil && *in.ContentQty != "" && !okMoney(*in.ContentQty)) ||
		(in.ContentUnit != nil && !strings.Contains("||kg|g|L|ml|m|pcs|", "|"+*in.ContentUnit+"|")) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		plu := -1
		if in.PLU != nil {
			plu = *in.PLU
		}
		cq := "-"
		if in.ContentQty != nil {
			cq = strings.TrimSpace(*in.ContentQty)
		}
		var brand *string
		if in.Brand != nil {
			b := strings.TrimSpace(*in.Brand)
			brand = &b
		}
		tag, err := tx.Exec(ctx, `UPDATE items SET plu = CASE WHEN $3 < 0 THEN plu WHEN $3 = 0 THEN NULL ELSE $3 END,
			sold_by_weight = coalesce($4, sold_by_weight), brand = coalesce($5, brand),
			content_qty = CASE WHEN $6 = '-' THEN content_qty WHEN $6 = '' THEN NULL ELSE $6::numeric END,
			content_unit = coalesce($7, content_unit) WHERE id = $1 AND company_id = $2`, id, cid, plu, in.SoldByWeight, brand, cq, in.ContentUnit)
		if isUniqueViolation(err) {
			return nil, &apiError{status: http.StatusConflict, code: "plu_taken"}
		}
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return queryJSONOne(ctx, tx, `SELECT id, plu, sold_by_weight, brand, content_qty::text AS content_qty, content_unit FROM items WHERE id = $1`, id)
	})
}

// GET /retail/labels?ids=a,b&currency=USD: what a shelf / barcode label shows (price, unit price, promotion price).
func (s *Server) labelData(w http.ResponseWriter, r *http.Request) {
	ids := []string{}
	for _, x := range strings.Split(r.URL.Query().Get("ids"), ",") {
		if isUUID(strings.TrimSpace(x)) {
			ids = append(ids, strings.TrimSpace(x))
		}
	}
	cur := r.URL.Query().Get("currency")
	if len(ids) == 0 || len(ids) > 500 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		if !currencyRe.MatchString(cur) {
			cur = set.Base
		}
		rc := newRateCache(ctx, tx, cid, set.Base, today())
		dec, err := rc.decimals(cur)
		if err != nil {
			return nil, err
		}
		promos, _, conv, _, err := promoContext(ctx, tx, cid, cur, "", "", "", time.Now())
		if err != nil {
			return nil, err
		}
		rows, err := tx.Query(ctx, `SELECT it.id::text, it.code, it.name, it.name_i18n::text, it.barcode, it.unit, it.sold_by_weight, it.plu,
			it.content_qty::text, it.content_unit, ip.price::text, it.sale_price::text, coalesce(it.sale_currency, $3)::text,
			coalesce(it.category_id::text, ''), it.brand, coalesce(it.template_id::text, ''), trim_scale(t.rate)::text
			FROM items it LEFT JOIN item_prices ip ON ip.item_id = it.id AND ip.currency = $2
			LEFT JOIN tax_rates t ON t.id = it.sale_tax_rate_id AND t.is_active
			WHERE it.company_id = $1 AND it.id = ANY ($4::uuid[]) ORDER BY it.name`, cid, cur, set.Base, ids)
		if err != nil {
			return nil, err
		}
		type lab struct {
			ID         string          `json:"id"`
			Code       string          `json:"code"`
			Name       string          `json:"name"`
			NameI18n   json.RawMessage `json:"name_i18n"`
			Barcode    string          `json:"barcode"`
			Unit       string          `json:"unit"`
			Weight     bool            `json:"sold_by_weight"`
			PLU        *int            `json:"plu"`
			Price      string          `json:"price"`       // incl. VAT
			PromoPrice *string         `json:"promo_price"` // incl. VAT, one unit
			UnitPrice  *string         `json:"unit_price"`  // per kg / L / … from the content quantity
			UnitBasis  string          `json:"unit_basis"`
		}
		type raw struct {
			l               lab
			i18n            string
			cq              *string
			cu              string
			list            *string
			sale, saleCur   string
			cat, brand, tpl string
			tax             *string
		}
		raws := []raw{}
		for rows.Next() {
			var x raw
			if err := rows.Scan(&x.l.ID, &x.l.Code, &x.l.Name, &x.i18n, &x.l.Barcode, &x.l.Unit, &x.l.Weight, &x.l.PLU, &x.cq, &x.cu, &x.list,
				&x.sale, &x.saleCur, &x.cat, &x.brand, &x.tpl, &x.tax); err != nil {
				rows.Close()
				return nil, err
			}
			raws = append(raws, x)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		out := []lab{}
		for _, x := range raws {
			x.l.NameI18n = json.RawMessage(x.i18n)
			net, err := itemPriceIn(rc, x.list, x.sale, x.saleCur, cur)
			if err != nil {
				return nil, err
			}
			vat := decimal.Zero
			if x.tax != nil {
				vat, _ = decimal.NewFromString(*x.tax)
			}
			gross := func(v decimal.Decimal) decimal.Decimal { return v.Add(v.Mul(vat).Div(hundred)).Round(dec) }
			x.l.Price = gross(net).String()
			res, err := evalPromotions(promos, []promoLine{{ItemID: x.l.ID, CategoryID: x.cat, Brand: x.brand, TemplateID: x.tpl, Qty: decimal.NewFromInt(1), Amount: net}}, conv, dec)
			if err == nil && res.Lines[0].IsPositive() {
				pp := gross(net.Sub(res.Lines[0])).String()
				x.l.PromoPrice = &pp
			}
			if x.cq != nil && x.cu != "" {
				cq, _ := decimal.NewFromString(*x.cq)
				basis, factor := x.cu, decimal.NewFromInt(1)
				switch x.cu {
				case "g":
					basis, factor = "kg", decimal.NewFromInt(1000)
				case "ml":
					basis, factor = "L", decimal.NewFromInt(1000)
				}
				if cq.IsPositive() {
					up := gross(net).Mul(factor).Div(cq).Round(dec).String()
					x.l.UnitPrice, x.l.UnitBasis = &up, basis
				}
			}
			out = append(out, x.l)
		}
		return map[string]any{"currency": cur, "labels": out}, nil
	})
}

// ======================= POS: held baskets, quick items =======================

func (s *Server) listHeld(w http.ResponseWriter, r *http.Request) {
	reg := r.URL.Query().Get("register_id")
	if !isUUID(reg) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT h.id, h.label, h.payload, h.created_at, u.full_name AS created_by_name FROM pos_held_baskets h
			LEFT JOIN users u ON u.id = h.created_by WHERE h.company_id = $1 AND h.register_id = $2 ORDER BY h.created_at`, r.PathValue("cid"), reg)
	})
}

func (s *Server) holdBasket(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RegisterID string          `json:"register_id"`
		Label      string          `json:"label"`
		Payload    json.RawMessage `json:"payload"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if !isUUID(in.RegisterID) || len(in.Payload) < 2 || len(in.Payload) > 200000 || len(in.Label) > 80 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := loadRegister(ctx, tx, cid, in.RegisterID); err != nil {
			return nil, err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM pos_held_baskets WHERE company_id = $1 AND register_id = $2`, cid, in.RegisterID).Scan(&n); err != nil {
			return nil, err
		}
		if n >= 30 {
			return nil, &apiError{status: http.StatusConflict, code: "too_many_held"}
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO pos_held_baskets (company_id, register_id, label, payload) VALUES ($1, $2, $3, $4)
			RETURNING id::text`, cid, in.RegisterID, strings.TrimSpace(in.Label), in.Payload).Scan(&id); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT id, label, payload, created_at FROM pos_held_baskets WHERE id = $1`, id)
	})
}

func (s *Server) deleteHeld(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		tag, err := tx.Exec(ctx, `DELETE FROM pos_held_baskets WHERE id = $1 AND company_id = $2`, id, cid)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return map[string]bool{"ok": true}, nil
	})
}

// POST /pos/quick-item {register_id, name, price, barcode, category_id}: a non-stock item created at the till.
func (s *Server) posQuickItem(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RegisterID string `json:"register_id"`
		Name       string `json:"name"`
		Price      string `json:"price"`
		Barcode    string `json:"barcode"`
		CategoryID string `json:"category_id"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	in.Name, in.Barcode = strings.TrimSpace(in.Name), strings.TrimSpace(in.Barcode)
	p, ok := money(in.Price)
	if !isUUID(in.RegisterID) || in.Name == "" || len(in.Name) > 120 || !ok || len(in.Barcode) > 64 || (in.CategoryID != "" && !isUUID(in.CategoryID)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	office := !isCashier(r)
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var allowed bool
		if err := tx.QueryRow(ctx, `SELECT cashier_quick_item FROM pos_registers WHERE id = $1 AND company_id = $2`, in.RegisterID, cid).Scan(&allowed); err != nil {
			if err == pgx.ErrNoRows {
				return nil, notFound()
			}
			return nil, err
		}
		if !office && !allowed {
			return nil, &apiError{status: http.StatusForbidden, code: "quick_item_forbidden"}
		}
		g, err := loadRegister(ctx, tx, cid, in.RegisterID)
		if err != nil {
			return nil, err
		}
		var rev *string
		_ = tx.QueryRow(ctx, `SELECT goods_revenue_account_id::text FROM companies WHERE id = $1`, cid).Scan(&rev)
		var n int64
		if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'item')`, cid).Scan(&n); err != nil {
			return nil, err
		}
		var id string
		err = tx.QueryRow(ctx, `INSERT INTO items (company_id, code, name, kind, unit, sale_price, sale_currency, revenue_account_id, barcode, category_id)
			VALUES ($1, $2, $3, 'consumable', 'pcs', $4::numeric, $5, $6::uuid, $7, nullif($8, '')::uuid) RETURNING id::text`,
			cid, "Q"+strconv.FormatInt(n, 10), in.Name, p.String(), g.Currency, rev, in.Barcode, in.CategoryID).Scan(&id)
		if isUniqueViolation(err) {
			return nil, &apiError{status: http.StatusConflict, code: "barcode_taken"}
		}
		if err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT it.id, it.code, it.name, it.name_i18n, it.kind, it.unit, it.barcode, it.category_id, it.image,
			it.sale_price::text AS price, NULL::uuid AS tax_rate_id, NULL::text AS tax_rate, NULL::text AS tax_name, '0' AS stock,
			NULL::uuid AS template_id, '{}'::jsonb AS variant_values, false AS sold_by_weight, NULL::int AS plu, '' AS brand
			FROM items it WHERE it.id = $1`, id)
	})
}

// ======================= supermarket: near expiry, retail reports =======================

// GET /retail/expiry?days=30: lots that expire soon, with a suggested markdown.
func (s *Server) expiryReport(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 || days > 365 {
		days = 30
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT l.id AS lot_id, l.lot_no, l.expiry, (l.expiry - current_date) AS days_left, it.id AS item_id, it.code, it.name,
			it.name_i18n, w.name AS warehouse, q.quantity::text AS quantity,
			CASE WHEN l.expiry < current_date THEN 100 WHEN l.expiry - current_date <= 3 THEN 50 WHEN l.expiry - current_date <= 7 THEN 30
			     WHEN l.expiry - current_date <= 14 THEN 20 ELSE 10 END AS suggested_pct,
			EXISTS (SELECT 1 FROM promotions p WHERE p.company_id = it.company_id AND p.is_active AND it.id = ANY (p.item_ids)
			        AND (p.date_to IS NULL OR p.date_to >= current_date)) AS has_promo
			FROM stock_lots l JOIN stock_lot_qty q ON q.lot_id = l.id AND q.quantity > 0
			JOIN items it ON it.id = l.item_id JOIN warehouses w ON w.id = q.warehouse_id
			WHERE l.company_id = $1 AND l.expiry IS NOT NULL AND l.expiry <= current_date + $2::int
			ORDER BY l.expiry, it.name`, r.PathValue("cid"), days)
	})
}

// POST /retail/expiry/markdown {item_id, pct, days}: a dated discount for one item.
func (s *Server) expiryMarkdown(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ItemID string `json:"item_id"`
		Pct    string `json:"pct"`
		Days   int    `json:"days"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	pct, ok := money(in.Pct)
	if !isUUID(in.ItemID) || !ok || !pct.IsPositive() || pct.GreaterThan(hundred) || in.Days < 1 || in.Days > 60 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var name string
		if err := tx.QueryRow(ctx, `SELECT name FROM items WHERE id = $1 AND company_id = $2`, in.ItemID, cid).Scan(&name); err != nil {
			if err == pgx.ErrNoRows {
				return nil, notFound()
			}
			return nil, err
		}
		until := time.Now().In(beirut).AddDate(0, 0, in.Days-1).Format("2006-01-02")
		i18n, _ := json.Marshal(map[string]string{"ar": "قرب انتهاء الصلاحية −" + pct.String() + "% · " + name, "fr": "Date courte −" + pct.String() + "% · " + name})
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO promotions (company_id, name, name_i18n, kind, applies_to, item_ids, value_type, value, date_from, date_to, priority)
			VALUES ($1, $2, $3, 'discount', 'items', ARRAY[$4::uuid], 'pct', $5::numeric, current_date, $6::date, 50) RETURNING id::text`,
			cid, "Near expiry -"+pct.String()+"% · "+name, i18n, in.ItemID, pct.String(), until).Scan(&id); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+promoCols+` FROM promotions p WHERE p.id = $1`, id)
	})
}

// GET /reports/retail?from&to&currency: sales by category, by hour of the day, and margin by item.
func (s *Server) retailReport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, to, cur := q.Get("from"), q.Get("to"), q.Get("currency")
	if !validDate(from) || !validDate(to) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		if !currencyRe.MatchString(cur) {
			cur = set.Base
		}
		// cost is in the base currency: shown in the report currency at today's rate when they differ
		rate := "1"
		if cur != set.Base {
			rc := newRateCache(ctx, tx, cid, set.Base, today())
			v, err := rc.convert(decimal.NewFromInt(1), set.Base, cur)
			if err != nil {
				return nil, err
			}
			rate = v.String()
		}
		const lines = `FROM invoice_lines il JOIN invoices i ON i.id = il.invoice_id
			WHERE i.company_id = $1 AND i.status <> 'void' AND i.issue_date BETWEEN $2::date AND $3::date AND i.currency = $4 AND il.item_id IS NOT NULL`
		byItem, err := queryJSON(ctx, tx, `WITH s AS (SELECT il.item_id, sum(il.quantity) AS qty, sum(il.amount) AS revenue `+lines+` GROUP BY il.item_id),
			c AS (SELECT m.item_id, -sum(m.value) AS cost FROM stock_moves m JOIN invoices i ON i.id = m.source_id
			      WHERE m.company_id = $1 AND m.source = 'sale' AND i.status <> 'void' AND i.issue_date BETWEEN $2::date AND $3::date AND i.currency = $4
			      GROUP BY m.item_id)
			SELECT it.id, it.code, it.name, it.name_i18n, s.qty::text AS qty, s.revenue::text AS revenue,
			  round(coalesce(c.cost, 0) * $5::numeric, 4)::text AS cost, round(s.revenue - coalesce(c.cost, 0) * $5::numeric, 4)::text AS margin,
			  CASE WHEN s.revenue > 0 THEN round((s.revenue - coalesce(c.cost, 0) * $5::numeric) * 100 / s.revenue, 1) END::text AS margin_pct
			FROM s JOIN items it ON it.id = s.item_id LEFT JOIN c ON c.item_id = s.item_id ORDER BY s.revenue DESC LIMIT 1000`,
			cid, from, to, cur, rate)
		if err != nil {
			return nil, err
		}
		byCat, err := queryJSON(ctx, tx, `SELECT coalesce(ic.id::text, '') AS id, coalesce(ic.name, '') AS name, ic.name_i18n,
			sum(il.quantity)::text AS qty, sum(il.amount)::text AS revenue, count(DISTINCT i.id) AS documents `+strings.Replace(lines, "WHERE", `JOIN items it ON it.id = il.item_id
			LEFT JOIN item_categories ic ON ic.id = it.category_id WHERE`, 1)+` GROUP BY ic.id, ic.name, ic.name_i18n ORDER BY sum(il.amount) DESC`,
			cid, from, to, cur)
		if err != nil {
			return nil, err
		}
		byHour, err := queryJSON(ctx, tx, `SELECT extract(hour FROM o.created_at AT TIME ZONE 'Asia/Beirut')::int AS hour, count(*) AS orders,
			sum(o.total)::text AS total FROM pos_orders o WHERE o.company_id = $1 AND o.kind = 'sale' AND o.order_date BETWEEN $2::date AND $3::date
			AND o.currency = $4 GROUP BY 1 ORDER BY 1`, cid, from, to, cur)
		if err != nil {
			return nil, err
		}
		promos, err := queryJSON(ctx, tx, `SELECT p.id, p.name, p.name_i18n, count(*) AS uses, sum(u.discount)::text AS discount
			FROM promotion_uses u JOIN promotions p ON p.id = u.promotion_id WHERE u.company_id = $1 AND u.currency = $4
			AND (u.created_at AT TIME ZONE 'Asia/Beirut')::date BETWEEN $2::date AND $3::date GROUP BY p.id, p.name, p.name_i18n ORDER BY 5 DESC`,
			cid, from, to, cur)
		if err != nil {
			return nil, err
		}
		return map[string]any{"currency": cur, "by_item": byItem, "by_category": byCat, "by_hour": byHour, "promotions": promos}, nil
	})
}
