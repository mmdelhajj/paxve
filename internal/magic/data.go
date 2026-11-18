// Package magic reads an export of the legacy "Magic" database (VB.NET + SQL Server 2008 R2) made by
// tools/magic-export/Export-Magic.ps1 and turns it into a migration plan for Paxve: stations, boxes,
// areas, collectors, plans, customers, subscriptions, meter readings, unpaid bills, customer and
// supplier opening balances and one opening journal entry per company.
//
// The package has no database code: Parse reads the zip, Build computes the plan of one Magic
// company. internal/httpapi writes the plan inside one transaction. See docs/MAGIC_MIGRATION.md.
package magic

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

const (
	// FormatName / FormatVersion are written by Export-Magic.ps1 into manifest.json.
	FormatName    = "paxve-magic-export"
	FormatVersion = 1

	maxUncompressed = 2 << 30 // 2 GiB in total: refuse zip bombs
)

type Company struct {
	ID        int
	Name      string
	TypeID    int
	Available bool
}

type Branch struct {
	ID, CompanyID int
	Name          string
}

type Station struct {
	ID, BranchID int
	Name         string
	Available    bool
}

type Location struct {
	ID, StationID, DefaultCollectorID int
	Name, Address                     string
	Available                         bool
	Order                             int
}

type Collector struct {
	ID, PersonID, CompanyID int
	Active, Default         bool
	Name                    string
}

type Money struct {
	ID                  int
	Code                string // USD, LBP …
	DolBource, LLBource decimal.Decimal
}

type Service struct {
	ID, TypeInscriptionID, CompanyID  int
	Name                              string
	Power                             decimal.Decimal
	BaseQt                            decimal.Decimal
	BaseCost, Cost, NetCost, Discount decimal.Decimal
	Available                         bool
}

type Ledger struct {
	ID, ParentID           int
	NameEn, NameAr, NameFr string
	BalanceType, Level     int
	Used                   bool
}

type Person struct {
	ID        int
	Name      string
	Available bool
	Type      int
}

type Account struct {
	LedgerID, PersonID, MoneyID, CompanyID int
	Available                              bool
}

type Sub struct {
	ID, LedgerID, PersonID, MoneyID, CompanyID  int
	CollectorID, LocationID, NumInLocation      int
	ServiceID, TypeInscriptionID, TypePayment   int
	Begin                                       string
	Freezed, FreezedNoCharge, Active, Available bool
	Maktab                                      bool
	SpecialBaseCost, SpecialCost                decimal.Decimal
	Address, UserNet, CellPhone, HomePhone      string
	Expires, Note                               string
}

type Bill struct {
	IssalID, Year, CompanyID, SubID, Month int
	Paid                                   bool
	NewCounter, OldCounter                 decimal.Decimal
	HasNew, HasOld                         bool
	BaseCost, Cost, FinalCost, Discount    decimal.Decimal
	Invoiced, MustPay                      string
	Deleted                                bool
	Visible                                bool
	ServiceID                              int
}

// Balance is one aggregated group of journal lines (Movement) exported by the script:
// all visible lines of one posting account (ledger × person × account currency × company),
// one line currency and one calendar year.
type Balance struct {
	CompanyID, LedgerID, PersonID, MoneyID, LineMoneyID, Year int
	AcctOK, USDOK                                             bool
	Lines                                                     int
	Raw                                                       decimal.Decimal // Σ(DB−CR) in the line currency
	USD                                                       decimal.Decimal // Σ(DB−CR)·DolBource (Magic's own USD value)
	LBP                                                       decimal.Decimal // Σ(DB−CR)·LLBource
}

type Manifest struct {
	Format     string         `json:"format"`
	Version    int            `json:"version"`
	ExportedAt string         `json:"exported_at"`
	Database   string         `json:"database"`
	Months     int            `json:"months"`
	Tables     map[string]int `json:"tables"`
}

// Data is everything read from one export zip.
type Data struct {
	Manifest   Manifest
	Companies  map[int]*Company
	Branches   map[int]*Branch
	Stations   map[int]*Station
	Locations  map[int]*Location
	Collectors map[int]*Collector
	Money      map[int]*Money
	Services   map[int]*Service
	Ledgers    map[int]*Ledger
	People     map[int]*Person
	Info       map[int]map[int][]string // person → TypeInformationID → values
	Accounts   []Account
	Subs       []*Sub
	Bills      []*Bill
	Balances   []Balance
}

// ---- parsing ----

