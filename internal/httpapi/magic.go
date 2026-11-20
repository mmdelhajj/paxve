package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/mmdelhajj/paxve/internal/db"
	"github.com/mmdelhajj/paxve/internal/magic"
)

// Migration from the legacy Magic system (see docs/MAGIC_MIGRATION.md).
//
//	POST /magic/imports                    {file_name, size, sha256}        start an upload
//	POST /magic/imports/{id}/chunk         {offset, data (base64)}          append a piece (≤ 4 MB); the last one triggers the analysis
//	GET  /magic/imports, /magic/imports/{id}                                 list / one import (never the file itself)
//	POST /magic/imports/{id}/analyze       {options}                        dry run again with other options
//	POST /magic/imports/{id}/commit        {mapping, options}               write everything in ONE transaction (background job)
//
// The zip is produced by tools/magic-export/Export-Magic.ps1 on the owner's Magic machine. Nothing here ever
// connects to the old server.

const (
	magicMaxSize  = 200 << 20
	magicMaxChunk = 4 << 20
)

const magicCols = `id, file_name, size, sha256, received, status, analysis, mapping, options, result, progress, error,
	created_at, updated_at, finished_at, (SELECT u.full_name FROM users u WHERE u.id = magic_imports.created_by) AS created_by`

type magicOptionsIn struct {
	OpeningDate    string `json:"opening_date"`
	Rate           string `json:"rate"`
	ReadingsMonths *int   `json:"readings_months"`
	IncludeEnded   bool   `json:"include_ended"`
}

func (in magicOptionsIn) parse() (magic.Options, error) {
	o := magic.Options{OpeningDate: strings.TrimSpace(in.OpeningDate), ReadingsMonths: 12, IncludeEnded: in.IncludeEnded}
	if o.OpeningDate == "" {
		o.OpeningDate = today()
	}
	if !validDate(o.OpeningDate) {
		return o, badRequest("invalid_date")
	}
	if s := strings.TrimSpace(in.Rate); s != "" {
		d, ok := money(s)
		if !ok || !d.IsPositive() {
			return o, badRequest("invalid_rate")
		}
		o.Rate = d
	}
	if in.ReadingsMonths != nil {
		if *in.ReadingsMonths < 0 || *in.ReadingsMonths > 120 {
			return o, badRequest("bad_request")
		}
		o.ReadingsMonths = *in.ReadingsMonths
	}
	return o, nil
}

func (s *Server) createMagicImport(w http.ResponseWriter, r *http.Request) {
	var in struct {
		FileName string `json:"file_name"`
		Size     int64  `json:"size"`
		SHA256   string `json:"sha256"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	in.SHA256 = strings.ToLower(strings.TrimSpace(in.SHA256))
	if in.Size <= 0 || in.Size > magicMaxSize {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file_too_large", "detail": strconv.Itoa(magicMaxSize >> 20)})
		return
	}
	if len(in.SHA256) != 64 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	name := strings.TrimSpace(in.FileName)
	if len(name) > 200 {
		name = name[:200]
	}
	s.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO magic_imports (file_name, size, sha256) VALUES ($1, $2, $3) RETURNING id::text`,
			name, in.Size, in.SHA256).Scan(&id); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+magicCols+` FROM magic_imports WHERE id = $1`, id)
	})
}

func (s *Server) magicChunk(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Offset int64  `json:"offset"`
		Data   string `json:"data"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, magicMaxChunk*4/3+4096))
	if err := dec.Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	chunk, err := base64.StdEncoding.DecodeString(in.Data)
	if err != nil || len(chunk) == 0 || len(chunk) > magicMaxChunk {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	id := r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var status, sum string
		var size, received int64
		if err := tx.QueryRow(ctx, `SELECT status, size, received, sha256 FROM magic_imports WHERE id = $1 FOR UPDATE`, id).
			Scan(&status, &size, &received, &sum); err != nil {
			return nil, err
		}
		if status != "uploading" {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if in.Offset != received || received+int64(len(chunk)) > size {
			return nil, &apiError{status: http.StatusConflict, code: "bad_offset", detail: strconv.FormatInt(received, 10)}
		}
		if _, err := tx.Exec(ctx, `UPDATE magic_imports SET data = data || $2, received = received + $3 WHERE id = $1`,
			id, chunk, len(chunk)); err != nil {
			return nil, err
		}
		if received+int64(len(chunk)) == size {
			var data []byte
			if err := tx.QueryRow(ctx, `SELECT data FROM magic_imports WHERE id = $1`, id).Scan(&data); err != nil {
				return nil, err
			}
			h := sha256.Sum256(data)
			if hex.EncodeToString(h[:]) != sum {
				if _, err := tx.Exec(ctx, `UPDATE magic_imports SET status = 'invalid', error = 'checksum_mismatch', data = ''::bytea WHERE id = $1`, id); err != nil {
					return nil, err
				}
			} else if err := analyzeMagic(ctx, tx, id, data, magicOptionsIn{}); err != nil {
				return nil, err
			}
		}
		return queryJSONOne(ctx, tx, `SELECT `+magicCols+` FROM magic_imports WHERE id = $1`, id)
	})
}

// analyzeMagic parses the zip and stores the dry run (per Magic company: what would be created, totals, warnings).
func analyzeMagic(ctx context.Context, tx pgx.Tx, id string, data []byte, optIn magicOptionsIn) error {
	d, perr := magic.Parse(data)
	if perr != nil {
		_, err := tx.Exec(ctx, `UPDATE magic_imports SET status = 'invalid', error = $2 WHERE id = $1`, id, perr.Error())
		return err
	}
	opt, err := optIn.parse()
	if err != nil {
		return err
	}
	ids := make([]int, 0, len(d.Companies))
	for cid := range d.Companies {
		ids = append(ids, cid)
	}
	sort.Ints(ids)
	type companyOut struct {
		magic.Summary
		AlreadyImported []map[string]string `json:"already_imported"`
	}
	out := []companyOut{}
	for _, mc := range ids {
		p, err := magic.Build(d, mc, opt)
		if err != nil {
			return err
		}
		sum := p.Summary()
		// a Paxve company that already holds this Magic company's opening balances
		done := []map[string]string{}
		rows, err := tx.Query(ctx, `SELECT c.id::text, c.name_en FROM journal_entries e JOIN companies c ON c.id = e.company_id
			WHERE e.external_ref = $1`, magic.CompanyRef(mc)+":opening")
		if err != nil {
			return err
		}
		for rows.Next() {
			var a, b string
			if err := rows.Scan(&a, &b); err != nil {
				rows.Close()
				return err
			}
			done = append(done, map[string]string{"company_id": a, "name": b})
		}
		rows.Close()
		out = append(out, companyOut{Summary: sum, AlreadyImported: done})
	}
	opts, _ := json.Marshal(map[string]any{"opening_date": opt.OpeningDate, "rate": rateOrDefault(opt.Rate, d),
		"readings_months": opt.ReadingsMonths, "include_ended": opt.IncludeEnded})
	analysis, _ := json.Marshal(map[string]any{"manifest": d.Manifest, "default_rate": d.DefaultRate().String(), "companies": out})
	_, err = tx.Exec(ctx, `UPDATE magic_imports SET status = CASE WHEN status IN ('uploading', 'invalid') THEN 'uploaded' ELSE status END,
		error = NULL, analysis = $2, options = $3 WHERE id = $1`, id, analysis, opts)
	return err
}

func rateOrDefault(r decimal.Decimal, d *magic.Data) string {
	if r.IsPositive() {
		return r.String()
	}
	return d.DefaultRate().String()
}

func (s *Server) listMagicImports(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT id, file_name, size, status, error, created_at, finished_at,
			(SELECT u.full_name FROM users u WHERE u.id = magic_imports.created_by) AS created_by
			FROM magic_imports ORDER BY created_at DESC LIMIT 30`)
	})
}

