package wallet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/async-wizard/paisa-wallet/internal/obs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// TreasuryID is the fixed id of the treasury wallet seeded by the initial migration.
var TreasuryID = uuid.Nil

// MaxAmountPaise caps a single movement well below BIGINT overflow.
const MaxAmountPaise int64 = 1_000_000_000_000

var (
	ErrForbidden      = errors.New("forbidden")
	ErrSameWallet     = errors.New("from and to are the same wallet")
	ErrInvalidAmount  = errors.New("amount out of range")
	ErrKeyReused      = errors.New("idempotency key reused with a different request")
	ErrInvalidRequest = errors.New("invalid request")
)

const (
	StatusSucceeded = "succeeded"
	StatusDeclined  = "declined"

	ReasonInsufficientFunds = "insufficient_funds"
)

type Transfer struct {
	ID            uuid.UUID `json:"id"`
	From          uuid.UUID `json:"from"`
	To            uuid.UUID `json:"to"`
	AmountPaise   int64     `json:"amount_paise"`
	Status        string    `json:"status"`
	DeclineReason string    `json:"decline_reason,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// Outcome is the HTTP response for a money movement. Body is exactly what was stored for
// the idempotency key, so the original response and every replay are identical.
type Outcome struct {
	StatusCode int
	Body       []byte
	Replayed   bool
}

// Transfer moves amount from a wallet the requester owns to another user's wallet.
func (s *Service) Transfer(ctx context.Context, requester, from, to uuid.UUID, amount int64, key string) (Outcome, error) {
	if from == to {
		return Outcome{}, ErrSameWallet
	}

	// Ownership never changes after a wallet is created, so it is safe to check outside the
	// transaction. The treasury has no owner: it can't be spent from or sent to by users.
	rows, err := s.pool.Query(ctx, `SELECT id, user_id FROM wallets WHERE id IN ($1, $2)`, from, to)
	if err != nil {
		return Outcome{}, err
	}
	owners := map[uuid.UUID]*uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		var owner *uuid.UUID
		if err := rows.Scan(&id, &owner); err != nil {
			return Outcome{}, err
		}
		owners[id] = owner
	}
	if err := rows.Err(); err != nil {
		return Outcome{}, err
	}

	fromOwner, fromOK := owners[from]
	toOwner, toOK := owners[to]
	if !fromOK || !toOK || toOwner == nil {
		return Outcome{}, ErrNotFound
	}
	if fromOwner == nil || *fromOwner != requester {
		return Outcome{}, ErrForbidden
	}

	return s.execute(ctx, "transfer", requester, from, to, amount, key)
}

// execute is the single money-movement path, used for both transfers and mints.
//
// One READ COMMITTED transaction:
//  1. Insert the transfer row first. The UNIQUE (requester_user_id, idempotency_key)
//     constraint is the idempotency mutex: a concurrent duplicate waits here for the first
//     request to commit, gets no row back, and replays the stored response instead.
//  2. Move the money (moveMoney), or record a decline.
//  3. Store the response on the row and send exactly those bytes.
//
// Key consumed and money moved commit together or not at all. Domain events are logged
// only after the commit succeeds, so the logs never describe a movement that rolled back.
func (s *Service) execute(ctx context.Context, kind string, requester, from, to uuid.UUID, amount int64, key string) (Outcome, error) {
	if amount <= 0 || amount > MaxAmountPaise {
		return Outcome{}, ErrInvalidAmount
	}
	if key == "" || len(key) > 128 {
		return Outcome{}, ErrInvalidRequest
	}
	fp := fingerprint(kind, from, to, amount)

	var (
		out      Outcome
		t        Transfer
		m        movement
		replayID uuid.UUID
	)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		t = Transfer{From: from, To: to, AmountPaise: amount}
		err := tx.QueryRow(ctx, `
			INSERT INTO transfers (requester_user_id, idempotency_key, request_fingerprint,
			                       from_wallet, to_wallet, amount_paise, status)
			VALUES ($1, $2, $3, $4, $5, $6, 'pending')
			ON CONFLICT (requester_user_id, idempotency_key) DO NOTHING
			RETURNING id, created_at`,
			requester, key, fp, from, to, amount,
		).Scan(&t.ID, &t.CreatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			replayID, out, err = replay(ctx, tx, requester, key, fp)
			return err
		}
		if err != nil {
			return fmt.Errorf("insert transfer: %w", err)
		}

		m, err = moveMoney(ctx, tx, t.ID, from, to, amount)
		if err != nil {
			return err
		}

		out.StatusCode = http.StatusCreated
		t.Status = StatusSucceeded
		if !m.moved {
			out.StatusCode = http.StatusUnprocessableEntity
			t.Status = StatusDeclined
			t.DeclineReason = ReasonInsufficientFunds
		}

		body, err := json.Marshal(t)
		if err != nil {
			return err
		}
		var reason *string
		if t.DeclineReason != "" {
			reason = &t.DeclineReason
		}
		// JSONB normalises key order and spacing, so send back what Postgres stored rather
		// than what we marshalled; replays read the same stored value.
		return tx.QueryRow(ctx, `
			UPDATE transfers
			   SET status = $2, decline_reason = $3, response_status = $4, response_body = $5
			 WHERE id = $1
			RETURNING response_body`,
			t.ID, t.Status, reason, out.StatusCode, body,
		).Scan(&out.Body)
	})

	switch {
	case errors.Is(err, ErrKeyReused):
		obs.Event(ctx, "transfer.key_conflict", "kind", kind, "idempotency_key", key)
	case err != nil:
	case out.Replayed:
		obs.IdempotentReplays.WithLabelValues(kind).Inc()
		obs.Event(ctx, "transfer.idempotent_replay",
			"kind", kind, "transfer_id", replayID, "idempotency_key", key, "status_code", out.StatusCode)
	default:
		obs.TransfersCreated.WithLabelValues(kind).Inc()
		obs.Event(ctx, "transfer.created",
			"kind", kind, "transfer_id", t.ID, "from", from, "to", to, "amount_paise", amount, "idempotency_key", key)
		if m.moved {
			obs.Event(ctx, "transfer.debited",
				"kind", kind, "transfer_id", t.ID, "wallet_id", from, "amount_paise", amount, "balance_after_paise", m.fromBalance)
			obs.Event(ctx, "transfer.credited",
				"kind", kind, "transfer_id", t.ID, "wallet_id", to, "amount_paise", amount, "balance_after_paise", m.toBalance)
		} else {
			obs.TransfersDeclined.WithLabelValues(kind, t.DeclineReason).Inc()
			obs.Event(ctx, "transfer.declined",
				"kind", kind, "transfer_id", t.ID, "wallet_id", from, "amount_paise", amount, "reason", t.DeclineReason)
		}
	}
	return out, err
}

type movement struct {
	moved                  bool
	fromBalance, toBalance int64
}

// moveMoney locks both wallets, then debits and credits. It reports moved=false, having
// changed nothing, when the source can't cover the amount.
//
// Both rows are locked up front, lowest id first (Postgres sorts before applying the
// locking clause). A fixed lock order means concurrent A->B and B->A transfers queue
// instead of deadlocking. Locking before writing means a declined debit never has to undo
// a credit that already ran.
func moveMoney(ctx context.Context, tx pgx.Tx, transferID, from, to uuid.UUID, amount int64) (movement, error) {
	var m movement
	locked, err := tx.Exec(ctx,
		`SELECT id FROM wallets WHERE id IN ($1, $2) ORDER BY id FOR NO KEY UPDATE`, from, to)
	if err != nil {
		return m, fmt.Errorf("lock wallets: %w", err)
	}
	if locked.RowsAffected() != 2 {
		return m, ErrNotFound
	}

	err = tx.QueryRow(ctx, `
		UPDATE wallets SET balance_paise = balance_paise - $1, updated_at = now()
		 WHERE id = $2 AND (is_treasury OR balance_paise >= $1)
		RETURNING balance_paise`,
		amount, from).Scan(&m.fromBalance)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, nil
	}
	if err != nil {
		return m, fmt.Errorf("debit: %w", err)
	}

	if err := tx.QueryRow(ctx,
		`UPDATE wallets SET balance_paise = balance_paise + $1, updated_at = now() WHERE id = $2 RETURNING balance_paise`,
		amount, to).Scan(&m.toBalance); err != nil {
		return m, fmt.Errorf("credit: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO ledger_entries (transfer_id, wallet_id, delta_paise)
		VALUES ($1, $2, $3), ($1, $4, $5)`,
		transferID, from, -amount, to, amount); err != nil {
		return m, fmt.Errorf("ledger: %w", err)
	}
	m.moved = true
	return m, nil
}

