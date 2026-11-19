package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// E-invoicing.
//
// ZATCA (Saudi Arabia) phase 2: each company onboards one e-invoicing unit: a secp256k1 key and a CSR with ZATCA's
// extensions are made here, the OTP from the FATOORA portal buys a compliance certificate (CSID), then a production
// CSID. Every invoice becomes a UBL 2.1 XML with an invoice counter (ICV), the previous invoice hash (PIH), its own
// SHA-256 hash, an ECDSA signature (XAdES) and the phase-2 QR (tags 1–9). Simplified (B2C) invoices are reported,
// standard (B2B, buyer with a VAT number) invoices are cleared, through the ZATCA API of the chosen environment
// (sandbox = developer portal, simulation, production).
//
// ETA (Egypt): the invoice becomes the ETA document JSON v1.0; it is serialized with ETA's canonical algorithm and
// signed (CAdES-BES) by the taxpayer's USB token through a signing service the company runs (signer_url); the signed
// document is submitted with the company's client id / secret (pre-production or production).

var zatcaBase = map[string]string{
	"sandbox":    "https://gw-fatoora.zatca.gov.sa/e-invoicing/developer-portal",
	"simulation": "https://gw-fatoora.zatca.gov.sa/e-invoicing/simulation",
	"production": "https://gw-fatoora.zatca.gov.sa/e-invoicing/core",
}

var etaBase = map[string][2]string{ // identity, api
	"preprod":    {"https://id.preprod.eta.gov.eg", "https://api.preprod.invoicing.eta.gov.eg"},
	"production": {"https://id.eta.gov.eg", "https://api.invoicing.eta.gov.eg"},
}

var einvSecretKeys = map[string]bool{"private_key": true, "csid_secret": true, "pcsid_secret": true, "client_secret": true, "signer_token": true}

var einvPublicKeys = map[string][]string{
	"zatca": {"seller_name", "vat_number", "crn", "street", "building", "district", "city", "postal_code", "branch", "egs_serial", "business_category"},
	"eta":   {"client_id", "issuer_name", "issuer_id", "issuer_type", "branch_id", "country", "governate", "region_city", "street", "building", "activity_code", "signer_url"},
}

func loadEinvConfig(ctx context.Context, tx pgx.Tx, cid, kind string) (env string, cfg map[string]any, counter int64, last string, err error) {
	err = tx.QueryRow(ctx, `SELECT env, config, counter, last_hash FROM einvoice_config WHERE company_id = $1 AND kind = $2`, cid, kind).Scan(&env, &cfg, &counter, &last)
	if errors.Is(err, pgx.ErrNoRows) {
		return map[string]string{"zatca": "sandbox", "eta": "preprod"}[kind], map[string]any{}, 0, "", nil
	}
	return
}

// GET /companies/{cid}/einvoice/config?kind=zatca|eta (secrets hidden)
func (s *Server) getEinvConfig(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	if kind != "zatca" && kind != "eta" {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		env, cfg, counter, last, err := loadEinvConfig(ctx, tx, r.PathValue("cid"), kind)
		if err != nil {
			return nil, err
		}
		pub := map[string]any{}
		for k, v := range cfg {
			if einvSecretKeys[k] {
				pub["has_"+k] = v != nil && v != ""
			} else {
				pub[k] = v
			}
		}
		return map[string]any{"kind": kind, "env": env, "config": pub, "counter": counter, "last_hash": last}, nil
	})
}

// PUT /companies/{cid}/einvoice/config {kind, env, config: {...}, client_secret?, signer_token?}
func (s *Server) putEinvConfig(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kind   string            `json:"kind"`
		Env    string            `json:"env"`
		Config map[string]string `json:"config"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	keys, ok := einvPublicKeys[in.Kind]
	envOK := (in.Kind == "zatca" && zatcaBase[in.Env] != "") || (in.Kind == "eta" && etaBase[in.Env][0] != "")
	if !ok || !envOK {
		writeErr(w, http.StatusBadRequest, "bad_request")
		return
	}
	allowed := map[string]bool{"client_secret": in.Kind == "eta", "signer_token": in.Kind == "eta"}
	for _, k := range keys {
		allowed[k] = true
	}
	for k, v := range in.Config {
		if !allowed[k] || len(v) > 500 {
			writeErr(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	if u := in.Config["signer_url"]; u != "" {
		if p, err := url.Parse(u); err != nil || (p.Scheme != "https" && p.Scheme != "http") {
			writeErr(w, http.StatusBadRequest, "invalid_url")
			return
		}
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		_, cfg, _, _, err := loadEinvConfig(ctx, tx, cid, in.Kind)
		if err != nil {
			return nil, err
		}
		for k, v := range in.Config {
			v = strings.TrimSpace(v)
			if einvSecretKeys[k] {
				if v == "" {
					continue
				}
				if v, err = sealSecret(v); err != nil {
					return nil, err
				}
			}
			cfg[k] = v
		}
		b, _ := json.Marshal(cfg)
		if _, err := tx.Exec(ctx, `INSERT INTO einvoice_config (company_id, kind, env, config) VALUES ($1, $2, $3, $4)
			ON CONFLICT (company_id, kind) DO UPDATE SET env = excluded.env, config = excluded.config, updated_at = now()`, cid, in.Kind, in.Env, b); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE companies SET einvoice = $2 WHERE id = $1`, cid, in.Kind); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	})
}

func saveEinvConfig(ctx context.Context, tx pgx.Tx, cid, kind string, cfg map[string]any) error {
	b, _ := json.Marshal(cfg)
	_, err := tx.Exec(ctx, `INSERT INTO einvoice_config (company_id, kind, config) VALUES ($1, $2, $3)
		ON CONFLICT (company_id, kind) DO UPDATE SET config = excluded.config, updated_at = now()`, cid, kind, b)
	return err
}

// ================= ZATCA =================

var (
	oidTemplateName = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 311, 20, 2}
	oidSAN          = asn1.ObjectIdentifier{2, 5, 29, 17}
	oidExtReq       = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 14}
	oidSN           = asn1.ObjectIdentifier{2, 5, 4, 4}
	oidUID          = asn1.ObjectIdentifier{0, 9, 2342, 19200300, 100, 1, 1}
	oidTitle        = asn1.ObjectIdentifier{2, 5, 4, 12}
	oidRegAddr      = asn1.ObjectIdentifier{2, 5, 4, 26}
	oidBizCat       = asn1.ObjectIdentifier{2, 5, 4, 15}
)

