-- Paisa Wallet initial schema.


CREATE TABLE users (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash TEXT        NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE wallets (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       UUID        UNIQUE REFERENCES users (id),
    is_treasury   BOOLEAN     NOT NULL DEFAULT false,
    balance_paise BIGINT      NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT wallets_no_overdraft CHECK (is_treasury OR balance_paise >= 0),
    CONSTRAINT wallets_owner CHECK (is_treasury <> (user_id IS NOT NULL))
);

CREATE UNIQUE INDEX wallets_single_treasury ON wallets ((true)) WHERE is_treasury;

CREATE TABLE transfers (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    requester_user_id   UUID        NOT NULL REFERENCES users (id),
    idempotency_key     TEXT        NOT NULL,
    -- SHA-256 of the canonical request. Distinguishes "same request again" from
    -- "same key, different body" on replay.
    request_fingerprint TEXT        NOT NULL,
    from_wallet         UUID        NOT NULL REFERENCES wallets (id),
    to_wallet           UUID        NOT NULL REFERENCES wallets (id),
    amount_paise        BIGINT      NOT NULL,
    status              TEXT        NOT NULL,
    decline_reason      TEXT,
    -- The exact status and body sent to the original caller. Replays return these verbatim,
    -- so a retry is identical by construction rather than by recomputation.
    response_status     INT,
    response_body       JSONB,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT transfers_amount_positive     CHECK (amount_paise > 0),
    CONSTRAINT transfers_status_valid        CHECK (status IN ('pending', 'succeeded', 'declined')),
    CONSTRAINT transfers_distinct_wallets    CHECK (from_wallet <> to_wallet),
    CONSTRAINT transfers_reason_iff_declined CHECK ((status = 'declined') = (decline_reason IS NOT NULL)),

    -- INVARIANT (exactly-once): the idempotency mutex. Inserted first and committed in the
    -- same transaction as the debit and credit, so "money moved" and "key consumed" cannot
    -- be separated. Scoped per caller.
    CONSTRAINT transfers_idempotency UNIQUE (requester_user_id, idempotency_key)
);

-- INVARIANT (conservation): every movement writes two rows summing to zero, so the sum of
-- delta_paise over the whole table is always zero and each wallet's entries reconcile to
-- its balance. This is the audit trail that proves conservation, not the balance source.
CREATE TABLE ledger_entries (
    id          BIGSERIAL   PRIMARY KEY,
    transfer_id UUID        NOT NULL REFERENCES transfers (id),
    wallet_id   UUID        NOT NULL REFERENCES wallets (id),
    delta_paise BIGINT      NOT NULL, -- negative = debit, positive = credit
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT ledger_entries_nonzero        CHECK (delta_paise <> 0),
    CONSTRAINT ledger_entries_one_per_wallet UNIQUE (transfer_id, wallet_id)
);

CREATE INDEX ledger_entries_wallet_idx ON ledger_entries (wallet_id);
CREATE INDEX transfers_requester_idx   ON transfers (requester_user_id);

INSERT INTO wallets (id, is_treasury)
VALUES ('00000000-0000-0000-0000-000000000000', true);
