package sqlkit

import (
	"errors"
)

// Connection errors.
var (
	// ErrOpenConnect wraps an error of sql.Open.
	ErrOpenConnect = errors.New("sqlkit.open_connect")
	// ErrPing wraps an error of the initial ping of Connect; the pool is closed.
	ErrPing = errors.New("sqlkit.ping")

	// ErrConnectionStringEmpty is returned by ConnectShards for a shard without a connection string.
	ErrConnectionStringEmpty = errors.New("sqlkit.connection_string_empty")
	// ErrConnectionStringDuplicate is returned by ConnectShards for a repeated shard key.
	ErrConnectionStringDuplicate = errors.New("sqlkit.connection_string_duplicate")
	// ErrConnectionIsNotShard is returned by EachShard/EachShardAsync for a DB that is not a shard client.
	ErrConnectionIsNotShard = errors.New("sqlkit.connection_is_not_shard")
	// ErrConnectionKeyEmpty is returned by ConnectShards for a shard without a key.
	ErrConnectionKeyEmpty = errors.New("sqlkit.connection_key_empty")

	// ErrConnNotSelected is returned by the shard client when its ConnectionSelector returns nil.
	ErrConnNotSelected = errors.New("sqlkit.connection.not_selected")

	// ErrMethodNotSupportedInSingle is returned by the shard-only methods of the single client and Cluster.
	ErrMethodNotSupportedInSingle = errors.New("sqlkit.method_not_supported_in_single")
)

// Config errors returned by Config.Validate.
var (
	// ErrConfigNoHost means Config.Host is empty.
	ErrConfigNoHost = errors.New("sqlkit.config.no_host")
	// ErrConfigNoPort means Config.Port is 0.
	ErrConfigNoPort = errors.New("sqlkit.config.no_port")
	// ErrConfigNoDatabase means Config.Database is empty.
	ErrConfigNoDatabase = errors.New("sqlkit.config.no_database")
	// ErrConfigNoUsername means Config.Username is empty.
	ErrConfigNoUsername = errors.New("sqlkit.config.no_username")
	// ErrConfigNoPassword means Config.Password is empty.
	ErrConfigNoPassword = errors.New("sqlkit.config.no_password")
	// ErrConfigNoDSN means Config.DSN is empty for a driver that needs it (SQLite).
	ErrConfigNoDSN = errors.New("sqlkit.config.no_dsn")
)

// Migration errors returned by Migrator.
var (
	// ErrMigrateOpenConn means a connection for the migration could not be taken from the pool.
	ErrMigrateOpenConn = errors.New("sqlkit.migrate.open_conn")
	// ErrMigrateGetDriver wraps an error of the golang-migrate database driver.
	ErrMigrateGetDriver = errors.New("sqlkit.migrate.get_driver")
	// ErrMigrateLock means the PostgreSQL lock_timeout could not be set.
	ErrMigrateLock = errors.New("sqlkit.migrate.lock")
	// ErrMigrateReadMigrationsDir means the migrations could not be read.
	ErrMigrateReadMigrationsDir = errors.New("sqlkit.migrate.read_migrations_dir")
	// ErrMigrateUp wraps an error of applying the migrations.
	ErrMigrateUp = errors.New("sqlkit.migrate.up")
	// ErrMigrateUnsupportedDriver means the driver of the connection has no migration support (ClickHouse).
	ErrMigrateUnsupportedDriver = errors.New("sqlkit.migrate.unsupported_driver")
)

// Transaction errors.
var (
	// ErrTransactorBegin wraps an error of beginning a transaction.
	ErrTransactorBegin = errors.New("sqlkit.transactor.begin")
	// ErrTransactorCommit wraps an error of Commit.
	ErrTransactorCommit = errors.New("sqlkit.transactor.commit")
	// ErrTransactorRollback wraps an error of Rollback.
	ErrTransactorRollback = errors.New("sqlkit.transactor.rollback")

	// ErrTxOptionNotSupported is returned by BeginTx/WithTx if tx options are given to a Transactor that
	// can not honor them.
	ErrTxOptionNotSupported = errors.New("sqlkit.transactor.option_not_supported")
)