type pkixExt struct {
	ID       asn1.ObjectIdentifier
	Critical bool `asn1:"optional"`
	Value    []byte
}

func rdn(oid asn1.ObjectIdentifier, v string) pkix.RelativeDistinguishedNameSET {
	return pkix.RelativeDistinguishedNameSET{{Type: oid, Value: v}}
}

// zatcaCSR builds the PKCS#10 request ZATCA expects (subject C/OU/O/CN, template name and the EGS details in the SAN).
func zatcaCSR(key *k1Key, env string, cfg map[string]any) ([]byte, error) {
	g := func(k string) string { return strings.TrimSpace(str2(cfg[k])) }
	org, vat := g("seller_name"), g("vat_number")
	if org == "" || len(vat) != 15 {
		return nil, badRequest("einvoice_seller_missing")
	}
	serial := g("egs_serial")
	if serial == "" {
		serial = "1-Paxve|2-Web|3-" + randHexStr(8)
	}
	branch := defaultStr(g("branch"), "Main")
	tmpl := map[string]string{"sandbox": "TSTZATCA-Code-Signing", "simulation": "PREZATCA-Code-Signing", "production": "ZATCA-Code-Signing"}[env]
	subject := pkix.RDNSequence{rdn(asn1.ObjectIdentifier{2, 5, 4, 6}, "SA"), rdn(asn1.ObjectIdentifier{2, 5, 4, 11}, branch),
		rdn(asn1.ObjectIdentifier{2, 5, 4, 10}, org), rdn(asn1.ObjectIdentifier{2, 5, 4, 3}, "EGS-"+vat)}
	subjDER, err := asn1.Marshal(subject)
	if err != nil {
		return nil, err
	}
	tmplVal, _ := asn1.MarshalWithParams(tmpl, "printable")
	dir := pkix.RDNSequence{rdn(oidSN, serial), rdn(oidUID, vat), rdn(oidTitle, "1100"),
		rdn(oidRegAddr, defaultStr(g("street"), g("city"))), rdn(oidBizCat, defaultStr(g("business_category"), "Utilities"))}
	dirDER, err := asn1.Marshal(dir)
	if err != nil {
		return nil, err
	}
	gn, _ := asn1.Marshal(asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 4, IsCompound: true, Bytes: dirDER})
	sanVal, _ := asn1.Marshal(asn1.RawValue{Class: asn1.ClassUniversal, Tag: asn1.TagSequence, IsCompound: true, Bytes: gn})
	exts, err := asn1.Marshal([]pkixExt{{ID: oidTemplateName, Value: tmplVal}, {ID: oidSAN, Value: sanVal}})
	if err != nil {
		return nil, err
	}
	extSet, _ := asn1.Marshal(asn1.RawValue{Class: asn1.ClassUniversal, Tag: asn1.TagSet, IsCompound: true, Bytes: exts})
	oidB, _ := asn1.Marshal(oidExtReq)
	attr, _ := asn1.Marshal(asn1.RawValue{Class: asn1.ClassUniversal, Tag: asn1.TagSequence, IsCompound: true, Bytes: append(oidB, extSet...)})
	attrs, _ := asn1.Marshal(asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: attr})
	pk, err := key.spkiDER()
	if err != nil {
		return nil, err
	}
	ver, _ := asn1.Marshal(0)
	info, _ := asn1.Marshal(asn1.RawValue{Class: asn1.ClassUniversal, Tag: asn1.TagSequence, IsCompound: true,
		Bytes: bytes.Join([][]byte{ver, subjDER, pk, attrs}, nil)})
	h := sha256.Sum256(info)
	sig, err := key.sign(h[:])
	if err != nil {
		return nil, err
	}
	alg, _ := asn1.Marshal(algID{Algorithm: oidECDSASHA256})
	sigBits, _ := asn1.Marshal(asn1.BitString{Bytes: sig, BitLength: 8 * len(sig)})
	return asn1.Marshal(asn1.RawValue{Class: asn1.ClassUniversal, Tag: asn1.TagSequence, IsCompound: true, Bytes: bytes.Join([][]byte{info, alg, sigBits}, nil)})
}

func zatcaKey(cfg map[string]any) (*k1Key, error) {
	d, ok := new(big.Int).SetString(openSecret(str2(cfg["private_key"])), 16)
	if !ok {
		return nil, notSetup("einvoice_key")
	}
	return k1FromD(d)
}

func zatcaCall(ctx context.Context, env, path string, user, pass, otp string, body any, extra map[string]string) (int, map[string]any, error) {
	b, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, zatcaBase[env]+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Version", "V2")
	req.Header.Set("Accept-Language", "en")
	if otp != "" {
		req.Header.Set("OTP", otp)
	}
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	if len(out) == 0 && len(raw) > 0 {
		out["raw"] = string(raw)
	}
	return res.StatusCode, out, nil
}

