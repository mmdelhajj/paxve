package httpapi

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Unrealized inter-company profit (phase 8 leftover): goods company A sold to company B that B still holds carry A's
// margin in B's stock value. The group report takes that margin out of the stock (balance sheet) and, for the change
// over the period, out of the profit.
//
// Per item: units B received on inter-company bills from A, at B's cost (A's selling price) and A's cost (the sale's
// stock moves); units still held = min(B's stock of the item at the date, units received). Held units × (B's unit
// cost − A's unit cost) in the report currency.

type urpLine struct {
	InvAccount string
	Amount     decimal.Decimal
}

func icUnrealized(ctx context.Context, tx pgx.Tx, linkID string, cs map[string]consComp, date string) ([]urpLine, error) {
	rows, err := tx.Query(ctx, `SELECT d.src_company::text, d.dst_company::text, d.src_invoice::text, d.dst_bill::text
		FROM ic_docs d JOIN invoices i ON i.id = d.src_invoice JOIN bills b ON b.id = d.dst_bill
		WHERE d.link_id = $1 AND d.status = 'done' AND i.status <> 'void' AND b.status <> 'void' AND i.issue_date <= $2::date`, linkID, date)
	if err != nil {
		return nil, err
	}
	type doc struct{ src, dst, inv, bill string }
	var docs []doc
	for rows.Next() {
		var d doc
		if err := rows.Scan(&d.src, &d.dst, &d.inv, &d.bill); err != nil {
			rows.Close()
			return nil, err
		}
		docs = append(docs, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	type key struct{ dst, item string }
	type acc struct {
		qty, buyerCost, sellerCost decimal.Decimal // report currency
		src                        string
	}
	byItem := map[key]*acc{}
	for _, d := range docs {
		src, okS := cs[strings.ToLower(d.src)]
		dst, okD := cs[strings.ToLower(d.dst)]
		if !okS || !okD {
			continue
		}
		// what B received (qty, value in B's base)
		in, err := tx.Query(ctx, `SELECT item_id::text, sum(quantity)::text, sum(value)::text FROM stock_moves
			WHERE company_id = $1 AND source = 'bill' AND source_id = $2 AND quantity > 0 GROUP BY 1`, d.dst, d.bill)
		if err != nil {
			return nil, err
		}
		type mv struct{ item, qty, val string }
		var got []mv
		for in.Next() {
			var m mv
			if err := in.Scan(&m.item, &m.qty, &m.val); err != nil {
				in.Close()
				return nil, err
			}
			got = append(got, m)
		}
		in.Close()
		// A's cost of the same goods (its sale moves of the invoice), per item code (the two companies have their own items)
		for _, m := range got {
			var code string
			if err := tx.QueryRow(ctx, `SELECT code FROM items WHERE id = $1`, m.item).Scan(&code); err != nil {
				return nil, err
			}
			var sq, sv *string
			if err := tx.QueryRow(ctx, `SELECT sum(-sm.quantity)::text, sum(-sm.value)::text FROM stock_moves sm JOIN items it ON it.id = sm.item_id
				WHERE sm.company_id = $1 AND sm.source = 'sale' AND sm.source_id = $2 AND it.code = $3`, d.src, d.inv, code).Scan(&sq, &sv); err != nil {
				return nil, err
			}
			q, _ := decimal.NewFromString(m.qty)
			v, _ := decimal.NewFromString(m.val)
			if !q.IsPositive() {
				continue
			}
			sellerCost := decimal.Zero
			if sq != nil && sv != nil {
				sQ, _ := decimal.NewFromString(*sq)
				sV, _ := decimal.NewFromString(*sv)
				if sQ.IsPositive() {
					sellerCost = sV.Div(sQ).Mul(q).Mul(src.Rate) // A's cost of the units B received
				}
			}
			if !sellerCost.IsPositive() {
				continue // A had no stock cost for it (a service, or no inventory): no margin held in stock
			}
			k := key{d.dst, m.item}
			a := byItem[k]
			if a == nil {
				a = &acc{src: d.src}
				byItem[k] = a
			}
			a.qty = a.qty.Add(q)
			a.buyerCost = a.buyerCost.Add(v.Mul(dst.Rate))
			a.sellerCost = a.sellerCost.Add(sellerCost)
		}
	}
	var out []urpLine
	for k, a := range byItem {
		var held, invAcc *string
		if err := tx.QueryRow(ctx, `SELECT coalesce(sum(quantity), 0)::text, coalesce((SELECT inventory_account_id::text FROM items WHERE id = $2),
			(SELECT inventory_account_id::text FROM companies WHERE id = $1)) FROM stock_moves WHERE company_id = $1 AND item_id = $2 AND move_date <= $3::date`,
			k.dst, k.item, date).Scan(&held, &invAcc); err != nil {
			return nil, err
		}
		h, _ := decimal.NewFromString(deref(held))
		if !h.IsPositive() || invAcc == nil {
			continue
		}
		if h.GreaterThan(a.qty) {
			h = a.qty // the rest of the stock came from other suppliers
		}
		margin := a.buyerCost.Sub(a.sellerCost) // margin on all units received
		if !margin.IsPositive() {
			continue // sold at or below cost: nothing unrealized to remove
		}
		urp := margin.Mul(h).DivRound(a.qty, 6)
		out = append(out, urpLine{InvAccount: *invAcc, Amount: urp})
	}
	return out, nil
}

// dayBefore: the day before an ISO date.
func dayBefore(d string) string {
	t, err := time.Parse(time.DateOnly, d)
	if err != nil {
		return d
	}
	return t.AddDate(0, 0, -1).Format(time.DateOnly)
}
