package httpapi

import (
	"context"
	"log/slog"
	"time"
)

// Daily ledger totals (migration 00023): reports read account_totals(), which combines ledger_daily with
// the entries posted since the last catch-up, so they are always exact; the worker only keeps the live
// part small. It runs on the owner connection, one company at a time.

const ledgerBatch = 50000

// CatchUpLedger adds every posted entry not yet in ledger_daily; returns how many entries were added.
func (s *Server) CatchUpLedger(ctx context.Context) (int, error) {
	if s.OwnerPool == nil {
		return 0, nil
	}
	rows, err := s.OwnerPool.Query(ctx, `
		SELECT c.id::text FROM companies c LEFT JOIN ledger_watermark w ON w.company_id = c.id
		WHERE EXISTS (SELECT 1 FROM journal_entries e WHERE e.company_id = c.id AND e.status = 'posted'
		              AND e.number > coalesce(w.last_number, 0))`)
	if err != nil {
		return 0, err
	}
	var companies []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		companies = append(companies, id)
	}
	rows.Close()
	total := 0
	for _, cid := range companies {
		for {
			var n int
			if err := s.OwnerPool.QueryRow(ctx, `SELECT ledger_catch_up($1, $2)`, cid, ledgerBatch).Scan(&n); err != nil {
				return total, err
			}
			total += n
			if n < ledgerBatch {
				break
			}
		}
	}
	return total, nil
}

// StartLedgerWorker keeps ledger_daily up to date (every 10 seconds).
func (s *Server) StartLedgerWorker(ctx context.Context) {
	if s.OwnerPool == nil {
		return
	}
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			if n, err := s.CatchUpLedger(ctx); err != nil {
				slog.Error("ledger catch-up", "err", err)
			} else if n > 1000 {
				slog.Info("ledger catch-up", "entries", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}