// POST /companies/{cid}/einvoice/zatca/onboard {otp}: key + CSR → compliance CSID (→ production CSID when compliance
// checks pass: POST again with {production: true}).
func (s *Server) zatcaOnboard(w http.ResponseWriter, r *http.Request) {
	var in struct {
		OTP        string `json:"otp"`
		Production bool   `json:"production"`
		NewKey     bool   `json:"new_key"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid := r.PathValue("cid")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		env, cfg, _, _, err := loadEinvConfig(ctx, tx, cid, "zatca")
		if err != nil {
			return nil, err
		}
		if in.Production {
			req := str2(cfg["compliance_request_id"])
			if req == "" {
				return nil, notSetup("einvoice_csid")
			}
			st, res, err := zatcaCall(ctx, env, "/production/csids", str2(cfg["csid"]), openSecret(str2(cfg["csid_secret"])), "",
				map[string]string{"compliance_request_id": req}, nil)
			if err != nil {
				return nil, &apiError{status: http.StatusBadGateway, code: "einvoice_unreachable", detail: err.Error()}
			}
			if st >= 300 {
				return map[string]any{"ok": false, "status": st, "response": res}, nil
			}
			cfg["pcsid"] = str2(res["binarySecurityToken"])
			if cfg["pcsid_secret"], err = sealSecret(str2(res["secret"])); err != nil {
				return nil, err
			}
			return map[string]any{"ok": true, "status": st, "stage": "production"}, saveEinvConfig(ctx, tx, cid, "zatca", cfg)
		}
		key, err := zatcaKey(cfg)
		if err != nil || in.NewKey {
			if key, err = k1Generate(); err != nil {
				return nil, err
			}
			if cfg["private_key"], err = sealSecret(hex.EncodeToString(pad32(key.d.Bytes()))); err != nil {
				return nil, err
			}
		}
		csr, err := zatcaCSR(key, env, cfg)
		if err != nil {
			return nil, err
		}
		pemCSR := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr})
		cfg["csr"] = string(pemCSR)
		if err := saveEinvConfig(ctx, tx, cid, "zatca", cfg); err != nil {
			return nil, err
		}
		if strings.TrimSpace(in.OTP) == "" {
			return map[string]any{"ok": true, "stage": "csr", "csr": string(pemCSR)}, nil
		}
		st, res, err := zatcaCall(ctx, env, "/compliance", "", "", strings.TrimSpace(in.OTP),
			map[string]string{"csr": base64.StdEncoding.EncodeToString(pemCSR)}, nil)
		if err != nil {
			return nil, &apiError{status: http.StatusBadGateway, code: "einvoice_unreachable", detail: err.Error()}
		}
		if st >= 300 {
			return map[string]any{"ok": false, "status": st, "response": res, "csr": string(pemCSR)}, nil
		}
		cfg["csid"] = str2(res["binarySecurityToken"])
		cfg["compliance_request_id"] = fmt.Sprint(res["requestID"])
		if f, ok := res["requestID"].(float64); ok {
			cfg["compliance_request_id"] = fmt.Sprintf("%.0f", f)
		}
		if cfg["csid_secret"], err = sealSecret(str2(res["secret"])); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "status": st, "stage": "compliance", "request_id": cfg["compliance_request_id"]}, saveEinvConfig(ctx, tx, cid, "zatca", cfg)
	})
}

func xmlEsc(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "\r", "&#xD;")
	return r.Replace(s)
}

type einvInvoice struct {
	ID, Number, Date, Time, Currency, Customer, CustomerVAT, CustomerCity string
	Total, Tax, Net                                                       decimal.Decimal
	Lines                                                                 []einvLine
	Standard                                                              bool
}

type einvLine struct {
	Desc            string
	Qty, Price, Net decimal.Decimal
	Rate, Tax       decimal.Decimal
}

func loadEinvInvoice(ctx context.Context, tx pgx.Tx, cid, id string) (*einvInvoice, error) {
	if !isUUID(id) {
		return nil, notFound()
	}
	x := &einvInvoice{ID: id}
	var total, ts, status string
	var created time.Time
	err := tx.QueryRow(ctx, `SELECT i.number::text, i.issue_date::text, i.created_at, i.currency, c.name, coalesce(to_jsonb(c) ->> 'tax_id', ''),
		coalesce(to_jsonb(c) ->> 'address', ''), i.total::text, coalesce((SELECT sum(l.tax_amount) FROM invoice_lines l WHERE l.invoice_id = i.id), 0)::text, i.status
		FROM invoices i JOIN customers c ON c.id = i.customer_id WHERE i.id = $1 AND i.company_id = $2`, id, cid).
		Scan(&x.Number, &x.Date, &created, &x.Currency, &x.Customer, &x.CustomerVAT, &x.CustomerCity, &total, &ts, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFound()
	}
	if err != nil {
		return nil, err
	}
	if status == "void" {
		return nil, &apiError{status: http.StatusConflict, code: "wrong_status"}
	}
	x.Time = created.UTC().Format("15:04:05")
	x.Total, _ = decimal.NewFromString(total)
	x.Tax, _ = decimal.NewFromString(ts)
	x.Net = x.Total.Sub(x.Tax)
	x.Standard = len(strings.TrimSpace(x.CustomerVAT)) == 15
	rows, err := tx.Query(ctx, `SELECT l.description, l.quantity::text, l.unit_price::text, l.amount::text, coalesce(t.rate, 0)::text, coalesce(l.tax_amount, 0)::text
		FROM invoice_lines l LEFT JOIN tax_rates t ON t.id = l.tax_rate_id WHERE l.invoice_id = $1 ORDER BY l.line_no`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var l einvLine
		var q, p, n, rt, tx2 string
		if err := rows.Scan(&l.Desc, &q, &p, &n, &rt, &tx2); err != nil {
			return nil, err
		}
		l.Qty, _ = decimal.NewFromString(q)
		l.Price, _ = decimal.NewFromString(p)
		l.Net, _ = decimal.NewFromString(n)
		l.Rate, _ = decimal.NewFromString(rt)
		l.Tax, _ = decimal.NewFromString(tx2)
		x.Lines = append(x.Lines, l)
	}
	return x, rows.Err()
}

const ublNS = `xmlns="urn:oasis:names:specification:ubl:schema:xsd:Invoice-2" xmlns:cac="urn:oasis:names:specification:ubl:schema:xsd:CommonAggregateComponents-2" xmlns:cbc="urn:oasis:names:specification:ubl:schema:xsd:CommonBasicComponents-2" xmlns:ext="urn:oasis:names:specification:ubl:schema:xsd:CommonExtensionComponents-2"`

func m2(d decimal.Decimal) string { return d.StringFixed(2) }

// zatcaXMLParts: the invoice without the UBL extensions, signature and QR (the part that is hashed), split where those
// go: head (root start tag) · body1 (up to PIH) · body2 (parties, totals, lines).
func zatcaXMLParts(inv *einvInvoice, cfg map[string]any, counter int64, pih, docUUID string) (head, body1, body2 string) {
	g := func(k string) string { return xmlEsc(strings.TrimSpace(str2(cfg[k]))) }
	typeName := "0200000" // simplified
	if inv.Standard {
		typeName = "0100000"
	}
	var b1 strings.Builder
	fmt.Fprintf(&b1, `<cbc:ProfileID>reporting:1.0</cbc:ProfileID><cbc:ID>%s</cbc:ID><cbc:UUID>%s</cbc:UUID><cbc:IssueDate>%s</cbc:IssueDate>`+
		`<cbc:IssueTime>%s</cbc:IssueTime><cbc:InvoiceTypeCode name="%s">388</cbc:InvoiceTypeCode><cbc:DocumentCurrencyCode>%s</cbc:DocumentCurrencyCode>`+
		`<cbc:TaxCurrencyCode>SAR</cbc:TaxCurrencyCode><cac:AdditionalDocumentReference><cbc:ID>ICV</cbc:ID><cbc:UUID>%d</cbc:UUID></cac:AdditionalDocumentReference>`+
		`<cac:AdditionalDocumentReference><cbc:ID>PIH</cbc:ID><cac:Attachment><cbc:EmbeddedDocumentBinaryObject mimeCode="text/plain">%s</cbc:EmbeddedDocumentBinaryObject></cac:Attachment></cac:AdditionalDocumentReference>`,
		xmlEsc(inv.Number), docUUID, inv.Date, inv.Time, typeName, inv.Currency, counter, pih)
	var b2 strings.Builder
	fmt.Fprintf(&b2, `<cac:AccountingSupplierParty><cac:Party><cac:PartyIdentification><cbc:ID schemeID="CRN">%s</cbc:ID></cac:PartyIdentification>`+
		`<cac:PostalAddress><cbc:StreetName>%s</cbc:StreetName><cbc:BuildingNumber>%s</cbc:BuildingNumber><cbc:CitySubdivisionName>%s</cbc:CitySubdivisionName>`+
		`<cbc:CityName>%s</cbc:CityName><cbc:PostalZone>%s</cbc:PostalZone><cac:Country><cbc:IdentificationCode>SA</cbc:IdentificationCode></cac:Country></cac:PostalAddress>`+
		`<cac:PartyTaxScheme><cbc:CompanyID>%s</cbc:CompanyID><cac:TaxScheme><cbc:ID>VAT</cbc:ID></cac:TaxScheme></cac:PartyTaxScheme>`+
		`<cac:PartyLegalEntity><cbc:RegistrationName>%s</cbc:RegistrationName></cac:PartyLegalEntity></cac:Party></cac:AccountingSupplierParty>`,
		g("crn"), g("street"), g("building"), g("district"), g("city"), g("postal_code"), g("vat_number"), g("seller_name"))
	b2.WriteString(`<cac:AccountingCustomerParty><cac:Party>`)
	if inv.Standard {
		fmt.Fprintf(&b2, `<cac:PostalAddress><cbc:StreetName>%s</cbc:StreetName><cac:Country><cbc:IdentificationCode>SA</cbc:IdentificationCode></cac:Country></cac:PostalAddress>`+
			`<cac:PartyTaxScheme><cbc:CompanyID>%s</cbc:CompanyID><cac:TaxScheme><cbc:ID>VAT</cbc:ID></cac:TaxScheme></cac:PartyTaxScheme>`, xmlEsc(inv.CustomerCity), xmlEsc(inv.CustomerVAT))
	}
	fmt.Fprintf(&b2, `<cac:PartyLegalEntity><cbc:RegistrationName>%s</cbc:RegistrationName></cac:PartyLegalEntity></cac:Party></cac:AccountingCustomerParty>`, xmlEsc(inv.Customer))
	fmt.Fprintf(&b2, `<cac:Delivery><cbc:ActualDeliveryDate>%s</cbc:ActualDeliveryDate></cac:Delivery><cac:PaymentMeans><cbc:PaymentMeansCode>10</cbc:PaymentMeansCode></cac:PaymentMeans>`, inv.Date)
	// tax totals by rate
	type sub struct{ net, tax decimal.Decimal }
	byRate := map[string]*sub{}
	rates := []string{}
	for _, l := range inv.Lines {
		k := l.Rate.StringFixed(2)
		if byRate[k] == nil {
			byRate[k] = &sub{}
			rates = append(rates, k)
		}
		byRate[k].net, byRate[k].tax = byRate[k].net.Add(l.Net), byRate[k].tax.Add(l.Tax)
	}
	sort.Strings(rates)
	fmt.Fprintf(&b2, `<cac:TaxTotal><cbc:TaxAmount currencyID="SAR">%s</cbc:TaxAmount></cac:TaxTotal><cac:TaxTotal><cbc:TaxAmount currencyID="%s">%s</cbc:TaxAmount>`,
		m2(inv.Tax), inv.Currency, m2(inv.Tax))
	for _, k := range rates {
		cat, reason := "S", ""
		if byRate[k].tax.IsZero() { // no VAT on the line: outside the scope of VAT, with its exemption reason (BR-KSA-69)
			cat, reason = "O", `<cbc:TaxExemptionReasonCode>VATEX-SA-OOS</cbc:TaxExemptionReasonCode><cbc:TaxExemptionReason>Not subject to VAT</cbc:TaxExemptionReason>`
		}
		fmt.Fprintf(&b2, `<cac:TaxSubtotal><cbc:TaxableAmount currencyID="%s">%s</cbc:TaxableAmount><cbc:TaxAmount currencyID="%s">%s</cbc:TaxAmount>`+
			`<cac:TaxCategory><cbc:ID>%s</cbc:ID><cbc:Percent>%s</cbc:Percent>%s<cac:TaxScheme><cbc:ID>VAT</cbc:ID></cac:TaxScheme></cac:TaxCategory></cac:TaxSubtotal>`,
			inv.Currency, m2(byRate[k].net), inv.Currency, m2(byRate[k].tax), cat, k, reason)
	}
	b2.WriteString(`</cac:TaxTotal>`)
	fmt.Fprintf(&b2, `<cac:LegalMonetaryTotal><cbc:LineExtensionAmount currencyID="%[1]s">%[2]s</cbc:LineExtensionAmount><cbc:TaxExclusiveAmount currencyID="%[1]s">%[2]s</cbc:TaxExclusiveAmount>`+
		`<cbc:TaxInclusiveAmount currencyID="%[1]s">%[3]s</cbc:TaxInclusiveAmount><cbc:PrepaidAmount currencyID="%[1]s">0.00</cbc:PrepaidAmount><cbc:PayableAmount currencyID="%[1]s">%[3]s</cbc:PayableAmount></cac:LegalMonetaryTotal>`,
		inv.Currency, m2(inv.Net), m2(inv.Total))
	for i, l := range inv.Lines {
		cat := "S"
		if l.Tax.IsZero() {
			cat = "O"
		}
		fmt.Fprintf(&b2, `<cac:InvoiceLine><cbc:ID>%d</cbc:ID><cbc:InvoicedQuantity unitCode="PCE">%s</cbc:InvoicedQuantity><cbc:LineExtensionAmount currencyID="%s">%s</cbc:LineExtensionAmount>`+
			`<cac:TaxTotal><cbc:TaxAmount currencyID="%s">%s</cbc:TaxAmount><cbc:RoundingAmount currencyID="%s">%s</cbc:RoundingAmount></cac:TaxTotal>`+
			`<cac:Item><cbc:Name>%s</cbc:Name><cac:ClassifiedTaxCategory><cbc:ID>%s</cbc:ID><cbc:Percent>%s</cbc:Percent><cac:TaxScheme><cbc:ID>VAT</cbc:ID></cac:TaxScheme></cac:ClassifiedTaxCategory></cac:Item>`+
			`<cac:Price><cbc:PriceAmount currencyID="%s">%s</cbc:PriceAmount></cac:Price></cac:InvoiceLine>`,
			i+1, l.Qty.String(), inv.Currency, m2(l.Net), inv.Currency, m2(l.Tax), inv.Currency, m2(l.Net.Add(l.Tax)), xmlEsc(l.Desc), cat, l.Rate.StringFixed(2),
			inv.Currency, l.Price.StringFixed(2))
	}
	return `<Invoice ` + ublNS + `>`, b1.String(), b2.String()
}

func tlv(tag byte, v []byte) []byte { return append([]byte{tag, byte(len(v))}, v...) }

// zatcaQR: phase-2 QR (tags 1–5 like phase 1, 6 hash, 7 signature, 8 public key, 9 certificate signature).
func zatcaQR(seller, vat, ts string, total, tax decimal.Decimal, hashB64, sigB64 string, pub, certSig []byte) string {
	var b []byte
	b = append(b, tlv(1, []byte(seller))...)
	b = append(b, tlv(2, []byte(vat))...)
	b = append(b, tlv(3, []byte(ts))...)
	b = append(b, tlv(4, []byte(m2(total)))...)
	b = append(b, tlv(5, []byte(m2(tax)))...)
	b = append(b, tlv(6, []byte(hashB64))...)
	b = append(b, tlv(7, []byte(sigB64))...)
	b = append(b, tlv(8, pub)...)
	if len(certSig) > 0 {
		b = append(b, tlv(9, certSig)...)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// certParts reads, from a base64 DER certificate, its issuer name (RFC 4514-ish) , serial number and signature.
func certParts(certB64 string) (issuer, serial string, sig []byte) {
	der, err := base64.StdEncoding.DecodeString(certB64)
	if err != nil {
		return "", "", nil
	}
	var c struct {
		TBS struct {
			Version   int `asn1:"optional,explicit,default:0,tag:0"`
			Serial    *big.Int
			SigAlg    asn1.RawValue
			Issuer    pkix.RDNSequence
			Validity  asn1.RawValue
			Subject   asn1.RawValue
			PublicKey asn1.RawValue
		}
		SigAlg asn1.RawValue
		Sig    asn1.BitString
	}
	if _, err := asn1.Unmarshal(der, &c); err != nil || c.TBS.Serial == nil {
		return "", "", nil
	}
	var n pkix.Name
	n.FillFromRDNSequence(&c.TBS.Issuer)
	return n.String(), c.TBS.Serial.String(), c.Sig.Bytes
}

// zatcaBuild makes the signed XML of an invoice. cert: base64 certificate ("" before onboarding: unsigned certificate
// parts are left empty, the hash, signature and QR are still produced).
func zatcaBuild(inv *einvInvoice, cfg map[string]any, key *k1Key, counter int64, pih, docUUID, cert string, now time.Time) (xml, hashB64, qr string, err error) {
	head, b1, b2 := zatcaXMLParts(inv, cfg, counter, pih, docUUID)
	hashForm := head + b1 + b2 + `</Invoice>`
	h := sha256.Sum256([]byte(hashForm))
	hashB64 = base64.StdEncoding.EncodeToString(h[:])
	sig, err := key.sign(h[:])
	if err != nil {
		return "", "", "", err
	}
	sigB64 := base64.StdEncoding.EncodeToString(sig)
	pub, err := key.spkiDER()
	if err != nil {
		return "", "", "", err
	}
	certRaw := []byte(cert) // the token is base64 of the base64 certificate
	certB64 := cert
	if d, err := base64.StdEncoding.DecodeString(cert); err == nil && len(d) > 0 {
		certB64 = string(d)
	}
	issuer, serial, certSig := certParts(certB64)
	ch := sha256.Sum256([]byte(certB64))
	certDigest := base64.StdEncoding.EncodeToString([]byte(hex.EncodeToString(ch[:])))
	_ = certRaw
	signTime := now.UTC().Format("2006-01-02T15:04:05")
	signedProps := `<xades:SignedProperties xmlns:xades="http://uri.etsi.org/01903/v1.3.2#" Id="xadesSignedProperties"><xades:SignedSignatureProperties>` +
		`<xades:SigningTime>` + signTime + `</xades:SigningTime><xades:SigningCertificate><xades:Cert><xades:CertDigest>` +
		`<ds:DigestMethod xmlns:ds="http://www.w3.org/2000/09/xmldsig#" Algorithm="http://www.w3.org/2001/04/xmlenc#sha256"></ds:DigestMethod>` +
		`<ds:DigestValue xmlns:ds="http://www.w3.org/2000/09/xmldsig#">` + certDigest + `</ds:DigestValue></xades:CertDigest><xades:IssuerSerial>` +
		`<ds:X509IssuerName xmlns:ds="http://www.w3.org/2000/09/xmldsig#">` + xmlEsc(issuer) + `</ds:X509IssuerName>` +
		`<ds:X509SerialNumber xmlns:ds="http://www.w3.org/2000/09/xmldsig#">` + serial + `</ds:X509SerialNumber></xades:IssuerSerial></xades:Cert>` +
		`</xades:SigningCertificate></xades:SignedSignatureProperties></xades:SignedProperties>`
	sp := sha256.Sum256([]byte(signedProps))
	spDigest := base64.StdEncoding.EncodeToString([]byte(hex.EncodeToString(sp[:])))
	ext := `<ext:UBLExtensions><ext:UBLExtension><ext:ExtensionURI>urn:oasis:names:specification:ubl:dsig:enveloped:xades</ext:ExtensionURI><ext:ExtensionContent>` +
		`<sig:UBLDocumentSignatures xmlns:sac="urn:oasis:names:specification:ubl:schema:xsd:SignatureAggregateComponents-2" xmlns:sbc="urn:oasis:names:specification:ubl:schema:xsd:SignatureBasicComponents-2" xmlns:sig="urn:oasis:names:specification:ubl:schema:xsd:CommonSignatureComponents-2">` +
		`<sac:SignatureInformation><cbc:ID>urn:oasis:names:specification:ubl:signature:1</cbc:ID><sbc:ReferencedSignatureID>urn:oasis:names:specification:ubl:signature:Invoice</sbc:ReferencedSignatureID>` +
		`<ds:Signature xmlns:ds="http://www.w3.org/2000/09/xmldsig#" Id="signature"><ds:SignedInfo>` +
		`<ds:CanonicalizationMethod Algorithm="http://www.w3.org/2006/12/xml-c14n11"></ds:CanonicalizationMethod>` +
		`<ds:SignatureMethod Algorithm="http://www.w3.org/2001/04/xmldsig-more#ecdsa-sha256"></ds:SignatureMethod>` +
		`<ds:Reference Id="invoiceSignedData" URI=""><ds:Transforms>` +
		`<ds:Transform Algorithm="http://www.w3.org/TR/1999/REC-xpath-19991116"><ds:XPath>not(//ancestor-or-self::ext:UBLExtensions)</ds:XPath></ds:Transform>` +
		`<ds:Transform Algorithm="http://www.w3.org/TR/1999/REC-xpath-19991116"><ds:XPath>not(//ancestor-or-self::cac:Signature)</ds:XPath></ds:Transform>` +
		`<ds:Transform Algorithm="http://www.w3.org/TR/1999/REC-xpath-19991116"><ds:XPath>not(//ancestor-or-self::cac:AdditionalDocumentReference[cbc:ID='QR'])</ds:XPath></ds:Transform>` +
		`<ds:Transform Algorithm="http://www.w3.org/2006/12/xml-c14n11"></ds:Transform></ds:Transforms>` +
		`<ds:DigestMethod Algorithm="http://www.w3.org/2001/04/xmlenc#sha256"></ds:DigestMethod><ds:DigestValue>` + hashB64 + `</ds:DigestValue></ds:Reference>` +
		`<ds:Reference Type="http://www.w3.org/2000/09/xmldsig#SignatureProperties" URI="#xadesSignedProperties">` +
		`<ds:DigestMethod Algorithm="http://www.w3.org/2001/04/xmlenc#sha256"></ds:DigestMethod><ds:DigestValue>` + spDigest + `</ds:DigestValue></ds:Reference>` +
		`</ds:SignedInfo><ds:SignatureValue>` + sigB64 + `</ds:SignatureValue><ds:KeyInfo><ds:X509Data><ds:X509Certificate>` + certB64 + `</ds:X509Certificate></ds:X509Data></ds:KeyInfo>` +
		`<ds:Object><xades:QualifyingProperties xmlns:xades="http://uri.etsi.org/01903/v1.3.2#" Target="signature">` + signedProps + `</xades:QualifyingProperties></ds:Object>` +
		`</ds:Signature></sac:SignatureInformation></sig:UBLDocumentSignatures></ext:ExtensionContent></ext:UBLExtension></ext:UBLExtensions>`
	ts := inv.Date + "T" + inv.Time
	qr = zatcaQR(str2(cfg["seller_name"]), str2(cfg["vat_number"]), ts, inv.Total, inv.Tax, hashB64, sigB64, pub, certSig)
	qrRef := `<cac:AdditionalDocumentReference><cbc:ID>QR</cbc:ID><cac:Attachment><cbc:EmbeddedDocumentBinaryObject mimeCode="text/plain">` + qr +
		`</cbc:EmbeddedDocumentBinaryObject></cac:Attachment></cac:AdditionalDocumentReference>`
	sigRef := `<cac:Signature><cbc:ID>urn:oasis:names:specification:ubl:signature:Invoice</cbc:ID><cbc:SignatureMethod>urn:oasis:names:specification:ubl:dsig:enveloped:xades</cbc:SignatureMethod></cac:Signature>`
	xml = `<?xml version="1.0" encoding="UTF-8"?>` + head + ext + b1 + qrRef + sigRef + b2 + `</Invoice>`
	return xml, hashB64, qr, nil
}

// the first PIH of a unit: base64(SHA-256("0"))
var zatcaFirstPIH = "NWZlY2ViNjZmZmM4NmYzOGQ5NTI3ODZjNmQ2OTZjNzljMmRiYzIzOWRkNGU5MWI0NjcyOWQ3M2EyN2ZiNTdlOQ=="

// ================= ETA (Egypt) =================

// etaSerialize implements ETA's canonical serialization (property names upper-cased and quoted, values quoted,
// array items repeated under their property name), the text that is hashed and signed.
func etaSerialize(v any) string {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		for _, k := range keys {
			name := `"` + strings.ToUpper(k) + `"`
			if arr, ok := x[k].([]any); ok {
				b.WriteString(name)
				for _, it := range arr {
					b.WriteString(name)
					b.WriteString(etaSerialize(it))
				}
				continue
			}
			b.WriteString(name)
			b.WriteString(etaSerialize(x[k]))
		}
		return b.String()
	case string:
		return `"` + x + `"`
	case nil:
		return `""`
	default:
		return `"` + fmt.Sprint(x) + `"`
	}
}

func etaDocument(inv *einvInvoice, cfg map[string]any, now time.Time) map[string]any {
	g := func(k string) string { return strings.TrimSpace(str2(cfg[k])) }
	f5 := func(d decimal.Decimal) string { return d.StringFixed(5) }
	lines := []any{}
	for _, l := range inv.Lines {
		lines = append(lines, map[string]any{
			"description": l.Desc, "itemType": "EGS", "itemCode": "EG-" + g("issuer_id") + "-1", "unitType": "EA", "quantity": l.Qty.StringFixed(5),
			"internalCode": "1", "salesTotal": f5(l.Net), "total": f5(l.Net.Add(l.Tax)), "valueDifference": "0.00000", "totalTaxableFees": "0.00000",
			"netTotal": f5(l.Net), "itemsDiscount": "0.00000",
			"unitValue":    map[string]any{"currencySold": "EGP", "amountEGP": f5(l.Price)},
			"discount":     map[string]any{"rate": "0", "amount": "0.00000"},
			"taxableItems": []any{map[string]any{"taxType": "T1", "amount": f5(l.Tax), "subType": "V009", "rate": l.Rate.StringFixed(2)}},
		})
	}
	receiverType := "P"
	if inv.Standard {
		receiverType = "B"
	}
	return map[string]any{
		"issuer": map[string]any{"type": defaultStr(g("issuer_type"), "B"), "id": g("issuer_id"), "name": g("issuer_name"),
			"address": map[string]any{"branchID": defaultStr(g("branch_id"), "0"), "country": defaultStr(g("country"), "EG"), "governate": g("governate"),
				"regionCity": g("region_city"), "street": g("street"), "buildingNumber": g("building")}},
		"receiver": map[string]any{"type": receiverType, "id": inv.CustomerVAT, "name": inv.Customer,
			"address": map[string]any{"country": "EG", "governate": "", "regionCity": "", "street": inv.CustomerCity, "buildingNumber": ""}},
		"documentType": "I", "documentTypeVersion": "1.0", "dateTimeIssued": now.UTC().Format("2006-01-02T15:04:05Z"),
		"taxpayerActivityCode": g("activity_code"), "internalID": inv.Number, "invoiceLines": lines,
		"totalDiscountAmount": "0.00000", "totalSalesAmount": f5(inv.Net), "netAmount": f5(inv.Net),
		"taxTotals":   []any{map[string]any{"taxType": "T1", "amount": f5(inv.Tax)}},
		"totalAmount": f5(inv.Total), "extraDiscountAmount": "0.00000", "totalItemsDiscountAmount": "0.00000",
	}
}

func etaToken(ctx context.Context, env, id, secret string) (string, error) {
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {id}, "client_secret": {secret}, "scope": {"InvoicingAPI"}}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, etaBase[env][0]+"/connect/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var t struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&t)
	if t.AccessToken == "" {
		return "", fmt.Errorf("eta token: %d %s", res.StatusCode, t.Error)
	}
	return t.AccessToken, nil
}