// columns that must be present in each file (the others are optional)
var needCols = map[string][]string{
	"company": {"CompanyID", "CompanyName"}, "money": {"MoneyID", "Money"}, "accountnames": {"AccountNameID", "AccountName"},
	"accounts": {"LedgerID", "AccountNameID", "MoneyID", "CompanyID"}, "subscrip": {"SubID", "AccountNameID", "MoneyID", "CompanyID", "SeviceID"},
	"sevice": {"SeviceID", "CompanyID", "Cost"}, "ledgeraccount": {"LedgerID"},
	"movementbalances": {"CompanyID", "LedgerID", "AccountNameID", "MoneyID", "MoneyID_DBCR", "Yr", "SumRaw", "SumUSD", "SumLBP"},
	"datacustomer":     {"IssalID", "Annee", "CompanyID", "SubID", "MonthID", "YesNoPayed", "FinalCost"},
}

var required = []string{"Company", "Money", "AccountNames", "Accounts", "Subscrip", "Sevice", "LedgerAccount", "MovementBalances"}

// Parse reads an export zip. It never trusts sizes from the archive: every file is read through a limit.
func Parse(zipBytes []byte) (*Data, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, fmt.Errorf("not_a_zip")
	}
	files := map[string]*zip.File{}
	var total uint64
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		total += f.UncompressedSize64
		name := strings.ToLower(strings.TrimSuffix(path.Base(strings.ReplaceAll(f.Name, "\\", "/")), path.Ext(f.Name)))
		files[name] = f
	}
	if total > maxUncompressed {
		return nil, fmt.Errorf("zip_too_large")
	}
	d := &Data{Companies: map[int]*Company{}, Branches: map[int]*Branch{}, Stations: map[int]*Station{},
		Locations: map[int]*Location{}, Collectors: map[int]*Collector{}, Money: map[int]*Money{}, Services: map[int]*Service{},
		Ledgers: map[int]*Ledger{}, People: map[int]*Person{}, Info: map[int]map[int][]string{}}

	if f, ok := files["manifest"]; ok {
		b, err := readAll(f)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")), &d.Manifest); err != nil {
			return nil, fmt.Errorf("bad_manifest")
		}
		if d.Manifest.Format != FormatName {
			return nil, fmt.Errorf("bad_manifest")
		}
		if d.Manifest.Version > FormatVersion {
			return nil, fmt.Errorf("unsupported_version")
		}
	} else {
		return nil, fmt.Errorf("missing_file: manifest.json")
	}
	for _, r := range required {
		if _, ok := files[strings.ToLower(r)]; !ok {
			return nil, fmt.Errorf("missing_file: %s.csv", r)
		}
	}
	each := func(name string, fn func(r row) error) error {
		f, ok := files[strings.ToLower(name)]
		if !ok {
			return nil
		}
		return readCSV(f, name, needCols[strings.ToLower(name)], fn)
	}
	steps := []struct {
		name string
		fn   func(r row) error
	}{
		{"Company", func(r row) error {
			c := &Company{ID: r.int("CompanyID"), Name: r.str("CompanyName"), TypeID: r.int("CompanyTypeID"), Available: r.boolean("Available", true)}
			d.Companies[c.ID] = c
			return r.err
		}},
		{"Branchs", func(r row) error {
			b := &Branch{ID: r.int("BranchID"), CompanyID: r.int("CompanyID"), Name: r.str("BranchName")}
			d.Branches[b.ID] = b
			return r.err
		}},
		{"Stations", func(r row) error {
			s := &Station{ID: r.int("StationID"), BranchID: r.int("BranchID"), Name: r.str("Station"), Available: r.boolean("Available", true)}
			d.Stations[s.ID] = s
			return r.err
		}},
		{"Location", func(r row) error {
			l := &Location{ID: r.int("LocationID"), StationID: r.int("StationID"), DefaultCollectorID: r.int("DefaultCollectorID"),
				Name: r.str("Location"), Address: r.str("Adress"), Available: r.boolean("Available", true), Order: r.int("LocOrder")}
			d.Locations[l.ID] = l
			return r.err
		}},
		{"Collector", func(r row) error {
			c := &Collector{ID: r.int("CollectorID"), PersonID: r.int("AccountNameID"), CompanyID: r.int("CompanyID"),
				Active: r.boolean("Activate", true), Default: r.boolean("DefaultC", false), Name: r.str("CollectorName")}
			if c.Name == "" {
				c.Name = r.str("AccountName")
			}
			d.Collectors[c.ID] = c
			return r.err
		}},
		{"Money", func(r row) error {
			m := &Money{ID: r.int("MoneyID"), Code: strings.ToUpper(strings.TrimSpace(r.str("Money"))),
				DolBource: r.dec("DolBource"), LLBource: r.dec("LLBource")}
			d.Money[m.ID] = m
			return r.err
		}},
		{"Sevice", func(r row) error {
			s := &Service{ID: r.int("SeviceID"), TypeInscriptionID: r.int("TypeInscriptionID"), CompanyID: r.int("CompanyID"),
				Name: r.str("Sevice"), Power: r.dec("PowerService"), BaseQt: r.dec("BaseQt"), BaseCost: r.dec("BaseCost"),
				Cost: r.dec("Cost"), NetCost: r.dec("NetCost"), Discount: r.dec("Discount"), Available: r.boolean("Available", true)}
			d.Services[s.ID] = s
			return r.err
		}},
		{"LedgerAccount", func(r row) error {
			l := &Ledger{ID: r.int("LedgerID"), ParentID: r.int("LedgerParentID"), NameEn: r.str("NameEn"), NameAr: r.str("NameAr"),
				NameFr: r.str("NameFr"), BalanceType: r.int("BalanceTypeID"), Level: r.int("Level"), Used: r.boolean("Used", true)}
			d.Ledgers[l.ID] = l
			return r.err
		}},
		{"AccountNames", func(r row) error {
			p := &Person{ID: r.int("AccountNameID"), Name: r.str("AccountName"), Available: r.boolean("Available", true), Type: r.int("AccountType")}
			d.People[p.ID] = p
			return r.err
		}},
		{"AcountNamesInformation", func(r row) error {
			id, t, v := r.int("AccountNameID"), r.int("TypeInformationID"), r.str("Information")
			if v == "" {
				return r.err
			}
			if d.Info[id] == nil {
				d.Info[id] = map[int][]string{}
			}
			d.Info[id][t] = append(d.Info[id][t], v)
			return r.err
		}},
		{"Accounts", func(r row) error {
			d.Accounts = append(d.Accounts, Account{LedgerID: r.int("LedgerID"), PersonID: r.int("AccountNameID"),
				MoneyID: r.int("MoneyID"), CompanyID: r.int("CompanyID"), Available: r.boolean("Available", true)})
			return r.err
		}},
		{"Subscrip", func(r row) error {
			s := &Sub{ID: r.int("SubID"), LedgerID: r.int("LedgerID"), PersonID: r.int("AccountNameID"), MoneyID: r.int("MoneyID"),
				CompanyID: r.int("CompanyID"), CollectorID: r.int("ExecutiveCollectorID"), LocationID: r.int("LocationID"),
				NumInLocation: r.int("NumInLocation"), ServiceID: r.int("SeviceID"), TypeInscriptionID: r.int("TypeInscriptionID"),
				TypePayment: r.int("TypePaymentSubID"), Begin: r.date("DateBegin"), Freezed: r.boolean("Freezed", false),
				FreezedNoCharge: r.boolean("FreezedNoCharge", false), Active: r.boolean("Activeted", true),
				Available: r.boolean("Available", true), Maktab: r.boolean("Maktab", false),
				SpecialBaseCost: r.dec("SpeciaBaseCost"), SpecialCost: r.dec("SpeciaCost"), Address: r.str("Adress"),
				UserNet: r.str("UserNet"), CellPhone: r.str("CellPhone"), HomePhone: r.str("HomePhone"),
				Expires: r.date("ExpiredDate"), Note: r.str("RQ")}
			d.Subs = append(d.Subs, s)
			return r.err
		}},
		{"DataCustomer", func(r row) error {
			b := &Bill{IssalID: r.int("IssalID"), Year: r.int("Annee"), CompanyID: r.int("CompanyID"), SubID: r.int("SubID"),
				Month: r.int("MonthID"), Paid: r.boolean("YesNoPayed", false), BaseCost: r.dec("BaseCost"), Cost: r.dec("Cost"),
				FinalCost: r.dec("FinalCost"), Discount: r.dec("Discount"), Invoiced: r.date("DateInvoiced"),
				MustPay: r.date("DateMustPayed"), Deleted: r.boolean("Deleted", false), Visible: r.boolean("Visible", true),
				ServiceID: r.int("SeviceID")}
			if s := r.str("NewCounter"); s != "" {
				b.NewCounter, b.HasNew = r.dec("NewCounter"), true
			}
			if s := r.str("OldCounter"); s != "" {
				b.OldCounter, b.HasOld = r.dec("OldCounter"), true
			}
			d.Bills = append(d.Bills, b)
			return r.err
		}},
		{"MovementBalances", func(r row) error {
			d.Balances = append(d.Balances, Balance{CompanyID: r.int("CompanyID"), LedgerID: r.int("LedgerID"),
				PersonID: r.int("AccountNameID"), MoneyID: r.int("MoneyID"), LineMoneyID: r.int("MoneyID_DBCR"),
				Year: r.int("Yr"), AcctOK: r.boolean("AcctRateOK", true), USDOK: r.boolean("UsdRateOK", true),
				Lines: r.int("Lines"), Raw: r.dec("SumRaw"), USD: r.dec("SumUSD"), LBP: r.dec("SumLBP")})
			return r.err
		}},
	}
	for _, s := range steps {
		if err := each(s.name, s.fn); err != nil {
			return nil, err
		}
	}
	if len(d.Companies) == 0 {
		return nil, fmt.Errorf("no_companies")
	}
	for _, m := range d.Money {
		if m.Code != "USD" && m.Code != "LBP" && len(m.Code) != 3 {
			return nil, fmt.Errorf("unknown_currency: %s", m.Code)
		}
	}
	return d, nil
}

