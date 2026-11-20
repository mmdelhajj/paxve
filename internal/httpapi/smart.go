package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Smart features (app "ai"), part 1: the alerts center. Deterministic checks — no AI key needed — compare the last
// 7 days with the company's own history (weekly buckets, z-score) or with fixed thresholds the owner can change:
//
//	diesel          litres per running hour of a generator jumped
//	discount        a cashier's discount share is far above their own history (or their colleagues')
//	cash_diff       a till closed with a shortage / overage above the limit; cash_repeat: small shortages that keep coming
//	voids           voids, credit notes and refunds by one user spiked
//	off_hours       payments recorded outside working hours
//	count_loss      a stock count lost more value than the limit
//	collector_drop  a collector brought in much less than usual
//	dup_bill        two supplier bills look like the same bill
//	fleet_fuel      the fuel flags of the fleet app (consumption jump, odometer back)
//
// Every alert has a fingerprint (kind + subject + period) so a scan never repeats one, and an alert someone
// resolved or dismissed is not raised again for the same period. A daily digest goes to the owner.

var smartKinds = []string{"diesel", "discount", "cash_diff", "cash_repeat", "voids", "off_hours", "count_loss", "collector_drop", "dup_bill", "fleet_fuel"}

// smartDefaults are the thresholds of the checks and the reorder planner (params of smart_settings override them).
var smartDefaults = map[string]float64{
	"z":                  2.5, // how unusual (standard deviations above the own history) before an alert
	"min_history":        4,   // weeks of history a check needs before it compares
	"diesel_min_pct":     20,  // diesel per hour at least this % above normal
	"discount_min_pct":   5,   // a cashier's discount share must be at least this %
	"cash_limit":         5,   // till shortage / overage limit, base currency
	"void_min":           3,   // voids / credit notes / refunds in 7 days before comparing
	"work_start":         7,   // working hours (company time): payments outside them are flagged
	"work_end":           21,
	"count_loss_limit":   50, // stock count loss limit, base currency
	"collector_drop_pct": 40, // collections this % under normal
	"lead_days":          7,  // reorder: supplier lead time when none is known
	"review_days":        14, // reorder: days between orders (max = min + demand × review days)
	"service_level":      95, // reorder: % of demand covered by the safety stock
}

type smartParams map[string]float64

func (p smartParams) get(k string) float64 {
	if v, ok := p[k]; ok {
		return v
	}
	return smartDefaults[k]
}

// smartParamLimits: allowed range of each parameter.
var smartParamLimits = map[string][2]float64{
	"z": {1, 10}, "min_history": {2, 26}, "diesel_min_pct": {0, 500}, "discount_min_pct": {0, 100}, "cash_limit": {0, 1e9},
	"void_min": {1, 1000}, "work_start": {0, 23}, "work_end": {1, 24}, "count_loss_limit": {0, 1e12}, "collector_drop_pct": {5, 100},
	"lead_days": {0, 365}, "review_days": {1, 180}, "service_level": {50, 99.9},
}

type smartSettingsRow struct {
	Params        smartParams `json:"params"`
	DigestEnabled bool        `json:"digest_enabled"`
	DigestHour    int         `json:"digest_hour"`
	DigestPhone   string      `json:"digest_phone"`
	LastScanAt    *time.Time  `json:"last_scan_at"`
	LastRiskAt    *time.Time  `json:"last_risk_at"`
	LastDigestOn  *string     `json:"last_digest_on"`
	Kinds         []string    `json:"kinds"` // the checks that are switched on
}