// etaSign asks the company's signing service (USB token on their computer / HSM) for the CAdES-BES signature.
func etaSign(ctx context.Context, signer, token, serialized string) (string, error) {
	b, _ := json.Marshal(map[string]string{"data": serialized, "hash": func() string { h := sha256.Sum256([]byte(serialized)); return hex.EncodeToString(h[:]) }()})
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, signer, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var out struct {
		Signature string `json:"signature"`
	}
	_ = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out)
	if out.Signature == "" {
		return "", fmt.Errorf("signer answered %d without a signature", res.StatusCode)
	}
	return out.Signature, nil
}

// ================= per invoice =================

// POST /companies/{cid}/invoices/{id}/einvoice {submit: bool}: makes (once) the e-invoice of the company's system and,
// with submit, sends it (ZATCA report / clearance; ETA submission).
func (s *Server) invoiceEinvoice(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Submit bool `json:"submit"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cid, id := r.PathValue("cid"), r.PathValue("id")
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var kind string
		if err := tx.QueryRow(ctx, `SELECT einvoice FROM companies WHERE id = $1`, cid).Scan(&kind); err != nil {
			return nil, err
		}
		if kind != "zatca" && kind != "eta" {
			return nil, notSetup("einvoice")
		}
		inv, err := loadEinvInvoice(ctx, tx, cid, id)
		if err != nil {
			return nil, err
		}
		var docID, status, payload, hash string
		err = tx.QueryRow(ctx, `SELECT id::text, status, payload, hash FROM einvoice_docs WHERE invoice_id = $1 AND kind = $2 FOR UPDATE`, id, kind).Scan(&docID, &status, &payload, &hash)
		if errors.Is(err, pgx.ErrNoRows) {
			if docID, err = createEinvDoc(ctx, tx, cid, kind, inv); err != nil {
				return nil, err
			}
		} else if err != nil {
			return nil, err
		}
		if in.Submit {
			if err := submitEinvDoc(ctx, tx, cid, kind, docID, inv); err != nil {
				return nil, err
			}
		}
		return loadEinvDoc(ctx, tx, docID)
	})
}

func loadEinvDoc(ctx context.Context, tx pgx.Tx, docID string) (json.RawMessage, error) {
	return queryJSONOne(ctx, tx, `SELECT id, invoice_id, kind, counter, doc_uuid, hash, prev_hash, qr, status, response, submitted_at, created_at,
		length(payload) AS size FROM einvoice_docs WHERE id = $1`, docID)
}

func createEinvDoc(ctx context.Context, tx pgx.Tx, cid, kind string, inv *einvInvoice) (string, error) {
	env, cfg, counter, last, err := loadEinvConfig(ctx, tx, cid, kind)
	if err != nil {
		return "", err
	}
	_ = env
	docUUID := newUUIDv4()
	var payload, hash, qr, prev string
	var next int64
	switch kind {
	case "zatca":
		key, err := zatcaKey(cfg)
		if err != nil {
			return "", err
		}
		if _, err := tx.Exec(ctx, `SELECT 1 FROM einvoice_config WHERE company_id = $1 AND kind = 'zatca' FOR UPDATE`, cid); err != nil {
			return "", err
		}
		next = counter + 1
		prev = defaultStr(last, zatcaFirstPIH)
		cert := str2(cfg["pcsid"])
		if cert == "" {
			cert = str2(cfg["csid"])
		}
		payload, hash, qr, err = zatcaBuild(inv, cfg, key, next, prev, docUUID, cert, time.Now())
		if err != nil {
			return "", err
		}
		if _, err := tx.Exec(ctx, `UPDATE einvoice_config SET counter = $2, last_hash = $3, updated_at = now() WHERE company_id = $1 AND kind = 'zatca'`, cid, next, hash); err != nil {
			return "", err
		}
	case "eta":
		doc := etaDocument(inv, cfg, time.Now())
		b, _ := json.Marshal(doc)
		payload = string(b)
		ser := etaSerialize(jsonRoundTrip(doc))
		h := sha256.Sum256([]byte(ser))
		hash = hex.EncodeToString(h[:])
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO einvoice_docs (company_id, invoice_id, kind, counter, doc_uuid, hash, prev_hash, payload, qr)
		VALUES ($1, $2, $3, $4, $5::uuid, $6, $7, $8, $9) RETURNING id::text`, cid, inv.ID, kind, next, docUUID, hash, prev, payload, qr).Scan(&id)
	return id, err
}

