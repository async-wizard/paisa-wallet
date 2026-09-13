package wallet

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Mint moves new money from the treasury into a user's wallet. It is the test faucet and
// the only way money enters the system; it goes through the same path as a transfer, so
// the ledger stays zero-sum. The idempotency key is scoped to the wallet's owner.
func (s *Service) Mint(ctx context.Context, to uuid.UUID, amount int64, key string) (Outcome, error) {
	var owner uuid.UUID
	err := s.pool.QueryRow(ctx,
		`SELECT user_id FROM wallets WHERE id = $1 AND user_id IS NOT NULL`, to,
	).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return Outcome{}, ErrNotFound
	}
	if err != nil {
		return Outcome{}, err
	}
	return s.execute(ctx, "mint", owner, TreasuryID, to, amount, key)
}