// loadSmartSettings reads the company's settings (defaults when none were saved).
func loadSmartSettings(ctx context.Context, tx pgx.Tx, cid string) (smartSettingsRow, error) {
	st := smartSettingsRow{Params: smartParams{}, DigestEnabled: true, DigestHour: 8}
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT params, digest_enabled, digest_hour, digest_phone, last_scan_at, last_risk_at, last_digest_on::text
		FROM smart_settings WHERE company_id = $1`, cid).Scan(&raw, &st.DigestEnabled, &st.DigestHour, &st.DigestPhone, &st.LastScanAt, &st.LastRiskAt, &st.LastDigestOn)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return st, err
	}
	var stored map[string]any
	_ = json.Unmarshal(raw, &stored)
	for k, v := range stored {
		if f, ok := v.(float64); ok {
			if _, known := smartDefaults[k]; known {
				st.Params[k] = f
			}
		}
	}
	st.Kinds = []string{}
	off := map[string]bool{}
	if l, ok := stored["off"].([]any); ok {
		for _, x := range l {
			if s, ok := x.(string); ok {
				off[s] = true
			}
		}
	}
	for _, k := range smartKinds {
		if !off[k] {
			st.Kinds = append(st.Kinds, k)
		}
	}
	return st, nil
}

func (st smartSettingsRow) on(kind string) bool { return contains(st.Kinds, kind) }

// GET /smart/settings: the thresholds (with their defaults) and the digest.
// PUT /smart/settings {params: {k: v}, kinds: [...], digest_enabled, digest_hour, digest_phone}
func (s *Server) smartSettings(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	var in struct {
		Params        map[string]float64 `json:"params"`
		Kinds         *[]string          `json:"kinds"`
		DigestEnabled *bool              `json:"digest_enabled"`
		DigestHour    *int               `json:"digest_hour"`
		DigestPhone   *string            `json:"digest_phone"`
	}
	if r.Method == http.MethodPut {
		if !decodeBody(w, r, &in) {
			return
		}
		for k, v := range in.Params {
			lim, ok := smartParamLimits[k]
			if !ok || math.IsNaN(v) || v < lim[0] || v > lim[1] {
				writeErr(w, http.StatusBadRequest, "bad_request")
				return
			}
		}
		if in.Kinds != nil {
			for _, k := range *in.Kinds {
				if !contains(smartKinds, k) {
					writeErr(w, http.StatusBadRequest, "bad_request")
					return
				}
			}
		}
		if (in.DigestHour != nil && (*in.DigestHour < 0 || *in.DigestHour > 23)) || (in.DigestPhone != nil && len(*in.DigestPhone) > 40) {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := companyBase(ctx, tx, cid); err != nil {
			return nil, err
		}
		if r.Method == http.MethodPut {
			st, err := loadSmartSettings(ctx, tx, cid)
			if err != nil {
				return nil, err
			}
			stored := map[string]any{}
			for k, v := range st.Params {
				stored[k] = v
			}
			for k, v := range in.Params {
				stored[k] = v
			}
			if ws, we := paramOr(stored, "work_start"), paramOr(stored, "work_end"); ws >= we {
				return nil, badRequest("bad_request")
			}
			kinds := st.Kinds
			if in.Kinds != nil {
				kinds = *in.Kinds
			}
			off := []string{}
			for _, k := range smartKinds {
				if !contains(kinds, k) {
					off = append(off, k)
				}
			}
			stored["off"] = off
			pj, _ := json.Marshal(stored)
			if _, err := tx.Exec(ctx, `INSERT INTO smart_settings (company_id, params, digest_enabled, digest_hour, digest_phone)
				VALUES ($1, $2::jsonb, coalesce($3, true), coalesce($4, 8), coalesce(trim($5), ''))
				ON CONFLICT (company_id) DO UPDATE SET params = EXCLUDED.params,
				  digest_enabled = coalesce($3, smart_settings.digest_enabled), digest_hour = coalesce($4, smart_settings.digest_hour),
				  digest_phone = coalesce(trim($5), smart_settings.digest_phone), updated_at = now()`,
				cid, string(pj), in.DigestEnabled, in.DigestHour, in.DigestPhone); err != nil {
				return nil, err
			}
		}
		st, err := loadSmartSettings(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		eff := map[string]float64{}
		for k := range smartDefaults {
			eff[k] = st.Params.get(k)
		}
		return map[string]any{"params": eff, "defaults": smartDefaults, "kinds": st.Kinds, "all_kinds": smartKinds,
			"digest_enabled": st.DigestEnabled, "digest_hour": st.DigestHour, "digest_phone": st.DigestPhone,
			"last_scan_at": st.LastScanAt, "last_risk_at": st.LastRiskAt, "last_digest_on": st.LastDigestOn}, nil
	})
}

func paramOr(m map[string]any, k string) float64 {
	if v, ok := m[k].(float64); ok {
		return v
	}
	return smartDefaults[k]
}

// ======================= statistics =======================

func meanSD(xs []float64) (float64, float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	m := sum / float64(len(xs))
	if len(xs) < 2 {
		return m, 0
	}
	var ss float64
	for _, x := range xs {
		ss += (x - m) * (x - m)
	}
	return m, math.Sqrt(ss / float64(len(xs)-1))
}

// zOf: how many standard deviations x is above the mean (a flat history counts any rise as very unusual, with a
// floor of 10% of the mean so that tiny noise on a flat line does not alarm).
func zOf(x, m, sd float64) float64 {
	floor := math.Abs(m) * 0.1
	if sd < floor {
		sd = floor
	}
	if sd == 0 {
		if x > m {
			return 99
		}
		return 0
	}
	return (x - m) / sd
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

// ======================= the checks =======================

type alertCand struct {
	Kind, Severity, Fingerprint, SubjectType, SubjectLabel, Period string
	SubjectID                                                      *string
	Value, Expected, Score                                         *float64
	Detail                                                         map[string]any
}

func fp(f float64) *float64 { v := round2(f); return &v }
func sp(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// weekKey names the current 7-day window (alerts of the same subject are raised at most once a week).
func weekKey() string {
	y, wk := time.Now().ISOWeek()
	return fmt.Sprintf("%d-W%02d", y, wk)
}

func periodLast7() string {
	return time.Now().AddDate(0, 0, -6).Format(time.DateOnly) + ".." + today()
}

// bucketRows: rows of (subject, label, bucket, value...) grouped by subject.
type bucketSeries struct {
	label string
	vals  map[int][]float64
}

func collectBuckets(rows pgx.Rows, nvals int) (map[string]*bucketSeries, []string, error) {
	out := map[string]*bucketSeries{}
	order := []string{}
	defer rows.Close()
	for rows.Next() {
		var id, label string
		var b int
		vals := make([]float64, nvals)
		dest := []any{&id, &label, &b}
		for i := range vals {
			dest = append(dest, &vals[i])
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, nil, err
		}
		s, ok := out[id]
		if !ok {
			s = &bucketSeries{label: label, vals: map[int][]float64{}}
			out[id] = s
			order = append(order, id)
		}
		s.vals[b] = vals
	}
	return out, order, rows.Err()
}

func checkDiesel(ctx context.Context, tx pgx.Tx, cid string, p smartParams) ([]alertCand, error) {
	rows, err := tx.Query(ctx, `
		WITH g AS (SELECT id, name FROM generators WHERE company_id = $1 AND status <> 'retired'),
		b AS (SELECT generate_series(0, 12) AS b)
		SELECT g.id::text, g.name, b.b,
		  coalesce((SELECT -sum(d.quantity) FROM stock_docs d WHERE d.generator_id = g.id AND d.kind = 'fuel' AND d.status = 'posted'
		            AND d.doc_date > current_date - 7 * (b.b + 1) AND d.doc_date <= current_date - 7 * b.b), 0)::float8,
		  coalesce((SELECT max(h.hour_meter) FROM generator_hours h WHERE h.generator_id = g.id AND h.log_date <= current_date - 7 * b.b), -1)::float8,
		  coalesce((SELECT max(h.hour_meter) FROM generator_hours h WHERE h.generator_id = g.id AND h.log_date <= current_date - 7 * (b.b + 1)), -1)::float8
		FROM g CROSS JOIN b ORDER BY 1, 3`, cid)
	if err != nil {
		return nil, err
	}
	series, order, err := collectBuckets(rows, 3)
	if err != nil {
		return nil, err
	}
	ratio := func(v []float64) (float64, bool) {
		if v == nil || v[1] < 0 || v[2] < 0 {
			return 0, false
		}
		h := v[1] - v[2]
		if h <= 0 || v[0] <= 0 {
			return 0, false
		}
		return v[0] / h, true
	}
	var out []alertCand
	for _, id := range order {
		s := series[id]
		cur, ok := ratio(s.vals[0])
		if !ok {
			continue
		}
		var hist []float64
		for b := 1; b <= 12; b++ {
			if r, ok := ratio(s.vals[b]); ok {
				hist = append(hist, r)
			}
		}
		if len(hist) < int(p.get("min_history")) {
			continue
		}
		m, sd := meanSD(hist)
		z := zOf(cur, m, sd)
		if z < p.get("z") || cur < m*(1+p.get("diesel_min_pct")/100) {
			continue
		}
		sev := "medium"
		if z >= p.get("z")+2 || cur >= m*1.5 {
			sev = "high"
		}
		out = append(out, alertCand{Kind: "diesel", Severity: sev, Fingerprint: "diesel:" + id + ":" + weekKey(), SubjectType: "generator",
			SubjectID: sp(id), SubjectLabel: s.label, Period: periodLast7(), Value: fp(cur), Expected: fp(m), Score: fp(z),
			Detail: map[string]any{"liters": round2(s.vals[0][0]), "hours": round2(s.vals[0][1] - s.vals[0][2]), "weeks": len(hist), "unit": "l/h"}})
	}
	return out, nil
}

func checkDiscounts(ctx context.Context, tx pgx.Tx, cid string, p smartParams) ([]alertCand, error) {
	rows, err := tx.Query(ctx, `
		SELECT o.cashier_id::text, coalesce(u.full_name, ''), ((current_date - o.order_date) / 7)::int,
		  coalesce(sum(smart_to_base($1, o.currency, l.quantity * l.unit_price)), 0)::float8,
		  coalesce(sum(smart_to_base($1, o.currency, greatest(l.quantity * l.unit_price - l.amount, 0))), 0)::float8,
		  count(*) FILTER (WHERE l.amount < l.quantity * l.unit_price - 0.0001)::float8
		FROM pos_orders o JOIN invoice_lines l ON l.invoice_id = o.invoice_id LEFT JOIN users u ON u.id = o.cashier_id
		WHERE o.company_id = $1 AND o.kind = 'sale' AND o.order_date > current_date - 91 AND o.order_date <= current_date AND o.cashier_id IS NOT NULL
		GROUP BY 1, 2, 3 ORDER BY 1, 3`, cid)
	if err != nil {
		return nil, err
	}
	series, order, err := collectBuckets(rows, 3)
	if err != nil {
		return nil, err
	}
	share := func(v []float64) (float64, bool) {
		if v == nil || v[0] <= 0 {
			return 0, false
		}
		return v[1] / v[0] * 100, true
	}
	var allG, allD float64
	for _, s := range series {
		if v := s.vals[0]; v != nil {
			allG += v[0]
			allD += v[1]
		}
	}
	var out []alertCand
	for _, id := range order {
		s := series[id]
		cur, ok := share(s.vals[0])
		if !ok || cur < p.get("discount_min_pct") {
			continue
		}
		var hist []float64
		for b := 1; b <= 12; b++ {
			if v, ok := share(s.vals[b]); ok {
				hist = append(hist, v)
			}
		}
		// colleagues this week (without this cashier)
		peerG, peerD := allG-s.vals[0][0], allD-s.vals[0][1]
		peer := -1.0
		if peerG > 0 {
			peer = peerD / peerG * 100
		}
		var z, expected float64
		switch {
		case len(hist) >= int(p.get("min_history")):
			m, sd := meanSD(hist)
			z, expected = zOf(cur, m, sd), m
			if z < p.get("z") {
				continue
			}
		case peer >= 0 && cur >= 2*math.Max(peer, 1):
			z, expected = 0, peer
		default:
			continue
		}
		sev := "medium"
		if cur >= 3*math.Max(expected, 1) {
			sev = "high"
		}
		out = append(out, alertCand{Kind: "discount", Severity: sev, Fingerprint: "discount:" + id + ":" + weekKey(), SubjectType: "user",
			SubjectID: sp(id), SubjectLabel: s.label, Period: periodLast7(), Value: fp(cur), Expected: fp(expected), Score: fp(z),
			Detail: map[string]any{"gross": round2(s.vals[0][0]), "discount": round2(s.vals[0][1]), "lines": s.vals[0][2], "peer_pct": round2(peer),
				"weeks": len(hist), "unit": "%"}})
	}
	return out, nil
}

func checkCash(ctx context.Context, tx pgx.Tx, cid string, p smartParams) ([]alertCand, error) {
	limit := p.get("cash_limit")
	rows, err := tx.Query(ctx, `
		SELECT s.id::text, s.number, r.name, coalesce(u.full_name, ''), coalesce(s.closed_by::text, ''), s.closed_at::date::text,
		  coalesce((SELECT sum(smart_to_base($1, e->>'currency', (e->>'difference')::numeric)) FROM jsonb_array_elements(s.counted) e
		            WHERE e ? 'difference' AND e ? 'currency' AND (e->>'difference') ~ '^-?[0-9]+(\.[0-9]+)?$'), 0)::float8
		FROM pos_sessions s JOIN pos_registers r ON r.id = s.register_id LEFT JOIN users u ON u.id = s.closed_by
		WHERE s.company_id = $1 AND s.status = 'closed' AND s.closed_at > now() - interval '30 days' ORDER BY s.closed_at`, cid)
	if err != nil {
		return nil, err
	}
	type sess struct {
		id, reg, user, uid, day string
		num                     int64
		diff                    float64
	}
	var list []sess
	for rows.Next() {
		var x sess
		if err := rows.Scan(&x.id, &x.num, &x.reg, &x.user, &x.uid, &x.day, &x.diff); err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []alertCand
	short := map[string][]sess{}
	recent := time.Now().AddDate(0, 0, -14).Format(time.DateOnly)
	for _, x := range list {
		if x.diff < 0 && x.uid != "" {
			short[x.uid] = append(short[x.uid], x)
		}
		if math.Abs(x.diff) <= limit || x.day < recent {
			continue
		}
		sev := "medium"
		if math.Abs(x.diff) > 4*limit {
			sev = "high"
		}
		out = append(out, alertCand{Kind: "cash_diff", Severity: sev, Fingerprint: "cash_diff:" + x.id, SubjectType: "pos_session", SubjectID: sp(x.id),
			SubjectLabel: fmt.Sprintf("%s #%d · %s", x.reg, x.num, x.user), Period: x.day, Value: fp(x.diff), Expected: fp(0),
			Detail: map[string]any{"register": x.reg, "session": x.num, "user": x.user, "limit": limit}})
	}
	// small shortages that keep coming back (each under the limit, together over twice the limit)
	for uid, l := range short {
		var total float64
		for _, x := range l {
			total += -x.diff
		}
		if len(l) < 3 || total <= 2*limit {
			continue
		}
		out = append(out, alertCand{Kind: "cash_repeat", Severity: "medium", Fingerprint: "cash_repeat:" + uid + ":" + weekKey(), SubjectType: "user",
			SubjectID: sp(uid), SubjectLabel: l[0].user, Period: l[0].day + ".." + l[len(l)-1].day, Value: fp(-total), Expected: fp(0),
			Detail: map[string]any{"sessions": len(l), "limit": limit}})
	}
	return out, nil
}

func checkVoids(ctx context.Context, tx pgx.Tx, cid string, p smartParams) ([]alertCand, error) {
	rows, err := tx.Query(ctx, `
		WITH v AS (
		  SELECT je.created_by AS uid, je.created_at::date AS d, 'invoice' AS what FROM invoices i JOIN journal_entries je ON je.id = i.void_entry_id
		  WHERE i.company_id = $1 AND i.status = 'void' AND je.created_at > now() - interval '91 days'
		  UNION ALL
		  SELECT je.created_by, je.created_at::date, 'payment' FROM payments p JOIN journal_entries je ON je.id = p.void_entry_id
		  WHERE p.company_id = $1 AND p.status = 'void' AND je.created_at > now() - interval '91 days'
		  UNION ALL
		  SELECT p.received_by, p.created_at::date, 'credit_note' FROM payments p
		  WHERE p.company_id = $1 AND p.kind = 'credit_note' AND p.created_at > now() - interval '91 days'
		  UNION ALL
		  SELECT o.cashier_id, o.order_date, 'refund' FROM pos_orders o WHERE o.company_id = $1 AND o.kind = 'refund' AND o.order_date > current_date - 91)
		SELECT v.uid::text, coalesce(u.full_name, ''), ((current_date - v.d) / 7)::int, count(*)::float8,
		  count(*) FILTER (WHERE v.what = 'invoice')::float8, count(*) FILTER (WHERE v.what = 'payment')::float8,
		  count(*) FILTER (WHERE v.what IN ('credit_note', 'refund'))::float8
		FROM v LEFT JOIN users u ON u.id = v.uid WHERE v.uid IS NOT NULL AND v.d <= current_date GROUP BY 1, 2, 3 ORDER BY 1, 3`, cid)
	if err != nil {
		return nil, err
	}
	series, order, err := collectBuckets(rows, 4)
	if err != nil {
		return nil, err
	}
	var out []alertCand
	for _, id := range order {
		s := series[id]
		v0 := s.vals[0]
		if v0 == nil || v0[0] < p.get("void_min") {
			continue
		}
		hist := make([]float64, 12)
		for b := 1; b <= 12; b++ {
			if v := s.vals[b]; v != nil {
				hist[b-1] = v[0]
			}
		}
		m, sd := meanSD(hist)
		z := zOf(v0[0], m, sd)
		if z < p.get("z") {
			continue
		}
		sev := "medium"
		if v0[0] >= 3*math.Max(m, 1) {
			sev = "high"
		}
		out = append(out, alertCand{Kind: "voids", Severity: sev, Fingerprint: "voids:" + id + ":" + weekKey(), SubjectType: "user", SubjectID: sp(id),
			SubjectLabel: s.label, Period: periodLast7(), Value: fp(v0[0]), Expected: fp(m), Score: fp(z),
			Detail: map[string]any{"invoices": v0[1], "payments": v0[2], "credit_notes": v0[3]}})
	}
	return out, nil
}

func checkOffHours(ctx context.Context, tx pgx.Tx, cid string, p smartParams) ([]alertCand, error) {
	rows, err := tx.Query(ctx, `
		SELECT coalesce(p.received_by::text, ''), coalesce(u.full_name, ''), (p.created_at AT TIME ZONE c.timezone)::date::text,
		  count(*), coalesce(sum(smart_to_base($1, p.currency, p.amount)), 0)::float8,
		  array_agg(p.number ORDER BY p.number), min(to_char(p.created_at AT TIME ZONE c.timezone, 'HH24:MI')), max(to_char(p.created_at AT TIME ZONE c.timezone, 'HH24:MI'))
		FROM payments p JOIN companies c ON c.id = p.company_id LEFT JOIN users u ON u.id = p.received_by
		WHERE p.company_id = $1 AND p.kind = 'payment' AND p.status = 'posted' AND p.created_at > now() - interval '3 days'
		  AND (extract(hour FROM p.created_at AT TIME ZONE c.timezone) < $2 OR extract(hour FROM p.created_at AT TIME ZONE c.timezone) >= $3)
		GROUP BY 1, 2, 3 ORDER BY 3`, cid, p.get("work_start"), p.get("work_end"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []alertCand
	for rows.Next() {
		var uid, name, day, first, last string
		var n int64
		var total float64
		var nums []int64
		if err := rows.Scan(&uid, &name, &day, &n, &total, &nums, &first, &last); err != nil {
			return nil, err
		}
		if len(nums) > 20 {
			nums = nums[:20]
		}
		sev := "low"
		if n >= 5 {
			sev = "medium"
		}
		out = append(out, alertCand{Kind: "off_hours", Severity: sev, Fingerprint: "off_hours:" + uid + ":" + day, SubjectType: "user", SubjectID: sp(uid),
			SubjectLabel: name, Period: day, Value: fp(total), Expected: fp(0),
			Detail: map[string]any{"payments": n, "numbers": nums, "first": first, "last": last, "work_start": p.get("work_start"), "work_end": p.get("work_end")}})
	}
	return out, rows.Err()
}

func checkCountLoss(ctx context.Context, tx pgx.Tx, cid string, p smartParams) ([]alertCand, error) {
	rows, err := tx.Query(ctx, `
		SELECT sc.id::text, sc.number, w.name, sc.count_date::text,
		  coalesce((SELECT sum(abs(m.value)) FROM stock_moves m WHERE m.company_id = $1 AND m.source = 'count' AND m.source_id = sc.id AND m.quantity < 0), 0)::float8,
		  coalesce((SELECT sum(abs(m.value)) FROM stock_moves m WHERE m.company_id = $1 AND m.source = 'count' AND m.source_id = sc.id AND m.quantity > 0), 0)::float8,
		  (SELECT count(*) FROM stock_count_lines l WHERE l.count_id = sc.id AND l.counted IS NOT NULL AND l.counted < l.expected)
		FROM stock_counts sc JOIN warehouses w ON w.id = sc.warehouse_id
		WHERE sc.company_id = $1 AND sc.status = 'posted' AND coalesce(sc.posted_at, sc.created_at) > now() - interval '30 days'`, cid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	limit := p.get("count_loss_limit")
	var out []alertCand
	for rows.Next() {
		var id, wh, day string
		var num, lines int64
		var loss, gain float64
		if err := rows.Scan(&id, &num, &wh, &day, &loss, &gain, &lines); err != nil {
			return nil, err
		}
		net := loss - gain
		if net <= limit {
			continue
		}
		sev := "medium"
		if net > 4*limit {
			sev = "high"
		}
		out = append(out, alertCand{Kind: "count_loss", Severity: sev, Fingerprint: "count_loss:" + id, SubjectType: "stock_count", SubjectID: sp(id),
			SubjectLabel: fmt.Sprintf("%s #%d", wh, num), Period: day, Value: fp(net), Expected: fp(limit),
			Detail: map[string]any{"loss": round2(loss), "gain": round2(gain), "lines": lines, "warehouse": wh, "count": num}})
	}
	return out, rows.Err()
}

func checkCollectors(ctx context.Context, tx pgx.Tx, cid string, p smartParams) ([]alertCand, error) {
	rows, err := tx.Query(ctx, `
		SELECT p.received_by::text, u.full_name, ((current_date - p.payment_date) / 7)::int,
		  coalesce(sum(smart_to_base($1, p.currency, p.amount)), 0)::float8, count(*)::float8
		FROM payments p JOIN users u ON u.id = p.received_by
		WHERE p.company_id = $1 AND p.kind = 'payment' AND p.status = 'posted' AND p.payment_date > current_date - 63
		  AND p.payment_date <= current_date AND u.role = 'collector' AND u.is_active
		GROUP BY 1, 2, 3 ORDER BY 1, 3`, cid)
	if err != nil {
		return nil, err
	}
	series, order, err := collectBuckets(rows, 2)
	if err != nil {
		return nil, err
	}
	var out []alertCand
	for _, id := range order {
		s := series[id]
		cur := 0.0
		if v := s.vals[0]; v != nil {
			cur = v[0]
		}
		hist := make([]float64, 0, 8)
		active := 0
		for b := 1; b <= 8; b++ {
			v := 0.0
			if x := s.vals[b]; x != nil {
				v = x[0]
				active++
			}
			hist = append(hist, v)
		}
		if active < int(p.get("min_history")) {
			continue
		}
		m, sd := meanSD(hist)
		if m <= 0 || cur >= m*(1-p.get("collector_drop_pct")/100) {
			continue
		}
		z := -zOf(cur, m, sd) // how far under
		if z < math.Min(p.get("z"), 2) {
			continue
		}
		sev := "medium"
		if cur <= m*0.25 {
			sev = "high"
		}
		out = append(out, alertCand{Kind: "collector_drop", Severity: sev, Fingerprint: "collector_drop:" + id + ":" + weekKey(), SubjectType: "user",
			SubjectID: sp(id), SubjectLabel: s.label, Period: periodLast7(), Value: fp(cur), Expected: fp(m), Score: fp(z),
			Detail: map[string]any{"drop_pct": round2((1 - cur/m) * 100), "weeks": active}})
	}
	return out, nil
}

func checkDupBills(ctx context.Context, tx pgx.Tx, cid string) ([]alertCand, error) {
	rows, err := tx.Query(ctx, `
		SELECT a.id::text, b.id::text, a.number, b.number, s.name, a.total::float8, b.total::float8, a.currency::text, a.bill_date::text, b.bill_date::text,
		  a.supplier_ref, b.supplier_ref, (a.supplier_ref <> '' AND lower(trim(a.supplier_ref)) = lower(trim(b.supplier_ref)))
		FROM bills a JOIN bills b ON b.company_id = a.company_id AND b.supplier_id = a.supplier_id AND b.id > a.id AND b.status <> 'void'
		JOIN suppliers s ON s.id = a.supplier_id
		WHERE a.company_id = $1 AND a.status <> 'void' AND b.created_at > now() - interval '90 days'
		  AND ((a.supplier_ref <> '' AND lower(trim(a.supplier_ref)) = lower(trim(b.supplier_ref)))
		       OR (a.total = b.total AND a.currency = b.currency AND abs(a.bill_date - b.bill_date) <= 7))
		ORDER BY b.created_at DESC LIMIT 50`, cid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []alertCand
	for rows.Next() {
		var a, b, sup, cur, da, db, ra, rb string
		var na, nb int64
		var ta, tb float64
		var sameRef bool
		if err := rows.Scan(&a, &b, &na, &nb, &sup, &ta, &tb, &cur, &da, &db, &ra, &rb, &sameRef); err != nil {
			return nil, err
		}
		sev := "medium"
		if sameRef {
			sev = "high"
		}
		out = append(out, alertCand{Kind: "dup_bill", Severity: sev, Fingerprint: "dup_bill:" + a + ":" + b, SubjectType: "bill", SubjectID: sp(b),
			SubjectLabel: fmt.Sprintf("%s · #%d / #%d", sup, na, nb), Period: db, Value: fp(tb), Expected: fp(ta),
			Detail: map[string]any{"bill_a": a, "bill_b": b, "number_a": na, "number_b": nb, "date_a": da, "date_b": db, "ref_a": ra, "ref_b": rb,
				"currency": cur, "same_ref": sameRef}})
	}
	return out, rows.Err()
}

func checkFleetFuel(ctx context.Context, tx pgx.Tx, cid string) ([]alertCand, error) {
	rows, err := tx.Query(ctx, `
		SELECT f.id::text, v.id::text, v.name || CASE WHEN v.plate <> '' THEN ' · ' || v.plate ELSE '' END, f.log_date::text, f.anomaly,
		  coalesce(f.consumption, 0)::float8, coalesce(v.fuel_norm, 0)::float8
		FROM fleet_fuel_logs f JOIN vehicles v ON v.id = f.vehicle_id
		WHERE f.company_id = $1 AND f.status = 'posted' AND f.anomaly <> '' AND f.log_date > current_date - 30`, cid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []alertCand
	for rows.Next() {
		var id, veh, label, day, anomaly string
		var cons, norm float64
		if err := rows.Scan(&id, &veh, &label, &day, &anomaly, &cons, &norm); err != nil {
			return nil, err
		}
		out = append(out, alertCand{Kind: "fleet_fuel", Severity: "medium", Fingerprint: "fleet_fuel:" + id, SubjectType: "vehicle", SubjectID: sp(veh),
			SubjectLabel: label, Period: day, Value: fp(cons), Expected: fp(norm), Detail: map[string]any{"anomaly": anomaly, "fuel_log_id": id, "unit": "l/100km"}})
	}
	return out, rows.Err()
}

// scanCompany runs every switched-on check and stores the new alerts. Returns how many alerts are new.
func scanCompany(ctx context.Context, tx pgx.Tx, cid string) (int, error) {
	st, err := loadSmartSettings(ctx, tx, cid)
	if err != nil {
		return 0, err
	}
	p := st.Params
	type check struct {
		kinds []string
		fn    func() ([]alertCand, error)
	}
	checks := []check{
		{[]string{"diesel"}, func() ([]alertCand, error) { return checkDiesel(ctx, tx, cid, p) }},
		{[]string{"discount"}, func() ([]alertCand, error) { return checkDiscounts(ctx, tx, cid, p) }},
		{[]string{"cash_diff", "cash_repeat"}, func() ([]alertCand, error) { return checkCash(ctx, tx, cid, p) }},
		{[]string{"voids"}, func() ([]alertCand, error) { return checkVoids(ctx, tx, cid, p) }},
		{[]string{"off_hours"}, func() ([]alertCand, error) { return checkOffHours(ctx, tx, cid, p) }},
		{[]string{"count_loss"}, func() ([]alertCand, error) { return checkCountLoss(ctx, tx, cid, p) }},
		{[]string{"collector_drop"}, func() ([]alertCand, error) { return checkCollectors(ctx, tx, cid, p) }},
		{[]string{"dup_bill"}, func() ([]alertCand, error) { return checkDupBills(ctx, tx, cid) }},
		{[]string{"fleet_fuel"}, func() ([]alertCand, error) { return checkFleetFuel(ctx, tx, cid) }},
	}
	created := 0
	for _, c := range checks {
		wanted := false
		for _, k := range c.kinds {
			wanted = wanted || st.on(k)
		}
		if !wanted {
			continue
		}
		cands, err := c.fn()
		if err != nil {
			return created, fmt.Errorf("smart check %v: %w", c.kinds, err)
		}
		for _, a := range cands {
			if !st.on(a.Kind) {
				continue
			}
			if a.Detail == nil {
				a.Detail = map[string]any{}
			}
			dj, _ := json.Marshal(a.Detail)
			label := a.SubjectLabel
			if r := []rune(label); len(r) > 300 {
				label = string(r[:300])
			}
			var inserted bool
			err := tx.QueryRow(ctx, `INSERT INTO smart_alerts (company_id, kind, severity, fingerprint, subject_type, subject_id, subject_label, period,
				value, expected, score, detail)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::jsonb)
				ON CONFLICT (company_id, fingerprint) DO UPDATE SET severity = EXCLUDED.severity, value = EXCLUDED.value, expected = EXCLUDED.expected,
				  score = EXCLUDED.score, detail = EXCLUDED.detail, subject_label = EXCLUDED.subject_label, updated_at = now()
				  WHERE smart_alerts.status IN ('open', 'ack')
				RETURNING (xmax = 0)`, cid, a.Kind, a.Severity, a.Fingerprint, a.SubjectType, a.SubjectID, label, a.Period,
				a.Value, a.Expected, a.Score, string(dj)).Scan(&inserted)
			if errors.Is(err, pgx.ErrNoRows) {
				continue // resolved / dismissed before: stays so
			}
			if err != nil {
				return created, err
			}
			if inserted {
				created++
			}
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO smart_settings (company_id, last_scan_at) VALUES ($1, now())
		ON CONFLICT (company_id) DO UPDATE SET last_scan_at = now()`, cid); err != nil {
		return created, err
	}
	return created, nil
}

