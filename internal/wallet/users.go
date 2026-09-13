package wallet

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	pool *pgxpool.Pool
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

func (s *Service) UserIDForToken(ctx context.Context, tokenHash string) (uuid.UUID, error) {
	var id uuid.UUID

	err := s.pool.QueryRow(ctx, `SELECT id FROM users WHERE token_hash = $1`, tokenHash).Scan(&id)
	if !errors.Is(err, pgx.ErrNoRows) {
		return id, err
	}

	err = s.pool.QueryRow(ctx,
		`INSERT INTO users (token_hash) VALUES ($1) ON CONFLICT (token_hash) DO NOTHING RETURNING id`,
		tokenHash,
	).Scan(&id)
	if !errors.Is(err, pgx.ErrNoRows) {
		return id, err
	}

	err = s.pool.QueryRow(ctx, `SELECT id FROM users WHERE token_hash = $1`, tokenHash).Scan(&id)
	return id, err
}