func jsonRoundTrip(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

func submitEinvDoc(ctx context.Context, tx pgx.Tx, cid, kind, docID string, inv *einvInvoice) error {
	env, cfg, _, _, err := loadEinvConfig(ctx, tx, cid, kind)
	if err != nil {
		return err
	}
	var payload, hash, docUUID, status string
	if err := tx.QueryRow(ctx, `SELECT payload, hash, doc_uuid::text, status FROM einvoice_docs WHERE id = $1`, docID).Scan(&payload, &hash, &docUUID, &status); err != nil {
		return err
	}
	if status == "reported" || status == "cleared" || status == "valid" || status == "accepted" {
		return nil
	}
	var st int
	var res map[string]any
	newStatus := "error"
	switch kind {
	case "zatca":
		user, pass := str2(cfg["pcsid"]), openSecret(str2(cfg["pcsid_secret"]))
		path := "/invoices/reporting/single"
		if inv.Standard {
			path = "/invoices/clearance/single"
		}
		if user == "" { // only the compliance certificate: the compliance check endpoint
			user, pass, path = str2(cfg["csid"]), openSecret(str2(cfg["csid_secret"])), "/compliance/invoices"
		}
		if user == "" {
			return notSetup("einvoice_csid")
		}
		st, res, err = zatcaCall(ctx, env, path, user, pass, "", map[string]string{"invoiceHash": hash, "uuid": docUUID,
			"invoice": base64.StdEncoding.EncodeToString([]byte(payload))}, map[string]string{"Clearance-Status": map[bool]string{true: "1", false: "0"}[inv.Standard]})
		if err != nil {
			return &apiError{status: http.StatusBadGateway, code: "einvoice_unreachable", detail: err.Error()}
		}
		switch {
		case st == 200 || st == 202:
			newStatus = map[bool]string{true: "cleared", false: "reported"}[inv.Standard]
			if path == "/compliance/invoices" {
				newStatus = "accepted"
			}
		case st == 400 || st == 409:
			newStatus = "rejected"
		}
	case "eta":
		signer := str2(cfg["signer_url"])
		if signer == "" || str2(cfg["client_id"]) == "" {
			return notSetup("einvoice_eta")
		}
		var doc map[string]any
		_ = json.Unmarshal([]byte(payload), &doc)
		sig, err := etaSign(ctx, signer, openSecret(str2(cfg["signer_token"])), etaSerialize(jsonRoundTrip(doc)))
		if err != nil {
			return &apiError{status: http.StatusBadGateway, code: "einvoice_signer", detail: err.Error()}
		}
		doc["signatures"] = []any{map[string]any{"signatureType": "I", "value": sig}}
		tok, err := etaToken(ctx, env, str2(cfg["client_id"]), openSecret(str2(cfg["client_secret"])))
		if err != nil {
			return &apiError{status: http.StatusBadGateway, code: "einvoice_unreachable", detail: err.Error()}
		}
		b, _ := json.Marshal(map[string]any{"documents": []any{doc}})
		rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(rctx, http.MethodPost, etaBase[env][1]+"/api/v1/documentsubmissions", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return &apiError{status: http.StatusBadGateway, code: "einvoice_unreachable", detail: err.Error()}
		}
		defer resp.Body.Close()
		st = resp.StatusCode
		res = map[string]any{}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&res)
		newStatus = map[bool]string{true: "submitted", false: "rejected"}[st == 202]
	}
	rb, _ := json.Marshal(map[string]any{"http_status": st, "body": res})
	_, err = tx.Exec(ctx, `UPDATE einvoice_docs SET status = $2, response = $3, submitted_at = now() WHERE id = $1`, docID, newStatus, rb)
	return err
}