func (s *Server) getMagicImport(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		id := r.PathValue("id")
		if !isUUID(id) {
			return nil, notFound()
		}
		return queryJSONOne(ctx, tx, `SELECT `+magicCols+` FROM magic_imports WHERE id = $1`, id)
	})
}

func (s *Server) analyzeMagicImport(w http.ResponseWriter, r *http.Request) {
	var in magicOptionsIn
	if !decodeBody(w, r, &in) {
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		id := r.PathValue("id")
		if !isUUID(id) {
			return nil, notFound()
		}
		var status string
		var data []byte
		if err := tx.QueryRow(ctx, `SELECT status, data FROM magic_imports WHERE id = $1 FOR UPDATE`, id).Scan(&status, &data); err != nil {
			return nil, err
		}
		if status == "uploading" || status == "running" || len(data) == 0 {
			return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		if err := analyzeMagic(ctx, tx, id, data, in); err != nil {
			return nil, err
		}
		return queryJSONOne(ctx, tx, `SELECT `+magicCols+` FROM magic_imports WHERE id = $1`, id)
	})
}

type magicMapping struct {
	MagicCompany int    `json:"magic_company"`
	Target       string `json:"target"` // company uuid | "new" | "skip"
	Name         string `json:"name"`   // name of a new company
}

