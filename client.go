package sqlkit

import (
	"context"
	"database/sql"

	"github.com/jmoiron/sqlx"
)

// DB is the database client of the package: the context-aware query methods of sqlx plus shard helpers.
//
// It is implemented by the single client (NewDB, NewDBWithOptions), the shard client (NewShardDB) and
// Cluster. Every query method runs in the transaction carried by ctx (see SetTx, Transactor), if any.
type DB interface {
	Connection() *sqlx.DB
	sqlx.ExecerContext
	sqlx.QueryerContext
	sqlx.PreparerContext
	GetContext
	NamedExecContext
	SelectContext
	PrepareContext
	NamedQueryRowxContext
	EachShard(fn func(conn DB) error) error
	EachShardAsync(fn func(conn DB) error, limit ...int) error
}

// NamedExecContext is the NamedExecContext method of sqlx.
type NamedExecContext interface {
	NamedExecContext(ctx context.Context, query string, arg interface{}) (sql.Result, error)
}

// SelectContext is the SelectContext method of sqlx.
type SelectContext interface {
	SelectContext(ctx context.Context, dest interface{}, query string, args ...interface{}) error
}

// GetContext is the GetContext method of sqlx.
type GetContext interface {
	GetContext(ctx context.Context, dest interface{}, query string, args ...interface{}) error
}

// PrepareContext is the PrepareNamedContext method of sqlx.
type PrepareContext interface {
	PrepareNamedContext(ctx context.Context, query string) (*sqlx.NamedStmt, error)
}

// NamedQueryRowxContext runs a query with named arguments (":name") and returns one row. The
// placeholders are converted to the bind style of the driver ("$N" or "?").
type NamedQueryRowxContext interface {
	NamedQueryRowxContext(ctx context.Context, query string, arg any) (*sqlx.Row, error)
}
