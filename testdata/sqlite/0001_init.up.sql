CREATE TABLE users (
    id         INTEGER PRIMARY KEY,
    email      TEXT     NOT NULL UNIQUE,
    age        INTEGER  CHECK (age >= 0),
    created_at DATETIME NOT NULL
);

CREATE TABLE orders (
    id      INTEGER PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users (id),
    amount  INTEGER NOT NULL
);
