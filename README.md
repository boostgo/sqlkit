# sqlkit

[![Go Reference](https://pkg.go.dev/badge/github.com/boostgo/sqlkit.svg)](https://pkg.go.dev/github.com/boostgo/sqlkit)
[![Go Report Card](https://goreportcard.com/badge/github.com/boostgo/sqlkit)](https://goreportcard.com/report/github.com/boostgo/sqlkit)
[![CI](https://github.com/boostgo/sqlkit/actions/workflows/ci.yml/badge.svg)](https://github.com/boostgo/sqlkit/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

**sqlkit** is a small, batteries-included layer over [sqlx](https://github.com/jmoiron/sqlx) for
**PostgreSQL**, **SQLite** and **ClickHouse**. It covers what most services rewrite in every project:
connecting from config, transactions carried in `context.Context`, offset and cursor pagination,
embedded migrations, read replicas and portable error handling.

```go
conn, _ := sqlkit.ConnectFromConfig(sqlkit.Config{Driver: "sqlite", DSN: "app.db"})
db, tr := sqlkit.NewDB(conn), sqlkit.NewTransactor(conn)

err := sqlkit.WithTx(ctx, tr, func(ctx context.Context) error {
    _, err := db.ExecContext(ctx, "INSERT INTO users (email) VALUES (?)", email)
    return err // rollback on error or panic, commit otherwise
})
if sqlkit.IsDuplicate(err) { /* 409 Conflict */ }
```

## Features

- **Four drivers, one API**: `postgres` (lib/pq), `pgx` (jackc/pgx), `sqlite` (modernc.org/sqlite,
  pure Go, **no cgo**) and `clickhouse`. `Rebind` and `Placeholder` keep queries portable.
- **SQLite done right**: WAL, `foreign_keys=on`, `busy_timeout`, `IMMEDIATE` write transactions and
  sortable time values out of the box; in-memory databases get a single pinned connection.
- **Transactions in context**: `WithTx` / `BeginTx` with `ReadOnly()`, `Isolation(level)` and
  `Join()`; every query method picks the transaction up from `ctx`, so repositories stay tx-agnostic.
- **Pagination**: offset and keyset (cursor) pagers with opaque, versioned page tokens, generic
  `PageOf`, and a [go-sqlbuilder](https://github.com/huandu/go-sqlbuilder) adapter (`sqlbpager`).
- **Migrations**: [golang-migrate](https://github.com/golang-migrate/migrate) from a directory or an
  `embed.FS`, for PostgreSQL and SQLite.
- **Error classification**: unique, foreign key, not-null and check violations of PostgreSQL and
  SQLite map to the same sentinel errors; `DuplicateFields` names the offending columns.
- **Read replicas**: `Cluster` routes reads to healthy replicas by replication lag with hysteresis,
  `PreferReplica` / `RequireReplica` / `ForcePrimary` hints and pluggable balancers.
- **Sharding**: a shard client with a context-based selector, `EachShard` and `EachShardAsync`.

## Install

```bash
go get github.com/boostgo/sqlkit
```

Requires Go 1.24 or newer.

## Quick start

### SQLite

```go
package main

import (
    "context"
    "embed"
    "log"

    "github.com/boostgo/sqlkit"
)

//go:embed migrations/*.sql
var migrations embed.FS

func main() {
    ctx := context.Background()

    conn, err := sqlkit.ConnectFromConfig(sqlkit.Config{
        Driver: sqlkit.SQLiteDriver,
        DSN:    "app.db", // or ":memory:", or a "file:" URI
    })
    if err != nil {
        log.Fatal(err)
    }
    defer conn.Close()

    if err = sqlkit.NewMigrator(ctx, conn, "app").WithFS(migrations, "migrations").MigrateContext(ctx); err != nil {
        log.Fatal(err)
    }

    db := sqlkit.NewDB(conn)

    var n int
    if err = db.GetContext(ctx, &n, "SELECT count(*) FROM users WHERE active = ?", true); err != nil {
        log.Fatal(err)
    }
}
```

### PostgreSQL

```go
conn, err := sqlkit.ConnectFromConfig(sqlkit.Config{
    Driver:   sqlkit.PgxDriver, // or sqlkit.PqDriver (default)
    Host:     "localhost",
    Port:     5432,
    Username: "app",
    Password: "secret",
    Database: "app",
    SSLMode:  "require",
    TimeoutMs: 5000,
})
// or: sqlkit.Config{Driver: "pgx", DSN: "postgres://app:secret@localhost:5432/app"}

db := sqlkit.NewDB(conn)
err = db.GetContext(ctx, &n, "SELECT count(*) FROM users WHERE active = $1", true)
```

`Config` has JSON and YAML tags, so it can be embedded in your application config as is:

```yaml
database:
  driver: sqlite
  dsn: /var/lib/app/app.db
  sqlite:
    busyTimeoutMs: 10000
    pragmas: "cache_size(-20000); temp_store(MEMORY)"
```

Runnable programs live in [`examples/`](examples): [`sqlite`](examples/sqlite/main.go) (runs as is:
`go run ./examples/sqlite`), [`postgres`](examples/postgres/main.go) and
[`cluster`](examples/cluster/main.go).

## Usage

### Repositories and portable queries

Write queries with `?` and let `Rebind` convert them to `$1, $2, ...` for PostgreSQL:

```go
type Repository struct {
    db sqlkit.DB
    tr sqlkit.Transactor
}

func (r *Repository) Create(ctx context.Context, email string) (int64, error) {
    var id int64
    err := r.db.GetContext(ctx, &id,
        sqlkit.Rebind(r.db, "INSERT INTO users (email) VALUES (?) RETURNING id"), email)
    return id, sqlkit.Classify(err)
}
```

`NamedExecContext` and `NamedQueryRowxContext` (`:name` arguments) choose the right bind style
automatically. `$N` placeholders also work with SQLite, so `Arguments` and `Dollar` can be shared.

### Transactions

```go
tr := sqlkit.NewTransactor(conn)

err := sqlkit.WithTx(ctx, tr, func(ctx context.Context) error {
    if err := repo.Debit(ctx, from, amount); err != nil {
        return err // rolled back
    }
    return repo.Credit(ctx, to, amount) // committed if nil
}, sqlkit.Isolation(sql.LevelSerializable))
```

- The transaction travels in `ctx`; `db.ExecContext(ctx, ...)` and friends use it automatically.
- `WithTx` commits on `nil`, rolls back on error **and on panic** (the panic is re-raised).
- `Join()` reuses a transaction already in `ctx`; inner commits/rollbacks become no-ops. **Without
  `Join()` a nested `WithTx` opens a second transaction on another connection.**
- `ReadOnly()` and `Isolation(level)` are passed to the driver. SQLite ignores the isolation level
  (it is always serializable) and starts read-only transactions with a plain deferred `BEGIN`.
- Manual control: `ctx, err := tr.Begin(ctx)` then `tr.Commit(ctx)` / `tr.Rollback(ctx)`.

### Pagination

```go
func (r *Repository) List(ctx context.Context, req sqlkit.PageRequest) (sqlkit.PageResult[User], error) {
    p := sqlkit.CursorPager(req, sqlkit.Desc("created_at"), sqlkit.Desc("id"))
    if err := p.Err(); err != nil {
        return sqlkit.PageResult[User]{}, err // ErrInvalidCursor -> 400, ErrInvalidPager -> 500
    }

    q, args := "SELECT * FROM users WHERE tenant_id = ?", []any{tenantID}
    if where, wargs := p.Where(sqlkit.Placeholder(sqlkit.DriverName(r.db), len(args)+1)); where != "" {
        q += " AND " + where
        args = append(args, wargs...)
    }

    var rows []User
    if err := r.db.SelectContext(ctx, &rows, sqlkit.Rebind(r.db, q)+" "+p.OrderBy()+" "+p.LimitOffset(), args...); err != nil {
        return sqlkit.PageResult[User]{}, err
    }

    return sqlkit.PageOf(rows, p, func(u User) []any { return []any{u.CreatedAt, u.ID} }), nil
}
```

| Pager | Use it for |
|---|---|
| `OffsetPager(req, keys...)` | numbered pages (`req.Page`), when a total count is shown |
| `CursorPager(req, keys...)` | feeds and large tables: stable under inserts, no OFFSET scan |
| `NewPager(db, req, keys...)` | the client default: `NewDBWithOptions(conn, WithPaging(PagingCursor))` |
| `AllPager(keys...)` | internal snapshots without a limit; never for user input |

Rules: `Size` defaults to 20 and is capped at 1000 (`MaxSize(n)`); the last key must be unique (usually
`id`); cursor keys must be `NOT NULL` and share one direction. Page tokens are base64url JSON with a
fingerprint of the sort order; a token from another sort order or approach restarts at page one.
Store times in UTC: with SQLite they are compared as text.

With go-sqlbuilder: `sqlbpager.Apply(sb, p)` adds the condition, `ORDER BY`, `LIMIT` and `OFFSET`.

### Migrations

```go
//go:embed migrations/*.sql
var migrations embed.FS

err := sqlkit.NewMigrator(ctx, conn, "app").
    WithFS(migrations, "migrations"). // or WithMigrationsDirectory("./migrations")
    WithMigrationsTable("schema_migrations").
    MigrateContext(ctx)
```

Files follow the golang-migrate naming: `0001_create_users.up.sql`, `0001_create_users.down.sql`.
The driver is picked from the connection: PostgreSQL migrations run on a dedicated connection with
`lock_timeout = 60s`; SQLite migrations run on the pool, which stays open afterwards. "No change" is
not an error. ClickHouse returns `ErrMigrateUnsupportedDriver`.

### Read replicas (PostgreSQL)

```go
cluster, err := sqlkit.ConnectCluster(sqlkit.ClusterConfig{
    Primary:         sqlkit.Config{Host: "db-primary", Port: 5432, Username: "app", Password: "secret", Database: "app"},
    ReplicasEnabled: true,
    Replicas: []sqlkit.ReplicaConfig{
        {Name: "ro1", Config: sqlkit.Config{Host: "db-ro1"}}, // zero fields are inherited from the primary
        {Name: "ro2", Config: sqlkit.Config{Host: "db-ro2"}},
    },
}, sqlkit.WithReplicaBalancer(sqlkit.LeastLag()))
defer cluster.Close()

err = cluster.SelectContext(sqlkit.PreferReplica(ctx), &rows, q)  // replica if healthy, else primary
err = cluster.SelectContext(sqlkit.RequireReplica(ctx), &rows, q) // replica or ErrReplicaUnavailable
err = cluster.SelectContext(sqlkit.ForcePrimary(ctx), &rows, q)   // always primary
```

Writes, prepares and anything inside a transaction always go to the primary. Replication lag is checked
in the background (`pg_last_wal_replay_lsn`); a replica is dropped above `WithMaxLag` and comes back at
half of it. A read that hits a recovery conflict (SQLSTATE `40001`) on a replica is retried once.
`Cluster` implements `DB`, so repositories do not change.

### Error handling

```go
_, err := db.ExecContext(ctx, query, args...)
switch {
case sqlkit.IsDuplicate(err):
    fields, _ := sqlkit.DuplicateFields(err) // e.g. ["email"]
case sqlkit.IsForeignKey(err), sqlkit.IsNotNull(err), sqlkit.IsCheckViolation(err):
case sqlkit.IsNoRows(err):
case errors.Is(err, sqlkit.ErrExecContext): // any other failure of ExecContext
}
```

| Sentinel | PostgreSQL | SQLite |
|---|---|---|
| `ErrDuplicate` | `23505` unique_violation | `SQLITE_CONSTRAINT_UNIQUE`, `_PRIMARYKEY` |
| `ErrForeignKeyViolation` | `23503` | `SQLITE_CONSTRAINT_FOREIGNKEY` |
| `ErrNotNull` | `23502` | `SQLITE_CONSTRAINT_NOTNULL` |
| `ErrCheckViolation` | `23514` | `SQLITE_CONSTRAINT_CHECK` |

`Classify(err)` wraps the driver error with the sentinel and keeps it in the chain, so
`errors.As(err, &pgErr)` still works. Every `DB` method wraps its errors with its own sentinel
(`ErrExecContext`, `ErrSelectContext`, ...).

### Sharding

```go
conns, err := sqlkit.ConnectShards(sqlkit.PgxDriver, []sqlkit.ShardConnectString{
    {Key: "eu", ConnectionString: euDSN},
    {Key: "us", ConnectionString: usDSN},
}, selectByRegion, 5*time.Second)
db := sqlkit.NewShardDB(conns)

err = db.EachShardAsync(func(shard sqlkit.DB) error { /* ... */ return nil }, 4)
```

## Options reference

### `Config`

| Field | YAML | Default | Description |
|---|---|---|---|
| `Driver` | `driver` | `postgres` | `postgres`, `pgx`, `sqlite`, `clickhouse` |
| `DSN` | `dsn` | | Connection string used as is; for SQLite the file path, `:memory:` or `file:` URI (required) |
| `Host`, `Port`, `Username`, `Password`, `Database` | `host`, ... | | Server connection (PostgreSQL, ClickHouse) |
| `SSLMode` | `sslMode` | `disable` | PostgreSQL `sslmode` |
| `TimeoutMs` | `timeoutMs` | none | Timeout of the initial ping |
| `MaxOpenConnections` | `maxOpenConnections` | 10 | Pool size |
| `MaxIdleConnections` | `maxIdleConnections` | 10 | Idle connections kept |
| `MaxLifetime` | `maxLifetime` | 30m | Connection lifetime, seconds |
| `MaxIdleTime` | `maxIdleTime` | 5m | Connection idle time, seconds |
| `Migration*` | `migrationHost`, ... | main values | Separate host/credentials for migrations |
| `BinaryParameters` | `binaryParameters` | false | lib/pq `binary_parameters=yes` |
| `ClickhouseScheme` | `clickhouseScheme` | `https` (`http` if `LOCAL=true`) | ClickHouse URL scheme |
| `ReadTimeout`, `WriteTimeout` | `readTimeout`, ... | driver | ClickHouse timeouts, seconds |
| `SQLite` | `sqlite` | see below | SQLite pragmas |

### `SQLiteOptions`

| Field | YAML | Default | Description |
|---|---|---|---|
| `JournalMode` | `journalMode` | `WAL` | `PRAGMA journal_mode` |
| `Synchronous` | `synchronous` | `NORMAL` | `PRAGMA synchronous` |
| `BusyTimeoutMs` | `busyTimeoutMs` | 5000 | `PRAGMA busy_timeout`; negative disables |
| `DisableForeignKeys` | `disableForeignKeys` | false | Foreign keys are on unless set |
| `TxLock` | `txLock` | `immediate` | `BEGIN` mode of write transactions: `deferred`, `immediate`, `exclusive` |
| `Pragmas` | `pragmas` | | Extra pragmas, `;`-separated, e.g. `cache_size(-20000); temp_store(MEMORY)` |

### Transactions, pagers and clusters

| Option | Applies to | Description |
|---|---|---|
| `ReadOnly()` | `WithTx`, `BeginTx` | Read-only transaction |
| `Isolation(level)` | `WithTx`, `BeginTx` | Isolation level (default `ReadCommitted`; ignored by SQLite) |
| `Join()` | `WithTx`, `BeginTx` | Reuse the transaction in `ctx` |
| `Asc(col)`, `Desc(col)` | pagers | Sort keys, in order; the last must be unique |
| `MaxSize(n)` | pagers | Page size cap (default 1000) |
| `WithPaging(mode)` | `NewDBWithOptions` | Default approach of `NewPager` |
| `WithReplica(db)`, `WithNamedReplica(name, db)` | `NewCluster` | Add replicas |
| `WithReplicaBalancer(b)` | clusters | `RoundRobin()` (default) or `LeastLag()` |
| `WithMaxLag(d)` | clusters | Max replication lag (default 30s) |
| `WithLagCheckInterval(d)` | clusters | Lag check period (default 5s) |
| `WithRecoveryConflictRetry(on)` | clusters | Retry reads on `40001` (default true) |
| `WithStateHook(fn)` | clusters | Called when a replica becomes usable or unusable |

## Contributing

Issues and pull requests are welcome. Before sending a change:

```bash
make vet lint test   # go vet, golangci-lint, go test -race
make cover           # coverage summary
```

Tests that need a server are skipped unless their DSN is set:

```bash
make postgres-up
SQLKIT_TEST_POSTGRES_DSN="postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable" make test
make postgres-down
```

`SQLKIT_TEST_CLICKHOUSE_DSN` (e.g. `http://default:@localhost:8123/default`) enables the ClickHouse
test. Please add tests for new behaviour and an entry to [CHANGELOG.md](CHANGELOG.md).

## License

[MIT](LICENSE) © 2026 boostgo
