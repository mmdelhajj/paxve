package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/mmdelhajj/paxve/internal/db"
)

// Phase 9 — card terminals at the till and QR ordering in restaurants.
//
// Card terminals: a card box (payment method of kind "card") gets a terminal driver. The till charges the card on the
// terminal first (simulator: answers at once, for training and tests; manual: a stand-alone terminal, the cashier types
// the approval code it printed), then sends the sale with the approved charges. The card box's ledger account is the
// clearing account: the terminal batch is settled to the bank (gross - fees = net, fees to an expense account), and
// the bank statement line of the net amount then matches that entry.

// terminalDriver charges a card. A real acquirer driver (Areeba, Network International…) plugs in here.
type terminalDriver interface {
	Charge(amount decimal.Decimal, currency, ref string, in chargeIn) (chargeResult, error)
}

type chargeIn struct {
	MethodID     string `json:"method_id"`
	Amount       string `json:"amount"`
	RegisterID   string `json:"register_id"`
	ApprovalCode string `json:"approval_code"` // manual terminal: the code printed by the terminal
	CardLast4    string `json:"card_last4"`
	CardBrand    string `json:"card_brand"`
	Simulate     string `json:"simulate"` // simulator: "decline" to test a refused card
}

type chargeResult struct {
	Approved bool
	Code     string
	RRN      string
	Brand    string
	Last4    string
	Message  string
}

func randDigits(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		v, _ := rand.Int(rand.Reader, big.NewInt(10))
		b.WriteByte(byte('0' + v.Int64()))
	}
	return b.String()
}

type simulatorTerminal struct{}

func (simulatorTerminal) Charge(amount decimal.Decimal, currency, ref string, in chargeIn) (chargeResult, error) {
	if in.Simulate == "decline" {
		return chargeResult{Approved: false, Message: "05 Do not honour (simulator)"}, nil
	}
	brands := []string{"VISA", "MASTERCARD"}
	v, _ := rand.Int(rand.Reader, big.NewInt(2))
	return chargeResult{Approved: true, Code: randDigits(6), RRN: randDigits(12), Brand: brands[v.Int64()], Last4: randDigits(4), Message: "00 Approved (simulator)"}, nil
}

var approvalRe = regexp.MustCompile(`^[A-Za-z0-9]{4,12}$`)
var last4Re = regexp.MustCompile(`^[0-9]{4}$`)

type manualTerminal struct{}

func (manualTerminal) Charge(amount decimal.Decimal, currency, ref string, in chargeIn) (chargeResult, error) {
	code := strings.TrimSpace(in.ApprovalCode)
	if !approvalRe.MatchString(code) {
		return chargeResult{}, badRequest("approval_code_required")
	}
	last4 := strings.TrimSpace(in.CardLast4)
	if last4 != "" && !last4Re.MatchString(last4) {
		return chargeResult{}, badRequest("bad_card_last4")
	}
	brand := strings.ToUpper(strings.TrimSpace(in.CardBrand))
	if len(brand) > 20 {
		brand = brand[:20]
	}
	return chargeResult{Approved: true, Code: strings.ToUpper(code), Brand: brand, Last4: last4, Message: "manual"}, nil
}

func terminalFor(driver string) terminalDriver {
	switch driver {
	case "simulator":
		return simulatorTerminal{}
	case "manual":
		return manualTerminal{}
	}
	return nil
}

const terminalCols = `m.id, m.name, m.currency, m.kind, m.terminal_driver, m.terminal_ref, trim_scale(m.card_fee_pct)::text AS fee_pct,
	m.card_fee_fixed::text AS fee_fixed, m.settle_method_id, sm.name AS settle_method, m.is_active,
	(SELECT count(*) FROM card_txns t WHERE t.method_id = m.id AND t.status = 'approved' AND t.settlement_id IS NULL AND t.pos_order_id IS NOT NULL) AS open_txns,
	coalesce((SELECT sum(t.amount) FROM card_txns t WHERE t.method_id = m.id AND t.status = 'approved' AND t.settlement_id IS NULL AND t.pos_order_id IS NOT NULL), 0)::text AS open_amount`

// GET /pos/terminals: the card boxes with their terminal settings and what is waiting to be settled.
func (s *Server) listTerminals(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+terminalCols+` FROM payment_methods m LEFT JOIN payment_methods sm ON sm.id = m.settle_method_id
			WHERE m.company_id = $1 AND m.kind = 'card' ORDER BY m.name`, r.PathValue("cid"))
	})
}

