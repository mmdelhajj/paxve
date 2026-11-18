# Migration from Magic

Magic is the legacy VB.NET + SQL Server 2008 R2 program (database `ManagmentDBElec`) that Paxve replaces.
This page explains how its data moves into Paxve, table by table.

**Paxve never connects to the Magic server.** The owner runs a read-only export script on the Magic
computer, then uploads the zip it creates in Paxve under **Settings → Import from Magic**.

```
Magic computer                                   paxve.com
┌──────────────────────────┐   magic-export-…zip  ┌───────────────────────────────────────────────┐
│ Export-Magic.ps1         │ ───────────────────▶ │ upload (2 MB pieces, SHA-256 checked)         │
│  SELECT only, 14 queries │                      │ dry run per Magic company (counts, warnings)  │
│  journal summed per year │                      │ choose: new company / existing / skip         │
└──────────────────────────┘                      │ import = ONE transaction (all or nothing)     │
                                                  └───────────────────────────────────────────────┘
```

The code lives here:

| Part | Where |
|---|---|
| Export script | `tools/magic-export/Export-Magic.ps1`. The page also offers it for download at `/tools/Export-Magic.ps1` |
| Reading the zip and computing the plan | `internal/magic` (`Parse`, `Build`): pure Go with unit tests |
| Writing the plan | `internal/httpapi/magic.go` |
| API | `/api/v1/magic/imports…` (admin, session only) |
| Page | `web/src/pages/MagicImport.tsx` (`#/magic-import`) |
| Test data | `paxve magic-sample out.zip` builds a synthetic Magic export with Arabic names and every edge case below |

## 1. Running the export (owner)

1. In Paxve open **Settings → Import from Magic** and click **Download Export-Magic.ps1**. Copy the file to the Magic computer.
2. On that computer open PowerShell in the folder that holds the file, then run:
   ```powershell
   powershell -ExecutionPolicy Bypass -File .\Export-Magic.ps1
   ```
3. When asked, type the SQL Server instance (for example `.`, `.\SQLEXPRESS` or `ACCOUNTNEW`).
   - By default the script signs in with your Windows login.
   - To use a SQL login instead, add `-SqlLogin`. The password is asked in a secure prompt and kept in memory only.
4. The script prints each table and its row count, then writes `magic-export-YYYYMMDD-HHMM.zip` on the Desktop and shows its SHA-256.
5. Upload that file in Paxve.

Options:

| Option | Meaning |
|---|---|
| `-Database` | Database name (default `ManagmentDBElec`) |
| `-Months` | How many months of bills to export, for the meter-reading history (default 24). Every unpaid bill is always exported |
| `-Out` | Path of the zip file |
| `-NoLock` | Read with `READ UNCOMMITTED` so Magic users are never blocked. The default is `READ COMMITTED`. With either setting, run the export when nobody is typing in Magic, so the bills and the journal match |

What the script guarantees:

- It runs `SELECT` statements only, each prefixed with `SET TRANSACTION ISOLATION LEVEL`.
- It refuses to run any statement that contains a data-changing keyword.
- It never stores the password.
- It does not export these secrets:
  - users' passwords (`Users` is not read);
  - VOD logins and passwords;
  - satellite receiver and card numbers;
  - the collectors' SMS gateway keys (`apiKey`, `gateway`).

The zip still contains customer names and phone numbers. Keep it private and delete it after the import.

Tested against SQL Server: the synthetic sample was loaded into a database that uses Magic's real table
definitions (`legacy/schema.sql`), and the script was run on it. Its zip gives exactly the same migration plan
as the sample itself (`go test ./internal/magic -run TestExportScriptZip`).

## 2. What is exported

