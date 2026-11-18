package magic

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"sort"
	"strconv"
	"time"

	"github.com/shopspring/decimal"
)

// Synthetic builds a realistic Magic export (two companies: generators + internet, Arabic names, USD bills,
// mixed USD/LBP payments with LBP change, advances, suppliers, an unbalanced one-line journal entry, a
// line with a broken exchange rate, duplicate / deleted / discounted bills…) and the balances the
// migration must reproduce. It is used by the tests and by `paxve magic-sample`.
//
// It never reads a real database: every name and number is generated.
func Synthetic(seed uint64) ([]byte, *Expected) {
	g := &synth{r: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)), rate: decimal.NewFromInt(89500),
		exp: &Expected{Customers: map[int]map[int]map[string]decimal.Decimal{}, Suppliers: map[int]map[int]map[string]decimal.Decimal{}}}
	g.build()
	return g.zip(), g.exp
}

// Expected balances (in the account currency) after migration.
type Expected struct {
	// company → person → currency → balance (debit − credit)
	Customers map[int]map[int]map[string]decimal.Decimal
	Suppliers map[int]map[int]map[string]decimal.Decimal
	// company → Σ(debit − credit) in USD of Magic's lines (unbalanced one-line entries)
	Imbalance map[int]decimal.Decimal
	Rate      decimal.Decimal
}

type mline struct {
	company, ledger, person, money, lineMoney int
	date                                      string
	db, cr                                    decimal.Decimal
	dol, ll                                   decimal.Decimal
	visible                                   bool
}

type synth struct {
	r     *rand.Rand
	rate  decimal.Decimal
	exp   *Expected
	lines []mline
	files map[string][][]string
	order []string
}

func (g *synth) table(name string, header ...string) {
	if g.files == nil {
		g.files = map[string][][]string{}
	}
	g.files[name] = [][]string{header}
	g.order = append(g.order, name)
}

func (g *synth) add(name string, vals ...any) {
	rec := make([]string, len(vals))
	for i, v := range vals {
		switch x := v.(type) {
		case nil:
			rec[i] = ""
		case bool:
			if x {
				rec[i] = "1"
			} else {
				rec[i] = "0"
			}
		case decimal.Decimal:
			rec[i] = x.String()
		default:
			rec[i] = fmt.Sprint(x)
		}
	}
	g.files[name] = append(g.files[name], rec)
}

var (
	firstNames = []string{"محمد", "علي", "حسن", "حسين", "فاطمة", "زينب", "أحمد", "خليل", "مريم", "يوسف", "رنا", "جورج", "إيلي", "نادين", "سامر", "هادي", "ليال", "كريم"}
	lastNames  = []string{"الحاج", "شبيب", "العسل", "حمدان", "سليمان", "خوري", "نصر", "عيسى", "طه", "فرحات", "زين الدين", "حداد"}
	usd        = 1
	lbp        = 2
)

func (g *synth) name() string {
	return firstNames[g.r.IntN(len(firstNames))] + " " + lastNames[g.r.IntN(len(lastNames))]
}

func (g *synth) phone() string { return fmt.Sprintf("0%d %06d", 3+g.r.IntN(5), g.r.IntN(1000000)) }

// post adds a journal line. amount in the line currency; for an LBP line in a USD account dol = 1/rate.
func (g *synth) post(company, ledger, person, money, lineMoney int, date string, db, cr decimal.Decimal) {
	l := mline{company: company, ledger: ledger, person: person, money: money, lineMoney: lineMoney, date: date, db: db, cr: cr, visible: true}
	switch {
	case lineMoney == usd && money == usd:
		l.dol, l.ll = decimal.NewFromInt(1), decimal.NewFromInt(1)
	case lineMoney == lbp && money == lbp:
		l.dol, l.ll = decimal.NewFromInt(1).DivRound(g.rate, 12), decimal.NewFromInt(1)
	case lineMoney == lbp && money == usd:
		l.dol, l.ll = decimal.NewFromInt(1).DivRound(g.rate, 12), decimal.NewFromInt(1)
	case lineMoney == usd && money == lbp:
		l.dol, l.ll = decimal.NewFromInt(1), g.rate
	}
	g.lines = append(g.lines, l)
}

