package magic

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Options of one migration run.
type Options struct {
	OpeningDate    string          // YYYY-MM-DD: date of the opening balances (default today)
	Rate           decimal.Decimal // LBP per 1 USD used for opening balances and to repair bad rates (default Money.LLBource)
	ReadingsMonths int             // meter readings imported for the last N months (default 12; 0 = none)
	IncludeEnded   bool            // also import cancelled subscriptions (as "ended")
	Today          string          // for tests
}

// ---- the plan of one Magic company ----

type PStation struct{ Ref, Name string }
type PBox struct{ Ref, StationRef, Code, Location string }
type PArea struct{ Ref, Name, CollectorRef string }
type PCollector struct{ Ref, Name, Phone string }
type PPlan struct {
	Ref, Name, Currency string
	Metered             bool
	BaseFee, UnitPrice  decimal.Decimal
}
type PCustomer struct {
	Ref, Code, Name, Phone, Phone2, Email, Address, Notes, Status, AreaRef string
}
type PSub struct {
	Ref, CustomerRef, PlanRef, BoxRef, MeterNo, Start, Status, Notes string
	FeeOverride                                                      *decimal.Decimal
	Initial                                                          decimal.Decimal
}
type PReading struct {
	SubRef, Period string
	Value          decimal.Decimal
}
type PBill struct {
	Ref, CustomerRef, SubRef, Issue, Due, Currency, Description string
	Period                                                      string // "" = not linked to a billing period
	Amount                                                      decimal.Decimal
	Meta                                                        map[string]any
}

// POpen is a customer / supplier balance carried over. Amount > 0: the party owes us (customer) or
// we owe the party (supplier); for customers Amount < 0 is a credit.
type POpen struct {
	Ref, PartyRef, Currency string
	Amount                  decimal.Decimal
}
type PSupplier struct{ Ref, Code, Name, Phone, Address string }

// PGL is one line of the opening journal entry. Amount > 0 = debit.
type PGL struct {
	Kind     string // gl | ar_clearing | ap_clearing | retained | ap_direct
	Code     string // Magic ledger code (gl / ap_direct)
	NameEn   string
	NameAr   string
	NameFr   string
	Type     string // asset | liability | equity | income | expense
	Currency string
	Amount   decimal.Decimal
}

type Warning struct {
	Code     string   `json:"code"`
	Count    int      `json:"count"`
	Examples []string `json:"examples,omitempty"`
}

type Plan struct {
	Company     Company
	OpeningDate string
	Rate        decimal.Decimal
	Stations    []PStation
	Boxes       []PBox
	Areas       []PArea
	Collectors  []PCollector
	Plans       []PPlan
	Customers   []PCustomer
	Subs        []PSub
	Readings    []PReading
	Bills       []PBill
	CustOpen    []POpen
	Suppliers   []PSupplier
	SuppOpen    []POpen
	GL          []PGL
	// MagicImbalanceUSD: Σ(debit − credit) of all of this company's visible Magic journal lines in USD at the
	// rates Magic used. Non-zero when Magic holds unbalanced (one-line) entries.
	MagicImbalanceUSD decimal.Decimal
	warnings          map[string]*Warning
	order             []string
}

func (p *Plan) warn(code, example string) {
	if p.warnings == nil {
		p.warnings = map[string]*Warning{}
	}
	w, ok := p.warnings[code]
	if !ok {
		w = &Warning{Code: code}
		p.warnings[code] = w
		p.order = append(p.order, code)
	}
	w.Count++
	if example != "" && len(w.Examples) < 5 {
		w.Examples = append(w.Examples, example)
	}
}

func (p *Plan) Warnings() []Warning {
	out := make([]Warning, 0, len(p.order))
	for _, c := range p.order {
		out = append(out, *p.warnings[c])
	}
	return out
}

// Decimals of the currencies Magic uses (others: 2).
func Decimals(cur string) int32 {
	switch cur {
	case "LBP", "IQD", "JPY", "KRW", "VND", "IRR", "SYP":
		return 0
	}
	return 2
}

func tolerance(cur string) decimal.Decimal {
	return decimal.New(5, -(Decimals(cur) + 1)) // half of the smallest unit
}

func ref(kind string, id ...int) string {
	parts := make([]string, len(id))
	for i, v := range id {
		parts[i] = strconv.Itoa(v)
	}
	return "magic:" + kind + ":" + strings.Join(parts, ":")
}

// Ref helpers used by the writer.
func CompanyRef(c int) string { return ref("company", c) }

func (d *Data) companyOf(stationID int) int {
	if s, ok := d.Stations[stationID]; ok {
		if b, ok := d.Branches[s.BranchID]; ok {
			return b.CompanyID
		}
	}
	return 0
}

func (d *Data) currency(moneyID int) string {
	if m, ok := d.Money[moneyID]; ok {
		return m.Code
	}
	return ""
}

