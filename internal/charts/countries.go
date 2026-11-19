package charts

// Country localization packs (like Odoo's l10n modules): a chart of accounts, default taxes, the fiscal
// year, what printed invoices must say and the default currency. Rates and sources: docs/LOCALIZATION.md.
//
// Charts come in families that share their numbering:
//   - pcg:     French "plan comptable" classes 1–7 (France, Algeria SCF, Tunisia); Lebanon has its own (templates.go)
//   - cgnc:    Moroccan CGNC (classes 1–7, four-digit accounts)
//   - unified: the Arab unified accounting system (Syria, Iraq, Egypt): 1 assets, 2 liabilities & equity, 3 uses, 4 resources
//   - ifrs:    IFRS-style four-digit chart (Gulf states, Jordan, UK, US, any other country)
//   - tdhp:    Turkey's Tekdüzen Hesap Planı

// Tax is a default tax rate created with the pack. Sales / Purchase are account codes of the chart.
type Tax struct {
	Names     map[string]string `json:"names"`
	Rate      string            `json:"rate"`
	AppliesTo string            `json:"applies_to"` // sales | purchases | both
	Sales     string            `json:"-"`
	Purchase  string            `json:"-"`
	Default   bool              `json:"default"`
}

func tax(rate, en, ar, fr, sales, purchase string) Tax {
	return Tax{Names: map[string]string{"en": en, "ar": ar, "fr": fr}, Rate: rate, AppliesTo: "both", Sales: sales, Purchase: purchase, Default: true}
}

func n3(en, ar, fr string) map[string]string { return map[string]string{"en": en, "ar": ar, "fr": fr} }

// roles: which account of the chart the company settings point at (keys = companies.<role>_account_id).
type roles = map[string]string

// ---------------- IFRS-style chart ----------------

func ifrsChart(vatName string, socialEN, socialAR, socialFR string) ([]Account, roles) {
	acc := []Account{
		a("1", "", "asset", false, "Assets", "الأصول", "Actifs"),
		a("11", "1", "asset", false, "Current assets", "الأصول المتداولة", "Actifs courants"),
		a("1110", "11", "asset", true, "Cash on hand", "النقدية في الصندوق", "Caisse"),
		a("1120", "11", "asset", true, "Banks", "البنوك", "Banques"),
		a("1125", "11", "asset", true, "Cheques in hand", "شيكات في الصندوق", "Chèques en caisse"),
		a("1126", "11", "asset", true, "Cheques under collection", "شيكات برسم التحصيل", "Chèques à l'encaissement"),
		a("1130", "11", "asset", true, "Accounts receivable", "الذمم المدينة (العملاء)", "Créances clients"),
		a("1135", "11", "asset", true, "Employee advances", "سلف الموظفين", "Avances au personnel"),
		a("1140", "11", "asset", true, "Inventory and fuel", "المخزون والمحروقات", "Stocks et carburant"),
		a("1150", "11", "asset", true, vatName+" recoverable (input)", "ضريبة المدخلات القابلة للاسترداد", vatName+" déductible"),
		a("1160", "11", "asset", true, "Prepaid expenses", "مصاريف مدفوعة مقدماً", "Charges constatées d'avance"),
		a("12", "1", "asset", false, "Non-current assets", "الأصول غير المتداولة", "Actifs non courants"),
		a("1210", "12", "asset", true, "Generators, machinery and equipment", "المولدات والآلات والمعدات", "Groupes électrogènes, machines et matériel"),
		a("1220", "12", "asset", true, "Networks and cables", "الشبكات والكابلات", "Réseaux et câbles"),
		a("1230", "12", "asset", true, "Vehicles and furniture", "السيارات والأثاث", "Véhicules et mobilier"),
		a("1290", "12", "asset", true, "Accumulated depreciation", "مجمع الاستهلاك", "Amortissements cumulés"),
		a("2", "", "liability", false, "Liabilities", "الالتزامات", "Passifs"),
		a("21", "2", "liability", false, "Current liabilities", "الالتزامات المتداولة", "Passifs courants"),
		a("2110", "21", "liability", true, "Accounts payable", "الذمم الدائنة (الموردون)", "Dettes fournisseurs"),
		a("2115", "21", "liability", true, "Cheques payable", "شيكات صادرة برسم الدفع", "Chèques émis à payer"),
		a("2120", "21", "liability", true, vatName+" payable (output)", "ضريبة المخرجات المستحقة", vatName+" collectée"),
		a("2130", "21", "liability", true, "Salaries payable", "الرواتب المستحقة", "Salaires à payer"),
		a("2140", "21", "liability", true, socialEN+" payable", socialAR+" المستحقة", socialFR+" à payer"),
		a("2150", "21", "liability", true, "Customer deposits and advances", "تأمينات ودفعات الزبائن المقدمة", "Dépôts et avances clients"),
		a("2160", "21", "liability", true, "Income tax payable", "ضريبة الدخل المستحقة", "Impôt sur le résultat à payer"),
		a("22", "2", "liability", false, "Non-current liabilities", "الالتزامات غير المتداولة", "Passifs non courants"),
		a("2210", "22", "liability", true, "End-of-service benefits", "مخصص مكافأة نهاية الخدمة", "Indemnités de fin de service"),
		a("2220", "22", "liability", true, "Long-term loans", "القروض طويلة الأجل", "Emprunts à long terme"),
		a("3", "", "equity", false, "Equity", "حقوق الملكية", "Capitaux propres"),
		a("3100", "3", "equity", true, "Share capital", "رأس المال", "Capital social"),
		a("3200", "3", "equity", true, "Statutory reserve", "الاحتياطي النظامي", "Réserve légale"),
		a("3300", "3", "equity", true, "Retained earnings", "الأرباح المبقاة", "Résultats non distribués"),
		a("4", "", "income", false, "Revenue", "الإيرادات", "Produits"),
		a("4100", "4", "income", true, "Subscription revenue", "إيرادات الاشتراكات", "Revenus d'abonnements"),
		a("4200", "4", "income", true, "Sales of goods", "مبيعات البضائع", "Ventes de marchandises"),
		a("4300", "4", "income", true, "Services and installation", "الخدمات ورسوم التركيب", "Prestations et installation"),
		a("4800", "4", "income", true, "Exchange gains", "أرباح فروقات العملة", "Gains de change"),
		a("4900", "4", "income", true, "Other income", "إيرادات أخرى", "Autres produits"),
		a("5", "", "expense", false, "Expenses", "المصروفات", "Charges"),
		a("5100", "5", "expense", true, "Cost of goods sold", "تكلفة البضاعة المباعة", "Coût des marchandises vendues"),
		a("5200", "5", "expense", true, "Fuel and lubricants", "المحروقات والزيوت", "Carburant et lubrifiants"),
		a("5210", "5", "expense", true, "Spare parts and supplies", "قطع الغيار واللوازم", "Pièces et fournitures"),
		a("5300", "5", "expense", true, "Salaries and wages", "الرواتب والأجور", "Salaires et traitements"),
		a("5310", "5", "expense", true, socialEN+" (employer)", socialAR+" (حصة صاحب العمل)", socialFR+" (part patronale)"),
		a("5320", "5", "expense", true, "End-of-service expense", "مصروف نهاية الخدمة", "Charge de fin de service"),
		a("5400", "5", "expense", true, "Rent", "الإيجار", "Loyer"),
		a("5500", "5", "expense", true, "Maintenance and repairs", "الصيانة والإصلاح", "Entretien et réparations"),
		a("5600", "5", "expense", true, "Utilities and telecom", "المرافق والاتصالات", "Services publics et télécoms"),
		a("5700", "5", "expense", true, "Depreciation", "الاستهلاك", "Amortissements"),
		a("5800", "5", "expense", true, "Exchange losses", "خسائر فروقات العملة", "Pertes de change"),
		a("5850", "5", "expense", true, "Bank charges", "المصاريف البنكية", "Frais bancaires"),
		a("5860", "5", "expense", true, "Cash over / short", "فروقات الصندوق", "Écarts de caisse"),
		a("5900", "5", "expense", true, "Other expenses", "مصروفات أخرى", "Autres charges"),
	}
	return acc, roles{
		"ar": "1130", "fx_gain": "4800", "fx_loss": "5800", "default_revenue": "4100",
		"ap": "2110", "inventory": "1140", "cogs": "5100", "fuel": "5200", "stock_adjust": "5210", "default_expense": "5900",
		"salary_expense": "5300", "nssf_expense": "5310", "salary_payable": "2130", "nssf_payable": "2140", "advances": "1135",
		"depreciation_expense": "5700", "accum_depreciation": "1290", "asset_gain": "4900", "asset_loss": "5900",
		"retained_earnings": "3300", "cheque_in_hand": "1125", "cheque_collection": "1126", "cheque_payable": "2115",
	}
}

