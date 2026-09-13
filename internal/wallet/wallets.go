package wallet

import (
	"context"
	"errors"

	"github.com/async-wizard/paisa-wallet/internal/obs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrNotFound = errors.New("not found")

type Wallet struct {
	ID           uuid.UUID `json:"id"`
	BalancePaise int64     `json:"balance_paise"`
}

// GetOrCreateWallet returns the user's wallet, creating it if needed; created reports
// which. Race-free: UNIQUE (user_id) admits one insert. Every concurrent loser's insert
// waits for the winner to commit, returns no row, and the follow-up SELECT (a new
// statement, so a new READ COMMITTED snapshot) reads the winner's wallet.
func (s *Service) GetOrCreateWallet(ctx context.Context, userID uuid.UUID) (w Wallet, created bool, err error) {
	err = s.pool.QueryRow(ctx,
		`INSERT INTO wallets (user_id) VALUES ($1) ON CONFLICT (user_id) DO NOTHING RETURNING id, balance_paise`,
		userID,
	).Scan(&w.ID, &w.BalancePaise)
	if err == nil {
		obs.Event(ctx, "wallet.created", "wallet_id", w.ID, "user_id", userID)
		return w, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Wallet{}, false, err
	}

	err = s.pool.QueryRow(ctx,
		`SELECT id, balance_paise FROM wallets WHERE user_id = $1`, userID,
	).Scan(&w.ID, &w.BalancePaise)
	if err == nil {
		obs.Event(ctx, "wallet.getorcreate.existing", "wallet_id", w.ID, "user_id", userID)
	}
	return w, false, err
}

// GetWallet returns a wallet only if userID owns it. Wallets owned by someone else, and the
// treasury (which has no owner), are reported as not found so their existence isn't leaked.
func (s *Service) GetWallet(ctx context.Context, userID, walletID uuid.UUID) (Wallet, error) {
	var w Wallet
	err := s.pool.QueryRow(ctx,
		`SELECT id, balance_paise FROM wallets WHERE id = $1 AND user_id = $2`, walletID, userID,
	).Scan(&w.ID, &w.BalancePaise)
	if errors.Is(err, pgx.ErrNoRows) {
		return Wallet{}, ErrNotFound
	}
	return w, err
}