func (g *synth) build() {
	g.table("Company", "CompanyID", "CompanyName", "CompanyTypeID", "Available")
	g.add("Company", 3, "كهرباء الضيعة", 3, true)
	g.add("Company", 11, "إنترنت الضيعة", 5, true)
	g.add("Company", 12, "الادارة العامة", 10, true)
	g.table("Branchs", "BranchID", "BranchName", "CompanyID")
	g.add("Branchs", 3, "كهرباء", 3)
	g.add("Branchs", 11, "نت", 11)
	g.add("Branchs", 12, "Managment", 12)
	g.table("Stations", "StationID", "Station", "BranchID", "Available")
	g.add("Stations", 1, "محطة الساحة", 3, true)
	g.add("Stations", 2, "محطة الجبل", 3, true)
	g.add("Stations", 51, "FarahNet", 11, true)
	g.table("Money", "MoneyID", "Money", "DolBource", "LLBource", "CashBackLLBource")
	g.add("Money", 1, "USD", 1, g.rate, g.rate)
	g.add("Money", 2, "LBP", decimal.NewFromInt(1).DivRound(g.rate, 12), 1, nil)

	// people: 2 = cash box, 15 = sales, 20 = capital, 5001-5003 collectors, 9001-9002 suppliers, 10001… customers
	g.table("AccountNames", "AccountNameID", "AccountName", "Available", "AccountType")
	g.table("AcountNamesInformation", "AccountNameID", "TypeInformationID", "Information")
	g.add("AccountNames", 2, "الصندوق", true, 1)
	g.add("AccountNames", 15, "المبيعات", true, 1)
	g.add("AccountNames", 20, "رأس المال", true, 1)
	collectors := map[int]string{5001: "أبو علي الجابي", 5002: "سامي الجابي", 5003: "جابي النت"}
	for _, id := range []int{5001, 5002, 5003} {
		g.add("AccountNames", id, collectors[id], true, 2)
		g.add("AcountNamesInformation", id, 1, g.phone())
	}
	g.add("AccountNames", 9001, "شركة المحروقات", true, 3)
	g.add("AccountNames", 9002, "مؤسسة الكابلات", true, 3)
	g.add("AcountNamesInformation", 9001, 1, g.phone())

	g.table("Collector", "CollectorID", "AccountNameID", "CompanyID", "Activate", "DefaultC", "AccountName", "CollectorName")
	g.add("Collector", 101, 5001, 3, true, true, "", collectors[5001])
	g.add("Collector", 102, 5002, 3, true, false, "", collectors[5002])
	g.add("Collector", 103, 5001, 11, true, false, "", collectors[5001]) // same person in two companies
	g.add("Collector", 104, 5003, 11, true, true, "", collectors[5003])

	g.table("Location", "LocationID", "Location", "StationID", "DefaultCollectorID", "Available", "Adress", "LocOrder")
	boxes3 := []int{301, 302, 303, 304}
	g.add("Location", 301, "علبة الساحة", 1, 101, true, "قرب الجامع", 1)
	g.add("Location", 302, "علبة المدرسة", 1, 101, true, "", 2)
	g.add("Location", 303, "علبة الساحة", 2, 102, true, "", 3) // duplicate name → renamed
	g.add("Location", 304, "علبة الجبل", 2, 102, true, "", 4)
	boxes11 := []int{501, 502}
	g.add("Location", 501, "حي الكنيسة", 51, 104, true, "", 1)
	g.add("Location", 502, "حي البيادر", 51, 103, true, "", 2)

	g.table("Sevice", "SeviceID", "TypeInscriptionID", "Sevice", "PowerService", "CompanyID", "BaseQt", "BaseCost", "Cost", "NetCost", "Discount", "Available")
	g.add("Sevice", 31, 1, "5 أمبير مقطوع", 5, 3, 0, 0, 25, 0, 0, true)
	g.add("Sevice", 32, 1, "10 أمبير مقطوع", 10, 3, 0, 0, 45, 0, 0, true)
	g.add("Sevice", 33, 3, "عداد 10A", 10, 3, 0, 4, decimal.RequireFromString("0.65"), 0, 0, true)
	g.add("Sevice", 34, 3, "عداد 20A", 20, 3, 0, 7, decimal.RequireFromString("0.65"), 0, 0, true)
	g.add("Sevice", 111, 1, "4M", 0, 11, 0, 0, 20, 0, 0, true)
	g.add("Sevice", 112, 1, "8M+Real IP", 0, 11, 0, 0, 40, 35, 5, true)
	g.add("Sevice", 113, 1, "باقة قديمة", 0, 11, 0, 0, 10, 0, 0, false)

	g.table("LedgerAccount", "LedgerID", "LedgerParentID", "NameEn", "NameAr", "NameFr", "BalanceTypeID", "Level", "Used")
	for _, l := range []struct {
		id, parent, bt int
		en, ar         string
	}{
		{10, 1, 3, "Capital", "رأس المال"}, {12, 1, 3, "Result", "النتيجة"}, {401, 40, 4, "Suppliers", "الموردون"},
		{4111, 411, 2, "Regular customers", "الزبائن العاديون"}, {42, 4, 4, "Staff", "المستخدمون"}, {531, 53, 2, "Cash", "الصندوق"},
		{601, 60, 5, "Purchases of fuel", "مشتريات المحروقات"}, {6312, 631, 5, "Wages", "الأجور"}, {711, 71, 6, "Sales", "المبيعات"},
		{675, 67, 5, "FX losses", "خسائر الصرف"}, {775, 77, 6, "FX gains", "أرباح الصرف"},
	} {
		g.add("LedgerAccount", l.id, l.parent, l.en, l.ar, l.en, l.bt, 3, true)
	}

	g.table("Accounts", "LedgerID", "AccountNameID", "MoneyID", "CompanyID", "Freezed", "Available")
	g.table("Subscrip", "SubID", "LedgerID", "AccountNameID", "MoneyID", "CompanyID", "ExecutiveCollectorID", "LocationID", "NumInLocation",
		"SeviceID", "TypeInscriptionID", "TypePaymentSubID", "DateBegin", "Freezed", "FreezedNoCharge", "Activeted", "Available", "Maktab",
		"SpeciaBaseCost", "SpeciaCost", "Adress", "UserNet", "CellPhone", "HomePhone", "ExpiredDate", "RQ")
	g.table("DataCustomer", "IssalID", "Annee", "CompanyID", "SubID", "MonthID", "YesNoPayed", "NewCounter", "OldCounter", "BaseCost", "Cost",
		"FinalCost", "Discount", "ValuePayed", "ValuePayedLBP", "CashbackLBP", "Bource", "DateInvoiced", "DateMustPayed", "Deleted", "Visible", "SeviceID")

	year := 2026
	issal := map[int]int{3: 1000, 11: 5000}
	subID := 40000
	accountsSeen := map[[4]int]bool{}
	account := func(ledger, person, money, company int) {
		k := [4]int{ledger, person, money, company}
		if !accountsSeen[k] {
			accountsSeen[k] = true
			g.add("Accounts", ledger, person, money, company, false, true)
		}
	}
	for _, c := range []int{3, 11} {
		account(531, 2, usd, c)
		account(531, 2, lbp, c)
		account(711, 15, usd, c)
		account(10, 20, usd, c)
	}
	// capital at the start
	g.post(3, 531, 2, usd, usd, "2024-01-02", d(5000), d(0))
	g.post(3, 10, 20, usd, usd, "2024-01-02", d(0), d(5000))
	g.post(11, 531, 2, usd, usd, "2024-01-02", d(2000), d(0))
	g.post(11, 10, 20, usd, usd, "2024-01-02", d(0), d(2000))

	people := 0
	newCustomer := func() int {
		people++
		id := 10000 + people
		nm := g.name()
		if people == 7 {
			nm = "" // a nameless account: imported as "Magic <id>"
		}
		g.add("AccountNames", id, nm, true, 0)
		g.add("AcountNamesInformation", id, 1, g.phone())
		if people%3 == 0 {
			g.add("AcountNamesInformation", id, 7, "حي "+lastNames[g.r.IntN(len(lastNames))])
		}
		return id
	}
	type subx struct {
		id, person, company, svc, loc int
		metered                       bool
		fee                           decimal.Decimal
	}
	var all []subx
	mk := func(company, person, svc, loc int, metered bool, fee decimal.Decimal, status string) subx {
		subID++
		s := subx{id: subID, person: person, company: company, svc: svc, loc: loc, metered: metered, fee: fee}
		account(4111, person, usd, company)
		freezed, active, avail := false, true, true
		switch status {
		case "suspended":
			freezed = true
		case "ended":
			avail = false
		}
		var special any = 0
		if status == "special" {
			special = 30
		}
		begin := fmt.Sprintf("%d-%02d-%02d", 2020+g.r.IntN(5), 1+g.r.IntN(12), 1+g.r.IntN(27))
		if subID == 40003 {
			begin = "1934-05-01" // typo date kept by Magic
		}
		g.add("Subscrip", s.id, 4111, person, usd, company, 101, loc, 1+g.r.IntN(40), svc, map[bool]int{true: 3, false: 1}[metered], 2,
			begin, freezed, false, active, avail, false, 0, special, "", "", g.phone(), "", nil, "")
		if status == "special" {
			s.fee = d(30)
		}
		all = append(all, s)
		return s
	}
	// company 3: 30 subscriptions (metered and fixed), company 11: 18, 6 persons subscribe to both
	var c3, c11 []subx
	for i := 0; i < 30; i++ {
		p := newCustomer()
		status := "active"
		switch i {
		case 4, 9:
			status = "suspended"
		case 14:
			status = "ended"
		case 20:
			status = "special"
		}
		loc := boxes3[i%len(boxes3)]
		if i%2 == 0 {
			svc := 33 + i%4/2
			base := map[int]int{33: 4, 34: 7}[svc]
			c3 = append(c3, mk(3, p, svc, loc, true, d(int64(base)), status))
		} else {
			svc := 31 + i%3%2
			c3 = append(c3, mk(3, p, svc, loc, false, map[int]decimal.Decimal{31: d(25), 32: d(45)}[svc], status))
		}
	}
	for i := 0; i < 18; i++ {
		var p int
		if i < 6 {
			p = c3[i*2+1].person // already an electricity customer
		} else {
			p = newCustomer()
		}
		svc := 111 + i%2
		fee := map[int]decimal.Decimal{111: d(20), 112: d(35)}[svc]
		status := "active"
		if i == 10 {
			status = "suspended"
		}
		c11 = append(c11, mk(11, p, svc, boxes11[i%2], false, fee, status))
	}
	_ = c11

	// monthly bills Jan..Oct 2026, most paid (USD, LBP, mixed with LBP change); the last months partly unpaid
	counters := map[int]decimal.Decimal{}
	for _, s := range all {
		counters[s.id] = d(int64(1000 + g.r.IntN(4000)))
	}
	billLine := func(company, person int, amt decimal.Decimal, date string) {
		g.post(company, 4111, person, usd, usd, date, amt, d(0))
		g.post(company, 711, 15, usd, usd, date, d(0), amt)
	}
	for m := 1; m <= 10; m++ {
		for si, s := range all {
			if s.id == 40015 && m > 6 { // the ended subscription stopped in June
				continue
			}
			issal[s.company]++
			date := fmt.Sprintf("%d-%02d-%02d", year, m, 1+g.r.IntN(5))
			due := fmt.Sprintf("%d-%02d-25", year, m)
			old, cost := counters[s.id], d(0)
			newc := old
			amount := s.fee
			if s.metered {
				used := d(int64(40 + g.r.IntN(260)))
				newc = old.Add(used)
				cost = decimal.RequireFromString("0.65")
				amount = s.fee.Add(used.Mul(cost)).Round(2)
				counters[s.id] = newc
			}
			discount := d(0)
			if si == 3 && m == 9 {
				discount = d(5)
			}
			due2 := amount.Sub(discount)
			paid := !(m >= 9 && si%4 == 0) && !(m == 10 && si%5 == 1) && !(s.id == 40015)
			if m < 6 && s.id == 40015 {
				paid = true
			}
			deleted := si == 7 && m == 4
			invoiced := date
			if si == 11 && m == 10 && !paid {
				invoiced = "5000-10-01" // typo date
			}
			var vUSD, vLBP, back decimal.Decimal
			if paid && !deleted {
				switch g.r.IntN(3) {
				case 0:
					vUSD = due2
				case 1:
					vLBP = due2.Mul(g.rate).Round(-3).Add(d(5000)) // pays in LBP, gets change
					back = vLBP.Sub(due2.Mul(g.rate)).Round(0)
				default:
					vUSD = due2.Floor()
					vLBP = due2.Sub(vUSD).Mul(g.rate).Round(0)
				}
			}
			var newS, oldS any = nil, nil
			if s.metered {
				newS, oldS = newc, old
			}
			g.add("DataCustomer", issal[s.company], year, s.company, s.id, m, paid && !deleted, newS, oldS, s.fee, cost, amount, discount,
				vUSD, vLBP, back, g.rate, invoiced, due, deleted, true, s.svc)
			if deleted {
				continue
			}
			billLine(s.company, s.person, due2, date)
			if !paid {
				continue
			}
			pay := fmt.Sprintf("%d-%02d-%02d", year, m, 10+g.r.IntN(15))
			if vUSD.IsPositive() {
				g.post(s.company, 531, 2, usd, usd, pay, vUSD, d(0))
				g.post(s.company, 4111, s.person, usd, usd, pay, d(0), vUSD)
			}
			if vLBP.IsPositive() {
				g.post(s.company, 531, 2, lbp, lbp, pay, vLBP, d(0))
				g.post(s.company, 4111, s.person, usd, lbp, pay, d(0), vLBP)
			}
			if back.IsPositive() {
				g.post(s.company, 4111, s.person, usd, lbp, pay, back, d(0))
				g.post(s.company, 531, 2, lbp, lbp, pay, d(0), back)
			}
		}
	}
	// a duplicate unpaid bill for the same month (Magic has no uniqueness check)
	dup := all[0]
	issal[3]++
	g.add("DataCustomer", issal[3], year, 3, dup.id, 10, false, nil, nil, dup.fee, 0, d(12), 0, 0, 0, 0, g.rate, "2026-10-28", "2026-11-05", false, true, dup.svc)
	billLine(3, dup.person, d(12), "2026-10-28")

	// partial payment on account (balance forward: the bill stays unpaid in Magic)
	pp := all[4]
	g.post(3, 531, 2, usd, usd, "2026-10-20", d(10), d(0))
	g.post(3, 4111, pp.person, usd, usd, "2026-10-20", d(0), d(10))
	// an advance (credit) paid by a customer with no unpaid bill
	adv := all[1]
	g.post(3, 531, 2, usd, usd, "2026-10-21", d(100), d(0))
	g.post(3, 4111, adv.person, usd, usd, "2026-10-21", d(0), d(100))
	// an old debtor without any subscription (previous balance, voucher 50)
	debtor := newCustomer()
	account(4111, debtor, usd, 3)
	g.post(3, 4111, debtor, usd, usd, "2023-06-01", d(250), d(0))
	g.post(3, 10, 20, usd, usd, "2023-06-01", d(0), d(250))
	// an LBP payment whose rate was typed as 1 (Magic then counts the LBP as dollars): repaired at the default rate
	bad := all[5]
	g.post(3, 531, 2, lbp, lbp, "2026-10-22", d(895000), d(0))
	l := mline{company: 3, ledger: 4111, person: bad.person, money: usd, lineMoney: lbp, date: "2026-10-22", db: d(0), cr: d(895000),
		dol: d(1), ll: d(1), visible: true}
	g.lines = append(g.lines, l)
	// supplier: fuel bought on credit, partly paid; another supplier prepaid (advance)
	account(401, 9001, usd, 3)
	account(601, 2, usd, 3)
	g.post(3, 601, 2, usd, usd, "2026-09-03", d(4200), d(0))
	g.post(3, 401, 9001, usd, usd, "2026-09-03", d(0), d(4200))
	g.post(3, 401, 9001, usd, usd, "2026-09-30", d(3000), d(0))
	g.post(3, 531, 2, usd, usd, "2026-09-30", d(0), d(3000))
	account(401, 9002, usd, 3)
	g.post(3, 401, 9002, usd, usd, "2026-10-02", d(300), d(0))
	g.post(3, 531, 2, usd, usd, "2026-10-02", d(0), d(300))
	// collector wages (staff account) and last year's result
	account(42, 5001, usd, 3)
	g.post(3, 6312, 5001, usd, usd, "2025-12-31", d(600), d(0))
	g.post(3, 42, 5001, usd, usd, "2025-12-31", d(0), d(600))
	g.post(3, 711, 15, usd, usd, "2025-12-31", d(0), d(1800))
	g.post(3, 531, 2, usd, usd, "2025-12-31", d(1800), d(0))
	// buy LBP with dollars (voucher 52) leaving LBP in the box
	g.post(3, 531, 2, lbp, lbp, "2026-10-25", d(44750000), d(0))
	g.post(3, 531, 2, usd, usd, "2026-10-25", d(0), d(500))
	// one-line manual journal (Magic does not enforce double entry)
	g.post(3, 531, 2, usd, usd, "2026-10-26", d(0), decimal.RequireFromString("7.50"))
	// hidden (cancelled) lines with amounts: Magic ignores them
	g.lines = append(g.lines, mline{company: 3, ledger: 4111, person: all[2].person, money: usd, lineMoney: usd, date: "2026-10-27",
		db: d(999), cr: d(0), dol: d(1), ll: d(1), visible: false})

	// ---- expected results ----
	g.exp.Rate = g.rate
	g.exp.Imbalance = map[int]decimal.Decimal{}
	for _, ln := range g.lines {
		if !ln.visible {
			continue
		}
		cur := map[int]string{usd: "USD", lbp: "LBP"}[ln.money]
		raw := ln.db.Sub(ln.cr)
		v := raw
		if ln.money != ln.lineMoney {
			if ln.money == usd {
				v = raw.DivRound(g.rate, 10) // includes the repaired line
			} else {
				v = raw.Mul(g.rate)
			}
		}
		usdv := raw
		if ln.lineMoney == lbp {
			usdv = raw.DivRound(g.rate, 10)
		}
		g.exp.Imbalance[ln.company] = g.exp.Imbalance[ln.company].Add(usdv)
		target := map[int]map[int]map[string]decimal.Decimal(nil)
		switch ln.ledger {
		case 4111:
			target = g.exp.Customers
		case 401:
			target = g.exp.Suppliers
		default:
			continue
		}
		if target[ln.company] == nil {
			target[ln.company] = map[int]map[string]decimal.Decimal{}
		}
		if target[ln.company][ln.person] == nil {
			target[ln.company][ln.person] = map[string]decimal.Decimal{}
		}
		target[ln.company][ln.person][cur] = target[ln.company][ln.person][cur].Add(v)
	}
	for c, m := range g.exp.Imbalance {
		g.exp.Imbalance[c] = m.Round(2)
	}
	for _, t := range []map[int]map[int]map[string]decimal.Decimal{g.exp.Customers, g.exp.Suppliers} {
		for _, pm := range t {
			for _, cm := range pm {
				for cur, v := range cm {
					cm[cur] = v.Round(Decimals(cur))
				}
			}
		}
	}

	// ---- the raw journal lines (Magic's Movement table), to test the export script's SQL against SQL Server.
	// The importer ignores this file: it reads MovementBalances only.
	g.table("Movement", "Annee", "MovementID", "LedgerID", "AccountNameID", "MoneyID", "CompanyID", "BranchID", "Date", "DB", "CR",
		"MoneyID_DBCR", "Details", "DolBource", "LLBource", "UserName", "VoucherID", "NumVoucher", "Visible")
	for i, ln := range g.lines {
		g.add("Movement", ln.date[:4], 1000000+i, ln.ledger, ln.person, ln.money, ln.company, ln.company, ln.date, ln.db, ln.cr,
			ln.lineMoney, "synthetic", ln.dol, ln.ll, "test", 2, i, ln.visible)
	}

	// ---- MovementBalances (what the export script's GROUP BY produces) ----
	g.table("MovementBalances", "CompanyID", "LedgerID", "AccountNameID", "MoneyID", "MoneyID_DBCR", "Yr", "AcctRateOK", "UsdRateOK", "Lines",
		"SumRaw", "SumUSD", "SumLBP", "MinDate", "MaxDate")
	type gk struct{ company, ledger, person, money, lineMoney, year, acctOK, usdOK int }
	type agg struct {
		lines         int
		raw, usd, lbp decimal.Decimal
		minD, maxD    string
	}
	groups := map[gk]*agg{}
	var keys []gk
	for _, ln := range g.lines {
		if !ln.visible {
			continue
		}
		acctOK, usdOK := 1, 1
		if ln.money != ln.lineMoney {
			if ln.money == usd && (ln.dol.LessThan(decimal.RequireFromString("0.000001")) || ln.dol.GreaterThan(decimal.RequireFromString("0.002"))) {
				acctOK = 0
			}
			if ln.money == lbp && (ln.ll.LessThan(d(500)) || ln.ll.GreaterThan(d(1000000))) {
				acctOK = 0
			}
		}
		if ln.lineMoney == lbp && (ln.dol.LessThan(decimal.RequireFromString("0.000001")) || ln.dol.GreaterThan(decimal.RequireFromString("0.002"))) {
			usdOK = 0
		}
		y, _ := strconv.Atoi(ln.date[:4])
		k := gk{ln.company, ln.ledger, ln.person, ln.money, ln.lineMoney, y, acctOK, usdOK}
		a, ok := groups[k]
		if !ok {
			a = &agg{minD: ln.date, maxD: ln.date}
			groups[k] = a
			keys = append(keys, k)
		}
		raw := ln.db.Sub(ln.cr)
		a.lines++
		a.raw = a.raw.Add(raw)
		a.usd = a.usd.Add(raw.Mul(ln.dol))
		a.lbp = a.lbp.Add(raw.Mul(ln.ll))
		if ln.date < a.minD {
			a.minD = ln.date
		}
		if ln.date > a.maxD {
			a.maxD = ln.date
		}
	}
	sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j]) })
	for _, k := range keys {
		a := groups[k]
		g.add("MovementBalances", k.company, k.ledger, k.person, k.money, k.lineMoney, k.year, k.acctOK, k.usdOK, a.lines,
			a.raw.Round(6), a.usd.Round(6), a.lbp.Round(6), a.minD, a.maxD)
	}
}

func d(v int64) decimal.Decimal { return decimal.NewFromInt(v) }

func (g *synth) zip() []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	tables := map[string]int{}
	for _, name := range g.order {
		w, _ := zw.Create(name + ".csv")
		w.Write([]byte("\ufeff"))
		cw := csv.NewWriter(w)
		cw.WriteAll(g.files[name])
		tables[name] = len(g.files[name]) - 1
	}
	m := Manifest{Format: FormatName, Version: FormatVersion, ExportedAt: time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC).Format(time.RFC3339),
		Database: "ManagmentDBElec (synthetic)", Months: 24, Tables: tables}
	b, _ := json.MarshalIndent(m, "", "  ")
	w, _ := zw.Create("manifest.json")
	w.Write(b)
	zw.Close()
	return buf.Bytes()
}
