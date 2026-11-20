package sqlkit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

// Connect to the database.
//
// "options" can override default settings
func Connect(
	driverName, connectionString string,
	timeout time.Duration,
	options ...func(connection *sqlx.DB),
) (*sqlx.DB, error) {
	driverName, connectionString = normalizeDriver(driverName, connectionString)

	connection, err := openPool(driverName, connectionString, options...)
	if err != nil {
		return nil, err
	}

	// make ping
	ctx := context.Background()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	if err = connection.PingContext(ctx); err != nil {
		err = fmt.Errorf("%w: %w", ErrPing, err)
		if closeErr := connection.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}

		return nil, err
	}

	return connection, nil
}

// normalizeDriver sets the default driver and drops binary_parameters where the driver does not know it.
func normalizeDriver(driverName, connectionString string) (string, string) {
	if driverName == "" {
		driverName = PqDriver
	}

	if driverName != PqDriver {
		connectionString = strings.ReplaceAll(connectionString, " binary_parameters=yes", "")
	}

	return driverName, connectionString
}

// openPool opens a pool without ping: sqlx.Open, default pool settings, then options.
// driverName and connectionString must be normalized by the caller (see normalizeDriver).
func openPool(driverName, connectionString string, options ...func(connection *sqlx.DB)) (*sqlx.DB, error) {
	connection, err := sqlx.Open(driverName, connectionString)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrOpenConnect, err)
	}

	// set default settings
	connection.SetMaxOpenConns(defaultMaxOpenConnections)
	connection.SetMaxIdleConns(defaultMaxIdleConnections)
	connection.SetConnMaxLifetime(defaultMaxConnLifetime)
	connection.SetConnMaxIdleTime(defaultMaxIdleTime)

	if isSQLiteMemory(driverName, connectionString) {
		sqliteMemoryPool(connection)
	}

	// apply options
	for _, option := range options {
		option(connection)
	}

	return connection, nil
}

// openFromConfig is ConnectFromConfig without ping.
func openFromConfig(cfg Config) (*sqlx.DB, error) {
	driverName, connectionString := normalizeDriver(cfg.Driver, cfg.ConnectionString())

	return openPool(driverName, connectionString, poolOptionsFromConfig(cfg)...)
}

// ConnectFromConfig connects with the driver and connection string of cfg (see Config.ConnectionString)
// and pings the database, with a timeout of cfg.TimeoutMs if it is set.
//
// Pool settings are applied in order: the defaults, the non-zero pool fields of cfg, then opts.
// An in-memory SQLite database always gets a single, never expiring connection.
func ConnectFromConfig(cfg Config, opts ...func(connection *sqlx.DB)) (*sqlx.DB, error) {
	var timeout time.Duration
	if cfg.TimeoutMs > 0 {
		timeout = time.Duration(cfg.TimeoutMs) * time.Millisecond
	}

	// order: Connect defaults -> Config values (> 0) -> caller opts
	all := append(poolOptionsFromConfig(cfg), opts...)

	return Connect(cfg.Driver, cfg.ConnectionString(), timeout, all...)
}

// poolOptionsFromConfig applies every pool field of cfg separately, if it is > 0.
func poolOptionsFromConfig(cfg Config) []func(connection *sqlx.DB) {
	var opts []func(connection *sqlx.DB)

	if cfg.MaxOpenConnections > 0 {
		opts = append(opts, WithMaxOpen(cfg.MaxOpenConnections))
	}

	if cfg.MaxIdleConnections > 0 {
		opts = append(opts, WithMaxIdle(cfg.MaxIdleConnections))
	}

	if cfg.MaxLifetime > 0 {
		opts = append(opts, WithConnMaxLifetime(time.Duration(cfg.MaxLifetime)*time.Second))
	}

	if cfg.MaxIdleTime > 0 {
		opts = append(opts, WithConnMaxIdleTime(time.Duration(cfg.MaxIdleTime)*time.Second))
	}

	return opts
}
