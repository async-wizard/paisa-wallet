package wallet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/async-wizard/paisa-wallet/internal/store"
	"github.com/async-wizard/paisa-wallet/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These are integration tests against a real Postgres, because the properties under test
// (row locks, unique-index waits, constraint checks) only exist in the database.
// Run with: TEST_DATABASE_URL=postgres://paisa:paisa@localhost:55432/paisa?sslmode=disable go test ./...

func setup(t *testing.T) (*Service, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	// Domain events would otherwise flood the output of a failing run.
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	pool, err := store.Open(ctx, dsn, 20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := store.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return NewService(pool), pool
}

// newWallet creates a fresh user and wallet, so tests never share state with each other
// or with anything else in the database.
func newWallet(t *testing.T, svc *Service) (userID, walletID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	userID, err := svc.UserIDForToken(ctx, "test-"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	w, _, err := svc.GetOrCreateWallet(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	return userID, w.ID
}

func fund(t *testing.T, svc *Service, walletID uuid.UUID, amount int64) {
	t.Helper()
	out, err := svc.Mint(context.Background(), walletID, amount, "fund-"+uuid.NewString())
	if err != nil || out.StatusCode != http.StatusCreated {
		t.Fatalf("fund: status %d err %v", out.StatusCode, err)
	}
}

func balance(t *testing.T, pool *pgxpool.Pool, walletID uuid.UUID) int64 {
	t.Helper()
	var b int64
	if err := pool.QueryRow(context.Background(), `SELECT balance_paise FROM wallets WHERE id = $1`, walletID).Scan(&b); err != nil {
		t.Fatal(err)
	}
	return b
}

func ledgerRows(t *testing.T, pool *pgxpool.Pool, body []byte) int {
	t.Helper()
	var tr Transfer
	if err := json.Unmarshal(body, &tr); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM ledger_entries WHERE transfer_id = $1`, tr.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Gate 1: concurrent get-or-create for one user yields one wallet.
func TestGetOrCreateWalletConcurrent(t *testing.T) {
	svc, _ := setup(t)
	ctx := context.Background()
	userID, err := svc.UserIDForToken(ctx, "test-"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}

	const n = 50
	ids := make([]uuid.UUID, n)
	created := make([]bool, n)
	errs := make([]error, n)
	var start, wg sync.WaitGroup
	start.Add(1)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start.Wait()
			w, c, err := svc.GetOrCreateWallet(ctx, userID)
			ids[i], created[i], errs[i] = w.ID, c, err
		}()
	}
	start.Done()
	wg.Wait()

	createdCount := 0
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("request %d: %v", i, errs[i])
		}
		if ids[i] != ids[0] {
			t.Fatalf("got two wallets: %s and %s", ids[0], ids[i])
		}
		if created[i] {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatalf("created reported %d times, want 1", createdCount)
	}
}

// Gate 2: the same transfer fired concurrently applies once and every response is identical.
func TestTransferIdempotentUnderConcurrency(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()
	alice, aw := newWallet(t, svc)
	_, bw := newWallet(t, svc)
	fund(t, svc, aw, 1000)

	const n = 50
	outs := make([]Outcome, n)
	errs := make([]error, n)
	var start, wg sync.WaitGroup
	start.Add(1)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start.Wait()
			outs[i], errs[i] = svc.Transfer(ctx, alice, aw, bw, 100, "same-key")
		}()
	}
	start.Done()
	wg.Wait()

	fresh := 0
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("request %d: %v", i, errs[i])
		}
		if outs[i].StatusCode != http.StatusCreated {
			t.Fatalf("request %d: status %d, want 201", i, outs[i].StatusCode)
		}
		if !bytes.Equal(outs[i].Body, outs[0].Body) {
			t.Fatalf("responses differ:\n%s\n%s", outs[0].Body, outs[i].Body)
		}
		if !outs[i].Replayed {
			fresh++
		}
	}
	if fresh != 1 {
		t.Fatalf("%d requests executed, want exactly 1", fresh)
	}
	if got := balance(t, pool, aw); got != 900 {
		t.Fatalf("alice balance %d, want 900", got)
	}
	if got := balance(t, pool, bw); got != 100 {
		t.Fatalf("bob balance %d, want 100", got)
	}
	if got := ledgerRows(t, pool, outs[0].Body); got != 2 {
		t.Fatalf("ledger rows %d, want 2", got)
	}
}

func TestKeyReusedWithDifferentBodyConflicts(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()
	alice, aw := newWallet(t, svc)
	_, bw := newWallet(t, svc)
	fund(t, svc, aw, 1000)

	if _, err := svc.Transfer(ctx, alice, aw, bw, 100, "k"); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Transfer(ctx, alice, aw, bw, 101, "k")
	if !errors.Is(err, ErrKeyReused) {
		t.Fatalf("err %v, want ErrKeyReused", err)
	}
	if got := balance(t, pool, aw); got != 900 {
		t.Fatalf("alice balance %d, want 900 (second request must not debit)", got)
	}
}

// Regression for the flaw found in step 3: when the recipient's wallet id sorts before the
// sender's, a declined debit must not leave the credit applied.
func TestDeclineWhenRecipientSortsFirst(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()

	var sender, senderW, recipientW uuid.UUID
	for {
		u1, w1 := newWallet(t, svc)
		_, w2 := newWallet(t, svc)
		if bytes.Compare(w2[:], w1[:]) < 0 {
			sender, senderW, recipientW = u1, w1, w2
			break
		}
	}
	fund(t, svc, senderW, 100)

	out, err := svc.Transfer(ctx, sender, senderW, recipientW, 500, "overdraw")
	if err != nil {
		t.Fatal(err)
	}
	if out.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, want 422", out.StatusCode)
	}
	if got := balance(t, pool, senderW); got != 100 {
		t.Fatalf("sender balance %d, want 100", got)
	}
	if got := balance(t, pool, recipientW); got != 0 {
		t.Fatalf("recipient balance %d, want 0: a declined transfer credited money", got)
	}
	if got := ledgerRows(t, pool, out.Body); got != 0 {
		t.Fatalf("ledger rows %d, want 0 for a decline", got)
	}
}

// A decline is a final outcome for its key: topping up and retrying the same key replays
// the decline rather than moving money.
func TestDeclineReplaysAfterTopUp(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()
	alice, aw := newWallet(t, svc)
	_, bw := newWallet(t, svc)

	first, err := svc.Transfer(ctx, alice, aw, bw, 500, "k")
	if err != nil || first.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("first: status %d err %v", first.StatusCode, err)
	}
	fund(t, svc, aw, 1000)

	again, err := svc.Transfer(ctx, alice, aw, bw, 500, "k")
	if err != nil {
		t.Fatal(err)
	}
	if !again.Replayed || again.StatusCode != first.StatusCode || !bytes.Equal(again.Body, first.Body) {
		t.Fatalf("retry was not an identical replay: %d %s", again.StatusCode, again.Body)
	}
	if got := balance(t, pool, bw); got != 0 {
		t.Fatalf("bob balance %d, want 0", got)
	}
}

func TestCannotSpendOthersWalletOrTreasury(t *testing.T) {
	svc, _ := setup(t)
	ctx := context.Background()
	alice, aw := newWallet(t, svc)
	_, bw := newWallet(t, svc)

	if _, err := svc.Transfer(ctx, alice, bw, aw, 1, "steal"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("spend another user's wallet: err %v, want ErrForbidden", err)
	}
	if _, err := svc.Transfer(ctx, alice, TreasuryID, aw, 1, "print"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("spend the treasury: err %v, want ErrForbidden", err)
	}
	if _, err := svc.Transfer(ctx, alice, aw, TreasuryID, 1, "burn"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("send to the treasury: err %v, want ErrNotFound", err)
	}
}

// Gate 3: hundreds of concurrent transfers among a few wallets, including A->B and B->A at
// once and many that would overdraw. Nothing errors, no balance goes negative, the total is
// unchanged, and every wallet reconciles with the ledger.
func TestConservationUnderContention(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()

	const wallets, perWallet, transfers = 6, 10_000, 300
	users := make([]uuid.UUID, wallets)
	ids := make([]uuid.UUID, wallets)
	for i := range wallets {
		users[i], ids[i] = newWallet(t, svc)
		fund(t, svc, ids[i], perWallet)
	}

	rng := rand.New(rand.NewPCG(1, 2))
	type job struct {
		from, to int
		amount   int64
	}
	jobs := make([]job, transfers)
	for i := range jobs {
		from := rng.IntN(wallets)
		to := (from + 1 + rng.IntN(wallets-1)) % wallets
		if i%2 == 1 { // every other transfer is the exact reverse of the previous one
			from, to = jobs[i-1].to, jobs[i-1].from
		}
		jobs[i] = job{from, to, 1 + rng.Int64N(6000)}
	}

	statuses := make([]int, transfers)
	errs := make([]error, transfers)
	var start, wg sync.WaitGroup
	start.Add(1)
	for i, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start.Wait()
			out, err := svc.Transfer(ctx, users[j.from], ids[j.from], ids[j.to], j.amount, fmt.Sprintf("c-%d", i))
			statuses[i], errs[i] = out.StatusCode, err
		}()
	}
	start.Done()
	wg.Wait()

	succeeded, declined := 0, 0
	for i := range transfers {
		if errs[i] != nil {
			t.Fatalf("transfer %d failed: %v", i, errs[i])
		}
		switch statuses[i] {
		case http.StatusCreated:
			succeeded++
		case http.StatusUnprocessableEntity:
			declined++
		default:
			t.Fatalf("transfer %d: status %d", i, statuses[i])
		}
	}
	t.Logf("succeeded=%d declined=%d", succeeded, declined)
	if declined == 0 {
		t.Fatal("no transfer was declined; the test is not exercising overdraw")
	}

	var total int64
	for _, id := range ids {
		b := balance(t, pool, id)
		if b < 0 {
			t.Fatalf("wallet %s went negative: %d", id, b)
		}
		total += b
	}
	if total != wallets*perWallet {
		t.Fatalf("total %d, want %d: money was created or destroyed", total, wallets*perWallet)
	}

	var mismatched int
	var ledgerSum int64
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM wallets w
		  LEFT JOIN (SELECT wallet_id, SUM(delta_paise) AS s FROM ledger_entries GROUP BY wallet_id) l
		    ON l.wallet_id = w.id
		 WHERE w.id = ANY($1) AND w.balance_paise <> COALESCE(l.s, 0)`, ids).Scan(&mismatched); err != nil {
		t.Fatal(err)
	}
	if mismatched != 0 {
		t.Fatalf("%d wallets do not reconcile with the ledger", mismatched)
	}
	if err := pool.QueryRow(ctx, `SELECT COALESCE(SUM(delta_paise), 0) FROM ledger_entries`).Scan(&ledgerSum); err != nil {
		t.Fatal(err)
	}
	if ledgerSum != 0 {
		t.Fatalf("global ledger sum %d, want 0", ledgerSum)
	}
}

// Money must serialise as a JSON integer, never a float or a string.
func TestAmountIsJSONInteger(t *testing.T) {
	svc, _ := setup(t)
	alice, aw := newWallet(t, svc)
	_, bw := newWallet(t, svc)
	fund(t, svc, aw, 1000)

	out, err := svc.Transfer(context.Background(), alice, aw, bw, 250, "k")
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(out.Body))
	dec.UseNumber()
	var body map[string]any
	if err := dec.Decode(&body); err != nil {
		t.Fatal(err)
	}
	n, ok := body["amount_paise"].(json.Number)
	if !ok || strings.ContainsAny(n.String(), ".eE") || n.String() != "250" {
		t.Fatalf("amount_paise = %#v, want the JSON integer 250", body["amount_paise"])
	}
}

// The live audit agrees with the ledger after real activity, including a decline.
func TestCheckInvariantsHold(t *testing.T) {
	svc, _ := setup(t)
	ctx := context.Background()
	alice, aw := newWallet(t, svc)
	_, bw := newWallet(t, svc)
	fund(t, svc, aw, 1000)
	if _, err := svc.Transfer(ctx, alice, aw, bw, 400, "ok"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Transfer(ctx, alice, aw, bw, 5000, "declined"); err != nil {
		t.Fatal(err)
	}

	inv, err := svc.CheckInvariants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !inv.OK {
		t.Fatalf("invariants violated: %+v", inv)
	}
	if inv.UserBalanceSumPaise != -inv.TreasuryBalancePaise {
		t.Fatalf("user balances %d do not equal minted total %d", inv.UserBalanceSumPaise, -inv.TreasuryBalancePaise)
	}
}