var ifrsMethods = []Method{
	m("cash", "", "1110", "Cash", "الصندوق", "Caisse"),
	m("bank", "", "1120", "Bank", "البنك", "Banque"),
}

// ---------------- French PCG family (France, Algeria SCF, Tunisia) ----------------

func pcgChart(variant string) ([]Account, roles) {
	cash, bank := "531", "512"
	if variant == "tn" { // Tunisia: 532 banks, 54 cash
		cash, bank = "54", "532"
	}
	acc := []Account{
		a("1", "", "equity", false, "Equity and long-term liabilities", "الأموال الدائمة", "Capitaux"),
		a("101", "1", "equity", true, "Capital", "رأس المال", "Capital"),
		a("106", "1", "equity", true, "Reserves", "الاحتياطيات", "Réserves"),
		a("11", "1", "equity", true, "Retained earnings", "النتائج المرحلة", "Report à nouveau"),
		a("164", "1", "liability", true, "Bank loans", "القروض المصرفية", "Emprunts auprès des établissements de crédit"),
		a("2", "", "asset", false, "Fixed assets", "الأصول الثابتة", "Immobilisations"),
		a("215", "2", "asset", true, "Machinery and generators", "الآلات والمولدات", "Installations techniques, matériel"),
		a("2181", "2", "asset", true, "Networks and fittings", "الشبكات والتجهيزات", "Installations et agencements"),
		a("2182", "2", "asset", true, "Vehicles", "السيارات", "Matériel de transport"),
		a("28", "2", "asset", true, "Accumulated depreciation", "مجمع الاستهلاكات", "Amortissements des immobilisations"),
		a("3", "", "asset", false, "Inventory", "المخزون", "Stocks"),
		a("31", "3", "asset", true, "Raw materials and fuel", "المواد الأولية والمحروقات", "Matières premières et carburant"),
		a("37", "3", "asset", true, "Goods for resale", "البضائع", "Marchandises"),
		a("4", "", "asset", false, "Third parties", "حسابات الغير", "Comptes de tiers"),
		a("401", "4", "liability", true, "Suppliers", "الموردون", "Fournisseurs"),
		a("403", "4", "liability", true, "Cheques payable", "شيكات صادرة برسم الدفع", "Effets et chèques à payer"),
		a("411", "4", "asset", true, "Customers", "الزبائن", "Clients"),
		a("4191", "4", "liability", true, "Customer advances and deposits", "تسبيقات وتأمينات الزبائن", "Avances et acomptes clients"),
		a("421", "4", "liability", true, "Salaries payable", "الرواتب المستحقة", "Personnel - rémunérations dues"),
		a("425", "4", "asset", true, "Employee advances", "تسبيقات للعاملين", "Personnel - avances et acomptes"),
		a("431", "4", "liability", true, "Social security", "الضمان الاجتماعي", "Sécurité sociale"),
		a("444", "4", "liability", true, "Income tax", "الضريبة على الأرباح", "État - impôt sur les bénéfices"),
		a("4456", "4", "asset", true, "VAT recoverable", "الرسم على القيمة المضافة القابل للاسترجاع", "TVA déductible"),
		a("4457", "4", "liability", true, "VAT collected", "الرسم على القيمة المضافة المحصل", "TVA collectée"),
		a("5", "", "asset", false, "Financial accounts", "الحسابات المالية", "Comptes financiers"),
		a(bank, "5", "asset", true, "Banks", "البنوك", "Banques"),
		a("5112", "5", "asset", true, "Cheques under collection", "شيكات برسم التحصيل", "Chèques à l'encaissement"),
		a("5113", "5", "asset", true, "Cheques in hand", "شيكات في الصندوق", "Chèques en portefeuille"),
		a(cash, "5", "asset", true, "Cash", "الصندوق", "Caisse"),
		a("6", "", "expense", false, "Expenses", "الأعباء", "Charges"),
		a("601", "6", "expense", true, "Fuel and raw materials consumed", "المحروقات والمواد المستهلكة", "Achats de matières et carburant"),
		a("602", "6", "expense", true, "Supplies and spare parts", "اللوازم وقطع الغيار", "Fournitures et pièces de rechange"),
		a("607", "6", "expense", true, "Cost of goods sold", "تكلفة البضائع المباعة", "Achats de marchandises vendues"),
		a("613", "6", "expense", true, "Rent", "الإيجارات", "Locations"),
		a("615", "6", "expense", true, "Maintenance and repairs", "الصيانة والتصليح", "Entretien et réparations"),
		a("626", "6", "expense", true, "Telecom", "الاتصالات", "Frais de télécommunications"),
		a("627", "6", "expense", true, "Bank charges", "المصاريف البنكية", "Services bancaires"),
		a("641", "6", "expense", true, "Salaries", "الأجور والرواتب", "Rémunérations du personnel"),
		a("645", "6", "expense", true, "Social charges", "الأعباء الاجتماعية", "Charges de sécurité sociale"),
		a("658", "6", "expense", true, "Cash over / short and other charges", "فروقات الصندوق وأعباء أخرى", "Écarts de caisse et charges diverses"),
		a("666", "6", "expense", true, "Exchange losses", "خسائر الصرف", "Pertes de change"),
		a("681", "6", "expense", true, "Depreciation expense", "مخصصات الاستهلاكات", "Dotations aux amortissements"),
		a("7", "", "income", false, "Revenue", "المنتوجات", "Produits"),
		a("706", "7", "income", true, "Services and subscriptions", "الخدمات والاشتراكات", "Prestations de services et abonnements"),
		a("707", "7", "income", true, "Sales of goods", "مبيعات البضائع", "Ventes de marchandises"),
		a("758", "7", "income", true, "Other income", "منتوجات أخرى", "Produits divers"),
		a("766", "7", "income", true, "Exchange gains", "أرباح الصرف", "Gains de change"),
	}
	return acc, roles{
		"ar": "411", "fx_gain": "766", "fx_loss": "666", "default_revenue": "706",
		"ap": "401", "inventory": "31", "cogs": "607", "fuel": "601", "stock_adjust": "602", "default_expense": "658",
		"salary_expense": "641", "nssf_expense": "645", "salary_payable": "421", "nssf_payable": "431", "advances": "425",
		"depreciation_expense": "681", "accum_depreciation": "28", "asset_gain": "758", "asset_loss": "658",
		"retained_earnings": "11", "cheque_in_hand": "5113", "cheque_collection": "5112", "cheque_payable": "403",
		"_cash": cash, "_bank": bank,
	}
}

