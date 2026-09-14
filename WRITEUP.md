# Paisa Wallet: write-up

**Live:** https://paisa-wallet.onrender.com · **Public logs:** `/logs/stream` · **Metrics:** `/metrics` · **Audit:** `/internal/invariants`
Go · pgx · Postgres 17 on Render + Neon (both Singapore)

## Data model
Money is `BIGINT` paise and Go `int64`; non-integer amounts are rejected. Each invariant has a database constraint behind it:
- **`users`**: `token_hash UNIQUE`, the SHA-256 of the bearer token. First use of a token creates the user.
- **`wallets`**: `user_id UNIQUE` (race-free get-or-create) and `CHECK (balance_paise >= 0)` (no overdraft).
- **`transfers`**: `UNIQUE (requester_user_id, idempotency_key)`, a request fingerprint, and the stored response status and body.
- **`ledger_entries`**: two rows per movement (−amt, +amt), so conservation can be audited.

A **treasury** wallet, the only one allowed to go negative, funds test wallets via an admin-only faucet, using the same code path as transfers. All balances therefore sum to 0, which `/internal/invariants` checks live.

## Conservation + no overdraft
One `READ COMMITTED` transaction:
1. Insert the transfer row.
2. `SELECT … WHERE id IN (from, to) ORDER BY id FOR NO KEY UPDATE`.
3. `UPDATE … SET balance = balance − amt WHERE id = from AND balance >= amt`. 0 rows → declined (422), with nothing changed.
4. Credit, ledger rows, commit.

**Deadlock:** both wallets are locked in ascending id order, so A→B and B→A queue behind each other instead of forming a cycle. Locking *before* the debit matters. My first version updated in id order without locking, so a credit could run before a declined debit. A test with that version swapped in created ₹441.50; a debit-first variant deadlocked (`40P01`).

**Rejected:**
- **`SERIALIZABLE`:** `40001` retry loops exactly when contention peaks.
- **Optimistic versioning:** retry storms.
- **Advisory locks:** a second lock scheme with nothing enforcing it.
- **Ledger-derived balance:** a `SUM` on every debit.
- **App mutex:** breaks with two instances.

**Lesson:** lock time ≈ in-transaction round trips × network latency. App in India with DB in Singapore (75 ms per round trip) timed out 4 of 300 transfers; colocated, p99 is 4 s.

## Where idempotency lives
In Postgres, on `UNIQUE (requester_user_id, idempotency_key)`, **in the same transaction as the debit, credit and ledger rows**, so "key consumed" and "money moved" commit together or not at all. The row is inserted first with `ON CONFLICT DO NOTHING`. A concurrent duplicate waits for the first commit, gets no row back, and never reaches the debit.
- **Same key, same body:** the stored status and body, byte-identical, with `Idempotent-Replay: true`.
- **Same key, different body:** the SHA-256 fingerprint of `kind|from|to|amount` differs → **409**, no money moves.

A check-then-insert version, tested for comparison, executed one transfer 6 times.

## Consistency vs availability
**CP.** One Postgres primary, synchronous commits, no cache or replica reads. A lost or dropped database returns **503** (tested), and a client unsure of the outcome retries with the same key. **Given up:** writes during a DB outage, horizontal write scaling, and on free tiers, Render's sleep after 15 min idle (about 1 min to wake; in-memory logs and counters reset). A refused write can be retried; a double-spend can't be undone.

## Evidence (burst against the live URL)
- **Get-or-create:** 50 concurrent → one wallet (1×201, 49×200).
- **Retry storm:** 50 identical transfers → 1 executed, 49 identical replays; reused key → 409.
- **Contention:** 300 transfers across 6 wallets, including A→B with B→A → 272 succeeded, 28 declined, **₹600.00 before and after**, 0 negative, 0 errors. The audit reports ledger ₹0 and 0 unreconciled wallets.

**Logs:** JSON with `request_id` on every line; events `transfer.created`, `debited`, `credited`, `declined`, `idempotent_replay`. **Metrics:** rate, p99, 5xx rate, created/declined/replay counters. The burst script is sent separately and exits non-zero on any violation.

## AI: directed vs decided
An AI assistant (Claude Code) wrote the code, tests and draft.

**I directed:**
- Go over Java
- Render + Neon over GCP
- lock-then-debit over compensating writes
- cutting scope to the brief (dashboard, extra metrics, `/readyz`, unused columns)
- rejecting an idle-connection tweak
- keeping stored responses (after reading Stripe's idempotency docs)

**I accepted its design for:**
- the constraint-first schema, treasury and ledger
- insert-first idempotency and response replay
- the invariants audit, log stream and metrics
- the anti-pattern tests

Its first plan had two errors, both caught by verification before shipping: the credit-before-declined-debit flaw, and an `ON CONFLICT DO UPDATE` get-or-create.

## Cost
**₹0, no card:** Render free web service + Neon free Postgres.