// PUT /pos/terminals/{id} {driver, terminal_ref, fee_pct, fee_fixed, settle_method_id}
func (s *Server) putTerminal(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Driver         string `json:"driver"`
		TerminalRef    string `json:"terminal_ref"`
		FeePct         string `json:"fee_pct"`
		FeeFixed       string `json:"fee_fixed"`
		SettleMethodID string `json:"settle_method_id"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.FeePct == "" {
		in.FeePct = "0"
	}
	if in.FeeFixed == "" {
		in.FeeFixed = "0"
	}
	pct, ok1 := money(in.FeePct)
	fix, ok2 := money(in.FeeFixed)
	if !contains([]string{"", "simulator", "manual"}, in.Driver) || len(in.TerminalRef) > 60 || !ok1 || !ok2 || pct.IsNegative() ||
		pct.GreaterThan(decimal.NewFromInt(20)) || fix.IsNegative() || (in.SettleMethodID != "" && !isUUID(in.SettleMethodID)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var kind, cur string
		err := tx.QueryRow(ctx, `SELECT kind, currency::text FROM payment_methods WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&kind, &cur)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if kind != "card" {
			return nil, badRequest("not_a_card_box")
		}
		if in.SettleMethodID != "" {
			var bk, bc string
			if err := tx.QueryRow(ctx, `SELECT kind, currency::text FROM payment_methods WHERE id = $1 AND company_id = $2 AND is_active`, in.SettleMethodID, cid).
				Scan(&bk, &bc); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return nil, badRequest("invalid_bank")
				}
				return nil, err
			}
			if bc != cur || bk == "card" || in.SettleMethodID == id {
				return nil, badRequest("invalid_bank")
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE payment_methods SET terminal_driver = $3, terminal_ref = trim($4), card_fee_pct = $5::numeric, card_fee_fixed = $6::numeric,
			settle_method_id = nullif($7, '')::uuid WHERE id = $1 AND company_id = $2`, id, cid, in.Driver, in.TerminalRef, pct.String(), fix.String(), in.SettleMethodID); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+terminalCols+` FROM payment_methods m LEFT JOIN payment_methods sm ON sm.id = m.settle_method_id WHERE m.id = $1`, id)
	})
}

const cardTxnCols = `t.id, t.method_id, m.name AS method, t.register_id, t.currency, t.amount::text AS amount, t.status, t.approval_code, t.rrn,
	t.card_brand, t.card_last4, t.message, t.pos_order_id, o.number AS pos_order_number, t.settlement_id, cs.number AS settlement_number,
	t.created_at, u.full_name AS created_by_name`
const cardTxnFrom = ` FROM card_txns t JOIN payment_methods m ON m.id = t.method_id LEFT JOIN pos_orders o ON o.id = t.pos_order_id
	LEFT JOIN card_settlements cs ON cs.id = t.settlement_id LEFT JOIN users u ON u.id = t.created_by`

// POST /pos/terminal/charge {method_id, amount, register_id, approval_code, card_last4, card_brand, simulate}
func (s *Server) terminalCharge(w http.ResponseWriter, r *http.Request) {
	var in chargeIn
	if !decodeBody(w, r, &in) {
		return
	}
	amt, ok := money(in.Amount)
	if !isUUID(in.MethodID) || !ok || !amt.IsPositive() || (in.RegisterID != "" && !isUUID(in.RegisterID)) || (in.Simulate != "" && in.Simulate != "decline") {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var kind, driver, cur string
		var active bool
		err := tx.QueryRow(ctx, `SELECT kind, terminal_driver, currency::text, is_active FROM payment_methods WHERE id = $1 AND company_id = $2`, in.MethodID, cid).
			Scan(&kind, &driver, &cur, &active)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, badRequest("invalid_tender")
		}
		if err != nil {
			return nil, err
		}
		drv := terminalFor(driver)
		if kind != "card" || !active || drv == nil {
			return nil, badRequest("no_terminal")
		}
		dec, err := currencyDecimals(ctx, tx, cur)
		if err != nil {
			return nil, err
		}
		if !amt.Round(dec).Equal(amt) {
			return nil, badRequest("invalid_amount")
		}
		if in.RegisterID != "" {
			if _, err := loadRegister(ctx, tx, cid, in.RegisterID); err != nil {
				return nil, err
			}
		}
		res, err := drv.Charge(amt, cur, "", in)
		if err != nil {
			return nil, err
		}
		status := "approved"
		if !res.Approved {
			status = "declined"
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO card_txns (company_id, method_id, register_id, currency, amount, status, approval_code, rrn, card_brand, card_last4, message)
			VALUES ($1, $2, nullif($3, '')::uuid, $4, $5::numeric, $6, $7, $8, $9, $10, $11) RETURNING id::text`,
			cid, in.MethodID, in.RegisterID, cur, amt.String(), status, res.Code, res.RRN, res.Brand, res.Last4, res.Message).Scan(&id); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+cardTxnCols+cardTxnFrom+` WHERE t.id = $1`, id)
	})
}