// ---------------- Morocco CGNC ----------------

func cgncChart() ([]Account, roles) {
	acc := []Account{
		a("1", "", "equity", false, "Permanent financing", "التمويل الدائم", "Financement permanent"),
		a("1111", "1", "equity", true, "Share capital", "رأس المال الاجتماعي", "Capital social"),
		a("1140", "1", "equity", true, "Legal reserve", "الاحتياطي القانوني", "Réserve légale"),
		a("1161", "1", "equity", true, "Retained earnings", "المرحل من جديد", "Report à nouveau"),
		a("1481", "1", "liability", true, "Bank loans", "القروض البنكية", "Emprunts auprès des établissements de crédit"),
		a("2", "", "asset", false, "Fixed assets", "الأصول الثابتة", "Actif immobilisé"),
		a("2332", "2", "asset", true, "Machinery and generators", "الآلات والمولدات", "Matériel et outillage"),
		a("2340", "2", "asset", true, "Vehicles", "معدات النقل", "Matériel de transport"),
		a("2833", "2", "asset", true, "Accumulated depreciation", "مجمع الاهتلاكات", "Amortissements des installations techniques"),
		a("3", "", "asset", false, "Current assets", "الأصول المتداولة", "Actif circulant"),
		a("3111", "3", "asset", true, "Goods and fuel in stock", "بضائع ومحروقات", "Marchandises et carburant"),
		a("3421", "3", "asset", true, "Customers", "الزبناء", "Clients"),
		a("3431", "3", "asset", true, "Employee advances", "تسبيقات المستخدمين", "Avances et acomptes au personnel"),
		a("3455", "3", "asset", true, "VAT recoverable", "الضريبة على القيمة المضافة القابلة للاسترجاع", "État - TVA récupérable"),
		a("4", "", "liability", false, "Current liabilities", "الخصوم المتداولة", "Passif circulant"),
		a("4411", "4", "liability", true, "Suppliers", "الموردون", "Fournisseurs"),
		a("4415", "4", "liability", true, "Cheques payable", "أوراق الأداء", "Fournisseurs - effets à payer"),
		a("4421", "4", "liability", true, "Customer advances", "تسبيقات الزبناء", "Clients - avances et acomptes reçus"),
		a("4432", "4", "liability", true, "Salaries payable", "الأجور المستحقة", "Rémunérations dues au personnel"),
		a("4441", "4", "liability", true, "CNSS payable", "الصندوق الوطني للضمان الاجتماعي", "CNSS"),
		a("4453", "4", "liability", true, "Income tax payable", "الضريبة على الشركات", "État - impôts sur les résultats"),
		a("4455", "4", "liability", true, "VAT collected", "الضريبة على القيمة المضافة المفوترة", "État - TVA facturée"),
		a("5", "", "asset", false, "Treasury", "الخزينة", "Trésorerie"),
		a("5113", "5", "asset", true, "Cheques under collection", "شيكات للتحصيل", "Chèques à encaisser"),
		a("5114", "5", "asset", true, "Cheques in hand", "شيكات في الصندوق", "Chèques en portefeuille"),
		a("5141", "5", "asset", true, "Banks", "البنوك", "Banques"),
		a("5161", "5", "asset", true, "Cash", "الصندوق", "Caisse"),
		a("6", "", "expense", false, "Expenses", "التكاليف", "Charges"),
		a("6111", "6", "expense", true, "Cost of goods sold", "مشتريات البضائع المباعة", "Achats de marchandises"),
		a("6121", "6", "expense", true, "Fuel and materials consumed", "المحروقات والمواد المستهلكة", "Achats de matières et carburant"),
		a("6125", "6", "expense", true, "Supplies and spare parts", "اللوازم وقطع الغيار", "Achats non stockés de matières et fournitures"),
		a("6131", "6", "expense", true, "Rent", "الكراء", "Locations et charges locatives"),
		a("6133", "6", "expense", true, "Maintenance and repairs", "الصيانة والإصلاح", "Entretien et réparations"),
		a("6145", "6", "expense", true, "Telecom", "مصاريف البريد والاتصالات", "Frais postaux et télécommunications"),
		a("6147", "6", "expense", true, "Bank charges", "المصاريف البنكية", "Services bancaires"),
		a("6171", "6", "expense", true, "Salaries", "الأجور", "Rémunérations du personnel"),
		a("6174", "6", "expense", true, "Social charges", "التكاليف الاجتماعية", "Charges sociales"),
		a("6331", "6", "expense", true, "Exchange losses", "خسائر الصرف", "Pertes de change"),
		a("6193", "6", "expense", true, "Depreciation expense", "مخصصات الاهتلاك", "Dotations d'exploitation aux amortissements"),
		a("6585", "6", "expense", true, "Other expenses", "تكاليف أخرى", "Charges diverses"),
		a("7", "", "income", false, "Revenue", "العائدات", "Produits"),
		a("7111", "7", "income", true, "Sales of goods", "مبيعات البضائع", "Ventes de marchandises"),
		a("7124", "7", "income", true, "Services and subscriptions", "الخدمات والاشتراكات", "Prestations de services"),
		a("7331", "7", "income", true, "Exchange gains", "أرباح الصرف", "Gains de change"),
		a("7585", "7", "income", true, "Other income", "عائدات أخرى", "Produits divers"),
	}
	return acc, roles{
		"ar": "3421", "fx_gain": "7331", "fx_loss": "6331", "default_revenue": "7124",
		"ap": "4411", "inventory": "3111", "cogs": "6111", "fuel": "6121", "stock_adjust": "6125", "default_expense": "6585",
		"salary_expense": "6171", "nssf_expense": "6174", "salary_payable": "4432", "nssf_payable": "4441", "advances": "3431",
		"depreciation_expense": "6193", "accum_depreciation": "2833", "asset_gain": "7585", "asset_loss": "6585",
		"retained_earnings": "1161", "cheque_in_hand": "5114", "cheque_collection": "5113", "cheque_payable": "4415",
		"_cash": "5161", "_bank": "5141",
	}
}