| File | Source | Columns kept |
|---|---|---|
| `Company.csv` | `Company` | CompanyID, CompanyName, CompanyTypeID, Available |
| `Branchs.csv` | `Branchs` | BranchID, BranchName, CompanyID |
| `Stations.csv` | `Stations` | StationID, Station, BranchID, Available |
| `Location.csv` | `Location` (boxes / areas) | LocationID, Location, StationID, DefaultCollectorID, Available, Adress, LocOrder |
| `Collector.csv` | `Collector` + `AccountNames` | CollectorID, AccountNameID, CompanyID, Activate, DefaultC, AccountName, CollectorName (**no apiKey / gateway**) |
| `Money.csv` | `Money` | MoneyID, Money, DolBource, LLBource, CashBackLLBource |
| `Sevice.csv` | `Sevice` (plans) | SeviceID, TypeInscriptionID, Sevice, PowerService, CompanyID, BaseQt, BaseCost, Cost, NetCost, Discount, Available |
| `LedgerAccount.csv` | `LedgerAccount` (chart) | LedgerID, LedgerParentID, NameEn, NameAr, NameFr, BalanceTypeID, Level, Used |
| `AccountNames.csv` | `AccountNames` | AccountNameID, AccountName, Available, AccountType |
| `AcountNamesInformation.csv` | contact details | types 1 mobile, 2 home, 3 work, 6 e-mail, 7 address, 8 note |
| `Accounts.csv` | posting accounts | LedgerID, AccountNameID, MoneyID, CompanyID, Freezed, Available |
| `Subscrip.csv` | subscriptions | all business columns (**no UserVOD / PassVOD / ReciverNb / CardNb**) |
| `DataCustomer.csv` | monthly bills | every unpaid, not-deleted bill + all bills of the last `-Months` months |
| `MovementBalances.csv` | `Movement` (1.78 M lines) | **summed** per company × ledger × person × account currency × line currency × year, visible lines only |
| `manifest.json` | — | format `paxve-magic-export` v1, export time, database, months, row counts |

The journal is not exported line by line. For each group the script exports:

- `SumRaw` = Σ(DB − CR), in the currency of the line;
- `SumUSD` = Σ(DB − CR) × DolBource;
- `SumLBP` = Σ(DB − CR) × LLBource;
- two flags that mark exchange rates that look wrong:
  - `AcctRateOK`: the rate that converts the line into the account's currency is plausible (LBP → USD with DolBource between 1/1,000,000 and 1/500; USD → LBP with LLBource between 500 and 1,000,000);
  - `UsdRateOK`: DolBource is plausible for the currency of the line.

This is exactly how Magic computes its own balances. The `BalanceDetailed` / `AccountMovement` screens use
`SUM((DB−CR)·DolBource)` for USD accounts, and `·LLBource` for LBP accounts, over `Visible = 'True'` lines.
These formulas were read from the queries in `BalanceDetailedDataSet.xsd`.

## 3. Mapping Magic → Paxve

One Magic company (`Company`) becomes one Paxve company. For each one you choose:

- **Create a new company**: the Lebanese chart of accounts, USD + LBP and its cash boxes are created automatically;
- an existing company;
- or **Do not import**.

Two Magic companies cannot go into the same Paxve company.

Every row created from Magic carries an `external_ref` such as `magic:customer:16826` or `magic:bill:3:2026:88112`.

