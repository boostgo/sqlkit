CREATE TABLE sqlkit_users (
    id         BIGSERIAL PRIMARY KEY,
    email      TEXT        NOT NULL UNIQUE,
    age        INTEGER     CHECK (age >= 0),
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE sqlkit_orders (
    id      BIGSERIAL PRIMARY KEY,
    user_id BIGINT  NOT NULL REFERENCES sqlkit_users (id),
    amount  INTEGER NOT NULL
);