// GET /pos/terminal/txns?method_id=&state=open|unused|settled|all
func (s *Server) listCardTxns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	method, state := q.Get("method_id"), q.Get("state")
	if state == "" {
		state = "all"
	}
	if (method != "" && !isUUID(method)) || !contains([]string{"open", "unused", "settled", "all"}, state) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+cardTxnCols+cardTxnFrom+` WHERE t.company_id = $1 AND ($2 = '' OR t.method_id = $2::uuid)
			AND ($3 = 'all' OR ($3 = 'open' AND t.status = 'approved' AND t.settlement_id IS NULL AND t.pos_order_id IS NOT NULL)
			  OR ($3 = 'unused' AND t.status = 'approved' AND t.pos_order_id IS NULL) OR ($3 = 'settled' AND t.settlement_id IS NOT NULL))
			ORDER BY t.created_at DESC LIMIT 500`, r.PathValue("cid"), method, state)
	})
}

// POST /pos/terminal/txns/{id}/void: a charge the sale never used (the customer changed their mind) is cancelled on
// the terminal.
func (s *Server) voidCardTxn(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var status string
		var order, settle *string
		err := tx.QueryRow(ctx, `SELECT status, pos_order_id::text, settlement_id::text FROM card_txns WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).
			Scan(&status, &order, &settle)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if status != "approved" || order != nil || settle != nil {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if _, err := tx.Exec(ctx, `UPDATE card_txns SET status = 'voided' WHERE id = $1`, id); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+cardTxnCols+cardTxnFrom+` WHERE t.id = $1`, id)
	})
}

// linkCardTxns ties the approved terminal charges to the sale. A card box with a terminal must be fully covered by
// its charges (no card tender without an approval code).
func linkCardTxns(ctx context.Context, tx pgx.Tx, cid, orderID string, txnIDs []string, tenders []tenderIn) error {
	if len(txnIDs) > 10 || !allUUID(txnIDs) {
		return badRequest("invalid_card_txn")
	}
	byMethod := map[string]decimal.Decimal{}
	for _, t := range tenders {
		a, _ := money(t.Amount)
		byMethod[t.MethodID] = byMethod[t.MethodID].Add(a)
	}
	charged := map[string]decimal.Decimal{}
	for _, id := range txnIDs {
		var method, amt string
		err := tx.QueryRow(ctx, `SELECT method_id::text, amount::text FROM card_txns WHERE id = $1 AND company_id = $2 AND status = 'approved'
			AND pos_order_id IS NULL AND settlement_id IS NULL FOR UPDATE`, id, cid).Scan(&method, &amt)
		if errors.Is(err, pgx.ErrNoRows) {
			return badRequest("invalid_card_txn")
		}
		if err != nil {
			return err
		}
		a, _ := decimal.NewFromString(amt)
		charged[method] = charged[method].Add(a)
		if _, err := tx.Exec(ctx, `UPDATE card_txns SET pos_order_id = $2 WHERE id = $1`, id, orderID); err != nil {
			return err
		}
	}
	rows, err := tx.Query(ctx, `SELECT id::text FROM payment_methods WHERE company_id = $1 AND kind = 'card' AND terminal_driver <> ''`, cid)
	if err != nil {
		return err
	}
	withTerminal, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	isTerm := map[string]bool{}
	for _, m := range withTerminal {
		isTerm[m] = true
	}
	for m, a := range charged {
		if !isTerm[m] || !a.Equal(byMethod[m]) {
			return &apiError{status: http.StatusUnprocessableEntity, code: "card_not_charged", detail: a.String()}
		}
	}
	for m, a := range byMethod {
		if isTerm[m] && a.IsPositive() && !charged[m].Equal(a) {
			return &apiError{status: http.StatusUnprocessableEntity, code: "card_not_charged", detail: a.String()}
		}
	}
	return nil
}

const settleCols = `cs.id, cs.number, cs.method_id, m.name AS method, cs.bank_method_id, b.name AS bank_method, cs.settle_date, cs.currency,
	cs.gross::text AS gross, cs.fee::text AS fee, cs.net::text AS net, cs.txns, cs.batch_ref, cs.status, cs.journal_entry_id, cs.void_entry_id, cs.created_at`
const settleFrom = ` FROM card_settlements cs JOIN payment_methods m ON m.id = cs.method_id JOIN payment_methods b ON b.id = cs.bank_method_id`

func (s *Server) listCardSettlements(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+settleCols+settleFrom+` WHERE cs.company_id = $1 ORDER BY cs.settle_date DESC, cs.number DESC LIMIT 300`, r.PathValue("cid"))
	})
}

// POST /pos/terminal/settlements {method_id, date, bank_method_id, txn_ids (default: every open charge up to date),
// fee (default from the box's % + fixed per charge), batch_ref}
func (s *Server) settleCards(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MethodID     string   `json:"method_id"`
		Date         string   `json:"date"`
		BankMethodID string   `json:"bank_method_id"`
		TxnIDs       []string `json:"txn_ids"`
		Fee          string   `json:"fee"`
		BatchRef     string   `json:"batch_ref"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Date == "" {
		in.Date = today()
	}
	if !isUUID(in.MethodID) || !validDate(in.Date) || (in.BankMethodID != "" && !isUUID(in.BankMethodID)) || len(in.TxnIDs) > 5000 ||
		!allUUID(in.TxnIDs) || len(in.BatchRef) > 60 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var kind, cur, card, account string
		var pct, fixed string
		var settle *string
		err := tx.QueryRow(ctx, `SELECT kind, currency::text, name, account_id::text, card_fee_pct::text, card_fee_fixed::text, settle_method_id::text
			FROM payment_methods WHERE id = $1 AND company_id = $2 FOR UPDATE`, in.MethodID, cid).Scan(&kind, &cur, &card, &account, &pct, &fixed, &settle)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if kind != "card" {
			return nil, badRequest("not_a_card_box")
		}
		bankID := in.BankMethodID
		if bankID == "" {
			bankID = deref(settle)
		}
		if bankID == "" || bankID == in.MethodID {
			return nil, badRequest("bank_required")
		}
		var bankAcc, bankCur, bankKind, bankName string
		if err := tx.QueryRow(ctx, `SELECT account_id::text, currency::text, kind, name FROM payment_methods WHERE id = $1 AND company_id = $2 AND is_active`,
			bankID, cid).Scan(&bankAcc, &bankCur, &bankKind, &bankName); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, badRequest("invalid_bank")
			}
			return nil, err
		}
		if bankCur != cur || bankKind == "card" {
			return nil, badRequest("invalid_bank")
		}
		var rows pgx.Rows
		if len(in.TxnIDs) > 0 {
			rows, err = tx.Query(ctx, `SELECT id::text, amount::text FROM card_txns WHERE company_id = $1 AND method_id = $2 AND id = ANY ($3::uuid[])
				AND status = 'approved' AND settlement_id IS NULL AND pos_order_id IS NOT NULL FOR UPDATE`, cid, in.MethodID, in.TxnIDs)
		} else {
			rows, err = tx.Query(ctx, `SELECT id::text, amount::text FROM card_txns WHERE company_id = $1 AND method_id = $2 AND created_at::date <= $3::date
				AND status = 'approved' AND settlement_id IS NULL AND pos_order_id IS NOT NULL FOR UPDATE`, cid, in.MethodID, in.Date)
		}
		if err != nil {
			return nil, err
		}
		type txn struct{ id, amt string }
		list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (txn, error) {
			var t txn
			err := row.Scan(&t.id, &t.amt)
			return t, err
		})
		if err != nil {
			return nil, err
		}
		if len(list) == 0 || (len(in.TxnIDs) > 0 && len(list) != len(in.TxnIDs)) {
			return nil, badRequest("nothing_to_settle")
		}
		dec, err := currencyDecimals(ctx, tx, cur)
		if err != nil {
			return nil, err
		}
		gross := decimal.Zero
		ids := make([]string, len(list))
		for i, t := range list {
			a, _ := decimal.NewFromString(t.amt)
			gross = gross.Add(a)
			ids[i] = t.id
		}
		p, _ := decimal.NewFromString(pct)
		f, _ := decimal.NewFromString(fixed)
		fee := gross.Mul(p).Div(decimal.NewFromInt(100)).Add(f.Mul(decimal.NewFromInt(int64(len(list))))).Round(dec)
		if strings.TrimSpace(in.Fee) != "" {
			v, ok := money(in.Fee)
			if !ok || v.IsNegative() {
				return nil, badRequest("invalid_amount")
			}
			fee = v.Round(dec)
		}
		net := gross.Sub(fee)
		if !net.IsPositive() {
			return nil, badRequest("invalid_amount")
		}
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		rate, err := resolveRate(ctx, tx, cid, set.Base, cur, in.Date, "")
		if err != nil {
			return nil, err
		}
		var number int64
		if err := tx.QueryRow(ctx, `SELECT coalesce(max(number), 0) + 1 FROM card_settlements WHERE company_id = $1`, cid).Scan(&number); err != nil {
			return nil, err
		}
		lines := []jline{
			{Account: bankAcc, Desc: bankName + " · " + card, Currency: cur, Debit: net, Rate: rate},
			{Account: account, Desc: card, Currency: cur, Credit: gross, Rate: rate},
		}
		if fee.IsPositive() {
			feeAcc, err := ensureAccount(ctx, tx, cid, []string{"6275", "627", "6800"}, "expense", "Card processing fees", "عمولات البطاقات المصرفية",
				"Frais de cartes bancaires", []string{"62", "6"})
			if err != nil {
				return nil, err
			}
			lines = append(lines, jline{Account: feeAcc, Desc: "Card fees · " + card, Currency: cur, Debit: fee, Rate: rate})
		}
		je, err := postJournal(ctx, tx, cid, set, in.Date, fmt.Sprintf("Card settlement #%d · %s", number, card), fmt.Sprintf("CARD-%d", number), "card_settlement", lines)
		if err != nil {
			return nil, err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO card_settlements (company_id, number, method_id, bank_method_id, settle_date, currency, gross, fee, net, txns, batch_ref, journal_entry_id)
			VALUES ($1, $2, $3, $4, $5::date, $6, $7::numeric, $8::numeric, $9::numeric, $10, trim($11), $12) RETURNING id::text`,
			cid, number, in.MethodID, bankID, in.Date, cur, gross.String(), fee.String(), net.String(), len(list), in.BatchRef, je).Scan(&id); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE card_txns SET settlement_id = $2 WHERE id = ANY ($1::uuid[])`, ids, id); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+settleCols+settleFrom+` WHERE cs.id = $1`, id)
	})
}