// DefaultRate is the LBP rate stored in Magic's Money table (Money.LLBource of USD), or 89,500.
func (d *Data) DefaultRate() decimal.Decimal {
	for _, m := range d.Money {
		if m.Code == "USD" && m.LLBource.GreaterThan(decimal.NewFromInt(100)) {
			return m.LLBource.Round(4)
		}
	}
	return decimal.NewFromInt(89500)
}

func validDate(s string, lo, hi string) bool { return s != "" && s >= lo && s <= hi }

// AccountType maps a Magic ledger to a Paxve account type: BalanceTypeID first (1-2 assets,
// 3-4 liabilities, 5 expense, 6 income), then the PCG class of the code.
func AccountType(code string, balanceType int) string {
	switch balanceType {
	case 1, 2:
		return "asset"
	case 3, 4:
		if strings.HasPrefix(code, "10") || strings.HasPrefix(code, "11") || strings.HasPrefix(code, "12") || strings.HasPrefix(code, "13") {
			return "equity"
		}
		return "liability"
	case 5:
		return "expense"
	case 6:
		return "income"
	}
	if code == "" {
		return "asset"
	}
	switch code[0] {
	case '1':
		if strings.HasPrefix(code, "16") || strings.HasPrefix(code, "17") || strings.HasPrefix(code, "18") || strings.HasPrefix(code, "15") {
			return "liability"
		}
		return "equity"
	case '2', '3', '5':
		return "asset"
	case '4':
		if strings.HasPrefix(code, "41") || strings.HasPrefix(code, "46") || strings.HasPrefix(code, "47") || strings.HasPrefix(code, "48") {
			return "asset"
		}
		return "liability"
	case '6':
		return "expense"
	case '7':
		return "income"
	}
	return "asset"
}