// ---------------- Arab unified accounting system (Syria, Iraq, Egypt) ----------------

func unifiedChart() ([]Account, roles) {
	acc := []Account{
		a("1", "", "asset", false, "Assets", "الموجودات", "Actifs"),
		a("11", "1", "asset", false, "Fixed assets", "الموجودات الثابتة", "Immobilisations"),
		a("112", "11", "asset", true, "Machinery, generators and equipment", "الآلات والمولدات والمعدات", "Machines, groupes et matériel"),
		a("113", "11", "asset", true, "Networks and installations", "الشبكات والتجهيزات", "Réseaux et installations"),
		a("114", "11", "asset", true, "Vehicles", "وسائل النقل والانتقال", "Matériel de transport"),
		a("116", "11", "asset", true, "Furniture and office equipment", "الأثاث وأجهزة المكاتب", "Mobilier et matériel de bureau"),
		a("119", "11", "asset", true, "Accumulated depreciation", "مخصص الاندثار المتراكم", "Amortissements cumulés"),
		a("13", "1", "asset", false, "Inventory", "المخزون", "Stocks"),
		a("131", "13", "asset", true, "Fuel and materials", "المحروقات والمواد", "Carburant et matières"),
		a("136", "13", "asset", true, "Goods for resale", "بضائع بغرض البيع", "Marchandises"),
		a("16", "1", "asset", false, "Debtors", "المدينون", "Débiteurs"),
		a("161", "16", "asset", true, "Customers", "العملاء", "Clients"),
		a("163", "16", "asset", true, "Employee advances", "سلف العاملين", "Avances au personnel"),
		a("166", "16", "asset", true, "Input tax recoverable", "ضريبة المدخلات القابلة للخصم", "Taxe déductible"),
		a("18", "1", "asset", false, "Cash and banks", "النقود", "Trésorerie"),
		a("181", "18", "asset", true, "Cash on hand", "النقدية في الصندوق", "Caisse"),
		a("183", "18", "asset", true, "Banks", "النقدية لدى المصارف", "Banques"),
		a("185", "18", "asset", true, "Cheques in hand", "شيكات في الصندوق", "Chèques en caisse"),
		a("186", "18", "asset", true, "Cheques under collection", "شيكات برسم التحصيل", "Chèques à l'encaissement"),
		a("2", "", "liability", false, "Liabilities and equity", "المطلوبات", "Passifs et capitaux"),
		a("21", "2", "equity", true, "Capital", "رأس المال", "Capital"),
		a("22", "2", "equity", false, "Reserves", "الاحتياطيات", "Réserves"),
		a("221", "22", "equity", true, "Statutory reserve", "الاحتياطي الإلزامي", "Réserve légale"),
		a("225", "22", "equity", true, "Retained surplus", "الفائض المتراكم", "Excédent cumulé"),
		a("26", "2", "liability", false, "Creditors", "الدائنون", "Créditeurs"),
		a("261", "26", "liability", true, "Suppliers", "الموردون", "Fournisseurs"),
		a("262", "26", "liability", true, "Cheques payable", "أوراق الدفع", "Effets à payer"),
		a("263", "26", "liability", true, "Salaries payable", "الرواتب المستحقة", "Salaires à payer"),
		a("264", "26", "liability", true, "Social security payable", "الضمان الاجتماعي المستحق", "Sécurité sociale à payer"),
		a("265", "26", "liability", true, "Customer deposits", "تأمينات مستلمة", "Dépôts reçus"),
		a("266", "26", "liability", true, "Taxes payable (output tax)", "الضرائب المستحقة (ضريبة المخرجات)", "Taxes à payer"),
		a("27", "2", "liability", true, "Long-term loans", "القروض طويلة الأجل", "Emprunts à long terme"),
		a("3", "", "expense", false, "Uses (expenses)", "الاستخدامات", "Emplois (charges)"),
		a("31", "3", "expense", true, "Wages and salaries", "الرواتب والأجور", "Salaires"),
		a("315", "3", "expense", true, "Social security contributions", "مساهمة في الضمان الاجتماعي", "Cotisations sociales"),
		a("322", "3", "expense", true, "Fuel and oils", "الوقود والزيوت", "Carburant et huiles"),
		a("324", "3", "expense", true, "Spare parts and supplies", "قطع الغيار واللوازم", "Pièces et fournitures"),
		a("331", "3", "expense", true, "Maintenance services", "خدمات الصيانة", "Services d'entretien"),
		a("333", "3", "expense", true, "Rent", "الإيجارات", "Loyers"),
		a("334", "3", "expense", true, "Telecom", "الاتصالات", "Télécommunications"),
		a("34", "3", "expense", true, "Cost of goods sold", "تكلفة البضائع المباعة", "Coût des marchandises vendues"),
		a("37", "3", "expense", true, "Depreciation", "الاندثارات", "Amortissements"),
		a("383", "3", "expense", true, "Exchange losses", "خسائر فروق العملة", "Pertes de change"),
		a("39", "3", "expense", true, "Other expenses", "مصروفات أخرى", "Autres charges"),
		a("4", "", "income", false, "Resources (revenue)", "الموارد", "Ressources (produits)"),
		a("411", "4", "income", true, "Subscription revenue", "إيرادات الاشتراكات", "Revenus d'abonnements"),
		a("412", "4", "income", true, "Sales of goods", "مبيعات البضائع", "Ventes de marchandises"),
		a("413", "4", "income", true, "Services and installation", "إيرادات الخدمات والتركيب", "Services et installation"),
		a("465", "4", "income", true, "Exchange gains", "أرباح فروق العملة", "Gains de change"),
		a("49", "4", "income", true, "Other income", "إيرادات أخرى", "Autres produits"),
	}
	return acc, roles{
		"ar": "161", "fx_gain": "465", "fx_loss": "383", "default_revenue": "411",
		"ap": "261", "inventory": "131", "cogs": "34", "fuel": "322", "stock_adjust": "324", "default_expense": "39",
		"salary_expense": "31", "nssf_expense": "315", "salary_payable": "263", "nssf_payable": "264", "advances": "163",
		"depreciation_expense": "37", "accum_depreciation": "119", "asset_gain": "49", "asset_loss": "39",
		"retained_earnings": "225", "cheque_in_hand": "185", "cheque_collection": "186", "cheque_payable": "262",
		"_cash": "181", "_bank": "183",
	}
}

