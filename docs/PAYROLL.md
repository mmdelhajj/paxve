# HR & payroll (module U)

Screens: **HR & payroll** ribbon tab → Employees, Attendance, Leave, Payroll, Payroll rules, HR reports, and **My page**
(employee self-service, also the landing page of a user who has no office permission).

## Employee file

`PATCH /employees/{id}` keeps the basics (name, salary, currency, transport, NSSF, cash box, user, cost center).
`PUT /employees/{id}/file` holds the rest: personal data, photo (≤ 300 KB data URL), department, manager, contract
(type, start, end — reminders 45 days before), last working day, bank (IBAN, routing code for WPS), work schedule,
fingerprint device number, **second salary part** (`currency2`, `salary2`), housing, commission %, daily transport,
attendance tracking, tax exemption, family status (spouse working, children) and fixed monthly components
(allowances / deductions, taxed or not, subject to social security or not).
Documents: `POST /employees/{id}/documents` (base64, ≤ 5 MB, optional expiry), download / delete.

## Attendance

* Schedules (`/hr/schedules`): start, end, break, grace minutes, work days (ISO 1 = Monday).
* Public holidays (`/hr/holidays`).
* Day sheet `GET /hr/attendance?date=` and manual correction `PUT /hr/attendance`.
* Phone clock in / out: `POST /self/clock` (first call of the day = in, next = out; latitude/longitude when the
  phone gives them). No selfie is stored.
* Fingerprint machines: `POST /hr/attendance/import {csv, dry_run}`. Accepts ZKTeco-style exports (comma, semicolon or
  tab separated) with a user-number column (`AC-No.`, `No.`, `User ID`, `Enroll No`, …) and either a date-time column
  (`Time`, `DateTime`, `Check Time`, …) or `Date` + `Time`. First punch of the day = in, last = out. Employees are matched
  on `device_id`, else on their code. Times are read as Asia/Beirut local time.
* Worked minutes = out − in − break; late = arrival after start + grace; overtime = worked beyond the schedule.

## Leave

