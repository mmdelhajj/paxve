package httpapi

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Payroll rules (module U). Every statutory figure is a parameter with an effective date. The country
// defaults below are the starting point; a company overrides any of them in payroll_params (key, value,
// effective_from) and the payroll of a month uses the values in force on its last day.
// Sources and caveats: docs/PAYROLL.md.

type ruleParam struct {
	key, value, from string // from: YYYY-MM-DD
}

var payrollDefaults = map[string][]ruleParam{
	// Lebanon (amounts in LBP). NSSF: sickness & maternity 8% employer + 3% employee up to a monthly ceiling;
	// family allowances 6% employer up to their own ceiling; end of service 8.5% employer, no ceiling.
	"LB": {
		{"legal_currency", "LBP", "2000-01-01"},
		{"social_label", "NSSF", "2000-01-01"},
		{"social_ee_rate", "3", "2000-01-01"},
		{"social_er_rate", "8", "2000-01-01"},
		{"social_ceiling", "140000000", "2025-01-01"},
		{"social_ceiling", "120000000", "2025-08-01"}, // NSSF memo 805 (22 Aug 2025)
		{"family_er_rate", "6", "2000-01-01"},
		{"family_ceiling", "18000000", "2025-01-01"},
		{"family_ceiling", "28000000", "2026-05-01"}, // Decree 2923
		{"eos_er_rate", "8.5", "2000-01-01"},
		{"family_allow_spouse", "2100000", "2026-05-01"},
		{"family_allow_child", "1155000", "2026-05-01"},
		{"family_allow_max_children", "5", "2000-01-01"},
		{"tax_brackets", "360000000:2,900000000:4,1800000000:7,3600000000:11,7200000000:15,13500000000:20,:25", "2024-01-01"},
		{"tax_ded_self", "450000000", "2024-01-01"},
		{"tax_ded_spouse", "225000000", "2024-01-01"},
		{"tax_ded_child", "45000000", "2024-01-01"},
		{"tax_ded_max_children", "5", "2000-01-01"},
		{"tax_rounding", "1000", "2000-01-01"},
		{"min_wage", "28000000", "2025-08-01"},        // Decree 699
		{"transport_per_day", "500000", "2026-10-01"}, // Decree 4021: linked to the price of fuel; check the current amount
		{"overtime_rate", "1.5", "2000-01-01"},
		{"month_days", "26", "2000-01-01"},
		{"month_hours", "208", "2000-01-01"},
		{"gratuity", "nssf", "2000-01-01"},
	},
	// Saudi Arabia (SAR). GOSI for Saudi nationals: annuities 9% + 9%, SANED 0.75% + 0.75%, occupational hazards
	// 2% employer; other nationals: occupational hazards 2% employer. Contribution base = basic + housing, capped.
	"SA": {
		{"legal_currency", "SAR", "2000-01-01"},
		{"social_label", "GOSI", "2000-01-01"},
		{"social_nationals", "SA", "2000-01-01"},
		{"social_ee_rate", "9.75", "2000-01-01"},
		{"social_er_rate", "11.75", "2000-01-01"},
		{"social_er_rate_foreign", "2", "2000-01-01"},
		{"social_ceiling", "45000", "2000-01-01"},
		{"social_includes_housing", "1", "2000-01-01"},
		{"overtime_rate", "1.5", "2000-01-01"},
		{"month_days", "30", "2000-01-01"},
		{"month_hours", "208", "2000-01-01"},
		{"gratuity", "ksa", "2000-01-01"},
	},
	// United Arab Emirates (AED). Pension (GPSSA) for UAE nationals only; no income tax on salaries.
	"AE": {
		{"legal_currency", "AED", "2000-01-01"},
		{"social_label", "GPSSA", "2000-01-01"},
		{"social_nationals", "AE", "2000-01-01"},
		{"social_ee_rate", "5", "2000-01-01"},
		{"social_er_rate", "12.5", "2000-01-01"},
		{"social_includes_housing", "1", "2000-01-01"},
		{"overtime_rate", "1.25", "2000-01-01"},
		{"month_days", "30", "2000-01-01"},
		{"month_hours", "208", "2000-01-01"},
		{"gratuity", "uae", "2000-01-01"},
	},
}