// POST /pos/terminal/settlements/{id}/void: the entry is reversed and the charges wait again.
func (s *Server) voidCardSettlement(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var status, je string
		var number int64
		err := tx.QueryRow(ctx, `SELECT status, number, journal_entry_id::text FROM card_settlements WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).
			Scan(&status, &number, &je)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if status != "posted" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		var matched bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM bank_statement_lines l JOIN journal_lines jl ON jl.id = l.journal_line_id WHERE jl.entry_id = $1)`, je).
			Scan(&matched); err != nil {
			return nil, err
		}
		if matched {
			return nil, &apiError{status: http.StatusConflict, code: "reconciled"}
		}
		var rev string
		if err := tx.QueryRow(ctx, `SELECT reverse_journal_entry($1, NULL, $2)::text`, je, fmt.Sprintf("Void card settlement #%d", number)).Scan(&rev); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE card_settlements SET status = 'void', void_entry_id = $2 WHERE id = $1`, id, rev); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE card_txns SET settlement_id = NULL WHERE settlement_id = $1`, id); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+settleCols+settleFrom+` WHERE cs.id = $1`, id)
	})
}

// ======================= restaurant QR ordering =======================

// GET /rest/qr: settings and the link of every table.
func (s *Server) restQRSettings(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	if r.Method == http.MethodPut {
		var in struct {
			Enabled    bool   `json:"enabled"`
			RegisterID string `json:"register_id"`
		}
		if !decodeBody(w, r, &in) {
			return
		}
		if in.RegisterID != "" && !isUUID(in.RegisterID) || (in.Enabled && in.RegisterID == "") {
			writeErr(w, http.StatusBadRequest, "register_required")
			return
		}
		s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
			if in.RegisterID != "" {
				if _, err := loadRegister(ctx, tx, cid, in.RegisterID); err != nil {
					return nil, err
				}
			}
			if _, err := tx.Exec(ctx, `UPDATE companies SET qr_ordering = $2, qr_register_id = nullif($3, '')::uuid WHERE id = $1`, cid, in.Enabled, in.RegisterID); err != nil {
				return nil, err
			}
			if in.Enabled {
				// every table gets its link the first time
				rows, err := tx.Query(ctx, `SELECT id::text FROM rest_tables WHERE company_id = $1 AND qr_token IS NULL`, cid)
				if err != nil {
					return nil, err
				}
				ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
				if err != nil {
					return nil, err
				}
				for _, t := range ids {
					if _, err := tx.Exec(ctx, `UPDATE rest_tables SET qr_token = $2 WHERE id = $1`, t, randToken(24)); err != nil {
						return nil, err
					}
				}
			}
			return loadQRSettings(ctx, tx, cid)
		})
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) { return loadQRSettings(ctx, tx, cid) })
}