| Magic | Paxve | Rules |
|---|---|---|
| `Stations` (via `Branchs.CompanyID`) | `stations` | Name `----` or empty → "Station N". A name already taken gets " (Magic)" added |
| `Location` | `boxes` (code = name, station) **and** `areas` (same name) | Covers every box of the company's stations and every box used by its subscriptions. Duplicate names → "Name (#id)" |
| `Location.DefaultCollectorID` → `Collector` → person | `areas.collector_id` | A collector registered under another company is still the same person |
| `Collector` (one per `AccountNameID`, for the whole organisation) | `users`, role **collector**, **inactive**, no password, e-mail `magic-collector-<id>-<random>@import.invalid` | The admin sets a real e-mail and password in **Users** before the collector signs in |
| `Sevice`, `TypeInscriptionID = 3` | `plans` **metered**: base fee = `BaseCost`, unit price = `Cost` (per kWh) | Same formula as Magic: `FinalCost = BaseCost + (New − Old) × Cost` |
| `Sevice`, `TypeInscriptionID = 1` | `plans` **fixed**: fee = `Cost` (`NetCost` when a discount is set) | Plans are in USD, like every Magic bill (`MoneyID_DBCR = 1` in `AddAllIssals`) |
| `AccountNames` with a subscription, or with a non-zero balance on a customer ledger | `customers` (code = `AccountNameID`, or `M<id>` if that code is taken) | Phone = mobile (type 1) else `Subscrip.CellPhone`. Phone 2 = home / work. Address = type 7 else `Subscrip.Adress`. Notes = type 8. Status: active if any subscription is active, suspended if only frozen ones, closed otherwise. Debtors without a subscription stay active. Area = box of the first active subscription |
| `Subscrip` | `subscriptions` | Status: `Available = 0` → ended (skipped unless "also import cancelled"); `Activeted = 0` / `Freezed` / `FreezedNoCharge` → suspended; else active. Metered: `meter_no` = SubID, `initial_reading` = first exported `OldCounter`, `SpeciaBaseCost` → fee override. Fixed: `SpeciaCost` → fee override. Box position, internet user name and "pays at the office" go to the notes. Impossible start dates (1934, 5000…) → opening date |
| `DataCustomer.NewCounter` of metered subscriptions | `meter_readings` (period = `Annee-MonthID`) | Last N months (option, default 12). The last bill of a month wins. A reading lower than the month before is skipped. An existing Paxve reading is never overwritten |
| `DataCustomer` unpaid (`YesNoPayed = 0`, not `Deleted`, `Visible`) | `invoices` with the Magic number in the description ("Magic bill 2026/88112 · 2026-10") | Amount = `FinalCost − Discount`. This is what Magic posts to 4111 (`AddOneIssal.vb`: `DB = FinalCost − Discount`). Linked to the subscription and period, so a Paxve billing run never bills that month again. A 2nd bill for the same month gets no period. Impossible dates → 1st of the month. **Booked Dr receivables / Cr clearing 4718, not revenue**: the revenue was already booked in Magic |
| Customer balance on the customer ledgers (`Subscrip.LedgerID`, 411…) − unpaid bills imported | `invoices` "Opening balance (Magic)" if positive; `payments` of kind **opening_credit** if negative | The credit is applied automatically to the oldest unpaid bills. This is how partial payments ("balance forward", R-PY-4) and advances come over. Opening credits stay out of the collections and cash reports |
| Persons on supplier ledgers (401…) | `suppliers` (code = `AccountNameID`) | A balance we owe → an opening `bills` entry (Dr clearing / Cr payables). An advance paid to a supplier → stays in payables through the opening entry, with a warning |
| Every other ledger × currency | **one opening journal entry**, dated on the opening date, reference `MAGIC-<company>` | Cash ledgers 53… → the company's cash box of that currency (531 USD / 532 LBP). Banks 512… → its bank account. Others → the account with the same code, created when missing (names AR/EN/FR from `LedgerAccount`, under the closest group, type from `BalanceTypeID` then the PCG class). Income and expenses of **previous years** are folded into 12 "Retained earnings / result"; this year's are kept per account. Customer and supplier ledgers become one line each on clearing 4718, which nets to zero against the documents above |

The opening entry must balance in USD. Two lines make up any difference:

1. **Magic's own unbalanced entries.** These are Σ(DB − CR) in USD of all of Magic's visible lines, for example the one-line "سند يومية عادية" (spec R-GL-2). They go to **4719 "Magic migration difference"**, so they stay visible and can be investigated.
2. **Revaluation.** LBP balances are carried at the opening rate, not at their historical rates. The difference goes to the exchange gain / loss account.

### 3.1 Exchange rates

- The LBP rate defaults to `Money.LLBource` of USD (90,000 in the live data). You can change it on the page.
- If the company has no official LBP rate on the opening date, this rate is added there.
- Lines whose rate looks wrong (`AcctRateOK = 0`) are converted at this rate instead of their own, and a warning **"bad_rate_repaired"** lists them. Example: an LBP payment typed with DolBource = 1, which Magic itself would count as dollars.

### 3.2 Idempotency (running it again)

- Master data is matched by `external_ref`.
  - **Customers:** name, phones, e-mail, address and status are updated; their area is filled only if it is empty.
  - **Subscriptions:** only the status is updated.
  - **Stations, boxes, areas, plans, collectors:** kept as they are. An area with no collector gets its Magic collector.
- Readings: missing months are added; existing ones are kept.
- **Money is booked once.** Unpaid bills, opening balances, supplier bills and the opening entry are written only if this Paxve company has none of them yet for this Magic company. On a second run the result says *"Balances were already imported"*.
- So you can import early, keep using Magic for a few weeks, then re-import to pick up new customers and subscriptions.

  To move the **balances** again, import into a fresh company, or wait for the final export before the first import.

