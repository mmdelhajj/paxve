package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Custom fields: each company adds its own fields (text, number, date, choice, yes/no) to customers, suppliers,
// items, invoices and employees. Values live in the record's "custom" jsonb column; the report screen shows them as
// grid columns (filter row and Excel / CSV export come with the grid).

var customEntities = map[string]struct{ table, label string }{
	"customers": {"customers", "c.name"}, "suppliers": {"suppliers", "c.name"}, "items": {"items", "c.name"},
	"invoices": {"invoices", "'#' || c.number"}, "employees": {"employees", "c.name"},
}

var customKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

const customFieldCols = `id, entity, key, label, label_i18n, type, options, required, show_in_list, position, is_active`

// GET /companies/{cid}/custom-fields?entity=
func (s *Server) listCustomFields(w http.ResponseWriter, r *http.Request) {
	ent := r.URL.Query().Get("entity")
	if _, ok := customEntities[ent]; ent != "" && !ok {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+customFieldCols+` FROM custom_fields WHERE company_id = $1 AND ($2 = '' OR entity = $2)
			ORDER BY entity, position, label`, r.PathValue("cid"), ent)
	})
}

// POST /companies/{cid}/custom-fields · PUT /companies/{cid}/custom-fields/{id}
func (s *Server) saveCustomField(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Entity     string            `json:"entity"`
		Key        string            `json:"key"`
		Label      string            `json:"label"`
		LabelI18n  map[string]string `json:"label_i18n"`
		Type       string            `json:"type"`
		Options    []string          `json:"options"`
		Required   bool              `json:"required"`
		ShowInList *bool             `json:"show_in_list"`
		Position   int               `json:"position"`
		IsActive   *bool             `json:"is_active"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	_, entOK := customEntities[in.Entity]
	typeOK := map[string]bool{"text": true, "number": true, "date": true, "choice": true, "bool": true}[in.Type]
	opts := []string{}
	for _, o := range in.Options {
		if o = strings.TrimSpace(o); o != "" && len(o) <= 100 {
			opts = append(opts, o)
		}
	}
	if (id == "" && (!entOK || !customKeyRe.MatchString(in.Key))) || (id != "" && !isUUID(id)) || !typeOK || strings.TrimSpace(in.Label) == "" ||
		len(in.Label) > 100 || (in.Type == "choice" && len(opts) == 0) || len(opts) > 100 || len(in.LabelI18n) > 20 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if in.LabelI18n == nil {
		in.LabelI18n = map[string]string{}
	}
	li, _ := json.Marshal(in.LabelI18n)
	show := in.ShowInList == nil || *in.ShowInList
	active := in.IsActive == nil || *in.IsActive
	cid := r.PathValue("cid")
	s.run(w, r, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[id == ""], func(ctx context.Context, tx pgx.Tx) (any, error) {
		if id == "" {
			err := tx.QueryRow(ctx, `INSERT INTO custom_fields (company_id, entity, key, label, label_i18n, type, options, required, show_in_list, position, is_active)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING id::text`, cid, in.Entity, in.Key, strings.TrimSpace(in.Label), li, in.Type, opts,
				in.Required, show, in.Position, active).Scan(&id)
			if isUniqueViolation(err) {
				return nil, &apiError{status: http.StatusConflict, code: "duplicate_key"}
			}
			if err != nil {
				return nil, err
			}
		} else {
			tag, err := tx.Exec(ctx, `UPDATE custom_fields SET label = $3, label_i18n = $4, type = $5, options = $6, required = $7, show_in_list = $8,
				position = $9, is_active = $10 WHERE id = $1 AND company_id = $2`, id, cid, strings.TrimSpace(in.Label), li, in.Type, opts, in.Required, show,
				in.Position, active)
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() == 0 {
				return nil, notFound()
			}
		}
		return queryJSONOne(ctx, tx, `SELECT `+customFieldCols+` FROM custom_fields WHERE id = $1`, id)
	})
}

type customDef struct {
	key, typ string
	options  []string
	required bool
}

func loadCustomDefs(ctx context.Context, tx pgx.Tx, cid, entity string) ([]customDef, error) {
	rows, err := tx.Query(ctx, `SELECT key, type, options, required FROM custom_fields WHERE company_id = $1 AND entity = $2 AND is_active ORDER BY position`, cid, entity)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (customDef, error) {
		var d customDef
		err := r.Scan(&d.key, &d.typ, &d.options, &d.required)
		return d, err
	})
}

// cleanCustom validates values against the definitions and returns what to store (unknown keys are dropped).
func cleanCustom(defs []customDef, in map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for _, d := range defs {
		v, ok := in[d.key]
		if !ok || v == nil || v == "" {
			if d.required {
				return nil, &apiError{status: http.StatusBadRequest, code: "custom_required", detail: d.key}
			}
			continue
		}
		bad := &apiError{status: http.StatusBadRequest, code: "custom_invalid", detail: d.key}
		switch d.typ {
		case "text":
			s, ok := v.(string)
			if !ok || len(s) > 1000 {
				return nil, bad
			}
			out[d.key] = strings.TrimSpace(s)
		case "number":
			var s string
			switch x := v.(type) {
			case string:
				s = x
			case float64:
				s = decimal.NewFromFloat(x).String()
			default:
				return nil, bad
			}
			n, err := decimal.NewFromString(strings.TrimSpace(s))
			if err != nil {
				return nil, bad
			}
			out[d.key] = n.String()
		case "date":
			s, ok := v.(string)
			if !ok || !validDate(s) {
				return nil, bad
			}
			out[d.key] = s
		case "choice":
			s, ok := v.(string)
			found := false
			for _, o := range d.options {
				found = found || o == s
			}
			if !ok || !found {
				return nil, bad
			}
			out[d.key] = s
		case "bool":
			b, ok := v.(bool)
			if !ok {
				return nil, bad
			}
			out[d.key] = b
		}
	}
	return out, nil
}

// GET / PUT /companies/{cid}/custom-values/{entity}/{id} {values: {key: value}}
func (s *Server) getCustomValues(w http.ResponseWriter, r *http.Request) {
	ent, id := r.PathValue("entity"), r.PathValue("id")
	e, ok := customEntities[ent]
	if !ok || !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		vals, err := queryJSONOne(ctx, tx, `SELECT custom FROM `+e.table+` WHERE id = $1 AND company_id = $2`, id, cid)
		if err != nil {
			return nil, err
		}
		fields, err := queryJSON(ctx, tx, `SELECT `+customFieldCols+` FROM custom_fields WHERE company_id = $1 AND entity = $2 AND is_active ORDER BY position, label`, cid, ent)
		if err != nil {
			return nil, err
		}
		var wrap struct {
			Custom json.RawMessage `json:"custom"`
		}
		_ = json.Unmarshal(vals, &wrap)
		return map[string]json.RawMessage{"fields": fields, "values": wrap.Custom}, nil
	})
}

func (s *Server) putCustomValues(w http.ResponseWriter, r *http.Request) {
	ent, id := r.PathValue("entity"), r.PathValue("id")
	e, ok := customEntities[ent]
	if !ok || !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	var in struct {
		Values map[string]any `json:"values"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.Values == nil {
		in.Values = map[string]any{}
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		defs, err := loadCustomDefs(ctx, tx, cid, ent)
		if err != nil {
			return nil, err
		}
		clean, err := cleanCustom(defs, in.Values)
		if err != nil {
			return nil, err
		}
		b, _ := json.Marshal(clean)
		var out json.RawMessage
		err = tx.QueryRow(ctx, `UPDATE `+e.table+` SET custom = $3 WHERE id = $1 AND company_id = $2 RETURNING custom`, id, cid, b).Scan(&out)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFound()
		}
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"values": out}, nil
	})
}

// GET /companies/{cid}/custom-fields/report?entity=&key=&value=: the records with their custom values (one column per
// field), optionally only those whose field key equals value.
func (s *Server) customReport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ent, key, val := q.Get("entity"), q.Get("key"), q.Get("value")
	e, ok := customEntities[ent]
	if !ok || (key != "" && !customKeyRe.MatchString(key)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	code := "c.code"
	if ent == "invoices" {
		code = "c.number::text"
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		fields, err := queryJSON(ctx, tx, `SELECT `+customFieldCols+` FROM custom_fields WHERE company_id = $1 AND entity = $2 AND is_active ORDER BY position, label`,
			r.PathValue("cid"), ent)
		if err != nil {
			return nil, err
		}
		rows, err := queryJSON(ctx, tx, `SELECT c.id, `+code+` AS code, `+e.label+` AS name, c.custom FROM `+e.table+` c
			WHERE c.company_id = $1 AND ($2 = '' OR c.custom ->> $2 = $3) ORDER BY 3 LIMIT 5000`, r.PathValue("cid"), key, val)
		if err != nil {
			return nil, err
		}
		return map[string]json.RawMessage{"fields": fields, "rows": rows}, nil
	})
}