func (s *Server) commitMagicImport(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Mapping []magicMapping `json:"mapping"`
		Options magicOptionsIn `json:"options"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	opt, err := in.Options.parse()
	if err != nil {
		fail(w, r, err)
		return
	}
	id := r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	// check the request and the file before creating anything
	var status string
	var data []byte
	if err := db.InTenant(r.Context(), s.Pool, scope(r), func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `SELECT status, data FROM magic_imports WHERE id = $1`, id).Scan(&status, &data)
	}); err != nil {
		fail(w, r, err)
		return
	}
	if status != "uploaded" && status != "done" && status != "failed" {
		writeErr(w, http.StatusConflict, "wrong_status")
		return
	}
	d, perr := magic.Parse(data)
	if perr != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "invalid_file", "detail": perr.Error()})
		return
	}
	seen := map[int]bool{}
	targets := map[string]bool{}
	var work []magicMapping
	for _, m := range in.Mapping {
		if _, ok := d.Companies[m.MagicCompany]; !ok || seen[m.MagicCompany] {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
		seen[m.MagicCompany] = true
		switch {
		case m.Target == "skip":
			continue
		case m.Target == "new":
		case isUUID(m.Target):
			if !companyAllowed(principal(r), m.Target) {
				writeErr(w, http.StatusNotFound, "not_found")
				return
			}
			if targets[m.Target] {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "duplicate_target"})
				return
			}
			targets[m.Target] = true
		default:
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
		work = append(work, m)
	}
	if len(work) == 0 {
		writeErr(w, http.StatusBadRequest, "nothing_to_import")
		return
	}
	// new companies are created first (with the Lebanese chart of accounts and its cash boxes)
	lang := principal(r).Locale
	if lang != "ar" && lang != "fr" {
		lang = "en"
	}
	for i, m := range work {
		if m.Target != "new" {
			continue
		}
		name := strings.TrimSpace(m.Name)
		if name == "" {
			name = strings.TrimSpace(d.Companies[m.MagicCompany].Name)
		}
		if name == "" {
			name = fmt.Sprintf("Magic %d", m.MagicCompany)
		}
		raw, err := callInternal(r, s.createCompany, nil, map[string]any{"no_chart": true, "name_en": name, "name_ar": name,
			"base_currency": "USD", "local_currency": "LBP"})
		for n := 1; err != nil && strings.Contains(err.Error(), "name_taken") && n <= 5; n++ {
			alt := fmt.Sprintf("%s (Magic %d)", name, m.MagicCompany)
			if n > 1 {
				alt = fmt.Sprintf("%s (Magic %d-%d)", name, m.MagicCompany, n)
			}
			raw, err = callInternal(r, s.createCompany, nil, map[string]any{"no_chart": true, "name_en": alt, "name_ar": alt,
				"base_currency": "USD", "local_currency": "LBP"})
		}
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "company_create_failed", "detail": err.Error()})
			return
		}
		var co struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(raw, &co)
		if _, err := callInternal(r, s.loadChartTemplate, map[string]string{"cid": co.ID},
			map[string]string{"template": "lebanon", "language": lang}); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "company_create_failed", "detail": err.Error()})
			return
		}
		work[i].Target = co.ID
	}
	mapping, _ := json.Marshal(work)
	opts, _ := json.Marshal(map[string]any{"opening_date": opt.OpeningDate, "rate": rateOrDefault(opt.Rate, d),
		"readings_months": opt.ReadingsMonths, "include_ended": opt.IncludeEnded})
	err = db.InTenant(r.Context(), s.Pool, scope(r), func(tx pgx.Tx) error {
		tag, err := tx.Exec(r.Context(), `UPDATE magic_imports SET status = 'running', mapping = $2, options = $3, result = NULL, error = NULL,
			progress = '{}'::jsonb, finished_at = NULL WHERE id = $1 AND status IN ('uploaded', 'done', 'failed')`, id, mapping, opts)
		if err == nil && tag.RowsAffected() == 0 {
			return &apiError{status: http.StatusConflict, code: "wrong_status"}
		}
		return err
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	sc := scope(r)
	go s.processMagicImport(context.WithoutCancel(r.Context()), db.Scope{TenantID: sc.TenantID, UserID: sc.UserID, IP: sc.IP, CompanyIDs: sc.CompanyIDs}, id, d, work, opt)
	s.run(w, r, http.StatusAccepted, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSONOne(ctx, tx, `SELECT `+magicCols+` FROM magic_imports WHERE id = $1`, id)
	})
}

var magicJobs sync.Map

// processMagicImport writes every mapped company in ONE transaction: if anything fails nothing is kept.
func (s *Server) processMagicImport(ctx context.Context, sc db.Scope, id string, d *magic.Data, work []magicMapping, opt magic.Options) {
	if _, dup := magicJobs.LoadOrStore(id, struct{}{}); dup {
		return
	}
	defer magicJobs.Delete(id)
	start := time.Now()
	progress := func(step string, company int, done, total int) {
		p, _ := json.Marshal(map[string]any{"step": step, "magic_company": company, "done": done, "total": total,
			"seconds": int(time.Since(start).Seconds())})
		_ = db.InTenant(ctx, s.Pool, sc, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE magic_imports SET progress = $2 WHERE id = $1`, id, p)
			return err
		})
	}
	results := []any{}
	err := db.InTenant(ctx, s.Pool, sc, func(tx pgx.Tx) error {
		for _, m := range work {
			p, err := magic.Build(d, m.MagicCompany, opt)
			if err != nil {
				return err
			}
			w := &magicWriter{ctx: quietCtx(ctx), tx: tx, cid: m.Target, plan: p, opt: opt,
				progress: func(step string, done, total int) { progress(step, m.MagicCompany, done, total) }}
			res, err := w.write()
			if err != nil {
				return fmt.Errorf("magic company %d: %w", m.MagicCompany, err)
			}
			results = append(results, res)
		}
		return nil
	})
	if err != nil {
		code := err.Error()
		var pg *pgconn.PgError
		var ae *apiError
		if errors.As(err, &ae) {
			code = fmt.Sprintf("%s%s", strings.SplitN(err.Error(), ae.code, 2)[0], ae.code)
			if ae.detail != "" {
				code += ": " + ae.detail
			}
		} else if errors.As(err, &pg) {
			code = strings.SplitN(err.Error(), ":", 2)[0] + ": " + pg.Code + " " + pg.Message
		}
		if len(code) > 500 {
			code = code[:500]
		}
		slog.Error("magic import", "import", id, "err", err)
		_ = db.InTenant(ctx, s.Pool, sc, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE magic_imports SET status = 'failed', error = $2, finished_at = now() WHERE id = $1`, id, code)
			return err
		})
		return
	}
	res, _ := json.Marshal(map[string]any{"companies": results, "seconds": int(time.Since(start).Seconds())})
	_ = db.InTenant(ctx, s.Pool, sc, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE magic_imports SET status = 'done', result = $2, finished_at = now(),
			progress = jsonb_build_object('step', 'done') WHERE id = $1`, id, res)
		return err
	})
}

// ---- writing one plan ----

type magicWriter struct {
	ctx      context.Context
	tx       pgx.Tx
	cid      string
	plan     *magic.Plan
	opt      magic.Options
	set      *billingSettings
	ps       *purchSettings
	progress func(step string, done, total int)

	ids      map[string]string // external ref → uuid
	accounts map[string]string // code → id (postable)
	groups   map[string]string // code → id (group accounts)
	clearing string
	created  map[string]int
	updated  map[string]int
	skipped  map[string]int
	notes    []string
	newAccts []string
}

func (w *magicWriter) exec(sql string, args ...any) error {
	_, err := w.tx.Exec(w.ctx, sql, args...)
	return err
}

func (w *magicWriter) loadRefs(table string) error {
	rows, err := w.tx.Query(w.ctx, `SELECT external_ref, id::text FROM `+table+` WHERE company_id = $1 AND external_ref LIKE 'magic:%'`, w.cid)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return err
		}
		w.ids[k] = v
	}
	return rows.Err()
}