func readAll(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("bad_zip_entry: %s", f.Name)
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, maxUncompressed))
	if err != nil {
		return nil, fmt.Errorf("bad_zip_entry: %s", f.Name)
	}
	return b, nil
}

type row struct {
	file string
	line int
	idx  map[string]int
	rec  []string
	err  error
}

func (r *row) str(col string) string {
	i, ok := r.idx[strings.ToLower(col)]
	if !ok || i >= len(r.rec) {
		return ""
	}
	return strings.TrimSpace(r.rec[i])
}

func (r *row) int(col string) int {
	s := r.str(col)
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		// some ids are exported as 12.0
		f, err2 := decimal.NewFromString(s)
		if err2 != nil || !f.Equal(f.Truncate(0)) {
			if r.err == nil {
				r.err = fmt.Errorf("bad_value: %s line %d column %s = %q", r.file, r.line, col, s)
			}
			return 0
		}
		return int(f.IntPart())
	}
	return n
}

func (r *row) dec(col string) decimal.Decimal {
	s := r.str(col)
	if s == "" {
		return decimal.Zero
	}
	s = strings.ReplaceAll(s, ",", ".")
	d, err := decimal.NewFromString(s)
	if err != nil {
		if r.err == nil {
			r.err = fmt.Errorf("bad_value: %s line %d column %s = %q", r.file, r.line, col, s)
		}
		return decimal.Zero
	}
	return d
}

