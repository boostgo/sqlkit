# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.0.0] - 2026-11-20

First public release.

### Added

- Connections from `Config` (JSON/YAML) or the `Connector` builder for `postgres` (lib/pq), `pgx`,
  `clickhouse` and `sqlite`, with pool defaults (10 open / 10 idle, 30m lifetime, 5m idle time).
- `DB` interface with a single client (`NewDB`, `NewDBWithOptions`), a shard client (`NewShardDB`,
  `ConnectShards`, `EachShard`, `EachShardAsync`) and a primary/replica `Cluster` (`NewCluster`,
  `ConnectCluster`, lag tracking with hysteresis, `PreferReplica`, `RequireReplica`, `ForcePrimary`,
  `RoundRobin`, `LeastLag`, `WithStateHook`, recovery conflict retry).
- Context-propagated transactions: `Transactor`, `NewTransactor`, `WithTx`, `BeginTx`, `ReadOnly`,
  `Isolation`, `Join`, `RunTransactionContext`, `SetTx`, `GetTx`.
- Pagination: `OffsetPager`, `CursorPager`, `NewPager`, `AllPager`, `PageOf`, versioned page tokens,
  and the `sqlbpager` subpackage for go-sqlbuilder.
- Migrations with golang-migrate: `Migrator`, `MigrateContext`.
- Error classification: `Classify`, `IsDuplicate`, `IsForeignKey`, `IsNotNull`, `IsNoRows`,
  `DuplicateFields`; the `duplicate` subpackage.
- **SQLite support** (`SQLiteDriver = "sqlite"`, pure-Go `modernc.org/sqlite`, no cgo):
  - `Config.DSN` and `Config.SQLite` (`SQLiteOptions`), `SQLiteDSN`: WAL, `synchronous=NORMAL`,
    `foreign_keys=on`, `busy_timeout=5000`, `IMMEDIATE` write transactions and the sortable SQLite
    time format by default; extra pragmas via `SQLiteOptions.Pragmas`.
  - In-memory databases (`:memory:`, `mode=memory`) get a single connection that never expires.
  - `Migrator` runs SQLite migrations on the pool and keeps it open.
  - `Classify` / `DuplicateFields` understand SQLite constraint errors.
  - `sqlite` is registered with sqlx as a `?` bind driver.
- `Migrator.WithFS(fsys, dir)` for `embed.FS` migrations and `Migrator.WithMigrationsTable(name)`.
- `ErrCheckViolation` and `IsCheckViolation` (PostgreSQL `23514`, SQLite `SQLITE_CONSTRAINT_CHECK`).
- Portable placeholders: `DriverName(db)`, `Rebind(db, query)`, `Placeholder(driver, start)`, `Question()`.
- `Config.DSN` for every driver: a ready connection string used as is (`Validate` then skips the
  host/credential checks). `ErrConfigNoDSN`, `ErrMigrateUnsupportedDriver`.
- Runnable examples (`examples/sqlite`, `examples/postgres`, `examples/cluster`, `Example*` tests),
  CI workflow, Makefile, golangci-lint config.

### Deprecated

- `Atomic` (use `WithTx` with `Join()`), `RunTransaction` (use `RunTransactionContext`),
  `HandleError` (use `Classify` / `Is*`), `Duplicate` (use `IsDuplicate` and `DuplicateFields`),
  `Migrator.Migrate` (use `MigrateContext`), the `Transaction` interface (use `Transactor`).

[Unreleased]: https://github.com/boostgo/sqlkit/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/boostgo/sqlkit/releases/tag/v1.0.0