// names already used in a table of the company (a new row gets " (Magic)" added when its name is taken)
func (w *magicWriter) taken(table, col string) (map[string]bool, error) {
	rows, err := w.tx.Query(w.ctx, `SELECT lower(`+col+`) FROM `+table+` WHERE company_id = $1`, w.cid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}

func freeName(taken map[string]bool, name string) string {
	n := name
	for i := 1; taken[strings.ToLower(n)]; i++ {
		if i == 1 {
			n = name + " (Magic)"
		} else {
			n = fmt.Sprintf("%s (Magic %d)", name, i)
		}
	}
	taken[strings.ToLower(n)] = true
	return n
}

func (w *magicWriter) batch(b *pgx.Batch) error {
	if b.Len() == 0 {
		return nil
	}
	return w.tx.SendBatch(w.ctx, b).Close()
}

func (w *magicWriter) write() (map[string]any, error) {
	ctx, tx, p := w.ctx, w.tx, w.plan
	w.ids, w.accounts = map[string]string{}, map[string]string{}
	w.created, w.updated, w.skipped = map[string]int{}, map[string]int{}, map[string]int{}
	var err error
	if w.set, err = loadBillingSettings(ctx, tx, w.cid); err != nil {
		return nil, err
	}
	if err := w.set.requireAR(); err != nil {
		return nil, err
	}
	if w.set.FXGain == nil || w.set.FXLoss == nil {
		return nil, &apiError{status: http.StatusUnprocessableEntity, code: "billing_not_setup", detail: "fx"}
	}
	if w.ps, err = loadPurchSettings(ctx, tx, w.cid); err != nil {
		return nil, err
	}
	var companyName string
	if err := tx.QueryRow(ctx, `SELECT name_en FROM companies WHERE id = $1`, w.cid).Scan(&companyName); err != nil {
		return nil, err
	}
	if err := w.exec(`SELECT pg_advisory_xact_lock(hashtext('magic:' || $1))`, w.cid); err != nil {
		return nil, err
	}
	for _, t := range []string{"stations", "boxes", "areas", "plans", "customers", "subscriptions", "suppliers"} {
		if err := w.loadRefs(t); err != nil {
			return nil, err
		}
	}
	urows, err := tx.Query(ctx, `SELECT external_ref, id::text FROM users WHERE external_ref LIKE 'magic:%'`)
	if err != nil {
		return nil, err
	}
	for urows.Next() {
		var k, v string
		if err := urows.Scan(&k, &v); err != nil {
			urows.Close()
			return nil, err
		}
		w.ids[k] = v
	}
	urows.Close()
	// the money part (unpaid bills, balances, opening entry) is written once per Magic company and Paxve company
	var financialDone bool
	co := strconv.Itoa(p.Company.ID)
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM journal_entries WHERE company_id = $1 AND external_ref = $2)
		OR EXISTS (SELECT 1 FROM invoices WHERE company_id = $1 AND (external_ref LIKE $3 OR external_ref LIKE $4))
		OR EXISTS (SELECT 1 FROM payments WHERE company_id = $1 AND external_ref LIKE $4)
		OR EXISTS (SELECT 1 FROM bills WHERE company_id = $1 AND external_ref LIKE $5)`,
		w.cid, magic.CompanyRef(p.Company.ID)+":opening", "magic:bill:"+co+":%", "magic:opening:"+co+":%",
		"magic:supplier-opening:"+co+":%").Scan(&financialDone); err != nil {
		return nil, err
	}
	if err := w.checkPlanLimit(); err != nil {
		return nil, err
	}

	steps := []struct {
		name string
		fn   func() error
	}{
		{"collectors", w.collectors}, {"stations", w.stations}, {"boxes", w.boxes}, {"areas", w.areas}, {"plans", w.plans},
		{"customers", w.customers}, {"subscriptions", w.subscriptions}, {"readings", w.readings}, {"suppliers", w.suppliers},
	}
	if !financialDone {
		steps = append(steps, struct {
			name string
			fn   func() error
		}{"balances", w.balances})
	} else {
		w.notes = append(w.notes, "opening_balances_already_imported")
	}
	for i, s := range steps {
		w.progress(s.name, i, len(steps))
		if err := s.fn(); err != nil {
			return nil, err
		}
	}
	return map[string]any{"magic_company": p.Company.ID, "magic_name": p.Company.Name, "company_id": w.cid, "company_name": companyName,
		"created": w.created, "updated": w.updated, "skipped": w.skipped, "notes": w.notes, "accounts_created": w.newAccts,
		"warnings": p.Warnings(), "financial_skipped": financialDone}, nil
}

func (w *magicWriter) checkPlanLimit() error {
	active := 0
	for _, s := range w.plan.Subs {
		if s.Status == "active" && w.ids[s.Ref] == "" {
			active++
		}
	}
	if active == 0 {
		return nil
	}
	var plan string
	var max *int
	var trialEnds *time.Time
	if err := w.tx.QueryRow(w.ctx, `SELECT plan, max_subscribers, trial_ends_at FROM tenants WHERE id = app_tenant()`).Scan(&plan, &max, &trialEnds); err != nil {
		return err
	}
	if plan == "trial" && trialEnds != nil && trialEnds.Before(time.Now()) {
		return &apiError{status: http.StatusPaymentRequired, code: "trial_expired"}
	}
	if max == nil {
		return nil
	}
	var cur int
	if err := w.tx.QueryRow(w.ctx, `SELECT count(*) FROM subscriptions WHERE status = 'active'`).Scan(&cur); err != nil {
		return err
	}
	if cur+active > *max {
		return &apiError{status: http.StatusPaymentRequired, code: "plan_limit", detail: strconv.Itoa(*max)}
	}
	return nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (w *magicWriter) collectors() error {
	for _, c := range w.plan.Collectors {
		if _, ok := w.ids[c.Ref]; ok {
			w.skipped["collectors"]++
			continue
		}
		// inactive collector users without a password: an admin sets the e-mail and password before they log in
		email := fmt.Sprintf("%s-%s@import.invalid", strings.ReplaceAll(strings.TrimPrefix(c.Ref, "magic:"), ":", "-"), randomHex(4))
		var id string
		if err := w.tx.QueryRow(w.ctx, `INSERT INTO users (email, full_name, role, locale, password_hash, is_active, external_ref)
			VALUES ($1, $2, 'collector', 'ar', '!', false, $3) RETURNING id::text`, email, c.Name, c.Ref).Scan(&id); err != nil {
			return err
		}
		w.ids[c.Ref] = id
		w.created["collectors"]++
	}
	return nil
}

func (w *magicWriter) stations() error {
	taken, err := w.taken("stations", "name")
	if err != nil {
		return err
	}
	for _, s := range w.plan.Stations {
		if _, ok := w.ids[s.Ref]; ok {
			w.skipped["stations"]++
			continue
		}
		var id string
		if err := w.tx.QueryRow(w.ctx, `INSERT INTO stations (company_id, name, external_ref) VALUES ($1, $2, $3) RETURNING id::text`,
			w.cid, freeName(taken, s.Name), s.Ref).Scan(&id); err != nil {
			return err
		}
		w.ids[s.Ref] = id
		w.created["stations"]++
	}
	return nil
}

func (w *magicWriter) boxes() error {
	taken, err := w.taken("boxes", "code")
	if err != nil {
		return err
	}
	b := &pgx.Batch{}
	for _, x := range w.plan.Boxes {
		if _, ok := w.ids[x.Ref]; ok {
			w.skipped["boxes"]++
			continue
		}
		b.Queue(`INSERT INTO boxes (company_id, station_id, code, location, external_ref) VALUES ($1, $2, $3, $4, $5)`,
			w.cid, w.ids[x.StationRef], freeName(taken, x.Code), x.Location, x.Ref)
		w.created["boxes"]++
	}
	if err := w.batch(b); err != nil {
		return err
	}
	return w.loadRefs("boxes")
}

func (w *magicWriter) areas() error {
	taken, err := w.taken("areas", "name")
	if err != nil {
		return err
	}
	b := &pgx.Batch{}
	for _, a := range w.plan.Areas {
		if id, ok := w.ids[a.Ref]; ok {
			// keep the area, but give it its Magic collector if it has none yet
			if c := w.ids[a.CollectorRef]; c != "" {
				b.Queue(`UPDATE areas SET collector_id = $2 WHERE id = $1 AND collector_id IS NULL`, id, c)
			}
			w.skipped["areas"]++
			continue
		}
		var coll *string
		if c := w.ids[a.CollectorRef]; c != "" {
			coll = &c
		}
		b.Queue(`INSERT INTO areas (company_id, name, collector_id, external_ref) VALUES ($1, $2, $3, $4)`,
			w.cid, freeName(taken, a.Name), coll, a.Ref)
		w.created["areas"]++
	}
	if err := w.batch(b); err != nil {
		return err
	}
	return w.loadRefs("areas")
}

func (w *magicWriter) plans() error {
	taken, err := w.taken("plans", "name")
	if err != nil {
		return err
	}
	revenue := ""
	if w.set.Revenue != nil {
		revenue = *w.set.Revenue
	}
	if revenue == "" {
		return &apiError{status: http.StatusUnprocessableEntity, code: "billing_not_setup", detail: "revenue"}
	}
	for _, pl := range w.plan.Plans {
		if _, ok := w.ids[pl.Ref]; ok {
			w.skipped["plans"]++
			continue
		}
		typ, unit := "fixed", "kWh"
		if pl.Metered {
			typ = "metered"
		}
		var id string
		if err := w.tx.QueryRow(w.ctx, `INSERT INTO plans (company_id, name, billing_type, currency, base_fee, unit_price, unit_label,
			revenue_account_id, external_ref) VALUES ($1, $2, $3, $4, $5::numeric, $6::numeric, $7, $8, $9) RETURNING id::text`,
			w.cid, freeName(taken, pl.Name), typ, pl.Currency, pl.BaseFee.String(), pl.UnitPrice.String(), unit, revenue, pl.Ref).Scan(&id); err != nil {
			return err
		}
		w.ids[pl.Ref] = id
		w.created["plans"]++
	}
	return nil
}

func (w *magicWriter) customers() error {
	codes := map[string]string{} // lower(code) → external_ref (or "")
	rows, err := w.tx.Query(w.ctx, `SELECT lower(code), coalesce(external_ref, '') FROM customers WHERE company_id = $1`, w.cid)
	if err != nil {
		return err
	}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			rows.Close()
			return err
		}
		codes[k] = v
	}
	rows.Close()
	b := &pgx.Batch{}
	flush := func() error {
		err := w.batch(b)
		b = &pgx.Batch{}
		return err
	}
	for i, c := range w.plan.Customers {
		var area *string
		if a := w.ids[c.AreaRef]; a != "" {
			area = &a
		}
		if id, ok := w.ids[c.Ref]; ok {
			b.Queue(`UPDATE customers SET name = $2, phone = CASE WHEN $3 = '' THEN phone ELSE $3 END,
				phone2 = CASE WHEN $4 = '' THEN phone2 ELSE $4 END, email = CASE WHEN $5 = '' THEN email ELSE $5 END,
				address = CASE WHEN $6 = '' THEN address ELSE $6 END, area_id = coalesce(area_id, $7), status = $8 WHERE id = $1`,
				id, c.Name, c.Phone, c.Phone2, c.Email, c.Address, area, c.Status)
			w.updated["customers"]++
		} else {
			code := c.Code
			if ref, used := codes[strings.ToLower(code)]; used && ref != c.Ref {
				code = "M" + c.Code
				for n := 2; codes[strings.ToLower(code)] != ""; n++ {
					code = fmt.Sprintf("M%s-%d", c.Code, n)
				}
			}
			codes[strings.ToLower(code)] = c.Ref
			b.Queue(`INSERT INTO customers (company_id, code, name, phone, phone2, email, address, area_id, status, notes, external_ref)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
				w.cid, code, c.Name, c.Phone, c.Phone2, c.Email, c.Address, area, c.Status, c.Notes, c.Ref)
			w.created["customers"]++
		}
		if b.Len() >= 500 {
			if err := flush(); err != nil {
				return err
			}
			w.progress("customers", i, len(w.plan.Customers))
		}
	}
	if err := flush(); err != nil {
		return err
	}
	return w.loadRefs("customers")
}

