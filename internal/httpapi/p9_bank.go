package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Phase 9 â€” bank statements like the big systems: files straight from the bank (OFX, MT940, CAMT.053, CSV with a
// column map), duplicate lines refused, matching rules, suggestions against open invoices / bills with a confidence,
// and one-click reconcile (the payment is booked and matched in one step).

type stmtFileLine struct {
	Date        string `json:"date"`
	Description string `json:"description"`
	Amount      string `json:"amount"`
	Reference   string `json:"reference"`
	Partner     string `json:"partner"`
	BankID      string `json:"bank_id,omitempty"` // FITID / AcctSvcrRef: the bank's own id of the line
	Fingerprint string `json:"fingerprint,omitempty"`
}

type csvMap struct {
	Delimiter  string `json:"delimiter"`   // "," ";" "\t" ("" = guess)
	Header     bool   `json:"header"`      // first row holds the column names
	Skip       int    `json:"skip"`        // rows to skip before the data (after the header)
	Date       string `json:"date"`        // column: 0-based index or header name
	Amount     string `json:"amount"`      // signed amount column, orâ€¦
	Debit      string `json:"debit"`       // â€¦money out andâ€¦
	Credit     string `json:"credit"`      // â€¦money in columns
	Desc       string `json:"description"` // column(s), "+"-joined
	Ref        string `json:"reference"`
	Partner    string `json:"partner"`
	DateFormat string `json:"date_format"` // YYYY-MM-DD | DD/MM/YYYY | MM/DD/YYYY | DD.MM.YYYY | DD-MM-YYYY | YYYYMMDD
	Decimal    string `json:"decimal"`     // "." (default) or ","
}

var dateLayouts = map[string]string{
	"YYYY-MM-DD": "2006-01-02", "DD/MM/YYYY": "02/01/2006", "MM/DD/YYYY": "01/02/2006", "DD.MM.YYYY": "02.01.2006",
	"DD-MM-YYYY": "02-01-2006", "YYYYMMDD": "20060102", "DD/MM/YY": "02/01/06", "YYYY/MM/DD": "2006/01/02",
}

// parseAmount reads "1,234.50", "1.234,50", "(12.00)", "12.00-", "-12" with the given decimal mark.
func bankAmount(s, dec string) (decimal.Decimal, bool) {
	s = strings.TrimSpace(strings.ReplaceAll(s, "آ ", ""))
	s = strings.ReplaceAll(s, " ", "")
	if s == "" {
		return decimal.Zero, false
	}
	neg := false
	if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		neg, s = true, s[1:len(s)-1]
	}
	if strings.HasSuffix(s, "-") {
		neg, s = true, strings.TrimSuffix(s, "-")
	}
	s = strings.TrimLeft(s, "+")
	for _, cur := range []string{"$", "â‚¬", "آ£", "USD", "EUR", "LBP", "L.L"} {
		s = strings.ReplaceAll(s, cur, "")
	}
	if dec == "," {
		s = strings.ReplaceAll(s, ".", "")
		s = strings.ReplaceAll(s, ",", ".")
	} else {
		s = strings.ReplaceAll(s, ",", "")
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero, false
	}
	if neg {
		d = d.Neg()
	}
	return d, true
}

func parseCSVStatement(content string, m csvMap) ([]stmtFileLine, []string, error) {
	content = strings.TrimPrefix(content, "ï»؟")
	delim := m.Delimiter
	if delim == "\\t" || delim == "tab" {
		delim = "\t"
	}
	if delim == "" {
		first := content
		if i := strings.IndexAny(first, "\r\n"); i > 0 {
			first = first[:i]
		}
		delim = ","
		best := strings.Count(first, ",")
		for _, d := range []string{";", "\t", "|"} {
			if n := strings.Count(first, d); n > best {
				delim, best = d, n
			}
		}
	}
	if len([]rune(delim)) != 1 {
		return nil, nil, badRequest("bad_delimiter")
	}
	rd := csv.NewReader(strings.NewReader(content))
	rd.Comma = []rune(delim)[0]
	rd.FieldsPerRecord = -1
	rd.LazyQuotes = true
	rd.TrimLeadingSpace = true
	rows, err := rd.ReadAll()
	if err != nil {
		return nil, nil, badRequest("bad_file")
	}
	var header []string
	if m.Header && len(rows) > 0 {
		header, rows = rows[0], rows[1:]
	}
	if m.Skip > 0 {
		if m.Skip >= len(rows) {
			rows = nil
		} else {
			rows = rows[m.Skip:]
		}
	}
	col := func(spec string) []int {
		var out []int
		for _, p := range strings.Split(spec, "+") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if n, err := strconv.Atoi(p); err == nil && n >= 0 {
				out = append(out, n)
				continue
			}
			for i, h := range header {
				if strings.EqualFold(strings.TrimSpace(h), p) {
					out = append(out, i)
					break
				}
			}
		}
		return out
	}
	get := func(row []string, idx []int) string {
		var parts []string
		for _, i := range idx {
			if i < len(row) && strings.TrimSpace(row[i]) != "" {
				parts = append(parts, strings.TrimSpace(row[i]))
			}
		}
		return strings.Join(parts, " ")
	}
	dc, ac, deb, cre := col(m.Date), col(m.Amount), col(m.Debit), col(m.Credit)
	if len(dc) == 0 || (len(ac) == 0 && len(deb) == 0 && len(cre) == 0) {
		return nil, nil, badRequest("mapping_required")
	}
	layout := dateLayouts[m.DateFormat]
	var out []stmtFileLine
	var warn []string
	for n, row := range rows {
		if len(row) == 0 || (len(row) == 1 && strings.TrimSpace(row[0]) == "") {
			continue
		}
		ds := get(row, dc)
		var dt time.Time
		ok := false
		if layout != "" {
			dt, err = time.Parse(layout, ds)
			ok = err == nil
		} else {
			for _, l := range []string{"2006-01-02", "02/01/2006", "02.01.2006", "20060102", "2006/01/02", "02-01-2006"} {
				if dt, err = time.Parse(l, ds); err == nil {
					ok = true
					break
				}
			}
		}
		var amt decimal.Decimal
		aok := false
		if len(ac) > 0 {
			amt, aok = bankAmount(get(row, ac), m.Decimal)
		} else {
			in, iok := bankAmount(get(row, cre), m.Decimal)
			out2, ook := bankAmount(get(row, deb), m.Decimal)
			if iok || ook {
				amt, aok = in.Abs().Sub(out2.Abs()), true
			}
		}
		if !ok || !aok {
			if len(warn) < 20 {
				warn = append(warn, fmt.Sprintf("row %d skipped", n+1+m.Skip+map[bool]int{true: 1}[m.Header]))
			}
			continue
		}
		if amt.IsZero() {
			continue
		}
		out = append(out, stmtFileLine{Date: dt.Format(time.DateOnly), Amount: amt.String(), Description: get(row, col(m.Desc)),
			Reference: get(row, col(m.Ref)), Partner: get(row, col(m.Partner))})
	}
	return out, warn, nil
}

