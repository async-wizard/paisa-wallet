# Paisa Wallet

A small wallet service with peer-to-peer transfers that stays correct under concurrency. Money is integer paise throughout.

- **Live:** https://paisa-wallet.onrender.com (free tier, sleeps when idle; the first request can take about a minute)
- **Logs:** [`/logs/stream`](https://paisa-wallet.onrender.com/logs/stream) · **Metrics:** [`/metrics`](https://paisa-wallet.onrender.com/metrics) · **Invariant audit:** [`/internal/invariants`](https://paisa-wallet.onrender.com/internal/invariants)
- **Design and reasoning:** [WRITEUP.md](WRITEUP.md)

## Run locally

```bash
docker compose up --build
```

The app is on http://localhost:8080 and Postgres on localhost:55432. If those ports are taken, set `APP_PORT` / `DB_PORT`.

## Try it

A bearer token identifies the caller, and the first request with a new token creates that user. Locally the faucet token is `local-dev-admin-token-do-not-deploy`.

```bash
BASE=http://localhost:8080
ADMIN=local-dev-admin-token-do-not-deploy

# get-or-create a wallet for two users
ALICE=$(curl -s -X POST -H "Authorization: Bearer alice-token" $BASE/wallets | jq -r .id)
BOB=$(curl -s -X POST -H "Authorization: Bearer bob-token" $BASE/wallets | jq -r .id)

# fund Alice with ₹100 (test faucet, admin only)
curl -s -X POST -H "Authorization: Bearer $ADMIN" \
  -d '{"amount_paise":10000,"idempotency_key":"fund-1"}' $BASE/wallets/$ALICE/credit

# transfer ₹25; re-sending the same body with the same key replays the original result
curl -s -X POST -H "Authorization: Bearer alice-token" \
  -d "{\"from\":\"$ALICE\",\"to\":\"$BOB\",\"amount_paise\":2500,\"idempotency_key\":\"t-1\"}" $BASE/transfers

curl -s -H "Authorization: Bearer alice-token" $BASE/wallets/$ALICE
```

| Endpoint | Notes |
|---|---|
| `POST /wallets` | Get-or-create the caller's wallet: `201` created, `200` existing |
| `GET /wallets/{id}` | Balance; owner only |
| `POST /transfers` | `{from, to, amount_paise, idempotency_key}`: `201` · `422` insufficient funds · `409` key reused with a different body |
| `GET /transfers/{id}` | Transfer status; sender or recipient |
| `POST /wallets/{id}/credit` | Test faucet, needs the admin token |

## Tests

Integration tests run against real Postgres, including the concurrency cases:

```bash
docker compose up -d db
TEST_DATABASE_URL='postgres://paisa:paisa@localhost:55432/paisa?sslmode=disable' go test ./...
```

## Deploy configuration

`DATABASE_URL` (Postgres connection string), `ADMIN_TOKEN` (at least 32 characters; the dev value is refused unless `APP_ENV=local`), and optionally `PORT` and `DB_MAX_CONNS`.