func loadQRSettings(ctx context.Context, tx pgx.Tx, cid string) (any, error) {
	set, err := queryJSONOne(ctx, tx, `SELECT c.qr_ordering AS enabled, c.qr_register_id AS register_id, g.name AS register FROM companies c
		LEFT JOIN pos_registers g ON g.id = c.qr_register_id WHERE c.id = $1`, cid)
	if err != nil {
		return nil, err
	}
	tables, err := queryJSON(ctx, tx, `SELECT t.id, t.name, t.area, CASE WHEN t.qr_token IS NOT NULL THEN $2 || '/#/menu/' || t.qr_token END AS url
		FROM rest_tables t WHERE t.company_id = $1 AND t.is_active ORDER BY t.area, t.name`, cid, publicURL())
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"settings": set, "tables": tables}, nil
}

// POST /rest/tables/{id}/qr: a new link for the table (the old printed QR stops working).
func (s *Server) restTableQR(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		tok := randToken(24)
		tag, err := tx.Exec(ctx, `UPDATE rest_tables SET qr_token = $3 WHERE id = $1 AND company_id = $2`, id, cid, tok)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return map[string]string{"id": id, "url": publicURL() + "/#/menu/" + tok}, nil
	})
}

func (s *Server) qrScope(w http.ResponseWriter, r *http.Request, write bool) (sc db.Scope, cid, table string, ok bool) {
	ip := clientIP(r)
	lim := publicReadLimiter
	if write {
		lim = publicWriteLimiter
	}
	if !lim.allow(ip) {
		writeErr(w, http.StatusTooManyRequests, "too_many_attempts")
		return
	}
	lim.fail(ip)
	tok := r.PathValue("token")
	if len(tok) < 32 || len(tok) > 128 {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	var tid string
	if err := s.Pool.QueryRow(r.Context(), `SELECT tenant_id::text, company_id::text, table_id::text FROM qr_lookup($1)`, tok).Scan(&tid, &cid, &table); err != nil {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	return db.Scope{TenantID: tid, CompanyID: cid, IP: ip}, cid, table, true
}

// GET /api/v1/menu/{token}: the menu of the restaurant for this table (prices in the till's currency).
func (s *Server) qrMenu(w http.ResponseWriter, r *http.Request) {
	sc, cid, table, ok := s.qrScope(w, r, false)
	if !ok {
		return
	}
	s.runScoped(w, r, sc, func(ctx context.Context, tx pgx.Tx) (any, error) {
		info, cur, err := qrInfo(ctx, tx, cid, table)
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
		rows, err := tx.Query(ctx, `SELECT it.id::text, it.name, it.name_i18n::text, coalesce(nullif(it.shop_description, ''), it.description, ''), it.category_id::text, ip.price::text,
			it.sale_price::text, coalesce(it.sale_currency, $2)::text, false
			FROM items it LEFT JOIN item_prices ip ON ip.item_id = it.id AND ip.currency = $3
			WHERE it.company_id = $1 AND it.is_active AND it.is_sellable AND NOT it.is_template AND it.kind <> 'consumable'
			ORDER BY it.name LIMIT 500`, cid, set.Base, cur)
		if err != nil {
			return nil, err
		}
		type menuItem struct {
			ID       string          `json:"id"`
			Name     string          `json:"name"`
			NameI18n json.RawMessage `json:"name_i18n"`
			Desc     string          `json:"description"`
			Category *string         `json:"category_id"`
			Price    string          `json:"price"`
			HasImage bool            `json:"has_image"`
		}
		items := []menuItem{}
		type raw struct {
			m                   menuItem
			list                *string
			sale, saleCur, i18n string
		}
		var all []raw
		for rows.Next() {
			var x raw
			if err := rows.Scan(&x.m.ID, &x.m.Name, &x.i18n, &x.m.Desc, &x.m.Category, &x.list, &x.sale, &x.saleCur, &x.m.HasImage); err != nil {
				rows.Close()
				return nil, err
			}
			all = append(all, x)
		}
		rows.Close()
		for _, x := range all {
			p, err := itemPriceIn(rc, x.list, x.sale, x.saleCur, cur)
			if err != nil || !p.IsPositive() {
				continue // no price in this currency: not on the menu
			}
			x.m.Price = p.Round(dec).StringFixed(dec)
			x.m.NameI18n = json.RawMessage(x.i18n)
			items = append(items, x.m)
		}
		cats, err := queryJSON(ctx, tx, `SELECT id, name, name_i18n, color FROM item_categories WHERE company_id = $1 AND is_active ORDER BY seq, name`, cid)
		if err != nil {
			return nil, err
		}
		return map[string]any{"info": info, "currency": cur, "decimals": dec, "items": items, "categories": cats}, nil
	})
}

func qrInfo(ctx context.Context, tx pgx.Tx, cid, table string) (json.RawMessage, string, error) {
	var cur string
	if err := tx.QueryRow(ctx, `SELECT g.currency::text FROM companies c JOIN pos_registers g ON g.id = c.qr_register_id WHERE c.id = $1`, cid).Scan(&cur); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", &apiError{status: http.StatusNotFound, code: "menu_closed"}
		}
		return nil, "", err
	}
	info, err := queryJSONOne(ctx, tx, `SELECT c.name_en, c.name_ar, c.logo, c.default_locale, t.name AS table_name, t.area
		FROM companies c JOIN rest_tables t ON t.company_id = c.id WHERE c.id = $1 AND t.id = $2`, cid, table)
	return info, cur, err
}