// Statement errors, reserved for prepared statement helpers.
var (
	// ErrStatementPrepare wraps an error of preparing a statement.
	ErrStatementPrepare = errors.New("sqlkit.statement.prepare")
	// ErrStatementExecuteQuery wraps an error of executing a prepared statement.
	ErrStatementExecuteQuery = errors.New("sqlkit.statement.execute_query")
)

// Pagination errors returned by Pager.Err.
var (
	// ErrInvalidCursor is a broken page token: a client error (e.g. HTTP 400).
	ErrInvalidCursor = errors.New("sqlkit.pager.invalid_cursor")
	// ErrInvalidPager is a Pager configuration error in code: a server error (e.g. HTTP 500).
	ErrInvalidPager = errors.New("sqlkit.pager.invalid")
)

// Constraint violations returned by Classify (PostgreSQL and SQLite).
var (
	// ErrDuplicate is a unique or primary key violation.
	ErrDuplicate = errors.New("sqlkit.duplicate")
	// ErrForeignKeyViolation is a foreign key violation.
	ErrForeignKeyViolation = errors.New("sqlkit.foreign_key_violation")
	// ErrNotNull is a NOT NULL violation.
	ErrNotNull = errors.New("sqlkit.not_null")
	// ErrCheckViolation is a CHECK constraint violation.
	ErrCheckViolation = errors.New("sqlkit.check_violation")
)

// Cluster errors.
var (
	// ErrClusterNoPrimary is returned by NewCluster if the primary connection is nil.
	ErrClusterNoPrimary = errors.New("sqlkit.cluster.no_primary")
	// ErrClusterOption is returned by NewCluster and ConnectCluster for an invalid option or config.
	ErrClusterOption = errors.New("sqlkit.cluster.option")
	// ErrReplicaUnavailable is returned by read methods of Cluster called with RequireReplica(ctx) when
	// no replica is usable.
	ErrReplicaUnavailable = errors.New("sqlkit.cluster.replica_unavailable")
)

// Method errors: every error of a DB method is wrapped with the sentinel of that method, so
// errors.Is(err, ErrExecContext) tells where it happened and errors.As still finds the driver error.
var (
	// ErrNamedQueryRowxContext wraps errors of NamedQueryRowxContext.
	ErrNamedQueryRowxContext = errors.New("sqlkit.NamedQueryRowxContext")
	// ErrPrepareNamedContext wraps errors of PrepareNamedContext.
	ErrPrepareNamedContext = errors.New("sqlkit.PrepareNamedContext")
	// ErrGetContext wraps errors of GetContext.
	ErrGetContext = errors.New("sqlkit.GetContext")
	// ErrSelectContext wraps errors of SelectContext.
	ErrSelectContext = errors.New("sqlkit.SelectContext")
	// ErrNamedExecContext wraps errors of NamedExecContext.
	ErrNamedExecContext = errors.New("sqlkit.NamedExecContext")
	// ErrPrepareContext wraps errors of PrepareContext.
	ErrPrepareContext = errors.New("sqlkit.PrepareContext")
	// ErrQueryxContext wraps errors of QueryxContext.
	ErrQueryxContext = errors.New("sqlkit.QueryxContext")
	// ErrQueryContext wraps errors of QueryContext.
	ErrQueryContext = errors.New("sqlkit.QueryContext")
	// ErrQueryRowxContext wraps errors of QueryRowxContext (carried by the returned row).
	ErrQueryRowxContext = errors.New("sqlkit.QueryRowxContext")
	// ErrExecContext wraps errors of ExecContext.
	ErrExecContext = errors.New("sqlkit.ExecContext")
)