// Build computes the migration plan of one Magic company.
func Build(d *Data, companyID int, opt Options) (*Plan, error) {
	co, ok := d.Companies[companyID]
	if !ok {
		return nil, fmt.Errorf("unknown_company: %d", companyID)
	}
	today := opt.Today
	if today == "" {
		today = time.Now().Format(time.DateOnly)
	}
	if opt.OpeningDate == "" {
		opt.OpeningDate = today
	}
	if !opt.Rate.IsPositive() {
		opt.Rate = d.DefaultRate()
	}
	if opt.ReadingsMonths < 0 {
		opt.ReadingsMonths = 0
	}
	p := &Plan{Company: *co, OpeningDate: opt.OpeningDate, Rate: opt.Rate}
	maxDate := time.Now().AddDate(2, 0, 0).Format(time.DateOnly)
	if t, err := time.Parse(time.DateOnly, opt.OpeningDate); err == nil {
		maxDate = t.AddDate(2, 0, 0).Format(time.DateOnly)
	}
	const minDate = "1995-01-01"

	// ---- people & contact details ----
	info := func(person, typ int) string {
		if v := d.Info[person][typ]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	personName := func(id int) string {
		if pp, ok := d.People[id]; ok && strings.TrimSpace(pp.Name) != "" {
			return strings.TrimSpace(pp.Name)
		}
		return fmt.Sprintf("Customer %d", id)
	}

	// ---- collectors (tenant-wide users, one per person) ----
	collectorRef := map[int]string{} // CollectorID → user ref
	seenCollector := map[int]bool{}
	ids := make([]int, 0, len(d.Collectors))
	for id := range d.Collectors {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		c := d.Collectors[id]
		if c.CompanyID != companyID || c.PersonID == 0 {
			continue
		}
		r := ref("collector", c.PersonID)
		collectorRef[c.ID] = r
		if seenCollector[c.PersonID] {
			continue
		}
		seenCollector[c.PersonID] = true
		name := strings.TrimSpace(c.Name)
		if name == "" {
			name = personName(c.PersonID)
		}
		p.Collectors = append(p.Collectors, PCollector{Ref: r, Name: name, Phone: info(c.PersonID, 1)})
	}

	// ---- subscriptions of this company ----
	var subs []*Sub
	for _, s := range d.Subs {
		if s.CompanyID != companyID {
			continue
		}
		if !s.Available && !opt.IncludeEnded {
			continue
		}
		subs = append(subs, s)
	}
	sort.Slice(subs, func(i, j int) bool { return subs[i].ID < subs[j].ID })

	// ---- stations, boxes, areas: every location of the company's stations + every location used ----
	usedLoc := map[int]bool{}
	for _, s := range subs {
		usedLoc[s.LocationID] = true
	}
	var locIDs []int
	for id, l := range d.Locations {
		if usedLoc[id] || d.companyOf(l.StationID) == companyID {
			locIDs = append(locIDs, id)
		}
	}
	sort.Ints(locIDs)
	stationDone := map[int]bool{}
	stationNames := map[string]int{}
	boxCodes := map[string]int{}
	areaNames := map[string]int{}
	unique := func(seen map[string]int, name string, id int) string {
		key := strings.ToLower(name)
		if _, dup := seen[key]; dup {
			name = fmt.Sprintf("%s (#%d)", name, id)
			key = strings.ToLower(name)
		}
		seen[key] = id
		return name
	}
	locBox, locArea := map[int]string{}, map[int]string{}
	for _, id := range locIDs {
		l := d.Locations[id]
		st := l.StationID
		if !stationDone[st] {
			stationDone[st] = true
			name := fmt.Sprintf("Station %d", st)
			if s, ok := d.Stations[st]; ok && strings.Trim(s.Name, "- ") != "" {
				name = strings.TrimSpace(s.Name)
			}
			p.Stations = append(p.Stations, PStation{Ref: ref("station", st), Name: unique(stationNames, name, st)})
		}
		name := strings.TrimSpace(l.Name)
		if name == "" {
			name = fmt.Sprintf("Box %d", l.ID)
		}
		b := PBox{Ref: ref("box", l.ID), StationRef: ref("station", st), Code: unique(boxCodes, name, l.ID), Location: l.Address}
		p.Boxes = append(p.Boxes, b)
		a := PArea{Ref: ref("area", l.ID), Name: unique(areaNames, name, l.ID)}
		if cr, ok := collectorRef[l.DefaultCollectorID]; ok {
			a.CollectorRef = cr
		} else if c, ok := d.Collectors[l.DefaultCollectorID]; ok && c.PersonID != 0 {
			// default collector registered under another company: still the same person
			a.CollectorRef = ref("collector", c.PersonID)
			if !seenCollector[c.PersonID] {
				seenCollector[c.PersonID] = true
				nm := strings.TrimSpace(c.Name)
				if nm == "" {
					nm = personName(c.PersonID)
				}
				p.Collectors = append(p.Collectors, PCollector{Ref: a.CollectorRef, Name: nm, Phone: info(c.PersonID, 1)})
			}
		}
		p.Areas = append(p.Areas, a)
		locBox[l.ID], locArea[l.ID] = b.Ref, a.Ref
	}

	// ---- plans ----
	usedSvc := map[int]bool{}
	for _, s := range subs {
		usedSvc[s.ServiceID] = true
	}
	var svcIDs []int
	for id, s := range d.Services {
		if s.CompanyID == companyID && (s.Available || usedSvc[id]) {
			svcIDs = append(svcIDs, id)
		}
	}
	sort.Ints(svcIDs)
	planNames := map[string]int{}
	planOf := map[int]string{}
	metered := map[int]bool{}
	baseCur := "USD"
	for _, id := range svcIDs {
		s := d.Services[id]
		name := strings.TrimSpace(s.Name)
		if name == "" {
			name = fmt.Sprintf("Plan %d", id)
		}
		pl := PPlan{Ref: ref("plan", id), Name: unique(planNames, name, id), Currency: baseCur, Metered: s.TypeInscriptionID == 3}
		if pl.Metered {
			pl.BaseFee, pl.UnitPrice = nonNeg(s.BaseCost).Round(4), nonNeg(s.Cost).Round(4)
		} else {
			fee := s.Cost
			if s.NetCost.IsPositive() && s.Discount.IsPositive() {
				fee = s.NetCost
			}
			pl.BaseFee = nonNeg(fee).Round(4)
		}
		p.Plans = append(p.Plans, pl)
		planOf[id] = pl.Ref
		metered[id] = pl.Metered
	}

	// ---- balances: which ledgers hold customers / suppliers ----
	custLedger := map[int]bool{}
	for _, s := range d.Subs {
		if s.CompanyID == companyID && s.LedgerID != 0 {
			custLedger[s.LedgerID] = true
		}
	}
	for id := range d.Ledgers {
		if strings.HasPrefix(strconv.Itoa(id), "411") {
			custLedger[id] = true
		}
	}
	isSupplier := func(ledger int) bool { return strings.HasPrefix(strconv.Itoa(ledger), "401") }

	rate := opt.Rate
	type key struct {
		ledger, person int
		cur            string
	}
	acctBal := map[key]decimal.Decimal{}
	type glKey struct {
		ledger int
		cur    string
		before bool // P&L before the opening year (folded into retained earnings)
	}
	glBal := map[glKey]decimal.Decimal{}
	openYear, _ := strconv.Atoi(opt.OpeningDate[:4])
	thisYear := time.Now().Year()
	for _, b := range d.Balances {
		if b.CompanyID != companyID {
			continue
		}
		acur, lcur := d.currency(b.MoneyID), d.currency(b.LineMoneyID)
		if acur == "" || lcur == "" {
			p.warn("unknown_currency_lines", fmt.Sprintf("ledger %d person %d money %d/%d", b.LedgerID, b.PersonID, b.MoneyID, b.LineMoneyID))
			continue
		}
		if b.Year < 1995 || b.Year > thisYear+1 {
			p.warn("odd_year_lines", fmt.Sprintf("%d (%d lines, ledger %d)", b.Year, b.Lines, b.LedgerID))
		}
		// value in the account's own currency (Magic: Σ(DB−CR)·DolBource for USD accounts, ·LLBource for LBP accounts)
		var v decimal.Decimal
		switch {
		case acur == lcur:
			v = b.Raw
		case b.AcctOK && acur == "USD":
			v = b.USD
		case b.AcctOK && acur == "LBP":
			v = b.LBP
		case acur == "USD" && lcur == "LBP":
			v = b.Raw.DivRound(rate, 8)
			p.warn("bad_rate_repaired", fmt.Sprintf("ledger %d person %d year %d: %s LBP", b.LedgerID, b.PersonID, b.Year, b.Raw.Round(0)))
		case acur == "LBP" && lcur == "USD":
			v = b.Raw.Mul(rate)
			p.warn("bad_rate_repaired", fmt.Sprintf("ledger %d person %d year %d: %s USD", b.LedgerID, b.PersonID, b.Year, b.Raw.Round(2)))
		default:
			v = b.Raw
			p.warn("unsupported_currency_pair", fmt.Sprintf("%s lines in a %s account (ledger %d)", lcur, acur, b.LedgerID))
		}
		// Magic's own USD value of the lines, for the balance check
		switch {
		case b.USDOK:
			p.MagicImbalanceUSD = p.MagicImbalanceUSD.Add(b.USD)
		case lcur == "USD":
			p.MagicImbalanceUSD = p.MagicImbalanceUSD.Add(b.Raw)
		case lcur == "LBP":
			p.MagicImbalanceUSD = p.MagicImbalanceUSD.Add(b.Raw.DivRound(rate, 8))
		}
		if custLedger[b.LedgerID] || isSupplier(b.LedgerID) {
			acctBal[key{b.LedgerID, b.PersonID, acur}] = acctBal[key{b.LedgerID, b.PersonID, acur}].Add(v)
			continue
		}
		code := strconv.Itoa(b.LedgerID)
		bt := 0
		if l, ok := d.Ledgers[b.LedgerID]; ok {
			bt = l.BalanceType
		}
		t := AccountType(code, bt)
		before := (t == "income" || t == "expense") && b.Year < openYear
		glBal[glKey{b.LedgerID, acur, before}] = glBal[glKey{b.LedgerID, acur, before}].Add(v)
	}
	p.MagicImbalanceUSD = p.MagicImbalanceUSD.Round(2)

	// customer balance per person and currency (all customer ledgers together)
	type pc struct {
		person int
		cur    string
	}
	custBal := map[pc]decimal.Decimal{}
	suppBal := map[pc]decimal.Decimal{}
	for k, v := range acctBal {
		if custLedger[k.ledger] {
			custBal[pc{k.person, k.cur}] = custBal[pc{k.person, k.cur}].Add(v)
		} else {
			suppBal[pc{k.person, k.cur}] = suppBal[pc{k.person, k.cur}].Add(v)
		}
	}
	for k, v := range custBal {
		custBal[k] = v.Round(Decimals(k.cur))
	}
	for k, v := range suppBal {
		suppBal[k] = v.Round(Decimals(k.cur))
	}

	// ---- customers: subscribers + anyone with a customer balance ----
	custSubs := map[int][]*Sub{}
	for _, s := range subs {
		custSubs[s.PersonID] = append(custSubs[s.PersonID], s)
	}
	custIDs := map[int]bool{}
	for id := range custSubs {
		custIDs[id] = true
	}
	for k, v := range custBal {
		if !v.IsZero() {
			custIDs[k.person] = true
		}
	}
	var people []int
	for id := range custIDs {
		people = append(people, id)
	}
	sort.Ints(people)
	subStatus := func(s *Sub) string {
		switch {
		case !s.Available:
			return "ended"
		case !s.Active || s.Freezed || s.FreezedNoCharge:
			return "suspended"
		}
		return "active"
	}
	custRef := map[int]string{}
	for _, id := range people {
		c := PCustomer{Ref: ref("customer", id), Code: strconv.Itoa(id), Name: personName(id),
			Phone: info(id, 1), Phone2: info(id, 2), Email: strings.ToLower(info(id, 6)), Address: info(id, 7), Notes: info(id, 8)}
		if c.Phone2 == "" {
			c.Phone2 = info(id, 3)
		}
		ss := custSubs[id]
		status := "closed"
		for _, s := range ss {
			if c.Phone == "" && s.CellPhone != "" {
				c.Phone = s.CellPhone
			}
			if c.Phone2 == "" && s.HomePhone != "" {
				c.Phone2 = s.HomePhone
			}
			if c.Address == "" && s.Address != "" {
				c.Address = s.Address
			}
			switch subStatus(s) {
			case "active":
				status = "active"
			case "suspended":
				if status != "active" {
					status = "suspended"
				}
			}
		}
		if len(ss) == 0 {
			status = "active" // a debtor without a subscription: keep visible
			if pp, ok := d.People[id]; ok && !pp.Available {
				status = "closed"
			}
		}
		c.Status = status
		// area = box of the first active subscription (else the first one)
		for _, want := range []string{"active", "suspended", "ended"} {
			if c.AreaRef != "" {
				break
			}
			for _, s := range ss {
				if subStatus(s) == want {
					c.AreaRef = locArea[s.LocationID]
					break
				}
			}
		}
		if len(c.Phone) > 50 {
			c.Phone = c.Phone[:50]
		}
		p.Customers = append(p.Customers, c)
		custRef[id] = c.Ref
	}

	// ---- subscriptions, initial readings ----
	subRef := map[int]string{}
	subCur := map[int]string{}
	firstOld := map[int]decimal.Decimal{}
	firstOldPeriod := map[int]string{}
	for _, b := range d.Bills {
		if b.CompanyID != companyID || b.Deleted || !b.HasOld {
			continue
		}
		per := fmt.Sprintf("%04d-%02d", b.Year, b.Month)
		if old, ok := firstOldPeriod[b.SubID]; !ok || per < old {
			firstOldPeriod[b.SubID] = per
			firstOld[b.SubID] = b.OldCounter
		}
	}
	for _, s := range subs {
		pl, ok := planOf[s.ServiceID]
		if !ok {
			p.warn("plan_missing", fmt.Sprintf("sub %d plan %d", s.ID, s.ServiceID))
			continue
		}
		x := PSub{Ref: ref("sub", s.ID), CustomerRef: custRef[s.PersonID], PlanRef: pl, BoxRef: locBox[s.LocationID], Status: subStatus(s)}
		if x.BoxRef == "" && s.LocationID != 0 {
			p.warn("box_missing", fmt.Sprintf("sub %d box %d", s.ID, s.LocationID))
		}
		x.Start = s.Begin
		if !validDate(x.Start, minDate, maxDate) {
			if s.Begin != "" {
				p.warn("bad_start_date", fmt.Sprintf("sub %d: %s", s.ID, s.Begin))
			}
			x.Start = opt.OpeningDate
		}
		if metered[s.ServiceID] {
			x.MeterNo = strconv.Itoa(s.ID)
			if v := firstOld[s.ID]; v.IsPositive() {
				x.Initial = v.Round(2)
			}
			if s.SpecialBaseCost.IsPositive() {
				f := s.SpecialBaseCost.Round(4)
				x.FeeOverride = &f
			}
			if s.SpecialCost.IsPositive() {
				p.warn("special_unit_price_ignored", fmt.Sprintf("sub %d: %s per unit", s.ID, s.SpecialCost))
			}
		} else if s.SpecialCost.IsPositive() {
			f := s.SpecialCost.Round(4)
			x.FeeOverride = &f
		}
		notes := []string{fmt.Sprintf("Imported subscription #%d", s.ID)}
		if s.NumInLocation > 0 && s.NumInLocation < 10000 {
			notes = append(notes, fmt.Sprintf("box position %d", s.NumInLocation))
		}
		if s.UserNet != "" {
			notes = append(notes, "user "+s.UserNet)
		}
		if s.Maktab {
			notes = append(notes, "pays at the office")
		}
		if s.Note != "" {
			notes = append(notes, s.Note)
		}
		x.Notes = strings.Join(notes, " · ")
		p.Subs = append(p.Subs, x)
		subRef[s.ID] = x.Ref
		subCur[s.ID] = d.currency(s.MoneyID)
		if subCur[s.ID] == "" {
			subCur[s.ID] = baseCur
		}
	}
	subPerson := map[int]int{}
	for _, s := range subs {
		subPerson[s.ID] = s.PersonID
	}

	// ---- meter readings of the last N months ----
	if opt.ReadingsMonths > 0 {
		t, _ := time.Parse(time.DateOnly, opt.OpeningDate)
		from := t.AddDate(0, -opt.ReadingsMonths, 0).Format("2006-01")
		type rk struct {
			sub    int
			period string
		}
		best := map[rk]*Bill{}
		for _, b := range d.Bills {
			if b.CompanyID != companyID || b.Deleted || !b.HasNew || !metered[serviceOfSub(subs, b.SubID)] {
				continue
			}
			if _, ok := subRef[b.SubID]; !ok || b.Month < 1 || b.Month > 12 {
				continue
			}
			per := fmt.Sprintf("%04d-%02d", b.Year, b.Month)
			if per < from || per > opt.OpeningDate[:7] {
				continue
			}
			k := rk{b.SubID, per}
			if old, ok := best[k]; !ok || b.IssalID > old.IssalID {
				best[k] = b
			}
		}
		keys := make([]rk, 0, len(best))
		for k := range best {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if keys[i].sub != keys[j].sub {
				return keys[i].sub < keys[j].sub
			}
			return keys[i].period < keys[j].period
		})
		last := map[int]decimal.Decimal{}
		for _, k := range keys {
			v := best[k].NewCounter
			if v.IsNegative() {
				continue
			}
			if prev, ok := last[k.sub]; ok && v.LessThan(prev) {
				p.warn("reading_below_previous", fmt.Sprintf("sub %d %s: %s < %s", k.sub, k.period, v, prev))
				continue
			}
			last[k.sub] = v
			p.Readings = append(p.Readings, PReading{SubRef: subRef[k.sub], Period: k.period, Value: v.Round(2)})
		}
	}

	// ---- unpaid bills ----
	openSum := map[pc]decimal.Decimal{}
	periodUsed := map[string]bool{}
	var open []*Bill
	for _, b := range d.Bills {
		if b.CompanyID != companyID || b.Paid || b.Deleted || !b.Visible {
			continue
		}
		open = append(open, b)
	}
	sort.Slice(open, func(i, j int) bool {
		if open[i].Year != open[j].Year {
			return open[i].Year < open[j].Year
		}
		return open[i].IssalID < open[j].IssalID
	})
	for _, b := range open {
		person, ok := subPerson[b.SubID]
		if !ok {
			// bill of a subscription that is not imported (e.g. cancelled): the debt stays in the opening balance
			p.warn("bill_without_subscription", fmt.Sprintf("bill %d/%d sub %d", b.Year, b.IssalID, b.SubID))
			continue
		}
		cur := subCur[b.SubID]
		amt := b.FinalCost.Sub(b.Discount).Round(Decimals(cur))
		if !amt.IsPositive() {
			continue
		}
		per := ""
		if b.Month >= 1 && b.Month <= 12 && b.Year >= 1995 && b.Year <= thisYear+1 {
			per = fmt.Sprintf("%04d-%02d", b.Year, b.Month)
		}
		issue := b.Invoiced
		if !validDate(issue, minDate, maxDate) {
			if per != "" {
				issue = per + "-01"
			} else {
				issue = opt.OpeningDate
			}
			p.warn("bad_bill_date", fmt.Sprintf("bill %d/%d: %q", b.Year, b.IssalID, b.Invoiced))
		}
		due := b.MustPay
		if !validDate(due, issue, maxDate) {
			due = issue
		}
		x := PBill{Ref: ref("bill", b.CompanyID, b.Year, b.IssalID), CustomerRef: custRef[person], SubRef: subRef[b.SubID],
			Issue: issue, Due: due, Currency: cur, Amount: amt,
			Meta: map[string]any{"magic_bill": fmt.Sprintf("%d/%d", b.Year, b.IssalID)}}
		key := x.SubRef + "|" + per
		if per != "" && !periodUsed[key] {
			x.Period = per
			periodUsed[key] = true
		} else if per != "" {
			p.warn("duplicate_bill_period", fmt.Sprintf("sub %d %s (bill %d)", b.SubID, per, b.IssalID))
		}
		desc := fmt.Sprintf("Opening bill %d/%d", b.Year, b.IssalID)
		if per != "" {
			desc += " · " + per
		}
		if b.HasNew && b.HasOld && b.NewCounter.GreaterThan(b.OldCounter) {
			desc += fmt.Sprintf(" · %s → %s", b.OldCounter.Round(2), b.NewCounter.Round(2))
			x.Meta["previous"], x.Meta["current"] = b.OldCounter.Round(2).String(), b.NewCounter.Round(2).String()
		}
		if b.Discount.IsPositive() {
			x.Meta["discount"] = b.Discount.Round(Decimals(cur)).String()
		}
		x.Description = desc
		p.Bills = append(p.Bills, x)
		openSum[pc{person, cur}] = openSum[pc{person, cur}].Add(amt)
	}

	// ---- customer opening balances = Magic balance − unpaid bills imported ----
	arByCur := map[string]decimal.Decimal{}
	for _, b := range p.Bills {
		arByCur[b.Currency] = arByCur[b.Currency].Add(b.Amount)
	}
	keys := map[pc]bool{}
	for k := range custBal {
		keys[k] = true
	}
	for k := range openSum {
		keys[k] = true
	}
	var pcs []pc
	for k := range keys {
		pcs = append(pcs, k)
	}
	sort.Slice(pcs, func(i, j int) bool {
		if pcs[i].person != pcs[j].person {
			return pcs[i].person < pcs[j].person
		}
		return pcs[i].cur < pcs[j].cur
	})
	for _, k := range pcs {
		diff := custBal[k].Sub(openSum[k])
		if diff.Abs().LessThan(tolerance(k.cur)) {
			continue
		}
		cr, ok := custRef[k.person]
		if !ok {
			continue
		}
		p.CustOpen = append(p.CustOpen, POpen{Ref: ref("opening", companyID, k.person) + ":" + k.cur, PartyRef: cr, Currency: k.cur, Amount: diff})
		arByCur[k.cur] = arByCur[k.cur].Add(diff)
		if diff.IsNegative() && openSum[k].IsPositive() {
			p.warn("partial_payments_applied", fmt.Sprintf("customer %d: %s %s", k.person, diff.Neg(), k.cur))
		}
	}

	// ---- suppliers ----
	suppIDs := map[int]bool{}
	for k, v := range suppBal {
		if !v.IsZero() {
			suppIDs[k.person] = true
		}
	}
	for _, a := range d.Accounts {
		if a.CompanyID == companyID && isSupplier(a.LedgerID) && a.Available {
			suppIDs[a.PersonID] = true
		}
	}
	var sids []int
	for id := range suppIDs {
		sids = append(sids, id)
	}
	sort.Ints(sids)
	apByCur := map[string]decimal.Decimal{}
	apDirect := map[string]decimal.Decimal{}
	suppRef := map[int]string{}
	for _, id := range sids {
		s := PSupplier{Ref: ref("supplier", id), Code: strconv.Itoa(id), Name: personName(id), Phone: info(id, 1), Address: info(id, 7)}
		p.Suppliers = append(p.Suppliers, s)
		suppRef[id] = s.Ref
	}
	var spcs []pc
	for k := range suppBal {
		spcs = append(spcs, k)
	}
	sort.Slice(spcs, func(i, j int) bool {
		if spcs[i].person != spcs[j].person {
			return spcs[i].person < spcs[j].person
		}
		return spcs[i].cur < spcs[j].cur
	})
	for _, k := range spcs {
		v := suppBal[k] // debit − credit: negative = we owe the supplier
		if v.Abs().LessThan(tolerance(k.cur)) {
			continue
		}
		if v.IsNegative() {
			p.SuppOpen = append(p.SuppOpen, POpen{Ref: ref("supplier-opening", companyID, k.person) + ":" + k.cur,
				PartyRef: suppRef[k.person], Currency: k.cur, Amount: v.Neg()})
			apByCur[k.cur] = apByCur[k.cur].Add(v.Neg())
		} else {
			apDirect[k.cur] = apDirect[k.cur].Add(v)
			p.warn("supplier_advance_in_gl", fmt.Sprintf("supplier %d: %s %s", k.person, v, k.cur))
		}
	}

	// ---- the opening journal entry ----
	var glKeys []glKey
	for k := range glBal {
		glKeys = append(glKeys, k)
	}
	sort.Slice(glKeys, func(i, j int) bool {
		if glKeys[i].ledger != glKeys[j].ledger {
			return strconv.Itoa(glKeys[i].ledger) < strconv.Itoa(glKeys[j].ledger)
		}
		if glKeys[i].cur != glKeys[j].cur {
			return glKeys[i].cur < glKeys[j].cur
		}
		return !glKeys[i].before
	})
	retained := map[string]decimal.Decimal{}
	for _, k := range glKeys {
		v := glBal[k].Round(Decimals(k.cur))
		if v.Abs().LessThan(tolerance(k.cur)) {
			continue
		}
		if k.before {
			retained[k.cur] = retained[k.cur].Add(v)
			continue
		}
		code := strconv.Itoa(k.ledger)
		g := PGL{Kind: "gl", Code: code, Currency: k.cur, Amount: v}
		if l, ok := d.Ledgers[k.ledger]; ok {
			g.NameEn, g.NameAr, g.NameFr = strings.TrimSpace(l.NameEn), strings.TrimSpace(l.NameAr), strings.TrimSpace(l.NameFr)
			g.Type = AccountType(code, l.BalanceType)
		} else {
			g.Type = AccountType(code, 0)
			p.warn("ledger_missing", code)
		}
		p.GL = append(p.GL, g)
	}
	for _, cur := range sortedKeys(retained) {
		if v := retained[cur]; !v.IsZero() {
			p.GL = append(p.GL, PGL{Kind: "retained", Currency: cur, Amount: v, Type: "equity"})
		}
	}
	for _, cur := range sortedKeys(arByCur) {
		if v := arByCur[cur]; !v.IsZero() {
			p.GL = append(p.GL, PGL{Kind: "ar_clearing", Currency: cur, Amount: v})
		}
	}
	for _, cur := range sortedKeys(apByCur) {
		if v := apByCur[cur]; !v.IsZero() {
			p.GL = append(p.GL, PGL{Kind: "ap_clearing", Currency: cur, Amount: v.Neg()})
		}
	}
	for _, cur := range sortedKeys(apDirect) {
		if v := apDirect[cur]; !v.IsZero() {
			p.GL = append(p.GL, PGL{Kind: "ap_direct", Currency: cur, Amount: v})
		}
	}
	if !p.MagicImbalanceUSD.IsZero() {
		p.warn("magic_books_unbalanced", p.MagicImbalanceUSD.String()+" USD")
	}
	return p, nil
}