// GET /companies/{cid}/invoices/{id}/einvoice (?xml=1 for the document itself)
func (s *Server) getInvoiceEinvoice(w http.ResponseWriter, r *http.Request) {
	cid, id := r.PathValue("cid"), r.PathValue("id")
	if !isUUID(id) {
		writeErr(w, http.StatusNotFound, "not_found")
		return
	}
	if r.URL.Query().Get("xml") == "1" {
		var payload, kind string
		s.runRaw(w, r, func(ctx context.Context, tx pgx.Tx) error {
			err := tx.QueryRow(ctx, `SELECT payload, kind FROM einvoice_docs WHERE invoice_id = $1 AND company_id = $2 ORDER BY created_at DESC LIMIT 1`, id, cid).Scan(&payload, &kind)
			if errors.Is(err, pgx.ErrNoRows) {
				return notFound()
			}
			return err
		}, func() {
			w.Header().Set("Content-Type", map[string]string{"zatca": "application/xml", "eta": "application/json"}[kind])
			_, _ = io.WriteString(w, payload)
		})
		return
	}
	s.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		var mine bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM invoices WHERE id = $1 AND company_id = $2)`, id, cid).Scan(&mine); err != nil || !mine {
			return nil, notFound()
		}
		var doc string
		err := tx.QueryRow(ctx, `SELECT id::text FROM einvoice_docs WHERE invoice_id = $1 AND company_id = $2 ORDER BY created_at DESC LIMIT 1`, id, cid).Scan(&doc)
		if errors.Is(err, pgx.ErrNoRows) {
			return map[string]any{"status": "none"}, nil
		}
		if err != nil {
			return nil, err
		}
		return loadEinvDoc(ctx, tx, doc)
	})
}

func newUUIDv4() string {
	b := []byte(randToken(16))
	h := sha256.Sum256(append(b, []byte(time.Now().String())...))
	u := h[:16]
	u[6] = (u[6] & 0x0f) | 0x40
	u[8] = (u[8] & 0x3f) | 0x80
	x := hex.EncodeToString(u)
	return x[0:8] + "-" + x[8:12] + "-" + x[12:16] + "-" + x[16:20] + "-" + x[20:32]
}