// POST /api/v1/menu/{token}/orders {lines: [{item_id, quantity, note}], name, note, website}: the guest's order, waiting for staff.
func (s *Server) qrPlaceOrder(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Lines []struct {
			ItemID   string `json:"item_id"`
			Quantity string `json:"quantity"`
			Note     string `json:"note"`
		} `json:"lines"`
		Name    string `json:"name"`
		Note    string `json:"note"`
		Website string `json:"website"` // honeypot
	}
	if !decodeBody(w, r, &in) {
		return
	}
	in.Name, in.Note = plainName(strings.TrimSpace(in.Name), 60), strings.TrimSpace(in.Note)
	if len(in.Lines) == 0 || len(in.Lines) > 40 || len([]rune(in.Note)) > 300 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	for _, l := range in.Lines {
		q, ok := money(l.Quantity)
		if !isUUID(l.ItemID) || !ok || !q.IsPositive() || q.GreaterThan(decimal.NewFromInt(50)) || !q.Equal(q.Truncate(0)) || len([]rune(l.Note)) > 120 {
			writeErr(w, http.StatusBadRequest, "invalid_line")
			return
		}
	}
	sc, cid, table, ok := s.qrScope(w, r, true)
	if !ok {
		return
	}
	if in.Website != "" {
		writeJSON(w, http.StatusOK, map[string]any{"number": 0, "status": "pending"})
		return
	}
	ip := clientIP(r)
	s.runScoped(w, r, sc, func(ctx context.Context, tx pgx.Tx) (any, error) {
		_, cur, err := qrInfo(ctx, tx, cid, table)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('qr:' || $1))`, table); err != nil {
			return nil, err
		}
		// a table sends at most 5 orders waiting at once and 20 an hour; a company at most 300 an hour
		var pending, hour, all int
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE table_id = $2 AND status = 'pending'),
			count(*) FILTER (WHERE table_id = $2 AND created_at > now() - interval '1 hour'), count(*) FILTER (WHERE created_at > now() - interval '1 hour')
			FROM qr_orders WHERE company_id = $1 AND created_at > now() - interval '1 day'`, cid, table).Scan(&pending, &hour, &all); err != nil {
			return nil, err
		}
		if pending >= 5 || hour >= 20 || all >= 300 {
			return nil, &apiError{status: http.StatusTooManyRequests, code: "too_many_orders"}
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
		total := decimal.Zero
		lines := []map[string]string{}
		for _, l := range in.Lines {
			var name string
			var list *string
			var sale, saleCur string
			err := tx.QueryRow(ctx, `SELECT it.name, ip.price::text, it.sale_price::text, coalesce(it.sale_currency, $3)::text FROM items it
				LEFT JOIN item_prices ip ON ip.item_id = it.id AND ip.currency = $4
				WHERE it.id = $1 AND it.company_id = $2 AND it.is_active AND it.is_sellable`, l.ItemID, cid, set.Base, cur).Scan(&name, &list, &sale, &saleCur)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, badRequest("invalid_item")
			}
			if err != nil {
				return nil, err
			}
			p, err := itemPriceIn(rc, list, sale, saleCur, cur)
			if err != nil {
				return nil, err
			}
			q, _ := money(l.Quantity)
			total = total.Add(p.Round(dec).Mul(q))
			lines = append(lines, map[string]string{"item_id": l.ItemID, "name": name, "quantity": q.String(), "price": p.Round(dec).String(),
				"note": plainName(strings.TrimSpace(l.Note), 120)})
		}
		lb, _ := json.Marshal(lines)
		var number int64
		if err := tx.QueryRow(ctx, `SELECT coalesce(max(number), 0) + 1 FROM qr_orders WHERE company_id = $1`, cid).Scan(&number); err != nil {
			return nil, err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO qr_orders (company_id, number, table_id, name, note, lines, total, currency, ip)
			VALUES ($1, $2, $3, $4, $5, $6, $7::numeric, $8, $9) RETURNING id::text`, cid, number, table, in.Name, plainName(in.Note, 300), lb,
			total.String(), cur, ip).Scan(&id); err != nil {
			return nil, err
		}
		var tname string
		_ = tx.QueryRow(ctx, `SELECT name FROM rest_tables WHERE id = $1`, table).Scan(&tname)
		if err := notifyStaff(ctx, tx, cid, "pos.create", "qr_order", fmt.Sprintf("QR order #%d · %s", number, tname),
			total.StringFixed(dec)+" "+cur, "", ""); err != nil {
			return nil, err
		}
		return map[string]any{"number": number, "status": "pending", "total": total.StringFixed(dec), "currency": cur}, nil
	})
}

// GET /api/v1/menu/{token}/orders: this table's orders of the last 4 hours (the guest sees them accepted).
func (s *Server) qrTableOrders(w http.ResponseWriter, r *http.Request) {
	sc, cid, table, ok := s.qrScope(w, r, false)
	if !ok {
		return
	}
	s.runScoped(w, r, sc, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT number, lines, total::text AS total, currency, status, created_at FROM qr_orders
			WHERE company_id = $1 AND table_id = $2 AND created_at > now() - interval '4 hours' ORDER BY created_at DESC LIMIT 20`, cid, table)
	})
}