Leave types are created per company on first use from the country (Lebanon: 15 days annual accrued monthly, sick,
maternity 70 days, unpaid). Balance = earned (prorated from the hire date; monthly accrual counts months elapsed) +
carried over (≤ max_carry) − taken − waiting. A request must fit in the balance (types with a limit), may not
overlap another one, and counts scheduled working days only (holidays excluded).
Employees request from their page; their **manager** (employee.manager_id → that employee's user) or anyone with
`payroll.approve` accepts or refuses — never the employee themselves. Approved unpaid leave is deducted in the payroll.

## Payroll

`POST /payroll {period}` prepares a **detailed payslip** (`payslip_lines`) for every active employee employed in the
month (part months are prorated by calendar days):

| line | rule |
|---|---|
| basic salary (1 or 2 currencies) | prorated; absences and unpaid leave deduct salary × days / `month_days` from each part |
| housing, overtime, commission, components, manual allowance | overtime = salary / `month_hours` × hours × `overtime_rate`; commission = % of the payments the employee's user received in the month |
| transport | fixed monthly amount and/or `transport_per_day` × days worked (legal currency) |
| social security | employee and employer shares on the social base (salary, housing in LB / when `social_includes_housing`, overtime, social components) capped by `social_ceiling`; LB adds family allowances fund 6 % (own ceiling) and end of service 8.5 % (no ceiling) |
| family allowance (LB) | spouse not working + per child (max 5), paid with the salary for the NSSF (booked against NSSF payable) |
| salary tax | (taxable monthly × 12 − family deductions) through `tax_brackets`, / 12, rounded to `tax_rounding` |
| advance recovery | instalments due (or the manual amount), capped by what is left of part 1 |

Statutory amounts are computed in the **legal currency** (`legal_currency`, LBP in Lebanon) at the official rate of the
last day of the month and taken from the part of the salary paid in that currency (else converted to part 1; if that
part is too small they move to part 1 automatically). A Lebanese company therefore needs an LBP rate.

Workflow: draft (recalculate, edit manual allowance / deduction / recovery) → **approved** (locked; `payroll.approve`)
→ **posted** → paid per cash box currency. Posting is per currency and cost center:
Dr salaries (earnings − salary deductions) · Dr social security expense (employer shares) · Cr social security payable
(employee + employer shares − family allowances paid) · Cr salary tax payable (created as 4421 / 2240 on first use) ·
Cr advances · Cr salaries payable (net). Payslips made before module U keep their old calculation.

Other: `GET /payroll/{id}/payslips/{pid}` (print / WhatsApp), `GET /payroll/{id}/wps` (UAE SIF: EDR lines + SCR;
needs the employer ID and bank code in Payroll rules), `GET /employees/{id}/end-of-service?date=&reason=`.

## Country rules and sources

All values live in `internal/httpapi/hr_rules.go` with an effective date and can be overridden per company from a date
(`POST /hr/payroll-params {key, value, effective_from}`). **Check every value with an accountant before relying on it.**

Lebanon (LBP):

| key | value | from | source |
|---|---|---|---|
| social_ee_rate / social_er_rate | 3 % / 8 % (sickness & maternity) | — | NSSF, see [Azkatech payroll guide 2026](https://azka.tech/blog/lebanon-payroll-guide/) |
| social_ceiling | 140,000,000 → **120,000,000** | 2025-08-01 | NSSF memo 805 (22 Aug 2025), [ALDIC](https://www.aldic.net/nssf-memo-805-dated-22-aug-2025-cancels-memo-801-and-sets-the-nssf-ceiling-at-lbp-120-million/) |
| family_er_rate / family_ceiling | 6 % / 18,000,000 → **28,000,000** | 2026-05-01 | Decree 2923, [Mercans](https://mercans.com/resources/statutory-alerts/lebanon-increase-in-maximum-monthly-income-subject-to-nssf-1-may-2026/) |
| eos_er_rate | 8.5 % (no ceiling) | — | NSSF end-of-service branch |
| family_allow_spouse / child | 2,100,000 / 1,155,000 (max 5 children) | 2026-05-01 | Decree 2923 (Azkatech) |
| tax_brackets | 2 % ≤ 360 M · 4 % ≤ 900 M · 7 % ≤ 1.8 B · 11 % ≤ 3.6 B · 15 % ≤ 7.2 B · 20 % ≤ 13.5 B · 25 % above | 2024 | Income Tax Law art. 58 as amended by the 2022/2024 budget laws ([lebaneselegislation.com](https://lebaneselegislation.com/en/topic/income-tax-brackets/)) |
| tax_ded_self / spouse / child | 450,000,000 / 225,000,000 / 45,000,000 (max 5) | 2024 | Budget law 2024 art. 49 (Azkatech) |
| min_wage | 28,000,000 / month | 2025-08-01 | Decree 699 |
| transport_per_day | 500,000 | 2026-10-01 | Decree 4021 links it to the price of fuel (500,000–800,000): **set the current amount** |

Sources disagree on the employer sickness rate (8 % vs 11 %) and the transport amount moves with fuel prices: both are
plain settings. Pension law 319 (2023) is not modelled; end of service stays the NSSF lump sum.

Saudi Arabia (SAR): GOSI for Saudi nationals 9.75 % employee (annuities 9 % + SANED 0.75 %) and 11.75 % employer
(+ occupational hazards 2 %), other nationalities 2 % employer, base = basic + housing capped at 45,000.
New entrants since July 2024 pay gradually higher annuity rates: change `social_ee_rate` / `social_er_rate` per company
if needed. End-of-service: ½ month per year for 5 years then a month per year; resignation 0 / ⅓ / ⅔ / full.
UAE (AED): GPSSA for UAE nationals 5 % / 12.5 % (federal); gratuity 21 days of basic pay per year for 5 years then
30 days, capped at 2 years; WPS SIF export. Kuwait, Qatar, Bahrain, Oman: UAE-style gratuity, no salary tax.
Other countries: social security rates from the company back-office settings, no tax until brackets are entered.

## Not done

* Leave carry-over is computed from last year's entitlement minus what was taken (no accrual history table).
* Overtime and lateness use one schedule per employee (no rotating shifts) and the Asia/Beirut time zone.
* ZKTeco devices are imported from CSV only (no live device connection); no selfie on phone clock-in.
* KSA GOSI phased rates for new entrants and the Lebanese pension law are settings, not automatic.
* WPS file follows the common SIF layout; banks may ask for small variations.
