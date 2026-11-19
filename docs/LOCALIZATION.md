# Localization: countries, currencies, languages

## Country packs

A company is created from a **country pack** (sign-up: "Country"; later companies: Accounts → load template).
The pack lives in `internal/charts` (`templates.go` for Lebanon / generic, `countries.go` for the rest) and sets:

- the chart of accounts (names in English, Arabic, French; Turkish too for Turkey),
- the account **roles** the company settings point at (receivables, payables, FX gain/loss, inventory, cost of
  sales, fuel, salaries, social security, depreciation, retained earnings, cheque accounts…),
- the default tax rate(s), the fiscal year start, the label of the company tax number, the invoice title
  ("Tax invoice" where the law asks for it) and legal mentions,
- the e-invoicing scheme (`zatca`, `eta`) and the default currency proposed at sign-up.

Everything can be edited afterwards (Settings → Country & language; Accounts; Tax rates).

| Pack | Chart family | Currency | Default tax (2026) |
|---|---|---|---|
| `lebanon` Lebanon | Lebanese PCG | USD / LBP | VAT 11% |
| `sa` Saudi Arabia | IFRS-style | SAR | VAT 15% · ZATCA QR |
| `ae` United Arab Emirates | IFRS-style | AED | VAT 5% |
| `bh` Bahrain | IFRS-style | BHD | VAT 10% |
| `om` Oman | IFRS-style | OMR | VAT 5% |
| `kw` Kuwait | IFRS-style | KWD | none (no VAT in force) |
| `qa` Qatar | IFRS-style | QAR | none (no VAT in force) |
| `jo` Jordan | IFRS-style | JOD | General Sales Tax 16% |
| `sy` Syria | Arab unified system | USD / SYP | none (no VAT) |
| `iq` Iraq | Arab unified system | USD / IQD | none (no VAT) |
| `eg` Egypt | Arab unified system | EGP | VAT 14% |
| `ma` Morocco | CGNC | MAD | TVA 20% |
| `dz` Algeria | PCG / SCF | DZD | TVA 19% |
| `tn` Tunisia | PCG | TND | TVA 19% |
| `fr` France | PCG | EUR | TVA 20% (+ late-payment mention) |
| `tr` Turkey | TDHP | TRY | KDV 20% |
| `gb` United Kingdom | IFRS-style | GBP | VAT 20% |
| `us` United States | IFRS-style | USD | sales tax 0% (set per state) |
| `ifrs` any other country | IFRS-style | USD | VAT 0% (to set) |

Reduced rates (Egypt 5%, Morocco 14/10/7%, Algeria 9%, Tunisia 13/7%, …) are added by the company as extra tax rates.

Sources for the 2026 standard rates (checked November 2026; confirm with the tax authority before filing):
- Global and GCC comparisons: https://www.vatupdate.com/2026/07/25/global-vat-rates-by-country-2026-standard-and-reduced-rates/ ·
  https://taxnews.ae/gcc-countries-and-vat-in-2026/ · https://www.taxinme.com/articles/gcc-vat-comparison-2026.php ·
  https://www.cleartax.com/global/vat-rates-by-country · https://vatinfo.org/countries/
- Lebanon 11% (a proposal for 12% was not enacted): https://taxsummaries.pwc.com/lebanon/corporate/other-taxes ·
  https://www.vatupdate.com/2026/02/26/lebanon-plans-vat-hike-from-11-to-12-pending-parliamentary-approval/

## E-invoicing

- **Saudi Arabia (ZATCA phase 1):** printed invoices of a company whose pack is `sa` carry the QR code: TLV
  (tag-length-value) fields 1 seller name, 2 VAT number, 3 time stamp (ISO 8601), 4 total with VAT, 5 VAT total,
  base64-encoded (`web/src/zatca.ts`). The VAT number is the company tax number (document settings).
- **Saudi Arabia phase 2 (integration / "clearance"):** future work. Needs: UBL 2.1 XML invoice per document,
  ECDSA (secp256k1) signature with a ZATCA-issued certificate (CSID, obtained by onboarding with an OTP),
  invoice hash chain (previous invoice hash), UUID per invoice, and calls to the ZATCA API (reporting for
  simplified invoices within 24 h, clearance before issuing standard B2B invoices). Design: a `zatca_devices`
  table (CSID, private key encrypted with `internal/crypto`), an outbox like the message worker, the signed XML
  and the cleared QR stored on the invoice.
- **Egypt (ETA e-invoice / e-receipt):** future work. Needs: taxpayer registration, item codes (GS1 / EGS),
  JSON documents signed with the company's e-seal (HSM / USB token), submission to the ETA API and the returned
  UUID / long id on the printed invoice. Same outbox design as ZATCA phase 2.

## Currencies

Every active ISO 4217 currency is available (`currencies`, migration 00028) with its minor units (KWD, BHD, OMR,
JOD, TND, LYD, IQD: 3; JPY, KRW, CLP, XOF, XAF, …: 0; LBP, SYP, IRR, YER: 0 because no coins circulate) and names
in English, Arabic and French.

**Automatic rates** (Accounting → Rates): off by default. When on, the published rates of
[open.er-api.com](https://open.er-api.com) (free, daily, base USD; only the request "latest/USD" is sent) are saved
once a day as the official rate of every currency the company works with, marked `auto`. A rate typed by a user is
never overwritten, and currencies listed as "always typed by hand" (default LBP: the market rate is not the
published one) are never fetched. "Fetch now" does it on demand.

**Presentation currency:** trial balance, profit & loss and balance sheet accept `?currency=XXX`; amounts are
translated at the official rate of the report date (the books stay in the base currency).

## Languages

- Shipped and reviewed: Arabic, English, French (`web/src/locales/*.ts`).
- Machine translations (to review): Turkish, Spanish, German, Portuguese, Russian, Persian (RTL), Urdu (RTL),
  Kurdish Sorani `ckb` (RTL) — `web/src/locales/extra/<code>.json`, made by `node web/scripts/translate.mjs`
  (incremental: only new keys are translated; delete a key to translate it again). Missing keys fall back to
  English (Kurdish: Arabic, then English).
- Each user picks a language; each customer can have one (messages, portal); the company has a default for
  customers (Settings → Country & language).
- **Corrections per organisation:** Settings → Translations: any interface text, any language. Stored in
  `ui_translations`, loaded after sign-in, they win over the shipped text.
- Adding a language: add it to `languages` in `web/src/i18n.ts` and `supportedLocales` in
  `internal/httpapi/server.go`, then run the translate script.