func (w *magicWriter) subscriptions() error {
	b := &pgx.Batch{}
	flush := func() error {
		err := w.batch(b)
		b = &pgx.Batch{}
		return err
	}
	for _, s := range w.plan.Subs {
		cust, plan := w.ids[s.CustomerRef], w.ids[s.PlanRef]
		if cust == "" || plan == "" {
			w.skipped["subscriptions"]++
			continue
		}
		var box *string
		if x := w.ids[s.BoxRef]; x != "" {
			box = &x
		}
		var fee *string
		if s.FeeOverride != nil {
			f := s.FeeOverride.String()
			fee = &f
		}
		if id, ok := w.ids[s.Ref]; ok {
			b.Queue(`UPDATE subscriptions SET status = $2 WHERE id = $1 AND status <> $2`, id, s.Status)
			w.updated["subscriptions"]++
		} else {
			b.Queue(`INSERT INTO subscriptions (company_id, customer_id, plan_id, meter_no, start_date, status, fee_override, initial_reading,
				notes, box_id, external_ref) VALUES ($1, $2, $3, $4, $5::date, $6, $7::numeric, $8::numeric, $9, $10, $11)`,
				w.cid, cust, plan, s.MeterNo, s.Start, s.Status, fee, s.Initial.String(), s.Notes, box, s.Ref)
			w.created["subscriptions"]++
		}
		if b.Len() >= 500 {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := flush(); err != nil {
		return err
	}
	return w.loadRefs("subscriptions")
}

func (w *magicWriter) readings() error {
	b := &pgx.Batch{}
	n := 0
	for _, r := range w.plan.Readings {
		sub := w.ids[r.SubRef]
		if sub == "" {
			continue
		}
		b.Queue(`INSERT INTO meter_readings (subscription_id, period, reading) VALUES ($1, $2, $3::numeric)
			ON CONFLICT (tenant_id, subscription_id, period) DO NOTHING`, sub, r.Period, r.Value.String())
		n++
		if b.Len() >= 1000 {
			if err := w.batch(b); err != nil {
				return err
			}
			b = &pgx.Batch{}
		}
	}
	if err := w.batch(b); err != nil {
		return err
	}
	w.created["readings"] = n
	return nil
}

func (w *magicWriter) suppliers() error {
	codes := map[string]bool{}
	rows, err := w.tx.Query(w.ctx, `SELECT lower(code) FROM suppliers WHERE company_id = $1`, w.cid)
	if err != nil {
		return err
	}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return err
		}
		codes[k] = true
	}
	rows.Close()
	for _, s := range w.plan.Suppliers {
		if _, ok := w.ids[s.Ref]; ok {
			w.skipped["suppliers"]++
			continue
		}
		code := s.Code
		for n := 1; codes[strings.ToLower(code)]; n++ {
			code = fmt.Sprintf("M%s-%d", s.Code, n)
		}
		codes[strings.ToLower(code)] = true
		var id string
		if err := w.tx.QueryRow(w.ctx, `INSERT INTO suppliers (company_id, code, name, phone, address, external_ref)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id::text`, w.cid, code, s.Name, s.Phone, s.Address, s.Ref).Scan(&id); err != nil {
			return err
		}
		w.ids[s.Ref] = id
		w.created["suppliers"]++
	}
	return nil
}