func ofxTag(block, tag string) string {
	re := regexp.MustCompile(`(?is)<` + tag + `>([^<\r\n]*)`)
	if m := re.FindStringSubmatch(block); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func parseOFX(content string) ([]stmtFileLine, string, error) {
	cur := ofxTag(content, "CURDEF")
	var out []stmtFileLine
	parts := strings.Split(content, "<STMTTRN>")
	pos := len(parts[0]) + len("<STMTTRN>")
	for _, p := range parts[1:] {
		// each transaction runs to its closing tag, the next transaction or the end of the list (SGML OFX has no closing tags)
		end := len(p)
		for _, stop := range []string{"</STMTTRN>", "</BANKTRANLIST>"} {
			if i := strings.Index(p, stop); i >= 0 && i < end {
				end = i
			}
		}
		b := content[pos : pos+end]
		pos += len(p) + len("<STMTTRN>")
		ds := ofxTag(b, "DTPOSTED")
		if len(ds) < 8 {
			continue
		}
		dt, err := time.Parse("20060102", ds[:8])
		if err != nil {
			continue
		}
		amt, ok := bankAmount(ofxTag(b, "TRNAMT"), ".")
		if !ok || amt.IsZero() {
			continue
		}
		name, memo := ofxTag(b, "NAME"), ofxTag(b, "MEMO")
		desc := strings.TrimSpace(strings.Join([]string{name, memo}, " "))
		ref := ofxTag(b, "REFNUM")
		if ref == "" {
			ref = ofxTag(b, "CHECKNUM")
		}
		out = append(out, stmtFileLine{Date: dt.Format(time.DateOnly), Amount: amt.String(), Description: desc, Reference: ref, Partner: name,
			BankID: ofxTag(b, "FITID")})
	}
	if len(out) == 0 {
		return nil, cur, badRequest("no_lines")
	}
	return out, cur, nil
}

var mt61Re = regexp.MustCompile(`^(\d{6})(\d{4})?(R?[CD])([A-Z])?([0-9,]+)([A-Z][A-Z0-9]{3})?([^/]*)(?://(.*))?`)

func parseMT940(content string) ([]stmtFileLine, string, error) {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	// fields start with :NN: at the beginning of a line; continuation lines belong to the field before
	type field struct{ tag, val string }
	var fields []field
	for _, ln := range strings.Split(content, "\n") {
		if len(ln) > 3 && ln[0] == ':' {
			if i := strings.Index(ln[1:], ":"); i > 0 && i <= 4 {
				fields = append(fields, field{ln[1 : i+1], ln[i+2:]})
				continue
			}
		}
		if len(fields) > 0 && strings.TrimSpace(ln) != "" && !strings.HasPrefix(ln, "-}") {
			fields[len(fields)-1].val += "\n" + ln
		}
	}
	cur := ""
	var out []stmtFileLine
	for i, f := range fields {
		switch f.tag {
		case "60F", "60M":
			if len(f.val) >= 10 {
				cur = f.val[7:10]
			}
		case "61":
			first := strings.SplitN(f.val, "\n", 2)
			m := mt61Re.FindStringSubmatch(strings.TrimSpace(first[0]))
			if m == nil {
				continue
			}
			dt, err := time.Parse("060102", m[1])
			if err != nil {
				continue
			}
			amt, ok := bankAmount(m[5], ",")
			if !ok || amt.IsZero() {
				continue
			}
			if m[3] == "D" || m[3] == "RC" {
				amt = amt.Neg()
			}
			ref := strings.TrimSpace(m[7])
			if ref == "NONREF" {
				ref = ""
			}
			bankRef := strings.TrimSpace(m[8])
			desc := ""
			if len(first) > 1 {
				desc = strings.TrimSpace(first[1])
			}
			partner := ""
			if i+1 < len(fields) && fields[i+1].tag == "86" {
				info := strings.ReplaceAll(fields[i+1].val, "\n", "")
				// structured ?20â€¦?29 purpose, ?32/?33 name (German banks); plain text otherwise
				if strings.Contains(info, "?") {
					var purpose, name []string
					for _, p := range strings.Split(info, "?")[1:] {
						if len(p) < 2 {
							continue
						}
						code, v := p[:2], strings.TrimSpace(p[2:])
						if code >= "20" && code <= "29" || code >= "60" && code <= "63" {
							purpose = append(purpose, v)
						} else if code == "32" || code == "33" {
							name = append(name, v)
						}
					}
					desc = strings.TrimSpace(desc + " " + strings.Join(purpose, " "))
					partner = strings.Join(name, " ")
				} else {
					desc = strings.TrimSpace(desc + " " + info)
				}
			}
			bid := ""
			if bankRef != "" {
				bid = m[1] + ":" + bankRef
			}
			out = append(out, stmtFileLine{Date: dt.Format(time.DateOnly), Amount: amt.String(), Description: desc, Reference: ref, Partner: partner, BankID: bid})
		}
	}
	if len(out) == 0 {
		return nil, cur, badRequest("no_lines")
	}
	return out, cur, nil
}

type camtDoc struct {
	Stmts []struct {
		Acct struct {
			Ccy string `xml:"Ccy"`
		} `xml:"Acct"`
		Ntries []struct {
			Amt struct {
				Ccy string `xml:"Ccy,attr"`
				V   string `xml:",chardata"`
			} `xml:"Amt"`
			CdtDbtInd string `xml:"CdtDbtInd"`
			Sts       struct {
				V  string `xml:",chardata"`
				Cd string `xml:"Cd"`
			} `xml:"Sts"`
			BookgDt   string `xml:"BookgDt>Dt"`
			BookgDtTm string `xml:"BookgDt>DtTm"`
			ValDt     string `xml:"ValDt>Dt"`
			SvcrRef   string `xml:"AcctSvcrRef"`
			Addtl     string `xml:"AddtlNtryInf"`
			Tx        []struct {
				E2E    string   `xml:"Refs>EndToEndId"`
				TxID   string   `xml:"Refs>TxId"`
				Ustrd  []string `xml:"RmtInf>Ustrd"`
				CrRef  string   `xml:"RmtInf>Strd>CdtrRefInf>Ref"`
				Dbtr   string   `xml:"RltdPties>Dbtr>Nm"`
				DbtrP  string   `xml:"RltdPties>Dbtr>Pty>Nm"`
				Cdtr   string   `xml:"RltdPties>Cdtr>Nm"`
				CdtrP  string   `xml:"RltdPties>Cdtr>Pty>Nm"`
				AddInf string   `xml:"AddtlTxInf"`
			} `xml:"NtryDtls>TxDtls"`
		} `xml:"Ntry"`
	} `xml:"BkToCstmrStmt>Stmt"`
}

func parseCAMT(content string) ([]stmtFileLine, string, error) {
	var d camtDoc
	dec := xml.NewDecoder(strings.NewReader(content))
	dec.Strict = false
	if err := dec.Decode(&d); err != nil {
		return nil, "", badRequest("bad_file")
	}
	cur := ""
	var out []stmtFileLine
	for _, st := range d.Stmts {
		if st.Acct.Ccy != "" {
			cur = st.Acct.Ccy
		}
		for _, e := range st.Ntries {
			if s := firstNonEmpty(e.Sts.Cd, strings.TrimSpace(e.Sts.V)); s != "" && !strings.EqualFold(s, "BOOK") {
				continue // pending entries are not on the account yet
			}
			amt, ok := bankAmount(e.Amt.V, ".")
			if !ok || amt.IsZero() {
				continue
			}
			if strings.EqualFold(e.CdtDbtInd, "DBIT") {
				amt = amt.Neg()
			}
			if cur == "" {
				cur = e.Amt.Ccy
			}
			ds := firstNonEmpty(e.BookgDt, e.BookgDtTm, e.ValDt)
			if len(ds) < 10 {
				continue
			}
			ds = ds[:10]
			if !validDate(ds) {
				continue
			}
			desc := strings.TrimSpace(e.Addtl)
			ref, partner := "", ""
			for _, t := range e.Tx {
				if u := strings.TrimSpace(strings.Join(t.Ustrd, " ")); u != "" {
					desc = strings.TrimSpace(u + " " + desc)
				}
				if t.AddInf != "" && !strings.Contains(desc, t.AddInf) {
					desc = strings.TrimSpace(desc + " " + t.AddInf)
				}
				if ref == "" {
					ref = firstNonEmpty(t.CrRef, t.E2E, t.TxID)
					if ref == "NOTPROVIDED" {
						ref = ""
					}
				}
				if partner == "" {
					if amt.IsPositive() {
						partner = firstNonEmpty(t.Dbtr, t.DbtrP)
					} else {
						partner = firstNonEmpty(t.Cdtr, t.CdtrP)
					}
				}
			}
			out = append(out, stmtFileLine{Date: ds, Amount: amt.String(), Description: desc, Reference: ref, Partner: partner, BankID: e.SvcrRef})
		}
	}
	if len(out) == 0 {
		return nil, cur, badRequest("no_lines")
	}
	return out, cur, nil
}

// parseStatementFile turns a bank file into statement lines with their fingerprints.
func parseStatementFile(format, content string, m csvMap) ([]stmtFileLine, string, []string, error) {
	var lines []stmtFileLine
	var cur string
	var warn []string
	var err error
	switch format {
	case "csv":
		lines, warn, err = parseCSVStatement(content, m)
	case "ofx":
		lines, cur, err = parseOFX(content)
	case "mt940":
		lines, cur, err = parseMT940(content)
	case "camt":
		lines, cur, err = parseCAMT(content)
	default:
		return nil, "", nil, badRequest("bad_format")
	}
	if err != nil {
		return nil, "", nil, err
	}
	if len(lines) > 5000 {
		return nil, "", nil, badRequest("too_many_lines")
	}
	fingerprints(lines)
	return lines, cur, warn, nil
}

// fingerprints: the bank's own id when it gives one, else a hash of date + amount + reference + text and the
// occurrence number (two identical coffees the same day stay two lines; the same file imported twice is caught).
func fingerprints(lines []stmtFileLine) {
	seen := map[string]int{}
	for i := range lines {
		l := &lines[i]
		amt, _ := decimal.NewFromString(l.Amount)
		if l.BankID != "" {
			l.Fingerprint = "id:" + strings.ToLower(strings.TrimSpace(l.BankID))
			if len(l.Fingerprint) > 120 {
				l.Fingerprint = l.Fingerprint[:120]
			}
			continue
		}
		k := strings.Join([]string{l.Date, amt.StringFixed(4), strings.ToLower(strings.TrimSpace(l.Reference)), strings.ToLower(strings.Join(strings.Fields(l.Description), " "))}, "|")
		seen[k]++
		h := sha256.Sum256([]byte(k + "|" + strconv.Itoa(seen[k])))
		l.Fingerprint = "h:" + hex.EncodeToString(h[:16])
	}
}

// POST /bank/statements/parse {format, content, mapping}: preview of a bank file (nothing saved).
func (s *Server) parseStatement(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 12<<20)
	var in struct {
		Format   string `json:"format"`
		Content  string `json:"content"`
		Mapping  csvMap `json:"mapping"`
		MethodID string `json:"method_id"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if len(in.Content) == 0 || len(in.Content) > 10<<20 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	lines, cur, warn, err := parseStatementFile(in.Format, in.Content, in.Mapping)
	if err != nil {
		fail(w, r, err)
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		dups := 0
		if isUUID(in.MethodID) {
			fps := make([]string, len(lines))
			for i, l := range lines {
				fps[i] = l.Fingerprint
			}
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM bank_statement_lines l JOIN bank_statements s ON s.id = l.statement_id
				WHERE s.company_id = $1 AND l.method_id = $2 AND l.fingerprint = ANY ($3::text[])`, cid, in.MethodID, fps).Scan(&dups); err != nil {
				return nil, err
			}
		}
		if warn == nil {
			warn = []string{}
		}
		total := decimal.Zero
		for _, l := range lines {
			a, _ := decimal.NewFromString(l.Amount)
			total = total.Add(a)
		}
		return map[string]any{"lines": lines, "currency": cur, "warnings": warn, "duplicates": dups, "total": total.String()}, nil
	})
}

