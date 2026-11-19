# Paxve for other industries: gap study (2026-11-19)

Scope: what a professional 2026 system needs for supermarkets, electronics shops, furniture stores, factories and
seven more industries, measured against Odoo 19, Lightspeed, Square, LS Retail, Zoho Inventory/Books, ERPNext and
the usual Lebanese/Gulf retail packages (for example the Magic-style desktop POS systems). This is analysis only; no code was changed.

Status legend: ✅ exists · 🟡 partial · ❌ missing. Effort: S = days, M = 1–2 weeks, L = 3+ weeks.

## What Paxve already has that every shop uses

| Area | Already in Paxve |
|---|---|
| Items | barcode, category, image, units of measure with their own barcode/price (`item_units`), kits (`item_components`), custom fields |
| Stock | multi-warehouse, transfers, FIFO or average cost, lots / serials / expiry with FEFO (`stock_lots`), reorder rules, stock counts, landed costs, scrap |
| Prices | price lists per currency / customer with quantity breaks (`price_lists`), supplier price lists |
| POS | registers, cashier sessions with opening float, X/Z reports, offline mode, barcode scan, line discount, refunds, USD + LBP with change, price lists and kits on the till, Bluetooth ESC/POS printing in the Android app |
| Buying | RFQ → purchase order → goods receipt (3-way match) → supplier bill, supplier debit notes, landed costs |
| Selling | quotes → orders → deliveries → invoices, credit notes, payment terms, dunning, statements, CRM pipeline |
| After-sales | service tickets with priority, assignee, SLA and history (`svc`), maintenance plans by hours/days |
| Making | single-level BoM, manufacturing orders at FIFO/WAC + labour + overhead, scrap |
| Money and admin | full accounting, cheques, cost centers (branch/project), multi-company + consolidation, approvals, attachments, e-invoicing (ZATCA ph.2, ETA), online payment layer, HR and payroll, 11 languages |

So the stock, costing, accounting and POS engine is already professional. What is missing is mostly the
**retail front layer**: variants, promotions, loyalty, scales, labels, installments, warranty and repair, delivery,
and online selling.

## 1. Supermarket / minimarket

| Feature | Status | Notes |
|---|---|---|
| Several tills, cashier shifts, X/Z | ✅ | `pos_registers`, `pos_sessions` |
| Offline till | ✅ | queue + sync |
| Barcode scan, fast item search | ✅ | |
| Multiple barcodes per item / pack barcode | 🟡 | one per item plus one per unit; no free list of extra barcodes |
| Weighed items, scale barcodes (prefix 20–29, embedded weight/price) | ❌ | most-requested supermarket feature |
| Direct scale on the till (serial/USB) | ❌ | needs the Android app or a local bridge |
| Promotions: buy X get Y, % off category, mix & match, happy hour, date range | ❌ | |
| Loyalty points / member cards / coupons / gift cards | ❌ | |
| Shelf labels and barcode label printing | ❌ | price-change labels batch |
| Fast item creation from the till (unknown barcode) | ❌ | |
| Expiry tracking, FEFO, near-expiry report | ✅ | stock_lots |
| Supplier returns (expired / damaged) | 🟡 | supplier debit note exists; no "return to supplier" stock document from the till/stock screen |
| Reorder suggestions from sales speed | ✅ | reorder rules + days-of-cover |
| Cash drawer kick, customer pole display / second screen | 🟡 | printing works; no drawer kick command or customer display |
| Hold / recall basket, split payment | 🟡 | multi-method payment exists; no parked baskets |
| Daily sales by cashier, margin by category | 🟡 | session reports exist; no category margin report |
| Price check screen / mobile price checker | ❌ | |

Build plan: scale barcodes + weighed items (M), promotions engine (M), loyalty/coupons/gift cards (M), shelf labels (S),
parked baskets + drawer kick + customer display (S), quick item create (S), extra barcodes (S), category margin report (S).

## 2. Electrical / electronics / mobile shop

