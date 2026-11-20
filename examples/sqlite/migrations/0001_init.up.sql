CREATE TABLE accounts (
    id         INTEGER PRIMARY KEY,
    owner      TEXT     NOT NULL UNIQUE,
    balance    INTEGER  NOT NULL CHECK (balance >= 0),
    created_at DATETIME NOT NULL
);

CREATE TABLE transfers (
    id         INTEGER PRIMARY KEY,
    from_id    INTEGER  NOT NULL REFERENCES accounts (id),
    to_id      INTEGER  NOT NULL REFERENCES accounts (id),
    amount     INTEGER  NOT NULL CHECK (amount > 0),
    created_at DATETIME NOT NULL
);

CREATE INDEX transfers_created_at_id ON transfers (created_at, id);