// ---- accounts used by the opening balances ----

func (w *magicWriter) loadAccounts() error {
	rows, err := w.tx.Query(w.ctx, `SELECT code, id::text, is_postable FROM accounts WHERE company_id = $1`, w.cid)
	if err != nil {
		return err
	}
	defer rows.Close()
	w.groups = map[string]string{}
	for rows.Next() {
		var code, id string
		var postable bool
		if err := rows.Scan(&code, &id, &postable); err != nil {
			return err
		}
		if postable {
			w.accounts[code] = id
		} else {
			w.groups[code] = id
		}
	}
	return rows.Err()
}

// ensureAccount returns the postable account with this code, creating it (under the closest group) if needed.
func (w *magicWriter) ensureAccount(code, typ string, names map[string]string) (string, error) {
	if id, ok := w.accounts[code]; ok {
		return id, nil
	}
	want := code
	if _, isGroup := w.groups[code]; isGroup {
		want = code + "-MG" // a group with that code exists: post to a child account
		if id, ok := w.accounts[want]; ok {
			return id, nil
		}
	}
	var parent *string
	for i := len(code); i >= 1; i-- {
		if id, ok := w.groups[code[:i]]; ok {
			parent = &id
			break
		}
	}
	name := names["en"]
	if name == "" {
		name = names["ar"]
	}
	if name == "" {
		name = "Magic " + code
	}
	i18n := map[string]string{}
	for k, v := range names {
		if v != "" {
			i18n[k] = v
		}
	}
	nj, _ := json.Marshal(i18n)
	var id string
	if err := w.tx.QueryRow(w.ctx, `INSERT INTO accounts (company_id, code, name, name_i18n, type, parent_id, is_postable)
		VALUES ($1, $2, $3, $4, $5, $6, true) RETURNING id::text`, w.cid, want, name, nj, typ, parent).Scan(&id); err != nil {
		return "", err
	}
	w.accounts[want] = id
	w.newAccts = append(w.newAccts, want+" "+name)
	return id, nil
}

// glAccount maps a Magic ledger (+ currency) to a Paxve account: cash ledgers (53…) and banks (512…) go to the
// company's cash box / bank account of that currency, everything else to the account with the same code.
func (w *magicWriter) glAccount(g magic.PGL) (string, error) {
	kind := ""
	switch {
	case strings.HasPrefix(g.Code, "53"):
		kind = "cash"
	case strings.HasPrefix(g.Code, "512"):
		kind = "bank"
	}
	if kind != "" {
		var acc string
		err := w.tx.QueryRow(w.ctx, `SELECT account_id::text FROM payment_methods WHERE company_id = $1 AND kind = $2 AND currency = $3
			AND user_id IS NULL AND is_active ORDER BY sort, created_at LIMIT 1`, w.cid, kind, g.Currency).Scan(&acc)
		if err == nil {
			return acc, nil
		}
		if err != pgx.ErrNoRows {
			return "", err
		}
	}
	return w.ensureAccount(g.Code, g.Type, map[string]string{"en": g.NameEn, "ar": g.NameAr, "fr": g.NameFr})
}

// ensureRate makes sure an official rate exists for cur on or before date (the opening rate of the migration).
func (w *magicWriter) ensureRate(cur, date string) error {
	if cur == w.set.Base {
		return nil
	}
	if _, err := resolveRate(w.ctx, w.tx, w.cid, w.set.Base, cur, date, ""); err == nil {
		return nil
	}
	rate := w.plan.Rate
	switch {
	case w.set.Base == "USD" && cur == "LBP":
	case w.set.Base == "LBP" && cur == "USD":
		rate = decimal.NewFromInt(1).DivRound(w.plan.Rate, 10)
	default:
		return &apiError{status: http.StatusUnprocessableEntity, code: "no_rate", detail: cur}
	}
	w.notes = append(w.notes, fmt.Sprintf("rate_added %s %s %s", cur, date, rate))
	return w.exec(`INSERT INTO exchange_rates (company_id, currency, rate_date, kind, rate) VALUES ($1, $2, $3::date, 'official', $4::numeric)
		ON CONFLICT (tenant_id, company_id, currency, kind, rate_date) DO NOTHING`, w.cid, cur, date, rate.String())
}

