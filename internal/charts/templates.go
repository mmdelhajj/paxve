// Package charts holds ready-made charts of accounts a company can start from.
package charts

type Account struct {
	Code     string
	Parent   string
	Type     string // asset, liability, equity, income, expense
	Postable bool
	Names    map[string]string // en, ar, fr
}

type Template struct {
	Key      string            `json:"key"`
	Names    map[string]string `json:"names"`
	Accounts []Account         `json:"-"`
	Count    int               `json:"count"`
	// Billing defaults (account codes) set on the company when the template is loaded.
	AR, FXGain, FXLoss, Revenue string   `json:"-"`
	Methods                     []Method `json:"-"`
	// Purchasing / stock defaults (account codes).
	AP, Inventory, COGS, Fuel, StockAdjust, Expense string `json:"-"`
}

// Method is a default way of receiving money. Currency "" means the company base currency.
type Method struct {
	Kind, Currency, Code string
	Names                map[string]string
}

func m(kind, currency, code, en, ar, fr string) Method {
	return Method{Kind: kind, Currency: currency, Code: code, Names: map[string]string{"en": en, "ar": ar, "fr": fr}}
}

func a(code, parent, typ string, postable bool, en, ar, fr string) Account {
	return Account{Code: code, Parent: parent, Type: typ, Postable: postable,
		Names: map[string]string{"en": en, "ar": ar, "fr": fr}}
}

