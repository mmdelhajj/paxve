package magic

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func TestSyntheticPlanMatchesMagicBalances(t *testing.T) {
	zipBytes, exp := Synthetic(7)
	d, err := Parse(zipBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Companies) != 3 {
		t.Fatalf("companies: %d", len(d.Companies))
	}
	if !d.DefaultRate().Equal(decimal.NewFromInt(89500)) {
		t.Fatalf("default rate %s", d.DefaultRate())
	}
	for _, co := range []int{3, 11, 12} {
		p, err := Build(d, co, Options{OpeningDate: "2026-11-01", Today: "2026-11-18"})
		if err != nil {
			t.Fatal(err)
		}
		// customer: unpaid bills + opening balance == Magic balance (per currency)
		got := map[string]map[string]decimal.Decimal{}
		add := func(ref, cur string, v decimal.Decimal) {
			if got[ref] == nil {
				got[ref] = map[string]decimal.Decimal{}
			}
			got[ref][cur] = got[ref][cur].Add(v)
		}
		for _, b := range p.Bills {
			add(b.CustomerRef, b.Currency, b.Amount)
		}
		for _, o := range p.CustOpen {
			add(o.PartyRef, o.Currency, o.Amount)
		}
		for person, cm := range exp.Customers[co] {
			for cur, want := range cm {
				r := "magic:customer:" + strconv.Itoa(person)
				if have := got[r][cur]; !have.Equal(want) && want.Sub(have).Abs().GreaterThan(decimal.RequireFromString("0.01")) {
					t.Errorf("company %d customer %d %s: plan %s, Magic %s", co, person, cur, have, want)
				}
			}
		}
		// AR clearing line == Σ customer documents
		docs := map[string]decimal.Decimal{}
		for r, cm := range got {
			_ = r
			for cur, v := range cm {
				docs[cur] = docs[cur].Add(v)
			}
		}
		for _, g := range p.GL {
			if g.Kind == "ar_clearing" && !g.Amount.Equal(docs[g.Currency]) {
				t.Errorf("company %d: AR clearing %s %s, documents %s", co, g.Amount, g.Currency, docs[g.Currency])
			}
		}
		// Magic's own imbalance (the one-line entry of company 3)
		if !p.MagicImbalanceUSD.Equal(exp.Imbalance[co]) {
			t.Errorf("company %d: imbalance %s, want %s", co, p.MagicImbalanceUSD, exp.Imbalance[co])
		}
		if co == 3 {
			if !p.MagicImbalanceUSD.Equal(decimal.RequireFromString("-7.5")) {
				t.Errorf("imbalance %s", p.MagicImbalanceUSD)
			}
			codes := map[string]bool{}
			for _, w := range p.Warnings() {
				codes[w.Code] = true
			}
			for _, c := range []string{"bad_rate_repaired", "duplicate_bill_period", "bad_start_date", "bad_bill_date",
				"bill_without_subscription", "magic_books_unbalanced", "supplier_advance_in_gl"} {
				if !codes[c] {
					t.Errorf("missing warning %s (have %v)", c, codes)
				}
			}
			// supplier 9001: 4200 bought, 3000 paid → we owe 1200
			if len(p.SuppOpen) != 1 || !p.SuppOpen[0].Amount.Equal(decimal.NewFromInt(1200)) {
				t.Errorf("supplier openings %+v", p.SuppOpen)
			}
			// duplicate box name renamed, both boxes kept
			n := 0
			for _, b := range p.Boxes {
				if strings.HasPrefix(b.Code, "علبة الساحة") {
					n++
				}
			}
			if n != 2 {
				t.Errorf("boxes named علبة الساحة: %d", n)
			}
			// previous-year result folded into retained earnings
			found := false
			for _, g := range p.GL {
				if g.Kind == "retained" {
					found = true
				}
				if g.Kind == "gl" && g.Code == "6312" {
					t.Errorf("2025 wages should be in retained earnings")
				}
			}
			if !found {
				t.Error("no retained earnings line")
			}
			s := p.Summary()
			if s.Counts["subscriptions"] != 29 || s.Counts["collectors"] != 2 || s.Counts["metered_plans"] != 2 {
				t.Errorf("counts %+v", s.Counts)
			}
		}
		if co == 11 && len(p.Customers) != 18 {
			t.Errorf("company 11 customers: %d", len(p.Customers))
		}
	}
}

// TestExportScriptZip checks a zip produced by tools/magic-export/Export-Magic.ps1 from a SQL Server database
// loaded with `paxve magic-sample` data (seed 42): it must give the same migration plan as the sample itself.
//
//	MAGIC_EXPORT_ZIP=/path/magic-export.zip go test ./internal/magic -run TestExportScriptZip
func TestExportScriptZip(t *testing.T) {
	path := os.Getenv("MAGIC_EXPORT_ZIP")
	if path == "" {
		t.Skip("MAGIC_EXPORT_ZIP not set")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	sample, _ := Synthetic(42)
	want, err := Parse(sample)
	if err != nil {
		t.Fatal(err)
	}
	opt := Options{OpeningDate: "2026-11-01", Today: "2026-11-18", ReadingsMonths: 120}
	for _, co := range []int{3, 11} {
		pg, err := Build(got, co, opt)
		if err != nil {
			t.Fatal(err)
		}
		pw, _ := Build(want, co, opt)
		sg, sw := pg.Summary(), pw.Summary()
		jg, _ := json.Marshal(sg)
		jw, _ := json.Marshal(sw)
		if string(jg) != string(jw) {
			t.Errorf("company %d:\n script: %s\n sample: %s", co, jg, jw)
		}
	}
}

func TestParseRejectsBadFiles(t *testing.T) {
	if _, err := Parse([]byte("not a zip")); err == nil || err.Error() != "not_a_zip" {
		t.Fatalf("got %v", err)
	}
}

func TestAccountType(t *testing.T) {
	cases := map[string]string{"10": "equity", "401": "liability", "4111": "asset", "531": "asset", "6312": "expense", "711": "income", "16": "liability"}
	for code, want := range cases {
		if got := AccountType(code, 0); got != want {
			t.Errorf("%s: %s, want %s", code, got, want)
		}
	}
	if AccountType("4111", 4) != "liability" || AccountType("7", 5) != "expense" {
		t.Error("balance type should win")
	}
}