// ---------------- rules ----------------

const bankRuleCols = `r.id, r.name, r.method_id, m.name AS method, r.field, r.pattern, r.direction, r.amount_min::text AS amount_min,
	r.amount_max::text AS amount_max, r.action, r.account_id, a.code AS account_code, a.name AS account_name, r.customer_id, c.name AS customer_name,
	r.supplier_id, su.name AS supplier_name, r.label, r.priority, r.is_active, r.created_at`
const bankRuleFrom = ` FROM bank_rules r LEFT JOIN payment_methods m ON m.id = r.method_id LEFT JOIN accounts a ON a.id = r.account_id
	LEFT JOIN customers c ON c.id = r.customer_id LEFT JOIN suppliers su ON su.id = r.supplier_id`

func (s *Server) listBankRules(w http.ResponseWriter, r *http.Request) {
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return queryJSON(ctx, tx, `SELECT `+bankRuleCols+bankRuleFrom+` WHERE r.company_id = $1 ORDER BY r.priority, r.name`, r.PathValue("cid"))
	})
}

type bankRuleIn struct {
	Name       *string `json:"name"`
	MethodID   *string `json:"method_id"`
	Field      *string `json:"field"`
	Pattern    *string `json:"pattern"`
	Direction  *string `json:"direction"`
	AmountMin  *string `json:"amount_min"`
	AmountMax  *string `json:"amount_max"`
	Action     *string `json:"action"`
	AccountID  *string `json:"account_id"`
	CustomerID *string `json:"customer_id"`
	SupplierID *string `json:"supplier_id"`
	Label      *string `json:"label"`
	Priority   *int    `json:"priority"`
	IsActive   *bool   `json:"is_active"`
}

