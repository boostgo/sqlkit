CREATE TABLE accounts (
    id         BIGSERIAL PRIMARY KEY,
    owner      TEXT     NOT NULL UNIQUE,
    balance    INTEGER  NOT NULL CHECK (balance >= 0),
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE transfers (
    id         BIGSERIAL PRIMARY KEY,
    from_id    BIGINT   NOT NULL REFERENCES accounts (id),
    to_id      BIGINT   NOT NULL REFERENCES accounts (id),
    amount     INTEGER  NOT NULL CHECK (amount > 0),
    created_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX transfers_created_at_id ON transfers (created_at, id);