// ---------------- Turkey TDHP ----------------

func at(code, parent, typ string, postable bool, en, ar, fr, tr string) Account {
	return Account{Code: code, Parent: parent, Type: typ, Postable: postable, Names: map[string]string{"en": en, "ar": ar, "fr": fr, "tr": tr}}
}

func tdhpChart() ([]Account, roles) {
	acc := []Account{
		at("1", "", "asset", false, "Current assets", "الأصول المتداولة", "Actifs circulants", "Dönen Varlıklar"),
		at("100", "1", "asset", true, "Cash", "الصندوق", "Caisse", "Kasa"),
		at("101", "1", "asset", true, "Cheques received", "الشيكات المستلمة", "Chèques reçus", "Alınan Çekler"),
		at("1011", "1", "asset", true, "Cheques under collection", "شيكات برسم التحصيل", "Chèques à l'encaissement", "Tahsildeki Çekler"),
		at("102", "1", "asset", true, "Banks", "البنوك", "Banques", "Bankalar"),
		at("120", "1", "asset", true, "Customers", "العملاء", "Clients", "Alıcılar"),
		at("153", "1", "asset", true, "Goods and fuel", "البضائع والمحروقات", "Marchandises et carburant", "Ticari Mallar"),
		at("191", "1", "asset", true, "VAT recoverable", "ضريبة القيمة المضافة القابلة للخصم", "TVA déductible", "İndirilecek KDV"),
		at("196", "1", "asset", true, "Employee advances", "سلف الموظفين", "Avances au personnel", "Personel Avansları"),
		at("2", "", "asset", false, "Non-current assets", "الأصول غير المتداولة", "Actifs non courants", "Duran Varlıklar"),
		at("253", "2", "asset", true, "Machinery and generators", "الآلات والمولدات", "Installations et machines", "Tesis, Makine ve Cihazlar"),
		at("254", "2", "asset", true, "Vehicles", "السيارات", "Véhicules", "Taşıtlar"),
		at("257", "2", "asset", true, "Accumulated depreciation", "مجمع الاستهلاك", "Amortissements cumulés", "Birikmiş Amortismanlar"),
		at("3", "", "liability", false, "Short-term liabilities", "الالتزامات قصيرة الأجل", "Dettes à court terme", "Kısa Vadeli Yabancı Kaynaklar"),
		at("103", "3", "liability", true, "Cheques issued", "الشيكات الصادرة", "Chèques émis", "Verilen Çekler ve Ödeme Emirleri"),
		at("320", "3", "liability", true, "Suppliers", "الموردون", "Fournisseurs", "Satıcılar"),
		at("335", "3", "liability", true, "Salaries payable", "الرواتب المستحقة", "Salaires à payer", "Personele Borçlar"),
		at("340", "3", "liability", true, "Customer advances", "دفعات مقدمة من العملاء", "Avances clients", "Alınan Sipariş Avansları"),
		at("361", "3", "liability", true, "Social security payable (SGK)", "الضمان الاجتماعي المستحق", "Sécurité sociale à payer", "Ödenecek Sosyal Güvenlik Kesintileri"),
		at("391", "3", "liability", true, "VAT collected", "ضريبة القيمة المضافة المحصلة", "TVA collectée", "Hesaplanan KDV"),
		at("4", "", "liability", false, "Long-term liabilities", "الالتزامات طويلة الأجل", "Dettes à long terme", "Uzun Vadeli Yabancı Kaynaklar"),
		at("400", "4", "liability", true, "Bank loans", "القروض البنكية", "Emprunts bancaires", "Banka Kredileri"),
		at("5", "", "equity", false, "Equity", "حقوق الملكية", "Capitaux propres", "Özkaynaklar"),
		at("500", "5", "equity", true, "Capital", "رأس المال", "Capital", "Sermaye"),
		at("540", "5", "equity", true, "Legal reserves", "الاحتياطيات القانونية", "Réserves légales", "Yasal Yedekler"),
		at("570", "5", "equity", true, "Retained earnings", "أرباح السنوات السابقة", "Report à nouveau", "Geçmiş Yıllar Karları"),
		at("6", "", "income", false, "Income statement", "قائمة الدخل", "Compte de résultat", "Gelir Tablosu Hesapları"),
		at("600", "6", "income", true, "Domestic sales and subscriptions", "المبيعات والاشتراكات المحلية", "Ventes et abonnements", "Yurtiçi Satışlar"),
		at("602", "6", "income", true, "Other revenue", "إيرادات أخرى", "Autres revenus", "Diğer Gelirler"),
		at("621", "6", "expense", true, "Cost of goods sold", "تكلفة البضاعة المباعة", "Coût des marchandises vendues", "Satılan Ticari Mallar Maliyeti"),
		at("646", "6", "income", true, "Exchange gains", "أرباح فروقات الصرف", "Gains de change", "Kambiyo Karları"),
		at("656", "6", "expense", true, "Exchange losses", "خسائر فروقات الصرف", "Pertes de change", "Kambiyo Zararları"),
		at("659", "6", "expense", true, "Other expenses", "مصاريف أخرى", "Autres charges", "Diğer Olağan Gider ve Zararlar"),
		at("7", "", "expense", false, "Cost accounts", "حسابات التكاليف", "Comptes de coûts", "Maliyet Hesapları"),
		at("7701", "7", "expense", true, "Salaries", "الرواتب", "Salaires", "Personel Ücretleri"),
		at("7702", "7", "expense", true, "Social security (employer)", "الضمان الاجتماعي (حصة صاحب العمل)", "Charges sociales", "SGK İşveren Payı"),
		at("7703", "7", "expense", true, "Fuel", "المحروقات", "Carburant", "Akaryakıt"),
		at("7704", "7", "expense", true, "Rent", "الإيجار", "Loyer", "Kira"),
		at("7705", "7", "expense", true, "Maintenance and parts", "الصيانة وقطع الغيار", "Entretien et pièces", "Bakım Onarım"),
		at("7706", "7", "expense", true, "Depreciation", "الاستهلاك", "Amortissements", "Amortisman Giderleri"),
		at("7707", "7", "expense", true, "Bank charges", "المصاريف البنكية", "Frais bancaires", "Banka Masrafları"),
		at("7709", "7", "expense", true, "General expenses", "مصاريف عامة", "Charges générales", "Genel Yönetim Giderleri"),
	}
	return acc, roles{
		"ar": "120", "fx_gain": "646", "fx_loss": "656", "default_revenue": "600",
		"ap": "320", "inventory": "153", "cogs": "621", "fuel": "7703", "stock_adjust": "7705", "default_expense": "7709",
		"salary_expense": "7701", "nssf_expense": "7702", "salary_payable": "335", "nssf_payable": "361", "advances": "196",
		"depreciation_expense": "7706", "accum_depreciation": "257", "asset_gain": "602", "asset_loss": "659",
		"retained_earnings": "570", "cheque_in_hand": "101", "cheque_collection": "1011", "cheque_payable": "103",
		"_cash": "100", "_bank": "102",
	}
}