func init() {
	// other Gulf states: end-of-service gratuity like the UAE (21 / 30 days of basic pay per year); no salary tax
	for _, c := range []string{"KW", "QA", "BH", "OM"} {
		payrollDefaults[c] = []ruleParam{
			{"overtime_rate", "1.25", "2000-01-01"}, {"month_days", "30", "2000-01-01"}, {"month_hours", "208", "2000-01-01"},
			{"gratuity", "uae", "2000-01-01"},
		}
	}
}

// generic values used for any country without its own defaults (every one can be changed per company)
var genericDefaults = []ruleParam{
	{"social_label", "Social security", "2000-01-01"},
	{"social_ee_rate", "0", "2000-01-01"},
	{"social_er_rate", "0", "2000-01-01"},
	{"overtime_rate", "1.5", "2000-01-01"},
	{"month_days", "26", "2000-01-01"},
	{"month_hours", "208", "2000-01-01"},
	{"gratuity", "none", "2000-01-01"},
}

// payrollParamKeys: every key the engine understands (the settings screen lists them).
var payrollParamKeys = []string{"legal_currency", "social_label", "social_nationals", "social_ee_rate", "social_er_rate", "social_er_rate_foreign",
	"social_ceiling", "social_includes_housing", "family_er_rate", "family_ceiling", "eos_er_rate", "family_allow_spouse", "family_allow_child",
	"family_allow_max_children", "tax_brackets", "tax_ded_self", "tax_ded_spouse", "tax_ded_child", "tax_ded_max_children", "tax_rounding",
	"min_wage", "transport_per_day", "overtime_rate", "month_days", "month_hours", "gratuity"}

type payrollParams map[string]string

func (p payrollParams) dec(k string) decimal.Decimal {
	v, err := decimal.NewFromString(strings.TrimSpace(p[k]))
	if err != nil {
		return decimal.Zero
	}
	return v
}

// effective picks, for each key, the value with the latest "from" not after date.
func effective(list []ruleParam, date string, into payrollParams, from map[string]string) {
	for _, r := range list {
		if r.from <= date && r.from >= from[r.key] {
			into[r.key] = r.value
			from[r.key] = r.from
		}
	}
}

// loadPayrollParams: country defaults + company overrides in force on date.
func loadPayrollParams(ctx context.Context, tx pgx.Tx, cid, date string) (payrollParams, string, error) {
	var country, eeRate, erRate string
	if err := tx.QueryRow(ctx, `SELECT country, trim_scale(nssf_employee_rate)::text, trim_scale(nssf_employer_rate)::text FROM companies WHERE id = $1`, cid).
		Scan(&country, &eeRate, &erRate); err != nil {
		if err == pgx.ErrNoRows {
			return nil, "", notFound()
		}
		return nil, "", err
	}
	p, from := payrollParams{}, map[string]string{}
	effective(genericDefaults, date, p, from)
	if _, ok := payrollDefaults[country]; ok {
		effective(payrollDefaults[country], date, p, from)
	} else { // no country rules: the company's simple social security rates (back-office settings)
		p["social_ee_rate"], p["social_er_rate"] = eeRate, erRate
	}
	rows, err := tx.Query(ctx, `SELECT key, value, effective_from::text FROM payroll_params WHERE company_id = $1 AND effective_from <= $2::date
		ORDER BY effective_from`, cid, date)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v, f string
		if err := rows.Scan(&k, &v, &f); err != nil {
			return nil, "", err
		}
		p[k] = v // ordered by date: the latest wins
	}
	return p, country, rows.Err()
}