// Lebanon: simplified Lebanese General Chart of Accounts (PCG), classes 1-7,
// with the accounts a subscription / services business uses every day.
var lebanon = []Account{
	a("1", "", "equity", false, "Capital accounts", "حسابات الأموال الدائمة", "Comptes de capitaux"),
	a("101", "1", "equity", true, "Capital", "رأس المال", "Capital"),
	a("11", "1", "equity", true, "Reserves", "الاحتياطيات", "Réserves"),
	a("12", "1", "equity", true, "Retained earnings / result", "النتيجة المدورة", "Report à nouveau / résultat"),
	a("16", "1", "liability", true, "Long-term loans", "القروض الطويلة الأجل", "Emprunts à long terme"),

	a("2", "", "asset", false, "Fixed assets", "الأصول الثابتة", "Immobilisations"),
	a("213", "2", "asset", true, "Generators and equipment", "المولدات والمعدات", "Groupes électrogènes et matériel"),
	a("214", "2", "asset", true, "Networks and cables", "الشبكات والكابلات", "Réseaux et câbles"),
	a("218", "2", "asset", true, "Vehicles and furniture", "السيارات والأثاث", "Véhicules et mobilier"),
	a("28", "2", "asset", true, "Accumulated depreciation", "مجمع الاستهلاكات", "Amortissements cumulés"),

	a("3", "", "asset", false, "Inventory", "المخزون", "Stocks"),
	a("31", "3", "asset", true, "Fuel and raw materials", "المحروقات والمواد الأولية", "Carburant et matières premières"),
	a("32", "3", "asset", true, "Spare parts and supplies", "قطع الغيار واللوازم", "Pièces de rechange et fournitures"),
	a("37", "3", "asset", true, "Goods for resale", "البضائع", "Marchandises"),

	a("4", "", "asset", false, "Third-party accounts", "حسابات الغير", "Comptes de tiers"),
	a("401", "4", "liability", true, "Suppliers", "الموردون", "Fournisseurs"),
	a("411", "4", "asset", true, "Customers / subscribers", "الزبائن / المشتركون", "Clients / abonnés"),
	a("419", "4", "liability", true, "Customer advances and deposits", "سلفات وتأمينات الزبائن", "Avances et dépôts clients"),
	a("421", "4", "liability", true, "Salaries payable", "رواتب مستحقة", "Rémunérations dues"),
	a("431", "4", "liability", true, "Social security (NSSF)", "الضمان الاجتماعي", "Sécurité sociale (CNSS)"),
	a("4426", "4", "asset", true, "VAT recoverable", "ضريبة القيمة المضافة القابلة للاسترداد", "TVA récupérable"),
	a("4427", "4", "liability", true, "VAT collected", "ضريبة القيمة المضافة المحصلة", "TVA collectée"),
	a("444", "4", "liability", true, "Income tax payable", "ضريبة الدخل المستحقة", "Impôt sur le revenu dû"),
	a("455", "4", "liability", true, "Partners current accounts", "حسابات الشركاء الجارية", "Comptes courants d'associés"),
	a("467", "4", "asset", true, "Collectors (cash to receive)", "الجباة (نقد قيد الاستلام)", "Encaisseurs (espèces à recevoir)"),

	a("5", "", "asset", false, "Financial accounts", "الحسابات المالية", "Comptes financiers"),
	a("512", "5", "asset", true, "Banks", "المصارف", "Banques"),
	a("531", "5", "asset", true, "Cash USD", "الصندوق دولار", "Caisse USD"),
	a("532", "5", "asset", true, "Cash LBP", "الصندوق ليرة", "Caisse LBP"),
	a("535", "5", "asset", true, "Electronic wallets (Whish, OMT)", "المحافظ الإلكترونية (Whish، OMT)", "Portefeuilles électroniques (Whish, OMT)"),
	a("58", "5", "asset", true, "Internal transfers", "التحويلات الداخلية", "Virements internes"),

	a("6", "", "expense", false, "Expenses", "الأعباء", "Charges"),
	a("601", "6", "expense", true, "Fuel purchases", "مشتريات المحروقات", "Achats de carburant"),
	a("602", "6", "expense", true, "Purchases of supplies and parts", "مشتريات اللوازم وقطع الغيار", "Achats de fournitures et pièces"),
	a("607", "6", "expense", true, "Purchases of goods", "مشتريات البضائع", "Achats de marchandises"),
	a("611", "6", "expense", true, "Rent", "الإيجارات", "Loyers"),
	a("615", "6", "expense", true, "Maintenance and repairs", "الصيانة والتصليح", "Entretien et réparations"),
	a("616", "6", "expense", true, "Insurance", "التأمين", "Assurances"),
	a("625", "6", "expense", true, "Transport and travel", "النقل والتنقلات", "Transports et déplacements"),
	a("626", "6", "expense", true, "Telecom and internet", "الاتصالات والإنترنت", "Télécommunications et internet"),
	a("627", "6", "expense", true, "Bank charges", "المصاريف المصرفية", "Frais bancaires"),
	a("63", "6", "expense", true, "Taxes and duties", "الضرائب والرسوم", "Impôts et taxes"),
	a("641", "6", "expense", true, "Salaries and wages", "الرواتب والأجور", "Salaires et traitements"),
	a("645", "6", "expense", true, "Social charges", "الأعباء الاجتماعية", "Charges sociales"),
	a("666", "6", "expense", true, "Exchange losses", "خسائر فروقات الصرف", "Pertes de change"),
	a("68", "6", "expense", true, "Depreciation expense", "مخصصات الاستهلاكات", "Dotations aux amortissements"),

	a("7", "", "income", false, "Revenue", "الإيرادات", "Produits"),
	a("701", "7", "income", true, "Subscription revenue (electricity)", "إيرادات الاشتراكات (كهرباء)", "Abonnements (électricité)"),
	a("702", "7", "income", true, "Subscription revenue (internet / TV)", "إيرادات الاشتراكات (إنترنت / تلفزيون)", "Abonnements (internet / TV)"),
	a("706", "7", "income", true, "Services and installation fees", "الخدمات ورسوم التركيب", "Prestations et frais d'installation"),
	a("707", "7", "income", true, "Sales of goods", "مبيعات البضائع", "Ventes de marchandises"),
	a("766", "7", "income", true, "Exchange gains", "أرباح فروقات الصرف", "Gains de change"),
	a("77", "7", "income", true, "Other income", "إيرادات أخرى", "Autres produits"),
}