// ======================= API =======================

const smartAlertCols = `a.id, a.kind, a.severity, a.subject_type, a.subject_id, a.subject_label, a.period, a.value::float8 AS value,
	a.expected::float8 AS expected, a.score::float8 AS score, a.detail, a.status, a.note, a.acted_at, u.full_name AS acted_by_name, a.created_at, a.updated_at`

// GET /smart/alerts?status=open|ack|resolved|dismissed|all&kind=&severity=
func (s *Server) smartAlerts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	status, kind, sev := q.Get("status"), q.Get("kind"), q.Get("severity")
	if status == "" {
		status = "active"
	}
	if !contains([]string{"active", "open", "ack", "resolved", "dismissed", "all"}, status) || (kind != "" && !contains(smartKinds, kind)) ||
		(sev != "" && !contains([]string{"low", "medium", "high"}, sev)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		rows, err := queryJSON(ctx, tx, `SELECT `+smartAlertCols+` FROM smart_alerts a LEFT JOIN users u ON u.id = a.acted_by
			WHERE a.company_id = $1 AND ($2 = 'all' OR ($2 = 'active' AND a.status IN ('open', 'ack')) OR a.status = $2)
			  AND ($3 = '' OR a.kind = $3) AND ($4 = '' OR a.severity = $4)
			ORDER BY (a.status = 'open') DESC, CASE a.severity WHEN 'high' THEN 0 WHEN 'medium' THEN 1 ELSE 2 END, a.created_at DESC LIMIT 500`,
			cid, status, kind, sev)
		if err != nil {
			return nil, err
		}
		summary, err := queryJSON(ctx, tx, `SELECT kind, count(*) FILTER (WHERE status = 'open') AS open, count(*) FILTER (WHERE status = 'ack') AS ack,
			count(*) FILTER (WHERE status IN ('open', 'ack') AND severity = 'high') AS high
			FROM smart_alerts WHERE company_id = $1 GROUP BY kind ORDER BY kind`, cid)
		if err != nil {
			return nil, err
		}
		var last *time.Time
		_ = tx.QueryRow(ctx, `SELECT last_scan_at FROM smart_settings WHERE company_id = $1`, cid).Scan(&last)
		return map[string]any{"alerts": rows, "summary": summary, "last_scan_at": last}, nil
	})
}