// progressiveTax applies "limit:rate,limit:rate,...,:rate" brackets (limits cumulative, empty = no limit).
func progressiveTax(income decimal.Decimal, brackets string) decimal.Decimal {
	if !income.IsPositive() || strings.TrimSpace(brackets) == "" {
		return decimal.Zero
	}
	tax, lower := decimal.Zero, decimal.Zero
	for _, b := range strings.Split(brackets, ",") {
		lim, rate, _ := strings.Cut(strings.TrimSpace(b), ":")
		r, err := decimal.NewFromString(strings.TrimSpace(rate))
		if err != nil {
			continue
		}
		upper := income
		if strings.TrimSpace(lim) != "" {
			l, err := decimal.NewFromString(strings.TrimSpace(lim))
			if err != nil {
				continue
			}
			upper = decimal.Min(income, l)
		}
		if upper.GreaterThan(lower) {
			tax = tax.Add(upper.Sub(lower).Mul(r).Div(pct100))
		}
		if upper.Equal(income) {
			break
		}
		lower = upper
	}
	return tax
}

// ---------------- payslip computation ----------------

type slipComponent struct {
	Kind, Name, Currency string
	Amount               decimal.Decimal
	Taxable, Social      bool
}

type slipInput struct {
	Country             string
	Params              payrollParams
	Cur1, Cur2          string
	Dec1, Dec2, DecL    int32
	Salary1, Salary2    decimal.Decimal // monthly, before proration
	Transport1          decimal.Decimal // fixed monthly transport (cur1)
	TransportDaily      bool
	Housing1            decimal.Decimal
	Components          []slipComponent
	Commission1         decimal.Decimal
	Married, SpouseWork bool
	Children            int
	Nationality         string
	SocialEnrolled      bool
	TaxExempt           bool
	ActiveFactor        decimal.Decimal // share of the month employed (1 = whole month)
	WorkedDays          decimal.Decimal // scheduled days actually worked (transport per day)
	AbsentDays          decimal.Decimal // unexcused absences
	UnpaidDays          decimal.Decimal // approved unpaid leave
	OvertimeHours       decimal.Decimal
	ManualAllow1        decimal.Decimal
	ManualDed1          decimal.Decimal
	Recovery1           decimal.Decimal // advance recovery (cur1)
	// legal-currency units per 1 unit of cur1 / cur2 (1 when the same currency)
	RateL1, RateL2 decimal.Decimal
	LegalInPart1   bool
}

type slipLine struct {
	Code, Label, Kind, Role, Currency, Basis string
	Amount                                   decimal.Decimal
}

type slipResult struct {
	Lines                                            []slipLine
	Base1, Transport1, Allow1, Ded1, SocialEE1       decimal.Decimal
	SocialER1, Net1                                  decimal.Decimal
	Base2, Allow2, Ded2, Net2                        decimal.Decimal
	IncomeTax, SocialEEL, SocialERL, TaxableL, Gross decimal.Decimal
}

var pct100 = decimal.NewFromInt(100)