### 3.3 Not migrated (on purpose)

- **Journal history.** Only balances come over. Magic's 1.78 M journal lines include placeholder and hidden lines, typo dates (1934–5000), broken rates and 2,840 unbalanced entries in 2026 alone. Keep Magic read-only as the archive of the years before the switch.
- **Paid bills and payments.** Their effect is in the balances. For metered subscriptions the reading history is imported.
- **Users and permissions.** Magic passwords are plain text and every user is an administrator (spec §0.1). Create users again in Paxve.
- **Other modules:** stock quantities (`StoresItems`), cheques, payroll, restaurant POS, water delivery, SMS templates, `SubHistory`, satellite receivers, prepaid expiry dates (`ExpiredDate`).
  - Stock **values** do come over, in the opening entry (class 3 accounts). Enter the quantities in Paxve with a stock adjustment.
- **Special kWh price per subscriber** (`SpeciaCost` on a metered subscription). Paxve has one unit price per plan, so the warning lists these subscribers. Create a dedicated plan for them.

## 4. Warnings shown in the dry run

| Code | Meaning |
|---|---|
| `bad_rate_repaired` | Lines with an implausible exchange rate were recalculated at the LBP rate |
| `magic_books_unbalanced` | Magic's journal does not balance; the difference goes to 4719 |
| `bill_without_subscription` | Unpaid bill of a cancelled subscription; its debt stays in the customer's opening balance |
| `duplicate_bill_period` | Two bills for the same subscription and month (spec R-BL-1); the second is imported without a period |
| `partial_payments_applied` | The customer paid on account; a credit is applied to the oldest bills |
| `bad_start_date`, `bad_bill_date`, `odd_year_lines` | Impossible dates in Magic (spec §6) |
| `special_unit_price_ignored` | See §3.3 |
| `supplier_advance_in_gl` | An advance paid to a supplier is kept in payables |
| `reading_below_previous` | A meter went backwards; that reading is skipped |
| `plan_missing`, `box_missing`, `ledger_missing`, `unknown_currency_lines`, `unsupported_currency_pair` | Broken references in Magic |

## 5. Checks after the import

- **Trial balance balanced.** The database enforces it for every entry.
- **Clearing 4718 = 0.**
- **Customer balances:** each one equals its Magic balance, that is, Paxve's unpaid invoices − unapplied credits = Σ(DB−CR) on the customer ledgers. This is tested automatically for every customer of the synthetic sample (`TestMagicMigration`).
- **Receivables (411)** = Σ customer balances.
- **Supplier balances** = Magic's.
- **4719** = Magic's own imbalance.

Then do these yourself:

1. Give the collectors e-mails and passwords.
2. Check a few customer statements against Magic.
3. Enter this month's readings.
4. Run the first billing in Paxve for the month after the last Magic bill.

## 6. API

All routes are admin only and require a signed-in session (API keys are refused).

| Method and path | Body | Result |
|---|---|---|
| `POST /api/v1/magic/imports` | `{file_name, size, sha256}` | `201` with the import (`status: uploading`) |
| `POST /api/v1/magic/imports/{id}/chunk` | `{offset, data}`, where `data` is base64, at most 4 MB of file per chunk | The import. After the last chunk the SHA-256 is checked and the dry run computed (`status: uploaded`, or `invalid` with `error`) |
| `GET /api/v1/magic/imports` | — | The last 30 imports |
| `GET /api/v1/magic/imports/{id}` | — | `analysis`, `options`, `mapping`, `progress`, `result`, `error` |
| `POST /api/v1/magic/imports/{id}/analyze` | `{opening_date, rate, readings_months, include_ended}` | The dry run again, with other options |
| `POST /api/v1/magic/imports/{id}/commit` | `{mapping: [{magic_company, target: "<company id>" \| "new" \| "skip"}], options}` | `202` with the import (`status: running`) |

- Poll `GET /api/v1/magic/imports/{id}` until `status` is `done` or `failed`.
- The commit runs in the background in **one** transaction: a failed import changes nothing. Only new companies created for it remain.