func familyMethods(r roles) []Method {
	return []Method{
		m("cash", "", r["_cash"], "Cash", "الصندوق", "Caisse"),
		m("bank", "", r["_bank"], "Bank", "البنك", "Banque"),
	}
}

// ---------------- packs ----------------

func pack(key, country, currency string, names map[string]string, acc []Account, r roles, methods []Method) Template {
	return Template{Key: key, Country: country, Currency: currency, Names: names, Accounts: acc, Roles: r, Methods: methods,
		AR: r["ar"], FXGain: r["fx_gain"], FXLoss: r["fx_loss"], Revenue: r["default_revenue"],
		AP: r["ap"], Inventory: r["inventory"], COGS: r["cogs"], Fuel: r["fuel"], StockAdjust: r["stock_adjust"], Expense: r["default_expense"],
		FiscalStart: "01-01"}
}

var vatLabel = n3("VAT number", "الرقم الضريبي", "Numéro de TVA")

func gccPack(key, country, currency, rate string, names, taxID, invoiceTitle map[string]string, social [3]string, einvoice string) Template {
	acc, r := ifrsChart("VAT", social[0], social[1], social[2])
	t := pack(key, country, currency, names, acc, r, ifrsMethods)
	t.TaxIDLabel = taxID
	t.InvoiceTitle = invoiceTitle
	t.EInvoice = einvoice
	if rate != "" {
		t.Taxes = []Tax{tax(rate, "VAT "+rate+"%", "ضريبة القيمة المضافة "+rate+"%", "TVA "+rate+"%", "2120", "1150")}
	}
	return t
}