func computeSlip(in slipInput) (slipResult, error) {
	var res slipResult
	p := in.Params
	L := strings.TrimSpace(p["legal_currency"])
	if L == "" {
		L = in.Cur1
	}
	if in.RateL1.IsZero() {
		in.RateL1 = decimal.NewFromInt(1)
	}
	if in.RateL2.IsZero() {
		in.RateL2 = decimal.NewFromInt(1)
	}
	if in.ActiveFactor.IsZero() {
		in.ActiveFactor = decimal.NewFromInt(1)
	}
	monthDays := p.dec("month_days")
	if !monthDays.IsPositive() {
		monthDays = decimal.NewFromInt(26)
	}
	monthHours := p.dec("month_hours")
	if !monthHours.IsPositive() {
		monthHours = decimal.NewFromInt(208)
	}
	has2 := in.Cur2 != "" && in.Salary2.IsPositive()
	// where legal-currency amounts land: the part paid in the legal currency, else part 1 (converted)
	legalPart := 1
	if has2 && in.Cur2 == L && !in.LegalInPart1 {
		legalPart = 2
	}
	toPart := func(amountL decimal.Decimal, part int) (string, decimal.Decimal) {
		if part == 2 {
			return in.Cur2, amountL.Div(in.RateL2).Round(in.Dec2)
		}
		if in.Cur1 == L {
			return in.Cur1, amountL.Round(in.Dec1)
		}
		return in.Cur1, amountL.Div(in.RateL1).Round(in.Dec1)
	}
	toL := func(amount decimal.Decimal, cur string) decimal.Decimal {
		switch {
		case cur == L:
			return amount
		case cur == in.Cur1:
			return amount.Mul(in.RateL1)
		case cur == in.Cur2:
			return amount.Mul(in.RateL2)
		}
		return amount
	}
	add := func(code, label, kind, role, cur, basis string, amt decimal.Decimal) {
		if amt.IsZero() {
			return
		}
		res.Lines = append(res.Lines, slipLine{Code: code, Label: label, Kind: kind, Role: role, Currency: cur, Basis: basis, Amount: amt})
	}
	socialL, taxableL := decimal.Zero, decimal.Zero // bases in the legal currency

	// 1. salary (prorated for a part month) and absences
	sal1 := in.Salary1.Mul(in.ActiveFactor).Round(in.Dec1)
	add("salary", "Basic salary", "earning", "salary", in.Cur1, "", sal1)
	socialL, taxableL = socialL.Add(toL(sal1, in.Cur1)), taxableL.Add(toL(sal1, in.Cur1))
	sal2 := decimal.Zero
	if has2 {
		sal2 = in.Salary2.Mul(in.ActiveFactor).Round(in.Dec2)
		add("salary2", "Basic salary", "earning", "salary", in.Cur2, "", sal2)
		socialL, taxableL = socialL.Add(toL(sal2, in.Cur2)), taxableL.Add(toL(sal2, in.Cur2))
	}
	if off := in.AbsentDays.Add(in.UnpaidDays); off.IsPositive() {
		basis := fmt.Sprintf("%s d / %s", off.String(), monthDays.String())
		a1 := in.Salary1.Mul(off).Div(monthDays).Round(in.Dec1)
		a1 = decimal.Min(a1, sal1)
		add("absence", "Absence / unpaid leave", "deduction", "salary", in.Cur1, basis, a1)
		socialL, taxableL = socialL.Sub(toL(a1, in.Cur1)), taxableL.Sub(toL(a1, in.Cur1))
		if has2 {
			a2 := decimal.Min(in.Salary2.Mul(off).Div(monthDays).Round(in.Dec2), sal2)
			add("absence2", "Absence / unpaid leave", "deduction", "salary", in.Cur2, basis, a2)
			socialL, taxableL = socialL.Sub(toL(a2, in.Cur2)), taxableL.Sub(toL(a2, in.Cur2))
		}
	}
	// 2. housing, overtime, commission, fixed components, manual allowance
	if in.Housing1.IsPositive() {
		h := in.Housing1.Mul(in.ActiveFactor).Round(in.Dec1)
		add("housing", "Housing allowance", "earning", "salary", in.Cur1, "", h)
		taxableL = taxableL.Add(toL(h, in.Cur1))
		if p["social_includes_housing"] == "1" || in.Country == "LB" {
			socialL = socialL.Add(toL(h, in.Cur1))
		}
	}
	if in.OvertimeHours.IsPositive() {
		hourly := in.Salary1.Div(monthHours)
		ot := hourly.Mul(in.OvertimeHours).Mul(p.dec("overtime_rate")).Round(in.Dec1)
		add("overtime", "Overtime", "earning", "salary", in.Cur1, fmt.Sprintf("%s h × %s", in.OvertimeHours.String(), p["overtime_rate"]), ot)
		socialL, taxableL = socialL.Add(toL(ot, in.Cur1)), taxableL.Add(toL(ot, in.Cur1))
		if has2 {
			ot2 := in.Salary2.Div(monthHours).Mul(in.OvertimeHours).Mul(p.dec("overtime_rate")).Round(in.Dec2)
			add("overtime2", "Overtime", "earning", "salary", in.Cur2, fmt.Sprintf("%s h × %s", in.OvertimeHours.String(), p["overtime_rate"]), ot2)
			socialL, taxableL = socialL.Add(toL(ot2, in.Cur2)), taxableL.Add(toL(ot2, in.Cur2))
		}
	}
	if in.Commission1.IsPositive() {
		c := in.Commission1.Round(in.Dec1)
		add("commission", "Commission", "earning", "salary", in.Cur1, "", c)
		taxableL = taxableL.Add(toL(c, in.Cur1))
	}
	for _, c := range in.Components {
		cur := c.Currency
		dec := in.Dec1
		if cur == in.Cur2 {
			dec = in.Dec2
		}
		if cur != in.Cur1 && cur != in.Cur2 { // a component in a third currency is paid in part 1
			cur = in.Cur1
		}
		a := c.Amount.Round(dec)
		if c.Kind == "deduction" {
			add("component", c.Name, "deduction", "salary", cur, "", a)
			continue
		}
		add("component", c.Name, "earning", "salary", cur, "", a)
		if c.Taxable {
			taxableL = taxableL.Add(toL(a, cur))
		}
		if c.Social {
			socialL = socialL.Add(toL(a, cur))
		}
	}
	if in.ManualAllow1.IsPositive() {
		add("manual_allow", "Other allowance", "earning", "salary", in.Cur1, "", in.ManualAllow1)
		taxableL = taxableL.Add(toL(in.ManualAllow1, in.Cur1))
	}
	if in.ManualDed1.IsPositive() {
		add("manual_ded", "Other deduction", "deduction", "salary", in.Cur1, "", in.ManualDed1)
	}
	// 3. transport (not taxed, not subject to social security)
	if in.Transport1.IsPositive() {
		add("transport", "Transport allowance", "earning", "salary", in.Cur1, "", in.Transport1.Mul(in.ActiveFactor).Round(in.Dec1))
	}
	if in.TransportDaily && in.WorkedDays.IsPositive() {
		perDay := p.dec("transport_per_day")
		if perDay.IsPositive() {
			cur, a := toPart(perDay.Mul(in.WorkedDays), legalPart)
			add("transport_daily", "Transport allowance", "earning", "salary", cur, fmt.Sprintf("%s d × %s %s", in.WorkedDays.String(), perDay.String(), L), a)
		}
	}
	if socialL.IsNegative() {
		socialL = decimal.Zero
	}
	if taxableL.IsNegative() {
		taxableL = decimal.Zero
	}
	res.TaxableL = taxableL

	// 4. social security
	label := p["social_label"]
	if label == "" {
		label = "Social security"
	}
	nationalsOnly := strings.TrimSpace(p["social_nationals"])
	national := nationalsOnly == "" || strings.Contains(","+nationalsOnly+",", ","+in.Nationality+",")
	if in.SocialEnrolled {
		ceil := p.dec("social_ceiling")
		capped := socialL
		if ceil.IsPositive() {
			capped = decimal.Min(socialL, ceil)
		}
		eeRate, erRate := p.dec("social_ee_rate"), p.dec("social_er_rate")
		if !national {
			eeRate, erRate = decimal.Zero, p.dec("social_er_rate_foreign")
		}
		ee := capped.Mul(eeRate).Div(pct100)
		er := capped.Mul(erRate).Div(pct100)
		res.SocialEEL, res.SocialERL = ee, er
		if ee.IsPositive() {
			cur, a := toPart(ee, legalPart)
			add("social_ee", label+" (employee)", "deduction", "social", cur, fmt.Sprintf("%s%% × %s %s", eeRate.String(), capped.Round(0).String(), L), a)
		}
		if er.IsPositive() {
			cur, a := toPart(er, legalPart)
			add("social_er", label+" (employer)", "employer", "social", cur, fmt.Sprintf("%s%% × %s %s", erRate.String(), capped.Round(0).String(), L), a)
		}
		if fr := p.dec("family_er_rate"); fr.IsPositive() {
			base := socialL
			if c := p.dec("family_ceiling"); c.IsPositive() {
				base = decimal.Min(base, c)
			}
			v := base.Mul(fr).Div(pct100)
			res.SocialERL = res.SocialERL.Add(v)
			cur, a := toPart(v, legalPart)
			add("family_er", label+" family allowances (employer)", "employer", "social", cur, fmt.Sprintf("%s%% × %s %s", fr.String(), base.Round(0).String(), L), a)
		}
		if eos := p.dec("eos_er_rate"); eos.IsPositive() {
			v := socialL.Mul(eos).Div(pct100)
			res.SocialERL = res.SocialERL.Add(v)
			cur, a := toPart(v, legalPart)
			add("eos_er", label+" end of service (employer)", "employer", "social", cur, fmt.Sprintf("%s%% × %s %s", eos.String(), socialL.Round(0).String(), L), a)
		}
		// family allowances paid with the salary for the social fund (non taxable)
		fa := decimal.Zero
		if in.Married && !in.SpouseWork {
			fa = fa.Add(p.dec("family_allow_spouse"))
		}
		kids := in.Children
		if m := int(p.dec("family_allow_max_children").IntPart()); m > 0 && kids > m {
			kids = m
		}
		fa = fa.Add(p.dec("family_allow_child").Mul(decimal.NewFromInt(int64(kids))))
		if fa.IsPositive() {
			cur, a := toPart(fa.Mul(in.ActiveFactor), legalPart)
			add("family_allow", "Family allowance", "earning", "allowance_social", cur, "", a)
		}
	}

	// 5. salary income tax (annualised, family deductions)
	if !in.TaxExempt && strings.TrimSpace(p["tax_brackets"]) != "" {
		annual := taxableL.Mul(decimal.NewFromInt(12))
		ded := p.dec("tax_ded_self")
		if in.Married && !in.SpouseWork {
			ded = ded.Add(p.dec("tax_ded_spouse"))
		}
		kids := in.Children
		if m := int(p.dec("tax_ded_max_children").IntPart()); m > 0 && kids > m {
			kids = m
		}
		ded = ded.Add(p.dec("tax_ded_child").Mul(decimal.NewFromInt(int64(kids))))
		net := annual.Sub(ded)
		tax := progressiveTax(net, p["tax_brackets"]).Div(decimal.NewFromInt(12))
		if rnd := p.dec("tax_rounding"); rnd.IsPositive() {
			tax = tax.Div(rnd).Round(0).Mul(rnd)
		}
		res.IncomeTax = tax
		if tax.IsPositive() {
			cur, a := toPart(tax, legalPart)
			add("income_tax", "Salary income tax", "deduction", "tax", cur, fmt.Sprintf("(%s × 12 − %s) / 12", taxableL.Round(0).String(), ded.Round(0).String()), a)
		}
	}

	// 6. advance recovery (cur1, capped by what is left of part 1)
	// summaries per part
	sum := func() {
		res.Base1, res.Transport1, res.Allow1, res.Ded1, res.SocialEE1, res.SocialER1 = sal1, decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero
		res.Base2, res.Allow2, res.Ded2 = sal2, decimal.Zero, decimal.Zero
		for _, l := range res.Lines {
			part2 := has2 && l.Currency == in.Cur2 && in.Cur2 != in.Cur1
			switch {
			case l.Code == "salary" || l.Code == "salary2" || l.Kind == "employer" || l.Kind == "info":
				if l.Kind == "employer" && !part2 {
					res.SocialER1 = res.SocialER1.Add(l.Amount)
				}
			case l.Code == "transport" && !part2:
				res.Transport1 = res.Transport1.Add(l.Amount)
			case l.Kind == "earning":
				if part2 {
					res.Allow2 = res.Allow2.Add(l.Amount)
				} else {
					res.Allow1 = res.Allow1.Add(l.Amount)
				}
			case l.Kind == "deduction" && l.Role == "advance":
				// kept apart (advance_recovery column)
			case l.Kind == "deduction" && l.Role == "social" && !part2:
				res.SocialEE1 = res.SocialEE1.Add(l.Amount)
			case l.Kind == "deduction":
				if part2 {
					res.Ded2 = res.Ded2.Add(l.Amount)
				} else {
					res.Ded1 = res.Ded1.Add(l.Amount)
				}
			}
		}
		res.Net1 = res.Base1.Add(res.Transport1).Add(res.Allow1).Sub(res.Ded1).Sub(res.SocialEE1)
		res.Net2 = res.Base2.Add(res.Allow2).Sub(res.Ded2)
	}
	sum()
	if res.Net2.IsNegative() && legalPart == 2 {
		// the legal-currency part cannot carry the deductions: put the legal-currency amounts on part 1
		in.LegalInPart1 = true
		return computeSlip(in)
	}
	if in.Recovery1.IsPositive() {
		rec := decimal.Min(in.Recovery1, decimal.Max(decimal.Zero, res.Net1))
		if rec.IsPositive() {
			add("advance", "Advance recovery", "deduction", "advance", in.Cur1, "", rec)
			res.Net1 = res.Net1.Sub(rec)
		}
	}
	if res.Net1.IsNegative() || res.Net2.IsNegative() {
		return res, badRequest("negative_net")
	}
	for _, l := range res.Lines {
		if l.Kind == "earning" {
			res.Gross = res.Gross.Add(toL(l.Amount, l.Currency))
		}
	}
	return res, nil
}