| Feature | Status | Notes |
|---|---|---|
| Serial / IMEI tracking in stock | ✅ | serial-tracked items on bills |
| Serial / IMEI captured at the till and on the invoice | 🟡 | supported on stock docs; POS screen does not ask for the serial |
| Serial history (who bought, when, warranty end) | ❌ | |
| Warranty (months per item, warranty card printout, claim lookup by serial) | ❌ | |
| Repairs / RMA: intake, diagnosis, parts used, labour, status SMS/WhatsApp, pickup | 🟡 | service tickets give status + assignee + SLA; no parts/labour costing or repair invoice |
| Supplier RMA (send faulty unit back to supplier) | ❌ | |
| Installments / buy now pay later with schedule and reminders | ❌ | very common in Lebanon/Gulf |
| Trade-in (used phone taken as part payment) | ❌ | |
| Bundles (phone + case + charger) | ✅ | kits |
| Variants (colour / storage) | ❌ | shared with clothing |
| Sales commission per salesperson | 🟡 | employee commission % in payroll; not computed from sales |
| Price in USD with LBP at the rate of the day | ✅ | |

Build plan: serial at POS + serial history (S), warranty (S), repair orders on top of service tickets (M),
installments (M), trade-in (S), sales commission from invoices (S), supplier RMA (S).

## 3. Furniture store / showroom

| Feature | Status | Notes |
|---|---|---|
| Quote → order → delivery → invoice | ✅ | |
| Variants (fabric, colour, size) | ❌ | |
| Custom dimensions / configurable product (price by size or options) | ❌ | |
| Made-to-order: the sales order creates a purchase or manufacturing order | ❌ | PO and MO exist separately |
| Customer deposit on the order, balance on delivery | 🟡 | payment on account exists; no deposit linked to the order and shown on it |
| Installments | ❌ | shared with electronics |
| Delivery scheduling (date, time slot, truck, team) + assembly / installation job | ❌ | deliveries exist without scheduling |
| Supplier lead time, expected arrival date on order | 🟡 | PO has dates; no lead time per supplier item |
| Showroom samples / display items (not for sale, separate warehouse) | 🟡 | a warehouse can serve this; no flag |
| Photos and catalog PDF | 🟡 | item image; no catalog print |
| Multi-warehouse (showroom + store) | ✅ | |
| After-sales (damage claims) | 🟡 | service tickets |

Build plan: variants (M, shared), configurable products / options pricing (M), deposits on orders (S), installments (shared),
delivery & installation scheduling board (M), make-to-order link SO→PO/MO (S), lead times (S), catalog print (S).

## 4. Factory / manufacturing

| Feature | Status | Notes |
|---|---|---|
| Bill of materials | 🟡 | single level; no sub-assemblies, no versions |
| Manufacturing order with FIFO/WAC + labour + overhead | ✅ | |
| Scrap | ✅ | |
| Multi-level BoM / sub-assemblies, BoM versions | ❌ | |
| Routings and work centers (operations, times, machine cost per hour) | ❌ | |
| MRP: plan from sales orders and min stock, suggest purchases and MOs | 🟡 | reorder rules suggest buying; no MRP explosion |
| By-products / co-products | ❌ | |
| Quality checks (points per step, pass/fail, photos) | ❌ | |
| Work orders on a shop-floor tablet (start/pause/finish, operator) | ❌ | |
| Subcontracting (send materials, receive finished goods) | ❌ | |
| Machine maintenance (preventive by hours/days) | ✅ | maintenance plans |
| Production cost report (planned vs actual, variance) | 🟡 | actual cost per MO; no variance report |
| Lot traceability raw material → finished lot | 🟡 | lots exist on both sides; no link report |

Build plan: multi-level BoM + versions (M), work centers + routings + work orders tablet (L), MRP run (L), by-products (S),
quality checks (M), subcontracting (M), variance + traceability reports (S).

## 5. Other industries (short)