func (s *Server) saveBankRule(w http.ResponseWriter, r *http.Request) {
	var in bankRuleIn
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if id != "" && !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	if !optUUID(in.MethodID) || !optUUID(in.AccountID) || !optUUID(in.CustomerID) || !optUUID(in.SupplierID) ||
		(in.Field != nil && !contains([]string{"any", "description", "reference", "partner"}, *in.Field)) ||
		(in.Direction != nil && !contains([]string{"any", "in", "out"}, *in.Direction)) ||
		(in.Action != nil && !contains([]string{"account", "customer", "supplier", "ignore"}, *in.Action)) ||
		len(deref(in.Pattern)) > 200 || len(deref(in.Label)) > 200 || len(deref(in.Name)) > 120 ||
		(in.Priority != nil && (*in.Priority < 0 || *in.Priority > 10000)) {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if p := deref(in.Pattern); p != "" {
		if _, err := regexp.Compile("(?i)" + p); err != nil {
			writeErr(w, http.StatusBadRequest, "bad_pattern")
			return
		}
	}
	for _, a := range []*string{in.AmountMin, in.AmountMax} {
		if a != nil && *a != "" {
			if v, ok := money(*a); !ok || v.IsNegative() {
				writeErr(w, http.StatusBadRequest, "invalid_amount")
				return
			}
		}
	}
	status := http.StatusOK
	if id == "" {
		status = http.StatusCreated
		if strings.TrimSpace(deref(in.Name)) == "" || deref(in.Action) == "" {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	s.run(w, r, status, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if a := deref(in.AccountID); a != "" {
			if err := requirePostable(ctx, tx, cid, a); err != nil {
				return nil, err
			}
			if err := rejectControlAccount(ctx, tx, cid, a); err != nil {
				return nil, err
			}
		}
		if m := deref(in.MethodID); m != "" {
			if _, err := loadBankMethod(ctx, tx, cid, m); err != nil {
				return nil, err
			}
		}
		if id == "" {
			err := tx.QueryRow(ctx, `INSERT INTO bank_rules (company_id, name, method_id, field, pattern, direction, amount_min, amount_max, action,
				account_id, customer_id, supplier_id, label, priority, is_active)
				VALUES ($1, trim($2), nullif($3, '')::uuid, coalesce($4, 'any'), coalesce($5, ''), coalesce($6, 'any'), nullif($7, '')::numeric, nullif($8, '')::numeric,
				$9, nullif($10, '')::uuid, nullif($11, '')::uuid, nullif($12, '')::uuid, coalesce($13, ''), coalesce($14, 100), coalesce($15, true)) RETURNING id::text`,
				cid, deref(in.Name), deref(in.MethodID), in.Field, in.Pattern, in.Direction, deref(in.AmountMin), deref(in.AmountMax), deref(in.Action),
				deref(in.AccountID), deref(in.CustomerID), deref(in.SupplierID), in.Label, in.Priority, in.IsActive).Scan(&id)
			if err != nil {
				return nil, ruleErr(err)
			}
		} else {
			tag, err := tx.Exec(ctx, `UPDATE bank_rules SET name = coalesce(trim($3), name),
				method_id = CASE WHEN $4::boolean THEN nullif($5, '')::uuid ELSE method_id END, field = coalesce($6, field), pattern = coalesce($7, pattern),
				direction = coalesce($8, direction),
				amount_min = CASE WHEN $9::boolean THEN nullif($10, '')::numeric ELSE amount_min END,
				amount_max = CASE WHEN $11::boolean THEN nullif($12, '')::numeric ELSE amount_max END,
				action = coalesce($13, action),
				account_id = CASE WHEN $13 IS NOT NULL OR $14::boolean THEN nullif($15, '')::uuid ELSE account_id END,
				customer_id = CASE WHEN $13 IS NOT NULL OR $16::boolean THEN nullif($17, '')::uuid ELSE customer_id END,
				supplier_id = CASE WHEN $13 IS NOT NULL OR $18::boolean THEN nullif($19, '')::uuid ELSE supplier_id END,
				label = coalesce($20, label), priority = coalesce($21, priority), is_active = coalesce($22, is_active)
				WHERE id = $1 AND company_id = $2`, id, cid, in.Name, in.MethodID != nil, deref(in.MethodID), in.Field, in.Pattern, in.Direction,
				in.AmountMin != nil, deref(in.AmountMin), in.AmountMax != nil, deref(in.AmountMax), in.Action,
				in.AccountID != nil, deref(in.AccountID), in.CustomerID != nil, deref(in.CustomerID), in.SupplierID != nil, deref(in.SupplierID),
				in.Label, in.Priority, in.IsActive)
			if err != nil {
				return nil, ruleErr(err)
			}
			if tag.RowsAffected() == 0 {
				return nil, notFound()
			}
		}
		return queryJSONOne(ctx, tx, `SELECT `+bankRuleCols+bankRuleFrom+` WHERE r.id = $1`, id)
	})
}

func ruleErr(err error) error {
	var pe interface{ SQLState() string }
	if errors.As(err, &pe) && (pe.SQLState() == "23514" || pe.SQLState() == "23503") {
		return badRequest("rule_target_required")
	}
	return err
}

func (s *Server) deleteBankRule(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		if _, err := tx.Exec(ctx, `UPDATE bank_statement_lines SET rule_id = NULL WHERE rule_id = $1`, id); err != nil {
			return nil, err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM bank_rules WHERE id = $1 AND company_id = $2`, id, cid)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, notFound()
		}
		return map[string]string{"deleted": id}, nil
	})
}

type bankRule struct {
	ID, Name, Field, Direction, Action, Label string
	Method, Account, Customer, Supplier       *string
	Min, Max                                  *decimal.Decimal
	re                                        *regexp.Regexp
}

func loadBankRules(ctx context.Context, tx pgx.Tx, cid string) ([]bankRule, error) {
	rows, err := tx.Query(ctx, `SELECT id::text, name, field, direction, action, label, pattern, method_id::text, account_id::text, customer_id::text,
		supplier_id::text, amount_min::text, amount_max::text FROM bank_rules WHERE company_id = $1 AND is_active ORDER BY priority, created_at`, cid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []bankRule
	for rows.Next() {
		var b bankRule
		var pat string
		var mn, mx *string
		if err := rows.Scan(&b.ID, &b.Name, &b.Field, &b.Direction, &b.Action, &b.Label, &pat, &b.Method, &b.Account, &b.Customer, &b.Supplier, &mn, &mx); err != nil {
			return nil, err
		}
		if pat != "" {
			re, err := regexp.Compile("(?i)" + pat)
			if err != nil {
				continue
			}
			b.re = re
		}
		if mn != nil {
			d, _ := decimal.NewFromString(*mn)
			b.Min = &d
		}
		if mx != nil {
			d, _ := decimal.NewFromString(*mx)
			b.Max = &d
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (b *bankRule) matches(method, desc, ref, partner string, amount decimal.Decimal) bool {
	if b.Method != nil && *b.Method != method {
		return false
	}
	if (b.Direction == "in" && !amount.IsPositive()) || (b.Direction == "out" && !amount.IsNegative()) {
		return false
	}
	a := amount.Abs()
	if (b.Min != nil && a.LessThan(*b.Min)) || (b.Max != nil && a.GreaterThan(*b.Max)) {
		return false
	}
	if b.re == nil {
		return true
	}
	switch b.Field {
	case "description":
		return b.re.MatchString(desc)
	case "reference":
		return b.re.MatchString(ref)
	case "partner":
		return b.re.MatchString(partner)
	}
	return b.re.MatchString(desc + " " + ref + " " + partner)
}

// ---------------- suggestions ----------------

type docSuggestion struct {
	Kind       string   `json:"kind"` // invoice | bill
	ID         string   `json:"id"`
	Number     int64    `json:"number"`
	PartyID    string   `json:"party_id"`
	Party      string   `json:"party"`
	Date       string   `json:"date"`
	Currency   string   `json:"currency"`
	Total      string   `json:"total"`
	Balance    string   `json:"balance"`
	Confidence int      `json:"confidence"`
	Why        []string `json:"why"`
}

var wordRe = regexp.MustCompile(`[\p{L}\p{N}]+`)

// docSuggestions: open invoices (money in) or bills (money out) that may explain a bank line, best first.
func docSuggestions(ctx context.Context, tx pgx.Tx, cid string, amount decimal.Decimal, cur, date, desc, ref, partner string) ([]docSuggestion, error) {
	text := strings.ToLower(desc + " " + ref + " " + partner)
	words := map[string]bool{}
	for _, w := range wordRe.FindAllString(text, -1) {
		words[w] = true
	}
	a := amount.Abs()
	var q string
	if amount.IsPositive() {
		q = `SELECT 'invoice', i.id::text, i.number, i.customer_id::text, c.name || ' ' || c.code, i.issue_date::text, i.currency::text, i.total::text, (i.total - i.paid)::text
			FROM invoices i JOIN customers c ON c.id = i.customer_id
			WHERE i.company_id = $1 AND i.status IN ('open', 'partial') AND i.total > i.paid
			  AND (i.currency = $2 OR $3) ORDER BY abs((i.total - i.paid) - $4::numeric), i.issue_date LIMIT 400`
	} else {
		q = `SELECT 'bill', b.id::text, b.number, b.supplier_id::text, s.name, b.bill_date::text, b.currency::text, b.total::text, (b.total - b.paid)::text
			FROM bills b JOIN suppliers s ON s.id = b.supplier_id
			WHERE b.company_id = $1 AND b.status IN ('open', 'partial') AND b.total > b.paid
			  AND (b.currency = $2 OR $3) ORDER BY abs((b.total - b.paid) - $4::numeric), b.bill_date LIMIT 400`
	}
	rows, err := tx.Query(ctx, q, cid, cur, false, a.String())
	if err != nil {
		return nil, err
	}
	var out []docSuggestion
	for rows.Next() {
		var d docSuggestion
		var party string
		if err := rows.Scan(&d.Kind, &d.ID, &d.Number, &d.PartyID, &party, &d.Date, &d.Currency, &d.Total, &d.Balance); err != nil {
			rows.Close()
			return nil, err
		}
		bal, _ := decimal.NewFromString(d.Balance)
		tot, _ := decimal.NewFromString(d.Total)
		score := 0
		d.Why = []string{}
		switch {
		case bal.Equal(a):
			score += 50
			d.Why = append(d.Why, "amount")
		case tot.Equal(a):
			score += 40
			d.Why = append(d.Why, "total")
		case bal.IsPositive() && bal.Sub(a).Abs().LessThanOrEqual(bal.Mul(decimal.NewFromFloat(0.01))):
			score += 20
			d.Why = append(d.Why, "amount_close")
		}
		num := strconv.FormatInt(d.Number, 10)
		if words[num] || strings.Contains(text, "inv-"+num) || strings.Contains(text, "#"+num) || strings.Contains(text, "inv"+num) || strings.Contains(text, "bill-"+num) {
			score += 35
			d.Why = append(d.Why, "number")
		}
		pw := wordRe.FindAllString(strings.ToLower(party), -1)
		hits := 0
		for _, w := range pw {
			if len([]rune(w)) >= 3 && words[w] {
				hits++
			}
		}
		if hits > 0 {
			score += 15 + 10*min(hits, 2)
			d.Why = append(d.Why, "name")
		}
		if dt, err := time.Parse(time.DateOnly, d.Date[:10]); err == nil {
			if ld, err := time.Parse(time.DateOnly, date); err == nil && !ld.Before(dt) && ld.Sub(dt) < 120*24*time.Hour {
				score += 5
			}
		}
		d.Party = strings.TrimSpace(party)
		if score > 100 {
			score = 100
		}
		d.Confidence = score
		if score >= 30 {
			out = append(out, d)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Confidence > out[j].Confidence })
	if len(out) > 5 {
		out = out[:5]
	}
	return out, nil
}

type bankLineRow struct {
	ID, Statement, Status, Method, Date, Desc, Ref, Partner string
	Amount                                                  decimal.Decimal
}

func lockBankLineRow(ctx context.Context, tx pgx.Tx, cid, id string) (*bankLineRow, *method, error) {
	if !isUUID(id) {
		return nil, nil, notFound()
	}
	var b bankLineRow
	var amt string
	err := tx.QueryRow(ctx, `SELECT l.id::text, l.statement_id::text, l.status, l.method_id::text, l.line_date::text, l.description, l.reference, l.partner, l.amount::text
		FROM bank_statement_lines l JOIN bank_statements s ON s.id = l.statement_id WHERE l.id = $1 AND s.company_id = $2 FOR UPDATE OF l`, id, cid).
		Scan(&b.ID, &b.Statement, &b.Status, &b.Method, &b.Date, &b.Desc, &b.Ref, &b.Partner, &amt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, notFound()
	}
	if err != nil {
		return nil, nil, err
	}
	b.Amount, _ = decimal.NewFromString(amt)
	m, err := loadBankMethod(ctx, tx, cid, b.Method)
	return &b, m, err
}

// GET /bank/lines/{id}/suggestions: journal lines that match exactly, and open invoices / bills with a confidence.
func (s *Server) bankLineSuggestions(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		b, m, err := lockBankLineRow(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		docs, err := docSuggestions(ctx, tx, cid, b.Amount, m.Currency, b.Date, b.Desc, b.Ref, b.Partner)
		if err != nil {
			return nil, err
		}
		rules, err := loadBankRules(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		var rule any
		for _, ru := range rules {
			if ru.matches(b.Method, b.Desc, b.Ref, b.Partner, b.Amount) {
				rule = map[string]string{"id": ru.ID, "name": ru.Name, "action": ru.Action}
				break
			}
		}
		return map[string]any{"documents": docs, "rule": rule}, nil
	})
}

// matchLineToEntry matches the statement line to the bank side of a journal entry just posted.
func matchLineToEntry(ctx context.Context, tx pgx.Tx, b *bankLineRow, m *method, je string, set string, setVal *string, rule *string) error {
	var jl string
	if err := tx.QueryRow(ctx, `SELECT jl.id::text FROM journal_lines jl WHERE jl.entry_id = $1 AND jl.account_id = $2 AND jl.currency = $3
		AND (jl.debit - jl.credit) = $4::numeric AND NOT EXISTS (SELECT 1 FROM bank_statement_lines x WHERE x.journal_line_id = jl.id) LIMIT 1`,
		je, m.Account, m.Currency, b.Amount.String()).Scan(&jl); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return &apiError{status: http.StatusConflict, code: "match_mismatch"}
		}
		return err
	}
	q := `UPDATE bank_statement_lines SET journal_line_id = $2, status = 'matched', matched_by = app_user(), matched_at = now(), rule_id = $3::uuid`
	args := []any{b.ID, jl, rule}
	if set != "" {
		q += ", " + set + " = $4::uuid"
		args = append(args, setVal)
	}
	_, err := tx.Exec(ctx, q+" WHERE id = $1", args...)
	return err
}

// reconcileLine books the money of a bank line against an invoice / bill / customer / supplier / account and matches it.
func reconcileLine(ctx context.Context, tx pgx.Tx, cid string, b *bankLineRow, m *method, kind, target, label string, rule *string) error {
	if b.Status != "unmatched" {
		return &apiError{status: http.StatusConflict, code: "wrong_status"}
	}
	abs := b.Amount.Abs()
	desc := strings.TrimSpace(firstNonEmpty(label, b.Desc, b.Ref, "Bank"))
	if len([]rune(desc)) > 200 {
		desc = string([]rune(desc)[:200])
	}
	switch kind {
	case "invoice", "customer":
		if !b.Amount.IsPositive() {
			return badRequest("wrong_direction")
		}
		customer, cur, inv := target, m.Currency, ""
		if kind == "invoice" {
			if err := tx.QueryRow(ctx, `SELECT customer_id::text, currency::text FROM invoices WHERE id = $1 AND company_id = $2 AND status IN ('open', 'partial')`,
				target, cid).Scan(&customer, &cur); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return badRequest("invalid_invoice")
				}
				return err
			}
			inv = target
		}
		pid, err := receivePayment(quietCtx(ctx), tx, cid, paymentIn{CustomerID: customer, Date: b.Date, Currency: cur, InvoiceID: inv,
			Tenders: []tenderIn{{MethodID: m.ID, Amount: abs.String()}}, Notes: "Bank آ· " + desc}, false, "")
		if err != nil {
			return err
		}
		var je string
		if err := tx.QueryRow(ctx, `SELECT journal_entry_id::text FROM payments WHERE id = $1`, pid).Scan(&je); err != nil {
			return err
		}
		return matchLineToEntry(ctx, tx, b, m, je, "payment_id", &pid, rule)
	case "bill", "supplier":
		if !b.Amount.IsNegative() {
			return badRequest("wrong_direction")
		}
		supplier, cur := target, m.Currency
		if kind == "bill" {
			if err := tx.QueryRow(ctx, `SELECT supplier_id::text, currency::text FROM bills WHERE id = $1 AND company_id = $2 AND status IN ('open', 'partial')`,
				target, cid).Scan(&supplier, &cur); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return badRequest("invalid_bill")
				}
				return err
			}
		}
		pid, err := paySupplier(ctx, tx, cid, supPayIn{SupplierID: supplier, Date: b.Date, Currency: cur, MethodID: m.ID, PaidAmount: abs.String(),
			Notes: "Bank آ· " + desc})
		if err != nil {
			return err
		}
		var je string
		if err := tx.QueryRow(ctx, `SELECT journal_entry_id::text FROM supplier_payments WHERE id = $1`, pid).Scan(&je); err != nil {
			return err
		}
		return matchLineToEntry(ctx, tx, b, m, je, "supplier_payment_id", &pid, rule)
	case "account":
		if err := requirePostable(ctx, tx, cid, target); err != nil {
			return err
		}
		if err := rejectControlAccount(ctx, tx, cid, target); err != nil {
			return err
		}
		if target == m.Account {
			return badRequest("invalid_account")
		}
		set, err := loadBillingSettings(ctx, tx, cid)
		if err != nil {
			return err
		}
		rate, err := resolveRate(ctx, tx, cid, set.Base, m.Currency, b.Date, "")
		if err != nil {
			return err
		}
		bank := jline{Account: m.Account, Desc: desc, Currency: m.Currency, Rate: rate}
		other := jline{Account: target, Desc: desc, Currency: m.Currency, Rate: rate}
		if b.Amount.IsPositive() {
			bank.Debit, other.Credit = abs, abs
		} else {
			bank.Credit, other.Debit = abs, abs
		}
		je, err := postJournal(ctx, tx, cid, set, b.Date, "Bank آ· "+desc, "BANK", "bank", []jline{bank, other})
		if err != nil {
			return err
		}
		if err := matchLineToEntry(ctx, tx, b, m, je, "", nil, rule); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE bank_statement_lines SET created_entry_id = $2 WHERE id = $1`, b.ID, je)
		return err
	case "ignore":
		_, err := tx.Exec(ctx, `UPDATE bank_statement_lines SET status = 'ignored', rule_id = $2::uuid WHERE id = $1`, b.ID, rule)
		return err
	}
	return badRequest("bad_request")
}

// POST /bank/lines/{id}/reconcile {kind: invoice|bill|customer|supplier|account, id, label}
func (s *Server) reconcileBankLine(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kind  string `json:"kind"`
		ID    string `json:"id"`
		Label string `json:"label"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if !contains([]string{"invoice", "bill", "customer", "supplier", "account"}, in.Kind) || !isUUID(in.ID) || len(in.Label) > 200 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	if (in.Kind == "invoice" || in.Kind == "customer") && !can(r, "collections.create") || (in.Kind == "bill" || in.Kind == "supplier") && !can(r, "purchasing.create") {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		b, m, err := lockBankLineRow(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		if err := reconcileLine(ctx, tx, cid, b, m, in.Kind, in.ID, in.Label, nil); err != nil {
			return nil, err
		}
		return loadStatement(ctx, tx, cid, b.Statement)
	})
}

// POST /bank/statements/{id}/auto-reconcile {min_confidence}: exact journal matches, then the rules, then invoices /
// bills the line points to without doubt (one suggestion at or above the confidence, default 85).
func (s *Server) autoReconcileStatement(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MinConfidence int  `json:"min_confidence"`
		RulesOnly     bool `json:"rules_only"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.MinConfidence == 0 {
		in.MinConfidence = 85
	}
	if in.MinConfidence < 50 || in.MinConfidence > 100 {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	payIn, payOut := can(r, "collections.create"), can(r, "purchasing.create")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM bank_statements WHERE id = $1 AND company_id = $2)`, id, cid).Scan(&ok); err != nil {
			return nil, err
		}
		if !ok {
			return nil, notFound()
		}
		res := map[string]int{"matched": 0, "rules": 0, "documents": 0, "left": 0}
		// 1. exact journal lines (same as auto-match)
		rows, err := tx.Query(ctx, `SELECT l.id::text, (SELECT array_agg(c.id::text) FROM (`+candidatesSQL+`) c)
			FROM bank_statement_lines l JOIN payment_methods m ON m.id = l.method_id
			WHERE l.statement_id = $1 AND l.status = 'unmatched' ORDER BY l.line_date`, id)
		if err != nil {
			return nil, err
		}
		type cand struct {
			line string
			ids  []string
		}
		list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (cand, error) {
			var c cand
			err := row.Scan(&c.line, &c.ids)
			return c, err
		})
		if err != nil {
			return nil, err
		}
		used := map[string]bool{}
		if !in.RulesOnly {
			for _, c := range list {
				if len(c.ids) != 1 || used[c.ids[0]] {
					continue
				}
				used[c.ids[0]] = true
				if _, err := tx.Exec(ctx, `UPDATE bank_statement_lines SET journal_line_id = $2, status = 'matched', matched_by = app_user(), matched_at = now()
					WHERE id = $1 AND status = 'unmatched'`, c.line, c.ids[0]); err != nil {
					return nil, err
				}
				res["matched"]++
			}
		}
		rules, err := loadBankRules(ctx, tx, cid)
		if err != nil {
			return nil, err
		}
		lrows, err := tx.Query(ctx, `SELECT id::text FROM bank_statement_lines WHERE statement_id = $1 AND status = 'unmatched' ORDER BY line_date, id`, id)
		if err != nil {
			return nil, err
		}
		ids, err := pgx.CollectRows(lrows, pgx.RowTo[string])
		if err != nil {
			return nil, err
		}
		usedDoc := map[string]bool{}
		for _, lid := range ids {
			b, m, err := lockBankLineRow(ctx, tx, cid, lid)
			if err != nil {
				return nil, err
			}
			done := false
			// 2. rules (first match wins)
			for _, ru := range rules {
				if !ru.matches(b.Method, b.Desc, b.Ref, b.Partner, b.Amount) {
					continue
				}
				kind, target := ru.Action, ""
				switch ru.Action {
				case "account":
					target = deref(ru.Account)
				case "customer":
					target = deref(ru.Customer)
					if !payIn || !b.Amount.IsPositive() {
						continue
					}
				case "supplier":
					target = deref(ru.Supplier)
					if !payOut || !b.Amount.IsNegative() {
						continue
					}
				}
				rid := ru.ID
				if err := withSavepoint(ctx, tx, func() error { return reconcileLine(ctx, tx, cid, b, m, kind, target, ru.Label, &rid) }); err != nil {
					var ae *apiError
					if errors.As(err, &ae) {
						continue // this rule cannot book this line (closed periodâ€¦): try the next one
					}
					return nil, err
				}
				res["rules"]++
				done = true
				break
			}
			if done || in.RulesOnly {
				if !done {
					res["left"]++
				}
				continue
			}
			// 3. a document the line points to without doubt
			if (b.Amount.IsPositive() && payIn) || (b.Amount.IsNegative() && payOut) {
				docs, err := docSuggestions(ctx, tx, cid, b.Amount, m.Currency, b.Date, b.Desc, b.Ref, b.Partner)
				if err != nil {
					return nil, err
				}
				if len(docs) > 0 && docs[0].Confidence >= in.MinConfidence && !usedDoc[docs[0].ID] &&
					(len(docs) == 1 || docs[1].Confidence < docs[0].Confidence) {
					d := docs[0]
					err := withSavepoint(ctx, tx, func() error { return reconcileLine(ctx, tx, cid, b, m, d.Kind, d.ID, "", nil) })
					if err == nil {
						usedDoc[d.ID] = true
						res["documents"]++
						continue
					}
					var ae *apiError
					if !errors.As(err, &ae) {
						return nil, err
					}
				}
			}
			res["left"]++
		}
		st, err := loadStatement(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"result": res, "statement": st}, nil
	})
}

// withSavepoint runs fn in a savepoint: an error rolls back only what fn did.
func withSavepoint(ctx context.Context, tx pgx.Tx, fn func() error) error {
	if _, err := tx.Exec(ctx, `SAVEPOINT p9`); err != nil {
		return err
	}
	if err := fn(); err != nil {
		if _, e2 := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT p9`); e2 != nil {
			return e2
		}
		return err
	}
	_, err := tx.Exec(ctx, `RELEASE SAVEPOINT p9`)
	return err
}