// recoveryOf: the advance recovery line of a result.
func (r slipResult) recovery() decimal.Decimal {
	for _, l := range r.Lines {
		if l.Role == "advance" {
			return l.Amount
		}
	}
	return decimal.Zero
}

// ---------------- end of service ----------------

// gratuity computes an end-of-service amount from the last monthly basic pay and the service length.
// ksa: half a month per year for the first 5 years, a month per year after (resignation: none < 2 y, 1/3 up to
// 5 y, 2/3 up to 10 y, full after). uae: 21 days of basic pay per year for the first 5 years, 30 days after,
// capped at 2 years of pay; under 1 year nothing. nssf (Lebanon): paid by the NSSF from the 8.5 %
// contributions — the figure is the contributions booked so far, shown for information.
func gratuity(mode string, basic decimal.Decimal, start, end time.Time, resignation bool) (decimal.Decimal, string) {
	days := decimal.NewFromInt(int64(end.Sub(start).Hours()/24) + 1)
	years := days.Div(decimal.NewFromFloat(365.25))
	switch mode {
	case "ksa":
		first := decimal.Min(years, decimal.NewFromInt(5))
		rest := decimal.Max(decimal.Zero, years.Sub(decimal.NewFromInt(5)))
		g := basic.Mul(first).Div(decimal.NewFromInt(2)).Add(basic.Mul(rest))
		if resignation {
			switch {
			case years.LessThan(decimal.NewFromInt(2)):
				g = decimal.Zero
			case years.LessThan(decimal.NewFromInt(5)):
				g = g.Div(decimal.NewFromInt(3))
			case years.LessThan(decimal.NewFromInt(10)):
				g = g.Mul(decimal.NewFromInt(2)).Div(decimal.NewFromInt(3))
			}
		}
		return g, fmt.Sprintf("%s years", years.Round(2).String())
	case "uae":
		if years.LessThan(decimal.NewFromInt(1)) {
			return decimal.Zero, "< 1 year"
		}
		daily := basic.Div(decimal.NewFromInt(30))
		first := decimal.Min(years, decimal.NewFromInt(5))
		rest := decimal.Max(decimal.Zero, years.Sub(decimal.NewFromInt(5)))
		g := daily.Mul(decimal.NewFromInt(21)).Mul(first).Add(daily.Mul(decimal.NewFromInt(30)).Mul(rest))
		g = decimal.Min(g, basic.Mul(decimal.NewFromInt(24)))
		return g, fmt.Sprintf("%s years", years.Round(2).String())
	}
	return decimal.Zero, mode
}

// sortedKeys of a params map (stable output).
func (p payrollParams) sortedKeys() []string {
	k := make([]string, 0, len(p))
	for x := range p {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}