// replay returns the stored response for an idempotency key that is already used. The
// SELECT is a new statement, so under READ COMMITTED it sees the row the conflicting
// request committed while our insert was waiting.
func replay(ctx context.Context, tx pgx.Tx, requester uuid.UUID, key, fp string) (uuid.UUID, Outcome, error) {
	var (
		id       uuid.UUID
		storedFP string
	)
	out := Outcome{Replayed: true}
	err := tx.QueryRow(ctx, `
		SELECT id, request_fingerprint, response_status, response_body
		  FROM transfers
		 WHERE requester_user_id = $1 AND idempotency_key = $2`,
		requester, key,
	).Scan(&id, &storedFP, &out.StatusCode, &out.Body)
	if err != nil {
		return id, Outcome{}, fmt.Errorf("read existing transfer: %w", err)
	}
	if storedFP != fp {
		return id, Outcome{}, ErrKeyReused
	}
	return id, out, nil
}

// GetTransfer returns a transfer's stored representation if the user sent it or owns
// either wallet involved.
func (s *Service) GetTransfer(ctx context.Context, userID, transferID uuid.UUID) ([]byte, error) {
	var body []byte
	err := s.pool.QueryRow(ctx, `
		SELECT t.response_body
		  FROM transfers t
		  JOIN wallets f ON f.id = t.from_wallet
		  JOIN wallets w ON w.id = t.to_wallet
		 WHERE t.id = $1
		   AND (t.requester_user_id = $2 OR f.user_id = $2 OR w.user_id = $2)`,
		transferID, userID,
	).Scan(&body)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return body, err
}

func fingerprint(kind string, from, to uuid.UUID, amount int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%d", kind, from, to, amount)))
	return hex.EncodeToString(sum[:])
}