// POST /smart/alerts/scan: run the checks now.
func (s *Server) smartScan(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := companyBase(ctx, tx, cid); err != nil {
			return nil, err
		}
		n, err := scanCompany(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		var open int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM smart_alerts WHERE company_id = $1 AND status = 'open'`, cid).Scan(&open); err != nil {
			return nil, err
		}
		return map[string]int{"new": n, "open": open}, nil
	})
}

// POST /smart/alerts/{id}/{action} {note}: ack | resolve | dismiss | reopen
func (s *Server) smartAlertAction(w http.ResponseWriter, r *http.Request) {
	cid, id, action := r.PathValue("cid"), r.PathValue("id"), r.PathValue("action")
	to, ok := map[string]string{"ack": "ack", "resolve": "resolved", "dismiss": "dismissed", "reopen": "open"}[action]
	if !ok || !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	var in struct {
		Note string `json:"note"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if len(in.Note) > 1000 {
		writeErr(w, http.StatusBadRequest, "text_too_long")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var cur string
		if err := tx.QueryRow(ctx, `SELECT status FROM smart_alerts WHERE id = $1 AND company_id = $2 FOR UPDATE`, id, cid).Scan(&cur); err != nil {
			return nil, err
		}
		if cur == to || (to == "ack" && cur != "open") {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if _, err := tx.Exec(ctx, `UPDATE smart_alerts SET status = $3, note = CASE WHEN trim($4) = '' THEN note ELSE trim($4) END,
			acted_by = app_user(), acted_at = now(), updated_at = now() WHERE id = $1 AND company_id = $2`, id, cid, to, in.Note); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+smartAlertCols+` FROM smart_alerts a LEFT JOIN users u ON u.id = a.acted_by WHERE a.id = $1`, id)
	})
}