func (w *magicWriter) balances() error {
	ctx, tx, p := w.ctx, w.tx, w.plan
	w.groups = nil
	if err := w.loadAccounts(); err != nil {
		return err
	}
	var err error
	if w.clearing, err = w.ensureAccount("4718", "liability", map[string]string{"en": "Magic migration clearing",
		"ar": "حساب وسيط - ترحيل ماجيك", "fr": "Compte d'attente - migration Magic"}); err != nil {
		return err
	}
	// rates: on the opening date, and before the first foreign-currency document
	first := map[string]string{}
	note := func(cur, date string) {
		if old, ok := first[cur]; !ok || date < old {
			first[cur] = date
		}
	}
	for _, b := range p.Bills {
		note(b.Currency, b.Issue)
	}
	for _, o := range p.CustOpen {
		note(o.Currency, p.OpeningDate)
	}
	for _, o := range p.SuppOpen {
		note(o.Currency, p.OpeningDate)
	}
	for _, g := range p.GL {
		note(g.Currency, p.OpeningDate)
	}
	for cur, date := range first {
		if err := w.ensureRate(cur, date); err != nil {
			return err
		}
	}

	// 1. unpaid bills (Dr receivables / Cr clearing: the revenue was already booked in Magic)
	for i, b := range p.Bills {
		cust := w.ids[b.CustomerRef]
		if cust == "" {
			w.skipped["open_bills"]++
			continue
		}
		var sub, period *string
		if x := w.ids[b.SubRef]; x != "" {
			sub = &x
			if b.Period != "" {
				var exists bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM invoices WHERE subscription_id = $1 AND period = $2 AND status <> 'void')`,
					x, b.Period).Scan(&exists); err != nil {
					return err
				}
				if !exists {
					per := b.Period
					period = &per
				}
			}
		}
		name, err := lockCustomer(ctx, tx, w.cid, cust)
		if err != nil {
			return err
		}
		line := invLine{Kind: "other", Description: b.Description, Quantity: decimal.NewFromInt(1), UnitPrice: b.Amount, Amount: b.Amount, Meta: b.Meta}
		if _, _, err := issueInvoice(withExternalRef(ctx, b.Ref), tx, w.cid, w.set, cust, name, sub, period, b.Issue, b.Due, b.Currency,
			w.clearing, "Magic", []invLine{line}); err != nil {
			return err
		}
		w.created["open_bills"]++
		if i%250 == 0 {
			w.progress("open_bills", i, len(p.Bills))
		}
	}
	// 2. customer balances not explained by the unpaid bills: debts first, then credits (applied oldest bill first)
	for _, o := range p.CustOpen {
		if !o.Amount.IsPositive() {
			continue
		}
		cust := w.ids[o.PartyRef]
		name, err := lockCustomer(ctx, tx, w.cid, cust)
		if err != nil {
			return err
		}
		line := invLine{Kind: "other", Description: "Opening balance (Magic)", Quantity: decimal.NewFromInt(1), UnitPrice: o.Amount, Amount: o.Amount}
		if _, _, err := issueInvoice(withExternalRef(ctx, o.Ref), tx, w.cid, w.set, cust, name, nil, nil, p.OpeningDate, p.OpeningDate,
			o.Currency, w.clearing, "Opening balance (Magic)", []invLine{line}); err != nil {
			return err
		}
		w.created["opening_debits"]++
	}
	for _, o := range p.CustOpen {
		if !o.Amount.IsNegative() {
			continue
		}
		if err := w.openingCredit(o); err != nil {
			return err
		}
		w.created["opening_credits"]++
	}
	// 3. suppliers we owe: opening bills (Dr clearing / Cr payables)
	for _, o := range p.SuppOpen {
		if err := w.openingBill(o); err != nil {
			return err
		}
		w.created["supplier_bills"]++
	}
	// 4. the opening journal entry (every other balance)
	return w.openingEntry()
}

func (w *magicWriter) openingCredit(o magic.POpen) error {
	ctx, tx := w.ctx, w.tx
	cust := w.ids[o.PartyRef]
	name, err := lockCustomer(ctx, tx, w.cid, cust)
	if err != nil {
		return err
	}
	amount := o.Amount.Neg()
	rate, err := resolveRate(ctx, tx, w.cid, w.set.Base, o.Currency, w.plan.OpeningDate, "")
	if err != nil {
		return err
	}
	var number int64
	if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'opening_credit')`, w.cid).Scan(&number); err != nil {
		return err
	}
	je, err := postJournal(ctx, tx, w.cid, w.set, w.plan.OpeningDate, fmt.Sprintf("Opening credit #%d · %s (Magic)", number, name),
		fmt.Sprintf("OC-%d", number), "opening", []jline{
			{Account: w.clearing, Desc: "Magic", Currency: o.Currency, Debit: amount, Rate: rate},
			{Account: *w.set.AR, Desc: name, Currency: o.Currency, Credit: amount, Rate: rate},
		})
	if err != nil {
		return err
	}
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO payments (company_id, number, customer_id, payment_date, currency, amount, journal_entry_id,
		notes, kind, ar_rate, external_ref) VALUES ($1, $2, $3, $4::date, $5, $6::numeric, $7, $8, 'opening_credit', $9::numeric, $10)
		RETURNING id::text`, w.cid, number, cust, w.plan.OpeningDate, o.Currency, amount.String(), je,
		"Opening credit (Magic)", rate, o.Ref).Scan(&id); err != nil {
		return err
	}
	return autoAllocate(ctx, tx, w.cid, w.set, cust, o.Currency, "")
}

func (w *magicWriter) openingBill(o magic.POpen) error {
	ctx, tx := w.ctx, w.tx
	if w.ps.AP == nil {
		return notSetup("ap")
	}
	sup := w.ids[o.PartyRef]
	if sup == "" {
		return nil
	}
	name, err := lockSupplier(ctx, tx, w.cid, sup)
	if err != nil {
		return err
	}
	rate, err := resolveRate(ctx, tx, w.cid, w.set.Base, o.Currency, w.plan.OpeningDate, "")
	if err != nil {
		return err
	}
	var number int64
	if err := tx.QueryRow(ctx, `SELECT next_doc_number($1, 'bill')`, w.cid).Scan(&number); err != nil {
		return err
	}
	je, err := postJournal(ctx, tx, w.cid, w.set, w.plan.OpeningDate, fmt.Sprintf("Bill #%d · %s (Magic opening balance)", number, name),
		fmt.Sprintf("BILL-%d", number), "bill", []jline{
			{Account: w.clearing, Desc: "Magic", Currency: o.Currency, Debit: o.Amount, Rate: rate},
			{Account: *w.ps.AP, Desc: name, Currency: o.Currency, Credit: o.Amount, Rate: rate},
		})
	if err != nil {
		return err
	}
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO bills (company_id, number, supplier_id, supplier_ref, bill_date, due_date, currency, total,
		journal_entry_id, notes, external_ref) VALUES ($1, $2, $3, 'Magic', $4::date, $4::date, $5, $6::numeric, $7, $8, $9) RETURNING id::text`,
		w.cid, number, sup, w.plan.OpeningDate, o.Currency, o.Amount.String(), je, "Opening balance (Magic)", o.Ref).Scan(&id); err != nil {
		return err
	}
	if err := w.exec(`INSERT INTO bill_lines (bill_id, line_no, account_id, description, quantity, unit_price, amount)
		VALUES ($1, 1, $2, 'Opening balance (Magic)', 1, $3::numeric, $3::numeric)`, id, w.clearing, o.Amount.String()); err != nil {
		return err
	}
	return autoAllocateSupplier(ctx, tx, w.cid, sup, o.Currency)
}