var taxInvoice = n3("Tax invoice", "فاتورة ضريبية", "Facture fiscale")

func countryPacks() []Template {
	gosi := [3]string{"GOSI", "التأمينات الاجتماعية", "GOSI"}
	social := [3]string{"Social insurance", "التأمينات الاجتماعية", "Assurances sociales"}
	out := []Template{
		gccPack("sa", "SA", "SAR", "15", n3("Saudi Arabia", "السعودية", "Arabie saoudite"),
			n3("VAT registration number", "الرقم الضريبي", "Numéro d'enregistrement TVA"), taxInvoice, gosi, "zatca"),
		gccPack("ae", "AE", "AED", "5", n3("United Arab Emirates", "الإمارات العربية المتحدة", "Émirats arabes unis"),
			n3("TRN", "رقم التسجيل الضريبي", "TRN"), taxInvoice, [3]string{"GPSSA pension", "المعاشات والتأمينات", "GPSSA"}, ""),
		gccPack("bh", "BH", "BHD", "10", n3("Bahrain", "البحرين", "Bahreïn"), vatLabel, taxInvoice, [3]string{"SIO", "الهيئة العامة للتأمين الاجتماعي", "SIO"}, ""),
		gccPack("om", "OM", "OMR", "5", n3("Oman", "عُمان", "Oman"), vatLabel, taxInvoice, [3]string{"SPF", "صندوق الحماية الاجتماعية", "SPF"}, ""),
		gccPack("kw", "KW", "KWD", "", n3("Kuwait", "الكويت", "Koweït"), n3("Commercial licence", "الرخصة التجارية", "Licence commerciale"), nil, [3]string{"PIFSS", "التأمينات الاجتماعية", "PIFSS"}, ""),
		gccPack("qa", "QA", "QAR", "", n3("Qatar", "قطر", "Qatar"), n3("Tax card number", "رقم البطاقة الضريبية", "Numéro de carte fiscale"), nil, [3]string{"GRSIA", "الهيئة العامة للتقاعد", "GRSIA"}, ""),
		gccPack("jo", "JO", "JOD", "16", n3("Jordan", "الأردن", "Jordanie"), n3("Sales tax number", "رقم ضريبة المبيعات", "Numéro de taxe sur les ventes"),
			n3("Sales tax invoice", "فاتورة ضريبة مبيعات", "Facture de taxe sur les ventes"), [3]string{"SSC", "الضمان الاجتماعي", "SSC"}, ""),
	}
	// Jordan's tax is the General Sales Tax
	out[len(out)-1].Taxes[0].Names = n3("GST 16%", "ضريبة المبيعات العامة 16%", "TGV 16%")

	// unified Arab chart
	for _, c := range []struct {
		key, iso, cur, rate string
		names               map[string]string
		taxID               map[string]string
	}{
		{"sy", "SY", "SYP", "", n3("Syria", "سوريا", "Syrie"), n3("Tax number", "الرقم الضريبي", "Numéro fiscal")},
		{"iq", "IQ", "IQD", "", n3("Iraq", "العراق", "Irak"), n3("Tax number", "الرقم الضريبي", "Numéro fiscal")},
		{"eg", "EG", "EGP", "14", n3("Egypt", "مصر", "Égypte"), n3("Tax registration number", "رقم التسجيل الضريبي", "Numéro d'enregistrement fiscal")},
	} {
		acc, r := unifiedChart()
		t := pack(c.key, c.iso, c.cur, c.names, acc, r, familyMethods(r))
		t.TaxIDLabel = c.taxID
		if c.rate != "" {
			t.Taxes = []Tax{tax(c.rate, "VAT "+c.rate+"%", "ضريبة القيمة المضافة "+c.rate+"%", "TVA "+c.rate+"%", "266", "166")}
			t.InvoiceTitle = taxInvoice
			t.EInvoice = "eta"
		}
		out = append(out, t)
	}

	// French PCG family
	for _, c := range []struct {
		key, iso, cur, rate, variant string
		names, taxID                 map[string]string
	}{
		{"fr", "FR", "EUR", "20", "fr", n3("France", "فرنسا", "France"), n3("VAT number", "رقم ضريبة القيمة المضافة", "N° TVA intracommunautaire")},
		{"dz", "DZ", "DZD", "19", "dz", n3("Algeria", "الجزائر", "Algérie"), n3("Tax ID (NIF)", "رقم التعريف الجبائي", "NIF")},
		{"tn", "TN", "TND", "19", "tn", n3("Tunisia", "تونس", "Tunisie"), n3("Tax ID (matricule fiscal)", "المعرف الجبائي", "Matricule fiscal")},
	} {
		acc, r := pcgChart(c.variant)
		t := pack(c.key, c.iso, c.cur, c.names, acc, r, familyMethods(r))
		t.TaxIDLabel = c.taxID
		t.Taxes = []Tax{tax(c.rate, "VAT "+c.rate+"%", "الرسم على القيمة المضافة "+c.rate+"%", "TVA "+c.rate+"%", "4457", "4456")}
		if c.key == "fr" {
			t.Mentions = n3("Late payment: penalties at three times the legal interest rate and a fixed €40 recovery fee.",
				"غرامات التأخير: ثلاثة أضعاف سعر الفائدة القانوني وتعويض ثابت قدره 40 يورو.",
				"Pénalités de retard : trois fois le taux d'intérêt légal ; indemnité forfaitaire pour frais de recouvrement : 40 €.")
		}
		out = append(out, t)
	}

	// Morocco
	{
		acc, r := cgncChart()
		t := pack("ma", "MA", "MAD", n3("Morocco", "المغرب", "Maroc"), acc, r, familyMethods(r))
		t.TaxIDLabel = n3("ICE", "التعريف الموحد للمقاولة", "ICE")
		t.Taxes = []Tax{tax("20", "VAT 20%", "الضريبة على القيمة المضافة 20%", "TVA 20%", "4455", "3455")}
		out = append(out, t)
	}

	// Turkey
	{
		acc, r := tdhpChart()
		t := pack("tr", "TR", "TRY", map[string]string{"en": "Turkey (TDHP)", "ar": "تركيا", "fr": "Turquie", "tr": "Türkiye (TDHP)"}, acc, r, familyMethods(r))
		t.TaxIDLabel = map[string]string{"en": "Tax number (VKN)", "ar": "الرقم الضريبي", "fr": "Numéro fiscal", "tr": "Vergi Kimlik No"}
		t.Taxes = []Tax{{Names: map[string]string{"en": "VAT 20%", "ar": "ضريبة القيمة المضافة 20%", "fr": "TVA 20%", "tr": "KDV %20"},
			Rate: "20", AppliesTo: "both", Sales: "391", Purchase: "191", Default: true}}
		out = append(out, t)
	}

	// UK, US, IFRS (any other country)
	{
		acc, r := ifrsChart("VAT", "National Insurance", "التأمين الوطني", "Assurance nationale")
		t := pack("gb", "GB", "GBP", n3("United Kingdom", "المملكة المتحدة", "Royaume-Uni"), acc, r, ifrsMethods)
		t.TaxIDLabel = n3("VAT registration number", "رقم التسجيل في ضريبة القيمة المضافة", "Numéro de TVA")
		t.FiscalStart = "04-01" // UK companies most often run April–March (any start can be set per company)
		t.Taxes = []Tax{tax("20", "VAT 20%", "ضريبة القيمة المضافة 20%", "TVA 20%", "2120", "1150")}
		out = append(out, t)
	}
	{
		acc, r := ifrsChart("Sales tax", "Payroll taxes", "ضرائب الرواتب", "Charges sociales")
		t := pack("us", "US", "USD", n3("United States", "الولايات المتحدة", "États-Unis"), acc, r, ifrsMethods)
		t.TaxIDLabel = n3("EIN", "رقم صاحب العمل", "EIN")
		// no national VAT: a 0% sales-tax rate to edit per state
		t.Taxes = []Tax{{Names: n3("Sales tax", "ضريبة المبيعات", "Taxe de vente"), Rate: "0", AppliesTo: "sales", Sales: "2120", Purchase: "2120"}}
		out = append(out, t)
	}
	{
		acc, r := ifrsChart("VAT", social[0], social[1], social[2])
		t := pack("ifrs", "", "USD", n3("Other country (IFRS)", "دولة أخرى (المعايير الدولية)", "Autre pays (IFRS)"), acc, r, ifrsMethods)
		t.TaxIDLabel = n3("Tax number", "الرقم الضريبي", "Numéro fiscal")
		t.Taxes = []Tax{{Names: n3("VAT", "ضريبة القيمة المضافة", "TVA"), Rate: "0", AppliesTo: "both", Sales: "2120", Purchase: "1150"}}
		out = append(out, t)
	}
	return out
}
