package wallet

import (
	"context"
	"time"
)

type Invariants struct {
	OK bool `json:"ok"`

	// Sum of every ledger entry. Each movement posts -amount and +amount, so this is 0.
	LedgerSumPaise int64 `json:"ledger_sum_paise"`
	// Sum of every balance including the treasury, whose negative balance is the total ever
	// minted. Money only moves between wallets, so this is 0.
	BalanceSumPaise      int64 `json:"balance_sum_paise"`
	UserBalanceSumPaise  int64 `json:"user_balance_sum_paise"`
	TreasuryBalancePaise int64 `json:"treasury_balance_paise"`

	// Each of these must be 0.
	NegativeUserWallets int64 `json:"negative_user_wallets"`
	UnreconciledWallets int64 `json:"unreconciled_wallets"`
	PendingTransfers    int64 `json:"pending_transfers"`

	Wallets   int64     `json:"wallets"`
	Transfers int64     `json:"transfers"`
	CheckedAt time.Time `json:"checked_at"`
}

// CheckInvariants audits the whole database. It is one statement, so every number comes
// from the same snapshot; separate queries would race with in-flight transfers and report
// mismatches that never existed. It scans every wallet and ledger entry, which is fine at
// this scale but would move to incremental reconciliation on a large ledger.
func (s *Service) CheckInvariants(ctx context.Context) (Invariants, error) {
	var inv Invariants
	err := s.pool.QueryRow(ctx, `
		WITH ledger AS (
			SELECT wallet_id, SUM(delta_paise) AS total FROM ledger_entries GROUP BY wallet_id
		)
		SELECT
			(SELECT COALESCE(SUM(delta_paise), 0) FROM ledger_entries)::bigint,
			(SELECT COALESCE(SUM(balance_paise), 0) FROM wallets)::bigint,
			(SELECT COALESCE(SUM(balance_paise), 0) FROM wallets WHERE NOT is_treasury)::bigint,
			(SELECT COALESCE(SUM(balance_paise), 0) FROM wallets WHERE is_treasury)::bigint,
			(SELECT count(*) FROM wallets WHERE NOT is_treasury AND balance_paise < 0),
			(SELECT count(*) FROM wallets w LEFT JOIN ledger l ON l.wallet_id = w.id
			  WHERE w.balance_paise <> COALESCE(l.total, 0)),
			(SELECT count(*) FROM transfers WHERE status = 'pending'),
			(SELECT count(*) FROM wallets WHERE NOT is_treasury),
			(SELECT count(*) FROM transfers),
			now()`,
	).Scan(
		&inv.LedgerSumPaise, &inv.BalanceSumPaise, &inv.UserBalanceSumPaise, &inv.TreasuryBalancePaise,
		&inv.NegativeUserWallets, &inv.UnreconciledWallets, &inv.PendingTransfers,
		&inv.Wallets, &inv.Transfers, &inv.CheckedAt,
	)
	if err != nil {
		return Invariants{}, err
	}
	inv.OK = inv.LedgerSumPaise == 0 && inv.BalanceSumPaise == 0 &&
		inv.NegativeUserWallets == 0 && inv.UnreconciledWallets == 0 && inv.PendingTransfers == 0
	return inv, nil
}