// openingEntry posts one journal entry with every remaining balance. Lines that do not balance in base currency are
// explained in two parts: Magic's own unbalanced entries (to the "Magic migration difference" account) and the
// revaluation of foreign-currency balances at the opening rate (exchange gain / loss).
func (w *magicWriter) openingEntry() error {
	ctx, tx, p := w.ctx, w.tx, w.plan
	if len(p.GL) == 0 {
		w.notes = append(w.notes, "no_opening_entry")
		return nil
	}
	var entry string
	ref := magic.CompanyRef(p.Company.ID) + ":opening"
	if err := tx.QueryRow(ctx, `INSERT INTO journal_entries (company_id, entry_date, memo, reference, source, external_ref)
		VALUES ($1, $2::date, $3, $4, 'opening', $5) RETURNING id::text`, w.cid, p.OpeningDate,
		fmt.Sprintf("Opening balances · Magic company %d (%s)", p.Company.ID, p.Company.Name), "MAGIC-"+strconv.Itoa(p.Company.ID), ref).Scan(&entry); err != nil {
		return err
	}
	n := 0
	insert := func(account, desc, cur string, amount decimal.Decimal, rate string) error {
		if amount.IsZero() {
			return nil
		}
		dr, cr := amount, decimal.Zero
		if amount.IsNegative() {
			dr, cr = decimal.Zero, amount.Neg()
		}
		n++
		return w.exec(`INSERT INTO journal_lines (entry_id, line_no, account_id, description, currency, debit, credit, rate)
			VALUES ($1, $2, $3, $4, $5, $6::numeric, $7::numeric, $8::numeric)`, entry, n, account, desc, cur, dr.String(), cr.String(), rate)
	}
	rates := map[string]string{}
	rateOf := func(cur string) (string, error) {
		if r, ok := rates[cur]; ok {
			return r, nil
		}
		r, err := resolveRate(ctx, tx, w.cid, w.set.Base, cur, p.OpeningDate, "")
		rates[cur] = r
		return r, err
	}
	for _, g := range p.GL {
		var acc, desc string
		var err error
		switch g.Kind {
		case "gl":
			acc, err = w.glAccount(g)
			desc = "Magic " + g.Code
			if g.NameAr != "" {
				desc += " " + g.NameAr
			} else if g.NameEn != "" {
				desc += " " + g.NameEn
			}
		case "retained":
			acc, err = w.ensureAccount("12", "equity", map[string]string{"en": "Retained earnings / result", "ar": "النتيجة المدورة", "fr": "Report à nouveau"})
			desc = "Magic: result of previous years"
		case "ar_clearing":
			acc, desc = w.clearing, "Magic: customer balances (see the customer documents)"
		case "ap_clearing":
			acc, desc = w.clearing, "Magic: supplier balances (see the supplier bills)"
		case "ap_direct":
			if w.ps.AP == nil {
				return notSetup("ap")
			}
			acc, desc = *w.ps.AP, "Magic: advances paid to suppliers"
		default:
			continue
		}
		if err != nil {
			return err
		}
		rate, err := rateOf(g.Currency)
		if err != nil {
			return err
		}
		if len(desc) > 200 {
			desc = desc[:200]
		}
		if err := insert(acc, desc, g.Currency, g.Amount, rate); err != nil {
			return err
		}
	}
	var diffS string
	if err := tx.QueryRow(ctx, `SELECT (coalesce(sum(base_debit), 0) - coalesce(sum(base_credit), 0))::text FROM journal_lines WHERE entry_id = $1`,
		entry).Scan(&diffS); err != nil {
		return err
	}
	diff, _ := decimal.NewFromString(diffS)
	// part 1: Magic's own imbalance (in base currency USD; other bases: everything goes to the difference account)
	magicPart := diff
	if w.set.Base == "USD" {
		magicPart = p.MagicImbalanceUSD
	}
	if !magicPart.IsZero() {
		acc, err := w.ensureAccount("4719", "equity", map[string]string{"en": "Magic migration difference",
			"ar": "فرق ترحيل ماجيك", "fr": "Écart de migration Magic"})
		if err != nil {
			return err
		}
		if err := insert(acc, "Magic: unbalanced entries in the old system", w.set.Base, magicPart.Neg(), "1"); err != nil {
			return err
		}
	}
	reval := diff.Sub(magicPart)
	switch {
	case reval.IsPositive():
		if err := insert(*w.set.FXGain, "Magic: revaluation at the opening rate", w.set.Base, reval.Neg(), "1"); err != nil {
			return err
		}
	case reval.IsNegative():
		if err := insert(*w.set.FXLoss, "Magic: revaluation at the opening rate", w.set.Base, reval.Neg(), "1"); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `SELECT post_journal_entry($1)`, entry); err != nil {
		return err
	}
	w.created["opening_entry_lines"] = n
	w.notes = append(w.notes, "magic_difference "+magicPart.String()+" "+w.set.Base, "revaluation "+reval.String()+" "+w.set.Base)
	return nil
}