func serviceOfSub(subs []*Sub, id int) int {
	i := sort.Search(len(subs), func(i int) bool { return subs[i].ID >= id })
	if i < len(subs) && subs[i].ID == id {
		return subs[i].ServiceID
	}
	return 0
}

func nonNeg(d decimal.Decimal) decimal.Decimal {
	if d.IsNegative() {
		return decimal.Zero
	}
	return d
}

func sortedKeys(m map[string]decimal.Decimal) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---- summary for the dry run ----

type Summary struct {
	CompanyID   int            `json:"company_id"`
	Name        string         `json:"name"`
	TypeID      int            `json:"type_id"`
	Counts      map[string]int `json:"counts"`
	Totals      map[string]any `json:"totals"`
	Warnings    []Warning      `json:"warnings"`
	Subscribers map[string]int `json:"subscriptions_by_status"`
	Currencies  []string       `json:"currencies"`
	OpeningDate string         `json:"opening_date"`
	Rate        string         `json:"rate"`
}

func sumByCur[T any](xs []T, cur func(T) string, amt func(T) decimal.Decimal) map[string]string {
	m := map[string]decimal.Decimal{}
	for _, x := range xs {
		m[cur(x)] = m[cur(x)].Add(amt(x))
	}
	out := map[string]string{}
	for k, v := range m {
		out[k] = v.String()
	}
	return out
}