// ======================= digest and background worker =======================

var smartKindLabels = map[string][2]string{
	"diesel": {"Diesel per hour jumped", "ارتفاع استهلاك المازوت بالساعة"}, "discount": {"Unusual discounts", "حسومات غير عادية"},
	"cash_diff": {"Till shortage / overage", "نقص / زيادة في الصندوق"}, "cash_repeat": {"Repeated till shortages", "نقص متكرر في الصندوق"},
	"voids": {"Many voids / refunds", "إلغاءات / مرتجعات كثيرة"}, "off_hours": {"Payments outside working hours", "قبض خارج ساعات العمل"},
	"count_loss": {"Stock count loss", "نقص في جرد المخزون"}, "collector_drop": {"Collector brought in less", "تراجع جباية جابي"},
	"dup_bill": {"Possible duplicate supplier bill", "فاتورة مورد مكررة محتملة"}, "fleet_fuel": {"Vehicle fuel flag", "تنبيه وقود سيارة"},
}

// sendDigest tells the owner about the alerts raised since the last digest (in-app + e-mail, and WhatsApp to the
// digest phone when one is set). Returns true when something was sent.
func sendDigest(ctx context.Context, tx pgx.Tx, cid string, st smartSettingsRow) (bool, error) {
	since := time.Now().AddDate(0, 0, -1)
	if st.LastDigestOn != nil {
		if d, err := time.Parse(time.DateOnly, *st.LastDigestOn); err == nil {
			since = d
		}
	}
	rows, err := tx.Query(ctx, `SELECT kind, severity, subject_label FROM smart_alerts WHERE company_id = $1 AND status = 'open' AND created_at >= $2
		ORDER BY CASE severity WHEN 'high' THEN 0 WHEN 'medium' THEN 1 ELSE 2 END, created_at DESC LIMIT 200`, cid, since)
	if err != nil {
		return false, err
	}
	type line struct{ kind, sev, label string }
	var ls []line
	for rows.Next() {
		var l line
		if err := rows.Scan(&l.kind, &l.sev, &l.label); err != nil {
			rows.Close()
			return false, err
		}
		ls = append(ls, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	if len(ls) == 0 {
		return false, nil
	}
	high := 0
	var b strings.Builder
	for i, l := range ls {
		if l.sev == "high" {
			high++
		}
		if i < 12 {
			lb := smartKindLabels[l.kind]
			fmt.Fprintf(&b, "• %s / %s: %s\n", lb[1], lb[0], l.label)
		}
	}
	if len(ls) > 12 {
		fmt.Fprintf(&b, "… +%d\n", len(ls)-12)
	}
	title := fmt.Sprintf("Paxve: %d تنبيهات جديدة / new alerts (%d high)", len(ls), high)
	if err := notifyStaff(ctx, tx, cid, "smart.manage", "smart", title, b.String(), "", ""); err != nil {
		return false, err
	}
	if phone := strings.TrimSpace(st.DigestPhone); phone != "" {
		if _, _, err := queueOut(ctx, tx, cid, outMessage{Channel: "whatsapp", Phone: phone, Body: title + "\n" + b.String(), Event: "custom"}); err != nil {
			return false, err
		}
	}
	return true, nil
}

// RunSmartJobs: for every company with the app on (organisations whose jobs may run): the checks every 6 hours,
// the risk scores once a day, and the digest once a day after the digest hour (company time).
func (s *Server) RunSmartJobs(ctx context.Context) (int, error) {
	if s.OwnerPool == nil {
		return 0, nil
	}
	rows, err := s.OwnerPool.Query(ctx, `SELECT DISTINCT c.tenant_id::text FROM companies c WHERE c.is_active AND 'ai' = ANY (c.apps) AND tenant_jobs_ok(c.tenant_id)`)
	if err != nil {
		return 0, err
	}
	tenants, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	total := 0
	for _, t := range tenants {
		err := s.inJobTx(ctx, t, func(tx pgx.Tx) error {
			crow, err := tx.Query(ctx, `SELECT c.id::text, c.timezone FROM companies c WHERE c.is_active AND 'ai' = ANY (c.apps) ORDER BY c.created_at`)
			if err != nil {
				return err
			}
			type comp struct{ id, tz string }
			var comps []comp
			for crow.Next() {
				var c comp
				if err := crow.Scan(&c.id, &c.tz); err != nil {
					crow.Close()
					return err
				}
				comps = append(comps, c)
			}
			crow.Close()
			for _, c := range comps {
				st, err := loadSmartSettings(ctx, tx, c.id)
				if err != nil {
					return err
				}
				if st.LastScanAt == nil || time.Since(*st.LastScanAt) > 6*time.Hour {
					n, err := scanCompany(ctx, tx, c.id)
					if err != nil {
						return err
					}
					total += n
				}
				if st.LastRiskAt == nil || time.Since(*st.LastRiskAt) > 20*time.Hour {
					if _, err := computeRisk(ctx, tx, c.id); err != nil {
						return err
					}
				}
				loc, err := time.LoadLocation(c.tz)
				if err != nil {
					loc = time.UTC
				}
				now := time.Now().In(loc)
				day := now.Format(time.DateOnly)
				if st.DigestEnabled && now.Hour() >= st.DigestHour && (st.LastDigestOn == nil || *st.LastDigestOn < day) {
					if _, err := sendDigest(ctx, tx, c.id, st); err != nil {
						return err
					}
					if _, err := tx.Exec(ctx, `INSERT INTO smart_settings (company_id, last_digest_on) VALUES ($1, $2::date)
						ON CONFLICT (company_id) DO UPDATE SET last_digest_on = $2::date`, c.id, day); err != nil {
						return err
					}
				}
			}
			return nil
		})
		if err != nil {
			slog.Error("smart jobs", "tenant", t, "err", err)
		}
	}
	return total, nil
}

// StartSmartWorker: every 10 minutes (the work itself runs only when due).
func (s *Server) StartSmartWorker(ctx context.Context) {
	if s.OwnerPool == nil {
		return
	}
	go func() {
		tick := time.NewTicker(10 * time.Minute)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if _, err := s.RunSmartJobs(ctx); err != nil {
					slog.Error("smart worker", "err", err)
				}
			}
		}
	}()
}

// sortedKeys is a small helper for stable output.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