func (r *row) boolean(col string, def bool) bool {
	switch strings.ToLower(r.str(col)) {
	case "1", "true", "yes", "-1":
		return true
	case "0", "false", "no":
		return false
	}
	return def
}

// date returns YYYY-MM-DD or "" (the export writes ISO dates; older exports may add a time).
func (r *row) date(col string) string {
	s := r.str(col)
	if s == "" {
		return ""
	}
	if len(s) >= 10 {
		if t, err := time.Parse(time.DateOnly, s[:10]); err == nil {
			return t.Format(time.DateOnly)
		}
	}
	for _, layout := range []string{"2/1/2006", "02/01/2006", "1/2/2006 15:04:05", "2006/01/02"} {
		if t, err := time.Parse(layout, strings.Fields(s)[0]); err == nil {
			return t.Format(time.DateOnly)
		}
	}
	return ""
}

func readCSV(f *zip.File, name string, need []string, fn func(r row) error) error {
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("bad_zip_entry: %s", f.Name)
	}
	defer rc.Close()
	br := bufio.NewReader(io.LimitReader(rc, maxUncompressed))
	if b, err := br.Peek(3); err == nil && string(b) == "\xef\xbb\xbf" {
		_, _ = br.Discard(3) // UTF-8 byte order mark (Windows tools write one)
	}
	cr := csv.NewReader(br)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	cr.ReuseRecord = true
	head, err := cr.Read()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return fmt.Errorf("bad_csv: %s", name)
	}
	idx := map[string]int{}
	for i, h := range head {
		h = strings.TrimSpace(strings.TrimPrefix(h, "\ufeff"))
		idx[strings.ToLower(h)] = i
	}
	for _, c := range need {
		if _, ok := idx[strings.ToLower(c)]; !ok {
			return fmt.Errorf("missing_column: %s.%s", name, c)
		}
	}
	line := 1
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		line++
		if err != nil {
			return fmt.Errorf("bad_csv: %s line %d", name, line)
		}
		if err := fn(row{file: name, line: line, idx: idx, rec: rec}); err != nil {
			return err
		}
	}
}