func (p *Plan) Summary() Summary {
	s := Summary{CompanyID: p.Company.ID, Name: p.Company.Name, TypeID: p.Company.TypeID, OpeningDate: p.OpeningDate,
		Rate: p.Rate.String(), Warnings: p.Warnings(), Subscribers: map[string]int{}}
	if s.Warnings == nil {
		s.Warnings = []Warning{}
	}
	metered := 0
	for _, pl := range p.Plans {
		if pl.Metered {
			metered++
		}
	}
	s.Counts = map[string]int{"stations": len(p.Stations), "boxes": len(p.Boxes), "areas": len(p.Areas),
		"collectors": len(p.Collectors), "plans": len(p.Plans), "metered_plans": metered, "customers": len(p.Customers),
		"subscriptions": len(p.Subs), "readings": len(p.Readings), "open_bills": len(p.Bills),
		"customer_openings": len(p.CustOpen), "suppliers": len(p.Suppliers), "supplier_openings": len(p.SuppOpen),
		"gl_lines": len(p.GL)}
	for _, x := range p.Subs {
		s.Subscribers[x.Status]++
	}
	debit, credit := []POpen{}, []POpen{}
	for _, o := range p.CustOpen {
		if o.Amount.IsPositive() {
			debit = append(debit, o)
		} else {
			credit = append(credit, POpen{Currency: o.Currency, Amount: o.Amount.Neg()})
		}
	}
	cur := func(o POpen) string { return o.Currency }
	amt := func(o POpen) decimal.Decimal { return o.Amount }
	ar := map[string]decimal.Decimal{}
	for _, g := range p.GL {
		if g.Kind == "ar_clearing" {
			ar[g.Currency] = ar[g.Currency].Add(g.Amount)
		}
	}
	arS := map[string]string{}
	for k, v := range ar {
		arS[k] = v.String()
	}
	glDr, glCr := map[string]decimal.Decimal{}, map[string]decimal.Decimal{}
	curSet := map[string]bool{}
	for _, g := range p.GL {
		curSet[g.Currency] = true
		if g.Amount.IsPositive() {
			glDr[g.Currency] = glDr[g.Currency].Add(g.Amount)
		} else {
			glCr[g.Currency] = glCr[g.Currency].Add(g.Amount.Neg())
		}
	}
	gl := map[string]any{}
	for c := range curSet {
		gl[c] = map[string]string{"debit": glDr[c].String(), "credit": glCr[c].String()}
		s.Currencies = append(s.Currencies, c)
	}
	sort.Strings(s.Currencies)
	s.Totals = map[string]any{
		"open_bills":          sumByCur(p.Bills, func(b PBill) string { return b.Currency }, func(b PBill) decimal.Decimal { return b.Amount }),
		"customer_debits":     sumByCur(debit, cur, amt),
		"customer_credits":    sumByCur(credit, cur, amt),
		"receivables":         arS,
		"supplier_balances":   sumByCur(p.SuppOpen, cur, amt),
		"opening_entry":       gl,
		"magic_imbalance_usd": p.MagicImbalanceUSD.String(),
	}
	return s
}