| Industry | Already fits | Main gaps |
|---|---|---|
| Pharmacy | lots, expiry, FEFO, POS, price lists | drug database / generic names, prescriptions, controlled-items log, insurance claims, shelf location (M–L) |
| Restaurant / café | POS, kits (as recipes), stock | tables and floor plan, modifiers (no sugar, extra cheese), kitchen display / printer, split bill, recipes deducting ingredients, delivery/takeaway orders (L) |
| Clothing / shoes | POS, barcode, price lists | **variants size × colour matrix** (key), season/collection, exchange without receipt, label printing (M) |
| Auto parts / garage | items, stock, service tickets | part numbers and cross-references (OEM ↔ alternative), vehicle compatibility, garage job card with parts + labour, vehicle history by plate (M–L) |
| Wholesale / distribution | price lists, payment terms, dunning, multi-currency, collector offline app | customer credit limit with block, van sales (stock on the van, offline invoicing), route planning, sales rep targets/commission (M) |
| Construction materials | units (ton, m³, bag), deliveries, price lists, cost centers | project/site per order, delivery by truck with weight ticket, retention on contractor invoices (M) |
| Services / contracting | recurring billing, subscriptions, attachments, approvals | projects with budgets, timesheets, progress billing (% complete), retention (M) |

## Recommendation

### Most value for least work

1. **Supermarket / minimarket.** The engine is already there (POS, stock, expiry). Scale barcodes, promotions,
   loyalty and labels turn it into a complete product. It is the largest market in Lebanon and the Gulf.
2. **Electronics / mobile shops.** Serials exist; add warranty, repairs, installments and trade-in. It is a
   short build and matches Lebanese habits (USD pricing, installments).
3. **Clothing / shoes.** Variants is the only big missing piece, and it is needed anyway for furniture and electronics.
4. **Wholesale / distribution.** Credit limit + van sales reuse the collector offline app.
5. Furniture next (needs variants + deposits + delivery board), then factory (routings/MRP is the largest job).

### "Retail core": build once, serves all shops

| Core piece | Effort | Used by |
|---|---|---|
| Product variants (attributes × values, matrix entry, barcode per variant) | M | clothing, furniture, electronics, supermarket |
| Promotions engine (rules, date range, branches, POS + invoices) | M | supermarket, clothing, electronics, restaurant |
| Loyalty, coupons and gift cards | M | supermarket, clothing, restaurant, pharmacy |
| Scale barcodes + weighed items + label printing | M | supermarket, butcher, bakery, construction |
| Installments (schedule, reminders via dunning, late fee) | M | electronics, furniture, cars |
| Warranty + repair orders (on service tickets) | M | electronics, furniture, auto, appliances |
| Delivery board (slots, drivers, driver phone view with proof of delivery) | M | furniture, supermarket, restaurant, wholesale |
| Online store + WhatsApp catalog (items, stock, price lists, cart → sales order, online payment) | L | all |
| POS hardware pack (drawer kick, customer display, parked baskets, price checker) | S | all shops |
| Customer credit limit | S | wholesale, construction, every shop selling on account |

About 3–4 months of work in total for the whole core, with each piece usable as soon as it ships.

### Vertical packs (turned on by business type, like Odoo)

Paxve already has `companies.business_type` and the apps switch. Each pack = a set of apps + defaults + chart
accounts + demo data + ribbon tiles:

| Pack | Turns on | Defaults |
|---|---|---|
| Supermarket | POS, stock pro, promotions, loyalty, scale, labels | FEFO, average cost, scale prefix 20–29 |
| Electronics | POS, serials, warranty, repairs, installments, trade-in | serial tracking on phones, 12-month warranty |
| Furniture | sales, variants/options, deposits, installments, delivery board | 30 % deposit, delivery + assembly job |
| Clothing | POS, variants, promotions, loyalty, labels | size × colour matrix, exchange 14 days |
| Factory | manufacturing, routings, MRP, quality, maintenance | FIFO, labour/overhead accounts |
| Wholesale | price lists, credit limit, van sales, routes | payment terms 30 days, credit block |
| Restaurant | POS tables, modifiers, kitchen display, recipes | ingredient deduction |
| Pharmacy | POS, lots/expiry, drug fields, prescriptions | FEFO, expiry alerts 90 days |
| Generator / ISP / satellite (today) | billing, collectors, network, maintenance | as now |

Suggested order: retail core part 1 (variants, promotions, loyalty, scales/labels, POS hardware), then the Supermarket
and Clothing packs. Then part 2 (installments, warranty/repairs, delivery board, credit limit) with the Electronics,
Furniture and Wholesale packs. Then the online store, then factory (routings, MRP, quality), restaurant and pharmacy.
