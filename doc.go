// Package sqlkit is a thin, batteries-included layer over [github.com/jmoiron/sqlx] for
// PostgreSQL, SQLite and ClickHouse.
//
// It provides:
//
//   - Connections from a Config (JSON/YAML friendly) or a Connector builder, with sane pool defaults.
//     Drivers: "postgres" (lib/pq, default), "pgx" (jackc/pgx), "sqlite" (modernc.org/sqlite, pure Go,
//     no cgo) and "clickhouse" (mailru/go-clickhouse).
//   - A DB interface implemented by a single client (NewDB), a shard client (NewShardDB) and a
//     primary/replica Cluster with replication lag tracking (NewCluster, ConnectCluster).
//   - Context-propagated transactions: Transactor, WithTx, BeginTx with ReadOnly, Isolation and Join.
//     Every DB method runs in the transaction carried by ctx, so repositories need no tx parameter.
//   - Offset and keyset (cursor) pagination with opaque page tokens: OffsetPager, CursorPager,
//     NewPager, PageOf; a go-sqlbuilder adapter lives in the sqlbpager subpackage.
//   - golang-migrate migrations from a directory or an embed.FS for PostgreSQL and SQLite: Migrator.
//   - Driver-independent error classification: Classify, IsDuplicate, IsForeignKey, IsNotNull,
//     IsCheckViolation, DuplicateFields.
//
// # SQLite
//
// A Config with Driver "sqlite" uses DSN as the database file path (or ":memory:", or a "file:" URI)
// and adds connection pragmas: WAL journal, synchronous=NORMAL, foreign_keys=on, busy_timeout=5000
// and IMMEDIATE write transactions (see SQLiteOptions and SQLiteDSN):
//
//	conn, err := sqlkit.ConnectFromConfig(sqlkit.Config{Driver: sqlkit.SQLiteDriver, DSN: "app.db"})
//
// SQLite uses "?" placeholders. Queries written with "?" can be rebound for any driver with Rebind,
// and Placeholder returns the placeholder func of a driver for Pager.Where. "$N" placeholders work
// with SQLite too, so Arguments and Dollar can be shared with PostgreSQL code.
//
// # Transactions
//
// A Transactor stores the transaction in the context; the clients pick it up automatically:
//
//	err := sqlkit.WithTx(ctx, tr, func(ctx context.Context) error {
//		_, err := db.ExecContext(ctx, "UPDATE accounts SET balance = balance - ? WHERE id = ?", 10, 1)
//		return err
//	})
//
// Without Join, a nested WithTx opens a second transaction on another connection; pass Join() to reuse
// the transaction already in ctx.
package sqlkit
