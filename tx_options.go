package sqlkit

import "database/sql"

// txConfig holds options of a transaction opened by BeginTx / WithTx.
type txConfig struct {
	isolation sql.IsolationLevel
	readOnly  bool
	join      bool
}

// TxOption configures a transaction. See ReadOnly, Isolation and Join.
type TxOption func(*txConfig)

// ReadOnly makes the transaction read-only.
func ReadOnly() TxOption {
	return func(c *txConfig) { c.readOnly = true }
}

// Isolation sets the isolation level of the transaction (default: sql.LevelReadCommitted).
func Isolation(level sql.IsolationLevel) TxOption {
	return func(c *txConfig) { c.isolation = level }
}

// Join makes BeginTx / WithTx reuse the transaction already stored in ctx instead of opening a second one.
//
// Commit and Rollback of the joined level are no-ops: the outer level commits or rolls back.
// Without a transaction in ctx, Join has no effect and a new transaction is opened.
func Join() TxOption {
	return func(c *txConfig) { c.join = true }
}

func newTxConfig(opts []TxOption) txConfig {
	cfg := txConfig{isolation: sql.LevelReadCommitted}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	return cfg
}