const qrOrderCols = `q.id, q.number, q.table_id, t.name AS table_name, t.area, q.name, q.note, q.lines, q.total::text AS total, q.currency, q.status,
	q.check_id, k.number AS check_number, q.created_at, q.decided_at, u.full_name AS decided_by_name`
const qrOrderFrom = ` FROM qr_orders q JOIN rest_tables t ON t.id = q.table_id LEFT JOIN rest_checks k ON k.id = q.check_id LEFT JOIN users u ON u.id = q.decided_by`

// GET /rest/qr/orders?status=pending|all
func (s *Server) restQROrders(w http.ResponseWriter, r *http.Request) {
	st := r.URL.Query().Get("status")
	if st == "" {
		st = "pending"
	}
	if !contains([]string{"pending", "accepted", "rejected", "all"}, st) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+qrOrderCols+qrOrderFrom+` WHERE q.company_id = $1 AND ($2 = 'all' OR q.status = $2)
			ORDER BY q.created_at DESC LIMIT 200`, r.PathValue("cid"), st)
	})
}

// POST /rest/qr/orders/{id}/{action}: accept (onto the table's open check, opened if needed, and sent to the kitchen) or reject.
func (s *Server) restQRDecide(w http.ResponseWriter, r *http.Request) {
	cid, id, action := r.PathValue("cid"), r.PathValue("id"), r.PathValue("action")
	if !isUUID(id) || (action != "accept" && action != "reject") {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var status, table string
		var lb []byte
		err := tx.QueryRow(ctx, `SELECT status, table_id::text, lines FROM qr_orders WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&status, &table, &lb)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		if status != "pending" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if action == "reject" {
			if _, err := tx.Exec(ctx, `UPDATE qr_orders SET status = 'rejected', decided_by = app_user(), decided_at = now() WHERE id = $1`, id); err != nil {
				return nil, err
			}
			return queryJSONOne(ctx, tx, `SELECT `+qrOrderCols+qrOrderFrom+` WHERE q.id = $1`, id)
		}
		if _, err := tx.Exec(ctx, `SELECT 1 FROM rest_tables WHERE id = $1 FOR UPDATE`, table); err != nil {
			return nil, err
		}
		var check string
		err = tx.QueryRow(ctx, `SELECT id::text FROM rest_checks WHERE table_id = $1 AND status = 'open' ORDER BY opened_at LIMIT 1`, table).Scan(&check)
		if errors.Is(err, pgx.ErrNoRows) {
			var reg *string
			if err := tx.QueryRow(ctx, `SELECT qr_register_id::text FROM companies WHERE id = $1`, cid).Scan(&reg); err != nil {
				return nil, err
			}
			if reg == nil {
				return nil, badRequest("register_required")
			}
			var n int64
			if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'rest_check')`, cid).Scan(&n); err != nil {
				return nil, err
			}
			if err := tx.QueryRow(ctx, `INSERT INTO rest_checks (company_id, number, register_id, table_id, kind, notes)
				VALUES ($1, $2, $3, $4, 'dine_in', 'QR') RETURNING id::text`, cid, n, *reg, table).Scan(&check); err != nil {
				return nil, err
			}
		} else if err != nil {
			return nil, err
		}
		var raw []map[string]string
		_ = json.Unmarshal(lb, &raw)
		lines := make([]checkLineIn, 0, len(raw))
		for _, l := range raw {
			lines = append(lines, checkLineIn{ItemID: l["item_id"], Quantity: l["quantity"], Note: l["note"]})
		}
		if err := addCheckLines(ctx, tx, cid, check, lines, true); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE qr_orders SET status = 'accepted', check_id = $2, decided_by = app_user(), decided_at = now() WHERE id = $1`, id, check); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+qrOrderCols+qrOrderFrom+` WHERE q.id = $1`, id)
	})
}