// Generic: a simple international chart (works for any country).
var generic = []Account{
	a("1000", "", "asset", false, "Assets", "الأصول", "Actifs"),
	a("1100", "1000", "asset", true, "Cash", "النقد", "Caisse"),
	a("1200", "1000", "asset", true, "Bank accounts", "الحسابات المصرفية", "Comptes bancaires"),
	a("1300", "1000", "asset", true, "Accounts receivable", "الذمم المدينة", "Créances clients"),
	a("1400", "1000", "asset", true, "Inventory", "المخزون", "Stocks"),
	a("1500", "1000", "asset", true, "Prepaid expenses", "مصاريف مدفوعة مقدماً", "Charges constatées d'avance"),
	a("1700", "1000", "asset", true, "Property and equipment", "الممتلكات والمعدات", "Immobilisations corporelles"),
	a("1750", "1000", "asset", true, "Accumulated depreciation", "مجمع الاستهلاك", "Amortissements cumulés"),
	a("2000", "", "liability", false, "Liabilities", "الالتزامات", "Passifs"),
	a("2100", "2000", "liability", true, "Accounts payable", "الذمم الدائنة", "Dettes fournisseurs"),
	a("2200", "2000", "liability", true, "Taxes payable", "الضرائب المستحقة", "Taxes à payer"),
	a("2300", "2000", "liability", true, "Salaries payable", "الرواتب المستحقة", "Salaires à payer"),
	a("2400", "2000", "liability", true, "Customer deposits", "تأمينات الزبائن", "Dépôts clients"),
	a("2700", "2000", "liability", true, "Loans", "القروض", "Emprunts"),
	a("3000", "", "equity", false, "Equity", "حقوق الملكية", "Capitaux propres"),
	a("3100", "3000", "equity", true, "Owner's capital", "رأس المال", "Capital"),
	a("3200", "3000", "equity", true, "Retained earnings", "الأرباح المحتجزة", "Résultats non distribués"),
	a("4000", "", "income", false, "Income", "الإيرادات", "Produits"),
	a("4100", "4000", "income", true, "Sales", "المبيعات", "Ventes"),
	a("4200", "4000", "income", true, "Service revenue", "إيرادات الخدمات", "Prestations de services"),
	a("4800", "4000", "income", true, "Exchange gains", "أرباح فروقات الصرف", "Gains de change"),
	a("4900", "4000", "income", true, "Other income", "إيرادات أخرى", "Autres produits"),
	a("5000", "", "expense", false, "Cost of sales", "تكلفة المبيعات", "Coût des ventes"),
	a("5100", "5000", "expense", true, "Cost of goods sold", "تكلفة البضاعة المباعة", "Coût des marchandises vendues"),
	a("6000", "", "expense", false, "Operating expenses", "المصاريف التشغيلية", "Charges d'exploitation"),
	a("6100", "6000", "expense", true, "Rent", "الإيجار", "Loyer"),
	a("6200", "6000", "expense", true, "Salaries", "الرواتب", "Salaires"),
	a("6300", "6000", "expense", true, "Utilities", "المرافق", "Services publics"),
	a("6400", "6000", "expense", true, "Maintenance", "الصيانة", "Entretien"),
	a("6500", "6000", "expense", true, "Bank charges", "المصاريف المصرفية", "Frais bancaires"),
	a("6600", "6000", "expense", true, "Depreciation", "الاستهلاك", "Amortissements"),
	a("6800", "6000", "expense", true, "Exchange losses", "خسائر فروقات الصرف", "Pertes de change"),
	a("6900", "6000", "expense", true, "Other expenses", "مصاريف أخرى", "Autres charges"),
}

var All = []Template{
	{
		Key: "lebanon", Names: map[string]string{"en": "Lebanon (PCG)", "ar": "لبنان (الخطة المحاسبية العامة)", "fr": "Liban (PCG)"},
		Accounts: lebanon, AR: "411", FXGain: "766", FXLoss: "666", Revenue: "701",
		AP: "401", Inventory: "31", COGS: "607", Fuel: "601", StockAdjust: "602", Expense: "602",
		Methods: []Method{
			m("cash", "USD", "531", "Cash USD", "صندوق دولار", "Caisse USD"),
			m("cash", "LBP", "532", "Cash LBP", "صندوق ليرة", "Caisse LBP"),
			m("wallet", "USD", "535", "Whish / OMT", "ويش / OMT", "Whish / OMT"),
			m("bank", "", "512", "Bank", "المصرف", "Banque"),
		},
	},
	{
		Key: "generic", Names: map[string]string{"en": "International (simple)", "ar": "دولي (مبسط)", "fr": "International (simple)"},
		Accounts: generic, AR: "1300", FXGain: "4800", FXLoss: "6800", Revenue: "4200",
		AP: "2100", Inventory: "1400", COGS: "5100", Fuel: "5100", StockAdjust: "6900", Expense: "6900",
		Methods: []Method{
			m("cash", "", "1100", "Cash", "الصندوق", "Caisse"),
			m("bank", "", "1200", "Bank", "المصرف", "Banque"),
		},
	},
}

func init() {
	for i := range All {
		All[i].Count = len(All[i].Accounts)
	}
}

func Find(key string) *Template {
	for i := range All {
		if All[i].Key == key {
			return &All[i]
		}
	}
	return nil
}
